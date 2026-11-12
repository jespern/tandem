import CryptoKit
import Foundation
import Testing

@testable import Tandem

// Checks the Swift SDK against the vectors the Go reference SDK writes
// (vectors/v1). Vector files are test input, so Foundation reads them.

let vectors = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("vectors/v1")

func load(_ name: String) throws -> Any {
    try JSONSerialization.jsonObject(with: Data(contentsOf: vectors.appendingPathComponent(name)))
}

func cases(_ name: String) throws -> Any { (try load(name) as! [String: Any])["cases"]! }

extension Dictionary where Key == String, Value == Any {
    func hex(_ k: String) -> Bytes { Bytes(hex: self[k] as! String)! }
    func str(_ k: String) -> String { self[k] as! String }
    func dict(_ k: String) -> [String: Any] { self[k] as! [String: Any] }
    func list(_ k: String) -> [[String: Any]] { self[k] as! [[String: Any]] }
}

@Test func keys() throws {
    for c in try cases("keys.json") as! [[String: Any]] {
        let u = try User(secret: c.hex("user_secret"))
        #expect(Bytes(u.signing.rawRepresentation) == c.hex("uks_seed"))
        #expect(u.uid == c.hex("uid"))
        #expect(B64u.encode(u.uid) == c.str("uid_b64u"))
        #expect(Tandem.displayUID(u.uid) == c.str("uid_display"))
        #expect(u.uka == c.hex("uka"))
        #expect(Tandem.keyID(u.uka) == c.hex("kid"))
        #expect(u.home == c.hex("home_key"))
        #expect(Tandem.homeTag(u.home) == c.str("home_tag"))
        #expect(Tandem.homeScope(u.uid) == c.hex("home_scope"))
        #expect(Tandem.inboxTag(u.uid) == c.str("inbox_tag"))
        #expect(Tandem.backupKey(u.secret) == c.str("backup_key"))
    }
}

@Test func backupKeys() throws {
    let c = try cases("backup.json") as! [String: Any]
    for v in c.list("valid") { #expect(try Tandem.parseBackupKey(v.str("input")) == v.hex("secret")) }
    for v in c.list("invalid") {
        #expect(throws: TandemError.self, "\(v.str("why"))") { try Tandem.parseBackupKey(v.str("input")) }
    }
}

@Test func b64u() throws {
    let c = try cases("b64u.json") as! [String: Any]
    for v in c.list("valid") {
        #expect(B64u.decode(v.str("text")) == v.hex("hex"))
        #expect(B64u.encode(v.hex("hex")) == v.str("text"))
    }
    for v in c.list("invalid") { #expect(B64u.decode(v.str("text")) == nil, "\(v.str("why"))") }
}

@Test func strictJSON() throws {
    let c = try cases("json.json") as! [String: Any]
    for v in c.list("valid") { #expect(throws: Never.self, "\(v.str("why"))") { try JSON.parse(v.hex("hex")) } }
    for v in c.list("invalid") {
        #expect(throws: TandemError.self, "\(v.str("why"))") { try JSON.parse(v.hex("hex")) }
    }
    // Foundation reads a repeated name without complaint: why the check exists.
    let dup = Data(#"{"a":1,"a":2}"#.utf8)
    #expect((try? JSONSerialization.jsonObject(with: dup)) != nil)
}

@Test func tags() throws {
    let c = try cases("tags.json") as! [String: Any]
    let col = c.dict("collection")
    #expect(Tandem.collectionTag(key: col.hex("epoch_key"), epoch: col.hex("epoch_id")) == col.str("tag"))
    #expect(Tandem.commit(col.hex("epoch_key")) == col.hex("commit"))
    #expect(Tandem.homeTag(c.dict("home").hex("home_key")) == c.dict("home").str("tag"))
    #expect(Tandem.inboxTag(c.dict("inbox").hex("uid")) == c.dict("inbox").str("tag"))
    let inv = c.dict("invite")
    #expect(Tandem.inviteKey(inv.hex("invite_secret")) == inv.hex("invite_key"))
    #expect(Tandem.inviteTag(inv.hex("invite_secret")) == inv.str("tag"))
    #expect(Bytes(try Tandem.inviteSigner(inv.hex("invite_secret")).publicKey.rawRepresentation) == inv.hex("invite_signing_public"))
    let pair = c.dict("pairing")
    #expect(Tandem.pairKey(pair.hex("pairing_secret")) == pair.hex("pair_key"))
    #expect(Tandem.pairTag(pair.hex("pairing_secret")) == pair.str("tag"))
    var ids = Set<Bytes>()
    for n in c.list("named") {
        #expect(Tandem.namedID(n.hex("name_hex")) == n.hex("record_id"))
        ids.insert(n.hex("record_id"))
    }
    #expect(ids.count == c.list("named").count, "NFC and NFD spellings name different records")
}

@Test func envelopes() throws {
    let c = try cases("envelopes.json") as! [String: Any]
    for v in c.list("symmetric") {
        let kind = UInt8(v["kind"] as! Int)
        #expect(Envelope.aad(kind: kind, tag: v.str("tag")) == v.hex("aad"))
        let env = try Envelope.seal(key: v.hex("key"), kind: kind, tag: v.str("tag"), plaintext: v.hex("plaintext"), salt: v.hex("salt"))
        #expect(env == v.hex("envelope"))
        #expect(H(env) == v.hex("envelope_id"))
        let opened = try Envelope.open(key: v.hex("key"), tag: v.str("tag"), envelope: env)
        #expect(opened.kind == kind && opened.plaintext == v.hex("plaintext"))
    }
    for v in c.list("reject") {
        #expect(throws: (any Error).self, "\(v.str("why"))") {
            try Envelope.open(key: v.hex("key"), tag: v.str("tag"), envelope: v.hex("envelope"))
        }
    }
    let inbox = c.dict("inbox")
    let to = try User(secret: inbox.hex("recipient_secret"))
    #expect(try Envelope.openInbox(to, envelope: inbox.hex("envelope")) == inbox.hex("plaintext"))
    #expect(throws: (any Error).self) { try Envelope.openInbox(User(secret: inbox.hex("other_secret")), envelope: inbox.hex("envelope")) }
    // And the other way: Swift seals, Swift opens (Go opens Swift's in the cross-check).
    let sealed = try Envelope.sealInbox(uid: to.uid, uka: to.uka, plaintext: Bytes("hi".utf8))
    #expect(try Envelope.openInbox(to, envelope: sealed) == Bytes("hi".utf8))

    let p = c.dict("pairing_reply")
    let ek = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: p.hex("ek_private"))
    #expect(Bytes(ek.publicKey.rawRepresentation) == p.hex("ek"))
    #expect(Envelope.pairAAD(uid: p.hex("uid"), ek: p.hex("ek")) == p.hex("aad"))
    #expect(try Envelope.openPairing(ek, uid: p.hex("uid"), sealed: p.hex("sealed")) == p.hex("user_secret"))
}

@Test func writes() throws {
    for c in try cases("writes.json") as! [[String: Any]] {
        let payload = Bytes(c.str("payload").utf8)
        guard case let .record(rs) = try Kind1.parse(payload, scope: c.hex("scope")) else {
            Issue.record("not a record payload: \(c.str("name"))"); continue
        }
        let w = rs.writes[0]
        #expect(try w.sigInput() == c.hex("sig_input"), "\(c.str("name"))")
        #expect(try w.id() == c.hex("write_id"), "\(c.str("name"))")
        #expect(w.sig == c.hex("signature"))
        #expect(throws: Never.self, "\(c.str("name"))") { try w.verify() }
        // CryptoKit signs with added randomness (Go doesn't), so a signature
        // made here differs from the vector's but must still verify, and the
        // write ID, which hashes sigInput and not the signature, is the same.
        let author = try User(secret: c.hex("author_secret"))
        let mine = try author.sign(w.sigInput())
        #expect(mine != c.hex("signature"))
        #expect(Ed25519.verify(signature: mine, message: try w.sigInput(), publicKey: author.uid))
    }
}

@Test func invalidWrites() throws {
    for c in try cases("writes-invalid.json") as! [[String: Any]] {
        let ok: Bool
        do {
            guard case let .record(rs) = try Kind1.parse(Bytes(c.str("payload").utf8), scope: c.hex("scope")) else {
                throw TandemError.invalid("not a record")
            }
            for w in rs.writes { try w.verify() }
            ok = true
        } catch { ok = false }
        #expect(ok == (c["valid"] as! Bool), "\(c.str("why"))")
    }
}

@Test func genesis() throws {
    let c = try cases("genesis.json") as! [String: Any]
    guard case let .record(rs) = try Kind1.parse(Bytes(c.str("payload").utf8), scope: Bytes(repeating: 0, count: 16)) else {
        Issue.record("not a record"); return
    }
    let w = rs.writes[0]
    #expect(rs.record == c.hex("roster_record_id"))
    #expect(rs.record == Tandem.namedID(Bytes("_roster".utf8)))
    #expect(w.value == Bytes(c.str("statement").utf8))
    #expect(try w.sigInput() == c.hex("sig_input"))
    #expect(try w.id() == c.hex("statement_id"))
    #expect(Array(try w.id().prefix(16)) == c.hex("collection_id"))
    #expect(try Tandem.collectionTag(key: c.hex("epoch_key"), epoch: w.id()) == c.str("epoch_tag"))
    #expect(throws: Never.self) { try w.verify() }
    #expect(w.author == (try User(secret: c.hex("owner_secret"))).uid)
}

@Test func inventories() throws {
    let c = try cases("inventory.json") as! [String: Any]
    for v in c.list("valid") {
        var inv = Inventory()
        for e in v.list("entries") { inv.entries[e.hex("record")] = (e["writes"] as! [String]).map { Bytes(hex: $0)! } }
        let b = try inv.bytes()
        #expect(b == v.hex("bytes"))
        #expect(H(b) == v.hex("snapshot"))
        #expect(try Inventory.parse(b).entries.mapValues { $0.sorted(by: lexLess) } == inv.entries.mapValues { $0.sorted(by: lexLess) })
    }
    for v in c.list("invalid") {
        #expect(throws: TandemError.self, "\(v.str("why"))") { try Inventory.parse(v.hex("bytes")) }
    }
}

@Test func signedObjects() throws {
    for c in try cases("signed.json") as! [[String: Any]] {
        let name = c.str("name")
        let input: Bytes
        let a = c.hex("signer_public")
        switch name {
        case "join request (sig)", "join request, name with a combining accent":
            continue  // joinRequests() checks these
        case "join request (isig, by the invite key)":
            let s = c.hex("invite_secret")
            #expect(Bytes(try Tandem.inviteSigner(s).publicKey.rawRepresentation) == a)
            input = Signed.invite(collection: c.hex("collection_id"), invite: c.hex("invite_id"), uid: aimeeUID())
        case "grant":
            input = Signed.grant(recipient: aimeeUID(), collection: c.hex("collection_id"), at: c.hex("at"),
                                 epoch: c.hex("epoch"), key: c.hex("epoch_key"))
            #expect(Tandem.commit(c.hex("epoch_key")) == c.hex("commit"))
        case "key request":
            input = Signed.keyRequest(collection: c.hex("collection_id"), have: c.hex("have"), uid: a)
        case "leave notice":
            input = Signed.leave(collection: c.hex("collection_id"), uid: a, at: c.hex("at"))
        case "pairing challenge":
            input = Signed.pairChallenge(p: c.hex("pairing_secret"), ek: c.hex("ek"), n: c.hex("n"))
        default:
            Issue.record("unknown case \(name)"); continue
        }
        #expect(input == c.hex("signed_input"), "\(name)")
        #expect(Ed25519.verify(signature: c.hex("signature"), message: input, publicKey: a), "\(name)")
    }
}

@Test func joinRequests() throws {
    let aimee = (try cases("keys.json") as! [[String: Any]])[0]
    for c in try cases("signed.json") as! [[String: Any]] where c["display_name_hex"] != nil {
        // The display name is signed as its UTF-8 bytes: "Aimée" spelled two ways signs two ways.
        let input = Signed.join(collection: c.hex("collection_id"), invite: c.hex("invite_id"), uid: aimee.hex("uid"),
                                uka: aimee.hex("uka"), name: c.hex("display_name_hex"))
        #expect(input == c.hex("signed_input"), "\(c.str("name"))")
        #expect(Ed25519.verify(signature: c.hex("signature"), message: input, publicKey: aimee.hex("uid")))
    }
}

func aimeeUID() -> Bytes {
    let keys = try! cases("keys.json") as! [[String: Any]]
    return keys[0].hex("uid")
}

@Test func pairingCodes() throws {
    for c in try cases("pairing.json") as! [[String: Any]] {
        #expect(Tandem.pairCode(p: c.hex("pairing_secret"), ek: c.hex("ek"), n: c.hex("n")) == c.str("code"))
    }
}

@Test func ed25519() throws {
    let c = try cases("ed25519.json") as! [String: Any]
    for v in c.list("signatures") {
        #expect(Ed25519.verify(signature: v.hex("signature"), message: v.hex("message"), publicKey: v.hex("public")) == (v["valid"] as! Bool),
                "case \(v["case"]!)")
    }
    for v in c.list("invalid_public_keys") {
        #expect(!Ed25519.isValidPublicKey(v.hex("public")), "\(v.str("why"))")
    }
}

/// What CryptoKit alone would accept, for the record: run with the strict
/// checks off to see which speccheck cases they are needed for.
@Test func cryptoKitAloneOnSpeccheck() throws {
    let c = try cases("ed25519.json") as! [String: Any]
    var differ: [String] = []
    for v in c.list("signatures") {
        let plain = (try? Curve25519.Signing.PublicKey(rawRepresentation: v.hex("public")))
            .map { $0.isValidSignature(v.hex("signature"), for: v.hex("message")) } ?? false
        if plain != (v["valid"] as! Bool) { differ.append("\(v["case"]!)") }
    }
    print("CryptoKit alone differs from Tandem's rules on cases: \(differ.joined(separator: ", "))")
}

@Test func messages() throws {
    let c = try cases("messages.json") as! [String: Any]
    let build = c.dict("build")
    let built = try HubMessage.parse(Bytes(build.str("raw").utf8))
    #expect(built.tag == build.str("tag") && built.envelope == build.hex("envelope"))
    for v in c.list("read") {
        let m = try HubMessage.parse(v.hex("raw"))
        #expect(m.tag == v.str("tag"), "\(v.str("why"))")
        #expect(m.envelope == v.hex("envelope"), "\(v.str("why"))")
    }
}
