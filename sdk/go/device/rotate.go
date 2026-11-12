package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The journalled key change (§11.6) and finishing or discarding another
// device's (§11.7).

// journal is persisted before anything of a key change leaves the device.
type journal struct {
	Col         []byte
	Parent      []byte
	OldEpoch    []byte
	StmtID      []byte
	Stmt        string // the signed statement, as a record payload
	Key         []byte
	Inv         []byte // canonical inventory bytes
	What        string
	Step        int
	NoteWritten bool
}

func (j *journal) statement() *tandem.Write {
	rs, _, err := tandem.ParseKind1([]byte(j.Stmt), j.Col)
	if err != nil || rs == nil || len(rs.Writes) != 1 {
		return nil
	}
	return rs.Writes[0]
}

func (d *Device) saveJournal() error {
	path := filepath.Join(d.dir, "journal.json")
	if d.journal == nil {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(d.journal)
	if err != nil {
		return err
	}
	return writeFileSync(path, data)
}

const maxFragment = 32 * 1024

// Remove takes a member out, starting a new epoch (§11.6).
func (d *Device) Remove(ctx context.Context, cid, uid []byte) error {
	return d.change(ctx, cid, func(ms []tandem.Member) []tandem.Member {
		return slices.DeleteFunc(ms, func(m tandem.Member) bool { return m.UID == tandem.B64u(uid) })
	}, "remove a member")
}

// SetRole changes a member's role; lowering one starts a new epoch.
func (d *Device) SetRole(ctx context.Context, cid, uid []byte, role string) error {
	d.mu.Lock()
	_, h, _, err := d.current(cid)
	old := ""
	if err == nil && h.Body != nil {
		old = h.Body.Role(uid)
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	ranks := map[string]int{"reader": 1, "writer": 2}
	switch {
	case old == "" || old == "owner":
		return ErrNotMember
	case ranks[role] == 0:
		return fmt.Errorf("device: no role %q", role)
	case role == old:
		return nil
	case ranks[role] > ranks[old]:
		// A promotion only increases access: a same-epoch statement, no new
		// key (§7.2, §8.2).
		return d.sameEpoch(ctx, cid, func(ms []tandem.Member) []tandem.Member {
			for i := range ms {
				if ms[i].UID == tandem.B64u(uid) {
					ms[i].Role = role
				}
			}
			return ms
		})
	}
	return d.change(ctx, cid, func(ms []tandem.Member) []tandem.Member {
		for i := range ms {
			if ms[i].UID == tandem.B64u(uid) {
				ms[i].Role = role
			}
		}
		return ms
	}, "change a role")
}

// Rotate starts a new epoch with no change of members.
func (d *Device) Rotate(ctx context.Context, cid []byte) error {
	return d.change(ctx, cid, func(ms []tandem.Member) []tandem.Member { return ms }, "a new key")
}

func (d *Device) change(ctx context.Context, cid []byte, edit func([]tandem.Member) []tandem.Member, what string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Step 1: sync the current epoch and choose what to carry over.
	if err := d.sync(ctx); err != nil {
		return err
	}
	c, h, ep, err := d.ownerCheck(cid)
	if err != nil {
		return err
	}
	inv := tandem.Inventory{}
	for rec, r := range c.recs[string(ep)] {
		for id := range r.Kept {
			inv[rec] = append(inv[rec], []byte(id))
		}
	}
	// Nothing listed is dropped unseen (§8.3): entries of the current
	// inventory this device neither holds nor has seen replaced.
	for rec, ids := range c.invs[string(ep)] {
		for _, id := range ids {
			held := false
			if r, ok := c.recs[string(ep)][rec]; ok {
				_, held = r.Kept[string(id)]
			}
			if !held && !c.replacedIn(ep, id) {
				inv[rec] = append(inv[rec], id)
			}
		}
	}
	invBytes, err := inv.Bytes()
	if err != nil {
		return err
	}
	d.crash("1:chosen")

	// Step 2: the new key, statement and inventory; journal, then the note.
	k := random(32)
	st := statementFrom(h.Body, edit(membersOf(h.Body)))
	st.N++
	st.Key = tandem.EpochKey{N: h.Body.KeyN + 1, Commit: tandem.B64u(tandem.Commit(k)), Prev: tandem.B64u(h.Body.Commit)}
	st.Snapshot = tandem.B64u(tandem.H(invBytes))
	w, err := tandem.NextStatement(d.u, cid, h.ID, st, d.tick())
	if err != nil {
		return err
	}
	id, _ := w.ID()
	payload, err := tandem.RecordPayload(tandem.RosterID, []*tandem.Write{w})
	if err != nil {
		return err
	}
	d.journal = &journal{Col: cid, Parent: h.ID, OldEpoch: ep, StmtID: id, Stmt: string(payload), Key: k, Inv: invBytes, What: what, Step: 2}
	if err := d.saveJournal(); err != nil {
		return err
	}
	d.crash("2:journal")
	return d.run(ctx)
}

// writeNote puts the pending change in home, so the owner's other devices
// stay out of the way and can finish or discard it.
func (d *Device) writeNote(ctx context.Context) error {
	j := d.journal
	w := j.statement()
	b, err := tandem.ParseBody(w.Value)
	if err != nil {
		return err
	}
	note, _ := json.Marshal(Note{Parent: tandem.B64u(j.Parent), Stmt: tandem.B64u(j.StmtID), K: tandem.B64u(j.Key),
		Members: membersOf(b), What: j.What, Device: d.Name})
	if err := d.setMembership(ctx, j.Col, "flight", note); err != nil {
		return err
	}
	j.NoteWritten = true
	return d.saveJournal()
}

// run carries the journalled change on from its recorded step.
func (d *Device) run(ctx context.Context) error {
	j := d.journal
	c := d.cols[string(j.Col)]
	w := j.statement()
	if c == nil || w == nil {
		return errors.New("device: journal doesn't match the replica")
	}
	newTag := tandem.CollectionTag(j.Key, j.StmtID)
	oldKey := d.key(j.Col, j.OldEpoch)
	for j.Step <= 6 {
		switch j.Step {
		case 2:
			if !j.NoteWritten {
				if err := d.writeNote(ctx); err != nil {
					return err
				}
				d.crash("2:note")
			}
		case 3:
			d.addStatement(c, w, nil) // a draft until announced
			if err := d.sync(ctx); err != nil {
				return err
			}
			if err := d.uploadSnapshot(ctx, c, newTag); err != nil {
				return err
			}
			if err := d.publishRoster(ctx, c, j.StmtID, j.Key, j.StmtID); err != nil {
				return err
			}
			d.crash("3:roster")
			// Verify: everything committed to is on the hub (restored if not).
			if err := d.sync(ctx); err != nil {
				return err
			}
			if err := d.uploadSnapshot(ctx, c, newTag); err != nil {
				return err
			}
		case 4:
			// Last check before the change happens: the note is still ours
			// and the parent still the head. Otherwise another device has
			// discarded this change or made its own, and announcing would
			// kill the collection; the change is dropped instead.
			if err := d.sync(ctx); err != nil {
				return err
			}
			n := d.membership(j.Col).flight
			if n == nil || n.Stmt != tandem.B64u(j.StmtID) || !bytes.Equal(c.roster.Head().ID, j.Parent) {
				if !slices.ContainsFunc(c.roster.Announced(), func(id []byte) bool { return bytes.Equal(id, j.StmtID) }) {
					d.destroyChannel(ctx, newTag)
					d.journal = nil
					if err := d.saveJournal(); err != nil {
						return err
					}
					return ErrSuperseded
				}
			}
			// The announcement: the moment the change happens.
			if err := d.publishRoster(ctx, c, j.OldEpoch, oldKey, j.StmtID); err != nil {
				return err
			}
			d.addStatement(c, w, j.OldEpoch)
			d.crash("4:announced")
			if err := d.addKey(ctx, j.Col, j.StmtID, j.Key); err != nil {
				return err
			}
			if err := d.setMembership(ctx, j.Col, "flight", []byte{}); err != nil {
				return err
			}
			d.crash("4:home")
		case 5:
			b, _ := tandem.ParseBody(w.Value)
			for _, m := range b.Members {
				if !bytes.Equal(m.UID, d.u.UID) {
					if err := d.sendGrant(ctx, c, m, j.StmtID, j.StmtID, j.Key); err != nil {
						return err
					}
					d.crash("5:grant")
				}
			}
		case 6:
			if err := d.sync(ctx); err != nil {
				return err
			}
			if err := d.uploadSnapshot(ctx, c, newTag); err != nil { // the inventory is still there
				return err
			}
			if err := d.sync(ctx); err != nil {
				return err
			}
			if err := d.cleanOld(ctx, c, j, newTag); err != nil {
				return err
			}
			d.crash("6:cleaned")
		}
		j.Step++
		if err := d.saveJournal(); err != nil {
			return err
		}
	}
	d.journal = nil
	if err := d.saveJournal(); err != nil {
		return err
	}
	return d.save()
}

// uploadSnapshot puts the inventory fragments and the listed writes this
// device holds into the new channel, skipping what is already there.
func (d *Device) uploadSnapshot(ctx context.Context, c *col, tag string) error {
	j := d.journal
	parts := (len(j.Inv) + maxFragment - 1) / maxFragment
	parts = max(parts, 1)
	for i := 0; i < parts; i++ {
		present := false
		for _, m := range d.idx {
			present = present || m.Tag == tag && m.Inventory && m.Part == i && m.Processed
		}
		if present {
			continue
		}
		frag := j.Inv[i*maxFragment : min((i+1)*maxFragment, len(j.Inv))]
		p, _ := json.Marshal(map[string]any{"inventory": map[string]any{
			"epoch": tandem.B64u(j.StmtID), "part": i, "parts": parts, "bytes": tandem.B64u(frag)}})
		if err := d.append(ctx, j.Key, tag, p); err != nil {
			return err
		}
		d.crash("3:fragment")
	}
	inv, err := tandem.ParseInventory(j.Inv)
	if err != nil {
		return err
	}
	recs := make([]string, 0, len(inv))
	for rec := range inv {
		recs = append(recs, rec)
	}
	slices.Sort(recs)
	for _, rec := range recs {
		var ws []*tandem.Write
		var ids [][]byte
		for _, id := range inv[rec] {
			if w := c.find(rec, id); w != nil {
				ws = append(ws, w)
				ids = append(ids, id)
			}
		}
		if len(ws) == 0 || d.has(tag, ids) {
			continue
		}
		sortWrites(ws)
		p, err := tandem.RecordPayload([]byte(rec), ws)
		if err != nil {
			return err
		}
		if err := d.append(ctx, j.Key, tag, p); err != nil {
			return err
		}
		d.crash("3:data")
	}
	return nil
}

// find looks up a write this device accepted, kept or since superseded: a
// write superseded in an old epoch may be current in the next (replacedIn).
func (c *col) find(rec string, id []byte) *tandem.Write {
	if h, ok := c.all[string(id)]; ok && string(h.W.Record) == rec {
		return h.W
	}
	return nil
}

// cleanOld destroys the old epoch's data messages once each write in them
// is in the new channel, replaced there, or was not carried over (§11.6 S6).
// The roster messages stay for good.
func (d *Device) cleanOld(ctx context.Context, c *col, j *journal, newTag string) error {
	oldTag := tandem.CollectionTag(d.key(j.Col, j.OldEpoch), j.OldEpoch)
	inv, _ := tandem.ParseInventory(j.Inv)
	inNew := map[string]bool{}
	for _, m := range d.idx {
		if m.Tag == newTag {
			for _, id := range m.Writes {
				inNew[string(id)] = true
			}
		}
	}
	listed := map[string]bool{}
	for _, ids := range inv {
		for _, id := range ids {
			listed[string(id)] = true
		}
	}
	var doomed []string
	for hid, m := range d.idx {
		if m.Tag != oldTag || !m.Processed || m.Inventory || bytes.Equal(m.Record, tandem.RosterID) || m.Record == nil {
			continue
		}
		ok := true
		for _, id := range m.Writes {
			if listed[string(id)] && !inNew[string(id)] && !c.replacedIn(j.StmtID, id) {
				ok = false
			}
		}
		if ok {
			doomed = append(doomed, hid)
		}
	}
	slices.Sort(doomed)
	if len(doomed) == 0 {
		return nil
	}
	return d.hub.Destroy(ctx, doomed)
}

// Resume carries on a journalled change after a restart (§11.6, §11.7).
func (d *Device) Resume(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.journal == nil {
		return d.sync(ctx)
	}
	if err := d.sync(ctx); err != nil {
		return err
	}
	j := d.journal
	c := d.cols[string(j.Col)]
	w := j.statement()
	if c != nil && w != nil {
		d.addStatement(c, w, nil)
	}
	announced := c != nil && slices.ContainsFunc(c.roster.Announced(), func(id []byte) bool { return bytes.Equal(id, j.StmtID) })
	note := d.membership(j.Col).flight
	headIsParent := c != nil && bytes.Equal(c.roster.Head().ID, j.Parent)
	ours := note != nil && note.Stmt == tandem.B64u(j.StmtID) && headIsParent
	switch {
	case announced:
		// Made, by this device or by the one that finished it: carry on.
		if j.Step < 4 {
			j.Step = 4
		}
	case ours:
		// Its own note is in home, whatever the local flag says: an append
		// whose answer was lost may have landed (found by Sol). Resume.
		if !j.NoteWritten {
			j.NoteWritten = true
			j.Step = max(j.Step, 3)
		}
	case !j.NoteWritten:
		// Nothing left the device but the journal. Its statement may only
		// be used if its parent is still the head and no other device has
		// a change pending: meanwhile another device, seeing no note, may
		// have made a change of its own, and announcing this one would
		// kill the collection. Unexported bytes can be dropped safely.
		if c == nil || note != nil || !bytes.Equal(c.roster.Head().ID, j.Parent) {
			d.journal = nil
			if err := d.saveJournal(); err != nil {
				return err
			}
			if err := d.save(); err != nil {
				return err
			}
			return ErrSuperseded // the user is told the change didn't happen
		}
	default:
		// No note of its own, another device's note, or a new head: the
		// change was discarded or overtaken. A draft may always be dropped
		// (review/2026-11-12-fable.md §1): drop the journal and what was
		// uploaded.
		d.destroyChannel(ctx, tandem.CollectionTag(j.Key, j.StmtID))
		d.journal = nil
		if err := d.saveJournal(); err != nil {
			return err
		}
		if err := d.save(); err != nil {
			return err
		}
		return ErrSuperseded // the user is told the change didn't happen
	}
	return d.run(ctx)
}

func (d *Device) destroyChannel(ctx context.Context, tag string) error {
	var ids []string
	for id, m := range d.idx {
		if m.Tag == tag {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return nil
	}
	return d.hub.Destroy(ctx, ids)
}

// ErrNotMember means a role change names someone the roster doesn't list.
var ErrNotMember = errors.New("device: not a member of this collection")

// ErrSuperseded means another device discarded this device's change, or
// made one of its own, before it was announced; nothing was changed.
var ErrSuperseded = errors.New("device: the change was discarded or overtaken by another device; nothing changed")

// ErrCantFinish means another device's change didn't get far enough to be
// finished; it can only be discarded (§11.7).
var ErrCantFinish = errors.New("device: the pending change can't be finished; discard it")

// ErrNoteChanged means the pending change the user was shown is no longer
// the one in home: another device finished, discarded or replaced it.
var ErrNoteChanged = errors.New("device: the pending change was finished, discarded or replaced meanwhile")

// pendingAs returns the note in home if it still names the statement of
// the note the user was shown.
func (d *Device) pendingAs(cid []byte, seen *Note) (*Note, error) {
	n := d.membership(cid).flight
	if n == nil || seen == nil || n.Stmt != seen.Stmt {
		return nil, ErrNoteChanged
	}
	return n, nil
}

// Finish completes another device's pending change, the one shown as seen,
// if its statement and inventory are on the hub (§11.7).
func (d *Device) Finish(ctx context.Context, cid []byte, seen *Note) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.sync(ctx); err != nil {
		return err
	}
	n, err := d.pendingAs(cid, seen)
	if err != nil {
		return err
	}
	c := d.cols[string(cid)]
	if c == nil {
		return errors.New("device: collection not followed")
	}
	stmt, _ := tandem.ParseB64u(n.Stmt)
	k, _ := tandem.ParseB64u(n.K)
	parent, _ := tandem.ParseB64u(n.Parent)
	w, ok := c.stmts[string(stmt)]
	if !ok {
		return ErrCantFinish
	}
	d.assemble(c, stmt)
	inv, ok := c.invs[string(stmt)]
	if !ok {
		return ErrCantFinish
	}
	invBytes, _ := inv.Bytes()
	payload, _ := tandem.RecordPayload(tandem.RosterID, []*tandem.Write{w})
	d.journal = &journal{Col: cid, Parent: parent, OldEpoch: c.roster.EpochOf(parent), StmtID: stmt, Stmt: string(payload),
		Key: k, Inv: invBytes, What: n.What, Step: 3, NoteWritten: true}
	if err := d.saveJournal(); err != nil {
		return err
	}
	return d.run(ctx)
}

// Discard drops another device's pending change, the one shown as seen:
// the note and its key leave home, and whatever was uploaded is destroyed
// (§11.7). It does nothing if the note in home names another statement now.
func (d *Device) Discard(ctx context.Context, cid []byte, seen *Note) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.sync(ctx); err != nil {
		return err
	}
	n, err := d.pendingAs(cid, seen)
	if err != nil {
		return err
	}
	stmt, _ := tandem.ParseB64u(n.Stmt)
	k, _ := tandem.ParseB64u(n.K)
	if err := d.destroyChannel(ctx, tandem.CollectionTag(k, stmt)); err != nil {
		return err
	}
	if err := d.setMembership(ctx, cid, "flight", []byte{}); err != nil {
		return err
	}
	return d.save()
}
