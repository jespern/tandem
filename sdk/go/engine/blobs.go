package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Blobs (DESIGN.md §17): files as immutable encrypted chunks in the hub's blob folder, pointed at by a
// manifest in a record field named _blob:<name>. A file attached here is a pending write (section
// 6): signed, kept in the upload journal and out of the replica until every chunk is on the hub,
// then merged and published like any write. Transfers run on the blob hub's own connections, in a
// worker that starts when there is work (an attach, a sync, Run) and stops when there is none;
// nothing waits on a timer.

// BlobPrefix starts the name of every field that holds a manifest.
const BlobPrefix = "_blob:"

// BlobState is what this device can do with a blob now (§17.8).
type BlobState uint8

const (
	// BlobHere: inline, or downloaded and verified; Path is the file.
	BlobHere BlobState = iota + 1
	// BlobRemote: on the hub, not fetched.
	BlobRemote
	// BlobDownloading: being fetched; Done of Chunks are here.
	BlobDownloading
	// BlobUnavailable: at the last look a chunk was not on the hub, or only junk claiming it was.
	BlobUnavailable
	// BlobUnreadable: not a manifest this device reads, or its chunks don't match it.
	BlobUnreadable
	// BlobUploading: attached on this device and going up; Path is the staged copy.
	BlobUploading
)

// Blob is a file attached to a record, as this device sees it.
type Blob struct {
	Name         string
	State        BlobState
	Size         int64
	Hash         []byte // SHA-256 of the content; nil if unreadable
	Path         string // the content on this device, for BlobHere and BlobUploading
	Chunks, Done int
}

// BlobSettings are the per-hub settings of DESIGN.md §17, changeable at runtime. Zero values take the defaults.
type BlobSettings struct {
	// ChunkSize is how a file is cut, default 1 MiB, at most 4 MiB (BLOBS.md has the measurements).
	ChunkSize int
	// Inline is the largest content kept in the manifest itself, default 16 KiB.
	Inline int
	// OrphanGrace is how long this device must have known an orphan chunk before it collects it, default 7 days.
	OrphanGrace time.Duration
	// StoreLimit bounds the content kept on this device, default 2 GiB; what was opened longest ago goes first.
	StoreLimit int64
	// Unasked is the largest file fetched without being asked, in lists set to keep files; default 25 MiB.
	Unasked int64
}

func (s BlobSettings) withDefaults() BlobSettings {
	if s.ChunkSize <= 0 || s.ChunkSize > tandem.MaxChunk {
		s.ChunkSize = tandem.DefaultChunk
	}
	if s.Inline <= 0 {
		s.Inline = tandem.DefaultInline
	}
	if s.OrphanGrace <= 0 {
		s.OrphanGrace = 7 * 24 * time.Hour
	}
	if s.StoreLimit <= 0 {
		s.StoreLimit = 2 << 30
	}
	if s.Unasked <= 0 {
		s.Unasked = 25 << 20
	}
	return s
}

var (
	// ErrBlobUnavailable: a chunk of the blob is not on the hub (§17.8); a device that holds the file restores it.
	ErrBlobUnavailable = errors.New("blob: a chunk is not on the hub")
	// ErrBlobUnreadable: the field holds no manifest this device reads, or its chunks don't match it.
	ErrBlobUnreadable = errors.New("blob: unreadable")
	// ErrNoBlobHub: UseBlobs has not been called.
	ErrNoBlobHub = errors.New("blob: no blob hub")
)

type blobs struct {
	hub      tandem.BlobHub
	dir      string
	settings BlobSettings

	mu          sync.Mutex
	kick        chan struct{}
	downloads   map[string]*download // by content hash
	unavailable map[string]bool      // manifest write IDs whose last fetch found a chunk missing
	hygiene     bool                 // a sync asked for a clean-up pass
	healed      bool                 // the healing check ran since UseBlobs
	view        []byte               // scopeView when the index was last refreshed by clean-up
	users       int                  // transfers using the hub's connections now: the worker, downloads, quota
	idle        chan struct{}        // closed and replaced whenever the worker has nothing left to do
}

type download struct {
	done        chan struct{}
	chunks, got int
	err         error
}

// UseBlobs turns blobs on: hub is the blob folder of this device's hub. Pending uploads resume.
func (d *Device) UseBlobs(hub tandem.BlobHub, settings BlobSettings) error {
	if err := os.MkdirAll(filepath.Join(d.store.BlobDir(), "staged"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(d.store.BlobDir(), "partial"), 0o700); err != nil {
		return err
	}
	b := &blobs{hub: hub, dir: d.store.BlobDir(), settings: settings.withDefaults(), kick: make(chan struct{}, 1),
		downloads: map[string]*download{}, unavailable: map[string]bool{}, idle: make(chan struct{})}
	d.mu.Lock()
	d.blobs = b
	d.mu.Unlock()
	go d.blobWorker(b)
	b.wake()
	return nil
}

// SetBlobSettings changes the blob settings at runtime.
func (d *Device) SetBlobSettings(s BlobSettings) {
	if b := d.blobState(); b != nil {
		b.mu.Lock()
		b.settings = s.withDefaults()
		b.mu.Unlock()
	}
}

func (d *Device) blobState() *blobs {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.blobs
}

// use notes a transfer starting on the hub's connections; the returned function ends it, and the
// last one to end lets the connections go.
func (b *blobs) use() func() {
	b.mu.Lock()
	b.users++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		b.users--
		last := b.users == 0
		b.mu.Unlock()
		if last {
			b.hub.Release()
		}
	}
}

func (b *blobs) wake() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

func (b *blobs) current() BlobSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// afterSync asks the worker for a clean-up and healing pass.
func (d *Device) afterSync() {
	if b := d.blobState(); b != nil {
		b.mu.Lock()
		b.hygiene = true
		b.mu.Unlock()
		b.wake()
	}
}

// BlobsIdle returns a channel that is closed when the blob worker next has nothing to do: for
// tests and for an app that wants to know its uploads are through.
func (d *Device) BlobsIdle() <-chan struct{} {
	b := d.blobState()
	if b == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.idle
}

// SettleBlobs wakes the blob worker and returns once it has run a whole round after now.
func (d *Device) SettleBlobs(ctx context.Context) error {
	b := d.blobState()
	if b == nil {
		return ErrNoBlobHub
	}
	for range 2 {
		ch := d.BlobsIdle()
		b.wake()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// CheckBlobs makes the next clean-up pass check that the chunks of files this device holds are still on
// the hub, and restore those that aren't, as after a restart (§17.8). An app may call it when it
// comes to the foreground.
func (d *Device) CheckBlobs() {
	if b := d.blobState(); b != nil {
		b.mu.Lock()
		b.healed, b.hygiene = false, true
		b.mu.Unlock()
		b.wake()
	}
}

// blobWorker runs uploads and the clean-up pass whenever woken, and lets the hub's transfer
// connections go when it runs out of work.
func (d *Device) blobWorker(b *blobs) {
	for {
		select {
		case <-d.life.Done():
			return
		case <-b.kick:
		}
		done := b.use()
		for d.life.Err() == nil {
			worked, err := d.blobRound(d.life, b)
			if err != nil && d.life.Err() == nil {
				d.reportFailure(err)
			}
			if !worked || err != nil {
				break
			}
		}
		done()
		b.mu.Lock()
		if len(b.kick) == 0 {
			close(b.idle)
			b.idle = make(chan struct{})
		}
		b.mu.Unlock()
	}
}

// blobRound is one round of work; it reports whether there was any.
func (d *Device) blobRound(ctx context.Context, b *blobs) (bool, error) {
	d.mu.Lock()
	ups, err := d.store.Uploads()
	d.mu.Unlock()
	if err != nil {
		return false, err
	}
	if len(ups) > 0 {
		if err := d.uploadAll(ctx, b, ups); err != nil {
			return false, err
		}
		d.mu.Lock()
		left, err := d.store.Uploads()
		d.mu.Unlock()
		if err != nil {
			return false, err
		}
		// What is left waits for the next sync or attach, not for a loop.
		if len(left) == len(ups) {
			return false, nil
		}
		return true, nil
	}
	b.mu.Lock()
	want := b.hygiene || !b.healed
	b.hygiene = false
	b.mu.Unlock()
	if !want {
		return false, nil
	}
	if err := d.blobHygiene(ctx, b); err != nil {
		return false, err
	}
	return false, d.keepFiles(ctx, b)
}

// Attaching (§17.6)

// AttachFile attaches a copy of the file at path to a field of a record, as _blob:<name>. Content up
// to the inline size is written at once; anything larger is pending until its chunks are on the hub,
// and shows as BlobUploading meanwhile. It works offline.
func (d *Device) AttachFile(ctx context.Context, list, record []byte, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return d.Attach(ctx, list, record, name, f)
}

// AttachBytes attaches content held in memory.
func (d *Device) AttachBytes(ctx context.Context, list, record []byte, name string, content []byte) error {
	return d.Attach(ctx, list, record, name, bytes.NewReader(content))
}

// Attach attaches what src holds; see AttachFile.
func (d *Device) Attach(ctx context.Context, list, record []byte, name string, src io.Reader) error {
	b := d.blobState()
	if b == nil {
		return ErrNoBlobHub
	}
	if name == "" {
		return fail("blob: a name is needed")
	}
	settings := b.current()
	// Stage: a copy of its own, so later changes to the original don't matter.
	staged, err := os.CreateTemp(filepath.Join(b.dir, "staged"), "new-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		staged.Close()
		if !keep {
			os.Remove(staged.Name())
		}
	}()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(staged, h), src)
	if err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	field := BlobPrefix + name
	if size <= int64(settings.Inline) {
		content, err := os.ReadFile(staged.Name())
		if err != nil {
			return err
		}
		value, err := tandem.EncodeManifest(&tandem.Manifest{Size: size, Hash: h.Sum(nil), Data: content})
		if err != nil {
			return err
		}
		defer d.begin()()
		d.dropPendingField(list, record, field)
		return d.writeChanges(ctx, list, record, []Change{Set(field, value)})
	}
	// Describe: one pass, sealing every chunk to learn its address.
	r := random(32)
	m := &tandem.Manifest{Size: size, Hash: h.Sum(nil), Chunks: []tandem.ChunkEntry{}}
	buf := make([]byte, settings.ChunkSize)
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for {
		n, err := io.ReadFull(staged, buf)
		if n > 0 {
			piece := buf[:n]
			key, _ := tandem.ChunkKey(r, piece)
			env, err := tandem.SealChunk(key, d.pin.Hub, piece)
			if err != nil {
				return err
			}
			m.Chunks = append(m.Chunks, tandem.ChunkEntry{Address: tandem.ChunkAddress(env), Key: key, Length: n})
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	value, err := tandem.EncodeManifest(m)
	if err != nil {
		return err
	}
	defer d.begin()()
	c, me, head, epoch, _, err := d.writable(list, record)
	if err != nil {
		return err
	}
	// Sign: with the context it would have if it were published now; not merged (§17.6, step 3).
	w, err := d.signRecord(c, me, record, field, tandem.OpAssign, value, nil, head, epoch, nil)
	if err != nil {
		return err
	}
	payload, err := onePayload(record, w)
	if err != nil {
		return err
	}
	id := writeID(w)
	final := filepath.Join(b.dir, "staged", hex.EncodeToString(id))
	if err := os.Rename(staged.Name(), final); err != nil {
		return err
	}
	keep = true
	d.dropPendingField(list, record, field)
	if err := d.store.PutUpload(Upload{Write: id, Scope: list, Record: record, Field: field, Epoch: epoch, Payload: payload,
		R: r, Staged: filepath.Base(final)}); err != nil {
		os.Remove(final)
		return err
	}
	d.changed(Event{Kind: EventBlobs, List: list, Records: [][]byte{record}})
	b.wake()
	return nil
}

// dropPendingField forgets a pending write of the same field, replaced by a new one. Its chunks
// already up are orphans; their uploader collects them (§17.6) in the next clean-up pass.
func (d *Device) dropPendingField(list, record []byte, field string) {
	ups, _ := d.store.Uploads()
	for _, u := range ups {
		if bytes.Equal(u.Scope, list) && bytes.Equal(u.Record, record) && u.Field == field {
			d.dropUpload(u)
		}
	}
}

func (d *Device) dropUpload(u Upload) {
	_ = d.store.RemoveUpload(u.Write)
	_ = d.store.SetValue("blobs.dropped."+hex.EncodeToString(u.Scope)+"."+hex.EncodeToString(u.Write), u.Payload)
	if b := d.blobs; b != nil {
		os.Remove(filepath.Join(b.dir, "staged", u.Staged))
	}
	d.changed(Event{Kind: EventBlobs, List: u.Scope, Records: [][]byte{u.Record}})
}

// CancelAttach drops a pending attachment of the field: its write is never published.
func (d *Device) CancelAttach(list, record []byte, name string) {
	defer d.begin()()
	d.dropPendingField(list, record, BlobPrefix+name)
}

// uploadOf parses an upload's write and manifest.
func (d *Device) uploadOf(u Upload) (*tandem.Write, *tandem.Manifest, bool) {
	w := storedWrite(u.Payload, d.pin.Hub, u.Scope)
	if w == nil {
		return nil, nil, false
	}
	m, kind := tandem.ParseManifest(w.Value)
	return w, m, kind == tandem.ManifestV1 && !m.Inline()
}

// uploadAll runs steps 4 and 5 of §17.6 for every pending write: append what the hub lacks,
// then commit the writes whose chunks are all on it.
func (d *Device) uploadAll(ctx context.Context, b *blobs, ups []Upload) error {
	if err := d.refreshIndex(ctx, b); err != nil {
		return err
	}
	appended := map[string][]byte{} // message ID -> address, appended by this device in this round
	for _, u := range ups {
		w, m, ok := d.uploadOf(u)
		if !ok || w == nil {
			d.mu.Lock()
			d.dropUpload(u)
			d.flushEvents()
			d.mu.Unlock()
			continue
		}
		present, err := d.settle(ctx, b, addresses(m))
		if err != nil {
			return err
		}
		var missing []tandem.ChunkEntry
		var offsets []int64
		var offset int64
		for _, e := range m.Chunks {
			if present[string(e.Address)] == "" && !slices.ContainsFunc(missing, func(x tandem.ChunkEntry) bool { return bytes.Equal(x.Address, e.Address) }) {
				missing = append(missing, e)
				offsets = append(offsets, offset)
			}
			offset += int64(e.Length)
		}
		tag, ok := d.channelOf(u.Scope)
		if !ok {
			continue // no key for the list's current epoch now: wait
		}
		if err := d.appendChunks(ctx, b, filepath.Join(b.dir, "staged", u.Staged), tag, missing, offsets, appended); err != nil {
			return err
		}
	}
	if err := d.refreshIndex(ctx, b); err != nil {
		return err
	}
	d.mu.Lock()
	for id, address := range appended {
		if rows, _ := d.store.ChunksWithAddress(address); slices.ContainsFunc(rows, func(r ChunkRow) bool { return r.ID == id }) {
			_ = d.store.SetChunkState(id, chunkVerified)
		}
	}
	d.mu.Unlock()
	for _, u := range ups {
		_, m, ok := d.uploadOf(u)
		if !ok {
			continue
		}
		present, err := d.settle(ctx, b, addresses(m))
		if err != nil {
			return err
		}
		whole := true
		for _, e := range m.Chunks {
			whole = whole && present[string(e.Address)] != ""
		}
		if whole {
			if err := d.commit(ctx, b, u); err != nil {
				return err
			}
		}
	}
	return nil
}

func addresses(m *tandem.Manifest) [][]byte {
	var out [][]byte
	for _, e := range m.Chunks {
		out = append(out, e.Address)
	}
	return out
}

// channelOf is the tag of a list's current channel.
func (d *Device) channelOf(cid []byte) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.collections[string(cid)]
	if c == nil {
		return "", false
	}
	epoch := c.epochOf(c.head())
	key := c.keys[string(epoch)]
	if epoch == nil || key == nil {
		return "", false
	}
	return tandem.CollectionTag(key, d.pin.Hub, epoch), true
}

// appendChunks seals chunks again from a file holding the content and appends them a few at a time,
// in the hub's form. The first escaped chunk this device appends on the hub is fetched back and
// compared (§17.4.2): if the server changed it, the hub is written in base64 from then on.
func (d *Device) appendChunks(ctx context.Context, b *blobs, path, tag string, entries []tandem.ChunkEntry, offsets []int64,
	appended map[string][]byte) error {
	if len(entries) == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const batch = 4
	for i := 0; i < len(entries); i += batch {
		var out []tandem.OutgoingChunk
		for j := i; j < min(i+batch, len(entries)); j++ {
			e := entries[j]
			piece := make([]byte, e.Length)
			if _, err := f.ReadAt(piece, offsets[j]); err != nil {
				return err
			}
			env, err := tandem.SealChunk(e.Key, d.pin.Hub, piece)
			if err != nil {
				return err
			}
			if !bytes.Equal(tandem.ChunkAddress(env), e.Address) {
				return fail("blob: the local copy does not seal to its manifest")
			}
			out = append(out, tandem.OutgoingChunk{Channel: tag, Envelope: env})
		}
		form := d.blobForm()
		ids, err := b.hub.Append(ctx, out, form)
		if err != nil {
			return err
		}
		if form == tandem.FormEscaped && len(ids) > 0 && ids[0] != "" {
			damaged, err := d.checkForm(ctx, b, ids[0], out[0])
			if err != nil {
				return err
			}
			if damaged {
				// None of this batch can be taken as verified: send it all again, in base64.
				d.mu.Lock()
				for _, id := range ids {
					if id != "" {
						_ = d.store.SetChunkState(id, chunkRejected)
					}
				}
				d.mu.Unlock()
				if ids, err = b.hub.Append(ctx, out, tandem.FormBase64); err != nil {
					return err
				}
			}
		}
		for k, id := range ids {
			if id != "" {
				appended[id] = tandem.ChunkAddress(out[k].Envelope)
			}
		}
	}
	return nil
}

func (d *Device) blobForm() tandem.ChunkForm {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if v, ok := d.store.Value("blobs.form"); ok && string(v) == "base64" {
		return tandem.FormBase64
	}
	return tandem.FormEscaped
}

// checkForm fetches the first escaped chunk this device appends on the hub back, once, and reports
// whether the server changed it; if so the hub is written in base64 from then on (§17.4.2).
func (d *Device) checkForm(ctx context.Context, b *blobs, id string, sent tandem.OutgoingChunk) (bool, error) {
	d.mu.RLock()
	_, checked := d.store.Value("blobs.formchecked")
	d.mu.RUnlock()
	if checked {
		return false, nil
	}
	got, err := b.hub.Fetch(ctx, []string{id})
	if err != nil {
		return false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(got) == 1 && bytes.Equal(got[0].Envelope, sent.Envelope) {
		return false, d.store.SetValue("blobs.formchecked", []byte{1})
	}
	if err := d.store.SetValue("blobs.form", []byte("base64")); err != nil {
		return false, err
	}
	if err := d.store.SetValue("blobs.formchecked", []byte{1}); err != nil {
		return false, err
	}
	d.changed(Event{Kind: EventSyncFailed, Message: "blob: the server changed the escaped form of a chunk; chunks go up in base64 on this hub from now on"})
	d.flushEvents()
	return true, nil
}

// commit merges a pending write whose chunks are all on the hub into the replica and publishes it
// (§17.6, step 5). A record deleted meanwhile drops it; a write whose epoch was replaced is
// signed again under the current one, as a late write is (§11.6).
func (d *Device) commit(ctx context.Context, b *blobs, u Upload) error {
	defer d.begin()()
	ups, err := d.store.Uploads()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(ups, func(x Upload) bool { return bytes.Equal(x.Write, u.Write) }) {
		return nil // cancelled or replaced meanwhile
	}
	w, m, ok := d.uploadOf(u)
	c := d.collections[string(u.Scope)]
	if !ok || c == nil {
		d.dropUpload(u)
		return nil
	}
	if r := c.records[string(u.Record)]; r != nil && r.Deleted() {
		d.dropUpload(u)
		return nil
	}
	if st, ok := c.headStatement(); c.dead() || !ok || st.Deleted || d.me == nil || st.member(d.me.Participant) == nil ||
		st.member(d.me.Participant).Role == Reader {
		d.dropUpload(u) // never published: its author can't write here any more, and its chunks are orphans (§17.6)
		return nil
	}
	c2, me, head, epoch, cur, err := d.writable(u.Scope, u.Record)
	if err != nil {
		return nil // no key for the current epoch yet, or waiting for a rekey: the next round tries again
	}
	if !bytes.Equal(epoch, u.Epoch) {
		if w, err = d.signRecord(c2, me, u.Record, u.Field, tandem.OpAssign, w.Value, nil, head, epoch, nil); err != nil {
			return err
		}
	}
	content := filepath.Join(b.dir, hex.EncodeToString(m.Hash))
	if err := os.Rename(filepath.Join(b.dir, "staged", u.Staged), content); err != nil && !os.IsNotExist(err) {
		return err
	}
	d.touchLocked(m.Hash)
	if err := d.store.RemoveUpload(u.Write); err != nil {
		return err
	}
	d.changed(Event{Kind: EventBlobs, List: u.Scope, Records: [][]byte{u.Record}})
	return d.keepWrites(ctx, c2, u.Record, epoch, cur.s.KeyN, []*tandem.Write{w})
}

// The index (§17.4.3)

// refreshIndex brings the chunk index up to date from the blob folder's headers.
func (d *Device) refreshIndex(ctx context.Context, b *blobs) error {
	for more := true; more; {
		d.mu.RLock()
		cursor, _ := d.store.Value("blobs.cursor")
		d.mu.RUnlock()
		changes, err := b.hub.Changes(ctx, string(cursor))
		if err != nil {
			return err
		}
		if changes.More && changes.Cursor == string(cursor) {
			return fail("blob hub: a page of changes made no progress")
		}
		d.mu.Lock()
		err = d.store.Transaction(func() error {
			if err := d.store.ForgetChunks(changes.Removed); err != nil {
				return err
			}
			if err := d.store.NoteChunks(changes.Added, time.Now().UnixMilli()); err != nil {
				return err
			}
			return d.store.SetValue("blobs.cursor", []byte(changes.Cursor))
		})
		d.mu.Unlock()
		if err != nil {
			return err
		}
		more = changes.More
	}
	return nil
}

// settle finds, for each address, the lowest message this device has verified as carrying it,
// fetching unverified claims to verify or reject them (§17.4.3). Addresses without one map to "".
func (d *Device) settle(ctx context.Context, b *blobs, addrs [][]byte) (map[string]string, error) {
	out := map[string]string{}
	for {
		var fetch []string
		claims := map[string][]byte{}
		d.mu.RLock()
		for _, a := range addrs {
			if _, done := out[string(a)]; done {
				continue
			}
			rows, err := d.store.ChunksWithAddress(a)
			if err != nil {
				d.mu.RUnlock()
				return nil, err
			}
			sort.Slice(rows, func(i, j int) bool { return tandem.BlobIDLess(rows[i].ID, rows[j].ID) })
			verified := ""
			var unverified string
			for _, r := range rows {
				if r.State == chunkVerified && verified == "" {
					verified = r.ID
				}
				if r.State == chunkUnverified && unverified == "" {
					unverified = r.ID
				}
			}
			switch {
			case verified != "":
				out[string(a)] = verified
			case unverified != "":
				fetch = append(fetch, unverified)
				claims[unverified] = a
			default:
				out[string(a)] = ""
			}
		}
		d.mu.RUnlock()
		if len(fetch) == 0 {
			return out, nil
		}
		got, err := b.hub.Fetch(ctx, fetch)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		d.mu.Lock()
		for _, c := range got {
			seen[c.ID] = true
			state := chunkRejected
			if bytes.Equal(tandem.ChunkAddress(c.Envelope), claims[c.ID]) {
				state = chunkVerified
			}
			_ = d.store.SetChunkState(c.ID, state)
		}
		var gone []string
		for _, id := range fetch {
			if !seen[id] {
				gone = append(gone, id)
			}
		}
		_ = d.store.ForgetChunks(gone)
		d.mu.Unlock()
	}
}

// Reading (§17.8)

// Blob is the file in a field _blob:<name> of a record, as this device sees it now.
func (d *Device) Blob(list, record []byte, name string) (Blob, bool) {
	defer d.read()()
	b := d.blobs
	field := BlobPrefix + name
	if b != nil {
		ups, _ := d.store.Uploads()
		for _, u := range ups {
			if bytes.Equal(u.Scope, list) && bytes.Equal(u.Record, record) && u.Field == field {
				if _, m, ok := d.uploadOf(u); ok {
					return Blob{Name: name, State: BlobUploading, Size: m.Size, Hash: m.Hash, Chunks: len(m.Chunks),
						Path: filepath.Join(b.dir, "staged", u.Staged)}, true
				}
			}
		}
	}
	c := d.collections[string(list)]
	if c == nil {
		return Blob{}, false
	}
	r := c.records[string(record)]
	if r == nil || r.Deleted() {
		return Blob{}, false
	}
	value, ok := r.Value(field)
	if !ok {
		return Blob{}, false
	}
	return d.blobOf(name, r, field, value), true
}

func (d *Device) blobOf(name string, r *tandem.Record, field string, value []byte) Blob {
	m, kind := tandem.ParseManifest(value)
	if kind != tandem.ManifestV1 {
		return Blob{Name: name, State: BlobUnreadable}
	}
	out := Blob{Name: name, Size: m.Size, Hash: m.Hash, Chunks: len(m.Chunks), State: BlobRemote}
	var id []byte
	if s := r.Siblings(field); len(s) > 0 {
		id = s[0].ID
	}
	if _, bad := d.store.Value("blobs.unreadable." + hex.EncodeToString(id)); bad {
		out.State = BlobUnreadable
		return out
	}
	b := d.blobs
	if m.Inline() {
		out.State = BlobHere
		if b != nil {
			out.Path = filepath.Join(b.dir, hex.EncodeToString(m.Hash))
		}
		return out
	}
	if b == nil {
		return out
	}
	path := filepath.Join(b.dir, hex.EncodeToString(m.Hash))
	if _, err := os.Stat(path); err == nil {
		out.State, out.Path, out.Done = BlobHere, path, len(m.Chunks)
		return out
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if dl := b.downloads[string(m.Hash)]; dl != nil {
		out.State, out.Done = BlobDownloading, dl.got
	} else if b.unavailable[string(id)] {
		out.State = BlobUnavailable
	}
	return out
}

// FetchBlob makes the file in the field _blob:<name> of a record here and returns its path. Chunks
// come a few to a command over the blob hub's transfer connections; a download that stops resumes.
func (d *Device) FetchBlob(ctx context.Context, list, record []byte, name string) (string, error) {
	b := d.blobState()
	if b == nil {
		return "", ErrNoBlobHub
	}
	d.mu.RLock()
	c := d.collections[string(list)]
	var value, id []byte
	if c != nil {
		if r := c.records[string(record)]; r != nil && !r.Deleted() {
			if s := r.Siblings(BlobPrefix + name); len(s) > 0 {
				value, id = s[0].W.Value, s[0].ID
			}
		}
	}
	_, bad := d.store.Value("blobs.unreadable." + hex.EncodeToString(id))
	d.mu.RUnlock()
	if value == nil {
		return "", fail("blob: no such file")
	}
	m, kind := tandem.ParseManifest(value)
	if kind != tandem.ManifestV1 || bad {
		return "", ErrBlobUnreadable
	}
	return d.fetchManifest(ctx, b, list, record, id, m)
}

func (d *Device) fetchManifest(ctx context.Context, b *blobs, list, record, id []byte, m *tandem.Manifest) (string, error) {
	path := filepath.Join(b.dir, hex.EncodeToString(m.Hash))
	if m.Inline() {
		if _, err := os.Stat(path); err != nil {
			if err := writeAtomically(path, m.Data); err != nil {
				return "", err
			}
		}
		return path, nil
	}
	if _, err := os.Stat(path); err == nil {
		d.touch(m.Hash)
		return path, nil
	}
	b.mu.Lock()
	if dl := b.downloads[string(m.Hash)]; dl != nil {
		b.mu.Unlock()
		select {
		case <-dl.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if dl.err != nil {
			return "", dl.err
		}
		return path, nil
	}
	dl := &download{done: make(chan struct{}), chunks: len(m.Chunks)}
	b.downloads[string(m.Hash)] = dl
	delete(b.unavailable, string(id))
	b.mu.Unlock()
	defer b.use()()
	d.blobEvent(list, record)
	err := d.download(ctx, b, dl, id, m, path)
	b.mu.Lock()
	delete(b.downloads, string(m.Hash))
	if errors.Is(err, ErrBlobUnavailable) {
		b.unavailable[string(id)] = true
	}
	b.mu.Unlock()
	dl.err = err
	close(dl.done)
	d.blobEvent(list, record)
	if err != nil {
		return "", err
	}
	d.evict(b)
	return path, nil
}

func (d *Device) blobEvent(list, record []byte) {
	d.mu.Lock()
	d.changed(Event{Kind: EventBlobs, List: list, Records: [][]byte{record}})
	d.flushEvents()
	d.mu.Unlock()
}

// download fetches every chunk not yet in the partial file, checks each against its address, its
// key and its length before writing it at its offset, and checks the whole content's hash.
func (d *Device) download(ctx context.Context, b *blobs, dl *download, id []byte, m *tandem.Manifest, path string) error {
	partial := filepath.Join(b.dir, "partial", hex.EncodeToString(m.Hash))
	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	doneKey := "blobs.partial." + hex.EncodeToString(m.Hash)
	d.mu.RLock()
	have, _ := d.store.Value(doneKey)
	d.mu.RUnlock()
	have = append(have, make([]byte, max(0, (len(m.Chunks)+7)/8-len(have)))...)
	type want struct {
		entry  tandem.ChunkEntry
		offset int64
		index  int
	}
	var todo []want
	var offset int64
	for i, e := range m.Chunks {
		if have[i/8]&(1<<(i%8)) == 0 {
			todo = append(todo, want{e, offset, i})
		} else {
			dl.got++
		}
		offset += int64(e.Length)
	}
	if len(todo) > 0 {
		if err := d.refreshIndex(ctx, b); err != nil {
			return err
		}
	}
	tried := map[string]bool{}
	for len(todo) > 0 {
		// Up to four distinct addresses a round; an address repeated in the manifest is fetched once.
		var ids []string
		wants := map[string][]want{} // candidate message ID -> the entries it fills
		addressOf := map[string][]byte{}
		var rest []want
		for _, t := range todo {
			placed := false
			for id, a := range addressOf {
				if bytes.Equal(a, t.entry.Address) {
					wants[id] = append(wants[id], t)
					placed = true
				}
			}
			if placed {
				continue
			}
			if len(ids) == 4 {
				rest = append(rest, t)
				continue
			}
			d.mu.RLock()
			rows, err := d.store.ChunksWithAddress(t.entry.Address)
			d.mu.RUnlock()
			if err != nil {
				return err
			}
			sort.Slice(rows, func(i, j int) bool {
				if (rows[i].State == chunkVerified) != (rows[j].State == chunkVerified) {
					return rows[i].State == chunkVerified
				}
				return tandem.BlobIDLess(rows[i].ID, rows[j].ID)
			})
			candidate := ""
			for _, r := range rows {
				if r.State != chunkRejected && !tried[r.ID] {
					candidate = r.ID
					break
				}
			}
			if candidate == "" {
				return ErrBlobUnavailable
			}
			tried[candidate] = true
			ids = append(ids, candidate)
			wants[candidate] = []want{t}
			addressOf[candidate] = t.entry.Address
		}
		got, err := b.hub.Fetch(ctx, ids)
		if err != nil {
			return err
		}
		bodies := map[string][]byte{}
		for _, c := range got {
			bodies[c.ID] = c.Envelope
		}
		var next []want
		for _, mid := range ids {
			env, ok := bodies[mid]
			address := addressOf[mid]
			good := ok && bytes.Equal(tandem.ChunkAddress(env), address)
			d.mu.Lock()
			switch {
			case !ok:
				_ = d.store.ForgetChunks([]string{mid})
			case !good:
				_ = d.store.SetChunkState(mid, chunkRejected)
			default:
				_ = d.store.SetChunkState(mid, chunkVerified)
			}
			d.mu.Unlock()
			if !good {
				next = append(next, wants[mid]...)
				continue
			}
			for _, t := range wants[mid] {
				plain, err := tandem.OpenChunk(t.entry.Key, d.pin.Hub, env, t.entry.Length)
				if err != nil {
					d.markUnreadable(id)
					return ErrBlobUnreadable
				}
				if _, err := f.WriteAt(plain, t.offset); err != nil {
					return err
				}
				have[t.index/8] |= 1 << (t.index % 8)
				b.mu.Lock()
				dl.got++
				b.mu.Unlock()
			}
		}
		if err := f.Sync(); err != nil {
			return err
		}
		d.mu.Lock()
		err = d.store.SetValue(doneKey, have)
		d.mu.Unlock()
		if err != nil {
			return err
		}
		todo = append(next, rest...)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if n, err := io.Copy(h, f); err != nil || n != m.Size {
		d.markUnreadable(id)
		return ErrBlobUnreadable
	}
	if !bytes.Equal(h.Sum(nil), m.Hash) {
		d.markUnreadable(id)
		os.Remove(partial)
		return ErrBlobUnreadable
	}
	if err := os.Rename(partial, path); err != nil {
		return err
	}
	d.mu.Lock()
	_ = d.store.RemoveValue(doneKey)
	d.mu.Unlock()
	d.touch(m.Hash)
	return nil
}

func (d *Device) markUnreadable(id []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.store.SetValue("blobs.unreadable."+hex.EncodeToString(id), []byte{1})
}

// touch notes that content was used now, for the store's eviction order.
func (d *Device) touch(hash []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touchLocked(hash)
}

func (d *Device) touchLocked(hash []byte) {
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], uint64(time.Now().UnixMilli()))
	_ = d.store.SetValue("blobs.opened."+hex.EncodeToString(hash), t[:])
}

// evict removes content opened longest ago while the store is over its limit; it becomes remote again.
func (d *Device) evict(b *blobs) {
	limit := b.current().StoreLimit
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return
	}
	type file struct {
		name   string
		size   int64
		opened uint64
	}
	var files []file
	var total int64
	d.mu.RLock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		var opened uint64
		if v, ok := d.store.Value("blobs.opened." + e.Name()); ok && len(v) == 8 {
			opened = binary.BigEndian.Uint64(v)
		}
		files = append(files, file{e.Name(), info.Size(), opened})
		total += info.Size()
	}
	d.mu.RUnlock()
	sort.Slice(files, func(i, j int) bool { return files[i].opened < files[j].opened })
	for _, f := range files {
		if total <= limit {
			break
		}
		if os.Remove(filepath.Join(b.dir, f.name)) == nil {
			total -= f.size
			d.mu.Lock()
			_ = d.store.RemoveValue("blobs.opened." + f.name)
			d.mu.Unlock()
		}
	}
}

func writeAtomically(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Keeping files (§17.8)

// KeepFiles sets whether this device fetches every file of a list unasked, up to the Unasked size.
func (d *Device) KeepFiles(list []byte, keep bool) error {
	defer d.begin()()
	name := "blobs.keep." + hex.EncodeToString(list)
	if !keep {
		return d.store.RemoveValue(name)
	}
	if err := d.store.SetValue(name, []byte{1}); err != nil {
		return err
	}
	if d.blobs != nil {
		d.blobs.mu.Lock()
		d.blobs.hygiene = true
		d.blobs.mu.Unlock()
		d.blobs.wake()
	}
	return nil
}

func (d *Device) keepFiles(ctx context.Context, b *blobs) error {
	type job struct {
		list, record, id []byte
		m                *tandem.Manifest
	}
	var jobs []job
	limit := b.current().Unasked
	d.mu.RLock()
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		if _, keep := d.store.Value("blobs.keep." + hex.EncodeToString(c.id)); !keep {
			continue
		}
		for rid, r := range c.records {
			if r.Deleted() {
				continue
			}
			for _, e := range r.Entries() {
				if e.W.Op != tandem.OpAssign || !strings.HasPrefix(e.W.Field, BlobPrefix) {
					continue
				}
				if s := r.Siblings(e.W.Field); len(s) == 0 || !bytes.Equal(s[0].ID, e.ID) {
					continue
				}
				m, kind := tandem.ParseManifest(e.W.Value)
				if kind == tandem.ManifestV1 && !m.Inline() && m.Size <= limit {
					if _, err := os.Stat(filepath.Join(b.dir, hex.EncodeToString(m.Hash))); err != nil {
						jobs = append(jobs, job{c.id, []byte(rid), e.ID, m})
					}
				}
			}
		}
	}
	d.mu.RUnlock()
	for _, j := range jobs {
		if _, err := d.fetchManifest(ctx, b, j.list, j.record, j.id, j.m); err != nil && !errors.Is(err, ErrBlobUnavailable) && !errors.Is(err, ErrBlobUnreadable) {
			return err
		}
	}
	return nil
}

// BlobQuota is the blob folder's storage quota; Known is false where the server reports none.
func (d *Device) BlobQuota(ctx context.Context) (tandem.Quota, error) {
	b := d.blobState()
	if b == nil {
		return tandem.Quota{}, ErrNoBlobHub
	}
	defer b.use()()
	return b.hub.Quota(ctx)
}
