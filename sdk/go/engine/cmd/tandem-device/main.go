// Command tandem-device drives one Tandem device over stdin/stdout, one JSON command per line, for the cross-engine
// tests in interop/. Every command answers one line: {"ok":true,…} or {"ok":false,"error":"…"}. Bytes travel as
// b64u. It speaks the same commands as the Swift driver (sdk/swift/Sources/tandem-device), so a test can put devices
// on different engines on one hub.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/engine"
	"github.com/jespern/tandem/sdk/go/imap"
)

type driver struct {
	account  engine.MailboxAccount
	folder   string
	insecure bool
	device   *engine.Device
	ms       []byte
	blobs    *imap.BlobStore
}

func (d *driver) imapConfig() imap.Config {
	security := imap.TLS
	if d.insecure {
		security = imap.TLSTrustingAnyCertificateForTesting
	}
	return imap.Config{Host: d.account.Host, Port: int(d.account.Port), Username: d.account.User,
		Password: d.account.Password, Security: security, Folder: d.folder}
}

var blobStates = map[engine.BlobState]string{engine.BlobHere: "here", engine.BlobRemote: "remote", engine.BlobDownloading: "downloading",
	engine.BlobUnavailable: "unavailable", engine.BlobUnreadable: "unreadable", engine.BlobUploading: "uploading"}

// blobOp handles the blob commands; ok is false for any other op.
func (d *driver) blobOp(ctx context.Context, dev *engine.Device, op string, a args) (map[string]any, bool, error) {
	if op == "useBlobs" {
		size, _ := a["chunkSize"].(float64)
		b, err := imap.NewBlobStore(d.imapConfig(), imap.BlobOptions{Folder: d.folder + "-blobs"})
		if err != nil {
			return nil, true, err
		}
		d.blobs = b
		return nil, true, dev.UseBlobs(b, engine.BlobSettings{ChunkSize: int(size)})
	}
	if op == "settleBlobs" {
		return nil, true, dev.SettleBlobs(ctx)
	}
	if op == "checkBlobs" {
		dev.CheckBlobs()
		return nil, true, dev.SettleBlobs(ctx)
	}
	list, err1 := a.b("list")
	record, err2 := a.b("record")
	name, err3 := a.s("name")
	switch op {
	case "attach", "blob", "fetchBlob", "dropChunk", "chunkCounts":
		if err := errors.Join(err1, err2, err3); err != nil {
			return nil, true, err
		}
	default:
		return nil, false, nil
	}
	switch op {
	case "attach":
		content, err := a.b("content")
		if err != nil {
			return nil, true, err
		}
		return nil, true, dev.AttachBytes(ctx, list, record, name, content)
	case "blob":
		b, ok := dev.Blob(list, record, name)
		if !ok {
			return map[string]any{"state": nil}, true, nil
		}
		return map[string]any{"state": blobStates[b.State], "size": b.Size}, true, nil
	case "fetchBlob":
		path, err := dev.FetchBlob(ctx, list, record, name)
		if err != nil {
			return nil, true, err
		}
		content, err := os.ReadFile(path)
		return map[string]any{"content": tandem.B64u(content)}, true, err
	}
	value, ok := dev.Value(list, record, engine.BlobPrefix+name)
	m, kind := tandem.ParseManifest(value)
	if !ok || kind != tandem.ManifestV1 || d.blobs == nil {
		return nil, true, errors.New("no manifest, or blobs not in use")
	}
	refs, err := d.allRefs(ctx)
	if err != nil {
		return nil, true, err
	}
	if op == "chunkCounts" {
		var counts []int
		for _, e := range m.Chunks {
			n := 0
			for _, r := range refs {
				if string(r.Address) == string(e.Address) {
					n++
				}
			}
			counts = append(counts, n)
		}
		return map[string]any{"counts": counts}, true, nil
	}
	index, _ := a["index"].(float64)
	var ids []string
	for _, r := range refs {
		if string(r.Address) == string(m.Chunks[int(index)].Address) {
			ids = append(ids, r.ID)
		}
	}
	return nil, true, d.blobs.Destroy(ctx, ids)
}

func (d *driver) allRefs(ctx context.Context) ([]tandem.BlobRef, error) {
	var out []tandem.BlobRef
	cursor := ""
	for {
		c, err := d.blobs.Changes(ctx, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, c.Added...)
		if !c.More {
			return out, nil
		}
		cursor = c.Cursor
	}
}

type args map[string]any

func (a args) s(k string) (string, error) {
	v, ok := a[k].(string)
	if !ok {
		return "", fmt.Errorf("missing %s", k)
	}
	return v, nil
}

func (a args) b(k string) ([]byte, error) {
	s, err := a.s(k)
	if err != nil {
		return nil, err
	}
	return tandem.ParseB64u(s)
}

func (d *driver) store() (tandem.PushStore, error) {
	if d.account.Host == "" {
		return nil, errors.New(`no hub: send {"op":"hub",…} first`)
	}
	s, err := imap.New(d.imapConfig())
	if err != nil {
		return nil, err
	}
	if err := s.EnsureFolder(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (d *driver) dev() (*engine.Device, error) {
	if d.device == nil {
		return nil, errors.New("no device yet")
	}
	return d.device, nil
}

func memoryStore() (*engine.Store, error) { return engine.OpenStore(":memory:") }

func (d *driver) handle(ctx context.Context, a args) (map[string]any, error) {
	op, err := a.s("op")
	if err != nil {
		return nil, err
	}
	if op == "hub" {
		port, _ := a["port"].(float64)
		if port == 0 {
			port = 993
		}
		d.account = engine.MailboxAccount{Host: str(a["host"]), Port: uint16(port), User: str(a["user"]), Password: str(a["password"])}
		d.folder = str(a["folder"])
		d.insecure, _ = a["insecure"].(bool)
		return nil, nil
	}
	if op == "found" || op == "join" || op == "pair" {
		hub, err := d.store()
		if err != nil {
			return nil, err
		}
		store, err := memoryStore()
		if err != nil {
			return nil, err
		}
		keys, err := tandem.NewDeviceKeys()
		if err != nil {
			return nil, err
		}
		label, _ := a.s("label")
		switch op {
		case "found":
			f, err := engine.Found(d.account, keys, label, nil)
			if err != nil {
				return nil, err
			}
			if d.device, err = engine.Open(f.Genesis, f.DirectoryKey, keys, store, hub); err != nil {
				return nil, err
			}
			d.ms = f.ManagementSeed
			return map[string]any{"participant": tandem.B64u(f.Participant)}, d.device.Publish(ctx, f)
		case "join":
			raw, err := a.b("link")
			if err != nil {
				return nil, err
			}
			link, err := engine.ParseInvitationLink(raw)
			if err != nil {
				return nil, err
			}
			d.device, err = engine.Join(ctx, link, keys, label, store, hub)
			return nil, err
		default:
			raw, err := a.b("link")
			if err != nil {
				return nil, err
			}
			link, err := engine.ParsePairingLink(raw)
			if err != nil {
				return nil, err
			}
			d.device, err = engine.Pair(ctx, link, keys, label, store, hub)
			return nil, err
		}
	}
	dev, err := d.dev()
	if err != nil {
		return nil, err
	}
	if out, ok, err := d.blobOp(ctx, dev, op, a); ok {
		return out, err
	}
	list, _ := a.b("list")
	record, _ := a.b("record")
	field, _ := a.s("field")
	switch op {
	case "invite":
		if d.ms == nil {
			return nil, errors.New("not the hub's admin")
		}
		label, _ := a.s("label")
		link, err := dev.Invite(ctx, label, d.account, d.ms, 0)
		if err != nil {
			return nil, err
		}
		j, err := link.JSON()
		return map[string]any{"link": tandem.B64u(j)}, err
	case "startPairing":
		label, _ := a.s("label")
		link, err := dev.StartPairing(ctx, label, d.account, 0)
		if err != nil {
			return nil, err
		}
		return map[string]any{"link": tandem.B64u(link.JSON())}, nil
	case "claims":
		claims := []any{}
		for _, c := range dev.PendingClaims() {
			claims = append(claims, map[string]any{"id": tandem.B64u(c.ID()), "participant": tandem.B64u(c.Participant()), "label": c.Label()})
		}
		return map[string]any{"claims": claims}, nil
	case "approve", "revoke":
		if d.ms == nil {
			return nil, errors.New("not the hub's admin")
		}
		if op == "approve" {
			id, err := a.b("claim")
			if err != nil {
				return nil, err
			}
			return nil, dev.Approve(ctx, id, d.ms)
		}
		cert, err := a.b("certificate")
		if err != nil {
			return nil, err
		}
		return nil, dev.Revoke(ctx, cert, d.ms)
	case "me":
		me := dev.Me()
		if me == nil {
			return map[string]any{"me": nil, "awaitingApproval": dev.AwaitingApproval()}, nil
		}
		return map[string]any{"participant": tandem.B64u(me.Participant), "certificate": tandem.B64u(me.Certificate),
			"awaitingApproval": dev.AwaitingApproval()}, nil
	case "sync":
		n, err := dev.Sync(ctx)
		return map[string]any{"applied": n}, err
	case "setName":
		name, _ := a.s("name")
		return nil, dev.SetName(ctx, name)
	case "people":
		people := []any{}
		for _, p := range dev.People() {
			var name any
			if p.Name != "" {
				name = p.Name
			}
			people = append(people, map[string]any{"id": tandem.B64u(p.ID), "name": name})
		}
		return map[string]any{"people": people}, nil
	case "createList":
		name, _ := a.s("name")
		id, err := dev.CreateList(ctx, name)
		return map[string]any{"list": tandem.B64u(id)}, err
	case "lists":
		lists := []any{}
		for _, l := range dev.Lists() {
			lists = append(lists, map[string]any{"id": tandem.B64u(l.ID), "name": l.Name})
		}
		return map[string]any{"lists": lists}, nil
	case "share":
		participant, err := a.b("participant")
		if err != nil {
			return nil, err
		}
		role, _ := a.s("role")
		return nil, dev.Share(ctx, list, participant, engine.Role(role), "")
	case "offers":
		offers := []any{}
		for _, o := range dev.PendingOffers() {
			offers = append(offers, map[string]any{"list": tandem.B64u(o.List), "name": o.Name})
		}
		return map[string]any{"offers": offers}, nil
	case "accept":
		return nil, dev.Accept(ctx, list)
	case "set":
		value, err := a.b("value")
		if err != nil {
			return nil, err
		}
		return nil, dev.SetField(ctx, list, record, field, value)
	case "add", "remove":
		element, err := a.b("element")
		if err != nil {
			return nil, err
		}
		if op == "add" {
			return nil, dev.Add(ctx, list, record, field, element)
		}
		return nil, dev.Remove(ctx, list, record, field, element)
	case "delete":
		return nil, dev.DeleteRecord(ctx, list, record)
	case "items":
		items := []any{}
		for _, it := range dev.Items(list) {
			values, sets := map[string]any{}, map[string]any{}
			for _, f := range it.Fields() {
				if v, ok := it.Value(f); ok {
					values[f] = tandem.B64u(v)
				}
				if es := it.Elements(f); len(es) > 0 {
					enc := []string{}
					for _, e := range es {
						enc = append(enc, tandem.B64u(e))
					}
					sets[f] = enc
				}
			}
			items = append(items, map[string]any{"record": tandem.B64u(it.ID), "values": values, "sets": sets})
		}
		return map[string]any{"items": items}, nil
	case "role":
		if r := dev.Role(list); r != "" {
			return map[string]any{"role": string(r)}, nil
		}
		return map[string]any{"role": nil}, nil
	case "removeMember":
		participant, err := a.b("participant")
		if err != nil {
			return nil, err
		}
		return nil, dev.RemoveMember(ctx, participant, list)
	case "rekey":
		return nil, dev.Rekey(ctx, list)
	case "copy":
		cp, err := dev.Copy(ctx, list)
		if err != nil {
			return nil, err
		}
		return map[string]any{"list": tandem.B64u(cp.List), "shared": b64us(cp.Shared), "undecided": b64us(cp.Undecided)}, nil
	case "copyOf":
		if from := dev.CopyOf(list); from != nil {
			return map[string]any{"from": tandem.B64u(from)}, nil
		}
		return map[string]any{"from": nil}, nil
	}
	return nil, errors.New("unknown op")
}

func str(v any) string { s, _ := v.(string); return s }

func b64us(xs [][]byte) []string {
	out := []string{}
	for _, x := range xs {
		out = append(out, tandem.B64u(x))
	}
	return out
}

func main() {
	d := &driver{folder: "Tandem"}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var a args
		reply := map[string]any{}
		if err := json.Unmarshal(in.Bytes(), &a); err != nil {
			reply = map[string]any{"ok": false, "error": err.Error()}
		} else if r, err := d.handle(context.Background(), a); err != nil {
			reply = map[string]any{"ok": false, "error": err.Error()}
		} else {
			for k, v := range r {
				reply[k] = v
			}
			reply["ok"] = true
		}
		out.Encode(reply)
	}
	if d.device != nil {
		d.device.Close()
	}
}
