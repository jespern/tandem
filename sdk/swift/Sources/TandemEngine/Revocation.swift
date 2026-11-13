import Foundation
import Tandem

// Removing a device (§6.4): the admin signs a revocation; every surviving device of that participant
// starts a new home epoch without it (§10.8). List owners rotate their lists (§11.6).

extension Device {
    // MARK: The admin

    /// Revoke an admitted keychain with the unlocked management seed (§6.4 step 1). Every other admitted keychain of
    /// that participant is retained, so its devices carry on; the target's keys are retired for good.
    public func revoke(certificate target: ID, ms: Bytes) async throws {
        try await sync()
        let manager = try DeviceKeys.manager(seed: ms)
        guard Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("revoke: another hub's key") }
        guard let t = registry.certificate(target), registry.certificateValidity(target).isValid
        else { throw TandemError.invalid("revoke: no valid certificate with that ID") }
        let retained = registry.allCertificates.filter {
            $0.participant == t.participant && $0.id != target && $0.keys != t.keys && registry.activity($0.id).isValid
        }.map(\.id).sorted { $0.lexicographicallyPrecedes($1) }
        var revocation = ControlObject(kind: "revocation", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "target": .id(target), "retained": .set(retained)])
        try revocation.sign("sig", with: manager.signing)
        try registry.add(revocation)
        let snapshot = try registry.currentSnapshot(of: t.participant)
        try registry.add(snapshot)
        try store.transaction { for o in [revocation, snapshot] { try store.putObject(o) } }
        try await publishDirectory([try revocation.json(), try snapshot.json()])
        try await followUp()
    }

    /// Whether this device's own keychain has been revoked. It then stops writing and receives no new keys.
    public var isRevoked: Bool {
        guard let me, case .invalid = registry.activity(me.certificate) else { return false }
        return registry.certificateValidity(me.certificate).isValid
    }

    // MARK: Keeping this device's authorization current

    /// Act under the current snapshot of this participant, publishing it if the directory lacks it, so writes
    /// extend any floor a later epoch pins (§6.9, §8.3).
    func refreshAuth() async throws {
        guard let me else { return }
        let current = try registry.currentSnapshot(of: me.participant)
        let id = try current.id()
        guard id != me.auth else { return }
        try registry.add(current)
        guard registry.authorizes(snapshot: id, certificate: me.certificate).isValid else { return }
        _ = try await publishedSnapshot(of: me.participant)
        try adopt(participant: me.participant, certificate: me.certificate, auth: id)
    }

    /// Valid revocations of this participant's keychains.
    func revocationsOfMine(_ me: Me) -> Set<ID> {
        Set(registry.revs.values.filter { r in
            registry.certificate(r.target)?.participant == me.participant && registry.revocationValidity(r.id).isValid
        }.map(\.id))
    }

    /// A current home head whose policy predates a revocation of this participant's keychains (§10.8).
    func homeNeedsRotation(_ me: Me) -> Bool {
        let mine = revocationsOfMine(me)
        guard !mine.isEmpty else { return false }
        return homeHeads.contains { head in
            guard case let .id(auth)? = homeEpochs[head]?.fields["auth"], let s = registry.snapshots[auth] else { return false }
            return !mine.isSubset(of: Set(s.revocations))
        }
    }

    // MARK: Rekeying home (§6.4 step 2, §10.8)

    /// Start a new home epoch after every current head, sealed only to this participant's admitted keychains, carrying
    /// the home's records through a fixed inventory. Joins concurrent heads. Journalled before anything leaves.
    public func rotateHome() async throws {
        try await refreshAuth()
        guard let me, !homeHeads.isEmpty, registry.activity(me.certificate).isValid
        else { throw TandemError.invalid("home: nothing to rotate, or this keychain is revoked") }
        let heads = homeHeads
        let n = (heads.map(epochNumber).max() ?? 0) + 1
        let key = Self.random(32)
        let recipients = registry.allCertificates.filter { $0.participant == me.participant && registry.activity($0.id).isValid }
        var entries: [Bytes: Set<Bytes>] = [:]
        var carried: [(record: Bytes, writes: [Write])] = []
        for (rid, r) in home.sorted(by: { $0.key.lexicographicallyPrecedes($1.key) }) where !r.kept.isEmpty {
            entries[rid, default: []].formUnion(r.kept.map(\.id))
            carried.append((rid, r.kept.map(\.write)))
        }
        // Nothing listed is dropped unseen (§8.3): what a head's inventory lists and this device doesn't hold goes on.
        for h in heads {
            for (rid, ids) in inventory(h)?.entries ?? [:] {
                for id in ids where try !store.hasWrite(id) { entries[rid, default: []].insert(id) }
            }
        }
        let inventory = Inventory(entries.mapValues { Array($0) })
        let start = try Founder.homeEpoch(pin: pin, participant: me.participant, n: n, parents: heads, homeKey: key,
                                          recipients: recipients.map(\.id), signer: me.certificate, auth: me.auth,
                                          inventory: inventory, keys: me.keys)
        let epoch = try start.id()
        let tag = Tandem.homeTag(key: key, hub: pin.hub, uid: me.participant, epoch: epoch)
        let inbox = Tandem.inboxTag(uid: me.participant, hub: pin.hub)

        var out: [(String, Bytes)] = []
        for cert in recipients {
            out.append((inbox, try Envelope.sealInbox(hub: pin.hub, uid: me.participant, agreement: cert.agree,
                                                      plaintext: Founder.homeKeyNotice(epoch: epoch, key: key).json())))
        }
        if me.participant == pin.creator {
            let box = try Control.sealHomeRecovery(managerAgreement: pin.agree, hub: pin.hub, epoch: epoch, homeKey: key)
            out.append((directoryTag, try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 3, tag: directoryTag,
                plaintext: Notice(type: "home-recovery", [("epoch", epoch), ("bytes", box)]).json())))
        }
        // The new channel: every earlier start (so a device paired later can follow the chain), the inventory, the
        // carried writes with their original signatures, and the start itself last.
        func seal(_ kind: UInt8, _ plaintext: Bytes) throws -> (String, Bytes) {
            (tag, try Envelope.seal(key: key, hub: pin.hub, kind: kind, tag: tag, plaintext: plaintext))
        }
        for (_, e) in homeEpochs.sorted(by: { epochNumber($0.key) < epochNumber($1.key) }) { out.append(try seal(3, try e.json())) }
        for fragment in try Self.fragments(inventory, epoch: epoch) { out.append(try seal(1, fragment)) }
        for (record, writes) in carried { out.append(try seal(1, try recordPayload(record: record, writes: writes))) }
        out.append(try seal(3, try start.json()))
        // The announcement in every parent's channel.
        for parent in heads {
            guard let k = homeKeys[parent] else { continue }
            let t = Tandem.homeTag(key: k, hub: pin.hub, uid: me.participant, epoch: parent)
            out.append((t, try Envelope.seal(key: k, hub: pin.hub, kind: 3, tag: t, plaintext: try start.json())))
        }

        let step = "home." + epoch.hex
        try store.transaction {
            try store.setJournal(step, Self.pack(out))
            try store.putObject(start)
            try store.setValue("home.key." + epoch.hex, key)
            try store.setValue("inv." + epoch.hex, try inventory.bytes())
        }
        homeEpochs[epoch] = start
        homeKeys[epoch] = key
        inventories[epoch] = inventory
        try await export(out, step: step)
    }

    // MARK: Home writes carried by an inventory

    /// Whether `ancestor` is reachable from `epoch` through `parents`; pending while a start on the way is missing.
    func homeAncestry(_ ancestor: ID, of epoch: ID) -> Validity {
        var queue = [epoch], seen: Set<ID> = []
        while let e = queue.popLast() {
            guard seen.insert(e).inserted else { continue }
            guard let start = homeEpochs[e], case let .set(parents)? = start.fields["parents"] else { return .pending([e]) }
            if parents.contains(ancestor) { return .valid }
            queue += parents
        }
        return .invalid("not an earlier home epoch")
    }
}
