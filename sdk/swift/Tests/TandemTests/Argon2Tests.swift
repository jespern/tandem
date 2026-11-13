import XCTest
@testable import Tandem
import TandemArgon2

final class Argon2Tests: XCTestCase {
    func testBLAKE2b() {
        // RFC 7693 Appendix A, then block boundaries and a short output, checked against libsodium.
        XCTAssertEqual(BLAKE2b.hash(Bytes("abc".utf8)).hex, "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d17d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923")
        XCTAssertEqual(BLAKE2b.hash([]).hex, "786a02f742015903c6c6fd852552d272912f4740e15847618a86e217f71f5419d25e1031afee585313896444934eb04b903a685b1448b755d56f701afe9be2ce")
        let cases = [127: "b6292669ccd38d5f01caae96ba272c76a879a45743afa0725d83b9ebb26665b731f1848c52f11972b6644f554c064fa90780dbbbf3a89d4fc31f67df3e5857ef",
                     128: "2319e3789c47e2daa5fe807f61bec2a1a6537fa03f19ff32e87eecbfd64b7e0e8ccff439ac333b040f19b0c4ddd11a61e24ac1fe0f10a039806c5dcc0da3d115",
                     129: "f59711d44a031d5f97a9413c065d1e614c417ede998590325f49bad2fd444d3e4418be19aec4e11449ac1a57207898bc57d76a1bcf3566292c20c683a5c4648f",
                     256: "93463ac058b6163eb43be3f5bb32b28541498f4e3366f1effe253ad44e1e076e41c3616046027c82a7124f8f4746668ad10b12e8e25a95ac8f3151df01cd5a93",
                     300: "3a482b7748b0bdc43c3d00c080890c10e57a9aa5618f78b86067eb7eaae4942acd96d827accbc16958364ae5b0df6105bbd3b15445092eba1137b5f69c1070f1"]
        for (n, digest) in cases {
            let data = (0..<n).map { UInt8($0 % 251) }
            XCTAssertEqual(BLAKE2b.hash(data).hex, digest, "\(n) bytes")
            var split = BLAKE2b(count: 64)
            split.update(Array(data.prefix(n / 3))); split.update(Array(data.dropFirst(n / 3)))
            XCTAssertEqual(split.finalize().hex, digest, "\(n) bytes in two updates")
        }
        XCTAssertEqual(BLAKE2b.hash(Bytes("abc".utf8), count: 20).hex, "384264f676f39536840523f284921cdc68b6846b")
    }

    func testRFC9106Argon2id() throws {
        // §5.3: four lanes, with a secret and associated data.
        let tag = try argon2id(password: Bytes(repeating: 1, count: 32), salt: Bytes(repeating: 2, count: 16),
                               profile: Argon2Profile(memoryKiB: 32, iterations: 3, lanes: 4), count: 32,
                               secret: Bytes(repeating: 3, count: 8), associatedData: Bytes(repeating: 4, count: 12))
        XCTAssertEqual(tag.hex, "0d640df58d78766c08c037a34a8b53c9d01ef0452d75b65eb52520e96b01e659")
    }

    func testArgon2idAgainstLibsodium() throws {
        // crypto_pwhash with ALG_ARGON2ID13: one lane, memory not a multiple of 4, tags past 64 bytes (H').
        let cases: [(t: Int, m: Int, count: Int, tag: String)] = [
            (1, 8, 32, "3a008963c65f7ffa7331a90babd1fd17ea9f03bd05356b857ceb43f7b7080adc"),
            (2, 256, 32, "8e39a7b45a996d379fda71d399f74299999273b1286be670f3786fd49251f12b"),
            (1, 103, 100, "200d84997f84544f4e4f52f1db1e64622bbce270b5747c549f696100ec6c9f2bc02ddba6b344fa01780a5bc71b49d5b4a1290840281cffd4c2ec25ff218ef12abef271a4bce72f046d5a79d441050155bd1cd0d33329dc97c18bd9458cb4851fbe5096a2"),
            (3, 64, 16, "32e124d9540bd551f50f54a1d1e5a8e7"),
            (4, 1024, 64, "f57ede6b754dc0957a58934def64e29a4718d166ea84b94726a3ed8a6ba0f5cec77e4173798e26e2d625493ca31b290e38fcb6b70d26a2b8054cd0527222f763")]
        for c in cases {
            let tag = try argon2id(password: Bytes("password".utf8), salt: Array(0..<16),
                                   profile: Argon2Profile(memoryKiB: c.m, iterations: c.t, lanes: 1), count: c.count)
            XCTAssertEqual(tag.hex, c.tag, "t=\(c.t) m=\(c.m)")
        }
    }

    func testArgon2idRefusesBadParameters() {
        let salt = Bytes(repeating: 0, count: 16)
        for (profile, salt, count) in [(Argon2Profile(memoryKiB: 7, iterations: 1, lanes: 1), salt, 32),
                                       (Argon2Profile(memoryKiB: 8, iterations: 0, lanes: 1), salt, 32),
                                       (Argon2Profile(memoryKiB: 8, iterations: 1, lanes: 0), salt, 32),
                                       (Argon2Profile(memoryKiB: 8, iterations: 1, lanes: 1), Array(salt.prefix(7)), 32),
                                       (Argon2Profile(memoryKiB: 8, iterations: 1, lanes: 1), salt, 3)] {
            XCTAssertThrowsError(try argon2id(password: [], salt: salt, profile: profile, count: count))
        }
    }
}
