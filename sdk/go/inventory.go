package tandem

import (
	"bytes"
	"errors"
	"slices"
)

type Inventory map[string][][]byte // key: string(recordID)

// Marks reports whether the inventory marks a record deleted in its epoch: an entry with no
// writes (§8.3).
func (inv Inventory) Marks(record []byte) bool {
	ws, ok := inv[string(record)]
	return ok && len(ws) == 0
}

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
		ws := slices.Clone(inv[r]) // none: a deletion mark (§8.3)
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
		ws := [][]byte{} // none: a deletion mark (§8.3)
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
