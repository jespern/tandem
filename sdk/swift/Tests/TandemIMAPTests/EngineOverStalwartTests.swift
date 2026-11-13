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
}
