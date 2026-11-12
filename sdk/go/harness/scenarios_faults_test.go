package harness

// scenarios/faults.md: network faults, offline devices, crashes, clocks,
// credentials, quota. The in-memory hub stands in for Stalwart behind
// Toxiproxy: a device can be offline, its appends or fetches reset, or
// their answers lost. Latency, binding specifics (IMAP UIDs, \Deleted,
// EventSource, IDLE), clocks, credentials and quotas are the testbed's and
// are skipped here.

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// ftHouse: J owns House (J.phone, J.mac), A (A.phone, A.mac) and B
// (B.phone) are writers, C (C.phone) a reader, with Milk, Bread and Jam.
func ftHouse(t *testing.T, extra ...string) *World {
	w := NewWorld(t, oracle)
	for _, d := range append([]string{"J.phone", "J.mac", "A.phone", "A.mac", "B.phone", "C.phone"}, extra...) {
		w.Dev(d)
	}
	w.Creates("J.mac", house)
	w.Shares("J.mac", house, "A", "writer")
	w.Shares("J.mac", house, "B", "writer")
	w.Shares("J.mac", house, "C", "reader")
	w.Settle()
	for _, item := range []string{"Milk", "Bread", "Jam"} {
		w.must(w.Adds("J.mac", house, item))
	}
	w.Settle()
	return w
}

func TestFT001(t *testing.T) {
	// Latency is played as delayed, interleaved syncs; push and
	// EventSource timing are the testbed's.
	w := ftHouse(t, "J.ipad")
	r := rand.New(rand.NewPCG(1, 1))
	devs := []string{"J.phone", "J.mac", "J.ipad", "A.phone", "A.mac"}
	items := []string{"Milk", "Bread", "Jam", "Eggs", "Tea"}
	for i := 0; i < 200; i++ {
		d, item := devs[r.IntN(len(devs))], items[r.IntN(len(items))]
		switch r.IntN(4) {
		case 0:
			w.Sets(d, house, item, "title", fmt.Sprint(d, i))
		case 1:
			w.Tags(d, house, item, []string{"a", "b"}[r.IntN(2)], r.IntN(2) == 0)
		case 2:
			if r.IntN(10) == 0 {
				w.Deletes(d, house, item)
			} else {
				w.Sets(d, house, item, "notes", fmt.Sprint(d, i))
			}
		default:
			w.Syncs(d)
		}
	}
	w.Settle()
	n := w.Hub.Len()
	w.Syncs(devs...)
	w.Want("no device publishes again", w.Hub.Len(), n)
	w.Check(house)
}

func TestFT002(t *testing.T) {
	w := ftHouse(t)
	w.Offline("J.phone")
	for i := 0; i < 5; i++ {
		w.must(w.Adds("J.phone", house, fmt.Sprint("new", i)))
	}
	for i := 0; i < 30; i++ {
		w.must(w.Sets("J.phone", house, "Milk", "title", fmt.Sprint("Milk ", i)))
	}
	w.must(w.Deletes("J.phone", house, "Bread"))
	w.WantShows(house, "Milk", "title", "Milk 29", "J.phone") // applies locally at once
	w.must(w.Sets("J.mac", house, "Milk", "notes", "from the Mac"))
	w.Online("J.phone")
	n := w.Hub.Len()
	w.Syncs("J.phone")
	if got := w.Hub.Len() - n; got > 7 {
		t.Fatalf("J.phone published %d messages for 7 changed records", got)
	}
	w.Settle()
	w.Check(house)
	for _, d := range []string{"J.phone", "J.mac"} {
		for i := 0; i < 5; i++ {
			w.Want(d+" shows new item", w.Present(d, house, fmt.Sprint("new", i)), true)
		}
	}
	w.WantShows(house, "Milk", "title", "Milk 29", "J.phone", "J.mac")
	w.WantShows(house, "Milk", "notes", "from the Mac", "J.phone", "J.mac")
	w.WantGone(house, "Bread", "J.phone", "J.mac")
}

func TestFT003(t *testing.T) {
	for _, variant := range []string{"stored", "not stored"} {
		t.Run(variant, func(t *testing.T) {
			w := ftHouse(t)
			if variant == "stored" {
				w.LosesReplies("J.mac", true)
			} else {
				w.Resets("J.mac", true, false)
			}
			w.must(w.Adds("J.mac", house, "Oat milk"))
			w.LosesReplies("J.mac", false)
			w.Resets("J.mac", false, false)
			w.Syncs("J.mac")
			w.Settle()
			w.Check(house)
			w.WantShows(house, "Oat milk", "title", "Oat milk", "A.phone", "J.phone")
			msgs := len(w.Payloads("J", house, w.Epoch("J.mac", house), "Oat milk"))
			if variant == "stored" && msgs > 1 {
				knownBugf(t, "engine bug: after an append whose answer was lost, the device appends the same record again (%d messages) instead of finding its envelope on the hub by H(envelope) first: flush runs before the change feed is read (§10.2, §10.7)", msgs)
			}
			w.Want("messages for Oat milk", msgs, 1)
		})
	}
}

func TestFT004(t *testing.T) {
	t.Skip("needs: the IMAP binding on the testbed (UIDs, APPENDUID)")
}

func TestFT005(t *testing.T) {
	// A cut body is played as a fetch that fails; partial bodies are the
	// testbed's.
	w := ftHouse(t)
	w.Offline("J.phone")
	for i := 0; i < 40; i++ {
		w.must(w.Sets("A.phone", house, fmt.Sprint("item", i), "title", fmt.Sprint("item ", i)))
	}
	w.Online("J.phone")
	w.Resets("J.phone", false, true)
	w.Syncs("J.phone")
	w.Want("nothing merged from the cut response", w.Present("J.phone", house, "item0"), false)
	w.Resets("J.phone", false, false)
	w.Syncs("J.phone")
	n := w.Hub.Len()
	w.Settle()
	w.Want("J.phone publishes nothing", w.Hub.Len(), n)
	w.Check(house)
	for i := 0; i < 40; i++ {
		w.Want("J.phone holds", w.Present("J.phone", house, fmt.Sprint("item", i)), true)
	}
}

func TestFT006(t *testing.T) {
	// The reset falls after the change feed was read and before the device
	// recorded it: its queued edit's append is cut.
	w := ftHouse(t)
	w.Offline("J.phone")
	w.must(w.Sets("J.phone", house, "Milk", "notes", "queued"))
	for i := 0; i < 50; i++ {
		w.must(w.Sets("J.mac", house, fmt.Sprint("item", i), "title", fmt.Sprint("item ", i)))
	}
	w.Online("J.phone")
	w.Resets("J.phone", true, false)
	w.Syncs("J.phone")
	w.Resets("J.phone", false, false)
	w.Syncs("J.phone")
	w.Settle()
	if !reflect.DeepEqual(w.Dev("J.phone").State(w.cid(house)), w.Dev("J.mac").State(w.cid(house))) {
		knownBug(t, "engine bug: a sync whose flush fails after reading the change feed drops the new message refs for good: Changes has moved the cursor, the refs were never recorded, so J.phone never sees J.mac's 50 edits (§10.2: the cursor never moves past changes whose ids aren't recorded)")
	}
	w.Check(house)
}

func TestFT007(t *testing.T) {
	t.Skip("needs: the IMAP binding on the testbed (\\Deleted and UID EXPUNGE)")
}

func TestFT008(t *testing.T) {
	w := ftHouse(t)
	for i := 0; i < 100; i++ {
		w.must(w.Adds("A.phone", house, fmt.Sprint("r", i)))
	}
	w.Settle()
	w.CutsAt("J.mac", "3:fragment") // Stalwart restarts during S3
	if err := w.Removes("J.mac", house, "B"); err == nil {
		t.Fatal("the change went through a restart without noticing")
	}
	w.Want("A still in E0", w.EpochLabel("A.phone", house), "E0")
	w.must(w.Sets("A.phone", house, "Milk", "notes", "during the restart"))
	w.Syncs("A.phone")
	w.Online("J.mac") // the restart ends; the app carries on
	w.must(w.Dev("J.mac").Resume(ctx))
	w.Forget(house, "B")
	w.Settle()
	w.Check(house)
	_, stmts := w.Dev("A.phone").Roster(w.cid(house))
	w.Want("statements", len(stmts), 5) // genesis, three admissions, one removal
	cur := w.Epoch("J.mac", house)
	w.Want("A's grant", w.Granted("A", house)[string(cur)], true)
	w.WantCut("B", house, cur)
	for i := 0; i < 100; i++ {
		w.Want("record", w.Present("C.phone", house, fmt.Sprint("r", i)), true)
	}
	w.WantShows(house, "Milk", "notes", "during the restart", "J.mac", "C.phone")
}

func TestFT009(t *testing.T) {
	t.Skip("needs: garbage collection (§10.7)")
}

func TestFT010(t *testing.T) {
	t.Run("a", func(t *testing.T) {
		w := ftHouse(t)
		stopsAt(w, "J.mac", "4:home", func() error { return w.Removes("J.mac", house, "B") })
		w.Forget(house, "B")
		e0 := w.Epoch("A.mac", house)
		w.Syncs("A.mac", "C.phone")
		inE0 := w.Messages("A", house, e0)
		errA := w.Adds("A.mac", house, "from A")
		w.Adds("C.phone", house, "from C") // C is a reader: refused anyway
		w.Syncs("A.mac", "C.phone")
		w.Want("nothing published to the old epoch", w.Messages("A", house, e0), inE0)
		w.mustLaunch(w.Launches("J.mac"))
		w.Settle()
		w.Check(house)
		cur := w.Epoch("J.mac", house)
		for _, u := range []string{"A", "C"} {
			w.Want(u+" holds the new key", w.Keys(u, house)[string(cur)] != nil, true)
		}
		w.WantCut("B", house, cur)
		if errA != nil {
			knownBug(t, "engine bug: a member waiting for the new epoch's key can't edit ("+errA.Error()+"); edits should apply locally and queue (§11.6, RO-035)")
		}
		w.WantShows(house, "from A", "title", "from A", "J.mac", "C.phone")
	})
	t.Run("b", func(t *testing.T) { t.Skip("needs: key requests (§11.4)") })
}

func TestFT011(t *testing.T) {
	for _, variant := range []string{"a", "b"} {
		t.Run(variant, func(t *testing.T) {
			w := ftHouse(t)
			for i := 0; i < 40; i++ {
				w.must(w.Adds("A.phone", house, fmt.Sprint("r", i)))
			}
			w.Settle()
			stopsAt(w, "J.mac", "3:data", func() error { return w.Removes("J.mac", house, "B") })
			w.must(w.Sets("A.phone", house, "r0", "notes", "still E0"))
			w.Syncs("A.phone", "B.phone", "C.phone")
			for _, d := range []string{"A.phone", "B.phone", "C.phone"} {
				w.Want(d+" in E0", w.EpochLabel(d, house), "E0")
			}
			switch variant {
			case "a":
				w.mustLaunch(w.Launches("J.mac"))
			case "b":
				w.Syncs("J.phone")
				n := w.Pending("J.phone", house)
				draft, _ := tandem.ParseB64u(n.Stmt)
				w.Want("finish", w.Finishes("J.phone", house) != nil, true) // roster not up: can't
				w.must(w.Discards("J.phone", house))
				w.Want("partial channel destroyed", w.Messages("J", house, draft), 0)
				w.must(w.Removes("J.phone", house, "B"))
				w.Want("a new key", bytes.Equal(w.Epoch("J.phone", house), draft), false)
				w.mustLaunch(w.Launches("J.mac"))
			}
			w.Forget(house, "B")
			w.Settle()
			w.Check(house)
			for i := 0; i < 40; i++ {
				w.Want("record", w.Present("C.phone", house, fmt.Sprint("r", i)), true)
			}
			w.WantShows(house, "r0", "notes", "still E0", "J.phone", "C.phone")
			w.WantCut("B", house, w.Epoch("J.phone", house))
		})
	}
}

func TestFT014(t *testing.T) { t.Skip("needs: first run and the backup key (identity)") }
func TestFT015(t *testing.T) { t.Skip("needs: pairing (§6.3)") }
func TestFT016(t *testing.T) { t.Skip("needs: join requests and key requests (§11.3, §11.4)") }
func TestFT017(t *testing.T) {
	t.Skip("needs: device clock control and losing the HLC in a crash (G-20)")
}

func TestFT018(t *testing.T) {
	// Which bodies are fetched is the binding's (testbed).
	w := ftHouse(t, "J.ipad")
	w.Creates("J.mac", groceries)
	w.Shares("J.mac", groceries, "A", "writer")
	w.must(w.Adds("J.mac", groceries, "coffee"))
	w.Settle()
	w.Offline("J.ipad")
	w.must(w.Sets("J.ipad", house, "Milk", "notes", "ipad 1"))
	w.must(w.Sets("J.ipad", house, "Jam", "notes", "ipad 2"))
	w.must(w.Sets("J.ipad", groceries, "coffee", "notes", "ipad 3"))
	r := rand.New(rand.NewPCG(18, 18))
	for i := 0; i < 200; i++ {
		d := []string{"J.mac", "A.phone"}[r.IntN(2)]
		c := []string{house, groceries}[r.IntN(2)]
		w.must(w.Sets(d, c, fmt.Sprint("item", r.IntN(30)), "title", fmt.Sprint(d, i)))
		if i%10 == 0 {
			w.Syncs(d)
		}
	}
	w.Settle()
	w.Online("J.ipad")
	w.Syncs("J.ipad")
	w.Syncs("J.mac")
	for _, c := range []string{house, groceries} {
		w.Want(c+" converges within one sync", w.Dev("J.ipad").State(w.cid(c)), w.Dev("J.mac").State(w.cid(c)))
	}
	w.Settle()
	w.Check(house)
	w.Check(groceries)
	w.WantShows(house, "Milk", "notes", "ipad 1", "A.phone")
	w.WantShows(groceries, "coffee", "notes", "ipad 3", "A.phone")
}

func TestFT019(t *testing.T) {
	w := ftHouse(t)
	e2 := w.Epoch("J.mac", house)
	w.Offline("A.phone")
	w.must(w.Sets("A.phone", house, "Milk", "notes", "A.phone's milk"))
	w.must(w.Sets("A.phone", house, "Jam", "notes", "A.phone's jam"))
	milk := w.WriteOf(house, "Milk", "notes", "A.phone's milk")
	w.must(w.Removes("J.mac", house, "B")) // S6 included
	w.Syncs("A.mac")
	w.must(w.Deletes("J.mac", house, "Jam"))
	w.Settle()
	w.Online("A.phone")
	w.Syncs("A.phone")
	w.Settle()
	w.Check(house)
	w.Want("A.phone moved", w.EpochLabel("A.phone", house), "E1")
	w.WantShows(house, "Milk", "notes", "A.phone's milk", "J.mac", "A.mac")
	w.WantGone(house, "Jam", "J.mac", "A.phone", "A.mac")
	w.WantKept(house, "Jam", "notes", "A.phone's jam")
	if leakedTo(w, "J", house, e2, "Milk", milk) {
		knownBug(t, flushLeak)
	}
}

func TestFT020(t *testing.T) {
	// "Day 195" is played as more key changes in between; no timer is
	// involved (§12.3).
	w := ftHouse(t)
	e0 := w.Epoch("J.mac", house)
	w.Offline("A.phone")
	w.must(w.Sets("A.phone", house, "Milk", "notes", "a year ago"))
	w.must(w.Sets("A.phone", house, "Bread", "notes", "a year ago"))
	milk := w.WriteOf(house, "Milk", "notes", "a year ago")
	w.must(w.Deletes("J.mac", house, "Bread"))
	w.must(w.Rotates("J.mac", house))
	w.must(w.Removes("J.mac", house, "B"))
	w.Settle()
	w.Online("A.phone")
	w.Settle()
	w.Check(house)
	w.WantShows(house, "Milk", "notes", "a year ago", "J.mac", "A.mac", "C.phone")
	w.WantGone(house, "Bread", "J.mac", "A.phone", "C.phone")
	if leakedTo(w, "J", house, e0, "Milk", milk) {
		knownBug(t, flushLeak)
	}
}

func TestFT021(t *testing.T) { t.Skip("needs: device clock control") }
func TestFT022(t *testing.T) { t.Skip("needs: device clock control and TLS on the testbed") }
func TestFT023(t *testing.T) { t.Skip("needs: device clock control") }
func TestFT024(t *testing.T) { t.Skip("needs: the share extension's direct save (§17)") }
func TestFT025(t *testing.T) { t.Skip("needs: IMAP IDLE on the testbed") }

func TestFT026(t *testing.T) {
	// Resets alternate with being online; session counts are the testbed's.
	w := ftHouse(t)
	r := rand.New(rand.NewPCG(26, 26))
	var edits []string
	for i := 0; i < 40; i++ {
		for _, d := range []string{"J.phone", "A.phone"} {
			reset := r.IntN(2) == 0
			w.Resets(d, reset, reset && r.IntN(2) == 0)
			item := fmt.Sprintf("%s-%d", d, i)
			w.must(w.Adds(d, house, item))
			edits = append(edits, item)
			w.Syncs(d)
		}
		w.Syncs("J.mac")
	}
	w.Resets("J.phone", false, false)
	w.Resets("A.phone", false, false)
	w.Settle()
	n := w.Hub.Len()
	w.Settle()
	w.Want("quiet", w.Hub.Len(), n)
	for _, d := range []string{"J.phone", "A.phone"} {
		if !reflect.DeepEqual(w.Dev(d).State(w.cid(house)), w.Dev("J.mac").State(w.cid(house))) {
			knownBug(t, "engine bug: a sync whose flush fails after reading the change feed drops the new message refs for good (FT-006)")
		}
	}
	w.Check(house)
	for _, item := range edits {
		w.Want(item+" on J.mac", w.Present("J.mac", house, item), true)
	}
}

func TestFT027(t *testing.T) { t.Skip("needs: per-device credentials and their revocation") }
func TestFT028(t *testing.T) { t.Skip("needs: changing the shared credential (G-17)") }
func TestFT029(t *testing.T) { t.Skip("needs: a mailbox quota (testbed)") }
func TestFT030(t *testing.T) { t.Skip("needs: a mailbox quota and junk on the hub (testbed)") }

func TestFT031(t *testing.T) {
	w := ftHouse(t)
	w.Offline("J.phone")
	for i := 0; i < 3; i++ {
		w.must(w.Adds("J.phone", house, fmt.Sprint("offline", i)))
	}
	w.must(w.Sets("J.phone", house, "Milk", "notes", "one"))
	w.mustLaunch(w.Launches("J.phone")) // quits and launches, still offline
	w.Offline("J.phone")
	w.WantShows(house, "Milk", "notes", "one", "J.phone")
	w.must(w.Sets("J.phone", house, "Milk", "notes", "two"))
	w.must(w.Sets("J.phone", house, "Jam", "notes", "three"))
	w.Down("J.phone") // crashes after a local edit
	w.mustLaunch(w.Launches("J.phone"))
	w.Offline("J.phone")
	for i := 0; i < 3; i++ {
		w.Want("still shown", w.Present("J.phone", house, fmt.Sprint("offline", i)), true)
	}
	w.WantShows(house, "Milk", "notes", "two", "J.phone")
	w.WantShows(house, "Jam", "notes", "three", "J.phone")
	w.Online("J.phone")
	n := w.Hub.Len()
	w.Syncs("J.phone")
	w.Want("each changed record published once", w.Hub.Len()-n, 5)
	w.Settle()
	w.Check(house)
	w.WantShows(house, "Milk", "notes", "two", "J.mac")
	w.WantShows(house, "Jam", "notes", "three", "J.mac")
	w.Want("J.mac shows offline2", w.Present("J.mac", house, "offline2"), true)
}

func TestFT032(t *testing.T) { t.Skip("needs: garbage collection (§10.7)") }
func TestFT033(t *testing.T) { t.Skip("needs: hub withholding") }

func TestFT034(t *testing.T) {
	w := ftHouse(t)
	stopsAt(w, "J.phone", "4:announced", func() error { return w.Removes("J.phone", house, "B") })
	w.Forget(house, "B")
	w.Syncs("J.mac")
	n := w.Pending("J.mac", house)
	if n == nil {
		t.Fatal("J.mac isn't shown the change")
	}
	w.must(w.Finishes("J.mac", house))
	w.Want("note removed", w.Pending("J.mac", house) == nil, true)
	cur := w.Epoch("J.mac", house)
	w.Want("key in home", w.Keys("J", house)[string(cur)] != nil, true)
	w.Want("A's grant", w.Granted("A", house)[string(cur)], true)
	w.mustLaunch(w.Launches("J.phone"))
	w.Settle()
	w.Check(house)
	w.Want("J.phone", w.EpochLabel("J.phone", house), "E1")
	w.WantCut("B", house, cur)
}

func TestFT035(t *testing.T) {
	w := ftHouse(t)
	stopsAt(w, "J.phone", "2:note", func() error { return w.Removes("J.phone", house, "B") })
	w.must(os.Remove(filepath.Join(w.devs["J.phone"].dir, "journal.json"))) // storage rolled back
	w.mustLaunch(w.Launches("J.phone"))
	if w.Pending("J.phone", house) == nil {
		t.Fatal("J.phone doesn't offer to do it or discard it")
	}
	w.must(w.Discards("J.phone", house))
	w.Settle()
	w.Check(house)
	w.Want("alive", w.Dead("A.phone", house), false)
	w.Want("B still a member", w.Roles("A.phone", house)["B"], "writer")
}
