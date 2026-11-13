import XCTest
import Tandem
@testable import TandemEngine

/// §6.3: Alice's phone shows a link, her Android tablet (no shared keychain) claims through it,
/// and Bob, the admin, approves or rejects.
final class PairingTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, founding: Founding, bobKeys: DeviceKeys, bob: Device, alice: Device, aliceID: ID
    }

    func world() async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s phone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys,
                                  store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.publish(f)
        let invite = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let alice = try await Device.join(link: invite, keys: DeviceKeys(), label: "Alice’s phone",
                                          store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.sync()
        let me = await alice.me
        return World(hub: hub, founding: f, bobKeys: bobKeys, bob: bob, alice: alice, aliceID: try XCTUnwrap(me?.participant))
    }

    func testApprovedTabletJoinsAlicesHomeAndLists() async throws {
        let w = try await world()
        try await w.alice.setHome("settings:liszto", field: "order", value: [7])
        let groceries = try await w.alice.createList(name: "Groceries")
        let milk = w.alice.newRecordID()
        try await w.alice.setField(list: groceries, record: milk, field: "title", value: Bytes("Milk".utf8))

        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let scanned = try PairingLink.parse(link.json())
        XCTAssertEqual(scanned.participant, w.aliceID)

        let tablet = try await Device.pair(link: scanned, keys: DeviceKeys(), label: "Alice’s tablet",
                                           store: try Store(path: ":memory:"), hub: await w.hub.connect())
        let waiting = await tablet.awaitingApproval
        XCTAssertTrue(waiting, "the tablet says “Waiting for the hub admin”")
        let before = await tablet.lists()
        XCTAssertTrue(before.isEmpty)

        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        XCTAssertEqual(claims.count, 1)
        XCTAssertEqual(claims.first?.participant, w.aliceID, "New device from Alice")
        XCTAssertEqual(claims.first?.label, "Alice’s tablet")
        try await w.bob.approve(claim: try XCTUnwrap(claims.first).id, ms: w.founding.managementSeed)
        let after = await w.bob.pendingClaims()
        XCTAssertTrue(after.isEmpty)

        try await tablet.sync()
        let done = await tablet.awaitingApproval
        XCTAssertFalse(done)
        let me = await tablet.me
        XCTAssertEqual(me?.participant, w.aliceID, "the same person, not a second participant")
        let order = await tablet.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, [7])
        let lists = await tablet.lists()
        XCTAssertEqual(lists.map(\.id), [groceries])
        let title = await tablet.value(list: groceries, record: milk, field: "title")
        XCTAssertEqual(title, Bytes("Milk".utf8))

        // Its writes reach Alice's phone, and Bob's device accepts the certificate.
        let bread = tablet.newRecordID()
        try await tablet.setField(list: groceries, record: bread, field: "title", value: Bytes("Bread".utf8))
        try await w.alice.sync()
        let seen = await w.alice.value(list: groceries, record: bread, field: "title")
        XCTAssertEqual(seen, Bytes("Bread".utf8))
        try await w.bob.sync()
        let bobsView = await w.bob.registry.activity(try XCTUnwrap(me?.certificate))
        XCTAssertEqual(bobsView, .valid)

        // Alice's history records the completed pairing.
        try await w.alice.sync()
        let outcomes = await w.alice.homeElements("pairing:" + B64u.encode(link.token), field: "outcome")
        XCTAssertEqual(outcomes.count, 1)
    }

    func testRejectedClaimGetsNothing() async throws {
        let w = try await world()
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Someone’s tablet",
                                           store: try Store(path: ":memory:"), hub: await w.hub.connect())
        try await w.bob.sync()
        let pending = await w.bob.pendingClaims()
        let claim = try XCTUnwrap(pending.first)
        try await w.bob.reject(claim: claim.id, ms: w.founding.managementSeed)
        let after = await w.bob.pendingClaims()
        XCTAssertTrue(after.isEmpty)

        // Bob's other device sees the rejection too.
        let bobLaptop = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey,
                                        keys: w.bobKeys, store: try Store(path: ":memory:"),
                                        hub: await w.hub.connect())
        try await bobLaptop.sync()
        let elsewhere = await bobLaptop.pendingClaims()
        XCTAssertTrue(elsewhere.isEmpty, "a rejection is in the directory, not only on the device that made it")

        try await tablet.sync()
        let waiting = await tablet.awaitingApproval
        XCTAssertTrue(waiting)
        let me = await tablet.me
        XCTAssertNil(me)
        do { try await w.bob.approve(claim: claim.id, ms: Bytes(repeating: 5, count: 32)); XCTFail("another hub's seed") } catch {}
    }

    func testCancelledOrExpiredLinksCanNotBeApproved() async throws {
        let w = try await world()
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        _ = try await Device.pair(link: link, keys: DeviceKeys(), label: "Tablet", store: try Store(path: ":memory:"),
                                  hub: await w.hub.connect())
        try await w.alice.cancelPairing(token: link.token)
        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        XCTAssertTrue(claims.isEmpty, "Alice cancelled it")

        let late = try await w.alice.startPairing(label: "Old", account: account, validFor: -1)
        do {
            _ = try await Device.pair(link: late, keys: DeviceKeys(), label: "x", store: try Store(path: ":memory:"),
                                      hub: await w.hub.connect())
            XCTFail("an expired link")
        } catch {}
    }

    func testTamperedLinkOrClaimIsRefused() async throws {
        let w = try await world()
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let wrongL = PairingLink(hub: link.hub, genesis: link.genesis, directoryKey: link.directoryKey, account: link.account,
                                 secret: Bytes(repeating: 1, count: 32), token: link.token, participant: link.participant)
        do {
            _ = try await Device.pair(link: wrongL, keys: DeviceKeys(), label: "x", store: try Store(path: ":memory:"),
                                      hub: await w.hub.connect())
            XCTFail("a wrong L reads no pairing")
        } catch {}
        let otherPerson = PairingLink(hub: link.hub, genesis: link.genesis, directoryKey: link.directoryKey, account: link.account,
                                      secret: link.secret, token: link.token, participant: w.founding.participant)
        do {
            _ = try await Device.pair(link: otherPerson, keys: DeviceKeys(), label: "x", store: try Store(path: ":memory:"),
                                      hub: await w.hub.connect())
            XCTFail("a link can't be turned into another participant's")
        } catch {}
    }

    /// The admin crashes after journalling the certificate and release but before they leave: running
    /// sync again sends the same bytes, and the tablet completes (§6.3 step 4).
    func testInterruptedApprovalFinishesWithTheSameBytes() async throws {
        let w = try await world()
        let link = try await w.alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Tablet", store: try Store(path: ":memory:"),
                                           hub: await w.hub.connect())
        let bobStore = try Store(path: ":memory:")
        let bob = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey,
                                  keys: w.bobKeys, store: bobStore, hub: FailingAppends(await w.hub.connect()))
        try await bob.sync()
        let pending = await bob.pendingClaims()
        let claim = try XCTUnwrap(pending.first)
        do { try await bob.approve(claim: claim.id, ms: w.founding.managementSeed); XCTFail("the hub went away") } catch {}
        XCTAssertEqual(try bobStore.journalSteps(), ["approve." + claim.id.hex])

        let again = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey,
                                    keys: w.bobKeys, store: bobStore, hub: await w.hub.connect())
        try await again.sync()
        XCTAssertEqual(try bobStore.journalSteps(), [])
        try await tablet.sync()
        let me = await tablet.me
        XCTAssertEqual(me?.participant, w.aliceID)
        let waiting = await tablet.awaitingApproval
        XCTAssertFalse(waiting)
    }
}

/// A connection whose appends fail, as if the network dropped right after the journal was written.
struct FailingAppends: PushStore {
    let inner: MemoryHub.Connection
    init(_ inner: MemoryHub.Connection) { self.inner = inner }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> { try await inner.watch() }
    func changes(since cursor: String) async throws -> ChangeBatch { try await inner.changes(since: cursor) }
    func append(channel: String, body: Bytes) async throws -> String { throw TandemError.invalid("offline") }
    func fetch(ids: [String]) async throws -> [Message] { try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func close() async { await inner.close() }
}
