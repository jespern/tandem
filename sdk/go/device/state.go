package device

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	tandem "github.com/jespern/tandem/sdk/go"
	"github.com/jespern/tandem/sdk/go/store"
)

// Durable state. Everything the device has accepted is written with fsync
// after each change; the hub index isn't, since reopening resyncs from the
// hub (merging is idempotent). The local file is the device's own, not
// protocol JSON, so encoding/json writes and reads it.

type savedWrite struct {
	Epoch string `json:"epoch"` // the channel's epoch (b64u), or "" for home and statements
	Scope string `json:"scope"`
	P     string `json:"p"` // a one-write record payload
}

type savedCol struct {
	ID        string            `json:"id"`
	Stmts     []savedWrite      `json:"stmts"`
	Announced []string          `json:"announced"`
	Data      []savedWrite      `json:"data"`
	Held      []savedWrite      `json:"held"`
	Invs      map[string]string `json:"invs"`
	Replaced  []string          `json:"replaced"`
	Own       []savedWrite      `json:"own"`
	Resigned  []string          `json:"resigned"`
	Unshared  []string          `json:"unshared"`
	Dirty     []string          `json:"dirty"`
}

type saved struct {
	Name   string       `json:"name"`
	Secret string       `json:"secret"` // test devices only; a real one keeps it in the Keychain (§5.3)
	Device string       `json:"device"`
	HLC    [2]uint64    `json:"hlc"`
	Home   []savedWrite `json:"home"`
	Cols   []savedCol   `json:"cols"`
}

func encodeWrite(w *tandem.Write, epoch []byte) savedWrite {
	p, _ := tandem.RecordPayload(w.Record, []*tandem.Write{w})
	return savedWrite{Epoch: tandem.B64u(epoch), Scope: tandem.B64u(w.Scope), P: string(p)}
}

func decodeWrite(s savedWrite) (*tandem.Write, []byte, error) {
	scope, err := tandem.ParseB64u(s.Scope)
	if err != nil {
		return nil, nil, err
	}
	epoch, _ := tandem.ParseB64u(s.Epoch)
	rs, _, err := tandem.ParseKind1([]byte(s.P), scope)
	if err != nil || rs == nil || len(rs.Writes) != 1 {
		return nil, nil, errors.New("device: bad saved write")
	}
	return rs.Writes[0], epoch, nil
}

func (d *Device) save() error {
	s := saved{Name: d.Name, Secret: tandem.B64u(d.u.Secret), Device: tandem.B64u(d.id[:]), HLC: [2]uint64{d.hlc.Millis, uint64(d.hlc.Counter)}}
	for _, r := range d.recs {
		for _, e := range r.Kept {
			s.Home = append(s.Home, encodeWrite(e.W, nil))
		}
	}
	for _, c := range d.cols {
		sc := savedCol{ID: tandem.B64u(c.id), Invs: map[string]string{}}
		for _, w := range c.stmts {
			sc.Stmts = append(sc.Stmts, encodeWrite(w, nil))
		}
		if c.roster != nil {
			for _, id := range c.roster.Announced() {
				sc.Announced = append(sc.Announced, tandem.B64u(id))
			}
		}
		for ep, recs := range c.recs {
			for _, r := range recs {
				for _, e := range r.Kept {
					sc.Data = append(sc.Data, encodeWrite(e.W, []byte(ep)))
				}
			}
		}
		for _, h := range c.held {
			sc.Held = append(sc.Held, encodeWrite(h.W, h.Epoch))
		}
		for ep, inv := range c.invs {
			b, _ := inv.Bytes()
			sc.Invs[tandem.B64u([]byte(ep))] = tandem.B64u(b)
		}
		for id := range c.replaced {
			sc.Replaced = append(sc.Replaced, tandem.B64u([]byte(id)))
		}
		for _, h := range c.own {
			sc.Own = append(sc.Own, encodeWrite(h.W, h.Epoch))
		}
		for id := range c.resigned {
			sc.Resigned = append(sc.Resigned, tandem.B64u([]byte(id)))
		}
		for id := range c.unshared {
			sc.Unshared = append(sc.Unshared, tandem.B64u([]byte(id)))
		}
		for key := range c.dirty {
			sc.Dirty = append(sc.Dirty, tandem.B64u([]byte(key)))
		}
		s.Cols = append(s.Cols, sc)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(d.dir, "state.json"), data)
}

// writeFileSync replaces a file atomically and durably: write a temporary
// file, fsync it, rename it over the old one, fsync the directory.
func writeFileSync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Open reopens a device from its directory, as after a restart or a crash.
// Call Resume to carry on a key change it was making.
func Open(ctx context.Context, dir string, hub store.Store) (*Device, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var s saved
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	secret, err := tandem.ParseB64u(s.Secret)
	if err != nil {
		return nil, err
	}
	u, err := tandem.NewUser(secret)
	if err != nil {
		return nil, err
	}
	d := newDevice(dir, s.Name, u, hub)
	id, _ := tandem.ParseB64u(s.Device)
	copy(d.id[:], id)
	d.hlc = tandem.Dot{Device: d.id, Millis: s.HLC[0], Counter: uint16(s.HLC[1])}
	for _, sw := range s.Home {
		w, _, err := decodeWrite(sw)
		if err != nil {
			return nil, err
		}
		r, ok := d.recs[string(w.Record)]
		if !ok {
			r = tandem.NewRecord(w.Record)
			d.recs[string(w.Record)] = r
		}
		e, _ := tandem.NewEntry(w, []byte(homeEpoch), 0)
		r.Merge(e)
	}
	for _, sc := range s.Cols {
		cid, _ := tandem.ParseB64u(sc.ID)
		c := d.colFor(cid)
		for _, sw := range sc.Stmts { // the genesis first: it starts the roster
			w, _, err := decodeWrite(sw)
			if err == nil && w.Scope != nil && string(w.Ref) == string(make([]byte, 32)) {
				d.addStatement(c, w, nil)
			}
		}
		for _, sw := range sc.Stmts {
			if w, _, err := decodeWrite(sw); err == nil {
				d.addStatement(c, w, nil)
			}
		}
		for _, a := range sc.Announced {
			if id, err := tandem.ParseB64u(a); err == nil && c.roster != nil {
				c.roster.Announce(id)
			}
		}
		for ep, b := range sc.Invs {
			e, _ := tandem.ParseB64u(ep)
			bb, _ := tandem.ParseB64u(b)
			if inv, err := tandem.ParseInventory(bb); err == nil {
				c.invs[string(e)] = inv
			}
		}
		for _, id := range sc.Replaced {
			if b, err := tandem.ParseB64u(id); err == nil {
				c.replaced[string(b)] = true
			}
		}
		for _, sw := range sc.Data {
			if w, ep, err := decodeWrite(sw); err == nil && c.roster != nil {
				d.mergeData(c, ep, w)
			}
		}
		for _, sw := range sc.Own {
			if w, ep, err := decodeWrite(sw); err == nil {
				id, _ := w.ID()
				c.own[string(id)] = heldWrite{w, ep}
			}
		}
		for _, x := range sc.Resigned {
			if b, err := tandem.ParseB64u(x); err == nil {
				c.resigned[string(b)] = true
			}
		}
		for _, x := range sc.Dirty {
			if b, err := tandem.ParseB64u(x); err == nil {
				c.dirty[string(b)] = true
			}
		}
		for _, x := range sc.Unshared {
			if b, err := tandem.ParseB64u(x); err == nil {
				c.unshared[string(b)] = true
			}
		}
		for _, sw := range sc.Held {
			if w, ep, err := decodeWrite(sw); err == nil {
				c.held = append(c.held, heldWrite{w, ep})
			}
		}
	}
	jdata, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err == nil {
		var j journal
		if err := json.Unmarshal(jdata, &j); err != nil {
			return nil, err
		}
		d.journal = &j
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return d, nil
}
