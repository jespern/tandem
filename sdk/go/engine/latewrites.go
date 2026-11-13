package engine

import (
	"bytes"
	"context"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// After a key change (§11.6): this participant's late writes signed again into the new epoch,
// and listed writes the new channel lacks carried over by any device that holds them. Two
// protections against a member's forged context: a claim about this device beyond any dot it
// issued is forged and doesn't stop signing again; a write this device held as kept that the
// new state claims to replace is kept for the app as possibly unshared.

// Unshared is one of this participant's writes in a list that a key change may have left behind.
type Unshared struct{ Record, Write []byte }

// Unshared is this participant's writes in a list that a key change may have left behind: the
// app offers to recover or dismiss them.
func (d *Device) Unshared(list []byte) []Unshared {
	defer d.read()()
	prefix := "unshared." + hexs(list) + "."
	vs, _ := d.store.Values(prefix)
	var out []Unshared
	for _, v := range vs {
		if w, ok := unhexs(strings.TrimPrefix(v.Name, prefix)); ok {
			out = append(out, Unshared{Record: v.Value, Write: w})
		}
	}
	return out
}

// DismissUnshared dismisses a write Unshared reported.
func (d *Device) DismissUnshared(list, write []byte) error {
	defer d.begin()()
	return d.store.RemoveValue("unshared." + hexs(list) + "." + hexs(write))
}

type storedEntry struct {
	record []byte
	entry  tandem.Entry
}

// storedWrites is every stored data write of a list, with the epoch it was written in.
func (d *Device) storedWrites(c *collection) ([]storedEntry, error) {
	var out []storedEntry
	records, err := d.store.Records(c.id)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if bytes.Equal(record, rosterRecord) {
			continue
		}
		rows, err := d.store.Writes(c.id, record)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			w := storedWrite(row.Payload, d.pin.Hub, c.id)
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
			out = append(out, storedEntry{record, e})
		}
	}
	return out, nil
}

// forged: whether w claims, for this installation, a dot beyond any it has issued.
func (d *Device) forged(w *tandem.Write, me *Me) bool {
	millis, counter := d.clock()
	issued := tandem.Dot{Millis: millis, Counter: counter}
	for _, v := range []tandem.VV{w.Context, w.Base} {
		for _, dot := range v {
			if dot.Device == me.Installation && dotLess(issued, dot) {
				return true
			}
		}
	}
	return false
}

// firstWriterStatement is the first statement of epoch that lets this participant write: the
// ref of a write signed again.
func (d *Device) firstWriterStatement(c *collection, epoch []byte, me *Me) []byte {
	for _, id := range chain(c, c.head()) {
		if !bytes.Equal(c.epochOf(id), epoch) {
			continue
		}
		if m := c.statements[string(id)].s.member(me.Participant); m != nil && (m.Role == Owner || m.Role == Writer) {
			return id
		}
	}
	return nil
}

// settleLateWrites settles this participant's writes from replaced epochs of every list it
// follows in its current epoch.
func (d *Device) settleLateWrites(ctx context.Context) error {
	me := d.me
	if me == nil || !d.registry.Activity(me.Certificate).IsValid() {
		return nil
	}
	for _, k := range sortedKeys(d.collections) {
		cid := []byte(k)
		if err := d.carryListed(ctx, cid); err != nil {
			return err
		}
		if err := d.resign(ctx, cid, me); err != nil {
			return err
		}
	}
	return nil
}

// currentEpoch is the current epoch of a list, its key and inventory, once this device can act in it.
func (d *Device) currentEpoch(cid []byte) (epoch, key []byte, inv tandem.Inventory, ok bool) {
	c := d.collections[string(cid)]
	if c == nil || c.dead() {
		return nil, nil, nil, false
	}
	epoch = c.epochOf(c.head())
	if epoch == nil || bytes.Equal(epoch, c.genesis) || c.keys[string(epoch)] == nil {
		return nil, nil, nil, false
	}
	inv, ok = d.inventories[string(epoch)]
	return epoch, c.keys[string(epoch)], inv, ok
}

func (d *Device) resign(ctx context.Context, cid []byte, me *Me) error {
	current, _, inv, ok := d.currentEpoch(cid)
	c := d.collections[string(cid)]
	if !ok {
		return nil
	}
	all, err := d.storedWrites(c)
	if err != nil {
		return err
	}
	touched := map[string][]*tandem.Write{}
	var resigned []string
	// Only this installation's own writes on this keychain: another device's, a revoked one's
	// above all, is not ours to sign again (§11.6, "the same still-admitted authoring device").
	for _, x := range all {
		if !bytes.Equal(x.entry.W.Certificate, me.Certificate) || x.entry.W.Dot.Device != me.Installation || x.entry.Epoch == string(current) {
			continue
		}
		done := "resigned." + hexs(x.entry.ID)
		if _, ok := d.store.Value(done); ok {
			continue
		}
		// Decided without signing: marked now. A write signed again is marked when it is stored, below.
		if containsID(inv[string(x.record)], x.entry.ID) { // carried over
			if err := d.store.SetValue(done, []byte{1}); err != nil {
				return err
			}
			continue
		}
		// Every write seen of the record counts, kept or since superseded. A claim about this
		// installation beyond what it issued is forged, unless the superseding write is this
		// installation's own later one.
		genuine := false
		for _, o := range all {
			if !bytes.Equal(o.record, x.record) || !tandem.Supersedes(o.entry, x.entry) {
				continue
			}
			ownLater := o.entry.W.Dot.Device == me.Installation && dotLess(x.entry.W.Dot, o.entry.W.Dot)
			if x.entry.W.Dot.Device != me.Installation || ownLater || !d.forged(o.entry.W, me) {
				genuine = true
			}
		}
		if genuine {
			if err := d.store.SetValue(done, []byte{1}); err != nil {
				return err
			}
			// Replaced by a write that plausibly saw it. If it was kept when the epoch changed,
			// the new state's claim can't be checked: keep it for the app.
			var same []tandem.Entry
			for _, o := range all {
				if bytes.Equal(o.record, x.record) && o.entry.Epoch == x.entry.Epoch {
					same = append(same, o.entry)
				}
			}
			old, err := merged(nil, d.pin.Hub, cid, x.record, same...)
			if err != nil {
				return err
			}
			if keptEntry(old, x.entry.ID) != nil {
				if err := d.store.SetValue("unshared."+hexs(cid)+"."+hexs(x.entry.ID), x.record); err != nil {
					return err
				}
			}
			continue
		}
		base := tandem.Join(x.entry.W.Base, x.entry.W.Context, tandem.VV{x.entry.W.Dot})
		ref := d.firstWriterStatement(c, current, me)
		if ref == nil || len(base) > tandem.MaxVector {
			// No statement allows it, or it can't be carried.
			if err := d.store.SetValue(done, []byte{1}); err != nil {
				return err
			}
			if err := d.store.SetValue("unshared."+hexs(cid)+"."+hexs(x.entry.ID), x.record); err != nil {
				return err
			}
			continue
		}
		o := x.entry.W
		w := &tandem.Write{Hub: d.pin.Hub, Certificate: me.Certificate, Authorization: me.Auth, Scope: cid, Record: x.record,
			Field: o.Field, Op: o.Op, Element: o.Element, Value: o.Value, Author: me.Participant, Dot: o.Dot, Ref: ref,
			Context: tandem.VV{}, Base: base}
		if err := w.Sign(me.Keys.Signing); err != nil {
			return err
		}
		touched[string(x.record)] = append(touched[string(x.record)], w)
		resigned = append(resigned, done)
	}
	s, ok := c.statements[string(current)]
	if !ok {
		return nil
	}
	for _, record := range sortedKeys(touched) {
		var es []tandem.Entry
		for _, w := range touched[record] {
			e, err := entryOf(w, current, s.s.KeyN)
			if err != nil {
				return err
			}
			es = append(es, e)
		}
		r, err := merged(c.records[record], d.pin.Hub, cid, []byte(record), es...)
		if err != nil {
			return err
		}
		c.records[record] = r
		err = d.store.Transaction(func() error {
			for _, e := range es {
				p, err := onePayload([]byte(record), e.W)
				if err != nil {
					return err
				}
				if _, err := d.store.PutWrite(e.ID, cid, []byte(record), current, p); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		d.changedItems(cid, []byte(record))
	}
	err = d.store.Transaction(func() error {
		for _, done := range resigned {
			if err := d.store.SetValue(done, []byte{1}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, record := range sortedKeys(touched) {
		if err := d.publish(ctx, cid, current, []byte(record), touched[record]); err != nil {
			return err
		}
	}
	return nil
}

// carryListed keeps every old write the current inventory lists, whoever wrote it, and
// publishes it into the new channel if that lacks it (§11.6 "Moving over", §12.2).
func (d *Device) carryListed(ctx context.Context, cid []byte) error {
	current, key, inv, ok := d.currentEpoch(cid)
	c := d.collections[string(cid)]
	if !ok {
		return nil
	}
	inNew, err := d.writesIn(tandem.CollectionTag(key, d.pin.Hub, current))
	if err != nil {
		return err
	}
	all, err := d.storedWrites(c)
	if err != nil {
		return err
	}
	missing := map[string][]*tandem.Write{}
	for _, x := range all {
		if x.entry.Epoch == string(current) || inNew[string(x.entry.ID)] {
			continue
		}
		if !containsID(inv[string(x.record)], x.entry.ID) || keptEntry(c.records[string(x.record)], x.entry.ID) == nil {
			continue
		}
		missing[string(x.record)] = append(missing[string(x.record)], x.entry.W)
	}
	for _, record := range sortedKeys(missing) {
		if err := d.publish(ctx, cid, current, []byte(record), missing[record]); err != nil {
			return err
		}
	}
	return nil
}
