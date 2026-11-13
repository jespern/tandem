package imap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// fakeGmail is a scripted stand-in for Gmail as far as §10.3 describes it, ported from
// GmailTests.swift: X-GM-EXT-1, no QRESYNC, MULTIAPPEND, LITERAL+ or ESEARCH, LOGIN only,
// and a label whose expunged messages stay in [Gmail]/All Mail until moved to [Gmail]/Trash
// and expunged there.
type fakeGmail struct {
	mu          sync.Mutex
	boxes       map[string]map[uint32]*stored
	next        map[string]uint32
	validity    map[string]int
	nextGmailID uint64
	selected    string
	// reported is the message count this session was last told about, so NOOP reports only a change.
	reported int
	// appendAfterNextFetch is a message another client appends right after the next body fetch
	// is answered. Like Stalwart, the server tells a session of it on its next command, not
	// when IDLE starts.
	appendAfterNextFetch []byte
	created              bool
	caps                 string
}

type stored struct {
	raw     []byte
	gmailID uint64
	deleted bool
}

const gmailCaps = "IMAP4rev1 IDLE NAMESPACE X-GM-EXT-1 UIDPLUS MOVE CONDSTORE SPECIAL-USE"

func newFakeGmail() *fakeGmail {
	return &fakeGmail{
		boxes:    map[string]map[uint32]*stored{"Tandem": {}, "[Gmail]/All Mail": {}, "[Gmail]/Trash": {}},
		next:     map[string]uint32{"Tandem": 1, "[Gmail]/All Mail": 100, "[Gmail]/Trash": 1},
		validity: map[string]int{"Tandem": 11, "[Gmail]/All Mail": 2, "[Gmail]/Trash": 3}, nextGmailID: 5000, caps: gmailCaps,
	}
}

func (g *fakeGmail) setCaps(caps string) { g.mu.Lock(); g.caps = caps; g.mu.Unlock() }

func (g *fakeGmail) setAppendAfterNextFetch(raw []byte) {
	g.mu.Lock()
	g.appendAfterNextFetch = raw
	g.mu.Unlock()
}

// gmailIDs are the Gmail IDs in a box.
func (g *fakeGmail) gmailIDs(box string) []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []uint64
	for _, m := range g.boxes[box] {
		out = append(out, m.gmailID)
	}
	slices.Sort(out)
	return out
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		return s[1 : len(s)-1]
	}
	return s
}

func (g *fakeGmail) sortedUIDs() []uint32 { return slices.Sorted(maps.Keys(g.boxes[g.selected])) }

// uids are the UIDs a set names in the selected box; "n:*" past the end names the last message, as in IMAP.
func (g *fakeGmail) uids(text string) []uint32 {
	all := g.sortedUIDs()
	top := uint32(1)
	if len(all) > 0 {
		top = max(all[len(all)-1], 1)
	}
	set, err := parseUIDSet(strings.ReplaceAll(text, "*", strconv.FormatUint(uint64(top), 10)))
	if err != nil {
		return nil
	}
	var out []uint32
	for _, u := range all {
		if set.contains(u) {
			out = append(out, u)
		}
	}
	return out
}

func (g *fakeGmail) seq(uid uint32) int { return slices.Index(g.sortedUIDs(), uid) + 1 }

func (g *fakeGmail) add(raw []byte, box string, id uint64) uint32 {
	uid := g.next[box]
	g.next[box] = uid + 1
	g.boxes[box][uid] = &stored{raw: raw, gmailID: id}
	return uid
}

func (g *fakeGmail) respond(c fakeCommand) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.answer(c)
}

var (
	reGmailID   = regexp.MustCompile(`X-GM-MSGID (\d+)`)
	reFetchGMID = regexp.MustCompile(`^UID FETCH (\S+) \(UID X-GM-MSGID\)$`)
	reFetchBody = regexp.MustCompile(`^UID FETCH (\S+) \(UID RFC822\.SIZE BODY\.PEEK\[HEADER\.FIELDS \(Tandem-Channel Content-Type Content-Transfer-Encoding\)\] BODY\.PEEK\[TEXT\]<0\.\d+>\)$`)
	reStore     = regexp.MustCompile(`^UID STORE (\S+) \+FLAGS\.SILENT \(\\Deleted\)$`)
	reExpunge   = regexp.MustCompile(`^UID EXPUNGE (\S+)$`)
	reMove      = regexp.MustCompile(`^UID MOVE (\S+) "\[Gmail\]/Trash"$`)
)

func searchLine(uids []uint32) string {
	s := "* SEARCH"
	for _, u := range uids {
		s += fmt.Sprintf(" %d", u)
	}
	return s
}

func (g *fakeGmail) answer(c fakeCommand) []string {
	t := c.text
	switch {
	case t == "CAPABILITY":
		return []string{"* CAPABILITY " + g.caps, "OK Thats all she wrote!"}
	case strings.HasPrefix(t, "LOGIN "):
		if t == `LOGIN alice "pass word"` {
			return []string{"OK [CAPABILITY " + g.caps + "] alice authenticated (Success)"}
		}
		return []string{"NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)"}
	case t == "NAMESPACE":
		return []string{`* NAMESPACE (("" "/")) NIL NIL`, "OK Success"}
	case t == `LIST "" Tandem`:
		if g.created {
			return []string{`* LIST (\HasNoChildren) "/" "Tandem"`, "OK Success"}
		}
		return []string{"OK Success"}
	case t == "CREATE Tandem":
		g.created = true
		return []string{"NO [ALREADYEXISTS] Duplicate folder name Tandem (Failure)"} // lost a race: fine
	case t == `LIST "" "*"`:
		return []string{`* LIST (\HasNoChildren) "/" "INBOX"`, `* LIST (\All \HasNoChildren) "/" "[Gmail]/All Mail"`,
			`* LIST (\HasNoChildren \Trash) "/" "[Gmail]/Trash"`, `* LIST (\HasNoChildren) "/" "Tandem"`, "OK Success"}
	case strings.HasPrefix(t, "SELECT "):
		name := unquote(t[7:])
		if g.boxes[name] == nil || (name == "Tandem" && !g.created) {
			return []string{"NO [NONEXISTENT] Unknown Mailbox: " + name}
		}
		g.selected = name
		g.reported = len(g.boxes[name])
		return []string{`* FLAGS (\Answered \Flagged \Draft \Deleted \Seen)`, fmt.Sprintf("* OK [UIDVALIDITY %d] UIDs valid.", g.validity[name]),
			fmt.Sprintf("* %d EXISTS", len(g.boxes[name])), fmt.Sprintf("* OK [UIDNEXT %d] Predicted next UID.", g.next[name]),
			"OK [READ-WRITE] " + name + " selected. (Success)"}
	case strings.HasPrefix(t, `APPEND Tandem (\Seen) {`) && len(c.literals) == 1:
		id := g.nextGmailID
		g.nextGmailID++
		uid := g.add(c.literals[0], "Tandem", id)
		g.add(c.literals[0], "[Gmail]/All Mail", id) // every message is also in All Mail
		if g.selected != "Tandem" {
			return []string{fmt.Sprintf("OK [APPENDUID 11 %d] (Success)", uid)}
		}
		g.reported = len(g.boxes["Tandem"])
		return []string{fmt.Sprintf("* %d EXISTS", g.reported), fmt.Sprintf("OK [APPENDUID 11 %d] (Success)", uid)}
	}
	if g.selected == "" {
		return []string{"BAD No mailbox selected"}
	}
	box := g.boxes[g.selected]
	if t == "UID SEARCH ALL" {
		return []string{searchLine(g.sortedUIDs()), "OK SEARCH completed (Success)"}
	}
	if strings.HasPrefix(t, "UID SEARCH ") && strings.Contains(t, "X-GM-MSGID") {
		wanted := map[uint64]bool{}
		for _, m := range reGmailID.FindAllStringSubmatch(t, -1) {
			n, _ := strconv.ParseUint(m[1], 10, 64)
			wanted[n] = true
		}
		var found []uint32
		for _, u := range g.sortedUIDs() {
			if wanted[box[u].gmailID] {
				found = append(found, u)
			}
		}
		return []string{searchLine(found), "OK SEARCH completed (Success)"}
	}
	if m := reFetchGMID.FindStringSubmatch(t); m != nil {
		var out []string
		for _, u := range g.uids(m[1]) {
			out = append(out, fmt.Sprintf("* %d FETCH (X-GM-MSGID %d UID %d)", g.seq(u), box[u].gmailID, u))
		}
		return append(out, "OK Success")
	}
	if m := reFetchBody.FindStringSubmatch(t); m != nil {
		var out []string
		for _, u := range g.uids(m[1]) {
			raw := box[u].raw
			split := bytes.Index(raw, []byte("\r\n\r\n"))
			head, body := string(raw[:split+2]), string(raw[split+4:])
			var fields string
			for _, line := range strings.Split(head, "\r\n") {
				l := strings.ToLower(line)
				if strings.HasPrefix(l, "tandem-channel:") || strings.HasPrefix(l, "content-type:") || strings.HasPrefix(l, "content-transfer-encoding:") {
					fields += line + "\r\n"
				}
			}
			fields += "\r\n"
			out = append(out, fmt.Sprintf(`* %d FETCH (UID %d RFC822.SIZE %d BODY[HEADER.FIELDS ("Tandem-Channel" "Content-Type" "Content-Transfer-Encoding")] {%d}`+"\r\n%s BODY[TEXT]<0> {%d}\r\n%s)",
				g.seq(u), u, len(raw), len(fields), fields, len(body), body))
		}
		if raw := g.appendAfterNextFetch; raw != nil {
			g.appendAfterNextFetch = nil
			g.nextGmailID++
			g.add(raw, "Tandem", g.nextGmailID)
		}
		return append(out, "OK Success")
	}
	if m := reStore.FindStringSubmatch(t); m != nil {
		for _, u := range g.uids(m[1]) {
			box[u].deleted = true
		}
		return []string{"OK Success"}
	}
	if m := reExpunge.FindStringSubmatch(t); m != nil {
		var out []string
		uids := g.uids(m[1])
		for i := len(uids) - 1; i >= 0; i-- {
			if u := uids[i]; box[u].deleted {
				out = append(out, fmt.Sprintf("* %d EXPUNGE", g.seq(u)))
				delete(box, u)
			}
		}
		return append(out, "OK Success")
	}
	if m := reMove.FindStringSubmatch(t); m != nil && g.selected == "[Gmail]/All Mail" {
		moving := g.uids(m[1])
		var destination []uint32
		for _, u := range moving {
			destination = append(destination, g.add(box[u].raw, "[Gmail]/Trash", box[u].gmailID))
		}
		out := []string{fmt.Sprintf("* OK [COPYUID 3 %s %s]", setOf(moving...), setOf(destination...))}
		for i := len(moving) - 1; i >= 0; i-- {
			out = append(out, fmt.Sprintf("* %d EXPUNGE", g.seq(moving[i])))
			delete(box, moving[i])
		}
		return append(out, "OK Success")
	}
	if t == "NOOP" {
		count := len(box)
		out := []string{"OK Success"}
		if count != g.reported {
			out = []string{fmt.Sprintf("* %d EXISTS", count), "OK Success"}
		}
		g.reported = count
		return out
	}
	return []string{"BAD Unknown command " + t}
}

type gmailFixture struct {
	gmail  *fakeGmail
	server *fakeServer
}

func newGmailFixture(t *testing.T) *gmailFixture {
	g := newFakeGmail()
	return &gmailFixture{g, startFake(t, "* OK Gimap ready for requests from 127.0.0.1", g.respond)}
}

func (f *gmailFixture) config(password string, restart IdleRestart) Config {
	return Config{Host: "127.0.0.1", Port: f.server.port(), Username: "alice", Password: password, Security: PlaintextForTesting,
		IdleRestart: restart, ReconnectMin: 50 * time.Millisecond, ReconnectMax: 200 * time.Millisecond}
}

func (f *gmailFixture) store(t *testing.T, password string, restart IdleRestart) *Store {
	t.Helper()
	s, err := New(f.config(password, restart))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// commands are the client lines without tags.
func (f *gmailFixture) commands() []string {
	var out []string
	for _, l := range f.server.lines() {
		_, after, found := strings.Cut(l, " ")
		if !found {
			after = l
		}
		out = append(out, after)
	}
	return out
}

func count(lines []string, pred func(string) bool) int {
	n := 0
	for _, l := range lines {
		if pred(l) {
			n++
		}
	}
	return n
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // a safety net for a hung test
	t.Cleanup(cancel)
	return ctx
}

// wake returns true if a wake arrives within the limit.
func wake(t *testing.T, ch <-chan tandem.Wake, limit time.Duration) bool {
	t.Helper()
	select {
	case w, ok := <-ch:
		if ok && w.Err != nil {
			t.Fatal(w.Err)
		}
		return ok
	case <-time.After(limit):
		return false
	}
}

func drain(ch <-chan tandem.Wake) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestAppendChangesAndDestroyCleanUpAllMailAndTrash(t *testing.T) {
	f := newGmailFixture(t)
	ctx := testContext(t)
	store := f.store(t, "pass word", IdleRestartAutomatic)
	if err := store.EnsureFolder(ctx); err != nil { // SELECT fails, LIST finds nothing, CREATE says ALREADYEXISTS: success
		t.Fatal(err)
	}
	first, err := store.Append(ctx, "chan", []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	more, err := store.AppendBatch(ctx, []Outgoing{{"chan", []byte{4}}, {"other", []byte{5}}})
	if err != nil {
		t.Fatal(err)
	}
	if ids := append([]string{first}, more...); !reflect.DeepEqual(ids, []string{"11.1", "11.2", "11.3"}) {
		t.Fatal(ids)
	}
	// No LITERAL+: every message waits for "+"; no MULTIAPPEND: one APPEND each.
	appendLine := regexp.MustCompile(`^APPEND Tandem \(\\Seen\) \{\d+\}$`)
	appends := slices.DeleteFunc(f.commands(), func(c string) bool { return !strings.HasPrefix(c, "APPEND") })
	if len(appends) != 3 || count(appends, appendLine.MatchString) != 3 {
		t.Fatal(appends)
	}

	batch, err := store.Changes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []tandem.Message{{ID: "11.1", Channel: "chan", Body: []byte{1, 2, 3}}, {ID: "11.2", Channel: "chan", Body: []byte{4}},
		{ID: "11.3", Channel: "other", Body: []byte{5}}}
	if !reflect.DeepEqual(batch.Added, want) {
		t.Fatal(batch.Added)
	}
	again, err := store.Changes(ctx, batch.Cursor) // "4:*" returns UID 3 again
	if err != nil || len(again.Added) != 0 {
		t.Fatal(again, err)
	}
	if !slices.Contains(f.commands(), "UID FETCH 4:* (UID RFC822.SIZE BODY.PEEK[HEADER.FIELDS (Tandem-Channel Content-Type Content-Transfer-Encoding)] BODY.PEEK[TEXT]<0.2097152>)") {
		t.Fatal(f.commands())
	}

	before := len(f.server.lines())
	if err = store.Destroy(ctx, []string{"11.1", "11.3"}); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"UID FETCH 1,3 (UID X-GM-MSGID)", `UID STORE 1,3 +FLAGS.SILENT (\Deleted)`, "UID EXPUNGE 1,3",
		`LIST "" "*"`, `SELECT "[Gmail]/All Mail"`, "UID SEARCH OR X-GM-MSGID 5000 X-GM-MSGID 5002",
		`UID MOVE 100,102 "[Gmail]/Trash"`, `SELECT "[Gmail]/Trash"`, "UID SEARCH OR X-GM-MSGID 5000 X-GM-MSGID 5002",
		`UID STORE 1:2 +FLAGS.SILENT (\Deleted)`, "UID EXPUNGE 1:2", "SELECT Tandem"}
	if got := f.commands()[before:]; !reflect.DeepEqual(got, expected) {
		t.Fatalf("got  %q\nwant %q", got, expected)
	}
	for box, ids := range map[string][]uint64{"Tandem": {5001}, "[Gmail]/All Mail": {5001}, "[Gmail]/Trash": nil} {
		if got := f.gmail.gmailIDs(box); !reflect.DeepEqual(got, ids) {
			t.Errorf("%s: %v", box, got)
		}
	}

	gone, err := store.Changes(ctx, again.Cursor) // no QRESYNC: UID SEARCH ALL against the known set
	if err != nil || !reflect.DeepEqual(gone.Removed, []string{"11.1", "11.3"}) {
		t.Fatal(gone, err)
	}
	left, err := store.Fetch(ctx, []string{"11.1", "11.2", "11.3", "12.2"})
	if err != nil || !reflect.DeepEqual(left, []tandem.Message{{ID: "11.2", Channel: "chan", Body: []byte{4}}}) {
		t.Fatal(left, err)
	}
}

func TestGmailRestartsIdleEveryThreeSeconds(t *testing.T) {
	f := newGmailFixture(t)
	ctx := testContext(t)
	store := f.store(t, "pass word", IdleRestartAutomatic)
	wakes, err := store.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	drain(wakes)
	f.server.push("* 4 EXISTS")
	if !wake(t, wakes, 2*time.Second) {
		t.Fatal("no push")
	}
	time.Sleep(6500 * time.Millisecond)
	idles := count(f.commands(), func(c string) bool { return c == "IDLE" })
	dones := count(f.server.lines(), func(c string) bool { return c == "DONE" })
	if dones != 2 || idles != 3 {
		t.Fatalf("restarted at 3 s and 6 s: %d DONE, %d IDLE", dones, idles)
	}
	store.Close()
	for range wakes {
	}
}

func TestIdleRestartCanBeTurnedOff(t *testing.T) {
	f := newGmailFixture(t)
	store := f.store(t, "pass word", IdleRestartNever)
	if _, err := store.Watch(testContext(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3500 * time.Millisecond)
	if n := count(f.server.lines(), func(c string) bool { return c == "DONE" }); n != 0 {
		t.Fatal(n, "DONE")
	}
	before := len(f.server.lines())
	store.Close()
	if after := len(f.server.lines()); after != before {
		t.Fatal(f.server.lines()[before:])
	}
}

func TestCancellingTheWatchEndsIdle(t *testing.T) {
	f := newGmailFixture(t)
	store := f.store(t, "pass word", IdleRestartNever)
	ctx, cancel := context.WithCancel(testContext(t))
	if _, err := store.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(300 * time.Millisecond)
	cmds := f.commands()
	if len(cmds) < 2 || !reflect.DeepEqual(cmds[len(cmds)-2:], []string{"IDLE", "DONE"}) {
		t.Fatal(cmds)
	}
}

// An app stops watching (engine.Device.Run returns) and starts again: the second Watch
// works, a change after it arrives, and nothing is left running at Close.
func TestWatchStartsAgainAfterItsContextEnds(t *testing.T) {
	for _, separate := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate=%v", separate), func(t *testing.T) {
			f := newGmailFixture(t)
			ctx := testContext(t)
			baseline := runtime.NumGoroutine()
			cfg := f.config("pass word", IdleRestartNever)
			cfg.SeparateWatchConnection = separate
			store, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			first, cancel := context.WithCancel(ctx)
			if _, err := store.Watch(first); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Watch(ctx); err == nil {
				t.Fatal("two watches at once")
			}
			cancel()
			wakes, err := store.Watch(ctx)
			if err != nil {
				t.Fatal("watching again:", err)
			}
			time.Sleep(100 * time.Millisecond)
			drain(wakes)
			f.server.push("* 9 EXISTS")
			if !wake(t, wakes, 2*time.Second) {
				t.Fatal("no push after watching again")
			}
			store.Close()
			for range wakes {
			}
			deadline := time.Now().Add(2 * time.Second)
			for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if n := runtime.NumGoroutine(); n > baseline {
				buf := make([]byte, 1<<16)
				t.Fatalf("%d goroutines, %d before:\n%s", n, baseline, buf[:runtime.Stack(buf, true)])
			}
		})
	}
}

func TestReconnectsAfterTheServerHangsUp(t *testing.T) {
	f := newGmailFixture(t)
	store := f.store(t, "pass word", IdleRestartNever)
	wakes, err := store.Watch(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	drain(wakes)
	f.server.disconnectAll()
	if !wake(t, wakes, 5*time.Second) {
		t.Fatal("no invalidation after reconnecting")
	}
	cmds := f.commands()
	if n := count(cmds, func(c string) bool { return strings.HasPrefix(c, "LOGIN") }); n != 2 || cmds[len(cmds)-1] != "IDLE" {
		t.Fatal(cmds)
	}
	f.server.push("* 9 EXISTS")
	if !wake(t, wakes, 2*time.Second) {
		t.Fatal("no push after reconnecting")
	}
}

// A message appended after the last command of a reconcile and before IDLE starts. Stalwart
// (measured) and this fake report it only on the next command, so IDLE alone would leave it
// unseen until some later command: IDLE starts with a NOOP before it. Without the NOOP the
// session misses it, which shows the test catches the gap.
func TestChangeBetweenCommandAndIdleIsReported(t *testing.T) {
	for _, withNoop := range []bool{true, false} {
		t.Run(fmt.Sprintf("noop=%v", withNoop), func(t *testing.T) {
			f := newGmailFixture(t)
			f.gmail.setCaps("IMAP4rev1 IDLE NAMESPACE UIDPLUS MOVE") // not Gmail: no IDLE restart to hide the gap
			ctx := testContext(t)
			store := f.store(t, "pass word", IdleRestartAutomatic)
			store.link.withoutNoop = !withNoop // before the worker's first operation
			if err := store.EnsureFolder(ctx); err != nil {
				t.Fatal(err)
			}
			session, err := tandem.NewSession(store, "", 8)
			if err != nil {
				t.Fatal(err)
			}
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- session.Run(runCtx) }()
			defer func() { stop(); <-done }()
			select {
			case <-session.Updates(): // the initial batch
			case <-ctx.Done():
				t.Fatal("no initial batch")
			}
			idleSettles(t, f.server)
			// A push wakes the session; its reconcile ends with the body fetch, right after which
			// another client appends. Nothing else wakes the session afterwards.
			raw, _ := BuildMessage("gap", []byte{9, 9}, time.Now(), "")
			f.gmail.setAppendAfterNextFetch(raw)
			f.server.push("* 0 EXISTS")
			var added []tandem.Message
			limit := time.After(3 * time.Second)
			for len(added) == 0 {
				select {
				case b := <-session.Updates():
					added = append(added, b.Added...)
				case <-limit:
					if withNoop {
						t.Fatal("the change before IDLE was not reported")
					}
					return
				}
			}
			if !withNoop {
				t.Fatal("seen without the NOOP; the test no longer shows the gap")
			}
			if !reflect.DeepEqual(added, []tandem.Message{{ID: "11.1", Channel: "gap", Body: []byte{9, 9}}}) {
				t.Fatal(added)
			}
		})
	}
}

// idleSettles waits until the client has idled, with nothing else sent, for 200 ms.
func idleSettles(t *testing.T, server *fakeServer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		before := server.lines()
		time.Sleep(200 * time.Millisecond)
		if len(before) > 0 && strings.HasSuffix(before[len(before)-1], " IDLE") && reflect.DeepEqual(server.lines(), before) {
			return
		}
	}
	t.Fatal("IDLE did not settle")
}

// A server that accepts the connection and then never answers: connecting gives up at the
// deadline as a lost connection, and watching retries with backoff until it answers again.
func TestASilentServerTimesOutAndWatchingRetries(t *testing.T) {
	f := newGmailFixture(t)
	cfg := f.config("pass word", IdleRestartNever)
	cfg.ConnectTimeout = 300 * time.Millisecond
	store, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f.server.silent.Store(true)
	short, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lost *ConnectionError
	if err := store.EnsureFolder(short); !errors.As(err, &lost) {
		t.Fatal("not a lost connection:", err)
	}
	f.server.silent.Store(false)
	ctx := testContext(t)
	wakes, err := store.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	drain(wakes)
	f.server.silent.Store(true)
	f.server.disconnectAll()
	time.Sleep(time.Second) // a few connects time out
	f.server.silent.Store(false)
	if !wake(t, wakes, 5*time.Second) {
		t.Fatal("no invalidation once the server answers again")
	}
	if cmds := f.commands(); cmds[len(cmds)-1] != "IDLE" {
		t.Fatal(cmds)
	}
}

func TestReadsRetryOnceAfterTheServerHangsUp(t *testing.T) {
	f := newGmailFixture(t)
	ctx := testContext(t)
	store := f.store(t, "pass word", IdleRestartAutomatic)
	first, err := store.Changes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	f.server.disconnectAll()
	second, err := store.Changes(ctx, first.Cursor)
	if err != nil || second.Cursor != first.Cursor {
		t.Fatal(second, err)
	}
	if n := count(f.commands(), func(c string) bool { return strings.HasPrefix(c, "LOGIN") }); n != 2 {
		t.Fatal(n)
	}
}

func TestRefusedCredentialsEndWatchingAndFailCommands(t *testing.T) {
	f := newGmailFixture(t)
	ctx := testContext(t)
	store := f.store(t, "wrong", IdleRestartAutomatic)
	var auth *AuthError
	if _, err := store.Changes(ctx, ""); !errors.As(err, &auth) || !strings.Contains(auth.Text, "Invalid credentials") {
		t.Fatal(err)
	}
	if _, err := store.Watch(ctx); !errors.As(err, &auth) {
		t.Fatal(err)
	}
}

func TestServerWithoutUIDPLUSIsRefused(t *testing.T) {
	f := newGmailFixture(t)
	f.gmail.setCaps("IMAP4rev1 IDLE")
	store := f.store(t, "pass word", IdleRestartAutomatic)
	var unsupported *UnsupportedError
	if err := store.EnsureFolder(testContext(t)); !errors.As(err, &unsupported) {
		t.Fatal(err)
	}
}

func TestTraceLeavesOutCredentials(t *testing.T) {
	f := newGmailFixture(t)
	var mu sync.Mutex
	var lines []string
	cfg := f.config("pass word", IdleRestartAutomatic)
	cfg.Trace = func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() }
	store, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureFolder(testContext(t)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	mu.Lock()
	defer mu.Unlock()
	redacted := false
	for _, l := range lines {
		if strings.HasPrefix(l, "C: T2 LOGIN") && strings.HasSuffix(l, "<redacted>") {
			redacted = true
		}
		if strings.HasPrefix(l, "C:") && (strings.Contains(l, "pass word") || strings.Contains(l, "alice")) {
			t.Error("credentials in the trace:", l)
		}
	}
	if !redacted {
		t.Error(lines)
	}
}

func TestCloseLeavesNoGoroutines(t *testing.T) {
	f := newGmailFixture(t)
	ctx := testContext(t)
	baseline := runtime.NumGoroutine()
	cfg := f.config("pass word", IdleRestartAutomatic)
	cfg.SeparateWatchConnection = true
	store, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wakes, err := store.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, "c", []byte{1}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	for range wakes {
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		buf := make([]byte, 1<<16)
		t.Fatalf("%d goroutines, %d before:\n%s", n, baseline, buf[:runtime.Stack(buf, true)])
	}
}
