import Foundation
import Tandem
import TandemEngine
@testable import TandemJMAP
import XCTest

final class JMAPTests: XCTestCase {
    func testJMAPExtensionsMayContainFractionalNumbers() throws {
        let data = try JSONSerialization.data(withJSONObject: ["extension": 0.5])
        XCTAssertEqual(try decodeJMAPObject(data)["extension"] as? Double, 0.5)
    }
    func testFastmailConfiguration() async throws {
        let config = JMAPConfig.fastmail(token: "example")
        XCTAssertEqual(config.sessionURL, "https://api.fastmail.com/jmap/session")
        XCTAssertEqual(config.authorization, "Bearer example")
        var invalid = config; invalid.sessionURL = "https://evil.invalid/"
        do { _ = try await JMAPStore.open(invalid); XCTFail("Fastmail preset trusted another host") } catch {}
    }
    func testRealJMAP() async throws {
        guard let encoded = ProcessInfo.processInfo.environment["TANDEM_JMAP_FIXTURE"] else { throw XCTSkip("run sdk/check_jmap.py for disposable Stalwart") }
        struct Fixture: Decodable { let sessionUrl: String, authorization: String }
        let fixture = try JSONDecoder().decode(Fixture.self, from: Data(encoded.utf8))
        var config = JMAPConfig(sessionURL: fixture.sessionUrl, authorization: fixture.authorization)
        config.folderName = "Tandem-swift-" + UUID().uuidString; config.pageSize = 2; config.commandTimeout = 5; config.allowsSelfSignedLoopback = true
        let reader = try await JMAPStore.open(config), writer = try await JMAPStore.open(config)
        do {
            let stream = try await reader.watch()
            let initial = try await reader.changes(since: "")
            let pushed = Task { var events = stream.makeAsyncIterator(); _ = try await events.next() }
            let key = H(Bytes("key".utf8)), hub = H(Bytes("hub".utf8)), epoch = H(Bytes("epoch".utf8)), tag = Tandem.collectionTag(key: key, hub: hub, epoch: epoch)
            var ids: [String] = [], bodies: [Bytes] = []
            for i in 0..<5 { let body = try Envelope.seal(key: key, hub: hub, kind: 1, tag: tag, plaintext: [UInt8(i)]); ids.append(try await writer.append(channel: tag, body: body)); bodies.append(body) }
            try await pushed.value
            let batch = try await reader.changes(since: initial.cursor)
            XCTAssertEqual(batch.added.count, 5)
            let replay = try await reader.changes(since: initial.cursor); XCTAssertEqual(replay, batch)
            let full = try await reader.changes(since: ""); XCTAssertEqual(full.added.count, 5)
            let fetched = try await reader.fetch(ids: ids); XCTAssertEqual(fetched.count, 5)
            for (id, body) in zip(ids, bodies) { XCTAssertEqual(fetched.first { $0.id == id }?.body, body) }
            var otherConfig = config; otherConfig.folderName += "-other"
            let other = try await JMAPStore.open(otherConfig)
            do { try await other.destroy(ids: [ids[0]]); XCTFail("destroyed another mailbox's mail") } catch {}
            await other.close()
            try await writer.destroy(ids: ids)
            let removed = try await reader.changes(since: batch.cursor); XCTAssertEqual(Set(removed.removed), Set(ids))
            await reader.close(); await writer.close()
        } catch { await reader.close(); await writer.close(); throw error }
    }
    func testEngineUsesExplicitJMAPTransport() async throws {
        guard let encoded = ProcessInfo.processInfo.environment["TANDEM_JMAP_FIXTURE"] else { throw XCTSkip("run sdk/check_jmap.py") }
        struct Fixture: Decodable { let sessionUrl: String, authorization: String }
        let fixture = try JSONDecoder().decode(Fixture.self, from: Data(encoded.utf8))
        var config = JMAPConfig(sessionURL: fixture.sessionUrl, authorization: fixture.authorization)
        config.folderName = "Tandem-engine-jmap-" + UUID().uuidString; config.allowsSelfSignedLoopback = true
        let sender = try await JMAPStore.open(config), receiver = try await JMAPStore.open(config)
        do {
            let keys = DeviceKeys(), founding = try Founder.found(account: MailboxAccount(host: "imap.example.org", port: 993, user: "fixture", password: "not-used-for-jmap"), keys: keys, label: "Swift JMAP")
            let device = try Device.open(genesis: founding.genesis, directoryKey: founding.directoryKey, keys: keys, store: Store(path: ":memory:"), hub: sender)
            try await device.publish(founding)
            let list = try await device.createList(name: "JMAP engine"), record = device.newRecordID()
            try await device.setField(list: list, record: record, field: "title", value: Bytes("Push only".utf8))
            let replica = try Device.open(genesis: founding.genesis, directoryKey: founding.directoryKey, keys: keys, store: Store(path: ":memory:"), hub: receiver)
            try await replica.sync()
            let items = await replica.items(list: list)
            XCTAssertEqual(items.first { $0.id == record }?.text("title"), "Push only")
            await sender.close(); await receiver.close()
        } catch { await sender.close(); await receiver.close(); throw error }
    }
}
