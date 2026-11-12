# Identity and devices

Scenarios for users and their devices: first run, the device chain, pairing, the per-user key and home, the recovery key and pointer, recovery, revocation and theft (DESIGN §3, §5.3, §5.5, §6, §9.4, §13).

**Standard setup**, used unless a scenario says otherwise: J has `J.phone` (his first device, which wrote the chain genesis), `J.mac` and `J.ipad`, at PUK generation `n`. A has `A.phone` and `A.mac`. J owns "Groceries" with A as writer; A owns "House" with J as writer. Each list is at some epoch `e`. Everything is synced on hub H. Device names beyond the actor table (`T.dev`, `T.dev2`, `J.new2`, `J.old`, `M2`…`M5`, `E.dev`) are harness extensions and are flagged where used.

---

### ID-001 First run on Jesper's phone
- **Kind:** real-world
- **Design:** §6.1, §6.2, §6.3, §6.4, §6.5, §5.3, §5.6, §13.1, §17
- **Binding:** any
- **Setup:** Empty hub H. `J.phone` has the app installed, no hub, no lists.
- **Steps:**
  1. `J.phone` connects H (JMAP: session URL and token; IMAP: host, user, app password).
  2. `J.phone` shows the recovery key; J confirms he has written it down.
  3. `J.phone syncs`.
- **Expect:**
  - The profile channel holds `_chain.genesis` with `first` = J.phone's signing key, `device:<J.phone>` (kind `device`, valid `pop`) and `device:<J.rk>` (kind `recovery`). The uid recomputes from `first` and `nonce`.
  - `_puk.manifest` has `n: 0`, boxes for exactly J.phone and J.rk, and a `uka` equal to the public half of `UKa(0)`. J.phone's box opens to a key matching `commit`.
  - A home generation-0 channel exists. Liszto's Inbox and Later exist as epoch-0 collections owned by J, and their IDs are in `settings:liszto`.
  - The hub holds exactly one kind-4 message, under the tag derived from `RS`. It decrypts with `RS` to `{ "uid": … }` naming J.
  - The key shown has 56 characters in 14 groups of 4, and its last 3 bytes match `H(RS)[0..3]`.
  - After step 2 the app offers no way to show the key again, and `RS` is not in J.phone's storage.
  - Every profile write has `epoch` 0 and the profile `scopeID`.
- **Invariants:** INV-HUB-BLIND, INV-SIGNED
- **Notes:** GAP: §6.1 step 2 lists the device's own entry before the recovery entry, but §6.2 requires the recovery entry to be the first device's "first write after the genesis". Reordering would fix that. The bigger problem is that "first write after the genesis" isn't defined in terms of `prev`, which is the only thing judging uses (see ID-026).

### ID-002 Add a Mac by scanning a pairing code
- **Kind:** real-world
- **Design:** §6.5, §6.2, §6.3, §5.5, §13.2
- **Binding:** any
- **Setup:** J has only `J.phone` (as after ID-001). He owns Groceries, with A as writer.
- **Steps:**
  1. `J.phone pairs J.mac`. J.mac names itself "Jesper's Mac", and J approves "Add 'Jesper's Mac'?".
  2. `J.mac syncs`.
  3. 30 s later, a client holding the session's `P` posts a second pairing request to the same channel.
- **Expect:**
  - `_chain` gains `device:<J.mac>`, signed by J.phone and named "Jesper's Mac". Its `pop` verifies over `"TANDEM-POP-V1" || uid || did || sign || agree`.
  - `_puk` gains `box:<J.mac>` with the current manifest's `n` and `commit`. No new manifest is written, and no collection changes epoch.
  - J.mac checks its box against `commit` and the derived `UKa` against `uka`. It shows Groceries and every other active membership within one sync.
  - Step 3 causes no prompt on J.phone, which stopped listening after one approval.
  - A.phone shows no change to Groceries' members.
- **Invariants:** INV-HUB-BLIND, INV-SIGNED
- **Notes:**
  - GAP: §5.5 lists pairing requests among the objects signed under their own `TANDEM-…-V1` prefix, but §6.5 defines the request as `{did, sign, agree, name, pop}` with no such signature. The `pop` covers neither `name` nor anything tying the request to this session. Anyone holding a later session's `P` can re-post a captured request into it under any name.
  - GAP: Nothing destroys pairing-channel messages. §11.3 destroys a join request once it has been handled; §6.5 has no equivalent. Every pairing leaves its requests (device names and public keys) on the hub, readable by anyone who saw the code.
  - GAP: `device:X` can be written only once ("no entry for X exists yet"), so a device's name is fixed at pairing. The Devices screen (§13.2) can't rename "iPad" to "Kitchen iPad", and no field exists for that.
  - Harness: step 3's injected request is outside the step vocabulary.

### ID-003 A new iPhone replaces the first device
- **Kind:** real-world
- **Design:** §6.2 (bootstrap rule), §6.3, §6.5, §6.6, §8.2, §11.6, §13.2, §13.3
- **Binding:** any
- **Setup:** Standard. J is trading in `J.phone`, the first device.
- **Steps:**
  1. `J.phone pairs J.new` ("Jesper's iPhone"); `J.new syncs`.
  2. `J.new revokes J.phone`.
  3. All of J's devices sync; `A.phone syncs`.
  4. `wait 31 days`; `J.new syncs`.
- **Expect:**
  - J's uid is unchanged, and `_chain.genesis` still names J.phone's key. J.phone is not active.
  - Manifest `n+1` builds on `revoke:<J.phone>` and has boxes for exactly J.mac, J.ipad, J.new and J.rk. Home `n+1` holds every membership.
  - Groceries moves to `e+1` (genesis by a J device) and House to `e+1` (genesis by an A device). Both carry `fence:<J.phone>`, whose `rev` is the revocation's write ID.
  - J's grant for House `e+1` is sealed to `UKa(n+1)`. No grant for any `e+1` is sealed to `UKa(n)`.
  - J.phone shows "This device was removed from your account." It can't decrypt `e+1` of either list.
  - After step 4 the hub holds no messages under home generation `n`'s tag.
  - A's House member list doesn't change.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-ONE-OWNER

### ID-004 New phone restored from a backup
- **Kind:** real-world
- **Design:** §5.3, §6.2, §6.5, §6.6, §13.2
- **Binding:** any
- **Setup:** Standard. J restores a new iPhone, `J.new`, from an iCloud backup of `J.phone`. J.phone is then wiped and traded in.
- **Steps:**
  1. `J.new launches`. App data was restored; the this-device-only Keychain items holding J.phone's keys were not.
  2. `J.new syncs`.
  3. `J.mac pairs J.new`.
- **Expect:**
  - Before step 3, J.new appends nothing to the hub under J.phone's device ID.
  - J.new tells J that it has to be added from another device or recovered (see GAP).
  - After step 3, J.new has a new device ID. J's Devices list shows both the old "Jesper's iPhone", still active, and the new one.
  - J.phone's entry stays active, and nothing rotates, until someone removes it.
- **Invariants:** INV-NO-LOSS, INV-SIGNED
- **Notes:** GAP: §5.3 says device keys aren't in backups, but not what a client does when it finds a replica, settings and perhaps a hub credential with no device keys. The design doesn't say whether it discards the replica, keeps its unsent writes to publish under new keys after pairing, or offers to remove the old entry (whose keys are now gone for good). Restoring from a backup is the usual way people move to a new iPhone. Harness: "restore from backup" is outside the step vocabulary.

### ID-005 Lost phone: the shared list re-keys in the background
- **Kind:** real-world
- **Design:** §6.6, §6.3, §8.2, §11.6, §13.2
- **Binding:** any
- **Setup:** Standard. `J.phone` is left in a taxi and nobody uses it. `J.phone offline`.
- **Steps:**
  1. `J.mac revokes J.phone`; `J.mac syncs`.
  2. `A.phone syncs`.
  3. `A.phone adds "Bulbs" to House`; `J.ipad syncs`.
  4. `J.phone online`; `J.phone syncs`.
- **Expect:**
  - After 1, the chain holds `revoke:<J.phone>` signed by J.mac. Manifest `n+1` builds on it, with boxes for exactly J.mac, J.ipad and J.rk. Groceries is at `e+1` with `fence:<J.phone>`.
  - After 2, House has exactly one valid genesis for `e+1` (by an A device) with `fence:<J.phone>`. J's `to` entry names the kid of `UKa(n+1)`, or is `null` until A.phone sees manifest `n+1`.
  - After 3, J.ipad shows "Bulbs", and J.mac, J.ipad and A.phone show identical House contents.
  - A sees no prompt or member change.
  - After 4, J.phone decrypts neither list's `e+1` and shows "This device was removed from your account."
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH
- **Notes:** House rotates only once an A device is online, because J is neither owner nor admin there. Until then the phone could still read House. §6.6 accepts that window.

### ID-006 Phone in a drawer for seven months, never removed
- **Kind:** real-world
- **Design:** §6.3, §6.6, §12.2, §12.3, §13.2, §13.3
- **Binding:** any
- **Setup:** Standard. `J.ipad` has two unsent edits to Groceries; then `J.ipad offline` (in a drawer).
- **Steps:**
  1. `wait 40 days`. `J.mac revokes J.phone` (lost), and all online devices sync.
  2. `A.phone deletes "Old lamp"` from House. After `wait 200 days`, an A device runs a maintenance rotation of House.
  3. `J.ipad online`; `J.ipad syncs`.
- **Expect:**
  - J.ipad opens its box in manifest `n+1`, written while it was away, and reads home `n+1`. Home `n` is gone from the hub.
  - J.ipad shows "This device was offline for a long time. Reloading from the hub; nothing you wrote here is lost."
  - J.ipad publishes nothing to Groceries or House before its resync, and never re-publishes "Old lamp".
  - Its two unsent edits reach the app as rescued data and are not published as writes.
  - J.mac's Devices list showed J.ipad as active the whole time.
- **Invariants:** INV-NO-LOSS, INV-GC-SAFE
- **Notes:** GAP: §13.2 shows each device "with name and last activity", but the protocol has no source for it. A device that only reads leaves no trace, and a write's time comes from the writer's own clock. During the drawer months J.mac can show at most the time J.ipad claimed for its last write, so a dead device looks the same as one that is quietly reading.

### ID-007 Removed as lost, then found with unsent edits
- **Kind:** real-world
- **Design:** §6.6, §6.2, §3.3, §8.3, §11.6, §13.3
- **Binding:** any
- **Setup:** Standard. `J.ipad offline`. While offline, J adds "Passport" to Groceries on it and edits `Milk.notes`.
- **Steps:**
  1. `J.mac revokes J.ipad` (J believes it is lost). J.mac and A.phone sync.
  2. `wait 10 days`.
  3. `J.ipad online`; `J.ipad syncs`.
  4. `J.mac pairs J.ipad` again.
- **Expect:**
  - After 3, J.ipad shows "This device was removed from your account." Its lists stay visible and read-only, and it appends nothing to Groceries `e` or `e+1`.
  - After 3, neither "Passport" nor the note appears on J.mac or A.phone.
  - In step 4, J.ipad comes back under a new device ID, because revocation is permanent. The old entry stays revoked.
  - After 4, either "Passport" and the note appear on J.mac, signed by the new device ID, or the app has told J they were dropped (see GAP).
- **Invariants:** INV-NO-LOSS, INV-CUT
- **Notes:** GAP: When a member is removed, §11.6 hands their unsent edits to the app. For a removed device, §13.3 only keeps data read-only "until the user signs in again". The design doesn't say whether re-pairing offers the unsent edits for publishing under the new keys, so INV-NO-LOSS has no defined outcome here. Nor does it forbid a revoked device from publishing its queue to the old epoch, whose key it still has (see ID-023 for why that matters).

### ID-008 Selling the old iPad
- **Kind:** real-world
- **Design:** §6.2, §6.6, §11.6, §13.2, §13.3
- **Binding:** any
- **Setup:** Standard.
- **Steps:**
  1. On `J.ipad`, J opens Settings → Devices.
  2. `J.mac revokes J.ipad`.
  3. `J.ipad syncs`.
  4. J erases the app's data on J.ipad.
- **Expect:**
  - In step 1, J.ipad's own row has no "Remove" (a device can't revoke itself).
  - After 2, the PUK moves to `n+1` with no box for J.ipad. Groceries and House each move to `e+1` with `fence:<J.ipad>`; House is rotated by an A device.
  - After 3, J.ipad shows "This device was removed from your account." and shows the lists read-only.
  - After 4, J.ipad holds no device keys, no PUK, no epoch keys and no hub credential.
- **Invariants:** INV-CUT
- **Notes:** GAP: Only another active device can remove a device. C has one phone; if she sells it before setting up the next, she can remove it only by recovering. The design has no "remove this device" or sign-out on the device itself. Until it is erased, a removed device also keeps J's lists and the hub credential (§13.3), whereas a removed member's device deletes its replica (§11.6). The design doesn't say whether erasing also forgets the hub credential. Harness: step 4 is outside the step vocabulary.

### ID-009 Handing the old iPad to Aimee
- **Kind:** real-world
- **Design:** §6.5, §6.6, §13.1, §13.3
- **Binding:** any
- **Setup:** Standard. J also has a private list, "Gifts for Aimee", with himself as the only member.
- **Steps:**
  1. `J.mac revokes J.ipad`.
  2. `J.ipad syncs`.
  3. A signs in on the iPad: `A.phone pairs J.ipad` (J.ipad scans A.phone's code).
- **Expect:**
  - After 3, the iPad has a new device ID in A's chain and opens only A's home.
  - The iPad shows A's lists and none of J's. "Gifts for Aimee" is not visible on it.
  - No collection owned by A on the hub contains records from "Gifts for Aimee", and A's home has no membership for it.
- **Invariants:** INV-HUB-BLIND, INV-CUT
- **Notes:** GAP: §13.3 keeps a removed device's data "until the user signs in again", without saying as whom or what happens to the previous identity's replica. On connecting, §13.1 uploads local collections as the connecting user's own. If pairing does the same, J's private lists become A's. The design should say that a device leaving one identity wipes that identity's replica before it joins another.

### ID-010 Lost everything, recovery key in the drawer
- **Kind:** real-world
- **Design:** §6.5, §6.2, §6.3, §6.6, §11.6, §13.1, §13.2
- **Binding:** any
- **Setup:** Standard. J's three devices are destroyed in a house fire and will never come online. J has the paper with `J.rk`.
- **Steps:**
  1. `J.new` connects H by scanning A.phone's hub QR.
  2. J chooses "I have a recovery key"; `J.new recovers with J.rk`.
  3. `J.new syncs`; `A.phone syncs`; `J.new syncs`.
- **Expect:**
  - J.new finds J's uid through the recovery pointer and runs as J, with the same uid.
  - The chain holds `revoke:` writes for J.phone, J.mac and J.ipad, signed by the recovery key, and a `device:<J.new>` signed by it that builds on all three.
  - Manifest `n+1` builds on the three revocations and has boxes for exactly J.new and J.rk. Home `n+1` holds every membership J had.
  - Groceries is rotated by J.new and House by an A device, each with fences for all three devices. J's grant for House is sealed to `UKa(n+1)`.
  - J.new shows Groceries, House, Inbox and Later. The shared lists match A.phone's contents, and A's House member list shows J once, unchanged.
  - With their captured keys, none of the three old devices can decrypt any new epoch.
- **Invariants:** INV-CUT, INV-ONE-OWNER

### ID-011 Lost everything, including the recovery key
- **Kind:** real-world
- **Design:** §6.5, §8.2, §6.6, §11.6, §17, §19
- **Binding:** any
- **Setup:** Standard. J loses all three devices and the recovery key.
- **Steps:**
  1. `J.new` connects H. With no recovery key, it sets up a new identity, J2 (first run).
  2. `A.phone invites (link) to House as writer`; `J.new joins via` it; `A.phone approves J2`.
  3. `A.phone removes J` (the old identity) from House.
  4. Later A.phone is lost: `A.mac revokes A.phone`.
- **Expect:**
  - J2 has a new uid and recovery key, and nothing links it to J.
  - J2 sees House after step 2. House rotates without J after step 3.
  - Groceries (owner J) never moves past its current epoch. No device rotates it, invites J2 to it, or changes its roles.
  - A can still write to Groceries and duplicate it into a new list.
  - After step 4, Groceries doesn't rotate, and A.phone's captured keys still decrypt it.
- **Invariants:** INV-ONE-OWNER (owner-gone state). INV-CUT fails for Groceries after step 4; that follows from §8.2 (no owner or admin left to rotate).
- **Notes:** GAP: Nothing tells A that Groceries' owner is gone for good; to A's devices he looks like an owner on holiday. The app can't prompt her to duplicate the list, and she isn't told that her lost phone will keep reading Groceries.

### ID-012 Replacing a recovery key that got wet
- **Kind:** real-world
- **Design:** §6.5, §6.2, §6.3, §6.6, §8.2, §11.6, §13.2
- **Binding:** any
- **Setup:** Standard. The paper with `J.rk` is barely legible, but J reads it correctly.
- **Steps:**
  1. `J.mac replaces recovery key`: J types `J.rk`, the app shows `J.rk2`, and J confirms.
  2. All of J's devices and `A.phone` sync.
  3. Later J loses all three devices; `J.new recovers with J.rk2`.
- **Expect:**
  - After 1, `_chain` has a new `device:<J.rk2>` of kind `recovery`, signed by the old recovery key and building on the chain head.
  - Manifest `n+1` builds on that write, with boxes for J.phone, J.mac, J.ipad and J.rk2, and none for J.rk.
  - The hub holds a pointer under the tag derived from `RS2` and none under the old tag.
  - Groceries and House move to `e+1` with `fence:<J.rk>`, House by an A device. J's grants are sealed to `UKa(n+1)`.
  - The old `RS` opens no box in manifest `n+1`.
  - After 3, J.new finds J's uid with `J.rk2` and recovers as in ID-010.
- **Invariants:** INV-CUT, INV-HUB-BLIND
- **Notes:** GAP: §6.5 says devices keep a copy of the pointer message and re-upload it if it is destroyed. But only the device that wrote a pointer knows its tag, which is derived from a secret it then erases. Here J.phone and J.ipad have no copy and no way to get one, and the same holds for every device paired after first run (ID-001). Nothing puts the pointer, or its tag, in home. If J.mac is lost and the pointer destroyed, recovery fails (ID-030).

### ID-013 An old recovery key typed on a new phone
- **Kind:** real-world
- **Design:** §6.5, §6.2, §9.4, §13.3
- **Binding:** any
- **Setup:** As after ID-012 step 2: `J.rk` was replaced by `J.rk2`, and J.phone and J.mac are active. `hub: replay <J.rk's old pointer>`, so the old pointer is back on the hub.
- **Steps:**
  1. J finds the old paper and, instead of pairing, runs `J.new recovers with J.rk`.
  2. All of J's devices sync.
- **Expect:**
  - J.new finds J's uid and sees in the chain that J.rk was replaced. It stops before signing anything and tells J the key is no longer valid (see GAP).
  - J.phone and J.mac stay active, and no new write signed by the old key reaches the hub.
  - J.new is not in J's chain.
- **Invariants:** INV-UX
- **Notes:** GAP: §6.5's recovery steps never check that the typed key is the current recovery key. A client following them literally signs revocations and a device entry with the old key. If those writes build on the replacement, they don't stand, but log writes are never discarded, so they stay in J's log for good. If they build on an older head (say the hub withholds the replacement from J.new), the revocations stand as cuts (§9.4 rule 1) and remove J.phone and J.mac, while J.new's entry doesn't count. §13.3 also has no message for a replaced key.

### ID-014 New Mac used offline for a week before pairing
- **Kind:** real-world
- **Design:** §6.5, §13.1, §17
- **Binding:** any
- **Setup:** Standard. A week ago J installed the app on `J.new`, a Mac, and used it without a hub. It has a list "Work" with 12 items, plus 3 items in its local Inbox.
- **Steps:**
  1. `J.phone pairs J.new`.
  2. `J.new syncs`; `J.phone syncs`.
- **Expect:**
  - J has one Inbox and one Later collection; the IDs in `settings:liszto` are unchanged. J.new's 3 Inbox items are in that Inbox.
  - "Work" is a collection owned by J with 12 items, and J.phone shows it.
  - No second identity is created. J.new's device entry is signed by J.phone.
- **Invariants:** INV-NO-LOSS
- **Notes:** GAP: §13.1 uploads local data when a device sets up a new identity on connecting, but §6.5 says nothing about local data when pairing. It is unspecified whether J.new uploads "Work", and how its local fixed collections (Liszto's Inbox and Later, §17) merge into the ones named in J's home instead of becoming duplicates.

### ID-015 A second device connected with the hub QR instead of pairing
- **Kind:** ux
- **Design:** §13.1, §6.1, §6.5
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install.
- **Steps:**
  1. On `J.phone`, J opens Settings → Sync and shows the hub QR.
  2. `J.ipad` connects H by scanning it.
- **Expect:**
  - Before creating an identity, J.ipad asks whether J already uses the app on another device, and offers to add this device to that account (see GAP).
  - If J goes ahead anyway, J.ipad creates a new identity with its own uid and recovery key. It shows none of J's lists, and J.phone's Devices list doesn't show it.
- **Invariants:** INV-UX
- **Notes:**
  - GAP: Under §6.1 and §13.1, any device without an identity creates one when it connects, and the hub QR is described as the way "a new or recovering device" gets access. That leads J straight to a second identity. Two identities of one person can never be merged, and lists created under the second stay owned by it.
  - GAP: §6.5 doesn't say what a device that already has an identity does when it scans someone else's pairing code, for example A.phone scanning J's: refuse, or leave A's identity.

### ID-016 Two devices revoke the lost phone at once
- **Kind:** edge
- **Design:** §6.2, §6.3, §6.6, §9.4, §13.3
- **Binding:** any
- **Setup:** Standard. At 09:00 `J.phone` paired `J.new`. `J.mac` synced at 09:01 and holds `device:<J.new>`; `J.ipad` last synced at 08:00. At 09:05 J.phone is lost.
- **Steps:**
  1. `J.ipad offline`; `J.ipad revokes J.phone`.
  2. `J.mac revokes J.phone`.
  3. `J.ipad online`; all of J's devices sync; `A.phone syncs`.
- **Expect:**
  - Both `revoke:<J.phone>` writes stand on every device.
  - `device:<J.new>` doesn't count, because J.ipad's revocation doesn't build on it.
  - The current manifest builds on both revocations, with boxes for exactly J.mac, J.ipad and J.rk. A manifest that builds on only one revocation isn't current.
  - The next geneses of Groceries and House fence both J.phone and J.new.
  - J.new stops syncing J's data and tells J it was removed (see GAP). J.mac's Devices list no longer shows it as active.
- **Invariants:** INV-CUT
- **Notes:** Two honest revocations together remove a device that neither revoker chose to remove. That is the rule working, but it will surprise users.
  GAP: No revocation names J.new. §13.3's "This device was removed" message covers a device that is named. The design doesn't say that a device whose entry merely stops counting must treat itself as removed. It also doesn't say that such a device must generate new keys before it can be paired again, which it must because `device:X` requires "no entry for X exists yet".

### ID-017 Pairing during a concurrent PUK rotation
- **Kind:** edge
- **Design:** §6.3, §6.5, §6.6
- **Binding:** any
- **Setup:** Standard.
- **Steps:**
  1. `J.mac offline`. `J.mac revokes J.phone` (lost), writing the revocation and manifest `n+1` locally.
  2. At the same time, `J.ipad pairs J.new`, writing `device:<J.new>` and `box:<J.new>` for generation `n`.
  3. `J.mac online`; all of J's devices sync.
  4. `wait 31 days`; `J.new syncs`.
- **Expect:**
  - `device:<J.new>` counts. Manifest `n+1` is current and has no box for J.new; that is valid, because J.new isn't in its past.
  - Within one more sync, an active device writes `box:<J.new>` for generation `n+1`, and J.new reads home `n+1` (see GAP).
  - Once J.new has seen manifest `n+1`, it writes nothing more to home `n`.
  - After 4, J.new still shows all of J's lists.
- **Invariants:** INV-NO-LOSS
- **Notes:** GAP: No rule makes any device box an active device that has no box in the current generation. §6.6 step 2 boxes only devices in the rotation's past, and pairing boxes only the generation the approver knows. J.new is left with `PUK(n)` only. Home `n` is destroyed after 30 days, and J.new loses every membership. The same happens if the approving device crashes after writing `device:X` and before writing `box:X`.

### ID-018 Two devices rotate the PUK at once
- **Kind:** edge
- **Design:** §6.3, §6.6, §11.4, §11.6, §9.4
- **Binding:** any
- **Setup:** Standard, plus J's old laptop `J.old`, also an active device of J's (harness extension).
- **Steps:**
  1. `J.mac offline`; `J.ipad offline`.
  2. `J.mac revokes J.phone`, writing manifest `n+1` with boxes for J.mac, J.ipad, J.old and J.rk, and rotates Groceries to `e+1`.
  3. `J.ipad revokes J.old`, writing manifest `n+1` with boxes for J.phone, J.mac, J.ipad and J.rk.
  4. Both come online; all devices sync.
- **Expect:**
  - Both revocations stand. Neither `n+1` manifest builds on both, so neither is current, and a rotation to `n+2` follows.
  - The final current manifest builds on both revocations, with boxes for exactly J.mac, J.ipad and J.rk. Home `n+2` holds the union of both `n+1` homes.
  - Groceries rotates again to `e+2`, fencing J.phone and J.old, because `e+1` didn't fence J.old.
  - Neither J.phone nor J.old can decrypt Groceries `e+2` or home `n+2`.
  - Within 30 days, the hub holds no messages under either `n+1` home tag.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH
- **Notes:**
  - GAP: §6.3 says that of two manifests of one generation, the one with the lowest write ID "wins" and the losing home is copied into it. But its definition of the current manifest makes neither of them current here. The design doesn't say which home devices write to in the meantime, or which `uka` others seal to (§11.4) while no manifest is current.
  - GAP: §11.6 step 4 holds back J's own grant until a manifest builds on the cut. But J.mac, the rotating device, records `K(Groceries,e+1)` in its own home `n+1`, and J.old has a box for that home. Nothing prevents this; only the follow-up rotation to `e+2` repairs it.
  - GAP: Nobody is told to destroy the losing home channel. For collections, "the loser's author destroys its channel" (§11.6); §6.3 has no equivalent.

### ID-019 Recovering on two new devices at once
- **Kind:** edge
- **Design:** §6.5, §6.2, §6.3, §11.6
- **Binding:** any
- **Setup:** Standard. J has lost all three devices. He has `J.rk`, a new iPhone `J.new` and a new Mac `J.new2` (harness extension).
- **Steps:**
  1. `net: latency 5000` ms for both. `J.new recovers with J.rk` and `J.new2 recovers with J.rk` within the same second.
  2. `net: reset`; both sync; `A.phone syncs`; both sync again.
- **Expect:**
  - Each device's revocations of J.phone, J.mac and J.ipad stand (six in all; the duplicates do no harm). Both `device:<J.new>` and `device:<J.new2>` count.
  - The final current manifest builds on all six revocations and has boxes for exactly J.new, J.new2 and J.rk.
  - Both devices show all of J's lists, and each lists the other under Devices.
  - On every device, Groceries' log shows one winning genesis per epoch number, and both devices follow the same epoch.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH
- **Notes:** Neither device revokes the other, because each revocation can name only devices in its own past. Expect up to two PUK generations and two rotations per list.

### ID-020 Pairing code photographed during a video call
- **Kind:** adversarial
- **Design:** §6.5, §3.1, §13.2, §19 (pairing always asks)
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install. E holds the hub credential, and J is sharing his screen with E on a call.
- **Steps:**
  1. `J.mac` shows a pairing code, and E captures it from the shared screen.
  2. 2 s later, E's client posts a pairing request named "Jesper's iPad" (harness: `E.dev`).
  3. 10 s later, `J.ipad` scans the code and posts its own request, also named "Jesper's iPad".
  4. J approves the prompt J.mac shows.
- **Expect** (the design as written):
  - J.mac shows "Add 'Jesper's iPad'?" with nothing to tell the two requests apart.
  - If the approved request is E's, E.dev joins J's chain, opens J's PUK box and home, and follows every list.
  - The other request gets neither a prompt nor an answer.
  - J.mac's Devices list shows one "Jesper's iPad".
  - After J revokes E.dev, the PUK and every list rotate, and E.dev can't decrypt the new epochs.
- **Invariants:** INV-HUB-BLIND; INV-CUT after the revocation
- **Notes:** GAP: §6.5 doesn't say what the showing device does when a second request arrives in the same session. A second request is the one reliable sign that the code was captured; the device could show both, warn, or refuse both. The design also gives J.ipad no outcome: nothing tells it that it wasn't added. Harness: E's request is outside the step vocabulary.

### ID-021 Theft: removing the phone as "lost since" two hours ago
- **Kind:** adversarial
- **Design:** §6.6, §6.2, §8.3, §9.3, §11.6, §13.2
- **Binding:** any
- **Setup:** Standard. Groceries has an item whose title J set to "Milk", and an item "Eggs". `J.mac` is online throughout.
- **Steps:**
  1. 14:00: J.phone is stolen unlocked.
  2. 14:10: `T(J.phone) pairs T.dev` (harness: a device the thief controls).
  3. 14:20: `T(J.phone) sets Milk.title = "xxx"`, which supersedes J's write. `T(J.phone) deletes Eggs`. `T.dev adds "Spam" to Groceries`.
  4. `J.mac syncs`, merging all of it.
  5. 16:00: `J.mac revokes J.phone lost since 13:55`; all devices sync.
- **Expect:**
  - Before signing, J.mac lists T.dev as a device the lost phone added after 13:55.
  - The past of `revoke:<J.phone>` leaves out `device:<T.dev>`, so T.dev counts on no device. Groceries' `e+1` genesis fences J.phone and T.dev.
  - In Groceries `e+1`, Eggs isn't deleted and Milk's title isn't "xxx".
  - Milk's title is "Milk" again (see GAP).
  - "Spam" is absent (see GAP).
  - The app is told which of the lost phone's edits were dropped (see GAP).
- **Invariants:** INV-CUT, INV-AUTHZ
- **Notes:**
  - GAP: When J.mac merged "xxx", it discarded J's superseded "Milk", because only maximal writes are kept (§9.3). Leaving "xxx" out of the snapshot therefore leaves the field with no value, not with the old one. The design neither keeps superseded writes for this case nor tells the app what was dropped.
  - GAP: §8.3's snapshot rule leaves out "a lost device's" writes after the given time, but for other cut devices it carries whatever was merged before the cut. T.dev isn't the lost device, so by the text its "Spam" (merged at 14:20, before the 16:00 cut) is carried, although T.dev exists only because of the theft.

### ID-022 Thief backdates `prev` after the revocation
- **Kind:** adversarial
- **Design:** §6.2, §6.3, §9.4, §8.2, §11.4, §10.7
- **Binding:** any
- **Setup:** Standard. `J.ipad` was stolen; `J.mac revoked J.ipad`; the PUK and all lists rotated.
- **Steps:**
  1. `T(J.ipad)` signs `device:<T.dev>` with `prev` = the chain head from before the revocation.
  2. `T.dev` signs `device:<T.dev2>` with `prev` = {step 1's write}.
  3. `T(J.ipad)`, backdated the same way, signs `revoke:<J.rk>`, a manifest `n+1` sealing a PUK of its own, and `name` = "Pwned".
  4. `T(J.ipad)` signs a chain write whose `prev` names a write ID that exists nowhere.
  5. `T.dev2` sends a key request for Groceries' current epoch to J.mac and A.phone.
  6. All devices sync. Later, `J.mac revokes J.phone` (an unrelated loss), which rotates the PUK.
- **Expect:**
  - On every device, neither T.dev nor T.dev2 is active or listed under Devices.
  - The recovery entry, the current manifest (J.mac's) and J's display name are unchanged.
  - No device answers T.dev2's key request.
  - The manifest written in step 6 has boxes for exactly J.mac and J.rk, with none for T.dev2.
  - Step 4's write is held, never applied, on every device.
- **Invariants:** INV-AUTHZ, INV-SIGNED, INV-CUT, INV-NO-HOLD-FOREVER (see GAP)
- **Notes:**
  - GAP: Voiding by a cut isn't transitive. §6.2 voids what *the revoked device* signed unless every revocation builds on it, so T.dev's entry doesn't count. But T.dev2's entry is signed by T.dev, which no revocation names. Judged on its own past, where T.dev is active, it stands, and no cut voids it. By the literal rules T.dev2 counts and is active. A manifest must then box it ("an entry for every active device … and no others", §6.3), so J.mac's step-6 rotation hands T the new PUK, and key requests from T.dev2 pass §11.4's "chain-valid" check. The thief can repeat this at any time with fresh backdated pairs. §8.2's fence for "a device that a revoked device added" covers one hop only.
  - GAP: §9.4 holds a log write until its whole past arrives, and nothing ever drops one whose past can't exist. Step 4's write is held forever, against INV-NO-HOLD-FOREVER. Held writes count for domination (§10.7), so the message carrying it is never garbage-collected. Anyone able to sign into a log can do this.

### ID-023 Thief writes into the old epoch after the rotation
- **Kind:** adversarial
- **Design:** §3.3, §6.6, §8.3, §11.6
- **Binding:** any
- **Setup:** Standard. J.phone was stolen; `J.mac revoked J.phone`; A.phone rotated House to `e+1` with `fence:<J.phone>`. It is less than 30 days later, so A.mac and J.mac still watch House `e`.
- **Steps:**
  1. `T(J.phone) adds "Buy a boat" to House`, stamped epoch `e`, and appends it to House `e`'s channel.
  2. `T(J.phone)` signs a backdated `device:<T.dev>`, as in ID-022 step 1. `T.dev sets Bulbs.title = "x"`, stamped `e`, and appends it to House `e`.
  3. `A.mac syncs`; `J.mac syncs`; `A.phone syncs`.
- **Expect:**
  - No device forwards either write: no `_carried` list in House `e+1` names them.
  - No device shows "Buy a boat", and Bulbs keeps its title.
- **Invariants:** INV-CUT, INV-AUTHZ
- **Notes:**
  - GAP: §3.3 says a revoked device's old-epoch writes aren't forwarded, but the rules don't make that true. A forward is valid when it is signed by a usable device of a write-enabled member (§8.3), and the forwarded write is checked against the epoch it was authored in, `e`. J.phone is fenced only in `e+1`'s genesis, and T.dev is valid on its own past and never fenced, so both are "usable in epoch `e`". §11.6 says forwarders pass on only writes "whose author was entitled to make them", without saying that this excludes devices cut before the target epoch. An honest forwarder that follows §8.3 vouches for the thief, and every receiver must accept the forward.
  - GAP: §11.6 makes a rotation due for revoked devices and replaced recovery keys, but not for a device whose entry stops counting. T.dev, created after the rotation, is never fenced in any later epoch.

### ID-024 Mutual revocation, then recovery while the thief is still active
- **Kind:** adversarial
- **Design:** §6.2, §6.5, §9.4, §3.2, §13.2, §13.3
- **Binding:** any
- **Setup:** Standard. J.phone has been stolen unlocked.
- **Steps:**
  1. `J.mac offline`. `T(J.phone) revokes J.mac` and `revokes J.ipad`. `J.mac revokes J.phone`. Neither side has seen the other's writes.
  2. `J.mac online`; all devices sync.
  3. `J.new recovers with J.rk`.
  4. `T(J.phone)` signs `revoke:<J.new>` building on everything it holds.
  5. All devices sync.
- **Expect:**
  - After 2, J.phone, J.mac and J.ipad are revoked on every device. No active device remains, and no new manifest is written.
  - J.mac and J.ipad show that they were removed and that J's recovery key restores access (see GAP). Their local data is read-only.
  - After 3, J.new is active: it was added by the recovery key, building on the key's own revocations. Manifest `n+1` has boxes for J.new and J.rk, and J's lists rotate, fencing all three devices.
  - After 5, T's `revoke:<J.new>` doesn't stand, because J.phone is revoked in its past. J.new stays active.
  - T(J.phone) can't decrypt any epoch created after step 3.
- **Invariants:** INV-CUT, INV-AUTHZ
- **Notes:** GAP: §13.3 has one message, "This device was removed from your account", whether J removed the device or a stolen device did. J.mac can tell the two apart, because its revoker is itself revoked. §13.2 promises that the recovery key restores access, but no message tells J to use it.

### ID-025 Stolen phone kept offline for six months, then used
- **Kind:** adversarial
- **Design:** §6.2, §9.4, §6.6, §11.6, Q15
- **Binding:** any
- **Setup:** Standard. J.phone was stolen; `J.mac revoked J.phone`, and all lists rotated. Then `J.mac paired J.new`.
- **Steps:**
  1. `wait 180 days`.
  2. `T(J.phone)` signs `revoke:<J.mac>` and `revoke:<J.ipad>`, with `prev` = the chain head at the time of the theft.
  3. All devices sync.
  4. `J.new recovers with J.rk`, generating new device keys.
- **Expect:**
  - After 3, both revocations stand and count on every device. J.mac, J.ipad and J.new are no longer active: J.new's entry was signed by J.mac, and T's revocation of J.mac doesn't build on it.
  - T still can't decrypt any list epoch created since the first rotation.
  - House rotates again, by an A device, fencing J.mac, J.ipad and J.new. J's `to` entry is `null` until J has a manifest that builds on those cuts.
  - After 4, J has every list back, and T can't touch the devices the recovery key adds.
- **Invariants:** INV-CUT
- **Notes:** The design accepts this outcome (§6.2: "a theft, unlike a loss, can end in recovery at any later time"; Q15). The scenario pins down the limit: T can force a recovery once and learns nothing new by it. J.new needs new keys because its device ID already has an entry (see ID-016).

### ID-026 Thief of the first device mints a second recovery entry
- **Kind:** adversarial
- **Design:** §6.2, §6.5, §9.4
- **Binding:** any
- **Setup:** Standard. `J.phone`, the first device, is stolen unlocked; J hasn't noticed yet.
- **Steps:**
  1. `T(J.phone)` picks a recovery secret of its own and signs `device:<rkT>` of kind `recovery`, with `prev` = {genesis}.
  2. `J.mac revokes J.phone`, not building on step 1.
  3. All devices sync.
  4. Signing with rkT, with `prev` = {step 1's write}, T writes `device:<T.dev>`.
  5. All devices sync.
- **Expect:**
  - On no device is rkT ever a recovery key. J.rk is J's only recovery key throughout.
  - T.dev counts on no device, and J.mac and J.ipad stay active.
  - J's, A's and B's devices reach the same verdict on every write in J's chain.
- **Invariants:** INV-AUTHZ, INV-CONVERGE
- **Notes:** GAP: §6.2 lets the first device create the recovery entry "in its first write after the genesis", and a write is judged on its own past. Step 1, whose past is only the genesis, passes that test, so two initial recovery entries stand, and the lowest-write-ID tie-break covers only replacements. J.mac's revocation voids rkT's entry, which J.phone signed. But step 4 is signed by rkT, not by J.phone, and its past holds no revocation, so it stands and no cut voids it (the same non-transitivity as ID-022). Whether rkT counts as "the current recovery key", whose writes "are affected by none of this", is undefined. The first device therefore keeps, for life, the power to set up a second recovery key that nothing can revoke. Mallory can publish two initial recovery entries in her own chain in the same way, and clients that pick different ones as current then disagree about her later writes (INV-CONVERGE). Possible fixes: have the genesis commit to the recovery key, or require the recovery entry to have `prev` = {genesis} and to be in the past of the device's own entry.

### ID-027 The holder of the old recovery key contests its replacement
- **Kind:** adversarial
- **Design:** §6.5, §6.2, §9.4, Q15, §19
- **Binding:** any
- **Setup:** Standard. E photographed the paper with `J.rk` and holds the hub credential. J finds out.
- **Steps:**
  1. `J.mac replaces recovery key` (J.rk → J.rk2). All of J's devices and A.phone sync, and the PUK and lists rotate.
  2. E gets J's uid from the old pointer, captured earlier, and reads J's profile.
  3. With the old key, and `prev` = the chain head before step 1, E writes a replacement naming her own key, rkE. She retries with fresh keys or HLC values until its write ID is lower than that of J's replacement.
  4. With rkE, E revokes J.phone, J.mac and J.ipad, adds `E.dev` (harness extension) and writes a manifest with boxes for E.dev and rkE.
  5. All devices sync.
- **Expect** (what the rules give):
  - J.rk has two concurrent replacements. E's has the lower write ID and stands; J's doesn't.
  - On every device, rkE is J's recovery key. J.phone, J.mac and J.ipad are revoked, and E.dev is J's only active device.
  - E.dev opens J's home, receives J's grants once the lists rotate, and reads every list of J's, as J.
  - J.rk2 opens nothing, and its entry doesn't stand. J can't replace rkE, because that needs rkE.
- **Invariants:** INV-CONVERGE (every device agrees that E won)
- **Notes:** GAP: Q15 says the holder of a replaced key can "contest" the replacement, and §6.5 says they can still make cuts. But the tie-break between concurrent replacements, lowest write ID (§6.2), depends on a hash over fields the signer chooses. The holder of the old key wins any contest within about two attempts and takes the whole identity, not just a forced recovery. Replacing a key that "someone has seen" therefore gives no protection against the person who saw it. The tie-break would need to be one an attacker can't grind, or the parties would need to be ranked, as Q15 suggests.

### ID-028 A separate hub token per device cuts the thief off
- **Kind:** adversarial
- **Design:** §13.1, §3.3, §6.2, §6.6
- **Binding:** jmap
- **Setup:** Standard, on Fastmail. Each of J's devices uses its own API token. J.phone has token tP, and when J.phone paired J.mac it put J.mac's token tM into the code.
- **Steps:**
  1. J.phone is stolen. `J.mac revokes J.phone`.
  2. J deletes tP at Fastmail: `hub: reject auth for T(J.phone)`.
  3. `T(J.phone)` signs backdated `revoke:<J.mac>` and `revoke:<J.ipad>` and tries to append them using tP.
  4. `T(J.phone)` tries again using tM.
- **Expect:**
  - Step 3's appends fail, and the hub holds none of T's writes.
  - T(J.phone) shows "Sync access was revoked or changed. Reconnect to keep syncing."
  - Step 4 fails because J.phone kept no copy of tM once the pairing code expired (see GAP). J.mac and J.ipad stay active.
- **Invariants:** INV-CUT, INV-AUTHZ
- **Notes:** GAP: §13.1 has the hub owner enter a separate token "when inviting or pairing", but doesn't say the showing device must forget it afterwards. If J.phone kept tM, the thief can use it, and the backdated revocations of ID-025 are possible again. Harness: step 4 is outside the step vocabulary.

### ID-029 Eve destroys Jesper's profile while his devices are offline
- **Kind:** adversarial
- **Design:** §12.1, §12.2, §12.4, §5.6, §6.2, §8.3
- **Binding:** any
- **Setup:** Standard. All of J's devices are offline (he is on holiday). A has invited B to House by link.
- **Steps:**
  1. `hub: destroy channel <J's profile tag>`.
  2. `B.phone joins via` the House link; `A.phone approves B`.
  3. `A.phone syncs`; `B.phone syncs`.
  4. `J.phone online`; all devices sync.
- **Expect:**
  - After 3, A.phone has re-published J's profile, which it holds in full with every write signed. B.phone then verifies J's writes and shows his items in House (see GAP).
  - Until B.phone has J's chain, it holds J's House writes as not yet valid instead of dropping them.
  - After 4, J's profile on the hub is complete, and every device holds the same chain for J.
- **Invariants:** INV-SIGNED, INV-GC-SAFE, INV-NO-HOLD-FOREVER
- **Notes:** GAP: §12.1 has every device keep its collaborators' profiles, but §12.2 heals only "a current channel: the current epoch of a collection, the current home generation", and §12.4's "re-publish everything it holds: profile, …" reads as the device's own profile. Whether A may re-publish J's profile is never said. Doing so is safe, since every write is signed. Without it, B can't verify J's writes until a device of J's comes back.

### ID-030 Eve destroys the recovery pointer
- **Kind:** adversarial
- **Design:** §6.5, §12.2, §12.4, §5.6, §13.3
- **Binding:** any
- **Setup:** Standard. `J.phone` wrote the pointer at first run and holds the only copy; J.mac and J.ipad were paired later.
- **Steps:**
  1. `hub: destroy channel <J's pointer tag>`. E spots it as a single message under a tag nobody reads.
  2. `J.mac syncs`; `J.ipad syncs`.
  3. J.phone is lost before it syncs again; later J.mac and J.ipad are lost too.
  4. `J.new recovers with J.rk`.
- **Expect:**
  - After 2, the pointer is back on the hub (see GAP), and step 4 finds J's uid and proceeds as in ID-010.
  - If no device put it back, J.new tells J that no account was found for this key and offers another way to enter his uid (see GAP). It creates no identity.
- **Invariants:** INV-UX
- **Notes:** GAP: Only the device that created a pointer can hold a copy (ID-012), so J.mac and J.ipad can't restore it, and §12.2's list of channels to heal doesn't include the pointer. With the pointer gone and its creator lost, J can't recover even though he has the key and A's devices know his uid from House's roster. The design has no way to take the uid from a collaborator, and the key's encoding doesn't include it.

### ID-031 Mallory's keys and signatures that lenient verifiers accept
- **Kind:** adversarial
- **Design:** §5.5, §6.2, §6.4, §8.3
- **Binding:** any
- **Setup:** Standard. M is a writer in House, and `M.phone` runs a modified client. J's and A's clients run on two different Ed25519 verifiers, each claimed to meet §5.5.
- **Steps:**
  1. `M.phone signs` into M's chain `device:<M2>`, whose `sign` key is a non-canonical encoding (y ≥ p) of a valid point.
  2. `M.phone signs` `device:<M3>` with a small-order `sign` key and a `pop` that verifies only under the cofactored equation.
  3. `M.phone signs` `device:<M4>` copying J.mac's `sign`, `agree` and `pop`.
  4. `M.phone signs` a House write, then posts a copy with `S` replaced by `S + L`.
  5. `M.phone signs` a House write with a non-canonical `R`.
  6. `M.phone` appends to J's profile channel (whose key she can derive from J's uid) a `revoke:<J.mac>` claiming J as author but signed with her own key.
  7. M2, M3 and M4 each sign a House write.
- **Expect:**
  - On every device, the entries for M2, M3 and M4 don't stand (M4's `pop` binds J's uid, not M's), and their House writes are never applied.
  - Step 4: the `S + L` copy is dropped and logged. The original applies once; both copies have the same write ID, because the signature isn't part of `sigInput`.
  - Step 5's write is dropped and logged.
  - Step 6's write doesn't verify against J's chain and is dropped. J.mac stays active.
  - Both verifier implementations reach the same verdict on every write.
- **Invariants:** INV-SIGNED, INV-CONVERGE, INV-AUTHZ
- **Notes:** §1.1 says CryptoKit covers every primitive, but CryptoKit doesn't document whether its Ed25519 verifier applies all of §5.5's rules (canonical `R` and `A`, rejection of small-order keys, the cofactorless equation), so an SDK may have to check them itself. Step 7's writes come from entries that never stand, so §8.3 holds them instead of dropping them; see ID-022 on writes held forever.

### ID-032 Mallory's non-canonical X25519 agreement keys
- **Kind:** adversarial
- **Design:** §6.2, §5.5, §6.3, §5.4
- **Binding:** any
- **Setup:** Standard. M is a writer in House. J.mac and A.phone use different X25519 libraries.
- **Steps:**
  1. `M.phone signs` `device:<M2>` with `agree` = a u-coordinate ≥ 2^255 − 19, and `device:<M3>` with an `agree` key whose bit 255 is set. Both have valid Ed25519 `sign` keys and `pop`s.
  2. `M.phone signs` `device:<M5>` with a small-order `agree` key (u = 0).
  3. M2, M3 and M5 each sign a House write. `M.phone` writes a manifest boxing all of M's devices.
- **Expect:**
  - J.mac, A.phone and B.phone reach the same verdict on whether each entry stands, and therefore on whether each House write is valid.
  - M5's entry stands on no device.
- **Invariants:** INV-CONVERGE, INV-SIGNED
- **Notes:** GAP: §6.2 requires every public key, `agree` included, to be canonical and not small-order, citing §5.5. But §5.5 defines those checks only for Ed25519. For X25519, RFC 7748 decoders mask bit 255 and accept u ≥ p, and libraries differ on rejecting small-order points (RFC 9180 only checks for an all-zero shared secret when sealing). Without a definition, clients disagree on whether M2 and M3 stand, then on which of M's later chain and data writes count, and on what a valid manifest of hers must box (§6.3).

### ID-033 Crash during first run, before the recovery key is confirmed
- **Kind:** fault
- **Design:** §6.1, §6.5, §19
- **Binding:** any
- **Setup:** Empty hub H. `J.phone` has no identity.
- **Steps:**
  1. `J.phone` connects H.
  2. `J.phone crashes after` storing the recovery pointer (§6.1 step 5), while the recovery key is on screen and not yet confirmed.
  3. `J.phone launches`.
- **Expect:**
  - After 3, either J.phone shows the same recovery key again, whose `RS` matches the recovery entry already on the hub, and asks J to confirm it; or the hub holds nothing of the interrupted setup and it restarts cleanly (see GAP).
  - J never ends up with an identity whose recovery key he hasn't seen.
  - At most one profile on the hub has J.phone's signing key as `first`.
- **Invariants:** INV-UX
- **Notes:** GAP: §6.1 publishes the recovery entry, the PUK box sealed to the recovery key and the pointer (steps 2–5) before the user sees the key (step 6), and doesn't say whether `RS` is persisted until it is confirmed. If the crash loses `RS`, J has an identity whose recovery key nobody knows, and §6.5 forbids making a new one without it. If setup simply restarts, it creates a second identity beside the first.

### ID-034 Revoking device crashes before rotating the PUK
- **Kind:** fault
- **Design:** §6.6, §6.3, §11.4, §11.6
- **Binding:** any
- **Setup:** Standard. J.phone has been stolen.
- **Steps:**
  1. `J.mac revokes J.phone`. `J.mac crashes after` appending the revocation, before the manifest, and stays down.
  2. `A.phone syncs`. It sees the revocation and rotates House to `e+1`.
  3. `J.ipad syncs` House and its inbox only. The harness holds back its PUK rotation.
  4. After 10 minutes without a grant, `J.ipad` sends a key request for House `e+1`. `A.phone syncs`.
  5. The harness releases J.ipad, which writes manifest `n+1`. `A.phone syncs`; `J.ipad syncs`.
- **Expect:**
  - House's `e+1` genesis has `null` as J's key ID. No grant to J for `e+1` is sealed to `UKa(n)`.
  - In step 4, A.phone doesn't answer the key request, because no manifest of J's builds on the revocation yet (see GAP). T(J.phone) can't decrypt House `e+1`.
  - After 5, A.phone sends J's grant sealed to `UKa(n+1)`, and J.ipad shows House.
- **Invariants:** INV-CUT, INV-NO-HOLD-FOREVER
- **Notes:** GAP: When answering a key request, §11.4 checks only that the requester is an active member with a chain-valid, unfenced device, then seals to "the current manifest". It doesn't repeat §11.6 step 4's rule to wait until the member's manifest builds on the cut. §6.3 implies there is no current manifest in this window but doesn't say what senders do. A receiver that seals to the latest manifest hands `K(House,e+1)` to the thief. Harness: holding back one step of a device's sync is outside the step vocabulary.

### ID-035 Crash while replacing the recovery key
- **Kind:** fault
- **Design:** §6.5, §6.2, §6.3, §19
- **Binding:** any
- **Setup:** Standard.
- **Steps:**
  1. On `J.mac`, J chooses "New recovery key" and types `J.rk`.
  2. `J.mac crashes after` appending the new recovery entry, before showing `J.rk2`.
  3. `J.mac launches`.
  4. `J.phone syncs`.
- **Expect:**
  - After 3, either J.mac shows the same `J.rk2`, matching the entry on the hub, and asks J to confirm it; or no new entry was published before confirmation (see GAP).
  - After 4, if the replacement is on the hub, J.phone sees that no manifest builds on it and rotates the PUK, with a box for J.rk2 and none for J.rk.
  - J is never left with a current recovery key he hasn't seen.
- **Invariants:** INV-UX
- **Notes:** GAP: §6.5 doesn't say whether the new entry is published before or after the new key is shown and confirmed, or whether `RS2` is kept until then. If `RS2` is lost, J's only recovery key is one nobody knows, the old key is cut, and §19 forbids making another without the current one. Unlike first run (ID-033), J can't start over.

### ID-036 Pairing with a skewed clock and a late scan
- **Kind:** fault
- **Design:** §6.5, §13.2, §13.3
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install.
- **Steps:**
  1. `J.ipad clock = +5 min`. `J.mac` shows a pairing code, and J.ipad scans it 10 s later.
  2. `J.ipad clock = 0`. J.mac shows a new code. J.ipad scans it at 118 s with `net: latency 5000` ms, so its request reaches the hub after the code has expired.
- **Expect:**
  - Step 1: J.ipad posts its request and J.mac prompts. The scanner's clock doesn't decide expiry (see GAP).
  - Step 2: J.mac shows no prompt, and no device entry is written. Within a bounded time, J.ipad shows "This code has expired. Ask for a new one." instead of waiting forever (see GAP).
- **Invariants:** INV-UX
- **Notes:** GAP: §6.5 uses `exp` only as the showing device's deadline for listening. It doesn't say whether the scanner checks `exp` against its own clock; if it does, a skewed scanner rejects live codes or accepts dead ones. Nor does it say how a scanner learns that its request went unanswered, since pairing has no decline.

### ID-037 Typing a recovery key wrong
- **Kind:** ux
- **Design:** §6.5, §13.2, §13.3, Q5
- **Binding:** any
- **Setup:** Standard. J has lost J.phone and J.mac and thinks the iPad is gone too, but J.ipad is in his office drawer. C's recovery key is in the same folder as J's.
- **Steps:**
  1. On `J.new`, J connects the hub, chooses "I have a recovery key" and types his key with one character wrong.
  2. He types C's key by mistake.
  3. He types his own key.
- **Expect:**
  - Step 1: J.new rejects the key on its checksum, before contacting the hub, with a message saying a character is wrong (see GAP).
  - Step 2: J.new finds C's uid. Before signing anything, it shows whose account it found ("Carla") and which devices it will remove, and asks J to confirm (see GAP). J cancels, and C's chain is unchanged.
  - Step 3: J.new shows "Jesper" and lists J.phone, J.mac and J.ipad as the devices it will remove. Once J confirms, all three are revoked.
- **Invariants:** INV-UX
- **Notes:** GAP: Under §6.5, recovery revokes every other device immediately, and §13.2's flow is just "type it". Nothing shows whose account was found or which devices will be removed. A valid key belonging to someone else, such as a family member's, or a device J forgot (here J.ipad) is removed beyond undoing. §13.3 also has no message for a bad checksum or for a key with no pointer on this hub (ID-030).
