package engine

import (
	"bytes"
	"context"
	"io"
	"slices"
	"sync"

	tandem "github.com/jespern/tandem/sdk/go"
)

// EventKind says what changed, for the app to refresh (§13). Coarse on purpose: each one
// says which getter to read again.
type EventKind uint8

const (
	// EventLists: Lists(): lists added, removed, renamed, or their roles changed.
	EventLists EventKind = iota + 1
	// EventItems: Items(list): these records of a list changed.
	EventItems
	// EventMembers: Members(list) and MemberStatus(list).
	EventMembers
	// EventOffers: PendingOffers().
	EventOffers
	// EventClaims: PendingClaims(), on an admin's device.
	EventClaims
	// EventPeople: People(): names, devices, admissions or revocations.
	EventPeople
	// EventHome: home records: HomeValue, the app's settings: records.
	EventHome
	// EventApproved: this device was waiting for the admin and is now on the hub.
	EventApproved
	// EventRevoked: this device's keychain was revoked: it reads what it has and writes nothing.
	EventRevoked
	// EventSyncFailed: a sync failed; the next push or Sync tries again. Message is for logs.
	EventSyncFailed
	// EventPublished: this device put something on the hub: the cue to send a wake hint.
	EventPublished
)

// Event is one change. List and Records are set for EventItems and EventMembers.
type Event struct {
	Kind    EventKind
	List    []byte
	Records [][]byte
	Message string
}

func (e Event) same(o Event) bool {
	return e.Kind == o.Kind && bytes.Equal(e.List, o.List) && e.Message == o.Message
}

// subscriber delivers events in order without ever blocking the device.
type subscriber struct {
	mu     sync.Mutex
	queue  []Event
	signal chan struct{}
	out    chan Event
	done   chan struct{}
	once   sync.Once
}

func (s *subscriber) push(es []Event) {
	s.mu.Lock()
	s.queue = append(s.queue, es...)
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *subscriber) pump() {
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			select {
			case <-s.signal:
				continue
			case <-s.done:
				return
			}
		}
		e := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		select {
		case s.out <- e:
		case <-s.done:
			return
		}
	}
}

func (s *subscriber) stop() { s.once.Do(func() { close(s.done) }) }

// Events is a stream of what changes, from now on. Each call is its own subscriber; call
// the returned function to unsubscribe, which closes the channel. Close ends them all.
func (d *Device) Events() (<-chan Event, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := &subscriber{signal: make(chan struct{}, 1), out: make(chan Event), done: make(chan struct{})}
	d.nextSub++
	id := d.nextSub
	d.subscribers[id] = s
	go s.pump()
	return s.out, func() {
		d.mu.Lock()
		delete(d.subscribers, id)
		d.mu.Unlock()
		s.stop()
	}
}

// changed notes a change, to be sent when the current operation ends.
func (d *Device) changed(e Event) { d.pendingEvents = append(d.pendingEvents, e) }

func (d *Device) changedItems(cid []byte, records ...[]byte) {
	d.changed(Event{Kind: EventItems, List: cid, Records: records})
}

// flushEvents sends what changed, merging record lists per list.
func (d *Device) flushEvents() {
	if len(d.pendingEvents) == 0 {
		return
	}
	items := map[string]map[string]bool{}
	var others []Event
	for _, e := range d.pendingEvents {
		if e.Kind == EventItems {
			if items[string(e.List)] == nil {
				items[string(e.List)] = map[string]bool{}
			}
			for _, r := range e.Records {
				items[string(e.List)][string(r)] = true
			}
			continue
		}
		if !slices.ContainsFunc(others, e.same) {
			others = append(others, e)
		}
	}
	d.pendingEvents = nil
	for _, list := range sortedKeys(items) {
		var records [][]byte
		for _, r := range sortedKeys(items[list]) {
			records = append(records, []byte(r))
		}
		others = append(others, Event{Kind: EventItems, List: []byte(list), Records: records})
	}
	for _, s := range d.subscribers {
		s.push(others)
	}
}

// WakeTag is the opaque tag a wake hint for this hub names (§10.3). Every participant's
// devices compute the same one; it reveals nothing else.
func (d *Device) WakeTag() string { return tandem.WakeTag(d.directoryKey, d.pin.Hub) }

// Run runs this device live: it subscribes to pushes, finishes any journalled work, then
// syncs on every push from the hub, never on a timer (§10.3). It returns when ctx ends or the
// push subscription does. A sync that fails is reported as EventSyncFailed and the next push
// tries again.
func (d *Device) Run(ctx context.Context) error {
	wakes, err := d.hub.Watch(ctx)
	if err != nil {
		return err
	}
	// Subscribed first, so nothing that lands during the first sync is missed.
	if err := d.Resume(ctx); err != nil {
		d.reportFailure(err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case w, ok := <-wakes:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return io.EOF
			}
			if w.Err != nil {
				return w.Err
			}
			if _, err := d.Sync(ctx); err != nil && ctx.Err() == nil {
				d.reportFailure(err)
			}
		}
	}
}

func (d *Device) reportFailure(err error) {
	if err == nil {
		return
	}
	d.mu.Lock()
	d.changed(Event{Kind: EventSyncFailed, Message: err.Error()})
	d.flushEvents()
	d.mu.Unlock()
}
