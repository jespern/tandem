import CryptoKit
import Foundation

// Notation of §5.2. Bytes are [UInt8] throughout: text the protocol compares
// is compared as UTF-8 bytes, never as String, whose == treats canonically
// equivalent spellings as equal (§10.5).

public typealias Bytes = [UInt8]

public enum TandemError: Error, Equatable {
    case invalid(String)
}

extension Array where Element == UInt8 {
    /// Bytes from lowercase or uppercase hex; nil if malformed.
    public init?(hex: String) {
        let chars = Array(hex.utf8)
        guard chars.count % 2 == 0 else { return nil }
        var out = Bytes()
        out.reserveCapacity(chars.count / 2)
        var i = 0
        while i < chars.count {
            guard let hi = hexValue(chars[i]), let lo = hexValue(chars[i + 1]) else { return nil }
            out.append(hi << 4 | lo)
            i += 2
        }
        self = out
    }

    public var hex: String {
        let digits = Array("0123456789abcdef".utf8)
        var out = Bytes()
        out.reserveCapacity(count * 2)
        for b in self {
            out.append(digits[Int(b >> 4)])
            out.append(digits[Int(b & 0xf)])
        }
        return String(decoding: out, as: UTF8.self)
    }
}

private func hexValue(_ c: UInt8) -> UInt8? {
    switch c {
    case UInt8(ascii: "0")...UInt8(ascii: "9"): c - UInt8(ascii: "0")
    case UInt8(ascii: "a")...UInt8(ascii: "f"): c - UInt8(ascii: "a") + 10
    case UInt8(ascii: "A")...UInt8(ascii: "F"): c - UInt8(ascii: "A") + 10
    default: nil
    }
}

/// SHA-256 of the concatenated parts.
public func H(_ parts: Bytes...) -> Bytes {
    var h = SHA256()
    for p in parts { h.update(data: p) }
    return Bytes(h.finalize())
}

/// HKDF-SHA-256. An empty salt is HKDF's default salt.
public func KDF(_ ikm: Bytes, salt: Bytes = [], info: String, count: Int = 32) -> Bytes {
    let key = HKDF<SHA256>.deriveKey(
        inputKeyMaterial: SymmetricKey(data: ikm), salt: salt, info: Bytes(info.utf8), outputByteCount: count)
    return key.withUnsafeBytes { Bytes($0) }
}

extension Array where Element == UInt8 {
    mutating func u8(_ v: UInt8) { append(v) }
    mutating func u16(_ v: UInt16) { append(contentsOf: [UInt8(v >> 8), UInt8(v & 0xff)]) }
    mutating func u32(_ v: UInt32) { for s in stride(from: 24, through: 0, by: -8) { append(UInt8(v >> UInt32(s) & 0xff)) } }
    mutating func u64(_ v: UInt64) { for s in stride(from: 56, through: 0, by: -8) { append(UInt8(v >> UInt64(s) & 0xff)) } }
    /// lp(x) = u32(len(x)) || x
    mutating func lp(_ x: Bytes) { u32(UInt32(x.count)); append(contentsOf: x) }
}

/// base64url without padding, in its one spelling (§5.2).
public enum B64u {
    static let alphabet = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_".utf8)
    static let values: [Int8] = {
        var t = [Int8](repeating: -1, count: 256)
        for (i, c) in alphabet.enumerated() { t[Int(c)] = Int8(i) }
        return t
    }()

    public static func encode(_ b: Bytes) -> String {
        var out = Bytes()
        var i = 0
        while i + 3 <= b.count {
            let n = UInt32(b[i]) << 16 | UInt32(b[i + 1]) << 8 | UInt32(b[i + 2])
            for s in [18, 12, 6, 0] { out.append(alphabet[Int(n >> UInt32(s) & 63)]) }
            i += 3
        }
        let rest = b.count - i
        if rest == 1 {
            let n = UInt32(b[i]) << 16
            for s in [18, 12] { out.append(alphabet[Int(n >> UInt32(s) & 63)]) }
        } else if rest == 2 {
            let n = UInt32(b[i]) << 16 | UInt32(b[i + 1]) << 8
            for s in [18, 12, 6] { out.append(alphabet[Int(n >> UInt32(s) & 63)]) }
        }
        return String(decoding: out, as: UTF8.self)
    }

    /// Decodes only the one spelling: no padding, nothing outside the
    /// alphabet, unused bits zero. Foundation's decoder is lenient on all three.
    public static func decode(_ s: Bytes) -> Bytes? {
        guard s.count % 4 != 1 else { return nil }
        var out = Bytes()
        var acc: UInt32 = 0
        var bits = 0
        for c in s {
            let v = values[Int(c)]
            guard v >= 0 else { return nil }
            acc = acc << 6 | UInt32(v)
            bits += 6
            if bits >= 8 {
                bits -= 8
                out.append(UInt8(acc >> UInt32(bits) & 0xff))
            }
        }
        guard acc & ((1 << UInt32(bits)) - 1) == 0 else { return nil }
        return out
    }

    public static func decode(_ s: String) -> Bytes? { decode(Bytes(s.utf8)) }
}

/// RFC 4648 base32, lowercase, no padding.
public enum B32 {
    static let alphabet = Array("abcdefghijklmnopqrstuvwxyz234567".utf8)

    public static func encode(_ b: Bytes) -> String {
        var out = Bytes()
        var acc: UInt64 = 0
        var bits = 0
        for byte in b {
            acc = acc << 8 | UInt64(byte)
            bits += 8
            while bits >= 5 {
                bits -= 5
                out.append(alphabet[Int(acc >> UInt64(bits) & 31)])
            }
        }
        if bits > 0 { out.append(alphabet[Int(acc << UInt64(5 - bits) & 31)]) }
        return String(decoding: out, as: UTF8.self)
    }

    /// Decodes lowercase base32 in its one spelling.
    public static func decode(_ s: String) -> Bytes? {
        var out = Bytes()
        var acc: UInt64 = 0
        var bits = 0
        for c in s.utf8 {
            guard let v = alphabet.firstIndex(of: c) else { return nil }
            acc = acc << 5 | UInt64(v)
            bits += 5
            if bits >= 8 {
                bits -= 8
                out.append(UInt8(acc >> UInt64(bits) & 0xff))
            }
        }
        guard encode(out) == s else { return nil }
        return out
    }
}

/// s split into space-separated groups of n characters.
func groups(_ s: String, _ n: Int) -> String {
    let chars = Array(s)
    return stride(from: 0, to: chars.count, by: n)
        .map { String(chars[$0..<Swift.min($0 + n, chars.count)]) }
        .joined(separator: " ")
}
