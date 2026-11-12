# Tandem proofs (Lean 4 + Mathlib)

**Status: scaffolding.** The project builds; no theorems yet. Proofs start once the design settles.

```sh
lake exe cache get     # once: prebuilt Mathlib (~7 GB in .lake/, git-ignored)
lake build
```

Pinned to Lean 4.35.0 and Mathlib `v4.35.0`.

## Planned theorems

What can be proved about Tandem is the logic: merge rules, garbage collection, encodings, roles. The cryptography (that ChaCha20-Poly1305 and Ed25519 are secure) is assumed, not proved.

| Module | Statement | Design |
|---|---|---|
| `Supersession` | Supersession is a strict partial order (irreflexive, transitive) for **any** `context` and `base`, honest or malformed; a write never supersedes one from a later epoch. | §9.2, §9.3 |
| `Register` | The kept set (writes no other write supersedes) of a union equals the kept set of the union of kept sets. Merging is therefore commutative, associative and idempotent, so garbage-collected and full states converge. | §9.3 |
| `ORSet` | A concurrent add and remove leave the element present; a remove that covers every add takes it out; a replayed superseded add stays superseded. | §9.2 |
| `Tombstone` | Deletion is monotone under merge: once any replica holds a valid `delete`, every merge containing it is deleted. | §9.2 |
| `Display` | The displayed sibling (greatest by `(epoch, hlc, author, deviceID, writeID)`) is independent of arrival order, and the 16-sibling limit is presentation only. | §9.2 |
| `HLC` | A device's HLC values are strictly increasing, including counter overflow. | §9.1 |
| `GC` | Destroying dominated messages never loses a write, under any interleaving of devices acting on stale views, given the tie-break below. | §10.7 |
| `Encoding` | `sigInput` (§5.5) is injective: two different writes never produce the same signed bytes. The `TANDEM-…-V1` prefixes and HKDF `info` labels are pairwise distinct and prefix-free, so a signature for one object type can't be read as another's. | §5.2, §5.5 |
| `Roles` | Under ranked roles, no write lets a member act on an equal or a higher rank; least-privilege resolution is a total, order-independent function of the concurrent writes. | §8.1, §8.2 |
| `Log` | A log write's verdict depends only on its own past ("valid on its own past" never changes), so validity is a function of the set of writes held, not their arrival order. | §9.4, §8.3 |

### A defect found while planning `GC`

§10.7 lets a device destroy a message that another message on the hub dominates, with a lowest-hash tie-break only for "two messages holding exactly the same writes". Domination can be mutual between messages holding **different** writes. Take write `y` superseding write `x`, `m = {x, y}` and `n = {y}`:

- `n` dominates `m`, because `y` is in `n` and `x` is superseded by `y`;
- `m` dominates `n`, because `y` is in `m`.

The two sets aren't equal, so the tie-break doesn't apply. Two devices can then destroy one each, concurrently, and `y` is lost from the hub.

The fix is to apply the tie-break to any mutual domination, not only to identical write sets.

With it, "dominated and losing the tie-break" is a strict partial order on messages. Follow any destroyed message upward through the messages that justified destroying it, and the chain ends at a message nobody destroys. So nothing is lost, even when devices act on stale views. That is the theorem `GC` will prove; the counterexample above will be checked in Lean too.
