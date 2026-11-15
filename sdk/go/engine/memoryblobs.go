package engine

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"

	tandem "github.com/jespern/tandem/sdk/go"
)

// MemoryBlobHub is a blob folder in memory, shared by every device of a test (DESIGN.md§17.). Like a credential holder, a test can drop chunks, append junk under any claim, make the
// escaped form come back damaged, or set a quota.
type MemoryBlobHub struct {
	// PageSize, if set, makes Changes return at most this many events per call, with More.
	PageSize int
	// DamageEscaped makes every chunk appended in the escaped form come back altered.
	DamageEscaped bool
	// QuotaReport is what Quota returns.
	QuotaReport tandem.Quota
	// FailAppends makes Append fail as an over-quota server does.
	FailAppends bool
	// PartialAppends, if set, stores only that many chunks of each Append, then fails it.
	PartialAppends int

	mu       sync.Mutex
	events   []blobEvent
	live     map[string]memoryChunk
	serial   int
	appends  int
	releases int
}

type memoryChunk struct {
	ref  tandem.BlobRef
	body []byte // what a fetch returns
	form tandem.ChunkForm
}

type blobEvent struct {
	added   *tandem.BlobRef
	removed string
}

func NewMemoryBlobHub() *MemoryBlobHub { return &MemoryBlobHub{live: map[string]memoryChunk{}} }

var _ tandem.BlobHub = (*MemoryBlobHub)(nil)

// ErrOverQuota is what Append returns while FailAppends is set.
var ErrOverQuota = errors.New("hub: over quota")

func (h *MemoryBlobHub) Changes(_ context.Context, cursor string) (tandem.BlobChanges, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(h.events) {
			return tandem.BlobChanges{}, errors.New("hub: bad cursor")
		}
		start = n
	}
	var out tandem.BlobChanges
	end := len(h.events)
	if h.PageSize > 0 && end-start > h.PageSize {
		end, out.More = start+h.PageSize, true
	}
	for _, e := range h.events[start:end] {
		if e.added != nil {
			if _, ok := h.live[e.added.ID]; ok {
				out.Added = append(out.Added, tandem.BlobRef{ID: e.added.ID, Channel: e.added.Channel, Address: bytes.Clone(e.added.Address)})
			}
		} else {
			out.Removed = append(out.Removed, e.removed)
		}
	}
	out.Cursor = strconv.Itoa(end)
	return out, nil
}

func (h *MemoryBlobHub) add(channel string, address, body []byte, form tandem.ChunkForm) string {
	h.serial++
	ref := tandem.BlobRef{ID: strconv.Itoa(h.serial), Channel: channel, Address: bytes.Clone(address)}
	h.live[ref.ID] = memoryChunk{ref: ref, body: bytes.Clone(body), form: form}
	h.events = append(h.events, blobEvent{added: &ref})
	return ref.ID
}

func (h *MemoryBlobHub) Append(_ context.Context, chunks []tandem.OutgoingChunk, form tandem.ChunkForm) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.FailAppends {
		return nil, ErrOverQuota
	}
	h.appends++
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		if h.PartialAppends > 0 && i >= h.PartialAppends {
			return nil, errors.New("hub: connection lost")
		}
		body := c.Envelope
		if form == tandem.FormEscaped && h.DamageEscaped {
			body = append(bytes.Clone(body[:len(body)-1]), body[len(body)-1]^0x20)
		}
		ids[i] = h.add(c.Channel, tandem.ChunkAddress(c.Envelope), body, form)
	}
	return ids, nil
}

func (h *MemoryBlobHub) Fetch(_ context.Context, ids []string) ([]tandem.Chunk, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []tandem.Chunk
	for _, id := range ids {
		if c, ok := h.live[id]; ok {
			out = append(out, tandem.Chunk{ID: id, Envelope: bytes.Clone(c.body)})
		}
	}
	return out, nil
}

func (h *MemoryBlobHub) Destroy(_ context.Context, ids []string) error {
	h.Drop(ids)
	return nil
}

func (h *MemoryBlobHub) Quota(context.Context) (tandem.Quota, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.QuotaReport, nil
}

func (h *MemoryBlobHub) Release() {
	h.mu.Lock()
	h.releases++
	h.mu.Unlock()
}

// Drop deletes chunk messages as a credential holder could.
func (h *MemoryBlobHub) Drop(ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		if _, ok := h.live[id]; ok {
			delete(h.live, id)
			h.events = append(h.events, blobEvent{removed: id})
		}
	}
}

// Inject appends a message claiming any address with any body, as anyone with the credential can.
func (h *MemoryBlobHub) Inject(channel string, address, body []byte) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.add(channel, address, body, tandem.FormEscaped)
}

// Refs is every chunk message on the hub, oldest first.
func (h *MemoryBlobHub) Refs() []tandem.BlobRef {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []tandem.BlobRef
	for _, e := range h.events {
		if e.added != nil {
			if c, ok := h.live[e.added.ID]; ok {
				out = append(out, c.ref)
			}
		}
	}
	return out
}

// WithAddress is the IDs of the messages claiming an address, oldest first.
func (h *MemoryBlobHub) WithAddress(address []byte) []string {
	var out []string
	for _, r := range h.Refs() {
		if bytes.Equal(r.Address, address) {
			out = append(out, r.ID)
		}
	}
	return out
}

// Appends is how many Append calls succeeded; Releases how many times Release was called.
func (h *MemoryBlobHub) Appends() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.appends
}

func (h *MemoryBlobHub) Releases() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.releases
}
