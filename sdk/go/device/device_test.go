package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/store"
)

func tandemInboxTag(d *Device) string { return tandem.InboxTag(d.UID()) }

var ctx = context.Background()

type crashed struct{ point string }

// hub is where a world's devices meet: the in-memory hub, or with
// TANDEM_TESTBED=1 a fresh account on the testbed, reached over JMAP or IMAP
// by turns.
type hub interface {
	client(t *testing.T) store.Store
	messages(t *testing.T) map[string]store.Message
}

type memHub struct{ h *store.MemoryHub }

func (m memHub) client(*testing.T) store.Store                { return m.h.Client() }
func (m memHub) messages(*testing.T) map[string]store.Message { return m.h.Messages() }

type testbedHub struct {
	ds []store.Descriptor
	n  int
}

func (h *testbedHub) client(t *testing.T) store.Store {
	d := h.ds[h.n%len(h.ds)]
	h.n++
	s, err := store.Open(ctx, d)
	mustOK(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func (h *testbedHub) messages(t *testing.T) map[string]store.Message {
	s := h.client(t)
	added, _, err := s.Changes(ctx)
	mustOK(t, err)
	var ids []string
	for _, a := range added {
		ids = append(ids, a.ID)
	}
	msgs, err := s.Fetch(ctx, ids)
	mustOK(t, err)
	return msgs
}

func newHub(t *testing.T) hub {
	if os.Getenv("TANDEM_TESTBED") == "" {
		return memHub{store.NewMemoryHub()}
	}
	tb := filepath.Join("..", "..", "..", "testbed", "tb")
	name := "dev-" + fmt.Sprintf("%x", random(4))
	out, err := exec.Command(tb, "hub", "new", "--name", name).Output()
	mustOK(t, err)
	t.Cleanup(func() { exec.Command(tb, "hub", "delete", name).Run() })
	var raw struct{ JMAP, IMAP json.RawMessage }
	mustOK(t, json.Unmarshal(out, &raw))
	jd, err := store.ParseDescriptor(raw.JMAP)
	mustOK(t, err)
	id, err := store.ParseDescriptor(raw.IMAP)
	mustOK(t, err)
	return &testbedHub{ds: []store.Descriptor{jd, id}}
}

type world struct {
	t                 *testing.T
	hub               hub
	j1, j2, aimee, bo *Device
	cid               []byte
	rec               []byte
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func device(t *testing.T, h hub, name string, secret []byte) *Device {
	d, err := New(ctx, t.TempDir(), name, secret, h.client(t))
	mustOK(t, err)
	return d
}

// newWorld: Jesper owns "House" on two devices; Aimee and Bo are writers.
func newWorld(t *testing.T) *world {
	w := &world{t: t, hub: newHub(t), rec: bytes.Repeat([]byte{1}, 16)}
	w.j1 = device(t, w.hub, "Jesper", nil)
	w.j2 = device(t, w.hub, "Jesper", w.j1.Secret())
	w.aimee = device(t, w.hub, "Aimee", nil)
	w.bo = device(t, w.hub, "Bo", nil)
	cid, err := w.j1.NewCollection(ctx, "House")
	mustOK(t, err)
	w.cid = cid
	for _, m := range []*Device{w.aimee, w.bo} {
		mustOK(t, m.Expect(ctx, cid, w.j1.UID()))
		mustOK(t, w.j1.AddMember(ctx, cid, m.UID(), m.UKa(), m.Name, "writer"))
	}
	mustOK(t, w.j1.Set(ctx, cid, w.rec, "title", "milk"))
	w.syncAll()
	if v, _ := w.bo.Value(cid, w.rec, "title"); v != "milk" {
		t.Fatalf("Bo sees %q", v)
	}
	mustOK(t, w.aimee.Set(ctx, cid, w.rec, "notes", "oat"))
	w.syncAll()
	return w
}

func (w *world) syncAll() {
	for _, d := range []*Device{w.j1, w.j2, w.aimee, w.bo} {
		mustOK(w.t, d.Sync(ctx))
	}
}

// run calls f, turning a crash at a point into a value.
func run(f func() error) (point string, err error) {
	defer func() {
		if r := recover(); r != nil {
			c, ok := r.(crashed)
			if !ok {
				panic(r)
			}
			point = c.point
		}
	}()
	return "", f()
}

func TestCollaborate(t *testing.T) {
	w := newWorld(t)
	for _, d := range []*Device{w.j1, w.j2, w.aimee, w.bo} {
		if v, _ := d.Value(w.cid, w.rec, "notes"); v != "oat" {
			t.Errorf("%s sees notes %q", d, v)
		}
		if got := d.Members(w.cid); len(got) != 3 {
			t.Errorf("%s sees members %v", d, got)
		}
	}
}

// After the change: Aimee is gone, everyone else carries on in a new epoch
// with the old content, and Aimee can't read what follows.
func (w *world) checkRemoved() {
	t := w.t
	t.Helper()
	w.syncAll()
	for _, d := range []*Device{w.j1, w.j2, w.bo} {
		if d.Dead(w.cid) {
			t.Fatalf("%s: dead", d)
		}
		if _, ok := d.Members(w.cid)["Aimee"]; ok {
			t.Fatalf("%s: Aimee still listed: %v", d, d.Members(w.cid))
		}
		if v, _ := d.Value(w.cid, w.rec, "notes"); v != "oat" {
			t.Fatalf("%s: carried-over notes %q", d, v)
		}
		if d.Pending(w.cid) != nil {
			t.Fatalf("%s: a note is still pending", d)
		}
	}
	if !bytes.Equal(w.bo.Epoch(w.cid), w.j1.Epoch(w.cid)) {
		t.Fatal("Bo is not in the owner's epoch")
	}
	mustOK(t, w.bo.Set(ctx, w.cid, w.rec, "title", "bread"))
	w.syncAll()
	for _, d := range []*Device{w.j1, w.j2} {
		if v, _ := d.Value(w.cid, w.rec, "title"); v != "bread" {
			t.Fatalf("%s: new-epoch write %q", d, v)
		}
	}
	if v, _ := w.aimee.Value(w.cid, w.rec, "title"); v == "bread" {
		t.Fatal("Aimee reads the new epoch")
	}
	w.checkHub()
}

// checkHub: every message on the hub is in a channel the owner follows or
// an inbox (nothing left of a discarded change), and the old epochs hold
// only their rosters.
func (w *world) checkHub() {
	t := w.t
	t.Helper()
	chs := w.j1.channels()
	inboxes := map[string]bool{}
	for _, d := range []*Device{w.j1, w.aimee, w.bo} {
		inboxes[tandemInboxTag(d)] = true
	}
	current := w.j1.Epoch(w.cid)
	for id, m := range w.hub.messages(t) {
		ch, ok := chs[m.Tag]
		if !ok && !inboxes[m.Tag] && !w.isHomeOfOthers(m.Tag) {
			t.Fatalf("message %s in a channel nobody follows (left over from a discarded change?)", id)
		}
		if ok && ch.kind == "epoch" && !bytes.Equal(ch.epoch, current) {
			if !w.isRoster(ch, m) {
				t.Fatalf("message %s in an old epoch is not its roster", id)
			}
		}
	}
}

func (w *world) isRoster(ch channel, m store.Message) bool {
	_, pt, err := tandem.Open(ch.key, tandem.CollectionTag(ch.key, ch.epoch), m.Envelope)
	if err != nil {
		return false
	}
	rs, _, err := tandem.ParseKind1(pt, w.cid)
	return err == nil && rs != nil && bytes.Equal(rs.Record, tandem.RosterID)
}

func (w *world) isHomeOfOthers(tag string) bool {
	for _, d := range []*Device{w.aimee, w.bo} {
		if _, ok := d.channels()[tag]; ok {
			return true
		}
	}
	return false
}

func TestRemove(t *testing.T) {
	w := newWorld(t)
	mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
	w.checkRemoved()
}

var points = []string{"1:chosen", "2:journal", "2:note", "3:fragment", "3:data", "3:roster",
	"4:announced", "4:home", "5:grant", "6:cleaned"}

// crashAt removes Aimee on Jesper's first device, which stops at point.
func (w *world) crashAt(point string) {
	w.j1.Crash = func(p string) {
		if p == point {
			panic(crashed{p})
		}
	}
	got, err := run(func() error { return w.j1.Remove(ctx, w.cid, w.aimee.UID()) })
	if got != point {
		w.t.Fatalf("expected a crash at %s, got %q (err %v)", point, got, err)
	}
}

// reopen restarts Jesper's first device from its directory.
func (w *world) reopen() {
	d, err := Open(ctx, w.j1.dir, w.hub.client(w.t))
	mustOK(w.t, err)
	w.j1 = d
}

func TestCrashThenResume(t *testing.T) {
	for _, p := range points {
		t.Run(p, func(t *testing.T) {
			w := newWorld(t)
			w.crashAt(p)
			w.reopen()
			mustOK(t, w.j1.Resume(ctx))
			if p == "1:chosen" { // nothing was kept: the user asks again
				mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
			}
			w.checkRemoved()
		})
	}
}

func TestCrashThenSecondDevice(t *testing.T) {
	for _, p := range points {
		t.Run(p, func(t *testing.T) {
			w := newWorld(t)
			w.crashAt(p)
			mustOK(t, w.j2.Sync(ctx))
			n := w.j2.Pending(w.cid)
			switch {
			case n == nil:
				// No note: either nothing left the first device, or the
				// change was made and the note removed. The second device
				// asks for the removal if it isn't done.
				if _, listed := w.j2.Members(w.cid)["Aimee"]; listed {
					err := w.j2.Remove(ctx, w.cid, w.aimee.UID())
					if p >= "2:note" && !errors.Is(err, ErrPending) {
						mustOK(t, err)
					}
				}
			default:
				err := w.j2.Finish(ctx, w.cid)
				if errors.Is(err, ErrCantFinish) {
					mustOK(t, w.j2.Discard(ctx, w.cid))
					mustOK(t, w.j2.Remove(ctx, w.cid, w.aimee.UID())) // "do it here"
				} else {
					mustOK(t, err)
				}
			}
			// The first device comes back and finds out what happened.
			w.reopen()
			mustOK(t, w.j1.Resume(ctx))
			if _, listed := w.j1.Members(w.cid)["Aimee"]; listed {
				mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
			}
			w.checkRemoved()
		})
	}
}
