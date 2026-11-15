package tandem

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

type blobVectors struct {
	MaxChunk int `json:"max_chunk"`
	Chunk    map[string]string
	Blobs    []struct {
		Name, Content, Manifest string
		ChunkSize               int `json:"chunk_size"`
		Inline                  int
		Envelopes               []string
	}
	Manifests []struct{ Name, Value, Result string }
	Escapes   []struct{ Input, Escaped string }
	Lenient   []struct{ Body, Envelope string } `json:"unescape_lenient"`
	Refused   []string                          `json:"unescape_refused"`
	OpenFails []struct {
		Name, Envelope string
		Length         int
	} `json:"open_fails"`
}

func blobs(t *testing.T) blobVectors {
	t.Helper()
	var v blobVectors
	fixture(t, "blobs.json", &v)
	return v
}

func TestBlobChunk(t *testing.T) {
	v := blobs(t)
	c := map[string][]byte{}
	for k, x := range v.Chunk {
		c[k] = unhex(x)
	}
	if v.MaxChunk != MaxChunk {
		t.Fatalf("max chunk %d, vectors say %d", MaxChunk, v.MaxChunk)
	}
	key, _ := ChunkKey(c["r"], c["plaintext"])
	env, err := SealChunk(key, c["hub"], c["plaintext"])
	if err != nil || !bytes.Equal(key, c["key"]) || !bytes.Equal(env, c["envelope"]) || !bytes.Equal(ChunkAddress(env), c["address"]) {
		t.Fatalf("chunk differs from the vector: %v", err)
	}
	if !bytes.Equal(Escape(env), c["escaped"]) {
		t.Fatal("escaped chunk differs from the vector")
	}
	pt, err := OpenChunk(key, c["hub"], env, len(c["plaintext"]))
	if err != nil || !bytes.Equal(pt, c["plaintext"]) {
		t.Fatalf("open: %v", err)
	}
	for _, f := range v.OpenFails {
		if _, err := OpenChunk(key, c["hub"], unhex(f.Envelope), f.Length); err == nil {
			t.Errorf("%s: opened", f.Name)
		}
	}
	if _, err := OpenChunk(key, c["hub"], env, len(c["plaintext"])-1); !errors.Is(err, ErrChunkLength) {
		t.Errorf("a wrong length must say so: %v", err)
	}
}

func TestBlobDescribe(t *testing.T) {
	v := blobs(t)
	r, hub := unhex(v.Chunk["r"]), unhex(v.Chunk["hub"])
	for _, b := range v.Blobs {
		m, envs, err := Describe(r, hub, unhex(b.Content), b.ChunkSize, b.Inline)
		if err != nil {
			t.Fatal(err)
		}
		enc, err := EncodeManifest(m)
		if err != nil || string(enc) != b.Manifest {
			t.Fatalf("%s: manifest %s, want %s (%v)", b.Name, enc, b.Manifest, err)
		}
		if len(envs) != len(b.Envelopes) {
			t.Fatalf("%s: %d envelopes, want %d", b.Name, len(envs), len(b.Envelopes))
		}
		parsed, kind := ParseManifest([]byte(b.Manifest))
		if kind != ManifestV1 {
			t.Fatalf("%s: does not parse", b.Name)
		}
		var content []byte
		for i, e := range parsed.Chunks {
			if hex.EncodeToString(envs[i]) != b.Envelopes[i] {
				t.Fatalf("%s: envelope %d differs", b.Name, i)
			}
			pt, err := OpenChunk(e.Key, hub, envs[i], e.Length)
			if err != nil {
				t.Fatal(err)
			}
			content = append(content, pt...)
		}
		if parsed.Inline() {
			content = parsed.Data
		}
		if !bytes.Equal(content, unhex(b.Content)) || !bytes.Equal(H(content), parsed.Hash) {
			t.Fatalf("%s: content differs", b.Name)
		}
	}
}

func TestBlobManifests(t *testing.T) {
	names := map[ManifestKind]string{ManifestNone: "none", ManifestV1: "manifest", ManifestUnknown: "unknown"}
	for _, c := range blobs(t).Manifests {
		if _, kind := ParseManifest([]byte(c.Value)); names[kind] != c.Result {
			t.Errorf("%s: %s, want %s", c.Name, names[kind], c.Result)
		}
	}
}

func TestBlobEscapes(t *testing.T) {
	v := blobs(t)
	for _, c := range v.Escapes {
		in, want := unhex(c.Input), unhex(c.Escaped)
		if got := Escape(in); !bytes.Equal(got, want) {
			t.Fatalf("escape %x: %x, want %x", in, got, want)
		}
		if got, err := Unescape(want); err != nil || !bytes.Equal(got, in) {
			t.Fatalf("unescape %x: %x %v", want, got, err)
		}
	}
	for _, c := range v.Lenient {
		if got, err := Unescape(unhex(c.Body)); err != nil || !bytes.Equal(got, unhex(c.Envelope)) {
			t.Errorf("lenient %s: %x %v", c.Body, got, err)
		}
	}
	for _, body := range v.Refused {
		if _, err := Unescape(unhex(body)); err == nil {
			t.Errorf("refused %s: accepted", body)
		}
	}
}

func TestBlobSealLimits(t *testing.T) {
	key, hub := make([]byte, 32), make([]byte, 32)
	if env, err := SealChunk(key, hub, make([]byte, MaxChunk)); err != nil || len(env) != 6+MaxChunk+16 {
		t.Fatalf("a full chunk: %v", err)
	}
	for _, n := range []int{0, MaxChunk + 1} {
		if _, err := SealChunk(key, hub, make([]byte, n)); err == nil {
			t.Errorf("sealed %d bytes", n)
		}
	}
}
