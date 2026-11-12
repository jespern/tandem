package tandem

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Writes and their signatures (§5.5, §9.1).

type Op uint8

const (
	OpAssign Op = 1
	OpAdd    Op = 2
	OpRemove Op = 3
	OpDelete Op = 4
)

const (
	MaxVector = 1024    // entries in a context or base (§9.2)
	maxMillis = 1 << 48 // HLC millis are u48 (§9.1)
)

// Dot is a position in one device's history: (deviceID, hlc).
type Dot struct {
	Device  [8]byte
	Millis  uint64
	Counter uint16
}

func (d Dot) less(e Dot) bool {
	return d.Millis < e.Millis || d.Millis == e.Millis && d.Counter < e.Counter
}

// VV is a version vector: at most one dot per device, sorted by device ID.
type VV []Dot

func (v VV) check() error {
	if len(v) > MaxVector {
		return errors.New("tandem: vector over 1,024 entries")
	}
	for i, d := range v {
		if d.Millis >= maxMillis {
			return errors.New("tandem: HLC millis beyond 48 bits")
		}
		if i > 0 && bytes.Compare(v[i-1].Device[:], d.Device[:]) >= 0 {
			return errors.New("tandem: vector not sorted by device, or a device twice")
		}
	}
	return nil
}

func (v VV) encode(b []byte) []byte {
	b = u16(b, uint16(len(v)))
	for _, d := range v {
		b = append(b, d.Device[:]...)
		b = u64(b, d.Millis)
		b = u16(b, d.Counter)
	}
	return b
}

// Write is one signed change to one field of one record.
type Write struct {
	Scope   []byte // 16: collection ID, home scope, or zero in a genesis
	Record  []byte // 16
	Field   string // compared as bytes, never normalized (§9.1)
	Op      Op
	Element []byte // add/remove only
	Value   []byte // assign only
	Author  []byte // 32: the author's user ID
	Dot     Dot
	Ref     []byte // 32, zero if none
	Context VV
	Base    VV
	Sig     []byte // 64
}

var zero32 = make([]byte, 32)

func (w *Write) check() error {
	switch {
	case len(w.Scope) != 16 || len(w.Record) != 16 || len(w.Author) != 32 || len(w.Ref) != 32:
		return errors.New("tandem: write has an ID of the wrong length")
	case w.Op < OpAssign || w.Op > OpDelete:
		return errors.New("tandem: unknown op")
	case (w.Op == OpAdd || w.Op == OpRemove) != (w.Element != nil):
		return errors.New("tandem: element only on add and remove")
	case w.Op != OpAssign && len(w.Value) > 0:
		return errors.New("tandem: value only on assign")
	case w.Dot.Millis >= maxMillis:
		return errors.New("tandem: HLC millis beyond 48 bits")
	}
	if err := w.Context.check(); err != nil {
		return err
	}
	return w.Base.check()
}

// SigInput is the exact byte string a write's signature covers (§5.5).
func (w *Write) SigInput() ([]byte, error) {
	if err := w.check(); err != nil {
		return nil, err
	}
	b := []byte("TANDEM-WRITE-V1")
	b = append(b, w.Scope...)
	b = append(b, w.Record...)
	b = lp(b, []byte(w.Field))
	b = u8(b, uint8(w.Op))
	b = lp(b, w.Element)
	b = append(b, H(w.Value)...)
	b = append(b, w.Author...)
	b = append(b, w.Dot.Device[:]...)
	b = u64(b, w.Dot.Millis)
	b = u16(b, w.Dot.Counter)
	b = append(b, w.Ref...)
	b = w.Context.encode(b)
	return w.Base.encode(b), nil
}

// ID is the write ID, H(sigInput).
func (w *Write) ID() ([]byte, error) {
	in, err := w.SigInput()
	if err != nil {
		return nil, err
	}
	return H(in), nil
}

// Sign sets the author and signature from the user's key.
func (w *Write) Sign(u *User) error {
	w.Author = bytes.Clone(u.UID)
	in, err := w.SigInput()
	if err != nil {
		return err
	}
	w.Sig = ed25519.Sign(u.Sign, in)
	return nil
}

// Verify checks the signature against the author's key. Whether the write is
// allowed is the roster's question (§8).
func (w *Write) Verify() error {
	in, err := w.SigInput()
	if err != nil {
		return err
	}
	if !VerifyStrict(w.Author, in, w.Sig) {
		return errors.New("tandem: bad signature")
	}
	return nil
}

// Payload JSON (§10.5). Written with encoding/json; read with ParseJSON.

type writeJSON struct {
	F  string  `json:"f"`
	Op Op      `json:"op"`
	V  *string `json:"v,omitempty"`
	E  *string `json:"e,omitempty"`
	A  string  `json:"a"`
	D  []any   `json:"d"`
	R  string  `json:"r,omitempty"`
	C  [][]any `json:"c"`
	B  [][]any `json:"b"`
	S  string  `json:"s"`
}

func dotJSON(d Dot) []any { return []any{B64u(d.Device[:]), d.Millis, d.Counter} }

func vvJSON(v VV) [][]any {
	out := [][]any{}
	for _, d := range v {
		out = append(out, dotJSON(d))
	}
	return out
}

func (w *Write) toJSON() writeJSON {
	j := writeJSON{F: w.Field, Op: w.Op, A: B64u(w.Author), D: dotJSON(w.Dot),
		C: vvJSON(w.Context), B: vvJSON(w.Base), S: B64u(w.Sig)}
	if w.Op == OpAssign {
		v := B64u(w.Value)
		j.V = &v
	}
	if w.Element != nil {
		e := B64u(w.Element)
		j.E = &e
	}
	if !bytes.Equal(w.Ref, zero32) {
		j.R = B64u(w.Ref)
	}
	return j
}

// RecordPayload is the plaintext of a kind-1 record-state message.
func RecordPayload(record []byte, writes []*Write) ([]byte, error) {
	ws := make([]writeJSON, len(writes))
	for i, w := range writes {
		ws[i] = w.toJSON()
	}
	return json.Marshal(struct {
		Rec    string      `json:"rec"`
		Writes []writeJSON `json:"writes"`
	}{B64u(record), ws})
}

func parseDot(j JSON) (Dot, error) {
	var d Dot
	if j.Kind != KindArray || len(j.Items) != 3 || j.Items[0].Kind != KindString ||
		j.Items[1].Kind != KindInt || j.Items[2].Kind != KindInt {
		return d, errors.New("tandem: malformed dot")
	}
	dev, err := parseB64uN(j.Items[0].Str, 8)
	if err != nil {
		return d, err
	}
	m, c := j.Items[1].Int, j.Items[2].Int
	if m < 0 || m >= maxMillis || c < 0 || c > 0xffff {
		return d, errors.New("tandem: dot out of range")
	}
	copy(d.Device[:], dev)
	d.Millis, d.Counter = uint64(m), uint16(c)
	return d, nil
}

func parseVV(j JSON, ok bool) (VV, error) {
	if !ok || j.Kind != KindArray {
		return nil, errors.New("tandem: vector missing")
	}
	v := VV{}
	for _, item := range j.Items {
		d, err := parseDot(item)
		if err != nil {
			return nil, err
		}
		v = append(v, d)
	}
	// The signed form is sorted by device ID (§5.5); JSON order doesn't
	// matter, a device twice does.
	slices.SortFunc(v, func(a, b Dot) int { return bytes.Compare(a.Device[:], b.Device[:]) })
	return v, v.check()
}

// parseWrite reads one write of a record payload.
func parseWrite(j JSON, scope, record []byte) (*Write, error) {
	if j.Kind != KindObject {
		return nil, errors.New("tandem: write is not an object")
	}
	w := &Write{Scope: scope, Record: record, Ref: zero32}
	var err error
	if w.Field, err = j.str("f"); err != nil {
		return nil, err
	}
	op, err := j.int("op")
	if err != nil {
		return nil, err
	}
	w.Op = Op(op)
	if op < int64(OpAssign) || op > int64(OpDelete) {
		return nil, errors.New("tandem: unknown op")
	}
	_, hasV := j.Get("v")
	_, hasE := j.Get("e")
	if hasV != (w.Op == OpAssign) || hasE != (w.Op == OpAdd || w.Op == OpRemove) {
		return nil, errors.New("tandem: v only on assign, e only on add and remove")
	}
	if hasV {
		if w.Value, err = j.bytes("v", -1); err != nil {
			return nil, err
		}
	}
	if hasE {
		if w.Element, err = j.bytes("e", -1); err != nil {
			return nil, err
		}
	}
	if w.Author, err = j.bytes("a", 32); err != nil {
		return nil, err
	}
	d, _ := j.Get("d")
	if w.Dot, err = parseDot(d); err != nil {
		return nil, err
	}
	if _, ok := j.Get("r"); ok {
		if w.Ref, err = j.bytes("r", 32); err != nil {
			return nil, err
		}
	}
	if w.Context, err = parseVV(j.Get("c")); err != nil {
		return nil, err
	}
	if w.Base, err = parseVV(j.Get("b")); err != nil {
		return nil, err
	}
	if w.Sig, err = j.bytes("s", 64); err != nil {
		return nil, err
	}
	return w, w.check()
}

// RecordState is a parsed kind-1 record-state payload.
type RecordState struct {
	Record []byte
	Writes []*Write
}

// InventoryFragment is a parsed kind-1 inventory fragment.
type InventoryFragment struct {
	Epoch       []byte
	Part, Parts int
	Bytes       []byte
}

// ParseKind1 reads a kind-1 plaintext: exactly one of a record state or an
// inventory fragment. scope is the channel's collection ID or home scope.
func ParseKind1(plaintext, scope []byte) (*RecordState, *InventoryFragment, error) {
	j, err := ParseJSON(plaintext)
	if err != nil {
		return nil, nil, err
	}
	if j.Kind != KindObject {
		return nil, nil, errors.New("tandem: payload is not an object")
	}
	_, hasRec := j.Get("rec")
	inv, hasInv := j.Get("inventory")
	if hasRec == hasInv {
		return nil, nil, errors.New("tandem: exactly one of rec and inventory")
	}
	if hasInv {
		f := &InventoryFragment{}
		if f.Epoch, err = inv.bytes("epoch", 32); err != nil {
			return nil, nil, err
		}
		part, err1 := inv.int("part")
		parts, err2 := inv.int("parts")
		if err1 != nil || err2 != nil || parts < 1 || part < 0 || part >= parts {
			return nil, nil, errors.New("tandem: bad fragment index")
		}
		f.Part, f.Parts = int(part), int(parts)
		if f.Bytes, err = inv.bytes("bytes", -1); err != nil {
			return nil, nil, err
		}
		return nil, f, nil
	}
	rs := &RecordState{}
	if rs.Record, err = j.bytes("rec", 16); err != nil {
		return nil, nil, err
	}
	ws, ok := j.Get("writes")
	if !ok || ws.Kind != KindArray {
		return nil, nil, errors.New("tandem: writes missing")
	}
	for i, item := range ws.Items {
		w, err := parseWrite(item, scope, rs.Record)
		if err != nil {
			return nil, nil, fmt.Errorf("write %d: %w", i, err)
		}
		rs.Writes = append(rs.Writes, w)
	}
	return rs, nil, nil
}
