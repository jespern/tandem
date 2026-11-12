# Epochs and key changes

Scope: when a collection moves to a new epoch, the owner's journalled six-step change (§11.6), how the owner's devices stay out of each other's way with the `flight` note (§8.2, §11.7), snapshot inventories, signing again, drafts, dead collections, and what removed members, misbehaving members and hub participants can do around it.

Conventions for this file: collections are `House`, `Groceries` and `Trip`. Unless a scenario says otherwise, `House` is owned by J, with A and B as writers and C as a reader, and its current epoch is `E3`: the epoch whose starting statement has ID `E3`. Labels like `E3` and `E4` are for reading only; the protocol knows an epoch by the full ID of its starting statement (§7.1), never by its counter. `K(E3)` is that epoch's key and "channel `E3`" its channel. `s` names roster statements; `s+1` is a successor of `s`. `inv(E4)` is the snapshot inventory committed by `E4`. "Announced" means the epoch-starting statement is in the old channel's `_roster` (§11.6 step 4). `wid(x)` is a write ID. "Holds K(E)" means the device's home `keys` (or, for the owner's devices before the announcement, the `flight` note) contains it.

The six steps of §11.6 are referred to as **S1** sync and choose, **S2** key, inventory, statement, note and journal, **S3** upload to the new channel, **S4** announce, **S5** grants, **S6** clean up the old channel.

---

## Real-world

### RO-001 Remove a friend from a shared list
- **Kind:** real-world
- **Design:** §8.2, §8.3, §11.6, §3.3, §13.2
- **Binding:** any
- **Setup:** `Groceries` in epoch `E3`: J owner, A writer, B writer. All devices online and synced. `B.phone` has one unpublished edit `b1` (debounce window).
- **Steps:**
  1. `J.phone removes B`.
  2. `J.phone syncs`; `A.phone`, `J.mac`, `J.ipad` sync.
  3. `B.phone syncs`.
- **Expect:**
  - The new statement `E4` has `n` one higher, the epoch counter one higher, a fresh key whose commitment occurs nowhere earlier in the roster, `snapshot = H(inventory bytes)`, and lists J and A only. It adds no member.
  - Before `E4` is exported, J's home holds a `flight` note for Groceries (parent, `E4`'s ID, members and roles, the new key) and `J.phone`'s journal holds the signed statement and inventory.
  - Channel `E4` holds the inventory fragments, the carried writes with their original signatures, and the full roster ending in `E4`, before `E4` appears in channel `E3`'s `_roster`.
  - After the announcement, the key moves from the note into `keys`, the note is removed, and grants go to J and A (not to B). No grant is sent before the announcement.
  - B's writes merged before S1 are in `inv(E4)` and still show author B.
  - After learning of `E4`, no device appends data to channel `E3`.
  - `B.phone` reads the announcement in channel `E3`, shows "Groceries is no longer shared with you", keeps its replica read-only for 30 days, and hands `b1` to the app (Liszto: Inbox). It holds no `K(E4)`.
  - Channel `E3`'s `_roster` stays for good; its data messages are destroyed in S6, each only once its writes are in channel `E4` or replaced there.
- **Invariants:** INV-CUT, INV-AUTHZ, INV-MONOTONE-EPOCH, INV-HUB-BLIND, INV-NO-LOSS, INV-UX

### RO-002 A member leaves
- **Kind:** real-world
- **Design:** §11.5, §11.6, §13.2
- **Binding:** any
- **Setup:** `House` in `E3`.
- **Steps:**
  1. `A.phone leaves House`.
  2. `J.mac syncs`; J confirms "Aimee left House".
  3. All sync.
- **Expect:**
  - A's home membership is `left` on both of A's devices; they stop following House.
  - Channel `E3` holds A's kind-3 leave notice, signed over `"TANDEM-LEAVE-V1" || collectionID || uid || at`, with `at` a statement of `E3`.
  - Until step 2, the roster still lists A.
  - Step 2 starts `E4` without A (RO-001's expectations), and A gets no grant.
- **Invariants:** INV-CUT, INV-BYSTANDER

### RO-004 Offline through a rotation, back three weeks later
- **Kind:** real-world
- **Design:** §11.4, §11.6, §8.3, §12.2
- **Binding:** any
- **Setup:** `Groceries` in `E3`. `A.phone` (A's only online device) goes offline with unsent edits to `milk.title` and `jam.notes`.
- **Steps:**
  1. Day 1: `J.mac removes B` (`E4`).
  2. Day 3: `J.mac deletes jam`.
  3. Day 21: `A.phone online`; `A.phone syncs`.
- **Expect:**
  - `A.phone` finds `E4`'s announcement in channel `E3`, opens its grant (inbox), checks the roster in channel `E4` through `at`, and stores `K(E4)` in home.
  - Its two edits aren't in `inv(E4)`. It signs both again (same dots, empty context, base = old base ∪ `reach`, `ref` = first `E4` statement listing A as writer) and publishes them to channel `E4`.
  - `jam` stays deleted (tombstone sticky); the re-signed `jam.notes` write is kept in the deleted record's state and never shown.
  - `milk.title` shows A's edit unless a newer write supersedes it.
  - `A.phone` appends nothing to channel `E3` after reading the announcement.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH, INV-CUT, INV-CONVERGE

### RO-005 Make a list private again
- **Kind:** real-world
- **Design:** §7.4, §11.6
- **Binding:** any
- **Setup:** `House` in `E3`; A offline.
- **Steps:**
  1. `J.mac` removes A, B and C in one change.
  2. `A.phone online`; all sync.
- **Expect:**
  - One statement `E4` lists only J; one key change, not three.
  - A, B and C each learn from the announcement in channel `E3` and show "House is no longer shared with you".
  - Only J's devices hold `K(E4)`.
- **Invariants:** INV-CUT, INV-ONE-OWNER

### RO-006 The owner asks for a new key
- **Kind:** real-world
- **Design:** §11.6, §8.2
- **Binding:** any
- **Setup:** `House` in `E3`.
- **Steps:**
  1. J chooses "New key" for House on `J.mac`.
- **Expect:**
  - `E4` keeps every member and role, advances the epoch counter, and commits a fresh key and an inventory.
  - Every member gets a grant; every device moves; no record changes.
- **Invariants:** INV-BYSTANDER, INV-MONOTONE-EPOCH

---

## Edge

### RO-007 Removed, then let back in
- **Kind:** edge
- **Design:** §11.3, §11.6, §8.2
- **Binding:** any
- **Setup:** `House` in `E3`. `B.phone` has an unsent edit `b1`.
- **Steps:**
  1. `J.mac removes B` (`E4`). `B.phone syncs` (learns of it; `b1` kept for the app).
  2. A day later B asks to come back. J creates a new link invite; B joins; J's device asks "Bo was removed from House. Add him again?"; J approves (statement `s5`, same epoch `E4`).
  3. `B.phone syncs`.
- **Expect:**
  - `s5` adds B with his unchanged `uid`, `uka` and `name` and `via` = the new invite. It keeps `E4`'s key and snapshot commitments.
  - B gets a grant for `E4` and follows House again.
  - `b1` may now be signed again: the first `E4` statement listing B as a writer is `s5`, so its `ref` is `s5`.
  - The invite B first came in through stays closed for good (§11.3).
- **Invariants:** INV-AUTHZ, INV-CONVERGE

### RO-008 A join approved while a key change is under way
- **Kind:** edge
- **Design:** §8.2, §11.3, §11.6
- **Binding:** any
- **Setup:** `House` in `E3`. D has posted a link-invite request.
- **Steps:**
  1. `J.mac removes B`; it reaches S3 (the note is in home).
  2. J taps Approve for D on `J.mac`.
  3. `J.mac` finishes the key change.
- **Expect:**
  - `J.mac` doesn't sign the admission while its own change is unannounced; it queues it.
  - After S4, it signs the admission as the successor of `E4`. D is granted `K(E4)` and never holds `K(E3)`.
  - There is exactly one successor of each statement.
- **Invariants:** INV-AUTHZ, INV-ONE-OWNER
- **Notes:** §8.2 says only that a device doesn't sign while *another* device's note is there. That the same device waits for its own announcement follows from "never signs two different successors of one statement".

### RO-009 An auto-approve join on one owner device while another rotates
- **Kind:** edge
- **Design:** §8.2, §11.3, §11.7, §9.4, Q20
- **Binding:** any
- **Setup:** `House` in `E3`. `J.ipad` is showing an auto-approve QR. `J.mac` starts removing B.
- **Steps:**
  1. Variant a: `J.mac` writes its note (S2); `J.ipad` syncs home; D scans the QR.
  2. Variant b: D scans the QR and `J.ipad` admits D in the same second that `J.mac` signs `E4`, before either sees the other's work.
- **Expect:**
  - Variant a: `J.ipad` sees the note and doesn't admit D while it is there; D waits; after the announcement `J.ipad` admits D under `E4`.
  - Variant b: `J.ipad`'s same-epoch statement and `J.mac`'s draft `E4` are both successors of the same statement once `E4` is announced. See RO-053 for what `J.mac` should do before announcing; under the text as written, announcing makes House dead on every device holding both.
- **Invariants:** INV-ONE-OWNER, INV-CONVERGE

### RO-010 A leave notice races a key change
- **Kind:** edge
- **Design:** §11.5, §11.6
- **Binding:** any
- **Setup:** `House` in `E3`.
- **Steps:**
  1. `J.mac removes B` and reaches S3.
  2. `A.phone leaves House`; its leave notice names `at` = the last `E3` statement and is posted in channel `E3`.
  3. `J.mac` announces `E4`.
  4. All sync.
- **Expect:**
  - Once `E4` is current, A's notice no longer counts (`at` isn't a statement of the current epoch).
  - A stays listed in `E4` and is granted `K(E4)`; A's devices ignore the grant (membership `left`).
  - Desired: J sees "Aimee left House" and A is removed.
- **Invariants:** INV-UX, INV-BYSTANDER
- **Notes:** GAP (G-06): a leave notice made in the epoch the owner is just leaving is lost. The leaver's devices have stopped following and never post it again, and the owner's devices drop it because `at` is no longer in the current epoch (§11.5). A stays a member for good, is sent every future key, and the owner is never told. A fix: the owner's devices also honour a notice whose `at` is the last statement of the epoch just left, or the leaver re-posts when it sees the announcement.

### RO-011 A second change asked for on the same device
- **Kind:** edge
- **Design:** §8.2, §11.6
- **Binding:** any
- **Setup:** `House` in `E3`.
- **Steps:**
  1. `J.mac removes B`; while it is at S3, J also makes C a writer on `J.mac`.
- **Expect:**
  - The promotion waits until `E4` is announced, then is signed as a same-epoch successor of `E4`.
  - No grant for the promotion is needed (C already holds `K(E4)`).
- **Invariants:** INV-ONE-OWNER, INV-BYSTANDER

### RO-012 Two owner devices change sharing in the same moment
- **Kind:** edge
- **Design:** §8.2, §9.4, §11.7, Q20
- **Binding:** any
- **Setup:** `House` in `E3`. `J.phone` and `J.mac` are both synced.
- **Steps:**
  1. In the same second, `J.phone removes B` and `J.mac` makes C a writer. Neither sees the other's note before signing.
  2. All sync.
- **Expect:**
  - Once `J.phone`'s `E4` is announced, the roster has two successors of the same statement: House is dead on every device that holds both (§9.4).
  - Every member device stops publishing, accepting grants and merging for House, puts the two statements in the current epoch channel's `_roster`, keeps its replica read-only, and offers to copy.
  - Members never end up following different rosters.
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER
- **Notes:** The design accepts this outcome (Q20). See RO-053 for the variant where `J.mac`'s change is the unannounced one.

### RO-013 Writer made a reader
- **Kind:** edge
- **Design:** §8.2, §8.3, §11.6
- **Binding:** any
- **Setup:** `House` in `E3`. `B.phone` has an unsent edit `b1`.
- **Steps:**
  1. `J.mac` makes B a reader (`E4`).
  2. `B.phone syncs`; `B.phone` (modified client) then signs a write naming `E4`.
- **Expect:**
  - `E4` starts an epoch (lowering a role), lists B as reader, and B gets `K(E4)`.
  - `b1` isn't in `inv(E4)` and no `E4` statement lists B as writer: B's device doesn't sign it again and hands it to the app.
  - Step 2's write is rejected everywhere.
  - B's `E3` writes in `inv(E4)` remain.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### RO-017 Thirty days after removal
- **Kind:** edge
- **Design:** §11.6, §9.4 (holding limits)
- **Binding:** any
- **Setup:** RO-001 done; `B.phone` holds a read-only replica.
- **Steps:**
  1. `wait 29 days`; `B.phone launches`.
  2. `wait 2 days`; `B.phone launches`.
- **Expect:**
  - Step 1: Groceries is still visible read-only on `B.phone`, with its unsent work offered for rescue.
  - Step 2: the replica is gone (default 30 days, configurable). Nothing on the hub changes because of it.
- **Invariants:** INV-UX

### RO-019 Away across two key changes
- **Kind:** edge
- **Design:** §11.4, §11.6, §8.3, §12.2
- **Binding:** any
- **Setup:** `House` in `E3`. `A.mac` is A's only device; it goes offline with an unsent edit `a1` to `milk.title`.
- **Steps:**
  1. `J.mac removes B` (`E4`), then removes C (`E5`).
  2. `A.mac online`; `A.mac syncs`.
- **Expect:**
  - `A.mac` follows the announcements `E3 → E4 → E5` through each channel's `_roster`, and uses the latest grant: one grant at `E5` carries the full roster in channel `E5`.
  - It never needs `inv(E4)` to move into `E5`; it needs `inv(E5)`.
  - `a1` is signed again into `E5` (`ref` = first `E5` statement listing A as writer).
  - It publishes nothing to channels `E3` or `E4`.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH
- **Notes:** If A's grant at `E4` arrives and the one at `E5` is destroyed, `A.mac` holds `K(E4)`, learns of `E5` from channel `E4`'s announcement, and sends a key request after 10 minutes (§11.4).

---

## Fault

### RO-020 A key change interrupted at each step
- **Kind:** fault
- **Design:** §11.6, §11.7, §8.2, §8.3
- **Binding:** any
- **Setup:** `House` in `E3` with 40 records. `J.mac` removes B. `J.phone` is J's other device, online.
- **Steps:** For each step S1…S6, a run in which `J.mac crashes after` that step (S3 is cut halfway: inventory fragments up, 20 of 40 records up, no roster). Then:
  1. `J.mac launches` within a minute (variant "resume"), or never comes back and J opens House sharing on `J.phone` (variant "other device").
  2. All sync.
- **Expect:**

  | Crash after | Before restart | Resume on `J.mac` | Handled on `J.phone` |
  |---|---|---|---|
  | S1 | No note, nothing exported | Nothing to resume; J asks again | No note: nothing to finish |
  | S2 | Note in home; nothing in channel `E4` | Resumes S3 with the journal's exact bytes | Offers "do it here or discard"; either way removes the note and its key |
  | S3 (partial) | Some of channel `E4` uploaded | Completes S3 with the same bytes, verifies, restores anything missing | Statement not on the hub: as S2, and destroys what was uploaded |
  | S3 (done) | Channel `E4` complete, not announced | Announces | Finishes it, telling J ("Finishing a change started on Jesper's Mac: …"), checking channel `E4` against `inv(E4)` |
  | S4 | Announced; key may still be in the note | Moves the key into `keys`, removes the note, sends grants | Finishes from S5 |
  | S5 | Grants sent | Cleans up | Cleans up |
  | S6 | Partly cleaned | Finishes S6 | Finishes S6 |

  - In every run, members never see two successors of one statement, and nobody but J and A ends up holding `K(E4)`.
  - Before the announcement, members keep publishing to channel `E3`; nothing stalls.
  - No data message of channel `E3` is destroyed while one of its writes is neither in channel `E4` nor replaced there nor uncarried.
- **Invariants:** INV-CUT, INV-NO-LOSS, INV-ONE-OWNER, INV-NO-HOLD-FOREVER
- **Notes:** GAP (G-07, minor): S2 writes the note to home and persists the journal "before exporting any of them", but doesn't order the two. A device that loses its journal after its note reached home can't resume (§11.7: "while its note is still there, it resumes") and must take the "otherwise" path §11.7 describes for another device.

### RO-022 A discarded change's channel is destroyed under a device holding its key
- **Kind:** fault
- **Design:** §11.6, §11.7, §10.7, §9.4
- **Binding:** any
- **Setup:** `House` in `E3`. `J.mac` started removing B, wrote its note (with key `K'`) and uploaded part of channel `E4'`, then crashed for a week. `J.ipad` read the note (and `K'`) before going offline.
- **Steps:**
  1. J opens sharing on `J.phone` and chooses "Discard".
  2. `J.ipad` comes online.
  3. `J.mac` launches.
- **Expect:**
  - Step 1: `J.phone` removes the note and destroys everything in channel `E4'`, its draft statement included (the one place a roster copy is destroyed, §10.7).
  - Step 2: `J.ipad` never treats `E4'` as made: there is no announcement in channel `E3`. It discards `K'` and keeps publishing to `E3`.
  - Step 3: `J.mac` finds no announcement and its note gone, discards its journal entry and anything it had uploaded, and doesn't announce.
  - House stays alive with B still a member.
- **Invariants:** INV-ONE-OWNER, INV-CONVERGE

### RO-023 The rotating device's clock is a year ahead
- **Kind:** fault
- **Design:** §9.1, §11.6, §8.2
- **Binding:** any
- **Setup:** `House` in `E3`. `J.mac clock = +1 year`.
- **Steps:**
  1. `J.mac removes B`.
  2. Members sign late writes again into `E4`.
- **Expect:**
  - Nothing about which statement or epoch is current depends on a clock.
  - Re-signed writes keep their original dots.
  - `J.mac`'s own new writes carry year-ahead HLCs, which win only the display order among siblings (§9.2), never supersession.
- **Invariants:** INV-CONVERGE

---

## Adversarial

### RO-024 A removed member backdates writes into the old epoch
- **Kind:** adversarial
- **Design:** §3.3, §8.3, §11.6
- **Binding:** any
- **Setup:** RO-001 done (`E4` without B).
- **Steps:**
  1. `B.phone` (modified client) appends writes to channel `E3` naming `E3` statements, with dots before the removal.
  2. B re-encrypts them into channel `E4` with a stolen `K(E4)` (harness).
- **Expect:**
  - Step 1: no device that has moved to `E4` shows them. (They are valid in `E3`'s own history only.)
  - Step 2: ineligible in `E4` (not `inv(E4)`, source epoch not `E4`); dropped and logged.
  - B can't sign them again into `E4`: no `E4` statement lists B.
- **Invariants:** INV-AUTHZ, INV-CUT

### RO-025 A reader copies archived writes into the new channel
- **Kind:** adversarial
- **Design:** §8.3, §11.6
- **Binding:** any
- **Setup:** `House` moved from `E3` to `E4`. C is a reader in both. C holds old `E2` writes of B that weren't carried into `E3`.
- **Steps:**
  1. C re-encrypts writes listed in `inv(E4)` into channel `E4`.
  2. C re-encrypts B's old `E2` writes into channel `E4`.
- **Expect:**
  - Step 1's writes are accepted: they are listed and their named statements are ancestors of `E4`. Nothing changes (they were already there or are a valid restore, §12.2).
  - Step 2's writes are rejected: not listed in `inv(E4)`.
- **Invariants:** INV-AUTHZ, INV-CONVERGE

### RO-027 A late write to a record deleted before the key change
- **Kind:** adversarial
- **Design:** §8.3, §9.2, §11.6, §12.3
- **Binding:** any
- **Setup:** `House` in `E3`. A deleted `lamp`; `A.mac` (offline) has an unsent edit to `lamp.title`.
- **Steps:**
  1. `J.mac removes B` (`E4`; `lamp`'s tombstone is in `inv(E4)`).
  2. `A.mac` comes online and signs its edit again into `E4`.
- **Expect:**
  - `lamp` stays deleted on every device; the re-signed write is kept in its state and never shown.
- **Invariants:** INV-CONVERGE

### RO-028 Eve destroys grants and key requests
- **Kind:** adversarial
- **Design:** §11.4, §3.3
- **Binding:** any
- **Setup:** RO-001 under way; E holds the hub credential.
- **Steps:**
  1. After S5, `hub: destroy grants for A`.
  2. At 10 minutes `A.phone` posts a key request to J's inbox and a notice in channel `E3`. E destroys the inbox copy.
  3. `J.phone` syncs.
- **Expect:**
  - `J.phone` sees the notice in channel `E3` (owner devices watch earlier epochs' tags, keys in home) and sends A a grant for `E4`.
  - A shows House in `E4` within one sync of the grant.
  - The 10 minutes are a safety net: the request is sent once per missing epoch, not on a timer loop.
- **Invariants:** INV-NO-HOLD-FOREVER

### RO-029 Eve destroys the old roster and an offline member's grant
- **Kind:** adversarial
- **Design:** §12.2, §11.4, §11.6
- **Binding:** any
- **Setup:** RO-001 done. `A.mac` was offline throughout.
- **Steps:**
  1. `hub: destroy` channel `E3`'s `_roster` messages; `hub: destroy grants for A`.
  2. Variant a: a device that has moved on syncs first. Variant b: `A.mac` comes online first.
- **Expect:**
  - Variant a: the device restores channel `E3`'s `_roster` (devices that moved on keep it complete, §12.2); `A.mac` then learns of `E4` and requests a key.
  - Variant b: `A.mac` finds no `_roster` in channel `E3`: it pauses healing there, looks for a newer epoch in home and inbox, and sends a key request. J's device answers with a grant for `E4`.
  - In both, `A.mac` never re-publishes data into channel `E3` and nothing it writes is readable with `K(E3)` alone.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-NO-HOLD-FOREVER

### RO-033 Malformed statements and a replayed grant
- **Kind:** adversarial
- **Design:** §8.2, §9.4, §11.4
- **Binding:** any
- **Setup:** `House` in `E3`, head statement `s`. M is a writer. E holds the hub credential.
- **Steps:**
  1. M signs a `_roster` successor of `s` with her own key.
  2. A buggy owner client on `J.ipad` signs a successor of `s` whose `n` skips one (`s+2`), while `J.mac` signs a valid one.
  3. E replays an old grant for `E2` to A.
  4. Separate runs, each a single owner-signed successor of the head: two owners; a fourth role; a staying member with a changed `name`; an epoch start that also adds a member; an epoch start reusing an earlier commitment; a same-epoch statement with a new snapshot commitment; a successor of a `deleted` statement.
- **Expect:**
  - Step 1: ignored; signatures with other keys don't count toward a conflict.
  - Step 2: House is dead: two owner-signed successors of one statement, "even if one of the two is invalid" (§9.4).
  - Step 3: ignored; an older grant never moves a device back.
  - Step 4: each lone invalid statement is rejected and the collection stays at `s`; none of them is a conflict on its own.
- **Invariants:** INV-AUTHZ, INV-ONE-OWNER, INV-MONOTONE-EPOCH

---

## UX

### RO-034 Removing someone without a network
- **Kind:** ux
- **Design:** §11.6, §13.2, §13.3
- **Binding:** any
- **Setup:** `House` in `E3`. `J.phone offline`.
- **Steps:**
  1. J taps Remove on Bo.
- **Expect:**
  - `J.phone` shows "Changing who a list is shared with needs a connection." and signs nothing.
  - Content edits keep working offline.
- **Invariants:** INV-UX

### RO-035 Waiting for the new key
- **Kind:** ux
- **Design:** §11.4, §11.6
- **Binding:** any
- **Setup:** RO-001 under way; `J.phone` crashes after S4. J's other devices are offline.
- **Steps:**
  1. `A.phone syncs`; A edits Groceries for 20 minutes.
  2. `J.phone launches`.
- **Expect:**
  - `A.phone` knows of `E4` from the announcement and stops publishing to channel `E3`. Its edits apply locally and are queued; a subtle indicator says it is waiting.
  - At 10 minutes it sends one key request.
  - After step 2, `J.phone` resumes at S5 and A's grant arrives; A's queued edits are signed into `E4` and published.
- **Invariants:** INV-NO-LOSS, INV-UX

### RO-036 A demoted writer's unsent edits
- **Kind:** ux
- **Design:** §11.6, §13.2
- **Binding:** any
- **Setup:** As RO-013.
- **Steps:**
  1. `B.phone syncs`.
- **Expect:**
  - B is told he can now only read House; `b1` is offered for rescue (Liszto: copied to B's Inbox).
- **Invariants:** INV-UX

---

## Inventories and signing again

### RO-037 An inventory entry carried forward unseen
- **Kind:** edge
- **Design:** §8.3 ("nothing listed is dropped unseen"), §12.2
- **Binding:** any
- **Setup:** `House` moved to `E4`; `inv(E4)` lists A's write `t` on `milk.title`. E destroyed `t`'s message from channel `E4` before `J.mac` ever saw it. `A.mac`, which holds `t`, is offline.
- **Steps:**
  1. `J.mac removes C` (`E5`).
  2. `A.mac` comes online and syncs.
- **Expect:**
  - `inv(E5)` lists `t` although `J.mac` doesn't hold it: entries of `inv(E4)` that `J.mac` neither holds nor has seen replaced are carried forward.
  - `A.mac` re-publishes `t` into channel `E5` (healing, §12.2); it is eligible there and `milk.title` shows it on every device.
  - Control run without the rule: `t` is never eligible again, and only A could sign it again.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE

### RO-038 A missing inventory fragment holds the move
- **Kind:** fault
- **Design:** §8.3, §10.5, §10.7, §12.2, §11.6
- **Binding:** any
- **Setup:** `House` has 6,000 records, so `inv(E4)` is published in three fragments.
- **Steps:**
  1. `J.mac removes C` (`E4`).
  2. `hub: destroy` fragment 1 of 3 before `A.phone` syncs.
  3. `A.phone syncs`. Then `J.mac` syncs.
- **Expect:**
  - `A.phone` learns of `E4` and stops publishing to channel `E3`, but doesn't move into `E4` until the three fragments assemble to bytes whose hash matches `E4`'s `snapshot`.
  - `J.mac` restores the fragment (fragments are never dominated, §10.7). `A.phone` then moves.
  - While a listed write is merely absent, nothing is held.
  - A device lacking `inv(E4)` can't start `E5` (§8.3).
- **Invariants:** INV-NO-LOSS, INV-NO-HOLD-FOREVER

### RO-039 A forged inventory fragment
- **Kind:** adversarial
- **Design:** §10.5, §8.3
- **Binding:** any
- **Setup:** As RO-038, before step 2. M (a writer, holding `K(E4)`) is still a member.
- **Steps:**
  1. M posts a fragment with `part: 1`, `parts: 3` and different bytes, listing her own old writes.
  2. M posts fragments claiming `parts: 2`.
- **Expect:**
  - Devices hold inconsistent versions separately and use only an assembly whose hash matches the signed `snapshot`.
  - M's listed writes never become eligible; nothing is held because of her fragments for longer than the holding limit.
- **Invariants:** INV-AUTHZ, INV-NO-HOLD-FOREVER

### RO-040 Two devices of one user sign the same write again
- **Kind:** edge
- **Design:** §11.6 (signing again)
- **Binding:** any
- **Setup:** `House` in `E3`. `A.phone` published a write `x` to channel `E3` after `J.mac` fixed `inv(E4)`; `A.mac` merged it.
- **Steps:**
  1. `J.mac` announces `E4`.
  2. `A.phone` and `A.mac` both move to `E4` and sign `x` again.
- **Expect:**
  - Both produce byte-identical writes with the same write ID; the hub ends with one message holding it.
- **Invariants:** INV-UNIQUE, INV-CONVERGE

### RO-041 A write the snapshot replaced is not signed again
- **Kind:** edge
- **Design:** §11.6, §9.2
- **Binding:** any
- **Setup:** `House` in `E3`. A wrote `x` to `milk.title`, published it, then went offline. J saw `x` and wrote `y` over it; `inv(E4)` lists `y`, not `x`.
- **Steps:**
  1. `J.mac removes C` (`E4`). `A.phone` comes online.
- **Expect:**
  - `A.phone` doesn't sign `x` again: the new state supersedes it.
  - `milk.title` shows `y` with no conflict on any device.
  - Control run (forcing a re-sign): `x'` and `y` are siblings and the replaced value returns, which is why the rule exists.
- **Invariants:** INV-CONVERGE, INV-INTENT

### RO-042 Signing again names the first statement that allows it
- **Kind:** edge
- **Design:** §11.6
- **Binding:** any
- **Setup:** `House` in `E3`. `B.phone` (offline) has an unsent edit `b1`.
- **Steps:**
  1. `J.mac` makes B a reader (`E4`), then a writer again (`s5`, same epoch).
  2. `B.phone` comes online.
- **Expect:**
  - `b1` is signed again with `ref = s5`, not `E4`, and is accepted.
  - Two of B's devices doing this produce the same bytes.
- **Invariants:** INV-AUTHZ, INV-UNIQUE

### RO-043 Signing again would exceed the vector limit
- **Kind:** edge
- **Design:** §11.6, §9.2 (vector size)
- **Binding:** any
- **Setup:** `House` in `E3`. A's late write `x` has a base of 1,024 entries; `reach(x)` adds one more device.
- **Steps:**
  1. `J.mac removes C` (`E4`); `A.phone` moves.
- **Expect:**
  - `A.phone` doesn't publish an invalid write; it keeps `x` for the app and tells it.
- **Invariants:** INV-UX, INV-CONVERGE

---

## The owner's devices

### RO-044 Finishing a change another device started
- **Kind:** real-world
- **Design:** §11.7, §13.2
- **Binding:** any
- **Setup:** `House` in `E3`. `J.phone` began making A a reader; it completed S3 and then its battery died.
- **Steps:**
  1. On `J.mac`, J taps Remove on C.
- **Expect:**
  - `J.mac` doesn't sign. It shows "Finishing a change started on Jesper's iPhone: making Aimee read-only in House."
  - It checks channel `E4` against `inv(E4)`, copies across from channel `E3` any listed write that is missing, announces, sends grants and cleans up.
  - Then it removes C as a successor of `E4` (`E5`).
  - When `J.phone` returns, it finds the announcement of its change, and carries on from S5 (re-sending grants is harmless).
- **Invariants:** INV-ONE-OWNER, INV-NO-LOSS, INV-UX

### RO-045 Discarding a change that never got far enough
- **Kind:** real-world
- **Design:** §11.7, §13.2
- **Binding:** any
- **Setup:** `House` in `E3`. `J.phone` began removing B; it wrote its note and uploaded part of channel `E4`, then was lost.
- **Steps:**
  1. On `J.mac`, J opens House sharing.
  2. Variant a: "Discard". Variant b: "Do it here".
- **Expect:**
  - `J.mac` asks: "Jesper's iPhone started removing Bo from House and didn't finish. Do it here, or discard it?"
  - Either way it removes the note and its key from home and destroys what was uploaded.
  - Variant b: it then removes B itself, with a new key and a new statement `E4'` (a different ID from the discarded draft).
  - The user is told which change was dropped in every case.
- **Invariants:** INV-ONE-OWNER, INV-UX

### RO-046 The first device returns after its change was discarded
- **Kind:** fault
- **Design:** §11.7
- **Binding:** any
- **Setup:** RO-045 variant a done. `J.phone` is found and launched.
- **Steps:**
  1. `J.phone launches`; `J.phone syncs`.
- **Expect:**
  - `J.phone` finds no announcement of its statement in channel `E3` and its note gone. It discards its journal entry and anything it had uploaded, and never announces.
  - House stays alive.
- **Invariants:** INV-ONE-OWNER

### RO-047 The hub hides one owner device's change from another
- **Kind:** adversarial
- **Design:** §11.7, §9.4, §8.2
- **Binding:** any
- **Setup:** `House` in `E3`.
- **Steps:**
  1. `J.phone` removes B; `hub: withhold` J's home changes and channel `E3`'s new `_roster` message from `J.mac`.
  2. J makes C a writer on `J.mac`.
  3. The hub stops withholding; all sync.
- **Expect:**
  - Two owner-signed successors of the same statement exist: House is dead on every device holding both.
  - Members who can't read channel `E4` learn it from the two statements placed in the current epoch channel they can read.
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER
- **Notes:** Accepted by the design (§11.7, "the hub hides one device's change from another").

### RO-048 News of a dead collection reaches a member who can't read where it died
- **Kind:** edge
- **Design:** §9.4, §12.2
- **Binding:** any
- **Setup:** `House`: `E4` was announced from `J.phone`, then D joined in `E4`. A conflict then arose on `E3`'s last statement (RO-047-style), visible in channel `E3` only.
- **Steps:**
  1. `A.phone` (which holds `K(E3)`) syncs and finds the two statements.
  2. `D.phone` syncs.
  3. `hub: destroy` the copies in channel `E4`; `A.phone` syncs.
- **Expect:**
  - `A.phone` puts both statements into channel `E4`'s `_roster`.
  - `D.phone`, which never held `K(E3)`, verifies both owner signatures and declares House dead.
  - Step 3: `A.phone` restores them.
- **Invariants:** INV-CONVERGE

### RO-049 A dead collection is copied
- **Kind:** real-world
- **Design:** §11.8, §9.4, §13.2
- **Binding:** any
- **Setup:** `House` is dead (RO-012).
- **Steps:**
  1. A taps "Copy it to a new list".
  2. A invites J, B and C to the copy.
- **Expect:**
  - A new collection owned by A holds the current records as A's writes; no tombstones and no old signatures come along.
  - The old House stays read-only on every device; no membership is redirected to the copy.
  - Unsent edits on any device are kept for the app.
- **Invariants:** INV-ONE-OWNER, INV-NO-LOSS

### RO-050 Different contents at the moment of death
- **Kind:** edge
- **Design:** §9.4
- **Binding:** any
- **Setup:** As RO-012, with A and B each holding edits the other hasn't seen when the conflict arrives.
- **Steps:**
  1. All sync.
- **Expect:**
  - A and B agree House is dead; each keeps its own replica read-only. Their contents may differ; nothing is rolled back and nothing more is merged.
- **Invariants:** INV-UX
- **Notes:** INV-CONVERGE is suspended for dead collections by design.

### RO-051 Equal counters and keys don't make one epoch
- **Kind:** adversarial
- **Design:** §7.1, §7.2, §8.3
- **Binding:** any
- **Setup:** `House` in `E3`. A discarded draft `E4'` and the real `E4` both have epoch counter 4. M (a writer) held `K(E4')` from a buggy owner device.
- **Steps:**
  1. M publishes writes naming `E4'` into channel `E4`.
- **Expect:**
  - Rejected: their source epoch is `E4'`, not `E4`, and they aren't in `inv(E4)`. Matching the counter (or a key) is never sufficient.
- **Invariants:** INV-AUTHZ

### RO-052 Old data is cleaned up only once it is covered
- **Kind:** fault
- **Design:** §11.6 (S6), §10.7
- **Binding:** any
- **Setup:** RO-001 at S6. One write listed in `inv(E4)` is missing from channel `E4`; another record's message in channel `E3` holds a late write of A's not yet signed again.
- **Steps:**
  1. `J.phone` runs S6.
- **Expect:**
  - The missing listed write is copied across first, then its old message destroyed.
  - The message holding A's late write may be destroyed: that write was not carried over (§11.6 S6). The device that wrote it still holds it and signs it again into `E4` when it moves; if that device is gone, the write is lost, as §11.6 accepts for late writes that miss the inventory.
  - Channel `E3`'s `_roster` is never destroyed; home keeps `K(E3)`.
- **Invariants:** INV-GC-SAFE, INV-NO-LOSS

### RO-053 A draft overtaken before it is announced
- **Kind:** edge
- **Design:** §11.6, §11.7, §9.4, §8.2
- **Binding:** any
- **Setup:** `House` in `E3`, head `s`. `J.mac` is removing B and has completed S3 (draft `E4`, successor of `s`). `J.ipad`, showing an auto-approve QR, hasn't yet seen `J.mac`'s note and admits D as `s+1`, a same-epoch successor of `s`.
- **Steps:**
  1. `J.mac` reaches S4.
- **Expect (desired):**
  - Before announcing, `J.mac` sees that `s` already has a successor. Since an unannounced draft is "not a successor, and not one half of a conflict", it discards its draft (destroying channel `E4`) and repeats the removal on top of `s+1`, telling J. House stays alive.
- **Invariants:** INV-ONE-OWNER
- **Notes:** GAP (G-08): §11.6 S4 doesn't say to check that the statement's parent is still the head before announcing, and §11.6 tells a device to resume "the same change" and "never abandon signed bytes it has exported". Read literally, `J.mac` announces and kills a collection that the draft rule could have saved. §11.7's list of what kills a collection includes "two devices are asked for a change in the same moment"; this is the case where the design already has the means to avoid it.

---

Retired: RO-003, RO-014, RO-015, RO-016, RO-018, RO-021, RO-026, RO-030, RO-031, RO-032 (device loss no longer rotates; admins, PUK manifests, fences, `_carried` lists, competing geneses and the tombstone horizon belong to withdrawn designs, §19).
