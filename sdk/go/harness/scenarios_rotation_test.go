package harness

// scenarios/rotation.md: epochs and key changes. Crash points map to the
// six steps as device.Crash names them: S1 "1:chosen", S2 "2:note", S3
// "3:data" (partway) and "3:roster" (done), S4 "4:announced", S5
// "5:grant", S6 "6:cleaned". The file's E3 is the harness's E0; only the
// order of epochs matters.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/device"
)

// rotHouse is rotation.md's House: J owner (J.phone, J.mac), A and B
// writers, C reader, with a few items. extra devices are created first.
func rotHouse(t *testing.T, extra ...string) *World {
	w := NewWorld(t, oracle)
	for _, d := range append([]string{"J.phone", "J.mac", "A.phone", "B.phone", "C.phone"}, extra...) {
		w.Dev(d)
	}
	w.Creates("J.mac", house)
	w.Shares("J.mac", house, "A", "writer")
	w.Shares("J.mac", house, "B", "writer")
	w.Shares("J.mac", house, "C", "reader")
	w.Settle()
	for _, item := range []string{"milk", "jam", "lamp"} {
		w.must(w.Adds("A.phone", house, item))
	}
	w.must(w.Adds("B.phone", house, "rug"))
	w.Settle()
	return w
}

// stopsAt runs a removal on the owner's device and wants it to stop at a
// crash point.
func stopsAt(w *World, ownerDev, point string, op func() error) {
	w.T.Helper()
	died, err := w.Crashes(ownerDev, point, op)
	w.must(err)
	if !died {
		w.T.Fatalf("%s didn't reach %s", ownerDev, point)
	}
}

func hasWrite(ws []*tandem.Write, x *tandem.Write) bool {
	id, _ := x.ID()
	return slices.ContainsFunc(ws, func(y *tandem.Write) bool { yid, _ := y.ID(); return bytes.Equal(yid, id) })
}

const removedUnsharedBug = "engine bug: a removed member's unsent edit isn't handed to the app: resign returns early when the device holds no key for the current epoch, so Unshared stays empty (§11.6: kept read-only so unsent work can be rescued)"

// flushLeak is RC-014's finding, where a scenario meets it again.
const flushLeak = "engine bug: a returning device publishes its queued edits to the old epoch's channel before reading the announcement (RC-014)"

func leakedTo(w *World, user, coll string, epoch []byte, item string, x *tandem.Write) bool {
	for _, ws := range w.Payloads(user, coll, epoch, item) {
		if hasWrite(ws, x) {
			return true
		}
	}
	return false
}

func TestRO001(t *testing.T) {
	w := rotHouse(t, "J.ipad")
	e3 := w.Epoch("J.mac", house)
	g := w.Statement("J.mac", house, w.Head("J.mac", house))
	w.Offline("B.phone")
	w.must(w.Sets("B.phone", house, "rug", "notes", "b1"))
	b1 := w.WriteOf(house, "rug", "notes", "b1")

	// Stop after S2: the note is in home, the journal on J.phone.
	stopsAt(w, "J.phone", "2:note", func() error { return w.Removes("J.phone", house, "B") })
	_, err := os.Stat(filepath.Join(w.devs["J.phone"].dir, "journal.json"))
	w.must(err)
	w.Syncs("J.mac")
	n := w.Pending("J.mac", house)
	if n == nil {
		t.Fatal("no flight note in J's home")
	}
	w.Want("note's members", len(n.Members), 3)
	// Stop again after S3: channel E4 is complete, nothing announced.
	w.Opens("J.phone")
	stopsAt(w, "J.phone", "3:roster", func() error { return w.Dev("J.phone").Resume(ctx) })
	e4, _ := tandem.ParseB64u(n.Stmt)
	w.Syncs("A.phone")
	w.Want("A.phone still in E3", fmt.Sprintf("%x", w.Epoch("A.phone", house)), fmt.Sprintf("%x", e3))
	w.Want("no grant before the announcement", w.Granted("A", house)[string(e4)], false)
	w.Want("E4's roster in channel E4", w.RosterMessages("J", house, e4) > 0, true)
	w.Want("rug carried into E4", len(w.Payloads("J", house, e4, "rug")) > 0, true)

	w.mustLaunch(w.Launches("J.phone"))
	w.Forget(house, "B")
	w.Syncs("J.phone", "A.phone", "J.mac", "J.ipad")
	s := w.Statement("J.mac", house, e4)
	w.Want("n", s.Body.N, g.Body.N+1)
	w.Want("epoch counter", s.Body.KeyN, g.Body.KeyN+1)
	w.Want("members", w.Roles("J.mac", house), map[string]string{"J": "owner", "A": "writer", "C": "reader"})
	_, stmts := w.Dev("J.mac").Roster(w.cid(house))
	for _, x := range stmts {
		if !bytes.Equal(x.ID, e4) && bytes.Equal(x.Body.Commit, s.Body.Commit) {
			t.Fatal("E4's key commitment occurs earlier")
		}
	}
	_, inv := w.Dev("J.mac").Inventory(w.cid(house))
	b, _ := inv.Bytes()
	w.Want("snapshot", fmt.Sprintf("%x", s.Body.Snapshot), fmt.Sprintf("%x", tandem.H(b)))
	w.Want("note removed", w.Pending("J.mac", house) == nil, true)
	w.Want("grant to A", w.Granted("A", house)[string(e4)], true)
	w.Want("grant to C", w.Granted("C", house)[string(e4)], true)
	w.Want("grant to B", w.Granted("B", house)[string(e4)], false)
	w.Want("rug by", w.Author(house, "rug", "title"), "B")
	w.Want("E3's data cleaned up", len(w.Payloads("A", house, e3, "milk")), 0)
	w.Want("E3's roster stays", w.RosterMessages("A", house, e3) > 0, true)

	w.Online("B.phone")
	w.Syncs("B.phone")
	w.Settle()
	checkAll(w)
	w.WantCut("B", house, e4)
	// B.phone's read-only replica isn't exposed by the engine (State shows
	// the current epoch, which it can't read).
	if !hasWrite(w.Unshared("B.phone", house), b1) {
		knownBug(t, removedUnsharedBug)
	}
	if leakedTo(w, "B", house, e3, "rug", b1) {
		knownBug(t, flushLeak)
	}
}

func TestRO002(t *testing.T) {
	t.Skip("needs: leave notices (§11.5)")
}

func TestRO004(t *testing.T) {
	w := rotHouse(t)
	e3 := w.Epoch("J.mac", house)
	w.Offline("A.phone")
	w.must(w.Sets("A.phone", house, "milk", "title", "A's milk"))
	w.must(w.Sets("A.phone", house, "jam", "notes", "A's jam"))
	m := w.WriteOf(house, "milk", "title", "A's milk")
	w.must(w.Removes("J.mac", house, "B"))
	w.Settle()
	w.must(w.Deletes("J.mac", house, "jam"))
	w.Settle()
	w.Online("A.phone")
	w.Syncs("A.phone")
	w.Settle()
	checkAll(w)
	e4 := w.Epoch("J.mac", house)
	w.Want("A.phone moved", fmt.Sprintf("%x", w.Epoch("A.phone", house)), fmt.Sprintf("%x", e4))
	w.Want("A holds K(E4)", w.Keys("A", house)[string(e4)] != nil, true)
	again := 0
	for _, x := range append(w.Writes(house, "milk", "title"), w.Writes(house, "jam", "notes")...) {
		if w.userOf(x.Author) == "A" && len(x.Context) == 0 && len(x.Base) > 0 {
			again++
			first := w.Statement("J.mac", house, x.Ref)
			w.Want("signed again under E4", fmt.Sprintf("%x", w.Epoch("J.mac", house)), fmt.Sprintf("%x", e4))
			w.Want("ref lists A as writer", first != nil && first.Body.Role(w.UID("A")) == "writer", true)
		}
	}
	w.Want("signed again", again, 2)
	w.WantGone(house, "jam", "J.mac", "A.phone")
	w.WantKept(house, "jam", "notes", "A's jam") // in the deleted record, never shown
	w.WantShows(house, "milk", "title", "A's milk", "J.mac", "A.phone")
	if leakedTo(w, "J", house, e3, "milk", m) {
		knownBug(t, flushLeak)
	}
}

func TestRO005(t *testing.T) {
	t.Skip("needs: removing several members in one change")
}

func TestRO006(t *testing.T) {
	w := rotHouse(t)
	before := w.View()
	g := w.Statement("J.mac", house, w.Head("J.mac", house))
	state := w.Dev("A.phone").State(w.cid(house))
	w.must(w.Rotates("J.mac", house))
	w.Settle()
	checkAll(w)
	e4 := w.Epoch("J.mac", house)
	s := w.Statement("J.mac", house, e4)
	w.Want("counter", s.Body.KeyN, g.Body.KeyN+1)
	w.Want("fresh key", bytes.Equal(s.Body.Commit, g.Body.Commit), false)
	w.Want("inventory committed", s.Body.Snapshot != nil, true)
	for _, u := range []string{"A", "B", "C"} {
		w.Want(u+"'s grant", w.Granted(u, house)[string(e4)], true)
	}
	w.WantDelta(before, w.View(), epochs(house, "E1", "J.phone", "J.mac", "A.phone", "B.phone", "C.phone"))
	w.Want("records unchanged", w.Dev("A.phone").State(w.cid(house)), state)
}

func TestRO007(t *testing.T) {
	w := rotHouse(t)
	w.Offline("B.phone")
	w.must(w.Sets("B.phone", house, "rug", "notes", "b1"))
	b1 := w.WriteOf(house, "rug", "notes", "b1")
	w.must(w.Removes("J.mac", house, "B"))
	w.Online("B.phone")
	w.Syncs("B.phone")
	unshared := hasWrite(w.Unshared("B.phone", house), b1)
	e4 := w.Statement("J.mac", house, w.Epoch("J.mac", house))
	w.Shares("J.mac", house, "B", "writer") // the new invite's end
	s5 := w.Statement("J.mac", house, w.Head("J.mac", house))
	w.Syncs("B.phone")
	w.Settle()
	checkAll(w)
	w.Want("s5 keeps E4's key", bytes.Equal(s5.Body.Commit, e4.Body.Commit), true)
	w.Want("s5 keeps E4's snapshot", bytes.Equal(s5.Body.Snapshot, e4.Body.Snapshot), true)
	w.Want("B follows House", w.EpochLabel("B.phone", house), "E1")
	w.Want("B's grant", w.Granted("B", house)[string(e4.ID)], true)
	for _, x := range w.Writes(house, "rug", "notes") {
		if x.Dot == b1.Dot && !bytes.Equal(x.Ref, b1.Ref) {
			w.Want("b1 signed again naming s5", fmt.Sprintf("%x", x.Ref), fmt.Sprintf("%x", s5.ID))
		}
	}
	// "b1 may now be signed again": the engine keeps it for the app.
	if !unshared {
		knownBug(t, removedUnsharedBug)
	}
}

func TestRO008(t *testing.T) {
	t.Skip("needs: queuing an admission behind the device's own key change (invites)")
}

func TestRO009(t *testing.T) {
	t.Skip("needs: invites with auto-approve")
}

func TestRO010(t *testing.T) {
	t.Skip("needs: leave notices (§11.5)")
}

func TestRO011(t *testing.T) {
	w := rotHouse(t)
	stopsAt(w, "J.mac", "3:roster", func() error { return w.Removes("J.mac", house, "B") })
	w.Opens("J.mac")
	if err := w.MakesRole("J.mac", house, "C", "writer"); !errors.Is(err, device.ErrPending) {
		t.Fatalf("a second change while the first is unannounced: %v", err)
	}
	w.must(w.Dev("J.mac").Resume(ctx))
	w.Forget(house, "B")
	e4 := w.Epoch("J.mac", house)
	w.must(w.MakesRole("J.mac", house, "C", "writer"))
	w.Settle()
	checkAll(w)
	if !bytes.Equal(w.Epoch("J.mac", house), e4) {
		knownBug(t, promotionBug)
	}
	w.Want("C's promotion follows E4", fmt.Sprintf("%x", w.Statement("J.mac", house, w.Head("J.mac", house)).Parent), fmt.Sprintf("%x", e4))
}

func TestRO012(t *testing.T) {
	t.Skip("needs: hub withholding (two owner devices signing without seeing each other's note)")
}

func TestRO013(t *testing.T) {
	w := rotHouse(t)
	w.Offline("B.phone")
	w.must(w.Sets("B.phone", house, "rug", "notes", "b1"))
	b1 := w.WriteOf(house, "rug", "notes", "b1")
	w.must(w.MakesRole("J.mac", house, "B", "reader"))
	e4 := w.Epoch("J.mac", house)
	w.Online("B.phone")
	w.Syncs("B.phone")
	w.Want("E4 starts an epoch", w.EpochLabel("J.mac", house), "E1")
	w.Want("B a reader", w.Roles("J.mac", house)["B"], "reader")
	w.Want("B holds K(E4)", w.Keys("B", house)[string(e4)] != nil, true)
	w.Want("b1 kept for the app", hasWrite(w.Unshared("B.phone", house), b1), true)
	w.Signs("B.phone", house, Forged{Item: "rug", Field: "title", Value: "B's rug", Ref: e4})
	w.Settle()
	checkAll(w)
	w.WantShows(house, "rug", "title", "rug", "J.mac", "A.phone", "B.phone")
	w.Want("rug in E4 by", w.Author(house, "rug", "title"), "B")
}

func TestRO017(t *testing.T) {
	t.Skip("needs: the 30-day read-only period of a removed member's replica")
}

func TestRO019(t *testing.T) {
	w := NewWorld(t, oracle)
	for _, d := range []string{"J.mac", "A.mac", "B.phone", "C.phone"} {
		w.Dev(d)
	}
	w.Creates("J.mac", house)
	w.Shares("J.mac", house, "A", "writer")
	w.Shares("J.mac", house, "B", "writer")
	w.Shares("J.mac", house, "C", "writer")
	w.must(w.Adds("J.mac", house, "milk"))
	w.Settle()
	e3 := w.Epoch("J.mac", house)
	w.Offline("A.mac")
	w.must(w.Sets("A.mac", house, "milk", "title", "a1"))
	a1 := w.WriteOf(house, "milk", "title", "a1")
	w.must(w.Removes("J.mac", house, "B"))
	e4 := w.Epoch("J.mac", house)
	w.must(w.Removes("J.mac", house, "C"))
	e5 := w.Epoch("J.mac", house)
	w.Settle()
	w.Online("A.mac")
	w.Syncs("A.mac")
	w.Settle()
	checkAll(w)
	w.Want("A.mac in E5", fmt.Sprintf("%x", w.Epoch("A.mac", house)), fmt.Sprintf("%x", e5))
	var again *tandem.Write
	for _, x := range w.Writes(house, "milk", "title") {
		if x.Dot == a1.Dot && !bytes.Equal(x.Ref, a1.Ref) {
			again = x
		}
	}
	if again == nil {
		t.Fatal("a1 wasn't signed again")
	}
	w.Want("a1' names E5's first statement listing A", fmt.Sprintf("%x", again.Ref), fmt.Sprintf("%x", e5))
	w.WantShows(house, "milk", "title", "a1", "J.mac", "A.mac")
	if leakedTo(w, "J", house, e4, "milk", a1) || leakedTo(w, "J", house, e3, "milk", a1) {
		knownBug(t, flushLeak)
	}
}

// RO-020: a key change interrupted at each step, resumed on J.mac or
// handled on J.phone.
func TestRO020(t *testing.T) {
	steps := []struct{ name, point string }{
		{"S1", "1:chosen"}, {"S2", "2:note"}, {"S3 partial", "3:data"}, {"S3 done", "3:roster"},
		{"S4", "4:announced"}, {"S5", "5:grant"}, {"S6", "6:cleaned"},
	}
	for _, s := range steps {
		for _, variant := range []string{"resume", "other device"} {
			t.Run(s.name+" "+variant, func(t *testing.T) {
				w := rotHouse(t)
				for i := 0; i < 40; i++ {
					w.must(w.Adds("A.phone", house, fmt.Sprintf("r%02d", i)))
				}
				w.Settle()
				stopsAt(w, "J.mac", s.point, func() error { return w.Removes("J.mac", house, "B") })
				announced := s.point >= "4"
				if !announced {
					// Members keep publishing to the old epoch; nothing stalls.
					w.must(w.Sets("A.phone", house, "r00", "notes", "during"))
					w.Syncs("A.phone")
				}
				switch variant {
				case "resume":
					w.mustLaunch(w.Launches("J.mac"))
					if s.point == "1:chosen" {
						w.Want("nothing to resume", w.Roles("J.mac", house)["B"], "writer")
						w.must(w.Removes("J.mac", house, "B")) // J asks again
					}
				case "other device":
					w.Syncs("J.phone")
					if n := w.Pending("J.phone", house); n != nil {
						err := w.Finishes("J.phone", house)
						if errors.Is(err, device.ErrCantFinish) {
							w.must(w.Discards("J.phone", house))
							w.must(w.Removes("J.phone", house, "B")) // "do it here"
						} else {
							w.must(err)
						}
					} else if w.Roles("J.phone", house)["B"] != "" {
						w.must(w.Removes("J.phone", house, "B"))
					}
					w.mustLaunch(w.Launches("J.mac")) // comes back later
				}
				w.Forget(house, "B")
				w.Settle()
				checkAll(w)
				w.Want("roster", w.Roles("A.phone", house), map[string]string{"J": "owner", "A": "writer", "C": "reader"})
				cur := w.Epoch("J.phone", house)
				w.WantCut("B", house, cur)
				for _, u := range []string{"A", "C"} {
					w.Want(u+" holds the key", w.Keys(u, house)[string(cur)] != nil, true)
				}
				for i := 0; i < 40; i++ {
					w.Want("record", w.Present("A.phone", house, fmt.Sprintf("r%02d", i)), true)
				}
				if !announced {
					w.WantShows(house, "r00", "notes", "during", "J.phone", "A.phone", "C.phone")
				}
			})
		}
	}
}

func TestRO022(t *testing.T) {
	w := rotHouse(t, "J.ipad")
	stopsAt(w, "J.mac", "3:data", func() error { return w.Removes("J.mac", house, "B") })
	w.Syncs("J.ipad")
	n := w.Pending("J.ipad", house)
	if n == nil {
		t.Fatal("J.ipad didn't read the note")
	}
	draft, _ := tandem.ParseB64u(n.Stmt)
	w.Offline("J.ipad")
	w.must(w.Discards("J.phone", house))
	w.Want("the draft's channel destroyed", w.Messages("J", house, draft), 0)
	w.Online("J.ipad")
	w.Syncs("J.ipad")
	w.Want("J.ipad stays in E3", w.EpochLabel("J.ipad", house), "E0")
	w.must(w.Sets("J.ipad", house, "milk", "notes", "from the iPad"))
	w.mustLaunch(w.Launches("J.mac"))
	w.Settle()
	checkAll(w)
	w.Want("J.mac didn't announce", w.EpochLabel("J.mac", house), "E0")
	w.Want("B still a member", w.Roles("A.phone", house)["B"], "writer")
	w.WantShows(house, "milk", "notes", "from the iPad", "B.phone", "A.phone")
	w.Want("journal gone", fileExists(filepath.Join(w.devs["J.mac"].dir, "journal.json")), false)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestRO023(t *testing.T) {
	t.Skip("needs: device clock control")
}

func TestRO024(t *testing.T) {
	w := rotHouse(t)
	e3head := w.Head("B.phone", house)
	e3 := w.Epoch("B.phone", house)
	w.must(w.Removes("J.mac", house, "B"))
	w.Settle()
	e4 := w.Epoch("J.mac", house)
	x := w.Sign("B.phone", house, Forged{Item: "milk", Field: "title", Value: "backdated", Ref: e3head,
		Dot: w.Dot("B.phone", 1)})
	w.Publish("B.phone", house, e3, x)
	w.Settle()
	checkAll(w)
	w.WantShows(house, "milk", "title", "milk", "J.mac", "A.phone", "C.phone")
	w.PublishWith(w.Keys("J", house)[string(e4)], e4, x) // a stolen K(E4)
	w.Settle()
	checkAll(w)
	w.WantShows(house, "milk", "title", "milk", "J.mac", "A.phone", "C.phone")
}

func TestRO025(t *testing.T) {
	w := rotHouse(t)
	w.must(w.Sets("B.phone", house, "rug", "notes", "B's old note"))
	w.Settle()
	x := w.WriteOf(house, "rug", "notes", "B's old note")
	w.must(w.Sets("A.phone", house, "rug", "notes", "A's note")) // replaces it
	w.Settle()
	w.must(w.Rotates("J.mac", house))
	w.Settle()
	w.must(w.Rotates("J.mac", house))
	w.Settle()
	e := w.Epoch("C.phone", house)
	state := w.Dev("A.phone").State(w.cid(house))
	_, inv := w.Dev("C.phone").Inventory(w.cid(house))
	for _, y := range w.Writes(house, "rug", "*") {
		id, _ := y.ID()
		if slices.ContainsFunc(inv[string(ItemID(house, "rug"))], func(z []byte) bool { return bytes.Equal(z, id) }) {
			w.Publish("C.phone", house, e, y) // listed: accepted, nothing changes
		}
	}
	w.Publish("C.phone", house, e, x) // not listed: rejected
	w.Settle()
	checkAll(w)
	w.Want("state", w.Dev("A.phone").State(w.cid(house)), state)
	w.WantKept(house, "rug", "notes", "A's note")
}

func TestRO027(t *testing.T) {
	w := rotHouse(t, "A.mac")
	w.Offline("A.mac")
	w.must(w.Sets("A.mac", house, "lamp", "title", "A.mac's lamp"))
	w.must(w.Deletes("A.phone", house, "lamp"))
	w.Settle()
	w.must(w.Removes("J.mac", house, "B"))
	w.Settle()
	w.Online("A.mac")
	w.Settle()
	checkAll(w)
	w.WantGone(house, "lamp", "J.mac", "A.phone", "A.mac", "C.phone")
	w.WantKept(house, "lamp", "title", "A.mac's lamp")
}

func TestRO028(t *testing.T) {
	t.Skip("needs: hub destroy and key requests (§11.4)")
}

func TestRO029(t *testing.T) {
	t.Skip("needs: hub destroy, healing (§12.2) and key requests")
}

func TestRO033(t *testing.T) {
	t.Run("member's statement", func(t *testing.T) {
		w := rotHouse(t, "M.phone")
		w.Shares("J.mac", house, "M", "writer")
		w.Settle()
		w.SignsStatement("M.phone", house, func(st *tandem.Statement) { st.Members = st.Members[:1] })
		w.Settle()
		checkAll(w)
		w.Want("alive", w.Dead("A.phone", house), false)
	})
	t.Run("invalid owner statement and a valid one", func(t *testing.T) {
		w := rotHouse(t, "J.ipad")
		w.SignsStatement("J.ipad", house, func(st *tandem.Statement) { st.N++ }) // skips one
		w.Syncs("J.mac")
		w.Want("a lone invalid statement kills nothing", w.Dead("J.mac", house), false)
		w.must(w.Removes("J.mac", house, "B"))
		w.Forget(house, "B")
		w.Settle()
		w.CheckDead(house)
	})
	t.Run("replayed grant", func(t *testing.T) { t.Skip("needs: hub replay") })
	invalid := map[string]func(*tandem.Statement, *World){
		"two owners":     func(st *tandem.Statement, w *World) { st.Members[1].Role = "owner" },
		"a fourth role":  func(st *tandem.Statement, w *World) { st.Members[1].Role = "admin" },
		"a changed name": func(st *tandem.Statement, w *World) { st.Members[1].Name = "Aimee N." },
		"an epoch start adding a member": func(st *tandem.Statement, w *World) {
			st.Key = tandem.EpochKey{N: st.Key.N + 1, Commit: tandem.B64u(tandem.H([]byte("new key"))), Prev: st.Key.Commit}
			st.Snapshot = tandem.B64u(tandem.H([]byte("inventory")))
			st.Members = append(st.Members, tandem.Member{UID: tandem.B64u(w.UID("M")), Role: "writer", UKa: tandem.B64u(w.user("M").UKa), Name: "M"})
		},
		"an epoch start reusing a commitment": func(st *tandem.Statement, w *World) {
			st.Key = tandem.EpochKey{N: st.Key.N + 1, Commit: st.Key.Commit, Prev: st.Key.Commit}
			st.Snapshot = tandem.B64u(tandem.H([]byte("inventory")))
		},
		"a same-epoch statement with a new snapshot": func(st *tandem.Statement, w *World) {
			st.Snapshot = tandem.B64u(tandem.H([]byte("another inventory")))
		},
	}
	for _, name := range sortedKeys(invalid) {
		t.Run(name, func(t *testing.T) {
			w := rotHouse(t, "M.phone")
			head := w.Answer(house).Head
			before := w.View()
			w.SignsStatement("J.mac", house, func(st *tandem.Statement) { invalid[name](st, w) })
			w.Settle()
			checkAll(w)
			w.WantDelta(before, w.View(), nil)
			w.Want("the oracle's head", w.Answer(house).Head, head)
			w.Want("alive", w.Dead("A.phone", house), false)
		})
	}
	t.Run("a successor of a deleted statement", func(t *testing.T) {
		t.Skip("needs: deleting a collection (§11.9)")
	})
}

func TestRO034(t *testing.T) {
	w := rotHouse(t)
	w.Offline("J.phone")
	n := w.Hub.Len()
	if err := w.Removes("J.phone", house, "B"); err == nil {
		t.Fatal("a removal without a network")
	}
	w.Want("nothing signed", fileExists(filepath.Join(w.devs["J.phone"].dir, "journal.json")), false)
	w.must(w.Sets("J.phone", house, "milk", "notes", "offline edit"))
	w.Want("hub", w.Hub.Len(), n)
	w.Online("J.phone")
	w.Settle()
	checkAll(w)
	w.WantShows(house, "milk", "notes", "offline edit", "A.phone", "B.phone")
}

func TestRO035(t *testing.T) {
	// The key request after 10 minutes needs key requests (§11.4).
	w := rotHouse(t)
	stopsAt(w, "J.phone", "4:home", func() error { return w.Removes("J.phone", house, "B") })
	w.Forget(house, "B")
	w.Down("J.mac")
	w.Syncs("A.phone")
	w.Want("A.phone knows of E4", w.EpochLabel("A.phone", house), "E1")
	if err := w.Sets("A.phone", house, "milk", "notes", "while waiting"); err != nil {
		knownBug(t, "engine bug: a member waiting for the new epoch's key can't edit ("+err.Error()+"); edits should apply locally and queue (§11.6, RO-035)")
	}
	w.mustLaunch(w.Launches("J.phone"))
	w.mustLaunch(w.Launches("J.mac"))
	w.Settle()
	checkAll(w)
	w.WantShows(house, "milk", "notes", "while waiting", "J.phone", "C.phone")
}

func TestRO036(t *testing.T) {
	w := rotHouse(t)
	w.Offline("B.phone")
	w.must(w.Sets("B.phone", house, "rug", "notes", "b1"))
	b1 := w.WriteOf(house, "rug", "notes", "b1")
	w.must(w.MakesRole("J.mac", house, "B", "reader"))
	w.Online("B.phone")
	w.Syncs("B.phone")
	w.Want("B told he reads only", w.Roles("B.phone", house)["B"], "reader")
	w.Want("b1 offered for rescue", hasWrite(w.Unshared("B.phone", house), b1), true)
	w.Settle()
	checkAll(w)
}

func TestRO037(t *testing.T) {
	t.Skip("needs: hub destroy and healing (§12.2)")
}

func TestRO038(t *testing.T) {
	t.Skip("needs: hub destroy and healing of inventory fragments")
}

func TestRO039(t *testing.T) {
	w := rotHouse(t, "M.phone")
	w.Shares("J.mac", house, "M", "writer")
	w.Settle()
	w.must(w.Sets("M.phone", house, "milk", "notes", "M's old note"))
	w.Settle()
	x := w.WriteOf(house, "milk", "notes", "M's old note")
	w.must(w.Sets("A.phone", house, "milk", "notes", "A's note")) // replaces it
	w.Settle()
	w.must(w.Rotates("J.mac", house))
	w.Settle()
	e4 := w.Epoch("M.phone", house)
	xid, _ := x.ID()
	forged := tandem.Inventory{string(ItemID(house, "milk")): {xid}}
	w.PublishFragment("M.phone", house, e4, 1, 3, forged)
	w.PublishFragment("M.phone", house, e4, 0, 2, forged)
	w.PublishFragment("M.phone", house, e4, 0, 1, forged)
	w.Publish("M.phone", house, e4, x)
	w.Dev("D.phone") // a member who joins afterwards reads everything fresh
	w.Shares("J.mac", house, "D", "writer")
	w.Settle()
	if !reflect.DeepEqual(w.Dev("D.phone").State(w.cid(house)), w.Dev("J.mac").State(w.cid(house))) {
		// Order-dependent: inbox() reads the granted channel in map order.
		knownBug(t, "engine bug: a forged inventory fragment can replace the real one: the device keeps one byte string per part (last wins) instead of each version separately, so when the fragments arrive before the statement that commits them, the inventory never assembles and the joiner misses every carried record")
	}
	checkAll(w)
	w.WantKept(house, "milk", "notes", "A's note")
	w.WantShows(house, "milk", "notes", "A's note", "A.phone", "D.phone", "C.phone")
}

func TestRO040(t *testing.T) {
	for _, variant := range []string{"both devices", "A.phone gone"} {
		t.Run(variant, func(t *testing.T) {
			w := rotHouse(t, "A.mac")
			stopsAt(w, "J.mac", "2:note", func() error { return w.Removes("J.mac", house, "C") })
			w.must(w.Sets("A.phone", house, "milk", "title", "x"))
			x := w.WriteOf(house, "milk", "title", "x")
			w.Syncs("A.mac")
			if variant == "A.phone gone" {
				w.Down("A.phone")
			}
			w.mustLaunch(w.Launches("J.mac"))
			w.Forget(house, "C")
			w.Settle()
			ids := map[string]bool{}
			for _, y := range w.Writes(house, "milk", "title") {
				if y.Dot == x.Dot && !bytes.Equal(y.Ref, x.Ref) {
					id, _ := y.ID()
					ids[string(id)] = true
				}
			}
			if variant == "A.phone gone" && len(ids) == 0 {
				_, shown := w.Shows("J.mac", house, "milk", "title")
				knownBugf(t, "engine bug: only the device that made a late write signs it again (§11.6: every device of the user), so x is lost with A.phone; and the listed write x replaced is lost too: J.mac didn't upload it to E4 (it no longer keeps it) and S6 destroyed its E3 message as replaced, though it was replaced only in E3. milk shows a title on J.mac: %v; the oracle keeps the listed one", shown)
			}
			checkAll(w)
			w.Want("one write ID for x signed again", len(ids), 1)
			w.WantShows(house, "milk", "title", "x", "J.mac", "A.mac")
		})
	}
}

func TestRO041(t *testing.T) {
	w := rotHouse(t)
	w.must(w.Sets("A.phone", house, "milk", "title", "x"))
	w.Settle()
	w.Offline("A.phone")
	w.must(w.Sets("J.mac", house, "milk", "title", "y"))
	w.must(w.Removes("J.mac", house, "C"))
	w.Forget(house, "C")
	w.Online("A.phone")
	w.Settle()
	checkAll(w)
	x := w.WriteOf(house, "milk", "title", "x")
	for _, z := range w.Writes(house, "milk", "title") {
		if z.Dot == x.Dot && !bytes.Equal(z.Ref, x.Ref) {
			t.Fatal("A.phone signed a replaced write again")
		}
	}
	w.WantKept(house, "milk", "title", "y")
}

func TestRO042(t *testing.T) {
	w := rotHouse(t)
	w.Offline("B.phone")
	w.must(w.Sets("B.phone", house, "rug", "notes", "b1"))
	w.must(w.MakesRole("J.mac", house, "B", "reader"))
	e4 := w.Epoch("J.mac", house)
	w.must(w.MakesRole("J.mac", house, "B", "writer"))
	if !bytes.Equal(w.Epoch("J.mac", house), e4) {
		knownBug(t, promotionBug)
	}
	s5 := w.Head("J.mac", house)
	w.Online("B.phone")
	w.Settle()
	checkAll(w)
	// b1 exists twice: as made (under E3's head) and signed again. The
	// copy names s5, the first statement of the epoch that lets B write.
	b1 := w.WriteOf(house, "rug", "notes", "b1")
	resigned := false
	for _, x := range w.Writes(house, "rug", "notes") {
		if x.Dot == b1.Dot && bytes.Equal(x.Ref, s5) {
			resigned = true
		}
	}
	w.Want("b1 signed again naming s5", resigned, true)
	w.WantShows(house, "rug", "notes", "b1", "J.mac", "A.phone")
}

func TestRO043(t *testing.T) {
	t.Skip("needs: an honest write with a base at the vector limit (hand-built)")
}

func TestRO044(t *testing.T) {
	w := rotHouse(t)
	stopsAt(w, "J.phone", "3:roster", func() error { return w.MakesRole("J.phone", house, "A", "reader") })
	w.Syncs("J.mac")
	if err := w.Removes("J.mac", house, "C"); !errors.Is(err, device.ErrPending) {
		t.Fatalf("J.mac signed while J.phone's change was pending: %v", err)
	}
	w.Want("J.mac is shown", w.Pending("J.mac", house).What, "change a role")
	w.must(w.Finishes("J.mac", house))
	w.members[house]["A"] = "reader"
	e4 := w.Epoch("J.mac", house)
	w.must(w.Removes("J.mac", house, "C"))
	w.Want("E5 follows E4", fmt.Sprintf("%x", w.Statement("J.mac", house, w.Epoch("J.mac", house)).Parent), fmt.Sprintf("%x", e4))
	w.mustLaunch(w.Launches("J.phone"))
	w.Settle()
	checkAll(w)
	w.Want("roster", w.Roles("B.phone", house), map[string]string{"J": "owner", "A": "reader", "B": "writer"})
	w.Want("J.phone", w.EpochLabel("J.phone", house), "E2")
}

func TestRO045(t *testing.T) {
	for _, variant := range []string{"discard", "do it here"} {
		t.Run(variant, func(t *testing.T) {
			w := rotHouse(t)
			stopsAt(w, "J.phone", "3:data", func() error { return w.Removes("J.phone", house, "B") })
			w.Syncs("J.mac")
			n := w.Pending("J.mac", house)
			w.Want("J.mac is shown", n.What, "remove a member")
			draft, _ := tandem.ParseB64u(n.Stmt)
			if variant == "do it here" {
				if err := w.Finishes("J.mac", house); !errors.Is(err, device.ErrCantFinish) {
					t.Fatalf("finishing a change whose statement isn't on the hub: %v", err)
				}
			}
			w.must(w.Discards("J.mac", house))
			w.Want("note removed", w.Pending("J.mac", house) == nil, true)
			w.Want("uploads destroyed", w.Messages("J", house, draft), 0)
			if variant == "do it here" {
				w.must(w.Removes("J.mac", house, "B"))
				w.Forget(house, "B")
				w.Want("a new statement", bytes.Equal(w.Epoch("J.mac", house), draft), false)
			}
			w.Settle()
			checkAll(w)
		})
	}
}

func TestRO046(t *testing.T) {
	w := rotHouse(t)
	stopsAt(w, "J.phone", "3:data", func() error { return w.Removes("J.phone", house, "B") })
	w.Syncs("J.mac")
	w.must(w.Discards("J.mac", house))
	w.mustLaunch(w.Launches("J.phone"))
	w.Settle()
	checkAll(w)
	w.Want("J.phone didn't announce", w.EpochLabel("J.phone", house), "E0")
	w.Want("journal gone", fileExists(filepath.Join(w.devs["J.phone"].dir, "journal.json")), false)
	w.Want("B a member", w.Roles("A.phone", house)["B"], "writer")
}

func TestRO047(t *testing.T) {
	t.Skip("needs: hub withholding")
}

func TestRO048(t *testing.T) {
	t.Skip("needs: putting a conflict into the current channel's _roster (§9.4) and hub destroy")
}

func TestRO049(t *testing.T) {
	t.Skip("needs: copying a collection (§11.8)")
}

func TestRO050(t *testing.T) {
	w := rotHouse(t)
	w.Offline("A.phone")
	w.Offline("B.phone")
	w.must(w.Sets("A.phone", house, "milk", "notes", "A's"))
	w.must(w.Sets("B.phone", house, "rug", "notes", "B's"))
	kill(w, "J.mac", house)
	w.Online("A.phone")
	w.Online("B.phone")
	w.Settle()
	w.CheckDead(house)
	if err := w.Sets("A.phone", house, "milk", "title", "after"); !errors.Is(err, device.ErrDead) {
		t.Fatalf("an edit to a dead collection: %v", err)
	}
	w.Want("A keeps its replica", w.Present("A.phone", house, "milk"), true)
	w.Want("B keeps its replica", w.Present("B.phone", house, "rug"), true)
	_, aSeen := w.Shows("B.phone", house, "milk", "notes")
	if v, _ := w.Shows("B.phone", house, "milk", "notes"); aSeen && v == "A's" {
		knownBug(t, "engine bug: a device that finds a collection dead still merges new data writes into it (B.phone merged A's edit published after the death; §9.4: nothing more is merged)")
	}
}

func TestRO051(t *testing.T) {
	w := rotHouse(t, "M.phone")
	w.Shares("J.mac", house, "M", "writer")
	w.Settle()
	stopsAt(w, "J.mac", "3:roster", func() error { return w.Removes("J.mac", house, "B") })
	w.Syncs("J.phone")
	draft, _ := tandem.ParseB64u(w.Pending("J.phone", house).Stmt)
	w.must(w.Discards("J.phone", house))
	w.must(w.Removes("J.phone", house, "B"))
	w.Forget(house, "B")
	w.mustLaunch(w.Launches("J.mac"))
	w.Settle()
	e4 := w.Epoch("M.phone", house)
	w.Want("the counters match", w.Statement("J.phone", house, e4).Body.KeyN, int64(1))
	w.Signs("M.phone", house, Forged{Item: "milk", Field: "title", Value: "naming E4'", Ref: draft, Unjudged: true})
	w.Settle()
	checkAll(w)
	w.WantShows(house, "milk", "title", "milk", "J.phone", "A.phone", "C.phone")
}

func TestRO052(t *testing.T) {
	// The listed write missing from channel E4 needs hub destroy.
	w := rotHouse(t, "A.mac")
	e3 := w.Epoch("J.phone", house)
	stopsAt(w, "J.phone", "2:note", func() error { return w.Removes("J.phone", house, "B") })
	w.must(w.Sets("A.mac", house, "milk", "notes", "late"))
	late := w.WriteOf(house, "milk", "notes", "late")
	w.Down("A.mac")
	w.mustLaunch(w.Launches("J.phone"))
	w.Forget(house, "B")
	w.Settle()
	w.Want("late's message may go", leakedTo(w, "J", house, e3, "milk", late), false)
	w.Want("E3's roster stays", w.RosterMessages("J", house, e3) > 0, true)
	w.Want("home keeps K(E3)", w.Keys("J", house)[string(e3)] != nil, true)
	w.mustLaunch(w.Launches("A.mac"))
	w.Settle()
	checkAll(w)
	w.WantShows(house, "milk", "notes", "late", "J.phone", "A.phone")
}

func TestRO053(t *testing.T) {
	// J.ipad mustn't see J.mac's note: J.mac stops after signing and
	// journalling, before the note (the G-08 case the engine fixed).
	w := rotHouse(t, "J.ipad", "D.phone")
	stopsAt(w, "J.mac", "2:journal", func() error { return w.Removes("J.mac", house, "B") })
	w.Shares("J.ipad", house, "D", "writer")
	err := w.Launches("J.mac")
	w.Settle()
	checkAll(w)
	w.Want("alive", w.Dead("A.phone", house), false)
	w.Want("roster", w.Roles("A.phone", house), map[string]string{"J": "owner", "A": "writer", "B": "writer", "C": "reader", "D": "writer"})
	if err == nil {
		knownBug(t, "engine bug: resuming drops an unexported change silently (Resume returns nil), so J isn't told his removal of B didn't happen")
	}
}
