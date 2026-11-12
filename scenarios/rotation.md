# Epochs and rotation

Scope: when a collection moves to a new epoch, how the rotating device and every other device behave, which writes and keys cross into the new epoch, and what removed members, revoked devices, misbehaving members and hub participants can do around it.

Conventions for this file: collections are `House`, `Groceries` and `Trip`. Unless a scenario says otherwise, `House` is owned by J and in epoch 3, so `K(3)` is its current key and "channel 3" its current channel. `G(n)` is a genesis for epoch `n`; `G(n)/X` names one of several competing geneses for `n`. `wid(x)` is a write ID. `kid(U, n)` is the key ID of `UKa(n)` of user U. "Holds K(n)" means the device's home or key store contains that epoch key.

---

## Real-world

### RO-001 Remove a friend from a shared list
- **Kind:** real-world
- **Design:** §8.2, §8.3, §11.6, §13.2
- **Binding:** any
- **Setup:** `Groceries` in epoch 3: J owner, A writer, B writer. All devices online and synced. B.phone has one unpublished edit `b1` (debounce window).
- **Steps:**
  1. `J.phone removes B`.
  2. `J.phone syncs` (it rotates after its random delay).
  3. `A.phone syncs`, `J.mac syncs`, `J.ipad syncs`.
  4. `B.phone syncs`.
- **Expect:**
  - `_roster` holds `member:B` with `status: removed`, signed by J.phone, in the past of `G(4)`.
  - Exactly one `G(4)`, signed by J.phone, in both channel 3 and channel 4, with `n: 4`, `prev = commit(K(3))`, `to = [[J, kid(J, current)], [A, kid(A, current)]]`. B is not in `to`.
  - J.phone, J.mac, J.ipad, A.phone, A.mac hold `K(4)`; their home `membership:Groceries.key.n = 4`. B's inbox receives no grant for epoch 4.
  - Every record in channel 4 has one `_carried` write signed by J.phone, stamped 4, naming each kept epoch-3 write. B's writes that J.phone merged before it wrote the removal are named and still show author B on A.phone.
  - After seeing `G(4)`, no device appends a record message to channel 3.
  - B.phone shows "Groceries is no longer shared with you", hands `b1` to the app (Liszto: Inbox), and deletes its `Groceries` replica. B.phone holds `K(3)` only.
- **Invariants:** INV-CUT, INV-AUTHZ, INV-MONOTONE-EPOCH, INV-HUB-BLIND, INV-NO-LOSS, INV-UX
- **Notes:** GAP: §11.6 says the removed device deletes its replica, but not that it sets `membership:Groceries.state = removed` in home (§11.7 does say so for a deleted collection). If it stays `active`, a later grant for this collection, for instance after an admin re-adds B without B asking, passes the first acceptance check in §11.4. That contradicts "a collection the user never asked for is never added".

### RO-002 A member leaves
- **Kind:** real-world
- **Design:** §11.5, §8.2, §11.6, §8.3
- **Binding:** any
- **Setup:** `House` epoch 3: J owner, A writer, C writer. C.phone is offline with two local edits `c1`, `c2`.
- **Steps:**
  1. `C.phone online`, `C.phone leaves House`.
  2. `J.mac syncs`.
  3. `A.phone syncs`.
- **Expect:**
  - `_roster` holds `member:C` `status: left`, signed by C.phone. C's home has `membership:House.state = left`, and C.phone no longer fetches channel 3.
  - `G(4)` by a J device, `to` = {J, A}. C's inbox receives no epoch-4 grant. C.phone holds `K(3)` only.
  - `c1`, `c2` are in channel 4 (named in J.mac's `_carried`) if and only if J.mac merged them before it merged C's `left` write.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-NO-LOSS
- **Notes:** GAP: §11.5 doesn't say whether a leaving device first publishes its queued writes and then the `left` write, or drops them. The outcome for `c1`/`c2` depends on that order, and on whether the app is told they were dropped.

### RO-003 Phone lost: every shared list re-keys
- **Kind:** real-world
- **Design:** §6.6, §6.3, §8.1 (`fence`), §8.3, §11.6, §13.2
- **Binding:** any
- **Setup:** J owns `House` (with A) and `Groceries` (with A, B). B owns `Trip` with J as writer. J.phone was lost at 12:00 and is now `T(J.phone)`. At 13:00 `T(J.phone)` adds item `x` to `House` and to `Trip`; J.mac, A.phone and B.phone merge both.
- **Steps:**
  1. At 14:00, `J.mac revokes J.phone lost since 12:00`.
  2. `J.mac syncs`.
  3. `A.phone syncs`, `B.phone syncs`, `J.ipad syncs`.
- **Expect:**
  - J's chain holds `revoke:J.phone`. J's `_puk` holds a manifest of generation `n+1` building on it, with boxes for J.mac, J.ipad and the recovery key only.
  - `House` and `Groceries`: `fence:J.phone` with `rev = wid(revoke)`, then `G(e+1)` by J.mac. In each, J's `to` entry carries `kid(J, n+1)`.
  - `Trip`: B.phone writes `fence:J.phone` and `G(e+1)` after seeing the revocation in J's profile. J's `to` entry carries `kid(J, n+1)`.
  - `T(J.phone)` holds no key of the new epoch of any of the three collections, and cannot open any grant sent after step 1.
  - `House`: `x` is not in the new epoch on any device (J.mac received it after 12:00).
  - J's UI shows nothing beyond the device leaving the list under Devices.
- **Invariants:** INV-CUT, INV-AUTHZ, INV-SIGNED, INV-MONOTONE-EPOCH
- **Notes:** GAP: in `Trip`, B.phone does the rotation but has no access to "lost since 12:00". The `revoke` entry's `at` is "for display only" (§6.2), and the chain revocation's `prev` records only which chain writes J.mac accepted, not which collection writes. So B.phone carries `x` into `Trip`'s new epoch, while J.mac leaves it out of `House`. The §8.3 snapshot rule "for a lost device, before the time the user gave" can only be applied in collections where the revoking user's own device rotates.

### RO-004 Offline through a rotation, back three weeks later
- **Kind:** real-world
- **Design:** §11.6 (every other device, late writes), §8.3, §9.2, §12.2
- **Binding:** any
- **Setup:** `House` epoch 3: J owner, A writer, B writer. A has A.phone and A.mac. Item `k` has `title = "Paint"`.
- **Steps:**
  1. `A.mac offline`. A.mac adds `y1` and sets `k.title = "Paint the shed"` (both stamped epoch 3).
  2. `wait 1 day`. `J.mac removes B`; `J.mac syncs` (rotation to 4). `A.phone syncs`.
  3. `wait 4 days`. `J.phone sets k.title = "Paint (white)"`.
  4. `wait 16 days`. `A.mac online`, `A.mac syncs`.
- **Expect:**
  - A.mac fetches channel 3 and sees `G(4)` before it publishes anything. Nothing from A.mac appears in channel 3.
  - A.mac takes `K(4)` from A's home (A.phone already consumed the grant) and switches without a grant of its own.
  - `y1` and A.mac's `k.title` write appear in channel 4 and are named in a `_carried` write signed by A.mac, stamped 4.
  - On every device `k.title` has two kept writes: J.phone's (epoch 4), which is displayed, and A.mac's (epoch 3), reported to the app as a sibling. A.mac's later HLC doesn't change which is displayed.
  - A.mac keeps its channel-3 replica until 30 days after its own switch.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE, INV-MONOTONE-EPOCH
- **Notes:** GAP: §11.6 says a device holding a grant "publishes what it queued", and only for a device returning after 30 days does it say it "forwards its own unsynced writes" via `_carried`. Writes made offline before the device saw the genesis are stamped 3, so within the 30 days they also need a `_carried` list from the device. Nothing says so, or says that they are re-authored as epoch-4 writes instead. For a writer demoted to reader in the meantime (RO-036), the two readings give different results.

### RO-005 Make a list private again
- **Kind:** real-world
- **Design:** §7.4, §8.2, §11.6, §17
- **Binding:** any
- **Setup:** `Groceries` epoch 3: J owner, A writer, B reader.
- **Steps:**
  1. `J.phone removes A`, then within one second `J.phone removes B`.
  2. `J.phone syncs`. `A.phone syncs`. `B.phone syncs`.
- **Expect:**
  - The final epoch's genesis has `to = [[J, kid(J, current)]]`, and its pinned roster has exactly one active member, J.
  - Either one rotation (both removals in `G(4)`'s past) or two (`G(4)` then `G(5)`). Either way A and B hold no key of the final epoch.
  - A.phone and B.phone show "Groceries is no longer shared with you" and delete their replicas.
  - J's devices show `Groceries` with no other members; records are unchanged apart from A's and B's writes that J.phone merged after the removals.
- **Invariants:** INV-CUT, INV-ONE-OWNER, INV-MONOTONE-EPOCH

### RO-006 Owner asks for a rotation
- **Kind:** real-world
- **Design:** §7.2, §11.6, §8.3, §5.5
- **Binding:** any
- **Setup:** `House` epoch 3: J owner, A writer. 200 live records, no tombstones older than 180 days.
- **Steps:**
  1. `J.mac rotates House`.
  2. All devices sync.
  3. `wait 31 days`. `J.mac syncs`.
- **Expect:**
  - `G(4)` by J.mac with `to` = {J, A}. The roster is unchanged.
  - The visible state of all 200 records is identical on every device before and after.
  - Each record in channel 4 has one `_carried` write by J.mac naming exactly its kept epoch-3 writes (32 bytes per write). Every carried write keeps its original signature and `ep: 3`.
  - After step 3, channel 3 holds only `_epoch` record messages.
- **Invariants:** INV-SIGNED, INV-QUIESCE, INV-GC-SAFE

---

## Edge

### RO-007 Removed and re-added within one epoch
- **Kind:** edge
- **Design:** §8.2 (the pin), §11.6 (when a rotation is due)
- **Binding:** any
- **Setup:** `House` epoch 3: J owner, A writer, B writer. J.phone offline.
- **Steps:**
  1. `J.phone removes B`, then `J.phone sets role B = writer`. The second write builds on the first.
  2. `J.phone online`, `J.phone syncs`.
- **Expect:**
  - The roster shows B active writer on every device.
  - Epoch after step 2: one of 3 (§11.6) or 4 (§8.2). The harness can't assert either until the GAP is settled.
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER
- **Notes:** GAP: §8.2 says "Every cut makes a rotation due, so backdating of any kind is bounded by the stretch since the last one". §11.6 makes a rotation due only by comparing the current roster with "what that epoch's genesis pinned". A removal followed by a re-add, or a demotion followed by a re-promotion, is a cut with no difference at the comparison, so by §11.6 no rotation is due, and the bound claimed in §8.2 doesn't hold.

### RO-008 Removed and re-invited while the rotation is under way
- **Kind:** edge
- **Design:** §11.6, §8.2 (re-issue), §11.2, §11.3, §11.4
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A writer, B writer.
- **Steps:**
  1. `J.mac removes B`; `J.mac syncs` (writes `G(4)` pinning B removed and sends grants to J and A).
  2. `hub: withhold G(4) from J.phone`.
  3. `J.phone invites qr to House as writer`; `B.phone joins via` it; J.phone auto-approves (writes `member:B` active with `via`, builds on the removal, concurrent with `G(4)`; sends B a grant for epoch 3).
  4. Withholding ends. All devices sync.
- **Expect:**
  - B.phone sees `G(4)` in channel 3, which doesn't pin B as active, so it deletes its replica and shows "House is no longer shared with you".
  - J.phone switches to 4 and re-issues `member:B` active on top of `G(4)`.
  - Intended end state: B active writer in epoch 4 and B.phone holds `K(4)`. The harness checks whether B.phone ever gets `K(4)`.
- **Invariants:** INV-CONVERGE, INV-NO-HOLD-FOREVER, INV-UX
- **Notes:** GAP: nothing sends B the epoch-4 key. The rotator's grants go only to `to`. §8.2 has the admission re-issued but doesn't say a grant goes with it. B.phone can't read channel 4, so it never sees "its user as an active member of an epoch whose key it doesn't have" (§11.4) and never sends a key request. GAP: a device can't tell "pinned as removed" from "admitted concurrently with the genesis": §11.6 treats any device not active in the pin as removed, so B.phone shows a removal message although B has just been let back in.

### RO-009 Join approved concurrently with a genesis; the joiner's writes are held
- **Kind:** edge
- **Design:** §8.3 (held writes), §11.6 (snapshot, late writes), §11.3, §10.7
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A admin, B writer. C has asked to join by link.
- **Steps:**
  1. `hub: withhold <A's approval of C> from J.mac`.
  2. `A.phone approves C` (roster write plus grant for epoch 3). C.phone adds `c1`, `c2` to `House`.
  3. `J.mac syncs`: it receives `c1`, `c2` and holds them (author not admitted as far as it knows). `J.mac removes B`; `J.mac syncs` (`G(4)`, `to` = {J, A}).
  4. Withholding ends. All devices sync.
- **Expect:**
  - A.phone re-issues `member:C` on top of `G(4)`. C is write-enabled in 3 (the original admission is valid on its own past) and in 4 (the re-issue).
  - Intended: `c1`, `c2` are valid in epoch 4 on every device. The harness checks whether any `_carried` write names them.
  - C.phone: same outcome as B.phone in RO-008 (it treats itself as removed).
- **Invariants:** INV-NO-LOSS, INV-NO-HOLD-FOREVER, INV-CONVERGE
- **Notes:** GAP: the snapshot names only writes in the rotator's state, and §11.6 forwards only writes that "arrive" in the old channel after a device has switched. `c1` and `c2` arrived before and were held, so neither path carries them once the admission turns up. They stay held in channel 3 until it is destroyed, then disappear. Under §10.7, held writes also count when checking dominance, so they keep channel-3 messages alive until then.

### RO-010 A departure concurrent with a genesis
- **Kind:** edge
- **Design:** §11.5, §8.2 (the pin, re-issue), §11.6, §11.4
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A writer, B writer, C writer.
- **Steps:**
  1. `J.mac removes B`; `hub: withhold <C's next write> from J.mac`.
  2. `C.phone leaves House`. `J.mac syncs` (`G(4)` built without C's `left`).
  3. Withholding ends. All devices sync.
  4. `wait 60 days`. `J.mac removes A`; `J.mac syncs` (`G(5)`).
- **Expect:**
  - `G(4).to` includes C, and C's inbox receives an epoch-4 grant. C.phone rejects it (home state `left`) and destroys it.
  - Intended: after all have synced, C isn't active in epoch 4's roster and isn't in `G(5).to`. The harness checks both.
- **Invariants:** INV-CUT, INV-NO-HOLD-FOREVER
- **Notes:** GAP: C's `left` write is left behind by the pin. Only its author may make it again (§8.2: `member:X` by X), but C.phone has stopped following and holds no `K(4)`, so it can't re-issue it. As written, C stays an active member in epoch 4 and every later epoch. Every future genesis names C in `to` and sends C a key: a modified client that ignores its own `left` state reads every later epoch. "Someone holds the epoch key who shouldn't" never fires, because the roster says C is active.

### RO-011 Rotation during another rotation
- **Kind:** edge
- **Design:** §11.6, §8.2 (the pin, re-issue), §8.3 (snapshot)
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A, B, C writers. 5,000 records, so the snapshot takes a while.
- **Steps:**
  1. `J.mac removes B`; J.mac writes `G(4)` and starts publishing the snapshot.
  2. `hub: withhold G(4) from J.phone`. `J.phone removes C` (written in epoch 3's log, concurrent with `G(4)`).
  3. C.phone adds `c1`.
  4. Withholding ends. All devices sync.
- **Expect:**
  - `G(4).to` = {J, A, C}. C.phone switches to 4 and holds `K(4)`.
  - J.phone re-issues the removal of C on top of `G(4)`. A J device then writes `G(5)` with `to` = {J, A}.
  - Final epoch 5. C holds `K(3)`, `K(4)`, not `K(5)`; C.phone deletes its replica on seeing `G(5)`. B holds `K(3)` only.
  - `c1` (and any epoch-4 write by C) is in epoch 5 only if the epoch-5 rotator merged it before it merged either copy of C's removal.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-AUTHZ
- **Notes:** When a cut is re-issued, §8.3's "before it merged the cut" could mean the original or the re-issue. The Expect above uses whichever the rotator merged first; the text should say so.

### RO-012 Two admins rotate concurrently while a writer keeps writing
- **Kind:** edge
- **Design:** §11.6 (competing rotations), §11.4, §8.3, §9.2
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A admin (A.mac), B writer (B.phone), C writer.
- **Steps:**
  1. `J.mac removes C`. `A.mac syncs`.
  2. `hub: withhold` each one's genesis from the other. J.mac writes `G(4)/J`, A.mac writes `G(4)/A`; each sends grants to J, A and B. Let `G(4)/A` have the lower write ID.
  3. `hub: withhold G(4)/A and its grants from B.phone`. `B.phone syncs` (switches to `G(4)/J`), then adds `b1` and sets `k.title = "x"`.
  4. Withholding ends. All devices sync.
- **Expect:**
  - Every device ends on `G(4)/A` and holds its key. Home `membership:House.key` is A's key on J, A and B.
  - `b1` and `k.title = "x"` are in `G(4)/A`'s channel, author B, `ep: 4`, and displayed on every device.
  - Once J.mac has synced, `G(4)/J`'s channel holds no messages.
  - Neither genesis names C; C holds `K(3)` only.
  - Log writes that `G(4)/A` doesn't build on, such as J.mac's own epoch-4 writes, are re-issued by J.mac on top of `G(4)/A`.
- **Invariants:** INV-CONVERGE, INV-MONOTONE-EPOCH, INV-NO-LOSS, INV-CUT
- **Notes:** GAP: epochs are identified by number in `sigInput` and in §8.3 (`u32(epoch)`), and siblings share a number. So B's writes stamped 4 in the losing channel have `a = e` in the winner and need no `_carried` list, and anyone holding both keys, readers included, can move them across. And "write-enabled in epoch `a`" doesn't say which sibling's genesis supplies the pinned roster. §8.3's "held until the admission is re-issued" implies the winner's, but the rule should say so.

### RO-013 Writer demoted to reader mid-epoch
- **Kind:** edge
- **Design:** §8.3, §11.6 (snapshot, late writes), §3.3
- **Binding:** any
- **Setup:** `Groceries` epoch 3: J owner (J.phone), A writer, B writer.
- **Steps:**
  1. `J.phone sets role B = reader`.
  2. Before seeing it, `B.phone adds x1`. `A.phone syncs` (merges `x1`). `J.phone syncs` (merges `x1` after the demotion), then rotates.
  3. `hub: withhold G(4) from B.phone`. `B.phone adds x2` (published to channel 3 after `G(4)`).
  4. `A.phone syncs` (switched to 4; sees `x2` arrive in channel 3). Withholding ends; all sync.
- **Expect:**
  - During epoch 3, `x1` is valid and shown on A.phone with author B.
  - `G(4).to` includes B (still an active member); B.phone switches to 4 and its UI no longer offers editing.
  - Neither `x1` nor `x2` is valid in epoch 4 on any device. A.phone writes no `_carried` naming `x2`. B's writes that J.phone merged before step 1 are carried.
  - Any epoch-4 write signed by B.phone is invalid everywhere.
- **Invariants:** INV-AUTHZ, INV-CONVERGE
- **Notes:** GAP: same as RO-024. B was write-enabled in epoch 3, so under §8.3 a forward of `x2` by A.phone is valid, and the §11.6 test ("only writes whose author was entitled to make them") doesn't clearly exclude it, though §3.3 and §8.3 ("not after it") say it shouldn't be forwarded. Also: A.phone saw `x1` in epoch 3 and loses it at the switch, without being told (see RO-034).

### RO-014 A member admitted by an admin whose demotion was under way
- **Kind:** edge
- **Design:** §8.2 (cuts), §8.3 (write-enabled), §11.6, §11.4
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A admin (A.phone). C has asked to join by link.
- **Steps:**
  1. `J.mac sets role A = writer`. `hub: withhold` it from A.phone.
  2. `A.phone approves C` (concurrent with the demotion). C.phone adds `c1`.
  3. Withholding ends. All devices sync; J.mac rotates (A's demotion is due).
- **Expect:**
  - On every device holding both writes, `member:C` doesn't count; C is not in the member list.
  - `c1` is valid in epoch 3 (C write-enabled through an admission valid on its own past) and shown with author C.
  - `G(4).to` = {J, A}. `c1` is carried (C isn't a cut author) and stays visible in epoch 4, author C.
  - C.phone, on seeing `G(4)`, treats itself as removed and shows "House is no longer shared with you".
  - No device re-issues C's admission (A may no longer make it).
- **Invariants:** INV-AUTHZ, INV-CONVERGE, INV-UX
- **Notes:** GAP: the design allows this ("writes validly until the rotation that follows", §8.3) but says nothing about what anyone sees. J's member list never shows C while C's items appear under C's name. C is told the list is no longer shared although nobody removed or declined them. Nobody is asked to approve C again. Whether C.phone accepts the epoch-3 grant at all depends on whether it saw the demotion before checking the log (§11.4).

### RO-015 A grant waits for the member's new PUK manifest
- **Kind:** edge
- **Design:** §11.6 step 4, §6.3, §6.6, §11.4 (key request)
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A writer (A.phone, A.mac). A.phone stolen: `T(A.phone)`.
- **Steps:**
  1. `A.mac revokes A.phone`; `A.mac crashes after` writing the revocation, before the PUK manifest.
  2. `J.mac syncs` (sees the revocation in A's profile, fences, rotates). `J.mac offline`.
  3. `T(A.phone)` sends a key request for epoch 4.
  4. `wait 1 hour`. `A.mac launches` (writes manifest `n+1`), then sends a key request after 10 minutes.
  5. `J.phone syncs`.
- **Expect:**
  - `G(4).to` has A with `null` key ID. No grant sealed to `kid(A, n)` for epoch 4 exists on the hub.
  - J.phone refuses `T(A.phone)`'s request (device fenced in epoch 4's log).
  - J.phone answers A.mac with a grant sealed to `kid(A, n+1)`. A.mac holds `K(4)`; `T(A.phone)` never does.
- **Invariants:** INV-CUT, INV-NO-HOLD-FOREVER
- **Notes:** GAP: §11.4's key-request handling checks "an active member with a chain-valid, unfenced device" but doesn't restate the manifest gate of §11.6 step 4. A responder that hasn't fetched A's profile since the revocation would seal to `kid(A, n)`, which `T(A.phone)` can open, in reply to any request from A's user, including a replayed one. Under §6.3 there is no "current manifest" while none builds on the revocation; the responder needs to be told to wait rather than use the last one it saw. GAP: "the grant follows when the manifest appears" names no sender; if J.mac never returns, the key-request path is the only one.

### RO-016 Rotator seals to a member before seeing that member's revocation
- **Kind:** edge
- **Design:** §11.6, §6.6, §8.3 (usable devices), §3.3
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A writer. A.phone stolen: `T(A.phone)`.
- **Steps:**
  1. `A.mac revokes A.phone` (PUK `n+1`). `hub: withhold A's profile from J.mac`.
  2. `J.mac removes B`; `J.mac syncs` (`G(4)`, grant to A sealed to `kid(A, n)`).
  3. `T(A.phone)` opens the grant, reads channel 4 and adds `t1` there.
  4. Withholding ends. `J.mac syncs`.
- **Expect:**
  - `T(A.phone)` holds `K(4)`. `t1` is valid in epoch 4 (A.phone usable in 4: no fence in `G(4)`'s past).
  - J.mac writes `fence:A.phone` in epoch 4 and `G(5)`; A's `to` entry has `kid(A, n+1)`.
  - `T(A.phone)` doesn't hold `K(5)`. `t1` is in epoch 5 only if J.mac merged it before the fence's revocation.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH
- **Notes:** Matches §6.6: the window lasts until the next rotation.

### RO-017 The 30-day boundary
- **Kind:** edge
- **Design:** §11.6 (late writes), §12.3, §9.4 (holding), §12.2
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.ipad), A writer (A.phone, A.mac). J.ipad and A.mac offline, each with one unsynced edit (`j1`, `a1`).
- **Steps:**
  1. `J.mac removes B`; rotation to 4 on day 0. A.phone switches.
  2. Day 29: `J.ipad online`, `J.ipad syncs`.
  3. Day 31: `J.mac syncs` (destroys channel 3's messages except `_epoch`).
  4. Day 40: `A.mac online`, `A.mac syncs`.
- **Expect:**
  - Day 29: J.ipad sees `G(4)` in channel 3, switches through J's home, publishes `j1` to channel 4 with its own `_carried`.
  - Day 31: channel 3 holds only `_epoch` messages.
  - Day 40: A.mac publishes nothing to channel 3, switches to 4 through A's home, and publishes `a1` with a `_carried` signed by A.mac.
- **Invariants:** INV-NO-LOSS, INV-GC-SAFE, INV-MONOTONE-EPOCH
- **Notes:** GAP: the `_epoch` record kept "as the pointer to the new epoch" is a log record, and `G(4)` builds on `_roster` writes made while A.mac was away (B's removal at least). Those are destroyed on day 31. By §9.4 A.mac holds `G(4)` without applying it until it has the write's whole past, so it can't treat it as a valid genesis (§11.6, §12.2). It's rescued here only because A's home names epoch 4. A user whose only device returns after day 30, and whose grant was lost, has an unverifiable pointer. The retained `_epoch` needs its past retained too, or a rule for using a genesis whose past is gone.

### RO-018 Maintenance rotation compacts old tombstones
- **Kind:** edge
- **Design:** §12.3, §11.6 step 3, §9.2 (delete)
- **Binding:** any
- **Setup:** `Groceries` epoch 3 has lasted 210 days: 300 live records, 1,200 records whose tombstones J.mac merged 190 to 205 days ago, 50 deleted 10 days ago. J.mac's threshold is the default 1,000. J.ipad was paired 20 days ago.
- **Steps:**
  1. `J.mac syncs` (maintenance rotation).
  2. All devices sync.
  3. `hub: replay` an epoch-3 message of a compacted record into channel 3.
- **Expect:**
  - `G(4)` by J.mac; `to` unchanged.
  - Channel 4 holds no message for any of the 1,200 records; the 50 recent tombstones and the 300 live records are carried.
  - After step 3, no device writes a `_carried` naming the replayed writes, and none of the 1,200 records exists on any device.
  - J.ipad never starts a maintenance rotation itself (it merged every tombstone 20 days ago).
- **Invariants:** INV-CONVERGE, INV-GC-SAFE, INV-QUIESCE

### RO-019 Stale device across two rotations
- **Kind:** edge
- **Design:** §12.3 (stale), §11.4, §11.6, §12.2, §13.3
- **Binding:** any
- **Setup:** `House` epoch 3: J owner, C writer with only C.phone.
- **Steps:**
  1. `C.phone offline`; C.phone adds `z1`.
  2. Day 10: rotation to 4. Day 100: rotation to 5. Day 190: J.mac destroys channel 3's `_epoch` (past the horizon).
  3. Day 200: `C.phone online`, `C.phone syncs`.
- **Expect:**
  - C.phone shows "This device was offline for a long time. Reloading from the hub; nothing you wrote here is lost."
  - C.phone accepts the epoch-5 grant from its inbox, destroys the epoch-4 grant as failed, and home `key.n = 5`.
  - C.phone writes nothing to channels 3, 4 or 5 on its own account. `z1` is handed to the app as rescued data, not forwarded.
- **Invariants:** INV-NO-LOSS, INV-UX, INV-MONOTONE-EPOCH
- **Notes:** GAP: if the grants are gone (destroyed by E, or consumed by a C device since revoked), C.phone finds channel 3 empty, no `_epoch` pointer, and nothing in home or inbox. It isn't allowed to heal, it doesn't know an epoch exists for which to send a key request, and owner devices only react to messages under superseded tags, which a stale device doesn't publish. The design doesn't say what it shows or does then.

---

## Fault

### RO-020 Rotation author crashes after the genesis
- **Kind:** fault
- **Design:** §11.6 (interrupted rotations), §11.4 (key request)
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A writer, B writer.
- **Steps:**
  1. `J.mac removes B`; `J.mac crashes after` appending `G(4)/1` to channel 3, before step 3 of §11.6.
  2. `J.phone syncs`, `A.phone syncs`. `A.phone adds a1`.
  3. `wait 10 min`; `wait 10 min`. `J.phone syncs`.
  4. `J.mac launches`.
- **Expect:**
  - From step 2, neither device publishes to channel 3; `a1` stays local.
  - At 10 minutes both send key requests to J's inbox; nobody answers.
  - At 20 minutes J.phone writes `G(4)/2` with `replaces: wid(G(4)/1)`, `to` = {J, A}, and grants. A.phone switches and publishes `a1`.
  - J.mac on launch abandons `G(4)/1` and follows `G(4)/2`. The `G(4)/1` channel holds no messages.
- **Invariants:** INV-NO-HOLD-FOREVER, INV-MONOTONE-EPOCH, INV-CUT
- **Notes:** GAP: `replaces` is missing from the genesis format and its validity conditions in §11.6 step 2 and §8.2. Nothing says how it ranks against "lowest write ID wins", or whether §11.4's "wins over the current one" includes it. GAP: §11.6 says an interrupted rotation "is picked up by the next such device" because "due" is a property of the log, but the same paragraph forbids starting one once a valid genesis for the next epoch exists. After a genesis, `replaces` is the only way forward. GAP: if J.mac launches before anyone replaces `G(4)`, nothing says whether it resumes, or whether it must have persisted `K(4)` before publishing the genesis. With one owner device and no admins (Liszto's usual case), nobody else can replace it, so A waits until J.mac returns. GAP: if the author crashed after sending some of the grants, some devices already follow `G(4)/1`, and nothing says who forwards their writes to `G(4)/2`.

### RO-021 Crash after writing the fence, before the genesis
- **Kind:** fault
- **Design:** §11.6 (when due, step 1), §8.3 (usable), §8.2 (`fence`)
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A writer (A.mac, A.phone). A.phone stolen: `T(A.phone)`.
- **Steps:**
  1. `A.mac revokes A.phone`.
  2. `J.mac syncs`; `J.mac crashes after` writing `fence:A.phone` in epoch 3's log.
  3. `J.phone syncs`.
- **Expect:**
  - J.phone finds a rotation due (`fence:A.phone` isn't in `G(3)`'s past) and writes `G(4)`. `T(A.phone)` holds no `K(4)`.
- **Invariants:** INV-CUT, INV-NO-HOLD-FOREVER
- **Notes:** GAP: §11.6 makes a rotation due when a revocation "isn't yet fenced in this epoch". Read literally, the fence J.mac wrote in step 2 is in this epoch, so nothing is due and `T(A.phone)` keeps reading epoch 3 indefinitely. §8.3's "usable" test (no fence in the past of the epoch's genesis) gives the intended answer; §11.6 should use it. The literal reading also lets A.mac stall rotation in a collection where A isn't an admin: §8.2 allows the device's own user to write the fence, which then makes nothing due.

### RO-022 A losing sibling's channel is destroyed under a device still on it
- **Kind:** fault
- **Design:** §11.6 (competing rotations), §12.2 (healing), §11.4
- **Binding:** any
- **Setup:** As RO-012 after step 2: `G(4)/A` (winner) and `G(4)/J` (loser). B.phone holds only the `G(4)/J` grant because `hub: withhold grants for B` from `G(4)/A`.
- **Steps:**
  1. `B.phone syncs` (follows `G(4)/J`).
  2. `J.mac syncs` (follows the winner, destroys `G(4)/J`'s channel).
  3. `B.phone syncs`.
  4. `wait 10 min`; `B.phone syncs`.
- **Expect:**
  - B.phone doesn't re-publish anything to `G(4)/J`'s channel after step 2.
  - B.phone sends a key request naming epoch 4 and receives a grant for `G(4)/A`.
  - `G(4)/J`'s channel holds no messages after all have synced.
- **Invariants:** INV-QUIESCE, INV-MONOTONE-EPOCH, INV-NO-LOSS
- **Notes:** GAP: §12.2 lets a device heal a channel unless its `_epoch` shows a valid successor or home or inbox names a newer epoch. A winning sibling is neither, so B.phone re-uploads the losing channel J.mac just emptied, and the two can alternate. The healing check needs to cover "a competing genesis that wins". Also, §11.6 doesn't say when the loser's author may destroy its channel relative to other devices forwarding out of it, and a key request names only `u32(epoch)`, not which sibling.

### RO-023 Rotating device's clock is a year ahead
- **Kind:** fault
- **Design:** §12.3 (horizon, stale), §11.6 (late writes), §9.2
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A writer (A.phone, A.mac).
- **Steps:**
  1. `A.mac offline`; A.mac sets `r.title = "x"` on record `r`.
  2. `A.phone deletes r`. `J.mac syncs` (merges the tombstone).
  3. `J.mac clock = +1 year`. `J.mac removes B`; `J.mac syncs` (rotation to 4).
  4. Day 2: `A.mac online`, `A.mac syncs`.
- **Expect:**
  - Intended (§12.3: "a deleted record can't be brought back"): `r` doesn't exist on any device after step 4.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** GAP: the horizon is measured as "merged more than 180 days ago" on the rotating device's own clock, so J.mac leaves `r` out of channel 4 three days after the delete. A.mac merges the tombstone into its old replica, then forwards its own `r.title` write. Whether that "changes that old replica" is undefined (see RO-027). If it does, `r` reappears in epoch 4 with only a title. The design ignores timestamps for authorization (§6.2) but trusts one local clock here. GAP: by the same clock J.mac's last full sync of every channel is now "a year ago", so §12.3 makes it stale: it must not heal or forward and must resync from scratch. A clock jump alone triggers that.

---

## Adversarial

### RO-024 A removed member backdates writes into the old epoch
- **Kind:** adversarial
- **Design:** §8.3 (carried writes), §11.6 (late writes), §3.3
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A writer (A.phone), M writer.
- **Steps:**
  1. `J.mac removes M`; `J.mac syncs` (rotation to 4, `to` = {J, A}).
  2. `M.phone signs` epoch-3 writes: a new item `m1`, and `k.title = "spam"` with an HLC from before step 1 and a context covering everything in epoch 3. M appends them to channel 3.
  3. `A.phone syncs`, `J.mac syncs`.
- **Expect:**
  - Neither write exists in epoch 4 on any device. No `_carried` write names them. `k.title` is unchanged everywhere.
  - Both are visible in A.phone's and J.mac's channel-3 replicas only.
- **Invariants:** INV-AUTHZ, INV-CUT, INV-CONVERGE
- **Notes:** GAP: §3.3 says a removed member's old-epoch writes "aren't forwarded", but the rules don't produce that. A forward is valid if the forwarder is write-enabled in 4 and the named write passes §8.3 rules 1–2, and M was write-enabled in epoch 3. §11.6's honest-forwarder test, "only writes whose author was entitled to make them", is met by M under §8.3. Both A.phone and J.mac would forward. The forwarder needs an explicit test, such as "not by an author reduced, or a device fenced, in the past of the genesis I switched to". Even then, any writer's `_carried` naming M's write is accepted by every validator, so the protection rests on each forwarder's local choice.

### RO-025 A reader re-encrypts archived writes into the new epoch
- **Kind:** adversarial
- **Design:** §8.3 (carried writes), §12.3, §10.7
- **Binding:** any
- **Setup:** `House` epoch 3: J owner, A writer, C reader. Record `s` was deleted 200 days ago and is compacted at the rotation to 4. C's client is modified (C acts as M would, within a reader's keys).
- **Steps:**
  1. Maintenance rotation to 4 by J.mac.
  2. C.phone re-encrypts `s`'s original epoch-3 create writes (by A, valid signatures) into channel 4, with no `_carried`.
  3. C.phone also publishes a `_carried` write naming them, signed by C.phone, stamped 4.
  4. All devices sync.
- **Expect:**
  - `s` doesn't exist in epoch 4 on any device.
  - C's `_carried` write is invalid (C isn't write-enabled in 4) on every device.
- **Invariants:** INV-AUTHZ, INV-CONVERGE, INV-NO-HOLD-FOREVER, INV-GC-SAFE
- **Notes:** GAP: §8.3 doesn't say whether an earlier-epoch write with no list naming it is held ("not yet valid": a forwarder's `_carried` could still arrive, perhaps in another message, §10.6) or dropped. If held, it's held forever. Under §10.7 held writes count for dominance, so C's message can never be garbage-collected, and C can make channel 4 grow without bound with valid-but-unvouched writes.

### RO-026 A writer overwrites a `_carried` list
- **Kind:** adversarial
- **Design:** §8.3 (`_carried`), §9.2, §9.3
- **Binding:** any
- **Setup:** `House` epoch 4 after a rotation by J.mac. Record `r`: `title` write `t0` (epoch 3, by A) named in J.mac's snapshot list `S`. M is a writer. Devices P = A.phone and Q = J.ipad.
- **Steps:**
  1. A late epoch-3 write `t1` (by A.mac, context covering `t0`) arrives in channel 3. J.phone forwards it with `_carried` `L = S + [t1]`, context covering `S`.
  2. `M.phone signs` `_carried` `M1 = [t0]` with context covering `L` (stamped 4).
  3. P receives `S, t0, L, t1, M1` in that order. Q receives `S, t0, M1, L, t1`.
- **Expect:**
  - Intended (§8.3: "nothing merged in an epoch can turn invalid later in that epoch"): `t1` stays valid; `r.title` shows `t1` on P and Q.
- **Invariants:** INV-CONVERGE, INV-AUTHZ
- **Notes:** GAP: `_carried` is a register in which "every kept value applies", so a writer can supersede a list. After step 3, P has already dropped `t0` (superseded by `t1`); `M1` then un-vouches `t1`, and P shows no title. Q never kept `L`, treats `t1` as unvouched, and shows `t0`. That's divergence (INV-CONVERGE fails), and a write merged in epoch 4 turning invalid inside epoch 4. `M1 = []` would just wipe carried content, which goes beyond the field-level vandalism §1.2 accepts. A vouch probably needs to apply once made (a grow-only set), not as a register value.

### RO-027 A late write to a record compacted at the rotation
- **Kind:** adversarial
- **Design:** §11.6 (late writes), §12.3
- **Binding:** any
- **Setup:** `Groceries` epoch 3: J owner, A writer, M writer. Record `r` deleted 200 days ago.
- **Steps:**
  1. Maintenance rotation to 4 by J.mac; `r` left out.
  2. `M.phone signs` an epoch-3 write `r.title = "back"` with a fresh dot and appends it to channel 3.
  3. `A.phone syncs`.
- **Expect:**
  - Per §12.3 ("late writes are forwarded only if they change the old replica, where the record is deleted"): A.phone doesn't forward; `r` doesn't exist in epoch 4.
- **Invariants:** INV-CONVERGE
- **Notes:** GAP: "changes that old replica" is undefined. By §9.3 a record's state is its set of kept writes, and a new write to a deleted record adds one, so its state changes even though it stays deleted. If that counts, A.phone forwards it and `r` returns in epoch 4 with only `title` (no type field, see RO-034). §12.3's "they could as well make a new one" covers M, but not the honest case in RO-023. The rule should say a write to a record deleted in the old replica is never forwarded.

### RO-028 Eve destroys grants and key requests
- **Kind:** adversarial
- **Design:** §11.4, §11.6, §3.3, §13.3
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A writer (A.phone). E holds the hub credential.
- **Steps:**
  1. `J.mac removes B`; `J.mac syncs`.
  2. `hub: destroy grants for A`.
  3. `wait 10 min`; `A.phone syncs`. `J.mac syncs`.
  4. Repeat with E also destroying every key request in J's inbox for 2 hours.
- **Expect:**
  - Step 3: A.phone sends a key request to J; J.mac answers with an epoch-4 grant; A.phone switches and publishes what it queued.
  - Step 4: A.phone holds no `K(4)` while E keeps destroying; it publishes nothing to channel 3; its edits stay local; once E stops, A.phone switches within one request cycle.
- **Invariants:** INV-CUT, INV-NO-LOSS, INV-UX
- **Notes:** GAP: §11.4 says where the first key request goes and to whom next, but not whether or how often a device retries after both go unanswered. Without a retry rule, step 4 never recovers. The user-facing side is in RO-035.

### RO-029 Eve destroys the old `_epoch` record and an offline device's grant
- **Kind:** adversarial
- **Design:** §12.2 (the remaining gap), §11.6, §3.3, INV-CUT
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), A writer with only A.phone, B writer. B is E: he keeps the hub credential and `K(3)` after removal.
- **Steps:**
  1. `A.phone offline`; A adds `a1`, `a2`.
  2. `J.mac removes B`; `J.mac syncs` (rotation to 4).
  3. `hub: destroy` channel 3's `_epoch` messages and `grants for A`.
  4. `A.phone online`, `A.phone syncs`.
  5. `J.mac syncs`. `A.phone syncs`.
- **Expect:**
  - Step 4: A.phone treats epoch 3 as current, publishes `a1`, `a2` and re-uploads channel 3 (as §12.2 admits).
  - Step 5: J.mac sees new messages under channel 3's tag, re-posts `G(4)` there and a grant to A. A.phone switches, forwards `a1`, `a2` into 4 with its own `_carried`, and stops publishing to 3.
  - B can decrypt `a1` and `a2`. The harness records this as an INV-CUT exception, not a pass.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH (INV-CUT is expected to fail; see Notes)
- **Notes:** GAP: §12.2 presents this gap as an availability problem ("it will re-upload there"), but the re-upload and A's new writes go into a channel the removed member can read, so it is also a confidentiality failure against exactly the party the rotation was for. GAP: §12.2 doesn't say how long owner and admin devices keep watching tags of superseded epochs. After 30 days they stop watching the old channel (§11.6), so a device that comes back on day 31 isn't noticed.

### RO-030 A removed admin races a genesis
- **Kind:** adversarial
- **Design:** §11.6 (competing rotations), §8.2 (cuts), §11.4
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), M admin, A writer.
- **Steps:**
  1. `J.mac removes M`. `hub: withhold` the removal from A.phone.
  2. `M.phone signs` `G(4)/M`, not building on the removal, `to` = {J, M, A}, with a write ID lower than any genesis J.mac will write, and sends grants.
  3. `A.phone syncs` (switches to `G(4)/M`), `A.phone adds a1`.
  4. `J.mac syncs` (writes `G(4)/J`, `to` = {J, A}). Withholding ends. All devices sync.
- **Expect:**
  - On every device that holds the removal, `G(4)/M` doesn't count, whatever its write ID.
  - All honest devices end on `G(4)/J`. M holds `K(3)` and `K(4)/M`, not `K(4)/J`.
  - `a1` is in `G(4)/J`'s channel. M could decrypt `a1` from `G(4)/M`'s channel; the harness records that as the expected window.
  - A.phone stops publishing to `G(4)/M`'s channel as soon as it holds the removal.
- **Invariants:** INV-CUT (after the window), INV-MONOTONE-EPOCH, INV-AUTHZ
- **Notes:** Nothing says who destroys `G(4)/M`'s channel, since "the loser's author destroys its channel" here means M. It stays until the horizon or until healing rules stop it; harmless, but the harness can't assert it is gone.

### RO-031 An admin replaces a healthy genesis
- **Kind:** adversarial
- **Design:** §11.6 (interrupted rotations, competing rotations)
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac), M admin, A writer, C writer.
- **Steps:**
  1. `J.mac removes C`; `J.mac syncs` (`G(4)/J`, grants sent).
  2. Immediately, `M.phone signs` `G(4)/M` with `replaces: wid(G(4)/J)` and a valid `to`, and sends grants.
  3. When J.mac writes a new genesis, M replaces that one too. Repeat 5 times.
- **Expect:**
  - Intended: the honest genesis stands; devices settle on one epoch-4 key within one round.
- **Invariants:** INV-MONOTONE-EPOCH, INV-QUIESCE, INV-NO-HOLD-FOREVER
- **Notes:** GAP: the 10+10-minute wait in §11.6 isn't something a validator can check (it depends on timing and on grants not reaching someone), so every replacement counts. One admin can knock out every honest genesis and keep the collection rotating forever, with every device stalled in its queue. Honest devices can get into the same loop under partial withholding: two admin devices that each lack the other's grant each replace the other's genesis. Nothing says whether two replacements of one genesis compete by write ID, whether a replacement can be replaced, or what happens if two geneses replace each other.

### RO-032 A fenced thief keeps making cuts
- **Kind:** adversarial
- **Design:** §8.2 (cuts, the pin), §9.4, §6.2 (chain-valid), §3.2, Q15
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A writer, M writer (colluding). `T(J.phone)`.
- **Steps:**
  1. `J.mac revokes J.phone`; `J.mac syncs` (fence, `G(4)`, `to` = {J, A, M}).
  2. M gives T the heads of epoch 4's log. `T(J.phone)` signs `owner {to: M}` building on them (and so on the fence).
  3. M re-encrypts it into channel 4.
  4. Variant: T signs the same write building on `G(4)` as read from channel 3 and appends it to channel 3 only.
  5. All devices sync.
- **Expect:**
  - Intended (§3.2, §8.2: "neither does a stolen device once it is fenced"): J is still owner on every device; M is not.
  - Variant: no device takes a log write from channel 3 into epoch 4.
- **Invariants:** INV-AUTHZ, INV-ONE-OWNER
- **Notes:** GAP: the §8.2 field rules check the signer's user (`owner`: "A is the owner") and a chain-valid signature, which a revoked device still has (§6.2: its entry still counts). None checks that the signing device is unfenced in the write's past. A hand-over is a cut, and "a cut that stands counts", so T's write counts and builds on the genesis, which satisfies the pin. Q15 covers cuts made by backdating; this one needs no backdating, and with a member's help it works at any time after the rotation. GAP: §11.6's late-write rule covers data writes (`_carried`); it doesn't say what a device does with log writes that appear in an old channel after it has switched, including ones that build on the new genesis (step 4).

### RO-033 Malformed geneses and a replayed grant
- **Kind:** adversarial
- **Design:** §11.6 step 2 (validity), §11.4, §6.3 (home `key`)
- **Binding:** any
- **Setup:** `House` epoch 5: J owner, M admin, A writer, B writer. E captured A's epoch-4 grant last month.
- **Steps:**
  1. `M.phone signs` and appends to channel 5: a genesis with `n: 6` and `to` omitting B; one with `n: 7`; one with `n: 6` and `prev = commit(K(4))`; one with `n: 6` naming C, a non-member.
  2. `hub: replay` A's epoch-4 grant into A's inbox.
  3. All devices sync.
- **Expect:**
  - None of the four geneses is valid on any device. No device stops publishing to channel 5 or queues writes because of them, and none of them affects which genesis wins if a valid `G(6)` appears.
  - A's home `key.n` stays 5. The replayed grant is destroyed and logged as failed.
- **Invariants:** INV-MONOTONE-EPOCH, INV-AUTHZ, INV-QUIESCE
- **Notes:** The key IDs in `to` aren't part of validity: M can name a wrong `kid` for B, and B's grant then goes nowhere useful until B's key request (§11.4). Not a gap, but worth asserting that a wrong `kid` doesn't make the genesis invalid.

---

## UX

### RO-034 Removed while the owner is offline: what the others lose
- **Kind:** ux
- **Design:** §13.2 (Members), §8.3 (snapshot), §11.6, §3.3
- **Binding:** any
- **Setup:** `Groceries` epoch 3: J owner (J.phone only), A writer, B writer.
- **Steps:**
  1. `J.phone offline`. `J.phone removes B`.
  2. Over two hours, `B.phone adds` "Oat milk" (record `x`: `type`, `title` by B). `A.phone syncs` and `A.phone sets x.notes = "the Swedish one"`.
  3. `J.phone online`, `J.phone syncs` (rotation to 4). `A.phone syncs`.
- **Expect:**
  - From step 1, J.phone's member list omits B.
  - Until step 3, A.phone and B.phone show "Oat milk", and B.phone shows no sign of the removal.
  - After A.phone switches, `x` in epoch 4 holds only A's `notes` write. B's `type` and `title` writes aren't carried (J.phone merged its own removal first).
- **Invariants:** INV-CONVERGE, INV-UX, INV-NO-LOSS
- **Notes:** GAP: "Removing is instant for the user" applies only to the owner's screen. Everything the removed member wrote between the removal and the rotation, which others saw and built on, disappears at the switch, and the design tells no one. GAP: a record whose creating writes are left behind while another member's field writes are carried reaches the app with no `type` field. Nothing tells the app to expect partial records, or whether such a record should be dropped, shown or handed back.

### RO-035 Waiting for the new key
- **Kind:** ux
- **Design:** §11.6 (every other device), §11.4, §13.2, §13.3
- **Binding:** any
- **Setup:** `House` epoch 3: J owner (J.mac, J.phone), A writer (A.phone).
- **Steps:**
  1. `J.mac removes B`; `J.mac crashes after` sending J's grant and before A's.
  2. `A.phone syncs`; A adds three items.
  3. `wait 10 min`; `A.phone syncs`; `J.phone syncs`.
- **Expect:**
  - Between steps 2 and 3, A.phone shows the three items locally and nothing on the hub; it publishes nothing to channel 3.
  - At 10 minutes A.phone sends a key request; J.phone answers; A.phone switches and publishes the three items to channel 4.
  - A.phone shows the sync indicator as behind for the whole wait.
- **Invariants:** INV-NO-LOSS, INV-UX
- **Notes:** GAP: §13 has no state or message for "waiting for a key" beyond the generic "subtle sync indicator when offline or behind". With no answer (owner gone, RO-028's E), writes queue indefinitely and the user is never told. GAP: writes made after seeing the genesis but before getting the key could be stamped 3 (and then need A.phone's own `_carried`) or 4 (once the key arrives); §11.6's "queues its writes" doesn't say which.

### RO-036 A demoted writer's unsent edits
- **Kind:** ux
- **Design:** §8.3, §11.6, §13.2, INV-NO-LOSS
- **Binding:** any
- **Setup:** `Groceries` epoch 3: J owner (J.phone), A writer, B writer.
- **Steps:**
  1. `B.phone offline`; B adds `x1`, `x2`.
  2. `J.phone sets role B = reader`; `J.phone syncs` (rotation to 4).
  3. `B.phone online`, `B.phone syncs`.
- **Expect:**
  - B.phone publishes nothing to channel 3 (it syncs first and sees `G(4)`), and cannot vouch for `x1`, `x2` in epoch 4.
  - `x1`, `x2` never appear on any other device.
  - B.phone shows `Groceries` read-only and keeps showing B as a member.
- **Invariants:** INV-AUTHZ, INV-NO-LOSS, INV-UX
- **Notes:** GAP: §11.6 hands unsent edits to the app only when the device was removed. For a demotion it says nothing, so `x1` and `x2` are dropped silently on the one device that holds them. INV-NO-LOSS allows the drop but needs the app told "when the design says it is", and here it doesn't say.
