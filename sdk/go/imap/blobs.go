package imap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The blob folder of DESIGN.md §17.4.3: chunks, each one message, in a folder next to the
// feed's that no device idles on. A BlobStore lists the chunks' claimed addresses from their
// headers alone, fetches and decodes chunk bodies, appends chunks and reads the quota, on
// transfer connections of its own that open when work arrives and close at Release.

// BlobFolder is the blob folder's name; a personal namespace prefix goes in front of it.
const BlobFolder = "Tandem-Blobs"

// ChunkForm is how a chunk's body is carried (DESIGN.md §17.4.2).
type ChunkForm = tandem.ChunkForm

const (
	// FormEscaped is 8-bit content with a few bytes escaped, 2% larger than the envelope. Writers use it.
	FormEscaped = tandem.FormEscaped
	// FormBase64 is for a server seen to damage the escaped form; 37% larger.
	FormBase64 = tandem.FormBase64
)

// chunkHeaderFields are the header fields a chunk reader fetches with the body.
var chunkHeaderFields = []string{"Tandem-Channel", "Tandem-Blob", "Tandem-Body", "Content-Type", "Content-Transfer-Encoding"}

// BuildChunkMessage returns a chunk message: the headers of DESIGN §10.4 with Tandem-Blob (and
// Tandem-Body for the escaped form), application/octet-stream, CRLF line ends. An empty id picks a
// random Message-ID.
func BuildChunkMessage(channel string, envelope []byte, form ChunkForm, date time.Time, id string) ([]byte, error) {
	if channel == "" || len(channel) > 900 {
		return nil, formatError("channel tag must be printable ASCII")
	}
	for i := 0; i < len(channel); i++ {
		if channel[i] <= 0x20 || channel[i] >= 0x7f {
			return nil, formatError("channel tag must be printable ASCII")
		}
	}
	if len(envelope) == 0 {
		return nil, formatError("empty chunk")
	}
	if id == "" {
		var r [16]byte
		_, _ = rand.Read(r[:])
		id = hex.EncodeToString(r[:])
	}
	lines := []string{"From: Tandem <tandem@tandem.airforce>", "Date: " + rfc5322(date), "Message-ID: <" + id + "@tandem.airforce>",
		"Subject: tandem", "MIME-Version: 1.0", "Tandem-Version: 2", "Tandem-Channel: " + channel,
		"Tandem-Blob: " + tandem.B32(tandem.ChunkAddress(envelope))}
	var body []byte
	switch form {
	case FormEscaped:
		lines = append(lines, "Tandem-Body: escaped", "Content-Type: application/octet-stream", "Content-Transfer-Encoding: 8bit")
		body = tandem.Escape(envelope)
	case FormBase64:
		lines = append(lines, "Content-Type: application/octet-stream", "Content-Transfer-Encoding: base64")
		body = wrapped(envelope)
	default:
		return nil, formatError("unknown chunk form")
	}
	var b bytes.Buffer
	b.Grow(len(body) + 512)
	for _, line := range append(lines, "") {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	b.Write(body)
	return b.Bytes(), nil
}

// DecodeChunkMessage returns a chunk's envelope from its header fields and body text: the transfer
// encoding the header names is undone once, then the escaping if Tandem-Body says so. Only
// application/octet-stream is a chunk. The caller checks the envelope against its address.
func DecodeChunkMessage(header, text []byte) ([]byte, error) {
	contentType := "text/plain"
	if v := field("Content-Type", header); v != nil {
		contentType = string(v)
	}
	media, _, _ := strings.Cut(contentType, ";")
	if !strings.EqualFold(strings.TrimSpace(media), "application/octet-stream") {
		return nil, formatError("a chunk is application/octet-stream")
	}
	transfer := "7bit"
	if v := field("Content-Transfer-Encoding", header); v != nil {
		transfer = string(v)
	}
	content, err := undoTransferEncoding(transfer, text)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(string(field("Tandem-Body", header)), "escaped") {
		if content, err = tandem.Unescape(content); err != nil {
			return nil, formatError(err.Error())
		}
	}
	if len(content) == 0 {
		return nil, formatError("empty chunk")
	}
	return content, nil
}

// The blob folder's types are the core's, so the engine can take any binding.
type (
	BlobRef       = tandem.BlobRef
	BlobChanges   = tandem.BlobChanges
	Chunk         = tandem.Chunk
	OutgoingChunk = tandem.OutgoingChunk
	Quota         = tandem.Quota
)

var _ tandem.BlobHub = (*BlobStore)(nil)

// BlobOptions are the blob folder's settings. Zero values take the defaults.
type BlobOptions struct {
	// Folder is the blob folder's name, default BlobFolder.
	Folder string
	// TransferConnections is how many connections a transfer may use: 0 decides from the server,
	// 2 where it batches appends (MULTIAPPEND or LITERAL+) and 4 where it doesn't (DESIGN.md§17.); at most 8. Fewer are used when the server refuses one.
	TransferConnections int
	// BatchBytes bounds the message bytes of one append batch; default 6 MiB.
	BatchBytes int
	// FetchBatch is how many chunks one FETCH asks for; default 4.
	FetchBatch int
}

// BlobStore is the blob folder. It connects on first use; Release closes its connections.
type BlobStore struct {
	cfg  Config
	opts BlobOptions

	mu      sync.Mutex
	links   []*link
	refused bool // a connection beyond the first was refused since the last Release
	closed  bool
}

// NewBlobStore returns the blob folder of the account cfg describes. It doesn't connect yet.
func NewBlobStore(cfg Config, opts BlobOptions) (*BlobStore, error) {
	if opts.Folder == "" {
		opts.Folder = BlobFolder
	}
	if opts.TransferConnections < 0 || opts.TransferConnections > 8 || opts.BatchBytes < 0 || opts.FetchBatch < 0 {
		return nil, errors.New("imap: invalid blob options")
	}
	if opts.BatchBytes == 0 {
		opts.BatchBytes = 6 << 20
	}
	if opts.FetchBatch == 0 {
		opts.FetchBatch = 4
	}
	cfg.Folder = opts.Folder
	if cfg.MaxMessageSize == 0 || cfg.MaxMessageSize < 8<<20 {
		cfg.MaxMessageSize = 8 << 20 // a 4 MiB chunk is 5.7 MB in base64
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = 2000 // headers only
	}
	probe, err := New(cfg) // checks and completes the configuration the way the feed's does
	if err != nil {
		return nil, err
	}
	probe.Close()
	return &BlobStore{cfg: probe.cfg, opts: opts}, nil
}

// first returns the first transfer connection, making it if needed.
func (b *BlobStore) first() (*link, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	if len(b.links) == 0 {
		b.links = []*link{newLink(&b.cfg, false, newSink())}
	}
	return b.links[0], nil
}

// transfer returns up to want connections, the first already connected; it opens more up to the
// configured count and keeps what the server accepts.
func (b *BlobStore) transfer(ctx context.Context, want int) ([]*link, error) {
	l, err := b.first()
	if err != nil {
		return nil, err
	}
	caps, err := l.do(ctx, func(ctx context.Context) (any, error) {
		return l.retrying(ctx, func(c *conn) (any, error) { return l.caps, nil })
	})
	if err != nil {
		return nil, err
	}
	limit := b.opts.TransferConnections
	if limit == 0 {
		limit = 4
		if m := caps.(map[string]bool); m["MULTIAPPEND"] || m["LITERAL+"] {
			limit = 2
		}
	}
	want = max(1, min(want, limit))
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil, ErrClosed
		}
		if len(b.links) >= want || b.refused {
			out := append([]*link(nil), b.links[:min(want, len(b.links))]...)
			b.mu.Unlock()
			return out, nil
		}
		b.mu.Unlock()
		extra := newLink(&b.cfg, false, newSink())
		if _, err := extra.do(ctx, func(ctx context.Context) (any, error) { return extra.run(ctx, nop) }); err != nil {
			extra.close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			b.mu.Lock()
			b.refused = true // the server's connection cap, most likely: go on with fewer
			b.mu.Unlock()
			continue
		}
		b.mu.Lock()
		b.links = append(b.links, extra)
		b.mu.Unlock()
	}
}

// Connections reports how many transfer connections are open.
func (b *BlobStore) Connections() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.links)
}

// Release closes the transfer connections. The next operation opens them again.
func (b *BlobStore) Release() {
	b.mu.Lock()
	links := b.links
	b.links, b.refused = nil, false
	b.mu.Unlock()
	for _, l := range links {
		l.close()
	}
}

// Close releases the connections for good.
func (b *BlobStore) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.Release()
	return nil
}

// Changes is one page of the blob folder's changes since cursorText ("" for the start), from the
// headers alone: no body is fetched. Messages without both claims are left out.
func (b *BlobStore) Changes(ctx context.Context, cursorText string) (BlobChanges, error) {
	cur, err := parseCursor(cursorText)
	if err != nil {
		return BlobChanges{}, err
	}
	l, err := b.first()
	if err != nil {
		return BlobChanges{}, err
	}
	v, err := l.do(ctx, func(ctx context.Context) (any, error) {
		return l.retrying(ctx, func(c *conn) (any, error) {
			added, removed, more, next, err := scanFolder(l, ctx, c, cur, l.fetchRefs, l.decodeRef)
			return BlobChanges{Added: added, Removed: removed, Cursor: next, More: more}, err
		})
	})
	if err != nil {
		return BlobChanges{}, err
	}
	return v.(BlobChanges), nil
}

func (l *link) fetchRefs(ctx context.Context, c *conn, set string) (map[uint32]*fetchItem, error) {
	return l.fetchWith(ctx, c, fmt.Sprintf("UID FETCH %s (UID BODY.PEEK[HEADER.FIELDS (Tandem-Channel Tandem-Blob)])", set))
}

func (l *link) decodeRef(item *fetchItem, uid uint32) (BlobRef, bool) {
	header, ok := item.sections["HEADER.FIELDS"]
	if !ok {
		return BlobRef{}, false
	}
	channel := field("Tandem-Channel", header)
	address, err := tandem.ParseB32(string(field("Tandem-Blob", header)))
	if len(channel) == 0 || err != nil || len(address) != 32 {
		return BlobRef{}, false
	}
	return BlobRef{ID: messageID(l.validity, uid), Channel: string(channel), Address: address}, true
}

func (l *link) fetchChunkItems(ctx context.Context, c *conn, set string) (map[uint32]*fetchItem, error) {
	return l.fetchWith(ctx, c, fmt.Sprintf("UID FETCH %s (UID RFC822.SIZE BODY.PEEK[HEADER.FIELDS (%s)] BODY.PEEK[TEXT]<0.%d>)",
		set, strings.Join(chunkHeaderFields, " "), l.cfg.MaxMessageSize))
}

func (l *link) fetchWith(ctx context.Context, c *conn, cmd string) (map[uint32]*fetchItem, error) {
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

// fetchChunks reads chunk messages by ID; those that are gone, too large or don't decode are left out.
func (l *link) fetchChunks(ctx context.Context, c *conn, ids []string) ([]Chunk, error) {
	uids := l.currentUIDs(ids)
	var out []Chunk
	for _, set := range chunks(uids) {
		items, err := l.fetchChunkItems(ctx, c, set)
		if err != nil {
			return nil, err
		}
		for _, uid := range sortedKeys(items) {
			item := items[uid]
			header, ok1 := item.sections["HEADER.FIELDS"]
			body, ok2 := item.sections["TEXT"]
			if !uids.contains(uid) || !ok1 || !ok2 || item.size == nil || *item.size > uint64(l.cfg.MaxMessageSize) {
				continue
			}
			if env, err := DecodeChunkMessage(header, body); err == nil {
				out = append(out, Chunk{ID: messageID(l.validity, uid), Envelope: env})
			}
		}
	}
	return out, nil
}

// Fetch reads chunks by ID over the transfer connections, a few to a command. The result follows
// the order of ids; IDs that are gone, too large or don't decode are left out.
func (b *BlobStore) Fetch(ctx context.Context, ids []string) ([]Chunk, error) {
	if len(ids) == 0 {
		return []Chunk{}, nil
	}
	var batches [][]string
	for i := 0; i < len(ids); i += b.opts.FetchBatch {
		batches = append(batches, ids[i:min(i+b.opts.FetchBatch, len(ids))])
	}
	links, err := b.transfer(ctx, len(batches))
	if err != nil {
		return nil, err
	}
	results := make([][]Chunk, len(batches))
	err = spread(ctx, links, len(batches), func(ctx context.Context, l *link, i int) error {
		v, err := l.do(ctx, func(ctx context.Context) (any, error) {
			return l.retrying(ctx, func(c *conn) (any, error) { return l.fetchChunks(ctx, c, batches[i]) })
		})
		if err == nil {
			results[i], _ = v.([]Chunk)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]Chunk{}
	for _, r := range results {
		for _, c := range r {
			byID[c.ID] = c
		}
	}
	out := make([]Chunk, 0, len(ids))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// Append stores chunks in batches of a few MiB over the transfer connections, each batch in as
// few commands as the server allows (MULTIAPPEND). IDs follow the order of chunks; "" where the
// server named no UID. It is not retried: an append whose answer was lost may have landed, which
// the caller learns from the next Changes.
func (b *BlobStore) Append(ctx context.Context, outgoing []OutgoingChunk, form ChunkForm) ([]string, error) {
	built, err := buildChunks(outgoing, form)
	if err != nil || len(built) == 0 {
		return []string{}, err
	}
	var batches [][2]int // [start, end) into built
	for i := 0; i < len(built); {
		start, size := i, 0
		for i < len(built) && (i == start || size+len(built[i]) <= b.opts.BatchBytes) {
			size += len(built[i])
			i++
		}
		batches = append(batches, [2]int{start, i})
	}
	links, err := b.transfer(ctx, len(batches))
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(built))
	err = spread(ctx, links, len(batches), func(ctx context.Context, l *link, i int) error {
		lo, hi := batches[i][0], batches[i][1]
		v, err := l.do(ctx, func(ctx context.Context) (any, error) {
			return l.run(ctx, func(c *conn) (any, error) { return l.appendMessages(ctx, c, built[lo:hi]) })
		})
		if err == nil {
			copy(ids[lo:hi], v.([]string))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func buildChunks(outgoing []OutgoingChunk, form ChunkForm) ([][]byte, error) {
	built := make([][]byte, len(outgoing))
	for i, o := range outgoing {
		m, err := BuildChunkMessage(o.Channel, o.Envelope, form, time.Now(), "")
		if err != nil {
			return nil, err
		}
		built[i] = m
	}
	return built, nil
}

// spread runs n jobs over the links, each link taking the next job when it is free. The first
// error stops the jobs not yet started and is returned.
func spread(ctx context.Context, links []*link, n int, job func(context.Context, *link, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, n)
	for i := range n {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for _, l := range links {
		wg.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := job(ctx, l, i); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		})
	}
	wg.Wait()
	return first
}

// Destroy removes chunk messages for good; on Gmail through Trash, as the feed's (§10.3).
func (b *BlobStore) Destroy(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	l, err := b.first()
	if err != nil {
		return err
	}
	_, err = l.do(ctx, func(ctx context.Context) (any, error) {
		return l.retrying(ctx, func(c *conn) (any, error) { return nil, l.destroy(ctx, c, ids) })
	})
	return err
}

// Quota reads the storage quota of the blob folder's root (RFC 9208). A server without QUOTA, or
// one that reports no storage resource, gives Known false: unknown, never zero.
func (b *BlobStore) Quota(ctx context.Context) (Quota, error) {
	l, err := b.first()
	if err != nil {
		return Quota{}, err
	}
	v, err := l.do(ctx, func(ctx context.Context) (any, error) {
		return l.retrying(ctx, func(c *conn) (any, error) { return l.quota(ctx, c) })
	})
	if err != nil {
		return Quota{}, err
	}
	return v.(Quota), nil
}

func (l *link) quota(ctx context.Context, c *conn) (Quota, error) {
	if !l.caps["QUOTA"] {
		return Quota{}, nil
	}
	r, err := c.execute(ctx, []part{text("GETQUOTAROOT "), astring([]byte(l.folder))}, false, false)
	var rejected *RejectedError
	if errors.As(err, &rejected) {
		return Quota{}, nil
	}
	if err != nil {
		return Quota{}, err
	}
	for _, u := range r.untagged {
		if q, ok := u.(quotaData); ok {
			if s, ok := q.resources["STORAGE"]; ok {
				return Quota{Known: true, Used: int64(s[0]) * 1024, Limit: int64(s[1]) * 1024}, nil
			}
		}
	}
	return Quota{}, nil
}

// AppendChunks appends chunks to the blob folder on the feed's own connection, without selecting
// it: for a transfer of one batch, which takes less time than opening a transfer connection
// (DESIGN.md §17.4.3). folder "" is BlobFolder.
func (s *Store) AppendChunks(ctx context.Context, folder string, outgoing []OutgoingChunk, form ChunkForm) ([]string, error) {
	if folder == "" {
		folder = BlobFolder
	}
	built, err := buildChunks(outgoing, form)
	if err != nil || len(built) == 0 {
		return []string{}, err
	}
	v, err := s.link.do(ctx, func(ctx context.Context) (any, error) {
		return s.link.run(ctx, func(c *conn) (any, error) { return s.link.appendTo(ctx, c, s.link.prefix+folder, built) })
	})
	if err != nil {
		return nil, err
	}
	return v.([]string), nil
}
