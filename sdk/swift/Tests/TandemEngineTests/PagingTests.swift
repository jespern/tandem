import Foundation
import XCTest
@testable import Tandem
@testable import TandemEngine

/// A hub that answers a page at a time (`ChangeBatch.more`): sync goes on page after page, and keeps each page it took.
final class PagingTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")
    let titles = ["Milk", "Eggs", "Bread", "Oats", "Tea"]

    func groceries() async throws -> (MemoryHub, Founding, DeviceKeys, Bytes) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let list = try await bob.createList(name: "Groceries")
        for title in titles { try await bob.setField(list: list, record: bob.newRecordID(), field: "title", value: Bytes(title.utf8)) }
        await hub.setPageSize(3)
        return (hub, f, keys, list)
    }

    func titles(_ d: Device, _ list: Bytes) async -> Set<String> { Set(await d.items(list: list).compactMap { $0.text("title") }) }

    func testANewDeviceSyncsPageAfterPage() async throws {
        let (hub, f, keys, list) = try await groceries()
        let counting = PagesThenFail(await hub.connect(), failAt: .max)
        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: counting)
        try await laptop.sync()
        let got = await titles(laptop, list)
        XCTAssertEqual(got, Set(titles))
        let messages = await hub.messages.count
        XCTAssertGreaterThanOrEqual(counting.calls, messages / 3, "paged")
    }

    func testAStoppedSyncKeepsItsPages() async throws {
        let (hub, f, keys, list) = try await groceries()
        let flaky = PagesThenFail(await hub.connect(), failAt: 3)
        let store = try Store(path: ":memory:")
        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: store, hub: flaky)
        do { try await laptop.sync(); XCTFail("expected the lost connection") } catch {}
        let cursor = try store.value("cursor").map { String(decoding: $0, as: UTF8.self) }
        XCTAssertEqual(cursor, "6", "the two pages taken are kept")
        flaky.failAt = .max
        try await laptop.sync()
        let got = await titles(laptop, list)
        XCTAssertEqual(got, Set(titles))
    }
}

/// Serves changes from a hub connection, and fails from the call numbered `failAt` on.
final class PagesThenFail: PushStore, @unchecked Sendable {
    // @unchecked: the counters are touched by one sync at a time, and the switch between awaited steps.
    let inner: MemoryHub.Connection
    var calls = 0, failAt: Int
    init(_ inner: MemoryHub.Connection, failAt: Int) { self.inner = inner; self.failAt = failAt }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> { try await inner.watch() }
    func changes(since cursor: String) async throws -> ChangeBatch {
        calls += 1
        guard calls < failAt else { throw TandemError.invalid("connection lost") }
        return try await inner.changes(since: cursor)
    }
    func append(channel: String, body: Bytes) async throws -> String { try await inner.append(channel: channel, body: body) }
    func fetch(ids: [String]) async throws -> [Message] { try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func close() async { await inner.close() }
}
