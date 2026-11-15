import CryptoKit
import Foundation
import Tandem

/// Who this installation is on the hub: its keychain's keys and the IDs they were admitted under (§5.3).
public struct Me: Sendable {
    public let keys: DeviceKeys
    public let participant: ID, certificate: ID, auth: ID
    /// The installation's own dot namespace; never synced (§5.3).
    public let installation: Bytes
}

/// One installation on one hub (§6). Everything it knows is in its `Store`; everything it shares goes
/// through its `PushStore`. Work happens on a push, a local action or `sync()`, never on a timer.
public actor Device {
    public nonisolated let pin: HubPin
    let store: Store
    let hub: any PushStore
    nonisolated let directoryKey: Bytes
    let keys: DeviceKeys?
    public internal(set) var registry: Registry
    public internal(set) var me: Me?
    /// Directory notices held: home-recovery boxes and the like.
    public internal(set) var notices: [Notice] = []
    /// Admin capsules held (§6.7), by ID.
    public internal(set) var capsules: [ID: ControlObject] = [:]
    /// Home epochs this device holds the key of, by epoch ID (§6.2, §10.8).
    var homeKeys: [ID: Bytes] = [:]
    /// Home keys not yet confirmed by their epoch's start, and starts held aside (`offerHomeKey`, `acceptHomeEpoch`).
    var homeCandidates: [ID: Set<Bytes>] = [:]
    var heldStarts: [ID: ControlObject] = [:]
    var homeEpochs: [ID: ControlObject] = [:]
    /// Assembled snapshot inventories, by epoch ID, for home and collections (§8.3).
    var inventories: [ID: Inventory] = [:]
    var home: [ID: Record] = [:]
    var collections: [Bytes: CollectionState] = [:]
    /// Grants delivered to this participant, by grant ID (§11.4).
    var offers: [ID: Offer] = [:]
    /// Pairing claims this admin device received, by request ID (§6.3).
    var claims: [ID: PairingClaim] = [:]
    /// Receipts from the directory, by ID (§10.8).
    var receipts: [ID: ControlObject] = [:]
    /// This device's own claim, while it waits for the admin (§6.3).
    var pairing: PendingPairing?
    /// Directory profiles, by record ID (§10.8).
    var profiles: [Bytes: Record] = [:]
    /// Event subscribers and what changed since the last flush (`events()`).
    var subscribers: [UUID: AsyncStream<DeviceEvent>.Continuation] = [:]
    var pendingEvents: [DeviceEvent] = []
    /// The sync in progress: syncs run one at a time.
    var syncing: Task<Int, any Error>?
    /// Key requests (§11.4): the wait before asking, and the one timer that waits for it.
    var keyRequestDelay: Duration = .seconds(600)
    var keyRequestTimer: Task<Void, Never>?
    /// The store's `data_version` when this device last took in other processes' writes (`takeExternalChanges`).
    var seenDataVersion: Int64 = 0
    /// Lists with a key change running on this device.
    var changing: Set<Bytes> = []
    /// Tests stop a key change here (§11.6's crash points).
    var crashPoint: String?

    var directoryTag: String { Tandem.directoryTag(directoryKey, hub: pin.hub) }
    var directoryChannelKey: Bytes { Tandem.directoryKey(directoryKey, hub: pin.hub) }

    init(pin: HubPin, directoryKey: Bytes, keys: DeviceKeys?, store: Store, hub: any PushStore) throws {
        let watch = PublishWatch()
        self.pin = pin; self.directoryKey = directoryKey; self.keys = keys; self.store = store
        self.hub = Watched(inner: hub, watch: watch)
        registry = Registry(pin: pin)
        for o in try store.objects() {
            switch o.kind {
            case "genesis": continue
            case "capsule": capsules[try o.id()] = o
            case "home-epoch": homeEpochs[try o.id()] = o
            case "receipt": receipts[try o.id()] = o
            default: try? registry.add(o)
            }
        }
        if let keys, let p = try store.value("me.participant"), let c = try store.value("me.certificate"),
           let a = try store.value("me.auth"), let i = try store.value("me.installation") {
            me = Me(keys: keys, participant: p, certificate: c, auth: a, installation: i)
        }
        for e in homeEpochs.keys { if let k = try store.value("home.key." + e.hex) { homeKeys[e] = k } }
        for (name, key) in try store.values(prefix: "home.cand.") {
            if let e = Bytes(hex: String(name.split(separator: ".")[2])) { homeCandidates[e, default: []].insert(key) }
        }
        for (name, bytes) in try store.values(prefix: "inv.") {
            if let e = Bytes(hex: String(name.dropFirst(4))), let inv = try? Inventory.parse(bytes) { inventories[e] = inv }
        }
        if let me {
            let scope = Tandem.homeScope(hub: pin.hub, uid: me.participant)
            let heads = Self.heads(homeEpochs, keys: homeKeys)
            for r in try store.records(scope: scope) {
                home[r] = try Self.loadRecord(store: store, hub: pin.hub, scope: scope, record: r, epochs: homeEpochs,
                                              heads: heads.isEmpty ? nil : heads, inventories: inventories)
            }
        }
        let dirScope = Control.directoryScope(hub: pin.hub)
        for record in try store.records(scope: dirScope) {
            var r = try Record(hub: pin.hub, scope: dirScope, id: record)
            try r.merge(try store.writes(scope: dirScope, record: record).compactMap { row -> Entry? in
                guard case let .record(state) = try Kind1.parse(row.payload, hub: pin.hub, scope: dirScope), let w = state.writes.first
                else { return nil }
                return try Entry(w, epoch: pin.genesis, epochNumber: 0)
            })
            profiles[record] = r
        }
        collections = try Self.loadCollections(store: store, hub: pin.hub, home: home, inventories: inventories)
        for (_, data) in try store.values(prefix: "claim.") {
            guard let j = try? JSON.parse(data), let c = try? PairingClaim.parse(j) else { continue }
            claims[c.id] = c
        }
        pairing = try store.value("pairing.pending").map(PendingPairing.parse)
        for (_, data) in try store.values(prefix: "offer.") {
            guard let j = try? JSON.parse(data), let g = j["grant"], let grant = try? ControlObject.parse(g, expect: "grant"),
                  let key = try? j.bytes("k", count: 32) else { continue }
            let o = Offer(grant: grant, key: key)
            offers[try grant.id()] = o
            // A list offered but not answered has no membership record in home, so loadCollections skipped it. Without
            // its roster the offer can't be checked again and is lost; without its records, accepting it shows an empty list.
            if collections[o.collection] == nil {
                collections[o.collection] = try Self.loadCollection(store: store, hub: pin.hub, cid: o.collection,
                    membership: try Record(hub: pin.hub, scope: o.collection, id: o.collection), inventories: inventories)
            }
            var c = collections[o.collection] ?? CollectionState(id: o.collection)
            c.offer(key: key, epoch: o.epoch)
            collections[o.collection] = c
        }
        seenDataVersion = try store.dataVersion()
        watch.device = self
    }

    /// Open a hub this device has the details of: its genesis (pinned at setup, invitation or pairing) and
    /// directory key. Nothing on the hub can replace the pin (§6.9). `keys` are the keychain's, if it has any yet.
    public static func open(genesis: ControlObject, directoryKey: Bytes, keys: DeviceKeys? = nil, store: Store,
                            hub: any PushStore) throws -> Device {
        let pin = try HubPin(genesis: genesis)
        guard case let .id(commit)? = genesis.fields["hubkey"], commit == Tandem.commit(directoryKey)
        else { throw TandemError.invalid("device: the directory key is not the genesis's") }
        try store.transaction {
            if let held = try store.value("pin.genesis") {
                guard held == pin.genesis else { throw TandemError.invalid("device: this store belongs to another hub") }
            } else {
                try store.setValue("pin.genesis", pin.genesis)
                try store.setValue("pin.hk", directoryKey)
                try store.putObject(genesis)
            }
        }
        return try Device(pin: pin, directoryKey: directoryKey, keys: keys, store: store, hub: hub)
    }

    /// Open the hub a store already belongs to, after a restart or in an app extension: its pinned genesis and
    /// directory key come from the store. Nil if the store is new.
    public static func reopen(store: Store, keys: DeviceKeys?, hub: any PushStore) throws -> Device? {
        guard let id = try store.value("pin.genesis"), let hk = try store.value("pin.hk"),
              let genesis = try store.objects(kind: "genesis").first(where: { (try? $0.id()) == id }) else { return nil }
        return try open(genesis: genesis, directoryKey: hk, keys: keys, store: store, hub: hub)
    }

    static func random(_ n: Int) -> Bytes { SymmetricKey(size: .init(bitCount: n * 8)).withUnsafeBytes { Bytes($0) } }

    /// Take on an identity this keychain was admitted under, with a fresh installation ID (§5.3).
    func adopt(participant: ID, certificate: ID, auth: ID) throws {
        guard let keys else { throw TandemError.invalid("device: no keychain keys") }
        let installation = try store.value("me.installation") ?? Self.random(8)
        try store.transaction {
            try store.setValue("me.participant", participant); try store.setValue("me.certificate", certificate)
            try store.setValue("me.auth", auth); try store.setValue("me.installation", installation)
        }
        me = Me(keys: keys, participant: participant, certificate: certificate, auth: auth, installation: installation)
    }

    // MARK: Founding a hub (§6.1)

    /// Publish a founding: journal every exact envelope first, then append each one not already on the hub.
    /// Running it again after a crash resumes with the same bytes (§6.1, §10.3 lost answers).
    public func publish(_ founding: Founding) async throws {
        let step = "found"
        let epoch = try founding.homeEpoch.id()
        var envelopes: [(String, Bytes)]
        if let journalled = try store.journal(step) {
            envelopes = try Self.unpack(journalled)
        } else {
            envelopes = try founding.directoryMessages.map {
                (directoryTag, try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 3, tag: directoryTag, plaintext: $0))
            }
            let homeTag = Tandem.homeTag(key: founding.homeKey, hub: pin.hub, uid: founding.participant, epoch: epoch)
            envelopes.append((homeTag, try Envelope.seal(key: founding.homeKey, hub: pin.hub, kind: 3, tag: homeTag,
                                                         plaintext: try founding.homeEpoch.json())))
            envelopes.append((Tandem.inboxTag(uid: founding.participant, hub: pin.hub), founding.homeKeyDelivery))
            try store.transaction {
                try store.setJournal(step, Self.pack(envelopes))
                for o in [founding.certificate, founding.snapshot, founding.homeEpoch] + (founding.capsule.map { [$0] } ?? []) {
                    try store.putObject(o)
                }
                try store.setValue("home.key." + epoch.hex, founding.homeKey)
            }
        }
        for o in [founding.certificate, founding.snapshot] { try registry.add(o) }
        if let c = founding.capsule { capsules[try c.id()] = c }
        homeEpochs[epoch] = founding.homeEpoch
        homeKeys[epoch] = founding.homeKey
        if me == nil { try adopt(participant: founding.participant, certificate: try founding.certificate.id(), auth: try founding.snapshot.id()) }
        for (tag, envelope) in envelopes where try store.messages(withHash: H(envelope)).isEmpty {
            let id = try await hub.append(channel: tag, body: envelope)
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
        }
        try store.clearJournal(step)
    }

    // MARK: Sync

    /// Read everything new on the hub and apply what this device can read, then retry what was held.
    /// Returns how many messages it applied.
    /// One at a time: a push and an app's own call never interleave. Not called from within a sync.
    @discardableResult
    public func sync() async throws -> Int {
        // Each sync runs after the one before it, chained, so no caller spins on a finished task.
        let previous = syncing
        let task = Task {
            _ = try? await previous?.value
            return try await self.syncOnce()
        }
        syncing = task
        defer { if syncing == task { syncing = nil }; flushEvents() }
        return try await task.value
    }

    func syncOnce() async throws -> Int {
        try takeExternalChanges()
        try await finishJournals()
        // A page at a time (`ChangeBatch.more`): each is saved with its cursor and applied before the next, so a big
        // first sync keeps what it has if it stops, and holds one page in memory.
        var applied = 0, more = true
        while more {
            let cursor = try store.value("cursor").map { String(decoding: $0, as: UTF8.self) } ?? ""
            let batch = try await hub.changes(since: cursor)
            if batch.more && batch.cursor == cursor { throw TandemError.invalid("hub: a page of changes made no progress") }
            try store.transaction {
                for m in batch.added { try store.noteMessage(id: m.id, channel: m.channel, hash: H(m.body)); try store.hold(m) }
                try store.forgetMessages(batch.removed)
                for id in batch.removed { try store.release(id) }
                try store.setValue("cursor", Bytes(batch.cursor.utf8))
            }
            applied += try applyHeld()
            more = batch.more
        }
        try await repairClaim()
        try await followUp()
        return applied
    }

    /// Apply held messages until none makes progress; how many it applied.
    func applyHeld() throws -> Int {
        var applied = 0, progress = true
        while progress {
            progress = false
            if me == nil, try discoverIdentity() { progress = true }
            if try adoptGrantedChains() { progress = true }
            for m in try store.held() {
                switch try apply(m) {
                case .applied: applied += 1; progress = true; try store.release(m.id); try store.markApplied(m.id)
                case .ignored: try store.release(m.id); try store.markApplied(m.id)
                case .waiting: continue
                }
            }
        }
        return applied
    }

    enum Outcome { case applied, ignored, waiting }

    /// A keychain that syncs already holds an admitted keypair (§6.3): find its certificate and a published
    /// snapshot that authorizes it, and act as that participant with this installation's own ID.
    func discoverIdentity() throws -> Bool {
        guard let keys else { return false }
        let sign = Bytes(keys.signing.publicKey.rawRepresentation), agree = Bytes(keys.agreement.publicKey.rawRepresentation)
        for c in registry.allCertificates where c.sign == sign && c.agree == agree && registry.activity(c.id).isValid {
            let snapshots = registry.snapshots.values.filter { registry.authorizes(snapshot: $0.id, certificate: c.id).isValid }
            guard let best = snapshots.max(by: { ($0.certs.count + $0.revocations.count) < ($1.certs.count + $1.revocations.count) })
            else { continue }
            try adopt(participant: c.participant, certificate: c.id, auth: best.id)
            return true
        }
        return false
    }

    /// One message, by its channel: the directory, this participant's inbox, or a home epoch.
    func apply(_ m: Message) throws -> Outcome {
        if m.channel == directoryTag {
            if let (kind, plaintext) = try? Envelope.open(key: directoryChannelKey, hub: pin.hub, tag: m.channel, envelope: m.body),
               kind == 1 {
                let outcome = try applyProfile(plaintext)
                if outcome == .applied { changed(.people) }
                return outcome
            }
            guard try applyDirectory(m) else { return .ignored }
            changed(.people); changed(.claims)
            return .applied
        }
        guard let me else { return .waiting }
        if m.channel == Tandem.inboxTag(uid: me.participant, hub: pin.hub) { return try applyInbox(m, me) }
        let readable = homeKeys.map { ($0.key, $0.value) } + homeCandidates.flatMap { e, ks in ks.map { (e, $0) } }
        for (epoch, key) in readable where m.channel == Tandem.homeTag(key: key, hub: pin.hub, uid: me.participant, epoch: epoch) {
            let outcome = try applyHome(m, epoch: epoch, key: key, me: me)
            if outcome == .applied { refreshMemberships(); changed(.home); changed(.lists) }
            return outcome
        }
        if let (cid, epoch, key) = collectionChannel(m.channel) { return try applyCollection(m, cid: cid, epoch: epoch, key: key) }
        // A channel this device can't read yet may become readable once a key arrives.
        return .waiting
    }

    /// One directory message: a registry object, a capsule, or a notice. Returns whether it was new.
    func applyDirectory(_ m: Message) throws -> Bool {
        guard let (kind, plaintext) = try? Envelope.open(key: directoryChannelKey, hub: pin.hub, tag: m.channel, envelope: m.body),
              kind == 3, let json = try? JSON.parse(plaintext), case let .string(type)? = json["type"]
        else { return false }   // not ours to read, or not well formed: ignored (§10.4)
        switch String(decoding: type, as: UTF8.self) {
        case "genesis":
            return false   // only the pinned genesis is ever accepted; another one is another hub (§6.9)
        case "capsule":
            guard let o = try? ControlObject.parse(json, expect: "capsule"), o.verify("sig", publicKey: pin.hub),
                  case let .id(g)? = o.fields["genesis"], g == pin.genesis, let id = try? o.id() else { return false }
            // Which message carries it, so a password change can destroy it (§6.7).
            try store.noteContents(message: m.id, record: Bytes(repeating: 0, count: 16), writes: [id])
            guard capsules[id] == nil else { return false }
            capsules[id] = o
            return try store.putObject(o)
        case "receipt":
            guard let o = try? ControlObject.parse(json, expect: "receipt"), let id = try? o.id(), receipts[id] == nil
            else { return false }
            receipts[id] = o
            return try store.putObject(o)
        case "home-recovery":
            guard let n = try? Notice.parse(plaintext, type: "home-recovery", fields: ["epoch", "bytes"]),
                  !notices.contains(n) else { return false }
            notices.append(n)
            return true
        default:
            guard let o = try? ControlObject.parse(json), (try? registry.add(o)) != nil else { return false }
            return try store.putObject(o)
        }
    }

    // MARK: Hub management (§6.7)

    /// The capsules no other capsule names: the ones a password opens.
    public var currentCapsules: [ControlObject] {
        let named = Set(capsules.values.flatMap { c -> [ID] in
            guard case let .set(s)? = c.fields["supersedes"] else { return [] }
            return s
        })
        return capsules.filter { !named.contains($0.key) }.sorted { $0.key.lexicographicallyPrecedes($1.key) }.map(\.value)
    }

    /// Unlock hub management with the hub password: `MS`, checked to be this hub's. Seconds and 1 GiB.
    public func unlock(password: String) throws -> Bytes {
        var last: (any Error)?
        for c in currentCapsules {
            guard case let .bytes(sealed)? = c.extras["capsule"] else { continue }
            do { return try Control.openCapsule(password: password, hub: pin.hub, capsule: sealed) } catch { last = error }
        }
        throw last ?? TandemError.invalid("device: no admin capsule on this hub")
    }

    // MARK: Journal encoding

    /// Envelopes and their tags, as length-prefixed pairs.
    static func pack(_ items: [(String, Bytes)]) -> Bytes {
        var out = Bytes()
        func lp(_ x: Bytes) { out += [UInt8(x.count >> 24 & 0xff), UInt8(x.count >> 16 & 0xff), UInt8(x.count >> 8 & 0xff), UInt8(x.count & 0xff)] + x }
        for (tag, body) in items { lp(Bytes(tag.utf8)); lp(body) }
        return out
    }

    static func unpack(_ data: Bytes) throws -> [(String, Bytes)] {
        var items: [(String, Bytes)] = [], i = 0
        func take() throws -> Bytes {
            guard i + 4 <= data.count else { throw TandemError.invalid("journal: truncated") }
            let n = data[i..<i + 4].reduce(0) { $0 << 8 | Int($1) }
            i += 4
            guard i + n <= data.count else { throw TandemError.invalid("journal: truncated") }
            defer { i += n }
            return Array(data[i..<i + n])
        }
        while i < data.count { items.append((String(decoding: try take(), as: UTF8.self), try take())) }
        return items
    }
}
