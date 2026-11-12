package harness

// scenarios/records.md: records, writes and merge.
//
// The default setup is the file's: J owns Groceries (J.phone, J.mac) with
// A as writer (A.phone); A.phone created milk, eggs and bread. Epoch labels
// (E4, E5) are the scenario's; the harness starts at the genesis epoch, and
// only the order of epochs matters. Not checked here, for want of the
// feature: garbage collection (one message per record), the debounce,
// latency budgets, the 16-sibling presentation limit, holding limits.

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/device"
)

// grocer is records.md's default setup; extra is (user, role) pairs of
// further members, each with a phone.
func grocer(t *testing.T, extra ...string) *World {
	w := NewWorld(t, oracle)
	w.Dev("J.phone")
	w.Dev("J.mac")
	w.Dev("A.phone")
	w.Creates("J.mac", groceries)
	w.Shares("J.mac", groceries, "A", "writer")
	for i := 0; i+1 < len(extra); i += 2 {
		w.Dev(extra[i] + ".phone")
		w.Shares("J.mac", groceries, extra[i], extra[i+1])
	}
	w.Settle()
	for _, item := range []string{"milk", "eggs", "bread"} {
		w.must(w.Adds("A.phone", groceries, item))
	}
	w.Settle()
	return w
}

func offline(w *World, devs ...string) {
	for _, d := range devs {
		w.Offline(d)
	}
}

func online(w *World, devs ...string) {
	for _, d := range devs {
		w.Online(d)
	}
}

// stmtCount is how many statements a device holds for a collection.
func stmtCount(w *World, devName, coll string) int {
	_, s := w.Dev(devName).Roster(w.cid(coll))
	return len(s)
}

func TestRC001(t *testing.T) {
	w := grocer(t)
	for i := 1; i <= 20; i++ {
		w.must(w.Adds("J.phone", groceries, fmt.Sprintf("item%02d", i)))
	}
	w.Settle()
	w.Down("J.mac") // quit
	for i := 1; i <= 10; i++ {
		w.must(w.Completes("A.phone", groceries, fmt.Sprintf("item%02d", i)))
		w.must(w.Completes("J.phone", groceries, fmt.Sprintf("item%02d", i+10)))
		w.Syncs("A.phone", "J.phone")
	}
	w.must(w.Sets("J.phone", groceries, "Milk (J)", "title", "Milk"))
	w.must(w.Sets("A.phone", groceries, "Milk (A)", "title", "Milk"))
	w.Settle()
	n := w.Hub.Len()
	w.Syncs("J.phone", "A.phone")
	w.Want("appends after quiescence", w.Hub.Len(), n)
	w.Check(groceries)
	w.NoConflicts(groceries)
	for _, d := range []string{"J.phone", "A.phone"} {
		_, done := w.Order(d, groceries)
		w.Want(d+" ticked", len(done), 20)
		w.WantShows(groceries, "Milk (J)", "title", "Milk", d)
		w.WantShows(groceries, "Milk (A)", "title", "Milk", d)
	}
}

func TestRC002(t *testing.T) {
	w := grocer(t)
	offline(w, "J.mac", "A.phone")
	w.must(w.Sets("J.mac", groceries, "bread", "title", "Sourdough"))
	w.must(w.Sets("A.phone", groceries, "bread", "completedAt", "10:02"))
	online(w, "J.mac", "A.phone")
	w.Syncs("J.mac", "A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantShows(groceries, "bread", "title", "Sourdough", "J.mac", "A.phone")
	w.WantShows(groceries, "bread", "completedAt", "10:02", "J.mac", "A.phone")
	w.WantKept(groceries, "bread", "title", "Sourdough")
	w.WantKept(groceries, "bread", "completedAt", "10:02")
}

func TestRC003(t *testing.T) {
	// A.phone's clock being a second ahead is played by writing a moment
	// later in real time.
	w := grocer(t)
	offline(w, "J.phone", "A.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "completedAt", "10:00:00"))
	w.Tick()
	w.must(w.Sets("A.phone", groceries, "milk", "completedAt", "10:00:01"))
	online(w, "J.phone", "A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "completedAt", "10:00:00", "10:00:01")
	w.WantShows(groceries, "milk", "completedAt", "10:00:01", "J.phone", "A.phone")
	n := len(w.Writes(groceries, "milk", "completedAt"))
	w.Settle()
	w.Want("nobody resolves completedAt on its own", len(w.Writes(groceries, "milk", "completedAt")), n)

	w.must(w.Uncompletes("J.phone", groceries, "milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "completedAt", "")
}

func TestRC004(t *testing.T) {
	w := grocer(t)
	meta := tandem.NamedID("_meta")
	epoch, stmts := w.Epoch("J.mac", groceries), stmtCount(w, "J.mac", groceries)
	w.Offline("J.phone")
	w.must(w.SetsRecord("J.phone", groceries, meta, "name", "Shopping"))
	w.Tick()
	w.must(w.SetsRecord("J.mac", groceries, meta, "name", "Food"))
	w.must(w.Completes("A.phone", groceries, "milk"))
	w.must(w.Completes("A.phone", groceries, "eggs"))
	w.Online("J.phone")
	w.Settle()
	w.Check(groceries)
	w.Want("kept names", w.keptOf(groceries, meta, "name"), []string{"Food", "Shopping"})
	for _, d := range []string{"J.phone", "J.mac", "A.phone"} {
		w.Want(d+" shows", w.ListName(d, groceries), "Food")
		w.WantShows(groceries, "milk", "completedAt", Completed, d)
		w.WantShows(groceries, "eggs", "completedAt", Completed, d)
	}
	w.Want("no key change", string(w.Epoch("A.phone", groceries)), string(epoch))
	w.Want("no roster statement", stmtCount(w, "A.phone", groceries), stmts)

	// A rename from a modified client is rejected everywhere.
	w.Signs("A.phone", groceries, Forged{Record: meta, Field: "name", Value: "Aimee's", Context: tandem.VV{}})
	w.Settle()
	w.Check(groceries)
	w.Want("J.mac shows", w.ListName("J.mac", groceries), "Food")

	// A.phone offers no rename action: an honest client refuses to sign one.
	if err := w.SetsRecord("A.phone", groceries, meta, "name", "Aimee's"); err == nil {
		knownBug(t, "engine bug: device.Set signs and shows a writer's _meta write that every other device rejects (§8.3)")
	}
}

func TestRC005(t *testing.T) {
	w := grocer(t)
	w.must(w.Sets("A.phone", groceries, "eggs", "notes", "Large"))
	w.Settle()
	w.Down("J.mac")
	w.Offline("J.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Oat milk"))
	w.must(w.Sets("J.phone", groceries, "eggs", "notes", "Large, free range"))
	w.must(w.Adds("J.phone", groceries, "Butter"))
	w.must(w.Completes("A.phone", groceries, "milk"))
	w.must(w.Sets("A.phone", groceries, "eggs", "notes", "Large, organic"))
	w.must(w.Deletes("A.phone", groceries, "bread"))
	w.Tick()
	w.must(w.Sets("J.phone", groceries, "bread", "title", "Rye bread"))
	w.Down("A.phone") // quits
	w.Online("J.phone")
	w.Syncs("J.phone") // landing
	w.WantKept(groceries, "eggs", "notes", "Large, free range", "Large, organic")
	merged := "Large, free range\n———\nLarge, organic"
	w.must(w.Sets("J.phone", groceries, "eggs", "notes", merged)) // Liszto's merge (§17)
	w.mustLaunch(w.Launches("A.phone"))
	w.Syncs("A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantShows(groceries, "milk", "title", "Oat milk", "J.phone", "A.phone")
	w.WantShows(groceries, "milk", "completedAt", Completed, "J.phone", "A.phone")
	w.WantShows(groceries, "Butter", "title", "Butter", "A.phone")
	w.WantGone(groceries, "bread", "J.phone", "A.phone")
	bread := w.Answer(groceries).State[fmt.Sprintf("%x", ItemID(groceries, "bread"))]
	w.Want("bread deleted", bread.Deleted, true)
	w.WantKept(groceries, "bread", "title", "Rye bread") // kept in the deleted record's state
	w.WantKept(groceries, "eggs", "notes", merged)
	w.NoConflicts(groceries)
}

func TestRC006(t *testing.T) {
	w := grocer(t)
	offline(w, "J.phone", "A.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Milk 1L"))
	w.Tick()
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk 2L"))
	online(w, "J.phone", "A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk 1L", "Milk 2L")
	w.WantShows(groceries, "milk", "title", "Milk 2L", "J.phone", "A.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk")
}

func TestRC007(t *testing.T) {
	w := grocer(t)
	w.must(w.Tags("A.phone", groceries, "milk", "dairy", true))
	w.Settle()
	w.Offline("J.phone")
	w.must(w.Tags("A.phone", groceries, "milk", "dairy", false))
	w.must(w.Tags("J.phone", groceries, "milk", "dairy", false))
	w.must(w.Tags("J.phone", groceries, "milk", "dairy", true))
	w.must(w.Tags("A.phone", groceries, "milk", "organic", true))
	w.must(w.Tags("J.phone", groceries, "milk", "organic", true))
	w.Online("J.phone")
	w.Settle()
	w.Check(groceries)
	for _, d := range []string{"J.phone", "J.mac", "A.phone"} {
		w.Want(d+" tags", w.Elements(d, groceries, "milk", "tags"), []string{"dairy", "organic"})
	}
	w.must(w.Tags("A.phone", groceries, "milk", "organic", false))
	w.Settle()
	w.Check(groceries)
	for _, d := range []string{"J.phone", "J.mac", "A.phone"} {
		w.Want(d+" tags", w.Elements(d, groceries, "milk", "tags"), []string{"dairy"})
	}
}

// undo re-creates a deleted item as a new record with the fields the
// device showed (Liszto's undo once the delete is published, G-30).
func undo(w *World, devName, coll, item string, saved device.RecordView) {
	w.T.Helper()
	for _, f := range sortedKeys(saved.Fields) {
		w.must(w.Sets(devName, coll, item+" (undone)", f, saved.Fields[f]))
	}
	for _, f := range sortedKeys(saved.Sets) {
		for _, el := range saved.Sets[f] {
			w.must(w.Tags(devName, coll, item+" (undone)", el, true))
		}
	}
}

func TestRC008(t *testing.T) {
	// The delete is published at once (no debounce), so undo always makes
	// a new record (G-29).
	w := grocer(t)
	w.must(w.Sets("A.phone", groceries, "milk", "notes", "2 cartons"))
	w.must(w.Tags("A.phone", groceries, "milk", "dairy", true))
	w.must(w.Reorders("A.phone", groceries, "milk", "a5"))
	w.must(w.Sets("A.phone", groceries, "eggs", "notes", "Large"))
	w.Settle()
	rec := func(item string) device.RecordView {
		return w.Dev("J.mac").State(w.cid(groceries))[string(ItemID(groceries, item))]
	}
	milk := rec("milk")
	w.must(w.Deletes("J.mac", groceries, "milk"))
	undo(w, "J.mac", groceries, "milk", milk)
	w.Syncs("J.mac", "A.phone")
	got := w.Dev("A.phone").State(w.cid(groceries))[string(ItemID(groceries, "milk (undone)"))]
	w.Want("restored milk", got, milk)
	w.WantGone(groceries, "milk", "A.phone")

	w.Offline("A.phone")
	w.must(w.Sets("A.phone", groceries, "eggs", "notes", "Organic"))
	eggs := rec("eggs")
	w.must(w.Deletes("J.mac", groceries, "eggs"))
	w.Syncs("J.mac")
	undo(w, "J.mac", groceries, "eggs", eggs)
	w.Syncs("J.mac")
	w.Online("A.phone")
	w.Syncs("A.phone")
	w.Settle()
	w.Check(groceries)
	open, _ := w.Order("A.phone", groceries)
	w.Want("eggs shown", count(open, "eggs"), 1)
	w.WantShows(groceries, "eggs (undone)", "notes", "Large", "A.phone", "J.mac")
	w.WantGone(groceries, "eggs", "A.phone", "J.mac")
	w.WantKept(groceries, "eggs", "notes", "Organic") // in the tombstoned record, never shown
	w.Want("restored item added by", w.Author(groceries, "eggs (undone)", "title"), "J")
}

func TestRC009(t *testing.T) {
	w := grocer(t)
	w.Creates("J.mac", "House")
	w.Shares("J.mac", "House", "A", "writer")
	w.must(w.Adds("J.mac", groceries, "batteries"))
	w.Settle()
	w.Offline("A.phone")
	w.must(w.Moves("J.mac", groceries, "batteries", "House"))
	w.must(w.Sets("A.phone", groceries, "batteries", "title", "AA batteries"))
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.Check("House")
	w.WantShows("House", "batteries", "title", "batteries", "J.phone", "J.mac", "A.phone")
	w.WantGone(groceries, "batteries", "J.phone", "J.mac", "A.phone")
	w.WantKept(groceries, "batteries", "title", "AA batteries") // with J's delete, never shown
}

func TestRC010(t *testing.T) {
	w := grocer(t)
	w.must(w.Adds("A.phone", groceries, "party"))
	w.must(w.AddsUnder("A.phone", groceries, "cake", "party"))
	w.must(w.AddsUnder("A.phone", groceries, "balloons", "party"))
	w.Settle()
	w.Offline("A.phone")
	for _, item := range []string{"party", "cake", "balloons"} {
		w.must(w.Deletes("J.mac", groceries, item))
	}
	w.must(w.AddsUnder("A.phone", groceries, "candles", "party"))
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	for _, item := range []string{"party", "cake", "balloons"} {
		w.WantGone(groceries, item, "J.phone", "J.mac", "A.phone")
	}
	w.WantShows(groceries, "candles", "parentID", ParentRef(groceries, "party"), "J.phone", "J.mac", "A.phone")
	w.Want("writes to candles", len(w.Writes(groceries, "candles", "*")), 2) // nobody repairs it
}

func TestRC011(t *testing.T) {
	t.Skip("needs: the share extension's direct save (§17)")
}

func TestRC012(t *testing.T) {
	t.Skip("needs: device clock control (pinning three devices to one millisecond)")
}

func TestRC013(t *testing.T) {
	t.Skip("needs: device clock control")
}

func TestRC014(t *testing.T) {
	w := grocer(t, "B", "writer")
	e4 := w.Epoch("J.mac", groceries)
	w.Offline("J.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Oat milk"))
	j := w.WriteOf(groceries, "milk", "title", "Oat milk")
	w.must(w.Removes("J.mac", groceries, "B"))
	e5 := w.Epoch("J.mac", groceries)
	w.Syncs("A.phone")
	w.Tick()
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Whole milk"))
	w.Online("J.phone")
	w.Syncs("J.phone")
	w.Settle()
	w.Check(groceries)
	w.WantCut("B", groceries, e5)
	w.WantKept(groceries, "milk", "title", "Oat milk", "Whole milk")
	w.WantShows(groceries, "milk", "title", "Whole milk", "J.phone", "A.phone")
	var again *tandem.Write
	for _, x := range w.Writes(groceries, "milk", "title") {
		if string(x.Value) == "Oat milk" && !bytes.Equal(x.Ref, j.Ref) {
			again = x
		}
	}
	if again == nil {
		t.Fatal("J.phone didn't sign its write again")
	}
	w.Want("same dot", again.Dot, j.Dot)
	w.Want("empty context", len(again.Context), 0)
	w.Want("signed again into", string(w.Dev("J.mac").Epoch(w.cid(groceries))), string(e5))
	jid, _ := j.ID()
	leaked := false
	for _, ws := range w.Payloads("B", groceries, e4, "milk") { // B still holds K(E4)
		for _, x := range ws {
			id, _ := x.ID()
			leaked = leaked || bytes.Equal(id, jid)
		}
	}
	if leaked {
		knownBug(t, "engine bug: a returning device publishes its queued edits to the old epoch's channel before reading the announcement (§11.6), where removed B can read them")
	}
}

func TestRC015(t *testing.T) {
	// A.mac's clock a minute fast decides only which sibling is displayed;
	// without clock control the check is on what is kept.
	w := grocer(t, "B", "writer")
	w.Dev("A.mac")
	w.must(w.Completes("A.phone", groceries, "milk"))
	w.Settle()
	died, err := w.Crashes("J.mac", "2:note", func() error { return w.Removes("J.mac", groceries, "B") })
	w.must(err)
	w.Want("J.mac stopped after fixing inv(E5)", died, true)
	w.must(w.Sets("A.mac", groceries, "milk", "title", "Milk 1L")) // appended to E4
	x := w.WriteOf(groceries, "milk", "title", "Milk 1L")
	w.mustLaunch(w.Launches("J.mac")) // announces E5
	w.Forget(groceries, "B")
	w.Syncs("A.phone")
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Whole milk"))
	w.Syncs("A.mac")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk 1L", "Whole milk")
	w.WantShows(groceries, "milk", "completedAt", Completed, "J.mac", "A.phone", "A.mac")
	ids := map[string]bool{}
	for _, y := range w.Writes(groceries, "milk", "title") {
		if string(y.Value) == "Milk 1L" && y.Dot == x.Dot && !bytes.Equal(y.Ref, x.Ref) {
			id, _ := y.ID()
			ids[string(id)] = true
		}
	}
	w.Want("x signed again once", len(ids), 1)
}

func TestRC016(t *testing.T) {
	w := grocer(t)
	for _, item := range []string{"cake", "party", "candles"} {
		w.must(w.Adds("A.phone", groceries, item))
	}
	w.Settle()
	offline(w, "J.phone", "A.phone")
	w.must(w.Indents("J.phone", groceries, "cake", "party"))
	w.must(w.Indents("A.phone", groceries, "party", "cake"))
	w.must(w.Indents("A.phone", groceries, "candles", "cake"))
	online(w, "J.phone", "A.phone")
	w.Settle()
	w.Check(groceries)
	w.NoConflicts(groceries)
	w.WantShows(groceries, "cake", "parentID", ParentRef(groceries, "party"), "J.phone", "A.phone")
	w.WantShows(groceries, "party", "parentID", ParentRef(groceries, "cake"), "J.phone", "A.phone")
	w.WantShows(groceries, "candles", "parentID", ParentRef(groceries, "cake"), "J.phone", "A.phone")
	// A one-level tree from a cycle is G-33: no rule to check.
}

func TestRC017(t *testing.T) {
	w := grocer(t)
	w.Dev("J.ipad")
	w.Settle()
	w.must(w.Sets("J.mac", groceries, "milk", "priority", "high"))
	w.must(w.TagsField("J.mac", groceries, "milk", "assignees", "A", true))
	w.must(w.Sets("J.mac", groceries, "milk", "_pin", "1"))
	w.Settle()
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk 2L"))
	w.Syncs("A.phone")
	theirs := map[string]bool{}
	for _, x := range w.Writes(groceries, "milk", "*") {
		if x.Field == "priority" || x.Field == "assignees" || x.Field == "_pin" {
			id, _ := x.ID()
			theirs[string(id)+string(x.Sig)] = true
		}
	}
	a := w.WriteOf(groceries, "milk", "title", "Milk 2L")
	found := false
	for _, ws := range w.Payloads("A", groceries, w.Epoch("A.phone", groceries), "milk") {
		has, carried := false, 0
		for _, x := range ws {
			id, _ := x.ID()
			if aid, _ := a.ID(); bytes.Equal(id, aid) {
				has = true
			}
			if theirs[string(id)+string(x.Sig)] {
				carried++
			}
		}
		found = found || has && carried == len(theirs)
	}
	w.Want("A's message carries J.mac's writes byte for byte", found, true)
	w.Settle()
	w.WantShows(groceries, "milk", "priority", "high", "J.mac")
	w.WantShows(groceries, "milk", "title", "Milk 2L", "J.mac")

	offline(w, "J.mac", "J.ipad")
	w.must(w.Sets("J.mac", groceries, "milk", "priority", "low"))
	w.must(w.Sets("J.ipad", groceries, "milk", "priority", "urgent"))
	online(w, "J.mac", "J.ipad")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "priority", "low", "urgent")

	w.must(w.Rotates("J.phone", groceries))
	w.Settle()
	w.Check(groceries)
	_, inv := w.Dev("J.phone").Inventory(w.cid(groceries))
	listed := map[string]bool{}
	for _, id := range inv[string(ItemID(groceries, "milk"))] {
		listed[string(id)] = true
	}
	for _, x := range w.Writes(groceries, "milk", "*") {
		if x.Field == "_pin" || x.Field == "assignees" || x.Field == "priority" && string(x.Value) != "high" {
			id, _ := x.ID()
			w.Want(x.Field+" "+string(x.Value)+" in inv(E5)", listed[string(id)], true)
		}
	}
	w.Want("J.mac shows assignees", w.Elements("J.mac", groceries, "milk", "assignees"), []string{"A"})
	w.WantShows(groceries, "milk", "_pin", "1", "J.mac")
	w.WantKept(groceries, "milk", "priority", "low", "urgent")
	// Step 5 (a write with op 5) needs version handling (G-23).
}

func TestRC018(t *testing.T) {
	t.Skip("needs: hub withholding from one device")
}

func TestRC019(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		w := grocer(t, "B", "writer")
		w.must(w.Adds("B.phone", groceries, "wine"))
		w.Settle()
		w.must(w.Removes("J.mac", groceries, "B"))
		w.Settle()
		w.Check(groceries)
		w.WantShows(groceries, "wine", "title", "wine", "J.phone", "J.mac", "A.phone")
		w.Want("wine's author", w.Author(groceries, "wine", "title"), "B")
		_, inv := w.Dev("A.phone").Inventory(w.cid(groceries))
		id, _ := w.WriteOf(groceries, "wine", "title", "wine").ID()
		w.Want("wine in inv(E5)", slices.ContainsFunc(inv[string(ItemID(groceries, "wine"))], func(x []byte) bool { return bytes.Equal(x, id) }), true)
		w.WantCut("B", groceries, w.Epoch("J.mac", groceries))
	})
	t.Run("variant", func(t *testing.T) {
		w := grocer(t, "B", "writer")
		died, err := w.Crashes("J.mac", "2:note", func() error { return w.Removes("J.mac", groceries, "B") })
		w.must(err)
		w.Want("J.mac stopped after fixing inv(E5)", died, true)
		w.must(w.Adds("B.phone", groceries, "wine"))
		w.Syncs("A.phone")
		w.WantShows(groceries, "wine", "title", "wine", "A.phone")
		w.mustLaunch(w.Launches("J.mac"))
		w.Forget(groceries, "B")
		w.Settle()
		w.Check(groceries)
		w.WantGone(groceries, "wine", "J.phone", "J.mac", "A.phone")
		w.Want("B can't sign wine again", len(w.Writes(groceries, "wine", "title")), 1)
		w.WantCut("B", groceries, w.Epoch("J.mac", groceries))
	})
}

func TestRC020(t *testing.T) {
	// Step 2 (the hub replays M0) needs hub replay; the other steps run.
	w := grocer(t)
	w.must(w.Tags("A.phone", groceries, "milk", "dairy", true))
	w.must(w.Tags("J.phone", groceries, "milk", "organic", true))
	w.Settle()
	w.must(w.Tags("J.mac", groceries, "milk", "dairy", false))
	w.Settle()
	w.Want("dairy", w.Elements("A.phone", groceries, "milk", "tags"), []string{"organic"})
	offline(w, "J.phone", "A.phone")
	w.must(w.Tags("J.phone", groceries, "milk", "organic", false))
	w.must(w.Tags("A.phone", groceries, "milk", "organic", false))
	online(w, "J.phone", "A.phone")
	w.Settle()
	w.Want("organic removed", len(w.Elements("J.mac", groceries, "milk", "tags")), 0)
	w.must(w.Tags("A.phone", groceries, "milk", "organic", true))
	w.Settle()
	w.Want("organic back", w.Elements("J.mac", groceries, "milk", "tags"), []string{"organic"})
	w.must(w.Tags("J.mac", groceries, "milk", "gluten", false))
	w.Offline("A.phone")
	w.must(w.Tags("A.phone", groceries, "milk", "gluten", true))
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	for _, d := range []string{"J.phone", "J.mac", "A.phone"} {
		w.Want(d+" tags", w.Elements(d, groceries, "milk", "tags"), []string{"gluten", "organic"})
	}
}

func TestRC021(t *testing.T) {
	w := grocer(t)
	w.Creates("J.mac", "House")
	w.Shares("J.mac", "House", "A", "writer")
	w.Creates("A.phone", "Later")
	w.must(w.Adds("A.phone", groceries, "batteries"))
	w.Settle()
	offline(w, "J.mac", "A.phone")
	w.must(w.Moves("J.mac", groceries, "batteries", "House"))
	w.must(w.Moves("A.phone", groceries, "batteries", "Later"))
	online(w, "J.mac", "A.phone")
	w.Settle()
	for _, c := range []string{groceries, "House", "Later"} {
		w.Check(c)
	}
	w.WantGone(groceries, "batteries", "J.phone", "J.mac", "A.phone")
	w.Want("batteries deleted", w.Answer(groceries).State[fmt.Sprintf("%x", ItemID(groceries, "batteries"))].Deleted, true)
	w.Want("deletes signed", len(w.Writes(groceries, "batteries", "")), 2) // the oracle's state lists no delete writes
	w.WantShows("House", "batteries", "title", "batteries", "J.mac", "A.phone")
	w.WantShows("Later", "batteries", "title", "batteries", "A.phone")
	w.Want("House's copy by", w.Author("House", "batteries", "title"), "J")
	w.Want("Later's copy by", w.Author("Later", "batteries", "title"), "A")
}

func TestRC022(t *testing.T) {
	// Without hub withholding the honest devices see both of M's writes
	// together; steps 2–3's partial views aren't run.
	w := grocer(t, "M", "writer")
	a0 := w.WriteOf(groceries, "milk", "title", "milk")
	dot := w.Dot("M.phone", a0.Dot.Millis+10)
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Bread", Dot: dot, Context: VV(a0.Dot)})
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Cake", Dot: dot, Context: VV(a0.Dot)})
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Bread", "Cake")
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk")
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Jam", Dot: dot, Context: VV(a0.Dot)})
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk")
	w.WantShows(groceries, "milk", "title", "Milk", "J.phone", "J.mac", "A.phone", "M.phone")
}

func TestRC023(t *testing.T) {
	w := grocer(t, "M", "writer")
	a0 := w.WriteOf(groceries, "milk", "title", "milk")
	base := a0.Dot.Millis
	ctxv := VV(a0.Dot, w.Dot("M.phone", base+20))
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Bread", Dot: w.Dot("M.phone", base+10), Context: ctxv})
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Cake", Dot: w.Dot("M.phone", base+15), Context: ctxv})
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Bread", "Cake")
	w.WantShows(groceries, "milk", "title", "Cake", "J.phone", "J.mac", "A.phone")
	w.must(w.Sets("J.mac", groceries, "milk", "title", "Milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk")
}

func TestRC024(t *testing.T) {
	// Steps 1–3; step 4 needs 70 minutes to pass on J.phone's clock.
	w := grocer(t, "M", "writer")
	w.Offline("J.phone")
	a0 := w.WriteOf(groceries, "milk", "title", "milk")
	now := uint64(time.Now().UnixMilli())
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Mallory's milk",
		Dot: w.Dot("M.phone", now), Context: VV(a0.Dot, w.Dot("J.phone", now+3600_000))})
	w.Settle()
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Oat milk"))
	w.Online("J.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Mallory's milk")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Milk"))
	w.Tick()
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Milk!"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk", "Milk!")
	w.WantShows(groceries, "milk", "title", "Milk!", "J.phone", "J.mac", "A.phone")
}

// everyone is a vector naming each device at MAX.
func everyone(w *World, devs ...string) tandem.VV {
	var v []tandem.Dot
	for _, d := range devs {
		v = append(v, w.Dot(d, MaxMillis))
	}
	return VV(v...)
}

func TestRC025(t *testing.T) {
	w := grocer(t, "B", "writer", "C", "reader", "M", "writer")
	w.Dev("J.ipad")
	w.Dev("A.mac")
	w.Settle()
	all := everyone(w, "J.phone", "J.mac", "J.ipad", "A.phone", "A.mac", "B.phone", "C.phone", "M.phone")
	w.Signs("M.phone", groceries, Forged{Item: "eggs", Field: "notes", Value: "x", Context: all})
	w.Settle()
	w.must(w.Sets("J.phone", groceries, "eggs", "notes", "a"))
	w.must(w.Sets("J.phone", groceries, "eggs", "notes", "b"))
	w.must(w.Sets("A.phone", groceries, "eggs", "notes", "c"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "eggs", "notes", "x", "a", "b", "c")

	w.Dev("J.new") // restored with the backup key: a new device ID
	w.Syncs("J.new")
	w.must(w.Sets("J.new", groceries, "eggs", "notes", "d"))
	w.Settle()
	w.WantKept(groceries, "eggs", "notes", "d")
	// "Then j3 and later writes from listed devices pile up again": j3's
	// context covers reach(n1) and n1's doesn't cover reach(j3), so j3
	// replaces n1 (§9.2); the pile-up starts with the next write.
	w.must(w.Sets("J.phone", groceries, "eggs", "notes", "e"))
	w.Settle()
	w.WantKept(groceries, "eggs", "notes", "e")
	w.must(w.Sets("A.phone", groceries, "eggs", "notes", "g"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "eggs", "notes", "e", "g")

	w.must(w.Removes("J.mac", groceries, "M"))
	w.Settle()
	w.must(w.Sets("J.mac", groceries, "eggs", "notes", "f"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "eggs", "notes", "f")
}

func TestRC026(t *testing.T) {
	for _, run := range []string{"as written", "control"} {
		t.Run(run, func(t *testing.T) {
			w := grocer(t, "B", "writer", "M", "writer")
			w.Dev("A.mac")
			w.Settle()
			if run == "as written" {
				w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "M",
					Context: everyone(w, "J.phone", "J.mac", "A.phone", "A.mac", "B.phone", "M.phone")})
				w.Settle()
			}
			w.Syncs("A.mac")
			// One change removing M and B isn't an engine call: B first, then
			// M, stopped once inv(E5) is fixed.
			w.must(w.Removes("J.mac", groceries, "B"))
			w.Syncs("A.mac")
			died, err := w.Crashes("J.mac", "2:note", func() error { return w.Removes("J.mac", groceries, "M") })
			w.must(err)
			w.Want("J.mac stopped after fixing inv(E5)", died, true)
			w.must(w.Sets("A.mac", groceries, "milk", "title", "Milk 1L")) // into the old channel
			x := w.WriteOf(groceries, "milk", "title", "Milk 1L")
			w.mustLaunch(w.Launches("J.mac"))
			w.Forget(groceries, "M")
			w.Syncs("A.phone")
			w.must(w.Sets("A.phone", groceries, "milk", "title", "Whole milk"))
			w.Syncs("A.mac")
			w.Settle()
			w.Check(groceries)
			kept := w.Kept(groceries, "milk", "title")
			if run == "control" {
				w.Want("kept", kept, []string{"Milk 1L", "Whole milk"})
				return
			}
			// Decided 2026-11-12 (G-26, both fixes): x is not lost silently.
			// It is signed again past M's forged entry, or kept for the app.
			xid, _ := x.ID()
			unshared := slices.ContainsFunc(w.Unshared("A.mac", groceries), func(u *tandem.Write) bool {
				id, _ := u.ID()
				return bytes.Equal(id, xid)
			})
			if !slices.Contains(kept, "Milk 1L") && !unshared {
				t.Fatalf("x vanished: kept %q, not reported unshared", kept)
			}
		})
	}
}

func TestRC027(t *testing.T) {
	w := grocer(t, "M", "writer")
	all := everyone(w, "J.phone", "J.mac", "A.phone", "M.phone")
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Mallory was here",
		Dot: w.Dot("M.phone", MaxMillis), Context: all})
	w.Settle()
	w.must(w.Sets("J.mac", groceries, "milk", "title", "Milk"))
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk 2L"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Mallory was here", "Milk", "Milk 2L")
	w.WantShows(groceries, "milk", "title", "Mallory was here", "J.phone", "J.mac", "A.phone")
	w.must(w.Removes("J.mac", groceries, "M"))
	w.Settle()
	w.Check(groceries)
	if slices.Contains(w.Kept(groceries, "milk", "title"), "milk") {
		knownBug(t, "engine bug: after M's removal A.phone signs its replaced first title again: resign treats a superseding write as forged when its vectors merely contain M's inflated entry for A.phone, not when it relies on it (device/resign.go forged)")
	}
	w.WantKept(groceries, "milk", "title", "Mallory was here", "Milk", "Milk 2L")
	w.must(w.Sets("J.mac", groceries, "milk", "title", "Milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk")
	w.WantCut("M", groceries, w.Epoch("J.mac", groceries))
}

func TestRC028(t *testing.T) {
	w := grocer(t, "M", "writer")
	a0 := w.WriteOf(groceries, "milk", "title", "milk")
	w.Offline("J.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Oat milk"))
	j := w.WriteOf(groceries, "milk", "title", "Oat milk")
	want := []string{"Oat milk"}
	for i := 1; i <= 20; i++ {
		v := fmt.Sprintf("m%02d", i)
		want = append(want, v)
		w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: v,
			Dot: w.Dot("M.phone", j.Dot.Millis+uint64(i)), Context: VV(a0.Dot)})
	}
	w.Online("J.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", want...)
	w.WantShows(groceries, "milk", "title", "m20", "J.phone", "J.mac", "A.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk")
}

func TestRC029(t *testing.T) {
	w := grocer(t, "M", "writer")
	w.Creates("J.mac", "House")
	w.Shares("J.mac", "House", "M", "writer")
	w.Shares("J.mac", "House", "A", "writer")
	w.Settle()
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk 2L"))
	w.Settle()
	a := w.WriteOf(groceries, "milk", "title", "Milk 2L")
	cut := func(edit func(x *tandem.Write)) *tandem.Write {
		x := *a
		edit(&x)
		return &x
	}
	w.Publish("M.phone", groceries, nil, cut(func(x *tandem.Write) { x.Record = ItemID(groceries, "eggs") }))
	w.Publish("M.phone", groceries, nil, cut(func(x *tandem.Write) { x.Field = "notes" }))
	w.Publish("M.phone", "House", nil, cut(func(x *tandem.Write) { x.Scope = w.cid("House") }))
	w.Publish("M.phone", groceries, nil, cut(func(x *tandem.Write) { x.Ref = w.Epoch("J.mac", groceries) }))
	w.Signs("M.phone", groceries, Forged{Item: "bread", Field: "title", Value: "by Aimee, honest", ClaimAuthor: "A", Unjudged: true})
	w.Settle()
	w.Check(groceries)
	w.Check("House")
	w.WantShows(groceries, "milk", "title", "Milk 2L", "J.phone", "J.mac", "A.phone")
	w.WantShows(groceries, "bread", "title", "bread", "J.phone", "J.mac", "A.phone")
	w.WantShows(groceries, "eggs", "title", "eggs", "J.phone", "J.mac", "A.phone")
	w.WantKept(groceries, "eggs", "notes")
	w.Want("House shows nothing of Groceries", len(w.Dev("A.phone").State(w.cid("House"))), 1) // _meta only
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk 1L"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Milk 1L")
	// The replay of a's message needs hub replay.
}

func TestRC030(t *testing.T) {
	w := grocer(t, "M", "reader")
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Beer"})
	big := strings.Repeat("x", 1024)
	for i := 0; i < 100; i++ {
		w.Signs("M.phone", groceries, Forged{Item: fmt.Sprint("junk", i), Field: "title", Value: big})
	}
	madeUp := tandem.H([]byte("no such statement"))
	for i := 0; i < 100; i++ {
		w.Signs("M.phone", groceries, Forged{Item: fmt.Sprint("held", i), Field: "title", Value: big, Ref: madeUp})
	}
	n := w.Hub.Len()
	w.Settle()
	w.Check(groceries)
	w.Want("no honest device re-publishes them", w.Hub.Len(), n)
	w.WantShows(groceries, "milk", "title", "milk", "J.phone", "J.mac", "A.phone")
	w.Want("records shown", len(w.Dev("A.phone").State(w.cid(groceries))), 4) // _meta, milk, eggs, bread
	// Holding and evicting after 30 days needs the holding limit.
}

func TestRC031(t *testing.T) {
	t.Skip("needs: a second SDK and hand-built malformed payloads")
}

func TestRC032(t *testing.T) {
	w := grocer(t, "M", "writer")
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Nine", Ref: tandem.H([]byte("made up"))})
	w.Settle()
	w.Check(groceries)
	w.WantShows(groceries, "milk", "title", "milk", "J.mac", "A.phone")
	e6head := w.Head("M.phone", groceries)
	w.must(w.MakesRole("J.mac", groceries, "M", "reader"))
	w.Settle()
	cake := w.Sign("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Cake", Ref: e6head})
	w.Publish("M.phone", groceries, nil, cake) // re-encrypted into E7
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "title", Value: "Cake", Ref: w.Epoch("M.phone", groceries)})
	w.Settle()
	w.Check(groceries)
	w.WantShows(groceries, "milk", "title", "milk", "J.phone", "J.mac", "A.phone", "M.phone")
}

func TestRC034(t *testing.T) {
	w := grocer(t, "M", "writer")
	var items []string
	for i := 0; i < 40; i++ {
		items = append(items, fmt.Sprintf("item%02d", i))
		w.must(w.Adds("A.phone", groceries, items[i]))
	}
	w.Settle()
	for _, it := range items {
		w.must(w.Deletes("M.phone", groceries, it))
	}
	w.Settle()
	w.Check(groceries)
	w.must(w.Removes("J.mac", groceries, "M"))
	w.Settle()
	w.Check(groceries)
	for _, it := range items {
		w.WantGone(groceries, it, "J.phone", "J.mac", "A.phone")
	}
	w.WantCut("M", groceries, w.Epoch("J.mac", groceries))
}

func TestRC035(t *testing.T) {
	t.Skip("needs: device clock control and losing the HLC in a crash (G-20)")
}

func TestRC036(t *testing.T) {
	t.Skip("needs: a journalled move in Liszto (G-36)")
}

func TestRC037(t *testing.T) {
	t.Skip("needs: records split across messages (§10.6) and partial fetches")
}

func TestRC038(t *testing.T) {
	t.Skip("needs: a signal for an edit that lands on a deleted record (G-31)")
}

func TestRC039(t *testing.T) {
	t.Skip("needs: Liszto's automatic notes resolution (§17, G-38)")
}

func TestRC040(t *testing.T) {
	w := grocer(t)
	offline(w, "J.phone", "J.mac", "A.phone")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "Oat milk"))
	w.Tick()
	w.must(w.Sets("J.mac", groceries, "milk", "title", "Skim milk"))
	w.Tick()
	w.must(w.Sets("A.phone", groceries, "milk", "title", "Milk 2L"))
	online(w, "J.phone", "J.mac", "A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Oat milk", "Skim milk", "Milk 2L")
	w.WantShows(groceries, "milk", "title", "Milk 2L", "J.phone", "J.mac", "A.phone")
	w.must(w.Sets("J.mac", groceries, "milk", "title", "Oat milk"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "Oat milk")
}

func TestRC041(t *testing.T) {
	w := grocer(t, "M", "writer")
	w.Signs("J.mac", groceries, Forged{Record: tandem.NamedID("_meta"), Op: tandem.OpDelete})
	w.Signs("M.phone", groceries, Forged{Record: tandem.RosterID, Op: tandem.OpDelete})
	w.Settle()
	w.Check(groceries)
	for _, d := range []string{"J.phone", "A.phone"} {
		w.Want(d+" list name", w.ListName(d, groceries), groceries)
		w.Want(d+" roster", w.Roles(d, groceries), map[string]string{"J": "owner", "A": "writer", "M": "writer"})
	}
}

func TestRC042(t *testing.T) {
	// Step 1 (1,025 entries) can't be signed by the SDK; it needs a
	// hand-built payload.
	w := grocer(t, "M", "writer")
	var many []tandem.Dot
	for i := 0; i < tandem.MaxVector; i++ {
		var id [8]byte
		copy(id[:], tandem.H([]byte(fmt.Sprint("made-up device ", i))))
		many = append(many, tandem.Dot{Device: id, Millis: 1})
	}
	w.Signs("M.phone", groceries, Forged{Item: "milk", Field: "notes", Value: "M's", Context: VV(many...)})
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "notes", "M's")
	err := w.Sets("J.mac", groceries, "milk", "notes", "J's")
	if err != nil {
		knownBug(t, "engine bug: a write whose vectors would exceed 1,024 entries is refused ("+err.Error()+"); §9.2 says write with what fits and tell the app")
	}
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "notes", "J's", "M's")
	w.must(w.Removes("J.mac", groceries, "M"))
	w.Settle()
	w.must(w.Deletes("A.phone", groceries, "milk"))
	w.must(w.Sets("A.phone", groceries, "milk2", "notes", "settled"))
	w.must(w.Sets("A.phone", groceries, "milk2", "notes", "settled again"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk2", "notes", "settled again")
}

func TestRC043(t *testing.T) {
	t.Skip("needs: hand-built statements (a discarded draft's history, a reused key commitment)")
}
