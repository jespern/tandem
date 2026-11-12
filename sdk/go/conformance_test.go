package tandem

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Cross-checks against conformance/ (the independent Python and CryptoKit
// harness on main): the same bytes from a third implementation.

func readFixture(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", name))
	if err != nil {
		t.Skip("conformance fixtures not present:", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConformanceFixtures(t *testing.T) {
	var f struct {
		Secret   string
		Identity struct{ Sign, Agree, UID, UKa, Home string }
		Writes   []struct {
			Inputs struct {
				Scope, Rec, Field, Value, Author, Dev, Ref string
				Op                                         int
				Millis                                     uint64
				Counter                                    uint16
				Context, Base                              [][3]any
			}
			Input     string
			WriteID   string `json:"write_id"`
			Signature string
		}
		Symmetric struct{ Key, Salt, Epoch, Tag, Plaintext, Envelope string }
	}
	readFixture(t, "tandem-v1.json", &f)

	u, err := NewUser(unhex(t, f.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(u.Sign.Seed()) != f.Identity.Sign || hex.EncodeToString(u.UID) != f.Identity.UID ||
		hex.EncodeToString(u.UKa) != f.Identity.UKa || hex.EncodeToString(u.Home) != f.Identity.Home {
		t.Error("identity differs from the conformance fixture")
	}

	vv := func(xs [][3]any) VV {
		v := VV{}
		for _, x := range xs {
			var d Dot
			copy(d.Device[:], unhex(t, x[0].(string)))
			d.Millis, d.Counter = uint64(x[1].(float64)), uint16(x[2].(float64))
			v = append(v, d)
		}
		return v
	}
	for _, c := range f.Writes {
		in := c.Inputs
		var dev [8]byte
		copy(dev[:], unhex(t, in.Dev))
		w := &Write{Scope: unhex(t, in.Scope), Record: unhex(t, in.Rec), Field: in.Field, Op: Op(in.Op),
			Value: unhex(t, in.Value), Author: unhex(t, in.Author), Dot: Dot{dev, in.Millis, in.Counter},
			Ref: unhex(t, in.Ref), Context: vv(in.Context), Base: vv(in.Base), Sig: unhex(t, c.Signature)}
		got, err := w.SigInput()
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got) != c.Input {
			t.Errorf("sigInput for field %q differs", in.Field)
		}
		if id, _ := w.ID(); hex.EncodeToString(id) != c.WriteID {
			t.Errorf("write ID for field %q differs", in.Field)
		}
		if err := w.Verify(); err != nil {
			t.Errorf("field %q: %v", in.Field, err)
		}
	}

	s := f.Symmetric
	tag := CollectionTag(unhex(t, s.Key), unhex(t, s.Epoch))
	if tag != s.Tag {
		t.Errorf("collection tag %s, fixture %s", tag, s.Tag)
	}
	env, err := Seal(unhex(t, s.Key), KindRecord, tag, unhex(t, s.Plaintext), unhex(t, s.Salt))
	if err != nil || !bytes.Equal(env, unhex(t, s.Envelope)) {
		t.Error("symmetric envelope differs from the conformance fixture")
	}
}

func TestConformanceEd25519Policy(t *testing.T) {
	var f struct {
		SmallOrder []string `json:"small_order"`
		Cases      []struct {
			Name, Public, Signature, Message string
			Valid                            bool
		}
	}
	readFixture(t, "ed25519-policy.json", &f)
	for _, h := range f.SmallOrder {
		found := false
		for _, s := range smallOrder {
			found = found || bytes.Equal(s, unhex(t, h))
		}
		if !found {
			t.Errorf("small-order point %s missing from our list", h)
		}
	}
	for _, c := range f.Cases {
		got := VerifyStrict(unhex(t, c.Public), unhex(t, c.Message), unhex(t, c.Signature))
		if got != c.Valid {
			t.Errorf("%s: VerifyStrict %v, fixture %v", c.Name, got, c.Valid)
		}
	}
}
