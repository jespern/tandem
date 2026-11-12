// Package device is a Tandem device: one installation holding its user's
// secret, a replica of home and of every collection it follows, and a
// connection to the hub. It is the reference SDK's engine, and what the
// scenario harness drives.
//
// State lives in a directory and is written with fsync after every change
// (state.json), and before anything leaves the device for a key change
// (journal.json). Opening the directory again after a crash resumes from it.
package device

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/store"
)

// Device is one installation.
type Device struct {
	mu   sync.Mutex
	dir  string
	Name string
	u    *tandem.User
	id   [8]byte
	hlc  tandem.Dot
	hub  store.Store
	now  func() time.Time

	// Crash, if set, is called at every point where a real device could
	// stop; a test makes it panic and then opens the directory again.
	Crash func(point string)

	home *tandem.Record // all home records, merged per record below
	recs map[string]*tandem.Record
	cols map[string]*col
	idx  map[string]*msgInfo // hub id → what it is

	journal *journal
}

type msgInfo struct {
	Tag       string
	Hash      []byte
	Processed bool
	Kind      byte
	Record    []byte   // for record states
	Writes    [][]byte // write IDs it holds
	Part      int      // for inventory fragments
	Inventory bool
}

type col struct {
	id       []byte
	roster   *tandem.Roster
	stmts    map[string]*tandem.Write // every owner-signed statement held
	recs     map[string]map[string]*tandem.Record
	invs     map[string]tandem.Inventory
	frags    map[string]map[int][]byte // epoch → part → bytes
	parts    map[string]int
	held     []heldWrite
	replaced map[string]bool      // write IDs this device has seen superseded
	resigned map[string]bool      // own old writes already decided: signed again, carried, or left
	own      map[string]heldWrite // writes this device signed, by write ID, with their epoch
	unshared map[string]bool      // own writes a key change may have left behind (Unshared)
}

type heldWrite struct {
	W     *tandem.Write
	Epoch []byte
}

const homeEpoch = "home"

// New creates a device for a new user (secret nil) or an existing one,
// with a fresh device ID (§5.3), in an empty directory.
func New(ctx context.Context, dir, name string, secret []byte, hub store.Store) (*Device, error) {
	if secret == nil {
		secret = make([]byte, 32)
		rand.Read(secret)
	}
	u, err := tandem.NewUser(secret)
	if err != nil {
		return nil, err
	}
	d := newDevice(dir, name, u, hub)
	rand.Read(d.id[:])
	if err := hub.EnsureFolder(ctx); err != nil {
		return nil, err
	}
	return d, d.save()
}

func newDevice(dir, name string, u *tandem.User, hub store.Store) *Device {
	return &Device{dir: dir, Name: name, u: u, hub: hub, now: time.Now,
		recs: map[string]*tandem.Record{}, cols: map[string]*col{}, idx: map[string]*msgInfo{}}
}

func (d *Device) crash(point string) {
	if d.Crash != nil {
		d.Crash(point)
	}
}

// UID is the device's user ID.
func (d *Device) UID() []byte { return d.u.UID }

// UKa is the user's agreement key.
func (d *Device) UKa() []byte { return d.u.UKa }

// Secret is the user secret, for pairing a second device in tests.
func (d *Device) Secret() []byte { return d.u.Secret }

// tick advances the HLC (§9.1).
func (d *Device) tick() tandem.Dot {
	now := uint64(d.now().UnixMilli())
	if now > d.hlc.Millis {
		d.hlc = tandem.Dot{Device: d.id, Millis: now}
	} else if d.hlc.Counter == 0xffff {
		d.hlc = tandem.Dot{Device: d.id, Millis: d.hlc.Millis + 1}
	} else {
		d.hlc.Counter++
	}
	d.hlc.Device = d.id
	return d.hlc
}

// ---- home (§6.2) ----

func (d *Device) homeRec(name string) *tandem.Record {
	id := tandem.NamedID(name)
	r, ok := d.recs[string(id)]
	if !ok {
		r = tandem.NewRecord(id)
		d.recs[string(id)] = r
	}
	return r
}

// homeWrite signs a write to a home record, merges it and publishes the record.
func (d *Device) homeWrite(ctx context.Context, name, field string, op tandem.Op, value, element []byte) error {
	r := d.homeRec(name)
	c, b, _ := r.Vectors(op, field, element, homeEpoch, 0)
	w := &tandem.Write{Scope: tandem.HomeScope(d.u.UID), Record: r.ID, Field: field, Op: op, Value: value, Element: element,
		Dot: d.tick(), Ref: make([]byte, 32), Context: c, Base: b}
	if err := w.Sign(d.u); err != nil {
		return err
	}
	e, _ := tandem.NewEntry(w, []byte(homeEpoch), 0)
	r.Merge(e)
	if err := d.save(); err != nil {
		return err
	}
	return d.publishHome(ctx, r)
}

func (d *Device) publishHome(ctx context.Context, r *tandem.Record) error {
	var ws []*tandem.Write
	for _, e := range r.Kept {
		ws = append(ws, e.W)
	}
	sortWrites(ws)
	p, err := tandem.RecordPayload(r.ID, ws)
	if err != nil {
		return err
	}
	return d.append(ctx, d.u.Home, tandem.HomeTag(d.u.Home), p)
}

func sortWrites(ws []*tandem.Write) {
	slices.SortFunc(ws, func(a, b *tandem.Write) int {
		x, _ := a.ID()
		y, _ := b.ID()
		return bytes.Compare(x, y)
	})
}

// keyEntry is an element of a membership's keys set.
type keyEntry struct {
	Epoch string `json:"epoch"`
	K     string `json:"k"`
}

// Note is a pending key change, kept in home's flight field (§8.2).
type Note struct {
	Parent  string          `json:"parent"`
	Stmt    string          `json:"stmt"`
	K       string          `json:"k"`
	Members []tandem.Member `json:"members"`
	What    string          `json:"what"`
	Device  string          `json:"device"`
}

type membership struct {
	keys   map[string][]byte // epoch ID → key
	role   string
	state  string
	owner  []byte
	flight *Note
}

func (d *Device) membership(cid []byte) membership {
	r := d.homeRec("membership:" + tandem.B64u(cid))
	m := membership{keys: map[string][]byte{}}
	for _, el := range r.Elements("keys") {
		j, err := tandem.ParseJSON(el)
		if err != nil {
			continue
		}
		ep, _ := j.Get("epoch")
		k, _ := j.Get("k")
		e, err1 := tandem.ParseB64u(ep.Str)
		kk, err2 := tandem.ParseB64u(k.Str)
		if err1 == nil && err2 == nil {
			m.keys[string(e)] = kk
		}
	}
	if v, ok := r.Value("role"); ok {
		m.role = string(v)
	}
	if v, ok := r.Value("state"); ok {
		m.state = string(v)
	}
	if v, ok := r.Value("owner"); ok {
		m.owner = v
	}
	// No pending change: an empty value, or JSON null (conformance/ writes
	// null). Anything that doesn't name a statement isn't a note.
	if v, ok := r.Value("flight"); ok && len(v) > 0 && string(v) != "null" {
		var n Note
		if json.Unmarshal(v, &n) == nil && n.Stmt != "" {
			m.flight = &n
		}
	}
	return m
}

func (d *Device) addKey(ctx context.Context, cid, epoch, k []byte) error {
	el, _ := json.Marshal(keyEntry{tandem.B64u(epoch), tandem.B64u(k)})
	if d.membership(cid).keys[string(epoch)] != nil {
		return nil
	}
	return d.homeWrite(ctx, "membership:"+tandem.B64u(cid), "keys", tandem.OpAdd, nil, el)
}

func (d *Device) setMembership(ctx context.Context, cid []byte, field string, value []byte) error {
	if v, ok := d.homeRec("membership:" + tandem.B64u(cid)).Value(field); ok && bytes.Equal(v, value) {
		return nil
	}
	return d.homeWrite(ctx, "membership:"+tandem.B64u(cid), field, tandem.OpAssign, value, nil)
}

// memberships lists the collection IDs home knows.
func (d *Device) memberships() [][]byte {
	var out [][]byte
	for _, r := range d.recs {
		if v, ok := r.Value("_name"); ok && bytes.HasPrefix(v, []byte("membership:")) {
			if cid, err := tandem.ParseB64u(string(v[len("membership:"):])); err == nil {
				out = append(out, cid)
			}
		}
	}
	slices.SortFunc(out, bytes.Compare)
	return out
}

// Expect records a request to join a collection owned by owner, as a join
// request does (§11.2 step 3): a key is accepted only for such a collection.
func (d *Device) Expect(ctx context.Context, cid, owner []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := "membership:" + tandem.B64u(cid)
	if _, ok := d.homeRec(name).Value("_name"); !ok {
		if err := d.homeWrite(ctx, name, "_name", tandem.OpAssign, []byte(name), nil); err != nil {
			return err
		}
	}
	if err := d.setMembership(ctx, cid, "owner", owner); err != nil {
		return err
	}
	return d.setMembership(ctx, cid, "state", []byte("requested"))
}

// ---- hub ----

// append seals a payload for a channel and stores it, unless a message with
// the same contents is already there (a resumed step appends nothing twice).
func (d *Device) append(ctx context.Context, key []byte, tag string, payload []byte) error {
	return d.appendKind(ctx, key, tag, tandem.KindRecord, payload)
}

func (d *Device) appendKind(ctx context.Context, key []byte, tag string, kind byte, payload []byte) error {
	env, err := tandem.Seal(key, kind, tag, payload, nil)
	if err != nil {
		return err
	}
	_, err = d.hub.Append(ctx, tag, env)
	return err
}

// channel is a channel this device follows.
type channel struct {
	kind  string // home, inbox, epoch
	key   []byte
	col   []byte
	epoch []byte
}

func (d *Device) channels() map[string]channel {
	chs := map[string]channel{
		tandem.HomeTag(d.u.Home): {kind: "home", key: d.u.Home},
		tandem.InboxTag(d.u.UID): {kind: "inbox"},
	}
	for _, cid := range d.memberships() {
		m := d.membership(cid)
		for ep, k := range m.keys {
			chs[tandem.CollectionTag(k, []byte(ep))] = channel{kind: "epoch", key: k, col: cid, epoch: []byte(ep)}
		}
		if f := m.flight; f != nil { // the owner's devices can read a pending change's channel
			k, _ := tandem.ParseB64u(f.K)
			s, _ := tandem.ParseB64u(f.Stmt)
			chs[tandem.CollectionTag(k, s)] = channel{kind: "epoch", key: k, col: cid, epoch: s}
		}
	}
	if j := d.journal; j != nil {
		chs[tandem.CollectionTag(j.Key, j.StmtID)] = channel{kind: "epoch", key: j.Key, col: j.Col, epoch: j.StmtID}
	}
	return chs
}

// Sync reads everything new on the hub for the channels this device follows,
// following new keys as they arrive.
func (d *Device) Sync(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sync(ctx)
}

func (d *Device) sync(ctx context.Context) error {
	added, removed, err := d.hub.Changes(ctx)
	if err != nil {
		return err
	}
	for _, r := range added {
		if _, ok := d.idx[r.ID]; !ok {
			d.idx[r.ID] = &msgInfo{Tag: r.Tag}
		}
	}
	for _, id := range removed {
		delete(d.idx, id)
	}
	for {
		chs := d.channels()
		var ids []string
		for id, m := range d.idx {
			if _, ok := chs[m.Tag]; ok && !m.Processed {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			break
		}
		slices.SortFunc(ids, func(a, b string) int { // hub order, for numeric ids
			if len(a) != len(b) {
				return len(a) - len(b)
			}
			return strings.Compare(a, b)
		})
		msgs, err := d.hub.Fetch(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			info := d.idx[id]
			info.Processed = true
			m, ok := msgs[id]
			if !ok {
				continue
			}
			info.Hash = tandem.H(m.Envelope)
			if err := d.process(ctx, chs[m.Tag], info, m.Envelope); err != nil {
				return err
			}
		}
	}
	for _, c := range d.cols {
		d.settle(ctx, c)
		if err := d.resign(ctx, c); err != nil {
			return err
		}
	}
	return d.save()
}

func (d *Device) process(ctx context.Context, ch channel, info *msgInfo, env []byte) error {
	switch ch.kind {
	case "home":
		kind, pt, err := tandem.Open(ch.key, info.Tag, env)
		if err != nil || kind != tandem.KindRecord {
			return nil // junk in a channel we follow: logged and ignored (§12.2)
		}
		rs, _, err := tandem.ParseKind1(pt, tandem.HomeScope(d.u.UID))
		if err != nil || rs == nil {
			return nil
		}
		info.Kind, info.Record = kind, rs.Record
		r, ok := d.recs[string(rs.Record)]
		if !ok {
			r = tandem.NewRecord(rs.Record)
			d.recs[string(rs.Record)] = r
		}
		for _, w := range rs.Writes {
			if bytes.Equal(w.Author, d.u.UID) && w.Verify() == nil { // only the user's own key writes home
				e, _ := tandem.NewEntry(w, []byte(homeEpoch), 0)
				r.Merge(e)
				id, _ := w.ID()
				info.Writes = append(info.Writes, id)
			}
		}
	case "inbox":
		pt, err := tandem.OpenInbox(d.u, env)
		if err != nil {
			return nil
		}
		return d.inbox(ctx, pt)
	case "epoch":
		kind, pt, err := tandem.Open(ch.key, info.Tag, env)
		if err != nil || kind != tandem.KindRecord {
			return nil
		}
		info.Kind = kind
		return d.epochMessage(ch, info, pt)
	}
	return nil
}

func (d *Device) colFor(cid []byte) *col {
	c, ok := d.cols[string(cid)]
	if !ok {
		c = &col{id: cid, stmts: map[string]*tandem.Write{}, recs: map[string]map[string]*tandem.Record{},
			invs: map[string]tandem.Inventory{}, frags: map[string]map[int][]byte{}, parts: map[string]int{},
			replaced: map[string]bool{}, resigned: map[string]bool{}, own: map[string]heldWrite{}, unshared: map[string]bool{}}
		d.cols[string(cid)] = c
	}
	return c
}

func (d *Device) epochMessage(ch channel, info *msgInfo, pt []byte) error {
	c := d.colFor(ch.col)
	rs, frag, err := tandem.ParseKind1(pt, ch.col)
	if err != nil {
		return nil
	}
	if frag != nil {
		info.Inventory, info.Part = true, frag.Part
		if !bytes.Equal(frag.Epoch, ch.epoch) {
			return nil
		}
		ep := string(ch.epoch)
		if c.frags[ep] == nil {
			c.frags[ep] = map[int][]byte{}
		}
		c.frags[ep][frag.Part] = frag.Bytes
		c.parts[ep] = frag.Parts
		d.assemble(c, ch.epoch)
		return nil
	}
	info.Record = rs.Record
	if bytes.Equal(rs.Record, tandem.RosterID) {
		for _, w := range rs.Writes {
			if bytes.Equal(w.Ref, make([]byte, 32)) {
				w.Scope = make([]byte, 16) // a genesis is signed with a zero scope (§7.1)
			}
			id, _ := w.ID()
			info.Writes = append(info.Writes, id)
			d.addStatement(c, w, ch.epoch)
		}
		return nil
	}
	for _, w := range rs.Writes {
		id, _ := w.ID()
		info.Writes = append(info.Writes, id)
		c.held = append(c.held, heldWrite{w, ch.epoch})
	}
	return nil
}

func (d *Device) addStatement(c *col, w *tandem.Write, inEpoch []byte) {
	id, err := w.ID()
	if err != nil {
		return
	}
	if c.roster == nil {
		if bytes.Equal(w.Ref, make([]byte, 32)) {
			if r, err := tandem.NewRoster(w); err == nil && bytes.Equal(r.Collection, c.id) {
				c.roster = r
				c.stmts[string(id)] = w
			}
		} else {
			c.stmts[string(id)] = w // held until the genesis arrives
		}
		if c.roster != nil {
			for _, s := range c.stmts {
				c.roster.Add(s, nil)
			}
		}
		return
	}
	c.stmts[string(id)] = w
	c.roster.Add(w, inEpoch)
}

func (d *Device) assemble(c *col, epoch []byte) {
	ep := string(epoch)
	if _, ok := c.invs[ep]; ok || c.roster == nil {
		return
	}
	s, ok := c.roster.Get(epoch)
	if !ok || s.Body == nil || s.Body.Snapshot == nil {
		return
	}
	var b []byte
	for i := 0; i < c.parts[ep]; i++ {
		p, ok := c.frags[ep][i]
		if !ok {
			return
		}
		b = append(b, p...)
	}
	if !bytes.Equal(tandem.H(b), s.Body.Snapshot) {
		return // inconsistent versions are held until a matching assembly exists
	}
	if inv, err := tandem.ParseInventory(b); err == nil {
		c.invs[ep] = inv
	}
}

// settle judges held writes now that more may be known, and keeps the
// device's own late writes alive by signing them again (§11.6).
func (d *Device) settle(ctx context.Context, c *col) {
	if c.roster == nil {
		return
	}
	for ep := range c.frags {
		d.assemble(c, []byte(ep))
	}
	var still []heldWrite
	for _, h := range c.held {
		inv := c.invs[string(h.Epoch)] // nil until verified: old writes wait for it
		if s, ok := c.roster.Get(h.Epoch); ok && s.Body != nil && s.Body.Snapshot == nil {
			inv = tandem.Inventory{} // the genesis epoch carries nothing old
		}
		switch v, _ := c.roster.Judge(h.W, h.Epoch, inv); v {
		case tandem.Accept:
			d.mergeData(c, h.Epoch, h.W)
		case tandem.Hold:
			still = append(still, h)
		}
	}
	c.held = still
}

func (d *Device) epochN(c *col, epoch []byte) int64 {
	if s, ok := c.roster.Get(epoch); ok && s.Body != nil {
		return s.Body.KeyN
	}
	return 0
}

func (d *Device) mergeData(c *col, epoch []byte, w *tandem.Write) {
	ep := string(epoch)
	if c.recs[ep] == nil {
		c.recs[ep] = map[string]*tandem.Record{}
	}
	r, ok := c.recs[ep][string(w.Record)]
	if !ok {
		r = tandem.NewRecord(w.Record)
		c.recs[ep][string(w.Record)] = r
	}
	// A write's epoch for merging is its source epoch (§9.1), not the
	// channel it was carried into.
	src := c.roster.EpochOf(w.Ref)
	e, err := tandem.NewEntry(w, src, d.epochN(c, src))
	if err != nil {
		return
	}
	before := map[string]bool{}
	for id := range r.Kept {
		before[id] = true
	}
	r.Merge(e)
	for id := range before {
		if _, ok := r.Kept[id]; !ok {
			c.replaced[id] = true
		}
	}
	if _, ok := r.Kept[string(e.ID)]; !ok {
		c.replaced[string(e.ID)] = true
	}
}

// ---- inbox (§11.4) ----

type grantJSON struct {
	Type  string `json:"type"`
	C     string `json:"c"`
	At    string `json:"at"`
	Epoch string `json:"epoch"`
	K     string `json:"k"`
	Sig   string `json:"sig"`
}

func (d *Device) inbox(ctx context.Context, pt []byte) error {
	j, err := tandem.ParseJSON(pt)
	if err != nil {
		return nil
	}
	t, _ := j.Get("type")
	if t.Str != "grant" {
		return nil
	}
	field := func(name string, n int) []byte {
		v, _ := j.Get(name)
		b, err := tandem.ParseB64u(v.Str)
		if err != nil || n > 0 && len(b) != n {
			return nil
		}
		return b
	}
	cid, at, epoch, k, sig := field("c", 16), field("at", 32), field("epoch", 32), field("k", 32), field("sig", 64)
	if cid == nil || at == nil || epoch == nil || k == nil || sig == nil {
		return nil
	}
	m := d.membership(cid)
	if m.state != "requested" && m.state != "active" {
		return nil // a key only for a collection asked for or followed
	}
	owner := m.owner
	if c, ok := d.cols[string(cid)]; ok && c.roster != nil {
		owner = c.roster.Owner
	}
	if !tandem.VerifyStrict(owner, tandem.GrantInput(d.u.UID, cid, at, epoch, k), sig) {
		return nil
	}
	// Read the epoch's channel with the granted key, then check the roster.
	tag := tandem.CollectionTag(k, epoch)
	var ids []string
	for id, info := range d.idx {
		if info.Tag == tag {
			ids = append(ids, id)
		}
	}
	msgs, err := d.hub.Fetch(ctx, ids)
	if err != nil {
		return err
	}
	ch := channel{kind: "epoch", key: k, col: cid, epoch: epoch}
	for id, msg := range msgs {
		info := d.idx[id]
		info.Processed, info.Hash = true, tandem.H(msg.Envelope)
		d.process(ctx, ch, info, msg.Envelope)
	}
	c := d.colFor(cid)
	if c.roster == nil || !bytes.Equal(c.roster.Owner, owner) {
		return nil
	}
	c.roster.Announce(epoch) // a verified grant counts as the announcement
	s, ok := c.roster.Get(at)
	if !ok || s.Body == nil || !c.roster.OnChain(at) || !bytes.Equal(c.roster.EpochOf(at), epoch) ||
		!bytes.Equal(s.Body.Commit, tandem.Commit(k)) || s.Body.Role(d.u.UID) == "" {
		return nil
	}
	if err := d.addKey(ctx, cid, epoch, k); err != nil {
		return err
	}
	if err := d.setMembership(ctx, cid, "role", []byte(s.Body.Role(d.u.UID))); err != nil {
		return err
	}
	return d.setMembership(ctx, cid, "state", []byte("active"))
}

func (d *Device) sendGrant(ctx context.Context, c *col, to tandem.Member2, at, epoch, k []byte) error {
	sig := tandemSign(d.u, tandem.GrantInput(to.UID, c.id, at, epoch, k))
	pt, err := json.Marshal(grantJSON{"grant", tandem.B64u(c.id), tandem.B64u(at), tandem.B64u(epoch), tandem.B64u(k), tandem.B64u(sig)})
	if err != nil {
		return err
	}
	env, err := tandem.SealInbox(to.UID, to.UKa, pt)
	if err != nil {
		return err
	}
	_, err = d.hub.Append(ctx, tandem.InboxTag(to.UID), env)
	return err
}

// ---- reading ----

// current returns a collection's head statement and its epoch.
func (d *Device) current(cid []byte) (*col, *tandem.Stmt, []byte, error) {
	c, ok := d.cols[string(cid)]
	if !ok || c.roster == nil {
		return nil, nil, nil, errors.New("device: collection not followed")
	}
	h := c.roster.Head()
	return c, h, c.roster.EpochOf(h.ID), nil
}

// Value is a field's displayed value in a collection's current epoch.
func (d *Device) Value(cid, rec []byte, field string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, _, ep, err := d.current(cid)
	if err != nil {
		return "", false
	}
	r, ok := c.recs[string(ep)][string(rec)]
	if !ok || r.Deleted() {
		return "", false
	}
	v, ok := r.Value(field)
	return string(v), ok
}

// Dead reports whether a collection is dead (§9.4).
func (d *Device) Dead(cid []byte) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.cols[string(cid)]
	return ok && c.roster != nil && c.roster.Dead()
}

// Epoch is the collection's current epoch-start ID, as this device sees it.
func (d *Device) Epoch(cid []byte) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _, ep, err := d.current(cid)
	if err != nil {
		return nil
	}
	return ep
}

// Members returns the head statement's members by name and role.
func (d *Device) Members(cid []byte) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, h, _, err := d.current(cid)
	if err != nil || h.Body == nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range h.Body.Members {
		out[m.Name] = m.Role
	}
	return out
}

// Pending returns another device's unfinished key change, if any (§11.7).
func (d *Device) Pending(cid []byte) *Note {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.membership(cid).flight
	if n == nil || d.journal != nil && bytes.Equal(d.journal.Col, cid) {
		return nil
	}
	return n
}

func tandemSign(u *tandem.User, msg []byte) []byte { return ed25519.Sign(u.Sign, msg) }

func (d *Device) String() string { return fmt.Sprintf("%s(%x)", d.Name, d.id[:2]) }
