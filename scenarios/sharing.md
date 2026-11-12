# Sharing

Scenarios for creating collections, invites and join requests, approvals and declines, grants and key requests, leaving, the owner-only `_meta` record, deleting, and copying a collection (DESIGN §7, §8, §11, §13).

One owner per collection for its whole life; members are writers or readers; only the owner changes sharing (§8.1). Invites are records in the owner's home, not in the roster (§11.1). Scenarios about admins, ranks and hand-over are retired (footer).

Unless a scenario says otherwise, "House" is a collection owned by J (devices `J.phone`, `J.mac`), every device is online, and `E1` is House's current epoch (see `rotation.md` for epoch labels).

---

## Real-world

### SH-001 Create a private list
- **Kind:** real-world
- **Design:** §7.1, §7.3, §7.4, §8.1, §5.6, §6.2
- **Binding:** any
- **Setup:** J has `J.phone` and `J.mac` on hub H with an identity.
- **Steps:**
  1. `J.phone creates collection Groceries`; it names it "Groceries".
  2. `J.phone adds milk to Groceries`.
  3. `J.phone syncs`; `J.mac syncs`.
- **Expect:**
  - The genesis is a `_roster` `s` assign with zero `scopeID`, zero `ref`, empty vectors, `n: 0`, J as the only member and owner, a key commitment `H("tandem/v1/commit" || K)`, and 16 random bytes. The collection ID is the first 16 bytes of its write ID; `E0` is the full write ID.
  - The channel tag is `b32(KDF(K(E0), E0, "tandem/v1/collection-tag", 20))`.
  - `_meta` (record ID `H("tandem/v1/named" || "_meta")[0..16]`, `_name` field) holds the name, written by J.
  - J's home holds `membership:<Groceries>`, `state: active`, `keys = [{epoch: E0, k}]`.
  - No message or header contains "Groceries", "milk" or J's user ID.
  - `J.mac` shows Groceries with "milk", author J; the members screen lists only J, as owner.
- **Invariants:** INV-HUB-BLIND, INV-SIGNED, INV-ONE-OWNER

### SH-002 Share the shopping list with Aimee in the kitchen
- **Kind:** real-world
- **Design:** §11.1, §11.2, §11.3, §11.4, §13.2
- **Binding:** any
- **Setup:** J owns Groceries (SH-001). A has an identity on H.
- **Steps:**
  1. `J.phone invites qr to Groceries as writer`.
  2. `A.phone joins via` the QR.
  3. All sync.
- **Expect:**
  - The invite is a record `invite:<id>` in J's home with `autoApprove: true`, `maxUses: 1`, `expires` 10 minutes ahead. No roster statement is made for it.
  - `A.phone` records `membership:<Groceries>`, `state: requested`, with the invite ID and `by.uid`, then posts a kind-3 request in the invite channel (tag from `KI = KDF(S, "", "tandem/v1/invite-key", 32)`). `sig` and `isig` verify.
  - `J.phone` (showing the QR) checks the invite is open, signs a same-epoch statement adding A as writer with her `uid`, `uka`, `name` and `via` = the invite ID, and, once it is on the hub, sends a grant sealed to A's `uka` and destroys the request. `J.mac` leaves the request alone.
  - `A.phone` checks the grant (membership requested; genesis signed by `by.uid`; admitting statement names this invite; `at` lists A; `epoch` is `at`'s epoch start and carries `commit(k)`), stores the key and shows Groceries within seconds.
  - No rotation.
- **Invariants:** INV-HUB-BLIND, INV-AUTHZ, INV-BYSTANDER

### SH-003 A link to Bo, opened two days later on a fresh phone
- **Kind:** real-world
- **Design:** §11.1, §11.2, §11.3, §6.1, §13.2
- **Binding:** any
- **Setup:** J owns House. `B.phone` has Liszto, no hub, no identity.
- **Steps:**
  1. `J.mac invites link to House as writer`; J sends it.
  2. Two days later `B.phone` opens it.
  3. The next day J opens Liszto on `J.phone` and taps Approve.
- **Expect:**
  - `B.phone` configures the hub from the payload, creates an identity, records the request in home and posts it. It shows "Waiting for Jesper to approve".
  - Both of J's devices show "Bo wants to join House" with Approve / Decline; only `J.phone` (where J tapped) signs.
  - B gets a grant and sees House.
- **Invariants:** INV-AUTHZ, INV-ONE-OWNER

### SH-004 Bo joins, then leaves a week later
- **Kind:** real-world
- **Design:** §11.5, §11.6, §13.2
- **Binding:** any
- **Setup:** SH-003 done.
- **Steps:**
  1. `wait 7 days`. `B.phone leaves House`.
  2. J confirms "Bo left House" on `J.mac`.
- **Expect:**
  - B's home membership becomes `left`; House disappears from B's devices; the notice is in channel `E1`.
  - Until step 2, B is still listed. Step 2 starts a new epoch without B.
  - B's items stay, attributed to B.
- **Invariants:** INV-CUT, INV-BYSTANDER

### SH-005 Make a shared list private again while Aimee is offline
- **Kind:** real-world
- **Design:** §7.4, §11.6
- **Binding:** any
- **Setup:** House shared with A (writer). `A.phone offline`.
- **Steps:**
  1. `J.mac removes A`.
  2. `A.phone online`; `A.phone syncs`.
- **Expect:**
  - As `rotation.md` RO-005. A sees "House is no longer shared with you" and her unsent edits are offered for rescue.
- **Invariants:** INV-CUT, INV-UX

### SH-006 Passing a list on means Aimee copies it
- **Kind:** real-world
- **Design:** §1.2, §8.1, §11.8
- **Binding:** any
- **Setup:** J owns House (A writer). J is stopping using Liszto and wants A to own it.
- **Steps:**
  1. A taps "Copy" on House.
  2. J deletes House (SH-008).
- **Expect:**
  - There is no hand-over: the copy is a new collection owned by A with the current items as A's writes.
  - The old House stays J's until he deletes it.
- **Invariants:** INV-ONE-OWNER

### SH-007 Make Bo a reader after he keeps adding junk
- **Kind:** real-world
- **Design:** §8.2, §8.3, §11.6
- **Binding:** any
- **Setup:** House: A, B writers.
- **Steps:**
  1. `J.mac` makes B a reader.
  2. `B.phone` (honest) tries to add an item.
- **Expect:**
  - A new epoch starts (lowering a role). B keeps reading.
  - `B.phone` doesn't offer editing; an attempt from a modified client is rejected everywhere.
- **Invariants:** INV-AUTHZ, INV-BYSTANDER

### SH-008 Delete a list while Aimee is away
- **Kind:** real-world
- **Design:** §11.9, §8.2
- **Binding:** any
- **Setup:** House shared with A. `A.phone offline`.
- **Steps:**
  1. `J.mac` deletes House.
  2. `wait 31 days`; `J.mac syncs`.
  3. `A.phone online`; `A.phone syncs`.
  4. J's buggy client signs a successor of the deleted statement.
- **Expect:**
  - The `deleted` statement is the last; devices stop showing House and keep local data read-only for 30 days.
  - After step 2, a device of J has destroyed House's data messages and kept the roster.
  - `A.phone` learns from the roster that House was deleted and removes it from view.
  - Step 4's statement is invalid and is the only successor: rejected, House stays deleted (not dead).
- **Invariants:** INV-UX, INV-GC-SAFE

---

## Edge

### SH-009 A request made in time, seen after the link expired
- **Kind:** edge
- **Design:** §11.1, §11.3, §11.4, §13.3
- **Binding:** any
- **Setup:** J sent Bo a 7-day link.
- **Steps:**
  1. Day 6: `B.phone` posts its request.
  2. Day 8: J opens Liszto.
- **Expect:**
  - J's devices don't admit Bo: the invite isn't open (expired).
  - Desired: J sees that Bo asked in time and is offered a new invite; Bo is told something rather than waiting forever.
- **Invariants:** INV-UX
- **Notes:** GAP (G-09): a request that the owner's devices refuse without a tap (expired, out of uses, or closed) gets no answer, and the design doesn't say what becomes of it on the hub or in the joiner's home. A request carries no time, so a request made in time and seen late is refused. The joiner's `membership` stays `requested` after the invite expires or a decline arrives; home has no `expired` or `declined` state, and records are never deleted (§6.2).

### SH-010 Two requests on a one-use link
- **Kind:** edge
- **Design:** §11.1 (`maxUses`), §11.3, §8.2
- **Binding:** any
- **Setup:** J's link to House has `maxUses: 1`. B and C both have it.
- **Steps:**
  1. B and C post requests.
  2. J approves B on `J.phone`; `J.mac` syncs; J tries to approve C on `J.mac`.
- **Expect:**
  - B is admitted. `J.mac` counts the invite's uses from the roster (`via`) and refuses C.
  - If J tapped both in the same moment on two devices before either synced, the two same-epoch statements kill House (§8.2); the harness records which variant happened.
- **Invariants:** INV-AUTHZ, INV-ONE-OWNER

### SH-012 Aimee, already a writer, taps a reader link
- **Kind:** edge
- **Design:** §11.2, §11.3
- **Binding:** any
- **Setup:** A is a writer of House. J sends a reader link to the family chat; A taps it.
- **Steps:**
  1. `A.phone joins via` the link.
- **Expect:**
  - J's device answers by sending A's grant again; no statement is signed and A stays a writer.
  - A's membership ends `active` on all her devices.
- **Invariants:** INV-BYSTANDER

### SH-013 Admitted just before a key change
- **Kind:** edge
- **Design:** §11.3, §11.4, §11.6
- **Binding:** any
- **Setup:** House: A writer, C writer.
- **Steps:**
  1. J admits B (same-epoch statement); the grant for `E1` is sent.
  2. Immediately, J removes C (`E2`).
- **Expect:**
  - B is listed in `E2` and gets a grant for `E2`; B can follow from either grant.
  - B never needs a key for an epoch before `E1`.
- **Invariants:** INV-AUTHZ, INV-CUT

### SH-014 The owner is gone for good
- **Kind:** edge
- **Design:** §11.8, §6.4
- **Binding:** any
- **Setup:** House: J owner, A and B writers. J loses every device and his backup key.
- **Steps:**
  1. A and B keep editing for a month.
  2. A wants to add C.
- **Expect:**
  - Editing keeps working: data writes need only a writer and a statement, not the owner.
  - Nobody can change House's sharing. A copies it into a new collection she owns and invites B and C.
- **Invariants:** INV-ONE-OWNER

### SH-017 Approve and decline race
- **Kind:** edge
- **Design:** §11.3, §11.4
- **Binding:** any
- **Setup:** B posted a link request.
- **Steps:**
  1. In the same second, J taps Approve on `J.phone` and Decline on `J.mac`.
- **Expect:**
  - B receives a decline hint and a valid grant; the grant counts (§11.4). B ends a member.
  - B's device shows the decline first, then the list.
- **Invariants:** INV-UX, INV-AUTHZ

### SH-040 An invite closes for good once someone it admitted is removed
- **Kind:** edge
- **Design:** §11.1, §11.3
- **Binding:** any
- **Setup:** J's 7-day writer link `I` (`maxUses: 5`) admitted B and C.
- **Steps:**
  1. J removes B.
  2. D posts a request on `I`.
  3. B is later let back in through a new invite `I2`.
  4. P1, a fresh identity B hands the link to, posts a request on `I`.
- **Expect:**
  - After step 1, `I` is closed: `J.mac` reads it from the roster history (B listed with `via = I`, then removed), not from home.
  - D and P1 are refused. B's return through `I2` doesn't reopen `I`.
  - The removing device also set `closed` in home; deleting that write (harness) changes nothing.
- **Invariants:** INV-AUTHZ

### SH-041 A former member is always asked about, even on a QR
- **Kind:** edge
- **Design:** §11.3, §13.2
- **Binding:** any
- **Setup:** B was removed from House. J shows a new auto-approve QR to a group in the kitchen, B among them.
- **Steps:**
  1. B scans the QR.
- **Expect:**
  - The showing device doesn't admit B; it asks "Bo was removed from House. Add him again?".
  - On Yes, B is added with `via` = the new invite and a grant follows.
- **Invariants:** INV-AUTHZ, INV-UX

### SH-042 A removed member asks for a key and gets the statements
- **Kind:** edge
- **Design:** §11.4
- **Binding:** any
- **Setup:** House moved `E1 → E2` (B removed) `→ E3` while `B.phone` was offline. E destroyed channel `E1`'s `_roster` copy of the announcement before B synced.
- **Steps:**
  1. `B.phone` comes online, learns some epoch exists whose key it lacks, waits 10 minutes and sends a key request with `have` = its last statement.
  2. `J.phone` answers.
- **Expect:**
  - B receives `{type: "roster", statements: [...]}` from `have` up to the statement that removed him, and no key.
  - The statements verify on their own; `B.phone` shows "House is no longer shared with you".
  - Two of J's devices answering is harmless.
- **Invariants:** INV-CUT, INV-UX

### SH-043 Key requests reach the owner by two routes
- **Kind:** edge
- **Design:** §11.4
- **Binding:** any
- **Setup:** House moved to `E2`; A's grant was destroyed.
- **Steps:**
  1. `A.phone` waits 10 minutes, then sends a key request to J's inbox and posts a notice in channel `E1`.
- **Expect:**
  - Either copy makes an owner device send A a grant for `E2`.
  - The owner's devices notice the channel-`E1` notice from the hub's one change feed, not by polling old channels.
- **Invariants:** INV-NO-HOLD-FOREVER

### SH-044 A leave notice from an earlier stay doesn't remove someone who came back
- **Kind:** edge
- **Design:** §11.5
- **Binding:** any
- **Setup:** B left House in `E1` and was removed (`E2`); later he was let back in during `E2`.
- **Steps:**
  1. E replays B's old leave notice into channel `E2`.
- **Expect:**
  - Ignored: its `at` is a statement of `E1`, not of the current epoch. B stays.
- **Invariants:** INV-AUTHZ

### SH-045 Renaming a list
- **Kind:** real-world
- **Design:** §7.3, §8.3, §13.2
- **Binding:** any
- **Setup:** House: A writer. `J.mac offline`.
- **Steps:**
  1. J renames House to "Home" on `J.mac`, offline.
  2. A (modified client) writes `_meta.name = "Mine"`.
  3. `J.mac online`; all sync.
- **Expect:**
  - Step 1 applies at once on `J.mac`, and on every device after step 3.
  - Step 2 is rejected everywhere: `_meta` accepts writes only when the named statement lists the author as owner.
- **Invariants:** INV-AUTHZ

### SH-046 What a first grant must prove
- **Kind:** adversarial
- **Design:** §11.4, §11.2
- **Binding:** any
- **Setup:** B has requested to join J's House via link `I`. M is another owner with her own collection "Fake".
- **Steps (separate runs):**
  1. M sends B a grant for "Fake" with `c` = House's ID.
  2. M sends B a grant for "Fake" with its own ID (B never asked to join it).
  3. J's device sends a grant whose `epoch` isn't the start of `at`'s epoch.
  4. J's device sends a grant whose `k` doesn't match the commitment.
  5. J's device sends a grant at a statement that doesn't list B.
  6. A valid grant, but the admitting statement names a different invite.
- **Expect:**
  - Every run is rejected; B's home gets no key.
  - Run 2 is ignored because B has no membership for that collection.
- **Invariants:** INV-AUTHZ

---

## Adversarial

### SH-019 Everyone holding an invite can read its join requests
- **Kind:** adversarial
- **Design:** §11.2, §5.6
- **Binding:** any
- **Setup:** J's link `I` to House was sent to B and C. M is a writer who never held `I`.
- **Steps:**
  1. B and C post requests.
- **Expect:**
  - C can read B's request (uid, `uka`, name); M can't; the hub can't.
  - C can post a request of her own on `I` but can't change B's name (B's `sig` covers it).
- **Invariants:** INV-HUB-BLIND
- **Notes:** The privacy claim is narrow by design (§11.2).

### SH-022 A reader writes
- **Kind:** adversarial
- **Design:** §8.3
- **Binding:** any
- **Setup:** C is a reader of House.
- **Steps:**
  1. `C.phone` (modified client) signs data writes naming the current statement.
- **Expect:**
  - Rejected on every device (the named statement lists C as reader); never displayed, not held.
- **Invariants:** INV-AUTHZ

### SH-023 Someone photographs the kitchen QR
- **Kind:** adversarial
- **Design:** §11.1, §11.3, §13.2
- **Binding:** any
- **Setup:** J shows A an auto-approve, one-use QR. E photographs it and has an identity (harness).
- **Steps:**
  1. E's device joins before A's request arrives, naming itself "Aimee".
  2. A scans it.
- **Expect:**
  - E is admitted; A is refused (no uses left).
  - J's members screen shows two "Aimee"s only if A came in some other way; J can remove the impostor, which starts a new epoch.
- **Invariants:** INV-AUTHZ
- **Notes:** Accepted by §11.1. A's device gets no answer (G-09). Harness: E having an identity goes beyond E's definition.

### SH-024 Eve replays old grants
- **Kind:** adversarial
- **Design:** §11.4
- **Binding:** any
- **Setup:** House moved `E1 → E2`; A follows `E2`. E captured A's `E1` grant.
- **Steps:**
  1. E replays it into A's inbox.
- **Expect:**
  - `A.phone` ignores it; an older grant never moves a device back. It may already have the key (`keys` only grows).
- **Invariants:** INV-MONOTONE-EPOCH

### SH-025 Tampering with a join request
- **Kind:** adversarial
- **Design:** §11.2
- **Binding:** any
- **Setup:** C holds link `I`. B posted a request.
- **Steps:**
  1. C reposts B's request with `name` changed to "Jesper's accountant".
  2. C posts a request with B's `uid` and C's own `uka`, signing `isig` with the invite key.
- **Expect:**
  - Step 1: `sig` fails; refused.
  - Step 2: `sig` fails (it must be by B's key over the `uka` and name); refused.
- **Invariants:** INV-AUTHZ, INV-SIGNED

### SH-026 An invite link leaks publicly
- **Kind:** adversarial
- **Design:** §11.1, §11.3, §3.3
- **Binding:** any
- **Setup:** J's House link (not auto-approve) is posted on a public forum.
- **Steps:**
  1. Twenty strangers post requests.
  2. J declines them all and closes the invite.
- **Expect:**
  - Nobody is admitted without a tap. J's devices show each request.
  - Everyone who saw the link now holds the hub credential (§11.1) and keeps it.
- **Invariants:** INV-AUTHZ
- **Notes:** See G-01: nothing says who destroys the invite channel's leftover requests after a close or decline.

### SH-028 Mallory forges a decline
- **Kind:** adversarial
- **Design:** §11.4
- **Binding:** any
- **Setup:** B's link request to House is pending.
- **Steps:**
  1. M sends B `{type: "declined", c, i}`.
  2. J approves B.
- **Expect:**
  - B's device tells B and keeps the request until the invite expires; the later valid grant counts.
- **Invariants:** INV-UX

### SH-029 A removed member's invite admits nobody new
- **Kind:** adversarial
- **Design:** §11.3
- **Binding:** any
- **Setup:** M came into House by auto-approve QR `I` (`maxUses: 3`) and was removed.
- **Steps:**
  1. M has a sockpuppet M2 scan a photo of `I` within its 10 minutes.
- **Expect:**
  - `I` is closed for good; M2 is refused even though `I` is auto-approve and has uses left.
- **Invariants:** INV-AUTHZ

### SH-030 A crash before closing the invite, and an old request replayed
- **Kind:** fault
- **Design:** §11.3, §11.6
- **Binding:** any
- **Setup:** B came into House by link `I`. B's original request is still in the invite channel (captured by E).
- **Steps:**
  1. `J.mac removes B` and crashes after the announcement, before setting `closed` on `I` in home.
  2. E reposts B's old request on `I`.
- **Expect:**
  - `J.phone` refuses it: `I` is closed by the roster's history, and B is a former member, who is never admitted without the owner being asked.
- **Invariants:** INV-AUTHZ

---

## Fault

### SH-032 The approver crashes before granting, and the first grant is destroyed
- **Kind:** fault
- **Design:** §11.3, §11.4
- **Binding:** any
- **Setup:** B posted a link request.
- **Steps:**
  1. `J.mac` admits B and crashes before the grant.
  2. `J.phone` syncs.
  3. Variant: `J.mac` sends the grant and E destroys it.
- **Expect:**
  - `J.phone` sees B's request still on the hub from someone already a member and sends the grant again.
  - Variant: B's device sends a key request after 10 minutes; any owner device answers.
- **Invariants:** INV-NO-HOLD-FOREVER

### SH-033 The hub withholds the roster from the joiner
- **Kind:** fault
- **Design:** §11.4
- **Binding:** any
- **Setup:** J admitted B and sent a grant.
- **Steps:**
  1. `hub: withhold` the admitting statement's message from `B.phone`.
  2. The hub stops withholding.
- **Expect:**
  - Step 1: B holds the grant (the roster through `at` isn't complete) and stores no key.
  - Step 2: B verifies and follows House.
- **Invariants:** INV-NO-HOLD-FOREVER

### SH-034 A wrong clock decides whether an invite has expired
- **Kind:** fault
- **Design:** §11.1, §11.3
- **Binding:** any
- **Setup:** J's 7-day link. `J.mac clock = +8 days`; `J.phone` is correct.
- **Steps:**
  1. Day 2: B posts a request.
- **Expect:**
  - `J.phone` shows it; `J.mac` treats the invite as expired.
  - Desired: the two devices agree.
- **Invariants:** INV-UX
- **Notes:** GAP (G-12, minor): invite expiry is the one admission check that uses a wall clock, the approving device's, with no tolerance. Two devices of one owner can disagree about whether an invite is open.

---

## UX

### SH-035 An invite for a different hub, and a per-person token
- **Kind:** ux
- **Design:** §11.2, §13.1, §13.3, §1.2
- **Binding:** any
- **Setup:** C is on hub H with the shared credential. J invites C to House using "a separate hub token for this invite".
- **Steps:**
  1. `C.phone joins via` the link.
- **Expect:**
  - C isn't told "This invite is for a different sync hub": the account is the same.
  - The harness records which credential C keeps.
- **Invariants:** INV-UX
- **Notes:** GAP (G-13): "a different hub" isn't defined. Descriptors for the same account differ by token or binding (§13.1, §4). The design should define hub identity and say which credential is kept. See `hub.md` HB-019.

### SH-036 An expired code, and an invite with no hub on a phone with none
- **Kind:** ux
- **Design:** §11.1, §11.2, §13.3
- **Binding:** any
- **Setup:** `B.phone` has no hub.
- **Steps:**
  1. B opens an invite whose `exp` has passed.
  2. B opens an invite without `hub`.
- **Expect:**
  - Step 1: "This code has expired. Ask for a new one.", before any hub is configured or identity created.
  - Step 2: a named message that a hub is needed first.
- **Invariants:** INV-UX
- **Notes:** GAP (G-14, minor): §11.2 doesn't cover a hubless invite on a hubless device, and doesn't say whether expiry is checked before step 1.

### SH-037 Declined, with the list name hidden
- **Kind:** ux
- **Design:** §11.1 (`hint`), §11.4, §13.3
- **Binding:** any
- **Setup:** J's link to House has `hint` turned off.
- **Steps:**
  1. B requests; J declines.
- **Expect:**
  - B sees that Jesper declined, without a list name.
- **Invariants:** INV-UX
- **Notes:** §13.3's wording names the list; the hint-off wording is the app's. What B's home `state` becomes is G-09.

### SH-038 A fresh phone scans the kitchen QR
- **Kind:** ux
- **Design:** §11.2, §6.1, §13.2
- **Binding:** any
- **Setup:** `A.phone` is fresh. J shows an auto-approve QR.
- **Steps:**
  1. `A.phone` scans it.
- **Expect:**
  - `A.phone` configures the hub, creates A's identity, posts the request within the QR's 10 minutes, and shows Groceries within seconds; the backup key is shown for saving, without blocking the join.
- **Invariants:** INV-UX

### SH-039 The owner taps "Leave list"
- **Kind:** ux
- **Design:** §11.5, §11.9
- **Binding:** any
- **Setup:** J owns House.
- **Steps:**
  1. J looks for "Leave list" on House.
- **Expect:**
  - Not offered; J is offered Delete instead (or keeping it).
- **Invariants:** INV-UX

---

Retired: SH-011, SH-015, SH-016, SH-018, SH-020, SH-021, SH-027, SH-031 (admins, ranks, least privilege and hand-over: withdrawn designs, §19).
