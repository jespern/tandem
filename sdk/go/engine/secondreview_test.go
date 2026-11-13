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
