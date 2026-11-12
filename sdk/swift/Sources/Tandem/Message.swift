import Foundation

/// Reading a hub message (§10.4): undo the transfer encoding, decode the
/// charset, remove whitespace and base64-decode.
public enum HubMessage {
    public static func parse(_ raw: Bytes) throws -> (tag: String, envelope: Bytes) {
        var text = Bytes()  // CRLF to LF, on bytes: an 8bit body needn't be UTF-8
        text.reserveCapacity(raw.count)
        for (i, b) in raw.enumerated() where !(b == 0x0D && i + 1 < raw.count && raw[i + 1] == 0x0A) { text.append(b) }
        guard let split = text.indices.first(where: { text[$0] == 0x0A && $0 + 1 < text.count && text[$0 + 1] == 0x0A })
        else { throw TandemError.invalid("no message body") }
        var headers: [String: String] = [:]
        var last: String?
        for line in String(decoding: text[..<split], as: UTF8.self).split(separator: "\n", omittingEmptySubsequences: false) {
            if let first = line.first, first == " " || first == "\t", let name = last {
                headers[name] = (headers[name, default: ""] + " " + line.trimmingCharacters(in: .whitespaces))
                    .trimmingCharacters(in: .whitespaces)  // unfold
            } else if let colon = line.firstIndex(of: ":") {
                let name = line[..<colon].trimmingCharacters(in: .whitespaces).lowercased()
                headers[name] = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
                last = name
            }
        }
        let env = try decodeBody(contentType: headers["content-type"] ?? "",
                                 transferEncoding: headers["content-transfer-encoding"] ?? "",
                                 body: Array(text[(split + 2)...]))
        return (headers["tandem-channel"] ?? "", env)
    }

    public static func decodeBody(contentType: String, transferEncoding: String, body: Bytes) throws -> Bytes {
        let data: Bytes
        switch transferEncoding.trimmingCharacters(in: .whitespaces).lowercased() {
        case "", "7bit", "8bit", "binary": data = body
        case "base64": data = try decodeText(body)
        case "quoted-printable": data = try quotedPrintable(body)
        case let other: throw TandemError.invalid("unknown transfer encoding \(other)")
        }
        return try decodeText(Bytes(try decodeCharset(contentType, data).utf8))
    }

    /// Step 3: remove all whitespace and base64-decode (standard alphabet).
    static func decodeText(_ text: Bytes) throws -> Bytes {
        let s = text.filter { ![0x20, 0x09, 0x0A, 0x0D, 0x0B, 0x0C].contains($0) }
        guard let d = Data(base64Encoded: Data(s)) else { throw TandemError.invalid("body is not base64") }
        return Bytes(d)
    }

    static func quotedPrintable(_ b: Bytes) throws -> Bytes {
        var out = Bytes()
        var i = 0
        while i < b.count {
            if b[i] == UInt8(ascii: "=") {
                if i + 1 < b.count, b[i + 1] == 0x0A { i += 2; continue }  // soft break (CRLF already LF)
                if i + 2 < b.count, b[i + 1] == 0x0D, b[i + 2] == 0x0A { i += 3; continue }
                guard i + 2 < b.count, let v = Bytes(hex: String(decoding: b[i + 1...i + 2], as: UTF8.self))
                else { throw TandemError.invalid("bad quoted-printable") }
                out += v
                i += 3
            } else {
                out.append(b[i])
                i += 1
            }
        }
        return out
    }

    /// The text is base64, so every ASCII-compatible charset reads it the
    /// same; UTF-16 is decoded here.
    static func decodeCharset(_ contentType: String, _ data: Bytes) throws -> String {
        var cs = "us-ascii"
        for param in contentType.split(separator: ";").dropFirst() {
            let kv = param.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if kv.count == 2, kv[0].lowercased() == "charset" {
                cs = kv[1].trimmingCharacters(in: CharacterSet(charactersIn: "\"")).lowercased()
            }
        }
        switch cs {
        case "utf-16", "utf-16be", "utf-16le":
            guard data.count % 2 == 0 else { throw TandemError.invalid("odd UTF-16 length") }
            var d = data[...]
            var big = cs != "utf-16le"
            if cs == "utf-16", d.count >= 2 {
                if d.starts(with: [0xFF, 0xFE]) { big = false; d = d.dropFirst(2) }
                else if d.starts(with: [0xFE, 0xFF]) { d = d.dropFirst(2) }
            }
            let units = stride(from: d.startIndex, to: d.endIndex, by: 2).map {
                big ? UInt16(d[$0]) << 8 | UInt16(d[$0 + 1]) : UInt16(d[$0 + 1]) << 8 | UInt16(d[$0])
            }
            return String(decoding: units, as: UTF16.self)
        case "utf-32", "utf-32be", "utf-32le", "utf-7":
            throw TandemError.invalid("charset \(cs) not supported")
        default:
            return String(decoding: data, as: UTF8.self)
        }
    }
}
