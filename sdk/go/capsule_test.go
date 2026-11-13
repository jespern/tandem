package tandem

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// fast is for everything but the vector; never on the wire.
var fast = argon2Profile{memoryKiB: 64, iterations: 1, lanes: 1}

type capsuleFixture struct {
	Password string
	Profile  struct {
		Alg     string
		Version int
		MKiB    uint32 `json:"m_kib"`
		T       uint32
		P       uint8
	}
	Salt, CK, MS, HubID, Nonce, Capsule string
}

func loadCapsuleFixture(t *testing.T) capsuleFixture {
	t.Helper()
	b, err := os.ReadFile("../../conformance/fixtures/tandem-v2-capsule.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		capsuleFixture
		HubID string `json:"hub_id"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	v.capsuleFixture.HubID = v.HubID
	return v.capsuleFixture
}

func managerHub(t *testing.T, ms []byte) []byte {
	t.Helper()
	k, err := ManagerKeys(ms)
	if err != nil {
		t.Fatal(err)
	}
	return k.Signing.Public().(ed25519.PublicKey)
}

func TestCapsuleVectorAtTheRealProfile(t *testing.T) {
	if os.Getenv("TANDEM_SKIP_ARGON2_GIB") == "1" {
		t.Skip("TANDEM_SKIP_ARGON2_GIB=1")
	}
	v := loadCapsuleFixture(t)
	if v.Profile.Alg != "argon2id" || v.Profile.Version != 0x13 || (argon2Profile{v.Profile.MKiB, v.Profile.T, v.Profile.P}) != capsuleProfile {
		t.Fatal("the fixture's profile is not the capsule profile")
	}
	if ck := StretchPassword([]byte(v.Password), unhex(v.Salt)); !bytes.Equal(ck, unhex(v.CK)) {
		t.Fatal("CK")
	}
	ms, err := OpenCapsule(v.Password, unhex(v.HubID), unhex(v.Capsule))
	if err != nil || !bytes.Equal(ms, unhex(v.MS)) {
		t.Fatal(err)
	}
}

func TestCapsuleVectorFromCK(t *testing.T) {
	v := loadCapsuleFixture(t)
	sealed, err := SealCapsuleCK(unhex(v.CK), unhex(v.HubID), unhex(v.MS), unhex(v.Salt), unhex(v.Nonce))
	if err != nil || !bytes.Equal(sealed, unhex(v.Capsule)) {
		t.Fatal("sealing from CK", err)
	}
	ms, err := OpenCapsuleCK(unhex(v.CK), unhex(v.HubID), unhex(v.Capsule))
	if err != nil || !bytes.Equal(ms, unhex(v.MS)) {
		t.Fatal(err)
	}
	if !bytes.Equal(managerHub(t, unhex(v.MS)), unhex(v.HubID)) {
		t.Fatal("hub")
	}
}

func newSeed(t *testing.T) ([]byte, []byte) {
	ms := make([]byte, 32)
	rand.Read(ms)
	return ms, managerHub(t, ms)
}

func TestCapsuleRoundTripNFCAndWrongPasswords(t *testing.T) {
	ms, hub := newSeed(t)
	password := "correct horse battery staple"
	c, err := sealCapsule(password, hub, ms, fast)
	if err != nil || len(c) != CapsuleLength {
		t.Fatal(err)
	}
	if got, err := openCapsule(password, hub, c, fast); err != nil || !bytes.Equal(got, ms) {
		t.Fatal(err)
	}
	other, _ := sealCapsule(password, hub, ms, fast)
	if bytes.Equal(other[:28], c[:28]) {
		t.Fatal("salt and nonce must be fresh")
	}
	composed, decomposed := "kålrabi og æbler", "kålrabi og æbler"
	sealed, err := sealCapsule(composed, hub, ms, fast)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openCapsule(decomposed, hub, sealed, fast); err != nil || !bytes.Equal(got, ms) {
		t.Fatal("NFC", err)
	}
	for _, wrong := range []string{"correct horse battery stapl", "Correct horse battery staple", " correct horse battery staple", "short", ""} {
		if _, err := openCapsule(wrong, hub, c, fast); !errors.Is(err, ErrCapsulePassword) {
			t.Fatal(wrong, err)
		}
	}
	if _, err := openCapsule(password, hub, c, argon2Profile{64, 2, 1}); err == nil {
		t.Fatal("another profile opened it")
	}
}

func TestCapsuleIsBoundToItsHubSaltAndSeed(t *testing.T) {
	ms, hub := newSeed(t)
	password := "correct horse battery staple"
	c, _ := sealCapsule(password, hub, ms, fast)
	_, otherHub := newSeed(t)
	if _, err := openCapsule(password, otherHub, c, fast); err == nil {
		t.Fatal("another hub")
	}
	for _, i := range []int{0, 15, 16, 27, 28, len(c) - 1} {
		bad := bytes.Clone(c)
		bad[i] ^= 1
		if _, err := openCapsule(password, hub, bad, fast); err == nil {
			t.Fatal("tampered at", i)
		}
	}
	for _, n := range []int{0, 75, 77} {
		if _, err := openCapsule(password, hub, append(bytes.Clone(c), 0)[:n], fast); err == nil {
			t.Fatal("length", n)
		}
	}
	salt, nonce := bytes.Repeat([]byte{7}, 16), bytes.Repeat([]byte{9}, 12)
	foreign, _ := SealCapsuleCK(stretch([]byte(password), salt, fast), hub, bytes.Repeat([]byte{1}, 32), salt, nonce)
	if _, err := openCapsule(password, hub, foreign, fast); !errors.Is(err, ErrCapsuleHub) {
		t.Fatal(err)
	}
	if _, err := sealCapsule(password, otherHub, ms, fast); err == nil {
		t.Fatal("sealed another hub's seed")
	}
}

func TestCapsulePasswordLengthWhenChosen(t *testing.T) {
	ms, hub := newSeed(t)
	c, _ := sealCapsule("correct horse battery staple", hub, ms, fast)
	seal := func(p string) error { _, err := sealCapsule(p, hub, ms, fast); return err }
	if seal("hunter2") == nil {
		t.Fatal("short")
	}
	if err := seal(strings.Repeat("\U0001F469‍\U0001F4BB", 4)); err != nil {
		t.Fatal("12 code points", err)
	}
	if seal(strings.Repeat("é", 6)) == nil {
		t.Fatal("6 code points after NFC")
	}
	for _, ok := range []string{strings.Repeat("a", 1024), strings.Repeat("æ", 512), strings.Repeat("é", 512)} {
		if err := seal(ok); err != nil {
			t.Fatal(err)
		}
	}
	for _, long := range []string{strings.Repeat("a", 1025), strings.Repeat("æ", 512) + "a", strings.Repeat("é", 1000)} {
		if err := seal(long); err == nil || !strings.Contains(err.Error(), "1,024") {
			t.Fatal(err)
		}
		if _, err := openCapsule(long, hub, c, fast); !errors.Is(err, ErrCapsulePassword) {
			t.Fatal("unlocking checks no length", err)
		}
	}
}

func TestJSONObjectWritesAsPython(t *testing.T) {
	got := NewJSONObject().Text("type", "a\"b\\\n\x01é").B64u("k", []byte{0xfb, 0xff}).Int("n", 7).Bool("d", false).
		Raw("m", JSONArray(NewJSONObject().Int("x", 1).Bytes(), []byte("null"))).Bytes()
	want := `{"type":"a\"b\\\n\u0001é","k":"-_8","n":7,"d":false,"m":[{"x":1},null]}`
	if string(got) != want {
		t.Fatal(string(got))
	}
	j, err := ParseJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := j.Text("type"); s != "a\"b\\\n\x01é" {
		t.Fatal(s)
	}
	if b, _ := j.B64u("k", 2); !bytes.Equal(b, []byte{0xfb, 0xff}) {
		t.Fatal(b)
	}
	if string(NewJSONObject().Bytes()) != "{}" {
		t.Fatal("empty")
	}
}
