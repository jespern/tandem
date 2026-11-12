package harness

import (
	"errors"

	"github.com/jespern/tandem/sdk/go/device"
)

// CrashPoints are the stops of a key change (§11.6) the engine offers.
var CrashPoints = []string{"1:chosen", "2:journal", "2:note", "3:fragment", "3:data", "3:roster",
	"4:announced", "4:home", "5:grant", "6:cleaned"}

// RemovesCrashing starts removing a user on ownerDev and stops the device at
// point, as a crash would. It reports whether the crash happened (a change
// can end before reaching a point, e.g. 5:grant with no one to grant to).
func (w *World) RemovesCrashing(ownerDev, coll, user, point string) (crashedAt bool) {
	w.T.Helper()
	d := w.Dev(ownerDev)
	d.Crash = func(p string) {
		if p == point {
			panic(crashed{p})
		}
	}
	defer func() { d.Crash = nil }()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crashed); !ok {
				panic(r)
			}
			crashedAt = true
		}
	}()
	if err := w.Removes(ownerDev, coll, user); err != nil {
		w.T.Fatalf("removing %s: %v", user, err)
	}
	return false
}

// FinishesOrRedoes acts on a pending change from devName (§11.7): finish it
// if it can be finished, otherwise discard it and make it again there.
func (w *World) FinishesOrRedoes(devName, coll, user string) {
	w.T.Helper()
	d, cid := w.Dev(devName), w.cid(coll)
	n := d.Pending(cid)
	if n == nil {
		return
	}
	err := d.Finish(ctx, cid, n)
	if errors.Is(err, device.ErrCantFinish) {
		w.must(d.Discard(ctx, cid, n))
		err = w.Removes(devName, coll, user)
	}
	if err != nil && !errors.Is(err, device.ErrPending) {
		w.T.Fatalf("%s finishing: %v", devName, err)
	}
	delete(w.members[coll], user)
}

// Listed reports whether devName's head statement lists user.
func (w *World) Listed(devName, coll, user string) bool {
	_, ok := w.Dev(devName).Members(w.cid(coll))[user]
	return ok
}
