import XCTest
import CryptoKit
import Foundation
import Tandem
@testable import TandemEngine

/// The registry against the vectors' small hub, and the attacks the Lean registry rules out.
final class RegistryTests: XCTestCase {
    struct Hub {
        let objects: [String: ControlObject]
        let pin: HubPin
        let manager: DeviceKeys
        func id(_ name: String) throws -> ID { try objects[name]!.id() }
    }

    func hub() throws -> Hub {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let v = try JSONSerialization.jsonObject(with: Data(contentsOf: root.appendingPathComponent("vectors/v2/control.json"))) as! [String: Any]
        var objects: [String: ControlObject] = [:]
        for row in v["objects"] as! [[String: Any]] {
            objects[row["name"] as! String] = try ControlObject.parse(Bytes((row["json"] as! String).utf8))
        }
        let m = v["material"] as! [String: String]
        return Hub(objects: objects, pin: try HubPin(genesis: objects["genesis"]!),
                   manager: try DeviceKeys.manager(seed: Bytes(hex: m["management_seed"]!)!))
    }

    func registry(_ h: Hub, _ names: [String]) throws -> Registry {
        var r = Registry(pin: h.pin)
        for n in names { try r.add(h.objects[n]!) }
        return r
    }

    let all = ["creator certificate", "invitation", "alice phone certificate", "delegation", "alice tablet certificate",
               "tablet revoked", "alice snapshot"]

    func testTheVectorsHub() throws {
        let h = try hub()
        let r = try registry(h, all)
        for name in ["creator certificate", "alice phone certificate", "alice tablet certificate"] {
            XCTAssertEqual(r.certificateValidity(try h.id(name)), .valid, name)
        }
        XCTAssertEqual(r.revocationValidity(try h.id("tablet revoked")), .valid)
        XCTAssertEqual(r.activity(try h.id("alice phone certificate")), .valid)
        XCTAssertEqual(r.activity(try h.id("alice tablet certificate")), .invalid("revoked"))
        XCTAssertEqual(r.activity(try h.id("creator certificate")), .valid)
        XCTAssertEqual(r.authorizes(snapshot: try h.id("alice snapshot"), certificate: try h.id("alice phone certificate")), .valid)
        XCTAssertEqual(r.authorizes(snapshot: try h.id("alice snapshot"), certificate: try h.id("alice tablet certificate")),
                       .invalid("revoked"))
        // The snapshot the registry computes is the one the vectors carry.
        let alice = r.certificate(try h.id("alice phone certificate"))!.participant
        XCTAssertEqual(try r.currentSnapshot(of: alice).id(), try h.id("alice snapshot"))
    }

    func testOrderDoesNotMatterAndMissingProofsHold() throws {
        let h = try hub()
        let tablet = try h.id("alice tablet certificate")
        var r = Registry(pin: h.pin)
        try r.add(h.objects["alice tablet certificate"]!)
        XCTAssertEqual(r.certificateValidity(tablet), .pending([try h.id("delegation")]))
        try r.add(h.objects["delegation"]!)
        XCTAssertEqual(r.certificateValidity(tablet), .pending([try h.id("alice phone certificate")]))
        try r.add(h.objects["alice phone certificate"]!)
        XCTAssertEqual(r.certificateValidity(tablet), .pending([try h.id("invitation")]))
        try r.add(h.objects["invitation"]!)
        XCTAssertEqual(r.certificateValidity(tablet), .valid)
        for permutation in [all, all.reversed(), all.shuffled(), all.shuffled()] {
            let r = try registry(h, permutation)
            XCTAssertEqual(r.activity(tablet), .invalid("revoked"))
        }
    }

    func testAnotherHubsObjectsAreRefused() throws {
        let h = try hub()
        var r = Registry(pin: HubPin(hub: h.pin.hub, genesis: Bytes(repeating: 9, count: 32), creator: h.pin.creator))
        XCTAssertThrowsError(try r.add(h.objects["creator certificate"]!))
    }

    /// The manager can't make itself a device of an existing member (`existing_member_needs_own_device`).
    func testTheManagerCannotMintAMembersDevice() throws {
        let h = try hub()
        var r = try registry(h, all)
        let phone = r.certificate(try h.id("alice phone certificate"))!
        let thief = DeviceKeys()
        for how in [Control.first, Control.byDevice, Control.creatorRecovery] {
            var request = ControlObject(kind: "request", fields: ["hub": .id(h.pin.hub), "genesis": .id(h.pin.genesis),
                "participant": .id(phone.participant), "sign": .id(Bytes(thief.signing.publicKey.rawRepresentation)),
                "agree": .id(Bytes(thief.agreement.publicKey.rawRepresentation)), "label": .text(Bytes("Alice’s".utf8)),
                "how": .int(how), "authority": .opt(how == Control.byDevice ? phone.id : nil),
                "enrollment": .id(Bytes(repeating: 0, count: 16))])
            try request.sign("self", with: thief.signing)
            if how == Control.byDevice { try request.sign("approver", with: h.manager.signing) }   // not Alice's phone
            var cert = ControlObject(kind: "certificate", fields: ["hub": .id(h.pin.hub), "genesis": .id(h.pin.genesis),
                "request": .id(try request.id()), "issued": .set([])], extras: ["body": .object(request)])
            try cert.sign("sig", with: h.manager.signing)
            let id = try r.add(cert)
            XCTAssertFalse(r.certificateValidity(id).isValid, "how \(how)")
        }
    }

    /// A revoked approver approves nothing new (`revoked_approver_approves_nothing`), and revoked keys never return.
    func testRevokedKeysStayOut() throws {
        let h = try hub()
        var r = try registry(h, all)
        let tablet = r.certificate(try h.id("alice tablet certificate"))!
        let laptop = DeviceKeys()
        func approvedByTablet(issued: [ID]) throws -> ID {
            var request = ControlObject(kind: "request", fields: ["hub": .id(h.pin.hub), "genesis": .id(h.pin.genesis),
                "participant": .id(tablet.participant), "sign": .id(Bytes(laptop.signing.publicKey.rawRepresentation)),
                "agree": .id(Bytes(laptop.agreement.publicKey.rawRepresentation)), "label": .text(Bytes("Laptop".utf8)),
                "how": .int(Control.byDevice), "authority": .opt(tablet.id), "enrollment": .id(Bytes(repeating: 0, count: 16))])
            try request.sign("self", with: laptop.signing)
            // The tablet's private key isn't in the vectors: sign as a stand-in that holds it.
            let tabletKey = try Curve25519.Signing.PrivateKey(rawRepresentation: H(Bytes("Tandem v2 public control vector: alice tablet signing".utf8)))
            XCTAssertEqual(Bytes(tabletKey.publicKey.rawRepresentation), tablet.sign)
            try request.sign("approver", with: tabletKey)
            var cert = ControlObject(kind: "certificate", fields: ["hub": .id(h.pin.hub), "genesis": .id(h.pin.genesis),
                "request": .id(try request.id()), "issued": .set(issued)], extras: ["body": .object(request)])
            try cert.sign("sig", with: h.manager.signing)
            return try r.add(cert)
        }
        let revocation = try h.id("tablet revoked")
        let afterCut = try approvedByTablet(issued: [revocation])
        XCTAssertEqual(r.certificateValidity(afterCut), .invalid("approving device revoked at issuance"))
        // Issued as if the revocation were unknown: valid as history, but not active once it is held.
        let stale = try approvedByTablet(issued: [])
        XCTAssertEqual(r.certificateValidity(stale), .valid)
        XCTAssertEqual(r.activity(stale), .invalid("revoked"))
        // A fresh certificate for the revoked tablet's own keys is never active again.
        var again = r.certificate(try h.id("alice tablet certificate"))!.object
        again.fields["issued"] = .set([revocation])
        try again.sign("sig", with: h.manager.signing)
        let aliasID = try r.add(again)
        XCTAssertNotEqual(r.activity(aliasID), .valid)
    }

    func testAForgedCapabilityOrDelegationAdmitsNobody() throws {
        let h = try hub()
        var r = try registry(h, ["creator certificate", "invitation", "alice phone certificate"])
        var forged = h.objects["invitation"]!
        forged.signatures["sig"] = Bytes(try DeviceKeys().signing.signature(for: try forged.signedInput()))
        XCTAssertThrowsError(try r.add(forged))
        var delegation = h.objects["delegation"]!
        delegation.signatures["sig"] = Bytes(try DeviceKeys().signing.signature(for: try delegation.signedInput()))
        try r.add(delegation)
        try r.add(h.objects["alice tablet certificate"]!)
        XCTAssertEqual(r.certificateValidity(try h.id("alice tablet certificate")),
                       .invalid("delegation not signed by a device of that participant"))
    }
}
