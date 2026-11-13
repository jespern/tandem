import Foundation
import Tandem

/// A piece of a command: text sent as is, a literal, or a line sent after the server's "+".
enum Part: Sendable, Equatable {
    case raw(Bytes)
    case literal(Bytes)
    case continuation(Bytes)

    static func text(_ s: String) -> Part { .raw(Bytes(s.utf8)) }

    /// An astring (RFC 9051 §9) in its cheapest form: atom, quoted string or literal.
    static func astring(_ b: Bytes) -> Part {
        if !b.isEmpty, b.allSatisfy(isAtomChar) { return .raw(b) }
        if b.allSatisfy({ $0 >= 0x01 && $0 <= 0x7f && $0 != 0x0d && $0 != 0x0a }) {
            var out: Bytes = [0x22]
            for c in b {
                if c == 0x22 || c == 0x5c { out.append(0x5c) }
                out.append(c)
            }
            return .raw(out + [0x22])
        }
        return .literal(b)
    }

    private static func isAtomChar(_ c: UInt8) -> Bool {
        c > 0x20 && c < 0x7f && !"(){%*\"\\]".utf8.contains(c)
    }
}

/// Untagged data that means the selected folder changed.
enum ConnectionEvent: Sendable { case changed, closed(any Error) }

/// One authenticated IMAP connection. A reader task frames and parses every response;
/// commands run one at a time (the caller serializes them). Untagged EXISTS, EXPUNGE and
/// VANISHED outside a SELECT are reported as changes, whether they arrive during IDLE or
/// alongside a command's answer.
actor Connection {
    struct Result: Sendable { let status: Status; let untagged: [Untagged] }

    private let transport: Transport
    private let events: @Sendable (ConnectionEvent) -> Void
    private let trace: (@Sendable (String) -> Void)?
    private var framer = Framer()
    private var reader: Task<Void, Never>?
    private var serial = 0
    private var tag: String?
    private var collecting = false, selecting = false
    private var collected: [Untagged] = []
    private var done: Status?, doneWaiter: CheckedContinuation<Status, any Error>?
    private var pluses = 0, plusWaiter: CheckedContinuation<Void, any Error>?
    private var failure: (any Error)?
    private var bye: String?
    var capabilities: Set<String> = []

    init(transport: Transport, trace: (@Sendable (String) -> Void)? = nil, events: @escaping @Sendable (ConnectionEvent) -> Void) {
        self.transport = transport
        self.trace = trace
        self.events = events
    }

    /// Lines for the trace hook: each cut at 200 bytes; a command's literals as {n}; credentials left out.
    private func log(_ prefix: String, _ b: Bytes) {
        guard let trace else { return }
        trace(prefix + String(decoding: b.prefix(200), as: UTF8.self).replacingOccurrences(of: "\r\n", with: "⏎"))
    }

    var isIdling: Bool { tag != nil && !collecting }
    func setCapabilities(_ caps: Set<String>) { capabilities = caps }

    /// Reads the greeting, then starts the reader.
    func start() async throws -> Status {
        while true {
            if let raw = try framer.next() {
                guard case .untagged(.status(let greeting)) = try Parser.response(raw) else {
                    throw IMAPError.protocol("bad greeting")
                }
                if greeting.kind == .bye { throw IMAPError.connection("server refused: \(greeting.text)") }
                reader = Task { await self.read() }
                return greeting
            }
            framer.append(try await transport.receive())
        }
    }

    private func read() async {
        do {
            while true {
                while let raw = try framer.next() { log("S: ", raw); try handle(try Parser.response(raw)) }
                let chunk = try await transport.receive()
                framer.append(chunk)
            }
        } catch {
            if let bye, case IMAPError.connection = error { fail(IMAPError.connection("server said BYE: \(bye)")) } else { fail(error) }
        }
    }

    private func handle(_ response: Response) throws {
        switch response {
        case .continuation:
            if let waiter = plusWaiter { plusWaiter = nil; waiter.resume() } else { pluses += 1 }
        case .tagged(let t, let status):
            guard t == tag else { throw IMAPError.protocol("unexpected tag \(t)") }
            tag = nil
            if let waiter = plusWaiter { plusWaiter = nil; waiter.resume(throwing: Connection.rejection(status)) }
            if let waiter = doneWaiter { doneWaiter = nil; waiter.resume(returning: status) } else { done = status }
        case .untagged(let u):
            switch u {
            case .status(let s) where s.kind == .bye: bye = s.text
            case .exists where !selecting, .expunge, .vanished(earlier: false, _): events(.changed)
            default: break
            }
            if tag != nil, collecting { collected.append(u) }
        }
    }

    private func fail(_ error: any Error) {
        guard failure == nil else { return }
        failure = error
        plusWaiter?.resume(throwing: error); plusWaiter = nil
        doneWaiter?.resume(throwing: error); doneWaiter = nil
        transport.cancel()
        events(.closed(error))
    }

    /// Cancels the connection; pending commands fail.
    func close() {
        fail(IMAPError.closed)
        reader?.cancel()
    }

    static func rejection(_ status: Status) -> IMAPError {
        .rejected(code: status.code?.name ?? "", text: status.text)
    }

    private func waitPlus() async throws {
        if let failure { throw failure }
        if pluses > 0 { pluses -= 1; return }
        if tag == nil, let done { throw Connection.rejection(done) }
        try await withCheckedThrowingContinuation { plusWaiter = $0 }
    }
    private func waitDone() async throws -> Status {
        if let done { self.done = nil; return done }
        if let failure { throw failure }
        return try await withCheckedThrowingContinuation { doneWaiter = $0 }
    }

    private func begin(collecting: Bool, selecting: Bool) throws -> String {
        if let failure { throw failure }
        guard tag == nil else { throw IMAPError.protocol("command already in progress") }
        serial += 1
        let t = "T\(serial)"
        tag = t; done = nil; pluses = 0; collected = []
        self.collecting = collecting; self.selecting = selecting
        return t
    }

    private func nonSynchronizing(_ count: Int) -> Bool {
        capabilities.contains("LITERAL+") || (count <= 4096 && (capabilities.contains("LITERAL-") || capabilities.contains("IMAP4REV2")))
    }

    /// Runs one command. NO and BAD throw IMAPError.rejected.
    @discardableResult
    func execute(_ parts: [Part], selecting: Bool = false, secret: Bool = false) async throws -> Result {
        let t = try begin(collecting: true, selecting: selecting)
        if trace != nil {
            let shown = secret ? [parts[0]] : parts
            var line = Bytes()
            for part in shown {
                switch part {
                case .raw(let b): line += b
                case .literal(let b): line += Bytes("{\(b.count)}".utf8)
                case .continuation: break
                }
            }
            log("C: \(t) ", line + (secret ? Bytes(" <redacted>".utf8) : []))
        }
        defer { self.selecting = false }
        var out = Bytes((t + " ").utf8)
        for part in parts {
            switch part {
            case .raw(let b): out += b
            case .literal(let data):
                if nonSynchronizing(data.count) {
                    out += Bytes("{\(data.count)+}\r\n".utf8) + data
                } else {
                    out += Bytes("{\(data.count)}\r\n".utf8)
                    try await transport.send(out)
                    try await waitPlus()
                    out = data
                }
            case .continuation(let line):
                try await transport.send(out + [0x0d, 0x0a])
                try await waitPlus()
                out = line
            }
        }
        try await transport.send(out + [0x0d, 0x0a])
        let status = try await waitDone()
        let untagged = collected
        collected = []
        guard status.kind == .ok else { throw Connection.rejection(status) }
        return Result(status: status, untagged: untagged)
    }

    /// Starts IDLE (RFC 2177) and returns once the server has accepted it.
    func idle() async throws {
        let t = try begin(collecting: false, selecting: false)
        log("C: ", Bytes("\(t) IDLE".utf8))
        try await transport.send(Bytes("\(t) IDLE\r\n".utf8))
        try await waitPlus()
    }

    /// Ends IDLE and waits for its completion.
    func done() async throws {
        guard isIdling else { return }
        log("C: ", Bytes("DONE".utf8))
        try await transport.send(Bytes("DONE\r\n".utf8))
        let status = try await waitDone()
        guard status.kind == .ok else { throw Connection.rejection(status) }
    }
}
