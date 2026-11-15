package engine

import (
	"bytes"
	"context"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// ListCopy is what Copy did: the new list, who it was shared with again, and who is left for the
// user to decide on.
type ListCopy struct {
	// List is the new list, owned by this participant.
	List []byte
	// Shared is who it was shared with again, with the role they had: listed the same way in
	// every version of the old roster.
	Shared [][]byte
	// Undecided is who wasn't: in only some versions of the old roster, with different roles in
	// them, or without an admitted device now. The app asks the user, then calls Share.
	Undecided [][]byte
}

// tips is the ends of the counted roster: one for a live collection, two or more for a dead one (§9.4).
func (c *collection) tips() [][]byte {
	var out [][]byte
	for _, k := range sortedKeys(c.statements) {
		if c.counts([]byte(k)) && len(c.successors([]byte(k))) == 0 {
			out = append(out, []byte(k))
		}
	}
	return out
}

const copyOfField = "copyof"

// Copy copies a list into a new one this participant owns, and shares it again: the way out of a
// dead list (§9.4), or of one whose owner can't change its sharing any more (§11.8). The records
// go over as this device holds them, as writes of this participant's own; of concurrent values
// only the displayed one. The new list's _meta names the old one (copyof), so members see where
// it came from (CopyOf). Members listed the same way at every end of the old roster get the same
// role (an old owner becomes a writer); the rest are Undecided.
//
// Calling it again, here or on another device of this participant once it has synced, finishes
// the same copy rather than starting another. The old list stays as it is, read-only if dead.
func (d *Device) Copy(ctx context.Context, list []byte) (ListCopy, error) {
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return ListCopy{}, err
	}
	me, old := d.me, d.collections[string(list)]
	if me == nil || old == nil || old.genesis == nil || !d.following(list) {
		return ListCopy{}, fail("copy: not a list this participant follows")
	}
	// File manifests cannot be copied as opaque values across scopes: the
	// source's cleanup would destroy the destination's chunks. Until copying
	// can download and re-encrypt files, refuse before creating a partial list.
	for _, r := range old.records {
		if r.Deleted() {
			continue
		}
		for _, e := range r.Entries() {
			if e.W.Op == tandem.OpAssign && strings.HasPrefix(e.W.Field, BlobPrefix) {
				return ListCopy{}, fail("copy: copying lists with file attachments is not implemented")
			}
		}
	}
	mark := "copy." + hexs(list)
	var cp []byte
	if earlier, ok := d.store.Value(mark); ok && d.collections[string(earlier)] != nil {
		cp = earlier
	} else {
		for _, k := range sortedKeys(d.collections) {
			c := d.collections[k]
			if v, _ := value(c.records[string(metaRecord)], copyOfField); bytes.Equal(v, list) &&
				bytes.Equal(c.owner(), me.Participant) && d.following(c.id) {
				cp = c.id
				break
			}
		}
		if cp == nil {
			name, _ := value(old.records[string(metaRecord)], "name")
			var err error
			if cp, err = d.createList(ctx, string(name)); err != nil {
				return ListCopy{}, err
			}
		}
	}
	if err := d.store.SetValue(mark, cp); err != nil {
		return ListCopy{}, err
	}

	// _meta: the old one's app fields, and where this list came from.
	if v, _ := value(d.collections[string(cp)].records[string(metaRecord)], copyOfField); !bytes.Equal(v, list) {
		var changes []Change
		if m := old.records[string(metaRecord)]; m != nil {
			item := Item{ID: metaRecord, record: m}
			for _, f := range item.Fields() {
				if f != "name" && f != copyOfField {
					changes = append(changes, changesOf(item, f)...)
				}
			}
		}
		changes = append(changes, Change{field: copyOfField, op: tandem.OpAssign, val: list})
		if err := d.writeChanges(ctx, cp, metaRecord, changes); err != nil {
			return ListCopy{}, err
		}
	}
	// The records, under their own IDs, unless already there (an earlier call that stopped part way).
	for _, k := range sortedKeys(old.records) {
		r := old.records[k]
		if k == string(metaRecord) || k == string(rosterRecord) || r.Deleted() || d.collections[string(cp)].records[k] != nil {
			continue
		}
		item := Item{ID: []byte(k), record: r}
		var changes []Change
		for _, f := range item.Fields() {
			changes = append(changes, changesOf(item, f)...)
		}
		if len(changes) > 0 {
			if err := d.writeChanges(ctx, cp, []byte(k), changes); err != nil {
				return ListCopy{}, err
			}
		}
	}

	// The members every end of the old roster agrees on.
	var ends [][]Member
	everyone := map[string]bool{}
	for _, t := range old.tips() {
		ms := old.statements[string(t)].s.Members
		ends = append(ends, ms)
		for _, m := range ms {
			if !bytes.Equal(m.UID, me.Participant) {
				everyone[string(m.UID)] = true
			}
		}
	}
	out := ListCopy{List: cp}
	for _, uid := range sortedKeys(everyone) {
		var role Role
		var first *Member
		agreed := true
		for i, ms := range ends {
			var m *Member
			for j := range ms {
				if string(ms[j].UID) == uid {
					m = &ms[j]
				}
			}
			r := Role("")
			if m != nil {
				r = m.Role
				if r == Owner {
					r = Writer
				}
			}
			if i == 0 {
				role, first = r, m
			}
			agreed = agreed && m != nil && r == role
		}
		if !agreed {
			out.Undecided = append(out.Undecided, []byte(uid))
			continue
		}
		if err := d.share(ctx, cp, []byte(uid), role, first.Name); err != nil {
			out.Undecided = append(out.Undecided, []byte(uid)) // no admitted device left
			continue
		}
		out.Shared = append(out.Shared, []byte(uid))
	}
	return out, nil
}

// CopyOf is the list this one was copied from, if its owner was in that list: an app can say "Bob
// continued House" on the offer (§11.2). The claim is the copy owner's; the old list must be one
// this device knows.
func (d *Device) CopyOf(list []byte) []byte {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil || c.owner() == nil {
		return nil
	}
	from, ok := value(c.records[string(metaRecord)], copyOfField)
	old := d.collections[string(from)]
	if !ok || old == nil {
		return nil
	}
	for _, s := range old.statements {
		if s.s.member(c.owner()) != nil {
			return from
		}
	}
	return nil
}

// changesOf is the writes that recreate one field of an item: its displayed value, and its elements.
func changesOf(item Item, field string) []Change {
	var out []Change
	if v, ok := item.Value(field); ok {
		out = append(out, Change{field: field, op: tandem.OpAssign, val: v})
	}
	for _, e := range item.Elements(field) {
		out = append(out, Change{field: field, op: tandem.OpAdd, element: e})
	}
	return out
}
