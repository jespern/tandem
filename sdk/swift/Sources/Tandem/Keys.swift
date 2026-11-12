import CryptoKit

/// The keys derived from a user secret (§5.3).
public struct User: Sendable {
    public let secret: Bytes
    public let signing: Curve25519.Signing.PrivateKey
    public let uid: Bytes
    public let agreement: Curve25519.KeyAgreement.PrivateKey
    public let uka: Bytes
    public let home: Bytes

    public init(secret: Bytes) throws {
        guard secret.count == 32 else { throw TandemError.invalid("user secret must be 32 bytes") }
        self.secret = secret
        signing = try Curve25519.Signing.PrivateKey(rawRepresentation: KDF(secret, info: "tandem/v1/user-sign"))
        uid = Bytes(signing.publicKey.rawRepresentation)
        agreement = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: KDF(secret, info: "tandem/v1/user-agree"))
        uka = Bytes(agreement.publicKey.rawRepresentation)
        home = KDF(secret, info: "tandem/v1/home")
    }

    public func sign(_ message: Bytes) throws -> Bytes { Bytes(try signing.signature(for: message)) }
}

public enum Tandem {
    /// The user ID as shown: the first 20 bytes in b32, in groups of 4.
    public static func displayUID(_ uid: Bytes) -> String { groups(B32.encode(Array(uid.prefix(20))), 4) }

    public static func homeScope(_ uid: Bytes) -> Bytes { Array(H(Bytes("tandem/v1/home".utf8), uid).prefix(16)) }

    /// The record ID of a named record (§7.3). The name's UTF-8 bytes, as given.
    public static func namedID(_ name: Bytes) -> Bytes { Array(H(Bytes("tandem/v1/named".utf8), name).prefix(16)) }

    public static func keyID(_ uka: Bytes) -> Bytes { H(Bytes("tandem/v1/kid".utf8), uka) }

    public static func commit(_ k: Bytes) -> Bytes { H(Bytes("tandem/v1/commit".utf8), k) }

    // Channel tags (§5.6).
    public static func collectionTag(key: Bytes, epoch: Bytes) -> String {
        B32.encode(KDF(key, salt: epoch, info: "tandem/v1/collection-tag", count: 20))
    }
    public static func homeTag(_ hk: Bytes) -> String { B32.encode(KDF(hk, info: "tandem/v1/tag", count: 20)) }
    public static func inboxTag(_ uid: Bytes) -> String { B32.encode(KDF(uid, info: "tandem/v1/inbox-tag", count: 20)) }
    public static func inviteKey(_ s: Bytes) -> Bytes { KDF(s, info: "tandem/v1/invite-key") }
    public static func inviteTag(_ s: Bytes) -> String { B32.encode(KDF(inviteKey(s), info: "tandem/v1/tag", count: 20)) }
    public static func inviteSigner(_ s: Bytes) throws -> Curve25519.Signing.PrivateKey {
        try Curve25519.Signing.PrivateKey(rawRepresentation: KDF(s, info: "tandem/v1/invite-sign"))
    }
    public static func pairKey(_ p: Bytes) -> Bytes { KDF(p, info: "tandem/v1/pair-key") }
    public static func pairTag(_ p: Bytes) -> String { B32.encode(KDF(p, info: "tandem/v1/tag", count: 20)) }

    /// The six digits both devices show while pairing (§6.3).
    public static func pairCode(p: Bytes, ek: Bytes, n: Bytes) -> String {
        let h = H(Bytes("tandem/v1/pair-code".utf8), p, ek, n)
        let v = (UInt32(h[0]) << 24 | UInt32(h[1]) << 16 | UInt32(h[2]) << 8 | UInt32(h[3])) % 1_000_000
        let digits = String(v)
        return String(repeating: "0", count: 6 - digits.count) + digits
    }

    /// b32(US || H(US)[0..3]) in 14 groups of 4 (§6.3).
    public static func backupKey(_ secret: Bytes) -> String { groups(B32.encode(secret + H(secret).prefix(3)), 4) }

    /// Reads a backup key as typed: spaces and case don't matter, the checksum does.
    public static func parseBackupKey(_ s: String) throws -> Bytes {
        let flat = String(s.unicodeScalars.filter { !" \t\r\n".unicodeScalars.contains($0) }).lowercased()
        guard let b = B32.decode(flat), b.count == 35 else { throw TandemError.invalid("not a backup key") }
        let secret = Array(b.prefix(32))
        guard Array(H(secret).prefix(3)) == Array(b.suffix(3)) else { throw TandemError.invalid("backup key has a typo") }
        return secret
    }
}
