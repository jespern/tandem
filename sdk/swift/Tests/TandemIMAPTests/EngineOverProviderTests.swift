import XCTest
import Foundation
import Tandem
import TandemEngine
@testable import TandemIMAP

/// The engine live against a real provider (Fastmail, Gmail, …), in a folder `Tandem-test` it empties before and after.
/// Set TANDEM_PROVIDER_HOST, TANDEM_PROVIDER_USER and TANDEM_PROVIDER_PASSWORD (an app password); see sdk/swift/README.md.
final class EngineOverProviderTests: XCTestCase {
    var account: MailboxAccount!

    override func setUp() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let host = env["TANDEM_PROVIDER_HOST"], let user = env["TANDEM_PROVIDER_USER"],
              let password = env["TANDEM_PROVIDER_PASSWORD"] else {
            throw XCTSkip("set TANDEM_PROVIDER_HOST, TANDEM_PROVIDER_USER and TANDEM_PROVIDER_PASSWORD to run against a provider")
        }
        account = MailboxAccount(host: host, port: UInt16(env["TANDEM_PROVIDER_PORT"] ?? "993") ?? 993, user: user, password: password)
    }

    func connect() async throws -> IMAPStore {
        let imap = IMAPStore(.init(host: account.host, port: account.port, username: account.user, password: account.password,
                                   folder: "Tandem-test"))
        try await imap.ensureFolder()
        return imap
    }

    func empty() async throws {
        let imap = try await connect()
        let ids = try await imap.changes(since: "").added.map(\.id)
        if !ids.isEmpty { try await imap.destroy(ids: ids) }
        await imap.close()
    }

    func report(_ what: String, since start: ContinuousClock.Instant) {
        let ms = (ContinuousClock.now - start).components.attoseconds / 1_000_000_000_000_000 + (ContinuousClock.now - start).components.seconds * 1000
        FileHandle.standardError.write(Data("PROVIDER \(account.host): \(what) in \(ms) ms\n".utf8))
    }

    func testLiveSharingPairingAndKeyChange() async throws {
        try await empty()
        let bobIMAP = try await connect(), aliceIMAP = try await connect(), tabletIMAP = try await connect()
        defer { Task { await bobIMAP.close(); await aliceIMAP.close(); await tabletIMAP.close() } }
        let bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s Mac")
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
        var start = ContinuousClock.now
        try await bob.share(list: list, with: aliceID, role: .writer)
        _ = try await EngineOverStalwartTests.next(aliceEvents, within: 90) { $0 == .offers }
        report("an offer arrived", since: start)
        try await alice.accept(list: list)
        let milk = alice.newRecordID()
        start = .now
        try await alice.setField(list: list, record: milk, field: "title", value: Bytes("Milk".utf8))
        _ = try await EngineOverStalwartTests.next(bobEvents, within: 90) { if case let .items(l, r) = $0 { return l == list && r.contains(milk) }; return false }
        report("an edit arrived", since: start)

        let link = try await alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Alice’s tablet", store: try Store(path: ":memory:"),
                                           hub: tabletIMAP)
        let tabletEvents = await tablet.events()
        let tabletRun = Task { try await tablet.run() }
        defer { tabletRun.cancel() }
        _ = try await EngineOverStalwartTests.next(bobEvents, within: 90) { $0 == .claims }
        let claims = await bob.pendingClaims()
        start = .now
        try await bob.approve(claim: try XCTUnwrap(claims.first).id, ms: f.managementSeed)
        _ = try await EngineOverStalwartTests.next(tabletEvents, within: 90) { $0 == .approved }
        report("a pairing was approved", since: start)

        start = .now
        try await bob.rekey(list: list)
        report("a key change was made", since: start)
        let bread = alice.newRecordID()
        var wrote = false
        for _ in 0..<12 where !wrote {
            do { try await alice.setField(list: list, record: bread, field: "title", value: Bytes("Bread".utf8)); wrote = true }
            catch { _ = try? await EngineOverStalwartTests.next(aliceEvents, within: 15) { _ in true } }
        }
        XCTAssertTrue(wrote)
        report("Alice wrote under the new key", since: start)
        _ = try await EngineOverStalwartTests.next(bobEvents, within: 90) { if case let .items(l, r) = $0 { return l == list && r.contains(bread) }; return false }
        let seen = await bob.value(list: list, record: bread, field: "title")
        XCTAssertEqual(seen, Bytes("Bread".utf8))
        try await empty()   // a run that fails before here leaves messages; the next one empties the folder first
    }
}
