# Swift SDK

Native Tandem v2 core for Swift 6.4 and iOS/macOS 27+, using CryptoKit.
See the [shared scope and real-time contract](../README.md).

The Swift package manifest is at the repository root, so Swift Package Manager
can consume the repository directly. The library product and module are `Tandem`.
To test from the repository root:

```sh
swift test
```

`Write`, `Dot`, `Entry` and `Record` have public constructors. Field names are
UTF-8 byte arrays: Swift `String` equality merges canonically equivalent Unicode
spellings, while the protocol deliberately distinguishes those spellings.
`recordPayload` serializes outbound records; `Kind1.parse` reads them strictly.

Given a `PushStore` adapter, the same actor handles incoming notifications and
queued outgoing envelopes:

```swift
import Tandem

let session = try Session(store: store, cursor: savedCursor, capacity: 64)
try await session.start()
for try await batch in session.updates {
    // Decrypt, validate and apply; save batch.cursor with the local transaction.
}
await session.close()
```

Another task can call `try await session.submit(channel: tag, body: envelope)`.
The updates sequence suspends producers when its buffer is full; it does not drop
old or new batches. The pending-command limit rejects excess submissions explicitly.
Close the session in the application's cancellation/error path, and persist exact
outgoing bytes before calling submit. `PushStore.close` must unblock its ongoing
network operations. No SDK timer initiates reconciliation.

## IMAP

The `TandemIMAP` product is the mail binding of DESIGN §10.3: `IMAPStore` is a
`PushStore` built on Network.framework and Foundation only.

```swift
import TandemIMAP

let store = IMAPStore(.init(host: "imap.example.org", username: "alice@example.org", password: appPassword))
let session = try Session(store: store, cursor: savedCursor)
```

One connection runs commands and idles between them (`watchConnection: .separate`
idles on a second one). It uses QRESYNC when offered, otherwise `UID SEARCH ALL`,
MULTIAPPEND and LITERAL+ for batch appends, and on Gmail (`X-GM-EXT-1`) restarts IDLE
every 3 s (`idleRestart`) and moves destroyed messages from All Mail to Trash. A lost
connection is retried with backoff and reported as an invalidation once IDLE runs again.
Every IDLE starts with a NOOP in the same write: Stalwart reports a change made between
the last command and IDLE only on the next command, not when IDLE starts.
IDs are `<uidvalidity>.<uid>`; after a UIDVALIDITY change, `changes` reports every known
ID as removed and the folder's messages as new. Its only timers are the IDLE refresh,
Gmail's IDLE restart and reconnect backoff, marked `allowed-timer:` for the SDK check.

The integration tests start a disposable Stalwart in Docker (the image pinned in
`conformance/stalwart.py`) and remove it afterwards:

```sh
TANDEM_IMAP_STALWART=1 swift test --filter StalwartTests
```

The engine runs live against a real provider in a folder `Tandem-test`, which the test
empties before and after (use an app password; the timings go to stderr):

```sh
TANDEM_PROVIDER_HOST=imap.gmail.com TANDEM_PROVIDER_USER=alice@gmail.com \
TANDEM_PROVIDER_PASSWORD="$(security find-generic-password -s tandem-probe -a alice@gmail.com -w)" \
TANDEM_SKIP_ARGON2_GIB=1 swift test --filter EngineOverProviderTests
```

`TANDEM_IMAP_TRACE=1` prints the protocol lines, without login credentials. Leftover
containers from a killed run carry the label `tandem-imap-test`.

## The device engine

`TandemEngine` is the complete device: hubs, people, devices, lists, sharing, key
changes, live sync and events, with the keychain integration. Start with
[INTEGRATING.md](INTEGRATING.md); the engine's design and state are in [ENGINE.md](ENGINE.md).

## Optional JMAP

The `TandemJMAP` product implements `PushStore` over HTTPS and persistent SSE.
See [JMAP setup, limits and provider behavior](../JMAP.md). Native transport
selection stays explicit; IMAP defaults are unchanged.

IDLE stays open by default on Gmail too; only the required 29-minute protocol
renewal is automatic. The former three-second Gmail workaround is disabled to
avoid timer-driven change discovery. Explicit restart configuration remains
available, but applications using the no-polling default should leave it alone.
