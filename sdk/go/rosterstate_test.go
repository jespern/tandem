package tandem

import (
	"bytes"
	"testing"
)

type world struct {
	t       *testing.T
	j, a, b *User
	dev     [8]byte
	clock   uint64
	r       *Roster
	col     []byte
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, dev: [8]byte{9}}
	w.j, _ = NewUser(bytes.Repeat([]byte{1}, 32))
	w.a, _ = NewUser(bytes.Repeat([]byte{2}, 32))
	w.b, _ = NewUser(bytes.Repeat([]byte{3}, 32))
	g, col, _, err := Genesis(w.j, "Jesper", bytes.Repeat([]byte{7}, 32), make([]byte, 16), w.dot())
	if err != nil {
		t.Fatal(err)
	}
	if w.r, err = NewRoster(g); err != nil {
		t.Fatal(err)
	}
	w.col = col
	return w
}

func (w *world) dot() Dot { w.clock++; return Dot{Device: w.dev, Millis: w.clock} }

func member(u *User, role, name string) Member {
	return Member{UID: B64u(u.UID), Role: role, UKa: B64u(u.UKa), Name: name}
}

// stmt signs a successor of parent with the given members; key nil keeps the
// parent's key, otherwise starts an epoch with it and the inventory inv.
func (w *world) stmt(parent []byte, members []Member, key []byte, inv Inventory) *Write {
	p, _ := w.r.Get(parent)
	st := Statement{N: p.Body.N + 1, Key: EpochKey{N: p.Body.KeyN, Commit: B64u(p.Body.Commit)}, Members: members}
	if p.Body.Prev != nil {
		st.Key.Prev = B64u(p.Body.Prev)
	}
	if p.Body.Snapshot != nil {
		st.Snapshot = B64u(p.Body.Snapshot)
	}
	if key != nil {
		b, _ := inv.Bytes()
		st.Key = EpochKey{N: p.Body.KeyN + 1, Commit: B64u(Commit(key)), Prev: B64u(p.Body.Commit)}
		st.Snapshot = B64u(H(b))
	}
	s, err := NextStatement(w.j, w.col, parent, st, w.dot())
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *world) add(s *Write, inEpoch []byte) []byte {
	if err := w.r.Add(s, inEpoch); err != nil {
		w.t.Fatal(err)
	}
	id, _ := s.ID()
	return id
}

func (w *world) data(u *User, ref []byte, title string) *Write {
	d := &Write{Scope: w.col, Record: bytes.Repeat([]byte{5}, 16), Field: "title", Op: OpAssign, Value: []byte(title),
		Dot: w.dot(), Ref: ref, Context: VV{}, Base: VV{}}
	if err := d.Sign(u); err != nil {
		w.t.Fatal(err)
	}
	return d
}

func TestRosterLifecycle(t *testing.T) {
	w := newWorld(t)
	g := w.r.Genesis
	owner := member(w.j, "owner", "Jesper")
	aimee := member(w.a, "writer", "Aimee")
	aimee.Via = B64u(bytes.Repeat([]byte{4}, 16))
	bo := member(w.b, "reader", "Bo")

	s1 := w.add(w.stmt(g, []Member{owner, aimee}, nil, nil), g)
	s2 := w.add(w.stmt(s1, []Member{owner, aimee, bo}, nil, nil), g)
	if !bytes.Equal(w.r.Head().ID, s2) || w.r.Dead() {
		t.Fatal("same-epoch statements count at once")
	}

	w1 := w.data(w.a, s1, "milk")
	if v, err := w.r.Judge(w1, g, nil); v != Accept {
		t.Fatalf("a writer's write: %v %v", v, err)
	}
	if v, _ := w.r.Judge(w.data(w.b, s2, "x"), g, nil); v != Reject {
		t.Fatal("a reader's write is rejected")
	}

	// Remove Aimee: a new epoch whose inventory carries w1.
	id1, _ := w1.ID()
	inv := Inventory{string(w1.Record): {id1}}
	rot := w.stmt(s2, []Member{owner, bo}, bytes.Repeat([]byte{8}, 32), inv)
	s3 := w.add(rot, nil) // found in its own channel only: a draft
	if s3draft := w.r.Head(); !bytes.Equal(s3draft.ID, s2) {
		t.Fatal("an unannounced statement is a draft, not part of the roster")
	}
	w.add(rot, g) // the announcement, in the old channel
	if !bytes.Equal(w.r.Head().ID, s3) || !bytes.Equal(w.r.EpochOf(s3), s3) {
		t.Fatal("announced: the head, and the start of a new epoch")
	}

	if v, err := w.r.Judge(w1, s3, inv); v != Accept {
		t.Fatalf("a listed write enters the new epoch: %v %v", v, err)
	}
	if v, _ := w.r.Judge(w.data(w.a, s1, "late"), s3, inv); v != Reject {
		t.Fatal("a backdated write absent from the inventory is rejected")
	}
	if v, _ := w.r.Judge(w.data(w.a, s1, "late"), s3, nil); v != Hold {
		t.Fatal("without the inventory, an old write is held")
	}
	if v, _ := w.r.Judge(w.data(w.a, s3, "x"), s3, inv); v != Reject {
		t.Fatal("a removed member can't write under the new statement")
	}

	// A second epoch start from s2, made by another device of the owner.
	other := w.stmt(s2, []Member{owner, bo}, bytes.Repeat([]byte{6}, 32), inv)
	w.add(other, nil)
	if w.r.Dead() {
		t.Fatal("a draft is not half of a conflict")
	}
	w.add(other, g)
	if !w.r.Dead() {
		t.Fatal("two announced successors of one statement kill the collection")
	}
}

func TestSameEpochConflict(t *testing.T) {
	w := newWorld(t)
	g := w.r.Genesis
	owner := member(w.j, "owner", "Jesper")
	w.add(w.stmt(g, []Member{owner, member(w.a, "writer", "Aimee")}, nil, nil), g)
	w.add(w.stmt(g, []Member{owner, member(w.b, "reader", "Bo")}, nil, nil), g)
	if !w.r.Dead() {
		t.Fatal("two same-epoch successors kill the collection")
	}
}

func TestInvalidStatements(t *testing.T) {
	w := newWorld(t)
	g := w.r.Genesis
	owner := member(w.j, "owner", "Jesper")
	s1 := w.add(w.stmt(g, []Member{owner, member(w.a, "writer", "Aimee")}, nil, nil), g)
	cases := map[string]*Write{
		"removal without a new epoch":  w.stmt(s1, []Member{owner}, nil, nil),
		"demotion without a new epoch": w.stmt(s1, []Member{owner, member(w.a, "reader", "Aimee")}, nil, nil),
		"a new epoch adding a member": w.stmt(s1, []Member{owner, member(w.a, "writer", "Aimee"), member(w.b, "reader", "Bo")},
			bytes.Repeat([]byte{8}, 32), Inventory{}),
		"the key commitment reused": w.stmt(s1, []Member{owner}, bytes.Repeat([]byte{7}, 32), Inventory{}),
		"a renamed member":          w.stmt(s1, []Member{owner, member(w.a, "writer", "Amy")}, nil, nil),
		"a second owner":            w.stmt(s1, []Member{owner, member(w.a, "owner", "Aimee")}, nil, nil),
	}
	for name, s := range cases {
		id, _ := s.ID()
		w.r.Add(s, g)
		st, _ := w.r.Get(id)
		if w.r.Valid(st) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
