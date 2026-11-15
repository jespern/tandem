import CryptoKit
import Foundation

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
 public var hub: Bytes
 public var certificate: Bytes
 public var authorization: Bytes
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

    public init(hub: Bytes, certificate: Bytes, authorization: Bytes, scope: Bytes, record: Bytes,
                field: Bytes, op: Op, element: Bytes? = nil, value: Bytes = [], author: Bytes,
                dot: Dot, ref: Bytes = Bytes(repeating: 0, count: 32), context: VV = [], base: VV = [], sig: Bytes = []) {
        self.hub = hub; self.certificate = certificate; self.authorization = authorization
        self.scope = scope; self.record = record; self.field = field; self.op = op
        self.element = element; self.value = value; self.author = author; self.dot = dot
        self.ref = ref; self.context = context; self.base = base; self.sig = sig
    }

    public mutating func sign(with key: Curve25519.Signing.PrivateKey) throws {
        sig = Bytes(try key.signature(for: sigInput()))
    }

    func check() throws {
        guard hub.count == 32, certificate.count == 32, authorization.count == 32, scope.count == 16, record.count == 16, author.count == 32, ref.count == 32, dot.device.count == 8
        else { throw TandemError.invalid("write has an ID of the wrong length") }
        guard (op == .add || op == .remove) == (element != nil) else { throw TandemError.invalid("element only on add and remove") }
        guard op == .assign || value.isEmpty else { throw TandemError.invalid("value only on assign") }
        guard op != .delete || field.isEmpty else { throw TandemError.invalid("a delete has an empty field name") }
        guard dot.millis < maxMillis else { throw TandemError.invalid("HLC millis beyond 48 bits") }
        guard validUTF8(field), field.count <= Int(UInt32.max), (element?.count ?? 0) <= Int(UInt32.max) else { throw TandemError.invalid("invalid field or oversized length prefix") }
        try checkVV(context)
        try checkVV(base)
    }

    public func sigInput() throws -> Bytes {
        try check()
        var b = Bytes("TANDEM-WRITE-V2".utf8)
        b += hub
        b += scope
        b += record
        b.lp(field)
        b.u8(op.rawValue)
        b.lp(element ?? [])
        b += H(value)
        b += author
        b += certificate
        b += authorization
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

    public func verify(publicKey: Bytes) throws {
        guard Ed25519.verify(signature: sig, message: try sigInput(), publicKey: publicKey)
        else { throw TandemError.invalid("bad signature") }
    }
}

// Kind-1 payloads (§10.5), read with the strict parser.

public struct RecordState: Sendable {
    public var record: Bytes
    public var writes: [Write]
    public init(record: Bytes, writes: [Write]) { self.record = record; self.writes = writes }
}

public struct InventoryFragment: Sendable {
    public var epoch: Bytes
    public var part: Int
    public var parts: Int
    public var bytes: Bytes
}

/// Serialize with Foundation; strict parsing and signing remain byte-based.
public func recordPayload(record: Bytes, writes: [Write]) throws -> Bytes {
    Bytes(try JSONSerialization.data(withJSONObject: try stateJSON(record: record, writes: writes)))
}

/// A pack (§10.5): several records' states in one message, in ascending order of record ID, each with writes.
public func packPayload(_ states: [RecordState]) throws -> Bytes {
    guard states.count >= 2 else { throw TandemError.invalid("a pack holds at least two records") }
    for (i, s) in states.enumerated() where s.writes.isEmpty || (i > 0 && !states[i - 1].record.lexicographicallyPrecedes(s.record)) {
        throw TandemError.invalid("pack entries out of order, repeated or without writes")
    }
    return Bytes(try JSONSerialization.data(withJSONObject: ["recs": try states.map { try stateJSON(record: $0.record, writes: $0.writes) }]))
}

private func stateJSON(record: Bytes, writes: [Write]) throws -> [String: Any] {
    guard record.count == 16 else { throw TandemError.invalid("record ID must be 16 bytes") }
    func dot(_ d: Dot) -> [Any] { [B64u.encode(d.device), d.millis, d.counter] }
    let rows: [[String: Any]] = try writes.map { w in
        try w.check()
        guard w.record == record, w.sig.count == 64 else { throw TandemError.invalid("record or signature mismatch") }
        var row: [String: Any] = ["f": String(decoding: w.field, as: UTF8.self), "op": w.op.rawValue,
            "a": B64u.encode(w.author), "cert": B64u.encode(w.certificate), "auth": B64u.encode(w.authorization),
            "d": dot(w.dot), "c": w.context.map(dot), "b": w.base.map(dot), "s": B64u.encode(w.sig)]
        if w.op == .assign { row["v"] = B64u.encode(w.value) }
        if let element = w.element { row["e"] = B64u.encode(element) }
        if w.ref != Bytes(repeating: 0, count: 32) { row["r"] = B64u.encode(w.ref) }
        return row
    }
    return ["rec": B64u.encode(record), "writes": rows]
}

public enum Kind1: Sendable {
    case record(RecordState)
    case inventory(InventoryFragment)
    /// Several records' states in one message, in ascending order of record ID (§10.5).
    case pack([RecordState])

    public static func parse(_ plaintext: Bytes, hub: Bytes, scope: Bytes) throws -> Kind1 {
        guard hub.count == 32, scope.count == 16 else { throw TandemError.invalid("invalid payload context") }
        let j = try JSON.parse(plaintext)
        guard case .object = j else { throw TandemError.invalid("payload is not an object") }
        guard [j["rec"], j["recs"], j["inventory"]].compactMap({ $0 }).count == 1 else {
            throw TandemError.invalid("exactly one of rec, recs and inventory")
        }
        func state(_ o: JSON) throws -> RecordState {
            guard case .object = o else { throw TandemError.invalid("a record state is an object") }
            let rec = try o.bytes("rec", count: 16)
            guard case let .array(items)? = o["writes"] else { throw TandemError.invalid("writes missing") }
            return RecordState(record: rec, writes: try items.map { try parseWrite($0, hub: hub, scope: scope, record: rec) })
        }
        if let recs = j["recs"] {
            guard case let .array(items) = recs, items.count >= 2 else { throw TandemError.invalid("a pack holds at least two records") }
            let states = try items.map(state)
            for (i, s) in states.enumerated() where s.writes.isEmpty || (i > 0 && !states[i - 1].record.lexicographicallyPrecedes(s.record)) {
                throw TandemError.invalid("pack entries out of order, repeated or without writes")
            }
            return .pack(states)
        }
        switch (j["rec"], j["inventory"]) {
        case (nil, let inv?):
            let epoch = try inv.bytes("epoch", count: 32)
            let part = try inv.int("part"), parts = try inv.int("parts")
            guard parts >= 1, part >= 0, part < parts else { throw TandemError.invalid("bad fragment index") }
            return .inventory(InventoryFragment(epoch: epoch, part: Int(part), parts: Int(parts), bytes: try inv.bytes("bytes")))
        case (_?, nil):
            return .record(try state(j))
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

func parseWrite(_ j: JSON, hub: Bytes, scope: Bytes, record: Bytes) throws -> Write {
    guard case .object = j else { throw TandemError.invalid("write is not an object") }
    guard let op = Op(rawValue: UInt8(clamping: try j.int("op"))), try j.int("op") <= 4 else { throw TandemError.invalid("unknown op") }
    guard (j["v"] != nil) == (op == .assign), (j["e"] != nil) == (op == .add || op == .remove)
    else { throw TandemError.invalid("v only on assign, e only on add and remove") }
    let w = Write(
        hub: hub, certificate: try j.bytes("cert", count: 32), authorization: try j.bytes("auth", count: 32), scope: scope, record: record, field: try j.text("f"), op: op,
        element: j["e"] != nil ? try j.bytes("e") : nil,
        value: j["v"] != nil ? try j.bytes("v") : [],
        author: try j.bytes("a", count: 32), dot: try parseDot(j["d"] ?? .null),
        ref: j["r"] != nil ? try j.bytes("r", count: 32) : Bytes(repeating: 0, count: 32),
        context: try parseVV(j["c"]), base: try parseVV(j["b"]), sig: try j.bytes("s", count: 64))
    try w.check()
    return w
}
