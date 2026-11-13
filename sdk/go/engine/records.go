package engine

import (
	"bytes"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Records in the device's maps are never changed in place: a change makes a new one, so an
// Item handed to the app stays as it was.

func emptyRecord(hub, scope, id []byte) *tandem.Record {
	r, err := tandem.NewRecord(hub, scope, id)
	if err != nil {
		panic(err)
	}
	return r
}

// merged is old (or an empty record) with entries merged, as a new record.
func merged(old *tandem.Record, hub, scope, id []byte, entries ...tandem.Entry) (*tandem.Record, error) {
	r, err := tandem.NewRecord(hub, scope, id)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if err := r.Merge(old.Entries()...); err != nil {
			return nil, err
		}
	}
	if err := r.Merge(entries...); err != nil {
		return nil, err
	}
	return r, nil
}

func keptWrites(r *tandem.Record) []*tandem.Write {
	if r == nil {
		return nil
	}
	var out []*tandem.Write
	for _, e := range r.Entries() {
		out = append(out, e.W)
	}
	return out
}

func keptIDs(r *tandem.Record) [][]byte {
	if r == nil {
		return nil
	}
	var out [][]byte
	for _, e := range r.Entries() {
		out = append(out, e.ID)
	}
	return out
}

func keptEntry(r *tandem.Record, id []byte) *tandem.Entry {
	if r == nil {
		return nil
	}
	for _, e := range r.Entries() {
		if bytes.Equal(e.ID, id) {
			return &e
		}
	}
	return nil
}

func value(r *tandem.Record, field string) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	return r.Value(field)
}

func elements(r *tandem.Record, field string) [][]byte {
	if r == nil {
		return nil
	}
	return r.Elements(field)
}

// storedWrite reads the one write of a stored payload.
func storedWrite(payload, hub, scope []byte) *tandem.Write {
	rs, _, err := tandem.ParseKind1(payload, hub, scope)
	if err != nil || rs == nil || len(rs.Writes) == 0 {
		return nil
	}
	return rs.Writes[0]
}

func onePayload(record []byte, w *tandem.Write) ([]byte, error) {
	return tandem.RecordPayload(record, []*tandem.Write{w})
}

func entryOf(w *tandem.Write, epoch []byte, n uint64) (tandem.Entry, error) {
	return tandem.NewEntry(w, epoch, int64(n))
}
