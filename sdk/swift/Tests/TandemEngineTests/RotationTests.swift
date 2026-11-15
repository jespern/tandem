import XCTest
import Tandem
@testable import TandemEngine

/// §11.6–11.7: Bob owns House, shared with Alice and Carol; he changes its key.
final class RotationTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, founding: Founding, bobKeys: DeviceKeys, bobStore: Store, bob: Device
        let alice: Device, carol: Device, aliceID: ID, carolID: ID, house: Bytes, milk: Bytes
    }

    func world() async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys(), bobStore = try Store(path: ":memory:")
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: bobStore, hub: await hub.connect())
        try await bob.publish(f)
        var members: [Device] = []
        for name in ["Alice", "Carol"] {
            let link = try await bob.invite(label: name, account: account, ms: f.managementSeed)
            members.append(try await Device.join(link: link, keys: DeviceKeys(), label: name + "’s phone",
                                                 store: try Store(path: ":memory:"), hub: await hub.connect()))
        }
        try await bob.sync()
        let house = try await bob.createList(name: "House")
        let milk = bob.newRecordID()
        try await bob.setField(list: house, record: milk, field: "title", value: Bytes("Milk".utf8))
        var ids: [ID] = []
        for m in members {
            let me = await m.me
            let id = try XCTUnwrap(me?.participant)
            ids.append(id)
            try await bob.share(list: house, with: id, role: .writer)
            try await m.sync()
            try await m.accept(list: house)
        }
        try await members[0].setField(list: house, record: members[0].newRecordID(), field: "title", value: Bytes("Bread".utf8))
        try await bob.sync()
        return World(hub: hub, founding: f, bobKeys: bobKeys, bobStore: bobStore, bob: bob, alice: members[0], carol: members[1],
                     aliceID: ids[0], carolID: ids[1], house: house, milk: milk)
    }

    func titles(_ d: Device, _ list: Bytes) async -> Set<String> {
        var out = Set<String>()
        for r in await d.records(list: list) {
            if let t = await d.value(list: list, record: r, field: "title") { out.insert(String(decoding: t, as: UTF8.self)) }
        }
        return out
    }

    /// After a removal: Alice moves to the new epoch with everything; Carol is out and her late write stays out.
    func checkRemoved(_ w: World, bob: Device, journal: Store? = nil) async throws {
        try await w.alice.sync()
        let aliceRole = await w.alice.role(list: w.house)
        XCTAssertEqual(aliceRole, .writer)
        let seen = await titles(w.alice, w.house)
        XCTAssertEqual(seen, ["Milk", "Bread"])
        try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "title", value: Bytes("Eggs".utf8))
        try await bob.sync()
        let bobSees = await titles(bob, w.house)
        XCTAssertEqual(bobSees, ["Milk", "Bread", "Eggs"])

        try await w.carol.sync()
        let carolRole = await w.carol.role(list: w.house)
        XCTAssertNil(carolRole, "Carol learns of her removal from the announcement")
        do { try await w.carol.setField(list: w.house, record: w.carol.newRecordID(), field: "title", value: [1]); XCTFail("removed") } catch {}
        let dead = await bob.isDead(list: w.house)
        XCTAssertFalse(dead)
        if let journal { XCTAssertEqual(try journal.journalSteps(), []) }
    }

    func testRemovingAMemberChangesTheKey() async throws {
        let w = try await world()
        let c0 = await w.bob.collections[w.house]!
        let oldEpoch = c0.epoch(of: c0.head!)!
        try await w.bob.remove(w.carolID, from: w.house)
        // Carol, not yet aware, writes into the old epoch.
        try await w.carol.setField(list: w.house, record: w.carol.newRecordID(), field: "title", value: Bytes("Late".utf8))
        try await checkRemoved(w, bob: w.bob, journal: w.bobStore)
        try await w.bob.sync()
        let bobSees = await titles(w.bob, w.house)
        XCTAssertFalse(bobSees.contains("Late"), "a late write in the replaced epoch isn't carried")

        // Step 6 left the old channel with its roster and nothing the new epoch needs.
        let c = await w.bob.collections[w.house]!
        let oldTag = Tandem.collectionTag(key: c.keys[oldEpoch]!, hub: w.founding.pin.hub, epoch: oldEpoch)
        let remaining = await w.hub.messages.filter { $0.channel == oldTag }
        for m in remaining {
            let (_, p) = try Envelope.open(key: c.keys[oldEpoch]!, hub: w.founding.pin.hub, tag: oldTag, envelope: m.body)
            guard case let .record(state) = try Kind1.parse(p, hub: w.founding.pin.hub, scope: w.house) else { continue }
            if state.writes.contains(where: { $0.author != w.carolID }) {
                XCTAssertEqual(state.record, Roster.record, "only the roster and Carol's late write remain in the old channel")
            }
        }
    }

    /// Alice was offline during the change and wrote into the old epoch: her device signs that write again into the
    /// new one. Carol, removed, can't sign hers into an epoch she has no key to; her replica keeps it, read-only, so the
    /// app can offer to rescue it (§11.6, "Moving over").
    func testLateWritesAreSignedAgainByTheirAuthor() async throws {
        let w = try await world()
        try await w.bob.remove(w.carolID, from: w.house)
        try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "title", value: Bytes("Late".utf8))
        try await w.carol.setField(list: w.house, record: w.carol.newRecordID(), field: "title", value: Bytes("Gone".utf8))
        try await w.bob.sync()
        let before = await titles(w.bob, w.house)
        XCTAssertFalse(before.contains("Late"), "refused in the replaced epoch")
        try await w.alice.sync()
        let alice = await titles(w.alice, w.house)
        XCTAssertEqual(alice, ["Milk", "Bread", "Late"])
        try await w.bob.sync()
        let after = await titles(w.bob, w.house)
        XCTAssertEqual(after, ["Milk", "Bread", "Late"], "signed again into the new epoch")
        XCTAssertFalse(after.contains("Gone"))
        try await w.carol.sync()
        let kept = await titles(w.carol, w.house)
        XCTAssertTrue(kept.contains("Gone"), "Carol's replica keeps her edit")
        let role = await w.carol.role(list: w.house)
        XCTAssertNil(role)
        try await w.alice.sync()
        let again = await titles(w.alice, w.house)
        XCTAssertEqual(again, ["Milk", "Bread", "Late"], "signed again once, not on every sync")
    }

    /// The owner's device dies at every crash point; started again, it finishes or tells the user nothing happened.
    func testACrashAtEveryPointResumesOrDrops() async throws {
        for point in ["1:chosen", "2:journal", "2:note", "3:roster", "4:announced", "4:home", "5:grant", "6:cleaned"] {
            let w = try await world()
            await w.bob.crash(at: point)
            do { try await w.bob.remove(w.carolID, from: w.house); XCTFail("\(point): no crash") } catch is Crash {}
            let restarted = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.bobKeys,
                                            store: w.bobStore, hub: await w.hub.connect())
            try await restarted.resume()
            if point == "1:chosen" {
                // Nothing was journalled: the change didn't happen and the user makes it again.
                let c = await restarted.collections[w.house]!
                XCTAssertTrue(c.statements[c.head!]!.statement.members.contains { $0.uid == w.carolID }, point)
                try await restarted.remove(w.carolID, from: w.house)
            }
            try await checkRemoved(w, bob: restarted, journal: w.bobStore)
        }
    }

    /// Bob's iPhone dies with the change uploaded; his Mac finishes it. With nothing uploaded, it discards it.
    func testAnotherOwnerDeviceFinishesOrDiscards() async throws {
        let w = try await world()
        let mac = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.bobKeys,
                                  store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await mac.sync()
        await w.bob.crash(at: "3:roster")
        do { try await w.bob.remove(w.carolID, from: w.house); XCTFail() } catch is Crash {}
        try await mac.sync()
        let pending = try await mac.pendingChange(list: w.house)
        let note = try XCTUnwrap(pending)
        XCTAssertEqual(note.what, "remove a member")
        XCTAssertEqual(note.device, "Bob’s iPhone")
        do { try await mac.delete(list: w.house); XCTFail("blocked by the note") } catch ChangeError.pending {}
        try await mac.finish(list: w.house, seen: note)
        try await checkRemoved(w, bob: mac)
        // The iPhone comes back: its change was made, by the Mac; it carries on without asking.
        let phone = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.bobKeys,
                                    store: w.bobStore, hub: await w.hub.connect())
        try await phone.resume()
        XCTAssertEqual(try w.bobStore.journalSteps(), [])
        let dead = await phone.isDead(list: w.house)
        XCTAssertFalse(dead)
    }

    func testDiscardingAChangeThatNeverLeft() async throws {
        let w = try await world()
        let mac = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.bobKeys,
                                  store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await mac.sync()
        await w.bob.crash(at: "2:note")
        do { try await w.bob.remove(w.carolID, from: w.house); XCTFail() } catch is Crash {}
        try await mac.sync()
        let pending = try await mac.pendingChange(list: w.house)
        let note = try XCTUnwrap(pending)
        do { try await mac.finish(list: w.house, seen: note); XCTFail("nothing to finish") } catch ChangeError.cantFinish {}
        try await mac.discard(list: w.house, seen: note)
        let phone = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.bobKeys,
                                    store: w.bobStore, hub: await w.hub.connect())
        do { try await phone.resume(); XCTFail("the user is told") } catch ChangeError.superseded {}
        XCTAssertEqual(try w.bobStore.journalSteps(), [])
        try await w.carol.sync()
        let stillIn = await w.carol.role(list: w.house)
        XCTAssertEqual(stillIn, .writer, "nothing changed")
        try await mac.remove(w.carolID, from: w.house)
        try await checkRemoved(w, bob: mac)
    }

    func testLoweringARoleChangesTheKeyAndPromotingDoesnt() async throws {
        let w = try await world()
        let before = await w.bob.collections[w.house]!.keys.count
        try await w.bob.setRole(.reader, for: w.aliceID, in: w.house)
        let lowered = await w.bob.collections[w.house]!.keys.count
        XCTAssertEqual(lowered, before + 1)
        try await w.alice.sync()
        do { try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "t", value: [1]); XCTFail("reader") } catch {}
        let seen = await titles(w.alice, w.house)
        XCTAssertEqual(seen, ["Milk", "Bread"], "a reader still reads, under the new key")
        try await w.bob.setRole(.writer, for: w.aliceID, in: w.house)
        let promoted = await w.bob.collections[w.house]!.keys.count
        XCTAssertEqual(promoted, lowered, "a promotion keeps the epoch")
        try await w.alice.sync()
        try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "title", value: Bytes("Tea".utf8))
        try await w.bob.sync()
        let bobSees = await titles(w.bob, w.house)
        XCTAssertTrue(bobSees.contains("Tea"))
    }

    /// §6.4 step 3: a member's device is revoked; writing waits until the owner changes the key, which leaves the
    /// revoked device out.
    func testRevokedMemberDeviceWaitsForTheOwnersRekey() async throws {
        let w = try await world()
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Alice’s tablet", store: try Store(path: ":memory:"),
                                           hub: await w.hub.connect())
        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        try await w.bob.approve(claim: try XCTUnwrap(claims.first).id, ms: w.founding.managementSeed)
        try await tablet.sync()
        let tabletSees = await titles(tablet, w.house)
        XCTAssertEqual(tabletSees, ["Milk", "Bread"])
        let tabletMe = await tablet.me
        let tabletCert = try XCTUnwrap(tabletMe?.certificate)

        try await w.bob.revoke(certificate: tabletCert, ms: w.founding.managementSeed)
        let needing = await w.bob.listsNeedingRekey()
        XCTAssertEqual(needing, [w.house])
        try await w.alice.sync()
        do { try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "t", value: [1]); XCTFail("exposed") } catch {}

        try await w.bob.rekey(list: w.house)
        let after = await w.bob.listsNeedingRekey()
        XCTAssertTrue(after.isEmpty)
        try await w.alice.sync()
        try await w.alice.setField(list: w.house, record: w.alice.newRecordID(), field: "title", value: Bytes("Soap".utf8))
        try await w.bob.sync()
        let bobSees = await titles(w.bob, w.house)
        XCTAssertTrue(bobSees.contains("Soap"))
        try await tablet.sync()
        let stale = await titles(tablet, w.house)
        XCTAssertFalse(stale.contains("Soap"), "the revoked tablet gets no key to the new epoch")
    }

    /// The owner's device merges the writes it carried into the new channel itself, though they reach it before its
    /// statement is announced: they wait for the statement instead of being refused (§8.3, §9.4). Otherwise it can't
    /// tell what those messages hold, to collect them (§10.7, §11.9).
    func testOwnCarriedWritesAreMerged() async throws {
        let w = try await world()
        try await w.bob.remove(w.carolID, from: w.house)
        try await w.bob.sync(); try await w.alice.sync(); try await w.bob.sync()
        let c = await w.bob.collections[w.house]!
        let epoch = c.epoch(of: c.head!)!, key = c.keys[epoch]!
        let tag = Tandem.collectionTag(key: key, hub: w.founding.pin.hub, epoch: epoch)
        let store = await w.bob.store
        var data = 0
        for m in await w.hub.messages where m.channel == tag {
            let (_, p) = try Envelope.open(key: key, hub: w.founding.pin.hub, tag: tag, envelope: m.body)
            guard case let .record(state)? = try? Kind1.parse(p, hub: w.founding.pin.hub, scope: w.house), state.record != Roster.record else { continue }
            data += 1
            XCTAssertEqual(try store.contents(message: m.id).count, state.writes.count, "the owner's device didn't merge a message it carried")
        }
        XCTAssertGreaterThan(data, 0, "nothing carried")
    }
}
