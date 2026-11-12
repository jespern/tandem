package store

import (
	"context"
	"strconv"
	"sync"
)

// Memory is an in-memory hub for fast, deterministic tests: many clients
// share one MemoryHub, each with its own change cursor. It behaves like a
// well-behaved server; hostile-hub behaviour is the testbed's job.
type MemoryHub struct {
	mu      sync.Mutex
	next    int
	msgs    map[string]Message
	log     []memEvent // every append and destroy, in order
	waiters []chan struct{}
}

type memEvent struct {
	id    string
	added bool
}

func NewMemoryHub() *MemoryHub { return &MemoryHub{msgs: map[string]Message{}} }

// Client opens a new connection to the hub.
func (h *MemoryHub) Client() *Memory { return &Memory{h: h} }

// Len is the number of messages on the hub.
func (h *MemoryHub) Len() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.msgs) }

// Messages returns a copy of everything on the hub.
func (h *MemoryHub) Messages() map[string]Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]Message, len(h.msgs))
	for k, v := range h.msgs {
		out[k] = v
	}
	return out
}

func (h *MemoryHub) notify() {
	for _, w := range h.waiters {
		signal(w)
	}
}

type Memory struct {
	h      *MemoryHub
	cursor int
}

func (m *Memory) EnsureFolder(context.Context) error { return nil }

func (m *Memory) Append(_ context.Context, tag string, env []byte) (string, error) {
	h := m.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := strconv.Itoa(h.next)
	h.msgs[id] = Message{Tag: tag, Envelope: append([]byte(nil), env...)}
	h.log = append(h.log, memEvent{id, true})
	h.notify()
	return id, nil
}

func (m *Memory) Changes(context.Context) ([]Ref, []string, error) {
	h := m.h
	h.mu.Lock()
	defer h.mu.Unlock()
	var added []Ref
	var removed []string
	for _, e := range h.log[m.cursor:] {
		if e.added {
			if msg, ok := h.msgs[e.id]; ok {
				added = append(added, Ref{e.id, msg.Tag})
			}
		} else {
			removed = append(removed, e.id)
		}
	}
	m.cursor = len(h.log)
	return added, removed, nil
}

func (m *Memory) Fetch(_ context.Context, ids []string) (map[string]Message, error) {
	h := m.h
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]Message{}
	for _, id := range ids {
		if msg, ok := h.msgs[id]; ok {
			out[id] = msg
		}
	}
	return out, nil
}

func (m *Memory) Destroy(_ context.Context, ids []string) error {
	h := m.h
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		if _, ok := h.msgs[id]; ok {
			delete(h.msgs, id)
			h.log = append(h.log, memEvent{id, false})
		}
	}
	h.notify()
	return nil
}

func (m *Memory) Watch(ctx context.Context, onChange func()) error {
	ch := make(chan struct{}, 1)
	m.h.mu.Lock()
	m.h.waiters = append(m.h.waiters, ch)
	m.h.mu.Unlock()
	onChange()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			onChange()
		}
	}
}

func (m *Memory) Close() error { return nil }
