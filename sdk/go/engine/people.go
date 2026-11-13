package engine

import (
	"bytes"
	"context"
	"slices"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// The hub's people (§6.6): participants from the registry, with the names they gave
// themselves in their directory profiles (§10.8), and their devices underneath.

// DeviceInfo is one admitted keychain of a participant, as the directory shows it.
type DeviceInfo struct {
	Certificate []byte
	// Label is the label the device gave itself when it was admitted.
	Label string
	// Active: admitted now, valid and not revoked.
	Active bool
	// IsThisDevice: this device's own keychain.
	IsThisDevice bool
}

// Person is a participant of the hub (§6.6).
type Person struct {
	ID []byte
	// Name is the name from their profile; "" until they set one. Display text only.
	Name      string
	IsMe      bool
	IsCreator bool
	Devices   []DeviceInfo
}

func profileRecord(participant []byte) []byte {
	return tandem.NamedID("profile:" + tandem.B64u(participant))
}

// People is everyone on the hub with a valid certificate, by name, then ID.
func (d *Device) People() []Person {
	defer d.read()()
	byParticipant := map[string][]*Certificate{}
	for _, c := range d.registry.Certificates() {
		if d.registry.CertificateValidity(c.ID).IsValid() {
			byParticipant[string(c.Participant)] = append(byParticipant[string(c.Participant)], c)
		}
	}
	var out []Person
	for uid, certs := range byParticipant {
		p := Person{ID: []byte(uid), Name: d.name(d.profiles, []byte(uid)), IsCreator: uid == string(d.pin.Creator)}
		p.IsMe = d.me != nil && bytes.Equal(d.me.Participant, p.ID)
		for _, c := range certs {
			p.Devices = append(p.Devices, DeviceInfo{Certificate: c.ID, Label: c.Label, Active: d.registry.Activity(c.ID).IsValid(),
				IsThisDevice: d.me != nil && bytes.Equal(c.ID, d.me.Certificate)})
		}
		slices.SortFunc(p.Devices, func(a, b DeviceInfo) int { return strings.Compare(a.Label, b.Label) })
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Person) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return bytes.Compare(a.ID, b.ID)
	})
	return out
}

func (d *Device) name(profiles map[string]*tandem.Record, participant []byte) string {
	v, _ := value(profiles[string(profileRecord(participant))], "name")
	return string(v)
}

// NameOf is a participant's name from their profile, "" if none.
func (d *Device) NameOf(participant []byte) string {
	defer d.read()()
	return d.name(d.profiles, participant)
}

// SetName sets this participant's name in their directory profile: at most 256 bytes of UTF-8 (§10.8).
func (d *Device) SetName(ctx context.Context, name string) error {
	defer d.begin()()
	me := d.me
	if me == nil {
		return fail("device: not on this hub yet")
	}
	if len(name) > 256 {
		return fail("profile: a name is at most 256 bytes")
	}
	scope, record := tandem.DirectoryScope(d.pin.Hub), profileRecord(me.Participant)
	current := d.profiles[string(record)]
	if current == nil {
		current = emptyRecord(d.pin.Hub, scope, record)
	}
	var writes []*tandem.Write
	sign := func(field string, val []byte) error {
		w, err := d.signWrite(me, current, scope, record, d.pin.Genesis, 0, d.pin.Genesis, field, tandem.OpAssign, val, nil)
		if err != nil {
			return err
		}
		e, err := entryOf(w, d.pin.Genesis, 0)
		if err != nil {
			return err
		}
		if current, err = merged(current, d.pin.Hub, scope, record, e); err != nil {
			return err
		}
		writes = append(writes, w)
		return nil
	}
	if _, ok := current.Value("_name"); !ok {
		if err := sign("_name", []byte("profile:"+tandem.B64u(me.Participant))); err != nil {
			return err
		}
	}
	if err := sign("name", []byte(name)); err != nil {
		return err
	}
	d.profiles[string(record)] = current
	err := d.store.Transaction(func() error {
		for _, w := range writes {
			p, err := onePayload(record, w)
			if err != nil {
				return err
			}
			if _, err := d.store.PutWrite(writeID(w), scope, record, d.pin.Genesis, p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.changed(Event{Kind: EventPeople})
	p, err := tandem.RecordPayload(record, writes)
	if err != nil {
		return err
	}
	env, err := tandem.Seal(d.directoryChannelKey(), d.pin.Hub, tandem.KindRecord, d.directoryTag(), p)
	if err != nil {
		return err
	}
	_, err = d.appendNoted(ctx, d.directoryTag(), env)
	return err
}

// applyProfile takes profile writes in the directory channel: only an admitted device of
// exactly that participant writes its profile, under the genesis (§10.8).
func (d *Device) applyProfile(plaintext []byte) (outcome, error) {
	scope := tandem.DirectoryScope(d.pin.Hub)
	state, _, err := tandem.ParseKind1(plaintext, d.pin.Hub, scope)
	if err != nil || state == nil {
		return ignored, nil
	}
	var entries []tandem.Entry
	for _, w := range state.Writes {
		if !bytes.Equal(w.Ref, d.pin.Genesis) || !bytes.Equal(state.Record, profileRecord(w.Author)) {
			continue
		}
		switch d.registry.Authorizes(w.Authorization, w.Certificate).State {
		case Pending:
			return waiting, nil
		case Invalid:
			continue
		}
		cert := d.registry.Certificate(w.Certificate)
		// A revoked keychain renames nobody.
		if cert == nil || !bytes.Equal(cert.Participant, w.Author) || !d.registry.Activity(cert.ID).IsValid() || w.Verify(cert.Sign) != nil {
			continue
		}
		if w.Field == "name" && len(w.Value) > 256 {
			continue
		}
		e, err := entryOf(w, d.pin.Genesis, 0)
		if err != nil {
			return ignored, err
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return ignored, nil
	}
	r, err := merged(d.profiles[string(state.Record)], d.pin.Hub, scope, state.Record, entries...)
	if err != nil {
		return ignored, err
	}
	d.profiles[string(state.Record)] = r
	return applied, d.store.Transaction(func() error {
		for _, e := range entries {
			p, err := onePayload(state.Record, e.W)
			if err != nil {
				return err
			}
			if _, err := d.store.PutWrite(e.ID, scope, state.Record, d.pin.Genesis, p); err != nil {
				return err
			}
		}
		return nil
	})
}
