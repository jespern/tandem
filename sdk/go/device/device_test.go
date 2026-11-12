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
	jd.RebaseURLs = true // Stalwart advertises its own host name
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
			resumed(t, w.j1)
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
				err := w.j2.Finish(ctx, w.cid, n)
				if errors.Is(err, ErrCantFinish) {
					mustOK(t, w.j2.Discard(ctx, w.cid, n))
					mustOK(t, w.j2.Remove(ctx, w.cid, w.aimee.UID())) // "do it here"
				} else {
					mustOK(t, err)
				}
			}
			// The first device comes back and finds out what happened.
			w.reopen()
			resumed(t, w.j1)
			if _, listed := w.j1.Members(w.cid)["Aimee"]; listed {
				mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
			}
			w.checkRemoved()
		})
	}
}

// Finishing or discarding acts only on the change the user was shown: if the
// note in home now names another statement, nothing happens.
func TestStaleNoteIsLeftAlone(t *testing.T) {
	w := newWorld(t)
	w.crashAt("3:roster")
	mustOK(t, w.j2.Sync(ctx))
	shown := w.j2.Pending(w.cid)
	if shown == nil {
		t.Fatal("no pending change")
	}
	stale := *shown
	stale.Stmt = "not-the-statement-in-home"
	if err := w.j2.Discard(ctx, w.cid, &stale); !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("discarding a note that changed: %v", err)
	}
	if err := w.j2.Finish(ctx, w.cid, &stale); !errors.Is(err, ErrNoteChanged) {
		t.Fatalf("finishing a note that changed: %v", err)
	}
	mustOK(t, w.j2.Finish(ctx, w.cid, shown))
	w.reopen()
	resumed(t, w.j1)
	w.checkRemoved()
}

// ackLoss stores the first append to one tag but reports an error, as when
// the answer to an append is lost.
type ackLoss struct {
	store.Store
	tag  string
	lost bool
}

func (s *ackLoss) Append(ctx context.Context, tag string, body []byte) (string, error) {
	id, err := s.Store.Append(ctx, tag, body)
	if err == nil && tag == s.tag && !s.lost {
		s.lost = true
		return "", errors.New("answer lost after the append landed")
	}
	return id, err
}

// Sol's witness: the note reached home but its answer was lost. After a
// restart the device must find its own note and finish, not strand it.
func TestLostAnswerToTheNote(t *testing.T) {
	w := newWorld(t)
	loss := &ackLoss{Store: w.j1.hub, tag: tandem.HomeTag(w.j1.u.Home)}
	w.j1.hub = loss
	if err := w.j1.Remove(ctx, w.cid, w.aimee.UID()); err == nil {
		t.Fatal("expected the lost answer to surface")
	}
	if !loss.lost || w.j1.journal == nil || w.j1.journal.NoteWritten {
		t.Fatal("not at the intended point")
	}
	w.reopen()
	resumed(t, w.j1)
	w.checkRemoved()
}

// A flight of JSON null (conformance/'s spelling of "none") is no change.
func TestNullFlightIsNoChange(t *testing.T) {
	w := newWorld(t)
	mustOK(t, w.j1.setMembership(ctx, w.cid, "flight", []byte("null")))
	if w.j1.Pending(w.cid) != nil {
		t.Fatal("null read as a pending change")
	}
	mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
	w.checkRemoved()
}

// craft signs a write with chosen vectors as d's user, under the head
// statement, merges and publishes it: a modified client.
func craft(t *testing.T, d *Device, cid, rec []byte, field, value string, cv, base tandem.VV) *tandem.Write {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	c, h, ep, err := d.current(cid)
	mustOK(t, err)
	w := &tandem.Write{Scope: cid, Record: rec, Field: field, Op: tandem.OpAssign, Value: []byte(value),
		Dot: d.tick(), Ref: h.ID, Context: cv, Base: base}
	mustOK(t, w.Sign(d.u))
	d.mergeData(c, ep, w)
	mustOK(t, d.publishRecord(ctx, c, ep, d.key(cid, ep), rec))
	return w
}

// A write made in the old epoch by a device that hadn't seen the key change
// is signed again into the new one (§11.6).
func TestLateWriteSignedAgain(t *testing.T) {
	w := newWorld(t)
	mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
	// Bo hasn't synced: his write goes to the old epoch.
	mustOK(t, w.bo.Set(ctx, w.cid, w.rec, "title", "late bread"))
	w.syncAll() // Bo learns of the change and signs his write again
	w.syncAll()
	for _, d := range []*Device{w.j1, w.j2, w.bo} {
		if v, _ := d.Value(w.cid, w.rec, "title"); v != "late bread" {
			t.Fatalf("%s: title %q, want the late write", d, v)
		}
	}
	if v, _ := w.aimee.Value(w.cid, w.rec, "title"); v == "late bread" {
		t.Fatal("the removed member reads the signed-again write")
	}
}

// G-26: a member's forged context claims Bo's future and buries his write.
// After her removal Bo's device sees the claim is beyond anything it issued,
// and signs its write again, which shows once more (Fable's check).
func TestForgedClaimDoesNotOutliveRemoval(t *testing.T) {
	w := newWorld(t)
	mustOK(t, w.bo.Set(ctx, w.cid, w.rec, "title", "bread"))
	w.syncAll()
	// Her context covers everything she has seen, plus a claim on Bo's future.
	c := w.aimee.cols[string(w.cid)]
	ep := w.aimee.Epoch(w.cid)
	seen, _, _ := c.recs[string(ep)][string(w.rec)].Vectors(tandem.OpAssign, "title", nil, string(ep), 0)
	future := tandem.Join(seen, tandem.VV{{Device: w.bo.id, Millis: 1 << 47}})
	craft(t, w.aimee, w.cid, w.rec, "title", "junk", future, tandem.VV{})
	w.syncAll()
	if v, _ := w.j1.Value(w.cid, w.rec, "title"); v != "junk" {
		t.Fatalf("setup: title %q; the forged write should win inside the epoch (§1.2)", v)
	}
	mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
	w.syncAll()
	w.syncAll()
	for _, d := range []*Device{w.j1, w.bo} {
		if v, _ := d.Value(w.cid, w.rec, "title"); v != "bread" {
			t.Fatalf("%s: title %q after removal, want Bo's write back", d, v)
		}
	}
}

// Sol's safety net: a new-epoch write claims, plausibly, to have seen Bo's
// late write, which never reached it. Bo's device can't tell; it keeps the
// write and reports it as possibly unshared.
func TestUnprovableClaimIsReported(t *testing.T) {
	w := newWorld(t)
	mustOK(t, w.j1.Remove(ctx, w.cid, w.aimee.UID()))
	mustOK(t, w.bo.Set(ctx, w.cid, w.rec, "title", "late bread")) // old epoch, unseen
	var late *tandem.Write
	for _, h := range w.bo.cols[string(w.cid)].own {
		if string(h.W.Value) == "late bread" {
			late = h.W
		}
	}
	// Jesper's modified client claims Bo's dot in the new epoch's base.
	mustOK(t, w.j1.Sync(ctx))
	claim := tandem.Join(late.Base, late.Context, tandem.VV{late.Dot})
	craft(t, w.j1, w.cid, w.rec, "title", "owner's", tandem.VV{}, claim)
	w.syncAll()
	got := w.bo.Unshared(w.cid)
	if len(got) != 1 || string(got[0].Value) != "late bread" {
		t.Fatalf("unshared: %d writes, want Bo's late write", len(got))
	}
}

// resumed restarts a journalled change; "nothing changed" is an answer, not
// a failure: another device discarded or overtook it, and the user is told.
func resumed(t *testing.T, d *Device) {
	t.Helper()
	if err := d.Resume(ctx); err != nil && !errors.Is(err, ErrSuperseded) {
		t.Fatal(err)
	}
}
