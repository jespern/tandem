import XCTest
import CryptoKit
import Foundation
@testable import Tandem

final class CapsuleTests: XCTestCase {
    /// For everything but the vector; never on the wire.
    let fast = Argon2Profile(memoryKiB: 64, iterations: 1, lanes: 1)
    let password = "correct horse battery staple"
    var ms = Bytes(), hub = Bytes(), capsule = Bytes()

    override func setUpWithError() throws {
        ms = SymmetricKey(size: .bits256).withUnsafeBytes { Bytes($0) }
        hub = try Self.hub(ms)
        capsule = try Control.sealCapsule(password: password, hub: hub, ms: ms, profile: fast)
    }
    static func hub(_ ms: Bytes) throws -> Bytes { Bytes(try DeviceKeys.manager(seed: ms).signing.publicKey.rawRepresentation) }

    func fixture() throws -> [String: Any] {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let url = root.appendingPathComponent("conformance/fixtures/tandem-v2-capsule.json")
        return try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as! [String: Any]
    }
    func hex(_ value: Any?) -> Bytes { Bytes(hex: value as! String)! }

    func testTheVectorAtTheRealProfile() throws {
        // Allocates 1 GiB and takes seconds in release, minutes in debug.
        try XCTSkipIf(ProcessInfo.processInfo.environment["TANDEM_SKIP_ARGON2_GIB"] == "1", "TANDEM_SKIP_ARGON2_GIB=1")
        let v = try fixture(), profile = v["profile"] as! [String: Any]
        XCTAssertEqual(profile["alg"] as? String, "argon2id")
        XCTAssertEqual(profile["version"] as? Int, 0x13)
        XCTAssertEqual(Argon2Profile(memoryKiB: profile["m_kib"] as! Int, iterations: profile["t"] as! Int,
                                     lanes: profile["p"] as! Int), .capsule)
        let start = ContinuousClock.now
        let ms = try Control.openCapsule(password: v["password"] as! String, hub: hex(v["hub_id"]), capsule: hex(v["capsule"]))
        print("Argon2id at the capsule profile: \(ContinuousClock.now - start)")
        XCTAssertEqual(ms, hex(v["ms"]))
    }

    func testTheVectorFromCK() throws {
        let v = try fixture()
        XCTAssertEqual(try Control.sealCapsule(ck: hex(v["ck"]), hub: hex(v["hub_id"]), ms: hex(v["ms"]), salt: hex(v["salt"]),
                                               nonce: hex(v["nonce"])), hex(v["capsule"]))
        XCTAssertEqual(try Control.openCapsule(ck: hex(v["ck"]), hub: hex(v["hub_id"]), capsule: hex(v["capsule"])), hex(v["ms"]))
        XCTAssertEqual(try Self.hub(hex(v["ms"])), hex(v["hub_id"]))
    }

    func testRoundTripAndNFC() throws {
        XCTAssertEqual(capsule.count, Control.capsuleLength)
        XCTAssertEqual(try Control.openCapsule(password: password, hub: hub, capsule: capsule, profile: fast), ms)
        let other = try Control.sealCapsule(password: password, hub: hub, ms: ms, profile: fast)
        XCTAssertNotEqual(other.prefix(28), capsule.prefix(28))                    // fresh salt and nonce
        let composed = "kålrabi og æbler", decomposed = composed.decomposedStringWithCanonicalMapping
        XCTAssertNotEqual(Bytes(composed.utf8), Bytes(decomposed.utf8))
        let sealed = try Control.sealCapsule(password: composed, hub: hub, ms: ms, profile: fast)
        XCTAssertEqual(try Control.openCapsule(password: decomposed, hub: hub, capsule: sealed, profile: fast), ms)
    }

    func testAWrongPasswordFails() {
        for wrong in ["correct horse battery stapl", "Correct horse battery staple", " correct horse battery staple", "short", ""] {
            XCTAssertThrowsError(try Control.openCapsule(password: wrong, hub: hub, capsule: capsule, profile: fast)) {
                XCTAssertEqual($0 as? TandemError, .invalid("capsule: the hub password does not open this capsule"))
            }
        }
    }

    func testAnotherProfileDoesNotOpen() {
        XCTAssertThrowsError(try Control.openCapsule(password: password, hub: hub, capsule: capsule,
                                                     profile: Argon2Profile(memoryKiB: 64, iterations: 2, lanes: 1)))
    }

    func testTheCapsuleIsBoundToItsHubSaltAndSeed() throws {
        let otherHub = try Self.hub(SymmetricKey(size: .bits256).withUnsafeBytes { Bytes($0) })
        XCTAssertThrowsError(try Control.openCapsule(password: password, hub: otherHub, capsule: capsule, profile: fast))
        for i in [0, 15, 16, 27, 28, capsule.count - 1] {
            var bad = capsule; bad[i] ^= 1
            XCTAssertThrowsError(try Control.openCapsule(password: password, hub: hub, capsule: bad, profile: fast))
        }
        for length in [0, 75, 77] {
            XCTAssertThrowsError(try Control.openCapsule(password: password, hub: hub,
                capsule: Array((capsule + [0]).prefix(length)), profile: fast))
        }
        // A capsule that opens but holds a seed for another hub; sealing refuses it, so build it from CK.
        let salt = Bytes(repeating: 7, count: 16), nonce = Bytes(repeating: 9, count: 12)
        let ck = try argon2id(password: Bytes(password.utf8), salt: salt, profile: fast)
        let foreign = try Control.sealCapsule(ck: ck, hub: hub, ms: Bytes(repeating: 1, count: 32), salt: salt, nonce: nonce)
        XCTAssertThrowsError(try Control.openCapsule(password: password, hub: hub, capsule: foreign, profile: fast)) {
            XCTAssertEqual($0 as? TandemError, .invalid("capsule: the capsule holds another hub's key"))
        }
        XCTAssertThrowsError(try Control.sealCapsule(password: password, hub: otherHub, ms: ms, profile: fast))
    }

    func testPasswordLengthWhenChosen() throws {
        func seal(_ p: String) throws { _ = try Control.sealCapsule(password: p, hub: hub, ms: ms, profile: fast) }
        XCTAssertThrowsError(try seal("hunter2"))
        let coder = String(repeating: "\u{1F469}\u{200D}\u{1F4BB}", count: 4)    // 4 Characters, 12 code points
        XCTAssertEqual(coder.count, 4)
        try seal(coder)
        XCTAssertThrowsError(try seal(String(repeating: "e\u{301}", count: 6)))  // 12 code points typed, 6 after NFC
        try seal(String(repeating: "a", count: 1024))
        try seal(String(repeating: "\u{e6}", count: 512))
        try seal(String(repeating: "e\u{301}", count: 512))                      // 1,536 bytes typed, 1,024 after NFC
        for long in [String(repeating: "a", count: 1025), String(repeating: "\u{e6}", count: 512) + "a",
                     String(repeating: "e\u{301}", count: 1000)] {
            XCTAssertThrowsError(try seal(long))
            XCTAssertThrowsError(try Control.openCapsule(password: long, hub: hub, capsule: capsule, profile: fast)) {
                XCTAssertEqual($0 as? TandemError, .invalid("capsule: a hub password has at most 1,024 bytes"))
            }
        }
    }

    func testCancellation() async throws {
        let profile = fast
        let task = Task {
            withUnsafeCurrentTask { $0?.cancel() }
            return try argon2id(password: [], salt: Bytes(repeating: 0, count: 16), profile: profile)
        }
        do { _ = try await task.value; XCTFail("not cancelled") } catch is CancellationError {}
    }
}
