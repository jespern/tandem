import Foundation
import Tandem

/// The blob folder of DESIGN.md §17.4.3: chunks, each one message, in a folder next to the feed's that no
/// device idles on. It lists the chunks' claimed addresses from their headers alone, fetches and decodes chunk
/// bodies, appends chunks and reads the quota, on transfer connections of its own that open when work arrives
/// and close at `release()`.
public actor BlobStore: BlobHub {
    /// The blob folder's name; a personal namespace prefix goes in front of it.
    public static let folder = "Tandem-Blobs"

    public typealias Ref = BlobRef
    public typealias Changes = BlobChanges

    public struct Options: Sendable {
        /// The blob folder's name.
        public var folder: String
        /// How many connections a transfer may use: nil decides from the server, 2 where it batches appends
        /// (MULTIAPPEND or LITERAL+) and 4 where it doesn't (§17.4.3); at most 8. Fewer when one is refused.
        public var transferConnections: Int?
        /// The message bytes of one append batch.
        public var batchBytes: Int
        /// How many chunks one FETCH asks for.
        public var fetchBatch: Int

        public init(folder: String = BlobStore.folder, transferConnections: Int? = nil, batchBytes: Int = 6 << 20, fetchBatch: Int = 4) {
            self.folder = folder; self.transferConnections = transferConnections.map { min(8, max(1, $0)) }
            self.batchBytes = max(1, batchBytes); self.fetchBatch = max(1, fetchBatch)
        }
    }

    public nonisolated let options: Options
    private let configuration: IMAPStore.Configuration
    private var links: [Link] = []
    private var refused = false
    private var closed = false

    /// The blob folder of the account `configuration` describes. It doesn't connect yet.
    public init(_ configuration: IMAPStore.Configuration, options: Options = Options()) {
        var c = configuration
        c.folder = options.folder
        c.maxMessageSize = max(c.maxMessageSize, 8 << 20)   // a 4 MiB chunk is 5.7 MB in base64
        c.pageSize = max(c.pageSize, 2000)                  // headers only
        c.watchConnection = .shared
        self.configuration = c
        self.options = options
    }

    /// How many transfer connections are open.
    public var connections: Int { links.count }

    private func first() throws -> Link {
        guard !closed else { throw TandemError.invalid("blob store closed") }
        if links.isEmpty { links = [Link(configuration, idles: false, sink: Sink())] }
        return links[0]
    }

    /// Up to `want` connections, the first connected; more are opened up to the configured count, keeping what
    /// the server accepts.
    private func transfer(_ want: Int) async throws -> [Link] {
        let l = try first()
        try await l.connectNow()
        let caps = await l.capabilities
        let limit = options.transferConnections ?? (caps.contains("MULTIAPPEND") || caps.contains("LITERAL+") ? 2 : 4)
        let want = max(1, min(want, limit))
        while links.count < want && !refused {
            let extra = Link(configuration, idles: false, sink: Sink())
            do {
                try await extra.connectNow()
                guard !closed else { await extra.close(); throw TandemError.invalid("blob store closed") }
                links.append(extra)
            } catch is CancellationError {
                await extra.close()
                throw CancellationError()
            } catch {
                await extra.close()
                refused = true   // the server's connection cap, most likely: go on with fewer
            }
        }
        return Array(links.prefix(want))
    }

    /// Closes the transfer connections. The next operation opens them again.
    public func release() async {
        let open = links
        links = []
        refused = false
        for l in open { await l.close() }
    }

    public func close() async {
        closed = true
        await release()
    }

    /// One page of the blob folder's changes since `cursor` ("" for the start), from the headers alone.
    /// Messages without both claims are left out.
    public func changes(since cursor: String) async throws -> Changes { try await first().refs(since: cursor) }

    /// Chunks by ID over the transfer connections, a few to a command, in the order of `ids`; those that are
    /// gone, too large or don't decode are left out.
    public func fetch(ids: [String]) async throws -> [Chunk] {
        guard !ids.isEmpty else { return [] }
        let batches = stride(from: 0, to: ids.count, by: options.fetchBatch).map { Array(ids[$0..<min($0 + options.fetchBatch, ids.count)]) }
        let results = try await spread(try await transfer(batches.count), batches) { link, batch in try await link.chunks(ids: batch) }
        let byID = Dictionary(results.joined().map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        return ids.compactMap { byID[$0] }
    }

    /// Appends chunks in batches of a few MiB over the transfer connections, each batch in as few commands as
    /// the server allows (MULTIAPPEND). IDs follow the order of `chunks`; "" where the server named no UID. Not
    /// retried: an append whose answer was lost may have landed, which the next `changes` shows.
    public func append(_ chunks: [(channel: String, envelope: Bytes)], form: ChunkForm = .escaped) async throws -> [String] {
        let built = try chunks.map { try ChunkFormat.build(channel: $0.channel, envelope: $0.envelope, form: form) }
        guard !built.isEmpty else { return [] }
        var batches: [[Bytes]] = [], i = 0
        while i < built.count {
            var batch = [built[i]], size = built[i].count
            i += 1
            while i < built.count, size + built[i].count <= options.batchBytes { batch.append(built[i]); size += built[i].count; i += 1 }
            batches.append(batch)
        }
        return try await spread(try await transfer(batches.count), batches) { link, batch in try await link.append(batch) }.flatMap { $0 }
    }

    /// Removes chunk messages for good; on Gmail through Trash, as the feed's (§10.3).
    public func destroy(ids: [String]) async throws {
        guard !ids.isEmpty else { return }
        try await first().destroy(ids: ids)
    }

    /// The storage quota of the blob folder's root.
    public func quota() async throws -> Quota { try await first().quota() }

    /// Runs one job per batch over the links, each link taking the next batch when it is free; results in batch order.
    private func spread<B: Sendable, R: Sendable>(_ links: [Link], _ batches: [B], _ job: @escaping @Sendable (Link, B) async throws -> R) async throws -> [R] {
        let queue = Queue(batches.count)
        return try await withThrowingTaskGroup(of: [(Int, R)].self) { group in
            for link in links {
                group.addTask {
                    var done: [(Int, R)] = []
                    while let i = queue.next() {
                        try Task.checkCancellation()
                        done.append((i, try await job(link, batches[i])))
                    }
                    return done
                }
            }
            var out = [R?](repeating: nil, count: batches.count)
            for try await done in group { for (i, r) in done { out[i] = r } }
            return out.map { $0! }
        }
    }
}

/// Hands out indexes 0..<count, each once, to whichever task asks first.
private final class Queue: @unchecked Sendable {
    private let lock = NSLock()
    private var nextIndex = 0
    private let count: Int
    init(_ count: Int) { self.count = count }
    func next() -> Int? {
        lock.lock(); defer { lock.unlock() }
        guard nextIndex < count else { return nil }
        defer { nextIndex += 1 }
        return nextIndex
    }
}

extension IMAPStore {
    /// Appends chunks to the blob folder on the feed's own connection, without selecting it: for a transfer of one
    /// batch, which takes less time than opening a transfer connection (DESIGN.md §17.4.3).
    public func appendChunks(_ chunks: [(channel: String, envelope: Bytes)], folder: String = BlobStore.folder,
                             form: ChunkForm = .escaped) async throws -> [String] {
        let built = try chunks.map { try ChunkFormat.build(channel: $0.channel, envelope: $0.envelope, form: form) }
        return built.isEmpty ? [] : try await feedLink.append(built, toFolderNamed: folder)
    }
}
