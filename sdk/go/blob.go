package tandem

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// Blobs (DESIGN.md §17): chunk encryption, manifests and the escaped body form. No I/O.

const (
	KindChunk     byte = 4
	MaxChunk           = 4 * 1024 * 1024 // plaintext bytes in one chunk (DESIGN.md §17.4.1)
	DefaultChunk       = 1024 * 1024     // what a writer cuts files into
	DefaultInline      = 16 * 1024       // content up to this size goes in the manifest itself
	manifestEntry      = 68              // address (32) || key (32) || u32(length)
	escapedLine        = 490             // envelope bytes per line in the escaped form
)

var errChunk = errors.New("tandem: invalid chunk or authentication failed")

// ErrChunkLength means a chunk opened but holds another number of bytes than its manifest entry says.
var ErrChunkLength = errors.New("tandem: chunk length differs from its manifest entry")

// ChunkKey is the writer's key for one chunk: never the same for two contents (§17.4.1).
func ChunkKey(r, plaintext []byte) ([]byte, error) {
	if len(r) != 32 {
		return nil, errChunk
	}
	return KDF(r, H(plaintext), "tandem/v2/blob-chunk", 32), nil
}

func chunkAAD(hub []byte) []byte { return cat([]byte{'T', 'N', 'D', 'M', 2, KindChunk}, hub) }

// SealChunk returns the chunk's envelope. It is deterministic: the same key and plaintext give the same bytes.
func SealChunk(key, hub, plaintext []byte) ([]byte, error) {
	if len(key) != 32 || len(hub) != 32 || len(plaintext) < 1 || len(plaintext) > MaxChunk {
		return nil, errChunk
	}
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 6, 6+len(plaintext)+16)
	copy(out, []byte{'T', 'N', 'D', 'M', 2, KindChunk})
	return a.Seal(out, make([]byte, 12), plaintext, chunkAAD(hub)), nil
}

// OpenChunk returns the plaintext, which must be exactly length bytes (§17.5).
func OpenChunk(key, hub, envelope []byte, length int) ([]byte, error) {
	if len(key) != 32 || len(hub) != 32 || len(envelope) < 6+16+1 || !bytes.Equal(envelope[:6], []byte{'T', 'N', 'D', 'M', 2, KindChunk}) {
		return nil, errChunk
	}
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	pt, err := a.Open(nil, make([]byte, 12), envelope[6:], chunkAAD(hub))
	if err != nil {
		return nil, errChunk
	}
	if len(pt) != length {
		return nil, ErrChunkLength
	}
	return pt, nil
}

// ChunkAddress is H(envelope): the chunk's identity on every device.
func ChunkAddress(envelope []byte) []byte { return H(envelope) }

// ChunkEntry is one chunk of a manifest, in content order.
type ChunkEntry struct {
	Address []byte
	Key     []byte
	Length  int
}

// Manifest describes a blob: its chunks, or its bytes inline (Data non-nil, possibly empty).
type Manifest struct {
	Size   int64
	Hash   []byte
	Chunks []ChunkEntry
	Data   []byte
}

// Inline reports whether the content is in the manifest itself.
func (m *Manifest) Inline() bool { return m.Chunks == nil }

// ManifestKind is how a value of a _blob: field reads (§17.5).
type ManifestKind uint8

const (
	ManifestNone    ManifestKind = iota // no manifest at all: unreadable, blocks nothing
	ManifestV1                          // a manifest this client reads
	ManifestUnknown                     // a JSON object whose blob is an integer other than 1: unreadable, and it stops clean-up (§17.7)
)

// EncodeManifest returns the canonical writer form: keys blob, size, hash, then chunks or data.
func EncodeManifest(m *Manifest) ([]byte, error) {
	if (m.Chunks == nil) == (m.Data == nil) || len(m.Hash) != 32 || m.Size < 0 {
		return nil, errors.New("tandem: invalid manifest")
	}
	o := NewJSONObject().Int("blob", 1).Int("size", uint64(m.Size)).B64u("hash", m.Hash)
	if m.Chunks != nil {
		raw := make([]byte, 0, manifestEntry*len(m.Chunks))
		for _, e := range m.Chunks {
			raw = binary.BigEndian.AppendUint32(append(append(raw, e.Address...), e.Key...), uint32(e.Length))
		}
		o.B64u("chunks", raw)
	} else {
		o.B64u("data", m.Data)
	}
	b := o.Bytes()
	if _, kind := ParseManifest(b); kind != ManifestV1 {
		return nil, errors.New("tandem: invalid manifest")
	}
	return b, nil
}

// ParseManifest reads a _blob: field's value. Only a ManifestV1 result carries a manifest.
func ParseManifest(value []byte) (*Manifest, ManifestKind) {
	doc, err := ParseJSON(value)
	if err != nil || doc.Kind != KindObject {
		return nil, ManifestNone
	}
	version, ok := doc.Get("blob")
	if !ok || version.Kind != KindInt {
		return nil, ManifestNone
	}
	if version.Int != 1 {
		return nil, ManifestUnknown
	}
	size, err := doc.int("size")
	if err != nil || size < 0 || size > 1<<53 {
		return nil, ManifestNone
	}
	hash, err := doc.bytes("hash", 32)
	if err != nil {
		return nil, ManifestNone
	}
	_, hasChunks := doc.Get("chunks")
	_, hasData := doc.Get("data")
	if hasChunks == hasData {
		return nil, ManifestNone
	}
	if hasData {
		data, err := doc.bytes("data", -1)
		if err != nil || int64(len(data)) != size {
			return nil, ManifestNone
		}
		if data == nil {
			data = []byte{}
		}
		return &Manifest{Size: size, Hash: hash, Data: data}, ManifestV1
	}
	raw, err := doc.bytes("chunks", -1)
	if err != nil || len(raw) == 0 || len(raw)%manifestEntry != 0 {
		return nil, ManifestNone
	}
	m := &Manifest{Size: size, Hash: hash, Chunks: make([]ChunkEntry, 0, len(raw)/manifestEntry)}
	var total int64
	for i := 0; i < len(raw); i += manifestEntry {
		n := int(binary.BigEndian.Uint32(raw[i+64 : i+68]))
		if n < 1 || n > MaxChunk {
			return nil, ManifestNone
		}
		total += int64(n)
		m.Chunks = append(m.Chunks, ChunkEntry{Address: bytes.Clone(raw[i : i+32]), Key: bytes.Clone(raw[i+32 : i+64]), Length: n})
	}
	if total != size {
		return nil, ManifestNone
	}
	return m, ManifestV1
}

// Describe returns the manifest of content and its envelopes in order (§17.6, step 2), all in memory.
func Describe(r, hub, content []byte, chunkSize, inline int) (*Manifest, [][]byte, error) {
	if chunkSize < 1 || chunkSize > MaxChunk || len(r) != 32 || len(hub) != 32 {
		return nil, nil, errChunk
	}
	if len(content) <= inline {
		return &Manifest{Size: int64(len(content)), Hash: H(content), Data: append([]byte{}, content...)}, nil, nil
	}
	m := &Manifest{Size: int64(len(content)), Hash: H(content), Chunks: []ChunkEntry{}}
	var envelopes [][]byte
	for i := 0; i < len(content); i += chunkSize {
		piece := content[i:min(i+chunkSize, len(content))]
		key, _ := ChunkKey(r, piece)
		env, err := SealChunk(key, hub, piece)
		if err != nil {
			return nil, nil, err
		}
		m.Chunks = append(m.Chunks, ChunkEntry{Address: ChunkAddress(env), Key: key, Length: len(piece)})
		envelopes = append(envelopes, env)
	}
	return m, envelopes, nil
}

func escapedAnywhere(b byte) bool { return b == 0x00 || b == 0x0A || b == 0x0D || b == 0x3D }
func escapedAtStart(b byte) bool {
	return escapedAnywhere(b) || b == 0x09 || b == 0x20 || b == 0x2E || b == 0x3E || b == 0x46
}
func escapedAtEnd(b byte) bool { return escapedAnywhere(b) || b == 0x09 || b == 0x20 }

// Escape returns the escaped body form of §17.4.2: lines of at most 490 envelope bytes, CRLF after each.
func Escape(envelope []byte) []byte {
	out := make([]byte, 0, len(envelope)+len(envelope)/32+2*(len(envelope)/escapedLine+1))
	for i := 0; i < len(envelope); i += escapedLine {
		line := envelope[i:min(i+escapedLine, len(envelope))]
		for j, b := range line {
			var escape bool
			switch {
			case j == 0:
				escape = escapedAtStart(b)
			case j == len(line)-1:
				escape = escapedAtEnd(b)
			default:
				escape = escapedAnywhere(b)
			}
			if escape {
				out = append(out, '=', b+64)
			} else {
				out = append(out, b)
			}
		}
		out = append(out, '\r', '\n')
	}
	return out
}

// Unescape removes every CR and LF, then turns each '=' and the byte after it into that byte minus 64.
func Unescape(body []byte) ([]byte, error) {
	out := make([]byte, 0, len(body))
	pending := false
	for _, b := range body {
		if b == '\r' || b == '\n' {
			continue
		}
		switch {
		case pending:
			out = append(out, b-64)
			pending = false
		case b == '=':
			pending = true
		default:
			out = append(out, b)
		}
	}
	if pending {
		return nil, errors.New("tandem: escaped body ends in '='")
	}
	return out, nil
}
