import TandemProofs.Kept
import TandemProofs.Model

/-!
# Supersession is a strict partial order, for any context and base (§9.2, §9.3)

Nothing here assumes the vectors are honest: a writer may sign any `context` and `base`,
claim to cover the future, or claim to cover a write that claims to cover it.
-/

namespace Tandem

theorem includes_trans {a b c : Write} (h1 : includes a b) (h2 : includes b c) : includes a c :=
  ⟨vle_trans h2.1 (vle_trans (vle_ctx_reach b) h1.1), vle_trans h2.2 h1.2⟩

theorem sup_irrefl (w : Write) : ¬ sup w w := by
  rintro ⟨-, ⟨h, -⟩ | ⟨-, h1, h2⟩⟩
  · exact lt_irrefl _ h
  · exact h2 h1

theorem sup_trans {a b c : Write} (hab : sup a b) (hbc : sup b c) : sup a c := by
  obtain ⟨sab, hab⟩ := hab
  obtain ⟨sbc, hbc⟩ := hbc
  refine ⟨sab.trans sbc, ?_⟩
  rcases hab with ⟨eab, rab, bab⟩ | ⟨eab, iab, nba⟩ <;>
    rcases hbc with ⟨ebc, rbc, bbc⟩ | ⟨ebc, ibc, ncb⟩
  · -- later, later
    exact Or.inl ⟨ebc.trans eab, vle_trans rbc bab, vle_trans bbc bab⟩
  · -- later, same
    exact Or.inl ⟨ebc ▸ eab, vle_trans ibc.1 (vle_trans (vle_ctx_reach b) rab),
      vle_trans ibc.2 bab⟩
  · -- same, later
    exact Or.inl ⟨eab ▸ ebc, vle_trans rbc iab.2, vle_trans bbc iab.2⟩
  · -- same, same
    refine Or.inr ⟨eab.trans ebc, includes_trans iab ibc, fun ica => ncb ?_⟩
    exact includes_trans ica iab

/-- Supersession is a strict partial order. -/
theorem sup_strict : IsStrict sup := ⟨sup_irrefl, sup_trans⟩

/-- A write never supersedes a write from a later epoch. -/
theorem sup_epoch_le {w x : Write} (h : sup w x) : x.epoch ≤ w.epoch := by
  rcases h.2 with ⟨h, -⟩ | ⟨h, -⟩
  · exact h.le
  · exact h.ge

/-- Two writes that each claim to include the other are both kept (§9.2). -/
theorem mutual_include_conflict {w x : Write} (he : w.epoch = x.epoch)
    (h1 : includes w x) (h2 : includes x w) : ¬ sup w x ∧ ¬ sup x w := by
  constructor
  · rintro ⟨-, ⟨h, -⟩ | ⟨-, -, h⟩⟩
    · exact absurd he (ne_of_gt h)
    · exact h h2
  · rintro ⟨-, ⟨h, -⟩ | ⟨-, -, h⟩⟩
    · exact absurd he.symm (ne_of_gt h)
    · exact h h1

/-! ## Register state and merge (§9.3) -/

/-- A record's state: its valid writes that no other valid write supersedes. -/
def state (S : Finset Write) : Finset Write := kept sup S

/-- Merging states is a join on a semilattice. -/
theorem state_merge_comm (A B : Finset Write) : merge sup A B = merge sup B A := merge_comm sup_strict A B
theorem state_merge_assoc (A B C : Finset Write) :
    merge sup (merge sup A B) C = merge sup A (merge sup B C) := merge_assoc sup_strict A B C
theorem state_merge_idem (A : Finset Write) :
    merge sup (state A) (state A) = state A := merge_idem sup_strict A

/-- Pruned and unpruned replicas converge: the state of a union is the state of the union of states. -/
theorem state_union (S T : Finset Write) : state (S ∪ T) = state (state S ∪ state T) :=
  kept_union sup_strict S T

/-- Delivery order, grouping and duplication never matter. -/
theorem state_receive_perm {bs cs : List (Finset Write)} (h : bs.Perm cs) :
    receive sup bs = receive sup cs := receive_of_perm sup_strict h

/-- A replayed write that something held supersedes changes nothing (§3.2). -/
theorem state_replay {S : Finset Write} {x y : Write} (hy : y ∈ S) (hyx : sup y x) :
    state (insert x S) = state S := kept_insert_superseded sup_strict hy hyx

end Tandem
