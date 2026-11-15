package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
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

// switchedAppends fails appends while offline is set.
type switchedAppends struct {
	*MemoryConn
	offline *atomic.Bool
}

func (s switchedAppends) Append(ctx context.Context, channel string, body []byte) (string, error) {
	if s.offline.Load() {
		return "", errors.New("offline")
	}
	return s.MemoryConn.Append(ctx, channel, body)
}

type sharing struct {
	hub             *MemoryHub
	f               *Founding
	bob, alice      *Device
	store           *Store
	aliceKeys       *tandem.DeviceKeys
	groceries, milk []byte
	offline         *atomic.Bool
}

// sharedWithAlice has Bob share Groceries, with Milk in it, with Alice, whose device keeps its store in a file and
// reaches the hub through a connection that fails while offline is set.
func sharedWithAlice(t *testing.T) *sharing {
	t.Helper()
	s := &sharing{offline: &atomic.Bool{}}
	s.hub, s.f, _, s.bob = found(t, "Bob’s Mac", nil)
	t.Cleanup(s.bob.Close)
	link := try(s.bob.Invite(bg, "Alice", account, s.f.ManagementSeed, 0))
	s.aliceKeys, s.store = newKeys(t), try(OpenStore(filepath.Join(t.TempDir(), "alice.sqlite")))
	t.Cleanup(func() { s.store.Close() })
	s.alice = try(Join(bg, link, s.aliceKeys, "Alice’s iPhone", s.store, switchedAppends{s.hub.Connect(), s.offline}))
	syncAll(t, s.bob)
	s.groceries = try(s.bob.CreateList(bg, "Groceries"))
	s.milk = NewRecordID()
	must(t, s.bob.SetField(bg, s.groceries, s.milk, "title", []byte("Milk")))
	must(t, s.bob.Share(bg, s.groceries, me(t, s.alice).Participant, Writer, "Alice"))
	syncAll(t, s.alice)
	if o := s.alice.PendingOffers(); len(o) != 1 {
		t.Fatal(o)
	}
	return s
}

// An offer received but not answered survives a restart, and accepting it then shows the list whole.
func TestAppAnOfferSurvivesARestart(t *testing.T) {
	s := sharedWithAlice(t)
	s.alice.Close()
	again := try(Reopen(s.store, s.aliceKeys, s.hub.Connect()))
	defer again.Close()
	syncAll(t, again)
	if o := again.PendingOffers(); len(o) != 1 || !eq(o[0].List, s.groceries) {
		t.Fatal("the offer is gone after a restart", o)
	}
	must(t, again.Accept(bg, s.groceries))
	if ls := again.Lists(); len(ls) != 1 || ls[0].Name != "Groceries" {
		t.Fatal("the list and its name", ls)
	}
	if value_(again, s.groceries, s.milk, "title") != "Milk" {
		t.Fatal("the items written before the share")
	}
}

// A crash in the middle of accepting leaves the offer open: the answer, which closes it, is written last.
func TestAppACrashWhileAcceptingLeavesTheOfferOpen(t *testing.T) {
	s := sharedWithAlice(t)
	s.alice.setCrashPoint("accept:keys")
	var c *crashError
	if err := s.alice.Accept(bg, s.groceries); !errors.As(err, &c) {
		t.Fatal("expected the crash", err)
	}
	s.alice.Close()
	again := try(Reopen(s.store, s.aliceKeys, s.hub.Connect()))
	defer again.Close()
	if o := again.PendingOffers(); len(o) != 1 {
		t.Fatal("the offer is closed", o)
	}
	must(t, again.Accept(bg, s.groceries))
	if ls := again.Lists(); len(ls) != 1 {
		t.Fatal(ls)
	}
}

// Accepting without the hub records the whole answer: the list follows on every device once back.
func TestAppAcceptingOfflineFollowsTheList(t *testing.T) {
	s := sharedWithAlice(t)
	s.offline.Store(true)
	must(t, s.alice.Accept(bg, s.groceries))
	if ls := s.alice.Lists(); len(ls) != 1 {
		t.Fatal(ls)
	}
	s.offline.Store(false)
	syncAll(t, s.alice)
	laptop := open(t, s.f, s.aliceKeys, nil, s.hub.Connect())
	defer laptop.Close()
	syncAll(t, laptop)
	if ls := laptop.Lists(); len(ls) != 1 || !eq(ls[0].ID, s.groceries) {
		t.Fatal("her other devices follow it too", ls)
	}
}

// Creating a list without the hub doesn't fail and leaves nothing half done: the next sync sends it all.
func TestAppCreatingAListOfflineSendsItLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	store := try(OpenStore(path))
	defer store.Close()
	hub, f, keys, online := found(t, "Bob’s iPhone", store)
	syncAll(t, online)
	online.Close()
	offline := try(Reopen(store, keys, failingAppends{hub.Connect()}))
	list := try(offline.CreateList(bg, "Weekend"))
	if ls := offline.Lists(); len(ls) != 1 || !eq(ls[0].ID, list) {
		t.Fatal(ls)
	}
	offline.Close()
	back := try(Reopen(store, keys, hub.Connect()))
	defer back.Close()
	syncAll(t, back)
	laptop := open(t, f, keys, nil, hub.Connect())
	defer laptop.Close()
	syncAll(t, laptop)
	if ls := laptop.Lists(); len(ls) != 1 || ls[0].Name != "Weekend" {
		t.Fatal("never went out", ls)
	}
}

// Home writes are local first, like list writes: offline they don't fail, and the next sync sends them.
func TestAppAHomeWriteOfflineIsSentLater(t *testing.T) {
	hub, f, keys, bob := found(t, "Bob’s Mac", nil)
	defer bob.Close()
	offline := &atomic.Bool{}
	phone := open(t, f, keys, nil, switchedAppends{hub.Connect(), offline})
	defer phone.Close()
	syncAll(t, phone)
	offline.Store(true)
	must(t, phone.SetHome(bg, "settings:liszto", "theme", []byte("dark")))
	if !eq(homeValue(phone, "settings:liszto", "theme"), []byte("dark")) {
		t.Fatal("not saved")
	}
	offline.Store(false)
	syncAll(t, phone, bob)
	if !eq(homeValue(bob, "settings:liszto", "theme"), []byte("dark")) {
		t.Fatal("healing never sent it")
	}
}

// Several fields of one record go out as one message, and arrive together.
func TestAppABatchedWriteIsOneMessage(t *testing.T) {
	hub, f, keys, bob := found(t, "Bob’s Mac", nil)
	defer bob.Close()
	list := try(bob.CreateList(bg, "Groceries"))
	before := len(hub.Messages())
	milk := NewRecordID()
	must(t, bob.Write(bg, list, milk, Set("title", []byte("Milk")), Set("notes", []byte("Oat")), Add("tags", []byte("dairy"))))
	if n := len(hub.Messages()) - before; n != 1 {
		t.Fatal("messages:", n)
	}
	laptop := open(t, f, keys, nil, hub.Connect())
	defer laptop.Close()
	syncAll(t, laptop)
	item, ok := laptop.Item(list, milk)
	if !ok || item.Text("title") != "Milk" || item.Text("notes") != "Oat" || len(item.Elements("tags")) != 1 {
		t.Fatal(item)
	}
	must(t, bob.Write(bg, list, milk, Delete()))
	syncAll(t, laptop)
	if item, _ := laptop.Item(list, milk); !item.Deleted() {
		t.Fatal("not deleted")
	}
	if err := bob.Write(bg, list, MetaRecord(), Delete()); err == nil {
		t.Fatal("a named record is never deleted")
	}
}

// The owner keeps the app's list properties in _meta; another device reads them, and only the owner writes them.
func TestAppTheOwnerWritesListPropertiesInMeta(t *testing.T) {
	s := sharedWithAlice(t)
	must(t, s.bob.Write(bg, s.groceries, MetaRecord(), Set("icon", []byte("cart")), Set("tagsEnabled", []byte("1"))))
	must(t, s.alice.Accept(bg, s.groceries))
	syncAll(t, s.alice)
	meta, ok := s.alice.Item(s.groceries, MetaRecord())
	if !ok || meta.Text("icon") != "cart" || meta.Text("tagsEnabled") != "1" || meta.Text("name") != "Groceries" {
		t.Fatal(meta)
	}
	if s.alice.Name(s.groceries) != "Groceries" {
		t.Fatal("the name stays where Rename and Lists read it")
	}
	if err := s.alice.Write(bg, s.groceries, MetaRecord(), Set("icon", []byte("x"))); err == nil {
		t.Fatal("a writer changed _meta")
	}
}

// pagesThenFail serves Changes from conn, and fails from the call numbered failAt on.
type pagesThenFail struct {
	*MemoryConn
	calls, failAt *atomic.Int32
}

func (p pagesThenFail) Changes(ctx context.Context, cursor string) (tandem.ChangeBatch, error) {
	if p.calls.Add(1) >= p.failAt.Load() {
		return tandem.ChangeBatch{}, errors.New("connection lost")
	}
	return p.MemoryConn.Changes(ctx, cursor)
}

// A hub that answers a page at a time: a new device syncs everything, page after page.
func TestAppSyncsAPageAtATime(t *testing.T) {
	hub, f, keys, bob := found(t, "Bob’s Mac", nil)
	defer bob.Close()
	list := try(bob.CreateList(bg, "Groceries"))
	for _, title := range []string{"Milk", "Eggs", "Bread", "Oats", "Tea"} {
		must(t, bob.SetField(bg, list, NewRecordID(), "title", []byte(title)))
	}
	hub.PageSize = 3
	calls, failAt := &atomic.Int32{}, &atomic.Int32{}
	failAt.Store(1 << 30)
	laptop := open(t, f, keys, nil, pagesThenFail{hub.Connect(), calls, failAt})
	defer laptop.Close()
	syncAll(t, laptop)
	if got := titles(laptop, list); !sameSet(got, set("Milk", "Eggs", "Bread", "Oats", "Tea")) {
		t.Fatal(got)
	}
	if n := len(hub.Messages()); calls.Load() < int32(n/3) {
		t.Fatal("not paged:", calls.Load(), "calls for", n, "messages")
	}
}

// A sync that stops partway keeps the pages it took: the next one goes on from there.
func TestAppAStoppedSyncKeepsItsPages(t *testing.T) {
	hub, f, keys, bob := found(t, "Bob’s Mac", nil)
	defer bob.Close()
	list := try(bob.CreateList(bg, "Groceries"))
	for _, title := range []string{"Milk", "Eggs", "Bread", "Oats", "Tea"} {
		must(t, bob.SetField(bg, list, NewRecordID(), "title", []byte(title)))
	}
	hub.PageSize = 3
	calls, failAt := &atomic.Int32{}, &atomic.Int32{}
	failAt.Store(3)
	store := MemoryStore()
	laptop := open(t, f, keys, store, pagesThenFail{hub.Connect(), calls, failAt})
	defer laptop.Close()
	if _, err := laptop.Sync(bg); err == nil {
		t.Fatal("expected the lost connection")
	}
	if c, _ := store.Value("cursor"); string(c) != "6" {
		t.Fatal("the two pages taken aren't kept:", string(c))
	}
	failAt.Store(1 << 30)
	syncAll(t, laptop)
	if got := titles(laptop, list); !sameSet(got, set("Milk", "Eggs", "Bread", "Oats", "Tea")) {
		t.Fatal(got)
	}
}
