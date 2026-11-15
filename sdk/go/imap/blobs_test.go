package imap

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

func chunkEnvelope(t *testing.T, n int) []byte {
	t.Helper()
	plain := make([]byte, n)
	_, _ = rand.Read(plain)
	copy(plain, "\x00\r\n.\r\nFrom =\t ")
	r, hub := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(r)
	key, _ := tandem.ChunkKey(r, plain)
	env, err := tandem.SealChunk(key, hub, plain)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestChunkMessageBothForms(t *testing.T) {
	env := chunkEnvelope(t, 3000)
	date := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	for _, form := range []ChunkForm{FormEscaped, FormBase64} {
		m, err := BuildChunkMessage("abc", env, form, date, "0123")
		if err != nil {
			t.Fatal(err)
		}
		header, body := split(m)
		if got := string(field("Tandem-Blob", header)); got != tandem.B32(tandem.ChunkAddress(env)) {
			t.Fatalf("form %d: Tandem-Blob %q", form, got)
		}
		for _, line := range strings.Split(string(body), "\r\n") {
			if len(line) > 998 {
				t.Fatalf("form %d: a line of %d bytes", form, len(line))
			}
		}
		got, err := DecodeChunkMessage(header, body)
		if err != nil || !bytes.Equal(got, env) {
			t.Fatalf("form %d: %v", form, err)
		}
	}
	m, _ := BuildChunkMessage("abc", env, FormEscaped, date, "0123")
	if !bytes.HasPrefix(m, []byte("From: Tandem <tandem@tandem.airforce>\r\nDate: Sun, 15 Nov 2026 09:30:00 +0000\r\nMessage-ID: <0123@tandem.airforce>\r\n"+
		"Subject: tandem\r\nMIME-Version: 1.0\r\nTandem-Version: 2\r\nTandem-Channel: abc\r\nTandem-Blob: ")) ||
		!bytes.Contains(m, []byte("\r\nTandem-Body: escaped\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")) {
		t.Fatalf("escaped form headers:\n%s", m[:400])
	}
}

// A server may re-encode a body after the append and rewrite the MIME headers, as Fastmail does
// with binary content; Tandem-Body survives, and the transfer encoding is undone exactly once.
func TestChunkMessageReencodedByTheServer(t *testing.T) {
	env := chunkEnvelope(t, 2000)
	escaped := tandem.Escape(env)
	header := []byte("Tandem-Body: escaped\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n")
	if got, err := DecodeChunkMessage(header, wrapped(escaped)); err != nil || !bytes.Equal(got, env) {
		t.Fatalf("escaped, stored as base64: %v", err)
	}
	header = []byte("Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n")
	if got, err := DecodeChunkMessage(header, env); err != nil || !bytes.Equal(got, env) {
		t.Fatalf("stored as binary: %v", err)
	}
	header = []byte("Content-Type: Application/Octet-Stream; name=x\r\ncontent-transfer-encoding: BASE64\r\n")
	if got, err := DecodeChunkMessage(header, wrapped(env)); err != nil || !bytes.Equal(got, env) {
		t.Fatalf("headers in other cases: %v", err)
	}
	// The feed's own form, a text body of base64, is not a chunk.
	feed, _ := BuildMessage("abc", env, time.Now(), "")
	if _, err := DecodeChunkMessage(split(feed)); err == nil {
		t.Fatal("a text/plain body was read as a chunk")
	}
	header = []byte("Tandem-Body: escaped\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n")
	if _, err := DecodeChunkMessage(header, []byte("abc=")); err == nil {
		t.Fatal("an escaped body ending in '=' was read")
	}
}

func TestQuotaResponse(t *testing.T) {
	for _, c := range []struct {
		line string
		want quotaData
	}{
		{`* QUOTA "" (STORAGE 88408 48828416)`, quotaData{"", map[string][2]uint64{"STORAGE": {88408, 48828416}}}},
		{`* QUOTA INBOX (storage 10 512 MESSAGE 3 1000)`, quotaData{"INBOX", map[string][2]uint64{"STORAGE": {10, 512}, "MESSAGE": {3, 1000}}}},
	} {
		v, err := parseResponse([]byte(c.line))
		q, ok := v.(quotaData)
		if err != nil || !ok || q.root != c.want.root || fmt.Sprint(q.resources) != fmt.Sprint(c.want.resources) {
			t.Fatalf("%q: %#v %v", c.line, v, err)
		}
	}
}

// Against a disposable Stalwart: append in both forms over several transfer connections, list the
// claims from headers alone, fetch the envelopes back byte for byte, destroy, release.
func TestStalwartBlobStore(t *testing.T) {
	needStalwart(t)
	ctx := context.Background()
	folder := folderName() + "-blobs"
	b := stalwartBlobs(t, folder, BlobOptions{BatchBytes: 64 << 10})
	var out []OutgoingChunk
	var envs [][]byte
	for i := range 12 {
		env := chunkEnvelope(t, 20000+i)
		envs = append(envs, env)
		out = append(out, OutgoingChunk{Channel: "tag" + fmt.Sprint(i%3), Envelope: env})
	}
	ids, err := b.Append(ctx, out, FormEscaped)
	if err != nil || len(ids) != len(out) {
		t.Fatalf("append: %v %v", ids, err)
	}
	if n := b.Connections(); n != 2 {
		t.Fatalf("%d transfer connections; Stalwart batches appends, so two", n)
	}
	more, err := b.Append(ctx, []OutgoingChunk{{Channel: "tag0", Envelope: envs[0]}}, FormBase64)
	if err != nil || len(more) != 1 {
		t.Fatal(err)
	}
	changes, err := b.Changes(ctx, "")
	if err != nil || len(changes.Added) != 13 {
		t.Fatalf("changes: %d added, %v", len(changes.Added), err)
	}
	// Batches went up on two connections at once, so UIDs need not follow the input's order.
	index := map[string]int{more[0]: 0}
	for i, id := range ids {
		index[id] = i
	}
	for _, ref := range changes.Added {
		i, ok := index[ref.ID]
		if !ok || ref.Channel != out[i].Channel || !bytes.Equal(ref.Address, tandem.ChunkAddress(envs[i])) {
			t.Fatalf("ref %+v does not match what was appended", ref)
		}
	}
	all := append(append([]string{}, ids...), more...)
	chunks, err := b.Fetch(ctx, all)
	if err != nil || len(chunks) != 13 {
		t.Fatalf("fetch: %d, %v", len(chunks), err)
	}
	for i, c := range chunks {
		if c.ID != all[i] || !bytes.Equal(c.Envelope, envs[i%12]) {
			t.Fatalf("chunk %d differs", i)
		}
	}
	noErr(t, b.Destroy(ctx, ids[:5]))
	next, err := b.Changes(ctx, changes.Cursor)
	if err != nil || len(next.Removed) != 5 || len(next.Added) != 0 {
		t.Fatalf("after destroy: %+v %v", next, err)
	}
	q, err := b.Quota(ctx)
	if err != nil || q.Known {
		t.Fatalf("Stalwart sets no quota, so unknown: %+v %v", q, err)
	}
	b.Release()
	if n := b.Connections(); n != 0 {
		t.Fatalf("%d connections after Release", n)
	}
	if got, err := b.Fetch(ctx, more); err != nil || len(got) != 1 {
		t.Fatalf("after Release the store connects again: %v", err)
	}
}

// The feed's connection appends to a blob folder that doesn't exist yet: TRYCREATE, CREATE, again.
func TestStalwartAppendChunksOnTheFeedConnection(t *testing.T) {
	needStalwart(t)
	ctx := context.Background()
	feed := stalwartStore(t, folderName(), stalwartOptions{})
	noErr(t, feed.EnsureFolder(ctx))
	blobFolder := folderName() + "-blobs"
	env := chunkEnvelope(t, 5000)
	ids, err := feed.AppendChunks(ctx, blobFolder, []OutgoingChunk{{Channel: "tag", Envelope: env}}, FormEscaped)
	if err != nil || len(ids) != 1 || ids[0] == "" {
		t.Fatalf("append on the feed connection: %v %v", ids, err)
	}
	b := stalwartBlobs(t, blobFolder, BlobOptions{})
	got, err := b.Fetch(ctx, ids)
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Envelope, env) {
		t.Fatalf("fetch: %v", err)
	}
	if n := b.Connections(); n != 1 {
		t.Fatalf("one fetch command needs one connection, not %d", n)
	}
}

func stalwartBlobs(t *testing.T, folder string, opts BlobOptions) *BlobStore {
	t.Helper()
	cfg := Config{Host: "127.0.0.1", Port: stalwart.imapPort, Username: stalwart.username, Password: stalwart.password,
		Security: TLSTrustingAnyCertificateForTesting, ReconnectMin: 250 * time.Millisecond, ReconnectMax: 2 * time.Second}
	if os.Getenv("TANDEM_IMAP_TRACE") != "" {
		cfg.Trace = func(line string) { fmt.Printf("[%s] %s\n", folder, line) }
	}
	opts.Folder = folder
	b, err := NewBlobStore(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}
