package engine

import (
	"bytes"
	"testing"
	"time"
)

// The whole path of §13.1 and §11: invite, join, share, offer, accept, edit both ways.
func TestSharingInviteJoinShareAccept(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	link := try(bob.Invite(bg, "Alice", account, f.ManagementSeed, 0))
	received := try(ParseInvitationLink(try(link.JSON()))) // as if scanned from a QR code
	if received.Account != account {
		t.Fatal(received.Account)
	}
	aliceKeys := newKeys(t)
	alice := try(Join(bg, received, aliceKeys, "Alice’s iPhone", MemoryStore(), hub.Connect()))
	defer alice.Close()
	aliceMe := me(t, alice)
	aliceID := aliceMe.Participant
	if bytes.Equal(aliceID, f.Participant) {
		t.Fatal("same participant")
	}
	must(t, alice.SetHome(bg, "settings:liszto", "order", []byte{1})) // her own home works
	syncAll(t, bob)
	expect(t, bob.Registry().Activity(aliceMe.Certificate), valid(), "Bob's device accepts Alice's admission")

	groceries := try(bob.CreateList(bg, "Groceries"))
	milk := NewRecordID()
	must(t, bob.SetField(bg, groceries, milk, "title", []byte("Milk")))
	must(t, bob.Share(bg, groceries, aliceID, Writer, "Alice"))

	syncAll(t, alice)
	offers := alice.PendingOffers()
	if len(offers) != 1 || !bytes.Equal(offers[0].List, groceries) || offers[0].Name != "Groceries" || !bytes.Equal(offers[0].Owner, f.Participant) {
		t.Fatal(offers)
	}
	if len(alice.Lists()) != 0 {
		t.Fatal("nothing appears without her saying so (§11.2)")
	}
	must(t, alice.Accept(bg, groceries))
	if ls := alice.Lists(); len(ls) != 1 || !bytes.Equal(ls[0].ID, groceries) {
		t.Fatal(ls)
	}
	if len(alice.PendingOffers()) != 0 {
		t.Fatal("still offered")
	}
	if value_(alice, groceries, milk, "title") != "Milk" {
		t.Fatal("Milk")
	}
	bread := NewRecordID()
	must(t, alice.SetField(bg, groceries, bread, "title", []byte("Bread")))
	syncAll(t, bob)
	if value_(bob, groceries, bread, "title") != "Bread" {
		t.Fatal("Bread at Bob")
	}
	// Alice's Mac, on her synced keychain, gets the list from her home with no grant of its own.
	aliceMac := open(t, f, aliceKeys, nil, hub.Connect())
	syncAll(t, aliceMac)
	if ls := aliceMac.Lists(); len(ls) != 1 || !bytes.Equal(ls[0].ID, groceries) {
		t.Fatal(ls)
	}
	if value_(aliceMac, groceries, bread, "title") != "Bread" {
		t.Fatal("Bread at the Mac")
	}
}

func TestSharingABadOrExpiredInvitationAdmitsNobody(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	link := try(bob.Invite(bg, "Alice", account, f.ManagementSeed, 0))
	tampered := *link
	tampered.Secret = bytes.Repeat([]byte{1}, 32)
	if _, err := Join(bg, &tampered, newKeys(t), "x", MemoryStore(), hub.Connect()); err == nil {
		t.Fatal("a wrong secret")
	}
	expired := try(bob.Invite(bg, "Late", account, f.ManagementSeed, -time.Second))
	if _, err := Join(bg, expired, newKeys(t), "x", MemoryStore(), hub.Connect()); err == nil {
		t.Fatal("an expired invitation")
	}
	if _, err := bob.Invite(bg, "x", account, bytes.Repeat([]byte{3}, 32), 0); err == nil {
		t.Fatal("another hub's management seed")
	}
}

// A reader can read but not write; a non-owner can't share.
func TestSharingRolesHold(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	carol := join(t, bob, f, hub, "Carol", nil)
	syncAll(t, bob)
	house := try(bob.CreateList(bg, "House"))
	carolID := me(t, carol).Participant
	must(t, bob.Share(bg, house, carolID, Reader, ""))
	syncAll(t, carol)
	must(t, carol.Accept(bg, house))
	if err := carol.SetField(bg, house, NewRecordID(), "t", []byte{1}); err == nil {
		t.Fatal("a reader wrote")
	}
	if err := carol.Share(bg, house, f.Participant, Writer, ""); err == nil {
		t.Fatal("a non-owner shared")
	}
}
