import Foundation
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

    /// The number of writes in a device's state of a list.
    func stateWrites(_ d: Device, _ list: Bytes) async -> Int {
        await d.collections[list]!.records.filter { $0.key != Roster.record }.values.reduce(0) { $0 + $1.kept.count }
    }

    func writes(_ d: Device, _ list: Bytes, _ record: Bytes) async throws -> Int {
        try await d.store.writes(scope: list, record: record).count
    }

    /// The hub messages of the current channel that hold a record.
    func messagesOf(_ hub: MemoryHub, _ d: Device, _ list: Bytes, _ record: Bytes) async throws -> Int {
        let c = await d.collections[list]!
        let epoch = c.epoch(of: c.head!)!, key = c.keys[epoch]!
        let tag = Tandem.collectionTag(key: key, hub: d.pin.hub, epoch: epoch)
        var n = 0
        for m in await hub.messages where m.channel == tag {
            let (_, p) = try Envelope.open(key: key, hub: d.pin.hub, tag: tag, envelope: m.body)
            if case let .record(state)? = try? Kind1.parse(p, hub: d.pin.hub, scope: list), state.record == record { n += 1 }
        }
        return n
    }

    /// GC.md §2: within an epoch a replaced write goes once its replacement is on the feed and no message holds it; an
    /// own write only for a later own one. The state doesn't change, the hub converges to one message per record, and
    /// a key change and a restart change nothing.
    func testReplacedWritesGoWithinAnEpoch() async throws {
        let w = try await world()
        let shelf = w.alice.newRecordID(), lamp = w.alice.newRecordID(), desk = w.alice.newRecordID()
        for v in ["1", "2", "3", "4", "5"] { try await w.alice.setField(list: w.house, record: shelf, field: "title", value: Bytes(v.utf8)) }
        try await w.alice.setField(list: w.house, record: lamp, field: "title", value: Bytes("Alice's".utf8))
        // Concurrent changes to two fields: the merged state is published and dominates both.
        try await w.alice.write(list: w.house, record: desk, [.set("title", Bytes("Desk".utf8)), .set("notes", Bytes("oak".utf8))])
        try await w.bob.sync()
        try await w.bob.setField(list: w.house, record: lamp, field: "title", value: Bytes("Bob's".utf8))
        try await w.alice.setField(list: w.house, record: desk, field: "title", value: Bytes("Table".utf8))
        try await w.bob.setField(list: w.house, record: desk, field: "notes", value: Bytes("beech".utf8))
        for _ in 0..<3 { try await sync(w.alice, w.bob, w.carol) }
        for d in [w.alice, w.bob, w.carol] {
            let n = try await writes(d, w.house, shelf)
            XCTAssertEqual(n, 1, "writes of a record edited five times")
        }
        let aliceLamp = try await writes(w.alice, w.house, lamp), bobLamp = try await writes(w.bob, w.house, lamp)
        XCTAssertEqual(aliceLamp, 2, "Alice keeps her own write that Bob's replaced, until the next inventory")
        XCTAssertEqual(bobLamp, 1, "Bob drops Alice's write that his replaced")
        for r in [shelf, desk] {
            let n = try await messagesOf(w.hub, w.bob, w.house, r)
            XCTAssertEqual(n, 1, "messages of a record on the hub")
        }
        let want: Set<String> = ["Milk", "Bread", "5", "Bob's", "Table"]
        for d in [w.alice, w.bob, w.carol] {
            let got = await titles(d, w.house)
            XCTAssertEqual(got, want)
        }
        try await w.bob.rekey(list: w.house)
        try await sync(w.alice, w.carol, w.bob, w.alice)
        for d in [w.alice, w.bob, w.carol] {
            let got = await titles(d, w.house)
            XCTAssertEqual(got, want, "after a key change")
        }
        let again = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.bobKeys, store: w.bobStore, hub: await w.hub.connect())
        let restarted = await titles(again, w.house)
        XCTAssertEqual(restarted, want, "after a restart")
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
        let inState = await stateWrites(w.bob, w.house)
        XCTAssertEqual(try dataWrites(w.bobStore, w.house), inState, "Bob holds exactly his state (\(before) writes before the key change)")
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

    /// The statement IDs that roster messages of a list's epoch channel hold.
    func rosterIn(_ hub: MemoryHub, _ d: Device, _ list: Bytes, _ epoch: ID) async throws -> Set<Bytes> {
        let key = await d.collections[list]!.keys[epoch]!
        let tag = Tandem.collectionTag(key: key, hub: d.pin.hub, epoch: epoch)
        var out: Set<Bytes> = []
        for m in await hub.messages where m.channel == tag {
            let (_, p) = try Envelope.open(key: key, hub: d.pin.hub, tag: tag, envelope: m.body)
            guard case let .record(state)? = try? Kind1.parse(p, hub: d.pin.hub, scope: list), state.record == Roster.record else { continue }
            for var w in state.writes {
                if w.ref == Bytes(repeating: 0, count: 32) { w.scope = Roster.zeroScope }
                out.insert(try w.id())
            }
        }
        return out
    }

    /// GC.md §4.3: a channel a later epoch replaced keeps only its epoch's statements and the announcement of the next;
    /// a device away for several key changes still finds its way.
    func testOldChannelsKeepTheirOwnStatements() async throws {
        let w = try await world()
        try await w.bob.rekey(list: w.house)                              // epoch 1
        try await w.bob.setRole(.reader, for: w.carolID, in: w.house)     // epoch 2
        try await w.bob.setRole(.writer, for: w.carolID, in: w.house)     // the same epoch
        try await w.bob.rekey(list: w.house)                              // epoch 3
        try await sync(w.bob, w.bob)
        let c = await w.bob.collections[w.house]!
        let line = await w.bob.chain(c, to: c.head!)
        let current = c.epoch(of: c.head!)!
        let epochs = Set(line.compactMap { c.epoch(of: $0) })
        XCTAssertEqual(epochs.count, 4)
        for epoch in epochs where epoch != current && epoch != c.genesis {
            let held = try await rosterIn(w.hub, w.bob, w.house, epoch)
            var want: Set<Bytes> = []
            for (i, id) in line.enumerated() where c.epoch(of: id) == epoch {
                want.insert(id)
                if i + 1 < line.count { want.insert(line[i + 1]) }
            }
            XCTAssertEqual(held, want, "an old channel keeps exactly its epoch's statements")
        }
        let whole = try await rosterIn(w.hub, w.bob, w.house, current)
        XCTAssertEqual(whole.count, line.count, "the current channel keeps the whole chain")
        try await sync(w.alice, w.carol)
        let roles = (await w.alice.role(list: w.house), await w.carol.role(list: w.house))
        XCTAssertEqual(roles.0, .writer)
        XCTAssertEqual(roles.1, .writer)
        try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "title", value: Bytes("Back".utf8))
        try await w.bob.sync()
        let bobSees = await titles(w.bob, w.house)
        XCTAssertEqual(bobSees, ["Milk", "Bread", "Back"])
    }

    /// In a list's current channel: the messages that hold one of the given records alone, and the packs.
    func tombstoneMessages(_ hub: MemoryHub, _ d: Device, _ list: Bytes, _ records: Set<Bytes>) async throws -> (single: Int, packs: Int) {
        let c = await d.collections[list]!
        let epoch = c.epoch(of: c.head!)!, key = c.keys[epoch]!
        let tag = Tandem.collectionTag(key: key, hub: d.pin.hub, epoch: epoch)
        var single = 0, packs = 0
        for m in await hub.messages where m.channel == tag {
            let (_, p) = try Envelope.open(key: key, hub: d.pin.hub, tag: tag, envelope: m.body)
            switch try? Kind1.parse(p, hub: d.pin.hub, scope: list) {
            case .pack?: packs += 1
            case let .record(s)? where records.contains(s.record): single += 1
            default: break
            }
        }
        return (single, packs)
    }

    /// GC.md §3 and §10.7: past the threshold the owner's device packs the tombstones; the single messages are collected;
    /// every device, a new one too, still reads the records as deleted, and a key change carries them.
    func testTombstonesArePacked() async throws {
        let w = try await world()
        await w.bob.setPackThreshold(3)
        var gone = Set<Bytes>()
        for i in 0..<6 {
            let r = w.alice.newRecordID()
            try await w.alice.setField(list: w.house, record: r, field: "title", value: [UInt8(ascii: "a") + UInt8(i)])
            if i < 5 { try await w.alice.deleteRecord(list: w.house, record: r); gone.insert(r) }
        }
        for _ in 0..<3 { try await sync(w.alice, w.bob, w.carol) }
        let (single, packs) = try await tombstoneMessages(w.hub, w.bob, w.house, gone)
        XCTAssertEqual(single, 0, "single tombstone messages left")
        XCTAssertGreaterThan(packs, 0)
        let want: Set<String> = ["Milk", "Bread", "f"]
        for d in [w.alice, w.bob, w.carol] {
            let got = await titles(d, w.house)
            XCTAssertEqual(got, want)
        }
        let laptop = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.aliceKeys, store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await sync(laptop, laptop)
        let fresh = await titles(laptop, w.house)
        XCTAssertEqual(fresh, want, "a new device reads the packed tombstones")
        try await w.bob.rekey(list: w.house)
        try await sync(w.alice, w.carol, laptop, w.bob)
        for d in [w.alice, w.bob, w.carol, laptop] {
            let got = await titles(d, w.house)
            XCTAssertEqual(got, want, "after a key change")
        }
    }

    /// §8.3, §12.3: at a key change a deleted record becomes a mark, its tombstone isn't carried, a late edit of it isn't
    /// signed again, writing to it is refused, a write to it signed anyway is invalid, the mark is carried forward, and a
    /// new device never lists the record as live, but can query its deletion mark.
    func testDeletedRecordsBecomeMarks() async throws {
        let w = try await world()
        let shelf = w.alice.newRecordID()
        try await w.alice.setField(list: w.house, record: shelf, field: "title", value: Bytes("Shelf".utf8))
        try await w.carol.sync()
        try await w.alice.deleteRecord(list: w.house, record: shelf)
        try await w.bob.sync()
        try await w.bob.rekey(list: w.house)
        // Carol, not yet aware of the delete or the key change, edits the shelf.
        try await w.carol.setField(list: w.house, record: shelf, field: "title", value: Bytes("Late".utf8))
        try await sync(w.alice, w.carol, w.bob, w.carol)
        var c = await w.bob.collections[w.house]!
        var epoch = c.epoch(of: c.head!)!
        for d in [w.alice, w.bob, w.carol] {
            let marked = await d.inventory(epoch)?.marks(shelf) == true
            XCTAssertTrue(marked, "the new inventory marks the deleted record")
            let item = await d.item(list: w.house, id: shelf)
            XCTAssertEqual(item?.deleted, true, "a deletion mark must be observable as a deleted item")
            let n = try await writes(d, w.house, shelf)
            XCTAssertEqual(n, 0, "writes of a marked record kept")
        }
        let n = try await messagesOf(w.hub, w.bob, w.house, shelf)
        XCTAssertEqual(n, 0, "no tombstone carried, no late edit signed again")
        do { try await w.alice.setField(list: w.house, record: shelf, field: "title", value: Bytes("Again".utf8)); XCTFail("refused") } catch {}
        // A write to it that another device signed anyway, as an older client would, isn't valid.
        let me = try some(await w.bob.me)
        let forged = try await w.bob.signRecord(c, me: me, record: shelf, field: Bytes("title".utf8), op: .assign,
                                                value: Bytes("Forged".utf8), element: nil, head: c.head!, epoch: epoch, after: [])
        let validity = await w.alice.dataWriteValidity(forged, cid: w.house, epoch: epoch)
        XCTAssertFalse(validity.isValid, "a write to a marked record is invalid")
        try await w.bob.rekey(list: w.house)
        try await sync(w.alice, w.carol, w.bob)
        c = await w.bob.collections[w.house]!
        epoch = c.epoch(of: c.head!)!
        let carried = await w.bob.inventory(epoch)?.marks(shelf) == true
        XCTAssertTrue(carried, "the mark is carried forward")
        let laptop = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.aliceKeys, store: try Store(path: ":memory:"), hub: await w.hub.connect())
        let updates = await laptop.events()
        try await sync(laptop, laptop)
        _ = try await LiveTests.next(updates) {
            if case let .items(list, records) = $0 { return list == w.house && records.contains(shelf) }
            return false
        }
        let fresh = await titles(laptop, w.house)
        XCTAssertEqual(fresh, ["Milk", "Bread"], "a new device")
        let marked = await laptop.item(list: w.house, id: shelf)
        XCTAssertEqual(marked?.deleted, true, "a fresh device must distinguish a deletion mark from an unknown record")
    }

    func testDeletionMarkReachesOfflineOwnerDevice() async throws {
        let w = try await world()
        let row = w.bob.newRecordID()
        try await w.bob.setField(list: w.house, record: row, field: "title", value: Bytes("Deleted while away".utf8))
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("tandem-mark-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try Store(path: directory.appendingPathComponent("offline.sqlite").path)
        let phone = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.bobKeys,
                                    store: store, hub: await w.hub.connect())
        try await sync(phone, phone)
        let live = await phone.item(list: w.house, id: row)
        XCTAssertEqual(live?.deleted, false)
        try await w.bob.deleteRecord(list: w.house, record: row)
        try await w.bob.remove(w.carolID, from: w.house)
        try await phone.sync()
        let marked = await phone.item(list: w.house, id: row)
        XCTAssertEqual(marked?.deleted, true, "offline device sees a marked deletion as unknown")
        XCTAssertEqual(marked?.fields, [])
        XCTAssertNil(marked?.createdBy)
        XCTAssertNil(marked?.lastModifiedBy)
        let unknown = await phone.item(list: w.house, id: phone.newRecordID())
        XCTAssertNil(unknown)
        let retained = try store.writes(scope: w.house, record: row)
        XCTAssertTrue(retained.isEmpty, "public deletion visibility must not retain old writes")
        let again = try Device.reopen(store: store, keys: w.bobKeys, hub: await w.hub.connect())
        let reopened = await again?.item(list: w.house, id: row)
        XCTAssertEqual(reopened?.deleted, true, "a durable reopen lost the public deletion mark")
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
