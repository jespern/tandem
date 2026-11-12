# Sharing and roles

Scenarios for collections, rosters and ranked roles, invites and join requests, approvals and declines, grants and key requests, leaving, hand-over and deletion (DESIGN §7, §8, §11.1–§11.5, §11.7, §13).

Unless a scenario says otherwise, "House" is a collection owned by J whose roster's owner `member:J` entry carries the fallback role `writer`, and every device is online.

---

## Real-world

### SH-001 Create a private list
- **Kind:** real-world
- **Design:** §7.1, §7.2, §7.3, §7.4, §8.1, §8.2, §6.3, §11.6
- **Binding:** any
- **Setup:** J has `J.phone` and `J.mac` on hub H with an identity. No collections besides Inbox and Later.
- **Steps:**
  1. `J.phone creates collection Groceries`.
  2. `J.phone adds milk to Groceries`.
  3. `J.phone syncs`; `J.mac syncs`.
- **Expect:**
  - `_roster.genesis` is `{creator: J's uid, nonce}`, and `H("tandem/v1/collection" || uid || nonce)[0..16]` equals the collection ID on both devices.
  - `_epoch.genesis` has `n: 0`, no `prev`, and `to` = `[[J's uid, kid of J's current uka]]`.
  - J's home holds `membership:<Groceries>` with `state: active` and `key.n: 0`.
  - All of Groceries' messages on the hub carry one channel tag (epoch 0); none of them, nor any header, contains "Groceries", "milk" or J's uid.
  - `J.mac` shows Groceries with "milk", author J; its members screen lists only J, as owner.
- **Invariants:** INV-HUB-BLIND, INV-ONE-OWNER, INV-SIGNED
- **Notes:** GAP: §8.2's bootstrap rule says "the creator's first act is writing their own `member` entry", but the rule for `member:X` written by X allows only `status: left`, and the rule for `member:X` written "by someone else" doesn't apply. As written the creator's own entry doesn't stand. The design also never says which role that entry carries. §8.1 calls it "the role they fall back to after handing ownership on", and the allowed values are admin, writer and reader, so two SDKs can give the former owner different roles after a hand-over (SH-006).

### SH-002 Share the shopping list with Aimee in the kitchen
- **Kind:** real-world
- **Design:** §11.1, §11.2, §11.3, §11.4, §8.3, §13.2
- **Binding:** any
- **Setup:** J owns Groceries at epoch 0 with items "milk" and "eggs". A has `A.phone` and `A.mac` on the same hub, with an identity.
- **Steps:**
  1. `J.phone invites qr to Groceries as writer`.
  2. `A.phone joins via` the QR.
  3. `J.phone syncs`; `A.phone syncs`; `A.mac syncs`.
  4. `A.phone adds bread to Groceries`; `J.phone syncs`.
- **Expect:**
  - The roster holds `invite:<id>` with `role: writer`, `autoApprove: true`, `maxUses: 1`, `expires` = creation + 10 min, and `by` = J. Neither `S` nor the invite payload appears on the hub.
  - `A.phone` shows no waiting screen, and `J.phone` shows no Approve/Decline prompt.
  - The roster holds `member:<A>` = `{role: writer, status: active, name: "Aimee", via: <id>}`, signed by a J device.
  - After step 3 the invite channel holds no kind-3 message, A's inbox holds no grant, and A's home has `membership:<Groceries>` active with `key.n: 0`.
  - `A.mac` shows Groceries with "milk" and "eggs" without scanning anything.
  - The epoch is still 0, because adding a member doesn't rotate.
  - `J.phone` shows "bread" with author Aimee, and its members screen lists Aimee as writer.
- **Invariants:** INV-HUB-BLIND, INV-AUTHZ, INV-SIGNED

### SH-003 A link to Bo, opened two days later on a fresh phone
- **Kind:** real-world
- **Design:** §11.1, §11.2, §11.3, §11.4, §6.1, §13.1, §13.2
- **Binding:** any
- **Setup:** J owns House at epoch 1, with A as a writer. `B.phone` is a fresh install with no hub and no identity.
- **Steps:**
  1. `J.mac invites link to House as writer`; J sends the link to Bo over iMessage (outside Tandem).
  2. `J.mac quits`; `J.phone offline`.
  3. `wait 2 days`.
  4. `B.phone joins via` the link.
  5. `wait 5 hours`; `J.phone online`; `J.phone launches`.
  6. `J.phone approves B`.
  7. `B.phone syncs`; `J.mac launches`.
- **Expect:**
  - After step 4, `B.phone` uses the hub credential from the payload. The hub holds Bo's profile, recovery pointer and home generation 0, and `B.phone` showed the recovery key once.
  - After step 4, Bo's home has `membership:<House>` with `state: requested` and `invite: <id>`, the invite channel holds one kind-3 message, and `B.phone` shows "Waiting for Jesper to approve".
  - After step 5, `J.phone` shows "Bo wants to join House" with Approve and Decline.
  - After step 6, the roster holds `member:<B>` = `{writer, active, via: <id>}`, Bo's inbox holds a grant sealed to the `kid` of Bo's current manifest, and the invite channel holds no kind-3 message.
  - After step 7, `B.phone` shows House with all of its items, Bo's home has `state: active`, Bo's inbox is empty, and `J.mac` shows no pending request.
  - `B.phone`'s hub credential equals the one `J.mac` uses, which is shared by default (§13.1).
- **Invariants:** INV-HUB-BLIND, INV-AUTHZ, INV-UX
- **Notes:** GAP: §11.1 gives a link's defaults as auto-approve off and a 7-day expiry, but no `maxUses`; only the QR has "1 use". Clients may write 1, a large number, or nothing, and §11.3's "has uses left" check then differs between approvers. Whether the request in step 4 is posted before or after Bo confirms the recovery key is also open (see SH-038).

### SH-004 Bo joins, then leaves a week later
- **Kind:** real-world
- **Design:** §11.5, §8.2, §11.6, §8.3, §13.2
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner, A and Bo are writers, and Bo joined by link.
- **Steps:**
  1. `B.phone adds BBQ coal to House`; `B.phone syncs`.
  2. `wait 7 days`.
  3. `B.phone leaves House`.
  4. `J.phone syncs`; `A.phone syncs` (until quiescent, past the rotation's random delay).
  5. `A.phone adds lighter to House`; `B.phone syncs`.
- **Expect:**
  - The roster holds `member:<B>` with `status: left`, signed by `B.phone`. Bo's home has `membership:<House>` with `state: left`, and `B.phone` no longer lists House.
  - A J device writes an epoch-3 genesis with `prev` = `commit(K(c,2))` and `to` = exactly `[J, A]`, publishes it in the epoch-2 channel, and grants the key to J and A. (A is a writer, so only J's devices may rotate.)
  - "BBQ coal" shows on every J and A device, with author Bo, and its epoch-3 record carries a `_carried` list naming Bo's write.
  - Bo's keys can't decrypt "lighter" or any other epoch-3 message.
  - No J or A device lists Bo as an active member.
  - Bo's credential still authenticates to the hub (§3.3).
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-ONE-OWNER
- **Notes:** GAP: §11.5 says the leaver "stops following". It doesn't say whether its devices delete their replica, which removal requires (§11.6), or what happens to writes queued but not yet published when the user taps Leave: publish them first, hand them to the app, or drop them.

### SH-005 Make a shared list private again while Aimee is offline
- **Kind:** real-world
- **Design:** §7.4, §8.2, §11.6, §8.3, §13.2
- **Binding:** any
- **Setup:** House is at epoch 4. J is owner, and A and Bo are writers. `A.mac` is offline; `A.phone` is online and idle.
- **Steps:**
  1. `A.mac adds eggs to House` (offline).
  2. `J.phone removes A`; `J.phone removes B` (seconds apart).
  3. `J.phone syncs` until quiescent.
  4. `B.phone syncs`; `A.phone syncs`.
  5. `A.mac online`; `A.mac syncs`.
- **Expect:**
  - `J.phone`'s members screen shows only J right after step 2, before any rotation.
  - The roster holds `member:<A>` and `member:<B>`, both `removed`.
  - The final epoch's genesis has `to` = `[J]`. Whether one rotation happened or two, neither A's nor Bo's keys decrypt any message of that epoch.
  - `B.phone` and `A.phone` show "House is no longer shared with you." and hold no replica of House.
  - `A.mac` syncs before publishing, sees the genesis, publishes nothing to any House channel, and hands "eggs" to the app (Liszto: Inbox).
  - No J device shows "eggs".
- **Invariants:** INV-CUT, INV-NO-LOSS, INV-ONE-OWNER, INV-MONOTONE-EPOCH

### SH-006 Hand the list to Aimee before stopping using the app
- **Kind:** real-world
- **Design:** §8.1, §8.2, §11.5, §11.6, §13.2
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner (fallback role writer), and A and Bo are writers.
- **Steps:**
  1. `J.phone hands House to A`; all devices sync.
  2. `J.phone leaves House`.
  3. `A.phone syncs`; `B.phone syncs`; `J.mac syncs`.
- **Expect:**
  - The roster holds `owner` = `{to: A}`, signed by `J.phone`. After step 1, every device shows Aimee as owner and Jesper as writer.
  - After step 1, `A.phone`'s sharing screen offers "Make admin" and "Hand over", and J's devices offer neither.
  - After step 3, the roster holds `member:<J>` with `status: left`, and J's home has `membership:<House>` with `state: left`.
  - An A device writes the final genesis, with `to` = exactly `[A, Bo]`. J's keys don't decrypt it.
  - No J device lists House, and items J wrote still show author J.
- **Invariants:** INV-ONE-OWNER, INV-CUT
- **Notes:** GAP: a hand-over needs no consent or action from the recipient (§8.2 requires only that the target is an active member). If Aimee had stopped using the app, House would be "owner gone" (SH-014) as soon as J left, and nobody would be told. The design is silent on whether the recipient must accept.

### SH-007 Make Bo a reader after he keeps adding junk
- **Kind:** real-world
- **Design:** §8.2, §8.3, §11.6, §3.3
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner, and A and Bo are writers.
- **Steps:**
  1. `J.phone sets role B = reader`. `hub: withhold` J's roster write and every later House message `from B.phone`.
  2. `J.phone syncs`, which rotates House to epoch 3 with `to` = `[J, A, B]`.
  3. `B.phone adds junk1 to House`; `B.phone syncs`, which publishes junk1 to the epoch-2 channel.
  4. `A.phone syncs`. A still watches the epoch-2 channel for late writes.
  5. The withholding ends; `B.phone syncs`.
- **Expect:**
  - The roster holds `member:<B>` with role reader. Bo is in the epoch-3 `to`, and `B.phone` accepts its grant and can read epoch 3.
  - After step 5, `B.phone` shows House read-only, with no add control.
  - The snapshot's `_carried` lists don't name junk1.
  - No J or A device shows junk1, and no `_carried` list in epoch 3 names it.
- **Invariants:** INV-AUTHZ, INV-CUT
- **Notes:** GAP: §11.6 forwards a late write if it changes the old replica and "its author was entitled to make it", and §8.3's `_carried` rule lets any usable device of a write-enabled member vouch. Under §8.3's fixed definitions Bo was write-enabled in epoch 2, so junk1 is a valid epoch-2 write and changes A's old replica, which means `A.phone` would forward it into epoch 3. That contradicts §3.3 ("their old-epoch writes aren't forwarded") and the snapshot rule, which leaves out what the rotating device hadn't merged before the cut. The forwarding rule needs to exclude writes by authors whose cut the forwarding device holds.

### SH-008 Delete a list while Aimee is away
- **Kind:** real-world
- **Design:** §11.7, §7.3, §8.2, §12.3, §13.2
- **Binding:** any
- **Setup:** J owns Trip at epoch 1, with A as a writer and Bo as a reader. `A.phone` is offline.
- **Steps:**
  1. `A.phone sets passport.notes = "renew"` (offline).
  2. `J.mac` deletes Trip (writes `_collection.deleted`).
  3. `J.mac syncs`; `B.phone syncs`; `J.phone syncs`.
  4. `wait 10 days`; `A.phone online`; `A.phone syncs`.
  5. `wait 20 days`; `J.mac syncs`.
- **Expect:**
  - `_collection.deleted`, signed by `J.mac`, stands.
  - After step 3, `B.phone`, `J.phone` and `J.mac` don't list Trip, and J's and Bo's homes have `membership:<Trip>` with `state: removed`.
  - After step 4, `A.phone` removes Trip, A's home has `state: removed`, and `A.phone` appends nothing to any Trip channel.
  - After step 5, the hub holds no data-record message of Trip, and the `_roster`, `_collection` and `_epoch` messages remain.
  - No rotation happens.
- **Invariants:** INV-QUIESCE, INV-UX
- **Notes:** Step 2 is outside the step vocabulary ("deletes collection").
  GAP: removal hands unsent edits to the app (§11.6, §13.2), but §11.7 says only that members' devices "remove the collection locally". Whether A's unpublished note is handed to the app or lost is unspecified.
  GAP: there is no message for "the owner deleted this list"; §13.2's "House is no longer shared with you" is wrong for the owner's own devices and misleading for members.
  GAP: `deleted` is an ordinary `assign` field. Nothing says whether the owner can later write `deleted: false`, or what devices that already removed the list do then.

---

## Edge

### SH-009 A request made in time, approved after the link expired
- **Kind:** edge
- **Design:** §11.1, §11.2, §11.3, §13.3
- **Binding:** any
- **Setup:** J owns House. `J.mac invites link to House as writer` (7 days) on day 0, then J's devices go offline (J is travelling).
- **Steps:**
  1. `wait 6 days`; `B.phone joins via` the link.
  2. `wait 2 days`; `J.phone online`; `J.phone launches`.
- **Expect:**
  - After step 1, `B.phone` shows "Waiting for Jesper to approve".
  - After step 2, `J.phone` doesn't approve the request: the roster has no `member:<B>`.
  - Bo is told something from §13.3, or a newly named message. He isn't left on a waiting screen.
  - `J.phone` shows J something about the refused request, or nothing; the scenario records which.
- **Invariants:** INV-UX, INV-AUTHZ
- **Notes:** GAP: §11.3 checks expiry when the approver sees the request, and a request carries no time, so a request posted on day 6 is refused on day 8. For an owner who opens the app now and then, this is the common case. It is also unspecified what the approver does with an expired request (destroy it silently, send a decline, or show "Bo's request expired"), what Bo's home `state` becomes when his device "stops waiting" at expiry (§6.3 has no `expired` state), and which message Bo sees. §13.3's "This code has expired" describes opening an expired code, not a pending request timing out.

### SH-010 Two approvers at once, and maxUses exceeded
- **Kind:** edge
- **Design:** §11.1, §11.3, §11.4, §8.2
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner and A is admin. There is a link invite I for writers with `maxUses: 1`. `J.phone` and `A.mac` are online.
- **Steps:**
  1. `B.phone joins via` I; `C.phone joins via` I within the same second.
  2. `J.phone syncs` and `J.phone approves B`. At the same moment, `A.mac syncs`, `A.mac approves B` and `A.mac approves C`, neither device having seen the other's writes.
  3. All devices sync.
- **Expect:**
  - The roster has `member:<B>` = `{writer, active, via: I}` from two counting writes with equal values, and `member:<C>` = `{writer, active, via: I}`.
  - Two members' `via` names I, which is more than `maxUses`. No device removes either automatically.
  - Bo's inbox received two grants for epoch 1. `B.phone` accepts one, destroys the other as not ahead, and its inbox ends empty.
  - Every device's members screen lists Bo and Carla, and no rotation happens.
- **Invariants:** INV-CONVERGE, INV-AUTHZ

### SH-011 Least privilege: the owner promotes while an admin demotes
- **Kind:** edge
- **Design:** §8.1, §8.2, §9.4, §8.3, §11.6
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner, A is admin, and Bo and Carla are writers. `J.mac` is offline.
- **Steps:**
  1. `J.mac sets role B = admin` (offline).
  2. `A.phone sets role B = reader`; `A.phone syncs`.
  3. `J.mac online`; `J.mac syncs`. `hub: withhold` A's roster write `from B.phone`.
  4. `B.phone syncs`, so Bo sees himself as admin; `B.phone sets role C = reader`.
  5. The withholding ends; all devices sync.
- **Expect:**
  - Every device resolves `member:<B>` to role reader, by least privilege.
  - Bo's change to Carla doesn't count, because it is concurrent with a standing reduction of Bo. Carla is a writer on every device.
  - A rotation follows, because Bo now holds a lower role than the pin. Its `to` includes Bo (reader) and Carla.
  - `B.phone` shows House read-only.
- **Invariants:** INV-AUTHZ, INV-CONVERGE
- **Notes:** GAP: least privilege is stated as `removed > left > reader > writer > admin`, but a `member` value holds both `status` and `role`, plus `name` and `via`. The design doesn't say how to compare `{status: active, role: reader}` with `{status: left, role: admin}` (status first, presumably), or how to break a tie between two counting writes of equal privilege whose `name` or `via` differ. §9.4's "otherwise the greatest (hlc, …)" reads as applying only to fields without a rule of their own.

### SH-012 Aimee, already a writer, taps a reader link
- **Kind:** edge
- **Design:** §11.2, §11.3, §6.3, §8.2
- **Binding:** any
- **Setup:** House is at epoch 3. J is owner and A is a writer. J posts a reader link for Carla in the family chat.
- **Steps:**
  1. `J.phone invites link to House as reader`.
  2. `A.phone joins via` the link.
  3. If a request reaches J: `J.phone approves A`.
- **Expect:**
  - `A.phone` tells Aimee she is already in House and appends no kind-3 message.
  - A's home keeps `membership:<House>` with `state: active` and its `key`.
  - `member:<A>` keeps role writer, and no rotation happens.
- **Invariants:** INV-AUTHZ, INV-UX
- **Notes:** GAP: §11.2 doesn't cover a joiner who is already an active member. Following it literally, step 3 of §11.2 overwrites the home `state` with `requested`, and approval writes `member:<A>` with the invite's role. That demotes Aimee to reader and forces a rotation, all from tapping a link. An admin tapping a writer link would likewise demote herself. The same applies when two of one user's devices both open a link: two requests, one uid.

### SH-013 Admitted just before a rotation
- **Kind:** edge
- **Design:** §8.2 (the pin), §11.3, §11.4, §11.6, §8.3
- **Binding:** any
- **Setup:** House is at epoch 5. J is owner, A is admin, and C is a writer. A has created link invite I for Bo.
- **Steps:**
  1. `hub: withhold` the epoch-5 `_epoch` messages `from A.mac`.
  2. `J.phone removes C`; `J.phone syncs`, which rotates to epoch 6 with `to` = `[J, A]`.
  3. `B.phone joins via` I; `A.mac approves B`. `A.mac` hasn't seen the epoch-6 genesis, so it writes `member:<B>` into the epoch-5 log and sends a grant for epoch 5.
  4. `B.phone syncs`; `B.phone adds tent to House`.
  5. The withholding ends; all devices sync.
- **Expect:**
  - `A.mac` re-issues `member:<B>` on top of the epoch-6 genesis (§8.2) and sends Bo a grant for epoch 6.
  - `B.phone` ends on epoch 6 with House visible, and Bo's home has `state: active`.
  - `B.phone` never shows "House is no longer shared with you".
  - Carla's keys don't decrypt epoch 6.
- **Invariants:** INV-CUT, INV-NO-HOLD-FOREVER, INV-MONOTONE-EPOCH
- **Notes:** GAP: when `B.phone` sees the epoch-6 genesis, it isn't active in the roster that genesis pinned. Per §11.6 it therefore treats itself as removed: it hands "tent" to the app, deletes its replica, and (§13.2) tells Bo the list is no longer shared. If it also sets its home `state` to `removed`, §11.4 refuses the re-issued grant (it needs `requested` or `active`), and Bo is shut out for good while the roster lists him active. The removal rule needs an exception for members admitted after the pinned roster, for example waiting for a re-issue while the admission is newer than the genesis.

### SH-014 The owner is gone for good
- **Kind:** edge
- **Design:** §8.2 ("owner gone"), §8.1, §11.6, §11.7, §11.4
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner, A is admin, and Bo is a writer.
- **Steps:**
  1. J loses every device and the recovery key: the harness erases `J.phone`, `J.mac`, `J.ipad` and `J.rk`.
  2. `A.phone removes B`; `A.phone syncs`, which rotates to epoch 3.
  3. `A.phone invites link to House as writer`; `C.phone joins via` it; `A.phone approves C`.
  4. A tries "Make admin", "Hand over" and "Delete" on House.
- **Expect:**
  - `A.phone` writes the epoch-3 genesis with `to` = `[J, A]`. A grant sealed to J's `uka` is appended to J's inbox and never consumed.
  - Carla is admitted with role writer and gets the epoch-3 key.
  - A's client offers none of the three actions in step 4. If a modified client writes them, they don't stand.
  - The roster still shows J as owner and active.
- **Invariants:** INV-ONE-OWNER, INV-AUTHZ
- **Notes:** Step 1 is outside the step vocabulary.
  GAP: "owner gone" (§8.2) isn't a state any device can detect. The roster can't distinguish a lost owner from one on holiday, so the "documented owner gone state" in INV-ONE-OWNER can't be checked. Consequences the design doesn't address: every rotation seals another grant to J's dead key, and they pile up in J's inbox because nobody destroys them; `deleted` can never be written, so House can only be abandoned, never deleted, and its log stays on the hub indefinitely; and once the last admin leaves, the members are left with a list nobody can manage.

### SH-015 A hand-over races the recipient's departure
- **Kind:** edge
- **Design:** §8.2 (`owner`, cuts, least privilege), §11.5, §11.6
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner, A is admin, and Bo is a writer. `J.phone` is offline.
- **Steps:**
  1. `J.phone hands House to B` (offline).
  2. `B.phone leaves House`.
  3. `J.phone online`; all devices sync.
  4. Variant: replace step 2 with `A.phone removes B`.
- **Expect:**
  - The converged roster has exactly one owner, and that owner is an active member.
  - Some device of the owner or an admin can still write `owner`, appoint admins and delete House.
- **Invariants:** INV-ONE-OWNER
- **Notes:** GAP: as written, both writes stand. The `owner` write is judged on its own past, where Bo is active, and Bo's `left` (or A's removal of Bo, since A outranks a writer) is judged on its own past. `owner` and `member:<B>` are separate fields and no rule reconciles them, so the roster ends with owner Bo and Bo `left` (or `removed`). The hand-over is also a reduction of J, so J can't act as owner either. Nobody can appoint admins, hand over or delete. And because the `owner` rule requires the author to be the owner but not active, a removed Bo could still hand House on by building on his own removal. A hand-over should not stand if the target is reduced concurrently, or a reduction of the target should void it.

### SH-016 Admitted by an admin whose demotion was under way
- **Kind:** edge
- **Design:** §8.2 (cuts), §8.3, §11.3, §11.6, §13.2
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner and A is admin, with link invite I created by A. `J.mac` is offline.
- **Steps:**
  1. `J.mac sets role A = writer` (offline).
  2. `B.phone joins via` I; `A.phone approves B`.
  3. `B.phone syncs`; `B.phone adds tent to House`; `B.phone syncs`.
  4. `J.mac online`; `J.mac syncs`, which rotates to epoch 3.
  5. `B.phone syncs`; `A.phone syncs`.
- **Expect:**
  - `member:<B>` doesn't count on any device that holds A's demotion. The epoch-3 genesis has `to` = exactly `[J, A]`.
  - "tent" is valid in epoch 2 (the admission is valid on its own past, §8.3) and appears in epoch 3 with author Bo.
  - `B.phone` deletes its replica and shows a message.
  - J's devices are told that Bo's admission didn't count (for example, Bo's request shown again as pending), and A's device too.
- **Invariants:** INV-AUTHZ, INV-UX
- **Notes:** GAP: §8.3 says why Bo's writes stay valid until the rotation, but nobody is told what happened. Bo is shown "House is no longer shared with you" for a removal nobody made. J never saw a request, A sees Bo disappear, and Bo's request was destroyed at approval, so J can't approve it now; Bo needs a new invite. The design should say who surfaces a voided admission, and to whom.

### SH-017 Approve and decline race
- **Kind:** edge
- **Design:** §11.3, §11.4, §6.3
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner and A is admin, with a link invite I.
- **Steps:**
  1. `B.phone joins via` I.
  2. `J.phone approves B`, and at the same moment `A.mac declines B`; neither has seen the other's action.
  3. `B.phone syncs`. Run twice: once with the grant fetched first, once with the decline first.
- **Expect:**
  - The roster holds `member:<B>` active in both runs, since a decline writes nothing to the roster.
  - In both runs, `B.phone` ends with Bo's home at `state: active` and House visible, so it agrees with the roster.
  - The invite channel holds no kind-3 message.
- **Invariants:** INV-CONVERGE, INV-NO-HOLD-FOREVER
- **Notes:** GAP: the outcome depends on arrival order. If the decline arrives first, `B.phone` drops the pending membership (§11.4) and then refuses the grant, which needs `requested` or `active`. Bo is then an active roster member who never gets a key, and no key request will follow (SH-032). If the grant arrives first, Bo is active, and §11.4 doesn't say whether a later decline for an active membership is ignored.

---

## Adversarial

### SH-018 Mallory, an admin, acts on an equal and on the owner
- **Kind:** adversarial
- **Design:** §8.1, §8.2, §7.1, §9.4, §11.6, §11.7
- **Binding:** any
- **Setup:** House is at epoch 4. J is owner, A and M are admins, and Bo is a writer.
- **Steps:** Each write builds on everything `M.phone` has seen.
  1. `M.phone signs` `member:<A>` = `{writer, active}`, and then `member:<A>` = `removed`.
  2. `M.phone signs` `member:<J>` = `{reader, active}`.
  3. `M.phone signs` `owner` = `{to: M}`.
  4. `M.phone signs` `member:<M>` = `{admin, active, name: "Jesper"}`.
  5. `M.phone signs` `_collection.deleted`.
  6. `M.phone signs` `fence:<J.phone did>` with `rev` = a write ID from J's chain that isn't a revocation.
  7. `M.phone signs` an `_epoch.genesis` for epoch 5 whose `to` omits A.
  8. `M.phone signs` a second `_roster.genesis` with M as creator and a fresh nonce.
  9. All devices sync.
- **Expect:**
  - None of the writes in steps 1–8 stands on any honest device. A remains admin, J remains owner and active, House isn't deleted, and M's display name is unchanged.
  - No J or A device starts a rotation because of these writes, and no device switches to M's epoch 5.
  - J.phone isn't fenced, and A's and J's devices keep reading epoch 4.
  - The genesis from step 8 fails the collection-ID check, and House's ID is unchanged everywhere.
  - Data writes M makes in epoch 4 remain valid.
- **Invariants:** INV-AUTHZ, INV-ONE-OWNER, INV-SIGNED
- **Notes:** GAP: §9.4 says that in a log "every write is kept", and §11.6 republishes "the whole log" at each rotation. The design doesn't say whether writes that don't stand are kept and republished, though they never can stand, since their past is fixed. If they are, Mallory can grow every member's log and every snapshot without bound at no cost; Q16 only considers honest growth. If they aren't, an honest write that lists one in `prev` ("building on it by mistake does no harm", §9.4) has an incomplete past and is held forever.

### SH-019 Mallory, an admin, tampers with invites
- **Kind:** adversarial
- **Design:** §8.1, §8.2 (`invite:Y`), §11.1, §11.3, §9.4
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner, M is admin, and A is a writer. J has created link invite I (writer, auto-approve off, 7 days, `by` J) and sent it to Carla.
- **Steps:**
  1. `M.phone signs` `invite:I` with the same `pub`, but `autoApprove: true`, `expires` one year out and `maxUses: 1000`, and an HLC one year in the future.
  2. `M.phone signs` `invite:I2` with `role: "admin"`, `autoApprove: true` and `by` = J's uid.
  3. `C.phone joins via` I. Someone joins via I2.
  4. Variant: in step 1, M also replaces `pub` with a key of her own.
- **Expect:**
  - J's devices still treat I as a link invite that needs approval, and Carla's request waits for J.
  - No device approves a request on I2, and no member ever gets role admin through an invite.
  - J's members screen shows who created each open invite, as the signer, not the `by` field.
- **Invariants:** INV-AUTHZ
- **Notes:** GAP: §8.2's rule for `invite:Y` is only "A is owner or admin". It doesn't require that the author created the invite or outranks its creator, or that `pub`, `role` and `by` stay fixed. As written, step 1 stands and silently turns J's link into a year-long auto-approve invite. Concurrent edits to an invite fall to the greatest-HLC default, which M wins by future-dating. In the variant, M moves the invite channel (keyed from `pub`, §5.6), so every existing copy of J's link fails `isig`.
  GAP: in step 2, only §8.1's value table limits `role` to writer or reader; §8.2 doesn't check it. An SDK that accepts the value auto-approves on `J.phone` and writes an admin, which stands because the owner outranks admin. Whether `by` must equal the signer isn't stated.

### SH-020 A demoted admin acts while leaving her demotion out of `prev`
- **Kind:** adversarial
- **Design:** §8.2 (cuts, the pin), §9.4, §11.6
- **Binding:** any
- **Setup:** House is at epoch 4. J is owner, M and A are admins, and C is a writer.
- **Steps:**
  1. `J.phone sets role M = writer`. `hub: withhold` that roster write `from A.phone`.
  2. `M.phone signs` `member:<C>` = `removed`, `_collection.name` = "Mallory's", and `invite:I3`, each with `prev` = the log heads before step 1.
  3. `J.mac syncs`.
  4. `A.phone syncs`. A sees M's removal of Carla but not J's demotion of M, so A rotates House to epoch 5 and its genesis pins Carla removed.
  5. The withholding ends; all devices sync.
- **Expect:**
  - After step 3, on `J.mac`: Carla is an active writer, the name is unchanged, and I3 isn't an open invite. These writes are concurrent with M's standing reduction.
  - After step 5: Carla is an active member in the current epoch and can decrypt it. M is a writer, and House keeps its name.
- **Invariants:** INV-AUTHZ, INV-CUT
- **Notes:** GAP: §8.2 says such a write "achieves nothing", but the pin contradicts that once another admin rotates first. A device following epoch 5 counts a log write only if it is in the past of the epoch-5 genesis or builds on it. M's removal of Carla is in that past; J's demotion of M is neither, so in epoch 5 Carla's removal counts and she is cut. J's device re-issues the demotion on top of epoch 5, but nothing restores Carla. "Backdating is bounded by the stretch since the last rotation" therefore means a reduced admin's backdated cut wins whenever an honest admin rotates before seeing the reduction.

### SH-021 Devices follow a demoted admin's rotation
- **Kind:** adversarial
- **Design:** §8.2 (cuts), §11.4, §11.6 (competing rotations), §8.3
- **Binding:** any
- **Setup:** House is at epoch 3. J is owner, M is admin, and A is a writer. `J.mac` is offline.
- **Steps:**
  1. `J.mac sets role M = writer` (offline).
  2. `M.phone rotates House`: a genesis for epoch 4 whose `prev` leaves out the demotion, with grants to J, A and M.
  3. `A.phone syncs`, accepts the grant and switches to M's epoch 4. `A.phone adds bread to House`.
  4. `J.mac online`; `J.mac syncs`; then all devices sync.
- **Expect:**
  - Every device holding the demotion treats M's genesis as not counting (§11.6: "A genesis written by someone a concurrent cut removed doesn't count").
  - A J device writes a valid epoch-4 genesis, and every device, `A.phone` included, ends on it.
  - "bread" appears in J's epoch 4 with author Aimee.
  - A's epoch never moves backwards.
- **Invariants:** INV-MONOTONE-EPOCH, INV-NO-LOSS, INV-AUTHZ
- **Notes:** GAP: there is no rule for a device already on an epoch whose genesis later stops counting. The "competing rotations" rules (winner by lowest write ID; holders of both keys forward the losing channel's content) are written for two valid geneses. Moving back to epoch 3 would break monotone epochs, and nothing says "bread", written into M's channel, is forwarded. The same applies to a genesis written by the loser of a hand-over race (SH-027) and to a genesis that pinned a voided admission.

### SH-022 A reader writes
- **Kind:** adversarial
- **Design:** §8.3, §8.2, §10.7, §11.6
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner, A is a writer, and C and M are readers.
- **Steps:**
  1. On `C.phone` (an honest client), Carla tries to add an item.
  2. `M.phone signs` a new record "free beer" with epoch stamp 2, and publishes it to the epoch-2 channel.
  3. `M.phone signs` a `_carried` write on an existing record, naming an epoch-1 write.
  4. `M.phone signs` `member:<C>` = `removed` and an `invite:I`.
  5. `J.phone rotates House`; all devices sync.
- **Expect:**
  - `C.phone` shows House read-only, with no add control, and appends nothing.
  - No honest device shows "free beer" in epoch 2 or 3. No `_carried` list signed by an honest device names it, and no honest device forwards it.
  - M's writes from steps 3 and 4 don't count. Carla is still an active reader, and I isn't an open invite.
  - After step 5, every honest device has either applied "free beer" or dropped it, and the harness checks it was not applied.
- **Invariants:** INV-AUTHZ, INV-NO-HOLD-FOREVER
- **Notes:** GAP: §8.3 holds a write "whose author isn't authorized by the log a device has seen so far", and drops only writes that "can never be valid". M's epoch-2 write could still become valid while epoch 2 is current (a promotion that builds on the epoch-2 genesis would make M write-enabled). Once epoch 2 is superseded it never can, but the design doesn't say a held write is dropped then. Meanwhile §10.7 counts held writes when judging domination, so M's messages are never garbage-collected, and it is unclear whether honest devices republish held writes. INV-NO-HOLD-FOREVER can't be decided from the text.

### SH-023 Someone photographs the kitchen QR
- **Kind:** adversarial
- **Design:** §11.1 (`maxUses`, photographed QR), §11.2, §11.3, §13.2
- **Binding:** any
- **Setup:** J owns House at epoch 1. A is on the hub with an identity. E stands behind A in the kitchen.
- **Steps:**
  1. `J.phone invites qr to House as writer`, meant for Aimee.
  2. E photographs the QR, creates an identity on the hub with display name "Aimee", and posts a join request with it.
  3. `A.phone joins via` the QR 30 seconds later.
  4. `J.phone syncs`; `wait 10 minutes`; `A.phone syncs`.
  5. J notices and `J.phone removes` E's user; all devices sync.
  6. Variant: `J.mac` sees A's request while `J.phone` sees E's.
- **Expect:**
  - The roster holds E's uid as `{writer, active, name: "Aimee", via: <id>}`, and E's device decrypts epoch 1.
  - Aimee's request is refused because the use is spent. `A.phone` shows a named message saying so, rather than nothing followed by "expired".
  - J's members screen shows one "Aimee", who is E.
  - After step 5, a rotation follows; E can't decrypt the new epoch but keeps the hub credential.
  - Variant: both are admitted, giving two members with `via` = id, which is accepted by §11.1.
- **Invariants:** INV-CUT, INV-UX
- **Notes:** Step 2 is outside the step vocabulary, and E acquiring an identity goes beyond E's definition (raw hub access only).
  GAP: a request refused because uses ran out gets no decline (§11.3 sends declines only when a user taps Decline), and an auto-approve joiner sees no waiting screen, so Aimee sees nothing until the code expires. Together with unsigned, claimed names (§11.3), J's members screen looks correct while the impostor is in and his wife is out. The design gives J no signal that anything went wrong.

### SH-024 Eve replays old grants
- **Kind:** adversarial
- **Design:** §11.4, §6.3, §11.6, §3.2
- **Binding:** any
- **Setup:** House is at epoch 5. A is a writer who joined via link I1. E captured A's epoch-3 and epoch-5 grant messages before they were destroyed.
- **Steps:**
  1. `hub: replay` A's epoch-3 grant; `A.phone syncs`; `A.mac syncs`.
  2. `A.phone leaves House`; `J.phone syncs`, which rotates to epoch 6.
  3. `wait 31 days`.
  4. `J.phone invites link to House as writer` (I2); `A.phone joins via` I2. J doesn't approve yet.
  5. `hub: replay` A's epoch-3 and epoch-5 grants; `A.phone syncs`.
- **Expect:**
  - After step 1, A's devices stay on epoch 5, A's home has `key.n: 5`, and the replayed grant is destroyed and logged.
  - After step 5, neither grant is accepted (the log shows `via` I1, not I2). A's home keeps `state: requested`, and `A.phone` shows "Waiting for Jesper to approve".
  - After step 5, A's inbox holds neither replayed grant once `A.phone` has synced.
- **Invariants:** INV-MONOTONE-EPOCH, INV-NO-HOLD-FOREVER
- **Notes:** GAP: after 30 days the epoch-3 and epoch-5 channels hold only their `_epoch` records (§11.6), so §11.4's checks that "k opens the channel" and "the log in that channel shows ... this user as an active member" can't be evaluated. A grant is "kept until it can" be judged and destroyed only once it "certainly failed", so a replayed grant for a pruned epoch sits in the inbox forever and every device of the recipient re-checks it on each sync.

### SH-025 A member forges `isig` and rewrites a joiner's name
- **Kind:** adversarial
- **Design:** §11.2, §11.3, §5.6, §8.1
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner and M is a writer. There is a link invite I for Bo. As a member, M reads the roster and derives the invite channel key.
- **Steps:**
  1. `B.phone joins via` I with name "Bo".
  2. M decrypts Bo's request, re-encrypts it with `name: "Carla"` (leaving `sig` and `isig` untouched), appends it, and destroys the original.
  3. M posts a request for a sockpuppet uid whose `isig` is made with a random key.
  4. `J.phone syncs`.
- **Expect:**
  - The sockpuppet's request is never shown on `J.phone`, gets no roster write, and is removed from the invite channel or ignored.
  - `J.phone` shows the name Bo's own signed profile gives ("Bo"), not "Carla", and if J approves, `member:<B>.name` is "Bo".
- **Invariants:** INV-AUTHZ, INV-SIGNED
- **Notes:** Step 2 is outside the step vocabulary.
  GAP: a join request's `name` is covered by neither `sig` nor `isig`, and §11.3 shows "whatever the request claims", which is then copied into `member:X.name`. Any member, or anyone holding the link, can rename a pending joiner. The profile's `name` (§6.2) is signed and could be shown instead.
  GAP: §11.3 says what happens to approved and declined requests but not to invalid ones (bad `isig`, unknown uid, closed invite); they may sit in the channel indefinitely.

### SH-026 An invite link leaks publicly
- **Kind:** adversarial
- **Design:** §11.1, §11.2, §11.3, §5.6, §13.1, §3.3
- **Binding:** any
- **Setup:** J owns House at epoch 2 with A as a writer. `J.phone` creates a link invite I (writer, 7 days, auto-approve off, hint "House") for Bo, and Bo posts it in a public group chat.
- **Steps:**
  1. `B.phone joins via` I.
  2. E opens the link, takes the hub credential from the payload, creates an identity and posts a request named "Bo's friend".
  3. `J.phone syncs`; J declines E's request and approves Bo's.
  4. J closes I.
  5. E posts another request.
- **Expect:**
  - Everyone who saw the link learns "House", "Jesper" and the hub credential. E's credential still works after step 4.
  - After step 4, `invite:I` has `closed: true`, and E's step-5 request is neither shown nor approved.
  - `J.phone` tells J that everyone with the link has hub access, and offers the separate-token or credential-change path (§13.1).
  - E can't decrypt any House message.
- **Invariants:** INV-HUB-BLIND, INV-AUTHZ
- **Notes:** Steps 2 and 4 are outside the step vocabulary ("closes invite").
  GAP: §5.6 says "members can read join requests", but the invite channel key derives from `pub(VKs)`, which anyone holding `S` can derive too. Everyone the link leaks to can read every request: joiners' uids and names, and through the uid each joiner's profile (device names, display name). That is a privacy leak about the people who joined, not just the list.
  GAP: what devices do with requests on a closed invite, and who destroys the invite channel's leftovers after close or expiry, is unspecified.

### SH-027 Two owners' hand-overs race
- **Kind:** adversarial
- **Design:** §8.2 (one hand-over at a time, cuts), §9.4, §11.6
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner, and A, Bo and Carla are writers. `J.mac` is offline.
- **Steps:**
  1. `J.phone hands House to A`; `A.phone syncs`.
  2. `A.phone hands House to B`; `B.phone syncs`; `B.phone sets role C = admin`.
  3. `J.mac hands House to C` (offline, unaware of steps 1–2).
  4. `J.mac online`; all devices sync.
- **Expect:**
  - Of J's two concurrent `owner` writes, only the one with the lower write ID stands, the same one on every device (the harness computes it).
  - If J→A wins: Bo is owner and Carla is admin.
  - If J→C wins: Carla is owner. A→Bo and Bo's appointment of Carla don't stand, and Bo and A are writers.
  - Every device shows the same single owner.
  - Any genesis written by someone whose authority rested on the losing hand-over doesn't count.
- **Invariants:** INV-ONE-OWNER, INV-AUTHZ, INV-CONVERGE
- **Notes:** A and Bo may each see themselves as owner for minutes and then not; §13 has no message for "your ownership didn't take". If one of them rotated meanwhile, see SH-021's GAP.

### SH-028 Mallory forges a decline
- **Kind:** adversarial
- **Design:** §11.4 (decline), §11.2, §6.3
- **Binding:** any
- **Setup:** House is at epoch 1. J is owner and M is a writer. There is a link invite I for Bo.
- **Steps:**
  1. `B.phone joins via` I.
  2. M reads the invite channel (members can), learns Bo's uid and the invite ID, and seals a `declined` message to Bo's inbox, signed by `M.phone`.
  3. `B.phone syncs`.
  4. `J.phone approves B`; `B.phone syncs`.
- **Expect:**
  - `B.phone` ignores the decline because M is neither the owner nor an admin. Bo's home keeps `state: requested`.
  - After step 4, `B.phone` shows House.
- **Invariants:** INV-AUTHZ, INV-UX
- **Notes:** Step 2 is outside the step vocabulary.
  GAP: §11.4 gives the decline's format and signature but no acceptance rule. A joiner can't read the log before admission, so it can't check that the decliner is owner or admin, as it does for grants. As written, `B.phone` drops the pending membership and shows "Mallory declined your request", then refuses J's grant. Bo becomes an active roster member who never gets the key. Anyone who holds the link and makes an identity can do the same. One possible check is that the decliner equals the payload's `by`, but admins also decline.

### SH-029 A removed admin's invite admits her sockpuppet
- **Kind:** adversarial
- **Design:** §8.1, §8.2, §11.1, §11.3, §11.6
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner and M is admin.
- **Steps:**
  1. `M.phone invites link to House as writer` with `autoApprove: true`, expiry one year out and `maxUses: 50`, and keeps the link.
  2. `J.phone removes M`; all devices sync, which rotates to epoch 3.
  3. M, who still holds the hub credential, creates a second identity "Mel" and posts a request on her invite.
  4. `J.phone syncs`.
- **Expect:**
  - "Mel" is not admitted, and no device grants her the epoch-3 key.
  - M's invite is closed or no longer counts after step 2.
- **Invariants:** INV-CUT, INV-AUTHZ
- **Notes:** Step 3 is outside the step vocabulary.
  GAP: nothing closes the invites of a member who is removed or demoted. §11.6 step 1 writes only reductions and fences, and §11.3 checks only that the invite is open, unexpired and has uses left. M keeps `S`, and `J.phone` (an owner device following the invite channel) auto-approves her sockpuppet, which undoes the removal. No upper bound is set on an auto-approve invite's lifetime either; only the QR default is 10 minutes.

### SH-030 A departed member's join request is replayed
- **Kind:** adversarial
- **Design:** §11.2, §11.3, §11.4, §6.3, §11.6
- **Binding:** any
- **Setup:** House is at epoch 2. There is a link invite I (writer, 7 days, `maxUses: 5`). Bo joined via I on day 0. E captured Bo's kind-3 message.
- **Steps:**
  1. `wait 1 day`; `B.phone leaves House`; all devices sync, which rotates to epoch 3.
  2. `hub: replay` Bo's join request into I's channel.
  3. `J.phone syncs`. J sees "Bo wants to join House" and approves, thinking Bo changed his mind.
  4. `B.phone syncs`.
- **Expect:**
  - `J.phone` doesn't show the replayed request as new, or flags that Bo left.
  - `member:<B>` remains `left`.
  - No grant for House is pending in Bo's inbox.
- **Invariants:** INV-AUTHZ, INV-NO-HOLD-FOREVER
- **Notes:** GAP: a join request carries no nonce, time or epoch, so it stays valid for the life of the invite. As written, step 3 sets `member:<B>` active, and `B.phone` refuses the grant because Bo's home has `left`. Bo becomes a phantom: active in the roster, named in every later genesis's `to`, with a grant sealed to him at each rotation that nobody ever consumes. With an auto-approve invite this needs no click from J at all.

### SH-031 An admin withholds the owner's key after a rotation
- **Kind:** adversarial
- **Design:** §11.4 (key request), §11.6 (interrupted rotations), §8.2
- **Binding:** any
- **Setup:** House is at epoch 3. J is owner, M is admin, and A and C are writers.
- **Steps:**
  1. `M.phone removes C`; `M.phone rotates House`. The epoch-4 genesis has `to` = `[J, A, M]` with valid kids, and M sends grants to A and M only.
  2. `J.phone syncs`; `wait 10 minutes`. `J.phone` sends a key request to M, which `M.phone` ignores; `wait 10 minutes`.
  3. `J.phone` writes its own epoch-4 genesis with `replaces` naming M's.
  4. `M.phone` immediately writes another epoch-4 genesis that `replaces` J's.
  5. All devices sync.
- **Expect:**
  - After step 3, J's genesis is the live one. J, A and M get grants for it, and A moves to it.
  - "Carla" can't decrypt any epoch-4 channel.
  - M's step-4 genesis doesn't displace J's, and every device ends on one epoch-4 line.
  - Writes A made in M's epoch 4 appear in J's epoch 4.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-NO-LOSS
- **Notes:** GAP: `replaces` has no validity condition beyond the author being owner or admin. The 10 + 10 minutes of waiting can't be verified, and nothing says whether a replacing genesis can itself be replaced, so M can knock out J's genesis at once and repeat indefinitely. §11.6 also doesn't say who forwards content written into a replaced epoch; "its author, on returning, abandons it" covers only the author. `replaces` isn't in the genesis JSON either. The rotation scenarios may overlap; this one is kept for the "admin acts on the owner" angle.

---

## Fault

### SH-032 The approver crashes before granting, and a first grant is destroyed
- **Kind:** fault
- **Design:** §11.3, §11.4 (key request), §6.3
- **Binding:** any
- **Setup:** House is at epoch 2. J is owner and `J.phone` is his only online device. There is a link invite I.
- **Steps:**
  1. `B.phone joins via` I.
  2. `J.phone approves B`; `J.phone crashes after` appending `member:<B>` and before sending the grant.
  3. `J.phone launches`; `J.phone syncs`; `B.phone syncs`.
  4. Second run, without the crash: `J.phone approves B`; `hub: destroy grants for B` before `B.phone syncs`; `wait 1 day`; `B.phone syncs`.
- **Expect:**
  - Run 1: `J.phone` sends Bo a grant within one sync after relaunching. `B.phone` shows House, and the invite channel ends empty.
  - Run 2: Bo receives a replacement grant within a bounded time (the scenario uses the 10-minute key-request safety net as the bound), and `B.phone` shows House.
- **Invariants:** INV-NO-HOLD-FOREVER, INV-UX
- **Notes:** GAP: §11.3 doesn't say that a relaunched approver re-sends grants to members it admitted. The request is still in the channel, so re-processing it is plausible, but Bo is already a member and the design doesn't say whether to approve again or only re-grant.
  GAP: a first grant destroyed by a hub participant is never replaced. A key request (§11.4) is triggered when "the log shows its user as an active member", which a joiner can't read without the key, and the request was destroyed at approval. Bo waits until the invite expires while the roster lists him active, and gets a key only at the next rotation, and then only if his home `state` is still `requested` (SH-009).

### SH-033 The hub withholds the roster write from the joiner
- **Kind:** fault
- **Design:** §11.4, §9.4, §12.2
- **Binding:** any
- **Setup:** J owns House at epoch 1.
- **Steps:**
  1. `J.phone invites qr to House as writer`; `B.phone joins via` it; `J.phone syncs` (auto-approve).
  2. `hub: withhold` the House `_roster` message that holds `member:<B>` `from B.phone`.
  3. `B.phone syncs`.
  4. The withholding ends; `B.phone syncs`.
- **Expect:**
  - After step 3, `B.phone` keeps the grant in its inbox without destroying it, Bo's home keeps `state: requested`, and House isn't shown.
  - After step 4, `B.phone` accepts the grant, shows House, and its inbox is empty.
- **Invariants:** INV-NO-HOLD-FOREVER
- **Notes:** GAP: §11.4 keeps a grant whose genesis "the device hasn't seen" and destroys it "once it has certainly failed", but doesn't say whether a log that doesn't (yet) show the recipient as active is a certain failure. A device can't know whether a roster write is still on its way, so one SDK destroys the grant at step 3 and Bo is stranded (SH-032), while another keeps it forever, including grants that really are invalid.

### SH-034 A wrong clock decides whether an invite has expired
- **Kind:** fault
- **Design:** §11.1, §11.2, §11.3, §6.2, §13.3
- **Binding:** any
- **Setup:** J owns House. A is admin, and `A.mac` has the correct clock.
- **Steps:**
  1. `J.phone clock = -8 days`; `J.phone invites link to House as writer`.
  2. `B.phone joins via` the link (B.phone has the correct clock).
  3. `J.phone clock = 0`; `J.mac clock = +8 days`; `J.mac invites link to House as writer`; `C.phone joins via` it.
  4. `A.mac syncs`; `J.mac syncs`.
- **Expect:**
  - Step 2: `B.phone` shows "This code has expired. Ask for a new one." and appends nothing.
  - Step 4: Carla's request is approved or refused the same way on `A.mac` and `J.mac`.
- **Invariants:** INV-UX, INV-CONVERGE
- **Notes:** GAP: invite expiry is the only authorization check in §11 that depends on wall clocks, whereas §6.2 says "Timestamps play no part". The joiner and each approver compare `expires` with their own clocks, and no tolerance is given. In step 4, `A.mac` sees the invite as valid for 7 days while `J.mac`'s own clock makes it valid until it is actually 15 days old; any approver with a fast or slow clock admits requests the others refuse.

---

## UX

### SH-035 An invite for a different hub, and a per-person token
- **Kind:** ux
- **Design:** §11.2 step 1, §13.1, §13.3, §1.2
- **Binding:** any
- **Setup:** Bo's `B.phone` is on hub H2, his own household's. J's House is on hub H. Carla's `C.phone` is on H with her own per-person token.
- **Steps:**
  1. `J.phone invites link to House as writer`; `B.phone joins via` it.
  2. `J.phone invites link to House as writer` with "Use a separate hub token for this invite"; `C.phone joins via` it.
- **Expect:**
  - Step 1: `B.phone` shows "This invite is for a different sync hub. Liszto can only use one hub at a time." It appends nothing, writes nothing to Bo's home, and leaves its hub settings unchanged.
  - Step 2: `C.phone` treats H as its own hub, posts the request, and keeps its existing token.
- **Invariants:** INV-UX, INV-HUB-BLIND
- **Notes:** GAP: "a different hub" isn't defined. Descriptors for the same account with different tokens (per-person tokens, §13.1), or the same IMAP server reached under another host name or user alias, differ byte for byte. An SDK comparing descriptors refuses Carla; one that adopts the invite's token silently swaps her credential. The design should define hub identity (for example, the JMAP session URL plus account ID, or the IMAP host plus user) and say which credential is kept.

### SH-036 An expired code, and an invite with no hub on a phone with none
- **Kind:** ux
- **Design:** §11.1, §11.2, §13.3
- **Binding:** any
- **Setup:** `C.phone` is a fresh install with no hub and no identity.
- **Steps:**
  1. `J.phone invites qr to House as writer`; `wait 11 minutes`; `C.phone joins via` the QR.
  2. `J.phone invites link to House as writer` with the payload's `hub` omitted; `C.phone joins via` it.
- **Expect:**
  - Step 1: `C.phone` shows "This code has expired. Ask for a new one." It has configured no hub, created no identity and appended nothing.
  - Step 2: `C.phone` shows a named error asking Carla to connect a hub first, and appends nothing.
- **Invariants:** INV-UX
- **Notes:** GAP: §11.2 step 1 covers a joiner with no hub when the payload has one, and a joiner on a different hub. §11.1 makes `hub` optional, but a hubless device opening a hubless invite has no specified behaviour and no §13.3 message. Whether expiry is checked before step 1, and so whether an expired invite still configures a hub and creates an identity, is also unspecified.

### SH-037 Declined, with the list name hidden
- **Kind:** ux
- **Design:** §11.1 (`hint`), §11.2, §11.4 (decline), §6.3, §13.2, §13.3
- **Binding:** any
- **Setup:** J owns House. `J.mac invites link to House as writer` with the hint turned off.
- **Steps:**
  1. `B.phone joins via` the link.
  2. `J.mac declines B`.
  3. `B.phone syncs`.
  4. `B.phone joins via` the same link again.
- **Expect:**
  - After step 1, `B.phone` shows a waiting screen naming Jesper, and no list name appears anywhere on the screen.
  - After step 3, `B.phone` shows a decline message naming Jesper. Bo's inbox and the invite channel are empty, and every J device's pending list is empty.
  - Bo's home `membership:<House>` is in a state that won't accept a later grant.
  - After step 4, J's devices show a new request, or `B.phone` says Bo was declined; the scenario records which.
- **Invariants:** INV-UX, INV-AUTHZ
- **Notes:** GAP: §13.3's message is "Jesper declined your request to join House", and §13.2's waiting text names the list, but with `hint` off the joiner never learns the name, and no wording is given for that case.
  GAP: §11.4 says the joiner "drops the pending membership", but §6.3 says home records are never deleted and has no `declined` state, so the resulting `state` is unspecified. Left at `requested`, it would accept a later grant nobody intended.
  GAP: whether a declined joiner may re-request on the same invite is unspecified; nothing stops repeated requests.

### SH-038 A fresh phone scans the kitchen QR
- **Kind:** ux
- **Design:** §11.1, §11.2, §6.1, §13.1, §13.2
- **Binding:** any
- **Setup:** `A.phone` is a fresh install with no hub. J owns House.
- **Steps:**
  1. `J.phone invites qr to House as writer`.
  2. `A.phone joins via` the QR. `A.phone` shows the recovery key, and Aimee takes 12 minutes writing it down before confirming.
  3. `J.phone syncs`; `A.phone syncs`.
- **Expect:**
  - Aimee is a member: the roster holds `member:<A>` as writer, and `A.phone` shows House within one sync of confirming.
  - `A.phone` showed the recovery key exactly once, and the hub holds A's recovery pointer.
- **Invariants:** INV-UX
- **Notes:** GAP: the order of §11.2 step 2 (identity, including §6.1's recovery-key confirmation) and step 4 (posting the request) relative to the 10-minute QR isn't specified. If the client posts after confirmation, `J.phone` refuses the request as expired, and Aimee sees no waiting screen (auto-approve), then at most an expiry message. If it posts before, an auto-approve join completes while the key is still unconfirmed, which may be fine but should be stated.

### SH-039 The owner taps "Leave list"
- **Kind:** ux
- **Design:** §8.2 (`member:X` by X), §11.5, §7.4, §13.2
- **Binding:** any
- **Setup:** J owns House (shared with A) and Notes (private). M owns Ideas, shared with A as a writer.
- **Steps:**
  1. On `J.phone`, J taps "Leave list" on House.
  2. On `J.phone`, J taps "Leave list" on Notes.
  3. `M.phone signs` `member:<M>` with `status: left` on Ideas, without handing it over; all devices sync.
- **Expect:**
  - Step 1: the app asks J to hand House over first, and writes nothing until he does.
  - Step 2: the app offers "Delete list" instead, since there is nobody to hand Notes to, and writes nothing.
  - Step 3: M's `left` doesn't stand while M is owner. Ideas' roster still has M active and owner, and no rotation is triggered.
- **Invariants:** INV-ONE-OWNER, INV-UX
- **Notes:** GAP: §8.2 and §11.5 say "the owner hands ownership on before leaving", but no validity rule enforces it: "`member:X` by X: only to set `status: left`" accepts the owner's write. If it stands, Ideas has an owner who is not an active member and isn't in the next genesis's `to`: a second, undocumented way to reach "owner gone". §13.2 also doesn't say what a sole-member owner sees on "Leave list".
