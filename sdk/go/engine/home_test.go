package engine

import (
	"bytes"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Two installations on one synced keychain (§6.3): the second needs no pairing and shares the home.
func TestHomeAKeychainThatSyncsSharesTheHome(t *testing.T) {
	hub, f, keys, phone := found(t, "iPhone", nil)
	must(t, phone.SetHome(bg, "settings:liszto", "order", []byte("groceries,house")))
	mac := open(t, f, keys, nil, hub.Connect())
	syncAll(t, mac)
	macMe, phoneMe := me(t, mac), me(t, phone)
	if !bytes.Equal(macMe.Participant, f.Participant) || !bytes.Equal(macMe.Certificate, phoneMe.Certificate) {
		t.Fatal("identity")
	}
	if macMe.Installation == phoneMe.Installation {
		t.Fatal("each installation has its own dot namespace")
	}
	if string(homeValue(mac, "settings:liszto", "order")) != "groceries,house" {
		t.Fatal("order")
	}
	must(t, mac.AddHome(bg, "settings:liszto", "pinned", []byte("groceries")))
	syncAll(t, phone)
	if p := phone.HomeElements("settings:liszto", "pinned"); len(p) != 1 || string(p[0]) != "groceries" {
		t.Fatal(p)
	}
	if string(homeValue(phone, "settings:liszto", "_name")) != "settings:liszto" {
		t.Fatal("_name")
	}
}

// A device of another hub participant can't write into this home, even holding its key.
func TestHomeOnlyTheParticipantsKeychainsWriteToItsHome(t *testing.T) {
	hub, f, _, phone := found(t, "iPhone", nil)
	epoch := oid(f.HomeEpoch)
	stranger := newKeys(t)
	w := &tandem.Write{Hub: f.Pin.Hub, Certificate: oid(f.Certificate), Authorization: oid(f.Snapshot),
		Scope: tandem.HomeScope(f.Pin.Hub, f.Participant), Record: tandem.NamedID("settings:x"), Field: "k", Op: tandem.OpAssign,
		Value: []byte{1}, Author: f.Participant, Dot: tandem.Dot{Device: [8]byte{1, 1, 1, 1, 1, 1, 1, 1}, Millis: 1}, Ref: epoch}
	must(t, w.Sign(stranger.Signing)) // not the certificate's key
	tag := tandem.HomeTag(f.HomeKey, f.Pin.Hub, f.Participant, epoch)
	p := try(tandem.RecordPayload(w.Record, []*tandem.Write{w}))
	rawAppend(t, hub, tag, try(tandem.Seal(f.HomeKey, f.Pin.Hub, tandem.KindRecord, tag, p)))
	syncAll(t, phone)
	if _, ok := phone.HomeValue("settings:x", "k"); ok {
		t.Fatal("a stranger wrote into the home")
	}
}

// Home writes and keys survive a restart: the store is the device's memory.
func TestHomeIsInTheStore(t *testing.T) {
	store := MemoryStore()
	hub, f, keys, phone := found(t, "iPhone", store)
	must(t, phone.SetHome(bg, "settings:liszto", "order", []byte{7}))
	reopened := open(t, f, keys, store, hub.Connect())
	if !bytes.Equal(homeValue(reopened, "settings:liszto", "order"), []byte{7}) {
		t.Fatal("order")
	}
	if !bytes.Equal(me(t, reopened).Participant, f.Participant) {
		t.Fatal("me")
	}
	must(t, reopened.SetHome(bg, "settings:liszto", "order", []byte{8}))
	if !bytes.Equal(homeValue(reopened, "settings:liszto", "order"), []byte{8}) {
		t.Fatal("updated")
	}
}
