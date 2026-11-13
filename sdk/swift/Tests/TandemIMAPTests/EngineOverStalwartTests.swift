import XCTest
import Foundation
import Tandem
import TandemEngine
@testable import TandemIMAP

/// The engine over the real IMAP binding, against a disposable Stalwart: a hub founded, a member invited and
/// joined, a list shared, accepted and edited by both, every byte through IMAP. Set TANDEM_IMAP_STALWART=1.
final class EngineOverStalwartTests: XCTestCase {
    nonisolated(unsafe) static var server: Stalwart?

    override func setUp() async throws {
        guard ProcessInfo.processInfo.environment["TANDEM_IMAP_STALWART"] == "1" else {
            throw XCTSkip("set TANDEM_IMAP_STALWART=1 to test against a disposable Stalwart in Docker")
        }
        if Self.server == nil {
            guard let docker = Stalwart.docker else { throw XCTSkip("Docker is not available") }
            let server = try Stalwart(docker: docker)
            try await server.start()
            Self.server = server
        }
    }

    override class func tearDown() {
        server?.stop()
        server = nil
        super.tearDown()
    }

    func store(_ folder: String) async throws -> IMAPStore {
        let s = Self.server!
        let imap = IMAPStore(IMAPStore.Configuration(host: "127.0.0.1", port: s.imapPort, username: s.username, password: s.password,
                                                     security: .tlsTrustingAnyCertificateForTesting, folder: folder))
        try await imap.ensureFolder()
        return imap
    }

    func testInviteShareAndEditOverIMAP() async throws {
        let folder = "Tandem-e2e-\(UUID().uuidString.prefix(8))"
        let s = Self.server!
        let account = MailboxAccount(host: "127.0.0.1", port: s.imapPort, user: s.username, password: s.password)
        let bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s Mac")
        let bobIMAP = try await store(folder), aliceIMAP = try await store(folder)
        defer { Task { await bobIMAP.close(); await aliceIMAP.close() } }
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"), hub: bobIMAP)
        try await bob.publish(f)

        let link = try InvitationLink.parse(try await bob.invite(label: "Alice", account: account, ms: f.managementSeed).json())
        let alice = try await Device.join(link: link, keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"),
                                          hub: aliceIMAP)
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        try await bob.sync()

        let groceries = try await bob.createList(name: "Groceries")
        let milk = bob.newRecordID()
        try await bob.setField(list: groceries, record: milk, field: "title", value: Bytes("Milk".utf8))
        try await bob.share(list: groceries, with: aliceID, role: .writer, name: "Alice")

        try await alice.sync()
        let offers = await alice.pendingOffers()
        XCTAssertEqual(offers.first?.name, "Groceries")
        try await alice.accept(list: groceries)
        let bread = alice.newRecordID()
        try await alice.setField(list: groceries, record: bread, field: "title", value: Bytes("Bread".utf8))
        let milkSeen = await alice.value(list: groceries, record: milk, field: "title")
        XCTAssertEqual(milkSeen, Bytes("Milk".utf8))

        try await bob.sync()
        let breadSeen = await bob.value(list: groceries, record: bread, field: "title")
        XCTAssertEqual(breadSeen, Bytes("Bread".utf8))
    }

    /// The first event matching `match`, or a failure after `seconds`: a safety net, not a schedule.
    static func next(_ events: AsyncStream<DeviceEvent>, within seconds: Double = 15,
                     where match: @escaping @Sendable (DeviceEvent) -> Bool) async throws -> DeviceEvent {
        try await withThrowingTaskGroup(of: DeviceEvent?.self) { group in
            group.addTask { for await e in events where match(e) { return e }; return nil }
            group.addTask { try await Task.sleep(for: .seconds(seconds)); return nil }
            defer { group.cancelAll() }
            guard let first = try await group.next(), let e = first else { throw TandemError.invalid("no such event in time") }
            return e
        }
    }

    /// Live over IMAP IDLE: pushes alone carry an offer, an edit, a pairing claim and a key change.
    func testLivePairingAndKeyChangeOverIMAP() async throws {
        let folder = "Tandem-live-\(UUID().uuidString.prefix(8))"
        let s = Self.server!
        let account = MailboxAccount(host: "127.0.0.1", port: s.imapPort, user: s.username, password: s.password)
        let bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s Mac")
        let bobIMAP = try await store(folder), aliceIMAP = try await store(folder), tabletIMAP = try await store(folder)
        defer { Task { await bobIMAP.close(); await aliceIMAP.close(); await tabletIMAP.close() } }
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"), hub: bobIMAP)
        try await bob.publish(f)
        let alice = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"), hub: aliceIMAP)
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        let bobEvents = await bob.events(), aliceEvents = await alice.events()
        let bobRun = Task { try await bob.run() }, aliceRun = Task { try await alice.run() }
        defer { bobRun.cancel(); aliceRun.cancel() }

        let list = try await bob.createList(name: "Groceries")
        try await bob.share(list: list, with: aliceID, role: .writer)
        _ = try await Self.next(aliceEvents) { $0 == .offers }
        try await alice.accept(list: list)
        let milk = alice.newRecordID()
        try await alice.setField(list: list, record: milk, field: "title", value: Bytes("Milk".utf8))
        _ = try await Self.next(bobEvents) { if case let .items(l, r) = $0 { return l == list && r.contains(milk) }; return false }

        // Alice pairs a tablet that doesn't share her keychain; Bob hears of the claim by push and approves.
        let link = try await alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Alice’s tablet", store: try Store(path: ":memory:"),
                                           hub: tabletIMAP)
        let tabletEvents = await tablet.events()
        let tabletRun = Task { try await tablet.run() }
        defer { tabletRun.cancel() }
        _ = try await Self.next(bobEvents) { $0 == .claims }
        let claims = await bob.pendingClaims()
        try await bob.approve(claim: try XCTUnwrap(claims.first).id, ms: f.managementSeed)
        _ = try await Self.next(tabletEvents) { $0 == .approved }
        _ = try await Self.next(tabletEvents) { if case let .items(l, _) = $0 { return l == list }; return false }
        let onTablet = await tablet.value(list: list, record: milk, field: "title")
        XCTAssertEqual(onTablet, Bytes("Milk".utf8))

        // A new key: Alice's devices take it from the grant and keep writing.
        try await bob.rekey(list: list)
        _ = try await Self.next(aliceEvents) { $0 == .lists }
        let bread = alice.newRecordID()
        var wrote = false
        for _ in 0..<8 where !wrote {   // until Alice's device has the new key; each try waits for her next event
            do { try await alice.setField(list: list, record: bread, field: "title", value: Bytes("Bread".utf8)); wrote = true }
            catch { _ = try? await Self.next(aliceEvents, within: 5) { _ in true } }
        }
        XCTAssertTrue(wrote)
        _ = try await Self.next(bobEvents) { if case let .items(l, r) = $0 { return l == list && r.contains(bread) }; return false }
        let seen = await bob.value(list: list, record: bread, field: "title")
        XCTAssertEqual(seen, Bytes("Bread".utf8))
    }
}
