import Foundation
import Tandem

// A participant's home (§6.2): its epochs, their keys, and the named records every device of that
// participant shares. Writes are signed by an admitted keychain of the participant and merged as §9 says.

extension Device {
    var homeScope: Bytes? { me.map { Tandem.homeScope(hub: pin.hub, uid: $0.participant) } }

    /// Home epochs no announced epoch names as a parent (§10.8).
    var homeHeads: [ID] { Self.heads(homeEpochs, keys: homeKeys) }

    static func heads(_ epochs: [ID: ControlObject], keys: [ID: Bytes]) -> [ID] {
        let named = Set(epochs.values.flatMap { e -> [ID] in
            guard case let .set(p)? = e.fields["parents"] else { return [] }
            return p
        })
        return epochs.keys.filter { !named.contains($0) && keys[$0] != nil }.sorted { $0.lexicographicallyPrecedes($1) }
    }

    /// The epoch this device writes to: the smallest current head (§10.8).
    var currentHomeEpoch: ID? { homeHeads.first }

    func epochNumber(_ epoch: ID) -> UInt64 { Self.number(epoch, in: homeEpochs) }

    static func number(_ epoch: ID, in epochs: [ID: ControlObject]) -> UInt64 {
        if case let .int(n)? = epochs[epoch]?.fields["n"] { return n }
        return 0
    }

    // MARK: Inbox (§11.4, §10.8)

    func applyInbox(_ m: Message, _ me: Me) throws -> Outcome {
        guard let plaintext = try? Envelope.openInbox(hub: pin.hub, uid: me.participant, key: me.keys.agreement, envelope: m.body),
              let json = try? JSON.parse(plaintext), case let .string(type)? = json["type"]
        else { return .ignored }   // sealed to another of this participant's keychains, or malformed
        switch String(decoding: type, as: UTF8.self) {
        case "home-key":
            guard let n = try? Notice.parse(plaintext, type: "home-key", fields: ["epoch", "k"]),
                  let epoch = n["epoch"], let key = n["k"], key.count == 32 else { return .ignored }
            if homeKeys[epoch] == key { return .ignored }
            // The key must be the one the epoch's start commits to; until the start arrives, hold it.
            if let start = homeEpochs[epoch] {
                guard start.fields["key"] == .id(Tandem.commit(key)) else { return .ignored }
            }
            homeKeys[epoch] = key
            try store.setValue("home.key." + epoch.hex, key)
            try moveHome()
            return .applied
        case "grant":
            return try receiveGrant(plaintext, json, me: me)
        case "answer":
            return try receiveAnswer(json)
        case "pair-claim":
            return try receiveClaim(plaintext, json)
        case "pair-release":
            return try receiveRelease(plaintext, me: me)
        default:
            return .ignored
        }
    }

    // MARK: Home channels

    func applyHome(_ m: Message, epoch: ID, key: Bytes, me: Me) throws -> Outcome {
        guard let (kind, plaintext) = try? Envelope.open(key: key, hub: pin.hub, tag: m.channel, envelope: m.body)
        else { return .ignored }
        switch kind {
        case 3:
            guard let o = try? ControlObject.parse(plaintext, expect: "home-epoch") else { return .ignored }
            return try acceptHomeEpoch(o, me: me)
        case 1:
            guard let scope = homeScope, let parsed = try? Kind1.parse(plaintext, hub: pin.hub, scope: scope) else { return .ignored }
            guard case let .record(state) = parsed else {
                guard case let .inventory(f) = parsed else { return .ignored }
                guard case let .id(commitment)? = homeEpochs[epoch]?.fields["snapshot"] else { return .waiting }
                guard try receiveFragment(f, epoch: epoch, commitment: commitment) else { return .ignored }
                try moveHome()
                return .applied
            }
            var entries: [Entry] = []
            for w in state.writes {
                switch homeWriteValidity(w, epoch: epoch, me: me) {
                case .valid: entries.append(try Entry(w, epoch: w.ref, epochNumber: epochNumber(w.ref)))
                case .pending: return .waiting
                case .invalid: continue   // dropped; the rest of the message still counts
                }
            }
            guard !entries.isEmpty else { return .ignored }
            try merge(entries, record: state.record, scope: scope)
            return .applied
        default:
            return .ignored
        }
    }

    /// A home-epoch start for this participant, signed by an admitted keychain of it under its snapshot (§10.8).
    func acceptHomeEpoch(_ o: ControlObject, me: Me) throws -> Outcome {
        guard o.fields["hub"] == .id(pin.hub), o.fields["genesis"] == .id(pin.genesis),
              o.fields["participant"] == .id(me.participant), case let .id(signer)? = o.fields["signer"],
              case let .id(auth)? = o.fields["auth"] else { return .ignored }
        let id = try o.id()
        if homeEpochs[id] != nil { return .ignored }
        switch registry.authorizes(snapshot: auth, certificate: signer) {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        guard let cert = registry.certificate(signer), o.verify("sig", publicKey: cert.sign) else { return .ignored }
        if let key = homeKeys[id], o.fields["key"] != .id(Tandem.commit(key)) { return .ignored }
        homeEpochs[id] = o
        try store.putObject(o)
        try moveHome()
        return .applied
    }

    /// Whether `w` is a valid home write in the channel of `epoch` (§6.2, §8.3): by this participant, under an admitted
    /// keychain and its snapshot, signed; and either written in this very epoch under a snapshot extending the epoch's
    /// policy, or written earlier and listed in this epoch's inventory.
    func homeWriteValidity(_ w: Write, epoch: ID, me: Me) -> Validity {
        guard w.author == me.participant, w.hub == pin.hub else { return .invalid("not this participant's home write") }
        guard let start = homeEpochs[epoch], case let .id(floor)? = start.fields["auth"] else { return .pending([epoch]) }
        var ok = registry.authorizes(snapshot: w.authorization, certificate: w.certificate)
        if w.ref == epoch {
            ok = Validity.all([ok, registry.extends(w.authorization, floor: floor), lateness(w, epoch: epoch)])
        } else {
            guard let inv = inventory(epoch) else { return .pending([epoch]) }
            guard let id = try? w.id(), inv.entries[w.record]?.contains(id) == true else { return .invalid("not carried into this epoch") }
            ok = Validity.all([ok, homeAncestry(w.ref, of: epoch)])
        }
        guard ok.isValid else { return ok }
        guard let cert = registry.certificate(w.certificate), cert.participant == me.participant,
              (try? w.verify(publicKey: cert.sign)) != nil else { return .invalid("bad signature") }
        return .valid
    }

    /// Home epochs that name `epoch` as a parent.
    func homeChildren(_ epoch: ID) -> [ID] {
        homeEpochs.filter { e in
            guard case let .set(p)? = e.value.fields["parents"] else { return false }
            return p.contains(epoch)
        }.map(\.key)
    }

    /// A write in an epoch that a later one replaced counts only if a later inventory carries it (§8.3, §11.6):
    /// what arrives late in an old channel, a revoked device's included, doesn't enter the current home.
    func lateness(_ w: Write, epoch: ID) -> Validity {
        let children = homeChildren(epoch)
        guard !children.isEmpty, let id = try? w.id() else { return .valid }
        var missing: [ID] = []
        for c in children {
            guard let inv = inventory(c) else { missing.append(c); continue }
            if inv.entries[w.record]?.contains(id) == true { return .valid }
        }
        return missing.isEmpty ? .invalid("late write in a replaced home epoch") : .pending(missing)
    }

    /// Whether an entry belongs to the current home: written in a current head, or carried by a head's inventory.
    static func homeEligible(_ entry: Entry, heads: [ID], inventories: [ID: Inventory]) -> Bool {
        heads.contains(entry.epoch) || heads.contains { inventories[$0]?.entries[entry.write.record]?.contains(entry.id) == true }
    }

    /// Move to the current heads (§11.6, "Moving over"): rebuild every home record from the stored writes that the
    /// heads keep. Waits while a head's inventory is incomplete.
    func moveHome() throws {
        guard let scope = homeScope else { return }
        let heads = homeHeads
        // With no head it holds the key of (a revoked device), the replica stays as it was, read-only.
        guard !heads.isEmpty, heads.allSatisfy({ inventory($0) != nil || homeEpochs[$0]?.fields["parents"] == .set([]) }) else { return }
        for record in try store.records(scope: scope) {
            home[record] = try Self.loadRecord(store: store, hub: pin.hub, scope: scope, record: record, epochs: homeEpochs,
                                               heads: heads, inventories: inventories)
        }
    }

    func merge(_ entries: [Entry], record: ID, scope: Bytes) throws {
        var r = try home[record] ?? Record(hub: pin.hub, scope: scope, id: record)
        try r.merge(entries)
        try store.transaction {
            for e in entries {
                try store.putWrite(id: e.id, scope: scope, record: record, epoch: e.epoch,
                                   payload: try recordPayload(record: record, writes: [e.write]))
            }
        }
        home[record] = r
    }

    /// A record as the store holds it: every write kept, merged again.
    static func loadRecord(store: Store, hub: Bytes, scope: Bytes, record: ID, epochs: [ID: ControlObject],
                           heads: [ID]? = nil, inventories: [ID: Inventory] = [:]) throws -> Record {
        var r = try Record(hub: hub, scope: scope, id: record)
        let entries = try store.writes(scope: scope, record: record).compactMap { row -> Entry? in
            guard case let .record(state) = try Kind1.parse(row.payload, hub: hub, scope: scope), let w = state.writes.first
            else { return nil }
            let e = try Entry(w, epoch: row.epoch, epochNumber: number(row.epoch, in: epochs))
            if let heads, !homeEligible(e, heads: heads, inventories: inventories) { return nil }
            return e
        }
        try r.merge(entries)
        return r
    }

    // MARK: Writing home records

    /// The next dot of this installation's clock, persisted before the write can leave the device (§9.1).
    func nextDot(_ me: Me) throws -> Dot {
        let held = try store.value("clock") ?? Bytes(repeating: 0, count: 10)
        let millis = held[0..<8].reduce(UInt64(0)) { $0 << 8 | UInt64($1) }
        let counter = UInt16(held[8]) << 8 | UInt16(held[9])
        let now = UInt64(Date().timeIntervalSince1970 * 1000)
        let next = try nextClock(millis: millis, counter: counter, now: now)
        var out = Bytes()
        for s in stride(from: 56, through: 0, by: -8) { out.append(UInt8(next.millis >> UInt64(s) & 0xff)) }
        out += [UInt8(next.counter >> 8), UInt8(next.counter & 0xff)]
        try store.setValue("clock", out)
        return Dot(device: me.installation, millis: next.millis, counter: next.counter)
    }

    /// Sign one home write on `name` and publish it in the current home epoch.
    func writeHome(_ name: String, field: String, op: Op, value: Bytes = [], element: Bytes? = nil) async throws {
        guard let me, let scope = homeScope, let epoch = currentHomeEpoch, let key = homeKeys[epoch]
        else { throw TandemError.invalid("device: no home to write to") }
        let record = Tandem.namedID(Bytes(name.utf8))
        var writes: [Write] = []
        if home[record]?.value(Bytes("_name".utf8)) == nil && field != "_name" {
            writes.append(try sign(me: me, scope: scope, record: record, epoch: epoch, field: "_name", op: .assign,
                                   value: Bytes(name.utf8), element: nil, after: writes))
        }
        writes.append(try sign(me: me, scope: scope, record: record, epoch: epoch, field: field, op: op, value: value,
                               element: element, after: writes))
        try merge(try writes.map { try Entry($0, epoch: epoch, epochNumber: epochNumber(epoch)) }, record: record, scope: scope)
        let tag = Tandem.homeTag(key: key, hub: pin.hub, uid: me.participant, epoch: epoch)
        let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 1, tag: tag,
                                         plaintext: try recordPayload(record: record, writes: writes))
        let id = try await hub.append(channel: tag, body: envelope)
        try store.noteMessage(id: id, channel: tag, hash: H(envelope))
        try store.markApplied(id)
    }

    private func sign(me: Me, scope: Bytes, record: ID, epoch: ID, field: String, op: Op, value: Bytes, element: Bytes?,
                      after earlier: [Write]) throws -> Write {
        var r = try home[record] ?? Record(hub: pin.hub, scope: scope, id: record)
        if !earlier.isEmpty { try r.merge(try earlier.map { try Entry($0, epoch: epoch, epochNumber: epochNumber(epoch)) }) }
        let v = try r.vectors(op: op, field: Bytes(field.utf8), element: element, epoch: epoch, epochNumber: epochNumber(epoch))
        var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: scope, record: record,
                      field: Bytes(field.utf8), op: op, element: element, value: value, author: me.participant,
                      dot: try nextDot(me), ref: epoch, context: v.context, base: v.base)
        try w.sign(with: me.keys.signing)
        return w
    }

    /// Set a field of a home record (§6.2). `settings:<app>` records are the app's own.
    public func setHome(_ name: String, field: String, value: Bytes) async throws {
        try await writeHome(name, field: field, op: .assign, value: value)
    }

    /// Add an element to a set field of a home record.
    public func addHome(_ name: String, field: String, element: Bytes) async throws {
        try await writeHome(name, field: field, op: .add, element: element)
    }

    /// A home record's field: its value if it has one, nil if unset. Concurrent values are siblings; this is the first.
    public func homeValue(_ name: String, field: String) -> Bytes? {
        home[Tandem.namedID(Bytes(name.utf8))]?.value(Bytes(field.utf8))
    }

    public func homeElements(_ name: String, field: String) -> [Bytes] {
        home[Tandem.namedID(Bytes(name.utf8))]?.elements(Bytes(field.utf8)) ?? []
    }
}
