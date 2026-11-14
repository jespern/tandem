package engine

import (
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// §11.4: a device without the key of its list's current epoch asks, once, after a safety-net delay.

func keyRequestWorld(t *testing.T) (*MemoryHub, *Founding, *tandem.DeviceKeys, *Device, *Device, []byte, []byte) {
	hub, f, keys, bob := found(t, "Bob’s iPhone", nil)
	carol := join(t, bob, f, hub, "Carol", nil)
	carol.SetKeyRequestDelay(0)
	carolID := me(t, carol).Participant
	syncAll(t, bob)
	list := try(bob.CreateList(bg, "House"))
	must(t, bob.Share(bg, list, carolID, Writer, ""))
	syncAll(t, carol)
	must(t, carol.Accept(bg, list))
	return hub, f, keys, bob, carol, carolID, list
}

// The grant of a new key is deleted and the device that kept it is gone: Carol asks, Bob's Mac answers.
func TestKeyRequestAMemberWhoseGrantWasLostAsks(t *testing.T) {
	hub, f, keys, bob, carol, carolID, list := keyRequestWorld(t)
	must(t, bob.Rekey(bg, list))
	inbox := tandem.InboxTag(carolID, f.Pin.Hub)
	var grants []string
	for _, m := range hub.Messages() {
		if m.Channel == inbox {
			grants = append(grants, m.ID)
		}
	}
	hub.Drop(grants)
	mac := open(t, f, keys, nil, hub.Connect())
	syncAll(t, mac)
	syncAll(t, carol) // misses the key, asks
	if err := carol.SetField(bg, list, NewRecordID(), "t", []byte{1}); err == nil {
		t.Fatal("no key yet")
	}
	syncAll(t, mac) // answers with the grant
	syncAll(t, carol)
	must(t, carol.SetField(bg, list, NewRecordID(), "title", []byte("Back")))
}

// Carol was away while Bob changed the key and then removed her: the grant for the middle epoch is
// gone (grants to someone no longer listed are destroyed), so only a key request tells her she was removed.
func TestKeyRequestADeviceAwayForTwoEpochsLearnsItWasRemoved(t *testing.T) {
	_, _, _, bob, carol, carolID, list := keyRequestWorld(t)
	must(t, bob.Rekey(bg, list))
	must(t, bob.RemoveMember(bg, carolID, list))
	syncAll(t, bob)
	syncAll(t, carol) // sees the first key change, has no grant for it, asks
	if carol.Role(list) != Writer {
		t.Fatal("as far as she can tell")
	}
	syncAll(t, bob) // answers with the roster
	syncAll(t, carol)
	if carol.Role(list) != "" {
		t.Fatal("she learns of her removal")
	}
}
