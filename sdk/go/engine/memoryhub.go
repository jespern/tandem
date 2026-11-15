package engine

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"

	tandem "github.com/jespern/tandem/sdk/go"
)

// MemoryHub is one mailbox folder in memory, shared by any number of devices: what the
// engine's tests run on. Each device gets its own connection (Connect), which pushes on
// every change the way IDLE does (§10.3). The hub can also misbehave on purpose, as §3.1
// allows a hub to: hide messages from one connection, or drop them for everyone.
type MemoryHub struct {
	// PageSize, if set, makes Changes return at most this many events per call, with More.
	PageSize int

	mu       sync.Mutex
	events   []hubEvent
	live     map[string]tandem.Message
	serial   int
	watchers map[int]chan tandem.Wake
	conns    int
	hidden   map[int]map[string]bool
}

type hubEvent struct {
	added   *tandem.Message
	removed string
}

func NewMemoryHub() *MemoryHub {
	return &MemoryHub{live: map[string]tandem.Message{}, watchers: map[int]chan tandem.Wake{}, hidden: map[int]map[string]bool{}}
}

// Connect is a connection for one device.
func (h *MemoryHub) Connect() *MemoryConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns++
	return &MemoryConn{hub: h, number: h.conns}
}

// Messages is every message currently on the hub, oldest first.
func (h *MemoryHub) Messages() []tandem.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []tandem.Message
	for _, e := range h.events {
		if e.added != nil {
			if _, ok := h.live[e.added.ID]; ok {
				out = append(out, copyMessage(*e.added))
			}
		}
	}
	return out
}

func copyMessage(m tandem.Message) tandem.Message {
	return tandem.Message{ID: m.ID, Channel: m.Channel, Body: bytes.Clone(m.Body)}
}

// Hide withholds existing and future messages of a channel from one connection.
func (h *MemoryHub) Hide(channel string, from *MemoryConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hidden[from.number] == nil {
		h.hidden[from.number] = map[string]bool{}
	}
	h.hidden[from.number][channel] = true
}

// Drop deletes messages as a credential holder could, telling every connection.
func (h *MemoryHub) Drop(ids []string) { h.remove(ids) }

func (h *MemoryHub) remove(ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	any := false
	for _, id := range ids {
		if _, ok := h.live[id]; ok {
			delete(h.live, id)
			h.events = append(h.events, hubEvent{removed: id})
			any = true
		}
	}
	if any {
		h.notify()
	}
}

func (h *MemoryHub) notify() {
	for _, w := range h.watchers {
		select {
		case w <- tandem.Wake{}:
		default: // one pending wake is enough
		}
	}
}

// MemoryConn is one device's connection to a MemoryHub: a tandem.PushStore.
type MemoryConn struct {
	hub    *MemoryHub
	number int
}

var _ tandem.PushStore = (*MemoryConn)(nil)

func (c *MemoryConn) Watch(ctx context.Context) (<-chan tandem.Wake, error) {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.watchers[c.number]; ok {
		delete(h.watchers, c.number)
		close(old)
	}
	ch := make(chan tandem.Wake, 1)
	h.watchers[c.number] = ch
	context.AfterFunc(ctx, func() { c.unwatch(ch) })
	return ch, nil
}

func (c *MemoryConn) unwatch(ch chan tandem.Wake) {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watchers[c.number] == ch {
		delete(h.watchers, c.number)
		close(ch)
	}
}

func (c *MemoryConn) Changes(_ context.Context, cursor string) (tandem.ChangeBatch, error) {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(h.events) {
			return tandem.ChangeBatch{}, errors.New("hub: bad cursor")
		}
		start = n
	}
	var batch tandem.ChangeBatch
	hide := h.hidden[c.number]
	end := len(h.events)
	if h.PageSize > 0 && end-start > h.PageSize {
		end, batch.More = start+h.PageSize, true
	}
	for _, e := range h.events[start:end] {
		if e.added != nil {
			if _, ok := h.live[e.added.ID]; ok && !hide[e.added.Channel] {
				batch.Added = append(batch.Added, copyMessage(*e.added))
			}
		} else {
			batch.Removed = append(batch.Removed, e.removed)
		}
	}
	batch.Cursor = strconv.Itoa(end)
	return batch, nil
}

func (c *MemoryConn) Append(_ context.Context, channel string, body []byte) (string, error) {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serial++
	m := tandem.Message{ID: strconv.Itoa(h.serial), Channel: channel, Body: bytes.Clone(body)}
	h.live[m.ID] = m
	h.events = append(h.events, hubEvent{added: &m})
	h.notify()
	return m.ID, nil
}

func (c *MemoryConn) Fetch(_ context.Context, ids []string) ([]tandem.Message, error) {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []tandem.Message
	for _, id := range ids {
		if m, ok := h.live[id]; ok && !h.hidden[c.number][m.Channel] {
			out = append(out, copyMessage(m))
		}
	}
	return out, nil
}

func (c *MemoryConn) Destroy(_ context.Context, ids []string) error {
	c.hub.remove(ids)
	return nil
}

func (c *MemoryConn) Close() error {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if w, ok := h.watchers[c.number]; ok {
		delete(h.watchers, c.number)
		close(w)
	}
	return nil
}
