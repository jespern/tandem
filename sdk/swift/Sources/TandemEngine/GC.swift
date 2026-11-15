import Foundation
import Tandem

// Garbage collection beyond §10.7, after each sync, in what the specification already allows:
//
// - Writes of past epochs (§9.3): once the current epoch's inventory is here, a write of an earlier epoch that the
//   inventory doesn't list is out of the state for good and is dropped, once this participant's late writes are
//   settled (§11.6) and unless the app was told of it as unshared.
// - Lists that are gone (§11.6, §11.9): a list the owner deleted, or this participant was removed from, keeps its
//   replica read-only for the retention period, on this device's own clock from when it first saw that. Then the
//   replica goes, all but the roster, so that the device still knows what happened. A device of the owner of a
//   deleted list also destroys the list's data on the hub, all but the roster, and its file chunks (DESIGN.md §17.7).

extension Device {
    /// How long a gone list's replica stays read-only, and a deleted list's data on the hub, before it is collected (§11.9).
    public static let defaultRetention: Duration = .seconds(30 * 24 * 3600)

    /// Changes how long a gone list stays before it is collected, from now on. Zero takes the default; a negative value
    /// keeps gone lists for good.
    public func setRetention(_ r: Duration) { retention = r == .zero ? Self.defaultRetention : r }

    /// How many messages of one deleted record each the current channel holds before a device of the owner packs them
    /// (§10.7).
    public static let defaultPackThreshold = 50

    /// Keeps a pack well within the message size limit (§10.6).
    static let maxPackBytes = 512 * 1024

    /// Changes how many single tombstone messages a list's channel holds before this device, if it is the owner's, packs
    /// them. Zero takes the default.
    public func setPackThreshold(_ n: Int) { packThreshold = n > 0 ? n : Self.defaultPackThreshold }

    /// The pass after hygiene.
    func collectGarbage() async throws {
        guard let me else { return }
        for cid in collections.keys.sorted(by: { $0.lexicographicallyPrecedes($1) }) {
            try dropPastWrites(cid, me: me)
            try await collectGone(cid, me: me)
            try await trimRosters(cid, me: me)
            try await packTombstones(cid, me: me)
        }
    }

    /// Drops writes the state can do without (§9.3): those of earlier epochs the current inventory doesn't list, and
    /// those of the current epoch that a held eligible write on the feed replaced, an own write only for a later own
    /// one (proved in proofs/TandemProofs/Drop.lean). Only once no message this device knows holds the write: §10.7's
    /// clean-up judges a message by the writes in it, and needs them while the message is there.
    func dropPastWrites(_ cid: Bytes, me: Me) throws {
        guard let (current, inv) = dropEpoch(cid), let c = collections[cid], !purged(cid) else { return }
        var drop: [Bytes] = [], markers: [String] = [], listed: [String: Bytes] = [:]
        let all = try storedWrites(c)
        let byRecord = Dictionary(grouping: all, by: \.record)
        func keepManifest(_ record: Bytes, _ x: Entry) throws {
            // A manifest's chunks stay accountable after its write goes, as a dropped upload's do, until none is in the
            // chunk index (DESIGN.md §17.7, condition 2).
            if blobs != nil, x.write.op == .assign, Self.isBlobField(x.write.field), case let .manifest(m) = Blob.parse(x.write.value), m.chunks != nil {
                listed["blobs.dropped." + cid.hex + "." + x.id.hex] = try recordPayload(record: record, writes: [x.write])
            }
        }
        for (record, x) in all {
            if x.epoch == current {
                if try !store.onFeed(x.id), try replacer(byRecord[record] ?? [], x, current: current, inventory: inv, me: me) {
                    drop.append(x.id)
                    try keepManifest(record, x)
                }
                continue
            }
            if inv.entries[record]?.contains(x.id) == true { continue }   // carried in through the inventory
            // Shown to the app as possibly unshared: kept until it is dismissed.
            if try store.value("unshared." + cid.hex + "." + x.id.hex) != nil { continue }
            // This installation's own: only once it is settled (signed again, carried or replaced).
            let done = "resigned." + x.id.hex
            let own = x.write.certificate == me.certificate && x.write.dot.device == me.installation
            if own, try store.value(done) == nil { continue }
            drop.append(x.id)
            markers.append(done)
            try keepManifest(record, x)
        }
        guard !drop.isEmpty else { return }
        try store.transaction {
            for (name, payload) in listed { try store.setValue(name, payload) }
            try store.dropWrites(drop)
            for m in markers { try store.removeValue(m) }
        }
    }

    /// The epoch writes are judged in for dropping, and its inventory: the current one once its inventory is here, or
    /// the first epoch, which has none.
    func dropEpoch(_ cid: Bytes) -> (epoch: ID, inventory: Inventory)? {
        if let (current, _, inv) = currentEpoch(cid) { return (current, inv) }
        guard let c = collections[cid], !c.dead, let head = c.head, c.statements[head]?.statement.deleted == false,
              let epoch = c.epoch(of: head), epoch == c.genesis, c.keys[epoch] != nil else { return nil }
        return (epoch, Inventory())
    }

    /// Whether a held write lets `x`, a write of the current epoch, go: eligible in it, on the feed, superseding `x`,
    /// and for an own `x` a later own write.
    func replacer(_ record: [(record: Bytes, entry: Entry)], _ x: Entry, current: ID, inventory inv: Inventory, me: Me) throws -> Bool {
        let own = x.write.dot.device == me.installation
        for (r, y) in record where y.id != x.id && supersedes(y, x) {
            guard y.epoch == current || inv.entries[r]?.contains(y.id) == true else { continue }
            if own, y.write.dot.device != me.installation
                || (y.write.dot.millis, y.write.dot.counter) <= (x.write.dot.millis, x.write.dot.counter) { continue }
            if try store.onFeed(y.id) { return true }
        }
        return false
    }

    /// Cuts the roster in each channel a later epoch replaced down to that epoch's own statements, from the one that
    /// started it to the announcement of the next (§10.7): a device that was away holds the chain up to its last head,
    /// in that epoch or an earlier one, and needs no more from the channel; a device that holds nothing gets the whole
    /// chain in the current channel. Done by the owner's devices, in a list that is neither deleted nor dead.
    func trimRosters(_ cid: Bytes, me: Me) async throws {
        guard let c = collections[cid], let head = c.head, c.statements[head]?.statement.deleted == false, !c.dead,
              c.owner == me.participant, registry.activity(me.certificate).isValid, let current = c.epoch(of: head) else { return }
        let line = chain(c, to: head)
        for (epoch, key) in c.keys.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) })
        where epoch != current && epoch != c.genesis && c.counts(epoch) {
            // The epoch's own statements, and the one that started the next.
            var segment: [Write] = [], inside: Set<Bytes> = []
            for (i, id) in line.enumerated() where c.epoch(of: id) == epoch {
                segment.append(c.statements[id]!.write); inside.insert(id)
                if i + 1 < line.count, c.epoch(of: line[i + 1]) != epoch {
                    segment.append(c.statements[line[i + 1]]!.write); inside.insert(line[i + 1])
                }
            }
            guard !segment.isEmpty else { continue }
            let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
            var extra: [String] = [], exact = false
            for (id, _) in try store.merged(channel: tag) {
                let contents = try store.contents(message: id)
                guard let first = contents.first, first.record == Roster.record else { continue }
                if contents.allSatisfy({ inside.contains($0.write) }) {
                    if contents.count == segment.count { exact = true }
                } else {
                    extra.append(id)
                }
            }
            guard !extra.isEmpty else { continue }
            if !exact { try await publish(cid: cid, epoch: epoch, record: Roster.record, writes: segment) }
            try await hub.destroy(ids: extra)
            try store.forgetMessages(extra)
        }
    }

    /// Packs the states of deleted records that each have a message of their own in the current channel, once there are
    /// more than the threshold of them (§10.7). The single messages are then dominated, and the next clean-up collects
    /// them. Done by the owner's devices.
    func packTombstones(_ cid: Bytes, me: Me) async throws {
        guard let c = collections[cid], let head = c.head, c.statements[head]?.statement.deleted == false, !c.dead,
              c.owner == me.participant, registry.activity(me.certificate).isValid,
              let (epoch, _) = dropEpoch(cid), let key = c.keys[epoch] else { return }
        let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
        var single = Set<Bytes>(), packed = Set<Bytes>()
        for (id, _) in try store.merged(channel: tag) {
            let records = Set(try store.contents(message: id).map(\.record))
            for rid in records {
                if records.count > 1 { packed.insert(rid) }
                else if rid != Roster.record, c.records[rid]?.deleted == true { single.insert(rid) }
            }
        }
        var todo = single.subtracting(packed).sorted { $0.lexicographicallyPrecedes($1) }
            .map { RecordState(record: $0, writes: c.records[$0]!.kept.map(\.write)) }
        guard todo.count > packThreshold else { return }
        while todo.count >= 2 {
            var n = todo.count
            while n > 2, (try? packPayload(Array(todo[..<n])))?.count ?? Int.max > Self.maxPackBytes { n /= 2 }
            try await publishPack(key: key, tag: tag, Array(todo[..<n]))
            todo.removeFirst(n)
        }
    }

    /// Appends a pack and notes it as this device's own, applied message holding each record's writes.
    func publishPack(key: Bytes, tag: String, _ states: [RecordState]) async throws {
        let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 1, tag: tag, plaintext: try packPayload(states))
        let id = try await hub.append(channel: tag, body: envelope)
        try store.transaction {
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
            try store.markApplied(id)
            for s in states { try store.noteContents(message: id, record: s.record, writes: try s.writes.map { try $0.id() }) }
        }
    }

    /// Whether a gone list's replica was collected here.
    func purged(_ cid: Bytes) -> Bool { (try? store.value("gone.purged." + cid.hex)) ?? nil != nil }

    /// Collects a gone list once the retention period has passed since this device first saw it gone.
    func collectGone(_ cid: Bytes, me: Me) async throws {
        guard let c = collections[cid], let head = c.head, let st = c.statements[head]?.statement else { return }
        let deleted = st.deleted, removed = !deleted && removed(from: c, me: me)
        let seenName = "gone.seen." + cid.hex, purgedName = "gone.purged." + cid.hex
        guard deleted || removed else {
            // Back in the list (added again): nothing of this is collected.
            try store.removeValue(seenName)
            try store.removeValue(purgedName)
            return
        }
        let now = Int64(Date().timeIntervalSince1970 * 1000)
        var seen = now
        if let v = try store.value(seenName), v.count == 8 {
            seen = Int64(bitPattern: v.reduce(UInt64(0)) { $0 << 8 | UInt64($1) })
        } else {
            var t = Bytes(), n = UInt64(now)
            for _ in 0..<8 { t.insert(UInt8(n & 0xff), at: 0); n >>= 8 }
            try store.setValue(seenName, t)
        }
        let ms = retention.components.seconds * 1000 + retention.components.attoseconds / 1_000_000_000_000_000
        guard retention >= .zero, now - seen >= ms else { return }
        if !purged(cid) { try purgeReplica(cid) }
        if deleted, c.owner == me.participant, registry.activity(me.certificate).isValid { try await purgeHub(c) }
    }

    /// Drops a gone list's records, everything but its roster, and what was kept for them.
    func purgeReplica(_ cid: Bytes) throws {
        guard let c = collections[cid] else { return }
        let rows = try store.writeIDs(scope: cid).filter { $0.record != Roster.record }
        let unshared = try store.values(prefix: "unshared." + cid.hex + ".").map(\.0)
        let ups = try store.uploads().filter { $0.scope == cid }
        try store.transaction {
            try store.dropWrites(rows.map(\.id))
            for r in rows { try store.removeValue("resigned." + r.id.hex) }
            for name in unshared { try store.removeValue(name) }
            try store.removeValue("blobs.keep." + cid.hex)
            for u in ups { try dropUpload(u) }
            try store.setValue("gone.purged." + cid.hex, [1])
        }
        let changedRecords = Array(c.records.keys)
        collections[cid]?.records = [:]
        changed(.items(list: cid, records: changedRecords))
    }

    /// Destroys a deleted list's data on the hub (§11.9): every message this device merged in any of its channels that
    /// carries no roster write, and its inventory fragments. The roster stays, so that a device returning late learns
    /// the list was deleted. A message this device hasn't merged stays (§10.7). The list's file chunks go in the blob
    /// pass (BlobGC.swift).
    func purgeHub(_ c: CollectionState) async throws {
        var doomed: [String] = []
        for tag in listTags(c) {
            for id in try store.messages(channel: tag) {
                if try store.value("fragmsg." + id) != nil { doomed.append(id); continue }
                let contents = try store.contents(message: id)
                if !contents.isEmpty, !contents.contains(where: { $0.record == Roster.record }) { doomed.append(id) }
            }
        }
        if !doomed.isEmpty { try await hub.destroy(ids: doomed) }
    }

    /// The tag of every channel of a list this device knows, any epoch.
    func listTags(_ c: CollectionState) -> [String] {
        var out: [String] = [], seen: Set<String> = []
        func add(_ epoch: ID, _ key: Bytes) {
            let t = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
            if seen.insert(t).inserted { out.append(t) }
        }
        for (e, k) in c.keys.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) { add(e, k) }
        for (e, ks) in c.readKeys.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            for k in ks.sorted(by: { $0.lexicographicallyPrecedes($1) }) { add(e, k) }
        }
        return out
    }

    /// The lists whose replicas were collected here; the app shows none of them.
    public func goneLists() throws -> [Bytes] {
        try store.values(prefix: "gone.purged.").compactMap { Bytes(hex: String($0.0.dropFirst("gone.purged.".count))) }
    }
}
