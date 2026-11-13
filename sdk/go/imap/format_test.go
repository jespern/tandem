package imap

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// Ported from sdk/swift/Tests/TandemIMAPTests/ProtocolTests.swift (MessageFormatTests).

// Bodies written by Python's email package (conformance/mail_transport.py), copied from the
// Swift tests: base64 of UTF-16 with a byte-order mark, and quoted-printable UTF-8 with soft
// breaks and "=3D".
const pythonUTF16Base64 = "RnJvbTogVGFuZGVtIDx0YW5kZW1AaW52YWxpZD4NCkRhdGU6IEZyaSwgMTMgTm92IDIwMjYgMTI6NDQ6MjEgKzAwMDANCk1lc3NhZ2UtSUQ6IDwxNzk0NTczODYxNzYuOTY3MTYuMzA5OTk5NDAyMjU4NjMxNDkzN0B0YW5kZW0uaW52YWxpZD4NClN1YmplY3Q6IHRhbmRlbQ0KVGFuZGVtLVZlcnNpb246IDENClRhbmRlbS1DaGFubmVsOiBhYmNkZWZnaA0KTUlNRS1WZXJzaW9uOiAxLjANCkNvbnRlbnQtVHlwZTogdGV4dC9wbGFpbjsgY2hhcnNldD0idXRmLTE2Ig0KQ29udGVudC1UcmFuc2Zlci1FbmNvZGluZzogYmFzZTY0DQoNCi8vNUJBRUVBUlFCREFFRUFkd0JSQUVZQVFnQm5BR01BU1FCREFGRUFid0JNQUVRQVFRQXdBRThBUkFCNEFFRUFVZ0JGQUdnQVRRQlYNCkFFWUFVZ0JaQUZnQVJ3QkNBR3NBWVFCSEFIZ0Fkd0JrQUVnQWFBQTRBR2NBU1FCVEFFa0FhZ0JLQUVNQVZRQnRBRW9BZVFCbkFIQUENClN3QnBBSE1BY3dCTUFGTUFOQUIyQUUwQVJBQkZBSGtBVFFCNkFGRUFNUUJPQUdvQVl3QTBBQTBBQ2dCUEFGUUFid0EzQUEwQUNnQT0NCg=="
const pythonQuotedPrintable = "RnJvbTogVGFuZGVtIDx0YW5kZW1AaW52YWxpZD4NCkRhdGU6IEZyaSwgMTMgTm92IDIwMjYgMTI6NDQ6MjEgKzAwMDANCk1lc3NhZ2UtSUQ6IDwxNzk0NTczODYxNzYuOTY3MTYuMTYxNDQ2OTU2MDg2OTM1NjkwMDBAdGFuZGVtLmludmFsaWQ+DQpTdWJqZWN0OiB0YW5kZW0NClRhbmRlbS1WZXJzaW9uOiAxDQpUYW5kZW0tQ2hhbm5lbDogYWJjZGVmZ2gNCkNvbnRlbnQtVHlwZTogdGV4dC9wbGFpbjsgY2hhcnNldD0idXRmLTgiDQpDb250ZW50LVRyYW5zZmVyLUVuY29kaW5nOiBxdW90ZWQtcHJpbnRhYmxlDQpNSU1FLVZlcnNpb246IDEuMA0KDQpBQUVDQXdRRkJnY0lDUW9MREEwT0R4QVJFaE1VRlJZWEdCa2FHeHdkSGg4Z0lTSWpKQ1VtSnlncEtpc3NMUzR2TURFeU16UTFOamM0DQpPVG83UEQwK1AwQkJRa05FUlVaSFNFbEtTMHhOVGs5UVVWSlRWRlZXVjFoWldsdGNYVjVmWUdGaVkyUmxabWRvYVdwcmJHMXViM0J4DQpjbk4wZFhaM2VIbDZlM3g5Zm4rQWdZS0RoSVdHaDRpSmlvdU1qWTZQa0pHU2s1U1ZscGVZbVpxYm5KMmVuNkNob3FPa3BhYW5xS21xDQpxNnl0cnErd3NiS3p0TFcydDdpNXVydTh2YjYvd01IQ3c4VEZ4c2M9M0QNCg=="

// split returns the header block (with its blank line) and the text of a whole message.
func split(raw []byte) ([]byte, []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+4], raw[i+4:]
	}
	return raw, nil
}

func seq(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

// The exact text the Swift test expects of MessageFormat.build for the same inputs.
func TestBuildIsExactlyTheFormatOfSection10_4(t *testing.T) {
	envelope := seq(100)
	raw, err := BuildMessage("abcdefgh", envelope, time.Unix(1794562200, 0), "00ff")
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(envelope)
	expected := "From: Tandem <tandem@invalid>\r\nDate: Fri, 13 Nov 2026 09:30:00 +0000\r\nMessage-ID: <00ff@tandem.invalid>\r\n" +
		"Subject: tandem\r\nMIME-Version: 1.0\r\nTandem-Version: 2\r\nTandem-Channel: abcdefgh\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: 7bit\r\n\r\n" +
		b64[:76] + "\r\n" + b64[76:] + "\r\n"
	if string(raw) != expected {
		t.Fatalf("got\n%s\nwant\n%s", raw, expected)
	}
	header, body := split(raw)
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if len(line) > 77 {
			t.Fatal("line longer than 76 characters")
		}
	}
	channel, got, err := DecodeMessage(header, body)
	if err != nil || channel != "abcdefgh" || !bytes.Equal(got, envelope) {
		t.Fatal(channel, got, err)
	}
	if s := rfc5322(time.Unix(0, 0)); s != "Thu, 1 Jan 1970 00:00:00 +0000" {
		t.Fatal(s)
	}
	for _, size := range []int{0, 1, 56, 57, 58, 1000} {
		e := bytes.Repeat([]byte{7}, size)
		raw, err := BuildMessage("c", e, time.Now(), "")
		if err != nil {
			t.Fatal(err)
		}
		h, b := split(raw)
		if _, got, err := DecodeMessage(h, b); err != nil || !bytes.Equal(got, e) {
			t.Fatal(size, err)
		}
	}
}

func TestChannelMustBeASafeHeaderValue(t *testing.T) {
	for _, bad := range []string{"", "a b", "a\r\nBcc: x", "é", strings.Repeat("a", 1000)} {
		if _, err := BuildMessage(bad, []byte{1}, time.Now(), ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPythonEncodedBodies(t *testing.T) {
	for _, c := range []struct {
		raw      string
		envelope []byte
	}{{pythonUTF16Base64, seq(60)}, {pythonQuotedPrintable, seq(200)}} {
		raw, _ := base64.StdEncoding.DecodeString(c.raw)
		header, body := split(raw)
		channel, envelope, err := DecodeMessage(header, body)
		if err != nil || channel != "abcdefgh" || !bytes.Equal(envelope, c.envelope) {
			t.Fatal(channel, envelope, err)
		}
	}
}

func utf16Bytes(s string, big bool) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		if big {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return out
}

// base64Lines is base64 in lines of 76 characters, as Foundation's .lineLength76Characters writes it.
func base64Lines(b []byte) []byte {
	s := base64.StdEncoding.EncodeToString(b)
	var out []string
	for i := 0; i < len(s); i += 76 {
		out = append(out, s[i:min(i+76, len(s))])
	}
	return []byte(strings.Join(out, "\r\n"))
}

func TestThreeSteps(t *testing.T) {
	envelope := make([]byte, 300)
	for i := range envelope {
		envelope[i] = byte(i % 251)
	}
	w := wrapped(envelope)
	text := string(w)
	header := func(cte, charset string) []byte {
		return []byte("Tandem-Channel:\r\n  abc \r\nContent-Type: text/plain;\r\n charset=\"" + charset + "\"\r\nContent-Transfer-Encoding: " + cte + "\r\n\r\n")
	}
	ok := func(cte, charset string, body []byte) {
		t.Helper()
		channel, got, err := DecodeMessage(header(cte, charset), body)
		if err != nil || channel != "abc" || !bytes.Equal(got, envelope) {
			t.Errorf("%s %s: %q %v", cte, charset, channel, err)
		}
	}
	utf32 := func(s string) []byte {
		var out []byte
		for _, r := range s {
			out = append(out, byte(r>>24), byte(r>>16), byte(r>>8), byte(r))
		}
		return out
	}
	ok("7bit", "us-ascii", w)
	ok("8bit", "utf-8", w)
	ok("base64", "utf-16", base64Lines(append([]byte{0xfe, 0xff}, utf16Bytes(text, true)...)))
	ok("base64", "utf-16", base64Lines(append([]byte{0xff, 0xfe}, utf16Bytes(text, false)...)))
	ok("base64", "utf-16", []byte(base64.StdEncoding.EncodeToString(utf16Bytes(text, true)))) // no BOM: big-endian
	ok("BASE64", "UTF-16LE", []byte(base64.StdEncoding.EncodeToString(utf16Bytes(text, false))))
	ok("base64", "utf-32", []byte(base64.StdEncoding.EncodeToString(utf32(text))))
	ok("quoted-printable", "utf-8", []byte(strings.ReplaceAll(strings.ReplaceAll(text, "=", "=3D"), "A", "=41=\r\n")))
	ok("quoted-printable", "windows-1252", []byte(strings.ReplaceAll(text, "\r\n", "=\r\n")))
	ok("7bit", "iso-8859-1", w)

	bad := func(h, body []byte) {
		t.Helper()
		if _, _, err := DecodeMessage(h, body); err == nil {
			t.Errorf("accepted %q", h)
		}
	}
	bad(header("7bit", "us-ascii"), append(bytes.Clone(w), '!'))
	bad(header("7bit", "us-ascii"), append([]byte{0xc3, 0xa9}, w...))
	bad(header("x-uuencode", "us-ascii"), w)
	bad(header("7bit", "x-no-such-charset"), w)
	bad(header("base64", "us-ascii"), []byte("!!!"))
	bad([]byte("Content-Type: text/plain\r\n\r\n"), w)
	// Without UTF-16 decoding (step 2), the base64 text is garbage.
	if _, err := envelopeOf(string(utf16Bytes(text, true))); err == nil {
		t.Error("UTF-16 read as bytes decoded")
	}
}

func TestHeaderFields(t *testing.T) {
	block := []byte("tandem-channel: one\r\nTANDEM-CHANNEL: two\r\nContent-Type: text/plain;\r\n\tcharset=UTF-16\r\n\r\n")
	if v := field("Tandem-Channel", block); string(v) != "one" {
		t.Fatal(string(v))
	}
	if v := field("Content-Type", block); string(v) != "text/plain;\tcharset=UTF-16" {
		t.Fatal(string(v))
	}
	if field("Subject", block) != nil {
		t.Fatal("found Subject")
	}
	if v, ok := parameter("charset", `text/plain; format=flowed; Charset="UTF-16"`); !ok || v != "utf-16" {
		t.Fatal(v)
	}
	if _, ok := parameter("charset", "text/plain"); ok {
		t.Fatal("found charset")
	}
}
