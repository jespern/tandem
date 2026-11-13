package engine

import (
	"bytes"
	"context"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// A participant's home (§6.2): its epochs, their keys, and the named records every device
// of that participant shares. Writes are signed by an admitted keychain of the participant
// and merged as §9 says.

func (d *Device) homeScope() []byte {
	if d.me == nil {
		return nil
	}
	return tandem.HomeScope(d.pin.Hub, d.me.Participant)
}

// HomeHeads is the home epochs no epoch names as a parent and whose key this device holds (§10.8).
func (d *Device) HomeHeads() [][]byte {
	defer d.read()()
	return d.homeHeads()
}

func (d *Device) homeHeads() [][]byte { return heads(d.homeEpochs, d.homeKeys) }

func heads(epochs map[string]*tandem.Control, keys map[string][]byte) [][]byte {
	named := map[string]bool{}
	for _, e := range epochs {
		for _, p := range fset(e, "parents") {
			named[string(p)] = true
		}
	}
	var out [][]byte
	for _, k := range sortedKeys(epochs) {
		if !named[k] && keys[k] != nil {
			out = append(out, []byte(k))
		}
	}
	return out
}

// currentHomeEpoch is the epoch this device writes to: the smallest current head (§10.8).
func (d *Device) currentHomeEpoch() []byte {
	if h := d.homeHeads(); len(h) > 0 {
		return h[0]
	}
	return nil
}

func (d *Device) epochNumber(epoch []byte) uint64 { return epochNumber(epoch, d.homeEpochs) }

func epochNumber(epoch []byte, epochs map[string]*tandem.Control) uint64 {
	n, _ := fint(epochs[string(epoch)], "n")
	return n
}

// Inbox (§11.4, §10.8)

func (d *Device) applyInbox(m tandem.Message, me *Me) (outcome, error) {
	pt, err := tandem.OpenInbox(d.pin.Hub, me.Participant, me.Keys.Agreement, m.Body)
	if err != nil {
		return ignored, nil // sealed to another of this participant's keychains, or malformed
	}
	j, err := tandem.ParseJSON(pt)
	if err != nil {
		return ignored, nil
	}
	typ, err := j.Text("type")
	if err != nil {
		return ignored, nil
	}
	switch typ {
	case "home-key":
		n, err := ParseNotice(pt, "home-key", "epoch", "k")
		if err != nil || len(n.Get("k")) != 32 {
			return ignored, nil
		}
		ok, err := d.offerHomeKey(n.Get("epoch"), n.Get("k"))
		if err != nil || !ok {
			return ignored, err
		}
		d.changed(Event{Kind: EventHome})
		d.changed(Event{Kind: EventLists})
		return applied, nil
	case "grant":
		o, err := d.receiveGrant(pt, j, me)
		if o == applied {
			d.changed(Event{Kind: EventOffers})
			d.changed(Event{Kind: EventLists})
		}
		return o, err
	case "key-request":
		return d.receiveKeyRequest(j)
	case "roster":
		return d.receiveRosterReply(j)
	case "answer":
		o, err := d.receiveAnswer(j)
		if o == applied {
			if a, err := tandem.ParseControl(j, "answer"); err == nil {
				d.changed(Event{Kind: EventMembers, List: fid(a, "collection")})
			}
		}
		return o, err
	case "pair-claim":
		o, err := d.receiveClaim(pt, j)
		if o == applied {
			d.changed(Event{Kind: EventClaims})
		}
		return o, err
	case "pair-release":
		o, err := d.receiveRelease(pt, me)
		if o == applied {
			d.changed(Event{Kind: EventApproved})
			d.changed(Event{Kind: EventHome})
			d.changed(Event{Kind: EventLists})
		}
		return o, err
	}
	return ignored, nil
}

// Home keys

// offerHomeKey takes a home key from anywhere unauthenticated (the inbox, a pairing package,
// a recovery box). It becomes this home's key only once the epoch's signed start commits to
// it; until then it is a candidate, used only to read the channel where that start is. A
// wrong key never displaces the right one.
func (d *Device) offerHomeKey(epoch, key []byte) (bool, error) {
	if d.homeKeys[string(epoch)] != nil || len(key) != 32 {
		return false, nil
	}
	if start := d.homeEpochs[string(epoch)]; start != nil {
		if !bytes.Equal(fid(start, "key"), tandem.Commit(key)) {
			return false, nil
		}
		return true, d.confirmHomeKey(epoch, key)
	}
	cands := d.homeCandidates[string(epoch)]
	if cands == nil {
		cands = map[string][]byte{}
		d.homeCandidates[string(epoch)] = cands
	}
	if cands[string(key)] != nil {
		return false, nil
	}
	cands[string(key)] = key
	return true, d.store.SetValue("home.cand."+hexs(epoch)+"."+hexs(tandem.H(key)), key)
}

func (d *Device) dropCandidates(epoch []byte) error {
	delete(d.homeCandidates, string(epoch))
	vs, err := d.store.Values("home.cand." + hexs(epoch) + ".")
	if err != nil {
		return err
	}
	for _, v := range vs {
		if err := d.store.RemoveValue(v.Name); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) confirmHomeKey(epoch, key []byte) error {
	d.homeKeys[string(epoch)] = key
	if err := d.store.SetValue("home.key."+hexs(epoch), key); err != nil {
		return err
	}
	if err := d.dropCandidates(epoch); err != nil {
		return err
	}
	return d.moveHome()
}

// Home channels

func (d *Device) applyHome(m tandem.Message, epoch, key []byte, me *Me) (outcome, error) {
	kind, pt, err := tandem.Open(key, d.pin.Hub, m.Channel, m.Body)
	if err != nil {
		return ignored, nil
	}
	switch kind {
	case tandem.KindRequest:
		o, err := tandem.FromJSON(pt, "home-epoch")
		if err != nil {
			return ignored, nil
		}
		return d.acceptHomeEpoch(o, me)
	case tandem.KindRecord:
		scope := d.homeScope()
		state, frag, err := tandem.ParseKind1(pt, d.pin.Hub, scope)
		if err != nil {
			return ignored, nil
		}
		if frag != nil {
			start := d.homeEpochs[string(epoch)]
			if start == nil {
				return waiting, nil
			}
			if err := d.store.SetValue("fragmsg."+m.ID, append(bytes.Clone(epoch), tandem.H(frag.Bytes)...)); err != nil {
				return ignored, err
			}
			done, err := d.receiveFragment(frag, epoch, fid(start, "snapshot"))
			if err != nil || !done {
				return ignored, err
			}
			return applied, d.moveHome()
		}
		var entries []tandem.Entry
		for _, w := range state.Writes {
			switch v := d.homeWriteValidity(w, epoch, me); v.State {
			case Valid:
				e, err := entryOf(w, w.Ref, d.epochNumber(w.Ref))
				if err != nil {
					return ignored, err
				}
				entries = append(entries, e)
			case Pending:
				return waiting, nil
			}
		}
		if len(entries) == 0 {
			return ignored, nil
		}
		if err := d.mergeHome(entries, state.Record, scope); err != nil {
			return ignored, err
		}
		var ids [][]byte
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		return applied, d.store.NoteContents(m.ID, state.Record, ids)
	}
	return ignored, nil
}

// acceptHomeEpoch takes a home-epoch start for this participant (§10.8): signed by one of
// this participant's own keychains, admitted under auth, which extends every parent's; every
// recipient admitted under it. A start whose signer is revoked by now is taken only as the
// parent of an accepted one.
func (d *Device) acceptHomeEpoch(o *tandem.Control, me *Me) (outcome, error) {
	id := oid(o)
	if d.homeEpochs[string(id)] != nil {
		return ignored, nil
	}
	switch v := d.homeEpochValidity(o, me); v.State {
	case Pending:
		d.heldStarts[string(id)] = o
		return waiting, nil
	case Invalid:
		return ignored, nil
	}
	if signer := fid(o, "signer"); !d.registry.Activity(signer).IsValid() && len(d.homeChildren(id)) == 0 {
		d.heldStarts[string(id)] = o // kept aside: a later start naming it brings it in
		return ignored, nil
	}
	return applied, d.takeHomeEpoch(o, id)
}

func (d *Device) takeHomeEpoch(o *tandem.Control, id []byte) error {
	// Starts held aside come in with their accepted child, and only then (§10.8).
	for _, p := range fset(o, "parents") {
		if held := d.heldStarts[string(p)]; held != nil && d.homeEpochs[string(p)] == nil {
			if err := d.takeHomeEpoch(held, p); err != nil {
				return err
			}
		}
	}
	d.homeEpochs[string(id)] = o
	delete(d.heldStarts, string(id))
	if _, err := d.store.PutObject(o); err != nil {
		return err
	}
	if cands := d.homeCandidates[string(id)]; cands != nil {
		for _, k := range sortedKeys(cands) {
			if bytes.Equal(fid(o, "key"), tandem.Commit(cands[k])) {
				return d.confirmHomeKey(id, cands[k])
			}
		}
		if err := d.dropCandidates(id); err != nil { // none of them is this epoch's key
			return err
		}
	}
	return d.moveHome()
}

func (d *Device) homeEpochValidity(o *tandem.Control, me *Me) Validity {
	return d.homeEpochValidityAt(o, me, 0)
}

func (d *Device) homeEpochValidityAt(o *tandem.Control, me *Me, depth int) Validity {
	signer, auth := fid(o, "signer"), fid(o, "auth")
	if !bytes.Equal(fid(o, "hub"), d.pin.Hub) || !bytes.Equal(fid(o, "genesis"), d.pin.Genesis) ||
		!bytes.Equal(fid(o, "participant"), me.Participant) || signer == nil || auth == nil {
		return invalid("malformed home epoch")
	}
	checks := []Validity{d.registry.Authorizes(auth, signer)}
	if !checks[0].IsValid() {
		return checks[0]
	}
	cert := d.registry.Certificate(signer)
	if cert == nil || !bytes.Equal(cert.Participant, me.Participant) || !cverify(o, "sig", cert.Sign) {
		return invalid("not signed by a keychain of this participant")
	}
	for _, p := range fset(o, "parents") {
		// A parent known only as a start held aside is checked here, but taken only with an
		// accepted child (takeHomeEpoch): checking a start must not take anything in.
		parent := d.homeEpochs[string(p)]
		if parent == nil {
			held := d.heldStarts[string(p)]
			if held == nil {
				return pending(p)
			}
			if depth >= 64 {
				return invalid("home epoch chain too deep")
			}
			if v := d.homeEpochValidityAt(held, me, depth+1); !v.IsValid() {
				return v
			}
			parent = held
		}
		checks = append(checks, d.registry.Extends(auth, fid(parent, "auth")))
	}
	for _, r := range fset(o, "recipients") {
		checks = append(checks, d.registry.Authorizes(auth, r))
	}
	return allValid(checks...)
}

// homeWriteValidity: whether w is a valid home write in the channel of epoch (§6.2, §8.3):
// by this participant, under an admitted keychain and its snapshot, signed; and either
// written in this very epoch under a snapshot extending the epoch's policy, or written
// earlier and listed in this epoch's inventory.
func (d *Device) homeWriteValidity(w *tandem.Write, epoch []byte, me *Me) Validity {
	if !bytes.Equal(w.Author, me.Participant) || !bytes.Equal(w.Hub, d.pin.Hub) {
		return invalid("not this participant's home write")
	}
	start := d.homeEpochs[string(epoch)]
	if start == nil {
		return pending(epoch)
	}
	ok := d.registry.Authorizes(w.Authorization, w.Certificate)
	if bytes.Equal(w.Ref, epoch) {
		ok = allValid(ok, d.registry.Extends(w.Authorization, fid(start, "auth")), d.lateness(w, epoch))
	} else {
		inv, has := d.inventories[string(epoch)]
		if !has {
			return pending(epoch)
		}
		if id := writeID(w); id == nil || !containsID(inv[string(w.Record)], id) {
			return invalid("not carried into this epoch")
		}
		ok = allValid(ok, d.homeAncestry(w.Ref, epoch))
	}
	if !ok.IsValid() {
		return ok
	}
	cert := d.registry.Certificate(w.Certificate)
	if cert == nil || !bytes.Equal(cert.Participant, me.Participant) || w.Verify(cert.Sign) != nil {
		return invalid("bad signature")
	}
	return valid()
}

// homeChildren is the home epochs that name epoch as a parent.
func (d *Device) homeChildren(epoch []byte) [][]byte {
	var out [][]byte
	for _, k := range sortedKeys(d.homeEpochs) {
		if containsID(fset(d.homeEpochs[k], "parents"), epoch) {
			out = append(out, []byte(k))
		}
	}
	return out
}

// lateness: a write in an epoch a later one replaced counts only if a later inventory carries
// it (§8.3, §11.6): what arrives late in an old channel doesn't enter the current home.
func (d *Device) lateness(w *tandem.Write, epoch []byte) Validity {
	children := d.homeChildren(epoch)
	id := writeID(w)
	if len(children) == 0 || id == nil {
		return valid()
	}
	var missing [][]byte
	for _, c := range children {
		inv, ok := d.inventories[string(c)]
		if !ok {
			missing = append(missing, c)
			continue
		}
		if containsID(inv[string(w.Record)], id) {
			return valid()
		}
	}
	if len(missing) == 0 {
		return invalid("late write in a replaced home epoch")
	}
	return pending(missing...)
}

// homeAncestry: whether ancestor is reachable from epoch through parents; pending while a
// start on the way is missing.
func (d *Device) homeAncestry(ancestor, epoch []byte) Validity {
	queue := [][]byte{epoch}
	seen := map[string]bool{}
	for len(queue) > 0 {
		e := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[string(e)] {
			continue
		}
		seen[string(e)] = true
		start := d.homeEpochs[string(e)]
		if start == nil {
			return pending(e)
		}
		parents := fset(start, "parents")
		if containsID(parents, ancestor) {
			return valid()
		}
		queue = append(queue, parents...)
	}
	return invalid("not an earlier home epoch")
}

// homeEligible: whether an entry belongs to the current home: written in a current head, or
// carried by a head's inventory.
func homeEligible(e tandem.Entry, heads [][]byte, inventories map[string]tandem.Inventory) bool {
	if containsID(heads, []byte(e.Epoch)) {
		return true
	}
	for _, h := range heads {
		if containsID(inventories[string(h)][string(e.W.Record)], e.ID) {
			return true
		}
	}
	return false
}

// moveHome moves to the current heads (§11.6, "Moving over"): it rebuilds every home record
// from the stored writes that the heads keep. It waits while a head's inventory is incomplete.
func (d *Device) moveHome() error {
	scope := d.homeScope()
	if scope == nil {
		return nil
	}
	hs := d.homeHeads()
	// With no head it holds the key of (a revoked device), the replica stays as it was, read-only.
	if len(hs) == 0 {
		return nil
	}
	for _, h := range hs {
		if _, ok := d.inventories[string(h)]; !ok && len(fset(d.homeEpochs[string(h)], "parents")) > 0 {
			return nil
		}
	}
	records, err := d.store.Records(scope)
	if err != nil {
		return err
	}
	for _, r := range records {
		rec, err := loadRecord(d.store, d.pin.Hub, scope, r, d.homeEpochs, hs, true, d.inventories)
		if err != nil {
			return err
		}
		d.home[string(r)] = rec
	}
	return nil
}

func (d *Device) mergeHome(entries []tandem.Entry, record, scope []byte) error {
	r, err := merged(d.home[string(record)], d.pin.Hub, scope, record, entries...)
	if err != nil {
		return err
	}
	err = d.store.Transaction(func() error {
		for _, e := range entries {
			p, err := onePayload(record, e.W)
			if err != nil {
				return err
			}
			if _, err := d.store.PutWrite(e.ID, scope, record, []byte(e.Epoch), p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.home[string(record)] = r
	return nil
}

// loadRecord is a record as the store holds it: every write kept, merged again; with
// useHeads, only the writes the heads keep.
func loadRecord(store *Store, hub, scope, record []byte, epochs map[string]*tandem.Control, heads [][]byte, useHeads bool,
	inventories map[string]tandem.Inventory) (*tandem.Record, error) {
	rows, err := store.Writes(scope, record)
	if err != nil {
		return nil, err
	}
	var es []tandem.Entry
	for _, row := range rows {
		w := storedWrite(row.Payload, hub, scope)
		if w == nil {
			continue
		}
		e, err := entryOf(w, row.Epoch, epochNumber(row.Epoch, epochs))
		if err != nil {
			return nil, err
		}
		if useHeads && !homeEligible(e, heads, inventories) {
			continue
		}
		es = append(es, e)
	}
	return merged(nil, hub, scope, record, es...)
}

// Writing home records

// nextDot is the next dot of this installation's clock, persisted before the write can leave
// the device (§9.1), in one transaction.
func (d *Device) nextDot(me *Me) (tandem.Dot, error) {
	var dot tandem.Dot
	err := d.store.Transaction(func() error {
		millis, counter := d.clock()
		now := uint64(time.Now().UnixMilli())
		m, c, err := tandem.NextClock(millis, counter, now)
		if err != nil {
			return err
		}
		out := make([]byte, 10)
		for i := 0; i < 8; i++ {
			out[i] = byte(m >> (56 - 8*i))
		}
		out[8], out[9] = byte(c>>8), byte(c)
		dot = tandem.Dot{Device: me.Installation, Millis: m, Counter: c}
		return d.store.SetValue("clock", out)
	})
	return dot, err
}

// clock is the last dot this installation reserved.
func (d *Device) clock() (uint64, uint16) {
	held, ok := d.store.Value("clock")
	if !ok || len(held) != 10 {
		return 0, 0
	}
	var m uint64
	for _, b := range held[:8] {
		m = m<<8 | uint64(b)
	}
	return m, uint16(held[8])<<8 | uint16(held[9])
}

// writeHome signs one home write on name and publishes it in the current home epoch.
func (d *Device) writeHome(ctx context.Context, name, field string, op tandem.Op, val, element []byte) error {
	me, scope, epoch := d.me, d.homeScope(), d.currentHomeEpoch()
	if me == nil || scope == nil || epoch == nil || d.homeKeys[string(epoch)] == nil {
		return fail("device: no home to write to")
	}
	record := tandem.NamedID(name)
	var writes []*tandem.Write
	if _, ok := value(d.home[string(record)], "_name"); !ok && field != "_name" {
		w, err := d.signHome(me, scope, record, epoch, "_name", tandem.OpAssign, []byte(name), nil, writes)
		if err != nil {
			return err
		}
		writes = append(writes, w)
	}
	w, err := d.signHome(me, scope, record, epoch, field, op, val, element, writes)
	if err != nil {
		return err
	}
	writes = append(writes, w)
	var entries []tandem.Entry
	for _, w := range writes {
		e, err := entryOf(w, epoch, d.epochNumber(epoch))
		if err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if err := d.mergeHome(entries, record, scope); err != nil {
		return err
	}
	d.changed(Event{Kind: EventHome})
	if strings.HasPrefix(name, "membership:") {
		d.changed(Event{Kind: EventLists})
	}
	return d.publishHome(ctx, record, epoch, keptWrites(d.home[string(record)]))
}

// publishHome puts one home record's state in a home epoch's channel (§10.7).
func (d *Device) publishHome(ctx context.Context, record, epoch []byte, writes []*tandem.Write) error {
	key := d.homeKeys[string(epoch)]
	if d.me == nil || key == nil {
		return nil
	}
	tag := tandem.HomeTag(key, d.pin.Hub, d.me.Participant, epoch)
	return d.publishRecord(ctx, key, tag, record, writes)
}

// publishRecord seals one record's writes into a channel and notes what the message carries.
func (d *Device) publishRecord(ctx context.Context, key []byte, tag string, record []byte, writes []*tandem.Write) error {
	p, err := tandem.RecordPayload(record, writes)
	if err != nil {
		return err
	}
	env, err := tandem.Seal(key, d.pin.Hub, tandem.KindRecord, tag, p)
	if err != nil {
		return err
	}
	id, err := d.hubAppend(ctx, tag, env)
	if err != nil {
		return err
	}
	var ids [][]byte
	for _, w := range writes {
		ids = append(ids, writeID(w))
	}
	return d.store.Transaction(func() error {
		if err := d.store.NoteMessage(id, tag, tandem.H(env)); err != nil {
			return err
		}
		if err := d.store.MarkApplied(id); err != nil {
			return err
		}
		return d.store.NoteContents(id, record, ids)
	})
}

func (d *Device) signHome(me *Me, scope, record, epoch []byte, field string, op tandem.Op, val, element []byte, earlier []*tandem.Write) (*tandem.Write, error) {
	n := d.epochNumber(epoch)
	r := d.home[string(record)]
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
		if r, err = merged(r, d.pin.Hub, scope, record, es...); err != nil {
			return nil, err
		}
	}
	if r == nil {
		r = emptyRecord(d.pin.Hub, scope, record)
	}
	return d.signWrite(me, r, scope, record, epoch, int64(n), epoch, field, op, val, element)
}

// signWrite signs one write of record r, superseding everything the device knows of its slot.
func (d *Device) signWrite(me *Me, r *tandem.Record, scope, record, epoch []byte, n int64, ref []byte, field string, op tandem.Op,
	val, element []byte) (*tandem.Write, error) {
	ctx, base, ok := r.Vectors(op, field, element, string(epoch), n)
	if !ok {
		return nil, fail("device: this field has too many concurrent writers to settle")
	}
	dot, err := d.nextDot(me)
	if err != nil {
		return nil, err
	}
	if op != tandem.OpAssign {
		val = nil
	}
	if (op == tandem.OpAdd || op == tandem.OpRemove) && element == nil {
		element = []byte{}
	}
	w := &tandem.Write{Hub: d.pin.Hub, Certificate: me.Certificate, Authorization: me.Auth, Scope: scope, Record: record,
		Field: field, Op: op, Element: element, Value: val, Author: me.Participant, Dot: dot, Ref: ref, Context: ctx, Base: base}
	return w, w.Sign(me.Keys.Signing)
}

// SetHome sets a field of a home record (§6.2). settings:<app> records are the app's own.
func (d *Device) SetHome(ctx context.Context, name, field string, value []byte) error {
	defer d.begin()()
	return d.setHome(ctx, name, field, value)
}

func (d *Device) setHome(ctx context.Context, name, field string, value []byte) error {
	return d.writeHome(ctx, name, field, tandem.OpAssign, value, nil)
}

// AddHome adds an element to a set field of a home record.
func (d *Device) AddHome(ctx context.Context, name, field string, element []byte) error {
	defer d.begin()()
	return d.addHome(ctx, name, field, element)
}

func (d *Device) addHome(ctx context.Context, name, field string, element []byte) error {
	return d.writeHome(ctx, name, field, tandem.OpAdd, nil, element)
}

// HomeValue is a home record's field, if it has a value. Of concurrent values, the first.
func (d *Device) HomeValue(name, field string) ([]byte, bool) {
	defer d.read()()
	return d.homeValue(name, field)
}

func (d *Device) homeValue(name, field string) ([]byte, bool) {
	return value(d.home[string(tandem.NamedID(name))], field)
}

// HomeElements is a home record's set field.
func (d *Device) HomeElements(name, field string) [][]byte {
	defer d.read()()
	return d.homeElements(name, field)
}

func (d *Device) homeElements(name, field string) [][]byte {
	return elements(d.home[string(tandem.NamedID(name))], field)
}
