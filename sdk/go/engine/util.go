package engine

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	tandem "github.com/jespern/tandem/sdk/go"
)

// ID is a 32-byte object, write or participant ID.
type ID = []byte

func fail(format string, args ...any) error { return fmt.Errorf("tandem/engine: "+format, args...) }

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func hexs(b []byte) string { return hex.EncodeToString(b) }

func unhexs(s string) ([]byte, bool) {
	b, err := hex.DecodeString(s)
	return b, err == nil
}

func sortIDs(ids [][]byte) [][]byte {
	out := slices.Clone(ids)
	slices.SortFunc(out, bytes.Compare)
	return out
}

func containsID(ids [][]byte, id []byte) bool {
	return slices.ContainsFunc(ids, func(x []byte) bool { return bytes.Equal(x, id) })
}

// sortedKeys returns a string-keyed map's keys in byte order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var zero32 = make([]byte, 32)
var zero16 = make([]byte, 16)

// Validity is three-way (§8.3): an object whose dependencies haven't arrived is pending, never invalid.
type Validity struct {
	State   ValidityState
	Reason  string   // why it is invalid
	Missing [][]byte // what it waits for
}

type ValidityState uint8

const (
	Valid ValidityState = iota
	Invalid
	Pending
)

func valid() Validity                 { return Validity{} }
func invalid(why string) Validity     { return Validity{State: Invalid, Reason: why} }
func pending(ids ...[]byte) Validity  { return Validity{State: Pending, Missing: ids} }
func (v Validity) IsValid() bool      { return v.State == Valid }
func (v Validity) IsInvalid() bool    { return v.State == Invalid }
func (v Validity) IsPending() bool    { return v.State == Pending }
func (v Validity) Is(why string) bool { return v.State == Invalid && v.Reason == why }
func (v Validity) String() string {
	switch v.State {
	case Invalid:
		return "invalid(" + v.Reason + ")"
	case Pending:
		return fmt.Sprintf("pending(%d)", len(v.Missing))
	}
	return "valid"
}

// allValid is the first non-valid outcome, merging pending dependencies.
func allValid(outcomes ...Validity) Validity {
	var missing [][]byte
	any := false
	for _, o := range outcomes {
		switch o.State {
		case Invalid:
			return o
		case Pending:
			missing = append(missing, o.Missing...)
			any = true
		}
	}
	if any {
		return Validity{State: Pending, Missing: missing}
	}
	return valid()
}

// Control object helpers. Objects held have been parsed and checked, so their
// fields have the schema's types.

func fid(c *tandem.Control, name string) []byte {
	if c == nil {
		return nil
	}
	b, _ := c.Fields[name].([]byte)
	return b
}

func fset(c *tandem.Control, name string) [][]byte {
	if c == nil {
		return nil
	}
	s, _ := c.Fields[name].([][]byte)
	return s
}

func fint(c *tandem.Control, name string) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	switch n := c.Fields[name].(type) {
	case uint64:
		return n, true
	case int:
		return uint64(n), n >= 0
	}
	return 0, false
}

func ftext(c *tandem.Control, name string) string {
	if c == nil {
		return ""
	}
	s, _ := c.Fields[name].(string)
	return s
}

func newControl(kind string, fields tandem.Fields) *tandem.Control {
	return &tandem.Control{Kind: kind, Fields: fields, Signatures: map[string][]byte{}, Extras: map[string]any{}}
}

func signControl(c *tandem.Control, name string, key ed25519.PrivateKey) error {
	sig, err := tandem.Sign(c.Kind, c.Fields, key)
	if err != nil {
		return err
	}
	c.Signatures[name] = sig
	return nil
}

func oid(c *tandem.Control) []byte {
	id, err := tandem.ObjectID(c.Kind, c.Fields)
	if err != nil {
		return nil
	}
	return id
}

func cjson(c *tandem.Control) ([]byte, error) {
	return tandem.ToJSON(c.Kind, c.Fields, c.Signatures, c.Extras)
}

func cverify(c *tandem.Control, name string, public []byte) bool {
	sig := c.Signatures[name]
	return sig != nil && tandem.Verify(c.Kind, c.Fields, public, sig)
}

func publicSign(k *tandem.DeviceKeys) []byte  { return k.Signing.Public().(ed25519.PublicKey) }
func publicAgree(k *tandem.DeviceKeys) []byte { return k.Agreement.PublicKey().Bytes() }

// writeID is a write's ID, or nil if it is malformed.
func writeID(w *tandem.Write) []byte {
	id, err := w.ID()
	if err != nil {
		return nil
	}
	return id
}

// pack and unpack keep envelopes and their tags as length-prefixed pairs, for the journal.
type tagged struct {
	tag  string
	body []byte
}

func pack(items []tagged) []byte {
	var out []byte
	lp := func(x []byte) {
		n := len(x)
		out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		out = append(out, x...)
	}
	for _, it := range items {
		lp([]byte(it.tag))
		lp(it.body)
	}
	return out
}

func unpack(data []byte) ([]tagged, error) {
	var items []tagged
	i := 0
	take := func() ([]byte, error) {
		if i+4 > len(data) {
			return nil, errors.New("journal: truncated")
		}
		n := int(data[i])<<24 | int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
		i += 4
		if n < 0 || i+n > len(data) {
			return nil, errors.New("journal: truncated")
		}
		x := bytes.Clone(data[i : i+n])
		i += n
		return x, nil
	}
	for i < len(data) {
		t, err := take()
		if err != nil {
			return nil, err
		}
		b, err := take()
		if err != nil {
			return nil, err
		}
		items = append(items, tagged{string(t), b})
	}
	return items, nil
}
