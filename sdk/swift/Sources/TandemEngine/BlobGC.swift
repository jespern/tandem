import CryptoKit
import Foundation
import Tandem

// Collecting and healing chunks (DESIGN.md §17.7 and §17.8). Both decide from one view of each list: which manifests
// are live (healed) and which are protected (never collected), which addresses any write this device holds has listed,
// and whether anything stops clean-up there.

struct BlobScope {
    let cid: Bytes
    var tags: Set<String> = []          // every channel of the list this device knows, any epoch
    var current: String?                // the current channel; nil in a deleted or dead list
    var collect = false                 // nothing stops clean-up in this list
    var orphans = false                 // and orphans may go too
    var purge = false                   // a deleted list this owner's device collected: every chunk goes (§11.9)
    var protected: Set<Bytes> = []      // addresses of protected manifests
    var listed: Set<Bytes> = []         // addresses a write this device holds has listed
    var live: [(id: Bytes, m: Blob.Manifest)] = []
}

extension Device {
    func blobScopes() throws -> [BlobScope] {
        let heldIn = Set(try store.held().map(\.channel))
        let ups = try store.uploads()
        let dropped = try store.values(prefix: "blobs.dropped.")
        var out: [BlobScope] = []
        for (cid, c) in collections.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            var s = BlobScope(cid: cid)
            for (epoch, key) in c.keys { s.tags.insert(Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)) }
            let head = c.head, epoch = head.flatMap { c.epoch(of: $0) }
            if let head, let epoch, let key = c.keys[epoch], let st = c.statements[head]?.statement, !st.deleted, !c.dead, following(cid) {
                s.current = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
                s.collect = true
            }
            if let head, c.statements[head]?.statement.deleted == true, purged(cid), c.owner == me?.participant { s.purge = true }
            if !s.tags.isDisjoint(with: heldIn) { s.collect = false }   // a held write may be a manifest this device can't see yet
            func protect(_ value: Bytes, live: Entry?) {
                switch Blob.parse(value) {
                case .unknown: s.collect = false   // it may list chunks this device can't tell (§17.7)
                case let .manifest(m):
                    for e in m.chunks ?? [] { s.protected.insert(e.address) }
                    if let live { s.live.append((live.id, m)) }
                case .none: break
                }
            }
            for (rid, r) in c.records.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
                let onFeed = try r.kept.allSatisfy { try store.onFeed($0.id) }
                if !r.deleted {
                    for e in r.kept where e.write.op == .assign && Self.isBlobField(e.write.field) { protect(e.write.value, live: e) }
                }
                var feed: [Entry] = []
                for row in try store.writes(scope: cid, record: rid) {
                    guard case let .record(state) = try? Kind1.parse(row.payload, hub: pin.hub, scope: cid), let w = state.writes.first else { continue }
                    if w.op == .assign, Self.isBlobField(w.field), case let .manifest(m) = Blob.parse(w.value) {
                        for e in m.chunks ?? [] { s.listed.insert(e.address) }
                    }
                    if !onFeed, try store.onFeed(try w.id()), let st = c.statements[w.ref], let source = c.epoch(of: w.ref),
                       let e = try? Entry(w, epoch: source, epochNumber: st.statement.keyN) {
                        feed.append(e)
                    }
                }
                // The replica is ahead of the hub here: what other devices see is what is on the feed, and that stays
                // protected until what replaces it is on the feed too.
                if !onFeed, !feed.isEmpty, var fr = try? Record(hub: pin.hub, scope: cid, id: rid), (try? fr.merge(feed)) != nil, !fr.deleted {
                    for e in fr.kept where e.write.op == .assign && Self.isBlobField(e.write.field) { protect(e.write.value, live: nil) }
                }
            }
            for u in ups where u.scope == cid {
                if case let .record(state)? = try? Kind1.parse(u.payload, hub: pin.hub, scope: cid), let w = state.writes.first { protect(w.value, live: nil) }
            }
            for (name, payload) in dropped where name.hasPrefix("blobs.dropped." + cid.hex + ".") {
                if case let .record(state)? = try? Kind1.parse(payload, hub: pin.hub, scope: cid), let w = state.writes.first,
                   case let .manifest(m) = Blob.parse(w.value) {
                    for e in m.chunks ?? [] { s.listed.insert(e.address) }
                }
            }
            // Orphans only while the current inventory is whole here: a missing listed write may be a manifest that
            // devices holding it will publish again (§17.7).
            s.orphans = s.collect
            if s.collect, let epoch, epoch != c.genesis {
                if let inv = inventory(epoch) {
                    for ids in inv.entries.values { for id in ids where try !store.hasWrite(id) { s.orphans = false } }
                } else {
                    s.orphans = false
                }
            }
            out.append(s)
        }
        return out
    }

    /// What clean-up may destroy now by the rules of §17.7 that need no fetch.
    static func doomed(_ scopes: [BlobScope], _ rows: [Store.ChunkRow], grace: Duration, now: Int64) -> [String] {
        var byTag: [String: BlobScope] = [:]
        for s in scopes { for t in s.tags { byTag[t] = s } }
        let ms = grace.components.seconds * 1000 + grace.components.attoseconds / 1_000_000_000_000_000
        return rows.compactMap { r in
            if let s = byTag[r.channel], s.purge { return r.id }
            guard let s = byTag[r.channel], s.collect, r.state != .rejected, !s.protected.contains(r.address) else { return nil }
            return s.listed.contains(r.address) || (s.orphans && now - r.seen > ms) ? r.id : nil
        }
    }

    static func scopeView(_ scopes: [BlobScope]) -> Bytes {
        var h = SHA256()
        for s in scopes {
            h.update(data: s.cid)
            if s.purge { h.update(data: [1]) }
            for set in [s.listed, s.protected] {
                for a in set.sorted(by: { $0.lexicographicallyPrecedes($1) }) { h.update(data: a) }
                h.update(data: [0])
            }
            for t in s.tags.sorted() { h.update(data: Bytes(t.utf8)) }
            h.update(data: [0])
        }
        return Bytes(h.finalize())
    }

    /// The clean-up and healing pass, after a sync. It opens the blob folder only when there is something to collect,
    /// when this device should check content it holds, or when what it lists or protects changed since it last looked.
    func blobHygiene(_ b: BlobRuntime) async throws {
        guard let me, registry.activity(me.certificate).isValid else { return }
        let scopes = try blobScopes()
        dropUnnamed(b)
        var rows = try store.chunks()
        let now = { Int64(Date().timeIntervalSince1970 * 1000) }
        let holds = scopes.contains { $0.live.contains { $0.m.chunks != nil && FileManager.default.fileExists(atPath: b.content($0.m.hash).path) } }
        var count: [Bytes: Int] = [:]
        for r in rows where r.state != .rejected { count[r.address, default: 0] += 1 }
        let copies = count.values.contains { $0 > 1 }
        let view = Self.scopeView(scopes), heal = !b.healed
        if Self.doomed(scopes, rows, grace: b.settings.orphanGrace, now: now()).isEmpty && !copies && !(heal && holds) && view == b.view {
            if !holds { b.healed = true }
            return
        }
        try await refreshIndex(b)
        b.view = view
        rows = try store.chunks()
        var gone = Set(Self.doomed(scopes, rows, grace: b.settings.orphanGrace, now: now()))
        // Copies (§17.7): every device keeps the lowest verified one.
        var byTag: [String: BlobScope] = [:]
        for s in scopes { for t in s.tags { byTag[t] = s } }
        var groups: [Bytes: [Store.ChunkRow]] = [:]
        for r in rows where byTag[r.channel]?.collect == true && r.state != .rejected && !gone.contains(r.id) { groups[r.address, default: []].append(r) }
        let several = groups.filter { $0.value.count > 1 }.map(\.key)
        if !several.isEmpty {
            let winners = try await settle(b, several)
            for a in several {
                guard let w = winners[a] else { continue }
                for r in groups[a]! where r.id != w && blobIDLess(w, r.id) { gone.insert(r.id) }
            }
        }
        if !gone.isEmpty {
            let ids = gone.sorted()
            try await b.hub.destroy(ids: ids)
            try store.forgetChunks(ids)
        }
        if heal {
            try await self.heal(b, scopes)
            b.healed = true
        }
        try forgetDropped()
    }

    /// Restores the chunks of live manifests in current channels whose content this device holds (§17.8). A local
    /// copy that seals to another address is checked against the manifest's hash: a good copy means the manifest is
    /// inconsistent, which is marked unreadable; a bad one is dropped.
    func heal(_ b: BlobRuntime, _ scopes: [BlobScope]) async throws {
        for s in scopes {
            guard let tag = s.current else { continue }
            for l in s.live where l.m.chunks != nil {
                let path = b.content(l.m.hash)
                guard FileManager.default.fileExists(atPath: path.path) else { continue }
                let present = try await settle(b, addresses(l.m))
                let (missing, offsets) = Self.missing(l.m, present)
                guard !missing.isEmpty else { continue }
                do { _ = try await Self.seal(file: path, entries: missing, offsets: offsets, hub: pin.hub) } catch {
                    if try await Self.hash(file: path) == l.m.hash { try store.setValue("blobs.unreadable." + l.id.hex, [1]) }   // keep the file
                    else { try? FileManager.default.removeItem(at: path) }   // the local copy is damaged
                    continue
                }
                var appended: [String: Bytes] = [:]
                try await appendChunks(b, file: path, tag: tag, missing, offsets, appended: &appended)
            }
        }
    }

    /// The content hashes that live blob writes on the feed name.
    func publishedContent() -> Set<String> {
        var out: Set<String> = []
        for c in collections.values {
            for r in c.records.values where !r.deleted {
                for e in r.kept where e.write.op == .assign && Self.isBlobField(e.write.field) {
                    if case let .manifest(m) = Blob.parse(e.write.value), m.chunks != nil, (try? store.onFeed(e.id)) == true { out.insert(m.hash.hex) }
                }
            }
        }
        return out
    }

    /// Removes content no record names any more, downloaded or half downloaded: a deleted record's, a replaced
    /// version's, a collected list's, and what was kept about it. A gone list's files stay as long as its replica does.
    func dropUnnamed(_ b: BlobRuntime) {
        // Without the upload journal the content still in use isn't known: nothing is taken as unnamed. Without the staged
        // markers, the staged folder is left alone.
        guard let uploads = try? store.uploads() else { return }
        let markers = try? store.values(prefix: "blobs.staged.")
        var hashes: Set<String> = [], writes: Set<String> = [], staged: Set<String> = []
        for c in collections.values {
            for r in c.records.values where !r.deleted {
                for e in r.kept where e.write.op == .assign && Self.isBlobField(e.write.field) {
                    if case let .manifest(m) = Blob.parse(e.write.value), m.chunks != nil { hashes.insert(m.hash.hex) }
                    writes.insert(e.id.hex)
                }
            }
        }
        for u in uploads {
            staged.insert(u.staged)
            if case let .record(state)? = try? Kind1.parse(u.payload, hub: pin.hub, scope: u.scope), let w = state.writes.first,
               case let .manifest(m) = Blob.parse(w.value), m.chunks != nil { hashes.insert(m.hash.hex) }
        }
        let fm = FileManager.default
        var published: Set<String>?
        for (key, value) in markers ?? [] {
            let name = String(key.dropFirst("blobs.staged.".count)), source = b.staged.appendingPathComponent(name)
            guard value.count == 64 else { staged.insert(name); continue }
            let id = Array(value.prefix(32)), hash = Array(value.suffix(32)).hex
            if hashes.contains(hash) {
                let cache = b.directory.appendingPathComponent(hash)
                if !fm.fileExists(atPath: cache.path) { try? fm.linkItem(at: source, to: cache) }
                if !fm.fileExists(atPath: cache.path) { staged.insert(name); continue }
                // What waits is the content, not that write's ID: a key change can sign the write again under a new
                // one (§11.6), and then the feed holds that one instead.
                if (try? store.onFeed(id)) != true {
                    if published == nil { published = publishedContent() }
                    if published?.contains(hash) != true { staged.insert(name); continue }
                }
            }
            if !fm.fileExists(atPath: source.path) { try? store.removeValue(key) }
        }
        var folders = [(b.directory, "blobs.opened.", hashes), (b.partial, "blobs.partial.", hashes)]
        if markers != nil { folders.append((b.staged, "", staged)) }
        for (dir, marker, names) in folders {
            for name in (try? fm.contentsOfDirectory(atPath: dir.path)) ?? [] where name.count == 64 && !names.contains(name) && Bytes(hex: name) != nil {
                if (try? fm.removeItem(at: dir.appendingPathComponent(name))) != nil {
                    try? store.removeValue((marker.isEmpty ? "blobs.staged." : marker) + name)
                }
            }
        }
        for prefix in ["blobs.opened.", "blobs.partial.", "blobs.layout."] {
            for (name, _) in (try? store.values(prefix: prefix)) ?? [] where !hashes.contains(String(name.dropFirst(prefix.count))) {
                try? store.removeValue(name)
            }
        }
        for (name, _) in (try? store.values(prefix: "blobs.unreadable.")) ?? [] where !writes.contains(String(name.dropFirst("blobs.unreadable.".count))) {
            try? store.removeValue(name)
        }
    }

    /// Forgets a dropped upload once none of its chunks is in the index any more.
    func forgetDropped() throws {
        let inIndex = Set(try store.chunks().map(\.address))
        for (name, payload) in try store.values(prefix: "blobs.dropped.") {
            let parts = name.dropFirst("blobs.dropped.".count).split(separator: ".")
            guard parts.count == 2, let scope = Bytes(hex: String(parts[0])) else { continue }
            var left = false
            if case let .record(state)? = try? Kind1.parse(payload, hub: pin.hub, scope: scope), let w = state.writes.first,
               case let .manifest(m) = Blob.parse(w.value) {
                left = (m.chunks ?? []).contains { inIndex.contains($0.address) }
            }
            if !left { try store.removeValue(name) }
        }
    }
}
