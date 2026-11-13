package engine

import (
	"bytes"
	"context"
	"sort"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Keeping the hub small and whole (§10.7, §12.2): after a sync, in each current channel this
// device follows, destroy messages another message dominates, and publish again a record whose
// messages on the hub, taken together, no longer cover this device's state. Only messages this
// device knows count; the roster and inventory fragments are never collected; nothing happens
// in a channel a later epoch replaced.

// hygiene is one pass over every current channel, after each sync.
func (d *Device) hygiene(ctx context.Context) error {
	me := d.me
	if me == nil || !d.registry.Activity(me.Certificate).IsValid() {
		return nil
	}
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		cid := c.id
		if !d.following(cid) || c.dead() {
			continue
		}
		head := c.head()
		epoch := c.epochOf(head)
		key := c.keys[string(epoch)]
		s, ok := c.statements[string(head)]
		_, hasInv := d.inventories[string(epoch)]
		if epoch == nil || key == nil || !ok || s.s.Deleted || (!bytes.Equal(epoch, c.genesis) && !hasInv) {
			continue
		}
		tag := tandem.CollectionTag(key, d.pin.Hub, epoch)
		if err := d.healRoster(ctx, cid, c, head, epoch, tag); err != nil {
			return err
		}
		if !bytes.Equal(epoch, c.genesis) {
			if err := d.healFragments(ctx, tag, epoch, key); err != nil {
				return err
			}
		}
		if err := d.healAnnouncements(ctx, cid, c); err != nil {
			return err
		}
		place := func(w *tandem.Write) *tandem.Entry {
			s, ok := c.statements[string(w.Ref)]
			source := c.epochOf(w.Ref)
			if !ok || source == nil {
				return nil
			}
			e, err := entryOf(w, source, s.s.KeyN)
			if err != nil {
				return nil
			}
			return &e
		}
		republish := func(record []byte, writes []*tandem.Write) error { return d.publish(ctx, cid, epoch, record, writes) }
		if err := d.tidy(ctx, tag, cid, c.records, place, republish); err != nil {
			return err
		}
	}
	if err := d.healGrants(ctx); err != nil {
		return err
	}
	if err := d.finishPendingGrants(ctx); err != nil {
		return err
	}
	for _, epoch := range d.homeHeads() {
		key, scope := d.homeKeys[string(epoch)], d.homeScope()
		if key == nil || scope == nil {
			continue
		}
		tag := tandem.HomeTag(key, d.pin.Hub, me.Participant, epoch)
		if len(fset(d.homeEpochs[string(epoch)], "parents")) > 0 {
			if err := d.healFragments(ctx, tag, epoch, key); err != nil {
				return err
			}
		}
		epochs := d.homeEpochs
		place := func(w *tandem.Write) *tandem.Entry {
			e, err := entryOf(w, w.Ref, epochNumber(w.Ref, epochs))
			if err != nil {
				return nil
			}
			return &e
		}
		republish := func(record []byte, writes []*tandem.Write) error { return d.publishHome(ctx, record, epoch, writes) }
		if err := d.tidy(ctx, tag, scope, d.home, place, republish); err != nil {
			return err
		}
	}
	return nil
}

// healGrants restores the last grant this device sent to each keychain of each current member
// if the hub lost it; one to someone no longer listed, or to a revoked keychain, is destroyed
// and forgotten (§11.4).
func (d *Device) healGrants(ctx context.Context) error {
	const prefix = "kept.grant."
	vs, err := d.store.Values(prefix)
	if err != nil {
		return err
	}
	for _, v := range vs {
		parts := strings.Split(strings.TrimPrefix(v.Name, prefix), ".")
		if len(parts) != 3 {
			continue
		}
		cid, ok1 := unhexs(parts[0])
		participant, ok2 := unhexs(parts[1])
		cert, ok3 := unhexs(parts[2])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		listed := false
		if c := d.collections[string(cid)]; c != nil {
			if s, ok := c.headStatement(); ok {
				listed = s.member(participant) != nil && !s.Deleted
			}
		}
		on, err := d.store.MessagesWithHash(tandem.H(v.Value))
		if err != nil {
			return err
		}
		if !listed || !d.registry.Activity(cert).IsValid() {
			if len(on) > 0 {
				if err := d.hubDestroy(ctx, on); err != nil {
					return err
				}
				if err := d.store.ForgetMessages(on); err != nil {
					return err
				}
			}
			if err := d.store.RemoveValue(v.Name); err != nil {
				return err
			}
			continue
		}
		if len(on) > 0 {
			continue
		}
		if _, err := d.appendNoted(ctx, tandem.InboxTag(participant, d.pin.Hub), v.Value); err != nil {
			return err
		}
	}
	return nil
}

// healRoster keeps the roster whole in the current channel (§12.2): every statement of the
// line from the genesis to the head must be in some message there, or the line is published
// again, so a new member's device can check it.
func (d *Device) healRoster(ctx context.Context, cid []byte, c *collection, head, epoch []byte, tag string) error {
	present := map[string]bool{}
	ms, err := d.store.Merged(tag)
	if err != nil {
		return err
	}
	for _, m := range ms {
		cs, err := d.store.Contents(m.ID)
		if err != nil {
			return err
		}
		for _, ct := range cs {
			if bytes.Equal(ct.Record, rosterRecord) {
				present[string(ct.Write)] = true
			}
		}
	}
	line := chain(c, head)
	whole := true
	for _, id := range line {
		whole = whole && present[string(id)]
	}
	if whole {
		return nil
	}
	var writes []*tandem.Write
	for _, id := range line {
		writes = append(writes, c.statements[string(id)].w)
	}
	return d.publish(ctx, cid, epoch, rosterRecord, writes)
}

// healFragments keeps an epoch's inventory on the hub while the epoch is current (§10.7,
// §12.2): published again if no fragment message is left in its channel.
func (d *Device) healFragments(ctx context.Context, tag string, epoch, key []byte) error {
	inv, ok := d.inventories[string(epoch)]
	if !ok {
		return nil
	}
	ms, err := d.store.Messages(tag)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if _, ok := d.store.Value("fragmsg." + m); ok {
			return nil
		}
	}
	frags, err := fragments(inv, epoch)
	if err != nil {
		return err
	}
	for _, f := range frags {
		env, err := tandem.Seal(key, d.pin.Hub, tandem.KindRecord, tag, f)
		if err != nil {
			return err
		}
		id, err := d.appendNoted(ctx, tag, env)
		if err != nil {
			return err
		}
		if err := d.store.SetValue("fragmsg."+id, epoch); err != nil {
			return err
		}
	}
	return nil
}

// healAnnouncements: every earlier epoch's channel keeps the announcement of the epoch after it
// (§11.6 step 6, §12.2): it is how a device that was away finds out where the list went.
func (d *Device) healAnnouncements(ctx context.Context, cid []byte, c *collection) error {
	for _, k := range sortedKeys(c.announced) {
		start := []byte(k)
		if !c.startsEpoch(start) {
			continue
		}
		s := c.statements[k]
		old := c.epochOf(s.w.Ref)
		key := c.keys[string(old)]
		if old == nil || key == nil {
			continue
		}
		tag := tandem.CollectionTag(key, d.pin.Hub, old)
		ms, err := d.store.Merged(tag)
		if err != nil {
			return err
		}
		present := false
		for _, m := range ms {
			cs, err := d.store.Contents(m.ID)
			if err != nil {
				return err
			}
			for _, ct := range cs {
				present = present || bytes.Equal(ct.Write, start)
			}
		}
		if !present {
			if err := d.publish(ctx, cid, old, rosterRecord, []*tandem.Write{s.w}); err != nil {
				return err
			}
		}
	}
	return nil
}

type hubMessage struct {
	id     string
	hash   []byte
	writes map[string]bool
}

// tidy collects and heals one channel. records is this device's state of the scope; place
// places a stored write.
func (d *Device) tidy(ctx context.Context, tag string, scope []byte, records map[string]*tandem.Record,
	place func(*tandem.Write) *tandem.Entry, republish func([]byte, []*tandem.Write) error) error {
	byRecord := map[string][]hubMessage{}
	ms, err := d.store.Merged(tag)
	if err != nil {
		return err
	}
	for _, m := range ms {
		cs, err := d.store.Contents(m.ID)
		if err != nil {
			return err
		}
		if len(cs) == 0 || bytes.Equal(cs[0].Record, rosterRecord) {
			continue // roster and fragments stay
		}
		h := hubMessage{id: m.ID, hash: m.Hash, writes: map[string]bool{}}
		for _, ct := range cs {
			h.writes[string(ct.Write)] = true
		}
		byRecord[string(cs[0].Record)] = append(byRecord[string(cs[0].Record)], h)
	}
	// A record with no message left at all is healed too.
	for rid, r := range records {
		if rid != string(rosterRecord) && byRecord[rid] == nil && len(r.Entries()) > 0 {
			byRecord[rid] = []hubMessage{}
		}
	}
	var doomed []string
	for _, rid := range sortedKeys(byRecord) {
		messages := byRecord[rid]
		// Every write the messages name, as this device placed it when it merged it.
		entries := map[string]tandem.Entry{}
		rows, err := d.store.Writes(scope, []byte(rid))
		if err != nil {
			return err
		}
		for _, row := range rows {
			w := storedWrite(row.Payload, d.pin.Hub, scope)
			if w == nil {
				continue
			}
			if e := place(w); e != nil {
				entries[string(e.ID)] = *e
			}
		}
		// covers: write x (by ID) is in n, or one of n's writes supersedes it.
		covers := func(n map[string]bool, x string) bool {
			if n[x] {
				return true
			}
			xe, ok := entries[x]
			if !ok {
				return false
			}
			for w := range n {
				if we, ok := entries[w]; ok && tandem.Supersedes(we, xe) {
					return true
				}
			}
			return false
		}
		dominated := func(m, n map[string]bool) bool {
			for w := range m {
				if !covers(n, w) {
					return false
				}
			}
			return true
		}
		gone := map[string]bool{}
		if len(messages) > 1 {
			for _, m := range messages {
				for _, n := range messages {
					if n.id == m.id || gone[n.id] || !dominated(m.writes, n.writes) {
						continue
					}
					// Of two that dominate each other, only the one with the greater envelope hash goes (§10.7).
					if dominated(n.writes, m.writes) && bytes.Compare(n.hash, m.hash) >= 0 {
						continue
					}
					gone[m.id] = true
					break
				}
			}
		}
		goneIDs := sortedKeys(gone)
		doomed = append(doomed, goneIDs...)
		// Healing (§12.2): the messages left, taken together, must cover this device's state of the record.
		state := records[rid]
		if state == nil {
			continue
		}
		left := map[string]bool{}
		var leftMessages []hubMessage
		for _, m := range messages {
			if gone[m.id] {
				continue
			}
			leftMessages = append(leftMessages, m)
			for w := range m.writes {
				left[w] = true
			}
		}
		kept := state.Entries()
		coveredBy := func(n map[string]bool) bool {
			for _, k := range kept {
				if !n[string(k.ID)] && !coversEntry(n, entries, k) {
					return false
				}
			}
			return true
		}
		single := false
		for _, m := range leftMessages {
			single = single || coveredBy(m.writes)
		}
		// Concurrent publishers (§10.7): when no single message left holds this device's state,
		// publish the merged state, which then dominates the others.
		if !coveredBy(left) || (!single && len(leftMessages) > 1) {
			if err := republish([]byte(rid), keptWrites(state)); err != nil {
				return err
			}
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	sort.Strings(doomed)
	if err := d.hubDestroy(ctx, doomed); err != nil {
		return err
	}
	return d.store.ForgetMessages(doomed)
}

// coversEntry: one of the writes in n supersedes k.
func coversEntry(n map[string]bool, entries map[string]tandem.Entry, k tandem.Entry) bool {
	for w := range n {
		if we, ok := entries[w]; ok && tandem.Supersedes(we, k) {
			return true
		}
	}
	return false
}
