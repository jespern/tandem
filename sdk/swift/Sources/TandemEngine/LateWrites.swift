import Foundation
import Tandem

// After a key change (§11.6): this participant's late writes signed again into the new epoch, and listed writes the
// new channel lacks carried over by any device that holds them. Ported from testbed:sdk/go/device/resign.go, with
// its two protections against a member's forged context: a claim about this device beyond any dot it issued is
// forged and doesn't stop signing again; a write this device held as kept that the new state claims to replace is
// kept for the app as possibly unshared.

/// The core's vector join, outside `Device`, whose `join(link:…)` would shadow it.
func reissueBase(_ write: Write) -> VV { join(write.base, write.context, [write.dot]) }

extension Device {
    /// This participant's writes in a list that a key change may have left behind: the app offers to recover or
    /// dismiss them. Returns record and write IDs.
    public func unshared(list cid: Bytes) throws -> [(record: Bytes, write: ID)] {
        let prefix = "unshared." + cid.hex + "."
        return try store.values(prefix: prefix).compactMap { name, record in
            Bytes(hex: String(name.dropFirst(prefix.count))).map { (record, $0) }
        }
    }

    /// Dismiss a write `unshared` reported.
    public func dismissUnshared(list cid: Bytes, write: ID) throws { try store.removeValue("unshared." + cid.hex + "." + write.hex) }

    /// Every stored data write of a list, with the epoch it was written in.
    func storedWrites(_ c: CollectionState) throws -> [(record: Bytes, entry: Entry)] {
        var out: [(Bytes, Entry)] = []
        for record in try store.records(scope: c.id) where record != Roster.record {
            for row in try store.writes(scope: c.id, record: record) {
                guard case let .record(state) = try Kind1.parse(row.payload, hub: pin.hub, scope: c.id), let w = state.writes.first,
                      let s = c.statements[w.ref] else { continue }
                out.append((record, try Entry(w, epoch: row.epoch, epochNumber: s.statement.keyN)))
            }
        }
        return out
    }

    /// Whether `w` claims, for this installation, a dot beyond any it has issued.
    func forged(_ w: Write, me: Me) throws -> Bool {
        let held = try store.value("clock") ?? Bytes(repeating: 0, count: 10)
        let millis = held[0..<8].reduce(UInt64(0)) { $0 << 8 | UInt64($1) }, counter = UInt16(held[8]) << 8 | UInt16(held[9])
        return (w.context + w.base).contains { d in
            d.device == me.installation && (d.millis > millis || (d.millis == millis && d.counter > counter))
        }
    }

    /// The first statement of `epoch` that lets this participant write: the `ref` of a write signed again.
    func firstWriterStatement(_ c: CollectionState, epoch: ID, me: Me) -> ID? {
        guard let head = c.head else { return nil }
        return chain(c, to: head).first { id in
            c.epoch(of: id) == epoch && (c.statements[id]?.statement.members.contains {
                $0.uid == me.participant && ($0.role == .owner || $0.role == .writer)
            } ?? false)
        }
    }

    /// Settle this participant's writes from replaced epochs of every list it follows in its current epoch.
    func settleLateWrites() async throws {
        guard let me, registry.activity(me.certificate).isValid else { return }
        for cid in collections.keys.sorted(by: { $0.lexicographicallyPrecedes($1) }) {
            try await carryListed(cid)
            try await resign(cid, me: me)
        }
    }

    /// The current epoch of a list, its key and inventory, once this device can act in it. A deleted list has none:
    /// nothing is published in it any more (§11.9).
    func currentEpoch(_ cid: Bytes) -> (epoch: ID, key: Bytes, inventory: Inventory)? {
        guard let c = collections[cid], !c.dead, let head = c.head, c.statements[head]?.statement.deleted != true,
              let epoch = c.epoch(of: head), epoch != c.genesis,
              let key = c.keys[epoch], let inv = inventory(epoch) else { return nil }
        return (epoch, key, inv)
    }

    func resign(_ cid: Bytes, me: Me) async throws {
        guard let (current, _, inv) = currentEpoch(cid), let c = collections[cid] else { return }
        let all = try storedWrites(c)
        var touched: [Bytes: [Write]] = [:], resigned: [String] = []
        // Only this installation's own writes on this keychain: another device's, a revoked one's above all, is not ours
        // to sign again (§11.6, "the same still-admitted authoring device").
        for (record, x) in all where x.write.certificate == me.certificate && x.write.dot.device == me.installation
            && x.epoch != current {
            let done = "resigned." + x.id.hex
            guard try store.value(done) == nil else { continue }
            // Decided without signing: marked now. A write signed again is marked when it is stored, below.
            if inv.entries[record]?.contains(x.id) == true { try store.setValue(done, [1]); continue }   // carried over
            // A record the new inventory marks deleted takes no write (§8.3): nothing to sign again.
            if inv.marks(record) { try store.setValue(done, [1]); continue }
            // Decided once every listed write of the record is here: one of them may replace it.
            if try (inv.entries[record] ?? []).contains(where: { try !store.hasWrite($0) }) { continue }
            // Not signed again when a later write of this installation replaced it, which carries the change, or a write of
            // the new state did, listed or written in the new epoch, unless only through a claim about this installation
            // beyond any dot it issued, which is forged (§11.6). A write of the replaced epoch the inventory doesn't list
            // replaces nothing, whoever wrote it.
            var ownLater = false, genuine = false
            for (r, o) in all where r == record && supersedes(o, x) {
                if o.write.dot.device == me.installation
                    && (o.write.dot.millis, o.write.dot.counter) > (x.write.dot.millis, x.write.dot.counter) {
                    ownLater = true
                } else if o.epoch == current || inv.entries[record]?.contains(o.id) == true, try !forged(o.write, me: me) {
                    genuine = true
                }
            }
            if ownLater { try store.setValue(done, [1]); continue }
            if genuine {
                try store.setValue(done, [1])
                // Replaced by a listed write. If it was kept when the epoch changed, the new state's claim can't be
                // checked: keep it for the app.
                var old = try Record(hub: pin.hub, scope: cid, id: record)
                try old.merge(all.filter { $0.record == record && $0.entry.epoch == x.epoch }.map(\.entry))
                if old.kept.contains(where: { $0.id == x.id }) { try store.setValue("unshared." + cid.hex + "." + x.id.hex, record) }
                continue
            }
            let base = reissueBase(x.write)
            guard let ref = firstWriterStatement(c, epoch: current, me: me), base.count <= maxVector else {
                try store.setValue(done, [1])
                try store.setValue("unshared." + cid.hex + "." + x.id.hex, record)   // no statement allows it, or it can't be carried
                continue
            }
            let w = try reissueWrite(x.write, me: me, ref: ref, base: base)
            touched[record, default: []].append(w)
            resigned.append(done)
        }
        guard var c2 = collections[cid], let n = c2.statements[current]?.statement.keyN else { return }
        for (record, writes) in touched {
            var r = try c2.records[record] ?? Record(hub: pin.hub, scope: cid, id: record)
            try r.merge(try writes.map { try Entry($0, epoch: current, epochNumber: n) })
            c2.records[record] = r
            try store.transaction {
                for w in writes {
                    try store.putWrite(id: try w.id(), scope: cid, record: record, epoch: current,
                                       payload: try recordPayload(record: record, writes: [w]))
                }
            }
        }
        try store.transaction { for done in resigned { try store.setValue(done, [1]) } }
        collections[cid] = c2
        for (record, writes) in touched.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            try await publish(cid: cid, epoch: current, record: record, writes: writes)
        }
    }

    /// Keep every old write the current inventory lists, whoever wrote it, and publish it into the new channel if
    /// that lacks it (§11.6 "Moving over", §12.2): a listed write that missed the rotating device's upload isn't lost
    /// while any device holds it.
    func carryListed(_ cid: Bytes) async throws {
        guard let (current, key, inv) = currentEpoch(cid), let c = collections[cid] else { return }
        let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: current)
        var inNew = Set<Bytes>()
        for m in try store.messages(channel: tag) { inNew.formUnion(try store.contents(message: m).map(\.write)) }
        var missing: [Bytes: [Write]] = [:]
        for (record, e) in try storedWrites(c) where e.epoch != current && !inNew.contains(e.id) {
            guard inv.entries[record]?.contains(e.id) == true, c.records[record]?.kept.contains(where: { $0.id == e.id }) == true
            else { continue }
            missing[record, default: []].append(e.write)
        }
        for (record, writes) in missing.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            try await publish(cid: cid, epoch: current, record: record, writes: writes)
        }
    }
    /// Keep the original dot and causal reach for both late edits and pending file uploads (§11.6).
    func reissueWrite(_ o: Write, me: Me, ref: ID, base: VV) throws -> Write {
        var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: o.scope, record: o.record,
                      field: o.field, op: o.op, element: o.element, value: o.value, author: me.participant,
                      dot: o.dot, ref: ref, context: [], base: base)
        try w.sign(with: me.keys.signing)
        return w
    }

}
