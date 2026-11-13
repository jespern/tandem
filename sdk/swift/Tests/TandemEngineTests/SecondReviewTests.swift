import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// Regression tests for the review of the Go engine (2026-11-13), whose findings also held for this engine.
final class SecondReviewTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    struct World {
        let hub: MemoryHub, f: Founding, bob: Device, phone: Device, tablet: Device, aliceID: ID, tabletCert: ID, list: Bytes
    }

    /// Bob owns a list shared with Alice, who has a phone and a paired tablet; Bob then revokes the tablet.
    func world(revoke: Bool = true) async throws -> World {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let phone = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s phone", store: try Store(path: ":memory:"), hub: await hub.connect())
        let tablet = try await Device.pair(link: try await phone.startPairing(label: "Tablet", account: account), keys: DeviceKeys(),
                                           label: "Alice’s tablet", store: try Store(path: ":memory:"), hub: await hub.connect())
        try await bob.sync()
        let claims = await bob.pendingClaims()
        try await bob.approve(claim: claims[0].id, ms: f.managementSeed)
        try await tablet.sync()
        try await phone.sync()
        let aliceID = await phone.me!.participant, tabletCert = await tablet.me!.certificate
        let list = try await bob.createList(name: "House")
        try await bob.share(list: list, with: aliceID, role: .writer)
        try await phone.sync()
        try await phone.accept(list: list)
        try await tablet.sync()
        if revoke { try await bob.revoke(certificate: tabletCert, ms: f.managementSeed) }
        return World(hub: hub, f: f, bob: bob, phone: phone, tablet: tablet, aliceID: aliceID, tabletCert: tabletCert, list: list)
    }

    func epochKey(_ d: Device, _ list: Bytes) async -> (epoch: ID, key: Bytes)? {
        let c = await d.collections[list]!
        guard let head = c.head, let e = c.epoch(of: head), let k = c.keys[e] else { return nil }
        return (e, k)
    }

    /// 1. The revoked tablet writes a change-in-flight note naming the new epoch with a key of its own into Alice's
    /// home: her phone keeps the real key, and its writes don't go where the tablet reads.
    func testAKeyFromHomeDoesntDisplaceTheListKey() async throws {
        let w = try await world()
        try await w.bob.rekey(list: w.list)
        let real = await epochKey(w.bob, w.list)!
        // Without Bob's grant the planted key is the only one the phone sees, whatever order it reads them in.
        let inbox = Tandem.inboxTag(uid: w.aliceID, hub: w.f.pin.hub)
        await w.hub.drop(await w.hub.messages.filter { $0.channel == inbox }.map(\.id))
        try await w.tablet.sync()
        let fake = Bytes(repeating: 9, count: 32)
        let note = FlightNote(parent: real.epoch, statement: real.epoch, key: fake, members: [], what: "x", device: "x")
        try await w.tablet.setHome("membership:" + B64u.encode(w.list), field: "flight", value: note.json())
        try await w.tablet.addHome("membership:" + B64u.encode(w.list), field: "keys", element: Device.keyElement(epoch: real.epoch, key: fake))
        try await w.phone.sync()
        try await w.phone.sync()
        let held = await w.phone.collections[w.list]!.keys[real.epoch]
        XCTAssertNotEqual(held, fake, "a key from home counts only once the epoch's statement commits to it")
        try? await w.phone.setField(list: w.list, record: w.phone.newRecordID(), field: "title", value: Bytes("Private".utf8))
        let leaked = await w.hub.messages.filter { $0.channel == Tandem.collectionTag(key: fake, hub: w.f.pin.hub, epoch: real.epoch) }
        XCTAssertTrue(leaked.isEmpty)
    }

    /// 2. After Alice's phone rekeyed her home without the tablet, the tablet starts a branch of its own (a start and a
    /// child of it) from the old head and sends the phone the key: neither start enters her home.
    func testARevokedKeychainCantBranchTheHome() async throws {
        let w = try await world()
        try await w.phone.sync()   // rekeys home without the tablet
        let tablet = await w.tablet.me!
        let heads = await w.tablet.homeHeads
        let oldHead = try XCTUnwrap(heads.first)
        let oldKey = await w.tablet.homeKeys[oldHead]!
        let k = Bytes(repeating: 4, count: 32)
        let et = try Founder.homeEpoch(pin: w.f.pin, participant: w.aliceID, n: 9, parents: [oldHead], homeKey: k,
                                       recipients: [tablet.certificate], signer: tablet.certificate, auth: tablet.auth, keys: tablet.keys)
        let et2 = try Founder.homeEpoch(pin: w.f.pin, participant: w.aliceID, n: 10, parents: [try et.id()], homeKey: k,
                                        recipients: [tablet.certificate], signer: tablet.certificate, auth: tablet.auth, keys: tablet.keys)
        let raw = await w.hub.connect()
        let oldTag = Tandem.homeTag(key: oldKey, hub: w.f.pin.hub, uid: w.aliceID, epoch: oldHead)
        for start in [et, et2] {
            _ = try await raw.append(channel: oldTag, body: try Envelope.seal(key: oldKey, hub: w.f.pin.hub, kind: 3, tag: oldTag, plaintext: try start.json()))
        }
        let phoneCert = await w.phone.registry.certificate(w.phone.me!.certificate)!
        for e in [et, et2] {
            _ = try await raw.append(channel: Tandem.inboxTag(uid: w.aliceID, hub: w.f.pin.hub),
                                     body: try Envelope.sealInbox(hub: w.f.pin.hub, uid: w.aliceID, agreement: phoneCert.agree,
                                                                  plaintext: Founder.homeKeyNotice(epoch: try e.id(), key: k).json()))
        }
        try await w.phone.sync()
        let epochs = await w.phone.homeEpochs
        XCTAssertNil(epochs[try et.id()])
        XCTAssertNil(epochs[try et2.id()])
    }

    /// 3. The admin's revocation can't reach the hub; after a restart the device sends it, and the others see it.
    func testAnInterruptedRevocationIsSentLater() async throws {
        let w = try await world(revoke: false)
        let store = try Store(path: ":memory:")
        let admin = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: nil, store: store,
                                    hub: FailingAppends(await w.hub.connect()))
        try await admin.sync()
        do { try await admin.revoke(certificate: w.tabletCert, ms: w.f.managementSeed); XCTFail("offline") } catch {}
        let again = try Device.open(genesis: w.f.genesis, directoryKey: w.f.directoryKey, keys: nil, store: store, hub: await w.hub.connect())
        try await again.sync()
        try await w.phone.sync()
        let seen = await w.phone.registry.activity(w.tabletCert)
        XCTAssertEqual(seen, .invalid("revoked"))
    }
}
