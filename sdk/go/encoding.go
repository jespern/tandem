package tandem

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

// Notation of §5.2.

// H is SHA-256.
func H(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func u8(b []byte, v uint8) []byte   { return append(b, v) }
func u16(b []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(b, v) }
func u32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }
func u64(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint64(b, v) }

// lp is u32(len(x)) || x.
func lp(b, x []byte) []byte { return append(u32(b, uint32(len(x))), x...) }

var errB64u = errors.New("tandem: b64u value not in its one spelling")

// B64u encodes base64url without padding.
func B64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ParseB64u decodes base64url in its one spelling (§5.2): no padding, nothing
// outside the alphabet (Go's decoder silently skips CR and LF), and unused bits
// zero (Strict).
func ParseB64u(s string) ([]byte, error) {
	if len(s)%4 == 1 {
		return nil, errB64u
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, errB64u
		}
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, errB64u
	}
	return b, nil
}

// parseB64uN decodes a b64u value that must be exactly n bytes long.
func parseB64uN(s string, n int) ([]byte, error) {
	b, err := ParseB64u(s)
	if err != nil {
		return nil, err
	}
	if len(b) != n {
		return nil, errors.New("tandem: wrong length")
	}
	return b, nil
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// B32 is RFC 4648 base32, lowercase, no padding.
func B32(b []byte) string { return strings.ToLower(b32.EncodeToString(b)) }

// ParseB32 decodes lowercase base32 without padding, in its one spelling.
func ParseB32(s string) ([]byte, error) {
	if s != strings.ToLower(s) {
		return nil, errors.New("tandem: b32 must be lowercase")
	}
	b, err := b32.DecodeString(strings.ToUpper(s))
	if err != nil || B32(b) != s {
		return nil, errors.New("tandem: b32 value not in its one spelling")
	}
	return b, nil
}

// groups splits s into space-separated groups of n characters.
func groups(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s); i += n {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:min(i+n, len(s))])
	}
	return b.String()
}
