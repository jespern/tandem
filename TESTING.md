# Testing Tandem

How Tandem is verified, what exists, and what waits. The protocol itself is in `DESIGN.md`.

**Status (2026-11-12): building.** The draft is frozen (`main` at `f4ce2e6`) and Jesper gave the go-ahead. Order: test vectors from the Go SDK checked by Swift, mail round trips on the testbed then Fastmail, crash recovery; Lean proofs and the scenario refresh alongside.

## Layers

| Layer | Where | Language | Status |
|---|---|---|---|
| Testbed: a real JMAP + IMAP server with per-device network faults and hostile-hub tools | `testbed/` | Docker; Python standard library for `tb` | Done. 42 binding and real-time probes pass on Stalwart 1.1.0 |
| Scenarios: 323 cases and families, with a format a harness can execute | `scenarios/` | Markdown | Drafted against an earlier design; refresh pending (`ASSUMPTIONS.md`) |
| Reference model: the merge and roster rules, executable and proved | `proofs/` | Lean 4 + Mathlib | Project builds; theorems planned in `proofs/README.md` |
| Reference SDK: the whole protocol, the harness driver, test vectors | `sdk/go/` | Go 1.26 | Empty module |
| Production SDK for Liszto | Liszto's repository | Swift (CryptoKit) | Not started |

## How they fit

```
scenarios/*.md ──► harness (Go) ──► reference SDK (Go) ──► testbed (Stalwart via Toxiproxy)
                        │                                     ▲
                        └── expected outcomes ◄── reference model (Lean, compiled)
                                                     │
                                         proofs about that same model
Swift SDK ──► same scenarios and test vectors as the Go SDK
```

## Decisions

| Date | Decision |
|---|---|
| 2026-11-12 | **Go** for the reference SDK and harness. Its standard library covers HPKE, HKDF, X25519 and Ed25519, and ChaCha20-Poly1305 comes from the Go team's `x/crypto`. It is fast enough for thousands of seeded runs, simulates many devices easily, ships as one binary, and is readable for other implementers. Rejected: Python (third-party cryptography, slow for fuzzing); Swift as the reference (ties it to Apple's toolchain and removes the independent second implementation). |
| 2026-11-12 | The **reference model** that judges scenario outcomes is written in **Lean** and compiled, so the proven model and the test oracle are the same code. |
| 2026-11-12 | After the redesign added a Python model to the design repository (`model/`), the **Lean model stays the oracle** (Jesper). The Python model is cross-checked against it by a differential test on random inputs (`ASSUMPTIONS.md`). |
| 2026-11-12 | Liszto uses a **native Swift SDK**, checked against the Go SDK through shared test vectors and the scenario suite. Two independent implementations are deliberate: where they disagree, the spec is ambiguous. |
