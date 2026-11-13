import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// What an app needs around the engine: reopening after a restart, an extension sharing the store, the keychain.
final class AppTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    func temporaryStore() throws -> (String, Store) {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tandem-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let path = dir.appendingPathComponent("hub.sqlite").path
        return (path, try Store(path: path))
    }

    func testReopenAfterARestartAndAnExtensionSharingTheStore() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let (path, store) = try temporaryStore()
        let app = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        try await app.publish(f)
        let list = try await app.createList(name: "Inbox")

        // The share extension opens the same file, quick-captures an item and leaves.
        let extStore = try Store(path: path)
        let extHub = await hub.connect()
        let ext = try XCTUnwrap(try Device.reopen(store: extStore, keys: keys, hub: extHub))
        let item = ext.newRecordID()
        try await ext.setField(list: list, record: item, field: "title", value: Bytes("From Safari".utf8))
        let extMe = await ext.me, appMe = await app.me
        XCTAssertEqual(extMe?.installation, appMe?.installation, "the same installation, its clock shared through the store")

        try await app.setField(list: list, record: app.newRecordID(), field: "title", value: Bytes("Typed".utf8))
        try await app.sync()
        let titles = Set(await app.items(list: list).compactMap { $0.text("title") })
        XCTAssertEqual(titles, ["From Safari", "Typed"])

        // A restart: the store alone opens the hub again, with everything in it.
        let againHub = await hub.connect()
        let again = try XCTUnwrap(try Device.reopen(store: try Store(path: path), keys: keys, hub: againHub))
        let lists = await again.lists()
        XCTAssertEqual(lists.map(\.name), ["Inbox"])
        let reopened = Set(await again.items(list: list).compactMap { $0.text("title") })
        XCTAssertEqual(reopened, ["From Safari", "Typed"])
        XCTAssertNil(try Device.reopen(store: try Store(path: ":memory:"), keys: keys, hub: againHub))
    }

    func testKeychainKeepsKeysAndHubs() throws {
        let keychain = Keychain(service: "tandem-test-\(UUID().uuidString)", synchronizable: false)
        let keys: DeviceKeys
        do { keys = try keychain.deviceKeys() } catch { throw XCTSkip("no keychain here: \(error)") }
        defer { try? keychain.forgetDeviceKeys() }
        let again = try keychain.deviceKeys()
        XCTAssertEqual(again.signing.publicKey.rawRepresentation, keys.signing.publicKey.rawRepresentation)
        let f = try Founder.found(account: account, keys: keys, label: "Mac")
        try keychain.save(hub: f.pin.hub, .init(account: account, genesis: try f.genesis.json(), directoryKey: f.directoryKey))
        defer { try? keychain.forget(hub: f.pin.hub) }
        let hubs = try keychain.hubs()
        XCTAssertEqual(hubs[f.pin.hub]?.account, account)
        XCTAssertEqual(hubs[f.pin.hub]?.directoryKey, f.directoryKey)
    }

    /// An edit made offline is saved; the next sync with a working connection sends it (§12.2 healing).
    func testAnEditMadeOfflineGoesOutOnTheNextSync() async throws {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let (path, store) = try temporaryStore()
        let online = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: await hub.connect())
        try await online.publish(f)
        let list = try await online.createList(name: "Inbox")

        let failing = FailingAppends(await hub.connect())
        let offline = try XCTUnwrap(try Device.reopen(store: try Store(path: path), keys: keys, hub: failing))
        let item = offline.newRecordID()
        try await offline.setField(list: list, record: item, field: "title", value: Bytes("On the train".utf8))
        let saved = await offline.item(list: list, id: item)?.text("title")
        XCTAssertEqual(saved, "On the train")

        let working = await hub.connect()
        let back = try XCTUnwrap(try Device.reopen(store: try Store(path: path), keys: keys, hub: working))
        try await back.sync()
        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await laptop.sync()
        let there = await laptop.item(list: list, id: item)?.text("title")
        XCTAssertEqual(there, "On the train")
    }
}
