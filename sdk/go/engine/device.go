package engine

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Me is who this installation is on the hub: its keychain's keys and the IDs they were
// admitted under (§5.3).
type Me struct {
	Keys                           *tandem.DeviceKeys
	Participant, Certificate, Auth []byte
	// Installation is the installation's own dot namespace; never synced (§5.3).
	Installation [8]byte
}

// Device is one installation on one hub (§6). Everything it knows is in its Store;
// everything it shares goes through its PushStore. Work happens on a push, a call from the
// app or Sync, never on a timer.
//
// Operations (syncs and the app's actions) run one at a time; reads run between them and
// while an operation waits on the network.
type Device struct {
	pin          HubPin
	store        *Store
	hub          tandem.PushStore
	directoryKey []byte
	keys         *tandem.DeviceKeys

	op sync.Mutex   // one operation at a time
	mu sync.RWMutex // the state below; released by an operation while it waits on the hub

	registry *Registry
	me       *Me
	notices  []Notice
	capsules map[string]*tandem.Control
	// Home epochs this device holds the key of; keys not yet confirmed by their epoch's start,
	// and starts held aside (offerHomeKey, acceptHomeEpoch).
	homeKeys       map[string][]byte
	homeCandidates map[string]map[string][]byte
	heldStarts     map[string]*tandem.Control
	homeEpochs     map[string]*tandem.Control
	// Assembled snapshot inventories, by epoch ID, for home and collections (§8.3).
	inventories map[string]tandem.Inventory
	home        map[string]*tandem.Record
	collections map[string]*collection
	offers      map[string]*offer
	claims      map[string]*PairingClaim
	receipts    map[string]*tandem.Control
	pairing     *pendingPairing
	profiles    map[string]*tandem.Record

	subscribers   map[int]*subscriber
	nextSub       int
	pendingEvents []Event

	keyRequestDelay time.Duration
	keyRequestTimer *time.Timer
	changing        map[string]bool
	crashPoint      string

	life   context.Context
	cancel context.CancelFunc
}

func (d *Device) directoryTag() string        { return tandem.DirectoryTag(d.directoryKey, d.pin.Hub) }
func (d *Device) directoryChannelKey() []byte { return tandem.DirectoryKey(d.directoryKey, d.pin.Hub) }

// Pin is the hub this device is pinned to.
func (d *Device) Pin() HubPin { return d.pin }

// begin starts an operation; the returned function ends it and sends what changed.
func (d *Device) begin() func() {
	d.op.Lock()
	d.mu.Lock()
	return func() {
		d.flushEvents()
		d.mu.Unlock()
		d.op.Unlock()
	}
}

// read starts a read.
func (d *Device) read() func() {
	d.mu.RLock()
	return d.mu.RUnlock
}

// The hub, as an operation uses it: the state lock is let go while it waits.

func (d *Device) hubAppend(ctx context.Context, channel string, body []byte) (string, error) {
	d.mu.Unlock()
	id, err := d.hub.Append(ctx, channel, body)
	d.mu.Lock()
	if err == nil {
		d.changed(Event{Kind: EventPublished})
	}
	return id, err
}

func (d *Device) hubChanges(ctx context.Context, cursor string) (tandem.ChangeBatch, error) {
	d.mu.Unlock()
	defer d.mu.Lock()
	return d.hub.Changes(ctx, cursor)
}

func (d *Device) hubDestroy(ctx context.Context, ids []string) error {
	d.mu.Unlock()
	defer d.mu.Lock()
	return d.hub.Destroy(ctx, ids)
}

// appendNoted appends an envelope and notes it as this device's own, applied message.
func (d *Device) appendNoted(ctx context.Context, channel string, envelope []byte) (string, error) {
	id, err := d.hubAppend(ctx, channel, envelope)
	if err != nil {
		return "", err
	}
	return id, d.store.Transaction(func() error {
		if err := d.store.NoteMessage(id, channel, tandem.H(envelope)); err != nil {
			return err
		}
		return d.store.MarkApplied(id)
	})
}

// Open opens a hub this device has the details of: its genesis (pinned at setup, invitation
// or pairing) and directory key. Nothing on the hub can replace the pin (§6.9). keys are the
// keychain's, if it has any yet.
func Open(genesis *tandem.Control, directoryKey []byte, keys *tandem.DeviceKeys, store *Store, hub tandem.PushStore) (*Device, error) {
	pin, err := PinGenesis(genesis)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(fid(genesis, "hubkey"), tandem.Commit(directoryKey)) {
		return nil, fail("device: the directory key is not the genesis's")
	}
	err = store.Transaction(func() error {
		if held, ok := store.Value("pin.genesis"); ok {
			if !bytes.Equal(held, pin.Genesis) {
				return fail("device: this store belongs to another hub")
			}
			return nil
		}
		if err := store.SetValue("pin.genesis", pin.Genesis); err != nil {
			return err
		}
		if err := store.SetValue("pin.hk", directoryKey); err != nil {
			return err
		}
		_, err := store.PutObject(genesis)
		return err
	})
	if err != nil {
		return nil, err
	}
	return load(pin, directoryKey, keys, store, hub)
}

// Reopen opens the hub a store already belongs to, after a restart: its pinned genesis and
// directory key come from the store. It returns nil if the store is new.
func Reopen(store *Store, keys *tandem.DeviceKeys, hub tandem.PushStore) (*Device, error) {
	id, ok1 := store.Value("pin.genesis")
	hk, ok2 := store.Value("pin.hk")
	if !ok1 || !ok2 {
		return nil, nil
	}
	gs, err := store.Objects("genesis")
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		if bytes.Equal(oid(g), id) {
			return Open(g, hk, keys, store, hub)
		}
	}
	return nil, nil
}

func load(pin HubPin, directoryKey []byte, keys *tandem.DeviceKeys, store *Store, hub tandem.PushStore) (*Device, error) {
	life, cancel := context.WithCancel(context.Background())
	d := &Device{pin: pin, store: store, hub: hub, directoryKey: directoryKey, keys: keys, registry: NewRegistry(pin),
		capsules: map[string]*tandem.Control{}, homeKeys: map[string][]byte{}, homeCandidates: map[string]map[string][]byte{},
		heldStarts: map[string]*tandem.Control{}, homeEpochs: map[string]*tandem.Control{}, inventories: map[string]tandem.Inventory{},
		home: map[string]*tandem.Record{}, collections: map[string]*collection{}, offers: map[string]*offer{},
		claims: map[string]*PairingClaim{}, receipts: map[string]*tandem.Control{}, profiles: map[string]*tandem.Record{},
		subscribers: map[int]*subscriber{}, keyRequestDelay: 10 * time.Minute, changing: map[string]bool{}, life: life, cancel: cancel}
	objs, err := store.Objects("")
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		switch o.Kind {
		case "genesis":
		case "capsule":
			d.capsules[string(oid(o))] = o
		case "home-epoch":
			d.homeEpochs[string(oid(o))] = o
		case "receipt":
			d.receipts[string(oid(o))] = o
		default:
			d.registry.Add(o)
		}
	}
	p, ok1 := store.Value("me.participant")
	c, ok2 := store.Value("me.certificate")
	a, ok3 := store.Value("me.auth")
	i, ok4 := store.Value("me.installation")
	if keys != nil && ok1 && ok2 && ok3 && ok4 && len(i) == 8 {
		d.me = &Me{Keys: keys, Participant: p, Certificate: c, Auth: a}
		copy(d.me.Installation[:], i)
	}
	for e := range d.homeEpochs {
		if k, ok := store.Value("home.key." + hexs([]byte(e))); ok {
			d.homeKeys[e] = k
		}
	}
	cands, _ := store.Values("home.cand.")
	for _, v := range cands {
		parts := strings.Split(v.Name, ".")
		if e, ok := unhexs(parts[2]); ok && len(parts) == 4 {
			if d.homeCandidates[string(e)] == nil {
				d.homeCandidates[string(e)] = map[string][]byte{}
			}
			d.homeCandidates[string(e)][string(v.Value)] = v.Value
		}
	}
	invs, _ := store.Values("inv.")
	for _, v := range invs {
		if e, ok := unhexs(v.Name[4:]); ok {
			if inv, err := tandem.ParseInventory(v.Value); err == nil {
				d.inventories[string(e)] = inv
			}
		}
	}
	if d.me != nil {
		scope := tandem.HomeScope(pin.Hub, d.me.Participant)
		heads := heads(d.homeEpochs, d.homeKeys)
		records, err := store.Records(scope)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			rec, err := loadRecord(store, pin.Hub, scope, r, d.homeEpochs, heads, len(heads) > 0, d.inventories)
			if err != nil {
				return nil, err
			}
			d.home[string(r)] = rec
		}
	}
	dirScope := tandem.DirectoryScope(pin.Hub)
	dirRecords, err := store.Records(dirScope)
	if err != nil {
		return nil, err
	}
	for _, record := range dirRecords {
		rows, err := store.Writes(dirScope, record)
		if err != nil {
			return nil, err
		}
		var es []tandem.Entry
		for _, row := range rows {
			if w := storedWrite(row.Payload, pin.Hub, dirScope); w != nil {
				if e, err := entryOf(w, pin.Genesis, 0); err == nil {
					es = append(es, e)
				}
			}
		}
		if d.profiles[string(record)], err = merged(nil, pin.Hub, dirScope, record, es...); err != nil {
			return nil, err
		}
	}
	if d.collections, err = loadCollections(store, pin.Hub, d.home, d.inventories); err != nil {
		return nil, err
	}
	claims, _ := store.Values("claim.")
	for _, v := range claims {
		if j, err := tandem.ParseJSON(v.Value); err == nil {
			if c, err := parseClaim(j); err == nil {
				d.claims[string(c.ID())] = c
			}
		}
	}
	if b, ok := store.Value("pairing.pending"); ok {
		if p, err := parsePendingPairing(b); err == nil {
			d.pairing = p
		}
	}
	offers, _ := store.Values("offer.")
	for _, v := range offers {
		j, err := tandem.ParseJSON(v.Value)
		if err != nil {
			continue
		}
		g, ok := j.Get("grant")
		if !ok {
			continue
		}
		grant, err := tandem.ParseControl(g, "grant")
		k, err2 := j.B64u("k", 32)
		if err != nil || err2 != nil {
			continue
		}
		o := &offer{grant: grant, key: k}
		d.offers[string(oid(grant))] = o
		// A list offered but not answered has no membership record in home, so loadCollections
		// skipped it. Without its roster the offer can't be checked again and is lost; without its
		// records, accepting it shows an empty list.
		if d.collections[string(o.collection())] == nil {
			empty, err := tandem.NewRecord(pin.Hub, o.collection(), o.collection())
			if err != nil {
				return nil, err
			}
			if d.collections[string(o.collection())], err = loadCollection(store, pin.Hub, o.collection(), empty, d.inventories); err != nil {
				return nil, err
			}
		}
		d.collection(o.collection()).offer(k, o.epoch())
	}
	return d, nil
}

// collection is the state of a list, made empty if this device holds none yet.
func (d *Device) collection(cid []byte) *collection {
	c := d.collections[string(cid)]
	if c == nil {
		c = newCollection(cid)
		d.collections[string(cid)] = c
	}
	return c
}

// Close ends the device's subscribers and timer. The store and hub connection are the app's.
func (d *Device) Close() {
	d.cancel()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.keyRequestTimer != nil {
		d.keyRequestTimer.Stop()
		d.keyRequestTimer = nil
	}
	for id, s := range d.subscribers {
		s.stop()
		delete(d.subscribers, id)
	}
}

// adopt takes on an identity this keychain was admitted under, with this installation's own ID (§5.3).
func (d *Device) adopt(participant, certificate, auth []byte) error {
	if d.keys == nil {
		return fail("device: no keychain keys")
	}
	installation, ok := d.store.Value("me.installation")
	if !ok || len(installation) != 8 {
		installation = random(8)
	}
	err := d.store.Transaction(func() error {
		for _, v := range []NamedValue{{"me.participant", participant}, {"me.certificate", certificate}, {"me.auth", auth}, {"me.installation", installation}} {
			if err := d.store.SetValue(v.Name, v.Value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.me = &Me{Keys: d.keys, Participant: participant, Certificate: certificate, Auth: auth}
	copy(d.me.Installation[:], installation)
	return nil
}

// Me is who this device is on the hub, or nil before it knows.
func (d *Device) Me() *Me {
	defer d.read()()
	if d.me == nil {
		return nil
	}
	m := *d.me
	return &m
}

// Registry is a snapshot of the device's registry, for inspection; don't change it.
func (d *Device) Registry() *Registry {
	defer d.read()()
	return d.registry
}

// Founding a hub (§6.1)

// Publish publishes a founding: every exact envelope is journalled first, then each one not
// already on the hub is appended. Running it again after a crash resumes with the same bytes.
func (d *Device) Publish(ctx context.Context, f *Founding) error {
	defer d.begin()()
	const step = "found"
	epoch := oid(f.HomeEpoch)
	var envelopes []tagged
	if j, ok := d.store.Journal(step); ok {
		var err error
		if envelopes, err = unpack(j); err != nil {
			return err
		}
	} else {
		msgs, err := f.DirectoryMessages()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			env, err := tandem.Seal(d.directoryChannelKey(), d.pin.Hub, tandem.KindRequest, d.directoryTag(), m)
			if err != nil {
				return err
			}
			envelopes = append(envelopes, tagged{d.directoryTag(), env})
		}
		homeTag := tandem.HomeTag(f.HomeKey, d.pin.Hub, f.Participant, epoch)
		start, err := cjson(f.HomeEpoch)
		if err != nil {
			return err
		}
		env, err := tandem.Seal(f.HomeKey, d.pin.Hub, tandem.KindRequest, homeTag, start)
		if err != nil {
			return err
		}
		envelopes = append(envelopes, tagged{homeTag, env}, tagged{tandem.InboxTag(f.Participant, d.pin.Hub), f.HomeKeyDelivery})
		err = d.store.Transaction(func() error {
			if err := d.store.SetJournal(step, pack(envelopes)); err != nil {
				return err
			}
			objs := []*tandem.Control{f.Certificate, f.Snapshot, f.HomeEpoch}
			if f.Capsule != nil {
				objs = append(objs, f.Capsule)
			}
			for _, o := range objs {
				if _, err := d.store.PutObject(o); err != nil {
					return err
				}
			}
			return d.store.SetValue("home.key."+hexs(epoch), f.HomeKey)
		})
		if err != nil {
			return err
		}
	}
	for _, o := range []*tandem.Control{f.Certificate, f.Snapshot} {
		if _, err := d.registry.Add(o); err != nil {
			return err
		}
	}
	if f.Capsule != nil {
		d.capsules[string(oid(f.Capsule))] = f.Capsule
	}
	d.homeEpochs[string(epoch)] = f.HomeEpoch
	d.homeKeys[string(epoch)] = f.HomeKey
	if d.me == nil {
		if err := d.adopt(f.Participant, oid(f.Certificate), oid(f.Snapshot)); err != nil {
			return err
		}
	}
	return d.export(ctx, envelopes, step)
}

// export appends each journalled envelope not already on the hub, then clears the journal.
// Safe to run again.
func (d *Device) export(ctx context.Context, envelopes []tagged, step string) error {
	for _, e := range envelopes {
		if on, err := d.store.MessagesWithHash(tandem.H(e.body)); err != nil || len(on) > 0 {
			if err != nil {
				return err
			}
			continue
		}
		id, err := d.hubAppend(ctx, e.tag, e.body)
		if err != nil {
			return err
		}
		if err := d.store.NoteMessage(id, e.tag, tandem.H(e.body)); err != nil {
			return err
		}
	}
	return d.store.ClearJournal(step)
}

// Sync

type outcome uint8

const (
	applied outcome = iota
	ignored
	waiting
)

// Sync reads everything new on the hub and applies what this device can read, then retries
// what was held. It returns how many messages it applied. Syncs never interleave.
func (d *Device) Sync(ctx context.Context) (int, error) {
	defer d.begin()()
	return d.sync(ctx)
}

func (d *Device) sync(ctx context.Context) (int, error) {
	if err := d.finishJournals(ctx); err != nil {
		return 0, err
	}
	cursor := ""
	if c, ok := d.store.Value("cursor"); ok {
		cursor = string(c)
	}
	batch, err := d.hubChanges(ctx, cursor)
	if err != nil {
		return 0, err
	}
	err = d.store.Transaction(func() error {
		for _, m := range batch.Added {
			if err := d.store.NoteMessage(m.ID, m.Channel, tandem.H(m.Body)); err != nil {
				return err
			}
			if err := d.store.Hold(m); err != nil {
				return err
			}
		}
		if err := d.store.ForgetMessages(batch.Removed); err != nil {
			return err
		}
		for _, id := range batch.Removed {
			if err := d.store.Release(id); err != nil {
				return err
			}
		}
		return d.store.SetValue("cursor", []byte(batch.Cursor))
	})
	if err != nil {
		return 0, err
	}
	count := 0
	for progress := true; progress; {
		progress = false
		if d.me == nil {
			found, err := d.discoverIdentity()
			if err != nil {
				return count, err
			}
			progress = progress || found
		}
		adopted, err := d.adoptGrantedChains()
		if err != nil {
			return count, err
		}
		progress = progress || adopted
		held, err := d.store.Held()
		if err != nil {
			return count, err
		}
		for _, m := range held {
			o, err := d.apply(m)
			if err != nil {
				return count, err
			}
			switch o {
			case applied:
				count++
				progress = true
				fallthrough
			case ignored:
				if err := d.store.Release(m.ID); err != nil {
					return count, err
				}
				if err := d.store.MarkApplied(m.ID); err != nil {
					return count, err
				}
			}
		}
	}
	if err := d.repairClaim(ctx); err != nil {
		return count, err
	}
	return count, d.followUp(ctx)
}

// discoverIdentity: a keychain that syncs already holds an admitted keypair (§6.3): find its
// certificate and a published snapshot that authorizes it, and act as that participant with
// this installation's own ID.
func (d *Device) discoverIdentity() (bool, error) {
	if d.keys == nil {
		return false, nil
	}
	sign, agree := publicSign(d.keys), publicAgree(d.keys)
	for _, c := range d.registry.Certificates() {
		if !bytes.Equal(c.Sign, sign) || !bytes.Equal(c.Agree, agree) || !d.registry.Activity(c.ID).IsValid() {
			continue
		}
		var best *Snapshot
		for _, s := range d.registry.Snapshots() {
			if !d.registry.Authorizes(s.ID, c.ID).IsValid() {
				continue
			}
			if best == nil || len(s.Certs)+len(s.Revocations) > len(best.Certs)+len(best.Revocations) {
				best = s
			}
		}
		if best == nil {
			continue
		}
		return true, d.adopt(c.Participant, c.ID, best.ID)
	}
	return false, nil
}

// apply takes one message, by its channel: the directory, this participant's inbox, a home
// epoch, or a collection epoch.
func (d *Device) apply(m tandem.Message) (outcome, error) {
	if m.Channel == d.directoryTag() {
		if kind, pt, err := tandem.Open(d.directoryChannelKey(), d.pin.Hub, m.Channel, m.Body); err == nil && kind == tandem.KindRecord {
			o, err := d.applyProfile(pt)
			if o == applied {
				d.changed(Event{Kind: EventPeople})
			}
			return o, err
		}
		isNew, err := d.applyDirectory(m)
		if err != nil || !isNew {
			return ignored, err
		}
		d.changed(Event{Kind: EventPeople})
		d.changed(Event{Kind: EventClaims})
		return applied, nil
	}
	me := d.me
	if me == nil {
		return waiting, nil
	}
	if m.Channel == tandem.InboxTag(me.Participant, d.pin.Hub) {
		return d.applyInbox(m, me)
	}
	for _, e := range sortedKeys(d.homeKeys) {
		if key := d.homeKeys[e]; m.Channel == tandem.HomeTag(key, d.pin.Hub, me.Participant, []byte(e)) {
			return d.applyHomeMessage(m, []byte(e), key, me)
		}
	}
	for _, e := range sortedKeys(d.homeCandidates) {
		for _, key := range d.homeCandidates[e] {
			if m.Channel == tandem.HomeTag(key, d.pin.Hub, me.Participant, []byte(e)) {
				return d.applyHomeMessage(m, []byte(e), key, me)
			}
		}
	}
	if cid, epoch, key, ok := d.collectionChannel(m.Channel); ok {
		return d.applyCollection(m, cid, epoch, key)
	}
	// A channel this device can't read yet may become readable once a key arrives.
	return waiting, nil
}

func (d *Device) applyHomeMessage(m tandem.Message, epoch, key []byte, me *Me) (outcome, error) {
	o, err := d.applyHome(m, epoch, key, me)
	if o == applied {
		d.refreshMemberships()
		d.changed(Event{Kind: EventHome})
		d.changed(Event{Kind: EventLists})
	}
	return o, err
}

// applyDirectory takes one directory message: a registry object, a capsule, a receipt or a
// notice. It reports whether it was new.
func (d *Device) applyDirectory(m tandem.Message) (bool, error) {
	kind, pt, err := tandem.Open(d.directoryChannelKey(), d.pin.Hub, m.Channel, m.Body)
	if err != nil || kind != tandem.KindRequest {
		return false, nil // not ours to read, or not well formed: ignored (§10.4)
	}
	j, err := tandem.ParseJSON(pt)
	if err != nil {
		return false, nil
	}
	typ, err := j.Text("type")
	if err != nil {
		return false, nil
	}
	switch typ {
	case "genesis":
		return false, nil // only the pinned genesis is ever accepted; another one is another hub (§6.9)
	case "capsule":
		o, err := tandem.ParseControl(j, "capsule")
		if err != nil || !cverify(o, "sig", d.pin.Hub) || !bytes.Equal(fid(o, "genesis"), d.pin.Genesis) {
			return false, nil
		}
		id := oid(o)
		// Which message carries it, so a password change can destroy it (§6.7).
		if err := d.store.NoteContents(m.ID, zero16, [][]byte{id}); err != nil {
			return false, err
		}
		if d.capsules[string(id)] != nil {
			return false, nil
		}
		d.capsules[string(id)] = o
		return d.store.PutObject(o)
	case "receipt":
		o, err := tandem.ParseControl(j, "receipt")
		if err != nil || d.receipts[string(oid(o))] != nil {
			return false, nil
		}
		d.receipts[string(oid(o))] = o
		return d.store.PutObject(o)
	case "home-recovery":
		n, err := ParseNotice(pt, "home-recovery", "epoch", "bytes")
		if err != nil {
			return false, nil
		}
		for _, held := range d.notices {
			if held.equal(n) {
				return false, nil
			}
		}
		d.notices = append(d.notices, n)
		return true, nil
	}
	o, err := tandem.ParseControl(j, "")
	if err != nil {
		return false, nil
	}
	if _, err := d.registry.Add(o); err != nil {
		return false, nil
	}
	return d.store.PutObject(o)
}

// Notices is the directory notices held: home-recovery boxes.
func (d *Device) Notices() []Notice {
	defer d.read()()
	return append([]Notice(nil), d.notices...)
}

// Hub management (§6.7)

// CurrentCapsules is the capsules no other capsule names: the ones a password opens.
func (d *Device) CurrentCapsules() []*tandem.Control {
	defer d.read()()
	return d.currentCapsules()
}

func (d *Device) currentCapsules() []*tandem.Control {
	named := map[string]bool{}
	for _, c := range d.capsules {
		for _, s := range fset(c, "supersedes") {
			named[string(s)] = true
		}
	}
	var out []*tandem.Control
	for _, k := range sortedKeys(d.capsules) {
		if !named[k] {
			out = append(out, d.capsules[k])
		}
	}
	return out
}

// Capsules is every admin capsule held.
func (d *Device) Capsules() []*tandem.Control {
	defer d.read()()
	var out []*tandem.Control
	for _, k := range sortedKeys(d.capsules) {
		out = append(out, d.capsules[k])
	}
	return out
}

// Unlock unlocks hub management with the hub password: MS, checked to be this hub's.
// Seconds and 1 GiB.
func (d *Device) Unlock(password string) ([]byte, error) {
	capsules := d.CurrentCapsules()
	var last error = fail("device: no admin capsule on this hub")
	for _, c := range capsules {
		sealed, _ := c.Extras["capsule"].([]byte)
		if sealed == nil {
			continue
		}
		ms, err := tandem.OpenCapsule(password, d.pin.Hub, sealed)
		if err == nil {
			return ms, nil
		}
		last = err
	}
	return nil, last
}
