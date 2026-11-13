/// An entry has passed the caller's device and roster authorization checks.
public struct Entry: Sendable {
    public let write: Write
    public let id: Bytes
    public let epoch: Bytes
    public let epochNumber: UInt64
    public init(_ write: Write, epoch: Bytes, epochNumber: UInt64) throws {
        guard epoch.count == 32 else { throw TandemError.invalid("epoch ID must be 32 bytes") }
        self.write = write; self.id = try write.id(); self.epoch = epoch; self.epochNumber = epochNumber
    }
}

func clockLess(_ a: Dot, _ b: Dot) -> Bool { a.millis < b.millis || (a.millis == b.millis && a.counter < b.counter) }
public func join(_ vectors: VV...) -> VV { joinVectors(vectors) }
func joinVectors(_ vectors: [VV]) -> VV {
    var byDevice: [Bytes: Dot] = [:]
    for v in vectors { for d in v {
        if let old = byDevice[d.device], !clockLess(old, d) { continue }
        byDevice[d.device] = d
    }}
    return byDevice.values.sorted { lexLess($0.device, $1.device) }
}
func covers(_ a: VV, _ b: VV) -> Bool {
    b.allSatisfy { d in a.contains { $0.device == d.device && !clockLess($0, d) } }
}
func reach(_ w: Write) -> VV { join(w.context, [w.dot]) }
func includes(_ w: Write, _ x: Write) -> Bool { covers(w.context, reach(x)) && covers(w.base, x.base) }
func sameSlot(_ w: Write, _ x: Write) -> Bool {
    if w.op == .delete || x.op == .delete { return w.op == x.op }
    let ws = w.op == .add || w.op == .remove, xs = x.op == .add || x.op == .remove
    return ws == xs && w.field == x.field && (!ws || w.element == x.element)
}
public func supersedes(_ w: Entry, _ x: Entry) -> Bool {
    let a = w.write, b = x.write
    guard w.id != x.id, a.hub == b.hub, a.scope == b.scope, a.record == b.record,
        !(b.op == .delete && a.op != .delete), a.op == .delete || sameSlot(a, b) else { return false }
    if w.epochNumber > x.epochNumber { return covers(a.base, reach(b)) && covers(a.base, b.base) }
    return w.epochNumber == x.epochNumber && w.epoch == x.epoch && includes(a, b) && !includes(b, a)
}
func displayLess(_ a: Entry, _ b: Entry) -> Bool {
    if a.epochNumber != b.epochNumber { return a.epochNumber < b.epochNumber }
    if a.write.dot.millis != b.write.dot.millis || a.write.dot.counter != b.write.dot.counter { return clockLess(a.write.dot, b.write.dot) }
    if a.write.author != b.write.author { return lexLess(a.write.author, b.write.author) }
    if a.write.dot.device != b.write.dot.device { return lexLess(a.write.dot.device, b.write.dot.device) }
    return lexLess(a.id, b.id)
}

public struct Record: Sendable {
    public let hub: Bytes, scope: Bytes, id: Bytes
    public private(set) var kept: [Entry] = []
    public init(hub: Bytes, scope: Bytes, id: Bytes) throws {
        guard hub.count == 32, scope.count == 16, id.count == 16 else { throw TandemError.invalid("invalid record context") }
        self.hub = hub; self.scope = scope; self.id = id
    }
    public mutating func merge(_ incoming: [Entry]) throws {
        guard incoming.allSatisfy({ $0.write.hub == hub && $0.write.scope == scope && $0.write.record == id })
        else { throw TandemError.invalid("entry belongs to another record, scope or hub") }
        var all: [Bytes: Entry] = [:]
        for e in kept + incoming { all[e.id] = e }
        let entries = Array(all.values)
        kept = entries.filter { x in !entries.contains { supersedes($0, x) } }.sorted { lexLess($0.id, $1.id) }
    }
    public var deleted: Bool { kept.contains { $0.write.op == .delete } }
    /// All siblings are retained; presentation may take the first 16.
    public func siblings(_ field: Bytes) -> [Entry] {
        kept.filter { $0.write.op == .assign && $0.write.field == field }.sorted { displayLess($1, $0) }
    }
    public func value(_ field: Bytes) -> Bytes? { siblings(field).first?.write.value }
    public func elements(_ field: Bytes) -> [Bytes] {
        Array(Set(kept.filter { $0.write.op == .add && $0.write.field == field }.compactMap { $0.write.element })).sorted(by: lexLess)
    }
    public func vectors(op: Op, field: Bytes, element: Bytes? = nil, epoch: Bytes, epochNumber: UInt64) throws -> (context: VV, base: VV) {
        let probe = Write(hub: hub, certificate: [], authorization: [], scope: scope, record: id, field: field, op: op,
                          element: element, author: [], dot: Dot(device: [], millis: 0, counter: 0))
        var contexts: [VV] = [], bases: [VV] = []
        for e in kept where op == .delete || sameSlot(probe, e.write) {
            bases.append(e.write.base)
            if e.epoch == epoch && e.epochNumber == epochNumber { contexts.append(reach(e.write)) }
            else { bases.append(reach(e.write)) }
        }
        let c = joinVectors(contexts), b = joinVectors(bases)
        try checkVV(c); try checkVV(b)
        return (c, b)
    }
}

/// The caller persists this clock and its write atomically before exposing the edit.
public func nextClock(millis: UInt64, counter: UInt16, now: UInt64) throws -> (millis: UInt64, counter: UInt16) {
    guard millis < maxMillis, now < maxMillis else { throw TandemError.invalid("clock exhausted") }
    if now > millis { return (now, 0) }
    if counter < UInt16.max { return (millis, counter + 1) }
    guard millis + 1 < maxMillis else { throw TandemError.invalid("clock exhausted") }
    return (millis + 1, 0)
}
