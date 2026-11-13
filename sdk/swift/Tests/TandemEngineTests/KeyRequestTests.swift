import XCTest
import Tandem
@testable import TandemEngine

/// §11.4: a device without the key of its list's current epoch asks, once, after a safety-net delay.
final class KeyRequestTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    func world() async throws -> (MemoryHub, Founding, DeviceKeys, Device, Device, ID, Bytes) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let carol = try await Device.join(link: try await bob.invite(label: "Carol", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Carol’s iPhone", store: try Store(path: ":memory:"),
                                          hub: await hub.connect())
        await carol.setKeyRequestDelay(.zero)
        let carolMe = await carol.me
        let carolID = try XCTUnwrap(carolMe?.participant)
        try await bob.sync()
        let list = try await bob.createList(name: "House")
        try await bob.share(list: list, with: carolID, role: .writer)
        try await carol.sync()
        try await carol.accept(list: list)
        return (hub, f, keys, bob, carol, carolID, list)
    }

    /// The grant of a new key is deleted and the device that kept it is gone: Carol asks, Bob's Mac answers.
    func testAMemberWhoseGrantWasLostAsks() async throws {
        let (hub, f, keys, bob, carol, carolID, list) = try await world()
        try await bob.rekey(list: list)
        let inbox = Tandem.inboxTag(uid: carolID, hub: f.pin.hub)
        let grants = await hub.messages.filter { $0.channel == inbox }.map(\.id)
        await hub.drop(grants)
        let mac = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await mac.sync()
        try await carol.sync()                         // misses the key, asks
        do { try await carol.setField(list: list, record: carol.newRecordID(), field: "t", value: [1]); XCTFail("no key yet") } catch {}
        try await mac.sync()                           // answers with the grant
        try await carol.sync()
        try await carol.setField(list: list, record: carol.newRecordID(), field: "title", value: Bytes("Back".utf8))
        _ = bob
    }

    /// Carol was away while Bob changed the key and then removed her: the grant for the middle epoch is gone (grants to
    /// someone no longer listed are destroyed), so only a key request tells her she was removed.
    func testADeviceAwayForTwoEpochsLearnsItWasRemoved() async throws {
        let (_, _, _, bob, carol, carolID, list) = try await world()
        try await bob.rekey(list: list)
        try await bob.remove(carolID, from: list)
        try await bob.sync()
        try await carol.sync()                          // sees the first key change, has no grant for it, asks
        let still = await carol.role(list: list)
        XCTAssertEqual(still, .writer, "as far as she can tell")
        try await bob.sync()                            // answers with the roster
        try await carol.sync()
        let role = await carol.role(list: list)
        XCTAssertNil(role, "she learns of her removal")
    }
}
