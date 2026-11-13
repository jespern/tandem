import Foundation
import XCTest

/// A disposable Stalwart in Docker, set up as conformance/stalwart.py does: same pinned
/// image, loopback ports, a generated account. Host ports are fixed at start so that a
/// `docker restart` (the reconnect test) keeps them. `stop()` removes the container.
final class Stalwart: @unchecked Sendable {
    static let image = "stalwartlabs/stalwart:v1.1.0@sha256:f772aa6ef8cd8018969fa491640f1436b0270eb14e2c219b5f37f1a77020837e"
    let name = "tandem-imap-test-" + UUID().uuidString.prefix(12).lowercased()
    let httpPort: UInt16, imapPort: UInt16
    private(set) var username = "", password = ""

    /// nil if Docker isn't installed or its daemon doesn't answer.
    static var docker: URL? {
        let paths = (ProcessInfo.processInfo.environment["PATH"] ?? "").split(separator: ":").map(String.init)
            + ["/usr/local/bin", "/opt/homebrew/bin"]
        guard let path = paths.map({ $0 + "/docker" }).first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else { return nil }
        let url = URL(fileURLWithPath: path)
        return (try? run(url, ["version", "--format", "{{.Server.Version}}"])) != nil ? url : nil
    }

    @discardableResult
    static func run(_ tool: URL, _ arguments: [String]) throws -> String {
        let p = Process(), out = Pipe()
        p.executableURL = tool; p.arguments = arguments
        p.standardOutput = out; p.standardError = out
        try p.run()
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        let text = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        guard p.terminationStatus == 0 else { throw DockerError(message: "docker \(arguments.first ?? ""): \(text)") }
        return text
    }

    private let tool: URL

    init(docker: URL) throws {
        tool = docker
        httpPort = try freePort(); imapPort = try freePort()
    }

    func start() async throws {
        do {
            try Stalwart.run(tool, ["run", "-d", "--name", name, "--init", "--label", "tandem-imap-test",
                                    "-p", "127.0.0.1:\(httpPort):8080", "-p", "127.0.0.1:\(imapPort):993",
                                    "-e", "STALWART_RECOVERY_ADMIN=admin:conformance-test-only", Stalwart.image])
            try await listening(on: 8080)
            try await bootstrap()
            let before = try listens(on: 993)
            try Stalwart.run(tool, ["restart", name])
            try await listening(on: 993, after: before)
        } catch {
            stop()
            throw error
        }
    }

    func restart() throws { try Stalwart.run(tool, ["restart", name]) }

    func stop() { _ = try? Stalwart.run(tool, ["rm", "-fv", name]) }

    /// How many times Stalwart has reported a listener on this container port.
    func listens(on port: Int) throws -> Int {
        let log = try Stalwart.run(tool, ["logs", name]).replacing(try Regex("\u{1b}\\[[0-9;]*m"), with: "")
        let pattern = try Regex("localPort[^=]*=\\s*\(port)\\b")
        return log.split(separator: "\n").filter { $0.contains("network.listen-start") && $0.contains(pattern) }.count
    }

    /// Follows the container's log until Stalwart reports a listener on the container port
    /// more often than `after` times. Counting avoids comparing host and VM clocks.
    func listening(on port: Int, after: Int = 0, timeout: Duration = .seconds(90)) async throws {
        let p = Process(), out = Pipe()
        p.executableURL = tool
        p.arguments = ["logs", "-f", name]
        p.standardOutput = out; p.standardError = out
        try p.run()
        defer { p.terminate() }
        let handle = out.fileHandleForReading
        let found = try await withThrowingTaskGroup(of: Bool.self) { group in
            group.addTask {
                let pattern = try Regex("localPort[^=]*=\\s*\(port)\\b"), ansi = try Regex("\u{1b}\\[[0-9;]*m")
                var seen = 0
                for try await line in handle.bytes.lines {
                    let plain = line.replacing(ansi, with: "")
                    if plain.contains("network.listen-start"), plain.contains(pattern) { seen += 1; if seen > after { return true } }
                }
                return false
            }
            group.addTask { try await Task.sleep(for: timeout); return false }
            let first = try await group.next() ?? false
            p.terminate()
            try? handle.close()   // ends the reader even if the log never closes
            group.cancelAll()
            return first
        }
        guard found else { throw DockerError(message: "Stalwart did not listen on \(port)") }
    }

    /// Creates the account over JMAP, as conformance/stalwart.py does.
    private func bootstrap() async throws {
        let origin = "http://127.0.0.1:\(httpPort)"
        let basic = "Basic " + Data("admin:conformance-test-only".utf8).base64EncodedString()
        var request = URLRequest(url: URL(string: origin + "/.well-known/jmap")!)
        request.setValue(basic, forHTTPHeaderField: "Authorization")
        let (sessionData, _) = try await URLSession.shared.data(for: request)
        let session = try JSONSerialization.jsonObject(with: sessionData) as! [String: Any]
        let api = URL(string: session["apiUrl"] as! String)!
        var call = URLRequest(url: URL(string: origin + api.path + (api.query.map { "?" + $0 } ?? ""))!)
        call.httpMethod = "POST"
        call.setValue(basic, forHTTPHeaderField: "Authorization")
        call.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let settings: [String: Any] = ["defaultHostname": "mail.example.org", "defaultDomain": "example.org",
                                       "requestTlsCertificate": false, "generateDkimKeys": false,
                                       "tracer": ["@type": "Stdout", "ansi": false, "buffered": false, "level": "info", "enable": true]]
        call.httpBody = try JSONSerialization.data(withJSONObject: [
            "using": ["urn:ietf:params:jmap:core", "urn:stalwart:jmap"],
            "methodCalls": [["x:Bootstrap/set", ["update": ["singleton": settings]], "0"]]])
        let (data, _) = try await URLSession.shared.data(for: call)
        let result = try JSONSerialization.jsonObject(with: data) as! [String: Any]
        let responses = result["methodResponses"] as! [[Any]]
        let updated = (responses[0][1] as! [String: Any])["updated"] as! [String: Any]
        let account = updated["singleton"] as! [String: Any]
        username = account["username"] as! String
        password = account["secret"] as! String
    }
}

struct DockerError: Error { let message: String }

/// A loopback port nothing listens on right now.
func freePort() throws -> UInt16 {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    guard fd >= 0 else { throw XCTSkip("socket failed") }
    defer { close(fd) }
    var address = sockaddr_in()
    address.sin_family = sa_family_t(AF_INET)
    address.sin_addr.s_addr = inet_addr("127.0.0.1")
    var length = socklen_t(MemoryLayout<sockaddr_in>.size)
    let ok = withUnsafeMutablePointer(to: &address) { pointer in
        pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            bind(fd, $0, length) == 0 && getsockname(fd, $0, &length) == 0
        }
    }
    guard ok else { throw XCTSkip("bind failed") }
    return UInt16(bigEndian: address.sin_port)
}
