import Foundation
import XCTest
@testable import Tandem
@testable import TandemEngine

/// What Liszto asked of the app API (2026-11-14): one message per batched write, the list's properties in `_meta`,
/// creating or accepting a list without the hub, and an offer that survives a restart.
final class AppWriteTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    func temporaryPath() -> String {
        FileManager.default.temporaryDirectory.appendingPathComponent("tandem-app-\(UUID().uuidString).sqlite").path
    }

    func founded(storePath: String = ":memory:") async throws -> (MemoryHub, Founding, Device, DeviceKeys) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: storePath),
                                  hub: await hub.connect())
        try await bob.publish(f)
        return (hub, f, bob, keys)
    }

    /// Several fields of one record go out as one message, and arrive together.
    func testABatchedWriteIsOneMessage() async throws {
        let (hub, f, bob, keys) = try await founded()
        let list = try await bob.createList(name: "Groceries")
        let before = await hub.messages.count
        let milk = bob.newRecordID()
        try await bob.write(list: list, record: milk, [.set("title", Bytes("Milk".utf8)), .set("notes", Bytes("Oat".utf8)),
                                                       .add("tags", Bytes("dairy".utf8))])
        let after = await hub.messages.count
        XCTAssertEqual(after - before, 1)

        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await laptop.sync()
        let fetched = await laptop.item(list: list, id: milk)
        let item = try XCTUnwrap(fetched)
        XCTAssertEqual(item.text("title"), "Milk")
        XCTAssertEqual(item.text("notes"), "Oat")
        XCTAssertEqual(item.elements("tags"), [Bytes("dairy".utf8)])

        try await bob.write(list: list, record: milk, [.delete])
        try await laptop.sync()
        let gone = await laptop.item(list: list, id: milk)?.deleted
        XCTAssertEqual(gone, true)
    }

    /// The owner keeps the app's list properties in `_meta`; another device reads them.
    func testTheOwnerWritesListPropertiesInMeta() async throws {
        let (hub, f, bob, keys) = try await founded()
        let list = try await bob.createList(name: "Groceries")
        try await bob.write(list: list, record: Device.metaRecord, [.set("icon", Bytes("cart".utf8)), .set("tagsEnabled", Bytes("1".utf8))])

        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await laptop.sync()
        let fetched = await laptop.item(list: list, id: Device.metaRecord)
        let meta = try XCTUnwrap(fetched)
        XCTAssertEqual(meta.text("icon"), "cart")
        XCTAssertEqual(meta.text("tagsEnabled"), "1")
        XCTAssertEqual(meta.text("name"), "Groceries")
        let name = await laptop.name(list: list)
        XCTAssertEqual(name, "Groceries", "the name stays where rename and lists() read it")
    }

    /// Creating a list without the hub doesn't throw and leaves nothing half done: the next sync sends it all.
    func testCreatingAListOfflineSendsItLater() async throws {
        let path = temporaryPath()
        defer { try? FileManager.default.removeItem(atPath: path) }
        let (hub, f, bob, keys) = try await founded(storePath: path)
        try await bob.sync()

        let failing = FailingAppends(await hub.connect())
        let offline = try XCTUnwrap(try Device.reopen(store: try Store(path: path), keys: keys, hub: failing))
        let list = try await offline.createList(name: "Weekend")
        let local = await offline.lists().map(\.id)
        XCTAssertTrue(local.contains(list))

        let working = await hub.connect()
        let back = try XCTUnwrap(try Device.reopen(store: try Store(path: path), keys: keys, hub: working))
        try await back.sync()
        let laptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                     hub: await hub.connect())
        try await laptop.sync()
        let there = await laptop.lists().first { $0.id == list }
        XCTAssertEqual(there?.name, "Weekend")
        _ = bob
    }

    /// Accepting a list without the hub records the whole answer: the list follows on every device once back.
    func testAcceptingOfflineFollowsTheList() async throws {
        let (hub, f, bob, _) = try await founded()
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let aliceKeys = DeviceKeys()
        let network = SwitchedAppends(await hub.connect())
        let alice = try await Device.join(link: try InvitationLink.parse(try link.json()), keys: aliceKeys, label: "Alice’s iPhone",
                                          store: try Store(path: ":memory:"), hub: network)
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        try await bob.sync()
        let groceries = try await bob.createList(name: "Groceries")
        try await bob.share(list: groceries, with: aliceID, role: .writer, name: "Alice")
        try await alice.sync()

        network.online = false
        try await alice.accept(list: groceries)
        let followed = await alice.lists().map(\.id)
        XCTAssertEqual(followed, [groceries])

        network.online = true
        try await alice.sync()
        let aliceLaptop = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: aliceKeys,
                                          store: try Store(path: ":memory:"), hub: await hub.connect())
        try await aliceLaptop.sync()
        let there = await aliceLaptop.lists().map(\.id)
        XCTAssertEqual(there, [groceries], "her other devices follow it too")
    }

    /// An offer received but not yet answered survives a restart (it was lost: its list's roster wasn't loaded).
    func testAnOfferSurvivesARestart() async throws {
        let (hub, f, bob, _) = try await founded()
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let aliceKeys = DeviceKeys(), alicePath = temporaryPath()
        defer { try? FileManager.default.removeItem(atPath: alicePath) }
        let alice = try await Device.join(link: try InvitationLink.parse(try link.json()), keys: aliceKeys, label: "Alice’s iPhone",
                                          store: try Store(path: alicePath), hub: await hub.connect())
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        try await bob.sync()
        let groceries = try await bob.createList(name: "Groceries")
        let milk = bob.newRecordID()
        try await bob.setField(list: groceries, record: milk, field: "title", value: Bytes("Milk".utf8))
        try await bob.share(list: groceries, with: aliceID, role: .writer, name: "Alice")
        try await alice.sync()
        let before = await alice.pendingOffers().map(\.list)
        XCTAssertEqual(before, [groceries])

        let working = await hub.connect()
        let restarted = try XCTUnwrap(try Device.reopen(store: try Store(path: alicePath), keys: aliceKeys, hub: working))
        try await restarted.sync()
        let after = await restarted.pendingOffers().map(\.list)
        XCTAssertEqual(after, [groceries], "the offer is still there after a restart")
        try await restarted.accept(list: groceries)
        let followed = await restarted.lists().map(\.id)
        XCTAssertEqual(followed, [groceries], "and it can still be accepted")
        let name = await restarted.name(list: groceries)
        XCTAssertEqual(name, "Groceries", "with its name")
        let title = await restarted.item(list: groceries, id: milk)?.text("title")
        XCTAssertEqual(title, "Milk", "and the items written before the share")
    }

    /// A crash in the middle of accepting leaves the offer open: the answer, which closes it, is written last.
    func testACrashWhileAcceptingLeavesTheOfferOpen() async throws {
        let (hub, f, bob, _) = try await founded()
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let aliceKeys = DeviceKeys(), alicePath = temporaryPath()
        defer { try? FileManager.default.removeItem(atPath: alicePath) }
        let alice = try await Device.join(link: try InvitationLink.parse(try link.json()), keys: aliceKeys, label: "Alice’s iPhone",
                                          store: try Store(path: alicePath), hub: await hub.connect())
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        try await bob.sync()
        let groceries = try await bob.createList(name: "Groceries")
        try await bob.share(list: groceries, with: aliceID, role: .writer, name: "Alice")
        try await alice.sync()

        await alice.crash(at: "accept:keys")
        do { try await alice.accept(list: groceries); XCTFail("expected the crash") } catch is Crash {}

        let working = await hub.connect()
        let restarted = try XCTUnwrap(try Device.reopen(store: try Store(path: alicePath), keys: aliceKeys, hub: working))
        let pending = await restarted.pendingOffers().map(\.list)
        XCTAssertEqual(pending, [groceries], "the offer is still open")
        try await restarted.accept(list: groceries)
        let followed = await restarted.lists().map(\.id)
        XCTAssertEqual(followed, [groceries])
    }

    /// Home writes are local first, like list writes: offline they don't throw, and the next sync sends them.
    func testAHomeWriteOfflineIsSentLater() async throws {
        let (hub, f, bob, keys) = try await founded()
        let network = SwitchedAppends(await hub.connect())
        let phone = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                    hub: network)
        try await phone.sync()
        network.online = false
        try await phone.setHome("settings:liszto", field: "theme", value: Bytes("dark".utf8))
        let local = await phone.homeValue("settings:liszto", field: "theme")
        XCTAssertEqual(local, Bytes("dark".utf8))

        network.online = true
        try await phone.sync()
        try await bob.sync()
        let there = await bob.homeValue("settings:liszto", field: "theme")
        XCTAssertEqual(there, Bytes("dark".utf8), "healing sent it")
    }
}

/// A connection whose appends fail while `online` is false.
final class SwitchedAppends: PushStore, @unchecked Sendable {
    // @unchecked: `online` is a test switch flipped between awaited steps, never concurrently with an append.
    let inner: MemoryHub.Connection
    var online = true
    init(_ inner: MemoryHub.Connection) { self.inner = inner }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> { try await inner.watch() }
    func changes(since cursor: String) async throws -> ChangeBatch { try await inner.changes(since: cursor) }
    func append(channel: String, body: Bytes) async throws -> String {
        guard online else { throw TandemError.invalid("offline") }
        return try await inner.append(channel: channel, body: body)
    }
    func fetch(ids: [String]) async throws -> [Message] { try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func close() async { await inner.close() }
}
