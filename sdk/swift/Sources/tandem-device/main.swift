import Foundation
import Tandem
import TandemEngine
import TandemIMAP

// One Tandem device driven over stdin/stdout, one JSON command per line, for the cross-engine tests in interop/.
// Every command answers one line: {"ok":true,…} or {"ok":false,"error":"…"}. Bytes travel as b64u. The Go engine has
// a driver that speaks the same commands, so a test can put a Swift device and a Go device on one hub.

struct Failure: Error { let message: String }

actor Driver {
    var account: MailboxAccount?, folder = "Tandem", insecure = false
    var device: Device?, ms: Bytes?
    var blobs: BlobStore?

    func configuration() throws -> IMAPStore.Configuration {
        guard let account else { throw Failure(message: "no hub: send {\"op\":\"hub\",…} first") }
        return .init(host: account.host, port: account.port, username: account.user, password: account.password,
                     security: insecure ? .tlsTrustingAnyCertificateForTesting : .tls, folder: folder)
    }

    func refs() async throws -> [BlobRef] {
        guard let blobs else { throw Failure(message: "blobs not in use") }
        var out: [BlobRef] = [], cursor = ""
        while true {
            let c = try await blobs.changes(since: cursor)
            out += c.added
            if !c.more { return out }
            cursor = c.cursor
        }
    }

    func manifest(_ list: Bytes, _ record: Bytes, _ name: String) async throws -> Blob.Manifest {
        guard let v = try await dev().value(list: list, record: record, field: "_blob:" + name), case let .manifest(m) = Blob.parse(v)
        else { throw Failure(message: "no manifest") }
        return m
    }

    func store() async throws -> IMAPStore {
        guard let account else { throw Failure(message: "no hub: send {\"op\":\"hub\",…} first") }
        let imap = IMAPStore(.init(host: account.host, port: account.port, username: account.user, password: account.password,
                                   security: insecure ? .tlsTrustingAnyCertificateForTesting : .tls, folder: folder))
        try await imap.ensureFolder()
        return imap
    }

    func dev() throws -> Device {
        guard let device else { throw Failure(message: "no device yet") }
        return device
    }

    func handle(_ c: [String: Any]) async throws -> [String: Any] {
        func s(_ k: String) throws -> String {
            guard let v = c[k] as? String else { throw Failure(message: "missing \(k)") }
            return v
        }
        func b(_ k: String) throws -> Bytes {
            guard let v = B64u.decode(try s(k)) else { throw Failure(message: "\(k) is not b64u") }
            return v
        }
        switch try s("op") {
        case "hub":
            account = MailboxAccount(host: try s("host"), port: UInt16(c["port"] as? Int ?? 993), user: try s("user"),
                                     password: try s("password"))
            folder = c["folder"] as? String ?? "Tandem"
            insecure = c["insecure"] as? Bool ?? false
            return [:]
        case "found":
            let keys = DeviceKeys()
            let f = try Founder.found(account: account!, keys: keys, label: try s("label"))
            let d = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: keys, store: try Store(path: ":memory:"),
                                    hub: try await store())
            try await d.publish(f)
            if let password = c["hubPassword"] as? String { try await d.changePassword(to: password, ms: f.managementSeed) }
            device = d; ms = f.managementSeed
            return ["participant": B64u.encode(f.participant)]
        case "unlock":
            ms = try await dev().unlock(password: try s("password"))
            return [:]
        case "invite":
            guard let ms else { throw Failure(message: "not the hub's admin") }
            let link = try await dev().invite(label: try s("label"), account: account!, ms: ms)
            return ["link": B64u.encode(try link.json())]
        case "join":
            device = try await Device.join(link: try InvitationLink.parse(try b("link")), keys: DeviceKeys(), label: try s("label"),
                                           store: try Store(path: ":memory:"), hub: try await store())
            return [:]
        case "startPairing":
            let link = try await dev().startPairing(label: try s("label"), account: account!)
            return ["link": B64u.encode(link.json())]
        case "pair":
            device = try await Device.pair(link: try PairingLink.parse(try b("link")), keys: DeviceKeys(), label: try s("label"),
                                           store: try Store(path: ":memory:"), hub: try await store())
            return [:]
        case "claims":
            let claims = try await dev().pendingClaims()
            return ["claims": claims.map { ["id": B64u.encode($0.id), "participant": B64u.encode($0.participant), "label": $0.label] }]
        case "approve":
            guard let ms else { throw Failure(message: "not the hub's admin") }
            try await dev().approve(claim: try b("claim"), ms: ms)
            return [:]
        case "revoke":
            guard let ms else { throw Failure(message: "not the hub's admin") }
            try await dev().revoke(certificate: try b("certificate"), ms: ms)
            return [:]
        case "me":
            guard let me = try await dev().me else { return ["me": NSNull()] }
            return ["participant": B64u.encode(me.participant), "certificate": B64u.encode(me.certificate),
                    "awaitingApproval": try await dev().awaitingApproval]
        case "sync":
            return ["applied": try await dev().sync()]
        case "setName":
            try await dev().setName(try s("name"))
            return [:]
        case "people":
            return ["people": try await dev().people().map { ["id": B64u.encode($0.id), "name": $0.name ?? NSNull()] as [String: Any] }]
        case "createList":
            return ["list": B64u.encode(try await dev().createList(name: try s("name")))]
        case "lists":
            return ["lists": try await dev().lists().map { ["id": B64u.encode($0.id), "name": $0.name ?? NSNull()] as [String: Any] }]
        case "share":
            try await dev().share(list: try b("list"), with: try b("participant"),
                                  role: Statement.Role(rawValue: try s("role")) ?? .writer)
            return [:]
        case "offers":
            return ["offers": try await dev().pendingOffers().map { ["list": B64u.encode($0.list), "name": $0.name ?? NSNull()] as [String: Any] }]
        case "accept":
            try await dev().accept(list: try b("list"))
            return [:]
        case "set":
            try await dev().setField(list: try b("list"), record: try b("record"), field: try s("field"), value: try b("value"))
            return [:]
        case "add":
            try await dev().add(list: try b("list"), record: try b("record"), field: try s("field"), element: try b("element"))
            return [:]
        case "remove":
            try await dev().remove(list: try b("list"), record: try b("record"), field: try s("field"), element: try b("element"))
            return [:]
        case "delete":
            try await dev().deleteRecord(list: try b("list"), record: try b("record"))
            return [:]
        case "items":
            let items = try await dev().items(list: try b("list"))
            return ["items": items.map { item -> [String: Any] in
                var values: [String: Any] = [:], sets: [String: Any] = [:]
                for f in item.fields {
                    if let v = item.value(f) { values[f] = B64u.encode(v) }
                    let e = item.elements(f)
                    if !e.isEmpty { sets[f] = e.map { B64u.encode($0) } }
                }
                return ["record": B64u.encode(item.id), "values": values, "sets": sets]
            }]
        case "role":
            let role = try await dev().role(list: try b("list"))
            return ["role": role?.rawValue ?? NSNull()]
        case "removeMember":
            try await dev().remove(try b("participant"), from: try b("list"))
            return [:]
        case "rekey":
            try await dev().rekey(list: try b("list"))
            return [:]
        case "copy":
            let copy = try await dev().copy(list: try b("list"))
            return ["list": B64u.encode(copy.list), "shared": copy.shared.map(B64u.encode), "undecided": copy.undecided.map(B64u.encode)]
        case "copyOf":
            return ["from": try await dev().copyOf(list: try b("list")).map(B64u.encode) ?? NSNull()]
        case "useBlobs":
            let store = BlobStore(try configuration(), options: .init(folder: folder + "-blobs"))
            blobs = store
            try await dev().useBlobs(store, settings: BlobSettings(chunkSize: c["chunkSize"] as? Int ?? Blob.defaultChunk))
            return [:]
        case "settleBlobs":
            try await dev().settleBlobs()
            return [:]
        case "checkBlobs":
            let d = try dev()
            await d.checkBlobs()
            await d.settleBlobs()
            return [:]
        case "attach":
            try await dev().attach(list: try b("list"), record: try b("record"), name: try s("name"), content: try b("content"))
            return [:]
        case "blob":
            guard let info = try await dev().blob(list: try b("list"), record: try b("record"), name: try s("name")) else { return ["state": NSNull()] }
            let names: [BlobState: String] = [.here: "here", .remote: "remote", .downloading: "downloading", .unavailable: "unavailable",
                                              .unreadable: "unreadable", .uploading: "uploading"]
            return ["state": names[info.state]!, "size": info.size]
        case "fetchBlob":
            let url = try await dev().fetchBlob(list: try b("list"), record: try b("record"), name: try s("name"))
            return ["content": B64u.encode(Bytes(try Data(contentsOf: url)))]
        case "chunkCounts":
            let m = try await manifest(try b("list"), try b("record"), try s("name")), all = try await refs()
            return ["counts": (m.chunks ?? []).map { e in all.filter { $0.address == e.address }.count }]
        case "dropChunk":
            let m = try await manifest(try b("list"), try b("record"), try s("name")), all = try await refs()
            let address = m.chunks![c["index"] as? Int ?? 0].address
            try await blobs!.destroy(ids: all.filter { $0.address == address }.map(\.id))
            return [:]
        default:
            throw Failure(message: "unknown op")
        }
    }
}

let driver = Driver()
for try await line in FileHandle.standardInput.bytes.lines {
    var reply: [String: Any]
    do {
        guard let command = try JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any] else { throw Failure(message: "not an object") }
        reply = try await driver.handle(command)
        reply["ok"] = true
    } catch let f as Failure {
        reply = ["ok": false, "error": f.message]
    } catch {
        reply = ["ok": false, "error": "\(error)"]
    }
    let data = try JSONSerialization.data(withJSONObject: reply, options: [.sortedKeys])
    FileHandle.standardOutput.write(data + Data("\n".utf8))
}
