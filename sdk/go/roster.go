package tandem

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// Roster statements (§8.1, §9.4) and snapshot inventories (§8.3).

// Member is one entry of a roster statement.
type Member struct {
	UID  string `json:"uid"`
	Role string `json:"role"` // owner, writer, reader
	UKa  string `json:"uka"`
	Name string `json:"name"`
	Via  string `json:"via,omitempty"`
}

// EpochKey is a statement's key block.
type EpochKey struct {
	N      int64  `json:"n"`
	Commit string `json:"commit"`
	Prev   string `json:"prev,omitempty"` // absent at genesis
}

// Statement is the JSON state a roster statement carries.
//
// Provisional (not in DESIGN.md): the genesis numbers both n and key.n from
// 0, as model/owner_model.py does, and carries its 16 random bytes as
// "nonce". §7.1 requires the bytes but doesn't name the field.
type Statement struct {
	N        int64    `json:"n"`
	Key      EpochKey `json:"key"`
	Snapshot string   `json:"snapshot,omitempty"` // absent at genesis
	Members  []Member `json:"members"`
	Deleted  bool     `json:"deleted"`
	Nonce    string   `json:"nonce,omitempty"` // genesis only
}

// RosterID is the record ID of _roster.
var RosterID = NamedID("_roster")

// StatementWrite builds the write carrying a statement: an assign to field
// "s" of _roster, with the parent in ref and empty vectors. Its signed value
// is the exact JSON bytes given; nothing re-serializes them.
func StatementWrite(scope, parent []byte, body []byte, dot Dot) *Write {
	if parent == nil {
		parent = zero32
	}
	return &Write{Scope: scope, Record: RosterID, Field: "s", Op: OpAssign, Value: body,
		Dot: dot, Ref: parent, Context: VV{}, Base: VV{}}
}

// Genesis creates a collection: owner u, epoch key k, 16 random bytes nonce.
// It returns the signed statement, the collection ID and the epoch ID.
func Genesis(u *User, name string, k, nonce []byte, dot Dot) (w *Write, collection, epoch []byte, err error) {
	body, err := json.Marshal(Statement{
		N:       0,
		Key:     EpochKey{N: 0, Commit: B64u(Commit(k))},
		Members: []Member{{UID: B64u(u.UID), Role: "owner", UKa: B64u(u.UKa), Name: name}},
		Nonce:   B64u(nonce),
	})
	if err != nil {
		return nil, nil, nil, err
	}
	w = StatementWrite(make([]byte, 16), nil, body, dot)
	if err = w.Sign(u); err != nil {
		return nil, nil, nil, err
	}
	id, _ := w.ID()
	return w, id[:16], id, nil
}

// NextStatement signs a successor of parent carrying st.
func NextStatement(u *User, collection, parent []byte, st Statement, dot Dot) (*Write, error) {
	body, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	w := StatementWrite(collection, parent, body, dot)
	return w, w.Sign(u)
}

// Inventory maps record IDs to the write IDs carried into a new epoch.
type Inventory map[string][][]byte // key: string(recordID)

// Bytes is the canonical inventory encoding: u32(recordCount), then entries
// sorted by record ID, each recordID || u32(writeCount) || sorted write IDs.
func (inv Inventory) Bytes() ([]byte, error) {
	recs := make([]string, 0, len(inv))
	for r := range inv {
		if len(r) != 16 {
			return nil, errors.New("tandem: record ID must be 16 bytes")
		}
		recs = append(recs, r)
	}
	slices.Sort(recs)
	b := u32(nil, uint32(len(recs)))
	for _, r := range recs {
		ws := slices.Clone(inv[r])
		slices.SortFunc(ws, bytes.Compare)
		for i, w := range ws {
			if len(w) != 32 {
				return nil, errors.New("tandem: write ID must be 32 bytes")
			}
			if i > 0 && bytes.Equal(ws[i-1], w) {
				return nil, errors.New("tandem: write ID listed twice")
			}
		}
		b = append(b, r...)
		b = u32(b, uint32(len(ws)))
		for _, w := range ws {
			b = append(b, w...)
		}
	}
	return b, nil
}

// ParseInventory reads canonical inventory bytes, rejecting any other
// spelling: unsorted or repeated IDs, short or trailing bytes.
func ParseInventory(b []byte) (Inventory, error) {
	bad := errors.New("tandem: not a canonical inventory")
	take := func(n int) ([]byte, bool) {
		if len(b) < n {
			return nil, false
		}
		x := b[:n]
		b = b[n:]
		return x, true
	}
	n, ok := take(4)
	if !ok {
		return nil, bad
	}
	inv := Inventory{}
	var prev []byte
	for range be32(n) {
		r, ok1 := take(16)
		c, ok2 := take(4)
		if !ok1 || !ok2 || prev != nil && bytes.Compare(prev, r) >= 0 {
			return nil, bad
		}
		prev = r
		var ws [][]byte
		for range be32(c) {
			w, ok := take(32)
			if !ok || len(ws) > 0 && bytes.Compare(ws[len(ws)-1], w) >= 0 {
				return nil, bad
			}
			ws = append(ws, bytes.Clone(w))
		}
		inv[string(r)] = ws
	}
	if len(b) != 0 {
		return nil, bad
	}
	return inv, nil
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
