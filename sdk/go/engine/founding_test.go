package engine

import (
	"bytes"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

func TestFoundingANewHubIsValidOnItsOwnTerms(t *testing.T) {
	keys := newKeys(t)
	acct := MailboxAccount{Host: "IMAP.Example.org", Port: 993, User: "hub@example.org", Password: "app-password"}
	f := try(Found(acct, keys, "Mac", nil))
	// A device that knows only the genesis can check everything else.
	pin := try(PinGenesis(f.Genesis))
	if !bytes.Equal(pin.Genesis, f.Pin.Genesis) || !bytes.Equal(pin.Creator, f.Participant) {
		t.Fatal("pin")
	}
	r := NewRegistry(pin)
	msgs := try(f.DirectoryMessages())
	for _, m := range msgs[1 : len(msgs)-1] {
		try(r.Add(try(tandem.FromJSON(m, ""))))
	}
	cert := oid(f.Certificate)
	expect(t, r.CertificateValidity(cert), valid(), "certificate")
	expect(t, r.Authorizes(oid(f.Snapshot), cert), valid(), "snapshot")
	if !bytes.Equal(oid(r.CurrentSnapshot(f.Participant)), oid(f.Snapshot)) {
		t.Fatal("current snapshot")
	}
	// The genesis lists the mailbox, lowercased, without its password.
	boxes := f.Genesis.Fields["mailboxes"].([]tandem.Mailbox)
	if len(boxes) != 1 || boxes[0] != (tandem.Mailbox{Host: "imap.example.org", Port: 993, User: "hub@example.org"}) {
		t.Fatal(boxes)
	}
	for _, m := range msgs {
		if bytes.Contains(m, []byte("app-password")) {
			t.Fatal("the password is in the directory")
		}
	}
	// The first home epoch: signed by the creator's keychain, for its certificate, under its snapshot.
	if !cverify(f.HomeEpoch, "sig", publicSign(keys)) || len(fset(f.HomeEpoch, "recipients")) != 1 ||
		!bytes.Equal(fset(f.HomeEpoch, "recipients")[0], cert) || !bytes.Equal(fid(f.HomeEpoch, "auth"), oid(f.Snapshot)) ||
		!bytes.Equal(fid(f.HomeEpoch, "key"), tandem.Commit(f.HomeKey)) {
		t.Fatal("home epoch")
	}
	// The key reaches the creator's inbox, and the hub password's MS can recover it (§6.8).
	delivered := try(tandem.OpenInbox(pin.Hub, f.Participant, keys.Agreement, f.HomeKeyDelivery))
	n := try(ParseNotice(delivered, "home-key", "epoch", "k"))
	if !bytes.Equal(n.Get("epoch"), oid(f.HomeEpoch)) || !bytes.Equal(n.Get("k"), f.HomeKey) {
		t.Fatal("delivery")
	}
	manager := try(tandem.ManagerKeys(f.ManagementSeed))
	rec := try(ParseNotice(f.HomeRecovery.JSON(), "home-recovery", "epoch", "bytes"))
	if k := try(tandem.OpenHomeRecovery(manager.Agreement, pin.Hub, rec.Get("epoch"), rec.Get("bytes"))); !bytes.Equal(k, f.HomeKey) {
		t.Fatal("recovery")
	}
}

func TestFoundingIDsDependOnlyOnTheInputs(t *testing.T) {
	keys, secrets := newKeys(t), RandomFoundingSecrets()
	a := try(Found(account, keys, "Mac", &secrets))
	b := try(Found(account, keys, "Mac", &secrets))
	if !bytes.Equal(oid(a.Genesis), oid(b.Genesis)) || !bytes.Equal(oid(a.Certificate), oid(b.Certificate)) ||
		!bytes.Equal(oid(a.HomeEpoch), oid(b.HomeEpoch)) {
		t.Fatal("IDs differ")
	}
	other := try(Found(account, keys, "Mac", nil))
	if bytes.Equal(other.Pin.Hub, a.Pin.Hub) {
		t.Fatal("same hub")
	}
}
