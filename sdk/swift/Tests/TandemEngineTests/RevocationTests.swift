import XCTest
import Tandem
@testable import TandemEngine

/// §6.4 and §10.8: Bob, the admin, revokes Alice's tablet; her phone rekeys her home without it.
final class RevocationTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, founding: Founding, bob: Device
        let aliceKeys: DeviceKeys, phone: Device, tablet: Device, aliceID: ID, tabletCert: ID
    }

    func store() throws -> Store { try Store(path: ":memory:") }

    func world() async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s phone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try store(), hub: await hub.connect())
        try await bob.publish(f)
        let aliceKeys = DeviceKeys()
        let phone = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: aliceKeys, label: "Alice’s phone", store: try store(), hub: await hub.connect())
        let link = try await phone.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Alice’s tablet", store: try store(),
                                           hub: await hub.connect())
        try await bob.sync()
        let claims = await bob.pendingClaims()
        try await bob.approve(claim: try XCTUnwrap(claims.first).id, ms: f.managementSeed)
        try await tablet.sync()
        try await phone.sync()
        let aliceMe = await phone.me, tabletMe = await tablet.me
        return World(hub: hub, founding: f, bob: bob, aliceKeys: aliceKeys, phone: phone, tablet: tablet,
                     aliceID: try XCTUnwrap(aliceMe?.participant), tabletCert: try XCTUnwrap(tabletMe?.certificate))
    }

    func testRevokedTabletIsCutOutOfHome() async throws {
        let w = try await world()
        try await w.phone.setHome("settings:liszto", field: "order", value: [1])
        try await w.tablet.sync()
        let shared = await w.tablet.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(shared, [1])
        let before = await w.phone.homeHeads

        try await w.bob.revoke(certificate: w.tabletCert, ms: w.founding.managementSeed)
        try await w.phone.sync()
        // The tablet, not yet aware, writes a new record into the old epoch after the phone rekeyed.
        try await w.tablet.setHome("settings:sneaky", field: "x", value: [9])

        let after = await w.phone.homeHeads
        XCTAssertEqual(after.count, 1)
        XCTAssertNotEqual(after, before, "the phone started a new home epoch")
        let order = await w.phone.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, [1], "home carried over")
        try await w.phone.setHome("settings:liszto", field: "order", value: [2])
        try await w.phone.sync()
        let sneaky = await w.phone.homeValue("settings:sneaky", field: "x")
        XCTAssertNil(sneaky, "a late write in the replaced epoch doesn't enter the current home")

        try await w.tablet.sync()
        let revoked = await w.tablet.isRevoked
        XCTAssertTrue(revoked)
        let stale = await w.tablet.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(stale, [1], "the tablet has no key to the new epoch")
        do { try await w.tablet.setHome("settings:liszto", field: "order", value: [3]); XCTFail("no home to write to") } catch {}
        let phoneOrder = await w.phone.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(phoneOrder, [2])

        // Bob's devices accept the new epoch's writes and Alice's current snapshot.
        let bobsView = await w.bob.registry.activity(w.tabletCert)
        XCTAssertEqual(bobsView, .invalid("revoked"))
    }

    /// A device paired after the rekey can't read the old channel: it gets the home through the new epoch's
    /// inventory and the copies of earlier starts, and the lists through the carried memberships.
    func testDevicePairedAfterTheRekeyReadsTheCarriedHome() async throws {
        let w = try await world()
        try await w.phone.setHome("settings:liszto", field: "order", value: [1])
        let groceries = try await w.phone.createList(name: "Groceries")
        let milk = w.phone.newRecordID()
        try await w.phone.setField(list: groceries, record: milk, field: "title", value: Bytes("Milk".utf8))
        try await w.bob.revoke(certificate: w.tabletCert, ms: w.founding.managementSeed)
        try await w.phone.sync()

        let link = try await w.phone.startPairing(label: "Laptop", account: account)
        let laptop = try await Device.pair(link: link, keys: DeviceKeys(), label: "Alice’s laptop", store: try store(),
                                           hub: await w.hub.connect())
        try await w.bob.sync()
        let claims = await w.bob.pendingClaims()
        try await w.bob.approve(claim: try XCTUnwrap(claims.first).id, ms: w.founding.managementSeed)
        try await laptop.sync()
        let order = await laptop.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, [1])
        let lists = await laptop.lists()
        XCTAssertEqual(lists.map(\.id), [groceries])
        let title = await laptop.value(list: groceries, record: milk, field: "title")
        XCTAssertEqual(title, Bytes("Milk".utf8))
        try await laptop.setHome("settings:liszto", field: "order", value: [4])
        try await w.phone.sync()
        let seen = await w.phone.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(seen, [4])
    }

    /// Two installations on Alice's keychain rekey at the same moment: two heads, both followed, joined by the next.
    func testConcurrentRekeysAreJoined() async throws {
        let w = try await world()
        try await w.phone.setHome("settings:liszto", field: "order", value: [1])
        let mac = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.aliceKeys,
                                  store: try store(), hub: await w.hub.connect())
        try await mac.sync()
        try await w.phone.rotateHome()
        try await mac.rotateHome()
        try await w.phone.setHome("settings:a", field: "x", value: [1])
        try await mac.setHome("settings:b", field: "x", value: [2])
        try await w.phone.sync()
        try await mac.sync()
        let heads = await w.phone.homeHeads, macHeads = await mac.homeHeads
        XCTAssertEqual(heads.count, 2)
        XCTAssertEqual(heads, macHeads)
        for d in [w.phone, mac] {
            let a = await d.homeValue("settings:a", field: "x"), b = await d.homeValue("settings:b", field: "x")
            XCTAssertEqual(a, [1]); XCTAssertEqual(b, [2])
        }
        try await w.phone.rotateHome()
        try await mac.sync()
        let joined = await mac.homeHeads
        XCTAssertEqual(joined.count, 1)
        let a = await mac.homeValue("settings:a", field: "x"), b = await mac.homeValue("settings:b", field: "x")
        XCTAssertEqual(a, [1]); XCTAssertEqual(b, [2])
    }

    /// The rekey is journalled; a crash before its messages leave finishes with the same bytes on the next sync.
    func testInterruptedRekeyFinishes() async throws {
        let w = try await world()
        try await w.phone.setHome("settings:liszto", field: "order", value: [1])
        let phoneStore = try store()
        let flaky = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.aliceKeys,
                                    store: phoneStore, hub: await w.hub.connect())
        try await flaky.sync()
        let offline = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.aliceKeys,
                                      store: phoneStore, hub: FailingAppends(await w.hub.connect()))
        do { try await offline.rotateHome(); XCTFail("offline") } catch {}
        XCTAssertEqual(try phoneStore.journalSteps().count, 1)
        let again = try Device.open(genesis: w.founding.genesis, directoryKey: w.founding.directoryKey, keys: w.aliceKeys,
                                    store: phoneStore, hub: await w.hub.connect())
        try await again.sync()
        XCTAssertEqual(try phoneStore.journalSteps(), [])
        try await w.phone.sync()
        let heads = await w.phone.homeHeads, mine = await again.homeHeads
        XCTAssertEqual(heads, mine)
        let order = await w.phone.homeValue("settings:liszto", field: "order")
        XCTAssertEqual(order, [1])
    }
}
