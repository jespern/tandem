# How the 2026-11-12 redesign changes our assumptions

Written against `DESIGN.md` as it stood after Fable's revision on 2026-11-12 (1,048 lines, uncommitted in the main checkout), with Sol reviewing again. The scenarios, proof plan and probes on this branch were written against the earlier draft (1,073 lines, merged authorization logs with cuts). This note says what still holds, what doesn't, and what the redesign itself gets wrong.

## 1. What changed

| Before | Now |
|---|---|
| Authorization as mergeable, hash-linked logs with cuts, fences and a pin | **Statement chains**: each statement the complete state, judged against its parent only; forks settled by rank at the split, then length, then lowest ID (§9.4) |
| Device chain of equal devices; first device + `userNonce` define the user ID | **User chain**: user ID is the genesis statement's ID; the genesis fixes the first device and the recovery key; devices ranked by seniority, recovery key above all; a device can sign itself out (§6.2) |
| Members listed by user ID, verified through each member's device chain | Members listed by a **user signing key** derived from the PUK (`UKs`); a collection never looks at device chains (§8.1). Every device of a user signs with the same key |
| `_roster`, `_collection`, `_epoch` log records; genesis `to` lists; `replaces` | One record, `_roster`; metadata, invites and the epoch key commitment live in the statement; a rotation **is** a statement with a new key (§11.6) |
| `_carried` field, vouched forwarding of late writes by any writer | **Snapshot lists** signed by the rotating device, not writes; late writes cross only if their author re-signs them (§8.3) |
| Members remove themselves with a `left` write | Members post a **leave notice**; an owner or admin removes them (§11.5) |
| Tombstones dropped after 180 days; horizon, stale devices, maintenance rotations | **Tombstones kept for good**; no horizon, no staleness (§12.3) |
| Declines signed and authoritative | Declines are **hints**; the request stays pending until expiry (§11.4) |
| No model | **`model/`**: a Python model of §6.2, §8, §9.2 and §9.4 with 23 passing tests, including randomized adversaries |

Unchanged: the crypto suite, envelopes, channels, the merge rule for data (§9.2, §9.3), the bindings (§10), healing (§12.2), versioning.

## 2. Defects in the current draft

### New: fork choice (§9.4), reproduced with the design's own model

`model-checks/fork_choice.py` (run it with the path to the tandem repo's `model/`).

1. **A removed admin can win when the owner uses two devices.** Every device of a user signs with one user key (§6.3), and the fork rule ignores a key that signed on both lines. So the owner's phone removing admin M, while the owner's Mac (unaware) adds a writer, leaves the owner's key counting for neither line.
   - Even with nobody misbehaving, the removal survives only if its statement ID happens to be lower.
   - M then builds on the Mac's line, where M is still an admin, and outranks the removal.
   - Result in the model: the head is M's statement, epoch 0, M still admin, a sock puppet admitted.
   - The same holds for any two devices of any owner or admin acting concurrently, which is ordinary use, not an attack.
2. **Fork choice isn't transitive.** Rank is taken at each pair's own split, so three valid lines can beat each other in a cycle (A > B > C > A; the script builds one).
   - The model stays deterministic only because it compares leaves in sorted-ID order.
   - The design text ("comparing them two at a time") doesn't fix an order.
   - Statement IDs can be ground, so a member can steer which line wins.

Possible directions, for Sol and Fable to weigh:

- **For 2:** choose the head by descending from the genesis. At each fork, score every child subtree by `(top rank of signers not appearing in any sibling subtree, length, ID)`. That gives one score per child, hence a total order.
- **For 1:** stop treating one user key on two lines as equivocation. Either rank by **user**, and neutralize only a key that signed two *conflicting* changes to the same thing, or make a reduction (removal, demotion, re-key) win against any line on which the reduced party signed after the split, which is the "cut" idea in its narrowest form.

### Still open from before

- **§10.7 garbage collection.** The lowest-hash tie-break covers only "messages holding exactly the same writes". Two messages with different writes can dominate each other (`m = {x, y}`, `n = {y}`, `y` supersedes `x`), so two devices can destroy one each and lose `y`. Byte-identical copies tie on the hash itself (hub.md HB-024). Fix: tie-break any mutual domination, by `H(envelope)` and then by binding-specific ID. (`proofs/README.md`)
- **Testbed findings not yet in the design** (`testbed/README.md`, findings 2, 3, 5, 6, 12, 14):
  - §10.2 must treat `invalidArguments` on `Email/changes` like `cannotCalculateChanges`.
  - The ping-based reconnect must use the interval the server reports in its pings (Stalwart raises 60 s requests to 30 s, and smaller ones too).
  - JMAP readers must check `isEncodingProblem` and fall back to the raw message.
  - The IMAP connection cap can be **per account** (Stalwart: 16). On a shared hub that caps IMAP devices across all users, not per household or address as §10.3 says.
  - A push can arrive before the writer's own `Email/set` response.

### Gaps the redesign resolves

Among the scenario GAP notes, the redesign settles at least these:

- **Identity:** ID-001 and ID-026 (the recovery key is in the genesis); ID-027 (the counter-signature after a recovery-key replacement).
- **Sharing:** SH-001 (the creator is in the genesis); SH-007, RO-024 and RC-019 (no forwarding by others); SH-028 (declines are hints); SH-039 (nobody removes themselves).
- **Rotation:** RO-026 and RC-033 (snapshot lists aren't writes); RO-020 and FT-010/011 (the rotation publishes the new channel first and the statement last).
- **Gone with the mechanism:**
  - every horizon, staleness and maintenance-rotation gap (RO-023, FT-021, FT-022, SC-004);
  - the `_epoch`-record gap, now the `_roster` record that stays for good.

Many others still apply as written, e.g. RC-031 (malformed input), SC-011 (unbounded version vectors), RC-026 (a poisoned `base`), HB-022 and HB-023 (decoding differences between the bindings) and HB-017 (the connection cap). Each needs re-checking against the new text.

## 3. Impact on our work

### Scenarios

Counted by the terms they rely on (cuts, fences, revocations, `_carried`, `_epoch`, `_collection`, horizon, staleness, maintenance rotations, forwarding, least privilege):

| File | Scenarios | Rely on removed mechanisms | Action |
|---|---|---|---|
| `identity.md` | 37 | ~28 | Rewrite for seniority, sign-out, the recovery key in the genesis, PUK advance in the same statement |
| `rotation.md` | 36 | ~27 | Rewrite for rotation-as-statement, snapshot lists, re-signing, the new publish order |
| `sharing.md` | 39 | ~15 | Update: leave notices, roster statements, decline hints, fork outcomes instead of least privilege |
| `permissions.md` | 28 | ~13 | Update the capability matrix (leave = notice; no fences; device removal needs seniority); concurrent-change cases now follow the fork rule |
| `faults.md` | 32 | ~12 | Drop the staleness cases; re-check crash points against the new rotation order |
| `records.md` | 40 | ~10 | Mostly holds (§9.2 is unchanged); swap epoch numbers for `ref` |
| `hub.md`, `scale.md` | 55 | ~10 | Mostly holds |
| `collab.md`, `realtime.md` | 56 | 0 | Hold |

Add scenarios for what is new: the fork rule itself (including the two defects above as regression cases), device seniority, sign-out and the residual of Q15, the user key shared by all of a user's devices, and the Q17 rotation spam.

**Recommendation:** wait for Sol's pass, then do one refresh of every file against the settled text, rather than chasing a moving draft twice.

### Proofs (`proofs/README.md`)

- **Unchanged:** `Supersession`, `Register`, `ORSet`, `Tombstone`, `Display`, `HLC` (§9.1 to §9.3 didn't change), and `GC`, whose counterexample still applies.
- **Update:** `Encoding`. `sigInput` now carries `ref` instead of the epoch and the `prev` list; the scope is zero in a genesis, and the device ID is zero for the recovery key.
- **Replace:** `Roles` and `Log` become `Chain`:
  - validity depends only on a statement's own line, so it never changes;
  - the head is a function of the set of statements held;
  - with the fork rule fixed, a party that someone of higher rank cut off can't win at any length.

  The current rule fails both of the last two properties as intended (see §2). Lean is the right place to settle a fixed rule, because the counterexamples above are exactly what a proof attempt finds.
- **Add:** `Snapshot`. A write valid in an epoch stays valid, and an old write is valid in a new epoch only if the rotating device's list names it.

### The reference model

Fable's `model/tandem_model.py` now exists and is cited by the design (Q14).

**Decided (Jesper, 2026-11-12): the reference model stays in Lean**, executable and proved, and is the harness's oracle, as `TESTING.md` says. Fable's Python model becomes an independent second implementation of the same rules. A differential test feeds both the same random inputs and compares their answers, so a disagreement points at either a bug or an ambiguity in the text. Its regression cases, and the two defects in §2, become Lean examples.

### Unaffected

The testbed, the binding and real-time probes, the Go reference SDK plan and the Swift plan.
