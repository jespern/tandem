import XCTest
import Tandem
@testable import TandemEngine

final class HomeTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "secret")

    func founded() async throws -> (MemoryHub, Founding, DeviceKeys, Device) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "iPhone")
        let phone = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                    hub: await hub.connect())
        try await phone.publish(f)
        return (hub, f, keys, phone)
    }

    /// Two installations on one synced keychain (§6.3): the second needs no pairing and shares the home.
    func testAKeychainThatSyncsSharesTheHome() async throws {
        let (hub, f, keys, phone) = try await founded()
        try await phone.setHome("settings:liszto", field: "order", value: Bytes("groceries,house".utf8))

        let mac = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await mac.sync()
        let macMe = await mac.me, phoneMe = await phone.me
        XCTAssertEqual(macMe?.participant, f.participant)
        XCTAssertEqual(macMe?.certificate, phoneMe?.certificate)
        XCTAssertNotEqual(macMe?.installation, phoneMe?.installation, "each installation has its own dot namespace")
        let order = await mac.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, Bytes("groceries,house".utf8))

        try await mac.addHome("settings:liszto", field: "pinned", element: Bytes("groceries".utf8))
        try await phone.sync()
        let pinned = await phone.homeElements("settings:liszto", field: "pinned")
        XCTAssertEqual(pinned, [Bytes("groceries".utf8)])
        let name = await phone.homeValue("settings:liszto", field: "_name")
        XCTAssertEqual(name, Bytes("settings:liszto".utf8))
    }

    /// A device of another hub participant can't write into this home, even holding its key.
    func testOnlyTheParticipantsKeychainsWriteToItsHome() async throws {
        let (hub, f, _, phone) = try await founded()
        let epoch = try f.homeEpoch.id()
        let stranger = DeviceKeys()
        var w = Write(hub: f.pin.hub, certificate: try f.certificate.id(), authorization: try f.snapshot.id(),
                      scope: Tandem.homeScope(hub: f.pin.hub, uid: f.participant), record: Tandem.namedID(Bytes("settings:x".utf8)),
                      field: Bytes("k".utf8), op: .assign, value: [1], author: f.participant,
                      dot: Dot(device: Bytes(repeating: 1, count: 8), millis: 1, counter: 0), ref: epoch)
        try w.sign(with: stranger.signing)   // not the certificate's key
        let tag = Tandem.homeTag(key: f.homeKey, hub: f.pin.hub, uid: f.participant, epoch: epoch)
        _ = try await hub.connect().append(channel: tag, body: try Envelope.seal(key: f.homeKey, hub: f.pin.hub, kind: 1, tag: tag,
            plaintext: try recordPayload(record: w.record, writes: [w])))
        try await phone.sync()
        let value = await phone.homeValue("settings:x", field: "k")
        XCTAssertNil(value)
    }

    /// Home writes and keys survive a restart: the store is the device's memory.
    func testTheHomeIsInTheStore() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "iPhone")
        let store = try Store(path: ":memory:")
        let phone = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        try await phone.publish(f)
        try await phone.setHome("settings:liszto", field: "order", value: [7])
        let reopened = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        let order = await reopened.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, [7])
        let me = await reopened.me
        XCTAssertEqual(me?.participant, f.participant)
        try await reopened.setHome("settings:liszto", field: "order", value: [8])
        let updated = await reopened.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(updated, [8])
    }
}
