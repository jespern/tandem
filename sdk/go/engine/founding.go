package engine

import (
	"bytes"
	"crypto/ed25519"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// MailboxAccount is the connection details a device needs for a hub, without authority:
// what an invitation or pairing link carries and the creator keeps (§6.1, §13.1).
type MailboxAccount struct {
	Host     string
	Port     uint16
	User     string
	Password string
}

func (a MailboxAccount) mailbox() tandem.Mailbox {
	return tandem.Mailbox{Host: strings.ToLower(a.Host), Port: a.Port, User: a.User}
}

// json is the mail member of a link (§10.8): {"host","port","user","pass"}.
func (a MailboxAccount) json() []byte {
	return tandem.NewJSONObject().Text("host", a.Host).Int("port", uint64(a.Port)).Text("user", a.User).
		Text("pass", a.Password).Bytes()
}

func parseAccount(j tandem.JSON) (MailboxAccount, error) {
	p, ok := j.Get("port")
	if !ok || p.Kind != tandem.KindInt || p.Int < 1 || p.Int > 65535 {
		return MailboxAccount{}, fail("link: mail port")
	}
	host, e1 := j.Text("host")
	user, e2 := j.Text("user")
	pass, e3 := j.Text("pass")
	if e1 != nil || e2 != nil || e3 != nil {
		return MailboxAccount{}, fail("link: mail")
	}
	return MailboxAccount{Host: host, Port: uint16(p.Int), User: user, Password: pass}, nil
}

// Notice is a protocol message that is not a signed control object (§10.8): the key and
// recovery boxes, the pairing package and gate, the pairing release.
type Notice struct {
	Type   string
	Fields []NoticeField // b64u in JSON, in this order
}

type NoticeField struct {
	Name  string
	Value []byte
}

func notice(typ string, fields ...NoticeField) Notice { return Notice{Type: typ, Fields: fields} }

func nf(name string, value []byte) NoticeField { return NoticeField{name, value} }

func (n Notice) JSON() []byte {
	o := tandem.NewJSONObject().Text("type", n.Type)
	for _, f := range n.Fields {
		o.B64u(f.Name, f.Value)
	}
	return o.Bytes()
}

// Get returns a field's value, or nil.
func (n Notice) Get(name string) []byte {
	for _, f := range n.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return nil
}

func (n Notice) equal(o Notice) bool {
	if n.Type != o.Type || len(n.Fields) != len(o.Fields) {
		return false
	}
	for i := range n.Fields {
		if n.Fields[i].Name != o.Fields[i].Name || !bytes.Equal(n.Fields[i].Value, o.Fields[i].Value) {
			return false
		}
	}
	return true
}

// ParseNotice reads one notice of typ, with the named b64u members.
func ParseNotice(data []byte, typ string, names ...string) (Notice, error) {
	j, err := tandem.ParseJSON(data)
	if err != nil {
		return Notice{}, err
	}
	if t, err := j.Text("type"); err != nil || t != typ {
		return Notice{}, fail("not a %s notice", typ)
	}
	n := Notice{Type: typ}
	for _, name := range names {
		b, err := j.B64u(name, -1)
		if err != nil {
			return Notice{}, fail("%s: %s", typ, name)
		}
		n.Fields = append(n.Fields, nf(name, b))
	}
	return n, nil
}

// FoundingSecrets is the random material a hub is founded from. Separate so tests can fix it.
type FoundingSecrets struct {
	ManagementSeed, DirectoryKey, Enrollment, HomeKey []byte
}

func RandomFoundingSecrets() FoundingSecrets {
	return FoundingSecrets{ManagementSeed: random(32), DirectoryKey: random(32), Enrollment: random(16), HomeKey: random(32)}
}

// Founding is everything a new hub starts with (§6.1), except the capsule, which needs the
// hub password (§6.7).
type Founding struct {
	Pin                                       HubPin
	Participant                               []byte
	Genesis, Certificate, Snapshot, HomeEpoch *tandem.Control
	// HomeKeyDelivery is the first home key, sealed to the creator's own agreement key (for its inbox).
	HomeKeyDelivery []byte
	// HomeRecovery is the same key sealed to MKa, for the directory (§6.8).
	HomeRecovery                          Notice
	HomeKey, DirectoryKey, ManagementSeed []byte
	// Capsule is the admin capsule (§6.7), once made with NewCapsule.
	Capsule *tandem.Control
}

// DirectoryMessages is the directory-channel messages in publication order: each one's dependencies first.
func (f *Founding) DirectoryMessages() ([][]byte, error) {
	objs := []*tandem.Control{f.Genesis}
	if f.Capsule != nil {
		objs = append(objs, f.Capsule)
	}
	objs = append(objs, f.Certificate, f.Snapshot)
	var out [][]byte
	for _, o := range objs {
		j, err := cjson(o)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return append(out, f.HomeRecovery.JSON()), nil
}

// Found founds a hub (§6.1) on account for the creator's keychain keys.
func Found(account MailboxAccount, keys *tandem.DeviceKeys, label string, secrets *FoundingSecrets) (*Founding, error) {
	s := RandomFoundingSecrets()
	if secrets != nil {
		s = *secrets
	}
	manager, err := tandem.ManagerKeys(s.ManagementSeed)
	if err != nil {
		return nil, err
	}
	hub := manager.Signing.Public().(ed25519.PublicKey)
	sign, agree := publicSign(keys), publicAgree(keys)
	participant, err := tandem.RegistrationID(hub, sign, agree, s.Enrollment)
	if err != nil {
		return nil, err
	}
	genesis := newControl("genesis", tandem.Fields{"hub": []byte(hub), "agree": publicAgree(manager),
		"hubkey": tandem.Commit(s.DirectoryKey), "creator": participant, "mailboxes": []tandem.Mailbox{account.mailbox()}})
	if err := signControl(genesis, "sig", manager.Signing); err != nil {
		return nil, err
	}
	pin, err := PinGenesis(genesis)
	if err != nil {
		return nil, err
	}
	request := newControl("request", tandem.Fields{"hub": []byte(hub), "genesis": pin.Genesis, "participant": participant,
		"sign": sign, "agree": agree, "label": label, "how": uint64(tandem.HowFirst), "authority": nil, "enrollment": s.Enrollment})
	if err := signControl(request, "self", keys.Signing); err != nil {
		return nil, err
	}
	certificate := newControl("certificate", tandem.Fields{"hub": []byte(hub), "genesis": pin.Genesis, "request": oid(request), "issued": [][]byte{}})
	certificate.Extras["body"] = request
	if err := signControl(certificate, "sig", manager.Signing); err != nil {
		return nil, err
	}
	certID := oid(certificate)
	snapshot := newControl("snapshot", tandem.Fields{"hub": []byte(hub), "genesis": pin.Genesis, "participant": participant,
		"certs": [][]byte{certID}, "revocations": [][]byte{}})
	homeEpoch, err := HomeEpochStart(pin, participant, 0, nil, s.HomeKey, [][]byte{certID}, certID, oid(snapshot), nil, keys)
	if err != nil {
		return nil, err
	}
	epochID := oid(homeEpoch)
	delivery, err := tandem.SealInbox(hub, participant, agree, homeKeyNotice(epochID, s.HomeKey).JSON())
	if err != nil {
		return nil, err
	}
	box, err := tandem.SealHomeRecovery(publicAgree(manager), hub, epochID, s.HomeKey)
	if err != nil {
		return nil, err
	}
	return &Founding{Pin: pin, Participant: participant, Genesis: genesis, Certificate: certificate, Snapshot: snapshot,
		HomeEpoch: homeEpoch, HomeKeyDelivery: delivery, HomeRecovery: notice("home-recovery", nf("epoch", epochID), nf("bytes", box)),
		HomeKey: s.HomeKey, DirectoryKey: s.DirectoryKey, ManagementSeed: s.ManagementSeed}, nil
}

// NewCapsule seals MS under the creator's hub password and signs the capsule (§6.7). Takes
// about a second and 1 GiB; supersedes names the capsules a password change replaces.
func NewCapsule(password string, pin HubPin, managementSeed []byte, supersedes [][]byte) (*tandem.Control, error) {
	sealed, err := tandem.SealCapsule(password, pin.Hub, managementSeed)
	if err != nil {
		return nil, err
	}
	manager, err := tandem.ManagerKeys(managementSeed)
	if err != nil {
		return nil, err
	}
	c := newControl("capsule", tandem.Fields{"hub": pin.Hub, "genesis": pin.Genesis, "sealed": tandem.H(sealed),
		"supersedes": nonNil(sortIDs(supersedes))})
	c.Extras["capsule"] = sealed
	return c, signControl(c, "sig", manager.Signing)
}

// HomeEpochStart is a home-epoch start (§10.8), signed by one of the participant's admitted keychains.
func HomeEpochStart(pin HubPin, participant []byte, n uint64, parents [][]byte, homeKey []byte, recipients [][]byte,
	signer, auth []byte, inventory tandem.Inventory, keys *tandem.DeviceKeys) (*tandem.Control, error) {
	if inventory == nil {
		inventory = tandem.Inventory{}
	}
	inv, err := inventory.Bytes()
	if err != nil {
		return nil, err
	}
	e := newControl("home-epoch", tandem.Fields{"hub": pin.Hub, "genesis": pin.Genesis, "participant": participant, "n": n,
		"parents": nonNil(sortIDs(parents)), "key": tandem.Commit(homeKey), "recipients": nonNil(sortIDs(recipients)),
		"snapshot": tandem.H(inv), "signer": signer, "auth": auth})
	return e, signControl(e, "sig", keys.Signing)
}

// homeKeyNotice is {"type":"home-key","epoch","k"}, sealed to one recipient (§10.8).
func homeKeyNotice(epoch, key []byte) Notice {
	return notice("home-key", nf("epoch", epoch), nf("k", key))
}
