import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// The app's running Device and an extension's ItemWriter on one store file (Liszto DESIGN §5.6).
final class SharedStoreTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, f: Founding, keys: DeviceKeys, path: String, app: Device, laptop: Device, list: Bytes
    }

    func world() async throws -> World {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("tandem-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let path = dir.appendingPathComponent("hub.sqlite").path
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s iPhone")
        let app = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: path), hub: await hub.connect())
        try await app.publish(f)
        let list = try await app.createList(name: "Inbox")
        // Another device of the same person, to see what reached the hub.
        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await laptop.sync()
        return World(hub: hub, f: f, keys: keys, path: path, app: app, laptop: laptop, list: list)
    }

    func title(_ d: Device, _ list: Bytes, _ id: Bytes) async -> String? { await d.item(list: list, id: id)?.text("title") }

    /// Offline in the extension: the running app takes the write in when told, announces it, and sends it.
    func testAnOfflineExtensionWriteGoesOutThroughTheRunningApp() async throws {
        let w = try await world()
        let events = await w.app.events()
        let writer = try ItemWriter.open(store: try Store(path: w.path), keys: w.keys, list: w.list)
        let item = Tandem.namedID(Bytes("item".utf8))
        let sent = try await writer.write(record: item, [.set("title", Bytes("From Safari".utf8)), .add("tags", Bytes("link".utf8))],
                                          hub: FailingAppends(await w.hub.connect()))
        XCTAssertFalse(sent)
        try await w.app.refresh()
        _ = try await LiveTests.next(events) { if case let .items(l, r) = $0 { return l == w.list && r.contains(item) }; return false }
        let atApp = await title(w.app, w.list, item)
        XCTAssertEqual(atApp, "From Safari")
        try await w.laptop.sync()
        let atLaptop = await w.laptop.item(list: w.list, id: item)
        XCTAssertEqual(atLaptop?.text("title"), "From Safari")
        XCTAssertEqual(atLaptop?.elements("tags"), [Bytes("link".utf8)])
    }

    /// Without `refresh()`: the next thing the app does picks the write up, and an edit of the same record carries it.
    func testTheAppsNextEditCarriesTheExtensionsWrite() async throws {
        let w = try await world()
        let writer = try ItemWriter.open(store: try Store(path: w.path), keys: w.keys, list: w.list)
        let item = w.app.newRecordID()
        try await writer.write(record: item, [.set("title", Bytes("Milk".utf8))], hub: nil)
        try await w.app.setField(list: w.list, record: item, field: "done", value: [1])
        try await w.laptop.sync()
        let atLaptop = await w.laptop.item(list: w.list, id: item)
        XCTAssertEqual(atLaptop?.text("title"), "Milk", "the app's whole-record message has the extension's write")
        XCTAssertEqual(atLaptop?.value("done"), [1])
    }

    /// Online: the extension sends the record itself; the app applies its own installation's message like any other.
    func testAnOnlineExtensionWriteReachesEveryone() async throws {
        let w = try await world()
        let writer = try ItemWriter.open(store: try Store(path: w.path), keys: w.keys, list: w.list)
        let item = w.app.newRecordID()
        let sent = try await writer.write(record: item, [.set("title", Bytes("Eggs".utf8))], hub: await w.hub.connect())
        XCTAssertTrue(sent)
        try await w.laptop.sync()
        let atLaptop = await title(w.laptop, w.list, item)
        XCTAssertEqual(atLaptop, "Eggs")
        try await w.app.sync()
        let atApp = await title(w.app, w.list, item)
        XCTAssertEqual(atApp, "Eggs")
        try await writer.write(record: item, [.delete], hub: await w.hub.connect())
        try await w.laptop.sync()
        let gone = await w.laptop.items(list: w.list)
        XCTAssertTrue(gone.isEmpty)
    }

    /// The writer refuses what it can't do, so the extension saves to the app's own store instead.
    func testTheWriterRefusesClearly() async throws {
        let w = try await world()
        XCTAssertThrowsError(try ItemWriter.open(store: try Store(path: ":memory:"), keys: w.keys, list: w.list)) {
            XCTAssertEqual($0 as? ItemWriter.Refusal, .notOnHub)
        }
        XCTAssertThrowsError(try ItemWriter.open(store: try Store(path: w.path), keys: DeviceKeys(), list: w.list)) {
            XCTAssertEqual($0 as? ItemWriter.Refusal, .notOnHub, "another keychain's keys")
        }
        let writer = try ItemWriter.open(store: try Store(path: w.path), keys: w.keys, list: w.list)
        do { try await writer.write(record: Roster.record, [.set("s", [1])], hub: nil); XCTFail() } catch ItemWriter.Refusal.cannotWrite {}
        try await w.app.delete(list: w.list)
        XCTAssertThrowsError(try ItemWriter.open(store: try Store(path: w.path), keys: w.keys, list: w.list)) {
            XCTAssertEqual($0 as? ItemWriter.Refusal, .closed)
        }
        XCTAssertThrowsError(try ItemWriter.open(store: try Store(path: w.path), keys: w.keys, list: Bytes(repeating: 1, count: 16))) {
            XCTAssertEqual($0 as? ItemWriter.Refusal, .closed)
        }
    }
}
