import CryptoKit
import Foundation

/// Blobs (DESIGN.md §17): chunk encryption, manifests and the escaped body form. No I/O.
public enum Blob {
    public static let kind: UInt8 = 4
    /// Plaintext bytes in one chunk (DESIGN.md §17.4.1).
    public static let maxChunk = 4 * 1024 * 1024
    /// What a writer cuts files into.
    public static let defaultChunk = 1024 * 1024
    /// Content up to this size goes in the manifest itself.
    public static let defaultInline = 16 * 1024
    static let entry = 68, line = 490
    static let magic = Bytes("TNDM".utf8) + [2, kind]
    static let zeroNonce = Bytes(repeating: 0, count: 12)

    public struct ChunkEntry: Equatable, Sendable {
        public var address: Bytes, key: Bytes, length: Int
        public init(address: Bytes, key: Bytes, length: Int) { self.address = address; self.key = key; self.length = length }
    }

    /// A blob's description: its chunks, or its bytes inline in `data`.
    public struct Manifest: Equatable, Sendable {
        public var size: Int, hash: Bytes, chunks: [ChunkEntry]?, data: Bytes?
        public init(size: Int, hash: Bytes, chunks: [ChunkEntry]? = nil, data: Bytes? = nil) {
            self.size = size; self.hash = hash; self.chunks = chunks; self.data = data
        }
    }

    /// How a `_blob:` field's value reads (§17.5). `unknown` is a JSON object whose `blob` is an
    /// integer other than 1: unreadable, and it stops clean-up (§17.7).
    public enum Parsed: Equatable, Sendable {
        case manifest(Manifest)
        case unknown(version: Int64)
        case none
    }

    /// The writer's key for one chunk: never the same for two contents (§17.4.1).
    public static func chunkKey(r: Bytes, plaintext: Bytes) throws -> Bytes {
        guard r.count == 32 else { throw TandemError.invalid("blob: r must be 32 bytes") }
        return KDF(r, salt: H(plaintext), info: "tandem/v2/blob-chunk")
    }

    static func aad(hub: Bytes) -> Bytes { magic + hub }

    /// The chunk's envelope. Deterministic: the same key and plaintext give the same bytes.
    public static func seal(key: Bytes, hub: Bytes, plaintext: Bytes) throws -> Bytes {
        guard key.count == 32, hub.count == 32, (1...maxChunk).contains(plaintext.count) else { throw TandemError.invalid("blob: invalid chunk") }
        let box = try ChaChaPoly.seal(plaintext, using: SymmetricKey(data: key), nonce: ChaChaPoly.Nonce(data: zeroNonce), authenticating: aad(hub: hub))
        return magic + Bytes(box.ciphertext) + Bytes(box.tag)
    }

    /// The plaintext, which must be exactly `length` bytes (§17.5).
    public static func open(key: Bytes, hub: Bytes, envelope: Bytes, length: Int) throws -> Bytes {
        guard key.count == 32, hub.count == 32, envelope.count >= 6 + 16 + 1, Array(envelope.prefix(6)) == magic
        else { throw TandemError.invalid("blob: not a chunk envelope") }
        let plaintext: Bytes
        do {
            let box = try ChaChaPoly.SealedBox(nonce: ChaChaPoly.Nonce(data: zeroNonce), ciphertext: envelope[6..<(envelope.count - 16)], tag: envelope.suffix(16))
            plaintext = Bytes(try ChaChaPoly.open(box, using: SymmetricKey(data: key), authenticating: aad(hub: hub)))
        } catch { throw TandemError.invalid("blob: chunk authentication failed") }
        guard plaintext.count == length else { throw TandemError.invalid("blob: chunk length differs from its manifest entry") }
        return plaintext
    }

    /// H(envelope): the chunk's identity on every device.
    public static func address(_ envelope: Bytes) -> Bytes { H(envelope) }

    /// The canonical writer form: no spaces, keys blob, size, hash, then chunks or data.
    public static func encode(_ m: Manifest) throws -> Bytes {
        guard (m.chunks == nil) != (m.data == nil), m.hash.count == 32, m.size >= 0 else { throw TandemError.invalid("blob: invalid manifest") }
        let out = JSONWriter.object { w in
            w.member("blob", int: 1)
            w.member("size", int: UInt64(m.size))
            w.member("hash", b64u: m.hash)
            if let chunks = m.chunks {
                var raw = Bytes()
                raw.reserveCapacity(entry * chunks.count)
                for e in chunks { raw += e.address + e.key; raw.u32(UInt32(e.length)) }
                w.member("chunks", b64u: raw)
            } else {
                w.member("data", b64u: m.data!)
            }
        }
        guard case .manifest = parse(out) else { throw TandemError.invalid("blob: invalid manifest") }
        return out
    }

    public static func parse(_ value: Bytes) -> Parsed {
        guard let doc = try? JSON.parse(value), case .object = doc, case let .int(version)? = doc["blob"] else { return .none }
        guard version == 1 else { return .unknown(version: version) }
        guard case let .int(size)? = doc["size"], size >= 0, size <= 1 << 53,
              case let .string(hashText)? = doc["hash"], let hash = B64u.decode(hashText), hash.count == 32 else { return .none }
        let chunks = doc["chunks"], data = doc["data"]
        guard (chunks == nil) != (data == nil), case let .string(text)? = chunks ?? data, let raw = B64u.decode(text) else { return .none }
        if data != nil { return raw.count == Int(size) ? .manifest(Manifest(size: Int(size), hash: hash, data: raw)) : .none }
        guard !raw.isEmpty, raw.count % entry == 0 else { return .none }
        var entries = [ChunkEntry](), total = 0
        entries.reserveCapacity(raw.count / entry)
        for i in stride(from: 0, to: raw.count, by: entry) {
            let n = Int(raw[i + 64]) << 24 | Int(raw[i + 65]) << 16 | Int(raw[i + 66]) << 8 | Int(raw[i + 67])
            guard (1...maxChunk).contains(n) else { return .none }
            total += n
            entries.append(ChunkEntry(address: Array(raw[i..<(i + 32)]), key: Array(raw[(i + 32)..<(i + 64)]), length: n))
        }
        return total == Int(size) ? .manifest(Manifest(size: Int(size), hash: hash, chunks: entries)) : .none
    }

    /// The manifest of `content` and its envelopes in order (§17.6, step 2), all in memory.
    public static func describe(r: Bytes, hub: Bytes, content: Bytes, chunkSize: Int = defaultChunk, inline: Int = defaultInline) throws -> (manifest: Manifest, envelopes: [Bytes]) {
        guard (1...maxChunk).contains(chunkSize) else { throw TandemError.invalid("blob: invalid chunk size") }
        if content.count <= inline { return (Manifest(size: content.count, hash: H(content), data: content), []) }
        var entries = [ChunkEntry](), envelopes = [Bytes]()
        for start in stride(from: 0, to: content.count, by: chunkSize) {
            let piece = Array(content[start..<min(start + chunkSize, content.count)])
            let key = try chunkKey(r: r, plaintext: piece)
            let envelope = try seal(key: key, hub: hub, plaintext: piece)
            entries.append(ChunkEntry(address: address(envelope), key: key, length: piece.count))
            envelopes.append(envelope)
        }
        return (Manifest(size: content.count, hash: H(content), chunks: entries), envelopes)
    }

    static func anywhere(_ b: UInt8) -> Bool { b == 0x00 || b == 0x0A || b == 0x0D || b == 0x3D }
    static func atStart(_ b: UInt8) -> Bool { anywhere(b) || b == 0x09 || b == 0x20 || b == 0x2E || b == 0x3E || b == 0x46 }
    static func atEnd(_ b: UInt8) -> Bool { anywhere(b) || b == 0x09 || b == 0x20 }

    /// The escaped body form of §17.4.2: lines of at most 490 envelope bytes, CRLF after each.
    public static func escape(_ envelope: Bytes) -> Bytes {
        var out = Bytes()
        out.reserveCapacity(envelope.count + envelope.count / 32 + 2 * (envelope.count / line + 1))
        var start = 0
        while start < envelope.count {
            let end = min(start + line, envelope.count)
            for j in start..<end {
                let b = envelope[j]
                let escaped = j == start ? atStart(b) : j == end - 1 ? atEnd(b) : anywhere(b)
                if escaped { out.append(0x3D); out.append(b &+ 64) } else { out.append(b) }
            }
            out.append(0x0D); out.append(0x0A)
            start = end
        }
        return out
    }

    /// Remove every CR and LF, then each '=' and the byte after it become that byte minus 64.
    public static func unescape(_ body: some Collection<UInt8>) throws -> Bytes {
        var out = Bytes()
        out.reserveCapacity(body.count)
        var pending = false
        for b in body where b != 0x0D && b != 0x0A {
            if pending { out.append(b &- 64); pending = false } else if b == 0x3D { pending = true } else { out.append(b) }
        }
        guard !pending else { throw TandemError.invalid("blob: escaped body ends in '='") }
        return out
    }
}
