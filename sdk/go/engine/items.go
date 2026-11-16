package engine

import (
	"context"
	"slices"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// FieldValue is one value of a field as merged (§9.2): the bytes, who wrote them and when
// their device says it did.
type FieldValue struct {
	Value []byte
	// Author is the participant who wrote it.
	Author []byte
	// Millis is the writing device's clock, Unix milliseconds: a claim, not an order.
	Millis uint64
	// Write is the write's ID, stable across devices.
	Write []byte
}

// Item is a record of a list as this device holds it: an immutable snapshot (§9).
type Item struct {
	ID            []byte
	record        *tandem.Record
	markedDeleted bool
}

// Deleted reports a tombstone or an inventory deletion mark (§9.2, §8.3).
func (i Item) Deleted() bool { return i.markedDeleted || i.record.Deleted() }

// Value is the displayed value of a field: of concurrent values, the one §9.2 orders first.
func (i Item) Value(field string) ([]byte, bool) { return i.record.Value(field) }

// Text is the displayed value as UTF-8 text, "" if unset.
func (i Item) Text(field string) string {
	v, _ := i.record.Value(field)
	return string(v)
}

// Values is every value of a field that no other replaced, displayed one first, at most 16
// (§9.2). More than one is a conflict the app may show; writing the field again settles it.
func (i Item) Values(field string) []FieldValue {
	var out []FieldValue
	for _, e := range i.record.Siblings(field) {
		if len(out) == 16 {
			break
		}
		out = append(out, FieldValue{Value: e.W.Value, Author: e.W.Author, Millis: e.W.Dot.Millis, Write: e.ID})
	}
	return out
}

// Elements is the elements of a set field (§9.2, add wins).
func (i Item) Elements(field string) [][]byte { return i.record.Elements(field) }

// Fields is the names of the fields with a value or an element, app fields only (no _ names).
func (i Item) Fields() []string {
	seen := map[string]bool{}
	for _, e := range i.record.Entries() {
		if (e.W.Op == tandem.OpAssign || e.W.Op == tandem.OpAdd) && !strings.HasPrefix(e.W.Field, "_") {
			seen[e.W.Field] = true
		}
	}
	return sortedKeys(seen)
}

func dotLess(a, b tandem.Dot) bool {
	return a.Millis < b.Millis || a.Millis == b.Millis && a.Counter < b.Counter
}

// CreatedBy is who first wrote this record, as far as the writes kept show: for "Alice
// added". A claim by the writers' clocks.
func (i Item) CreatedBy() []byte {
	es := i.record.Entries()
	if len(es) == 0 {
		return nil
	}
	return slices.MinFunc(es, func(a, b tandem.Entry) int { return cmpDot(a.W.Dot, b.W.Dot) }).W.Author
}

// LastModifiedBy is who wrote last, as far as the writes kept show.
func (i Item) LastModifiedBy() []byte {
	es := i.record.Entries()
	if len(es) == 0 {
		return nil
	}
	return slices.MaxFunc(es, func(a, b tandem.Entry) int { return cmpDot(a.W.Dot, b.W.Dot) }).W.Author
}

func cmpDot(a, b tandem.Dot) int {
	switch {
	case dotLess(a, b):
		return -1
	case dotLess(b, a):
		return 1
	}
	return 0
}

// Item is one record of a list, including tombstones and inventory deletion marks;
// false means the record is unknown. A mark carries no old fields or attribution.
func (d *Device) Item(list, id []byte) (Item, bool) {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return Item{}, false
	}
	if d.recordMarked(c, id) {
		r, err := tandem.NewRecord(d.pin.Hub, list, id)
		if err != nil {
			return Item{}, false
		}
		return Item{ID: id, record: r, markedDeleted: true}, true
	}
	if c.records[string(id)] == nil {
		return Item{}, false
	}
	return Item{ID: id, record: c.records[string(id)]}, true
}

// recordMarked uses the newest assembled inventory on the roster line. Until
// a new inventory arrives, the previous epoch's state remains readable.
// The caller holds the device state lock.
func (d *Device) recordMarked(c *collection, id []byte) bool {
	for _, ref := range slices.Backward(chain(c, c.head())) {
		if inv, known := d.inventories[string(c.epochOf(ref))]; known {
			return inv.Marks(id)
		}
	}
	return false
}

// Items is the list's records that aren't deleted, without the named ones, by ID.
func (d *Device) Items(list []byte) []Item {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return nil
	}
	var out []Item
	for _, k := range sortedKeys(c.records) {
		if k != string(metaRecord) && !c.records[k].Deleted() {
			out = append(out, Item{ID: []byte(k), record: c.records[k]})
		}
	}
	return out
}

// Add adds an element to a set field, such as a tag (§9.2).
func (d *Device) Add(ctx context.Context, list, record []byte, field string, element []byte) error {
	defer d.begin()()
	if element == nil {
		element = []byte{}
	}
	return d.writeRecord(ctx, list, record, field, tandem.OpAdd, nil, element)
}

// Remove removes an element from a set field. A concurrent add on another device wins (§9.2).
func (d *Device) Remove(ctx context.Context, list, record []byte, field string, element []byte) error {
	defer d.begin()()
	if element == nil {
		element = []byte{}
	}
	return d.writeRecord(ctx, list, record, field, tandem.OpRemove, nil, element)
}

// Name is the list's name, from its _meta record (§7.3).
func (d *Device) Name(list []byte) string {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return ""
	}
	v, _ := value(c.records[string(metaRecord)], "name")
	return string(v)
}

// Rename renames a list; only its owner may (§8.1).
func (d *Device) Rename(ctx context.Context, list []byte, name string) error {
	defer d.begin()()
	return d.writeRecord(ctx, list, metaRecord, "name", tandem.OpAssign, []byte(name), nil)
}

// Owner is the list's owner, from its genesis.
func (d *Device) Owner(list []byte) []byte {
	defer d.read()()
	if c := d.collections[string(list)]; c != nil {
		return c.owner()
	}
	return nil
}

// Members is the members the roster lists now, with their roles and display names.
func (d *Device) Members(list []byte) []Member {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return nil
	}
	s, _ := c.headStatement()
	return slices.Clone(s.Members)
}
