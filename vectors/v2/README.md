# Tandem v2 core vectors

Public, deterministic test material, shared by all four SDKs.

| File | Coverage |
| --- | --- |
| `core.json` | v2 derivations, exact signed write inputs/IDs/payloads, all four operations, empty values/elements, distinct Unicode spellings, maximum HLC, symmetric and HPKE inbox envelopes, inventory bytes, and merge/replay histories. |
| `ed25519.json` | Version-independent strict verification cases, carried from the v1 harness, including valid mixed-order cases and forbidden encodings. |
| `json.json` | Version-independent strict JSON acceptance/rejection cases from the v1 harness. |
| `b64u.json` | Version-independent canonical base64url cases from the v1 harness. |

Regenerate with `sdk/python/.venv/bin/python -B vectors/v2/generate.py`. Use `--check`
to detect drift. The generator uses fixed public test keys and explicit test-only
salt/ephemeral material; SDK sealing APIs always draw fresh random material.

The full [SDK check](../../sdk/README.md#checks) also tests native encryption and
signing in all 16 sender/receiver combinations with temporary artifacts, and can
compare the shared merge relation against Lean. These are core vectors, not a
final v2 control-object schema: invitations, certificates and other Q28 formats
are deliberately absent.
