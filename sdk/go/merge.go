package tandem

import (
	"bytes"
	"slices"
)

// Field merge (§9.2) and record state (§9.3).
//
// Writes are judged inside one collection, so each carries the epoch it was
// made in: the epoch-start ID and that epoch's number (the roster's key.n).
// In home there is one epoch.

// Entry is a valid write with its epoch.
type Entry struct {
	W      *Write
	ID     []byte // write ID
	Epoch  string // epoch-start ID (bytes)
	EpochN int64  // the epoch's number, for "later"
}

// NewEntry computes the write ID.
func NewEntry(w *Write, epoch []byte, n int64) (Entry, error) {
	id, err := w.ID()
	if err != nil {
		return Entry{}, err
	}
	return Entry{W: w, ID: id, Epoch: string(epoch), EpochN: n}, nil
}

// slot is what one register holds writes for: a field, and for set ops one
// element of it. Field names compare as bytes (§9.1).
type slot struct {
	field   string
	set     bool
	element string
}

func slotOf(w *Write) slot {
	switch w.Op {
	case OpAdd, OpRemove:
		return slot{w.Field, true, string(w.Element)}
	case OpDelete:
		return slot{field: "\x00delete"} // the record's tombstones; see Supersedes
	}
	return slot{field: w.Field}
}

// vector operations

func (v VV) get(dev [8]byte) (Dot, bool) {
	i, ok := slices.BinarySearchFunc(v, dev, func(d Dot, k [8]byte) int { return bytes.Compare(d.Device[:], k[:]) })
	if !ok {
		return Dot{}, false
	}
	return v[i], true
}

// covers reports whether v is at least as large as u in every entry of u.
func (v VV) covers(u VV) bool {
	for _, d := range u {
		e, ok := v.get(d.Device)
		if !ok || e.less(d) {
			return false
		}
	}
	return true
}

func (v VV) coversDot(d Dot) bool {
	e, ok := v.get(d.Device)
	return ok && !e.less(d)
}

// Join is the entry-wise maximum.
func Join(vs ...VV) VV {
	m := map[[8]byte]Dot{}
	for _, v := range vs {
		for _, d := range v {
			if e, ok := m[d.Device]; !ok || e.less(d) {
				m[d.Device] = d
			}
		}
	}
	out := make(VV, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Dot) int { return bytes.Compare(a.Device[:], b.Device[:]) })
	return out
}

// reach is x.context joined with x's own dot.
func reach(x *Write) VV { return Join(x.Context, VV{x.Dot}) }

func includes(w, x *Write) bool {
	return w.Context.covers(reach(x)) && w.Base.covers(x.Base)
}

// Supersedes reports whether w supersedes x (§9.2). Both are writes to the
// same record. Registers are (field) for assign and (field, element) for
// add/remove, and writes in different registers never supersede each other,
// except that a delete supersedes any write of the record it includes,
// whatever its field, and only a delete supersedes a delete (decided
// 2026-11-12, review/2026-11-12-fable.md §2). A tombstone that saw the whole
// record therefore leaves one write.
func Supersedes(w, x Entry) bool {
	if bytes.Equal(w.ID, x.ID) || !bytes.Equal(w.W.Record, x.W.Record) {
		return false
	}
	wd, xd := w.W.Op == OpDelete, x.W.Op == OpDelete
	if xd && !wd || !wd && slotOf(w.W) != slotOf(x.W) {
		return false
	}
	switch {
	case w.EpochN > x.EpochN:
		return w.W.Base.covers(reach(x.W)) && w.W.Base.covers(x.W.Base)
	case w.EpochN == x.EpochN && w.Epoch == x.Epoch:
		return includes(w.W, x.W) && !includes(x.W, w.W)
	}
	return false // never a write from a later epoch
}

// Record is the state of one record: its kept writes.
type Record struct {
	ID    []byte
	Kept  map[string]Entry // by write ID
	order []string         // write IDs, sorted, for determinism
}

func NewRecord(id []byte) *Record { return &Record{ID: id, Kept: map[string]Entry{}} }

// Merge adds writes and drops whatever is superseded. Merging is a join on
// a semilattice (§9.3): order, duplicates and replays don't matter.
func (r *Record) Merge(es ...Entry) {
	for _, e := range es {
		if _, ok := r.Kept[string(e.ID)]; ok {
			continue
		}
		r.Kept[string(e.ID)] = e
	}
	for id, x := range r.Kept {
		for _, w := range r.Kept {
			if Supersedes(w, x) {
				delete(r.Kept, id)
				break
			}
		}
	}
	r.order = r.order[:0]
	for id := range r.Kept {
		r.order = append(r.order, id)
	}
	slices.Sort(r.order)
}

// Deleted reports whether the record has a kept delete (§9.2). A delete is
// never superseded by anything but another delete, so this is "has any".
func (r *Record) Deleted() bool {
	for _, id := range r.order {
		if r.Kept[id].W.Op == OpDelete {
			return true
		}
	}
	return false
}

// displayLess orders siblings by (epoch, hlc, author, deviceID, writeID).
func displayLess(a, b Entry) bool {
	if a.EpochN != b.EpochN {
		return a.EpochN < b.EpochN
	}
	if a.W.Dot.Millis != b.W.Dot.Millis || a.W.Dot.Counter != b.W.Dot.Counter {
		return a.W.Dot.less(b.W.Dot)
	}
	if c := bytes.Compare(a.W.Author, b.W.Author); c != 0 {
		return c < 0
	}
	if c := bytes.Compare(a.W.Dot.Device[:], b.W.Dot.Device[:]); c != 0 {
		return c < 0
	}
	return bytes.Compare(a.ID, b.ID) < 0
}

// Siblings returns the kept assign writes of a field, greatest first.
func (r *Record) Siblings(field string) []Entry {
	var out []Entry
	for _, id := range r.order {
		e := r.Kept[id]
		if e.W.Op == OpAssign && e.W.Field == field {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if displayLess(b, a) {
			return -1
		}
		return 1
	})
	return out
}

// Value is a field's displayed value: the greatest sibling (§9.2).
func (r *Record) Value(field string) ([]byte, bool) {
	s := r.Siblings(field)
	if len(s) == 0 {
		return nil, false
	}
	return s[0].W.Value, true
}

// Elements returns a set field's present elements: those with a kept add.
func (r *Record) Elements(field string) [][]byte {
	present := map[string]bool{}
	for _, id := range r.order {
		e := r.Kept[id].W
		if e.Op == OpAdd && e.Field == field {
			present[string(e.Element)] = true
		}
	}
	out := make([][]byte, 0, len(present))
	for el := range present {
		out = append(out, []byte(el))
	}
	slices.SortFunc(out, bytes.Compare)
	return out
}

// Vectors returns the context and base for a new write to slot s of this
// record in epoch (epoch, n): it supersedes everything the device knows
// (§9.2, "Building a write"). ok is false if a vector would pass 1,024
// entries, in which case the device can't settle the slot.
func (r *Record) Vectors(op Op, field string, element []byte, epoch string, n int64) (context, base VV, ok bool) {
	probe := &Write{Field: field, Op: op, Element: element}
	s := slotOf(probe)
	var ctxs, bases []VV
	for _, id := range r.order {
		k := r.Kept[id]
		if op != OpDelete && slotOf(k.W) != s { // a delete covers every kept write of the record
			continue
		}
		bases = append(bases, k.W.Base)
		if k.Epoch == epoch && k.EpochN == n {
			ctxs = append(ctxs, reach(k.W))
		} else {
			bases = append(bases, reach(k.W))
		}
	}
	context, base = Join(ctxs...), Join(bases...)
	return context, base, len(context) <= MaxVector && len(base) <= MaxVector
}
