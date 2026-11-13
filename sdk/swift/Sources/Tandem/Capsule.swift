import CryptoKit
import Foundation

// The admin capsule of §6.7: salt || nonce || ChaCha20-Poly1305(KDF(CK, hubID, ...), nonce, MS, aad).

extension Control {
    public static let capsuleLength = 16 + 12 + 32 + 16

    /// Opens the capsule with a typed password and returns MS, checked to be this hub's seed.
    /// Takes seconds and 1 GiB; run it off the main thread. Throws CancellationError if cancelled.
    public static func openCapsule(password: String, hub: Bytes, capsule: Bytes) throws -> Bytes {
        try openCapsule(password: password, hub: hub, capsule: capsule, profile: .capsule)
    }

    /// Seals MS under a newly chosen password, with a fresh salt and nonce.
    public static func sealCapsule(password: String, hub: Bytes, ms: Bytes) throws -> Bytes {
        try sealCapsule(password: password, hub: hub, ms: ms, profile: .capsule)
    }

    /// NFC-normalized UTF-8, nothing trimmed. A chosen password has 12 code points and at most 1,024 bytes;
    /// unlocking checks neither and simply tries what was typed (§6.7).
    static func passwordBytes(_ password: String, choosing: Bool) throws -> Bytes {
        let nfc = password.precomposedStringWithCanonicalMapping
        let bytes = Bytes(nfc.utf8)
        guard !choosing || bytes.count <= 1024 else { throw refused("a hub password has at most 1,024 bytes") }
        // Code points, not Characters: four 👩‍💻 are 4 Characters and 12 code points.
        guard !choosing || nfc.unicodeScalars.count >= 12 else { throw refused("a hub password has at least 12 characters") }
        return bytes
    }

    static func capsuleAAD(hub: Bytes, salt: Bytes) -> Bytes { Bytes("TANDEM-ADMIN-CAPSULE-V2".utf8) + hub + salt }
    static func capsuleKey(ck: Bytes, hub: Bytes) -> SymmetricKey { SymmetricKey(data: KDF(ck, salt: hub, info: "tandem/v2/admin-capsule")) }

    static func checkSeed(_ ms: Bytes, hub: Bytes) throws {
        guard try Bytes(DeviceKeys.manager(seed: ms).signing.publicKey.rawRepresentation) == hub
        else { throw refused("the capsule holds another hub's key") }
    }

    /// `profile` is for tests only; the protocol accepts exactly `.capsule`.
    static func openCapsule(password: String, hub: Bytes, capsule: Bytes, profile: Argon2Profile) throws -> Bytes {
        guard hub.count == 32, capsule.count == capsuleLength else { throw refused("not an admin capsule") }
        let salt = Array(capsule.prefix(16))
        let ck = try argon2id(password: passwordBytes(password, choosing: false), salt: salt, profile: profile)
        return try openCapsule(ck: ck, hub: hub, capsule: capsule)
    }

    static func openCapsule(ck: Bytes, hub: Bytes, capsule: Bytes) throws -> Bytes {
        let salt = Array(capsule.prefix(16))
        let box = try ChaChaPoly.SealedBox(nonce: ChaChaPoly.Nonce(data: capsule[16..<28]),
                                           ciphertext: capsule[28..<60], tag: capsule[60...])
        let ms: Bytes
        do { ms = Bytes(try ChaChaPoly.open(box, using: capsuleKey(ck: ck, hub: hub), authenticating: capsuleAAD(hub: hub, salt: salt))) }
        catch { throw refused("the hub password does not open this capsule") }
        try checkSeed(ms, hub: hub)
        return ms
    }

    static func sealCapsule(password: String, hub: Bytes, ms: Bytes, profile: Argon2Profile) throws -> Bytes {
        let password = try passwordBytes(password, choosing: true)
        try checkSeed(ms, hub: hub)
        let salt = SymmetricKey(size: .bits128).withUnsafeBytes { Bytes($0) }
        let ck = try argon2id(password: password, salt: salt, profile: profile)
        return try sealCapsule(ck: ck, hub: hub, ms: ms, salt: salt, nonce: Bytes(ChaChaPoly.Nonce()))
    }

    static func sealCapsule(ck: Bytes, hub: Bytes, ms: Bytes, salt: Bytes, nonce: Bytes) throws -> Bytes {
        let box = try ChaChaPoly.seal(ms, using: capsuleKey(ck: ck, hub: hub), nonce: ChaChaPoly.Nonce(data: nonce),
                                      authenticating: capsuleAAD(hub: hub, salt: salt))
        return salt + nonce + Bytes(box.ciphertext) + Bytes(box.tag)
    }
}

private func refused(_ what: String) -> TandemError { .invalid("capsule: " + what) }
