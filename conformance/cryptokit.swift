// Independent Apple backend for the conformance harness. NDJSON in/out.
import Foundation
import CryptoKit

enum Failure: Error { case invalid }
extension Data {
    init(hex: String) throws {
        guard hex.utf8.count % 2 == 0 else { throw Failure.invalid }
        self.init()
        var i = hex.startIndex
        while i < hex.endIndex {
            let end = hex.index(i, offsetBy: 2)
            guard let byte = UInt8(hex[i..<end], radix: 16) else { throw Failure.invalid }
            append(byte)
            i = end
        }
    }
    var hex: String { map { String(format: "%02x", $0) }.joined() }
}
func bytes(_ r: [String: Any], _ key: String) throws -> Data {
    guard let value = r[key] as? String else { throw Failure.invalid }
    return try Data(hex: value)
}
func text(_ r: [String: Any], _ key: String) throws -> String {
    guard let value = r[key] as? String else { throw Failure.invalid }
    return value
}
func hash(_ input: Data) -> Data { Data(SHA256.hash(data: input)) }
func derive(_ key: Data, _ salt: Data, _ info: String, _ count: Int = 32) -> Data {
    let key = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: key), salt: salt,
                                    info: Data(info.utf8), outputByteCount: count)
    return key.withUnsafeBytes { Data($0) }
}
func integer(_ n: UInt64, _ width: Int) -> Data {
    Data((0..<width).reversed().map { UInt8(truncatingIfNeeded: n >> ($0 * 8)) })
}
func lp(_ value: Data) -> Data { integer(UInt64(value.count), 4) + value }
func littleLess(_ a: Data, _ b: Data) -> Bool {
    a.reversed().lexicographicallyPrecedes(b.reversed())
}
func canonicalPoint(_ raw: Data) -> Bool {
    guard raw.count == 32 else { return false }
    var y = raw
    y[31] &= 127
    let p = try! Data(hex: "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")
    guard littleLess(y, p) else { return false }
    // The sign of x=0 must be zero (y=1 or -1).
    let zeroX = ["0100000000000000000000000000000000000000000000000000000000000000",
                 "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"]
    return !zeroX.contains(y.hex) || raw[31] & 128 == 0
}
let smallOrderKeys: Set<Data> = Set([
    "0100000000000000000000000000000000000000000000000000000000000000",
    "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
    "0000000000000000000000000000000000000000000000000000000000000080",
    "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
    "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
    "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
    "0000000000000000000000000000000000000000000000000000000000000000",
    "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa"
].map { try! Data(hex: $0) })
func signaturePolicy(_ publicKey: Data, _ signature: Data) -> Bool {
    guard signature.count == 64, canonicalPoint(publicKey), canonicalPoint(Data(signature.prefix(32))),
          !smallOrderKeys.contains(publicKey) else { return false }
    let order = try! Data(hex: "edd3f55c1a631258d69cf7a2def9de1400000000000000000000000000000010")
    return littleLess(Data(signature.suffix(32)), order)
}
func vector(_ entries: [[Any]]) throws -> Data {
    var values: [(Data, UInt64, UInt64)] = []
    for v in entries {
        guard v.count == 3, let dev = v[0] as? String, let m = v[1] as? NSNumber,
              let c = v[2] as? NSNumber else { throw Failure.invalid }
        let d = try Data(hex: dev)
        guard d.count == 8, m.uint64Value < 1 << 48, c.uint64Value < 1 << 16 else { throw Failure.invalid }
        values.append((d, m.uint64Value, c.uint64Value))
    }
    guard values.count <= 1024, Set(values.map { $0.0 }).count == values.count else { throw Failure.invalid }
    values.sort { $0.0.lexicographicallyPrecedes($1.0) }
    return integer(UInt64(values.count), 2) + values.reduce(Data()) { $0 + $1.0 + integer($1.1, 8) + integer($1.2, 2) }
}
func perform(_ r: [String: Any]) throws -> [String: Any] {
    switch try text(r, "op") {
    case "derive":
        let us = try bytes(r, "secret")
        guard us.count == 32 else { throw Failure.invalid }
        let sign = derive(us, Data(), "tandem/v1/user-sign")
        let agree = derive(us, Data(), "tandem/v1/user-agree")
        return ["sign": sign.hex, "agree": agree.hex,
                "uid": try Curve25519.Signing.PrivateKey(rawRepresentation: sign).publicKey.rawRepresentation.hex,
                "uka": try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: agree).publicKey.rawRepresentation.hex,
                "home": derive(us, Data(), "tandem/v1/home").hex]
    case "write":
        let who = try Curve25519.Signing.PrivateKey(rawRepresentation: bytes(r, "seed"))
        let op = (r["operation"] as! NSNumber).uint64Value
        let millis = (r["millis"] as! NSNumber).uint64Value
        let counter = (r["counter"] as! NSNumber).uint64Value
        guard (1...4).contains(op), millis < 1 << 48, counter < 1 << 16 else { throw Failure.invalid }
        var input = Data("TANDEM-WRITE-V1".utf8)
        input += try bytes(r, "scope") + bytes(r, "record")
        input += try lp(Data(text(r, "field").utf8)) + integer(op, 1)
        input += try lp(bytes(r, "element")) + hash(bytes(r, "value"))
        input += who.publicKey.rawRepresentation
        input += try bytes(r, "device")
        input += integer(millis, 8) + integer(counter, 2)
        input += try bytes(r, "ref")
        input += try vector(r["context"] as! [[Any]]) + vector(r["base"] as! [[Any]])
        return ["input": input.hex, "id": hash(input).hex, "signature": try who.signature(for: input).hex]
    case "verify", "verify-native":
        let pk = try bytes(r, "public"), signature = try bytes(r, "signature")
        if try text(r, "op") == "verify", !signaturePolicy(pk, signature) { return ["valid": false] }
        let key = try Curve25519.Signing.PublicKey(rawRepresentation: pk)
        return ["valid": try key.isValidSignature(signature, for: bytes(r, "message"))]
    case "seal":
        let salt = try bytes(r, "salt"), key = try bytes(r, "key")
        let kind = (r["kind"] as! NSNumber).uint8Value
        guard salt.count == 32, [UInt8(1), 3].contains(kind) else { throw Failure.invalid }
        let header = Data("TNDM".utf8) + Data([1, kind])
        let authenticated = header + Data(try text(r, "tag").utf8)
        let mk = SymmetricKey(data: derive(key, salt, "tandem/v1/msg"))
        let box = try ChaChaPoly.seal(bytes(r, "plaintext"), using: mk,
                                     nonce: ChaChaPoly.Nonce(data: Data(repeating: 0, count: 12)), authenticating: authenticated)
        return ["envelope": (header + salt + box.ciphertext + box.tag).hex]
    case "open":
        let body = try bytes(r, "envelope")
        guard body.count >= 54, body.prefix(5) == Data("TNDM".utf8) + Data([1]), [1, 3].contains(body[5]) else { throw Failure.invalid }
        let salt = body.subdata(in: 6..<38)
        let mk = SymmetricKey(data: derive(try bytes(r, "key"), salt, "tandem/v1/msg"))
        let box = try ChaChaPoly.SealedBox(nonce: ChaChaPoly.Nonce(data: Data(repeating: 0, count: 12)),
                                         ciphertext: body.subdata(in: 38..<(body.count - 16)), tag: body.suffix(16))
        return ["plaintext": try ChaChaPoly.open(box, using: mk,
                    authenticating: body.prefix(6) + Data(text(r, "tag").utf8)).hex]
    case "hpke-seal":
        let key = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: bytes(r, "public"))
        var sender = try HPKE.Sender(recipientKey: key, ciphersuite: .Curve25519_SHA256_ChachaPoly, info: bytes(r, "info"))
        let ct = try sender.seal(bytes(r, "plaintext"), authenticating: bytes(r, "aad"))
        return ["sealed": (sender.encapsulatedKey + ct).hex]
    case "hpke-open":
        let sealed = try bytes(r, "sealed")
        guard sealed.count >= 48 else { throw Failure.invalid }
        let key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: bytes(r, "private"))
        var receiver = try HPKE.Recipient(privateKey: key, ciphersuite: .Curve25519_SHA256_ChachaPoly,
                                         info: bytes(r, "info"), encapsulatedKey: Data(sealed.prefix(32)))
        return ["plaintext": try receiver.open(Data(sealed.dropFirst(32)), authenticating: bytes(r, "aad")).hex]
    default: throw Failure.invalid
    }
}
while let line = readLine() {
    do {
        let request = try JSONSerialization.jsonObject(with: Data(line.utf8)) as! [String: Any]
        let result = try perform(request)
        print(String(decoding: try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]), as: UTF8.self))
    } catch {
        print(String(decoding: try! JSONSerialization.data(withJSONObject: ["error": String(describing: error)]), as: UTF8.self))
    }
    fflush(nil)
}
