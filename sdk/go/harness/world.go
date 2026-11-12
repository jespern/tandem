package harness

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/device"
	"github.com/jespern/tandem/sdk/go/store"
)

var ctx = context.Background()

var errOffline = errors.New("harness: device is offline")

// gate is a device's link to the hub, which a step can cut.
type gate struct {
	store.Store
	off  bool
	down bool // crashed, until the app launches again
	// loseReplies: appends reach the hub but their answers are lost, as
	// with a connection reset after the request (S-FLAP).
	loseReplies bool
	// failAppends, failFetches: those requests are cut before the hub
	// acts on them (net: reset); everything else gets through.
	failAppends, failFetches bool
}

var (
	errReplyLost = fmt.Errorf("%w: the answer was lost", errOffline)
	errReset     = fmt.Errorf("%w: connection reset", errOffline)
)

func (g *gate) EnsureFolder(c context.Context) error {
	if g.off || g.down {
		return errOffline
	}
	return g.Store.EnsureFolder(c)
}
func (g *gate) Append(c context.Context, tag string, env []byte) (string, error) {
	if g.off || g.down {
		return "", errOffline
	}
	if g.loseReplies {
		g.Store.Append(c, tag, env)
		return "", errReplyLost
	}
	if g.failAppends {
		return "", errReset
	}
	return g.Store.Append(c, tag, env)
}
func (g *gate) Changes(c context.Context) ([]store.Ref, []string, error) {
	if g.off || g.down {
		return nil, nil, errOffline
	}
	return g.Store.Changes(c)
}
func (g *gate) Fetch(c context.Context, ids []string) (map[string]store.Message, error) {
	if g.off || g.down {
		return nil, errOffline
	}
	if g.failFetches {
		return nil, errReset
	}
	return g.Store.Fetch(c, ids)
}
func (g *gate) Destroy(c context.Context, ids []string) error {
	if g.off || g.down {
		return errOffline
	}
	return g.Store.Destroy(c, ids)
}

type dev struct {
	name, user string
	d          *device.Device
	g          *gate
	dir        string
}

// World is one scenario: users named by a letter (J, A, B…), their devices
// named user.device (J.phone), collections and items named by the scenario.
type World struct {
	T      testing.TB
	Hub    *store.MemoryHub
	Oracle *Oracle // nil: only convergence is checked

	secrets map[string][]byte
	devs    map[string]*dev
	order   []string
	colls   map[string][]byte
	owner   map[string]string            // collection → owner user
	members map[string]map[string]string // collection → user → role, as the harness expects it
	writes  map[string]map[string]*tandem.Write
}

func NewWorld(t testing.TB, o *Oracle) *World {
	return &World{T: t, Hub: store.NewMemoryHub(), Oracle: o, secrets: map[string][]byte{}, devs: map[string]*dev{},
		colls: map[string][]byte{}, owner: map[string]string{}, members: map[string]map[string]string{},
		writes: map[string]map[string]*tandem.Write{}}
}

func (w *World) must(err error) {
	w.T.Helper()
	if err != nil {
		w.T.Fatal(err)
	}
}

// Dev returns a device, creating it on first use. A user's first device
// creates their identity; later ones get the same secret and a new device
// ID, as pairing does (§6.3).
func (w *World) Dev(name string) *device.Device {
	w.T.Helper()
	if x, ok := w.devs[name]; ok {
		return x.d
	}
	user, _, _ := strings.Cut(name, ".")
	g := &gate{Store: w.Hub.Client()}
	dir := w.T.TempDir()
	d, err := device.New(ctx, dir, user, w.secrets[user], g)
	w.must(err)
	if w.secrets[user] == nil {
		w.secrets[user] = d.Secret()
	}
	x := &dev{name: name, user: user, d: d, g: g, dir: dir}
	w.hook(x)
	w.devs[name] = x
	w.order = append(w.order, name)
	return d
}

func (w *World) hook(x *dev) {
	x.d.OnWrite = func(cid []byte, wr *tandem.Write) {
		if w.writes[string(cid)] == nil {
			w.writes[string(cid)] = map[string]*tandem.Write{}
		}
		id, _ := wr.ID()
		w.writes[string(cid)][string(id)] = wr
	}
}

// ItemID is the record ID the harness gives an item of a collection.
func ItemID(coll, item string) []byte {
	return tandem.H([]byte("harness/item/" + coll + "/" + item))[:16]
}

// Creates: `X.dev creates collection <name>`.
func (w *World) Creates(devName, coll string) {
	w.T.Helper()
	cid, err := w.Dev(devName).NewCollection(ctx, coll)
	w.must(err)
	user, _, _ := strings.Cut(devName, ".")
	w.colls[coll], w.owner[coll] = cid, user
	w.members[coll] = map[string]string{user: "owner"}
}

func (w *World) cid(coll string) []byte {
	cid, ok := w.colls[coll]
	if !ok {
		w.T.Fatalf("no collection %q", coll)
	}
	return cid
}

// Sets assigns a field. Errors a real app would show (read-only, dead) are
// returned, not fatal.
func (w *World) Sets(devName, coll, item, field, value string) error {
	return w.Dev(devName).Set(ctx, w.cid(coll), ItemID(coll, item), field, value)
}

// Adds creates an item with a title.
func (w *World) Adds(devName, coll, item string) error {
	return w.Sets(devName, coll, item, "title", item)
}

func (w *World) Tags(devName, coll, item, tag string, add bool) error {
	return w.Dev(devName).Tag(ctx, w.cid(coll), ItemID(coll, item), "tags", tag, add)
}

func (w *World) Deletes(devName, coll, item string) error {
	return w.Dev(devName).Delete(ctx, w.cid(coll), ItemID(coll, item))
}

// Shares adds a user to a collection with a role (the join flow's end:
// their devices expect the collection, the owner admits them).
func (w *World) Shares(ownerDev, coll, user, role string) {
	w.T.Helper()
	cid := w.cid(coll)
	first := w.userDevs(user)
	if len(first) == 0 {
		w.T.Fatalf("user %s has no device", user)
	}
	u := w.devs[first[0]].d
	w.must(u.Expect(ctx, cid, w.Dev(ownerDev).UID()))
	w.must(w.Dev(ownerDev).AddMember(ctx, cid, u.UID(), u.UKa(), user, role))
	w.members[coll][user] = role
}

// Removes takes a user out (a key change, §11.6).
func (w *World) Removes(ownerDev, coll, user string) error {
	err := w.Dev(ownerDev).Remove(ctx, w.cid(coll), w.devs[w.userDevs(user)[0]].d.UID())
	if err == nil {
		delete(w.members[coll], user)
	}
	return err
}

func (w *World) userDevs(user string) []string {
	var out []string
	for _, n := range w.order {
		if w.devs[n].user == user {
			out = append(out, n)
		}
	}
	return out
}

func (w *World) Offline(devName string) { w.Dev(devName); w.devs[devName].g.off = true }
func (w *World) Online(devName string)  { w.Dev(devName); w.devs[devName].g.off = false }
func (w *World) IsOffline(devName string) bool {
	return w.devs[devName] != nil && (w.devs[devName].g.off || w.devs[devName].g.down)
}

// Quits and Launches: the app stops and starts again from its stored state.
func (w *World) Relaunches(devName string) {
	w.T.Helper()
	x := w.devs[devName]
	x.g.Store = w.Hub.Client() // a new connection: the hub index isn't stored, so it reads from the start
	x.g.down = false
	d, err := device.Open(ctx, x.dir, x.g)
	w.must(err)
	x.d = d
	w.hook(x)
	if err := d.Resume(ctx); err != nil && !errors.Is(err, device.ErrSuperseded) {
		w.T.Fatal(err)
	}
}

// Syncs runs a sync; offline devices do nothing.
func (w *World) Syncs(devNames ...string) {
	w.T.Helper()
	for _, n := range devNames {
		err := w.Dev(n).Sync(ctx)
		if err != nil && !errors.Is(err, errOffline) {
			w.T.Fatalf("%s sync: %v", n, err)
		}
	}
}

// Settle syncs every online device until nothing changes any more.
func (w *World) Settle() {
	w.T.Helper()
	prev := ""
	for round := 0; round < 12; round++ {
		w.Syncs(w.order...)
		snap := fmt.Sprint(w.Hub.Len())
		for _, n := range w.order {
			for _, coll := range sortedKeys(w.colls) {
				snap += fmt.Sprint(w.devs[n].d.State(w.colls[coll]))
			}
		}
		if snap == prev {
			return
		}
		prev = snap
	}
	w.T.Fatal("the world doesn't settle: devices keep publishing")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Check: every online device of every current member shows the same state
// of the collection, and it is what the reference model computes from every
// write made (INV-CONVERGE, INV-ORACLE).
func (w *World) Check(coll string) {
	w.T.Helper()
	cid := w.cid(coll)
	var ref *dev
	var refState map[string]device.RecordView
	for _, n := range w.order {
		x := w.devs[n]
		if _, member := w.members[coll][x.user]; !member || x.g.off || x.g.down {
			continue
		}
		if x.d.Dead(cid) {
			w.T.Fatalf("%s: %s is dead", n, coll)
		}
		st := x.d.State(cid)
		if ref == nil {
			ref, refState = x, st
			continue
		}
		if !reflect.DeepEqual(st, refState) {
			w.T.Fatalf("%s and %s disagree on %s:%s%s", ref.name, n, coll, stateDiff(ref.name, refState, n, st), w.explain(cid, refState, st, ref, x))
		}
	}
	if ref == nil || w.Oracle == nil {
		return
	}
	w.checkOracle(coll, ref, refState)
}

// oracleRequest is what the reference model is asked about a collection:
// the roster and inventory ref holds, and every write any device signed.
func (w *World) oracleRequest(coll string, ref *dev) map[string]any {
	cid := w.cid(coll)
	genesis, stmts := ref.d.Roster(cid)
	ep, inv := ref.d.Inventory(cid)
	var writes []oWrite
	for _, id := range sortedKeys(w.writes[string(cid)]) {
		writes = append(writes, writeOf(w.writes[string(cid)][id]))
	}
	req := map[string]any{"genesis": hx(genesis), "statements": statements(stmts), "writes": writes,
		"meta": hx(tandem.NamedID("_meta")), "named": []string{hx(tandem.NamedID("_meta")), hx(tandem.RosterID)}, "channel": hx(ep)}
	if inv != nil {
		m := map[string][]string{}
		for rec, ids := range inv {
			for _, id := range ids {
				m[hx([]byte(rec))] = append(m[hx([]byte(rec))], hx(id))
			}
		}
		req["inventory"] = m
	}
	return req
}

func (w *World) checkOracle(coll string, ref *dev, got map[string]device.RecordView) {
	w.T.Helper()
	cid := w.cid(coll)
	a, err := w.Oracle.Ask(w.oracleRequest(coll, ref))
	w.must(err)
	if a.Dead {
		w.T.Fatalf("oracle: %s is dead", coll)
	}
	value := func(id string) string {
		for _, wr := range w.writes[string(cid)] {
			wid, _ := wr.ID()
			if hx(wid) == id {
				return string(wr.Value)
			}
		}
		return "<unknown write " + id[:8] + ">"
	}
	want := map[string]device.RecordView{}
	for rec, rs := range a.State {
		v := device.RecordView{Deleted: rs.Deleted, Fields: map[string]string{}, Sets: map[string][]string{}}
		for f, fs := range rs.Fields {
			if fs.Display != "" {
				v.Fields[f] = value(fs.Display)
			}
		}
		for f, els := range rs.Sets {
			if len(els) > 0 {
				s := slices.Clone(els)
				slices.Sort(s)
				v.Sets[f] = s
			}
		}
		raw, _ := hexDecode(rec)
		want[string(raw)] = v
	}
	for rec := range got { // the engine shows a record the model doesn't know only if it holds nothing
		if _, ok := want[rec]; !ok && (len(got[rec].Fields) > 0 || len(got[rec].Sets) > 0 || got[rec].Deleted) {
			w.T.Fatalf("%s shows record %x that the oracle doesn't: %v", ref.name, rec, got[rec])
		}
	}
	for rec, v := range want {
		g := got[rec]
		if g.Fields == nil {
			g = device.RecordView{Fields: map[string]string{}, Sets: map[string][]string{}}
		}
		if !reflect.DeepEqual(g, v) {
			w.T.Fatalf("%s and the oracle disagree on record %x of %s:\n engine %v\n oracle %v", ref.name, rec, coll, g, v)
		}
	}
}

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

// stateDiff lists only the records two devices show differently.
func stateDiff(an string, a map[string]device.RecordView, bn string, b map[string]device.RecordView) string {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var out strings.Builder
	for _, k := range sortedKeys(keys) {
		if !reflect.DeepEqual(a[k], b[k]) {
			fmt.Fprintf(&out, "\n record %x\n  %s: %+v\n  %s: %+v", k, an, a[k], bn, b[k])
		}
	}
	return out.String()
}

// explain says, for each record two devices disagree on, which writes were
// made and what each device holds of them.
func (w *World) explain(cid []byte, a, b map[string]device.RecordView, da, db *dev) string {
	var out strings.Builder
	for _, k := range sortedKeys(a) {
		if reflect.DeepEqual(a[k], b[k]) {
			continue
		}
		fmt.Fprintf(&out, "\n writes made to %x:", []byte(k)[:4])
		for id, wr := range w.writes[string(cid)] {
			if string(wr.Record) == k {
				fmt.Fprintf(&out, "\n  %x %s=%q by device %x, ref %x", []byte(id)[:4], wr.Field, wr.Value, wr.Dot.Device[:2], wr.Ref[:4])
			}
		}
		for _, x := range []*dev{da, db} {
			fmt.Fprintf(&out, "\n %s (%x): %s", x.name, x.d.ID()[:2], x.d.Explain(cid, []byte(k)))
		}
	}
	return out.String()
}
