import Mathlib.Data.Finset.Card
import Mathlib.Order.Defs.Unbundled

/-!
# Kept sets under any strict partial order (DESIGN §9.3)

A record's state is the set of its valid writes that no other valid write supersedes.
Everything here holds for *any* relation that is irreflexive and transitive; `Write.lean`
shows that Tandem's supersession is one. `r w x` reads "`w` supersedes `x`".
-/

set_option linter.unusedSectionVars false

namespace Tandem

/-- A strict partial order, stated without type classes. -/
structure IsStrict {α : Type*} (r : α → α → Prop) : Prop where
  irrefl : ∀ a, ¬ r a a
  trans : ∀ {a b c}, r a b → r b c → r a c

variable {α : Type*} [DecidableEq α] (r : α → α → Prop) [DecidableRel r]

/-- The writes of `S` that nothing in `S` supersedes. -/
def kept (S : Finset α) : Finset α := S.filter (fun x => ∀ y ∈ S, ¬ r y x)

variable {r}

theorem mem_kept {S : Finset α} {x : α} : x ∈ kept r S ↔ x ∈ S ∧ ∀ y ∈ S, ¬ r y x := by
  simp [kept]

theorem kept_subset (S : Finset α) : kept r S ⊆ S := Finset.filter_subset _ _

variable (hr : IsStrict r)
include hr

/-- Every write of a finite set is kept, or superseded by a kept write. -/
theorem kept_or_superseded (S : Finset α) {x : α} (hx : x ∈ S) :
    x ∈ kept r S ∨ ∃ k ∈ kept r S, r k x := by
  induction h : (S.filter (fun y => r y x)).card using Nat.strong_induction_on generalizing x with
  | _ n ih =>
    by_cases hk : ∀ y ∈ S, ¬ r y x
    · exact Or.inl (mem_kept.2 ⟨hx, hk⟩)
    · simp only [not_forall, not_not, exists_prop] at hk
      obtain ⟨y, hyS, hyx⟩ := hk
      have hlt : (S.filter (fun z => r z y)).card < n := by
        rw [← h]
        apply Finset.card_lt_card
        refine ⟨fun z hz => ?_, fun hsub => ?_⟩
        · simp only [Finset.mem_filter] at hz ⊢
          exact ⟨hz.1, hr.trans hz.2 hyx⟩
        · have := hsub (Finset.mem_filter.2 ⟨hyS, hyx⟩)
          exact hr.irrefl y (Finset.mem_filter.1 this).2
      rcases ih _ hlt hyS rfl with hy | ⟨k, hk, hky⟩
      · exact Or.inr ⟨y, hy, hyx⟩
      · exact Or.inr ⟨k, hk, hr.trans hky hyx⟩

/-- A write superseded by anything in `S` is superseded by something kept in `S`. -/
theorem superseded_by_kept {S : Finset α} {x y : α} (hy : y ∈ S) (hyx : r y x) :
    ∃ k ∈ kept r S, r k x := by
  rcases kept_or_superseded hr S hy with h | ⟨k, hk, hky⟩
  · exact ⟨y, h, hyx⟩
  · exact ⟨k, hk, hr.trans hky hyx⟩

/-- Pruning `S` to its kept set before merging changes nothing (DESIGN §9.3). -/
theorem kept_kept_union (S T : Finset α) : kept r (kept r S ∪ T) = kept r (S ∪ T) := by
  ext x
  simp only [mem_kept, Finset.mem_union]
  constructor
  · rintro ⟨hx | hx, hn⟩
    · refine ⟨Or.inl hx.1, fun y hy hyx => ?_⟩
      rcases hy with hy | hy
      · obtain ⟨k, hk, hkx⟩ := superseded_by_kept hr hy hyx
        exact hn k (Or.inl (mem_kept.1 hk)) hkx
      · exact hn y (Or.inr hy) hyx
    · refine ⟨Or.inr hx, fun y hy hyx => ?_⟩
      rcases hy with hy | hy
      · obtain ⟨k, hk, hkx⟩ := superseded_by_kept hr hy hyx
        exact hn k (Or.inl (mem_kept.1 hk)) hkx
      · exact hn y (Or.inr hy) hyx
  · rintro ⟨hx, hn⟩
    refine ⟨?_, fun y hy hyx => ?_⟩
    · rcases hx with hx | hx
      · exact Or.inl ⟨hx, fun y hy => hn y (Or.inl hy)⟩
      · exact Or.inr hx
    · rcases hy with hy | hy
      · exact hn y (Or.inl hy.1) hyx
      · exact hn y (Or.inr hy) hyx

theorem kept_union_kept (S T : Finset α) : kept r (S ∪ kept r T) = kept r (S ∪ T) := by
  rw [Finset.union_comm, kept_kept_union hr, Finset.union_comm]

/-- The fundamental identity: the kept set of a union is the kept set of the union of kept sets. -/
theorem kept_union (S T : Finset α) : kept r (S ∪ T) = kept r (kept r S ∪ kept r T) := by
  rw [kept_kept_union hr, kept_union_kept hr]

theorem kept_idem (S : Finset α) : kept r (kept r S) = kept r S := by
  simpa using kept_kept_union (r := r) hr S ∅

/-- Replaying a write that something in `S` supersedes changes nothing (DESIGN §3.2, replay). -/
theorem kept_insert_superseded {S : Finset α} {x y : α} (hy : y ∈ S) (hyx : r y x) :
    kept r (insert x S) = kept r S := by
  ext z
  simp only [mem_kept, Finset.mem_insert]
  constructor
  · rintro ⟨rfl | hz, hn⟩
    · exact absurd hyx (hn y (Or.inr hy))
    · exact ⟨hz, fun w hw => hn w (Or.inr hw)⟩
  · rintro ⟨hz, hn⟩
    refine ⟨Or.inr hz, fun w hw hwz => ?_⟩
    rcases hw with rfl | hw
    · exact hn y hy (hr.trans hyx hwz)
    · exact hn w hw hwz

/-! ## Merge: a semilattice join on states -/

/-- Merging two states: union, then drop what is superseded. -/
def merge (r : α → α → Prop) [DecidableRel r] (A B : Finset α) : Finset α := kept r (A ∪ B)

theorem merge_comm (A B : Finset α) : merge r A B = merge r B A := by
  simp [merge, Finset.union_comm]

theorem merge_assoc (A B C : Finset α) :
    merge r (merge r A B) C = merge r A (merge r B C) := by
  simp only [merge]
  rw [kept_kept_union hr, kept_union_kept hr, Finset.union_assoc]

/-- On states (kept sets), merging with yourself does nothing. -/
theorem merge_idem (A : Finset α) : merge r (kept r A) (kept r A) = kept r A := by
  simp [merge, kept_idem hr]

/-- A device's state after receiving batches of writes in any order, with any duplicates. -/
def receive (r : α → α → Prop) [DecidableRel r] (batches : List (Finset α)) : Finset α :=
  batches.foldl (fun st b => merge r st b) ∅

theorem receive_eq (batches : List (Finset α)) :
    receive r batches = kept r (batches.foldr (· ∪ ·) ∅) := by
  suffices h : ∀ (st : Finset α) (bs : List (Finset α)),
      bs.foldl (fun st b => merge r st b) (kept r st) = kept r (st ∪ bs.foldr (· ∪ ·) ∅) by
    simpa [receive, kept] using h ∅ batches
  intro st bs
  induction bs generalizing st with
  | nil => simp
  | cons b bs ih =>
    simp only [List.foldl_cons, List.foldr_cons]
    have : merge r (kept r st) b = kept r (st ∪ b) := by simp [merge, kept_kept_union hr]
    rw [this, ih, Finset.union_assoc]

/-- **Convergence** (DESIGN §9.3): devices that received the same writes hold the same state,
whatever the order, grouping or duplication of delivery. -/
theorem receive_perm {bs cs : List (Finset α)}
    (h : bs.foldr (· ∪ ·) ∅ = cs.foldr (· ∪ ·) ∅) : receive r bs = receive r cs := by
  rw [receive_eq hr, receive_eq hr, h]

theorem foldr_union_perm {bs cs : List (Finset α)} (h : bs.Perm cs) :
    bs.foldr (· ∪ ·) ∅ = cs.foldr (· ∪ ·) (∅ : Finset α) := by
  induction h with
  | nil => rfl
  | cons x _ ih => simp [ih]
  | swap x y l => simp [← Finset.union_assoc, Finset.union_comm x y]
  | trans _ _ ih1 ih2 => exact ih1.trans ih2

/-- Delivery order never matters. -/
theorem receive_of_perm {bs cs : List (Finset α)} (h : bs.Perm cs) :
    receive r bs = receive r cs := receive_perm hr (foldr_union_perm hr h)

end Tandem
