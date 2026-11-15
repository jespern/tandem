import Foundation
import Synchronization
import Tandem

/// The module's one timer: the IDLE refresh (RFC 2177), a configured IDLE restart period and the
/// reconnect backoff (§10.3). Nothing here reconciles on a timer.
func pause(_ duration: Duration) async throws { try await Task.sleep(for: duration) } // allowed-timer: §10.3

/// Hands invalidations to the store's watch stream.
final class Sink: Sendable {
    private let state = Mutex<AsyncThrowingStream<Void, any Error>.Continuation?>(nil)
    func attach(_ continuation: AsyncThrowingStream<Void, any Error>.Continuation) { state.withLock { $0 = continuation } }
    func changed() { state.withLock { _ = $0?.yield(()) } }
    func finish(_ error: (any Error)? = nil) {
        let continuation = state.withLock { c -> AsyncThrowingStream<Void, any Error>.Continuation? in defer { c = nil }; return c }
        continuation?.finish(throwing: error)
    }
}

/// One logical connection to the hub's Tandem folder: it connects on demand, runs
/// commands one at a time, and, while watching, idles between them and reconnects
/// with backoff when the connection is lost. Every new connection made while
/// watching reports one change once IDLE runs again, since anything may have
/// happened while it was down.
actor Link {
    struct Selection { var validity: UInt32; var uidNext: UInt32 = 0; var modseq: UInt64; var vanished: UIDSet }   // uidNext 0: not said

    private let config: IMAPStore.Configuration
    private let idles: Bool
    private let sink: Sink
    private var connection: Connection?
    private var serial = 0, current = 0
    private var caps: Set<String> = []
    private var fresh = false
    private var folder: Bytes
    /// The personal namespace prefix (RFC 2342), empty if none.
    private(set) var prefix: Bytes = []
    private var selected: Bytes?
    private var validity: UInt32 = 0, modseq: UInt64 = 0
    private var qresync = false
    private var special: (all: Bytes?, trash: Bytes?)?
    private var watching = false, idling = false, closed = false
    private var timer: Task<Void, Never>?
    private var reconnecting: Task<Void, Never>?
    private var busy = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(_ config: IMAPStore.Configuration, idles: Bool, sink: Sink) {
        self.config = config; self.idles = idles; self.sink = sink
        folder = Bytes(config.folder.utf8)
    }

    // MARK: - Serialization

    private func acquire() async {
        if !busy { busy = true; return }
        await withCheckedContinuation { waiters.append($0) }
    }
    private func release() {
        if waiters.isEmpty { busy = false } else { waiters.removeFirst().resume() }
    }

    /// Runs body on a ready connection with the folder selected, then resumes IDLE if watching.
    func run<T: Sendable>(_ body: (Connection) async throws -> T) async throws -> T {
        guard !closed else { throw IMAPError.closed }
        await acquire()
        defer { release() }
        guard !closed else { throw IMAPError.closed }
        let c = try await ready()
        let result: Result<T, any Error>
        do { result = .success(try await body(c)) } catch { result = .failure(error) }
        if case .failure(let error) = result {
            guard Link.keepsConnection(error) else { await drop(c); throw error }
            selected = nil   // after a NO, select again: the folder may have been deleted under us
        }
        do { try await settle(c) } catch { await drop(c) }
        return try result.get()
    }

    /// run, repeated once after a NO (freshly selected) or a lost connection (freshly connected).
    /// Only for operations that are safe to repeat; an APPEND is not (§10.3, lost answers).
    func retrying<T: Sendable>(_ body: (Connection) async throws -> T) async throws -> T {
        do { return try await run(body) } catch where !Link.permanent(error) { return try await run(body) }
    }

    private static func keepsConnection(_ error: any Error) -> Bool {
        guard let error = error as? IMAPError else { return true }
        if case .rejected = error { return true }
        return false
    }
    private static func permanent(_ error: any Error) -> Bool {
        switch error as? IMAPError {
        case .authentication?, .unsupported?, .closed?: true
        default: false
        }
    }

    private func ready() async throws -> Connection {
        if let c = connection {
            do {
                if idling { idling = false; timer?.cancel(); try await c.done() }
                if selected != folder { try await selectFolder(c) }
                return c
            } catch where Link.keepsConnection(error) {
                throw error
            } catch {
                await drop(c)
            }
        }
        return try await connect()
    }

    private func settle(_ c: Connection) async throws {
        if selected != folder { try await selectFolder(c) }
        if watching, idles {
            try await c.idle()
            idling = true
            arm()
            if fresh { sink.changed() }
        }
        fresh = false
    }

    private func drop(_ c: Connection) async {
        guard connection === c else { await c.close(); return }
        connection = nil; idling = false; selected = nil
        timer?.cancel()
        await c.close()
        reconnect()
    }

    private func lost(_ id: Int) async {
        guard id == current, let c = connection else { return }
        await drop(c)
    }

    // MARK: - Connecting

    private func connect() async throws -> Connection {
        let transport = Transport(host: config.host, port: config.port, security: config.security)
        try await transport.open()
        serial += 1
        let id = serial, sink = self.sink
        let c = Connection(transport: transport, trace: config.trace) { [weak self] event in
            switch event {
            case .changed: sink.changed()
            case .closed: Task { await self?.lost(id) }
            }
        }
        do {
            let greeting = try await c.start()
            var caps: Set<String> = []
            if case .capability(let list)? = greeting.code { caps = Set(list) }
            if caps.isEmpty { caps = try await capability(c) }
            if greeting.kind != .preauth { caps = try await authenticate(c, caps) }
            await c.setCapabilities(caps)
            guard caps.contains("IMAP4REV2") || (caps.contains("UIDPLUS") && caps.contains("IDLE"))
            else { throw IMAPError.unsupported("the server lacks UIDPLUS or IDLE (§10.3)") }
            qresync = false
            if config.qresync, caps.contains("QRESYNC") {
                let r = try await c.execute([.text("ENABLE QRESYNC")])
                qresync = r.untagged.contains { if case .enabled(let e) = $0 { e.contains("QRESYNC") } else { false } }
            }
            if caps.contains("NAMESPACE") || caps.contains("IMAP4REV2") {
                let r = try await c.execute([.text("NAMESPACE")])
                for case .namespace(let personal) in r.untagged {
                    prefix = personal.first?.prefix ?? []
                    folder = prefix + Bytes(config.folder.utf8)
                }
            }
            self.caps = caps
            connection = c; current = id; fresh = true; selected = nil
            try await selectFolder(c)
            return c
        } catch {
            if connection === c { connection = nil }
            await c.close()
            throw error
        }
    }

    private func capability(_ c: Connection) async throws -> Set<String> {
        let r = try await c.execute([.text("CAPABILITY")])
        for case .capability(let list) in r.untagged { return Set(list) }
        throw IMAPError.protocol("no CAPABILITY response")
    }

    /// AUTHENTICATE PLAIN when offered, else LOGIN. Returns the capabilities after login.
    private func authenticate(_ c: Connection, _ caps: Set<String>) async throws -> Set<String> {
        let r: Connection.Result
        do {
            if caps.contains("AUTH=PLAIN") {
                let token = Bytes(Data([0] + Bytes(config.username.utf8) + [0] + Bytes(config.password.utf8)).base64EncodedString().utf8)
                if caps.contains("SASL-IR") || caps.contains("IMAP4REV2") {
                    r = try await c.execute([.text("AUTHENTICATE PLAIN "), .raw(token)], secret: true)
                } else {
                    r = try await c.execute([.text("AUTHENTICATE PLAIN"), .continuation(token)], secret: true)
                }
            } else if caps.contains("LOGINDISABLED") {
                throw IMAPError.unsupported("the server offers neither LOGIN nor AUTHENTICATE PLAIN")
            } else {
                r = try await c.execute([.text("LOGIN "), .astring(Bytes(config.username.utf8)), .text(" "), .astring(Bytes(config.password.utf8))],
                                        secret: true)
            }
        } catch IMAPError.rejected(let code, let text) {
            if code == "UNAVAILABLE" { throw IMAPError.connection("login unavailable: \(text)") }
            throw IMAPError.authentication(text)
        }
        if case .capability(let list)? = r.status.code { return Set(list) }
        for case .capability(let list) in r.untagged { return Set(list) }
        return try await capability(c)
    }

    // MARK: - Folder

    private func ensureFolder(_ c: Connection) async throws {
        let r = try await c.execute([.text("LIST \"\" "), .astring(folder)])
        let exists = r.untagged.contains {
            guard case .list(let flags, _, let name) = $0 else { return false }
            return name == folder && !flags.contains { $0.uppercased() == "\\NONEXISTENT" }
        }
        if exists { return }
        do { try await c.execute([.text("CREATE "), .astring(folder)]) }
        catch IMAPError.rejected(let code, _) where code == "ALREADYEXISTS" {}
    }

    /// Selects the Tandem folder, creating it if missing.
    @discardableResult
    private func selectFolder(_ c: Connection, since: Cursor? = nil) async throws -> Selection {
        do { return try await select(c, folder, since: since) }
        catch IMAPError.rejected { try await ensureFolder(c); return try await select(c, folder, since: since) }
    }

    /// SELECT, with QRESYNC parameters when enabled and the cursor has a modseq (RFC 7162 §3.2.5).
    @discardableResult
    private func select(_ c: Connection, _ name: Bytes, since: Cursor? = nil) async throws -> Selection {
        var parts: [Part] = [.text("SELECT "), .astring(name)]
        if qresync, let since, since.modseq > 0 {
            var p = "\(since.validity) \(since.modseq)"
            let known = since.known.description
            if !known.isEmpty, known.utf8.count <= 4000 { p += " " + known }   // else the server reports more; intersected below
            parts.append(.text(" (QRESYNC (\(p)))"))
        }
        selected = nil
        let r = try await c.execute(parts, selecting: true)
        var s = Selection(validity: 0, modseq: 0, vanished: UIDSet())
        for u in r.untagged {
            switch u {
            case .status(let status):
                if case .uidValidity(let v)? = status.code { s.validity = v }
                if case .highestModSeq(let m)? = status.code { s.modseq = m }
                if case .uidNext(let n)? = status.code { s.uidNext = n }
            case .vanished(earlier: true, let set): s.vanished = UIDSet(ranges: s.vanished.ranges + set.ranges)
            default: break
            }
        }
        guard s.validity != 0 else { throw IMAPError.protocol("SELECT without UIDVALIDITY") }
        selected = name
        if name == folder { validity = s.validity; modseq = s.modseq }
        return s
    }

    // MARK: - Watching

    private var restartPeriod: Duration {
        let refresh = Duration.seconds(29 * 60)
        return switch config.idleRestart {
        case .never: refresh
        case .every(let period): min(period, refresh)
        case .automatic: refresh
        }
    }

    private func arm() {
        timer?.cancel()
        let period = restartPeriod
        timer = Task { [weak self] in
            do { try await pause(period) } catch { return }
            await self?.refresh()
        }
    }

    /// Ends IDLE and starts it again: ready() sends DONE and settle() IDLE.
    private func refresh() async {
        guard idling, !closed else { return }
        _ = try? await run { _ in }
    }

    private func reconnect() {
        guard reconnecting == nil, watching, !closed else { return }
        reconnecting = Task { [weak self] in await self?.reconnectLoop() }
    }

    private func reconnectLoop() async {
        defer { reconnecting = nil }
        var delay = config.reconnectDelay.lowerBound
        while watching, !closed {
            do {
                try await run { _ in }
                if idling || !watching { return }
            } catch where Link.permanent(error) {
                watching = false
                sink.finish(error)
                return
            } catch {}
            do { try await pause(delay) } catch { return }
            delay = min(delay * 2, config.reconnectDelay.upperBound)
        }
    }

    /// Returns once IDLE runs, so no change after this call is missed.
    func startWatching() async throws {
        watching = true
        do { try await run { _ in } } catch { watching = false; throw error }
        if idles, !idling {
            watching = false
            reconnecting?.cancel()
            throw IMAPError.connection("could not start IDLE")
        }
    }

    func stopWatching() async {
        guard watching else { return }
        watching = false
        reconnecting?.cancel()
        if idling { _ = try? await run { _ in } }
    }

    func close() async {
        closed = true; watching = false
        timer?.cancel(); reconnecting?.cancel()
        if let c = connection { connection = nil; idling = false; await c.close() }
    }

    // MARK: - Store operations (§10.3)

    func ensureFolder() async throws { try await retrying { c in try await self.ensureFolder(c) } }

    func changes(since text: String) async throws -> ChangeBatch {
        let cursor = try Cursor(text)
        return try await retrying { c in try await self.scan(c, cursor) }
    }

    private func scan(_ c: Connection, _ cursor: Cursor?) async throws -> ChangeBatch {
        let page = try await scanFolder(c, cursor, fetch: fetchItems, decode: decode)
        return ChangeBatch(added: page.added, removed: page.removed, cursor: page.cursor, more: page.more)
    }

    /// One page of what changed in the folder since the cursor: the new messages that decode, in UID
    /// order, the IDs removed, the next cursor and whether more is waiting. The feed fetches bodies with
    /// it; the blob folder only the headers its index needs (BLOBS.md section 4.3).
    private func scanFolder<T>(_ c: Connection, _ cursor: Cursor?, fetch: (Connection, String) async throws -> [UInt32: FetchItem],
                               decode: (FetchItem, UInt32) -> T?) async throws -> (added: [T], removed: [String], cursor: String, more: Bool) {
        // SELECT every time: it is where UIDVALIDITY and HIGHESTMODSEQ are learned, and a
        // session may keep a stale view of a folder deleted and recreated by another client.
        var vanished: UIDSet?
        let s = try await selectFolder(c, since: cursor)
        if qresync, let cursor, cursor.modseq > 0, cursor.validity == s.validity { vanished = s.vanished }
        var removed: [String] = [], known = UIDSet(), last: UInt32 = 0
        if let cursor, cursor.validity == validity {
            known = cursor.known; last = cursor.last
            let gone: UIDSet
            if let vanished { gone = known.intersection(vanished) } else { gone = known.subtracting(try await searchAll(c)) }
            removed = gone.uids.map { MessageID.make(validity, $0) }
            known = known.subtracting(gone)
        } else if let cursor {
            removed = cursor.known.uids.map { MessageID.make(cursor.validity, $0) }   // UIDVALIDITY changed: full resync
        }
        var added: [T] = []
        var top = last, more = false
        if last < .max {
            // n:* always returns the last message, so a UID below the range is ignored.
            var set = "\(last + 1):*"
            // A page at a time: new UIDs lie below UIDNEXT, so only a wider gap than a page can hold more than one.
            // Then the new UIDs are searched, and the first page of them fetched.
            if s.uidNext == 0 || Int64(s.uidNext) - 1 - Int64(last) > Int64(config.pageSize) {
                var uids = try await search(c, "UID " + set).uids.filter { $0 > last }
                if uids.count > config.pageSize { uids = Array(uids.prefix(config.pageSize)); more = true }
                if let end = uids.last { top = end }   // past the page even if its last messages went meanwhile
                set = UIDSet(uids).description
            }
            let items = set.isEmpty ? [:] : try await fetch(c, set)
            for uid in items.keys.sorted() where uid > last {
                top = max(top, uid)
                guard let value = decode(items[uid]!, uid) else { continue }
                added.append(value)
                known.insert(uid)
            }
        }
        return (added, removed, Cursor(validity: validity, modseq: qresync ? modseq : 0, last: top, known: known).text, more)
    }

    private func searchAll(_ c: Connection) async throws -> UIDSet { try await search(c, "ALL") }

    /// UID SEARCH for a criterion: ALL, or UID and a set such as "n:*".
    private func search(_ c: Connection, _ criterion: String) async throws -> UIDSet {
        if caps.contains("ESEARCH") || caps.contains("IMAP4REV2") {
            let r = try await c.execute([.text("UID SEARCH RETURN (ALL) \(criterion)")])
            for case .esearch(_, _, let all) in r.untagged { return all }
            return UIDSet()
        }
        let r = try await c.execute([.text("UID SEARCH \(criterion)")])
        var out: [UInt32] = []
        for case .search(let uids) in r.untagged { out += uids }
        return UIDSet(out)
    }

    private func fetchItems(_ c: Connection, _ set: String) async throws -> [UInt32: FetchItem] {
        let fields = MessageFormat.fields.joined(separator: " ")
        return try await fetchWith(c, "UID FETCH \(set) (UID RFC822.SIZE BODY.PEEK[HEADER.FIELDS (\(fields))] BODY.PEEK[TEXT]<0.\(config.maxMessageSize)>)")
    }

    private func fetchWith(_ c: Connection, _ command: String) async throws -> [UInt32: FetchItem] {
        let r = try await c.execute([.text(command)])
        var items: [UInt32: FetchItem] = [:]
        for case .fetch(_, let item) in r.untagged {
            if let uid = item.uid { items[uid, default: FetchItem()].merge(item) }
        }
        return items
    }

    /// nil for a message that is too large or doesn't decode (§10.4: ignored).
    private func decode(_ item: FetchItem, _ uid: UInt32) -> Message? {
        guard let size = item.size, size <= UInt64(config.maxMessageSize),
              let header = item.sections["HEADER.FIELDS"], let text = item.sections["TEXT"],
              let (channel, envelope) = try? MessageFormat.decode(header: header, text: text) else { return nil }
        return Message(id: MessageID.make(validity, uid), channel: channel, body: envelope)
    }

    /// UID sets small enough for one command line.
    private func chunks(_ set: UIDSet) -> [String] {
        stride(from: 0, to: set.ranges.count, by: 200).map {
            UIDSet(ranges: Array(set.ranges[$0..<min($0 + 200, set.ranges.count)])).description
        }
    }

    private func current(_ ids: [String]) -> UIDSet {
        UIDSet(ids.compactMap(MessageID.parse).filter { $0.validity == validity }.map(\.uid))
    }

    /// Appends whole messages, several per command with MULTIAPPEND (RFC 3502). IDs in order;
    /// "" where the server named no UID.
    func append(_ messages: [Bytes]) async throws -> [String] {
        try await run { c in try await self.append(c, messages, to: self.folder) }
    }

    /// Appends to the named folder under the namespace prefix; APPEND needs none selected. A folder that
    /// doesn't exist yet is created on TRYCREATE and the append sent again: a NO stored nothing (§10.3).
    func append(_ messages: [Bytes], toFolderNamed name: String) async throws -> [String] {
        try await run { c in
            let folder = self.prefix + Bytes(name.utf8)
            do { return try await self.append(c, messages, to: folder) } catch IMAPError.rejected(let code, _) where code == "TRYCREATE" {
                do { try await c.execute([.text("CREATE "), .astring(folder)]) } catch IMAPError.rejected(let code, _) where code == "ALREADYEXISTS" {}
                return try await self.append(c, messages, to: folder)
            }
        }
    }

    private func append(_ c: Connection, _ messages: [Bytes], to folder: Bytes) async throws -> [String] {
        var ids: [String] = [], i = 0
        let multi = self.caps.contains("MULTIAPPEND")
        while i < messages.count {
            var batch = [messages[i]], size = messages[i].count
            i += 1
            while multi, i < messages.count, batch.count < 100, size + messages[i].count <= 8 << 20 {
                batch.append(messages[i]); size += messages[i].count; i += 1
            }
            var parts: [Part] = [.text("APPEND "), .astring(folder)]
            for m in batch { parts += [.text(" (\\Seen) "), .literal(m)] }
            let r = try await c.execute(parts)
            if case .appendUID(let v, let uids)? = r.status.code, uids.count == batch.count {
                ids += uids.map { MessageID.make(v, $0) }
            } else {
                ids += Array(repeating: "", count: batch.count)
            }
        }
        return ids
    }

    // MARK: - The blob folder (BLOBS.md section 4.3)

    /// One page of the folder's changes, from the headers alone: no body is fetched.
    func refs(since text: String) async throws -> BlobStore.Changes {
        let cursor = try Cursor(text)
        return try await retrying { c in
            let page = try await self.scanFolder(c, cursor, fetch: { c, set in
                try await self.fetchWith(c, "UID FETCH \(set) (UID BODY.PEEK[HEADER.FIELDS (Tandem-Channel Tandem-Blob)])")
            }, decode: self.decodeRef)
            return BlobStore.Changes(added: page.added, removed: page.removed, cursor: page.cursor, more: page.more)
        }
    }

    private func decodeRef(_ item: FetchItem, _ uid: UInt32) -> BlobStore.Ref? {
        guard let header = item.sections["HEADER.FIELDS"], let tag = MessageFormat.field("Tandem-Channel", in: header), !tag.isEmpty,
              let channel = String(validating: tag, as: UTF8.self), let claim = MessageFormat.field("Tandem-Blob", in: header),
              let address = B32.decode(String(decoding: claim, as: UTF8.self)), address.count == 32 else { return nil }
        return BlobStore.Ref(id: MessageID.make(validity, uid), channel: channel, address: address)
    }

    /// Chunk messages by ID; those that are gone, too large or don't decode are left out.
    func chunks(ids: [String]) async throws -> [BlobStore.Chunk] {
        try await retrying { c in
            let uids = self.current(ids)
            let fields = ChunkFormat.fields.joined(separator: " ")
            var out: [BlobStore.Chunk] = []
            for set in self.chunks(uids) {
                let items = try await self.fetchWith(c, "UID FETCH \(set) (UID RFC822.SIZE BODY.PEEK[HEADER.FIELDS (\(fields))] BODY.PEEK[TEXT]<0.\(self.config.maxMessageSize)>)")
                for uid in items.keys.sorted() where uids.contains(uid) {
                    let item = items[uid]!
                    guard let size = item.size, size <= UInt64(self.config.maxMessageSize), let header = item.sections["HEADER.FIELDS"],
                          let text = item.sections["TEXT"], let envelope = try? ChunkFormat.decode(header: header, text: text) else { continue }
                    out.append(BlobStore.Chunk(id: MessageID.make(self.validity, uid), envelope: envelope))
                }
            }
            return out
        }
    }

    /// The storage quota of the folder's root; unknown where the server has no QUOTA or reports no storage.
    func quota() async throws -> BlobStore.Quota {
        try await retrying { c in
            guard self.caps.contains("QUOTA") else { return BlobStore.Quota.unknown }
            let r: Connection.Result
            do { r = try await c.execute([.text("GETQUOTAROOT "), .astring(self.folder)]) } catch IMAPError.rejected { return BlobStore.Quota.unknown }
            for case .quota(_, let resources) in r.untagged {
                if let storage = resources.first(where: { $0.name == "STORAGE" }) {
                    return BlobStore.Quota.known(used: Int64(storage.usage) * 1024, limit: Int64(storage.limit) * 1024)
                }
            }
            return BlobStore.Quota.unknown
        }
    }

    /// Makes sure the connection is up; for a transfer connection being opened.
    func connectNow() async throws { try await run { _ in () } }

    func fetch(ids: [String]) async throws -> [Message] {
        try await retrying { c in
            let uids = self.current(ids)
            var out: [Message] = []
            for chunk in self.chunks(uids) {
                let items = try await self.fetchItems(c, chunk)
                out += items.keys.sorted().filter(uids.contains).compactMap { self.decode(items[$0]!, $0) }
            }
            return out
        }
    }

    /// UID STORE \Deleted and UID EXPUNGE; on Gmail, then the copy in All Mail goes to Trash and is expunged there.
    func destroy(ids: [String]) async throws {
        try await retrying { c in
            let uids = self.current(ids)
            let gmail = self.caps.contains("X-GM-EXT-1")
            var gmailIDs: [UInt64] = []
            for chunk in self.chunks(uids) {
                if gmail {
                    let r = try await c.execute([.text("UID FETCH \(chunk) (UID X-GM-MSGID)")])
                    for case .fetch(_, let item) in r.untagged {
                        if let id = item.gmailID, let uid = item.uid, uids.contains(uid) { gmailIDs.append(id) }
                    }
                }
                try await c.execute([.text("UID STORE \(chunk) +FLAGS.SILENT (\\Deleted)")])
                try await c.execute([.text("UID EXPUNGE \(chunk)")])
            }
            if !gmailIDs.isEmpty { try await self.purgeGmail(c, gmailIDs) }
        }
    }

    private func specialUse(_ c: Connection) async throws -> (all: Bytes?, trash: Bytes?) {
        if let special { return special }
        let r = try await c.execute([.text("LIST \"\" \"*\"")])
        var found: (all: Bytes?, trash: Bytes?) = (nil, nil)
        for case .list(let flags, _, let name) in r.untagged {
            let f = Set(flags.map { $0.uppercased() })
            if f.contains("\\ALL") { found.all = name }
            if f.contains("\\TRASH") { found.trash = name }
        }
        special = found
        return found
    }

    private func purgeGmail(_ c: Connection, _ ids: [UInt64]) async throws {
        let (all, trash) = try await specialUse(c)
        guard let all, let trash else { throw IMAPError.unsupported("Gmail without \\All and \\Trash folders") }
        try await select(c, all)
        let inAll = try await searchGmail(c, ids)
        var inTrash = UIDSet()
        for chunk in chunks(inAll) {
            let r: Connection.Result
            if caps.contains("MOVE") {
                r = try await c.execute([.text("UID MOVE \(chunk) "), .astring(trash)])
            } else {
                r = try await c.execute([.text("UID COPY \(chunk) "), .astring(trash)])
                try await c.execute([.text("UID STORE \(chunk) +FLAGS.SILENT (\\Deleted)")])
                try await c.execute([.text("UID EXPUNGE \(chunk)")])
            }
            let codes = [r.status.code] + r.untagged.map { if case .status(let s) = $0 { s.code } else { nil } }
            for case .copyUID(_, _, let destination)? in codes { inTrash = UIDSet(ranges: inTrash.ranges + destination.ranges) }
        }
        try await select(c, trash)
        inTrash = UIDSet(ranges: inTrash.ranges + (try await searchGmail(c, ids)).ranges)
        for chunk in chunks(inTrash) {
            try await c.execute([.text("UID STORE \(chunk) +FLAGS.SILENT (\\Deleted)")])
            try await c.execute([.text("UID EXPUNGE \(chunk)")])
        }
    }

    private func searchGmail(_ c: Connection, _ ids: [UInt64]) async throws -> UIDSet {
        var out: [UInt32] = []
        for start in stride(from: 0, to: ids.count, by: 20) {
            let group = ids[start..<min(start + 20, ids.count)]
            let query = group.dropLast().map { "OR X-GM-MSGID \($0) " }.joined() + "X-GM-MSGID \(group.last!)"
            let r = try await c.execute([.text("UID SEARCH \(query)")])
            for case .search(let uids) in r.untagged { out += uids }
            for case .esearch(_, _, let all) in r.untagged { out += all.uids }
        }
        return UIDSet(out)
    }

    /// Test hook: one raw command on the ready connection.
    func command(_ text: String) async throws -> [Untagged] {
        try await run { c in try await c.execute([.text(text)]).untagged }
    }
    /// Test hook: what the server offered.
    var capabilities: Set<String> { caps }
}
