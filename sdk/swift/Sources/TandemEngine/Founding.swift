import CryptoKit
import Tandem

/// The connection details a device needs for a hub, without authority: what
/// an invitation or pairing link carries and the creator keeps (§6.1, §13.1).
public struct MailboxAccount: Equatable, Sendable {
    public var host: String, port: UInt16, user: String, password: String
    public init(host: String, port: UInt16, user: String, password: String) {
        self.host = host; self.port = port; self.user = user; self.password = password
    }
    var mailbox: Mailbox { Mailbox(host: Bytes(host.lowercased().utf8), port: port, user: Bytes(user.utf8)) }

    /// The `mail` member of a link (§10.8): `{"host","port","user","pass"}`.
    func json() -> Bytes {
        JSONWriter.object { m in
            m.member("host", text: Bytes(host.utf8))
            m.member("port", int: UInt64(port))
            m.member("user", text: Bytes(user.utf8))
            m.member("pass", text: Bytes(password.utf8))
        }
    }

    static func parse(_ mail: JSON) throws -> MailboxAccount {
        guard case let .int(port)? = mail["port"], (1...65535).contains(port) else { throw TandemError.invalid("link: mail port") }
        return MailboxAccount(host: String(decoding: try mail.text("host"), as: UTF8.self), port: UInt16(port),
                              user: String(decoding: try mail.text("user"), as: UTF8.self),
                              password: String(decoding: try mail.text("pass"), as: UTF8.self))
    }
}

/// A protocol message that is not a signed control object (§10.8): the key and
/// recovery boxes, the pairing package and gate, grants in their delivery form.
public struct Notice: Equatable, Sendable {
    public let type: String
    public let fields: [(name: String, value: Bytes)]   // b64u in JSON, in this order

    public init(type: String, _ fields: [(String, Bytes)]) { self.type = type; self.fields = fields.map { ($0.0, $0.1) } }

    public static func == (a: Notice, b: Notice) -> Bool {
        a.type == b.type && a.fields.count == b.fields.count && zip(a.fields, b.fields).allSatisfy { $0 == $1 }
    }

    public func json() -> Bytes {
        JSONWriter.object { w in
            w.member("type", text: Bytes(type.utf8))
            for f in fields { w.member(f.name, b64u: f.value) }
        }
    }

    /// Read one notice of `type`, with the named b64u members.
    public static func parse(_ data: Bytes, type: String, fields names: [String]) throws -> Notice {
        let v = try JSON.parse(data)
        guard case let .string(t)? = v["type"], t == Bytes(type.utf8) else { throw TandemError.invalid("not a \(type) notice") }
        return Notice(type: type, try names.map { n in
            guard case let .string(s)? = v[n], let b = B64u.decode(s) else { throw TandemError.invalid("\(type): \(n)") }
            return (n, b)
        })
    }

    public subscript(name: String) -> Bytes? { fields.first { $0.name == name }?.value }
}

/// The random material a hub is founded from. Separate so tests can fix it.
public struct FoundingSecrets: Sendable {
    public var managementSeed: Bytes, directoryKey: Bytes, enrollment: Bytes, homeKey: Bytes
    public static func random() -> FoundingSecrets {
        func r(_ n: Int) -> Bytes { SymmetricKey(size: .init(bitCount: n * 8)).withUnsafeBytes { Bytes($0) } }
        return FoundingSecrets(managementSeed: r(32), directoryKey: r(32), enrollment: r(16), homeKey: r(32))
    }
}

/// Everything a new hub starts with (§6.1), except the capsule, which needs the hub password (§6.7).
public struct Founding: Sendable {
    public let pin: HubPin
    public let participant: ID
    public let genesis: ControlObject, certificate: ControlObject, snapshot: ControlObject, homeEpoch: ControlObject
    /// The first home key, sealed to the creator's own agreement key (for its inbox).
    public let homeKeyDelivery: Bytes
    /// The same key sealed to `MKa`, for the directory (§6.8).
    public let homeRecovery: Notice
    public let homeKey: Bytes, directoryKey: Bytes, managementSeed: Bytes

    /// The admin capsule (§6.7), once made with `Founder.capsule`.
    public var capsule: ControlObject?

    /// Directory-channel messages in publication order: each one's dependencies come first.
    public var directoryMessages: [Bytes] {
        get throws {
            try [genesis.json()] + (capsule.map { [try $0.json()] } ?? [])
                + [certificate.json(), snapshot.json(), homeRecovery.json()]
        }
    }
}

public enum Founder {
    /// Found a hub (§6.1) on `account` for the creator's keychain `keys`.
    public static func found(account: MailboxAccount, keys: DeviceKeys, label: String,
                             secrets: FoundingSecrets = .random()) throws -> Founding {
        let manager = try DeviceKeys.manager(seed: secrets.managementSeed)
        let hub = Bytes(manager.signing.publicKey.rawRepresentation)
        let sign = Bytes(keys.signing.publicKey.rawRepresentation), agree = Bytes(keys.agreement.publicKey.rawRepresentation)
        let participant = try Control.registrationID(hub: hub, sign: sign, agree: agree, enrollment: secrets.enrollment)

        var genesis = ControlObject(kind: "genesis", fields: ["hub": .id(hub),
            "agree": .id(Bytes(manager.agreement.publicKey.rawRepresentation)), "hubkey": .id(Tandem.commit(secrets.directoryKey)),
            "creator": .id(participant), "mailboxes": .mailboxes([account.mailbox])])
        try genesis.sign("sig", with: manager.signing)
        let pin = try HubPin(genesis: genesis)

        var request = ControlObject(kind: "request", fields: ["hub": .id(hub), "genesis": .id(pin.genesis),
            "participant": .id(participant), "sign": .id(sign), "agree": .id(agree), "label": .text(Bytes(label.utf8)),
            "how": .int(Control.first), "authority": .opt(nil), "enrollment": .id(secrets.enrollment)])
        try request.sign("self", with: keys.signing)
        var certificate = ControlObject(kind: "certificate", fields: ["hub": .id(hub), "genesis": .id(pin.genesis),
            "request": .id(try request.id()), "issued": .set([])], extras: ["body": .object(request)])
        try certificate.sign("sig", with: manager.signing)
        let certID = try certificate.id()

        let snapshot = ControlObject(kind: "snapshot", fields: ["hub": .id(hub), "genesis": .id(pin.genesis),
            "participant": .id(participant), "certs": .set([certID]), "revocations": .set([])])
        let homeEpoch = try Self.homeEpoch(pin: pin, participant: participant, n: 0, parents: [], homeKey: secrets.homeKey,
                                           recipients: [certID], signer: certID, auth: try snapshot.id(), keys: keys)
        let epochID = try homeEpoch.id()

        let delivery = try Envelope.sealInbox(hub: hub, uid: participant, agreement: agree,
                                              plaintext: homeKeyNotice(epoch: epochID, key: secrets.homeKey).json())
        let box = try Control.sealHomeRecovery(managerAgreement: Bytes(manager.agreement.publicKey.rawRepresentation),
                                               hub: hub, epoch: epochID, homeKey: secrets.homeKey)
        return Founding(pin: pin, participant: participant, genesis: genesis, certificate: certificate, snapshot: snapshot,
                        homeEpoch: homeEpoch, homeKeyDelivery: delivery,
                        homeRecovery: Notice(type: "home-recovery", [("epoch", epochID), ("bytes", box)]),
                        homeKey: secrets.homeKey, directoryKey: secrets.directoryKey, managementSeed: secrets.managementSeed)
    }

    /// Seal `MS` under the creator's hub password and sign the capsule (§6.7). Takes about a second and
    /// 1 GiB; `supersedes` names the capsules a password change replaces.
    public static func capsule(password: String, pin: HubPin, managementSeed: Bytes, supersedes: [ID] = []) throws -> ControlObject {
        let sealed = try Control.sealCapsule(password: password, hub: pin.hub, ms: managementSeed)
        var c = ControlObject(kind: "capsule", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis), "sealed": .id(H(sealed)),
            "supersedes": .set(supersedes.sorted { $0.lexicographicallyPrecedes($1) })], extras: ["capsule": .bytes(sealed)])
        try c.sign("sig", with: try DeviceKeys.manager(seed: managementSeed).signing)
        return c
    }

    /// A home-epoch start (§10.8), signed by one of the participant's admitted keychains.
    public static func homeEpoch(pin: HubPin, participant: ID, n: UInt64, parents: [ID], homeKey: Bytes, recipients: [ID],
                                 signer: ID, auth: ID, inventory: Inventory = Inventory(), keys: DeviceKeys) throws -> ControlObject {
        var e = ControlObject(kind: "home-epoch", fields: ["hub": .id(pin.hub), "genesis": .id(pin.genesis),
            "participant": .id(participant), "n": .int(n), "parents": .set(parents.sorted { $0.lexicographicallyPrecedes($1) }),
            "key": .id(Tandem.commit(homeKey)), "recipients": .set(recipients.sorted { $0.lexicographicallyPrecedes($1) }),
            "snapshot": .id(H(try inventory.bytes())), "signer": .id(signer), "auth": .id(auth)])
        try e.sign("sig", with: keys.signing)
        return e
    }

    /// `{"type":"home-key","epoch","k"}`, sealed to one recipient (§10.8).
    public static func homeKeyNotice(epoch: ID, key: Bytes) -> Notice { Notice(type: "home-key", [("epoch", epoch), ("k", key)]) }
}
