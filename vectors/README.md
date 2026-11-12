# Tandem v1 test vectors

Exact bytes for every encoding the protocol defines, written by the Go reference SDK (`sdk/go/cmd/vectors`) and checked by the Swift SDK (`sdk/swift`, `swift test`). Every input derives from a fixed seed, so the files are the same on every run; `go test ./...` in `sdk/go` fails if they drift.

```sh
cd sdk/go && go run ./cmd/vectors -out ../../vectors/v1   # regenerate
cd sdk/swift && swift test                                # check the Swift SDK
```

Bytes are hex unless the protocol itself uses another text form (tags in b32, payloads as JSON text, backup keys as typed). The secrets are test material.

| File | What | Must reject |
|---|---|---|
| `keys.json` | User secret → signing seed, user ID and its display form, agreement key, kid, home key, home and inbox tags, home scope, backup key | |
| `backup.json` | Backup keys as people type them (spaces, case) | typo, length, alphabet, padding, dashes |
| `b64u.json` | base64url in its one spelling | non-zero unused bits (`QR`), padding, newline, other alphabet |
| `json.json` | The one reading (§10.5) | repeated names (nested, escaped), fractions, exponents, beyond 2^53, leading zeros, BOM, bad or overlong UTF-8, encoded or escaped lone surrogates, two values, trailing data |
| `tags.json` | All five channel tags, invite and pairing keys, key commitment, named record IDs (`é` in two spellings names two records) | |
| `envelopes.json` | Symmetric envelopes (kinds 1 and 3) with their message keys and AAD; an inbox envelope and a pairing reply to open | another channel, a tag in upper case, changed ciphertext, changed kind, version 2, wrong key, wrong recipient |
| `writes.json` | Writes of every op, with vectors, an empty value, an empty element, a home write, a field name in two Unicode spellings: sigInput, write ID, signature, payload | |
| `writes-invalid.json` | Kind-1 payloads | bad signature, repeated properties, unknown op, `v`/`e` on the wrong op, both payload forms, non-canonical b64u, fractions, HLC out of range, a device twice in a vector, a vector out of order, 1,025 entries, short device ID |
| `genesis.json` | A genesis statement: bytes, statement ID, collection ID, epoch ID, epoch tag | |
| `inventory.json` | Canonical inventories and their hash | unsorted, repeated, trailing, short, empty, an entry with no writes |
| `signed.json` | Signed inputs of join requests (`sig` and `isig`), grants, key requests, leave notices and pairing challenges | |
| `pairing.json` | Pairing codes | |
| `ed25519.json` | The 12 ed25519-speccheck cases under Tandem's rules (only 2 and 3 verify), a valid signature and the same with `S + L`; public keys that are never valid (the eight small-order points, five non-canonical encodings) | |
| `messages.json` | A hub message as built; reading it back folded, quoted-printable, base64, UTF-16 with a BOM, UTF-16BE, LF line endings | |

## Choices the draft doesn't make yet

The vectors had to pick bytes where `DESIGN.md` (frozen at `f4ce2e6`) is silent. Each is marked in the file it affects, and listed in `ASSUMPTIONS.md` §2.

1. **AAD tag:** the tag enters the AAD as its b32 text (32 ASCII bytes), not the 20 raw bytes. `model/` and the other conformance work read it the same way.
2. **Genesis:** `n` and `key.n` start at 0, and the 16 random bytes of §7.1 are a `nonce` field.
3. **Home record names:** `membership:<collectionID>` and `invite:<inviteID>` spell the ID in b64u.
4. **Key requests:** `have` is the 32-byte ID of the latest statement the member holds.
5. **Vectors in JSON:** sorted by device ID, as signed, with each device once; anything else is rejected (Fable, 2026-11-12, replacing "any order").
6. **Op and payload fields:** `v` is present exactly on `assign`, `e` exactly on `add`/`remove`; other ops sign `H("")`.
7. **HLC millis** at or beyond 2^48 make a write invalid, although `sigInput` has room for 64 bits.
8. **Inventory entries** list at least one write (Lean F3; Fable).
9. **Deletes** have an empty field name, no element and no value (Fable).

## What the Swift check found

- **CryptoKit signs Ed25519 with added randomness.** Go doesn't, so a write signed twice in Swift gets two different signatures with the same write ID. The protocol identifies writes by write ID, which hashes `sigInput` and not the signature, so nothing breaks. But the draft has to say so in two places: §11.6 says signing again gives "the same bytes", and §9.4 calls two "different" successors a conflict, which must mean different statement IDs, not different signatures.
- **Without Tandem's own checks, CryptoKit would accept speccheck cases 0 and 1** (small-order public keys) and reject the other cases Tandem rejects. A Swift client needs only the small-order check on top. Foundation's JSON parser accepts a repeated name, which the test shows, so the strict parser is required.
