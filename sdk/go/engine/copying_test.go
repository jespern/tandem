package engine

import (
	"bytes"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

type copyWorld struct {
	hub                      *MemoryHub
	bob, bob2                *Device
	alice, carol, dave       *Device
	aliceID, carolID, daveID []byte
	list, milk               []byte
}

// deadList: Bob shares House with Alice, then his two devices each add someone while the hub
// hides one from the other: the phone adds Carol, the laptop Dave, both after the same statement.
func deadList(t *testing.T) copyWorld {
	t.Helper()
	hub, f, keys, bob := found(t, "Bob’s iPhone", nil)
	alice, carol, dave := join(t, bob, f, hub, "Alice", nil), join(t, bob, f, hub, "Carol", nil), join(t, bob, f, hub, "Dave", nil)
	syncAll(t, bob)
	w := copyWorld{hub: hub, bob: bob, alice: alice, carol: carol, dave: dave,
		aliceID: me(t, alice).Participant, carolID: me(t, carol).Participant, daveID: me(t, dave).Participant}
	w.list = try(bob.CreateList(bg, "House"))
	must(t, bob.SetField(bg, w.list, metaRecord, "icon", []byte("🏠")))
	w.milk = NewRecordID()
	must(t, bob.SetField(bg, w.list, w.milk, "title", []byte("Milk")))
	must(t, bob.Add(bg, w.list, w.milk, "tags", []byte("dairy")))
	must(t, bob.Share(bg, w.list, w.aliceID, Writer, ""))
	syncAll(t, alice)
	must(t, alice.Accept(bg, w.list))

	laptop := hub.Connect()
	w.bob2 = open(t, f, keys, nil, laptop)
	syncAll(t, w.bob2)
	c := bob.collections[string(w.list)]
	epoch := c.epochOf(c.head())
	hub.Hide(tandem.CollectionTag(c.keys[string(epoch)], f.Pin.Hub, epoch), laptop)
	must(t, bob.Share(bg, w.list, w.carolID, Writer, ""))
	must(t, w.bob2.Share(bg, w.list, w.daveID, Reader, ""))
	syncAll(t, bob)
	if !bob.IsDead(w.list) {
		t.Fatal("two successors of one statement should kill the list")
	}
	return w
}

func ids(xs [][]byte) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[string(x)] = true
	}
	return out
}

func TestCopyTheOwnerCopiesADeadListAndItsMembersFollow(t *testing.T) {
	w := deadList(t)
	cp := try(w.bob.Copy(bg, w.list))
	if !sameSet(ids(cp.Shared), ids([][]byte{w.aliceID})) {
		t.Fatal("Alice is in both versions")
	}
	if !sameSet(ids(cp.Undecided), ids([][]byte{w.carolID, w.daveID})) {
		t.Fatal("Carol and Dave are each in one")
	}
	it, ok := w.bob.Item(cp.List, w.milk)
	if !ok || it.Text("title") != "Milk" || len(it.Elements("tags")) != 1 || string(it.Elements("tags")[0]) != "dairy" {
		t.Fatal("the item didn't come over")
	}
	if w.bob.Name(cp.List) != "House" || w.bob.IsDead(cp.List) {
		t.Fatal("name, or dead")
	}
	if v, _ := w.bob.Value(cp.List, metaRecord, "icon"); string(v) != "🏠" {
		t.Fatal("icon")
	}

	syncAll(t, w.alice)
	offers := w.alice.PendingOffers()
	if len(offers) != 1 || !bytes.Equal(offers[0].List, cp.List) {
		t.Fatalf("offers %v", offers)
	}
	if !bytes.Equal(w.alice.CopyOf(cp.List), w.list) {
		t.Fatal("Alice can't tell it continues House")
	}
	must(t, w.alice.Accept(bg, cp.List))
	must(t, w.alice.SetField(bg, cp.List, w.milk, "done", []byte{1}))
	syncAll(t, w.bob)
	if v, _ := w.bob.Value(cp.List, w.milk, "done"); !bytes.Equal(v, []byte{1}) {
		t.Fatal("Alice's edit")
	}

	// The undecided: the app asks Bob, who adds Carol.
	must(t, w.bob.Share(bg, cp.List, w.carolID, Writer, ""))
	syncAll(t, w.carol)
	found := false
	for _, o := range w.carol.PendingOffers() {
		found = found || bytes.Equal(o.List, cp.List)
	}
	if !found {
		t.Fatal("Carol wasn't offered the copy")
	}
}

// Asking again, here or on Bob's other device, finishes the same copy instead of making another.
func TestCopyAgainKeepsOneCopy(t *testing.T) {
	w := deadList(t)
	first := try(w.bob.Copy(bg, w.list))
	again := try(w.bob.Copy(bg, w.list))
	if !bytes.Equal(again.List, first.List) {
		t.Fatal("a second copy on the same device")
	}
	syncAll(t, w.bob2)
	fromLaptop := try(w.bob2.Copy(bg, w.list))
	if !bytes.Equal(fromLaptop.List, first.List) {
		t.Fatal("a second copy on another device")
	}
	if n := len(w.bob.Lists()); n != 2 {
		t.Fatalf("%d lists, want House and its one copy", n)
	}
}

// A member may copy too: the copy is theirs, and the old owner is offered it as a writer.
func TestCopyByAMemberMakesTheOldOwnerAWriter(t *testing.T) {
	w := deadList(t)
	syncAll(t, w.alice)
	if !w.alice.IsDead(w.list) {
		t.Fatal("Alice should see it dead")
	}
	cp := try(w.alice.Copy(bg, w.list))
	bobID := me(t, w.bob).Participant
	if !sameSet(ids(cp.Shared), ids([][]byte{bobID})) {
		t.Fatal("Bob should be shared")
	}
	var role Role
	for _, m := range w.alice.Members(cp.List) {
		if bytes.Equal(m.UID, bobID) {
			role = m.Role
		}
	}
	if role != Writer || !bytes.Equal(w.alice.Owner(cp.List), w.aliceID) {
		t.Fatal("roles")
	}
	syncAll(t, w.bob)
	if !bytes.Equal(w.bob.CopyOf(cp.List), w.list) {
		t.Fatal("Bob can't tell it continues House")
	}
}

// A list that claims to continue another, from someone who was never in it, isn't shown as its continuation.
func TestCopyAStrangersClaimIsntBelieved(t *testing.T) {
	w := deadList(t)
	notes := try(w.bob.CreateList(bg, "Notes"))
	must(t, w.bob.Share(bg, notes, w.aliceID, Writer, ""))
	fake := try(w.dave.CreateList(bg, "Notes"))
	must(t, w.dave.SetField(bg, fake, metaRecord, "copyof", notes))
	must(t, w.dave.Share(bg, fake, w.aliceID, Writer, ""))
	syncAll(t, w.alice)
	must(t, w.alice.Accept(bg, notes))
	if w.alice.CopyOf(fake) != nil {
		t.Fatal("Notes never listed Dave")
	}
}
