package imap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSwiftInterop checks §10.4 against the Swift binding itself: a message Swift's
// MessageFormat builds parses in Go and is byte for byte what Go builds for the same date
// and Message-ID, and a message Go builds parses in Swift. It builds the helper package in
// testdata/swiftinterop, so it needs a Swift 6.4 toolchain; set TANDEM_IMAP_SWIFT=1 to run.
func TestSwiftInterop(t *testing.T) {
	if os.Getenv("TANDEM_IMAP_SWIFT") != "1" {
		t.Skip("set TANDEM_IMAP_SWIFT=1 to check the message format against the Swift binding")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute) // a safety net for the build
	defer cancel()
	scratch := t.TempDir()
	swift := func(stdin []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdin = bytes.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v: %s", strings.Join(args, " "), err, stderr.Bytes())
		}
		return out
	}
	build := []string{"swift", "build", "--package-path", filepath.Join("testdata", "swiftinterop"), "--scratch-path", scratch}
	swift(nil, build...)
	bin := filepath.Join(strings.TrimSpace(string(swift(nil, append(build, "--show-bin-path")...))), "swiftinterop")

	dateLine := regexp.MustCompile(`(?m)^Date: (.*)\r$`)
	idLine := regexp.MustCompile(`(?m)^Message-ID: <([0-9a-f]+)@tandem\.airforce>\r$`)
	for _, size := range []int{0, 1, 2, 3, 56, 57, 58, 100, 1000, 65536} {
		for _, channel := range []string{"abcdefgh", "Zq3-_x9Ab~!", strings.Repeat("c", 900)} {
			name := fmt.Sprintf("%d bytes, channel of %d", size, len(channel))
			envelope := make([]byte, size)
			for i := range envelope {
				envelope[i] = byte(i*7 + size)
			}

			// Swift builds, Go reads.
			raw := swift(nil, bin, "build", channel, hex.EncodeToString(envelope))
			header, body := split(raw)
			got, env, err := DecodeMessage(header, body)
			if err != nil || got != channel || !bytes.Equal(env, envelope) {
				t.Fatalf("%s: Go read Swift's message as %q, %d bytes, %v", name, got, len(env), err)
			}
			date, id := dateLine.FindSubmatch(raw), idLine.FindSubmatch(raw)
			if date == nil || id == nil {
				t.Fatalf("%s: no Date or Message-ID in\n%s", name, header)
			}
			when, err := time.Parse("Mon, 2 Jan 2006 15:04:05 +0000", string(date[1]))
			if err != nil {
				t.Fatal(err)
			}
			mine, err := BuildMessage(channel, envelope, when, string(id[1]))
			if err != nil || !bytes.Equal(mine, raw) {
				t.Fatalf("%s: Go builds\n%q\nSwift built\n%q", name, mine, raw)
			}

			// Go builds, Swift reads.
			mine, err = BuildMessage(channel, envelope, time.Now(), "")
			if err != nil {
				t.Fatal(err)
			}
			read := swift(mine, bin, "decode")
			if want := channel + "\n" + hex.EncodeToString(envelope) + "\n"; string(read) != want {
				t.Fatalf("%s: Swift read Go's message as %q", name, read)
			}
		}
	}
	// Bodies stored in another encoding, as a server or mail client may leave them, read alike.
	for _, b := range []string{pythonUTF16Base64, pythonQuotedPrintable} {
		raw, _ := base64.StdEncoding.DecodeString(b)
		header, body := split(raw)
		channel, env, err := DecodeMessage(header, body)
		if err != nil {
			t.Fatal(err)
		}
		if read := swift(raw, bin, "decode"); string(read) != channel+"\n"+hex.EncodeToString(env)+"\n" {
			t.Fatalf("Swift and Go read a re-encoded body differently: %q", read)
		}
	}
}
