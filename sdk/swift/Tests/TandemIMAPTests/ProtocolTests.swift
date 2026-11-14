import Foundation
import Tandem
@testable import TandemIMAP
import XCTest

func bytes(_ s: String) -> Bytes { Bytes(s.utf8) }

/// A response with "{n}" literals: pieces alternate between line text and literal bytes.
func withLiterals(_ pieces: [Bytes]) -> Bytes {
    var out = Bytes()
    for (i, p) in pieces.enumerated() {
        if i % 2 == 0 { out += p } else { out += bytes("{\(p.count)}\r\n") + p }
    }
    return out
}

final class ParserTests: XCTestCase {
    func parse(_ s: String) throws -> Response { try Parser.response(bytes(s)) }

    func testStatusAndCodes() throws {
        XCTAssertEqual(try parse("* OK [CAPABILITY IMAP4rev1 IDLE UIDPLUS AUTH=PLAIN] ready"),
                       .untagged(.status(Status(kind: .ok, code: .capability(["IMAP4REV1", "IDLE", "UIDPLUS", "AUTH=PLAIN"]), text: "ready"))))
        XCTAssertEqual(try parse("T1 OK [APPENDUID 38505 3955] APPEND completed"),
                       .tagged("T1", Status(kind: .ok, code: .appendUID(validity: 38505, uids: [3955]), text: "APPEND completed")))
        XCTAssertEqual(try parse("T2 OK [APPENDUID 1 4:6] done"), .tagged("T2", Status(kind: .ok, code: .appendUID(validity: 1, uids: [4, 5, 6]), text: "done")))
        XCTAssertEqual(try parse("T2 OK [APPENDUID 1 9,4:5] done"), .tagged("T2", Status(kind: .ok, code: .appendUID(validity: 1, uids: [9, 4, 5]), text: "done")))
        XCTAssertEqual(try parse("* OK [COPYUID 7 1:2 10:11] moved"),
                       .untagged(.status(Status(kind: .ok, code: .copyUID(validity: 7, source: try UIDSet(parsing: "1:2"),
                                                                           destination: try UIDSet(parsing: "10:11")), text: "moved"))))
        XCTAssertEqual(try parse("* OK [UIDVALIDITY 3857529045] UIDs valid"),
                       .untagged(.status(Status(kind: .ok, code: .uidValidity(3857529045), text: "UIDs valid"))))
        XCTAssertEqual(try parse("* OK [HIGHESTMODSEQ 715194045007]"),
                       .untagged(.status(Status(kind: .ok, code: .highestModSeq(715194045007), text: ""))))
        XCTAssertEqual(try parse("* OK [NOMODSEQ] Sorry"), .untagged(.status(Status(kind: .ok, code: .noModSeq, text: "Sorry"))))
        XCTAssertEqual(try parse("T3 NO [ALREADYEXISTS] Mailbox exists"),
                       .tagged("T3", Status(kind: .no, code: .other("ALREADYEXISTS", ""), text: "Mailbox exists")))
        XCTAssertEqual(try parse("* OK [PERMANENTFLAGS (\\Deleted \\Seen \\*)] Limited"),
                       .untagged(.status(Status(kind: .ok, code: .other("PERMANENTFLAGS", "(\\Deleted \\Seen \\*)"), text: "Limited"))))
        XCTAssertEqual(try parse("T4 BAD"), .tagged("T4", Status(kind: .bad, code: nil, text: "")))
        XCTAssertEqual(try parse("* BYE Autologout"), .untagged(.status(Status(kind: .bye, code: nil, text: "Autologout"))))
        XCTAssertEqual(try parse("+ idling"), .continuation("idling"))
        XCTAssertEqual(try parse("+"), .continuation(""))
    }

    func testMailboxData() throws {
        XCTAssertEqual(try parse("* 3 EXISTS"), .untagged(.exists(3)))
        XCTAssertEqual(try parse("* 0 RECENT"), .untagged(.recent(0)))
        XCTAssertEqual(try parse("* 2 EXPUNGE"), .untagged(.expunge(2)))
        XCTAssertEqual(try parse("* VANISHED (EARLIER) 1:3,5"), .untagged(.vanished(earlier: true, try UIDSet(parsing: "1:3,5"))))
        XCTAssertEqual(try parse("* VANISHED 7"), .untagged(.vanished(earlier: false, UIDSet([7]))))
        XCTAssertEqual(try parse("* SEARCH 2 84 882"), .untagged(.search([2, 84, 882])))
        XCTAssertEqual(try parse("* SEARCH"), .untagged(.search([])))
        XCTAssertEqual(try parse("* SEARCH 1 3 (MODSEQ 917162500)"), .untagged(.search([1, 3])))
        XCTAssertEqual(try parse("* ESEARCH (TAG \"T3\") UID ALL 1:3,5"), .untagged(.esearch(tag: "T3", uid: true, all: try UIDSet(parsing: "1:3,5"))))
        XCTAssertEqual(try parse("* ESEARCH (TAG \"T3\") UID"), .untagged(.esearch(tag: "T3", uid: true, all: UIDSet())))
        XCTAssertEqual(try parse("* ESEARCH (TAG \"T3\") UID COUNT 2 ALL 4,9"), .untagged(.esearch(tag: "T3", uid: true, all: UIDSet([4, 9]))))
        XCTAssertEqual(try parse("* ENABLED QRESYNC"), .untagged(.enabled(["QRESYNC"])))
        XCTAssertEqual(try parse("* CAPABILITY IMAP4rev2 X-GM-EXT-1"), .untagged(.capability(["IMAP4REV2", "X-GM-EXT-1"])))
        XCTAssertEqual(try parse("* FLAGS (\\Answered \\Seen $Forwarded)"), .untagged(.flags(["\\Answered", "\\Seen", "$Forwarded"])))
        XCTAssertEqual(try parse("* STATUS Tandem (MESSAGES 3)"), .untagged(.other("STATUS")))
    }

    func testListAndNamespace() throws {
        XCTAssertEqual(try parse("* LIST (\\HasNoChildren \\Trash) \"/\" \"[Gmail]/Trash\""),
                       .untagged(.list(flags: ["\\HasNoChildren", "\\Trash"], delimiter: bytes("/"), name: bytes("[Gmail]/Trash"))))
        XCTAssertEqual(try parse("* LIST (\\All) \"/\" [Gmail]/All"), .untagged(.list(flags: ["\\All"], delimiter: bytes("/"), name: bytes("[Gmail]/All"))))
        XCTAssertEqual(try parse("* LIST () \".\" INBOX.Tandem"), .untagged(.list(flags: [], delimiter: bytes("."), name: bytes("INBOX.Tandem"))))
        XCTAssertEqual(try Parser.response(bytes("* LIST () NIL {6}\r\nTandem")), .untagged(.list(flags: [], delimiter: nil, name: bytes("Tandem"))))
        XCTAssertEqual(try parse("* LIST (\\Noselect) \"/\" \"a \\\"b\\\\\" (\"CHILDINFO\" (\"SUBSCRIBED\"))"),
                       .untagged(.list(flags: ["\\Noselect"], delimiter: bytes("/"), name: bytes("a \"b\\"))))
        XCTAssertEqual(try parse("* NAMESPACE ((\"\" \"/\")) NIL NIL"), .untagged(.namespace(personal: [Namespace(prefix: [], delimiter: bytes("/"))])))
        XCTAssertEqual(try parse("* NAMESPACE ((\"INBOX.\" \".\")) NIL ((\"#shared.\" \".\"))"),
                       .untagged(.namespace(personal: [Namespace(prefix: bytes("INBOX."), delimiter: bytes("."))])))
        XCTAssertEqual(try parse("* NAMESPACE NIL NIL NIL"), .untagged(.namespace(personal: [])))
    }

    func testFetch() throws {
        let header = bytes("Tandem-Channel: abc\r\nContent-Type: text/plain\r\n\r\n"), text = bytes("QUJD\r\n")
        let raw = withLiterals([bytes("* 1 FETCH (UID 5 RFC822.SIZE 900 BODY[HEADER.FIELDS (TANDEM-CHANNEL CONTENT-TYPE)] "), header,
                                bytes(" BODY[TEXT]<0> "), text, bytes(" FLAGS (\\Seen) MODSEQ (12) X-GM-MSGID 1278455344230334865)")])
        var item = FetchItem(uid: 5, size: 900, modseq: 12, gmailID: 1278455344230334865, flags: ["\\Seen"])
        item.sections = ["HEADER.FIELDS": header, "TEXT": text]
        XCTAssertEqual(try Parser.response(raw), .untagged(.fetch(1, item)))
        XCTAssertEqual(try parse("* 2 FETCH (UID 6 BODY[TEXT] NIL)"), .untagged(.fetch(2, FetchItem(uid: 6))))
        XCTAssertEqual(try parse("* 2 FETCH (BODY[HEADER.FIELDS (\"Tandem-Channel\")] \"x\" UID 6)"),
                       .untagged(.fetch(2, FetchItem(uid: 6, sections: ["HEADER.FIELDS": bytes("x")]))))
        XCTAssertEqual(try parse("* 2 FETCH (UID 6 INTERNALDATE \"17-Jul-1996 02:44:25 -0700\" X-GM-LABELS (\\Inbox \"a b\"))"),
                       .untagged(.fetch(2, FetchItem(uid: 6))))
        XCTAssertEqual(try parse("* 3 FETCH (FLAGS ())"), .untagged(.fetch(3, FetchItem(flags: []))))
    }

    func testMalformedInputThrows() {
        let bad = ["", "*", "* ", "* 5", "* 5 FETCH", "* 5 FETCH (UID", "* 5 FETCH (UID x)", "* 5 FETCH (UID 5", "T1 MAYBE", "T1",
                   "* OK [APPENDUID 1] x", "* OK [APPENDUID 1 x] y", "* OK [UIDVALIDITY", "* 99999999999 EXISTS", "* 5 EXISTS now",
                   "* LIST (\\x) \"/\" \"unterminated", "* VANISHED 0", "* VANISHED 1:", "* VANISHED (EARLIER", "* SEARCH 1 x",
                   "* 1 FETCH (BODY[TEXT] {5}\r\nab)", "* 1 FETCH (BODY[TEXT] {99999999999}\r\n)", "* 1 FETCH (BODY[TEXT \"x\")",
                   "* NAMESPACE ((x))", "* ESEARCH (TAG) UID", "* 1 FETCH (MODSEQ (99999999999999999999))", "* FLAGS \"x\"",
                   "* 1 FETCH (X " + String(repeating: "(", count: 10_000) + ")"]
        for s in bad { XCTAssertThrowsError(try Parser.response(bytes(s)), s) }
    }

    /// Truncated, mutated and random responses parse or throw; none traps.
    func testFuzzNeverTraps() {
        let seeds = [withLiterals([bytes("* 1 FETCH (UID 5 BODY[HEADER.FIELDS (A B)] "), bytes("A: b\r\n\r\n"), bytes(" BODY[TEXT]<0> "), bytes("xyz"), bytes(")")]),
                     bytes("* OK [COPYUID 7 1:2,4 10:12] moved"), bytes("* ESEARCH (TAG \"T3\") UID ALL 1:3,5"),
                     bytes("* LIST (\\HasNoChildren \\Trash) \"/\" \"[Gmail]/Trash\""), bytes("* NAMESPACE ((\"INBOX.\" \".\")) NIL NIL"),
                     bytes("* VANISHED (EARLIER) 1:3,5"), bytes("T12 NO [ALREADYEXISTS] exists")]
        var rng = SystemRandomNumberGenerator()
        let alphabet = bytes(" ()[]{}<>\"\\*+:,~0123456789\r\nABCUIDFETCHNIL")
        for seed in seeds {
            for n in 0...seed.count { _ = try? Parser.response(Array(seed.prefix(n))) }
            for _ in 0..<2000 {
                var m = seed
                for _ in 0..<Int.random(in: 1...4, using: &rng) {
                    let at = Int.random(in: 0..<m.count, using: &rng)
                    switch Int.random(in: 0..<3, using: &rng) {
                    case 0: m[at] = alphabet.randomElement(using: &rng)!
                    case 1: m.insert(alphabet.randomElement(using: &rng)!, at: at)
                    default: m.remove(at: at)
                    }
                    if m.isEmpty { break }
                }
                _ = try? Parser.response(m)
            }
        }
        for _ in 0..<5000 {
            let random = (0..<Int.random(in: 0...64, using: &rng)).map { _ in alphabet.randomElement(using: &rng)! }
            _ = try? Parser.response(random)
            var framer = Framer(limit: 1 << 10)
            framer.append(random)
            while let r = try? framer.next() { _ = try? Parser.response(r) }
        }
    }

    func testFramerSplitsResponsesAndLiterals() throws {
        let literal = bytes("line {3}\r\nwith CRLF\r\n* 9 EXISTS\r\n")   // a literal that looks like responses
        let first = withLiterals([bytes("* 1 FETCH (UID 1 BODY[TEXT] "), literal, bytes(")")])
        let stream = first + bytes("\r\n* 2 EXISTS\r\nT1 OK done\r\n")
        for size in [1, 2, 7, stream.count] {
            var framer = Framer(), out: [Bytes] = []
            var i = 0
            while i < stream.count {
                framer.append(Array(stream[i..<min(i + size, stream.count)]))
                i += size
                while let r = try framer.next() { out.append(r) }
            }
            XCTAssertEqual(out, [first, bytes("* 2 EXISTS"), bytes("T1 OK done")], "chunk size \(size)")
        }
        var framer = Framer(limit: 100)
        framer.append(bytes("* 1 FETCH (BODY[TEXT] {1000}\r\n"))
        XCTAssertThrowsError(try framer.next())
        framer = Framer()
        framer.append(bytes("* OK bare\n"))
        XCTAssertThrowsError(try framer.next())
    }

    func testUIDSet() throws {
        let set = try UIDSet(parsing: "5,1:3,4,9:7,20")
        XCTAssertEqual(set.description, "1:5,7:9,20")
        XCTAssertEqual(set.count, 9)
        XCTAssertTrue(set.contains(8)); XCTAssertFalse(set.contains(6)); XCTAssertFalse(set.contains(21))
        XCTAssertEqual(set.subtracting(try UIDSet(parsing: "2,8:30")).description, "1,3:5,7")
        XCTAssertEqual(set.intersection(try UIDSet(parsing: "3:8,100")).description, "3:5,7:8")
        XCTAssertEqual(set.subtracting(try UIDSet(parsing: "1:4294967295")), UIDSet())
        var grown = UIDSet()
        for u: UInt32 in [1, 2, 3, 7, 5, 6, 2] { grown.insert(u) }
        XCTAssertEqual(grown.description, "1:3,5:7")
        XCTAssertEqual(try UIDSet(parsing: "4:*").max, .max)
        for bad in ["", "0", "1:", ":2", "a", "1,,2", "1:2:3", "4294967296"] { XCTAssertThrowsError(try UIDSet(parsing: bad), bad) }
    }

    func testCommandArguments() {
        XCTAssertEqual(Part.astring(bytes("Tandem")), .raw(bytes("Tandem")))
        XCTAssertEqual(Part.astring(bytes("INBOX.Tandem")), .raw(bytes("INBOX.Tandem")))
        XCTAssertEqual(Part.astring(bytes("[Gmail]/All Mail")), .raw(bytes("\"[Gmail]/All Mail\"")))
        XCTAssertEqual(Part.astring(bytes("pa\"ss\\word")), .raw(bytes("\"pa\\\"ss\\\\word\"")))
        XCTAssertEqual(Part.astring(bytes("")), .raw(bytes("\"\"")))
        XCTAssertEqual(Part.astring(bytes("pässword")), .literal(bytes("pässword")))
        XCTAssertEqual(Part.astring(bytes("a\r\nb")), .literal(bytes("a\r\nb")))
    }

    func testCursorAndIDs() throws {
        let c = Cursor(validity: 42, modseq: 7, last: 9, known: try UIDSet(parsing: "1:3,9"))
        XCTAssertEqual(c.text, "imap1:42:7:9:1:3,9")
        XCTAssertEqual(try Cursor(c.text), c)
        XCTAssertEqual(try Cursor("imap1:42:0:0:"), Cursor(validity: 42, modseq: 0, last: 0, known: UIDSet()))
        XCTAssertNil(try Cursor(""))
        for bad in ["x", "imap1:1:2:3", "imap2:1:2:3:", "imap1:a:0:0:", "imap1:1:0:0:0"] { XCTAssertThrowsError(try Cursor(bad), bad) }
        XCTAssertEqual(MessageID.make(42, 7), "42.7")
        XCTAssertTrue(MessageID.parse("42.7")! == (42, 7))
        XCTAssertNil(MessageID.parse("42")); XCTAssertNil(MessageID.parse("42.0")); XCTAssertNil(MessageID.parse("a.1"))
    }
}

final class MessageFormatTests: XCTestCase {
    /// Header block and text of a whole message.
    func split(_ raw: Bytes) -> (Bytes, Bytes) {
        for i in 0..<(raw.count - 3) where Array(raw[i..<(i + 4)]) == bytes("\r\n\r\n") {
            return (Array(raw[..<(i + 4)]), Array(raw[(i + 4)...]))
        }
        return (raw, [])
    }

    func testBuildIsExactlyTheFormatOfSection10_4() throws {
        let envelope = Bytes(0..<100)
        let raw = try MessageFormat.build(channel: "abcdefgh", envelope: envelope, date: Date(timeIntervalSince1970: 1794562200), id: "00ff")
        let b64 = Data(envelope).base64EncodedString()
        let expected = "From: Tandem <tandem@tandem.airforce>\r\nDate: Fri, 13 Nov 2026 09:30:00 +0000\r\nMessage-ID: <00ff@tandem.airforce>\r\n"
            + "Subject: tandem\r\nMIME-Version: 1.0\r\nTandem-Version: 2\r\nTandem-Channel: abcdefgh\r\n"
            + "Content-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: 7bit\r\n\r\n"
            + b64.prefix(76) + "\r\n" + b64.dropFirst(76) + "\r\n"
        XCTAssertEqual(String(decoding: raw, as: UTF8.self), expected)
        let (header, text) = split(raw)
        XCTAssertTrue(text.split(separator: 0x0a).allSatisfy { $0.count <= 77 })
        XCTAssertEqual(try MessageFormat.decode(header: header, text: text).envelope, envelope)
        XCTAssertEqual(try MessageFormat.decode(header: header, text: text).channel, "abcdefgh")
        XCTAssertEqual(MessageFormat.rfc5322(Date(timeIntervalSince1970: 0)), "Thu, 1 Jan 1970 00:00:00 +0000")
        for size in [0, 1, 56, 57, 58, 1000] {
            let e = Bytes(repeating: 7, count: size)
            let (h, t) = split(try MessageFormat.build(channel: "c", envelope: e))
            XCTAssertEqual(try MessageFormat.decode(header: h, text: t).envelope, e)
        }
    }

    func testChannelMustBeASafeHeaderValue() {
        for bad in ["", "a b", "a\r\nBcc: x", "é", String(repeating: "a", count: 1000)] {
            XCTAssertThrowsError(try MessageFormat.build(channel: bad, envelope: [1]), bad)
        }
    }

    /// Bodies written by Python's email package (conformance/mail_transport.py): base64 of
    /// UTF-16 with a byte-order mark, and quoted-printable UTF-8 with soft breaks and "=3D".
    func testPythonEncodedBodies() throws {
        for (raw, envelope) in [(testUTF16Base64, Bytes(0..<60)), (testQuotedPrintable, Bytes(0..<200))] {
            let (header, text) = split(Bytes(Data(base64Encoded: raw)!))
            let decoded = try MessageFormat.decode(header: header, text: text)
            XCTAssertEqual(decoded.channel, "abcdefgh")
            XCTAssertEqual(decoded.envelope, envelope)
        }
    }

    func testThreeSteps() throws {
        let envelope = Bytes((0..<300).map { UInt8($0 % 251) })
        let wrapped = MessageFormat.wrapped(envelope)
        func header(_ cte: String, _ charset: String) -> Bytes {
            bytes("Tandem-Channel:\r\n  abc \r\nContent-Type: text/plain;\r\n charset=\"\(charset)\"\r\nContent-Transfer-Encoding: \(cte)\r\n\r\n")
        }
        func check(_ cte: String, _ charset: String, _ body: Bytes, line: UInt = #line) {
            XCTAssertEqual(try? MessageFormat.decode(header: header(cte, charset), text: body).envelope, envelope, line: line)
            XCTAssertEqual(try? MessageFormat.decode(header: header(cte, charset), text: body).channel, "abc", line: line)
        }
        let text = String(decoding: wrapped, as: UTF8.self)
        check("7bit", "us-ascii", wrapped)
        check("8bit", "utf-8", wrapped)
        check("base64", "utf-16", bytes(Data([0xfe, 0xff] + Bytes(text.data(using: .utf16BigEndian)!)).base64EncodedString(options: .lineLength76Characters)))
        check("base64", "utf-16", bytes(Data([0xff, 0xfe] + Bytes(text.data(using: .utf16LittleEndian)!)).base64EncodedString(options: .lineLength76Characters)))
        check("base64", "utf-16", bytes(text.data(using: .utf16BigEndian)!.base64EncodedString()))   // no BOM: big-endian
        check("BASE64", "UTF-16LE", bytes(text.data(using: .utf16LittleEndian)!.base64EncodedString()))
        check("base64", "utf-32", bytes(text.data(using: .utf32BigEndian)!.base64EncodedString()))
        check("quoted-printable", "utf-8", bytes(text.replacingOccurrences(of: "=", with: "=3D").replacingOccurrences(of: "A", with: "=41=\r\n")))
        check("quoted-printable", "windows-1252", bytes(text.replacingOccurrences(of: "\r\n", with: "=\r\n")))
        check("7bit", "iso-8859-1", wrapped)

        XCTAssertThrowsError(try MessageFormat.decode(header: header("7bit", "us-ascii"), text: wrapped + bytes("!")))
        XCTAssertThrowsError(try MessageFormat.decode(header: header("7bit", "us-ascii"), text: [0xc3, 0xa9] + wrapped))
        XCTAssertThrowsError(try MessageFormat.decode(header: header("x-uuencode", "us-ascii"), text: wrapped))
        XCTAssertThrowsError(try MessageFormat.decode(header: header("7bit", "x-no-such-charset"), text: wrapped))
        XCTAssertThrowsError(try MessageFormat.decode(header: header("base64", "us-ascii"), text: bytes("!!!")))
        XCTAssertThrowsError(try MessageFormat.decode(header: bytes("Content-Type: text/plain\r\n\r\n"), text: wrapped))
        // Without UTF-16 decoding (step 2), the base64 text is garbage.
        let utf16 = Bytes(text.data(using: .utf16BigEndian)!)
        XCTAssertThrowsError(try MessageFormat.envelope(String(decoding: utf16, as: UTF8.self)))
    }

    func testHeaderFields() {
        let block = bytes("tandem-channel: one\r\nTANDEM-CHANNEL: two\r\nContent-Type: text/plain;\r\n\tcharset=UTF-16\r\n\r\n")
        XCTAssertEqual(MessageFormat.field("Tandem-Channel", in: block), bytes("one"))
        XCTAssertEqual(MessageFormat.field("Content-Type", in: block), bytes("text/plain;\tcharset=UTF-16"))
        XCTAssertNil(MessageFormat.field("Subject", in: block))
        XCTAssertEqual(MessageFormat.parameter("charset", in: "text/plain; format=flowed; Charset=\"UTF-16\""), "utf-16")
        XCTAssertNil(MessageFormat.parameter("charset", in: "text/plain"))
    }
}
