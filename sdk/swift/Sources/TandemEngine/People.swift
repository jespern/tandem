import Foundation
import Tandem

// The hub's people (§6.6): participants from the registry, with the names they gave themselves in their directory
// profiles (§10.8), and their devices underneath.

/// One admitted keychain of a participant, as the directory shows it.
public struct DeviceInfo: Equatable, Sendable {
    public let certificate: ID
    /// The label the device gave itself when it was admitted.
    public let label: String
    /// Admitted now: valid and not revoked.
    public let active: Bool
    /// This device's own keychain.
    public let isThisDevice: Bool
}

/// A participant of the hub (§6.6).
public struct Person: Equatable, Sendable {
    public let id: ID
    /// The name from their profile; nil until they set one. Display text only: it never selects anyone.
    public let name: String?
    public let isMe: Bool
    /// The hub's creator, whom the genesis names.
    public let isCreator: Bool
    public let devices: [DeviceInfo]
}

extension Device {
    var directoryScope: Bytes { Control.directoryScope(hub: pin.hub) }

    static func profileRecord(_ participant: ID) -> Bytes { Tandem.namedID(Bytes(("profile:" + B64u.encode(participant)).utf8)) }

    /// Everyone on the hub with a valid certificate, by name, then ID.
    public func people() -> [Person] {
        let byParticipant = Dictionary(grouping: registry.allCertificates.filter { registry.certificateValidity($0.id).isValid },
                                       by: \.participant)
        return byParticipant.map { uid, certs in
            Person(id: uid, name: name(of: uid), isMe: uid == me?.participant, isCreator: uid == pin.creator,
                   devices: certs.map { c in
                       DeviceInfo(certificate: c.id, label: String(decoding: c.label, as: UTF8.self),
                                  active: registry.activity(c.id).isValid, isThisDevice: c.id == me?.certificate)
                   }.sorted { $0.label < $1.label })
        }.sorted { ($0.name ?? "", $0.id.hex) < ($1.name ?? "", $1.id.hex) }
    }

    /// A participant's name from their profile.
    public func name(of participant: ID) -> String? {
        profiles[Self.profileRecord(participant)]?.value(Bytes("name".utf8)).map { String(decoding: $0, as: UTF8.self) }
    }

    /// Set this participant's name in their directory profile: at most 256 bytes of UTF-8 (§10.8).
    public func setName(_ name: String) async throws {
        guard let me else { throw TandemError.invalid("device: not on this hub yet") }
        guard Bytes(name.utf8).count <= 256 else { throw TandemError.invalid("profile: a name is at most 256 bytes") }
        let scope = directoryScope, record = Self.profileRecord(me.participant)
        var current = try profiles[record] ?? Record(hub: pin.hub, scope: scope, id: record)
        var writes: [Write] = []
        func sign(_ field: String, _ value: Bytes) throws {
            let v = try current.vectors(op: .assign, field: Bytes(field.utf8), epoch: pin.genesis, epochNumber: 0)
            var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: scope, record: record,
                          field: Bytes(field.utf8), op: .assign, value: value, author: me.participant, dot: try nextDot(me),
                          ref: pin.genesis, context: v.context, base: v.base)
            try w.sign(with: me.keys.signing)
            try current.merge([try Entry(w, epoch: pin.genesis, epochNumber: 0)])
            writes.append(w)
        }
        if current.value(Bytes("_name".utf8)) == nil { try sign("_name", Bytes(("profile:" + B64u.encode(me.participant)).utf8)) }
        try sign("name", Bytes(name.utf8))
        profiles[record] = current
        try store.transaction {
            for w in writes {
                try store.putWrite(id: try w.id(), scope: scope, record: record, epoch: pin.genesis,
                                   payload: try recordPayload(record: record, writes: [w]))
            }
        }
        let envelope = try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 1, tag: directoryTag,
                                         plaintext: try recordPayload(record: record, writes: writes))
        let id = try await hub.append(channel: directoryTag, body: envelope)
        try store.noteMessage(id: id, channel: directoryTag, hash: H(envelope))
        try store.markApplied(id)
    }

    /// Profile writes in the directory channel: only an admitted device of exactly that participant writes its
    /// profile, under the genesis (§10.8).
    func applyProfile(_ plaintext: Bytes) throws -> Outcome {
        guard case let .record(state)? = try? Kind1.parse(plaintext, hub: pin.hub, scope: directoryScope) else { return .ignored }
        var entries: [Entry] = []
        for w in state.writes {
            guard w.ref == pin.genesis, state.record == Self.profileRecord(w.author) else { continue }
            switch registry.authorizes(snapshot: w.authorization, certificate: w.certificate) {
            case .pending: return .waiting
            case .invalid: continue
            case .valid: break
            }
            guard let cert = registry.certificate(w.certificate), cert.participant == w.author, registry.activity(cert.id).isValid,
                  (try? w.verify(publicKey: cert.sign)) != nil else { continue }   // a revoked keychain renames nobody
            if w.field == Bytes("name".utf8), w.value.count > 256 { continue }
            entries.append(try Entry(w, epoch: pin.genesis, epochNumber: 0))
        }
        guard !entries.isEmpty else { return .ignored }
        var r = try profiles[state.record] ?? Record(hub: pin.hub, scope: directoryScope, id: state.record)
        try r.merge(entries)
        profiles[state.record] = r
        try store.transaction {
            for e in entries {
                try store.putWrite(id: e.id, scope: directoryScope, record: state.record, epoch: pin.genesis,
                                   payload: try recordPayload(record: state.record, writes: [e.write]))
            }
        }
        return .applied
    }
}
