package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The JMAP binding (§10.2). JMAP responses are the server's JSON, not
// protocol JSON, so encoding/json reads them.

const (
	capCore = "urn:ietf:params:jmap:core"
	capMail = "urn:ietf:params:jmap:mail"
)

type JMAP struct {
	d       Descriptor
	http    *http.Client
	origin  *url.URL
	account string
	apiURL  string
	dlURL   string
	esURL   string
	maxSet  int

	mu      sync.Mutex
	mailbox string
	state   string            // Email state the next Changes starts from
	known   map[string]string // id → channel tag, messages in Tandem
}

func OpenJMAP(ctx context.Context, d Descriptor) (*JMAP, error) {
	origin, err := url.Parse(d.Session)
	if err != nil {
		return nil, err
	}
	j := &JMAP{d: d, http: &http.Client{}, origin: origin, known: map[string]string{}}
	var s struct {
		APIURL          string                     `json:"apiUrl"`
		DownloadURL     string                     `json:"downloadUrl"`
		EventSourceURL  string                     `json:"eventSourceUrl"`
		PrimaryAccounts map[string]string          `json:"primaryAccounts"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
	}
	resp, err := j.do(ctx, "GET", d.Session, nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(resp, &s); err != nil {
		return nil, fmt.Errorf("jmap: session: %w", err)
	}
	var core struct {
		MaxObjectsInSet int `json:"maxObjectsInSet"`
	}
	json.Unmarshal(s.Capabilities[capCore], &core)
	j.account, j.apiURL, j.dlURL, j.esURL, j.maxSet = s.PrimaryAccounts[capMail], s.APIURL, s.DownloadURL, s.EventSourceURL, max(core.MaxObjectsInSet, 1)
	if j.account == "" {
		return nil, errors.New("jmap: no mail account")
	}
	return j, nil
}

// rebase keeps the session URL's origin: a server may advertise URLs with
// its own host name, and through a proxy the client must stay on the proxy.
func (j *JMAP) rebase(raw string) string {
	u, err := j.origin.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host = j.origin.Scheme, j.origin.Host
	return u.String()
}

func (j *JMAP) auth(r *http.Request) {
	if j.d.Token != "" {
		r.Header.Set("Authorization", "Bearer "+j.d.Token)
	} else {
		r.SetBasicAuth(j.d.User, j.d.Password)
	}
}

func (j *JMAP) do(ctx context.Context, method, u string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, j.rebase(u), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	j.auth(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := j.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrAuth
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jmap: HTTP %d: %.200s", resp.StatusCode, data)
	}
	return data, err
}

// MethodError is a JMAP method-level error.
type MethodError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (e *MethodError) Error() string { return "jmap: " + e.Type + ": " + e.Description }

type call struct {
	name string
	args any
}

// calls sends method calls in one request and returns each response's
// arguments, or the first method error.
func (j *JMAP) calls(ctx context.Context, cs ...call) ([]json.RawMessage, error) {
	mc := make([][]any, len(cs))
	for i, c := range cs {
		mc[i] = []any{c.name, c.args, fmt.Sprint(i)}
	}
	body, err := json.Marshal(map[string]any{"using": []string{capCore, capMail}, "methodCalls": mc})
	if err != nil {
		return nil, err
	}
	data, err := j.do(ctx, "POST", j.apiURL, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		MethodResponses [][3]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(cs))
	for _, r := range resp.MethodResponses {
		var name string
		json.Unmarshal(r[0], &name)
		if name == "error" {
			var e MethodError
			json.Unmarshal(r[1], &e)
			return nil, &e
		}
		out = append(out, r[1])
	}
	if len(out) != len(cs) {
		return nil, fmt.Errorf("jmap: %d responses to %d calls", len(out), len(cs))
	}
	return out, nil
}

func (j *JMAP) EnsureFolder(ctx context.Context) error {
	find := func() (string, error) {
		r, err := j.calls(ctx,
			call{"Mailbox/query", map[string]any{"accountId": j.account, "filter": map[string]any{"name": Folder}}},
			call{"Mailbox/get", map[string]any{"accountId": j.account, "#ids": map[string]any{
				"resultOf": "0", "name": "Mailbox/query", "path": "/ids"}, "properties": []string{"name", "parentId", "role"}}},
			call{"Mailbox/get", map[string]any{"accountId": j.account, "ids": nil, "properties": []string{"role"}}})
		if err != nil {
			return "", err
		}
		type mb struct {
			ID, Name string
			ParentID *string
			Role     *string
		}
		var got, all struct{ List []mb }
		json.Unmarshal(r[1], &got)
		json.Unmarshal(r[2], &all)
		inbox := ""
		for _, m := range all.List {
			if m.Role != nil && *m.Role == "inbox" {
				inbox = m.ID
			}
		}
		// The filter matches substrings (RFC 8621 §2.3): take the one named
		// exactly Tandem at top level, or failing that directly under the inbox.
		under := ""
		for _, m := range got.List {
			if m.Name != Folder {
				continue
			}
			if m.ParentID == nil {
				return m.ID, nil
			}
			if *m.ParentID == inbox {
				under = m.ID
			}
		}
		return under, nil
	}
	id, err := find()
	if err != nil {
		return err
	}
	if id == "" {
		_, err = j.calls(ctx, call{"Mailbox/set", map[string]any{"accountId": j.account,
			"create": map[string]any{"t": map[string]any{"name": Folder, "parentId": nil}}}})
		if err != nil {
			return err
		}
		// Whether we created it or lost a race to another device, it exists now.
		if id, err = find(); err != nil || id == "" {
			return fmt.Errorf("jmap: can't create the %s mailbox: %v", Folder, err)
		}
	}
	j.mu.Lock()
	j.mailbox = id
	j.mu.Unlock()
	return nil
}

func (j *JMAP) emailCreate(tag string, env []byte) map[string]any {
	return map[string]any{
		"mailboxIds":                   map[string]bool{j.mailbox: true},
		"keywords":                     map[string]bool{"$seen": true},
		"from":                         []map[string]string{{"name": "Tandem", "email": "tandem@invalid"}},
		"subject":                      "tandem",
		"header:Tandem-Version:asText": "1",
		"header:Tandem-Channel:asText": tag,
		"bodyStructure":                map[string]string{"type": "text/plain", "partId": "b"},
		"bodyValues":                   map[string]any{"b": map[string]string{"value": strings.ReplaceAll(tandem.WrappedBase64(env), "\r\n", "\n")}},
	}
}

func (j *JMAP) Append(ctx context.Context, tag string, env []byte) (string, error) {
	ids, err := j.AppendBatch(ctx, []Ref{{Tag: tag}}, [][]byte{env})
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// AppendBatch appends several envelopes, up to maxObjectsInSet per request.
func (j *JMAP) AppendBatch(ctx context.Context, tags []Ref, envs [][]byte) ([]string, error) {
	var ids []string
	for start := 0; start < len(envs); start += j.maxSet {
		create := map[string]any{}
		end := min(start+j.maxSet, len(envs))
		for i := start; i < end; i++ {
			create[fmt.Sprint(i)] = j.emailCreate(tags[i].Tag, envs[i])
		}
		r, err := j.calls(ctx, call{"Email/set", map[string]any{"accountId": j.account, "create": create}})
		if err != nil {
			return ids, err
		}
		var res struct {
			Created    map[string]struct{ ID string }
			NotCreated map[string]MethodError
		}
		json.Unmarshal(r[0], &res)
		for i := start; i < end; i++ {
			c, ok := res.Created[fmt.Sprint(i)]
			if !ok {
				e := res.NotCreated[fmt.Sprint(i)]
				return ids, &e
			}
			ids = append(ids, c.ID)
		}
	}
	return ids, nil
}

func (j *JMAP) Changes(ctx context.Context) ([]Ref, []string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == "" {
		return j.resync(ctx)
	}
	var touched []string
	var destroyed []string
	state := j.state
	for {
		r, err := j.calls(ctx, call{"Email/changes", map[string]any{"accountId": j.account, "sinceState": state}})
		var me *MethodError
		if errors.As(err, &me) && (me.Type == "cannotCalculateChanges" || me.Type == "invalidArguments") {
			return j.resync(ctx) // §10.2: both mean a full resync
		}
		if err != nil {
			return nil, nil, err
		}
		var c struct {
			NewState                    string
			HasMoreChanges              bool
			Created, Updated, Destroyed []string
		}
		json.Unmarshal(r[0], &c)
		touched = append(append(touched, c.Created...), c.Updated...)
		destroyed = append(destroyed, c.Destroyed...)
		state = c.NewState
		if !c.HasMoreChanges {
			break
		}
	}
	var added []Ref
	var removed []string
	inFolder, err := j.headers(ctx, touched)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range touched {
		tag, in := inFolder[id]
		_, was := j.known[id]
		switch {
		case in && !was:
			j.known[id] = tag
			added = append(added, Ref{id, tag})
		case !in && was: // moved out: an update, not a destroy
			delete(j.known, id)
			removed = append(removed, id)
		}
	}
	for _, id := range destroyed {
		if _, was := j.known[id]; was {
			delete(j.known, id)
			removed = append(removed, id)
		}
	}
	j.state = state
	return added, removed, nil
}

// headers returns the channel tag of each id that is in the Tandem mailbox.
func (j *JMAP) headers(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for start := 0; start < len(ids); start += 500 {
		r, err := j.calls(ctx, call{"Email/get", map[string]any{"accountId": j.account, "ids": ids[start:min(start+500, len(ids))],
			"properties": []string{"mailboxIds", "header:Tandem-Channel:asText"}}})
		if err != nil {
			return nil, err
		}
		var g struct {
			List []struct {
				ID         string
				MailboxIDs map[string]bool `json:"mailboxIds"`
				Tag        *string         `json:"header:Tandem-Channel:asText"`
			}
		}
		json.Unmarshal(r[0], &g)
		for _, m := range g.List {
			if m.MailboxIDs[j.mailbox] {
				tag := ""
				if m.Tag != nil {
					tag = strings.TrimSpace(*m.Tag)
				}
				out[m.ID] = tag
			}
		}
	}
	return out, nil
}

// resync lists the whole folder and reports the difference to what was known.
func (j *JMAP) resync(ctx context.Context) ([]Ref, []string, error) {
	var ids []string
	state := ""
	for pos := 0; ; {
		r, err := j.calls(ctx,
			call{"Email/get", map[string]any{"accountId": j.account, "ids": []string{}}},
			call{"Email/query", map[string]any{"accountId": j.account, "filter": map[string]any{"inMailbox": j.mailbox},
				"position": pos, "limit": 1000}})
		if err != nil {
			return nil, nil, err
		}
		var g struct{ State string }
		var q struct{ IDs []string }
		json.Unmarshal(r[0], &g)
		json.Unmarshal(r[1], &q)
		if state == "" {
			state = g.State // before the first page: changes after it are caught next time
		}
		ids = append(ids, q.IDs...)
		if len(q.IDs) < 1000 {
			break
		}
		pos += len(q.IDs)
	}
	tags, err := j.headers(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	var added []Ref
	var removed []string
	for id, tag := range tags {
		if _, was := j.known[id]; !was {
			added = append(added, Ref{id, tag})
		}
	}
	for id := range j.known {
		if _, in := tags[id]; !in {
			removed = append(removed, id)
		}
	}
	j.known = tags
	j.state = state
	return added, removed, nil
}

func (j *JMAP) Fetch(ctx context.Context, ids []string) (map[string]Message, error) {
	out := map[string]Message{}
	for start := 0; start < len(ids); start += 100 {
		r, err := j.calls(ctx, call{"Email/get", map[string]any{"accountId": j.account, "ids": ids[start:min(start+100, len(ids))],
			"properties":          []string{"blobId", "textBody", "bodyValues", "header:Tandem-Channel:asText"},
			"fetchTextBodyValues": true}})
		if err != nil {
			return nil, err
		}
		var g struct {
			List []struct {
				ID, BlobID string
				TextBody   []struct{ PartID string }
				BodyValues map[string]struct {
					Value             string
					IsEncodingProblem bool
					IsTruncated       bool
				}
				Tag *string `json:"header:Tandem-Channel:asText"`
			}
		}
		if err := json.Unmarshal(r[0], &g); err != nil {
			return nil, err
		}
		for _, m := range g.List {
			tag := ""
			if m.Tag != nil {
				tag = strings.TrimSpace(*m.Tag)
			}
			var env []byte
			if len(m.TextBody) == 1 {
				v := m.BodyValues[m.TextBody[0].PartID]
				if !v.IsEncodingProblem && !v.IsTruncated {
					env, _ = tandem.DecodeText(v.Value) // steps 1 and 2 done by the server
				}
			}
			if env == nil { // §10.2: read the raw message as an IMAP reader would
				raw, err := j.download(ctx, m.BlobID)
				if err != nil {
					return nil, err
				}
				_, env, _ = tandem.ParseMessage(raw)
			}
			if env != nil {
				out[m.ID] = Message{Tag: tag, Envelope: env}
			}
		}
	}
	return out, nil
}

func (j *JMAP) download(ctx context.Context, blob string) ([]byte, error) {
	u := strings.NewReplacer("{accountId}", url.PathEscape(j.account), "{blobId}", url.PathEscape(blob),
		"{type}", url.QueryEscape("message/rfc822"), "{name}", "m.eml").Replace(j.dlURL)
	return j.do(ctx, "GET", u, nil)
}

func (j *JMAP) Destroy(ctx context.Context, ids []string) error {
	for start := 0; start < len(ids); start += j.maxSet {
		_, err := j.calls(ctx, call{"Email/set", map[string]any{"accountId": j.account, "destroy": ids[start:min(start+j.maxSet, len(ids))]}})
		if err != nil {
			return err
		}
	}
	return nil
}

// Watch follows the EventSource (§10.2). Reconnects when neither an event
// nor a ping arrives for two of the intervals the server says it pings at;
// every (re)connect counts as a change, since events may have been missed.
func (j *JMAP) Watch(ctx context.Context, onChange func()) error {
	for ctx.Err() == nil {
		err := j.watchOnce(ctx, onChange)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrAuth) {
			return err
		}
		select { // brief back-off before reconnecting after an error
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}

func (j *JMAP) watchOnce(ctx context.Context, onChange func()) error {
	u := strings.NewReplacer("{types}", "Email", "{closeafter}", "no", "{ping}", "60").Replace(j.esURL)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", j.rebase(u), nil)
	if err != nil {
		return err
	}
	j.auth(req)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := j.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrAuth
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("jmap: eventsource HTTP %d", resp.StatusCode)
	}
	onChange()

	interval := 60 * time.Second
	alive := time.AfterFunc(2*interval, cancel) // the safety net behind the event stream
	defer alive.Stop()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	event, data := "", ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // dispatch
			alive.Reset(2 * interval)
			if event == "ping" {
				var p struct{ Interval int }
				if json.Unmarshal([]byte(data), &p) == nil && p.Interval > 0 {
					interval = time.Duration(p.Interval) * time.Second
					alive.Reset(2 * interval)
				}
			} else if data != "" {
				onChange()
			}
			event, data = "", ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimPrefix(line[5:], " ")
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return errors.New("jmap: eventsource closed")
}

func (j *JMAP) Close() error { j.http.CloseIdleConnections(); return nil }
