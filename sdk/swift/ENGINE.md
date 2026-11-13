# The Swift device engine

`TandemEngine` is the production implementation of a Tandem v2 device for Apple platforms: what Liszto links. `Tandem` (the core) has the primitives and the control-object codec; the engine runs a hub on top of them. This file is the plan and the state of the work, kept current so a later session can pick it up.

## Shape

One `Device` per installation per hub, an actor. Everything it knows is in a `Store` (durable, local) and everything it shares is on the hub through a `PushStore` (the mail binding). The app talks to the device; the device never blocks the app on the network.

| Layer | Type | Spec | State |
|---|---|---|---|
| Mail binding | `TandemIMAP.IMAPStore: PushStore` (Network.framework, no dependencies), `MemoryHub` for tests | §10.1–10.4 | **Done**: IMAP binding (30 tests; Stalwart integration with `TANDEM_IMAP_STALWART=1`, ~2 ms push on loopback; IDLE starts with a pipelined `NOOP` because Stalwart reports a change made just before IDLE only on the next command); `MemoryHub` (push, removals, hiding and dropping as a hostile hub) |
| Password stretching | `Argon2id` and BLAKE2b in the core, pure Swift; `Control.openCapsule`/`sealCapsule` | §6.7 | **Done**: 1.2–2 s and 1.1 GB at the real profile. In their own target, `TandemArgon2`, built optimized in every configuration (an unoptimized stretch took about 80 s, which hung apps' debug builds; found by the Liszto session). `TANDEM_SKIP_ARGON2_GIB=1` still skips the 1 GiB tests for a quicker run |
| Registry | `Registry`: certificates, revocations, snapshots, floors | §6.9 | **Done**, tested against the vectors and the Lean attacks |
| Durable state | `Store`: values, control objects, message index, writes, journal; SQLite from the SDK (`import SQLite3`, a system library), WAL with full synchronous commits | §11.6 journal rules | **Done** (tables and transactions; the engine decides what goes in them) |
| Hub authority | genesis pin, directory channel, capsule unlock, profiles | §5.3, §6.6–6.8, §10.8 | Founding **done** (`Founder`: genesis, creator certificate, snapshot, first home epoch, inbox delivery, recovery box); `Founder.capsule` and `Device.unlock` **done**. `Device` publishes a founding (journal first, no duplicates on resume) and syncs the directory channel into its registry **done** |
| Homes | home epochs (several heads), home records, recovery box | §6.2, §10.8 | First epoch **done**: home keys through the inbox, home-epoch starts in their channel, verified home writes merged and stored, `setHome`/`addHome`/`homeValue`; a second installation on a synced keychain finds its identity and shares the home. Rekeying **done** (`rotateHome`, automatic on a revocation of this participant's keychain: journalled, inventory fragments, carried writes, earlier starts copied for later devices, heads joined, late writes in replaced epochs refused, revoked replica kept read-only) |
| Collections | roster chain (statement JSON v2), epochs, inventories, data writes, merge, grants, answers, leave, key requests | §7–9, §11 | Owner and members **done**: `createList` (genesis statement, key into home), records (`setField`, `deleteRecord`, `value`, `records`, `lists`), roster and data-write checks (§8.2, §8.3 rule 1), reload from the store; `share` (same-epoch statement with the member's floor, grants to every admitted keychain), offers checked against the roster in their channel (§11.4), `pendingOffers`, `accept` (answer and key into home). Key changes **done** (`remove`, `setRole`, `rekey`; port of `testbed:sdk/go/device/rotate.go`): epoch starts as drafts until announced in the old channel, the six journalled steps with every crash point tested, `resume`, `pendingChange`/`finish`/`discard` from another owner device, settled leftover notes, grants to the remaining members, old-channel cleanup through a message-contents index, members moving over through the inventory, late writes in a replaced epoch refused, a dead roster detected, `listsNeedingRekey` and writes held back while a revoked device's key is exposed. Late writes signed again by their author with resign.go's forged-claim protections, possibly unshared work reported (`unshared`), listed writes the new channel lacks carried by any device that holds them. Declining, leaving and key requests to do |
| Flows | create hub, accept invitation, pair (both sides), approve/reject, revoke, share, rotate, password, recovery | §6.1–6.4, §6.7–6.8, §11, §13 | `changePassword` (old capsules destroyed), `invitations`/`cancelInvitation` (join refuses a cancelled one), `recoverCreator` **done**. | Founding, `invite` (capability, home record, link JSON) and `Device.join` (pins the genesis from the link, admits itself under the capability, starts its home) **done**; pairing **done** (`startPairing`, `Device.pair`, `pendingClaims`, `approve`/`reject` journalled and resumed, `cancelPairing`, release and package, completion receipt, home keys handed to a paired keychain the epoch's recipients leave out); revocation, rotation to do |
| Sync | change feed → dispatch by channel tag → hold/validate/merge; publish, debounce, garbage collection, healing | §10.7, §12 | Dispatch, holding and `sync()` (serialized, chained) **done**; `run()`: resume journals, then sync on every IDLE push, failures reported as events, tested live on `MemoryHub` and on Stalwart (pairing and a key change in 0.3 s). Records published with their whole state; garbage collection (dominated messages, the hash rule for mutual ones) and healing (records, the roster line) in current channels **done**. Without §10.7's random delay before a merged publish; the hash rule settles duplicates. The ~1 s publish debounce is the app's (coalesce edits before calling) |
| App API | lists and records, people, events, actions as async functions | §13 | `Item` (values, concurrent values, set elements, fields, attribution), `items`, `add`/`remove`, `rename`, `members`, `people`/`setName`, `decline`/`leave`/`memberStatus`/`delete`, `events()` (`DeviceEvent`), `Device.reopen`, `Keychain`, offline writes sent by the next sync **done**. Guide: `INTEGRATING.md` |

## Independent review (2026-11-13)

A review agent attacked the engine and proved seven bugs, each now a test in `ReviewFindingTests` that fails on the code before its fix: another participant could start someone's home epoch and read their list keys; a forged grant could replace an epoch key; a revoked owner keychain could extend the roster in the same epoch; deleting an announcement stranded a lagging member where the removed one could read; late writes of another keychain were signed again as one's own; a stale device sharing killed the list; a member added after a key change couldn't use the list. The fixes: keys from the inbox, grants, packages and recovery boxes are candidates until the signed start or statement commits to them; home epochs must be signed by the participant's own keychain, extend their parents' policy and admit their recipients; same-epoch statements extend the owner floor; a verified grant counts as the announcement (§11.6) and old channels' announcements are healed; only the same keychain and installation signs a late write again; same-epoch changes sync and check before publishing. Also fixed from its list: inventory fragments healed and checked before step 6; a dropped change clears its own note; one-part inventories can't be blocked by bogus fragments; the late-write marker is set after the write is stored; removed members don't follow (`removedLists`); revoked keychains' profiles and pairing cancellations are refused; one key change per list at a time; a share's grant survives a crash. Open from it: another admin can't finish a certified pairing whose release never went out (the approving device's journal finishes it).

## Several processes on one store (Liszto, 2026-11-13)

The Liszto session asked for two things, decided with Jesper, now done (`SharedStore.swift`, `SharedStoreTests`): the running `Device` takes in another process's writes, checking `PRAGMA data_version` at the start of every sync, write and read and on `refresh()`, reloading the changed records and announcing them; and `ItemWriter`, a light writer for extensions that loads one list and one record, signs several changes in one call, appends once with a timeout and leaves the rest to the app. Its tests fail without the data-version check.

## Rules the engine keeps

- **Event-driven.** Work happens on a push (IDLE), a local action, a reconnect or a start. The only timers are the protocol's own safety nets: the 10-minute key-request fallback (§11.4), the IDLE refresh, Gmail's IDLE restart (§10.3) and the publish debounce (§10.7).
- **Journal before export.** Every signed object that leaves the device for a management action or key change is persisted, with its exact bytes, before the first append (§6.1, §11.6). A restart resumes from the journal; nothing is signed twice for the same step.
- **Hold, don't reject, what's incomplete.** Validity is three-way (`Validity.pending`): an object whose dependencies haven't arrived waits, with configurable limits.
- **Bytes, not strings.** Protocol text is compared as UTF-8 bytes (§10.5).

## Order of work

1. ~~Registry~~.
2. ~~`MemoryHub` and `Store`; a `Device` that founds a hub, with its capsule, and another that reads it and unlocks management~~.
3. ~~Home on one participant's two installations; collections on them: create a list, write records, see them on the other device~~.
4. ~~Invitations: a second participant joins; sharing, grants, accepting~~. Declining and leaving still to do.
5. ~~Pairing with approval and rejection~~; ~~revocation (`revoke`) with the home rekey~~; ~~list rekeys~~.
6. ~~Rotation with the journal and crash points~~; ~~late writes signed again~~; ~~garbage collection; healing~~.
7. The IMAP binding in place of `MemoryHub`: ~~the invite → join → share → accept → edit path on Stalwart~~ (`EngineOverStalwartTests`, with `TANDEM_IMAP_STALWART=1`); then Fastmail and Gmail.
8. ~~The app API Liszto uses~~ (`INTEGRATING.md`).
9. ~~Kept grants and key requests (§11.4)~~; restoring a wiped hub (§12.4); engine runs on Fastmail and Gmail (`EngineOverProviderTests`).

## Checking

- Vectors: `vectors/v2/*.json` (core and control) through `ControlTests` and `CoreTests`.
- Registry: `RegistryTests`.
- Multi-device behaviour: scenario tests on `MemoryHub`; the testbed's scenarios (`testbed:scenarios/`) are the list to translate.
- Real mail: Stalwart in Docker (`conformance/stalwart.py` has the pinned image), then `probes/imap_probe.py`-style runs on real providers.
