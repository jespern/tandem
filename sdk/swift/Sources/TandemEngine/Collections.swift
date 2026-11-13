import Foundation
import Tandem

/// One collection as a device holds it (§7–9): its roster chain, the epoch keys it was given, and its records.
struct CollectionState: Sendable {
    let id: Bytes                                   // 16
    var statements: [ID: (write: Write, statement: Statement)] = [:]
    var genesis: ID?
    /// Epoch starts seen in their parent epoch's channel (§11.6 step 4). Until then a start is a draft (§9.4).
    var announced: Set<ID> = []
    var keys: [ID: Bytes] = [:]                     // epoch-start statement ID → epoch key
    var records: [Bytes: Record] = [:]

    var owner: ID? { genesis.flatMap { statements[$0]?.statement.owner?.uid } }

    /// Whether `id` starts an epoch: its commitment differs from its parent's.
    func startsEpoch(_ id: ID) -> Bool {
        guard let s = statements[id] else { return false }
        guard let p = statements[s.write.ref] else { return id == genesis }
        return p.statement.commit != s.statement.commit
    }

    /// A statement counts toward the roster unless it is an epoch start not yet announced.
    func counts(_ id: ID) -> Bool { id == genesis || !startsEpoch(id) || announced.contains(id) }

    func successors(of id: ID) -> [ID] {
        statements.filter { $0.key != id && $0.value.write.ref == id && counts($0.key) }.map(\.key)
            .sorted { $0.lexicographicallyPrecedes($1) }
    }

    /// The roster's head: the one line of counted statements from the genesis (§9.4).
    var head: ID? {
        guard var current = genesis else { return nil }
        while let next = successors(of: current).first, successors(of: current).count == 1 { current = next }
        return current
    }

    /// Two counted successors of one statement: the collection is dead (§9.4).
    var dead: Bool { statements.keys.contains { counts($0) && successors(of: $0).count > 1 } }

    /// The epoch a statement belongs to: the first statement back along the chain with the same commitment (§8.3).
    func epoch(of id: ID) -> ID? {
        var current = id
        while let s = statements[current] {
            guard let parent = statements[s.write.ref], parent.statement.commit == s.statement.commit else { return current }
            current = s.write.ref
        }
        return nil
    }

    /// The epoch `epoch` was replaced by: an announced start whose parent is in it.
    func successorEpochs(of epoch: ID) -> [ID] {
        statements.keys.filter { startsEpoch($0) && announced.contains($0) && self.epoch(of: statements[$0]!.write.ref) == epoch }
    }

    /// Whether `ancestor` is on the chain before `id`.
    func precedes(_ ancestor: ID, _ id: ID) -> Bool {
        var current = statements[id]?.write.ref
        while let c = current, let s = statements[c] {
            if c == ancestor { return true }
            current = s.write.ref
        }
        return false
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
        collections.values.filter { following($0.id) }.map { c in
            (c.id, c.records[Self.meta]?.value(Bytes("name".utf8)).map { String(decoding: $0, as: UTF8.self) })
        }.sorted { $0.id.lexicographicallyPrecedes($1.id) }
    }

    /// This participant's role in a list now, or nil if the roster doesn't list it (removed, or never added).
    public func role(list cid: Bytes) -> Statement.Role? {
        guard let me, let c = collections[cid], let head = c.head else { return nil }
        return c.statements[head]?.statement.members.first { $0.uid == me.participant }?.role
    }

    /// Whether a list can't sync any more: two successors of one roster statement (§9.4).
    public func isDead(list cid: Bytes) -> Bool { collections[cid]?.dead ?? false }

    func writeRecord(_ cid: Bytes, record: Bytes, field: Bytes, op: Op, value: Bytes = [], element: Bytes? = nil) async throws {
        guard let me, var c = collections[cid], !c.dead, let head = c.head, let epoch = c.epoch(of: head), c.keys[epoch] != nil,
              c.statements[head]?.statement.deleted == false
        else { throw TandemError.invalid("device: no such list, no key for its current epoch yet, or it can't sync or was deleted") }
        guard let member = c.statements[head]?.statement.members.first(where: { $0.uid == me.participant }),
              member.role != .reader, record != Self.meta || member.role == .owner
        else { throw TandemError.invalid("device: this participant may not write here") }
        guard exposure(cid).isEmpty else { throw TandemError.invalid("device: waiting for the list's owner to change its key") }
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
        changed(.items(list: cid, records: [record]))
        if record == Self.meta { changed(.lists) }
        flushEvents()
        // A record is published with its whole current state, so this message dominates the ones before it (§10.7).
        // Offline, the write stays saved: the next sync's healing publishes what the hub lacks (§12.2).
        do { try await publish(cid: cid, epoch: epoch, record: record, writes: r.kept.map(\.write)) }
        catch { changed(.syncFailed("\(error)")); flushEvents() }
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
        try store.transaction {
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
            try store.markApplied(id)
            try store.noteContents(message: id, record: record, writes: try writes.map { try $0.id() })
        }
    }

    /// Members whose devices a known revocation cut but the current epoch's floors don't reflect (§6.4): until the
    /// owner changes the key, nothing new goes out under the exposed one.
    func exposure(_ cid: Bytes) -> [ID] {
        guard let c = collections[cid], let head = c.head, let s = c.statements[head]?.statement else { return [] }
        return s.members.filter { m in
            guard let floor = registry.snapshots[m.floor] else { return false }
            return registry.revs.values.contains { r in
                registry.certificate(r.target)?.participant == m.uid && registry.revocationValidity(r.id).isValid
                    && !floor.revocations.contains(r.id)
            }
        }.map(\.uid)
    }

    /// Lists this participant owns whose key must change because a member's device was revoked (§6.4 step 3).
    public func listsNeedingRekey() -> [Bytes] {
        guard let me else { return [] }
        return collections.values.filter { $0.owner == me.participant && !$0.dead && !exposure($0.id).isEmpty }.map(\.id)
            .sorted { $0.lexicographicallyPrecedes($1) }
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
            // The key of a change still in flight, so this device can finish or discard it (§11.7).
            if let note = r.value(Bytes("flight".utf8)).flatMap(FlightNote.parse) { c.keys[note.statement] = note.key }
            collections[cid] = c
        }
    }

    /// Collections as the store holds them: memberships from home, statements and records from their writes.
    static func loadCollections(store: Store, hub: Bytes, home: [ID: Record], inventories: [ID: Inventory]) throws -> [Bytes: CollectionState] {
        var out: [Bytes: CollectionState] = [:]
        for (_, r) in home {
            guard let name = r.value(Bytes("_name".utf8)), name.starts(with: Bytes("membership:".utf8)),
                  let cid = B64u.decode(String(decoding: name.dropFirst(11), as: UTF8.self)), cid.count == 16 else { continue }
            var c = CollectionState(id: cid)
            for element in r.elements(Bytes("keys".utf8)) {
                if let j = try? JSON.parse(element), let e = try? j.bytes("epoch", count: 32), let k = try? j.bytes("k", count: 32) { c.keys[e] = k }
            }
            if let note = r.value(Bytes("flight".utf8)).flatMap(FlightNote.parse) { c.keys[note.statement] = note.key }
            for row in try store.writes(scope: cid, record: Roster.record) {
                guard case let .record(state) = try Kind1.parse(row.payload, hub: hub, scope: cid), var w = state.writes.first,
                      let st = try? Statement.parse(w.value) else { continue }
                if w.ref == Bytes(repeating: 0, count: 32) { w.scope = Roster.zeroScope; c.genesis = try w.id() }
                c.statements[try w.id()] = (w, st)
            }
            for (name, _) in try store.values(prefix: "ann." + cid.hex + ".") {
                if let id = Bytes(hex: String(name.dropFirst(4 + 32 + 1))) { c.announced.insert(id) }
            }
            for record in try store.records(scope: cid) where record != Roster.record {
                c.records[record] = try loadData(store: store, hub: hub, c: c, record: record, inventories: inventories)
            }
            out[cid] = c
        }
        return out
    }

    /// A record of a collection rebuilt from the stored writes the current epoch keeps (§11.6, "Moving over"):
    /// those written in it and those its inventory lists.
    static func loadData(store: Store, hub: Bytes, c: CollectionState, record: Bytes, inventories: [ID: Inventory]) throws -> Record {
        var rec = try Record(hub: hub, scope: c.id, id: record)
        let current = c.head.flatMap(c.epoch(of:))
        let entries = try store.writes(scope: c.id, record: record).compactMap { row -> Entry? in
            guard case let .record(state) = try Kind1.parse(row.payload, hub: hub, scope: c.id), let w = state.writes.first,
                  let s = c.statements[w.ref] else { return nil }
            let e = try Entry(w, epoch: row.epoch, epochNumber: s.statement.keyN)
            guard let current, e.epoch != current else { return e }
            return inventories[current]?.entries[record]?.contains(e.id) == true ? e : nil
        }
        try rec.merge(entries)
        return rec
    }

    /// Change to the current epoch's state once its inventory is here; until then keep the old one.
    func moveCollection(_ cid: Bytes) throws {
        guard let c = collections[cid], let head = c.head, let current = c.epoch(of: head),
              current == c.genesis || inventory(current) != nil else { return }
        var moved = c
        for record in try store.records(scope: cid) where record != Roster.record {
            moved.records[record] = try Self.loadData(store: store, hub: pin.hub, c: c, record: record, inventories: inventories)
        }
        collections[cid] = moved
        changed(.items(list: cid, records: Array(moved.records.keys)))
    }

    // MARK: Collection channels

    func collectionChannel(_ tag: String) -> (cid: Bytes, epoch: ID, key: Bytes)? {
        for (cid, c) in collections {
            for (e, k) in c.keys where Tandem.collectionTag(key: k, hub: pin.hub, epoch: e) == tag { return (cid, e, k) }
        }
        return nil
    }

    func applyCollection(_ m: Message, cid: Bytes, epoch: ID, key: Bytes) throws -> Outcome {
        guard let (kind, plaintext) = try? Envelope.open(key: key, hub: pin.hub, tag: m.channel, envelope: m.body) else { return .ignored }
        if kind == 3 {
            guard let j = try? JSON.parse(plaintext), case let .string(type)? = j["type"] else { return .ignored }
            switch String(decoding: type, as: UTF8.self) {
            case "leave": return try receiveLeave(j, cid: cid)
            case "key-request": return try receiveKeyRequest(j)
            default: return .ignored
            }
        }
        guard kind == 1, let parsed = try? Kind1.parse(plaintext, hub: pin.hub, scope: cid) else { return .ignored }
        guard case let .record(state) = parsed else {
            guard case let .inventory(f) = parsed else { return .ignored }
            guard let s = collections[cid]?.statements[epoch]?.statement, let commitment = s.snapshot else { return .waiting }
            guard try receiveFragment(f, epoch: epoch, commitment: commitment) else { return .ignored }
            try moveCollection(cid)
            return .applied
        }
        var any = false, moved = false
        var applied: [Bytes] = []
        for var w in state.writes {
            if state.record == Roster.record {
                if w.ref == Bytes(repeating: 0, count: 32) { w.scope = Roster.zeroScope }   // a genesis is signed with zero scope (§7.1)
                switch try acceptStatement(w, cid: cid, channel: epoch) {
                case .applied: any = true; moved = true; applied.append(try w.id())
                case .waiting: return .waiting
                case .ignored: if collections[cid]?.statements[try w.id()] != nil { applied.append(try w.id()) }
                }
            } else {
                switch dataWriteValidity(w, cid: cid, epoch: epoch) {
                case .valid:
                    guard var c = collections[cid], let s = c.statements[w.ref], let source = c.epoch(of: w.ref) else { continue }
                    let id = try w.id()
                    try store.putWrite(id: id, scope: cid, record: state.record, epoch: source,
                                       payload: try recordPayload(record: state.record, writes: [w]))
                    var r = try c.records[state.record] ?? Record(hub: pin.hub, scope: cid, id: state.record)
                    try r.merge([try Entry(w, epoch: source, epochNumber: s.statement.keyN)])
                    c.records[state.record] = r
                    collections[cid] = c
                    applied.append(id)
                    any = true
                    changed(.items(list: cid, records: [state.record]))
                case .pending: return .waiting
                case .invalid: continue
                }
            }
        }
        try store.noteContents(message: m.id, record: state.record, writes: applied)
        if moved { try moveCollection(cid); changed(.lists); changed(.members(list: cid)) }
        return any ? .applied : .ignored
    }

    /// One roster statement (§8.2): the genesis, a same-epoch successor, or an epoch start by the owner. An epoch start
    /// seen in its parent epoch's channel is announced; seen only in its own, it is a draft (§9.4, §11.6).
    func acceptStatement(_ w: Write, cid: Bytes, channel: ID) throws -> Outcome {
        guard w.record == Roster.record, w.field == Roster.field, w.op == .assign, w.element == nil, w.context.isEmpty, w.base.isEmpty,
              let s = try? Statement.parse(w.value) else { return .ignored }
        let id = try w.id()
        guard var c = collections[cid] else { return .ignored }
        if let known = c.statements[id] {
            // Already held: an epoch start now seen in its parent's channel is announced.
            if c.startsEpoch(id), !c.announced.contains(id), c.epoch(of: known.write.ref) == channel {
                c.announced.insert(id)
                collections[cid] = c
                try store.setValue("ann." + cid.hex + "." + id.hex, [1])
                return .applied
            }
            return .ignored
        }
        var floorsToCheck: [(ID, ID)] = []   // (snapshot, floor) pairs the signer must extend
        if w.ref == Bytes(repeating: 0, count: 32) {
            guard s.isGenesis, Roster.collectionID(genesis: id) == cid, s.owner?.uid == w.author else { return .ignored }
        } else {
            guard let parent = c.statements[w.ref] else { return .waiting }
            let p = parent.statement
            guard s.n == p.n + 1, !p.deleted, w.author == c.owner, s.owner?.uid == c.owner, s.nonce == nil else { return .ignored }
            let before = Dictionary(uniqueKeysWithValues: p.members.map { ($0.uid, $0) })
            if s.commit == p.commit {
                // Same epoch (§8.2): key, snapshot and every kept floor unchanged; nobody removed or lowered.
                guard s.keyN == p.keyN, s.prev == p.prev, s.snapshot == p.snapshot else { return .ignored }
                for m in p.members {
                    guard let now = s.members.first(where: { $0.uid == m.uid }), now.floor == m.floor,
                          rank(now.role) >= rank(m.role) else { return .ignored }
                }
            } else {
                // A new epoch: next counter, a fresh commitment, an inventory, nobody added.
                guard s.keyN == p.keyN + 1, s.prev == p.commit, s.snapshot != nil,
                      !c.statements.values.contains(where: { $0.statement.commit == s.commit }),
                      s.members.allSatisfy({ before[$0.uid] != nil }) else { return .ignored }
                if let oldFloor = p.owner?.floor { floorsToCheck.append((w.authorization, oldFloor)) }
                if let newFloor = s.owner?.floor { floorsToCheck.append((w.authorization, newFloor)) }
            }
        }
        let ok = Validity.all([registry.authorizes(snapshot: w.authorization, certificate: w.certificate)]
                              + floorsToCheck.map { registry.extends($0.0, floor: $0.1) })
        switch ok {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        guard let cert = registry.certificate(w.certificate), cert.participant == w.author, (try? w.verify(publicKey: cert.sign)) != nil
        else { return .ignored }
        c.statements[id] = (w, s)
        if w.ref == Bytes(repeating: 0, count: 32) { c.genesis = id }
        let starts = c.startsEpoch(id)
        if starts, c.epoch(of: w.ref) == channel { c.announced.insert(id) }
        collections[cid] = c
        try store.transaction {
            try store.putWrite(id: id, scope: cid, record: Roster.record, epoch: c.epoch(of: id) ?? id,
                               payload: try recordPayload(record: Roster.record, writes: [w]))
            if c.announced.contains(id) { try store.setValue("ann." + cid.hex + "." + id.hex, [1]) }
        }
        return .applied
    }

    func rank(_ r: Statement.Role) -> Int { r == .owner ? 3 : r == .writer ? 2 : 1 }

    /// A data write in the channel of `epoch` (§8.3): rule 1, written under a counted statement of this very epoch
    /// that lists its author as owner or writer, by an admitted keychain extending the author's floor, and not late in a
    /// replaced epoch; or rule 2, an earlier write this epoch's inventory carries.
    func dataWriteValidity(_ w: Write, cid: Bytes, epoch: ID) -> Validity {
        guard let c = collections[cid] else { return .pending([]) }
        guard let s = c.statements[w.ref] else { return .pending([w.ref]) }
        guard w.hub == pin.hub, let source = c.epoch(of: w.ref) else { return .invalid("another hub") }
        guard c.counts(w.ref), c.counts(source) else { return .invalid("a draft authorizes nothing") }
        guard let member = s.statement.members.first(where: { $0.uid == w.author }), member.role != .reader,
              w.record != Self.meta || member.role == .owner else { return .invalid("not a writer here") }
        var checks = [registry.authorizes(snapshot: w.authorization, certificate: w.certificate)]
        if source == epoch {
            checks.append(registry.extends(w.authorization, floor: member.floor))
            checks.append(dataLateness(w, c: c, epoch: epoch))
        } else {
            guard c.precedes(w.ref, epoch) || w.ref == epoch else { return .invalid("not from an earlier epoch of this chain") }
            guard let inv = inventory(epoch) else { return .pending([epoch]) }
            guard let id = try? w.id(), inv.entries[w.record]?.contains(id) == true else { return .invalid("not carried into this epoch") }
        }
        let ok = Validity.all(checks)
        guard ok.isValid else { return ok }
        guard let cert = registry.certificate(w.certificate), cert.participant == w.author,
              (try? w.verify(publicKey: cert.sign)) != nil else { return .invalid("bad signature") }
        return .valid
    }

    /// A write in an epoch a later one replaced counts only if that one's inventory carries it (§8.3, §11.6).
    func dataLateness(_ w: Write, c: CollectionState, epoch: ID) -> Validity {
        let next = c.successorEpochs(of: epoch)
        guard !next.isEmpty, let id = try? w.id() else { return .valid }
        var missing: [ID] = []
        for e in next {
            guard let inv = inventory(e) else { missing.append(e); continue }
            if inv.entries[w.record]?.contains(id) == true { return .valid }
        }
        return missing.isEmpty ? .invalid("late write in a replaced epoch") : .pending(missing)
    }
}
