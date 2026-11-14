package imap

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The stored message of DESIGN §10.4: building it, and reading it back in three steps.
// It matches the Swift binding's MessageFormat byte for byte.

// headerFields are the header fields a reader fetches along with the body.
var headerFields = []string{"Tandem-Channel", "Content-Type", "Content-Transfer-Encoding"}

// ErrFormat means a message is not a Tandem message, or its body doesn't decode (§10.4).
var ErrFormat = errors.New("imap: not a Tandem message")

func formatError(msg string) error { return &formatErr{msg} }

type formatErr struct{ msg string }

func (e *formatErr) Error() string { return "imap: message format: " + e.msg }
func (e *formatErr) Unwrap() error { return ErrFormat }

// BuildMessage returns the full RFC 5322 message: headers in the order of §10.4, CRLF line
// ends, the envelope in base64 wrapped at 76 characters. An empty id picks a random Message-ID.
func BuildMessage(channel string, envelope []byte, date time.Time, id string) ([]byte, error) {
	if channel == "" || len(channel) > 900 {
		return nil, formatError("channel tag must be printable ASCII")
	}
	for i := 0; i < len(channel); i++ {
		if channel[i] <= 0x20 || channel[i] >= 0x7f {
			return nil, formatError("channel tag must be printable ASCII")
		}
	}
	if id == "" {
		var r [16]byte
		_, _ = rand.Read(r[:])
		id = hex.EncodeToString(r[:])
	}
	var b bytes.Buffer
	for _, line := range []string{"From: Tandem <tandem@tandem.airforce>", "Date: " + rfc5322(date), "Message-ID: <" + id + "@tandem.airforce>",
		"Subject: tandem", "MIME-Version: 1.0", "Tandem-Version: 2", "Tandem-Channel: " + channel,
		"Content-Type: text/plain; charset=us-ascii", "Content-Transfer-Encoding: 7bit", ""} {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	b.Write(wrapped(envelope))
	return b.Bytes(), nil
}

// wrapped is base64 in lines of 76 characters, each ending in CRLF.
func wrapped(envelope []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(envelope)
	var out []byte
	for i := 0; i < len(encoded); i += 76 {
		out = append(out, encoded[i:min(i+76, len(encoded))]...)
		out = append(out, '\r', '\n')
	}
	return out
}

// rfc5322 formats "Fri, 13 Nov 2026 09:30:00 +0000".
func rfc5322(t time.Time) string { return t.UTC().Format("Mon, 2 Jan 2006 15:04:05 +0000") }

// DecodeMessage reads a message from its header fields and its body text (§10.4): the
// channel tag, unfolded and trimmed, and the envelope. It fails if the body doesn't decode.
func DecodeMessage(header, text []byte) (channel string, envelope []byte, err error) {
	tag := field("Tandem-Channel", header)
	if len(tag) == 0 || !utf8.Valid(tag) {
		return "", nil, formatError("no Tandem-Channel header")
	}
	contentType := "text/plain"
	if v := field("Content-Type", header); v != nil {
		contentType = string(v)
	}
	transfer := "7bit"
	if v := field("Content-Transfer-Encoding", header); v != nil {
		transfer = string(v)
	}
	decoded, err := undoTransferEncoding(transfer, text)
	if err != nil {
		return "", nil, err
	}
	charset, ok := parameter("charset", contentType)
	if !ok {
		charset = "us-ascii"
	}
	chars, err := decodeText(decoded, charset)
	if err != nil {
		return "", nil, err
	}
	envelope, err = envelopeOf(chars)
	if err != nil {
		return "", nil, err
	}
	return string(tag), envelope, nil
}

// field returns a header field's value, unfolded and trimmed; the first if repeated, nil if
// absent. Names compare case-insensitively.
func field(name string, block []byte) []byte {
	want := []byte(strings.ToLower(name))
	var lines [][]byte
	for _, line := range bytes.Split(block, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] = append(lines[len(lines)-1], line...)
		} else {
			lines = append(lines, bytes.Clone(line))
		}
	}
	for _, line := range lines {
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		var key []byte
		for _, c := range line[:colon] {
			if c == ' ' || c == '\t' {
				continue
			}
			if c >= 'A' && c <= 'Z' {
				c += 0x20
			}
			key = append(key, c)
		}
		if bytes.Equal(key, want) {
			v := bytes.Trim(line[colon+1:], " \t\r\n")
			if v == nil {
				v = []byte{}
			}
			return v
		}
	}
	return nil
}

// parameter returns a Content-Type parameter, unquoted and lowercased.
func parameter(name, value string) (string, bool) {
	parts := fields(value, ';')
	for i, part := range parts {
		if i == 0 {
			continue
		}
		k, v, found := strings.Cut(part, "=")
		if !found || k == "" || v == "" || strings.ToLower(strings.TrimFunc(k, isBlank)) != name {
			continue
		}
		v = strings.TrimFunc(v, isBlank)
		if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
			v = v[1 : len(v)-1]
		}
		return strings.ToLower(v), true
	}
	return "", false
}

// isBlank is Foundation's CharacterSet.whitespaces: tab and the space separators.
func isBlank(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) }

// undoTransferEncoding is step 1: 7bit, 8bit and binary need nothing; quoted-printable; base64.
func undoTransferEncoding(name string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimFunc(name, isBlank)) {
	case "7bit", "8bit", "binary", "":
		return body, nil
	case "quoted-printable":
		return quotedPrintable(body), nil
	case "base64":
		kept := make([]byte, 0, len(body))
		for _, c := range body {
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				kept = append(kept, c)
			}
		}
		out, err := base64.StdEncoding.DecodeString(string(kept))
		if err != nil {
			return nil, formatError("bad base64 transfer encoding")
		}
		return out, nil
	default:
		return nil, formatError("unknown transfer encoding")
	}
}

// quotedPrintable follows RFC 2045 §6.7: "=XX" is a byte, "=" at a line end is a soft break.
// Other "=" are kept.
func quotedPrintable(b []byte) []byte {
	hexValue := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		}
		return 0, false
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] == '=' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t') {
				j++
			}
			if j+1 < len(b) && b[j] == '\r' && b[j+1] == '\n' {
				i = j + 2
				continue
			}
			if j < len(b) && b[j] == '\n' {
				i = j + 1
				continue
			}
			if j == len(b) {
				i = j
				continue
			}
			if i+2 < len(b) {
				hi, ok1 := hexValue(b[i+1])
				lo, ok2 := hexValue(b[i+2])
				if ok1 && ok2 {
					out = append(out, hi<<4|lo)
					i += 3
					continue
				}
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// asciiCompatible charsets encode US-ASCII as itself. Without a converter in the standard
// library, a body in one of them is read only if it is plain ASCII, which a base64 body is.
var asciiCompatible = map[string]bool{
	"iso-8859-2": true, "iso-8859-3": true, "iso-8859-4": true, "iso-8859-5": true, "iso-8859-6": true,
	"iso-8859-7": true, "iso-8859-8": true, "iso-8859-9": true, "iso-8859-10": true, "iso-8859-13": true,
	"iso-8859-14": true, "iso-8859-15": true, "iso-8859-16": true, "windows-1250": true, "windows-1251": true,
	"windows-1253": true, "windows-1254": true, "windows-1255": true, "windows-1256": true,
	"windows-1257": true, "windows-1258": true, "koi8-r": true, "koi8-u": true, "macintosh": true,
	"gb2312": true, "gbk": true, "gb18030": true, "big5": true, "euc-jp": true, "euc-kr": true,
	"shift_jis": true, "iso-2022-jp": true, "tis-620": true,
}

// windows1252 maps 0x80–0x9F; the rest of the code page is ISO 8859-1.
var windows1252 = [32]rune{
	0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178,
}

// decodeText is step 2: bytes to text in the body's charset.
func decodeText(b []byte, charset string) (string, error) {
	invalid := formatError("body is not valid " + charset)
	utf16Text := func(b []byte, big bool) (string, error) {
		if len(b)%2 != 0 {
			return "", invalid
		}
		units := make([]uint16, len(b)/2)
		for i := range units {
			if big {
				units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
			} else {
				units[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
			}
		}
		return string(utf16.Decode(units)), nil
	}
	utf32Text := func(b []byte, big bool) (string, error) {
		if len(b)%4 != 0 {
			return "", invalid
		}
		var s strings.Builder
		for i := 0; i < len(b); i += 4 {
			var r uint32
			if big {
				r = uint32(b[i])<<24 | uint32(b[i+1])<<16 | uint32(b[i+2])<<8 | uint32(b[i+3])
			} else {
				r = uint32(b[i+3])<<24 | uint32(b[i+2])<<16 | uint32(b[i+1])<<8 | uint32(b[i])
			}
			if !utf8.ValidRune(rune(r)) {
				return "", invalid
			}
			s.WriteRune(rune(r))
		}
		return s.String(), nil
	}
	ascii := func(b []byte) bool {
		for _, c := range b {
			if c >= 0x80 {
				return false
			}
		}
		return true
	}
	switch charset {
	case "us-ascii", "ascii", "ansi_x3.4-1968":
		if !ascii(b) {
			return "", formatError("body is not ASCII")
		}
		return string(b), nil
	case "utf-8", "utf8":
		if !utf8.Valid(b) {
			return "", formatError("body is not UTF-8")
		}
		return string(b), nil
	case "utf-16": // RFC 2781 §4.3: big-endian without a byte-order mark
		if bytes.HasPrefix(b, []byte{0xfe, 0xff}) {
			return utf16Text(b[2:], true)
		}
		if bytes.HasPrefix(b, []byte{0xff, 0xfe}) {
			return utf16Text(b[2:], false)
		}
		return utf16Text(b, true)
	case "utf-16be":
		return utf16Text(b, true)
	case "utf-16le":
		return utf16Text(b, false)
	case "utf-32":
		if bytes.HasPrefix(b, []byte{0, 0, 0xfe, 0xff}) {
			return utf32Text(b[4:], true)
		}
		if bytes.HasPrefix(b, []byte{0xff, 0xfe, 0, 0}) {
			return utf32Text(b[4:], false)
		}
		return utf32Text(b, true)
	case "utf-32be":
		return utf32Text(b, true)
	case "utf-32le":
		return utf32Text(b, false)
	case "iso-8859-1", "latin1", "windows-1252", "cp1252":
		var s strings.Builder
		for _, c := range b {
			if c >= 0x80 && c < 0xa0 && charset != "iso-8859-1" && charset != "latin1" {
				s.WriteRune(windows1252[c-0x80])
			} else {
				s.WriteRune(rune(c))
			}
		}
		return s.String(), nil
	default:
		if !asciiCompatible[charset] {
			return "", formatError("unknown charset")
		}
		if !ascii(b) {
			return "", invalid
		}
		return string(b), nil
	}
}

// envelopeOf is step 3: drop all whitespace, base64-decode the rest.
func envelopeOf(text string) ([]byte, error) {
	kept := make([]byte, 0, len(text))
	for _, r := range text {
		if unicode.Is(unicode.White_Space, r) || r == 0xfeff {
			continue
		}
		if r >= 0x80 {
			return nil, formatError("body is not base64")
		}
		kept = append(kept, byte(r))
	}
	out, err := base64.StdEncoding.DecodeString(string(kept))
	if err != nil {
		return nil, formatError("body is not base64")
	}
	return out, nil
}
