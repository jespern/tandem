# Browser primitives for Liszto

`@tandem/protocol/browser` runs without Node built-ins or a Tandem backend. It
talks directly to a JMAP mail server. This is the primitive layer: Liszto's UI
and a complete browser device controller still need to be built on it.

## Runtime and storage

The Node and browser builds share write/control encodings, strict JSON/base64url,
Ed25519 acceptance, inventories and record merging. Node keeps native crypto.
The browser backend uses Noble hashes, curves and ciphers with Web Crypto's
random source; `buffer` supplies the existing byte API. The SDK bundle costs
about 49 KB gzip; the Argon2 worker loads separately.

Private keys now use an opaque SDK `PrivateKey` handle in both runtimes. Construct
them with `privateKey(seed, algorithm)`; pass that handle to signing/agreement
functions. This replaces direct Node `KeyObject` arguments in the development API.

`Registry` implements the Swift/Lean admission and revocation rules with explicit
valid, invalid and pending results. Missing proofs are not rejected. Alternative
signature witnesses stay separate from content IDs: a bad variant must not
replace a valid witness. Defaults are 64 approval links and 16 witnesses per ID,
configurable in the constructor. A holding-limit error does not revoke an ID.

`BrowserJournal` owns a separate IndexedDB database. A synchronous signing/sealing
callback in `queue` reserves the clock and journals exact encrypted/MIME bytes in
one strict-durability transaction. Inbox, cursor and matching send confirmation
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
30s command deadline and failed-connection backoff from 1s to 30s. Constructor
options configure these. Format/limit errors do not advance the cursor. A closed
or suspended browser cannot guarantee a live connection; reopening catches up.

`openCapsule`/`sealCapsule` use NFC and the fixed Argon2id v0x13, 1 GiB, three-pass,
one-lane profile in a cancellable worker. No weaker fallback exists. Only admin
actions/recovery need this memory. The native performance fix `802e812` is included:
Swift builds TandemArgon2 optimized even in debug, while the rest stays debuggable.
It changes no bytes or costs. SwiftPM consumers use a branch/revision because
the optimized target uses unsafe build flags.

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
```

The first checks all 25 sender/receiver combinations across the four native SDKs
and the browser. The second requires Docker and adds direct JMAP/CORS, push,
pagination, replay, deletion and lost-ack recovery against disposable Stalwart.
Only that fixture accepts self-signed TLS; production SDK checks stay enabled.
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
