package tandem

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"strings"
	"time"
	"unicode/utf16"
)

// The hub message of §10.4.

// WrappedBase64 is the body text: base64 of the envelope, wrapped at 76
// characters, CRLF line endings, with a final CRLF.
func WrappedBase64(env []byte) string {
	s := base64.StdEncoding.EncodeToString(env)
	var b strings.Builder
	for i := 0; i < len(s); i += 76 {
		b.WriteString(s[i:min(i+76, len(s))])
		b.WriteString("\r\n")
	}
	return b.String()
}

// BuildMessage is the RFC 5322 message an IMAP append stores. msgID nil
// draws a random one.
func BuildMessage(tag string, env []byte, at time.Time, msgID []byte) []byte {
	if msgID == nil {
		msgID = make([]byte, 16)
		rand.Read(msgID)
	}
	h := []string{
		"From: Tandem <tandem@invalid>",
		"Date: " + at.UTC().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(msgID) + "@tandem.invalid>",
		"Subject: tandem",
		"MIME-Version: 1.0",
		"Tandem-Version: 1",
		"Tandem-Channel: " + tag,
		"Content-Type: text/plain; charset=us-ascii",
		"Content-Transfer-Encoding: 7bit",
	}
	return []byte(strings.Join(h, "\r\n") + "\r\n\r\n" + WrappedBase64(env))
}

// EnvelopeID is a message's identity across bindings and devices.
func EnvelopeID(env []byte) []byte { return H(env) }

// DecodeText is step 3: remove all whitespace and base64-decode.
func DecodeText(text string) ([]byte, error) {
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\f' || r == '\v' {
			return -1
		}
		return r
	}, text)
	return base64.StdEncoding.Strict().DecodeString(s)
}

// DecodeBody runs all three steps of §10.4 on a raw MIME body.
func DecodeBody(contentType, transferEncoding string, body []byte) ([]byte, error) {
	var data []byte
	switch cte := strings.ToLower(strings.TrimSpace(transferEncoding)); cte {
	case "", "7bit", "8bit", "binary":
		data = body
	case "base64":
		d, err := DecodeText(string(body))
		if err != nil {
			return nil, err
		}
		data = d
	case "quoted-printable":
		d, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			return nil, err
		}
		data = d
	default:
		return nil, fmt.Errorf("tandem: unknown transfer encoding %q", cte)
	}
	text, err := decodeCharset(contentType, data)
	if err != nil {
		return nil, err
	}
	return DecodeText(text)
}

// decodeCharset turns body bytes into text. The text is base64, so every
// ASCII-compatible charset reads it the same; the ones that aren't (UTF-16
// and UTF-32) are decoded here.
func decodeCharset(contentType string, data []byte) (string, error) {
	cs := "us-ascii"
	if contentType != "" {
		if _, params, err := mime.ParseMediaType(contentType); err == nil && params["charset"] != "" {
			cs = strings.ToLower(params["charset"])
		}
	}
	switch cs {
	case "utf-16", "utf-16be", "utf-16le":
		return decodeUTF16(cs, data)
	case "utf-32", "utf-32be", "utf-32le", "utf-7":
		return "", fmt.Errorf("tandem: charset %s not supported", cs)
	}
	return string(data), nil
}

func decodeUTF16(cs string, data []byte) (string, error) {
	if len(data)%2 != 0 {
		return "", errors.New("tandem: odd UTF-16 length")
	}
	big := cs != "utf-16le"
	if cs == "utf-16" && len(data) >= 2 { // RFC 2781: BOM, else big-endian
		if data[0] == 0xFF && data[1] == 0xFE {
			big, data = false, data[2:]
		} else if data[0] == 0xFE && data[1] == 0xFF {
			data = data[2:]
		}
	}
	u := make([]uint16, len(data)/2)
	for i := range u {
		if big {
			u[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		} else {
			u[i] = uint16(data[2*i+1])<<8 | uint16(data[2*i])
		}
	}
	return string(utf16.Decode(u)), nil
}

// ParseMessage returns the channel tag and envelope of a raw stored message.
func ParseMessage(raw []byte) (string, []byte, error) {
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	head, body, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		return "", nil, errors.New("tandem: no message body")
	}
	headers := map[string]string{}
	var last string
	for _, line := range strings.Split(string(head), "\n") {
		if line != "" && (line[0] == ' ' || line[0] == '\t') && last != "" {
			headers[last] = strings.TrimSpace(headers[last] + " " + strings.TrimSpace(line)) // unfold
		} else if name, value, ok := strings.Cut(line, ":"); ok {
			last = strings.ToLower(strings.TrimSpace(name))
			headers[last] = strings.TrimSpace(value)
		}
	}
	env, err := DecodeBody(headers["content-type"], headers["content-transfer-encoding"], body)
	if err != nil {
		return "", nil, err
	}
	return headers["tandem-channel"], env, nil
}
