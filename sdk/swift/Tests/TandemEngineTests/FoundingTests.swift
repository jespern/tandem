import XCTest
import Tandem
@testable import TandemEngine

final class FoundingTests: XCTestCase {
    let account = MailboxAccount(host: "IMAP.Example.org", port: 993, user: "hub@example.org", password: "app-password")

    func testANewHubIsValidOnItsOwnTerms() throws {
        let keys = DeviceKeys()
        let f = try Founder.found(account: account, keys: keys, label: "Mac")
        // A device that knows only the genesis can check everything else.
        let pin = try HubPin(genesis: f.genesis)
        XCTAssertEqual(pin, f.pin)
        XCTAssertEqual(pin.creator, f.participant)
        var registry = Registry(pin: pin)
        for message in try f.directoryMessages.dropFirst().dropLast() { try registry.add(try ControlObject.parse(message)) }
        let cert = try f.certificate.id()
        XCTAssertEqual(registry.certificateValidity(cert), .valid)
        XCTAssertEqual(registry.authorizes(snapshot: try f.snapshot.id(), certificate: cert), .valid)
        XCTAssertEqual(try registry.currentSnapshot(of: f.participant).id(), try f.snapshot.id())
        // The genesis lists the mailbox, lowercased, without its password.
        guard case let .mailboxes(boxes)? = f.genesis.fields["mailboxes"] else { return XCTFail() }
        XCTAssertEqual(boxes, [Mailbox(host: Bytes("imap.example.org".utf8), port: 993, user: Bytes("hub@example.org".utf8))])
        XCTAssertFalse(try f.directoryMessages.contains { $0.windowsContain(Bytes("app-password".utf8)) })
        // The first home epoch: signed by the creator's keychain, for its certificate, under its snapshot.
        XCTAssertTrue(f.homeEpoch.verify("sig", publicKey: Bytes(keys.signing.publicKey.rawRepresentation)))
        XCTAssertEqual(f.homeEpoch.fields["recipients"], .set([cert]))
        XCTAssertEqual(f.homeEpoch.fields["auth"], .id(try f.snapshot.id()))
        XCTAssertEqual(f.homeEpoch.fields["key"], .id(Tandem.commit(f.homeKey)))
        // The key reaches the creator's inbox, and the hub password's MS can recover it (§6.8).
        let delivered = try Envelope.openInbox(hub: pin.hub, uid: f.participant, key: keys.agreement, envelope: f.homeKeyDelivery)
        let notice = try Notice.parse(delivered, type: "home-key", fields: ["epoch", "k"])
        XCTAssertEqual(notice["epoch"], try f.homeEpoch.id())
        XCTAssertEqual(notice["k"], f.homeKey)
        let manager = try DeviceKeys.manager(seed: f.managementSeed)
        let recovery = try Notice.parse(f.homeRecovery.json(), type: "home-recovery", fields: ["epoch", "bytes"])
        XCTAssertEqual(try Control.openHomeRecovery(manager.agreement, hub: pin.hub, epoch: recovery["epoch"]!,
                                                    box: recovery["bytes"]!), f.homeKey)
    }

    func testIDsDependOnlyOnTheInputs() throws {
        let keys = DeviceKeys(), secrets = FoundingSecrets.random()
        let a = try Founder.found(account: account, keys: keys, label: "Mac", secrets: secrets)
        let b = try Founder.found(account: account, keys: keys, label: "Mac", secrets: secrets)
        XCTAssertEqual(try a.genesis.id(), try b.genesis.id())
        XCTAssertEqual(try a.certificate.id(), try b.certificate.id())
        XCTAssertEqual(try a.homeEpoch.id(), try b.homeEpoch.id())
        let other = try Founder.found(account: account, keys: keys, label: "Mac", secrets: .random())
        XCTAssertNotEqual(other.pin.hub, a.pin.hub)
    }
}

extension Array where Element == UInt8 {
    func windowsContain(_ needle: Bytes) -> Bool {
        guard needle.count <= count else { return false }
        return (0...(count - needle.count)).contains { Array(self[$0..<($0 + needle.count)]) == needle }
    }
}
