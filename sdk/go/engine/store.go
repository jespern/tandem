package engine

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"

	tandem "github.com/jespern/tandem/sdk/go"
	_ "github.com/mattn/go-sqlite3"
)

// Store is a device's durable state (§11.6): one SQLite file in WAL mode with full
// synchronous commits, written in transactions, so a crash at any point leaves the last
// committed state. The tables are the Swift engine's:
//
//   - values: small named values (the hub pin, the cursor, the clock, markers).
//   - objects: every control object held, by ID, as its exact JSON.
//   - messages: the hub messages seen: their channel, envelope hash and whether they were applied.
//   - writes: every record write held, by write ID, with its scope, record and epoch.
//   - journal: exact bytes signed for a step that hasn't finished exporting.
//   - held: messages waiting for something they depend on (§8.3).
//   - contents: which writes each kind-1 message carries (§10.7).
//   - chunks: the blob folder's index: each message's claimed address and tag, when this device
//     first saw it, and whether it verified or rejected it (BLOBS.md section 4.3).
//   - uploads: the upload journal: pending blob writes with their r and staged copy (section 6).
//
// Blob content lives in files, in BlobDir beside the database, never in SQLite.
//
// A Store belongs to one Device in one process.
type Store struct {
	db      *sql.DB
	conn    *sql.Conn
	mu      sync.Mutex
	depth   int
	ctx     context.Context
	blobDir string
	tempDir bool // blobDir is a temporary directory of a store in memory, removed at Close
}

const storeSchema = `
CREATE TABLE IF NOT EXISTS values_ (name TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS objects (id BLOB PRIMARY KEY, kind TEXT NOT NULL, json BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS messages (id TEXT PRIMARY KEY, channel TEXT NOT NULL, hash BLOB NOT NULL,
    applied INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS messages_hash ON messages (hash);
CREATE TABLE IF NOT EXISTS writes (id BLOB PRIMARY KEY, scope BLOB NOT NULL, record BLOB NOT NULL,
    epoch BLOB NOT NULL, payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS writes_record ON writes (scope, record);
CREATE TABLE IF NOT EXISTS journal (step TEXT PRIMARY KEY, bytes BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS held (id TEXT PRIMARY KEY, channel TEXT NOT NULL, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS contents (message TEXT NOT NULL, record BLOB NOT NULL, write BLOB NOT NULL,
    PRIMARY KEY (message, write));
CREATE INDEX IF NOT EXISTS messages_channel ON messages (channel);
CREATE INDEX IF NOT EXISTS contents_write ON contents (write);
CREATE TABLE IF NOT EXISTS chunks (id TEXT PRIMARY KEY, address BLOB NOT NULL, channel TEXT NOT NULL,
    seen INTEGER NOT NULL, state INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS chunks_address ON chunks (address);
CREATE TABLE IF NOT EXISTS uploads (write BLOB PRIMARY KEY, scope BLOB NOT NULL, record BLOB NOT NULL,
    field TEXT NOT NULL, epoch BLOB NOT NULL, payload BLOB NOT NULL, r BLOB NOT NULL, staged TEXT NOT NULL);
`

// OpenStore opens (or creates) the store at path; ":memory:" for tests.
func OpenStore(path string) (*Store, error) {
	dsn := "file:" + path + "?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000"
	if path == ":memory:" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, conn: conn, ctx: ctx, blobDir: path + ".blobs"}
	if path == ":memory:" {
		dir, err := os.MkdirTemp("", "tandem-blobs-")
		if err != nil {
			conn.Close()
			db.Close()
			return nil, err
		}
		s.blobDir, s.tempDir = dir, true
	}
	if _, err := conn.ExecContext(ctx, storeSchema); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// BlobDir is the directory blob content is kept in: path + ".blobs", or a temporary one for a
// store in memory.
func (s *Store) BlobDir() string { return s.blobDir }

// MemoryStore is a new store in memory, for tests.
func MemoryStore() *Store {
	s, err := OpenStore(":memory:")
	if err != nil {
		panic(err)
	}
	return s
}

func (s *Store) Close() error {
	s.conn.Close()
	err := s.db.Close()
	if s.tempDir {
		os.RemoveAll(s.blobDir)
	}
	return err
}

// Transaction runs fn in one transaction: all of it is committed, or none of it. A
// transaction inside another joins it.
func (s *Store) Transaction(fn func() error) (err error) {
	s.mu.Lock()
	s.depth++
	if s.depth == 1 {
		if _, err := s.conn.ExecContext(s.ctx, "BEGIN IMMEDIATE"); err != nil {
			s.depth--
			s.mu.Unlock()
			return err
		}
	}
	s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(err, fail("store: panic in a transaction: %v", r))
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.depth--
		if s.depth > 0 {
			return
		}
		if err != nil {
			s.conn.ExecContext(s.ctx, "ROLLBACK")
			return
		}
		if _, e := s.conn.ExecContext(s.ctx, "COMMIT"); e != nil {
			s.conn.ExecContext(s.ctx, "ROLLBACK")
			err = e
		}
	}()
	return fn()
}

func (s *Store) exec(q string, args ...any) error {
	_, err := s.conn.ExecContext(s.ctx, q, args...)
	return err
}

func (s *Store) changed(q string, args ...any) (bool, error) {
	r, err := s.conn.ExecContext(s.ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// rows runs a query and hands each row to scan, collecting everything before returning.
func (s *Store) rows(q string, args []any, scan func(*sql.Rows) error) error {
	r, err := s.conn.QueryContext(s.ctx, q, args...)
	if err != nil {
		return err
	}
	defer r.Close()
	for r.Next() {
		if err := scan(r); err != nil {
			return err
		}
	}
	return r.Err()
}

func blob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Values

// Value is a named value and whether it is set.
func (s *Store) Value(name string) ([]byte, bool) {
	var v []byte
	err := s.conn.QueryRowContext(s.ctx, "SELECT value FROM values_ WHERE name = ?", name).Scan(&v)
	if err != nil {
		return nil, false
	}
	if v == nil {
		v = []byte{}
	}
	return v, true
}

func (s *Store) SetValue(name string, value []byte) error {
	return s.exec("INSERT INTO values_ (name, value) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value", name, blob(value))
}

func (s *Store) RemoveValue(name string) error {
	return s.exec("DELETE FROM values_ WHERE name = ?", name)
}

// NamedValue is one value of Values.
type NamedValue struct {
	Name  string
	Value []byte
}

// Values is every named value whose name starts with prefix, by name.
func (s *Store) Values(prefix string) ([]NamedValue, error) {
	var out []NamedValue
	err := s.rows("SELECT name, value FROM values_ WHERE substr(name, 1, ?) = ? ORDER BY name", []any{len(prefix), prefix}, func(r *sql.Rows) error {
		var v NamedValue
		if err := r.Scan(&v.Name, &v.Value); err != nil {
			return err
		}
		out = append(out, v)
		return nil
	})
	return out, err
}

// Control objects

// PutObject keeps a control object's exact JSON; it reports false if it was already held.
func (s *Store) PutObject(o *tandem.Control) (bool, error) {
	id := oid(o)
	if id == nil {
		return false, fail("store: malformed %s", o.Kind)
	}
	j, err := cjson(o)
	if err != nil {
		return false, err
	}
	return s.changed("INSERT OR IGNORE INTO objects (id, kind, json) VALUES (?, ?, ?)", id, o.Kind, j)
}

// Objects returns the objects held, oldest first; kind "" is every kind.
func (s *Store) Objects(kind string) ([]*tandem.Control, error) {
	var raw [][]byte
	q, args := "SELECT json FROM objects ORDER BY rowid", []any{}
	if kind != "" {
		q, args = "SELECT json FROM objects WHERE kind = ? ORDER BY rowid", []any{kind}
	}
	if err := s.rows(q, args, func(r *sql.Rows) error {
		var b []byte
		err := r.Scan(&b)
		raw = append(raw, b)
		return err
	}); err != nil {
		return nil, err
	}
	out := make([]*tandem.Control, 0, len(raw))
	for _, b := range raw {
		o, err := tandem.FromJSON(b, "")
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// Hub messages

func (s *Store) NoteMessage(id, channel string, hash []byte) error {
	return s.exec("INSERT OR IGNORE INTO messages (id, channel, hash) VALUES (?, ?, ?)", id, channel, hash)
}

func (s *Store) MarkApplied(id string) error {
	return s.exec("UPDATE messages SET applied = 1 WHERE id = ?", id)
}

func (s *Store) ForgetMessages(ids []string) error {
	return s.Transaction(func() error {
		for _, id := range ids {
			if err := s.exec("DELETE FROM messages WHERE id = ?", id); err != nil {
				return err
			}
			if err := s.exec("DELETE FROM contents WHERE message = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

// MessageHash is a hub message ID with its envelope hash.
type MessageHash struct {
	ID   string
	Hash []byte
}

// Merged is the hub messages of one channel this device knows, with their envelope hashes (§10.7).
func (s *Store) Merged(channel string) ([]MessageHash, error) {
	var out []MessageHash
	err := s.rows("SELECT id, hash FROM messages WHERE channel = ? ORDER BY rowid", []any{channel}, func(r *sql.Rows) error {
		var m MessageHash
		err := r.Scan(&m.ID, &m.Hash)
		out = append(out, m)
		return err
	})
	return out, err
}

// Messages is the hub messages known in one channel, oldest first.
func (s *Store) Messages(channel string) ([]string, error) {
	return s.ids("SELECT id FROM messages WHERE channel = ? ORDER BY rowid", channel)
}

// MessagesWithHash is the hub message IDs holding an envelope with this hash (§10.3).
func (s *Store) MessagesWithHash(hash []byte) ([]string, error) {
	return s.ids("SELECT id FROM messages WHERE hash = ?", hash)
}

func (s *Store) ids(q string, arg any) ([]string, error) {
	var out []string
	err := s.rows(q, []any{arg}, func(r *sql.Rows) error {
		var id string
		err := r.Scan(&id)
		out = append(out, id)
		return err
	})
	return out, err
}

// Unapplied is the hub messages noted but not yet applied, oldest first.
func (s *Store) Unapplied() ([]tandem.Message, error) {
	var out []tandem.Message
	err := s.rows("SELECT id, channel FROM messages WHERE applied = 0 ORDER BY rowid", nil, func(r *sql.Rows) error {
		var m tandem.Message
		err := r.Scan(&m.ID, &m.Channel)
		out = append(out, m)
		return err
	})
	return out, err
}

// NoteContents records which writes a kind-1 message carries (§10.7, §11.6 step 6).
func (s *Store) NoteContents(message string, record []byte, writes [][]byte) error {
	return s.Transaction(func() error {
		for _, w := range writes {
			if err := s.exec("INSERT OR IGNORE INTO contents (message, record, write) VALUES (?, ?, ?)", message, record, w); err != nil {
				return err
			}
		}
		return nil
	})
}

// Content is one write a message carries.
type Content struct{ Record, Write []byte }

func (s *Store) Contents(message string) ([]Content, error) {
	var out []Content
	err := s.rows("SELECT record, write FROM contents WHERE message = ?", []any{message}, func(r *sql.Rows) error {
		var c Content
		err := r.Scan(&c.Record, &c.Write)
		out = append(out, c)
		return err
	})
	return out, err
}

// Writes

// PutWrite keeps one write; it reports false if it was already held.
func (s *Store) PutWrite(id, scope, record, epoch, payload []byte) (bool, error) {
	return s.changed("INSERT OR IGNORE INTO writes (id, scope, record, epoch, payload) VALUES (?, ?, ?, ?, ?)", id, scope, record, epoch, payload)
}

func (s *Store) HasWrite(id []byte) bool {
	var one int
	return s.conn.QueryRowContext(s.ctx, "SELECT 1 FROM writes WHERE id = ?", id).Scan(&one) == nil
}

// StoredWrite is one write as stored: the epoch it belongs to and its one-write payload.
type StoredWrite struct{ Epoch, Payload []byte }

func (s *Store) Writes(scope, record []byte) ([]StoredWrite, error) {
	var out []StoredWrite
	err := s.rows("SELECT epoch, payload FROM writes WHERE scope = ? AND record = ? ORDER BY rowid", []any{scope, record}, func(r *sql.Rows) error {
		var w StoredWrite
		err := r.Scan(&w.Epoch, &w.Payload)
		out = append(out, w)
		return err
	})
	return out, err
}

// Records is the record IDs with writes in a scope.
func (s *Store) Records(scope []byte) ([][]byte, error) {
	var out [][]byte
	err := s.rows("SELECT DISTINCT record FROM writes WHERE scope = ?", []any{scope}, func(r *sql.Rows) error {
		var b []byte
		err := r.Scan(&b)
		out = append(out, b)
		return err
	})
	return out, err
}

// Held messages (§8.3): readable, but waiting for something they depend on.

func (s *Store) Hold(m tandem.Message) error {
	return s.exec("INSERT OR IGNORE INTO held (id, channel, body) VALUES (?, ?, ?)", m.ID, m.Channel, blob(m.Body))
}

func (s *Store) Held() ([]tandem.Message, error) {
	var out []tandem.Message
	err := s.rows("SELECT id, channel, body FROM held ORDER BY rowid", nil, func(r *sql.Rows) error {
		var m tandem.Message
		err := r.Scan(&m.ID, &m.Channel, &m.Body)
		out = append(out, m)
		return err
	})
	return out, err
}

func (s *Store) Release(id string) error { return s.exec("DELETE FROM held WHERE id = ?", id) }

// Journal (§11.6)

func (s *Store) Journal(step string) ([]byte, bool) {
	var b []byte
	if s.conn.QueryRowContext(s.ctx, "SELECT bytes FROM journal WHERE step = ?", step).Scan(&b) != nil {
		return nil, false
	}
	return b, true
}

func (s *Store) SetJournal(step string, b []byte) error {
	return s.exec("INSERT INTO journal (step, bytes) VALUES (?, ?) ON CONFLICT (step) DO UPDATE SET bytes = excluded.bytes", step, blob(b))
}

func (s *Store) ClearJournal(step string) error {
	return s.exec("DELETE FROM journal WHERE step = ?", step)
}

func (s *Store) JournalSteps() ([]string, error) {
	var out []string
	err := s.rows("SELECT step FROM journal ORDER BY rowid", nil, func(r *sql.Rows) error {
		var st string
		err := r.Scan(&st)
		out = append(out, st)
		return err
	})
	return out, err
}

// OnFeed reports whether a message in the feed holds the write: the server answered this device's
// append of it, or a sync saw it (BLOBS.md section 7).
func (s *Store) OnFeed(write []byte) bool {
	var one int
	return s.conn.QueryRowContext(s.ctx, "SELECT 1 FROM contents JOIN messages ON contents.message = messages.id WHERE contents.write = ? LIMIT 1",
		write).Scan(&one) == nil
}

// The blob folder's index (BLOBS.md section 4.3)

const (
	chunkUnverified = 0
	chunkVerified   = 1 // this device had the body and saw it hash to its claim
	chunkRejected   = 2 // its body hashes to something else: absent everywhere, for good
)

// ChunkRow is one message of the blob folder as this device knows it.
type ChunkRow struct {
	ID, Channel string
	Address     []byte
	Seen        int64 // when this device first saw it, Unix milliseconds by its own clock
	State       int
}

func (s *Store) NoteChunks(refs []tandem.BlobRef, seen int64) error {
	for _, r := range refs {
		if err := s.exec("INSERT OR IGNORE INTO chunks (id, address, channel, seen) VALUES (?, ?, ?, ?)", r.ID, r.Address, r.Channel, seen); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ForgetChunks(ids []string) error {
	for _, id := range ids {
		if err := s.exec("DELETE FROM chunks WHERE id = ?", id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetChunkState(id string, state int) error {
	return s.exec("UPDATE chunks SET state = ? WHERE id = ?", state, id)
}

func (s *Store) chunkRows(q string, args ...any) ([]ChunkRow, error) {
	var out []ChunkRow
	err := s.rows(q, args, func(r *sql.Rows) error {
		var c ChunkRow
		err := r.Scan(&c.ID, &c.Address, &c.Channel, &c.Seen, &c.State)
		out = append(out, c)
		return err
	})
	return out, err
}

// ChunksWithAddress is every message claiming an address.
func (s *Store) ChunksWithAddress(address []byte) ([]ChunkRow, error) {
	return s.chunkRows("SELECT id, address, channel, seen, state FROM chunks WHERE address = ?", address)
}

// Chunks is the whole index.
func (s *Store) Chunks() ([]ChunkRow, error) {
	return s.chunkRows("SELECT id, address, channel, seen, state FROM chunks ORDER BY rowid")
}

// The upload journal (BLOBS.md section 6)

// Upload is one pending blob write: signed, not in the replica, its chunks going up.
type Upload struct {
	Write, Scope, Record, Epoch, Payload, R []byte
	Field, Staged                           string
}

func (s *Store) PutUpload(u Upload) error {
	return s.exec("INSERT OR REPLACE INTO uploads (write, scope, record, field, epoch, payload, r, staged) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		u.Write, u.Scope, u.Record, u.Field, u.Epoch, u.Payload, u.R, u.Staged)
}

func (s *Store) RemoveUpload(write []byte) error {
	return s.exec("DELETE FROM uploads WHERE write = ?", write)
}

func (s *Store) Uploads() ([]Upload, error) {
	var out []Upload
	err := s.rows("SELECT write, scope, record, field, epoch, payload, r, staged FROM uploads ORDER BY rowid", nil, func(r *sql.Rows) error {
		var u Upload
		err := r.Scan(&u.Write, &u.Scope, &u.Record, &u.Field, &u.Epoch, &u.Payload, &u.R, &u.Staged)
		out = append(out, u)
		return err
	})
	return out, err
}
