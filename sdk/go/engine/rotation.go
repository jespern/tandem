package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Changing a list's key (§11.6) and finishing or discarding another owner device's change (§11.7).

// FlightNote is a key change in flight, as the flight field of the list's membership: record
// in home holds it (§6.2, §8.2).
type FlightNote struct {
	Parent, Statement, Key []byte
	Members                []Member
	// What is a short description for the user; Device the name of the device that started the change.
	What, Device string
}

func (n FlightNote) json() []byte {
	return tandem.NewJSONObject().B64u("parent", n.Parent).B64u("stmt", n.Statement).B64u("k", n.Key).
		Raw("members", membersJSON(n.Members)).Text("what", n.What).Text("device", n.Device).Bytes()
}

// noFlight is no pending change: the JSON null. A reader takes anything that isn't a
// well-formed note as none.
var noFlight = []byte("null")

func parseFlight(data []byte) *FlightNote {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return nil
	}
	parent, e1 := j.B64u("parent", 32)
	st, e2 := j.B64u("stmt", 32)
	key, e3 := j.B64u("k", 32)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil
	}
	n := &FlightNote{Parent: parent, Statement: st, Key: key}
	if ms, ok := j.Get("members"); ok && ms.Kind == tandem.KindArray {
		for _, m := range ms.Items {
			role, _ := m.Text("role")
			uid, e1 := m.B64u("uid", 32)
			floor, e2 := m.B64u("floor", 32)
			if e1 != nil || e2 != nil || (role != "owner" && role != "writer" && role != "reader") {
				continue
			}
			name, _ := m.Text("name")
			n.Members = append(n.Members, Member{UID: uid, Role: Role(role), Name: name, Floor: floor})
		}
	}
	n.What, _ = j.Text("what")
	n.Device, _ = j.Text("device")
	return n
}

func parseFlightNote(membership *tandem.Record) *FlightNote {
	v, ok := value(membership, "flight")
	if !ok {
		return nil
	}
	return parseFlight(v)
}

// rotationJournal is what a device persists before any of a key change leaves it (§11.6 step 2).
type rotationJournal struct {
	cid, parent, oldEpoch []byte
	statement             *tandem.Write
	id, key, inventory    []byte
	what                  string
	step                  int
	noteWritten           bool
}

func (j *rotationJournal) json() ([]byte, error) {
	p, err := onePayload(rosterRecord, j.statement)
	if err != nil {
		return nil, err
	}
	note := uint64(0)
	if j.noteWritten {
		note = 1
	}
	return tandem.NewJSONObject().B64u("c", j.cid).B64u("parent", j.parent).B64u("old", j.oldEpoch).B64u("stmt", p).
		B64u("k", j.key).B64u("inv", j.inventory).Text("what", j.what).Int("step", uint64(j.step)).Int("note", note).Bytes(), nil
}

func parseRotationJournal(data, hub []byte) (*rotationJournal, error) {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	r := &rotationJournal{}
	var p []byte
	for _, f := range []struct {
		name string
		n    int
		into *[]byte
	}{{"c", 16, &r.cid}, {"parent", 32, &r.parent}, {"old", 32, &r.oldEpoch}, {"stmt", -1, &p}, {"k", 32, &r.key}, {"inv", -1, &r.inventory}} {
		if *f.into, err = j.B64u(f.name, f.n); err != nil {
			return nil, err
		}
	}
	if r.statement = storedWrite(p, hub, r.cid); r.statement == nil {
		return nil, fail("journal: no statement")
	}
	r.id = writeID(r.statement)
	r.what, _ = j.Text("what")
	step, err := j.Integer("step")
	if err != nil {
		return nil, err
	}
	note, _ := j.Integer("note")
	r.step, r.noteWritten = int(step), note == 1
	return r, nil
}

// Why a change didn't happen (§11.6–11.7).
var (
	// ErrSuperseded: nothing changed: another device discarded this change, or made one of its own first.
	ErrSuperseded = errors.New("tandem/engine: the change was discarded or overtaken by another device")
	// ErrNoteChanged: the pending change the user was shown was finished, discarded or replaced meanwhile.
	ErrNoteChanged = errors.New("tandem/engine: the pending change changed meanwhile")
	// ErrCantFinish: the other device's change didn't get far enough to be finished; it can only be discarded.
	ErrCantFinish = errors.New("tandem/engine: the pending change can't be finished, only discarded")
)

// PendingError: another device of the owner has a change pending; finish or discard it first.
type PendingError struct{ Note FlightNote }

func (e *PendingError) Error() string {
	return fmt.Sprintf("tandem/engine: %s started a change (%s); finish or discard it first", e.Note.Device, e.Note.What)
}

// crashError is a crash injected at a named point, for tests (§11.6's crash points).
type crashError struct{ point string }

func (c *crashError) Error() string { return "crash at " + c.point }

func (d *Device) crash(point string) error {
	if d.crashPoint == point {
		return &crashError{point}
	}
	return nil
}

// setCrashPoint stops a key change at point ("1:chosen", "2:journal", "2:note", "3:roster",
// "4:announced", "4:home", "5:grant", "6:cleaned"), for tests.
func (d *Device) setCrashPoint(point string) {
	d.mu.Lock()
	d.crashPoint = point
	d.mu.Unlock()
}

// deviceName is this device's name for the user: its certificate's label.
func (d *Device) deviceName() string {
	if d.me == nil {
		return ""
	}
	if c := d.registry.Certificate(d.me.Certificate); c != nil {
		return c.Label
	}
	return ""
}

func (d *Device) flightNote(cid []byte) *FlightNote {
	return parseFlightNote(d.home[string(tandem.NamedID(membershipName(cid)))])
}

// PendingChange is a change another device of this owner started and hasn't finished (§11.7), to show the user.
func (d *Device) PendingChange(list []byte) *FlightNote {
	defer d.read()()
	if _, ok := d.store.Journal("rotate." + hexs(list)); ok {
		return nil
	}
	return d.flightNote(list)
}

// Changes that start an epoch

// RemoveMember removes a member, starting a new epoch with a fresh key (§8.2, §11.6).
func (d *Device) RemoveMember(ctx context.Context, participant, list []byte) error {
	defer d.begin()()
	return d.change(ctx, list, "remove a member", func(ms []Member) []Member {
		var out []Member
		for _, m := range ms {
			if !bytes.Equal(m.UID, participant) || m.Role == Owner {
				out = append(out, m)
			}
		}
		return out
	})
}

// SetRole changes a member's role. A promotion only adds access and keeps the epoch; lowering
// one starts a new epoch.
func (d *Device) SetRole(ctx context.Context, role Role, participant, list []byte) error {
	defer d.begin()()
	c := d.collections[string(list)]
	if role == Owner || c == nil {
		return fail("role: not a member of that list")
	}
	s, ok := c.headStatement()
	m := s.member(participant)
	if !ok || m == nil || m.Role == Owner {
		return fail("role: not a member of that list")
	}
	old := m.Role
	if role == old {
		return nil
	}
	edit := func(ms []Member) []Member {
		out := append([]Member(nil), ms...)
		for i := range out {
			if bytes.Equal(out[i].UID, participant) {
				out[i].Role = role
			}
		}
		return out
	}
	if rank(role) > rank(old) {
		_, _, _, err := d.sameEpoch(ctx, list, func(s *Statement) { s.Members = edit(s.Members) })
		return err
	}
	return d.change(ctx, list, "change a role", edit)
}

// Rekey starts a new epoch with the same members: after a device revocation, or when the owner asks (§6.4, §11.6).
func (d *Device) Rekey(ctx context.Context, list []byte) error {
	defer d.begin()()
	return d.change(ctx, list, "a new key", func(ms []Member) []Member { return ms })
}

// sameEpoch makes a same-epoch roster statement (§8.2): it only adds access or deletes the
// list, and keeps every floor. Before signing it syncs; between signing and publishing it
// syncs again and checks that the parent is still the head and that no note has appeared, so
// a stale device can't put a second successor on one statement.
func (d *Device) sameEpoch(ctx context.Context, cid []byte, edit func(*Statement)) (sid, epoch, key []byte, err error) {
	if _, err := d.sync(ctx); err != nil {
		return nil, nil, nil, err
	}
	me, c := d.me, d.collections[string(cid)]
	if me == nil || c == nil || c.dead() || !bytes.Equal(c.owner(), me.Participant) {
		return nil, nil, nil, fail("roster: not a list this participant owns, or it was deleted")
	}
	head := c.head()
	current, ok := c.statements[string(head)]
	epoch = c.epochOf(head)
	key = c.keys[string(epoch)]
	if !ok || key == nil || current.s.Deleted {
		return nil, nil, nil, fail("roster: not a list this participant owns, or it was deleted")
	}
	if note := d.flightNote(cid); note != nil {
		return nil, nil, nil, &PendingError{*note}
	}
	next := current.s.clone()
	next.N++
	next.Nonce = nil // only a genesis has one (§10.8)
	edit(&next)
	next.Members = sortMembers(next.Members)
	dot, err := d.nextDot(me)
	if err != nil {
		return nil, nil, nil, err
	}
	w := &tandem.Write{Hub: d.pin.Hub, Certificate: me.Certificate, Authorization: me.Auth, Scope: cid, Record: rosterRecord,
		Field: rosterField, Op: tandem.OpAssign, Value: next.JSON(), Author: me.Participant, Dot: dot, Ref: head}
	if err := w.Sign(me.Keys.Signing); err != nil {
		return nil, nil, nil, err
	}
	id := writeID(w)
	if _, err := d.sync(ctx); err != nil {
		return nil, nil, nil, err
	}
	now := d.collections[string(cid)]
	if now == nil || !bytes.Equal(now.head(), head) || now.dead() {
		return nil, nil, nil, ErrSuperseded
	}
	if note := d.flightNote(cid); note != nil {
		return nil, nil, nil, &PendingError{*note}
	}
	now.statements[string(id)] = stmt{w, next}
	p, err := onePayload(rosterRecord, w)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := d.store.PutWrite(id, cid, rosterRecord, epoch, p); err != nil {
		return nil, nil, nil, err
	}
	d.changed(Event{Kind: EventMembers, List: cid})
	d.changed(Event{Kind: EventLists})
	return id, epoch, key, d.publish(ctx, cid, epoch, rosterRecord, []*tandem.Write{w})
}

// chain is the counted roster line from the genesis to id.
func chain(c *collection, id []byte) [][]byte {
	var out [][]byte
	for x := id; x != nil; {
		s, ok := c.statements[string(x)]
		if !ok {
			break
		}
		out = append(out, x)
		if bytes.Equal(x, c.genesis) {
			break
		}
		x = s.w.Ref
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// admissionOf is the admission in force at at for participant: the first statement listing
// them after one that doesn't (§11.2).
func admissionOf(participant []byte, c *collection, at []byte) []byte {
	var admission []byte
	listed := false
	for _, id := range chain(c, at) {
		now := c.statements[string(id)].s.member(participant) != nil
		if now && !listed {
			admission = id
		}
		listed = now
	}
	if !listed {
		return nil
	}
	return admission
}

// change makes one journalled key change (§11.6 steps 1–2), then the rest of the steps.
func (d *Device) change(ctx context.Context, cid []byte, what string, edit func([]Member) []Member) error {
	if _, ok := d.store.Journal("rotate." + hexs(cid)); ok || d.changing[string(cid)] {
		return fail("rotate: a change of this list is in progress here")
	}
	d.changing[string(cid)] = true
	defer delete(d.changing, string(cid))
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	me, c := d.me, d.collections[string(cid)]
	if me == nil || c == nil || c.dead() || !bytes.Equal(c.owner(), me.Participant) {
		return fail("rotate: not a list this participant owns, or it can't sync any more")
	}
	head := c.head()
	epoch := c.epochOf(head)
	cur, ok := c.statements[string(head)]
	if epoch == nil || !ok || c.keys[string(epoch)] == nil {
		return fail("rotate: not a list this participant owns, or it can't sync any more")
	}
	if note := d.flightNote(cid); note != nil {
		return &PendingError{*note}
	}

	// Step 1: the state to carry over, tombstones included, and what the current inventory lists unseen (§8.3).
	entries := map[string]map[string]bool{}
	add := func(r string, id []byte) {
		if entries[r] == nil {
			entries[r] = map[string]bool{}
		}
		entries[r][string(id)] = true
	}
	carried := map[string][]*tandem.Write{}
	for rid, r := range c.records {
		es := r.Entries()
		if len(es) == 0 {
			continue
		}
		for _, e := range es {
			add(rid, e.ID)
		}
		carried[rid] = keptWrites(r)
	}
	for rid, ids := range d.inventories[string(epoch)] {
		for _, id := range ids {
			if !d.store.HasWrite(id) {
				add(rid, id)
			}
		}
	}
	inv := inventoryOf(entries)
	if err := d.crash("1:chosen"); err != nil {
		return err
	}

	// Step 2: the new key, floors afresh, the statement and inventory; journal, then the note.
	key := random(32)
	members := edit(cur.s.Members)
	for i := range members {
		floor, err := d.publishedSnapshot(ctx, members[i].UID)
		if err != nil {
			return err
		}
		members[i].Floor = floor
	}
	invBytes, err := inv.Bytes()
	if err != nil {
		return err
	}
	next := cur.s.clone()
	next.N++
	next.Nonce = nil
	next.KeyN++
	next.Prev = cur.s.Commit
	next.Commit = tandem.Commit(key)
	next.Snapshot = tandem.H(invBytes)
	next.Members = sortMembers(members)
	dot, err := d.nextDot(me)
	if err != nil {
		return err
	}
	w := &tandem.Write{Hub: d.pin.Hub, Certificate: me.Certificate, Authorization: me.Auth, Scope: cid, Record: rosterRecord,
		Field: rosterField, Op: tandem.OpAssign, Value: next.JSON(), Author: me.Participant, Dot: dot, Ref: head}
	if err := w.Sign(me.Keys.Signing); err != nil {
		return err
	}
	id := writeID(w)

	// The new channel's contents, sealed once: the inventory, the carried writes, and the whole
	// roster ending in the new statement, so the statement is on the hub only once everything it
	// commits to is (step 3).
	tag := tandem.CollectionTag(key, d.pin.Hub, id)
	var upload []tagged
	seal := func(pt []byte) error {
		env, err := tandem.Seal(key, d.pin.Hub, tandem.KindRecord, tag, pt)
		if err == nil {
			upload = append(upload, tagged{tag, env})
		}
		return err
	}
	frags, err := fragments(inv, id)
	if err != nil {
		return err
	}
	for _, f := range frags {
		if err := seal(f); err != nil {
			return err
		}
	}
	for _, rid := range sortedKeys(carried) {
		p, err := tandem.RecordPayload([]byte(rid), carried[rid])
		if err != nil {
			return err
		}
		if err := seal(p); err != nil {
			return err
		}
	}
	var line []*tandem.Write
	for _, sid := range chain(c, head) {
		line = append(line, c.statements[string(sid)].w)
	}
	p, err := tandem.RecordPayload(rosterRecord, append(line, w))
	if err != nil {
		return err
	}
	if err := seal(p); err != nil {
		return err
	}
	j := &rotationJournal{cid: cid, parent: head, oldEpoch: epoch, statement: w, id: id, key: key, inventory: invBytes, what: what, step: 2}
	jb, err := j.json()
	if err != nil {
		return err
	}
	err = d.store.Transaction(func() error {
		if err := d.store.SetJournal("rotate."+hexs(cid), jb); err != nil {
			return err
		}
		return d.store.SetJournal("rotate."+hexs(cid)+".upload", pack(upload))
	})
	if err != nil {
		return err
	}
	if err := d.crash("2:journal"); err != nil {
		return err
	}
	return d.runRotation(ctx, j)
}

func (d *Device) saveRotation(j *rotationJournal) error {
	b, err := j.json()
	if err != nil {
		return err
	}
	return d.store.SetJournal("rotate."+hexs(j.cid), b)
}

// runRotation carries a journalled change on from its recorded step (§11.6 steps 2–6).
func (d *Device) runRotation(ctx context.Context, start *rotationJournal) error {
	j := *start
	cid, name := j.cid, membershipName(j.cid)
	newTag := tandem.CollectionTag(j.key, d.pin.Hub, j.id)
	inv, err := tandem.ParseInventory(j.inventory)
	if err != nil {
		return err
	}
	for ; j.step <= 6; j.step++ {
		switch j.step {
		case 2:
			if !j.noteWritten {
				s, err := ParseStatement(j.statement.Value)
				if err != nil {
					return fail("journal: statement")
				}
				note := FlightNote{Parent: j.parent, Statement: j.id, Key: j.key, Members: s.Members, What: j.what, Device: d.deviceName()}
				if err := d.setHome(ctx, name, "flight", note.json()); err != nil {
					return err
				}
				j.noteWritten = true
				if err := d.saveRotation(&j); err != nil {
					return err
				}
				if err := d.crash("2:note"); err != nil {
					return err
				}
			}
		case 3:
			if _, err := d.sync(ctx); err != nil {
				return err
			}
			var upload []tagged
			if b, ok := d.store.Journal("rotate." + hexs(cid) + ".upload"); ok {
				if upload, err = unpack(b); err != nil {
					return err
				}
			}
			for _, e := range upload {
				on, err := d.store.MessagesWithHash(tandem.H(e.body))
				if err != nil {
					return err
				}
				if len(on) > 0 {
					continue
				}
				id, err := d.hubAppend(ctx, e.tag, e.body)
				if err != nil {
					return err
				}
				if err := d.store.NoteMessage(id, e.tag, tandem.H(e.body)); err != nil {
					return err
				}
			}
			if err := d.crash("3:roster"); err != nil {
				return err
			}
		case 4:
			if _, err := d.sync(ctx); err != nil {
				return err
			}
			if c := d.collections[string(cid)]; c == nil || !c.announced[string(j.id)] {
				// The last check before the change happens: the note is still ours and the parent still the head.
				note := d.flightNote(cid)
				if note == nil || !bytes.Equal(note.Statement, j.id) || c == nil || !bytes.Equal(c.head(), j.parent) {
					if err := d.destroyChannel(ctx, newTag); err != nil {
						return err
					}
					if err := d.clearOwnNote(ctx, cid, j.id); err != nil {
						return err
					}
					if err := d.dropRotation(cid); err != nil {
						return err
					}
					return ErrSuperseded
				}
				// The announcement, in the old epoch's channel: the moment the change happens.
				if err := d.publish(ctx, cid, j.oldEpoch, rosterRecord, []*tandem.Write{j.statement}); err != nil {
					return err
				}
				if _, err := d.acceptStatement(j.statement, cid, j.oldEpoch); err != nil {
					return err
				}
				d.changed(Event{Kind: EventMembers, List: cid})
				d.changed(Event{Kind: EventLists})
				if err := d.crash("4:announced"); err != nil {
					return err
				}
			}
			if err := d.settleHome(ctx, cid, j.id, j.key, inv); err != nil {
				return err
			}
			if err := d.crash("4:home"); err != nil {
				return err
			}
		case 5:
			me, c := d.me, d.collections[string(cid)]
			if me == nil || c == nil {
				break
			}
			s, ok := c.statements[string(j.id)]
			if !ok {
				break
			}
			for _, m := range s.s.Members {
				if bytes.Equal(m.UID, me.Participant) {
					continue
				}
				admission := admissionOf(m.UID, c, j.id)
				if admission == nil {
					continue
				}
				if err := d.sendGrant(ctx, cid, m.UID, admission, j.id, j.id, j.key, me); err != nil {
					if errors.Is(err, errNoDevice) {
						continue // no admitted device to send to: their devices ask later (§11.4)
					}
					return err
				}
				if err := d.crash("5:grant"); err != nil {
					return err
				}
			}
		case 6:
			if _, err := d.sync(ctx); err != nil {
				return err
			}
			if err := d.cleanOld(ctx, &j, inv); err != nil {
				return err
			}
			if err := d.crash("6:cleaned"); err != nil {
				return err
			}
		}
		next := j
		next.step++
		if err := d.saveRotation(&next); err != nil {
			return err
		}
	}
	return d.dropRotation(cid)
}

// settleHome is step 4's home update, and the housekeeping of §11.7: the key into keys, the
// note out if it names this change.
func (d *Device) settleHome(ctx context.Context, cid, statement, key []byte, inv tandem.Inventory) error {
	name := membershipName(cid)
	element := keyElement(statement, key)
	if !containsID(d.homeElements(name, "keys"), element) {
		if err := d.addHome(ctx, name, "keys", element); err != nil {
			return err
		}
	}
	if note := d.flightNote(cid); note != nil && bytes.Equal(note.Statement, statement) {
		if err := d.setHome(ctx, name, "flight", noFlight); err != nil {
			return err
		}
	}
	if c := d.collections[string(cid)]; c != nil {
		c.keys[string(statement)] = key
	}
	if _, ok := d.inventories[string(statement)]; inv != nil && !ok {
		if err := d.keepInventory(inv, statement); err != nil {
			return err
		}
	}
	return d.moveCollection(cid)
}

// clearOwnNote: a dropped change takes its note with it, if the note is still its own;
// otherwise it would block the list.
func (d *Device) clearOwnNote(ctx context.Context, cid, statement []byte) error {
	if note := d.flightNote(cid); note != nil && bytes.Equal(note.Statement, statement) {
		return d.setHome(ctx, membershipName(cid), "flight", noFlight)
	}
	return nil
}

func (d *Device) dropRotation(cid []byte) error {
	return d.store.Transaction(func() error {
		if err := d.store.ClearJournal("rotate." + hexs(cid)); err != nil {
			return err
		}
		return d.store.ClearJournal("rotate." + hexs(cid) + ".upload")
	})
}

func (d *Device) destroyChannel(ctx context.Context, tag string) error {
	ids, err := d.store.Messages(tag)
	if err != nil || len(ids) == 0 {
		return err
	}
	return d.hubDestroy(ctx, ids)
}

// writesIn is every write the messages of a channel carry, as this device noted them.
func (d *Device) writesIn(tag string) (map[string]bool, error) {
	out := map[string]bool{}
	ms, err := d.store.Messages(tag)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		cs, err := d.store.Contents(m)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			out[string(c.Write)] = true
		}
	}
	return out, nil
}

// cleanOld is step 6: destroy the old epoch's data messages, each once every write in it is in
// the new channel, replaced there, or not carried over; a listed write missing from the new
// channel is copied across first. The roster stays.
func (d *Device) cleanOld(ctx context.Context, j *rotationJournal, inv tandem.Inventory) error {
	c := d.collections[string(j.cid)]
	if c == nil || c.keys[string(j.oldEpoch)] == nil {
		return nil
	}
	oldTag := tandem.CollectionTag(c.keys[string(j.oldEpoch)], d.pin.Hub, j.oldEpoch)
	newTag := tandem.CollectionTag(j.key, d.pin.Hub, j.id)
	if err := d.healFragments(ctx, newTag, j.id, j.key); err != nil { // the inventory first, then the old data
		return err
	}
	listed := map[string]bool{}
	for _, ids := range inv {
		for _, id := range ids {
			listed[string(id)] = true
		}
	}
	inNew, err := d.writesIn(newTag)
	if err != nil {
		return err
	}
	missing := map[string][]*tandem.Write{}
	var doomed []string
	olds, err := d.store.Messages(oldTag)
	if err != nil {
		return err
	}
	for _, m := range olds {
		contents, err := d.store.Contents(m)
		if err != nil {
			return err
		}
		if len(contents) == 0 {
			continue
		}
		roster := false
		for _, ct := range contents {
			roster = roster || bytes.Equal(ct.Record, rosterRecord)
		}
		if roster {
			continue
		}
		for _, ct := range contents {
			if listed[string(ct.Write)] && !inNew[string(ct.Write)] {
				if e := keptEntry(c.records[string(ct.Record)], ct.Write); e != nil {
					missing[string(ct.Record)] = append(missing[string(ct.Record)], e.W)
					inNew[string(ct.Write)] = true
				}
			}
		}
		doomed = append(doomed, m)
	}
	for _, record := range sortedKeys(missing) {
		p, err := tandem.RecordPayload([]byte(record), missing[record])
		if err != nil {
			return err
		}
		env, err := tandem.Seal(j.key, d.pin.Hub, tandem.KindRecord, newTag, p)
		if err != nil {
			return err
		}
		id, err := d.hubAppend(ctx, newTag, env)
		if err != nil {
			return err
		}
		var ids [][]byte
		for _, w := range missing[record] {
			ids = append(ids, writeID(w))
		}
		if err := d.store.NoteMessage(id, newTag, tandem.H(env)); err != nil {
			return err
		}
		if err := d.store.NoteContents(id, []byte(record), ids); err != nil {
			return err
		}
	}
	if len(doomed) > 0 {
		return d.hubDestroy(ctx, doomed)
	}
	return nil
}

// Resume carries on journalled changes after a restart (§11.6): it resumes a change that is
// still this device's, finishes the home update of one that was announced, and drops one
// another device discarded or overtook, returning ErrSuperseded.
func (d *Device) Resume(ctx context.Context) error {
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	var failure error
	steps, err := d.store.JournalSteps()
	if err != nil {
		return err
	}
	for _, step := range steps {
		if !strings.HasPrefix(step, "rotate.") || strings.HasSuffix(step, ".upload") {
			continue
		}
		data, ok := d.store.Journal(step)
		if !ok {
			continue
		}
		j, err := parseRotationJournal(data, d.pin.Hub)
		if err != nil {
			continue
		}
		c, note := d.collections[string(j.cid)], d.flightNote(j.cid)
		announced := c != nil && c.announced[string(j.id)]
		headIsParent := c != nil && bytes.Equal(c.head(), j.parent)
		switch {
		case announced:
			j.step = max(j.step, 4)
		case note != nil && bytes.Equal(note.Statement, j.id) && headIsParent:
			// Its own note is in home, whatever the journal says: an append whose answer was lost may have landed.
			if !j.noteWritten {
				j.noteWritten = true
				j.step = max(j.step, 3)
			}
		case !j.noteWritten:
			// Nothing but the journal left the device; usable only if nothing moved meanwhile.
			if c == nil || note != nil || !headIsParent {
				if err := d.clearOwnNote(ctx, j.cid, j.id); err != nil {
					return err
				}
				if err := d.dropRotation(j.cid); err != nil {
					return err
				}
				failure = cmpOr(failure, ErrSuperseded)
				continue
			}
		default:
			// Discarded or overtaken: a draft may always be dropped.
			if err := d.destroyChannel(ctx, tandem.CollectionTag(j.key, d.pin.Hub, j.id)); err != nil {
				return err
			}
			if err := d.clearOwnNote(ctx, j.cid, j.id); err != nil {
				return err
			}
			if err := d.dropRotation(j.cid); err != nil {
				return err
			}
			failure = cmpOr(failure, ErrSuperseded)
			continue
		}
		if err := d.saveRotation(j); err != nil {
			return err
		}
		if err := d.runRotation(ctx, j); err != nil {
			failure = cmpOr(failure, err)
		}
	}
	return failure
}

func cmpOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// Another device's change (§11.7)

// Finish finishes the change another device of this owner started, the one the user was
// shown, if its statement and inventory are on the hub.
func (d *Device) Finish(ctx context.Context, list []byte, seen *FlightNote) error {
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	note := d.flightNote(list)
	if note == nil || !bytes.Equal(note.Statement, seen.Statement) {
		return ErrNoteChanged
	}
	c := d.collections[string(list)]
	if c == nil {
		return ErrCantFinish
	}
	st, ok := c.statements[string(note.Statement)]
	old := c.epochOf(note.Parent)
	inv, hasInv := d.inventories[string(note.Statement)]
	if !ok || old == nil || !hasInv {
		return ErrCantFinish
	}
	invBytes, err := inv.Bytes()
	if err != nil {
		return err
	}
	j := &rotationJournal{cid: list, parent: note.Parent, oldEpoch: old, statement: st.w, id: note.Statement, key: note.Key,
		inventory: invBytes, what: note.What, step: 3, noteWritten: true}
	// What the inventory lists and the new channel lacks, from this device's replica (§11.7).
	tag := tandem.CollectionTag(note.Key, d.pin.Hub, note.Statement)
	inNew, err := d.writesIn(tag)
	if err != nil {
		return err
	}
	var upload []tagged
	for _, rid := range sortedKeys(inv) {
		var writes []*tandem.Write
		for _, id := range inv[rid] {
			if inNew[string(id)] {
				continue
			}
			if e := keptEntry(c.records[rid], id); e != nil {
				writes = append(writes, e.W)
			}
		}
		if len(writes) == 0 {
			continue
		}
		p, err := tandem.RecordPayload([]byte(rid), writes)
		if err != nil {
			return err
		}
		env, err := tandem.Seal(note.Key, d.pin.Hub, tandem.KindRecord, tag, p)
		if err != nil {
			return err
		}
		upload = append(upload, tagged{tag, env})
	}
	jb, err := j.json()
	if err != nil {
		return err
	}
	err = d.store.Transaction(func() error {
		if err := d.store.SetJournal("rotate."+hexs(list), jb); err != nil {
			return err
		}
		return d.store.SetJournal("rotate."+hexs(list)+".upload", pack(upload))
	})
	if err != nil {
		return err
	}
	return d.runRotation(ctx, j)
}

// Discard discards the change another device of this owner started, the one the user was
// shown: its note and key leave home and what it uploaded is destroyed.
func (d *Device) Discard(ctx context.Context, list []byte, seen *FlightNote) error {
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	note := d.flightNote(list)
	if note == nil || !bytes.Equal(note.Statement, seen.Statement) {
		return ErrNoteChanged
	}
	if err := d.destroyChannel(ctx, tandem.CollectionTag(note.Key, d.pin.Hub, note.Statement)); err != nil {
		return err
	}
	return d.setHome(ctx, membershipName(list), "flight", noFlight)
}

// settleAnnouncedNotes settles notes left over from a crash between an announcement and its
// home update, without asking (§11.7).
func (d *Device) settleAnnouncedNotes(ctx context.Context) error {
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		note := d.flightNote(c.id)
		if note == nil || !c.announced[string(note.Statement)] {
			continue
		}
		if _, ok := d.store.Journal("rotate." + hexs(c.id)); ok {
			continue
		}
		if err := d.settleHome(ctx, c.id, note.Statement, note.Key, d.inventories[string(note.Statement)]); err != nil {
			return err
		}
	}
	return nil
}
