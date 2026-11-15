package tandem

import (
	"context"
	"strconv"
	"strings"
)

// The blob folder as the engine sees it (DESIGN.md §17.4.3), beside the feed's PushStore. A
// binding lists the chunks' claimed addresses from their headers alone, fetches chunk envelopes,
// appends chunks, destroys them and reads the quota. Nothing here idles: a device asks.

// ChunkForm is how a chunk's body is carried (DESIGN.md §17.4.2).
type ChunkForm int

const (
	// FormEscaped is 8-bit content with a few bytes escaped, 2% larger than the envelope. Writers use it.
	FormEscaped ChunkForm = iota
	// FormBase64 is for a server seen to damage the escaped form; 37% larger.
	FormBase64
)

// BlobRef is a message in the blob folder as its headers claim it: the claims are unverified.
type BlobRef struct {
	ID      string
	Channel string
	Address []byte // 32 bytes
}

// BlobChanges is one page of what changed in the blob folder.
type BlobChanges struct {
	Added   []BlobRef
	Removed []string
	Cursor  string
	More    bool
}

// Chunk is a fetched chunk message, decoded but not verified against its address.
type Chunk struct {
	ID       string
	Envelope []byte
}

// OutgoingChunk is one envelope to append, under the channel tag of its scope.
type OutgoingChunk struct {
	Channel  string
	Envelope []byte
}

// Quota is the blob folder's storage quota (RFC 9208). Known is false where the server reports
// none; Used and Limit then mean nothing. Unknown is never zero.
type Quota struct {
	Known       bool
	Used, Limit int64 // bytes
}

// BlobHub is the blob folder of one hub.
type BlobHub interface {
	// Changes is one page of what changed since cursor ("" for the start), from headers alone.
	Changes(ctx context.Context, cursor string) (BlobChanges, error)
	// Append stores chunks in the given form; IDs in order, "" where the server named none. Not
	// retried: an append whose answer was lost may have landed.
	Append(ctx context.Context, chunks []OutgoingChunk, form ChunkForm) ([]string, error)
	// Fetch reads chunks by ID, in the order of ids, leaving out what is gone or doesn't decode.
	Fetch(ctx context.Context, ids []string) ([]Chunk, error)
	// Destroy removes chunk messages for good.
	Destroy(ctx context.Context, ids []string) error
	// Quota reads the storage quota.
	Quota(ctx context.Context) (Quota, error)
	// Release closes the connections the transfers used; the next call opens them again.
	Release()
}

// BlobIDLess orders blob message IDs the way every device does (DESIGN.md §17.4.3): by
// UIDVALIDITY, then UID, for "<validity>.<uid>"; plain numbers by value; anything else as text.
func BlobIDLess(a, b string) bool {
	av, au, aok := blobID(a)
	bv, bu, bok := blobID(b)
	if aok && bok {
		return av < bv || av == bv && au < bu
	}
	return a < b
}

func blobID(id string) (validity, uid uint64, ok bool) {
	head, tail, dotted := strings.Cut(id, ".")
	if !dotted {
		tail, head = head, "0"
	}
	v, e1 := strconv.ParseUint(head, 10, 64)
	u, e2 := strconv.ParseUint(tail, 10, 64)
	return v, u, e1 == nil && e2 == nil
}
