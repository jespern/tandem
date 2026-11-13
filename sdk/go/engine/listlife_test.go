package engine

import (
	"bytes"
	"slices"
	"testing"
)

// What Liszto does with lists day to day: items with sets and conflicts, people by name, answers, leaving, deleting.

func lifeHub(t *testing.T) (*MemoryHub, *Founding, *Device, *Device, []byte) {
	hub, f, _, bob := found(t, "Bob’s iPhone", nil)
	must(t, bob.SetName(bg, "Bob"))
	link := try(bob.Invite(bg, "Alice", account, f.ManagementSeed, 0))
	alice := try(Join(bg, link, newKeys(t), "Alice’s iPhone", MemoryStore(), hub.Connect()))
	t.Cleanup(alice.Close)
	must(t, alice.SetName(bg, "Alice"))
	syncAll(t, bob)
	return hub, f, bob, alice, me(t, alice).Participant
}

func TestListLifePeopleHaveNamesAndDevices(t *testing.T) {
	_, f, bob, alice, aliceID := lifeHub(t)
	syncAll(t, alice)
	people := alice.People()
	var names []string
	for _, p := range people {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"Alice", "Bob"}) {
		t.Fatal(names)
	}
	for _, p := range people {
		if p.IsMe != bytes.Equal(p.ID, aliceID) || p.IsCreator != bytes.Equal(p.ID, f.Participant) {
			t.Fatal(p)
		}
		if p.IsMe && (len(p.Devices) != 1 || p.Devices[0].Label != "Alice’s iPhone") {
			t.Fatal(p.Devices)
		}
	}
	if bob.NameOf(aliceID) != "Alice" {
		t.Fatal("Bob's view")
	}
	must(t, alice.SetName(bg, "Alice B."))
	syncAll(t, bob)
	if bob.NameOf(aliceID) != "Alice B." {
		t.Fatal("renamed")
	}
}

func TestListLifeItemsSetsConflictsAndAttribution(t *testing.T) {
	_, _, bob, alice, aliceID := lifeHub(t)
	list := try(bob.CreateList(bg, "Groceries"))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	syncAll(t, alice)
	must(t, alice.Accept(bg, list))
	milk := NewRecordID()
	must(t, bob.SetField(bg, list, milk, "title", []byte("Milk")))
	must(t, bob.Add(bg, list, milk, "tags", []byte("dairy")))
	syncAll(t, alice)
	// Concurrent edits of one note, and a concurrent remove and add of one tag.
	must(t, alice.SetField(bg, list, milk, "notes", []byte("oat")))
	must(t, bob.SetField(bg, list, milk, "notes", []byte("whole")))
	must(t, alice.Remove(bg, list, milk, "tags", []byte("dairy")))
	must(t, bob.Add(bg, list, milk, "tags", []byte("cold")))
	syncAll(t, alice, bob)
	for _, d := range []*Device{alice, bob} {
		item, ok := d.Item(list, milk)
		if !ok || item.Text("title") != "Milk" {
			t.Fatal("item")
		}
		notes := map[string]bool{}
		for _, v := range item.Values("notes") {
			notes[string(v.Value)] = true
		}
		if !sameSet(notes, set("oat", "whole")) {
			t.Fatal("both texts of a conflicting note", notes)
		}
		if tags := item.Elements("tags"); len(tags) != 1 || string(tags[0]) != "cold" {
			t.Fatal("Alice saw dairy and removed it", tags)
		}
		if !slices.Equal(item.Fields(), []string{"notes", "tags", "title"}) {
			t.Fatal(item.Fields())
		}
		if item.LastModifiedBy() == nil {
			t.Fatal("attribution")
		}
	}
	if alice.Name(list) != "Groceries" {
		t.Fatal("name")
	}
	must(t, bob.Rename(bg, list, "Food"))
	if err := alice.Rename(bg, list, "Mine"); err == nil {
		t.Fatal("only the owner renames")
	}
	syncAll(t, alice)
	if alice.Name(list) != "Food" {
		t.Fatal("renamed")
	}
}

func statuses(ms []MemberState) []MemberStatus {
	var out []MemberStatus
	for _, m := range ms {
		out = append(out, m.Status)
	}
	return out
}

func TestListLifeDeclineLeaveAndTheOwnerSeesIt(t *testing.T) {
	_, _, bob, alice, aliceID := lifeHub(t)
	house := try(bob.CreateList(bg, "House"))
	must(t, bob.Share(bg, house, aliceID, Writer, ""))
	if s := statuses(bob.MemberStatus(house)); !slices.Equal(s, []MemberStatus{StatusPending}) {
		t.Fatal(s)
	}
	syncAll(t, alice)
	must(t, alice.Decline(bg, house))
	if len(alice.Lists()) != 0 || len(alice.PendingOffers()) != 0 {
		t.Fatal("a declined offer isn't asked again")
	}
	syncAll(t, bob)
	if s := statuses(bob.MemberStatus(house)); !slices.Equal(s, []MemberStatus{StatusDeclined}) {
		t.Fatal(s)
	}
	garden := try(bob.CreateList(bg, "Garden"))
	must(t, bob.Share(bg, garden, aliceID, Writer, ""))
	syncAll(t, alice)
	must(t, alice.Accept(bg, garden))
	syncAll(t, bob)
	if s := statuses(bob.MemberStatus(garden)); !slices.Equal(s, []MemberStatus{StatusAccepted}) {
		t.Fatal(s)
	}
	must(t, alice.Leave(bg, garden))
	if len(alice.Lists()) != 0 {
		t.Fatal("she stops following it")
	}
	syncAll(t, bob)
	if s := statuses(bob.MemberStatus(garden)); !slices.Equal(s, []MemberStatus{StatusLeft}) {
		t.Fatal("Alice left Garden", s)
	}
	must(t, bob.RemoveMember(bg, aliceID, garden))
	if s := bob.MemberStatus(garden); len(s) != 0 {
		t.Fatal(s)
	}
}

func TestListLifeDeletingAList(t *testing.T) {
	_, _, bob, alice, aliceID := lifeHub(t)
	list := try(bob.CreateList(bg, "Old"))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	syncAll(t, alice)
	must(t, alice.Accept(bg, list))
	must(t, bob.Delete(bg, list))
	if len(bob.Lists()) != 0 {
		t.Fatal("Bob's lists")
	}
	syncAll(t, alice)
	if !alice.IsDeleted(list) || len(alice.Lists()) != 0 {
		t.Fatal("Alice's lists")
	}
	if err := alice.SetField(bg, list, NewRecordID(), "t", []byte{1}); err == nil {
		t.Fatal("deleted")
	}
	if err := bob.Share(bg, list, aliceID, Reader, ""); err == nil {
		t.Fatal("deleted")
	}
}
