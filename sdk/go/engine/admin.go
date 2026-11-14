package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The hub admin's other actions (§6.7, §6.8, §13.1): changing the hub password, cancelling an
// invitation, and the creator's recovery with the hub password.

// MadeInvitation is a hub invitation this participant made, from its home record (§6.2).
type MadeInvitation struct {
	Invite, Capability []byte
	Label              string
	// Made is when it was made, by this device's clock (zero if unknown).
	Made time.Time
	// Hint is the last four characters of the mailbox password it carried, to tell links apart.
	Hint      string
	Cancelled bool
}

// ChangePassword changes the hub password with the unlocked management seed: a new capsule
// names every capsule it supersedes, and once it is on the hub the old ones are destroyed.
// Seconds and 1 GiB. The old password still opens any copy of an old capsule someone kept (§6.8).
func (d *Device) ChangePassword(ctx context.Context, password string, ms []byte) error {
	if _, err := managerFor(ms, d.pin.Hub, "password"); err != nil {
		return err
	}
	// Stretch outside the operation, so reads and syncs go on meanwhile.
	sealed, err := tandem.SealCapsule(password, d.pin.Hub, ms)
	if err != nil {
		return err
	}
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	named := map[string]bool{}
	for k, c := range d.capsules {
		named[k] = true
		for _, s := range fset(c, "supersedes") {
			named[string(s)] = true
		}
	}
	var supersedes [][]byte
	for _, k := range sortedKeys(named) {
		supersedes = append(supersedes, []byte(k))
	}
	manager, _ := tandem.ManagerKeys(ms)
	capsule := newControl("capsule", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "sealed": tandem.H(sealed),
		"supersedes": nonNil(supersedes)})
	capsule.Extras["capsule"] = sealed
	if err := signControl(capsule, "sig", manager.Signing); err != nil {
		return err
	}
	d.capsules[string(oid(capsule))] = capsule
	if _, err := d.store.PutObject(capsule); err != nil {
		return err
	}
	if err := d.publishObjects(ctx, capsule); err != nil {
		return err
	}
	return d.destroyDirectoryObjects(ctx, named)
}

// destroyDirectoryObjects destroys the directory messages that carry these objects.
func (d *Device) destroyDirectoryObjects(ctx context.Context, ids map[string]bool) error {
	ms, err := d.store.Merged(d.directoryTag())
	if err != nil {
		return err
	}
	var doomed []string
	for _, m := range ms {
		cs, err := d.store.Contents(m.ID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(cs, func(c Content) bool { return ids[string(c.Write)] }) {
			doomed = append(doomed, m.ID)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	if err := d.hubDestroy(ctx, doomed); err != nil {
		return err
	}
	return d.store.ForgetMessages(doomed)
}

// Invitations is the hub invitations this participant made, newest first.
func (d *Device) Invitations() []MadeInvitation {
	defer d.read()()
	var out []MadeInvitation
	for _, r := range d.home {
		name, _ := value(r, "_name")
		capability, ok := value(r, "capability")
		if !strings.HasPrefix(string(name), "hubinvite:") || !ok {
			continue
		}
		invite, err := tandem.ParseB64u(string(name[len("hubinvite:"):]))
		if err != nil {
			continue
		}
		m := MadeInvitation{Invite: invite, Capability: capability, Cancelled: d.receipt(tandem.ReceiptInviteCancelled, capability)}
		if v, ok := value(r, "label"); ok {
			m.Label = string(v)
		}
		if v, ok := value(r, "hint"); ok {
			m.Hint = string(v)
		}
		if v, ok := value(r, "made"); ok {
			if ms, err := strconv.ParseInt(string(v), 10, 64); err == nil {
				m.Made = time.UnixMilli(ms)
			}
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b MadeInvitation) int { return b.Made.Compare(a.Made) })
	return out
}

// CancelInvitation cancels an invitation (§10.8, receipt outcome 3): devices then refuse to
// join with it. Anyone already admitted through it stays until revoked.
func (d *Device) CancelInvitation(ctx context.Context, capability, ms []byte) error {
	defer d.begin()()
	manager, err := managerFor(ms, d.pin.Hub, "cancel")
	if err != nil {
		return err
	}
	r := newControl("receipt", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "outcome": uint64(tandem.ReceiptInviteCancelled),
		"subject": capability, "signer": nil})
	if err := signControl(r, "sig", manager.Signing); err != nil {
		return err
	}
	if err := d.keepReceipt(ctx, r); err != nil {
		return err
	}
	d.changed(Event{Kind: EventPeople})
	return nil
}

// RecoverCreator: the creator, with every device and the keychain lost, recovers with the hub
// password on a new device (§6.8): a fresh keychain is admitted to the creator's participant
// and the creator's home opens from the boxes sealed to the management key. Nobody else can be
// recovered this way.
func RecoverCreator(ctx context.Context, genesis *tandem.Control, directoryKey []byte, password string, keys *tandem.DeviceKeys,
	label string, store *Store, hub tandem.PushStore) (*Device, error) {
	d, err := Open(genesis, directoryKey, keys, store, hub)
	if err != nil {
		return nil, err
	}
	if _, err := d.Sync(ctx); err != nil {
		return nil, err
	}
	ms, err := d.Unlock(password)
	if err != nil {
		return nil, err
	}
	end := d.begin()
	defer end()
	if err := d.admitCreator(ctx, ms, label); err != nil {
		return nil, err
	}
	if _, err := d.sync(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Device) admitCreator(ctx context.Context, ms []byte, label string) error {
	if d.me != nil {
		return fail("recover: this device is already on the hub")
	}
	manager, err := managerFor(ms, d.pin.Hub, "recover")
	if err != nil || d.keys == nil {
		return fail("recover: another hub's key")
	}
	request := newControl("request", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "participant": d.pin.Creator,
		"sign": publicSign(d.keys), "agree": publicAgree(d.keys), "label": label, "how": uint64(tandem.HowCreatorRecovery),
		"authority": nil, "enrollment": make([]byte, 16)})
	if err := signControl(request, "self", d.keys.Signing); err != nil {
		return err
	}
	certificate := newControl("certificate", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "request": oid(request),
		"issued": nonNil(d.registry.RevocationIDs())})
	certificate.Extras["body"] = request
	if err := signControl(certificate, "sig", manager.Signing); err != nil {
		return err
	}
	if _, err := d.registry.Add(certificate); err != nil {
		return err
	}
	if !d.registry.CertificateValidity(oid(certificate)).IsValid() {
		return fail("recover: not accepted")
	}
	snapshot := d.registry.CurrentSnapshot(d.pin.Creator)
	if _, err := d.registry.Add(snapshot); err != nil {
		return err
	}
	err = d.store.Transaction(func() error {
		for _, o := range []*tandem.Control{certificate, snapshot} {
			if _, err := d.store.PutObject(o); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := d.publishObjects(ctx, certificate, snapshot); err != nil {
		return err
	}
	if err := d.adopt(d.pin.Creator, oid(certificate), oid(snapshot)); err != nil {
		return err
	}
	// The creator's home keys, from the boxes sealed to the management agreement key.
	for _, n := range d.notices {
		if n.Type != "home-recovery" {
			continue
		}
		key, err := tandem.OpenHomeRecovery(manager.Agreement, d.pin.Hub, n.Get("epoch"), n.Get("bytes"))
		if err != nil {
			continue
		}
		if _, err := d.offerHomeKey(n.Get("epoch"), key); err != nil { // anyone can seal a box to MKa: the epoch's start decides
			return err
		}
	}
	return nil
}
