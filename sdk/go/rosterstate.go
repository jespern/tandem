package tandem

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
)

// The roster chain (§8.2, §9.4) and data validity (§8.3).

// Body is a parsed roster statement.
type Body struct {
	N        int64
	KeyN     int64
	Commit   []byte
	Prev     []byte // nil at genesis
	Snapshot []byte // nil at genesis
	Members  []Member2
	Deleted  bool
}

// Member2 is a parsed member (Member is the JSON form).
type Member2 struct {
	UID, UKa []byte
	Role     string
	Name     string // as bytes
	Via      []byte // nil if none
}

var roles = map[string]int{"reader": 1, "writer": 2, "owner": 3}

// ParseBody reads a statement's exact value bytes as protocol JSON.
func ParseBody(value []byte) (*Body, error) {
	j, err := ParseJSON(value)
	if err != nil {
		return nil, err
	}
	if j.Kind != KindObject {
		return nil, errors.New("tandem: statement is not an object")
	}
	b := &Body{}
	if b.N, err = j.int("n"); err != nil {
		return nil, err
	}
	key, ok := j.Get("key")
	if !ok || key.Kind != KindObject {
		return nil, errors.New("tandem: statement has no key")
	}
	if b.KeyN, err = key.int("n"); err != nil {
		return nil, err
	}
	if b.Commit, err = key.bytes("commit", 32); err != nil {
		return nil, err
	}
	if _, ok := key.Get("prev"); ok {
		if b.Prev, err = key.bytes("prev", 32); err != nil {
			return nil, err
		}
	}
	if _, ok := j.Get("snapshot"); ok {
		if b.Snapshot, err = j.bytes("snapshot", 32); err != nil {
			return nil, err
		}
	}
	if d, ok := j.Get("deleted"); ok {
		if d.Kind != KindBool {
			return nil, errors.New("tandem: deleted is not a boolean")
		}
		b.Deleted = d.Bool
	}
	ms, ok := j.Get("members")
	if !ok || ms.Kind != KindArray {
		return nil, errors.New("tandem: statement has no members")
	}
	for _, m := range ms.Items {
		var x Member2
		if x.UID, err = m.bytes("uid", 32); err != nil {
			return nil, err
		}
		if x.UKa, err = m.bytes("uka", 32); err != nil {
			return nil, err
		}
		if x.Role, err = m.str("role"); err != nil {
			return nil, err
		}
		if x.Name, err = m.str("name"); err != nil {
			return nil, err
		}
		if _, ok := m.Get("via"); ok {
			if x.Via, err = m.bytes("via", 16); err != nil {
				return nil, err
			}
		}
		b.Members = append(b.Members, x)
	}
	return b, nil
}

func (b *Body) member(uid []byte) (Member2, bool) {
	for _, m := range b.Members {
		if bytes.Equal(m.UID, uid) {
			return m, true
		}
	}
	return Member2{}, false
}

// Role returns uid's role in this statement, or "".
func (b *Body) Role(uid []byte) string {
	m, _ := b.member(uid)
	return m.Role
}

// Stmt is a statement with its write and parsed body.
type Stmt struct {
	W    *Write
	ID   []byte
	Body *Body // nil if the value doesn't parse: still counts toward a conflict
}

// Roster holds every owner-signed statement a device has, and follows the
// chain from the genesis.
type Roster struct {
	Collection []byte
	Owner      []byte
	Genesis    []byte
	stmts      map[string]*Stmt
	children   map[string][]string // parent ID → child IDs
	announced  map[string]bool     // epoch-starting statements seen announced
}

// NewRoster starts from a genesis statement, which fixes the collection and
// its owner for good (§7.1).
func NewRoster(genesis *Write) (*Roster, error) {
	if err := genesis.Verify(); err != nil {
		return nil, err
	}
	id, _ := genesis.ID()
	body, err := ParseBody(genesis.Value)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(genesis.Scope, make([]byte, 16)) || !bytes.Equal(genesis.Ref, zero32) || !isStatementWrite(genesis) ||
		body.N != 0 || body.KeyN != 0 || body.Prev != nil || body.Snapshot != nil || body.Deleted ||
		len(body.Members) != 1 || body.Members[0].Role != "owner" || body.Members[0].Via != nil ||
		!bytes.Equal(body.Members[0].UID, genesis.Author) {
		return nil, errors.New("tandem: not a valid genesis")
	}
	r := &Roster{Collection: id[:16], Owner: genesis.Author, Genesis: id,
		stmts: map[string]*Stmt{}, children: map[string][]string{}, announced: map[string]bool{}}
	r.stmts[string(id)] = &Stmt{genesis, id, body}
	return r, nil
}

func isStatementWrite(w *Write) bool {
	return bytes.Equal(w.Record, RosterID) && w.Field == "s" && w.Op == OpAssign &&
		w.Element == nil && len(w.Context) == 0 && len(w.Base) == 0
}

// Add records an owner-signed statement found in a channel. inEpoch is the
// epoch-start ID of the channel it was found in; an epoch-starting statement
// counts (is announced) once it is found in its parent's epoch (§9.4, §11.6).
// Statements whose parent hasn't arrived are kept and count once it does.
func (r *Roster) Add(w *Write, inEpoch []byte) error {
	if !bytes.Equal(w.Author, r.Owner) || !bytes.Equal(w.Record, RosterID) {
		return errors.New("tandem: not the owner's statement")
	}
	if err := w.Verify(); err != nil {
		return err
	}
	id, _ := w.ID()
	if _, ok := r.stmts[string(id)]; !ok {
		body, _ := ParseBody(w.Value) // an unparseable statement still counts toward a conflict
		r.stmts[string(id)] = &Stmt{w, id, body}
		r.children[string(w.Ref)] = append(r.children[string(w.Ref)], string(id))
	}
	if inEpoch != nil {
		if p, ok := r.stmts[string(w.Ref)]; ok && bytes.Equal(r.EpochOf(p.ID), inEpoch) {
			r.announced[string(id)] = true
		}
	}
	return nil
}

// Announce marks an epoch-starting statement as announced, as a verified
// grant for it does (§11.6).
func (r *Roster) Announce(id []byte) { r.announced[string(id)] = true }

// starts reports whether s starts an epoch: a new key commitment.
func (r *Roster) starts(s *Stmt) bool {
	p, ok := r.stmts[string(s.W.Ref)]
	return ok && s.Body != nil && p.Body != nil && !bytes.Equal(s.Body.Commit, p.Body.Commit)
}

// counts reports whether a statement is part of the roster: anything but an
// epoch-starting statement not yet announced, which is a draft (§9.4).
func (r *Roster) counts(s *Stmt) bool {
	if bytes.Equal(s.ID, r.Genesis) {
		return true
	}
	if s.Body == nil {
		return true // can't tell; an unreadable owner-signed successor still conflicts
	}
	return !r.starts(s) || r.announced[string(s.ID)]
}

// Conflict returns two counted successors of one statement, if any (§9.4).
func (r *Roster) Conflict() (a, b *Stmt) {
	parents := make([]string, 0, len(r.children))
	for p := range r.children {
		parents = append(parents, p)
	}
	slices.Sort(parents)
	for _, p := range parents {
		if _, ok := r.stmts[p]; !ok {
			continue
		}
		var counted []*Stmt
		for _, c := range r.children[p] {
			if s := r.stmts[c]; r.counts(s) {
				counted = append(counted, s)
			}
		}
		if len(counted) >= 2 {
			slices.SortFunc(counted, func(x, y *Stmt) int { return bytes.Compare(x.ID, y.ID) })
			return counted[0], counted[1]
		}
	}
	return nil, nil
}

// Dead reports whether the collection is dead.
func (r *Roster) Dead() bool { a, _ := r.Conflict(); return a != nil }

// Valid reports whether s is a valid successor of its parent (§8.2). Its
// validity depends only on the two of them and what precedes the parent.
func (r *Roster) Valid(s *Stmt) error {
	p, ok := r.stmts[string(s.W.Ref)]
	switch {
	case !ok:
		return errors.New("parent missing")
	case s.Body == nil || p.Body == nil:
		return errors.New("statement doesn't parse")
	case !bytes.Equal(s.W.Scope, r.Collection) || !isStatementWrite(s.W):
		return errors.New("not a statement of this collection")
	case p.Body.Deleted:
		return errors.New("a deleted statement has no successors")
	case s.Body.N != p.Body.N+1:
		return errors.New("n must be one higher")
	}
	owners := 0
	seen := map[string]bool{}
	for _, m := range s.Body.Members {
		if seen[string(m.UID)] {
			return errors.New("a member twice")
		}
		seen[string(m.UID)] = true
		if roles[m.Role] == 0 {
			return fmt.Errorf("unknown role %q", m.Role)
		}
		if m.Role == "owner" {
			owners++
			if !bytes.Equal(m.UID, r.Owner) {
				return errors.New("the owner never changes")
			}
		}
		if old, ok := p.Body.member(m.UID); ok &&
			(!bytes.Equal(old.UKa, m.UKa) || old.Name != m.Name || !bytes.Equal(old.Via, m.Via)) {
			return errors.New("a member's uka, name and via never change")
		}
	}
	if owners != 1 {
		return errors.New("exactly one owner")
	}
	reducing, adding := false, false
	for _, old := range p.Body.Members {
		if roles[s.Body.Role(old.UID)] < roles[old.Role] {
			reducing = true
		}
	}
	for _, m := range s.Body.Members {
		if _, ok := p.Body.member(m.UID); !ok {
			adding = true
		}
	}
	if !r.starts(s) {
		if reducing {
			return errors.New("removing or demoting needs a new epoch")
		}
		if s.Body.KeyN != p.Body.KeyN || !bytes.Equal(s.Body.Prev, p.Body.Prev) || !bytes.Equal(s.Body.Snapshot, p.Body.Snapshot) {
			return errors.New("a same-epoch statement keeps the key and snapshot")
		}
		return nil
	}
	switch {
	case adding:
		return errors.New("a statement that starts an epoch adds no member")
	case s.Body.KeyN != p.Body.KeyN+1:
		return errors.New("the epoch counter advances by one")
	case !bytes.Equal(s.Body.Prev, p.Body.Commit):
		return errors.New("prev names the previous commitment")
	case s.Body.Snapshot == nil:
		return errors.New("a new epoch commits an inventory")
	}
	for q := p; q != nil; q = r.stmts[string(q.W.Ref)] { // a fresh key
		if q.Body != nil && bytes.Equal(q.Body.Commit, s.Body.Commit) {
			return errors.New("the key commitment occurs earlier")
		}
		if bytes.Equal(q.ID, r.Genesis) {
			break
		}
	}
	return nil
}

// Head follows the chain from the genesis through counted, valid
// statements. Processing stops in a dead collection, but the last head is
// still returned for display.
func (r *Roster) Head() *Stmt {
	s := r.stmts[string(r.Genesis)]
	for {
		var next *Stmt
		for _, c := range r.children[string(s.ID)] {
			cs := r.stmts[c]
			if r.counts(cs) && r.Valid(cs) == nil {
				next = cs
				break
			}
		}
		if next == nil {
			return s
		}
		s = next
	}
}

// Get returns a statement the device holds.
func (r *Roster) Get(id []byte) (*Stmt, bool) { s, ok := r.stmts[string(id)]; return s, ok }

// OnChain reports whether a statement is on the followed chain.
func (r *Roster) OnChain(id []byte) bool {
	for s := r.Head(); ; s = r.stmts[string(s.W.Ref)] {
		if bytes.Equal(s.ID, id) {
			return true
		}
		if bytes.Equal(s.ID, r.Genesis) {
			return false
		}
	}
}

// EpochOf is the epoch-start ID of a statement's epoch: going back, the
// first statement with the same key commitment (§8.3).
func (r *Roster) EpochOf(id []byte) []byte {
	s, ok := r.stmts[string(id)]
	if !ok || s.Body == nil {
		return nil
	}
	for !bytes.Equal(s.ID, r.Genesis) {
		p, ok := r.stmts[string(s.W.Ref)]
		if !ok || p.Body == nil || !bytes.Equal(p.Body.Commit, s.Body.Commit) {
			break
		}
		s = p
	}
	return s.ID
}

// chainValid reports whether s and everything before it back to the
// genesis is valid. §8.3 says "a valid roster statement"; a statement valid
// only relative to an invalid parent is taken not to be (ASSUMPTIONS.md).
func (r *Roster) chainValid(s *Stmt) DataVerdict {
	for !bytes.Equal(s.ID, r.Genesis) {
		p, ok := r.stmts[string(s.W.Ref)]
		if !ok {
			return Hold
		}
		if r.Valid(s) != nil {
			return Reject
		}
		s = p
	}
	return Accept
}

// ancestor reports whether a is b or precedes it on b's chain.
func (r *Roster) ancestor(a, b []byte) bool {
	for s, ok := r.stmts[string(b)]; ok; s, ok = r.stmts[string(s.W.Ref)] {
		if bytes.Equal(s.ID, a) {
			return true
		}
		if bytes.Equal(s.ID, r.Genesis) {
			return false
		}
	}
	return false
}

// DataVerdict says what to do with a data write found in epoch channel E.
type DataVerdict int

const (
	Accept DataVerdict = iota
	Hold               // names a statement or inventory not yet here
	Reject
)

// Judge decides a data write found in the channel of epoch E (§8.3). inv is
// E's verified inventory, nil if E has none yet (the genesis epoch has none
// and needs none).
func (r *Roster) Judge(w *Write, epoch []byte, inv Inventory) (DataVerdict, error) {
	if !bytes.Equal(w.Scope, r.Collection) {
		return Reject, errors.New("another collection")
	}
	if bytes.Equal(w.Record, RosterID) {
		return Reject, errors.New("_roster holds statements, not data")
	}
	if w.Op == OpDelete && bytes.Equal(w.Record, NamedID("_meta")) {
		return Reject, errors.New("a named record is never deleted")
	}
	s, ok := r.stmts[string(w.Ref)]
	if !ok {
		return Hold, nil
	}
	if v := r.chainValid(s); v != Accept {
		if v == Hold {
			return Hold, nil
		}
		return Reject, errors.New("names a statement that isn't on a valid chain")
	}
	role := s.Body.Role(w.Author)
	if role != "owner" && role != "writer" {
		return Reject, errors.New("the statement doesn't let its author write")
	}
	if bytes.Equal(w.Record, NamedID("_meta")) && role != "owner" {
		return Reject, errors.New("only the owner writes _meta")
	}
	if err := w.Verify(); err != nil {
		return Reject, err
	}
	if bytes.Equal(r.EpochOf(w.Ref), epoch) {
		return Accept, nil
	}
	if inv == nil {
		return Hold, nil // a missing inventory holds the epoch
	}
	id, _ := w.ID()
	if slices.ContainsFunc(inv[string(w.Record)], func(x []byte) bool { return bytes.Equal(x, id) }) && r.ancestor(w.Ref, epoch) {
		return Accept, nil
	}
	return Reject, errors.New("not eligible in this epoch")
}
