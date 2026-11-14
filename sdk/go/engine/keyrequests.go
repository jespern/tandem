package engine

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Key requests (§11.4): a device that learns of an epoch whose key it lacks waits for a grant;
// after a safety-net delay it asks the owner's devices once. A current member's keychain gets
// the grant again; someone since removed gets the roster statements that prove it.

// SetKeyRequestDelay sets how long a device waits for a grant before it asks (§11.4: 10 minutes).
func (d *Device) SetKeyRequestDelay(delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keyRequestDelay = delay
}

// noteMissingKeys notes the lists this participant follows whose current epoch it has no key
// for, by epoch, with the time first seen.
func (d *Device) noteMissingKeys() error {
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		if !d.following(c.id) {
			continue
		}
		epoch := c.epochOf(c.head())
		if epoch == nil || c.keys[string(epoch)] != nil {
			continue
		}
		name := "keymiss." + hexs(c.id) + "." + hexs(epoch)
		if _, ok := d.store.Value(name); !ok {
			if err := d.store.SetValue(name, []byte(strconv.FormatUint(nowMillis(), 10))); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendDueKeyRequests asks for the keys that have been missing longer than the delay, once
// each, and returns when the next one is due (0 if none).
func (d *Device) sendDueKeyRequests(ctx context.Context) (time.Duration, error) {
	me := d.me
	if me == nil || !d.registry.Activity(me.Certificate).IsValid() {
		return 0, nil
	}
	delay := uint64(d.keyRequestDelay.Milliseconds())
	var next uint64
	vs, err := d.store.Values("keymiss.")
	if err != nil {
		return 0, err
	}
	for _, v := range vs {
		parts := strings.Split(v.Name, ".")
		if len(parts) != 3 {
			continue
		}
		cid, ok1 := unhexs(parts[1])
		epoch, ok2 := unhexs(parts[2])
		first, err := strconv.ParseUint(string(v.Value), 10, 64)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		c := d.collections[string(cid)]
		if c == nil || c.keys[string(epoch)] != nil || !d.following(cid) || c.head() == nil {
			if err := d.store.RemoveValue(v.Name); err != nil { // the key came, or the list went
				return 0, err
			}
			continue
		}
		if _, asked := d.store.Value("keyreq." + v.Name); asked {
			continue
		}
		if now := nowMillis(); now < first+delay {
			if wait := first + delay - now; next == 0 || wait < next {
				next = wait
			}
			continue
		}
		if err := d.sendKeyRequest(ctx, cid, c, c.head(), me); err != nil {
			return 0, err
		}
		if err := d.store.SetValue("keyreq."+v.Name, []byte{1}); err != nil {
			return 0, err
		}
	}
	return time.Duration(next) * time.Millisecond, nil
}

func (d *Device) sendKeyRequest(ctx context.Context, cid []byte, c *collection, have []byte, me *Me) error {
	owner := c.owner()
	if owner == nil {
		return nil
	}
	r := newControl("key-request", tandem.Fields{"hub": d.pin.Hub, "collection": cid, "participant": me.Participant, "have": have,
		"signer": me.Certificate, "auth": me.Auth})
	if err := signControl(r, "sig", me.Keys.Signing); err != nil {
		return err
	}
	if err := d.toInboxes(ctx, owner, r); err != nil {
		return err
	}
	// And as a notice in the last epoch channel it can read, which the owner's devices also watch.
	line := chain(c, have)
	for i := len(line) - 1; i >= 0; i-- {
		if e := c.epochOf(line[i]); e != nil && c.keys[string(e)] != nil {
			body, err := cjson(r)
			if err != nil {
				return err
			}
			return d.publishNotice(ctx, cid, e, body)
		}
	}
	return nil
}

// receiveKeyRequest is the owner's side: check a key request, then answer it after the sync, once.
func (d *Device) receiveKeyRequest(j tandem.JSON) (outcome, error) {
	me := d.me
	r, err := tandem.ParseControl(j, "key-request")
	if me == nil || err != nil || !bytes.Equal(fid(r, "hub"), d.pin.Hub) {
		return ignored, nil
	}
	c := d.collections[string(fid(r, "collection"))]
	if c == nil || !bytes.Equal(c.owner(), me.Participant) {
		return ignored, nil
	}
	participant, signer, auth, id := fid(r, "participant"), fid(r, "signer"), fid(r, "auth"), oid(r)
	switch d.registry.Authorizes(auth, signer).State {
	case Pending:
		return waiting, nil
	case Invalid:
		return ignored, nil
	}
	// A revoked keychain gets nothing, whatever its participant's standing (§11.4).
	cert := d.registry.Certificate(signer)
	if !d.registry.Activity(signer).IsValid() || cert == nil || !bytes.Equal(cert.Participant, participant) || !cverify(r, "sig", cert.Sign) {
		return ignored, nil
	}
	if _, done := d.store.Value("keyreq.answered." + hexs(id)); done {
		return ignored, nil
	}
	body, err := cjson(r)
	if err != nil {
		return ignored, err
	}
	return applied, d.store.SetValue("keyreq.pending."+hexs(id), body)
}

// answerKeyRequests answers the key requests received: a grant for a current member, the
// roster that removed them otherwise.
func (d *Device) answerKeyRequests(ctx context.Context) error {
	me := d.me
	if me == nil {
		return nil
	}
	vs, err := d.store.Values("keyreq.pending.")
	if err != nil {
		return err
	}
	for _, v := range vs {
		id := strings.TrimPrefix(v.Name, "keyreq.pending.")
		if err := d.answerKeyRequest(ctx, v.Value, me); err != nil {
			return err
		}
		if err := d.store.RemoveValue(v.Name); err != nil {
			return err
		}
		if err := d.store.SetValue("keyreq.answered."+id, []byte{1}); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) answerKeyRequest(ctx context.Context, data []byte, me *Me) error {
	r, err := tandem.FromJSON(data, "key-request")
	if err != nil {
		return nil
	}
	cid, participant, have, signer := fid(r, "collection"), fid(r, "participant"), fid(r, "have"), fid(r, "signer")
	c := d.collections[string(cid)]
	if c == nil {
		return nil
	}
	head := c.head()
	epoch := c.epochOf(head)
	key, cert := c.keys[string(epoch)], d.registry.Certificate(signer)
	if key == nil || cert == nil {
		return nil
	}
	if admission := admissionOf(participant, c, head); admission != nil {
		d.sendGrant(ctx, cid, participant, admission, head, epoch, key, me) // best effort, as asked
		return nil
	}
	// Removed: the statements from have through the one that removed them prove it.
	line := chain(c, head)
	from := -1
	for i, sid := range line {
		if bytes.Equal(sid, have) {
			from = i
		}
	}
	if from < 0 {
		return nil
	}
	var statements []*tandem.Write
	for _, sid := range line[from+1:] {
		s := c.statements[string(sid)]
		statements = append(statements, s.w)
		if s.s.member(participant) == nil {
			break
		}
	}
	if len(statements) == 0 {
		return nil
	}
	p, err := tandem.RecordPayload(rosterRecord, statements)
	if err != nil {
		return err
	}
	reply := tandem.NewJSONObject().Text("type", "roster").B64u("c", cid).B64u("statements", p).Bytes()
	env, err := tandem.SealInbox(d.pin.Hub, participant, cert.Agree, reply)
	if err != nil {
		return err
	}
	_, err = d.appendNoted(ctx, tandem.InboxTag(participant, d.pin.Hub), env)
	return err
}

// receiveRosterReply takes a roster reply in this participant's inbox: statements that prove
// themselves, read as announced.
func (d *Device) receiveRosterReply(j tandem.JSON) (outcome, error) {
	cid, err := j.B64u("c", 16)
	if err != nil || d.collections[string(cid)] == nil {
		return ignored, nil
	}
	payload, err := j.B64u("statements", -1)
	if err != nil {
		return ignored, nil
	}
	state, _, err := tandem.ParseKind1(payload, d.pin.Hub, cid)
	if err != nil || state == nil || !bytes.Equal(state.Record, rosterRecord) {
		return ignored, nil
	}
	anyApplied := false
	for _, w := range state.Writes {
		channel := d.collections[string(cid)].epochOf(w.Ref)
		if channel == nil {
			channel = w.Ref
		}
		o, err := d.acceptStatement(w, cid, channel)
		if err != nil {
			return ignored, err
		}
		switch o {
		case applied:
			anyApplied = true
		case waiting:
			return waiting, nil
		}
	}
	if !anyApplied {
		return ignored, nil
	}
	d.changed(Event{Kind: EventLists})
	d.changed(Event{Kind: EventMembers, List: cid})
	return applied, nil
}

// keyRequestFollowUp runs after a sync: note missing keys, answer requests, and arm the one
// safety-net timer if a request will be due.
func (d *Device) keyRequestFollowUp(ctx context.Context) error {
	if err := d.noteMissingKeys(); err != nil {
		return err
	}
	if err := d.answerKeyRequests(ctx); err != nil {
		return err
	}
	due, err := d.sendDueKeyRequests(ctx)
	if err != nil || due == 0 || d.keyRequestTimer != nil || d.life.Err() != nil {
		return err
	}
	// allowed-timer: §11.4's safety net behind grant delivery; the only timer the engine has.
	d.keyRequestTimer = time.AfterFunc(due, d.keyRequestTimerFired)
	return nil
}

func (d *Device) keyRequestTimerFired() {
	defer d.begin()()
	d.keyRequestTimer = nil
	if d.life.Err() != nil {
		return
	}
	if err := d.keyRequestFollowUp(d.life); err != nil {
		d.changed(Event{Kind: EventSyncFailed, Message: err.Error()})
	}
}
