package engine

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Regression tests for the review of the Go engine (2026-11-13), ported from the Swift
// engine's SecondReviewTests: findings that held in both engines, and one (6) only here. Each
// failed before its fix.

type secondWorld struct {
	hub                 *MemoryHub
	f                   *Founding
	bob, phone, tablet  *Device
	aliceKeys           *tandem.DeviceKeys
	aliceID, tabletCert []byte
	list                []byte
}

// newSecondWorld: Bob owns a list shared with Alice, who has a phone and a paired tablet; Bob
// then revokes the tablet, if revoke.
func newSecondWorld(t *testing.T, revoke bool) secondWorld {
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	aliceKeys := newKeys(t)
	phone := join(t, bob, f, hub, "Alice", aliceKeys)
	tablet := pairApproved(t, phone, bob, f, hub, "Alice’s tablet")
	syncAll(t, phone)
	aliceID, tabletCert := me(t, phone).Participant, me(t, tablet).Certificate
	list := try(bob.CreateList(bg, "House"))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	syncAll(t, phone)
	must(t, phone.Accept(bg, list))
	syncAll(t, tablet)
	if revoke {
		must(t, bob.Revoke(bg, tabletCert, f.ManagementSeed))
	}
	return secondWorld{hub, f, bob, phone, tablet, aliceKeys, aliceID, tabletCert, list}
}

func dropChannel(hub *MemoryHub, tag string) {
	var ids []string
	for _, m := range hub.Messages() {
		if m.Channel == tag {
			ids = append(ids, m.ID)
		}
	}
	hub.Drop(ids)
}

// 1. The revoked tablet writes a change-in-flight note naming the new epoch with a key of its
// own into Alice's home: her phone keeps the real key, and its writes don't go where the tablet reads.
func TestSecondReviewAKeyFromHomeDoesntDisplaceTheListKey(t *testing.T) {
	w := newSecondWorld(t, true)
	must(t, w.bob.Rekey(bg, w.list))
	epoch, _ := epochKey(w.bob, w.list)
	// Without Bob's grant the planted key is the only one the phone sees, whatever order it reads them in.
	dropChannel(w.hub, tandem.InboxTag(w.aliceID, w.f.Pin.Hub))
	syncAll(t, w.tablet)
	fake := bytes.Repeat([]byte{9}, 32)
	note := FlightNote{Parent: epoch, Statement: epoch, Key: fake, What: "x", Device: "x"}
	name := membershipName(w.list)
	must(t, w.tablet.SetHome(bg, name, "flight", note.json()))
	must(t, w.tablet.AddHome(bg, name, "keys", keyElement(epoch, fake)))
	syncAll(t, w.phone, w.phone)
	func() {
		defer w.phone.read()()
		if held := w.phone.collections[string(w.list)].keys[string(epoch)]; bytes.Equal(held, fake) {
			t.Error("a key from home counts only once the epoch's statement commits to it")
		}
	}()
	_ = w.phone.SetField(bg, w.list, NewRecordID(), "title", []byte("Private"))
	leak := tandem.CollectionTag(fake, w.f.Pin.Hub, epoch)
	for _, m := range w.hub.Messages() {
		if m.Channel == leak {
			t.Fatal("the phone wrote where the tablet reads")
		}
	}
}

// 2. After Alice's phone rekeyed her home without the tablet, the tablet starts a branch of its
// own (a start and a child of it) from the old head and sends the phone the key: neither start
// enters her home.
func TestSecondReviewARevokedKeychainCantBranchTheHome(t *testing.T) {
	w := newSecondWorld(t, true)
	syncAll(t, w.phone) // rekeys home without the tablet
	tablet := me(t, w.tablet)
	oldHead := w.tablet.HomeHeads()[0]
	var oldKey []byte
	func() { defer w.tablet.read()(); oldKey = w.tablet.homeKeys[string(oldHead)] }()
	k := bytes.Repeat([]byte{4}, 32)
	et := try(HomeEpochStart(w.f.Pin, w.aliceID, 9, [][]byte{oldHead}, k, [][]byte{tablet.Certificate}, tablet.Certificate, tablet.Auth, nil, tablet.Keys))
	et2 := try(HomeEpochStart(w.f.Pin, w.aliceID, 10, [][]byte{oid(et)}, k, [][]byte{tablet.Certificate}, tablet.Certificate, tablet.Auth, nil, tablet.Keys))
	oldTag := tandem.HomeTag(oldKey, w.f.Pin.Hub, w.aliceID, oldHead)
	for _, start := range []*tandem.Control{et, et2} {
		rawAppend(t, w.hub, oldTag, try(tandem.Seal(oldKey, w.f.Pin.Hub, tandem.KindRequest, oldTag, try(cjson(start)))))
	}
	phoneCert := w.phone.Registry().Certificate(me(t, w.phone).Certificate)
	for _, e := range []*tandem.Control{et, et2} {
		rawAppend(t, w.hub, tandem.InboxTag(w.aliceID, w.f.Pin.Hub),
			try(tandem.SealInbox(w.f.Pin.Hub, w.aliceID, phoneCert.Agree, homeKeyNotice(oid(e), k).JSON())))
	}
	syncAll(t, w.phone)
	defer w.phone.read()()
	if w.phone.homeEpochs[string(oid(et))] != nil || w.phone.homeEpochs[string(oid(et2))] != nil {
		t.Fatal("a revoked keychain's branch entered the home")
	}
}

// 3. The admin's revocation can't reach the hub; after a restart the device sends it, and the others see it.
func TestSecondReviewAnInterruptedRevocationIsSentLater(t *testing.T) {
	w := newSecondWorld(t, false)
	store := MemoryStore()
	admin := open(t, w.f, nil, store, failingAppends{w.hub.Connect()})
	syncAll(t, admin)
	if err := admin.Revoke(bg, w.tabletCert, w.f.ManagementSeed); err == nil {
		t.Fatal("offline")
	}
	again := open(t, w.f, nil, store, w.hub.Connect())
	syncAll(t, again, w.phone)
	if v := w.phone.Registry().Activity(w.tabletCert); !v.Is("revoked") {
		t.Fatal(v)
	}
}

// Liveness and crash safety

type bobAndCarol struct {
	hub     *MemoryHub
	f       *Founding
	keys    *tandem.DeviceKeys
	store   *Store
	bob     *Device
	carol   *Device
	carolID []byte
	list    []byte
}

func newBobAndCarol(t *testing.T) bobAndCarol {
	store := MemoryStore()
	hub, f, keys, bob := found(t, "Bob’s iPhone", store)
	carol := join(t, bob, f, hub, "Carol", nil)
	syncAll(t, bob)
	carolID := me(t, carol).Participant
	list := try(bob.CreateList(bg, "House"))
	must(t, bob.Share(bg, list, carolID, Writer, ""))
	syncAll(t, carol)
	must(t, carol.Accept(bg, list))
	return bobAndCarol{hub, f, keys, store, bob, carol, carolID, list}
}

// lostAnswers is a connection whose appends land but whose answers are lost, once lose is set
// (§10.3, "Lost answers").
type lostAnswers struct {
	*MemoryConn
	lose atomic.Bool
}

func (c *lostAnswers) Append(ctx context.Context, channel string, body []byte) (string, error) {
	id, err := c.MemoryConn.Append(ctx, channel, body)
	if err == nil && c.lose.Load() {
		return "", errors.New("the answer was lost")
	}
	return id, err
}

// 5. The note's append lands but its answer is lost; Bob's Mac discards the change; when the
// iPhone resumes, it must take the missing note as the discard it is, not write the note again
// and announce.
func TestSecondReviewADiscardedChangeStaysDiscardedAfterALostAnswer(t *testing.T) {
	w := newBobAndCarol(t)
	lossy := &lostAnswers{MemoryConn: w.hub.Connect()}
	phone := open(t, w.f, w.keys, w.store, lossy)
	syncAll(t, phone)
	lossy.lose.Store(true)
	if err := phone.RemoveMember(bg, w.carolID, w.list); err == nil {
		t.Fatal("the answer was lost")
	}
	mac := open(t, w.f, w.keys, nil, w.hub.Connect())
	syncAll(t, mac)
	pending := mac.PendingChange(w.list)
	if pending == nil {
		t.Fatal("no pending change")
	}
	must(t, mac.Discard(bg, w.list, pending))
	restarted := open(t, w.f, w.keys, w.store, w.hub.Connect())
	if err := restarted.Resume(bg); !errors.Is(err, ErrSuperseded) {
		t.Fatal("the user is told:", err)
	}
	syncAll(t, w.carol)
	if r := w.carol.Role(w.list); r != Writer {
		t.Fatal("the discarded change happened:", r)
	}
}

// 7. A same-epoch change whose publish fails leaves nothing behind locally, so a later change
// doesn't fork the roster.
func TestSecondReviewAFailedSameEpochChangeLeavesNoTrace(t *testing.T) {
	w := newBobAndCarol(t)
	offline := open(t, w.f, w.keys, w.store, failingAppends{w.hub.Connect()})
	if err := offline.SetRole(bg, Writer, w.carolID, w.list); err == nil {
		if err := offline.Delete(bg, w.list); err == nil {
			t.Fatal("offline")
		}
	}
	back := open(t, w.f, w.keys, w.store, w.hub.Connect())
	syncAll(t, back)
	if back.IsDeleted(w.list) {
		t.Fatal("a delete that never left the device didn't happen")
	}
	must(t, back.SetRole(bg, Reader, w.carolID, w.list))
	must(t, back.SetRole(bg, Writer, w.carolID, w.list))
	if back.IsDead(w.list) {
		t.Fatal("the roster forked")
	}
	syncAll(t, w.carol)
	if w.carol.IsDeleted(w.list) {
		t.Fatal("the delete never reached the hub")
	}
}

// 8. Joining fails half way; joining again with the same store finishes it, and the admin sees the member.
func TestSecondReviewAnInterruptedJoinFinishes(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	link := try(bob.Invite(bg, "Alice", account, f.ManagementSeed, 0))
	aliceKeys, aliceStore := newKeys(t), MemoryStore()
	if d, err := Join(bg, link, aliceKeys, "Alice", aliceStore, failingAppends{hub.Connect()}); err == nil {
		d.Close()
		t.Fatal("offline")
	}
	alice := try(Join(bg, link, aliceKeys, "Alice", aliceStore, hub.Connect()))
	t.Cleanup(alice.Close)
	must(t, alice.SetHome(bg, "settings:x", "v", []byte{1}))
	syncAll(t, bob)
	if v := bob.Registry().Activity(me(t, alice).Certificate); !v.IsValid() {
		t.Fatal(v)
	}
}

// 8. A pairing claim that didn't go out is sent again by the waiting device's next sync.
func TestSecondReviewAPairingClaimThatDidntGoOutIsSentAgain(t *testing.T) {
	w := newSecondWorld(t, false)
	link := try(w.phone.StartPairing(bg, "Laptop", account, 0))
	laptopStore, laptopKeys := MemoryStore(), newKeys(t)
	if d, err := Pair(bg, link, laptopKeys, "Laptop", laptopStore, failingAppends{w.hub.Connect()}); err == nil {
		d.Close()
		t.Fatal("offline")
	}
	laptop := try(Reopen(laptopStore, laptopKeys, w.hub.Connect()))
	if laptop == nil {
		t.Fatal("nothing to reopen")
	}
	t.Cleanup(laptop.Close)
	syncAll(t, laptop, w.bob)
	claims := w.bob.PendingClaims()
	if len(claims) != 1 || claims[0].Label() != "Laptop" {
		t.Fatal(claims)
	}
}

// 11. A part of the current inventory is deleted and a bogus version put in its place: the
// owner's device publishes the real one again, so a member who hadn't moved over yet still can.
func TestSecondReviewAReplacedInventoryPartIsRestored(t *testing.T) {
	w := newBobAndCarol(t)
	must(t, w.bob.SetField(bg, w.list, NewRecordID(), "title", []byte("Kept")))
	must(t, w.bob.Rekey(bg, w.list))
	epoch, key := epochKey(w.bob, w.list)
	tag := tandem.CollectionTag(key, w.f.Pin.Hub, epoch)
	var fragments []string
	for _, m := range w.hub.Messages() {
		if m.Channel != tag {
			continue
		}
		_, pt := try2(tandem.Open(key, w.f.Pin.Hub, tag, m.Body))
		if _, frag, err := tandem.ParseKind1(pt, w.f.Pin.Hub, w.list); err == nil && frag != nil {
			fragments = append(fragments, m.ID)
		}
	}
	if len(fragments) == 0 {
		t.Fatal("no fragments")
	}
	w.hub.Drop(fragments)
	bogus := tandem.NewJSONObject().Raw("inventory", tandem.NewJSONObject().B64u("epoch", epoch).Int("part", 0).Int("parts", 1).
		B64u("bytes", []byte{0, 0, 0, 0}).Bytes()).Bytes()
	rawAppend(t, w.hub, tag, try(tandem.Seal(key, w.f.Pin.Hub, tandem.KindRecord, tag, bogus)))
	syncAll(t, w.bob, w.carol)
	if got := titles(w.carol, w.list); !sameSet(got, set("Kept")) {
		t.Fatal("Carol moves over through the restored inventory:", got)
	}
}
