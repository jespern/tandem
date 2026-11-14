package engine

import (
	"bytes"
	"context"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

type stmt struct {
	w *tandem.Write
	s Statement
}

// collection is one list as a device holds it (§7–9): its roster chain, the epoch keys it was
// given, and its records.
type collection struct {
	id         []byte // 16
	statements map[string]stmt
	genesis    []byte
	// Epoch starts seen in their parent epoch's channel (§11.6 step 4). Until then a start is a draft (§9.4).
	announced map[string]bool
	keys      map[string][]byte // epoch-start statement ID → epoch key
	// Keys from grants not yet confirmed by the owner-signed statement that starts their epoch: used only to read.
	readKeys map[string]map[string][]byte
	records  map[string]*tandem.Record
}

func newCollection(id []byte) *collection {
	return &collection{id: bytes.Clone(id), statements: map[string]stmt{}, announced: map[string]bool{},
		keys: map[string][]byte{}, readKeys: map[string]map[string][]byte{}, records: map[string]*tandem.Record{}}
}

// offer takes a key from a grant, unauthenticated until checked (§11.4): it never displaces a
// key, and becomes the epoch's key only once the statement starting the epoch, signed by the
// owner, commits to it.
func (c *collection) offer(key, epoch []byte) {
	if c.keys[string(epoch)] != nil {
		return
	}
	if c.readKeys[string(epoch)] == nil {
		c.readKeys[string(epoch)] = map[string][]byte{}
	}
	c.readKeys[string(epoch)][string(key)] = key
	c.confirmKey(epoch)
}

func (c *collection) confirmKey(epoch []byte) {
	s, ok := c.statements[string(epoch)]
	cands := c.readKeys[string(epoch)]
	if c.keys[string(epoch)] != nil || !ok || cands == nil {
		return
	}
	for _, k := range sortedKeys(cands) {
		if bytes.Equal(tandem.Commit(cands[k]), s.s.Commit) {
			c.keys[string(epoch)] = cands[k]
		}
	}
	delete(c.readKeys, string(epoch))
}

func (c *collection) owner() []byte {
	if c.genesis == nil {
		return nil
	}
	s, ok := c.statements[string(c.genesis)]
	if !ok || s.s.Owner() == nil {
		return nil
	}
	return s.s.Owner().UID
}

// startsEpoch: whether id starts an epoch: its commitment differs from its parent's.
func (c *collection) startsEpoch(id []byte) bool {
	s, ok := c.statements[string(id)]
	if !ok {
		return false
	}
	p, ok := c.statements[string(s.w.Ref)]
	if !ok {
		return bytes.Equal(id, c.genesis)
	}
	return !bytes.Equal(p.s.Commit, s.s.Commit)
}

// counts: a statement counts toward the roster unless it is an epoch start not yet announced.
func (c *collection) counts(id []byte) bool {
	return bytes.Equal(id, c.genesis) || !c.startsEpoch(id) || c.announced[string(id)]
}

func (c *collection) successors(id []byte) [][]byte {
	var out [][]byte
	for _, k := range sortedKeys(c.statements) {
		if k != string(id) && bytes.Equal(c.statements[k].w.Ref, id) && c.counts([]byte(k)) {
			out = append(out, []byte(k))
		}
	}
	return out
}

// head is the roster's head: the one line of counted statements from the genesis (§9.4).
func (c *collection) head() []byte {
	if c.genesis == nil {
		return nil
	}
	current := c.genesis
	for {
		next := c.successors(current)
		if len(next) != 1 {
			return current
		}
		current = next[0]
	}
}

func (c *collection) headStatement() (Statement, bool) {
	h := c.head()
	if h == nil {
		return Statement{}, false
	}
	s, ok := c.statements[string(h)]
	return s.s, ok
}

// dead: two counted successors of one statement (§9.4).
func (c *collection) dead() bool {
	for k := range c.statements {
		if c.counts([]byte(k)) && len(c.successors([]byte(k))) > 1 {
			return true
		}
	}
	return false
}

// epochOf is the epoch a statement belongs to: the first statement back along the chain with
// the same commitment (§8.3).
func (c *collection) epochOf(id []byte) []byte {
	if id == nil {
		return nil
	}
	current := id
	for {
		s, ok := c.statements[string(current)]
		if !ok {
			return nil
		}
		parent, ok := c.statements[string(s.w.Ref)]
		if !ok || !bytes.Equal(parent.s.Commit, s.s.Commit) {
			return current
		}
		current = s.w.Ref
	}
}

// successorEpochs: the epochs epoch was replaced by: announced starts whose parent is in it.
func (c *collection) successorEpochs(epoch []byte) [][]byte {
	var out [][]byte
	for _, k := range sortedKeys(c.statements) {
		id := []byte(k)
		if c.startsEpoch(id) && c.announced[k] && bytes.Equal(c.epochOf(c.statements[k].w.Ref), epoch) {
			out = append(out, id)
		}
	}
	return out
}

// precedes: whether ancestor is on the chain before id.
func (c *collection) precedes(ancestor, id []byte) bool {
	s, ok := c.statements[string(id)]
	if !ok {
		return false
	}
	current := s.w.Ref
	for {
		s, ok := c.statements[string(current)]
		if !ok {
			return false
		}
		if bytes.Equal(current, ancestor) {
			return true
		}
		current = s.w.Ref
	}
}

// Creating a list (§7.1)

// CreateList creates a collection owned by this participant, private until shared (§7.4),
// and returns its ID.
func (d *Device) CreateList(ctx context.Context, name string) ([]byte, error) {
	defer d.begin()()
	return d.createList(ctx, name)
}

func (d *Device) createList(ctx context.Context, name string) ([]byte, error) {
	me := d.me
	if me == nil {
		return nil, fail("device: not on this hub yet")
	}
	key := random(32)
	st := Statement{Commit: tandem.Commit(key), Nonce: random(16), Members: []Member{{UID: me.Participant, Role: Owner, Floor: me.Auth}}}
	dot, err := d.nextDot(me)
	if err != nil {
		return nil, err
	}
	genesis := &tandem.Write{Hub: d.pin.Hub, Certificate: me.Certificate, Authorization: me.Auth, Scope: zero16, Record: rosterRecord,
		Field: rosterField, Op: tandem.OpAssign, Value: st.JSON(), Author: me.Participant, Dot: dot, Ref: zero32}
	if err := genesis.Sign(me.Keys.Signing); err != nil {
		return nil, err
	}
	gid := writeID(genesis)
	cid := collectionID(gid)
	c := newCollection(cid)
	c.statements[string(gid)] = stmt{genesis, st}
	c.genesis = gid
	c.keys[string(gid)] = key
	d.collections[string(cid)] = c
	p, err := onePayload(rosterRecord, genesis)
	if err != nil {
		return nil, err
	}
	if _, err := d.store.PutWrite(gid, cid, rosterRecord, gid, p); err != nil {
		return nil, err
	}
	// The key goes into home first: that's how this participant's other devices find the list (§6.2).
	m := membershipName(cid)
	if err := d.setHome(ctx, m, "owner", me.Participant); err != nil {
		return nil, err
	}
	if err := d.setHome(ctx, m, "role", []byte("owner")); err != nil {
		return nil, err
	}
	if err := d.addHome(ctx, m, "keys", keyElement(gid, key)); err != nil {
		return nil, err
	}
	if err := d.publish(ctx, cid, gid, rosterRecord, []*tandem.Write{genesis}); err != nil {
		return nil, err
	}
	return cid, d.writeRecord(ctx, cid, metaRecord, "name", tandem.OpAssign, []byte(name), nil)
}

// keyElement is {"epoch":"…","k":"…"}, in that order (§6.2).
func keyElement(epoch, key []byte) []byte {
	return tandem.NewJSONObject().B64u("epoch", epoch).B64u("k", key).Bytes()
}

func parseKeyElement(b []byte) (epoch, key []byte, ok bool) {
	j, err := tandem.ParseJSON(b)
	if err != nil {
		return nil, nil, false
	}
	e, err1 := j.B64u("epoch", 32)
	k, err2 := j.B64u("k", 32)
	return e, k, err1 == nil && err2 == nil
}

func membershipName(cid []byte) string { return "membership:" + tandem.B64u(cid) }

// Records

// NewRecordID is a new random record ID (§7.3).
func NewRecordID() []byte { return random(16) }

// SetField sets a field of a record in a list, as this device, in the list's current epoch.
func (d *Device) SetField(ctx context.Context, list, record []byte, field string, value []byte) error {
	defer d.begin()()
	return d.writeRecord(ctx, list, record, field, tandem.OpAssign, value, nil)
}

// DeleteRecord deletes a record (§9.5). Named records can't be deleted (§7.3).
func (d *Device) DeleteRecord(ctx context.Context, list, record []byte) error {
	defer d.begin()()
	if bytes.Equal(record, metaRecord) || bytes.Equal(record, rosterRecord) {
		return fail("device: a named record is never deleted")
	}
	return d.writeRecord(ctx, list, record, "", tandem.OpDelete, nil, nil)
}

// Value is a field's displayed value, unless the record is deleted.
func (d *Device) Value(list, record []byte, field string) ([]byte, bool) {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return nil, false
	}
	r := c.records[string(record)]
	if r == nil || r.Deleted() {
		return nil, false
	}
	return r.Value(field)
}

// Records is the list's records, without the named ones and without deleted ones.
func (d *Device) Records(list []byte) [][]byte {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return nil
	}
	var out [][]byte
	for _, k := range sortedKeys(c.records) {
		if k != string(metaRecord) && !c.records[k].Deleted() {
			out = append(out, []byte(k))
		}
	}
	return out
}

// ListInfo is one list this participant follows.
type ListInfo struct {
	ID   []byte
	Name string // "" until its _meta record is here
}

// Lists is the lists this participant is a member of, with their names where known: those its home records (§6.2).
func (d *Device) Lists() []ListInfo {
	defer d.read()()
	var out []ListInfo
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		if !d.following(c.id) {
			continue
		}
		name, _ := value(c.records[string(metaRecord)], "name")
		out = append(out, ListInfo{ID: c.id, Name: string(name)})
	}
	return out
}

// Role is this participant's role in a list now, or "" if the roster doesn't list it.
func (d *Device) Role(list []byte) Role {
	defer d.read()()
	return d.role(list)
}

func (d *Device) role(list []byte) Role {
	c := d.collections[string(list)]
	if d.me == nil || c == nil {
		return ""
	}
	s, ok := c.headStatement()
	if !ok {
		return ""
	}
	if m := s.member(d.me.Participant); m != nil {
		return m.Role
	}
	return ""
}

// IsDead reports whether a list can't sync any more: two successors of one roster statement (§9.4).
func (d *Device) IsDead(list []byte) bool {
	defer d.read()()
	c := d.collections[string(list)]
	return c != nil && c.dead()
}

func (d *Device) writeRecord(ctx context.Context, cid, record []byte, field string, op tandem.Op, val, element []byte) error {
	me, c := d.me, d.collections[string(cid)]
	if me == nil || c == nil || c.dead() {
		return fail("device: no such list, or it can't sync")
	}
	head := c.head()
	epoch := c.epochOf(head)
	cur, ok := c.statements[string(head)]
	if head == nil || epoch == nil || c.keys[string(epoch)] == nil || !ok || cur.s.Deleted {
		return fail("device: no key for the list's current epoch yet, or it was deleted")
	}
	member := cur.s.member(me.Participant)
	if member == nil || member.Role == Reader || (bytes.Equal(record, metaRecord) && member.Role != Owner) {
		return fail("device: this participant may not write here")
	}
	if len(d.exposure(cid)) > 0 {
		return fail("device: waiting for the list's owner to change its key")
	}
	var writes []*tandem.Write
	if bytes.Equal(record, metaRecord) {
		if _, ok := value(c.records[string(record)], "_name"); !ok {
			w, err := d.signRecord(c, me, record, "_name", tandem.OpAssign, []byte("_meta"), nil, head, epoch, writes)
			if err != nil {
				return err
			}
			writes = append(writes, w)
		}
	}
	w, err := d.signRecord(c, me, record, field, op, val, element, head, epoch, writes)
	if err != nil {
		return err
	}
	writes = append(writes, w)
	var entries []tandem.Entry
	for _, w := range writes {
		e, err := entryOf(w, epoch, cur.s.KeyN)
		if err != nil {
			return err
		}
		entries = append(entries, e)
	}
	r, err := merged(c.records[string(record)], d.pin.Hub, cid, record, entries...)
	if err != nil {
		return err
	}
	err = d.store.Transaction(func() error {
		for _, e := range entries {
			p, err := onePayload(record, e.W)
			if err != nil {
				return err
			}
			if _, err := d.store.PutWrite(e.ID, cid, record, epoch, p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.records[string(record)] = r
	d.changedItems(cid, record)
	if bytes.Equal(record, metaRecord) {
		d.changed(Event{Kind: EventLists})
	}
	// A record is published with its whole current state, so this message dominates the ones
	// before it (§10.7). Offline, the write stays saved: the next sync's healing publishes it (§12.2).
	if err := d.publish(ctx, cid, epoch, record, keptWrites(r)); err != nil {
		d.changed(Event{Kind: EventSyncFailed, Message: err.Error()})
	}
	return nil
}

func (d *Device) signRecord(c *collection, me *Me, record []byte, field string, op tandem.Op, val, element, head, epoch []byte,
	earlier []*tandem.Write) (*tandem.Write, error) {
	n := c.statements[string(head)].s.KeyN
	r := c.records[string(record)]
	if len(earlier) > 0 {
		var es []tandem.Entry
		for _, w := range earlier {
			e, err := entryOf(w, epoch, n)
			if err != nil {
				return nil, err
			}
			es = append(es, e)
		}
		var err error
		if r, err = merged(r, d.pin.Hub, c.id, record, es...); err != nil {
			return nil, err
		}
	}
	if r == nil {
		r = emptyRecord(d.pin.Hub, c.id, record)
	}
	return d.signWrite(me, r, c.id, record, epoch, int64(n), head, field, op, val, element)
}

// publish puts one record's writes in a list epoch's channel.
func (d *Device) publish(ctx context.Context, cid, epoch, record []byte, writes []*tandem.Write) error {
	c := d.collections[string(cid)]
	if c == nil || c.keys[string(epoch)] == nil {
		return nil
	}
	key := c.keys[string(epoch)]
	return d.publishRecord(ctx, key, tandem.CollectionTag(key, d.pin.Hub, epoch), record, writes)
}

// exposure is the members whose devices a known revocation cut but the current epoch's floors
// don't reflect (§6.4): until the owner changes the key, nothing new goes out under the exposed one.
func (d *Device) exposure(cid []byte) [][]byte {
	c := d.collections[string(cid)]
	if c == nil {
		return nil
	}
	s, ok := c.headStatement()
	if !ok {
		return nil
	}
	return exposureOf(s, d.registry)
}

func exposureOf(s Statement, r *Registry) [][]byte {
	var out [][]byte
	for _, m := range s.Members {
		floor := r.Snapshot(m.Floor)
		if floor == nil {
			continue
		}
		for _, rv := range r.revs {
			if t := r.Certificate(rv.target); t != nil && bytes.Equal(t.Participant, m.UID) && r.RevocationValidity(rv.id).IsValid() &&
				!containsID(floor.Revocations, rv.id) {
				out = append(out, m.UID)
				break
			}
		}
	}
	return out
}

// ListsNeedingRekey is the lists this participant owns whose key must change because a
// member's device was revoked (§6.4 step 3).
func (d *Device) ListsNeedingRekey() [][]byte {
	defer d.read()()
	if d.me == nil {
		return nil
	}
	var out [][]byte
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		if bytes.Equal(c.owner(), d.me.Participant) && !c.dead() && len(d.exposure(c.id)) > 0 {
			out = append(out, c.id)
		}
	}
	return out
}

// Learning of lists from home (§6.2)

// membershipList reads a membership: record's list ID.
func membershipList(r *tandem.Record) []byte {
	name, ok := value(r, "_name")
	if !ok || !strings.HasPrefix(string(name), "membership:") {
		return nil
	}
	cid, err := tandem.ParseB64u(string(name[len("membership:"):]))
	if err != nil || len(cid) != 16 {
		return nil
	}
	return cid
}

// refreshMemberships follows every collection a membership: record in home has keys for.
func (d *Device) refreshMemberships() {
	for _, k := range sortedKeys(d.home) {
		r := d.home[k]
		cid := membershipList(r)
		if cid == nil {
			continue
		}
		c := d.collection(cid)
		// Keys from home are candidates too: a revoked keychain can still write to home until it
		// is rekeyed, so a key counts only once the owner-signed statement of its epoch commits to
		// it, and never displaces one.
		for _, el := range r.Elements("keys") {
			if e, key, ok := parseKeyElement(el); ok {
				c.offer(key, e)
			}
		}
		// The key of a change still in flight, so this device can finish or discard it (§11.7).
		if note := parseFlightNote(r); note != nil {
			c.offer(note.Key, note.Statement)
		}
	}
}

// loadCollections is the collections as the store holds them: memberships from home,
// statements and records from their writes.
func loadCollections(store *Store, hub []byte, home map[string]*tandem.Record, inventories map[string]tandem.Inventory) (map[string]*collection, error) {
	out := map[string]*collection{}
	for _, k := range sortedKeys(home) {
		cid := membershipList(home[k])
		if cid == nil {
			continue
		}
		c, err := loadRoster(store, hub, cid, home[k])
		if err != nil {
			return nil, err
		}
		records, err := store.Records(cid)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if bytes.Equal(record, rosterRecord) {
				continue
			}
			if c.records[string(record)], err = loadData(store, hub, c, record, inventories); err != nil {
				return nil, err
			}
		}
		out[string(cid)] = c
	}
	return out, nil
}

// loadRoster is a collection's keys (from its membership: record in home), roster and
// announcements, without its records.
func loadRoster(store *Store, hub, cid []byte, membership *tandem.Record) (*collection, error) {
	c := newCollection(cid)
	rows, err := store.Writes(cid, rosterRecord)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		w := storedWrite(row.Payload, hub, cid)
		if w == nil {
			continue
		}
		s, err := ParseStatement(w.Value)
		if err != nil {
			continue
		}
		if bytes.Equal(w.Ref, zero32) {
			w.Scope = zero16
			c.genesis = writeID(w)
		}
		c.statements[string(writeID(w))] = stmt{w, s}
	}
	// Keys after statements, so a key is confirmed against its epoch's commitment as it is read (offer).
	for _, el := range membership.Elements("keys") {
		if e, k, ok := parseKeyElement(el); ok {
			c.offer(k, e)
		}
	}
	if note := parseFlightNote(membership); note != nil {
		c.offer(note.Key, note.Statement)
	}
	prefix := "ann." + hexs(cid) + "."
	anns, err := store.Values(prefix)
	if err != nil {
		return nil, err
	}
	for _, v := range anns {
		if id, ok := unhexs(strings.TrimPrefix(v.Name, prefix)); ok {
			c.announced[string(id)] = true
		}
	}
	return c, nil
}

// loadData is a record of a collection rebuilt from the stored writes the current epoch keeps
// (§11.6, "Moving over"): those written in it and those its inventory lists.
func loadData(store *Store, hub []byte, c *collection, record []byte, inventories map[string]tandem.Inventory) (*tandem.Record, error) {
	current := c.epochOf(c.head())
	rows, err := store.Writes(c.id, record)
	if err != nil {
		return nil, err
	}
	var es []tandem.Entry
	for _, row := range rows {
		w := storedWrite(row.Payload, hub, c.id)
		if w == nil {
			continue
		}
		s, ok := c.statements[string(w.Ref)]
		if !ok {
			continue
		}
		e, err := entryOf(w, row.Epoch, s.s.KeyN)
		if err != nil {
			return nil, err
		}
		if current != nil && e.Epoch != string(current) && !containsID(inventories[string(current)][string(record)], e.ID) {
			continue
		}
		es = append(es, e)
	}
	return merged(nil, hub, c.id, record, es...)
}

// moveCollection changes to the current epoch's state once its inventory is here; until then
// it keeps the old one.
func (d *Device) moveCollection(cid []byte) error {
	c := d.collections[string(cid)]
	if c == nil {
		return nil
	}
	current := c.epochOf(c.head())
	if current == nil {
		return nil
	}
	if _, ok := d.inventories[string(current)]; !bytes.Equal(current, c.genesis) && !ok {
		return nil
	}
	records, err := d.store.Records(cid)
	if err != nil {
		return err
	}
	var changed [][]byte
	for _, record := range records {
		if bytes.Equal(record, rosterRecord) {
			continue
		}
		r, err := loadData(d.store, d.pin.Hub, c, record, d.inventories)
		if err != nil {
			return err
		}
		c.records[string(record)] = r
	}
	for _, k := range sortedKeys(c.records) {
		changed = append(changed, []byte(k))
	}
	d.changedItems(cid, changed...)
	return nil
}

// Collection channels

func (d *Device) collectionChannel(tag string) (cid, epoch, key []byte, ok bool) {
	for _, ck := range sortedKeys(d.collections) {
		c := d.collections[ck]
		for _, e := range sortedKeys(c.keys) {
			if tandem.CollectionTag(c.keys[e], d.pin.Hub, []byte(e)) == tag {
				return c.id, []byte(e), c.keys[e], true
			}
		}
		for _, e := range sortedKeys(c.readKeys) {
			for _, k := range c.readKeys[e] {
				if tandem.CollectionTag(k, d.pin.Hub, []byte(e)) == tag {
					return c.id, []byte(e), k, true
				}
			}
		}
	}
	return nil, nil, nil, false
}

func (d *Device) applyCollection(m tandem.Message, cid, epoch, key []byte) (outcome, error) {
	kind, pt, err := tandem.Open(key, d.pin.Hub, m.Channel, m.Body)
	if err != nil {
		return ignored, nil
	}
	if kind == tandem.KindRequest {
		j, err := tandem.ParseJSON(pt)
		if err != nil {
			return ignored, nil
		}
		switch typ, _ := j.Text("type"); typ {
		case "leave":
			return d.receiveLeave(j, cid)
		case "key-request":
			return d.receiveKeyRequest(j)
		}
		return ignored, nil
	}
	state, frag, err := tandem.ParseKind1(pt, d.pin.Hub, cid)
	if err != nil {
		return ignored, nil
	}
	if frag != nil {
		c := d.collections[string(cid)]
		s, ok := c.statements[string(epoch)]
		if !ok || s.s.Snapshot == nil {
			return waiting, nil
		}
		// Which part each message carries, for healing (§12.2).
		if err := d.store.SetValue("fragmsg."+m.ID, append(bytes.Clone(epoch), tandem.H(frag.Bytes)...)); err != nil {
			return ignored, err
		}
		done, err := d.receiveFragment(frag, epoch, s.s.Snapshot)
		if err != nil || !done {
			return ignored, err
		}
		return applied, d.moveCollection(cid)
	}
	anyApplied, moved := false, false
	var appliedIDs [][]byte
	for _, w := range state.Writes {
		if bytes.Equal(state.Record, rosterRecord) {
			if bytes.Equal(w.Ref, zero32) {
				w.Scope = zero16 // a genesis is signed with zero scope (§7.1)
			}
			o, err := d.acceptStatement(w, cid, epoch)
			if err != nil {
				return ignored, err
			}
			switch o {
			case applied:
				anyApplied, moved = true, true
				appliedIDs = append(appliedIDs, writeID(w))
			case waiting:
				return waiting, nil
			case ignored:
				if c := d.collections[string(cid)]; c != nil {
					if _, ok := c.statements[string(writeID(w))]; ok {
						appliedIDs = append(appliedIDs, writeID(w))
					}
				}
			}
			continue
		}
		switch v := d.dataWriteValidity(w, cid, epoch); v.State {
		case Valid:
			c := d.collections[string(cid)]
			s, ok := c.statements[string(w.Ref)]
			source := c.epochOf(w.Ref)
			if !ok || source == nil {
				continue
			}
			e, err := entryOf(w, source, s.s.KeyN)
			if err != nil {
				return ignored, err
			}
			p, err := onePayload(state.Record, w)
			if err != nil {
				return ignored, err
			}
			if _, err := d.store.PutWrite(e.ID, cid, state.Record, source, p); err != nil {
				return ignored, err
			}
			r, err := merged(c.records[string(state.Record)], d.pin.Hub, cid, state.Record, e)
			if err != nil {
				return ignored, err
			}
			c.records[string(state.Record)] = r
			appliedIDs = append(appliedIDs, e.ID)
			anyApplied = true
			d.changedItems(cid, state.Record)
		case Pending:
			return waiting, nil
		}
	}
	if err := d.store.NoteContents(m.ID, state.Record, appliedIDs); err != nil {
		return ignored, err
	}
	if moved {
		if err := d.moveCollection(cid); err != nil {
			return ignored, err
		}
		d.changed(Event{Kind: EventLists})
		d.changed(Event{Kind: EventMembers, List: cid})
	}
	if anyApplied {
		return applied, nil
	}
	return ignored, nil
}

type floorCheck struct{ snapshot, floor []byte }

// acceptStatement takes one roster statement (§8.2): the genesis, a same-epoch successor, or an
// epoch start by the owner. An epoch start seen in its parent epoch's channel is announced;
// seen only in its own, it is a draft (§9.4, §11.6).
func (d *Device) acceptStatement(w *tandem.Write, cid, channel []byte) (outcome, error) {
	if !bytes.Equal(w.Record, rosterRecord) || w.Field != rosterField || w.Op != tandem.OpAssign || w.Element != nil ||
		len(w.Context) != 0 || len(w.Base) != 0 {
		return ignored, nil
	}
	s, err := ParseStatement(w.Value)
	if err != nil {
		return ignored, nil
	}
	id := writeID(w)
	c := d.collections[string(cid)]
	if id == nil || c == nil {
		return ignored, nil
	}
	if known, ok := c.statements[string(id)]; ok {
		// Already held: an epoch start now seen in its parent's channel is announced.
		if c.startsEpoch(id) && !c.announced[string(id)] && bytes.Equal(c.epochOf(known.w.Ref), channel) {
			c.announced[string(id)] = true
			return applied, d.store.SetValue("ann."+hexs(cid)+"."+hexs(id), []byte{1})
		}
		return ignored, nil
	}
	var floors []floorCheck // (snapshot, floor) pairs the signer must extend
	if bytes.Equal(w.Ref, zero32) {
		if !s.isGenesis() || !bytes.Equal(collectionID(id), cid) || s.Owner() == nil || !bytes.Equal(s.Owner().UID, w.Author) {
			return ignored, nil
		}
	} else {
		parent, ok := c.statements[string(w.Ref)]
		if !ok {
			return waiting, nil
		}
		p := parent.s
		owner := c.owner()
		if s.N != p.N+1 || p.Deleted || !bytes.Equal(w.Author, owner) || s.Owner() == nil || !bytes.Equal(s.Owner().UID, owner) || s.Nonce != nil {
			return ignored, nil
		}
		if bytes.Equal(s.Commit, p.Commit) {
			// Same epoch (§8.2): key, snapshot and every kept floor unchanged; nobody removed or
			// lowered; signed under a snapshot that extends the epoch's owner floor, so a revoked
			// owner keychain can't extend it.
			if s.KeyN != p.KeyN || !bytes.Equal(s.Prev, p.Prev) || !bytes.Equal(s.Snapshot, p.Snapshot) {
				return ignored, nil
			}
			if o := p.Owner(); o != nil {
				floors = append(floors, floorCheck{w.Authorization, o.Floor})
			}
			for _, m := range p.Members {
				now := s.member(m.UID)
				if now == nil || !bytes.Equal(now.Floor, m.Floor) || rank(now.Role) < rank(m.Role) {
					return ignored, nil
				}
			}
		} else {
			// A new epoch: next counter, a fresh commitment, an inventory, nobody added.
			if s.KeyN != p.KeyN+1 || !bytes.Equal(s.Prev, p.Commit) || s.Snapshot == nil {
				return ignored, nil
			}
			for _, other := range c.statements {
				if bytes.Equal(other.s.Commit, s.Commit) {
					return ignored, nil
				}
			}
			for _, m := range s.Members {
				if p.member(m.UID) == nil {
					return ignored, nil
				}
			}
			if o := p.Owner(); o != nil {
				floors = append(floors, floorCheck{w.Authorization, o.Floor})
			}
			if o := s.Owner(); o != nil {
				floors = append(floors, floorCheck{w.Authorization, o.Floor})
			}
		}
	}
	checks := []Validity{d.registry.Authorizes(w.Authorization, w.Certificate)}
	for _, f := range floors {
		checks = append(checks, d.registry.Extends(f.snapshot, f.floor))
	}
	switch allValid(checks...).State {
	case Pending:
		return waiting, nil
	case Invalid:
		return ignored, nil
	}
	cert := d.registry.Certificate(w.Certificate)
	if cert == nil || !bytes.Equal(cert.Participant, w.Author) || w.Verify(cert.Sign) != nil {
		return ignored, nil
	}
	c.statements[string(id)] = stmt{w, s}
	if bytes.Equal(w.Ref, zero32) {
		c.genesis = id
	}
	c.confirmKey(id)
	if c.startsEpoch(id) && bytes.Equal(c.epochOf(w.Ref), channel) {
		c.announced[string(id)] = true
	}
	epoch := c.epochOf(id)
	if epoch == nil {
		epoch = id
	}
	p, err := onePayload(rosterRecord, w)
	if err != nil {
		return ignored, err
	}
	return applied, d.store.Transaction(func() error {
		if _, err := d.store.PutWrite(id, cid, rosterRecord, epoch, p); err != nil {
			return err
		}
		if c.announced[string(id)] {
			return d.store.SetValue("ann."+hexs(cid)+"."+hexs(id), []byte{1})
		}
		return nil
	})
}

// dataWriteValidity: a data write in the channel of epoch (§8.3): rule 1, written under a
// counted statement of this very epoch that lists its author as owner or writer, by an
// admitted keychain extending the author's floor, and not late in a replaced epoch; or rule
// 2, an earlier write this epoch's inventory carries.
func (d *Device) dataWriteValidity(w *tandem.Write, cid, epoch []byte) Validity {
	c := d.collections[string(cid)]
	if c == nil {
		return pending()
	}
	s, ok := c.statements[string(w.Ref)]
	if !ok {
		return pending(w.Ref)
	}
	source := c.epochOf(w.Ref)
	if !bytes.Equal(w.Hub, d.pin.Hub) || source == nil {
		return invalid("another hub")
	}
	// A draft authorizes nothing (§9.4); held, not dropped, until its announcement or a grant shows it counts.
	if !c.counts(w.Ref) || !c.counts(source) {
		return pending(source)
	}
	member := s.s.member(w.Author)
	if member == nil || member.Role == Reader || (bytes.Equal(w.Record, metaRecord) && member.Role != Owner) {
		return invalid("not a writer here")
	}
	checks := []Validity{d.registry.Authorizes(w.Authorization, w.Certificate), d.registry.Extends(w.Authorization, member.Floor)}
	if bytes.Equal(source, epoch) {
		checks = append(checks, d.dataLateness(w, c, epoch))
	} else {
		if !c.precedes(w.Ref, epoch) && !bytes.Equal(w.Ref, epoch) {
			return invalid("not from an earlier epoch of this chain")
		}
		inv, ok := d.inventories[string(epoch)]
		if !ok {
			return pending(epoch)
		}
		if id := writeID(w); id == nil || !containsID(inv[string(w.Record)], id) {
			return invalid("not carried into this epoch")
		}
	}
	if v := allValid(checks...); !v.IsValid() {
		return v
	}
	cert := d.registry.Certificate(w.Certificate)
	if cert == nil || !bytes.Equal(cert.Participant, w.Author) || w.Verify(cert.Sign) != nil {
		return invalid("bad signature")
	}
	return valid()
}

// dataLateness: a write in an epoch a later one replaced counts only if that one's inventory
// carries it (§8.3, §11.6).
func (d *Device) dataLateness(w *tandem.Write, c *collection, epoch []byte) Validity {
	next := c.successorEpochs(epoch)
	id := writeID(w)
	if len(next) == 0 || id == nil {
		return valid()
	}
	var missing [][]byte
	for _, e := range next {
		inv, ok := d.inventories[string(e)]
		if !ok {
			missing = append(missing, e)
			continue
		}
		if containsID(inv[string(w.Record)], id) {
			return valid()
		}
	}
	if len(missing) == 0 {
		return invalid("late write in a replaced epoch")
	}
	return pending(missing...)
}
