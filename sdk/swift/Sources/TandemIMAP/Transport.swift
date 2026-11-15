import Foundation
import Network
import Synchronization
import Tandem

/// How the client reaches the server. Only `.tls` belongs in production.
public enum Security: Sendable, Equatable {
    /// TLS, verified against the system's trust store and the host name.
    case tls
    /// TLS that accepts any certificate. For a disposable test server only.
    case tlsTrustingAnyCertificateForTesting
    /// No TLS. For a loopback test server only.
    case plaintextForTesting
}

/// One TCP (TLS) connection. Pending sends and receives fail once it is cancelled.
package final class Transport: Sendable {
    private let connection: NWConnection
    private let queue = DispatchQueue(label: "tandem.imap.transport")

    package init(host: String, port: UInt16, security: Security) {
        let tcp = NWProtocolTCP.Options()
        tcp.enableKeepalive = true          // finds a dead peer while idling, without a timer of ours
        tcp.keepaliveIdle = 60
        tcp.keepaliveInterval = 15
        tcp.keepaliveCount = 4
        tcp.connectionTimeout = 30
        let parameters: NWParameters
        switch security {
        case .plaintextForTesting:
            parameters = NWParameters(tls: nil, tcp: tcp)
        case .tls, .tlsTrustingAnyCertificateForTesting:
            let tls = NWProtocolTLS.Options()
            if security == .tlsTrustingAnyCertificateForTesting {
                sec_protocol_options_set_verify_block(tls.securityProtocolOptions, { _, _, complete in complete(true) },
                                                      DispatchQueue(label: "tandem.imap.verify"))
            }
            parameters = NWParameters(tls: tls, tcp: tcp)
        }
        connection = NWConnection(host: NWEndpoint.Host(host), port: NWEndpoint.Port(rawValue: port) ?? .imaps, using: parameters)
    }

    /// Returns once the connection is ready. Waiting for a usable path counts as failure.
    package func open() async throws {
        let once = Mutex(false)
        let connection = self.connection
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
                @Sendable func finish(_ error: (any Error)?) {
                    guard once.withLock({ let first = !$0; $0 = true; return first }) else { return }
                    if let error { continuation.resume(throwing: error) } else { continuation.resume() }
                }
                connection.stateUpdateHandler = { state in
                    switch state {
                    case .ready: finish(nil)
                    case .waiting(let error), .failed(let error):
                        finish(IMAPError.connection("\(error)"))
                        connection.cancel()
                    case .cancelled: finish(IMAPError.closed)
                    default: break
                    }
                }
                connection.start(queue: queue)
            }
        } onCancel: { connection.cancel() }
    }

    package func send(_ bytes: Bytes) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
            connection.send(content: Data(bytes), completion: .contentProcessed { error in
                if let error { continuation.resume(throwing: IMAPError.connection("\(error)")) } else { continuation.resume() }
            })
        }
    }

    /// The next bytes from the server. Throws when the connection ends.
    package func receive() async throws -> Bytes {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Bytes, any Error>) in
            connection.receive(minimumIncompleteLength: 1, maximumLength: 1 << 16) { data, _, complete, error in
                if let data, !data.isEmpty { continuation.resume(returning: Bytes(data)) }
                else if let error { continuation.resume(throwing: IMAPError.connection("\(error)")) }
                else if complete { continuation.resume(throwing: IMAPError.connection("server closed the connection")) }
                else { continuation.resume(returning: []) }
            }
        }
    }

    package func cancel() { connection.cancel() }
}
