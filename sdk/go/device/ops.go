package device

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"

	tandem "github.com/jespern/tandem/sdk/go"
)

var (
	ErrNotOwner = errors.New("device: only the owner changes sharing")
	ErrPending  = errors.New("device: another key change is pending; finish or discard it first (§11.7)")
	ErrDead     = errors.New("device: the collection is dead (§9.4)")
	ErrReadOnly = errors.New("device: this user can't write here")
)

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// NewCollection creates a collection with this user as its owner (§7.1).
func (d *Device) NewCollection(ctx context.Context, name string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := random(32)
	g, cid, epoch, err := tandem.Genesis(d.u, d.Name, k, random(16), d.tick())
	if err != nil {
		return nil, err
	}
	c := d.colFor(cid)
	d.addStatement(c, g, epoch)
	hname := "membership:" + tandem.B64u(cid)
	for _, f := range []struct {
		field string
		v     []byte
	}{{"_name", []byte(hname)}, {"owner", d.u.UID}, {"role", []byte("owner")}, {"state", []byte("active")}} {
		if err := d.homeWrite(ctx, hname, f.field, tandem.OpAssign, f.v, nil); err != nil {
			return nil, err
		}
	}
	if err := d.addKey(ctx, cid, epoch, k); err != nil {
		return nil, err
	}
	if err := d.publishRoster(ctx, c, epoch, k, epoch); err != nil {
		return nil, err
	}
	if err := d.set(ctx, cid, tandem.NamedID("_meta"), "name", []byte(name), tandem.OpAssign); err != nil {
		return nil, err
	}
	return cid, d.save()
}

// chain is the statements from the genesis to id, in order.
func (c *col) chain(id []byte) []*tandem.Write {
	var out []*tandem.Write
	for w, ok := c.stmts[string(id)]; ok; w, ok = c.stmts[string(w.Ref)] {
		out = append([]*tandem.Write{w}, out...)
		if bytes.Equal(w.Ref, make([]byte, 32)) {
			break
		}
	}
	return out
}

// has reports whether a message in channel tag already holds these writes.
func (d *Device) has(tag string, ids [][]byte) bool {
	for _, m := range d.idx {
		if m.Tag != tag || !m.Processed || m.Inventory {
			continue
		}
		all := true
		for _, id := range ids {
			found := false
			for _, x := range m.Writes {
				found = found || bytes.Equal(x, id)
			}
			all = all && found
		}
		if all && len(ids) > 0 {
			return true
		}
	}
	return false
}

// publishRoster puts the full roster through upTo in an epoch's channel,
// unless a message there already holds it.
func (d *Device) publishRoster(ctx context.Context, c *col, epoch, k, upTo []byte) error {
	ws := c.chain(upTo)
	var ids [][]byte
	for _, w := range ws {
		id, _ := w.ID()
		ids = append(ids, id)
	}
	tag := tandem.CollectionTag(k, epoch)
	if d.has(tag, ids) {
		return nil
	}
	p, err := tandem.RecordPayload(tandem.RosterID, ws)
	if err != nil {
		return err
	}
	return d.append(ctx, k, tag, p)
}

func (d *Device) key(cid, epoch []byte) []byte { return d.membership(cid).keys[string(epoch)] }

// Set assigns a field of a record in a collection's current epoch.
func (d *Device) Set(ctx context.Context, cid, rec []byte, field, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.set(ctx, cid, rec, field, []byte(value), tandem.OpAssign)
}

// Delete puts a tombstone on a record (§9.2).
func (d *Device) Delete(ctx context.Context, cid, rec []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.set(ctx, cid, rec, "", nil, tandem.OpDelete)
}

func (d *Device) set(ctx context.Context, cid, rec []byte, field string, value []byte, op tandem.Op) error {
	return d.setOp(ctx, cid, rec, field, value, op)
}

// setOp makes one write: b is the value of an assign, or the element of an
// add or remove.
func (d *Device) setOp(ctx context.Context, cid, rec []byte, field string, b []byte, op tandem.Op) error {
	var value, element []byte
	switch op {
	case tandem.OpAssign:
		value = b
	case tandem.OpAdd, tandem.OpRemove:
		element = append([]byte{}, b...)
	}
	c, h, ep, err := d.current(cid)
	if err != nil {
		return err
	}
	if c.roster.Dead() {
		return ErrDead
	}
	if r := h.Body.Role(d.u.UID); r != "owner" && r != "writer" {
		return ErrReadOnly
	}
	k := d.key(cid, ep)
	if k == nil {
		return errors.New("device: no key for the current epoch")
	}
	if c.recs[string(ep)] == nil {
		c.recs[string(ep)] = map[string]*tandem.Record{}
	}
	r, ok := c.recs[string(ep)][string(rec)]
	if !ok {
		r = tandem.NewRecord(rec)
		c.recs[string(ep)][string(rec)] = r
	}
	ctxv, base, ok := r.Vectors(op, field, element, string(ep), h.Body.KeyN)
	if !ok {
		return errors.New("device: the field needs more than 1,024 vector entries (§9.2)")
	}
	w := &tandem.Write{Scope: cid, Record: rec, Field: field, Op: op, Dot: d.tick(), Ref: h.ID, Context: ctxv, Base: base}
	w.Value, w.Element = value, element
	if err := w.Sign(d.u); err != nil {
		return err
	}
	id, _ := w.ID()
	c.own[string(id)] = heldWrite{w, ep}
	d.mergeData(c, ep, w)
	if err := d.save(); err != nil {
		return err
	}
	if d.OnWrite != nil {
		d.OnWrite(cid, w)
	}
	// Offline edits work (§1.1): what can't be published now is queued and
	// published by the next sync.
	if err := d.publishRecord(ctx, c, ep, k, rec); err != nil {
		c.dirty[dirtyKey(ep, rec)] = true
		return d.save()
	}
	return nil
}

func dirtyKey(epoch, rec []byte) string { return string(epoch) + string(rec) }

// flush publishes the records queued while the hub was unreachable.
func (d *Device) flush(ctx context.Context) error {
	for _, c := range d.cols {
		for key := range c.dirty {
			ep, rec := []byte(key[:len(key)-16]), []byte(key[len(key)-16:])
			k := d.key(c.id, ep)
			if k == nil {
				continue
			}
			if err := d.publishRecord(ctx, c, ep, k, rec); err != nil {
				return err
			}
			delete(c.dirty, key)
		}
	}
	return nil
}

func (d *Device) publishRecord(ctx context.Context, c *col, epoch, k, rec []byte) error {
	r := c.recs[string(epoch)][string(rec)]
	var ws []*tandem.Write
	var ids [][]byte
	for id, e := range r.Kept {
		ws = append(ws, e.W)
		ids = append(ids, []byte(id))
	}
	tag := tandem.CollectionTag(k, epoch)
	if d.has(tag, ids) {
		return nil
	}
	sortWrites(ws)
	p, err := tandem.RecordPayload(rec, ws)
	if err != nil {
		return err
	}
	return d.append(ctx, k, tag, p)
}

// AddMember admits a user with a same-epoch statement and sends them the
// key (§11.3). Joining through an invite ends here too; the request's
// checks are the invite flow's.
func (d *Device) AddMember(ctx context.Context, cid, uid, uka []byte, name, role string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.sync(ctx); err != nil {
		return err
	}
	c, h, ep, err := d.ownerCheck(cid)
	if err != nil {
		return err
	}
	members := membersOf(h.Body)
	members = append(members, tandem.Member{UID: tandem.B64u(uid), Role: role, UKa: tandem.B64u(uka), Name: name})
	st := statementFrom(h.Body, members)
	st.N++
	w, err := tandem.NextStatement(d.u, cid, h.ID, st, d.tick())
	if err != nil {
		return err
	}
	id, _ := w.ID()
	// Between signing and publishing, the check of §11.6 step 4: if another
	// device changed the roster or started a change meanwhile, this
	// statement is dropped unpublished (review/2026-11-12-fable.md, "Missed").
	if err := d.sync(ctx); err != nil {
		return err
	}
	if !bytes.Equal(c.roster.Head().ID, h.ID) || d.membership(cid).flight != nil {
		return ErrSuperseded
	}
	d.addStatement(c, w, ep)
	if err := d.save(); err != nil {
		return err
	}
	k := d.key(cid, ep)
	if err := d.publishRoster(ctx, c, ep, k, id); err != nil {
		return err
	}
	return d.sendGrant(ctx, c, tandem.Member2{UID: uid, UKa: uka}, id, ep, k)
}

func (d *Device) ownerCheck(cid []byte) (*col, *tandem.Stmt, []byte, error) {
	c, h, ep, err := d.current(cid)
	if err != nil {
		return nil, nil, nil, err
	}
	if !bytes.Equal(c.roster.Owner, d.u.UID) {
		return nil, nil, nil, ErrNotOwner
	}
	if c.roster.Dead() {
		return nil, nil, nil, ErrDead
	}
	if d.membership(cid).flight != nil || d.journal != nil {
		return nil, nil, nil, ErrPending
	}
	return c, h, ep, nil
}

func membersOf(b *tandem.Body) []tandem.Member {
	var out []tandem.Member
	for _, m := range b.Members {
		x := tandem.Member{UID: tandem.B64u(m.UID), Role: m.Role, UKa: tandem.B64u(m.UKa), Name: m.Name}
		if m.Via != nil {
			x.Via = tandem.B64u(m.Via)
		}
		out = append(out, x)
	}
	return out
}

// statementFrom copies a statement's key and snapshot with new members.
func statementFrom(b *tandem.Body, members []tandem.Member) tandem.Statement {
	st := tandem.Statement{N: b.N, Key: tandem.EpochKey{N: b.KeyN, Commit: tandem.B64u(b.Commit)}, Members: members}
	if b.Prev != nil {
		st.Key.Prev = tandem.B64u(b.Prev)
	}
	if b.Snapshot != nil {
		st.Snapshot = tandem.B64u(b.Snapshot)
	}
	return st
}
