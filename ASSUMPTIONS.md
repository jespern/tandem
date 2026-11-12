# What we assume about Tandem now

Written against `DESIGN.md` of 2026-11-12, 13:05 (973 lines, "cut down to the requirements"), uncommitted in the main checkout, with `model/owner_model.py` as its model (46 tests, all passing). Earlier versions of this note described two withdrawn designs; git history has them.

The scenarios on this branch were written against the earliest of those designs and are **out of date**; the refresh waits for Jesper's go-ahead.

## 1. The design in one page

**Users.**
- A user is **one secret** (`US`), held by all of their devices and printable as a **backup key**. Their user ID is their public signing key.
- There are no per-device keys, no device chain, no recovery key, and no way to remove one device.
- A new device is paired by QR code plus a **six-digit code** compared on both screens, and receives the secret sealed to a one-time key. Restoring from the backup key needs nothing on the hub to change.
- A stolen device or a seen backup key means **starting over** with a new identity: the user's own lists are copied into new ones and other people re-invite them.

**Collections.**
- **One owner for life**: whoever signed the genesis, fixed by the collection ID.
- Members are **writers or readers**. There are no admins and no hand-over.
- Only the owner changes sharing. The owner can't leave, only delete.

**Roster.**
- A chain of complete statements, signed by the owner's key, each `n + 1`. Nothing merges and nothing ranks.
- **Two owner-signed successors of one statement kill the collection.** It goes read-only for everyone, with an offer to copy it into a new collection. The two statements are posted where every member will find them.
- The owner's devices avoid that through a **`flight` note in home**: no device signs while another device's change is pending, and a device can finish or drop another device's change and tells the user which (§11.7). Two devices acting in the same moment, or a hub hiding one device's change from another, still kill the collection (Q20).

**Epochs.**
- An epoch is identified by the ID of its **starting statement**. The channel tag derives from both the key and that ID.
- Removing a member or lowering a role starts a new epoch with a fresh key. Adding someone doesn't.
- Rotation is owner-only, needs the network, and runs as a **journalled six-step procedure** that resumes after a crash with the same signed bytes (§11.6). The announcement in the old channel is the moment the change happens.

**Data validity.**
- A write names the roster statement it was made under. It is eligible in epoch `E` iff its source epoch is exactly `E`, or it is listed in `E`'s **snapshot inventory**.
- The inventory is canonical bytes whose hash is committed in the statement, published as fragments. It also carries forward entries the rotating device hasn't seen.
- Late writes cross epochs only when their author **signs them again**, by fixed rules that produce identical bytes on every device of that user.
- Validity never changes, so pruning is safe.

**Merge.**
- §9.2–§9.3 are unchanged: multi-value registers and an add-wins set over `context`/`base` vectors, and sticky tombstones, kept for good.
- New: a vector may hold at most **1,024 entries**. A field that would need more is reset by deleting the record and creating it again.

**Sharing.**
- **Invites live in the owner's home**, not the roster. The invite channel is keyed from the invite secret, so only the joiner and the owner's devices can read join requests.
- Grants are signed by the owner, and one grant catches a device up across any number of missed epochs.
- A removed member who asks for a key gets the signed statements that removed them.
- Leaving is a notice that the owner confirms. Declines are hints only.
- The collection's name and icon are an owner-only data record, `_meta`, so renaming works offline.

**Hub.** The bindings (§10) are unchanged, except that inventory fragments, the roster and the two statements of a dead collection are never pruned. Restoring a wiped hub is an explicit user action.

## 2. Still open from our work

1. **Garbage-collection tie-break (§10.7).** The lowest-`H(envelope)` tie-break still covers only "messages holding exactly the same writes".
   - **Mutual domination:** two messages with different writes can dominate each other (`m = {x, y}`, `n = {y}`, where `y` supersedes `x`). Two devices can each destroy one and lose `y`.
   - **Identical copies:** byte-identical copies tie on the hash itself (hub.md HB-024).
   - **Fix:** apply the tie-break to any mutual domination, and break hash ties by something both devices see the same way.
2. **Testbed findings not yet in §10** (`testbed/README.md`):
   - an `invalidArguments` answer to `Email/changes` must trigger a full resync, like `cannotCalculateChanges`;
   - reconnecting after missed pings must use the interval the server reports in its pings;
   - JMAP readers must check `isEncodingProblem` and fall back to the raw message;
   - the IMAP connection cap can be **per account** (Stalwart: 16), which on a shared hub caps IMAP devices across every user;
   - a push can arrive before the writer's own `Email/set` response.
3. **Device ID after a restore (small).** §6.3 says a paired device picks a new device ID, but restoring from the backup key doesn't say so.
   - **Why it matters:** the device ID sits in app data, which an iPhone migration copies. A restored phone could keep the old phone's device ID while the old phone is still in use.
   - **The effect:** the two devices' dots would then collide, and a later write covering one device's dot would silently supersede the other's write.
   - **Fix:** a device ID is only valid alongside the secret it was created with, so a new one is picked whenever the secret is (re)installed.

The fork-choice defects in the previous version of this note concerned a withdrawn design, and no longer apply. Their substance, one key on two devices looking like equivocation, is now a stated design property: it kills the collection (§11.7, Q20).

## 3. What this means for our work

### Scenarios (refresh pending)

| File | State |
|---|---|
| `identity.md` | Almost entirely obsolete. Device chains, recovery keys, revocation and theft are gone. Replace with pairing and its code, the backup key, restoring, and starting over. |
| `rotation.md` | Almost entirely obsolete. Replace with:<br>• the owner-only journalled rotation, crashing at each of its six steps;<br>• the `flight` note and finishing or dropping another device's change;<br>• inventories, including entries carried forward unseen;<br>• signing again;<br>• dead collections, and the news reaching every member. |
| `sharing.md` | Largely obsolete: admins, ranks and hand-over are gone. Rewrite for invites in home, owner-confirmed leaving, declines as hints, and copying a dead or orphaned collection. |
| `permissions.md` | Rewrite the capability matrix for owner / writer / reader (plus removed, dead and outsider). The world checks and the bystander invariant still apply. |
| `faults.md` | About a third needs redoing: crash points move to the journal, staleness is gone, and a hub hiding a change from another device now kills the collection. |
| `records.md`, `scale.md` | Mostly hold. The vector limit settles SC-011. A `delete` on a named record is now invalid. Swap epoch numbers for epoch-start IDs. |
| `hub.md` | Mostly holds; add inventory fragments and the records that are never pruned. |
| `collab.md`, `realtime.md` | Hold. |

New scenario areas:
- two devices of one owner;
- a dead collection, seen by members who can't read the channel where it died;
- a removed member's key request answered with statements;
- the pairing code against a photographed QR code;
- a restored phone and its device ID;
- the vector limit and the reset by re-creation.

### Proofs

The cut makes the Lean work smaller and more tractable.

- **Unchanged:** `Supersession`, `Register`, `ORSet`, `Tombstone`, `Display`, `HLC` and `GC` (its counterexample still applies).
- **Update:** `Encoding`. `sigInput` now carries the author's signing key as the user ID, and `ref`; add the canonical inventory bytes (injective and deterministic).
- **Replace** the roles, log and chain theorems with `Roster`:
  - a statement's validity depends only on its parent;
  - "dead" is a function of the set of statements held, whatever order they arrived in;
  - once dead, always dead: dead in any superset;
  - every device that holds both conflicting statements agrees the collection is dead.
- **Add:**
  - `Eligibility`: a write's eligibility in an epoch never changes;
  - `Reissue`: every device of a user signing an old write again produces the same bytes, and a re-signed write never supersedes a write the snapshot kept;
  - `VectorLimit`: building a write never exceeds 1,024 entries unless it says so.
- **Cross-check:** the Lean model is the oracle (decided). It is differentially tested against `model/owner_model.py`, whose 46 tests become Lean examples. `tandem_model.py` and `rotation_model.py` model withdrawn designs and are ignored.

### Unaffected

The testbed, the binding and real-time probes, and the Go reference SDK plan, which is now smaller: no device chain, no fork choice. The Swift plan likewise.
