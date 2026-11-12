# Tandem scenarios

A catalogue of situations the protocol must handle, from everyday use to deliberate attacks. Each scenario is written so that a person can review it today and a test harness can execute it once a Tandem SDK exists: fixed actors, steps from a small vocabulary, and checkable expectations.

The protocol is specified in `DESIGN.md` at the repository root. Every scenario cites the sections it exercises. When the design changes, scenarios citing the changed sections are re-checked.

**Status:** refreshed on 2026-11-12 against the frozen draft (`main` at `f4ce2e6`, 999 lines): one secret per user, one owner per collection, the journalled six-step key change, inventories, signing again, dead collections, invites in the owner's home, and one reading of every message. 340 scenarios; 62 open gaps, numbered G-01 to G-64 (below).

## Files

| File | Prefix | Count | Area |
|---|---|---|---|
| `identity.md` | `ID` | 32 | First run, home, pairing and its code, the backup key and restoring, device IDs, lost and stolen devices, starting over |
| `records.md` | `RC` | 42 | CRUD, field semantics, conflicts, sets, tombstones, unknown fields, clocks, signing again, held writes, the vector limit, eligibility |
| `sharing.md` | `SH` | 38 | Creating, invites in home, join requests, approvals and declines, grants and key requests, leaving, `_meta`, deleting, copying |
| `rotation.md` | `RO` | 43 | Epochs, the six-step key change and its crashes, the `flight` note, drafts, inventories, signing again, dead collections |
| `hub.md` | `HB` | 47 | JMAP and IMAP bindings, server quirks, message format, GC and its tie-break, healing, what is never pruned, hostile hub participants |
| `faults.md` | `FT` | 33 | Network faults, offline devices, crashes mid-operation, clock trouble, credentials, quota |
| `collab.md` | `CO` | 37 | One list, 1–10 players collaborating: adds, edits, reorders, nesting, sleep, outages |
| `permissions.md` | `PM` | 23 | The owner/writer/reader capability matrix; the world after every roster and key change |
| `realtime.md` | `RT` | 19 | Live updates over JMAP push and IMAP IDLE; dropped connections and catch-up |
| `scale.md` | `SC` | 14 | Volume, many devices, long rosters, kept tombstones, the vector limit |
| `reading.md` | `RD` | 12 | One reading of every message: repeated names, numbers, UTF-8, `b64u` spellings, bytes not strings |

Counts include families. Against the catalogue before the refresh (323), 284 scenarios were kept (most of those in `identity`, `rotation`, `sharing` and `permissions` rewritten), 39 retired and 56 added. Each file lists its retired IDs in its footer.

## Actors

Users and their devices are fixed across all scenarios, so steps stay short.

| Actor | Who | Devices |
|---|---|---|
| **J** | Jesper. Sets up the hub; owns most collections. | `J.phone`, `J.mac`, `J.ipad` |
| **A** | Aimee, Jesper's wife. | `A.phone`, `A.mac` |
| **B** | Bo, a friend. | `B.phone` |
| **C** | Carla, a friend. | `C.phone` |
| **D** | Dana, a friend. | `D.phone` |
| **P1**–**P10** | Generic players in parameterised families (`collab.md`, `permissions.md`). P1 is the owner unless a family says otherwise. | `P3.d1`, `P3.d2`, … as the family sets |
| **M** | Mallory, a member who misbehaves within what her keys allow. Runs a modified client that can sign anything. | `M.phone` |
| **E** | Eve. Holds the hub credential (an ex-member, or someone who saw an invite or a QR) but no collection keys. | raw hub access only |
| **T** | A thief holding one of someone else's unlocked devices, and so their user secret, e.g. `T(J.phone)`. | the stolen device |
| **Hub** | The mail server and its operator. | |

`J.bk` is Jesper's backup key (DESIGN §6.3). A device written `J.new` is a device being added or restored; `J2` is Jesper after starting over with a new identity (§6.4). Every device of a user holds the same secret and has its own random device ID.

Epochs are written `E0`, `E1`, …: a label for the epoch started by that statement. The protocol knows an epoch only by the full ID of its starting statement (§7.1); no check may use the label's number. `K(E3)` is that epoch's key, "channel `E3`" its channel, and `inv(E4)` the snapshot inventory `E4` commits. The six steps of a key change (§11.6) are **S1**–**S6** (`rotation.md`).

## Scenario format

````markdown
### ID-002 Add a Mac by scanning a pairing code
- **Kind:** real-world
- **Design:** §6.3, §5.4, §13.2
- **Binding:** any
- **Setup:** J has only `J.phone`. He owns Groceries, with A as writer.
- **Steps:**
  1. `J.phone pairs J.mac`; both show a six-digit code; J approves.
  2. `J.mac syncs`.
- **Expect:**
  - The reply opens to J's `US`, which gives the `uid` in the QR.
  - `J.mac` picks a new device ID and shows every active membership within one sync.
  - No roster changes, and A sees nothing.
- **Invariants:** INV-HUB-BLIND, INV-BYSTANDER
````

Field rules:

- **ID** is prefix + three digits, never reused. Retired IDs are listed, with the reason, in the footer of their file.
- **Kind** is one of:
  - `real-world`: what users actually do;
  - `edge`: legitimate but unusual;
  - `adversarial`: someone misbehaves on purpose (M, E, T or the hub);
  - `fault`: crashes, network failures, clocks, server quirks;
  - `ux`: what the user sees and does.
- **Binding** is `any`, `jmap` or `imap`. `any` scenarios run against both.
- **Steps** use the vocabulary below. Anything outside it is written plainly and flagged in **Notes** so the harness can grow to cover it.
- **Expect** lists observable outcomes: what devices show, what the hub holds, what the app is told. Each item must be checkable by a harness, not "works correctly".
- **Invariants** names the global properties (below) that must hold after the scenario. All scenarios implicitly check `INV-CONVERGE` once every device has synced.
- **Notes** (optional): design ambiguities the scenario exposes, open questions (`Q12`), or what makes it exotic.

A scenario that finds a gap or contradiction in the design says so in **Notes**, starting with `GAP (G-nn):`. These are the most valuable ones; the list is under "Open gaps" below. The same gap found by several scenarios has one number.

## Step vocabulary

| Step | Meaning |
|---|---|
| `X.dev online` / `offline` | Network reachability of a device. Offline devices keep working locally. |
| `X.dev syncs` | Runs `changes`, fetches, merges, publishes queued writes, until quiescent. |
| `X.dev quits` / `launches` | App stops (no background work) or starts (catch-up sync on launch). |
| `X.dev crashes after <step>` | Process dies at a named point, e.g. after appending the genesis but before sending grants. |
| `X.dev clock = <offset>` | Shifts that device's wall clock, e.g. `+3 days`, `-1 year`. |
| `X.dev creates collection <name>` | New collection, X as owner. |
| `X.dev adds <item> to <coll>` | Creates a record. |
| `X.dev sets <item>.<field> = <value>` | Assigns a register field. |
| `X.dev tags <item> +t` / `-t` | Set add / remove. |
| `X.dev deletes <item>` | Writes a tombstone. |
| `X.dev moves <item> to <coll>` | Create in target, delete in source. |
| `X.dev invites (qr\|link) to <coll> as <role>` | Creates an invite. |
| `Y.dev joins via <invite>` | Scans or opens it; posts the join request. |
| `X.dev approves Y` / `declines Y` | Acts on a join request. |
| `Y.dev leaves <coll>` | Posts a leave notice; the owner confirms it (§11.5). |
| `X.dev makes Y a <role>` / `removes Y` / `asks for a new key` | Owner-only roster changes. Removing or demoting starts a key change (§11.6). |
| `X.dev deletes <coll>` / `copies <coll>` | Delete (owner, §11.9); copy into a new collection the copier owns (§11.8). |
| `X.dev pairs X.new` | Pairing (§6.3): QR, request, signed challenge, six-digit code on both screens, approval, sealed reply. |
| `X.new restores with X.bk` | Types the backup key on a device with the hub configured (§6.3). |
| `X.dev starts over` | New identity on that device (§6.4); written `X2` afterwards. |
| `X.dev crashes after S<n>` | Crash after step *n* of the key change (§11.6): S1 sync, S2 key/inventory/statement/note/journal, S3 upload, S4 announce, S5 grants, S6 clean up. |
| `X.dev finishes` / `discards` the pending change | §11.7, on another device of the owner while a `flight` note is in home. |
| `X.dev signs again <write>` | §11.6 signing again; usually implicit when a device moves epoch. |
| `X.dev sends a key request` | §11.4; normally after 10 minutes without a grant. |
| `hub: destroy fragment <n> of inv(E)` / `hub: destroy statements of <coll>` | Targeted destroys of inventory fragments, the roster, or the two statements that prove a collection dead. |
| `check dead <coll>` | Every device holding both conflicting statements treats the collection as dead (§9.4). |
| `hub: destroy <selector>` | E or the hub deletes messages: `all`, `channel <tag>`, `record <id>`, `grants for Y`, `latest N`. |
| `hub: replay <message>` | Re-appends a previously captured message. |
| `hub: rollback <channel> to <time>` | Replaces a channel's messages with those it had at an earlier time. |
| `hub: withhold <selector> from X.dev` | The hub hides messages from one device's view. |
| `hub: move <message> out of / into Tandem` | Moves a message between folders. |
| `hub: inject <bytes>` | Appends arbitrary bytes or malformed messages. |
| `hub: rewrite charset <message> as <charset>` | Stores a body in another charset or transfer encoding, as a JMAP server may. |
| `hub: reject auth for X.dev` | Credential revoked or changed. |
| `net: latency <ms>` / `partition X.dev` / `drop after <n> bytes` / `reset` | Network faults between a device and the hub (Toxiproxy in the testbed). |
| `M.dev signs <write>` | Mallory's modified client signs an arbitrary write: malformed contexts, reused dots, wrong `ref`, oversized vectors, repeated JSON names, non-canonical `b64u`. |
| `T(X.dev) …` | The thief uses the stolen device, and so the user's secret, with any of the above. |
| `wait <duration>` | Simulated time passes (the harness controls clocks; 30-day and 180-day periods are not waited out in real time). |
| `X.dev sleeps` / `wakes` | A phone in a pocket: suspended, no network, no push; `wakes` brings it to the foreground, which runs the catch-up sync on launch. Differs from `offline`, where the app runs but can't reach the hub. |
| `X.dev backgrounded` | iOS background: the app may get a background refresh but holds no push connection. |
| `net: outage [X.dev, …]` / `net: restored` | The hub is unreachable for several devices at once (home Wi-Fi down), or for all of them when no list is given. |
| `hub: restart` | The mail server restarts; open push and IDLE connections drop. |
| `X.dev reorders <item> after <item>` / `to top` | Moves an item within its list (rewrites its `sortKey` only, Liszto §6.1). |
| `X.dev indents <item> under <item>` / `outdents <item>` | Sets or clears `parentID`. |
| `X.dev completes <item>` / `uncompletes <item>` | Sets or clears `completedAt`. |
| `X.dev edits <item>.notes` | A notes edit (conflicting text is kept, Liszto §17). |
| `check world` | Compares every user and device with the expected world (`permissions.md`, "World checks"): role, status, epoch, keys held, `flight` notes, dead or alive, what it can decrypt, what each action does. |
| `check state <coll> = <expected>` | Compares the list as every device renders it: items, order, nesting, completion, tags, titles, conflicts reported to the app. |
| `measure latency <event> on <devices>` | Time from a write being published to it showing on each named device (`realtime.md`). |

## Families

Some behaviour can't be covered by hand-picked cases: the interesting bugs hide in interleavings nobody thought to write down. A **family** is a scenario with parameters, run many times by the harness with different seeds:

````markdown
### CO-F03 Family: outage while everyone keeps editing
- **Kind:** fault
- **Design:** §9.2, §9.3, §10.7
- **Parameters:** players 2–10; devices per player 1–3; ops per player 20–200 from mix M-LIST; schedule S-OUTAGE; bindings all-jmap | all-imap | mixed; 200 seeds per combination
- **Oracle:** the reference model over every write made
- **Expect:**
  - after `net: restored` and all devices syncing, `check state` matches the oracle on every device
  - …
````

Field rules beyond the scenario format:

- **Parameters** name ranges and the schedule (`S-…`) and op mix (`M-…`) defined in the file. The harness samples the cross-product and records the seed of every failure, so a failure replays exactly.
- **Oracle** names what computes the expected outcome. Families never list expected states by hand.

### The reference model

The oracle for every family. It is a small, pure implementation of the merge rules (DESIGN §9.2–§9.3) plus the Liszto rendering rules (order by `sortKey`, ties by record ID; one level of nesting with orphans at top level; the completion repair rule), written independently of any SDK. Given the set of valid writes, it returns the state every device must show.

It also checks **intent**: when a player made a change after their device had synced someone else's change to the same field, the later change must be what everyone sees, with no conflict reported. Concurrent changes may resolve either way the design allows, but identically everywhere.

The reference model is the executable Lean model in `proofs/`, so the proved model and the oracle are the same code (`TESTING.md`). It covers the merge rules (theorems `Supersession`, `Register`, `ORSet`, `Tombstone`, `Display`, `HLC`) and the roster rules (`Roster`, `Eligibility`, `Reissue`, `VectorLimit`, `GC`). It is cross-checked against the design's Python model, `model/owner_model.py`. The harness checks the SDK against the oracle.

## Global invariants

Checked after every scenario unless the scenario says otherwise and why.

| Invariant | Holds when |
|---|---|
| `INV-CONVERGE` | Every device following a collection holds the same state of it after all have synced, regardless of order. |
| `INV-HUB-BLIND` | No plaintext record content, collection name, user ID, device ID or display name appears anywhere on the hub (headers or bodies). |
| `INV-SIGNED` | Every write and signed object any device accepted verifies under the strict Ed25519 rules (§5.5) against its author's user ID. |
| `INV-NO-LOSS` | No write accepted by an honest device is lost, except where the design says it is dropped (superseded; a late write of an author who never returns that missed the inventory, §11.6; a dead collection's unmerged data), and then the app was told when the design says it is. |
| `INV-AUTHZ` | No device applies a statement, grant or data write its author wasn't entitled to make, or a data write that isn't eligible in its epoch (§8.2, §8.3, §11.4). |
| `INV-CUT` | Removed members can't decrypt anything published after the announcement of the key change that removed them. |
| `INV-MONOTONE-EPOCH` | No device's epoch for a collection ever moves backwards. |
| `INV-NO-HOLD-FOREVER` | Every held write is eventually applied or dropped once all honest devices have synced. |
| `INV-ONE-OWNER` | Every collection has exactly one owner, the genesis signer, for its whole life; every device follows the same roster, or treats the collection as dead (§9.4). |
| `INV-GC-SAFE` | Garbage collection never leaves the hub with less than the union of what honest devices published, minus what the design lets it drop; rosters, current inventory fragments and the statements proving a death are never pruned (§10.7). |
| `INV-QUIESCE` | After all devices sync with no new edits, they stop publishing: no feedback loops between devices. |
| `INV-UX` | Every error the user can hit has a message from §13.3 or a named new one; nothing fails silently. |
| `INV-ORACLE` | Every device's rendered state equals the reference model's (Families). |
| `INV-INTENT` | A change made after its author had seen the previous value of that field wins, with no conflict reported. |
| `INV-UNIQUE` | Every record appears exactly once in every device's view: no duplicates from retries, healing or forwarding, nothing missing. |
| `INV-BYSTANDER` | An operation changes the world only for those it targets: everyone else keeps their role, keys, epoch membership and every capability (`permissions.md`). |
| `INV-LIVE` | A foreground device with a push connection shows another device's published change within the latency budget (`realtime.md`). |
| `INV-DEAD` | A collection is dead on a device iff it holds two different owner-signed successors of one roster statement (an unannounced draft doesn't count); once dead, always dead, whatever arrives later (§9.4). Convergence of contents is suspended for dead collections. |

## Open gaps

Ambiguities and omissions in the frozen draft found by the scenarios. "Minor" marks wording or UX points. Numbers are stable; gaps the design later settles are struck from this list, not renumbered. (G-10 and G-11 are unused.)

### Protocol

| Gap | Design | Where | Summary |
|---|---|---|---|
| G-01 | §6.3, §11.1–§11.3 | ID-002, FT-015, SH-026 | Nothing destroys leftover kind-3 messages: pairing requests, challenges and the sealed copy of `US` stay on the hub for good, as do invite requests after a close, expiry or decline. That the new device erases its one-time `ek` is unstated. |
| G-02 | §6.3, §13.1 | ID-004, ID-014 | What a device does with local data when the secret is installed by pairing or backup key: a restored replica and queue of existing collections, or writes already signed under another device's ID. |
| G-06 | §11.5, §11.6 | RO-010 | A leave notice made in the epoch the owner is just leaving is lost: its `at` is no longer in the current epoch, and the leaver never re-posts. The member stays listed and keeps receiving keys. |
| G-07 | §11.6 S2, §11.7 | RO-020, FT-035 | Minor. S2 doesn't order the home note and the journal; a device whose note is on the hub but whose journal is lost can't "resume" as §11.7 says. |
| G-08 | §11.6 S4, §11.7, §9.4 | RO-053, RO-009 | Before announcing, nothing checks that the statement's parent is still the head. A draft overtaken by another device's statement could be discarded (a draft isn't a successor), but read literally the device announces it and kills the collection. |
| G-09 | §11.3, §11.4, §6.2 | SH-009, SH-023, SH-037 | Requests refused without a tap (expired, used up, closed) get no answer; a request carries no time; the joiner's home `state` after expiry or decline is undefined (no `expired`/`declined`, and home records are never deleted). |
| G-12 | §11.1, §11.3 | SH-034, FT-021 | Minor. Invite expiry is judged on the approving device's clock with no tolerance; two devices of one owner can disagree. |
| G-13 | §11.2, §13.1 | SH-035, HB-019, HB-034 | Hub identity is undefined, so "different hub" can't be decided for per-person tokens or two bindings of one account. |
| G-16 | §17, §3.3 | FT-024, RC-011 | The share extension appends without syncing, so after a key change it can publish to the old epoch, readable by the removed member, for as long as the app is suspended. |
| G-19 | §10.7 | FT-030, HB-037 | Junk a device can prove is junk (AEAD fails under a collection tag) can never be destroyed. |
| G-20 | §9.1 | FT-017, RC-035 | HLC durability and recovery aren't required; a lost HLC lets an old dot fall under others' contexts and a write is lost silently. |
| G-23 | §9.2, §15 | RC-017 | A write with an unknown `op` value: carried, dropped, or "a version it doesn't understand"? |
| G-26 | §9.2, §11.6 | RC-026 | A future-claiming context, copied into every new write's `base`, pre-supersedes late writes of its epoch; §11.6 then never signs them again, so they vanish silently, after the claimant's removal too. |
| G-27 | §5.5, §9.1, §9.2 | RC-031, RD-005 | Whether vector entries must arrive sorted; a device named twice in one vector; HLC millis above 2^48; a field holding both register and set writes. |
| G-28 | §5.5, §10.5 | RC-034 | The encoding of a `delete` write (field name, vectors), and whether a key change carries a deleted record's other writes. |
| G-35 | §9.2 | RC-028 | The 16-sibling presentation limit tells the app neither the count nor whether its own value is among those shown. |
| G-37 | §9.2 | RC-037 | Whether a set write's context covers the element's kept writes or the whole field's; per field breaks add-wins. |
| G-40 | §10.6, §10.7 | SC-008, SC-010, HB-028 | Records split across messages: domination is per message, so no part is ever dominated and devices republish for ever; no deterministic split. |
| G-43 | §10.6 | SC-008, HB-028 | What the 64 KiB and 1 MiB limits count (plaintext, envelope, stored message). |
| G-44, G-45 | §10.7 | SC-009 | The ~1 s debounce has no maximum wait, and writes in the window needn't be durable. |
| G-49 | §12.2, §3.3 | HB-033 | A destroyed join or pairing request is re-posted by neither side; healing covers only current collection channels and home. |
| G-50 | §15, §11.6, §11.9 | HB-035 | Whether a newer-version message also stops S6 clean-up, a deleted collection's clean-up and invite-channel clean-up. |
| G-60, G-61 | §15 | HB-036 | A forged version-2 message is an unauthenticated off switch for healing and GC; header and envelope versions may disagree. |
| G-62 | §5.4, §15 | HB-037 | How a version-1 envelope of a reserved kind is treated. |
| G-63 | §10.6 | HB-038 | No size limit for readers. |
| G-64 | §10.5 | RD-005 | Minor. Negative integers pass the JSON rule; which fields are unsigned is unsaid. |

### Bindings and operations

| Gap | Design | Where | Summary |
|---|---|---|---|
| G-15 | §13.3 | FT-022 | A TLS failure caused by the device's clock shows only as "offline". |
| G-17 | §13.1, §13.3, §12.4 | FT-028, HB-034 | No reconnect, credential-update or hub-replacement flow that keeps identity and cursors. |
| G-18 | §10.1, §13.3 | FT-029, SC-013 | The abstract store has no error model; no full-mailbox message. |
| G-21 | §10.3 | FT-025, RT-011 | IMAP has no liveness check between 29-minute IDLE renewals. |
| G-22 | §3.3, §13.1 | FT-027, ID-028 | Sessions opened before a credential is revoked may survive it. |
| G-39 | §11.6, §13.2 | SC-003 | No pacing for hundreds of key changes at once; removing one identity from many collections isn't a flow. |
| G-41 | §10.3, §13.3 | HB-017, SC-012, RT-016 | No fallback or message when the IMAP connection cap refuses a device. |
| G-42 | §10.6 | SC-014 | Minor. No value-size figure an app can check before accepting input. |
| G-46 | §10.2, §10.3 | HB-008 | What may move the stored change cursor (only `changes` results). |
| G-47 | §10.3, §17 | HB-017 | The share extension needs a second IMAP connection. |
| G-48 | §10.4 | HB-021 | The charsets a reader must decode, and what an undecodable body does to healing. |
| G-51 | §10.4, §14 | HB-039 | The `Date` header leaks each device's clock offset. |
| G-52 | §10.1–§10.3 | HB-041 | Whether the mailbox is cached or resolved each sync; renamed or deleted folder. |
| G-53 | §10.2, §10.3 | HB-002 | JMAP's fallback under the inbox and IMAP's empty prefix can create two folders. |
| G-54 | §10.2 | HB-003 | Minor. RFC 8621 has no duplicate-name `SetError`; "query again after any failed create". |
| G-55 | §10.2 | HB-004 | Other session limits and `notCreated` handling. |
| G-56 | §10.2 | HB-007 | Which state to store after a full resync, and paging while messages are destroyed. |
| G-57 | §13.3 | HB-011 | No message for a missing IMAP extension or JMAP capability. |
| G-58 | §10.4 | HB-022, HB-023 | Several `Tandem-Channel` headers; non-`text/plain` or multipart bodies. |
| G-59 | §12.2 | HB-029 | Healing has no random delay, so every replica republishes at once. |

### Identity and UX

| Gap | Design | Where | Summary |
|---|---|---|---|
| G-03 | §6.1, §13.1 | ID-015 | Minor. Connecting a hub doesn't ask whether the user already has an identity. |
| G-04 | §6.3 | ID-036 | Whether the scanner checks `exp`; the new device gets no signal that its request went unanswered. |
| G-05 | §6.3, §13.3 | ID-037, ID-038 | Minor. No message for a mistyped backup key; restoring doesn't show whose identity it found. |
| G-14 | §11.2 | SH-036 | Minor. A hubless invite on a hubless device; whether expiry is checked before configuring a hub. |
| G-24 | §11.6 | RC-019 | Minor. The app isn't told when a displayed item was left behind by a key change. |
| G-31 | §9.2, §13.2 | RC-009, RC-038, CO-010 | No signal when an edit lands on a tombstoned record. |

### Liszto (§17)

| Gap | Where | Summary |
|---|---|---|
| G-25 | RC-025 | Automatic notes resolution can loop when a field can't be settled. |
| G-29, G-30 | RC-008 | Undo: whether a signed, unpublished delete may be withdrawn; undo by re-creation changes identity and authorship. |
| G-32 | RC-010 | A sub-item whose parent is deleted. |
| G-33 | RC-016, CO-015 | `parentID` cycles and second levels. |
| G-34 | RC-021 | Concurrent moves duplicate an item. |
| G-36 | RC-036 | A move isn't journalled. |
| G-38 | RC-039 | Which device resolves a notes conflict. |
