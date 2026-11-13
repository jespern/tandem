import XCTest
import CryptoKit
import Foundation
import Tandem

final class ControlTests: XCTestCase {
    func vectors() throws -> [String: Any] {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        return try JSONSerialization.jsonObject(with: Data(contentsOf: root.appendingPathComponent("vectors/v2/control.json"))) as! [String: Any]
    }
    func hex(_ value: Any?) -> Bytes { Bytes(hex: value as! String)! }

    func testSchemaMatchesTheVectors() throws {
        let table = try vectors()["schema"] as! [String: [String: Any]]
        XCTAssertEqual(Set(table.keys), Set(Control.schema.keys))
        XCTAssertEqual(Control.order.count, Control.schema.count)
        for (name, row) in table {
            let spec = Control.schema[name]!
            XCTAssertEqual(row["tag"] as? String, spec.tag, name)
            XCTAssertEqual((row["fields"] as! [[String]]).map { $0[0] + ":" + $0[1] },
                           spec.fields.map { $0.name + ":" + $0.type.rawValue }, name)
            XCTAssertEqual(row["signatures"] as? [String], spec.signatures, name)
            XCTAssertEqual((row["extras"] as! [[String]]).map { $0[0] + ":" + $0[1] },
                           spec.extras.map { $0.name + ":" + $0.type.rawValue }, name)
        }
    }

    func testObjects() throws {
        for row in try vectors()["objects"] as! [[String: Any]] {
            let name = row["name"] as! String
            let data = Bytes((row["json"] as! String).utf8)
            let object = try ControlObject.parse(data)
            XCTAssertEqual(object.kind, row["kind"] as? String, name)
            XCTAssertEqual(try object.signedInput().hex, row["input"] as? String, name)
            XCTAssertEqual(try object.id().hex, row["id"] as? String, name)
            let signers = row["signers"] as! [String: String]
            XCTAssertEqual(Set(object.signatures.keys), Set(signers.keys), name)
            for (sig, key) in signers {
                XCTAssertTrue(object.verify(sig, publicKey: hex(key)), "\(name) \(sig)")
                var tampered = object
                tampered.signatures[sig]![0] ^= 1
                XCTAssertFalse(tampered.verify(sig, publicKey: hex(key)), "\(name) \(sig) tampered")
            }
            XCTAssertEqual(try object.json(), data, name)
        }
    }

    func testRejects() throws {
        for row in try vectors()["rejects"] as! [[String: Any]] {
            XCTAssertThrowsError(try ControlObject.parse(Bytes((row["json"] as! String).utf8)), row["why"] as! String)
        }
    }

    func testDerivationsAndBoxes() throws {
        let v = try vectors()
        let m = (v["material"] as! [String: String]).mapValues { Bytes(hex: $0)! }
        let d = v["derived"] as! [String: String]
        let manager = try DeviceKeys.manager(seed: m["management_seed"]!)
        let hub = Bytes(manager.signing.publicKey.rawRepresentation)
        XCTAssertEqual(Bytes(try Control.inviteKey(secret: m["invite_secret"]!, hub: hub, invite: m["invite"]!)
            .publicKey.rawRepresentation).hex, d["invite_key"])
        XCTAssertEqual(Bytes(try Control.pairingInviteKey(link: m["link"]!, hub: hub, token: m["token"]!)
            .publicKey.rawRepresentation).hex, d["pairing_invite_key"])
        XCTAssertEqual(Control.pairingKey(link: m["link"]!, hub: hub, token: m["token"]!).hex, d["pairing_key"])
        XCTAssertEqual(Control.pairingTag(link: m["link"]!, hub: hub, token: m["token"]!), d["pairing_tag"])
        XCTAssertEqual(Control.directoryScope(hub: hub).hex, d["directory_scope"])
        XCTAssertEqual(Bytes(manager.agreement.publicKey.rawRepresentation).hex, d["manager_agreement"])
        let package = hex(d["package"])
        XCTAssertEqual(try Control.openPackage(link: m["link"]!, gate: m["gate_secret"]!, hub: hub, token: m["token"]!,
                                               package: package).hex, d["package_plaintext"])
        XCTAssertThrowsError(try Control.openPackage(link: m["link"]!, gate: Bytes(repeating: 0, count: 32), hub: hub,
                                                     token: m["token"]!, package: package))
        let alice = hex((v["objects"] as! [[String: Any]]).first { $0["name"] as? String == "alice registration" }!["id"])
        XCTAssertEqual(try Control.openGate(manager.agreement, hub: hub, token: m["token"]!, participant: alice,
                                            gate: hex(d["gate"])), m["gate_secret"]!)
        XCTAssertEqual(try Control.openHomeRecovery(manager.agreement, hub: hub, epoch: m["home_epoch"]!,
                                                    box: hex(d["home_recovery"])), m["creator_home_key"]!)
    }

    func testSigningAndFreshBoxes() throws {
        let manager = try DeviceKeys.manager(seed: Bytes(repeating: 7, count: 32))
        let hub = Bytes(manager.signing.publicKey.rawRepresentation)
        var revocation = ControlObject(kind: "revocation", fields: ["hub": .id(hub), "genesis": .id(Bytes(repeating: 1, count: 32)),
            "target": .id(Bytes(repeating: 2, count: 32)), "retained": .set([])])
        try revocation.sign("sig", with: manager.signing)
        XCTAssertTrue(revocation.verify("sig", publicKey: hub))
        XCTAssertEqual(try ControlObject.parse(try revocation.json()), revocation)
        XCTAssertThrowsError(try revocation.sign("approver", with: manager.signing))
        let token = Bytes(repeating: 3, count: 16), participant = Bytes(repeating: 4, count: 32)
        let gate = try Control.sealGate(managerAgreement: Bytes(manager.agreement.publicKey.rawRepresentation), hub: hub,
                                        token: token, participant: participant, secret: Bytes(repeating: 5, count: 32))
        XCTAssertEqual(try Control.openGate(manager.agreement, hub: hub, token: token, participant: participant, gate: gate),
                       Bytes(repeating: 5, count: 32))
        let package = try Control.sealPackage(link: Bytes(repeating: 6, count: 32), gate: Bytes(repeating: 5, count: 32),
                                              hub: hub, token: token, plaintext: Bytes("{}".utf8))
        XCTAssertEqual(try Control.openPackage(link: Bytes(repeating: 6, count: 32), gate: Bytes(repeating: 5, count: 32),
                                               hub: hub, token: token, package: package), Bytes("{}".utf8))
    }
}
