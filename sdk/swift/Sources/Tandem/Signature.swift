import CryptoKit

/// Strict Ed25519 verification (§5.5): S below the group order, canonical A
/// and R, A not of small order. CryptoKit's own checks aren't documented, so
/// each is made here before CryptoKit checks the equation; the vectors in
/// vectors/v2/ed25519.json pin the combined behaviour.
public enum Ed25519 {
    /// Canonical encodings of the eight points of order dividing 8 (complete
    /// once the encoding is canonical; derived in the Go SDK's sig_test.go).
    static let smallOrder: [Bytes] = [
        "0100000000000000000000000000000000000000000000000000000000000000",
        "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
        "0000000000000000000000000000000000000000000000000000000000000000",
        "0000000000000000000000000000000000000000000000000000000000000080",
        "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
        "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
        "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
        "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
    ].map { Bytes(hex: $0)! }

    /// p = 2^255 - 19 and the group order L, little-endian.
    static let p = Bytes(hex: "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")!
    static let order = Bytes(hex: "edd3f55c1a631258d69cf7a2def9de1400000000000000000000000000000010")!

    /// a < b for little-endian numbers of equal length.
    static func less(_ a: ArraySlice<UInt8>, _ b: Bytes) -> Bool {
        for (x, y) in zip(a.reversed(), b.reversed()) where x != y { return x < y }
        return false
    }

    /// The one encoding of whatever point it names: y < p, and no sign bit
    /// when x = 0 (y = 1 or y = p - 1).
    static func canonicalPoint(_ enc: ArraySlice<UInt8>) -> Bool {
        var y = Bytes(enc)
        let sign = y[31] >> 7
        y[31] &= 0x7f
        guard less(y[...], p) else { return false }
        if sign == 1 {
            var pm1 = p
            pm1[0] = 0xec
            if y == smallOrder[0] || y == pm1 { return false }
        }
        return true
    }

    static func validPublicKeyEncoding(_ pub: Bytes) -> Bool {
        pub.count == 32 && canonicalPoint(pub[...]) && !smallOrder.contains(pub)
    }

    public static func verify(signature sig: Bytes, message: Bytes, publicKey pub: Bytes) -> Bool {
        guard sig.count == 64, validPublicKeyEncoding(pub), canonicalPoint(sig[0..<32]), less(sig[32..<64], order),
              let key = try? Curve25519.Signing.PublicKey(rawRepresentation: pub)
        else { return false }
        return key.isValidSignature(sig, for: message)
    }
}
