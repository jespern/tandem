# `tandem-oracle`

The Lean reference model as a test oracle. It runs the definitions in `TandemProofs/` that the theorems are about; `Oracle/Main.lean` only parses, resolves IDs and prints.

```sh
lake build tandem-oracle          # .lake/build/bin/tandem-oracle
.lake/build/bin/tandem-oracle < requests.ndjson
```

One JSON request per line on stdin. One JSON answer per line on stdout, flushed after each line, so a harness can keep a single process open. A request the oracle can't use gets `{"error": "…"}`.

Cryptography is symbolic, as in `model/owner_model.py`. IDs are whatever strings the caller uses (the Go harness passes real hex IDs). Signatures are participant names: device certificates and the registry (§6.9) are outside the oracle, which is told instead which policy snapshots extend which floors. Hashes are the caller's business:

- the oracle never checks an ID against content;
- the caller passes only inventories whose hash it has checked.

## Request

Every field is optional except `genesis` and `statements`.

| Field | Meaning |
|---|---|
| `genesis` | ID of the statement the device expects as genesis (the one the collection ID names). |
| `statements` | Roster statements the device holds, in any order. A repeated ID counts once. Each is `{id, parent, signer, n, epoch, commit, snapshot, deleted, members, announced}`.<br>• `parent` and `snapshot` may be `null`.<br>• `epoch` is the key counter `key.n` and `commit` the key commitment.<br>• `members` is `[{uid, role, name, floor}]`: participant ID, role (any string; only `owner`, `writer` and `reader` are valid), display name, and the device-policy floor the epoch pins for them (§8.1).<br>• `announced` (default `true`): for a statement that starts an epoch, whether the device has seen it in the previous epoch's channel. If not, it is a draft. |
| `writes` | Data writes: `{id, author, ref, record, field, op, elem, value, dev, hlc, ctx, base, auth}`.<br>• `auth` is the policy snapshot the signature binds (§5.5); a direct write counts only if it extends the author's floor (§8.3).<br>• `op` is `assign` (default), `add`, `remove` or `delete`.<br>• A `delete` must have empty `field`, `elem` and `value`, or it is invalid (§9.2).<br>• `hlc` is one integer, the wire's `millis·2^16 + counter`; 2^64 or more (millis past 2^48) is invalid.<br>• `ctx` and `base` are `[[device, hlc], …]` or `{device: hlc}`. |
| `meta`, `named` | Record IDs of `_meta`, and of every named record (on which `delete` is invalid). |
| `extends` | `[{snapshot, floor}]`: which policy snapshots extend which floors, besides each floor extending itself. |
| `channel` | Epoch-start statement ID whose channel is being judged. Default: the epoch of the head. |
| `inventory` | That epoch's assembled inventory, `{record: [writeID, …]}`, or `null` while it is missing. |
| `messages` | For the clean-up rule: `[{id, hash, writes: [writeID, …]}]`. `hash` is an integer standing for `H(envelope)`. |
| `reissue` | Late writes to sign again under the head: `[{write: writeID, state: [writeID, …]}]`. `state` is the new epoch's state the device holds. |
| `memberships` | `[{uid, answers: [[admissionID, "a" \| "n" \| "l"], …], held: [statementID, …]}]`: a user's add-only answers from home, and the `at` statements of the grants their devices hold (§11.2). |
| `inventoryBytes` | `{recordHex: [writeIdHex, …]}`: canonical inventory bytes to compute. Records are 16 bytes and write IDs 32. |

## Answer

| Field | Meaning | Lean |
|---|---|---|
| `accepted` | Statements the device follows. | `accepted` |
| `head` | The roster head: the statement reached by following the one accepted successor while there is no conflict. | `head` |
| `dead` | Whether two different owner-signed, counted successors of one followed statement are held. | `dead` |
| `conflicts` | Those pairs. | `authenticated` |
| `epochs` | For each accepted statement, the statement that starts its epoch. | `epochStart` |
| `channel` | The epoch judged. | |
| `held` | Writes whose `ref` the device doesn't follow (yet). | |
| `eligible` | Writes eligible in `channel` (§8.3). | `eligible` |
| `supersedes` | `[w, x]` pairs, among writes whose `ref` is followed: `w` supersedes `x` (§9.2). Registers: (record, field) for `assign`, (record, field, element) for `add`/`remove`; a `delete` supersedes any write to its record that it includes, and only a `delete` supersedes a `delete` (decided 2026-11-12). | `sup`, `Write.governs` |
| `state` | Per record, the kept eligible writes:<br>• `deleted`;<br>• `fields`: `{field: {kept: [ids], display: id}}`;<br>• `sets`: `{field: [present elements]}`. | `kept`, `display` |
| `dominated` | `[m, n]`: message `n` dominates message `m` (§10.7). | `dominated` |
| `mayDestroy` | `[m, n]`: `m` may be destroyed on account of `n`. | `mayDestroy` |
| `reissue` | Per write, `{status}`:<br>• `refused`: dead, invalid, not a current writer, or over the vector limit;<br>• `skip`: the state holds it or supersedes it;<br>• `new`, with `write`: the re-signed write, without an ID (the harness computes it). Vectors are normalised: largest value per device, sorted. | `reissue`, `reissueIn` |
| `memberships` | `{uid: {state, admission}}` at the head: `state` is `removed`, `none`, `offered`, `active` or `left`; `admission` is the statement that added them, or `null` (§11.2). | `mstate`, `admission` |
| `inventoryBytes` | Lowercase hex of the canonical inventory (§8.3). | `Enc.inventoryBytes`, `Enc.canon` |

## Checked against `model/owner_model.py`

```sh
python3 -B difftest.py [runs] [first-seed]
```

Each run builds a random history:

- valid and invalid statements;
- rotations whose inventories list earlier writes;
- conflicting successors, and statements signed by others;
- random writes, some with vectors over the limit, and about one in five a delete;
- fields named `é` in both Unicode spellings;
- messages with colliding hashes, answers to real and unknown admissions, held grants, and late writes.

It delivers a random subset of the history, in random order, to an `OwnerLog`. It then compares, between the two models:

- `accepted`, `dead`, `head` and `epochs`;
- `supersedes`, `dominated` and `mayDestroy`;
- each user's admission and membership state;
- `reissue`, both status and fields;
- eligibility in every followed epoch, with its inventory;
- inventory bytes.

**Deletes are compared through an adjustment.** `owner_model.py` has no ops: it compares every write by (record, field), so on its own it would treat a delete (field `""`) as a register of its own field and never let it supersede another field. `difftest.py` doesn't edit the model; it wraps the model's `supersedes` (`with_delete_registers`) with the decided register rule and leaves the model's vector test alone:

- a non-delete never supersedes a delete;
- a delete is tested against any write to its record as if it had that write's field;
- every other pair goes to the model unchanged.

The model's `dominated`, `may_destroy` and `reissue` call `supersedes` through the module, so they get the same rule. What is still differential for deletes is the vector test, epochs, eligibility and clean-up; the register rule itself is the harness's and is proved in Lean (`Record.lean`). With the wrapper removed, the oracle disagrees with the model on 25 of the first 200 seeds.

The Python model doesn't cover these, so they are checked only in Lean (`Examples.lean`):

- display;
- sets;
- tombstones, and the state a delete leaves (`delete_built_over_state`);
- drafts;
- `_meta` and named-record deletes;
- device floors and the clock bound (`owner_model.py` has neither: the harness sends empty floors and snapshots);
- JSON and `b64u` strictness (the Go SDK's job).
