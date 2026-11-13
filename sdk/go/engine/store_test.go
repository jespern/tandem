package engine

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

func TestStoreValuesObjectsMessagesWritesJournal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.sqlite")
	manager, _ := tandem.ManagerKeys(bytes.Repeat([]byte{1}, 32))
	rev := newControl("revocation", tandem.Fields{"hub": publicSign(manager), "genesis": bytes.Repeat([]byte{2}, 32),
		"target": bytes.Repeat([]byte{3}, 32), "retained": [][]byte{}})
	if err := signControl(rev, "sig", manager.Signing); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Transaction(func() error {
		must(t, s.SetValue("cursor", []byte("12")))
		if ok, _ := s.PutObject(rev); !ok {
			t.Fatal("first put")
		}
		if ok, _ := s.PutObject(rev); ok {
			t.Fatal("second put")
		}
		must(t, s.NoteMessage("7", "abc", []byte{9}))
		if ok, _ := s.PutWrite([]byte{1}, []byte{2}, []byte{3}, []byte{4}, []byte{5}); !ok {
			t.Fatal("write")
		}
		return s.SetJournal("rotate:x", []byte{6, 7})
	})
	must(t, err)
	if err := s.Transaction(func() error { s.SetValue("cursor", []byte{0}); return errors.New("crash") }); err == nil {
		t.Fatal("no error")
	}
	s.Close()

	// Reopened: what was committed is there, the rolled-back change is not.
	s, err = OpenStore(file)
	must(t, err)
	defer s.Close()
	if v, _ := s.Value("cursor"); string(v) != "12" {
		t.Fatal(v)
	}
	objs, _ := s.Objects("revocation")
	if len(objs) != 1 || !bytes.Equal(oid(objs[0]), oid(rev)) {
		t.Fatal(objs)
	}
	if ids, _ := s.MessagesWithHash([]byte{9}); !slices.Equal(ids, []string{"7"}) {
		t.Fatal(ids)
	}
	if u, _ := s.Unapplied(); len(u) != 1 || u[0].ID != "7" {
		t.Fatal(u)
	}
	must(t, s.MarkApplied("7"))
	if u, _ := s.Unapplied(); len(u) != 0 {
		t.Fatal(u)
	}
	if w, _ := s.Writes([]byte{2}, []byte{3}); len(w) != 1 || !bytes.Equal(w[0].Payload, []byte{5}) {
		t.Fatal(w)
	}
	if r, _ := s.Records([]byte{2}); len(r) != 1 || !bytes.Equal(r[0], []byte{3}) {
		t.Fatal(r)
	}
	if j, ok := s.Journal("rotate:x"); !ok || !bytes.Equal(j, []byte{6, 7}) {
		t.Fatal(j)
	}
	if steps, _ := s.JournalSteps(); !slices.Equal(steps, []string{"rotate:x"}) {
		t.Fatal(steps)
	}
	must(t, s.ClearJournal("rotate:x"))
	if _, ok := s.Journal("rotate:x"); ok {
		t.Fatal("journal")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
