import XCTest
import Foundation
import Tandem
@testable import TandemEngine

final class StoreTests: XCTestCase {
    func path() -> String {
        FileManager.default.temporaryDirectory.appendingPathComponent("tandem-store-\(UUID().uuidString).sqlite").path
    }

    func testValuesObjectsMessagesWritesJournal() throws {
        let file = path()
        defer { for suffix in ["", "-wal", "-shm"] { try? FileManager.default.removeItem(atPath: file + suffix) } }
        let manager = try DeviceKeys.manager(seed: Bytes(repeating: 1, count: 32))
        var revocation = ControlObject(kind: "revocation", fields: ["hub": .id(Bytes(manager.signing.publicKey.rawRepresentation)),
            "genesis": .id(Bytes(repeating: 2, count: 32)), "target": .id(Bytes(repeating: 3, count: 32)), "retained": .set([])])
        try revocation.sign("sig", with: manager.signing)
        do {
            let s = try Store(path: file)
            try s.transaction {
                try s.setValue("cursor", Bytes("12".utf8))
                XCTAssertTrue(try s.putObject(revocation))
                XCTAssertFalse(try s.putObject(revocation))
                try s.noteMessage(id: "7", channel: "abc", hash: [9])
                XCTAssertTrue(try s.putWrite(id: [1], scope: [2], record: [3], epoch: [4], payload: [5]))
                try s.setJournal("rotate:x", [6, 7])
            }
            XCTAssertThrowsError(try s.transaction { try s.setValue("cursor", [0]); throw TandemError.invalid("crash") })
        }
        // Reopened: what was committed is there, the rolled-back change is not.
        let s = try Store(path: file)
        XCTAssertEqual(try s.value("cursor"), Bytes("12".utf8))
        XCTAssertEqual(try s.objects(kind: "revocation"), [revocation])
        XCTAssertEqual(try s.messages(withHash: [9]), ["7"])
        XCTAssertEqual(try s.unapplied().map(\.id), ["7"])
        try s.markApplied("7")
        XCTAssertTrue(try s.unapplied().isEmpty)
        XCTAssertEqual(try s.writes(scope: [2], record: [3]).map(\.payload), [[5]])
        XCTAssertEqual(try s.records(scope: [2]), [[3]])
        XCTAssertEqual(try s.journal("rotate:x"), [6, 7])
        XCTAssertEqual(try s.journalSteps(), ["rotate:x"])
        try s.clearJournal("rotate:x")
        XCTAssertNil(try s.journal("rotate:x"))
    }
}
