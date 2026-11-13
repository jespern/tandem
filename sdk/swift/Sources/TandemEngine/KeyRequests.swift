import Foundation
import Tandem

// Key requests (§11.4): a device that learns of an epoch whose key it lacks waits for a grant; after a safety-net
// delay it asks the owner's devices once. A current member's keychain gets the grant again; someone since removed
// gets the roster statements that prove it.

extension Device {
    /// How long a device waits for a grant before it asks (§11.4: 10 minutes). Tests shorten it.
    public func setKeyRequestDelay(_ delay: Duration) { keyRequestDelay = delay }

    /// Lists this participant follows whose current epoch it has no key for, by epoch: noted with the time first seen.
    func noteMissingKeys() throws {
        for (cid, c) in collections where following(cid) {
            guard let head = c.head, let epoch = c.epoch(of: head), c.keys[epoch] == nil else { continue }
            let name = "keymiss.\(cid.hex).\(epoch.hex)"
            if try store.value(name) == nil { try store.setValue(name, Bytes(String(Self.now).utf8)) }
        }
    }

    /// Ask for the keys that have been missing longer than the delay, once each; returns when the next one is due.
    @discardableResult
    func sendDueKeyRequests() async throws -> Duration? {
        guard let me, registry.activity(me.certificate).isValid else { return nil }
        let delayMillis = UInt64(keyRequestDelay.components.seconds * 1000 + keyRequestDelay.components.attoseconds / 1_000_000_000_000_000)
        var next: UInt64?
        for (name, since) in try store.values(prefix: "keymiss.") {
            let parts = name.split(separator: ".").map(String.init)
            guard parts.count == 3, let cid = Bytes(hex: parts[1]), let epoch = Bytes(hex: parts[2]),
                  let first = UInt64(String(decoding: since, as: UTF8.self)) else { continue }
            guard let c = collections[cid], c.keys[epoch] == nil, following(cid), let head = c.head else {
                try store.removeValue(name)   // the key came, or the list went
                continue
            }
            guard try store.value("keyreq." + name) == nil else { continue }
            guard Self.now >= first + delayMillis else { next = min(next ?? .max, first + delayMillis - Self.now); continue }
            try await sendKeyRequest(cid, c: c, have: head, me: me)
            try store.setValue("keyreq." + name, [1])
        }
        return next.map { .milliseconds(Int64($0)) }
    }

    func sendKeyRequest(_ cid: Bytes, c: CollectionState, have: ID, me: Me) async throws {
        guard let owner = c.owner else { return }
        var request = ControlObject(kind: "key-request", fields: ["hub": .id(pin.hub), "collection": .id(cid),
            "participant": .id(me.participant), "have": .id(have), "signer": .id(me.certificate), "auth": .id(me.auth)])
        try request.sign("sig", with: me.keys.signing)
        let body = try request.json(), inbox = Tandem.inboxTag(uid: owner, hub: pin.hub)
        for cert in registry.allCertificates where cert.participant == owner && registry.activity(cert.id).isValid {
            let envelope = try Envelope.sealInbox(hub: pin.hub, uid: owner, agreement: cert.agree, plaintext: body)
            let id = try await hub.append(channel: inbox, body: envelope)
            try store.noteMessage(id: id, channel: inbox, hash: H(envelope))
            try store.markApplied(id)
        }
        // And as a notice in the last epoch channel it can read, which the owner's devices also watch.
        if let readable = chain(c, to: have).reversed().compactMap({ c.epoch(of: $0) }).first(where: { c.keys[$0] != nil }) {
            try await publishNotice(cid, epoch: readable, plaintext: body)
        }
    }

    /// The owner's side: check a key request, then answer it after the sync, once.
    func receiveKeyRequest(_ json: JSON) throws -> Outcome {
        guard let me, let r = try? ControlObject.parse(json, expect: "key-request"), r.fields["hub"] == .id(pin.hub),
              case let .id(cid)? = r.fields["collection"], collections[cid]?.owner == me.participant,
              case let .id(participant)? = r.fields["participant"], case let .id(signer)? = r.fields["signer"],
              case let .id(auth)? = r.fields["auth"], let id = try? r.id() else { return .ignored }
        switch registry.authorizes(snapshot: auth, certificate: signer) {
        case .pending: return .waiting
        case .invalid: return .ignored
        case .valid: break
        }
        // A revoked keychain gets nothing, whatever its participant's standing (§11.4).
        guard registry.activity(signer).isValid, let cert = registry.certificate(signer), cert.participant == participant,
              r.verify("sig", publicKey: cert.sign) else { return .ignored }
        guard try store.value("keyreq.answered." + id.hex) == nil else { return .ignored }
        try store.setValue("keyreq.pending." + id.hex, try r.json())
        return .applied
    }

    /// Answer the key requests received: a grant for a current member, the roster that removed them otherwise.
    func answerKeyRequests() async throws {
        guard let me else { return }
        for (name, data) in try store.values(prefix: "keyreq.pending.") {
            let id = String(name.dropFirst("keyreq.pending.".count))
            defer { try? store.removeValue(name); try? store.setValue("keyreq.answered." + id, [1]) }
            guard let j = try? JSON.parse(data), let r = try? ControlObject.parse(j, expect: "key-request"),
                  case let .id(cid)? = r.fields["collection"], case let .id(participant)? = r.fields["participant"],
                  case let .id(have)? = r.fields["have"], case let .id(signer)? = r.fields["signer"],
                  let c = collections[cid], let head = c.head, let epoch = c.epoch(of: head), let key = c.keys[epoch],
                  let cert = registry.certificate(signer) else { continue }
            if let admission = admission(of: participant, in: c, at: head) {
                try? await sendGrant(cid: cid, to: participant, admission: admission, at: head, epoch: epoch, key: key, me: me)
                continue
            }
            // Removed: the statements from `have` through the one that removed them prove it.
            let line = chain(c, to: head)
            guard let from = line.firstIndex(of: have) else { continue }
            var statements: [Write] = []
            for sid in line[(from + 1)...] {
                guard let s = c.statements[sid] else { break }
                statements.append(s.write)
                if !s.statement.members.contains(where: { $0.uid == participant }) { break }
            }
            guard !statements.isEmpty else { continue }
            let reply = JSONWriter.object { w in
                w.member("type", text: Bytes("roster".utf8))
                w.member("c", b64u: cid)
                w.member("statements", b64u: (try? recordPayload(record: Roster.record, writes: statements)) ?? [])
            }
            let inbox = Tandem.inboxTag(uid: participant, hub: pin.hub)
            let envelope = try Envelope.sealInbox(hub: pin.hub, uid: participant, agreement: cert.agree, plaintext: reply)
            let mid = try await hub.append(channel: inbox, body: envelope)
            try store.noteMessage(id: mid, channel: inbox, hash: H(envelope))
            try store.markApplied(mid)
        }
    }

    /// A roster reply in this participant's inbox: statements that prove themselves, read as announced.
    func receiveRosterReply(_ json: JSON) throws -> Outcome {
        guard let cid = try? json.bytes("c", count: 16), collections[cid] != nil, let payload = try? json.bytes("statements"),
              case let .record(state)? = try? Kind1.parse(payload, hub: pin.hub, scope: cid), state.record == Roster.record
        else { return .ignored }
        var any = false
        for w in state.writes {
            let channel = collections[cid]?.epoch(of: w.ref) ?? w.ref
            switch try acceptStatement(w, cid: cid, channel: channel) {
            case .applied: any = true
            case .waiting: return .waiting
            case .ignored: continue
            }
        }
        if any { changed(.lists); changed(.members(list: cid)) }
        return any ? .applied : .ignored
    }

    /// After a sync: note missing keys, answer requests, and arm the one safety-net timer if a request will be due.
    func keyRequestFollowUp() async throws {
        try noteMissingKeys()
        try await answerKeyRequests()
        guard let due = try await sendDueKeyRequests(), keyRequestTimer == nil else { return }
        keyRequestTimer = Task { [weak self] in
            try? await Task.sleep(for: due)   // allowed-timer: §11.4's safety net behind grant delivery
            await self?.keyRequestTimerFired()
        }
    }

    func keyRequestTimerFired() async {
        keyRequestTimer = nil
        do { try await keyRequestFollowUp() } catch { changed(.syncFailed("\(error)")); flushEvents() }
    }
}
