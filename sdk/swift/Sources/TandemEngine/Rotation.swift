import Foundation
import Tandem

// Changing a list's key (§11.6) and finishing or discarding another owner device's change (§11.7).
// Ported from the testbed's Go engine (testbed:sdk/go/device/rotate.go), crash points and all.

/// A key change in flight, as the `flight` field of the list's `membership:` record in home holds it (§6.2, §8.2).
public struct FlightNote: Equatable, Sendable {
    public let parent: ID, statement: ID, key: Bytes
    public let members: [Statement.Member]
    /// A short description for the user, and the name of the device that started the change.
    public let what: String, device: String

    func json() -> Bytes {
        JSONWriter.object { w in
            w.member("parent", b64u: parent)
            w.member("stmt", b64u: statement)
            w.member("k", b64u: key)
            w.member("members") { a in
                a.array(members.sorted { $0.uid.lexicographicallyPrecedes($1.uid) }) { a, m in
                    a.open()
                    a.member("uid", b64u: m.uid)
                    a.member("role", text: Bytes(m.role.rawValue.utf8))
                    a.member("name", text: m.name)
                    a.member("floor", b64u: m.floor)
                    a.close()
                }
            }
            w.member("what", text: Bytes(what.utf8))
            w.member("device", text: Bytes(device.utf8))
        }
    }

    /// No pending change: the JSON `null`. A reader takes anything that isn't a well-formed note as none.
    static let none = Bytes("null".utf8)

    static func parse(_ data: Bytes) -> FlightNote? {
        guard let j = try? JSON.parse(data), let parent = try? j.bytes("parent", count: 32),
              let statement = try? j.bytes("stmt", count: 32), let key = try? j.bytes("k", count: 32) else { return nil }
        var members: [Statement.Member] = []
        if case let .array(items)? = j["members"] {
            for m in items {
                guard case let .string(r)? = m["role"], let role = Statement.Role(rawValue: String(decoding: r, as: UTF8.self)),
                      let uid = try? m.bytes("uid", count: 32), let floor = try? m.bytes("floor", count: 32) else { continue }
                members.append(.init(uid: uid, role: role, name: (try? m.text("name")) ?? [], floor: floor))
            }
        }
        let what = (try? j.text("what")).map { String(decoding: $0, as: UTF8.self) } ?? ""
        let device = (try? j.text("device")).map { String(decoding: $0, as: UTF8.self) } ?? ""
        return FlightNote(parent: parent, statement: statement, key: key, members: members, what: what, device: device)
    }
}

/// What a device persists before any of a key change leaves it (§11.6 step 2).
struct RotationJournal {
    var cid: Bytes, parent: ID, oldEpoch: ID, statement: Write, id: ID, key: Bytes, inventory: Bytes, what: String
    var step: Int, noteWritten: Bool

    func json() throws -> Bytes {
        let payload = try recordPayload(record: Roster.record, writes: [statement])
        return JSONWriter.object { w in
            w.member("c", b64u: cid)
            w.member("parent", b64u: parent)
            w.member("old", b64u: oldEpoch)
            w.member("stmt", b64u: payload)
            w.member("k", b64u: key)
            w.member("inv", b64u: inventory)
            w.member("what", text: Bytes(what.utf8))
            w.member("step", int: UInt64(step))
            w.member("note", int: noteWritten ? 1 : 0)
        }
    }

    static func parse(_ data: Bytes, hub: Bytes) throws -> RotationJournal {
        let j = try JSON.parse(data)
        let cid = try j.bytes("c", count: 16)
        guard case let .record(state) = try Kind1.parse(try j.bytes("stmt"), hub: hub, scope: cid), let w = state.writes.first
        else { throw TandemError.invalid("journal: no statement") }
        return RotationJournal(cid: cid, parent: try j.bytes("parent", count: 32), oldEpoch: try j.bytes("old", count: 32),
                               statement: w, id: try w.id(), key: try j.bytes("k", count: 32), inventory: try j.bytes("inv"),
                               what: String(decoding: try j.text("what"), as: UTF8.self), step: Int(try j.int("step")),
                               noteWritten: try j.int("note") == 1)
    }
}

/// Why a change didn't happen: another device discarded or overtook it before it was announced (§11.6–11.7).
public enum ChangeError: Error, Equatable {
    /// Nothing changed: another device discarded this change, or made one of its own first.
    case superseded
    /// Another device of the owner has a change pending; finish or discard it first.
    case pending(FlightNote)
    /// The pending change the user was shown was finished, discarded or replaced meanwhile.
    case noteChanged
    /// The other device's change didn't get far enough to be finished; it can only be discarded.
    case cantFinish
}

/// A crash injected at a named point, for tests (§11.6's crash points).
struct Crash: Error { let point: String }

extension Device {
    func crash(_ point: String) throws { if crashPoint == point { throw Crash(point: point) } }

    /// This device's name for the user: its certificate's label.
    var deviceName: String {
        me.flatMap { registry.certificate($0.certificate) }.map { String(decoding: $0.label, as: UTF8.self) } ?? ""
    }

    /// For tests: stop with `Crash` when the change reaches `point` ("1:chosen", "2:journal", "2:note", "3:roster",
    /// "4:announced", "4:home", "5:grant", "6:cleaned").
    func crash(at point: String?) { crashPoint = point }

    func membershipName(_ cid: Bytes) -> String { "membership:" + B64u.encode(cid) }

    /// The note of a change in flight for this list, if home holds one.
    func flightNote(_ cid: Bytes) -> FlightNote? { homeValue(membershipName(cid), field: "flight").flatMap(FlightNote.parse) }

    /// A change another device of this owner started and hasn't finished (§11.7), to show the user.
    public func pendingChange(list cid: Bytes) throws -> FlightNote? {
        guard try store.journal("rotate." + cid.hex) == nil else { return nil }
        return flightNote(cid)
    }

    // MARK: Changes that start an epoch

    /// Remove a member, starting a new epoch with a fresh key (§8.2, §11.6).
    public func remove(_ participant: ID, from cid: Bytes) async throws {
        try await change(cid, what: "remove a member") { members in members.filter { $0.uid != participant || $0.role == .owner } }
    }

    /// Change a member's role. A promotion only adds access and keeps the epoch; lowering one starts a new epoch.
    public func setRole(_ role: Statement.Role, for participant: ID, in cid: Bytes) async throws {
        guard role != .owner, let c = collections[cid], let head = c.head,
              let old = c.statements[head]?.statement.members.first(where: { $0.uid == participant })?.role, old != .owner
        else { throw TandemError.invalid("role: not a member of that list") }
        guard role != old else { return }
        let edit: ([Statement.Member]) -> [Statement.Member] = { ms in
            ms.map { m in var m = m; if m.uid == participant { m.role = role }; return m }
        }
        if rank(role) > rank(old) { try await sameEpoch(cid) { $0.members = edit($0.members) } } else { try await change(cid, what: "change a role", edit: edit) }
    }

    /// Start a new epoch with the same members: after a device revocation, or when the owner asks (§6.4, §11.6).
    public func rekey(list cid: Bytes) async throws {
        try await change(cid, what: "a new key") { $0 }
    }

    /// A same-epoch roster statement (§8.2): only adds access or deletes the list, keeps every floor.
    /// Before signing it syncs; between signing and publishing it syncs again and checks that the parent is still the
    /// head and that no note has appeared, so a stale device can't put a second successor on one statement.
    @discardableResult
    func sameEpoch(_ cid: Bytes, edit: (inout Statement) -> Void) async throws -> (statement: ID, epoch: ID, key: Bytes) {
        try await sync()
        guard let me, let c = collections[cid], !c.dead, c.owner == me.participant, let head = c.head,
              let current = c.statements[head], let epoch = c.epoch(of: head), let key = c.keys[epoch], !current.statement.deleted
        else { throw TandemError.invalid("roster: not a list this participant owns, or it was deleted") }
        if let note = flightNote(cid) { throw ChangeError.pending(note) }
        var next = current.statement
        next.n += 1
        next.nonce = nil   // only a genesis has one (§10.8)
        edit(&next)
        next.members.sort { $0.uid.lexicographicallyPrecedes($1.uid) }
        var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: cid, record: Roster.record,
                      field: Roster.field, op: .assign, value: next.json(), author: me.participant, dot: try nextDot(me), ref: head)
        try w.sign(with: me.keys.signing)
        let id = try w.id()
        try await sync()
        guard var now = collections[cid], now.head == head, !now.dead else { throw ChangeError.superseded }
        if let note = flightNote(cid) { throw ChangeError.pending(note) }
        now.statements[id] = (w, next)
        collections[cid] = now
        try store.putWrite(id: id, scope: cid, record: Roster.record, epoch: epoch, payload: try recordPayload(record: Roster.record, writes: [w]))
        try await publish(cid: cid, epoch: epoch, record: Roster.record, writes: [w])
        return (id, epoch, key)
    }

    /// The counted roster line from the genesis to `id`.
    func chain(_ c: CollectionState, to id: ID) -> [ID] {
        var out: [ID] = [], current: ID? = id
        while let x = current, let s = c.statements[x] {
            out.append(x)
            current = x == c.genesis ? nil : s.write.ref
        }
        return out.reversed()
    }

    /// The admission in force at `at` for `participant`: the first statement listing them after one that doesn't (§11.2).
    func admission(of participant: ID, in c: CollectionState, at: ID) -> ID? {
        var admission: ID?, listed = false
        for id in chain(c, to: at) {
            let now = c.statements[id]?.statement.members.contains { $0.uid == participant } ?? false
            if now && !listed { admission = id }
            listed = now
        }
        return listed ? admission : nil
    }

    /// One journalled key change (§11.6 steps 1–2), then the rest of the steps.
    func change(_ cid: Bytes, what: String, edit: ([Statement.Member]) -> [Statement.Member]) async throws {
        guard try store.journal("rotate." + cid.hex) == nil, changing.insert(cid).inserted
        else { throw TandemError.invalid("rotate: a change of this list is in progress here") }
        defer { changing.remove(cid) }
        try await sync()
        guard let me, let c = collections[cid], !c.dead, c.owner == me.participant, let head = c.head, let epoch = c.epoch(of: head),
              let current = c.statements[head]?.statement, c.keys[epoch] != nil
        else { throw TandemError.invalid("rotate: not a list this participant owns, or it can't sync any more") }
        if let note = flightNote(cid) { throw ChangeError.pending(note) }

        // Step 1: the state to carry over, tombstones included, and what the current inventory lists unseen (§8.3).
        var entries: [Bytes: Set<Bytes>] = [:]
        var carried: [Bytes: [Write]] = [:]
        for (rid, r) in c.records where !r.kept.isEmpty {
            entries[rid, default: []].formUnion(r.kept.map(\.id))
            carried[rid] = r.kept.map(\.write)
        }
        for (rid, ids) in inventory(epoch)?.entries ?? [:] {
            for id in ids where try !store.hasWrite(id) { entries[rid, default: []].insert(id) }
        }
        let inventory = Inventory(entries.mapValues { Array($0) })
        try crash("1:chosen")

        // Step 2: the new key, floors afresh, the statement and inventory; journal, then the note.
        let key = Self.random(32)
        var members = edit(current.members)
        for i in members.indices { members[i].floor = try await publishedSnapshot(of: members[i].uid) }
        var next = current
        next.n += 1
        next.nonce = nil
        next.keyN += 1
        next.prev = current.commit
        next.commit = Tandem.commit(key)
        next.snapshot = H(try inventory.bytes())
        next.members = members.sorted { $0.uid.lexicographicallyPrecedes($1.uid) }
        var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: cid, record: Roster.record,
                      field: Roster.field, op: .assign, value: next.json(), author: me.participant, dot: try nextDot(me), ref: head)
        try w.sign(with: me.keys.signing)
        let id = try w.id()

        // The new channel's contents, sealed once: the inventory, the carried writes, and the whole roster ending in
        // the new statement, so the statement is on the hub only once everything it commits to is (step 3).
        let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: id)
        func seal(_ plaintext: Bytes) throws -> (String, Bytes) {
            (tag, try Envelope.seal(key: key, hub: pin.hub, kind: 1, tag: tag, plaintext: plaintext))
        }
        var upload = try Self.fragments(inventory, epoch: id).map(seal)
        for (rid, writes) in carried.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) {
            upload.append(try seal(try recordPayload(record: rid, writes: writes)))
        }
        upload.append(try seal(try recordPayload(record: Roster.record, writes: chain(c, to: head).map { c.statements[$0]!.write } + [w])))
        let j = RotationJournal(cid: cid, parent: head, oldEpoch: epoch, statement: w, id: id, key: key,
                                inventory: try inventory.bytes(), what: what, step: 2, noteWritten: false)
        try store.transaction {
            try store.setJournal("rotate." + cid.hex, try j.json())
            try store.setJournal("rotate." + cid.hex + ".upload", Self.pack(upload))
        }
        try crash("2:journal")
        try await runRotation(j)
    }

    /// Carry a journalled change on from its recorded step (§11.6 steps 2–6).
    func runRotation(_ start: RotationJournal) async throws {
        var j = start
        let cid = j.cid, name = membershipName(cid)
        let newTag = Tandem.collectionTag(key: j.key, hub: pin.hub, epoch: j.id)
        let inventory = try Inventory.parse(j.inventory)
        while j.step <= 6 {
            switch j.step {
            case 2:
                if !j.noteWritten {
                    guard let s = try? Statement.parse(j.statement.value) else { throw TandemError.invalid("journal: statement") }
                    let note = FlightNote(parent: j.parent, statement: j.id, key: j.key, members: s.members, what: j.what,
                                          device: deviceName)
                    try await setHome(name, field: "flight", value: note.json())
                    j.noteWritten = true
                    try store.setJournal("rotate." + cid.hex, try j.json())
                    try crash("2:note")
                }
            case 3:
                try await sync()
                let upload = try store.journal("rotate." + cid.hex + ".upload").map(Self.unpack) ?? []
                for (tag, envelope) in upload where try store.messages(withHash: H(envelope)).isEmpty {
                    let id = try await hub.append(channel: tag, body: envelope)
                    try store.noteMessage(id: id, channel: tag, hash: H(envelope))
                }
                try crash("3:roster")
            case 4:
                try await sync()
                if collections[cid]?.announced.contains(j.id) != true {
                    // The last check before the change happens: the note is still ours and the parent still the head.
                    guard flightNote(cid)?.statement == j.id, collections[cid]?.head == j.parent else {
                        try await destroyChannel(newTag)
                        try await clearOwnNote(cid, statement: j.id)
                        try dropRotation(cid)
                        throw ChangeError.superseded
                    }
                    // The announcement, in the old epoch's channel: the moment the change happens.
                    try await publish(cid: cid, epoch: j.oldEpoch, record: Roster.record, writes: [j.statement])
                    _ = try acceptStatement(j.statement, cid: cid, channel: j.oldEpoch)
                    try crash("4:announced")
                }
                try await settleHome(cid, statement: j.id, key: j.key, inventory: inventory)
                try crash("4:home")
            case 5:
                guard let me, let c = collections[cid], let s = c.statements[j.id]?.statement else { break }
                for m in s.members where m.uid != me.participant {
                    guard let admission = admission(of: m.uid, in: c, at: j.id) else { continue }
                    do {
                        try await sendGrant(cid: cid, to: m.uid, admission: admission, at: j.id, epoch: j.id, key: j.key, me: me)
                    } catch TandemError.invalid {
                        continue   // no admitted device to send to: their devices ask later (§11.4)
                    }
                    try crash("5:grant")
                }
            case 6:
                try await sync()
                try await cleanOld(j, inventory: inventory)
                try crash("6:cleaned")
            default:
                break
            }
            j.step += 1
            try store.setJournal("rotate." + cid.hex, try j.json())
        }
        try dropRotation(cid)
    }

    /// Step 4's home update, and the housekeeping of §11.7: the key into `keys`, the note out if it names this change.
    func settleHome(_ cid: Bytes, statement: ID, key: Bytes, inventory: Inventory?) async throws {
        let name = membershipName(cid)
        let element = Self.keyElement(epoch: statement, key: key)
        if !homeElements(name, field: "keys").contains(element) { try await addHome(name, field: "keys", element: element) }
        if flightNote(cid)?.statement == statement { try await setHome(name, field: "flight", value: FlightNote.none) }
        if var c = collections[cid] { c.offer(key: key, epoch: statement); collections[cid] = c }
        if let inventory, self.inventory(statement) == nil { try keepInventory(inventory, epoch: statement) }
        try moveCollection(cid)
    }

    /// A dropped change takes its note with it, if the note is still its own; otherwise it would block the list.
    func clearOwnNote(_ cid: Bytes, statement: ID) async throws {
        if flightNote(cid)?.statement == statement { try await setHome(membershipName(cid), field: "flight", value: FlightNote.none) }
    }

    func dropRotation(_ cid: Bytes) throws {
        try store.transaction {
            try store.clearJournal("rotate." + cid.hex)
            try store.clearJournal("rotate." + cid.hex + ".upload")
        }
    }

    func destroyChannel(_ tag: String) async throws {
        let ids = try store.messages(channel: tag)
        guard !ids.isEmpty else { return }
        try await hub.destroy(ids: ids)
    }

    /// Step 6: destroy the old epoch's data messages, each once every write in it is in the new channel, replaced there,
    /// or not carried over; a listed write missing from the new channel is copied across first. The roster stays.
    func cleanOld(_ j: RotationJournal, inventory: Inventory) async throws {
        guard let c = collections[j.cid], let oldKey = c.keys[j.oldEpoch] else { return }
        let oldTag = Tandem.collectionTag(key: oldKey, hub: pin.hub, epoch: j.oldEpoch)
        let newTag = Tandem.collectionTag(key: j.key, hub: pin.hub, epoch: j.id)
        try await healFragments(channel: newTag, epoch: j.id, key: j.key)   // the inventory first, then the old data
        let listed = Set(inventory.entries.values.flatMap { $0 })
        var inNew = Set<Bytes>()
        for m in try store.messages(channel: newTag) { inNew.formUnion(try store.contents(message: m).map(\.write)) }
        var missing: [Bytes: [Write]] = [:], doomed: [String] = []
        for m in try store.messages(channel: oldTag) {
            let contents = try store.contents(message: m)
            guard !contents.isEmpty, !contents.contains(where: { $0.record == Roster.record }) else { continue }
            for (record, id) in contents where listed.contains(id) && !inNew.contains(id) {
                if let e = c.records[record]?.kept.first(where: { $0.id == id }) { missing[record, default: []].append(e.write); inNew.insert(id) }
            }
            doomed.append(m)
        }
        for (record, writes) in missing {
            let envelope = try Envelope.seal(key: j.key, hub: pin.hub, kind: 1, tag: newTag, plaintext: try recordPayload(record: record, writes: writes))
            let id = try await hub.append(channel: newTag, body: envelope)
            try store.noteMessage(id: id, channel: newTag, hash: H(envelope))
            try store.noteContents(message: id, record: record, writes: try writes.map { try $0.id() })
        }
        if !doomed.isEmpty { try await hub.destroy(ids: doomed) }
    }

    /// Carry on journalled changes after a restart (§11.6): resume a change that is still this device's, finish the
    /// home update of one that was announced, and drop one another device discarded or overtook.
    public func resume() async throws {
        try await sync()
        var failure: (any Error)?
        for step in try store.journalSteps() where step.hasPrefix("rotate.") && !step.hasSuffix(".upload") {
            guard let data = try store.journal(step), var j = try? RotationJournal.parse(data, hub: pin.hub) else { continue }
            let c = collections[j.cid], note = flightNote(j.cid)
            let announced = c?.announced.contains(j.id) == true
            let headIsParent = c?.head == j.parent
            if announced {
                j.step = max(j.step, 4)
            } else if note?.statement == j.id && headIsParent {
                // Its own note is in home, whatever the journal says: an append whose answer was lost may have landed.
                if !j.noteWritten { j.noteWritten = true; j.step = max(j.step, 3) }
            } else if !j.noteWritten {
                // Nothing but the journal left the device; usable only if nothing moved meanwhile.
                if c == nil || note != nil || !headIsParent {
                    try await clearOwnNote(j.cid, statement: j.id)
                    try dropRotation(j.cid); failure = failure ?? ChangeError.superseded; continue
                }
            } else {
                // Discarded or overtaken: a draft may always be dropped.
                try await destroyChannel(Tandem.collectionTag(key: j.key, hub: pin.hub, epoch: j.id))
                try await clearOwnNote(j.cid, statement: j.id)
                try dropRotation(j.cid)
                failure = failure ?? ChangeError.superseded
                continue
            }
            try store.setJournal(step, try j.json())
            do { try await runRotation(j) } catch { failure = failure ?? error }
        }
        if let failure { throw failure }
    }

    // MARK: Another device's change (§11.7)

    /// Finish the change another device of this owner started, the one the user was shown, if its statement and
    /// inventory are on the hub: "Finishing a change started on Bob's iPhone".
    public func finish(list cid: Bytes, seen: FlightNote) async throws {
        try await sync()
        guard let note = flightNote(cid), note.statement == seen.statement else { throw ChangeError.noteChanged }
        guard let c = collections[cid], let st = c.statements[note.statement], let old = c.epoch(of: note.parent),
              st.statement.commit == Tandem.commit(note.key), let inv = inventory(note.statement) else { throw ChangeError.cantFinish }
        let j = RotationJournal(cid: cid, parent: note.parent, oldEpoch: old, statement: st.write, id: note.statement, key: note.key,
                                inventory: try inv.bytes(), what: note.what, step: 3, noteWritten: true)
        // What the inventory lists and the new channel lacks, from this device's replica (§11.7).
        let tag = Tandem.collectionTag(key: note.key, hub: pin.hub, epoch: note.statement)
        var inNew = Set<Bytes>()
        for m in try store.messages(channel: tag) { inNew.formUnion(try store.contents(message: m).map(\.write)) }
        var upload: [(String, Bytes)] = []
        for (rid, ids) in inv.entries {
            let writes = ids.filter { !inNew.contains($0) }.compactMap { id in c.records[rid]?.kept.first { $0.id == id }?.write }
            guard !writes.isEmpty else { continue }
            upload.append((tag, try Envelope.seal(key: note.key, hub: pin.hub, kind: 1, tag: tag,
                                                  plaintext: try recordPayload(record: rid, writes: writes))))
        }
        try store.transaction {
            try store.setJournal("rotate." + cid.hex, try j.json())
            try store.setJournal("rotate." + cid.hex + ".upload", Self.pack(upload))
        }
        try await runRotation(j)
    }

    /// Discard the change another device of this owner started, the one the user was shown: its note and key leave
    /// home and what it uploaded is destroyed. Does nothing if the note names another change now.
    public func discard(list cid: Bytes, seen: FlightNote) async throws {
        try await sync()
        guard let note = flightNote(cid), note.statement == seen.statement else { throw ChangeError.noteChanged }
        try await destroyChannel(Tandem.collectionTag(key: note.key, hub: pin.hub, epoch: note.statement))
        try await setHome(membershipName(cid), field: "flight", value: FlightNote.none)
    }

    /// Notes left over from a crash between an announcement and its home update: settled without asking (§11.7).
    func settleAnnouncedNotes() async throws {
        for (cid, c) in collections {
            // Only a note whose key the announced statement commits to: anyone who can write to home can write a note.
            guard let note = flightNote(cid), c.announced.contains(note.statement),
                  c.statements[note.statement]?.statement.commit == Tandem.commit(note.key),
                  try store.journal("rotate." + cid.hex) == nil else { continue }
            try await settleHome(cid, statement: note.statement, key: note.key, inventory: inventory(note.statement))
        }
    }
}
