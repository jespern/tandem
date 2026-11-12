# Scale and longevity

Scope: how Tandem behaves with large collections, many collections and devices on one hub, records and fields too big for one message, rosters and tombstones that grow for years, the vector limit, a hub near its expected message ceiling, and bursts of rapid edits.

Conventions for this file: actors beyond the README's table are written `J.d01`…`J.d12` (extra devices of J) and `G001`… (guest users); the harness creates them, and each scenario that uses them says so. Bulk operations ("imports", "types for 90 s") are outside the step vocabulary and are flagged per scenario. Sizes are approximate: a data write without its value is about 250 bytes of JSON plus about 35 bytes per vector entry, a value grows by 4/3 as `b64u` inside the JSON, and the envelope grows by another 4/3 as base64 in the message body (§10.4, §10.5). "Kept", "displayed" and "reported" are as defined in `records.md`.

---

## Real-world

### SC-001 A list with 5,000 items, synced to a new device
- **Kind:** real-world
- **Design:** §1.2, §6.3, §10.1, §10.5, §10.6, §12.1
- **Binding:** any
- **Setup:** J owns `Recipes` (A writer, epoch `E2`) with 5,000 items, each with title, notes (~500 characters), tags, sortKey. All on the hub, one message per record.
- **Steps:**
  1. `J.phone pairs J.ipad`.
  2. `J.ipad syncs`.
  3. `A.phone sets item0001.title = "Bread v2"`; `J.ipad syncs`.
- **Expect:**
  - `J.ipad` fetches the bodies of exactly the 5,000 record messages, the inventory fragments and the roster of `Recipes`' `E2` channel, plus J's home and inbox. It fetches no other channel's bodies.
  - `J.ipad` shows 5,000 items, each equal field by field to `J.phone`.
  - Every message in the channel is under 64 KiB; no record is split.
  - Step 3 makes `J.ipad` fetch one message, not the channel.
  - The harness records the bytes `J.ipad` downloads (about 10 MB) and its time to first full display, for comparison across builds.
- **Invariants:** INV-CONVERGE, INV-HUB-BLIND

### SC-002 Importing 15,000 items at once
- **Kind:** real-world
- **Design:** §9.1 (HLC counter), §10.2 (`maxObjectsInSet`), §10.5, §10.7
- **Binding:** any
- **Setup:** J owns an empty `Archive`. The harness freezes `J.mac`'s wall clock at T for the import.
- **Steps:**
  1. `J.mac` imports 15,000 items with 5 fields each (75,000 writes) into `Archive`.
  2. `J.mac syncs`; `A.mac syncs` (A is a reader).
- **Expect:**
  - All 75,000 dots from `J.mac` are distinct and strictly increasing. The 65,537th write has hlc `(T + 1, 0)`, and later ones continue from there.
  - The hub holds 15,000 new kind-1 messages in `Archive`'s channel, one per record (records aren't combined), each holding the record's 5 writes, not one message per write.
  - JMAP: `J.mac` makes at most ⌈15,000 / `maxObjectsInSet`⌉ `Email/set` calls for the records. IMAP: 15,000 `APPEND`s.
  - `A.mac` shows 15,000 items.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** Import and frozen clock are outside the step vocabulary. One message per record means bulk imports move the hub toward Q3's ceiling quickly (SC-013); provider rate limits are unknown (Q1).

### SC-003 Starting over removes one identity from 200 collections
- **Kind:** real-world
- **Design:** §6.4, §8.3, §11.4, §11.6, §14
- **Binding:** any
- **Setup:** A owns 200 collections, each shared with J as writer, averaging 50 records (10,000 records). J's phone was stolen and J started over as J2.
- **Steps:**
  1. On `A.mac`, A removes J from all 200 and invites J2.
  2. `A.mac crashes after` completing 80 of the 200 key changes.
  3. `A.phone syncs`; `A.mac launches`; all sync.
- **Expect:**
  - Every one of the 200 collections ends exactly one epoch ahead of where it started, never two: each crashed change is resumed or finished from its journal or the note in home, not started afresh.
  - Grants: one per remaining member user per collection, sealed to each user's `uka`, not one per device.
  - After S6, the old channels keep only their `_roster`; the hub's record count returns to about 10,000.
  - The thief, holding J's old secret, holds no key of any new epoch.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-NO-LOSS, INV-GC-SAFE, INV-ONE-OWNER
- **Notes:** GAP (G-39): the design gives no pacing for a device that must change keys in hundreds of collections at once. That is about 10,600 appends in one burst against provider limits (Q1) that aren't known, and §13.2 says only that "the key change runs in the background". The flow of removing one identity from every collection a user shares with it is also undefined (§6.4 leaves it to each owner). §14 already notes that each key change exposes group size; here the hub also learns that one user owns all 200.

### SC-004 Tombstones pile up for good
- **Kind:** real-world
- **Design:** §9.5, §11.6, §12.3, Q10
- **Binding:** any
- **Setup:** `Groceries` (J owner, A writer) has had no membership change for two years. Every day A clears about 30 completed items (30 `delete`s), so there are about 21,000 tombstoned records.
- **Steps:**
  1. `J.mac` removes B (a key change).
  2. `J.ipad` is paired and syncs.
- **Expect:**
  - `inv(E1)` lists every tombstone and each deleted record's writes as the rotating device chose to keep them; nothing is compacted.
  - The hub keeps one small message per deleted record for good; the harness records the count, bytes and `J.ipad`'s download.
  - No deleted item reappears on any device.
- **Invariants:** INV-GC-SAFE, INV-MONOTONE-EPOCH
- **Notes:** This is the cost Q10 asks about. Liszto's deletion rate makes it the main source of hub growth (SC-013).

---

## Edge

### SC-005 A hub with 300 collections, four users
- **Kind:** edge
- **Design:** §1.2, §5.6, §10.1, §10.2, §10.3, §14
- **Binding:** any
- **Setup:** J has 40 collections (10 shared with A), A 30, B 120, C 110. No collection is shared with B or C except `Trip` (B owner, J writer). About 40,000 messages on the hub.
- **Steps:**
  1. `J.phone pairs J.ipad`; `J.ipad syncs`.
  2. `B.phone sets` an item in one of B's private collections; `J.ipad syncs`.
- **Expect:**
  - `J.ipad` reads the channel tag of every message (JMAP: `Email/query` and `Email/get` of headers; IMAP: header `UID FETCH`), but fetches bodies only for J's home, J's inbox, and the current epochs of J's 41 collections (with the `_roster` of their earlier channels as needed).
  - Step 2 causes `J.ipad` to read one header and fetch no body.
  - No body of B's or C's private collections is ever fetched by `J.ipad` (checked in the hub's access log).
- **Invariants:** INV-HUB-BLIND, INV-CONVERGE

### SC-006 Twelve devices of one user editing the same record
- **Kind:** edge
- **Design:** §6.3, §9.2, §10.7
- **Binding:** any
- **Setup:** J has 12 devices `J.d01`…`J.d12`, all online and synced, each following `Groceries`. A's devices have quit.
- **Steps:**
  1. At the same moment, `J.dNN sets milk.fieldNN = "vNN"` for NN = 01…12 (twelve different app fields of `milk`).
  2. `wait 60 s`.
- **Expect:**
  - Each device first publishes its own write (12 messages, none dominating another).
  - Within 10 s some device publishes a message containing all 12 writes; fewer than 12 devices publish a merged state (the harness reports the number).
  - Merged messages that dominate each other are reduced by the envelope-hash rule: only the one with the greater `H(envelope)` of each pair is destroyed, so one remains.
  - After step 2 the hub holds exactly one message for `milk`, and every device shows all 12 values.
  - All 12 devices hold the same `US`; their device IDs are distinct.
- **Invariants:** INV-CONVERGE, INV-QUIESCE, INV-GC-SAFE
- **Notes:** The extra devices are outside the actor table. §10.7's wait is "a few seconds at most" whatever the number of online devices; the harness records how many duplicates that gives at 12.

### SC-007 Five years of roster, copied at every key change
- **Kind:** edge
- **Design:** §8.1, §9.4, §10.6, §11.3, §11.6, §12.2, §12.4, Q16
- **Binding:** any
- **Setup:** `House` (J owner, A writer) has existed for five years: 60 key changes and 300 admissions and removals, about 360 statements. Each statement is the complete member list (up to 40 members), so the roster is about 3 MB of JSON. 120 guests who came and went have deleted Liszto.
- **Steps:**
  1. `J.mac` removes a guest (a key change).
  2. `hub: destroy all`; J restores the hub explicitly; `J.mac` and `A.phone` re-publish what they hold (§12.4).
  3. `C.phone joins via` a new QR invite to `House`; `J.mac` admits C.
- **Expect:**
  - After step 1 the new channel holds the whole roster, every statement, split across messages under 64 KiB. Nothing is truncated (§9.4).
  - Old channels keep their `_roster` for good, so the hub holds the roster once per epoch: about 60 copies.
  - Every statement verifies with the owner's key alone; departed guests' devices aren't needed.
  - After step 3, `C.phone` validates the roster from the genesis, accepts the grant and shows `House`; the harness records the time.
  - `J.mac` decides whether old invites are closed by reading `via` and removals across the whole chain (§11.3).
- **Invariants:** INV-CONVERGE, INV-AUTHZ, INV-NO-HOLD-FOREVER
- **Notes:** Q16 asks whether a signed checkpoint is needed. Complete statements make the roster grow with members × changes, and every old channel keeps a copy.

### SC-008 A record larger than one message
- **Kind:** edge
- **Design:** §10.6, §10.7, §12.2
- **Binding:** any
- **Setup:** Item `wedding` in `House` has 3,000 tags (guest names): about 3,000 add writes, roughly 1 MB of JSON and 1.6 MB of message bytes.
- **Steps:**
  1. `A.mac` adds the 3,000 tags (one import); `A.mac syncs`.
  2. `J.mac tags wedding +Bo`; `A.phone tags wedding +Carla`, at the same moment.
  3. All sync; `wait 10 min`.
- **Expect (desired):**
  - The hub holds a set of messages for `wedding` whose union is the record's state (3,002 elements); each is under 64 KiB, and their total size is at most twice the state's.
  - No device publishes for `wedding` after the first 60 s of step 3.
  - A device that fetches all parts holds 3,002 elements; one that fetches some parts holds a subset of them, never a different element.
- **Invariants:** INV-QUIESCE, INV-GC-SAFE, INV-CONVERGE
- **Notes:** Import is outside the step vocabulary. GAP (G-40): §10.7's rules work per message. No single message can dominate a split record, so "messages for one record that none of them dominates" is always true, and every device keeps re-publishing the merged state (split again), with no quiescence and no part ever garbage-collected. Healing (§12.2) uses "taken together"; publishing and GC don't. GAP (G-40): how a record is split ("the part that changed") isn't defined, so devices produce overlapping parts. GAP (G-43): it isn't said what the 64 KiB counts (plaintext, envelope or stored message, which differ by about 1.8×).

### SC-009 Coalescing continuous typing
- **Kind:** edge
- **Design:** §1.1, §10.7
- **Binding:** any
- **Setup:** Default `Groceries`. `J.mac` and `A.phone` online.
- **Steps:**
  1. J types 600 characters into `milk.notes` on `J.mac`, one keystroke every 150 ms (90 s), never pausing for 1 s.
  2. `J.mac crashes after` the last keystroke, 0.5 s before a publish would fire.
  3. `J.mac launches`; all sync.
- **Expect:**
  - The hub receives far fewer `milk` messages than keystrokes: at most about one per second.
  - Each new `milk` message dominates the previous one, which is destroyed; after step 3 the hub holds one.
  - Desired: `A.phone` shows J's text with no more than a few seconds' lag while J types.
  - After step 3 the full 600-character text is on `A.phone`.
- **Invariants:** INV-QUIESCE, INV-NO-LOSS, INV-GC-SAFE
- **Notes:** Typing is outside the step vocabulary. GAP (G-44): §10.7 says "debounce ~1 s" with no maximum wait. A trailing debounce publishes nothing until J pauses, so `A.phone` sees nothing for 90 s, against §1.1's "within seconds". GAP (G-45): nothing says whether writes in the debounce window are persisted before they are published, so step 2 may lose the last second of typing (see also RC-035 for the HLC).

---

## Adversarial

### SC-010 A field with 500 concurrent siblings
- **Kind:** adversarial
- **Design:** §9.2 (16-sibling limit), §9.3, §10.6, §10.7
- **Binding:** any
- **Setup:** Default `Groceries` with M a writer.
- **Steps:**
  1. `M.phone signs` 500 writes to `milk.notes`, each with a 2 KiB value, dots `(M.phone, T + 1)`…`(M.phone, T + 500)`, all with the same context, so none includes another. About 2 MB of message bytes, in 64 KiB messages (about 32).
  2. All sync.
  3. `J.mac sets milk.notes = "2 cartons"` (`j`); all sync; `wait 60 s`.
- **Expect:**
  - After step 2 every honest device keeps all 500 writes, still applies other records' writes normally, and shows the app 16 values.
  - Every message an honest device publishes for `milk` is under 1 MiB (MUST) and split per §10.6.
  - After step 3, kept {`j`} (its context entry `M.phone: T + 500` covers all 500, one vector entry). All of M's messages are dominated by `j`'s and destroyed; the hub holds one message for `milk`.
- **Invariants:** INV-CONVERGE, INV-GC-SAFE, INV-QUIESCE
- **Notes:** Between steps 2 and 3, devices meet the publishing problem of SC-008 (G-40): no single message can dominate the 500-sibling state.

### SC-011 Version vectors at the limit
- **Kind:** adversarial
- **Design:** §5.5 (`vv`), §9.2 (vector size), §9.3, §11.6
- **Binding:** any
- **Setup:** Part A: over five years J and A have used 30 device installations (phones replaced, restores, reinstalls), each with its own device ID, and each has written `House`'s item `notes-board`. Part B: default `Groceries` with M a writer.
- **Steps:**
  1. Part A: `J.mac sets notes-board.notes = "…"` after the 60th key change.
  2. Part B: `M.phone signs` one write to `milk.notes` whose context lists 65,000 invented device IDs.
  3. Part B: `M.phone signs` one whose context lists 1,024 invented device IDs. All sync.
  4. `J.mac removes M` (`E5`); `J.mac sets milk.notes = "2 cartons"`.
  5. `A.phone` deletes `milk` and creates it again.
- **Expect:**
  - Step 1: the new write's `base` has an entry for each of the 30 installations that ever wrote the field: about 1 KiB, far below the limit.
  - Step 2: invalid everywhere (over 1,024 entries).
  - Step 3: valid. Step 4's write can't carry `base` entries for all 1,024 plus its own: J's device writes with what fits, the field stays unsettled, and the app is told.
  - Step 5 settles it: the new record starts with empty vectors.
- **Invariants:** INV-CONVERGE, INV-UX
- **Notes:** The limit (§9.2, decided 2026-11-12) replaces the earlier finding that one malicious write made a field unwritable for good.

---

## Fault

### SC-012 More devices than the IMAP server allows connections
- **Kind:** fault
- **Design:** §10.3, §13.3
- **Binding:** imap
- **Setup:** Variant a: a Dovecot hub with `mail_max_userip_connections = 10`, the household behind one NAT address. Variant b: Stalwart's cap of 16 per account, J, A and three friends with 17 IMAP devices in all, from different addresses.
- **Steps:**
  1. All devices launch in turn.
  2. `A.phone sets milk.title = "Milk 2L"`.
  3. `J.mac quits`.
- **Expect (desired):**
  - Devices up to the cap hold IDLE. The next is refused, keeps working locally, and shows a named state rather than a reconnect loop.
  - The refused device still receives step 2's edit within a bounded time (by polling, or after step 3 frees a connection).
  - No device's retries take another device's connection away.
- **Invariants:** INV-UX, INV-CONVERGE
- **Notes:** The extra devices are outside the actor table. GAP (G-41): §10.3 now states both kinds of cap, and that a device uses one connection, but specifies no fallback (polling without IDLE, dropping IDLE when backgrounded, back-off), and §13.3 has no message for "the server refused: too many connections".

### SC-013 A hub approaching 100,000 messages
- **Kind:** fault
- **Design:** §1.2, §10.1, §11.6, §12.3, §13.3, Q3
- **Binding:** any
- **Setup:** After five years the household hub holds 92,000 messages. Liszto keeps completed items and tombstones last for good, so about 85,000 are item records. `House` alone has 20,000 records. B is a writer of `House`. The hub is configured to refuse appends beyond 100,000 messages.
- **Steps:**
  1. `J.mac removes B` from `House`.
  2. `A.phone adds bread to Groceries`.
  3. The harness raises the limit to 150,000; all sync.
- **Expect (desired):**
  - The key change stops in S3 at the limit, unannounced; every member stays on the old epoch. `J.mac` tells J the hub is full, with a named message.
  - `A.phone`'s new item stays local, marked unsynced, and is not lost.
  - After step 3, `J.mac` resumes from its journal, announces, and cleans up; `bread` is published. No duplicate messages remain once GC has run.
- **Invariants:** INV-NO-LOSS, INV-UX, INV-NO-HOLD-FOREVER, INV-MONOTONE-EPOCH
- **Notes:** See G-18 (no error model, no message). A key change briefly needs room for a second copy of the collection until S6; at 20,000 records that is 20,000 more messages. Liszto never deletes completed items and tombstones are kept for good (Q10), so its record count only grows; neither the design nor §17 provides archival.

---

## UX

### SC-014 A single value over the size limit
- **Kind:** ux
- **Design:** §10.4, §10.5, §10.6, §13.3
- **Binding:** any
- **Setup:** Default `Groceries`.
- **Steps:**
  1. J pastes 3 MB of text into `milk.notes` on `J.mac`.
  2. J pastes 700 KiB of text into `eggs.notes` on `J.mac`.
  3. `J.mac syncs`; `A.phone syncs`.
- **Expect (desired):**
  - For both pastes, Tandem refuses the write before signing it and returns an error Liszto shows ("Notes are too long to sync"). Nothing is appended.
  - `milk.notes` and `eggs.notes` keep their previous values on every device.
- **Invariants:** INV-UX, INV-CONVERGE
- **Notes:** §10.6 now says "a single write value must itself fit the message limit; apps put large content elsewhere". GAP (G-42, minor): the figure an app can check before accepting input isn't given. A 700 KiB value is already about 1.25 MB once `b64u` in JSON and base64 in the body are applied, and the limits are stated for messages, not values.
