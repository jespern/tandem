import XCTest
import Foundation
import Tandem
@testable import TandemEngine

/// `run()` and `events()`: a change on one device reaches another by push alone, and the app hears of it.
final class LiveTests: XCTestCase {
    let account = MailboxAccount(host: "imap.example.org", port: 993, user: "hub@example.org", password: "app-password")

    /// The first event matching `match`, or a failure after `seconds`: a safety net, not a schedule.
    static func next(_ events: AsyncStream<DeviceEvent>, within seconds: Double = 5,
                     where match: @escaping @Sendable (DeviceEvent) -> Bool) async throws -> DeviceEvent {
        try await withThrowingTaskGroup(of: DeviceEvent?.self) { group in
            group.addTask { for await e in events where match(e) { return e }; return nil }
            group.addTask { try await Task.sleep(for: .seconds(seconds)); return nil }
            defer { group.cancelAll() }
            guard let first = try await group.next(), let e = first else { throw TandemError.invalid("no such event in time") }
            return e
        }
    }

    func testAnEditArrivesByPush() async throws {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let alice = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"),
                                          hub: await hub.connect())
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        let bobEvents = await bob.events(), aliceEvents = await alice.events()
        let bobRun = Task { try await bob.run() }, aliceRun = Task { try await alice.run() }
        defer { bobRun.cancel(); aliceRun.cancel() }

        let list = try await bob.createList(name: "Groceries")
        try await bob.share(list: list, with: aliceID, role: .writer)
        _ = try await Self.next(aliceEvents) { $0 == .offers }
        try await alice.accept(list: list)
        let milk = alice.newRecordID()
        try await alice.setField(list: list, record: milk, field: "title", value: Bytes("Milk".utf8))
        let e = try await Self.next(bobEvents) { if case let .items(l, records) = $0 { return l == list && records.contains(milk) }; return false }
        XCTAssertEqual(e, .items(list: list, records: [milk]))
        let seen = await bob.value(list: list, record: milk, field: "title")
        XCTAssertEqual(seen, Bytes("Milk".utf8))
    }

    func testPairingAndKeyChangeLive() async throws {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s Mac")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let alice = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"), hub: await hub.connect())
        let aliceMe = await alice.me
        let aliceID = try XCTUnwrap(aliceMe?.participant)
        let bobEvents = await bob.events(), aliceEvents = await alice.events()
        let bobRun = Task { try await bob.run() }, aliceRun = Task { try await alice.run() }
        defer { bobRun.cancel(); aliceRun.cancel() }
        let list = try await bob.createList(name: "Groceries")
        try await bob.share(list: list, with: aliceID, role: .writer)
        _ = try await Self.next(aliceEvents) { $0 == .offers }
        try await alice.accept(list: list)
        let milk = alice.newRecordID()
        try await alice.setField(list: list, record: milk, field: "title", value: Bytes("Milk".utf8))
        _ = try await Self.next(bobEvents) { if case let .items(l, r) = $0 { return l == list && r.contains(milk) }; return false }
        let link = try await alice.startPairing(label: "Tablet", account: account)
        let tablet = try await Device.pair(link: link, keys: DeviceKeys(), label: "Alice’s tablet", store: try Store(path: ":memory:"),
                                           hub: await hub.connect())
        let tabletEvents = await tablet.events()
        let tabletRun = Task { try await tablet.run() }
        defer { tabletRun.cancel() }
        _ = try await Self.next(bobEvents) { $0 == .claims }
        let claims = await bob.pendingClaims()
        try await bob.approve(claim: try XCTUnwrap(claims.first).id, ms: f.managementSeed)
        _ = try await Self.next(tabletEvents) { $0 == .approved }
        try await bob.rekey(list: list)
        let bread = alice.newRecordID()
        var wrote = false
        for _ in 0..<8 where !wrote {
            do { try await alice.setField(list: list, record: bread, field: "title", value: Bytes("Bread".utf8)); wrote = true }
            catch { _ = try? await Self.next(aliceEvents, within: 2) { _ in true } }
        }
        XCTAssertTrue(wrote)
        _ = try await Self.next(bobEvents) { if case let .items(l, r) = $0 { return l == list && r.contains(bread) }; return false }
    }

    /// §10.3 wake hints: every participant's device names the same opaque tag, and a device says when it published.
    func testWakeHints() async throws {
        let hub = MemoryHub(), bobKeys = DeviceKeys()
        let f = try Founder.found(account: account, keys: bobKeys, label: "Bob’s iPhone")
        let bob = try Device.open(genesis: f.genesis, directoryKey: f.directoryKey, keys: bobKeys, store: try Store(path: ":memory:"),
                                  hub: await hub.connect())
        try await bob.publish(f)
        let alice = try await Device.join(link: try await bob.invite(label: "Alice", account: account, ms: f.managementSeed),
                                          keys: DeviceKeys(), label: "Alice’s iPhone", store: try Store(path: ":memory:"),
                                          hub: await hub.connect())
        XCTAssertEqual(alice.wakeTag, bob.wakeTag)
        let other = try Founder.found(account: account, keys: DeviceKeys(), label: "x")
        XCTAssertNotEqual(Control.wakeTag(directoryKey: other.directoryKey, hub: other.pin.hub), bob.wakeTag)
        let events = await bob.events()
        let list = try await bob.createList(name: "Groceries")
        _ = try await Self.next(events) { $0 == .published }
        _ = list
    }
}
