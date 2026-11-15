import Foundation
import Tandem

/// What `copy(list:)` did: the new list, who it was shared with again, and who is left for the user to decide on.
public struct ListCopy: Sendable, Equatable {
    /// The new list, owned by this participant.
    public let list: Bytes
    /// Shared again with the role they had: listed the same way in every version of the old roster.
    public let shared: [ID]
    /// Not shared: in only some versions of the old roster, with different roles in them, or without an admitted
    /// device now. The app asks the user, then calls `share`.
    public let undecided: [ID]
}

extension CollectionState {
    /// The ends of the counted roster: one for a live collection, two or more for a dead one (§9.4).
    var tips: [ID] {
        statements.keys.filter { counts($0) && successors(of: $0).isEmpty }.sorted { $0.lexicographicallyPrecedes($1) }
    }
}

extension Device {
    static let copyOf = Bytes("copyof".utf8)

    // MARK: Copying a list (§11.8)

    /// Copy a list into a new one this participant owns, and share it again: the way out of a dead list (§9.4), or of
    /// one whose owner can't change its sharing any more (§11.8). The records go over as this device holds them, as
    /// writes of this participant's own; of concurrent values only the displayed one. The new list's `_meta` names the
    /// old one (`copyof`), so members see where it came from (`copyOf(list:)`). Members listed the same way at every end
    /// of the old roster get the same role (an old owner becomes a writer); the rest are `undecided`.
    ///
    /// Calling it again, here or on another device of this participant once it has synced, finishes the same copy
    /// rather than starting another. The old list stays as it is, read-only if dead.
    public func copy(list cid: Bytes) async throws -> ListCopy {
        try await sync()
        guard let me, let old = collections[cid], old.genesis != nil, following(cid)
        else { throw TandemError.invalid("copy: not a list this participant follows") }
        // Cross-scope manifests would depend on chunks the source can collect.
        // Refuse before creating a partial copy until files can be re-encrypted.
        guard !old.records.values.contains(where: { r in
            !r.deleted && r.kept.contains { $0.write.op == .assign && Self.isBlobField($0.write.field) }
        }) else { throw TandemError.invalid("copy: copying lists with file attachments is not implemented") }
        let mark = "copy." + cid.hex
        let copy: Bytes
        if let earlier = try store.value(mark), collections[earlier] != nil {
            copy = earlier
        } else if let earlier = collections.values.first(where: { $0.owner == me.participant && following($0.id)
                                                          && $0.records[Self.meta]?.value(Self.copyOf) == cid })?.id {
            copy = earlier
        } else {
            copy = try await createList(name: self.name(list: cid) ?? "")
        }
        try store.setValue(mark, copy)

        // `_meta`: the old one's app fields, and where this list came from.
        let meta = collections[copy]?.records[Self.meta]
        if meta?.value(Self.copyOf) != cid {
            var changes: [(field: Bytes, op: Op, value: Bytes, element: Bytes?)] = []
            if let m = old.records[Self.meta] {
                let item = Item(id: Self.meta, record: m)
                for f in item.fields where f != "name" && f != "copyof" { changes += Self.changes(item, f) }
            }
            changes.append((Self.copyOf, .assign, cid, nil))
            try await writeRecord(copy, record: Self.meta, changes)
        }
        // The records, under their own IDs, unless already there (an earlier call that stopped part way).
        for item in items(list: cid) where collections[copy]?.records[item.id] == nil {
            let changes = item.fields.flatMap { Self.changes(item, $0) }
            if !changes.isEmpty { try await writeRecord(copy, record: item.id, changes) }
        }

        // The members every end of the old roster agrees on.
        let ends = old.tips.compactMap { old.statements[$0]?.statement.members }
        let everyone = Set(ends.flatMap { $0.map(\.uid) }).subtracting([me.participant])
        var shared: [ID] = [], undecided: [ID] = []
        for uid in everyone.sorted(by: { $0.lexicographicallyPrecedes($1) }) {
            let listed = ends.map { members in members.first { $0.uid == uid } }
            let roles = listed.map { m -> Statement.Role? in m.map { $0.role == .owner ? .writer : $0.role } }
            guard let role = roles.first ?? nil, roles.allSatisfy({ $0 == role }) else { undecided.append(uid); continue }
            let member = listed.first ?? nil
            do {
                try await share(list: copy, with: uid, role: role, name: String(decoding: member?.name ?? [], as: UTF8.self))
                shared.append(uid)
            } catch {
                undecided.append(uid)   // no admitted device left
            }
        }
        return ListCopy(list: copy, shared: shared, undecided: undecided)
    }

    /// The list this one was copied from, if its owner was in that list: an app can say "Bob continued House" on the
    /// offer (§11.2). The claim is the copy owner's; the old list must be one this device knows.
    public func copyOf(list cid: Bytes) -> Bytes? {
        guard let c = collections[cid], let owner = c.owner, let from = c.records[Self.meta]?.value(Self.copyOf),
              let old = collections[from], old.statements.values.contains(where: { $0.statement.members.contains { $0.uid == owner } })
        else { return nil }
        return from
    }

    /// The writes that recreate one field of an item: its displayed value, or its elements.
    static func changes(_ item: Item, _ field: String) -> [(field: Bytes, op: Op, value: Bytes, element: Bytes?)] {
        let f = Bytes(field.utf8)
        return (item.value(field).map { [(f, Op.assign, $0, nil)] } ?? []) + item.elements(field).map { (f, .add, [], $0) }
    }
}
