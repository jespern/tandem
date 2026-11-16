# Browser primitives for Liszto

`@tandem/protocol/browser` runs without Node built-ins or a Tandem backend. It
talks directly to a JMAP mail server. This is the primitive layer: Liszto's UI
is built separately in Liszto's `Web/` directory. The browser controller supports founding a hub, pairing in both directions, hub invitations,
verified home/collection reads, durable field/set edits, owner sharing, key
rotation and administration. Key-envelope and record healing survive reloads.
Browser invitation acceptance and creator recovery remain acceptance gates.

## Runtime and storage

The Node and browser builds share write/control encodings, strict JSON/base64url,
Ed25519 acceptance, inventories and record merging. Node keeps native crypto.
The browser backend uses Noble hashes, curves and ciphers with Web Crypto's
random source; `buffer` supplies the existing byte API. The SDK bundle costs
about 78 KB gzip; the Argon2 worker loads separately.

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
These authority primitives feed the durable device operations below.

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
SSE heartbeats trigger no queries. No polling fallback exists. Held outbox entries
on an earlier page do not block later exportable entries.

The provider must permit the app's origin through CORS. Supply an explicit HTTPS
session URL and Basic/Bearer Authorization header; never put credentials in URLs.
Additional advertised API/upload/push origins require `trustedOrigins`. Discovery
redirects work, with the final origin checked. An IMAP password is never forwarded
automatically to a discovered JMAP host. Native pairing links currently describe
IMAP credentials: use `fastmailConfig(apiToken)` for Fastmail, or supply an explicit JMAP config
for another provider. `folderName` selects a dedicated root mailbox and defaults
to `Tandem`. The Fastmail preset trusts HTTPS `api.fastmail.com` and regional
subdomains below it; generic configurations retain exact-origin trust.
Persist a serialized config through `BrowserKeychain.save` to remember the token
under the same local wrapping protection as enrollment keys. The app must supply
its own connection and settings UI. See [FASTMAIL.md](FASTMAIL.md).

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
`unlock` opens a current signed capsule in the Argon2 worker. The returned seed
is not persisted by the controller; pass it to an admin operation and erase it
when finished. The controller supports approval/rejection, device revocation,
invitation cancellation and password replacement.

`InventoryAssembler` keeps competing fragment versions separately and accepts
only canonical bytes matching the signed commitment. Defaults: 4,096 parts,
16 MiB buffered/assembled bytes and 4,096 alternative assemblies, all configurable.
`inventoryPayloads` uses the native engine's 48 KiB fragment size by default.

## Device operations

`BrowserDevice.found(mailbox, label, password, journal, keychain, options)` creates
the signed root, first admission, snapshot, private home, recovery box and password
capsule. `mailbox` is a credential-free locator; browser creation requires only
JMAP authentication. The management seed is used to seal/sign the capsule and is
not retained in enrollment. Password stretching retains the fixed 1 GiB profile.
Prepared exact envelopes are wrapped in a separate keychain setup slot **before**
any outbox commit/export. `resumeFounding` recovers that preparation even if the
app never saved the returned `hub` ID. `setupID` distinguishes selected folders.
Retries reuse the authority and exact persisted MIME, rather than generating a
second root. Publication is a durable, mailbox-confirmed `founding` action.

Founding is an explicit operation; a successful empty-folder check never grants
permission to replace another root. JMAP's `hasMessages` supports the app's one-off
setup check, and `mailboxDescriptor` exposes credential-free session identity.
The app refuses an occupied folder for fresh setup, while interrupted setup
resumes its existing root. Concurrent independent authorities in other browser
profiles cannot be prevented by a check against an untrusted mail server.

`ensureList(name, {record, field})` atomically creates a collection and an element
in its private home reference set. Concurrent callers use the lowest existing
16-byte reference. Liszto uses this to create Inbox/Later without orphan collections
or duplicates when a page closes between list creation and saving its reference.

The optional third argument to `startPairing(label, validForMs, mail)` supplies
the native device's IMAP connection details for that one link. It does not change
the browser's JMAP connection. Liszto asks for the IMAP app password **when adding
a native device**, not when creating the hub. Existing paired browsers can reuse
their link's native details. Native IMAP remains the default; no JMAP token is
substituted into a legacy IMAP pairing link.

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
`unshared` for app recovery. `state.historicalWrite(scope, writeID)` retrieves
a verified copy of the historical write for rescue; it grants no membership and
never re-signs someone else’s edit. Local bookkeeping and re-signed bytes commit together.
A reload immediately before announcement preserves an edit made in that gap.

Current-channel hygiene restores missing kept records, roster ancestry, home starts
and inventory fragments from verified cached data. Fragment presence binds the
part index/count and slice hash. Queued repairs suppress duplicate publication;
only a confirmed physical replacement permits deletion of dominated data. Held
writes, metadata and equal-byte duplicate envelopes are retained. Concurrent home
heads keep their own writes/inventory; healing does not copy sibling-head edits
back and forth. This pass runs on durable events and local commits, without a timer.

`startPairing(label, validForMs)` prepares a native-compatible delegation, gated
package and gate box, then records it in the private home. Its default lifetime is
24 hours. `cancelPairing(token)` atomically cancels unsent stages and publishes the
participant-signed cancellation. `invite(label, seed, validForMs)` returns a native
invitation; the default lifetime is seven days. The native IMAP credentials in the
existing enrollment link remain separate from JMAP credentials. Lifetimes are
configurable per call. These browser APIs return after durable local commit;
`operations` reports the mail-confirmed stages, so links created offline become
usable as their staged packets reach the mailbox.

`approve`, `reject`, `revoke`, `cancelInvitation` and `changePassword` use the
unlocked management seed. Approval journals exact admission proofs first, waits
for mailbox confirmation, then releases the gate to the exact admitted keys.
A later revocation retires a queued release. Missing dependencies hold operations;
they do not falsely mark them revoked. Hypothetical certificates are checked in a
separate registry fork, leaving live admission unchanged until confirmed.
Revocations enter local authority with their durable outbox commit, so later
private writes immediately require any necessary key rotation. Password changes
confirm a signed replacement before deleting the capsules it supersedes. Seeds
are not written into action plans. Plan boxes may contain pairing link secrets
and exact encrypted release envelopes, under the separate keychain wrapping key.

Before the exact gated release, `state` is unavailable and local device operations
are refused, even if another peer has already forwarded a home-key notice. The
controller publishes only its enrollment claim in that state. Once paired,
queued recipient envelopes retain their guards in journal events. Reloads resend
missing deliveries byte for byte; successor deliveries replace obsolete copies
only after confirmation. New admitted recipients get current home keys/grants
without bypassing an active rotation’s stages. Revoked recipients get no repair.
A staged record can also remain confirmed through a newer, physically present
valid record that dominates it; compaction cannot trap an action in a resend loop.
Inventories and control proofs still require exact confirmation.

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
The browser scenarios include native collection rotation,
member removal, home cutoff, a late edit across an interrupted rotation, key
envelope repair after reload, and browser administration accepted by Swift. Only that fixture accepts self-signed TLS;
production SDK checks stay enabled.
It explicitly enables browser CORS and publishes real loopback URLs, with no proxy.

## Remaining acceptance gates

1. Joining an invitation and creator recovery still need controller entry points.
   Browsers can now create a hub or pair with an existing device, then stage
   further pairings and invitations.
   Explicit owner-note finish/discard, broader restore/rescue UI and the full
   native crash corpus remain parity work. The Liszto browser workspace is in its
   own repository; it is not shipped as part of this protocol SDK.
2. Broader native/browser Liszto feature parity. Its Web tests compare actual Swift
   ordering/field encodings, and verify UI edits and device setup both ways against
   the native engine. Hub founding tests inject a journal failure before commit;
   Liszto's UI test loses an accepted append response, reloads the creator, then
   pairs/approves a native device over IMAP and opens the browser's capsule in Swift.
3. Fastmail passes direct Chromium CORS/push/replay/deletion and byte-exact IMAP
   interchange. A live run measured 265 ms browser push, 587 ms IMAP→browser and
   **60.6 s JMAP→IMAP IDLE**. Jesper accepted this delay for mixed mode and kept native IMAP as the default. A browser using JMAP does not make Fastmail’s IMAP push immediate.
4. Browser files remain on `liszto/files` (reviewed at `2e27165`, 2026-11-16).
   The independent JMAP destroy race fix has been taken separately: `notFound`
   means another device already completed deletion; other refusals still fail.
   Before integrating the file engine, add browser regressions for the native
   campaign's remaining recovery/integrity cases and reconcile these code paths
   in `src/browser/blobs.ts`:

   - `peek`, `blob` and `#fetchManifest` currently accept inline content without
     checking its advertised whole-file hash.
   - `#download` resumes per-index pieces under a content-hash directory using
     their lengths, without binding the manifest's order and keys. `#downloads`
     also shares a promise by hash alone. Exercise an interrupted inconsistent
     layout followed by a good manifest with that same hash, and concurrent reads.
   - `#commit` removes the staged source after the local journal/outbox commit,
     before feed confirmation. Exercise cache eviction and remote chunk loss
     while the feed remains offline, then reopen and repair.
   - `#uploadAll` can wait for missing chunks or a missing current tag before
     `prepareBlobCommit` checks lost writing permission. Exercise observed
     removal/downgrade during a blocked upload, followed by re-admission/promotion.

   These are code-review integration gates, not claims that new browser
   reproductions have already run. Pending-write causal reissue and retaining an
   oversized just-fetched file are already implemented on the branch. Its native
   driver folder rename also needs matching updates to the interop provider and
   socket-fixture folder checks; resolving only the three reported merge
   conflicts would leave those tests pointing at a different folder.
