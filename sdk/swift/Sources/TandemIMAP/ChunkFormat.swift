import Foundation
import Tandem

/// The chunk message of BLOBS.md section 4.2: building it, and reading its envelope back.
public enum ChunkFormat {
    /// How a chunk's body is carried.
    public typealias Form = ChunkForm

    /// The header fields a chunk reader fetches with the body.
    static let fields = ["Tandem-Channel", "Tandem-Blob", "Tandem-Body", "Content-Type", "Content-Transfer-Encoding"]

    /// A chunk message: the headers of DESIGN §10.4 with Tandem-Blob (and Tandem-Body for the escaped
    /// form), application/octet-stream, CRLF line ends.
    public static func build(channel: String, envelope: Bytes, form: Form = .escaped, date: Date = Date(), id: String? = nil) throws -> Bytes {
        guard !channel.isEmpty, channel.utf8.allSatisfy({ $0 > 0x20 && $0 < 0x7f }), channel.utf8.count <= 900
        else { throw TandemError.invalid("channel tag must be printable ASCII") }
        guard !envelope.isEmpty else { throw TandemError.invalid("empty chunk") }
        let messageID = id ?? (0..<16).map { _ in UInt8.random(in: 0...255) }.hex
        var head = ["From: Tandem <tandem@tandem.airforce>", "Date: \(MessageFormat.rfc5322(date))", "Message-ID: <\(messageID)@tandem.airforce>",
                    "Subject: tandem", "MIME-Version: 1.0", "Tandem-Version: 2", "Tandem-Channel: \(channel)",
                    "Tandem-Blob: \(B32.encode(Blob.address(envelope)))"]
        let body: Bytes
        switch form {
        case .escaped:
            head += ["Tandem-Body: escaped", "Content-Type: application/octet-stream", "Content-Transfer-Encoding: 8bit"]
            body = Blob.escape(envelope)
        case .base64:
            head += ["Content-Type: application/octet-stream", "Content-Transfer-Encoding: base64"]
            body = MessageFormat.wrapped(envelope)
        }
        return Bytes((head + [""]).map { $0 + "\r\n" }.joined().utf8) + body
    }

    /// A chunk's envelope from its header fields and body text: the transfer encoding the header names is
    /// undone once, then the escaping if Tandem-Body says so. Only application/octet-stream is a chunk.
    /// The caller checks the envelope against its address.
    public static func decode(header: Bytes, text: Bytes) throws -> Bytes {
        let type = MessageFormat.field("Content-Type", in: header).map { String(decoding: $0, as: UTF8.self) } ?? "text/plain"
        let media = type.split(separator: ";", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? ""
        guard media.trimmingCharacters(in: .whitespaces).lowercased() == "application/octet-stream"
        else { throw TandemError.invalid("a chunk is application/octet-stream") }
        let transfer = MessageFormat.field("Content-Transfer-Encoding", in: header).map { String(decoding: $0, as: UTF8.self) } ?? "7bit"
        var content = try MessageFormat.undoTransferEncoding(transfer, text)
        if let body = MessageFormat.field("Tandem-Body", in: header), String(decoding: body, as: UTF8.self).lowercased() == "escaped" {
            content = try Blob.unescape(content)
        }
        guard !content.isEmpty else { throw TandemError.invalid("empty chunk") }
        return content
    }
}
