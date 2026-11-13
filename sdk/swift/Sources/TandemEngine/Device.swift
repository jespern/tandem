import Tandem

/// One installation on one hub (§6). Everything it knows is in its `Store`; everything it shares goes
/// through its `PushStore`. Work happens on a push, a local action or `sync()`, never on a timer.
public actor Device {
    public let pin: HubPin
    let store: Store
    let hub: any PushStore
    let directoryKey: Bytes
    public private(set) var registry: Registry
    /// Directory notices held: home-recovery boxes and the like, by type.
    public private(set) var notices: [Notice] = []

    var directoryTag: String { Tandem.directoryTag(directoryKey, hub: pin.hub) }
    var directoryChannelKey: Bytes { Tandem.directoryKey(directoryKey, hub: pin.hub) }

    init(pin: HubPin, directoryKey: Bytes, store: Store, hub: any PushStore) throws {
        self.pin = pin; self.directoryKey = directoryKey; self.store = store; self.hub = hub
        registry = Registry(pin: pin)
        for o in try store.objects() where o.kind != "genesis" { try? registry.add(o) }
    }

    /// Open a hub this device has the details of: its genesis (pinned at setup, invitation or pairing) and
    /// directory key. Nothing on the hub can replace the pin (§6.9).
    public static func open(genesis: ControlObject, directoryKey: Bytes, store: Store, hub: any PushStore) throws -> Device {
        let pin = try HubPin(genesis: genesis)
        guard case let .id(commit)? = genesis.fields["hubkey"], commit == Tandem.commit(directoryKey)
        else { throw TandemError.invalid("device: the directory key is not the genesis's") }
        try store.transaction {
            if let held = try store.value("pin.genesis") {
                guard held == pin.genesis else { throw TandemError.invalid("device: this store belongs to another hub") }
            } else {
                try store.setValue("pin.genesis", pin.genesis)
                try store.putObject(genesis)
            }
        }
        return try Device(pin: pin, directoryKey: directoryKey, store: store, hub: hub)
    }

    // MARK: Founding a hub (§6.1)

    /// Publish a founding: journal every exact envelope first, then append each one not already on the hub.
    /// Running it again after a crash resumes with the same bytes (§6.1, §10.3 lost answers).
    public func publish(_ founding: Founding) async throws {
        let step = "found"
        var envelopes: [(String, Bytes)]
        if let journalled = try store.journal(step) {
            envelopes = try Self.unpack(journalled)
        } else {
            envelopes = try founding.directoryMessages.map {
                (directoryTag, try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 3, tag: directoryTag, plaintext: $0))
            }
            envelopes.append((Tandem.inboxTag(uid: founding.participant, hub: pin.hub), founding.homeKeyDelivery))
            try store.setJournal(step, Self.pack(envelopes))
        }
        for (tag, envelope) in envelopes where try store.messages(withHash: H(envelope)).isEmpty {
            let id = try await hub.append(channel: tag, body: envelope)
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
        }
        try store.transaction {
            for o in [founding.certificate, founding.snapshot, founding.homeEpoch] { try store.putObject(o) }
            try store.clearJournal(step)
        }
        for o in [founding.certificate, founding.snapshot] { try registry.add(o) }
    }

    // MARK: Sync

    /// Read everything new on the hub and apply what this device can read. Returns how many messages it applied.
    @discardableResult
    public func sync() async throws -> Int {
        let cursor = try store.value("cursor").map { String(decoding: $0, as: UTF8.self) } ?? ""
        let batch = try await hub.changes(since: cursor)
        var applied = 0
        try store.transaction {
            for m in batch.added { try store.noteMessage(id: m.id, channel: m.channel, hash: H(m.body)) }
            try store.forgetMessages(batch.removed)
            try store.setValue("cursor", Bytes(batch.cursor.utf8))
        }
        for m in batch.added where m.channel == directoryTag {
            if try applyDirectory(m) { applied += 1 }
            try store.markApplied(m.id)
        }
        return applied
    }

    /// One directory message: a registry object, a genesis copy, or a notice. Returns whether it was new.
    func applyDirectory(_ m: Message) throws -> Bool {
        guard let (kind, plaintext) = try? Envelope.open(key: directoryChannelKey, hub: pin.hub, tag: m.channel, envelope: m.body),
              kind == 3, let json = try? JSON.parse(plaintext), case let .string(type)? = json["type"]
        else { return false }   // not ours to read, or not well formed: ignored (§10.4)
        switch String(decoding: type, as: UTF8.self) {
        case "genesis":
            // Only the pinned genesis is ever accepted; another one is another hub (§6.9).
            return false
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
