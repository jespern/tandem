# The Swift device engine

`TandemEngine` is the development implementation of a Tandem v2 device for Apple platforms, used by Liszto. Independent security review and broader live-provider testing remain acceptance gates. `Tandem` (the core) has the primitives and the control-object codec; the engine runs a hub on top of them. This file is the plan and the state of the work, kept current so a later session can pick it up.

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
| Collections | roster chain (statement JSON v2), epochs, inventories, data writes, merge, grants, answers, leave, key requests | §7–9, §11 | Owner and members **done**: `createList` (genesis statement, key into home), records (`setField`, `deleteRecord`, `value`, `records`, `lists`), roster and data-write checks (§8.2, §8.3 rule 1), reload from the store; `share` (same-epoch statement with the member's floor, grants to every admitted keychain), offers checked against the roster in their channel (§11.4), `pendingOffers`, `accept` (answer and key into home). Key changes **done** (`remove`, `setRole`, `rekey`; port of `testbed:sdk/go/device/rotate.go`): epoch starts as drafts until announced in the old channel, the six journalled steps with every crash point tested, `resume`, `pendingChange`/`finish`/`discard` from another owner device, settled leftover notes, grants to the remaining members, old-channel cleanup through a message-contents index, members moving over through the inventory, late writes in a replaced epoch refused, a dead roster detected and copied into a new list (`copy`, `copyOf`, §11.8), `listsNeedingRekey` and writes held back while a revoked device's key is exposed. Late writes signed again by their author with resign.go's forged-claim protections, possibly unshared work reported (`unshared`), listed writes the new channel lacks carried by any device that holds them. Declining, leaving and key requests to do |
| Flows | create hub, accept invitation, pair (both sides), approve/reject, revoke, share, rotate, password, recovery | §6.1–6.4, §6.7–6.8, §11, §13 | `changePassword` (old capsules destroyed), `invitations`/`cancelInvitation` (join refuses a cancelled one), `recoverCreator` **done**. | Founding, `invite` (capability, home record, link JSON) and `Device.join` (pins the genesis from the link, admits itself under the capability, starts its home) **done**; pairing **done** (`startPairing`, `Device.pair`, `pendingClaims`, `approve`/`reject` journalled and resumed, `cancelPairing`, release and package, completion receipt, home keys handed to a paired keychain the epoch's recipients leave out); revocation, rotation to do |
| Sync | change feed → dispatch by channel tag → hold/validate/merge; publish, debounce, garbage collection, healing | §10.7, §12 | Dispatch, holding and `sync()` (serialized, chained) **done**; `run()`: resume journals, then sync on every IDLE push, failures reported as events, tested live on `MemoryHub` and on Stalwart (pairing and a key change in 0.3 s). Records published with their whole state; garbage collection (dominated messages, the hash rule for mutual ones) and healing (records, the roster line) in current channels **done**. Without §10.7's random delay before a merged publish; the hash rule settles duplicates. The ~1 s publish debounce is the app's (coalesce edits before calling) |
| App API | lists and records, people, events, actions as async functions | §13 | `Item` (values, concurrent values, set elements, fields, attribution), `items`, `add`/`remove`, `rename`, `members`, `people`/`setName`, `decline`/`leave`/`memberStatus`/`delete`, `events()` (`DeviceEvent`), `Device.reopen`, `Keychain`, offline writes sent by the next sync **done**. Guide: `INTEGRATING.md` |

## Independent review (2026-11-13)

A review agent attacked the engine and proved seven bugs, each now a test in `ReviewFindingTests` that fails on the code before its fix: another participant could start someone's home epoch and read their list keys; a forged grant could replace an epoch key; a revoked owner keychain could extend the roster in the same epoch; deleting an announcement stranded a lagging member where the removed one could read; late writes of another keychain were signed again as one's own; a stale device sharing killed the list; a member added after a key change couldn't use the list. The fixes: keys from the inbox, grants, packages and recovery boxes are candidates until the signed start or statement commits to them; home epochs must be signed by the participant's own keychain, extend their parents' policy and admit their recipients; same-epoch statements extend the owner floor; a verified grant counts as the announcement (§11.6) and old channels' announcements are healed; only the same keychain and installation signs a late write again; same-epoch changes sync and check before publishing. Also fixed from its list: inventory fragments healed and checked before step 6; a dropped change clears its own note; one-part inventories can't be blocked by bogus fragments; the late-write marker is set after the write is stored; removed members don't follow (`removedLists`); revoked keychains' profiles and pairing cancellations are refused; one key change per list at a time; a share's grant survives a crash. Open from it: another admin can't finish a certified pairing whose release never went out (the approving device's journal finishes it).

## Asked by Liszto, 2026-11-15 (branch `liszto/api`)

Built by the Liszto session at Jesper's request, from its handover; tests in `AppWriteTests`.

- **`write(list:record:_:)`** is public: several changes of one record, signed together, one message. It's the internal batched `writeRecord`.
- **`Device.metaRecord`** is public, so the owner can keep app properties (icon, kind) in `_meta`.
- **Home writes are local first, like list writes.** `writeHome` saves, then publishes; if the hub can't be reached it reports `.syncFailed` and the next sync's healing publishes it (§12.2). It always saved before publishing, so a throw never undid anything: it only stopped the caller halfway (an invitation's record with a label and no capability, a pairing receipt sent twice). What must be on the hub before a next step syncs first, which heals or throws: a key change's note before its upload (§11.6 step 3). The one part that still needs the hub is rotating home after a revocation (`rotateHomeIfNeeded`): nothing new goes into an exposed epoch, so that throws offline. `createList` and `accept` rotate before they store anything.
- **`createList` survives a hub that can't be reached**, so a caller never ends up with a created list it can't name (an orphan "Untitled" on every device). Its roster publish is left to healing too.
- **`accept(list:)` survives a hub that can't be reached and a crash.** The answer is written last: it closes the offer, so a crash before it leaves the offer open to accept again (crash point `accept:keys`). A lost answer receipt only leaves the owner seeing "pending".
- **An unanswered offer survives a restart.** Reopening loaded only followed lists, so an offered list was missing: its offer couldn't be checked again and `pendingOffers()` lost it for good. Now the offered list loads whole from the store (`loadCollection`), roster and records; with the roster alone, accepting it after a restart showed an empty list with no name until the next launch.
- **Not done: a removed member's writes that never reached the list.** A device can't tell them apart: it can no longer read the new epoch, so it doesn't know which of its late writes the new inventory took.
- **Go engine:** the same, tests in `app_test.go`: local-first home writes, offline `CreateList`/`Accept`, answer last, offers across restarts, and the API as `Write(ctx, list, record, Set(…), Add(…), Remove(…), Delete())` and `MetaRecord()`.

## Paged changes (2026-11-15)

`ChangeBatch.more` (Go `More`, as the TypeScript binding already had): a store may return one page of what's new, and the caller asks again with the new cursor at once. Both engines' sync takes a page at a time, saving each with its cursor in one transaction and applying it before the next, so a first sync of a big hub holds one page in memory and keeps what it took if it stops; `followUp` runs once at the end. A page that says there's more without moving the cursor is an error, not a loop. IMAP: `pageSize` (Go `PageSize`), default 500; new UIDs lie below UIDNEXT, so when the gap is smaller than a page it fetches `n:*` as before, else it searches the new UIDs and fetches the first page. JMAP (Swift, Go, Python): as the browser binding, a full scan lists IDs into the cursor's `pending` and each call delivers a page of them; `Email/changes` is one call per page, `hasMoreChanges` is `more`. This replaces the hard "batch byte limit exceeded" on a big first scan. `MemoryHub` has a page size for tests (`PagingTests`, Go `TestAppSyncsAPageAtATime`).

## Files (DESIGN.md §17, 2026-11-15)

`Blobs.swift` and `BlobGC.swift`, the same as the Go engine's `blobs.go` and `blobgc.go`. A file attached to a record field
`_blob:<name>` is inline up to 16 KiB; larger content is described off the actor (one pass, sealing each chunk to learn
its address), signed as a **pending write** kept in the store's `uploads` table and out of the replica, uploaded by a
worker on the `BlobHub`'s own connections, and merged and published only once every chunk is verified on the hub. The
store's `chunks` table is the blob folder's index (claim, tag, first seen by this device's clock, verified or rejected);
content lives in files in `Store.blobDirectory`, never in SQLite. After each sync the worker collects what nothing protects
(live, held, pending, or the version still on the feed while its replacement isn't), keeps the lowest verified copy,
collects orphans after a grace on this device's clock, and heals chunks of live manifests it holds. It refreshes the
index for that only when the local index shows something to collect, when it should check content it holds (once after
`useBlobs`, or on `checkBlobs()`), or when what it lists or protects changed; so a chunk orphaned by another device's
dead upload is found the next time something changes. The worker, downloads and quota count as users of the blob hub; the
last to finish lets the connections go (found by interop: a download broke when the worker released under it).
`BlobTests` (18 scenarios, one per review finding among them) and `BlobReleaseTests`; `interop/interop.py`'s `blobs`
scenario runs both engines on Stalwart. Not done: a one-batch transfer on the feed connection (`IMAPStore.appendChunks`
exists, the engine doesn't use it yet); requested downloads ahead of unasked ones; pipelined appends for a server with
`LITERAL+` but no `MULTIAPPEND`.

## Garbage collection (2026-11-15)

`GC.swift`, the same as the Go engine's `gc.go`: what `DESIGN.md` already allows, after each sync. Writes of past epochs
that the current inventory doesn't list go once the inventory is here (§9.3), after this participant's late writes are
settled and unless `unshared` reported them; a dropped manifest's addresses are kept as `blobs.dropped.*` so its chunks
are still collected. A list deleted (§11.9) or this participant was removed from (§11.6) keeps its replica read-only for
`setRetention` (30 days by default) on this device's own clock from when it first saw that; then the replica goes but for
its roster, and the list is in `goneLists()` and out of `removedLists()`. A device of a deleted list's owner also destroys
every message it merged in the list's channels that carries no roster write, and every chunk of the list. Downloaded and
partial files no record names go after each sync, with their markers. `GCTests` mirrors Go's `gc_test.go`; interop's
`collecting` scenario runs it across engines. Interop found two bugs on the way, both fixed in both engines: the owner's
device refused the writes it carried into a new channel because they arrived before its statement was announced (now
held, `testOwnCarriedWritesAreMerged`), and a member's device carried writes back into a deleted list after the owner
purged it. Since then (`GC.md`, all implemented): a write of the current epoch goes once a held eligible write on the feed replaced it (an own one only for a later own one) and no message holds it; the owner's devices pack single tombstone messages past `setPackThreshold` (50), and cut old channels' rosters to their epoch's statements; at a key change a deleted record becomes a deletion mark in the inventory, which carries no tombstone, takes no write and is carried forward. A record with no write in the current epoch isn't in `collections` any more (it showed as an empty item before). Signing again follows §11.6 as amended: a later own write, or a write of the new state (listed, or written in the new epoch) whose claim isn't forged, keeps a late write from being signed again; nothing else does.

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
