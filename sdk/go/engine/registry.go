package engine

import (
	"bytes"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The device registry (§6.9), as proofs/TandemProofs/Registry.lean defines it.
//
// Objects are named by content and merge by union; nothing here depends on the order they
// arrived in. Validity has three outcomes: an object whose dependencies haven't arrived is
// pending, never invalid, and becomes valid or invalid only once they have (§8.3).

// HubPin is what a device pins for a hub: its manager keys (hubID and pub(MKa)), its
// genesis and the creator it names.
type HubPin struct {
	Hub, Agree, Genesis, Creator []byte
}

// PinGenesis pins a hub from its genesis, after checking the genesis's signature.
func PinGenesis(g *tandem.Control) (HubPin, error) {
	if g == nil || g.Kind != "genesis" || fid(g, "hub") == nil || !cverify(g, "sig", fid(g, "hub")) {
		return HubPin{}, fail("registry: not a genesis signed by its own hub key")
	}
	return HubPin{Hub: fid(g, "hub"), Agree: fid(g, "agree"), Genesis: oid(g), Creator: fid(g, "creator")}, nil
}

// Certificate is one admitted keychain (§6.9).
type Certificate struct {
	ID, RequestID         []byte
	Participant           []byte
	Sign, Agree           []byte
	Label                 string
	How                   uint64
	Authority, Enrollment []byte
	Issued                [][]byte
	Object, Request       *tandem.Control
}

func (c *Certificate) sameKeys(o *Certificate) bool {
	return bytes.Equal(c.Sign, o.Sign) && bytes.Equal(c.Agree, o.Agree)
}

type revocation struct {
	id, target []byte
	retained   [][]byte
}

type delegation struct {
	id, participant, source, key, token []byte
	expires                             uint64
	pkg, gate                           []byte
	object                              *tandem.Control
}

type capability struct {
	id, key, invite []byte
	expires         uint64
}

// Snapshot is a policy snapshot (§6.9).
type Snapshot struct {
	ID, Participant    []byte
	Certs, Revocations [][]byte
}

// Registry holds a hub's certificates, revocations, delegations, capabilities and snapshots.
type Registry struct {
	Pin          HubPin
	certs        map[string]*Certificate
	revs         map[string]*revocation
	delegations  map[string]*delegation
	capabilities map[string]*capability
	snapshots    map[string]*Snapshot
}

func NewRegistry(pin HubPin) *Registry {
	return &Registry{Pin: pin, certs: map[string]*Certificate{}, revs: map[string]*revocation{},
		delegations: map[string]*delegation{}, capabilities: map[string]*capability{}, snapshots: map[string]*Snapshot{}}
}

func (r *Registry) bound(o *tandem.Control) bool {
	if h := fid(o, "hub"); h != nil && !bytes.Equal(h, r.Pin.Hub) {
		return false
	}
	if g := fid(o, "genesis"); g != nil && !bytes.Equal(g, r.Pin.Genesis) {
		return false
	}
	return true
}

// Add takes in one registry object and returns its ID. It fails if the object can never be
// valid on this hub: another hub or genesis, malformed, or a manager signature that doesn't verify.
func (r *Registry) Add(o *tandem.Control) ([]byte, error) {
	if !r.bound(o) {
		return nil, fail("registry: %s is for another hub", o.Kind)
	}
	id := oid(o)
	if id == nil {
		return nil, fail("registry: malformed %s", o.Kind)
	}
	switch o.Kind {
	case "certificate":
		req, _ := o.Extras["body"].(*tandem.Control)
		if req == nil || req.Kind != "request" || !r.bound(req) || !bytes.Equal(oid(req), fid(o, "request")) {
			return nil, fail("registry: certificate without its request")
		}
		how, _ := fint(req, "how")
		r.certs[string(id)] = &Certificate{ID: id, RequestID: fid(o, "request"), Participant: fid(req, "participant"),
			Sign: fid(req, "sign"), Agree: fid(req, "agree"), Label: ftext(req, "label"), How: how,
			Authority: fid(req, "authority"), Enrollment: fid(req, "enrollment"), Issued: fset(o, "issued"), Object: o, Request: req}
	case "revocation":
		if !cverify(o, "sig", r.Pin.Hub) {
			return nil, fail("registry: revocation not signed by the hub")
		}
		r.revs[string(id)] = &revocation{id: id, target: fid(o, "target"), retained: fset(o, "retained")}
	case "delegation":
		exp, _ := fint(o, "expires")
		r.delegations[string(id)] = &delegation{id: id, participant: fid(o, "participant"), source: fid(o, "source"),
			key: fid(o, "key"), token: fid(o, "token"), expires: exp, pkg: fid(o, "package"), gate: fid(o, "gate"), object: o}
	case "invitation":
		if !cverify(o, "sig", r.Pin.Hub) {
			return nil, fail("registry: invitation not signed by the hub")
		}
		exp, _ := fint(o, "expires")
		r.capabilities[string(id)] = &capability{id: id, key: fid(o, "key"), invite: fid(o, "invite"), expires: exp}
	case "snapshot":
		r.snapshots[string(id)] = &Snapshot{ID: id, Participant: fid(o, "participant"), Certs: fset(o, "certs"), Revocations: fset(o, "revocations")}
	default:
		return nil, fail("registry: %s is not a registry object", o.Kind)
	}
	return id, nil
}

func (r *Registry) Certificate(id []byte) *Certificate { return r.certs[string(id)] }

// Certificates is every certificate held, by ID.
func (r *Registry) Certificates() []*Certificate {
	out := make([]*Certificate, 0, len(r.certs))
	for _, k := range sortedKeys(r.certs) {
		out = append(out, r.certs[k])
	}
	return out
}

// RevocationIDs is every revocation held, sorted.
func (r *Registry) RevocationIDs() [][]byte {
	var out [][]byte
	for _, k := range sortedKeys(r.revs) {
		out = append(out, []byte(k))
	}
	return out
}

func (r *Registry) Snapshot(id []byte) *Snapshot { return r.snapshots[string(id)] }

// Snapshots is every snapshot held, by ID.
func (r *Registry) Snapshots() []*Snapshot {
	out := make([]*Snapshot, 0, len(r.snapshots))
	for _, k := range sortedKeys(r.snapshots) {
		out = append(out, r.snapshots[k])
	}
	return out
}

// §6.9 rules

// cuts: revocation rv, whose target is t, retires c.
func cuts(t *Certificate, rv *revocation, c *Certificate) bool {
	return bytes.Equal(t.Participant, c.Participant) && (t.sameKeys(c) || (!containsID(rv.retained, c.ID) && !containsID(c.Issued, rv.id)))
}

// survives: c survives every revocation in rs whose target is known.
func (r *Registry) survives(rs []*revocation, c *Certificate) bool {
	for _, rv := range rs {
		if t := r.certs[string(rv.target)]; t != nil && cuts(t, rv, c) {
			return false
		}
	}
	return true
}

func (r *Registry) revocations(ids [][]byte) ([]*revocation, [][]byte) {
	var found []*revocation
	var missing [][]byte
	for _, id := range ids {
		if rv := r.revs[string(id)]; rv != nil {
			found = append(found, rv)
		} else {
			missing = append(missing, id)
		}
	}
	return found, missing
}

// CertificateValidity is certOK: a valid certificate, with every proof of §6.9 and its dependencies held.
func (r *Registry) CertificateValidity(id []byte) Validity { return r.certOK(id, 0) }

func (r *Registry) certOK(id []byte, depth int) Validity {
	c := r.certs[string(id)]
	if c == nil {
		return pending(id)
	}
	if depth >= 64 {
		return invalid("approval chain too deep")
	}
	if !cverify(c.Request, "self", c.Sign) {
		return invalid("request not signed by its own keys")
	}
	issued, missingRevs := r.revocations(c.Issued)
	if len(missingRevs) > 0 {
		return pending(missingRevs...)
	}
	var missingTargets [][]byte
	for _, rv := range issued {
		if r.certs[string(rv.target)] == nil {
			missingTargets = append(missingTargets, rv.target)
		}
	}
	if len(missingTargets) > 0 {
		return pending(missingTargets...)
	}
	registration, _ := tandem.RegistrationID(r.Pin.Hub, c.Sign, c.Agree, c.Enrollment)
	signedByHub := func() Validity {
		if cverify(c.Object, "sig", r.Pin.Hub) {
			return valid()
		}
		return invalid("not signed by the hub")
	}
	switch c.How {
	case tandem.HowFirst:
		if c.Authority != nil || !bytes.Equal(c.Participant, registration) {
			return invalid("first admission of other keys")
		}
		return signedByHub()
	case tandem.HowCreatorRecovery:
		if c.Authority != nil || !bytes.Equal(c.Participant, r.Pin.Creator) {
			return invalid("recovery for someone but the creator")
		}
		return signedByHub()
	case tandem.HowInvited:
		if c.Authority == nil {
			return invalid("invited without a capability")
		}
		capa := r.capabilities[string(c.Authority)]
		if capa == nil {
			return pending(c.Authority)
		}
		if !bytes.Equal(c.Participant, registration) {
			return invalid("invitation for other keys")
		}
		if !cverify(c.Request, "approver", capa.key) {
			return invalid("claim not signed by the invite key")
		}
		if !cverify(c.Object, "sig", capa.key) {
			return invalid("not signed by the invite key")
		}
		return valid()
	case tandem.HowByDevice:
		if c.Authority == nil {
			return invalid("no approving device")
		}
		src := r.certs[string(c.Authority)]
		if src == nil {
			return pending(c.Authority)
		}
		if !bytes.Equal(src.Participant, c.Participant) {
			return invalid("approved by another participant's device")
		}
		if !cverify(c.Request, "approver", src.Sign) {
			return invalid("not approved by that device")
		}
		if !r.survives(issued, src) {
			return invalid("approving device revoked at issuance")
		}
		if v := signedByHub(); !v.IsValid() {
			return v
		}
		return r.certOK(src.ID, depth+1)
	case tandem.HowByPairing:
		if c.Authority == nil {
			return invalid("no delegation")
		}
		dl := r.delegations[string(c.Authority)]
		if dl == nil {
			return pending(c.Authority)
		}
		if !bytes.Equal(dl.participant, c.Participant) {
			return invalid("delegation for another participant")
		}
		if !cverify(c.Request, "approver", dl.key) {
			return invalid("claim not signed by the link key")
		}
		src := r.certs[string(dl.source)]
		if src == nil {
			return pending(dl.source)
		}
		if !bytes.Equal(src.Participant, dl.participant) || !cverify(dl.object, "sig", src.Sign) {
			return invalid("delegation not signed by a device of that participant")
		}
		if !r.survives(issued, src) {
			return invalid("delegating device revoked at issuance")
		}
		if v := signedByHub(); !v.IsValid() {
			return v
		}
		return r.certOK(dl.source, depth+1)
	}
	return invalid("unknown admission")
}

// RevocationValidity is revOK: signed by the hub (checked on arrival), its target held, its
// retained set of that participant.
func (r *Registry) RevocationValidity(id []byte) Validity {
	rv := r.revs[string(id)]
	if rv == nil {
		return pending(id)
	}
	t := r.certs[string(rv.target)]
	if t == nil {
		return pending(rv.target)
	}
	var missing [][]byte
	for _, k := range rv.retained {
		c := r.certs[string(k)]
		if c == nil {
			missing = append(missing, k)
			continue
		}
		if !bytes.Equal(c.Participant, t.Participant) {
			return invalid("retains another participant's device")
		}
	}
	if len(missing) > 0 {
		return pending(missing...)
	}
	return valid()
}

// Activity is active: held, valid, and surviving every revocation held.
func (r *Registry) Activity(id []byte) Validity { return r.activityUnder(id, r.RevocationIDs()) }

func (r *Registry) activityUnder(id []byte, revocationIDs [][]byte) Validity {
	ok := r.CertificateValidity(id)
	c := r.certs[string(id)]
	if !ok.IsValid() || c == nil {
		return ok
	}
	rs, missing := r.revocations(revocationIDs)
	if len(missing) > 0 {
		return pending(missing...)
	}
	var checks []Validity
	for _, rv := range rs {
		checks = append(checks, r.RevocationValidity(rv.id))
	}
	if v := allValid(checks...); !v.IsValid() {
		return v
	}
	if r.survives(rs, c) {
		return valid()
	}
	return invalid("revoked")
}

// CurrentSnapshot is a snapshot of everything held for one participant: its valid
// certificates and the valid revocations of them.
func (r *Registry) CurrentSnapshot(participant []byte) *tandem.Control {
	var mine, revoked [][]byte
	for _, c := range r.certs {
		if bytes.Equal(c.Participant, participant) && r.CertificateValidity(c.ID).IsValid() {
			mine = append(mine, c.ID)
		}
	}
	for _, rv := range r.revs {
		if t := r.certs[string(rv.target)]; t != nil && bytes.Equal(t.Participant, participant) && r.RevocationValidity(rv.id).IsValid() {
			revoked = append(revoked, rv.id)
		}
	}
	return newControl("snapshot", tandem.Fields{"hub": r.Pin.Hub, "genesis": r.Pin.Genesis, "participant": participant,
		"certs": nonNil(sortIDs(mine)), "revocations": nonNil(sortIDs(revoked))})
}

func nonNil(ids [][]byte) [][]byte {
	if ids == nil {
		return [][]byte{}
	}
	return ids
}

// Authorizes reports whether certificate may act under snapshot: the snapshot is its
// participant's and lists it, every object it names is valid, and the certificate survives
// its revocations.
func (r *Registry) Authorizes(snapshot, certificate []byte) Validity {
	s := r.snapshots[string(snapshot)]
	if s == nil {
		return pending(snapshot)
	}
	c := r.certs[string(certificate)]
	if c == nil {
		return pending(certificate)
	}
	if !bytes.Equal(s.Participant, c.Participant) || !containsID(s.Certs, certificate) {
		return invalid("not in its snapshot")
	}
	var named []Validity
	for _, id := range s.Certs {
		named = append(named, r.CertificateValidity(id))
	}
	for _, id := range s.Revocations {
		named = append(named, r.RevocationValidity(id))
	}
	if v := allValid(named...); !v.IsValid() {
		return v
	}
	return r.activityUnder(certificate, s.Revocations)
}

// Extends: source includes every certificate and revocation of floor (§6.9).
func (r *Registry) Extends(source, floor []byte) Validity {
	s := r.snapshots[string(source)]
	if s == nil {
		return pending(source)
	}
	f := r.snapshots[string(floor)]
	if f == nil {
		return pending(floor)
	}
	if !bytes.Equal(s.Participant, f.Participant) {
		return invalid("snapshots of two participants")
	}
	for _, c := range f.Certs {
		if !containsID(s.Certs, c) {
			return invalid("does not extend the floor")
		}
	}
	for _, rv := range f.Revocations {
		if !containsID(s.Revocations, rv) {
			return invalid("does not extend the floor")
		}
	}
	return valid()
}
