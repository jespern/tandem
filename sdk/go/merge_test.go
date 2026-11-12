package tandem

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// Randomized checks of §9.3 on arbitrary (honest or malformed) vectors:
// supersession is a strict partial order, and merging converges whatever the
// order, duplication or grouping of writes.

func randomWrites(r *rand.Rand, n int) []Entry {
	devs := [][8]byte{{1}, {2}, {3}}
	epochs := []struct {
		id string
		n  int64
	}{{"e0", 0}, {"e1", 1}}
	dot := func() Dot {
		return Dot{Device: devs[r.IntN(len(devs))], Millis: uint64(r.IntN(4)), Counter: uint16(r.IntN(2))}
	}
	vv := func() VV {
		var vs []VV
		for range r.IntN(3) {
			vs = append(vs, VV{dot()})
		}
		return Join(vs...)
	}
	var out []Entry
	for range n {
		ep := epochs[r.IntN(len(epochs))]
		op := []Op{OpAssign, OpAssign, OpAdd, OpRemove, OpDelete}[r.IntN(5)]
		field := "f"
		if op == OpDelete {
			field = ""
		}
		w := &Write{Scope: make([]byte, 16), Record: make([]byte, 16), Field: field, Op: op,
			Author: make([]byte, 32), Dot: dot(), Ref: append([]byte{byte(ep.n)}, make([]byte, 31)...), Context: vv(), Base: vv()} // the ref fixes the epoch
		switch op {
		case OpAssign:
			w.Value = []byte{byte(r.IntN(3))}
		case OpAdd, OpRemove:
			w.Element = []byte{byte(r.IntN(2))}
		}
		e, err := NewEntry(w, []byte(ep.id), ep.n)
		if err != nil {
			panic(err)
		}
		out = append(out, e)
	}
	return out
}

func TestSupersessionIsAStrictPartialOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 300 {
		ws := randomWrites(r, 12)
		for _, a := range ws {
			if Supersedes(a, a) {
				t.Fatal("irreflexivity")
			}
			for _, b := range ws {
				if Supersedes(a, b) && Supersedes(b, a) {
					t.Fatal("asymmetry")
				}
				for _, c := range ws {
					if Supersedes(a, b) && Supersedes(b, c) && !Supersedes(a, c) {
						t.Fatalf("transitivity: %+v %+v %+v", a.W, b.W, c.W)
					}
				}
			}
		}
	}
}

func keptIDs(rec *Record) []string { return slices.Clone(rec.order) }

func TestMergeConverges(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 300 {
		ws := randomWrites(r, 15)
		all := NewRecord(nil)
		all.Merge(ws...)
		// Any order, with duplicates, in any grouping, gives the same state.
		for range 5 {
			perm := slices.Clone(ws)
			r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			perm = append(perm, perm[:r.IntN(len(perm))]...)
			a, b := NewRecord(nil), NewRecord(nil)
			cut := r.IntN(len(perm))
			a.Merge(perm[:cut]...)
			b.Merge(perm[cut:]...)
			// Merge states: union of kept sets (garbage-collected states).
			for _, id := range b.order {
				a.Merge(b.Kept[id])
			}
			if !slices.Equal(keptIDs(a), keptIDs(all)) {
				t.Fatal("merge depends on order or grouping")
			}
			if a.Deleted() != all.Deleted() {
				t.Fatal("deletion depends on order")
			}
			va, oka := a.Value("f")
			vb, okb := all.Value("f")
			if oka != okb || string(va) != string(vb) {
				t.Fatal("displayed value depends on order")
			}
		}
	}
}

// The familiar cases of §9.2 with honest vectors.
func TestHonestMerge(t *testing.T) {
	p, q := [8]byte{1}, [8]byte{2}
	mk := func(op Op, el string, d Dot, ctx VV) Entry {
		w := &Write{Scope: make([]byte, 16), Record: make([]byte, 16), Field: "tags", Op: op, Element: []byte(el),
			Author: make([]byte, 32), Dot: d, Ref: make([]byte, 32), Context: ctx, Base: VV{}}
		if op == OpAssign {
			w.Field, w.Element, w.Value = "title", nil, []byte(el)
		}
		e, _ := NewEntry(w, []byte("e"), 0)
		return e
	}
	add := mk(OpAdd, "x", Dot{p, 1, 0}, VV{})
	concurrentRemove := mk(OpRemove, "x", Dot{q, 1, 0}, VV{})
	rec := NewRecord(nil)
	rec.Merge(add, concurrentRemove)
	if len(rec.Elements("tags")) != 1 {
		t.Fatal("a concurrent add and remove leave the element in (add wins)")
	}
	sawRemove := mk(OpRemove, "x", Dot{q, 2, 0}, VV{{p, 1, 0}, {q, 1, 0}})
	rec.Merge(sawRemove)
	if len(rec.Elements("tags")) != 0 {
		t.Fatal("a remove that saw every add takes it out")
	}
	rec.Merge(add) // replay
	if len(rec.Elements("tags")) != 0 {
		t.Fatal("a replayed add stays superseded")
	}

	a := mk(OpAssign, "A", Dot{p, 5, 0}, VV{})
	b := mk(OpAssign, "B", Dot{q, 3, 0}, VV{})
	rec = NewRecord(nil)
	rec.Merge(a, b)
	if v, _ := rec.Value("title"); string(v) != "A" || len(rec.Siblings("title")) != 2 {
		t.Fatal("concurrent assigns are siblings; the greatest HLC is shown")
	}
	ctx, base, ok := rec.Vectors(OpAssign, "title", nil, "e", 0)
	c := mk(OpAssign, "C", Dot{q, 4, 0}, ctx)
	c.W.Base = base
	c, _ = NewEntry(c.W, []byte("e"), 0)
	rec.Merge(c)
	if v, _ := rec.Value("title"); !ok || string(v) != "C" || len(rec.Siblings("title")) != 1 {
		t.Fatal("a write covering all siblings resolves the conflict")
	}
}

// A delete supersedes every write of the record it includes, in any field,
// so a tombstone that saw the record leaves one write; a write concurrent
// with it survives under it; nothing but a delete supersedes a delete.
func TestDeleteSupersedesWhatItSaw(t *testing.T) {
	p, q := [8]byte{1}, [8]byte{2}
	mk := func(op Op, field string, d Dot, ctx VV) Entry {
		w := &Write{Scope: make([]byte, 16), Record: make([]byte, 16), Field: field, Op: op,
			Author: make([]byte, 32), Dot: d, Ref: make([]byte, 32), Context: ctx, Base: VV{}}
		switch op {
		case OpAssign:
			w.Value = []byte("v")
		case OpAdd:
			w.Element = []byte("x")
		}
		e, err := NewEntry(w, []byte("e"), 0)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	title := mk(OpAssign, "title", Dot{p, 1, 0}, VV{})
	tag := mk(OpAdd, "tags", Dot{p, 2, 0}, VV{})
	rec := NewRecord(nil)
	rec.Merge(title, tag)
	ctx, _, _ := rec.Vectors(OpDelete, "", nil, "e", 0)
	del := mk(OpDelete, "", Dot{q, 3, 0}, ctx)
	concurrent := mk(OpAssign, "notes", Dot{p, 4, 0}, VV{{p, 2, 0}})
	rec.Merge(del, concurrent)
	if !rec.Deleted() || len(rec.Kept) != 2 {
		t.Fatalf("kept %d writes, want the delete and the concurrent edit", len(rec.Kept))
	}
	cover := mk(OpAssign, "notes", Dot{p, 5, 0}, Join(reach(del.W), reach(concurrent.W)))
	rec.Merge(cover)
	if !rec.Deleted() {
		t.Fatal("an assign covering the delete must not supersede it")
	}
	if _, err := (&Write{Scope: make([]byte, 16), Record: make([]byte, 16), Field: "title", Op: OpDelete,
		Author: make([]byte, 32), Ref: make([]byte, 32)}).SigInput(); err == nil {
		t.Fatal("a delete with a field name is invalid")
	}
}
