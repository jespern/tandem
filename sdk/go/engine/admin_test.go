package engine

import (
	"bytes"
	"testing"
)

// The hub admin (§6.7, §6.8, §13.1). Tests named TestAdminTheHubPassword… stretch at 1 GiB.

func TestAdminACancelledInvitationIsRefused(t *testing.T) {
	hub, f, _, bob := found(t, "Bob’s iPhone", nil)
	link := try(bob.Invite(bg, "Alice", account, f.ManagementSeed, 0))
	made := bob.Invitations()
	if len(made) != 1 || made[0].Label != "Alice" || made[0].Hint != "word" || made[0].Cancelled {
		t.Fatal(made)
	}
	must(t, bob.CancelInvitation(bg, oid(link.Invitation), f.ManagementSeed))
	if after := bob.Invitations(); !after[0].Cancelled {
		t.Fatal("not cancelled")
	}
	if _, err := Join(bg, link, newKeys(t), "x", MemoryStore(), hub.Connect()); err == nil {
		t.Fatal("a cancelled invitation")
	}
}

func TestAdminTheHubPasswordChanges(t *testing.T) {
	skipArgon(t)
	hub, keys := NewMemoryHub(), newKeys(t)
	f := try(Found(account, keys, "Mac", nil))
	f.Capsule = try(NewCapsule("correct horse battery staple", f.Pin, f.ManagementSeed, nil))
	bob := open(t, f, keys, nil, hub.Connect())
	must(t, bob.Publish(bg, f))
	must(t, bob.ChangePassword(bg, "purple monkey dishwasher sky", f.ManagementSeed))
	reader := open(t, f, nil, nil, hub.Connect())
	syncAll(t, reader)
	if n := len(reader.CurrentCapsules()); n != 1 {
		t.Fatal(n)
	}
	if n := len(reader.Capsules()); n != 1 {
		t.Fatal("the old capsule was destroyed", n)
	}
	if ms := try(reader.Unlock("purple monkey dishwasher sky")); !bytes.Equal(ms, f.ManagementSeed) {
		t.Fatal("MS")
	}
	if _, err := reader.Unlock("correct horse battery staple"); err == nil {
		t.Fatal("the old password")
	}
}

// Bob, the creator, loses every device and his keychain; with the hub password a new Mac gets his home and lists.
func TestAdminTheHubPasswordRecoversTheCreator(t *testing.T) {
	skipArgon(t)
	hub, lost := NewMemoryHub(), newKeys(t)
	f := try(Found(account, lost, "Bob’s iPhone", nil))
	f.Capsule = try(NewCapsule("correct horse battery staple", f.Pin, f.ManagementSeed, nil))
	phone := open(t, f, lost, nil, hub.Connect())
	must(t, phone.Publish(bg, f))
	must(t, phone.SetHome(bg, "settings:liszto", "order", []byte{3}))
	list := try(phone.CreateList(bg, "House"))
	mac := try(RecoverCreator(bg, f.Genesis, f.DirectoryKey, "correct horse battery staple", newKeys(t), "Bob’s new Mac",
		MemoryStore(), hub.Connect()))
	defer mac.Close()
	if !bytes.Equal(me(t, mac).Participant, f.Participant) {
		t.Fatal("the same person")
	}
	if !bytes.Equal(homeValue(mac, "settings:liszto", "order"), []byte{3}) {
		t.Fatal("order")
	}
	if ls := mac.Lists(); len(ls) != 1 || !bytes.Equal(ls[0].ID, list) {
		t.Fatal(ls)
	}
	must(t, mac.SetField(bg, list, NewRecordID(), "title", []byte("Back")))
}
