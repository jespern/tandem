import XCTest
import Tandem
@testable import TandemEngine

/// Garbage collection beyond §10.7 (GC.swift): past epochs' writes, gone lists, and their files. The Go engine's
/// gc_test.go, case for case.
final class GCTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")
    let small = BlobSettings(chunkSize: 1000, inline: 100)

    struct Missing: Error {}
    func some<T>(_ v: T?) throws -> T { guard let v else { throw Missing() }; return v }
    func content(_ n: Int) -> Bytes { (0..<n).map { _ in UInt8.random(in: 0...255) } }

    struct World {
        let hub: MemoryHub, f: Founding, bobKeys: DeviceKeys, bobStore: Store, bob: Device
        let alice: Device, aliceKeys: DeviceKeys, carol: Device, aliceID: ID, carolID: ID, house: Bytes
    }

    /// Bob owns House, shared with Alice and Carol as writers; Milk is Bob's, Bread Alice's.
    func world() async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys(), bobStore = try Store(path: ":memory:"), aliceKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: bobStore, hub: await hub.connect())
        try await bob.publish(f)
        var members: [Device] = []
        for (name, keys) in [("Alice", aliceKeys), ("Carol", DeviceKeys())] {
            let link = try await bob.invite(label: name, account: account, ms: f.managementSeed)
            members.append(try await Device.join(link: link, keys: keys, label: name + "’s phone", store: try Store(path: ":memory:"), hub: await hub.connect()))
        }
        try await bob.sync()
        let house = try await bob.createList(name: "House")
        try await bob.setField(list: house, record: bob.newRecordID(), field: "title", value: Bytes("Milk".utf8))
        var ids: [ID] = []
        for m in members {
            let id = try some(await m.me?.participant)
            ids.append(id)
            try await bob.share(list: house, with: id, role: .writer)
            try await m.sync()
            try await m.accept(list: house)
        }
        try await members[0].setField(list: house, record: members[0].newRecordID(), field: "title", value: Bytes("Bread".utf8))
        try await bob.sync()
        return World(hub: hub, f: f, bobKeys: bobKeys, bobStore: bobStore, bob: bob, alice: members[0], aliceKeys: aliceKeys,
                     carol: members[1], aliceID: ids[0], carolID: ids[1], house: house)
    }

    func titles(_ d: Device, _ list: Bytes) async -> Set<String> {
        var out = Set<String>()
        for r in await d.records(list: list) {
            if let t = await d.value(list: list, record: r, field: "title") { out.insert(String(decoding: t, as: UTF8.self)) }
        }
        return out
    }

    func sync(_ ds: Device...) async throws { for d in ds { try await d.sync() } }

    /// The data writes a device holds from epochs before the current one that the current inventory doesn't list.
    func pastWrites(_ d: Device, _ list: Bytes) async throws -> Int {
        let c = try some(await d.collections[list])
        let current = try some(c.head.flatMap { c.epoch(of: $0) })
        let inv = await d.inventory(current)?.entries ?? [:]
        let store = await d.store
        return try store.writeIDs(scope: list).filter { $0.record != Roster.record && $0.epoch != current && inv[$0.record]?.contains($0.id) != true }.count
    }

    func dataWrites(_ s: Store, _ list: Bytes) throws -> Int { try s.writeIDs(scope: list).filter { $0.record != Roster.record }.count }

    /// §9.3: once the next inventory is here, what it doesn't list goes; the state, and a restart, don't change.
    func testPastWritesGoAfterAKeyChange() async throws {
        let w = try await world()
        let bread = w.alice.newRecordID()
        for v in ["Rye", "Spelt", "Sourdough"] { try await w.alice.setField(list: w.house, record: bread, field: "title", value: Bytes(v.utf8)) }
        try await w.bob.sync()
        let before = try dataWrites(w.bobStore, w.house)
        try await w.bob.remove(w.carolID, from: w.house)
        try await w.carol.setField(list: w.house, record: w.carol.newRecordID(), field: "title", value: Bytes("Late".utf8))
        try await w.alice.sync()
        try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "title", value: Bytes("Eggs".utf8))
        try await sync(w.bob, w.carol, w.alice, w.bob)
        for d in [w.bob, w.alice] {
            let n = try await pastWrites(d, w.house)
            XCTAssertEqual(n, 0, "unlisted writes of the past epoch kept")
        }
        XCTAssertLessThan(try dataWrites(w.bobStore, w.house), before)
        let want: Set<String> = ["Milk", "Bread", "Sourdough", "Eggs"]
        let bobSees = await titles(w.bob, w.house)
        XCTAssertEqual(bobSees, want)
        let again = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.bobKeys, store: w.bobStore, hub: await w.hub.connect())
        let restarted = await titles(again, w.house)
        XCTAssertEqual(restarted, want, "after a restart")
        try await again.sync()
        let synced = await titles(again, w.house)
        XCTAssertEqual(synced, want, "after a restart and a sync")
    }

    /// A removed member's own late write stays in their replica for the retention period.
    func testLateWritesOfARemovedMemberStay() async throws {
        let w = try await world()
        try await w.bob.remove(w.carolID, from: w.house)
        try await w.carol.setField(list: w.house, record: w.carol.newRecordID(), field: "title", value: Bytes("Gone".utf8))
        try await sync(w.bob, w.alice, w.carol, w.carol)
        let carolSees = await titles(w.carol, w.house)
        XCTAssertTrue(carolSees.contains("Gone"))
    }

    /// §11.9: a deleted list stays, read-only, for the retention period; then its replica goes, all but the roster, and a
    /// device of the owner destroys its data on the hub, all but the roster. The list changed key first, so the new
    /// channel holds writes carried over from the old one.
    func testDeletedListAfterRetention() async throws {
        let w = try await world()
        try await w.bob.rekey(list: w.house)
        try await sync(w.alice, w.carol, w.bob, w.alice)
        let (epoch, key) = try some(await { () async -> (ID, Bytes)? in
            let c = await w.bob.collections[w.house]!
            guard let h = c.head, let e = c.epoch(of: h), let k = c.keys[e] else { return nil }
            return (e, k)
        }())
        let tag = Tandem.collectionTag(key: key, hub: w.f.pin.hub, epoch: epoch)
        func inChannel() async throws -> (data: Int, roster: Int) {
            var data = 0, roster = 0
            for m in await w.hub.messages where m.channel == tag {
                let (_, p) = try Envelope.open(key: key, hub: w.f.pin.hub, tag: tag, envelope: m.body)
                if case let .record(state)? = try? Kind1.parse(p, hub: w.f.pin.hub, scope: w.house), state.record == Roster.record { roster += 1 } else { data += 1 }
            }
            return (data, roster)
        }
        try await w.bob.delete(list: w.house)
        try await sync(w.alice, w.carol, w.bob)
        let kept = try await inChannel()
        XCTAssertGreaterThan(kept.data, 0, "the data stays on the hub for the retention period")
        let aliceStore = await w.alice.store
        XCTAssertGreaterThan(try dataWrites(aliceStore, w.house), 0, "Alice's replica stays for the retention period")
        for d in [w.alice, w.bob] { await d.setRetention(.nanoseconds(1)) }
        try await sync(w.bob, w.bob, w.alice, w.alice, w.alice)
        let after = try await inChannel()
        XCTAssertEqual(after.data, 0)
        XCTAssertGreaterThan(after.roster, 0)
        for d in [w.alice, w.bob] {
            let store = await d.store
            XCTAssertEqual(try dataWrites(store, w.house), 0, "writes of a collected list kept")
            let deleted = await d.isDeleted(list: w.house), gone = try await d.goneLists(), lists = await d.lists()
            XCTAssertTrue(deleted)
            XCTAssertEqual(gone.count, 1)
            XCTAssertTrue(lists.isEmpty)
        }
        // A device that comes later learns from the roster that the list was deleted.
        let late = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.aliceKeys, store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await late.sync()
        let learned = await late.isDeleted(list: w.house)
        XCTAssertTrue(learned)
    }

    /// §11.6: a removed member's replica stays, read-only, for the retention period, then goes; the hub is the owner's business.
    func testRemovedListAfterRetention() async throws {
        let w = try await world()
        try await w.bob.remove(w.carolID, from: w.house)
        try await sync(w.alice, w.carol)
        let removed = await w.carol.removedLists(), seen = await titles(w.carol, w.house)
        XCTAssertEqual(removed.count, 1)
        XCTAssertFalse(seen.isEmpty)
        let hubBefore = await w.hub.messages.count
        await w.carol.setRetention(.nanoseconds(1))
        try await w.carol.sync()
        let after = await w.carol.removedLists(), left = await titles(w.carol, w.house)
        XCTAssertTrue(after.isEmpty)
        XCTAssertTrue(left.isEmpty)
        let carolStore = await w.carol.store
        XCTAssertEqual(try dataWrites(carolStore, w.house), 0)
        let hubAfter = await w.hub.messages.count
        XCTAssertEqual(hubAfter, hubBefore, "a removed member's device destroys nothing on the hub")
        let aliceSees = await titles(w.alice, w.house)
        XCTAssertEqual(aliceSees, ["Milk", "Bread"])
    }

    // MARK: Files

    struct BlobWorld { let hub: MemoryHub; let blobs: MemoryBlobHub; let bob: Device; let alice: Device; let list: Bytes }

    func blobWorld() async throws -> BlobWorld {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.publish(f)
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let alice = try await Device.join(link: link, keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.sync()
        let list = try await bob.createList(name: "Photos")
        try await bob.share(list: list, with: try some(await alice.me?.participant), role: .writer, name: "Alice")
        try await alice.sync()
        try await alice.accept(list: list)
        try await sync(bob, alice)
        let blobs = MemoryBlobHub()
        try await bob.useBlobs(blobs, settings: small)
        try await alice.useBlobs(blobs, settings: small)
        await bob.settleBlobs(); await alice.settleBlobs()
        return BlobWorld(hub: hub, blobs: blobs, bob: bob, alice: alice, list: list)
    }

    func clean(_ ds: Device...) async throws { for d in ds { try await d.sync(); await d.settleBlobs() } }

    func manifest(_ d: Device, _ list: Bytes, _ record: Bytes, _ name: String) async throws -> Blob.Manifest {
        let v = try some(await d.value(list: list, record: record, field: "_blob:" + name))
        guard case let .manifest(m) = Blob.parse(v) else { throw TandemError.invalid("not a manifest") }
        return m
    }

    func onHub(_ h: MemoryBlobHub, _ m: Blob.Manifest) async -> Int {
        var n = 0
        for e in m.chunks ?? [] { n += await h.withAddress(e.address).count }
        return n
    }

    func fetch(_ d: Device, _ list: Bytes, _ record: Bytes, _ name: String) async throws {
        _ = try await d.fetchBlob(list: list, record: record, name: name)
    }

    func file(_ d: Device, _ m: Blob.Manifest) async -> String { await d.store.blobDirectory.appendingPathComponent(m.hash.hex).path }

    /// Files of a deleted list: kept while the replica is, then the owner's device destroys every chunk and each device
    /// drops what it downloaded (DESIGN.md §17.7).
    func testDeletedListsFilesGo() async throws {
        let w = try await blobWorld()
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        try await w.alice.sync()
        try await fetch(w.alice, w.list, photo, "photo")
        let m = try await manifest(w.bob, w.list, photo, "photo")
        let aliceFile = await file(w.alice, m)
        try await w.bob.delete(list: w.list)
        try await clean(w.alice, w.bob)
        let kept = await onHub(w.blobs, m)
        XCTAssertEqual(kept, m.chunks!.count, "kept for the retention period")
        XCTAssertTrue(FileManager.default.fileExists(atPath: aliceFile))
        for d in [w.alice, w.bob] { await d.setRetention(.nanoseconds(1)) }
        try await clean(w.alice, w.bob, w.alice, w.bob)
        let left = await onHub(w.blobs, m)
        XCTAssertEqual(left, 0, "chunks of a deleted list left")
        XCTAssertFalse(FileManager.default.fileExists(atPath: aliceFile), "Alice's copy of a collected list's file stays")
    }

    /// A downloaded file no record names any more goes: a replaced version's, a deleted record's.
    func testUnnamedFilesGo() async throws {
        let w = try await blobWorld()
        let photo = w.bob.newRecordID(), doc = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        try await w.bob.attach(list: w.list, record: doc, name: "file", content: content(1500))
        await w.bob.settleBlobs()
        try await w.alice.sync()
        try await fetch(w.alice, w.list, photo, "photo")
        try await fetch(w.alice, w.list, doc, "file")
        let old = await file(w.alice, try await manifest(w.alice, w.list, photo, "photo"))
        let gone = await file(w.alice, try await manifest(w.alice, w.list, doc, "file"))
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2600))
        try await w.bob.deleteRecord(list: w.list, record: doc)
        await w.bob.settleBlobs()
        try await w.bob.sync()
        try await clean(w.alice)
        let newer = await file(w.alice, try await manifest(w.alice, w.list, photo, "photo"))
        try await fetch(w.alice, w.list, photo, "photo")
        try await clean(w.alice)
        let fm = FileManager.default
        XCTAssertFalse(fm.fileExists(atPath: old) || fm.fileExists(atPath: gone), "unnamed content stays")
        XCTAssertTrue(fm.fileExists(atPath: newer), "the current version was dropped")
        let store = await w.alice.store
        XCTAssertNil(try store.value("blobs.opened." + URL(fileURLWithPath: old).lastPathComponent))
        let partial = try fm.contentsOfDirectory(atPath: store.blobDirectory.appendingPathComponent("partial").path)
        XCTAssertEqual(partial, [], "partial downloads left")
    }

    /// A replaced version whose chunks were still on the hub when its write was dropped is collected all the same: the
    /// dropped manifest still accounts for them (DESIGN.md §17.7, condition 2).
    func testDroppedManifestsChunksAreStillCollected() async throws {
        let w = try await blobWorld()
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        let m1 = try await manifest(w.bob, w.list, photo, "photo")
        await w.blobs.set(failDestroys: true)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2600))
        await w.bob.settleBlobs()
        try await w.alice.sync()
        await w.alice.settleBlobs()
        let stays = await onHub(w.blobs, m1)
        XCTAssertGreaterThan(stays, 0, "the destroy was meant to fail")
        try await w.bob.remove(try some(await w.alice.me?.participant), from: w.list)
        try await clean(w.bob, w.bob)
        let past = try await pastWrites(w.bob, w.list)
        XCTAssertEqual(past, 0, "the replaced write was dropped")
        await w.blobs.set(failDestroys: false)
        try await clean(w.bob, w.bob)
        let left = await onHub(w.blobs, m1)
        XCTAssertEqual(left, 0, "chunks of a dropped manifest left")
        let bobStore = await w.bob.store
        XCTAssertTrue(try bobStore.values(prefix: "blobs.dropped.").isEmpty, "forgotten once its chunks are gone")
    }
}
