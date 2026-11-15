import Foundation
import Tandem
@testable import TandemIMAP
import XCTest

/// Arrival times of a watch stream's invalidations.
actor EventLog {
    private var events: [ContinuousClock.Instant] = []
    private var waiter: (after: ContinuousClock.Instant, continuation: CheckedContinuation<ContinuousClock.Instant?, Never>)?
    private var ended = false, endWaiter: CheckedContinuation<Bool, Never>?

    func record(_ t: ContinuousClock.Instant) {
        events.append(t)
        if let w = waiter, t > w.after { waiter = nil; w.continuation.resume(returning: t) }
    }
    func end(_ error: (any Error)?) {
        ended = true
        if let w = waiter { waiter = nil; w.continuation.resume(returning: nil) }
        endWaiter?.resume(returning: true); endWaiter = nil
    }
    /// Whether the stream ends within the limit.
    func finished(within limit: Duration) async -> Bool {
        if ended { return true }
        let timer = Task { try? await Task.sleep(for: limit); self.expireEnd() }
        defer { timer.cancel() }
        return await withCheckedContinuation { endWaiter = $0 }
    }
    private func expireEnd() { endWaiter?.resume(returning: false); endWaiter = nil }
    private func expire(_ after: ContinuousClock.Instant) {
        if let w = waiter, w.after == after { waiter = nil; w.continuation.resume(returning: nil) }
    }
    /// The first invalidation after t0, or nil if none arrives in time.
    func first(after t0: ContinuousClock.Instant, within limit: Duration) async -> ContinuousClock.Instant? {
        if let t = events.first(where: { $0 > t0 }) { return t }
        let timer = Task { try? await Task.sleep(for: limit); self.expire(t0) }
        defer { timer.cancel() }
        return await withCheckedContinuation { waiter = (t0, $0) }
    }

    static func follow(_ stream: AsyncThrowingStream<Void, any Error>) -> EventLog {
        let log = EventLog()
        Task {
            do { for try await _ in stream { await log.record(.now) }; await log.end(nil) } catch { await log.end(error) }
        }
        return log
    }
}

struct Timeout: Error {}

/// The operation's result, or Timeout after the limit, so a lost push fails a test instead of hanging it.
func within<T: Sendable>(_ limit: Duration, _ operation: @escaping @Sendable () async throws -> T) async throws -> T {
    try await withThrowingTaskGroup(of: T.self) { group in
        group.addTask { try await operation() }
        group.addTask { try await Task.sleep(for: limit); throw Timeout() }
        defer { group.cancelAll() }
        do { return try await group.next()! } catch is CancellationError { throw Timeout() }
    }
}

func seconds(_ d: Duration) -> Double { Double(d.components.seconds) + Double(d.components.attoseconds) / 1e18 }

/// Against a disposable Stalwart in Docker. Set TANDEM_IMAP_STALWART=1 to run; skipped otherwise.
final class StalwartTests: XCTestCase {
    nonisolated(unsafe) static var server: Stalwart?

    override func setUp() async throws {
        guard ProcessInfo.processInfo.environment["TANDEM_IMAP_STALWART"] == "1" else {
            throw XCTSkip("set TANDEM_IMAP_STALWART=1 to test against a disposable Stalwart in Docker")
        }
        if Self.server == nil {
            guard let docker = Stalwart.docker else { throw XCTSkip("Docker is not available") }
            let server = try Stalwart(docker: docker)
            try await server.start()
            Self.server = server
        }
    }

    override class func tearDown() {
        server?.stop()
        server = nil
        super.tearDown()
    }

    func config(_ folder: String, qresync: Bool = true, watch: IMAPStore.WatchConnection = .shared,
                security: Security = .tlsTrustingAnyCertificateForTesting, password: String? = nil, pageSize: Int = 500) -> IMAPStore.Configuration {
        let s = Self.server!
        var trace: (@Sendable (String) -> Void)?
        if ProcessInfo.processInfo.environment["TANDEM_IMAP_TRACE"] != nil { trace = { line in print("[\(folder)] \(line)") } }
        return IMAPStore.Configuration(host: "127.0.0.1", port: s.imapPort, username: s.username, password: password ?? s.password,
                                       security: security, folder: folder, watchConnection: watch, qresync: qresync,
                                       pageSize: pageSize, reconnectDelay: .milliseconds(250) ... .seconds(2),
                                       trace: trace)
    }
    func folder() -> String { "Tandem-" + UUID().uuidString.prefix(8) }
    func envelope(_ n: Int) -> Bytes { Bytes("envelope \(n) ".utf8) + (0..<UInt8(n % 200)).map { $0 } }

    func testCredentialsAndCertificates() async throws {
        let wrong = IMAPStore(config(folder(), password: "wrong"))
        do { try await wrong.ensureFolder(); XCTFail("wrong password accepted") }
        catch IMAPError.authentication {} catch { XCTFail("\(error)") }
        await wrong.close()
        // The self-signed certificate fails verification unless the test override is set.
        let verified = IMAPStore(config(folder(), security: .tls))
        do { try await verified.ensureFolder(); XCTFail("self-signed certificate accepted") }
        catch IMAPError.connection {} catch { XCTFail("\(error)") }
        await verified.close()
    }

    /// Changes come a page at a time (`ChangeBatch.more`), with and without QRESYNC: every message once, in order, the
    /// last page without `more`.
    func testChangesComeAPageAtATime() async throws {
        let name = folder()
        let a = IMAPStore(config(name, pageSize: 3)), b = IMAPStore(config(name, qresync: false, pageSize: 3))
        defer { Task { await a.close(); await b.close() } }
        try await a.ensureFolder()
        let ids = try await a.append((0..<8).map { (channel: "chan-a", body: envelope($0)) })
        try await a.destroy(ids: [ids[4]])   // a gap in the UIDs
        var want = ids; want.remove(at: 4)
        for s in [a, b] {
            var got: [String] = [], pages: [Bool] = [], cursor = ""
            while true {
                let batch = try await s.changes(since: cursor)
                got += batch.added.map(\.id); pages.append(batch.more); cursor = batch.cursor
                if !batch.more { break }
            }
            XCTAssertEqual(got, want)
            XCTAssertEqual(pages, [true, true, false])
            // Fewer new messages than a page: one call, no search.
            let two = try await a.append([(channel: "chan-a", body: envelope(20)), (channel: "chan-a", body: envelope(21))])
            let next = try await s.changes(since: cursor)
            XCTAssertEqual(next.added.map(\.id), two)
            XCTAssertFalse(next.more)
            want += two
        }
    }

    func testFolderAppendChangesFetchDestroy() async throws {
        let name = folder()
        let a = IMAPStore(config(name)), b = IMAPStore(config(name, qresync: false))
        defer { Task { await a.close(); await b.close() } }
        try await a.ensureFolder()
        try await a.ensureFolder()   // idempotent
        let listed = try await a.command("LIST \"\" \(name)")
        XCTAssertEqual(listed.count, 1)
        let caps = await a.capabilities()
        print("Stalwart capabilities:", caps.sorted().joined(separator: " "))

        let empty = try await a.changes(since: "")
        XCTAssertEqual(empty.added, []); XCTAssertEqual(empty.removed, [])

        let first = try await a.append(channel: "chan-a", body: envelope(1))
        let more = try await a.append([(channel: "chan-b", body: envelope(2)), (channel: "chan-a", body: envelope(3))])
        let ids = [first] + more
        XCTAssertEqual(Set(ids).count, 3)
        XCTAssertFalse(ids.contains(""))

        let batch = try await a.changes(since: empty.cursor)
        XCTAssertEqual(batch.added, [Message(id: ids[0], channel: "chan-a", body: envelope(1)),
                                     Message(id: ids[1], channel: "chan-b", body: envelope(2)),
                                     Message(id: ids[2], channel: "chan-a", body: envelope(3))])
        XCTAssertEqual(batch.removed, [])
        let again = try await a.changes(since: batch.cursor)   // n:* returns the last message again; ignored
        XCTAssertEqual(again.added, []); XCTAssertEqual(again.removed, [])

        let fetched = try await a.fetch(ids: ids.reversed() + ["1.999999", "junk"])
        XCTAssertEqual(fetched.map(\.body), [envelope(1), envelope(2), envelope(3)])
        XCTAssertEqual(fetched.map(\.id), ids)

        // Bodies another client stored differently (§10.4), and a message that isn't Tandem's.
        let raws = [testUTF16Base64, testQuotedPrintable].map { Bytes(Data(base64Encoded: $0)!) }
        _ = try await a.append(raw: raws + [Bytes("Subject: hello\r\n\r\nnot an envelope\r\n".utf8)])
        let odd = try await a.changes(since: again.cursor)
        XCTAssertEqual(odd.added.map(\.body), [Bytes(0..<60), Bytes(0..<200)])
        XCTAssertEqual(odd.added.map(\.channel), ["abcdefgh", "abcdefgh"])

        // The same history without QRESYNC (UID SEARCH ALL).
        let plain = try await b.changes(since: "")
        XCTAssertEqual(plain.added.map(\.id), ids + odd.added.map(\.id))

        try await a.destroy(ids: [ids[0], odd.added[0].id])
        let gone = try await a.changes(since: odd.cursor)
        XCTAssertEqual(Set(gone.removed), [ids[0], odd.added[0].id]); XCTAssertEqual(gone.added, [])
        let fetchedGone = try await a.fetch(ids: [ids[0]])
        XCTAssertEqual(fetchedGone, [])
        let plainGone = try await b.changes(since: plain.cursor)
        XCTAssertEqual(Set(plainGone.removed), [ids[0], odd.added[0].id]); XCTAssertEqual(plainGone.added, [])
        let none = try await a.changes(since: gone.cursor)
        XCTAssertEqual(none.removed, [])

        // A message moved out of the folder counts as removed, and moved back in as added.
        _ = try await a.command("CREATE \(name)-other")
        guard let (_, uid) = MessageID.parse(ids[1]) else { return XCTFail() }
        _ = try await a.command("UID MOVE \(uid) \(name)-other")
        let moved = try await a.changes(since: none.cursor)
        XCTAssertEqual(moved.removed, [ids[1]])
        _ = try await a.command("SELECT \(name)-other")
        _ = try await a.command("UID MOVE 1:* \(name)")
        let back = try await a.changes(since: moved.cursor)
        XCTAssertEqual(back.added.map(\.body), [envelope(2)])
        XCTAssertNotEqual(back.added.first?.id, ids[1])
    }

    func testUIDValidityChangeIsAFullResync() async throws {
        let name = folder()
        let a = IMAPStore(config(name)), b = IMAPStore(config(name, qresync: false)), other = IMAPStore(config(name))
        defer { Task { await a.close(); await b.close(); await other.close() } }
        let id = try await a.append(channel: "c", body: envelope(1))
        let before = try await a.changes(since: "")
        let plainBefore = try await b.changes(since: "")
        // Another client deletes the folder while a and b have it selected.
        _ = try await other.command("UNSELECT")
        _ = try await other.command("DELETE \(name)")
        let after = try await a.changes(since: before.cursor)
        XCTAssertEqual(after.removed, [id]); XCTAssertEqual(after.added, [])
        let newID = try await a.append(channel: "c", body: envelope(2))
        XCTAssertNotEqual(MessageID.parse(newID)!.validity, MessageID.parse(id)!.validity)
        let plainAfter = try await b.changes(since: plainBefore.cursor)
        XCTAssertEqual(plainAfter.removed, [id]); XCTAssertEqual(plainAfter.added.map(\.id), [newID])
    }

    func testIdlePushLatency() async throws {
        for mode in [IMAPStore.WatchConnection.shared, .separate] {
            let name = folder()
            let watcher = IMAPStore(config(name, watch: mode)), writer = IMAPStore(config(name))
            try await writer.ensureFolder()
            let log = EventLog.follow(try await watcher.watch())
            var appends: [Double] = [], expunges: [Double] = [], roundTrips: [Double] = []
            // Pushes are timed from the start of the writer's command. Each sample first waits
            // for earlier invalidations (the one after connecting, repeated EXISTS) to arrive.
            for n in 0..<7 {
                try await Task.sleep(for: .milliseconds(200))
                let t0 = ContinuousClock.now
                let id = try await writer.append(channel: "c", body: envelope(n))
                roundTrips.append(seconds(.now - t0))
                guard let t1 = await log.first(after: t0, within: .seconds(10)) else { return XCTFail("no push for append \(n)") }
                appends.append(seconds(t1 - t0))
                _ = try await watcher.changes(since: "")   // a command between pushes: IDLE ends and resumes
                try await Task.sleep(for: .milliseconds(200))
                let t2 = ContinuousClock.now
                try await writer.destroy(ids: [id])
                guard let t3 = await log.first(after: t2, within: .seconds(10)) else { return XCTFail("no push for expunge \(n)") }
                expunges.append(seconds(t3 - t2))
            }
            func ms(_ v: [Double]) -> String { String(format: "median %.1f ms, max %.1f ms", v.sorted()[v.count / 2] * 1000, v.max()! * 1000) }
            print("IDLE push latency on Stalwart (\(mode) connection), 7 samples: append \(ms(appends)); expunge \(ms(expunges)); "
                  + "APPEND round trip \(ms(roundTrips))")
            await watcher.close(); await writer.close()
            let ended = await log.finished(within: .seconds(5))
            XCTAssertTrue(ended, "close ends the watch stream")
        }
    }

    func testSessionEndToEnd() async throws {
        let name = folder()
        let device = IMAPStore(config(name)), other = IMAPStore(config(name))
        try await other.ensureFolder()
        let session = try Session(store: device, capacity: 8)
        defer { Task { await session.close(); await other.close() } }
        try await within(.seconds(10)) { try await session.start() }
        let updates = session.updates
        /// Added messages from the session's batches until `done` holds for them.
        @Sendable func collect(until done: @escaping @Sendable ([Message]) -> Bool) async throws -> [Message] {
            try await within(.seconds(10)) {
                var seen: [Message] = []
                for try await batch in updates {
                    seen += batch.added
                    if done(seen) { break }
                }
                return seen
            }
        }
        let initial = try await collect { _ in true }
        XCTAssertEqual(initial, [])
        let id = try await other.append(channel: "chan", body: envelope(7))
        let pushed = try await collect { !$0.isEmpty }
        XCTAssertEqual(pushed, [Message(id: id, channel: "chan", body: envelope(7))])
        let own = try await session.submit(channel: "mine", body: envelope(8))
        let local = try await collect { $0.contains { $0.id == own } }
        XCTAssertEqual(local.last, Message(id: own, channel: "mine", body: envelope(8)))
    }

    /// Restarting the server drops every connection; the watcher reconnects with backoff,
    /// reports an invalidation, and IDLE pushes resume.
    func testReconnectAfterServerRestart() async throws {
        let name = folder()
        let watcher = IMAPStore(config(name)), writer = IMAPStore(config(name))
        defer { Task { await watcher.close(); await writer.close() } }
        try await writer.ensureFolder()
        let log = EventLog.follow(try await watcher.watch())
        try await Task.sleep(for: .milliseconds(200))   // the invalidation after connecting arrives first
        let before = try Self.server!.listens(on: 993), t0 = ContinuousClock.now
        try Self.server!.restart()
        try await Self.server!.listening(on: 993, after: before)
        let up = ContinuousClock.now
        guard let t1 = await log.first(after: t0, within: .seconds(60)) else { return XCTFail("no invalidation after reconnect") }
        XCTAssertGreaterThan(t1, t0)
        print(String(format: "server restarted in %.1f s; the watcher reconnected and invalidated %.1f s after the server listened again",
                     seconds(up - t0), seconds(t1 - up)))
        let t2 = ContinuousClock.now
        let id = try await writer.append(channel: "c", body: envelope(1))   // the writer reconnects on demand
        guard let t3 = await log.first(after: t2, within: .seconds(10)) else { return XCTFail("no push after reconnect") }
        print(String(format: "push after reconnect: %.1f ms", seconds(t3 - t2) * 1000))
        let batch = try await watcher.changes(since: "")
        XCTAssertEqual(batch.added.map(\.id), [id])
    }
}

let testUTF16Base64 = "RnJvbTogVGFuZGVtIDx0YW5kZW1AaW52YWxpZD4NCkRhdGU6IEZyaSwgMTMgTm92IDIwMjYgMTI6NDQ6MjEgKzAwMDANCk1lc3NhZ2UtSUQ6IDwxNzk0NTczODYxNzYuOTY3MTYuMzA5OTk5NDAyMjU4NjMxNDkzN0B0YW5kZW0uaW52YWxpZD4NClN1YmplY3Q6IHRhbmRlbQ0KVGFuZGVtLVZlcnNpb246IDENClRhbmRlbS1DaGFubmVsOiBhYmNkZWZnaA0KTUlNRS1WZXJzaW9uOiAxLjANCkNvbnRlbnQtVHlwZTogdGV4dC9wbGFpbjsgY2hhcnNldD0idXRmLTE2Ig0KQ29udGVudC1UcmFuc2Zlci1FbmNvZGluZzogYmFzZTY0DQoNCi8vNUJBRUVBUlFCREFFRUFkd0JSQUVZQVFnQm5BR01BU1FCREFGRUFid0JNQUVRQVFRQXdBRThBUkFCNEFFRUFVZ0JGQUdnQVRRQlYNCkFFWUFVZ0JaQUZnQVJ3QkNBR3NBWVFCSEFIZ0Fkd0JrQUVnQWFBQTRBR2NBU1FCVEFFa0FhZ0JLQUVNQVZRQnRBRW9BZVFCbkFIQUENClN3QnBBSE1BY3dCTUFGTUFOQUIyQUUwQVJBQkZBSGtBVFFCNkFGRUFNUUJPQUdvQVl3QTBBQTBBQ2dCUEFGUUFid0EzQUEwQUNnQT0NCg=="
let testQuotedPrintable = "RnJvbTogVGFuZGVtIDx0YW5kZW1AaW52YWxpZD4NCkRhdGU6IEZyaSwgMTMgTm92IDIwMjYgMTI6NDQ6MjEgKzAwMDANCk1lc3NhZ2UtSUQ6IDwxNzk0NTczODYxNzYuOTY3MTYuMTYxNDQ2OTU2MDg2OTM1NjkwMDBAdGFuZGVtLmludmFsaWQ+DQpTdWJqZWN0OiB0YW5kZW0NClRhbmRlbS1WZXJzaW9uOiAxDQpUYW5kZW0tQ2hhbm5lbDogYWJjZGVmZ2gNCkNvbnRlbnQtVHlwZTogdGV4dC9wbGFpbjsgY2hhcnNldD0idXRmLTgiDQpDb250ZW50LVRyYW5zZmVyLUVuY29kaW5nOiBxdW90ZWQtcHJpbnRhYmxlDQpNSU1FLVZlcnNpb246IDEuMA0KDQpBQUVDQXdRRkJnY0lDUW9MREEwT0R4QVJFaE1VRlJZWEdCa2FHeHdkSGg4Z0lTSWpKQ1VtSnlncEtpc3NMUzR2TURFeU16UTFOamM0DQpPVG83UEQwK1AwQkJRa05FUlVaSFNFbEtTMHhOVGs5UVVWSlRWRlZXVjFoWldsdGNYVjVmWUdGaVkyUmxabWRvYVdwcmJHMXViM0J4DQpjbk4wZFhaM2VIbDZlM3g5Zm4rQWdZS0RoSVdHaDRpSmlvdU1qWTZQa0pHU2s1U1ZscGVZbVpxYm5KMmVuNkNob3FPa3BhYW5xS21xDQpxNnl0cnErd3NiS3p0TFcydDdpNXVydTh2YjYvd01IQ3c4VEZ4c2M9M0QNCg=="
