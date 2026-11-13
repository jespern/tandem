import Foundation
import Tandem

// One store, several processes (Liszto DESIGN §5.6): the app's running Device and its extensions open the same SQLite
// file. An extension's write lands in the store; the app's Device notices with PRAGMA data_version, which changes only
// when another connection commits, reloads what changed and announces it. A write the extension couldn't send then goes
// out with the app's next sync (healing, §12.2).

extension Device {
    /// Take in what another process (an app extension) saved to this store, then sync, so an edit it couldn't send goes
    /// out now. Call it when an extension says it wrote, for instance by a Darwin notification. The same check also runs
    /// by itself at the start of every sync, write and read, so a missed call costs nothing but time.
    public func refresh() async throws {
        if try takeExternalChanges() { try await sync() }
    }

    /// Reload the records another connection changed since this device last looked, and announce them. Cheap when
    /// nothing changed: one pragma.
    @discardableResult
    func takeExternalChanges() throws -> Bool {
        let version = try store.dataVersion()
        guard version != seenDataVersion else { return false }
        seenDataVersion = version
        var any = false
        for (cid, c) in collections {
            var updated = c
            for record in try store.records(scope: cid) where record != Roster.record {
                let fresh = try Self.loadData(store: store, hub: pin.hub, c: c, record: record, inventories: inventories)
                guard Set(fresh.kept.map(\.id)) != Set(c.records[record]?.kept.map(\.id) ?? []) else { continue }
                updated.records[record] = fresh
                changed(.items(list: cid, records: [record]))
                any = true
            }
            collections[cid] = updated
        }
        if let scope = homeScope {
            let heads = homeHeads
            for record in try store.records(scope: scope) {
                let fresh = try Self.loadRecord(store: store, hub: pin.hub, scope: scope, record: record, epochs: homeEpochs,
                                                heads: heads.isEmpty ? nil : heads, inventories: inventories)
                guard Set(fresh.kept.map(\.id)) != Set(home[record]?.kept.map(\.id) ?? []) else { continue }
                home[record] = fresh
                changed(.home)
                any = true
            }
            if any { refreshMemberships() }
        }
        if any { flushEvents() }
        return any
    }
}

/// Writes one record of one list without a running `Device`: for app extensions (share sheet, quick add, Shortcuts,
/// widgets), which live under tight memory limits. Opens the app's store and keys and loads only what one write needs:
/// the list's roster, its current key, this participant's role and the record. Signs every change in one call, saves
/// them, and makes one attempt to put the record on the hub with a short timeout. Whatever doesn't reach the hub stays
/// in the store, and the app's `Device` sends it (`refresh()`, or its next sync).
public struct ItemWriter: Sendable {
    /// One change to a field of the record.
    public enum Change: Sendable {
        case set(String, Bytes)
        case add(String, Bytes)
        case remove(String, Bytes)
        case delete
    }

    /// Why a write can't be made here; the extension then saves to the app's own store and the app writes later.
    public enum Refusal: Error, Equatable {
        /// The store isn't on a hub yet, or this keychain hasn't been admitted.
        case notOnHub
        /// This device has no key for the list's current epoch yet.
        case noKey
        /// This participant can't write to the list (a reader, removed, or `_meta` without being the owner).
        case cannotWrite
        /// A key change of this list is in flight, or the owner must change its key after a revocation.
        case changePending
        /// The list was deleted, or can't sync any more.
        case closed
    }

    let store: Store, pin: HubPin, me: Me
    let list: CollectionState, epoch: ID, key: Bytes, epochNumber: UInt64, inventories: [ID: Inventory], role: Statement.Role

    /// Load what writing to `list` needs, and nothing else.
    public static func open(store: Store, keys: DeviceKeys, list cid: Bytes) throws -> ItemWriter {
        guard let genesisID = try store.value("pin.genesis"),
              let genesis = try store.objects(kind: "genesis").first(where: { (try? $0.id()) == genesisID }),
              let participant = try store.value("me.participant"), let certificate = try store.value("me.certificate"),
              let auth = try store.value("me.auth"), let installation = try store.value("me.installation")
        else { throw Refusal.notOnHub }
        let pin = try HubPin(genesis: genesis)
        let me = Me(keys: keys, participant: participant, certificate: certificate, auth: auth, installation: installation)
        guard Bytes(keys.signing.publicKey.rawRepresentation) == (try? Self.certificateKey(store: store, pin: pin, id: certificate))
        else { throw Refusal.notOnHub }

        let homeScope = Tandem.homeScope(hub: pin.hub, uid: participant)
        let epochs = Dictionary(uniqueKeysWithValues: try store.objects(kind: "home-epoch").map { (try $0.id(), $0) })
        let membership = try Device.loadRecord(store: store, hub: pin.hub, scope: homeScope,
                                               record: Tandem.namedID(Bytes(("membership:" + B64u.encode(cid)).utf8)), epochs: epochs)
        if membership.value(Bytes("flight".utf8)).flatMap(FlightNote.parse) != nil { throw Refusal.changePending }
        let c = try Device.loadRoster(store: store, hub: pin.hub, cid: cid, membership: membership)
        guard !c.dead, let head = c.head, let s = c.statements[head]?.statement, !s.deleted else { throw Refusal.closed }
        guard let epoch = c.epoch(of: head), let key = c.keys[epoch] else { throw Refusal.noKey }
        guard let role = s.members.first(where: { $0.uid == participant })?.role, role != .reader else { throw Refusal.cannotWrite }

        var registry = Registry(pin: pin)
        for o in try store.objects() where !["genesis", "capsule", "home-epoch", "receipt"].contains(o.kind) { _ = try? registry.add(o) }
        guard Device.exposure(of: s, registry: registry).isEmpty else { throw Refusal.changePending }
        var inventories: [ID: Inventory] = [:]
        if let bytes = try store.value("inv." + epoch.hex) { inventories[epoch] = try Inventory.parse(bytes) }
        return ItemWriter(store: store, pin: pin, me: me, list: c, epoch: epoch, key: key, epochNumber: s.keyN,
                          inventories: inventories, role: role)
    }

    static func certificateKey(store: Store, pin: HubPin, id: ID) throws -> Bytes? {
        var registry = Registry(pin: pin)
        for o in try store.objects(kind: "certificate") where (try? o.id()) == id { _ = try registry.add(o) }
        return registry.certificate(id)?.sign
    }

    /// Sign `changes` to `record`, save them, and try once to put the record's state on `hub` within `timeout`.
    /// Returns whether it reached the hub; when it didn't, the app's `Device` sends it. With `hub` nil, only saves.
    @discardableResult
    public func write(record: Bytes, _ changes: [Change], hub: (any PushStore)?, timeout: Duration = .seconds(5)) async throws -> Bool {
        guard record != Roster.record, record != Device.meta || role == .owner else { throw Refusal.cannotWrite }
        guard let head = list.head else { throw Refusal.closed }
        var r = try Device.loadData(store: store, hub: pin.hub, c: list, record: record, inventories: inventories)
        var writes: [Write] = []
        for change in changes {
            let (op, field, value, element): (Op, Bytes, Bytes, Bytes?) = switch change {
            case let .set(f, v): (.assign, Bytes(f.utf8), v, nil)
            case let .add(f, e): (.add, Bytes(f.utf8), [], e)
            case let .remove(f, e): (.remove, Bytes(f.utf8), [], e)
            case .delete: (.delete, [], [], nil)
            }
            let v = try r.vectors(op: op, field: field, element: element, epoch: epoch, epochNumber: epochNumber)
            var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: list.id, record: record,
                          field: field, op: op, element: element, value: value, author: me.participant,
                          dot: try Device.reserveDot(store: store, installation: me.installation), ref: head,
                          context: v.context, base: v.base)
            try w.sign(with: me.keys.signing)
            try r.merge([try Entry(w, epoch: epoch, epochNumber: epochNumber)])
            writes.append(w)
        }
        try store.transaction {
            for w in writes {
                try store.putWrite(id: try w.id(), scope: list.id, record: record, epoch: epoch,
                                   payload: try recordPayload(record: record, writes: [w]))
            }
        }
        guard let hub else { return false }
        let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
        let kept = r.kept.map(\.write)
        let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 1, tag: tag, plaintext: try recordPayload(record: record, writes: kept))
        let id: String? = try? await withThrowingTaskGroup(of: String?.self) { group in
            group.addTask { try await hub.append(channel: tag, body: envelope) }
            group.addTask { try await Task.sleep(for: timeout); return nil }   // allowed-timer: an extension's bounded wait
            defer { group.cancelAll() }
            return try await group.next() ?? nil
        }
        guard let id else { return false }
        try store.transaction {
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
            try store.markApplied(id)
            try store.noteContents(message: id, record: record, writes: try kept.map { try $0.id() })
        }
        return true
    }
}
