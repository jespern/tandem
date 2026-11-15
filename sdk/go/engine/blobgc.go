package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Collecting and healing chunks (DESIGN.md §17.7 and §17.8). Both decide from one view of each
// list: which manifests are live (healed) and which are protected (never collected), which
// addresses any write this device holds has listed, and whether anything stops clean-up there.

type blobScope struct {
	cid       []byte
	tags      map[string]bool // every channel of the list this device knows, any epoch
	current   string          // the current channel; "" in a deleted or dead list
	collect   bool            // nothing stops clean-up in this list
	orphans   bool            // and orphans may go too
	purge     bool            // a deleted list this owner's device collected: every chunk goes (§11.9)
	protected map[string]bool // addresses of protected manifests
	listed    map[string]bool // addresses a write this device holds has listed
	live      []liveBlob
}

type liveBlob struct {
	id []byte
	m  *tandem.Manifest
}

// blobScopes reads the view; the caller holds the state lock.
func (d *Device) blobScopes() ([]*blobScope, error) {
	held, err := d.store.Held()
	if err != nil {
		return nil, err
	}
	heldIn := map[string]bool{}
	for _, m := range held {
		heldIn[m.Channel] = true
	}
	ups, err := d.store.Uploads()
	if err != nil {
		return nil, err
	}
	dropped, err := d.store.Values("blobs.dropped.")
	if err != nil {
		return nil, err
	}
	var out []*blobScope
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		s := &blobScope{cid: c.id, tags: map[string]bool{}, protected: map[string]bool{}, listed: map[string]bool{}}
		for epoch, key := range c.keys {
			s.tags[tandem.CollectionTag(key, d.pin.Hub, []byte(epoch))] = true
		}
		head := c.head()
		st, ok := c.headStatement()
		epoch := c.epochOf(head)
		key := c.keys[string(epoch)]
		if ok && !st.Deleted && !c.dead() && key != nil && d.following(c.id) {
			s.current = tandem.CollectionTag(key, d.pin.Hub, epoch)
			s.collect = true
		}
		s.purge = ok && st.Deleted && d.purged(c.id) && d.me != nil && bytes.Equal(c.owner(), d.me.Participant)
		for tag := range s.tags {
			if heldIn[tag] {
				s.collect = false // a held write may be a manifest this device can't see yet
			}
		}
		protect := func(value []byte, live *tandem.Entry) {
			m, kind := tandem.ParseManifest(value)
			switch kind {
			case tandem.ManifestUnknown:
				s.collect = false // it may list chunks this device can't tell (§17.7)
			case tandem.ManifestV1:
				for _, e := range m.Chunks {
					s.protected[string(e.Address)] = true
				}
				if live != nil {
					s.live = append(s.live, liveBlob{live.ID, m})
				}
			}
		}
		for _, rid := range sortedKeys(c.records) {
			r := c.records[rid]
			onFeed := true
			for _, e := range r.Entries() {
				onFeed = onFeed && d.store.OnFeed(e.ID)
			}
			if !r.Deleted() {
				for _, e := range r.Entries() {
					if e.W.Op == tandem.OpAssign && strings.HasPrefix(e.W.Field, BlobPrefix) {
						protect(e.W.Value, &e)
					}
				}
			}
			rows, err := d.store.Writes(c.id, []byte(rid))
			if err != nil {
				return nil, err
			}
			var feed []tandem.Entry
			for _, row := range rows {
				w := storedWrite(row.Payload, d.pin.Hub, c.id)
				if w == nil {
					continue
				}
				if w.Op == tandem.OpAssign && strings.HasPrefix(w.Field, BlobPrefix) {
					if m, kind := tandem.ParseManifest(w.Value); kind == tandem.ManifestV1 {
						for _, e := range m.Chunks {
							s.listed[string(e.Address)] = true
						}
					}
				}
				if !onFeed && d.store.OnFeed(writeID(w)) {
					if e := d.placeIn(c, w); e != nil {
						feed = append(feed, *e)
					}
				}
			}
			// The replica is ahead of the hub here: what other devices see is what is on the feed,
			// and that stays protected until what replaces it is on the feed too.
			if !onFeed && len(feed) > 0 {
				fr, err := merged(nil, d.pin.Hub, c.id, []byte(rid), feed...)
				if err == nil && !fr.Deleted() {
					for _, e := range fr.Entries() {
						if e.W.Op == tandem.OpAssign && strings.HasPrefix(e.W.Field, BlobPrefix) {
							protect(e.W.Value, nil)
						}
					}
				}
			}
		}
		for _, u := range ups {
			if bytes.Equal(u.Scope, c.id) {
				if w := storedWrite(u.Payload, d.pin.Hub, u.Scope); w != nil {
					protect(w.Value, nil)
				}
			}
		}
		for _, v := range dropped {
			if strings.HasPrefix(v.Name, "blobs.dropped."+hex.EncodeToString(c.id)+".") {
				if w := storedWrite(v.Value, d.pin.Hub, c.id); w != nil {
					if m, kind := tandem.ParseManifest(w.Value); kind == tandem.ManifestV1 {
						for _, e := range m.Chunks {
							s.listed[string(e.Address)] = true
						}
					}
				}
			}
		}
		// Orphans only while the current inventory is whole here: a missing listed write may be a
		// manifest that devices holding it will publish again (§17.7).
		s.orphans = s.collect
		if s.collect && !bytes.Equal(epoch, c.genesis) {
			inv, ok := d.inventories[string(epoch)]
			if !ok {
				s.orphans = false
			}
			for _, ids := range inv {
				for _, id := range ids {
					if !d.store.HasWrite(id) {
						s.orphans = false
					}
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// placeIn places a stored write in its epoch, for merging.
func (d *Device) placeIn(c *collection, w *tandem.Write) *tandem.Entry {
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

// doomed is what clean-up may destroy now, by the rules of §17.7 that need no fetch: chunks
// no protected manifest lists that a write this device holds listed, and orphans past the grace.
// Copies are settled separately.
func doomed(scopes []*blobScope, rows []ChunkRow, grace time.Duration, now int64) []string {
	byTag := map[string]*blobScope{}
	for _, s := range scopes {
		for t := range s.tags {
			byTag[t] = s
		}
	}
	var out []string
	for _, r := range rows {
		s := byTag[r.Channel]
		if s != nil && s.purge {
			out = append(out, r.ID)
			continue
		}
		if s == nil || !s.collect || r.State == chunkRejected || s.protected[string(r.Address)] {
			continue
		}
		if s.listed[string(r.Address)] || s.orphans && now-r.Seen > grace.Milliseconds() {
			out = append(out, r.ID)
		}
	}
	return out
}

// blobHygiene is the clean-up and healing pass, after a sync. It opens the blob folder only when
// there is something to collect or this device holds content it should check is still on the hub.
func (d *Device) blobHygiene(ctx context.Context, b *blobs) error {
	d.mu.RLock()
	if d.me == nil || !d.registry.Activity(d.me.Certificate).IsValid() {
		d.mu.RUnlock()
		return nil
	}
	scopes, err := d.blobScopes()
	if err != nil {
		d.mu.RUnlock()
		return err
	}
	named := d.namedContent()
	rows, err := d.store.Chunks()
	d.mu.RUnlock()
	if err != nil {
		return err
	}
	d.dropUnnamed(b, named)
	settings := b.current()
	b.mu.Lock()
	heal := !b.healed
	b.mu.Unlock()
	holds := false
	for _, s := range scopes {
		for _, l := range s.live {
			if !l.m.Inline() && fileExists(filepath.Join(b.dir, hex.EncodeToString(l.m.Hash))) {
				holds = true
			}
		}
	}
	copies := false
	count := map[string]int{}
	for _, r := range rows {
		if r.State != chunkRejected {
			count[string(r.Address)]++
			copies = copies || count[string(r.Address)] > 1
		}
	}
	// The folder is opened when the index already shows something to collect, when this device
	// should check content it holds, or when what this device lists or protects changed since it
	// last looked: only then can there be chunks it doesn't know of yet to collect.
	view := scopeView(scopes)
	b.mu.Lock()
	changed := !bytes.Equal(view, b.view)
	b.mu.Unlock()
	if len(doomed(scopes, rows, settings.OrphanGrace, time.Now().UnixMilli())) == 0 && !copies && !(heal && holds) && !changed {
		if !holds {
			b.mu.Lock()
			b.healed = true
			b.mu.Unlock()
		}
		return nil
	}
	if err := d.refreshIndex(ctx, b); err != nil {
		return err
	}
	b.mu.Lock()
	b.view = view
	b.mu.Unlock()
	d.mu.RLock()
	rows, err = d.store.Chunks()
	d.mu.RUnlock()
	if err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, id := range doomed(scopes, rows, settings.OrphanGrace, time.Now().UnixMilli()) {
		gone[id] = true
	}
	// Copies (§17.7): every device keeps the lowest verified one.
	byTag := map[string]*blobScope{}
	for _, s := range scopes {
		for t := range s.tags {
			byTag[t] = s
		}
	}
	groups := map[string][]ChunkRow{}
	for _, r := range rows {
		if s := byTag[r.Channel]; s != nil && s.collect && r.State != chunkRejected && !gone[r.ID] {
			groups[string(r.Address)] = append(groups[string(r.Address)], r)
		}
	}
	var settleThese [][]byte
	for a, g := range groups {
		if len(g) > 1 {
			settleThese = append(settleThese, []byte(a))
		}
	}
	if len(settleThese) > 0 {
		winners, err := d.settle(ctx, b, settleThese)
		if err != nil {
			return err
		}
		for _, a := range settleThese {
			w := winners[string(a)]
			if w == "" {
				continue
			}
			for _, r := range groups[string(a)] {
				if r.ID != w && tandem.BlobIDLess(w, r.ID) {
					gone[r.ID] = true
				}
			}
		}
	}
	if len(gone) > 0 {
		ids := sortedKeys(gone)
		if err := b.hub.Destroy(ctx, ids); err != nil {
			return err
		}
		d.mu.Lock()
		err := d.store.ForgetChunks(ids)
		d.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if heal {
		if err := d.heal(ctx, b, scopes); err != nil {
			return err
		}
		b.mu.Lock()
		b.healed = true
		b.mu.Unlock()
	}
	return d.forgetDropped()
}

// heal restores the chunks of live manifests in current channels whose content this device holds
// (§17.8). A local copy that seals to another address is checked against the manifest's hash:
// a good copy means the manifest is inconsistent, which is marked unreadable; a bad one is dropped.
func (d *Device) heal(ctx context.Context, b *blobs, scopes []*blobScope) error {
	for _, s := range scopes {
		if s.current == "" {
			continue
		}
		for _, l := range s.live {
			path := filepath.Join(b.dir, hex.EncodeToString(l.m.Hash))
			if l.m.Inline() || !fileExists(path) {
				continue
			}
			present, err := d.settle(ctx, b, addresses(l.m))
			if err != nil {
				return err
			}
			var missing []tandem.ChunkEntry
			var offsets []int64
			var offset int64
			for _, e := range l.m.Chunks {
				if present[string(e.Address)] == "" {
					missing = append(missing, e)
					offsets = append(offsets, offset)
				}
				offset += int64(e.Length)
			}
			if len(missing) == 0 {
				continue
			}
			ok, err := d.sealsTo(b, path, l, missing, offsets)
			if err != nil || !ok {
				continue
			}
			if err := d.appendChunks(ctx, b, path, s.current, missing, offsets, map[string][]byte{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sealsTo checks that the local copy seals to every missing address before anything goes up.
func (d *Device) sealsTo(b *blobs, path string, l liveBlob, missing []tandem.ChunkEntry, offsets []int64) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	for i, e := range missing {
		piece := make([]byte, e.Length)
		if _, err := f.ReadAt(piece, offsets[i]); err != nil {
			return false, err
		}
		env, err := tandem.SealChunk(e.Key, d.pin.Hub, piece)
		if err == nil && bytes.Equal(tandem.ChunkAddress(env), e.Address) {
			continue
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return false, err
		}
		if bytes.Equal(h.Sum(nil), l.m.Hash) {
			d.markUnreadable(l.id) // the file is right and the manifest is not: keep the file
		} else {
			f.Close()
			os.Remove(path) // the local copy is damaged
		}
		return false, nil
	}
	return true, nil
}

// forgetDropped forgets a dropped upload once none of its chunks is in the index any more.
func (d *Device) forgetDropped() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	dropped, err := d.store.Values("blobs.dropped.")
	if err != nil {
		return err
	}
	rows, err := d.store.Chunks()
	if err != nil {
		return err
	}
	inIndex := map[string]bool{}
	for _, r := range rows {
		inIndex[string(r.Address)] = true
	}
	for _, v := range dropped {
		parts := strings.Split(strings.TrimPrefix(v.Name, "blobs.dropped."), ".")
		scope, ok := unhexs(parts[0])
		if len(parts) != 2 || !ok {
			continue
		}
		w := storedWrite(v.Value, d.pin.Hub, scope)
		left := false
		if w != nil {
			if m, kind := tandem.ParseManifest(w.Value); kind == tandem.ManifestV1 {
				for _, e := range m.Chunks {
					left = left || inIndex[string(e.Address)]
				}
			}
		}
		if !left {
			if err := d.store.RemoveValue(v.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// scopeView is a digest of what every list lists and protects.
func scopeView(scopes []*blobScope) []byte {
	h := sha256.New()
	for _, s := range scopes {
		h.Write(s.cid)
		if s.purge {
			h.Write([]byte{1})
		}
		for _, set := range []map[string]bool{s.listed, s.protected, s.tags} {
			for _, k := range sortedKeys(set) {
				h.Write([]byte(k))
			}
			h.Write([]byte{0})
		}
	}
	return h.Sum(nil)
}

// namedContent is what this device still needs of the files it has: the content hash of every
// file a record of a list not yet collected names, in a kept write of a record that is not
// deleted, or a pending upload names; and those kept writes' IDs. The caller holds the state
// lock. A gone list's files stay as long as its replica does (gc.go).
type named struct{ hashes, writes map[string]bool }

func (d *Device) namedContent() named {
	out := named{map[string]bool{}, map[string]bool{}}
	name := func(value []byte) {
		if m, kind := tandem.ParseManifest(value); kind == tandem.ManifestV1 && !m.Inline() {
			out.hashes[hex.EncodeToString(m.Hash)] = true
		}
	}
	for _, k := range sortedKeys(d.collections) {
		for _, r := range d.collections[k].records {
			if r.Deleted() {
				continue
			}
			for _, e := range r.Entries() {
				if e.W.Op == tandem.OpAssign && strings.HasPrefix(e.W.Field, BlobPrefix) {
					name(e.W.Value)
					out.writes[hex.EncodeToString(e.ID)] = true
				}
			}
		}
	}
	ups, _ := d.store.Uploads()
	for _, u := range ups {
		if w := storedWrite(u.Payload, d.pin.Hub, u.Scope); w != nil {
			name(w.Value)
		}
	}
	return out
}

// dropUnnamed removes content no record names any more, downloaded or half downloaded: a deleted
// record's, a replaced version's, a collected list's, and what was kept about it.
func (d *Device) dropUnnamed(b *blobs, n named) {
	for _, dir := range []struct{ path, marker string }{{b.dir, "blobs.opened."}, {filepath.Join(b.dir, "partial"), "blobs.partial."}} {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || len(name) != 64 || n.hashes[name] {
				continue
			}
			if _, err := hex.DecodeString(name); err != nil {
				continue
			}
			if os.Remove(filepath.Join(dir.path, name)) == nil {
				d.mu.Lock()
				_ = d.store.RemoveValue(dir.marker + name)
				d.mu.Unlock()
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// A crash after commit can leave a staged hard link. Use a fresh journal
	// under the state lock: an earlier snapshot could miss a newly staged upload.
	uploads, uploadErr := d.store.Uploads()
	stages, stageErr := d.store.Values("blobs.staged.")
	if uploadErr == nil && stageErr == nil {
		pending := map[string]bool{}
		markers := map[string]string{}
		for _, v := range stages {
			name := strings.TrimPrefix(v.Name, "blobs.staged.")
			markers[name] = v.Name
			if len(v.Value) != 64 {
				pending[name] = true
				continue
			}
			id, hash := v.Value[:32], hex.EncodeToString(v.Value[32:])
			if n.hashes[hash] {
				source, cache := filepath.Join(b.dir, "staged", name), filepath.Join(b.dir, hash)
				if !fileExists(cache) {
					_ = os.Link(source, cache)
				}
				if !fileExists(cache) || !d.store.OnFeed(id) {
					pending[name] = true
					continue
				}
			}
			if !fileExists(filepath.Join(b.dir, "staged", name)) {
				_ = d.store.RemoveValue(v.Name)
			}
		}
		for _, u := range uploads {
			pending[u.Staged] = true
		}
		staged := filepath.Join(b.dir, "staged")
		entries, _ := os.ReadDir(staged)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || len(name) != 64 || pending[name] {
				continue
			}
			if _, err := hex.DecodeString(name); err == nil {
				if err := os.Remove(filepath.Join(staged, name)); err == nil && markers[name] != "" {
					_ = d.store.RemoveValue(markers[name])
				}
			}
		}
	}
	for _, prefix := range []string{"blobs.opened.", "blobs.partial."} {
		vs, _ := d.store.Values(prefix)
		for _, v := range vs {
			if h := strings.TrimPrefix(v.Name, prefix); !n.hashes[h] {
				_ = d.store.RemoveValue(v.Name)
			}
		}
	}
	vs, _ := d.store.Values("blobs.unreadable.")
	for _, v := range vs {
		if !n.writes[strings.TrimPrefix(v.Name, "blobs.unreadable.")] {
			_ = d.store.RemoveValue(v.Name)
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
