import CryptoKit

// Control objects (§10.8): one schema, one encoder, one strict reader.
//
// Every object is a tag followed by typed fields; its ID is the hash of that
// input and its signatures cover the same bytes. `Control.schema` is the table
// of §10.8; `vectors/v2/control.json` carries a copy that the tests compare.

public enum FieldType: String, Sendable {
    case id, id16, opt, text, set, u8, u16, u32, u64, answer, mailboxes
    case bytes, request
}

/// A permitted mailbox in a genesis: host (lowercase), port and user, without credentials.
public struct Mailbox: Equatable, Sendable {
    public var host: Bytes, port: UInt16, user: Bytes
    public init(host: Bytes, port: UInt16, user: Bytes) { self.host = host; self.port = port; self.user = user }
}

public enum ControlValue: Equatable, Sendable {
    case id(Bytes)              // id and id16
    case opt(Bytes?)            // nil is none
    case text(Bytes)            // UTF-8, compared as bytes
    case set([Bytes])           // sorted, unique 32-byte IDs
    case int(UInt64)            // u8 to u64
    case answer(UInt8)          // a, n or l
    case mailboxes([Mailbox])
}

public struct ControlSpec: Sendable {
    public let tag: String
    public let fields: [(name: String, type: FieldType)]
    public let signatures: [String]          // a trailing "?" marks an optional signature
    public let extras: [(name: String, type: FieldType)]
}

public indirect enum ControlExtra: Equatable, Sendable {
    case bytes(Bytes)
    case object(ControlObject)
}

public struct ControlObject: Equatable, Sendable {
    public let kind: String
    public var fields: [String: ControlValue]
    public var signatures: [String: Bytes]
    public var extras: [String: ControlExtra]

    public init(kind: String, fields: [String: ControlValue], signatures: [String: Bytes] = [:],
                extras: [String: ControlExtra] = [:]) {
        self.kind = kind; self.fields = fields; self.signatures = signatures; self.extras = extras
    }
}

public enum Control {
    public static let maxText = 256, maxSet = 65_536, maxMailboxes = 64
    public static let first: UInt64 = 1, invited: UInt64 = 2, byDevice: UInt64 = 3, byPairing: UInt64 = 4,
                      creatorRecovery: UInt64 = 5
    public static let rejected: UInt64 = 1, pairingCancelled: UInt64 = 2, inviteCancelled: UInt64 = 3, paired: UInt64 = 4

    /// The table of §10.8, in its order.
    public static let order = ["genesis", "capsule", "snapshot", "registration", "request", "certificate", "revocation",
                               "invitation", "delegation", "receipt", "home-epoch", "grant", "key-request", "leave",
                               "answer"]

    public static let schema: [String: ControlSpec] = [
        "genesis": ControlSpec(tag: "TANDEM-GENESIS-V2", fields: [("hub", .id), ("agree", .id), ("hubkey", .id), ("creator", .id),
            ("mailboxes", .mailboxes)], signatures: ["sig"], extras: []),
        "capsule": ControlSpec(tag: "TANDEM-CAPSULE-V2", fields: [("hub", .id), ("genesis", .id), ("sealed", .id),
            ("supersedes", .set)], signatures: ["sig"], extras: [("capsule", .bytes)]),
        "snapshot": ControlSpec(tag: "tandem/v2/auth-snapshot", fields: [("hub", .id), ("genesis", .id),
            ("participant", .id), ("certs", .set), ("revocations", .set)], signatures: [], extras: []),
        "registration": ControlSpec(tag: "TANDEM-REGISTRATION-V2", fields: [("hub", .id), ("sign", .id), ("agree", .id),
            ("enrollment", .id16)], signatures: [], extras: []),
        "request": ControlSpec(tag: "TANDEM-REQUEST-V2", fields: [("hub", .id), ("genesis", .id), ("participant", .id),
            ("sign", .id), ("agree", .id), ("label", .text), ("how", .u8), ("authority", .opt), ("enrollment", .id16)],
            signatures: ["self", "approver?"], extras: []),
        "certificate": ControlSpec(tag: "TANDEM-CERT-V2", fields: [("hub", .id), ("genesis", .id), ("request", .id),
            ("issued", .set)], signatures: ["sig"], extras: [("body", .request)]),
        "revocation": ControlSpec(tag: "TANDEM-REVOKE-V2", fields: [("hub", .id), ("genesis", .id), ("target", .id),
            ("retained", .set)], signatures: ["sig"], extras: []),
        "invitation": ControlSpec(tag: "TANDEM-INVITE-V2", fields: [("hub", .id), ("genesis", .id), ("key", .id),
            ("invite", .id16), ("expires", .u64), ("label", .text)], signatures: ["sig"], extras: []),
        "delegation": ControlSpec(tag: "TANDEM-DELEGATION-V2", fields: [("hub", .id), ("genesis", .id),
            ("participant", .id), ("source", .id), ("key", .id), ("token", .id16), ("expires", .u64), ("package", .id),
            ("gate", .id)], signatures: ["sig"], extras: []),
        "receipt": ControlSpec(tag: "TANDEM-RECEIPT-V2", fields: [("hub", .id), ("genesis", .id), ("outcome", .u8),
            ("subject", .id), ("signer", .opt)], signatures: ["sig"], extras: []),
        "home-epoch": ControlSpec(tag: "TANDEM-HOME-EPOCH-V2", fields: [("hub", .id), ("genesis", .id),
            ("participant", .id), ("n", .u32), ("parents", .set), ("key", .id), ("recipients", .set), ("snapshot", .id),
            ("signer", .id), ("auth", .id)], signatures: ["sig"], extras: []),
        "grant": ControlSpec(tag: "TANDEM-GRANT-V2", fields: [("hub", .id), ("collection", .id16), ("recipient", .id),
            ("admission", .id), ("at", .id), ("epoch", .id), ("key", .id), ("signer", .id), ("auth", .id)],
            signatures: ["sig"], extras: []),
        "key-request": ControlSpec(tag: "TANDEM-KEYREQ-V2", fields: [("hub", .id), ("collection", .id16),
            ("participant", .id), ("have", .id), ("signer", .id), ("auth", .id)], signatures: ["sig"], extras: []),
        "leave": ControlSpec(tag: "TANDEM-LEAVE-V2", fields: [("hub", .id), ("collection", .id16), ("participant", .id),
            ("at", .id), ("signer", .id), ("auth", .id)], signatures: ["sig"], extras: []),
        "answer": ControlSpec(tag: "TANDEM-ANSWER-V2", fields: [("hub", .id), ("collection", .id16),
            ("participant", .id), ("admission", .id), ("answer", .answer), ("event", .id16), ("signer", .id),
            ("auth", .id)], signatures: ["sig"], extras: []),
    ]

    static func fail(_ what: String) -> TandemError { .invalid("control: " + what) }

    static func spec(_ kind: String) throws -> ControlSpec {
        guard let s = schema[kind] else { throw fail("unknown type \(kind)") }
        return s
    }

    static func textBytes(_ t: Bytes) throws -> Bytes {
        guard t.count <= maxText, validUTF8(t) else { throw fail("text must be UTF-8 of at most 256 bytes") }
        var out = Bytes(); out.lp(t); return out
    }

    static func mailboxBytes(_ m: Mailbox) throws -> Bytes {
        guard m.host.allSatisfy({ $0 < 0x80 && !(0x41...0x5a).contains($0) }) else { throw fail("mailbox host must be lowercase ASCII") }
        guard m.port > 0 else { throw fail("mailbox port") }
        var out = try textBytes(m.host); out.u16(m.port); out += try textBytes(m.user); return out
    }

    static func fieldBytes(_ type: FieldType, _ value: ControlValue?) throws -> Bytes {
        guard let value else { throw fail("missing field") }
        switch (type, value) {
        case let (.id, .id(b)) where b.count == 32: return b
        case let (.id16, .id(b)) where b.count == 16: return b
        case (.opt, .opt(nil)): return Bytes(repeating: 0, count: 32)
        case let (.opt, .opt(b?)) where b.count == 32 && b.contains(where: { $0 != 0 }): return b
        case let (.text, .text(t)): return try textBytes(t)
        case let (.set, .set(xs)):
            guard xs.count <= maxSet, xs.allSatisfy({ $0.count == 32 }),
                  zip(xs, xs.dropFirst()).allSatisfy({ $0.lexicographicallyPrecedes($1) })
            else { throw fail("a set holds sorted, unique 32-byte IDs") }
            var out = Bytes(); out.u32(UInt32(xs.count)); xs.forEach { out += $0 }; return out
        case let (.u8, .int(n)) where n <= 0xff: return [UInt8(n)]
        case let (.u16, .int(n)) where n <= 0xffff: var o = Bytes(); o.u16(UInt16(n)); return o
        case let (.u32, .int(n)) where n <= 0xffff_ffff: var o = Bytes(); o.u32(UInt32(n)); return o
        case let (.u64, .int(n)) where n <= 1 << 53: var o = Bytes(); o.u64(n); return o
        case let (.answer, .answer(a)) where [0x61, 0x6e, 0x6c].contains(a): return [a]
        case let (.mailboxes, .mailboxes(ms)):
            guard ms.count <= maxMailboxes else { throw fail("too many mailboxes") }
            let encoded = try ms.map(mailboxBytes)
            guard zip(encoded, encoded.dropFirst()).allSatisfy({ $0.lexicographicallyPrecedes($1) })
            else { throw fail("mailboxes must be sorted and unique") }
            var out = Bytes(); out.u32(UInt32(encoded.count)); encoded.forEach { out += $0 }; return out
        default: throw fail("a \(type.rawValue) field holds the wrong value")
        }
    }

    // MARK: Derivations (§10.8)

    public static func inviteKey(secret: Bytes, hub: Bytes, invite: Bytes) throws -> Curve25519.Signing.PrivateKey {
        try .init(rawRepresentation: KDF(secret, salt: hub + invite, info: "tandem/v2/invite-key"))
    }
    public static func pairingInviteKey(link: Bytes, hub: Bytes, token: Bytes) throws -> Curve25519.Signing.PrivateKey {
        try .init(rawRepresentation: KDF(link, salt: hub + token, info: "tandem/v2/pair-invite"))
    }
    public static func pairingKey(link: Bytes, hub: Bytes, token: Bytes) -> Bytes {
        KDF(link, salt: hub + token, info: "tandem/v2/pair-key")
    }
    public static func pairingTag(link: Bytes, hub: Bytes, token: Bytes) -> String {
        B32.encode(KDF(link, salt: hub + token, info: "tandem/v2/pair-tag", count: 20))
    }
    /// The opaque tag a wake hint names (§10.3): computable by the hub's participants, nothing else.
    public static func wakeTag(directoryKey: Bytes, hub: Bytes) -> String {
        B32.encode(KDF(directoryKey, salt: hub, info: "tandem/v2/wake-tag", count: 20))
    }
    public static func directoryScope(hub: Bytes) -> Bytes { Array(H(Bytes("tandem/v2/directory".utf8), hub).prefix(16)) }
    public static func registrationID(hub: Bytes, sign: Bytes, agree: Bytes, enrollment: Bytes) throws -> Bytes {
        try ControlObject(kind: "registration", fields: ["hub": .id(hub), "sign": .id(sign), "agree": .id(agree),
                                                         "enrollment": .id(enrollment)]).id()
    }

    static func packageAAD(hub: Bytes, token: Bytes) -> Bytes { Bytes("TANDEM-PAIR-PACKAGE-V2".utf8) + hub + token }

    public static func sealPackage(link: Bytes, gate: Bytes, hub: Bytes, token: Bytes, plaintext: Bytes) throws -> Bytes {
        let key = SymmetricKey(data: KDF(link + gate, salt: token, info: "tandem/v2/pair-package"))
        let box = try ChaChaPoly.seal(plaintext, using: key, authenticating: packageAAD(hub: hub, token: token))
        return Bytes(box.nonce) + Bytes(box.ciphertext) + Bytes(box.tag)
    }
    public static func openPackage(link: Bytes, gate: Bytes, hub: Bytes, token: Bytes, package: Bytes) throws -> Bytes {
        guard package.count > 28 else { throw fail("package too short") }
        let key = SymmetricKey(data: KDF(link + gate, salt: token, info: "tandem/v2/pair-package"))
        let box = try ChaChaPoly.SealedBox(combined: package)
        return Bytes(try ChaChaPoly.open(box, using: key, authenticating: packageAAD(hub: hub, token: token)))
    }

    static func gateAAD(_ hub: Bytes, _ token: Bytes, _ participant: Bytes) -> Bytes {
        Bytes("TANDEM-PAIR-GATE-V2".utf8) + hub + token + participant
    }
    public static func sealGate(managerAgreement: Bytes, hub: Bytes, token: Bytes, participant: Bytes, secret: Bytes) throws -> Bytes {
        try Envelope.sealHPKE(to: managerAgreement, info: "tandem/v2/pair-gate", aad: gateAAD(hub, token, participant),
                              plaintext: secret)
    }
    public static func openGate(_ key: Curve25519.KeyAgreement.PrivateKey, hub: Bytes, token: Bytes, participant: Bytes,
                                gate: Bytes) throws -> Bytes {
        try Envelope.openHPKE(key, info: "tandem/v2/pair-gate", aad: gateAAD(hub, token, participant), sealed: gate)
    }

    static func recoveryAAD(_ hub: Bytes, _ epoch: Bytes) -> Bytes { Bytes("TANDEM-HOME-RECOVERY-V2".utf8) + hub + epoch }
    public static func sealHomeRecovery(managerAgreement: Bytes, hub: Bytes, epoch: Bytes, homeKey: Bytes) throws -> Bytes {
        try Envelope.sealHPKE(to: managerAgreement, info: "tandem/v2/home-recovery", aad: recoveryAAD(hub, epoch),
                              plaintext: homeKey)
    }
    public static func openHomeRecovery(_ key: Curve25519.KeyAgreement.PrivateKey, hub: Bytes, epoch: Bytes,
                                        box: Bytes) throws -> Bytes {
        try Envelope.openHPKE(key, info: "tandem/v2/home-recovery", aad: recoveryAAD(hub, epoch), sealed: box)
    }
}

extension ControlObject {
    var spec: ControlSpec { get throws { try Control.spec(kind) } }

    /// The exact bytes the ID hashes and the signatures cover.
    public func signedInput() throws -> Bytes {
        let s = try spec
        var out = Bytes(s.tag.utf8)
        for f in s.fields { out += try Control.fieldBytes(f.type, fields[f.name]) }
        return out
    }

    public func id() throws -> Bytes { H(try signedInput()) }

    public mutating func sign(_ name: String, with key: Curve25519.Signing.PrivateKey) throws {
        guard try spec.signatures.contains(where: { $0 == name || $0 == name + "?" }) else {
            throw Control.fail("\(kind) has no signature \(name)")
        }
        signatures[name] = Bytes(try key.signature(for: try signedInput()))
    }

    /// Strict verification (§5.5) of one named signature against a public key.
    public func verify(_ name: String, publicKey: Bytes) -> Bool {
        guard let sig = signatures[name], let input = try? signedInput() else { return false }
        return Ed25519.verify(signature: sig, message: input, publicKey: publicKey)
    }

    // MARK: JSON

    /// Strict JSON with one spelling: `type`, the fields in schema order, the signatures, then extras.
    public func json() throws -> Bytes {
        _ = try signedInput()
        let s = try spec
        var out = JSONWriter()
        out.open()
        out.member("type") { $0.string(Bytes(kind.utf8)) }
        for f in s.fields {
            let v = fields[f.name]!
            out.member(f.name) { w in
                switch v {
                case let .id(b): w.string(Bytes(B64u.encode(b).utf8))
                case let .opt(b): if let b { w.string(Bytes(B64u.encode(b).utf8)) } else { w.raw("null") }
                case let .text(t): w.string(t)
                case let .set(xs): w.array(xs) { $0.string(Bytes(B64u.encode($1).utf8)) }
                case let .int(n): w.raw(String(n))
                case let .answer(a): w.string([a])
                case let .mailboxes(ms):
                    w.array(ms) { w, m in
                        w.open()
                        w.member("host") { $0.string(m.host) }
                        w.member("port") { $0.raw(String(m.port)) }
                        w.member("user") { $0.string(m.user) }
                        w.close()
                    }
                }
            }
        }
        for raw in s.signatures {
            let name = raw.hasSuffix("?") ? String(raw.dropLast()) : raw
            if let sig = signatures[name] {
                out.member(name) { $0.string(Bytes(B64u.encode(sig).utf8)) }
            } else if !raw.hasSuffix("?") {
                throw Control.fail("missing signature \(name)")
            }
        }
        for e in s.extras {
            guard let extra = extras[e.name] else { continue }
            switch extra {
            case let .bytes(b): out.member(e.name) { $0.string(Bytes(B64u.encode(b).utf8)) }
            case let .object(o): let inner = try o.json(); out.member(e.name) { $0.raw(inner) }
            }
        }
        out.close()
        return out.bytes
    }

    public static func parse(_ data: Bytes, expect: String? = nil) throws -> ControlObject {
        try parse(JSON.parse(data), expect: expect)
    }

    public static func parse(_ value: JSON, expect: String? = nil) throws -> ControlObject {
        guard case .object = value, case let .string(k)? = value["type"], let kind = String(validating: k, as: UTF8.self)
        else { throw Control.fail("not a control object") }
        if let expect, expect != kind { throw Control.fail("expected \(expect), got \(kind)") }
        let s = try Control.spec(kind)
        var fields: [String: ControlValue] = [:]
        for f in s.fields {
            guard let v = value[f.name] else { throw Control.fail("missing \(f.name)") }
            fields[f.name] = try read(f.type, v)
        }
        var object = ControlObject(kind: kind, fields: fields)
        _ = try object.signedInput()
        for raw in s.signatures {
            let optional = raw.hasSuffix("?"), name = optional ? String(raw.dropLast()) : raw
            switch value[name] {
            case let .string(t)?:
                guard let sig = B64u.decode(t), sig.count == 64 else { throw Control.fail("signature \(name)") }
                object.signatures[name] = sig
            case .null?, nil:
                guard optional else { throw Control.fail("missing signature \(name)") }
            default: throw Control.fail("signature \(name) is not a string")
            }
        }
        for e in s.extras {
            guard let v = value[e.name] else { continue }
            switch e.type {
            case .bytes:
                guard case let .string(t) = v, let b = B64u.decode(t) else { throw Control.fail(e.name) }
                object.extras[e.name] = .bytes(b)
            case .request: object.extras[e.name] = .object(try parse(v, expect: "request"))
            default: throw Control.fail("extra \(e.name)")
            }
        }
        if case let .bytes(sealed)? = object.extras["capsule"], case let .id(h)? = fields["sealed"], H(sealed) != h {
            throw Control.fail("capsule bytes do not hash to sealed")
        }
        if case let .object(body)? = object.extras["body"], case let .id(r)? = fields["request"], try body.id() != r {
            throw Control.fail("body is not the named request")
        }
        return object
    }

    static func read(_ type: FieldType, _ v: JSON) throws -> ControlValue {
        func id(_ v: JSON, _ n: Int) throws -> Bytes {
            guard case let .string(t) = v, let b = B64u.decode(t), b.count == n else { throw Control.fail("\(type.rawValue)") }
            return b
        }
        switch type {
        case .id: return .id(try id(v, 32))
        case .id16: return .id(try id(v, 16))
        case .opt: if case .null = v { return .opt(nil) }; return .opt(try id(v, 32))
        case .text: guard case let .string(t) = v else { throw Control.fail("text") }; return .text(t)
        case .set:
            guard case let .array(xs) = v else { throw Control.fail("set") }
            return .set(try xs.map { try id($0, 32) })
        case .u8, .u16, .u32, .u64:
            guard case let .int(n) = v, n >= 0 else { throw Control.fail(type.rawValue) }
            return .int(UInt64(n))
        case .answer:
            guard case let .string(t) = v, t.count == 1 else { throw Control.fail("answer") }
            return .answer(t[0])
        case .mailboxes:
            guard case let .array(xs) = v else { throw Control.fail("mailboxes") }
            return .mailboxes(try xs.map { m in
                guard case let .string(h)? = m["host"], case let .int(p)? = m["port"], (1...65535).contains(p),
                      case let .string(u)? = m["user"] else { throw Control.fail("mailbox") }
                return Mailbox(host: h, port: UInt16(p), user: u)
            })
        case .bytes, .request: throw Control.fail("not a field type")
        }
    }
}

/// Compact JSON written like Python's `json.dumps(ensure_ascii=False, separators=(",", ":"))`,
/// so every SDK writes protocol JSON with the same bytes.
public struct JSONWriter: Sendable {
    public private(set) var bytes = Bytes()
    var first = [true]

    public init() {}

    /// One object: `build` adds its members in order.
    public static func object(_ build: (inout JSONWriter) -> Void) -> Bytes {
        var w = JSONWriter(); w.open(); build(&w); w.close(); return w.bytes
    }

    public mutating func member(_ name: String, b64u value: Bytes) { member(name) { $0.string(Bytes(B64u.encode(value).utf8)) } }
    public mutating func member(_ name: String, text value: Bytes) { member(name) { $0.string(value) } }
    public mutating func member(_ name: String, int value: UInt64) { member(name) { $0.raw(String(value)) } }
    public mutating func member(_ name: String, json value: Bytes) { member(name) { $0.raw(value) } }

    mutating func comma() { if first[first.count - 1] { first[first.count - 1] = false } else { bytes.append(0x2c) } }
    public mutating func open() { bytes.append(0x7b); first.append(true) }
    public mutating func close() { bytes.append(0x7d); first.removeLast() }
    public mutating func raw(_ s: String) { bytes += Bytes(s.utf8) }
    public mutating func raw(_ b: Bytes) { bytes += b }

    public mutating func member(_ name: String, _ value: (inout JSONWriter) -> Void) {
        comma(); string(Bytes(name.utf8)); bytes.append(0x3a); value(&self)
    }

    public mutating func array<T>(_ xs: [T], _ each: (inout JSONWriter, T) -> Void) {
        bytes.append(0x5b); first.append(true)
        for x in xs { comma(); each(&self, x) }
        bytes.append(0x5d); first.removeLast()
    }

    public mutating func string(_ s: Bytes) {
        bytes.append(0x22)
        for c in s {
            switch c {
            case 0x22: bytes += [0x5c, 0x22]
            case 0x5c: bytes += [0x5c, 0x5c]
            case 0x0a: bytes += [0x5c, 0x6e]
            case 0x0d: bytes += [0x5c, 0x72]
            case 0x09: bytes += [0x5c, 0x74]
            case 0x08: bytes += [0x5c, 0x62]
            case 0x0c: bytes += [0x5c, 0x66]
            case 0..<0x20:
                let hex = Array("0123456789abcdef".utf8)
                bytes += [0x5c, 0x75, 0x30, 0x30, hex[Int(c >> 4)], hex[Int(c & 0xf)]]
            default: bytes.append(c)
            }
        }
        bytes.append(0x22)
    }
}
