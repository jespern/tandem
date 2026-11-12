package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Round trips on the testbed (testbed/, `./tb up`). Run with
//
//	TANDEM_TESTBED=1 go test ./store
//
// Each test gets a fresh hub account and deletes it afterwards.

var tb = filepath.Join("..", "..", "..", "testbed", "tb")

func hub(t *testing.T) (jmap, imap Descriptor, name string) {
	t.Helper()
	if os.Getenv("TANDEM_TESTBED") == "" {
		t.Skip("set TANDEM_TESTBED=1 with the testbed up")
	}
	name = "go-" + hex.EncodeToString(rnd(4))
	out, err := exec.Command(tb, "hub", "new", "--name", name).Output()
	if err != nil {
		t.Fatalf("tb hub new: %v", err)
	}
	t.Cleanup(func() { exec.Command(tb, "hub", "delete", name).Run() })
	var d struct{ JMAP, IMAP json.RawMessage }
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatal(err)
	}
	if jmap, err = ParseDescriptor(d.JMAP); err != nil {
		t.Fatal(err)
	}
	if imap, err = ParseDescriptor(d.IMAP); err != nil {
		t.Fatal(err)
	}
	return jmap, imap, name
}

func rnd(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func open(t *testing.T, d Descriptor) Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, d)
	if err != nil {
		t.Fatalf("open %s: %v", d.Kind, err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.EnsureFolder(ctx); err != nil {
		t.Fatalf("%s ensureFolder: %v", d.Kind, err)
	}
	return s
}

// watch starts s.Watch and returns a channel that receives on every change.
func watch(t *testing.T, s Store) <-chan struct{} {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{}, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Watch(ctx, func() { ch <- struct{}{} })
	}()
	t.Cleanup(func() { cancel(); <-done })
	<-ch // the first call, on connecting
	return ch
}

// until runs Changes after each change event until cond holds.
func until(t *testing.T, s Store, events <-chan struct{}, what string, cond func(added []Ref, removed []string) bool) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		added, removed, err := s.Changes(context.Background())
		if err != nil {
			t.Fatalf("changes: %v", err)
		}
		if cond(added, removed) {
			return
		}
		select {
		case <-events:
		case <-deadline:
			t.Fatalf("no push for %s within 15 s", what)
		}
	}
}

func TestRoundTrips(t *testing.T) {
	jd, id, _ := hub(t)
	ds := map[string]Descriptor{"jmap": jd, "imap": id}
	for _, w := range []string{"jmap", "imap"} {
		for _, r := range []string{"jmap", "imap"} {
			t.Run(w+"→"+r, func(t *testing.T) {
				ctx := context.Background()
				writer, reader := open(t, ds[w]), open(t, ds[r])
				if _, _, err := reader.Changes(ctx); err != nil { // catch up first
					t.Fatal(err)
				}
				events := watch(t, reader)

				k := rnd(32)
				tag := tandem.CollectionTag(k, rnd(32))
				plaintext := []byte(`{"rec":"AAAAAAAAAAAAAAAAAAAAAA","writes":[]}`)
				env, err := tandem.Seal(k, tandem.KindRecord, tag, plaintext, nil)
				if err != nil {
					t.Fatal(err)
				}
				wid, err := writer.Append(ctx, tag, env)
				if err != nil {
					t.Fatalf("append: %v", err)
				}
				start := time.Now()

				var rid string
				until(t, reader, events, "the append", func(added []Ref, _ []string) bool {
					for _, a := range added {
						if a.Tag == tag {
							rid = a.ID
						}
					}
					return rid != ""
				})
				t.Logf("seen after %v", time.Since(start).Round(time.Millisecond))
				msgs, err := reader.Fetch(ctx, []string{rid})
				if err != nil {
					t.Fatal(err)
				}
				m := msgs[rid]
				if m.Tag != tag || !bytes.Equal(m.Envelope, env) {
					t.Fatalf("envelope changed in transit")
				}
				if _, pt, err := tandem.Open(k, m.Tag, m.Envelope); err != nil || !bytes.Equal(pt, plaintext) {
					t.Fatalf("doesn't open: %v", err)
				}

				if err := writer.Destroy(ctx, []string{wid}); err != nil {
					t.Fatalf("destroy: %v", err)
				}
				until(t, reader, events, "the destroy", func(_ []Ref, removed []string) bool {
					for _, x := range removed {
						if x == rid {
							return true
						}
					}
					return false
				})
			})
		}
	}
}

// A JMAP server may store a body in another charset and transfer encoding
// (§10.2); both bindings must still read the envelope (§10.4).
func TestOtherCharsets(t *testing.T) {
	jd, id, name := hub(t)
	ctx := context.Background()
	imapS, jmapS := open(t, id), open(t, jd)
	for _, c := range []struct{ charset, encoding string }{
		{"utf-16", "base64"}, {"utf-16be", "base64"}, {"utf-8", "quoted-printable"}, {"iso-8859-1", "8bit"},
	} {
		t.Run(c.charset+"/"+c.encoding, func(t *testing.T) {
			k := rnd(32)
			tag := tandem.CollectionTag(k, rnd(32))
			env, _ := tandem.Seal(k, tandem.KindRecord, tag, rnd(300), nil)
			uid, err := imapS.Append(ctx, tag, env)
			if err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(tb, "hub", "rewrite-charset", name, uid, "--charset", c.charset, "--encoding", c.encoding).CombinedOutput(); err != nil {
				t.Fatalf("tb hub rewrite-charset: %v %s", err, out)
			}
			for _, s := range []Store{imapS, jmapS} {
				added, _, err := s.Changes(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				for _, a := range added {
					if a.Tag == tag {
						ids = append(ids, a.ID)
					}
				}
				msgs, err := s.Fetch(ctx, ids)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, m := range msgs {
					found = found || bytes.Equal(m.Envelope, env)
				}
				if !found {
					t.Errorf("%T didn't read the rewritten message (%d candidates)", s, len(ids))
				}
			}
		})
	}
}

// A message moved out of the folder counts as removed (§10.1).
func TestMovedOut(t *testing.T) {
	jd, id, name := hub(t)
	ctx := context.Background()
	imapS, jmapS := open(t, id), open(t, jd)
	k := rnd(32)
	tag := tandem.CollectionTag(k, rnd(32))
	env, _ := tandem.Seal(k, tandem.KindRecord, tag, rnd(50), nil)
	uid, err := imapS.Append(ctx, tag, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Store{imapS, jmapS} {
		if added, _, err := s.Changes(ctx); err != nil || len(added) == 0 {
			t.Fatalf("%T: not seen: %v", s, err)
		}
	}
	if out, err := exec.Command(tb, "hub", "move", name, uid, "--to", "Archive").CombinedOutput(); err != nil {
		t.Fatalf("tb hub move: %v %s", err, out)
	}
	for _, s := range []Store{imapS, jmapS} {
		_, removed, err := s.Changes(ctx)
		if err != nil || len(removed) != 1 {
			t.Errorf("%T: removed %v, err %v", s, removed, err)
		}
	}
}

// A client opened after a restart reads and appends without calling
// EnsureFolder first (found by the device crash tests on the testbed).
func TestNoEnsureFolderNeeded(t *testing.T) {
	jd, id, _ := hub(t)
	ctx := context.Background()
	open(t, jd) // creates the folder
	for _, d := range []Descriptor{jd, id} {
		s, err := Open(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, _, err := s.Changes(ctx); err != nil {
			t.Fatalf("%s changes without EnsureFolder: %v", d.Kind, err)
		}
		if _, err := s.Append(ctx, "tag", []byte("TNDM\x01\x01")); err != nil {
			t.Fatalf("%s append without EnsureFolder: %v", d.Kind, err)
		}
	}
}
