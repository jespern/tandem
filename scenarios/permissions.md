# Roles, removals and key changes: the world after each change

Every roster or key operation must change exactly what it targets and nothing else. Each scenario checks the whole world with `check world` before and after: every user's role and status, every device's epoch and keys, what each can decrypt, and what each of their actions does.

## The capability matrix

What an honest client lets each party do, and what every other device accepts from them (DESIGN §8.1–§8.3, §11). "Ignored" means an honest client wouldn't offer the action, and a modified client's attempt is rejected by everyone else (INV-AUTHZ).

| Action | Owner | Writer | Reader | Removed / left | Holder of an open invite (not a member) | Hub participant (no key) |
|---|---|---|---|---|---|---|
| Read records and the member list | yes | yes | yes | only epochs it had keys for | no | no |
| Add, edit, reorder, complete, delete records | yes | yes | ignored | ignored (late writes can't be signed again) | can't (no key) | can't |
| Rename the list, change its icon (`_meta`) | yes | ignored | ignored | ignored | can't | can't |
| Delete a named record (`_meta`, `_roster`) | ignored | ignored | ignored | ignored | can't | can't |
| Create, edit or close an invite | yes (home) | no (no access to the owner's home) | no | no | no | no |
| Read join requests on an invite | yes | only if they hold that invite | only if they hold it | only if they still hold it | yes | no |
| Approve or decline a join request | yes | ignored | ignored | ignored | ignored | can't |
| Change a role, remove a member | yes | ignored | ignored | ignored | ignored | can't |
| Ask for a new key (rotation) | yes | ignored | ignored | ignored | ignored | can't |
| Delete the collection | yes | ignored | ignored | ignored | ignored | destroys messages only (availability, §1.2) |
| Leave | no (delete instead) | notice; owner confirms | notice; owner confirms | n/a | n/a | n/a |
| Send a key request | n/a | yes | yes | yes (answered with statements, no key) | no | no |
| Answer a key request | yes | ignored | ignored | ignored | ignored | can't |
| Copy the collection into a new one | yes | yes | yes | from its read-only replica | no | no |

A **dead** collection (§9.4) collapses the matrix: every member can read their replica and copy it; nothing else is offered or accepted. A collection whose owner's secret is lost keeps every content row and loses every sharing row (§11.8).

## World checks

`check world` records, for every user and device:

- **Roster:** role and status in the roster head; who the owner is; whether the collection is dead.
- **Keys:** the epoch-start ID the device follows; whether it holds the current epoch key; which epochs it can decrypt; whether it holds a `flight` note's key (owner devices only).
- **Capabilities:** for every row of the matrix, the result of attempting the action (honest client: offered or not; modified client: accepted or ignored by every other device).
- **Home:** the device's `membership` record (state, `keys`), and for owners the open invites and any `flight` note.

A scenario's **Expect** names only the changes the operation should make (the **delta**). Everything not in the delta must be identical before and after (INV-BYSTANDER).

The standard setup for this file, unless a scenario says otherwise:

- **List "House":** owner J; A writer; B writer; C reader; D writer.
- **Devices:** `J.phone`, `J.mac`, `A.phone`, `B.phone`, `C.phone` and `D.phone`, all online, in epoch `E0`, every member holding its key.
- **Other lists:** "Groceries" (J owner, A and B writers) and "Trip" (A owner, J and D writers), so bystander checks cover other collections too.

---

## Baseline

### PM-001 The matrix holds at rest
- **Kind:** real-world
- **Design:** §8.1–§8.3, §11
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. Every member attempts every action of the matrix from an honest client, then from a modified client that signs it anyway.
- **Expect:**
  - Honest clients offer exactly the actions the matrix allows.
  - Every "ignored" attempt from a modified client changes nothing on any other device; the world is identical before and after.
  - No attempt by a non-owner counts toward a dead collection (only owner-signed statements do, §9.4).
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### PM-002 Adding a member changes only the roster
- **Kind:** real-world
- **Design:** §11.1–§11.4
- **Binding:** any
- **Setup:** standard; P6 has an identity on the hub.
- **Steps:**
  1. `J.phone invites link to House as reader`; P6 joins; `J.phone approves` P6.
  2. `check world`.
- **Expect:**
  - **Delta:** P6 is an active reader holding the `E0` key; J's home holds the invite with one use counted on the roster.
  - The epoch is still `E0`; no rotation.
  - Nobody else's role, keys or capabilities changed, here or in Groceries and Trip.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

## Roles

### PM-005 Writer made a reader
- **Kind:** real-world
- **Design:** §8.2, §8.3, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone` makes B a reader; everyone syncs.
  2. `B.phone` (modified client) adds an item naming the new statement.
  3. `check world`.
- **Expect:**
  - **Delta:** B is a reader; epoch `E1`; every remaining member, B included, holds `K(E1)`.
  - Step 2's item appears on no other device.
  - B still reads everything, including what others write in `E1`.
  - B's `E0` writes are in `inv(E1)` and remain.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

### PM-006 Reader made a writer
- **Kind:** real-world
- **Design:** §8.2, §8.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone` makes C a writer; everyone syncs.
  2. `C.phone adds` an item.
- **Expect:**
  - **Delta:** C is a writer, and C's item appears everywhere.
  - No rotation: raising a role is a same-epoch statement, which keeps the epoch and snapshot commitments.
  - C's write names the promoting statement, which lists C as writer.
- **Invariants:** INV-BYSTANDER

## Removals and departures

### PM-007 The owner removes a writer
- **Kind:** real-world
- **Design:** §8.2, §11.6, §3.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone removes D`; everyone syncs; `check world`.
  2. J, A, B keep editing; `D.phone` (modified client) keeps trying to read and write.
- **Expect:**
  - **Delta:** D is removed and has no `K(E1)`; everyone else holds `K(E1)`.
  - D reads nothing written after the announcement, and nothing D writes afterwards is accepted.
  - D's role in Trip (A's list) is unchanged.
- **Invariants:** INV-BYSTANDER, INV-CUT, INV-AUTHZ

### PM-009 A writer tries to change the roster
- **Kind:** adversarial
- **Design:** §8.2, §9.4
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone` (modified client) signs `_roster` successors of the head that remove J, remove B, and make A the owner.
- **Expect:**
  - All rejected (not signed with the owner's key); the world is unchanged; House isn't dead.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER, INV-ONE-OWNER

### PM-010 A member leaves
- **Kind:** real-world
- **Design:** §11.5, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `B.phone leaves House`; everyone syncs; `check world`.
  2. J confirms; everyone syncs; `check world`.
- **Expect:**
  - **Delta after 1:** B's home membership is `left` and B's devices stop following. The roster still lists B.
  - **Delta after 2:** B removed; epoch `E1`.
- **Invariants:** INV-BYSTANDER, INV-CUT

### PM-011 Removed, then invited back
- **Kind:** real-world
- **Design:** §11.3, §8.2
- **Binding:** any
- **Setup:** PM-007 done.
- **Steps:**
  1. J creates a new invite; D joins; J is asked "Dana was removed from House. Add her again?" and says yes.
- **Expect:**
  - **Delta:** D is a writer again in `E1`, with the same `uid`, `uka` and `name` and `via` = the new invite. D holds `K(E1)`, not `K(E0)` again (D still has it from before).
  - The invite D first came through stays closed.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

### PM-012 Making the list private again
- **Kind:** real-world
- **Design:** §7.4, §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. J removes A, B, C and D in one change.
- **Expect:**
  - **Delta:** one statement, one new epoch, only J listed and only J's devices hold its key.
  - Groceries and Trip are unchanged for everyone.
- **Invariants:** INV-BYSTANDER, INV-CUT

## Owners and identities

### PM-014 The owner loses every device and restores
- **Kind:** real-world
- **Design:** §6.3, §8.1
- **Binding:** any
- **Setup:** standard. J's devices are all lost; J has the backup key.
- **Steps:**
  1. J restores on `J.new`; then removes D from House.
- **Expect:**
  - **Delta after restore:** none on the hub. J's new device follows House and holds `K(E0)` from home.
  - The removal works from `J.new` (the owner is a key, not a device).
- **Invariants:** INV-BYSTANDER, INV-ONE-OWNER

### PM-015 A member starts over
- **Kind:** real-world
- **Design:** §6.4, §11.3
- **Binding:** any
- **Setup:** standard. B's phone was stolen; B starts over as B2.
- **Steps:**
  1. J removes B and invites B2; B2 joins.
- **Expect:**
  - **Delta:** B removed, B2 a writer, epoch `E1`. B's old items keep B as author.
  - The thief, holding B's old secret, holds no `K(E1)`.
  - In Groceries, B (the old identity) is still a writer until J removes him there too: nothing links the identities.
- **Invariants:** INV-BYSTANDER, INV-CUT

### PM-016 The owner starts over
- **Kind:** real-world
- **Design:** §6.4, §11.8
- **Binding:** any
- **Setup:** standard. J's phone was stolen; J starts over as J2.
- **Steps:**
  1. J2 copies House into "House 2" and invites A, B, C and D with the same roles.
  2. A leaves the old House; B doesn't.
- **Expect:**
  - "House 2": J2 owner, others as invited, all items J2's writes.
  - Old House: unchanged roster; the thief can act as its owner; B still receives what the thief publishes there.
  - In Trip (A's), J is still a writer until A removes him.
- **Invariants:** INV-ONE-OWNER

## Key changes without membership changes

### PM-018 The owner asks for a new key
- **Kind:** real-world
- **Design:** §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. J asks for a new key on House.
- **Expect:**
  - **Delta:** epoch `E1`; every member holds `K(E1)`; roles unchanged.
- **Invariants:** INV-BYSTANDER

### PM-019 A member offline through a key change
- **Kind:** real-world
- **Design:** §11.4, §11.6
- **Binding:** any
- **Setup:** standard; `C.phone offline`.
- **Steps:**
  1. J removes D; `wait 3 days`; `C.phone online`; `check world`.
- **Expect:**
  - **Delta:** as PM-007, and C ends holding `K(E1)` via its grant.
  - Until C syncs, C's world shows `E0`; nothing else about C changes.
- **Invariants:** INV-BYSTANDER

### PM-020 An open invite across key changes
- **Kind:** edge
- **Design:** §11.1, §11.3
- **Binding:** any
- **Setup:** standard; J has open link invites `I` (which admitted D) and `I2` (which admitted no one yet).
- **Steps:**
  1. J removes B (who came in through neither).
  2. J removes D.
  3. P6 requests through `I`; P7 through `I2`.
- **Expect:**
  - After step 1, both invites are still open; nothing about them is in the roster.
  - After step 2, `I` is closed for good; `I2` is open.
  - P6 is refused; P7 can be approved.
- **Invariants:** INV-BYSTANDER, INV-AUTHZ

## Concurrent changes

### PM-021 Two owner devices act on the same member
- **Kind:** edge
- **Design:** §8.2, §11.7, §9.4
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. Variant a: `J.phone` starts removing B; `J.mac` syncs home and J then tries to make B a reader there.
  2. Variant b: both are asked in the same second, neither seeing the other's note.
- **Expect:**
  - Variant a: `J.mac` doesn't sign; it offers to finish `J.phone`'s change (or asks, if it isn't on the hub). One change happens, then the other is refused (B is gone).
  - Variant b: House is dead once both are announced or published; `check world` shows the dead-collection matrix for every member.
- **Invariants:** INV-ONE-OWNER

### PM-023 Unrelated changes on two owner devices
- **Kind:** edge
- **Design:** §8.2, §11.7
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `J.phone` makes C a writer (same epoch). Ten seconds later, after syncing, `J.mac` removes D.
- **Expect:**
  - Two statements in a chain: C's promotion, then `E1` without D, built on it.
  - **Delta:** C writer, D removed, epoch `E1`.
- **Invariants:** INV-BYSTANDER, INV-ONE-OWNER

### PM-024 The world of a dead collection
- **Kind:** edge
- **Design:** §9.4, §11.8, §13.3
- **Binding:** any
- **Setup:** standard; House dies (PM-021 variant b).
- **Steps:**
  1. Every member attempts every action of the matrix.
- **Expect:**
  - Only reading the replica and copying are offered; every device ignores grants, data and statements for House.
  - Groceries and Trip are unaffected.
- **Invariants:** INV-BYSTANDER

### PM-025 A reader with a modified client
- **Kind:** adversarial
- **Design:** §8.3, §7.3
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `C.phone` writes data, `_meta`, a `delete` on `_meta`, a `_roster` statement, and a grant to P6, each signed with C's key.
- **Expect:**
  - All rejected everywhere; the world is unchanged.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### PM-026 A writer tries to delete the list; the owner does
- **Kind:** edge
- **Design:** §11.9
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. `A.phone` (modified client) signs a `deleted` statement.
  2. J deletes House.
- **Expect:**
  - Step 1 ignored.
  - Step 2: every member's world shows House deleted; local data read-only for 30 days; Groceries and Trip unchanged.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### PM-027 Waiting between the announcement and the grant
- **Kind:** edge
- **Design:** §11.6
- **Binding:** any
- **Setup:** standard.
- **Steps:**
  1. J removes D; `J.phone crashes after` S4 (announced, no grants sent); `check world`.
  2. `J.phone launches`; `check world`.
- **Expect:**
  - After step 1, members know of `E1`, hold no `K(E1)` yet, and publish nothing to channel `E0`; their edits queue. D is removed.
  - After step 2, everyone but D holds `K(E1)` and the queued edits are published.
- **Invariants:** INV-CUT, INV-NO-LOSS

## Families

### PM-F01 Family: permission churn with 2–10 players
- **Kind:** edge
- **Design:** §8, §11
- **Parameters:**
  - players 2–10, with random initial roles (P1 the owner, with 1–3 devices);
  - 20–100 operations, drawn from: invite by QR or link, request, approve, decline, promote, demote, remove, leave and confirm, new key, re-admit a former member, key request, delete;
  - actors chosen at random, **including actors not entitled** (modified clients);
  - the owner's operations spread across their devices, with the `flight` note in force;
  - interleaved with `M-LIST` record edits;
  - schedules `S-LIVE`, `S-SLEEP`, `S-OUTAGE`;
  - 200 seeds.
- **Oracle:** the roster model (the Lean reference model; cross-checked against `model/owner_model.py`), which applies §8.2's validity rules, §9.4's conflict rule and §11.3's invite rules to every statement made, and the reference model for records.
- **Expect:**
  - After every operation, once all devices sync, `check world` equals the oracle's world.
  - The difference between consecutive worlds is exactly the operation's delta (INV-BYSTANDER).
  - Removed members never decrypt a later epoch (INV-CUT).
  - Epochs never go backwards (INV-MONOTONE-EPOCH); exactly one owner (INV-ONE-OWNER).
  - A collection dies only in seeds where two of the owner's devices signed successors of one statement; the oracle names them.

### PM-F02 Family: key changes under load
- **Kind:** fault
- **Design:** §11.6, §11.7, §8.3
- **Parameters:** players 3–10; continuous `M-LIST` edits by every writer while 1–5 key changes happen (removals, demotions, new keys); the owner's device crashes at a random step S1–S6 in a third of seeds, resumed by itself or finished or discarded by another owner device; schedules `S-FLAP`, `S-SLEEP`; 200 seeds
- **Oracle:** the reference model plus the roster model
- **Expect:**
  - No honest current member's write is lost across a key change (INV-NO-LOSS): carried by the inventory, or signed again by its author.
  - Nothing a removed member wrote after the announcement appears anywhere.
  - Every crash ends with the change made or discarded, and the user told which.
- **Invariants:** INV-NO-LOSS, INV-CUT, INV-CONVERGE

---

Retired: PM-003, PM-004, PM-008, PM-013, PM-017, PM-022 (admins, hand-over and device revocation belong to withdrawn designs, §19; PM-022's race is covered by PM-021).
