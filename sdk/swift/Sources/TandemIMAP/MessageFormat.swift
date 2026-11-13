import Foundation
import Tandem

/// The stored message of §10.4: building it, and reading it back in three steps.
public enum MessageFormat {
    /// The header fields a reader fetches along with the body.
    static let fields = ["Tandem-Channel", "Content-Type", "Content-Transfer-Encoding"]

    /// The full RFC 5322 message, headers in the order of §10.4, CRLF line ends,
    /// the envelope in base64 wrapped at 76 characters.
    public static func build(channel: String, envelope: Bytes, date: Date = Date(), id: String? = nil) throws -> Bytes {
        guard !channel.isEmpty, channel.utf8.allSatisfy({ $0 > 0x20 && $0 < 0x7f }), channel.utf8.count <= 900
        else { throw TandemError.invalid("channel tag must be printable ASCII") }
        let messageID = id ?? (0..<16).map { _ in UInt8.random(in: 0...255) }.hex
        let head = ["From: Tandem <tandem@invalid>", "Date: \(rfc5322(date))", "Message-ID: <\(messageID)@tandem.invalid>",
                    "Subject: tandem", "MIME-Version: 1.0", "Tandem-Version: 2", "Tandem-Channel: \(channel)",
                    "Content-Type: text/plain; charset=us-ascii", "Content-Transfer-Encoding: 7bit", ""]
        return Bytes(head.map { $0 + "\r\n" }.joined().utf8) + wrapped(envelope)
    }

    /// base64 in lines of 76 characters, each ending in CRLF.
    static func wrapped(_ envelope: Bytes) -> Bytes {
        let encoded = Bytes(Data(envelope).base64EncodedString().utf8)
        var out = Bytes(), i = 0
        while i < encoded.count {
            out += encoded[i..<min(i + 76, encoded.count)] + [0x0d, 0x0a]
            i += 76
        }
        return out
    }

    /// "Fri, 13 Nov 2026 09:30:00 +0000"
    static func rfc5322(_ date: Date) -> String {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        let c = calendar.dateComponents([.weekday, .day, .month, .year, .hour, .minute, .second], from: date)
        let days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
        let months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
        func two(_ n: Int?) -> String { let n = n ?? 0; return n < 10 ? "0\(n)" : "\(n)" }
        return "\(days[(c.weekday ?? 1) - 1]), \(c.day ?? 1) \(months[(c.month ?? 1) - 1]) \(c.year ?? 1970) "
            + "\(two(c.hour)):\(two(c.minute)):\(two(c.second)) +0000"
    }

    /// Reads a message from its header fields and its body text (§10.4): the channel tag,
    /// unfolded and trimmed, and the envelope. Throws if the body doesn't decode.
    public static func decode(header: Bytes, text: Bytes) throws -> (channel: String, envelope: Bytes) {
        guard let tag = field("Tandem-Channel", in: header), !tag.isEmpty, let channel = String(validating: tag, as: UTF8.self)
        else { throw TandemError.invalid("no Tandem-Channel header") }
        let type = field("Content-Type", in: header).map { String(decoding: $0, as: UTF8.self) } ?? "text/plain"
        let transfer = field("Content-Transfer-Encoding", in: header).map { String(decoding: $0, as: UTF8.self) } ?? "7bit"
        let decoded = try undoTransferEncoding(transfer, text)
        let chars = try decodeText(decoded, charset: parameter("charset", in: type) ?? "us-ascii")
        return (channel, try envelope(chars))
    }

    /// A header field's value, unfolded and trimmed; the first if repeated. Names compare case-insensitively.
    static func field(_ name: String, in block: Bytes) -> Bytes? {
        let want = Bytes(name.lowercased().utf8)
        var lines: [Bytes] = []
        for line in block.split(separator: 0x0a, omittingEmptySubsequences: false) {
            var line = Bytes(line)
            if line.last == 0x0d { line.removeLast() }
            if let first = line.first, first == 0x20 || first == 0x09, !lines.isEmpty { lines[lines.count - 1] += line }
            else { lines.append(line) }
        }
        for line in lines {
            guard let colon = line.firstIndex(of: UInt8(ascii: ":")) else { continue }
            let key = line[..<colon].filter { $0 != 0x20 && $0 != 0x09 }.map { $0 >= 0x41 && $0 <= 0x5a ? $0 + 0x20 : $0 }
            if key == want { return trim(Array(line[(colon + 1)...])) }
        }
        return nil
    }

    private static func trim(_ b: Bytes) -> Bytes {
        let ws: (UInt8) -> Bool = { $0 == 0x20 || $0 == 0x09 || $0 == 0x0d || $0 == 0x0a }
        guard let lo = b.firstIndex(where: { !ws($0) }), let hi = b.lastIndex(where: { !ws($0) }) else { return [] }
        return Array(b[lo...hi])
    }

    /// A Content-Type parameter, unquoted and lowercased.
    static func parameter(_ name: String, in value: String) -> String? {
        for part in value.split(separator: ";").dropFirst() {
            let kv = part.split(separator: "=", maxSplits: 1)
            guard kv.count == 2, kv[0].trimmingCharacters(in: .whitespaces).lowercased() == name else { continue }
            var v = kv[1].trimmingCharacters(in: .whitespaces)
            if v.hasPrefix("\""), v.hasSuffix("\""), v.count >= 2 { v = String(v.dropFirst().dropLast()) }
            return v.lowercased()
        }
        return nil
    }

    /// Step 1: 7bit and 8bit (and binary) need nothing; quoted-printable; base64.
    static func undoTransferEncoding(_ name: String, _ body: Bytes) throws -> Bytes {
        switch name.trimmingCharacters(in: .whitespaces).lowercased() {
        case "7bit", "8bit", "binary", "": return body
        case "quoted-printable": return quotedPrintable(body)
        case "base64":
            guard let data = Data(base64Encoded: Data(body.filter { !isSpace($0) })) else { throw TandemError.invalid("bad base64 transfer encoding") }
            return Bytes(data)
        default: throw TandemError.invalid("unknown transfer encoding")
        }
    }

    /// RFC 2045 §6.7: "=XX" is a byte, "=" at a line end is a soft break. Other "=" are kept.
    static func quotedPrintable(_ b: Bytes) -> Bytes {
        var out = Bytes(), i = 0
        out.reserveCapacity(b.count)
        func hex(_ c: UInt8) -> UInt8? {
            switch c {
            case 0x30...0x39: c - 0x30
            case 0x41...0x46: c - 0x37
            case 0x61...0x66: c - 0x57
            default: nil
            }
        }
        while i < b.count {
            if b[i] == UInt8(ascii: "=") {
                var j = i + 1
                while j < b.count, b[j] == 0x20 || b[j] == 0x09 { j += 1 }
                if j < b.count, b[j] == 0x0d, j + 1 < b.count, b[j + 1] == 0x0a { i = j + 2; continue }
                if j < b.count, b[j] == 0x0a { i = j + 1; continue }
                if j == b.count { i = j; continue }
                if i + 2 < b.count, let hi = hex(b[i + 1]), let lo = hex(b[i + 2]) { out.append(hi << 4 | lo); i += 3; continue }
            }
            out.append(b[i]); i += 1
        }
        return out
    }

    /// Step 2: bytes to text in the body's charset.
    static func decodeText(_ b: Bytes, charset: String) throws -> String {
        func decode(_ bytes: Bytes, _ encoding: String.Encoding) throws -> String {
            guard let s = String(bytes: bytes, encoding: encoding) else { throw TandemError.invalid("body is not valid \(charset)") }
            return s
        }
        func bom(_ bytes: Bytes, _ be: Bytes, _ le: Bytes, big: String.Encoding, little: String.Encoding) throws -> String {
            if bytes.starts(with: be) { return try decode(Array(bytes.dropFirst(be.count)), big) }
            if bytes.starts(with: le) { return try decode(Array(bytes.dropFirst(le.count)), little) }
            return try decode(bytes, big)   // RFC 2781 §4.3: big-endian without a BOM
        }
        switch charset {
        case "us-ascii", "ascii", "ansi_x3.4-1968":
            guard b.allSatisfy({ $0 < 0x80 }) else { throw TandemError.invalid("body is not ASCII") }
            return String(decoding: b, as: UTF8.self)
        case "utf-8", "utf8":
            guard let s = String(validating: b, as: UTF8.self) else { throw TandemError.invalid("body is not UTF-8") }
            return s
        case "utf-16": return try bom(b, [0xfe, 0xff], [0xff, 0xfe], big: .utf16BigEndian, little: .utf16LittleEndian)
        case "utf-16be": return try decode(b, .utf16BigEndian)
        case "utf-16le": return try decode(b, .utf16LittleEndian)
        case "utf-32": return try bom(b, [0, 0, 0xfe, 0xff], [0xff, 0xfe, 0, 0], big: .utf32BigEndian, little: .utf32LittleEndian)
        case "utf-32be": return try decode(b, .utf32BigEndian)
        case "utf-32le": return try decode(b, .utf32LittleEndian)
        case "iso-8859-1", "latin1": return try decode(b, .isoLatin1)
        default:
            let cf = CFStringConvertIANACharSetNameToEncoding(charset as CFString)
            guard cf != kCFStringEncodingInvalidId else { throw TandemError.invalid("unknown charset") }
            return try decode(b, String.Encoding(rawValue: CFStringConvertEncodingToNSStringEncoding(cf)))
        }
    }

    /// Step 3: drop all whitespace, base64-decode the rest.
    static func envelope(_ text: String) throws -> Bytes {
        let kept = text.unicodeScalars.filter { !$0.properties.isWhitespace && $0 != "\u{feff}" }
        guard kept.allSatisfy({ $0.isASCII }), let data = Data(base64Encoded: String(String.UnicodeScalarView(kept)))
        else { throw TandemError.invalid("body is not base64") }
        return Bytes(data)
    }

    private static func isSpace(_ c: UInt8) -> Bool { c == 0x20 || c == 0x09 || c == 0x0d || c == 0x0a }
}
