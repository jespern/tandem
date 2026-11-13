import XCTest
import Tandem
@testable import TandemEngine

/// What Liszto does with lists day to day: items with sets and conflicts, people by name, answers, leaving, deleting.
final class ListLifeTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    func hub() async throws -> (MemoryHub, Founding, Device, Device, ID, DeviceKeys) {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        try await bob.setName("Bob")
        let alice = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"),
                                          hub: await hub.connect())
        try await alice.setName("Alice")
        try await bob.sync()
        let me = await alice.me
        return (hub, f, bob, alice, try XCTUnwrap(me?.participant), bobKeys)
    }

    func testPeopleHaveNamesAndDevices() async throws {
        let (_, f, bob, alice, aliceID, _) = try await hub()
        try await alice.sync()
        let people = await alice.people()
        XCTAssertEqual(people.map(\.name), ["Alice", "Bob"])
        XCTAssertEqual(people.first { $0.isMe }?.id, aliceID)
        XCTAssertEqual(people.first { $0.isCreator }?.id, f.participant)
        XCTAssertEqual(people.first { $0.isMe }?.devices.map(\.label), ["Alice’s iPhone"])
        let bobsView = await bob.name(of: aliceID)
        XCTAssertEqual(bobsView, "Alice")
        try await alice.setName("Alice B.")
        try await bob.sync()
        let renamed = await bob.name(of: aliceID)
        XCTAssertEqual(renamed, "Alice B.")
    }

    func testItemsSetsConflictsAndAttribution() async throws {
        let (_, _, bob, alice, aliceID, _) = try await hub()
        let list = try await bob.createList(name: "Groceries")
        try await bob.share(list: list, with: aliceID, role: .writer)
        try await alice.sync()
        try await alice.accept(list: list)
        let milk = bob.newRecordID()
        try await bob.setField(list: list, record: milk, field: "title", value: Bytes("Milk".utf8))
        try await bob.add(list: list, record: milk, field: "tags", element: Bytes("dairy".utf8))
        try await alice.sync()
        // Concurrent edits of one note, and a concurrent remove and add of one tag.
        try await alice.setField(list: list, record: milk, field: "notes", value: Bytes("oat".utf8))
        try await bob.setField(list: list, record: milk, field: "notes", value: Bytes("whole".utf8))
        try await alice.remove(list: list, record: milk, field: "tags", element: Bytes("dairy".utf8))
        try await bob.add(list: list, record: milk, field: "tags", element: Bytes("cold".utf8))
        try await alice.sync()
        try await bob.sync()
        for d in [alice, bob] {
            let held = await d.item(list: list, id: milk)
            let item = try XCTUnwrap(held)
            XCTAssertEqual(item.text("title"), "Milk")
            XCTAssertEqual(Set(item.values("notes").map { String(decoding: $0.value, as: UTF8.self) }), ["oat", "whole"],
                           "both texts of a conflicting note")
            XCTAssertEqual(item.elements("tags"), [Bytes("cold".utf8)], "Alice saw dairy and removed it")
            XCTAssertEqual(item.fields, ["notes", "tags", "title"])
            XCTAssertNotNil(item.lastModifiedBy)
        }
        let name = await alice.name(list: list)
        XCTAssertEqual(name, "Groceries")
        try await bob.rename(list: list, to: "Food")
        do { try await alice.rename(list: list, to: "Mine"); XCTFail("only the owner renames") } catch {}
        try await alice.sync()
        let renamed = await alice.name(list: list)
        XCTAssertEqual(renamed, "Food")
    }

    func testDeclineLeaveAndTheOwnerSeesIt() async throws {
        let (_, _, bob, alice, aliceID, _) = try await hub()
        let house = try await bob.createList(name: "House")
        try await bob.share(list: house, with: aliceID, role: .writer)
        let pending = try await bob.memberStatus(list: house)
        XCTAssertEqual(pending.map(\.status), [.pending])
        try await alice.sync()
        try await alice.decline(list: house)
        let none = await alice.lists()
        XCTAssertTrue(none.isEmpty)
        let offers = await alice.pendingOffers()
        XCTAssertTrue(offers.isEmpty, "a declined offer isn't asked again")
        try await bob.sync()
        let declined = try await bob.memberStatus(list: house)
        XCTAssertEqual(declined.map(\.status), [.declined])

        let garden = try await bob.createList(name: "Garden")
        try await bob.share(list: garden, with: aliceID, role: .writer)
        try await alice.sync()
        try await alice.accept(list: garden)
        try await bob.sync()
        let accepted = try await bob.memberStatus(list: garden)
        XCTAssertEqual(accepted.map(\.status), [.accepted])
        try await alice.leave(list: garden)
        let after = await alice.lists()
        XCTAssertTrue(after.isEmpty, "she stops following it")
        try await bob.sync()
        let left = try await bob.memberStatus(list: garden)
        XCTAssertEqual(left.map(\.status), [.left], "Alice left Garden")
        try await bob.remove(aliceID, from: garden)
        let gone = try await bob.memberStatus(list: garden)
        XCTAssertTrue(gone.isEmpty)
    }

    func testDeletingAList() async throws {
        let (_, _, bob, alice, aliceID, _) = try await hub()
        let list = try await bob.createList(name: "Old")
        try await bob.share(list: list, with: aliceID, role: .writer)
        try await alice.sync()
        try await alice.accept(list: list)
        try await bob.delete(list: list)
        let bobs = await bob.lists()
        XCTAssertTrue(bobs.isEmpty)
        try await alice.sync()
        let deleted = await alice.isDeleted(list: list)
        XCTAssertTrue(deleted)
        let alices = await alice.lists()
        XCTAssertTrue(alices.isEmpty)
        do { try await alice.setField(list: list, record: alice.newRecordID(), field: "t", value: [1]); XCTFail("deleted") } catch {}
        do { try await bob.share(list: list, with: aliceID, role: .reader); XCTFail("deleted") } catch {}
    }
}
