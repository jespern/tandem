import Foundation
import Network
import Synchronization
import Tandem
import TandemIMAP
@testable import TandemJMAP
import XCTest

private final class HTTPFixture: Sendable {
    let listener: NWListener
    let peer = Mutex<NWConnection?>(nil)
    let ports: AsyncThrowingStream<UInt16, any Error>
    init(headers: String) throws {
        let parameters = NWParameters.tcp; parameters.requiredLocalEndpoint = .hostPort(host: .ipv4(.loopback), port: .any)
        listener = try NWListener(using: parameters)
        let (stream, output) = AsyncThrowingStream<UInt16, any Error>.makeStream(); ports = stream
        let listener = self.listener
        listener.stateUpdateHandler = { state in
            if case .ready = state, let port = listener.port { output.yield(port.rawValue); output.finish() }
            if case .failed(let error) = state { output.finish(throwing: error) }
        }
        listener.newConnectionHandler = { [self] connection in
            peer.withLock { $0 = connection }
            connection.stateUpdateHandler = { state in if case .ready = state { connection.send(content: Data(headers.utf8), completion: .idempotent) } }
            connection.start(queue: .global())
        }
        listener.start(queue: .global())
    }
    func send(_ bytes: String) async throws {
        guard let connection = peer.withLock({ $0 }) else { throw URLError(.notConnectedToInternet) }
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
            connection.send(content: Data(bytes.utf8), completion: .contentProcessed { error in
                if let error { continuation.resume(throwing: error) } else { continuation.resume() }
            })
        }
    }
    func close() { listener.cancel(); listener.stateUpdateHandler = nil; listener.newConnectionHandler = nil; peer.withLock { $0?.stateUpdateHandler = nil; $0?.cancel(); $0 = nil } }
}

final class EventConnectionTests: XCTestCase {
    private func connect(_ server: HTTPFixture, maxHeaders: Int = 64 * 1024) async throws -> EventConnection {
        var ports = server.ports.makeAsyncIterator(); let port = try await ports.next()!
        var config = JMAPConfig(sessionURL: "https://127.0.0.1:\(port)/session", authorization: "Bearer fixture")
        config.maxHeaderBytes = maxHeaders
        return try EventConnection(config: config, request: URLRequest(url: URL(string: "https://127.0.0.1:\(port)/events")!), transport: Transport(host: "127.0.0.1", port: port, security: .plaintextForTesting))
    }
    func testQuietHeadersAreReadyBeforeAnyBodyBytesAndChunksKeepExactBytes() async throws {
        let server = try HTTPFixture(headers: "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"); defer { server.close() }
        let connection = try await connect(server); defer { connection.cancel() }
        // The regression: await start must finish while the server has sent no
        // body or heartbeat. A timeout is only the test's failure deadline.
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask { try await connection.start() }
            group.addTask { try await Task.sleep(for: .seconds(2)); throw URLError(.timedOut) }
            defer { group.cancelAll() }; _ = try await group.next()
        }
        try await server.send("5;extension=yes\r\nabcde\r\n0\r\nX-Trace: done\r\n\r\n")
        let first = try await connection.next(), end = try await connection.next()
        XCTAssertEqual(first, Bytes("abcde".utf8)); XCTAssertNil(end)
    }
    func testConflictingFramingAndOversizedHeadersFail() async throws {
        for headers in ["HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\nContent-Length: 5\r\n\r\n", "HTTP/1.1 200 OK\r\nX-Large: " + String(repeating: "x", count: 256) + "\r\n\r\n"] {
            let server = try HTTPFixture(headers: headers); defer { server.close() }
            let connection = try await connect(server, maxHeaders: 128); defer { connection.cancel() }
            do { try await connection.start(); XCTFail("accepted invalid HTTP framing") } catch {}
        }
    }
    func testInvalidChunkSizeFails() async throws {
        let server = try HTTPFixture(headers: "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"); defer { server.close() }
        let connection = try await connect(server); defer { connection.cancel() }; try await connection.start(); try await server.send("not-hex\r\n")
        do { _ = try await connection.next(); XCTFail("accepted invalid HTTP chunk size") } catch {}
    }
}
