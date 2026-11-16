import CryptoKit
import Foundation
import Tandem

// Blobs (DESIGN.md §17): files as immutable encrypted chunks in the hub's blob folder, pointed at by a manifest in a record
// field named _blob:<name>. A file attached here is a pending write (§17.6): signed, kept in the upload journal and
// out of the replica until every chunk is on the hub, then merged and published like any write. Transfers run on the
// blob hub's own connections in a worker that starts when there is work (an attach, a sync, `useBlobs`) and stops when
// there is none; nothing waits on a timer. File reading, sealing and hashing run off the actor.

/// What this device can do with a blob now (§17.8).
public enum BlobState: Sendable, Hashable {
    /// Inline, or downloaded and verified; `path` is the file.
    case here
    /// On the hub, not fetched.
    case remote
    /// Being fetched; `done` of `chunks` are here.
    case downloading
    /// At the last look a chunk was not on the hub, or only junk claiming it was.
    case unavailable
    /// Not a manifest this device reads, or its chunks don't match it.
    case unreadable
    /// Attached on this device and going up; `path` is the staged copy.
    case uploading
}

/// A file attached to a record, as this device sees it.
public struct BlobInfo: Sendable, Equatable {
    public var name: String, state: BlobState, size: Int, hash: Bytes?, path: URL?, chunks: Int, done: Int
}

/// The per-hub settings of DESIGN.md §17, changeable at runtime.
public struct BlobSettings: Sendable, Equatable {
    /// How a file is cut; at most 4 MiB (BLOBS.md has the measurements).
    public var chunkSize: Int
    /// The largest content kept in the manifest itself.
    public var inline: Int
    /// How long this device must have known an orphan chunk before it collects it.
    public var orphanGrace: Duration
    /// Content-cache budget; oldest content goes first. The file just fetched
    /// is retained even if it alone exceeds the budget.
    public var storeLimit: Int
    /// The largest file fetched without being asked, in lists set to keep files.
    public var unasked: Int

    public init(chunkSize: Int = Blob.defaultChunk, inline: Int = Blob.defaultInline, orphanGrace: Duration = .seconds(7 * 24 * 3600),
                storeLimit: Int = 2 << 30, unasked: Int = 25 << 20) {
        self.chunkSize = min(max(1, chunkSize), Blob.maxChunk); self.inline = max(0, inline); self.orphanGrace = orphanGrace
        self.storeLimit = storeLimit; self.unasked = unasked
    }
}

public enum BlobError: Error, Equatable {
    /// A chunk of the blob is not on the hub (§17.8); a device that holds the file restores it.
    case unavailable
    /// The field holds no manifest this device reads, or its chunks don't match it.
    case unreadable
    /// `useBlobs` has not been called.
    case noBlobHub
    case noSuchFile
}

/// The blob worker's state, isolated to the device.
final class BlobRuntime {
    let hub: any BlobHub
    let directory: URL
    var settings: BlobSettings
    let kicker: AsyncStream<Void>.Continuation
    var kicked = false
    var settledWaiters: [CheckedContinuation<Void, Never>] = []
    var downloads: [Bytes: Download] = [:]          // by content hash
    var unavailable: Set<Bytes> = []                // manifest write IDs whose last fetch found a chunk missing
    var hygiene = false                             // a sync asked for a clean-up pass
    var healed = false                              // the healing check ran since useBlobs
    var view: Bytes?                                // scope view when clean-up last refreshed the index
    var users = 0                                   // transfers using the hub's connections now: the worker, downloads, quota

    init(hub: any BlobHub, directory: URL, settings: BlobSettings, kicker: AsyncStream<Void>.Continuation) {
        self.hub = hub; self.directory = directory; self.settings = settings; self.kicker = kicker
    }

    final class Download {
        let chunks: Int
        let layout: Bytes
        var done = 0
        var waiters: [CheckedContinuation<Void, Never>] = []
        var error: (any Error)?
        var finished = false
        init(chunks: Int, layout: Bytes) { self.chunks = chunks; self.layout = layout }
    }

    var staged: URL { directory.appendingPathComponent("staged") }
    var partial: URL { directory.appendingPathComponent("partial") }
    func content(_ hash: Bytes) -> URL { directory.appendingPathComponent(hash.hex) }
}

extension Device {
    /// Every field that holds a manifest starts with this.
    public static let blobPrefix = Bytes("_blob:".utf8)

    static func isBlobField(_ f: Bytes) -> Bool { f.starts(with: blobPrefix) }

    /// Turns blobs on: `hub` is the blob folder of this device's hub. Pending uploads resume.
    public func useBlobs(_ hub: any BlobHub, settings: BlobSettings = BlobSettings()) throws {
        let dir = store.blobDirectory
        for sub in ["staged", "partial"] {
            try FileManager.default.createDirectory(at: dir.appendingPathComponent(sub), withIntermediateDirectories: true)
        }
        let (kicks, kicker) = AsyncStream<Void>.makeStream(bufferingPolicy: .bufferingNewest(1))
        blobs = BlobRuntime(hub: hub, directory: dir, settings: settings, kicker: kicker)
        Task { [weak self] in
            for await _ in kicks {
                guard let device = self else { return }
                await device.blobRounds()
            }
        }
        wakeBlobs()
    }

    /// Tests put a misbehaving folder in place.
    func replaceBlobHub(_ hub: any BlobHub) {
        guard let b = blobs else { return }
        let r = BlobRuntime(hub: hub, directory: b.directory, settings: b.settings, kicker: b.kicker)
        r.healed = b.healed; r.view = b.view
        blobs = r
    }

    /// Changes the blob settings at runtime.
    public func setBlobSettings(_ settings: BlobSettings) { blobs?.settings = settings }

    func wakeBlobs() {
        guard let b = blobs else { return }
        b.kicked = true
        b.kicker.yield()
    }

    /// Wakes the blob worker and returns once it has run a whole round after now: for tests, and for an app that
    /// wants to know its uploads are through.
    public func settleBlobs() async {
        guard blobs != nil else { return }
        wakeBlobs()
        await withCheckedContinuation { blobs!.settledWaiters.append($0) }
    }

    /// Makes the next clean-up pass check held content against the hub again, as after a restart.
    func forgetHealing() { blobs?.healed = false }

    /// Makes the next clean-up pass check that the chunks of files this device holds are still on the hub, and restore
    /// those that aren't, as after a restart (§17.8). An app may call it when it comes to the foreground.
    public func checkBlobs() {
        guard let b = blobs else { return }
        b.healed = false
        b.hygiene = true
        wakeBlobs()
    }

    /// Asks the worker for a clean-up and healing pass; after every sync.
    func afterSync() throws {
        try discardInvalidUploads()
        guard let b = blobs else { return }
        b.hygiene = true
        wakeBlobs()
    }

    /// Cancel when revocation is learned, before waiting for chunks or an
    /// unavailable key. Later re-admission cannot revive the pending write.
    func discardInvalidUploads() throws {
        for u in try store.uploads() {
            guard let me, let c = collections[u.scope], !c.dead, c.records[u.record]?.deleted != true,
                  !recordMarked(c, id: u.record) else {
                try dropUpload(u); continue
            }
            if case .invalid = registry.activity(me.certificate) { try dropUpload(u); continue }
            guard let head = c.head, let st = c.statements[head]?.statement else { continue }
            if st.deleted || !st.members.contains(where: { $0.uid == me.participant && $0.role != .reader }) {
                try dropUpload(u)
            }
        }
    }

    /// Notes a transfer starting on the hub's connections; `doneUsing` ends it, and the last to end lets them go.
    func useBlobHub(_ b: BlobRuntime) { b.users += 1 }

    func doneUsing(_ b: BlobRuntime) async {
        b.users -= 1
        if b.users == 0 { await b.hub.release() }
    }

    func blobRounds() async {
        guard let b = blobs else { return }
        b.kicked = false
        useBlobHub(b)
        while true {
            do {
                if try await !blobRound(b) { break }
            } catch {
                changed(.syncFailed("\(error)")); flushEvents()
                break
            }
        }
        await doneUsing(b)
        if !b.kicked {
            let waiters = b.settledWaiters
            b.settledWaiters.removeAll()
            for w in waiters { w.resume() }
        }
    }

    /// One round of work; whether there was any that calls for another.
    func blobRound(_ b: BlobRuntime) async throws -> Bool {
        try discardInvalidUploads()
        let ups = try store.uploads()
        if !ups.isEmpty {
            try await uploadAll(b, ups)
            return try store.uploads().count < ups.count
        }
        guard b.hygiene || !b.healed else { return false }
        b.hygiene = false
        try await blobHygiene(b)
        try await keepFiles(b)
        return false
    }

    // MARK: Attaching (§17.6)

    /// Attaches a copy of the file at `path` to a field of a record, as _blob:<name>. Content up to the inline size is
    /// written at once; anything larger is pending until its chunks are on the hub, and shows as `.uploading` meanwhile.
    /// It works offline.
    public func attachFile(list cid: Bytes, record: Bytes, name: String, path: URL) async throws {
        guard let b = blobs else { throw BlobError.noBlobHub }
        let staged = b.staged.appendingPathComponent("new-" + UUID().uuidString)
        try FileManager.default.copyItem(at: path, to: staged)
        try await attachStaged(b, list: cid, record: record, name: name, staged: staged)
    }

    /// Attaches content held in memory.
    public func attach(list cid: Bytes, record: Bytes, name: String, content: Bytes) async throws {
        guard let b = blobs else { throw BlobError.noBlobHub }
        let staged = b.staged.appendingPathComponent("new-" + UUID().uuidString)
        try Data(content).write(to: staged)
        try await attachStaged(b, list: cid, record: record, name: name, staged: staged)
    }

    func attachStaged(_ b: BlobRuntime, list cid: Bytes, record: Bytes, name: String, staged: URL) async throws {
        var keep = false
        defer { if !keep { try? FileManager.default.removeItem(at: staged) } }
        guard !name.isEmpty else { throw TandemError.invalid("blob: a name is needed") }
        let field = Self.blobPrefix + Bytes(name.utf8)
        let settings = b.settings
        let size = (try FileManager.default.attributesOfItem(atPath: staged.path)[.size] as? NSNumber)?.intValue ?? 0
        if size <= settings.inline {
            let content = Bytes(try Data(contentsOf: staged))
            let value = try Blob.encode(Blob.Manifest(size: content.count, hash: H(content), data: content))
            try dropPending(list: cid, record: record, field: field)
            try await writeRecord(cid, record: record, field: field, op: .assign, value: value)
            return
        }
        // Describe: one pass, off the actor, sealing every chunk to learn its address.
        let r = Self.random(32)
        let manifest = try await Self.describe(file: staged, r: r, hub: pin.hub, chunkSize: settings.chunkSize)
        let value = try Blob.encode(manifest)
        try takeExternalChanges()
        let (c, me, head, epoch) = try writable(cid, record: record)
        // Sign: with the context it would have if it were published now; not merged (§17.6, step 3).
        let w = try signRecord(c, me: me, record: record, field: field, op: .assign, value: value, element: nil, head: head, epoch: epoch, after: [])
        let id = try w.id()
        let final = b.staged.appendingPathComponent(id.hex)
        try FileManager.default.moveItem(at: staged, to: final)
        keep = true
        try dropPending(list: cid, record: record, field: field)
        try store.putUpload(Store.Upload(write: id, scope: cid, record: record, field: String(decoding: field, as: UTF8.self), epoch: epoch,
                                         payload: try recordPayload(record: record, writes: [w]), r: r, staged: final.lastPathComponent))
        changed(.blobs(list: cid, records: [record]))
        flushEvents()
        wakeBlobs()
    }

    nonisolated static func describe(file: URL, r: Bytes, hub: Bytes, chunkSize: Int) async throws -> Blob.Manifest {
        let handle = try FileHandle(forReadingFrom: file)
        defer { try? handle.close() }
        var sha = SHA256(), entries: [Blob.ChunkEntry] = [], size = 0
        while let data = try handle.read(upToCount: chunkSize), !data.isEmpty {
            let piece = Bytes(data)
            sha.update(data: piece)
            let key = try Blob.chunkKey(r: r, plaintext: piece)
            let envelope = try Blob.seal(key: key, hub: hub, plaintext: piece)
            entries.append(Blob.ChunkEntry(address: Blob.address(envelope), key: key, length: piece.count))
            size += piece.count
        }
        return Blob.Manifest(size: size, hash: Bytes(sha.finalize()), chunks: entries)
    }

    /// Checks that this device may write the record now, and returns where a write goes.
    func writable(_ cid: Bytes, record: Bytes) throws -> (CollectionState, Me, ID, ID) {
        guard let me, let c = collections[cid], !c.dead, let head = c.head, let epoch = c.epoch(of: head), c.keys[epoch] != nil,
              c.statements[head]?.statement.deleted == false
        else { throw TandemError.invalid("device: no such list, no key for its current epoch yet, or it can't sync or was deleted") }
        guard let member = c.statements[head]?.statement.members.first(where: { $0.uid == me.participant }),
              member.role != .reader, record != Self.meta || member.role == .owner
        else { throw TandemError.invalid("device: this participant may not write here") }
        guard exposure(cid).isEmpty else { throw TandemError.invalid("device: waiting for the list's owner to change its key") }
        guard inventory(epoch)?.marks(record) != true else { throw TandemError.invalid("device: the record was deleted") }
        return (c, me, head, epoch)
    }

    /// Forgets a pending write of the same field. Its chunks already up are collected in the next clean-up pass.
    func dropPending(list cid: Bytes, record: Bytes, field: Bytes) throws {
        for u in try store.uploads() where u.scope == cid && u.record == record && Bytes(u.field.utf8) == field { try dropUpload(u) }
    }

    func dropUpload(_ u: Store.Upload) throws {
        try store.removeUpload(u.write)
        try store.setValue("blobs.dropped." + u.scope.hex + "." + u.write.hex, u.payload)
        if let b = blobs { try? FileManager.default.removeItem(at: b.staged.appendingPathComponent(u.staged)) }
        changed(.blobs(list: u.scope, records: [u.record]))
    }

    /// Drops a pending attachment of the field: its write is never published.
    public func cancelAttach(list cid: Bytes, record: Bytes, name: String) throws {
        try dropPending(list: cid, record: record, field: Self.blobPrefix + Bytes(name.utf8))
        flushEvents()
    }

    func uploadOf(_ u: Store.Upload) -> (Write, Blob.Manifest)? {
        guard case let .record(state)? = try? Kind1.parse(u.payload, hub: pin.hub, scope: u.scope), let w = state.writes.first,
              case let .manifest(m) = Blob.parse(w.value), m.chunks != nil else { return nil }
        return (w, m)
    }

    /// Steps 4 and 5 of §17.6 for every pending write: append what the hub lacks, then commit the writes whose
    /// chunks are all on it.
    func uploadAll(_ b: BlobRuntime, _ ups: [Store.Upload]) async throws {
        try await refreshIndex(b)
        var appended: [String: Bytes] = [:]
        for u in ups {
            guard let (_, m) = uploadOf(u) else { try dropUpload(u); flushEvents(); continue }
            let present = try await settle(b, addresses(m))
            let (missing, offsets) = Self.missing(m, present)
            guard let tag = channel(of: u.scope) else { continue }
            try await appendChunks(b, file: b.staged.appendingPathComponent(u.staged), tag: tag, missing, offsets, appended: &appended)
        }
        try await refreshIndex(b)
        for (id, address) in appended where try store.chunks(address: address).contains(where: { $0.id == id }) {
            try store.setChunkState(id, .verified)
        }
        for u in ups {
            guard let (_, m) = uploadOf(u) else { continue }
            let present = try await settle(b, addresses(m))
            if m.chunks!.allSatisfy({ present[$0.address] != nil }) { try await commit(b, u) }
        }
    }

    func addresses(_ m: Blob.Manifest) -> [Bytes] { (m.chunks ?? []).map(\.address) }

    /// The entries whose addresses aren't on the hub, once each, with their offsets.
    static func missing(_ m: Blob.Manifest, _ present: [Bytes: String]) -> ([Blob.ChunkEntry], [Int]) {
        var out: [Blob.ChunkEntry] = [], offsets: [Int] = [], offset = 0, seen: Set<Bytes> = []
        for e in m.chunks ?? [] {
            if present[e.address] == nil, !seen.contains(e.address) { out.append(e); offsets.append(offset); seen.insert(e.address) }
            offset += e.length
        }
        return (out, offsets)
    }

    /// The tag of a list's current channel.
    func channel(of cid: Bytes) -> String? {
        guard let c = collections[cid], let head = c.head, let epoch = c.epoch(of: head), let key = c.keys[epoch] else { return nil }
        return Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
    }

    nonisolated static func seal(file: URL, entries: [Blob.ChunkEntry], offsets: [Int], hub: Bytes) async throws -> [Bytes] {
        let handle = try FileHandle(forReadingFrom: file)
        defer { try? handle.close() }
        return try zip(entries, offsets).map { e, offset in
            try handle.seek(toOffset: UInt64(offset))
            let piece = Bytes(try handle.read(upToCount: e.length) ?? Data())
            let envelope = try Blob.seal(key: e.key, hub: hub, plaintext: piece)
            guard Blob.address(envelope) == e.address else { throw TandemError.invalid("blob: the local copy does not seal to its manifest") }
            return envelope
        }
    }

    /// Seals chunks again from a file holding the content and appends them a few at a time, in the hub's form. The
    /// first escaped chunk this device appends on the hub is fetched back and compared (§17.4.2).
    func appendChunks(_ b: BlobRuntime, file: URL, tag: String, _ entries: [Blob.ChunkEntry], _ offsets: [Int],
                      appended: inout [String: Bytes]) async throws {
        for start in stride(from: 0, to: entries.count, by: 4) {
            let end = min(start + 4, entries.count)
            let envelopes = try await Self.seal(file: file, entries: Array(entries[start..<end]), offsets: Array(offsets[start..<end]), hub: pin.hub)
            let out = envelopes.map { (channel: tag, envelope: $0) }
            let form = try blobForm()
            var ids = try await b.hub.append(out, form: form)
            if form == .escaped, let first = ids.first, !first.isEmpty, try await checkForm(b, id: first, sent: envelopes[0]) {
                // None of this batch can be taken as verified: send it all again, in base64.
                for id in ids where !id.isEmpty { try store.setChunkState(id, .rejected) }
                ids = try await b.hub.append(out, form: .base64)
            }
            for (k, id) in ids.enumerated() where !id.isEmpty { appended[id] = Blob.address(envelopes[k]) }
        }
    }

    func blobForm() throws -> ChunkForm { try store.value("blobs.form") == Bytes("base64".utf8) ? .base64 : .escaped }

    /// Fetches the first escaped chunk this device appends on the hub back, once; whether the server changed it. If so
    /// the hub is written in base64 from then on.
    func checkForm(_ b: BlobRuntime, id: String, sent: Bytes) async throws -> Bool {
        guard try store.value("blobs.formchecked") == nil else { return false }
        let got = try await b.hub.fetch(ids: [id])
        try store.setValue("blobs.formchecked", [1])
        if got.count == 1 && got[0].envelope == sent { return false }
        try store.setValue("blobs.form", Bytes("base64".utf8))
        changed(.syncFailed("blob: the server changed the escaped form of a chunk; chunks go up in base64 on this hub from now on"))
        flushEvents()
        return true
    }

    /// Merges a pending write whose chunks are all on the hub into the replica and publishes it (§17.6, step 5). A
    /// record deleted meanwhile drops it; a write whose epoch was replaced is signed again under the current one (§11.6).
    func commit(_ b: BlobRuntime, _ u: Store.Upload) async throws {
        try takeExternalChanges()
        guard try store.uploads().contains(where: { $0.write == u.write }) else { return }   // cancelled or replaced meanwhile
        guard let (signed, m) = uploadOf(u), let c = collections[u.scope] else { try dropUpload(u); flushEvents(); return }
        var w = signed
        if c.records[u.record]?.deleted == true { try dropUpload(u); flushEvents(); return }
        guard let me, let head = c.head, !c.dead, let st = c.statements[head]?.statement, !st.deleted,
              let member = st.members.first(where: { $0.uid == me.participant }), member.role != .reader
        else { try dropUpload(u); flushEvents(); return }   // never published: its chunks are orphans (§17.6)
        guard let (c2, me2, _, epoch) = try? writable(u.scope, record: u.record) else { return }   // wait for a key or a rekey
        if epoch != u.epoch {
            let base = reissueBase(w)
            guard let ref = firstWriterStatement(c2, epoch: epoch, me: me2), base.count <= maxVector
            else { throw TandemError.invalid("blob: pending write cannot enter the current epoch") }
            w = try reissueWrite(w, me: me2, ref: ref, base: base)
        }
        let content = b.content(m.hash), staged = b.staged.appendingPathComponent(u.staged)
        // Preserve staged content until the manifest and journal removal commit together.
        // A failed transaction or process loss can then retry the same upload.
        if !FileManager.default.fileExists(atPath: content.path) {
            try FileManager.default.linkItem(at: staged, to: content)
        }
        let stageMark = "blobs.staged." + u.write.hex, id = try w.id()
        try await keepWrites(u.scope, record: u.record, epoch: epoch, writes: [w]) {
            try touch(m.hash)
            try store.setValue(stageMark, id + m.hash)
            try store.removeUpload(u.write)
        }
        // A failed feed append must not turn the only source into evictable cache.
        if try store.onFeed(id) {
            try? FileManager.default.removeItem(at: staged)
            try? store.removeValue(stageMark)
        }
        changed(.blobs(list: u.scope, records: [u.record]))
        flushEvents()
    }

    /// Save writes and optional completion bookkeeping atomically before exposing or publishing the new replica.
    func keepWrites(_ cid: Bytes, record: Bytes, epoch: ID, writes: [Write], commit: () throws -> Void = {}) async throws {
        guard var c = collections[cid], let head = c.head, let n = c.statements[head]?.statement.keyN else { return }
        var r = try c.records[record] ?? Record(hub: pin.hub, scope: cid, id: record)
        try r.merge(try writes.map { try Entry($0, epoch: epoch, epochNumber: n) })
        try store.transaction {
            for w in writes {
                try store.putWrite(id: try w.id(), scope: cid, record: record, epoch: epoch, payload: try recordPayload(record: record, writes: [w]))
            }
            try commit()
        }
        c.records[record] = r
        collections[cid] = c
        changed(.items(list: cid, records: [record]))
        flushEvents()
        do { try await publish(cid: cid, epoch: epoch, record: record, writes: r.kept.map(\.write)) }
        catch { changed(.syncFailed("\(error)")); flushEvents() }
    }

    // MARK: The index (§17.4.3)

    /// Brings the chunk index up to date from the blob folder's headers.
    func refreshIndex(_ b: BlobRuntime) async throws {
        var more = true
        while more {
            let cursor = try store.value("blobs.cursor").map { String(decoding: $0, as: UTF8.self) } ?? ""
            let changes = try await b.hub.changes(since: cursor)
            if changes.more && changes.cursor == cursor { throw TandemError.invalid("blob hub: a page of changes made no progress") }
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            try store.transaction {
                try store.forgetChunks(changes.removed)
                try store.noteChunks(changes.added, seen: now)
                try store.setValue("blobs.cursor", Bytes(changes.cursor.utf8))
            }
            more = changes.more
        }
    }

    /// For each address, the lowest message this device verified as carrying it, fetching unverified claims to verify
    /// or reject them (§17.4.3). Addresses without one are left out.
    func settle(_ b: BlobRuntime, _ addrs: [Bytes]) async throws -> [Bytes: String] {
        var out: [Bytes: String] = [:], done: Set<Bytes> = []
        while true {
            var fetch: [String] = [], claims: [String: Bytes] = [:]
            for a in addrs where !done.contains(a) {
                let rows = try store.chunks(address: a).sorted { blobIDLess($0.id, $1.id) }
                if let v = rows.first(where: { $0.state == .verified }) { out[a] = v.id; done.insert(a) }
                else if let u = rows.first(where: { $0.state == .unverified }) { fetch.append(u.id); claims[u.id] = a }
                else { done.insert(a) }
            }
            if fetch.isEmpty { return out }
            let got = try await b.hub.fetch(ids: fetch)
            var seen: Set<String> = []
            for c in got {
                seen.insert(c.id)
                try store.setChunkState(c.id, Blob.address(c.envelope) == claims[c.id] ? .verified : .rejected)
            }
            try store.forgetChunks(fetch.filter { !seen.contains($0) })
        }
    }

    // MARK: Reading (§17.8)

    /// The file in a field _blob:<name> of a record, as this device sees it now.
    public func blob(list cid: Bytes, record: Bytes, name: String) -> BlobInfo? {
        let field = Self.blobPrefix + Bytes(name.utf8)
        if let b = blobs, let ups = try? store.uploads() {
            for u in ups where u.scope == cid && u.record == record && Bytes(u.field.utf8) == field {
                if let (_, m) = uploadOf(u) {
                    return BlobInfo(name: name, state: .uploading, size: m.size, hash: m.hash, path: b.staged.appendingPathComponent(u.staged),
                                    chunks: m.chunks?.count ?? 0, done: 0)
                }
            }
        }
        guard let r = collections[cid]?.records[record], !r.deleted, let top = r.siblings(field).first else { return nil }
        guard case let .manifest(m) = Blob.parse(top.write.value) else { return BlobInfo(name: name, state: .unreadable, size: 0, hash: nil, path: nil, chunks: 0, done: 0) }
        var info = BlobInfo(name: name, state: .remote, size: m.size, hash: m.hash, path: nil, chunks: m.chunks?.count ?? 0, done: 0)
        if (try? store.value("blobs.unreadable." + top.id.hex)) != nil { info.state = .unreadable; return info }
        if let data = m.data, H(data) != m.hash { info.state = .unreadable; return info }
        guard let b = blobs else { if m.chunks == nil { info.state = .here }; return info }
        if m.chunks == nil { info.state = .here; info.path = b.content(m.hash); return info }
        if FileManager.default.fileExists(atPath: b.content(m.hash).path) { info.state = .here; info.path = b.content(m.hash); info.done = info.chunks }
        else if let d = b.downloads[m.hash] { info.state = .downloading; info.done = d.done }
        else if b.unavailable.contains(top.id) { info.state = .unavailable }
        return info
    }

    /// Makes the file in the field _blob:<name> of a record here and returns where it is. Chunks come a few to a command
    /// over the blob hub's transfer connections; a download that stops resumes.
    public func fetchBlob(list cid: Bytes, record: Bytes, name: String) async throws -> URL {
        guard let b = blobs else { throw BlobError.noBlobHub }
        guard let r = collections[cid]?.records[record], !r.deleted, let top = r.siblings(Self.blobPrefix + Bytes(name.utf8)).first
        else { throw BlobError.noSuchFile }
        guard case let .manifest(m) = Blob.parse(top.write.value), try store.value("blobs.unreadable." + top.id.hex) == nil
        else { throw BlobError.unreadable }
        return try await fetchManifest(b, list: cid, record: record, id: top.id, m)
    }

    func fetchManifest(_ b: BlobRuntime, list cid: Bytes, record: Bytes, id: Bytes, _ m: Blob.Manifest) async throws -> URL {
        let path = b.content(m.hash)
        if let data = m.data {
            guard H(data) == m.hash else { throw BlobError.unreadable }
            if !FileManager.default.fileExists(atPath: path.path) { try Data(data).write(to: path, options: .atomic) }
            return path
        }
        let layout = H(try Blob.encode(m))
        while true {
            if FileManager.default.fileExists(atPath: path.path) { try touch(m.hash); return path }
            if let d = b.downloads[m.hash] {
                if !d.finished { await withCheckedContinuation { d.waiters.append($0) } }
                if d.layout != layout { continue } // a different manifest's failure is not ours
                if let e = d.error { throw e }
                return path
            }
            let d = BlobRuntime.Download(chunks: m.chunks!.count, layout: layout)
            b.downloads[m.hash] = d
            b.unavailable.remove(id)
            changed(.blobs(list: cid, records: [record])); flushEvents()
            useBlobHub(b)
            do { try await download(b, d, id: id, m, to: path) } catch { d.error = error }
            await doneUsing(b)
            b.downloads[m.hash] = nil
            if d.error as? BlobError == .unavailable { b.unavailable.insert(id) }
            d.finished = true
            for w in d.waiters { w.resume() }
            d.waiters.removeAll()
            changed(.blobs(list: cid, records: [record])); flushEvents()
            if let e = d.error { throw e }
            evict(b, keeping: m.hash.hex)
            return path
        }
    }

    /// Fetches every chunk not yet in the partial file, checks each against its address, its key and its length before
    /// writing it at its offset, and checks the whole content's hash.
    func download(_ b: BlobRuntime, _ d: BlobRuntime.Download, id: Bytes, _ m: Blob.Manifest, to path: URL) async throws {
        let entries = m.chunks!
        let partial = b.partial.appendingPathComponent(m.hash.hex)
        let doneKey = "blobs.partial." + m.hash.hex
        let layoutKey = "blobs.layout." + m.hash.hex
        // The hash names plaintext; it does not bind this manifest's offsets,
        // keys or chunk order. A missing partial also invalidates its bitmap.
        if try store.value(layoutKey) != d.layout || !FileManager.default.fileExists(atPath: partial.path) {
            if FileManager.default.fileExists(atPath: partial.path) { try FileManager.default.removeItem(at: partial) }
            try store.transaction {
                try store.removeValue(doneKey)
                try store.setValue(layoutKey, d.layout)
            }
        }
        if !FileManager.default.fileExists(atPath: partial.path) { FileManager.default.createFile(atPath: partial.path, contents: nil) }
        var have = try store.value(doneKey) ?? []
        if have.count < (entries.count + 7) / 8 { have += Bytes(repeating: 0, count: (entries.count + 7) / 8 - have.count) }
        var todo: [(entry: Blob.ChunkEntry, offset: Int, index: Int)] = [], offset = 0
        for (i, e) in entries.enumerated() {
            if have[i / 8] & (1 << (i % 8)) == 0 { todo.append((e, offset, i)) } else { d.done += 1 }
            offset += e.length
        }
        if !todo.isEmpty { try await refreshIndex(b) }
        var tried: Set<String> = []
        while !todo.isEmpty {
            // Up to four distinct addresses a round; an address repeated in the manifest is fetched once.
            var ids: [String] = [], wants: [String: [(entry: Blob.ChunkEntry, offset: Int, index: Int)]] = [:]
            var addressOf: [String: Bytes] = [:], rest: [(entry: Blob.ChunkEntry, offset: Int, index: Int)] = []
            for t in todo {
                if let id = addressOf.first(where: { $0.value == t.entry.address })?.key { wants[id]!.append(t); continue }
                if ids.count == 4 { rest.append(t); continue }
                let rows = try store.chunks(address: t.entry.address).sorted { x, y in
                    (x.state == .verified) != (y.state == .verified) ? x.state == .verified : blobIDLess(x.id, y.id)
                }
                guard let candidate = rows.first(where: { $0.state != .rejected && !tried.contains($0.id) })?.id else { throw BlobError.unavailable }
                tried.insert(candidate); ids.append(candidate); wants[candidate] = [t]; addressOf[candidate] = t.entry.address
            }
            let got = Dictionary(try await b.hub.fetch(ids: ids).map { ($0.id, $0.envelope) }, uniquingKeysWith: { a, _ in a })
            var next: [(entry: Blob.ChunkEntry, offset: Int, index: Int)] = []
            for mid in ids {
                let envelope = got[mid], good = envelope.map { Blob.address($0) == addressOf[mid] } ?? false
                if envelope == nil { try store.forgetChunks([mid]) } else { try store.setChunkState(mid, good ? .verified : .rejected) }
                guard good, let envelope else { next += wants[mid]!; continue }
                for t in wants[mid]! {
                    guard let plain = try? Blob.open(key: t.entry.key, hub: pin.hub, envelope: envelope, length: t.entry.length) else {
                        try store.setValue("blobs.unreadable." + id.hex, [1])
                        throw BlobError.unreadable
                    }
                    try Self.write(plain, at: t.offset, to: partial)
                    have[t.index / 8] |= 1 << (t.index % 8)
                    d.done += 1
                }
            }
            try store.setValue(doneKey, have)
            todo = next + rest
        }
        guard try await Self.hash(file: partial) == m.hash else {
            try store.setValue("blobs.unreadable." + id.hex, [1])
            try? FileManager.default.removeItem(at: partial)
            throw BlobError.unreadable
        }
        try? FileManager.default.removeItem(at: path)
        try FileManager.default.moveItem(at: partial, to: path)
        try store.removeValue(doneKey)
        try store.removeValue(layoutKey)
        try touch(m.hash)
    }

    nonisolated static func write(_ bytes: Bytes, at offset: Int, to file: URL) throws {
        let handle = try FileHandle(forWritingTo: file)
        defer { try? handle.close() }
        try handle.seek(toOffset: UInt64(offset))
        try handle.write(contentsOf: Data(bytes))
        try handle.synchronize()
    }

    nonisolated static func hash(file: URL) async throws -> Bytes {
        let handle = try FileHandle(forReadingFrom: file)
        defer { try? handle.close() }
        var sha = SHA256()
        while let data = try handle.read(upToCount: 1 << 20), !data.isEmpty { sha.update(data: data) }
        return Bytes(sha.finalize())
    }

    /// Notes that content was used now, for the store's eviction order.
    func touch(_ hash: Bytes) throws {
        var t = Bytes(), now = UInt64(Date().timeIntervalSince1970 * 1000)
        for _ in 0..<8 { t.insert(UInt8(now & 0xff), at: 0); now >>= 8 }
        try store.setValue("blobs.opened." + hash.hex, t)
    }

    /// Removes content opened longest ago while the store is over its limit; it becomes remote again.
    func evict(_ b: BlobRuntime, keeping: String) {
        guard let names = try? FileManager.default.contentsOfDirectory(atPath: b.directory.path) else { return }
        // Content a staged copy holds is one inode with it: evicting the cached link frees nothing.
        let held = Set(((try? store.values(prefix: "blobs.staged.")) ?? []).filter { $0.1.count == 64 }.map { Array($0.1.suffix(32)).hex })
        var files: [(name: String, size: Int, opened: UInt64)] = [], total = 0
        for name in names where name.count == 64 {
            let size = ((try? FileManager.default.attributesOfItem(atPath: b.directory.appendingPathComponent(name).path))?[.size] as? NSNumber)?.intValue ?? 0
            let opened = ((try? store.value("blobs.opened." + name)) ?? nil).map { $0.reduce(UInt64(0)) { $0 << 8 | UInt64($1) } } ?? 0
            files.append((name, size, opened)); total += size
        }
        for f in files.sorted(by: { $0.opened < $1.opened }) where total > b.settings.storeLimit {
            if f.name == keeping { continue } // an explicit fetch must return a readable file
            if held.contains(f.name) { continue } // its size still counts: the disk holds it either way
            if (try? FileManager.default.removeItem(at: b.directory.appendingPathComponent(f.name))) != nil {
                total -= f.size
                try? store.removeValue("blobs.opened." + f.name)
            }
        }
    }

    // MARK: Keeping files (§17.8)

    /// Whether this device fetches every file of a list unasked, up to the `unasked` size.
    public func keepFiles(list cid: Bytes, _ keep: Bool) throws {
        let name = "blobs.keep." + cid.hex
        if keep { try store.setValue(name, [1]); blobs?.hygiene = true; wakeBlobs() } else { try store.removeValue(name) }
    }

    func keepFiles(_ b: BlobRuntime) async throws {
        var jobs: [(list: Bytes, record: Bytes, id: Bytes, m: Blob.Manifest)] = []
        for (cid, c) in collections.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) where try store.value("blobs.keep." + cid.hex) != nil {
            for (rid, r) in c.records where !r.deleted {
                for e in r.kept where e.write.op == .assign && Self.isBlobField(e.write.field) && r.siblings(e.write.field).first?.id == e.id {
                    if case let .manifest(m) = Blob.parse(e.write.value), m.chunks != nil, m.size <= b.settings.unasked,
                       !FileManager.default.fileExists(atPath: b.content(m.hash).path) {
                        jobs.append((cid, rid, e.id, m))
                    }
                }
            }
        }
        for j in jobs {
            do { _ = try await fetchManifest(b, list: j.list, record: j.record, id: j.id, j.m) }
            catch let e as BlobError where e == .unavailable || e == .unreadable {}
        }
    }

    /// The blob folder's storage quota.
    public func blobQuota() async throws -> Quota {
        guard let b = blobs else { throw BlobError.noBlobHub }
        useBlobHub(b)
        do { let q = try await b.hub.quota(); await doneUsing(b); return q } catch { await doneUsing(b); throw error }
    }
}
