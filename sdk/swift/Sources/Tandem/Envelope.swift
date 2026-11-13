import CryptoKit
import Foundation

public enum Envelope {
    public static let record: UInt8 = 1, inbox: UInt8 = 2, request: UInt8 = 3
    static let magic = Bytes("TNDM".utf8)
    static let suite = HPKE.Ciphersuite.Curve25519_SHA256_ChachaPoly
    public static func aad(kind: UInt8, hub: Bytes, tag: String) -> Bytes { magic + [2, kind] + hub + Bytes(tag.utf8) }
    static func check(key: Bytes, hub: Bytes, tag: String) throws {
        guard key.count == 32, hub.count == 32, B32.decode(tag)?.count == 20 else { throw TandemError.invalid("invalid envelope context") }
    }
    /// Salt is always fresh. Retry the persisted envelope, rather than re-sealing.
    public static func seal(key: Bytes, hub: Bytes, kind: UInt8, tag: String, plaintext: Bytes) throws -> Bytes {
        try check(key: key, hub: hub, tag: tag)
        guard kind == record || kind == request else { throw TandemError.invalid("not a symmetric kind") }
        let salt = SymmetricKey(size: .bits256).withUnsafeBytes { Bytes($0) }
        let box = try ChaChaPoly.seal(plaintext, using: SymmetricKey(data: KDF(key, salt: salt, info: "tandem/v2/msg")),
            nonce: ChaChaPoly.Nonce(data: Bytes(repeating: 0, count: 12)), authenticating: aad(kind: kind, hub: hub, tag: tag))
        return magic + [2, kind] + salt + Bytes(box.ciphertext) + Bytes(box.tag)
    }
    public static func open(key: Bytes, hub: Bytes, tag: String, envelope env: Bytes) throws -> (kind: UInt8, plaintext: Bytes) {
        try check(key: key, hub: hub, tag: tag)
        guard env.count >= 54, Array(env.prefix(5)) == magic + [2], env[5] == record || env[5] == request
        else { throw TandemError.invalid("not a v2 envelope") }
        let box = try ChaChaPoly.SealedBox(nonce: ChaChaPoly.Nonce(data: Bytes(repeating: 0, count: 12)),
            ciphertext: env[38..<(env.count - 16)], tag: env.suffix(16))
        return (env[5], Bytes(try ChaChaPoly.open(box, using: SymmetricKey(data: KDF(key, salt: Array(env[6..<38]), info: "tandem/v2/msg")),
            authenticating: aad(kind: env[5], hub: hub, tag: tag))))
    }
    public static func sealHPKE(to publicKey: Bytes, info: String, aad: Bytes, plaintext: Bytes) throws -> Bytes {
        var sender = try HPKE.Sender(recipientKey: Curve25519.KeyAgreement.PublicKey(rawRepresentation: publicKey),
            ciphersuite: suite, info: Data(info.utf8))
        return Bytes(sender.encapsulatedKey) + Bytes(try sender.seal(plaintext, authenticating: aad))
    }
    public static func openHPKE(_ key: Curve25519.KeyAgreement.PrivateKey, info: String, aad: Bytes, sealed: Bytes) throws -> Bytes {
        guard sealed.count >= 48 else { throw TandemError.invalid("short HPKE input") }
        var recipient = try HPKE.Recipient(privateKey: key, ciphersuite: suite, info: Data(info.utf8), encapsulatedKey: Data(sealed.prefix(32)))
        return Bytes(try recipient.open(sealed.dropFirst(32), authenticating: aad))
    }
    public static func sealInbox(hub: Bytes, uid: Bytes, agreement: Bytes, plaintext: Bytes) throws -> Bytes {
        try check(key: agreement, hub: hub, tag: Tandem.inboxTag(uid: uid, hub: hub))
        guard uid.count == 32 else { throw TandemError.invalid("participant ID must be 32 bytes") }
        let sealed = try sealHPKE(to: agreement, info: "tandem/v2/inbox", aad: aad(kind: inbox, hub: hub, tag: Tandem.inboxTag(uid: uid, hub: hub)), plaintext: plaintext)
        return magic + [2, inbox] + Tandem.keyID(hub: hub, agreement: agreement) + sealed
    }
    public static func openInbox(hub: Bytes, uid: Bytes, key: Curve25519.KeyAgreement.PrivateKey, envelope env: Bytes) throws -> Bytes {
        guard hub.count == 32, uid.count == 32, env.count >= 86, Array(env.prefix(6)) == magic + [2, inbox],
            Array(env[6..<38]) == Tandem.keyID(hub: hub, agreement: Bytes(key.publicKey.rawRepresentation))
        else { throw TandemError.invalid("not an inbox envelope for this device") }
        return try openHPKE(key, info: "tandem/v2/inbox", aad: aad(kind: inbox, hub: hub, tag: Tandem.inboxTag(uid: uid, hub: hub)), sealed: Array(env.dropFirst(38)))
    }
}
