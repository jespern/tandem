package engine

import (
	"bytes"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

func twoInstallations(t *testing.T) (*MemoryHub, *Founding, *tandem.DeviceKeys, *Device, *Device, *Store) {
	store := MemoryStore()
	hub, f, keys, phone := found(t, "iPhone", store)
	mac := open(t, f, keys, nil, hub.Connect())
	syncAll(t, mac)
	return hub, f, keys, phone, mac, store
}

// A list made on the phone appears on the Mac with no invitation (§6.2), and edits flow both ways.
func TestCollectionAListReachesTheParticipantsOtherInstallation(t *testing.T) {
	_, _, _, phone, mac, _ := twoInstallations(t)
	groceries := try(phone.CreateList(bg, "Groceries"))
	milk := NewRecordID()
	must(t, phone.SetField(bg, groceries, milk, "title", []byte("Milk")))
	syncAll(t, mac)
	lists := mac.Lists()
	if len(lists) != 1 || !bytes.Equal(lists[0].ID, groceries) || lists[0].Name != "Groceries" {
		t.Fatal(lists)
	}
	if value_(mac, groceries, milk, "title") != "Milk" {
		t.Fatal("title")
	}
	must(t, mac.SetField(bg, groceries, milk, "done", []byte("true")))
	bread := NewRecordID()
	must(t, mac.SetField(bg, groceries, bread, "title", []byte("Bread")))
	syncAll(t, phone)
	if value_(phone, groceries, milk, "done") != "true" {
		t.Fatal("done")
	}
	if rs := phone.Records(groceries); len(rs) != 2 {
		t.Fatal(rs)
	}
	must(t, phone.DeleteRecord(bg, groceries, bread))
	syncAll(t, mac)
	if rs := mac.Records(groceries); len(rs) != 1 || !bytes.Equal(rs[0], milk) {
		t.Fatal(rs)
	}
}

func TestCollectionListsSurviveARestart(t *testing.T) {
	hub, f, keys, phone, _, store := twoInstallations(t)
	list := try(phone.CreateList(bg, "House"))
	r := NewRecordID()
	must(t, phone.SetField(bg, list, r, "title", []byte{1}))
	reopened := open(t, f, keys, store, hub.Connect())
	if ls := reopened.Lists(); len(ls) != 1 || ls[0].Name != "House" {
		t.Fatal(ls)
	}
	if value_(reopened, list, r, "title") != "\x01" {
		t.Fatal("title")
	}
	must(t, reopened.SetField(bg, list, r, "title", []byte{2}))
	if value_(reopened, list, r, "title") != "\x02" {
		t.Fatal("updated")
	}
}

// Holding the list key isn't membership: a write by someone the roster doesn't list is refused (§8.3).
func TestCollectionAWriteByANonMemberIsRefused(t *testing.T) {
	hub, f, _, phone, mac, _ := twoInstallations(t)
	list := try(phone.CreateList(bg, "Groceries"))
	syncAll(t, mac)
	other := newKeys(t)
	epoch, key := epochKey(phone, list)
	head := phone.collections[string(list)].head()
	w := &tandem.Write{Hub: f.Pin.Hub, Certificate: oid(f.Certificate), Authorization: oid(f.Snapshot), Scope: list,
		Record: NewRecordID(), Field: "title", Op: tandem.OpAssign, Value: []byte("Vandal"), Author: bytes.Repeat([]byte{7}, 32),
		Dot: tandem.Dot{Device: [8]byte{9, 9, 9, 9, 9, 9, 9, 9}, Millis: 1}, Ref: head}
	must(t, w.Sign(other.Signing))
	tag := tandem.CollectionTag(key, f.Pin.Hub, epoch)
	rawAppend(t, hub, tag, try(tandem.Seal(key, f.Pin.Hub, tandem.KindRecord, tag, try(tandem.RecordPayload(w.Record, []*tandem.Write{w})))))
	syncAll(t, mac)
	if rs := mac.Records(list); len(rs) != 0 {
		t.Fatal(rs)
	}
}
