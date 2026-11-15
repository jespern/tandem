import Foundation
import Tandem
@testable import TandemIMAP
import XCTest

func chunkEnvelope(_ n: Int) throws -> Bytes {
    var plain = (0..<n).map { _ in UInt8.random(in: 0...255) }
    plain.replaceSubrange(0..<14, with: Bytes("\u{0}\r\n.\r\nFrom =\t ".utf8))
    let r = (0..<32).map { _ in UInt8.random(in: 0...255) }
    let key = try Blob.chunkKey(r: r, plaintext: plain)
    return try Blob.seal(key: key, hub: Bytes(repeating: 0, count: 32), plaintext: plain)
}

func split(_ m: Bytes) -> (header: Bytes, body: Bytes) {
    let marker: Bytes = [0x0d, 0x0a, 0x0d, 0x0a]
    let i = (0...(m.count - 4)).first { Array(m[$0..<($0 + 4)]) == marker }!
    return (Array(m[..<(i + 2)]), Array(m[(i + 4)...]))
}

final class ChunkFormatTests: XCTestCase {
    func testBothForms() throws {
        let envelope = try chunkEnvelope(3000)
        for form in [ChunkFormat.Form.escaped, .base64] {
            let m = try ChunkFormat.build(channel: "abc", envelope: envelope, form: form, date: Date(timeIntervalSince1970: 1_794_735_000), id: "0123")
            let (header, body) = split(m)
            XCTAssertEqual(MessageFormat.field("Tandem-Blob", in: header), Bytes(B32.encode(Blob.address(envelope)).utf8))
            XCTAssertTrue(String(decoding: body, as: UTF8.self).components(separatedBy: "\r\n").allSatisfy { $0.utf8.count <= 998 })
            XCTAssertEqual(try ChunkFormat.decode(header: header, text: body), envelope)
        }
        let m = String(decoding: try ChunkFormat.build(channel: "abc", envelope: envelope, date: Date(timeIntervalSince1970: 1_794_735_000), id: "0123"), as: UTF8.self)
        XCTAssertTrue(m.hasPrefix("From: Tandem <tandem@tandem.airforce>\r\nDate: Sun, 15 Nov 2026 09:30:00 +0000\r\nMessage-ID: <0123@tandem.airforce>\r\n"
            + "Subject: tandem\r\nMIME-Version: 1.0\r\nTandem-Version: 2\r\nTandem-Channel: abc\r\nTandem-Blob: "), m)
        XCTAssertTrue(m.contains("\r\nTandem-Body: escaped\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n\r\n"))
    }

    /// A server may re-encode a body after the append and rewrite the MIME headers; Tandem-Body survives, and
    /// the transfer encoding is undone exactly once.
    func testReencodedByTheServer() throws {
        let envelope = try chunkEnvelope(2000)
        XCTAssertEqual(try ChunkFormat.decode(header: Bytes("Tandem-Body: escaped\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n".utf8),
                                              text: MessageFormat.wrapped(Blob.escape(envelope))), envelope)
        XCTAssertEqual(try ChunkFormat.decode(header: Bytes("Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n".utf8), text: envelope), envelope)
        XCTAssertEqual(try ChunkFormat.decode(header: Bytes("Content-Type: Application/Octet-Stream; name=x\r\ncontent-transfer-encoding: BASE64\r\n".utf8),
                                              text: MessageFormat.wrapped(envelope)), envelope)
        let feed = split(try MessageFormat.build(channel: "abc", envelope: envelope))
        XCTAssertThrowsError(try ChunkFormat.decode(header: feed.header, text: feed.body), "a text/plain body is not a chunk")
        XCTAssertThrowsError(try ChunkFormat.decode(header: Bytes("Tandem-Body: escaped\r\nContent-Type: application/octet-stream\r\n".utf8), text: Bytes("abc=".utf8)))
    }

    func testQuotaResponse() throws {
        guard case .untagged(.quota(let root, let resources)) = try Parser.response(Bytes("* QUOTA \"\" (STORAGE 88408 48828416)".utf8))
        else { return XCTFail("not a QUOTA response") }
        XCTAssertEqual(root, [])
        XCTAssertEqual(resources, [QuotaResource(name: "STORAGE", usage: 88408, limit: 48_828_416)])
    }
}

extension StalwartTests {
    func blobs(_ folder: String, options: BlobStore.Options? = nil) -> BlobStore {
        BlobStore(config("unused"), options: options ?? BlobStore.Options(folder: folder))
    }

    /// Append in both forms over several transfer connections, list the claims from headers alone, fetch the
    /// envelopes back byte for byte, destroy, release.
    func testBlobStore() async throws {
        let folder = folder() + "-blobs"
        let store = blobs(folder, options: BlobStore.Options(folder: folder, batchBytes: 64 << 10))
        var outgoing: [(channel: String, envelope: Bytes)] = []
        for i in 0..<12 { outgoing.append(("tag\(i % 3)", try chunkEnvelope(20000 + i))) }
        let ids = try await store.append(outgoing)
        XCTAssertEqual(ids.count, 12)
        let open = await store.connections
        XCTAssertEqual(open, 2, "Stalwart batches appends, so two transfer connections")
        let base64 = BlobStore(config("unused"), options: BlobStore.Options(folder: folder, form: .base64))
        let more = try await base64.append([("tag0", outgoing[0].envelope)])
        await base64.close()
        let changes = try await store.changes(since: "")
        XCTAssertEqual(changes.added.count, 13)
        var index = [more[0]: 0]
        for (i, id) in ids.enumerated() { index[id] = i }
        for ref in changes.added {
            let i = try XCTUnwrap(index[ref.id])
            XCTAssertEqual(ref.channel, outgoing[i].channel)
            XCTAssertEqual(ref.address, Blob.address(outgoing[i].envelope))
        }
        let all = ids + more
        let chunks = try await store.fetch(ids: all)
        XCTAssertEqual(chunks.map(\.id), all)
        for (i, c) in chunks.enumerated() { XCTAssertEqual(c.envelope, outgoing[i % 12].envelope) }
        try await store.destroy(ids: Array(ids.prefix(5)))
        let next = try await store.changes(since: changes.cursor)
        XCTAssertEqual(next.removed.count, 5)
        XCTAssertEqual(next.added.count, 0)
        let quota = try await store.quota()
        XCTAssertEqual(quota, .unknown, "Stalwart sets no quota")
        await store.release()
        let closed = await store.connections
        XCTAssertEqual(closed, 0)
        let again = try await store.fetch(ids: more)
        XCTAssertEqual(again.count, 1)
        await store.close()
    }

    /// The feed's connection appends to a blob folder that doesn't exist yet: TRYCREATE, CREATE, again.
    func testAppendChunksOnTheFeedConnection() async throws {
        let feed = IMAPStore(config(folder()))
        try await feed.ensureFolder()
        let folder = folder() + "-blobs"
        let envelope = try chunkEnvelope(5000)
        let ids = try await feed.appendChunks([("tag", envelope)], folder: folder)
        XCTAssertEqual(ids.count, 1)
        XCTAssertNotEqual(ids.first, "")
        let store = blobs(folder)
        let got = try await store.fetch(ids: ids)
        XCTAssertEqual(got.map(\.envelope), [envelope])
        let open = await store.connections
        XCTAssertEqual(open, 1, "one fetch command needs one connection")
        await store.close()
        await feed.close()
    }
}
