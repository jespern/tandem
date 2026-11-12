package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Probes against a real Fastmail account, for DESIGN §18 Q1–Q3. Run with
//
//	TANDEM_FASTMAIL=1 go test ./store -run Fastmail -v
//
// The API token is read from ~/.config/tandem/fastmail.json (a bare token,
// or a hub descriptor). Use a dedicated account: the probes create the
// Tandem folder and append messages, and destroy the messages afterwards.
// TANDEM_FASTMAIL_SCALE=n also runs the folder-size probe with n messages.

func fastmail(t *testing.T) Descriptor {
	t.Helper()
	if os.Getenv("TANDEM_FASTMAIL") == "" {
		t.Skip("set TANDEM_FASTMAIL=1 to probe Fastmail")
	}
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".config", "tandem", "fastmail.json"))
	if err != nil {
		t.Skip("no ~/.config/tandem/fastmail.json")
	}
	raw = bytes.TrimSpace(raw)
	if raw[0] == '{' {
		d, err := ParseDescriptor(raw)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return Descriptor{Kind: "jmap", Session: "https://api.fastmail.com/jmap/session", Token: string(raw)}
}

func openJMAP(t *testing.T, d Descriptor) *JMAP {
	t.Helper()
	j, err := OpenJMAP(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.EnsureFolder(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func sealed(t *testing.T, size int) (string, []byte) {
	k := rnd(32)
	tag := tandem.CollectionTag(k, rnd(32))
	env, err := tandem.Seal(k, tandem.KindRecord, tag, rnd(size), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tag, env
}

// Q2: are the Tandem-* headers accepted by Email/set and returned unchanged,
// in Email/get and in the stored message?
func TestFastmailHeaders(t *testing.T) {
	j := openJMAP(t, fastmail(t))
	ctx := context.Background()
	tag, env := sealed(t, 500)
	id, err := j.Append(ctx, tag, env)
	if err != nil {
		t.Fatalf("Email/set with Tandem headers: %v", err)
	}
	t.Cleanup(func() { j.Destroy(ctx, []string{id}) })

	r, err := j.calls(ctx, call{"Email/get", map[string]any{"accountId": j.account, "ids": []string{id},
		"properties": []string{"blobId", "header:Tandem-Channel:asText", "header:Tandem-Version:asText", "bodyStructure"}}})
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		List []struct {
			BlobID  string
			Channel string          `json:"header:Tandem-Channel:asText"`
			Version string          `json:"header:Tandem-Version:asText"`
			Body    json.RawMessage `json:"bodyStructure"`
		}
	}
	json.Unmarshal(r[0], &g)
	m := g.List[0]
	if strings.TrimSpace(m.Channel) != tag || strings.TrimSpace(m.Version) != "1" {
		t.Errorf("Email/get headers: channel %q version %q", m.Channel, m.Version)
	}
	raw, err := j.download(ctx, m.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	gotTag, gotEnv, err := tandem.ParseMessage(raw)
	if err != nil || gotTag != tag || !bytes.Equal(gotEnv, env) {
		t.Errorf("stored message: tag %q, envelope intact %v, err %v", gotTag, bytes.Equal(gotEnv, env), err)
	}
	head, _, _ := bytes.Cut(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
	t.Logf("stored headers:\n%s", head)
	t.Logf("body structure: %s", m.Body)
	msgs, err := j.Fetch(ctx, []string{id})
	if err != nil || !bytes.Equal(msgs[id].Envelope, env) {
		t.Errorf("Fetch through bodyValues: %v", err)
	}
}

// Q1: does EventSource push reach an API-token client, how fast, and what
// ping interval does the server report?
func TestFastmailPush(t *testing.T) {
	d := fastmail(t)
	writer, reader := openJMAP(t, d), openJMAP(t, d)
	ctx := context.Background()
	if _, _, err := reader.Changes(ctx); err != nil {
		t.Fatal(err)
	}
	events := make(chan struct{}, 64)
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); reader.Watch(wctx, func() { events <- struct{}{} }) }()
	t.Cleanup(func() { cancel(); <-done })
	<-events

	var lat []time.Duration
	var ids []string
	for i := 0; i < 5; i++ {
		tag, env := sealed(t, 200)
		start := time.Now()
		id, err := writer.Append(ctx, tag, env)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		for found := false; !found; {
			select {
			case <-events:
			case <-time.After(30 * time.Second):
				t.Fatalf("no push within 30 s")
			}
			added, _, err := reader.Changes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range added {
				found = found || a.Tag == tag
			}
		}
		lat = append(lat, time.Since(start))
	}
	writer.Destroy(ctx, ids)
	sort.Slice(lat, func(i, k int) bool { return lat[i] < lat[k] })
	t.Logf("append → seen by another client: min %v, median %v, max %v", lat[0].Round(time.Millisecond),
		lat[len(lat)/2].Round(time.Millisecond), lat[len(lat)-1].Round(time.Millisecond))

	// The ping interval the server reports, asking for 10 s.
	u := strings.NewReplacer("{types}", "Email", "{closeafter}", "no", "{ping}", "10").Replace(reader.esURL)
	pctx, pcancel := context.WithTimeout(ctx, 90*time.Second)
	defer pcancel()
	req, _ := http.NewRequestWithContext(pctx, "GET", reader.rebase(u), nil)
	reader.auth(req)
	resp, err := reader.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	opened := time.Now()
	event := ""
	for sc.Scan() {
		line := sc.Text()
		t.Logf("%5.1fs  %q", time.Since(opened).Seconds(), line)
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[6:])
		}
		if strings.HasPrefix(line, "data:") && event == "ping" {
			t.Logf("first ping after %v, data %s (asked for 10 s)", time.Since(opened).Round(time.Second), strings.TrimSpace(line[5:]))
			return
		}
	}
	t.Logf("no ping within 90 s (asked for 10 s)")
}

// Q1: rate limits. A burst of batched appends, then concurrent reads at the
// session's maxConcurrentRequests.
func TestFastmailBurst(t *testing.T) {
	j := openJMAP(t, fastmail(t))
	ctx := context.Background()
	const n = 500
	tags := make([]Ref, n)
	envs := make([][]byte, n)
	for i := range envs {
		tags[i].Tag, envs[i] = sealed(t, 300)
	}
	start := time.Now()
	ids, err := j.AppendBatch(ctx, tags, envs)
	t.Logf("%d appends in batches of %d: %v (err %v)", len(ids), j.maxSet, time.Since(start).Round(time.Millisecond), err)
	t.Cleanup(func() { j.Destroy(ctx, ids) })
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	errs := map[string]int{}
	var wg sync.WaitGroup
	start = time.Now()
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_, err := j.Fetch(ctx, ids[(w*10+i)*5:(w*10+i)*5+5])
				mu.Lock()
				errs[fmt.Sprint(err)]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	t.Logf("100 fetches, 10 at a time: %v, results %v", time.Since(start).Round(time.Millisecond), errs)
}

// Q3: a large folder. Appends n messages, then times a full resync, an
// incremental change and a fetch, and destroys them again.
func TestFastmailScale(t *testing.T) {
	d := fastmail(t)
	var n int
	fmt.Sscan(os.Getenv("TANDEM_FASTMAIL_SCALE"), &n)
	if n == 0 {
		t.Skip("set TANDEM_FASTMAIL_SCALE=n")
	}
	j := openJMAP(t, d)
	ctx := context.Background()
	var all []string
	t.Cleanup(func() {
		start := time.Now()
		j.Destroy(ctx, all)
		t.Logf("destroying %d: %v", len(all), time.Since(start).Round(time.Millisecond))
	})
	start := time.Now()
	for len(all) < n {
		b := min(1000, n-len(all))
		tags := make([]Ref, b)
		envs := make([][]byte, b)
		for i := range envs {
			tags[i].Tag, envs[i] = sealed(t, 400)
		}
		ids, err := j.AppendBatch(ctx, tags, envs)
		all = append(all, ids...)
		if err != nil {
			t.Fatalf("after %d appends: %v", len(all), err)
		}
	}
	t.Logf("appending %d: %v", n, time.Since(start).Round(time.Millisecond))

	fresh := openJMAP(t, d)
	start = time.Now()
	added, _, err := fresh.Changes(ctx)
	t.Logf("full resync, %d messages listed: %v (err %v)", len(added), time.Since(start).Round(time.Millisecond), err)
	tag, env := sealed(t, 400)
	id, _ := j.Append(ctx, tag, env)
	all = append(all, id)
	start = time.Now()
	added, _, err = fresh.Changes(ctx)
	t.Logf("incremental change after one append: %d added in %v (err %v)", len(added), time.Since(start).Round(time.Millisecond), err)
	start = time.Now()
	msgs, err := fresh.Fetch(ctx, all[:min(100, len(all))])
	t.Logf("fetching 100 bodies: %d in %v (err %v)", len(msgs), time.Since(start).Round(time.Millisecond), err)
}

// IMAP on Fastmail, for the record (JMAP is the binding Liszto uses). The app
// password is read from ~/.config/tandem/fastmail-imap.json.
func fastmailIMAP(t *testing.T) Descriptor {
	t.Helper()
	jd := fastmail(t)
	home, _ := os.UserHomeDir()
	pw, err := os.ReadFile(filepath.Join(home, ".config", "tandem", "fastmail-imap.json"))
	if err != nil {
		t.Skip("no ~/.config/tandem/fastmail-imap.json")
	}
	j := openJMAP(t, jd)
	r, err := j.do(context.Background(), "GET", jd.Session, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Username string }
	json.Unmarshal(r, &s)
	return Descriptor{Kind: "imap", Host: "imap.fastmail.com", Port: 993, User: s.Username, Password: string(bytes.TrimSpace(pw))}
}

// Several devices on one credential, each holding IDLE: does every one of
// them hear about a JMAP append, and how fast?
func TestFastmailIMAPIdle(t *testing.T) {
	id := fastmailIMAP(t)
	writer := openJMAP(t, fastmail(t))
	ctx := context.Background()
	const n = 6
	type watcher struct {
		s      Store
		events chan struct{}
	}
	var ws []watcher
	for i := 0; i < n; i++ {
		s := open(t, id)
		if _, _, err := s.Changes(ctx); err != nil {
			t.Fatal(err)
		}
		ev := make(chan struct{}, 64)
		wctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); s.Watch(wctx, func() { ev <- struct{}{} }) }()
		t.Cleanup(func() { cancel(); <-done })
		<-ev
		ws = append(ws, watcher{s, ev})
	}
	time.Sleep(time.Second) // let every connection enter IDLE
	tag, env := sealed(t, 200)
	start := time.Now()
	jid, err := writer.Append(ctx, tag, env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Destroy(ctx, []string{jid}) })
	var mu sync.Mutex
	var lat []time.Duration
	var wg sync.WaitGroup
	for i, w := range ws {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.After(60 * time.Second)
			for {
				select {
				case <-w.events:
				case <-deadline:
					t.Errorf("IDLE connection %d: no push within 60 s", i)
					return
				}
				added, _, err := w.s.Changes(ctx)
				if err != nil {
					t.Errorf("connection %d: %v", i, err)
					return
				}
				for _, a := range added {
					if a.Tag == tag {
						mu.Lock()
						lat = append(lat, time.Since(start))
						mu.Unlock()
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	sort.Slice(lat, func(i, k int) bool { return lat[i] < lat[k] })
	if len(lat) > 0 {
		t.Logf("%d of %d IDLE connections pushed; JMAP append → seen over IMAP: min %v, max %v", len(lat), n,
			lat[0].Round(time.Millisecond), lat[len(lat)-1].Round(time.Millisecond))
	}

	// And the other way: an IMAP append, read back over JMAP.
	tag2, env2 := sealed(t, 300)
	uid, err := ws[0].s.Append(ctx, tag2, env2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws[0].s.Destroy(ctx, []string{uid}) })
	found := false
	for i := 0; i < 10 && !found; i++ {
		added, _, _ := writer.Changes(ctx)
		var ids []string
		for _, a := range added {
			if a.Tag == tag2 {
				ids = append(ids, a.ID)
			}
		}
		msgs, _ := writer.Fetch(ctx, ids)
		for _, m := range msgs {
			found = found || bytes.Equal(m.Envelope, env2)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !found {
		t.Error("an IMAP append didn't read back intact over JMAP")
	}
}

// How many IMAP connections one credential may hold at once.
func TestFastmailIMAPConnectionCap(t *testing.T) {
	id := fastmailIMAP(t)
	ctx := context.Background()
	var open []*IMAP
	defer func() {
		for _, s := range open {
			s.Close()
		}
	}()
	for i := 0; i < 40; i++ {
		s, err := OpenIMAP(ctx, id)
		if err == nil {
			err = s.EnsureFolder(ctx)
		}
		if err != nil {
			t.Logf("connection %d refused: %v", i+1, err)
			return
		}
		open = append(open, s)
	}
	t.Logf("40 concurrent IMAP connections accepted on one credential")
}

// Fastmail keeps one EventSource connection per token: a second one with the
// same token closes the first, another token's is unaffected. Needs a second
// token in ~/.config/tandem/fastmail2.json.
func TestFastmailOneStreamPerToken(t *testing.T) {
	d1 := fastmail(t)
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".config", "tandem", "fastmail2.json"))
	if err != nil {
		t.Skip("no second token in ~/.config/tandem/fastmail2.json")
	}
	d2 := d1
	d2.Token = string(bytes.TrimSpace(raw))
	j1, j2 := openJMAP(t, d1), openJMAP(t, d2)

	// stream opens an EventSource and reports when the server closes it.
	stream := func(j *JMAP, ctx context.Context) <-chan string {
		out := make(chan string, 1)
		u := strings.NewReplacer("{types}", "Email", "{closeafter}", "no", "{ping}", "60").Replace(j.esURL)
		req, _ := http.NewRequestWithContext(ctx, "GET", j.rebase(u), nil)
		j.auth(req)
		go func() {
			resp, err := j.http.Do(req)
			if err != nil {
				out <- "error"
				return
			}
			defer resp.Body.Close()
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				if strings.TrimSpace(sc.Text()) == "event: close" {
					out <- "closed"
					return
				}
			}
			out <- "ended"
		}()
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := stream(j1, ctx)
	time.Sleep(3 * time.Second)
	b := stream(j2, ctx)
	time.Sleep(3 * time.Second)
	c := stream(j2, ctx) // same token as b
	select {
	case got := <-b:
		t.Logf("second connection with token 2: %s once a third opened", got)
	case <-time.After(5 * time.Second):
		t.Error("token 2's first connection survived a second one with the same token")
	}
	select {
	case got := <-a:
		t.Errorf("token 1's connection was %s by token 2's connections", got)
	case got := <-c:
		t.Errorf("token 2's newest connection was %s", got)
	case <-time.After(5 * time.Second):
		t.Log("token 1's connection and token 2's newest stayed open")
	}
}
