import Foundation

// The blob folder as the engine sees it (BLOBS.md section 4.3), beside the feed's PushStore. A binding lists the
// chunks' claimed addresses from their headers alone, fetches chunk envelopes, appends chunks, destroys them and reads
// the quota. Nothing here idles: a device asks.

/// How a chunk's body is carried (BLOBS.md section 4.2).
public enum ChunkForm: Sendable, Equatable {
    /// 8-bit content with a few bytes escaped, 2% larger than the envelope. Writers use it.
    case escaped
    /// For a server seen to damage the escaped form; 37% larger.
    case base64
}

/// A message in the blob folder as its headers claim it: the claims are unverified.
public struct BlobRef: Sendable, Equatable {
    public var id: String, channel: String, address: Bytes
    public init(id: String, channel: String, address: Bytes) { self.id = id; self.channel = channel; self.address = address }
}

/// One page of what changed in the blob folder.
public struct BlobChanges: Sendable, Equatable {
    public var added: [BlobRef], removed: [String], cursor: String, more: Bool
    public init(added: [BlobRef], removed: [String], cursor: String, more: Bool = false) {
        self.added = added; self.removed = removed; self.cursor = cursor; self.more = more
    }
}

/// A fetched chunk message, decoded but not verified against its address.
public struct Chunk: Sendable, Equatable {
    public var id: String, envelope: Bytes
    public init(id: String, envelope: Bytes) { self.id = id; self.envelope = envelope }
}

/// The blob folder's storage quota (RFC 9208): unknown where the server reports none, never zero.
public enum Quota: Sendable, Equatable {
    case unknown
    case known(used: Int64, limit: Int64)
}

/// The blob folder of one hub.
public protocol BlobHub: Sendable {
    /// One page of what changed since `cursor` ("" for the start), from headers alone.
    func changes(since cursor: String) async throws -> BlobChanges
    /// Stores chunks in the given form; IDs in order, "" where the server named none. Not retried: an append whose
    /// answer was lost may have landed.
    func append(_ chunks: [(channel: String, envelope: Bytes)], form: ChunkForm) async throws -> [String]
    /// Reads chunks by ID, in the order of `ids`, leaving out what is gone or doesn't decode.
    func fetch(ids: [String]) async throws -> [Chunk]
    /// Removes chunk messages for good.
    func destroy(ids: [String]) async throws
    /// The storage quota.
    func quota() async throws -> Quota
    /// Closes the connections the transfers used; the next call opens them again.
    func release() async
}

/// Orders blob message IDs the way every device does (BLOBS.md section 4.3): by UIDVALIDITY, then UID, for
/// "<validity>.<uid>"; plain numbers by value; anything else as text.
public func blobIDLess(_ a: String, _ b: String) -> Bool {
    func parse(_ s: String) -> (UInt64, UInt64)? {
        let parts = s.split(separator: ".", omittingEmptySubsequences: false)
        if parts.count == 1, let u = UInt64(parts[0]) { return (0, u) }
        if parts.count == 2, let v = UInt64(parts[0]), let u = UInt64(parts[1]) { return (v, u) }
        return nil
    }
    if let x = parse(a), let y = parse(b) { return x.0 < y.0 || x.0 == y.0 && x.1 < y.1 }
    return a < b
}
