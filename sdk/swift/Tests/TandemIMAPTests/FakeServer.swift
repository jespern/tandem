import Foundation
import Network
import Tandem

/// A plaintext loopback IMAP server for scripted tests. It frames client commands
/// (synchronizing literals get a "+", LITERAL+ ones don't), handles IDLE/DONE itself and
/// passes every other command to `respond`, which returns the response lines.
final class FakeServer: @unchecked Sendable {
    struct Command { let tag: String; let text: String; let literals: [Bytes] }

    private let listener: NWListener
    private let queue = DispatchQueue(label: "fake-imap")
    private let lock = NSLock()
    private var clients: [Client] = []
    private var _log: [String] = []
    let greeting: String
    let respond: @Sendable (Command) -> [String]
    private(set) var port: UInt16 = 0

    /// Client lines in order, IDLE and DONE included.
    var log: [String] { lock.withLock { _log } }

    init(greeting: String, respond: @escaping @Sendable (Command) -> [String]) throws {
        self.greeting = greeting
        self.respond = respond
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
    }

    func start() async throws {
        listener.newConnectionHandler = { [weak self] connection in self?.accept(connection) }
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
            let once = NSLock()
            nonisolated(unsafe) var done = false
            listener.stateUpdateHandler = { state in
                once.lock(); defer { once.unlock() }
                guard !done else { return }
                switch state {
                case .ready: done = true; continuation.resume()
                case .failed(let error): done = true; continuation.resume(throwing: error)
                default: break
                }
            }
            listener.start(queue: queue)
        }
        port = listener.port!.rawValue
    }

    func stop() {
        listener.cancel()
        lock.withLock { clients }.forEach { $0.connection.cancel() }
    }

    /// Sends unsolicited lines to every connected client.
    func push(_ lines: [String]) { lock.withLock { clients }.forEach { $0.send(lines) } }
    /// Drops every client connection, as a server restart would.
    func disconnectAll() { lock.withLock { clients }.forEach { $0.connection.cancel() } }

    fileprivate func record(_ line: String) { lock.withLock { _log.append(line) } }

    private func accept(_ connection: NWConnection) {
        let client = Client(connection: connection, server: self)
        lock.withLock { clients.append(client) }
        connection.start(queue: queue)
        client.send([greeting])
        client.read()
    }

    fileprivate final class Client: @unchecked Sendable {
        let connection: NWConnection
        unowned let server: FakeServer
        var buffer = Bytes()
        var pending: (tag: String, text: String, literals: [Bytes], need: Int)?
        var idleTag: String?

        init(connection: NWConnection, server: FakeServer) { self.connection = connection; self.server = server }

        func send(_ lines: [String]) {
            connection.send(content: Data(lines.map { $0 + "\r\n" }.joined().utf8), completion: .idempotent)
        }
        func sendRaw(_ bytes: Bytes) { connection.send(content: Data(bytes), completion: .idempotent) }

        func read() {
            connection.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [self] data, _, complete, error in
                if let data { buffer += Bytes(data); process() }
                if complete || error != nil { connection.cancel(); return }
                read()
            }
        }

        private func process() {
            while true {
                if var p = pending, p.need > 0 {
                    guard buffer.count >= p.need else { return }
                    p.literals.append(Array(buffer.prefix(p.need)))
                    buffer.removeFirst(p.need)
                    p.need = 0
                    pending = p
                }
                guard let lf = buffer.firstIndex(of: 0x0a) else { return }
                let line = String(decoding: buffer[..<lf].filter { $0 != 0x0d }, as: UTF8.self)
                buffer.removeFirst(lf + 1)
                handle(line)
            }
        }

        private func handle(_ line: String) {
            if let tag = idleTag, pending == nil {
                server.record(line)
                if line == "DONE" { idleTag = nil; send(["\(tag) OK IDLE terminated"]) }
                return
            }
            var text = line, tag = pending?.tag ?? ""
            if pending == nil {
                let parts = line.split(separator: " ", maxSplits: 1)
                tag = String(parts.first ?? ""); text = parts.count > 1 ? String(parts[1]) : ""
            }
            let joined = (pending?.text ?? "") + text
            let literals = pending?.literals ?? []
            if let match = text.firstMatch(of: /\{(\d+)(\+?)\}$/) {
                pending = (tag, joined, literals, Int(match.1)!)
                if match.2.isEmpty { send(["+ go ahead"]) }
                return
            }
            pending = nil
            server.record("\(tag) \(joined)")
            if joined.uppercased() == "IDLE" { idleTag = tag; send(["+ idling"]); return }
            if joined.uppercased() == "LOGOUT" { send(["* BYE", "\(tag) OK"]); connection.cancel(); return }
            for response in server.respond(Command(tag: tag, text: joined, literals: literals)) {
                sendRaw(Bytes((response.hasPrefix("*") || response.hasPrefix("+") ? response : "\(tag) \(response)").utf8) + [0x0d, 0x0a])
            }
        }
    }
}
