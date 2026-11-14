import XCTest
import Foundation
@testable import Tandem
@testable import TandemEngine

/// Regression tests for the review of the Go engine (2026-11-13), whose findings also held for this engine.
final class SecondReviewTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, f: Founding, bob: Device, phone: Device, tablet: Device, aliceID: ID, tabletCert: ID, list: Bytes
    }

    /// Bob owns a list shared with Alice, who has a phone and a paired tablet; Bob then revokes the tablet.
    func world(revoke: Bool = true) async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let phone = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s phone", store: try Store(path: ":memory:"), hub: await hub.connect())
        let tablet = try await Device.pair(link: try await phone.startPairing(label: "Tablet", account: account), keys: DeviceKeys(),
                                           label: "Alice’s tablet", store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.sync()
        let claims = await bob.pendingClaims()
        try await bob.approve(claim: claims[0].id, ms: f.managementSeed)
        try await tablet.sync()
        try await phone.sync()
        let aliceID = await phone.me!.participant, tabletCert = await tablet.me!.certificate
        let list = try await bob.createList(name: "House")
        try await bob.share(list: list, with: aliceID, role: .writer)
        try await phone.sync()
        try await phone.accept(list: list)
        try await tablet.sync()
        if revoke { try await bob.revoke(certificate: tabletCert, ms: f.managementSeed) }
        return World(hub: hub, f: f, bob: bob, phone: phone, tablet: tablet, aliceID: aliceID, tabletCert: tabletCert, list: list)
    }

    func epochKey(_ d: Device, _ list: Bytes) async -> (epoch: ID, key: Bytes)? {
        let c = await d.collections[list]!
        guard let head = c.head, let e = c.epoch(of: head), let k = c.keys[e] else { return nil }
        return (e, k)
    }

    /// 1. The revoked tablet writes a change-in-flight note naming the new epoch with a key of its own into Alice's
    /// home: her phone keeps the real key, and its writes don't go where the tablet reads.
    func testAKeyFromHomeDoesntDisplaceTheListKey() async throws {
        let w = try await world()
        try await w.bob.rekey(list: w.list)
        let real = await epochKey(w.bob, w.list)!
        // Without Bob's grant the planted key is the only one the phone sees, whatever order it reads them in.
        let inbox = Tandem.inboxTag(uid: w.aliceID, hub: w.f.pin.hub)
        await w.hub.drop(await w.hub.messages.filter { $0.channel == inbox }.map(\.id))
        try await w.tablet.sync()
        let fake = Bytes(repeating: 9, count: 32)
        let note = FlightNote(parent: real.epoch, statement: real.epoch, key: fake, members: [], what: "x", device: "x")
        try await w.tablet.setHome("membership:" + B64u.encode(w.list), field: "flight", value: note.json())
        try await w.tablet.addHome("membership:" + B64u.encode(w.list), field: "keys", element: Device.keyElement(epoch: real.epoch, key: fake))
        try await w.phone.sync()
        try await w.phone.sync()
        let held = await w.phone.collections[w.list]!.keys[real.epoch]
        XCTAssertNotEqual(held, fake, "a key from home counts only once the epoch's statement commits to it")
        try? await w.phone.setField(list: w.list, record: w.phone.newRecordID(), field: "title", value: Bytes("Private".utf8))
        let leaked = await w.hub.messages.filter { $0.channel == Tandem.collectionTag(key: fake, hub: w.f.pin.hub, epoch: real.epoch) }
        XCTAssertTrue(leaked.isEmpty)
    }

    /// 2. After Alice's phone rekeyed her home without the tablet, the tablet starts a branch of its own (a start and a
    /// child of it) from the old head and sends the phone the key: neither start enters her home.
    func testARevokedKeychainCantBranchTheHome() async throws {
        let w = try await world()
        try await w.phone.sync()   // rekeys home without the tablet
        let tablet = await w.tablet.me!
        let heads = await w.tablet.homeHeads
        let oldHead = try XCTUnwrap(heads.first)
        let oldKey = await w.tablet.homeKeys[oldHead]!
        let k = Bytes(repeating: 4, count: 32)
        let et = try Founder.homeEpoch(pin: w.f.pin, participant: w.aliceID, n: 9, parents: [oldHead], homeKey: k,
                                       recipients: [tablet.certificate], signer: tablet.certificate, auth: tablet.auth, keys: tablet.keys)
        let et2 = try Founder.homeEpoch(pin: w.f.pin, participant: w.aliceID, n: 10, parents: [try et.id()], homeKey: k,
                                        recipients: [tablet.certificate], signer: tablet.certificate, auth: tablet.auth, keys: tablet.keys)
        let raw = await w.hub.connect()
        let oldTag = Tandem.homeTag(key: oldKey, hub: w.f.pin.hub, uid: w.aliceID, epoch: oldHead)
        for start in [et, et2] {
            _ = try await raw.append(channel: oldTag, body: try Envelope.seal(key: oldKey, hub: w.f.pin.hub, kind: 3, tag: oldTag, plaintext: try start.json()))
        }
        let phoneCert = await w.phone.registry.certificate(w.phone.me!.certificate)!
        for e in [et, et2] {
            _ = try await raw.append(channel: Tandem.inboxTag(uid: w.aliceID, hub: w.f.pin.hub),
                                     body: try Envelope.sealInbox(hub: w.f.pin.hub, uid: w.aliceID, agreement: phoneCert.agree,
                                                                  plaintext: Founder.homeKeyNotice(epoch: try e.id(), key: k).json()))
        }
        try await w.phone.sync()
        let epochs = await w.phone.homeEpochs
        XCTAssertNil(epochs[try et.id()])
        XCTAssertNil(epochs[try et2.id()])
    }

    /// 3. The admin's revocation can't reach the hub; after a restart the device sends it, and the others see it.
    func testAnInterruptedRevocationIsSentLater() async throws {
        let w = try await world(revoke: false)
        let store = try Store(path: ":memory:")
        let admin = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: nil, store: store,
                                    hub: FailingAppends(await w.hub.connect()))
        try await admin.sync()
        do { try await admin.revoke(certificate: w.tabletCert, ms: w.f.managementSeed); XCTFail("offline") } catch {}
        let again = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: nil, store: store, hub: await w.hub.connect())
        try await again.sync()
        try await w.phone.sync()
        let seen = await w.phone.registry.activity(w.tabletCert)
        XCTAssertEqual(seen, .invalid("revoked"))
    }

    // MARK: Liveness and crash safety

    func hubWithBobAndCarol() async throws -> (MemoryHub, Founding, DeviceKeys, Store, Device, Device, ID, Bytes) {
        let hub = MemoryHub(), keys = DeviceKeys(), store = try Store(path: ":memory:")
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        try await bob.publish(f)
        let carol = try await Device.join(link: try await bob.invite(label: "Carol", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Carol", store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.sync()
        let carolID = await carol.me!.participant
        let list = try await bob.createList(name: "House")
        try await bob.share(list: list, with: carolID, role: .writer)
        try await carol.sync()
        try await carol.accept(list: list)
        return (hub, f, keys, store, bob, carol, carolID, list)
    }

    /// 5. The note's append lands but its answer is lost; Bob's Mac discards the change; when the iPhone resumes, it
    /// must take the missing note as the discard it is, not write the note again and announce.
    func testADiscardedChangeStaysDiscardedAfterALostAnswer() async throws {
        let (hub, f, keys, store, _, carol, carolID, list) = try await hubWithBobAndCarol()
        let lossy = LostAnswers(await hub.connect())
        let phone = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: lossy)
        try await phone.sync()
        lossy.lose = true
        do { try await phone.remove(carolID, from: list); XCTFail("the answer was lost") } catch {}
        let mac = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await mac.sync()
        let pending = try await mac.pendingChange(list: list)
        try await mac.discard(list: list, seen: try XCTUnwrap(pending))
        let restarted = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        do { try await restarted.resume(); XCTFail("the user is told") } catch ChangeError.superseded {}
        try await carol.sync()
        let role = await carol.role(list: list)
        XCTAssertEqual(role, .writer, "the discarded change didn't happen")
    }

    /// 6. Two installations of Alice rekey her home at once, leaving two heads: syncing again and again settles, rather
    /// than each destroying and re-publishing the other's messages.
    func testTwoHomeHeadsSettle() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let a = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"), hub: await hub.connect())
        try await a.publish(f)
        let b = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"), hub: await hub.connect())
        try await b.sync()
        try await a.setHome("settings:x", field: "v", value: [1])
        try await b.sync()
        try await a.rotateHome()
        try await b.rotateHome()
        try await a.setHome("settings:a", field: "v", value: [1])
        try await b.setHome("settings:b", field: "v", value: [2])
        try await a.setHome("settings:x", field: "v", value: [3])   // a record both heads carry, changed after the fork
        for _ in 0..<3 { try await a.sync(); try await b.sync() }
        let heads = await a.homeHeads
        XCTAssertEqual(heads.count, 2)
        let before = Set(await hub.messages.map(\.id))
        for _ in 0..<3 { try await a.sync(); try await b.sync() }
        let after = Set(await hub.messages.map(\.id))
        XCTAssertEqual(after, before, "nothing destroyed or published any more")
        for d in [a, b] {
            let x = await d.homeValue("settings:x", field: "v"), va = await d.homeValue("settings:a", field: "v")
            XCTAssertEqual(x, [3]); XCTAssertEqual(va, [1])
        }
    }

    /// 7. A same-epoch change whose publish fails leaves nothing behind locally, so a later change doesn't fork the roster.
    func testAFailedSameEpochChangeLeavesNoTrace() async throws {
        let (hub, f, keys, store, _, carol, carolID, list) = try await hubWithBobAndCarol()
        let offline = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store,
                                      hub: FailingAppends(await hub.connect()))
        do { try await offline.setRole(.writer, for: carolID, in: list); try await offline.delete(list: list); XCTFail("offline") } catch {}
        let back = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        try await back.sync()
        let gone = await back.isDeleted(list: list)
        XCTAssertFalse(gone, "a delete that never left the device didn't happen")
        try await back.setRole(.reader, for: carolID, in: list)
        try await back.setRole(.writer, for: carolID, in: list)
        let dead = await back.isDead(list: list)
        XCTAssertFalse(dead)
        try await carol.sync()
        let deleted = await carol.isDeleted(list: list)
        XCTAssertFalse(deleted, "the delete never reached the hub")
    }

    /// 8. Joining fails half way; joining again with the same store finishes it, and the admin sees the member.
    func testAnInterruptedJoinFinishes() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.publish(f)
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let aliceKeys = DeviceKeys(), aliceStore = try Store(path: ":memory:")
        do {
            _ = try await Device.join(link: link, keys: aliceKeys, label: "Alice", store: aliceStore, hub: FailingAppends(await hub.connect()))
            XCTFail("offline")
        } catch {}
        let alice = try await Device.join(link: link, keys: aliceKeys, label: "Alice", store: aliceStore, hub: await hub.connect())
        try await alice.setHome("settings:x", field: "v", value: [1])
        try await bob.sync()
        let aliceMe = await alice.me
        let seen = await bob.registry.activity(try XCTUnwrap(aliceMe?.certificate))
        XCTAssertEqual(seen, .valid)
    }

    /// 8. A pairing claim that didn't go out is sent again by the waiting device's next sync.
    func testAPairingClaimThatDidntGoOutIsSentAgain() async throws {
        let w = try await world(revoke: false)
        let link = try await w.phone.startPairing(label: "Laptop", account: account)
        let laptopStore = try Store(path: ":memory:"), laptopKeys = DeviceKeys()
        do {
            _ = try await Device.pair(link: link, keys: laptopKeys, label: "Laptop", store: laptopStore, hub: FailingAppends(await w.hub.connect()))
            XCTFail("offline")
        } catch {}
        let conn = await w.hub.connect()
        let laptop = try XCTUnwrap(try Device.reopen(store: laptopStore, keys: laptopKeys, hub: conn))
        try await laptop.sync()
        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        XCTAssertEqual(claims.map(\.label), ["Laptop"])
    }

    /// 4. 64 parts with two versions each would overflow the bound on the search; they must be refused promptly.
    func testBogusFragmentVersionsCantOverflowTheSearch() async throws {
        let (_, _, _, _, bob, _, _, _) = try await hubWithBobAndCarol()
        let epoch = Bytes(repeating: 5, count: 32), commitment = Bytes(repeating: 6, count: 32)
        var complete = false
        for part in 0..<64 {
            for version in 0..<2 {
                complete = try await bob.receiveFragment(InventoryFragment(epoch: epoch, part: part, parts: 64, bytes: [UInt8(version)]),
                                                         epoch: epoch, commitment: commitment)
            }
        }
        XCTAssertFalse(complete)
    }

    /// 11. A part of the current inventory is deleted and a bogus version put in its place: the owner's device publishes
    /// the real one again, so a member who hadn't moved over yet still can.
    func testAReplacedInventoryPartIsRestored() async throws {
        let (hub, f, _, _, bob, carol, _, list) = try await hubWithBobAndCarol()
        try await bob.setField(list: list, record: bob.newRecordID(), field: "title", value: Bytes("Kept".utf8))
        try await bob.rekey(list: list)
        let c = await bob.collections[list]!
        let epoch = c.epoch(of: c.head!)!, key = c.keys[epoch]!
        let tag = Tandem.collectionTag(key: key, hub: f.pin.hub, epoch: epoch)
        var fragments: [String] = []
        for m in await hub.messages where m.channel == tag {
            let (_, p) = try Envelope.open(key: key, hub: f.pin.hub, tag: tag, envelope: m.body)
            if case .inventory = try Kind1.parse(p, hub: f.pin.hub, scope: list) { fragments.append(m.id) }
        }
        await hub.drop(fragments)
        let bogus = JSONWriter.object { w in
            w.member("inventory", json: JSONWriter.object { x in
                x.member("epoch", b64u: epoch); x.member("part", int: 0); x.member("parts", int: 1); x.member("bytes", b64u: [0, 0, 0, 0])
            })
        }
        let raw = await hub.connect()
        _ = try await raw.append(channel: tag, body: try Envelope.seal(key: key, hub: f.pin.hub, kind: 1, tag: tag, plaintext: bogus))
        try await bob.sync()
        try await carol.sync()
        let titles = await carol.items(list: list).compactMap { $0.text("title") }
        XCTAssertEqual(titles, ["Kept"], "Carol moves over through the restored inventory")
    }
}

/// A connection whose appends land but whose answers are lost, once `lose` is set (§10.3, "Lost answers").
final class LostAnswers: PushStore, @unchecked Sendable {
    let inner: MemoryHub.Connection
    var lose = false
    init(_ inner: MemoryHub.Connection) { self.inner = inner }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> { try await inner.watch() }
    func changes(since cursor: String) async throws -> ChangeBatch { try await inner.changes(since: cursor) }
    func append(channel: String, body: Bytes) async throws -> String {
        let id = try await inner.append(channel: channel, body: body)
        if lose { throw TandemError.invalid("the answer was lost") }
        return id
    }
    func fetch(ids: [String]) async throws -> [Message] { try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func close() async { await inner.close() }
}
