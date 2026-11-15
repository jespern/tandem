# Optional JMAP transport

All four SDKs expose HTTPS JMAP transports using persistent authenticated SSE.
Swift and Go accept them directly as the device engine’s `PushStore`. **Native
applications keep IMAP as their default**; JMAP requires explicit configuration.
The browser uses JMAP directly, because ordinary web pages cannot open IMAP sockets.

## Use it

```go
import "github.com/jespern/tandem/sdk/go/jmap"

config := jmap.FastmailConfig(apiToken)
store, err := jmap.Open(ctx, config)
// Pass store to tandem.NewSession or engine.Open; handle err first.
```

```swift
import TandemJMAP

let store = try await JMAPStore.open(.fastmail(token: apiToken))
// Pass store to Session or Device.open; keep using TandemIMAP for the default path.
```

```python
from tandem import JMAPConfig, JMAPStore, Session

store = await JMAPStore.open(JMAPConfig.for_fastmail(api_token))
async with Session(store, cursor=saved_cursor) as session:
    async for batch in session.updates():
        await validate_apply_and_commit_cursor(batch)
```

```typescript
import {JMAPStore, fastmailConfig, Session} from '@tandem/protocol';
// Use '@tandem/protocol/browser' in a browser.
const store = await JMAPStore.open(fastmailConfig(apiToken));
const session = new Session(store, {cursor: savedCursor});
await session.start();
```

The generic configuration takes a session URL and an Authorization header. The
Fastmail preset uses its documented session endpoint and a **JMAP API token**,
never the IMAP app password from an invitation. Additional generic provider
origins must be explicitly trusted. The Fastmail preset trusts HTTPS
`api.fastmail.com` and regional API subdomains below it, on the standard port.
TLS verification stays on in production; no credentials are placed in URLs.
Discovery redirects are permitted only to trusted origins. API redirects fail.

Connection credentials are separate from protocol authority and pairing links.
An app can use its platform keychain, or the browser’s wrapped keychain, to
remember them. SDK transport selection does not alter admissions or wire formats.
For distributed Fastmail web apps, [register OAuth origins/redirect URIs with
Fastmail](https://www.fastmail.com/dev/) instead of distributing a personal token.

## Push and cursor behavior

Subscribe before scanning. The connection remains open for updates. Reconcile
only on startup, a pushed Email state change, reconnect or an explicit local
submission. SSE heartbeats never trigger queries. Failed connections use bounded
backoff; malformed data, credentials, limits and protocol failures stop explicitly.
No polling fallback exists.

Initial scans page through only the selected replica mailbox, checking queryState
across pages. They sample Email state before scanning and replay changes from it.
Expired cursors cause a full scan; another account’s or mailbox’s cursor is refused.
Account-wide Email changes fetch metadata first and download bodies only for the
replica mailbox. Truncated bodies fail without advancing the cursor. Native
Go/Swift/Python changes calls drain explicit pages into a bounded batch; TypeScript
also exposes `more` for durable page-by-page processing.

Uploads import real MIME and preserve envelopes exactly. `AppendPrepared`,
`appendPrepared` or `append_prepared` accepts caller-journalled MIME for exact-byte
retry. An upload/import answer does not confirm a mailbox scan; persist the cursor
only with its incoming data. Fetch and destruction use opaque JMAP Email IDs.
Destruction checks mailbox membership first and refuses unrelated mail.

Defaults are 128 objects/page (reduced to advertised Get/Set limits), a 16 MiB
response budget, one million known IDs, a 128 KiB SSE event budget, a 30s command
safety deadline and failed-connection backoff from 1s to 30s. Native aggregate
batches are limited to 64 MiB. Swift bounds HTTP headers to 64 KiB and pulls SSE bytes directly from its
existing native TLS transport, avoiding Foundation’s quiet-stream header
buffering. JSON requests still use Foundation. All limits are configuration fields. Folder name defaults to `Tandem` and
is configurable for intentional replica separation and isolated probes.

No new runtime dependencies were added. Go uses `net/http`; Swift uses Foundation;
Python uses standard-library HTTPS in worker threads. Python command requests use
fresh connections to avoid introducing an HTTP-pool dependency; its SSE connection
is persistent. Swift and Go reuse the existing IMAP MIME formatter, so their
JMAP adapters depend on that module for formatting without opening IMAP sockets.

## Provider limits and checks

[The live Fastmail probe](typescript/FASTMAIL.md) measured 265 ms browser push,
587 ms IMAP→browser and **60.6 s JMAP→IMAP IDLE**. This is one run, not a latency
guarantee. Jesper selected IMAP as the native default and accepted the delay when
mixing transports. Fastmail also limits SSE connections per API token; use a
separate token per concurrent connection/profile. The latest probe did not
remeasure token concurrency limits.

For disposable real-server checks (Docker required):

```sh
sdk/python/.venv/bin/python -B sdk/check_jmap.py
python3 -B sdk/check_browser.py --native
```

`--sdk go|swift|python` narrows the native transport run. The fixtures create
isolated accounts/containers and remove them on exit. Fixture TLS exceptions are
limited to these explicit test configurations; the production defaults remain
verified. Go and Swift engine scenarios also create and reconstruct lists through
JMAP. The ordinary `sdk/check.py --browser` runs unit tests and all 25 cross-SDK
cryptographic sender/receiver combinations, including the browser build.
