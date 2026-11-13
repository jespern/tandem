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

The platform Keychain integration, IMAP adapter and complete device engine are
not part of this core milestone. The app owns those adapters and local transactions.
