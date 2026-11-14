package engine

import (
	"bytes"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Regression tests for the independent review of the Swift engine (2026-11-13), most severe
// first. Each one is the attack or failure the review proved; the Go engine has every fix.

type reviewWorld struct {
	hub              *MemoryHub
	f                *Founding
	bobKeys          *tandem.DeviceKeys
	bob              *Device
	alice, carol     *Device
	aliceID, carolID []byte
	list             []byte
}

func newReviewWorld(t *testing.T, share bool) reviewWorld {
	hub, f, bobKeys, bob := found(t, "Bob’s iPhone", nil)
	alice, carol := join(t, bob, f, hub, "Alice", nil), join(t, bob, f, hub, "Carol", nil)
	syncAll(t, bob)
	list := try(bob.CreateList(bg, "House"))
	if share {
		for _, p := range []*Device{alice, carol} {
			must(t, bob.Share(bg, list, me(t, p).Participant, Writer, ""))
			syncAll(t, p)
			must(t, p.Accept(bg, list))
		}
	}
	return reviewWorld{hub, f, bobKeys, bob, alice, carol, me(t, alice).Participant, me(t, carol).Participant, list}
}

// 1. Carol starts a home epoch for Alice and sends her its key: Alice's home doesn't take it,
// and her list keys never reach Carol's channel.
func TestReviewNobodyElseCanStartSomeonesHomeEpoch(t *testing.T) {
	w := newReviewWorld(t, false)
	carolMe, aliceMe := me(t, w.carol), me(t, w.alice)
	for i := 0; i < 8; i++ { // whatever its ID sorts as
		k := random(32)
		forged := try(HomeEpochStart(w.f.Pin, w.aliceID, 0, nil, k, [][]byte{aliceMe.Certificate}, carolMe.Certificate, carolMe.Auth, nil, carolMe.Keys))
		tag := tandem.HomeTag(k, w.f.Pin.Hub, w.aliceID, oid(forged))
		rawAppend(t, w.hub, tag, try(tandem.Seal(k, w.f.Pin.Hub, tandem.KindRequest, tag, try(cjson(forged)))))
		rawAppend(t, w.hub, tandem.InboxTag(w.aliceID, w.f.Pin.Hub),
			try(tandem.SealInbox(w.f.Pin.Hub, w.aliceID, publicAgree(aliceMe.Keys), homeKeyNotice(oid(forged), k).JSON())))
	}
	syncAll(t, w.alice)
	func() {
		defer w.alice.read()()
		for _, e := range w.alice.homeEpochs {
			if !bytes.Equal(fid(e, "signer"), aliceMe.Certificate) {
				t.Fatal("only Alice's own keychain starts her home epochs")
			}
		}
	}()
	if n := len(w.alice.HomeHeads()); n != 1 {
		t.Fatal(n)
	}
}

// 2. Removed Carol sends Alice a grant for the new epoch with a key of her own: Alice keeps the
// real key, and her next write doesn't go where Carol can read it.
func TestReviewAForgedGrantDoesntReplaceAKey(t *testing.T) {
	w := newReviewWorld(t, true)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.list))
	syncAll(t, w.carol)
	var e2 []byte
	func() {
		defer w.carol.read()()
		c := w.carol.collections[string(w.list)]
		for _, k := range sortedKeys(c.statements) {
			if c.startsEpoch([]byte(k)) && k != string(c.genesis) {
				e2 = []byte(k)
			}
		}
	}()
	if e2 == nil {
		t.Fatal("Carol doesn't see the new epoch's start")
	}
	carolMe, aliceMe := me(t, w.carol), me(t, w.alice)
	fake := bytes.Repeat([]byte{7}, 32)
	var admission []byte
	func() {
		defer w.alice.read()()
		c := w.alice.collections[string(w.list)]
		admission = admissionOf(w.aliceID, c, c.head())
	}()
	grant := newControl("grant", tandem.Fields{"hub": w.f.Pin.Hub, "collection": w.list, "recipient": w.aliceID, "admission": admission,
		"at": e2, "epoch": e2, "key": tandem.Commit(fake), "signer": carolMe.Certificate, "auth": carolMe.Auth})
	must(t, signControl(grant, "sig", carolMe.Keys.Signing))
	delivery := tandem.NewJSONObject().Text("type", "grant").Raw("grant", try(cjson(grant))).B64u("certificate", aliceMe.Certificate).
		B64u("k", fake).Bytes()
	rawAppend(t, w.hub, tandem.InboxTag(w.aliceID, w.f.Pin.Hub), try(tandem.SealInbox(w.f.Pin.Hub, w.aliceID, publicAgree(aliceMe.Keys), delivery)))
	syncAll(t, w.alice)
	_, real := epochKey(w.bob, w.list)
	if _, held := epochKey(w.alice, w.list); !bytes.Equal(held, real) {
		t.Fatal("Alice's key was replaced")
	}
	must(t, w.alice.SetField(bg, w.list, NewRecordID(), "title", []byte("Private")))
	carolsTag := tandem.CollectionTag(fake, w.f.Pin.Hub, e2)
	for _, m := range w.hub.Messages() {
		if m.Channel == carolsTag {
			t.Fatal("leaked into Carol's channel")
		}
	}
}

// 3. Alice's revoked tablet, still holding the old epoch's key, signs a same-epoch statement adding someone: refused.
func TestReviewARevokedOwnerKeychainCantExtendTheRoster(t *testing.T) {
	w := newReviewWorld(t, false)
	list := try(w.alice.CreateList(bg, "Alice’s"))
	tablet := pairApproved(t, w.alice, w.bob, w.f, w.hub, "Tablet")
	tabletMe := me(t, tablet)
	oldEpoch, oldKey := epochKey(tablet, list)
	must(t, w.bob.Revoke(bg, tabletMe.Certificate, w.f.ManagementSeed))
	syncAll(t, w.alice)
	must(t, w.alice.Rekey(bg, list))
	syncAll(t, tablet)
	var stmtW *tandem.Write
	func() {
		defer tablet.read()()
		c := tablet.collections[string(list)]
		head := c.head()
		next := c.statements[string(head)].s.clone()
		next.N++
		next.Members = sortMembers(append(next.Members, Member{UID: w.carolID, Role: Writer, Floor: next.Members[0].Floor}))
		stmtW = &tandem.Write{Hub: w.f.Pin.Hub, Certificate: tabletMe.Certificate, Authorization: tabletMe.Auth, Scope: list,
			Record: rosterRecord, Field: rosterField, Op: tandem.OpAssign, Value: next.JSON(), Author: w.aliceID,
			Dot: tandem.Dot{Device: tabletMe.Installation, Millis: 1}, Ref: head}
	}()
	must(t, stmtW.Sign(tabletMe.Keys.Signing))
	tag := tandem.CollectionTag(oldKey, w.f.Pin.Hub, oldEpoch)
	rawAppend(t, w.hub, tag, try(tandem.Seal(oldKey, w.f.Pin.Hub, tandem.KindRecord, tag, try(tandem.RecordPayload(rosterRecord, []*tandem.Write{stmtW})))))
	syncAll(t, w.alice)
	if memberOf(w.alice, list, w.carolID) {
		t.Fatal("the revoked tablet added Carol")
	}
}

// announcementsOf is the messages in a channel that carry the statement id.
func announcementsOf(t *testing.T, w reviewWorld, tag string, key, id []byte) []string {
	var out []string
	for _, m := range w.hub.Messages() {
		if m.Channel != tag {
			continue
		}
		_, p := try2(tandem.Open(key, w.f.Pin.Hub, tag, m.Body))
		s, _, err := tandem.ParseKind1(p, w.f.Pin.Hub, w.list)
		if err != nil || s == nil {
			continue
		}
		for _, x := range s.Writes {
			if bytes.Equal(writeID(x), id) {
				out = append(out, m.ID)
				break
			}
		}
	}
	return out
}

// 4. Removed Carol deletes the announcement; Alice, offline throughout, still moves to the new
// epoch through her grant, and her writes don't land where Carol reads.
func TestReviewADeletedAnnouncementDoesntStrandAMember(t *testing.T) {
	w := newReviewWorld(t, true)
	oldEpoch, oldKey := epochKey(w.bob, w.list)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.list))
	newEpoch, _ := epochKey(w.bob, w.list)
	oldTag := tandem.CollectionTag(oldKey, w.f.Pin.Hub, oldEpoch)
	announcement := announcementsOf(t, w, oldTag, oldKey, newEpoch)
	if len(announcement) == 0 {
		t.Fatal("no announcement")
	}
	w.hub.Drop(announcement)
	syncAll(t, w.alice)
	if e, _ := epochKey(w.alice, w.list); !bytes.Equal(e, newEpoch) {
		t.Fatal("Alice is stranded in the old epoch")
	}
	item := NewRecordID()
	must(t, w.alice.SetField(bg, w.list, item, "title", []byte("Private")))
	syncAll(t, w.bob)
	if it, _ := w.bob.Item(w.list, item); it.record == nil || it.Text("title") != "Private" {
		t.Fatal("Bob doesn't see it")
	}
	// And Bob's devices put the announcement back.
	if len(announcementsOf(t, w, oldTag, oldKey, newEpoch)) == 0 {
		t.Fatal("the announcement wasn't restored")
	}
}

// 5. A write another keychain of Alice made late in the old epoch isn't signed again by her phone.
func TestReviewOnlyTheAuthoringInstallationSignsALateWriteAgain(t *testing.T) {
	w := newReviewWorld(t, true)
	tablet := pairApproved(t, w.alice, w.bob, w.f, w.hub, "Tablet")
	syncAll(t, w.alice)
	w.bob.setCrashPoint("3:roster")
	if err := w.bob.RemoveMember(bg, w.carolID, w.list); !isCrash(err) {
		t.Fatal(err)
	}
	thief := NewRecordID()
	must(t, tablet.SetField(bg, w.list, thief, "title", []byte("thief")))
	syncAll(t, w.alice)
	w.bob.setCrashPoint("")
	must(t, w.bob.Resume(bg))
	syncAll(t, w.alice, w.bob)
	if it, ok := w.bob.Item(w.list, thief); ok && it.Text("title") != "" {
		t.Fatal("the phone signed the tablet's write as its own")
	}
}

// 6. A second installation of Bob's that last synced before a removal shares the list: it syncs
// first, so the list doesn't get two successors of one statement.
func TestReviewAStaleDeviceSharingDoesntKillTheList(t *testing.T) {
	w := newReviewWorld(t, false)
	must(t, w.bob.Share(bg, w.list, w.aliceID, Writer, ""))
	bob2 := open(t, w.f, w.bobKeys, nil, w.hub.Connect())
	syncAll(t, bob2)
	must(t, w.bob.RemoveMember(bg, w.aliceID, w.list))
	must(t, bob2.Share(bg, w.list, w.carolID, Writer, ""))
	syncAll(t, w.bob)
	if w.bob.IsDead(w.list) || bob2.IsDead(w.list) {
		t.Fatal("dead")
	}
	if !memberOf(w.bob, w.list, w.carolID) || memberOf(w.bob, w.list, w.aliceID) {
		t.Fatal("members")
	}
}

// 7. Carol is added after a key change: she reads, writes and is listed.
func TestReviewAMemberAddedAfterAKeyChangeCanUseTheList(t *testing.T) {
	w := newReviewWorld(t, false)
	must(t, w.bob.SetField(bg, w.list, NewRecordID(), "title", []byte("Before")))
	must(t, w.bob.Rekey(bg, w.list))
	must(t, w.bob.Rekey(bg, w.list))
	must(t, w.bob.Share(bg, w.list, w.carolID, Writer, ""))
	syncAll(t, w.carol)
	must(t, w.carol.Accept(bg, w.list))
	if w.carol.Role(w.list) != Writer {
		t.Fatal("role")
	}
	if got := titles(w.carol, w.list); !sameSet(got, set("Before")) {
		t.Fatal(got)
	}
	mine := NewRecordID()
	must(t, w.carol.SetField(bg, w.list, mine, "title", []byte("Carol’s")))
	after := NewRecordID()
	must(t, w.bob.SetField(bg, w.list, after, "title", []byte("After")))
	syncAll(t, w.bob, w.carol)
	if value_(w.bob, w.list, mine, "title") != "Carol’s" || value_(w.carol, w.list, after, "title") != "After" {
		t.Fatal("writes don't flow")
	}
}

// The seventh fix: a write under an epoch this device doesn't count yet is held, not dropped,
// and applied once the epoch counts.
func TestReviewAWriteUnderANotYetCountedEpochIsHeld(t *testing.T) {
	w := newReviewWorld(t, true)
	oldEpoch, oldKey := epochKey(w.bob, w.list)
	must(t, w.bob.RemoveMember(bg, w.carolID, w.list))
	newEpoch, _ := epochKey(w.bob, w.list)
	// Alice's device sees the new channel before the announcement: hide the old channel from her.
	oldTag := tandem.CollectionTag(oldKey, w.f.Pin.Hub, oldEpoch)
	w.hub.Drop(announcementsOf(t, w, oldTag, oldKey, newEpoch))
	item := NewRecordID()
	must(t, w.bob.SetField(bg, w.list, item, "title", []byte("Fresh")))
	syncAll(t, w.alice)
	if value_(w.alice, w.list, item, "title") != "Fresh" {
		t.Fatal("the write in the new epoch was dropped")
	}
}
