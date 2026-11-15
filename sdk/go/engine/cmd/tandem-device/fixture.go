package main

// Test-only faults around real IMAP stores. A lost answer is injected only after
// the server accepted the bytes. No production engine behavior is replaced.
import (
	"context"
	"errors"
	tandem "github.com/jespern/tandem/sdk/go"
	"sync"
)

var errInjected = errors.New("injected transport interruption")

type fixtureFeed struct {
	tandem.PushStore
	mu      sync.Mutex
	offline bool
	ready   chan struct{}
	once    sync.Once
}

func (f *fixtureFeed) setOffline(v bool) { f.mu.Lock(); f.offline = v; f.mu.Unlock() }
func (f *fixtureFeed) check() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return errInjected
	}
	return nil
}
func (f *fixtureFeed) Watch(ctx context.Context) (<-chan tandem.Wake, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	ch, err := f.PushStore.Watch(ctx)
	if err == nil {
		f.once.Do(func() { close(f.ready) })
	}
	return ch, err
}
func (f *fixtureFeed) Changes(ctx context.Context, c string) (tandem.ChangeBatch, error) {
	if err := f.check(); err != nil {
		return tandem.ChangeBatch{}, err
	}
	return f.PushStore.Changes(ctx, c)
}
func (f *fixtureFeed) Append(ctx context.Context, c string, b []byte) (string, error) {
	if err := f.check(); err != nil {
		return "", err
	}
	return f.PushStore.Append(ctx, c, b)
}
func (f *fixtureFeed) Fetch(ctx context.Context, ids []string) ([]tandem.Message, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.PushStore.Fetch(ctx, ids)
}
func (f *fixtureFeed) Destroy(ctx context.Context, ids []string) error {
	if err := f.check(); err != nil {
		return err
	}
	return f.PushStore.Destroy(ctx, ids)
}

type fixtureBlobs struct {
	tandem.BlobHub
	mu                   sync.Mutex
	armed, blocked, lost bool
	remaining, stored    int
}

func (f *fixtureBlobs) fault(n int, lost bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed = true
	f.blocked = n == 0
	f.remaining = n
	f.lost = lost
	f.stored = 0
}
func (f *fixtureBlobs) clear()     { f.mu.Lock(); f.armed = false; f.blocked = false; f.mu.Unlock() }
func (f *fixtureBlobs) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.stored }
func (f *fixtureBlobs) Append(ctx context.Context, chunks []tandem.OutgoingChunk, form tandem.ChunkForm) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked {
		return nil, errInjected
	}
	if !f.armed {
		return f.BlobHub.Append(ctx, chunks, form)
	}
	n := min(f.remaining, len(chunks))
	ids, err := f.BlobHub.Append(ctx, chunks[:n], form)
	if err != nil {
		return nil, err
	}
	f.stored += n
	f.remaining -= n
	if f.remaining == 0 {
		f.blocked = true
		if f.lost || n < len(chunks) {
			return nil, errInjected
		}
	}
	return ids, nil
}
