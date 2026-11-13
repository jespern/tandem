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
        guard role != .owner else { throw TandemError.invalid("share: a list has one owner") }
        try await sync()
        guard let me, let c = collections[cid], c.owner == me.participant, let head = c.head, !c.dead,
              c.statements[head]?.statement.deleted == false else {
            throw TandemError.invalid("share: not a list this participant owns, or it was deleted")
        }
        guard c.statements[head]?.statement.members.contains(where: { $0.uid == participant }) == false else { return }
        let floor = try await publishedSnapshot(of: participant)
        // Until the grant is out, a crash leaves this mark for the next sync to finish (§11.4).
        let pending = "pendinggrant.\(cid.hex).\(participant.hex)"
        try store.setValue(pending, [1])
        let (sid, epoch, key) = try await sameEpoch(cid) { s in
            s.members.append(.init(uid: participant, role: role, name: Bytes(name.utf8), floor: floor))
        }
        try await sendGrant(cid: cid, to: participant, admission: sid, at: sid, epoch: epoch, key: key, me: me)
        try store.removeValue(pending)
    }

    /// Grants a crash kept from going out after their member was added.
    func finishPendingGrants() async throws {
        guard let me else { return }
        for (name, _) in try store.values(prefix: "pendinggrant.") {
            let parts = name.split(separator: ".").map(String.init)
            guard parts.count == 3, let cid = Bytes(hex: parts[1]), let uid = Bytes(hex: parts[2]), let c = collections[cid],
                  let head = c.head, let epoch = c.epoch(of: head), let key = c.keys[epoch] else { continue }
            if let admission = admission(of: uid, in: c, at: head) {
                try await sendGrant(cid: cid, to: uid, admission: admission, at: head, epoch: epoch, key: key, me: me)
            }
            try store.removeValue(name)
        }
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
            // Grants are kept (§11.4): this one is healed if it goes missing, and replaces the last one to that keychain.
            let kept = "kept.grant.\(cid.hex).\(participant.hex).\(cert.id.hex)"
            let earlier = try store.value(kept)
            try store.setValue(kept, envelope)
            if let earlier, earlier != envelope {
                let old = try store.messages(withHash: H(earlier))
                if !old.isEmpty { try await hub.destroy(ids: old); try store.forgetMessages(old) }
            }
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
        let offer = Offer(grant: grant, key: key), id = try grant.id()
        guard offers[id] == nil else { return .ignored }
        offers[id] = offer
        try store.setValue("offer." + id.hex, plaintext)
        var c = collections[offer.collection] ?? CollectionState(id: offer.collection)
        c.offer(key: key, epoch: offer.epoch)
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

    /// A verified grant shows that its epoch was announced: the owner grants only after announcing (§11.6, "a device
    /// learns of the change … from a verified grant and the roster in the new channel"). Every epoch start on the line to
    /// its `at` counts from then on, for a member added after a key change and for one whose announcement was deleted.
    func adoptGrantedChains() throws -> Bool {
        guard let me else { return false }
        var any = false
        for o in offers.values where answer(to: o) != UInt8(ascii: "n") {
            guard case let .id(at)? = o.grant.fields["at"], var c = collections[o.collection], c.statements[at] != nil,
                  offerValidity(o, me: me).isValid else { continue }
            var changedHere = false
            for id in chain(c, to: at) where c.startsEpoch(id) && !c.announced.contains(id) {
                c.announced.insert(id)
                try store.setValue("ann." + o.collection.hex + "." + id.hex, [1])
                changedHere = true
            }
            guard changedHere else { continue }
            collections[o.collection] = c
            try moveCollection(o.collection)
            changed(.lists); changed(.members(list: o.collection))
            any = true
        }
        return any
    }

    /// Lists shared with this participant that it hasn't answered (§11.2): ID, name and owner.
    public func pendingOffers() -> [(list: Bytes, name: String?, owner: ID)] {
        guard let me else { return [] }
        let open = Set(offers.values.filter { offerValidity($0, me: me).isValid && answer(to: $0) == nil }.map(\.collection))
        return open.compactMap { cid -> (Bytes, String?, ID)? in
            guard let c = collections[cid], let owner = c.owner else { return nil }
            return (cid, c.records[Self.meta]?.value(Bytes("name".utf8)).map { String(decoding: $0, as: UTF8.self) }, owner)
        }.sorted { $0.0.lexicographicallyPrecedes($1.0) }
    }

    /// Valid grants for a list, latest epoch first.
    func validOffers(_ cid: Bytes, me: Me) -> [Offer] {
        let c = collections[cid]
        return offers.values.filter { $0.collection == cid && offerValidity($0, me: me).isValid }
            .sorted { (c?.statements[$0.epoch]?.statement.keyN ?? 0) > (c?.statements[$1.epoch]?.statement.keyN ?? 0) }
    }

    /// Keys of later epochs for lists this participant accepted: into home once their grants check out (§11.4).
    func takeGrantedKeys() async throws {
        guard let me else { return }
        for o in offers.values where answer(to: o) == UInt8(ascii: "a") && offerValidity(o, me: me).isValid {
            let name = "membership:" + B64u.encode(o.collection), element = Self.keyElement(epoch: o.epoch, key: o.key)
            guard !homeElements(name, field: "keys").contains(element) else { continue }
            try await addHome(name, field: "keys", element: element)
            try moveCollection(o.collection)
        }
    }

    func answer(to o: Offer) -> UInt8? {
        let membership = "membership:" + B64u.encode(o.collection)
        return homeElements(membership, field: "answers").first { $0.count == 33 && Array($0.prefix(32)) == o.admission }?.last
    }

    /// Add a shared list (§11.2): record the answer and the key in home, so every device of this participant follows it.
    public func accept(list cid: Bytes) async throws {
        guard let me, let o = validOffers(cid, me: me).first(where: { answer(to: $0) == nil }), let c = collections[cid], let owner = c.owner,
              case let .id(at)? = o.grant.fields["at"], let member = c.statements[at]?.statement.members.first(where: { $0.uid == me.participant })
        else { throw TandemError.invalid("accept: no valid offer for that list") }
        let membership = "membership:" + B64u.encode(cid)
        try await addHome(membership, field: "answers", element: o.admission + [UInt8(ascii: "a")])
        try await setHome(membership, field: "owner", value: owner)
        try await setHome(membership, field: "role", value: Bytes(member.role.rawValue.utf8))
        try await addHome(membership, field: "keys", element: Self.keyElement(epoch: o.epoch, key: o.key))
        try await sendAnswer(cid, admission: o.admission, answer: UInt8(ascii: "a"), me: me)
    }
}
