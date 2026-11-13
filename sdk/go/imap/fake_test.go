package imap

import (
	"bytes"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeServer is a plaintext loopback IMAP server for scripted tests, ported from
// FakeServer.swift. It frames client commands (synchronizing literals get a "+", LITERAL+
// ones don't), handles IDLE/DONE itself and passes every other command to respond, which
// returns the response lines.
type fakeServer struct {
	ln       net.Listener
	greeting string
	respond  func(fakeCommand) []string

	mu      sync.Mutex
	clients []*fakeClient
	log     []string
	wg      sync.WaitGroup
}

type fakeCommand struct {
	tag, text string
	literals  [][]byte
}

func startFake(t *testing.T, greeting string, respond func(fakeCommand) []string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, greeting: greeting, respond: respond}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			c := &fakeClient{nc: nc, server: s}
			s.mu.Lock()
			s.clients = append(s.clients, c)
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				c.send([]string{s.greeting})
				c.read()
			}()
		}
	}()
	t.Cleanup(s.stop)
	return s
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) stop() {
	s.ln.Close()
	s.disconnectAll()
	s.wg.Wait()
}

// lines are the client lines in order, IDLE and DONE included.
func (s *fakeServer) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *fakeServer) record(line string) {
	s.mu.Lock()
	s.log = append(s.log, line)
	s.mu.Unlock()
}

// push sends unsolicited lines to every connected client.
func (s *fakeServer) push(lines ...string) {
	s.mu.Lock()
	clients := append([]*fakeClient(nil), s.clients...)
	s.mu.Unlock()
	for _, c := range clients {
		c.send(lines)
	}
}

// disconnectAll drops every client connection, as a server restart would.
func (s *fakeServer) disconnectAll() {
	s.mu.Lock()
	clients := s.clients
	s.clients = nil
	s.mu.Unlock()
	for _, c := range clients {
		c.nc.Close()
	}
}

type fakeClient struct {
	nc      net.Conn
	server  *fakeServer
	wmu     sync.Mutex
	buf     []byte
	pending *fakePending
	idleTag string
}

type fakePending struct {
	tag, text string
	literals  [][]byte
	need      int
}

func (c *fakeClient) send(lines []string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	_, _ = c.nc.Write([]byte(b.String()))
}

func (c *fakeClient) read() {
	defer c.nc.Close()
	buf := make([]byte, 1<<16)
	for {
		n, err := c.nc.Read(buf)
		c.buf = append(c.buf, buf[:n]...)
		if !c.process() || err != nil {
			return
		}
	}
}

var literalAtEnd = regexp.MustCompile(`\{(\d+)(\+?)\}$`)

func (c *fakeClient) process() bool {
	for {
		if p := c.pending; p != nil && p.need > 0 {
			if len(c.buf) < p.need {
				return true
			}
			p.literals = append(p.literals, bytes.Clone(c.buf[:p.need]))
			c.buf = c.buf[p.need:]
			p.need = 0
		}
		lf := bytes.IndexByte(c.buf, '\n')
		if lf < 0 {
			return true
		}
		line := strings.ReplaceAll(string(c.buf[:lf]), "\r", "")
		c.buf = c.buf[lf+1:]
		if !c.handle(line) {
			return false
		}
	}
}

func (c *fakeClient) handle(line string) bool {
	if c.idleTag != "" && c.pending == nil {
		c.server.record(line)
		if line == "DONE" {
			c.send([]string{c.idleTag + " OK IDLE terminated"})
			c.idleTag = ""
		}
		return true
	}
	text, tag := line, ""
	var literals [][]byte
	joined := line
	if c.pending != nil {
		tag, literals, joined = c.pending.tag, c.pending.literals, c.pending.text+line
	} else {
		tag, text, _ = strings.Cut(line, " ")
		joined = text
	}
	if m := literalAtEnd.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		c.pending = &fakePending{tag, joined, literals, n}
		if m[2] == "" {
			c.send([]string{"+ go ahead"})
		}
		return true
	}
	c.pending = nil
	c.server.record(tag + " " + joined)
	switch strings.ToUpper(joined) {
	case "IDLE":
		c.idleTag = tag
		c.send([]string{"+ idling"})
		return true
	case "LOGOUT":
		c.send([]string{"* BYE", tag + " OK"})
		return false
	}
	for _, r := range c.server.respond(fakeCommand{tag, joined, literals}) {
		if !strings.HasPrefix(r, "*") && !strings.HasPrefix(r, "+") {
			r = tag + " " + r
		}
		c.send([]string{r})
	}
	return true
}
