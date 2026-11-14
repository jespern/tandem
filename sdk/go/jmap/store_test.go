package jmap

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/jespern/tandem/sdk/go/engine"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

func TestSSEFramingAndLimits(t *testing.T) {
	for _, sep := range []string{"\n", "\r\n", "\r"} {
		s := &Store{account: "account", cfg: Config{MaxEventBytes: 1024}}
		count := 0
		state := func(account, value string) string {
			encoded, _ := json.Marshal(map[string]any{"changed": map[string]any{account: map[string]string{"Email": value}}})
			return "data: " + string(encoded)
		}
		raw := strings.Join([]string{"\ufeff: heartbeat", "", "event: state", state("other", "a"), "", "event: state", state("account", "b"), "", ""}, sep)
		e := s.readEvents(&http.Response{Body: io.NopCloser(strings.NewReader(raw))}, func() { count++ })
		if e != io.EOF || count != 1 {
			t.Fatalf("separator %q: wakes=%d err=%v", sep, count, e)
		}
	}
	s := &Store{cfg: Config{MaxEventBytes: 16}}
	if e := s.readEvents(&http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 100)))}, func() { t.Fatal("invalid stream woke") }); e == nil {
		t.Fatal("unbounded push line")
	}
}
func TestOriginTrust(t *testing.T) {
	s := &Store{cfg: FastmailConfig("example"), origins: map[string]bool{"https://api.fastmail.com:443": true}}
	for _, raw := range []string{"https://ams.api.fastmail.com/api", "https://api.fastmail.com/api"} {
		if e := s.trusted(raw); e != nil {
			t.Fatal(e)
		}
	}
	for _, raw := range []string{"https://api.fastmail.com.evil.test/api", "http://ams.api.fastmail.com/api", "https://ams.api.fastmail.com:8443/api", "https://user:secret@ams.api.fastmail.com/api"} {
		if s.trusted(raw) == nil {
			t.Fatal("trusted", raw)
		}
	}
	s.cfg.Fastmail = false
	if s.trusted("https://ams.api.fastmail.com/api") == nil {
		t.Fatal("generic config trusted regional host")
	}
}
func TestSSEReconnectAndClose(t *testing.T) {
	second := make(chan struct{})
	count := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		count++
		if count == 1 {
			return
		}
		close(second)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root, stop := context.WithCancel(context.Background())
	s := &Store{cfg: Config{Authorization: "Bearer example", RetryMin: time.Millisecond, RetryMax: time.Millisecond, MaxEventBytes: 1024}, client: server.Client(), origins: map[string]bool{origin(serverURL(server.URL)): true}, events: server.URL, ctx: root, cancel: stop}
	wakes, e := s.Watch(ctx)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-second:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case wake := <-wakes:
		if wake.Err != nil {
			t.Fatal(wake.Err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, ok := <-wakes; ok {
		t.Fatal("watch not closed")
	}
}
func TestRealJMAP(t *testing.T) {
	raw := os.Getenv("TANDEM_JMAP_FIXTURE")
	if raw == "" {
		t.Skip("run sdk/check_jmap.py for disposable Stalwart")
	}
	var fixture struct {
		SessionURL    string `json:"sessionUrl"`
		Authorization string `json:"authorization"`
	}
	if e := json.Unmarshal([]byte(raw), &fixture); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // disposable loopback fixture only
	cfg := Config{SessionURL: fixture.SessionURL, Authorization: fixture.Authorization, FolderName: "Tandem-go-" + time.Now().Format("150405.000000000"), PageSize: 2, HTTPClient: client}
	reader, e := Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	writer, e := Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Close()
	wakes, e := reader.Watch(ctx)
	if e != nil {
		t.Fatal(e)
	}
	initial, e := reader.Changes(ctx, "")
	if e != nil {
		t.Fatal(e)
	}
	key, hub, epoch := tandem.H([]byte("key")), tandem.H([]byte("hub")), tandem.H([]byte("epoch"))
	tag := tandem.CollectionTag(key, hub, epoch)
	var ids []string
	var bodies [][]byte
	for i := 0; i < 5; i++ {
		body, e := tandem.Seal(key, hub, tandem.KindRecord, tag, []byte{byte(i)})
		if e != nil {
			t.Fatal(e)
		}
		id, e := writer.Append(ctx, tag, body)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
		bodies = append(bodies, body)
	}
	select {
	case wake := <-wakes:
		if wake.Err != nil {
			t.Fatal(wake.Err)
		}
	case <-ctx.Done():
		t.Fatal("no push", ctx.Err())
	}
	batch, e := reader.Changes(ctx, initial.Cursor)
	if e != nil {
		t.Fatal(e)
	}
	if len(batch.Added) != 5 {
		t.Fatal("did not drain explicit pages", len(batch.Added))
	}
	replay, e := reader.Changes(ctx, initial.Cursor)
	if e != nil || !reflect.DeepEqual(replay, batch) {
		t.Fatal("cursor replay", e)
	}
	full, e := reader.Changes(ctx, "")
	if e != nil || len(full.Added) != 5 {
		t.Fatal("full paged scan", e, len(full.Added))
	}
	fetched, e := reader.Fetch(ctx, ids)
	if e != nil || len(fetched) != 5 {
		t.Fatal("fetch", e)
	}
	for i, id := range ids {
		found := false
		for _, m := range fetched {
			if m.ID == id {
				found = bytes.Equal(m.Body, bodies[i])
			}
		}
		if !found {
			t.Fatal("MIME round trip changed bytes")
		}
	}
	other, e := Open(ctx, Config{SessionURL: cfg.SessionURL, Authorization: cfg.Authorization, FolderName: cfg.FolderName + "-other", HTTPClient: client})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if e = other.Destroy(ctx, ids[:1]); e == nil {
		t.Fatal("destroyed another mailbox's mail")
	}
	if e = writer.Destroy(ctx, ids); e != nil {
		t.Fatal(e)
	}
	removed, e := reader.Changes(ctx, batch.Cursor)
	if e != nil || len(removed.Removed) != 5 {
		t.Fatal("removal", e, removed.Removed)
	}
}

func serverURL(raw string) *url.URL { u, _ := url.Parse(raw); return u }
func TestEngineUsesExplicitJMAPTransport(t *testing.T) {
	raw := os.Getenv("TANDEM_JMAP_FIXTURE")
	if raw == "" {
		t.Skip("run sdk/check_jmap.py")
	}
	var fixture struct {
		SessionURL    string `json:"sessionUrl"`
		Authorization string `json:"authorization"`
	}
	if e := json.Unmarshal([]byte(raw), &fixture); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := Config{SessionURL: fixture.SessionURL, Authorization: fixture.Authorization, FolderName: "Tandem-engine-go-" + time.Now().Format("150405.000000000"), HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	sender, e := Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer sender.Close()
	receiver, e := Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer receiver.Close()
	keys, e := tandem.NewDeviceKeys()
	if e != nil {
		t.Fatal(e)
	}
	founding, e := engine.Found(engine.MailboxAccount{Host: "imap.example.org", Port: 993, User: "fixture", Password: "not-used-for-jmap"}, keys, "Go JMAP", nil)
	if e != nil {
		t.Fatal(e)
	}
	local, remote := engine.MemoryStore(), engine.MemoryStore()
	defer local.Close()
	defer remote.Close()
	device, e := engine.Open(founding.Genesis, founding.DirectoryKey, keys, local, sender)
	if e != nil {
		t.Fatal(e)
	}
	defer device.Close()
	if e = device.Publish(ctx, founding); e != nil {
		t.Fatal(e)
	}
	list, e := device.CreateList(ctx, "JMAP engine")
	if e != nil {
		t.Fatal(e)
	}
	record := engine.NewRecordID()
	if e = device.SetField(ctx, list, record, "title", []byte("Push only")); e != nil {
		t.Fatal(e)
	}
	replica, e := engine.Open(founding.Genesis, founding.DirectoryKey, keys, remote, receiver)
	if e != nil {
		t.Fatal(e)
	}
	defer replica.Close()
	if _, e = replica.Sync(ctx); e != nil {
		t.Fatal(e)
	}
	items := replica.Items(list)
	if len(items) != 1 || items[0].Text("title") != "Push only" {
		t.Fatal("engine did not restore over JMAP", items)
	}
}
