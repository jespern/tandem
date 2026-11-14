package engine

import (
	"bytes"
	"testing"
	"time"

	tandem "github.com/jespern/tandem/sdk/go"
)

// A member posts two versions of every part of a 64-part inventory: the count of assemblies
// (2^64) must not wrap past the bound, or the search runs forever under the device's locks.
func TestInventorySearchBoundDoesntOverflow(t *testing.T) {
	_, _, _, bob := found(t, "Bob’s iPhone", nil)
	epoch, commitment := random(32), random(32)
	done := make(chan error, 1)
	go func() { // without the device's locks, so a failure can still close it
		for v := 0; v < 2; v++ {
			for part := 0; part < 64; part++ {
				f := &tandem.InventoryFragment{Epoch: epoch, Part: part, Parts: 64, Bytes: random(8)}
				if ok, err := bob.receiveFragment(f, epoch, commitment); err != nil || ok {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the inventory search didn't stop at its bound")
	}
}

// The bound still lets a real assembly through a few bogus versions.
func TestInventoryAssemblesPastBogusVersions(t *testing.T) {
	_, _, _, bob := found(t, "Bob’s iPhone", nil)
	inv := tandem.Inventory{string(random(16)): {random(32), random(32)}, string(random(16)): {random(32)}}
	b := try(inv.Bytes())
	epoch, half := random(32), len(b)/2
	end := bob.begin()
	defer end()
	for part, want := range [][]byte{b[:half], b[half:]} {
		for _, bs := range [][]byte{random(len(want)), want, random(3)} {
			f := &tandem.InventoryFragment{Epoch: epoch, Part: part, Parts: 2, Bytes: bs}
			ok := try(bob.receiveFragment(f, epoch, tandem.H(b)))
			if ok != (part == 1 && bytes.Equal(bs, want)) {
				t.Fatal("assembled at the wrong fragment", part, ok)
			}
			if ok {
				return
			}
		}
	}
	t.Fatal("never assembled")
}
