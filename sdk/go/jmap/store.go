// Package jmap implements Tandem's PushStore over HTTPS JMAP and persistent SSE.
// IMAP remains the native default; selecting JMAP is explicit.
package jmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/imap"
)

const core = "urn:ietf:params:jmap:core"
const mail = "urn:ietf:params:jmap:mail"
const FastmailSession = "https://api.fastmail.com/jmap/session"

type Config struct {
	SessionURL, Authorization, AccountID, FolderName                           string
	TrustedOrigins                                                             []string
	Fastmail                                                                   bool
	PageSize, MaxResponseBytes, MaxKnownMessages, MaxEventBytes, MaxBatchBytes int
	CommandTimeout, RetryMin, RetryMax                                         time.Duration
	// HTTPClient is optional, chiefly for controlled fixture TLS. Redirects are
	// always refused; the supplied client's configuration is copied, not changed.
	HTTPClient *http.Client
}

func FastmailConfig(token string) Config {
	return Config{SessionURL: FastmailSession, Authorization: "Bearer " + token, Fastmail: true}
}

type Error struct{ Type string }

func (e *Error) Error() string { return "jmap: " + e.Type }

var ErrSnapshotChanged = errors.New("jmap: query changed during full scan; resume after push")

type Store struct {
	cfg                                  Config
	client                               *http.Client
	origins                              map[string]bool
	api, upload, events, account, folder string
	deleteSize                           int
	ctx                                  context.Context
	cancel                               context.CancelFunc
	mu                                   sync.Mutex
	watching                             bool
	wg                                   sync.WaitGroup
}

var _ tandem.PushStore = (*Store)(nil)

func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.PageSize == 0 {
		cfg.PageSize = 128
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 16 << 20
	}
	if cfg.MaxKnownMessages == 0 {
		cfg.MaxKnownMessages = 1_000_000
	}
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = 128 << 10
	}
	if cfg.MaxBatchBytes == 0 {
		cfg.MaxBatchBytes = 64 << 20
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	if cfg.RetryMin == 0 {
		cfg.RetryMin = time.Second
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 30 * time.Second
	}
	if cfg.FolderName == "" {
		cfg.FolderName = "Tandem"
	}
	if cfg.PageSize < 1 || cfg.MaxResponseBytes < 1 || cfg.MaxKnownMessages < 1 || cfg.MaxEventBytes < 1 || cfg.MaxBatchBytes < 1 || cfg.CommandTimeout <= 0 || cfg.RetryMin <= 0 || cfg.RetryMax < cfg.RetryMin || strings.ContainsAny(cfg.Authorization, "\r\n") || !(strings.HasPrefix(cfg.Authorization, "Bearer ") || strings.HasPrefix(cfg.Authorization, "Basic ")) || strings.ContainsAny(cfg.FolderName, "\r\n\x00") {
		return nil, errors.New("jmap: invalid configuration")
	}
	if cfg.Fastmail && (cfg.SessionURL != FastmailSession || !strings.HasPrefix(cfg.Authorization, "Bearer ") || !ValidateToken(strings.TrimPrefix(cfg.Authorization, "Bearer "))) {
		return nil, errors.New("jmap: invalid Fastmail configuration")
	}
	root, cancel := context.WithCancel(context.Background())
	s := &Store{cfg: cfg, origins: map[string]bool{}, ctx: root, cancel: cancel}
	for _, raw := range append([]string{cfg.SessionURL}, cfg.TrustedOrigins...) {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, errors.New("jmap: invalid trusted origin")
		}
		s.origins[origin(u)] = true
	}
	if cfg.HTTPClient != nil {
		copy := *cfg.HTTPClient
		copy.Jar = nil
		s.client = &copy
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = cfg.CommandTimeout
		s.client = &http.Client{Transport: transport}
	}
	s.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && len(via) < 6 && via[0].URL.String() == cfg.SessionURL && req.Method == "GET" {
			if e := s.trusted(req.URL.String()); e != nil {
				return e
			}
			req.Header.Set("Authorization", cfg.Authorization)
			return nil
		}
		return http.ErrUseLastResponse
	}
	if e := s.connect(ctx); e != nil {
		s.Close()
		return nil, e
	}
	return s, nil
}
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return "https://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
func (s *Store) trusted(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return errors.New("jmap: invalid URL")
	}
	fast := s.cfg.Fastmail && (u.Port() == "" || u.Port() == "443") && (u.Hostname() == "api.fastmail.com" || strings.HasSuffix(u.Hostname(), ".api.fastmail.com"))
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || !(s.origins[origin(u)] || fast) {
		return errors.New("jmap: untrusted URL")
	}
	return nil
}

type responseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	stop   func() bool
}

func (b *responseBody) Close() error { e := b.ReadCloser.Close(); b.cancel(); b.stop(); return e }
func (s *Store) request(ctx context.Context, raw, method, typ string, data []byte, stream bool) (*http.Response, error) {
	if e := s.trusted(raw); e != nil {
		return nil, e
	}
	var cancel context.CancelFunc
	if stream {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithTimeout(ctx, s.cfg.CommandTimeout)
	}
	stop := context.AfterFunc(s.ctx, cancel)
	req, e := http.NewRequestWithContext(ctx, method, raw, bytes.NewReader(data))
	if e != nil {
		cancel()
		stop()
		return nil, e
	}
	req.Header.Set("Authorization", s.cfg.Authorization)
	if typ != "" {
		req.Header.Set("Content-Type", typ)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	r, e := s.client.Do(req)
	if e != nil {
		cancel()
		stop()
		return nil, e
	}
	r.Body = &responseBody{r.Body, cancel, stop}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		r.Body.Close()
		return nil, &Error{fmt.Sprintf("HTTP %d", r.StatusCode)}
	}
	return r, nil
}
func (s *Store) json(ctx context.Context, raw, method, typ string, data []byte, out any) error {
	r, e := s.request(ctx, raw, method, typ, data, false)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	body, e := io.ReadAll(io.LimitReader(r.Body, int64(s.cfg.MaxResponseBytes)+1))
	if e != nil {
		return e
	}
	if len(body) > s.cfg.MaxResponseBytes {
		return errors.New("jmap: response limit exceeded")
	}
	if _, e = tandem.ParseJSON(body); e != nil {
		return e
	}
	return json.Unmarshal(body, out)
}
func (s *Store) call(ctx context.Context, method string, args map[string]any, out any) error {
	args["accountId"] = s.account
	body, e := json.Marshal(map[string]any{"using": []string{core, mail}, "methodCalls": [][]any{{method, args, "0"}}})
	if e != nil {
		return e
	}
	var response struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if e = s.json(ctx, s.api, "POST", "application/json", body, &response); e != nil {
		return e
	}
	if len(response.MethodResponses) != 1 || len(response.MethodResponses[0]) != 3 {
		return errors.New("jmap: invalid response")
	}
	r := response.MethodResponses[0]
	var name, id string
	if json.Unmarshal(r[0], &name) != nil || json.Unmarshal(r[2], &id) != nil || id != "0" {
		return errors.New("jmap: response mismatch")
	}
	var result map[string]json.RawMessage
	if e = json.Unmarshal(r[1], &result); e != nil {
		return e
	}
	if name == "error" {
		var typ string
		json.Unmarshal(result["type"], &typ)
		return &Error{typ}
	}
	if name != method {
		return errors.New("jmap: response method mismatch")
	}
	for _, key := range []string{"notCreated", "notUpdated", "notDestroyed"} {
		var failures map[string]json.RawMessage
		if value, ok := result[key]; ok {
			if e = json.Unmarshal(value, &failures); e != nil {
				return e
			}
			if len(failures) > 0 {
				return &Error{method + " refused"}
			}
		}
	}
	return json.Unmarshal(r[1], out)
}
func (s *Store) connect(ctx context.Context) error {
	var session struct {
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		PrimaryAccounts map[string]string          `json:"primaryAccounts"`
		Accounts        map[string]struct {
			AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
		} `json:"accounts"`
		APIURL         string `json:"apiUrl"`
		UploadURL      string `json:"uploadUrl"`
		EventSourceURL string `json:"eventSourceUrl"`
	}
	if e := s.json(ctx, s.cfg.SessionURL, "GET", "", nil, &session); e != nil {
		return e
	}
	if _, ok := session.Capabilities[mail]; !ok {
		return &Error{"mail capability missing"}
	}
	s.account = s.cfg.AccountID
	if s.account == "" {
		s.account = session.PrimaryAccounts[mail]
	}
	if _, ok := session.Accounts[s.account].AccountCapabilities[mail]; !ok {
		return &Error{"mail account missing"}
	}
	s.api, s.upload, s.events = session.APIURL, session.UploadURL, session.EventSourceURL
	if e := s.trusted(s.api); e != nil {
		return e
	}
	if s.upload == "" || s.events == "" {
		return errors.New("jmap: missing endpoint")
	}
	var capability struct {
		MaxObjectsInGet int `json:"maxObjectsInGet"`
		MaxObjectsInSet int `json:"maxObjectsInSet"`
	}
	if json.Unmarshal(session.Capabilities[core], &capability) == nil && capability.MaxObjectsInGet > 0 {
		s.cfg.PageSize = min(s.cfg.PageSize, capability.MaxObjectsInGet)
	}
	s.deleteSize = s.cfg.PageSize
	if capability.MaxObjectsInSet > 0 {
		s.deleteSize = min(s.deleteSize, capability.MaxObjectsInSet)
	}
	var query struct {
		IDs []string `json:"ids"`
	}
	if e := s.call(ctx, "Mailbox/query", map[string]any{"filter": map[string]any{"name": s.cfg.FolderName}}, &query); e != nil {
		return e
	}
	if len(query.IDs) > 0 {
		var boxes struct {
			List []struct {
				ID, Name string
				ParentID *string `json:"parentId"`
			} `json:"list"`
		}
		if e := s.call(ctx, "Mailbox/get", map[string]any{"ids": query.IDs}, &boxes); e != nil {
			return e
		}
		for _, box := range boxes.List {
			if box.Name == s.cfg.FolderName && box.ParentID == nil {
				if s.folder != "" {
					return errors.New("jmap: ambiguous root mailbox")
				}
				s.folder = box.ID
			}
		}
	}
	if s.folder == "" {
		var created struct {
			Created map[string]struct{ ID string } `json:"created"`
		}
		if e := s.call(ctx, "Mailbox/set", map[string]any{"create": map[string]any{"tandem": map[string]any{"name": s.cfg.FolderName, "parentId": nil}}}, &created); e != nil {
			return e
		}
		s.folder = created.Created["tandem"].ID
	}
	if s.folder == "" {
		return errors.New("jmap: no mailbox ID")
	}
	return nil
}
func (s *Store) subscribe(ctx context.Context) (*http.Response, error) {
	raw := strings.NewReplacer("{types}", "Email", "{closeafter}", "no", "{ping}", "60").Replace(s.events)
	if strings.ContainsAny(raw, "{}") {
		return nil, errors.New("jmap: unsupported event URL template")
	}
	r, e := s.request(ctx, raw, "GET", "", nil, true)
	if e != nil {
		return nil, e
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "text/event-stream") {
		r.Body.Close()
		return nil, &Error{"not an event stream"}
	}
	return r, nil
}
func (s *Store) Watch(ctx context.Context) (<-chan tandem.Wake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watching {
		return nil, errors.New("jmap: watch already started")
	}
	s.watching = true
	r, e := s.subscribe(ctx)
	if e != nil {
		return nil, e
	}
	wakes := make(chan tandem.Wake, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(wakes)
		delay := s.cfg.RetryMin
		wake := func() {
			select {
			case wakes <- tandem.Wake{}:
			default:
			}
		}
		terminal := func(e error) {
			select {
			case <-wakes:
			default:
			}
			select {
			case wakes <- tandem.Wake{Err: e}:
			case <-ctx.Done():
			case <-s.ctx.Done():
			}
		}
		for {
			e := s.readEvents(r, wake)
			r.Body.Close()
			if ctx.Err() != nil || s.ctx.Err() != nil {
				return
			}
			var protocol *Error
			if errors.As(e, &protocol) {
				terminal(e)
				return
			}
			for {
				timer := time.NewTimer(delay) // allowed-timer: failed-connection backoff only
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-s.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				r, e = s.subscribe(ctx)
				if e == nil {
					delay = s.cfg.RetryMin
					wake()
					break
				}
				if errors.As(e, &protocol) {
					terminal(e)
					return
				}
				delay = min(delay*2, s.cfg.RetryMax)
			}
		}
	}()
	return wakes, nil
}
func (s *Store) readEvents(r *http.Response, wake func()) error {
	scanner := bufio.NewScanner(r.Body)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		for i, b := range data {
			if b == '\n' {
				return i + 1, data[:i], nil
			}
			if b == '\r' {
				if i+1 == len(data) && !atEOF {
					return 0, nil, nil
				}
				n := i + 1
				if n < len(data) && data[n] == '\n' {
					n++
				}
				return n, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	scanner.Buffer(make([]byte, min(4096, s.cfg.MaxEventBytes)), s.cfg.MaxEventBytes)
	event := ""
	var data []string
	size := 0
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			first = false
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if !utf8.ValidString(line) {
			return &Error{"invalid push UTF-8"}
		}
		size += len(line) + 1
		if size > s.cfg.MaxEventBytes {
			return &Error{"event limit exceeded"}
		}
		if line == "" {
			if event == "state" && len(data) > 0 {
				var payload struct {
					Changed map[string]map[string]json.RawMessage `json:"changed"`
				}
				raw := []byte(strings.Join(data, "\n"))
				if _, e := tandem.ParseJSON(raw); e != nil {
					return &Error{"invalid push JSON"}
				}
				if e := json.Unmarshal(raw, &payload); e != nil {
					return &Error{"invalid push payload"}
				}
				if _, ok := payload.Changed[s.account]["Email"]; ok {
					wake()
				}
			}
			event = ""
			data = nil
			size = 0
		} else if strings.HasPrefix(line, "event:") {
			event = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if e := scanner.Err(); e != nil {
		if errors.Is(e, bufio.ErrTooLong) {
			return &Error{"push line limit exceeded"}
		}
		return e
	}
	return io.EOF
}

type emailObject struct {
	ID         string          `json:"id"`
	MailboxIDs map[string]bool `json:"mailboxIds"`
	Versions   []string        `json:"header:Tandem-Version:asText:all"`
	Channels   []string        `json:"header:Tandem-Channel:asText:all"`
	BodyValues map[string]struct {
		Value             string `json:"value"`
		IsTruncated       bool   `json:"isTruncated"`
		IsEncodingProblem bool   `json:"isEncodingProblem"`
	} `json:"bodyValues"`
}

func (s *Store) objects(ctx context.Context, ids []string, bodies bool) ([]emailObject, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	properties := []string{"id", "mailboxIds"}
	if bodies {
		properties = append(properties, "bodyValues", "textBody", "header:Tandem-Channel:asText:all", "header:Tandem-Version:asText:all")
	}
	var result struct {
		List     []emailObject `json:"list"`
		NotFound []string      `json:"notFound"`
	}
	e := s.call(ctx, "Email/get", map[string]any{"ids": ids, "properties": properties, "fetchTextBodyValues": bodies, "maxBodyValueBytes": s.cfg.MaxResponseBytes}, &result)
	return result.List, result.NotFound, e
}
func validEnvelope(b []byte) bool {
	return len(b) >= 54 && bytes.Equal(b[:5], []byte{'T', 'N', 'D', 'M', 2}) && (b[5] == 1 || b[5] == 3 || b[5] == 2 && len(b) >= 86)
}
func (s *Store) message(row emailObject) (*tandem.Message, error) {
	if !row.MailboxIDs[s.folder] || len(row.Versions) != 1 || strings.TrimSpace(row.Versions[0]) != "2" || len(row.Channels) != 1 {
		return nil, nil
	}
	tag := strings.TrimSpace(row.Channels[0])
	if b, e := tandem.ParseB32(tag); e != nil || len(b) != 20 {
		return nil, nil
	}
	for _, v := range row.BodyValues {
		if v.IsTruncated {
			return nil, errors.New("jmap: body truncated; cursor unchanged")
		}
	}
	if len(row.BodyValues) != 1 {
		return nil, nil
	}
	for _, v := range row.BodyValues {
		if v.IsEncodingProblem {
			return nil, nil
		}
		compact := strings.Map(func(r rune) rune {
			if strings.ContainsRune("\t\n\v\f\r ", r) {
				return -1
			}
			return r
		}, v.Value)
		body, e := base64.StdEncoding.Strict().DecodeString(compact)
		if e != nil || base64.StdEncoding.EncodeToString(body) != compact || !validEnvelope(body) {
			return nil, nil
		}
		return &tandem.Message{ID: row.ID, Channel: tag, Body: body}, nil
	}
	return nil, nil
}

type cursor struct {
	V       int      `json:"v"`
	Account string   `json:"account"`
	Folder  string   `json:"folder"`
	State   string   `json:"state"`
	Known   []string `json:"known"`
}

func (s *Store) Changes(ctx context.Context, encoded string) (tandem.ChangeBatch, error) {
	var result tandem.ChangeBatch
	current := cursor{V: 1, Account: s.account, Folder: s.folder}
	known := map[string]bool{}
	added := map[string]tandem.Message{}
	removed := map[string]bool{}
	totalBytes := 0
	collect := func(row emailObject) error {
		if !row.MailboxIDs[s.folder] {
			delete(known, row.ID)
			delete(added, row.ID)
			removed[row.ID] = true
			return nil
		}
		known[row.ID] = true
		m, e := s.message(row)
		if e != nil {
			return e
		}
		if m != nil {
			if old, ok := added[m.ID]; ok {
				totalBytes -= len(old.Body)
			}
			totalBytes += len(m.Body)
			if totalBytes > s.cfg.MaxBatchBytes {
				return errors.New("jmap: batch byte limit exceeded")
			}
			added[m.ID] = *m
		}
		return nil
	}
	full := encoded == ""
	if !full {
		if _, e := tandem.ParseJSON([]byte(encoded)); e != nil {
			return result, e
		}
		if e := json.Unmarshal([]byte(encoded), &current); e != nil {
			return result, e
		}
		if current.V != 1 || current.Account != s.account || current.Folder != s.folder || current.State == "" || current.Known == nil || len(current.Known) > s.cfg.MaxKnownMessages {
			return result, errors.New("jmap: cursor belongs to another store or exceeds limits")
		}
		for _, id := range current.Known {
			if id == "" || known[id] {
				return result, errors.New("jmap: invalid cursor IDs")
			}
			known[id] = true
		}
	}
	fullScan := func() error {
		var base struct {
			State string `json:"state"`
		}
		if e := s.call(ctx, "Email/get", map[string]any{"ids": []string{}, "properties": []string{"id"}}, &base); e != nil {
			return e
		}
		if base.State == "" {
			return errors.New("jmap: missing state")
		}
		current.State = base.State
		var ids []string
		seen := map[string]bool{}
		queryState := ""
		for {
			var query struct {
				IDs        []string `json:"ids"`
				QueryState string   `json:"queryState"`
				Total      *int     `json:"total"`
			}
			if e := s.call(ctx, "Email/query", map[string]any{"filter": map[string]any{"inMailbox": s.folder}, "sort": []any{map[string]any{"property": "receivedAt", "isAscending": true}}, "position": len(ids), "limit": s.cfg.PageSize, "calculateTotal": true}, &query); e != nil {
				return e
			}
			if query.QueryState == "" || queryState != "" && query.QueryState != queryState {
				return ErrSnapshotChanged
			}
			queryState = query.QueryState
			for _, id := range query.IDs {
				if id == "" || seen[id] {
					return errors.New("jmap: repeated query ID")
				}
				seen[id] = true
				ids = append(ids, id)
			}
			if len(ids) > s.cfg.MaxKnownMessages {
				return errors.New("jmap: message limit exceeded")
			}
			if query.Total == nil || *query.Total < len(ids) {
				return errors.New("jmap: invalid query total")
			}
			if len(ids) == *query.Total {
				break
			}
			if len(query.IDs) == 0 {
				return errors.New("jmap: query made no progress")
			}
		}
		for id := range known {
			if !seen[id] {
				removed[id] = true
			}
		}
		known = seen
		added = map[string]tandem.Message{}
		totalBytes = 0
		for at := 0; at < len(ids); at += s.cfg.PageSize {
			rows, missing, e := s.objects(ctx, ids[at:min(at+s.cfg.PageSize, len(ids))], true)
			if e != nil {
				return e
			}
			for _, id := range missing {
				delete(known, id)
				removed[id] = true
			}
			for _, row := range rows {
				if e := collect(row); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if full {
		if e := fullScan(); e != nil {
			return result, e
		}
	}
	reset := false
	for {
		var changes struct {
			Created, Updated, Destroyed []string
			NewState                    string `json:"newState"`
			HasMoreChanges              bool   `json:"hasMoreChanges"`
		}
		e := s.call(ctx, "Email/changes", map[string]any{"sinceState": current.State, "maxChanges": s.cfg.PageSize}, &changes)
		var protocol *Error
		if errors.As(e, &protocol) && protocol.Type == "cannotCalculateChanges" && !reset {
			reset = true
			if e = fullScan(); e != nil {
				return result, e
			}
			continue
		}
		if e != nil {
			return result, e
		}
		if changes.NewState == "" || changes.HasMoreChanges && changes.NewState == current.State {
			return result, errors.New("jmap: changes made no progress")
		}
		ids := append(changes.Created, changes.Updated...)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		for _, id := range changes.Destroyed {
			if known[id] {
				removed[id] = true
			}
			delete(known, id)
			delete(added, id)
		}
		for at := 0; at < len(ids); at += s.cfg.PageSize {
			rows, missing, e := s.objects(ctx, ids[at:min(at+s.cfg.PageSize, len(ids))], false)
			if e != nil {
				return result, e
			}
			var bodies []string
			for _, id := range missing {
				delete(known, id)
				delete(added, id)
				removed[id] = true
			}
			for _, row := range rows {
				if slices.Contains(changes.Destroyed, row.ID) {
					continue
				}
				if row.MailboxIDs[s.folder] {
					known[row.ID] = true
					bodies = append(bodies, row.ID)
				} else if known[row.ID] {
					delete(known, row.ID)
					delete(added, row.ID)
					removed[row.ID] = true
				}
			}
			fetched, missing, e := s.objects(ctx, bodies, true)
			if e != nil {
				return result, e
			}
			for _, id := range missing {
				delete(known, id)
				delete(added, id)
				removed[id] = true
			}
			for _, row := range fetched {
				if e := collect(row); e != nil {
					return result, e
				}
			}
		}
		if len(known) > s.cfg.MaxKnownMessages {
			return result, errors.New("jmap: message limit exceeded")
		}
		current.State = changes.NewState
		if !changes.HasMoreChanges {
			break
		}
	}
	current.Known = []string{}
	for id := range known {
		current.Known = append(current.Known, id)
	}
	slices.Sort(current.Known)
	for id := range removed {
		if !known[id] {
			result.Removed = append(result.Removed, id)
		}
	}
	slices.Sort(result.Removed)
	for _, row := range added {
		result.Added = append(result.Added, row)
	}
	slices.SortFunc(result.Added, func(a, b tandem.Message) int { return strings.Compare(a.ID, b.ID) })
	b, e := json.Marshal(current)
	result.Cursor = string(b)
	return result, e
}
func (s *Store) Append(ctx context.Context, channel string, body []byte) (string, error) {
	if b, e := tandem.ParseB32(channel); e != nil || len(b) != 20 || !validEnvelope(body) {
		return "", errors.New("jmap: invalid envelope or channel")
	}
	wire, e := imap.BuildMessage(channel, body, time.Now(), "")
	if e != nil {
		return "", e
	}
	return s.AppendPrepared(ctx, wire)
}

// AppendPrepared retries a caller-journalled MIME message byte for byte.
func (s *Store) AppendPrepared(ctx context.Context, wire []byte) (string, error) {
	if len(wire) > s.cfg.MaxResponseBytes {
		return "", errors.New("jmap: message limit exceeded")
	}
	var upload struct {
		BlobID string `json:"blobId"`
	}
	raw := strings.ReplaceAll(s.upload, "{accountId}", url.PathEscape(s.account))
	if strings.ContainsAny(raw, "{}") {
		return "", errors.New("jmap: unsupported upload template")
	}
	if e := s.json(ctx, raw, "POST", "message/rfc822", wire, &upload); e != nil {
		return "", e
	}
	if upload.BlobID == "" {
		return "", errors.New("jmap: missing blob ID")
	}
	var imported struct {
		Created map[string]struct{ ID string } `json:"created"`
	}
	e := s.call(ctx, "Email/import", map[string]any{"emails": map[string]any{"tandem": map[string]any{"blobId": upload.BlobID, "mailboxIds": map[string]bool{s.folder: true}, "keywords": map[string]bool{"$seen": true}}}}, &imported)
	if e != nil {
		return "", e
	}
	id := imported.Created["tandem"].ID
	if id == "" {
		return "", errors.New("jmap: missing imported ID")
	}
	return id, nil
}
func (s *Store) Fetch(ctx context.Context, ids []string) ([]tandem.Message, error) {
	var out []tandem.Message
	for at := 0; at < len(ids); at += s.cfg.PageSize {
		rows, _, e := s.objects(ctx, ids[at:min(at+s.cfg.PageSize, len(ids))], true)
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			m, e := s.message(row)
			if e != nil {
				return nil, e
			}
			if m != nil {
				out = append(out, *m)
			}
		}
	}
	return out, nil
}
func (s *Store) Destroy(ctx context.Context, ids []string) error {
	for at := 0; at < len(ids); at += s.deleteSize {
		rows, _, e := s.objects(ctx, ids[at:min(at+s.deleteSize, len(ids))], false)
		if e != nil {
			return e
		}
		var safe []string
		for _, row := range rows {
			if !row.MailboxIDs[s.folder] {
				return errors.New("jmap: refusing destruction outside replica mailbox")
			}
			safe = append(safe, row.ID)
		}
		if len(safe) > 0 {
			var result map[string]json.RawMessage
			if e := s.call(ctx, "Email/set", map[string]any{"destroy": safe}, &result); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Store) Close() error {
	s.cancel()
	s.wg.Wait()
	if transport, ok := s.client.Transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

// Keep token input validation available without a network call.
func ValidateToken(token string) bool {
	return token != "" && !strings.ContainsFunc(token, unicode.IsSpace)
}
