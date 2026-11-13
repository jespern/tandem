import Foundation
import Tandem

/// One collection as a device holds it (§7–9): its roster chain, the epoch keys it was given, and its records.
struct CollectionState: Sendable {
    let id: Bytes                                   // 16
    var statements: [ID: (write: Write, statement: Statement)] = [:]
    var genesis: ID?
    var keys: [ID: Bytes] = [:]                     // epoch-start statement ID → epoch key
    var records: [Bytes: Record] = [:]

    var owner: ID? { genesis.flatMap { statements[$0]?.statement.owner?.uid } }

    /// The statement no other statement follows: this epoch's roster head (no rotation yet: one line).
    var head: ID? {
        let parents = Set(statements.values.map(\.write.ref))
        return statements.keys.filter { !parents.contains($0) }.max { statements[$0]!.statement.n < statements[$1]!.statement.n }
    }

    /// The epoch a statement belongs to: the first statement back along the chain with the same commitment (§8.3).
    func epoch(of id: ID) -> ID? {
        var current = id
        while let s = statements[current] {
            guard let parent = statements[s.write.ref], parent.statement.commit == s.statement.commit else { return current }
            current = s.write.ref
        }
        return nil
    }
}

extension Device {
    static let meta = Tandem.namedID(Bytes("_meta".utf8))

    // MARK: Creating a list (§7.1)

    /// Create a collection owned by this participant, private until shared (§7.4). Returns its ID.
    public func createList(name: String) async throws -> Bytes {
        guard let me else { throw TandemError.invalid("device: not on this hub yet") }
        let key = Self.random(32)
        let statement = Statement(n: 0, keyN: 0, commit: Tandem.commit(key), prev: nil, snapshot: nil, nonce: Self.random(16),
                                  members: [.init(uid: me.participant, role: .owner, name: Bytes(), floor: me.auth)], deleted: false)
        var genesis = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: Roster.zeroScope,
                            record: Roster.record, field: Roster.field, op: .assign, value: statement.json(),
                            author: me.participant, dot: try nextDot(me))
        try genesis.sign(with: me.keys.signing)
        let gid = try genesis.id(), cid = Roster.collectionID(genesis: gid)

        var c = CollectionState(id: cid)
        c.statements[gid] = (genesis, statement)
        c.genesis = gid
        c.keys[gid] = key
        collections[cid] = c
        try store.transaction {
            try store.putWrite(id: gid, scope: cid, record: Roster.record, epoch: gid, payload: try recordPayload(record: Roster.record, writes: [genesis]))
        }
        // The key goes into home first: that's how this participant's other devices find the list (§6.2).
        let membership = "membership:" + B64u.encode(cid)
        try await setHome(membership, field: "owner", value: me.participant)
        try await setHome(membership, field: "role", value: Bytes("owner".utf8))
        try await addHome(membership, field: "keys", element: Self.keyElement(epoch: gid, key: key))
        try await publish(cid: cid, epoch: gid, record: Roster.record, writes: [genesis])
        try await setField(list: cid, record: Self.meta, field: "name", value: Bytes(name.utf8))
        return cid
    }

    /// `{"epoch":"…","k":"…"}`, in that order (§6.2).
    static func keyElement(epoch: ID, key: Bytes) -> Bytes {
        JSONWriter.object { $0.member("epoch", b64u: epoch); $0.member("k", b64u: key) }
    }

    // MARK: Records

    /// A new random record ID (§7.3).
    public nonisolated func newRecordID() -> Bytes { Self.random(16) }

    /// Set a field of a record in a list, as this device, in the list's current epoch.
    public func setField(list cid: Bytes, record: Bytes, field: String, value: Bytes) async throws {
        try await writeRecord(cid, record: record, field: Bytes(field.utf8), op: .assign, value: value)
    }

    /// Delete a record (§9.5). Named records can't be deleted (§7.3).
    public func deleteRecord(list cid: Bytes, record: Bytes) async throws {
        guard record != Self.meta, record != Roster.record else { throw TandemError.invalid("device: a named record is never deleted") }
        try await writeRecord(cid, record: record, field: [], op: .delete)
    }

    public func value(list cid: Bytes, record: Bytes, field: String) -> Bytes? {
        guard let r = collections[cid]?.records[record], !r.deleted else { return nil }
        return r.value(Bytes(field.utf8))
    }

    /// The list's records, without the named ones and without deleted ones.
    public func records(list cid: Bytes) -> [Bytes] {
        (collections[cid]?.records ?? [:]).filter { $0.key != Self.meta && !$0.value.deleted }.map(\.key)
            .sorted { $0.lexicographicallyPrecedes($1) }
    }

    /// The lists this participant is a member of, with their names where known: those its home records (§6.2).
    public func lists() -> [(id: Bytes, name: String?)] {
        collections.values.filter { homeValue("membership:" + B64u.encode($0.id), field: "owner") != nil }.map { c in
            (c.id, c.records[Self.meta]?.value(Bytes("name".utf8)).map { String(decoding: $0, as: UTF8.self) })
        }.sorted { $0.id.lexicographicallyPrecedes($1.id) }
    }

    func writeRecord(_ cid: Bytes, record: Bytes, field: Bytes, op: Op, value: Bytes = [], element: Bytes? = nil) async throws {
        guard let me, var c = collections[cid], let head = c.head, let epoch = c.epoch(of: head), c.keys[epoch] != nil
        else { throw TandemError.invalid("device: no such list, or no key for it yet") }
        guard let member = c.statements[head]?.statement.members.first(where: { $0.uid == me.participant }),
              member.role != .reader, record != Self.meta || member.role == .owner
        else { throw TandemError.invalid("device: this participant may not write here") }
        var writes: [Write] = []
        if record == Self.meta, c.records[record]?.value(Bytes("_name".utf8)) == nil {
            writes.append(try signRecord(c, me: me, record: record, field: Bytes("_name".utf8), op: .assign,
                                         value: Bytes("_meta".utf8), element: nil, head: head, epoch: epoch, after: []))
        }
        writes.append(try signRecord(c, me: me, record: record, field: field, op: op, value: value, element: element,
                                     head: head, epoch: epoch, after: writes))
        var r = try c.records[record] ?? Record(hub: pin.hub, scope: cid, id: record)
        try r.merge(try writes.map { try Entry($0, epoch: epoch, epochNumber: c.statements[head]!.statement.keyN) })
        c.records[record] = r
        collections[cid] = c
        try store.transaction {
            for w in writes {
                try store.putWrite(id: try w.id(), scope: cid, record: record, epoch: epoch, payload: try recordPayload(record: record, writes: [w]))
            }
        }
        try await publish(cid: cid, epoch: epoch, record: record, writes: writes)
    }

    private func signRecord(_ c: CollectionState, me: Me, record: Bytes, field: Bytes, op: Op, value: Bytes, element: Bytes?,
                            head: ID, epoch: ID, after earlier: [Write]) throws -> Write {
        let n = c.statements[head]!.statement.keyN
        var r = try c.records[record] ?? Record(hub: pin.hub, scope: c.id, id: record)
        if !earlier.isEmpty { try r.merge(try earlier.map { try Entry($0, epoch: epoch, epochNumber: n) }) }
        let v = try r.vectors(op: op, field: field, element: element, epoch: epoch, epochNumber: n)
        var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: c.id, record: record, field: field,
                      op: op, element: element, value: value, author: me.participant, dot: try nextDot(me), ref: head,
                      context: v.context, base: v.base)
        try w.sign(with: me.keys.signing)
        return w
    }

    func publish(cid: Bytes, epoch: ID, record: Bytes, writes: [Write]) async throws {
        guard let key = collections[cid]?.keys[epoch] else { return }
        let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
        let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 1, tag: tag, plaintext: try recordPayload(record: record, writes: writes))
        let id = try await hub.append(channel: tag, body: envelope)
        try store.noteMessage(id: id, channel: tag, hash: H(envelope))
        try store.markApplied(id)
    }

    // MARK: Learning of lists from home (§6.2)

    /// Every `membership:` record in home with keys: follow those collections.
    func refreshMemberships() {
        for (_, r) in home {
            guard let name = r.value(Bytes("_name".utf8)), name.starts(with: Bytes("membership:".utf8)),
                  let cid = B64u.decode(String(decoding: name.dropFirst(11), as: UTF8.self)), cid.count == 16 else { continue }
            var c = collections[cid] ?? CollectionState(id: cid)
            for element in r.elements(Bytes("keys".utf8)) {
                guard let j = try? JSON.parse(element), let e = try? j.bytes("epoch", count: 32), let k = try? j.bytes("k", count: 32)
                else { continue }
                c.keys[e] = k
            }
            collections[cid] = c
        }
    }

    /// Collections as the store holds them: memberships from home, statements and records from their writes.
    static func loadCollections(store: Store, hub: Bytes, home: [ID: Record]) throws -> [Bytes: CollectionState] {
        var out: [Bytes: CollectionState] = [:]
        for (_, r) in home {
            guard let name = r.value(Bytes("_name".utf8)), name.starts(with: Bytes("membership:".utf8)),
                  let cid = B64u.decode(String(decoding: name.dropFirst(11), as: UTF8.self)), cid.count == 16 else { continue }
            var c = CollectionState(id: cid)
            for element in r.elements(Bytes("keys".utf8)) {
                if let j = try? JSON.parse(element), let e = try? j.bytes("epoch", count: 32), let k = try? j.bytes("k", count: 32) { c.keys[e] = k }
            }
            for row in try store.writes(scope: cid, record: Roster.record) {
                guard case let .record(state) = try Kind1.parse(row.payload, hub: hub, scope: cid), var w = state.writes.first,
                      let st = try? Statement.parse(w.value) else { continue }
                if w.ref == Bytes(repeating: 0, count: 32) { w.scope = Roster.zeroScope; c.genesis = try w.id() }
                c.statements[try w.id()] = (w, st)
            }
            for record in try store.records(scope: cid) where record != Roster.record {
                var rec = try Record(hub: hub, scope: cid, id: record)
                let entries = try store.writes(scope: cid, record: record).compactMap { row -> Entry? in
                    guard case let .record(state) = try Kind1.parse(row.payload, hub: hub, scope: cid), let w = state.writes.first,
                          let s = c.statements[w.ref] else { return nil }
                    return try Entry(w, epoch: row.epoch, epochNumber: s.statement.keyN)
                }
                try rec.merge(entries)
                c.records[record] = rec
            }
            out[cid] = c
        }
        return out
    }

    // MARK: Collection channels

    func collectionChannel(_ tag: String) -> (cid: Bytes, epoch: ID, key: Bytes)? {
        for (cid, c) in collections {
            for (e, k) in c.keys where Tandem.collectionTag(key: k, hub: pin.hub, epoch: e) == tag { return (cid, e, k) }
        }
        return nil
    }

    func applyCollection(_ m: Message, cid: Bytes, epoch: ID, key: Bytes) throws -> Outcome {
        guard let (kind, plaintext) = try? Envelope.open(key: key, hub: pin.hub, tag: m.channel, envelope: m.body), kind == 1,
              case let .record(state)? = try? Kind1.parse(plaintext, hub: pin.hub, scope: cid)
        else { return .ignored }
        var any = false
        for var w in state.writes {
            if state.record == Roster.record {
                if w.ref == Bytes(repeating: 0, count: 32) { w.scope = Roster.zeroScope }   // a genesis is signed with zero scope (§7.1)
                switch try acceptStatement(w, cid: cid) {
                case .applied: any = true
                case .waiting: return .waiting
                case .ignored: continue
                }
            } else {
                switch dataWriteValidity(w, cid: cid, epoch: epoch) {
                case .valid:
                    guard var c = collections[cid], let s = c.statements[w.ref] else { continue }
                    var r = try c.records[state.record] ?? Record(hub: pin.hub, scope: cid, id: state.record)
                    try r.merge([try Entry(w, epoch: epoch, epochNumber: s.statement.keyN)])
                    c.records[state.record] = r
                    collections[cid] = c
                    try store.putWrite(id: try w.id(), scope: cid, record: state.record, epoch: epoch,
                                       payload: try recordPayload(record: state.record, writes: [w]))
                    any = true
                case .pending: return .waiting
                case .invalid: continue
                }
            }
        }
        return any ? .applied : .ignored
    }

    /// One roster statement (§8.2): the genesis of this collection, or a successor in the same epoch by its owner.
    func acceptStatement(_ w: Write, cid: Bytes) throws -> Outcome {
        guard w.record == Roster.record, w.field == Roster.field, w.op == .assign, w.element == nil, w.context.isEmpty, w.base.isEmpty,
              let s = try? Statement.parse(w.value) else { return .ignored }
        let id = try w.id()
        guard var c = collections[cid], c.statements[id] == nil else { return .ignored }
        if w.ref == Bytes(repeating: 0, count: 32) {
            guard s.isGenesis, Roster.collectionID(genesis: id) == cid, s.owner?.uid == w.author else { return .ignored }
        } else {
            guard let parent = c.statements[w.ref] else { return .waiting }
            guard s.n == parent.statement.n + 1, !parent.statement.deleted, w.author == c.owner, s.owner?.uid == c.owner,
                  s.commit == parent.statement.commit, s.keyN == parent.statement.keyN, s.snapshot == parent.statement.snapshot
            else { return .ignored }   // epoch changes come with rotation (§11.6)
        }
        switch registry.authorizes(snapshot: w.authorization, certificate: w.certificate) {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        guard let cert = registry.certificate(w.certificate), cert.participant == w.author, (try? w.verify(publicKey: cert.sign)) != nil
        else { return .ignored }
        c.statements[id] = (w, s)
        if w.ref == Bytes(repeating: 0, count: 32) { c.genesis = id }
        collections[cid] = c
        try store.putWrite(id: id, scope: cid, record: Roster.record, epoch: c.epoch(of: id) ?? id,
                           payload: try recordPayload(record: Roster.record, writes: [w]))
        return .applied
    }

    /// A data write (§8.3 rule 1): under a valid statement of this very epoch that lists its author as owner or writer
    /// (owner only for `_meta`), by an admitted keychain whose snapshot extends the author's floor.
    func dataWriteValidity(_ w: Write, cid: Bytes, epoch: ID) -> Validity {
        guard let c = collections[cid] else { return .pending([]) }
        guard let s = c.statements[w.ref] else { return .pending([w.ref]) }
        guard c.epoch(of: w.ref) == epoch, w.hub == pin.hub else { return .invalid("another epoch") }
        guard let member = s.statement.members.first(where: { $0.uid == w.author }), member.role != .reader,
              w.record != Self.meta || member.role == .owner else { return .invalid("not a writer here") }
        let ok = Validity.all([registry.authorizes(snapshot: w.authorization, certificate: w.certificate),
                               registry.extends(w.authorization, floor: member.floor)])
        guard ok.isValid else { return ok }
        guard let cert = registry.certificate(w.certificate), cert.participant == w.author,
              (try? w.verify(publicKey: cert.sign)) != nil else { return .invalid("bad signature") }
        return .valid
    }
}
