// The one reading of protocol JSON (§10.5).
//
// Foundation's JSONSerialization and JSONDecoder keep the first of two
// repeated names and compare names as Strings, which fold canonically
// equivalent spellings into one. Protocol JSON is therefore read with this
// parser: names and strings stay UTF-8 bytes, repeated names are rejected,
// numbers must be plain integers within 2^53.

public indirect enum JSON: Equatable, Sendable {
    case null
    case bool(Bool)
    case int(Int64)
    case string(Bytes)
    case array([JSON])
    case object([(name: Bytes, value: JSON)])

    public static func == (a: JSON, b: JSON) -> Bool {
        switch (a, b) {
        case (.null, .null): true
        case let (.bool(x), .bool(y)): x == y
        case let (.int(x), .int(y)): x == y
        case let (.string(x), .string(y)): x == y
        case let (.array(x), .array(y)): x == y
        case let (.object(x), .object(y)):
            x.count == y.count && zip(x, y).allSatisfy { $0.name == $1.name && $0.value == $1.value }
        default: false
        }
    }

    /// An object's member, by name compared byte for byte.
    public subscript(name: String) -> JSON? {
        guard case let .object(members) = self else { return nil }
        let key = Bytes(name.utf8)
        return members.first { $0.name == key }?.value
    }

    public static func parse(_ data: Bytes) throws -> JSON {
        guard validUTF8(data) else { throw TandemError.invalid("json: not valid UTF-8") }
        if data.starts(with: [0xEF, 0xBB, 0xBF]) { throw TandemError.invalid("json: byte-order mark") }
        var p = Parser(data: data)
        p.space()
        let v = try p.value(depth: 0)
        p.space()
        guard p.i == data.count else { throw TandemError.invalid("json: trailing data") }
        return v
    }
}

/// Strict UTF-8: no overlong forms, no surrogates, nothing above U+10FFFF.
func validUTF8(_ b: Bytes) -> Bool {
    var i = 0
    while i < b.count {
        let c = b[i]
        if c < 0x80 { i += 1; continue }
        let n: Int
        var lo: UInt8 = 0x80, hi: UInt8 = 0xBF
        switch c {
        case 0xC2...0xDF: n = 1
        case 0xE0: n = 2; lo = 0xA0
        case 0xE1...0xEC, 0xEE...0xEF: n = 2
        case 0xED: n = 2; hi = 0x9F
        case 0xF0: n = 3; lo = 0x90
        case 0xF1...0xF3: n = 3
        case 0xF4: n = 3; hi = 0x8F
        default: return false
        }
        guard i + n < b.count, (lo...hi).contains(b[i + 1]) else { return false }
        for k in stride(from: 2, through: n, by: 1) where !(0x80...0xBF).contains(b[i + k]) { return false }
        i += n + 1
    }
    return true
}

private let maxInt: Int64 = 1 << 53
private let maxDepth = 64

private struct Parser {
    let data: Bytes
    var i = 0

    init(data: Bytes) { self.data = data }

    func fail(_ msg: String) -> TandemError { .invalid("json: \(msg) at byte \(i)") }

    mutating func space() {
        while i < data.count, [0x20, 0x09, 0x0A, 0x0D].contains(data[i]) { i += 1 }
    }

    mutating func literal(_ s: String) -> Bool {
        let b = Bytes(s.utf8)
        guard data.count - i >= b.count, Array(data[i..<i + b.count]) == b else { return false }
        i += b.count
        return true
    }

    mutating func value(depth: Int) throws -> JSON {
        guard depth <= maxDepth else { throw fail("nested too deeply") }
        guard i < data.count else { throw fail("unexpected end") }
        switch data[i] {
        case UInt8(ascii: "{"): return try object(depth: depth)
        case UInt8(ascii: "["):
            i += 1
            var items: [JSON] = []
            space()
            if literal("]") { return .array(items) }
            while true {
                space()
                items.append(try value(depth: depth + 1))
                space()
                if literal("]") { return .array(items) }
                guard literal(",") else { throw fail("expected , or ]") }
            }
        case UInt8(ascii: "\""): return .string(try string())
        case UInt8(ascii: "-"), UInt8(ascii: "0")...UInt8(ascii: "9"): return try number()
        default:
            if literal("true") { return .bool(true) }
            if literal("false") { return .bool(false) }
            if literal("null") { return .null }
            throw fail("unexpected character")
        }
    }

    mutating func object(depth: Int) throws -> JSON {
        i += 1
        var members: [(name: Bytes, value: JSON)] = []
        var seen = Set<Bytes>()
        space()
        if literal("}") { return .object(members) }
        while true {
            space()
            guard i < data.count, data[i] == UInt8(ascii: "\"") else { throw fail("expected name") }
            let name = try string()
            guard seen.insert(name).inserted else { throw fail("repeated name") }
            space()
            guard literal(":") else { throw fail("expected :") }
            space()
            members.append((name, try value(depth: depth + 1)))
            space()
            if literal("}") { return .object(members) }
            guard literal(",") else { throw fail("expected , or }") }
        }
    }

    mutating func number() throws -> JSON {
        let start = i
        if data[i] == UInt8(ascii: "-") { i += 1 }
        let digits = i
        while i < data.count, (UInt8(ascii: "0")...UInt8(ascii: "9")).contains(data[i]) { i += 1 }
        guard i > digits, !(data[digits] == UInt8(ascii: "0") && i - digits > 1) else { throw fail("malformed number") }
        if i < data.count, [UInt8(ascii: "."), UInt8(ascii: "e"), UInt8(ascii: "E")].contains(data[i]) {
            throw fail("number is not a plain integer")
        }
        guard i - digits <= 16, let n = Int64(String(decoding: data[start..<i], as: UTF8.self)),
              n <= maxInt, n >= -maxInt
        else { throw fail("integer beyond 2^53") }
        return .int(n)
    }

    mutating func string() throws -> Bytes {
        i += 1
        var out = Bytes()
        while true {
            guard i < data.count else { throw fail("unterminated string") }
            let c = data[i]
            if c == UInt8(ascii: "\"") { i += 1; return out }
            if c < 0x20 { throw fail("control character in string") }
            if c != UInt8(ascii: "\\") { out.append(c); i += 1; continue }
            i += 1
            guard i < data.count else { throw fail("unterminated escape") }
            let e = data[i]
            i += 1
            switch e {
            case UInt8(ascii: "\""), UInt8(ascii: "\\"), UInt8(ascii: "/"): out.append(e)
            case UInt8(ascii: "b"): out.append(0x08)
            case UInt8(ascii: "f"): out.append(0x0C)
            case UInt8(ascii: "n"): out.append(0x0A)
            case UInt8(ascii: "r"): out.append(0x0D)
            case UInt8(ascii: "t"): out.append(0x09)
            case UInt8(ascii: "u"):
                var r = try hex4()
                if (0xD800...0xDFFF).contains(r) {
                    guard r < 0xDC00, literal("\\u") else { throw fail("unpaired surrogate") }
                    let lo = try hex4()
                    guard (0xDC00...0xDFFF).contains(lo) else { throw fail("unpaired surrogate") }
                    r = 0x10000 + ((r - 0xD800) << 10) + (lo - 0xDC00)
                }
                guard let scalar = Unicode.Scalar(r) else { throw fail("bad scalar") }
                out.append(contentsOf: Bytes(String(Character(scalar)).utf8))
            default: throw fail("bad escape")
            }
        }
    }

    mutating func hex4() throws -> UInt32 {
        guard data.count - i >= 4, let n = UInt32(String(decoding: data[i..<i + 4], as: UTF8.self), radix: 16),
              data[i..<i + 4].allSatisfy({ $0 != UInt8(ascii: "+") && $0 != UInt8(ascii: "-") })
        else { throw fail("bad \\u escape") }
        i += 4
        return n
    }
}

// Typed accessors for payload readers. Unknown members are ignored (§10.5).
extension JSON {
    public func bytes(_ name: String, count: Int? = nil) throws -> Bytes {
        guard case let .string(s)? = self[name], let b = B64u.decode(s), count == nil || b.count == count
        else { throw TandemError.invalid("\(name): missing, not b64u, or the wrong length") }
        return b
    }

    public func text(_ name: String) throws -> Bytes {
        guard case let .string(s)? = self[name] else { throw TandemError.invalid("\(name): missing or not a string") }
        return s
    }

    public func int(_ name: String) throws -> Int64 {
        guard case let .int(n)? = self[name] else { throw TandemError.invalid("\(name): missing or not an integer") }
        return n
    }
}
