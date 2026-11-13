import XCTest
import Foundation
import Tandem
@testable import TandemEngine

final class DeviceTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "secret")

    /// The creator founds a hub; another device that knows only the genesis and directory key reads it back.
    func testFoundAndRead() async throws {
        let hub = MemoryHub()
        let keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Mac")
        let creator = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, store: try Store(path: ":memory:"),
                                      hub: await hub.connect())
        try await creator.publish(f)
        let messages = await hub.messages
        XCTAssertEqual(messages.count, 5)   // genesis, certificate, snapshot, recovery box; the inbox delivery

        let reader = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await reader.sync()
        let registry = await reader.registry
        XCTAssertEqual(registry.activity(try f.certificate.id()), .valid)
        XCTAssertEqual(registry.authorizes(snapshot: try f.snapshot.id(), certificate: try f.certificate.id()), .valid)
        let notices = await reader.notices
        XCTAssertEqual(notices.map(\.type), ["home-recovery"])
        // Nothing new: a second sync applies nothing.
        let again = try await reader.sync()
        XCTAssertEqual(again, 0)
    }

    /// The hub password unlocks management on a device that never held it (§6.7). Three 1 GiB stretches.
    func testTheHubPasswordUnlocksManagement() async throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["TANDEM_SKIP_ARGON2_GIB"] == "1", "TANDEM_SKIP_ARGON2_GIB=1")
        let hub = MemoryHub()
        var f = try Founder.found(account: account, keys: DeviceKeys(), label: "Mac")
        f.capsule = try Founder.capsule(password: "correct horse battery staple", pin: f.pin, managementSeed: f.managementSeed)
        let creator = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, store: try Store(path: ":memory:"),
                                      hub: await hub.connect())
        try await creator.publish(f)
        let reader = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await reader.sync()
        let ms = try await reader.unlock(password: "correct horse battery staple")
        XCTAssertEqual(ms, f.managementSeed)
        do { _ = try await reader.unlock(password: "wrong horse battery staple"); XCTFail("wrong password") } catch {}
    }

    /// A crash after journalling, before or between appends, resumes with the same bytes and no duplicates.
    func testPublishingResumesWithoutDuplicates() async throws {
        let hub = MemoryHub()
        let f = try Founder.found(account: account, keys: DeviceKeys(), label: "Mac")
        let store = try Store(path: ":memory:")
        let device = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, store: store, hub: await hub.connect())
        try await device.publish(f)
        // Simulate the crash: the journal survived, the step ran again.
        let envelopes = await hub.messages.map { ($0.channel, $0.body) }
        try store.setJournal("found", Device.pack(envelopes))
        try await device.publish(f)
        let count = await hub.messages.count
        XCTAssertEqual(count, 5)
        XCTAssertNil(try store.journal("found"))
    }

    func testAStoreBelongsToOneHub() async throws {
        let store = try Store(path: ":memory:")
        let a = try Founder.found(account: account, keys: DeviceKeys(), label: "Mac")
        let b = try Founder.found(account: account, keys: DeviceKeys(), label: "Mac")
        let hub = MemoryHub()
        _ = try Device.open(genesis: a.genesis, directoryKey: a.directoryKey, store: store, hub: await hub.connect())
        let other = await MemoryHub().connect()
        XCTAssertThrowsError(try Device.open(genesis: b.genesis, directoryKey: b.directoryKey, store: store, hub: other))
        XCTAssertThrowsError(try Device.open(genesis: a.genesis, directoryKey: b.directoryKey, store: try Store(path: ":memory:"),
                                             hub: other))
    }
}
