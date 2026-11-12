# Tandem v1 conformance harness

Exercises real cryptographic implementations, actual JMAP/IMAP traffic, and
process-crash recovery from durable state. This is a reference harness, not a
production Tandem client. It does not import or extend the symbolic models.

Run from the repository root with current Python and, on macOS, Swift:

```sh
python3 -m venv conformance/.venv
conformance/.venv/bin/python -m pip install -r conformance/requirements.txt
conformance/.venv/bin/python -B conformance/run.py --require-cryptokit --mail
```

Omit `--mail` for an offline run after dependency installation. Omit
`--require-cryptokit` on Linux to run the Python and storage checks; Apple tests
are explicitly skipped there. `--verbose` names individual tests. Docker is
required only with `--mail`; the first mail run downloads the pinned image.
No real mailbox, token, SMTP delivery, or provider account is used.

GitHub Actions runs the Apple backend and storage tests on `macos-latest`, and
the real-server and storage tests on `ubuntu-latest`. See [RESULTS.md](RESULTS.md)
for the initial local evidence and the implementation requirements it exposed.

## What runs

* `wire.py` implements the binary signature input, identity derivation,
  symmetric envelopes, HPKE base mode, inbox envelopes and grant inputs from
  DESIGN §§5–6 and §11. HPKE uses `cryptography` primitives because its native
  single-shot HPKE API does not expose Tandem's AEAD associated data. The only
  additional Python dependency is pinned in `requirements.txt`.
* `cryptokit.swift` independently builds signature inputs and uses Apple's
  native HKDF, Ed25519, ChaChaPoly and HPKE. Tests compare input bytes, IDs and
  envelopes, verify signatures in both directions, and open HPKE messages in
  both directions. A fixed RFC 9180 vector checks both implementations.
* `fixtures/tandem-v1.json` contains reusable hex inputs and expected outputs
  for another SDK, including distinct composed/decomposed field names.
  Its `spec_sha256` identifies the reviewed draft it targets. Signatures in
  fixtures are valid examples, not a requirement to reproduce their bytes.
* `mail_transport.py` sends the exact JMAP `Email/set`, `Email/changes` and `Email/get`
  traffic and IMAP `APPEND`, `UID FETCH`, IDLE, QRESYNC and `UID EXPUNGE` calls.
  Tests cover both binding directions, MIME transfer encodings and UTF-16,
  a large body, pagination, moves, push events and expunge isolation. The UID
  scan cursor advances after scans, never merely from an `APPENDUID` response.
* `journal.py` persists one rotation's exact signed and encrypted bytes in
  SQLite WAL with `synchronous=FULL`, before export. A separate durable hub
  permits duplicate physical messages. `test_journal.py` uses real SIGKILL at
  42 transaction/export boundaries and fresh processes to finish. It checks
  byte-identical retries, snapshot/tombstone retention, grant ordering,
  removal, inventory commitments and restoration before destructive cleanup.
* With `--mail`, the same outbox is also killed after a real JMAP announcement,
  reopened by a new process, and finished against Stalwart. Surviving envelopes
  are then independently fetched over IMAP.

All test directories and compilation outputs are temporary. Containers and
their anonymous volumes are removed on completion or failure. Readiness uses
Stalwart's actual listener events; push tests wait for events, not poll loops.
Timeouts bound failed tests. No background daemon survives a normal run.

## Backend acceptance rules

Native Ed25519 verification is not automatically Tandem verification.
CryptoKit on the tested OS accepts an identity public key that DESIGN §5.5
forbids. The Swift adapter explicitly checks canonical encodings, `S < L`, and
the eight small-order public keys before native verification. Policy fixtures
also exercise the plain equation on mixed-order points, including valid cases
that an overly strict prime-subgroup-only policy would wrongly reject.
`wire.strict_verify` is an independent public-point arithmetic oracle. It is
not constant-time secret-key code and is not intended as a production signer.

The tested CryptoKit also randomizes Ed25519 signatures. Its signatures verify
against the strict equation and Python's verifier, but differ bytewise for the
same input. Tandem's `writeID = H(sigInput)` remains stable. Persist and retry
the actual signed message; do not assume re-signing reproduces its bytes.

## Isolation and limits

Stalwart **1.1.0** is pinned by image digest in `stalwart.py`. The fixture creates
a disposable account through its bootstrap API, disables ACME and DKIM setup,
and publishes only loopback HTTP/HTTPS/IMAPS ports. It uses Basic authentication
and a self-signed TLS certificate only inside that fixture. The test-only URL
override handles the server's advertised hostname. This does not test
Fastmail's bearer-token policy, limits, or EventSource availability.

The outbox covers a serialized, single-device rotation with a fixed snapshot.
It stores public test secrets in test files, not in a production Keychain.
SIGKILL exercises process crashes and SQLite transaction recovery, not physical
power loss. This is not a complete roster engine, parser, CRDT SDK, pairing UI,
or multi-device durable home-channel implementation; the symbolic models still
cover those collection rules separately.

Primary references:
[RFC 9180](https://www.rfc-editor.org/rfc/rfc9180),
[RFC 8032](https://www.rfc-editor.org/rfc/rfc8032),
[RFC 8620](https://www.rfc-editor.org/rfc/rfc8620),
[RFC 8621](https://www.rfc-editor.org/rfc/rfc8621),
[Stalwart Docker installation](https://www.stalw.art/docs/install/platform/docker/),
and the commit-pinned HPKE fixture's `source` URL.
