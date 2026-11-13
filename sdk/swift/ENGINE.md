# The Swift device engine

`TandemEngine` is the production implementation of a Tandem v2 device for Apple platforms: what Liszto links. `Tandem` (the core) has the primitives and the control-object codec; the engine runs a hub on top of them. This file is the plan and the state of the work, kept current so a later session can pick it up.

## Shape

One `Device` per installation per hub, an actor. Everything it knows is in a `Store` (durable, local) and everything it shares is on the hub through a `PushStore` (the mail binding). The app talks to the device; the device never blocks the app on the network.

| Layer | Type | Spec | State |
|---|---|---|---|
| Mail binding | `TandemIMAP.IMAPStore: PushStore` (Network.framework, no dependencies), `MemoryHub` for tests | §10.1–10.4 | **Done**: IMAP binding (30 tests; Stalwart integration with `TANDEM_IMAP_STALWART=1`, ~2 ms push on loopback; `testSessionEndToEnd` flaky about 1 in 4, being fixed on `swift/imap`); `MemoryHub` (push, removals, hiding and dropping as a hostile hub) |
| Password stretching | `Argon2id` and BLAKE2b in the core, pure Swift; `Control.openCapsule`/`sealCapsule` | §6.7 | **Done**: 1.2–1.4 s and 1.1 GB at the real profile in release; over a minute in debug, so the 1 GiB tests skip under `TANDEM_SKIP_ARGON2_GIB=1` and `sdk/check.py` runs them optimized |
| Registry | `Registry`: certificates, revocations, snapshots, floors | §6.9 | **Done**, tested against the vectors and the Lean attacks |
| Durable state | `Store`: values, control objects, message index, writes, journal; SQLite from the SDK (`import SQLite3`, a system library), WAL with full synchronous commits | §11.6 journal rules | **Done** (tables and transactions; the engine decides what goes in them) |
| Hub authority | genesis pin, directory channel, capsule unlock, profiles | §5.3, §6.6–6.8, §10.8 | Founding **done** (`Founder`: genesis, creator certificate, snapshot, first home epoch, inbox delivery, recovery box); `Founder.capsule` and `Device.unlock` **done**. `Device` publishes a founding (journal first, no duplicates on resume) and syncs the directory channel into its registry **done** |
| Homes | home epochs (several heads), home records, recovery box | §6.2, §10.8 | First epoch **done**: home keys through the inbox, home-epoch starts in their channel, verified home writes merged and stored, `setHome`/`addHome`/`homeValue`; a second installation on a synced keychain finds its identity and shares the home. Rekeying and joining heads to do |
| Collections | roster chain (statement JSON v2), epochs, inventories, data writes, merge, grants, answers, leave, key requests | §7–9, §11 | Owner and members **done**: `createList` (genesis statement, key into home), records (`setField`, `deleteRecord`, `value`, `records`, `lists`), roster and data-write checks (§8.2, §8.3 rule 1), reload from the store; `share` (same-epoch statement with the member's floor, grants to every admitted keychain), offers checked against the roster in their channel (§11.4), `pendingOffers`, `accept` (answer and key into home). Declining, leaving, key requests, rotation to do; port `testbed:sdk/go/device` (rotate.go, resign.go, ops.go) |
| Flows | create hub, accept invitation, pair (both sides), approve/reject, revoke, share, rotate | §6.1–6.4, §11, §13 | Founding, `invite` (capability, home record, link JSON) and `Device.join` (pins the genesis from the link, admits itself under the capability, starts its home) **done**; pairing, approval, revocation, rotation to do |
| Sync | change feed → dispatch by channel tag → hold/validate/merge; publish, debounce, garbage collection, healing | §10.7, §12 | To do |
| App API | lists and records as `AsyncSequence` snapshots; actions as async functions with typed errors | §13 | To do |

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
5. Pairing with approval and rejection; revocation with home and list rekeys.
6. Rotation with the journal and crash points (port the Go engine's crash tests); late writes; garbage collection; healing.
7. The IMAP binding in place of `MemoryHub`: integration tests on Stalwart, then Fastmail and Gmail.
8. The app API Liszto uses, with Liszto's v1 → v2 migration (Q23) designed alongside.

## Checking

- Vectors: `vectors/v2/*.json` (core and control) through `ControlTests` and `CoreTests`.
- Registry: `RegistryTests`.
- Multi-device behaviour: scenario tests on `MemoryHub`; the testbed's scenarios (`testbed:scenarios/`) are the list to translate.
- Real mail: Stalwart in Docker (`conformance/stalwart.py` has the pinned image), then `probes/imap_probe.py`-style runs on real providers.
