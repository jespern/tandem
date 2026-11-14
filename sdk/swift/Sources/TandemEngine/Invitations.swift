import CryptoKit
import Foundation
import Tandem

/// What a hub invitation link carries (§10.8, §13.1): connection details, the pins, the directory key,
/// the bearer secret and the capability. Sensitive: never log it or put it in the directory.
public struct InvitationLink: Sendable {
    public let hub: Bytes, genesis: ID, directoryKey: Bytes, account: MailboxAccount, secret: Bytes
    public let invitation: ControlObject

    public func json() throws -> Bytes {
        let inv = try invitation.json()
        return JSONWriter.object { w in
            w.member("v", int: 2)
            w.member("hub", b64u: hub)
            w.member("genesis", b64u: genesis)
            w.member("hk", b64u: directoryKey)
            w.member("mail", json: account.json())
            w.member("s", b64u: secret)
            w.member("invitation", json: inv)
        }
    }

    public static func parse(_ data: Bytes) throws -> InvitationLink {
        let j = try JSON.parse(data)
        guard case .int(2)? = j["v"], let mail = j["mail"], let inv = j["invitation"]
        else { throw TandemError.invalid("invitation: not a v2 link") }
        let account = try MailboxAccount.parse(mail)
        return InvitationLink(hub: try j.bytes("hub", count: 32), genesis: try j.bytes("genesis", count: 32),
                              directoryKey: try j.bytes("hk", count: 32), account: account, secret: try j.bytes("s", count: 32),
                              invitation: try ControlObject.parse(inv, expect: "invitation"))
    }
}

extension Device {
    // MARK: The admin's side (§13.1)

    /// Create a new-member invitation with the unlocked management seed `ms`. Publishes the capability, records
    /// it privately in home, and returns the link to hand over, by QR code or a link that opens the app.
    public func invite(label: String, account: MailboxAccount, ms: Bytes, validFor seconds: TimeInterval = 7 * 24 * 3600)
        async throws -> InvitationLink {
        let manager = try DeviceKeys.manager(seed: ms)
        guard Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("invite: another hub's key") }
        let secret = Self.random(32), inviteID = Self.random(16)
        let key = try Control.inviteKey(secret: secret, hub: pin.hub, invite: inviteID)
        var capability = ControlObject(kind: "invitation", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "key": .id(Bytes(key.publicKey.rawRepresentation)), "invite": .id(inviteID),
            "expires": .int(UInt64((Date().timeIntervalSince1970 + seconds) * 1000)), "label": .text(Bytes(label.utf8))])
        try capability.sign("sig", with: manager.signing)
        try await publishDirectory([try capability.json()])
        try registry.add(capability)
        try store.putObject(capability)
        let record = "hubinvite:" + B64u.encode(inviteID)
        try await setHome(record, field: "label", value: Bytes(label.utf8))
        try await setHome(record, field: "capability", value: try capability.id())
        try await setHome(record, field: "made", value: Bytes(String(UInt64(Date().timeIntervalSince1970 * 1000)).utf8))
        try await setHome(record, field: "hint", value: Bytes(account.password.suffix(4).utf8))
        return InvitationLink(hub: pin.hub, genesis: pin.genesis, directoryKey: directoryKey, account: account, secret: secret,
                              invitation: capability)
    }

    func publishDirectory(_ messages: [Bytes]) async throws {
        for plaintext in messages {
            let envelope = try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 3, tag: directoryTag, plaintext: plaintext)
            let id = try await hub.append(channel: directoryTag, body: envelope)
            try store.noteMessage(id: id, channel: directoryTag, hash: H(envelope))
            try store.markApplied(id)
        }
    }

    // MARK: The new member's side (§6.1, §13.1)

    /// Join a hub through an invitation: find and pin its genesis, admit this keychain under the capability, and
    /// start a home. Needs no hub password; the capability is the admin's preauthorization.
    public static func join(link: InvitationLink, keys: DeviceKeys, label: String, store: Store, hub: any PushStore) async throws -> Device {
        let inv = link.invitation
        guard inv.fields["hub"] == .id(link.hub), inv.fields["genesis"] == .id(link.genesis), inv.verify("sig", publicKey: link.hub),
              case let .int(expires)? = inv.fields["expires"], expires > UInt64(Date().timeIntervalSince1970 * 1000),
              case let .id(inviteID)? = inv.fields["invite"], case let .id(capKey)? = inv.fields["key"]
        else { throw TandemError.invalid("invitation: not valid for this hub, or expired") }
        let inviteKey = try Control.inviteKey(secret: link.secret, hub: link.hub, invite: inviteID)
        guard Bytes(inviteKey.publicKey.rawRepresentation) == capKey else { throw TandemError.invalid("invitation: the secret doesn't match") }

        let genesis = try await findGenesis(hub: link.hub, genesis: link.genesis, directoryKey: link.directoryKey, on: hub)
        let device = try Device.open(genesis: genesis, directoryKey: link.directoryKey, keys: keys, store: store, hub: hub)
        try await device.sync()
        // Joined already with this store, perhaps half way: the sync above finished the journalled admission.
        if await device.me != nil { return device }
        guard await !device.receipt(Control.inviteCancelled, about: try inv.id()) else {
            throw TandemError.invalid("invitation: the admin cancelled it")
        }
        try await device.admit(invitation: inv, inviteKey: inviteKey, label: label)
        return device
    }

    /// Register, request admission with the invite key, certify it with the same key (§10.8), and start a home.
    func admit(invitation inv: ControlObject, inviteKey: Curve25519.Signing.PrivateKey, label: String) async throws {
        guard let keys, me == nil else { throw TandemError.invalid("invitation: this device is already on the hub") }
        let enrollment = Self.random(16)
        let sign = Bytes(keys.signing.publicKey.rawRepresentation), agree = Bytes(keys.agreement.publicKey.rawRepresentation)
        let participant = try Control.registrationID(hub: pin.hub, sign: sign, agree: agree, enrollment: enrollment)
        var request = ControlObject(kind: "request", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "participant": .id(participant), "sign": .id(sign), "agree": .id(agree), "label": .text(Bytes(label.utf8)),
            "how": .int(Control.invited), "authority": .opt(try inv.id()), "enrollment": .id(enrollment)])
        try request.sign("self", with: keys.signing)
        try request.sign("approver", with: inviteKey)
        let issued = registry.revocationIDs.sorted { $0.lexicographicallyPrecedes($1) }
        var certificate = ControlObject(kind: "certificate", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "request": .id(try request.id()), "issued": .set(issued)], extras: ["body": .object(request)])
        try certificate.sign("sig", with: inviteKey)
        try registry.add(certificate)
        guard registry.certificateValidity(try certificate.id()).isValid else { throw TandemError.invalid("invitation: not accepted") }
        let snapshot = try registry.currentSnapshot(of: participant)
        try registry.add(snapshot)
        let homeKey = Self.random(32)
        let epoch = try Founder.homeEpoch(pin: pin, participant: participant, n: 0, parents: [], homeKey: homeKey,
                                          recipients: [try certificate.id()], signer: try certificate.id(), auth: try snapshot.id(), keys: keys)
        // Every envelope of the admission, journalled with this device's identity before any leaves (§6.1): a join that
        // fails half way is finished by the next sync, or by joining again with the same store.
        let homeTag = Tandem.homeTag(key: homeKey, hub: pin.hub, uid: participant, epoch: try epoch.id())
        let inbox = Tandem.inboxTag(uid: participant, hub: pin.hub)
        var out = try [certificate, snapshot].map {
            (directoryTag, try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 3, tag: directoryTag, plaintext: try $0.json()))
        }
        out.append((homeTag, try Envelope.seal(key: homeKey, hub: pin.hub, kind: 3, tag: homeTag, plaintext: try epoch.json())))
        out.append((inbox, try Envelope.sealInbox(hub: pin.hub, uid: participant, agreement: agree,
                                                  plaintext: Founder.homeKeyNotice(epoch: try epoch.id(), key: homeKey).json())))
        try store.transaction {
            try store.setJournal("admit", Self.pack(out))
            for o in [certificate, snapshot, epoch] { try store.putObject(o) }
            try store.setValue("home.key." + (try epoch.id()).hex, homeKey)
        }
        homeEpochs[try epoch.id()] = epoch
        homeKeys[try epoch.id()] = homeKey
        try adopt(participant: participant, certificate: try certificate.id(), auth: try snapshot.id())
        try await export(out, step: "admit")
    }
}
