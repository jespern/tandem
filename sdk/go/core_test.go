package tandem

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type coreVectors struct {
	Material map[string]string `json:"material"`
	Derived  map[string]string `json:"derived"`
	Writes   []struct {
		Payload string
		Input   string `json:"sig_input"`
		ID      string
	} `json:"writes"`
	Plaintext, Symmetric, Inbox, Inventory string
	Merges                                 []struct {
		Indexes []int
		Kept    []string
		Deleted bool
	}
	Packs struct {
		Valid []struct {
			Payload string
			Records []struct {
				Record string
				Writes []string
			}
		}
		Invalid []string
	}
}

func unhex(s string) []byte {
	b, e := hex.DecodeString(s)
	if e != nil {
		panic(e)
	}
	return b
}
func fixture(t *testing.T, file string, into any) {
	t.Helper()
	b, e := os.ReadFile("../../vectors/v2/" + file)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, into); e != nil {
		t.Fatal(e)
	}
}
func core(t *testing.T) coreVectors {
	t.Helper()
	var v coreVectors
	fixture(t, "core.json", &v)
	return v
}
func decodeWrite(t *testing.T, v coreVectors, i int) *Write {
	t.Helper()
	r, _, e := ParseKind1(unhex(v.Writes[i].Payload), unhex(v.Material["hub"]), unhex(v.Material["scope"]))
	if e != nil {
		t.Fatal(e)
	}
	return r.Writes[0]
}

func TestV2Vectors(t *testing.T) {
	v := core(t)
	m := func(k string) []byte { return unhex(v.Material[k]) }
	d := map[string]string{
		"home_scope": hex.EncodeToString(HomeScope(m("hub"), m("uid"))),
		"named":      hex.EncodeToString(NamedID("é")), "named_decomposed": hex.EncodeToString(NamedID("e\u0301")),
		"commitment": hex.EncodeToString(Commit(m("key"))), "key_id": hex.EncodeToString(KeyID(m("hub"), m("agreement_public"))),
		"collection_tag": CollectionTag(m("key"), m("hub"), m("epoch")), "home_tag": HomeTag(m("key"), m("hub"), m("uid"), m("epoch")),
		"inbox_tag": InboxTag(m("uid"), m("hub")), "directory_key": hex.EncodeToString(DirectoryKey(m("key"), m("hub"))), "directory_tag": DirectoryTag(m("key"), m("hub")),
	}
	manager, e := ManagerKeys(m("management_seed"))
	if e != nil {
		t.Fatal(e)
	}
	d["manager_signing_public"] = hex.EncodeToString(manager.Signing.Public().(ed25519.PublicKey))
	d["manager_agreement_public"] = hex.EncodeToString(manager.Agreement.PublicKey().Bytes())
	for k, got := range d {
		if got != v.Derived[k] {
			t.Errorf("%s differs", k)
		}
	}
	inv, e := ParseInventory(unhex(v.Inventory))
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := inv.Bytes()
	if e != nil || !bytes.Equal(encoded, unhex(v.Inventory)) {
		t.Fatal("inventory mismatch", e)
	}
	for i, want := range v.Writes {
		w := decodeWrite(t, v, i)
		in, e := w.SigInput()
		if e != nil || hex.EncodeToString(in) != want.Input {
			t.Fatalf("write %d input: %v", i, e)
		}
		id, e := w.ID()
		if e != nil || hex.EncodeToString(id) != want.ID {
			t.Fatalf("write %d ID", i)
		}
		if e = w.Verify(m("signing_public")); e != nil {
			t.Fatal(e)
		}
		if e = w.Verify(w.Author); e == nil {
			t.Fatal("participant ID used as a device key")
		}
		if e = w.Sign(ed25519.NewKeyFromSeed(m("signing_seed"))); e != nil {
			t.Fatal(e)
		}
		body, e := RecordPayload(w.Record, []*Write{w})
		if e != nil {
			t.Fatal(e)
		}
		r, _, e := ParseKind1(body, w.Hub, w.Scope)
		if e != nil || r.Writes[0].Verify(m("signing_public")) != nil {
			t.Fatal("payload round trip", e)
		}
	}
	for _, test := range v.Merges {
		r, e := NewRecord(m("hub"), m("scope"), m("record"))
		if e != nil {
			t.Fatal(e)
		}
		// Reverse arrival and replay the complete input after incremental receipt.
		for i := len(test.Indexes) - 1; i >= 0; i-- {
			entry, e := NewEntry(decodeWrite(t, v, test.Indexes[i]), m("epoch"), 0)
			if e != nil {
				t.Fatal(e)
			}
			if e = r.Merge(entry); e != nil {
				t.Fatal(e)
			}
		}
		for _, i := range test.Indexes {
			entry, _ := NewEntry(decodeWrite(t, v, i), m("epoch"), 0)
			if e = r.Merge(entry); e != nil {
				t.Fatal(e)
			}
		}
		var ids []string
		for _, e := range r.Entries() {
			ids = append(ids, hex.EncodeToString(e.ID))
		}
		if !slices.Equal(ids, test.Kept) || r.Deleted() != test.Deleted {
			t.Fatalf("merge %v: %v", test.Indexes, ids)
		}
	}
}

func TestEnvelopeInteroperability(t *testing.T) {
	v := core(t)
	m := func(k string) []byte { return unhex(v.Material[k]) }
	tag := v.Derived["collection_tag"]
	pt := unhex(v.Plaintext)
	_, plain, e := Open(m("key"), m("hub"), tag, unhex(v.Symmetric))
	if e != nil || !bytes.Equal(plain, pt) {
		t.Fatal("symmetric fixture", e)
	}
	private, e := ecdh.X25519().NewPrivateKey(m("agreement_private"))
	if e != nil {
		t.Fatal(e)
	}
	plain, e = OpenInbox(m("hub"), m("uid"), private, unhex(v.Inbox))
	if e != nil || !bytes.Equal(plain, pt) {
		t.Fatal("inbox fixture", e)
	}
	for _, kind := range []byte{KindRecord, KindRequest} {
		sealed, e := Seal(m("key"), m("hub"), kind, tag, pt)
		if e != nil {
			t.Fatal(e)
		}
		got, p, e := Open(m("key"), m("hub"), tag, sealed)
		if e != nil || got != kind || !bytes.Equal(p, pt) {
			t.Fatal("symmetric round trip", e)
		}
	}
	sealed, e := SealInbox(m("hub"), m("uid"), m("agreement_public"), pt)
	if e != nil {
		t.Fatal(e)
	}
	plain, e = OpenInbox(m("hub"), m("uid"), private, sealed)
	if e != nil || !bytes.Equal(plain, pt) {
		t.Fatal("native HPKE round trip", e)
	}
	badHub := bytes.Repeat([]byte{1}, 32)
	if _, _, e = Open(m("key"), badHub, tag, unhex(v.Symmetric)); e == nil {
		t.Fatal("accepted another hub")
	}
	if _, e = OpenInbox(m("hub"), badHub, private, unhex(v.Inbox)); e == nil {
		t.Fatal("accepted another participant")
	}
	for n := 0; n < len(unhex(v.Symmetric)); n++ {
		if _, _, e = Open(m("key"), m("hub"), tag, unhex(v.Symmetric)[:n]); e == nil {
			t.Fatal("accepted truncation")
		}
	}
	bad := unhex(v.Symmetric)
	bad[4] = 1
	if _, _, e = Open(m("key"), m("hub"), tag, bad); e == nil {
		t.Fatal("accepted v1")
	}
	if _, _, e = Open(m("key"), m("hub"), strings.ToUpper(tag), unhex(v.Symmetric)); e == nil {
		t.Fatal("accepted uppercase channel")
	}
	if _, e = ParseInventory(append(unhex(v.Inventory), 0)); e == nil {
		t.Fatal("accepted trailing inventory data")
	}
	if dir := os.Getenv("TANDEM_INTEROP_DIR"); dir != "" {
		signature := ed25519.Sign(ed25519.NewKeyFromSeed(m("signing_seed")), pt)
		artifact, _ := json.Marshal(map[string]string{"inbox": hex.EncodeToString(sealed), "signature": hex.EncodeToString(signature)})
		if e = os.WriteFile(dir+"/go.json", artifact, 0600); e != nil {
			t.Fatal(e)
		}
	}
}

func TestStrictInputs(t *testing.T) {
	var j struct {
		Cases struct{ Invalid, Valid []struct{ Hex string } }
	}
	fixture(t, "json.json", &j)
	for _, c := range j.Cases.Invalid {
		if _, e := ParseJSON(unhex(c.Hex)); e == nil {
			t.Errorf("accepted invalid JSON %s", c.Hex)
		}
	}
	for _, c := range j.Cases.Valid {
		if _, e := ParseJSON(unhex(c.Hex)); e != nil {
			t.Error(e)
		}
	}
	var b struct {
		Cases struct{ Invalid, Valid []struct{ Text, Hex string } }
	}
	fixture(t, "b64u.json", &b)
	for _, c := range b.Cases.Invalid {
		if _, e := ParseB64u(c.Text); e == nil {
			t.Errorf("accepted b64u %q", c.Text)
		}
	}
	for _, c := range b.Cases.Valid {
		p, e := ParseB64u(c.Text)
		if e != nil || hex.EncodeToString(p) != c.Hex {
			t.Error("base64 mismatch", e)
		}
	}
	var ed struct {
		Cases struct {
			Signatures []struct {
				Public, Message, Signature string
				Valid                      bool
			}
		}
	}
	fixture(t, "ed25519.json", &ed)
	for i, c := range ed.Cases.Signatures {
		if VerifyStrict(unhex(c.Public), unhex(c.Message), unhex(c.Signature)) != c.Valid {
			t.Errorf("signature case %d", i)
		}
	}
}

func TestBindingsClockAndMutation(t *testing.T) {
	v := core(t)
	w := decodeWrite(t, v, 0)
	e, _ := NewEntry(w, unhex(v.Material["epoch"]), 0)
	r, _ := NewRecord(w.Hub, w.Scope, w.Record)
	if err := r.Merge(e); err != nil {
		t.Fatal(err)
	}
	w.Value[0] ^= 1
	if bytes.Equal(w.Value, r.Entries()[0].W.Value) {
		t.Fatal("caller mutation changed state")
	}
	copy := r.Entries()
	copy[0].W.Value[0] ^= 1
	if bytes.Equal(copy[0].W.Value, r.Entries()[0].W.Value) {
		t.Fatal("reader mutation changed state")
	}
	foreign := decodeWrite(t, v, 0)
	foreign.Hub[0] ^= 1
	other, _ := NewEntry(foreign, unhex(v.Material["epoch"]), 0)
	if Supersedes(e, other) || r.Merge(other) == nil {
		t.Fatal("accepted another hub")
	}
	m, c, err := NextClock(9, 65535, 0)
	if err != nil || m != 10 || c != 0 {
		t.Fatal("counter overflow")
	}
	if _, _, err = NextClock(maxMillis-1, 65535, 0); err == nil {
		t.Fatal("accepted clock exhaustion")
	}
}

func TestCrossSDK(t *testing.T) {
	if os.Getenv("TANDEM_VERIFY_INTEROP") != "1" {
		t.Skip("run sdk/check.py for cross-SDK verification")
	}
	v := core(t)
	m := func(k string) []byte { return unhex(v.Material[k]) }
	key, err := ecdh.X25519().NewPrivateKey(m("agreement_private"))
	if err != nil {
		t.Fatal(err)
	}
	senders, err := os.ReadDir(os.Getenv("TANDEM_INTEROP_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sender := range senders {
		if sender.IsDir() || !strings.HasSuffix(sender.Name(), ".json") {
			continue
		}
		language := strings.TrimSuffix(sender.Name(), ".json")
		data, err := os.ReadFile(os.Getenv("TANDEM_INTEROP_DIR") + "/" + language + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var artifact map[string]string
		if err = json.Unmarshal(data, &artifact); err != nil {
			t.Fatal(err)
		}
		if !VerifyStrict(m("signing_public"), unhex(v.Plaintext), unhex(artifact["signature"])) {
			t.Errorf("%s signature", language)
		}
		plain, err := OpenInbox(m("hub"), m("uid"), key, unhex(artifact["inbox"]))
		if err != nil || !bytes.Equal(plain, unhex(v.Plaintext)) {
			t.Errorf("%s ciphertext: %v", language, err)
		}
	}
}

type fakePushStore struct {
	mu                 sync.Mutex
	push               chan Wake
	subscribed, closed bool
	calls              int
	added              []Message
}

func (s *fakePushStore) Watch(context.Context) (<-chan Wake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed = true
	return s.push, nil
}
func (s *fakePushStore) Changes(ctx context.Context, cursor string) (ChangeBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.subscribed {
		return ChangeBatch{}, fmt.Errorf("not subscribed")
	}
	s.calls++
	b := ChangeBatch{Added: s.added, Cursor: fmt.Sprint(s.calls)}
	s.added = nil
	return b, nil
}
func (s *fakePushStore) Append(ctx context.Context, channel string, body []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, Message{"local", channel, bytes.Clone(body)})
	return "local", nil
}
func (s *fakePushStore) Fetch(context.Context, []string) ([]Message, error) { return nil, nil }
func (s *fakePushStore) Destroy(context.Context, []string) error            { return nil }
func (s *fakePushStore) Close() error                                       { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true; return nil }

func TestSessionPushSubmitReconnectAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel() // failure safety net only
	store := &fakePushStore{push: make(chan Wake, 4)}
	session, _ := NewSession(store, "", 4)
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()
	take := func() ChangeBatch {
		t.Helper()
		select {
		case b := <-session.Updates():
			return b
		case <-ctx.Done():
			t.Fatal("session stuck")
			return ChangeBatch{}
		}
	}
	if take().Cursor != "1" {
		t.Fatal("no initial sync")
	}
	store.push <- Wake{}
	if take().Cursor != "2" {
		t.Fatal("push did not reconcile")
	}
	id, err := session.Submit(ctx, "channel", []byte("local edit"))
	if err != nil || id != "local" {
		t.Fatal(err)
	}
	if b := take(); len(b.Added) != 1 || string(b.Added[0].Body) != "local edit" {
		t.Fatal("local submission was not delivered")
	}
	store.push <- Wake{}
	if take().Cursor != "4" {
		t.Fatal("reconnect did not reconcile")
	}
	cancel()
	<-done
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.closed || store.calls != 4 {
		t.Fatalf("closed=%v changes=%d", store.closed, store.calls)
	}
}

// Packs (§10.5): several records' states in one message; the vectors' bad forms are refused.
func TestPacks(t *testing.T) {
	v := core(t)
	hub, scope := unhex(v.Material["hub"]), unhex(v.Material["scope"])
	for _, p := range v.Packs.Valid {
		states, f, err := ParseKind1States(unhex(p.Payload), hub, scope)
		if err != nil || f != nil || len(states) != len(p.Records) {
			t.Fatal("pack", err, len(states))
		}
		for i, r := range p.Records {
			if hex.EncodeToString(states[i].Record) != r.Record || len(states[i].Writes) != len(r.Writes) {
				t.Fatal("pack entry", i)
			}
			for k, w := range states[i].Writes {
				if id, err := w.ID(); err != nil || hex.EncodeToString(id) != r.Writes[k] {
					t.Fatal("pack write", i, k)
				}
			}
		}
		// Writers may order JSON keys differently; what a reader parses must agree.
		again, err := PackPayload(states)
		if err != nil {
			t.Fatal(err)
		}
		back, _, err := ParseKind1States(again, hub, scope)
		if err != nil || len(back) != len(states) {
			t.Fatal("PackPayload reads back", err)
		}
		for i := range back {
			for k := range back[i].Writes {
				a, _ := back[i].Writes[k].ID()
				b, _ := states[i].Writes[k].ID()
				if !bytes.Equal(a, b) {
					t.Fatal("PackPayload reads back to other writes")
				}
			}
		}
		if _, _, err := ParseKind1(unhex(p.Payload), hub, scope); err == nil {
			t.Fatal("ParseKind1 takes one record only")
		}
	}
	for i, bad := range v.Packs.Invalid {
		if _, _, err := ParseKind1States(unhex(bad), hub, scope); err == nil {
			t.Fatal("accepted bad pack", i)
		}
	}
}
