import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// Copying a list (§11.8): the way out of a dead one (§9.4).
final class CopyTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, f: Founding, bob: Device, bob2: Device, alice: Device, carol: Device, dave: Device
        let aliceID: ID, carolID: ID, daveID: ID, list: Bytes, milk: Bytes
    }

    /// Bob shares House with Alice, then his two devices each add someone while the hub hides one from the other:
    /// the phone adds Carol, the laptop Dave, both after the same statement. The list is dead.
    func deadList() async throws -> World {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        var people: [Device] = []
        for name in ["Alice", "Carol", "Dave"] {
            people.append(try await Device.join(link: try await bob.invite(label: name, account: account, ms: f.managementSeed),
                                                keys: DeviceKeys(), label: name, store: try Store(path: ":memory:"), hub: await hub.connect()))
        }
        try await bob.sync()
        let ids = await people.asyncMap { await $0.me!.participant }
        let list = try await bob.createList(name: "House")
        try await bob.setField(list: list, record: Device.meta, field: "icon", value: Bytes("🏠".utf8))
        let milk = bob.newRecordID()
        try await bob.setField(list: list, record: milk, field: "title", value: Bytes("Milk".utf8))
        try await bob.add(list: list, record: milk, field: "tags", element: Bytes("dairy".utf8))
        try await bob.share(list: list, with: ids[0], role: .writer)
        try await people[0].sync()
        try await people[0].accept(list: list)

        let laptopHub = await hub.connect()
        let bob2 = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"), hub: laptopHub)
        try await bob2.sync()
        let c = await bob.collections[list]!
        let epoch = c.epoch(of: c.head!)!
        await hub.hide(channel: Tandem.collectionTag(key: c.keys[epoch]!, hub: f.pin.hub, epoch: epoch), from: laptopHub)
        try await bob.share(list: list, with: ids[1], role: .writer)
        try await bob2.share(list: list, with: ids[2], role: .reader)
        try await bob.sync()
        let dead = await bob.isDead(list: list)
        XCTAssertTrue(dead, "two successors of one statement")
        return World(hub: hub, f: f, bob: bob, bob2: bob2, alice: people[0], carol: people[1], dave: people[2],
                     aliceID: ids[0], carolID: ids[1], daveID: ids[2], list: list, milk: milk)
    }

    func testTheOwnerCopiesADeadListAndItsMembersFollow() async throws {
        let w = try await deadList()
        let copy = try await w.bob.copy(list: w.list)
        XCTAssertEqual(copy.shared, [w.aliceID], "Alice is in both versions")
        XCTAssertEqual(Set(copy.undecided), [w.carolID, w.daveID], "Carol and Dave are each in one")
        let copied = await w.bob.item(list: copy.list, id: w.milk)
        XCTAssertEqual(copied?.text("title"), "Milk")
        XCTAssertEqual(copied?.elements("tags"), [Bytes("dairy".utf8)])
        let name = await w.bob.name(list: copy.list), dead = await w.bob.isDead(list: copy.list)
        XCTAssertEqual(name, "House")
        XCTAssertFalse(dead)
        let icon = await w.bob.value(list: copy.list, record: Device.meta, field: "icon")
        XCTAssertEqual(icon, Bytes("🏠".utf8))

        try await w.alice.sync()
        let offers = await w.alice.pendingOffers()
        XCTAssertEqual(offers.map(\.list), [copy.list])
        let from = await w.alice.copyOf(list: copy.list)
        XCTAssertEqual(from, w.list, "Alice can tell it continues House")
        try await w.alice.accept(list: copy.list)
        try await w.alice.setField(list: copy.list, record: w.milk, field: "done", value: [1])
        try await w.bob.sync()
        let done = await w.bob.item(list: copy.list, id: w.milk)?.value("done")
        XCTAssertEqual(done, [1])

        // The undecided: the app asks Bob, who adds Carol.
        try await w.bob.share(list: copy.list, with: w.carolID, role: .writer)
        try await w.carol.sync()
        let carolOffers = await w.carol.pendingOffers().map(\.list)
        XCTAssertTrue(carolOffers.contains(copy.list))
    }

    /// Asking again, here or on Bob's other device, finishes the same copy instead of making another.
    func testCopyingAgainKeepsOneCopy() async throws {
        let w = try await deadList()
        let first = try await w.bob.copy(list: w.list)
        let again = try await w.bob.copy(list: w.list)
        XCTAssertEqual(again.list, first.list)
        try await w.bob2.sync()
        let fromLaptop = try await w.bob2.copy(list: w.list)
        XCTAssertEqual(fromLaptop.list, first.list)
        let lists = await w.bob.lists().map(\.id)
        XCTAssertEqual(lists.count, 2, "House and its one copy")
    }

    /// A member may copy too: the copy is theirs, and the old owner is offered it as a writer.
    func testAMemberCopiesAndTheOldOwnerBecomesAWriter() async throws {
        let w = try await deadList()
        try await w.alice.sync()
        let aliceDead = await w.alice.isDead(list: w.list)
        XCTAssertTrue(aliceDead)
        let copy = try await w.alice.copy(list: w.list)
        let bobID = await w.bob.me!.participant
        XCTAssertEqual(copy.shared, [bobID])
        let members = await w.alice.members(list: copy.list)
        XCTAssertEqual(members.first { $0.uid == bobID }?.role, .writer)
        let owner = await w.alice.owner(list: copy.list)
        XCTAssertEqual(owner, w.aliceID)
        try await w.bob.sync()
        let from = await w.bob.copyOf(list: copy.list)
        XCTAssertEqual(from, w.list)
    }

    /// A list that claims to continue another, from someone who was never in it, isn't shown as its continuation.
    func testAStrangersClaimIsntBelieved() async throws {
        let w = try await deadList()
        let notes = try await w.bob.createList(name: "Notes")
        try await w.bob.share(list: notes, with: w.aliceID, role: .writer)
        let fake = try await w.dave.createList(name: "Notes")
        try await w.dave.setField(list: fake, record: Device.meta, field: "copyof", value: notes)
        try await w.dave.share(list: fake, with: w.aliceID, role: .writer)
        try await w.alice.sync()
        try await w.alice.accept(list: notes)
        let from = await w.alice.copyOf(list: fake)
        XCTAssertNil(from, "Notes never listed Dave")
    }
}

extension Array {
    func asyncMap<T>(_ f: (Element) async throws -> T) async rethrows -> [T] {
        var out: [T] = []
        for x in self { out.append(try await f(x)) }
        return out
    }
}
