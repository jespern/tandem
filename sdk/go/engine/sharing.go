package engine

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// offer is a grant delivered to this participant (§11.4): the owner's signed grant and the
// key it commits to.
type offer struct {
	grant *tandem.Control
	key   []byte
}

func (o *offer) collection() []byte { return fid(o.grant, "collection") }
func (o *offer) admission() []byte  { return fid(o.grant, "admission") }
func (o *offer) epoch() []byte      { return fid(o.grant, "epoch") }

// errNoDevice: the participant has no admitted device to send to.
var errNoDevice = errors.New("tandem/engine: share: that participant has no admitted device")

// Share adds participant to a list this participant owns, as a writer or reader, in the
// current epoch, and sends them a grant. name is how the roster lists them; it may be "".
func (d *Device) Share(ctx context.Context, list, participant []byte, role Role, name string) error {
	defer d.begin()()
	return d.share(ctx, list, participant, role, name)
}

func (d *Device) share(ctx context.Context, cid, participant []byte, role Role, name string) error {
	if role != Writer && role != Reader {
		return fail("share: a list has one owner")
	}
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	me, c := d.me, d.collections[string(cid)]
	if me == nil || c == nil || !bytes.Equal(c.owner(), me.Participant) || c.dead() {
		return fail("share: not a list this participant owns, or it was deleted")
	}
	s, ok := c.headStatement()
	if !ok || s.Deleted {
		return fail("share: not a list this participant owns, or it was deleted")
	}
	if s.member(participant) != nil {
		return nil
	}
	floor, err := d.publishedSnapshot(ctx, participant)
	if err != nil {
		return err
	}
	// Until the grant is out, a crash leaves this mark for the next sync to finish (§11.4).
	pendingName := "pendinggrant." + hexs(cid) + "." + hexs(participant)
	if err := d.store.SetValue(pendingName, []byte{1}); err != nil {
		return err
	}
	sid, epoch, key, err := d.sameEpoch(ctx, cid, func(st *Statement) {
		st.Members = append(st.Members, Member{UID: participant, Role: role, Name: name, Floor: floor})
	})
	if err != nil {
		return err
	}
	if err := d.sendGrant(ctx, cid, participant, sid, sid, epoch, key, me); err != nil {
		return err
	}
	return d.store.RemoveValue(pendingName)
}

// finishPendingGrants sends grants a crash kept from going out after their member was added.
func (d *Device) finishPendingGrants(ctx context.Context) error {
	me := d.me
	if me == nil {
		return nil
	}
	vs, err := d.store.Values("pendinggrant.")
	if err != nil {
		return err
	}
	for _, v := range vs {
		parts := strings.Split(v.Name, ".")
		if len(parts) != 3 {
			continue
		}
		cid, ok1 := unhexs(parts[1])
		uid, ok2 := unhexs(parts[2])
		c := d.collections[string(cid)]
		if !ok1 || !ok2 || c == nil {
			continue
		}
		head := c.head()
		epoch := c.epochOf(head)
		key := c.keys[string(epoch)]
		if key == nil {
			continue
		}
		if admission := admissionOf(uid, c, head); admission != nil {
			if err := d.sendGrant(ctx, cid, uid, admission, head, epoch, key, me); err != nil {
				return err
			}
		}
		if err := d.store.RemoveValue(v.Name); err != nil {
			return err
		}
	}
	return nil
}

// publishedSnapshot is a snapshot of participant that the directory holds, publishing the
// current one if none does (§6.9).
func (d *Device) publishedSnapshot(ctx context.Context, participant []byte) ([]byte, error) {
	current := d.registry.CurrentSnapshot(participant)
	id := oid(current)
	if d.registry.Snapshot(id) == nil {
		if _, err := d.registry.Add(current); err != nil {
			return nil, err
		}
		if _, err := d.store.PutObject(current); err != nil {
			return nil, err
		}
		if err := d.publishObjects(ctx, current); err != nil {
			return nil, err
		}
	}
	return id, nil
}

// sendGrant sends one grant per admitted keychain of the recipient, sealed to its agreement
// key in their inbox (§11.4).
func (d *Device) sendGrant(ctx context.Context, cid, participant, admission, at, epoch, key []byte, me *Me) error {
	grant := newControl("grant", tandem.Fields{"hub": d.pin.Hub, "collection": cid, "recipient": participant, "admission": admission,
		"at": at, "epoch": epoch, "key": tandem.Commit(key), "signer": me.Certificate, "auth": me.Auth})
	if err := signControl(grant, "sig", me.Keys.Signing); err != nil {
		return err
	}
	inbox := tandem.InboxTag(participant, d.pin.Hub)
	var devices []*Certificate
	for _, c := range d.registry.Certificates() {
		if bytes.Equal(c.Participant, participant) && d.registry.Activity(c.ID).IsValid() {
			devices = append(devices, c)
		}
	}
	if len(devices) == 0 {
		return errNoDevice
	}
	gj, err := cjson(grant)
	if err != nil {
		return err
	}
	for _, cert := range devices {
		delivery := tandem.NewJSONObject().Text("type", "grant").Raw("grant", gj).B64u("certificate", cert.ID).B64u("k", key).Bytes()
		env, err := tandem.SealInbox(d.pin.Hub, participant, cert.Agree, delivery)
		if err != nil {
			return err
		}
		if _, err := d.appendNoted(ctx, inbox, env); err != nil {
			return err
		}
		// Grants are kept (§11.4): this one is healed if it goes missing, and replaces the last one to that keychain.
		kept := "kept.grant." + hexs(cid) + "." + hexs(participant) + "." + hexs(cert.ID)
		earlier, had := d.store.Value(kept)
		if err := d.store.SetValue(kept, env); err != nil {
			return err
		}
		if had && !bytes.Equal(earlier, env) {
			old, err := d.store.MessagesWithHash(tandem.H(earlier))
			if err != nil {
				return err
			}
			if len(old) > 0 {
				if err := d.hubDestroy(ctx, old); err != nil {
					return err
				}
				if err := d.store.ForgetMessages(old); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// receiveGrant: a grant arrived: keep it as an offer and start reading the collection's
// channel to validate it.
func (d *Device) receiveGrant(plaintext []byte, j tandem.JSON, me *Me) (outcome, error) {
	g, ok := j.Get("grant")
	if !ok {
		return ignored, nil
	}
	grant, err := tandem.ParseControl(g, "grant")
	if err != nil || !bytes.Equal(fid(grant, "hub"), d.pin.Hub) || !bytes.Equal(fid(grant, "recipient"), me.Participant) {
		return ignored, nil
	}
	cert, err1 := j.B64u("certificate", 32)
	key, err2 := j.B64u("k", 32)
	if err1 != nil || err2 != nil || !bytes.Equal(cert, me.Certificate) || !bytes.Equal(fid(grant, "key"), tandem.Commit(key)) {
		return ignored, nil
	}
	o := &offer{grant: grant, key: key}
	id := oid(grant)
	if d.offers[string(id)] != nil {
		return ignored, nil
	}
	d.offers[string(id)] = o
	if err := d.store.SetValue("offer."+hexs(id), plaintext); err != nil {
		return ignored, err
	}
	d.collection(o.collection()).offer(key, o.epoch())
	return applied, nil
}

// offerValidity: whether an offer is a real admission (§11.4): the roster in its channel,
// from a genesis whose owner signed the grant, through at, which lists this participant and
// starts or continues the granted epoch.
func (d *Device) offerValidity(o *offer, me *Me) Validity {
	c := d.collections[string(o.collection())]
	if c == nil || c.owner() == nil {
		return pending()
	}
	at := fid(o.grant, "at")
	s, ok := c.statements[string(at)]
	if !ok {
		return pending()
	}
	if !bytes.Equal(c.epochOf(at), o.epoch()) || !bytes.Equal(s.s.Commit, tandem.Commit(o.key)) || s.s.member(me.Participant) == nil {
		return invalid("the roster doesn't grant this")
	}
	signer, auth := fid(o.grant, "signer"), fid(o.grant, "auth")
	if v := d.registry.Authorizes(auth, signer); !v.IsValid() {
		return v
	}
	cert := d.registry.Certificate(signer)
	if cert == nil || !bytes.Equal(cert.Participant, c.owner()) || !cverify(o.grant, "sig", cert.Sign) {
		return invalid("not granted by the owner")
	}
	return valid()
}

func (d *Device) sortedOffers() []*offer {
	var out []*offer
	for _, k := range sortedKeys(d.offers) {
		out = append(out, d.offers[k])
	}
	return out
}

// adoptGrantedChains: a verified grant shows that its epoch was announced: the owner grants
// only after announcing (§11.6). Every epoch start on the line to its at counts from then on,
// for a member added after a key change and for one whose announcement was deleted.
func (d *Device) adoptGrantedChains() (bool, error) {
	me := d.me
	if me == nil {
		return false, nil
	}
	anyAdopted := false
	for _, o := range d.sortedOffers() {
		if a, ok := d.answer(o); ok && a == 'n' {
			continue
		}
		at := fid(o.grant, "at")
		c := d.collections[string(o.collection())]
		if c == nil {
			continue
		}
		if _, ok := c.statements[string(at)]; !ok || !d.offerValidity(o, me).IsValid() {
			continue
		}
		changedHere := false
		for _, id := range chain(c, at) {
			if c.startsEpoch(id) && !c.announced[string(id)] {
				c.announced[string(id)] = true
				if err := d.store.SetValue("ann."+hexs(o.collection())+"."+hexs(id), []byte{1}); err != nil {
					return anyAdopted, err
				}
				changedHere = true
			}
		}
		if !changedHere {
			continue
		}
		if err := d.moveCollection(o.collection()); err != nil {
			return anyAdopted, err
		}
		d.changed(Event{Kind: EventLists})
		d.changed(Event{Kind: EventMembers, List: o.collection()})
		anyAdopted = true
	}
	return anyAdopted, nil
}

// Offer is a list shared with this participant that it hasn't answered (§11.2).
type Offer struct {
	List  []byte
	Name  string
	Owner []byte
}

// PendingOffers is the lists shared with this participant that it hasn't answered.
func (d *Device) PendingOffers() []Offer {
	defer d.read()()
	me := d.me
	if me == nil {
		return nil
	}
	open := map[string]bool{}
	for _, o := range d.offers {
		if _, answered := d.answer(o); !answered && d.offerValidity(o, me).IsValid() {
			open[string(o.collection())] = true
		}
	}
	var out []Offer
	for _, k := range sortedKeys(open) {
		c := d.collections[k]
		if c == nil || c.owner() == nil {
			continue
		}
		name, _ := value(c.records[string(metaRecord)], "name")
		out = append(out, Offer{List: c.id, Name: string(name), Owner: c.owner()})
	}
	return out
}

// validOffers is the valid grants for a list, latest epoch first.
func (d *Device) validOffers(cid []byte, me *Me) []*offer {
	c := d.collections[string(cid)]
	var out []*offer
	for _, o := range d.sortedOffers() {
		if bytes.Equal(o.collection(), cid) && d.offerValidity(o, me).IsValid() {
			out = append(out, o)
		}
	}
	keyN := func(o *offer) uint64 {
		if c == nil {
			return 0
		}
		return c.statements[string(o.epoch())].s.KeyN
	}
	slices.SortStableFunc(out, func(a, b *offer) int {
		ka, kb := keyN(a), keyN(b)
		switch {
		case ka > kb:
			return -1
		case ka < kb:
			return 1
		}
		return 0
	})
	return out
}

// takeGrantedKeys puts keys of later epochs for lists this participant accepted into home,
// once their grants check out (§11.4).
func (d *Device) takeGrantedKeys(ctx context.Context) error {
	me := d.me
	if me == nil {
		return nil
	}
	for _, o := range d.sortedOffers() {
		if a, ok := d.answer(o); !ok || a != 'a' || !d.offerValidity(o, me).IsValid() {
			continue
		}
		name, element := membershipName(o.collection()), keyElement(o.epoch(), o.key)
		if containsID(d.homeElements(name, "keys"), element) {
			continue
		}
		if err := d.addHome(ctx, name, "keys", element); err != nil {
			return err
		}
		if err := d.moveCollection(o.collection()); err != nil {
			return err
		}
	}
	return nil
}

// answer is the letter this participant answered an offer's admission with, from home.
func (d *Device) answer(o *offer) (byte, bool) {
	for _, a := range d.homeElements(membershipName(o.collection()), "answers") {
		if len(a) == 33 && bytes.Equal(a[:32], o.admission()) {
			return a[32], true
		}
	}
	return 0, false
}

// Accept adds a shared list (§11.2): the answer and the key go into home, so every device of
// this participant follows it.
func (d *Device) Accept(ctx context.Context, list []byte) error {
	defer d.begin()()
	me := d.me
	if me == nil {
		return fail("accept: no valid offer for that list")
	}
	var o *offer
	for _, x := range d.validOffers(list, me) {
		if _, answered := d.answer(x); !answered {
			o = x
			break
		}
	}
	c := d.collections[string(list)]
	if o == nil || c == nil || c.owner() == nil {
		return fail("accept: no valid offer for that list")
	}
	member := c.statements[string(fid(o.grant, "at"))].s.member(me.Participant)
	if member == nil {
		return fail("accept: no valid offer for that list")
	}
	m := membershipName(list)
	if err := d.addHome(ctx, m, "answers", append(bytes.Clone(o.admission()), 'a')); err != nil {
		return err
	}
	if err := d.setHome(ctx, m, "owner", c.owner()); err != nil {
		return err
	}
	if err := d.setHome(ctx, m, "role", []byte(member.Role)); err != nil {
		return err
	}
	if err := d.addHome(ctx, m, "keys", keyElement(o.epoch(), o.key)); err != nil {
		return err
	}
	return d.sendAnswer(ctx, list, o.admission(), 'a', me)
}
