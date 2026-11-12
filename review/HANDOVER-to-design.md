# Handover: what implementing the frozen draft changed

Written 2026-11-12 by the session that owns the `testbed` branch, for the session that maintains `DESIGN.md` on `main`. It stands on its own.

## What happened

Your handover froze the draft at `f4ce2e6` and asked for implementation testing. Since then, on branch `testbed` (pushed as `origin/testbed`):

- A Go reference SDK, test vectors checked by a Swift SDK and by Codex's `conformance/`, JMAP and IMAP bindings, and a durable Go device engine with multi-device crash tests. Everything runs on an in-memory hub, on Stalwart, and against Fastmail.
- Lean proofs of the merge, roster, GC and encoding rules, and 340 refreshed scenarios with 62 open gaps.
- Two reviews of what that found: `review/2026-11-12-fable.md` and `review/2026-11-12-sol.md`. Both answer `review/2026-11-12-implementation-findings.md`.

Jesper decided where the reviews differ. The code already follows every decision below; **the draft doesn't yet**. Please bring `DESIGN.md` up to it as the next draft, with a §19 row per change. The exact wording is in the two reviews: section names below point to it.

## Changes to make

1. **Key changes: the announcement is the boundary (§8.2, §11.6, §11.7).** Reproduced as a dead collection by `sdk/go/device/device_test.go` (`TestCrashThenSecondDevice/2:journal`). Use Fable's §1 text, with Sol's §1 additions:
   - "never **announces** two successors" replaces "never signs two";
   - a draft may always be abandoned, and its channel destroyed;
   - before announcing, sync and require that the note still names this statement and the parent is still the head;
   - resume after a restart only under the same conditions;
   - an observed own note in home outweighs a local "note written" flag (a lost answer to the append, Sol's finding B);
   - finishing and discarding act only while the note names the statement the user was shown;
   - "do it here" signs only after the discard is on the hub;
   - same-epoch statements (admitting a member) make the same check between signing and publishing.
2. **Deletes (§9.2, §12.3).** Fable's §2 text: a delete has an empty field name, no element and no value. It supersedes every write of its record that it includes, in any field, and only a delete supersedes a delete. A deleted record therefore shrinks to one write, as §12.3 promises. Registers are typed, per Sol's §2: (assign, field), (set, field, element), (delete). Lean has re-proved this (`proofs/README.md`, F1 resolved).
3. **Identity is the write ID (§5.5, §9.4, §11.6).** Fable §3 / Sol §3. CryptoKit's Ed25519 is randomized, so signatures may differ. A roster conflict means two different statement IDs; "the same bytes" means the same `sigInput`.
4. **Inventories (§8.3).** Every entry lists at least one write; the empty inventory is `u32(0)`.
5. **Valid chain (§8.3, §9.4).** Data names a statement whose whole chain to the genesis is valid. An owner-signed but invalid successor counts for the conflict test and ends the chain (Fable §5).
6. **Encodings left unsaid (§5.4, §6.2, §7.1, §7.3, §9.1, §10.5, §11.4).** Fable §6, confirmed by Sol:
   - the AAD tag is its b32 text;
   - the genesis has `n = key.n = 0` and a 16-byte `nonce`;
   - home record names spell IDs in b64u;
   - `have` is a 32-byte statement ID;
   - `v` only on assign, `e` only on add and remove;
   - HLC millis are below 2^48;
   - `_roster` carries nothing but `s`;
   - **JSON vectors must be sorted** (Fable's position, decided);
   - pairing and join payloads get a `type` field.
7. **Signing again (§11.6, §9.3; G-26).** Both protections, decided:
   - Fable: a vector entry for one's own device beyond any dot it issued is forged, and the write is signed again anyway;
   - Sol: a late own write the new state claims to supersede is kept and reported to the app as possibly unshared.
8. **Leave notices (§11.5; G-06).** A notice counts while its signer has been listed continuously from `at` to the head (Fable and Sol agree).
9. **Set contexts (§9.2; G-37).** Contexts and bases are built over the register being edited.
10. **Split records (§10.6, §10.7; G-40).** Sol's text, decided:
    - publish for healing only when the union of fragments doesn't dominate the local state;
    - keep the pairwise domination and hash-order GC.

    Fable's set-domination GC is rejected: `{a,b}`, `{b,c}`, `{a,c}` each dominate one another, and all three could be destroyed.
11. **Refused requests (§6.2, §11.3, §11.4; G-09).** Sol's text, decided:
    - no new state;
    - a decline is a hint;
    - the joiner stops waiting but keeps `requested`;
    - explicit cancellation sets `left`.
12. **Pairing clean-up (G-01), re-posting lost requests (G-49), HLC durability (G-20).** Fable and Sol agree; either text works.
13. **Home field encodings (§6.2).** Specify every protocol field in home (Sol's finding C). The Go engine's encodings are proposed in `review/home-encoding.md`. Codex's `conformance/journal.py` writes the `flight` note differently (`id`/`key`, `null` to clear). Pick one; Go accepts `null` and an empty value.
14. **Fastmail (§10.2, §13.1, §18 Q1–Q3).** Measured on a dedicated account; details in `ASSUMPTIONS.md` ("Fastmail"):
    - Fastmail keeps **one EventSource connection per token**.
    - **Decided: one API token per device on Fastmail.** Use Sol's §7 text for §13.1: invites and pairing hand a separately issued token to the new device instead of copying the inviter's.
    - Headers survive, and 100,000 messages in one folder work. Q1–Q3 can close.
    - IMAP IDLE works for every connection, but a JMAP change reaches IMAP about 58 s late.

## Still open, for Jesper

- **Pairing `type` names:** Fable proposes `request`/`challenge`/`reply`, Sol `pairreq`/`pairchallenge`/`pairreply`.
- **Versioning (G-60):** Fable defines a v2 migration through a v1 statement. Sol prefers to leave migration undefined and let only authenticated v1 messages affect v1 processing. Both make a foreign-version message junk rather than an off switch.

## This file

A copy is committed on `testbed` as `review/HANDOVER-to-design.md`. The copy in your checkout is untracked: fold it into the draft, then delete it.
