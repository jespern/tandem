import Tandem

/// A roster statement (§8.1, §10.8): the value of `_roster`'s `s` field.
public struct Statement: Equatable, Sendable {
    public struct Member: Equatable, Sendable {
        public var uid: ID, role: Role, name: Bytes, floor: ID
    }
    public enum Role: String, Sendable { case owner, writer, reader }

    public var n: UInt64
    public var keyN: UInt64, commit: Bytes, prev: Bytes?
    public var snapshot: Bytes?
    public var nonce: Bytes?
    public var members: [Member]
    public var deleted: Bool

    public var owner: Member? { members.first { $0.role == .owner } }

    /// The strict JSON of §10.8, members sorted by their participant ID's bytes.
    public func json() -> Bytes {
        JSONWriter.object { w in
            w.member("n", int: n)
            w.member("key", json: JSONWriter.object { k in
                k.member("n", int: keyN)
                k.member("commit", b64u: commit)
                if let prev { k.member("prev", b64u: prev) }
            })
            if let snapshot { w.member("snapshot", b64u: snapshot) }
            if let nonce { w.member("nonce", b64u: nonce) }
            w.member("members") { a in
                a.array(members.sorted { $0.uid.lexicographicallyPrecedes($1.uid) }) { a, m in
                    a.open()
                    a.member("uid", b64u: m.uid)
                    a.member("role", text: Bytes(m.role.rawValue.utf8))
                    a.member("name", text: m.name)
                    a.member("floor", b64u: m.floor)
                    a.close()
                }
            }
            w.member("deleted") { $0.raw(deleted ? "true" : "false") }
        }
    }

    public static func parse(_ data: Bytes) throws -> Statement {
        func fail(_ what: String) -> TandemError { .invalid("roster: \(what)") }
        let j = try JSON.parse(data)
        guard case let .int(n)? = j["n"], n >= 0, case let key? = j["key"], case let .int(kn)? = key["n"], kn >= 0
        else { throw fail("n and key.n") }
        let commit = try key.bytes("commit", count: 32)
        let prev: Bytes? = key["prev"] == nil ? nil : try key.bytes("prev", count: 32)
        let snapshot: Bytes? = j["snapshot"] == nil ? nil : try j.bytes("snapshot", count: 32)
        let nonce: Bytes? = j["nonce"] == nil ? nil : try j.bytes("nonce", count: 16)
        guard case let .array(items)? = j["members"], case let .bool(deleted)? = j["deleted"] else { throw fail("members, deleted") }
        let members = try items.map { m -> Member in
            guard case let .string(r)? = m["role"], let role = Role(rawValue: String(decoding: r, as: UTF8.self)),
                  case let .string(name)? = m["name"], name.count <= 256 else { throw fail("member") }
            return Member(uid: try m.bytes("uid", count: 32), role: role, name: name, floor: try m.bytes("floor", count: 32))
        }
        guard zip(members, members.dropFirst()).allSatisfy({ $0.uid.lexicographicallyPrecedes($1.uid) })
        else { throw fail("members must be sorted and unique") }
        guard members.filter({ $0.role == .owner }).count == 1 else { throw fail("exactly one owner") }
        return Statement(n: UInt64(n), keyN: UInt64(kn), commit: commit, prev: prev, snapshot: snapshot, nonce: nonce,
                         members: members, deleted: deleted)
    }

    /// The genesis rules of §7.1.
    public var isGenesis: Bool {
        n == 0 && keyN == 0 && prev == nil && snapshot == nil && nonce?.count == 16 && members.count == 1 && !deleted
    }
}

public enum Roster {
    public static let record = Tandem.namedID(Bytes("_roster".utf8))
    public static let field = Bytes("s".utf8)
    public static let zeroScope = Bytes(repeating: 0, count: 16)

    /// The collection ID: the first 16 bytes of the genesis statement's write ID (§7.1).
    public static func collectionID(genesis: ID) -> Bytes { Array(genesis.prefix(16)) }
}
