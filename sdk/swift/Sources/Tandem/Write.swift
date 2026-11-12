/// Writes and their signatures (§5.5, §9.1).

public enum Op: UInt8, Sendable {
    case assign = 1, add, remove, delete
}

public struct Dot: Equatable, Sendable {
    public var device: Bytes  // 8
    public var millis: UInt64  // u48
    public var counter: UInt16

    public init(device: Bytes, millis: UInt64, counter: UInt16) {
        self.device = device
        self.millis = millis
        self.counter = counter
    }
}

/// At most one dot per device, sorted by device ID; at most 1,024 entries (§9.2).
public typealias VV = [Dot]

public let maxVector = 1024
let maxMillis: UInt64 = 1 << 48

func lexLess(_ a: Bytes, _ b: Bytes) -> Bool { a.lexicographicallyPrecedes(b) }

func checkVV(_ v: VV) throws {
    guard v.count <= maxVector else { throw TandemError.invalid("vector over 1,024 entries") }
    for (i, d) in v.enumerated() {
        guard d.device.count == 8, d.millis < maxMillis else { throw TandemError.invalid("bad dot") }
        if i > 0, !lexLess(v[i - 1].device, d.device) {
            throw TandemError.invalid("vector not sorted by device, or a device twice")
        }
    }
}

public struct Write: Sendable {
    public var scope: Bytes  // 16
    public var record: Bytes  // 16
    public var field: Bytes  // UTF-8, compared as bytes (§9.1)
    public var op: Op
    public var element: Bytes?  // add/remove only
    public var value: Bytes  // assign only
    public var author: Bytes  // 32
    public var dot: Dot
    public var ref: Bytes  // 32, zero if none
    public var context: VV
    public var base: VV
    public var sig: Bytes  // 64

    func check() throws {
        guard scope.count == 16, record.count == 16, author.count == 32, ref.count == 32, dot.device.count == 8
        else { throw TandemError.invalid("write has an ID of the wrong length") }
        guard (op == .add || op == .remove) == (element != nil) else { throw TandemError.invalid("element only on add and remove") }
        guard op == .assign || value.isEmpty else { throw TandemError.invalid("value only on assign") }
        guard op != .delete || field.isEmpty else { throw TandemError.invalid("a delete has an empty field name") }
        guard dot.millis < maxMillis else { throw TandemError.invalid("HLC millis beyond 48 bits") }
        try checkVV(context)
        try checkVV(base)
    }

    public func sigInput() throws -> Bytes {
        try check()
        var b = Bytes("TANDEM-WRITE-V1".utf8)
        b += scope
        b += record
        b.lp(field)
        b.u8(op.rawValue)
        b.lp(element ?? [])
        b += H(value)
        b += author
        b += dot.device
        b.u64(dot.millis)
        b.u16(dot.counter)
        b += ref
        for v in [context, base] {
            b.u16(UInt16(v.count))
            for d in v {
                b += d.device
                b.u64(d.millis)
                b.u16(d.counter)
            }
        }
        return b
    }

    public func id() throws -> Bytes { H(try sigInput()) }

    public func verify() throws {
        guard Ed25519.verify(signature: sig, message: try sigInput(), publicKey: author)
        else { throw TandemError.invalid("bad signature") }
    }
}

// Kind-1 payloads (§10.5), read with the strict parser.

public struct RecordState: Sendable {
    public var record: Bytes
    public var writes: [Write]
}

public struct InventoryFragment: Sendable {
    public var epoch: Bytes
    public var part: Int
    public var parts: Int
    public var bytes: Bytes
}

public enum Kind1: Sendable {
    case record(RecordState)
    case inventory(InventoryFragment)

    public static func parse(_ plaintext: Bytes, scope: Bytes) throws -> Kind1 {
        let j = try JSON.parse(plaintext)
        guard case .object = j else { throw TandemError.invalid("payload is not an object") }
        switch (j["rec"], j["inventory"]) {
        case (nil, let inv?):
            let epoch = try inv.bytes("epoch", count: 32)
            let part = try inv.int("part"), parts = try inv.int("parts")
            guard parts >= 1, part >= 0, part < parts else { throw TandemError.invalid("bad fragment index") }
            return .inventory(InventoryFragment(epoch: epoch, part: Int(part), parts: Int(parts), bytes: try inv.bytes("bytes")))
        case (_?, nil):
            let rec = try j.bytes("rec", count: 16)
            guard case let .array(items)? = j["writes"] else { throw TandemError.invalid("writes missing") }
            return .record(RecordState(record: rec, writes: try items.map { try parseWrite($0, scope: scope, record: rec) }))
        default:
            throw TandemError.invalid("exactly one of rec and inventory")
        }
    }
}

func parseDot(_ j: JSON) throws -> Dot {
    guard case let .array(items) = j, items.count == 3, case let .string(d) = items[0],
          case let .int(m) = items[1], case let .int(c) = items[2],
          let dev = B64u.decode(d), dev.count == 8, m >= 0, UInt64(m) < maxMillis, c >= 0, c <= 0xffff
    else { throw TandemError.invalid("malformed dot") }
    return Dot(device: dev, millis: UInt64(m), counter: UInt16(c))
}

func parseVV(_ j: JSON?) throws -> VV {
    guard case let .array(items)? = j else { throw TandemError.invalid("vector missing") }
    // One reading (§10.5): sorted by device ID, as signed (§5.5), each device once.
    let v = try items.map(parseDot)
    try checkVV(v)
    return v
}

func parseWrite(_ j: JSON, scope: Bytes, record: Bytes) throws -> Write {
    guard case .object = j else { throw TandemError.invalid("write is not an object") }
    guard let op = Op(rawValue: UInt8(clamping: try j.int("op"))), try j.int("op") <= 4 else { throw TandemError.invalid("unknown op") }
    guard (j["v"] != nil) == (op == .assign), (j["e"] != nil) == (op == .add || op == .remove)
    else { throw TandemError.invalid("v only on assign, e only on add and remove") }
    let w = Write(
        scope: scope, record: record, field: try j.text("f"), op: op,
        element: j["e"] != nil ? try j.bytes("e") : nil,
        value: j["v"] != nil ? try j.bytes("v") : [],
        author: try j.bytes("a", count: 32), dot: try parseDot(j["d"] ?? .null),
        ref: j["r"] != nil ? try j.bytes("r", count: 32) : Bytes(repeating: 0, count: 32),
        context: try parseVV(j["c"]), base: try parseVV(j["b"]), sig: try j.bytes("s", count: 64))
    try w.check()
    return w
}
