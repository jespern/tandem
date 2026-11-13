import Foundation
import Tandem

/// The change cursor: "imap1:<uidvalidity>:<highestmodseq>:<last uid>:<known uids>".
/// The known UIDs are the decoded messages already reported as added, as an IMAP
/// sequence set; removals are reported against them. Modseq is 0 without QRESYNC.
struct Cursor: Sendable, Equatable {
    var validity: UInt32
    var modseq: UInt64
    var last: UInt32
    var known: UIDSet

    init(validity: UInt32, modseq: UInt64, last: UInt32, known: UIDSet) {
        self.validity = validity; self.modseq = modseq; self.last = last; self.known = known
    }

    /// nil for the empty cursor, which means "from the start".
    init?(_ text: String) throws {
        if text.isEmpty { return nil }
        let parts = text.split(separator: ":", maxSplits: 4, omittingEmptySubsequences: false)
        guard parts.count == 5, parts[0] == "imap1", let v = UInt32(parts[1]), let m = UInt64(parts[2]), let l = UInt32(parts[3])
        else { throw TandemError.invalid("not an IMAP cursor") }
        validity = v; modseq = m; last = l
        known = parts[4].isEmpty ? UIDSet() : try UIDSet(parsing: String(parts[4]))
    }

    var text: String { "imap1:\(validity):\(modseq):\(last):\(known)" }
}

/// Message IDs are "<uidvalidity>.<uid>", so IDs from before a UIDVALIDITY change never
/// name a message after it.
enum MessageID {
    static func make(_ validity: UInt32, _ uid: UInt32) -> String { "\(validity).\(uid)" }
    static func parse(_ id: String) -> (validity: UInt32, uid: UInt32)? {
        let parts = id.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 2, let v = UInt32(parts[0]), let u = UInt32(parts[1]), u > 0 else { return nil }
        return (v, u)
    }
}
