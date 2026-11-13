// Package imap is the IMAP binding of DESIGN §10.3 as a tandem.PushStore, on the Go
// standard library only. It is a port of the Swift binding (TandemIMAP) and behaves as it does.
//
// By default one connection both runs commands and idles between them, as §10.3 prescribes
// (servers cap connections per account); SeparateWatchConnection idles on a second one.
// QRESYNC is used when offered, otherwise UID SEARCH ALL; MULTIAPPEND and LITERAL+ batch
// appends. Message IDs are "<uidvalidity>.<uid>"; the cursor is opaque. After a UIDVALIDITY
// change, Changes reports every known ID as removed and the folder's messages as new.
// Every IDLE is sent in the same write as a NOOP before it: Stalwart reports a change made
// between the last command and IDLE only on the next command. On Gmail (X-GM-EXT-1) IDLE
// restarts every 3 s and destroyed messages are moved from All Mail to Trash and expunged.
// A lost connection is retried with backoff and reported as one invalidation once IDLE
// runs again. The only timers are the IDLE refresh, Gmail's IDLE restart and the
// reconnect backoff; nothing reconciles on a timer.
package imap

import (
	"context"
	"errors"
	"sync"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Security is how the client reaches the server. Only TLS belongs in production.
type Security int

const (
	// TLS is verified against the system's trust store and the host name.
	TLS Security = iota
	// TLSTrustingAnyCertificateForTesting accepts any certificate. For a disposable test server only.
	TLSTrustingAnyCertificateForTesting
	// PlaintextForTesting uses no TLS. For a loopback test server only.
	PlaintextForTesting
)

// IdleRestart says how often IDLE is ended and started again while watching.
type IdleRestart time.Duration

const (
	// IdleRestartAutomatic restarts every 3 s on Gmail (X-GM-EXT-1), which otherwise delays
	// pushes by ~30 s; elsewhere only the 29-minute refresh of RFC 2177.
	IdleRestartAutomatic IdleRestart = 0
	// IdleRestartNever keeps only the 29-minute refresh.
	IdleRestartNever IdleRestart = -1
)

// Config describes the account and the binding's options. Zero values take the defaults.
type Config struct {
	Host     string
	Port     int // default 993
	Username string
	Password string
	Security Security
	// Folder is the folder name, default "Tandem"; a personal namespace prefix (RFC 2342) is put in front of it.
	Folder string
	// IdleRestart is IdleRestartAutomatic, IdleRestartNever or a period (at most 29 minutes).
	IdleRestart IdleRestart
	// SeparateWatchConnection idles on a second connection instead of the one that runs commands.
	SeparateWatchConnection bool
	// DisableQRESYNC leaves QRESYNC unused even when offered; removals then come from UID SEARCH ALL.
	DisableQRESYNC bool
	// MaxMessageSize ignores larger messages (RFC822.SIZE); default 2 MiB. §10.6 caps a message at 1 MiB of envelope.
	MaxMessageSize int
	// ReconnectMin and ReconnectMax bound the reconnect backoff; defaults 1 s and 60 s.
	ReconnectMin, ReconnectMax time.Duration
	// Trace receives protocol lines for debugging, each cut at 200 bytes, from several
	// goroutines. Login credentials are left out.
	Trace func(string)
}

// Store is the IMAP binding as a tandem.PushStore. It connects on first use.
type Store struct {
	cfg  Config
	sink *sink
	link *link

	mu      sync.Mutex
	watcher *link
	watched bool
	closed  bool
}

var _ tandem.PushStore = (*Store)(nil)

// New checks the configuration and returns a store; it doesn't connect yet.
func New(cfg Config) (*Store, error) {
	if cfg.Host == "" || cfg.Username == "" {
		return nil, errors.New("imap: host and username are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 993
	}
	if cfg.Folder == "" {
		cfg.Folder = "Tandem"
	}
	if cfg.MaxMessageSize == 0 {
		cfg.MaxMessageSize = 2 << 20
	}
	if cfg.ReconnectMin == 0 {
		cfg.ReconnectMin = time.Second
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 60 * time.Second
	}
	if cfg.Port < 1 || cfg.Port > 65535 || cfg.MaxMessageSize < 0 || cfg.ReconnectMin < 0 || cfg.ReconnectMax < cfg.ReconnectMin {
		return nil, errors.New("imap: invalid configuration")
	}
	s := &Store{cfg: cfg, sink: newSink()}
	s.link = newLink(&s.cfg, !cfg.SeparateWatchConnection, s.sink)
	return s, nil
}

// EnsureFolder runs LIST, and CREATE if the folder is missing (ALREADYEXISTS is success).
// Other operations also create it when needed.
func (s *Store) EnsureFolder(ctx context.Context) error {
	_, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.retrying(ctx, func(c *conn) (any, error) { return nil, s.link.ensureFolder(ctx, c) })
	})
	return err
}

// Watch returns once IDLE runs. The channel receives a wake on EXISTS, EXPUNGE and
// VANISHED, and after every reconnect; wakes are coalesced while unread. It carries an
// error and closes only when reconnecting can't help (credentials, server); it closes at
// Close. Cancelling ctx ends IDLE.
func (s *Store) Watch(ctx context.Context) (<-chan tandem.Wake, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if s.watched {
		s.mu.Unlock()
		return nil, errors.New("imap: already watching")
	}
	s.watched = true
	target := s.link
	if s.cfg.SeparateWatchConnection {
		target = newLink(&s.cfg, true, s.sink)
		s.watcher = target
	}
	s.mu.Unlock()
	s.sink.attach(true)
	if err := target.startWatching(ctx); err != nil {
		s.sink.attach(false)
		s.mu.Lock()
		s.watched = false
		if target != s.link {
			s.watcher = nil
		}
		s.mu.Unlock()
		if target != s.link {
			target.close()
		}
		return nil, err
	}
	context.AfterFunc(ctx, target.stopWatching)
	return s.sink.ch, nil
}

// Changes reconciles from a cursor ("" for the start).
func (s *Store) Changes(ctx context.Context, cursorText string) (tandem.ChangeBatch, error) {
	cur, err := parseCursor(cursorText)
	if err != nil {
		return tandem.ChangeBatch{}, err
	}
	v, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.retrying(ctx, func(c *conn) (any, error) { return s.link.scan(ctx, c, cur) })
	})
	if err != nil {
		return tandem.ChangeBatch{}, err
	}
	return v.(tandem.ChangeBatch), nil
}

// Append stores one envelope. The ID is "" if the server named no UID.
func (s *Store) Append(ctx context.Context, channel string, body []byte) (string, error) {
	m, err := BuildMessage(channel, body, time.Now(), "")
	if err != nil {
		return "", err
	}
	ids, err := s.appendRaw(ctx, [][]byte{m})
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// Outgoing is one envelope for AppendBatch.
type Outgoing struct {
	Channel string
	Body    []byte
}

// AppendBatch stores several envelopes in as few commands as the server allows (MULTIAPPEND,
// LITERAL+). IDs are in order; "" where the server named no UID.
func (s *Store) AppendBatch(ctx context.Context, messages []Outgoing) ([]string, error) {
	built := make([][]byte, len(messages))
	for i, m := range messages {
		b, err := BuildMessage(m.Channel, m.Body, time.Now(), "")
		if err != nil {
			return nil, err
		}
		built[i] = b
	}
	if len(built) == 0 {
		return []string{}, nil
	}
	return s.appendRaw(ctx, built)
}

// appendRaw appends whole messages. It is not retried: an append whose answer was lost may
// still have landed (§10.3).
func (s *Store) appendRaw(ctx context.Context, raws [][]byte) ([]string, error) {
	v, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.run(ctx, func(c *conn) (any, error) { return s.link.appendMessages(ctx, c, raws) })
	})
	if err != nil {
		return nil, err
	}
	return v.([]string), nil
}

// Fetch reads messages by ID, leaving out those that are gone, too large or don't decode.
func (s *Store) Fetch(ctx context.Context, ids []string) ([]tandem.Message, error) {
	v, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.retrying(ctx, func(c *conn) (any, error) { return s.link.fetch(ctx, c, ids) })
	})
	if err != nil {
		return nil, err
	}
	out, _ := v.([]tandem.Message)
	return out, nil
}

// Destroy removes messages for good (§10.7).
func (s *Store) Destroy(ctx context.Context, ids []string) error {
	_, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.retrying(ctx, func(c *conn) (any, error) { return nil, s.link.destroy(ctx, c, ids) })
	})
	return err
}

// Close drops the connections and closes the watch channel.
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	watcher := s.watcher
	s.mu.Unlock()
	if watcher != nil {
		watcher.close()
	}
	s.link.close()
	s.sink.finish(nil)
	return nil
}

// command is a test hook: one raw command on the ready connection.
func (s *Store) command(ctx context.Context, line string) ([]any, error) {
	v, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.run(ctx, func(c *conn) (any, error) {
			r, err := s.link.exec(ctx, c, line)
			return r.untagged, err
		})
	})
	out, _ := v.([]any)
	return out, err
}

// capabilities is a test hook: what the server offered.
func (s *Store) capabilities(ctx context.Context) map[string]bool {
	v, _ := s.link.do(ctx, func(context.Context) (any, error) { return s.link.caps, nil })
	caps, _ := v.(map[string]bool)
	return caps
}
