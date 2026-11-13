import Tandem

// The device registry (§6.9), as `proofs/TandemProofs/Registry.lean` defines it.
//
// Objects are named by content and merge by union; nothing here depends on
// the order they arrived in. Validity has three outcomes: an object whose
// dependencies haven't arrived is pending, never invalid, and becomes valid
// or invalid only once they have (§8.3, "missing proofs hold the object").

public typealias ID = Bytes

public enum Validity: Equatable, Sendable {
    case valid
    case invalid(String)
    case pending([ID])

    public var isValid: Bool { self == .valid }

    /// The first non-valid outcome, merging pending dependencies.
    static func all(_ outcomes: [Validity]) -> Validity {
        var missing: [ID] = []
        for o in outcomes {
            switch o {
            case .valid: continue
            case .invalid: return o
            case let .pending(ids): missing += ids
            }
        }
        return missing.isEmpty ? .valid : .pending(missing)
    }
}

/// What a device pins for a hub: its manager keys (`hubID` and `pub(MKa)`), its genesis and the creator it names.
public struct HubPin: Equatable, Sendable {
    public let hub: Bytes, agree: Bytes, genesis: ID, creator: ID
    public init(hub: Bytes, agree: Bytes, genesis: ID, creator: ID) {
        self.hub = hub; self.agree = agree; self.genesis = genesis; self.creator = creator
    }

    /// Pin from a genesis object, after checking its signature.
    public init(genesis g: ControlObject) throws {
        guard g.kind == "genesis", case let .id(hub)? = g.fields["hub"], case let .id(agree)? = g.fields["agree"],
              case let .id(creator)? = g.fields["creator"], g.verify("sig", publicKey: hub)
        else { throw TandemError.invalid("registry: not a genesis signed by its own hub key") }
        self.init(hub: hub, agree: agree, genesis: try g.id(), creator: creator)
    }
}

public struct Certificate: Sendable {
    public let id: ID, requestID: ID
    public let participant: ID, sign: Bytes, agree: Bytes, label: Bytes
    public let how: UInt64, authority: ID?, enrollment: Bytes
    public let issued: [ID]
    public let object: ControlObject
    let request: ControlObject

    public var keys: Bytes { sign + agree }
}

struct Revocation: Sendable {
    let id: ID, target: ID, retained: [ID]
}

struct Delegation: Sendable {
    let id: ID, participant: ID, source: ID, key: Bytes, token: Bytes, expires: UInt64, package: ID, gate: ID
    let object: ControlObject
}

struct Capability: Sendable {
    let id: ID, key: Bytes, invite: Bytes, expires: UInt64
}

public struct Snapshot: Equatable, Sendable {
    public let id: ID, participant: ID, certs: [ID], revocations: [ID]
}

public struct Registry: Sendable {
    public let pin: HubPin
    private(set) var certs: [ID: Certificate] = [:]
    private(set) var revs: [ID: Revocation] = [:]
    private(set) var delegations: [ID: Delegation] = [:]
    private(set) var capabilities: [ID: Capability] = [:]
    public private(set) var snapshots: [ID: Snapshot] = [:]

    public init(pin: HubPin) { self.pin = pin }

    func bound(_ o: ControlObject) -> Bool {
        if case let .id(h)? = o.fields["hub"], h != pin.hub { return false }
        if case let .id(g)? = o.fields["genesis"], g != pin.genesis { return false }
        return true
    }

    /// Take in one registry object. Returns its ID; throws if it can never be valid on
    /// this hub (wrong hub or genesis, malformed, a manager signature that doesn't verify).
    @discardableResult
    public mutating func add(_ o: ControlObject) throws -> ID {
        guard bound(o) else { throw TandemError.invalid("registry: \(o.kind) is for another hub") }
        let id = try o.id()
        func v(_ name: String) -> ControlValue? { o.fields[name] }
        switch o.kind {
        case "certificate":
            guard case let .object(request)? = o.extras["body"], request.kind == "request", bound(request),
                  case let .id(rid)? = v("request"), try request.id() == rid, case let .set(issued)? = v("issued"),
                  case let .id(participant)? = request.fields["participant"], case let .id(sign)? = request.fields["sign"],
                  case let .id(agree)? = request.fields["agree"], case let .text(label)? = request.fields["label"],
                  case let .int(how)? = request.fields["how"], case let .opt(authority)? = request.fields["authority"],
                  case let .id(enrollment)? = request.fields["enrollment"]
            else { throw TandemError.invalid("registry: certificate without its request") }
            certs[id] = Certificate(id: id, requestID: rid, participant: participant, sign: sign, agree: agree, label: label,
                                    how: how, authority: authority, enrollment: enrollment, issued: issued, object: o,
                                    request: request)
        case "revocation":
            guard o.verify("sig", publicKey: pin.hub), case let .id(target)? = v("target"),
                  case let .set(retained)? = v("retained")
            else { throw TandemError.invalid("registry: revocation not signed by the hub") }
            revs[id] = Revocation(id: id, target: target, retained: retained)
        case "delegation":
            guard case let .id(p)? = v("participant"), case let .id(src)? = v("source"), case let .id(key)? = v("key"),
                  case let .id(token)? = v("token"), case let .int(exp)? = v("expires"), case let .id(pkg)? = v("package"),
                  case let .id(gate)? = v("gate")
            else { throw TandemError.invalid("registry: malformed delegation") }
            delegations[id] = Delegation(id: id, participant: p, source: src, key: key, token: token, expires: exp,
                                         package: pkg, gate: gate, object: o)
        case "invitation":
            guard o.verify("sig", publicKey: pin.hub), case let .id(key)? = v("key"), case let .id(invite)? = v("invite"),
                  case let .int(exp)? = v("expires")
            else { throw TandemError.invalid("registry: invitation not signed by the hub") }
            capabilities[id] = Capability(id: id, key: key, invite: invite, expires: exp)
        case "snapshot":
            guard case let .id(p)? = v("participant"), case let .set(c)? = v("certs"), case let .set(r)? = v("revocations")
            else { throw TandemError.invalid("registry: malformed snapshot") }
            snapshots[id] = Snapshot(id: id, participant: p, certs: c, revocations: r)
        default:
            throw TandemError.invalid("registry: \(o.kind) is not a registry object")
        }
        return id
    }

    public func certificate(_ id: ID) -> Certificate? { certs[id] }
    public var allCertificates: [Certificate] { Array(certs.values) }
    public var revocationIDs: [ID] { Array(revs.keys) }

    // MARK: §6.9 rules

    /// `cuts t r c`: revocation `r`, whose target is `t`, retires `c`.
    func cuts(_ t: Certificate, _ r: Revocation, _ c: Certificate) -> Bool {
        t.participant == c.participant && (t.keys == c.keys || (!r.retained.contains(c.id) && !c.issued.contains(r.id)))
    }

    /// `c` survives every revocation in `rs` whose target is known.
    func survives(_ rs: [Revocation], _ c: Certificate) -> Bool {
        rs.allSatisfy { r in certs[r.target].map { !cuts($0, r, c) } ?? true }
    }

    func revocations(_ ids: [ID]) -> ([Revocation], [ID]) {
        var found: [Revocation] = [], missing: [ID] = []
        for id in ids { if let r = revs[id] { found.append(r) } else { missing.append(id) } }
        return (found, missing)
    }

    /// `certOK`: a valid certificate, with every proof of §6.9 and its dependencies held.
    public func certificateValidity(_ id: ID) -> Validity { certOK(id, depth: 0) }

    func certOK(_ id: ID, depth: Int) -> Validity {
        guard let c = certs[id] else { return .pending([id]) }
        guard depth < 64 else { return .invalid("approval chain too deep") }
        guard c.request.verify("self", publicKey: c.sign) else { return .invalid("request not signed by its own keys") }
        let (issued, missingRevs) = revocations(c.issued)
        guard missingRevs.isEmpty else { return .pending(missingRevs) }
        let missingTargets = issued.map(\.target).filter { certs[$0] == nil }
        guard missingTargets.isEmpty else { return .pending(missingTargets) }
        let registration = try? Control.registrationID(hub: pin.hub, sign: c.sign, agree: c.agree, enrollment: c.enrollment)
        switch c.how {
        case Control.first:
            guard c.authority == nil, c.participant == registration else { return .invalid("first admission of other keys") }
            return c.object.verify("sig", publicKey: pin.hub) ? .valid : .invalid("not signed by the hub")
        case Control.creatorRecovery:
            guard c.authority == nil, c.participant == pin.creator else { return .invalid("recovery for someone but the creator") }
            return c.object.verify("sig", publicKey: pin.hub) ? .valid : .invalid("not signed by the hub")
        case Control.invited:
            guard let capID = c.authority else { return .invalid("invited without a capability") }
            guard let cap = capabilities[capID] else { return .pending([capID]) }
            guard c.participant == registration else { return .invalid("invitation for other keys") }
            guard c.request.verify("approver", publicKey: cap.key) else { return .invalid("claim not signed by the invite key") }
            return c.object.verify("sig", publicKey: cap.key) ? .valid : .invalid("not signed by the invite key")
        case Control.byDevice:
            guard let srcID = c.authority else { return .invalid("no approving device") }
            guard let src = certs[srcID] else { return .pending([srcID]) }
            guard src.participant == c.participant else { return .invalid("approved by another participant's device") }
            guard c.request.verify("approver", publicKey: src.sign) else { return .invalid("not approved by that device") }
            guard survives(issued, src) else { return .invalid("approving device revoked at issuance") }
            guard c.object.verify("sig", publicKey: pin.hub) else { return .invalid("not signed by the hub") }
            return certOK(srcID, depth: depth + 1)
        case Control.byPairing:
            guard let dlID = c.authority else { return .invalid("no delegation") }
            guard let dl = delegations[dlID] else { return .pending([dlID]) }
            guard dl.participant == c.participant else { return .invalid("delegation for another participant") }
            guard c.request.verify("approver", publicKey: dl.key) else { return .invalid("claim not signed by the link key") }
            guard let src = certs[dl.source] else { return .pending([dl.source]) }
            guard src.participant == dl.participant, dl.object.verify("sig", publicKey: src.sign)
            else { return .invalid("delegation not signed by a device of that participant") }
            guard survives(issued, src) else { return .invalid("delegating device revoked at issuance") }
            guard c.object.verify("sig", publicKey: pin.hub) else { return .invalid("not signed by the hub") }
            return certOK(dl.source, depth: depth + 1)
        default:
            return .invalid("unknown admission")
        }
    }

    /// `revOK`: signed by the hub (checked on arrival), its target held, its retained set of that participant.
    public func revocationValidity(_ id: ID) -> Validity {
        guard let r = revs[id] else { return .pending([id]) }
        guard let t = certs[r.target] else { return .pending([r.target]) }
        var missing: [ID] = []
        for k in r.retained {
            guard let c = certs[k] else { missing.append(k); continue }
            if c.participant != t.participant { return .invalid("retains another participant's device") }
        }
        return missing.isEmpty ? .valid : .pending(missing)
    }

    /// `active`: held, valid, and surviving the revocations `rs` (a snapshot's, or every one held).
    public func activity(_ id: ID, under revocationIDs: [ID]? = nil) -> Validity {
        let ok = certificateValidity(id)
        guard ok.isValid, let c = certs[id] else { return ok }
        let (rs, missing) = revocations(revocationIDs ?? Array(revs.keys))
        guard missing.isEmpty else { return .pending(missing) }
        let checked = Validity.all(rs.map { revocationValidity($0.id) })
        guard checked.isValid else { return checked }
        return survives(rs, c) ? .valid : .invalid("revoked")
    }

    // MARK: Snapshots (§6.9)

    /// A snapshot of everything held for one participant: its certificates and the valid revocations of them.
    public func currentSnapshot(of participant: ID) throws -> ControlObject {
        let mine = certs.values.filter { $0.participant == participant && certificateValidity($0.id).isValid }.map(\.id)
        let revoked = revs.values.filter { r in
            certs[r.target]?.participant == participant && revocationValidity(r.id).isValid
        }.map(\.id)
        return ControlObject(kind: "snapshot", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "participant": .id(participant), "certs": .set(mine.sorted { $0.lexicographicallyPrecedes($1) }),
            "revocations": .set(revoked.sorted { $0.lexicographicallyPrecedes($1) })])
    }

    /// Whether `certificate` may act under the snapshot `snapshot`: the snapshot is its participant's and
    /// lists it, every object it names is valid, and the certificate survives its revocations.
    public func authorizes(snapshot snapshotID: ID, certificate certID: ID) -> Validity {
        guard let s = snapshots[snapshotID] else { return .pending([snapshotID]) }
        guard let c = certs[certID] else { return .pending([certID]) }
        guard s.participant == c.participant, s.certs.contains(certID) else { return .invalid("not in its snapshot") }
        let named = Validity.all(s.certs.map(certificateValidity) + s.revocations.map(revocationValidity))
        guard named.isValid else { return named }
        return activity(certID, under: s.revocations)
    }

    /// `Extends`: `source` includes every certificate and revocation of `floor` (§6.9).
    public func extends(_ source: ID, floor: ID) -> Validity {
        guard let s = snapshots[source] else { return .pending([source]) }
        guard let f = snapshots[floor] else { return .pending([floor]) }
        guard s.participant == f.participant else { return .invalid("snapshots of two participants") }
        return Set(f.certs).isSubset(of: Set(s.certs)) && Set(f.revocations).isSubset(of: Set(s.revocations))
            ? .valid : .invalid("does not extend the floor")
    }
}
