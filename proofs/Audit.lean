import TandemProofs
import Lean.Util.CollectAxioms

/-!
Run after `lake build`, with `lake env lean Audit.lean`.
Check the transitive dependencies of the protocol declarations and their private helpers.
Only Lean's standard logical axioms are allowed: no unfinished proofs or custom axioms.
-/

open Lean Elab Command

run_cmd do
  let env ← getEnv
  let allowed : Array Name := #[``propext, ``Classical.choice, ``Quot.sound]
  let mut checked : Nat := 0
  for (name, _) in env.constants.toList do
    if name.toString.startsWith "Tandem." || name.toString.startsWith "_private.TandemProofs" then
      let axioms ← collectAxioms name
      for ax in axioms do
        unless allowed.contains ax do
          throwError "{name} depends on unapproved axiom {ax}"
      checked := checked + 1
  logInfo m!"Audited {checked} declarations: only standard Lean axioms; no sorryAx or custom axioms."
