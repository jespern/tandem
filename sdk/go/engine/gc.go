package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Garbage collection beyond §10.7, after each sync, in what the specification already allows:
//
//   - Writes of past epochs (§9.3): once the current epoch's inventory is here, a write of an
//     earlier epoch that the inventory doesn't list is out of the state for good and is dropped,
//     once this participant's late writes are settled (§11.6) and unless the app was told of it
//     as unshared.
//   - Lists that are gone (§11.6, §11.9): a list the owner deleted, or this participant was
//     removed from, keeps its replica read-only for the retention period, on this device's own
//     clock from when it first saw that. Then the replica goes, all but the roster, so that the
//     device still knows what happened. A device of the owner of a deleted list also destroys
//     the list's data on the hub, all but the roster, and its file chunks (DESIGN.md §17.7).

// DefaultRetention is how long a gone list's replica stays read-only, and a deleted list's data on
// the hub, before it is collected (§11.9).
const DefaultRetention = 30 * 24 * time.Hour

// SetRetention changes how long a gone list stays before it is collected, from now on. Zero takes
// the default; a negative value keeps gone lists for good.
func (d *Device) SetRetention(r time.Duration) {
	defer d.begin()()
	if r == 0 {
		r = DefaultRetention
	}
	d.retention = r
}

// collectGarbage is the pass after hygiene.
func (d *Device) collectGarbage(ctx context.Context) error {
	me := d.me
	if me == nil {
		return nil
	}
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		if err := d.dropPastWrites(c, me); err != nil {
			return err
		}
		if err := d.collectGone(ctx, c, me); err != nil {
			return err
		}
	}
	return nil
}

// dropPastWrites drops the writes of earlier epochs the current inventory doesn't list (§9.3).
func (d *Device) dropPastWrites(c *collection, me *Me) error {
	current, _, inv, ok := d.currentEpoch(c.id)
	if !ok || d.purged(c.id) {
		return nil
	}
	all, err := d.storedWrites(c)
	if err != nil {
		return err
	}
	var drop [][]byte
	var markers []string
	listed := map[string][]byte{}
	for _, x := range all {
		if x.entry.Epoch == string(current) || containsID(inv[string(x.record)], x.entry.ID) {
			continue
		}
		// Shown to the app as possibly unshared: kept until it is dismissed.
		if _, ok := d.store.Value("unshared." + hexs(c.id) + "." + hexs(x.entry.ID)); ok {
			continue
		}
		// This installation's own: only once it is settled (signed again, carried or replaced).
		done := "resigned." + hexs(x.entry.ID)
		own := bytes.Equal(x.entry.W.Certificate, me.Certificate) && x.entry.W.Dot.Device == me.Installation
		if _, settled := d.store.Value(done); own && !settled {
			continue
		}
		drop = append(drop, x.entry.ID)
		markers = append(markers, done)
		// A manifest's chunks stay accountable after its write goes, as a dropped upload's do,
		// until none is in the chunk index (DESIGN.md §17.7, condition 2).
		if d.blobs != nil && x.entry.W.Op == tandem.OpAssign && strings.HasPrefix(x.entry.W.Field, BlobPrefix) {
			if m, kind := tandem.ParseManifest(x.entry.W.Value); kind == tandem.ManifestV1 && !m.Inline() {
				p, err := onePayload(x.record, x.entry.W)
				if err != nil {
					return err
				}
				listed["blobs.dropped."+hexs(c.id)+"."+hexs(x.entry.ID)] = p
			}
		}
	}
	if len(drop) == 0 {
		return nil
	}
	return d.store.Transaction(func() error {
		for _, name := range sortedKeys(listed) {
			if err := d.store.SetValue(name, listed[name]); err != nil {
				return err
			}
		}
		if err := d.store.DropWrites(drop); err != nil {
			return err
		}
		for _, m := range markers {
			if err := d.store.RemoveValue(m); err != nil {
				return err
			}
		}
		return nil
	})
}

// gone reports whether a list is deleted, or this participant was removed from it after
// accepting it (RemovedLists).
func (d *Device) gone(c *collection, me *Me) (deleted, removed bool) {
	s, ok := c.headStatement()
	if !ok {
		return false, false
	}
	if s.Deleted {
		return true, false
	}
	return false, d.removedFrom(c, me)
}

// purged reports whether a gone list's replica was collected here.
func (d *Device) purged(cid []byte) bool {
	_, ok := d.store.Value("gone.purged." + hexs(cid))
	return ok
}

// collectGone collects a gone list once the retention period has passed since this device first
// saw it gone.
func (d *Device) collectGone(ctx context.Context, c *collection, me *Me) error {
	deleted, removed := d.gone(c, me)
	seenName, purgedName := "gone.seen."+hexs(c.id), "gone.purged."+hexs(c.id)
	if !deleted && !removed {
		// Back in the list (added again): nothing of this is collected.
		for _, n := range []string{seenName, purgedName} {
			if err := d.store.RemoveValue(n); err != nil {
				return err
			}
		}
		return nil
	}
	now := time.Now().UnixMilli()
	seen, ok := d.store.Value(seenName)
	if !ok || len(seen) != 8 {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(now))
		if err := d.store.SetValue(seenName, b[:]); err != nil {
			return err
		}
		seen = b[:]
	}
	if d.retention < 0 || now-int64(binary.BigEndian.Uint64(seen)) < d.retention.Milliseconds() {
		return nil
	}
	if !d.purged(c.id) {
		if err := d.purgeReplica(c); err != nil {
			return err
		}
	}
	if deleted && bytes.Equal(c.owner(), me.Participant) && d.registry.Activity(me.Certificate).IsValid() {
		return d.purgeHub(ctx, c)
	}
	return nil
}

// purgeReplica drops a gone list's records, everything but its roster, and what was kept for them.
func (d *Device) purgeReplica(c *collection) error {
	rows, err := d.store.WriteIDs(c.id)
	if err != nil {
		return err
	}
	var drop [][]byte
	var markers []string
	for _, row := range rows {
		if !bytes.Equal(row.Record, rosterRecord) {
			drop = append(drop, row.ID)
			markers = append(markers, "resigned."+hexs(row.ID))
		}
	}
	unshared, err := d.store.Values("unshared." + hexs(c.id) + ".")
	if err != nil {
		return err
	}
	for _, v := range unshared {
		markers = append(markers, v.Name)
	}
	markers = append(markers, "blobs.keep."+hexs(c.id))
	ups, err := d.store.Uploads()
	if err != nil {
		return err
	}
	var changed [][]byte
	for k := range c.records {
		changed = append(changed, []byte(k))
	}
	err = d.store.Transaction(func() error {
		if err := d.store.DropWrites(drop); err != nil {
			return err
		}
		for _, m := range markers {
			if err := d.store.RemoveValue(m); err != nil {
				return err
			}
		}
		for _, u := range ups {
			if bytes.Equal(u.Scope, c.id) {
				d.dropUpload(u)
			}
		}
		return d.store.SetValue("gone.purged."+hexs(c.id), []byte{1})
	})
	if err != nil {
		return err
	}
	c.records = map[string]*tandem.Record{}
	d.changedItems(c.id, changed...)
	return nil
}

// purgeHub destroys a deleted list's data on the hub (§11.9): every message this device merged in
// any of its channels that carries no roster write, and its inventory fragments. The roster stays,
// so that a device returning late learns the list was deleted. A message this device hasn't merged
// stays (§10.7). The list's file chunks go in the blob pass (blobgc.go).
func (d *Device) purgeHub(ctx context.Context, c *collection) error {
	var doomed []string
	for _, tag := range d.listTags(c) {
		ids, err := d.store.Messages(tag)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, frag := d.store.Value("fragmsg." + id); frag {
				doomed = append(doomed, id)
				continue
			}
			contents, err := d.store.Contents(id)
			if err != nil {
				return err
			}
			data := len(contents) > 0
			for _, ct := range contents {
				if bytes.Equal(ct.Record, rosterRecord) {
					data = false
				}
			}
			if data {
				doomed = append(doomed, id)
			}
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	return d.hubDestroy(ctx, doomed)
}

// listTags is the tag of every channel of a list this device knows, any epoch.
func (d *Device) listTags(c *collection) []string {
	seen := map[string]bool{}
	var out []string
	add := func(epoch string, key []byte) {
		t := tandem.CollectionTag(key, d.pin.Hub, []byte(epoch))
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, e := range sortedKeys(c.keys) {
		add(e, c.keys[e])
	}
	for _, e := range sortedKeys(c.readKeys) {
		for _, k := range sortedKeys(c.readKeys[e]) {
			add(e, c.readKeys[e][k])
		}
	}
	return out
}

// GoneLists is the lists whose replicas were collected here; the app shows none of them.
func (d *Device) GoneLists() [][]byte {
	defer d.read()()
	vs, _ := d.store.Values("gone.purged.")
	var out [][]byte
	for _, v := range vs {
		if id, ok := unhexs(strings.TrimPrefix(v.Name, "gone.purged.")); ok {
			out = append(out, id)
		}
	}
	return out
}
