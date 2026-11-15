package engine

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Garbage collection beyond §10.7 (gc.go): past epochs' writes, gone lists, and their files.

// pastWrites is the data writes a device holds from epochs before the current one that the current
// inventory doesn't list.
func pastWrites(t *testing.T, d *Device, list []byte) int {
	t.Helper()
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.collections[string(list)]
	current := c.epochOf(c.head())
	inv := d.inventories[string(current)]
	n := 0
	for _, row := range try(d.store.WriteIDs(list)) {
		if !bytes.Equal(row.Record, rosterRecord) && !bytes.Equal(row.Epoch, current) && !containsID(inv[string(row.Record)], row.ID) {
			n++
		}
	}
	return n
}

func dataWrites(t *testing.T, s *Store, list []byte) int {
	t.Helper()
	n := 0
	for _, row := range try(s.WriteIDs(list)) {
		if !bytes.Equal(row.Record, rosterRecord) {
			n++
		}
	}
	return n
}

// §9.3: once the next inventory is here, what it doesn't list goes; the state, and a restart, don't change.
func TestGCPastWritesGoAfterAKeyChange(t *testing.T) {
	w := newRotWorld(t)
	bread := NewRecordID()
	for _, v := range []string{"Rye", "Spelt", "Sourdough"} {
		must(t, w.alice.SetField(bg, w.house, bread, "title", []byte(v)))
	}
	syncAll(t, w.bob)
	before := dataWrites(t, w.bobStore, w.house)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.house))
	must(t, w.carol.SetField(bg, w.house, NewRecordID(), "title", []byte("Late")))
	syncAll(t, w.alice)
	must(t, w.alice.SetField(bg, w.house, NewRecordID(), "title", []byte("Eggs")))
	syncAll(t, w.bob, w.carol, w.alice, w.bob)
	for _, d := range []*Device{w.bob, w.alice} {
		if n := pastWrites(t, d, w.house); n != 0 {
			t.Fatalf("%d unlisted writes of the past epoch kept", n)
		}
	}
	if after := dataWrites(t, w.bobStore, w.house); after >= before {
		t.Fatalf("Bob holds %d writes, %d before the key change", after, before)
	}
	want := set("Milk", "Bread", "Sourdough", "Eggs")
	if got := titles(w.bob, w.house); !sameSet(got, want) {
		t.Fatal("Bob sees", got)
	}
	again := open(t, w.f, w.bobKeys, w.bobStore, w.hub.Connect())
	w.bob.Close()
	if got := titles(again, w.house); !sameSet(got, want) {
		t.Fatal("after a restart Bob sees", got)
	}
	syncAll(t, again)
	if got := titles(again, w.house); !sameSet(got, want) {
		t.Fatal("after a restart and a sync Bob sees", got)
	}
}

// A removed member's own late write stays in their replica, and no unshared write is dropped.
func TestGCLateWritesOfARemovedMemberStay(t *testing.T) {
	w := newRotWorld(t)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.house))
	must(t, w.carol.SetField(bg, w.house, NewRecordID(), "title", []byte("Gone")))
	syncAll(t, w.bob, w.alice, w.carol, w.carol)
	if !titles(w.carol, w.house)["Gone"] {
		t.Fatal("Carol's replica keeps her edit for the retention period")
	}
}

// §11.9: a deleted list stays, read-only, for the retention period; then its replica goes, all
// but the roster, and a device of the owner destroys its data on the hub, all but the roster. The
// list changed key first, so the new channel holds writes carried over from the old one.
func TestGCDeletedListAfterRetention(t *testing.T) {
	hub, f, bob, alice, aliceID := lifeHub(t)
	list := try(bob.CreateList(bg, "Old"))
	must(t, bob.SetField(bg, list, NewRecordID(), "title", []byte("Milk")))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	syncAll(t, alice)
	must(t, alice.Accept(bg, list))
	must(t, alice.SetField(bg, list, NewRecordID(), "title", []byte("Bread")))
	syncAll(t, bob)
	must(t, bob.Rekey(bg, list))
	syncAll(t, alice, bob, alice)
	epoch, key := epochKey(bob, list)
	tag := tandem.CollectionTag(key, bob.Pin().Hub, epoch)
	inChannel := func() (data, roster int) {
		for _, m := range hub.Messages() {
			if m.Channel != tag {
				continue
			}
			_, p := try2(tandem.Open(key, bob.Pin().Hub, tag, m.Body))
			if state, _, err := tandem.ParseKind1(p, bob.Pin().Hub, list); err == nil && state != nil && bytes.Equal(state.Record, rosterRecord) {
				roster++
			} else {
				data++
			}
		}
		return
	}
	must(t, bob.Delete(bg, list))
	syncAll(t, alice, bob)
	if data, _ := inChannel(); data == 0 {
		t.Fatal("the data stays on the hub for the retention period")
	}
	if dataWrites(t, alice.store, list) == 0 || len(alice.GoneLists()) != 0 {
		t.Fatal("Alice's replica stays for the retention period")
	}
	for _, d := range []*Device{alice, bob} {
		d.SetRetention(time.Nanosecond)
	}
	syncAll(t, bob, bob, alice, alice, alice)
	if data, roster := inChannel(); data != 0 || roster == 0 {
		t.Fatalf("on the hub: %d data messages, %d roster messages", data, roster)
	}
	for _, d := range []*Device{alice, bob} {
		if n := dataWrites(t, d.store, list); n != 0 {
			t.Fatalf("%d writes of a collected list kept", n)
		}
		if !d.IsDeleted(list) || len(d.GoneLists()) != 1 || len(d.Lists()) != 0 {
			t.Fatal("still known as deleted, and not shown")
		}
	}
	// A device that comes later learns from the roster that the list was deleted.
	late := open(t, f, alice.keys, MemoryStore(), hub.Connect())
	syncAll(t, late)
	if !late.IsDeleted(list) {
		t.Fatal("a late device learns of the deletion")
	}
}

// §11.6: a removed member's replica stays, read-only, for the retention period, then goes; the hub
// is the owner's business.
func TestGCRemovedListAfterRetention(t *testing.T) {
	w := newRotWorld(t)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.house))
	syncAll(t, w.alice, w.carol)
	if lists := w.carol.RemovedLists(); len(lists) != 1 || len(titles(w.carol, w.house)) == 0 {
		t.Fatal("Carol keeps the replica for the retention period")
	}
	hubBefore := len(w.hub.Messages())
	w.carol.SetRetention(time.Nanosecond)
	syncAll(t, w.carol)
	if len(w.carol.RemovedLists()) != 0 || len(titles(w.carol, w.house)) != 0 || dataWrites(t, w.carol.store, w.house) != 0 {
		t.Fatal("collected after the retention period")
	}
	if len(w.hub.Messages()) != hubBefore {
		t.Fatal("a removed member's device destroys nothing on the hub")
	}
	if got := titles(w.alice, w.house); !sameSet(got, set("Milk", "Bread")) {
		t.Fatal("Alice", got)
	}
}

// Files of a deleted list: kept while the replica is, then the owner's device destroys every
// chunk and each device drops what it downloaded (DESIGN.md §17.7).
func TestGCDeletedListsFilesGo(t *testing.T) {
	w := newBlobWorld(t)
	photo, data := NewRecordID(), content(2500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	syncAll(t, w.alice)
	fetched(t, w.alice, w.list, photo, "photo")
	m := manifestOf(t, w.bob, w.list, photo, "photo")
	aliceFile := filepath.Join(w.alice.store.BlobDir(), hex.EncodeToString(m.Hash))
	must(t, w.bob.Delete(bg, w.list))
	clean(t, w.alice, w.bob)
	if onHub(w.blobs, m) != len(m.Chunks) || !fileExists(aliceFile) {
		t.Fatal("kept for the retention period")
	}
	for _, d := range []*Device{w.alice, w.bob} {
		d.SetRetention(time.Nanosecond)
	}
	clean(t, w.alice, w.bob, w.alice, w.bob)
	if n := onHub(w.blobs, m); n != 0 {
		t.Fatalf("%d chunks of a deleted list left", n)
	}
	if fileExists(aliceFile) {
		t.Fatal("Alice's copy of a collected list's file stays")
	}
}

// A downloaded file no record names any more goes: a replaced version's, a deleted record's.
func TestGCUnnamedFilesGo(t *testing.T) {
	w := newBlobWorld(t)
	photo, doc := NewRecordID(), NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	must(t, w.bob.AttachBytes(bg, w.list, doc, "file", content(1500)))
	settle(t, w.bob)
	syncAll(t, w.alice)
	fetched(t, w.alice, w.list, photo, "photo")
	fetched(t, w.alice, w.list, doc, "file")
	dir := w.alice.store.BlobDir()
	old := filepath.Join(dir, hex.EncodeToString(manifestOf(t, w.alice, w.list, photo, "photo").Hash))
	gone := filepath.Join(dir, hex.EncodeToString(manifestOf(t, w.alice, w.list, doc, "file").Hash))
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2600)))
	must(t, w.bob.DeleteRecord(bg, w.list, doc))
	settle(t, w.bob)
	syncAll(t, w.bob)
	clean(t, w.alice)
	newer := filepath.Join(dir, hex.EncodeToString(manifestOf(t, w.alice, w.list, photo, "photo").Hash))
	fetched(t, w.alice, w.list, photo, "photo")
	clean(t, w.alice)
	if fileExists(old) || fileExists(gone) {
		t.Fatal("unnamed content stays")
	}
	if !fileExists(newer) {
		t.Fatal("the current version was dropped")
	}
	if _, ok := w.alice.store.Value("blobs.opened." + filepath.Base(old)); ok {
		t.Fatal("the dropped file's last-opened time stays")
	}
	entries := try(os.ReadDir(filepath.Join(dir, "partial")))
	if len(entries) != 0 {
		t.Fatal("partial downloads left", len(entries))
	}
}

// A replaced version whose chunks were still on the hub when its write was dropped is collected
// all the same: the dropped manifest still accounts for them (DESIGN.md §17.7, condition 2).
func TestGCDroppedManifestsChunksAreStillCollected(t *testing.T) {
	w := newBlobWorld(t)
	photo := NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	settle(t, w.bob)
	m1 := manifestOf(t, w.bob, w.list, photo, "photo")
	w.blobs.FailDestroys = true
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2600)))
	settle(t, w.bob)
	syncAll(t, w.alice)
	settle(t, w.alice)
	if onHub(w.blobs, m1) == 0 {
		t.Fatal("the destroy was meant to fail")
	}
	must(t, w.bob.RemoveMember(bg, me(t, w.alice).Participant, w.list))
	clean(t, w.bob, w.bob)
	if n := pastWrites(t, w.bob, w.list); n != 0 {
		t.Fatal("the replaced write was dropped", n)
	}
	w.blobs.FailDestroys = false
	clean(t, w.bob, w.bob)
	if n := onHub(w.blobs, m1); n != 0 {
		t.Fatalf("%d chunks of a dropped manifest left", n)
	}
	if vs := try(w.bob.store.Values("blobs.dropped.")); len(vs) != 0 {
		t.Fatal("the dropped manifest is forgotten once its chunks are gone")
	}
}
