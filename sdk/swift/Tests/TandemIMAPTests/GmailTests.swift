import Foundation
import Tandem
@testable import TandemIMAP
import XCTest

/// A scripted stand-in for Gmail, as far as §10.3 describes it: X-GM-EXT-1, no QRESYNC,
/// MULTIAPPEND, LITERAL+ or ESEARCH, LOGIN only, and a label whose expunged messages
/// stay in [Gmail]/All Mail until moved to [Gmail]/Trash and expunged there.
final class FakeGmail: @unchecked Sendable {
    struct Stored { var raw: Bytes; var gmailID: UInt64; var deleted = false }
    static let caps = "IMAP4rev1 IDLE NAMESPACE X-GM-EXT-1 UIDPLUS MOVE CONDSTORE SPECIAL-USE"
    private let lock = NSLock()
    private var boxes: [String: [UInt32: Stored]] = ["Tandem": [:], "[Gmail]/All Mail": [:], "[Gmail]/Trash": [:]]
    private var next: [String: UInt32] = ["Tandem": 1, "[Gmail]/All Mail": 100, "[Gmail]/Trash": 1]
    private let validity = ["Tandem": 11, "[Gmail]/All Mail": 2, "[Gmail]/Trash": 3]
    private var nextGmailID: UInt64 = 5000
    private var selected = ""
    /// The message count this session was last told about, so NOOP reports only a change.
    private var reported = 0
    /// A message another client appends right after the next body fetch is answered. Like
    /// Stalwart, the server tells a session of it on its next command, not when IDLE starts.
    var appendAfterNextFetch: Bytes?
    var created = false
    var caps = FakeGmail.caps

    /// Gmail IDs anywhere in the account.
    func gmailIDs(in box: String) -> Set<UInt64> { lock.withLock { Set(boxes[box]!.values.map(\.gmailID)) } }

    private func unquote(_ s: Substring) -> String { s.hasPrefix("\"") ? String(s.dropFirst().dropLast()) : String(s) }

    /// UIDs a set names in the selected box; "n:*" past the end names the last message, as in IMAP.
    private func uids(_ text: Substring) -> [UInt32] {
        let box = boxes[selected]!
        let top = box.keys.max() ?? 0
        guard let set = try? UIDSet(parsing: text.replacingOccurrences(of: "*", with: String(Swift.max(top, 1)))) else { return [] }
        return box.keys.sorted().filter(set.contains)
    }

    private func seq(_ uid: UInt32) -> Int { (boxes[selected]!.keys.sorted().firstIndex(of: uid) ?? 0) + 1 }

    private func add(_ raw: Bytes, to name: String, id: UInt64) -> UInt32 {
        let uid = next[name]!
        next[name] = uid + 1
        boxes[name]![uid] = Stored(raw: raw, gmailID: id)
        return uid
    }

    func respond(_ c: FakeServer.Command) -> [String] {
        lock.withLock { answer(c) }
    }

    private func answer(_ c: FakeServer.Command) -> [String] {
        let t = c.text
        if t == "CAPABILITY" { return ["* CAPABILITY \(caps)", "OK Thats all she wrote!"] }
        if t.hasPrefix("LOGIN ") {
            return t == "LOGIN alice \"pass word\"" ? ["OK [CAPABILITY \(caps)] alice authenticated (Success)"]
                : ["NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)"]
        }
        if t == "NAMESPACE" { return ["* NAMESPACE ((\"\" \"/\")) NIL NIL", "OK Success"] }
        if t == "LIST \"\" Tandem" { return (created ? ["* LIST (\\HasNoChildren) \"/\" \"Tandem\""] : []) + ["OK Success"] }
        if t == "CREATE Tandem" { created = true; return ["NO [ALREADYEXISTS] Duplicate folder name Tandem (Failure)"] }   // lost a race: fine
        if t == "LIST \"\" \"*\"" {
            return ["* LIST (\\HasNoChildren) \"/\" \"INBOX\"", "* LIST (\\All \\HasNoChildren) \"/\" \"[Gmail]/All Mail\"",
                    "* LIST (\\HasNoChildren \\Trash) \"/\" \"[Gmail]/Trash\"", "* LIST (\\HasNoChildren) \"/\" \"Tandem\"", "OK Success"]
        }
        if t.hasPrefix("SELECT ") {
            let name = unquote(t.dropFirst(7))
            guard boxes[name] != nil, name != "Tandem" || created else { return ["NO [NONEXISTENT] Unknown Mailbox: \(name)"] }
            selected = name
            reported = boxes[name]!.count
            return ["* FLAGS (\\Answered \\Flagged \\Draft \\Deleted \\Seen)", "* OK [UIDVALIDITY \(validity[name]!)] UIDs valid.",
                    "* \(boxes[name]!.count) EXISTS", "* OK [UIDNEXT \(next[name]!)] Predicted next UID.", "OK [READ-WRITE] \(name) selected. (Success)"]
        }
        if t.hasPrefix("APPEND Tandem (\\Seen) {"), c.literals.count == 1 {
            let id = nextGmailID
            nextGmailID += 1
            let uid = add(c.literals[0], to: "Tandem", id: id)
            _ = add(c.literals[0], to: "[Gmail]/All Mail", id: id)   // every message is also in All Mail
            guard selected == "Tandem" else { return ["OK [APPENDUID 11 \(uid)] (Success)"] }
            reported = boxes["Tandem"]!.count
            return ["* \(reported) EXISTS", "OK [APPENDUID 11 \(uid)] (Success)"]
        }
        guard !selected.isEmpty else { return ["BAD No mailbox selected"] }
        if t == "UID SEARCH ALL" { return ["* SEARCH" + boxes[selected]!.keys.sorted().map { " \($0)" }.joined(), "OK SEARCH completed (Success)"] }
        if t.hasPrefix("UID SEARCH "), t.contains("X-GM-MSGID") {
            let wanted = Set(t.matches(of: /X-GM-MSGID (\d+)/).map { UInt64($0.1)! })
            let found = boxes[selected]!.filter { wanted.contains($0.value.gmailID) }.keys.sorted()
            return ["* SEARCH" + found.map { " \($0)" }.joined(), "OK SEARCH completed (Success)"]
        }
        if let m = t.wholeMatch(of: /UID FETCH (\S+) \(UID X-GM-MSGID\)/) {
            return uids(m.1).map { "* \(seq($0)) FETCH (X-GM-MSGID \(boxes[selected]![$0]!.gmailID) UID \($0))" } + ["OK Success"]
        }
        if let m = t.wholeMatch(of: /UID FETCH (\S+) \(UID RFC822\.SIZE BODY\.PEEK\[HEADER\.FIELDS \(Tandem-Channel Content-Type Content-Transfer-Encoding\)\] BODY\.PEEK\[TEXT\]<0\.\d+>\)/) {
            defer {
                if let raw = appendAfterNextFetch {
                    appendAfterNextFetch = nil
                    nextGmailID += 1
                    _ = add(raw, to: "Tandem", id: nextGmailID)
                }
            }
            return uids(m.1).map { uid in
                let raw = boxes[selected]![uid]!.raw
                let split = (0..<(raw.count - 3)).first { Array(raw[$0..<($0 + 4)]) == Bytes("\r\n\r\n".utf8) }!
                let head = Array(raw[..<(split + 2)]), text = Array(raw[(split + 4)...])
                let fields = String(decoding: head, as: UTF8.self).components(separatedBy: "\r\n").filter { line in
                    ["tandem-channel:", "content-type:", "content-transfer-encoding:"].contains { line.lowercased().hasPrefix($0) }
                }.map { $0 + "\r\n" }.joined() + "\r\n"
                return "* \(seq(uid)) FETCH (UID \(uid) RFC822.SIZE \(raw.count) BODY[HEADER.FIELDS (\"Tandem-Channel\" \"Content-Type\" \"Content-Transfer-Encoding\")] "
                    + "{\(fields.utf8.count)}\r\n\(fields) BODY[TEXT]<0> {\(text.count)}\r\n\(String(decoding: text, as: UTF8.self)))"
            } + ["OK Success"]
        }
        if let m = t.wholeMatch(of: /UID STORE (\S+) \+FLAGS\.SILENT \(\\Deleted\)/) {
            for uid in uids(m.1) { boxes[selected]![uid]!.deleted = true }
            return ["OK Success"]
        }
        if let m = t.wholeMatch(of: /UID EXPUNGE (\S+)/) {
            var out: [String] = []
            for uid in uids(m.1).reversed() where boxes[selected]![uid]!.deleted {
                out.append("* \(seq(uid)) EXPUNGE")
                boxes[selected]![uid] = nil
            }
            return out + ["OK Success"]
        }
        if let m = t.wholeMatch(of: /UID MOVE (\S+) "\[Gmail\]\/Trash"/), selected == "[Gmail]/All Mail" {
            let moving = uids(m.1)
            var destination: [UInt32] = [], out: [String] = []
            for uid in moving { destination.append(add(boxes[selected]![uid]!.raw, to: "[Gmail]/Trash", id: boxes[selected]![uid]!.gmailID)) }
            out.append("* OK [COPYUID 3 \(UIDSet(moving)) \(UIDSet(destination))]")
            for uid in moving.reversed() { out.append("* \(seq(uid)) EXPUNGE"); boxes[selected]![uid] = nil }
            return out + ["OK Success"]
        }
        if t == "NOOP" {
            let count = boxes[selected]!.count
            defer { reported = count }
            return (count != reported ? ["* \(count) EXISTS"] : []) + ["OK Success"]
        }
        return ["BAD Unknown command \(t)"]
    }
}

final class GmailTests: XCTestCase {
    var gmail: FakeGmail!
    var server: FakeServer!

    override func setUp() async throws {
        let gmail = FakeGmail()
        self.gmail = gmail
        server = try FakeServer(greeting: "* OK Gimap ready for requests from 127.0.0.1") { gmail.respond($0) }
        try await server.start()
    }
    override func tearDown() { server.stop() }

    func config(password: String = "pass word", idleRestart: IMAPStore.IdleRestart = .automatic) -> IMAPStore.Configuration {
        IMAPStore.Configuration(host: "127.0.0.1", port: server.port, username: "alice", password: password, security: .plaintextForTesting,
                                idleRestart: idleRestart, reconnectDelay: .milliseconds(50) ... .milliseconds(200))
    }

    /// The commands after logging in, without tags.
    func commands() -> [String] { server.log.map { String($0.split(separator: " ", maxSplits: 1).last ?? "") } }

    func testAppendChangesAndDestroyCleanUpAllMailAndTrash() async throws {
        let store = IMAPStore(config())
        defer { Task { await store.close() } }
        try await store.ensureFolder()   // SELECT fails, LIST finds nothing, CREATE says ALREADYEXISTS: success
        let first = try await store.append(channel: "chan", body: [1, 2, 3])
        let more = try await store.append([(channel: "chan", body: [4]), (channel: "other", body: [5])])
        XCTAssertEqual([first] + more, ["11.1", "11.2", "11.3"])
        // No LITERAL+: every message waits for "+"; no MULTIAPPEND: one APPEND each.
        XCTAssertEqual(commands().filter { $0.hasPrefix("APPEND") }.count, 3)
        XCTAssertTrue(commands().filter { $0.hasPrefix("APPEND") }.allSatisfy { $0.wholeMatch(of: /APPEND Tandem \(\\Seen\) \{\d+\}/) != nil })

        let batch = try await store.changes(since: "")
        XCTAssertEqual(batch.added, [Message(id: "11.1", channel: "chan", body: [1, 2, 3]), Message(id: "11.2", channel: "chan", body: [4]),
                                     Message(id: "11.3", channel: "other", body: [5])])
        let again = try await store.changes(since: batch.cursor)   // "4:*" returns UID 3 again
        XCTAssertEqual(again.added, [])
        XCTAssertTrue(commands().contains("UID FETCH 4:* (UID RFC822.SIZE BODY.PEEK[HEADER.FIELDS (Tandem-Channel Content-Type Content-Transfer-Encoding)] BODY.PEEK[TEXT]<0.2097152>)"))

        let before = server.log.count
        try await store.destroy(ids: ["11.1", "11.3"])
        XCTAssertEqual(Array(commands()[before...]), [
            "UID FETCH 1,3 (UID X-GM-MSGID)", "UID STORE 1,3 +FLAGS.SILENT (\\Deleted)", "UID EXPUNGE 1,3",
            "LIST \"\" \"*\"", "SELECT \"[Gmail]/All Mail\"", "UID SEARCH OR X-GM-MSGID 5000 X-GM-MSGID 5002",
            "UID MOVE 100,102 \"[Gmail]/Trash\"", "SELECT \"[Gmail]/Trash\"", "UID SEARCH OR X-GM-MSGID 5000 X-GM-MSGID 5002",
            "UID STORE 1:2 +FLAGS.SILENT (\\Deleted)", "UID EXPUNGE 1:2", "SELECT Tandem"])
        XCTAssertEqual(gmail.gmailIDs(in: "Tandem"), [5001])
        XCTAssertEqual(gmail.gmailIDs(in: "[Gmail]/All Mail"), [5001])
        XCTAssertEqual(gmail.gmailIDs(in: "[Gmail]/Trash"), [])

        let gone = try await store.changes(since: again.cursor)   // no QRESYNC: UID SEARCH ALL against the known set
        XCTAssertEqual(gone.removed, ["11.1", "11.3"])
        let left = try await store.fetch(ids: ["11.1", "11.2", "11.3", "12.2"])
        XCTAssertEqual(left, [Message(id: "11.2", channel: "chan", body: [4])])
    }

    func testExplicitIdleRestartEveryThreeSeconds() async throws {
        let store = IMAPStore(config(idleRestart: .every(.seconds(3))))
        let log = EventLog.follow(try await store.watch())
        let t0 = ContinuousClock.now
        server.push(["* 4 EXISTS"])
        let pushed = await log.first(after: t0, within: .seconds(2))
        XCTAssertNotNil(pushed)
        try await Task.sleep(for: .milliseconds(6500))
        let idles = commands().filter { $0 == "IDLE" }.count, dones = server.log.filter { $0 == "DONE" }.count
        XCTAssertEqual(dones, 2, "restarted at 3 s and 6 s")
        XCTAssertEqual(idles, 3)
        await store.close()
        let ended = await log.finished(within: .seconds(2))
        XCTAssertTrue(ended)
    }

    func testAutomaticKeepsIdleOpenOnGmail() async throws {
        let store = IMAPStore(config(idleRestart: .automatic))
        let stream = try await store.watch()
        try await Task.sleep(for: .milliseconds(3500))
        XCTAssertEqual(server.log.filter { $0 == "DONE" }.count, 0)
        withExtendedLifetime(stream) {}
        // Dropping the stream ends IDLE.
        let before = server.log.count
        await store.close()
        XCTAssertEqual(server.log.count, before)
    }

    func testDroppingTheWatchStreamEndsIdle() async throws {
        let store = IMAPStore(config(idleRestart: .never))
        defer { Task { await store.close() } }
        _ = try await store.watch()
        try await Task.sleep(for: .milliseconds(300))
        XCTAssertEqual(server.log.suffix(2).map { String($0.split(separator: " ").last ?? "") }, ["IDLE", "DONE"])
    }

    func testReconnectsAfterTheServerHangsUp() async throws {
        let store = IMAPStore(config(idleRestart: .never))
        defer { Task { await store.close() } }
        let log = EventLog.follow(try await store.watch())
        try await Task.sleep(for: .milliseconds(100))
        let t0 = ContinuousClock.now
        server.disconnectAll()
        let back = await log.first(after: t0, within: .seconds(5))
        XCTAssertNotNil(back, "an invalidation after reconnecting")
        XCTAssertEqual(commands().filter { $0.hasPrefix("LOGIN") }.count, 2)
        XCTAssertEqual(commands().last, "IDLE")
        let t1 = ContinuousClock.now
        server.push(["* 9 EXISTS"])
        let pushed = await log.first(after: t1, within: .seconds(2))
        XCTAssertNotNil(pushed)
    }

    /// A message appended after the last command of a reconcile and before IDLE starts.
    /// Stalwart (measured) and this fake report it only on the next command, so IDLE alone
    /// would leave it unseen until some later command: IDLE starts with a NOOP before it.
    func testChangeBetweenCommandAndIdleIsReported() async throws {
        gmail.caps = "IMAP4rev1 IDLE NAMESPACE UIDPLUS MOVE"   // not Gmail: no IDLE restart to hide the gap
        let store = IMAPStore(config())
        try await store.ensureFolder()
        let session = try Session(store: store, capacity: 8)
        defer { Task { await session.close() } }
        let updates = session.updates
        try await session.start()
        _ = try await within(.seconds(3)) { try await updates.first { _ in true } }   // the initial batch
        try await idleSettles()
        // A push wakes the session; its reconcile ends with the body fetch, right after which
        // another client appends. Nothing else wakes the session afterwards.
        gmail.appendAfterNextFetch = try MessageFormat.build(channel: "gap", envelope: [9, 9])
        server.push(["* 0 EXISTS"])
        let seen = try await within(.seconds(3)) {
            var added: [Message] = []
            for try await batch in updates {
                added += batch.added
                if !added.isEmpty { break }
            }
            return added
        }
        XCTAssertEqual(seen, [Message(id: "11.1", channel: "gap", body: [9, 9])])
    }

    /// Waits until the client has idled, with nothing else sent, for 200 ms.
    func idleSettles() async throws {
        try await within(.seconds(3)) { [server] in
            while true {
                let before = server!.log
                try await Task.sleep(for: .milliseconds(200))
                if before.last?.hasSuffix(" IDLE") == true, server!.log == before { return }
            }
        }
    }

    func testReadsRetryOnceAfterTheServerHangsUp() async throws {
        let store = IMAPStore(config())
        defer { Task { await store.close() } }
        let first = try await store.changes(since: "")
        server.disconnectAll()
        let second = try await store.changes(since: first.cursor)
        XCTAssertEqual(second.cursor, first.cursor)
        XCTAssertEqual(commands().filter { $0.hasPrefix("LOGIN") }.count, 2)
    }

    func testRefusedCredentialsEndWatchingAndFailCommands() async throws {
        let store = IMAPStore(config(password: "wrong"))
        do { _ = try await store.changes(since: ""); XCTFail("accepted") } catch IMAPError.authentication(let text) {
            XCTAssertTrue(text.contains("Invalid credentials"))
        }
        do { _ = try await store.watch(); XCTFail("accepted") } catch IMAPError.authentication {}
        await store.close()
    }

    func testServerWithoutUIDPLUSIsRefused() async throws {
        gmail.caps = "IMAP4rev1 IDLE"
        let store = IMAPStore(config())
        do { try await store.ensureFolder(); XCTFail("accepted") } catch IMAPError.unsupported {}
        await store.close()
    }

    func testTraceLeavesOutCredentials() async throws {
        let lines = LockedLines()
        var c = config()
        c.trace = { lines.append($0) }
        let store = IMAPStore(c)
        try await store.ensureFolder()
        await store.close()
        XCTAssertTrue(lines.all.contains { $0.hasPrefix("C: T2 LOGIN") && $0.hasSuffix("<redacted>") }, "\(lines.all)")
        XCTAssertFalse(lines.all.contains { $0.hasPrefix("C:") && ($0.contains("pass word") || $0.contains("alice")) })
    }
}

final class LockedLines: @unchecked Sendable {
    private let lock = NSLock()
    private var lines: [String] = []
    func append(_ s: String) { lock.withLock { lines.append(s) } }
    var all: [String] { lock.withLock { lines } }
}
