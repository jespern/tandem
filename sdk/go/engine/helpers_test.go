package engine

import (
	"bytes"
	"context"
	"os"
	"testing"

	tandem "github.com/jespern/tandem/sdk/go"
)

var bg = context.Background()

var account = MailboxAccount{Host: "imap.example.org", Port: 993, User: "hub@example.org", Password: "app-password"}

func newKeys(t *testing.T) *tandem.DeviceKeys {
	t.Helper()
	k, err := tandem.NewDeviceKeys()
	must(t, err)
	return k
}

func skipArgon(t *testing.T) {
	if os.Getenv("TANDEM_SKIP_ARGON2_GIB") == "1" {
		t.Skip("TANDEM_SKIP_ARGON2_GIB=1")
	}
}

// try returns v, or fails the test (by panicking) on an error.
func try[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func open(t *testing.T, f *Founding, keys *tandem.DeviceKeys, store *Store, hub tandem.PushStore) *Device {
	t.Helper()
	if store == nil {
		store = MemoryStore()
	}
	d, err := Open(f.Genesis, f.DirectoryKey, keys, store, hub)
	must(t, err)
	t.Cleanup(d.Close)
	return d
}

// found founds a hub on a memory hub and publishes it from the creator's device.
func found(t *testing.T, label string, store *Store) (*MemoryHub, *Founding, *tandem.DeviceKeys, *Device) {
	t.Helper()
	hub, keys := NewMemoryHub(), newKeys(t)
	f := try(Found(account, keys, label, nil))
	d := open(t, f, keys, store, hub.Connect())
	must(t, d.Publish(bg, f))
	return hub, f, keys, d
}

func join(t *testing.T, admin *Device, f *Founding, hub *MemoryHub, name string, keys *tandem.DeviceKeys) *Device {
	t.Helper()
	if keys == nil {
		keys = newKeys(t)
	}
	link := try(admin.Invite(bg, name, account, f.ManagementSeed, 0))
	d := try(Join(bg, link, keys, name+"’s phone", MemoryStore(), hub.Connect()))
	t.Cleanup(d.Close)
	return d
}

func syncAll(t *testing.T, ds ...*Device) {
	t.Helper()
	for _, d := range ds {
		try(d.Sync(bg))
	}
}

func me(t *testing.T, d *Device) *Me {
	t.Helper()
	m := d.Me()
	if m == nil {
		t.Fatal("not on the hub")
	}
	return m
}

func value_(d *Device, list, record []byte, field string) string {
	v, _ := d.Value(list, record, field)
	return string(v)
}

func homeValue(d *Device, name, field string) []byte {
	v, _ := d.HomeValue(name, field)
	return v
}

func titles(d *Device, list []byte) map[string]bool {
	out := map[string]bool{}
	for _, r := range d.Records(list) {
		if v, ok := d.Value(list, r, "title"); ok {
			out[string(v)] = true
		}
	}
	return out
}

func set(xs ...string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func eq(a, b []byte) bool { return bytes.Equal(a, b) }

// epochKey is a list's current epoch and key on a device.
func epochKey(d *Device, list []byte) (epoch, key []byte) {
	defer d.read()()
	c := d.collections[string(list)]
	e := c.epochOf(c.head())
	return e, c.keys[string(e)]
}

// rawAppend puts bytes on the hub as someone with the mailbox password could.
func rawAppend(t *testing.T, hub *MemoryHub, channel string, body []byte) {
	t.Helper()
	try(hub.Connect().Append(bg, channel, body))
}
