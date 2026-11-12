package device

import (
	"context"
	"slices"

	tandem "github.com/jespern/tandem/sdk/go"
)

// RecordView is what an app would show of one record.
type RecordView struct {
	Deleted bool
	Fields  map[string]string   // displayed value per assigned field
	Sets    map[string][]string // present elements per set field
}

// State is every record of a collection's current epoch, by record ID, as
// this device would show it.
func (d *Device) State(cid []byte) map[string]RecordView {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, _, ep, err := d.current(cid)
	if err != nil {
		return nil
	}
	out := map[string]RecordView{}
	for id, r := range c.recs[string(ep)] {
		v := RecordView{Deleted: r.Deleted(), Fields: map[string]string{}, Sets: map[string][]string{}}
		for _, e := range r.Kept {
			switch e.W.Op {
			case tandem.OpAssign:
				if val, ok := r.Value(e.W.Field); ok {
					v.Fields[e.W.Field] = string(val)
				}
			case tandem.OpAdd, tandem.OpRemove:
				var els []string
				for _, el := range r.Elements(e.W.Field) {
					els = append(els, string(el))
				}
				slices.Sort(els)
				if len(els) > 0 {
					v.Sets[e.W.Field] = els
				}
			}
		}
		out[id] = v
	}
	return out
}

// Tag adds or removes a set element (§9.2).
func (d *Device) Tag(ctx context.Context, cid, rec []byte, field, element string, add bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	op := tandem.OpRemove
	if add {
		op = tandem.OpAdd
	}
	return d.setOp(ctx, cid, rec, field, []byte(element), op)
}

// StatementView is a roster statement as the device holds it.
type StatementView struct {
	ID, Parent, Signer []byte
	Body               *tandem.Body
	Announced          bool // false only for an epoch-starting draft
}

// Roster exports the genesis ID and every statement the device holds.
func (d *Device) Roster(cid []byte) (genesis []byte, out []StatementView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.cols[string(cid)]
	if !ok || c.roster == nil {
		return nil, nil
	}
	announced := map[string]bool{}
	for _, id := range c.roster.Announced() {
		announced[string(id)] = true
	}
	for id, w := range c.stmts {
		s, ok := c.roster.Get([]byte(id))
		if !ok || s.Body == nil {
			continue
		}
		v := StatementView{ID: []byte(id), Signer: w.Author, Body: s.Body, Announced: true}
		if !bytesEqual(w.Ref, make([]byte, 32)) {
			v.Parent = w.Ref
			if p, ok := c.roster.Get(w.Ref); ok && p.Body != nil && !bytesEqual(p.Body.Commit, s.Body.Commit) {
				v.Announced = announced[id]
			}
		}
		out = append(out, v)
	}
	return c.roster.Genesis, out
}

// Inventory is the verified inventory of the collection's current epoch,
// with that epoch's ID; nil for the genesis epoch.
func (d *Device) Inventory(cid []byte) (epoch []byte, inv tandem.Inventory) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, _, ep, err := d.current(cid)
	if err != nil {
		return nil, nil
	}
	return ep, c.invs[string(ep)]
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }
