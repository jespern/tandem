import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// The hub admin (§6.7, §6.8, §13.1). Tests named testTheHubPassword… stretch at 1 GiB: `sdk/check.py` runs them optimized.
final class AdminTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")
    var skipArgon: Bool { ProcessInfo.processInfo.environment["TANDEM_SKIP_ARGON2_GIB"] == "1" }

    func testACancelledInvitationIsRefused() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let made = await bob.invitations()
        XCTAssertEqual(made.map(\.label), ["Alice"])
        XCTAssertEqual(made.first?.hint, "word")
        XCTAssertEqual(made.first?.cancelled, false)
        try await bob.cancelInvitation(capability: try link.invitation.id(), ms: f.managementSeed)
        let after = await bob.invitations()
        XCTAssertEqual(after.first?.cancelled, true)
        do {
            _ = try await Device.join(link: link, keys: DeviceKeys(), label: "x", store: try Store(path: ":memory:"), hub: await hub.connect())
            XCTFail("a cancelled invitation")
        } catch {}
    }

    func testTheHubPasswordChanges() async throws {
        try XCTSkipIf(skipArgon, "TANDEM_SKIP_ARGON2_GIB=1")
        let hub = MemoryHub(), keys = DeviceKeys()
        var f = try Founder.found(account: account, keys: keys, label: "Mac")
        f.capsule = try Founder.capsule(password: "correct horse battery staple", pin: f.pin, managementSeed: f.managementSeed)
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        try await bob.changePassword(to: "purple monkey dishwasher sky", ms: f.managementSeed)
        let reader = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await reader.sync()
        let current = await reader.currentCapsules
        XCTAssertEqual(current.count, 1)
        let held = await reader.capsules
        XCTAssertEqual(held.count, 1, "the old capsule was destroyed")
        let ms = try await reader.unlock(password: "purple monkey dishwasher sky")
        XCTAssertEqual(ms, f.managementSeed)
        do { _ = try await reader.unlock(password: "correct horse battery staple"); XCTFail("the old password") } catch {}
    }

    /// Bob, the creator, loses every device and his keychain; with the hub password a new Mac gets his home and lists.
    func testTheHubPasswordRecoversTheCreator() async throws {
        try XCTSkipIf(skipArgon, "TANDEM_SKIP_ARGON2_GIB=1")
        let hub = MemoryHub(), lost = DeviceKeys()
        var f = try Founder.found(account: account, keys: lost, label: "Bob’s iPhone")
        f.capsule = try Founder.capsule(password: "correct horse battery staple", pin: f.pin, managementSeed: f.managementSeed)
        let phone = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: lost, store: try Store(path: ":memory:"),
                                    hub: await hub.connect())
        try await phone.publish(f)
        try await phone.setHome("settings:liszto", field: "order", value: [3])
        let list = try await phone.createList(name: "House")

        let mac = try await Device.recoverCreator(genesis: f.genesis, directoryKey: f.directoryKey, password: "correct horse battery staple",
                                                  keys: DeviceKeys(), label: "Bob’s new Mac", store: try Store(path: ":memory:"),
                                                  hub: await hub.connect())
        let me = await mac.me
        XCTAssertEqual(me?.participant, f.participant, "the same person")
        let order = await mac.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, [3])
        let lists = await mac.lists()
        XCTAssertEqual(lists.map(\.id), [list])
        try await mac.setField(list: list, record: mac.newRecordID(), field: "title", value: Bytes("Back".utf8))
    }
}
