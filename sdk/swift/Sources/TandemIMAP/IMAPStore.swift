import Foundation
import Tandem

/// The IMAP binding of §10.3 as a PushStore, on Network.framework and Foundation only.
///
/// By default one connection both runs commands and idles between them, as §10.3
/// prescribes (servers cap connections per account). `.separate` idles on a second one.
/// Message IDs are "<uidvalidity>.<uid>"; the cursor is opaque. changes() returns
/// decoded envelopes, leaving out messages that are too large or don't decode (§10.4).
public actor IMAPStore: PushStore {
    public enum IdleRestart: Sendable, Equatable {
        /// Only the 29-minute protocol renewal; no automatic short restart on Gmail.
        case automatic
        case never
        /// End and restart IDLE this often while watching.
        case every(Duration)
    }
    public enum WatchConnection: Sendable, Equatable { case shared, separate }

    public struct Configuration: Sendable {
        public var host: String
        public var port: UInt16
        public var username: String
        public var password: String
        public var security: Security
        /// The folder name; a personal namespace prefix (RFC 2342) is put in front of it.
        public var folder: String
        public var idleRestart: IdleRestart
        public var watchConnection: WatchConnection
        /// Use QRESYNC when offered. Without it, removals come from UID SEARCH ALL.
        public var qresync: Bool
        /// Larger messages (RFC822.SIZE) are ignored; §10.6 caps a message at 1 MiB of envelope.
        public var maxMessageSize: Int
        /// The most new messages one `changes(since:)` fetches. More come in the next call (`ChangeBatch.more`), so a
        /// first sync of a big hub holds one page in memory.
        public var pageSize: Int
        public var reconnectDelay: ClosedRange<Duration>
        /// Receives protocol lines for debugging, each cut at 200 bytes. Login credentials are left out.
        public var trace: (@Sendable (String) -> Void)?

        public init(host: String, port: UInt16 = 993, username: String, password: String, security: Security = .tls,
                    folder: String = "Tandem", idleRestart: IdleRestart = .automatic, watchConnection: WatchConnection = .shared,
                    qresync: Bool = true, maxMessageSize: Int = 2 << 20, pageSize: Int = 500,
                    reconnectDelay: ClosedRange<Duration> = .seconds(1) ... .seconds(60), trace: (@Sendable (String) -> Void)? = nil) {
            self.host = host; self.port = port; self.username = username; self.password = password
            self.security = security; self.folder = folder; self.idleRestart = idleRestart
            self.watchConnection = watchConnection; self.qresync = qresync; self.maxMessageSize = maxMessageSize
            self.pageSize = max(1, pageSize)
            self.reconnectDelay = reconnectDelay; self.trace = trace
        }
    }

    public nonisolated let configuration: Configuration
    private let sink = Sink()
    private let link: Link
    var feedLink: Link { link }
    private var watcher: Link?
    private var watched = false

    public init(_ configuration: Configuration) {
        self.configuration = configuration
        link = Link(configuration, idles: configuration.watchConnection == .shared, sink: sink)
    }

    /// LIST, and CREATE if missing (ALREADYEXISTS is success). Other operations also create it when needed.
    public func ensureFolder() async throws { try await link.ensureFolder() }

    /// Returns once IDLE runs. Yields on EXISTS, EXPUNGE and VANISHED, and after every
    /// reconnect; ends with an error only when reconnecting can't help (credentials, server).
    public func watch() async throws -> AsyncThrowingStream<Void, any Error> {
        guard !watched else { throw TandemError.invalid("already watching") }
        watched = true
        let (stream, continuation) = AsyncThrowingStream<Void, any Error>.makeStream(bufferingPolicy: .bufferingNewest(1))
        sink.attach(continuation)
        let target: Link
        if configuration.watchConnection == .shared { target = link } else {
            target = Link(configuration, idles: true, sink: sink)
            watcher = target
        }
        do { try await target.startWatching() } catch { sink.finish(error); watched = false; throw error }
        continuation.onTermination = { _ in Task { await target.stopWatching() } }
        return stream
    }

    public func changes(since cursor: String) async throws -> ChangeBatch { try await link.changes(since: cursor) }

    public func append(channel: String, body: Bytes) async throws -> String {
        try await link.append([try MessageFormat.build(channel: channel, envelope: body)])[0]
    }

    /// Several envelopes, in as few commands as the server allows (MULTIAPPEND, LITERAL+).
    public func append(_ messages: [(channel: String, body: Bytes)]) async throws -> [String] {
        let built = try messages.map { try MessageFormat.build(channel: $0.channel, envelope: $0.body) }
        return built.isEmpty ? [] : try await link.append(built)
    }

    public func fetch(ids: [String]) async throws -> [Message] { try await link.fetch(ids: ids) }

    public func destroy(ids: [String]) async throws { try await link.destroy(ids: ids) }

    public func close() async {
        await watcher?.close()
        await link.close()
        sink.finish()
    }

    /// Test hooks.
    func append(raw: [Bytes]) async throws -> [String] { try await link.append(raw) }
    func command(_ text: String) async throws -> [Untagged] { try await link.command(text) }
    func capabilities() async -> Set<String> { await link.capabilities }
}
