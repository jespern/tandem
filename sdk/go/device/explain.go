package device

import (
	"fmt"
	"strings"
)

// Explain describes what this device holds of one record: kept writes per
// epoch, held writes, inventory entries and queued edits. For diagnosing
// disagreements in tests.
func (d *Device) Explain(cid, rec []byte) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.cols[string(cid)]
	if !ok || c.roster == nil {
		return "collection not followed"
	}
	var b strings.Builder
	cur := c.roster.EpochOf(c.roster.Head().ID)
	fmt.Fprintf(&b, "current epoch %x", cur[:4])
	for ep, recs := range c.recs {
		if r := recs[string(rec)]; r != nil {
			fmt.Fprintf(&b, "\n   epoch %x kept:", []byte(ep)[:4])
			for id, e := range r.Kept {
				fmt.Fprintf(&b, " %s=%q(%x)", e.W.Field, e.W.Value, []byte(id)[:4])
			}
		}
	}
	for id, h := range c.all {
		if string(h.W.Record) == string(rec) {
			fmt.Fprintf(&b, "\n   has %x %s=%q via channel %x, source %x", []byte(id)[:4], h.W.Field, h.W.Value, h.Epoch[:4], c.roster.EpochOf(h.W.Ref)[:min(4, len(c.roster.EpochOf(h.W.Ref)))])
		}
	}
	for _, h := range c.held {
		if string(h.W.Record) == string(rec) {
			fmt.Fprintf(&b, "\n   held %s=%q in %x", h.W.Field, h.W.Value, h.Epoch[:4])
		}
	}
	if inv := c.invs[string(cur)]; inv != nil {
		fmt.Fprintf(&b, "\n   inventory lists %d writes of it", len(inv[string(rec)]))
	}
	for key := range c.dirty {
		if strings.HasSuffix(key, string(rec)) {
			fmt.Fprintf(&b, "\n   queued in %x", []byte(key[:len(key)-16])[:4])
		}
	}
	return b.String()
}
