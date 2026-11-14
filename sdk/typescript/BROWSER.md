# Browser primitives for Liszto

`@tandem/protocol/browser` runs without Node built-ins or a Tandem backend. It
talks directly to a JMAP mail server. This is the primitive layer: Liszto's UI
still needs to be built on it. The browser controller currently supports pairing,
verified home/collection reads and durable field/set edits. Owner sharing and home/collection key rotation now use durable stages. Other
management workflows and delivery-envelope healing remain acceptance gates.

## Runtime and storage

The Node and browser builds share write/control encodings, strict JSON/base64url,
Ed25519 acceptance, inventories and record merging. Node keeps native crypto.
The browser backend uses Noble hashes, curves and ciphers with Web Crypto's
random source; `buffer` supplies the existing byte API. The SDK bundle costs
about 72 KB gzip; the Argon2 worker loads separately.

Private keys now use an opaque SDK `PrivateKey` handle in both runtimes. Construct
them with `privateKey(seed, algorithm)`; pass that handle to signing/agreement
functions. This replaces direct Node `KeyObject` arguments in the development API.

`Registry` implements the Swift/Lean admission and revocation rules with explicit
valid, invalid and pending results. Missing proofs are not rejected. Alternative
signature witnesses stay separate from content IDs: a bad variant must not
replace a valid witness. Defaults are 64 approval links and 16 witnesses per ID,
configurable in the constructor. A holding-limit error does not revoke an ID.

`HomePolicy` binds home starts to the participant, checks parent authorization
floors, and keeps concurrent rekeys as separate heads. `CollectionPolicy` pins the
owner from signed genesis and checks roster succession, reader permissions,
announced epochs, owner forks and fixed inventories. Keys received from inboxes
remain candidates until signed commitments confirm them. Defaults hold 100,000
home epochs/roster statements, eight candidate keys per epoch, 16 home witnesses,
1,024 home ancestry links and 1,024 candidate collection epochs. Constructor limits
are configurable; exhaustion raises a holding error rather than invalidating proof.
These are authority primitives; they do not yet provide device operations.

`BrowserJournal` owns a separate IndexedDB database. A synchronous signing/sealing
callback in `queue` reserves the clock and journals exact encrypted/MIME bytes in
one strict-durability transaction. `commit` reserves multiple dots and queues multiple envelopes with an opaque
device action state in that same transaction. Concurrent tabs read/update that
state under the database write lock. Wrap secrets before putting them in action
state; the journal does not encrypt it. Inbox, cursor and matching send confirmation
commit together. Removed deliveries retain cached bytes. Durable envelope events
can be replayed from the app's last committed projection sequence.

Installation IDs and clocks belong to the journal. Losing a committed clock
refuses further writes under that ID; `installation()` supplies a fresh one.
`BrowserKeychain` uses a different database, so enrollment keys survive that loss.
Jesper selected **remember this browser without another password**. Seeds are
wrapped by a nonextractable Web Crypto AES-GCM key in IndexedDB. This is local
profile storage, usable by trusted same-origin code, not an Apple synced keychain
or protection against malicious same-origin scripts. Clearing site data loses
enrollment; another browser profile pairs as a new keychain.

`BrowserReplica` uses Web Locks for one connection owner per profile/database/hub
and BroadcastChannel for other tabs' commits. Closing the owner hands over through
a lock event. No timer discovers writes or other tabs. Cancel and await the
replica before closing its journal. Its events are **raw envelopes**, not trusted
items; full home/roster validation must precede app merge and display.

## JMAP and password recovery

`JMAPStore` uses authenticated streaming fetch for persistent SSE, real MIME
upload/import, paged changes and deletion. Subscription precedes initial sync.
Full scans check queryState across pages, then replay Email/changes from a state
taken before scanning. Account-wide discovery does not download unrelated bodies.
Reconnection reconciles from the saved cursor. `more` explicitly continues a scan;
SSE heartbeats trigger no queries. No polling fallback exists.

The provider must permit the app's origin through CORS. Supply an explicit HTTPS
session URL and Basic/Bearer Authorization header; never put credentials in URLs.
Additional advertised API/upload/push origins require `trustedOrigins`. Discovery
redirects work, with the final origin checked. An IMAP password is never forwarded
automatically to a discovered JMAP host. Native pairing links currently describe
IMAP credentials: browser setup still needs a JMAP endpoint/credential mapping.

Defaults: 128 objects/page, 16 MiB response/message budget, 1,000,000 known IDs,
128 KiB SSE event budget, 30s command deadline and failed-connection backoff from 1s to 30s. Constructor
options configure these. Format/limit errors do not advance the cursor. A closed
or suspended browser cannot guarantee a live connection; reopening catches up.

`openCapsule`/`sealCapsule` use NFC and the fixed Argon2id v0x13, 1 GiB, three-pass,
one-lane profile in a cancellable worker. No weaker fallback exists. Only admin
actions/recovery need this memory. The native performance fix `802e812` is included:
Swift builds TandemArgon2 optimized even in debug, while the rest stays debuggable.
It changes no bytes or costs. SwiftPM consumers use a branch/revision because
the optimized target uses unsafe build flags.

`parsePairingLink` accepts native v2 links without using their IMAP credentials
for JMAP. `pairingRegistry` pins the exact signed genesis and directory-key
commitment. `proposePairing` validates the source device, expiry and package/gate
hashes, and prepares exact HPKE claim envelopes. Persist its pending secrets in
`BrowserKeychain` before committing those envelopes to `BrowserJournal`.
`releasePairing` requires the exact admitted request, recipient keys, certificate,
delegation and package hash before opening the gated package. Its home key is
still a candidate for `HomePolicy`. `BrowserDevice` runs this enrollment flow;
browser-side admin approval is not implemented yet.

`InventoryAssembler` keeps competing fragment versions separately and accepts
only canonical bytes matching the signed commitment. Defaults: 4,096 parts,
16 MiB buffered/assembled bytes and 4,096 alternative assemblies, all configurable.
`inventoryPayloads` uses the native engine's 48 KiB fragment size by default.

## Device operations

`BrowserDevice.pair(link, label, journal, keychain)` remembers enrollment and
journals the exact claim before export. `resume(hub, journal, keychain)` restores
it after reload, including a claim awaiting approval or edits made offline.
`run(() => JMAPStore.open(config), signal)` owns the push pump and durable replay;
`updates(signal)` is an async sequence of device changes. `state` exposes the
verified `DeviceState` view, with `lists`, `records`, `record`, `homeRecord`, `role`
and `offers`. `createList` commits membership, genesis and metadata together; `accept`
records the grant answer/key in home and sends the signed answer to the owner.
Lists received as offers stay out of the workspace until accepted.
`writeFields` and `writeHome` commit signed edits locally before
export. Set adds/removes and record tombstones use the shared merge rules.

`share` publishes and confirms the roster before delivering grants. `rotateHome`
uploads ancestry, a fixed inventory and carried writes before announcing its start
and delivering keys. A known device cutoff starts home rotation automatically.
`rekey`, `removeMember` and role reductions use a six-stage collection rotation:
private change note, complete upload, old-channel announcement, current-home
settlement, recipient grants and confirmed cleanup. Role upgrades and collection
deletion use confirmed same-epoch roster changes. Future home updates/grants are
signed when their stage runs, then journalled atomically before export.
Native peers accept these flows; a revoked native phone cannot discover a private
list created afterward. `operations` reports active stages and superseded actions.

Journal schema 2 adds encrypted action plans and a durable deletion queue. The
local action key lives in the separate keychain. Every stage is committed with
its exact envelopes, then waits for actual mailbox confirmation before advancing.
Reloads resume it. Same-epoch roster changes stay out of the local authority view
until confirmed. Recipient delivery checks run again before export, and queued
user data stays held when its key is exposed. Schema 1 upgrades retain deliveries;
foreign databases are refused without modification. Replica/device runners can
restart after cancellation. No timer advances a stage.

The controller re-signs its own installation’s late writes into a later epoch,
retaining their original dot and causal base. It never re-signs another device’s
writes. Implausible claims about its future clock cannot suppress a late edit;
plausibly superseded edits that may have been left behind are exposed through
`unshared` for app recovery. Local bookkeeping and re-signed bytes commit together.
A reload immediately before announcement preserves an edit made in that gap.

Current-channel hygiene restores missing kept records, roster ancestry, home starts
and inventory fragments from verified cached data. Fragment presence binds the
part index/count and slice hash. Queued repairs suppress duplicate publication;
only a confirmed physical replacement permits deletion of dominated data. Held
writes, metadata and equal-byte duplicate envelopes are retained. Concurrent home
heads keep their own writes/inventory; healing does not copy sibling-head edits
back and forth. This pass runs on durable events and local commits, without a timer.

The controller owns the journal's opaque per-hub action state. Device Web Locks
serialize local actions across tabs. A write transaction also checks the last
durable event sequence: if inbox/authority data arrived after replay, it retries
replay before signing. A verified revocation in that gap prevents the write.
No-op transactions emit no wake, so waiting for approval cannot create a busy loop.
Unknown proof status stays distinct from a verified revocation.

`DeviceState` caches decryptions and signature validation. New batches rebuild
record merges from retained source writes, including changed epoch eligibility;
this favors replay correctness over an incremental application cache. Defaults
hold 100,000 envelopes / 64 MiB and publish toward 64 KiB messages, configurable.
`collectionStatus` reports epoch readiness; reader role and current authority
still determine whether edits are allowed. Home writes stop when a known device
cut is absent from the current home epoch: they require a new home epoch key first.
Individual writes still obey the protocol's 1 MiB maximum. Private application
storage is separate from this SDK journal. Cancel/await the device and close its
keychain/journal when finished; no background watcher is needed.

## Build and checks

```sh
cd sdk/typescript
npm ci --ignore-scripts
npm run build
npx playwright install chromium
npm run test:browser
```

Use `@tandem/protocol/browser` in a bundler, or serve `dist/browser/tandem.js`
and `dist/browser/argon-worker.js` together on HTTPS. CapsuleOptions.workerUrl
supports a relocated worker. Allow same-origin module workers in the app's CSP.

The headless Chromium tests use real IndexedDB, Web Crypto, workers, Web Locks
and BroadcastChannel. They cover vectors, Node interchange, rollback/reopen,
lost clocks, remembered keys, multi-tab clocks, owner handover and the full 1 GiB
capsule vector. From the repository root:

```sh
python3 -B sdk/check.py --browser
python3 -B sdk/check_browser.py
python3 -B sdk/check_browser.py --native
```

The first checks all 25 sender/receiver combinations across the four native SDKs
and the browser. The second requires Docker and adds direct JMAP/CORS, push,
pagination, replay, deletion and lost-ack recovery against disposable Stalwart.
With `--native`, it also builds the Swift driver and proves real browser claim →
Swift approval over IMAP → browser page reload → gated JMAP push release → signed
home-key confirmation. It additionally verifies native/browser edits both ways,
controller enrollment/reload, offline edit recovery and revocation between replay
and commit. Native clients also read browser-created lists and acceptance answers;
reader edits are refused, and browser writer edits survive native list rekeys.
All 17 browser scenarios passed locally, including native collection rotation,
member removal, home cutoff and a late edit across an interrupted rotation. Only that fixture accepts self-signed TLS;
production SDK checks stay enabled.
It explicitly enables browser CORS and publishes real loopback URLs, with no proxy.

## Remaining acceptance gates

1. Finish the remaining management operations and healing.
   Pairing/release, bidirectional field edits, list creation/acceptance, browser
   sharing, home/list rotation, member removal and late-edit restart pass against
   Swift; full home/list rotation parity still needs
   the native crash/late-write scenarios. Reader and stale-epoch validation exist
   in the authority layer and must remain enforced by every new operation.
2. Shared Liszto field-value vectors against its native adapter, then the browser
   store/operations layer and workspace with the same completion and keyboard rules.
3. Fastmail is selected for real-provider validation; a JMAP API token is needed. Earlier Fastmail measurements found
   one SSE stream/token and delayed JMAP-to-IMAP notifications. Stalwart success
   does not establish mixed-transport latency on Fastmail; multi-tab ownership
   does not solve token limits across separate profiles.
