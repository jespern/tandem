package harness

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

func seeds(t *testing.T, def int, f func(t *testing.T, seed uint64)) {
	runs, first := def, 1
	if n, err := strconv.Atoi(os.Getenv("TANDEM_FAMILY_RUNS")); err == nil {
		runs = n
	}
	if s, err := strconv.Atoi(os.Getenv("TANDEM_FAMILY_SEED")); err == nil {
		first, runs = s, 1
	}
	for seed := first; seed < first+runs; seed++ {
		t.Run(fmt.Sprint("seed=", seed), func(t *testing.T) { f(t, uint64(seed)) })
	}
}

// Family: J removes a member on one device, which crashes at a random point
// of the key change. Then, at random, the same device restarts and resumes,
// or J's second device finishes or discards-and-redoes it, or both happen in
// either order. Everyone keeps editing and going offline throughout. In the
// end the member is gone, nothing is dead, and every remaining device shows
// what the reference model computes.
func TestFamilyCrashedKeyChange(t *testing.T) {
	seeds(t, 25, func(t *testing.T, seed uint64) {
		r := rand.New(rand.NewPCG(seed, 0xc4a5))
		w := NewWorld(t, oracle)
		devs := []string{"J.d0", "J.d1", "A.d0", "B.d0", "B.d1", "C.d0"}
		for _, d := range devs {
			w.Dev(d)
		}
		const coll = "House"
		w.Creates("J.d0", coll)
		for _, u := range []string{"A", "B", "C"} {
			w.Shares("J.d0", coll, u, "writer")
		}
		w.Settle()
		items := []string{"milk", "eggs", "bread", "coffee"}
		edit := func(n int) {
			for i := 0; i < n; i++ {
				d := devs[2+r.IntN(len(devs)-2)] // members other than J edit; J's devices too, sometimes
				if r.IntN(4) == 0 {
					d = devs[r.IntN(2)]
				}
				switch x := r.IntN(10); {
				case x < 5:
					w.Sets(d, coll, items[r.IntN(len(items))], "title", fmt.Sprintf("%s-%d-%d", d, seed, i))
				case x < 7:
					w.Tags(d, coll, items[r.IntN(len(items))], "t", r.IntN(2) == 0)
				case x < 9:
					w.Syncs(d)
				default:
					if w.IsOffline(d) {
						w.Online(d)
					} else if d != "J.d0" && d != "J.d1" {
						w.Offline(d)
					}
				}
			}
		}
		edit(15)
		w.Online("J.d0")
		w.Online("J.d1")
		w.Syncs("J.d0", "J.d1")
		victim := []string{"A", "B", "C"}[r.IntN(3)]
		point := CrashPoints[r.IntN(len(CrashPoints))]
		w.RemovesCrashing("J.d0", coll, victim, point)
		edit(10)
		switch r.IntN(3) {
		case 0: // the same device comes back
			w.Relaunches("J.d0")
		case 1: // the second device deals with it, then the first comes back
			w.Pending("J.d1", coll)
			w.FinishesOrRedoes("J.d1", coll, victim)
			w.Relaunches("J.d0")
		default: // the first comes back, then the second looks
			w.Relaunches("J.d0")
			w.Pending("J.d1", coll)
			w.FinishesOrRedoes("J.d1", coll, victim)
		}
		// If the change was lost before anything left the device, the
		// user asks again (nothing changed, and they're told so).
		w.Syncs("J.d0")
		if w.Listed("J.d0", coll, victim) {
			if err := w.Removes("J.d0", coll, victim); err != nil {
				t.Fatalf("seed %d (crash at %s): asking again: %v", seed, point, err)
			}
		}
		delete(w.members[coll], victim)
		edit(10)
		for _, d := range devs {
			w.Online(d)
		}
		w.Settle()
		for _, d := range []string{"J.d0", "J.d1"} {
			if w.Listed(d, coll, victim) {
				t.Fatalf("seed %d (crash at %s): %s still lists %s", seed, point, d, victim)
			}
			if w.Dev(d).Pending(w.cid(coll)) != nil {
				t.Fatalf("seed %d (crash at %s): a change is still pending on %s", seed, point, d)
			}
		}
		w.Check(coll)
	})
}
