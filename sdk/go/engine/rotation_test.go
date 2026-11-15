package engine

import (
	"bytes"
	"errors"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// §11.6–11.7: Bob owns House, shared with Alice and Carol; he changes its key.

type rotWorld struct {
	hub              *MemoryHub
	f                *Founding
	bobKeys          *tandem.DeviceKeys
	bobStore         *Store
	bob              *Device
	alice, carol     *Device
	aliceID, carolID []byte
	house, milk      []byte
}

func newRotWorld(t *testing.T) rotWorld {
	bobStore := MemoryStore()
	hub, f, bobKeys, bob := found(t, "Bob’s iPhone", bobStore)
	alice, carol := join(t, bob, f, hub, "Alice", nil), join(t, bob, f, hub, "Carol", nil)
	syncAll(t, bob)
	house := try(bob.CreateList(bg, "House"))
	milk := NewRecordID()
	must(t, bob.SetField(bg, house, milk, "title", []byte("Milk")))
	for _, m := range []*Device{alice, carol} {
		must(t, bob.Share(bg, house, me(t, m).Participant, Writer, ""))
		syncAll(t, m)
		must(t, m.Accept(bg, house))
	}
	must(t, alice.SetField(bg, house, NewRecordID(), "title", []byte("Bread")))
	syncAll(t, bob)
	return rotWorld{hub, f, bobKeys, bobStore, bob, alice, carol, me(t, alice).Participant, me(t, carol).Participant, house, milk}
}

// checkRemoved: after a removal, Alice moves to the new epoch with everything; Carol is out and her late write stays out.
func checkRemoved(t *testing.T, w rotWorld, bob *Device, journal *Store) {
	t.Helper()
	syncAll(t, w.alice)
	if r := w.alice.Role(w.house); r != Writer {
		t.Fatal("Alice's role", r)
	}
	if got := titles(w.alice, w.house); !sameSet(got, set("Milk", "Bread")) {
		t.Fatal("Alice sees", got)
	}
	must(t, w.alice.SetField(bg, w.house, NewRecordID(), "title", []byte("Eggs")))
	syncAll(t, bob)
	if got := titles(bob, w.house); !sameSet(got, set("Milk", "Bread", "Eggs")) {
		t.Fatal("Bob sees", got)
	}
	syncAll(t, w.carol)
	if r := w.carol.Role(w.house); r != "" {
		t.Fatal("Carol learns of her removal from the announcement", r)
	}
	if err := w.carol.SetField(bg, w.house, NewRecordID(), "title", []byte{1}); err == nil {
		t.Fatal("removed")
	}
	if bob.IsDead(w.house) {
		t.Fatal("dead")
	}
	if journal != nil {
		if steps := try(journal.JournalSteps()); len(steps) != 0 {
			t.Fatal(steps)
		}
	}
}

func TestRotationRemovingAMemberChangesTheKey(t *testing.T) {
	w := newRotWorld(t)
	oldEpoch, oldKey := epochKey(w.bob, w.house)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.house))
	// Carol, not yet aware, writes into the old epoch.
	must(t, w.carol.SetField(bg, w.house, NewRecordID(), "title", []byte("Late")))
	checkRemoved(t, w, w.bob, w.bobStore)
	syncAll(t, w.bob)
	if titles(w.bob, w.house)["Late"] {
		t.Fatal("a late write in the replaced epoch isn't carried")
	}
	// Step 6 left the old channel with its roster and nothing the new epoch needs.
	oldTag := tandem.CollectionTag(oldKey, w.f.Pin.Hub, oldEpoch)
	for _, m := range w.hub.Messages() {
		if m.Channel != oldTag {
			continue
		}
		_, p := try2(tandem.Open(oldKey, w.f.Pin.Hub, oldTag, m.Body))
		state, _, err := tandem.ParseKind1(p, w.f.Pin.Hub, w.house)
		if err != nil || state == nil {
			continue
		}
		for _, x := range state.Writes {
			if !bytes.Equal(x.Author, w.carolID) && !bytes.Equal(state.Record, rosterRecord) {
				t.Fatal("only the roster and Carol's late write remain in the old channel")
			}
		}
	}
}

// Alice was offline during the change and wrote into the old epoch: her device signs that write
// again into the new one. Carol, removed, can't sign hers into an epoch she has no key to; her
// replica keeps it, read-only (§11.6, "Moving over").
func TestRotationLateWritesAreSignedAgainByTheirAuthor(t *testing.T) {
	w := newRotWorld(t)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.house))
	must(t, w.alice.SetField(bg, w.house, NewRecordID(), "title", []byte("Late")))
	must(t, w.carol.SetField(bg, w.house, NewRecordID(), "title", []byte("Gone")))
	syncAll(t, w.bob)
	if titles(w.bob, w.house)["Late"] {
		t.Fatal("refused in the replaced epoch")
	}
	syncAll(t, w.alice)
	if got := titles(w.alice, w.house); !sameSet(got, set("Milk", "Bread", "Late")) {
		t.Fatal("Alice", got)
	}
	syncAll(t, w.bob)
	if got := titles(w.bob, w.house); !sameSet(got, set("Milk", "Bread", "Late")) {
		t.Fatal("signed again into the new epoch", got)
	}
	syncAll(t, w.carol)
	if !titles(w.carol, w.house)["Gone"] {
		t.Fatal("Carol's replica keeps her edit")
	}
	if w.carol.Role(w.house) != "" {
		t.Fatal("Carol's role")
	}
	syncAll(t, w.alice)
	if got := titles(w.alice, w.house); !sameSet(got, set("Milk", "Bread", "Late")) {
		t.Fatal("signed again once, not on every sync", got)
	}
}

func isCrash(err error) bool {
	var c *crashError
	return errors.As(err, &c)
}

// The owner's device dies at every crash point; started again, it finishes or tells the user nothing happened.
func TestRotationACrashAtEveryPointResumesOrDrops(t *testing.T) {
	for _, point := range []string{"1:chosen", "2:journal", "2:note", "3:roster", "4:announced", "4:home", "5:grant", "6:cleaned"} {
		t.Run(point, func(t *testing.T) {
			w := newRotWorld(t)
			w.bob.setCrashPoint(point)
			if err := w.bob.RemoveMember(bg, w.carolID, w.house); !isCrash(err) {
				t.Fatal("no crash", err)
			}
			restarted := open(t, w.f, w.bobKeys, w.bobStore, w.hub.Connect())
			must(t, restarted.Resume(bg))
			if point == "1:chosen" {
				// Nothing was journalled: the change didn't happen and the user makes it again.
				if !memberOf(restarted, w.house, w.carolID) {
					t.Fatal("Carol was removed")
				}
				must(t, restarted.RemoveMember(bg, w.carolID, w.house))
			}
			checkRemoved(t, w, restarted, w.bobStore)
		})
	}
}

func memberOf(d *Device, list, uid []byte) bool {
	for _, m := range d.Members(list) {
		if bytes.Equal(m.UID, uid) {
			return true
		}
	}
	return false
}

// Bob's iPhone dies with the change uploaded; his Mac finishes it. With nothing uploaded, it discards it.
func TestRotationAnotherOwnerDeviceFinishesOrDiscards(t *testing.T) {
	w := newRotWorld(t)
	mac := open(t, w.f, w.bobKeys, nil, w.hub.Connect())
	syncAll(t, mac)
	w.bob.setCrashPoint("3:roster")
	if err := w.bob.RemoveMember(bg, w.carolID, w.house); !isCrash(err) {
		t.Fatal(err)
	}
	syncAll(t, mac)
	note := mac.PendingChange(w.house)
	if note == nil || note.What != "remove a member" || note.Device != "Bob’s iPhone" {
		t.Fatal(note)
	}
	var pending *PendingError
	if err := mac.Delete(bg, w.house); !errors.As(err, &pending) {
		t.Fatal("blocked by the note", err)
	}
	must(t, mac.Finish(bg, w.house, note))
	checkRemoved(t, w, mac, nil)
	// The iPhone comes back: its change was made, by the Mac; it carries on without asking.
	phone := open(t, w.f, w.bobKeys, w.bobStore, w.hub.Connect())
	must(t, phone.Resume(bg))
	if steps := try(w.bobStore.JournalSteps()); len(steps) != 0 {
		t.Fatal(steps)
	}
	if phone.IsDead(w.house) {
		t.Fatal("dead")
	}
}

func TestRotationDiscardingAChangeThatNeverLeft(t *testing.T) {
	w := newRotWorld(t)
	mac := open(t, w.f, w.bobKeys, nil, w.hub.Connect())
	syncAll(t, mac)
	w.bob.setCrashPoint("2:note")
	if err := w.bob.RemoveMember(bg, w.carolID, w.house); !isCrash(err) {
		t.Fatal(err)
	}
	syncAll(t, mac)
	note := mac.PendingChange(w.house)
	if note == nil {
		t.Fatal("no note")
	}
	if err := mac.Finish(bg, w.house, note); !errors.Is(err, ErrCantFinish) {
		t.Fatal("nothing to finish", err)
	}
	must(t, mac.Discard(bg, w.house, note))
	phone := open(t, w.f, w.bobKeys, w.bobStore, w.hub.Connect())
	if err := phone.Resume(bg); !errors.Is(err, ErrSuperseded) {
		t.Fatal("the user is told", err)
	}
	if steps := try(w.bobStore.JournalSteps()); len(steps) != 0 {
		t.Fatal(steps)
	}
	syncAll(t, w.carol)
	if w.carol.Role(w.house) != Writer {
		t.Fatal("nothing changed")
	}
	must(t, mac.RemoveMember(bg, w.carolID, w.house))
	checkRemoved(t, w, mac, nil)
}

func keyCount(d *Device, list []byte) int {
	defer d.read()()
	return len(d.collections[string(list)].keys)
}

func TestRotationLoweringARoleChangesTheKeyAndPromotingDoesnt(t *testing.T) {
	w := newRotWorld(t)
	before := keyCount(w.bob, w.house)
	must(t, w.bob.SetRole(bg, Reader, w.aliceID, w.house))
	lowered := keyCount(w.bob, w.house)
	if lowered != before+1 {
		t.Fatal(before, lowered)
	}
	syncAll(t, w.alice)
	if err := w.alice.SetField(bg, w.house, NewRecordID(), "t", []byte{1}); err == nil {
		t.Fatal("reader")
	}
	if got := titles(w.alice, w.house); !sameSet(got, set("Milk", "Bread")) {
		t.Fatal("a reader still reads, under the new key", got)
	}
	must(t, w.bob.SetRole(bg, Writer, w.aliceID, w.house))
	if keyCount(w.bob, w.house) != lowered {
		t.Fatal("a promotion keeps the epoch")
	}
	syncAll(t, w.alice)
	must(t, w.alice.SetField(bg, w.house, NewRecordID(), "title", []byte("Tea")))
	syncAll(t, w.bob)
	if !titles(w.bob, w.house)["Tea"] {
		t.Fatal("Tea")
	}
}

// §6.4 step 3: a member's device is revoked; writing waits until the owner changes the key, which leaves the revoked device out.
func TestRotationRevokedMemberDeviceWaitsForTheOwnersRekey(t *testing.T) {
	w := newRotWorld(t)
	tablet := pairApproved(t, w.alice, w.bob, w.f, w.hub, "Alice’s tablet")
	if got := titles(tablet, w.house); !sameSet(got, set("Milk", "Bread")) {
		t.Fatal("tablet sees", got)
	}
	tabletCert := me(t, tablet).Certificate
	must(t, w.bob.Revoke(bg, tabletCert, w.f.ManagementSeed))
	if n := w.bob.ListsNeedingRekey(); len(n) != 1 || !bytes.Equal(n[0], w.house) {
		t.Fatal(n)
	}
	syncAll(t, w.alice)
	if err := w.alice.SetField(bg, w.house, NewRecordID(), "t", []byte{1}); err == nil {
		t.Fatal("exposed")
	}
	must(t, w.bob.Rekey(bg, w.house))
	if n := w.bob.ListsNeedingRekey(); len(n) != 0 {
		t.Fatal(n)
	}
	syncAll(t, w.alice)
	must(t, w.alice.SetField(bg, w.house, NewRecordID(), "title", []byte("Soap")))
	syncAll(t, w.bob)
	if !titles(w.bob, w.house)["Soap"] {
		t.Fatal("Soap")
	}
	syncAll(t, tablet)
	if titles(tablet, w.house)["Soap"] {
		t.Fatal("the revoked tablet gets no key to the new epoch")
	}
}

func try2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

// The owner's device merges the writes it carried into the new channel itself, though they reach it
// before its statement is announced: they wait for the statement instead of being refused (§8.3,
// §9.4). Otherwise it can't tell what those messages hold, to collect them (§10.7, §11.9).
func TestRotationOwnCarriedWritesAreMerged(t *testing.T) {
	w := newRotWorld(t)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.house))
	syncAll(t, w.bob, w.alice, w.bob)
	epoch, key := epochKey(w.bob, w.house)
	tag := tandem.CollectionTag(key, w.f.Pin.Hub, epoch)
	data := 0
	for _, m := range w.hub.Messages() {
		if m.Channel != tag {
			continue
		}
		_, p := try2(tandem.Open(key, w.f.Pin.Hub, tag, m.Body))
		state, frag, err := tandem.ParseKind1(p, w.f.Pin.Hub, w.house)
		if err != nil || frag != nil || bytes.Equal(state.Record, rosterRecord) {
			continue
		}
		data++
		if len(try(w.bob.store.Contents(m.ID))) != len(state.Writes) {
			t.Fatal("the owner's device didn't merge a message it carried")
		}
	}
	if data == 0 {
		t.Fatal("nothing carried")
	}
}
