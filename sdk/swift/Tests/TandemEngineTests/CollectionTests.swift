import XCTest
import Tandem
@testable import TandemEngine

final class CollectionTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "secret")

    func twoInstallations() async throws -> (MemoryHub, Founding, DeviceKeys, Device, Device, Store) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "iPhone")
        let phoneStore = try Store(path: ":memory:")
        let phone = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: phoneStore, hub: await hub.connect())
        try await phone.publish(f)
        let mac = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await mac.sync()
        return (hub, f, keys, phone, mac, phoneStore)
    }

    /// A list made on the phone appears on the Mac with no invitation (§6.2), and edits flow both ways.
    func testAListReachesTheParticipantsOtherInstallation() async throws {
        let (_, _, _, phone, mac, _) = try await twoInstallations()
        let groceries = try await phone.createList(name: "Groceries")
        let milk = phone.newRecordID()
        try await phone.setField(list: groceries, record: milk, field: "title", value: Bytes("Milk".utf8))

        try await mac.sync()
        let lists = await mac.lists()
        XCTAssertEqual(lists.map(\.id), [groceries])
        XCTAssertEqual(lists.first?.name, "Groceries")
        let title = await mac.value(list: groceries, record: milk, field: "title")
        XCTAssertEqual(title, Bytes("Milk".utf8))

        try await mac.setField(list: groceries, record: milk, field: "done", value: Bytes("true".utf8))
        let bread = mac.newRecordID()
        try await mac.setField(list: groceries, record: bread, field: "title", value: Bytes("Bread".utf8))
        try await phone.sync()
        let done = await phone.value(list: groceries, record: milk, field: "done")
        XCTAssertEqual(done, Bytes("true".utf8))
        let records = await phone.records(list: groceries)
        XCTAssertEqual(Set(records), Set([milk, bread]))

        try await phone.deleteRecord(list: groceries, record: bread)
        try await mac.sync()
        let left = await mac.records(list: groceries)
        XCTAssertEqual(left, [milk])
    }

    func testListsSurviveARestart() async throws {
        let (hub, f, keys, phone, _, store) = try await twoInstallations()
        let list = try await phone.createList(name: "House")
        let r = phone.newRecordID()
        try await phone.setField(list: list, record: r, field: "title", value: [1])
        let reopened = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        let name = await reopened.lists().first?.name
        XCTAssertEqual(name, "House")
        let title = await reopened.value(list: list, record: r, field: "title")
        XCTAssertEqual(title, [1])
        try await reopened.setField(list: list, record: r, field: "title", value: [2])
        let updated = await reopened.value(list: list, record: r, field: "title")
        XCTAssertEqual(updated, [2])
    }

    /// Holding the list key isn't membership: a write by someone the roster doesn't list is refused (§8.3).
    func testAWriteByANonMemberIsRefused() async throws {
        let (hub, f, _, phone, mac, _) = try await twoInstallations()
        let list = try await phone.createList(name: "Groceries")
        try await mac.sync()
        // Someone else on the hub, who somehow has the key.
        let other = DeviceKeys()
        let state = await phone.collections[list]!
        let (epoch, key) = state.keys.first!
        var w = Write(hub: f.pin.hub, certificate: try f.certificate.id(), authorization: try f.snapshot.id(), scope: list,
                      record: phone.newRecordID(), field: Bytes("title".utf8), op: .assign, value: Bytes("Vandal".utf8),
                      author: Bytes(repeating: 7, count: 32), dot: Dot(device: Bytes(repeating: 9, count: 8), millis: 1, counter: 0),
                      ref: state.head!)
        try w.sign(with: other.signing)
        let tag = Tandem.collectionTag(key: key, hub: f.pin.hub, epoch: epoch)
        _ = try await hub.connect().append(channel: tag, body: try Envelope.seal(key: key, hub: f.pin.hub, kind: 1, tag: tag,
                                                                                  plaintext: try recordPayload(record: w.record, writes: [w])))
        try await mac.sync()
        let records = await mac.records(list: list)
        XCTAssertTrue(records.isEmpty)
    }
}
