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
An IMAP adapter is not written yet.
