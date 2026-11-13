package engine

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"os"
	"slices"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The registry against the vectors' small hub, and the attacks the Lean registry rules out.

type vectorHub struct {
	objects map[string]*tandem.Control
	pin     HubPin
	manager *tandem.DeviceKeys
}

func (h vectorHub) id(name string) []byte { return oid(h.objects[name]) }

func loadVectorHub(t *testing.T) vectorHub {
	t.Helper()
	raw, err := os.ReadFile("../../../vectors/v2/control.json")
	must(t, err)
	var v struct {
		Objects  []struct{ Name, JSON string }
		Material map[string]string
	}
	must(t, json.Unmarshal(raw, &v))
	h := vectorHub{objects: map[string]*tandem.Control{}}
	for _, o := range v.Objects {
		c, err := tandem.FromJSON([]byte(o.JSON), "")
		must(t, err)
		h.objects[o.Name] = c
	}
	h.pin, err = PinGenesis(h.objects["genesis"])
	must(t, err)
	seed, _ := hex.DecodeString(v.Material["management_seed"])
	h.manager, err = tandem.ManagerKeys(seed)
	must(t, err)
	return h
}

func (h vectorHub) registry(t *testing.T, names []string) *Registry {
	r := NewRegistry(h.pin)
	for _, n := range names {
		if _, err := r.Add(h.objects[n]); err != nil {
			t.Fatal(n, err)
		}
	}
	return r
}

var allVectorObjects = []string{"creator certificate", "invitation", "alice phone certificate", "delegation",
	"alice tablet certificate", "tablet revoked", "alice snapshot"}

func expect(t *testing.T, got Validity, want Validity, what string) {
	t.Helper()
	if got.State != want.State || got.Reason != want.Reason || len(got.Missing) != len(want.Missing) {
		t.Fatalf("%s: %v (%q), want %v (%q)", what, got, got.Reason, want, want.Reason)
	}
	for i := range want.Missing {
		if !bytes.Equal(got.Missing[i], want.Missing[i]) {
			t.Fatalf("%s: missing %x, want %x", what, got.Missing[i], want.Missing[i])
		}
	}
}

func TestRegistryTheVectorsHub(t *testing.T) {
	h := loadVectorHub(t)
	r := h.registry(t, allVectorObjects)
	for _, n := range []string{"creator certificate", "alice phone certificate", "alice tablet certificate"} {
		expect(t, r.CertificateValidity(h.id(n)), valid(), n)
	}
	expect(t, r.RevocationValidity(h.id("tablet revoked")), valid(), "revocation")
	expect(t, r.Activity(h.id("alice phone certificate")), valid(), "phone")
	expect(t, r.Activity(h.id("alice tablet certificate")), invalid("revoked"), "tablet")
	expect(t, r.Activity(h.id("creator certificate")), valid(), "creator")
	expect(t, r.Authorizes(h.id("alice snapshot"), h.id("alice phone certificate")), valid(), "snapshot phone")
	expect(t, r.Authorizes(h.id("alice snapshot"), h.id("alice tablet certificate")), invalid("revoked"), "snapshot tablet")
	alice := r.Certificate(h.id("alice phone certificate")).Participant
	if !bytes.Equal(oid(r.CurrentSnapshot(alice)), h.id("alice snapshot")) {
		t.Fatal("the snapshot the registry computes is the one the vectors carry")
	}
}

func TestRegistryOrderDoesNotMatterAndMissingProofsHold(t *testing.T) {
	h := loadVectorHub(t)
	tablet := h.id("alice tablet certificate")
	r := NewRegistry(h.pin)
	r.Add(h.objects["alice tablet certificate"])
	expect(t, r.CertificateValidity(tablet), pending(h.id("delegation")), "1")
	r.Add(h.objects["delegation"])
	expect(t, r.CertificateValidity(tablet), pending(h.id("alice phone certificate")), "2")
	r.Add(h.objects["alice phone certificate"])
	expect(t, r.CertificateValidity(tablet), pending(h.id("invitation")), "3")
	r.Add(h.objects["invitation"])
	expect(t, r.CertificateValidity(tablet), valid(), "4")
	reversed := slices.Clone(allVectorObjects)
	slices.Reverse(reversed)
	shuffled := func() []string {
		s := slices.Clone(allVectorObjects)
		rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
		return s
	}
	for _, perm := range [][]string{allVectorObjects, reversed, shuffled(), shuffled()} {
		expect(t, h.registry(t, perm).Activity(tablet), invalid("revoked"), "permutation")
	}
}

func TestRegistryAnotherHubsObjectsAreRefused(t *testing.T) {
	h := loadVectorHub(t)
	r := NewRegistry(HubPin{Hub: h.pin.Hub, Agree: h.pin.Agree, Genesis: bytes.Repeat([]byte{9}, 32), Creator: h.pin.Creator})
	if _, err := r.Add(h.objects["creator certificate"]); err == nil {
		t.Fatal("another genesis")
	}
}

func certFor(t *testing.T, h vectorHub, request *tandem.Control, issued [][]byte) *tandem.Control {
	cert := newControl("certificate", tandem.Fields{"hub": h.pin.Hub, "genesis": h.pin.Genesis, "request": oid(request), "issued": nonNil(issued)})
	cert.Extras["body"] = request
	must(t, signControl(cert, "sig", h.manager.Signing))
	return cert
}

// The manager can't make itself a device of an existing member (existing_member_needs_own_device).
func TestRegistryTheManagerCannotMintAMembersDevice(t *testing.T) {
	h := loadVectorHub(t)
	r := h.registry(t, allVectorObjects)
	phone := r.Certificate(h.id("alice phone certificate"))
	thief, _ := tandem.NewDeviceKeys()
	for _, how := range []uint64{tandem.HowFirst, tandem.HowByDevice, tandem.HowCreatorRecovery} {
		var authority []byte
		if how == tandem.HowByDevice {
			authority = phone.ID
		}
		req := newControl("request", tandem.Fields{"hub": h.pin.Hub, "genesis": h.pin.Genesis, "participant": phone.Participant,
			"sign": publicSign(thief), "agree": publicAgree(thief), "label": "Alice’s", "how": how, "authority": authority,
			"enrollment": make([]byte, 16)})
		must(t, signControl(req, "self", thief.Signing))
		if how == tandem.HowByDevice {
			must(t, signControl(req, "approver", h.manager.Signing)) // not Alice's phone
		}
		id, err := r.Add(certFor(t, h, req, nil))
		must(t, err)
		if r.CertificateValidity(id).IsValid() {
			t.Fatal("how", how)
		}
	}
}

// A revoked approver approves nothing new (revoked_approver_approves_nothing), and revoked keys never return.
func TestRegistryRevokedKeysStayOut(t *testing.T) {
	h := loadVectorHub(t)
	r := h.registry(t, allVectorObjects)
	tablet := r.Certificate(h.id("alice tablet certificate"))
	laptop, _ := tandem.NewDeviceKeys()
	tabletKey := ed25519.NewKeyFromSeed(tandem.H([]byte("Tandem v2 public control vector: alice tablet signing")))
	if !bytes.Equal(tabletKey.Public().(ed25519.PublicKey), tablet.Sign) {
		t.Fatal("stand-in key")
	}
	approvedByTablet := func(issued [][]byte) []byte {
		req := newControl("request", tandem.Fields{"hub": h.pin.Hub, "genesis": h.pin.Genesis, "participant": tablet.Participant,
			"sign": publicSign(laptop), "agree": publicAgree(laptop), "label": "Laptop", "how": uint64(tandem.HowByDevice),
			"authority": tablet.ID, "enrollment": make([]byte, 16)})
		must(t, signControl(req, "self", laptop.Signing))
		must(t, signControl(req, "approver", tabletKey))
		id, err := r.Add(certFor(t, h, req, issued))
		must(t, err)
		return id
	}
	revocation := h.id("tablet revoked")
	expect(t, r.CertificateValidity(approvedByTablet([][]byte{revocation})), invalid("approving device revoked at issuance"), "after the cut")
	stale := approvedByTablet(nil)
	expect(t, r.CertificateValidity(stale), valid(), "stale")
	expect(t, r.Activity(stale), invalid("revoked"), "stale active")
	old := r.Certificate(h.id("alice tablet certificate")).Object
	again := newControl("certificate", tandem.Fields{"hub": h.pin.Hub, "genesis": h.pin.Genesis, "request": fid(old, "request"),
		"issued": [][]byte{revocation}})
	again.Extras["body"] = old.Extras["body"]
	must(t, signControl(again, "sig", h.manager.Signing))
	alias, err := r.Add(again)
	must(t, err)
	if r.Activity(alias).IsValid() {
		t.Fatal("a revoked keychain came back")
	}
}

func TestRegistryAForgedCapabilityOrDelegationAdmitsNobody(t *testing.T) {
	h := loadVectorHub(t)
	r := h.registry(t, []string{"creator certificate", "invitation", "alice phone certificate"})
	other, _ := tandem.NewDeviceKeys()
	forge := func(o *tandem.Control) *tandem.Control {
		c := *o
		c.Signatures = map[string][]byte{}
		must(t, signControl(&c, "sig", other.Signing))
		return &c
	}
	if _, err := r.Add(forge(h.objects["invitation"])); err == nil {
		t.Fatal("a forged capability")
	}
	_, err := r.Add(forge(h.objects["delegation"]))
	must(t, err)
	r.Add(h.objects["alice tablet certificate"])
	expect(t, r.CertificateValidity(h.id("alice tablet certificate")), invalid("delegation not signed by a device of that participant"), "delegation")
}
