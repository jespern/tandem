# The Swift device engine

`TandemEngine` is the production implementation of a Tandem v2 device for Apple platforms: what Liszto links. `Tandem` (the core) has the primitives and the control-object codec; the engine runs a hub on top of them. This file is the plan and the state of the work, kept current so a later session can pick it up.

## Shape

One `Device` per installation per hub, an actor. Everything it knows is in a `Store` (durable, local) and everything it shares is on the hub through a `PushStore` (the mail binding). The app talks to the device; the device never blocks the app on the network.

| Layer | Type | Spec | State |
|---|---|---|---|
| Mail binding | `TandemIMAP.IMAPStore: PushStore` (Network.framework, no dependencies), `MemoryHub` for tests | §10.1–10.4 | IMAP client in progress on `swift/imap`; `MemoryHub` **done** (push, removals, hiding and dropping as a hostile hub) |
| Password stretching | `Argon2id` in the core, for the admin capsule | §6.7 | In progress on `swift/argon2` |
| Registry | `Registry`: certificates, revocations, snapshots, floors | §6.9 | **Done**, tested against the vectors and the Lean attacks |
| Durable state | `Store`: values, control objects, message index, writes, journal; SQLite from the SDK (`import SQLite3`, a system library), WAL with full synchronous commits | §11.6 journal rules | **Done** (tables and transactions; the engine decides what goes in them) |
| Hub authority | genesis pin, directory channel, capsule unlock, profiles | §5.3, §6.6–6.8, §10.8 | Founding **done** (`Founder`: genesis, creator certificate, snapshot, first home epoch, inbox delivery, recovery box); capsule waits on Argon2id; publishing and the directory reader to do |
| Homes | home epochs (several heads), home records, recovery box | §6.2, §10.8 | To do |
| Collections | roster chain (statement JSON v2), epochs, inventories, data writes, merge, grants, answers, leave, key requests | §7–9, §11 | To do; port `testbed:sdk/go/device` (rotate.go, resign.go, ops.go) |
| Flows | create hub, accept invitation, pair (both sides), approve/reject, revoke, share, rotate | §6.1–6.4, §11, §13 | To do |
| Sync | change feed → dispatch by channel tag → hold/validate/merge; publish, debounce, garbage collection, healing | §10.7, §12 | To do |
| App API | lists and records as `AsyncSequence` snapshots; actions as async functions with typed errors | §13 | To do |

## Rules the engine keeps

- **Event-driven.** Work happens on a push (IDLE), a local action, a reconnect or a start. The only timers are the protocol's own safety nets: the 10-minute key-request fallback (§11.4), the IDLE refresh, Gmail's IDLE restart (§10.3) and the publish debounce (§10.7).
- **Journal before export.** Every signed object that leaves the device for a management action or key change is persisted, with its exact bytes, before the first append (§6.1, §11.6). A restart resumes from the journal; nothing is signed twice for the same step.
- **Hold, don't reject, what's incomplete.** Validity is three-way (`Validity.pending`): an object whose dependencies haven't arrived waits, with configurable limits.
- **Bytes, not strings.** Protocol text is compared as UTF-8 bytes (§10.5).

## Order of work

1. ~~Registry~~.
2. `MemoryHub` and `Store`; a `Device` that creates a hub (genesis, creator certificate, capsule, first home epoch) and another device that opens it from the saved setup.
3. Collections end to end on one participant's two devices: create a list, write records, see them on the other device.
4. Invitations: a second participant joins; sharing, answering, grants, leave.
5. Pairing with approval and rejection; revocation with home and list rekeys.
6. Rotation with the journal and crash points (port the Go engine's crash tests); late writes; garbage collection; healing.
7. The IMAP binding in place of `MemoryHub`: integration tests on Stalwart, then Fastmail and Gmail.
8. The app API Liszto uses, with Liszto's v1 → v2 migration (Q23) designed alongside.

## Checking

- Vectors: `vectors/v2/*.json` (core and control) through `ControlTests` and `CoreTests`.
- Registry: `RegistryTests`.
- Multi-device behaviour: scenario tests on `MemoryHub`; the testbed's scenarios (`testbed:scenarios/`) are the list to translate.
- Real mail: Stalwart in Docker (`conformance/stalwart.py` has the pinned image), then `probes/imap_probe.py`-style runs on real providers.
