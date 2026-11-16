import Foundation
import Synchronization
import Tandem
@testable import TandemEngine
import XCTest
import SQLite3

/// DESIGN.md §17 in the engine: attaching, the pending write, uploads, downloads, clean-up and healing, on a memory hub and a
/// memory blob folder. The scenarios of the Go engine's blobs_test.go.
final class BlobTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")
    let small = BlobSettings(chunkSize: 1000, inline: 100)

    struct World { let hub: MemoryHub; let blobs: MemoryBlobHub; let f: Founding; let bob: Device; let alice: Device; let list: Bytes }

    struct Missing: Error {}
    func some<T>(_ v: T?) throws -> T { guard let v else { throw Missing() }; return v }

    func content(_ n: Int) -> Bytes { (0..<n).map { _ in UInt8.random(in: 0...255) } }

    func world(store: Store? = nil) async throws -> World {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try store ?? Store(path: ":memory:"), hub: await hub.connect())
        try await bob.publish(f)
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let alice = try await Device.join(link: link, keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.sync()
        let list = try await bob.createList(name: "Photos")
        let aliceID = try some(await alice.me?.participant)
        try await bob.share(list: list, with: aliceID, role: .writer, name: "Alice")
        try await alice.sync()
        try await alice.accept(list: list)
        try await bob.sync(); try await alice.sync()
        let blobs = MemoryBlobHub()
        try await bob.useBlobs(blobs, settings: small)
        try await alice.useBlobs(blobs, settings: small)
        await bob.settleBlobs(); await alice.settleBlobs()
        return World(hub: hub, blobs: blobs, f: f, bob: bob, alice: alice, list: list)
    }

    /// A sync and the clean-up pass after it.
    func clean(_ ds: Device...) async throws {
        for d in ds { try await d.sync(); await d.settleBlobs() }
    }

    func recheck(_ ds: Device...) async { for d in ds { await d.forgetHealing() } }

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

    func fetched(_ d: Device, _ list: Bytes, _ record: Bytes, _ name: String) async throws -> Bytes {
        Bytes(try Data(contentsOf: try await d.fetchBlob(list: list, record: record, name: name)))
    }

    func state(_ d: Device, _ list: Bytes, _ record: Bytes, _ name: String) async -> BlobState? {
        await d.blob(list: list, record: record, name: name)?.state
    }

    func testAttachUploadFetch() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID(), data = content(5500)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs()
        let bobState = await state(w.bob, w.list, photo, "photo")
        XCTAssertEqual(bobState, .here)
        let m = try await manifest(w.bob, w.list, photo, "photo")
        XCTAssertEqual(m.chunks?.count, 6)
        let up = await onHub(w.blobs, m)
        XCTAssertEqual(up, 6)
        try await w.alice.sync()
        let remote = await state(w.alice, w.list, photo, "photo")
        XCTAssertEqual(remote, .remote)
        let got = try await fetched(w.alice, w.list, photo, "photo")
        XCTAssertEqual(got, data)
        let here = await state(w.alice, w.list, photo, "photo")
        XCTAssertEqual(here, .here)
        // Small content is inline: here everywhere, no chunk.
        let note = w.bob.newRecordID(), before = await w.blobs.refs.count
        try await w.bob.attach(list: w.list, record: note, name: "note", content: Bytes("hello, Alice".utf8))
        try await w.alice.sync()
        let inline = try await fetched(w.alice, w.list, note, "note")
        XCTAssertEqual(inline, Bytes("hello, Alice".utf8))
        let after = await w.blobs.refs.count
        XCTAssertEqual(after, before)
    }

    func testMalformedManifestCannotPoisonSameHash() async throws {
        let w = try await world()
        let good = w.bob.newRecordID(), bad = w.bob.newRecordID(), data = content(5500)
        try await w.bob.attach(list: w.list, record: good, name: "photo", content: data)
        await w.bob.settleBlobs()
        var m = try await manifest(w.bob, w.list, good, "photo")
        m.chunks!.swapAt(0, 1)
        try await w.bob.setField(list: w.list, record: bad, field: "_blob:photo", value: Blob.encode(m))
        try await w.alice.sync()
        do {
            _ = try await fetched(w.alice, w.list, bad, "photo")
            XCTFail("malformed order was accepted")
        } catch BlobError.unreadable {}
        let got = try await fetched(w.alice, w.list, good, "photo")
        XCTAssertEqual(got, data, "a malformed manifest cannot poison valid content with the same hash")
    }

    func testInlineHashCannotPoisonContentCache() async throws {
        let w = try await world()
        let good = w.bob.newRecordID(), bad = w.bob.newRecordID(), data = content(2500)
        try await w.bob.attach(list: w.list, record: good, name: "photo", content: data)
        await w.bob.settleBlobs()
        let m = try await manifest(w.bob, w.list, good, "photo")
        let poison = Bytes("wrong bytes under an existing file's hash".utf8)
        let inline = Blob.Manifest(size: poison.count, hash: m.hash, data: poison)
        try await w.bob.setField(list: w.list, record: bad, field: "_blob:photo", value: Blob.encode(inline))
        try await w.alice.sync()
        do {
            _ = try await fetched(w.alice, w.list, bad, "photo")
            XCTFail("false inline hash was accepted")
        } catch BlobError.unreadable {}
        let got = try await fetched(w.alice, w.list, good, "photo")
        XCTAssertEqual(got, data)
    }

    func testRemovalDropsBlockedUpload() async throws {
        let w = try await world()
        await w.blobs.set(failAppends: true)
        let photo = w.alice.newRecordID()
        try await w.alice.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.alice.settleBlobs()
        let participant = try some(await w.alice.me?.participant)
        try await w.bob.remove(participant, from: w.list)
        try await w.alice.sync(); await w.alice.settleBlobs()
        let pending = try await w.alice.store.uploads()
        XCTAssertTrue(pending.isEmpty, "an observed removal left its blocked upload resumable")
    }

    func testInventoryMarkDropsBlockedUpload() async throws {
        let w = try await world()
        await w.blobs.set(failAppends: true)
        let photo = w.alice.newRecordID()
        try await w.alice.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.alice.settleBlobs()
        try await w.bob.deleteRecord(list: w.list, record: photo)
        try await w.bob.rekey(list: w.list)
        try await w.alice.sync(); await w.alice.settleBlobs()
        let marked = await w.alice.item(list: w.list, id: photo)
        XCTAssertEqual(marked?.deleted, true)
        let pending = try await w.alice.store.uploads()
        XCTAssertTrue(pending.isEmpty, "a deletion compacted into an inventory mark left its upload resumable")
    }

    func testDowngradeDropsBlockedUpload() async throws {
        let w = try await world()
        await w.blobs.set(failAppends: true)
        let photo = w.alice.newRecordID()
        try await w.alice.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.alice.settleBlobs()
        let participant = try some(await w.alice.me?.participant)
        try await w.bob.setRole(.reader, for: participant, in: w.list)
        try await w.alice.sync(); await w.alice.settleBlobs()
        let pending = try await w.alice.store.uploads()
        XCTAssertTrue(pending.isEmpty)
        try await w.bob.setRole(.writer, for: participant, in: w.list)
        await w.blobs.set(failAppends: false)
        try await clean(w.alice, w.bob)
        let revived = await w.bob.blob(list: w.list, record: photo, name: "photo")
        XCTAssertNil(revived, "promotion revived the discarded upload")
    }

    func testFetchLargerThanCacheReturnsExistingFile() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID(), data = content(2500)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs(); try await w.alice.sync()
        await w.alice.setBlobSettings(BlobSettings(storeLimit: 1))
        let got = try await fetched(w.alice, w.list, photo, "photo")
        XCTAssertEqual(got, data)
    }

    /// Review 1: a pending replacement leaves the published version whole.
    func testPendingReplacementKeepsThePublishedVersion() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID(), v2 = content(3200)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(3000))
        await w.bob.settleBlobs()
        let m1 = try await manifest(w.bob, w.list, photo, "photo")
        await w.blobs.set(failAppends: true)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: v2)
        await w.bob.settleBlobs()
        let pending = await state(w.bob, w.list, photo, "photo")
        XCTAssertEqual(pending, .uploading)
        let still = try await manifest(w.bob, w.list, photo, "photo")
        XCTAssertEqual(still.hash, m1.hash, "the replica still holds the published version")
        try await w.bob.setField(list: w.list, record: photo, field: "title", value: Bytes("Beach".utf8))
        try await clean(w.bob)
        let kept = await onHub(w.blobs, m1)
        XCTAssertEqual(kept, m1.chunks!.count)
        try await w.alice.sync()
        let seen = try await manifest(w.alice, w.list, photo, "photo")
        XCTAssertEqual(seen.hash, m1.hash, "a record message published meanwhile carries the old manifest")
        await w.blobs.set(failAppends: false)
        await w.bob.settleBlobs()
        try await clean(w.bob)
        let left = await onHub(w.blobs, m1)
        XCTAssertEqual(left, 0, "once the replacement is on the feed the old chunks go")
        try await w.alice.sync()
        let got = try await fetched(w.alice, w.list, photo, "photo")
        XCTAssertEqual(got, v2)
    }

    /// The third review: a committed replacement whose publish failed keeps the old version protected.
    func testUnpublishedReplacementStaysProtected() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let feed = FailingHub(inner: await hub.connect())
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"), hub: feed)
        try await bob.publish(f)
        let list = try await bob.createList(name: "Photos")
        let blobs = MemoryBlobHub()
        try await bob.useBlobs(blobs, settings: small)
        let photo = bob.newRecordID()
        try await bob.attach(list: list, record: photo, name: "photo", content: content(2500))
        await bob.settleBlobs()
        let m1 = try await manifest(bob, list, photo, "photo")
        feed.down.withLock { $0 = true }
        let newer = content(2600)
        try await bob.attach(list: list, record: photo, name: "photo", content: newer)
        await bob.settleBlobs()
        let m2 = try await manifest(bob, list, photo, "photo")
        XCTAssertNotEqual(m1.hash, m2.hash, "the replacement committed")
        try await bob.afterSync()
        await bob.settleBlobs()
        let kept = await onHub(blobs, m1)
        XCTAssertEqual(kept, m1.chunks!.count, "collected before the replacement was on the feed")
        let local = await bob.store
        XCTAssertEqual(try local.values(prefix: "blobs.staged.").count, 1)
        let hash = m2.hash.map { String(format: "%02x", $0) }.joined()
        try FileManager.default.removeItem(at: local.blobDirectory.appendingPathComponent(hash))
        for e in m2.chunks ?? [] {
            let refs = await blobs.withAddress(e.address)
            try await blobs.destroy(ids: refs)
        }
        await bob.checkBlobs(); await bob.settleBlobs()
        let restored = await onHub(blobs, m2)
        XCTAssertEqual(restored, m2.chunks!.count, "staged source must repair an evicted file")
        feed.down.withLock { $0 = false }
        try await clean(bob)
        try await clean(bob)
        let left = await onHub(blobs, m1)
        XCTAssertEqual(left, 0)
        XCTAssertTrue(try local.values(prefix: "blobs.staged.").isEmpty)
        let got = try await fetched(bob, list, photo, "photo")
        XCTAssertEqual(got, newer)
    }

    /// Review 2: junk claiming a chunk's address neither lets an upload skip it nor stops healing.
    func testJunkClaimsAreSettled() async throws {
        let w = try await world()
        let tag = try some(await w.bob.channel(of: w.list))
        let photo = w.bob.newRecordID(), data = content(2500)
        await w.blobs.set(failAppends: true)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs()
        let ups = try await w.bob.store.uploads()
        let pending = try some(await w.bob.uploadOf(ups[0])).1
        let junk = await w.blobs.inject(channel: tag, address: pending.chunks![0].address, body: Bytes("junk".utf8))
        await w.blobs.set(failAppends: false)
        await w.bob.settleBlobs()
        let m = try await manifest(w.bob, w.list, photo, "photo")
        let both = await w.blobs.withAddress(m.chunks![0].address)
        XCTAssertEqual(both.count, 2, "the real chunk went up beside the junk")
        await w.blobs.drop(await w.blobs.withAddress(m.chunks![1].address))
        await w.blobs.inject(channel: tag, address: m.chunks![1].address, body: Bytes("more junk".utf8))
        await recheck(w.bob)
        try await clean(w.bob)
        var real = 0
        for id in await w.blobs.withAddress(m.chunks![1].address) {
            if let c = try await w.blobs.fetch(ids: [id]).first, Blob.address(c.envelope) == m.chunks![1].address { real += 1 }
        }
        XCTAssertEqual(real, 1)
        let stillJunk = await w.blobs.withAddress(m.chunks![0].address)
        XCTAssertTrue(stillJunk.contains(junk), "a rejected message is never destroyed on that account")
        try await w.alice.sync()
        let got = try await fetched(w.alice, w.list, photo, "photo")
        XCTAssertEqual(got, data)
    }

    /// Review 3: two devices healing the same chunk end with one copy.
    func testTwoHealersKeepOneCopy() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        try await w.alice.sync()
        _ = try await fetched(w.alice, w.list, photo, "photo")
        let m = try await manifest(w.bob, w.list, photo, "photo")
        await w.blobs.drop(await w.blobs.withAddress(m.chunks![1].address))
        await recheck(w.bob, w.alice)
        try await clean(w.bob, w.alice)
        try await clean(w.bob, w.alice)
        let ids = await w.blobs.withAddress(m.chunks![1].address)
        XCTAssertEqual(ids.count, 1)
    }

    /// The form check moves a hub that damages the escaped form to base64, the whole batch again.
    func testFormCheckFallsBackToBase64() async throws {
        let w = try await world()
        await w.blobs.set(damageEscaped: true)
        let photo = w.bob.newRecordID(), data = content(2500)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs()
        let form = try await w.bob.blobForm()
        XCTAssertEqual(form, .base64)
        try await w.alice.sync()
        let got = try await fetched(w.alice, w.list, photo, "photo")
        XCTAssertEqual(got, data)
    }

    /// Orphans go only after this device has known them for the grace, by its own clock.
    func testOrphansWaitForTheGrace() async throws {
        let w = try await world()
        let tag = try some(await w.bob.channel(of: w.list))
        let (_, envs) = try Blob.describe(r: Bytes(repeating: 7, count: 32), hub: w.f.pin.hub, content: content(1500), chunkSize: 1000, inline: 0)
        _ = try await w.blobs.append([(tag, envs[0])], form: .escaped)
        try await w.bob.attach(list: w.list, record: w.bob.newRecordID(), name: "photo", content: content(1500))
        await w.bob.settleBlobs()
        try await clean(w.bob)
        let early = await w.blobs.withAddress(Blob.address(envs[0]))
        XCTAssertEqual(early.count, 1, "an orphan went before the grace")
        await w.bob.setBlobSettings(BlobSettings(chunkSize: 1000, inline: 100, orphanGrace: .milliseconds(1)))
        try await Task.sleep(for: .milliseconds(5))
        try await clean(w.bob)
        let late = await w.blobs.withAddress(Blob.address(envs[0]))
        XCTAssertEqual(late.count, 0)
    }

    /// The second review: lengths that don't match their chunks make the blob unreadable, and nothing of it is shown.
    func testLengthMismatchIsUnreadable() async throws {
        let w = try await world()
        let tag = try some(await w.bob.channel(of: w.list))
        var (m, envs) = try Blob.describe(r: Bytes(repeating: 3, count: 32), hub: w.f.pin.hub, content: content(1800), chunkSize: 1000, inline: 0)
        _ = try await w.blobs.append([(tag, envs[0]), (tag, envs[1])], form: .escaped)
        let a = m.chunks![0].length
        m.chunks![0].length = m.chunks![1].length; m.chunks![1].length = a
        let photo = w.bob.newRecordID()
        try await w.bob.setField(list: w.list, record: photo, field: "_blob:photo", value: try Blob.encode(m))
        try await w.alice.sync()
        do { _ = try await w.alice.fetchBlob(list: w.list, record: photo, name: "photo"); XCTFail("read") } catch BlobError.unreadable {}
        let s = await w.alice.blob(list: w.list, record: photo, name: "photo")
        XCTAssertEqual(s?.state, .unreadable)
        XCTAssertNil(s?.path)
    }

    /// The second review: a manifest of an unknown version stops every kind of clean-up in its list.
    func testUnknownVersionStopsCleanup() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        let m1 = try await manifest(w.bob, w.list, photo, "photo")
        try await w.bob.setField(list: w.list, record: w.bob.newRecordID(), field: "_blob:future", value: Bytes(#"{"blob":2,"chunks":"x"}"#.utf8))
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: Bytes("x".utf8))
        try await clean(w.bob)
        let kept = await onHub(w.blobs, m1)
        XCTAssertEqual(kept, m1.chunks!.count)
    }

    /// A crash after the write is signed and journalled resumes and commits exactly one blob.
    func testUploadResumesAfterARestart() async throws {
        let path = FileManager.default.temporaryDirectory.appendingPathComponent("tandem-\(UUID().uuidString).sqlite").path
        defer { try? FileManager.default.removeItem(atPath: path); try? FileManager.default.removeItem(atPath: path + ".blobs") }
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let blobs = MemoryBlobHub()
        await blobs.set(failAppends: true)
        let photo: Bytes, list: Bytes, data = content(4200)
        do {
            let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: path), hub: await hub.connect())
            try await bob.publish(f)
            list = try await bob.createList(name: "Photos")
            try await bob.useBlobs(blobs, settings: small)
            photo = bob.newRecordID()
            try await bob.attach(list: list, record: photo, name: "photo", content: data)
            await bob.settleBlobs()
        }
        await blobs.set(failAppends: false)
        let again = try some(try Device.reopen(store: try Store(path: path), keys: keys, hub: await hub.connect()))
        try await again.useBlobs(blobs, settings: small)
        await again.settleBlobs()
        let m = try await manifest(again, list, photo, "photo")
        let up = await onHub(blobs, m)
        XCTAssertEqual(up, m.chunks!.count)
        let left = try await again.store.uploads()
        XCTAssertTrue(left.isEmpty)
        let got = try await fetched(again, list, photo, "photo")
        XCTAssertEqual(got, data)
    }

    /// An append whose answer was lost landed anyway: nothing goes up twice.
    func testLostAnswerAppendsNothingTwice() async throws {
        let w = try await world()
        await w.blobs.set(loseAnswers: 1)
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        await w.bob.settleBlobs()
        let m = try await manifest(w.bob, w.list, photo, "photo")
        for e in m.chunks! {
            let n = await w.blobs.withAddress(e.address).count
            XCTAssertEqual(n, 1)
        }
    }

    /// While a blob is pending, other writes go out; a field changed meanwhile ends as two siblings.
    func testOtherWritesGoOutWhilePending() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(500))
        try await w.alice.sync()
        await w.blobs.set(failAppends: true)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        try await w.bob.setField(list: w.list, record: photo, field: "title", value: Bytes("Beach".utf8))
        try await w.alice.sync()
        let title = await w.alice.value(list: w.list, record: photo, field: "title")
        XCTAssertEqual(title, Bytes("Beach".utf8))
        try await w.alice.attach(list: w.list, record: photo, name: "photo", content: content(30))
        try await w.bob.sync()
        await w.blobs.set(failAppends: false)
        await w.bob.settleBlobs()
        try await w.alice.sync()
        let item = try some(await w.alice.item(list: w.list, id: photo))
        XCTAssertEqual(item.values("_blob:photo").count, 2)
    }

    /// Cancelling, or deleting the record, drops the pending write; its chunks already up are collected.
    func testCancelAndDeleteDropThePendingWrite() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID()
        await w.blobs.set(partialAppends: .some(1))
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        let ups = try await w.bob.store.uploads()
        let pending = try some(await w.bob.uploadOf(ups[0])).1
        let some = await onHub(w.blobs, pending)
        XCTAssertGreaterThan(some, 0)
        try await w.bob.cancelAttach(list: w.list, record: photo, name: "photo")
        await w.blobs.set(partialAppends: .some(nil))
        let gone = await w.bob.blob(list: w.list, record: photo, name: "photo")
        XCTAssertNil(gone)
        try await clean(w.bob)
        let left = await onHub(w.blobs, pending)
        XCTAssertEqual(left, 0)
        await w.blobs.set(failAppends: true)
        let other = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: other, name: "photo", content: content(2500))
        try await w.bob.deleteRecord(list: w.list, record: other)
        await w.blobs.set(failAppends: false)
        await w.bob.settleBlobs()
        let none = try await w.bob.store.uploads()
        XCTAssertTrue(none.isEmpty)
    }

    /// A key change carries committed blobs into the new epoch without a chunk going up.
    func testKeyChangeUploadsNothing() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID(), data = content(2500)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs()
        let before = await w.blobs.appends
        try await w.bob.remove(try some(await w.alice.me?.participant), from: w.list)
        try await clean(w.bob)
        let after = await w.blobs.appends
        XCTAssertEqual(after, before)
        let got = try await fetched(w.bob, w.list, photo, "photo")
        XCTAssertEqual(got, data)
    }

    /// Clean-up: chunks of a replaced or deleted file go; another list's, and the live version's, stay.
    func testCleanupCollectsWhatNothingProtects() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID(), doc = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        try await w.bob.attach(list: w.list, record: doc, name: "file", content: content(1500))
        await w.bob.settleBlobs()
        let m1 = try await manifest(w.bob, w.list, photo, "photo"), md = try await manifest(w.bob, w.list, doc, "file")
        let other = try await w.bob.createList(name: "Other"), o = w.bob.newRecordID()
        try await w.bob.attach(list: other, record: o, name: "photo", content: content(1500))
        await w.bob.settleBlobs()
        let mo = try await manifest(w.bob, other, o, "photo")
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2600))
        try await w.bob.deleteRecord(list: w.list, record: doc)
        await w.bob.settleBlobs()
        try await clean(w.bob)
        let a = await onHub(w.blobs, m1), b = await onHub(w.blobs, md), c = await onHub(w.blobs, mo)
        XCTAssertEqual(a, 0); XCTAssertEqual(b, 0); XCTAssertEqual(c, mo.chunks!.count)
        let m2 = try await manifest(w.bob, w.list, photo, "photo")
        let live = await onHub(w.blobs, m2)
        XCTAssertEqual(live, m2.chunks!.count)
    }

    /// Healing: a destroyed chunk comes back from a device that holds the file; one that doesn't reports it unavailable.
    func testHealingRestoresAChunk() async throws {
        let w = try await world()
        let photo = w.bob.newRecordID(), data = content(2500)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs()
        let m = try await manifest(w.bob, w.list, photo, "photo")
        await w.blobs.drop(await w.blobs.withAddress(m.chunks![2].address))
        try await w.alice.sync()
        do { _ = try await w.alice.fetchBlob(list: w.list, record: photo, name: "photo"); XCTFail("read") } catch BlobError.unavailable {}
        let s = await state(w.alice, w.list, photo, "photo")
        XCTAssertEqual(s, .unavailable)
        await recheck(w.bob)
        try await clean(w.bob)
        let got = try await fetched(w.alice, w.list, photo, "photo")
        XCTAssertEqual(got, data)
    }

    /// Manifests of any shape: mixed lengths, a repeated address, one address in two manifests.
    func testManifestsOfAnyShapeRead() async throws {
        let w = try await world()
        let tag = try some(await w.bob.channel(of: w.list)), hub = w.f.pin.hub, r = Bytes(repeating: 9, count: 32)
        let a = content(700), b = content(300)
        let ka = try Blob.chunkKey(r: r, plaintext: a), kb = try Blob.chunkKey(r: r, plaintext: b)
        let ea = try Blob.seal(key: ka, hub: hub, plaintext: a), eb = try Blob.seal(key: kb, hub: hub, plaintext: b)
        _ = try await w.blobs.append([(tag, ea), (tag, eb)], form: .escaped)
        let xa = Blob.ChunkEntry(address: Blob.address(ea), key: ka, length: 700), xb = Blob.ChunkEntry(address: Blob.address(eb), key: kb, length: 300)
        let whole = a + b + a
        let x = w.bob.newRecordID(), y = w.bob.newRecordID()
        try await w.bob.setField(list: w.list, record: x, field: "_blob:f", value: try Blob.encode(Blob.Manifest(size: whole.count, hash: H(whole), chunks: [xa, xb, xa])))
        try await w.bob.setField(list: w.list, record: y, field: "_blob:f", value: try Blob.encode(Blob.Manifest(size: 300, hash: H(b), chunks: [xb])))
        try await w.alice.sync()
        let gx = try await fetched(w.alice, w.list, x, "f"), gy = try await fetched(w.alice, w.list, y, "f")
        XCTAssertEqual(gx, whole); XCTAssertEqual(gy, b)
    }

    func testQuotaAndAFullHub() async throws {
        let w = try await world()
        let unknown = try await w.bob.blobQuota()
        XCTAssertEqual(unknown, .unknown)
        await w.blobs.set(quota: .known(used: 10, limit: 100))
        let known = try await w.bob.blobQuota()
        XCTAssertEqual(known, .known(used: 10, limit: 100))
        await w.blobs.set(failAppends: true)
        let photo = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: content(2500))
        await w.bob.settleBlobs()
        let waiting = try await w.bob.store.uploads()
        XCTAssertEqual(waiting.count, 1)
        await w.blobs.set(failAppends: false)
        await w.bob.settleBlobs()
        let s = await state(w.bob, w.list, photo, "photo")
        XCTAssertEqual(s, .here)
    }
    func testPendingRotationPreservesConcurrentAttachment() async throws {
        let w = try await world()
        let record = w.bob.newRecordID()
        try await w.bob.attach(list: w.list, record: record, name: "file", content: content(3000))
        await w.bob.settleBlobs(); try await w.alice.sync()
        await w.blobs.set(failAppends: true)
        try await w.bob.attach(list: w.list, record: record, name: "file", content: content(3200))
        await w.bob.settleBlobs()
        try await w.alice.attach(list: w.list, record: record, name: "file", content: Bytes("peer version".utf8))
        try await w.bob.sync(); try await w.bob.rekey(list: w.list); try await w.alice.sync()
        await w.blobs.set(failAppends: false); await w.bob.settleBlobs()
        try await w.alice.sync(); try await w.bob.sync()
        let item = await w.bob.item(list: w.list, id: record)
        XCTAssertEqual(item?.values("_blob:file").count, 2, "rotation must preserve the concurrent attachment")
    }

    func testCommitFailureKeepsJournalAndStagedContent() async throws {
        for (table, action, row) in [("writes", "INSERT", "NEW"), ("uploads", "DELETE", "OLD")] {
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: directory) }
            let path = directory.appendingPathComponent("device.sqlite").path, store = try Store(path: path)
            let w = try await world(store: store), record = w.bob.newRecordID(), newer = content(3200)
            try await w.bob.attach(list: w.list, record: record, name: "file", content: content(3000))
            await w.bob.settleBlobs()
            let previous = await w.bob.value(list: w.list, record: record, field: "_blob:file")
            await w.blobs.set(failAppends: true)
            try await w.bob.attach(list: w.list, record: record, name: "file", content: newer)
            await w.bob.settleBlobs()
            var db: OpaquePointer?
            XCTAssertEqual(sqlite3_open(path, &db), SQLITE_OK)
            defer { sqlite3_close(db) }
            let hex = record.map { String(format: "%02x", $0) }.joined()
            let sql = "CREATE TRIGGER fail_file_commit BEFORE \(action) ON \(table) WHEN \(row).record = X'\(hex)' BEGIN SELECT RAISE(ABORT, 'injected durable commit failure'); END"
            XCTAssertEqual(sqlite3_exec(db, sql, nil, nil, nil), SQLITE_OK)
            await w.blobs.set(failAppends: false); await w.bob.settleBlobs()
            let uploads = try store.uploads()
            XCTAssertEqual(uploads.count, 1, "a failed commit must retain the journal")
            let upload = try some(uploads.first)
            XCTAssertTrue(FileManager.default.fileExists(atPath: store.blobDirectory.appendingPathComponent("staged/" + upload.staged).path))
            let after = await w.bob.value(list: w.list, record: record, field: "_blob:file")
            XCTAssertEqual(after, previous, "failed persistence must not change the replica")
            XCTAssertEqual(sqlite3_exec(db, "DROP TRIGGER fail_file_commit", nil, nil, nil), SQLITE_OK)
            await w.bob.settleBlobs(); try await w.alice.sync()
            let got = try await fetched(w.alice, w.list, record, "file")
            XCTAssertEqual(got, newer)
        }
    }

    func testCopyRefusesAttachmentsBeforeCreatingAList() async throws {
        for data in [Bytes("inline file".utf8), content(3000)] {
            let w = try await world()
            let record = w.bob.newRecordID()
            try await w.bob.attach(list: w.list, record: record, name: "file", content: data)
            await w.bob.settleBlobs()
            let before = await w.bob.lists().count
            do { _ = try await w.bob.copy(list: w.list); XCTFail("copy silently omitted attachments") }
            catch { XCTAssertTrue("\(error)".contains("file attachments")) }
            let after = await w.bob.lists().count
            XCTAssertEqual(after, before, "refused copy must not create a partial list")
        }
    }

}

/// A feed connection that can go away.
final class FailingHub: PushStore, @unchecked Sendable {
    let inner: any PushStore
    let down = Mutex(false)
    init(inner: any PushStore) { self.inner = inner }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> { try await inner.watch() }
    func changes(since cursor: String) async throws -> ChangeBatch { try await inner.changes(since: cursor) }
    func append(channel: String, body: Bytes) async throws -> String {
        if down.withLock({ $0 }) { throw TandemError.invalid("hub: unreachable") }
        return try await inner.append(channel: channel, body: body)
    }
    func fetch(ids: [String]) async throws -> [Message] { try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func close() async { await inner.close() }
}

/// Found by interop on Stalwart: the worker let the hub's connections go at the end of its round while a download was
/// still using them. A download in flight keeps them.
final class BlobReleaseTests: XCTestCase {
    func testDownloadSurvivesTheWorkersRelease() async throws {
        let t = BlobTests()
        let w = try await t.world()
        let photo = w.bob.newRecordID(), data = t.content(2500)
        try await w.bob.attach(list: w.list, record: photo, name: "photo", content: data)
        await w.bob.settleBlobs()
        try await w.alice.sync()
        let gated = GatedBlobs(inner: w.blobs)
        await w.alice.replaceBlobHub(gated)
        let fetch = Task { try await w.alice.fetchBlob(list: w.list, record: photo, name: "photo") }
        await gated.entered()
        gated.armed.withLock { $0 = true }
        try await w.alice.afterSync()
        await w.alice.settleBlobs()
        gated.open()
        let url = try await fetch.value
        XCTAssertEqual(Bytes(try Data(contentsOf: url)), data)
    }
}

final class GatedBlobs: BlobHub, @unchecked Sendable {
    let inner: MemoryBlobHub
    let armed = Mutex(false), state = Mutex((inFlight: 0, broken: false, gated: false))
    let enteredStream: AsyncStream<Void>, enteredSignal: AsyncStream<Void>.Continuation
    let gateStream: AsyncStream<Void>, gateSignal: AsyncStream<Void>.Continuation
    init(inner: MemoryBlobHub) {
        self.inner = inner
        (enteredStream, enteredSignal) = AsyncStream<Void>.makeStream()
        (gateStream, gateSignal) = AsyncStream<Void>.makeStream()
    }
    func entered() async { for await _ in enteredStream { return } }
    func open() { gateSignal.finish() }
    func changes(since cursor: String) async throws -> BlobChanges { try await inner.changes(since: cursor) }
    func append(_ chunks: [(channel: String, envelope: Bytes)], form: ChunkForm) async throws -> [String] { try await inner.append(chunks, form: form) }
    func fetch(ids: [String]) async throws -> [Chunk] {
        let first = state.withLock { s -> Bool in s.inFlight += 1; defer { s.gated = true }; return !s.gated }
        if first { enteredSignal.yield(); for await _ in gateStream {} }
        defer { state.withLock { $0.inFlight -= 1 } }
        if state.withLock({ $0.broken }) { throw TandemError.invalid("closed") }
        return try await inner.fetch(ids: ids)
    }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func quota() async throws -> Quota { try await inner.quota() }
    func release() async {
        if armed.withLock({ $0 }) { state.withLock { if $0.inFlight > 0 { $0.broken = true } } }   // closing a connection under a command
        await inner.release()
    }

}
