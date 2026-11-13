import Foundation
import SQLite3
import Tandem

/// A device's durable state (§11.6): one SQLite file, written in transactions
/// with full synchronous commits, so a crash at any point leaves the last
/// committed state. SQLite is the system library on Apple platforms.
///
/// - `values`: small named values (the hub pin, the cursor, the clock).
/// - `objects`: every control object held, by ID, as its exact JSON.
/// - `messages`: the hub messages seen: their channel, envelope hash and whether they were applied.
/// - `writes`: every record write held, by write ID, with its scope, record and epoch.
/// - `journal`: exact bytes signed for a step that hasn't finished exporting.
public final class Store: @unchecked Sendable {
    private var db: OpaquePointer?
    private let lock = NSLock()

    public static let schema = """
        PRAGMA journal_mode = WAL;
        PRAGMA synchronous = FULL;
        CREATE TABLE IF NOT EXISTS values_ (name TEXT PRIMARY KEY, value BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS objects (id BLOB PRIMARY KEY, kind TEXT NOT NULL, json BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS messages (id TEXT PRIMARY KEY, channel TEXT NOT NULL, hash BLOB NOT NULL,
            applied INTEGER NOT NULL DEFAULT 0);
        CREATE INDEX IF NOT EXISTS messages_hash ON messages (hash);
        CREATE TABLE IF NOT EXISTS writes (id BLOB PRIMARY KEY, scope BLOB NOT NULL, record BLOB NOT NULL,
            epoch BLOB NOT NULL, payload BLOB NOT NULL);
        CREATE INDEX IF NOT EXISTS writes_record ON writes (scope, record);
        CREATE TABLE IF NOT EXISTS journal (step TEXT PRIMARY KEY, bytes BLOB NOT NULL);
        CREATE TABLE IF NOT EXISTS held (id TEXT PRIMARY KEY, channel TEXT NOT NULL, body BLOB NOT NULL);
        """

    /// Open (or create) the store at `path`; ":memory:" for tests.
    public init(path: String) throws {
        guard sqlite3_open_v2(path, &db, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_FULLMUTEX, nil) == SQLITE_OK
        else { throw Self.error(db, "open") }
        try exec(Self.schema)
    }

    deinit { sqlite3_close(db) }

    static func error(_ db: OpaquePointer?, _ what: String) -> TandemError {
        .invalid("store: \(what): \(db.map { String(cString: sqlite3_errmsg($0)) } ?? "no database")")
    }

    private func exec(_ sql: String) throws {
        guard sqlite3_exec(db, sql, nil, nil, nil) == SQLITE_OK else { throw Self.error(db, "exec") }
    }

    /// Run `body` in one transaction: all of it is committed, or none of it.
    public func transaction<T>(_ body: () throws -> T) throws -> T {
        lock.lock(); defer { lock.unlock() }
        try exec("BEGIN IMMEDIATE")
        do {
            let result = try body()
            try exec("COMMIT")
            return result
        } catch {
            try? exec("ROLLBACK")
            throw error
        }
    }

    enum Arg { case text(String), blob(Bytes), int(Int64) }

    private func run(_ sql: String, _ args: [Arg], _ row: ((OpaquePointer) -> Void)? = nil) throws {
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { throw Self.error(db, "prepare") }
        defer { sqlite3_finalize(stmt) }
        let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)
        for (i, a) in args.enumerated() {
            let n = Int32(i + 1)
            switch a {
            case let .text(s): sqlite3_bind_text(stmt, n, s, -1, transient)
            case let .blob(b): b.withUnsafeBytes { _ = sqlite3_bind_blob(stmt, n, $0.baseAddress, Int32(b.count), transient) }
            case let .int(v): sqlite3_bind_int64(stmt, n, v)
            }
        }
        while true {
            switch sqlite3_step(stmt) {
            case SQLITE_ROW: row?(stmt!)
            case SQLITE_DONE: return
            default: throw Self.error(db, "step")
            }
        }
    }

    private static func blob(_ stmt: OpaquePointer, _ col: Int32) -> Bytes {
        let n = Int(sqlite3_column_bytes(stmt, col))
        guard n > 0, let p = sqlite3_column_blob(stmt, col) else { return [] }
        return Bytes(UnsafeRawBufferPointer(start: p, count: n))
    }

    private static func text(_ stmt: OpaquePointer, _ col: Int32) -> String {
        sqlite3_column_text(stmt, col).map { String(cString: $0) } ?? ""
    }

    // MARK: Values

    public func value(_ name: String) throws -> Bytes? {
        var out: Bytes?
        try run("SELECT value FROM values_ WHERE name = ?", [.text(name)]) { out = Self.blob($0, 0) }
        return out
    }

    public func setValue(_ name: String, _ value: Bytes) throws {
        try run("INSERT INTO values_ (name, value) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value",
                [.text(name), .blob(value)])
    }

    public func removeValue(_ name: String) throws { try run("DELETE FROM values_ WHERE name = ?", [.text(name)]) }

    /// Every named value whose name starts with `prefix`.
    public func values(prefix: String) throws -> [(name: String, value: Bytes)] {
        var out: [(String, Bytes)] = []
        try run("SELECT name, value FROM values_ WHERE substr(name, 1, ?) = ? ORDER BY name", [.int(Int64(prefix.utf8.count)), .text(prefix)]) {
            out.append((Self.text($0, 0), Self.blob($0, 1)))
        }
        return out
    }

    // MARK: Control objects

    /// Keep a control object's exact JSON; returns false if it was already held.
    @discardableResult
    public func putObject(_ object: ControlObject) throws -> Bool {
        let id = try object.id()
        var held = false
        try run("SELECT 1 FROM objects WHERE id = ?", [.blob(id)]) { _ in held = true }
        guard !held else { return false }
        try run("INSERT INTO objects (id, kind, json) VALUES (?, ?, ?)", [.blob(id), .text(object.kind), .blob(try object.json())])
        return true
    }

    public func objects(kind: String? = nil) throws -> [ControlObject] {
        var rows: [Bytes] = []
        if let kind {
            try run("SELECT json FROM objects WHERE kind = ? ORDER BY rowid", [.text(kind)]) { rows.append(Self.blob($0, 0)) }
        } else {
            try run("SELECT json FROM objects ORDER BY rowid", []) { rows.append(Self.blob($0, 0)) }
        }
        return try rows.map { try ControlObject.parse($0) }
    }

    // MARK: Hub messages

    public func noteMessage(id: String, channel: String, hash: Bytes) throws {
        try run("INSERT OR IGNORE INTO messages (id, channel, hash) VALUES (?, ?, ?)", [.text(id), .text(channel), .blob(hash)])
    }

    public func markApplied(_ id: String) throws { try run("UPDATE messages SET applied = 1 WHERE id = ?", [.text(id)]) }

    public func forgetMessages(_ ids: [String]) throws {
        for id in ids { try run("DELETE FROM messages WHERE id = ?", [.text(id)]) }
    }

    /// Hub message IDs holding an envelope with this hash (§10.3: own messages are known by hash).
    public func messages(withHash hash: Bytes) throws -> [String] {
        var out: [String] = []
        try run("SELECT id FROM messages WHERE hash = ?", [.blob(hash)]) { out.append(Self.text($0, 0)) }
        return out
    }

    public func unapplied() throws -> [(id: String, channel: String)] {
        var out: [(String, String)] = []
        try run("SELECT id, channel FROM messages WHERE applied = 0 ORDER BY rowid", []) { out.append((Self.text($0, 0), Self.text($0, 1))) }
        return out
    }

    // MARK: Writes

    @discardableResult
    public func putWrite(id: Bytes, scope: Bytes, record: Bytes, epoch: Bytes, payload: Bytes) throws -> Bool {
        var held = false
        try run("SELECT 1 FROM writes WHERE id = ?", [.blob(id)]) { _ in held = true }
        guard !held else { return false }
        try run("INSERT INTO writes (id, scope, record, epoch, payload) VALUES (?, ?, ?, ?, ?)",
                [.blob(id), .blob(scope), .blob(record), .blob(epoch), .blob(payload)])
        return true
    }

    public func writes(scope: Bytes, record: Bytes) throws -> [(epoch: Bytes, payload: Bytes)] {
        var out: [(Bytes, Bytes)] = []
        try run("SELECT epoch, payload FROM writes WHERE scope = ? AND record = ? ORDER BY rowid", [.blob(scope), .blob(record)]) {
            out.append((Self.blob($0, 0), Self.blob($0, 1)))
        }
        return out
    }

    public func records(scope: Bytes) throws -> [Bytes] {
        var out: [Bytes] = []
        try run("SELECT DISTINCT record FROM writes WHERE scope = ?", [.blob(scope)]) { out.append(Self.blob($0, 0)) }
        return out
    }

    // MARK: Held messages (§8.3): readable, but waiting for something they depend on

    public func hold(_ m: Message) throws {
        try run("INSERT OR IGNORE INTO held (id, channel, body) VALUES (?, ?, ?)", [.text(m.id), .text(m.channel), .blob(m.body)])
    }

    public func held() throws -> [Message] {
        var out: [Message] = []
        try run("SELECT id, channel, body FROM held ORDER BY rowid", []) {
            out.append(Message(id: Self.text($0, 0), channel: Self.text($0, 1), body: Self.blob($0, 2)))
        }
        return out
    }

    public func release(_ id: String) throws { try run("DELETE FROM held WHERE id = ?", [.text(id)]) }

    // MARK: Journal (§11.6)

    public func journal(_ step: String) throws -> Bytes? {
        var out: Bytes?
        try run("SELECT bytes FROM journal WHERE step = ?", [.text(step)]) { out = Self.blob($0, 0) }
        return out
    }

    public func setJournal(_ step: String, _ bytes: Bytes) throws {
        try run("INSERT INTO journal (step, bytes) VALUES (?, ?) ON CONFLICT (step) DO UPDATE SET bytes = excluded.bytes",
                [.text(step), .blob(bytes)])
    }

    public func clearJournal(_ step: String) throws { try run("DELETE FROM journal WHERE step = ?", [.text(step)]) }

    public func journalSteps() throws -> [String] {
        var out: [String] = []
        try run("SELECT step FROM journal ORDER BY rowid", []) { out.append(Self.text($0, 0)) }
        return out
    }
}
