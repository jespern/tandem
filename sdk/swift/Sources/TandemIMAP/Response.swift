import Foundation
import Tandem

// IMAP responses (RFC 9051, RFC 3501) as far as the binding of §10.3 needs them.
// Malformed server input throws IMAPError.protocol; it never traps.

public enum IMAPError: Error, Equatable, Sendable {
    /// The server sent something this client can't parse, or didn't expect.
    case `protocol`(String)
    /// NO or BAD, with the response code's name ("" if none) and the text.
    case rejected(code: String, text: String)
    /// LOGIN or AUTHENTICATE was refused. Retrying won't help.
    case authentication(String)
    /// The server lacks something §10.3 requires.
    case unsupported(String)
    /// The network or TLS failed, or the server hung up.
    case connection(String)
    /// The store was closed.
    case closed
}

/// A set of UIDs as sorted, disjoint, non-adjacent ranges; IMAP sequence-set syntax.
struct UIDSet: Sendable, Equatable, CustomStringConvertible {
    private(set) var ranges: [ClosedRange<UInt32>] = []

    init() {}
    init<S: Sequence>(_ uids: S) where S.Element == UInt32 { self.init(ranges: uids.map { $0...$0 }) }
    init(ranges: [ClosedRange<UInt32>]) {
        for r in ranges.sorted(by: { $0.lowerBound < $1.lowerBound }) {
            if let last = self.ranges.last, UInt64(r.lowerBound) <= UInt64(last.upperBound) + 1 {
                self.ranges[self.ranges.count - 1] = last.lowerBound...Swift.max(last.upperBound, r.upperBound)
            } else {
                self.ranges.append(r)
            }
        }
    }
    /// "1:3,5,9:*". `*` reads as the largest UID.
    init(parsing text: String) throws {
        var out: [ClosedRange<UInt32>] = []
        for item in text.split(separator: ",", omittingEmptySubsequences: false) {
            let ends = item.split(separator: ":", omittingEmptySubsequences: false)
            guard ends.count == 1 || ends.count == 2 else { throw IMAPError.protocol("bad sequence set") }
            let values = try ends.map { end -> UInt32 in
                if end == "*" { return .max }
                guard !end.isEmpty, end.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }), let n = UInt32(end), n > 0
                else { throw IMAPError.protocol("bad sequence set") }
                return n
            }
            out.append(Swift.min(values[0], values[values.count - 1])...Swift.max(values[0], values[values.count - 1]))
        }
        self.init(ranges: out)
    }

    var isEmpty: Bool { ranges.isEmpty }
    var count: UInt64 { ranges.reduce(0) { $0 + UInt64($1.upperBound - $1.lowerBound) + 1 } }
    var max: UInt32? { ranges.last?.upperBound }
    /// Every UID. Only for sets this client built, whose size it knows.
    var uids: [UInt32] { ranges.flatMap { Array($0) } }

    func contains(_ uid: UInt32) -> Bool {
        var lo = 0, hi = ranges.count
        while lo < hi {
            let mid = (lo + hi) / 2
            if ranges[mid].upperBound < uid { lo = mid + 1 } else { hi = mid }
        }
        return lo < ranges.count && ranges[lo].contains(uid)
    }
    mutating func insert(_ uid: UInt32) {
        if let last = ranges.last, uid > last.upperBound {
            if uid == last.upperBound + 1 { ranges[ranges.count - 1] = last.lowerBound...uid } else { ranges.append(uid...uid) }
        } else if ranges.isEmpty {
            ranges = [uid...uid]
        } else if !contains(uid) {
            self = UIDSet(ranges: ranges + [uid...uid])
        }
    }
    func subtracting(_ other: UIDSet) -> UIDSet {
        var out: [ClosedRange<UInt32>] = [], j = 0
        for r in ranges {
            var lo = UInt64(r.lowerBound)
            let hi = UInt64(r.upperBound)
            while j < other.ranges.count, UInt64(other.ranges[j].upperBound) < lo { j += 1 }
            var k = j
            while lo <= hi {
                guard k < other.ranges.count, UInt64(other.ranges[k].lowerBound) <= hi else {
                    out.append(UInt32(lo)...UInt32(hi)); break
                }
                let cut = other.ranges[k]
                if UInt64(cut.lowerBound) > lo { out.append(UInt32(lo)...cut.lowerBound - 1) }
                lo = UInt64(cut.upperBound) + 1
                k += 1
            }
        }
        return UIDSet(ranges: out)
    }
    func intersection(_ other: UIDSet) -> UIDSet { subtracting(subtracting(other)) }

    var description: String {
        ranges.map { $0.lowerBound == $0.upperBound ? "\($0.lowerBound)" : "\($0.lowerBound):\($0.upperBound)" }.joined(separator: ",")
    }
}

struct Status: Sendable, Equatable {
    enum Kind: String, Sendable { case ok = "OK", no = "NO", bad = "BAD", bye = "BYE", preauth = "PREAUTH" }
    var kind: Kind
    var code: Code?
    var text: String
}

/// Response codes (RFC 9051 §7.1, RFC 4315, RFC 7162).
enum Code: Sendable, Equatable {
    /// UIDs in the order the messages were appended.
    case appendUID(validity: UInt32, uids: [UInt32])
    case copyUID(validity: UInt32, source: UIDSet, destination: UIDSet)
    case uidValidity(UInt32), uidNext(UInt32), highestModSeq(UInt64), noModSeq
    case capability([String])
    /// Any other code: its name, uppercased, and its raw arguments.
    case other(String, String)

    var name: String {
        switch self {
        case .appendUID: "APPENDUID"
        case .copyUID: "COPYUID"
        case .uidValidity: "UIDVALIDITY"
        case .uidNext: "UIDNEXT"
        case .highestModSeq: "HIGHESTMODSEQ"
        case .noModSeq: "NOMODSEQ"
        case .capability: "CAPABILITY"
        case .other(let name, _): name
        }
    }
}

/// The parts of a FETCH response the binding reads.
struct FetchItem: Sendable, Equatable {
    var uid: UInt32?
    var size: UInt64?
    var modseq: UInt64?
    var gmailID: UInt64?
    var flags: [String]?
    /// Body sections by specifier, uppercased, without field lists: "HEADER.FIELDS", "TEXT", "".
    var sections: [String: Bytes] = [:]

    mutating func merge(_ other: FetchItem) {
        uid = other.uid ?? uid; size = other.size ?? size; modseq = other.modseq ?? modseq
        gmailID = other.gmailID ?? gmailID; flags = other.flags ?? flags
        sections.merge(other.sections) { $1 }
    }
}

struct Namespace: Sendable, Equatable {
    var prefix: Bytes
    var delimiter: Bytes?
}

enum Untagged: Sendable, Equatable {
    case status(Status)
    case capability([String]), enabled([String]), flags([String])
    case exists(UInt32), recent(UInt32), expunge(UInt32)
    case vanished(earlier: Bool, UIDSet)
    case fetch(UInt32, FetchItem)
    case search([UInt32])
    case esearch(tag: String?, uid: Bool, all: UIDSet)
    case list(flags: [String], delimiter: Bytes?, name: Bytes)
    case namespace(personal: [Namespace])
    /// QUOTA (RFC 9208): a root and its resources by uppercased name, (usage, limit).
    case quota(root: Bytes, resources: [QuotaResource])
    case other(String)
}

struct QuotaResource: Sendable, Equatable {
    var name: String, usage: UInt64, limit: UInt64
}

enum Response: Sendable, Equatable {
    case continuation(String)
    case tagged(String, Status)
    case untagged(Untagged)
}

/// A generic parenthesized value, for the parts of a response that aren't read field by field.
indirect enum Value: Sendable, Equatable {
    case atom(String), string(Bytes), null, list([Value])
}

private let SP = UInt8(ascii: " "), CR = UInt8(ascii: "\r"), LF = UInt8(ascii: "\n")
private let open = UInt8(ascii: "("), close = UInt8(ascii: ")"), dquote = UInt8(ascii: "\"")
private let lbrace = UInt8(ascii: "{"), rbrace = UInt8(ascii: "}"), lbracket = UInt8(ascii: "["), rbracket = UInt8(ascii: "]")
private let backslash = UInt8(ascii: "\\"), tilde = UInt8(ascii: "~")

/// Splits the byte stream into responses: a line, with any `{n}` literals it announces inline.
struct Framer {
    private var buffer = Bytes()
    private var start = 0, scan = 0
    let limit: Int

    init(limit: Int = 64 << 20) { self.limit = limit }

    mutating func append(_ bytes: Bytes) { buffer.append(contentsOf: bytes) }

    /// The next complete response without its final CRLF, or nil if more bytes are needed.
    mutating func next() throws -> Bytes? {
        while scan <= buffer.count {
            guard let lf = buffer[scan...].firstIndex(of: LF) else {
                scan = buffer.count
                if buffer.count - start > limit { throw IMAPError.protocol("response too large") }
                return nil
            }
            guard lf > start, buffer[lf - 1] == CR else { throw IMAPError.protocol("line without CRLF") }
            if let n = try literalLength(endingAt: lf - 1) {
                guard n <= limit - (lf + 1 - start) else { throw IMAPError.protocol("response too large") }
                scan = lf + 1 + n
                continue
            }
            let response = Array(buffer[start..<(lf - 1)])
            start = lf + 1; scan = start
            if start > 1 << 16, start * 2 > buffer.count { buffer.removeFirst(start); scan -= start; start = 0 }
            return response
        }
        if buffer.count - start > limit { throw IMAPError.protocol("response too large") }
        return nil
    }

    /// n if the line ending before `end` ends with "{n}", "{n+}" or "~{n}".
    private func literalLength(endingAt end: Int) throws -> Int? {
        var i = end - 1
        guard i >= start, buffer[i] == rbrace else { return nil }
        i -= 1
        if i >= start, buffer[i] == UInt8(ascii: "+") { i -= 1 }
        var n = 0, digits = 0
        var scale = 1
        while i >= start, buffer[i] >= 0x30, buffer[i] <= 0x39 {
            digits += 1
            guard digits <= 10 else { throw IMAPError.protocol("literal too large") }
            n += Int(buffer[i] - 0x30) * scale; scale *= 10
            i -= 1
        }
        guard digits > 0, i >= start, buffer[i] == lbrace else { return nil }
        return n
    }
}

struct Parser {
    private let b: Bytes
    private var i = 0
    private static let maxDepth = 32

    init(_ bytes: Bytes) { b = bytes }

    static func response(_ bytes: Bytes) throws -> Response {
        var p = Parser(bytes)
        switch p.peek {
        case nil: throw IMAPError.protocol("empty response")
        case UInt8(ascii: "+"):
            p.i += 1
            if p.peek == SP { p.i += 1 }
            return .continuation(p.rest())
        case UInt8(ascii: "*"):
            p.i += 1
            try p.space()
            return .untagged(try p.untagged())
        default:
            let tag = try p.atom()
            try p.space()
            let kind = try p.atom().uppercased()
            guard let k = Status.Kind(rawValue: kind), k == .ok || k == .no || k == .bad else { throw IMAPError.protocol("bad tagged status") }
            return .tagged(tag, try p.status(k))
        }
    }

    // MARK: - Responses

    private mutating func untagged() throws -> Untagged {
        let word = try atom()
        if word.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }) {
            let n = try number32(word)
            try space()
            let kind = try atom().uppercased()
            switch kind {
            case "EXISTS": try end(); return .exists(n)
            case "RECENT": try end(); return .recent(n)
            case "EXPUNGE": try end(); return .expunge(n)
            case "FETCH":
                try space()
                let item = try fetchItem()
                try end()
                return .fetch(n, item)
            default: return .other(kind)
            }
        }
        let name = word.uppercased()
        switch name {
        case "OK", "NO", "BAD", "BYE", "PREAUTH": return .status(try status(Status.Kind(rawValue: name)!))
        case "CAPABILITY": return .capability(try atoms())
        case "ENABLED": return .enabled(try atoms())
        case "FLAGS":
            try space()
            return .flags(try flagList())
        case "SEARCH":
            var out: [UInt32] = []
            while !atEnd {
                try space()
                if peek == open { _ = try value(0); continue }   // (MODSEQ n), RFC 7162
                out.append(try number32(try atom()))
            }
            return .search(out)
        case "ESEARCH": return try esearch()
        case "LIST", "LSUB":
            try space()
            let flags = try flagList()
            try space()
            let delimiter = try nstring()
            try space()
            return .list(flags: flags, delimiter: delimiter, name: try astring())
        case "NAMESPACE":
            try space()
            var personal: [Namespace] = []
            if case .list(let items) = try value(0) {
                for item in items {
                    guard case .list(let parts) = item, parts.count >= 2, case .string(let prefix) = parts[0]
                    else { throw IMAPError.protocol("bad NAMESPACE") }
                    if case .string(let d) = parts[1] { personal.append(Namespace(prefix: prefix, delimiter: d)) }
                    else if parts[1] == .null { personal.append(Namespace(prefix: prefix, delimiter: nil)) }
                    else { throw IMAPError.protocol("bad NAMESPACE") }
                }
            }
            return .namespace(personal: personal)
        case "QUOTA":
            try space()
            let root = try astring()
            try space()
            guard case .list(let items) = try value(0), items.count % 3 == 0 else { throw IMAPError.protocol("bad QUOTA") }
            var resources: [QuotaResource] = []
            for i in stride(from: 0, to: items.count, by: 3) {
                guard case .atom(let name) = items[i], case .atom(let used) = items[i + 1], case .atom(let limit) = items[i + 2],
                      let u = UInt64(used), let l = UInt64(limit) else { throw IMAPError.protocol("bad QUOTA") }
                resources.append(QuotaResource(name: name.uppercased(), usage: u, limit: l))
            }
            try end()
            return .quota(root: root, resources: resources)
        case "VANISHED":
            try space()
            var earlier = false
            if peek == open {
                guard case .list(let tags) = try value(0) else { throw IMAPError.protocol("bad VANISHED") }
                earlier = tags.contains(.atom("EARLIER")) || tags.contains(.atom("earlier"))
                try space()
            }
            let set = try UIDSet(parsing: try atom())
            try end()
            return .vanished(earlier: earlier, set)
        default:
            return .other(name)
        }
    }

    private mutating func status(_ kind: Status.Kind) throws -> Status {
        var code: Code?
        if atEnd { return Status(kind: kind, code: nil, text: "") }
        try space()
        if peek == lbracket {
            i += 1
            code = try responseCode()
            if peek == SP { i += 1 }
        }
        return Status(kind: kind, code: code, text: rest())
    }

    private mutating func responseCode() throws -> Code {
        var name = Bytes()
        while let c = peek, c != SP, c != rbracket {
            guard c > 0x20, c < 0x7f else { throw IMAPError.protocol("bad response code") }
            name.append(c); i += 1
        }
        var args = Bytes()
        if peek == SP {
            i += 1
            var depth = 0
            while let c = peek, !(c == rbracket && depth == 0) {
                if c == lbracket { depth += 1 } else if c == rbracket { depth -= 1 }
                args.append(c); i += 1
            }
        }
        guard peek == rbracket, !name.isEmpty else { throw IMAPError.protocol("unterminated response code") }
        i += 1
        let n = String(decoding: name, as: UTF8.self).uppercased(), a = String(decoding: args, as: UTF8.self)
        let parts = a.split(separator: " ").map(String.init)
        func count(_ k: Int) throws { guard parts.count == k else { throw IMAPError.protocol("bad \(n) code") } }
        switch n {
        case "APPENDUID":
            try count(2)
            return .appendUID(validity: try number32(parts[0]), uids: try Parser.ordered(parts[1]))
        case "COPYUID":
            try count(3)
            return .copyUID(validity: try number32(parts[0]), source: try UIDSet(parsing: parts[1]), destination: try UIDSet(parsing: parts[2]))
        case "UIDVALIDITY": try count(1); return .uidValidity(try number32(parts[0]))
        case "UIDNEXT": try count(1); return .uidNext(try number32(parts[0]))
        case "HIGHESTMODSEQ": try count(1); return .highestModSeq(try number64(parts[0]))
        case "NOMODSEQ": return .noModSeq
        case "CAPABILITY": return .capability(parts.map { $0.uppercased() })
        default: return .other(n, a)
        }
    }

    /// A uid-set in the order written, as APPENDUID lists a MULTIAPPEND's messages.
    static func ordered(_ text: String) throws -> [UInt32] {
        var out: [UInt32] = []
        for item in text.split(separator: ",", omittingEmptySubsequences: false) {
            let set = try UIDSet(parsing: String(item))
            guard UInt64(out.count) + set.count <= 1 << 20 else { throw IMAPError.protocol("APPENDUID set too large") }
            let ends = item.split(separator: ":")
            let ascending = ends.count < 2 || (UInt32(ends[0]) ?? 0) <= (UInt32(ends[1]) ?? 0)
            out += ascending ? set.uids : set.uids.reversed()
        }
        return out
    }

    private mutating func fetchItem() throws -> FetchItem {
        try expect(open)
        var item = FetchItem(), first = true
        while peek != close {
            if !first { try space() }
            first = false
            let name = try atom()
            try space()
            let upper = name.uppercased()
            switch upper {
            case "UID": item.uid = try number32(try atom())
            case "RFC822.SIZE": item.size = try number64(try atom())
            case "X-GM-MSGID": item.gmailID = try number64(try atom())
            case "MODSEQ":
                try expect(open)
                item.modseq = try number64(try atom())
                try expect(close)
            case "FLAGS": item.flags = try flagList()
            default:
                if let open = upper.firstIndex(of: "["), upper.hasPrefix("BODY[") || upper.hasPrefix("BINARY["),
                   let shut = upper.lastIndex(of: "]"), open < shut {
                    let spec = upper[upper.index(after: open)..<shut]
                    let key = String(spec.prefix { $0 != " " })
                    if let data = try nstring() { item.sections[key] = data }
                } else {
                    _ = try value(0)
                }
            }
        }
        i += 1
        return item
    }

    private mutating func esearch() throws -> Untagged {
        var tag: String?, uid = false, all = UIDSet()
        if peek == SP, i + 1 < b.count, b[i + 1] == open {
            i += 1
            guard case .list(let correlator) = try value(0), correlator.count == 2,
                  case .atom(let t) = correlator[0], t.uppercased() == "TAG", case .string(let s) = correlator[1]
            else { throw IMAPError.protocol("bad ESEARCH correlator") }
            tag = String(decoding: s, as: UTF8.self)
        }
        while !atEnd {
            try space()
            let name = try atom().uppercased()
            if name == "UID" { uid = true; continue }
            try space()
            if name == "ALL" { all = try UIDSet(parsing: try atom()) } else { _ = try value(0) }
        }
        return .esearch(tag: tag, uid: uid, all: all)
    }

    // MARK: - Tokens

    private var peek: UInt8? { i < b.count ? b[i] : nil }
    private var atEnd: Bool { i >= b.count }

    private mutating func expect(_ c: UInt8) throws {
        guard peek == c else { throw IMAPError.protocol("expected '\(Character(UnicodeScalar(c)))'") }
        i += 1
    }
    private mutating func space() throws { try expect(SP) }
    private mutating func end() throws { guard atEnd else { throw IMAPError.protocol("trailing data") } }
    private mutating func rest() -> String { defer { i = b.count }; return String(decoding: b[min(i, b.count)...], as: UTF8.self) }

    private func number32(_ s: String) throws -> UInt32 {
        guard !s.isEmpty, s.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }), let n = UInt32(s) else { throw IMAPError.protocol("bad number") }
        return n
    }
    private func number64(_ s: String) throws -> UInt64 {
        guard !s.isEmpty, s.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }), let n = UInt64(s), n <= UInt64(Int64.max)
        else { throw IMAPError.protocol("bad number") }
        return n
    }

    /// An atom; a bracketed section inside it ("BODY[HEADER.FIELDS (A B)]<0>", "[Gmail]/Trash") is kept whole.
    private mutating func atomBytes() throws -> Bytes {
        var out = Bytes()
        while let c = peek {
            if c == lbracket {
                var depth = 0, quoted = false
                repeat {
                    guard let d = peek else { throw IMAPError.protocol("unterminated bracket") }
                    if quoted {
                        if d == backslash { out.append(d); i += 1; guard let e = peek else { break }; out.append(e); i += 1; continue }
                        if d == dquote { quoted = false }
                    } else if d == dquote { quoted = true }
                    else if d == lbracket { depth += 1 }
                    else if d == rbracket { depth -= 1 }
                    else if d == CR || d == LF { throw IMAPError.protocol("bad atom") }
                    out.append(d); i += 1
                } while depth > 0
                continue
            }
            if c <= 0x20 || c == 0x7f || c == open || c == close || c == dquote || c == rbracket || (c == lbrace && out.isEmpty) { break }
            out.append(c); i += 1
        }
        guard !out.isEmpty else { throw IMAPError.protocol("expected an atom") }
        return out
    }
    private mutating func atom() throws -> String { String(decoding: try atomBytes(), as: UTF8.self) }

    private mutating func atoms() throws -> [String] {
        var out: [String] = []
        while !atEnd {
            try space()
            if atEnd { break }
            out.append(try atom().uppercased())
        }
        return out
    }

    private mutating func flagList() throws -> [String] {
        guard case .list(let items) = try value(0) else { throw IMAPError.protocol("expected a list") }
        return try items.map {
            guard case .atom(let a) = $0 else { throw IMAPError.protocol("bad flag") }
            return a
        }
    }

    private mutating func quoted() throws -> Bytes {
        try expect(dquote)
        var out = Bytes()
        while true {
            guard let c = peek, c != CR, c != LF else { throw IMAPError.protocol("unterminated string") }
            i += 1
            if c == dquote { return out }
            if c == backslash {
                guard let e = peek, e != CR, e != LF else { throw IMAPError.protocol("unterminated string") }
                i += 1; out.append(e)
            } else {
                out.append(c)
            }
        }
    }

    private mutating func literal() throws -> Bytes {
        if peek == tilde { i += 1 }
        try expect(lbrace)
        var n = 0, digits = 0
        while let c = peek, c >= 0x30, c <= 0x39 {
            digits += 1
            guard digits <= 10 else { throw IMAPError.protocol("literal too large") }
            n = n * 10 + Int(c - 0x30); i += 1
        }
        guard digits > 0 else { throw IMAPError.protocol("bad literal") }
        if peek == UInt8(ascii: "+") { i += 1 }
        try expect(rbrace); try expect(CR); try expect(LF)
        guard n <= b.count - i else { throw IMAPError.protocol("truncated literal") }
        defer { i += n }
        return Array(b[i..<(i + n)])
    }

    private mutating func string() throws -> Bytes {
        switch peek {
        case dquote: return try quoted()
        case lbrace, tilde: return try literal()
        default: throw IMAPError.protocol("expected a string")
        }
    }
    private mutating func nstring() throws -> Bytes? {
        if peek == dquote || peek == lbrace || peek == tilde { return try string() }
        guard try atom().uppercased() == "NIL" else { throw IMAPError.protocol("expected a string or NIL") }
        return nil
    }
    private mutating func astring() throws -> Bytes {
        if peek == dquote || peek == lbrace || peek == tilde { return try string() }
        return try atomBytes()
    }

    private mutating func value(_ depth: Int) throws -> Value {
        guard depth < Parser.maxDepth else { throw IMAPError.protocol("nesting too deep") }
        switch peek {
        case nil: throw IMAPError.protocol("expected a value")
        case open:
            i += 1
            var items: [Value] = []
            while peek != close {
                if !items.isEmpty { try space() }
                items.append(try value(depth + 1))
            }
            i += 1
            return .list(items)
        case dquote, lbrace, tilde: return .string(try string())
        default:
            let a = try atom()
            return a.uppercased() == "NIL" ? .null : .atom(a)
        }
    }
}
