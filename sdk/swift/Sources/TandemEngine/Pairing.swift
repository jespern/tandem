import CryptoKit
import Foundation
import Tandem

// Adding a device that doesn't share the keychain (§6.3, §10.8): the member's phone stages a delegation,
// a package and a gate box and shows a link; the new device claims through it; an admin approves with
// the hub password and releases the gate secret to exactly that device.

/// What a pairing link carries (§10.8): connection details, the pins, the directory key, `L`, the token and
/// the participant the new device joins. Sensitive: never log it, put it in the directory, or show it to an admin.
public struct PairingLink: Sendable {
    public let hub: Bytes, genesis: ID, directoryKey: Bytes, account: MailboxAccount
    public let secret: Bytes, token: Bytes, participant: ID

    public func json() -> Bytes {
        JSONWriter.object { w in
            w.member("v", int: 2)
            w.member("hub", b64u: hub)
            w.member("genesis", b64u: genesis)
            w.member("hk", b64u: directoryKey)
            w.member("mail", json: account.json())
            w.member("l", b64u: secret)
            w.member("t", b64u: token)
            w.member("participant", b64u: participant)
        }
    }

    public static func parse(_ data: Bytes) throws -> PairingLink {
        let j = try JSON.parse(data)
        guard case .int(2)? = j["v"], let mail = j["mail"] else { throw TandemError.invalid("pairing: not a v2 link") }
        return PairingLink(hub: try j.bytes("hub", count: 32), genesis: try j.bytes("genesis", count: 32),
                           directoryKey: try j.bytes("hk", count: 32), account: try MailboxAccount.parse(mail),
                           secret: try j.bytes("l", count: 32), token: try j.bytes("t", count: 16),
                           participant: try j.bytes("participant", count: 32))
    }
}

/// A claim an admin device holds (§6.3 step 2): the new device's request, the delegation it claims under,
/// and the gate box. The admin never sees `L`.
public struct PairingClaim: Sendable {
    public let request: ControlObject, delegation: ControlObject, gate: Bytes

    public var id: ID { (try? request.id()) ?? [] }
    public var participant: ID { if case let .id(p)? = request.fields["participant"] { return p }; return [] }
    /// The label the new device gave itself.
    public var label: String { if case let .text(t)? = request.fields["label"] { return String(decoding: t, as: UTF8.self) }; return "" }
    /// The certificate of the member's device that made the link.
    public var source: ID { if case let .id(s)? = delegation.fields["source"] { return s }; return [] }
    public var expires: Date {
        if case let .int(ms)? = delegation.fields["expires"] { return Date(timeIntervalSince1970: TimeInterval(ms) / 1000) }
        return .distantPast
    }
    var delegationID: ID { (try? delegation.id()) ?? [] }
    var token: Bytes { if case let .id(t)? = delegation.fields["token"] { return t }; return [] }

    func json() throws -> Bytes {
        let r = try request.json(), d = try delegation.json()
        return JSONWriter.object { w in
            w.member("type", text: Bytes("pair-claim".utf8))
            w.member("request", json: r)
            w.member("delegation", json: d)
            w.member("gate", b64u: gate)
        }
    }

    static func parse(_ j: JSON) throws -> PairingClaim {
        guard let r = j["request"], let d = j["delegation"] else { throw TandemError.invalid("pair-claim") }
        return PairingClaim(request: try ControlObject.parse(r, expect: "request"),
                            delegation: try ControlObject.parse(d, expect: "delegation"), gate: try j.bytes("gate"))
    }
}

/// The new device's side of a claim in flight, kept until the admin's release arrives.
struct PendingPairing: Sendable {
    let secret: Bytes, token: Bytes, participant: ID, request: ID, delegation: ID, package: Bytes

    func json() -> Bytes {
        JSONWriter.object { w in
            for (name, value) in [("l", secret), ("t", token), ("participant", participant), ("request", request),
                                  ("delegation", delegation), ("package", package)] { w.member(name, b64u: value) }
        }
    }

    static func parse(_ data: Bytes) throws -> PendingPairing {
        let j = try JSON.parse(data)
        return PendingPairing(secret: try j.bytes("l", count: 32), token: try j.bytes("t", count: 16),
                              participant: try j.bytes("participant", count: 32), request: try j.bytes("request", count: 32),
                              delegation: try j.bytes("delegation", count: 32), package: try j.bytes("package"))
    }
}

extension Device {
    static var now: UInt64 { UInt64(Date().timeIntervalSince1970 * 1000) }

    // MARK: The member's phone (§6.3, "What the phone prepares")

    /// Stage a pairing for a device of this participant that doesn't share the keychain, and return the link to
    /// show it. The delegation, package and gate box are on the hub before the link exists.
    public func startPairing(label: String, account: MailboxAccount, validFor seconds: TimeInterval = 24 * 3600)
        async throws -> PairingLink {
        guard let me, let epoch = currentHomeEpoch, let homeKey = homeKeys[epoch]
        else { throw TandemError.invalid("pairing: this device has no home yet") }
        let token = Self.random(16), secret = Self.random(32), gateSecret = Self.random(32)
        let inviteKey = try Control.pairingInviteKey(link: secret, hub: pin.hub, token: token)
        let contents = JSONWriter.object { w in w.member("home", b64u: epoch); w.member("k", b64u: homeKey) }
        let package = try Control.sealPackage(link: secret, gate: gateSecret, hub: pin.hub, token: token, plaintext: contents)
        let gate = try Control.sealGate(managerAgreement: pin.agree, hub: pin.hub, token: token, participant: me.participant,
                                        secret: gateSecret)
        let expires = UInt64(max(0, Date().timeIntervalSince1970 + seconds) * 1000)
        var delegation = ControlObject(kind: "delegation", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "participant": .id(me.participant), "source": .id(me.certificate),
            "key": .id(Bytes(inviteKey.publicKey.rawRepresentation)), "token": .id(token), "expires": .int(expires),
            "package": .id(H(package)), "gate": .id(H(gate))])
        try delegation.sign("sig", with: me.keys.signing)
        try registry.add(delegation)
        try store.putObject(delegation)

        let key = Control.pairingKey(link: secret, hub: pin.hub, token: token)
        let tag = Control.pairingTag(link: secret, hub: pin.hub, token: token)
        for plaintext in [try delegation.json(), Notice(type: "package", [("bytes", package)]).json(),
                          Notice(type: "gate", [("bytes", gate)]).json()] {
            let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 3, tag: tag, plaintext: plaintext)
            let id = try await hub.append(channel: tag, body: envelope)
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
            try store.markApplied(id)
        }
        let record = "pairing:" + B64u.encode(token)
        try await setHome(record, field: "delegation", value: try delegation.id())
        try await setHome(record, field: "label", value: Bytes(label.utf8))
        try await setHome(record, field: "expires", value: Bytes(String(expires).utf8))
        return PairingLink(hub: pin.hub, genesis: pin.genesis, directoryKey: directoryKey, account: account, secret: secret,
                           token: token, participant: me.participant)
    }

    /// Cancel a pairing this participant started (§6.3): honest admins then refuse its claims. A device already
    /// approved through it stays until it is revoked.
    public func cancelPairing(token: Bytes) async throws {
        let record = "pairing:" + B64u.encode(token)
        guard let me, let delegation = homeValue(record, field: "delegation")
        else { throw TandemError.invalid("pairing: no such pairing") }
        let receipt = try await publishReceipt(outcome: Control.pairingCancelled, subject: delegation, me: me)
        try await addHome(record, field: "outcome", element: receipt)
    }

    // MARK: The new device (§6.3 step 1)

    /// Claim a device enrollment through a pairing link. Returns a device that waits for the admin
    /// (`awaitingApproval`); syncing it later picks up the certificate and the release.
    public static func pair(link: PairingLink, keys: DeviceKeys, label: String, store: Store, hub: any PushStore) async throws -> Device {
        let genesis = try await findGenesis(hub: link.hub, genesis: link.genesis, directoryKey: link.directoryKey, on: hub)
        let device = try Device.open(genesis: genesis, directoryKey: link.directoryKey, keys: keys, store: store, hub: hub)
        try await device.sync()
        try await device.claim(link: link, label: label)
        return device
    }

    /// Find a hub's genesis in its directory; the link pins its ID.
    static func findGenesis(hub: Bytes, genesis id: ID, directoryKey: Bytes, on store: any PushStore) async throws -> ControlObject {
        let key = Tandem.directoryKey(directoryKey, hub: hub), tag = Tandem.directoryTag(directoryKey, hub: hub)
        for m in try await store.changes(since: "").added where m.channel == tag {
            guard let (_, p) = try? Envelope.open(key: key, hub: hub, tag: tag, envelope: m.body),
                  let o = try? ControlObject.parse(p, expect: "genesis"), (try? o.id()) == id else { continue }
            return o
        }
        throw TandemError.invalid("link: the hub's genesis isn't on this mailbox")
    }

    /// Whether this device has claimed an enrollment and waits for the admin's release.
    public var awaitingApproval: Bool { pairing != nil }

    func claim(link: PairingLink, label: String) async throws {
        guard let keys, me == nil, pairing == nil else { throw TandemError.invalid("pairing: this device is already on the hub") }
        let inviteKey = try Control.pairingInviteKey(link: link.secret, hub: pin.hub, token: link.token)
        let channelKey = Control.pairingKey(link: link.secret, hub: pin.hub, token: link.token)
        let tag = Control.pairingTag(link: link.secret, hub: pin.hub, token: link.token)
        var delegation: ControlObject?, package: Bytes?, gate: Bytes?
        for m in try store.held() where m.channel == tag {
            guard let (kind, p) = try? Envelope.open(key: channelKey, hub: pin.hub, tag: tag, envelope: m.body), kind == 3,
                  let j = try? JSON.parse(p), case let .string(type)? = j["type"] else { continue }
            switch String(decoding: type, as: UTF8.self) {
            case "delegation": delegation = try? ControlObject.parse(j, expect: "delegation")
            case "package": package = try? j.bytes("bytes")
            case "gate": gate = try? j.bytes("bytes")
            default: continue
            }
        }
        guard let delegation, let package, let gate else { throw TandemError.invalid("pairing: the link's pairing isn't on the hub") }
        let staged = PairingClaim(request: ControlObject(kind: "request", fields: [:]), delegation: delegation, gate: gate)
        guard delegation.fields["participant"] == .id(link.participant),
              delegation.fields["key"] == .id(Bytes(inviteKey.publicKey.rawRepresentation)),
              delegation.fields["token"] == .id(link.token), delegation.fields["package"] == .id(H(package)),
              delegation.fields["gate"] == .id(H(gate)), staged.expires > Date(),
              sourceValidity(delegation).isValid
        else { throw TandemError.invalid("pairing: the link is expired, or not from a device of that participant") }
        try registry.add(delegation)

        let sign = Bytes(keys.signing.publicKey.rawRepresentation), agree = Bytes(keys.agreement.publicKey.rawRepresentation)
        var request = ControlObject(kind: "request", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "participant": .id(link.participant), "sign": .id(sign), "agree": .id(agree), "label": .text(Bytes(label.utf8)),
            "how": .int(Control.byPairing), "authority": .opt(try delegation.id()), "enrollment": .id(Bytes(repeating: 0, count: 16))])
        try request.sign("self", with: keys.signing)
        try request.sign("approver", with: inviteKey)
        let claim = PairingClaim(request: request, delegation: delegation, gate: gate)
        let admins = registry.allCertificates.filter { $0.participant == pin.creator && registry.activity($0.id).isValid }
        guard !admins.isEmpty else { throw TandemError.invalid("pairing: the hub has no admitted admin device") }

        let pending = PendingPairing(secret: link.secret, token: link.token, participant: link.participant,
                                     request: try request.id(), delegation: try delegation.id(), package: package)
        // The exact claim envelopes are kept with the pending pairing: each sync while it waits sends any that the
        // hub lacks (§12.2, "Pending pairing claims").
        let inbox = Tandem.inboxTag(uid: pin.creator, hub: pin.hub), body = try claim.json()
        let envelopes = try admins.map { (inbox, try Envelope.sealInbox(hub: pin.hub, uid: pin.creator, agreement: $0.agree, plaintext: body)) }
        try store.transaction {
            try store.putObject(delegation)
            try store.setValue("pairing.pending", pending.json())
            try store.setValue("pairing.claims", Self.pack(envelopes))
        }
        pairing = pending
        try await repairClaim()
    }

    /// Send whichever claim envelopes the hub lacks, while this device waits for the admin.
    func repairClaim() async throws {
        guard pairing != nil, let packed = try store.value("pairing.claims") else { return }
        for (tag, envelope) in try Self.unpack(packed) where try store.messages(withHash: H(envelope)).isEmpty {
            let id = try await hub.append(channel: tag, body: envelope)
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
            try store.markApplied(id)
        }
    }

    /// The delegation is signed by a device of its participant that is admitted now.
    func sourceValidity(_ delegation: ControlObject) -> Validity {
        guard delegation.fields["hub"] == .id(pin.hub), delegation.fields["genesis"] == .id(pin.genesis),
              case let .id(source)? = delegation.fields["source"], case let .id(participant)? = delegation.fields["participant"]
        else { return .invalid("delegation for another hub") }
        let active = registry.activity(source)
        guard active.isValid else { return active }
        guard let src = registry.certificate(source), src.participant == participant, delegation.verify("sig", publicKey: src.sign)
        else { return .invalid("delegation not signed by a device of that participant") }
        return .valid
    }

    // MARK: The admin (§6.3 steps 2–4, Reject)

    func receiveClaim(_ plaintext: Bytes, _ json: JSON) throws -> Outcome {
        guard let claim = try? PairingClaim.parse(json), claims[claim.id] == nil else { return .ignored }
        switch claimValidity(claim) {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        claims[claim.id] = claim
        try registry.add(claim.delegation)
        try store.transaction {
            try store.putObject(claim.delegation)
            try store.setValue("claim." + claim.id.hex, plaintext)
        }
        return .applied
    }

    /// The claim's exact request is bound to its delegation, signed by the new keys and by the link's invite
    /// key, and the delegation by an admitted device of the same participant (§6.9).
    func claimValidity(_ c: PairingClaim) -> Validity {
        let r = c.request, d = c.delegation
        guard r.fields["hub"] == .id(pin.hub), r.fields["genesis"] == .id(pin.genesis),
              r.fields["how"] == .int(Control.byPairing), r.fields["authority"] == .opt(c.delegationID),
              r.fields["participant"] == d.fields["participant"], r.fields["enrollment"] == .id(Bytes(repeating: 0, count: 16)),
              d.fields["gate"] == .id(H(c.gate)), case let .id(sign)? = r.fields["sign"], case let .id(key)? = d.fields["key"],
              r.verify("self", publicKey: sign), r.verify("approver", publicKey: key)
        else { return .invalid("claim does not match its delegation") }
        return sourceValidity(d)
    }

    /// Claims waiting for an admin's answer: "New device from Alice: Laptop". Claims on one link are listed
    /// together; cancelled, expired, rejected and approved ones are not listed.
    public func pendingClaims() -> [PairingClaim] {
        claims.values.filter { c in
            c.expires > Date() && !cancelled(c.delegationID) && !rejected(c.id) && !registry.allCertificates.contains { $0.requestID == c.id }
        }.sorted { ($0.token + $0.id).lexicographicallyPrecedes($1.token + $1.id) }
    }

    /// Approve a claim with the unlocked management seed (§6.3 steps 3–4): certify the exact keys, then release
    /// the gate secret to that device. The certificate and the release are journalled before either leaves.
    public func approve(claim id: ID, ms: Bytes) async throws {
        try await sync()
        let step = "approve." + id.hex
        let envelopes: [(String, Bytes)]
        if let journalled = try store.journal(step) {
            envelopes = try Self.unpack(journalled)
        } else {
            guard let c = claims[id], claimValidity(c).isValid, c.expires > Date(), !cancelled(c.delegationID)
            else { throw TandemError.invalid("approve: no valid claim with that ID; it may be cancelled or expired") }
            let manager = try DeviceKeys.manager(seed: ms)
            guard Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("approve: another hub's key") }
            guard case let .id(agree)? = c.request.fields["agree"], case let .id(package)? = c.delegation.fields["package"]
            else { throw TandemError.invalid("approve: malformed claim") }
            try registry.add(c.delegation)
            let issued = registry.revocationIDs.sorted { $0.lexicographicallyPrecedes($1) }
            var certificate = ControlObject(kind: "certificate", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
                "request": .id(id), "issued": .set(issued)], extras: ["body": .object(c.request)])
            try certificate.sign("sig", with: manager.signing)
            try registry.add(certificate)
            let valid = registry.certificateValidity(try certificate.id())
            guard valid.isValid else { throw TandemError.invalid("approve: the certificate would not be valid (\(valid))") }
            let snapshot = try registry.currentSnapshot(of: c.participant)
            try registry.add(snapshot)
            let a = try Control.openGate(manager.agreement, hub: pin.hub, token: c.token, participant: c.participant, gate: c.gate)
            let release = Notice(type: "pair-release", [("certificate", try certificate.id()), ("delegation", c.delegationID),
                                                        ("package", package), ("a", a)])
            var out = try [c.delegation, certificate, snapshot].map {
                (directoryTag, try Envelope.seal(key: directoryChannelKey, hub: pin.hub, kind: 3, tag: directoryTag, plaintext: try $0.json()))
            }
            out.append((Tandem.inboxTag(uid: c.participant, hub: pin.hub),
                        try Envelope.sealInbox(hub: pin.hub, uid: c.participant, agreement: agree, plaintext: release.json())))
            try store.transaction {
                try store.setJournal(step, Self.pack(out))
                for o in [c.delegation, certificate, snapshot] { try store.putObject(o) }
            }
            envelopes = out
        }
        try await export(envelopes, step: step)
    }

    /// Reject a claim (§6.3, Reject): a receipt signed with `MKs`. A certificate approved elsewhere outweighs it.
    public func reject(claim id: ID, ms: Bytes) async throws {
        guard claims[id] != nil else { throw TandemError.invalid("reject: no such claim") }
        let manager = try DeviceKeys.manager(seed: ms)
        guard Bytes(manager.signing.publicKey.rawRepresentation) == pin.hub else { throw TandemError.invalid("reject: another hub's key") }
        var receipt = ControlObject(kind: "receipt", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "outcome": .int(Control.rejected), "subject": .id(id), "signer": .opt(nil)])
        try receipt.sign("sig", with: manager.signing)
        try await keepReceipt(receipt)
    }

    /// Append each journalled envelope not already on the hub, then clear the journal. Safe to run again.
    func export(_ envelopes: [(String, Bytes)], step: String) async throws {
        for (tag, envelope) in envelopes where try store.messages(withHash: H(envelope)).isEmpty {
            let id = try await hub.append(channel: tag, body: envelope)
            try store.noteMessage(id: id, channel: tag, hash: H(envelope))
        }
        try store.clearJournal(step)
    }

    /// Finish approvals and home rekeys a crash interrupted: lost acknowledgements retry the same bytes (§6.3 step 4).
    func finishJournals() async throws {
        for step in try store.journalSteps() where step == "admit" || ["approve.", "home.", "revoke."].contains(where: step.hasPrefix) {
            guard let journalled = try store.journal(step) else { continue }
            try await export(try Self.unpack(journalled), step: step)
        }
    }

    // MARK: Receipts (§10.8)

    func publishReceipt(outcome: UInt64, subject: ID, me: Me) async throws -> ID {
        var receipt = ControlObject(kind: "receipt", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "outcome": .int(outcome), "subject": .id(subject), "signer": .opt(me.certificate)])
        try receipt.sign("sig", with: me.keys.signing)
        try await keepReceipt(receipt)
        return try receipt.id()
    }

    func keepReceipt(_ receipt: ControlObject) async throws {
        receipts[try receipt.id()] = receipt
        try store.putObject(receipt)
        try await publishDirectory([try receipt.json()])
    }

    /// A receipt counts if the right authority signed it: `MKs` for a rejection or a cancelled invitation, a device
    /// of the pairing participant for a cancelled or completed pairing.
    func receiptHolds(_ r: ControlObject) -> Bool {
        guard r.fields["hub"] == .id(pin.hub), r.fields["genesis"] == .id(pin.genesis), case let .int(outcome)? = r.fields["outcome"],
              case let .opt(signer)? = r.fields["signer"], case let .id(subject)? = r.fields["subject"] else { return false }
        switch outcome {
        case Control.rejected, Control.inviteCancelled:
            return signer == nil && r.verify("sig", publicKey: pin.hub)
        case Control.pairingCancelled, Control.paired:
            guard let signer, let cert = registry.certificate(signer),
                  (outcome == Control.paired ? registry.certificateValidity(signer) : registry.activity(signer)).isValid,
                  r.verify("sig", publicKey: cert.sign) else { return false }
            let participant = outcome == Control.paired ? registry.certificate(subject)?.participant
                                                        : registry.delegations[subject]?.participant
            return participant == cert.participant
        default:
            return false
        }
    }

    func receipt(_ outcome: UInt64, about subject: ID) -> Bool {
        receipts.values.contains { $0.fields["outcome"] == .int(outcome) && $0.fields["subject"] == .id(subject) && receiptHolds($0) }
    }

    func rejected(_ claim: ID) -> Bool { receipt(Control.rejected, about: claim) }
    func cancelled(_ delegation: ID) -> Bool { receipt(Control.pairingCancelled, about: delegation) }

    // MARK: The release (§6.3 step 5)

    func receiveRelease(_ plaintext: Bytes, me: Me) throws -> Outcome {
        guard let pending = pairing,
              let n = try? Notice.parse(plaintext, type: "pair-release", fields: ["certificate", "delegation", "package", "a"]),
              n["certificate"] == me.certificate, n["delegation"] == pending.delegation, n["package"] == H(pending.package),
              registry.certificate(me.certificate)?.requestID == pending.request, let a = n["a"],
              let contents = try? Control.openPackage(link: pending.secret, gate: a, hub: pin.hub, token: pending.token,
                                                      package: pending.package),
              let j = try? JSON.parse(contents), let epoch = try? j.bytes("home", count: 32), let key = try? j.bytes("k", count: 32)
        else { return .ignored }
        if let start = homeEpochs[epoch], start.fields["key"] != .id(Tandem.commit(key)) { return .ignored }
        try offerHomeKey(epoch, key)
        try store.transaction {
            try store.removeValue("pairing.pending")
            try store.removeValue("pairing.claims")
            try store.setValue("pairing.done." + pending.token.hex, me.certificate)
        }
        pairing = nil
        try moveHome()
        return .applied
    }

    /// After a sync: record finished pairings and hand this home's current keys to admitted keychains of this
    /// participant that no epoch was sealed to, such as a device paired after the package was staged (§6.3).
    func followUp() async throws {
        if isRevoked, try store.value("revoked") == nil { try store.setValue("revoked", [1]); changed(.revoked) }
        guard self.me != nil, registry.activity(self.me!.certificate).isValid else { return }
        try await refreshAuth()
        if homeNeedsRotation(self.me!) { try await rotateHome() }
        try await takeGrantedKeys()
        try await settleAnnouncedNotes()
        try await settleLateWrites()
        try await keyRequestFollowUp()
        try await hygiene()
        try await collectGarbage()
        guard let me else { return }
        for (name, cert) in try store.values(prefix: "pairing.done.") {
            guard let token = Bytes(hex: String(name.dropFirst("pairing.done.".count))), currentHomeEpoch != nil else { continue }
            let receipt = try await publishReceipt(outcome: Control.paired, subject: cert, me: me)
            try await addHome("pairing:" + B64u.encode(token), field: "outcome", element: receipt)
            try store.removeValue(name)
        }
        let inbox = Tandem.inboxTag(uid: me.participant, hub: pin.hub)
        for epoch in homeHeads {
            guard let start = homeEpochs[epoch], let key = homeKeys[epoch], case let .set(recipients)? = start.fields["recipients"]
            else { continue }
            for cert in registry.allCertificates where cert.participant == me.participant && cert.id != me.certificate
                && !recipients.contains(cert.id) && registry.activity(cert.id).isValid {
                let sent = "home.sent." + epoch.hex + "." + cert.id.hex
                guard try store.value(sent) == nil else { continue }
                let envelope = try Envelope.sealInbox(hub: pin.hub, uid: me.participant, agreement: cert.agree,
                                                      plaintext: Founder.homeKeyNotice(epoch: epoch, key: key).json())
                let id = try await hub.append(channel: inbox, body: envelope)
                try store.noteMessage(id: id, channel: inbox, hash: H(envelope))
                try store.markApplied(id)
                try store.setValue(sent, [1])
            }
        }
    }
}
