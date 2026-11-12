# Fault scenarios

Network trouble between devices and the hub, crashes at the points where a half-done operation matters (most of them now steps of the owner's journalled key change, §11.6), devices that are away for a day, a month or a year, wrong clocks, suspended mobile apps, revoked credentials and a full mailbox.

Binding `jmap+imap` marks a mixed scenario; the Setup says which device uses which binding. Network steps run through Toxiproxy between each device and Stalwart.

---

### FT-001 A slow link everywhere
- **Kind:** fault
- **Design:** §10.2, §10.3, §10.7
- **Binding:** any
- **Setup:** J's three devices and A's two follow House. Every device has `net: latency 2000 ms` with 1000 ms jitter.
- **Steps:**
  1. For 10 minutes, the harness has random devices add, edit, tag and delete items in House, 2 per second.
  2. Stop editing; all devices sync.
- **Expect:**
  - Within 2 minutes of the last edit, every device holds the same House and no device publishes again.
  - No JMAP device reopens its EventSource while pings arrive within 120 s.
  - After quiescence, each record has one message.
- **Invariants:** INV-CONVERGE, INV-QUIESCE, INV-GC-SAFE

### FT-002 One device partitioned for an hour while it edits
- **Kind:** real-world
- **Design:** §1.1, §10.7, §13.2, §13.3
- **Binding:** any
- **Setup:** `J.mac` and `J.phone` follow House.
- **Steps:**
  1. `net: partition J.phone`.
  2. `J.phone` adds 5 items, edits Milk's title 30 times, and deletes Bread.
  3. `J.mac` edits Milk's notes.
  4. `wait 1 hour`; the partition is lifted; both devices sync.
- **Expect:**
  - During the partition, `J.phone` shows the offline indicator and every edit applies locally at once.
  - After step 4, `J.phone` publishes each record it changed at most once (debounced), within one sync.
  - Both devices show the 5 items, the last title, `J.mac`'s notes, and Bread deleted.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS, INV-QUIESCE

### FT-003 A JMAP append whose response is lost
- **Kind:** fault
- **Design:** §10.2, §10.4, §10.7
- **Binding:** jmap
- **Setup:** `J.mac` follows House.
- **Steps:**
  1. `net: drop after <n> bytes` on `J.mac`'s downstream, timed so that the `Email/set` request reaches Stalwart and the response is cut.
  2. `J.mac adds Milk to House`.
  3. The drop is lifted; `J.mac syncs`.
  4. Variant: Stalwart did not store the create.
- **Expect:**
  - `J.mac` keeps Milk queued until it finds a message on the hub whose `H(envelope)` equals the envelope it sent; it identifies its message by that hash, not by an id.
  - After step 3 it finds the envelope and doesn't append again; the hub holds one message for Milk.
  - Variant: it finds no such envelope and appends the same envelope again.
  - If a retry did produce byte-identical copies, only `J.mac` (which made the envelope) removes the extras, keeping one (§10.7).
  - `J.mac` shows nothing more than the sync indicator.
- **Invariants:** INV-NO-LOSS, INV-GC-SAFE, INV-QUIESCE

### FT-004 An IMAP append that completed without a reply
- **Kind:** fault
- **Design:** §10.2, §10.3, §10.4, §10.7
- **Binding:** imap
- **Setup:** `A.mac` follows House. Its last known UID is 200.
- **Steps:**
  1. `A.mac sets Milk.notes = "2 l"`. The `APPEND` literal reaches Stalwart (UID 201), then `net: reset` cuts the connection before the tagged `OK`/`APPENDUID`.
  2. Before `A.mac` reconnects, `J.mac` appends UID 202.
  3. `A.mac` reconnects and syncs.
- **Expect:**
  - `A.mac` finds UID 201 through `UID FETCH 201:*` and fetches 201 and 202.
  - It recognises 201 as its own by `H(envelope)` and doesn't append again.
  - `A.mac`'s last known UID is 202.
  - The note shows on every device.
- **Invariants:** INV-NO-LOSS, INV-GC-SAFE, INV-CONVERGE

### FT-005 Connection reset mid-fetch
- **Kind:** fault
- **Design:** §10.2, §10.3, §10.4, §12.2
- **Binding:** any
- **Setup:** `J.phone` is behind by 40 messages in House, among them an 800 KiB record.
- **Steps:**
  1. `net: drop after <n> bytes` on `J.phone`, cutting partway through the large body (an IMAP literal or JMAP response).
  2. The drop is lifted; `J.phone syncs`.
- **Expect:**
  - `J.phone` merges nothing from the cut response.
  - It doesn't log the large message as undecryptable or junk.
  - It doesn't heal any House record while its sync is incomplete.
  - After step 2, it holds all 40 messages' writes and publishes nothing.
- **Invariants:** INV-CONVERGE, INV-QUIESCE

### FT-006 Connection reset while reading the change feed
- **Kind:** fault
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** `J.phone` is behind by 500 changes. Stalwart returns at most 50 per `Email/changes`.
- **Steps:**
  1. `net: reset` `J.phone` after its 4th `Email/changes` page (JMAP), or partway through the `UID FETCH <n>:*` header response (IMAP).
  2. `J.phone syncs` again.
- **Expect:**
  - `J.phone`'s stored cursor never moves past changes whose added ids it hasn't recorded.
  - After step 2, `J.phone` knows every message in Tandem and holds the same House as `J.mac`.
  - No message is fetched twice by body (checked by `H(envelope)`).
- **Invariants:** INV-CONVERGE, INV-NO-LOSS

### FT-007 Reset between `\Deleted` and `UID EXPUNGE`
- **Kind:** fault
- **Design:** §10.3, §10.7
- **Binding:** jmap+imap (`A.mac` IMAP, `J.mac` JMAP)
- **Setup:** Milk has two messages, `m1` and `m2`, where `m2` dominates `m1`.
- **Steps:**
  1. `A.mac` runs `UID STORE m1 +FLAGS.SILENT (\Deleted)`; `net: reset` before `UID EXPUNGE`.
  2. `J.mac syncs`.
  3. `A.mac` reconnects and syncs.
- **Expect:**
  - In step 2, `J.mac` treats `m1` as present (it may see the `$deleted` keyword).
  - In step 3, `m1` is expunged by `A.mac` or by any device's next GC.
  - No device treats a message as removed because it has the `\Deleted` flag.
  - `m2` stays.
- **Invariants:** INV-GC-SAFE

### FT-008 Server restart during a key change
- **Kind:** fault
- **Design:** §11.6, §10.2, §10.3
- **Binding:** any
- **Setup:** House has 100 records. J is owner (`J.mac`), A and B are writers.
- **Steps:**
  1. `J.mac removes B`.
  2. While `J.mac` is uploading to the new channel (S3), Stalwart restarts, and every connection is reset for 20 s.
  3. All devices sync.
- **Expect:**
  - `J.mac` resumes S3 from its journal with the same signed bytes and finishes the change.
  - The new channel holds the inventory, every carried record and the roster before the announcement appears in the old channel.
  - Exactly one new statement exists; an identical message appended twice counts once.
  - A receives a grant and switches. B doesn't.
  - Until the announcement, A keeps publishing to the old epoch; nothing stalls.
  - EventSource and IDLE reconnect within 10 s of the restart ending.
- **Invariants:** INV-CUT, INV-NO-LOSS, INV-MONOTONE-EPOCH, INV-ONE-OWNER

### FT-009 Crash after publishing a merge, before GC
- **Kind:** fault
- **Design:** §10.7
- **Binding:** any
- **Setup:** Milk has two messages, `m1` and `m2`, that neither dominates. `J.mac` and `A.mac` are online.
- **Steps:**
  1. `J.mac crashes after` appending its merge `n`, before destroying `m1` and `m2`.
  2. `A.mac syncs`.
  3. `J.mac launches`.
- **Expect:**
  - `A.mac` doesn't publish another merge, since `n` dominates. It may destroy `m1` and `m2`.
  - After step 3, the hub holds only `n` for Milk.
  - Milk is the same on both devices.
- **Invariants:** INV-GC-SAFE, INV-QUIESCE

### FT-010 Crash between the announcement and the grants
- **Kind:** fault
- **Design:** §11.6, §11.4, §11.7
- **Binding:** any
- **Setup:** House. J is owner on `J.mac` and `J.phone`; A, B and C are writers.
- **Steps:**
  1. `J.mac removes B`.
  2. `J.mac crashes after` S4 (announced; key moved into `keys`), before any grant.
  3. During the next 25 minutes, `A.mac` and `C.phone` each add an item.
  4. Variant a: `J.mac launches` 5 minutes after the crash. Variant b: `J.mac` stays down; `J.phone` is online.
- **Expect:**
  - From the announcement on, no device publishes data to the old epoch; `A.mac`'s and `C.phone`'s items are queued.
  - Variant a: `J.mac` resumes at S5 from its journal and sends the grants.
  - Variant b: at 10 minutes A and C send key requests; `J.phone` answers with the current key (the key is in home).
  - All members except B end in the new epoch with the queued items published. B can't read it.
- **Invariants:** INV-CUT, INV-NO-LOSS, INV-NO-HOLD-FOREVER, INV-MONOTONE-EPOCH

### FT-011 Crash partway through uploading the new channel
- **Kind:** fault
- **Design:** §11.6 (S3), §11.7, §8.3, §12.2
- **Binding:** any
- **Setup:** House has 40 records. J is owner (`J.mac`, `J.phone`), A and B are writers, and C is a reader (`C.phone`).
- **Steps:**
  1. `J.mac removes B`.
  2. `J.mac crashes after` uploading the inventory and 25 of the 40 records to the new channel, before the roster and the announcement.
  3. Variant a: `J.mac launches` 2 minutes later. Variant b: J opens House sharing on `J.phone` and chooses "Do it here".
  4. All devices sync.
- **Expect:**
  - Until the announcement, every member stays on the old epoch and keeps publishing there; none sees anything of the half-uploaded channel.
  - Variant a: `J.mac` uploads the remaining 15 records and the roster with the journal's bytes, verifies, then announces.
  - Variant b: `J.phone` destroys the partial channel, removes the note, and makes the change itself with a new key.
  - In both, all 40 records are in the new epoch on every device.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE, INV-CUT

### FT-014 Crash during first run, before the backup key is shown
- **Kind:** fault
- **Design:** §6.1, §6.3, §13.1
- **Binding:** any
- **Setup:** `J.mac` has local lists and no identity.
- **Steps:**
  1. `J.mac` connects the hub.
  2. `J.mac crashes after` creating home (§6.1 step 3), before showing the backup key and before uploading the local lists.
  3. `J.mac launches`.
- **Expect:**
  - `J.mac` keeps its `US` and user ID, shows the backup key and asks for confirmation.
  - The hub ends with one home for J.
  - J's local lists are uploaded once, each as one collection.
- **Invariants:** INV-UX, INV-UNIQUE

### FT-015 Crash during pairing
- **Kind:** fault
- **Design:** §6.3
- **Binding:** any
- **Setup:** J has `J.mac` and `J.phone`.
- **Steps:**
  1. `J.mac pairs J.ipad`; J approves.
  2. Variant a: `J.mac crashes after` the approval, before posting the reply. Variant b: `J.ipad crashes after` receiving the reply, before storing `US`.
  3. The crashed device launches.
- **Expect:**
  - Variant a: no reply arrives; `J.ipad` shows a named state and, once the QR has expired, "This code has expired. Ask for a new one."; a new pairing works.
  - Variant b: `J.ipad` has no identity; a new pairing works. The old reply stays sealed to an `ek` whose private key is gone (see G-01).
  - Nothing else on the hub changes.
- **Invariants:** INV-NO-HOLD-FOREVER, INV-UX

### FT-016 Crash after admitting a joiner, before the grant
- **Kind:** fault
- **Design:** §11.3, §11.4
- **Binding:** any
- **Setup:** J owns House. A has sent a join request through a link invite.
- **Steps:**
  1. `J.mac approves A`.
  2. `J.mac crashes after` the admitting statement is on the hub, before sending the grant or destroying the request. `J.mac` stays down.
  3. `wait 15 min`; `J.phone` is online throughout.
- **Expect:**
  - `J.phone` sees the still-present request from someone who is already a member and sends the grant again.
  - Failing that, at 10 minutes `A.phone` sends a key request to J's inbox, and `J.phone` answers.
  - `A.phone` shows House within 1 minute of either.
  - The join request is destroyed once A's grant is sent.
- **Invariants:** INV-NO-HOLD-FOREVER, INV-AUTHZ

### FT-017 A lost HLC and a clock that went back
- **Kind:** fault
- **Design:** §9.1, §9.2, §5.5
- **Binding:** any
- **Setup:** `J.mac` and `A.mac` follow House. `J.mac`'s clock is 2 minutes fast.
- **Steps:**
  1. `J.mac sets Milk.title = "Oat milk"` (HLC `h1`). It publishes, and `A.mac syncs`.
  2. `A.mac sets Milk.title = "Soy milk"`; its context covers `h1`. `J.mac offline`.
  3. `J.mac crashes after` the publish in step 1, before its HLC state is saved.
  4. `J.mac clock = -2 minutes` (corrected); `J.mac launches` offline.
  5. `J.mac sets Milk.title = "Rice milk"` (HLC `h2 < h1`).
  6. `J.mac online`; both devices sync.
- **Expect:**
  - `h2` is greater than `h1`; `J.mac`'s HLCs never go backwards.
  - After step 6, both devices hold "Soy milk" and "Rice milk" as concurrent siblings.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE
- **Notes:** GAP (G-20): §9.1 says a device's HLCs strictly increase, but it doesn't require the HLC to be stored durably before a write leaves the device, or say how to recover it. If it is lost and the clock is behind, the new write's dot falls under entries other devices' contexts already cover. "Soy milk" then supersedes "Rice milk" although it never saw it, and the write is lost without any error. Recovery could take the maximum of the device's own dots found on the hub before writing. The same applies to the HLC that §17 shares with the share extension.

### FT-018 Offline for a day
- **Kind:** real-world
- **Design:** §10.2, §10.3, §10.7, §12.2
- **Binding:** any
- **Setup:** `J.ipad` follows House and Groceries. It goes offline with 3 unsynced edits.
- **Steps:**
  1. During the day, J and A make 200 edits; GC runs.
  2. `wait 1 day`; `J.ipad online`; `J.ipad syncs`.
- **Expect:**
  - `J.ipad` fetches bodies only for messages added since its cursor that belong to channels it follows.
  - It publishes its 3 edits.
  - It republishes nothing the hub already dominates.
  - It converges with `J.mac` within one sync.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS, INV-QUIESCE

### FT-019 Offline for a month, across a key change
- **Kind:** real-world
- **Design:** §11.6, §11.4, §8.3, §10.2, §12.3
- **Binding:** any
- **Setup:** House is in epoch `E2`. `A.phone` goes offline with unsynced edits to Milk and Jam. `A.mac` stays online.
- **Steps:**
  1. Day 1: `J.mac removes B`; House moves to `E3`. `A.mac` consumes A's grant.
  2. Day 3: `J.mac deletes Jam`.
  3. Day 4: `J.mac` finishes S6 (old data messages destroyed; the old `_roster` stays).
  4. Day 32: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - `A.phone` handles `cannotCalculateChanges` or `invalidArguments` by a full resync.
  - It reads the announcement in channel `E2`'s `_roster`, finds `K(E3)` in A's home, and moves.
  - It signs its Milk and Jam edits again into `E3`.
  - Jam stays deleted on every device.
  - `A.phone` appends nothing under the `E2` tag.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH, INV-CUT, INV-CONVERGE

### FT-020 Offline for a year
- **Kind:** edge
- **Design:** §12.3, §11.6, §8.3
- **Binding:** any
- **Setup:** `A.phone` goes offline with unsynced edits to Milk and Bread. `A.mac` stays active.
- **Steps:**
  1. Day 10: `J.mac deletes Bread`.
  2. Day 195: J removes B from House (a key change).
  3. Day 365: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - No "long absence" handling: `A.phone` moves to the current epoch and signs its edits again.
  - Bread stays deleted on every device: tombstones are kept for good and carried at every key change (§12.3).
  - Milk shows `A.phone`'s edit unless a later write superseded it.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE

### FT-021 An owner device's clock a year fast
- **Kind:** fault
- **Design:** §9.1, §9.4 (holding limits), §11.1, §11.6, §11.9
- **Binding:** any
- **Setup:** House is in `E3`. J is owner (`J.mac`); A and B are writers. `A.phone` is offline with an unsynced edit to Bread's notes. Day 0: `A.mac deletes Bread`; `J.mac` merges the tombstone.
- **Steps:**
  1. Day 1: `J.mac clock = +1 year`.
  2. `J.mac removes B`; House moves to `E4`. J creates a 7-day link invite on `J.mac`.
  3. `J.mac` deletes Groceries.
  4. Day 3: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - Bread stays deleted on every device, including in `E4`: nothing about tombstones depends on a clock.
  - The invite's `exp` is a year late; the harness records how long it is accepted.
  - `J.mac` destroys Groceries' data on the hub at once (its 30 days have "passed"); members keep their local read-only copies for their own 30 days and the roster stays.
  - Held items on `J.mac` may be evicted locally at once; no hub message is destroyed because of that.
- **Invariants:** INV-CONVERGE, INV-GC-SAFE
- **Notes:** The timers that remain (invite expiry, holding limits, the 30-day read-only periods, the deleted collection's cleanup, the 10-minute key-request wait, the pairing QR) run on device clocks. Only invite expiry decides anything for other users (G-12).

### FT-022 A device clock years behind
- **Kind:** fault
- **Design:** §9.1, §9.2, §10.4, §13.3, §14
- **Binding:** any
- **Setup:** Stalwart's test certificate is valid from 2000 to 2100. `A.phone` is A's second device, and `A.mac` is active.
- **Steps:**
  1. `A.phone clock = -3 years`.
  2. `A.phone sets Milk.notes`, while `J.mac` sets Milk.notes concurrently.
  3. Variant: with Stalwart's real certificate (valid from 2026), `A.phone` tries to sync.
- **Expect:**
  - Step 2: both notes are kept as siblings. The displayed one is `J.mac`'s, by the `(epoch, hlc, …)` order.
  - Step 3: `A.phone` shows a named error about the date and time, not just the offline indicator.
- **Invariants:** INV-CONVERGE, INV-UX, INV-NO-LOSS
- **Notes:**
  - GAP (G-15): §13.3 maps every connection failure to "Hub unreachable". A TLS failure caused by the device's own clock leaves it showing offline for ever, with no hint why.
  - `A.phone`'s `Date` headers are 3 years old, which marks its messages as its own (HB-039).

### FT-023 HLC counter overflow
- **Kind:** edge
- **Design:** §9.1, §5.5, §10.5, §17
- **Binding:** any
- **Setup:** `J.mac` has just written with HLC millis `T`.
- **Steps:**
  1. `J.mac clock = -1 hour`.
  2. `J.mac` imports 70,000 items into House within the hour. Meanwhile the macOS share extension saves 1,000 links into House.
  3. `J.mac clock = +1 hour` (back to correct); `J.mac adds` one more item.
- **Expect:**
  - All 71,001 dots of `J.mac` and its extension are distinct and strictly increasing in `(millis, counter)`.
  - Millis stays at `T` until the counter passes 65,535, then becomes `T+1` with counter 0.
  - After step 3, millis jumps to the current time.
  - `A.mac` accepts every write. Signatures verify with the `u16` counter, and the JSON numbers survive exactly.
- **Invariants:** INV-SIGNED, INV-CONVERGE, INV-NO-LOSS
- **Notes:** Volume overlaps with `scale.md`; this scenario is about the counter rule.

### FT-024 An iPhone that is suspended, and its share extension
- **Kind:** real-world
- **Design:** §17, §1.1, §10.2, §11.6, §3.3
- **Binding:** any
- **Setup:** `J.phone` follows House. J is owner on `J.mac`; A and B are writers.
- **Steps:**
  1. `J.phone` goes to the background and is suspended for 6 hours. Background refresh is disabled.
  2. Hour 1: `A.mac` adds 10 items.
  3. Hour 2: `J.mac removes B`, and House moves to a new epoch.
  4. Hour 3: on `J.phone`, the user shares a URL into House through the share extension. The app stays suspended.
  5. Hour 6: `J.phone launches`.
- **Expect:**
  - While suspended, `J.phone` holds no EventSource or IDLE session, according to Stalwart's session list after the server's idle timeout.
  - Step 4: the shared item is written to the new epoch, or queued for the app. Nothing is appended under the old epoch's tag after the announcement.
  - Step 5: `J.phone` runs `changes` and shows the 10 items and the shared URL within 10 s of launch.
  - B can't read the shared URL.
- **Invariants:** INV-CUT, INV-CONVERGE, INV-NO-LOSS
- **Notes:**
  - GAP (G-16): §17 has the extension append "one record message" using the hub credential, without syncing. An extension that hasn't seen the announcement publishes into the old epoch, where B can read it, for as long as the app stays suspended; §3.3 bounds that window to "the moments before they see the new one announced". The write itself survives (the app signs it again later), but the design should say whether the extension must check House's `_roster` first or hand the write to the app.
  - Suspending the app and running the extension are outside the vocabulary (`xcrun simctl` or a real device).

### FT-025 An IDLE connection silently dropped by a NAT
- **Kind:** fault
- **Design:** §10.3, §1.1
- **Binding:** imap
- **Setup:** `A.mac` is in the foreground, in IDLE.
- **Steps:**
  1. At minute 0: `net: blackhole A.mac` (silent drop both ways, no reset), as a NAT does when it forgets a mapping.
  2. At minute 2: `J.mac adds Milk to House`.
  3. At minute 40: the harness resets the blackholed connection.
- **Expect:**
  - `A.mac` shows Milk no later than 31 minutes after step 2: the 29-minute renewal plus a 2-minute response timeout.
  - Until it notices, `A.mac`'s sync indicator doesn't claim to be up to date for more than that bound.
  - Afterwards, `A.mac` holds exactly one IMAP session.
- **Invariants:** INV-CONVERGE, INV-UX
- **Notes:** GAP (G-21): the JMAP binding has a liveness rule (ping every 60 s, reconnect after two missed). The IMAP binding has only "re-issue IDLE at least every 29 minutes". It sets no timeout for the server's reply to `DONE`, and has no check in between. A silent drop therefore delays push by up to 29 minutes plus TCP's retransmission timeout, while the device shows itself online. Many NATs and mobile carriers drop idle mappings after 5 minutes or less. Possible fix: renew every few minutes, with a reply timeout of seconds. `net: blackhole` is a Toxiproxy `timeout` toxic with timeout 0.

### FT-026 A flapping network
- **Kind:** fault
- **Design:** §10.2, §10.3, §10.7
- **Binding:** jmap+imap (`J.phone` JMAP, `A.phone` IMAP)
- **Setup:** Both devices follow House. `J.mac` is stable.
- **Steps:**
  1. For 10 minutes, `J.phone` and `A.phone` alternate between `online` and `net: reset` every 20 s. Each makes an edit every 15 s.
  2. The network stabilises; all devices sync.
- **Expect:**
  - Every edit appears on `J.mac`.
  - After GC, each record has one message.
  - At no point does Stalwart hold more than 2 IMAP sessions for `A.phone`, or more than 2 EventSource streams for `J.phone`.
  - Within 2 minutes of step 2, no device publishes.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE, INV-QUIESCE
- **Notes:** Leaked sessions count against the server's connection cap (HB-017).

### FT-027 A per-device credential revoked mid-session
- **Kind:** fault
- **Design:** §13.3, §13.1, §3.3, §10.2, §10.3
- **Binding:** jmap+imap (`A.mac` JMAP with its own token; `A.phone` IMAP with its own app password)
- **Setup:** Both of A's devices follow House. `A.mac` is publishing 10 records in one `Email/set` batch series. `A.phone` is in IDLE.
- **Steps:**
  1. `hub: reject auth for A.mac` between `A.mac`'s second and third batch.
  2. `hub: reject auth for A.phone`.
  3. `A.mac` and `A.phone` keep editing locally for 10 minutes.
  4. `J.mac adds Milk to House`.
- **Expect:**
  - `A.mac` shows "Sync access was revoked or changed. Reconnect to keep syncing." on its first rejected request.
  - The records `A.mac` sent before step 1 are on the hub; the rest stay queued.
  - After the first rejection, each device makes no more than 1 further login attempt until the user reconnects (server auth log).
  - Local edits keep working, and local data is untouched.
  - Within 1 minute of step 2, `A.phone` can no longer append. Whether its existing IDLE session still delivers Milk is recorded.
- **Invariants:** INV-UX, INV-NO-LOSS
- **Notes:**
  - GAP (G-22): §3.3 says revoking the hub credential "removes even that" (vandalism), and §13.1 uses per-device tokens to cut off a stolen device. On many servers an IMAP session that logged in before the revocation, or an open EventSource stream, survives until it disconnects. The design should tell the hub owner to end open sessions too, or say that the cut takes effect at the next login. The scenario records what Stalwart does.
  - Repeated failed logins can trip Stalwart's blocking of failing addresses. That would lock out every device in the household behind the same address, which is why the retry bound matters.

### FT-028 The shared credential is changed
- **Kind:** real-world
- **Design:** §13.1, §13.3, §11.2, §1.2
- **Binding:** any
- **Setup:** All devices use one shared credential. A has queued edits.
- **Steps:**
  1. J changes the account's password or token at Stalwart and enters the new one on `J.mac`.
  2. Every other device syncs and is rejected.
  3. `J.mac` shows the hub QR; `A.phone` scans it.
  4. `A.phone syncs`.
- **Expect:**
  - Step 2: each device shows "Sync access was revoked or changed. Reconnect to keep syncing."
  - Step 4: `A.phone` keeps its user ID, device ID, memberships and replicas; it doesn't run first run, show a backup key, or create a collection; it publishes its queued edits.
  - Devices that haven't reconnected keep working locally.
- **Invariants:** INV-UX, INV-NO-LOSS, INV-ONE-OWNER
- **Notes:** GAP (G-17): "Reconnect" isn't a defined flow. Scanning a hub QR on a device that already has a hub is, by §11.2 and §1.2, either a "different hub" (see G-13) or a fresh connect, and §13.1's connect sets up an identity and uploads local data as new collections. The design needs a credential-update path that keeps the identity and the cursors.

### FT-029 The mailbox is over quota
- **Kind:** fault
- **Design:** §10.7, §11.6, §13.3, §10.2, §10.3
- **Binding:** any
- **Setup:** Stalwart's quota for the account is set to current usage plus 200 KiB. House has 300 records; J is owner, A and B are writers.
- **Steps:**
  1. `J.mac` and `A.mac` make 300 edits over 10 minutes, including concurrent edits to 20 records.
  2. `J.mac removes B`.
  3. The harness raises the quota; all devices sync.
- **Expect:**
  - Steps 1–2: every rejected append (JMAP `overQuota`, IMAP `NO [OVERQUOTA]`) leaves its writes queued, and no write is dropped.
  - Steps 1–2: each device shows a named "sync mailbox is full" message.
  - Steps 1–2: no device destroys a message to make room unless it is dominated.
  - Step 2: the key change stops in S3 and stays unannounced, so every member keeps working in the old epoch; it resumes after step 3 from its journal.
  - Step 3: every queued write is published within one sync, B is cut, and each record has one message after GC.
- **Invariants:** INV-NO-LOSS, INV-CUT, INV-UX, INV-GC-SAFE
- **Notes:**
  - GAP (G-18): §13.3 has no message for a full mailbox, and the abstract store (§10.1) has no error model to tell a quota refusal from other failures.
  - GC appends a merge before it destroys (§10.7), so at quota the dominated pairs stay until space returns. That is safe, and expected.

### FT-030 A hub participant fills the quota with junk
- **Kind:** adversarial
- **Design:** §10.7, §12.2, §3.3, §1.2
- **Binding:** any
- **Setup:** Stalwart has a quota. House and Groceries are in use.
- **Steps:**
  1. E runs `hub: inject` with 64 KiB junk messages under House's tag and under random tags until the quota is full.
  2. J and A edit; all devices sync.
  3. The harness deletes E's messages through raw hub access.
  4. All devices sync.
- **Expect:**
  - Step 2: devices show the full-mailbox message and queue their edits.
  - Step 2: no device destroys a message it hasn't merged.
  - Step 4: every queued edit is published, and House and Groceries converge.
- **Invariants:** INV-NO-LOSS, INV-UX
- **Notes:** GAP (G-19, see HB-037): a device can prove that a version-1 envelope under a collection tag is junk (AEAD fails under that tag's key), but §10.7 doesn't let it destroy it. Only the hub owner can clean up, using a mail client, and they can't tell junk under unfamiliar tags from other users' channels.

### FT-031 Offline edits survive quitting and relaunching while offline
- **Kind:** real-world
- **Design:** §1.1, §10.7
- **Binding:** any
- **Setup:** `J.phone` follows House.
- **Steps:**
  1. `J.phone offline`; it adds 3 items and edits Milk.
  2. `J.phone quits`; `J.phone launches` (still offline); it edits Milk again.
  3. `J.phone crashes after` a local edit; `J.phone launches`.
  4. `J.phone online`; `J.phone syncs`.
- **Expect:**
  - Every edit from steps 1–3 is visible on `J.phone` after each relaunch.
  - Every edit reaches the hub after step 4, and `J.mac` shows them.
  - Each record changed offline is published once.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE, INV-QUIESCE

### FT-032 GC must wait for its own append to land
- **Kind:** fault
- **Design:** §10.2, §10.7
- **Binding:** any
- **Setup:** Milk has two messages, `m1` and `m2`, that neither dominates. `J.mac` alone is online.
- **Steps:**
  1. `J.mac` publishes the merge `n`. `net: drop after <n> bytes` cuts the upload, and Stalwart doesn't store `n`.
  2. `J.mac` continues; the drop is lifted after 30 s; `J.mac syncs`.
- **Expect:**
  - `J.mac` doesn't destroy `m1` or `m2` before it has seen a message with `n`'s `H(envelope)` on the hub ("another message on the hub dominates it").
  - At every moment, the hub holds messages that together dominate Milk (harness polls).
  - After step 2, only `n` (re-sent) remains.
- **Invariants:** INV-GC-SAFE, INV-NO-LOSS

### FT-033 The hub hides a key change from the owner's other device
- **Kind:** adversarial
- **Design:** §11.7, §9.4, §8.2
- **Binding:** any
- **Setup:** House: J owner (`J.phone`, `J.mac`), A and B writers.
- **Steps:**
  1. `J.phone removes B`; the hub withholds from `J.mac` J's home changes (the note) and House's new `_roster` messages.
  2. On `J.mac`, J makes A a reader.
  3. The hub stops withholding; all sync.
- **Expect:**
  - Two owner-signed successors of one statement are on the hub once both are announced: House is dead on every device holding both (§9.4).
  - Every device stops publishing and merging House, keeps its replica read-only and offers a copy.
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER
- **Notes:** Accepted by the design (§11.7). The same outcome as `rotation.md` RO-047, here driven through the testbed's withholding proxy.

### FT-034 Crash between the announcement and moving the key
- **Kind:** fault
- **Design:** §11.6 (S4), §11.7
- **Binding:** any
- **Setup:** House: J owner (`J.phone`, `J.mac`), A writer.
- **Steps:**
  1. `J.phone removes B` and `crashes after` publishing the announcement, before moving the key from the note into `keys`.
  2. J opens House sharing on `J.mac`.
- **Expect:**
  - `J.mac` finds the note and the announcement: the change was made. It moves the key into `keys`, removes the note, sends grants and cleans up, telling J it finished a change started on the iPhone.
  - When `J.phone` returns, it sees the announcement and carries on from S5 (re-sending grants is harmless).
- **Invariants:** INV-ONE-OWNER, INV-NO-HOLD-FOREVER

### FT-035 A journal lost after the note reached home
- **Kind:** fault
- **Design:** §11.6 (S2), §11.7
- **Binding:** any
- **Setup:** House: J owner (`J.phone`, `J.mac`).
- **Steps:**
  1. `J.phone removes B`; it writes the note to home; then its storage is rolled back (harness), losing the journal entry but not the note on the hub.
  2. `J.phone launches`.
- **Expect (desired):**
  - `J.phone` can't resume (it lacks the signed bytes), so it treats its own note as §11.7 treats another device's: the statement isn't on the hub, so it asks J "do it here or discard".
- **Invariants:** INV-ONE-OWNER, INV-UX
- **Notes:** See G-07: §11.6 S2 doesn't order the note and the journal, and §11.7 says the first device "resumes" while its note is there.

---

Retired: FT-012, FT-013 (per-user key manifests and home generations belong to withdrawn designs, §19).
