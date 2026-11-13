package tandem

import (
	"context"
	"errors"
	"io"
	"sync"
)

// Message and ChangeBatch describe envelope delivery, before protocol validation.
type Message struct {
	ID, Channel string
	Body        []byte
}
type ChangeBatch struct {
	Added   []Message
	Removed []string
	Cursor  string
}
type Wake struct{ Err error }

// PushStore.Watch returns only once the live subscription is active. It buffers
// invalidations until read, and emits one after reconnecting. Changes reconciles
// from a cursor; it is called only at startup, after push/reconnect, or after Append.
// Implementations serialize IMAP commands with IDLE/DONE and resume IDLE afterwards.
type PushStore interface {
	Watch(context.Context) (<-chan Wake, error)
	Changes(context.Context, string) (ChangeBatch, error)
	Append(context.Context, string, []byte) (string, error)
	Close() error
}
type submission struct {
	ctx     context.Context
	channel string
	body    []byte
	result  chan submitResult
}
type submitResult struct {
	id  string
	err error
}

// Session is an event-driven envelope pump. The caller owns durable storage,
// exact-byte retry, device authorization and applying changes to records.
type Session struct {
	store   PushStore
	updates chan ChangeBatch
	pending chan submission
	done    chan struct{}
	once    sync.Once
	cursor  string
}

// NewSession bounds pending submissions and delivered batches by capacity.
func NewSession(store PushStore, cursor string, capacity int) (*Session, error) {
	if capacity < 1 {
		return nil, errors.New("tandem: session capacity must be positive")
	}
	return &Session{store: store, cursor: cursor, updates: make(chan ChangeBatch, capacity), pending: make(chan submission, capacity), done: make(chan struct{})}, nil
}
func (s *Session) Updates() <-chan ChangeBatch { return s.updates }

// Submit copies the envelope before queueing it. Cancellation does not promise
// the mailbox did not receive a command; retry persisted, identical bytes.
func (s *Session) Submit(ctx context.Context, channel string, body []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	req := submission{ctx, channel, append([]byte(nil), body...), make(chan submitResult, 1)}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", io.ErrClosedPipe
	case s.pending <- req:
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", io.ErrClosedPipe
	case result := <-req.result:
		return result.id, result.err
	}
}

// Run holds the subscription open until cancellation or a transport error. There
// are no tickers, sleep loops or periodic network requests. Call Run once.
func (s *Session) Run(ctx context.Context) error {
	started := false
	s.once.Do(func() { started = true })
	if !started {
		return errors.New("tandem: session already started")
	}
	defer close(s.done)
	defer close(s.updates)
	defer s.store.Close()
	wakes, err := s.store.Watch(ctx)
	if err != nil {
		return err
	}
	reconcile := func() error {
		batch, e := s.store.Changes(ctx, s.cursor)
		if e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case s.updates <- batch:
			s.cursor = batch.Cursor
			return nil
		}
	}
	if err = reconcile(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case wake, ok := <-wakes:
			if !ok {
				return io.EOF
			}
			if wake.Err != nil {
				return wake.Err
			}
			if err = reconcile(); err != nil {
				return err
			}
		case req := <-s.pending:
			callCtx, cancel := context.WithCancel(req.ctx)
			stop := context.AfterFunc(ctx, cancel)
			id, e := s.store.Append(callCtx, req.channel, req.body)
			stop()
			cancel()
			req.result <- submitResult{id, e}
			if e != nil {
				continue
			}
			if err = reconcile(); err != nil {
				return err
			}
		}
	}
}
