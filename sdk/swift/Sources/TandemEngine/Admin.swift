import Foundation
import Tandem

// The hub admin's other actions (§6.7, §6.8, §13.1): changing the hub password, cancelling an invitation, and the
// creator's recovery with the hub password.

/// A hub invitation this participant made, from its home record (§6.2).
public struct MadeInvitation: Equatable, Sendable {
    public let invite: Bytes, capability: ID
    public let label: String
    /// When it was made, by this device's clock.
    public let made: Date?
    /// The last four characters of the mailbox password it carried, to tell links apart.
    public let hint: String
    public let cancelled: Bool
}

extension Device {
    // MARK: The hub password (§6.7)

    /// Change the hub password with the unlocked management seed: a new capsule names every capsule it supersedes,
    /// and once it is on the hub the old ones are destroyed. Seconds and 1 GiB. The old password still opens any copy
    /// of an old capsule someone kept: this is not a reset (§6.8).
    public func changePassword(to password: String, ms: Bytes) async throws {
        try await sync()
        let manager = try DeviceKeys.manager(seed: ms)
        guard Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("password: another hub's key") }
        var named = Set(capsules.keys)
        for c in capsules.values { if case let .set(s)? = c.fields["supersedes"] { named.formUnion(s) } }
        let capsule = try Founder.capsule(password: password, pin: pin, managementSeed: ms, supersedes: Array(named))
        capsules[try capsule.id()] = capsule
        try store.putObject(capsule)
        try await publishDirectory([try capsule.json()])
        try await destroyDirectoryObjects(named)
    }

    /// Destroy the directory messages that carry these objects.
    func destroyDirectoryObjects(_ ids: Set<ID>) async throws {
        var doomed: [String] = []
        for (message, _) in try store.merged(channel: directoryTag) where try store.contents(message: message).contains(where: { ids.contains($0.write) }) {
            doomed.append(message)
        }
        guard !doomed.isEmpty else { return }
        try await hub.destroy(ids: doomed)
        try store.forgetMessages(doomed)
    }

    // MARK: Invitations (§13.1)

    /// The hub invitations this participant made, newest first.
    public func invitations() -> [MadeInvitation] {
        home.values.compactMap { r -> MadeInvitation? in
            guard let name = r.value(Bytes("_name".utf8)).map({ String(decoding: $0, as: UTF8.self) }), name.hasPrefix("hubinvite:"),
                  let invite = B64u.decode(String(name.dropFirst(10))), let capability = r.value(Bytes("capability".utf8)) else { return nil }
            let made = r.value(Bytes("made".utf8)).flatMap { Double(String(decoding: $0, as: UTF8.self)) }.map { Date(timeIntervalSince1970: $0 / 1000) }
            return MadeInvitation(invite: invite, capability: capability,
                                  label: r.value(Bytes("label".utf8)).map { String(decoding: $0, as: UTF8.self) } ?? "",
                                  made: made, hint: r.value(Bytes("hint".utf8)).map { String(decoding: $0, as: UTF8.self) } ?? "",
                                  cancelled: receipt(Control.inviteCancelled, about: capability))
        }.sorted { ($0.made ?? .distantPast) > ($1.made ?? .distantPast) }
    }

    /// Cancel an invitation (§10.8, receipt outcome 3): devices then refuse to join with it. Anyone already admitted
    /// through it stays until revoked.
    public func cancelInvitation(capability: ID, ms: Bytes) async throws {
        let manager = try DeviceKeys.manager(seed: ms)
        guard Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("cancel: another hub's key") }
        var receipt = ControlObject(kind: "receipt", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "outcome": .int(Control.inviteCancelled), "subject": .id(capability), "signer": .opt(nil)])
        try receipt.sign("sig", with: manager.signing)
        try await keepReceipt(receipt)
        changed(.people)
        flushEvents()
    }

    // MARK: The creator's recovery (§6.8)

    /// The creator, with every device and the keychain lost, recovers with the hub password on a new device: a fresh
    /// keychain is admitted to the creator's participant (the one exception to member authorization, §6.9) and the
    /// creator's home opens from the boxes sealed to the management key. Nobody else can be recovered this way.
    public static func recoverCreator(genesis: ControlObject, directoryKey: Bytes, password: String, keys: DeviceKeys, label: String,
                                      store: Store, hub: any PushStore) async throws -> Device {
        let device = try Device.open(genesis: genesis, directoryKey: directoryKey, keys: keys, store: store, hub: hub)
        try await device.sync()
        let ms = try await device.unlock(password: password)
        try await device.admitCreator(ms: ms, label: label)
        try await device.sync()
        return device
    }

    func admitCreator(ms: Bytes, label: String) async throws {
        guard me == nil else { throw TandemError.invalid("recover: this device is already on the hub") }
        let manager = try DeviceKeys.manager(seed: ms)
        guard let keys, Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("recover: another hub's key") }
        let sign = Bytes(keys.signing.publicKey.rawRepresentation), agree = Bytes(keys.agreement.publicKey.rawRepresentation)
        var request = ControlObject(kind: "request", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "participant": .id(pin.creator), "sign": .id(sign), "agree": .id(agree), "label": .text(Bytes(label.utf8)),
            "how": .int(Control.creatorRecovery), "authority": .opt(nil), "enrollment": .id(Bytes(repeating: 0, count: 16))])
        try request.sign("self", with: keys.signing)
        var certificate = ControlObject(kind: "certificate", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "request": .id(try request.id()), "issued": .set(registry.revocationIDs.sorted { $0.lexicographicallyPrecedes($1) })],
            extras: ["body": .object(request)])
        try certificate.sign("sig", with: manager.signing)
        try registry.add(certificate)
        guard registry.certificateValidity(try certificate.id()).isValid else { throw TandemError.invalid("recover: not accepted") }
        let snapshot = try registry.currentSnapshot(of: pin.creator)
        try registry.add(snapshot)
        try store.transaction { for o in [certificate, snapshot] { try store.putObject(o) } }
        try await publishDirectory([try certificate.json(), try snapshot.json()])
        try adopt(participant: pin.creator, certificate: try certificate.id(), auth: try snapshot.id())
        // The creator's home keys, from the boxes sealed to the management agreement key.
        for n in notices where n.type == "home-recovery" {
            guard let epoch = n["epoch"], let box = n["bytes"],
                  let key = try? Control.openHomeRecovery(manager.agreement, hub: pin.hub, epoch: epoch, box: box) else { continue }
            try offerHomeKey(epoch, key)   // anyone can seal a box to MKa: the epoch's start decides
        }
    }
}
