package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// What an app needs around the engine: reopening after a restart, its keys, offline edits.

func TestAppReopenAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	store := try(OpenStore(path))
	hub, _, keys, app := found(t, "Bob’s iPhone", store)
	list := try(app.CreateList(bg, "Inbox"))
	must(t, app.SetField(bg, list, NewRecordID(), "title", []byte("Typed")))
	app.Close()
	must(t, store.Close())
	// A restart: the store alone opens the hub again, with everything in it.
	store = try(OpenStore(path))
	defer store.Close()
	again := try(Reopen(store, keys, hub.Connect()))
	defer again.Close()
	if ls := again.Lists(); len(ls) != 1 || ls[0].Name != "Inbox" {
		t.Fatal(ls)
	}
	if got := titles(again, list); !sameSet(got, set("Typed")) {
		t.Fatal(got)
	}
	must(t, again.SetField(bg, list, NewRecordID(), "title", []byte("After")))
	if d := try(Reopen(MemoryStore(), keys, hub.Connect())); d != nil {
		t.Fatal("a new store")
	}
}

func TestAppKeepsItsKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	keys := try(LoadOrCreateDeviceKeys(path))
	again := try(LoadOrCreateDeviceKeys(path))
	if !bytes.Equal(publicSign(keys), publicSign(again)) || !bytes.Equal(publicAgree(keys), publicAgree(again)) {
		t.Fatal("other keys")
	}
	if info := try(os.Stat(path)); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	parsed := try(ParseDeviceKeys(MarshalDeviceKeys(keys)))
	if !bytes.Equal(publicSign(parsed), publicSign(keys)) {
		t.Fatal("round trip")
	}
	if _, err := ParseDeviceKeys(make([]byte, 63)); err == nil {
		t.Fatal("short")
	}
}

// An edit made offline is saved; the next sync with a working connection sends it (§12.2 healing).
func TestAppAnEditMadeOfflineGoesOutOnTheNextSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	store := try(OpenStore(path))
	hub, f, keys, online := found(t, "Bob’s iPhone", store)
	list := try(online.CreateList(bg, "Inbox"))
	online.Close()
	offline := try(Reopen(store, keys, failingAppends{hub.Connect()}))
	item := NewRecordID()
	must(t, offline.SetField(bg, list, item, "title", []byte("On the train")))
	if value_(offline, list, item, "title") != "On the train" {
		t.Fatal("not saved")
	}
	offline.Close()
	back := try(Reopen(store, keys, hub.Connect()))
	defer back.Close()
	syncAll(t, back)
	laptop := open(t, f, keys, nil, hub.Connect())
	syncAll(t, laptop)
	if value_(laptop, list, item, "title") != "On the train" {
		t.Fatal("never went out")
	}
	store.Close()
}
