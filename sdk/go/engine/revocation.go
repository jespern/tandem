package engine

import (
	"bytes"
	"context"
	"slices"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Removing a device (§6.4): the admin signs a revocation; every surviving device of that
// participant starts a new home epoch without it (§10.8). List owners rotate their lists (§11.6).

// Revoke revokes an admitted keychain with the unlocked management seed (§6.4 step 1). Every
// other admitted keychain of that participant is retained; the target's keys are retired for good.
func (d *Device) Revoke(ctx context.Context, certificate, ms []byte) error {
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	manager, err := managerFor(ms, d.pin.Hub, "revoke")
	if err != nil {
		return err
	}
	t := d.registry.Certificate(certificate)
	if t == nil || !d.registry.CertificateValidity(certificate).IsValid() {
		return fail("revoke: no valid certificate with that ID")
	}
	var retained [][]byte
	for _, c := range d.registry.Certificates() {
		if bytes.Equal(c.Participant, t.Participant) && !bytes.Equal(c.ID, certificate) && !c.sameKeys(t) && d.registry.Activity(c.ID).IsValid() {
			retained = append(retained, c.ID)
		}
	}
	r := newControl("revocation", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "target": certificate, "retained": nonNil(sortIDs(retained))})
	if err := signControl(r, "sig", manager.Signing); err != nil {
		return err
	}
	if _, err := d.registry.Add(r); err != nil {
		return err
	}
	snapshot := d.registry.CurrentSnapshot(t.Participant)
	if _, err := d.registry.Add(snapshot); err != nil {
		return err
	}
	// Journalled with its exact bytes before it leaves: a revocation this device shows must reach
	// the hub, so an interrupted one is sent by the next sync (§6.4 step 1, §6.9).
	var out []tagged
	for _, o := range []*tandem.Control{r, snapshot} {
		j, err := cjson(o)
		if err != nil {
			return err
		}
		env, err := tandem.Seal(d.directoryChannelKey(), d.pin.Hub, tandem.KindRequest, d.directoryTag(), j)
		if err != nil {
			return err
		}
		out = append(out, tagged{d.directoryTag(), env})
	}
	step := "revoke." + hexs(oid(r))
	err = d.store.Transaction(func() error {
		if err := d.store.SetJournal(step, pack(out)); err != nil {
			return err
		}
		for _, o := range []*tandem.Control{r, snapshot} {
			if _, err := d.store.PutObject(o); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := d.export(ctx, out, step); err != nil {
		return err
	}
	return d.followUp(ctx)
}

// IsRevoked reports whether this device's own keychain has been revoked. It then stops
// writing and receives no new keys.
func (d *Device) IsRevoked() bool {
	defer d.read()()
	return d.isRevoked()
}

func (d *Device) isRevoked() bool {
	if d.me == nil || !d.registry.Activity(d.me.Certificate).IsInvalid() {
		return false
	}
	return d.registry.CertificateValidity(d.me.Certificate).IsValid()
}

// refreshAuth acts under the current snapshot of this participant, publishing it if the
// directory lacks it, so writes extend any floor a later epoch pins (§6.9, §8.3).
func (d *Device) refreshAuth(ctx context.Context) error {
	me := d.me
	if me == nil {
		return nil
	}
	current := d.registry.CurrentSnapshot(me.Participant)
	id := oid(current)
	if bytes.Equal(id, me.Auth) {
		return nil
	}
	if _, err := d.registry.Add(current); err != nil {
		return err
	}
	if !d.registry.Authorizes(id, me.Certificate).IsValid() {
		return nil
	}
	if _, err := d.publishedSnapshot(ctx, me.Participant); err != nil {
		return err
	}
	return d.adopt(me.Participant, me.Certificate, id)
}

// revocationsOfMine is the valid revocations of this participant's keychains.
func (d *Device) revocationsOfMine(me *Me) [][]byte {
	var out [][]byte
	for _, id := range d.registry.RevocationIDs() {
		rv := d.registry.revs[string(id)]
		if t := d.registry.Certificate(rv.target); t != nil && bytes.Equal(t.Participant, me.Participant) && d.registry.RevocationValidity(id).IsValid() {
			out = append(out, id)
		}
	}
	return out
}

// homeNeedsRotation: a current home head whose policy predates a revocation of this
// participant's keychains (§10.8).
func (d *Device) homeNeedsRotation(me *Me) bool {
	mine := d.revocationsOfMine(me)
	if len(mine) == 0 {
		return false
	}
	for _, h := range d.homeHeads() {
		s := d.registry.Snapshot(fid(d.homeEpochs[string(h)], "auth"))
		if s == nil {
			continue
		}
		for _, r := range mine {
			if !containsID(s.Revocations, r) {
				return true
			}
		}
	}
	return false
}

// RotateHome starts a new home epoch after every current head, sealed only to this
// participant's admitted keychains, carrying the home's records through a fixed inventory. It
// joins concurrent heads. Journalled before anything leaves.
func (d *Device) RotateHome(ctx context.Context) error {
	defer d.begin()()
	return d.rotateHome(ctx)
}

func (d *Device) rotateHome(ctx context.Context) error {
	if err := d.refreshAuth(ctx); err != nil {
		return err
	}
	me, hs := d.me, d.homeHeads()
	if me == nil || len(hs) == 0 || !d.registry.Activity(me.Certificate).IsValid() {
		return fail("home: nothing to rotate, or this keychain is revoked")
	}
	var n uint64
	for _, h := range hs {
		n = max(n, d.epochNumber(h))
	}
	n++
	key := random(32)
	var recipients []*Certificate
	for _, c := range d.registry.Certificates() {
		if bytes.Equal(c.Participant, me.Participant) && d.registry.Activity(c.ID).IsValid() {
			recipients = append(recipients, c)
		}
	}
	entries := map[string]map[string]bool{}
	add := func(r string, id []byte) {
		if entries[r] == nil {
			entries[r] = map[string]bool{}
		}
		entries[r][string(id)] = true
	}
	type carried struct {
		record []byte
		writes []*tandem.Write
	}
	var carry []carried
	for _, rid := range sortedKeys(d.home) {
		r := d.home[rid]
		es := r.Entries()
		if len(es) == 0 {
			continue
		}
		for _, e := range es {
			add(rid, e.ID)
		}
		carry = append(carry, carried{[]byte(rid), keptWrites(r)})
	}
	// Nothing listed is dropped unseen (§8.3): what a head's inventory lists and this device doesn't hold goes on.
	for _, h := range hs {
		for rid, ids := range d.inventories[string(h)] {
			for _, id := range ids {
				if !d.store.HasWrite(id) {
					add(rid, id)
				}
			}
		}
	}
	inv := inventoryOf(entries)
	var recipientIDs [][]byte
	for _, c := range recipients {
		recipientIDs = append(recipientIDs, c.ID)
	}
	start, err := HomeEpochStart(d.pin, me.Participant, n, hs, key, recipientIDs, me.Certificate, me.Auth, inv, me.Keys)
	if err != nil {
		return err
	}
	epoch := oid(start)
	tag := tandem.HomeTag(key, d.pin.Hub, me.Participant, epoch)
	inbox := tandem.InboxTag(me.Participant, d.pin.Hub)
	var out []tagged
	for _, c := range recipients {
		env, err := tandem.SealInbox(d.pin.Hub, me.Participant, c.Agree, homeKeyNotice(epoch, key).JSON())
		if err != nil {
			return err
		}
		out = append(out, tagged{inbox, env})
	}
	if bytes.Equal(me.Participant, d.pin.Creator) {
		box, err := tandem.SealHomeRecovery(d.pin.Agree, d.pin.Hub, epoch, key)
		if err != nil {
			return err
		}
		env, err := tandem.Seal(d.directoryChannelKey(), d.pin.Hub, tandem.KindRequest, d.directoryTag(),
			notice("home-recovery", nf("epoch", epoch), nf("bytes", box)).JSON())
		if err != nil {
			return err
		}
		out = append(out, tagged{d.directoryTag(), env})
	}
	seal := func(kind byte, pt []byte) error {
		env, err := tandem.Seal(key, d.pin.Hub, kind, tag, pt)
		if err == nil {
			out = append(out, tagged{tag, env})
		}
		return err
	}
	// The new channel: every earlier start (so a device paired later can follow the chain), the
	// inventory, the carried writes with their original signatures, and the start itself last.
	earlier := sortedKeys(d.homeEpochs)
	slices.SortStableFunc(earlier, func(a, b string) int {
		return int(d.epochNumber([]byte(a))) - int(d.epochNumber([]byte(b)))
	})
	for _, e := range earlier {
		j, err := cjson(d.homeEpochs[e])
		if err != nil {
			return err
		}
		if err := seal(tandem.KindRequest, j); err != nil {
			return err
		}
	}
	frags, err := fragments(inv, epoch)
	if err != nil {
		return err
	}
	for _, f := range frags {
		if err := seal(tandem.KindRecord, f); err != nil {
			return err
		}
	}
	for _, c := range carry {
		p, err := tandem.RecordPayload(c.record, c.writes)
		if err != nil {
			return err
		}
		if err := seal(tandem.KindRecord, p); err != nil {
			return err
		}
	}
	sj, err := cjson(start)
	if err != nil {
		return err
	}
	if err := seal(tandem.KindRequest, sj); err != nil {
		return err
	}
	// The announcement in every parent's channel.
	for _, parent := range hs {
		k := d.homeKeys[string(parent)]
		if k == nil {
			continue
		}
		t := tandem.HomeTag(k, d.pin.Hub, me.Participant, parent)
		env, err := tandem.Seal(k, d.pin.Hub, tandem.KindRequest, t, sj)
		if err != nil {
			return err
		}
		out = append(out, tagged{t, env})
	}
	step := "home." + hexs(epoch)
	invBytes, err := inv.Bytes()
	if err != nil {
		return err
	}
	err = d.store.Transaction(func() error {
		if err := d.store.SetJournal(step, pack(out)); err != nil {
			return err
		}
		if _, err := d.store.PutObject(start); err != nil {
			return err
		}
		if err := d.store.SetValue("home.key."+hexs(epoch), key); err != nil {
			return err
		}
		return d.store.SetValue("inv."+hexs(epoch), invBytes)
	})
	if err != nil {
		return err
	}
	d.homeEpochs[string(epoch)] = start
	d.homeKeys[string(epoch)] = key
	d.inventories[string(epoch)] = inv
	return d.export(ctx, out, step)
}
