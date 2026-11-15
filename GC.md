# Proposal: garbage collection beyond what the specification allows today

Status: **recommendations agreed** by Jesper, 2026-11-15. Section 2's rule is proved (`proofs/TandemProofs/Drop.lean`), on one condition about §11.6 (section 2.3). Every open question is answered (section 8). Nothing here is implemented yet. References with § are to
`DESIGN.md`. Decisions left for Jesper are marked **Open**.

Step 1 of the clean-up work, on branch `gc`, implements what `DESIGN.md` already allows and the
engines did not do: purging a deleted list after its retention, its file chunks included (§11.9,
§17.7); dropping the replaced writes of an epoch once the next epoch's inventory is fixed (§9.3);
and dropping downloaded files that no record names any more. This document is step 2: the parts
the specification leaves open on purpose, Q22, Q10 and Q16, and what the hub still keeps for good
after step 1.

| Question | Recommendation | Changes guarantees? |
|---|---|---|
| Q22, replaced writes in an epoch that never rotates | Drop at once another author's replaced write of the current epoch when its replacement is on the feed; keep a device's own until its own later write replaces it (section 2) | Narrows one durability margin; needs a proof |
| Q10, tombstones | Keep them, but pack many into one message; at a key change, carry a deleted record as a 20-byte mark in the inventory instead of its delete write (section 3) | No: keeps "return after any length of time"; changes the inventory format |
| Q16, the roster | No checkpoint now. After a key change, cut the old channel's roster down to what a returning device needs (section 4) | No; a checkpoint would, and needs its own design |
| Other hub state | Grants, notices and home entries of a deleted list go with its purge; the rest is bounded (section 5) | No |

## 1. What grows, in numbers

Sizes from `vectors/v2/core.json`: a kind-1 payload holding one write with a short value is 367 to
391 bytes of JSON. Sealed (54 bytes of envelope), carried as a base64 text body (§10.4) and given
its headers, it is about 0.8 KB on the hub. A device stores about the same per write.

| What | Size | How it grows |
|---|---|---|
| A replaced write, on a device | about 0.4 KB | Every edit, for as long as the epoch lasts (§9.3). A list whose key never changes keeps all of them. |
| A tombstone | about 0.8 KB on the hub, 0.4 KB on each device, one hub message | Every deleted record, for good (§12.3), and uploaded again at every key change (§11.6 step 3). |
| A roster statement | about 0.11 KB plus 0.135 KB per member as JSON; about 2 KB on the hub with 5 members, 6 KB with 20 | Every change to sharing, copied into every later epoch's channel and kept in every old one (§11.6 step 6). |
| An inventory entry | 52 bytes per record (16 + 4 + 32 per write) | Every carried record, in the current epoch only. |

What it means for a to-do list, Liszto's case. Twenty items deleted a day is 7,300 tombstones a
year: 6 MB and 7,300 messages on the hub, and toward the folder's bound of about 100,000 messages
(Q3). A key change then uploads every one of them again; on Gmail, where an append is answered
alone and costs 1.5 s or more (§17.4.3), 7,300 appends take about three hours. A hundred edits a
day keep 15 MB a year of replaced writes on every device of a list that never rotates. The roster
matters only to large groups that churn: 50 members and 200 changes over 50 key changes is a roster
of about 2.4 MB, copied into 50 channels.

The message count, not the bytes, is the first thing to hurt: the folder bound and the cost of a
key change on a server that cannot batch.

## 2. Replaced writes in an epoch that never rotates (Q22)

### 2.1 Why §9.3 keeps them

A write `x` that another write `w` supersedes is no part of the record's state, and the merge is
the same with or without it (`kept_insert_superseded` in `proofs/`). §9.3 keeps `x` anyway, until
the next epoch's inventory is fixed, because a key change can make it current again. In the new
epoch only listed writes and new writes are eligible (§8.3). If the owner device that fixes the
inventory never had `w`, `w` is not listed; if its author cannot sign it again (§11.6, late writes),
because they are the member being removed, `w` is gone for good from the new epoch and `x`, which
the owner did hold and listed, is current again. Example: Carol, being removed, renamed an item
offline; Alice's device fixed the inventory before Carol's rename arrived. Bob's device had already
seen the rename. In the new epoch the item has Bob's old name again, and Bob's device needs that
write.

Keeping `x` serves three things:

1. **The returning value.** Bob's device has `x` when it moves to the new epoch, without fetching it.
2. **Its durability.** If the new channel's copy of `x` is destroyed before devices read it, the
   devices that kept it restore it (§11.6 "Moving over", §12.2).
3. **The author's own late writes.** A device signs again its own write that the inventory does not
   list and no listed write supersedes (§11.6). If Bob's device had dropped its own `x` because of
   Carol's `w`, and `w` is not listed, Bob's work would be lost although the rule says to bring it
   back.

### 2.2 The rule

A device MAY drop a write `x` from its store when all of this holds:

1. `x` was written in the scope's current epoch: its source epoch is the current one. A write that
   is eligible only through the inventory keeps §9.3's rule.
2. A valid write `w` that the device holds, not held for a missing dependency (§8.3), supersedes
   `x`, and `w`, or a write that supersedes `w`, is on the feed (§17.7: a message that holds it is in
   the feed folder).
3. If `x` is this installation's own write, `w` is too: a later write of the same installation.
   A device keeps its own writes that someone else replaced until the next inventory is fixed.

Writes this device has not merged, held writes and roster statements are never dropped by it.
Nothing changes on the hub: §10.7 already collects the messages, and it judges from the messages
themselves, so a dropped write that arrives again is merged again and dropped again
(`kept_insert_superseded`).

### 2.3 Why it is enough

- **The returning value.** Condition 1 means `x` is not in the current inventory, so a rotating
  device can list `x` only if it holds it: there is no "carried over unseen" path for a write of
  the current epoch (§8.3). The device that lists `x` therefore holds it and uploads it with the
  snapshot data before the statement is on the hub (§11.6 step 3), and a device that dropped `x`
  reads it from the new channel when it moves over, in the same sync. If the inventory does not
  list `x`, the new epoch never needed it.
- **Late writes.** Condition 3 keeps every own write that another author's write replaced. An own
  write that a later own write replaced is dropped, and the proof shows that is safe only if §11.6
  does not sign such a write again: as written, §11.6 signs both again, and they come back as a
  conflict, where a device that dropped the earlier one signs only the later
  (`listed_rule_differs`). Not signing it again is better on its own merits, since the later write,
  signed again or listed, carries the change. §11.6 says so since 2026-11-15, and both engines follow
  it (`proofs/README.md` F6).
- **The inventory and healing.** The state a device preserves at a key change (§11.6 step 1) and
  the state it heals (§12.2) are its kept writes, which the rule never touches. Entries carried over
  unseen are earlier-epoch writes, which condition 1 leaves alone.
- **Files.** Condition 2 is §17.7's feed rule: a manifest that every other device still shows stays
  on this device until its replacement is on the feed, so protection works from what the device
  stores. See section 6 for the clean-up condition that relies on held writes.

### 2.4 What it gives up

- **Durability of a returning value, for a short window.** Under §9.3 every device that saw `x`
  replaced keeps it; under this rule, of the devices that saw the replacement, only the owner
  devices that fixed the inventory do. If the new channel's copy of `x` is destroyed (by anyone with
  the hub credential, §3.1) before other devices have read it, and those owner devices are gone,
  `x` is lost. Once a device reads `x` in the new epoch it holds it as kept, which closes the window.
- **An owner who leaves a removed vandal's writes out of the inventory.** §11.6 step 1 says the
  device "chooses the data state to preserve". If the specification means the owner may leave out
  the writes of a member being removed, to restore what that member overwrote, the owner's devices
  need the overwritten writes, and this rule would have dropped them. The engines list every kept
  write today. **Decided** (G1, Jesper, 2026-11-15): no. A key change carries every kept write, and
  §11.6 step 1 now says so. Undoing a member's edits is an explicit act of the app that writes the
  old values again as ordinary writes, which everyone sees and which merge like any other. So the
  owner's devices use this rule like every other device.

**Proved** in `proofs/TandemProofs/Drop.lean` (2026-11-15). After any history of receiving
writes, dropping under section 2.2 and §9.3, and key changes, a device holds the state of a device
that drops nothing (`same_state`), and decides as it which own writes to sign again (`resign_same`),
given §11.6 amended as in section 2.3. Condition 3 is needed (`own_condition_needed`). What the proof
takes from the protocol: an inventory lists only eligible writes; the listed writes of the replaced
epoch are in the new channel when a device moves over; a write waits until its epoch counts, which
both engines do since 2026-11-15 (a write carried into a new channel used to be refused when it
arrived before the channel's statement). Not modelled: condition 2's "on the feed", which serves
files only (§17.7), and the durability window above.

### 2.5 The alternative: rotate to compact

The owner could change the key from time to time, so §9.3's rule applies as it stands. Against it:

- **It is expensive.** A key change uploads the whole list again (§11.6 step 3). Q26 has not
  measured that over IMAP; at the rates of §17.4.3 a list of 500 records takes about two minutes on
  Fastmail with `MULTIAPPEND` and more than ten on Gmail.
- **Only an owner device can do it, and only online** (§11.6). A list whose owner rarely opens the
  app never compacts, and every member's devices wait on it.
- **It changes a rule.** A roster changes only when a user asks (§8.2); a compaction would be the
  first automatic change, and each one adds a statement to the roster (section 4) and a visible
  burst to what the hub learns (§14).
- **It blocks.** While its note is in home, no other device of the owner can change sharing
  (§11.7).

Recommended against as a means of collection. A key change stays a security act.

### 2.6 What would change in DESIGN.md

§9.3: the paragraph on discarding gains the rule of 2.2 and the window of 2.4; Q22 is answered and
leaves §19. If the answer to the open point is that the owner may roll back a vandal, §11.6 step 1
says so and that the owner's devices keep replaced writes.

## 3. Tombstones (Q10)

### 3.1 What a tombstone does in each epoch

§12.3 keeps a deleted record's tombstone for good so that "a deleted record can never be brought
back by replaying an old message, in this epoch or a later one". The two halves rest on different
things:

- **In the epoch of the delete**, the tombstone is the only defence. Any write of the record from
  this epoch is eligible, and only a delete that supersedes it, or the fact that any delete marks
  the record deleted (§9.2), keeps a replayed or late write from bringing the record back.
- **In a later epoch**, eligibility already blocks replays: a write of an earlier epoch counts only
  if the inventory lists it (§8.3), and the owner lists only the tombstone and the writes kept under
  it. What the tombstone still blocks there is a late write signed again (§11.6): a device that
  edited the record offline, without seeing the delete, signs its edit again into the new epoch.
  The tombstone, listed, still marks the record deleted.

So after a key change a tombstone needs to be only a mark that the record is deleted, not a write.

### 3.2 What could establish "no device still holds the record"

| Option | What it needs | Verdict |
|---|---|---|
| **Expiry after a horizon.** Drop a tombstone a device first saw more than, say, a year ago; a device away longer than the horizon throws away its replica of the list and reads it again. | Each device's own clock and its last full sync. | Loses §12.3's guarantee against replays: anyone with the hub credential who kept an old message can append it after the horizon, and in the delete's epoch it is valid again. Clock skew needs a margin. Not recommended. |
| **Acknowledgements.** Drop when every current member device has said it has seen the delete. | A new message per device and delete, or per sync; a list of devices that must answer, which removed and lost devices never do. | A device that never comes back blocks it for good. Not recommended. |
| **A mark in the inventory.** At a key change, a deleted record is listed with no writes, meaning "deleted"; its delete write is not carried, and no write of the record is valid in that epoch. | A change to the inventory format (§8.3 makes an entry with no writes invalid today). | Keeps the guarantee, with 20 bytes per deleted record instead of a message. Recommended. |
| **Packing.** One kind-1 message holds the tombstones of many records. | The message format and §10.7's dominance across records. | Keeps the guarantee and cuts the message count by about three orders of magnitude; the bytes stay. Recommended. |

### 3.3 Recommendation: pack, and mark at a key change

**Packing.** A kind-1 payload MAY hold the states of several records (`"recs": [ { "rec", "writes" },
... ]`). §10.7 already compares messages by the writes they hold. Dominance stated over write IDs
("every write in `m` is in `n` or is superseded by a write in `n`") does not need the two messages
to be of one record, so a pack of a thousand tombstones dominates each single tombstone message and
the hub converges to a few packs. Devices publish a pack, after the usual random delay (§10.7),
when an epoch holds more than some number of tombstone messages (64, say). A pack is limited by the
message size like any message: about 1,900 tombstones in 1 MiB once sealed and carried as base64. **Already proved** (G3,
checked 2026-11-15): `gc_safe` and `gc_no_write_lost` in `proofs/TandemProofs/GC.lean` take a
message to be any set of writes and never look at records, so they hold for packs as they stand.
What remains is engine work: a pack's contents are noted per record. **Only tombstones are packed**
(Jesper, 2026-11-15). A tombstone never changes, so a pack of them stays dominant; a pack of live
records is never dominated by one record's new message, since that covers none of the other
records, so it would stay on the hub until someone packed everything again.

**Marks.** At a key change, the owner's device lists a deleted record as an entry with a write count
of zero: the record is deleted in that epoch. It carries neither the tombstone nor the writes kept
under it. In an epoch whose inventory marks a record deleted, every write of that record is
invalid, which depends only on the signed inventory and never changes (§8.3). The rotating device
carries every mark forward. A device that holds the record's writes drops them once the new
inventory is fixed. A device returning after any length of time finds the mark in the inventory of
every later epoch, so it neither shows the record nor signs its edits again. Twenty bytes per
deleted record, in inventory fragments: 7,300 deletes a year is 146 KB in a handful of messages,
against 7,300 messages.

What is given up: a write made concurrently with a delete is kept under the tombstone today, and
the app never shows it (the record is deleted). After a mark it is gone. A blob manifest kept under
a tombstone is not live (§17.7), so nothing about files changes.

**What stays.** In a list whose key never changes, tombstones stay writes, packed. That is the
case packing exists for.

### 3.4 What would change in DESIGN.md

§8.3: entries with a count of zero become deletion marks, with their rule of validity, and a key
change carries marks forward. §10.5: the `recs` form of a kind-1 payload. §10.7: dominance across
records. §12.3: tombstones become marks at the next key change. The inventory vectors and the
proof `inventory_bytes_inj` change with the format; Q10 leaves §19.

## 4. The roster (Q16)

### 4.1 The cost

Every statement is the complete roster state (§9.4), and a device keeps every statement the owner
signed. The current channel holds the whole chain from genesis; every old channel keeps its own
copy for good (§11.6 step 6). With 5 members and a few changes a year it is tens of KB in all. With
50 members and 200 changes over 50 key changes it is 2.4 MB a copy and some 60 MB over all the
channels, and a new device downloads and checks 200 statements. Tandem is designed for tens of
users per hub (§1.2), so this is the edge, not the common case.

### 4.2 A checkpoint, and the conflict test

A checkpoint would be a statement signed by the owner that commits to the chain up to some
statement, so that a new device need not check, and the hub need not keep, what comes before it.
The difficulty is §9.4: two owner-signed successors of **any** statement kill the collection, and
the test needs both statements' full bytes. A device that trusts a checkpoint instead of the chain
below it can no longer tell whether a successor it receives of an old statement is a fork. Either
it ignores such forks, and the checkpoint has chosen one branch, which §8.2 rules out ("no clock,
chain length or ranking selects a winning history"); or it must still keep and check everything.
Ignoring forks below a checkpoint is in fact the owner-control design §9.4 says is needed against a
revoked owner device forking old history, and it needs that design's care: what makes a checkpoint
itself unforkable, and what happens to members who followed the other branch. **Parked** (G5,
Jesper, 2026-11-15): no checkpoint, unless that owner-control design is ever taken up. Section 4.3's
cut covers the cost that matters.

### 4.3 Recommendation: cut old channels' copies down

A channel that a later epoch replaced keeps its `_roster` so that "a device that was away finds out
where the collection went" (§11.6 step 6). Such a device already holds the chain up to its last
epoch; what it needs from the old channel is the statement that announced the next epoch, which it
checks against the chain it holds, and the two statements that prove the collection dead if there
are any (§9.4). A device that holds nothing gets the whole chain in the current channel with its
grant (§11.4).

So, after step 6 of a key change, the owner's device MAY replace the old channel's `_roster`
messages with one that holds the announcing statement and its parent. A device that finds a
collection dead still puts the proof into the current channel (§9.4), as now. That makes the
roster's hub storage linear (one full copy and two statements per old epoch) instead of growing
with the square of the number of key changes. It does not change what a device keeps or checks, so
the conflict test is untouched. This needs a sentence in §10.7, where "the roster is never pruned"
becomes "never pruned in a current channel", and the healing rule of §12.2, which restores the
roster only in current channels already.

## 5. What else the hub keeps

| What | Today | Proposal |
|---|---|---|
| Announcements in old channels | One statement per key change, for good | Keep: it is how an absent device learns where to go. |
| Epoch keys in home | 32 bytes and a tag per epoch, for good | Keep: needed to read old channels. |
| Grants | The last per current member and collection; earlier ones and those to removed members are destroyed (§11.4) | Bounded. For a deleted list §11.9 says nothing; its grants should go with its purge, and its home entry should keep only what is needed to read the deleting statement. |
| Leave notices, key-request notices (kind 3) | Not collected by any rule | A notice that is answered (the member removed, the key granted) can be destroyed by the owner's device that acted on it. Small, but unbounded on a busy list. |
| Inventory fragments of an old epoch | Kept until the epoch is replaced; then data | Destroy with the old epoch's data in step 6; §11.6 does not say which. |

## 6. Files

- **Clean-up and dropped writes.** §17.7's second condition lets a device destroy a chunk only when
  it "holds a valid write ... whose manifest lists its address". Once devices drop replaced writes,
  by §9.3 after a key change or by section 2's rule, the manifests of replaced versions are gone and
  their chunks fall to the orphan rule instead, which waits for the orphan grace and for a resolved
  inventory. That is safe, only slower. Better: when a device drops a write whose value is a
  manifest, it keeps the manifest's addresses, as "listed once" for that scope, until clean-up has
  destroyed them or found them protected. The engines already do this for dropped uploads
  (`blobs.dropped.*`). **Done in step 1:** a write dropped under §9.3 whose value is a manifest is
  kept the same way, on a device that uses files.
- **B5, copying a dead collection,** uploads every blob again. A copy that listed the old chunks
  would need clean-up to know about the copy's scope, and the dead collection is never collected.
  Unchanged: a copy uploads.
- **B6, earlier versions.** Keeping the chunks of replaced manifests for a while would need the
  manifests, which section 2 and step 1 drop. If version history is wanted, the rule is that a
  manifest write is dropped only after the history period, and its chunks are protected until
  then. Not proposed.
- **A deleted list's chunks** go with its purge in step 1, as §17.7 says.

## 7. What looks wrong or unclear in DESIGN.md today

1. **§11.9 is silent on much of the purge**: from what moment the 30 days run (the deleting
   statement's arrival on the device, by the device's own clock, is the only safe choice), whether
   inventory fragments, notices and grants count as data, what happens to members' home entries,
   and what non-owner devices do with their replicas when the 30 days are over. It also says devices
   "stop showing the collection" and "keep local data read-only", which an app can only do both of
   by showing the list somewhere as deleted. **What step 1 does:** the period runs on each device's
   own clock from when it first saw the list gone (deleted, or this participant removed); it is a
   setting (`SetRetention`, `setRetention`), 30 days by default. Then every device drops its
   replica but for the roster, and a device of a deleted list's owner destroys every message it
   merged in the list's channels that carries no roster write, inventory fragments included, and
   every chunk of the list. Notices, grants and home entries stay (section 5). The engines report
   collected lists (`GoneLists`, `goneLists`).
2. **§12.3 overstates what the tombstone does in a later epoch** (section 3.1): replays from earlier
   epochs are already stopped by eligibility. Harmless, but it hides that a mark would do.
3. **§11.6 step 1, "choose the data state to preserve"**, read as if the owner may leave writes
   out. **Fixed** (G1): step 1 now says the inventory carries every kept write.
4. **§11.7, finishing another device's change,** copies a missing listed write "from the old
   channel". That message may already have been destroyed by §10.7, if a late write that the
   inventory does not list dominated it. Devices that kept the write under §9.3 restore it when they
   move over, so nothing is lost, but the text should say that the old channel is not the only
   source.
5. **§17.7, orphans:** "no write the device holds has ever listed it" mixes present and past.
   With dropped writes, "holds or has held" is what is meant (section 6).

## 8. Open questions

| # | Question |
|---|---|
| G1 | **No** (Jesper, 2026-11-15): a key change carries every kept write (§11.6 step 1); undoing a member's edits is an explicit app action that writes the old values again. |
| G2 | **Done** (2026-11-15): `proofs/TandemProofs/Drop.lean`, given §11.6 amended (section 2.3, F6 in `proofs/README.md`). |
| G3 | **Yes, already** (2026-11-15): `gc_safe` is record-agnostic. Only tombstones are packed (Jesper). |
| G4 | **Yes** (Jesper, 2026-11-15): the inventory format may change before v2 ships. Packing first, then the marks. |
| G5 | **Parked** (Jesper, 2026-11-15): no checkpoint; section 4.3's cut instead. |
| G6 | **The owner's devices pack** (Jesper, 2026-11-15). The threshold is open: about 50 tombstone messages in a channel is the proposal. |
