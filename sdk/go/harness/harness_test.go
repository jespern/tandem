package harness

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

var oracle *Oracle

func TestMain(m *testing.M) {
	o, err := StartOracle()
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness: no oracle, convergence checks only:", err)
	} else {
		oracle = o
	}
	code := m.Run()
	if oracle != nil {
		oracle.Close()
	}
	os.Exit(code)
}

// Family: players edit one list at random through outages, and the owner
// removes one of them part-way. After everyone is back online and synced,
// every remaining device shows the same list, which is what the reference
// model computes from every write made.
//
// TANDEM_FAMILY_RUNS sets the number of seeds (default 25); a failure names
// its seed, and TANDEM_FAMILY_SEED replays one.
func TestFamilyEditsOutagesRemoval(t *testing.T) {
	runs, first := 25, 1
	if n, err := strconv.Atoi(os.Getenv("TANDEM_FAMILY_RUNS")); err == nil {
		runs = n
	}
	if s, err := strconv.Atoi(os.Getenv("TANDEM_FAMILY_SEED")); err == nil {
		first, runs = s, 1
	}
	for seed := first; seed < first+runs; seed++ {
		t.Run(fmt.Sprint("seed=", seed), func(t *testing.T) { family(t, uint64(seed)) })
	}
}

func family(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0x7a4d))
	w := NewWorld(t, oracle)
	users := []string{"J", "A", "B", "C", "D"}[:2+r.IntN(4)]
	var devs []string
	for _, u := range users {
		for i := 0; i < 1+r.IntN(2); i++ {
			devs = append(devs, fmt.Sprintf("%s.d%d", u, i))
			w.Dev(devs[len(devs)-1])
		}
	}
	const coll = "List"
	w.Creates("J.d0", coll)
	for _, u := range users[1:] {
		w.Shares("J.d0", coll, u, "writer")
	}
	w.Settle()
	items := []string{"milk", "eggs", "bread", "coffee", "apples", "tea"}
	removeAt, removed := -1, ""
	if len(users) > 2 && r.IntN(2) == 0 {
		removeAt = 10 + r.IntN(40)
	}
	for step := 0; step < 60; step++ {
		d := devs[r.IntN(len(devs))]
		item := items[r.IntN(len(items))]
		switch x := r.IntN(100); {
		case x < 35:
			w.Sets(d, coll, item, []string{"title", "notes"}[r.IntN(2)], fmt.Sprintf("%s-%d", d, step))
		case x < 50:
			w.Tags(d, coll, item, []string{"dairy", "urgent", "weekly"}[r.IntN(3)], r.IntN(3) > 0)
		case x < 53:
			w.Deletes(d, coll, item)
		case x < 75:
			w.Syncs(d)
		case x < 88:
			w.Offline(d)
		default:
			w.Online(d)
		}
		if step == removeAt {
			w.Online("J.d0")
			w.Syncs("J.d0")
			removed = users[1+r.IntN(len(users)-1)]
			if err := w.Removes("J.d0", coll, removed); err != nil {
				t.Fatalf("seed %d: removing %s: %v", seed, removed, err)
			}
		}
	}
	for _, d := range devs {
		w.Online(d)
	}
	w.Settle()
	w.Check(coll)
}

// knownBug reports an engine bug a scenario observed: a failure, or a skip
// with TANDEM_KNOWN_BUGS=skip.
func knownBug(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("TANDEM_KNOWN_BUGS") == "skip" {
		t.Skip(msg)
	}
	t.Fatal(msg)
}

func knownBugf(t *testing.T, format string, args ...any) {
	t.Helper()
	knownBug(t, fmt.Sprintf(format, args...))
}
