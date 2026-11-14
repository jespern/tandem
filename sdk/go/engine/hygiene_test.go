package engine

import (
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// §10.7 and §12.2: the hub keeps about one message per record, and what a hostile hub drops comes back.

func hygieneWorld(t *testing.T) (*MemoryHub, *Founding, *tandem.DeviceKeys, *Device, *Device, []byte) {
	hub, f, keys, bob := found(t, "Bob’s iPhone", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	syncAll(t, bob)
	list := try(bob.CreateList(bg, "House"))
	must(t, bob.Share(bg, list, me(t, alice).Participant, Writer, ""))
	syncAll(t, alice)
	must(t, alice.Accept(bg, list))
	return hub, f, keys, bob, alice, list
}

func channel(d *Device, list []byte) string {
	epoch, key := epochKey(d, list)
	return tandem.CollectionTag(key, d.pin.Hub, epoch)
}

func TestHygieneEditsLeaveOneMessagePerRecord(t *testing.T) {
	hub, _, _, bob, alice, list := hygieneWorld(t)
	item := NewRecordID()
	for i := 0; i < 10; i++ {
		must(t, bob.SetField(bg, list, item, "title", []byte{'v', byte('0' + i)}))
	}
	must(t, bob.SetField(bg, list, item, "done", []byte{1}))
	must(t, alice.SetField(bg, list, item, "notes", []byte("hi")))
	syncAll(t, bob, alice, bob, alice) // the merged state goes out, then the rest is collected
	tag := channel(bob, list)
	_, key := epochKey(bob, list)
	count := 0
	for _, m := range hub.Messages() {
		if m.Channel != tag {
			continue
		}
		_, p := try2(tandem.Open(key, bob.pin.Hub, tag, m.Body))
		if s, _, _ := tandem.ParseKind1(p, bob.pin.Hub, list); s != nil && eq(s.Record, item) {
			count++
		}
	}
	if count != 1 {
		t.Fatal("eleven edits on two devices leave one message, not", count)
	}
	fresh, _ := alice.Item(list, item)
	if fresh.Text("title") != "v9" || fresh.Text("notes") != "hi" {
		t.Fatal(fresh.Text("title"), fresh.Text("notes"))
	}
}

func TestHygieneWhatTheHubDropsComesBack(t *testing.T) {
	hub, f, keys, bob, alice, list := hygieneWorld(t)
	item := NewRecordID()
	must(t, alice.SetField(bg, list, item, "title", []byte("Milk")))
	syncAll(t, bob)
	tag := channel(bob, list)
	// A participant with the mailbox password deletes every data message of the list.
	var ids []string
	for _, m := range hub.Messages() {
		if m.Channel == tag {
			ids = append(ids, m.ID)
		}
	}
	hub.Drop(ids)
	syncAll(t, bob) // Bob's device notices and publishes what it holds again
	laptop := open(t, f, keys, nil, hub.Connect())
	syncAll(t, laptop)
	if it, ok := laptop.Item(list, item); !ok || it.Text("title") != "Milk" {
		t.Fatal("Milk")
	}
	if laptop.Name(list) != "House" {
		t.Fatal("name")
	}
}

// §11.4: a grant deleted before it was read comes back; one to someone removed goes.
func TestHygieneGrantsAreKept(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s iPhone", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	aliceID := me(t, alice).Participant
	syncAll(t, bob)
	list := try(bob.CreateList(bg, "House"))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	inbox := tandem.InboxTag(aliceID, f.Pin.Hub)
	var before []string
	for _, m := range hub.Messages() {
		if m.Channel == inbox {
			before = append(before, m.ID)
		}
	}
	hub.Drop(before) // someone with the mailbox password deletes Alice's inbox
	syncAll(t, bob)  // Bob's device puts the grant back
	syncAll(t, alice)
	if o := alice.PendingOffers(); len(o) != 1 || !eq(o[0].List, list) {
		t.Fatal(o)
	}
	must(t, alice.Accept(bg, list))
	must(t, bob.RemoveMember(bg, aliceID, list))
	syncAll(t, bob)
	if kept := try(bob.store.Values("kept.grant.")); len(kept) != 0 {
		t.Fatal("a grant to someone no longer listed is forgotten")
	}
}
