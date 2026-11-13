import Foundation
import Tandem

/// One value of a field as merged (§9.2): the bytes, who wrote them and when their device says it did.
public struct FieldValue: Equatable, Sendable {
    public let value: Bytes
    /// The participant who wrote it.
    public let author: ID
    /// The writing device's clock, Unix milliseconds: a claim, not an order.
    public let millis: UInt64
    /// The write's ID, stable across devices.
    public let write: ID
}

/// A record of a list as this device holds it: an immutable snapshot (§9).
public struct Item: Sendable {
    public let id: Bytes
    let record: Record

    /// Whether a delete is among its writes: a deleted record stays deleted (§9.2).
    public var deleted: Bool { record.deleted }

    /// The displayed value of a field: of concurrent values, the one §9.2 orders first.
    public func value(_ field: String) -> Bytes? { record.value(Bytes(field.utf8)) }

    /// The displayed value as UTF-8 text.
    public func text(_ field: String) -> String? { value(field).map { String(decoding: $0, as: UTF8.self) } }

    /// Every value of a field that no other replaced, displayed one first, at most 16 (§9.2). More than one is a
    /// conflict the app may show (Liszto keeps both texts of a note); writing the field again settles it.
    public func values(_ field: String) -> [FieldValue] {
        record.siblings(Bytes(field.utf8)).prefix(16).map {
            FieldValue(value: $0.write.value, author: $0.write.author, millis: $0.write.dot.millis, write: $0.id)
        }
    }

    /// The elements of a set field (§9.2, add wins).
    public func elements(_ field: String) -> [Bytes] { record.elements(Bytes(field.utf8)) }

    /// The names of the fields with a value or an element, app fields only (no `_` names).
    public var fields: [String] {
        Set(record.kept.filter { $0.write.op == .assign || $0.write.op == .add }.map { String(decoding: $0.write.field, as: UTF8.self) })
            .filter { !$0.hasPrefix("_") }.sorted()
    }

    /// Who first wrote this record, as far as the writes kept show, and who wrote last: for "Alice added" and
    /// "completed by Bob" (Liszto §7.5). Claims by the writers' clocks.
    public var createdBy: ID? { record.kept.min { ($0.write.dot.millis, $0.write.dot.counter) < ($1.write.dot.millis, $1.write.dot.counter) }?.write.author }
    public var lastModifiedBy: ID? { record.kept.max { ($0.write.dot.millis, $0.write.dot.counter) < ($1.write.dot.millis, $1.write.dot.counter) }?.write.author }
}

extension Device {
    /// One record of a list, deleted or not; nil if this device holds no write of it.
    public func item(list cid: Bytes, id: Bytes) -> Item? {
        collections[cid]?.records[id].map { Item(id: id, record: $0) }
    }

    /// The list's records that aren't deleted, without the named ones, by ID.
    public func items(list cid: Bytes) -> [Item] {
        (collections[cid]?.records ?? [:]).filter { $0.key != Self.meta && !$0.value.deleted }
            .sorted { $0.key.lexicographicallyPrecedes($1.key) }.map { Item(id: $0.key, record: $0.value) }
    }

    /// Add an element to a set field, such as a tag (§9.2).
    public func add(list cid: Bytes, record: Bytes, field: String, element: Bytes) async throws {
        try await writeRecord(cid, record: record, field: Bytes(field.utf8), op: .add, element: element)
    }

    /// Remove an element from a set field. A concurrent add on another device wins (§9.2).
    public func remove(list cid: Bytes, record: Bytes, field: String, element: Bytes) async throws {
        try await writeRecord(cid, record: record, field: Bytes(field.utf8), op: .remove, element: element)
    }

    /// The list's name, from its `_meta` record (§7.3).
    public func name(list cid: Bytes) -> String? {
        collections[cid]?.records[Self.meta]?.value(Bytes("name".utf8)).map { String(decoding: $0, as: UTF8.self) }
    }

    /// Rename a list; only its owner may (§8.1).
    public func rename(list cid: Bytes, to name: String) async throws {
        try await setField(list: cid, record: Self.meta, field: "name", value: Bytes(name.utf8))
    }

    /// The list's owner, from its genesis.
    public func owner(list cid: Bytes) -> ID? { collections[cid]?.owner }

    /// The members the roster lists now, with their roles and display names.
    public func members(list cid: Bytes) -> [Statement.Member] {
        guard let c = collections[cid], let head = c.head else { return [] }
        return c.statements[head]?.statement.members ?? []
    }
}
