package engine

import (
	"bytes"
	"testing"
)

// The creator founds a hub; another device that knows only the genesis and directory key reads it back.
func TestDeviceFoundAndRead(t *testing.T) {
	hub, f, _, _ := found(t, "Mac", nil)
	if n := len(hub.Messages()); n != 6 { // genesis, certificate, snapshot, recovery box; the home epoch; the inbox delivery
		t.Fatal(n)
	}
	reader := open(t, f, nil, nil, hub.Connect())
	syncAll(t, reader)
	expect(t, reader.Registry().Activity(oid(f.Certificate)), valid(), "certificate")
	expect(t, reader.Registry().Authorizes(oid(f.Snapshot), oid(f.Certificate)), valid(), "snapshot")
	if ns := reader.Notices(); len(ns) != 1 || ns[0].Type != "home-recovery" {
		t.Fatal(ns)
	}
	if n := try(reader.Sync(bg)); n != 0 {
		t.Fatal("a second sync applied", n)
	}
}

// The hub password unlocks management on a device that never held it (§6.7). Three 1 GiB stretches.
func TestDeviceTheHubPasswordUnlocksManagement(t *testing.T) {
	skipArgon(t)
	hub, keys := NewMemoryHub(), newKeys(t)
	f := try(Found(account, keys, "Mac", nil))
	f.Capsule = try(NewCapsule("correct horse battery staple", f.Pin, f.ManagementSeed, nil))
	creator := open(t, f, keys, nil, hub.Connect())
	must(t, creator.Publish(bg, f))
	reader := open(t, f, nil, nil, hub.Connect())
	syncAll(t, reader)
	if ms := try(reader.Unlock("correct horse battery staple")); !bytes.Equal(ms, f.ManagementSeed) {
		t.Fatal("MS")
	}
	if _, err := reader.Unlock("wrong horse battery staple"); err == nil {
		t.Fatal("wrong password")
	}
}

// A crash after journalling, before or between appends, resumes with the same bytes and no duplicates.
func TestDevicePublishingResumesWithoutDuplicates(t *testing.T) {
	store := MemoryStore()
	hub, f, _, d := found(t, "Mac", store)
	var envelopes []tagged
	for _, m := range hub.Messages() {
		envelopes = append(envelopes, tagged{m.Channel, m.Body})
	}
	must(t, store.SetJournal("found", pack(envelopes)))
	must(t, d.Publish(bg, f))
	if n := len(hub.Messages()); n != 6 {
		t.Fatal(n)
	}
	if _, ok := store.Journal("found"); ok {
		t.Fatal("journal")
	}
}

func TestDeviceAStoreBelongsToOneHub(t *testing.T) {
	store := MemoryStore()
	a := try(Found(account, newKeys(t), "Mac", nil))
	b := try(Found(account, newKeys(t), "Mac", nil))
	hub := NewMemoryHub()
	open(t, a, nil, store, hub.Connect())
	other := NewMemoryHub().Connect()
	if _, err := Open(b.Genesis, b.DirectoryKey, nil, store, other); err == nil {
		t.Fatal("another hub's genesis")
	}
	if _, err := Open(a.Genesis, b.DirectoryKey, nil, MemoryStore(), other); err == nil {
		t.Fatal("another directory key")
	}
}

// Reopen finds the hub a store belongs to.
func TestDeviceReopen(t *testing.T) {
	store := MemoryStore()
	hub, f, keys, _ := found(t, "Mac", store)
	d := try(Reopen(store, keys, hub.Connect()))
	if d == nil || !bytes.Equal(me(t, d).Participant, f.Participant) {
		t.Fatal("reopen")
	}
	d.Close()
	if d, err := Reopen(MemoryStore(), keys, hub.Connect()); d != nil || err != nil {
		t.Fatal("a new store")
	}
}
