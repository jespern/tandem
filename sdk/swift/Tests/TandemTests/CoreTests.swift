import XCTest
import CryptoKit
import Foundation
import Tandem

final class CoreTests: XCTestCase {
    func fixture(_ name: String) throws -> [String: Any] {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        return try JSONSerialization.jsonObject(with: Data(contentsOf: root.appendingPathComponent("vectors/v2/" + name))) as! [String: Any]
    }
    func hex(_ value: Any?) -> Bytes { Bytes(hex: value as! String)! }

    func testV2DerivationsAndInventory() throws {
        let v = try fixture("core.json"), m = v["material"] as! [String: String], expected = v["derived"] as! [String: String]
        func b(_ key: String) -> Bytes { hex(m[key]) }
        let manager = try DeviceKeys.manager(seed: b("management_seed"))
        let got = ["home_scope": Tandem.homeScope(hub: b("hub"), uid: b("uid")).hex,
            "named": Tandem.namedID(Bytes("é".utf8)).hex, "named_decomposed": Tandem.namedID(Bytes("e\u{301}".utf8)).hex,
            "commitment": Tandem.commit(b("key")).hex, "key_id": Tandem.keyID(hub: b("hub"), agreement: b("agreement_public")).hex,
            "collection_tag": Tandem.collectionTag(key: b("key"), hub: b("hub"), epoch: b("epoch")),
            "home_tag": Tandem.homeTag(key: b("key"), hub: b("hub"), uid: b("uid"), epoch: b("epoch")),
            "inbox_tag": Tandem.inboxTag(uid: b("uid"), hub: b("hub")), "directory_key": Tandem.directoryKey(b("key"), hub: b("hub")).hex,
            "directory_tag": Tandem.directoryTag(b("key"), hub: b("hub")),
            "manager_signing_public": Bytes(manager.signing.publicKey.rawRepresentation).hex,
            "manager_agreement_public": Bytes(manager.agreement.publicKey.rawRepresentation).hex]
        XCTAssertEqual(got, expected)
        let inv = hex(v["inventory"])
        XCTAssertEqual(try Inventory.parse(inv).bytes(), inv)
        XCTAssertThrowsError(try Inventory.parse(inv + [0]))
        XCTAssertThrowsError(try Inventory.parse(Array(inv.dropLast())))
    }

    func writes(_ v: [String: Any]) throws -> [Write] {
        let m = v["material"] as! [String: String]
        return try (v["writes"] as! [[String: Any]]).map { row in
            guard case let .record(record) = try Kind1.parse(hex(row["payload"]), hub: hex(m["hub"]), scope: hex(m["scope"]))
            else { throw TandemError.invalid("fixture was not a record") }
            return record.writes[0]
        }
    }
    func testWriteVectorsAndPublicConstruction() throws {
        let v = try fixture("core.json"), m = v["material"] as! [String: String], rows = v["writes"] as! [[String: Any]]
        for (i, w) in try writes(v).enumerated() {
            XCTAssertEqual(try w.sigInput().hex, rows[i]["sig_input"] as? String)
            XCTAssertEqual(try w.id().hex, rows[i]["id"] as? String)
            try w.verify(publicKey: hex(m["signing_public"]))
            XCTAssertThrowsError(try w.verify(publicKey: w.author))
            var unsigned = Write(hub: w.hub, certificate: w.certificate, authorization: w.authorization,
                scope: w.scope, record: w.record, field: w.field, op: w.op, element: w.element, value: w.value,
                author: w.author, dot: w.dot, ref: w.ref, context: w.context, base: w.base)
            try unsigned.sign(with: Curve25519.Signing.PrivateKey(rawRepresentation: hex(m["signing_seed"])))
            try unsigned.verify(publicKey: hex(m["signing_public"]))
            guard case .record(let parsed) = try Kind1.parse(recordPayload(record: w.record, writes: [unsigned]), hub: w.hub, scope: w.scope)
            else { return XCTFail("payload was not a record") }
            try parsed.writes[0].verify(publicKey: hex(m["signing_public"]))
            for binding in [\Write.hub, \Write.certificate, \Write.authorization, \Write.author] {
                var changed = w; changed[keyPath: binding] = Bytes(repeating: 0, count: 32)
                XCTAssertThrowsError(try changed.verify(publicKey: hex(m["signing_public"])))
            }
        }
    }

    func testEnvelopesAndNativeHPKE() throws {
        let v = try fixture("core.json"), m = v["material"] as! [String: String], d = v["derived"] as! [String: String]
        func b(_ key: String) -> Bytes { hex(m[key]) }
        let plain = hex(v["plaintext"]), tag = d["collection_tag"]!
        let key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: b("agreement_private"))
        XCTAssertEqual(try Envelope.open(key: b("key"), hub: b("hub"), tag: tag, envelope: hex(v["symmetric"])).plaintext, plain)
        XCTAssertEqual(try Envelope.openInbox(hub: b("hub"), uid: b("uid"), key: key, envelope: hex(v["inbox"])), plain)
        for kind: UInt8 in [1, 3] {
            let sealed = try Envelope.seal(key: b("key"), hub: b("hub"), kind: kind, tag: tag, plaintext: plain)
            XCTAssertEqual(try Envelope.open(key: b("key"), hub: b("hub"), tag: tag, envelope: sealed).plaintext, plain)
        }
        let inbox = try Envelope.sealInbox(hub: b("hub"), uid: b("uid"), agreement: b("agreement_public"), plaintext: plain)
        XCTAssertEqual(try Envelope.openInbox(hub: b("hub"), uid: b("uid"), key: key, envelope: inbox), plain)
        XCTAssertThrowsError(try Envelope.open(key: b("key"), hub: Bytes(repeating: 0, count: 32), tag: tag, envelope: hex(v["symmetric"])))
        XCTAssertThrowsError(try Envelope.open(key: b("key"), hub: b("hub"), tag: tag.uppercased(), envelope: hex(v["symmetric"])))
        for length in 0..<hex(v["symmetric"]).count {
            XCTAssertThrowsError(try Envelope.open(key: b("key"), hub: b("hub"), tag: tag, envelope: Array(hex(v["symmetric"]).prefix(length))))
        }
        if let directory = ProcessInfo.processInfo.environment["TANDEM_INTEROP_DIR"] {
            let signature = try Curve25519.Signing.PrivateKey(rawRepresentation: b("signing_seed")).signature(for: plain)
            let artifact = try JSONSerialization.data(withJSONObject: ["inbox": inbox.hex, "signature": Bytes(signature).hex])
            try artifact.write(to: URL(fileURLWithPath: directory).appendingPathComponent("swift.json"))
        }
    }

    func testStrictInputs() throws {
        let json = try fixture("json.json")["cases"] as! [String: [[String: Any]]]
        for row in json["invalid"]! { XCTAssertThrowsError(try JSON.parse(hex(row["hex"])), row["why"] as? String ?? "") }
        for row in json["valid"]! { _ = try JSON.parse(hex(row["hex"])) }
        let b64 = try fixture("b64u.json")["cases"] as! [String: [[String: Any]]]
        for row in b64["invalid"]! { XCTAssertNil(B64u.decode(row["text"] as! String)) }
        for row in b64["valid"]! { XCTAssertEqual(B64u.decode(row["text"] as! String), hex(row["hex"])) }
        let ed = try fixture("ed25519.json")["cases"] as! [String: [[String: Any]]]
        for row in ed["signatures"]! {
            XCTAssertEqual(Ed25519.verify(signature: hex(row["signature"]), message: hex(row["message"]), publicKey: hex(row["public"])), row["valid"] as! Bool)
        }
    }

    func testMergeReplayAndScope() throws {
        let v = try fixture("core.json"), m = v["material"] as! [String: String], ws = try writes(v)
        for scenario in v["merges"] as! [[String: Any]] {
            let entries = try (scenario["indexes"] as! [Int]).map { try Entry(ws[$0], epoch: hex(m["epoch"]), epochNumber: 0) }
            for order in [entries, Array(entries.reversed())] {
                var record = try Record(hub: hex(m["hub"]), scope: hex(m["scope"]), id: hex(m["record"]))
                for e in order { try record.merge([e]) }; try record.merge(entries)
                XCTAssertEqual(record.kept.map { $0.id.hex }, scenario["kept"] as! [String])
                XCTAssertEqual(record.deleted, scenario["deleted"] as! Bool)
            }
        }
        var record = try Record(hub: hex(m["hub"]), scope: hex(m["scope"]), id: hex(m["record"]))
        var foreign = ws[0]; foreign.hub = Bytes(repeating: 0, count: 32)
        XCTAssertThrowsError(try record.merge([Entry(foreign, epoch: hex(m["epoch"]), epochNumber: 0)]))
        let clock = try nextClock(millis: 9, counter: 65535, now: 0)
        XCTAssertEqual(clock.millis, 10); XCTAssertEqual(clock.counter, 0)
        XCTAssertThrowsError(try nextClock(millis: (1 << 48) - 1, counter: 65535, now: 0))
    }

    func testCrossSDK() throws {
        guard ProcessInfo.processInfo.environment["TANDEM_VERIFY_INTEROP"] == "1",
            let directory = ProcessInfo.processInfo.environment["TANDEM_INTEROP_DIR"] else {
            throw XCTSkip("run sdk/check.py for cross-SDK verification")
        }
        let v = try fixture("core.json"), m = v["material"] as! [String: String]
        let key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: hex(m["agreement_private"]))
        for language in ["go", "python", "typescript", "swift"] {
            let data = try Data(contentsOf: URL(fileURLWithPath: directory).appendingPathComponent(language + ".json"))
            let artifact = try JSONSerialization.jsonObject(with: data) as! [String: String]
            XCTAssertTrue(Ed25519.verify(signature: hex(artifact["signature"]), message: hex(v["plaintext"]), publicKey: hex(m["signing_public"])), language)
            XCTAssertEqual(try Envelope.openInbox(hub: hex(m["hub"]), uid: hex(m["uid"]), key: key, envelope: hex(artifact["inbox"])), hex(v["plaintext"]), language)
        }
    }
}

private actor FakeStore: PushStore {
    let events: AsyncThrowingStream<Void, any Error>
    let continuation: AsyncThrowingStream<Void, any Error>.Continuation
    var calls = 0, subscribed = false, closed = false
    var added: [Message] = []
    var callWaiters: [(Int, CheckedContinuation<Void, Never>)] = []
    init() { let (s, c) = AsyncThrowingStream<Void, any Error>.makeStream(); events = s; continuation = c }
    func watch() async throws -> AsyncThrowingStream<Void, any Error> { subscribed = true; return events }
    func changes(since: String) async throws -> ChangeBatch {
        guard subscribed else { throw TandemError.invalid("not subscribed") }; calls += 1
        let ready = callWaiters.filter { $0.0 <= calls }
        callWaiters.removeAll { $0.0 <= calls }
        for (_, continuation) in ready { continuation.resume() }
        let out = ChangeBatch(added: added, removed: [], cursor: String(calls)); added = []; return out
    }
    func append(channel: String, body: Bytes) async throws -> String { added.append(Message(id: "local", channel: channel, body: body)); return "local" }
    func fetch(ids: [String]) async throws -> [Message] { [] }
    func destroy(ids: [String]) async throws {}
    func close() async { closed = true; continuation.finish() }
    func wake() { continuation.yield(()) }
    func untilChanges(_ target: Int) async {
        if calls >= target { return }
        await withCheckedContinuation { callWaiters.append((target, $0)) }
    }
}

final class SessionTests: XCTestCase {
    func testCloseUnderBackpressurePreservesQueuedBatch() async throws {
        let store = FakeStore(), session = try Session(store: store, capacity: 1)
        try await session.start()
        await store.untilChanges(1)
        await store.wake()
        await store.untilChanges(2)
        let pending = Task { try await session.submit(channel: "channel", body: [1]) }
        await session.close()
        do { _ = try await pending.value; XCTFail("submission unexpectedly succeeded") } catch {}
        var updates = session.updates.makeAsyncIterator()
        let first = try await updates.next(); XCTAssertEqual(first?.cursor, "1")
        let ended = try await updates.next(); XCTAssertNil(ended)
    }
    func testPushSubmitReconnectAndClose() async throws {
        let store = FakeStore(), session = try Session(store: store, capacity: 4)
        try await session.start()
        var updates = session.updates.makeAsyncIterator()
        let initial = try await updates.next(); XCTAssertEqual(initial?.cursor, "1")
        await store.wake(); let pushed = try await updates.next(); XCTAssertEqual(pushed?.cursor, "2")
        let ident = try await session.submit(channel: "channel", body: Bytes("local edit".utf8)); XCTAssertEqual(ident, "local")
        let local = try await updates.next(); XCTAssertEqual(local?.added.first?.body, Bytes("local edit".utf8))
        await store.wake(); let reconnected = try await updates.next(); XCTAssertEqual(reconnected?.cursor, "4")
        await session.close()
        let calls = await store.calls, closed = await store.closed; XCTAssertEqual(calls, 4); XCTAssertTrue(closed)
        let ended = try await updates.next(); XCTAssertNil(ended)
    }
}
