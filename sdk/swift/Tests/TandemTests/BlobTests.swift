import XCTest
import Foundation
import Tandem

final class BlobTests: XCTestCase {
    func fixture() throws -> [String: Any] {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        return try JSONSerialization.jsonObject(with: Data(contentsOf: root.appendingPathComponent("vectors/v2/blobs.json"))) as! [String: Any]
    }
    func hex(_ value: Any?) -> Bytes { Bytes(hex: value as! String)! }

    func testChunk() throws {
        let v = try fixture(), c = (v["chunk"] as! [String: String]).mapValues { Bytes(hex: $0)! }
        XCTAssertEqual(v["max_chunk"] as? Int, Blob.maxChunk)
        let key = try Blob.chunkKey(r: c["r"]!, plaintext: c["plaintext"]!)
        let envelope = try Blob.seal(key: key, hub: c["hub"]!, plaintext: c["plaintext"]!)
        XCTAssertEqual(key, c["key"]); XCTAssertEqual(envelope, c["envelope"]); XCTAssertEqual(Blob.address(envelope), c["address"])
        XCTAssertEqual(Blob.escape(envelope), c["escaped"])
        XCTAssertEqual(try Blob.open(key: key, hub: c["hub"]!, envelope: envelope, length: c["plaintext"]!.count), c["plaintext"])
        for f in v["open_fails"] as! [[String: Any]] {
            XCTAssertThrowsError(try Blob.open(key: key, hub: c["hub"]!, envelope: hex(f["envelope"]), length: f["length"] as! Int), f["name"] as! String)
        }
        XCTAssertThrowsError(try Blob.seal(key: key, hub: c["hub"]!, plaintext: []))
        XCTAssertThrowsError(try Blob.seal(key: key, hub: c["hub"]!, plaintext: Bytes(repeating: 0, count: Blob.maxChunk + 1)))
    }

    func testDescribeAndManifests() throws {
        let v = try fixture(), c = (v["chunk"] as! [String: String]).mapValues { Bytes(hex: $0)! }
        for b in v["blobs"] as! [[String: Any]] {
            let name = b["name"] as! String, content = hex(b["content"])
            let (manifest, envelopes) = try Blob.describe(r: c["r"]!, hub: c["hub"]!, content: content, chunkSize: b["chunk_size"] as! Int, inline: b["inline"] as! Int)
            XCTAssertEqual(String(decoding: try Blob.encode(manifest), as: UTF8.self), b["manifest"] as! String, name)
            XCTAssertEqual(envelopes.map(\.hex), b["envelopes"] as! [String], name)
            guard case let .manifest(m) = Blob.parse(Bytes((b["manifest"] as! String).utf8)) else { return XCTFail(name) }
            XCTAssertEqual(m, manifest, name)
            let opened = try (m.chunks ?? []).enumerated().flatMap { i, e in try Blob.open(key: e.key, hub: c["hub"]!, envelope: envelopes[i], length: e.length) }
            XCTAssertEqual(m.chunks == nil ? m.data! : opened, content, name)
        }
        for m in v["manifests"] as! [[String: String]] {
            let got: String = switch Blob.parse(Bytes(m["value"]!.utf8)) {
            case .manifest: "manifest"
            case .unknown: "unknown"
            case .none: "none"
            }
            XCTAssertEqual(got, m["result"], m["name"]!)
        }
    }

    func testEscapes() throws {
        let v = try fixture()
        for e in v["escapes"] as! [[String: String]] {
            XCTAssertEqual(Blob.escape(hex(e["input"])), hex(e["escaped"]))
            XCTAssertEqual(try Blob.unescape(hex(e["escaped"])), hex(e["input"]))
        }
        for e in v["unescape_lenient"] as! [[String: String]] { XCTAssertEqual(try Blob.unescape(hex(e["body"])), hex(e["envelope"])) }
        for body in v["unescape_refused"] as! [String] { XCTAssertThrowsError(try Blob.unescape(hex(body))) }
    }
}
