# Tandem v2 core vectors

Public, deterministic test material, shared by all four SDKs.

| File | Coverage |
| --- | --- |
| `core.json` | v2 derivations, exact signed write inputs/IDs/payloads, all four operations, empty values/elements, distinct Unicode spellings, maximum HLC, symmetric and HPKE inbox envelopes, inventory bytes, and merge/replay histories. |
| `ed25519.json` | Version-independent strict verification cases, carried from the v1 harness, including valid mixed-order cases and forbidden encodings. |
| `json.json` | Version-independent strict JSON acceptance/rejection cases from the v1 harness. |
| `b64u.json` | Version-independent canonical base64url cases from the v1 harness. |
| `control.json` | The control-object schema of §10.8, one small hub's signed objects (genesis, capsules, registrations, requests, certificates, an invitation, a pairing with its package and gate, receipts, a revocation, an authorization snapshot, a home epoch, a grant, a key request, a leave notice and an answer), derivations, sealed boxes, and JSON every reader must reject. |
| `blobs.json` | Blobs ([BLOBS.md](../../BLOBS.md)): a chunk's key, envelope, address and escaped form; three blobs cut into chunks, inline and empty, with their manifests; 33 manifest values every reader must classify the same way (a manifest, an unknown version, or none); the escaped form for every byte value at every place in a line and at the line boundaries; what an unescaping reader must accept and refuse; envelopes that must not open. |

Regenerate with `sdk/python/.venv/bin/python -B vectors/v2/generate.py`, `vectors/v2/generate_control.py` and `vectors/v2/generate_blobs.py`. Use `--check`
to detect drift. The generator uses fixed public test keys and explicit test-only
salt/ephemeral material; SDK sealing APIs always draw fresh random material.

The full [SDK check](../../sdk/README.md#checks) also tests native encryption and
signing in all 16 sender/receiver combinations with temporary artifacts, and can
compare the shared merge relation against Lean.
