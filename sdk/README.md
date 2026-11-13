# Tandem SDKs

Four native **v2 protocol cores**, with an event-driven session API. These are
development SDKs. They do not yet implement a complete device controller or an
IMAP adapter; the v1 implementations on `testbed` are separate.

| Language | Runtime | Changes arrive as | Runtime dependencies |
| --- | --- | --- | --- |
| [Go](go/README.md) | Go 1.26.4+ | A channel, scoped by `context.Context` | Go's `x/crypto` for ChaCha20-Poly1305 |
| [Python](python/README.md) | Python 3.15+ | An async iterator under an async context manager | `cryptography` |
| [TypeScript](typescript/README.md) | Node.js 26+ | An async iterator, with `AbortSignal` cancellation | None; TypeScript and Node types are build dependencies |
| [Swift](swift/README.md) | Swift 6.4, iOS/macOS 27+ | A bounded `AsyncSequence`, with task cancellation | None; CryptoKit supplies the native primitives |

## What is implemented

All four implement the same hub-bound derivations, device/management key creation,
strict base64url and protocol JSON, exact v2 write inputs and IDs, signatures,
symmetric envelopes and HPKE device inboxes, canonical inventories, HLC advancement,
and record merging: registers, sets, sticky deletes, conflicts and replay handling.
Record merge rejects inputs from another hub, scope or record.

Participant IDs, device certificates and policy snapshots are distinct from device
public keys. Signing preserves those bindings. Verifying a signature takes a
separately resolved device public key; it does not grant admission or permission.
The caller must authorize entries before passing them to record merge.

The Go and Swift code reuse the tested v1 core where the algorithm is unchanged.
The Python and TypeScript ports are independent implementations. The encodings
and key derivations are v2 throughout; no v1 user secret or pairing-code API is
carried forward.

Native Ed25519 verifiers disagree on edge cases. Go and Swift add the required
encoding checks around their native verifier. Python and TypeScript check the
plain public-input equation explicitly. This costs verification speed, but adds
no runtime library and matches the protocol's acceptance rule. Their public-point
arithmetic never handles secret keys; signing and encryption use native primitives.

## Real time is part of the contract

Each `Session` holds a `PushStore` subscription open while serializing outgoing
envelopes and cursor reconciliation. It calls `changes` only for:

1. Initial sync, after the push subscription is active.
2. A mailbox push notification.
3. A completed reconnect, reported by the transport as a notification.
4. A local submission that was appended successfully.

There is no periodic fetch, search, sleep loop or timer-triggered reconciliation.
Network failures are surfaced; the session does not hide them behind polling.
`watch` must return only after subscription is active and buffer notifications
until consumed, so a change during the initial sync cannot fall into a gap.

Sessions deliver envelope batches, not trusted records. The app/device engine
decrypts, verifies and applies them, and saves the batch's cursor with its local
transaction. An APPEND result alone must never advance a scan cursor. The caller
persists exact outgoing bytes before submitting them; a lost acknowledgement is
retried with those same bytes. Cancelling a submission cannot promise it did not
reach the server.

Buffers are bounded and configured per session (`capacity`, default 64 in Python,
TypeScript and Swift; explicit in Go). A slow consumer applies backpressure without
silently dropping a batch. Swift rejects a submission when its pending-command
capacity is full; the other runtimes suspend queueing. Closing wakes blocked
readers and submissions and closes the owned store. Go callers cancel `Run`'s
context; the other SDKs expose `close`/`close()` or an async context manager.

The forthcoming IMAP adapter must maintain IDLE and use DONE/command completion
before submitting commands, then resume IDLE. EXISTS, EXPUNGE/VANISHED, socket
failure and network reachability are event sources. Reconnection reconciles from
the last saved cursor; UIDVALIDITY changes require a full resync. Protocol lease
renewal and failure deadlines do not justify periodic searches. A server without
the required IDLE/UIDPLUS support must fail explicitly. There is no polling fallback.

## Checks

From the repository root, install the Python SDK and TypeScript build dependencies:

```sh
python3 -m venv sdk/python/.venv
sdk/python/.venv/bin/python -m pip install -e sdk/python
npm --prefix sdk/typescript ci --ignore-scripts
sdk/python/.venv/bin/python -B sdk/check.py
```

Go and Swift must also be on PATH. `--node /path/to/node` selects a Node runtime.
The runner uses temporary public test artifacts and leaves no background process.
It tests each native implementation, then checks every SDK's signatures and
encrypted inbox messages in every other SDK: all 16 sender/receiver combinations.
Go's race detector also checks the session code.

The shared [vectors](../vectors/v2/README.md) cover exact write bytes, strict parser
rejection, Ed25519 policy, derivations, envelopes, inventories and merge histories.
To compare all ordered pairs of those writes against the compiled Lean model:

```sh
sdk/python/.venv/bin/python -B sdk/check.py \
  --oracle proofs/.lake/build/bin/tandem-oracle
```

Build the oracle first using [its guide](../proofs/README.md). The SDK check's source
scan also rejects timer/sleep calls in SDK runtime code. That scan complements
the session tests; it is not a proof that a third-party transport never polls.

## What comes next

The full SDKs still need the control formats in DESIGN Q21/Q28: registrations,
certificates, exact policy snapshots, invitations, pairing, profiles, grants,
receipts and home-epoch starts. They also need the device/roster controller,
durable storage and crash-resumable outbox, platform key storage, admin-password
capsule support, IMAP adapters, and end-to-end device scenarios. Neither the Lean
proofs nor the core interoperability checks substitute for those layers.

The first milestone deliberately implements the bytes already specified. It does
not invent control formats independently in four languages.
