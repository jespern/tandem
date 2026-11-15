package imap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// A disposable Stalwart in Docker, set up as conformance/stalwart.py and the Swift tests'
// Stalwart.swift do: the same pinned image, loopback ports, a generated account. Host ports
// are fixed at start so that a docker restart (the reconnect test) keeps them. The
// container carries the label tandem-imap-test and is removed after the run.
// Set TANDEM_IMAP_STALWART=1 to run; TANDEM_IMAP_TRACE=1 prints the protocol lines.

const stalwartImage = "stalwartlabs/stalwart:v1.1.0@sha256:f772aa6ef8cd8018969fa491640f1436b0270eb14e2c219b5f37f1a77020837e"

type stalwartServer struct {
	name               string
	httpPort, imapPort int
	username, password string
}

var stalwart *stalwartServer

func TestMain(m *testing.M) {
	if os.Getenv("TANDEM_IMAP_STALWART") != "1" {
		os.Exit(m.Run())
	}
	server, err := startStalwart()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Stalwart:", err)
		os.Exit(1)
	}
	stalwart = server
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-signals; ok {
			server.stop()
			os.Exit(1)
		}
	}()
	code := m.Run()
	signal.Stop(signals)
	close(signals)
	server.stop()
	os.Exit(code)
}

func docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %v: %s", args[0], err, bytes.TrimSpace(out))
	}
	return string(bytes.TrimSpace(out)), nil
}

// freePort is a loopback port nothing listens on right now.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func startStalwart() (*stalwartServer, error) {
	if _, err := docker("version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, err
	}
	var r [6]byte
	_, _ = rand.Read(r[:])
	s := &stalwartServer{name: "tandem-imap-test-" + hex.EncodeToString(r[:])}
	var err error
	if s.httpPort, err = freePort(); err != nil {
		return nil, err
	}
	if s.imapPort, err = freePort(); err != nil {
		return nil, err
	}
	if _, err = docker("run", "-d", "--name", s.name, "--init", "--label", "tandem-imap-test",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", s.httpPort), "-p", fmt.Sprintf("127.0.0.1:%d:993", s.imapPort),
		"-e", "STALWART_RECOVERY_ADMIN=admin:conformance-test-only", stalwartImage); err != nil {
		s.stop()
		return nil, err
	}
	err = func() error {
		if err := s.listening(8080, 0); err != nil {
			return err
		}
		if err := s.bootstrap(); err != nil {
			return err
		}
		before, err := s.listens(993)
		if err != nil {
			return err
		}
		if _, err = docker("restart", s.name); err != nil {
			return err
		}
		return s.listening(993, before)
	}()
	if err != nil {
		s.stop()
		return nil, err
	}
	return s, nil
}

func (s *stalwartServer) stop() { _, _ = docker("rm", "-fv", s.name) }

var (
	ansi        = regexp.MustCompile("\x1b\\[[0-9;]*m")
	listenStart = map[int]*regexp.Regexp{}
	listenMu    sync.Mutex
)

func listenPattern(port int) *regexp.Regexp {
	listenMu.Lock()
	defer listenMu.Unlock()
	if listenStart[port] == nil {
		listenStart[port] = regexp.MustCompile(fmt.Sprintf(`localPort[^=]*=\s*%d\b`, port))
	}
	return listenStart[port]
}

func isListen(line string, port int) bool {
	plain := ansi.ReplaceAllString(line, "")
	return strings.Contains(plain, "network.listen-start") && listenPattern(port).MatchString(plain)
}

// listens counts how many times Stalwart has reported a listener on this container port.
func (s *stalwartServer) listens(port int) (int, error) {
	log, err := docker("logs", s.name)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if isListen(line, port) {
			n++
		}
	}
	return n, nil
}

// listening follows the container's log until Stalwart reports a listener on the container
// port more often than after times. Counting avoids comparing host and VM clocks.
func (s *stalwartServer) listening(port, after int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // a safety net, not a poll
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", s.name)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { pw.CloseWithError(cmd.Wait()) }()
	defer func() {
		cancel()
		_, _ = io.Copy(io.Discard, pr) // until docker logs has exited
	}()
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 1<<16), 1<<20)
	seen := 0
	for scanner.Scan() {
		if isListen(scanner.Text(), port) {
			if seen++; seen > after {
				return nil
			}
		}
	}
	return fmt.Errorf("Stalwart did not listen on %d", port)
}

// bootstrap creates the account over JMAP, as conformance/stalwart.py does.
func (s *stalwartServer) bootstrap() error {
	origin := fmt.Sprintf("http://127.0.0.1:%d", s.httpPort)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:conformance-test-only"))
	call := func(method, target string, body any, out any) error {
		var reader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, target, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", basic)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("%s %s: %s: %s", method, target, resp.Status, data)
		}
		return json.Unmarshal(data, out)
	}
	var session struct {
		APIURL string `json:"apiUrl"`
	}
	if err := call("GET", origin+"/.well-known/jmap", nil, &session); err != nil {
		return err
	}
	api, err := url.Parse(session.APIURL)
	if err != nil {
		return err
	}
	settings := map[string]any{"defaultHostname": "mail.example.org", "defaultDomain": "example.org",
		"requestTlsCertificate": false, "generateDkimKeys": false,
		"tracer": map[string]any{"@type": "Stdout", "ansi": false, "buffered": false, "level": "info", "enable": true}}
	var result struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	target := origin + api.Path
	if api.RawQuery != "" {
		target += "?" + api.RawQuery
	}
	if err = call("POST", target, map[string]any{"using": []string{"urn:ietf:params:jmap:core", "urn:stalwart:jmap"},
		"methodCalls": []any{[]any{"x:Bootstrap/set", map[string]any{"update": map[string]any{"singleton": settings}}, "0"}}}, &result); err != nil {
		return err
	}
	if len(result.MethodResponses) == 0 || len(result.MethodResponses[0]) < 2 {
		return errors.New("bootstrap: no response")
	}
	var updated struct {
		Updated struct {
			Singleton struct {
				Username string `json:"username"`
				Secret   string `json:"secret"`
			} `json:"singleton"`
		} `json:"updated"`
	}
	if err = json.Unmarshal(result.MethodResponses[0][1], &updated); err != nil {
		return err
	}
	s.username, s.password = updated.Updated.Singleton.Username, updated.Updated.Singleton.Secret
	if s.username == "" {
		return fmt.Errorf("bootstrap: no account in %s", result.MethodResponses[0][1])
	}
	return nil
}

// Tests

func needStalwart(t *testing.T) {
	t.Helper()
	if stalwart == nil {
		t.Skip("set TANDEM_IMAP_STALWART=1 to test against a disposable Stalwart in Docker")
	}
}

type stalwartOptions struct {
	noQRESYNC, separate, verify bool
	password                    string
	pageSize                    int
}

func stalwartStore(t *testing.T, folder string, o stalwartOptions) *Store {
	t.Helper()
	cfg := Config{Host: "127.0.0.1", Port: stalwart.imapPort, Username: stalwart.username, Password: stalwart.password,
		Security: TLSTrustingAnyCertificateForTesting, Folder: folder, SeparateWatchConnection: o.separate,
		DisableQRESYNC: o.noQRESYNC, ReconnectMin: 250 * time.Millisecond, ReconnectMax: 2 * time.Second, PageSize: o.pageSize}
	if o.verify {
		cfg.Security = TLS
	}
	if o.password != "" {
		cfg.Password = o.password
	}
	if os.Getenv("TANDEM_IMAP_TRACE") != "" {
		cfg.Trace = func(line string) { fmt.Printf("[%s] %s\n", folder, line) }
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func folderName() string {
	var r [4]byte
	_, _ = rand.Read(r[:])
	return "Tandem-" + hex.EncodeToString(r[:])
}

func envelope(n int) []byte { return append([]byte(fmt.Sprintf("envelope %d ", n)), seq(n%200)...) }

func bodies(ms []tandem.Message) [][]byte {
	out := [][]byte{}
	for _, m := range ms {
		out = append(out, m.Body)
	}
	return out
}

func messageIDs(ms []tandem.Message) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func sameSet(a, b []string) bool {
	return reflect.DeepEqual(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// must(call())(t) is call's value, or the test fails with its error.
func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStalwartCredentialsAndCertificates(t *testing.T) {
	needStalwart(t)
	ctx := testContext(t)
	wrong := stalwartStore(t, folderName(), stalwartOptions{password: "wrong"})
	var auth *AuthError
	if err := wrong.EnsureFolder(ctx); !errors.As(err, &auth) {
		t.Fatal("wrong password:", err)
	}
	// The self-signed certificate fails verification unless the test override is set.
	verified := stalwartStore(t, folderName(), stalwartOptions{verify: true})
	var conn *ConnectionError
	if err := verified.EnsureFolder(ctx); !errors.As(err, &conn) {
		t.Fatal("self-signed certificate:", err)
	}
}

func TestStalwartFolderAppendChangesFetchDestroy(t *testing.T) {
	needStalwart(t)
	ctx := testContext(t)
	name := folderName()
	a, b := stalwartStore(t, name, stalwartOptions{}), stalwartStore(t, name, stalwartOptions{noQRESYNC: true})
	noErr(t, a.EnsureFolder(ctx))
	noErr(t, a.EnsureFolder(ctx)) // idempotent
	if listed := must(a.command(ctx, `LIST "" `+name))(t); len(listed) != 1 {
		t.Fatal(listed)
	}
	caps := slices.Sorted(func(yield func(string) bool) {
		for c := range a.capabilities(ctx) {
			if !yield(c) {
				return
			}
		}
	})
	t.Log("Stalwart capabilities:", strings.Join(caps, " "))

	empty := must(a.Changes(ctx, ""))(t)
	if len(empty.Added) != 0 || len(empty.Removed) != 0 {
		t.Fatal(empty)
	}
	first := must(a.Append(ctx, "chan-a", envelope(1)))(t)
	more := must(a.AppendBatch(ctx, []Outgoing{{"chan-b", envelope(2)}, {"chan-a", envelope(3)}}))(t)
	ids := append([]string{first}, more...)
	if len(slices.Compact(slices.Sorted(slices.Values(ids)))) != 3 || slices.Contains(ids, "") {
		t.Fatal(ids)
	}

	batch := must(a.Changes(ctx, empty.Cursor))(t)
	want := []tandem.Message{{ID: ids[0], Channel: "chan-a", Body: envelope(1)}, {ID: ids[1], Channel: "chan-b", Body: envelope(2)},
		{ID: ids[2], Channel: "chan-a", Body: envelope(3)}}
	if !reflect.DeepEqual(batch.Added, want) || len(batch.Removed) != 0 {
		t.Fatal(batch)
	}
	again := must(a.Changes(ctx, batch.Cursor))(t) // n:* returns the last message again; ignored
	if len(again.Added) != 0 || len(again.Removed) != 0 {
		t.Fatal(again)
	}

	reversed := append(slices.Clone(ids), "1.999999", "junk")
	slices.Reverse(reversed[:3])
	fetched := must(a.Fetch(ctx, reversed))(t)
	if !reflect.DeepEqual(bodies(fetched), [][]byte{envelope(1), envelope(2), envelope(3)}) || !reflect.DeepEqual(messageIDs(fetched), ids) {
		t.Fatal(fetched)
	}

	// Bodies another client stored differently (§10.4), and a message that isn't Tandem's.
	var raws [][]byte
	for _, s := range []string{pythonUTF16Base64, pythonQuotedPrintable} {
		raw, _ := base64.StdEncoding.DecodeString(s)
		raws = append(raws, raw)
	}
	must(a.appendRaw(ctx, append(raws, []byte("Subject: hello\r\n\r\nnot an envelope\r\n"))))(t)
	odd := must(a.Changes(ctx, again.Cursor))(t)
	if !reflect.DeepEqual(bodies(odd.Added), [][]byte{seq(60), seq(200)}) || odd.Added[0].Channel != "abcdefgh" || odd.Added[1].Channel != "abcdefgh" {
		t.Fatal(odd)
	}

	// The same history without QRESYNC (UID SEARCH ALL).
	plain := must(b.Changes(ctx, ""))(t)
	if !reflect.DeepEqual(messageIDs(plain.Added), append(slices.Clone(ids), messageIDs(odd.Added)...)) {
		t.Fatal(plain)
	}

	noErr(t, a.Destroy(ctx, []string{ids[0], odd.Added[0].ID}))
	gone := must(a.Changes(ctx, odd.Cursor))(t)
	if !sameSet(gone.Removed, []string{ids[0], odd.Added[0].ID}) || len(gone.Added) != 0 {
		t.Fatal(gone)
	}
	if f := must(a.Fetch(ctx, []string{ids[0]}))(t); len(f) != 0 {
		t.Fatal(f)
	}
	plainGone := must(b.Changes(ctx, plain.Cursor))(t)
	if !sameSet(plainGone.Removed, []string{ids[0], odd.Added[0].ID}) || len(plainGone.Added) != 0 {
		t.Fatal(plainGone)
	}
	none := must(a.Changes(ctx, gone.Cursor))(t)
	if len(none.Removed) != 0 {
		t.Fatal(none)
	}

	// A message moved out of the folder counts as removed, and moved back in as added.
	must(a.command(ctx, "CREATE "+name+"-other"))(t)
	_, uid, _ := parseMessageID(ids[1])
	must(a.command(ctx, fmt.Sprintf("UID MOVE %d %s-other", uid, name)))(t)
	moved := must(a.Changes(ctx, none.Cursor))(t)
	if !reflect.DeepEqual(moved.Removed, []string{ids[1]}) {
		t.Fatal(moved)
	}
	must(a.command(ctx, "SELECT "+name+"-other"))(t)
	must(a.command(ctx, "UID MOVE 1:* "+name))(t)
	back := must(a.Changes(ctx, moved.Cursor))(t)
	if !reflect.DeepEqual(bodies(back.Added), [][]byte{envelope(2)}) || back.Added[0].ID == ids[1] {
		t.Fatal(back)
	}
}

// Changes returns a page at a time (ChangeBatch.More), with and without QRESYNC: every message
// once, in order, the last page without More.
func TestStalwartChangesComeAPageAtATime(t *testing.T) {
	needStalwart(t)
	ctx := testContext(t)
	name := folderName()
	a := stalwartStore(t, name, stalwartOptions{pageSize: 3})
	b := stalwartStore(t, name, stalwartOptions{pageSize: 3, noQRESYNC: true})
	noErr(t, a.EnsureFolder(ctx))
	var out []Outgoing
	for i := range 8 {
		out = append(out, Outgoing{"chan-a", envelope(i)})
	}
	ids := must(a.AppendBatch(ctx, out))(t)
	noErr(t, a.Destroy(ctx, []string{ids[4]})) // a gap in the UIDs
	want := slices.Delete(slices.Clone(ids), 4, 5)
	for _, s := range []*Store{a, b} {
		var got []string
		var pages []bool
		cursor := ""
		for {
			batch := must(s.Changes(ctx, cursor))(t)
			got = append(got, messageIDs(batch.Added)...)
			pages = append(pages, batch.More)
			cursor = batch.Cursor
			if !batch.More {
				break
			}
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(pages, []bool{true, true, false}) {
			t.Fatal(got, pages)
		}
		// Fewer new messages than a page: one call, no search.
		two := must(a.AppendBatch(ctx, []Outgoing{{"chan-a", envelope(20)}, {"chan-a", envelope(21)}}))(t)
		next := must(s.Changes(ctx, cursor))(t)
		if !reflect.DeepEqual(messageIDs(next.Added), two) || next.More {
			t.Fatal(next)
		}
		want = append(want, two...)
	}
}

func TestStalwartUIDValidityChangeIsAFullResync(t *testing.T) {
	needStalwart(t)
	ctx := testContext(t)
	name := folderName()
	a, b, other := stalwartStore(t, name, stalwartOptions{}), stalwartStore(t, name, stalwartOptions{noQRESYNC: true}), stalwartStore(t, name, stalwartOptions{})
	id := must(a.Append(ctx, "c", envelope(1)))(t)
	before := must(a.Changes(ctx, ""))(t)
	plainBefore := must(b.Changes(ctx, ""))(t)
	// Another client deletes the folder while a and b have it selected.
	must(other.command(ctx, "UNSELECT"))(t)
	must(other.command(ctx, "DELETE "+name))(t)
	after := must(a.Changes(ctx, before.Cursor))(t)
	if !reflect.DeepEqual(after.Removed, []string{id}) || len(after.Added) != 0 {
		t.Fatal(after)
	}
	newID := must(a.Append(ctx, "c", envelope(2)))(t)
	v1, _, _ := parseMessageID(id)
	v2, _, _ := parseMessageID(newID)
	if v1 == v2 {
		t.Fatal(id, newID)
	}
	plainAfter := must(b.Changes(ctx, plainBefore.Cursor))(t)
	if !reflect.DeepEqual(plainAfter.Removed, []string{id}) || !reflect.DeepEqual(messageIDs(plainAfter.Added), []string{newID}) {
		t.Fatal(plainAfter)
	}
}

func median(v []time.Duration) time.Duration {
	s := slices.Sorted(slices.Values(v))
	return s[len(s)/2]
}

// Push via IDLE between two stores: an append and a destroy by one wake the other, on
// the shared connection and on a separate watch connection.
func TestStalwartIdlePush(t *testing.T) {
	needStalwart(t)
	for _, separate := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate=%v", separate), func(t *testing.T) {
			ctx := testContext(t)
			name := folderName()
			watcher, writer := stalwartStore(t, name, stalwartOptions{separate: separate}), stalwartStore(t, name, stalwartOptions{})
			noErr(t, writer.EnsureFolder(ctx))
			wakes := must(watcher.Watch(ctx))(t)
			var appends, expunges []time.Duration
			// Each sample first lets earlier invalidations (the one after connecting, repeated EXISTS) arrive.
			for n := range 5 {
				time.Sleep(200 * time.Millisecond)
				drain(wakes)
				t0 := time.Now()
				id := must(writer.Append(ctx, "c", envelope(n)))(t)
				if !wake(t, wakes, 10*time.Second) {
					t.Fatalf("no push for append %d", n)
				}
				appends = append(appends, time.Since(t0))
				must(watcher.Changes(ctx, ""))(t) // a command between pushes: IDLE ends and resumes
				time.Sleep(200 * time.Millisecond)
				drain(wakes)
				t2 := time.Now()
				noErr(t, writer.Destroy(ctx, []string{id}))
				if !wake(t, wakes, 10*time.Second) {
					t.Fatalf("no push for expunge %d", n)
				}
				expunges = append(expunges, time.Since(t2))
			}
			t.Logf("IDLE push latency, 5 samples: append median %v, max %v; expunge median %v, max %v",
				median(appends), slices.Max(appends), median(expunges), slices.Max(expunges))
			watcher.Close()
			for range wakes {
			}
		})
	}
}

func TestStalwartSessionEndToEnd(t *testing.T) {
	needStalwart(t)
	ctx := testContext(t)
	name := folderName()
	device, other := stalwartStore(t, name, stalwartOptions{}), stalwartStore(t, name, stalwartOptions{})
	noErr(t, other.EnsureFolder(ctx))
	session := must(tandem.NewSession(device, "", 8))(t)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- session.Run(runCtx) }()
	defer func() {
		stop()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Error("Run:", err)
		}
	}()
	// collect gathers added messages from the session's batches until enough holds.
	collect := func(enough func([]tandem.Message) bool) []tandem.Message {
		t.Helper()
		var seen []tandem.Message
		limit := time.After(10 * time.Second)
		for {
			select {
			case b, ok := <-session.Updates():
				if !ok {
					t.Fatal("session ended")
				}
				seen = append(seen, b.Added...)
				if enough(seen) {
					return seen
				}
			case <-limit:
				t.Fatalf("timed out with %v", seen)
			}
		}
	}
	if initial := collect(func([]tandem.Message) bool { return true }); len(initial) != 0 {
		t.Fatal(initial)
	}
	id := must(other.Append(ctx, "chan", envelope(7)))(t)
	pushed := collect(func(m []tandem.Message) bool { return len(m) > 0 })
	if !reflect.DeepEqual(pushed, []tandem.Message{{ID: id, Channel: "chan", Body: envelope(7)}}) {
		t.Fatal(pushed)
	}
	own := must(session.Submit(ctx, "mine", envelope(8)))(t)
	local := collect(func(m []tandem.Message) bool {
		return slices.ContainsFunc(m, func(x tandem.Message) bool { return x.ID == own })
	})
	if last := local[len(local)-1]; !reflect.DeepEqual(last, tandem.Message{ID: own, Channel: "mine", Body: envelope(8)}) {
		t.Fatal(local)
	}
}

// A change made between a store's last command and its IDLE (§10.3). Stalwart reports it
// only on the next command; the NOOP sent with IDLE is that command. The hook appends from
// another store at exactly that point. Without the NOOP, the outcome is only logged: it
// documents the server's behaviour rather than this binding's.
func TestStalwartChangeBeforeIdleIsReported(t *testing.T) {
	needStalwart(t)
	for _, withNoop := range []bool{true, false} {
		t.Run(fmt.Sprintf("noop=%v", withNoop), func(t *testing.T) {
			ctx := testContext(t)
			name := folderName()
			watcher, writer := stalwartStore(t, name, stalwartOptions{}), stalwartStore(t, name, stalwartOptions{})
			noErr(t, writer.EnsureFolder(ctx))
			start := must(watcher.Changes(ctx, ""))(t)
			wakes := must(watcher.Watch(ctx))(t)
			time.Sleep(300 * time.Millisecond) // the invalidation after connecting
			var appended error
			must(watcher.link.do(ctx, func(context.Context) (any, error) {
				l := watcher.link
				l.withoutNoop = !withNoop
				l.beforeIdle = func() {
					l.beforeIdle = nil
					_, appended = writer.Append(ctx, "gap", envelope(9))
				}
				return nil, nil
			}))(t)
			drain(wakes)
			must(watcher.Changes(ctx, start.Cursor))(t) // ends IDLE; the hook runs before IDLE resumes
			if appended != nil {
				t.Fatal(appended)
			}
			seen := wake(t, wakes, 5*time.Second)
			switch {
			case withNoop && !seen:
				t.Fatal("the change made before IDLE was not reported")
			case !withNoop:
				t.Logf("without the NOOP, Stalwart reported the change during IDLE within 5 s: %v", seen)
			}
		})
	}
}

// Restarting the server drops every connection; the watcher reconnects with backoff,
// reports an invalidation, and IDLE pushes resume. It restarts the shared container, so
// it runs last.
func TestStalwartZReconnectAfterServerRestart(t *testing.T) {
	needStalwart(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := folderName()
	watcher, writer := stalwartStore(t, name, stalwartOptions{}), stalwartStore(t, name, stalwartOptions{})
	noErr(t, writer.EnsureFolder(ctx))
	wakes := must(watcher.Watch(ctx))(t)
	time.Sleep(200 * time.Millisecond) // the invalidation after connecting arrives first
	drain(wakes)
	before := must(stalwart.listens(993))(t)
	t0 := time.Now()
	must(docker("restart", stalwart.name))(t)
	noErr(t, stalwart.listening(993, before))
	up := time.Now()
	if !wake(t, wakes, 60*time.Second) {
		t.Fatal("no invalidation after reconnecting")
	}
	t.Logf("server restarted in %v; the watcher reconnected and invalidated %v after the server listened again", up.Sub(t0), time.Since(up))
	t2 := time.Now()
	id := must(writer.Append(ctx, "c", envelope(1)))(t) // the writer reconnects on demand
	if !wake(t, wakes, 10*time.Second) {
		t.Fatal("no push after reconnecting")
	}
	t.Logf("push after reconnect: %v", time.Since(t2))
	if batch := must(watcher.Changes(ctx, ""))(t); !reflect.DeepEqual(messageIDs(batch.Added), []string{id}) {
		t.Fatal(batch)
	}
}
