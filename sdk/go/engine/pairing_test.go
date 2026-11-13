package engine

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// §6.3: Alice's phone shows a link, her tablet (no shared keychain) claims through it, and
// Bob, the admin, approves or rejects.

type pairWorld struct {
	hub     *MemoryHub
	f       *Founding
	bobKeys *tandem.DeviceKeys
	bob     *Device
	alice   *Device
	aliceID []byte
}

func newPairWorld(t *testing.T) pairWorld {
	hub, f, bobKeys, bob := found(t, "Bob’s phone", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	syncAll(t, bob)
	return pairWorld{hub, f, bobKeys, bob, alice, me(t, alice).Participant}
}

func pair(t *testing.T, link *PairingLink, label string, hub *MemoryHub) *Device {
	t.Helper()
	d := try(Pair(bg, link, newKeys(t), label, MemoryStore(), hub.Connect()))
	t.Cleanup(d.Close)
	return d
}

func TestPairingApprovedTabletJoinsAlicesHomeAndLists(t *testing.T) {
	w := newPairWorld(t)
	must(t, w.alice.SetHome(bg, "settings:liszto", "order", []byte{7}))
	groceries := try(w.alice.CreateList(bg, "Groceries"))
	milk := NewRecordID()
	must(t, w.alice.SetField(bg, groceries, milk, "title", []byte("Milk")))

	link := try(w.alice.StartPairing(bg, "Tablet", account, 0))
	scanned := try(ParsePairingLink(link.JSON()))
	if !bytes.Equal(scanned.Participant, w.aliceID) {
		t.Fatal("participant")
	}
	tablet := pair(t, scanned, "Alice’s tablet", w.hub)
	if !tablet.AwaitingApproval() {
		t.Fatal("the tablet says “Waiting for the hub admin”")
	}
	if len(tablet.Lists()) != 0 {
		t.Fatal("lists before approval")
	}
	syncAll(t, w.bob)
	claims := w.bob.PendingClaims()
	if len(claims) != 1 || !bytes.Equal(claims[0].Participant(), w.aliceID) || claims[0].Label() != "Alice’s tablet" {
		t.Fatal(claims)
	}
	must(t, w.bob.Approve(bg, claims[0].ID(), w.f.ManagementSeed))
	if len(w.bob.PendingClaims()) != 0 {
		t.Fatal("still pending")
	}
	syncAll(t, tablet)
	if tablet.AwaitingApproval() {
		t.Fatal("still waiting")
	}
	tm := me(t, tablet)
	if !bytes.Equal(tm.Participant, w.aliceID) {
		t.Fatal("the same person, not a second participant")
	}
	if !bytes.Equal(homeValue(tablet, "settings:liszto", "order"), []byte{7}) {
		t.Fatal("home")
	}
	if ls := tablet.Lists(); len(ls) != 1 || !bytes.Equal(ls[0].ID, groceries) {
		t.Fatal(ls)
	}
	if value_(tablet, groceries, milk, "title") != "Milk" {
		t.Fatal("Milk")
	}
	// Its writes reach Alice's phone, and Bob's device accepts the certificate.
	bread := NewRecordID()
	must(t, tablet.SetField(bg, groceries, bread, "title", []byte("Bread")))
	syncAll(t, w.alice)
	if value_(w.alice, groceries, bread, "title") != "Bread" {
		t.Fatal("Bread")
	}
	syncAll(t, w.bob)
	expect(t, w.bob.Registry().Activity(tm.Certificate), valid(), "Bob's view")
	// Alice's history records the completed pairing.
	syncAll(t, w.alice)
	if o := w.alice.HomeElements("pairing:"+tandem.B64u(link.Token), "outcome"); len(o) != 1 {
		t.Fatal(o)
	}
}

func TestPairingRejectedClaimGetsNothing(t *testing.T) {
	w := newPairWorld(t)
	link := try(w.alice.StartPairing(bg, "Tablet", account, 0))
	tablet := pair(t, link, "Someone’s tablet", w.hub)
	syncAll(t, w.bob)
	claims := w.bob.PendingClaims()
	if len(claims) != 1 {
		t.Fatal(claims)
	}
	claim := claims[0]
	must(t, w.bob.Reject(bg, claim.ID(), w.f.ManagementSeed))
	if len(w.bob.PendingClaims()) != 0 {
		t.Fatal("still pending")
	}
	// Bob's other device sees the rejection too.
	laptop := open(t, w.f, w.bobKeys, nil, w.hub.Connect())
	syncAll(t, laptop)
	if len(laptop.PendingClaims()) != 0 {
		t.Fatal("a rejection is in the directory, not only on the device that made it")
	}
	syncAll(t, tablet)
	if !tablet.AwaitingApproval() || tablet.Me() != nil {
		t.Fatal("the tablet got in")
	}
	if err := w.bob.Approve(bg, claim.ID(), bytes.Repeat([]byte{5}, 32)); err == nil {
		t.Fatal("another hub's seed")
	}
}

func TestPairingCancelledOrExpiredLinksCanNotBeApproved(t *testing.T) {
	w := newPairWorld(t)
	link := try(w.alice.StartPairing(bg, "Tablet", account, 0))
	pair(t, link, "Tablet", w.hub)
	must(t, w.alice.CancelPairing(bg, link.Token))
	syncAll(t, w.bob)
	if len(w.bob.PendingClaims()) != 0 {
		t.Fatal("Alice cancelled it")
	}
	late := try(w.alice.StartPairing(bg, "Old", account, -time.Second))
	if _, err := Pair(bg, late, newKeys(t), "x", MemoryStore(), w.hub.Connect()); err == nil {
		t.Fatal("an expired link")
	}
}

func TestPairingTamperedLinkOrClaimIsRefused(t *testing.T) {
	w := newPairWorld(t)
	link := try(w.alice.StartPairing(bg, "Tablet", account, 0))
	wrongL := *link
	wrongL.Secret = bytes.Repeat([]byte{1}, 32)
	if _, err := Pair(bg, &wrongL, newKeys(t), "x", MemoryStore(), w.hub.Connect()); err == nil {
		t.Fatal("a wrong L reads no pairing")
	}
	otherPerson := *link
	otherPerson.Participant = w.f.Participant
	if _, err := Pair(bg, &otherPerson, newKeys(t), "x", MemoryStore(), w.hub.Connect()); err == nil {
		t.Fatal("a link can't be turned into another participant's")
	}
}

// The admin crashes after journalling the certificate and release but before they leave:
// syncing again sends the same bytes, and the tablet completes (§6.3 step 4).
func TestPairingInterruptedApprovalFinishesWithTheSameBytes(t *testing.T) {
	w := newPairWorld(t)
	link := try(w.alice.StartPairing(bg, "Tablet", account, 0))
	tablet := pair(t, link, "Tablet", w.hub)
	bobStore := MemoryStore()
	bob := open(t, w.f, w.bobKeys, bobStore, failingAppends{w.hub.Connect()})
	syncAll(t, bob)
	claims := bob.PendingClaims()
	if len(claims) != 1 {
		t.Fatal(claims)
	}
	if err := bob.Approve(bg, claims[0].ID(), w.f.ManagementSeed); err == nil {
		t.Fatal("the hub went away")
	}
	if steps := try(bobStore.JournalSteps()); len(steps) != 1 || steps[0] != "approve."+hexs(claims[0].ID()) {
		t.Fatal(steps)
	}
	again := open(t, w.f, w.bobKeys, bobStore, w.hub.Connect())
	syncAll(t, again)
	if steps := try(bobStore.JournalSteps()); len(steps) != 0 {
		t.Fatal(steps)
	}
	syncAll(t, tablet)
	if !bytes.Equal(me(t, tablet).Participant, w.aliceID) || tablet.AwaitingApproval() {
		t.Fatal("not paired")
	}
}

// failingAppends is a connection whose appends fail, as if the network dropped right after the journal was written.
type failingAppends struct{ *MemoryConn }

func (failingAppends) Append(context.Context, string, []byte) (string, error) {
	return "", errors.New("offline")
}
