package device

import (
	"bytes"
	"context"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Signing again (§11.6), with the two protections decided on 2026-11-12
// against a member's forged context (scenario gap G-26; review/ Fable and Sol):
//
//   - A write is signed again if nothing supersedes it, or if everything
//     that does relies on a vector entry for this device's ID beyond any dot
//     it has issued. Such an entry is forged (Fable).
//   - A late write the device held as kept, which the new state claims to
//     supersede, is not signed again but kept and reported to the app as
//     possibly unshared (Sol).
//
// Only this device's own writes are considered: its high-water mark is what
// tells a forged claim from a real one.

// Unshared lists this device's writes in a collection that a key change may
// have left behind: the app shows them for recovery or dismissal.
func (d *Device) Unshared(cid []byte) []*tandem.Write {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.cols[string(cid)]
	if !ok {
		return nil
	}
	var out []*tandem.Write
	for id := range c.unshared {
		if h, ok := c.own[id]; ok {
			out = append(out, h.W)
		}
	}
	sortWrites(out)
	return out
}

// forged reports whether w claims, for this device, a dot beyond any it has
// issued.
func (d *Device) forged(w *tandem.Write) bool {
	for _, v := range []tandem.VV{w.Context, w.Base} {
		for _, dot := range v {
			if dot.Device == d.id && (dot.Millis > d.hlc.Millis || dot.Millis == d.hlc.Millis && dot.Counter > d.hlc.Counter) {
				return true
			}
		}
	}
	return false
}

// firstWriterStatement is the first statement of an epoch that lets this
// user write: the ref of a write signed again.
func (d *Device) firstWriterStatement(c *col, epoch []byte) []byte {
	for _, w := range c.chain(c.roster.Head().ID) {
		id, _ := w.ID()
		if !bytes.Equal(c.roster.EpochOf(id), epoch) {
			continue
		}
		if s, ok := c.roster.Get(id); ok && s.Body != nil {
			if r := s.Body.Role(d.u.UID); r == "owner" || r == "writer" {
				return id
			}
		}
	}
	return nil
}

func (d *Device) resign(ctx context.Context, c *col) error {
	if c.roster == nil || c.roster.Dead() {
		return nil
	}
	cur := c.roster.EpochOf(c.roster.Head().ID)
	k := d.key(c.id, cur)
	s, ok := c.roster.Get(cur)
	if k == nil || !ok || s.Body == nil {
		return nil
	}
	inv := c.invs[string(cur)]
	if s.Body.Snapshot != nil && inv == nil {
		return nil // the new epoch's inventory isn't here yet
	}
	var touched [][]byte
	for id, h := range c.own {
		src := c.roster.EpochOf(h.W.Ref)
		if c.resigned[id] || src == nil || bytes.Equal(src, cur) {
			continue
		}
		for _, x := range inv[string(h.W.Record)] {
			if string(x) == id {
				c.resigned[id] = true // carried over as it is
			}
		}
		if c.resigned[id] {
			continue
		}
		x, err := tandem.NewEntry(h.W, src, d.epochN(c, src))
		if err != nil {
			continue
		}
		genuine := false
		for _, recs := range []map[string]*tandem.Record{c.recs[string(cur)], c.recs[string(src)]} {
			if r, ok := recs[string(h.W.Record)]; ok {
				for _, w := range r.Kept {
					if tandem.Supersedes(w, x) && !d.forged(w.W) {
						genuine = true
					}
				}
			}
		}
		c.resigned[id] = true
		if genuine {
			// Replaced by a write that plausibly saw it. If this device still
			// held it as kept when the epoch changed, the new state's claim
			// can't be checked: keep it for the app (Sol).
			if r, ok := c.recs[string(src)][string(h.W.Record)]; ok {
				if _, kept := r.Kept[id]; kept {
					c.unshared[id] = true
				}
			}
			continue
		}
		ref := d.firstWriterStatement(c, cur)
		base := tandem.Join(h.W.Base, tandem.Join(h.W.Context, tandem.VV{h.W.Dot}))
		if ref == nil || len(base) > tandem.MaxVector {
			c.unshared[id] = true // no statement allows it, or it can't be carried: the app keeps it
			continue
		}
		nw := &tandem.Write{Scope: c.id, Record: h.W.Record, Field: h.W.Field, Op: h.W.Op, Element: h.W.Element,
			Value: h.W.Value, Dot: h.W.Dot, Ref: ref, Context: tandem.VV{}, Base: base}
		if err := nw.Sign(d.u); err != nil {
			return err
		}
		nid, _ := nw.ID()
		c.own[string(nid)] = heldWrite{nw, cur} // a later key change may need it signed again
		d.mergeData(c, cur, nw)
		touched = append(touched, h.W.Record)
	}
	if len(touched) > 0 {
		if err := d.save(); err != nil {
			return err
		}
	}
	for _, rec := range touched {
		if err := d.publishRecord(ctx, c, cur, k, rec); err != nil {
			return err
		}
	}
	return nil
}
