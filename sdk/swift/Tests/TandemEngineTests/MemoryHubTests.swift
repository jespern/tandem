import XCTest
import Tandem
@testable import TandemEngine

final class MemoryHubTests: XCTestCase {
    func testPushChangesAndRemovals() async throws {
        let hub = MemoryHub()
        let a = await hub.connect(), b = await hub.connect()
        let pushes = try await b.watch()
        var wakes = pushes.makeAsyncIterator()
        let id = try await a.append(channel: "x", body: [1, 2])
        _ = try await wakes.next()
        var batch = try await b.changes(since: "")
        XCTAssertEqual(batch.added.map(\.id), [id])
        XCTAssertEqual(batch.added.first?.body, [1, 2])
        try await a.destroy(ids: [id])
        _ = try await wakes.next()
        batch = try await b.changes(since: batch.cursor)
        XCTAssertEqual(batch.removed, [id])
        XCTAssertTrue(batch.added.isEmpty)
        let fromStart = try await b.changes(since: "")
        XCTAssertTrue(fromStart.added.isEmpty, "a destroyed message is not listed again")
        await b.close()
    }

    func testAHubThatHidesAndDrops() async throws {
        let hub = MemoryHub()
        let a = await hub.connect(), b = await hub.connect()
        await hub.hide(channel: "secret", from: b)
        _ = try await a.append(channel: "secret", body: [1])
        let open = try await a.append(channel: "open", body: [2])
        let seenByB = try await b.changes(since: "").added.map(\.channel)
        XCTAssertEqual(seenByB, ["open"])
        let seenByA = try await a.changes(since: "").added.count
        XCTAssertEqual(seenByA, 2)
        await hub.drop([open])
        let afterDrop = try await b.changes(since: "").added.count
        XCTAssertEqual(afterDrop, 0)
        do { _ = try await b.changes(since: "99"); XCTFail("a cursor past the end") } catch {}
    }

    func testTheSessionPumpRunsOnIt() async throws {
        let hub = MemoryHub()
        let session = try Session(store: await hub.connect())
        try await session.start()
        let id = try await session.submit(channel: "c", body: [9])
        var updates = session.updates.makeAsyncIterator()
        var seen: [String] = []
        while !seen.contains(id), let batch = try await updates.next() { seen += batch.added.map(\.id) }
        XCTAssertTrue(seen.contains(id))
        await session.close()
    }
}
