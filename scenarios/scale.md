# Scale and longevity

Scope: how Tandem behaves with large collections, many collections and devices on one hub, records and fields too big for one message, logs and vectors that grow for years, a hub near its expected message ceiling, and bursts of rapid edits.

Conventions for this file: actors beyond the README's table are written `J.d01`…`J.d12` (extra devices of J) and `G001`… (guest users); the harness creates them, and each scenario that uses them says so. Bulk operations ("imports", "types for 90 s") are outside the step vocabulary and are flagged per scenario. Sizes are approximate: a data write without its value is about 250 bytes of JSON plus about 35 bytes per vector entry, a value grows by 4/3 as `b64u` inside the JSON, and the envelope grows by another 4/3 as base64 in the message body (§10.4, §10.5). "Kept", "displayed" and "reported" are as defined in `records.md`.

---

## Real-world

### SC-001 A list with 5,000 items, synced to a new device
- **Kind:** real-world
- **Design:** §1.2, §6.5, §10.1, §10.5, §10.6, §12.1
- **Binding:** any
- **Setup:** J owns `Recipes` (A writer, epoch 2) with 5,000 items, each with title, notes (~500 characters), tags, sortKey. All on the hub, one message per record.
- **Steps:**
  1. `J.phone pairs J.ipad`.
  2. `J.ipad syncs`.
  3. `A.phone sets item0001.title = "Bread v2"`; `J.ipad syncs`.
- **Expect:**
  - `J.ipad` fetches the bodies of exactly the 5,000 record messages and the log messages of `Recipes`' epoch-2 channel, plus J's home, J's and A's profiles. It fetches no other channel's bodies.
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
- **Notes:** Import and frozen clock are outside the step vocabulary. One message per record means bulk imports move the hub toward Q3's ceiling quickly (SC-011); provider rate limits are unknown (Q1).

### SC-003 Revoking a device of a user who owns 200 collections
- **Kind:** real-world
- **Design:** §6.6, §8.3, §11.4, §11.6, §14
- **Binding:** any
- **Setup:** J owns 200 collections, each shared with A as writer, averaging 50 records (10,000 records). J has `J.phone`, `J.mac`, `J.ipad`.
- **Steps:**
  1. `J.mac revokes J.ipad`.
  2. `J.mac crashes after` completing 80 of the 200 rotations.
  3. `J.phone syncs`.
  4. `J.mac launches`; all sync.
  5. `wait 30 days`; all sync.
- **Expect:**
  - J's PUK rotates once: one manifest of generation n+1, building on the revocation.
  - Every one of the 200 collections ends exactly one epoch ahead of where it started. The remaining 120 are rotated by `J.phone` or the relaunched `J.mac`, because "due" is a property of the log. Where both start the same collection, the genesis with the lower write ID wins and the other is abandoned.
  - Grants: one per member user per collection (400), sealed to each user's current `uka`, not one per device.
  - During the 30-day grace the hub holds about 10,000 extra record messages plus 200 copies of the logs. After step 5 the old channels keep only their `_epoch` records.
  - `J.ipad` holds no key of any new epoch.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-NO-LOSS, INV-GC-SAFE
- **Notes:** GAP: the design gives no pacing for a device that must rotate hundreds of collections at once. That is about 10,600 appends in one burst against provider limits (Q1) that aren't known, and nothing says what Liszto shows while it lasts beyond "lists re-key in the background" (§13.2). §14 already notes that each rotation exposes group size; here the hub also learns that one user owns all 200.

### SC-004 Tombstones piling up until a maintenance rotation
- **Kind:** real-world
- **Design:** §9.5, §11.6, §12.3, Q10
- **Binding:** any
- **Setup:** `Groceries` (J owner, A writer) has had no membership change for two years. Every day A clears about 30 completed items (30 `delete`s), so there are about 21,000 tombstoned records. `J.ipad` was paired 10 days ago; `J.mac` has followed the collection throughout.
- **Steps:**
  1. Simulated time reaches the day when `J.mac` holds 1,000 tombstones merged more than 180 days ago. Only `J.ipad` is online among J's devices; `J.ipad syncs`.
  2. `J.mac launches`; `J.mac syncs`.
  3. `wait 30 days`; all sync.
- **Expect:**
  - Step 2: `J.mac` starts a maintenance rotation. The new epoch omits every record whose tombstone `J.mac` merged more than 180 days ago and carries about 5,400 younger tombstones.
  - After step 3 the omitted records' messages are gone from the hub; record count on the hub falls accordingly.
  - Desired at step 1: `J.ipad` also sees the rotation as due.
- **Invariants:** INV-GC-SAFE, INV-MONOTONE-EPOCH
- **Notes:** GAP: the horizon is measured from when *the rotating device* merged each tombstone (§12.3). A device paired 10 days ago merged everything 10 days ago, so it sees nothing past the horizon and never compacts. The design doesn't say whether merge times travel with the replica (they aren't in any message), so how much is compacted depends on which device happens to rotate.

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
  - `J.ipad` reads the channel tag of every message (JMAP: `Email/query` and `Email/get` of headers; IMAP: header `UID FETCH`), but fetches bodies only for J's home, J's inbox, the profiles of A and B, and the current epochs of J's 41 collections.
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
  - Merged messages with identical writes but different envelopes are reduced to the one with the lowest `H(envelope)`.
  - After step 2 the hub holds exactly one message for `milk`, and every device shows all 12 values.
  - J's PUK manifest has 13 boxes (12 devices and the recovery key).
- **Invariants:** INV-CONVERGE, INV-QUIESCE, INV-GC-SAFE
- **Notes:** The extra devices are outside the actor table. §10.7's wait is "a few seconds at most" whatever the number of online devices; the harness records how many duplicates that gives at 12.

### SC-007 Five years of log, copied at every rotation
- **Kind:** edge
- **Design:** §8.1, §9.4, §10.6, §11.6, §12.1, §12.2, §12.4, Q16
- **Binding:** any
- **Setup:** `House` (J owner, A writer) has existed for five years. History: 60 rotations, 300 invites, 120 guests `G001`…`G120` who joined (writers) and later left themselves (`member:<self> status left`), 90 fences, 200 renames and icon changes. That is about 1,100 log writes, about 550 KB of JSON. Most guests have since deleted Liszto; their devices are gone.
- **Steps:**
  1. `J.mac rotates House`.
  2. `hub: destroy all`; `J.mac` and `A.phone` re-publish what they hold (§12.4).
  3. `C.phone joins via` a new QR invite to `House`; `J.mac` auto-approves.
- **Expect:**
  - After step 1 the epoch-61 channel holds the whole log, all ~1,100 writes, split across messages each under 64 KiB (about 12). The log is never truncated (§9.4).
  - Across five years, rotations have published the log 60 times (about 20 MB in total), but only the current epoch's copy remains beyond each 30-day grace.
  - Desired after step 3: `C.phone` validates the log, accepts the grant and shows `House`.
- **Invariants:** INV-CONVERGE, INV-AUTHZ, INV-NO-HOLD-FOREVER
- **Notes:** GAP: judging a log write needs its author's device chain (§6.2, §8.2), including guests who left years ago. §12.4 has each device re-publish its *own* profile, and healing (§12.2) covers only the current collection and home channels. Nothing says who keeps departed users' profiles on the hub. After step 2, every guest's `left` write has no chain to verify against, so `C.phone` holds them, and every write built on them, indefinitely. The same happens if Eve destroys a departed user's profile. Q16 (authenticated compaction) would also remove this dependency.

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
- **Notes:** Import is outside the step vocabulary. GAP: §10.7's rules work per message. No single message can dominate a split record, so "messages for one record that none of them dominates" is always true, and every device keeps re-publishing the merged state (split again), with no quiescence and no part ever garbage-collected. Healing (§12.2) uses "taken together"; publishing and GC don't. GAP: how a record is split ("the part that changed") isn't defined, so devices produce overlapping parts. GAP: it isn't said what the 64 KiB counts (plaintext, envelope or stored message, which differ by about 1.8×).

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
- **Notes:** Typing is outside the step vocabulary. GAP: §10.7 says "debounce ~1 s" with no maximum wait. A trailing debounce publishes nothing until J pauses, so `A.phone` sees nothing for 90 s, against §1.1's "within seconds". GAP: nothing says whether writes in the debounce window are persisted before they are published, so step 2 may lose the last second of typing (see also RC-035 for the HLC).

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
  - After step 3, kept {`j`} (its context entry `M.phone: T + 500` covers all 500). All of M's messages are dominated by `j`'s and destroyed; the hub holds one message for `milk`.
- **Invariants:** INV-CONVERGE, INV-GC-SAFE, INV-QUIESCE
- **Notes:** Between steps 2 and 3, devices meet the publishing problem of SC-008: no single message can dominate the 500-sibling state.

### SC-011 Version vectors that never shrink
- **Kind:** adversarial
- **Design:** §5.5 (`vv`), §9.2, §9.3, §10.6, §11.6
- **Binding:** any
- **Setup:** Part A: over five years J and A have used 30 devices (phones replaced, reinstalls), all but 5 now revoked. Each has written `House`'s item `notes-board`. Part B: default `Groceries` with M a writer.
- **Steps:**
  1. Part A: `J.mac sets notes-board.notes = "…"` after the 60th rotation.
  2. Part B: `M.phone signs` one write to `milk.notes` whose context lists 65,000 invented device IDs, each at hlc 1. All sync.
  3. `J.mac removes M`; rotation to epoch 5; `J.mac sets milk.notes = "2 cartons"`.
- **Expect:**
  - Step 1, as written: the new write's `base` has an entry for each of the 30 devices that ever wrote the field. Entries for revoked devices are never pruned.
  - Step 3, as written: J.mac's write inherits all 65,000 entries in its `base` (about 2.3 MB of JSON), over the 1 MiB message limit. The same holds for every later write to `milk.notes`, in every epoch, after M's removal too.
  - Desired: after the removal and rotation, `milk.notes` can be written within normal message sizes.
- **Invariants:** INV-CONVERGE; INV-NO-LOSS fails under the current text (see GAP)
- **Notes:** GAP: §9.2 builds `base` by joining the bases of kept writes, so vectors only grow; nothing prunes entries for revoked devices or rejects entries naming devices that exist in no chain. One malicious write makes a field permanently unwritable for everyone. That survives removal and rotation, against §9.3 ("the rotation that follows their removal ends it"). `vv()` encodes the count as `u16`, so 536 more entries (65,536 in all) make `sigInput` impossible to encode at all.

---

## Fault

### SC-012 More devices than the IMAP server allows connections
- **Kind:** fault
- **Design:** §10.3, §13.3
- **Binding:** imap
- **Setup:** A Dovecot hub with `mail_max_userip_connections = 10`. The household sits behind one NAT address: `J.phone`, `J.mac`, `J.ipad`, `A.phone`, `A.mac`, `J.d01`…`J.d06` (11 devices), all running Liszto.
- **Steps:**
  1. All 11 devices launch in turn.
  2. `A.phone sets milk.title = "Milk 2L"`.
  3. `J.mac quits`.
- **Expect (desired):**
  - Ten devices hold IDLE. The eleventh is refused, keeps working locally, and shows a named state rather than a reconnect loop.
  - The eleventh device still receives step 2's edit within a bounded time (by polling, or after step 3 frees a connection).
  - No device's retries take another device's connection away.
- **Invariants:** INV-UX, INV-CONVERGE
- **Notes:** The extra devices are outside the actor table. GAP: §10.3 acknowledges the per-user-and-address cap but specifies no fallback (polling without IDLE, dropping IDLE when backgrounded, back-off), and §13.3 has no message for "the server refused: too many connections".

### SC-013 A hub approaching 100,000 messages
- **Kind:** fault
- **Design:** §1.2, §10.1, §11.6, §12.3, §13.3, Q3
- **Binding:** any
- **Setup:** After five years the household hub holds 92,000 messages. Liszto keeps completed items, so about 85,000 are item records. `House` alone has 20,000 records. B is a writer of `House`. The hub is configured to refuse appends beyond 100,000 messages.
- **Steps:**
  1. `J.mac removes B` from `House`; `J.mac syncs` (rotation).
  2. `A.phone adds bread to Groceries`.
  3. The harness raises the limit to 150,000; all sync.
- **Expect (desired):**
  - The rotation stops at the limit with its genesis published and part of the snapshot appended. `J.mac` tells J that the hub is full, with a named message.
  - `A.phone`'s new item stays local, marked unsynced, and is not lost.
  - After step 3 the rotation completes (it is still due) and `bread` is published. No duplicate messages remain once GC has run.
- **Invariants:** INV-NO-LOSS, INV-UX, INV-NO-HOLD-FOREVER, INV-MONOTONE-EPOCH
- **Notes:** GAP: the abstract store (§10.1) has no error model, and §13.3 has no message for an append refused by quota or a folder limit. It isn't said whether a rotation interrupted by such an error is abandoned, retried or replaced (§11.6 covers only an author who vanishes). GAP: a rotation doubles a collection's messages for 30 days, so a hub's practical ceiling is about half of Q3's figure. Liszto never deletes completed items, so its record count only grows; neither the design nor §17 provides archival.

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
- **Notes:** Pasting is outside the step vocabulary. GAP: the design limits messages (64 KiB SHOULD, 1 MiB MUST) but not values. A single write can't be split, and a 700 KiB value is already about 1.25 MB once `b64u` in JSON and base64 in the body are applied, so it can never be published. There is no API error, no §13.3 message, and no figure the app can check against before accepting input.
