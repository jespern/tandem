package imap

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// sink hands invalidations to the store's watch channel: one buffered wake, coalesced.
type sink struct {
	mu       sync.Mutex
	ch       chan tandem.Wake
	attached bool
	finished bool
}

func newSink() *sink { return &sink{ch: make(chan tandem.Wake, 1)} }

func (s *sink) attach(on bool) {
	s.mu.Lock()
	s.attached = on
	s.mu.Unlock()
}

func (s *sink) changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attached || s.finished {
		return
	}
	select {
	case s.ch <- tandem.Wake{}:
	default:
	}
}

// finish ends the channel, after a wake carrying err if there is one.
func (s *sink) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	if err != nil {
		select {
		case <-s.ch:
		default:
		}
		s.ch <- tandem.Wake{Err: err}
	}
	close(s.ch)
}

type selection struct {
	validity uint32
	uidNext  uint32 // 0 if the server didn't say
	modseq   uint64
	vanished uidSet
}

type op struct {
	ctx   context.Context
	fn    func(context.Context) (any, error)
	reply chan opResult
}

type opResult struct {
	v   any
	err error
}

const (
	evLost = iota
	evRefresh
	evRetry
)

type event struct {
	kind int
	n    int // the connection (evLost) or timer generation (evRefresh)
}

// link is one logical connection to the hub's Tandem folder. A worker goroutine owns it:
// it connects on demand, runs commands one at a time, and, while watching, idles between
// them and reconnects with backoff when the connection is lost. Every new connection made
// while watching reports one change once IDLE runs again, since anything may have happened
// while it was down.
type link struct {
	cfg    *Config
	idles  bool
	sink   *sink
	ctx    context.Context // ends at close
	cancel context.CancelFunc
	ops    chan *op
	events chan event
	quit   chan struct{}
	exited chan struct{}
	once   sync.Once
	shut   atomic.Bool
	live   atomic.Pointer[conn]

	// Worker-owned below.
	c                *conn
	serial, current  int
	caps             map[string]bool
	fresh            bool
	folder, selected string
	validity         uint32
	modseq           uint64
	qresync          bool
	special          *[2]string // \All and \Trash; "" if missing
	watching, idling bool
	timer            *time.Timer
	timerGen         int
	reconnecting     bool
	delay            time.Duration
	retryTimer       *time.Timer
	beforeIdle       func() // test hook: runs just before IDLE starts
	withoutNoop      bool   // test hook: IDLE without the NOOP before it
}

func newLink(cfg *Config, idles bool, sink *sink) *link {
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{cfg: cfg, idles: idles, sink: sink, ctx: ctx, cancel: cancel, ops: make(chan *op),
		events: make(chan event, 16), quit: make(chan struct{}), exited: make(chan struct{}), folder: cfg.Folder}
	go l.loop()
	return l
}

func (l *link) loop() {
	defer close(l.exited)
	for {
		select {
		case <-l.quit:
			l.shutdown()
			return
		case o := <-l.ops:
			if err := o.ctx.Err(); err != nil {
				o.reply <- opResult{nil, err}
				continue
			}
			v, err := o.fn(o.ctx)
			o.reply <- opResult{v, err}
		case e := <-l.events:
			switch e.kind {
			case evLost:
				if e.n == l.current && l.c != nil {
					l.drop(l.c)
				}
			case evRefresh:
				// Ends IDLE and starts it again: ready() sends DONE and settle() IDLE.
				if e.n == l.timerGen && l.idling {
					_, _ = l.run(l.ctx, nop)
				}
			case evRetry:
				l.retry()
			}
		}
	}
}

func nop(*conn) (any, error) { return nil, nil }

// post hands an event to the worker from another goroutine.
func (l *link) post(e event) {
	select {
	case l.events <- e:
	case <-l.exited:
	}
}

// do runs fn on the worker.
func (l *link) do(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	if l.shut.Load() {
		return nil, ErrClosed
	}
	o := &op{ctx, fn, make(chan opResult, 1)}
	select {
	case l.ops <- o:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.exited:
		return nil, ErrClosed
	}
	select {
	case r := <-o.reply:
		return r.v, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.exited:
		return nil, ErrClosed
	}
}

func (l *link) close() {
	l.once.Do(func() {
		l.shut.Store(true)
		l.cancel()
		if c := l.live.Load(); c != nil {
			c.close()
		}
		close(l.quit)
	})
	<-l.exited
}

func (l *link) shutdown() {
	l.watching, l.reconnecting = false, false
	l.stopTimer()
	if l.retryTimer != nil {
		l.retryTimer.Stop()
	}
	if l.c != nil {
		l.c.close()
		l.c, l.idling = nil, false
		l.live.Store(nil)
	}
}

// Serialization

// keepsConnection: after a NO or BAD the connection is still good; the folder is selected again.
func keepsConnection(c *conn, err error) bool {
	if c.failed() != nil {
		return false
	}
	var rejected *RejectedError
	if errors.As(err, &rejected) {
		return true
	}
	return !isConnErr(err) && !errors.Is(err, ErrClosed)
}

// permanent errors aren't retried: reconnecting can't help.
func permanent(err error) bool {
	var auth *AuthError
	var unsupported *UnsupportedError
	return errors.As(err, &auth) || errors.As(err, &unsupported) || errors.Is(err, ErrClosed) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// run runs body on a ready connection with the folder selected, then resumes IDLE if watching.
func (l *link) run(ctx context.Context, body func(*conn) (any, error)) (any, error) {
	if l.shut.Load() {
		return nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(l.ctx, cancel)
	defer stop()
	c, err := l.ready(ctx)
	if err != nil {
		if l.shut.Load() {
			return nil, ErrClosed
		}
		return nil, err
	}
	v, err := body(c)
	if err != nil {
		if l.shut.Load() {
			return nil, ErrClosed
		}
		if !keepsConnection(c, err) {
			l.drop(c)
			return nil, err
		}
		l.selected = "" // after a NO, select again: the folder may have been deleted under us
	}
	if e := l.settle(c); e != nil {
		l.drop(c)
	}
	return v, err
}

// retrying is run, repeated once after a NO (freshly selected) or a lost connection (freshly
// connected). Only for operations that are safe to repeat; an APPEND is not (§10.3, lost answers).
func (l *link) retrying(ctx context.Context, body func(*conn) (any, error)) (any, error) {
	v, err := l.run(ctx, body)
	if err != nil && !permanent(err) && ctx.Err() == nil {
		return l.run(ctx, body)
	}
	return v, err
}

func (l *link) ready(ctx context.Context) (*conn, error) {
	if c := l.c; c != nil {
		if c.failed() != nil {
			l.drop(c)
		} else {
			err := func() error {
				if l.idling {
					l.idling = false
					l.stopTimer()
					if err := c.done(ctx); err != nil {
						return err
					}
				}
				if l.selected != l.folder {
					_, err := l.selectFolder(ctx, c, nil)
					return err
				}
				return nil
			}()
			if err == nil {
				return c, nil
			}
			if keepsConnection(c, err) {
				return nil, err
			}
			l.drop(c)
		}
	}
	return l.connect(ctx)
}

func (l *link) settle(c *conn) error {
	if l.selected != l.folder {
		if _, err := l.selectFolder(l.ctx, c, nil); err != nil {
			return err
		}
	}
	if l.watching && l.idles {
		if l.beforeIdle != nil {
			l.beforeIdle()
		}
		if err := c.idle(l.ctx, !l.withoutNoop); err != nil {
			return err
		}
		l.idling = true
		l.arm()
		if l.fresh {
			l.sink.changed()
		}
	}
	l.fresh = false
	return nil
}

func (l *link) drop(c *conn) {
	if l.c != c {
		c.close()
		return
	}
	l.c, l.idling, l.selected = nil, false, ""
	l.live.Store(nil)
	l.stopTimer()
	c.close()
	l.reconnect()
}

// Connecting

func dial(ctx context.Context, cfg *Config) (net.Conn, error) {
	d := &net.Dialer{KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 60 * time.Second, Interval: 15 * time.Second, Count: 4}}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var nc net.Conn
	var err error
	if cfg.Security == PlaintextForTesting {
		nc, err = d.DialContext(ctx, "tcp", address)
	} else {
		t := &tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12,
			InsecureSkipVerify: cfg.Security == TLSTrustingAnyCertificateForTesting}}
		nc, err = t.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ConnectionError{Msg: err.Error(), Err: err}
	}
	return nc, nil
}

// connect dials, logs in and selects the folder, all within cfg.ConnectTimeout: a server that
// accepts the connection and then stays silent fails as a lost connection, so watching retries
// with backoff instead of waiting on it for good.
func (l *link) connect(ctx context.Context) (*conn, error) {
	bounded, cancel := context.WithTimeout(ctx, l.cfg.ConnectTimeout) // allowed-timer: connect deadline, a safety net against a silent server (§10.3)
	defer cancel()
	c, err := l.dialAndLogin(bounded)
	if err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, &ConnectionError{Msg: "no answer within " + l.cfg.ConnectTimeout.String() + " of connecting"} // not wrapping err: a lost connection, not a cancellation
	}
	return c, err
}

func (l *link) dialAndLogin(ctx context.Context) (*conn, error) {
	nc, err := dial(ctx, l.cfg)
	if err != nil {
		return nil, err
	}
	l.serial++
	id := l.serial
	c := newConn(nc, l.cfg.Trace, l.sink.changed, func(error) { go l.post(event{evLost, id}) })
	l.live.Store(c)
	if l.shut.Load() {
		c.close()
	}
	if err = l.login(ctx, c); err != nil {
		if l.c == c {
			l.c = nil
		}
		l.live.CompareAndSwap(c, nil)
		c.close()
		return nil, err
	}
	return c, nil
}

func (l *link) login(ctx context.Context, c *conn) error {
	greeting, err := c.start(ctx)
	if err != nil {
		return err
	}
	var caps map[string]bool
	if greeting.code != nil && greeting.code.name == "CAPABILITY" {
		caps = setOfStrings(greeting.code.caps)
	}
	if len(caps) == 0 {
		if caps, err = capability(ctx, c); err != nil {
			return err
		}
	}
	if greeting.kind != "PREAUTH" {
		if caps, err = l.authenticate(ctx, c, caps); err != nil {
			return err
		}
	}
	c.caps = caps
	if !caps["IMAP4REV2"] && !(caps["UIDPLUS"] && caps["IDLE"]) {
		return &UnsupportedError{"the server lacks UIDPLUS or IDLE (§10.3)"}
	}
	l.qresync = false
	if !l.cfg.DisableQRESYNC && caps["QRESYNC"] {
		r, err := c.execute(ctx, []part{text("ENABLE QRESYNC")}, false, false)
		if err != nil {
			return err
		}
		for _, u := range r.untagged {
			if e, ok := u.(enabledData); ok && slices.Contains(e, "QRESYNC") {
				l.qresync = true
			}
		}
	}
	if caps["NAMESPACE"] || caps["IMAP4REV2"] {
		r, err := c.execute(ctx, []part{text("NAMESPACE")}, false, false)
		if err != nil {
			return err
		}
		for _, u := range r.untagged {
			if personal, ok := u.(namespaceData); ok {
				prefix := ""
				if len(personal) > 0 {
					prefix = string(personal[0].prefix)
				}
				l.folder = prefix + l.cfg.Folder
			}
		}
	}
	l.caps = caps
	l.c, l.current, l.fresh, l.selected = c, l.serial, true, ""
	_, err = l.selectFolder(ctx, c, nil)
	return err
}

func setOfStrings(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

func capability(ctx context.Context, c *conn) (map[string]bool, error) {
	r, err := c.execute(ctx, []part{text("CAPABILITY")}, false, false)
	if err != nil {
		return nil, err
	}
	for _, u := range r.untagged {
		if list, ok := u.(capabilityData); ok {
			return setOfStrings(list), nil
		}
	}
	return nil, perr("no CAPABILITY response")
}

// authenticate uses AUTHENTICATE PLAIN when offered, else LOGIN. It returns the capabilities after login.
func (l *link) authenticate(ctx context.Context, c *conn, caps map[string]bool) (map[string]bool, error) {
	var r result
	var err error
	user, pass := []byte(l.cfg.Username), []byte(l.cfg.Password)
	switch {
	case caps["AUTH=PLAIN"]:
		plain := append(append(append([]byte{0}, user...), 0), pass...)
		token := []byte(base64.StdEncoding.EncodeToString(plain))
		if caps["SASL-IR"] || caps["IMAP4REV2"] {
			r, err = c.execute(ctx, []part{text("AUTHENTICATE PLAIN "), {partRaw, token}}, false, true)
		} else {
			r, err = c.execute(ctx, []part{text("AUTHENTICATE PLAIN"), {partContinuation, token}}, false, true)
		}
	case caps["LOGINDISABLED"]:
		return nil, &UnsupportedError{"the server offers neither LOGIN nor AUTHENTICATE PLAIN"}
	default:
		r, err = c.execute(ctx, []part{text("LOGIN "), astring(user), text(" "), astring(pass)}, false, true)
	}
	if err != nil {
		var rejected *RejectedError
		if errors.As(err, &rejected) {
			if rejected.Code == "UNAVAILABLE" {
				return nil, &ConnectionError{Msg: "login unavailable: " + rejected.Text}
			}
			return nil, &AuthError{rejected.Text}
		}
		return nil, err
	}
	if r.status.code != nil && r.status.code.name == "CAPABILITY" {
		return setOfStrings(r.status.code.caps), nil
	}
	for _, u := range r.untagged {
		if list, ok := u.(capabilityData); ok {
			return setOfStrings(list), nil
		}
	}
	return capability(ctx, c)
}

// Folder

func (l *link) ensureFolder(ctx context.Context, c *conn) error {
	r, err := c.execute(ctx, []part{text(`LIST "" `), astring([]byte(l.folder))}, false, false)
	if err != nil {
		return err
	}
	for _, u := range r.untagged {
		if list, ok := u.(listData); ok && string(list.name) == l.folder &&
			!slices.ContainsFunc(list.flags, func(f string) bool { return strings.ToUpper(f) == `\NONEXISTENT` }) {
			return nil
		}
	}
	_, err = c.execute(ctx, []part{text("CREATE "), astring([]byte(l.folder))}, false, false)
	var rejected *RejectedError
	if errors.As(err, &rejected) && rejected.Code == "ALREADYEXISTS" {
		return nil
	}
	return err
}

// selectFolder selects the Tandem folder, creating it if missing.
func (l *link) selectFolder(ctx context.Context, c *conn, since *cursor) (selection, error) {
	s, err := l.selectBox(ctx, c, l.folder, since)
	var rejected *RejectedError
	if errors.As(err, &rejected) {
		if err = l.ensureFolder(ctx, c); err != nil {
			return s, err
		}
		return l.selectBox(ctx, c, l.folder, since)
	}
	return s, err
}

// selectBox is SELECT, with QRESYNC parameters when enabled and the cursor has a modseq (RFC 7162 §3.2.5).
func (l *link) selectBox(ctx context.Context, c *conn, name string, since *cursor) (selection, error) {
	parts := []part{text("SELECT "), astring([]byte(name))}
	if l.qresync && since != nil && since.modseq > 0 {
		p := fmt.Sprintf("%d %d", since.validity, since.modseq)
		if known := since.known.String(); known != "" && len(known) <= 4000 {
			p += " " + known // else the server reports more; intersected in scan
		}
		parts = append(parts, text(" (QRESYNC ("+p+"))"))
	}
	l.selected = ""
	r, err := c.execute(ctx, parts, true, false)
	if err != nil {
		return selection{}, err
	}
	var s selection
	for _, u := range r.untagged {
		switch u := u.(type) {
		case statusData:
			if u.code != nil && u.code.name == "UIDVALIDITY" {
				s.validity = uint32(u.code.num)
			}
			if u.code != nil && u.code.name == "HIGHESTMODSEQ" {
				s.modseq = u.code.num
			}
			if u.code != nil && u.code.name == "UIDNEXT" {
				s.uidNext = uint32(u.code.num)
			}
		case vanishedData:
			if u.earlier {
				s.vanished = s.vanished.union(u.set)
			}
		}
	}
	if s.validity == 0 {
		return s, perr("SELECT without UIDVALIDITY")
	}
	l.selected = name
	if name == l.folder {
		l.validity, l.modseq = s.validity, s.modseq
	}
	return s, nil
}

// Watching

func (l *link) restartPeriod() time.Duration {
	refresh := 29 * time.Minute
	switch r := l.cfg.IdleRestart; {
	case r < 0:
		return refresh
	case r > 0:
		return min(time.Duration(r), refresh)
	default:
		return refresh
	}
}

func (l *link) arm() {
	l.stopTimer()
	l.timerGen++
	gen := l.timerGen
	l.timer = time.AfterFunc(l.restartPeriod(), func() { l.post(event{evRefresh, gen}) }) // allowed-timer: IDLE refresh (RFC 2177, 29 min) and Gmail's 3 s IDLE restart (§10.3)
}

func (l *link) stopTimer() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	l.timerGen++
}

func (l *link) reconnect() {
	if l.reconnecting || !l.watching || l.shut.Load() {
		return
	}
	l.reconnecting = true
	l.delay = l.cfg.ReconnectMin
	go l.post(event{kind: evRetry})
}

func (l *link) retry() {
	if !l.reconnecting {
		return
	}
	if !l.watching || l.shut.Load() {
		l.reconnecting = false
		return
	}
	_, err := l.run(l.ctx, nop)
	if err == nil && (l.idling || !l.watching) {
		l.reconnecting = false
		return
	}
	if err != nil && permanent(err) {
		l.reconnecting = false
		if !l.shut.Load() {
			l.watching = false
			l.sink.finish(err)
		}
		return
	}
	d := l.delay
	l.delay = min(2*d, l.cfg.ReconnectMax)
	l.retryTimer = time.AfterFunc(d, func() { l.post(event{kind: evRetry}) }) // allowed-timer: reconnect backoff (§10.3)
}

// startWatching returns once IDLE runs, so no change after this call is missed.
func (l *link) startWatching(ctx context.Context) error {
	_, err := l.do(ctx, func(ctx context.Context) (any, error) {
		l.watching = true
		if _, err := l.run(ctx, nop); err != nil {
			l.watching = false
			return nil, err
		}
		if l.idles && !l.idling {
			l.watching, l.reconnecting = false, false
			return nil, &ConnectionError{Msg: "could not start IDLE"}
		}
		return nil, nil
	})
	return err
}

func (l *link) stopWatching() {
	_, _ = l.do(context.Background(), func(ctx context.Context) (any, error) {
		if !l.watching {
			return nil, nil
		}
		l.watching, l.reconnecting = false, false
		if l.retryTimer != nil {
			l.retryTimer.Stop()
		}
		if l.idling {
			_, _ = l.run(ctx, nop)
		}
		return nil, nil
	})
}

// Store operations (§10.3)

func (l *link) scan(ctx context.Context, c *conn, cur *cursor) (tandem.ChangeBatch, error) {
	// SELECT every time: it is where UIDVALIDITY and HIGHESTMODSEQ are learned, and a
	// session may keep a stale view of a folder deleted and recreated by another client.
	s, err := l.selectFolder(ctx, c, cur)
	if err != nil {
		return tandem.ChangeBatch{}, err
	}
	var vanished *uidSet
	if l.qresync && cur != nil && cur.modseq > 0 && cur.validity == s.validity {
		vanished = &s.vanished
	}
	var removed []string
	var known uidSet
	var last uint32
	if cur != nil && cur.validity == l.validity {
		known, last = cur.known, cur.last
		var gone uidSet
		if vanished != nil {
			gone = known.intersection(*vanished)
		} else {
			all, err := l.searchAll(ctx, c)
			if err != nil {
				return tandem.ChangeBatch{}, err
			}
			gone = known.subtracting(all)
		}
		for _, uid := range gone.uids() {
			removed = append(removed, messageID(l.validity, uid))
		}
		known = known.subtracting(gone)
	} else if cur != nil {
		for _, uid := range cur.known.uids() { // UIDVALIDITY changed: full resync
			removed = append(removed, messageID(cur.validity, uid))
		}
	}
	var added []tandem.Message
	top, more := last, false
	if last < math.MaxUint32 {
		// n:* always returns the last message, so a UID below the range is ignored.
		set := fmt.Sprintf("%d:*", last+1)
		// A page at a time: new UIDs lie below UIDNEXT, so only a wider gap than a page can hold
		// more than one. Then the new UIDs are searched, and the first page of them fetched.
		if page := l.cfg.PageSize; s.uidNext == 0 || int64(s.uidNext)-1-int64(last) > int64(page) {
			found, err := l.search(ctx, c, set)
			if err != nil {
				return tandem.ChangeBatch{}, err
			}
			var uids []uint32
			for _, uid := range found.uids() {
				if uid > last {
					uids = append(uids, uid)
				}
			}
			if len(uids) > page {
				uids, more = uids[:page], true
			}
			if len(uids) > 0 {
				top = uids[len(uids)-1] // past the page even if its last messages went meanwhile
			}
			set = setOf(uids...).String()
		}
		items := map[uint32]*fetchItem{}
		if set != "" {
			var err error
			if items, err = l.fetchItems(ctx, c, set); err != nil {
				return tandem.ChangeBatch{}, err
			}
		}
		for _, uid := range sortedKeys(items) {
			if uid <= last {
				continue
			}
			top = max(top, uid)
			if m, ok := l.decode(items[uid], uid); ok {
				added = append(added, m)
				known.insert(uid)
			}
		}
	}
	modseq := uint64(0)
	if l.qresync {
		modseq = l.modseq
	}
	return tandem.ChangeBatch{Added: added, Removed: removed, More: more,
		Cursor: cursor{validity: l.validity, modseq: modseq, last: top, known: known}.String()}, nil
}

func sortedKeys(m map[uint32]*fetchItem) []uint32 {
	keys := make([]uint32, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (l *link) searchAll(ctx context.Context, c *conn) (uidSet, error) {
	return l.search(ctx, c, "ALL")
}

// search is UID SEARCH for a criterion: ALL, or a UID set such as "n:*".
func (l *link) search(ctx context.Context, c *conn, criterion string) (uidSet, error) {
	if criterion != "ALL" {
		criterion = "UID " + criterion
	}
	if l.caps["ESEARCH"] || l.caps["IMAP4REV2"] {
		r, err := c.execute(ctx, []part{text("UID SEARCH RETURN (ALL) " + criterion)}, false, false)
		if err != nil {
			return uidSet{}, err
		}
		for _, u := range r.untagged {
			if e, ok := u.(esearchData); ok {
				return e.all, nil
			}
		}
		return uidSet{}, nil
	}
	r, err := c.execute(ctx, []part{text("UID SEARCH " + criterion)}, false, false)
	if err != nil {
		return uidSet{}, err
	}
	var out []uint32
	for _, u := range r.untagged {
		if s, ok := u.(searchData); ok {
			out = append(out, s...)
		}
	}
	return setOf(out...), nil
}

func (l *link) fetchItems(ctx context.Context, c *conn, set string) (map[uint32]*fetchItem, error) {
	cmd := fmt.Sprintf("UID FETCH %s (UID RFC822.SIZE BODY.PEEK[HEADER.FIELDS (%s)] BODY.PEEK[TEXT]<0.%d>)",
		set, strings.Join(headerFields, " "), l.cfg.MaxMessageSize)
	r, err := c.execute(ctx, []part{text(cmd)}, false, false)
	if err != nil {
		return nil, err
	}
	items := map[uint32]*fetchItem{}
	for _, u := range r.untagged {
		if f, ok := u.(fetchData); ok && f.item.uid != nil {
			item := items[*f.item.uid]
			if item == nil {
				item = &fetchItem{}
				items[*f.item.uid] = item
			}
			item.merge(f.item)
		}
	}
	return items, nil
}

// decode returns false for a message that is too large or doesn't decode (§10.4: ignored).
func (l *link) decode(item *fetchItem, uid uint32) (tandem.Message, bool) {
	if item.size == nil || *item.size > uint64(l.cfg.MaxMessageSize) {
		return tandem.Message{}, false
	}
	header, ok1 := item.sections["HEADER.FIELDS"]
	body, ok2 := item.sections["TEXT"]
	if !ok1 || !ok2 {
		return tandem.Message{}, false
	}
	channel, envelope, err := DecodeMessage(header, body)
	if err != nil {
		return tandem.Message{}, false
	}
	return tandem.Message{ID: messageID(l.validity, uid), Channel: channel, Body: envelope}, true
}

// chunks splits a UID set into sets small enough for one command line.
func chunks(set uidSet) []string {
	var out []string
	for i := 0; i < len(set.r); i += 200 {
		out = append(out, uidSet{set.r[i:min(i+200, len(set.r))]}.String())
	}
	return out
}

// currentUIDs are the UIDs of the IDs that belong to the folder's current UIDVALIDITY.
func (l *link) currentUIDs(ids []string) uidSet {
	var uids []uint32
	for _, id := range ids {
		if v, u, ok := parseMessageID(id); ok && v == l.validity {
			uids = append(uids, u)
		}
	}
	return setOf(uids...)
}

// appendMessages appends whole messages, several per command with MULTIAPPEND (RFC 3502).
// IDs in order; "" where the server named no UID.
func (l *link) appendMessages(ctx context.Context, c *conn, messages [][]byte) ([]string, error) {
	var ids []string
	multi := l.caps["MULTIAPPEND"]
	for i := 0; i < len(messages); {
		batch, size := [][]byte{messages[i]}, len(messages[i])
		i++
		for multi && i < len(messages) && len(batch) < 100 && size+len(messages[i]) <= 8<<20 {
			batch = append(batch, messages[i])
			size += len(messages[i])
			i++
		}
		parts := []part{text("APPEND "), astring([]byte(l.folder))}
		for _, m := range batch {
			parts = append(parts, text(` (\Seen) `), part{partLiteral, m})
		}
		r, err := c.execute(ctx, parts, false, false)
		if err != nil {
			return nil, err
		}
		if code := r.status.code; code != nil && code.name == "APPENDUID" && len(code.uids) == len(batch) {
			for _, uid := range code.uids {
				ids = append(ids, messageID(code.validity, uid))
			}
		} else {
			ids = append(ids, make([]string, len(batch))...)
		}
	}
	return ids, nil
}

func (l *link) fetch(ctx context.Context, c *conn, ids []string) ([]tandem.Message, error) {
	uids := l.currentUIDs(ids)
	var out []tandem.Message
	for _, chunk := range chunks(uids) {
		items, err := l.fetchItems(ctx, c, chunk)
		if err != nil {
			return nil, err
		}
		for _, uid := range sortedKeys(items) {
			if !uids.contains(uid) {
				continue
			}
			if m, ok := l.decode(items[uid], uid); ok {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func (l *link) exec(ctx context.Context, c *conn, s string) (result, error) {
	return c.execute(ctx, []part{text(s)}, false, false)
}

// destroy is UID STORE \Deleted and UID EXPUNGE; on Gmail, the copy in All Mail then goes to
// Trash and is expunged there.
func (l *link) destroy(ctx context.Context, c *conn, ids []string) error {
	uids := l.currentUIDs(ids)
	gmail := l.caps["X-GM-EXT-1"]
	var gmailIDs []uint64
	for _, chunk := range chunks(uids) {
		if gmail {
			r, err := l.exec(ctx, c, "UID FETCH "+chunk+" (UID X-GM-MSGID)")
			if err != nil {
				return err
			}
			for _, u := range r.untagged {
				if f, ok := u.(fetchData); ok && f.item.gmailID != nil && f.item.uid != nil && uids.contains(*f.item.uid) {
					gmailIDs = append(gmailIDs, *f.item.gmailID)
				}
			}
		}
		if _, err := l.exec(ctx, c, "UID STORE "+chunk+` +FLAGS.SILENT (\Deleted)`); err != nil {
			return err
		}
		if _, err := l.exec(ctx, c, "UID EXPUNGE "+chunk); err != nil {
			return err
		}
	}
	if len(gmailIDs) > 0 {
		return l.purgeGmail(ctx, c, gmailIDs)
	}
	return nil
}

func (l *link) specialUse(ctx context.Context, c *conn) ([2]string, error) {
	if l.special != nil {
		return *l.special, nil
	}
	r, err := l.exec(ctx, c, `LIST "" "*"`)
	if err != nil {
		return [2]string{}, err
	}
	var found [2]string
	for _, u := range r.untagged {
		if list, ok := u.(listData); ok {
			for _, f := range list.flags {
				switch strings.ToUpper(f) {
				case `\ALL`:
					found[0] = string(list.name)
				case `\TRASH`:
					found[1] = string(list.name)
				}
			}
		}
	}
	l.special = &found
	return found, nil
}

func (l *link) purgeGmail(ctx context.Context, c *conn, ids []uint64) error {
	special, err := l.specialUse(ctx, c)
	if err != nil {
		return err
	}
	all, trash := special[0], special[1]
	if all == "" || trash == "" {
		return &UnsupportedError{`Gmail without \All and \Trash folders`}
	}
	if _, err = l.selectBox(ctx, c, all, nil); err != nil {
		return err
	}
	inAll, err := l.searchGmail(ctx, c, ids)
	if err != nil {
		return err
	}
	var inTrash uidSet
	for _, chunk := range chunks(inAll) {
		var r result
		if l.caps["MOVE"] {
			r, err = c.execute(ctx, []part{text("UID MOVE " + chunk + " "), astring([]byte(trash))}, false, false)
			if err != nil {
				return err
			}
		} else {
			if r, err = c.execute(ctx, []part{text("UID COPY " + chunk + " "), astring([]byte(trash))}, false, false); err != nil {
				return err
			}
			if _, err = l.exec(ctx, c, "UID STORE "+chunk+` +FLAGS.SILENT (\Deleted)`); err != nil {
				return err
			}
			if _, err = l.exec(ctx, c, "UID EXPUNGE "+chunk); err != nil {
				return err
			}
		}
		codes := []*respCode{r.status.code}
		for _, u := range r.untagged {
			if s, ok := u.(statusData); ok {
				codes = append(codes, s.code)
			}
		}
		for _, code := range codes {
			if code != nil && code.name == "COPYUID" {
				inTrash = inTrash.union(code.dest)
			}
		}
	}
	if _, err = l.selectBox(ctx, c, trash, nil); err != nil {
		return err
	}
	found, err := l.searchGmail(ctx, c, ids)
	if err != nil {
		return err
	}
	inTrash = inTrash.union(found)
	for _, chunk := range chunks(inTrash) {
		if _, err = l.exec(ctx, c, "UID STORE "+chunk+` +FLAGS.SILENT (\Deleted)`); err != nil {
			return err
		}
		if _, err = l.exec(ctx, c, "UID EXPUNGE "+chunk); err != nil {
			return err
		}
	}
	return nil
}

func (l *link) searchGmail(ctx context.Context, c *conn, ids []uint64) (uidSet, error) {
	var out []uint32
	for start := 0; start < len(ids); start += 20 {
		group := ids[start:min(start+20, len(ids))]
		var q strings.Builder
		for _, id := range group[:len(group)-1] {
			fmt.Fprintf(&q, "OR X-GM-MSGID %d ", id)
		}
		fmt.Fprintf(&q, "X-GM-MSGID %d", group[len(group)-1])
		r, err := l.exec(ctx, c, "UID SEARCH "+q.String())
		if err != nil {
			return uidSet{}, err
		}
		for _, u := range r.untagged {
			switch u := u.(type) {
			case searchData:
				out = append(out, u...)
			case esearchData:
				out = append(out, u.all.uids()...)
			}
		}
	}
	return setOf(out...), nil
}
