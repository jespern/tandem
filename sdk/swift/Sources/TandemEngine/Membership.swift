import Foundation
import Tandem

// Answering an admission, leaving and deleting (§11.2, §11.5, §11.9), and what the owner learns of them.

/// A member's answer to their current admission, as the owner's devices know it (§11.2).
public enum MemberStatus: Equatable, Sendable {
    /// Added, no answer yet: "invitation pending".
    case pending
    case accepted
    case declined
    /// Left; still listed until the owner removes them, which changes the key (§11.5).
    case left
}

extension Device {
    // MARK: The member

    /// The answer letters this participant gave an admission, from home (§6.2).
    func answers(_ cid: Bytes, admission: ID) -> Set<UInt8> {
        Set(homeElements(membershipName(cid), field: "answers").filter { $0.count == 33 && Array($0.prefix(32)) == admission }.map { $0[32] })
    }

    /// Whether this participant follows a list: its own, or one whose current admission it accepted and didn't leave.
    func following(_ cid: Bytes) -> Bool {
        guard let me, let c = collections[cid], homeValue(membershipName(cid), field: "owner") != nil else { return false }
        if let head = c.head, c.statements[head]?.statement.deleted == true { return false }
        if c.owner == me.participant || homeValue(membershipName(cid), field: "owner") == me.participant { return true }
        let admissions = Set(homeElements(membershipName(cid), field: "answers").filter { $0.count == 33 }.map { Array($0.prefix(32)) })
        let current = c.head.flatMap { admission(of: me.participant, in: c, at: $0) }
        // Before the roster is here, the latest answered admission stands in for the current one.
        let candidates = current.map { [$0] } ?? Array(admissions)
        return candidates.contains { a in
            let letters = answers(cid, admission: a)
            return letters.contains(UInt8(ascii: "a")) && !letters.contains(UInt8(ascii: "l")) && !letters.contains(UInt8(ascii: "n"))
        }
    }

    /// Say no to a list shared with this participant (§11.2): nothing appears, and the owner is told.
    public func decline(list cid: Bytes) async throws {
        guard let me, let o = validOffers(cid, me: me).first(where: { answer(to: $0) == nil })
        else { throw TandemError.invalid("decline: no offer for that list") }
        try await addHome(membershipName(cid), field: "answers", element: o.admission + [UInt8(ascii: "n")])
        try await sendAnswer(cid, admission: o.admission, answer: UInt8(ascii: "n"), me: me)
    }

    /// Leave a list (§11.5): stop following it on every device of this participant, post a leave notice for the
    /// owner, who removes this participant when they confirm. The owner can't leave their own list.
    public func leave(list cid: Bytes) async throws {
        guard let me, let c = collections[cid], let head = c.head, c.owner != me.participant,
              let admission = admission(of: me.participant, in: c, at: head), let epoch = c.epoch(of: head), c.keys[epoch] != nil
        else { throw TandemError.invalid("leave: not a member of that list") }
        try await addHome(membershipName(cid), field: "answers", element: admission + [UInt8(ascii: "l")])
        var notice = ControlObject(kind: "leave", fields: ["hub": .id(pin.hub), "collection": .id(cid),
            "participant": .id(me.participant), "at": .id(admission), "signer": .id(me.certificate), "auth": .id(me.auth)])
        try notice.sign("sig", with: me.keys.signing)
        try await publishNotice(cid, epoch: epoch, plaintext: try notice.json())
        try await sendAnswer(cid, admission: admission, answer: UInt8(ascii: "l"), me: me)
    }

    /// A kind-3 notice in a collection channel (§10.5).
    func publishNotice(_ cid: Bytes, epoch: ID, plaintext: Bytes) async throws {
        guard let key = collections[cid]?.keys[epoch] else { return }
        let tag = Tandem.collectionTag(key: key, hub: pin.hub, epoch: epoch)
        let envelope = try Envelope.seal(key: key, hub: pin.hub, kind: 3, tag: tag, plaintext: plaintext)
        let id = try await hub.append(channel: tag, body: envelope)
        try store.noteMessage(id: id, channel: tag, hash: H(envelope))
        try store.markApplied(id)
    }

    /// The answer receipt, sealed to the owner's admitted devices (§10.8, §6.10).
    func sendAnswer(_ cid: Bytes, admission: ID, answer: UInt8, me: Me) async throws {
        guard let owner = collections[cid]?.owner else { return }
        var a = ControlObject(kind: "answer", fields: ["hub": .id(pin.hub), "collection": .id(cid), "participant": .id(me.participant),
            "admission": .id(admission), "answer": .answer(answer), "event": .id(Self.random(16)), "signer": .id(me.certificate),
            "auth": .id(me.auth)])
        try a.sign("sig", with: me.keys.signing)
        let body = try a.json(), inbox = Tandem.inboxTag(uid: owner, hub: pin.hub)
        for cert in registry.allCertificates where cert.participant == owner && registry.activity(cert.id).isValid {
            let envelope = try Envelope.sealInbox(hub: pin.hub, uid: owner, agreement: cert.agree, plaintext: body)
            let id = try await hub.append(channel: inbox, body: envelope)
            try store.noteMessage(id: id, channel: inbox, hash: H(envelope))
            try store.markApplied(id)
        }
    }

    // MARK: The owner

    /// An answer receipt from a member, in this owner's inbox.
    func receiveAnswer(_ json: JSON) throws -> Outcome {
        guard let me, let a = try? ControlObject.parse(json, expect: "answer"), a.fields["hub"] == .id(pin.hub),
              case let .id(cid)? = a.fields["collection"], collections[cid]?.owner == me.participant,
              case let .id(participant)? = a.fields["participant"], case let .id(admission)? = a.fields["admission"],
              case let .answer(letter)? = a.fields["answer"], case let .id(signer)? = a.fields["signer"],
              case let .id(auth)? = a.fields["auth"] else { return .ignored }
        switch registry.authorizes(snapshot: auth, certificate: signer) {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        guard let cert = registry.certificate(signer), cert.participant == participant, a.verify("sig", publicKey: cert.sign)
        else { return .ignored }
        let name = "ans.\(cid.hex).\(participant.hex).\(admission.hex)"
        var letters = Set(try store.value(name) ?? [])
        guard letters.insert(letter).inserted else { return .ignored }
        try store.setValue(name, letters.sorted())
        return .applied
    }

    /// A leave notice in a collection channel (§11.5): it counts if `at` is a statement of the chain that lists the
    /// member, and every statement from `at` to the head lists them too.
    func receiveLeave(_ json: JSON, cid: Bytes) throws -> Outcome {
        guard let me, let c = collections[cid], c.owner == me.participant, let l = try? ControlObject.parse(json, expect: "leave"),
              l.fields["hub"] == .id(pin.hub), l.fields["collection"] == .id(cid), case let .id(participant)? = l.fields["participant"],
              case let .id(at)? = l.fields["at"], case let .id(signer)? = l.fields["signer"], case let .id(auth)? = l.fields["auth"]
        else { return .ignored }
        guard c.statements[at] != nil else { return .waiting }
        switch registry.authorizes(snapshot: auth, certificate: signer) {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        guard let cert = registry.certificate(signer), cert.participant == participant, l.verify("sig", publicKey: cert.sign)
        else { return .ignored }
        try store.setValue("left.\(cid.hex).\(participant.hex)", at)
        changed(.members(list: cid))
        return .applied
    }

    /// Each listed member's answer to their current admission (§11.2): "invitation pending", "Alice left House".
    public func memberStatus(list cid: Bytes) throws -> [(participant: ID, status: MemberStatus)] {
        guard let me, let c = collections[cid], c.owner == me.participant, let head = c.head,
              let s = c.statements[head]?.statement else { return [] }
        return try s.members.filter { $0.uid != me.participant }.map { m in
            guard let admission = admission(of: m.uid, in: c, at: head) else { return (m.uid, .pending) }
            // A leave notice counts only while every statement from its `at` to the head lists the member.
            if let at = try store.value("left.\(cid.hex).\(m.uid.hex)"), at == admission { return (m.uid, .left) }
            let letters = Set(try store.value("ans.\(cid.hex).\(m.uid.hex).\(admission.hex)") ?? [])
            if letters.contains(UInt8(ascii: "l")) { return (m.uid, .left) }
            if letters.contains(UInt8(ascii: "n")) { return (m.uid, .declined) }
            if letters.contains(UInt8(ascii: "a")) { return (m.uid, .accepted) }
            return (m.uid, .pending)
        }
    }

    /// Delete a list (§11.9): a final statement no successor follows. Devices stop showing it.
    public func delete(list cid: Bytes) async throws {
        try await sameEpoch(cid) { $0.deleted = true }
    }

    /// Whether the owner deleted a list.
    public func isDeleted(list cid: Bytes) -> Bool {
        guard let c = collections[cid], let head = c.head else { return false }
        return c.statements[head]?.statement.deleted ?? false
    }
}
