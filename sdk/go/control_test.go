package tandem

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type controlVectors struct {
	Schema   json.RawMessage
	Material map[string]string
	Derived  map[string]string
	Objects  []struct {
		Name, Kind, JSON, Input, ID string
		Signers                     map[string]string
	}
	Rejects []struct{ Why, JSON string }
}

func controlFixture(t *testing.T) controlVectors {
	t.Helper()
	var v controlVectors
	fixture(t, "control.json", &v)
	return v
}

func TestControlSchemaMatchesVectors(t *testing.T) {
	v := controlFixture(t)
	mine, err := json.Marshal(ControlSchema())
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err = json.Unmarshal(mine, &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(v.Schema, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema differs from the vectors:\n%s", mine)
	}
}

func TestControlObjects(t *testing.T) {
	for _, row := range controlFixture(t).Objects {
		t.Run(row.Name, func(t *testing.T) {
			c, err := FromJSON([]byte(row.JSON), "")
			if err != nil {
				t.Fatal(err)
			}
			if c.Kind != row.Kind {
				t.Fatalf("kind %q", c.Kind)
			}
			in, err := SignedInput(c.Kind, c.Fields)
			if err != nil || hex.EncodeToString(in) != row.Input {
				t.Fatal("signed input", err)
			}
			id, err := ObjectID(c.Kind, c.Fields)
			if err != nil || hex.EncodeToString(id) != row.ID {
				t.Fatal("ID", err)
			}
			if len(c.Signatures) != len(row.Signers) {
				t.Fatalf("signatures %v", c.Signatures)
			}
			for name, key := range row.Signers {
				sig, ok := c.Signatures[name]
				if !ok || !Verify(c.Kind, c.Fields, unhex(key), sig) {
					t.Fatalf("signature %s", name)
				}
				tampered := bytes.Clone(sig)
				tampered[0] ^= 1
				if Verify(c.Kind, c.Fields, unhex(key), tampered) {
					t.Fatalf("tampered signature %s verified", name)
				}
			}
			out, err := ToJSON(c.Kind, c.Fields, c.Signatures, c.Extras)
			if err != nil || string(out) != row.JSON {
				t.Fatalf("re-serialization differs: %v\n%s", err, out)
			}
			if _, err = FromJSON([]byte(row.JSON), "leave"); row.Kind != "leave" && err == nil {
				t.Fatal("accepted an unexpected type")
			}
		})
	}
}

func TestControlRejects(t *testing.T) {
	v := controlFixture(t)
	for _, row := range v.Rejects {
		if _, err := FromJSON([]byte(row.JSON), ""); err == nil {
			t.Errorf("accepted: %s", row.Why)
		}
	}
	for _, row := range v.Objects {
		if row.Kind != "genesis" {
			continue
		}
		c, err := FromJSON([]byte(row.JSON), "genesis")
		if err != nil {
			t.Fatal(err)
		}
		var ms []Mailbox
		for i := range maxMailboxes + 1 {
			ms = append(ms, Mailbox{"imap.example.org", uint16(1000 + i), "hub@example.org"})
		}
		c.Fields["mailboxes"] = ms[:maxMailboxes]
		if _, err = SignedInput("genesis", c.Fields); err != nil {
			t.Error("refused 64 mailboxes", err)
		}
		c.Fields["mailboxes"] = ms
		if _, err = SignedInput("genesis", c.Fields); err == nil {
			t.Error("accepted 65 mailboxes")
		}
	}
}

func TestControlTextAsPythonWritesIt(t *testing.T) {
	v := controlFixture(t)
	var invitation string
	for _, row := range v.Objects {
		if row.Kind == "invitation" {
			invitation = row.JSON
		}
	}
	c, err := FromJSON([]byte(invitation), "invitation")
	if err != nil {
		t.Fatal(err)
	}
	c.Fields["label"] = "Alice's \"phone\"\n\x01\x7f\\/é😀 "
	out, err := ToJSON(c.Kind, c.Fields, c.Signatures, c.Extras)
	if err != nil {
		t.Fatal(err)
	}
	// What json.dumps(..., ensure_ascii=False, separators=(",", ":")) writes.
	want := `"label":"Alice's \"phone\"\n\u0001` + "\x7f" + `\\/é😀` + " " + `"`
	if !strings.Contains(string(out), want) {
		t.Fatalf("text escaping differs from Python: %s", out)
	}
	back, err := FromJSON(out, "invitation")
	if err != nil || back.Fields["label"] != c.Fields["label"] {
		t.Fatal("text round trip", err)
	}
}

func TestControlDerivationsAndBoxes(t *testing.T) {
	v := controlFixture(t)
	m := func(k string) []byte { return unhex(v.Material[k]) }
	d := func(k string) []byte { return unhex(v.Derived[k]) }
	manager, err := ManagerKeys(m("management_seed"))
	if err != nil {
		t.Fatal(err)
	}
	hub := []byte(manager.Signing.Public().(ed25519.PublicKey))
	pub := func(k ed25519.PrivateKey) []byte { return k.Public().(ed25519.PublicKey) }
	if !bytes.Equal(pub(InviteKey(m("invite_secret"), hub, m("invite"))), d("invite_key")) {
		t.Error("invite key")
	}
	if !bytes.Equal(pub(PairingInviteKey(m("link"), hub, m("token"))), d("pairing_invite_key")) {
		t.Error("pairing invite key")
	}
	if !bytes.Equal(PairingKey(m("link"), hub, m("token")), d("pairing_key")) {
		t.Error("pairing key")
	}
	if PairingTag(m("link"), hub, m("token")) != v.Derived["pairing_tag"] {
		t.Error("pairing tag")
	}
	if !bytes.Equal(DirectoryScope(hub), d("directory_scope")) {
		t.Error("directory scope")
	}
	if !bytes.Equal(manager.Agreement.PublicKey().Bytes(), d("manager_agreement")) {
		t.Error("manager agreement key")
	}
	plain, err := OpenPackage(m("link"), m("gate_secret"), hub, m("token"), d("package"))
	if err != nil || !bytes.Equal(plain, d("package_plaintext")) {
		t.Error("package", err)
	}
	var alice []byte
	for _, row := range v.Objects {
		if row.Name == "alice registration" {
			alice = unhex(row.ID)
			c, err := FromJSON([]byte(row.JSON), "registration")
			if err != nil {
				t.Fatal(err)
			}
			f := c.Fields
			id, err := RegistrationID(f["hub"].([]byte), f["sign"].([]byte), f["agree"].([]byte), f["enrollment"].([]byte))
			if err != nil || !bytes.Equal(id, alice) {
				t.Error("registration ID", err)
			}
		}
	}
	gate, err := OpenGate(manager.Agreement, hub, m("token"), alice, d("gate"))
	if err != nil || !bytes.Equal(gate, m("gate_secret")) {
		t.Error("gate", err)
	}
	home, err := OpenHomeRecovery(manager.Agreement, hub, m("home_epoch"), d("home_recovery"))
	if err != nil || !bytes.Equal(home, m("creator_home_key")) {
		t.Error("home recovery", err)
	}
	if _, err = OpenPackage(m("link"), make([]byte, 32), hub, m("token"), d("package")); err == nil {
		t.Error("package opened under a wrong key")
	}
	if _, err = OpenGate(manager.Agreement, hub, m("token"), hub, d("gate")); err == nil {
		t.Error("gate opened for another participant")
	}
	if _, err = OpenHomeRecovery(manager.Agreement, hub, hub, d("home_recovery")); err == nil {
		t.Error("home recovery opened for another epoch")
	}
}

func TestControlFreshBoxesRoundTrip(t *testing.T) {
	agree, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hub, token, participant := make([]byte, 32), make([]byte, 16), bytes.Repeat([]byte{7}, 32)
	secret := bytes.Repeat([]byte("A"), 32)
	gate, err := SealGate(agree.PublicKey().Bytes(), hub, token, participant, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := OpenGate(agree, hub, token, participant, gate); err != nil || !bytes.Equal(got, secret) {
		t.Fatal("gate round trip", err)
	}
	box, err := SealHomeRecovery(agree.PublicKey().Bytes(), hub, participant, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := OpenHomeRecovery(agree, hub, participant, box); err != nil || !bytes.Equal(got, secret) {
		t.Fatal("home recovery round trip", err)
	}
	link := bytes.Repeat([]byte("L"), 32)
	pkg, err := SealPackage(link, secret, hub, token, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPackage(link, secret, hub, token, pkg); err != nil || string(got) != "{}" {
		t.Fatal("package round trip", err)
	}
}
