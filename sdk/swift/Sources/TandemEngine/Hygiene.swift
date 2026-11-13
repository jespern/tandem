import Foundation
import Tandem

// Keeping the hub small and whole (§10.7, §12.2): after a sync, in each current channel this device follows,
// destroy messages another message dominates, and publish again a record whose messages on the hub, taken together,
// no longer cover this device's state. Only messages this device merged count; the roster and inventory fragments
// are never collected; nothing happens in a channel a later epoch replaced.

extension Device {
    /// One pass over every current channel. Called after each sync.
    func hygiene() async throws {
        guard let me, registry.activity(me.certificate).isValid else { return }
        for (cid, c) in collections.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            guard following(cid), !c.dead, let head = c.head, let epoch = c.epoch(of: head), let key = c.keys[epoch],
                  epoch == c.genesis || inventory(epoch) != nil, c.statements[head]?.statement.deleted == false else { continue }
            let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
            try await healRoster(cid, c: c, head: head, epoch: epoch, tag: tag)
            if epoch != c.genesis { try await healFragments(channel: tag, epoch: epoch, key: key) }
            try await healAnnouncements(cid, c: c)
            try await tidy(channel: tag, scope: cid, records: c.records, entry: { w in
                guard let s = c.statements[w.ref], let source = c.epoch(of: w.ref) else { return nil }
                return try? Entry(w, epoch: source, epochNumber: s.statement.keyN)
            }, republish: { record, writes in
                try await self.publish(cid: cid, epoch: epoch, record: record, writes: writes)
            })
        }
        try await healGrants()
        try await finishPendingGrants()
        for epoch in homeHeads {
            guard let key = homeKeys[epoch], let scope = homeScope else { continue }
            let tag = Tandem.homeTag(key: key, hub: pin.hub, uid: me.participant, epoch: epoch)
            if homeEpochs[epoch]?.fields["parents"] != .set([]) { try await healFragments(channel: tag, epoch: epoch, key: key) }
            let epochs = homeEpochs
            try await tidy(channel: tag, scope: scope, records: home, entry: { w in
                try? Entry(w, epoch: w.ref, epochNumber: Self.number(w.ref, in: epochs))
            }, republish: { record, writes in
                try await self.publishHome(record, epoch: epoch, writes: writes)
            })
        }
    }

    /// The last grant this device sent to each keychain of each current member is restored if the hub lost it; one
    /// to someone no longer listed, or to a revoked keychain, is destroyed and forgotten (§11.4).
    func healGrants() async throws {
        let prefix = "kept.grant."
        for (name, envelope) in try store.values(prefix: prefix) {
            let parts = name.dropFirst(prefix.count).split(separator: ".").map(String.init)
            guard parts.count == 3, let cid = Bytes(hex: parts[0]), let participant = Bytes(hex: parts[1]),
                  let cert = Bytes(hex: parts[2]) else { continue }
            let listed = collections[cid].flatMap { c in c.head.flatMap { c.statements[$0]?.statement } }
                .map { $0.members.contains { $0.uid == participant } && !$0.deleted } ?? false
            let on = try store.messages(withHash: H(envelope))
            guard listed, registry.activity(cert).isValid else {
                if !on.isEmpty { try await hub.destroy(ids: on); try store.forgetMessages(on) }
                try store.removeValue(name)
                continue
            }
            guard on.isEmpty else { continue }
            let inbox = Tandem.inboxTag(uid: participant, hub: pin.hub)
            let id = try await hub.append(channel: inbox, body: envelope)
            try store.noteMessage(id: id, channel: inbox, hash: H(envelope))
            try store.markApplied(id)
        }
    }

    /// The roster stays whole in the current channel (§12.2): every statement of the line from the genesis to the head
    /// must be in some message there, or the line is published again, so a new member's device can check it.
    func healRoster(_ cid: Bytes, c: CollectionState, head: ID, epoch: ID, tag: String) async throws {
        var present = Set<Bytes>()
        for (id, _) in try store.merged(channel: tag) {
            for (record, write) in try store.contents(message: id) where record == Roster.record { present.insert(write) }
        }
        let line = chain(c, to: head)
        guard !line.allSatisfy(present.contains) else { return }
        try await publish(cid: cid, epoch: epoch, record: Roster.record, writes: line.map { c.statements[$0]!.write })
    }

    /// An epoch's inventory stays on the hub while the epoch is current (§10.7, §12.2): published again if no fragment
    /// message is left in its channel.
    func healFragments(channel tag: String, epoch: ID, key: Bytes) async throws {
        guard let inv = inventory(epoch) else { return }
        for m in try store.messages(channel: tag) where try store.value("fragmsg." + m) != nil { return }
        for fragment in try Self.fragments(inv, epoch: epoch) {
            let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 1, tag: tag, plaintext: fragment)
            let id = try await hub.append(channel: tag, body: envelope)
            try store.transaction {
                try store.noteMessage(id: id, channel: tag, hash: H(envelope))
                try store.markApplied(id)
                try store.setValue("fragmsg." + id, epoch)
            }
        }
    }

    /// Every earlier epoch's channel keeps the announcement of the epoch after it (§11.6 step 6, §12.2): it is how a
    /// device that was away finds out where the list went. Restored if it was destroyed.
    func healAnnouncements(_ cid: Bytes, c: CollectionState) async throws {
        for start in c.announced where c.startsEpoch(start) {
            guard let s = c.statements[start], let old = c.epoch(of: s.write.ref), let key = c.keys[old] else { continue }
            let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: old)
            var present = false
            for (id, _) in try store.merged(channel: tag) where try store.contents(message: id).contains(where: { $0.write == start }) {
                present = true
            }
            if !present { try await publish(cid: cid, epoch: old, record: Roster.record, writes: [s.write]) }
        }
    }

    /// Collect and heal one channel. `records` is this device's state of the scope; `entry` places a stored write.
    func tidy(channel tag: String, scope: Bytes, records: [Bytes: Record], entry: (Write) -> Entry?,
              republish: (Bytes, [Write]) async throws -> Void) async throws {
        var byRecord: [Bytes: [(id: String, hash: Bytes, writes: Set<Bytes>)]] = [:]
        for (id, hash) in try store.merged(channel: tag) {
            let contents = try store.contents(message: id)
            guard let record = contents.first?.record, record != Roster.record else { continue }   // roster and fragments stay
            byRecord[record, default: []].append((id, hash, Set(contents.map(\.write))))
        }
        var doomed: [String] = []
        // A record with no message left at all is healed too.
        for record in records.keys where record != Roster.record && byRecord[record] == nil && !(records[record]?.kept.isEmpty ?? true) {
            byRecord[record] = []
        }
        for (record, messages) in byRecord.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            // Every write the messages name, as this device placed it when it merged it.
            var entries: [Bytes: Entry] = [:]
            for row in try store.writes(scope: scope, record: record) {
                guard case let .record(state) = try Kind1.parse(row.payload, hub: pin.hub, scope: scope), let w = state.writes.first,
                      let e = entry(w) else { continue }
                entries[e.id] = e
            }
            func dominated(_ m: Set<Bytes>, by n: Set<Bytes>) -> Bool {
                m.allSatisfy { w in
                    n.contains(w) || (entries[w].map { x in n.contains { entries[$0].map { supersedes($0, x) } ?? false } } ?? false)
                }
            }
            var gone = Set<String>()
            for m in messages where messages.count > 1 {
                for n in messages where n.id != m.id && !gone.contains(n.id) {
                    guard dominated(m.writes, by: n.writes) else { continue }
                    // Of two that dominate each other, only the one with the greater envelope hash goes (§10.7).
                    if dominated(n.writes, by: m.writes) && !n.hash.lexicographicallyPrecedes(m.hash) { continue }
                    gone.insert(m.id)
                    break
                }
            }
            doomed += gone.sorted()
            // Healing (§12.2): the messages left, taken together, must cover this device's state of the record.
            guard let state = records[record] else { continue }
            let left = messages.filter { !gone.contains($0.id) }.reduce(into: Set<Bytes>()) { $0.formUnion($1.writes) }
            let covered = state.kept.allSatisfy { k in
                left.contains(k.id) || left.contains { entries[$0].map { supersedes($0, k) } ?? false }
            }
            // Concurrent publishers (§10.7): when no single message left holds this device's state, publish the merged
            // state, which then dominates the others. Two devices doing it at once are settled by the hash rule.
            let single = messages.filter { !gone.contains($0.id) }.contains { m in
                state.kept.allSatisfy { k in m.writes.contains(k.id) || m.writes.contains { entries[$0].map { supersedes($0, k) } ?? false } }
            }
            if !covered || (!single && messages.count - gone.count > 1) { try await republish(record, state.kept.map(\.write)) }
        }
        guard !doomed.isEmpty else { return }
        try await hub.destroy(ids: doomed)
        try store.forgetMessages(doomed)
    }
}
