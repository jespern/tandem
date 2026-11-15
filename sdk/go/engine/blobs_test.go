package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// BLOBS.md in the engine: attaching, the pending write, uploads, downloads, clean-up and healing,
// on a memory hub and a memory blob folder.

var smallBlobs = BlobSettings{ChunkSize: 1000, Inline: 100}

type blobWorld struct {
	hub        *MemoryHub
	blobs      *MemoryBlobHub
	f          *Founding
	bob, alice *Device
	list       []byte
}

// newBlobWorld is Bob's list, shared with Alice as a writer, both using one blob folder.
func newBlobWorld(t *testing.T) *blobWorld {
	t.Helper()
	hub, f, _, bob := found(t, "Bob’s Mac", nil)
	alice := join(t, bob, f, hub, "Alice", nil)
	syncAll(t, bob)
	list := try(bob.CreateList(bg, "Photos"))
	must(t, bob.Share(bg, list, me(t, alice).Participant, Writer, "Alice"))
	syncAll(t, alice)
	must(t, alice.Accept(bg, list))
	syncAll(t, bob, alice)
	w := &blobWorld{hub: hub, blobs: NewMemoryBlobHub(), f: f, bob: bob, alice: alice, list: list}
	must(t, bob.UseBlobs(w.blobs, smallBlobs))
	must(t, alice.UseBlobs(w.blobs, smallBlobs))
	settle(t, bob, alice)
	return w
}

func content(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// settle lets each device's blob worker run at least one whole round after now.
func settle(t *testing.T, ds ...*Device) {
	t.Helper()
	for _, d := range ds {
		for range 2 {
			ch := d.BlobsIdle()
			if b := d.blobState(); b != nil {
				b.wake()
			}
			select {
			case <-ch:
			case <-time.After(10 * time.Second):
				t.Fatal("the blob worker didn't settle")
			}
		}
	}
}

// recheck makes the next clean-up pass check held content against the hub again, as after a restart.
func recheck(ds ...*Device) {
	for _, d := range ds {
		b := d.blobState()
		b.mu.Lock()
		b.healed = false
		b.mu.Unlock()
	}
}

// round lets the worker run exactly the rounds one wake starts.
func round(t *testing.T, d *Device) {
	t.Helper()
	ch := d.BlobsIdle()
	d.blobState().wake()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the blob worker didn't settle")
	}
}

// clean runs a sync and the clean-up pass that follows it.
func clean(t *testing.T, ds ...*Device) {
	t.Helper()
	for _, d := range ds {
		try(d.Sync(bg))
		settle(t, d)
	}
}

func blobState(t *testing.T, d *Device, list, record []byte, name string) BlobState {
	t.Helper()
	b, ok := d.Blob(list, record, name)
	if !ok {
		return 0
	}
	return b.State
}

func fetched(t *testing.T, d *Device, list, record []byte, name string) []byte {
	t.Helper()
	path, err := d.FetchBlob(bg, list, record, name)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return try(readFile(path))
}

func manifestOf(t *testing.T, d *Device, list, record []byte, name string) *tandem.Manifest {
	t.Helper()
	v, ok := d.Value(list, record, BlobPrefix+name)
	if !ok {
		t.Fatal("no manifest")
	}
	m, kind := tandem.ParseManifest(v)
	if kind != tandem.ManifestV1 {
		t.Fatal("not a manifest")
	}
	return m
}

func onHub(h *MemoryBlobHub, m *tandem.Manifest) int {
	n := 0
	for _, e := range m.Chunks {
		n += len(h.WithAddress(e.Address))
	}
	return n
}

func TestBlobAttachUploadFetch(t *testing.T) {
	w := newBlobWorld(t)
	photo, data := NewRecordID(), content(5500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	if s := blobState(t, w.bob, w.list, photo, "photo"); s != BlobUploading && s != BlobHere {
		t.Fatalf("right after attaching: %d", s)
	}
	settle(t, w.bob)
	if s := blobState(t, w.bob, w.list, photo, "photo"); s != BlobHere {
		t.Fatalf("after the upload, Bob's copy is here: %d", s)
	}
	m := manifestOf(t, w.bob, w.list, photo, "photo")
	if len(m.Chunks) != 6 || onHub(w.blobs, m) != 6 {
		t.Fatalf("%d chunks, %d on the hub", len(m.Chunks), onHub(w.blobs, m))
	}
	syncAll(t, w.alice)
	if s := blobState(t, w.alice, w.list, photo, "photo"); s != BlobRemote {
		t.Fatalf("Alice sees it remote: %d", s)
	}
	if got := fetched(t, w.alice, w.list, photo, "photo"); !bytes.Equal(got, data) {
		t.Fatal("Alice's copy differs")
	}
	if s := blobState(t, w.alice, w.list, photo, "photo"); s != BlobHere {
		t.Fatalf("after fetching: %d", s)
	}
	// Small content is inline: here everywhere, no chunk.
	note := NewRecordID()
	before := len(w.blobs.Refs())
	must(t, w.bob.AttachBytes(bg, w.list, note, "note", []byte("hello, Alice")))
	syncAll(t, w.alice)
	if got := fetched(t, w.alice, w.list, note, "note"); string(got) != "hello, Alice" || len(w.blobs.Refs()) != before {
		t.Fatal("inline")
	}
}

// Review 1 and the third review's first finding: a pending replacement, and a committed one whose
// publish hasn't reached the feed, leave the published version whole.
func TestBlobPendingReplacementKeepsThePublishedVersion(t *testing.T) {
	w := newBlobWorld(t)
	photo, v1, v2 := NewRecordID(), content(3000), content(3200)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", v1))
	settle(t, w.bob)
	m1 := manifestOf(t, w.bob, w.list, photo, "photo")
	w.blobs.FailAppends = true
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", v2))
	settle(t, w.bob)
	if s := blobState(t, w.bob, w.list, photo, "photo"); s != BlobUploading {
		t.Fatalf("the replacement is pending: %d", s)
	}
	if got := manifestOf(t, w.bob, w.list, photo, "photo"); !bytes.Equal(got.Hash, m1.Hash) {
		t.Fatal("the replica must still hold the published version")
	}
	must(t, w.bob.SetField(bg, w.list, photo, "title", []byte("Beach")))
	clean(t, w.bob)
	if onHub(w.blobs, m1) != len(m1.Chunks) {
		t.Fatal("the published version's chunks were collected while its replacement was pending")
	}
	syncAll(t, w.alice)
	if got := manifestOf(t, w.alice, w.list, photo, "photo"); !bytes.Equal(got.Hash, m1.Hash) || value_(w.alice, w.list, photo, "title") != "Beach" {
		t.Fatal("a record message published meanwhile carries the old manifest")
	}
	w.blobs.FailAppends = false
	settle(t, w.bob)
	m2 := manifestOf(t, w.bob, w.list, photo, "photo")
	if bytes.Equal(m2.Hash, m1.Hash) {
		t.Fatal("not committed")
	}
	clean(t, w.bob)
	if onHub(w.blobs, m1) != 0 {
		t.Fatalf("once the replacement is on the feed the old chunks go: %d left", onHub(w.blobs, m1))
	}
	syncAll(t, w.alice)
	if got := fetched(t, w.alice, w.list, photo, "photo"); !bytes.Equal(got, v2) {
		t.Fatal("v2")
	}
}

// The third review's first finding, on its own: a committed replacement whose publish failed.
func TestBlobUnpublishedReplacementStaysProtected(t *testing.T) {
	hub, _, _, bob := found(t, "Bob’s Mac", nil)
	feed := &failingHub{PushStore: hub.Connect()}
	list := try(bob.CreateList(bg, "Photos"))
	blobs := NewMemoryBlobHub()
	must(t, bob.UseBlobs(blobs, smallBlobs))
	photo := NewRecordID()
	must(t, bob.AttachBytes(bg, list, photo, "photo", content(2500)))
	settle(t, bob)
	m1 := manifestOf(t, bob, list, photo, "photo")
	// Bob's feed goes away just as the replacement commits.
	bob.hub = feed
	feed.fail(true)
	must(t, bob.AttachBytes(bg, list, photo, "photo", content(2600)))
	settle(t, bob)
	m2 := manifestOf(t, bob, list, photo, "photo")
	if bytes.Equal(m1.Hash, m2.Hash) {
		t.Fatal("the replacement didn't commit")
	}
	bob.afterSync()
	settle(t, bob)
	if onHub(blobs, m1) != len(m1.Chunks) {
		t.Fatal("collected before the replacement was on the feed")
	}
	feed.fail(false)
	clean(t, bob) // the sync's healing publishes the record; then the old chunks may go
	clean(t, bob)
	if onHub(blobs, m1) != 0 {
		t.Fatalf("%d chunks of the replaced version left", onHub(blobs, m1))
	}
}

type failingHub struct {
	tandem.PushStore
	mu   sync.Mutex
	down bool
}

func (h *failingHub) fail(on bool) { h.mu.Lock(); h.down = on; h.mu.Unlock() }

func (h *failingHub) Append(ctx context.Context, channel string, body []byte) (string, error) {
	h.mu.Lock()
	down := h.down
	h.mu.Unlock()
	if down {
		return "", errors.New("hub: unreachable")
	}
	return h.PushStore.Append(ctx, channel, body)
}

// Review 2: junk claiming a chunk's address neither lets an upload skip it nor stops healing.
func TestBlobJunkClaimsAreSettled(t *testing.T) {
	w := newBlobWorld(t)
	tag, _ := w.bob.channelOf(w.list)
	data := content(2500)
	photo := NewRecordID()
	w.blobs.FailAppends = true
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	pending := w.pendingManifest(t, w.bob)
	junk := w.blobs.Inject(tag, pending.Chunks[0].Address, []byte("junk"))
	w.blobs.FailAppends = false
	settle(t, w.bob)
	got := manifestOf(t, w.bob, w.list, photo, "photo")
	if ids := w.blobs.WithAddress(got.Chunks[0].Address); len(ids) != 2 {
		t.Fatalf("the real chunk went up beside the junk: %v", ids)
	}
	// The real chunk destroyed and junk in its place: healing restores it.
	for _, id := range w.blobs.WithAddress(got.Chunks[1].Address) {
		w.blobs.Drop([]string{id})
	}
	w.blobs.Inject(tag, got.Chunks[1].Address, []byte("more junk"))
	recheck(w.bob)
	clean(t, w.bob)
	real := 0
	for _, id := range w.blobs.WithAddress(got.Chunks[1].Address) {
		if c := try(w.blobs.Fetch(bg, []string{id})); len(c) == 1 && bytes.Equal(tandem.ChunkAddress(c[0].Envelope), got.Chunks[1].Address) {
			real++
		}
	}
	if real != 1 {
		t.Fatalf("%d real copies after healing", real)
	}
	if len(w.blobs.WithAddress(got.Chunks[0].Address)) < 1 || !containsString(w.blobs.WithAddress(got.Chunks[0].Address), junk) {
		t.Fatal("a rejected message is never destroyed on that account")
	}
	syncAll(t, w.alice)
	if got := fetched(t, w.alice, w.list, photo, "photo"); !bytes.Equal(got, data) {
		t.Fatal("Alice reads past the junk")
	}
}

func containsString(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (w *blobWorld) pendingManifest(t *testing.T, d *Device) *tandem.Manifest {
	t.Helper()
	ups := try(d.store.Uploads())
	if len(ups) != 1 {
		t.Fatalf("%d uploads pending", len(ups))
	}
	_, m, ok := d.uploadOf(ups[0])
	if !ok {
		t.Fatal("upload")
	}
	return m
}

// Review 3: two devices healing the same chunk end with one copy, the lowest.
func TestBlobTwoHealersKeepOneCopy(t *testing.T) {
	w := newBlobWorld(t)
	photo, data := NewRecordID(), content(2500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	syncAll(t, w.alice)
	fetched(t, w.alice, w.list, photo, "photo")
	m := manifestOf(t, w.bob, w.list, photo, "photo")
	w.blobs.Drop(w.blobs.WithAddress(m.Chunks[1].Address))
	recheck(w.bob, w.alice)
	clean(t, w.bob, w.alice)
	clean(t, w.bob, w.alice)
	ids := w.blobs.WithAddress(m.Chunks[1].Address)
	if len(ids) != 1 {
		t.Fatalf("copies left: %v", ids)
	}
}

// The binary form was dropped; the form check moves a hub that damages the escaped form to base64.
func TestBlobFormCheckFallsBackToBase64(t *testing.T) {
	w := newBlobWorld(t)
	w.blobs.DamageEscaped = true
	photo, data := NewRecordID(), content(2500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	if w.bob.blobForm() != tandem.FormBase64 {
		t.Fatal("the hub stays on the escaped form")
	}
	if s := blobState(t, w.bob, w.list, photo, "photo"); s != BlobHere {
		t.Fatalf("the upload completes: %d", s)
	}
	syncAll(t, w.alice)
	if got := fetched(t, w.alice, w.list, photo, "photo"); !bytes.Equal(got, data) {
		t.Fatal("Alice reads it")
	}
}

// Orphans go only after this device has known them for the grace, by its own clock.
func TestBlobOrphansWaitForTheGrace(t *testing.T) {
	w := newBlobWorld(t)
	tag, _ := w.bob.channelOf(w.list)
	_, envs, err := tandem.Describe(random(32), w.bob.Pin().Hub, content(1500), 1000, 0)
	must(t, err)
	ids := try(w.blobs.Append(bg, []tandem.OutgoingChunk{{Channel: tag, Envelope: envs[0]}}, tandem.FormEscaped))
	// A device learns of chunks it never saw when what it lists changes: here, another file.
	must(t, w.bob.AttachBytes(bg, w.list, NewRecordID(), "photo", content(1500)))
	settle(t, w.bob)
	clean(t, w.bob)
	if len(w.blobs.WithAddress(tandem.ChunkAddress(envs[0]))) != 1 {
		t.Fatal("an orphan went before the grace")
	}
	w.bob.SetBlobSettings(BlobSettings{ChunkSize: 1000, Inline: 100, OrphanGrace: time.Millisecond})
	time.Sleep(5 * time.Millisecond) // allowed in tests: the grace is the subject
	clean(t, w.bob)
	if len(w.blobs.WithAddress(tandem.ChunkAddress(envs[0]))) != 0 {
		t.Fatalf("the orphan %v stays after the grace", ids)
	}
}

// The second review: a manifest whose lengths don't match its chunks is unreadable, and nothing of it is shown.
func TestBlobLengthMismatchIsUnreadable(t *testing.T) {
	w := newBlobWorld(t)
	tag, _ := w.bob.channelOf(w.list)
	data := content(1800)
	m, envs, err := tandem.Describe(random(32), w.bob.Pin().Hub, data, 1000, 0)
	must(t, err)
	try(w.blobs.Append(bg, []tandem.OutgoingChunk{{Channel: tag, Envelope: envs[0]}, {Channel: tag, Envelope: envs[1]}}, tandem.FormEscaped))
	m.Chunks[0].Length, m.Chunks[1].Length = m.Chunks[1].Length, m.Chunks[0].Length // same sum, same hash
	value := try(tandem.EncodeManifest(m))
	photo := NewRecordID()
	must(t, w.bob.Write(bg, w.list, photo, Set(BlobPrefix+"photo", value)))
	syncAll(t, w.alice)
	if _, err := w.alice.FetchBlob(bg, w.list, photo, "photo"); !errors.Is(err, ErrBlobUnreadable) {
		t.Fatalf("fetch: %v", err)
	}
	if s := blobState(t, w.alice, w.list, photo, "photo"); s != BlobUnreadable {
		t.Fatalf("state %d", s)
	}
	if b, _ := w.alice.Blob(w.list, photo, "photo"); b.Path != "" {
		t.Fatal("no path to a file that isn't whole")
	}
}

// The second review: a manifest of an unknown version stops every kind of clean-up in its list.
func TestBlobUnknownVersionStopsCleanup(t *testing.T) {
	w := newBlobWorld(t)
	photo := NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	settle(t, w.bob)
	m1 := manifestOf(t, w.bob, w.list, photo, "photo")
	must(t, w.bob.Write(bg, w.list, NewRecordID(), Set(BlobPrefix+"future", []byte(`{"blob":2,"chunks":"whatever"}`))))
	must(t, w.bob.Write(bg, w.list, photo, Set(BlobPrefix+"photo", try(tandem.EncodeManifest(&tandem.Manifest{Size: 1, Hash: tandem.H([]byte("x")), Data: []byte("x")})))))
	clean(t, w.bob)
	if onHub(w.blobs, m1) != len(m1.Chunks) {
		t.Fatal("collected beside a manifest this device can't read")
	}
	if _, kind := tandem.ParseManifest([]byte(`{"blob":"2"}`)); kind != tandem.ManifestNone {
		t.Fatal("a value that is no manifest blocks nothing")
	}
}

// A crash at each step of section 6 resumes and commits exactly one blob.
func TestBlobUploadResumesAfterARestart(t *testing.T) {
	path := t.TempDir() + "/hub.sqlite"
	store := try(OpenStore(path))
	hub, _, keys, bob := found(t, "Bob’s Mac", store)
	list := try(bob.CreateList(bg, "Photos"))
	blobs := NewMemoryBlobHub()
	blobs.FailAppends = true // signed and journalled, nothing up yet: then the app is killed
	must(t, bob.UseBlobs(blobs, smallBlobs))
	photo, data := NewRecordID(), content(4200)
	must(t, bob.AttachBytes(bg, list, photo, "photo", data))
	settle(t, bob)
	bob.Close()
	must(t, store.Close())
	blobs.FailAppends = false
	store = try(OpenStore(path))
	defer store.Close()
	again := try(Reopen(store, keys, hub.Connect()))
	defer again.Close()
	must(t, again.UseBlobs(blobs, smallBlobs))
	settle(t, again)
	m := manifestOf(t, again, list, photo, "photo")
	if onHub(blobs, m) != len(m.Chunks) || len(try(again.store.Uploads())) != 0 {
		t.Fatal("resumed and committed")
	}
	if got := fetched(t, again, list, photo, "photo"); !bytes.Equal(got, data) {
		t.Fatal("content")
	}
}

// An append whose answer was lost landed anyway: the resumed upload finds it and appends nothing twice.
func TestBlobLostAnswerAppendsNothingTwice(t *testing.T) {
	w := newBlobWorld(t)
	lossy := &lossyBlobs{MemoryBlobHub: w.blobs, lose: 1}
	w.bob.blobs.hub = lossy
	photo := NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	settle(t, w.bob)
	settle(t, w.bob)
	m := manifestOf(t, w.bob, w.list, photo, "photo")
	for _, e := range m.Chunks {
		if n := len(w.blobs.WithAddress(e.Address)); n != 1 {
			t.Fatalf("%d copies of a chunk", n)
		}
	}
}

type lossyBlobs struct {
	*MemoryBlobHub
	lose int
}

func (l *lossyBlobs) Append(ctx context.Context, chunks []tandem.OutgoingChunk, form tandem.ChunkForm) ([]string, error) {
	ids, err := l.MemoryBlobHub.Append(ctx, chunks, form)
	if err == nil && l.lose > 0 {
		l.lose--
		return nil, errors.New("hub: connection lost before the answer")
	}
	return ids, err
}

// While a blob is pending, other writes to the record go out; a field changed meanwhile ends as two siblings.
func TestBlobOtherWritesGoOutWhilePending(t *testing.T) {
	w := newBlobWorld(t)
	photo := NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(500)))
	syncAll(t, w.alice)
	w.blobs.FailAppends = true
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	must(t, w.bob.SetField(bg, w.list, photo, "title", []byte("Beach")))
	syncAll(t, w.alice)
	if value_(w.alice, w.list, photo, "title") != "Beach" {
		t.Fatal("the title went out while the blob was pending")
	}
	must(t, w.alice.AttachBytes(bg, w.list, photo, "photo", content(30))) // inline, at once
	syncAll(t, w.bob)
	w.blobs.FailAppends = false
	settle(t, w.bob)
	syncAll(t, w.alice)
	item, _ := w.alice.Item(w.list, photo)
	if n := len(item.Values(BlobPrefix + "photo")); n != 2 {
		t.Fatalf("%d siblings; the upload didn't see Alice's change, so two", n)
	}
}

// Cancelling, or deleting the record, drops the pending write; its chunks already up are collected.
func TestBlobCancelAndDeleteDropThePendingWrite(t *testing.T) {
	w := newBlobWorld(t)
	photo := NewRecordID()
	w.blobs.PartialAppends = 1 // one chunk of each batch lands, then the connection drops: the upload stays pending
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	round(t, w.bob)
	pending := w.pendingManifest(t, w.bob)
	if onHub(w.blobs, pending) == 0 {
		t.Fatal("a chunk went up")
	}
	w.bob.CancelAttach(w.list, photo, "photo")
	w.blobs.PartialAppends = 0
	if _, ok := w.bob.Blob(w.list, photo, "photo"); ok {
		t.Fatal("still shown")
	}
	clean(t, w.bob)
	if onHub(w.blobs, pending) != 0 {
		t.Fatalf("%d chunks of the cancelled upload left", onHub(w.blobs, pending))
	}
	// Deleted before the commit.
	w.blobs.FailAppends = true
	other := NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, other, "photo", content(2500)))
	must(t, w.bob.DeleteRecord(bg, w.list, other))
	w.blobs.FailAppends = false
	settle(t, w.bob)
	if len(try(w.bob.store.Uploads())) != 0 {
		t.Fatal("the pending write of a deleted record is dropped")
	}
}

// A key change carries committed blobs into the new epoch without a single chunk going up.
func TestBlobKeyChangeUploadsNothing(t *testing.T) {
	w := newBlobWorld(t)
	photo, data := NewRecordID(), content(2500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	before := w.blobs.Appends()
	must(t, w.bob.RemoveMember(bg, me(t, w.alice).Participant, w.list))
	clean(t, w.bob)
	if w.blobs.Appends() != before {
		t.Fatal("chunks went up on a key change")
	}
	carol := join(t, w.bob, w.f, w.hub, "Carol", nil)
	must(t, carol.UseBlobs(w.blobs, smallBlobs))
	syncAll(t, w.bob)
	must(t, w.bob.Share(bg, w.list, me(t, carol).Participant, Reader, "Carol"))
	syncAll(t, carol)
	must(t, carol.Accept(bg, w.list))
	if got := fetched(t, carol, w.list, photo, "photo"); !bytes.Equal(got, data) {
		t.Fatal("a member added after the key change reads the file")
	}
}

// Clean-up: chunks of a replaced or deleted file go; another list's chunks, and protected ones, stay.
func TestBlobCleanupCollectsWhatNothingProtects(t *testing.T) {
	w := newBlobWorld(t)
	photo, doc := NewRecordID(), NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	must(t, w.bob.AttachBytes(bg, w.list, doc, "file", content(1500)))
	settle(t, w.bob)
	m1 := manifestOf(t, w.bob, w.list, photo, "photo")
	md := manifestOf(t, w.bob, w.list, doc, "file")
	other := try(w.bob.CreateList(bg, "Other"))
	o := NewRecordID()
	must(t, w.bob.AttachBytes(bg, other, o, "photo", content(1500)))
	settle(t, w.bob)
	mo := manifestOf(t, w.bob, other, o, "photo")
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2600)))
	must(t, w.bob.DeleteRecord(bg, w.list, doc))
	settle(t, w.bob)
	clean(t, w.bob)
	if onHub(w.blobs, m1) != 0 || onHub(w.blobs, md) != 0 {
		t.Fatalf("replaced %d, deleted %d left", onHub(w.blobs, m1), onHub(w.blobs, md))
	}
	if onHub(w.blobs, mo) != len(mo.Chunks) {
		t.Fatal("another list's chunks were touched")
	}
	m2 := manifestOf(t, w.bob, w.list, photo, "photo")
	if onHub(w.blobs, m2) != len(m2.Chunks) {
		t.Fatal("the live version's chunks were collected")
	}
}

// The second review: a manifest kept under a tombstone is collected and never healed.
func TestBlobManifestUnderATombstoneIsNotHealed(t *testing.T) {
	w := newBlobWorld(t)
	photo := NewRecordID()
	must(t, w.bob.SetField(bg, w.list, photo, "title", []byte("x")))
	syncAll(t, w.alice)
	must(t, w.alice.AttachBytes(bg, w.list, photo, "photo", content(2500))) // without having seen the delete
	must(t, w.bob.DeleteRecord(bg, w.list, photo))
	settle(t, w.alice)
	ups := try(w.alice.store.Uploads())
	if len(ups) != 0 {
		t.Fatal("Alice's upload committed")
	}
	syncAll(t, w.alice, w.bob)
	var m *tandem.Manifest
	for _, row := range try(w.bob.store.Writes(w.list, photo)) {
		if wr := storedWrite(row.Payload, w.bob.Pin().Hub, w.list); wr != nil && wr.Field == BlobPrefix+"photo" {
			m, _ = tandem.ParseManifest(wr.Value)
		}
	}
	if m == nil {
		t.Fatal("the manifest reached Bob")
	}
	for range 3 {
		recheck(w.alice, w.bob)
		clean(t, w.alice, w.bob)
	}
	if onHub(w.blobs, m) != 0 {
		t.Fatalf("%d chunks of a manifest under a tombstone left", onHub(w.blobs, m))
	}
}

// Healing: a destroyed chunk comes back byte for byte from a device that holds the file; one
// that doesn't reports it unavailable and leaves the record alone.
func TestBlobHealingRestoresAChunk(t *testing.T) {
	w := newBlobWorld(t)
	photo, data := NewRecordID(), content(2500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	m := manifestOf(t, w.bob, w.list, photo, "photo")
	w.blobs.Drop(w.blobs.WithAddress(m.Chunks[2].Address))
	syncAll(t, w.alice)
	if _, err := w.alice.FetchBlob(bg, w.list, photo, "photo"); !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("fetch: %v", err)
	}
	if s := blobState(t, w.alice, w.list, photo, "photo"); s != BlobUnavailable {
		t.Fatalf("state %d", s)
	}
	if value_(w.alice, w.list, photo, BlobPrefix+"photo") == "" {
		t.Fatal("the record is untouched")
	}
	recheck(w.bob)
	clean(t, w.bob)
	if got := fetched(t, w.alice, w.list, photo, "photo"); !bytes.Equal(got, data) {
		t.Fatal("restored byte for byte")
	}
}

// Manifests a reader must take from day one: mixed lengths, a repeated address, one address in two manifests.
func TestBlobManifestsOfAnyShapeRead(t *testing.T) {
	w := newBlobWorld(t)
	tag, _ := w.bob.channelOf(w.list)
	hub, r := w.bob.Pin().Hub, random(32)
	a := content(700)
	keyA, _ := tandem.ChunkKey(r, a)
	envA := try(tandem.SealChunk(keyA, hub, a))
	b := content(300)
	keyB, _ := tandem.ChunkKey(r, b)
	envB := try(tandem.SealChunk(keyB, hub, b))
	try(w.blobs.Append(bg, []tandem.OutgoingChunk{{Channel: tag, Envelope: envA}, {Channel: tag, Envelope: envB}}, tandem.FormEscaped))
	ea := tandem.ChunkEntry{Address: tandem.ChunkAddress(envA), Key: keyA, Length: 700}
	eb := tandem.ChunkEntry{Address: tandem.ChunkAddress(envB), Key: keyB, Length: 300}
	whole := append(append(append([]byte{}, a...), b...), a...)
	one := try(tandem.EncodeManifest(&tandem.Manifest{Size: int64(len(whole)), Hash: tandem.H(whole), Chunks: []tandem.ChunkEntry{ea, eb, ea}}))
	two := try(tandem.EncodeManifest(&tandem.Manifest{Size: 300, Hash: tandem.H(b), Chunks: []tandem.ChunkEntry{eb}}))
	x, y := NewRecordID(), NewRecordID()
	must(t, w.bob.Write(bg, w.list, x, Set(BlobPrefix+"f", one)))
	must(t, w.bob.Write(bg, w.list, y, Set(BlobPrefix+"f", two)))
	syncAll(t, w.alice)
	if got := fetched(t, w.alice, w.list, x, "f"); !bytes.Equal(got, whole) {
		t.Fatal("mixed lengths and a repeated address")
	}
	if got := fetched(t, w.alice, w.list, y, "f"); !bytes.Equal(got, b) {
		t.Fatal("an address in two manifests")
	}
}

func TestBlobQuotaAndAFullHub(t *testing.T) {
	w := newBlobWorld(t)
	if q := try(w.bob.BlobQuota(bg)); q.Known {
		t.Fatal("unknown, never zero")
	}
	w.blobs.QuotaReport = tandem.Quota{Known: true, Used: 10, Limit: 100}
	if q := try(w.bob.BlobQuota(bg)); !q.Known || q.Limit != 100 {
		t.Fatal(q)
	}
	w.blobs.FailAppends = true
	photo := NewRecordID()
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", content(2500)))
	settle(t, w.bob)
	if len(try(w.bob.store.Uploads())) != 1 {
		t.Fatal("a full hub leaves the upload in the journal")
	}
	w.blobs.FailAppends = false
	settle(t, w.bob)
	if s := blobState(t, w.bob, w.list, photo, "photo"); s != BlobHere {
		t.Fatalf("resumed: %d", s)
	}
}

// The fourth review: the unknown-version guard holds when such a manifest is protected only
// because this device's replacement of it is not on the feed yet.
func TestBlobUnknownVersionProtectedByTheFeedStopsCleanup(t *testing.T) {
	hub, _, _, bob := found(t, "Bob’s Mac", nil)
	feed := &failingHub{PushStore: hub.Connect()}
	list := try(bob.CreateList(bg, "Photos"))
	blobs := NewMemoryBlobHub()
	must(t, bob.UseBlobs(blobs, smallBlobs))
	photo, old := NewRecordID(), NewRecordID()
	must(t, bob.AttachBytes(bg, list, old, "photo", content(2500)))
	settle(t, bob)
	m1 := manifestOf(t, bob, list, old, "photo")
	must(t, bob.Write(bg, list, old, Set(BlobPrefix+"photo", []byte(`{"blob":1,"size":1,"hash":"`+tandem.B64u(tandem.H([]byte("x")))+`","data":"eA"}`))))
	must(t, bob.Write(bg, list, photo, Set(BlobPrefix+"f", []byte(`{"blob":2}`))))
	bob.hub = feed
	feed.fail(true)
	must(t, bob.Write(bg, list, photo, Set(BlobPrefix+"f", []byte(`{"blob":1,"size":1,"hash":"`+tandem.B64u(tandem.H([]byte("y")))+`","data":"eQ"}`))))
	bob.afterSync()
	settle(t, bob)
	if onHub(blobs, m1) != len(m1.Chunks) {
		t.Fatal("collected while an unknown-version manifest was still on the feed")
	}
	feed.fail(false)
}

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Found by interop on Stalwart: the worker let the hub's connections go at the end of its round while a
// download was still using them. A download in flight keeps them.
func TestBlobDownloadSurvivesTheWorkersRelease(t *testing.T) {
	w := newBlobWorld(t)
	photo, data := NewRecordID(), content(2500)
	must(t, w.bob.AttachBytes(bg, w.list, photo, "photo", data))
	settle(t, w.bob)
	syncAll(t, w.alice)
	gated := &gatedBlobs{MemoryBlobHub: w.blobs, entered: make(chan struct{}, 1), gate: make(chan struct{})}
	w.alice.blobs.hub = gated
	got := make(chan error, 1)
	go func() {
		_, err := w.alice.FetchBlob(bg, w.list, photo, "photo")
		got <- err
	}()
	<-gated.entered // the download is in a fetch
	gated.armed.Store(true)
	w.alice.afterSync()
	settle(t, w.alice) // a whole worker round, which ends by letting connections go if nobody uses them
	close(gated.gate)
	if err := <-got; err != nil {
		t.Fatalf("the download broke: %v", err)
	}
}

type gatedBlobs struct {
	*MemoryBlobHub
	entered    chan struct{}
	gate       chan struct{}
	armed      atomic.Bool
	inFlight   atomic.Int32
	brokenOnce atomic.Bool
}

func (g *gatedBlobs) Fetch(ctx context.Context, ids []string) ([]tandem.Chunk, error) {
	g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	select {
	case g.entered <- struct{}{}:
		<-g.gate
	default:
	}
	if g.brokenOnce.Load() {
		return nil, errors.New("imap: store closed")
	}
	return g.MemoryBlobHub.Fetch(ctx, ids)
}

func (g *gatedBlobs) Release() {
	if g.armed.Load() && g.inFlight.Load() > 0 {
		g.brokenOnce.Store(true) // what closing a connection under a command does
	}
	g.MemoryBlobHub.Release()
}
