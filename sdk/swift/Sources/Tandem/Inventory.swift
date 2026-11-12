/// Snapshot inventories (§8.3): record ID → write IDs carried into a new epoch.
public struct Inventory: Equatable, Sendable {
    public var entries: [Bytes: [Bytes]]

    public init(_ entries: [Bytes: [Bytes]] = [:]) { self.entries = entries }

    /// u32(recordCount), then entries sorted by record ID, each
    /// recordID || u32(writeCount) || sorted, unique write IDs.
    public func bytes() throws -> Bytes {
        var b = Bytes()
        b.u32(UInt32(entries.count))
        for rec in entries.keys.sorted(by: lexLess) {
            guard rec.count == 16 else { throw TandemError.invalid("record ID must be 16 bytes") }
            let ws = entries[rec, default: []].sorted(by: lexLess)
            guard !ws.isEmpty else { throw TandemError.invalid("an inventory entry lists at least one write") }
            for (i, w) in ws.enumerated() {
                guard w.count == 32, i == 0 || ws[i - 1] != w else { throw TandemError.invalid("bad write ID list") }
            }
            b += rec
            b.u32(UInt32(ws.count))
            for w in ws { b += w }
        }
        return b
    }

    /// Reads canonical bytes and nothing else.
    public static func parse(_ data: Bytes) throws -> Inventory {
        let bad = TandemError.invalid("not a canonical inventory")
        var i = 0
        func take(_ n: Int) throws -> Bytes {
            guard data.count - i >= n else { throw bad }
            defer { i += n }
            return Array(data[i..<i + n])
        }
        func count() throws -> Int { try take(4).reduce(0) { $0 << 8 | Int($1) } }
        var inv = Inventory()
        var prev: Bytes?
        for _ in 0..<(try count()) {
            let rec = try take(16)
            if let prev, !lexLess(prev, rec) { throw bad }
            prev = rec
            var ws: [Bytes] = []
            let n = try count()
            guard n > 0 else { throw bad }  // every entry lists at least one write
            for _ in 0..<n {
                let w = try take(32)
                if let last = ws.last, !lexLess(last, w) { throw bad }
                ws.append(w)
            }
            inv.entries[rec] = ws
        }
        guard i == data.count else { throw bad }
        return inv
    }
}

/// Signed inputs of the objects other than writes (§5.5).
public enum Signed {
    public static func join(collection: Bytes, invite: Bytes, uid: Bytes, uka: Bytes, name: Bytes) -> Bytes {
        var b = Bytes("TANDEM-JOIN-V1".utf8) + collection + invite + uid + uka
        b.lp(name)
        return b
    }
    public static func invite(collection: Bytes, invite: Bytes, uid: Bytes) -> Bytes {
        Bytes("TANDEM-INVITE-V1".utf8) + collection + invite + uid
    }
    public static func grant(recipient: Bytes, collection: Bytes, at: Bytes, epoch: Bytes, key: Bytes) -> Bytes {
        Bytes("TANDEM-GRANT-V1".utf8) + recipient + collection + at + epoch + Tandem.commit(key)
    }
    public static func keyRequest(collection: Bytes, have: Bytes, uid: Bytes) -> Bytes {
        Bytes("TANDEM-KEYREQ-V1".utf8) + collection + have + uid
    }
    public static func leave(collection: Bytes, uid: Bytes, at: Bytes) -> Bytes {
        Bytes("TANDEM-LEAVE-V1".utf8) + collection + uid + at
    }
    public static func pairChallenge(p: Bytes, ek: Bytes, n: Bytes) -> Bytes {
        Bytes("TANDEM-PAIR-CODE-V1".utf8) + p + ek + n
    }
}
