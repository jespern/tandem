package engine

import (
	"context"
	"testing"
	"time"
)

// Run and Events: a change on one device reaches another by push alone, and the app hears of it.

// nextEvent is the first event matching match, or a failure after a while: a safety net, not a schedule.
func nextEvent(t *testing.T, events <-chan Event, within time.Duration, match func(Event) bool) (Event, bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return Event{}, false
			}
			if match(e) {
				return e, true
			}
		case <-deadline:
			return Event{}, false
		}
	}
}

func waitFor(t *testing.T, events <-chan Event, match func(Event) bool) Event {
	t.Helper()
	e, ok := nextEvent(t, events, 5*time.Second, match)
	if !ok {
		t.Fatal("no such event in time")
	}
	return e
}

func kind(k EventKind) func(Event) bool { return func(e Event) bool { return e.Kind == k } }

func itemsOf(list, record []byte) func(Event) bool {
	return func(e Event) bool {
		if e.Kind != EventItems || !eq(e.List, list) {
			return false
		}
		for _, r := range e.Records {
			if eq(r, record) {
				return true
			}
		}
		return false
	}
}

// run runs a device live until the test ends, and waits for Run to return.
func run(t *testing.T, d *Device) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func events(t *testing.T, d *Device) <-chan Event {
	ch, stop := d.Events()
	t.Cleanup(stop)
	return ch
}

func TestLiveAnEditArrivesByPush(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s iPhone", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	aliceID := me(t, alice).Participant
	bobEvents, aliceEvents := events(t, bob), events(t, alice)
	run(t, bob)
	run(t, alice)
	list := try(bob.CreateList(bg, "Groceries"))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	waitFor(t, aliceEvents, kind(EventOffers))
	must(t, alice.Accept(bg, list))
	milk := NewRecordID()
	must(t, alice.SetField(bg, list, milk, "title", []byte("Milk")))
	e := waitFor(t, bobEvents, itemsOf(list, milk))
	if len(e.Records) == 0 {
		t.Fatal(e)
	}
	if value_(bob, list, milk, "title") != "Milk" {
		t.Fatal("Milk")
	}
}

func TestLivePairingAndKeyChange(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	aliceID := me(t, alice).Participant
	bobEvents, aliceEvents := events(t, bob), events(t, alice)
	run(t, bob)
	run(t, alice)
	list := try(bob.CreateList(bg, "Groceries"))
	must(t, bob.Share(bg, list, aliceID, Writer, ""))
	waitFor(t, aliceEvents, kind(EventOffers))
	must(t, alice.Accept(bg, list))
	milk := NewRecordID()
	must(t, alice.SetField(bg, list, milk, "title", []byte("Milk")))
	waitFor(t, bobEvents, itemsOf(list, milk))
	link := try(alice.StartPairing(bg, "Tablet", account, 0))
	tablet := pair(t, link, "Alice’s tablet", hub)
	tabletEvents := events(t, tablet)
	run(t, tablet)
	waitFor(t, bobEvents, kind(EventClaims))
	claims := bob.PendingClaims()
	if len(claims) == 0 {
		t.Fatal("no claim")
	}
	must(t, bob.Approve(bg, claims[0].ID(), f.ManagementSeed))
	waitFor(t, tabletEvents, kind(EventApproved))
	must(t, bob.Rekey(bg, list))
	bread := NewRecordID()
	wrote := false
	for i := 0; i < 8 && !wrote; i++ {
		if err := alice.SetField(bg, list, bread, "title", []byte("Bread")); err == nil {
			wrote = true
		} else {
			nextEvent(t, aliceEvents, 2*time.Second, func(Event) bool { return true })
		}
	}
	if !wrote {
		t.Fatal("never wrote")
	}
	waitFor(t, bobEvents, itemsOf(list, bread))
}

// §10.3 wake hints: every participant's device names the same opaque tag, and a device says when it published.
func TestLiveWakeHints(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s iPhone", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	if alice.WakeTag() != bob.WakeTag() {
		t.Fatal("tags differ")
	}
	other := try(Found(account, newKeys(t), "x", nil))
	o := open(t, other, nil, nil, NewMemoryHub().Connect())
	if o.WakeTag() == bob.WakeTag() {
		t.Fatal("another hub's tag")
	}
	evs := events(t, bob)
	try(bob.CreateList(bg, "Groceries"))
	waitFor(t, evs, kind(EventPublished))
}

// Run returns when its context ends, and Close ends the event streams.
func TestLiveRunAndEventsEnd(t *testing.T) {
	_, _, _, bob := found(t, "Bob’s iPhone", nil)
	evs, _ := bob.Events()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bob.Run(ctx) }()
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	bob.Close()
	for range evs {
	}
}
