package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"slices"
	"strconv"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Adding a device that doesn't share the keychain (§6.3, §10.8): the member's phone stages a
// delegation, a package and a gate box and shows a link; the new device claims through it;
// an admin approves with the hub password and releases the gate secret to exactly that device.

// PairingLink is what a pairing link carries (§10.8): connection details, the pins, the
// directory key, L, the token and the participant the new device joins. Sensitive.
type PairingLink struct {
	Hub, Genesis, DirectoryKey []byte
	Account                    MailboxAccount
	Secret, Token, Participant []byte
}

func (l *PairingLink) JSON() []byte {
	return tandem.NewJSONObject().Int("v", 2).B64u("hub", l.Hub).B64u("genesis", l.Genesis).B64u("hk", l.DirectoryKey).
		Raw("mail", l.Account.json()).B64u("l", l.Secret).B64u("t", l.Token).B64u("participant", l.Participant).Bytes()
}

func ParsePairingLink(data []byte) (*PairingLink, error) {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	v, _ := j.Integer("v")
	mail, ok := j.Get("mail")
	if v != 2 || !ok {
		return nil, fail("pairing: not a v2 link")
	}
	l := &PairingLink{}
	if l.Account, err = parseAccount(mail); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		n    int
		into *[]byte
	}{{"hub", 32, &l.Hub}, {"genesis", 32, &l.Genesis}, {"hk", 32, &l.DirectoryKey}, {"l", 32, &l.Secret}, {"t", 16, &l.Token},
		{"participant", 32, &l.Participant}} {
		if *f.into, err = j.B64u(f.name, f.n); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// PairingClaim is a claim an admin device holds (§6.3 step 2): the new device's request, the
// delegation it claims under, and the gate box. The admin never sees L.
type PairingClaim struct {
	Request, Delegation *tandem.Control
	Gate                []byte
}

func (c *PairingClaim) ID() []byte          { return oid(c.Request) }
func (c *PairingClaim) Participant() []byte { return fid(c.Request, "participant") }

// Label is the label the new device gave itself.
func (c *PairingClaim) Label() string { return ftext(c.Request, "label") }

// Source is the certificate of the member's device that made the link.
func (c *PairingClaim) Source() []byte { return fid(c.Delegation, "source") }

func (c *PairingClaim) Expires() time.Time {
	ms, _ := fint(c.Delegation, "expires")
	return time.UnixMilli(int64(ms))
}

func (c *PairingClaim) delegationID() []byte { return oid(c.Delegation) }
func (c *PairingClaim) token() []byte        { return fid(c.Delegation, "token") }

func (c *PairingClaim) json() ([]byte, error) {
	r, err := cjson(c.Request)
	if err != nil {
		return nil, err
	}
	dl, err := cjson(c.Delegation)
	if err != nil {
		return nil, err
	}
	return tandem.NewJSONObject().Text("type", "pair-claim").Raw("request", r).Raw("delegation", dl).B64u("gate", c.Gate).Bytes(), nil
}

func parseClaim(j tandem.JSON) (*PairingClaim, error) {
	r, ok1 := j.Get("request")
	dl, ok2 := j.Get("delegation")
	if !ok1 || !ok2 {
		return nil, fail("pair-claim")
	}
	req, err := tandem.ParseControl(r, "request")
	if err != nil {
		return nil, err
	}
	del, err := tandem.ParseControl(dl, "delegation")
	if err != nil {
		return nil, err
	}
	gate, err := j.B64u("gate", -1)
	if err != nil {
		return nil, err
	}
	return &PairingClaim{Request: req, Delegation: del, Gate: gate}, nil
}

// pendingPairing is the new device's side of a claim in flight, kept until the admin's release arrives.
type pendingPairing struct {
	secret, token, participant, request, delegation, pkg []byte
}

func (p *pendingPairing) json() []byte {
	return tandem.NewJSONObject().B64u("l", p.secret).B64u("t", p.token).B64u("participant", p.participant).
		B64u("request", p.request).B64u("delegation", p.delegation).B64u("package", p.pkg).Bytes()
}

func parsePendingPairing(data []byte) (*pendingPairing, error) {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	p := &pendingPairing{}
	for _, f := range []struct {
		name string
		n    int
		into *[]byte
	}{{"l", 32, &p.secret}, {"t", 16, &p.token}, {"participant", 32, &p.participant}, {"request", 32, &p.request},
		{"delegation", 32, &p.delegation}, {"package", -1, &p.pkg}} {
		if *f.into, err = j.B64u(f.name, f.n); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// The member's phone (§6.3, "What the phone prepares")

// StartPairing stages a pairing for a device of this participant that doesn't share the
// keychain, and returns the link to show it. The delegation, package and gate box are on the
// hub before the link exists. validFor zero is the default of a day.
func (d *Device) StartPairing(ctx context.Context, label string, account MailboxAccount, validFor time.Duration) (*PairingLink, error) {
	defer d.begin()()
	me, epoch := d.me, d.currentHomeEpoch()
	if me == nil || epoch == nil || d.homeKeys[string(epoch)] == nil {
		return nil, fail("pairing: this device has no home yet")
	}
	if validFor == 0 {
		validFor = 24 * time.Hour
	}
	homeKey := d.homeKeys[string(epoch)]
	token, secret, gateSecret := random(16), random(32), random(32)
	inviteKey := tandem.PairingInviteKey(secret, d.pin.Hub, token)
	contents := tandem.NewJSONObject().B64u("home", epoch).B64u("k", homeKey).Bytes()
	pkg, err := tandem.SealPackage(secret, gateSecret, d.pin.Hub, token, contents)
	if err != nil {
		return nil, err
	}
	gate, err := tandem.SealGate(d.pin.Agree, d.pin.Hub, token, me.Participant, gateSecret)
	if err != nil {
		return nil, err
	}
	expires := uint64(max(0, time.Now().Add(validFor).UnixMilli()))
	delegation := newControl("delegation", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "participant": me.Participant,
		"source": me.Certificate, "key": []byte(inviteKey.Public().(ed25519.PublicKey)), "token": token, "expires": expires,
		"package": tandem.H(pkg), "gate": tandem.H(gate)})
	if err := signControl(delegation, "sig", me.Keys.Signing); err != nil {
		return nil, err
	}
	if _, err := d.registry.Add(delegation); err != nil {
		return nil, err
	}
	if _, err := d.store.PutObject(delegation); err != nil {
		return nil, err
	}
	key, tag := tandem.PairingKey(secret, d.pin.Hub, token), tandem.PairingTag(secret, d.pin.Hub, token)
	dj, err := cjson(delegation)
	if err != nil {
		return nil, err
	}
	for _, pt := range [][]byte{dj, notice("package", nf("bytes", pkg)).JSON(), notice("gate", nf("bytes", gate)).JSON()} {
		env, err := tandem.Seal(key, d.pin.Hub, tandem.KindRequest, tag, pt)
		if err != nil {
			return nil, err
		}
		if _, err := d.appendNoted(ctx, tag, env); err != nil {
			return nil, err
		}
	}
	record := "pairing:" + tandem.B64u(token)
	for _, f := range []NamedValue{{"delegation", oid(delegation)}, {"label", []byte(label)}, {"expires", []byte(strconv.FormatUint(expires, 10))}} {
		if err := d.setHome(ctx, record, f.Name, f.Value); err != nil {
			return nil, err
		}
	}
	return &PairingLink{Hub: d.pin.Hub, Genesis: d.pin.Genesis, DirectoryKey: d.directoryKey, Account: account, Secret: secret,
		Token: token, Participant: me.Participant}, nil
}

// CancelPairing cancels a pairing this participant started (§6.3): honest admins then refuse
// its claims. A device already approved through it stays until it is revoked.
func (d *Device) CancelPairing(ctx context.Context, token []byte) error {
	defer d.begin()()
	record := "pairing:" + tandem.B64u(token)
	delegation, ok := d.homeValue(record, "delegation")
	if d.me == nil || !ok {
		return fail("pairing: no such pairing")
	}
	receipt, err := d.publishReceipt(ctx, tandem.ReceiptPairingCancelled, delegation, d.me)
	if err != nil {
		return err
	}
	return d.addHome(ctx, record, "outcome", receipt)
}

// The new device (§6.3 step 1)

// Pair claims a device enrollment through a pairing link. It returns a device that waits for
// the admin (AwaitingApproval); syncing it later picks up the certificate and the release.
func Pair(ctx context.Context, link *PairingLink, keys *tandem.DeviceKeys, label string, store *Store, hub tandem.PushStore) (*Device, error) {
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
	if err := d.claim(ctx, link, label); err != nil {
		return nil, err
	}
	return d, nil
}

// AwaitingApproval reports whether this device has claimed an enrollment and waits for the admin's release.
func (d *Device) AwaitingApproval() bool {
	defer d.read()()
	return d.pairing != nil
}

func (d *Device) claim(ctx context.Context, link *PairingLink, label string) error {
	keys := d.keys
	if keys == nil || d.me != nil || d.pairing != nil {
		return fail("pairing: this device is already on the hub")
	}
	inviteKey := tandem.PairingInviteKey(link.Secret, d.pin.Hub, link.Token)
	channelKey, tag := tandem.PairingKey(link.Secret, d.pin.Hub, link.Token), tandem.PairingTag(link.Secret, d.pin.Hub, link.Token)
	var delegation *tandem.Control
	var pkg, gate []byte
	held, err := d.store.Held()
	if err != nil {
		return err
	}
	for _, m := range held {
		if m.Channel != tag {
			continue
		}
		kind, p, err := tandem.Open(channelKey, d.pin.Hub, tag, m.Body)
		if err != nil || kind != tandem.KindRequest {
			continue
		}
		j, err := tandem.ParseJSON(p)
		if err != nil {
			continue
		}
		switch typ, _ := j.Text("type"); typ {
		case "delegation":
			if o, err := tandem.ParseControl(j, "delegation"); err == nil {
				delegation = o
			}
		case "package":
			pkg, _ = j.B64u("bytes", -1)
		case "gate":
			gate, _ = j.B64u("bytes", -1)
		}
	}
	if delegation == nil || pkg == nil || gate == nil {
		return fail("pairing: the link's pairing isn't on the hub")
	}
	expires, _ := fint(delegation, "expires")
	if !bytes.Equal(fid(delegation, "participant"), link.Participant) || !bytes.Equal(fid(delegation, "key"), inviteKey.Public().(ed25519.PublicKey)) ||
		!bytes.Equal(fid(delegation, "token"), link.Token) || !bytes.Equal(fid(delegation, "package"), tandem.H(pkg)) ||
		!bytes.Equal(fid(delegation, "gate"), tandem.H(gate)) || expires <= nowMillis() || !d.sourceValidity(delegation).IsValid() {
		return fail("pairing: the link is expired, or not from a device of that participant")
	}
	if _, err := d.registry.Add(delegation); err != nil {
		return err
	}
	request := newControl("request", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "participant": link.Participant,
		"sign": publicSign(keys), "agree": publicAgree(keys), "label": label, "how": uint64(tandem.HowByPairing),
		"authority": oid(delegation), "enrollment": make([]byte, 16)})
	if err := signControl(request, "self", keys.Signing); err != nil {
		return err
	}
	if err := signControl(request, "approver", inviteKey); err != nil {
		return err
	}
	c := &PairingClaim{Request: request, Delegation: delegation, Gate: gate}
	var admins []*Certificate
	for _, cert := range d.registry.Certificates() {
		if bytes.Equal(cert.Participant, d.pin.Creator) && d.registry.Activity(cert.ID).IsValid() {
			admins = append(admins, cert)
		}
	}
	if len(admins) == 0 {
		return fail("pairing: the hub has no admitted admin device")
	}
	pp := &pendingPairing{secret: link.Secret, token: link.Token, participant: link.Participant, request: oid(request),
		delegation: oid(delegation), pkg: pkg}
	err = d.store.Transaction(func() error {
		if _, err := d.store.PutObject(delegation); err != nil {
			return err
		}
		return d.store.SetValue("pairing.pending", pp.json())
	})
	if err != nil {
		return err
	}
	d.pairing = pp
	body, err := c.json()
	if err != nil {
		return err
	}
	inbox := tandem.InboxTag(d.pin.Creator, d.pin.Hub)
	for _, admin := range admins {
		env, err := tandem.SealInbox(d.pin.Hub, d.pin.Creator, admin.Agree, body)
		if err != nil {
			return err
		}
		if _, err := d.appendNoted(ctx, inbox, env); err != nil {
			return err
		}
	}
	return nil
}

// sourceValidity: the delegation is signed by a device of its participant that is admitted now.
func (d *Device) sourceValidity(delegation *tandem.Control) Validity {
	if !bytes.Equal(fid(delegation, "hub"), d.pin.Hub) || !bytes.Equal(fid(delegation, "genesis"), d.pin.Genesis) {
		return invalid("delegation for another hub")
	}
	source, participant := fid(delegation, "source"), fid(delegation, "participant")
	if v := d.registry.Activity(source); !v.IsValid() {
		return v
	}
	src := d.registry.Certificate(source)
	if src == nil || !bytes.Equal(src.Participant, participant) || !cverify(delegation, "sig", src.Sign) {
		return invalid("delegation not signed by a device of that participant")
	}
	return valid()
}

// The admin (§6.3 steps 2–4, Reject)

func (d *Device) receiveClaim(plaintext []byte, j tandem.JSON) (outcome, error) {
	c, err := parseClaim(j)
	if err != nil || d.claims[string(c.ID())] != nil {
		return ignored, nil
	}
	switch d.claimValidity(c).State {
	case Pending:
		return waiting, nil
	case Invalid:
		return ignored, nil
	}
	d.claims[string(c.ID())] = c
	if _, err := d.registry.Add(c.Delegation); err != nil {
		return ignored, nil
	}
	return applied, d.store.Transaction(func() error {
		if _, err := d.store.PutObject(c.Delegation); err != nil {
			return err
		}
		return d.store.SetValue("claim."+hexs(c.ID()), plaintext)
	})
}

// claimValidity: the claim's exact request is bound to its delegation, signed by the new keys
// and by the link's invite key, and the delegation by an admitted device of the same participant (§6.9).
func (d *Device) claimValidity(c *PairingClaim) Validity {
	r, dl := c.Request, c.Delegation
	how, _ := fint(r, "how")
	if !bytes.Equal(fid(r, "hub"), d.pin.Hub) || !bytes.Equal(fid(r, "genesis"), d.pin.Genesis) || how != tandem.HowByPairing ||
		!bytes.Equal(fid(r, "authority"), c.delegationID()) || !bytes.Equal(fid(r, "participant"), fid(dl, "participant")) ||
		!bytes.Equal(fid(r, "enrollment"), zero16) || !bytes.Equal(fid(dl, "gate"), tandem.H(c.Gate)) ||
		!cverify(r, "self", fid(r, "sign")) || !cverify(r, "approver", fid(dl, "key")) {
		return invalid("claim does not match its delegation")
	}
	return d.sourceValidity(dl)
}

// PendingClaims is the claims waiting for an admin's answer: "New device from Alice: Laptop".
// Cancelled, expired, rejected and approved ones are not listed.
func (d *Device) PendingClaims() []*PairingClaim {
	defer d.read()()
	var out []*PairingClaim
	for _, c := range d.claims {
		if !c.Expires().After(time.Now()) || d.cancelled(c.delegationID()) || d.rejected(c.ID()) {
			continue
		}
		approved := false
		for _, cert := range d.registry.Certificates() {
			approved = approved || bytes.Equal(cert.RequestID, c.ID())
		}
		if !approved {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *PairingClaim) int {
		return bytes.Compare(append(a.token(), a.ID()...), append(b.token(), b.ID()...))
	})
	return out
}

// Approve approves a claim with the unlocked management seed (§6.3 steps 3–4): it certifies
// the exact keys, then releases the gate secret to that device. The certificate and the
// release are journalled before either leaves.
func (d *Device) Approve(ctx context.Context, claim, ms []byte) error {
	defer d.begin()()
	if _, err := d.sync(ctx); err != nil {
		return err
	}
	step := "approve." + hexs(claim)
	var envelopes []tagged
	if j, ok := d.store.Journal(step); ok {
		var err error
		if envelopes, err = unpack(j); err != nil {
			return err
		}
	} else {
		c := d.claims[string(claim)]
		if c == nil || !d.claimValidity(c).IsValid() || !c.Expires().After(time.Now()) || d.cancelled(c.delegationID()) {
			return fail("approve: no valid claim with that ID; it may be cancelled or expired")
		}
		manager, err := managerFor(ms, d.pin.Hub, "approve")
		if err != nil {
			return err
		}
		agree, pkg := fid(c.Request, "agree"), fid(c.Delegation, "package")
		if _, err := d.registry.Add(c.Delegation); err != nil {
			return err
		}
		certificate := newControl("certificate", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "request": claim,
			"issued": nonNil(d.registry.RevocationIDs())})
		certificate.Extras["body"] = c.Request
		if err := signControl(certificate, "sig", manager.Signing); err != nil {
			return err
		}
		if _, err := d.registry.Add(certificate); err != nil {
			return err
		}
		if v := d.registry.CertificateValidity(oid(certificate)); !v.IsValid() {
			return fail("approve: the certificate would not be valid (%v)", v)
		}
		snapshot := d.registry.CurrentSnapshot(c.Participant())
		if _, err := d.registry.Add(snapshot); err != nil {
			return err
		}
		a, err := tandem.OpenGate(manager.Agreement, d.pin.Hub, c.token(), c.Participant(), c.Gate)
		if err != nil {
			return err
		}
		release := notice("pair-release", nf("certificate", oid(certificate)), nf("delegation", c.delegationID()), nf("package", pkg), nf("a", a))
		for _, o := range []*tandem.Control{c.Delegation, certificate, snapshot} {
			j, err := cjson(o)
			if err != nil {
				return err
			}
			env, err := tandem.Seal(d.directoryChannelKey(), d.pin.Hub, tandem.KindRequest, d.directoryTag(), j)
			if err != nil {
				return err
			}
			envelopes = append(envelopes, tagged{d.directoryTag(), env})
		}
		env, err := tandem.SealInbox(d.pin.Hub, c.Participant(), agree, release.JSON())
		if err != nil {
			return err
		}
		envelopes = append(envelopes, tagged{tandem.InboxTag(c.Participant(), d.pin.Hub), env})
		err = d.store.Transaction(func() error {
			if err := d.store.SetJournal(step, pack(envelopes)); err != nil {
				return err
			}
			for _, o := range []*tandem.Control{c.Delegation, certificate, snapshot} {
				if _, err := d.store.PutObject(o); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return d.export(ctx, envelopes, step)
}

// Reject rejects a claim (§6.3, Reject): a receipt signed with MKs. A certificate approved
// elsewhere outweighs it.
func (d *Device) Reject(ctx context.Context, claim, ms []byte) error {
	defer d.begin()()
	if d.claims[string(claim)] == nil {
		return fail("reject: no such claim")
	}
	manager, err := managerFor(ms, d.pin.Hub, "reject")
	if err != nil {
		return err
	}
	r := newControl("receipt", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "outcome": uint64(tandem.ReceiptRejected),
		"subject": claim, "signer": nil})
	if err := signControl(r, "sig", manager.Signing); err != nil {
		return err
	}
	return d.keepReceipt(ctx, r)
}

// finishJournals finishes admissions, approvals, home rekeys and revocations a crash interrupted: lost
// acknowledgements retry the same bytes (§6.3 step 4).
func (d *Device) finishJournals(ctx context.Context) error {
	steps, err := d.store.JournalSteps()
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step != "admit" && !strings.HasPrefix(step, "approve.") && !strings.HasPrefix(step, "home.") && !strings.HasPrefix(step, "revoke.") {
			continue
		}
		j, ok := d.store.Journal(step)
		if !ok {
			continue
		}
		envelopes, err := unpack(j)
		if err != nil {
			return err
		}
		if err := d.export(ctx, envelopes, step); err != nil {
			return err
		}
	}
	return nil
}

// Receipts (§10.8)

func (d *Device) publishReceipt(ctx context.Context, outcome uint64, subject []byte, me *Me) ([]byte, error) {
	r := newControl("receipt", tandem.Fields{"hub": d.pin.Hub, "genesis": d.pin.Genesis, "outcome": outcome, "subject": subject,
		"signer": me.Certificate})
	if err := signControl(r, "sig", me.Keys.Signing); err != nil {
		return nil, err
	}
	return oid(r), d.keepReceipt(ctx, r)
}

func (d *Device) keepReceipt(ctx context.Context, r *tandem.Control) error {
	d.receipts[string(oid(r))] = r
	if _, err := d.store.PutObject(r); err != nil {
		return err
	}
	return d.publishObjects(ctx, r)
}

// receiptHolds: a receipt counts if the right authority signed it: MKs for a rejection or a
// cancelled invitation, a device of the pairing participant for a cancelled or completed pairing.
func (d *Device) receiptHolds(r *tandem.Control) bool {
	outcome, ok := fint(r, "outcome")
	if !bytes.Equal(fid(r, "hub"), d.pin.Hub) || !bytes.Equal(fid(r, "genesis"), d.pin.Genesis) || !ok {
		return false
	}
	signer, subject := fid(r, "signer"), fid(r, "subject")
	switch outcome {
	case tandem.ReceiptRejected, tandem.ReceiptInviteCancelled:
		return signer == nil && cverify(r, "sig", d.pin.Hub)
	case tandem.ReceiptPairingCancelled, tandem.ReceiptPaired:
		cert := d.registry.Certificate(signer)
		if signer == nil || cert == nil {
			return false
		}
		standing := d.registry.Activity(signer)
		if outcome == tandem.ReceiptPaired {
			standing = d.registry.CertificateValidity(signer)
		}
		if !standing.IsValid() || !cverify(r, "sig", cert.Sign) {
			return false
		}
		var participant []byte
		if outcome == tandem.ReceiptPaired {
			if c := d.registry.Certificate(subject); c != nil {
				participant = c.Participant
			}
		} else if dl := d.registry.delegations[string(subject)]; dl != nil {
			participant = dl.participant
		}
		return participant != nil && bytes.Equal(participant, cert.Participant)
	}
	return false
}

func (d *Device) receipt(outcome uint64, subject []byte) bool {
	for _, r := range d.receipts {
		if o, _ := fint(r, "outcome"); o == outcome && bytes.Equal(fid(r, "subject"), subject) && d.receiptHolds(r) {
			return true
		}
	}
	return false
}

func (d *Device) rejected(claim []byte) bool { return d.receipt(tandem.ReceiptRejected, claim) }
func (d *Device) cancelled(dl []byte) bool   { return d.receipt(tandem.ReceiptPairingCancelled, dl) }

// The release (§6.3 step 5)

func (d *Device) receiveRelease(plaintext []byte, me *Me) (outcome, error) {
	p := d.pairing
	if p == nil {
		return ignored, nil
	}
	n, err := ParseNotice(plaintext, "pair-release", "certificate", "delegation", "package", "a")
	if err != nil || !bytes.Equal(n.Get("certificate"), me.Certificate) || !bytes.Equal(n.Get("delegation"), p.delegation) ||
		!bytes.Equal(n.Get("package"), tandem.H(p.pkg)) {
		return ignored, nil
	}
	if c := d.registry.Certificate(me.Certificate); c == nil || !bytes.Equal(c.RequestID, p.request) {
		return ignored, nil
	}
	contents, err := tandem.OpenPackage(p.secret, n.Get("a"), d.pin.Hub, p.token, p.pkg)
	if err != nil {
		return ignored, nil
	}
	j, err := tandem.ParseJSON(contents)
	if err != nil {
		return ignored, nil
	}
	epoch, err1 := j.B64u("home", 32)
	key, err2 := j.B64u("k", 32)
	if err1 != nil || err2 != nil {
		return ignored, nil
	}
	if start := d.homeEpochs[string(epoch)]; start != nil && !bytes.Equal(fid(start, "key"), tandem.Commit(key)) {
		return ignored, nil
	}
	if _, err := d.offerHomeKey(epoch, key); err != nil {
		return ignored, err
	}
	err = d.store.Transaction(func() error {
		if err := d.store.RemoveValue("pairing.pending"); err != nil {
			return err
		}
		return d.store.SetValue("pairing.done."+hexs(p.token), me.Certificate)
	})
	if err != nil {
		return ignored, err
	}
	d.pairing = nil
	return applied, d.moveHome()
}

// followUp runs after a sync: it records finished pairings and hands this home's current keys
// to admitted keychains of this participant that no epoch was sealed to, such as a device
// paired after the package was staged (§6.3), and runs the rest of the after-sync work.
func (d *Device) followUp(ctx context.Context) error {
	if d.isRevoked() {
		if _, done := d.store.Value("revoked"); !done {
			if err := d.store.SetValue("revoked", []byte{1}); err != nil {
				return err
			}
			d.changed(Event{Kind: EventRevoked})
		}
	}
	if d.me == nil || !d.registry.Activity(d.me.Certificate).IsValid() {
		return nil
	}
	if err := d.refreshAuth(ctx); err != nil {
		return err
	}
	if d.homeNeedsRotation(d.me) {
		if err := d.rotateHome(ctx); err != nil {
			return err
		}
	}
	for _, f := range []func(context.Context) error{d.takeGrantedKeys, d.settleAnnouncedNotes, d.settleLateWrites,
		d.keyRequestFollowUp, d.hygiene} {
		if err := f(ctx); err != nil {
			return err
		}
	}
	me := d.me
	done, err := d.store.Values("pairing.done.")
	if err != nil {
		return err
	}
	for _, v := range done {
		token, ok := unhexs(strings.TrimPrefix(v.Name, "pairing.done."))
		if !ok || d.currentHomeEpoch() == nil {
			continue
		}
		receipt, err := d.publishReceipt(ctx, tandem.ReceiptPaired, v.Value, me)
		if err != nil {
			return err
		}
		if err := d.addHome(ctx, "pairing:"+tandem.B64u(token), "outcome", receipt); err != nil {
			return err
		}
		if err := d.store.RemoveValue(v.Name); err != nil {
			return err
		}
	}
	inbox := tandem.InboxTag(me.Participant, d.pin.Hub)
	for _, epoch := range d.homeHeads() {
		start, key := d.homeEpochs[string(epoch)], d.homeKeys[string(epoch)]
		if start == nil || key == nil {
			continue
		}
		recipients := fset(start, "recipients")
		for _, cert := range d.registry.Certificates() {
			if !bytes.Equal(cert.Participant, me.Participant) || bytes.Equal(cert.ID, me.Certificate) || containsID(recipients, cert.ID) ||
				!d.registry.Activity(cert.ID).IsValid() {
				continue
			}
			sent := "home.sent." + hexs(epoch) + "." + hexs(cert.ID)
			if _, ok := d.store.Value(sent); ok {
				continue
			}
			env, err := tandem.SealInbox(d.pin.Hub, me.Participant, cert.Agree, homeKeyNotice(epoch, key).JSON())
			if err != nil {
				return err
			}
			if _, err := d.appendNoted(ctx, inbox, env); err != nil {
				return err
			}
			if err := d.store.SetValue(sent, []byte{1}); err != nil {
				return err
			}
		}
	}
	return nil
}
