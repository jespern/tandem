import XCTest
import Tandem
@testable import TandemEngine

/// §10.7 and §12.2: the hub keeps about one message per record, and what a hostile hub drops comes back.
final class HygieneTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    func world() async throws -> (MemoryHub, Founding, DeviceKeys, Device, Device, Bytes, ID) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let alice = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"),
                                          hub: await hub.connect())
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        try await bob.sync()
        let list = try await bob.createList(name: "House")
        try await bob.share(list: list, with: aliceID, role: .writer)
        try await alice.sync()
        try await alice.accept(list: list)
        return (hub, f, keys, bob, alice, list, aliceID)
    }

    func channel(_ d: Device, _ list: Bytes) async -> String {
        let c = await d.collections[list]!
        let epoch = c.epoch(of: c.head!)!
        return Tandem.collectionTag(key: c.keys[epoch]!, hub: d.pin.hub, epoch: epoch)
    }

    func testEditsLeaveOneMessagePerRecord() async throws {
        let (hub, _, _, bob, alice, list, _) = try await world()
        let item = bob.newRecordID()
        for i in 0..<10 { try await bob.setField(list: list, record: item, field: "title", value: Bytes("v\(i)".utf8)) }
        try await bob.setField(list: list, record: item, field: "done", value: [1])
        try await alice.setField(list: list, record: item, field: "notes", value: Bytes("hi".utf8))
        for d in [bob, alice, bob, alice] { try await d.sync() }   // the merged state goes out, then the rest is collected
        let tag = await channel(bob, list)
        let messages = await hub.messages.filter { $0.channel == tag }
        let key = await bob.collections[list]!.keys[bob.collections[list]!.epoch(of: bob.collections[list]!.head!)!]!
        var perRecord: [Bytes: Int] = [:]
        for m in messages {
            let (_, p) = try Envelope.open(key: key, hub: bob.pin.hub, tag: tag, envelope: m.body)
            if case let .record(s) = try Kind1.parse(p, hub: bob.pin.hub, scope: list), s.record != Roster.record { perRecord[s.record, default: 0] += 1 }
        }
        XCTAssertEqual(perRecord[item], 1, "eleven edits on two devices leave one message")
        let fresh = await alice.item(list: list, id: item)
        XCTAssertEqual(fresh?.text("title"), "v9")
        XCTAssertEqual(fresh?.text("notes"), "hi")
    }

    func testWhatTheHubDropsComesBack() async throws {
        let (hub, f, keys, bob, alice, list, _) = try await world()
        let item = alice.newRecordID()
        try await alice.setField(list: list, record: item, field: "title", value: Bytes("Milk".utf8))
        try await bob.sync()
        let tag = await channel(bob, list)
        // A participant with the mailbox password deletes every data message of the list.
        let ids = await hub.messages.filter { $0.channel == tag }.map(\.id)
        await hub.drop(ids)
        try await bob.sync()   // Bob's device notices and publishes what it holds again
        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await laptop.sync()
        let seen = await laptop.item(list: list, id: item)
        XCTAssertEqual(seen?.text("title"), "Milk")
        let name = await laptop.name(list: list)
        XCTAssertEqual(name, "House")
    }
}
