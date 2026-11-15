import Foundation
import Tandem
import TandemIMAP

/// Pull-based SSE over the existing native TLS transport. Foundation delays
/// quiet-stream response headers until body bytes arrive on current macOS; that
/// cannot implement watch's subscribe-before-scan contract. HTTP/1.1 framing is
/// limited to this GET stream (RFC 9112 §6–7); JSON requests use Foundation.
actor EventConnection {
    private let transport: Transport
    private let request: Bytes
    private let config: JMAPConfig
    private var buffer: Bytes = [], offset = 0
    private var chunked = false, chunkRemaining = 0, separator = false, ended = false
    private var contentRemaining: Int?
    init(config: JMAPConfig, request: URLRequest, transport supplied: Transport? = nil) throws {
        guard let url = request.url, let host = url.host, let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let port = UInt16(exactly: url.port ?? 443) else { throw TandemError.invalid("JMAP: invalid event endpoint") }
        self.config = config
        let testTLS = config.allowsSelfSignedLoopback && ["127.0.0.1", "::1", "[::1]"].contains(host)
        transport = supplied ?? Transport(host: host, port: port, security: testTLS ? .tlsTrustingAnyCertificateForTesting : .tls)
        let authority = (host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host) + (port == 443 ? "" : ":\(port)")
        let path = (parts.percentEncodedPath.isEmpty ? "/" : parts.percentEncodedPath) + (parts.percentEncodedQuery.map { "?" + $0 } ?? "")
        // No compressed stream buffering, cookies, redirects or credentials in URLs.
        let headers = ["GET \(path) HTTP/1.1", "Host: \(authority)", "Authorization: \(config.authorization)",
                       "Accept: text/event-stream", "Accept-Encoding: identity", "Cache-Control: no-cache", "Connection: keep-alive", "", ""]
        self.request = Bytes(headers.joined(separator: "\r\n").utf8)
    }
    nonisolated func cancel() { transport.cancel() }
    func start() async throws {
        try await withTaskCancellationHandler {
            try await transport.open(); try await transport.send(request)
            var total = 0
            while true {
                let status = try await line(limit: config.maxHeaderBytes - total); total += status.count + 2
                let fields = String(decoding: status, as: UTF8.self).split(separator: " ", maxSplits: 2)
                guard fields.count >= 2, ["HTTP/1.1", "HTTP/1.0"].contains(fields[0]), let code = Int(fields[1]), (100...599).contains(code) else { throw TandemError.invalid("JMAP: invalid SSE HTTP status") }
                var headers: [String: [String]] = [:]
                while true {
                    let raw = try await line(limit: config.maxHeaderBytes - total); total += raw.count + 2
                    if raw.isEmpty { break }
                    guard let colon = raw.firstIndex(of: 58), colon > 0, raw[..<colon].allSatisfy({ $0 > 32 && $0 < 127 }), !raw[..<colon].contains(32) else { throw TandemError.invalid("JMAP: invalid SSE HTTP header") }
                    let name = String(decoding: raw[..<colon], as: UTF8.self).lowercased(), value = String(decoding: raw[(colon + 1)...], as: UTF8.self).trimmingCharacters(in: .whitespaces)
                    headers[name, default: []].append(value)
                }
                if (100..<200).contains(code), code != 101 { continue }
                guard (200..<300).contains(code) else { throw JMAPError(type: "HTTP \(code)") }
                guard headers["content-type"]?.count == 1, headers["content-type"]![0].lowercased().hasPrefix("text/event-stream"),
                      headers["content-encoding"] == nil || headers["content-encoding"]?.map { $0.lowercased() } == ["identity"] else { throw JMAPError(type: "not an uncompressed event stream") }
                if let transfer = headers["transfer-encoding"] {
                    guard transfer.count == 1, transfer[0].lowercased() == "chunked", headers["content-length"] == nil else { throw TandemError.invalid("JMAP: invalid stream transfer framing") }; chunked = true
                } else if let length = headers["content-length"] {
                    guard length.count == 1, !length[0].isEmpty, length[0].utf8.allSatisfy({ (48...57).contains($0) }), let count = Int(length[0]) else { throw TandemError.invalid("JMAP: invalid stream length") }; contentRemaining = count
                }
                return
            }
        } onCancel: { transport.cancel() }
    }
    private func line(limit: Int) async throws -> Bytes {
        guard limit >= 2 else { throw TandemError.invalid("JMAP: HTTP header/line limit exceeded") }
        var scan = offset
        while true {
            while scan + 1 < buffer.count {
                if buffer[scan] == 13 && buffer[scan + 1] == 10 {
                    guard scan - offset + 2 <= limit else { throw TandemError.invalid("JMAP: HTTP line limit exceeded") }
                    let result = Array(buffer[offset..<scan]); offset = scan + 2; return result
                }; scan += 1
            }
            guard buffer.count - offset < limit else { throw TandemError.invalid("JMAP: HTTP line limit exceeded") }
            let consumed = offset; if consumed > 0 { buffer.removeFirst(consumed); offset = 0; scan = max(0, scan - consumed) }
            buffer += try await transport.receive()
        }
    }
    private func available(_ maximum: Int) async throws -> Bytes {
        while offset == buffer.count { buffer = try await transport.receive(); offset = 0 }
        let end = offset + min(maximum, buffer.count - offset), result = Array(buffer[offset..<end]); offset = end; return result
    }
    private func exact(_ count: Int) async throws -> Bytes {
        var result: Bytes = []; while result.count < count { result += try await available(count - result.count) }; return result
    }
    func next() async throws -> Bytes? {
        try Task.checkCancellation(); if ended { return nil }
        if chunked {
            if chunkRemaining == 0 {
                if separator { guard try await exact(2) == [13, 10] else { throw TandemError.invalid("JMAP: malformed HTTP chunk separator") }; separator = false }
                let raw = try await line(limit: config.maxHeaderBytes), size = raw.split(separator: 59, maxSplits: 1, omittingEmptySubsequences: false)[0]
                guard !size.isEmpty, size.allSatisfy({ (48...57).contains($0) || (65...70).contains($0) || (97...102).contains($0) }), let count = Int(String(decoding: size, as: UTF8.self), radix: 16) else { throw TandemError.invalid("JMAP: invalid HTTP chunk size") }
                if count == 0 {
                    var total = 0
                    while true { let trailer = try await line(limit: config.maxHeaderBytes - total); total += trailer.count + 2; if trailer.isEmpty { break } }
                    ended = true; return nil
                }; chunkRemaining = count
            }
            let bytes = try await available(min(chunkRemaining, 64 * 1024)); chunkRemaining -= bytes.count; if chunkRemaining == 0 { separator = true }; return bytes
        }
        if let remaining = contentRemaining { if remaining == 0 { ended = true; return nil }; let bytes = try await available(min(remaining, 64 * 1024)); contentRemaining = remaining - bytes.count; return bytes }
        return try await available(64 * 1024)
    }
}
