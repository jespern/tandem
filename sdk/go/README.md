# Go SDK

Native Tandem v2 core for Go 1.26.4+. See the [shared scope and real-time contract](../README.md).

```sh
cd sdk/go
go test -race ./...
```

The module is `github.com/jespern/tandem/sdk/go`. `NewDeviceKeys` creates independent
signing/agreement keys. `Write.Sign` preserves the participant, certificate and
policy bindings; `Write.Verify` takes the device public key separately. `NewEntry`
and `Record.Merge` handle already-authorized writes. Persist the HLC and exact signed
bytes before exposing or submitting an edit.

For real-time delivery, supply a `PushStore` that maintains a live subscription:

```go
session, err := tandem.NewSession(store, savedCursor, 64)
if err != nil { return err }
// Run is blocking; the application owns this goroutine and its cancellation.
go func() { errors <- session.Run(ctx) }()
for batch := range session.Updates() {
    // Validate and apply envelopes; save batch.Cursor in the same transaction.
}
```

Submit concurrently with `session.Submit(ctx, channelTag, persistedEnvelope)`.
Cancel the run context and wait for `Run` to return during shutdown. No SDK timer
initiates a network request.

`OpenCapsule`/`SealCapsule` are the admin capsule of §6.7 (Argon2id at 1 GiB, NFC
passwords), and `JSONObject` writes payload JSON as the other SDKs do.

The device engine is the `engine` package: see [engine/README.md](engine/README.md).

## IMAP

Package `github.com/jespern/tandem/sdk/go/imap` is the mail binding of DESIGN §10.3,
a port of the Swift `TandemIMAP` on the standard library only. `imap.Store` is a
`PushStore`:

```go
store, err := imap.New(imap.Config{Host: "imap.example.org", Username: "alice@example.org", Password: appPassword})
if err != nil { return err }
session, err := tandem.NewSession(store, savedCursor, 64)
```

It behaves as the Swift binding does: one connection runs commands and idles between
them (`SeparateWatchConnection` idles on a second); QRESYNC when offered, otherwise
`UID SEARCH ALL`; MULTIAPPEND and LITERAL+ for `AppendBatch`; a NOOP in the same write
as every IDLE; on Gmail (`X-GM-EXT-1`) IDLE restarts every 3 s and destroyed messages
leave All Mail through Trash; a lost connection is retried with backoff and reported
as one wake once IDLE runs again. IDs (`<uidvalidity>.<uid>`), cursors and the §10.4
message are the same as Swift's. Its only timers are the IDLE refresh, Gmail's IDLE
restart, reconnect backoff and a deadline on connecting and logging in
(`Config.ConnectTimeout`, 30 s; running out counts as a lost connection), marked
`allowed-timer:`. `Config.Trace` receives the
protocol lines without login credentials.

The integration tests start a disposable Stalwart in Docker (the image pinned in
`conformance/stalwart.py`, label `tandem-imap-test`) and remove it afterwards; a second
check builds the Swift `MessageFormat` and compares messages both ways:

```sh
TANDEM_IMAP_STALWART=1 go test -race -run Stalwart ./imap
TANDEM_IMAP_SWIFT=1 go test -run SwiftInterop ./imap
```

## Optional JMAP

`sdk/go/jmap` implements `tandem.PushStore` over HTTPS and persistent SSE.
See [JMAP setup, limits and provider behavior](../JMAP.md). Native transport
selection stays explicit; IMAP defaults are unchanged.
