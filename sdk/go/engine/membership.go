package engine

import (
	"bytes"
	"context"
	"slices"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Answering an admission, leaving and deleting (§11.2, §11.5, §11.9), and what the owner learns of them.

// MemberStatus is a member's answer to their current admission, as the owner's devices know it (§11.2).
type MemberStatus string

const (
	// StatusPending: added, no answer yet: "invitation pending".
	StatusPending  MemberStatus = "pending"
	StatusAccepted MemberStatus = "accepted"
	StatusDeclined MemberStatus = "declined"
	// StatusLeft: still listed until the owner removes them, which changes the key (§11.5).
	StatusLeft MemberStatus = "left"
)

// answers is the answer letters this participant gave an admission, from home (§6.2).
func (d *Device) answers(cid, admission []byte) map[byte]bool {
	out := map[byte]bool{}
	for _, a := range d.homeElements(membershipName(cid), "answers") {
		if len(a) == 33 && bytes.Equal(a[:32], admission) {
			out[a[32]] = true
		}
	}
	return out
}

// following reports whether this participant follows a list: its own, or one whose current
// admission it accepted and didn't leave.
func (d *Device) following(cid []byte) bool {
	me, c := d.me, d.collections[string(cid)]
	if me == nil || c == nil {
		return false
	}
	owner, ok := d.homeValue(membershipName(cid), "owner")
	if !ok {
		return false
	}
	head := c.head()
	if s, ok := c.statements[string(head)]; ok && s.s.Deleted {
		return false
	}
	if bytes.Equal(c.owner(), me.Participant) || bytes.Equal(owner, me.Participant) {
		return true
	}
	// Listed no more: removed. The replica stays, read-only (RemovedLists).
	if _, ok := c.statements[string(head)]; ok && head != nil && admissionOf(me.Participant, c, head) == nil {
		return false
	}
	var candidates [][]byte
	if head != nil {
		if a := admissionOf(me.Participant, c, head); a != nil {
			candidates = [][]byte{a}
		}
	}
	if candidates == nil {
		// Before the roster is here, the latest answered admission stands in for the current one.
		for _, a := range d.homeElements(membershipName(cid), "answers") {
			if len(a) == 33 {
				candidates = append(candidates, a[:32])
			}
		}
	}
	for _, a := range candidates {
		letters := d.answers(cid, a)
		if letters['a'] && !letters['l'] && !letters['n'] {
			return true
		}
	}
	return false
}

// RemovedLists is the lists this participant accepted and has since been removed from: their
// replicas stay, read-only, so the app can offer to keep what's in them.
func (d *Device) RemovedLists() [][]byte {
	defer d.read()()
	me := d.me
	if me == nil {
		return nil
	}
	var out [][]byte
	for _, k := range sortedKeys(d.collections) {
		c := d.collections[k]
		s, ok := c.headStatement()
		if bytes.Equal(c.owner(), me.Participant) || !ok || s.Deleted || admissionOf(me.Participant, c, c.head()) != nil {
			continue
		}
		if slices.ContainsFunc(d.homeElements(membershipName(c.id), "answers"), func(a []byte) bool { return len(a) == 33 && a[32] == 'a' }) {
			out = append(out, c.id)
		}
	}
	return out
}

// Decline says no to a list shared with this participant (§11.2): nothing appears, and the owner is told.
func (d *Device) Decline(ctx context.Context, list []byte) error {
	defer d.begin()()
	me := d.me
	if me == nil {
		return fail("decline: no offer for that list")
	}
	for _, o := range d.validOffers(list, me) {
		if _, answered := d.answer(o); answered {
			continue
		}
		if err := d.addHome(ctx, membershipName(list), "answers", append(bytes.Clone(o.admission()), 'n')); err != nil {
			return err
		}
		return d.sendAnswer(ctx, list, o.admission(), 'n', me)
	}
	return fail("decline: no offer for that list")
}

// Leave leaves a list (§11.5): every device of this participant stops following it, and a
// leave notice goes to the owner, who removes this participant when they confirm. The owner
// can't leave their own list.
func (d *Device) Leave(ctx context.Context, list []byte) error {
	defer d.begin()()
	me, c := d.me, d.collections[string(list)]
	if me == nil || c == nil || bytes.Equal(c.owner(), me.Participant) {
		return fail("leave: not a member of that list")
	}
	head := c.head()
	admission := admissionOf(me.Participant, c, head)
	epoch := c.epochOf(head)
	if admission == nil || epoch == nil || c.keys[string(epoch)] == nil {
		return fail("leave: not a member of that list")
	}
	if err := d.addHome(ctx, membershipName(list), "answers", append(bytes.Clone(admission), 'l')); err != nil {
		return err
	}
	n := newControl("leave", tandem.Fields{"hub": d.pin.Hub, "collection": list, "participant": me.Participant, "at": admission,
		"signer": me.Certificate, "auth": me.Auth})
	if err := signControl(n, "sig", me.Keys.Signing); err != nil {
		return err
	}
	j, err := cjson(n)
	if err != nil {
		return err
	}
	if err := d.publishNotice(ctx, list, epoch, j); err != nil {
		return err
	}
	return d.sendAnswer(ctx, list, admission, 'l', me)
}

// publishNotice puts a kind-3 notice in a collection channel (§10.5).
func (d *Device) publishNotice(ctx context.Context, cid, epoch, plaintext []byte) error {
	c := d.collections[string(cid)]
	if c == nil || c.keys[string(epoch)] == nil {
		return nil
	}
	key := c.keys[string(epoch)]
	tag := tandem.CollectionTag(key, d.pin.Hub, epoch)
	env, err := tandem.Seal(key, d.pin.Hub, tandem.KindRequest, tag, plaintext)
	if err != nil {
		return err
	}
	_, err = d.appendNoted(ctx, tag, env)
	return err
}

// sendAnswer seals the answer receipt to the owner's admitted devices (§10.8, §6.10).
func (d *Device) sendAnswer(ctx context.Context, cid, admission []byte, answer byte, me *Me) error {
	c := d.collections[string(cid)]
	if c == nil || c.owner() == nil {
		return nil
	}
	owner := c.owner()
	a := newControl("answer", tandem.Fields{"hub": d.pin.Hub, "collection": cid, "participant": me.Participant, "admission": admission,
		"answer": answer, "event": random(16), "signer": me.Certificate, "auth": me.Auth})
	if err := signControl(a, "sig", me.Keys.Signing); err != nil {
		return err
	}
	return d.toInboxes(ctx, owner, a)
}

// toInboxes seals a control object to every admitted device of participant, in their inbox.
func (d *Device) toInboxes(ctx context.Context, participant []byte, o *tandem.Control) error {
	body, err := cjson(o)
	if err != nil {
		return err
	}
	inbox := tandem.InboxTag(participant, d.pin.Hub)
	for _, cert := range d.registry.Certificates() {
		if !bytes.Equal(cert.Participant, participant) || !d.registry.Activity(cert.ID).IsValid() {
			continue
		}
		env, err := tandem.SealInbox(d.pin.Hub, participant, cert.Agree, body)
		if err != nil {
			return err
		}
		if _, err := d.appendNoted(ctx, inbox, env); err != nil {
			return err
		}
	}
	return nil
}

// receiveAnswer takes an answer receipt from a member, in this owner's inbox.
func (d *Device) receiveAnswer(j tandem.JSON) (outcome, error) {
	me := d.me
	a, err := tandem.ParseControl(j, "answer")
	if me == nil || err != nil || !bytes.Equal(fid(a, "hub"), d.pin.Hub) {
		return ignored, nil
	}
	cid := fid(a, "collection")
	c := d.collections[string(cid)]
	if c == nil || !bytes.Equal(c.owner(), me.Participant) {
		return ignored, nil
	}
	participant, admission, signer, auth := fid(a, "participant"), fid(a, "admission"), fid(a, "signer"), fid(a, "auth")
	letter, _ := a.Fields["answer"].(byte)
	switch d.registry.Authorizes(auth, signer).State {
	case Pending:
		return waiting, nil
	case Invalid:
		return ignored, nil
	}
	cert := d.registry.Certificate(signer)
	if cert == nil || !bytes.Equal(cert.Participant, participant) || !cverify(a, "sig", cert.Sign) {
		return ignored, nil
	}
	name := "ans." + hexs(cid) + "." + hexs(participant) + "." + hexs(admission)
	letters, _ := d.store.Value(name)
	if bytes.IndexByte(letters, letter) >= 0 {
		return ignored, nil
	}
	letters = append(bytes.Clone(letters), letter)
	slices.Sort(letters)
	return applied, d.store.SetValue(name, letters)
}

// receiveLeave takes a leave notice in a collection channel (§11.5): it counts if at is a
// statement of the chain that lists the member, and every statement from at to the head lists them too.
func (d *Device) receiveLeave(j tandem.JSON, cid []byte) (outcome, error) {
	me, c := d.me, d.collections[string(cid)]
	l, err := tandem.ParseControl(j, "leave")
	if me == nil || c == nil || err != nil || !bytes.Equal(c.owner(), me.Participant) || !bytes.Equal(fid(l, "hub"), d.pin.Hub) ||
		!bytes.Equal(fid(l, "collection"), cid) {
		return ignored, nil
	}
	participant, at, signer, auth := fid(l, "participant"), fid(l, "at"), fid(l, "signer"), fid(l, "auth")
	if _, ok := c.statements[string(at)]; !ok {
		return waiting, nil
	}
	switch d.registry.Authorizes(auth, signer).State {
	case Pending:
		return waiting, nil
	case Invalid:
		return ignored, nil
	}
	cert := d.registry.Certificate(signer)
	if cert == nil || !bytes.Equal(cert.Participant, participant) || !cverify(l, "sig", cert.Sign) {
		return ignored, nil
	}
	if err := d.store.SetValue("left."+hexs(cid)+"."+hexs(participant), at); err != nil {
		return ignored, err
	}
	d.changed(Event{Kind: EventMembers, List: cid})
	return applied, nil
}

// MemberState is one listed member's answer.
type MemberState struct {
	Participant []byte
	Status      MemberStatus
}

// MemberStatus is each listed member's answer to their current admission (§11.2), on the
// owner's devices: "invitation pending", "Alice left House".
func (d *Device) MemberStatus(list []byte) []MemberState {
	defer d.read()()
	me, c := d.me, d.collections[string(list)]
	if me == nil || c == nil || !bytes.Equal(c.owner(), me.Participant) {
		return nil
	}
	head := c.head()
	s, ok := c.headStatement()
	if !ok {
		return nil
	}
	var out []MemberState
	for _, m := range s.Members {
		if bytes.Equal(m.UID, me.Participant) {
			continue
		}
		admission := admissionOf(m.UID, c, head)
		status := StatusPending
		if admission != nil {
			letters, _ := d.store.Value("ans." + hexs(list) + "." + hexs(m.UID) + "." + hexs(admission))
			// A leave notice counts only while every statement from its at to the head lists the member.
			if at, ok := d.store.Value("left." + hexs(list) + "." + hexs(m.UID)); ok && bytes.Equal(at, admission) {
				status = StatusLeft
			} else if bytes.IndexByte(letters, 'l') >= 0 {
				status = StatusLeft
			} else if bytes.IndexByte(letters, 'n') >= 0 {
				status = StatusDeclined
			} else if bytes.IndexByte(letters, 'a') >= 0 {
				status = StatusAccepted
			}
		}
		out = append(out, MemberState{m.UID, status})
	}
	return out
}

// Delete deletes a list (§11.9): a final statement no successor follows. Devices stop showing it.
func (d *Device) Delete(ctx context.Context, list []byte) error {
	defer d.begin()()
	_, _, _, err := d.sameEpoch(ctx, list, func(s *Statement) { s.Deleted = true })
	return err
}

// IsDeleted reports whether the owner deleted a list.
func (d *Device) IsDeleted(list []byte) bool {
	defer d.read()()
	c := d.collections[string(list)]
	if c == nil {
		return false
	}
	s, ok := c.headStatement()
	return ok && s.Deleted
}
