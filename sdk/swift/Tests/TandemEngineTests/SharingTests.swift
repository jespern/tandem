import XCTest
import Tandem
@testable import TandemEngine

final class SharingTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    func founded() async throws -> (MemoryHub, Founding, Device) {
        let hub = MemoryHub(), keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        return (hub, f, bob)
    }

    /// The whole path of §13.1 and §11: invite, join, share, offer, accept, edit both ways.
    func testInviteJoinShareAccept() async throws {
        let (hub, f, bob) = try await founded()
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let received = try InvitationLink.parse(try link.json())    // as if scanned from a QR code
        XCTAssertEqual(received.account, account)

        let aliceKeys = DeviceKeys()
        let alice = try await Device.join(link: received, keys: aliceKeys, label: "Alice’s iPhone",
                                          store: try Store(path: ":memory:"), hub: await hub.connect())
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        XCTAssertNotEqual(aliceID, f.participant)
        try await alice.setHome("settings:liszto", field: "order", value: [1])   // her own home works

        try await bob.sync()
        let bobsView = await bob.registry.activity(try XCTUnwrap(aliceMe?.certificate))
        XCTAssertEqual(bobsView, .valid, "Bob's device accepts Alice's admission through the invitation")

        let groceries = try await bob.createList(name: "Groceries")
        let milk = bob.newRecordID()
        try await bob.setField(list: groceries, record: milk, field: "title", value: Bytes("Milk".utf8))
        try await bob.share(list: groceries, with: aliceID, role: .writer, name: "Alice")

        try await alice.sync()
        let offers = await alice.pendingOffers()
        XCTAssertEqual(offers.map(\.list), [groceries])
        XCTAssertEqual(offers.first?.name, "Groceries")
        XCTAssertEqual(offers.first?.owner, f.participant)
        let notYet = await alice.lists()
        XCTAssertTrue(notYet.isEmpty, "nothing appears without her saying so (§11.2)")

        try await alice.accept(list: groceries)
        let joined = await alice.lists()
        XCTAssertEqual(joined.map(\.id), [groceries])
        let afterAccept = await alice.pendingOffers()
        XCTAssertTrue(afterAccept.isEmpty)
        let title = await alice.value(list: groceries, record: milk, field: "title")
        XCTAssertEqual(title, Bytes("Milk".utf8))

        let bread = alice.newRecordID()
        try await alice.setField(list: groceries, record: bread, field: "title", value: Bytes("Bread".utf8))
        try await bob.sync()
        let seen = await bob.value(list: groceries, record: bread, field: "title")
        XCTAssertEqual(seen, Bytes("Bread".utf8))

        // Alice's Mac, on her synced keychain, gets the list from her home with no grant of its own.
        let aliceMac = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: aliceKeys,
                                       store: try Store(path: ":memory:"), hub: await hub.connect())
        try await aliceMac.sync()
        let macLists = await aliceMac.lists()
        XCTAssertEqual(macLists.map(\.id), [groceries])
        let macBread = await aliceMac.value(list: groceries, record: bread, field: "title")
        XCTAssertEqual(macBread, Bytes("Bread".utf8))
    }

    func testABadOrExpiredInvitationAdmitsNobody() async throws {
        let (hub, f, bob) = try await founded()
        let link = try await bob.invite(label: "Alice", account: account, ms: f.managementSeed)
        let tampered = InvitationLink(hub: link.hub, genesis: link.genesis, directoryKey: link.directoryKey, account: link.account,
                                      secret: Bytes(repeating: 1, count: 32), invitation: link.invitation)
        do {
            _ = try await Device.join(link: tampered, keys: DeviceKeys(), label: "x", store: try Store(path: ":memory:"), hub: await hub.connect())
            XCTFail("a wrong secret")
        } catch {}
        let expired = try await bob.invite(label: "Late", account: account, ms: f.managementSeed, validFor: -1)
        do {
            _ = try await Device.join(link: expired, keys: DeviceKeys(), label: "x", store: try Store(path: ":memory:"), hub: await hub.connect())
            XCTFail("an expired invitation")
        } catch {}
        do {
            _ = try await bob.invite(label: "x", account: account, ms: Bytes(repeating: 3, count: 32))
            XCTFail("another hub's management seed")
        } catch {}
    }

    /// A reader can read but not write; a non-owner can't share.
    func testRolesHold() async throws {
        let (hub, f, bob) = try await founded()
        let link = try await bob.invite(label: "Carol", account: account, ms: f.managementSeed)
        let carol = try await Device.join(link: link, keys: DeviceKeys(), label: "Carol’s iPad", store: try Store(path: ":memory:"),
                                          hub: await hub.connect())
        try await bob.sync()
        let house = try await bob.createList(name: "House")
        let carolMe = await carol.me
        let carolID = try XCTUnwrap(carolMe?.participant)
        try await bob.share(list: house, with: carolID, role: .reader)
        try await carol.sync()
        try await carol.accept(list: house)
        do { try await carol.setField(list: house, record: carol.newRecordID(), field: "t", value: [1]); XCTFail("a reader wrote") } catch {}
        do { try await carol.share(list: house, with: f.participant, role: .writer); XCTFail("a non-owner shared") } catch {}
    }
}
