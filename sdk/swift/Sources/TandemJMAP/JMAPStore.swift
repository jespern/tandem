import Foundation
import Security
import Tandem
import TandemIMAP

private let core = "urn:ietf:params:jmap:core", mail = "urn:ietf:params:jmap:mail"
private typealias Object = [String: Any]
func decodeJMAPObject(_ data: Data) throws -> [String: Any] { try object(JSONSerialization.jsonObject(with: data)) }
private func object(_ value: Any?) throws -> Object { guard let result = value as? Object else { throw TandemError.invalid("JMAP: object required") }; return result }
private func string(_ value: Any?) throws -> String { guard let result = value as? String, !result.isEmpty else { throw TandemError.invalid("JMAP: string required") }; return result }
private func strings(_ value: Any?) throws -> [String] { guard let result = value as? [String], result.allSatisfy({ !$0.isEmpty }) else { throw TandemError.invalid("JMAP: IDs required") }; return result }
private func origin(_ url: URL) -> String { "\(url.scheme ?? "")://\(url.host ?? ""):\(url.port ?? 443)" }
private func trusted(_ url: URL, config: JMAPConfig) -> Bool {
    let origins = ([config.sessionURL] + config.trustedOrigins).compactMap(URL.init(string:)).map(origin)
    let fastmail = config.fastmail && (url.port == nil || url.port == 443) && (url.host == "api.fastmail.com" || url.host?.hasSuffix(".api.fastmail.com") == true)
    return url.scheme == "https" && url.host != nil && url.user == nil && url.password == nil && url.fragment == nil && (origins.contains(origin(url)) || fastmail)
}

public struct JMAPConfig: Sendable {
    public var sessionURL: String, authorization: String
    public var accountID: String?, folderName = "Tandem", trustedOrigins: [String] = []
    public var fastmail = false
    public var pageSize = 128, maxResponseBytes = 16 * 1024 * 1024, maxKnownMessages = 1_000_000
    public var maxEventBytes = 128 * 1024, maxBatchBytes = 64 * 1024 * 1024, maxHeaderBytes = 64 * 1024
    public var commandTimeout: TimeInterval = 30, retryMin: Duration = .seconds(1), retryMax: Duration = .seconds(30)
    /// Test fixtures only: accepts self-signed TLS solely on literal loopback hosts.
    public var allowsSelfSignedLoopback = false
    public init(sessionURL: String, authorization: String) { self.sessionURL = sessionURL; self.authorization = authorization }
    public static func fastmail(token: String) -> Self {
        var config = Self(sessionURL: "https://api.fastmail.com/jmap/session", authorization: "Bearer " + token)
        config.fastmail = true; return config
    }
}
private final class TransportDelegate: NSObject, URLSessionTaskDelegate, Sendable {
    let config: JMAPConfig
    init(_ config: JMAPConfig) { self.config = config }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        guard task.originalRequest?.url?.absoluteString == config.sessionURL, task.countOfBytesSent == 0,
              let url = request.url, trusted(url, config: config) else { completionHandler(nil); return }
        var next = request; next.setValue(config.authorization, forHTTPHeaderField: "Authorization"); completionHandler(next)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        if config.allowsSelfSignedLoopback, ["127.0.0.1", "::1"].contains(challenge.protectionSpace.host),
           challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust, let trust = challenge.protectionSpace.serverTrust {
            completionHandler(.useCredential, URLCredential(trust: trust))
        } else { completionHandler(.performDefaultHandling, nil) }
    }
}
public struct JMAPError: Error, Sendable { public let type: String }

/// Explicit HTTPS JMAP transport with one persistent SSE stream. IMAP defaults
/// are unchanged. Call watch before the initial scan, and reconcile on its wakes.
public actor JMAPStore: PushStore {
    private var config: JMAPConfig
    private let session: URLSession
    private var api = "", upload = "", events = "", account = "", folder = "", deleteSize = 128
    private var watching = false, closed = false
    private var connection: EventConnection?
    private var worker: Task<Void, Never>?, output: AsyncThrowingStream<Void, any Error>.Continuation?
    private init(_ config: JMAPConfig) {
        self.config = config
        let settings = URLSessionConfiguration.ephemeral
        settings.urlCache = nil; settings.httpCookieStorage = nil; settings.urlCredentialStorage = nil
        settings.timeoutIntervalForResource = 365 * 24 * 60 * 60
        settings.timeoutIntervalForRequest = 365 * 24 * 60 * 60
        session = URLSession(configuration: settings, delegate: TransportDelegate(config), delegateQueue: nil)
    }
    public static func open(_ config: JMAPConfig) async throws -> JMAPStore {
        guard [config.pageSize, config.maxResponseBytes, config.maxKnownMessages, config.maxEventBytes, config.maxBatchBytes, config.maxHeaderBytes].allSatisfy({ $0 > 0 }),
              config.commandTimeout.isFinite, config.commandTimeout > 0, config.retryMin > .zero, config.retryMax >= config.retryMin,
              !config.folderName.isEmpty, !config.folderName.contains(where: { $0 == "\r" || $0 == "\n" || $0 == "\0" }),
              !config.authorization.contains(where: { $0 == "\r" || $0 == "\n" }),
              config.authorization.hasPrefix("Bearer ") || config.authorization.hasPrefix("Basic ") else { throw TandemError.invalid("JMAP: invalid configuration") }
        if config.fastmail {
            guard config.sessionURL == "https://api.fastmail.com/jmap/session", config.authorization.hasPrefix("Bearer "),
                  !config.authorization.dropFirst(7).isEmpty, !config.authorization.dropFirst(7).contains(where: \.isWhitespace)
            else { throw TandemError.invalid("JMAP: invalid Fastmail configuration") }
        }
        let store = JMAPStore(config)
        do { try await store.connect(); return store } catch { await store.close(); throw error }
    }
    private func request(_ raw: String, method: String = "GET", type: String? = nil, data: Data? = nil, stream: Bool = false) throws -> URLRequest {
        guard !closed, let url = URL(string: raw), trusted(url, config: config) else { throw TandemError.invalid("JMAP: untrusted URL or closed store") }
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData); request.httpMethod = method; request.httpBody = data
        request.setValue(config.authorization, forHTTPHeaderField: "Authorization")
        if let type { request.setValue(type, forHTTPHeaderField: "Content-Type") }
        request.timeoutInterval = stream ? 365 * 24 * 60 * 60 : config.commandTimeout
        if stream { request.setValue("text/event-stream", forHTTPHeaderField: "Accept"); request.setValue("no-cache", forHTTPHeaderField: "Cache-Control") }; return request
    }
    private func check(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else { throw JMAPError(type: "HTTP \((response as? HTTPURLResponse)?.statusCode ?? 0)") }
    }
    private func json(_ raw: String, method: String = "GET", type: String? = nil, data: Data? = nil) async throws -> Object {
        let (bytes, response) = try await session.bytes(for: request(raw, method: method, type: type, data: data))
        defer { bytes.task.cancel() }; try check(response)
        var data = Data()
        for try await byte in bytes { guard data.count < config.maxResponseBytes else { throw TandemError.invalid("JMAP: response limit exceeded") }; data.append(byte) }
        return try decodeJMAPObject(data)
    }
    private func call(_ method: String, _ arguments: Object) async throws -> Object {
        var args = arguments; args["accountId"] = account
        let data = try JSONSerialization.data(withJSONObject: ["using": [core, mail], "methodCalls": [[method, args, "0"]]])
        let response = try await json(api, method: "POST", type: "application/json", data: data)
        guard let rows = response["methodResponses"] as? [[Any]], rows.count == 1, rows[0].count == 3, rows[0][2] as? String == "0" else { throw TandemError.invalid("JMAP: response mismatch") }
        let result = try object(rows[0][1]), name = try string(rows[0][0])
        if name == "error" { throw JMAPError(type: try string(result["type"])) }
        guard name == method else { throw TandemError.invalid("JMAP: response method mismatch") }
        for key in ["notCreated", "notUpdated", "notDestroyed"] { if let value = result[key], !(try object(value)).isEmpty { throw JMAPError(type: method + " refused") } }
        return result
    }
    private func connect() async throws {
        let info = try await json(config.sessionURL), capabilities = try object(info["capabilities"])
        guard capabilities[mail] != nil else { throw JMAPError(type: "mail capability missing") }
        account = try config.accountID ?? string(object(info["primaryAccounts"])[mail])
        guard try object(object(object(info["accounts"])[account])["accountCapabilities"])[mail] != nil else { throw JMAPError(type: "mail account missing") }
        api = try string(info["apiUrl"]); upload = try string(info["uploadUrl"]); events = try string(info["eventSourceUrl"])
        _ = try request(api)
        let capability = try object(capabilities[core])
        if let limit = capability["maxObjectsInGet"] as? Int, limit > 0 { config.pageSize = min(config.pageSize, limit) }
        deleteSize = config.pageSize
        if let limit = capability["maxObjectsInSet"] as? Int, limit > 0 { deleteSize = min(deleteSize, limit) }
        let ids = try strings((await call("Mailbox/query", ["filter": ["name": config.folderName]]))["ids"])
        if !ids.isEmpty {
            guard let boxes = try await call("Mailbox/get", ["ids": ids])["list"] as? [Object] else { throw TandemError.invalid("JMAP: invalid mailboxes") }
            for box in boxes where box["name"] as? String == config.folderName && box["parentId"] is NSNull {
                guard folder.isEmpty else { throw TandemError.invalid("JMAP: ambiguous root mailbox") }; folder = try string(box["id"])
            }
        }
        if folder.isEmpty { folder = try string(object(object((await call("Mailbox/set", ["create": ["tandem": ["name": config.folderName, "parentId": NSNull()]]]))["created"])["tandem"])["id"]) }
    }
    private func subscribe() async throws -> EventConnection {
        var raw = events
        for (key, value) in [("types", "Email"), ("closeafter", "no"), ("ping", "60")] { raw = raw.replacingOccurrences(of: "{\(key)}", with: value) }
        guard !raw.contains("{"), !raw.contains("}") else { throw TandemError.invalid("JMAP: unsupported event URL template") }
        let stream = try EventConnection(config: config, request: try request(raw, stream: true)), timeout = config.commandTimeout
        do {
            try await withThrowingTaskGroup(of: Void.self) { group in
                group.addTask { try await stream.start() }
                group.addTask { try await Task.sleep(for: .seconds(timeout)); throw URLError(.timedOut) } // allowed-timer: initial SSE headers safety deadline only
                defer { group.cancelAll() }; _ = try await group.next()
            }
            connection = stream; return stream
        } catch { stream.cancel(); throw error }
    }
    public func watch() async throws -> AsyncThrowingStream<Void, any Error> {
        guard !watching, !closed else { throw TandemError.invalid("JMAP: watch already started or closed") }; watching = true
        let bytes = try await subscribe(), (stream, continuation) = AsyncThrowingStream<Void, any Error>.makeStream(bufferingPolicy: .bufferingNewest(1))
        output = continuation
        continuation.onTermination = { @Sendable _ in Task { await self.cancelWatch() } }
        worker = Task { await self.pump(bytes, continuation) }; return stream
    }
    private func cancelWatch() { worker?.cancel(); connection?.cancel() }
    private func isConnectionFailure(_ error: any Error) -> Bool {
        if error is URLError { return true }
        if case IMAPError.connection = error { return true }
        return false
    }
    private func pump(_ initial: EventConnection, _ continuation: AsyncThrowingStream<Void, any Error>.Continuation) async {
        var bytes = initial, delay = config.retryMin
        while !Task.isCancelled && !closed {
            do { try await readEvents(bytes, continuation); throw URLError(.networkConnectionLost) }
            catch { bytes.cancel(); if Task.isCancelled || closed { break }; guard isConnectionFailure(error) else { continuation.finish(throwing: error); return } }
            while !Task.isCancelled && !closed {
                do { try await Task.sleep(for: delay); bytes = try await subscribe(); delay = config.retryMin; continuation.yield(()); break } // allowed-timer: failed-connection backoff only
                catch { if Task.isCancelled || closed { break }; guard isConnectionFailure(error) else { continuation.finish(throwing: error); return }; delay = min(delay * 2, config.retryMax) }
            }
        }
        continuation.finish()
    }
    private func readEvents(_ bytes: EventConnection, _ continuation: AsyncThrowingStream<Void, any Error>.Continuation) async throws {
        var line: Bytes = [], event = "", data: [String] = [], size = 0, cr = false, first = true
        func consume() throws {
            guard var text = String(bytes: line, encoding: .utf8) else { throw TandemError.invalid("JMAP: invalid push UTF-8") }
            if first { first = false; if text.hasPrefix("\u{feff}") { text.removeFirst() } }
            size += line.count + 1; line.removeAll(keepingCapacity: true)
            guard size <= config.maxEventBytes else { throw TandemError.invalid("JMAP: event limit exceeded") }
            if text.isEmpty {
                if event == "state", !data.isEmpty {
                    let raw = Data(data.joined(separator: "\n").utf8)
                    let value = try decodeJMAPObject(raw)
                    if try object(object(value["changed"] ?? Object())[account] ?? Object())["Email"] != nil { continuation.yield(()) }
                }; event = ""; data = []; size = 0
            } else if text.hasPrefix("event:") { event = String(text.dropFirst(6)); if event.hasPrefix(" ") { event.removeFirst() } }
            else if text.hasPrefix("data:") { var value = String(text.dropFirst(5)); if value.hasPrefix(" ") { value.removeFirst() }; data.append(value) }
        }
        while let chunk = try await bytes.next() {
            for byte in chunk {
            try Task.checkCancellation()
            if cr { cr = false; if byte == 10 { continue } }
            if byte == 10 || byte == 13 { try consume(); cr = byte == 13 }
            else { guard line.count < config.maxEventBytes else { throw TandemError.invalid("JMAP: push line limit exceeded") }; line.append(byte) }
            }
        }
    }
    private func objects(_ ids: [String], bodies: Bool) async throws -> (rows: [Object], missing: [String]) {
        if ids.isEmpty { return ([], []) }
        let properties = ["id", "mailboxIds"] + (bodies ? ["bodyValues", "textBody", "header:Tandem-Channel:asText:all", "header:Tandem-Version:asText:all"] : [])
        let result = try await call("Email/get", ["ids": ids, "properties": properties, "fetchTextBodyValues": bodies, "maxBodyValueBytes": config.maxResponseBytes])
        guard let list = result["list"] as? [Object] else { throw TandemError.invalid("JMAP: invalid Email/get") }; return (list, try strings(result["notFound"] ?? [String]()))
    }
    private func message(_ row: Object) throws -> Message? {
        guard try object(row["mailboxIds"])[folder] as? Bool == true, let versions = row["header:Tandem-Version:asText:all"] as? [String], versions.count == 1, versions[0].trimmingCharacters(in: .whitespacesAndNewlines) == "2",
              let channels = row["header:Tandem-Channel:asText:all"] as? [String], channels.count == 1 else { return nil }
        let tag = channels[0].trimmingCharacters(in: .whitespacesAndNewlines)
        guard B32.decode(tag)?.count == 20 else { return nil }
        let values = try object(row["bodyValues"])
        for value in values.values { if try object(value)["isTruncated"] as? Bool == true { throw TandemError.invalid("JMAP: body truncated; cursor unchanged") } }
        guard values.count == 1, let value = values.values.first, try object(value)["isEncodingProblem"] as? Bool != true, let text = try object(value)["value"] as? String else { return nil }
        let compact = text.filter { !"\t\n\u{0b}\u{0c}\r ".contains($0) }
        guard let data = Data(base64Encoded: compact), data.base64EncodedString() == compact else { return nil }
        let body = Bytes(data)
        guard body.count >= 54, Array(body.prefix(5)) == [84,78,68,77,2], body[5] == 1 || body[5] == 3 || body[5] == 2 && body.count >= 86 else { return nil }
        return Message(id: try string(row["id"]), channel: tag, body: body)
    }
    private struct Cursor: Codable { var v = 1; var account: String; var folder: String; var state: String; var known: [String] }
    private struct FullSnapshot { let state: String; let known: Set<String>; let added: [Message] }
    private func fullScan() async throws -> FullSnapshot {
        let state = try string((await call("Email/get", ["ids": [String](), "properties": ["id"]]))["state"])
        var ids: [String] = [], seen = Set<String>(), queryState = ""
        while true {
            let result = try await call("Email/query", ["filter": ["inMailbox": folder], "sort": [["property": "receivedAt", "isAscending": true]], "position": ids.count, "limit": config.pageSize, "calculateTotal": true])
            let current = try string(result["queryState"]), page = try strings(result["ids"])
            guard queryState.isEmpty || current == queryState else { throw JMAPError(type: "query changed during full scan; resume after push") }; queryState = current
            for id in page { guard seen.insert(id).inserted else { throw TandemError.invalid("JMAP: repeated query ID") }; ids.append(id) }
            guard ids.count <= config.maxKnownMessages, let total = result["total"] as? Int, total >= ids.count else { throw TandemError.invalid("JMAP: invalid query total or message limit exceeded") }
            if total == ids.count { break }; guard !page.isEmpty else { throw TandemError.invalid("JMAP: query made no progress") }
        }
        var known = seen, added: [Message] = [], count = 0
        for at in stride(from: 0, to: ids.count, by: config.pageSize) {
            let (rows, missing) = try await objects(Array(ids[at..<min(at + config.pageSize, ids.count)]), bodies: true)
            known.subtract(missing)
            for row in rows {
                let id = try string(row["id"])
                guard try object(row["mailboxIds"])[folder] as? Bool == true else { known.remove(id); continue }
                if let value = try message(row) { count += value.body.count; guard count <= config.maxBatchBytes else { throw TandemError.invalid("JMAP: batch byte limit exceeded") }; added.append(value) }
            }
        }
        return FullSnapshot(state: state, known: known, added: added)
    }
    public func changes(since encoded: String) async throws -> ChangeBatch {
        var cursor = Cursor(account: account, folder: folder, state: "", known: []), known = Set<String>(), added: [String: Message] = [:], removed = Set<String>(), batchBytes = 0
        if !encoded.isEmpty {
            let data = Data(encoded.utf8); _ = try JSON.parse(Bytes(data)); cursor = try JSONDecoder().decode(Cursor.self, from: data)
            guard cursor.v == 1, cursor.account == account, cursor.folder == folder, !cursor.state.isEmpty, cursor.known.count <= config.maxKnownMessages,
                  Set(cursor.known).count == cursor.known.count, cursor.known.allSatisfy({ !$0.isEmpty }) else { throw TandemError.invalid("JMAP: cursor belongs to another store or exceeds limits") }; known = Set(cursor.known)
        }
        func collect(_ row: Object) throws {
            let id = try string(row["id"])
            guard try object(row["mailboxIds"])[folder] as? Bool == true else { known.remove(id); added.removeValue(forKey: id); removed.insert(id); return }
            known.insert(id)
            if let value = try message(row) { batchBytes += value.body.count - (added[id]?.body.count ?? 0); guard batchBytes <= config.maxBatchBytes else { throw TandemError.invalid("JMAP: batch byte limit exceeded") }; added[id] = value }
        }
        if encoded.isEmpty { let full = try await fullScan(); cursor.state = full.state; removed.formUnion(known.subtracting(full.known)); known = full.known; added = Dictionary(uniqueKeysWithValues: full.added.map { ($0.id, $0) }); batchBytes = full.added.reduce(0) { $0 + $1.body.count } }
        var reset = false
        while true {
            let result: Object
            do { result = try await call("Email/changes", ["sinceState": cursor.state, "maxChanges": config.pageSize]) }
            catch let error as JMAPError where error.type == "cannotCalculateChanges" && !reset { reset = true; let full = try await fullScan(); cursor.state = full.state; removed.formUnion(known.subtracting(full.known)); known = full.known; added = Dictionary(uniqueKeysWithValues: full.added.map { ($0.id, $0) }); batchBytes = full.added.reduce(0) { $0 + $1.body.count }; continue }
            let next = try string(result["newState"]), more = result["hasMoreChanges"] as? Bool == true
            guard !more || next != cursor.state else { throw TandemError.invalid("JMAP: changes made no progress") }
            let destroyed = Set(try strings(result["destroyed"])), changed = Set(try strings(result["created"]) + strings(result["updated"])).sorted()
            for id in destroyed { if known.remove(id) != nil { removed.insert(id) }; added.removeValue(forKey: id) }
            for at in stride(from: 0, to: changed.count, by: config.pageSize) {
                let (rows, missing) = try await objects(Array(changed[at..<min(at + config.pageSize, changed.count)]), bodies: false)
                for id in missing { known.remove(id); added.removeValue(forKey: id); removed.insert(id) }
                var bodies: [String] = []
                for row in rows {
                    let id = try string(row["id"]); if destroyed.contains(id) { continue }
                    if try object(row["mailboxIds"])[folder] as? Bool == true { known.insert(id); bodies.append(id) }
                    else if known.remove(id) != nil { added.removeValue(forKey: id); removed.insert(id) }
                }
                let (fetched, missingBodies) = try await objects(bodies, bodies: true)
                for id in missingBodies { known.remove(id); added.removeValue(forKey: id); removed.insert(id) }; for row in fetched { try collect(row) }
            }
            guard known.count <= config.maxKnownMessages else { throw TandemError.invalid("JMAP: message limit exceeded") }
            cursor.state = next; if !more { break }
        }
        cursor.known = known.sorted(); let encoder = JSONEncoder(); encoder.outputFormatting = .sortedKeys; let data = try encoder.encode(cursor)
        return ChangeBatch(added: added.values.sorted { $0.id < $1.id }, removed: removed.subtracting(known).sorted(), cursor: String(decoding: data, as: UTF8.self))
    }
    public func append(channel: String, body: Bytes) async throws -> String {
        guard B32.decode(channel)?.count == 20, body.count >= 54, Array(body.prefix(5)) == [84,78,68,77,2],
              body[5] == 1 || body[5] == 3 || body[5] == 2 && body.count >= 86 else { throw TandemError.invalid("JMAP: invalid envelope or channel") }
        return try await appendPrepared(MessageFormat.build(channel: channel, envelope: body))
    }
    public func appendPrepared(_ wire: Bytes) async throws -> String {
        guard wire.count <= config.maxResponseBytes else { throw TandemError.invalid("JMAP: message limit exceeded") }
        var allowed = CharacterSet.urlPathAllowed; allowed.remove(charactersIn: "/?#%")
        let raw = upload.replacingOccurrences(of: "{accountId}", with: account.addingPercentEncoding(withAllowedCharacters: allowed)!)
        guard !raw.contains("{"), !raw.contains("}") else { throw TandemError.invalid("JMAP: unsupported upload template") }
        let blob = try string((await json(raw, method: "POST", type: "message/rfc822", data: Data(wire)))["blobId"])
        return try string(object(object((await call("Email/import", ["emails": ["tandem": ["blobId": blob, "mailboxIds": [folder: true], "keywords": ["$seen": true]]]]))["created"])["tandem"])["id"])
    }
    public func fetch(ids: [String]) async throws -> [Message] {
        var result: [Message] = []
        for at in stride(from: 0, to: ids.count, by: config.pageSize) { let (rows, _) = try await objects(Array(ids[at..<min(at + config.pageSize, ids.count)]), bodies: true); for row in rows { if let value = try message(row) { result.append(value) } } }; return result
    }
    public func destroy(ids: [String]) async throws {
        for at in stride(from: 0, to: ids.count, by: deleteSize) {
            let (rows, _) = try await objects(Array(ids[at..<min(at + deleteSize, ids.count)]), bodies: false)
            guard try rows.allSatisfy({ try object($0["mailboxIds"])[folder] as? Bool == true }) else { throw TandemError.invalid("JMAP: refusing destruction outside replica mailbox") }
            if !rows.isEmpty { _ = try await call("Email/set", ["destroy": rows.map { try string($0["id"]) }]) }
        }
    }
    public func close() async {
        guard !closed else { return }; closed = true; worker?.cancel(); connection?.cancel(); output?.finish(); session.invalidateAndCancel(); await worker?.value
    }
}
