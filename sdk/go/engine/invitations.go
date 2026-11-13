package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strconv"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// InvitationLink is what a hub invitation link carries (§10.8, §13.1): connection details, the
// pins, the directory key, the bearer secret and the capability. Sensitive: never log it or
// put it in the directory.
type InvitationLink struct {
	Hub, Genesis, DirectoryKey []byte
	Account                    MailboxAccount
	Secret                     []byte
	Invitation                 *tandem.Control
}

func (l *InvitationLink) JSON() ([]byte, error) {
	inv, err := cjson(l.Invitation)
	if err != nil {
		return nil, err
	}
	return tandem.NewJSONObject().Int("v", 2).B64u("hub", l.Hub).B64u("genesis", l.Genesis).B64u("hk", l.DirectoryKey).
		Raw("mail", l.Account.json()).B64u("s", l.Secret).Raw("invitation", inv).Bytes(), nil
}

func ParseInvitationLink(data []byte) (*InvitationLink, error) {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	v, _ := j.Integer("v")
	mail, ok1 := j.Get("mail")
	inv, ok2 := j.Get("invitation")
	if v != 2 || !ok1 || !ok2 {
		return nil, fail("invitation: not a v2 link")
	}
	l := &InvitationLink{}
	if l.Account, err = parseAccount(mail); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		into *[]byte
	}{{"hub", &l.Hub}, {"genesis", &l.Genesis}, {"hk", &l.DirectoryKey}, {"s", &l.Secret}} {
		if *f.into, err = j.B64u(f.name, 32); err != nil {
			return nil, err
		}
	}
	if l.Invitation, err = tandem.ParseControl(inv, "invitation"); err != nil {
		return nil, err
	}
	return l, nil
}

func nowMillis() uint64 { return uint64(time.Now().UnixMilli()) }

func managerFor(ms, hub []byte, what string) (*tandem.DeviceKeys, error) {
	m, err := tandem.ManagerKeys(ms)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(publicSign(m), hub) {
		return nil, fail("%s: another hub's key", what)
	}
	return m, nil
}

// Invite creates a new-member invitation with the unlocked management seed ms. It publishes
// the capability, records it privately in home, and returns the link to hand over. validFor
// zero is the default of seven days.
func (d *Device) Invite(ctx context.Context, label string, account MailboxAccount, ms []byte, validFor time.Duration) (*InvitationLink, error) {
	defer d.begin()()
	manager, err := managerFor(ms, d.pin.Hub, "invite")
	if err != nil {
		return nil, err
	}
	if validFor == 0 {
		validFor = 7 * 24 * time.Hour
	}
	secret, inviteID := random(32), random(16)
	key := tandem.InviteKey(secret, d.pin.Hub, inviteID)
	expires := max(0, time.Now().Add(validFor).UnixMilli())
	capability := newControl("invitation", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "key": []byte(key.Public().(ed25519.PublicKey)),
		"invite": inviteID, "expires": uint64(expires), "label": label})
	if err := signControl(capability, "sig", manager.Signing); err != nil {
		return nil, err
	}
	j, err := cjson(capability)
	if err != nil {
		return nil, err
	}
	if err := d.publishDirectory(ctx, j); err != nil {
		return nil, err
	}
	if _, err := d.registry.Add(capability); err != nil {
		return nil, err
	}
	if _, err := d.store.PutObject(capability); err != nil {
		return nil, err
	}
	record := "hubinvite:" + tandem.B64u(inviteID)
	pw := []rune(account.Password)
	for _, f := range []NamedValue{{"label", []byte(label)}, {"capability", oid(capability)},
		{"made", []byte(strconv.FormatUint(nowMillis(), 10))}, {"hint", []byte(string(pw[max(0, len(pw)-4):]))}} {
		if err := d.setHome(ctx, record, f.Name, f.Value); err != nil {
			return nil, err
		}
	}
	return &InvitationLink{Hub: d.pin.Hub, Genesis: d.pin.Genesis, DirectoryKey: d.directoryKey, Account: account, Secret: secret,
		Invitation: capability}, nil
}

func (d *Device) publishDirectory(ctx context.Context, messages ...[]byte) error {
	for _, pt := range messages {
		env, err := tandem.Seal(d.directoryChannelKey(), d.pin.Hub, tandem.KindRequest, d.directoryTag(), pt)
		if err != nil {
			return err
		}
		if _, err := d.appendNoted(ctx, d.directoryTag(), env); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) publishObjects(ctx context.Context, objs ...*tandem.Control) error {
	for _, o := range objs {
		j, err := cjson(o)
		if err != nil {
			return err
		}
		if err := d.publishDirectory(ctx, j); err != nil {
			return err
		}
	}
	return nil
}

// Join joins a hub through an invitation: it finds and pins the hub's genesis, admits this
// keychain under the capability, and starts a home. It needs no hub password.
func Join(ctx context.Context, link *InvitationLink, keys *tandem.DeviceKeys, label string, store *Store, hub tandem.PushStore) (*Device, error) {
	inv := link.Invitation
	expires, _ := fint(inv, "expires")
	if !bytes.Equal(fid(inv, "hub"), link.Hub) || !bytes.Equal(fid(inv, "genesis"), link.Genesis) || !cverify(inv, "sig", link.Hub) ||
		expires <= nowMillis() {
		return nil, fail("invitation: not valid for this hub, or expired")
	}
	inviteKey := tandem.InviteKey(link.Secret, link.Hub, fid(inv, "invite"))
	if !bytes.Equal(inviteKey.Public().(ed25519.PublicKey), fid(inv, "key")) {
		return nil, fail("invitation: the secret doesn't match")
	}
	genesis, err := findGenesis(ctx, link.Hub, link.Genesis, link.DirectoryKey, hub)
	if err != nil {
		return nil, err
	}
	d, err := Open(genesis, link.DirectoryKey, keys, store, hub)
	if err != nil {
		return nil, err
	}
	end := d.begin()
	defer end()
	if _, err := d.sync(ctx); err != nil {
		return nil, err
	}
	if d.receipt(tandem.ReceiptInviteCancelled, oid(inv)) {
		return nil, fail("invitation: the admin cancelled it")
	}
	if err := d.admit(ctx, inv, inviteKey, label); err != nil {
		return nil, err
	}
	return d, nil
}

// admit registers, requests admission with the invite key, certifies it with the same key
// (§10.8), and starts a home.
func (d *Device) admit(ctx context.Context, inv *tandem.Control, inviteKey ed25519.PrivateKey, label string) error {
	keys := d.keys
	if keys == nil || d.me != nil {
		return fail("invitation: this device is already on the hub")
	}
	enrollment := random(16)
	sign, agree := publicSign(keys), publicAgree(keys)
	participant, err := tandem.RegistrationID(d.pin.Hub, sign, agree, enrollment)
	if err != nil {
		return err
	}
	request := newControl("request", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "participant": participant,
		"sign": sign, "agree": agree, "label": label, "how": uint64(tandem.HowInvited), "authority": oid(inv), "enrollment": enrollment})
	if err := signControl(request, "self", keys.Signing); err != nil {
		return err
	}
	if err := signControl(request, "approver", inviteKey); err != nil {
		return err
	}
	certificate := newControl("certificate", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "request": oid(request),
		"issued": nonNil(d.registry.RevocationIDs())})
	certificate.Extras["body"] = request
	if err := signControl(certificate, "sig", inviteKey); err != nil {
		return err
	}
	if _, err := d.registry.Add(certificate); err != nil {
		return err
	}
	certID := oid(certificate)
	if !d.registry.CertificateValidity(certID).IsValid() {
		return fail("invitation: not accepted")
	}
	snapshot := d.registry.CurrentSnapshot(participant)
	if _, err := d.registry.Add(snapshot); err != nil {
		return err
	}
	homeKey := random(32)
	epoch, err := HomeEpochStart(d.pin, participant, 0, nil, homeKey, [][]byte{certID}, certID, oid(snapshot), nil, keys)
	if err != nil {
		return err
	}
	epochID := oid(epoch)
	err = d.store.Transaction(func() error {
		for _, o := range []*tandem.Control{certificate, snapshot, epoch} {
			if _, err := d.store.PutObject(o); err != nil {
				return err
			}
		}
		return d.store.SetValue("home.key."+hexs(epochID), homeKey)
	})
	if err != nil {
		return err
	}
	d.homeEpochs[string(epochID)] = epoch
	d.homeKeys[string(epochID)] = homeKey
	if err := d.adopt(participant, certID, oid(snapshot)); err != nil {
		return err
	}
	if err := d.publishObjects(ctx, certificate, snapshot); err != nil {
		return err
	}
	homeTag := tandem.HomeTag(homeKey, d.pin.Hub, participant, epochID)
	ej, err := cjson(epoch)
	if err != nil {
		return err
	}
	start, err := tandem.Seal(homeKey, d.pin.Hub, tandem.KindRequest, homeTag, ej)
	if err != nil {
		return err
	}
	delivery, err := tandem.SealInbox(d.pin.Hub, participant, agree, homeKeyNotice(epochID, homeKey).JSON())
	if err != nil {
		return err
	}
	for _, e := range []tagged{{homeTag, start}, {tandem.InboxTag(participant, d.pin.Hub), delivery}} {
		if _, err := d.appendNoted(ctx, e.tag, e.body); err != nil {
			return err
		}
	}
	return nil
}

// findGenesis finds a hub's genesis in its directory; the link pins its ID.
func findGenesis(ctx context.Context, hub, genesis, directoryKey []byte, store tandem.PushStore) (*tandem.Control, error) {
	key, tag := tandem.DirectoryKey(directoryKey, hub), tandem.DirectoryTag(directoryKey, hub)
	batch, err := store.Changes(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, m := range batch.Added {
		if m.Channel != tag {
			continue
		}
		_, p, err := tandem.Open(key, hub, tag, m.Body)
		if err != nil {
			continue
		}
		if o, err := tandem.FromJSON(p, "genesis"); err == nil && bytes.Equal(oid(o), genesis) {
			return o, nil
		}
	}
	return nil, fail("link: the hub's genesis isn't on this mailbox")
}
