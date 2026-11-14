package engine

import (
	"bytes"
	"context"
	"errors"
	"slices"
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

// 6. Two devices of Alice rekey her home at once, leaving two heads, and one then changes a
// record both heads carry: syncing again and again settles, rather than each destroying and
// re-publishing the other's messages. Each head's channel keeps only the writes that count in it.
func TestSecondReviewTwoHomeHeadsSettle(t *testing.T) {
	for _, keychains := range []bool{false, true} {
		t.Run(map[bool]string{false: "installations", true: "keychains"}[keychains], func(t *testing.T) {
			var hub *MemoryHub
			var a, b *Device
			if keychains { // two keychains of one participant
				w := newRevWorld(t)
				hub, a, b = w.hub, w.phone, w.tablet
			} else { // two installations of one keychain
				var f *Founding
				var keys *tandem.DeviceKeys
				hub, f, keys, a = found(t, "Bob’s Mac", nil)
				b = open(t, f, keys, nil, hub.Connect())
				syncAll(t, b)
			}
			must(t, a.SetHome(bg, "settings:x", "v", []byte{1}))
			syncAll(t, b)
			must(t, a.RotateHome(bg))
			must(t, b.RotateHome(bg))
			must(t, a.SetHome(bg, "settings:a", "v", []byte{1}))
			must(t, b.SetHome(bg, "settings:b", "v", []byte{2}))
			must(t, a.SetHome(bg, "settings:x", "v", []byte{3}))
			for range 3 {
				syncAll(t, a, b)
			}
			if n := len(a.HomeHeads()); n != 2 {
				t.Fatal(n, "heads")
			}
			ids := func() []string {
				var out []string
				for _, m := range hub.Messages() {
					out = append(out, m.ID)
				}
				slices.Sort(out)
				return out
			}
			before := ids()
			for range 3 {
				syncAll(t, a, b)
			}
			if after := ids(); !slices.Equal(after, before) {
				t.Fatal("still destroying and publishing:", len(before), "messages before,", len(after), "after")
			}
			for _, d := range []*Device{a, b} {
				if homeValue(d, "settings:a", "v")[0] != 1 || homeValue(d, "settings:b", "v")[0] != 2 || homeValue(d, "settings:x", "v")[0] != 3 {
					t.Fatal("a home write was lost")
				}
			}
		})
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

// A home write on a device that knows of a revocation its home doesn't reflect yet rekeys the
// home first, so nothing new goes into an epoch the revoked keychain reads. (A sync rekeys the
// home as soon as it learns of a revocation; this is the write that comes first anyway.)
func TestSecondReviewAHomeWriteFirstRekeysAnExposedHome(t *testing.T) {
	w := newSecondWorld(t, false)
	seen := map[string]bool{}
	for _, m := range w.hub.Messages() {
		seen[m.ID] = true
	}
	must(t, w.bob.Revoke(bg, w.tabletCert, w.f.ManagementSeed))
	var learned []*tandem.Control
	for _, m := range w.hub.Messages() {
		if seen[m.ID] {
			continue
		}
		if _, pt, err := tandem.Open(w.bob.directoryChannelKey(), w.f.Pin.Hub, m.Channel, m.Body); err == nil {
			for _, typ := range []string{"revocation", "snapshot"} {
				if o, err := tandem.FromJSON(pt, typ); err == nil {
					learned = append(learned, o)
				}
			}
		}
	}
	func() { // the phone knows of the revocation, and nothing has followed up on it yet
		defer w.phone.begin()()
		for _, o := range learned {
			try(w.phone.registry.Add(o))
		}
		if !w.phone.homeNeedsRotation(w.phone.me) {
			t.Fatal("the test needs a home the revocation isn't reflected in")
		}
	}()
	exposed := map[string][]byte{} // tag → key
	func() {
		defer w.tablet.read()()
		for e, k := range w.tablet.homeKeys {
			exposed[tandem.HomeTag(k, w.f.Pin.Hub, w.aliceID, []byte(e))] = k
		}
	}()
	count := func() (n int) { // home writes the tablet can read; an announcement of the next epoch is fine
		for _, m := range w.hub.Messages() {
			if k := exposed[m.Channel]; k != nil && !seen[m.ID] {
				if kind, _, err := tandem.Open(k, w.f.Pin.Hub, m.Channel, m.Body); err == nil && kind == tandem.KindRecord {
					n++
				}
			}
		}
		return n
	}
	must(t, w.phone.SetHome(bg, "settings:liszto", "secret", []byte("for Alice's devices")))
	if n := count(); n != 0 {
		t.Fatal("a home write went into an epoch the revoked tablet reads")
	}
	if v := homeValue(w.phone, "settings:liszto", "secret"); v == nil {
		t.Fatal("not written")
	}
}

// An inventory cannot make a stale source authorization valid in another epoch.
func TestInventoryCannotBypassTheOriginalHomeFloor(t *testing.T) {
	w := newSecondWorld(t, true)
	oldAuth := bytes.Clone(me(t, w.tablet).Auth)
	syncAll(t, w.phone)
	m := me(t, w.phone)
	w.phone.mu.Lock()
	defer w.phone.mu.Unlock()
	parent := bytes.Clone(w.phone.homeHeads()[0])
	bad := &tandem.Write{Hub: w.f.Pin.Hub, Certificate: m.Certificate, Authorization: oldAuth, Scope: tandem.HomeScope(w.f.Pin.Hub, m.Participant), Record: tandem.NamedID("settings:floor"), Field: "v", Op: tandem.OpAssign, Value: []byte{9}, Author: m.Participant, Dot: tandem.Dot{Device: m.Installation, Millis: 1}, Ref: parent}
	must(t, bad.Sign(m.Keys.Signing))
	if v := w.phone.homeWriteValidity(bad, parent, m); v.State != Invalid {
		t.Fatal("stale source accepted", v)
	}
	inv := tandem.Inventory{string(bad.Record): [][]byte{writeID(bad)}}
	next := try(HomeEpochStart(w.f.Pin, m.Participant, w.phone.epochNumber(parent)+1, [][]byte{parent}, bytes.Repeat([]byte{7}, 32), [][]byte{m.Certificate}, m.Certificate, m.Auth, inv, m.Keys))
	_, err := w.phone.acceptHomeEpoch(next, m)
	must(t, err)
	must(t, w.phone.keepInventory(inv, oid(next)))
	if v := w.phone.homeWriteValidity(bad, oid(next), m); v.State != Invalid {
		t.Fatal("inventory import bypassed the source floor", v)
	}
}

func TestInventoryCannotBypassTheOriginalCollectionFloor(t *testing.T) {
	w := newSecondWorld(t, true)
	oldAuth := bytes.Clone(me(t, w.tablet).Auth)
	must(t, w.bob.Rekey(bg, w.list))
	syncAll(t, w.phone)
	m, owner := me(t, w.phone), me(t, w.bob)
	w.phone.mu.Lock()
	defer w.phone.mu.Unlock()
	c := w.phone.collections[string(w.list)]
	parent := c.head()
	epoch := c.epochOf(parent)
	bad := &tandem.Write{Hub: w.f.Pin.Hub, Certificate: m.Certificate, Authorization: oldAuth, Scope: w.list, Record: NewRecordID(), Field: "title", Op: tandem.OpAssign, Value: []byte{9}, Author: m.Participant, Dot: tandem.Dot{Device: m.Installation, Millis: 1}, Ref: parent}
	must(t, bad.Sign(m.Keys.Signing))
	if v := w.phone.dataWriteValidity(bad, w.list, epoch); v.State != Invalid {
		t.Fatal("stale source accepted", v)
	}
	inv := tandem.Inventory{string(bad.Record): [][]byte{writeID(bad)}}
	next := c.statements[string(parent)].s.clone()
	next.N++
	next.KeyN++
	next.Nonce = nil
	next.Prev = bytes.Clone(next.Commit)
	next.Commit = tandem.Commit(bytes.Repeat([]byte{6}, 32))
	next.Snapshot = tandem.H(try(inv.Bytes()))
	statement := &tandem.Write{Hub: w.f.Pin.Hub, Certificate: owner.Certificate, Authorization: owner.Auth, Scope: w.list, Record: rosterRecord, Field: rosterField, Op: tandem.OpAssign, Value: next.JSON(), Author: owner.Participant, Dot: tandem.Dot{Device: owner.Installation, Millis: 2}, Ref: parent}
	must(t, statement.Sign(owner.Keys.Signing))
	_, err := w.phone.acceptStatement(statement, w.list, epoch)
	must(t, err)
	id := writeID(statement)
	must(t, w.phone.keepInventory(inv, id))
	if v := w.phone.dataWriteValidity(bad, w.list, id); v.State != Invalid {
		t.Fatal("inventory import bypassed the member source floor", v)
	}
}

// Correct slice bytes with the wrong index/count cannot satisfy inventory healing.
func TestInventoryHealingChecksFragmentIndex(t *testing.T) {
	w := newBobAndCarol(t)
	must(t, w.bob.SetField(bg, w.list, NewRecordID(), "title", []byte("Kept")))
	must(t, w.bob.Rekey(bg, w.list))
	epoch, key := epochKey(w.bob, w.list)
	tag := tandem.CollectionTag(key, w.f.Pin.Hub, epoch)
	var fragments []string
	var inventory []byte
	for _, m := range w.hub.Messages() {
		if m.Channel != tag {
			continue
		}
		_, pt := try2(tandem.Open(key, w.f.Pin.Hub, tag, m.Body))
		if _, frag, err := tandem.ParseKind1(pt, w.f.Pin.Hub, w.list); err == nil && frag != nil {
			fragments = append(fragments, m.ID)
			inventory = bytes.Clone(frag.Bytes)
		}
	}
	if len(fragments) == 0 {
		t.Fatal("no fragments")
	}
	w.hub.Drop(fragments)
	bogus := tandem.NewJSONObject().Raw("inventory", tandem.NewJSONObject().B64u("epoch", epoch).Int("part", 1).Int("parts", 2).
		B64u("bytes", inventory).Bytes()).Bytes()
	rawAppend(t, w.hub, tag, try(tandem.Seal(key, w.f.Pin.Hub, tandem.KindRecord, tag, bogus)))
	syncAll(t, w.bob, w.carol)
	if got := titles(w.carol, w.list); !sameSet(got, set("Kept")) {
		t.Fatal("Carol moves over through the restored inventory:", got)
	}
}
