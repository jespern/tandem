import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// Regression tests for the independent review of the engine (2026-11-13), most severe first. Each one is the attack
/// or failure the review proved, and passes only with its fix.
final class ReviewFindingTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, f: Founding, bobKeys: DeviceKeys, bob: Device
        let alice: Device, carol: Device, aliceID: ID, carolID: ID, list: Bytes
    }

    func world(share: Bool = true) async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        var people: [Device] = []
        for name in ["Alice", "Carol"] {
            people.append(try await Device.join(link: try await bob.invite(label: name, account: account, ms: f.managementSeed),
                                                keys: DeviceKeys(), label: name, store: try Store(path: ":memory:"), hub: await hub.connect()))
        }
        try await bob.sync()
        let a = await people[0].me, c = await people[1].me
        let list = try await bob.createList(name: "House")
        if share {
            for p in people {
                let id = await p.me!.participant
                try await bob.share(list: list, with: id, role: .writer)
                try await p.sync()
                try await p.accept(list: list)
            }
        }
        return World(hub: hub, f: f, bobKeys: bobKeys, bob: bob, alice: people[0], carol: people[1],
                     aliceID: try XCTUnwrap(a?.participant), carolID: try XCTUnwrap(c?.participant), list: list)
    }

    func epochKey(_ d: Device, _ list: Bytes) async -> (epoch: ID, key: Bytes)? {
        let c = await d.collections[list]!
        guard let head = c.head, let e = c.epoch(of: head), let k = c.keys[e] else { return nil }
        return (e, k)
    }

    /// 1. Carol starts a home epoch for Alice and sends her its key: Alice's home doesn't take it, and her list keys
    /// never reach Carol's channel.
    func testNobodyElseCanStartSomeonesHomeEpoch() async throws {
        let w = try await world(share: false)
        let carolMe = await w.carol.me!, aliceMe = await w.alice.me!
        let raw = await w.hub.connect()
        for _ in 0..<8 {   // whatever its ID sorts as
            let k = Bytes((0..<32).map { _ in UInt8.random(in: 0...255) })
            let forged = try Founder.homeEpoch(pin: w.f.pin, participant: w.aliceID, n: 0, parents: [], homeKey: k,
                                               recipients: [aliceMe.certificate], signer: carolMe.certificate, auth: carolMe.auth,
                                               keys: carolMe.keys)
            let tag = Tandem.homeTag(key: k, hub: w.f.pin.hub, uid: w.aliceID, epoch: try forged.id())
            _ = try await raw.append(channel: tag, body: try Envelope.seal(key: k, hub: w.f.pin.hub, kind: 3, tag: tag, plaintext: try forged.json()))
            let notice = Founder.homeKeyNotice(epoch: try forged.id(), key: k).json()
            _ = try await raw.append(channel: Tandem.inboxTag(uid: w.aliceID, hub: w.f.pin.hub),
                                     body: try Envelope.sealInbox(hub: w.f.pin.hub, uid: w.aliceID, agreement: Bytes(aliceMe.keys.agreement.publicKey.rawRepresentation),
                                                                  plaintext: notice))
        }
        try await w.alice.sync()
        let epochs = await w.alice.homeEpochs.values
        XCTAssertTrue(epochs.allSatisfy { $0.fields["signer"] == .id(aliceMe.certificate) }, "only Alice's own keychain starts her home epochs")
        let heads = await w.alice.homeHeads
        XCTAssertEqual(heads.count, 1)
    }

    /// 2. Removed Carol sends Alice a grant for the new epoch with a key of her own: Alice keeps the real key, and her
    /// next write doesn't go where Carol can read it.
    func testAForgedGrantDoesntReplaceAKey() async throws {
        let w = try await world()
        try await w.bob.remove(w.carolID, from: w.list)
        try await w.carol.sync()
        let carolC = await w.carol.collections[w.list]!
        let e2 = try XCTUnwrap(carolC.statements.keys.first { carolC.startsEpoch($0) && $0 != carolC.genesis })
        let carolMe = await w.carol.me!, aliceMe = await w.alice.me!
        let fake = Bytes(repeating: 7, count: 32)
        let aliceC = await w.alice.collections[w.list]!
        let admission = await w.alice.admission(of: w.aliceID, in: aliceC, at: aliceC.head!)!
        var grant = ControlObject(kind: "grant", fields: ["hub": .id(w.f.pin.hub), "collection": .id(w.list), "recipient": .id(w.aliceID),
            "admission": .id(admission), "at": .id(e2), "epoch": .id(e2), "key": .id(Tandem.commit(fake)),
            "signer": .id(carolMe.certificate), "auth": .id(carolMe.auth)])
        try grant.sign("sig", with: carolMe.keys.signing)
        let g = try grant.json()
        let delivery = JSONWriter.object { d in
            d.member("type", text: Bytes("grant".utf8)); d.member("grant", json: g)
            d.member("certificate", b64u: aliceMe.certificate); d.member("k", b64u: fake)
        }
        let raw = await w.hub.connect()
        _ = try await raw.append(channel: Tandem.inboxTag(uid: w.aliceID, hub: w.f.pin.hub),
                                 body: try Envelope.sealInbox(hub: w.f.pin.hub, uid: w.aliceID,
                                                              agreement: Bytes(aliceMe.keys.agreement.publicKey.rawRepresentation), plaintext: delivery))
        try await w.alice.sync()
        let real = await epochKey(w.bob, w.list)!, held = await epochKey(w.alice, w.list)
        XCTAssertEqual(held?.key, real.key)
        try await w.alice.setField(list: w.list, record: w.alice.newRecordID(), field: "title", value: Bytes("Private".utf8))
        let carolsTag = Tandem.collectionTag(key: fake, hub: w.f.pin.hub, epoch: e2)
        let leaked = await w.hub.messages.filter { $0.channel == carolsTag }
        XCTAssertTrue(leaked.isEmpty)
    }

    /// 3. Alice's revoked tablet, still holding the old epoch's key, signs a same-epoch statement adding someone: refused.
    func testARevokedOwnerKeychainCantExtendTheRoster() async throws {
        let w = try await world(share: false)
        let list = try await w.alice.createList(name: "Alice’s")
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Tablet", store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        try await w.bob.approve(claim: claims[0].id, ms: w.f.managementSeed)
        try await tablet.sync()
        let tabletMe = await tablet.me!
        let old = await epochKey(tablet, list)!
        try await w.bob.revoke(certificate: tabletMe.certificate, ms: w.f.managementSeed)
        try await w.alice.sync()
        try await w.alice.rekey(list: list)
        try await tablet.sync()
        let c = await tablet.collections[list]!
        let head = try XCTUnwrap(c.head)
        var next = c.statements[head]!.statement
        next.n += 1
        next.members.append(.init(uid: w.carolID, role: .writer, name: [], floor: next.members[0].floor))
        next.members.sort { $0.uid.lexicographicallyPrecedes($1.uid) }
        var stmt = Write(hub: w.f.pin.hub, certificate: tabletMe.certificate, authorization: tabletMe.auth, scope: list, record: Roster.record,
                         field: Roster.field, op: .assign, value: next.json(), author: w.aliceID,
                         dot: Dot(device: tabletMe.installation, millis: 1, counter: 0), ref: head)
        try stmt.sign(with: tabletMe.keys.signing)
        let tag = Tandem.collectionTag(key: old.key, hub: w.f.pin.hub, epoch: old.epoch)
        let raw = await w.hub.connect()
        _ = try await raw.append(channel: tag, body: try Envelope.seal(key: old.key, hub: w.f.pin.hub, kind: 1, tag: tag,
                                                                     plaintext: try recordPayload(record: Roster.record, writes: [stmt])))
        try await w.alice.sync()
        let members = await w.alice.members(list: list).map(\.uid)
        XCTAssertFalse(members.contains(w.carolID))
    }

    /// 4. Removed Carol deletes the announcement; Alice, offline throughout, still moves to the new epoch through her
    /// grant, and her writes don't land where Carol reads.
    func testADeletedAnnouncementDoesntStrandAMember() async throws {
        let w = try await world()
        let old = await epochKey(w.bob, w.list)!
        try await w.bob.remove(w.carolID, from: w.list)
        let new = await epochKey(w.bob, w.list)!
        let oldTag = Tandem.collectionTag(key: old.key, hub: w.f.pin.hub, epoch: old.epoch)
        var announcement: [String] = []
        for m in await w.hub.messages where m.channel == oldTag {
            let (_, p) = try Envelope.open(key: old.key, hub: w.f.pin.hub, tag: oldTag, envelope: m.body)
            if case let .record(s) = try Kind1.parse(p, hub: w.f.pin.hub, scope: w.list),
               s.writes.contains(where: { (try? $0.id()) == new.epoch }) { announcement.append(m.id) }
        }
        XCTAssertFalse(announcement.isEmpty)
        await w.hub.drop(announcement)
        try await w.alice.sync()
        let alices = await epochKey(w.alice, w.list)
        XCTAssertEqual(alices?.epoch, new.epoch)
        let item = w.alice.newRecordID()
        try await w.alice.setField(list: w.list, record: item, field: "title", value: Bytes("Private".utf8))
        try await w.bob.sync()
        let seen = await w.bob.item(list: w.list, id: item)?.text("title")
        XCTAssertEqual(seen, "Private")
        // And Bob's devices put the announcement back.
        var restored = false
        for m in await w.hub.messages where m.channel == oldTag {
            let (_, p) = try Envelope.open(key: old.key, hub: w.f.pin.hub, tag: oldTag, envelope: m.body)
            if case let .record(s) = try Kind1.parse(p, hub: w.f.pin.hub, scope: w.list),
               s.writes.contains(where: { (try? $0.id()) == new.epoch }) { restored = true }
        }
        XCTAssertTrue(restored)
    }

    /// 5. A write another keychain of Alice made late in the old epoch isn't signed again by her phone.
    func testOnlyTheAuthoringInstallationSignsALateWriteAgain() async throws {
        let w = try await world()
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Tablet", store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        try await w.bob.approve(claim: claims[0].id, ms: w.f.managementSeed)
        try await tablet.sync()
        try await w.alice.sync()
        await w.bob.crash(at: "3:roster")
        do { try await w.bob.remove(w.carolID, from: w.list); XCTFail() } catch is Crash {}
        let thief = tablet.newRecordID()
        try await tablet.setField(list: w.list, record: thief, field: "title", value: Bytes("thief".utf8))
        try await w.alice.sync()
        await w.bob.crash(at: nil)
        try await w.bob.resume()
        try await w.alice.sync()
        try await w.bob.sync()
        let atBob = await w.bob.item(list: w.list, id: thief)
        XCTAssertNil(atBob?.text("title"), "the phone doesn't sign the tablet's write as its own")
    }

    /// 6. A second installation of Bob's that last synced before a removal shares the list: it syncs first, so the
    /// list doesn't get two successors of one statement.
    func testAStaleDeviceSharingDoesntKillTheList() async throws {
        let w = try await world(share: false)
        try await w.bob.share(list: w.list, with: w.aliceID, role: .writer)
        let bob2 = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: w.bobKeys, store: try Store(path: ":memory:"),
                                   hub: await w.hub.connect())
        try await bob2.sync()
        try await w.bob.remove(w.aliceID, from: w.list)
        try await bob2.share(list: w.list, with: w.carolID, role: .writer)
        try await w.bob.sync()
        let dead = await w.bob.isDead(list: w.list), dead2 = await bob2.isDead(list: w.list)
        XCTAssertFalse(dead); XCTAssertFalse(dead2)
        let members = await w.bob.members(list: w.list).map(\.uid)
        XCTAssertTrue(members.contains(w.carolID)); XCTAssertFalse(members.contains(w.aliceID))
    }

    /// 7. Carol is added after a key change: she reads, writes and is listed.
    func testAMemberAddedAfterAKeyChangeCanUseTheList() async throws {
        let w = try await world(share: false)
        try await w.bob.setField(list: w.list, record: w.bob.newRecordID(), field: "title", value: Bytes("Before".utf8))
        try await w.bob.rekey(list: w.list)
        try await w.bob.rekey(list: w.list)
        try await w.bob.share(list: w.list, with: w.carolID, role: .writer)
        try await w.carol.sync()
        try await w.carol.accept(list: w.list)
        let role = await w.carol.role(list: w.list)
        XCTAssertEqual(role, .writer)
        let titles = await w.carol.items(list: w.list).compactMap { $0.text("title") }
        XCTAssertEqual(titles, ["Before"])
        let mine = w.carol.newRecordID()
        try await w.carol.setField(list: w.list, record: mine, field: "title", value: Bytes("Carol’s".utf8))
        let after = w.bob.newRecordID()
        try await w.bob.setField(list: w.list, record: after, field: "title", value: Bytes("After".utf8))
        try await w.bob.sync()
        try await w.carol.sync()
        let atBob = await w.bob.item(list: w.list, id: mine)?.text("title")
        let atCarol = await w.carol.item(list: w.list, id: after)?.text("title")
        XCTAssertEqual(atBob, "Carol’s"); XCTAssertEqual(atCarol, "After")
    }
}
