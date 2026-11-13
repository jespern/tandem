import Foundation
import Tandem

/// A grant delivered to this participant (§11.4): the owner's signed grant and the key it commits to.
struct Offer: Sendable {
    let grant: ControlObject
    let key: Bytes
    var collection: Bytes { if case let .id(c)? = grant.fields["collection"] { return c }; return [] }
    var admission: ID { if case let .id(a)? = grant.fields["admission"] { return a }; return [] }
    var epoch: ID { if case let .id(e)? = grant.fields["epoch"] { return e }; return [] }
}

extension Device {
    // MARK: Sharing, the owner's side (§11.1, §11.4)

    /// Add `participant` to a list this participant owns, as a writer or reader, in the current epoch, and send them a grant.
    public func share(list cid: Bytes, with participant: ID, role: Statement.Role, name: String = "") async throws {
        guard role != .owner, let me, var c = collections[cid], c.owner == me.participant, let head = c.head,
              let current = c.statements[head], let epoch = c.epoch(of: head), let key = c.keys[epoch]
        else { throw TandemError.invalid("share: not a list this participant owns") }
        guard !current.statement.members.contains(where: { $0.uid == participant }) else { return }
        let floor = try await publishedSnapshot(of: participant)
        var next = current.statement
        next.n += 1
        next.members.append(.init(uid: participant, role: role, name: Bytes(name.utf8), floor: floor))
        next.members.sort { $0.uid.lexicographicallyPrecedes($1.uid) }
        var w = Write(hub: pin.hub, certificate: me.certificate, authorization: me.auth, scope: cid, record: Roster.record,
                      field: Roster.field, op: .assign, value: next.json(), author: me.participant, dot: try nextDot(me), ref: head)
        try w.sign(with: me.keys.signing)
        let sid = try w.id()
        c.statements[sid] = (w, next)
        collections[cid] = c
        try store.putWrite(id: sid, scope: cid, record: Roster.record, epoch: epoch, payload: try recordPayload(record: Roster.record, writes: [w]))
        try await publish(cid: cid, epoch: epoch, record: Roster.record, writes: [w])
        try await sendGrant(cid: cid, to: participant, admission: sid, at: sid, epoch: epoch, key: key, me: me)
    }

    /// A snapshot of `participant` that the directory holds, publishing the current one if none does (§6.9).
    func publishedSnapshot(of participant: ID) async throws -> ID {
        let current = try registry.currentSnapshot(of: participant)
        let id = try current.id()
        if registry.snapshots[id] == nil {
            try registry.add(current)
            try store.putObject(current)
            try await publishDirectory([try current.json()])
        }
        return id
    }

    /// One grant per admitted keychain of the recipient, sealed to its agreement key in their inbox (§11.4).
    func sendGrant(cid: Bytes, to participant: ID, admission: ID, at: ID, epoch: ID, key: Bytes, me: Me) async throws {
        var grant = ControlObject(kind: "grant", fields: ["hub": .id(pin.hub), "collection": .id(cid), "recipient": .id(participant),
            "admission": .id(admission), "at": .id(at), "epoch": .id(epoch), "key": .id(Tandem.commit(key)),
            "signer": .id(me.certificate), "auth": .id(me.auth)])
        try grant.sign("sig", with: me.keys.signing)
        let inbox = Tandem.inboxTag(uid: participant, hub: pin.hub)
        let devices = registry.allCertificates.filter { $0.participant == participant && registry.activity($0.id).isValid }
        guard !devices.isEmpty else { throw TandemError.invalid("share: that participant has no admitted device") }
        let grantJSON = try grant.json()
        for cert in devices {
            let delivery = JSONWriter.object { w in
                w.member("type", text: Bytes("grant".utf8))
                w.member("grant", json: grantJSON)
                w.member("certificate", b64u: cert.id)
                w.member("k", b64u: key)
            }
            let envelope = try Envelope.sealInbox(hub: pin.hub, uid: participant, agreement: cert.agree, plaintext: delivery)
            let id = try await hub.append(channel: inbox, body: envelope)
            try store.noteMessage(id: id, channel: inbox, hash: H(envelope))
            try store.markApplied(id)
        }
    }

    // MARK: Sharing, the recipient's side (§11.2, §11.4)

    /// A grant arrived: keep it as an offer and start reading the collection's channel to validate it.
    func receiveGrant(_ plaintext: Bytes, _ json: JSON, me: Me) throws -> Outcome {
        guard let g = json["grant"], let grant = try? ControlObject.parse(g, expect: "grant"),
              grant.fields["hub"] == .id(pin.hub), grant.fields["recipient"] == .id(me.participant),
              let certificate = try? json.bytes("certificate", count: 32), certificate == me.certificate,
              let key = try? json.bytes("k", count: 32), grant.fields["key"] == .id(Tandem.commit(key))
        else { return .ignored }
        let offer = Offer(grant: grant, key: key)
        guard offers[offer.collection] == nil || offers[offer.collection]!.admission != offer.admission else { return .ignored }
        offers[offer.collection] = offer
        try store.setValue("offer." + offer.collection.hex, plaintext)
        var c = collections[offer.collection] ?? CollectionState(id: offer.collection)
        c.keys[offer.epoch] = key
        collections[offer.collection] = c
        return .applied
    }

    /// Whether an offer is a real admission (§11.4): the roster in its channel, from a genesis whose owner signed the
    /// grant, through `at`, which lists this participant and starts or continues the granted epoch.
    func offerValidity(_ o: Offer, me: Me) -> Validity {
        guard let c = collections[o.collection], let owner = c.owner else { return .pending([]) }
        guard case let .id(at)? = o.grant.fields["at"], let s = c.statements[at] else { return .pending([]) }
        guard c.epoch(of: at) == o.epoch, s.statement.commit == Tandem.commit(o.key),
              s.statement.members.contains(where: { $0.uid == me.participant }) else { return .invalid("the roster doesn't grant this") }
        guard case let .id(signer)? = o.grant.fields["signer"], case let .id(auth)? = o.grant.fields["auth"] else { return .invalid("grant") }
        let ok = registry.authorizes(snapshot: auth, certificate: signer)
        guard ok.isValid else { return ok }
        guard let cert = registry.certificate(signer), cert.participant == owner, o.grant.verify("sig", publicKey: cert.sign)
        else { return .invalid("not granted by the owner") }
        return .valid
    }

    /// Lists shared with this participant that it hasn't answered (§11.2): ID, name and owner.
    public func pendingOffers() -> [(list: Bytes, name: String?, owner: ID)] {
        guard let me else { return [] }
        return offers.values.compactMap { o -> (Bytes, String?, ID)? in
            guard offerValidity(o, me: me).isValid, answer(to: o) == nil, let c = collections[o.collection], let owner = c.owner
            else { return nil }
            return (o.collection, c.records[Self.meta]?.value(Bytes("name".utf8)).map { String(decoding: $0, as: UTF8.self) }, owner)
        }.sorted { $0.0.lexicographicallyPrecedes($1.0) }
    }

    func answer(to o: Offer) -> UInt8? {
        let membership = "membership:" + B64u.encode(o.collection)
        return homeElements(membership, field: "answers").first { $0.count == 33 && Array($0.prefix(32)) == o.admission }?.last
    }

    /// Add a shared list (§11.2): record the answer and the key in home, so every device of this participant follows it.
    public func accept(list cid: Bytes) async throws {
        guard let me, let o = offers[cid], offerValidity(o, me: me).isValid, let c = collections[cid], let owner = c.owner,
              case let .id(at)? = o.grant.fields["at"], let member = c.statements[at]?.statement.members.first(where: { $0.uid == me.participant })
        else { throw TandemError.invalid("accept: no valid offer for that list") }
        let membership = "membership:" + B64u.encode(cid)
        try await addHome(membership, field: "answers", element: o.admission + [UInt8(ascii: "a")])
        try await setHome(membership, field: "owner", value: owner)
        try await setHome(membership, field: "role", value: Bytes(member.role.rawValue.utf8))
        try await addHome(membership, field: "keys", element: Self.keyElement(epoch: o.epoch, key: o.key))
    }
}
