package harness

// Steps beyond world.go, for the hand-written scenarios (scenarios/*.md):
// Liszto's item fields, reading what a device shows, what the reference
// model keeps, owner changes and crashes, and Mallory's modified client.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/device"
)

// ---- Liszto's item fields (DESIGN §17) ----

// Completed is the completedAt value the harness writes; the time itself
// doesn't matter to any scenario.
const Completed = "2026-11-12T10:00:00Z"

// Completes: `X.dev completes <item>`.
func (w *World) Completes(devName, coll, item string) error {
	return w.Sets(devName, coll, item, "completedAt", Completed)
}

// Uncompletes: `X.dev uncompletes <item>` (completedAt cleared).
func (w *World) Uncompletes(devName, coll, item string) error {
	return w.Sets(devName, coll, item, "completedAt", "")
}

// Reorders sets an item's sortKey (Liszto §6.1); the scenario chooses the
// key that "to top" or "after X" would produce.
func (w *World) Reorders(devName, coll, item, sortKey string) error {
	return w.Sets(devName, coll, item, "sortKey", sortKey)
}

// ParentRef is how parentID names an item.
func ParentRef(coll, item string) string { return hex.EncodeToString(ItemID(coll, item)) }

// Indents: `X.dev indents <item> under <parent>`.
func (w *World) Indents(devName, coll, item, parent string) error {
	return w.Sets(devName, coll, item, "parentID", ParentRef(coll, parent))
}

// Outdents: `X.dev outdents <item>`.
func (w *World) Outdents(devName, coll, item string) error {
	return w.Sets(devName, coll, item, "parentID", "")
}

// AddsAt creates an item with a title and a sortKey.
func (w *World) AddsAt(devName, coll, item, sortKey string) error {
	if err := w.Adds(devName, coll, item); err != nil {
		return err
	}
	return w.Reorders(devName, coll, item, sortKey)
}

// AddsUnder creates a sub-item.
func (w *World) AddsUnder(devName, coll, item, parent string) error {
	if err := w.Adds(devName, coll, item); err != nil {
		return err
	}
	return w.Indents(devName, coll, item, parent)
}

// Moves: `X.dev moves <item> to <coll>`: create in the target with the
// fields the device shows, then delete in the source (§9.5). The copy is
// named item in the target.
func (w *World) Moves(devName, from, item, to string) error {
	v, ok := w.Dev(devName).State(w.cid(from))[string(ItemID(from, item))]
	if !ok || v.Deleted {
		return fmt.Errorf("%s doesn't show %s in %s", devName, item, from)
	}
	for _, f := range sortedKeys(v.Fields) {
		if err := w.Sets(devName, to, item, f, v.Fields[f]); err != nil {
			return err
		}
	}
	for _, f := range sortedKeys(v.Sets) {
		for _, el := range v.Sets[f] {
			if err := w.Dev(devName).Tag(ctx, w.cid(to), ItemID(to, item), f, el, true); err != nil {
				return err
			}
		}
	}
	return w.Deletes(devName, from, item)
}

// Repairs applies Liszto's completion repair rule on one device (§6.3)
// until nothing is left to repair: a completed parent with an open
// sub-item is reopened (which can expose its own parent when concurrent
// indents made two levels, G-33). It returns the parents it reopened.
func (w *World) Repairs(devName, coll string) []string {
	w.T.Helper()
	var all []string
	for {
		r := w.repairOnce(devName, coll)
		if len(r) == 0 {
			slices.Sort(all)
			return all
		}
		all = append(all, r...)
	}
}

func (w *World) repairOnce(devName, coll string) []string {
	st := w.Dev(devName).State(w.cid(coll))
	var reopened []string
	for rec, v := range st {
		if v.Deleted || v.Fields["completedAt"] == "" {
			continue
		}
		ref := hex.EncodeToString([]byte(rec))
		for _, c := range st {
			if !c.Deleted && c.Fields["parentID"] == ref && c.Fields["completedAt"] == "" {
				err := w.Dev(devName).Set(ctx, w.cid(coll), []byte(rec), "completedAt", "")
				w.must(err)
				reopened = append(reopened, v.Fields["title"])
				break
			}
		}
	}
	slices.Sort(reopened)
	return reopened
}

// ---- what a device shows ----

// Shows is a field's displayed value on a device, and whether the item is
// shown at all (present and not deleted).
func (w *World) Shows(devName, coll, item, field string) (string, bool) {
	v, ok := w.Dev(devName).State(w.cid(coll))[string(ItemID(coll, item))]
	if !ok || v.Deleted {
		return "", false
	}
	return v.Fields[field], true
}

// Present reports whether a device shows an item.
func (w *World) Present(devName, coll, item string) bool {
	_, ok := w.Shows(devName, coll, item, "title")
	return ok
}

// Elements are a set field's present elements on a device.
func (w *World) Elements(devName, coll, item, field string) []string {
	v := w.Dev(devName).State(w.cid(coll))[string(ItemID(coll, item))]
	return v.Sets[field]
}

// Order is Liszto's list as a device renders it, from field values only:
// open items by (sortKey, record ID), then the completed ones (the Done
// section) in the same order. Titles are returned. Nesting isn't rendered.
func (w *World) Order(devName, coll string) (open, done []string) {
	type it struct{ key, id, title string }
	var o, d []it
	for rec, v := range w.Dev(devName).State(w.cid(coll)) {
		if v.Deleted || v.Fields["title"] == "" && len(v.Fields) == 0 {
			continue
		}
		if bytes.Equal([]byte(rec), tandem.NamedID("_meta")) {
			continue
		}
		x := it{v.Fields["sortKey"], rec, v.Fields["title"]}
		if v.Fields["completedAt"] != "" {
			d = append(d, x)
		} else {
			o = append(o, x)
		}
	}
	less := func(a, b it) int {
		if c := strings.Compare(a.key, b.key); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	}
	slices.SortFunc(o, less)
	slices.SortFunc(d, less)
	for _, x := range o {
		open = append(open, x.title)
	}
	for _, x := range d {
		done = append(done, x.title)
	}
	return open, done
}

// Want fails the test if got != want, naming what was checked.
func (w *World) Want(what string, got, want any) {
	w.T.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		w.T.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// WantShows checks a field's displayed value on every named device.
func (w *World) WantShows(coll, item, field, want string, devNames ...string) {
	w.T.Helper()
	for _, n := range devNames {
		got, ok := w.Shows(n, coll, item, field)
		if !ok {
			w.T.Fatalf("%s doesn't show %s in %s", n, item, coll)
		}
		if got != want {
			w.T.Fatalf("%s shows %s.%s = %q, want %q", n, item, field, got, want)
		}
	}
}

// WantGone checks that no named device shows an item.
func (w *World) WantGone(coll, item string, devNames ...string) {
	w.T.Helper()
	for _, n := range devNames {
		if w.Present(n, coll, item) {
			w.T.Fatalf("%s still shows %s in %s", n, item, coll)
		}
	}
}

// ---- what the reference model keeps ----

func (w *World) refDev(coll string) *dev {
	for _, n := range w.order {
		x := w.devs[n]
		if _, member := w.members[coll][x.user]; member && !x.g.off && !x.g.down {
			return x
		}
	}
	return nil
}

// Answer asks the reference model about a collection as its first online
// member device holds it. A scenario that needs it is skipped without the
// oracle.
func (w *World) Answer(coll string) *Answer {
	w.T.Helper()
	if w.Oracle == nil {
		w.T.Skip("needs: the oracle (TANDEM_ORACLE)")
	}
	ref := w.refDev(coll)
	if ref == nil {
		w.T.Fatalf("no online member device of %s", coll)
	}
	a, err := w.Oracle.Ask(w.oracleRequest(coll, ref))
	w.must(err)
	return a
}

func (w *World) writeByID(coll, id string) *tandem.Write {
	for _, wr := range w.writes[string(w.cid(coll))] {
		wid, _ := wr.ID()
		if hex.EncodeToString(wid) == id {
			return wr
		}
	}
	return nil
}

// Kept is the values of the kept writes of a field (its siblings, §9.2),
// sorted. More than one means the app is told of a conflict.
func (w *World) Kept(coll, item, field string) []string {
	w.T.Helper()
	return w.keptOf(coll, ItemID(coll, item), field)
}

func (w *World) keptOf(coll string, rec []byte, field string) []string {
	w.T.Helper()
	a := w.Answer(coll)
	var out []string
	for _, id := range a.State[hex.EncodeToString(rec)].Fields[field].Kept {
		wr := w.writeByID(coll, id)
		if wr == nil {
			w.T.Fatalf("oracle keeps write %s that no device signed", id[:8])
		}
		out = append(out, string(wr.Value))
	}
	slices.Sort(out)
	return out
}

// WantKept checks the kept values of a field.
func (w *World) WantKept(coll, item, field string, want ...string) {
	w.T.Helper()
	slices.Sort(want)
	if got := w.Kept(coll, item, field); !slices.Equal(got, want) {
		w.T.Fatalf("kept %s.%s = %q, want %q", item, field, got, want)
	}
}

// NoConflicts checks that no field of any record has more than one kept
// write.
func (w *World) NoConflicts(coll string) {
	w.T.Helper()
	for rec, rs := range w.Answer(coll).State {
		for f, fs := range rs.Fields {
			if len(fs.Kept) > 1 {
				w.T.Fatalf("%s: record %s field %s has %d kept writes", coll, rec[:8], f, len(fs.Kept))
			}
		}
	}
}

// Author is the user (letter) who signed a field's displayed write.
func (w *World) Author(coll, item, field string) string {
	w.T.Helper()
	fs := w.Answer(coll).State[hex.EncodeToString(ItemID(coll, item))].Fields[field]
	wr := w.writeByID(coll, fs.Display)
	if wr == nil {
		w.T.Fatalf("no displayed write for %s.%s", item, field)
	}
	return w.userOf(wr.Author)
}

// Writes are the writes the harness saw signed for an item's field (every
// op when field is "*"), in signing order is not kept: sorted by dot.
func (w *World) Writes(coll, item, field string) []*tandem.Write {
	rec := ItemID(coll, item)
	var out []*tandem.Write
	for _, wr := range w.writes[string(w.cid(coll))] {
		if bytes.Equal(wr.Record, rec) && (field == "*" || wr.Field == field) {
			out = append(out, wr)
		}
	}
	slices.SortFunc(out, func(a, b *tandem.Write) int {
		if a.Dot.Millis != b.Dot.Millis {
			return int(a.Dot.Millis) - int(b.Dot.Millis)
		}
		return int(a.Dot.Counter) - int(b.Dot.Counter)
	})
	return out
}

// WriteOf finds the write that set an item's field to a value.
func (w *World) WriteOf(coll, item, field, value string) *tandem.Write {
	w.T.Helper()
	for _, wr := range w.Writes(coll, item, field) {
		if string(wr.Value) == value || string(wr.Element) == value {
			return wr
		}
	}
	w.T.Fatalf("no write %s.%s = %q", item, field, value)
	return nil
}

func (w *World) user(letter string) *tandem.User {
	u, err := tandem.NewUser(w.secrets[letter])
	w.must(err)
	return u
}

// UID is a user's ID.
func (w *World) UID(letter string) []byte { return w.user(letter).UID }

func (w *World) userOf(uid []byte) string {
	for l := range w.secrets {
		if bytes.Equal(w.UID(l), uid) {
			return l
		}
	}
	return "?"
}

// Tick lets the wall clock move on, so the next write's HLC is later than
// every earlier one's on any device.
func (w *World) Tick() { time.Sleep(3 * time.Millisecond) }

// ---- roster and keys ----

// Epoch is the collection's current epoch-start ID on a device.
func (w *World) Epoch(devName, coll string) []byte { return w.Dev(devName).Epoch(w.cid(coll)) }

// Roles are the head statement's members (name → role) on a device.
func (w *World) Roles(devName, coll string) map[string]string {
	return w.Dev(devName).Members(w.cid(coll))
}

// Dead reports whether a device treats the collection as dead.
func (w *World) Dead(devName, coll string) bool { return w.Dev(devName).Dead(w.cid(coll)) }

// MakesRole: `X.dev makes Y a <role>`.
func (w *World) MakesRole(ownerDev, coll, user, role string) error {
	err := w.Dev(ownerDev).SetRole(ctx, w.cid(coll), w.UID(user), role)
	if err == nil {
		w.members[coll][user] = role
	}
	return err
}

// Rotates: `X.dev asks for a new key`.
func (w *World) Rotates(ownerDev, coll string) error {
	return w.Dev(ownerDev).Rotate(ctx, w.cid(coll))
}

// Pending is another device's unfinished key change, as a device sees it.
func (w *World) Pending(devName, coll string) *device.Note {
	return w.Dev(devName).Pending(w.cid(coll))
}

// Finishes: `X.dev finishes` the pending change it is shown.
func (w *World) Finishes(devName, coll string) error {
	n := w.Pending(devName, coll)
	if n == nil {
		return errors.New("harness: no pending change shown")
	}
	return w.Dev(devName).Finish(ctx, w.cid(coll), n)
}

// Discards: `X.dev discards` the pending change it is shown.
func (w *World) Discards(devName, coll string) error {
	n := w.Pending(devName, coll)
	if n == nil {
		return errors.New("harness: no pending change shown")
	}
	return w.Dev(devName).Discard(ctx, w.cid(coll), n)
}

// Forget takes a user out of the harness's expected members, for a
// collection whose roster changed in a way the harness didn't drive.
func (w *World) Forget(coll, user string) { delete(w.members[coll], user) }

// Keys are the epoch keys a user can find: granted in its inbox, or in
// its home (§11.4, §6.2). The harness reads them with the user's secret.
func (w *World) Keys(user, coll string) map[string][]byte {
	u := w.user(user)
	cid := w.cid(coll)
	out := map[string][]byte{}
	inbox, home := tandem.InboxTag(u.UID), tandem.HomeTag(u.Home)
	for _, m := range w.Hub.Messages() {
		switch m.Tag {
		case inbox:
			pt, err := tandem.OpenInbox(u, m.Envelope)
			if err != nil {
				continue
			}
			var g grantJSON
			if json.Unmarshal(pt, &g) != nil || g.Type != "grant" || g.C != tandem.B64u(cid) {
				continue
			}
			ep, err1 := tandem.ParseB64u(g.Epoch)
			k, err2 := tandem.ParseB64u(g.K)
			if err1 == nil && err2 == nil {
				out[string(ep)] = k
			}
		case home:
			kind, pt, err := tandem.Open(u.Home, m.Tag, m.Envelope)
			if err != nil || kind != tandem.KindRecord {
				continue
			}
			rs, _, err := tandem.ParseKind1(pt, tandem.HomeScope(u.UID))
			if err != nil || rs == nil || !bytes.Equal(rs.Record, tandem.NamedID("membership:"+tandem.B64u(cid))) {
				continue
			}
			for _, wr := range rs.Writes {
				if wr.Field != "keys" || wr.Op != tandem.OpAdd {
					continue
				}
				var e struct{ Epoch, K string }
				if json.Unmarshal(wr.Element, &e) != nil {
					continue
				}
				ep, err1 := tandem.ParseB64u(e.Epoch)
				k, err2 := tandem.ParseB64u(e.K)
				if err1 == nil && err2 == nil {
					out[string(ep)] = k
				}
			}
		}
	}
	return out
}

type grantJSON struct {
	Type, C, At, Epoch, K, Sig string
}

// Granted is the epochs a user was sent grants for.
func (w *World) Granted(user, coll string) map[string]bool {
	u := w.user(user)
	cid := w.cid(coll)
	out := map[string]bool{}
	for _, m := range w.Hub.Messages() {
		if m.Tag != tandem.InboxTag(u.UID) {
			continue
		}
		pt, err := tandem.OpenInbox(u, m.Envelope)
		if err != nil {
			continue
		}
		var g grantJSON
		if json.Unmarshal(pt, &g) == nil && g.Type == "grant" && g.C == tandem.B64u(cid) {
			ep, _ := tandem.ParseB64u(g.Epoch)
			out[string(ep)] = true
		}
	}
	return out
}

// WantCut checks INV-CUT for a removed user: no key for the epoch is
// anywhere they can look, and their devices don't show the epoch's data.
func (w *World) WantCut(user, coll string, epoch []byte) {
	w.T.Helper()
	if k := w.Keys(user, coll)[string(epoch)]; k != nil {
		w.T.Fatalf("%s holds the key of an epoch it was removed before", user)
	}
	for _, n := range w.userDevs(user) {
		if bytes.Equal(w.Epoch(n, coll), epoch) && len(w.Dev(n).State(w.cid(coll))) > 0 {
			w.T.Fatalf("%s shows data of an epoch it was removed before", n)
		}
	}
}

// Messages counts the hub's messages in a collection epoch's channel, read
// with a key some user holds.
func (w *World) Messages(user, coll string, epoch []byte) int {
	k := w.Keys(user, coll)[string(epoch)]
	if k == nil {
		w.T.Fatalf("%s holds no key for that epoch", user)
	}
	tag := tandem.CollectionTag(k, epoch)
	n := 0
	for _, m := range w.Hub.Messages() {
		if m.Tag == tag {
			n++
		}
	}
	return n
}

// ---- lifecycle ----

type crashed struct{ point string }

// Crashes runs op on a device that dies at the named point
// (device.Crash: "1:chosen", "2:journal", "2:note", "3:fragment",
// "3:data", "3:roster", "4:announced", "4:home", "5:grant", "6:cleaned").
// It reports whether the device crashed; a crashed device is down (no
// network, no syncs) until Launches.
func (w *World) Crashes(devName, point string, op func() error) (died bool, err error) {
	w.T.Helper()
	x := w.devs[devName]
	x.d.Crash = func(p string) {
		if p == point {
			panic(crashed{p})
		}
	}
	defer func() {
		x.d.Crash = nil
		if r := recover(); r != nil {
			if _, ok := r.(crashed); !ok {
				panic(r)
			}
			died, err = true, nil
			x.g.down = true
		}
	}()
	return false, op()
}

// Launches starts a device's app again from its stored state, online or
// not; a key change it was making carries on (Resume).
func (w *World) Launches(devName string) error {
	w.T.Helper()
	x := w.devs[devName]
	x.g.Store = w.Hub.Client() // a new connection: the hub index isn't stored, so it reads from the start
	x.g.down = false
	d, err := device.Open(ctx, x.dir, x.g)
	w.must(err)
	x.d = d
	w.hook(x)
	err = d.Resume(ctx)
	if errors.Is(err, errOffline) {
		return nil
	}
	return err
}

// Down stops a device without a word, as a crash, a flat battery or a
// lost phone does, until it Launches.
func (w *World) Down(devName string) { w.Dev(devName); w.devs[devName].g.down = true }

// LosesReplies makes a device's appends land without their answers.
func (w *World) LosesReplies(devName string, lose bool) {
	w.Dev(devName)
	w.devs[devName].g.loseReplies = lose
}

// ---- Mallory's modified client (`M.dev signs <write>`) ----

// DeviceID is a device's ID, from its stored state.
func (w *World) DeviceID(devName string) [8]byte {
	w.T.Helper()
	w.Dev(devName)
	data, err := os.ReadFile(filepath.Join(w.devs[devName].dir, "state.json"))
	w.must(err)
	var s struct{ Device string }
	w.must(json.Unmarshal(data, &s))
	b, err := tandem.ParseB64u(s.Device)
	w.must(err)
	var id [8]byte
	copy(id[:], b)
	return id
}

// Dot makes a dot for a device at a given millis.
func (w *World) Dot(devName string, millis uint64) tandem.Dot {
	return tandem.Dot{Device: w.DeviceID(devName), Millis: millis}
}

// MaxMillis is the largest HLC millis (2^48 − 1, §9.1).
const MaxMillis = 1<<48 - 1

// VV builds a version vector, sorted by device ID as signed.
func VV(dots ...tandem.Dot) tandem.VV {
	v := slices.Clone(dots)
	slices.SortFunc(v, func(a, b tandem.Dot) int { return bytes.Compare(a.Device[:], b.Device[:]) })
	return v
}

// Forged is a write a modified client signs.
type Forged struct {
	Item    string // record of the item in the collection
	Record  []byte // or a raw record ID
	Field   string
	Op      tandem.Op // default assign
	Value   string
	Element string
	Dot     tandem.Dot // default: the device at the current time
	Context tandem.VV
	Base    tandem.VV
	Ref     []byte // default: the head statement the device holds
	// Signer, if set, signs with this user's key while the author field
	// names another (forged attribution). Default: the device's user.
	Signer string
	// Unjudged writes aren't given to the reference model: those every
	// honest device drops before judging (bad signatures).
	Unjudged bool
}

// Sign builds and signs a forged write as a device's user.
func (w *World) Sign(devName, coll string, f Forged) *tandem.Write {
	w.T.Helper()
	user, _, _ := strings.Cut(devName, ".")
	rec := f.Record
	if rec == nil {
		rec = ItemID(coll, f.Item)
	}
	op := f.Op
	if op == 0 {
		op = tandem.OpAssign
	}
	dot := f.Dot
	if dot == (tandem.Dot{}) {
		dot = w.Dot(devName, uint64(time.Now().UnixMilli()))
	}
	ref := f.Ref
	if ref == nil {
		ref = w.Head(devName, coll)
	}
	wr := &tandem.Write{Scope: w.cid(coll), Record: rec, Field: f.Field, Op: op, Dot: dot, Ref: ref,
		Context: f.Context, Base: f.Base}
	if wr.Context == nil {
		wr.Context = tandem.VV{}
	}
	if wr.Base == nil {
		wr.Base = tandem.VV{}
	}
	switch op {
	case tandem.OpAssign:
		wr.Value = []byte(f.Value)
	case tandem.OpAdd, tandem.OpRemove:
		wr.Element = []byte(f.Element)
	}
	w.must(wr.Sign(w.user(user)))
	if f.Signer != "" {
		wr.Author = w.UID(f.Signer)
	}
	if !f.Unjudged {
		w.record(coll, wr)
	}
	return wr
}

func (w *World) record(coll string, wr *tandem.Write) {
	cid := string(w.cid(coll))
	if w.writes[cid] == nil {
		w.writes[cid] = map[string]*tandem.Write{}
	}
	id, _ := wr.ID()
	w.writes[cid][string(id)] = wr
}

// Head is the head statement's ID as a device holds it: the statement
// with the highest n on the chain it follows.
func (w *World) Head(devName, coll string) []byte {
	_, stmts := w.Dev(devName).Roster(w.cid(coll))
	var best *device.StatementView
	for i := range stmts {
		s := &stmts[i]
		if !s.Announced {
			continue
		}
		if best == nil || s.Body.N > best.Body.N {
			best = s
		}
	}
	if best == nil {
		w.T.Fatalf("%s holds no roster for %s", devName, coll)
	}
	return best.ID
}

// Publish puts writes of one record into an epoch's channel as one
// message, sealed with a key the device's user can find (or the key
// given). epoch nil: the device's current epoch.
func (w *World) Publish(devName, coll string, epoch []byte, writes ...*tandem.Write) {
	w.T.Helper()
	user, _, _ := strings.Cut(devName, ".")
	if epoch == nil {
		epoch = w.Epoch(devName, coll)
	}
	k := w.Keys(user, coll)[string(epoch)]
	if k == nil {
		w.T.Fatalf("%s holds no key for that epoch of %s", user, coll)
	}
	w.PublishWith(k, epoch, writes...)
}

// PublishWith seals writes of one record under a given epoch key.
func (w *World) PublishWith(k, epoch []byte, writes ...*tandem.Write) {
	w.T.Helper()
	p, err := tandem.RecordPayload(writes[0].Record, writes)
	w.must(err)
	tag := tandem.CollectionTag(k, epoch)
	env, err := tandem.Seal(k, tandem.KindRecord, tag, p, nil)
	w.must(err)
	_, err = w.Hub.Client().Append(ctx, tag, env)
	w.must(err)
}

// Signs: `M.dev signs <write>` and publishes it in the current epoch.
func (w *World) Signs(devName, coll string, f Forged) *tandem.Write {
	w.T.Helper()
	wr := w.Sign(devName, coll, f)
	w.Publish(devName, coll, nil, wr)
	return wr
}

// SignsStatement: a modified client signs a roster successor of the head
// it holds with its own key, and publishes it in the current channel.
func (w *World) SignsStatement(devName, coll string, edit func(*tandem.Statement)) *tandem.Write {
	w.T.Helper()
	user, _, _ := strings.Cut(devName, ".")
	_, stmts := w.Dev(devName).Roster(w.cid(coll))
	head := w.Head(devName, coll)
	var hb *tandem.Body
	for _, s := range stmts {
		if bytes.Equal(s.ID, head) {
			hb = s.Body
		}
	}
	var ms []tandem.Member
	for _, m := range hb.Members {
		x := tandem.Member{UID: tandem.B64u(m.UID), Role: m.Role, UKa: tandem.B64u(m.UKa), Name: m.Name}
		if m.Via != nil {
			x.Via = tandem.B64u(m.Via)
		}
		ms = append(ms, x)
	}
	st := tandem.Statement{N: hb.N + 1, Key: tandem.EpochKey{N: hb.KeyN, Commit: tandem.B64u(hb.Commit)}, Members: ms}
	if hb.Prev != nil {
		st.Key.Prev = tandem.B64u(hb.Prev)
	}
	if hb.Snapshot != nil {
		st.Snapshot = tandem.B64u(hb.Snapshot)
	}
	edit(&st)
	wr, err := tandem.NextStatement(w.user(user), w.cid(coll), head, st, w.Dot(devName, uint64(time.Now().UnixMilli())))
	w.must(err)
	epoch := w.Epoch(devName, coll)
	k := w.Keys(user, coll)[string(epoch)]
	if k == nil {
		w.T.Fatalf("%s holds no key for the current epoch", user)
	}
	p, err := tandem.RecordPayload(tandem.RosterID, []*tandem.Write{wr})
	w.must(err)
	tag := tandem.CollectionTag(k, epoch)
	env, err := tandem.Seal(k, tandem.KindRecord, tag, p, nil)
	w.must(err)
	_, err = w.Hub.Client().Append(ctx, tag, env)
	w.must(err)
	return wr
}

// Unshared is a device's own writes a key change may have left behind.
func (w *World) Unshared(devName, coll string) []*tandem.Write {
	return w.Dev(devName).Unshared(w.cid(coll))
}

// CheckDead: every online member device treats the collection as dead
// (§9.4); contents may differ (INV-DEAD suspends INV-CONVERGE).
func (w *World) CheckDead(coll string) {
	w.T.Helper()
	for _, n := range w.order {
		x := w.devs[n]
		if _, member := w.members[coll][x.user]; !member || x.g.off || x.g.down {
			continue
		}
		if !x.d.Dead(w.cid(coll)) {
			w.T.Fatalf("%s: %s isn't dead", n, coll)
		}
	}
}

// familyRuns reads TANDEM_FAMILY_RUNS (default def) and
// TANDEM_FAMILY_SEED (replay one seed).
func familyRuns(def int) (runs, first int) {
	runs, first = def, 1
	if n, err := strconv.Atoi(os.Getenv("TANDEM_FAMILY_RUNS")); err == nil {
		runs = n
	}
	if s, err := strconv.Atoi(os.Getenv("TANDEM_FAMILY_SEED")); err == nil {
		first, runs = s, 1
	}
	return runs, first
}
