package imap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// part is a piece of a command: text sent as is, a literal, or a line sent after the server's "+".
type part struct {
	kind int // partRaw, partLiteral, partContinuation
	b    []byte
}

const (
	partRaw = iota
	partLiteral
	partContinuation
)

func text(s string) part { return part{partRaw, []byte(s)} }

func isAtomChar(c byte) bool {
	return c > 0x20 && c < 0x7f && !strings.ContainsRune(`(){%*"\]`, rune(c))
}

// astring is an astring (RFC 9051 §9) in its cheapest form: atom, quoted string or literal.
func astring(b []byte) part {
	atom, quotable := len(b) > 0, true
	for _, c := range b {
		if !isAtomChar(c) {
			atom = false
		}
		if c < 0x01 || c > 0x7f || c == '\r' || c == '\n' {
			quotable = false
		}
	}
	if atom {
		return part{partRaw, b}
	}
	if quotable {
		out := []byte{'"'}
		for _, c := range b {
			if c == '"' || c == '\\' {
				out = append(out, '\\')
			}
			out = append(out, c)
		}
		return part{partRaw, append(out, '"')}
	}
	return part{partLiteral, b}
}

// command is one command in flight. The reader goroutine fills it in and closes finished.
type command struct {
	tag                   string
	collecting, selecting bool
	untagged              []any
	plus                  chan struct{}
	status                status
	finished              chan struct{}
}

type result struct {
	status   status
	untagged []any
}

// errConnClosed is how a connection this client closed on purpose fails its commands.
var errConnClosed = &ConnectionError{Msg: "connection closed"}

// conn is one IMAP connection. A reader goroutine frames and parses every response;
// commands run one at a time (the caller serializes them). Untagged EXISTS, EXPUNGE and
// VANISHED outside a SELECT are reported as changes, whether they arrive during IDLE or
// alongside a command's answer.
type conn struct {
	nc       net.Conn
	trace    func(string)
	onChange func()
	onClosed func(error)
	caps     map[string]bool // set after login; read by the command goroutine only
	f        *framer         // the greeting reader, then the reader goroutine

	mu      sync.Mutex
	serial  int
	cur     *command
	noopTag string
	bye     string
	err     error
	dead    chan struct{}
}

func newConn(nc net.Conn, trace func(string), onChange func(), onClosed func(error)) *conn {
	return &conn{nc: nc, trace: trace, onChange: onChange, onClosed: onClosed, f: newFramer(64 << 20), dead: make(chan struct{})}
}

// log hands a protocol line to the trace hook: cut at 200 bytes, CRLF shown as "⏎".
func (c *conn) log(prefix string, b []byte) {
	if c.trace == nil {
		return
	}
	c.trace(prefix + strings.ReplaceAll(strings.ToValidUTF8(string(b[:min(len(b), 200)]), "�"), "\r\n", "⏎"))
}

// start reads the greeting, then starts the reader goroutine.
func (c *conn) start(ctx context.Context) (status, error) {
	stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	defer stop()
	buf := make([]byte, 1<<16)
	for {
		raw, err := c.f.next()
		if err != nil {
			c.fail(err)
			return status{}, err
		}
		if raw != nil {
			c.log("S: ", raw)
			r, err := parseResponse(raw)
			if err != nil {
				c.fail(err)
				return status{}, err
			}
			greeting, ok := r.(statusData)
			if !ok {
				err = perr("bad greeting")
				c.fail(err)
				return status{}, err
			}
			if greeting.kind == "BYE" {
				err = &ConnectionError{Msg: "server refused: " + greeting.text}
				c.fail(err)
				return status{}, err
			}
			go c.read()
			return greeting.status, nil
		}
		n, err := c.nc.Read(buf)
		c.f.append(buf[:n])
		if err != nil && n == 0 {
			if e := c.failed(); e != nil {
				return status{}, e
			}
			e := &ConnectionError{Msg: err.Error(), Err: err}
			c.fail(e)
			return status{}, e
		}
	}
}

func (c *conn) read() {
	buf := make([]byte, 1<<16)
	var readErr error
	for {
		for {
			raw, err := c.f.next()
			if err != nil {
				c.fail(err)
				return
			}
			if raw == nil {
				break
			}
			c.log("S: ", raw)
			r, err := parseResponse(raw)
			if err == nil {
				err = c.handle(r)
			}
			if err != nil {
				c.fail(err)
				return
			}
		}
		if readErr != nil {
			c.mu.Lock()
			bye := c.bye
			c.mu.Unlock()
			if bye != "" {
				c.fail(&ConnectionError{Msg: "server said BYE: " + bye, Err: readErr})
			} else {
				c.fail(&ConnectionError{Msg: "server closed the connection: " + readErr.Error(), Err: readErr})
			}
			return
		}
		n, err := c.nc.Read(buf)
		c.f.append(buf[:n])
		readErr = err
	}
}

func (c *conn) handle(r any) error {
	switch r := r.(type) {
	case continuation:
		c.mu.Lock()
		cur := c.cur
		c.mu.Unlock()
		if cur != nil {
			select {
			case cur.plus <- struct{}{}:
			default:
			}
		}
	case taggedResp:
		c.mu.Lock()
		if c.noopTag != "" && r.tag == c.noopTag {
			c.noopTag = ""
			c.mu.Unlock()
			return nil
		}
		cur := c.cur
		if cur == nil || r.tag != cur.tag {
			c.mu.Unlock()
			return perr("unexpected tag %s", r.tag)
		}
		c.cur = nil
		c.mu.Unlock()
		cur.status = r.status
		close(cur.finished)
	default:
		c.mu.Lock()
		cur := c.cur
		if s, ok := r.(statusData); ok && s.kind == "BYE" {
			c.bye = s.text
		}
		c.mu.Unlock()
		switch u := r.(type) {
		case existsData:
			if cur == nil || !cur.selecting {
				c.onChange()
			}
		case expungeData:
			c.onChange()
		case vanishedData:
			if !u.earlier {
				c.onChange()
			}
		}
		if cur != nil && cur.collecting {
			cur.untagged = append(cur.untagged, r)
		}
	}
	return nil
}

// fail ends the connection once; pending and later commands fail with err.
func (c *conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.dead)
	c.mu.Unlock()
	c.nc.Close()
	c.onClosed(err)
}

func (c *conn) failed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// close cancels the connection; pending commands fail.
func (c *conn) close() { c.fail(errConnClosed) }

func rejection(s status) error {
	code := ""
	if s.code != nil {
		code = s.code.name
	}
	return &RejectedError{Code: code, Text: s.text}
}

func (c *conn) begin(collecting, selecting bool) (*command, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if c.cur != nil {
		return nil, perr("command already in progress")
	}
	c.serial++
	cmd := &command{tag: fmt.Sprintf("T%d", c.serial), collecting: collecting, selecting: selecting,
		plus: make(chan struct{}, 64), finished: make(chan struct{})}
	c.cur = cmd
	return cmd, nil
}

func (c *conn) send(ctx context.Context, b []byte) error {
	stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	_, err := c.nc.Write(b)
	stop()
	if err != nil {
		if e := c.failed(); e != nil {
			return e
		}
		e := &ConnectionError{Msg: err.Error(), Err: err}
		c.fail(e)
		return e
	}
	return nil
}

func (c *conn) waitPlus(ctx context.Context, cmd *command) error {
	select {
	case <-cmd.plus:
		return nil
	default:
	}
	select {
	case <-cmd.plus:
		return nil
	case <-cmd.finished:
		return rejection(cmd.status)
	case <-c.dead:
		return c.failed()
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	}
}

func (c *conn) waitDone(ctx context.Context, cmd *command) (status, error) {
	select {
	case <-cmd.finished:
		return cmd.status, nil
	default:
	}
	select {
	case <-cmd.finished:
		return cmd.status, nil
	case <-c.dead:
		return status{}, c.failed()
	case <-ctx.Done():
		c.fail(ctx.Err())
		return status{}, ctx.Err()
	}
}

func (c *conn) nonSynchronizing(n int) bool {
	return c.caps["LITERAL+"] || (n <= 4096 && (c.caps["LITERAL-"] || c.caps["IMAP4REV2"]))
}

// execute runs one command. NO and BAD return a *RejectedError. A secret command's
// arguments are left out of the trace.
func (c *conn) execute(ctx context.Context, parts []part, selecting, secret bool) (result, error) {
	cmd, err := c.begin(true, selecting)
	if err != nil {
		return result{}, err
	}
	if c.trace != nil {
		shown := parts
		if secret {
			shown = parts[:1]
		}
		var line []byte
		for _, p := range shown {
			switch p.kind {
			case partRaw:
				line = append(line, p.b...)
			case partLiteral:
				line = fmt.Appendf(line, "{%d}", len(p.b))
			}
		}
		if secret {
			line = append(line, " <redacted>"...)
		}
		c.log("C: "+cmd.tag+" ", line)
	}
	out := []byte(cmd.tag + " ")
	for _, p := range parts {
		switch p.kind {
		case partRaw:
			out = append(out, p.b...)
		case partLiteral:
			if c.nonSynchronizing(len(p.b)) {
				out = fmt.Appendf(out, "{%d+}\r\n", len(p.b))
				out = append(out, p.b...)
			} else {
				out = fmt.Appendf(out, "{%d}\r\n", len(p.b))
				if err = c.send(ctx, out); err != nil {
					return result{}, err
				}
				if err = c.waitPlus(ctx, cmd); err != nil {
					return result{}, err
				}
				out = append([]byte(nil), p.b...)
			}
		case partContinuation:
			if err = c.send(ctx, append(out, '\r', '\n')); err != nil {
				return result{}, err
			}
			if err = c.waitPlus(ctx, cmd); err != nil {
				return result{}, err
			}
			out = append([]byte(nil), p.b...)
		}
	}
	if err = c.send(ctx, append(out, '\r', '\n')); err != nil {
		return result{}, err
	}
	st, err := c.waitDone(ctx, cmd)
	if err != nil {
		return result{}, err
	}
	if st.kind != "OK" {
		return result{}, rejection(st)
	}
	return result{st, cmd.untagged}, nil
}

// idle starts IDLE (RFC 2177) and returns once the server has accepted it. A NOOP goes in
// the same write: Stalwart reports a change made between the last command and IDLE only
// on the next command, not when IDLE starts, and it would stay unseen until then.
func (c *conn) idle(ctx context.Context, withNoop bool) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.serial++
	noop := fmt.Sprintf("T%d", c.serial)
	c.mu.Unlock()
	cmd, err := c.begin(false, false)
	if err != nil {
		return err
	}
	var out []byte
	if withNoop {
		c.mu.Lock()
		c.noopTag = noop
		c.mu.Unlock()
		c.log("C: ", []byte(noop+" NOOP"))
		out = []byte(noop + " NOOP\r\n")
	}
	c.log("C: ", []byte(cmd.tag+" IDLE"))
	if err = c.send(ctx, append(out, cmd.tag+" IDLE\r\n"...)); err != nil {
		return err
	}
	return c.waitPlus(ctx, cmd)
}

func (c *conn) isIdling() *command {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && !c.cur.collecting {
		return c.cur
	}
	return nil
}

// done ends IDLE and waits for its completion.
func (c *conn) done(ctx context.Context) error {
	cmd := c.isIdling()
	if cmd == nil {
		return nil
	}
	c.log("C: ", []byte("DONE"))
	if err := c.send(ctx, []byte("DONE\r\n")); err != nil {
		return err
	}
	st, err := c.waitDone(ctx, cmd)
	if err != nil {
		return err
	}
	if st.kind != "OK" {
		return rejection(st)
	}
	return nil
}

// isConnErr reports errors after which the connection can't be used.
func isConnErr(err error) bool {
	var ce *ConnectionError
	var pe *ProtocolError
	return errors.As(err, &ce) || errors.As(err, &pe) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
