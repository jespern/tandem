import CryptoKit

/// Keys belong to an admitted device, not to a participant-wide secret.
public struct DeviceKeys: Sendable {
    public let signing: Curve25519.Signing.PrivateKey
    public let agreement: Curve25519.KeyAgreement.PrivateKey
    public init() { signing = .init(); agreement = .init() }
    public init(signingSeed: Bytes, agreementPrivate: Bytes) throws {
        signing = try .init(rawRepresentation: signingSeed)
        agreement = try .init(rawRepresentation: agreementPrivate)
    }
    public static func manager(seed: Bytes) throws -> DeviceKeys {
        guard seed.count == 32 else { throw TandemError.invalid("management seed must be 32 bytes") }
        return try DeviceKeys(signingSeed: KDF(seed, info: "tandem/v2/manager-sign"),
                              agreementPrivate: KDF(seed, info: "tandem/v2/manager-agree"))
    }
}

public enum Tandem {
    public static func homeScope(hub: Bytes, uid: Bytes) -> Bytes { Array(H(Bytes("tandem/v2/home".utf8), hub, uid).prefix(16)) }
    public static func namedID(_ name: Bytes) -> Bytes { Array(H(Bytes("tandem/v2/named".utf8), name).prefix(16)) }
    public static func keyID(hub: Bytes, agreement: Bytes) -> Bytes { H(Bytes("tandem/v2/kid".utf8), hub, agreement) }
    public static func commit(_ key: Bytes) -> Bytes { H(Bytes("tandem/v2/commit".utf8), key) }
    public static func collectionTag(key: Bytes, hub: Bytes, epoch: Bytes) -> String {
        B32.encode(KDF(key, salt: hub + epoch, info: "tandem/v2/collection-tag", count: 20))
    }
    public static func homeTag(key: Bytes, hub: Bytes, uid: Bytes, epoch: Bytes) -> String {
        B32.encode(KDF(key, salt: hub + uid + epoch, info: "tandem/v2/home-tag", count: 20))
    }
    public static func inboxTag(uid: Bytes, hub: Bytes) -> String { B32.encode(KDF(uid, salt: hub, info: "tandem/v2/inbox-tag", count: 20)) }
    public static func directoryKey(_ key: Bytes, hub: Bytes) -> Bytes { KDF(key, salt: hub, info: "tandem/v2/directory-key") }
    public static func directoryTag(_ key: Bytes, hub: Bytes) -> String { B32.encode(KDF(key, salt: hub, info: "tandem/v2/directory-tag", count: 20)) }
}
