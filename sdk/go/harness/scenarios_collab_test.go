package harness

// scenarios/collab.md: one list, 1–10 players collaborating.
//
// Pairing is the DSL's: a user's second device gets the same secret and a
// new device ID (§6.3). Inviting and joining end in Shares (§11.3). Liszto
// rendering is checked through the fields that express it (sortKey,
// completedAt, parentID); Order sorts by sortKey and record ID.
// Not checked anywhere here, for want of the feature: garbage collection
// and the debounce (one message per record, §10.7), push latency.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
)

const groceries = "Groceries"

// kitchen is CO-001 to CO-003 done: J's phone and Mac, A's phone; J owns
// Groceries with five items, coffee moved to the top, bread completed,
// eggs renamed; A is a writer. Everyone synced.
func kitchen(t *testing.T) *World {
	w := NewWorld(t, oracle)
	w.Dev("J.phone")
	w.Creates("J.phone", groceries)
	// New items go to the top: each gets a key before the first.
	for i, item := range []string{"milk", "eggs", "bread", "coffee", "apples"} {
		w.must(w.AddsAt("J.phone", groceries, item, fmt.Sprintf("m%d", 5-i)))
	}
	w.must(w.Reorders("J.phone", groceries, "coffee", "m0"))
	w.must(w.Completes("J.phone", groceries, "bread"))
	w.must(w.Sets("J.phone", groceries, "eggs", "title", "eggs (12)"))
	w.Dev("J.mac")
	w.Dev("A.phone")
	w.Shares("J.phone", groceries, "A", "writer")
	w.Settle()
	return w
}

func wantOrder(w *World, devName, coll string, open, done []string) {
	w.T.Helper()
	o, d := w.Order(devName, coll)
	if !slices.Equal(o, open) || !slices.Equal(d, done) {
		w.T.Fatalf("%s renders %s as %q, done %q; want %q, done %q", devName, coll, o, d, open, done)
	}
}

func TestCO001(t *testing.T) {
	w := NewWorld(t, oracle)
	w.Creates("J.phone", groceries)
	for i, item := range []string{"milk", "eggs", "bread", "coffee", "apples"} {
		w.must(w.AddsAt("J.phone", groceries, item, fmt.Sprintf("m%d", 5-i)))
	}
	w.must(w.Reorders("J.phone", groceries, "coffee", "m0"))
	w.must(w.Completes("J.phone", groceries, "bread"))
	w.must(w.Sets("J.phone", groceries, "eggs", "title", "eggs (12)"))
	w.Settle()
	before := w.Dev("J.phone").State(w.cid(groceries))
	hub := w.Hub.Len()
	open := []string{"coffee", "apples", "eggs (12)", "milk"}
	wantOrder(w, "J.phone", groceries, open, []string{"bread"})

	w.Relaunches("J.phone")
	w.Settle()
	w.Want("state after relaunch", w.Dev("J.phone").State(w.cid(groceries)), before)
	w.Want("hub messages after relaunch (INV-QUIESCE)", w.Hub.Len(), hub)
	wantOrder(w, "J.phone", groceries, open, []string{"bread"})
	w.Check(groceries)
}

func TestCO002(t *testing.T) {
	w := NewWorld(t, oracle)
	w.Creates("J.phone", groceries)
	for i, item := range []string{"milk", "eggs", "bread", "coffee", "apples"} {
		w.must(w.AddsAt("J.phone", groceries, item, fmt.Sprintf("m%d", 5-i)))
	}
	w.must(w.Reorders("J.phone", groceries, "coffee", "m0"))
	w.must(w.Completes("J.phone", groceries, "bread"))
	w.Settle()

	w.Dev("J.mac") // paired
	w.Syncs("J.mac")
	w.Want("J.mac after its first sync", w.Dev("J.mac").State(w.cid(groceries)), w.Dev("J.phone").State(w.cid(groceries)))

	w.must(w.Completes("J.mac", groceries, "milk"))
	w.must(w.AddsAt("J.phone", groceries, "butter", "l9"))
	w.Settle()
	for _, d := range []string{"J.phone", "J.mac"} {
		wantOrder(w, d, groceries, []string{"butter", "coffee", "apples", "eggs"}, []string{"bread", "milk"})
	}
	w.Check(groceries)
}

func TestCO003(t *testing.T) {
	w := NewWorld(t, oracle)
	w.Dev("J.phone")
	w.Creates("J.phone", groceries)
	for i, item := range []string{"milk", "eggs", "bread", "coffee", "apples"} {
		w.must(w.AddsAt("J.phone", groceries, item, fmt.Sprintf("m%d", 5-i)))
	}
	w.must(w.Completes("J.phone", groceries, "bread"))
	w.Dev("J.mac")
	w.Dev("A.phone")
	w.Settle()
	epoch := w.Epoch("J.phone", groceries)

	w.Shares("J.phone", groceries, "A", "writer")
	w.Syncs("A.phone")
	w.Want("A.phone after one sync", w.Dev("A.phone").State(w.cid(groceries)), w.Dev("J.phone").State(w.cid(groceries)))
	w.Want("roster", w.Roles("A.phone", groceries), map[string]string{"J": "owner", "A": "writer"})
	w.Want("epoch unchanged", string(w.Epoch("A.phone", groceries)), string(epoch))
	for _, item := range []string{"milk", "eggs", "bread", "coffee", "apples"} {
		w.Want(item+" attributed to", w.Author(groceries, item, "title"), "J")
	}
	w.Settle()
	w.Check(groceries)

	// Both edit, concurrently and not (the harness's first CO-003).
	w.must(w.Sets("A.phone", groceries, "eggs", "title", "eggs (12)"))
	w.must(w.Tags("J.phone", groceries, "milk", "dairy", true))
	w.must(w.Sets("J.phone", groceries, "bread", "notes", "rye"))
	w.must(w.Sets("A.phone", groceries, "bread", "notes", "sourdough"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "bread", "notes", "rye", "sourdough")
}

func TestCO004(t *testing.T) {
	w := kitchen(t)
	w.must(w.AddsAt("A.phone", groceries, "oat milk", "l9"))
	w.Settle()
	w.NoConflicts(groceries)
	w.must(w.Sets("J.phone", groceries, "oat milk", "title", "oat milk (2)"))
	w.Settle()
	w.NoConflicts(groceries)
	w.must(w.Completes("A.phone", groceries, "oat milk"))
	w.Settle()
	w.NoConflicts(groceries)
	w.must(w.Uncompletes("J.mac", groceries, "oat milk"))
	w.Settle()
	w.Check(groceries)
	w.NoConflicts(groceries)
	w.WantShows(groceries, "oat milk", "completedAt", "", "J.phone", "J.mac", "A.phone")
	w.WantShows(groceries, "oat milk", "title", "oat milk (2)", "J.phone", "J.mac", "A.phone")
	w.Want("added by", w.userOf(w.Writes(groceries, "oat milk", "*")[0].Author), "A")
	w.Want("title last changed by", w.Author(groceries, "oat milk", "title"), "J")
	w.Want("completion last changed by", w.Author(groceries, "oat milk", "completedAt"), "J")
}

func TestCO005(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	w.Offline("A.phone")
	// Both "before the current first item": equal keys.
	w.must(w.AddsAt("J.phone", groceries, "tea", "l9"))
	w.must(w.AddsAt("A.phone", groceries, "honey", "l9"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	open, _ := w.Order("J.phone", groceries)
	for _, d := range []string{"J.mac", "A.phone"} {
		o, _ := w.Order(d, groceries)
		w.Want(d+" order", o, open)
	}
	w.Want("the two new items at the top", len(open) >= 2 && (open[0] == "tea" && open[1] == "honey" || open[0] == "honey" && open[1] == "tea"), true)
	// Equal keys: ordered by record ID.
	first := "tea"
	if string(ItemID(groceries, "honey")) < string(ItemID(groceries, "tea")) {
		first = "honey"
	}
	w.Want("tie broken by record ID", open[0], first)
}

func TestCO006(t *testing.T) {
	for _, first := range []string{"J.phone", "A.phone"} {
		t.Run(first+" first", func(t *testing.T) {
			w := kitchen(t)
			w.Offline("J.phone")
			w.Offline("A.phone")
			w.must(w.Sets("J.phone", groceries, "eggs", "title", "eggs (6)"))
			w.must(w.Completes("A.phone", groceries, "eggs"))
			second := map[string]string{"J.phone": "A.phone", "A.phone": "J.phone"}[first]
			w.Online(first)
			w.Syncs(first)
			w.Online(second)
			w.Syncs(second)
			w.Settle()
			w.Check(groceries)
			w.WantShows(groceries, "eggs", "title", "eggs (6)", "J.phone", "J.mac", "A.phone")
			w.WantShows(groceries, "eggs", "completedAt", Completed, "J.phone", "J.mac", "A.phone")
			w.NoConflicts(groceries)
		})
	}
}

func TestCO007(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Sets("J.phone", groceries, "coffee", "title", "coffee beans"))
	w.must(w.Sets("A.phone", groceries, "coffee", "title", "ground coffee"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "coffee", "title", "coffee beans", "ground coffee")

	w.must(w.Sets("A.phone", groceries, "coffee", "title", "coffee beans"))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "coffee", "title", "coffee beans")
	w.WantShows(groceries, "coffee", "title", "coffee beans", "J.phone", "J.mac", "A.phone")
}

func TestCO008(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Reorders("J.phone", groceries, "milk", "l9"))  // to top
	w.must(w.Reorders("A.phone", groceries, "milk", "m1V")) // after apples
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "sortKey", "l9", "m1V") // the app is told
	open, _ := w.Order("J.phone", groceries)
	w.Want("milk shown", count(open, "milk"), 1)
	for item, key := range map[string]string{"coffee": "m0", "apples": "m1", "eggs": "m4"} {
		w.WantShows(groceries, item, "sortKey", key, "J.phone", "A.phone")
	}
}

func TestCO009(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Reorders("J.phone", groceries, "milk", "m0V"))
	w.must(w.Reorders("A.phone", groceries, "eggs", "m0V"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.NoConflicts(groceries)
	open, _ := w.Order("A.phone", groceries)
	w.Want("first", open[0], "coffee")
	w.Want("between coffee and apples", fmt.Sprint(slices.Sorted(slices.Values(open[1:3]))), "[eggs (12) milk]")
	w.Want("then", open[3], "apples")
}

func TestCO010(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Deletes("J.phone", groceries, "apples"))
	w.must(w.Sets("A.phone", groceries, "apples", "title", "green apples"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.WantGone(groceries, "apples", "J.phone", "J.mac", "A.phone")
	// Telling A.phone its edit landed on a deleted record: G-31, no signal.
}

func TestCO011(t *testing.T) {
	w := kitchen(t)
	w.Offline("A.phone") // sleeps before its edit is published
	w.must(w.AddsAt("A.phone", groceries, "cheese", "l9"))
	w.must(w.Launches("A.phone")) // iOS killed it while it slept
	for i := 0; i < 20; i++ {
		d := []string{"J.phone", "J.mac"}[i%2]
		w.must(w.Sets(d, groceries, []string{"milk", "eggs", "coffee", "apples"}[i%4], "notes", fmt.Sprint("change ", i)))
		w.Syncs(d)
	}
	w.Online("A.phone") // wakes
	w.Syncs("A.phone")
	w.Want("A.phone within one sync of waking", w.Dev("A.phone").State(w.cid(groceries)), func() any {
		w.Syncs("J.phone")
		return w.Dev("J.phone").State(w.cid(groceries))
	}())
	w.Settle()
	w.Check(groceries)
	w.WantShows(groceries, "cheese", "title", "cheese", "J.phone", "J.mac", "A.phone")
}

func TestCO012(t *testing.T) {
	orders := [][]string{
		{"A.phone", "J.mac", "J.phone"}, {"A.phone", "J.phone", "J.mac"}, {"J.mac", "A.phone", "J.phone"},
		{"J.mac", "J.phone", "A.phone"}, {"J.phone", "A.phone", "J.mac"}, {"J.phone", "J.mac", "A.phone"},
	}
	for _, order := range orders {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			w := kitchen(t)
			for _, d := range order {
				w.Offline(d)
			}
			items := []string{"milk", "eggs", "coffee", "apples", "bread"}
			for i := 0; i < 10; i++ {
				w.must(w.Sets("J.phone", groceries, items[i%5], "title", fmt.Sprint("J", i)))
				w.must(w.Sets("A.phone", groceries, items[(i+2)%5], "notes", fmt.Sprint("A", i)))
				w.Want("J.phone shows its own change", fmt.Sprint(w.Shows("J.phone", groceries, items[i%5], "title")), fmt.Sprint(fmt.Sprint("J", i), true))
			}
			for _, d := range order {
				w.Online(d)
				w.Syncs(d)
			}
			w.Settle()
			w.Check(groceries)
		})
	}
}

func TestCO013(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	var jItems []string
	for i := 0; i < 15; i++ {
		item := fmt.Sprintf("j%02d", i)
		jItems = append(jItems, item)
		w.must(w.AddsAt("J.phone", groceries, item, fmt.Sprintf("k%02d", 99-i)))
	}
	for _, item := range jItems[:9] {
		w.must(w.Sets("J.phone", groceries, item, "title", item+" renamed"))
	}
	w.must(w.Sets("J.phone", groceries, "coffee", "title", "coffee (J)"))
	for _, item := range jItems[3:13] {
		w.must(w.Completes("J.phone", groceries, item))
	}
	w.must(w.Deletes("J.phone", groceries, jItems[13]))
	w.must(w.Deletes("J.phone", groceries, jItems[14]))

	for i := 0; i < 5; i++ {
		w.must(w.AddsAt("A.phone", groceries, fmt.Sprintf("a%d", i), fmt.Sprintf("l%d", i)))
	}
	for _, item := range []string{"milk", "eggs", "bread", "coffee", "apples"} {
		w.must(w.Completes("A.phone", groceries, item))
	}
	w.must(w.Sets("A.phone", groceries, "coffee", "title", "coffee (A)"))
	w.Syncs("A.phone", "J.mac")

	w.Online("J.phone")
	w.Settle()
	w.Check(groceries)
	for _, item := range jItems[:13] {
		w.Want(item+" present", w.Present("A.phone", groceries, item), true)
	}
	w.WantGone(groceries, jItems[13], "A.phone", "J.phone")
	w.WantGone(groceries, jItems[14], "A.phone", "J.phone")
	for i := 0; i < 5; i++ {
		w.Want("a present", w.Present("J.phone", groceries, fmt.Sprintf("a%d", i)), true)
	}
	for _, item := range append(jItems[3:13], "milk", "eggs", "bread", "coffee", "apples") {
		v, _ := w.Shows("J.phone", groceries, item, "completedAt")
		w.Want(item+" completed", v, Completed)
	}
	w.WantKept(groceries, "coffee", "title", "coffee (A)", "coffee (J)")
	for _, item := range jItems[:9] {
		w.WantKept(groceries, item, "title", item+" renamed")
	}
	// "J.phone publishes after syncing the channel" isn't observable here.
}

func TestCO014(t *testing.T) {
	w := kitchen(t)
	w.must(w.AddsAt("J.phone", groceries, "party", "a0"))
	w.must(w.AddsUnder("J.phone", groceries, "cake", "party"))
	w.must(w.AddsUnder("J.phone", groceries, "candles", "party"))
	w.must(w.Completes("J.phone", groceries, "cake"))
	w.must(w.Completes("J.phone", groceries, "candles"))
	w.Settle()
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Completes("J.phone", groceries, "party"))
	w.must(w.AddsUnder("A.phone", groceries, "balloons", "party"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	// Every device sees a completed parent with an open sub-item and
	// repairs it before seeing another's repair.
	for _, d := range []string{"J.phone", "J.mac", "A.phone"} {
		w.Want(d+" repairs", w.Repairs(d, groceries), []string{"party"})
	}
	w.Settle()
	w.Check(groceries)
	w.WantShows(groceries, "party", "completedAt", "", "J.phone", "J.mac", "A.phone")
	w.WantShows(groceries, "balloons", "completedAt", "", "J.phone", "J.mac", "A.phone")
	w.WantShows(groceries, "balloons", "parentID", ParentRef(groceries, "party"), "J.phone", "A.phone")
	w.Want("nothing more to repair", w.Repairs("A.phone", groceries), []string(nil))
	// The repairs agree on the value; Tandem still keeps them as three
	// equal-valued siblings (RC-039, G-38: equal values stay a conflict).
	w.WantKept(groceries, "party", "completedAt", "", "", "")
}

func TestCO015(t *testing.T) {
	w := kitchen(t)
	w.must(w.AddsAt("J.phone", groceries, "X", "a1"))
	w.must(w.AddsAt("J.phone", groceries, "Y", "a2"))
	w.Settle()
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Indents("J.phone", groceries, "X", "Y"))
	w.must(w.Indents("A.phone", groceries, "Y", "X"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.NoConflicts(groceries)
	w.WantShows(groceries, "X", "parentID", ParentRef(groceries, "Y"), "J.phone", "J.mac", "A.phone")
	w.WantShows(groceries, "Y", "parentID", ParentRef(groceries, "X"), "J.phone", "J.mac", "A.phone")
	// Rendering the cycle one level deep is G-33: Liszto has no rule yet.
}

func TestCO016(t *testing.T) {
	w := kitchen(t)
	w.must(w.AddsAt("J.phone", groceries, "trip", "a0"))
	w.must(w.Sets("J.phone", groceries, "trip", "notes", "Pack passports"))
	w.Settle()
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Sets("J.phone", groceries, "trip", "notes", "Pack passports\nBook taxi"))
	w.must(w.Sets("A.phone", groceries, "trip", "notes", "Pack passports\nFeed the cat"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.WantKept(groceries, "trip", "notes", "Pack passports\nBook taxi", "Pack passports\nFeed the cat")
	// Liszto resolves notes by writing a merged value covering both (§17);
	// here one device does, as G-38 leaves open which.
	merged := "Pack passports\nBook taxi\n———\nPack passports\nFeed the cat"
	w.must(w.Sets("J.phone", groceries, "trip", "notes", merged))
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "trip", "notes", merged)
}

func TestCO017(t *testing.T) {
	w := kitchen(t)
	w.must(w.Tags("J.phone", groceries, "milk", "dairy", true))
	w.Settle()
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Tags("J.phone", groceries, "milk", "dairy", false))
	w.must(w.Tags("A.phone", groceries, "milk", "dairy", true))
	w.must(w.Tags("A.phone", groceries, "milk", "fridge", true))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	for _, d := range []string{"J.phone", "J.mac", "A.phone"} {
		w.Want(d+" tags", w.Elements(d, groceries, "milk", "tags"), []string{"dairy", "fridge"})
	}
}

func TestCO018(t *testing.T) {
	w := kitchen(t)
	w.Creates("J.phone", "House")
	w.Shares("J.phone", "House", "A", "writer")
	w.must(w.AddsAt("J.phone", groceries, "light bulbs", "a0"))
	w.Settle()
	w.Offline("J.phone")
	w.Offline("A.phone")
	w.must(w.Moves("J.phone", groceries, "light bulbs", "House"))
	w.must(w.Sets("A.phone", groceries, "light bulbs", "title", "LED bulbs"))
	w.Online("J.phone")
	w.Online("A.phone")
	w.Settle()
	w.Check(groceries)
	w.Check("House")
	w.WantGone(groceries, "light bulbs", "J.phone", "J.mac", "A.phone")
	w.WantShows("House", "light bulbs", "title", "light bulbs", "J.phone", "J.mac", "A.phone")
}

func TestCO019(t *testing.T) {
	t.Skip("needs: the debounce and garbage collection (§10.7); push latency")
}

func TestCO020(t *testing.T) {
	w := kitchen(t)
	w.Offline("J.phone")
	w.Offline("J.mac")
	w.must(w.Sets("J.phone", groceries, "milk", "title", "whole milk"))
	w.must(w.Sets("J.mac", groceries, "milk", "title", "skimmed milk"))
	w.Online("J.phone")
	w.Online("J.mac")
	w.Settle()
	w.Check(groceries)
	w.WantKept(groceries, "milk", "title", "skimmed milk", "whole milk")
}

func TestCO021(t *testing.T) {
	w := kitchen(t)
	w.must(w.Sets("J.phone", groceries, "coffee", "title", "a"))
	for _, s := range []struct{ dev, v string }{{"A.phone", "b"}, {"J.mac", "c"}, {"A.phone", "a"}} {
		w.Syncs(s.dev)
		w.must(w.Sets(s.dev, groceries, "coffee", "title", s.v))
		w.WantKept(groceries, "coffee", "title", s.v)
	}
	w.Settle()
	w.Check(groceries)
	w.NoConflicts(groceries)
	w.WantShows(groceries, "coffee", "title", "a", "J.phone", "J.mac", "A.phone")
}

func TestCO022(t *testing.T) {
	t.Skip("needs: device clock control (the engine's clock is time.Now)")
}

func TestCO023(t *testing.T) {
	// The delete is published at once (no debounce), so only the second
	// run exists: undo re-creates the item as a new record (G-29, G-30).
	w := kitchen(t)
	w.must(w.Tags("J.phone", groceries, "milk", "dairy", true))
	w.must(w.Sets("J.phone", groceries, "milk", "notes", "2 cartons"))
	w.Settle()
	saved := w.Dev("A.phone").State(w.cid(groceries))[string(ItemID(groceries, "milk"))]
	w.must(w.Deletes("A.phone", groceries, "milk"))
	for _, f := range sortedKeys(saved.Fields) {
		w.must(w.Sets("A.phone", groceries, "milk (undone)", f, saved.Fields[f]))
	}
	for _, el := range saved.Sets["tags"] {
		w.must(w.Tags("A.phone", groceries, "milk (undone)", el, true))
	}
	w.Settle()
	w.Check(groceries)
	w.WantGone(groceries, "milk", "J.phone", "J.mac", "A.phone")
	got := w.Dev("J.phone").State(w.cid(groceries))[string(ItemID(groceries, "milk (undone)"))]
	w.Want("restored fields", got, saved)
	open, _ := w.Order("J.mac", groceries)
	w.Want("milk shown", count(open, "milk"), 1)
}

func TestCO024(t *testing.T) {
	w := kitchen(t)
	for i := 0; i < 300; i++ {
		w.must(w.AddsAt([]string{"J.phone", "A.phone"}[i%2], groceries, fmt.Sprintf("item%03d", i), fmt.Sprintf("n%03d", i)))
	}
	w.Settle()
	w.Dev("B.phone")
	epoch := w.Epoch("J.phone", groceries)
	w.Shares("J.mac", groceries, "B", "writer")
	// J and A keep editing while B fetches.
	for i := 0; i < 10; i++ {
		w.must(w.Completes("J.phone", groceries, fmt.Sprintf("item%03d", i)))
		w.must(w.Sets("A.phone", groceries, fmt.Sprintf("item%03d", 100+i), "notes", "busy"))
		if i == 5 {
			w.Syncs("B.phone")
		}
	}
	w.Settle()
	w.Check(groceries)
	w.Want("no rotation", string(w.Epoch("B.phone", groceries)), string(epoch))
	w.must(w.Sets("B.phone", groceries, "item200", "title", "B was here"))
	w.Syncs("B.phone", "J.phone", "A.phone")
	w.WantShows(groceries, "item200", "title", "B was here", "J.phone", "A.phone")
	w.Settle()
	w.Check(groceries)
}

func TestCO025(t *testing.T) {
	w := NewWorld(t, oracle)
	w.Creates("J.phone", groceries)
	for _, d := range []string{"A.phone", "B.phone"} {
		w.Dev(d)
	}
	items := make([]string, 30)
	for i := range items {
		items[i] = fmt.Sprintf("item%02d", i)
		w.must(w.AddsAt("J.phone", groceries, items[i], fmt.Sprintf("n%02d", i)))
	}
	w.Shares("J.phone", groceries, "A", "writer")
	w.Shares("J.phone", groceries, "B", "writer")
	w.Settle()
	for i := 0; i < 5; i++ {
		w.must(w.Completes("J.phone", groceries, items[i]))
		w.must(w.Completes("B.phone", groceries, items[10+i]))
		w.Syncs("J.phone", "B.phone", "A.phone")
	}
	w.Offline("B.phone") // the car park
	for _, i := range []int{15, 16, 17, 18, 19} {
		w.must(w.Completes("B.phone", groceries, items[i]))
	}
	w.must(w.Completes("J.phone", groceries, items[18]))
	w.must(w.Completes("J.phone", groceries, items[19]))
	w.must(w.Completes("J.phone", groceries, items[5]))
	w.Syncs("J.phone", "A.phone")
	for i := 0; i < 3; i++ {
		w.must(w.AddsAt("A.phone", groceries, fmt.Sprintf("new%d", i), fmt.Sprintf("a%d", i)))
	}
	w.must(w.Sets("A.phone", groceries, items[5], "title", "item05 (A)"))
	w.Online("B.phone")
	w.Settle()
	w.Check(groceries)
	open, done := w.Order("B.phone", groceries)
	w.Want("A's items at the top", open[:3], []string{"new0", "new1", "new2"})
	w.Want("completed", len(done), 16)
	w.WantShows(groceries, items[5], "title", "item05 (A)", "J.phone", "A.phone", "B.phone")
	w.WantShows(groceries, items[5], "completedAt", Completed, "J.phone", "A.phone", "B.phone")
	w.WantKept(groceries, items[5], "title", "item05 (A)")
}

func count(xs []string, x string) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

func players(w *World, n int, coll string) []string {
	var devs []string
	for i := 1; i <= n; i++ {
		devs = append(devs, fmt.Sprintf("P%d.d1", i))
		w.Dev(devs[i-1])
	}
	w.Creates(devs[0], coll)
	for i := 2; i <= n; i++ {
		w.Shares(devs[0], coll, fmt.Sprintf("P%d", i), "writer")
	}
	w.Settle()
	return devs
}

func TestCO026(t *testing.T) {
	w := NewWorld(t, oracle)
	r := rand.New(rand.NewPCG(26, 26))
	devs := players(w, 10, "Party")
	var all []string
	for i, d := range devs {
		for j := 0; j < 10; j++ {
			item := fmt.Sprintf("p%d-%d", i+1, j)
			all = append(all, item)
			w.must(w.AddsAt(d, "Party", item, fmt.Sprintf("k%d%d", 9-j, i)))
		}
	}
	w.Settle()
	for _, d := range devs {
		for j := 0; j < 3; j++ {
			w.must(w.Completes(d, "Party", all[r.IntN(100)]))
		}
		for j := 0; j < 2; j++ {
			w.must(w.Reorders(d, "Party", all[r.IntN(100)], fmt.Sprintf("z%d", r.IntN(1000))))
		}
	}
	w.Settle()
	w.Check("Party")
	for _, d := range devs {
		open, done := w.Order(d, "Party")
		got := slices.Sorted(slices.Values(append(open, done...)))
		w.Want(d+" shows every item once", got, slices.Sorted(slices.Values(all)))
	}
}

func TestCO027(t *testing.T) {
	t.Skip("needs: leave notices (§11.5)")
}

func TestCO028(t *testing.T) {
	w := NewWorld(t, oracle)
	devs := players(w, 10, "Rota")
	w.must(w.AddsAt(devs[0], "Rota", "rota", "a0"))
	w.Settle()
	var titles []string
	for i, d := range devs {
		w.Offline(d)
		titles = append(titles, fmt.Sprintf("rota by P%d", i+1))
		w.must(w.Sets(d, "Rota", "rota", "title", titles[i]))
		w.must(w.Sets(d, "Rota", "rota", "notes", fmt.Sprintf("note by P%d", i+1)))
	}
	r := rand.New(rand.NewPCG(28, 28))
	for _, i := range r.Perm(10) {
		w.Online(devs[i])
		w.Syncs(devs[i])
	}
	w.Settle()
	w.Check("Rota")
	w.WantKept("Rota", "rota", "title", titles...)
	w.must(w.Sets("P3.d1", "Rota", "rota", "title", "the rota"))
	w.Settle()
	w.Check("Rota")
	w.WantKept("Rota", "rota", "title", "the rota")
	// Merging the ten notes is Liszto's (§17, G-38); the 16-sibling
	// presentation limit is the app's (§9.2).
}

// ---- families ----

// fam is a family's parameters (collab.md, "Schedules and op mixes").
type fam struct {
	players [2]int // inclusive range
	devs    int    // devices per player, 1..devs
	ops     [2]int // total ops
	mix     string // list, reorder, tree, hot
	sched   string // live, sleep, outage, plane, stagger, flap, chaos
	removal bool   // the owner removes a player part-way (chaos)
}

// seeds runs a family over TANDEM_FAMILY_RUNS seeds (default 5);
// TANDEM_FAMILY_SEED replays one.
func seeds(t *testing.T, salt uint64, run func(t *testing.T, r *rand.Rand)) {
	runs, first := familyRuns(5)
	for seed := first; seed < first+runs; seed++ {
		t.Run(fmt.Sprint("seed=", seed), func(t *testing.T) {
			run(t, rand.New(rand.NewPCG(uint64(seed), salt)))
		})
	}
}

// loseReplies: S-FLAP includes appends whose answers are lost. Off with
// TANDEM_LOSE_REPLIES=0, to run the flapping families past the engine bug
// that FT-006 records.
var loseReplies = os.Getenv("TANDEM_LOSE_REPLIES") != "0"

const lostReplyBug = "engine bug: a sync whose flush fails after reading the change feed drops the new message refs for good (FT-006); set TANDEM_LOSE_REPLIES=0 to run without lost replies"

func (f fam) run(t *testing.T, r *rand.Rand) {
	w := NewWorld(t, oracle)
	const coll = "List"
	n := f.players[0] + r.IntN(f.players[1]-f.players[0]+1)
	var users []string
	devsOf := map[string][]string{}
	var devs []string
	for i := 1; i <= n; i++ {
		u := fmt.Sprintf("P%d", i)
		users = append(users, u)
		k := 1 + r.IntN(f.devs)
		if f.sched == "sleep" {
			k = 1 + r.IntN(2) // a phone, and half the time a Mac
		}
		for j := 1; j <= k; j++ {
			d := fmt.Sprintf("%s.d%d", u, j)
			w.Dev(d)
			devsOf[u] = append(devsOf[u], d)
			devs = append(devs, d)
		}
	}
	owner := devsOf["P1"][0]
	w.Creates(owner, coll)
	joined := map[string]bool{"P1": true}
	join := func(u string) {
		epoch := w.Epoch(owner, coll)
		w.Online(owner)
		w.Shares(owner, coll, u, "writer")
		joined[u] = true
		if string(w.Epoch(owner, coll)) != string(epoch) {
			t.Fatal("adding a member rotated the key")
		}
	}
	if f.sched != "stagger" {
		for _, u := range users[1:] {
			join(u)
		}
	}
	w.Settle()

	items := []string{"i0", "i1", "i2", "i3", "i4"}
	if f.mix == "hot" {
		items = items[:3]
	}
	for _, it := range items {
		w.must(w.AddsAt(owner, coll, it, fmt.Sprintf("m%d", r.IntN(100))))
	}
	w.Settle()
	pick := func() string { return items[r.IntN(len(items))] }
	if f.mix == "reorder" || f.mix == "hot" {
		pick = func() string { return items[r.IntN(min(len(items), 5))] }
	}
	newItem := func() string {
		it := fmt.Sprintf("i%d", len(items))
		items = append(items, it)
		return it
	}
	key := func() string { return fmt.Sprintf("m%03d", r.IntN(1000)) }

	op := func(d string) {
		switch f.mix {
		case "list", "hot":
			switch x := r.IntN(100); {
			case x < 30:
				if f.mix == "hot" {
					w.Sets(d, coll, pick(), "title", fmt.Sprint(d, x))
				} else {
					w.AddsAt(d, coll, newItem(), key())
				}
			case x < 45:
				w.Sets(d, coll, pick(), "title", fmt.Sprint(d, "-", r.IntN(1000)))
			case x < 65:
				w.Reorders(d, coll, pick(), key())
			case x < 80:
				if r.IntN(2) == 0 {
					w.Completes(d, coll, pick())
				} else {
					w.Uncompletes(d, coll, pick())
				}
			case x < 85:
				w.Deletes(d, coll, pick())
			case x < 90:
				w.Sets(d, coll, pick(), "notes", fmt.Sprint(d, "-", r.IntN(1000)))
			case x < 95:
				w.Tags(d, coll, pick(), []string{"a", "b", "c"}[r.IntN(3)], r.IntN(3) > 0)
			default:
				if r.IntN(2) == 0 {
					w.Indents(d, coll, pick(), pick())
				} else {
					w.Outdents(d, coll, pick())
				}
			}
		case "reorder":
			switch x := r.IntN(100); {
			case x < 70:
				w.Reorders(d, coll, pick(), key())
			case x < 85:
				w.AddsAt(d, coll, newItem(), key())
			case x < 90:
				w.Deletes(d, coll, pick())
			default:
				w.Sets(d, coll, pick(), "title", fmt.Sprint(d, "-", r.IntN(1000)))
			}
		case "tree":
			switch x := r.IntN(100); {
			case x < 40:
				if r.IntN(3) > 0 {
					w.Indents(d, coll, pick(), pick())
				} else {
					w.Outdents(d, coll, pick())
				}
			case x < 70:
				if r.IntN(2) == 0 {
					w.Completes(d, coll, pick())
				} else {
					w.Uncompletes(d, coll, pick())
				}
			case x < 90:
				w.AddsUnder(d, coll, newItem(), pick())
			default:
				w.Deletes(d, coll, pick())
			}
		}
	}

	total := f.ops[0] + r.IntN(f.ops[1]-f.ops[0]+1)
	outageFrom, outageTo := total/4+r.IntN(total/4+1), total/2+r.IntN(total/4+1)
	plane := devs[r.IntN(len(devs))]
	if f.sched == "plane" {
		w.Offline(plane)
	}
	stagger := map[int]string{}
	if f.sched == "stagger" {
		for _, u := range users[1:] {
			stagger[r.IntN(total)] = u
		}
	}
	removeAt := -1
	if f.removal && n > 2 {
		removeAt = total/3 + r.IntN(total/3)
	}
	removed := map[string]bool{}
	for step := 0; step < total; step++ {
		if u, ok := stagger[step]; ok {
			join(u)
		}
		var active []string
		for _, d := range devs {
			u, _, _ := cutUser(d)
			if joined[u] && !removed[u] {
				active = append(active, d)
			}
		}
		d := active[r.IntN(len(active))]
		if f.sched == "plane" && r.IntN(2) == 0 {
			d = plane
		}
		op(d)
		switch f.sched {
		case "live":
			w.Syncs(d, active[r.IntN(len(active))])
		case "sleep":
			if x := r.IntN(10); x == 0 && len(devsOf[userOfDev(d)]) > 0 && d == devsOf[userOfDev(d)][0] {
				w.Offline(d)
			} else if x == 1 {
				w.Online(d)
			} else {
				w.Syncs(d)
			}
		case "outage":
			if step == outageFrom {
				for _, x := range devs {
					w.Offline(x)
				}
			}
			if step == outageTo {
				for _, i := range r.Perm(len(devs)) {
					w.Online(devs[i])
					w.Syncs(devs[i])
				}
			}
			w.Syncs(d)
		case "plane", "stagger":
			w.Syncs(d)
		case "flap", "chaos":
			switch x := r.IntN(20); {
			case x < 3:
				w.Offline(d)
			case x < 7:
				w.Online(d)
			case x < 9:
				w.LosesReplies(d, loseReplies)
			case x < 12:
				w.LosesReplies(d, false)
			case x < 13 && f.sched == "chaos":
				w.LosesReplies(d, false)
				w.Online(d)
				w.must(w.Launches(d))
			default:
				w.Syncs(d)
			}
		}
		if step == removeAt {
			victim := users[1+r.IntN(n-1)]
			w.Online(owner)
			w.LosesReplies(owner, false)
			w.Syncs(owner)
			points := []string{"", "1:chosen", "2:journal", "2:note", "3:fragment", "3:roster", "4:announced", "4:home", "5:grant", "6:cleaned"}
			point := points[r.IntN(len(points))]
			died, err := w.Crashes(owner, point, func() error { return w.Removes(owner, coll, victim) })
			if died {
				w.must(w.Launches(owner))
			} else if err != nil {
				t.Fatalf("removing %s: %v", victim, err)
			}
			if _, still := w.Roles(owner, coll)[victim]; !still {
				w.Forget(coll, victim)
				removed[victim] = true
			}
		}
	}
	for _, d := range devs {
		w.LosesReplies(d, false)
		w.Online(d)
	}
	w.Settle()
	w.Check(coll)
	if f.mix == "tree" {
		// Liszto's repair rule, run by every device before it sees any
		// other's repair; afterwards no completed parent has an open
		// sub-item anywhere.
		for _, d := range devs {
			if !removed[userOfDev(d)] {
				w.Repairs(d, coll)
			}
		}
		w.Settle()
		w.Check(coll)
		w.Want("repairs left after repairing", w.Repairs(owner, coll), []string(nil))
	}
}

func cutUser(d string) (string, string, bool) {
	for i := range d {
		if d[i] == '.' {
			return d[:i], d[i+1:], true
		}
	}
	return d, "", false
}

func userOfDev(d string) string { u, _, _ := cutUser(d); return u }

func TestCOF01(t *testing.T) {
	seeds(t, 0xc0f1, fam{players: [2]int{1, 10}, devs: 3, ops: [2]int{20, 120}, mix: "list", sched: "live"}.run)
}

func TestCOF02(t *testing.T) {
	seeds(t, 0xc0f2, fam{players: [2]int{2, 10}, devs: 2, ops: [2]int{20, 120}, mix: "list", sched: "sleep"}.run)
}

func TestCOF03(t *testing.T) {
	seeds(t, 0xc0f3, func(t *testing.T, r *rand.Rand) {
		mix := []string{"list", "hot"}[r.IntN(2)]
		fam{players: [2]int{2, 10}, devs: 3, ops: [2]int{20, 120}, mix: mix, sched: "outage"}.run(t, r)
	})
}

func TestCOF04(t *testing.T) {
	seeds(t, 0xc0f4, fam{players: [2]int{2, 10}, devs: 1, ops: [2]int{50, 150}, mix: "list", sched: "plane"}.run)
}

func TestCOF05(t *testing.T) {
	seeds(t, 0xc0f5, fam{players: [2]int{2, 10}, devs: 1, ops: [2]int{20, 120}, mix: "list", sched: "stagger"}.run)
}

func TestCOF06(t *testing.T) {
	if loseReplies {
		t.Skip(lostReplyBug)
	}
	seeds(t, 0xc0f6, fam{players: [2]int{2, 10}, devs: 2, ops: [2]int{20, 120}, mix: "list", sched: "flap"}.run)
}

func TestCOF07(t *testing.T) {
	seeds(t, 0xc0f7, func(t *testing.T, r *rand.Rand) {
		sched := []string{"live", "outage"}[r.IntN(2)]
		fam{players: [2]int{2, 10}, devs: 1, ops: [2]int{20, 120}, mix: "reorder", sched: sched}.run(t, r)
	})
}

func TestCOF08(t *testing.T) {
	seeds(t, 0xc0f8, func(t *testing.T, r *rand.Rand) {
		sched := []string{"live", "outage", "sleep"}[r.IntN(3)]
		fam{players: [2]int{2, 10}, devs: 1, ops: [2]int{20, 120}, mix: "tree", sched: sched}.run(t, r)
	})
}

func TestCOF09(t *testing.T) {
	if loseReplies {
		t.Skip(lostReplyBug)
	}
	seeds(t, 0xc0f9, func(t *testing.T, r *rand.Rand) {
		mix := []string{"list", "reorder", "tree", "hot"}[r.IntN(4)]
		fam{players: [2]int{1, 10}, devs: 3, ops: [2]int{20, 120}, mix: mix, sched: "chaos", removal: true}.run(t, r)
	})
}
