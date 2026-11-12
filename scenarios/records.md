# Records, writes and merge

Scope: creating, editing and deleting records; how concurrent writes merge (registers, siblings, sets, tombstones, unknown fields, clocks, `context` and `base` across epochs, the 1,024-entry vector limit); when a data write is valid and eligible in an epoch; how Liszto's items map onto all of it; and what a member with a modified client can do to a collection's data.

Conventions for this file: unless a scenario says otherwise, `Groceries` is owned by J with A as writer and is in epoch `E4` (labels as in `rotation.md`: the protocol knows an epoch only by its starting statement's ID, §7.1). It holds items `milk`, `eggs` and `bread`, created by A.phone, each with Liszto's fields (§17): registers `title`, `notes`, `parentID`, `sortKey`, `completedAt`, and the set `tags`. `House` is a second collection owned by J with A as writer. A step names the write it creates in parentheses (`j1`). **Kept** is the set of valid writes to a field (or to one set element) that no other valid write supersedes (§9.3). **Displayed** is the kept write greatest by `(epoch, hlc, author, deviceID, writeID)` (§9.2). **Reported** means Tandem tells the app the field has concurrent values (§9.2, §13.2). Vectors are written `{device: hlc}`; where only order matters, HLCs are small integers. `MAX` is an HLC millis of 2^48 − 1.

---

## Real-world

### RC-001 Two people ticking off a shopping list in the supermarket
- **Kind:** real-world
- **Design:** §1.1, §9.2, §9.5, §10.2, §10.3, §10.7, §17
- **Binding:** any
- **Setup:** `Groceries` holds 20 unticked items. `J.phone` and `A.phone` are online with Liszto in the foreground; every other device has quit.
- **Steps:**
  1. `A.phone sets item01…item10.completedAt = now`, one every 5 s.
  2. During the same minute, `J.phone sets item11…item20.completedAt = now`, one every 5 s.
  3. `J.phone adds Milk to Groceries`; within the same second `A.phone adds Milk to Groceries`.
  4. `wait 30 s`.
- **Expect:**
  - Each tick appears on the other phone within 5 s of being made.
  - Both phones show all 20 items ticked and two separate unticked `Milk` items with different record IDs.
  - Tandem reports no field in conflict on either phone.
  - After step 4 the hub holds exactly one kind-1 message per data record in the epoch-4 channel (22), and neither phone appends anything further.
- **Invariants:** INV-CONVERGE, INV-QUIESCE, INV-HUB-BLIND
- **Notes:** §1.1 says "within seconds" without a number; 5 s is the harness threshold. Tandem doesn't deduplicate records created concurrently, so two `Milk` items is the expected result.

### RC-002 Renaming an item while the other person ticks it off
- **Kind:** real-world
- **Design:** §9.2, §9.3, §10.7, §17
- **Binding:** any
- **Setup:** Default. All devices synced.
- **Steps:**
  1. `net: partition J.mac`, `net: partition A.phone`.
  2. `J.mac sets bread.title = "Sourdough"` (`j`).
  3. `A.phone sets bread.completedAt = 10:02` (`a`).
  4. `net: reset`; `J.mac syncs`, `A.phone syncs`; `wait 30 s`.
- **Expect:**
  - Both devices show `bread` titled "Sourdough" and ticked at 10:02.
  - Kept: `bread.title` = {`j`}, `bread.completedAt` = {`a`}. Nothing is reported.
  - Right after step 4 the hub may hold two messages for `bread`, neither dominating the other. Within 30 s it holds exactly one, containing both `j` and `a`.
- **Invariants:** INV-CONVERGE, INV-QUIESCE, INV-GC-SAFE

### RC-003 Both people tick off the same item
- **Kind:** real-world
- **Design:** §9.1, §9.2, §17
- **Binding:** any
- **Setup:** Default. `A.phone clock = +1 s` relative to `J.phone`.
- **Steps:**
  1. `J.phone offline`, `A.phone offline`.
  2. `J.phone sets milk.completedAt = 10:00:00` (`j`); `A.phone sets milk.completedAt = 10:00:01` (`a`), at the same real moment.
  3. Both online; both sync.
  4. `J.phone sets milk.completedAt = ""` (unticks, `j2`); both sync.
- **Expect:**
  - After step 3: kept {`j`, `a`}; displayed `a`; both phones show `milk` ticked at 10:00:01. Tandem reports `milk.completedAt` with one other value (`j`, author J).
  - Neither phone writes `completedAt` on its own after step 3 (Liszto resolves only `notes` automatically, §17). The hub's message for `milk` holds both writes.
  - After step 4: kept {`j2`}; nothing reported; `milk` unticked on both.
- **Invariants:** INV-CONVERGE, INV-QUIESCE

### RC-004 Jesper renames the list while Aimee ticks items off
- **Kind:** real-world
- **Design:** §7.3, §8.3, §9.2, §17
- **Binding:** any
- **Setup:** Default. `J.phone` offline.
- **Steps:**
  1. `J.phone` renames `Groceries` to "Shopping" (a `_meta` write, hlc T).
  2. `J.mac` renames `Groceries` to "Food" (hlc T + 60 s), not having seen step 1.
  3. `A.phone sets milk.completedAt = now`; `A.phone sets eggs.completedAt = now`.
  4. `J.phone online`; every device syncs.
- **Expect:**
  - `_meta.name` holds two siblings; every device shows "Food" (greater `(epoch, hlc, …)`) and reports "Shopping".
  - `milk` and `eggs` are ticked on every device.
  - No roster statement and no key change: `_meta` is an ordinary data record (§8.1).
  - `A.phone` offers no rename action; a rename from a modified client is rejected (§8.3).
- **Invariants:** INV-CONVERGE, INV-AUTHZ, INV-MONOTONE-EPOCH

### RC-005 Offline edits on a plane
- **Kind:** real-world
- **Design:** §1.1, §9.2, §9.5, §10.7, §17
- **Binding:** any
- **Setup:** Default. `eggs.notes` = "Large". `J.mac` and `A.mac` have quit, so only the two phones run Liszto.
- **Steps:**
  1. `J.phone offline` (boarding).
  2. `J.phone sets milk.title = "Oat milk"`; `J.phone sets eggs.notes = "Large, free range"` (`jN`); `J.phone adds Butter to Groceries`.
  3. `A.phone sets milk.completedAt = now`; `A.phone sets eggs.notes = "Large, organic"` (`aN`); `A.phone deletes bread`.
  4. Later in real time, `J.phone sets bread.title = "Rye bread"` (`jB`).
  5. `A.phone quits`.
  6. `J.phone online`; `J.phone syncs` (landing).
  7. `A.phone launches`; `A.phone syncs`.
- **Expect:**
  - `milk` shows "Oat milk", ticked, on both phones; nothing reported for it.
  - `Butter` appears on `A.phone`.
  - `bread` is absent on both phones. The hub's state for `bread` holds A's `delete` and `jB`.
  - At step 6 `J.phone` finds kept {`jN`, `aN`} for `eggs.notes` and Liszto writes one merged value `m` whose context covers both. After step 7 kept is {`m`} on both phones, and its text contains both "free range" and "organic".
  - After quiescence the hub holds one message per record.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS, INV-QUIESCE
- **Notes:** The tombstone wins over `jB` by design (§9.2). Whether J is told that "Rye bread" landed on a deleted item is RC-038.

### RC-006 Both edit a title within the same second
- **Kind:** real-world
- **Design:** §9.2, §13.2, §17
- **Binding:** any
- **Setup:** Default. Both phones online. `net: latency 2000` for both.
- **Steps:**
  1. `J.phone sets milk.title = "Milk 1L"` (`j`) at T; `A.phone sets milk.title = "Milk 2L"` (`a`) at T + 300 ms.
  2. Both sync.
  3. `J.phone sets milk.title = "Milk"` (`j2`); both sync.
- **Expect:**
  - After step 2: kept {`j`, `a`}; both phones show "Milk 2L". Each phone reports `milk.title` with one other value, "Milk 1L" by J.
  - Neither phone writes `title` on its own.
  - After step 3: kept {`j2`}; nothing reported; both show "Milk".
- **Invariants:** INV-CONVERGE, INV-QUIESCE

### RC-007 Tags: an add wins over a concurrent remove
- **Kind:** real-world
- **Design:** §9.2 (set), §9.3, §17
- **Binding:** any
- **Setup:** Default. `milk.tags` = {dairy}, from A.phone's add `d0`.
- **Steps:**
  1. `J.phone offline`.
  2. `A.phone tags milk -dairy` (`r1`, context covers `d0`).
  3. `J.phone tags milk -dairy` (`j1`), then `J.phone tags milk +dairy` (`j2`; context covers `d0` and `j1`).
  4. `A.phone tags milk +organic` (`o1`); `J.phone tags milk +organic` (`o2`).
  5. `J.phone online`; all sync.
  6. `A.phone tags milk -organic` (`r2`); all sync.
- **Expect:**
  - After step 5: `milk.tags` = {dairy, organic} everywhere. Kept for `dairy` = {`r1`, `j2`}; for `organic` = {`o1`, `o2`}.
  - After step 6: kept for `organic` = {`r2`}; `milk.tags` = {dairy}.
  - Nothing is reported for `tags`; sets have no siblings to show.
- **Invariants:** INV-CONVERGE
- **Notes:** §9.1 and §10.5 settle the bytes: set elements, like field names, are compared as UTF-8 bytes, so "Café" typed on iOS (NFC) and pasted on macOS (NFD), or "Dairy" and "dairy", are distinct elements. Normalising tags is Liszto's job (§17 doesn't yet say how).

### RC-008 Undo after delete
- **Kind:** real-world
- **Design:** §9.2 (delete), §9.5, §10.7, §17
- **Binding:** any
- **Setup:** Default. `milk` has `notes` "2 cartons", `tags` {dairy}, `sortKey` "a5". `eggs` has `notes` "Large".
- **Steps:**
  1. `J.mac deletes milk`; 0.5 s later, before the ~1 s debounce publishes, J presses ⌘Z.
  2. `J.mac syncs`; `A.phone syncs`.
  3. `A.phone offline`; `A.phone sets eggs.notes = "Organic"` (`aN`).
  4. `J.mac deletes eggs`; `J.mac syncs`; 10 s later J presses ⌘Z; `J.mac syncs`.
  5. `A.phone online`; `A.phone syncs`.
- **Expect:**
  - After step 2: `A.phone` shows exactly one "Milk", with `notes`, `tags`, `sortKey` and `completedAt` equal to the original. Because a tombstone is sticky (§9.2), it is a new record with a new ID, unless the unpublished delete may be discarded (GAP below).
  - After step 5: `A.phone` shows exactly one "Eggs" item, a new record with `notes` "Large". The old `eggs` record is tombstoned on the hub and holds `aN`. "Organic" appears nowhere.
  - The restored records' writes are authored by J; Liszto's "added by" shows Jesper, no longer Aimee.
- **Invariants:** INV-CONVERGE, INV-UX
- **Notes:** ⌘Z is outside the step vocabulary. GAP (G-29): §10.7 coalesces *publishes*, but nothing says whether a signed, unpublished write (step 1's delete) may be withdrawn rather than published. If it may, undo inside the debounce keeps the record ID; otherwise it never does. GAP (G-30): undo-by-recreation changes record identity and authorship (against goal 5, attribution) and silently drops concurrent edits made on other devices (`aN`); §17 says nothing about how Liszto should restore an item or rewrite references to it (`parentID`, RC-010).

### RC-009 Moving an item while the other person edits it
- **Kind:** real-world
- **Design:** §9.2 (delete), §9.5, §17
- **Binding:** any
- **Setup:** Default plus item `batteries` in `Groceries`.
- **Steps:**
  1. `A.phone offline`.
  2. `J.mac moves batteries to House` (creates `batteries'` in `House`, then deletes `batteries` in `Groceries`).
  3. `A.phone sets batteries.title = "AA batteries"` (`a`).
  4. `A.phone online`; all sync.
- **Expect:**
  - Every device shows "Batteries" in `House` and nothing in `Groceries`.
  - The hub's `Groceries` state for `batteries` holds J's `delete` and `a`. "AA batteries" is shown nowhere.
- **Invariants:** INV-CONVERGE, INV-UX
- **Notes:** GAP (G-31): delete-wins is deliberate (§9.2), but for a move the losing edit is a live user's change, made in good faith, to an item that still exists elsewhere. Tandem gives the app no signal that a write landed on a tombstoned record, so Liszto can't tell A her edit was dropped (INV-UX).

### RC-010 A sub-item added under a parent that is being deleted
- **Kind:** real-world
- **Design:** §9.2 (delete), §9.5, §17
- **Binding:** any
- **Setup:** `Groceries` holds `party` with sub-items `cake` and `balloons` (`parentID` = `party`).
- **Steps:**
  1. `A.phone offline`.
  2. `J.mac deletes party` (Liszto also deletes `cake` and `balloons`, one `delete` each).
  3. `A.phone adds candles to Groceries` with `parentID` = `party`.
  4. `A.phone online`; all sync.
- **Expect:**
  - `party`, `cake`, `balloons` are absent on every device.
  - `candles` is a live record whose `parentID` names a deleted record.
  - Every device shows `candles` in the same place, and no device writes to `candles` unless exactly one repair write results.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** That Liszto deletes children with their parent is an assumption, flagged for the harness. GAP (G-32): §17 maps `parentID` to a register but says nothing about a sub-item whose parent is deleted (show at top level, hide, delete). If Liszto repairs orphans by writing, every device that sees one repairs it at once (the RC-039 problem). After an undo (RC-008) the parent comes back under a new ID and the orphan still points at the old one.

### RC-011 The share extension saves a link while the app edits, during a key change
- **Kind:** real-world
- **Design:** §9.1, §11.6, §17 (extension direct-save)
- **Binding:** any
- **Setup:** Default. `J.phone` app in the foreground.
- **Steps:**
  1. `J.phone sets milk.title = "Milk 2L"`; at the same moment the share extension on `J.phone` saves a URL to `Groceries` as a new item `link1`.
  2. `J.phone syncs`; `A.phone syncs`.
  3. `J.phone quits`. `J.mac` asks for a new key for Groceries (`E5`).
  4. The share extension on `J.phone` saves a URL as item `link2`, without the app running.
  5. `J.mac syncs`; `A.phone syncs`; `J.phone launches`.
- **Expect:**
  - The writes from step 1 have distinct dots on `J.phone` with strictly increasing HLCs; no two writes from `J.phone` share a dot.
  - `A.phone` shows "Milk 2L" and `link1` after step 2.
  - `link2` appears on every device in `E5`: either the extension wrote it there, or the app signed it again on launch.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** Extension actions are outside the step vocabulary. See G-16 (`faults.md` FT-024) for the extension publishing into an old epoch.

---

## Edge

### RC-012 Equal HLCs, broken by author and device
- **Kind:** edge
- **Design:** §9.1, §9.2
- **Binding:** any
- **Setup:** Default. The harness pins `J.phone`, `J.mac` and `A.phone` to the same wall-clock millisecond T, with no earlier write at T on any of them.
- **Steps:**
  1. All three offline.
  2. `J.phone sets milk.title = "J1"`; `J.mac sets milk.title = "J2"`; `A.phone sets milk.title = "A"`. Each has hlc (T, 0).
  3. All online; all sync.
- **Expect:**
  - Kept {all three}; each device reports the other two.
  - Displayed: if A's user ID is greater than J's as unsigned bytes, "A". Otherwise whichever of `J.phone` and `J.mac` has the greater device ID. The harness computes the expected value from the IDs; all devices agree.
- **Invariants:** INV-CONVERGE
- **Notes:** Pinning clocks to one millisecond is outside the step vocabulary.

### RC-013 A Mac whose clock is three days fast
- **Kind:** edge
- **Design:** §9.1, §9.2
- **Binding:** any
- **Setup:** Default.
- **Steps:**
  1. `J.mac clock = +3 days`.
  2. `J.mac sets milk.title = "Milk 2L"` (`j1`, hlc ≈ T + 3 d); all sync.
  3. `A.phone sets milk.title = "Milk 1L"` (`a1`, hlc ≈ T; context covers `j1`); all sync.
  4. `J.mac offline`, `A.phone offline`. `J.mac sets eggs.title = "Eggs 6"` (`j2`); an hour later in real time, `A.phone sets eggs.title = "Eggs 12"` (`a2`). Both online; all sync.
  5. `J.mac clock = 0`; `J.mac sets bread.title = "Rye"` (`j3`).
- **Expect:**
  - After step 3: kept {`a1`}; every device shows "Milk 1L". A write that saw `j1` supersedes it whatever its HLC.
  - After step 4: kept {`j2`, `a2`}; every device shows "Eggs 6", the earlier edit in real time, and reports "Eggs 12".
  - `j3`'s hlc is greater than `j2`'s: a device's HLC never goes backwards.
  - `A.phone`'s next write carries an HLC near its own clock, not near `J.mac`'s (devices don't adopt other devices' clocks).
- **Invariants:** INV-CONVERGE

### RC-014 Offline edits queued across a key change
- **Kind:** edge
- **Design:** §8.3, §9.2, §11.6 (signing again)
- **Binding:** any
- **Setup:** Default, with B also a writer.
- **Steps:**
  1. `J.phone offline`.
  2. `J.phone sets milk.title = "Oat milk"` (`j`, naming an `E4` statement), not published.
  3. `J.mac removes B` (`E5`).
  4. `A.phone sets milk.title = "Whole milk"` (`a`, in `E5`).
  5. `J.phone online`; `J.phone syncs`; all sync.
- **Expect:**
  - `J.phone` appends nothing to channel `E4` after coming online; it moves to `E5` first.
  - `j` isn't in `inv(E5)`, so `J.phone` signs it again as `j'`: same dot, empty context, base = `j.base` ∪ `reach(j)`, `ref` = the first `E5` statement listing J.
  - Kept {`j'`, `a`}: neither covers the other's dot. The displayed one is the greater by `(epoch, hlc, author, deviceID, writeID)`; both are `E5`, so the later HLC wins (here `a`, "Whole milk"), and "Oat milk" is reported.
  - `B.phone` cannot read `j'`.
- **Invariants:** INV-CONVERGE, INV-CUT, INV-NO-LOSS

### RC-015 A write lands in the old channel during a key change
- **Kind:** edge
- **Design:** §8.3, §9.2, §11.6 (signing again)
- **Binding:** any
- **Setup:** Default, with B also a writer. `A.mac clock = +1 min`. `milk.completedAt` holds an `E4` write `c4`.
- **Steps:**
  1. `A.mac syncs` (`E4` current).
  2. `J.mac removes B`; it fixes `inv(E5)` (listing `c4`).
  3. `A.mac sets milk.title = "Milk 1L"` (`x`) and appends it to channel `E4` without re-syncing.
  4. `J.mac` announces `E5`. `A.phone syncs`; `A.phone sets milk.title = "Whole milk"` (`a5`, in `E5`).
  5. `A.mac syncs`; all sync.
- **Expect:**
  - `x` isn't eligible in `E5` as it stands. `A.mac` (its author's device) signs it again as `x'`; no other device forwards it.
  - Kept for `milk.title` = {`x'`, `a5`}: `a5` never saw `x`. Both are `E5`; `x'` keeps `x`'s dot, whose HLC (one minute fast) is the greater, so every device shows "Milk 1L" and reports "Whole milk".
  - `milk.completedAt` still shows `c4`.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** The interleaving of step 3 must be forced by the harness. That an old dot can outrank a newer write in the display order is a consequence of keeping the dot when signing again (§11.6).

### RC-016 Concurrent `parentID` changes make a cycle and a second level
- **Kind:** edge
- **Design:** §9.2, §17
- **Binding:** any
- **Setup:** `Groceries` holds top-level items `cake`, `party`, `candles`.
- **Steps:**
  1. `J.phone offline`, `A.phone offline`.
  2. `J.phone sets cake.parentID = party`.
  3. `A.phone sets party.parentID = cake`; `A.phone sets candles.parentID = cake`.
  4. Both online; all sync.
- **Expect:**
  - Tandem state: `cake.parentID` = {party}, `party.parentID` = {cake}, `candles.parentID` = {cake}; nothing reported (different records).
  - Every device shows the same one-level tree, with no cycle and no item two levels deep.
  - Devices either write nothing or produce exactly one repair write per item.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** GAP (G-33): §17 maps `parentID` to a per-record register, so concurrent moves can produce a cycle (cake → party → cake) or two levels (candles → cake → party), which Liszto's "one level of sub-items" forbids. Nothing says how the app derives a deterministic tree from such state, or which device, if any, repairs it.

### RC-017 Unknown fields and operations from a newer app version
- **Kind:** edge
- **Design:** §9.1, §9.2 (unknown fields), §10.5, §10.7, §11.6, §15
- **Binding:** any
- **Setup:** Default. `J.mac` and `J.ipad` run Liszto v2, which adds the register `priority`, the set `assignees`, and a reserved field `_pin`. `J.phone` (an owner device) and `A.phone` run v1.
- **Steps:**
  1. `J.mac sets milk.priority = "high"`; `J.mac tags milk.assignees +A`; `J.mac sets milk._pin = 1`; all sync.
  2. `A.phone sets milk.title = "Milk 2L"`; `A.phone syncs` and destroys J.mac's message, now dominated (§10.7).
  3. `J.mac` and `J.ipad`, both offline, set `milk.priority` to "low" and "urgent"; both online; all sync.
  4. `J.phone` asks for a new key.
  5. A v2 client publishes a write with `op` 5 to `milk.flags`.
- **Expect:**
  - After step 2 the hub's message for `milk`, published by `A.phone`, contains J.mac's `priority`, `assignees` and `_pin` writes byte-identical (same `v`, same `s`).
  - `J.mac` shows `priority` high and title "Milk 2L".
  - After step 3, v1 devices keep both `priority` siblings and write nothing to `priority`; v2 devices report the conflict.
  - After step 4, `inv(E5)` lists every `priority`, `assignees` and `_pin` write, and `J.mac` still shows them.
  - Step 5: every v1 device reaches the same verdict on the `op` 5 write and doesn't destroy a message holding it.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS, INV-GC-SAFE
- **Notes:** GAP (G-23): §9.2 carries unknown *fields*, but nothing says whether a write with an unknown `op` value is carried like an unknown field, dropped as malformed (§8.3), or makes the message "a version it doesn't understand" (§15). A client that drops it and then garbage-collects destroys v2 data.

### RC-018 Writes that arrive before the statement admitting their author
- **Kind:** edge
- **Design:** §8.3 (held), §9.2 (building a write), §10.7
- **Binding:** any
- **Setup:** Default. B not yet a member.
- **Steps:**
  1. `B.phone joins via link` to `Groceries`; `J.mac approves B` (statement `s`).
  2. `hub: withhold` the message holding `s` from `A.phone`.
  3. `B.phone syncs`; `B.phone adds chips to Groceries`; `B.phone sets milk.title = "Milk!"` (`b`, `ref` = `s`).
  4. `A.phone syncs`; `A.phone sets milk.title = "Milk 2L"` (`a`).
  5. The hub stops withholding; `A.phone syncs`.
- **Expect:**
  - After step 4, `A.phone` shows neither `chips` nor "Milk!" and shows no error. B's writes are held (they name a statement it doesn't have), not dropped.
  - `a`'s context doesn't cover `b`: held writes aren't kept writes, so they don't enter a new write's vectors (§9.2).
  - A message holding `b` isn't destroyed meanwhile: held writes count for domination (§10.7).
  - After step 5, `A.phone` shows `chips`; kept for `milk.title` = {`a`, `b`} and the conflict is reported.
- **Invariants:** INV-CONVERGE, INV-AUTHZ, INV-NO-HOLD-FOREVER
- **Notes:** "Stops withholding" is outside the step vocabulary.

### RC-019 A removed member's last edit, before and after the inventory
- **Kind:** edge
- **Design:** §3.3, §8.3, §11.6
- **Binding:** any
- **Setup:** Default, with B also a writer.
- **Steps:**
  1. `B.phone adds wine to Groceries` and publishes it to `E4`.
  2. `J.mac removes B`: it syncs and fixes `inv(E5)` (S1–S2), then announces.
  3. Variant: `B.phone` publishes `wine` only after `J.mac` fixed `inv(E5)` and before the announcement; `A.phone` syncs in between and shows it.
  4. All sync.
- **Expect:**
  - Main run: `wine` is in `inv(E5)`, carried with B's signature, and shows author B on every device. B wrote before the change took effect (§3.3).
  - Variant: `wine` is valid in `E4` but not eligible in `E5`, and B can't sign it again (no `E5` statement lists B). It disappears from `A.phone` when A moves to `E5`.
- **Invariants:** INV-CUT, INV-AUTHZ, INV-UX
- **Notes:** GAP (G-24, minor): nothing tells the app that an item it displayed was left behind by a key change (§11.6, "another user's late write can be lost"). Liszto can't explain the vanished item.

### RC-020 Sets: replayed adds, concurrent removes, removing what was never added
- **Kind:** edge
- **Design:** §9.2 (set), §9.3, §10.7
- **Binding:** any
- **Setup:** Default. `milk.tags` = {dairy (`d0` by A), organic (`o0` by J)}. Message `M0` on the hub holds `milk` with `d0` and `o0` only.
- **Steps:**
  1. `J.mac tags milk -dairy` (`r1`); all sync.
  2. `hub: replay M0`; all sync.
  3. `J.phone offline`, `A.phone offline`. `J.phone tags milk -organic` (`r2`); `A.phone tags milk -organic` (`r3`). Both online; all sync.
  4. `A.phone tags milk +organic` (`o1`, context covers `r2`, `r3`); all sync.
  5. `J.mac tags milk -gluten` (`r4`; `gluten` was never added). `A.phone offline`; `A.phone tags milk +gluten` (`g1`); `A.phone online`; all sync.
- **Expect:**
  - After step 2: `dairy` absent everywhere; kept for `dairy` = {`r1`}. No device re-publishes `d0`, and some device that merged the replayed copy of `M0` destroys it.
  - After step 3: kept for `organic` = {`r2`, `r3`}; absent.
  - After step 4: kept for `organic` = {`o1`}; present.
  - After step 5: kept for `gluten` = {`r4`, `g1`}; present.
- **Invariants:** INV-CONVERGE, INV-GC-SAFE, INV-QUIESCE

### RC-021 Two people move the same item to different lists
- **Kind:** edge
- **Design:** §9.5, §17
- **Binding:** any
- **Setup:** Default plus `batteries` in `Groceries`. A has a private list `Later` (A only).
- **Steps:**
  1. `J.mac offline`, `A.phone offline`.
  2. `J.mac moves batteries to House`.
  3. `A.phone moves batteries to Later`.
  4. Both online; all sync.
- **Expect:**
  - `batteries` in `Groceries` is deleted; its state holds both `delete` writes.
  - `House` holds one "Batteries" (created by J); A's `Later` holds another (created by A). A sees both; J sees one.
- **Invariants:** INV-CONVERGE
- **Notes:** GAP (G-34): §17 keeps move as "create in target + delete in source (unchanged)". Nothing links the copies to the original, so concurrent moves duplicate and no device can detect it. The design should say so, or record the source in the new record.

---

## Adversarial

### RC-022 Mallory signs two writes with the same dot
- **Kind:** adversarial
- **Design:** §9.2, §9.3, §10.7
- **Binding:** any
- **Setup:** Default, with M a writer. `milk.title` kept {`a0`, dot `{A.phone: 5}`}.
- **Steps:**
  1. `M.phone signs` `m1`: `milk.title = "Bread"`, dot `(M.phone, 10)`, context `{A.phone: 5}`. `M.phone signs` `m2`: `milk.title = "Cake"`, same dot, same context. Each goes in its own message.
  2. `hub: withhold m2's message from A.phone`; `A.phone syncs` (gets `m1`).
  3. `A.phone sets milk.title = "Milk"` (`a1`, context `{A.phone: 5, M.phone: 10}`); `hub: withhold a1's message from J.mac`; `J.mac syncs`.
  4. Withholding ends; all sync.
  5. `M.phone signs` `m3`: `milk.title = "Jam"`, dot `(M.phone, 10)`, context `{A.phone: 5}`; all sync.
- **Expect:**
  - After step 3 `J.mac` has kept {`m1`, `m2`}, displays the one with the greater write ID, and reports the other.
  - After step 4 every device has kept {`a1`}. `A.phone` never displayed "Cake".
  - `m3` is superseded on arrival everywhere; "Jam" is never displayed.
  - After quiescence the hub's only message for `milk` holds `a1`.
- **Invariants:** INV-CONVERGE, INV-SIGNED, INV-GC-SAFE

### RC-023 Contexts that cover each other
- **Kind:** adversarial
- **Design:** §9.2, §9.3
- **Binding:** any
- **Setup:** Default, with M a writer. `milk.title` kept {`a0`, dot `{A.phone: 5}`}.
- **Steps:**
  1. `M.phone signs` `m1`: "Bread", dot `(M.phone, 10)`, context `{A.phone: 5, M.phone: 20}`; and `m2`: "Cake", dot `(M.phone, 15)`, same context. All sync.
  2. `J.mac sets milk.title = "Milk"` (`j`, context `{A.phone: 5, M.phone: 20}`); all sync.
- **Expect:**
  - After step 1: kept {`m1`, `m2`} (each includes the other; both supersede `a0`). Displayed "Cake" (hlc 15 > 10); "Bread" reported.
  - After step 2: kept {`j`}, because neither M write's context covers `J.mac`'s dot. Every device shows "Milk".
- **Invariants:** INV-CONVERGE

### RC-024 A context claiming the future erases edits not yet made
- **Kind:** adversarial
- **Design:** §9.2, §9.3, §1.2
- **Binding:** any
- **Setup:** Default, with M a writer. `milk.title` kept {`a0`, dot `(A.phone, T − 1 h)`}. `J.phone` offline since before T.
- **Steps:**
  1. At T, `M.phone signs` `m`: "Mallory's milk", dot `(M.phone, T)`, context `{A.phone: T − 1 h, J.phone: T + 1 h}`. All online devices sync.
  2. At T + 10 min, `J.phone sets milk.title = "Oat milk"` (`j`). `J.phone online`; all sync.
  3. At T + 20 min and T + 30 min, `J.phone sets milk.title` to "Milk" (`j1`), then "Milk!" (`j2`); all sync.
  4. At T + 70 min, `J.phone sets milk.title = "Milk 1L"` (`j3`); all sync.
- **Expect:**
  - After step 2: kept {`m`} on every device, `J.phone` included. "Oat milk" is gone; nothing is reported; the hub keeps no message holding `j`.
  - After step 3: kept {`j1`, `j2`}. The two writes include each other because both contexts carry M's `J.phone: T + 1 h`. Displayed "Milk!"; "Milk" (J's own earlier value) is reported as a conflict.
  - After step 4: kept {`j3`}; nothing reported.
- **Invariants:** INV-CONVERGE
- **Notes:** §9.3 accepts this as "pin a value of their own on top" while M is a member. `J.phone` could notice a context entry for itself above its own HLC; the design doesn't ask it to.

### RC-025 A future context for every device keeps a field in conflict
- **Kind:** adversarial
- **Design:** §9.2, §9.3, §10.7, §17
- **Binding:** any
- **Setup:** Default, with M a writer. All devices online.
- **Steps:**
  1. `M.phone signs` `m`: `eggs.notes = "x"`, dot `(M.phone, T)`, context naming `J.phone`, `J.mac`, `J.ipad`, `A.phone`, `A.mac`, `B.phone`, `C.phone`, `M.phone`, each at `MAX`. All sync.
  2. `J.phone sets eggs.notes` to "a" (`j1`), then "b" (`j2`); `A.phone sets eggs.notes = "c"` (`a1`); all sync.
  3. `J.new` (a device of J restored with the backup key, so with a new device ID) `sets eggs.notes = "d"` (`n1`); `J.phone sets eggs.notes = "e"` (`j3`); all sync.
  4. `J.mac removes M` (`E5`); `J.mac sets eggs.notes = "f"` (`f5`); all sync.
- **Expect:**
  - After step 2: kept {`m`, `j1`, `j2`, `a1`}; no write supersedes another; displayed the one with the greatest HLC.
  - After step 3: `n1` supersedes all of them (M didn't list `J.new`); then `j3` and later writes from listed devices pile up again.
  - After step 4: kept {`f5`}.
  - Liszto doesn't loop: an automatic notes resolution (§17) that can't settle the field isn't written again and again.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** §9.3 accepts the conflict while M is a member. GAP (G-25, Liszto): §17 has Liszto resolve `notes` siblings "by writing a merged value covering both"; here every such write is one more kept sibling, so a device that resolves whenever it sees a conflict publishes for ever. The rule needs a stop (resolve only what the new write would supersede, or once per user action).

### RC-026 A future-claiming context pre-empts late writes of its epoch
- **Kind:** adversarial
- **Design:** §9.2 (building a write), §9.3, §11.6 (signing again), §8.3
- **Binding:** any
- **Setup:** Default, with M and B writers.
- **Steps:**
  1. `M.phone signs` `m`: `milk.title = "M"`, with a context naming every device at `MAX`. All sync.
  2. `A.mac syncs`. `J.mac removes M` and B; `J.mac` fixes `inv(E5)` (listing `m`).
  3. `A.mac sets milk.title = "Milk 1L"` (`x`, naming an `E4` statement) and appends it to channel `E4` without re-syncing.
  4. `J.mac` announces `E5`. `A.phone sets milk.title = "Whole milk"` (`a5`, in `E5`).
  5. `A.mac` moves to `E5`; all sync.
  6. Control run: the same steps without step 1.
- **Expect:**
  - Control run: `A.mac` signs `x` again; kept {`x'`, `a5`}; one is reported (as RC-015).
  - With step 1, as written: `a5.base` contains `reach(m)`, so it covers `reach(x)` and `x.base`: the new state supersedes `x`. `A.mac` therefore doesn't sign it again (§11.6), and "Milk 1L" is never displayed or reported.
  - The same holds for every write of the old epoch that missed the inventory, and for inventory entries carried forward unseen (§8.3) when they are restored.
- **Invariants:** INV-NO-LOSS fails under the current text (see GAP)
- **Notes:** GAP (G-26): §9.2 says a context that claims the future "binds only its own epoch". That is literally true, but the epoch it binds includes its late writes, which only become visible after the next key change. "Building a write" copies `reach(m)` into every new write's `base`, so `m` pre-supersedes them, and §11.6's "a write the snapshot replaced is not signed again" then drops them silently, after M's removal too. A fix: build `base` only from writes the device actually holds (cap each entry at the highest dot seen from that device), or exclude a removed author's claims from `base`.

### RC-027 Pinning a title, ended by removal
- **Kind:** adversarial
- **Design:** §1.2, §9.2, §9.3, §8.3, §11.6
- **Binding:** any
- **Setup:** Default, with M a writer.
- **Steps:**
  1. `M.phone signs` `m`: `milk.title = "Mallory was here"`, hlc `(MAX, 0)`, context naming every device (`M.phone` included) at `MAX`. All sync.
  2. `J.mac sets milk.title = "Milk"` (`j1`); `A.phone sets milk.title = "Milk 2L"` (`a1`); all sync.
  3. `J.mac removes M` (`E5`); all sync.
  4. `J.mac sets milk.title = "Milk"` (`j5`); all sync.
- **Expect:**
  - After step 2: kept {`m`, `j1`, `a1`}; every device shows "Mallory was here" and reports two others.
  - After step 3: unchanged. `m` was merged before the change, so `inv(E5)` lists it.
  - After step 4: kept {`j5`}; every device shows "Milk". `M.phone` can't read `E5`.
- **Invariants:** INV-CONVERGE, INV-CUT
- **Notes:** The removal ends the pin, as §9.3 says; RC-026 is what survives it.

### RC-028 A flood of siblings beyond the 16 shown
- **Kind:** adversarial
- **Design:** §9.2 (16-sibling limit), §9.3
- **Binding:** any
- **Setup:** Default, with M a writer.
- **Steps:**
  1. `J.phone offline`; `J.phone sets milk.title = "Oat milk"` (`j`).
  2. `M.phone signs` 20 writes `m01`…`m20` to `milk.title`, dots `(M.phone, T + 1)`…`(M.phone, T + 20)` with HLCs above `j`'s, each with the same context (what M has seen before `j`).
  3. `J.phone online`; all sync.
  4. `J.phone sets milk.title = "Milk"` (`j2`); all sync.
- **Expect:**
  - After step 3: kept is 21 writes everywhere; displayed `m20`. The app is shown 16 values, `m05`…`m20`; `j` is not among them, including on `J.phone`.
  - After step 4: kept {`j2`} (its context entry `M.phone: T + 20` covers all of M's); every M message for `milk` is dominated and destroyed by some device.
- **Invariants:** INV-CONVERGE, INV-GC-SAFE, INV-UX
- **Notes:** GAP (G-35): §9.2 caps presentation at 16 but doesn't tell the app how many siblings exist, or guarantee that the local user's own value is among those shown. With 16 high-HLC siblings, M hides any honest value from everyone, its author included.

### RC-029 Forged attribution and cut-and-paste of a signed write
- **Kind:** adversarial
- **Design:** §5.5, §8.3, §9.2
- **Binding:** any
- **Setup:** Default, with M a writer of `Groceries` and `House`.
- **Steps:**
  1. `A.phone sets milk.title = "Milk 2L"` (`a`); all sync.
  2. `M.phone` republishes `a` unchanged except for: (i) record `eggs`; (ii) field `notes`; (iii) inside a `House` message; (iv) `r` naming a different statement.
  3. `M.phone signs` a write to `bread.title` with `a` = A's user ID and `d` = `M.phone`.
  4. `A.phone sets milk.title = "Milk 1L"` (`a2`); all sync; `hub: replay` the message holding `a`.
- **Expect:**
  - Every honest device drops (i)–(iv) and step 3's write (the signature check fails: `sigInput` covers scope, record, field and `ref`, and the author's key is the user ID), logs them, never displays them, and doesn't hold them.
  - `a` stays valid in `milk` until superseded; after step 4, kept {`a2`} and the replay changes nothing.
- **Invariants:** INV-SIGNED, INV-AUTHZ, INV-CONVERGE

### RC-030 A reader signs writes
- **Kind:** adversarial
- **Design:** §8.3, §9.4 (holding limits), §10.7, §12.2
- **Binding:** any
- **Setup:** Default, with M a reader.
- **Steps:**
  1. `M.phone signs` `milk.title = "Beer"` naming the current statement, and 10,000 writes creating new records with 1 KiB values each.
  2. `M.phone signs` 10,000 more naming a statement ID that doesn't exist.
  3. All sync; `wait 31 days`.
- **Expect:**
  - Step 1's writes are rejected on every device (the named statement lists M as reader) and not held.
  - Step 2's writes are held (the statement might still arrive), never displayed, and evicted locally after the holding limit (30 days, configurable). Their messages are not destroyed because of the eviction (§9.4).
  - No honest device re-publishes any of them.
- **Invariants:** INV-AUTHZ, INV-NO-HOLD-FOREVER

### RC-031 Malformed writes that two SDKs may read differently
- **Kind:** adversarial
- **Design:** §5.5, §8.3, §9.1, §9.2, §10.5
- **Binding:** any
- **Setup:** Default, with M a writer. `J.mac` runs SDK 1 and `A.mac` runs SDK 2 (two independent implementations).
- **Steps:** `M.phone signs` and publishes, each to a different item:
  1. A context whose entries aren't sorted by device ID; the signature covers the entries sorted.
  2. A context with two entries for the same device.
  3. A dot whose millis is 2^50 (above §9.1's u48, within §5.5's u64 and §10.5's 2^53).
  4. An `add` (op 2) to `title`, a field every other write treats as a register.
  5. A `delete` (op 4) carrying a field name and a value.
- **Expect:**
  - For each write, `J.mac` and `A.mac` make the same accept-or-drop decision and end in the same state.
  - Nothing malformed is displayed.
- **Invariants:** INV-CONVERGE, INV-SIGNED
- **Notes:** Running two SDKs is a harness capability outside the step vocabulary. Repeated JSON keys and non-canonical base64url, formerly here, are settled by §10.5 and tested in `reading.md`. GAP (G-27): still unsaid are (1) whether a context's JSON order must already be sorted or is sorted before `vv()`; (2) whether a vector may name one device twice, and what "covers" means if it does; (3) whether millis above 2^48 make a write invalid; (4) how a field holding both `assign` and `add`/`remove` writes merges and displays; (5) see RC-034.

### RC-032 Writes naming the wrong statements
- **Kind:** adversarial
- **Design:** §8.3, §11.6
- **Binding:** any
- **Setup:** `Groceries` in `E6`, with M a writer.
- **Steps:**
  1. `M.phone signs` a write to `milk.title` = "Nine" naming a made-up statement ID and publishes it in channel `E6`.
  2. `J.mac` makes M a reader (`E7`).
  3. `M.phone` (a reader holding `K(E7)`) re-encrypts her own unlisted `E6` write `milk.title = "Cake"` into channel `E7`.
  4. `M.phone` signs "Cake" again, naming the `E7` start.
- **Expect:**
  - Step 1's write is held (unknown statement), never displayed, evicted after the holding limit.
  - Step 3's write is ineligible in `E7` (not `E7`'s own, not in `inv(E7)`).
  - Step 4's write is rejected: `E7`'s statements list M as reader.
- **Invariants:** INV-AUTHZ, INV-CONVERGE

### RC-034 A writer deletes every item
- **Kind:** adversarial
- **Design:** §1.2, §9.2 (delete), §8.3, §11.6, §12.3
- **Binding:** any
- **Setup:** `Groceries` holds 40 items, with M a writer.
- **Steps:**
  1. `M.phone deletes` all 40 items; all sync.
  2. `J.mac removes M` (`E5`); all sync.
- **Expect:**
  - After step 1 every device shows `Groceries` empty; each record's hub state holds M's tombstone.
  - After step 2 all 40 are still deleted (the tombstones are in `inv(E5)`). `M.phone` holds no `K(E5)`.
  - The tombstones stay for good (§12.3). Restoring an item means creating a new record.
- **Invariants:** INV-CONVERGE, INV-CUT
- **Notes:** Accepted risk (§1.2): writers can vandalise what they can write. GAP (G-28): neither §5.5 nor §10.5 says what a `delete` write's field name, context and base are. `sigInput` needs a field name (empty?), and §10.5 shows `f` on every write. Two SDKs that encode deletes differently can't verify each other's. Nor is it said whether a key change must carry a deleted record's other field writes or only its tombstone.

---

## Fault

### RC-035 HLC lost in a crash, then the clock goes back
- **Kind:** fault
- **Design:** §9.1, §9.2
- **Binding:** any
- **Setup:** Default. `milk.notes` kept {`a0`, dot `(A.phone, 5)`}. `J.phone` has persisted HLC `(T, 0)`.
- **Steps:**
  1. `J.phone sets milk.notes = "3 cartons"` (`j1`, hlc `(T + 60 s, 0)`, context `{A.phone: 5}`); `J.phone syncs` (publishes `j1`).
  2. `J.phone crashes after` appending `j1`, before persisting its HLC and replica.
  3. `J.phone clock = -10 min`; `J.phone offline`; `J.phone launches`.
  4. `J.phone sets milk.notes = "4 cartons"` (`j2`, hlc `(T, 1)`, context `{A.phone: 5}`).
  5. `A.phone syncs` (has `j1`); `A.phone sets milk.notes = "5 cartons"` (`a1`, context `{A.phone: 5, J.phone: (T + 60 s, 0)}`).
  6. `J.phone online`; all sync.
- **Expect (desired):**
  - "4 cartons" is reported as a conflict on every device; kept {`a1`, `j2`}.
- **Invariants:** INV-NO-LOSS fails under the current text (see GAP)
- **Notes:** GAP (G-20): §9.1 requires a device's HLCs to be strictly increasing but not that the HLC be persisted before a write leaves the device, or rebuilt on launch from the device's own highest published dot. As written, `j2`'s dot falls below `J.phone`'s entry in `a1`'s context, so `a1` supersedes `j2` although A never saw it. J's latest edit vanishes everywhere with no report. With an exact clock match the same failure reuses a dot.

### RC-036 Crash between the two halves of a move
- **Kind:** fault
- **Design:** §9.5, §17
- **Binding:** any
- **Setup:** Default plus `batteries` in `Groceries`.
- **Steps:**
  1. `J.mac moves batteries to House`; `J.mac crashes after` appending the create in `House`, before signing the delete in `Groceries`.
  2. `J.mac launches`; all sync.
- **Expect (desired):**
  - `batteries` appears in exactly one list on every device.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** GAP (G-36): a move spans two key domains and two writes (§9.5); the design doesn't say the app must journal it. Read literally, `batteries` stays in both lists. The opposite order (delete first) would lose it on a crash; §9.5 implies create first but doesn't require it.

### RC-037 A partially fetched record and set contexts
- **Kind:** fault
- **Design:** §9.2 (set; building a write), §10.6
- **Binding:** any
- **Setup:** Item `party` has 2,000 tags, published as two messages. `P1` holds B's add of `balloons` (`b1`, dot `(B.phone, 5)`) and A's add of `cake` (`(A.phone, 60)`). `P2` holds A's concurrent add of `balloons` (`a1`, dot `(A.phone, 50)`, empty context).
- **Steps:**
  1. `J.phone syncs` with `net: drop after <n> bytes` timed so that `P1` arrives and `P2` doesn't; `J.phone offline`.
  2. `J.phone tags party -balloons` (`r`).
  3. `J.phone online`; all sync.
- **Expect:**
  - `balloons` is present on every device: J never saw `a1`, so add wins (§9.2). Kept for `balloons` = {`r`, `a1`}.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** GAP (G-37): "Building a write" takes the join of `reach(k)` over "the kept writes" without saying whether, for a set, that is the kept writes of the element or of the whole field. With a per-field context, `r` carries `A.phone: 60` (from `cake`), covers `reach(a1)`, and removes `balloons` that J never saw, breaking add-wins. That can happen whenever a device holds a later dot of a device without an earlier one, as a split record (§10.6) allows. Contexts must be per element.

---

## UX

### RC-038 The item being edited is deleted by someone else
- **Kind:** ux
- **Design:** §9.2 (delete), §13.2, §13.3
- **Binding:** any
- **Setup:** Default. `J.mac` has `eggs` open in the notes editor.
- **Steps:**
  1. J types "free range" into `eggs.notes`.
  2. `A.phone deletes eggs`; `J.mac syncs` while J keeps typing "…, 12 pack".
  3. `wait 5 s`.
- **Expect (desired):**
  - `J.mac` tells J the item was deleted by Aimee and offers to keep the text, rather than closing the editor silently.
  - J's typed text is not discarded without a message.
- **Invariants:** INV-UX
- **Notes:** GAP (G-31): §13.2 lists no flow for a local edit that lands on a tombstoned record, and Tandem defines no signal to the app for it. Read literally, `eggs` disappears from J's screen and the writes made in step 2 go into a deleted record's state, never shown (also RC-005, RC-009).

### RC-039 Two devices resolve the same notes conflict at once
- **Kind:** ux
- **Design:** §9.2, §10.7, §17
- **Binding:** any
- **Setup:** Default. `eggs.notes` = "Large". `J.mac`, `A.mac` and `J.ipad` have quit. C is a reader with `C.phone` running.
- **Steps:**
  1. `J.phone offline`, `A.phone offline`. `J.phone sets eggs.notes = "Large, free range"` (`j`); `A.phone sets eggs.notes = "Large, organic"` (`a`).
  2. Both online at the same moment; both sync.
  3. `wait 60 s`.
- **Expect (desired):**
  - After step 3 kept for `eggs.notes` is one write on every device, and its text contains "free range" and "organic" exactly once each.
  - `C.phone` shows the displayed sibling and writes nothing (a reader's writes are never valid).
  - No device publishes for `eggs` after the first 60 s.
- **Invariants:** INV-QUIESCE, INV-CONVERGE
- **Notes:** GAP (G-38): §17 says Liszto resolves `notes` siblings "by writing a merged value covering both" but not which device does it. Both phones resolve concurrently, giving two new siblings `mJ`, `mA`. Liszto's conflict-text rule is perspective-dependent ("local" vs "remote"), so `mJ ≠ mA`, and the next merge nests the texts ("A / B / B / A"). Even identical texts stay a conflict, because they are different writes. Nothing designates a resolver, delays resolution as §10.7 delays publishing, or treats equal-valued siblings as one.

### RC-040 A three-way title conflict and picking a winner
- **Kind:** ux
- **Design:** §9.2, §13.2, §17
- **Binding:** any
- **Setup:** Default. `A.phone clock = +2 s`, `J.mac clock = +1 s`.
- **Steps:**
  1. `J.phone`, `J.mac`, `A.phone` offline. `J.phone sets milk.title = "Oat milk"` (`j1`); `J.mac sets milk.title = "Skim milk"` (`j2`); `A.phone sets milk.title = "Milk 2L"` (`a`).
  2. All online; all sync.
  3. On `J.mac`, J picks "Oat milk" from the conflict list (Liszto writes `milk.title = "Oat milk"`, `j3`); all sync.
- **Expect:**
  - After step 2: kept {`j1`, `j2`, `a`} everywhere; displayed "Milk 2L"; Tandem reports two other values, "Skim milk" (J, `J.mac`) and "Oat milk" (J, `J.phone`), each with author and device.
  - After step 3: kept {`j3`}; nothing reported; every device shows "Oat milk".
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** Picking from a conflict list is outside the step vocabulary. §17 describes conflict UI only for `notes`; whether Liszto shows title conflicts at all is a product choice, and the Expect only checks what Tandem reports.
## Validity and limits

### RC-041 A delete on a named record
- **Kind:** adversarial
- **Design:** §7.3
- **Binding:** any
- **Setup:** Default.
- **Steps:**
  1. A modified client on `J.mac` (the owner) signs a `delete` on `_meta`.
  2. M (a writer) signs a `delete` on `_roster`.
- **Expect:**
  - Both are invalid and rejected everywhere; `_meta` and the roster are unchanged.
- **Invariants:** INV-AUTHZ

### RC-042 A vector at the limit
- **Kind:** adversarial
- **Design:** §9.2 (vector size), §5.5 (`vv`)
- **Binding:** any
- **Setup:** Default, with M a writer.
- **Steps:**
  1. `M.phone signs` a write to `milk.notes` whose context has 1,025 entries.
  2. `M.phone signs` one whose context has exactly 1,024 made-up device IDs.
  3. `J.mac sets milk.notes`; then `J.mac` removes M (`E5`); then `A.phone` deletes `milk` and creates it again as `milk2`.
- **Expect:**
  - Step 1's write is invalid everywhere.
  - Step 2's write is valid. `J.mac`'s next write would need more than 1,024 entries, so it writes with what fits; it doesn't supersede step 2's write, and the app is told the field can't be settled.
  - After M's removal, deleting and re-creating the record settles it: `milk2.notes` takes ordinary vectors.
- **Invariants:** INV-CONVERGE, INV-UX
- **Notes:** This is the design's answer to the old "vectors that never shrink" finding (`scale.md` SC-011).

### RC-043 Eligibility through the inventory
- **Kind:** adversarial
- **Design:** §8.3
- **Binding:** any
- **Setup:** `Groceries` moved from `E4` to `E5`. A discarded draft `E5'` also existed (`rotation.md` RO-022). `inv(E5)` lists A's write `t`.
- **Steps:**
  1. A device publishes `t` into channel `E5`.
  2. M publishes a write whose ID and record are listed in `inv(E5)` but whose named statement is in `E5'`'s history and not an ancestor of `E5` (harness constructs it).
  3. M publishes a write naming an `E4` statement with the same key commitment as `E5` (harness).
- **Expect:**
  - Step 1: eligible.
  - Steps 2–3: ineligible. A listed ID with the wrong ancestry, or a matching key or counter, is never enough.
- **Invariants:** INV-AUTHZ

---

Retired: RC-033 (`_carried` lists belong to a withdrawn design, §19).
