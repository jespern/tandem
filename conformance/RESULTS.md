# Initial implementation evidence

Validated locally with Python 3.15.0, cryptography 50.0.2, Apple Swift 6.4.2 /
CryptoKit on macOS 27.2, and the pinned Stalwart 1.1.0 multi-architecture image.
`fixtures/tandem-v1.json` records the target draft's SHA-256.

The full command is:

```sh
conformance/.venv/bin/python -B -W error::ResourceWarning conformance/run.py --mail --require-cryptokit
```

28 tests pass, with no skipped backend or transport tests. The durable rotation
test includes 42 SIGKILL/restart subcases. The same persisted outbox also resumes
after losing a real JMAP announcement acknowledgement, and every surviving
envelope agrees when read independently through IMAP.

## Findings relevant to implementation

1. **DESIGN §5.5 needs an adapter around native verification.** Raw CryptoKit
   accepts `A = 01 00…00`, `R = 01 00…00`, `S = 0` for an arbitrary message.
   Tandem forbids this small-order public key. The Swift adapter rejects all
   eight small-order public keys, noncanonical point encodings and scalars
   outside the group order. Mixed-order vectors confirm that the backend uses
   the required plain equation. This is a library acceptance mismatch, not a
   break of a legitimate user's key. Keep the adapter for every signed object,
   including join requests; signature validity must have one definition.
2. **DESIGN §11.6's “same bytes” means the signed input and ID, not necessarily
   the signature.** The tested CryptoKit randomizes Ed25519 signing, while
   Python produces deterministic signatures. Both verify. `H(sigInput)` agrees
   across the implementations. Journals must retain the actual selected bytes
   for retries instead of re-signing, and implementations must deduplicate
   logical writes by write ID rather than signature bytes or envelopes.
3. **MIME encoding needs a fixture that preserves the encoded text.** Python's
   `EmailMessage.set_content(..., charset='utf-16')` normalizes newlines after
   encoding and corrupts that test input. The fixture now encodes the text to
   bytes before applying base64 MIME transfer encoding. Actual UTF-16/IMAP →
   JMAP and JMAP → IMAP envelope bytes then agree. This was a harness bug,
   not a protocol defect or Stalwart data corruption.

## What this establishes

* Independent Python/CryptoKit agreement on identity derivation, signature
  inputs and IDs, symmetric envelopes, and native HPKE in both directions.
* An RFC 8032 Ed25519 known-answer test and a commit-pinned RFC 9180 HPKE
  known-answer test, alongside tamper and strict acceptance cases.
* Real cross-binding MIME round trips, large-body retention, paged JMAP
  changes including moves, EventSource/IDLE delivery, QRESYNC removals,
  scan-cursor safety after concurrent appends, and selective expunge.
* Persistence of exact planned ciphertext across process death, safe duplicate
  exports after acknowledgement loss, inventory/tombstone preservation,
  correct announcement/grant ordering, removal without a grant, and snapshot
  repair before old-data cleanup.

The tests use a single serialized rotation. They do not establish a production
SDK's complete authorization/merge behavior, multi-device disk-backed home
coordination, physical-power-loss behavior, pairing UX, or Fastmail-specific
authentication and service limits. The existing symbolic model remains a
separate test suite. CI is provided for macOS crypto/storage and Linux
mail/storage; a remote CI result requires pushing the commit.
