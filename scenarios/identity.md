# Identity and devices

Scenarios for users and their devices: first run, home, pairing and its six-digit code, the backup key and restoring, device IDs, lost and stolen devices, and starting over (DESIGN §3, §5.3, §5.5, §6, §12.2, §13).

A user is one secret `US`. Every device of the user holds it; there are no per-device keys, no device list and no way to remove one device (§6). Scenarios that tested device chains, recovery keys, revocation and per-user key generations are retired (footer).

**Standard setup**, used unless a scenario says otherwise: J has `J.phone`, `J.mac` and `J.ipad`, each with its own device ID. A has `A.phone` and `A.mac`. J owns "Groceries" with A as writer; A owns "House" with J as writer. Everything is synced on hub H. `J.bk` is J's backup key (§6.3). A device written `J.new` is a device being added or restored. Device names beyond the actor table (`J.new2`, `E.dev`) are harness extensions and are flagged where used.

---

### ID-001 First run on Jesper's phone
- **Kind:** real-world
- **Design:** §6.1, §6.2, §6.3, §5.3, §5.6, §7.1, §13.1, §17
- **Binding:** any
- **Setup:** Empty hub H. `J.phone` has the app installed, no hub, no lists.
- **Steps:**
  1. `J.phone` connects H (JMAP: session URL and token; IMAP: host, user, app password).
  2. `J.phone` shows the backup key; J confirms he has written it down.
  3. `J.phone syncs`.
- **Expect:**
  - `J.phone` holds a 32-byte `US` and an 8-byte device ID, both with this-device-only protection. J's user ID is `pub(KDF(US, "", "tandem/v1/user-sign", 32))`.
  - The hub holds J's home under `b32(KDF(HK, "", "tandem/v1/tag", 20))`. Every home write has `scopeID = H("tandem/v1/home" || uid)[0..16]`, a zero `ref` and is signed with `UKs`.
  - Liszto's Inbox and Later exist as collections owned by J: each genesis has zero `scopeID`, J as the only member and owner, and a key commitment; each collection ID is the first 16 bytes of its genesis ID; each channel tag derives from the epoch key and the full genesis ID. Their IDs are in `settings:liszto`.
  - Home holds `membership:<Inbox>` and `membership:<Later>`, `state: active`, each with one `keys` entry `{epoch: <genesis ID>, k}`.
  - The key shown has 56 characters in 14 groups of 4; it decodes to `US || H(US)[0..3]`.
  - Nothing on the hub names J, the device, or a list.
- **Invariants:** INV-HUB-BLIND, INV-SIGNED

### ID-002 Add a Mac by scanning a pairing code
- **Kind:** real-world
- **Design:** §6.3, §5.4 (HPKE wire format), §5.6, §13.2
- **Binding:** any
- **Setup:** J has only `J.phone` (as after ID-001). He owns Groceries, with A as writer.
- **Steps:**
  1. `J.phone pairs J.mac`: the QR is shown, `J.mac` scans it and names itself "Jesper's Mac", both devices show a six-digit code, and J approves "Add 'Jesper's Mac'? Code …" on `J.phone`.
  2. `J.mac syncs`.
  3. 30 s later, a client holding the session's `P` posts a second pairing request to the same channel (harness: `E.dev`).
- **Expect:**
  - The pairing channel's tag is `b32(KDF(P, "", "tandem/v1/tag", 20))`; its messages are kind 3, keyed by `KDF(P, "", "tandem/v1/pair-key", 32)`.
  - The challenge is signed by J's `UKs` over `"TANDEM-PAIR-CODE-V1" || P || ek || n`, and both devices show `u32(H("tandem/v1/pair-code" || P || ek || n)[0..4]) mod 1,000,000` as six digits.
  - The reply is `enc || ct`, HPKE-sealed to `ek` with `info = "tandem/v1/pair"` and `aad = "TANDEM-PAIR-V1" || uid || ek`; it opens to J's `US`, which gives the `uid` in the QR.
  - `J.mac` picks a new random device ID, different from `J.phone`'s, and shows Groceries and every other active membership within one sync.
  - No roster changes, no collection changes epoch, and A sees nothing.
  - Step 3 gets no challenge: `J.phone` stopped listening after one approval.
- **Invariants:** INV-HUB-BLIND, INV-SIGNED, INV-BYSTANDER
- **Notes:** GAP (G-01): nothing destroys pairing-channel messages. After a session the hub keeps the requests (names), the challenge and the sealed copy of `US` for good. The last is safe only while the new device's one-time key is gone, and §6.3 doesn't say that `ek`'s private key is erased after use. Harness: step 3's injected request is outside the step vocabulary.

### ID-003 A new iPhone replaces the first device
- **Kind:** real-world
- **Design:** §6.3, §6.4, §5.3, §13.2
- **Binding:** any
- **Setup:** Standard. J is trading in `J.phone`.
- **Steps:**
  1. `J.phone pairs J.new` ("Jesper's iPhone"); `J.new syncs`.
  2. J erases `J.phone` and hands it in.
  3. All devices sync; `A.phone syncs`.
- **Expect:**
  - J's user ID is unchanged. `J.new` has a device ID that no other device of J's has used.
  - Nothing on the hub changes apart from the pairing channel: no roster statement, no rotation, no grant.
  - Writes `J.phone` made remain attributed to J on every device.
  - A's view of Groceries and House doesn't change.
- **Invariants:** INV-BYSTANDER, INV-CONVERGE

### ID-004 New phone restored from a backup
- **Kind:** real-world
- **Design:** §5.3, §6.3, §13.2
- **Binding:** any
- **Setup:** Standard. J restores a new iPhone, `J.new`, from an iCloud backup of `J.phone`, which stays in use for a week. `J.phone` had two edits to Groceries queued, one signed and one not yet signed.
- **Steps:**
  1. `J.new launches`. App data (replica, queue, settings, hub credential) was restored; the this-device-only Keychain items (`US`, device ID) were not.
  2. `J.new syncs`.
  3. `J.mac pairs J.new`.
  4. `J.phone` and `J.new` both edit `milk.title` at the same moment; all sync.
- **Expect:**
  - Before step 3, `J.new` appends nothing and tells J it has to be added from another device or with the backup key.
  - After step 3, `J.new`'s device ID differs from `J.phone`'s.
  - Step 4 leaves two siblings for `milk.title`, one per device, and both are reported.
  - `J.phone`'s two queued edits reach the hub once, from `J.phone`.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE, INV-UNIQUE
- **Notes:** GAP (G-02): §6.3 and §13.1 don't say what a device does with local data it finds when the secret is installed by pairing or the backup key: a restored replica and queue of the same user's collections (keep, discard, or upload as new collections the way §13.1 treats local lists on first connect), or a queued write already signed under another device's ID. Uploading a restored replica as new collections would duplicate every list; re-publishing the old device's queued writes would publish them twice under the old dots (harmless only because the write IDs are equal). Harness: restoring from a device backup is outside the step vocabulary.

### ID-005 A lost phone needs nothing
- **Kind:** ux
- **Design:** §6.4, §1.1 goal 6, §13.2
- **Binding:** any
- **Setup:** Standard. `J.phone` is left in a taxi, switched off. J believes nobody can unlock it.
- **Steps:**
  1. J opens Settings → Sync on `J.mac`.
  2. J and A keep editing Groceries and House for a week.
- **Expect:**
  - `J.mac` offers no way to remove one device, and no device list to remove it from (§6, §13.2 "A device broke or was replaced: nothing to do").
  - "Start over on this device" is offered for the case where the phone may be in someone's hands.
  - No roster statement, rotation or grant follows from the loss.
- **Invariants:** INV-BYSTANDER

### ID-006 Phone in a drawer for seven months
- **Kind:** real-world
- **Design:** §6.2, §8.3, §11.6, §12.3, §13.2
- **Binding:** any
- **Setup:** Standard. `J.ipad` has two unsent edits to `milk` in Groceries; then `J.ipad offline` (in a drawer).
- **Steps:**
  1. `wait 40 days`. `J.mac removes` C from Groceries (C was added meanwhile); Groceries moves to a new epoch.
  2. `A.phone deletes "Old lamp"` from House; later A removes B from House, which rotates.
  3. `wait 160 days`. `J.ipad online`; `J.ipad syncs`.
- **Expect:**
  - `J.ipad` reads each announcement in the old channels' `_roster`, finds the new keys in J's home (stored there by J's other devices), and follows the new epochs.
  - Its two Groceries edits are not in the new inventory. `J.ipad` signs them again with the same dots, an empty context, base = old base ∪ `reach(old)`, and `ref` = the first statement of the new epoch that lists J as a writer or owner. They appear on every device.
  - It publishes nothing under the old epochs' tags after reading the announcements.
  - "Old lamp" stays deleted: its tombstone was carried (§12.3) and `J.ipad` never re-publishes the record.
  - No special "long absence" handling is shown.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH, INV-CONVERGE

### ID-008 Selling the old iPad
- **Kind:** ux
- **Design:** §5.3, §6.4
- **Binding:** any
- **Setup:** Standard.
- **Steps:**
  1. J erases `J.ipad` (factory reset) and sells it.
  2. Variant: J forgets to erase it; the buyer opens Liszto.
- **Expect:**
  - Step 1 needs no protocol action and changes nothing on the hub.
  - In the variant, the buyer holds `US` and is J in every collection (§3.1, device thief). Only "Start over" (ID-021) ends that.
- **Invariants:** INV-BYSTANDER

### ID-009 Handing the old iPad to Aimee
- **Kind:** real-world
- **Design:** §5.3, §6.3, §6.4
- **Binding:** any
- **Setup:** Standard. J gives `J.ipad` to A.
- **Steps:**
  1. Liszto is deleted from the iPad and reinstalled (no data, no Keychain items).
  2. `A.phone pairs` the iPad.
  3. The iPad edits House.
- **Expect:**
  - The iPad's device ID after step 2 differs from the one it had as J's device.
  - It holds A's `US`, follows A's memberships and shows none of J's lists.
  - Its House writes are A's, with dots under its new device ID.
- **Invariants:** INV-CONVERGE
- **Notes:** Switching identity on one installation isn't a defined flow; the scenario deletes the app first. If an SDK kept the iPad's old device ID, A's and J's dots could collide (see ID-040).

### ID-010 Lost everything, backup key in the drawer
- **Kind:** real-world
- **Design:** §6.3, §6.4, §5.3, §13.2
- **Binding:** any
- **Setup:** Standard. All three of J's devices are gone (broken, not stolen).
- **Steps:**
  1. On `J.new`, J scans the hub QR from `A.phone` (§13.1).
  2. J chooses "I have a backup key" and types `J.bk`.
  3. `J.new syncs`; J removes A from Groceries.
- **Expect:**
  - `J.new` derives J's keys, picks a new device ID, opens J's home and follows every active membership.
  - Nothing on the hub changes until step 3.
  - Step 3 works: J is still the owner of Groceries (the collection ID fixes the owner's key), and the rotation runs from `J.new`.
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER

### ID-011 Lost everything, including the backup key
- **Kind:** real-world
- **Design:** §6.4, §11.8, §13.2
- **Binding:** any
- **Setup:** Standard. All of J's devices are gone and `J.bk` is lost.
- **Steps:**
  1. On `J.new`, J connects the hub and starts as a new user, J2.
  2. A copies Groceries (owned by J) into a new collection "Groceries 2", owned by A, and invites J2.
  3. A removes J from House and invites J2.
- **Expect:**
  - J2 has a new user ID; nothing links it to J on the hub or in any roster.
  - Groceries stays readable and writable by A, but its sharing can never change again (§11.8, "owner's secret is lost").
  - "Groceries 2" holds the current items as A's writes; no tombstones and no old signatures are carried (§11.8). A's old membership of Groceries is not redirected.
  - House moves to a new epoch without J; J2 joins it in that epoch.
- **Invariants:** INV-ONE-OWNER, INV-CUT, INV-BYSTANDER

### ID-013 An abandoned identity's backup key, typed later
- **Kind:** edge
- **Design:** §6.3, §6.4
- **Binding:** any
- **Setup:** J started over as J2 after `J.bk` was seen by someone (ID-021). The owner of House removed J; Groceries (J's) was copied to "Groceries 2" (J2's). A never left the old Groceries.
- **Steps:**
  1. Months later, J types the old `J.bk` on `J.new2` (harness device).
- **Expect:**
  - The restore succeeds: `J.new2` is J, opens J's old home and follows the old Groceries, still with A as writer.
  - Nothing on the hub or in any roster says that J was abandoned; the app can't warn (§6.4, "nothing tells other members that an identity was abandoned except the user").
- **Invariants:** INV-CONVERGE

### ID-014 New Mac used offline for a week before pairing
- **Kind:** edge
- **Design:** §6.3, §13.1
- **Binding:** any
- **Setup:** Standard, except that `J.new` (a Mac) has been used without a hub for a week: lists "Work" and "Ideas" with 30 items.
- **Steps:**
  1. `J.phone pairs J.new`; `J.new syncs`.
  2. `J.phone syncs`.
- **Expect:**
  - "Work" and "Ideas" become collections on the hub owned by J, each with its own genesis and a membership in J's home, once (§13.1, "uploads existing local data").
  - `J.new` also shows every collection J already had.
  - `J.phone` shows "Work" and "Ideas" after step 2.
- **Invariants:** INV-UNIQUE, INV-CONVERGE, INV-ONE-OWNER
- **Notes:** §13.1 describes the upload on first connect; this scenario reads it as applying to a device that gets its identity by pairing. See G-02 for local data that belongs to existing collections.

### ID-015 A second device connected with the hub QR instead of pairing
- **Kind:** ux
- **Design:** §6.1, §13.1, §13.2
- **Binding:** any
- **Setup:** Standard. J has a new iPad, `J.new`, and scans the hub QR from `J.phone`.
- **Steps:**
  1. `J.new` connects H.
- **Expect:**
  - `J.new` asks whether J is new to this hub, has another device (pair), or has a backup key, before generating any secret.
  - If J picks "new", J ends with two identities that nothing merges; A would see two "Jesper"s in a roster that lists both.
- **Invariants:** INV-UX
- **Notes:** GAP (G-03, minor): §6.1 generates a secret when a client connects a hub "and has no identity yet", and §13.1's connect flow lists no such choice. §13.2's "Lost every device" flow ("choose 'I have a backup key'") implies one. The hub can't tell a client whether its user already exists.

### ID-019 Restoring on two new devices at once
- **Kind:** edge
- **Design:** §6.3, §5.3, §6.2, §9.1
- **Binding:** any
- **Setup:** J has lost every device. He has `J.bk`, a new phone `J.new` and a new Mac `J.new2` (harness device).
- **Steps:**
  1. J types `J.bk` on both devices at the same moment.
  2. Both edit `settings:liszto` and Groceries concurrently; all sync.
- **Expect:**
  - Both devices are J, with different device IDs.
  - Nothing on the hub was needed to restore either; neither waits for the other.
  - Concurrent home and Groceries writes merge per §9 with no lost write.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS

### ID-020 Pairing QR photographed during a video call
- **Kind:** adversarial
- **Design:** §6.3, §3.1, §13.2, §19 (pairing code)
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install. E holds the hub credential, and J is sharing his screen with E on a call.
- **Steps:**
  1. `J.mac` shows a pairing QR, and E captures it from the shared screen.
  2. 2 s later, E's client posts a pairing request named "Jesper's iPad" (harness: `E.dev`).
  3. 10 s later, `J.ipad` scans the QR and posts its own request, also named "Jesper's iPad".
  4. J compares the codes and approves the prompt whose code `J.ipad` shows.
- **Expect:**
  - `J.mac` answers both requests with challenges and shows two prompts, each with its own code.
  - `J.ipad` shows exactly one code: the one bound to its own `ek`. It shows nothing for E's challenge.
  - The approved reply is sealed to `J.ipad`'s `ek`; E can read the pairing channel but can't open it.
  - E's request gets no reply, and `J.mac` stops listening after the one approval.
  - If J instead approved E's prompt, E would hold `US` for good and only "Start over" (ID-021) would help; the test records that the prompt names the code to compare.
- **Invariants:** INV-HUB-BLIND
- **Notes:** Harness: E's request is outside the step vocabulary. How `J.mac` presents several prompts at once isn't specified; any presentation in which each prompt shows its own code satisfies §6.3.

### ID-021 Theft: starting over
- **Kind:** real-world
- **Design:** §6.4, §11.8, §8.2, §11.6, §13.2
- **Binding:** any
- **Setup:** Standard. `J.phone` is stolen unlocked. T holds it (`T(J.phone)`).
- **Steps:**
  1. On `J.mac`, J chooses "Start over on this device". `J.mac` becomes J2 with a new `US`.
  2. `J.mac` copies Groceries into a new collection owned by J2 and invites A; A joins.
  3. J2 asks A to re-add him to House; A removes J and invites J2; J2 joins.
  4. `J.ipad pairs` from `J.mac`.
  5. `T(J.phone)` keeps using Liszto.
- **Expect:**
  - J2 owns the new Groceries; its items are J2's writes (§11.8).
  - House's new epoch excludes J; T gets no grant and can't read anything House publishes afterwards.
  - T, as J, still reads and writes the old Groceries and can change its sharing; A is told only by J, out of band (§6.4), and should leave it.
  - `J.ipad` ends as J2, with a device ID different from its previous one.
- **Invariants:** INV-CUT, INV-ONE-OWNER

### ID-023 A thief writes into the old epoch after being removed
- **Kind:** adversarial
- **Design:** §8.3, §11.6, §3.3
- **Binding:** any
- **Setup:** After ID-021: House moved from epoch `E1` to `E2` when A removed J. T holds J's `US` and `K(E1)`.
- **Steps:**
  1. `T(J.phone)` signs writes to House naming the last `E1` statement and appends them to the `E1` channel.
  2. T re-encrypts the same writes into the `E2` channel (T has no `K(E2)`; harness gives it the key to test the rule).
  3. T signs one of J's own late writes again, naming the `E2` start.
- **Expect:**
  - Step 1's writes are valid in `E1` only; devices that moved to `E2` don't show them.
  - Step 2's writes are ineligible in `E2`: their source epoch isn't `E2` and they aren't in `E2`'s inventory.
  - Step 3's write is rejected: no statement of `E2` lists J as owner or writer (§11.6, "signing again").
- **Invariants:** INV-AUTHZ, INV-CUT

### ID-025 A stolen phone kept offline for six months, then used
- **Kind:** adversarial
- **Design:** §6.4, §8.2, §9.4, §11.8
- **Binding:** any
- **Setup:** J started over as J2 (ID-021), but forgot "Trip", owned by J, shared with B and C, which nobody left. `J.ipad` still holds J's old identity (never started over) and is in a drawer.
- **Steps:**
  1. `wait 180 days`. `T(J.phone)` removes C from Trip (a rotation) and invites T's friend.
  2. Same hour: `J.ipad` comes online and J, not knowing, adds B's partner to Trip from it.
  3. All devices sync.
- **Expect:**
  - Whichever of the two successors of the same statement is seen first, every device holding both declares Trip dead (§9.4), shows "Trip can't sync any more. Copy it to a new list?" and stops publishing, accepting grants and merging.
  - The two statements are put in the current epoch channel's `_roster` and restored there if destroyed.
  - B can copy Trip into a new collection of his own.
- **Invariants:** INV-CONVERGE, INV-ONE-OWNER
- **Notes:** Without step 2, T simply runs Trip as its owner; the design accepts that (§6.4: members should leave collections an abandoned identity owns).

### ID-028 A separate hub token per device cuts the thief off the hub
- **Kind:** adversarial
- **Design:** §13.1, §3.3, §6.4
- **Binding:** jmap
- **Setup:** Standard, but each of J's devices has its own Fastmail-style token. `J.phone` is stolen.
- **Steps:**
  1. J revokes `J.phone`'s token at the provider, and starts over (ID-021).
  2. `T(J.phone)` tries to sync.
- **Expect:**
  - From the next login on, T can't read or append on the hub; it keeps only its local replica.
  - T still holds J's `US`. Hub access obtained any other way (a hub QR, an invite link) makes T J again; the token cut doesn't replace starting over.
- **Invariants:** INV-UX
- **Notes:** Whether an already-open session survives the revocation is recorded; see FT-027.

### ID-029 Eve destroys Jesper's home while his devices are offline
- **Kind:** adversarial
- **Design:** §12.1, §12.2, §6.2
- **Binding:** any
- **Setup:** Standard. All of J's devices are offline (he is on holiday).
- **Steps:**
  1. `hub: destroy channel <J's home tag>`.
  2. A and B keep using House and Groceries.
  3. `J.phone online`; `J.phone syncs`.
- **Expect:**
  - Step 2 is unaffected: no other user reads J's home.
  - After step 3, `J.phone` re-publishes every home record it holds (home is a current channel, §12.2); the other devices of J converge on it.
  - No membership, key or setting is lost.
- **Invariants:** INV-GC-SAFE, INV-NO-LOSS

### ID-031 Mallory's keys and signatures that lenient verifiers accept
- **Kind:** adversarial
- **Design:** §5.5, §5.3, §11.2, §8.3
- **Binding:** any
- **Setup:** Standard. M is a writer in House; `M.phone` runs a modified client. J's and A's clients use two different Ed25519 verifiers, each claimed to meet §5.5.
- **Steps:**
  1. `M.phone` posts a join request to a House invite she holds, for a second identity whose `uid` is a non-canonical encoding (y ≥ p) of her own key.
  2. She posts another whose `uid` is a small-order point, with a `sig` that verifies only under the cofactored equation.
  3. `M.phone signs` a House write, then posts a copy with `S` replaced by `S + L`.
  4. `M.phone signs` a House write with a non-canonical `R`.
  5. `M.phone` posts a write naming J as author but signed with her own key.
- **Expect:**
  - Steps 1–2: the owner's devices reject both requests on the key checks; neither identity is ever admitted.
  - Step 3: the `S + L` copy is dropped and logged. The original applies once; both copies have the same write ID, since the signature isn't part of `sigInput`.
  - Steps 4–5: dropped and logged.
  - Both verifiers reach the same verdict on every object.
- **Invariants:** INV-SIGNED, INV-CONVERGE, INV-AUTHZ
- **Notes:** The same edge cases belong in the SDK test vectors (§5.5). Libraries differ here: an SDK on CryptoKit or Go's `crypto/ed25519` must add the canonical-key and small-order checks itself.

### ID-032 Mallory's odd X25519 agreement keys
- **Kind:** adversarial
- **Design:** §11.2, §11.3, §11.4, §8.2, §5.4
- **Binding:** any
- **Setup:** Standard. M holds a link invite to House.
- **Steps:**
  1. M joins with `uka` = a u-coordinate ≥ 2^255 − 19; J approves.
  2. M's sockpuppet M2 joins with `uka` = 0 (small order); J approves.
  3. M3 joins with `uka` = A's agreement key; J approves.
- **Expect:**
  - Every device agrees that each admitting statement is valid: roster validity compares `uka` as bytes and never decodes it.
  - Sealing a grant to M2 fails (all-zero shared secret); `J`'s device logs it and admits no fallback; M2 never gets a key.
  - M3's grants are sealed to A's key; A's devices can open them but learn only keys A already has. M3 gets no key.
  - No honest member is affected.
- **Invariants:** INV-BYSTANDER, INV-CONVERGE
- **Notes:** A joiner can only hurt themselves through `uka`. Whether the owner's app should refuse such a request up front is a UX choice the design leaves open.

### ID-033 Crash during first run, before the backup key is confirmed
- **Kind:** fault
- **Design:** §6.1, §6.3, §5.3
- **Binding:** any
- **Setup:** Empty hub H. `J.phone` has no identity.
- **Steps:**
  1. `J.phone` connects H.
  2. `J.phone crashes after` creating home (§6.1 step 3), while the backup key is on screen and not yet confirmed.
  3. `J.phone launches`.
- **Expect:**
  - After step 3, `J.phone` has the same `US` and user ID, and asks J to save the backup key again before anything else.
  - The hub holds one home for J; no second identity is created.
  - The key can always be shown again behind device authentication (§6.3), so nothing is lost by the crash.
- **Invariants:** INV-UX, INV-UNIQUE
- **Notes:** A crash before `US` reaches the Keychain leaves at most an orphan home on the hub, unreadable by anyone; first run then starts afresh.

### ID-036 Pairing with a skewed clock and a late scan
- **Kind:** fault
- **Design:** §6.3, §13.2, §13.3
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install.
- **Steps:**
  1. `J.ipad clock = +5 min`. `J.mac` shows a pairing QR, and `J.ipad` scans it 10 s later.
  2. `J.ipad clock = 0`. `J.mac` shows a new QR. `J.ipad` scans it at 118 s with `net: latency 5000` ms, so its request reaches the hub after the QR has expired.
- **Expect:**
  - Step 1: `J.ipad` posts its request and pairing completes; the scanner's clock doesn't decide expiry.
  - Step 2: `J.mac` answers nothing. Within a bounded time, `J.ipad` shows "This code has expired. Ask for a new one." instead of waiting forever.
- **Invariants:** INV-UX
- **Notes:** GAP (G-04): §6.3 uses `exp` only as the showing device's deadline for listening. It doesn't say whether the scanner checks `exp` against its own clock, and the new device has no signal that its request went unanswered: pairing has no decline.

### ID-037 Typing a backup key wrong
- **Kind:** ux
- **Design:** §6.3, §13.2, §13.3, Q5
- **Binding:** any
- **Setup:** J has lost every device. C's backup key is in the same folder as J's.
- **Steps:**
  1. On `J.new`, J types his key with one character wrong.
  2. He types it in upper case with the group spaces.
  3. He types C's key by mistake.
- **Expect:**
  - Step 1: rejected on the checksum `H(US)[0..3]`, before anything is derived or fetched, with a message that a character is wrong.
  - Step 2: accepted if the app normalises case and spaces (input tolerance is the app's; the encoding itself is lower-case `b32`).
  - Step 3: `J.new` becomes C and opens C's home. It should show whose lists it found before carrying on.
- **Invariants:** INV-UX
- **Notes:** GAP (G-05, minor): §13.3 has no message for a mistyped backup key, and restoring has no step that shows whose identity was restored. Home holds no display name for its own user, so the app can show only the lists it finds.

### ID-038 Restoring when home is gone from the hub
- **Kind:** adversarial
- **Design:** §6.3, §12.2, §12.4, §1.2, §11.8
- **Binding:** any
- **Setup:** Standard. E destroyed J's home channel; then all of J's devices were lost before any came online to heal it.
- **Steps:**
  1. J restores on `J.new` with `J.bk`.
  2. A keeps using Groceries (owned by J) and House (owned by A).
- **Expect:**
  - `J.new` is J but holds no memberships or keys and shows no lists.
  - Nothing on the hub lets J's device recover the keys: it doesn't know the collection IDs, so it can't send a key request, and only owners sign grants.
  - For House, A can re-admit J only by removing him and inviting him again (a former member is always asked about, §11.3).
  - Groceries' key is held only by A, who can't grant it. A can copy Groceries into a new collection (§11.8).
- **Invariants:** INV-UX
- **Notes:** Within §1.2's accepted loss of availability. The restore cannot tell "home destroyed" from "wrong hub" or "user who never had lists" (see G-05).

### ID-039 A pairing request swapped by a hub participant
- **Kind:** adversarial
- **Design:** §6.3
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install. E holds the hub credential and sees the pairing QR.
- **Steps:**
  1. `J.mac` shows a pairing QR; `J.ipad` scans it and posts its request.
  2. E destroys `J.ipad`'s request and posts one with its own `ek`, named "Jesper's iPad".
- **Expect:**
  - `J.mac` shows one prompt, with the code bound to E's `ek`.
  - `J.ipad` shows no code (it never sees a challenge for its own `ek`), and J declines.
  - Nothing is sealed to E.
- **Invariants:** INV-HUB-BLIND

### ID-040 A forged challenge and a forged reply
- **Kind:** adversarial
- **Design:** §6.3, §5.5
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install. E holds the hub credential, saw the QR, and has an identity of its own (`uidE`, `USE`).
- **Steps:**
  1. `J.ipad` posts its request.
  2. E posts a challenge for `J.ipad`'s `ek`, signed with E's key.
  3. E posts a reply sealing `USE` to `J.ipad`'s `ek`.
  4. `J.mac` answers with the real challenge; J approves; the real reply arrives.
- **Expect:**
  - Step 2: `J.ipad` rejects the challenge (its signature doesn't verify against the `uid` in the QR) and shows no code for it.
  - Step 3: `J.ipad` rejects the reply: `USE` doesn't give the QR's `uid`.
  - Step 4: pairing completes with J's `US`.
- **Invariants:** INV-SIGNED

### ID-041 A fourth request on one QR
- **Kind:** adversarial
- **Design:** §6.3, §19 (three answers per QR)
- **Binding:** any
- **Setup:** Standard, except that `J.ipad` is a fresh install. E saw the QR.
- **Steps:**
  1. E posts three pairing requests within 5 s.
  2. `J.ipad` posts its request.
- **Expect:**
  - `J.mac` answers E's three requests and not `J.ipad`'s: at most three challenges per QR.
  - `J.ipad` shows no code; J sees three prompts whose codes `J.ipad` doesn't show, declines them, and shows a new QR.
- **Invariants:** INV-UX
- **Notes:** The limit trades availability for the 3-in-a-million guess bound; a captured QR can deny a pairing. The test records that the user can recover by showing a new QR.

### ID-042 A phone migration that carried the device ID
- **Kind:** fault
- **Design:** §5.3, §6.3, §9.1, §9.2
- **Binding:** any
- **Setup:** Standard. J migrates to `J.new` with a transfer that copies app data. A non-conforming build stored the device ID in app data, so `J.new` starts with `J.phone`'s device ID and replica. `J.phone` stays in use.
- **Steps:**
  1. J types `J.bk` on `J.new` (or pairs it from `J.mac`).
  2. `J.phone` and `J.new` each set `milk.title` while offline from each other; both sync.
- **Expect:**
  - A conforming SDK picks a new device ID at step 1 because the secret was installed, whatever the app data held.
  - Step 2 leaves two siblings, both reported.
  - Control run (harness forces the copied device ID): the two writes have colliding dots, and one silently supersedes the other on some devices. The harness records this to show why §5.3's rule exists.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE

### ID-043 The share extension shares the app's device ID and clock
- **Kind:** edge
- **Design:** §17 (extension direct-save), §9.1, §5.3
- **Binding:** any
- **Setup:** Standard. `J.phone` app in the foreground.
- **Steps:**
  1. The app and the share extension each write to Groceries 200 times within the same 2 s.
- **Expect:**
  - All 400 dots are `(J.phone's device ID, hlc)` with distinct, strictly increasing HLCs: both draw from the one HLC in the shared container, under a lock.
  - No write supersedes another it didn't see.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE
- **Notes:** Extension actions are outside the step vocabulary.

### ID-044 Two of the owner's devices disagree after starting over on only one
- **Kind:** edge
- **Design:** §6.4, §11.7
- **Binding:** any
- **Setup:** J started over on `J.mac` (ID-021). `J.ipad` was offline and still holds J's old `US`.
- **Steps:**
  1. `J.ipad` comes online and J keeps editing the old Groceries on it.
- **Expect:**
  - `J.ipad` is still J and sees nothing of J2; nothing on the hub links the two identities.
  - `J.ipad`'s edits land in the old Groceries, which A should have left.
- **Invariants:** INV-CONVERGE
- **Notes:** The "Start over" flow is per device (§6.4, §13.2). Telling the user's other devices is the app's job; the protocol has nothing for it.

---

Retired: ID-007, ID-012, ID-016, ID-017, ID-018, ID-022, ID-024, ID-026, ID-027, ID-030, ID-034, ID-035 (device chains, revocation, recovery keys and pointers, per-user key generations: withdrawn designs, §19).
