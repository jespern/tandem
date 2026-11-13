package engine

import (
	"bytes"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// §6.4 and §10.8: Bob, the admin, revokes Alice's tablet; her phone rekeys her home without it.

type revWorld struct {
	hub        *MemoryHub
	f          *Founding
	bob        *Device
	aliceKeys  *tandem.DeviceKeys
	phone      *Device
	tablet     *Device
	aliceID    []byte
	tabletCert []byte
}

// pairApproved pairs a new device with member's participant and has admin approve it.
func pairApproved(t *testing.T, member, admin *Device, f *Founding, hub *MemoryHub, label string) *Device {
	t.Helper()
	link := try(member.StartPairing(bg, label, account, 0))
	d := pair(t, link, label, hub)
	syncAll(t, admin)
	claims := admin.PendingClaims()
	if len(claims) == 0 {
		t.Fatal("no claim")
	}
	must(t, admin.Approve(bg, claims[0].ID(), f.ManagementSeed))
	syncAll(t, d)
	return d
}

func newRevWorld(t *testing.T) revWorld {
	hub, f, _, bob := found(t, "Bob’s phone", nil)
	aliceKeys := newKeys(t)
	phone := join(t, bob, f, hub, "Alice", aliceKeys)
	tablet := pairApproved(t, phone, bob, f, hub, "Alice’s tablet")
	syncAll(t, phone)
	return revWorld{hub, f, bob, aliceKeys, phone, tablet, me(t, phone).Participant, me(t, tablet).Certificate}
}

func sameIDs(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestRevocationRevokedTabletIsCutOutOfHome(t *testing.T) {
	w := newRevWorld(t)
	must(t, w.phone.SetHome(bg, "settings:liszto", "order", []byte{1}))
	syncAll(t, w.tablet)
	if !bytes.Equal(homeValue(w.tablet, "settings:liszto", "order"), []byte{1}) {
		t.Fatal("shared")
	}
	before := w.phone.HomeHeads()
	must(t, w.bob.Revoke(bg, w.tabletCert, w.f.ManagementSeed))
	syncAll(t, w.phone)
	// The tablet, not yet aware, writes a new record into the old epoch after the phone rekeyed.
	must(t, w.tablet.SetHome(bg, "settings:sneaky", "x", []byte{9}))
	after := w.phone.HomeHeads()
	if len(after) != 1 || sameIDs(after, before) {
		t.Fatal("the phone started a new home epoch")
	}
	if !bytes.Equal(homeValue(w.phone, "settings:liszto", "order"), []byte{1}) {
		t.Fatal("home carried over")
	}
	must(t, w.phone.SetHome(bg, "settings:liszto", "order", []byte{2}))
	syncAll(t, w.phone)
	if _, ok := w.phone.HomeValue("settings:sneaky", "x"); ok {
		t.Fatal("a late write in the replaced epoch doesn't enter the current home")
	}
	syncAll(t, w.tablet)
	if !w.tablet.IsRevoked() {
		t.Fatal("not revoked")
	}
	if !bytes.Equal(homeValue(w.tablet, "settings:liszto", "order"), []byte{1}) {
		t.Fatal("the tablet has no key to the new epoch")
	}
	if err := w.tablet.SetHome(bg, "settings:liszto", "order", []byte{3}); err == nil {
		t.Fatal("no home to write to")
	}
	if !bytes.Equal(homeValue(w.phone, "settings:liszto", "order"), []byte{2}) {
		t.Fatal("phone order")
	}
	expect(t, w.bob.Registry().Activity(w.tabletCert), invalid("revoked"), "Bob's view")
}

// A device paired after the rekey can't read the old channel: it gets the home through the new
// epoch's inventory and the copies of earlier starts, and the lists through the carried memberships.
func TestRevocationDevicePairedAfterTheRekeyReadsTheCarriedHome(t *testing.T) {
	w := newRevWorld(t)
	must(t, w.phone.SetHome(bg, "settings:liszto", "order", []byte{1}))
	groceries := try(w.phone.CreateList(bg, "Groceries"))
	milk := NewRecordID()
	must(t, w.phone.SetField(bg, groceries, milk, "title", []byte("Milk")))
	must(t, w.bob.Revoke(bg, w.tabletCert, w.f.ManagementSeed))
	syncAll(t, w.phone)
	laptop := pairApproved(t, w.phone, w.bob, w.f, w.hub, "Alice’s laptop")
	if !bytes.Equal(homeValue(laptop, "settings:liszto", "order"), []byte{1}) {
		t.Fatal("order")
	}
	if ls := laptop.Lists(); len(ls) != 1 || !bytes.Equal(ls[0].ID, groceries) {
		t.Fatal(ls)
	}
	if value_(laptop, groceries, milk, "title") != "Milk" {
		t.Fatal("Milk")
	}
	must(t, laptop.SetHome(bg, "settings:liszto", "order", []byte{4}))
	syncAll(t, w.phone)
	if !bytes.Equal(homeValue(w.phone, "settings:liszto", "order"), []byte{4}) {
		t.Fatal("seen")
	}
}

// Two installations on Alice's keychain rekey at the same moment: two heads, both followed, joined by the next.
func TestRevocationConcurrentRekeysAreJoined(t *testing.T) {
	w := newRevWorld(t)
	must(t, w.phone.SetHome(bg, "settings:liszto", "order", []byte{1}))
	mac := open(t, w.f, w.aliceKeys, nil, w.hub.Connect())
	syncAll(t, mac)
	must(t, w.phone.RotateHome(bg))
	must(t, mac.RotateHome(bg))
	must(t, w.phone.SetHome(bg, "settings:a", "x", []byte{1}))
	must(t, mac.SetHome(bg, "settings:b", "x", []byte{2}))
	syncAll(t, w.phone, mac)
	heads, macHeads := w.phone.HomeHeads(), mac.HomeHeads()
	if len(heads) != 2 || !sameIDs(heads, macHeads) {
		t.Fatal(len(heads), len(macHeads))
	}
	for _, d := range []*Device{w.phone, mac} {
		if !bytes.Equal(homeValue(d, "settings:a", "x"), []byte{1}) || !bytes.Equal(homeValue(d, "settings:b", "x"), []byte{2}) {
			t.Fatal("values")
		}
	}
	must(t, w.phone.RotateHome(bg))
	syncAll(t, mac)
	if len(mac.HomeHeads()) != 1 {
		t.Fatal("not joined")
	}
	if !bytes.Equal(homeValue(mac, "settings:a", "x"), []byte{1}) || !bytes.Equal(homeValue(mac, "settings:b", "x"), []byte{2}) {
		t.Fatal("values after the join")
	}
}

// The rekey is journalled; a crash before its messages leave finishes with the same bytes on the next sync.
func TestRevocationInterruptedRekeyFinishes(t *testing.T) {
	w := newRevWorld(t)
	must(t, w.phone.SetHome(bg, "settings:liszto", "order", []byte{1}))
	phoneStore := MemoryStore()
	flaky := open(t, w.f, w.aliceKeys, phoneStore, w.hub.Connect())
	syncAll(t, flaky)
	offline := open(t, w.f, w.aliceKeys, phoneStore, failingAppends{w.hub.Connect()})
	if err := offline.RotateHome(bg); err == nil {
		t.Fatal("offline")
	}
	if steps := try(phoneStore.JournalSteps()); len(steps) != 1 {
		t.Fatal(steps)
	}
	again := open(t, w.f, w.aliceKeys, phoneStore, w.hub.Connect())
	syncAll(t, again)
	if steps := try(phoneStore.JournalSteps()); len(steps) != 0 {
		t.Fatal(steps)
	}
	syncAll(t, w.phone)
	if !sameIDs(w.phone.HomeHeads(), again.HomeHeads()) {
		t.Fatal("heads")
	}
	if !bytes.Equal(homeValue(w.phone, "settings:liszto", "order"), []byte{1}) {
		t.Fatal("order")
	}
}
