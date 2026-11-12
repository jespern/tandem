package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The IMAP binding (§10.3), on emersion/go-imap v2. One connection per
// device: IDLE holds it while nothing else runs, and every command leaves
// IDLE first and lets the watcher resume it afterwards.
//
// Removals are found by comparing UID SEARCH ALL with the known UIDs, which
// §10.3 allows without QRESYNC and which go-imap's client doesn't offer yet.

type IMAP struct {
	d Descriptor

	mu     sync.Mutex // serializes commands on the one connection
	c      *imapclient.Client
	idle   *imapclient.IdleCommand
	folder string

	uidValidity uint32
	lastUID     imap.UID            // the scan cursor: advanced by scans, never by APPENDUID
	known       map[imap.UID]string // UID → channel tag
	stale       []string            // known before a UIDVALIDITY change; reported as removed

	wake chan struct{} // the server said something changed
	kick chan struct{} // a command finished: the watcher resumes IDLE
}

func OpenIMAP(ctx context.Context, d Descriptor) (*IMAP, error) {
	s := &IMAP{d: d, known: map[imap.UID]string{}, wake: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.connect(); err != nil {
		return nil, err
	}
	return s, nil
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *IMAP) connect() error {
	opts := &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { signal(s.wake) },
		Mailbox: func(m *imapclient.UnilateralDataMailbox) {
			if m.NumMessages != nil {
				signal(s.wake)
			}
		},
	}}
	addr := net.JoinHostPort(s.d.Host, strconv.Itoa(s.d.Port))
	var c *imapclient.Client
	var err error
	if s.d.TLS == nil || *s.d.TLS {
		c, err = imapclient.DialTLS(addr, opts)
	} else {
		c, err = imapclient.DialInsecure(addr, opts)
	}
	if err != nil {
		return err
	}
	if err := c.Login(s.d.User, s.d.Password).Wait(); err != nil {
		c.Close()
		var ie *imap.Error
		if errors.As(err, &ie) && ie.Code == imap.ResponseCodeAuthenticationFailed {
			return ErrAuth
		}
		return err
	}
	caps := c.Caps()
	if !caps.Has(imap.CapUIDPlus) && !caps.Has(imap.CapIMAP4rev2) || !caps.Has(imap.CapIdle) && !caps.Has(imap.CapIMAP4rev2) {
		c.Close()
		return errors.New("imap: server lacks UIDPLUS or IDLE (§10.3)")
	}
	s.folder = Folder
	if caps.Has(imap.CapNamespace) {
		if ns, err := c.Namespace().Wait(); err == nil && len(ns.Personal) > 0 {
			s.folder = ns.Personal[0].Prefix + Folder
		}
	}
	s.c = c
	return nil
}

// selectFolder selects Tandem, and starts over if UIDVALIDITY changed.
func (s *IMAP) selectFolder() error {
	sel, err := s.c.Select(s.folder, nil).Wait()
	if err != nil {
		return err
	}
	if sel.UIDValidity != s.uidValidity {
		// A full resync: every known UID is gone, and the rescan adds what
		// is there now.
		for u := range s.known {
			s.stale = append(s.stale, strconv.FormatUint(uint64(u), 10))
		}
		s.uidValidity, s.lastUID, s.known = sel.UIDValidity, 0, map[imap.UID]string{}
	}
	return nil
}

// with runs fn on the connection, leaving IDLE first and reconnecting if the
// connection was lost.
func (s *IMAP) with(fn func(c *imapclient.Client) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer signal(s.kick)
	if err := s.stopIdle(); err != nil {
		s.drop()
	}
	if s.c == nil {
		if err := s.connect(); err != nil {
			return err
		}
	}
	if s.c.Mailbox() == nil || s.c.Mailbox().Name != s.folder {
		if err := s.selectFolder(); err != nil && !strings.Contains(err.Error(), "NONEXISTENT") {
			return err
		}
	}
	err := fn(s.c)
	var ie *imap.Error
	if err != nil && !errors.As(err, &ie) { // not a NO or BAD: the connection may be broken
		s.drop()
	}
	return err
}

func (s *IMAP) stopIdle() error {
	if s.idle == nil {
		return nil
	}
	err := s.idle.Close()
	if err == nil {
		err = s.idle.Wait()
	}
	s.idle = nil
	return err
}

func (s *IMAP) drop() {
	if s.c != nil {
		s.c.Close()
	}
	s.c, s.idle = nil, nil
}

func (s *IMAP) EnsureFolder(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c == nil {
		if err := s.connect(); err != nil {
			return err
		}
	}
	s.stopIdle()
	list, err := s.c.List("", s.folder, nil).Collect()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		if err := s.c.Create(s.folder, nil).Wait(); err != nil {
			var ie *imap.Error
			if !errors.As(err, &ie) || ie.Code != imap.ResponseCodeAlreadyExists {
				return err
			}
		}
	}
	return s.selectFolder()
}

func (s *IMAP) Append(ctx context.Context, tag string, env []byte) (string, error) {
	msg := tandem.BuildMessage(tag, env, time.Now(), nil)
	var id string
	err := s.with(func(c *imapclient.Client) error {
		cmd := c.Append(s.folder, int64(len(msg)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
		if _, err := cmd.Write(msg); err != nil {
			cmd.Close()
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		data, err := cmd.Wait()
		if err != nil {
			return err
		}
		id = strconv.FormatUint(uint64(data.UID), 10)
		return nil
	})
	return id, err
}

var channelHeader = &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Tandem-Channel"}, Peek: true}

func (s *IMAP) Changes(ctx context.Context) ([]Ref, []string, error) {
	var added []Ref
	var removed []string
	err := s.with(func(c *imapclient.Client) error {
		removed, s.stale = s.stale, nil
		// New messages: UID FETCH last+1:*, ignoring a UID below the range
		// (n:* always returns the last message).
		msgs, err := c.Fetch(imap.UIDSet{imap.UIDRange{Start: s.lastUID + 1}},
			&imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{channelHeader}}).Collect()
		if err != nil {
			return err
		}
		// Removed messages: compare UID SEARCH ALL with the known set.
		search, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
		if err != nil {
			return err
		}
		present := map[imap.UID]bool{}
		for _, u := range search.AllUIDs() {
			present[u] = true
		}
		for u := range s.known {
			if !present[u] {
				delete(s.known, u)
				removed = append(removed, strconv.FormatUint(uint64(u), 10))
			}
		}
		for _, m := range msgs {
			if m.UID <= s.lastUID || !present[m.UID] {
				continue
			}
			if _, ok := s.known[m.UID]; ok {
				continue
			}
			tag := headerValue(m.FindBodySection(channelHeader), "tandem-channel")
			s.known[m.UID] = tag
			added = append(added, Ref{strconv.FormatUint(uint64(m.UID), 10), tag})
		}
		for _, m := range msgs {
			s.lastUID = max(s.lastUID, m.UID)
		}
		return nil
	})
	return added, removed, err
}

// headerValue finds one header in a header block, unfolded and trimmed.
func headerValue(block []byte, name string) string {
	block = bytes.ReplaceAll(block, []byte("\r\n"), []byte("\n"))
	block = bytes.ReplaceAll(block, []byte("\n "), []byte(" "))
	block = bytes.ReplaceAll(block, []byte("\n\t"), []byte(" "))
	for _, line := range strings.Split(string(block), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func uidSet(ids []string) (imap.UIDSet, error) {
	var set imap.UIDSet
	for _, id := range ids {
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("imap: bad id %q", id)
		}
		set.AddNum(imap.UID(n))
	}
	return set, nil
}

var (
	mimeHeader = &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"Content-Type", "Content-Transfer-Encoding", "Tandem-Channel"}, Peek: true}
	textBody = &imap.FetchItemBodySection{Specifier: imap.PartSpecifierText, Peek: true}
)

func (s *IMAP) Fetch(ctx context.Context, ids []string) (map[string]Message, error) {
	set, err := uidSet(ids)
	if err != nil || len(ids) == 0 {
		return map[string]Message{}, err
	}
	out := map[string]Message{}
	err = s.with(func(c *imapclient.Client) error {
		msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{mimeHeader, textBody}}).Collect()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			head := m.FindBodySection(mimeHeader)
			env, err := tandem.DecodeBody(headerValue(head, "content-type"), headerValue(head, "content-transfer-encoding"),
				m.FindBodySection(textBody))
			if err != nil {
				continue // a message whose body doesn't decode is ignored (§10.4)
			}
			out[strconv.FormatUint(uint64(m.UID), 10)] = Message{Tag: headerValue(head, "tandem-channel"), Envelope: env}
		}
		return nil
	})
	return out, err
}

func (s *IMAP) Destroy(ctx context.Context, ids []string) error {
	set, err := uidSet(ids)
	if err != nil || len(ids) == 0 {
		return err
	}
	return s.with(func(c *imapclient.Client) error {
		if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
			return err
		}
		return c.UIDExpunge(set).Close() // UIDPLUS: only these messages
	})
}

// Watch holds IDLE on the selected folder and calls onChange on EXISTS and
// EXPUNGE. go-imap re-issues IDLE before the server's 29-minute limit.
func (s *IMAP) Watch(ctx context.Context, onChange func()) error {
	onChange()
	for {
		s.mu.Lock()
		var closed <-chan struct{}
		err := func() error {
			if s.c == nil {
				if err := s.connect(); err != nil {
					return err
				}
			}
			if s.c.Mailbox() == nil {
				if err := s.selectFolder(); err != nil {
					return err
				}
			}
			if s.idle == nil {
				idle, err := s.c.Idle()
				if err != nil {
					s.drop()
					return err
				}
				s.idle = idle
			}
			closed = s.c.Closed()
			return nil
		}()
		s.mu.Unlock()
		if errors.Is(err, ErrAuth) {
			return err
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second): // back-off before reconnecting
			}
			continue
		}
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.stopIdle()
			s.mu.Unlock()
			return ctx.Err()
		case <-s.wake:
			onChange()
		case <-s.kick: // a command ran; resume IDLE
		case <-closed:
			s.mu.Lock()
			s.drop()
			s.mu.Unlock()
			onChange() // anything may have happened while the connection was down
		}
	}
}

func (s *IMAP) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopIdle()
	if s.c != nil {
		s.c.Logout().Wait()
		s.c.Close()
		s.c = nil
	}
	return nil
}
