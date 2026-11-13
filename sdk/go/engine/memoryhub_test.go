package engine

import (
	"context"
	"slices"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

func TestMemoryHubPushChangesAndRemovals(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()
	a, b := hub.Connect(), hub.Connect()
	wakes, _ := b.Watch(ctx)
	id, _ := a.Append(ctx, "x", []byte{1, 2})
	<-wakes
	batch, _ := b.Changes(ctx, "")
	if len(batch.Added) != 1 || batch.Added[0].ID != id || string(batch.Added[0].Body) != "\x01\x02" {
		t.Fatal(batch)
	}
	must(t, a.Destroy(ctx, []string{id}))
	<-wakes
	batch, _ = b.Changes(ctx, batch.Cursor)
	if !slices.Equal(batch.Removed, []string{id}) || len(batch.Added) != 0 {
		t.Fatal(batch)
	}
	if fromStart, _ := b.Changes(ctx, ""); len(fromStart.Added) != 0 {
		t.Fatal("a destroyed message is not listed again")
	}
	b.Close()
	if _, ok := <-wakes; ok {
		t.Fatal("closed")
	}
}

func TestMemoryHubThatHidesAndDrops(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()
	a, b := hub.Connect(), hub.Connect()
	hub.Hide("secret", b)
	a.Append(ctx, "secret", []byte{1})
	open, _ := a.Append(ctx, "open", []byte{2})
	if seen, _ := b.Changes(ctx, ""); len(seen.Added) != 1 || seen.Added[0].Channel != "open" {
		t.Fatal(seen)
	}
	if seen, _ := a.Changes(ctx, ""); len(seen.Added) != 2 {
		t.Fatal(seen)
	}
	hub.Drop([]string{open})
	if seen, _ := b.Changes(ctx, ""); len(seen.Added) != 0 {
		t.Fatal(seen)
	}
	if _, err := b.Changes(ctx, "99"); err == nil {
		t.Fatal("a cursor past the end")
	}
}

func TestMemoryHubRunsTheSessionPump(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hub := NewMemoryHub()
	s, err := tandem.NewSession(hub.Connect(), "", 8)
	must(t, err)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	id, err := s.Submit(ctx, "c", []byte{9})
	must(t, err)
	seen := false
	for batch := range s.Updates() {
		for _, m := range batch.Added {
			seen = seen || m.ID == id
		}
		if seen {
			break
		}
	}
	cancel()
	<-done
	if !seen {
		t.Fatal("not delivered")
	}
}
