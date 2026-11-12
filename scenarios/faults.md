# Fault scenarios

Network trouble between devices and the hub, crashes at the points where a half-done operation matters, devices that are away for a day, a month or past the horizon, wrong clocks, suspended mobile apps, revoked credentials and a full mailbox.

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
- **Expect:**
  - Stalwart stored the first create, and `J.mac` keeps Milk queued until it sees a message holding Milk on the hub.
  - After step 3, the hub holds one or two messages for Milk, and one after GC.
  - `J.mac` shows nothing more than the sync indicator.
- **Invariants:** INV-NO-LOSS, INV-GC-SAFE, INV-QUIESCE
- **Notes:** The design doesn't say whether a retried append reuses the envelope bytes. If it does, the result is byte-identical copies, whose GC tie-break is undefined (HB-024).

### FT-004 An IMAP append that completed without a reply
- **Kind:** fault
- **Design:** §10.3, §10.4, §10.7
- **Binding:** imap
- **Setup:** `A.mac` follows House. Its last known UID is 200.
- **Steps:**
  1. `A.mac sets Milk.notes = "2 l"`. The `APPEND` literal reaches Stalwart (UID 201), then `net: reset` cuts the connection before the tagged `OK`/`APPENDUID`.
  2. Before `A.mac` reconnects, `J.mac` appends UID 202.
  3. `A.mac` reconnects and syncs.
- **Expect:**
  - `A.mac` finds UID 201 through `UID FETCH 201:*` and fetches 201 and 202.
  - It recognises 201 as its own write and doesn't count the write as lost.
  - If it appended again, the hub holds two copies, and one after GC.
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

### FT-008 Server restart during a rotation
- **Kind:** fault
- **Design:** §11.6, §10.2, §10.3
- **Binding:** any
- **Setup:** House has 100 records. J is owner (`J.mac`), A and B are writers.
- **Steps:**
  1. `J.mac removes B`.
  2. While `J.mac` is publishing the new channel (step 3 of §11.6), Stalwart restarts, and every connection is reset for 20 s.
  3. All devices sync.
- **Expect:**
  - `J.mac` finishes the rotation.
  - The new channel holds every record, and one message per record after GC.
  - Exactly one genesis for the new epoch stands. An identical genesis appended twice counts as one write.
  - A receives a grant and switches. B doesn't.
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

### FT-010 Crash between the genesis and the grants
- **Kind:** fault
- **Design:** §11.6, §11.4, §8.2
- **Binding:** any
- **Setup:** House. J is owner on `J.mac` and `J.phone`, A is an admin on `A.mac`, and B and C are writers.
- **Steps:**
  1. `J.mac removes B`.
  2. `J.mac crashes after` publishing the epoch genesis in the old channel, before step 3 of §11.6. The new key was held only in memory.
  3. During the next 25 minutes, `A.mac` and `C.phone` each add an item.
  4. `J.mac launches` 5 minutes after the crash.
- **Expect:**
  - From step 2 on, no device publishes data to the old epoch; `A.mac`'s and `C.phone`'s items are queued.
  - No sooner than 20 minutes after the genesis, one owner or admin device writes a replacing genesis whose `replaces` names the stalled one.
  - All members except B end in the replacing epoch, with the queued items published.
  - `J.mac`, on launch, doesn't keep the stalled epoch going.
  - B can't read the new epoch.
- **Invariants:** INV-CUT, INV-NO-LOSS, INV-NO-HOLD-FOREVER, INV-MONOTONE-EPOCH
- **Notes:**
  - GAP: §11.6 doesn't require the rotating device to store the new key and its plan before publishing the genesis. A crash right after it turns a stall the author could finish on relaunch into the 20-minute wait. During that wait every member has stopped publishing.
  - GAP: `replaces` appears only in prose. It isn't in the genesis JSON of step 2 or in the validity rule ("valid iff its author is owner or admin, `n` is…"). Nothing says whether the replacing genesis must build on the stalled one, or how two concurrent replacements resolve against each other and against the stalled genesis.

### FT-011 Crash partway through copying records into the new epoch
- **Kind:** fault
- **Design:** §11.6, §8.3, §12.2
- **Binding:** any
- **Setup:** House has 40 records. J is owner (`J.mac`), A and B are writers, and C is a reader (`C.phone`). The rotating device stores the new key before publishing the genesis.
- **Steps:**
  1. `J.mac removes B`.
  2. `J.mac crashes after` publishing the genesis and 25 of the 40 records in the new channel, before any grant.
  3. `J.mac launches` 2 minutes later.
  4. All devices sync.
- **Expect:**
  - All 40 records are in the new epoch on every device, with no record missing.
  - `A.mac` and `C.phone` show the same 40 records as before step 1.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE, INV-CUT
- **Notes:** GAP: the design has no rule for resuming step 3 of §11.6 after a crash or a partial failure (the same applies to a quota failure, FT-029). It also gives other devices no way to tell that a snapshot is incomplete.
  - The rotation is no longer "due", since a genesis exists. If `J.mac` goes straight to sending grants, members switch and build their new replica from the new channel.
  - The 15 missing records are named in no `_carried` list, so healing (§12.2) has nothing to republish: a device's state of the current epoch doesn't include them.
  - The late-write rule doesn't help either, because nothing new arrives in the old channel. A reader couldn't vouch for those records anyway.
  - The 15 records disappear for everyone.

### FT-012 Crash after a revocation, before the PUK manifest
- **Kind:** fault
- **Design:** §6.6, §6.3, §6.2
- **Binding:** any
- **Setup:** J has `J.mac`, `J.phone` and `J.ipad`. `J.ipad` is lost and offline. J owns House, shared with A.
- **Steps:**
  1. `J.mac revokes J.ipad`.
  2. `J.mac crashes after` writing `revoke:<J.ipad>`, before writing the manifest.
  3. `J.mac launches` and `J.phone syncs` in the same second.
- **Expect:**
  - Exactly one generation-`n+1` manifest ends up current.
  - Every home record written in either generation-`n+1` home channel is in the winner's home.
  - The losing home channel is destroyed within the same grace as the old home (30 days).
  - `J.ipad` holds no box for generation `n+1`.
  - House rotates, and `J.ipad` is fenced.
- **Invariants:** INV-CUT, INV-CONVERGE, INV-NO-LOSS
- **Notes:**
  - GAP: §6.3 makes a PUK rotation due as soon as no manifest builds on every cut, and has "any active device" perform it. Unlike collection rotation (§11.6), there is no random delay, so a user's online devices usually write competing manifests at once.
  - GAP: §6.3 says devices copy the losing home into the winner. It doesn't say who destroys the losing home channel, or when.

### FT-013 Crash after the PUK manifest, before home is copied
- **Kind:** fault
- **Design:** §6.6, §6.3, §12.2
- **Binding:** any
- **Setup:** As FT-012.
- **Steps:**
  1. `J.mac revokes J.ipad`.
  2. `J.mac crashes after` writing the generation-`n+1` manifest, before copying home.
  3. `J.phone syncs`.
  4. `J.mac` doesn't launch for a week.
- **Expect:**
  - Within one sync, `J.phone` opens its box and fills the generation-`n+1` home with every home record (memberships, settings).
  - `J.phone` publishes nothing new to generation `n`.
  - The generation-`n` home stays on the hub for 30 days from the manifest and is then destroyed by one of J's devices.
- **Invariants:** INV-NO-LOSS, INV-CUT
- **Notes:** §6.6 gives the copy to the rotating device. Here healing of the current home generation covers it, which works only because home needs no carried lists. Who starts the 30-day clock for the old home when the rotating device is gone is unstated; this is minor.

### FT-014 Crash during first run, before the recovery key is shown
- **Kind:** fault
- **Design:** §6.1, §6.5, §13.1
- **Binding:** any
- **Setup:** `J.mac` has local lists and no identity.
- **Steps:**
  1. `J.mac` connects the hub.
  2. `J.mac crashes after` step 5 of §6.1 (recovery pointer stored), before showing the recovery key.
  3. `J.mac launches`.
  4. Variant: crash after step 2 of §6.1, with the profile written and no manifest.
- **Expect:**
  - After step 3, `J.mac` shows the recovery key that matches the recovery entry in its chain, and asks for confirmation before doing anything else.
  - In either variant, the hub ends with one profile for J, one chain genesis, one current manifest and one home.
  - J's local lists are uploaded once.
- **Invariants:** INV-UX, INV-ONE-OWNER
- **Notes:** GAP: §6.1 doesn't say that `RS` must be stored durably until the user confirms it, or how a relaunch resumes a half-finished first run once the chain genesis, and with it the user ID, is already on the hub. If `RS` lives only in memory, a crash before step 6 leaves an identity whose recovery key nobody has seen. §6.5 makes that permanent, because replacing the key needs the current one. Showing and confirming the key before step 2 would avoid it, since the key is local until then.

### FT-015 Crash during pairing: device entry written, box not
- **Kind:** fault
- **Design:** §6.5, §6.3, §6.2
- **Binding:** any
- **Setup:** J has `J.mac` and `J.phone`.
- **Steps:**
  1. `J.mac pairs J.ipad`.
  2. `J.mac crashes after` writing `device:<J.ipad>`, before writing `box:<J.ipad>`.
  3. `J.mac launches`; `J.phone syncs`; `wait 1 day`.
- **Expect:**
  - `J.ipad` opens a PUK box and shows every collection J belongs to, within one sync of `J.mac` or `J.phone` coming online.
  - `J.ipad` shows a named state, not an endless spinner, until then.
- **Invariants:** INV-NO-HOLD-FOREVER, INV-UX
- **Notes:** GAP: nothing makes a missing box due. §6.3 makes a PUK rotation due only when no manifest builds on every cut. §6.5 gives the box to "the showing device", which stops listening after one approval. `J.ipad` is active in the chain but can't open home. Only an unrelated revocation, whose new manifest boxes every active device, would rescue it. Possible fix: any active device that sees an active device without a box for the current generation writes `box:X`.

### FT-016 Crash after approving a join, before the grant
- **Kind:** fault
- **Design:** §11.3, §11.4
- **Binding:** any
- **Setup:** J owns House. A has sent a join request through a link invite.
- **Steps:**
  1. `J.mac approves A`.
  2. `J.mac crashes after` the roster write `member:<A>`, before sending the grant. `J.mac` stays down.
  3. `wait 15 min`; `J.phone` is online throughout.
- **Expect:**
  - At 10 minutes, `A.phone` sends a key request, first to the epoch genesis author (`J.mac`), then to the owner's other devices.
  - `J.phone` answers with a grant.
  - `A.phone` shows House within 1 minute of the key request.
  - The join request is destroyed once A is a member.
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
- **Notes:** GAP: §9.1 says a device's HLCs strictly increase, but it doesn't require the HLC to be stored durably before a write leaves the device, or say how to recover it. If it is lost and the clock is behind, the new write's dot falls under entries other devices' contexts already cover. "Soy milk" then supersedes "Rice milk" although it never saw it, and the write is lost without any error. Recovery could take the maximum of the device's own dots found on the hub before writing. The same applies to the HLC that §17 shares with the share extension.

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

### FT-019 Offline for a month, across a rotation
- **Kind:** real-world
- **Design:** §11.6, §11.4, §8.3, §10.2, §12.3
- **Binding:** any
- **Setup:** House is at epoch 2. `A.phone` goes offline with unsynced edits to Milk and Jam. `A.mac` stays online.
- **Steps:**
  1. Day 1: `J.mac removes B`, and House rotates to epoch 3. `A.mac` consumes A's grant.
  2. Day 3: `J.mac deletes Jam`.
  3. Day 31: `J.mac` destroys the epoch-2 messages except `_epoch`.
  4. Day 32: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - `A.phone` handles `cannotCalculateChanges` if Stalwart returns it.
  - `A.phone` finds the epoch-2 `_epoch` and the epoch-3 key in A's home, and switches.
  - It forwards its Milk edit into epoch 3 with a `_carried` list it signs.
  - Jam stays deleted on every device.
  - `A.phone` appends nothing under the epoch-2 tag.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH, INV-CUT, INV-CONVERGE
- **Notes:** Whether `A.phone`'s Jam edit counts as "changing" its old replica is unclear. Its old replica never received the tombstone, because the old channel's data was destroyed on day 31. The scenario checks that Jam doesn't return; see FT-021 for how this can fail.

### FT-020 Offline past the horizon
- **Kind:** edge
- **Design:** §12.3, §13.3, §11.6
- **Binding:** any
- **Setup:** `A.phone` goes offline with unsynced edits to Milk and Bread. `A.mac` stays active.
- **Steps:**
  1. Day 10: `J.mac deletes Bread`.
  2. Day 195: `J.mac rotates House`, a maintenance rotation that compacts Bread away.
  3. Day 200: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - `A.phone` shows "This device was offline for a long time. Reloading from the hub; nothing you wrote here is lost."
  - It heals nothing and forwards nothing.
  - It reloads House from the hub.
  - It hands its Milk and Bread edits to the app as rescued data.
  - Bread doesn't reappear in House on any device.
  - Milk on the hub doesn't carry `A.phone`'s edit unless the user applies it again.
- **Invariants:** INV-NO-LOSS, INV-UX, INV-CONVERGE

### FT-021 An owner device's clock a year fast
- **Kind:** fault
- **Design:** §6.6, §11.6, §11.7, §12.3, §9.1
- **Binding:** any
- **Setup:** House is at epoch 3. J is owner (`J.mac`); A and B are writers. `A.phone` is offline with an unsynced edit to Bread's notes. Day 0: `A.mac deletes Bread`; `J.mac` merges the tombstone with a correct clock.
- **Steps:**
  1. Day 1: `J.mac clock = +1 year`.
  2. `J.mac removes B`, and House rotates to epoch 4.
  3. `J.mac syncs`.
  4. Day 3: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - Bread stays deleted on every device, including in epoch 4.
  - The epoch-3 data messages stay on the hub until day 31 of hub time.
  - The epoch-3 `_epoch` message stays until day 181 of hub time.
- **Invariants:** INV-CONVERGE, INV-GC-SAFE
- **Notes:** GAP: every destructive timer runs on one device's wall clock, which §9.1 otherwise treats as untrusted. That covers:
  - the 30-day old home (§6.6) and old channel (§11.6) cleanups;
  - the 30-day cleanup after a collection is deleted (§11.7);
  - the 180-day horizon for tombstones and `_epoch` records (§12.3);
  - staleness (§12.3).

  Here `J.mac` sees Bread's tombstone as a year old, drops it from epoch 4, and empties the epoch-3 channel at once. When `A.phone` returns, its old replica never meets the tombstone, so its edit "changes the old replica". It is forwarded into epoch 4, and Bread comes back as a fragment. Possible fix: measure these periods in hub time (JMAP `receivedAt`, IMAP `INTERNALDATE`).

### FT-022 A device clock years behind
- **Kind:** fault
- **Design:** §12.3, §9.1, §10.4, §13.3, §14
- **Binding:** any
- **Setup:** Stalwart's test certificate is valid from 2000 to 2100. `A.phone` is A's second device, and `A.mac` is active.
- **Steps:**
  1. `A.phone clock = -3 years`.
  2. `A.phone sets Milk.notes`, while `J.mac` sets Milk.notes concurrently.
  3. `A.phone offline` for 200 days of simulated time, with an unsynced edit to Bread. Meanwhile Bread is deleted and compacted by a maintenance rotation.
  4. `A.phone online`; `A.phone syncs`.
  5. Variant: with Stalwart's real certificate (valid from 2026), `A.phone` tries to sync.
- **Expect:**
  - Step 2: both notes are kept as siblings. The displayed one is `J.mac`'s, by the `(epoch, hlc, …)` order.
  - Step 4: `A.phone` treats itself as stale, shows the stale message, and doesn't forward its Bread edit. Bread stays gone.
  - Step 5: `A.phone` shows a named error about the date and time, not just the offline indicator.
- **Invariants:** INV-CONVERGE, INV-UX, INV-NO-LOSS
- **Notes:**
  - GAP: §12.3 defines staleness as "last successful full sync … older than the horizon" without saying whose clock measures it. A device whose clock has gone back since its last sync (or was fast then) computes a negative age, isn't stale, and forwards writes into an epoch where their records were compacted away. Hub time would avoid this (see FT-021).
  - GAP: §13.3 maps every connection failure to "Hub unreachable". A TLS failure caused by the device's own clock leaves it showing offline for ever, with no hint why.
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
- **Design:** §17, §1.1, §10.2, §11.6, §8.3
- **Binding:** any
- **Setup:** `J.phone` follows House. J is owner on `J.mac`; A and B are writers.
- **Steps:**
  1. `J.phone` goes to the background and is suspended for 6 hours. Background refresh is disabled.
  2. Hour 1: `A.mac` adds 10 items.
  3. Hour 2: `J.mac removes B`, and House rotates.
  4. Hour 3: on `J.phone`, the user shares a URL into House through the share extension. The app stays suspended.
  5. Hour 6: `J.phone launches`.
- **Expect:**
  - While suspended, `J.phone` holds no EventSource or IDLE session, according to Stalwart's session list after the server's idle timeout.
  - Step 4: the shared item is written to the new epoch, or queued for the app. Nothing is appended under the old epoch's tag after the genesis.
  - Step 5: `J.phone` runs `changes` and shows the 10 items and the shared URL within 10 s of launch.
  - B can't read the shared URL.
- **Invariants:** INV-CUT, INV-CONVERGE, INV-NO-LOSS
- **Notes:**
  - GAP: §17 has the extension append "one record message" using the hub credential. §11.6 requires a device to sync a channel before publishing to it, and to stop publishing to an old epoch once a genesis exists. An extension that appends without walking the change feed publishes into the superseded epoch, where B can still read it. The design should say whether the extension must sync House first or hand the write to the app.
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
- **Notes:** GAP: the JMAP binding has a liveness rule (ping every 60 s, reconnect after two missed). The IMAP binding has only "re-issue IDLE at least every 29 minutes". It sets no timeout for the server's reply to `DONE`, and has no check in between. A silent drop therefore delays push by up to 29 minutes plus TCP's retransmission timeout, while the device shows itself online. Many NATs and mobile carriers drop idle mappings after 5 minutes or less. Possible fix: renew every few minutes, with a reply timeout of seconds. `net: blackhole` is a Toxiproxy `timeout` toxic with timeout 0.

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
  - GAP: §3.3 says revoking the hub credential "removes even that" (vandalism), and §13.1 uses per-device tokens to cut off a stolen device. On many servers an IMAP session that logged in before the revocation, or an open EventSource stream, survives until it disconnects. The design should tell the hub owner to end open sessions too, or say that the cut takes effect at the next login. The scenario records what Stalwart does.
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
  - Step 4: `A.phone` keeps its user ID, device ID, memberships and replicas.
  - Step 4: it doesn't run first run or show a recovery key, and creates no collection.
  - Step 4: it publishes its queued edits.
  - Devices that haven't reconnected keep working locally.
- **Invariants:** INV-UX, INV-NO-LOSS, INV-ONE-OWNER
- **Notes:** GAP: "Reconnect" isn't a defined flow.
  - Scanning a hub QR on a device that already has a hub is, by §11.2 and §1.2, either a "different hub" (the descriptor changed; see HB-019) or a fresh connect.
  - Read literally, §13.1's connect sets up the identity and "uploads existing local data: collections that existed locally become collections on the hub with the user as owner". For `A.phone` that would duplicate every list it follows as a new collection owned by A.
  - The design needs a credential-update path that keeps the identity and the cursors.

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
  - Step 2: either the rotation waits for space before publishing its genesis, or it completes after step 3. No member is left in a stalled epoch.
  - Step 3: every queued write is published within one sync, B is cut, and each record has one message after GC.
- **Invariants:** INV-NO-LOSS, INV-CUT, INV-UX, INV-GC-SAFE
- **Notes:**
  - GAP: §13.3 has no message for a full mailbox.
  - GAP: GC publishes before it destroys (§10.7), so at quota the merged message for concurrent edits can't be appended and the dominated pairs stay.
  - GAP: a rotation that runs out of space after its genesis leaves the snapshot incomplete (FT-011), while every member has stopped publishing to the old epoch. A device could check free space before rotating (JMAP Quota, IMAP `GETQUOTAROOT`).

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
- **Notes:** GAP (see HB-037): a device can prove that a version-1 envelope under a collection tag is junk (AEAD fails under that tag's key), but §10.7 doesn't let it destroy it. Only the hub owner can clean up, using a mail client. They can't tell junk under unfamiliar tags from other users' channels, so in practice nobody can recover the space safely.

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
- **Design:** §10.7
- **Binding:** any
- **Setup:** Milk has two messages, `m1` and `m2`, that neither dominates. `J.mac` alone is online.
- **Steps:**
  1. `J.mac` publishes the merge `n`. `net: drop after <n> bytes` cuts the upload, and Stalwart doesn't store `n`.
  2. `J.mac` continues; the drop is lifted after 30 s; `J.mac syncs`.
- **Expect:**
  - `J.mac` doesn't destroy `m1` or `m2` before `n` has shown up in its change feed or its append has returned an id.
  - At every moment, the hub holds messages that together dominate Milk (harness polls).
  - After step 2, only `n` (re-sent) remains.
- **Invariants:** INV-GC-SAFE, INV-NO-LOSS
