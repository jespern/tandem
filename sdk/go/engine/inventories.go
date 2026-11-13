package engine

import (
	"bytes"
	"strconv"
	"strings"

	tandem "github.com/jespern/tandem/sdk/go"
)

// Snapshot inventories as they travel (§8.3, §10.5–10.6): split into kind-1 fragments in the
// new epoch's channel, kept until a complete assembly matches the epoch's signed commitment.

// fragmentSize keeps fragments under the 64 KiB a message should keep to.
const fragmentSize = 48 * 1024

// fragments is the kind-1 payloads that carry inventory for the epoch.
func fragments(inv tandem.Inventory, epoch []byte) ([][]byte, error) {
	b, err := inv.Bytes()
	if err != nil {
		return nil, err
	}
	parts := max(1, (len(b)+fragmentSize-1)/fragmentSize)
	var out [][]byte
	for part := 0; part < parts; part++ {
		slice := b[min(len(b), part*fragmentSize):min(len(b), (part+1)*fragmentSize)]
		f := tandem.NewJSONObject().B64u("epoch", epoch).Int("part", uint64(part)).Int("parts", uint64(parts)).B64u("bytes", slice).Bytes()
		out = append(out, tandem.NewJSONObject().Raw("inventory", f).Bytes())
	}
	return out, nil
}

func (d *Device) keepInventory(inv tandem.Inventory, epoch []byte) error {
	b, err := inv.Bytes()
	if err != nil {
		return err
	}
	d.inventories[string(epoch)] = inv
	return d.store.SetValue("inv."+hexs(epoch), b)
}

// receiveFragment takes one fragment of the inventory for epoch, whose start commits to
// commitment. Every distinct version of a part is kept, so a bogus fragment can't block the
// real one; it reports whether the inventory is now complete.
func (d *Device) receiveFragment(f *tandem.InventoryFragment, epoch, commitment []byte) (bool, error) {
	if _, have := d.inventories[string(epoch)]; have || !bytes.Equal(f.Epoch, epoch) || f.Parts > 4096 {
		return false, nil
	}
	prefix := "frag." + hexs(epoch) + "." + strconv.Itoa(f.Parts) + "."
	if err := d.store.SetValue(prefix+strconv.Itoa(f.Part)+"."+hexs(tandem.H(f.Bytes)), f.Bytes); err != nil {
		return false, err
	}
	vs, err := d.store.Values(prefix)
	if err != nil {
		return false, err
	}
	candidates := make([][][]byte, f.Parts)
	for _, v := range vs {
		rest := strings.TrimPrefix(v.Name, prefix)
		part, err := strconv.Atoi(rest[:strings.IndexByte(rest, '.')])
		if err != nil || part >= f.Parts {
			continue
		}
		candidates[part] = append(candidates[part], v.Value)
	}
	combos := 1
	for _, c := range candidates {
		if len(c) == 0 {
			return false, nil
		}
		combos *= len(c)
	}
	// One part (the usual case) tries every version; several parts bound the search.
	if f.Parts > 1 && combos > 4096 {
		return false, nil
	}
	var found tandem.Inventory
	var search func(i int, acc []byte)
	search = func(i int, acc []byte) {
		if found != nil {
			return
		}
		if i == len(candidates) {
			if bytes.Equal(tandem.H(acc), commitment) {
				if inv, err := tandem.ParseInventory(acc); err == nil {
					found = inv
				}
			}
			return
		}
		for _, c := range candidates[i] {
			search(i+1, append(bytes.Clone(acc), c...))
		}
	}
	search(0, nil)
	if found == nil {
		return false, nil
	}
	if err := d.keepInventory(found, epoch); err != nil {
		return false, err
	}
	all, err := d.store.Values("frag." + hexs(epoch) + ".")
	if err != nil {
		return false, err
	}
	for _, v := range all {
		if err := d.store.RemoveValue(v.Name); err != nil {
			return false, err
		}
	}
	return true, nil
}

// inventoryOf builds an inventory from record IDs and their write IDs.
func inventoryOf(entries map[string]map[string]bool) tandem.Inventory {
	inv := tandem.Inventory{}
	for r, ids := range entries {
		if len(ids) == 0 {
			continue
		}
		var ws [][]byte
		for _, id := range sortedKeys(ids) {
			ws = append(ws, []byte(id))
		}
		inv[r] = ws
	}
	return inv
}
