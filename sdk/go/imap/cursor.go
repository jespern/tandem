package imap

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// cursor is the change cursor: "imap1:<uidvalidity>:<highestmodseq>:<last uid>:<known uids>".
// The known UIDs are the decoded messages already reported as added, as an IMAP sequence
// set; removals are reported against them. Modseq is 0 without QRESYNC. The text is the
// same as the Swift binding's.
type cursor struct {
	validity uint32
	modseq   uint64
	last     uint32
	known    uidSet
}

var errCursor = errors.New("imap: not an IMAP cursor")

// parseCursor returns nil for the empty cursor, which means "from the start".
func parseCursor(text string) (*cursor, error) {
	if text == "" {
		return nil, nil
	}
	parts := strings.SplitN(text, ":", 5)
	if len(parts) != 5 || parts[0] != "imap1" {
		return nil, errCursor
	}
	v, err1 := strconv.ParseUint(parts[1], 10, 32)
	m, err2 := strconv.ParseUint(parts[2], 10, 64)
	l, err3 := strconv.ParseUint(parts[3], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, errCursor
	}
	c := &cursor{validity: uint32(v), modseq: m, last: uint32(l)}
	if parts[4] != "" {
		if c.known, err1 = parseUIDSet(parts[4]); err1 != nil {
			return nil, errCursor
		}
	}
	return c, nil
}

func (c cursor) String() string {
	return fmt.Sprintf("imap1:%d:%d:%d:%s", c.validity, c.modseq, c.last, c.known)
}

// Message IDs are "<uidvalidity>.<uid>", so IDs from before a UIDVALIDITY change never
// name a message after it.
func messageID(validity, uid uint32) string {
	return strconv.FormatUint(uint64(validity), 10) + "." + strconv.FormatUint(uint64(uid), 10)
}

func parseMessageID(id string) (validity, uid uint32, ok bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 2 {
		return 0, 0, false
	}
	v, err1 := strconv.ParseUint(parts[0], 10, 32)
	u, err2 := strconv.ParseUint(parts[1], 10, 32)
	if err1 != nil || err2 != nil || u == 0 {
		return 0, 0, false
	}
	return uint32(v), uint32(u), true
}
