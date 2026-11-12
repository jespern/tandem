import CryptoKit
import Foundation

/// Envelopes (§5.4).
public enum Envelope {
    public static let record: UInt8 = 1
    public static let inbox: UInt8 = 2
    public static let request: UInt8 = 3

    static let magic = Bytes("TNDM".utf8)
    static let suite = HPKE.Ciphersuite.Curve25519_SHA256_ChachaPoly

    /// "TNDM" || u8(1) || u8(kind) || tag, with the tag as its b32 text.
    public static func aad(kind: UInt8, tag: String) -> Bytes { magic + [1, kind] + Bytes(tag.utf8) }

    /// A symmetric envelope (kinds 1 and 3). salt nil draws 32 random bytes.
    public static func seal(key: Bytes, kind: UInt8, tag: String, plaintext: Bytes, salt: Bytes? = nil) throws -> Bytes {
        guard kind == record || kind == request else { throw TandemError.invalid("not a symmetric kind") }
        let salt = salt ?? Bytes(SymmetricKey(size: .bits256).withUnsafeBytes { Bytes($0) })
        guard salt.count == 32 else { throw TandemError.invalid("salt must be 32 bytes") }
        let box = try ChaChaPoly.seal(plaintext, using: SymmetricKey(data: KDF(key, salt: salt, info: "tandem/v1/msg")),
                                      nonce: ChaChaPoly.Nonce(data: Bytes(repeating: 0, count: 12)),
                                      authenticating: aad(kind: kind, tag: tag))
        return magic + [1, kind] + salt + Bytes(box.ciphertext) + Bytes(box.tag)
    }

    public static func open(key: Bytes, tag: String, envelope env: Bytes) throws -> (kind: UInt8, plaintext: Bytes) {
        guard env.count >= 38 + 16, Array(env[0..<4]) == magic, env[4] == 1, env[5] == record || env[5] == request
        else { throw TandemError.invalid("not an envelope") }
        let kind = env[5]
        let box = try ChaChaPoly.SealedBox(nonce: ChaChaPoly.Nonce(data: Bytes(repeating: 0, count: 12)),
                                           ciphertext: env[38..<(env.count - 16)], tag: env[(env.count - 16)...])
        let pt = try ChaChaPoly.open(box, using: SymmetricKey(data: KDF(key, salt: Array(env[6..<38]), info: "tandem/v1/msg")),
                                     authenticating: aad(kind: kind, tag: tag))
        return (kind, Bytes(pt))
    }

    // HPKE (§5.1), sealed bytes enc || ct (§5.4).

    static func hpkeSeal(to recipient: Bytes, info: String, aad: Bytes, plaintext: Bytes) throws -> Bytes {
        let key = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: recipient)
        var sender = try HPKE.Sender(recipientKey: key, ciphersuite: suite, info: Data(info.utf8))
        let ct = try sender.seal(plaintext, authenticating: aad)
        return Bytes(sender.encapsulatedKey) + Bytes(ct)
    }

    static func hpkeOpen(_ key: Curve25519.KeyAgreement.PrivateKey, info: String, aad: Bytes, sealed: Bytes) throws -> Bytes {
        guard sealed.count >= 32 + 16 else { throw TandemError.invalid("sealed bytes too short") }
        var recipient = try HPKE.Recipient(privateKey: key, ciphersuite: suite, info: Data(info.utf8),
                                           encapsulatedKey: Data(sealed[0..<32]))
        return Bytes(try recipient.open(sealed[32...], authenticating: aad))
    }

    /// A kind-2 envelope to the user with this ID and agreement key.
    public static func sealInbox(uid: Bytes, uka: Bytes, plaintext: Bytes) throws -> Bytes {
        let sealed = try hpkeSeal(to: uka, info: "tandem/v1/inbox", aad: aad(kind: inbox, tag: Tandem.inboxTag(uid)), plaintext: plaintext)
        return magic + [1, inbox] + Tandem.keyID(uka) + sealed
    }

    public static func openInbox(_ user: User, envelope env: Bytes) throws -> Bytes {
        guard env.count >= 38, Array(env[0..<4]) == magic, env[4] == 1, env[5] == inbox,
              Array(env[6..<38]) == Tandem.keyID(user.uka)
        else { throw TandemError.invalid("not an inbox envelope for this user") }
        return try hpkeOpen(user.agreement, info: "tandem/v1/inbox", aad: aad(kind: inbox, tag: Tandem.inboxTag(user.uid)),
                            sealed: Array(env[38...]))
    }

    public static func pairAAD(uid: Bytes, ek: Bytes) -> Bytes { Bytes("TANDEM-PAIR-V1".utf8) + uid + ek }

    /// The pairing reply: the user secret sealed to the new device's one-time key (§6.3).
    public static func sealPairing(uid: Bytes, ek: Bytes, secret: Bytes) throws -> Bytes {
        try hpkeSeal(to: ek, info: "tandem/v1/pair", aad: pairAAD(uid: uid, ek: ek), plaintext: secret)
    }

    public static func openPairing(_ ephemeral: Curve25519.KeyAgreement.PrivateKey, uid: Bytes, sealed: Bytes) throws -> Bytes {
        try hpkeOpen(ephemeral, info: "tandem/v1/pair",
                     aad: pairAAD(uid: uid, ek: Bytes(ephemeral.publicKey.rawRepresentation)), sealed: sealed)
    }
}
