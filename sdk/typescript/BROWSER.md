# Browser primitives for Liszto

`@tandem/protocol/browser` runs without Node built-ins or a Tandem backend. It
talks directly to a JMAP mail server. This is the primitive layer: Liszto's UI
and a complete browser device controller still need to be built on it.

## Runtime and storage

The Node and browser builds share write/control encodings, strict JSON/base64url,
Ed25519 acceptance, inventories and record merging. Node keeps native crypto.
The browser backend uses Noble hashes, curves and ciphers with Web Crypto's
random source; `buffer` supplies the existing byte API. The SDK bundle costs
about 55 KB gzip; the Argon2 worker loads separately.

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
still a candidate for `HomePolicy`. These functions do not yet run enrollment
as an app workflow or implement admin approval in the browser.

`InventoryAssembler` keeps competing fragment versions separately and accepts
only canonical bytes matching the signed commitment. Defaults: 4,096 parts,
16 MiB buffered/assembled bytes and 4,096 alternative assemblies, all configurable.
`inventoryPayloads` uses the native engine's 48 KiB fragment size by default.

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
home-key confirmation. All 11 scenarios passed locally. Only that fixture accepts self-signed TLS; production SDK checks stay enabled.
It explicitly enables browser CORS and publishes real loopback URLs, with no proxy.

## Remaining acceptance gates

1. A browser device controller passing the Swift pairing/release, home, roster
   fork, grant, inventory and rotation scenarios. Preserve pending dependencies
   and alternative witnesses through journal replay. Refuse revoked keys,
   reader writes and stale epochs before app integration.
2. Shared Liszto field-value vectors against its native adapter, then the browser
   store/operations layer and workspace with the same completion and keyboard rules.
3. Validation on the selected real provider. Earlier Fastmail measurements found
   one SSE stream/token and delayed JMAP-to-IMAP notifications. Stalwart success
   does not establish mixed-transport latency on Fastmail; multi-tab ownership
   does not solve token limits across separate profiles.
