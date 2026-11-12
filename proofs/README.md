# Tandem proofs and reference model (Lean 4 + Mathlib)

**Status:** proved against the frozen `DESIGN.md` (`main` at `f4ce2e6`). Nothing is left as `sorry`, and no axioms are added. The same definitions run as the test oracle, `tandem-oracle` (`ORACLE.md`).

```sh
lake exe cache get          # once: prebuilt Mathlib (~7 GB in .lake/, git-ignored)
lake build                  # the proofs
lake build tandem-oracle    # the oracle (first build compiles Mathlib's C, ~2 min, ~0.5 GB)
python3 -B difftest.py      # oracle vs model/owner_model.py on random histories
```

Pinned to Lean 4.35.0 and Mathlib `v4.35.0`.

## What is proved

The logic is proved: merge rules, roster rules, garbage collection, encodings. The cryptography is assumed, not proved (§ Assumptions).

| Module | Statement | Design | Status |
|---|---|---|---|
| `Kept` | Under **any** strict partial order:<br>• the kept set of a union is the kept set of the union of kept sets (`kept_union`);<br>• merging is commutative, associative and idempotent (`merge_comm`, `merge_assoc`, `merge_idem`);<br>• delivery order, grouping and duplicates never matter (`receive_of_perm`);<br>• replaying a superseded write changes nothing (`kept_insert_superseded`). | §9.3 | proved |
| `Supersession` | Supersession is a strict partial order for any `context` and `base`, malformed included (`sup_strict`). A write never supersedes a later epoch's (`sup_epoch_le`). Two writes that each claim to include the other are both kept (`mutual_include_conflict`). The record-state instances of `Kept` follow. | §9.2, §9.3 | proved |
| `Record` | **Sets:**<br>• an add that nothing supersedes keeps its element (`add_wins`, `add_remove_concurrent`);<br>• a remove that saw the adds removes the element (`remove_supersedes_seen_add`, `absent_of_all_adds_superseded`);<br>• a replayed add stays out.<br>**Tombstones** are sticky under merge (`deleted_sticky`).<br>**Display** is independent of arrival order (`display_perm`).<br>Keeping only the top siblings in the *state* would make results depend on order (`truncated_state_depends_on_order`), which is why the 16-sibling limit is for presentation only. | §9.2 | proved; one finding (F1) |
| `HLC` | A device's clock values strictly increase, counter overflow included, whatever the wall clock does (`run_chain`). The counter stays in 16 bits. The wire's 64-bit order agrees (`HLC.toNat_lt`). | §9.1 | proved |
| `GC` | **Under the old rule:** `{x, y}` and `{y}` may each be destroyed on account of the other, and `y` is lost (`old_rule_loses_y`, `old_rule_trace_loses`).<br>**Under the frozen rule:**<br>• at most one of two messages that dominate each other may go (`new_rule_keeps_one`);<br>• "may destroy on account of" is a strict partial order (`md_strict`);<br>• under any interleaving of justified destroys on stale views, every message ever on the hub is dominated by one still there, so no write is lost (`gc_safe`, `gc_no_write_lost`). | §10.7 | proved; assumption A3 |
| `Roster` | **Validity depends only on the parent** (`chainOK_cons`).<br>**What a device follows and whether the collection is dead are functions of the set held:**<br>• order and duplicates don't matter (`same_set`, `same_set_perm`);<br>• once dead, dead in every superset (`dead_mono`);<br>• every device holding both conflicting statements and the parent's line agrees (`dead_of_held`, `conflict_agreed`);<br>• unannounced drafts change nothing (`drafts_change_nothing`). | §8.2, §9.4, §11.6 | proved |
| `Eligibility` | Eligibility never changes once the statement a write names and the epoch start are followed, while the collection is alive (`eligible_stable`, `eligible_mono`).<br>• Readers can't write (`reader_rejected`).<br>• Only the owner writes `_meta` (`meta_owner_only`).<br>• `delete` on a named record is invalid (`named_delete_rejected`).<br>• Without the inventory, only writes whose source epoch is exactly `E` count (`without_inventory`). | §7.3, §8.3 | proved |
| `Reissue` | **Signing again:**<br>• every device of the user gets the same write, at any later head of that epoch (`reissue_deterministic`);<br>• it never supersedes a write of its new epoch (`reissued_not_over_new`);<br>• it keeps replacing what the original replaced (`reissued_keeps_replacing`), and nothing the original's vectors didn't cover (`reissued_only_what_was_seen`);<br>• two re-signed writes lose their order (`reissued_pair_conflicts`);<br>• it stays within the vector limit (`reissue_within_limit`). | §11.6 | proved; correction F2 to our plan |
| `VectorLimit` | A write built from the kept set supersedes all of it, given a fresh dot (`build_supersedes`). Its vectors name only devices the kept writes name, so honest fields stay within 1,024 entries while fewer than 1,025 devices have written them (`honest_build_within_limit`, `reissue_devices`). Writes over the limit are invalid (`over_limit_invalid`). | §9.2 | proved |
| `Invite` | **Closed stays closed** at every later roster head (`closed_stays_closed`).<br>• Removing anyone who came in through an invite closes it, read from the roster alone, even if they come back another way (`removal_closes`).<br>• A former member is never admitted unasked (`former_member_needs_asking`). | §11.1, §11.3 | proved |
| `Encoding` | **`sigInput` is injective** on well-formed fields (`sigInput_inj`).<br>• The `TANDEM-…-V1` prefixes, and the labels hashed in front of data, are prefix-free, so inputs of different kinds never coincide (`signed_domains_separate`, `hash_domains_separate`).<br>• HKDF `info` labels are distinct per input key (`kdf_labels_distinct`).<br>• The canonical inventory bytes are a function of the set of (record, write) pairs and determine it (`inventory_bytes_inj`). | §5.2–§5.6, §8.3 | proved; one finding (F3) |
| `Examples` | The regressions of `model/test_owner_model.py` that concern these rules, rebuilt and checked by `decide` on the model's own definitions. | | checked |

## Findings

**F1. A delete's register isn't specified (§9.2, §9.3).**
- **What the design says:**
  - a record with any valid delete is deleted "whatever else happens to it";
  - a record's state is the writes nothing supersedes;
  - supersession is between writes "to the same field".
- **What it doesn't say:** which field a `delete` names (`sigInput` carries one), or whether an `assign` (or `add`) can supersede a `delete`.
- **Why it matters:** `model/owner_model.py` compares (record, field) only. Under that reading, a write to the delete's field that covers it drops the tombstone from the state (`tombstone_dropped_if_keyed_by_field`).
- **What the Lean model does** (`Write.slot`):
  - a delete is the record's own register, so only a delete supersedes a delete;
  - each set element is its own register;
  - `assign` and `add`/`remove` on one field name are separate registers.

  With that, `deleted_sticky` holds.
- **Suggested fix:** state the register key in §9.2: (record, field) for `assign`, (record, field, element) for `add`/`remove`, and the record alone for `delete`. The SDKs need this to agree.

**F2. Our plan, not the design, was wrong about signing again.**
- **The plan's claim:** "a re-signed write never supersedes a write the snapshot kept". It is false.
- **Why it's false, and why that's right:** a late write that replaced a snapshot write in the old epoch replaces it again once signed in the new one (`late_write_replaces_snapshot_again`). That is correct behaviour, and §11.6 never claimed otherwise.
- **What holds instead:** the theorems in `Reissue`.

**F3. Inventory entries with no writes (§8.3).**
- **The gap:** the byte format allows a record with a write count of zero, and `model/owner_model.py` emits one for `{rec: []}`. The bytes are then not a function of the set of (record, write) pairs: `{a: [x]}` and `{a: [x], b: []}` list the same writes under different commitments.
- **Why it's harmless:** every device judges against the committed bytes.
- **The cost:** the bytes stop being canonical. A device resuming from its journal must reuse the exact bytes, as §11.6 already requires.
- **What the Lean model does:** `Enc.canon` never emits an empty entry. `difftest.py` doesn't generate empty entries, so it doesn't test this case.
- **Suggested fix:** say in §8.3 that every entry lists at least one write.

## Assumptions

1. **Symbolic cryptography.**
   - A statement or write is its content, and its ID is collision-free.
   - A signature is the signer's name. Whoever signed holds the key.
   - A statement carries its ancestry (`Stmt` is the list of nodes, nearest first), as its ID commits to its parent's.
2. **Supersession compares epochs by their counter**, as `owner_model.py` does. On one roster line the counter and the epoch-start ID determine each other.
3. **Byte-identical copies (§10.7).** The device that made an envelope never removes a copy on account of a copy it has itself already removed: it sees its own destroys. Every other destroy may act on a stale view. Its justifying message must only have been on the hub at some point.
4. **Not modelled:**
   - the six-step rotation procedure, grants, key requests and the `flight` note. These are procedures; `owner_model.py` simulates them, and the scenario suite will exercise them on the testbed;
   - pairing;
   - the mail bindings;
   - JSON and `b64u` strictness (§10.5), which are the SDKs' job, with test vectors;
   - HLC millis overflowing 48 bits;
   - invite expiry, which needs a clock.
5. **Unprefixed hashes:** `H(value)`, `H(US)`, `H(inventory bytes)`, `H(envelope)`. They hash data that is never read as another domain's input.
