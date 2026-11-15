import Tandem

/// A blob folder in memory, shared by every device of a test (BLOBS.md section 4.3). Like a credential holder, a test
/// can drop chunks, append junk under any claim, make the escaped form come back damaged, or set a quota.
public actor MemoryBlobHub: BlobHub {
    struct Stored { let ref: BlobRef; let body: Bytes }
    enum Event { case added(BlobRef), removed(String) }
    public struct OverQuota: Error {}
    public struct Lost: Error {}

    private var events: [Event] = []
    private var live: [String: Stored] = [:]
    private var serial = 0
    public private(set) var appends = 0, releases = 0
    /// Every chunk appended in the escaped form comes back altered.
    public var damageEscaped = false
    /// What `quota()` returns.
    public var quotaReport: Quota = .unknown
    /// Appends fail as an over-quota server's do.
    public var failAppends = false
    /// If set, only that many chunks of each append are stored, then it fails.
    public var partialAppends: Int?
    /// This many appends store their chunks and then lose the answer.
    public var loseAnswers = 0

    public init() {}

    public func set(damageEscaped: Bool? = nil, failAppends: Bool? = nil, partialAppends: Int?? = nil, loseAnswers: Int? = nil, quota: Quota? = nil) {
        if let damageEscaped { self.damageEscaped = damageEscaped }
        if let failAppends { self.failAppends = failAppends }
        if let partialAppends { self.partialAppends = partialAppends }
        if let loseAnswers { self.loseAnswers = loseAnswers }
        if let quota { quotaReport = quota }
    }

    public func changes(since cursor: String) async throws -> BlobChanges {
        let start = cursor.isEmpty ? 0 : (Int(cursor) ?? -1)
        guard start >= 0, start <= events.count else { throw TandemError.invalid("hub: bad cursor") }
        var added: [BlobRef] = [], removed: [String] = []
        for e in events[start...] {
            switch e {
            case let .added(r): if live[r.id] != nil { added.append(r) }
            case let .removed(id): removed.append(id)
            }
        }
        return BlobChanges(added: added, removed: removed, cursor: String(events.count))
    }

    private func add(channel: String, address: Bytes, body: Bytes) -> String {
        serial += 1
        let ref = BlobRef(id: String(serial), channel: channel, address: address)
        live[ref.id] = Stored(ref: ref, body: body)
        events.append(.added(ref))
        return ref.id
    }

    public func append(_ chunks: [(channel: String, envelope: Bytes)], form: ChunkForm) async throws -> [String] {
        if failAppends { throw OverQuota() }
        appends += 1
        var ids: [String] = []
        for (i, c) in chunks.enumerated() {
            if let p = partialAppends, i >= p { throw Lost() }
            var body = c.envelope
            if form == .escaped && damageEscaped { body[body.count - 1] ^= 0x20 }
            ids.append(add(channel: c.channel, address: Blob.address(c.envelope), body: body))
        }
        if loseAnswers > 0 { loseAnswers -= 1; throw Lost() }
        return ids
    }

    public func fetch(ids: [String]) async throws -> [Chunk] { ids.compactMap { id in live[id].map { Chunk(id: id, envelope: $0.body) } } }

    public func destroy(ids: [String]) async throws { drop(ids) }

    public func quota() async throws -> Quota { quotaReport }

    public func release() async { releases += 1 }

    /// Deletes chunk messages as a credential holder could.
    public func drop(_ ids: [String]) {
        for id in ids where live.removeValue(forKey: id) != nil { events.append(.removed(id)) }
    }

    /// Appends a message claiming any address with any body, as anyone with the credential can.
    @discardableResult
    public func inject(channel: String, address: Bytes, body: Bytes) -> String { add(channel: channel, address: address, body: body) }

    /// Every chunk message on the hub, oldest first.
    public var refs: [BlobRef] {
        events.compactMap { if case let .added(r) = $0, live[r.id] != nil { r } else { nil } }
    }

    /// The IDs of the messages claiming an address, oldest first.
    public func withAddress(_ address: Bytes) -> [String] { refs.filter { $0.address == address }.map(\.id) }
}
