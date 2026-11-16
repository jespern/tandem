import Foundation
import Tandem

// Test-only faults around the production IMAP bindings. A lost answer follows a
// successful real append. These fixtures are not linked into the SDK libraries.
actor FixtureFeed: PushStore {
    let inner: any PushStore
    var offline = false, watching = false, ended: (any Error)?
    var waiters: [CheckedContinuation<Void, any Error>] = []
    init(_ inner: any PushStore) { self.inner = inner }
    func setOffline(_ value: Bool) { offline = value }
    func check() throws { if offline { throw Failure(message: "injected transport interruption") } }
    /// Returns once the run's watch is up, or throws what ended the run before it.
    func waitForWatch() async throws {
        if watching { return }
        if let ended { throw ended }
        try await withCheckedThrowingContinuation { waiters.append($0) }
    }
    func runEnded(_ error: any Error) {
        guard !watching else { return }
        ended = error
        for waiter in waiters { waiter.resume(throwing: error) }
        waiters.removeAll()
    }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> {
        try check()
        let stream = try await inner.watch()
        watching = true
        for waiter in waiters { waiter.resume() }
        waiters.removeAll()
        return stream
    }
    func changes(since cursor: String) async throws -> ChangeBatch { try check(); return try await inner.changes(since: cursor) }
    func append(channel: String, body: Bytes) async throws -> String { try check(); return try await inner.append(channel: channel, body: body) }
    func fetch(ids: [String]) async throws -> [Message] { try check(); return try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try check(); try await inner.destroy(ids: ids) }
    func close() async { await inner.close() }
}

actor FixtureBlobs: BlobHub {
    let inner: any BlobHub
    var armed = false, blocked = false, lost = false
    var remaining = 0, stored = 0
    init(_ inner: any BlobHub) { self.inner = inner }
    func fault(after n: Int, lostAck: Bool) { armed = true; blocked = n == 0; remaining = n; lost = lostAck; stored = 0 }
    func clear() { armed = false; blocked = false }
    func append(_ chunks: [(channel: String, envelope: Bytes)], form: ChunkForm) async throws -> [String] {
        if blocked { throw Failure(message: "injected transport interruption") }
        if !armed { return try await inner.append(chunks, form: form) }
        let n = min(remaining, chunks.count)
        let ids = try await inner.append(Array(chunks.prefix(n)), form: form)
        stored += n; remaining -= n
        if remaining == 0 {
            blocked = true
            if lost || n < chunks.count { throw Failure(message: "injected transport interruption") }
        }
        return ids
    }
    func changes(since cursor: String) async throws -> BlobChanges { try await inner.changes(since: cursor) }
    func fetch(ids: [String]) async throws -> [Chunk] { try await inner.fetch(ids: ids) }
    func destroy(ids: [String]) async throws { try await inner.destroy(ids: ids) }
    func quota() async throws -> Quota { try await inner.quota() }
    func release() async { await inner.release() }
}
