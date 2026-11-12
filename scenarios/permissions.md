# Roles, revocations and rotations: the world after each change

Every roster, device or key operation must change exactly what it targets and nothing else. Each scenario checks the whole world with `check world` before and after: every user's role and status, every device's epoch and keys, what each can decrypt, and what each of their actions does.

## The capability matrix

What an honest client lets each party do, and what every other device accepts from them (DESIGN §8.1–§8.3, §11). "Ignored" means an honest client wouldn't offer the action, and a modified client's attempt is held or dropped by everyone else (INV-AUTHZ).

| Action | Owner | Admin | Writer | Reader | Removed / left | Revoked device | Hub participant (not a member) |
|---|---|---|---|---|---|---|---|
| Read records and the member list | yes | yes | yes | yes | only epochs it had keys for | only epochs before its fence | no |
| Add, edit, reorder, complete, delete records | yes | yes | yes | ignored | ignored | ignored after its fence | can't (no key) |
| Rename the list, change its icon (`_collection`) | yes | yes | ignored | ignored | ignored | ignored | can't |
| Delete the collection | yes | ignored | ignored | ignored | ignored | ignored | destroys messages only (availability, §1.2) |
| Create, edit or close an invite (writer or reader role) | yes | yes | ignored | ignored | ignored | ignored | can't |
| Approve or decline a join request | yes | yes | ignored | ignored | ignored | ignored | can't |
| Change a writer or reader (role, remove) | yes | yes | ignored | ignored | ignored | ignored | can't |
| Make someone an admin, demote or remove an admin | yes | ignored | ignored | ignored | ignored | ignored | can't |
| Hand ownership to a member | yes | ignored | ignored | ignored | ignored | ignored | can't |
| Leave | after handing over | yes | yes | yes | n/a | n/a | n/a |
| Start a rotation | yes | yes | ignored | ignored | ignored | ignored | can't |
| Fence a member's revoked device | yes | yes | own devices only | own devices only | ignored | ignored | can't |

## World checks

`check world` records, for every user and device:

- **Roster:** role and status in the converged roster; who the owner is.
- **Keys:** the epoch the device is in; whether it holds the current epoch key; which epochs it can decrypt; whether it is in the current genesis `to` list.
- **Capabilities:** for every row of the matrix, the result of attempting the action (honest client: offered or not; modified client: accepted or ignored by every other device).
- **Home:** the device's `membership` record (state and epoch).

A scenario's **Expect** names only the changes the operation should make (the **delta**). Everything not in the delta must be identical before and after (INV-BYSTANDER).

The standard setup for this file, unless a scenario says otherwise:

- **List "House":** owner J; A admin; B writer; C reader; D writer.
- **Devices:** `J.phone`, `J.mac`, `A.phone`, `B.phone`, `C.phone` and `D.phone`, all online, epoch 0, every member holding the key.

---

## Baseline

### PM-001 The matrix holds at rest
- **Kind:** real-world
- **Design:** §8.1–§8.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. Every member attempts every action of the matrix from an honest client, then from a modified client that signs it anyway.
- **Expect:**
  - Honest clients offer exactly the actions the matrix allows.
  - Every "ignored" attempt from a modified client changes nothing on any other device; the world is identical before and after.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### PM-002 Adding a member changes only the roster
- **Kind:** real-world
- **Design:** §11.1–§11.4
- **Binding:** any
- **Setup:** standard; P6 has an identity on the hub.
- **Steps:**
  1. `A.phone invites link to House as reader`; P6 joins; `A.phone approves` P6.
  2. `check world`.
- **Expect:**
  - **Delta:** P6 is an active reader holding the epoch-0 key.
  - The epoch is still 0; no rotation.
  - Nobody else's role, keys or capabilities changed.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

## Promotions and demotions

### PM-003 Writer promoted to admin
- **Kind:** real-world
- **Design:** §8.1, §8.2, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone sets role B = admin`; everyone syncs; `check world`.
- **Expect:**
  - **Delta:** B is admin; B gains the admin rows of the matrix.
  - No rotation: a promotion is not a cut (§11.6 lists only reductions).
  - B can now remove C and D, but still can't act on A (an equal) or J.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

### PM-004 Admin demoted to writer
- **Kind:** real-world
- **Design:** §8.2 (cuts), §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone sets role A = writer`; everyone syncs; `check world`.
- **Expect:**
  - **Delta:** A is a writer; epoch 1; every active member, including A, holds the epoch-1 key.
  - A loses the admin rows; admin actions A attempts afterwards are ignored by every device.
  - Records, and everyone else's roles and capabilities, are unchanged.
  - Invites A created while admin: see sharing.md SH-029 (unsettled).
- **Invariants:** INV-BYSTANDER, INV-AUTHZ, INV-MONOTONE-EPOCH

### PM-005 Writer demoted to reader
- **Kind:** real-world
- **Design:** §8.3, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone sets role B = reader`; everyone syncs.
  2. `B.phone` (modified client) adds an item in epoch 1.
  3. `check world`.
- **Expect:**
  - **Delta:** B is a reader; epoch 1; B holds its key.
  - Step 2's item appears on no other device.
  - B still reads everything, including what others write in epoch 1.
  - B's writes from epoch 0 remain.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

### PM-006 Reader made a writer
- **Kind:** real-world
- **Design:** §8.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone sets role C = writer`; everyone syncs.
  2. `C.phone adds` an item.
- **Expect:**
  - **Delta:** C is a writer, and C's item appears everywhere.
  - No rotation: raising a role isn't a cut.
  - C's write is valid in epoch 0 because the admission builds on that epoch's genesis (§8.3, "write-enabled").
- **Invariants:** INV-BYSTANDER

## Removals and departures

### PM-007 Admin removes a writer
- **Kind:** real-world
- **Design:** §8.2, §11.6, §3.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone removes D`; everyone syncs; `check world`.
  2. J, A, B keep editing; `D.phone` (modified client) keeps trying to read and write.
- **Expect:**
  - **Delta:** D is removed and has no epoch-1 key.
  - `D.phone` shows "House is no longer shared with you"; unsent edits are handed to the app.
  - Nothing published in epoch 1 decrypts on `D.phone` (INV-CUT).
  - D's earlier items remain, attributed to D.
  - J, A, B, C keep their roles, read and write as before, and all hold epoch 1.
- **Invariants:** INV-BYSTANDER, INV-CUT, INV-AUTHZ

### PM-008 Owner removes an admin
- **Kind:** real-world
- **Design:** §8.1, §8.2
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone removes A`; everyone syncs; `check world`.
- **Expect:**
  - **Delta:** A is removed; epoch 1; A has no key for it.
  - Invites A created: SH-029.
  - B, C, D and J unchanged otherwise.
- **Invariants:** INV-BYSTANDER, INV-CUT

### PM-009 Admin tries to remove the owner or an equal
- **Kind:** adversarial
- **Design:** §8.1 (no acting on equals or higher ranks)
- **Binding:** any
- **Setup:** standard, plus B promoted to admin (PM-003).
- **Steps:**
  1. `A.phone` (modified client) signs `removes J`, then `removes B`, then `sets role B = writer`.
  2. Everyone syncs; `check world`.
- **Expect:**
  - The world is unchanged: every write is ignored on every device, and no rotation is triggered by them.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### PM-010 A member leaves
- **Kind:** real-world
- **Design:** §11.5, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `B.phone leaves House`; everyone syncs; `check world`.
- **Expect:**
  - **Delta:** B is `left`; epoch 1; B has no epoch-1 key; B's home shows the membership as `left`.
  - Everyone else unchanged.
- **Invariants:** INV-BYSTANDER, INV-CUT

### PM-011 Removed, then invited back
- **Kind:** real-world
- **Design:** §11.1–§11.6
- **Binding:** any
- **Setup:** PM-007 done (epoch 1).
- **Steps:**
  1. `A.phone invites qr to House as writer`; `D.phone joins via` it.
  2. `check world`.
- **Expect:**
  - **Delta:** D is an active writer holding the epoch-1 key, and sees the whole current list, including what was written while D was out.
  - D's pre-removal items are still attributed to D.
  - No rotation.
- **Invariants:** INV-BYSTANDER
- **Notes:** see sharing.md SH-012 and SH-013 for re-joins that race a rotation.

### PM-012 Making the list private again
- **Kind:** real-world
- **Design:** §7.4, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. J removes A, B, C and D (one action in the UI, four roster writes); everyone syncs.
- **Expect:**
  - **Delta:** one rotation, not four, to epoch 1, with J as the only recipient.
  - Every former member's device shows the list as no longer shared.
- **Invariants:** INV-CUT
- **Notes:** the design doesn't say whether consecutive cuts must be batched into one rotation (§11.6 only says a rotation is "due"); the expectation of one rotation is a proposal.

## Ownership

### PM-013 Handing the list over
- **Kind:** real-world
- **Design:** §8.1, §8.2, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone hands House to A`; everyone syncs; `check world`.
- **Expect:**
  - **Delta:** A is the owner; J has the fallback role recorded in J's `member` entry; epoch 1, because handing away is a reduction of J.
  - A can now delete the list and appoint admins; J can't.
  - B, C and D are unchanged.
- **Invariants:** INV-BYSTANDER, INV-ONE-OWNER
- **Notes:** the fallback role's value at creation is unspecified (sharing.md SH-001).

### PM-014 The owner loses every device and recovers
- **Kind:** real-world
- **Design:** §6.5, §6.6, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone` and `J.mac` are lost. `J.new recovers with J.rk`.
  2. Everyone syncs; `check world`.
- **Expect:**
  - **Delta:** J is still the owner (same user ID). J's old devices are revoked and fenced. Epoch 1, with `J.new` holding the key.
  - A, B, C and D are unchanged and kept working throughout, including during the time J had no devices.
- **Invariants:** INV-BYSTANDER, INV-ONE-OWNER

## Devices

### PM-015 A member's phone is revoked
- **Kind:** real-world
- **Design:** §6.6, §11.6
- **Binding:** any
- **Setup:** standard; B also has `B.mac`.
- **Steps:**
  1. `B.mac revokes B.phone`; everyone syncs; `check world`.
- **Expect:**
  - **Delta:** `B.phone` is revoked and fenced, holds no epoch-1 key, and its later writes are ignored. Epoch 1. `B.mac` holds the new key.
  - B's role is unchanged: B is still a writer, through `B.mac`.
  - Everyone else unchanged.
  - The rotation was done by an owner or admin device (J's or A's), since B isn't an admin (§6.6 step 3).
- **Invariants:** INV-BYSTANDER, INV-CUT

### PM-016 The owner's phone is revoked
- **Kind:** real-world
- **Design:** §6.6
- **Binding:** any
- **Setup:** standard; J is also a member of four other lists.
- **Steps:**
  1. `J.mac revokes J.phone`; everyone syncs.
- **Expect:**
  - Every collection J belongs to rotates. Lists J owns or administers rotate at once from `J.mac`. The others rotate when their owner's or admins' devices see the revocation.
  - J keeps every role through `J.mac`.
  - Members of those lists keep their roles and capabilities.
- **Invariants:** INV-BYSTANDER, INV-CUT

### PM-017 Revoking a device that holds unsent edits
- **Kind:** edge
- **Design:** §6.6, §8.3
- **Binding:** any
- **Setup:** standard; B also has `B.mac`; `B.phone` offline with three unsent edits.
- **Steps:**
  1. `B.mac revokes B.phone`; rotation happens.
  2. `B.phone online`.
- **Expect:**
  - `B.phone` shows it was removed. Its three edits aren't accepted by anyone, and the app is told (identity.md ID-007 notes this is unspecified).
  - `B.mac` and everyone else are unaffected.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

## Rotations without membership changes

### PM-018 Maintenance rotation
- **Kind:** real-world
- **Design:** §11.6, §12.3
- **Binding:** any
- **Setup:** standard, with 1,200 tombstones older than the horizon.
- **Steps:**
  1. A maintenance rotation runs (started by `J.mac`); everyone syncs; `check world`.
- **Expect:**
  - **Delta:** epoch 1; every member holds its key; compacted records are gone from the hub.
  - Roles, capabilities and the rendered list are identical on every device.
- **Invariants:** INV-BYSTANDER, INV-ORACLE

### PM-019 A member offline through a rotation
- **Kind:** real-world
- **Design:** §11.4, §11.6
- **Binding:** any
- **Setup:** standard; `C.phone` offline for 3 days.
- **Steps:**
  1. During those days: D is removed (rotation to epoch 1), and B is demoted to reader (rotation to epoch 2).
  2. `C.phone online`; syncs.
- **Expect:**
  - `C.phone` moves to epoch 2 using its grants, without asking anyone. C's role (reader) and everything C can read are unchanged.
  - If a grant was destroyed on the hub, C gets the key by key request within the safety-net time (§11.4).
- **Invariants:** INV-BYSTANDER, INV-MONOTONE-EPOCH

### PM-020 An invite survives a rotation
- **Kind:** edge
- **Design:** §11.1, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone invites link to House as writer` (7-day expiry).
  2. `J.phone removes D` (rotation to epoch 1).
  3. P6 opens the link; `A.phone approves` P6.
- **Expect:**
  - P6 is admitted and gets the epoch-1 key (the grant names the current epoch). The invite needed no reissue.
- **Invariants:** INV-AUTHZ

## Concurrent changes

### PM-021 Removal and demotion of the same member at once
- **Kind:** edge
- **Design:** §8.2 (least privilege)
- **Binding:** any
- **Setup:** standard; J and A offline.
- **Steps:**
  1. `J.phone removes B`; `A.phone sets role B = reader`.
  2. Both sync.
- **Expect:**
  - B ends removed on every device (`removed` > `reader`).
  - One rotation covers it, or two that converge; the final world is the same either way.
- **Invariants:** INV-CONVERGE, INV-BYSTANDER

### PM-022 Promotion and removal of the same member at once
- **Kind:** edge
- **Design:** §8.2
- **Binding:** any
- **Setup:** standard; J and A offline.
- **Steps:**
  1. `J.phone sets role B = admin`; `A.phone removes B` (B is a writer in A's view, so A may).
  2. Both sync.
- **Expect:**
  - B is removed on every device (least privilege).
  - J's promotion still stands but is outranked.
- **Invariants:** INV-CONVERGE
- **Notes:** a product question rather than a gap: the owner's explicit promotion loses to an admin's concurrent removal. Least privilege is safe, but J may be surprised.

### PM-023 Unrelated changes at once
- **Kind:** real-world
- **Design:** §8.2, §11.6
- **Binding:** any
- **Setup:** standard; J and A offline.
- **Steps:**
  1. `J.phone sets role C = writer`; `A.phone removes D`.
  2. Both sync; `check world`.
- **Expect:**
  - **Delta:** C is a writer; D is removed; epoch 1.
  - Both changes hold on every device; nothing else changes.
- **Invariants:** INV-BYSTANDER, INV-CONVERGE

### PM-024 Rotations racing
- **Kind:** edge
- **Design:** §11.6 (competing rotations)
- **Binding:** any
- **Setup:** standard; J and A offline.
- **Steps:**
  1. `J.phone removes D`; `A.phone removes C` (a reader; A may). Each rotates to epoch 1 independently.
  2. Both sync.
- **Expect:**
  - One epoch-1 genesis wins (lowest write ID). The other's content is forwarded into it.
  - A further rotation follows if the winner's `to` list still names C or D (it names whoever its author hadn't removed).
  - Finally neither C nor D holds the current key, and J, A and B do.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-CONVERGE

### PM-025 A reader with a modified client
- **Kind:** adversarial
- **Design:** §8.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `C.phone` (modified) signs item adds, renames, a `_collection` rename, an invite, and a roster write promoting itself.
  2. Everyone syncs; `check world`.
- **Expect:**
  - The world and the rendered list are unchanged on every device.
  - The writes are held or dropped (sharing.md SH-022, records.md RC-030: when held writes are finally dropped is unsettled).
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### PM-026 The owner deletes the list; an admin tries first
- **Kind:** real-world
- **Design:** §11.7
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone` (modified) writes `_collection.deleted`.
  2. `J.phone` deletes House.
- **Expect:**
  - After step 1: nothing changes anywhere.
  - After step 2: House disappears from every member's devices, and every home shows it as `removed`.
- **Invariants:** INV-AUTHZ

## Families

### PM-F01 Family: permission churn with 2–10 players
- **Kind:** edge
- **Design:** §8, §11, §6.6
- **Parameters:**
  - players 2–10, with random initial roles (one owner);
  - 20–100 roster operations, drawn from: add by QR or link, approve, decline, promote, demote, remove, leave, hand over, revoke device, recover, maintenance rotation;
  - actors chosen at random, **including actors not entitled** (modified clients);
  - interleaved with `M-LIST` record edits;
  - schedules `S-LIVE`, `S-SLEEP`, `S-OUTAGE`;
  - 200 seeds.
- **Oracle:** a roster model that applies §8.2's rules (standing, cuts, least privilege, the pin) to every log write made, and the reference model for records.
- **Expect:**
  - After every operation, once all devices sync, `check world` equals the oracle's world.
  - The difference between consecutive worlds is exactly the operation's delta (INV-BYSTANDER).
  - Removed members and revoked devices never decrypt a later epoch (INV-CUT).
  - Epochs never go backwards (INV-MONOTONE-EPOCH); exactly one owner (INV-ONE-OWNER).

### PM-F02 Family: rotations under load
- **Kind:** fault
- **Design:** §11.6, §8.3
- **Parameters:** players 3–10; continuous `M-LIST` edits by every writer while 1–5 rotations happen (removals, revocations, maintenance); schedules `S-FLAP`, `S-SLEEP`; 200 seeds
- **Oracle:** the reference model plus the roster model
- **Expect:**
  - No honest member's write is lost across a rotation (INV-NO-LOSS): carried by the snapshot, or forwarded within 30 days.
  - Nothing a removed member or revoked device wrote after its cut appears anywhere.
- **Invariants:** INV-NO-LOSS, INV-CUT, INV-CONVERGE
