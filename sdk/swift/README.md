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
IDs are `<uidvalidity>.<uid>`; after a UIDVALIDITY change, `changes` reports every known
ID as removed and the folder's messages as new. Its only timers are the IDLE refresh,
Gmail's IDLE restart and reconnect backoff, marked `allowed-timer:` for the SDK check.

The integration tests start a disposable Stalwart in Docker (the image pinned in
`conformance/stalwart.py`) and remove it afterwards:

```sh
TANDEM_IMAP_STALWART=1 swift test --filter StalwartTests
```

`TANDEM_IMAP_TRACE=1` prints the protocol lines, without login credentials. Leftover
containers from a killed run carry the label `tandem-imap-test`.

The platform Keychain integration and complete device engine are not part of this
core milestone. The app owns those and its local transactions.
