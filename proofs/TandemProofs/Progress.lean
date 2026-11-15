import TandemProofs.GC
import TandemProofs.Drop

/-!
# Garbage collection makes progress (GC.md, Jesper's question of 2026-11-15)

`GC` proves clean-up safe: whatever devices destroy, no write is lost (`gc_safe`). `Drop` proves
dropping safe: a device's state doesn't change (`same_state`). This module proves the other half,
that everything collectable is collected once devices keep running and nothing new arrives:

* **The hub** (§10.7). Destroying, one at a time, a live message that another live message may
  destroy it on account of always stops (`hub_terminates`); every message destroyed on the way is
  dominated by one that is left (`hub_steps_safe`); and once it has stopped, if one live message
  dominates all the others, as the merged state a device publishes does, it is the only one left
  (`hub_one_message`).
* **A device** (§9.3, `GC.md` §2). Dropping always stops (`drops_terminate`), and where it stops a
  device holds exactly this residue (`residue`): its state; its own writes of the current epoch
  that only another author's writes replaced, which §11.6 may need; and replaced writes carried in
  through the current inventory. Both of the last go at the next key change, when they are no
  longer eligible (`Drop`'s `dropPast`).

What is not modelled, and is kept by design: tombstones and marks (§12.3), the roster and inventory
fragments of the current channel, and the timed holds (a gone list for its retention period, an
orphan chunk for its grace). Byte-identical copies are excluded by assuming distinct hashes; §10.7
leaves them to the device that made them.
-/

set_option linter.unusedSectionVars false

namespace Tandem.Progress

open Classical

/-! ## The hub -/

section Hub

variable {α M : Type*} [DecidableEq α] [DecidableEq M] {r : α → α → Prop}
variable (r) (ws : M → Finset α) (hash : M → ℕ)

/-- One clean-up step on the live messages: destroy one that another live message may destroy
it on account of (§10.7). -/
def hubStep (L L' : Finset M) : Prop :=
  ∃ m ∈ L, ∃ n ∈ L, md (r := r) ws hash m n ∧ L' = L.erase m

/-- Settled: no live message may be destroyed on account of another. -/
def settled (L : Finset M) : Prop := ∀ m ∈ L, ∀ n ∈ L, ¬ md (r := r) ws hash m n

variable {r ws hash}

/-- **Clean-up stops**: from any live set, steps reach a settled one. -/
theorem hub_terminates (L : Finset M) :
    ∃ L', Relation.ReflTransGen (hubStep r ws hash) L L' ∧ settled r ws hash L' := by
  induction h : L.card using Nat.strong_induction_on generalizing L with
  | _ n ih =>
    by_cases hs : settled r ws hash L
    · exact ⟨L, Relation.ReflTransGen.refl, hs⟩
    · simp only [settled, not_forall, not_not, exists_prop] at hs
      obtain ⟨m, hm, k, hk, hmk⟩ := hs
      have hlt : (L.erase m).card < n := h ▸ Finset.card_erase_lt_of_mem hm
      obtain ⟨L', hpath, hset⟩ := ih _ hlt (L.erase m) rfl
      exact ⟨L', Relation.ReflTransGen.head ⟨m, hm, k, hk, hmk, rfl⟩ hpath, hset⟩

/-- **Nothing is lost on the way**: every message of the start is dominated by one left. -/
theorem hub_steps_safe (hr : IsStrict r) {L L' : Finset M}
    (h : Relation.ReflTransGen (hubStep r ws hash) L L') :
    ∀ m ∈ L, ∃ q ∈ L', dominated r (ws m) (ws q) := by
  induction h with
  | refl => exact fun m hm => ⟨m, hm, dominated_refl _⟩
  | tail _ step ih =>
    rename_i L1 L2
    obtain ⟨x, hx, k, hk, hxk, rfl⟩ := step
    intro m hm
    obtain ⟨q, hq, hmq⟩ := ih m hm
    by_cases hqx : q = x
    · subst hqx
      have hkq : k ≠ q := fun e => (md_strict ws hash hr).irrefl q (e ▸ hxk)
      exact ⟨k, Finset.mem_erase.2 ⟨hkq, hk⟩, dominated_trans hr hmq hxk.1⟩
    · exact ⟨q, Finset.mem_erase.2 ⟨hqx, hq⟩, hmq⟩

/-- **One message is left** once clean-up has stopped, if a live message dominates all of them
(the merged state a device publishes when no single message holds its state, §10.7) and no two
live messages share a hash. -/
theorem hub_one_message {L : Finset M} (hs : settled r ws hash L) {q : M} (hq : q ∈ L)
    (hall : ∀ m ∈ L, dominated r (ws m) (ws q))
    (hinj : ∀ a ∈ L, ∀ b ∈ L, hash a = hash b → a = b) : L = {q} := by
  ext m
  simp only [Finset.mem_singleton]
  refine ⟨fun hm => ?_, fun e => e ▸ hq⟩
  by_contra hmq
  have hnot : ¬ md (r := r) ws hash m q := hs m hm q hq
  simp only [md, mayDestroy, not_and, not_or, not_not, not_lt] at hnot
  obtain ⟨hqm, hle⟩ := hnot (hall m hm)
  have hne : hash m ≠ hash q := fun e => hmq (hinj m hm q hq e)
  have hlt : hash m < hash q := lt_of_le_of_ne hle hne
  exact hs q hq m hm ⟨hqm, Or.inr hlt⟩

end Hub

/-! ## A device -/

section Device

variable {α : Type*} [DecidableEq α] (S : Drop.Setting α)

open Drop

/-- One drop that is possible now, of a write the device stores. -/
inductive DropStep : Dev α → Dev α → Prop
  | replaced (d : Dev α) (x y : α) (hx : x ∈ d.T) (hcur : S.src x = d.cur) (hy : y ∈ d.T)
      (hey : elig S d y) (hyx : S.r y x) (hown : S.own x → S.own y) :
      DropStep d ⟨d.U, d.T.erase x, d.cur, d.L⟩
  | past (d : Dev α) (x : α) (hx : x ∈ d.T) (hne : ¬ elig S d x) :
      DropStep d ⟨d.U, d.T.erase x, d.cur, d.L⟩

/-- Every possible drop is a step of `Drop`, so its invariant and theorems hold along the way. -/
theorem dropStep_step {d d' : Dev α} (h : DropStep S d d') : Step S d d' := by
  cases h with
  | replaced x y _ hcur hy hey hyx hown => exact Step.dropReplaced d x y hcur hy hey hyx hown
  | past x _ hne => exact Step.dropPast d x hne

/-- Nothing more can be dropped. -/
def done (d : Dev α) : Prop := ∀ d', ¬ DropStep S d d'

/-- **Dropping stops**: from any device, drops reach one where none is possible. -/
theorem drops_terminate (d : Dev α) :
    ∃ d', Relation.ReflTransGen (DropStep S) d d' ∧ done S d' := by
  induction h : d.T.card using Nat.strong_induction_on generalizing d with
  | _ n ih =>
    by_cases hd : done S d
    · exact ⟨d, Relation.ReflTransGen.refl, hd⟩
    · simp only [done, not_forall, not_not] at hd
      obtain ⟨d1, hstep⟩ := hd
      have hlt : d1.T.card < n := by
        rw [← h]
        cases hstep with
        | replaced x _ hx => exact Finset.card_erase_lt_of_mem hx
        | past x hx => exact Finset.card_erase_lt_of_mem hx
      obtain ⟨d', hpath, hdone⟩ := ih _ hlt d1 rfl
      exact ⟨d', Relation.ReflTransGen.head hstep hpath, hdone⟩

/-- **What is left** where dropping has stopped: every write a device stores is eligible, and
is in its state, or is an own write of the current epoch that only other authors' writes
replaced (kept for §11.6 until the next key change), or was carried in through the current
inventory (kept until the next one is fixed, §9.3). -/
theorem residue {d : Dev α} (h : Inv S d) (hd : done S d) :
    ∀ x ∈ d.T, elig S d x ∧
      (x ∈ state S d d.T ∨
        (S.own x ∧ S.src x = d.cur ∧ ∀ y ∈ d.T, elig S d y → S.r y x → ¬ S.own y) ∨
        (S.src x < d.cur ∧ x ∈ d.L)) := by
  intro x hx
  have he : elig S d x := by
    by_contra hne
    exact hd _ (DropStep.past d x hx hne)
  refine ⟨he, ?_⟩
  by_cases hk : x ∈ state S d d.T
  · exact Or.inl hk
  · right
    by_cases hc : S.src x = d.cur
    · left
      have hown : S.own x := by
        by_contra hno
        unfold state at hk
        rw [mem_kept] at hk
        simp only [Finset.mem_filter, not_and, not_forall, not_not, exists_prop] at hk
        obtain ⟨y, ⟨hy, hey⟩, hyx⟩ := hk ⟨hx, he⟩
        exact hd _ (DropStep.replaced d x y hx hc hy hey hyx (fun ho => absurd ho hno))
      refine ⟨hown, hc, fun y hy hey hyx hoy => ?_⟩
      exact hd _ (DropStep.replaced d x y hx hc hy hey hyx (fun _ => hoy))
    · right
      rcases he with he | hL
      · exact absurd he hc
      · exact ⟨h.listed x hL, hL⟩

/-- Its state is all there: dropping never removes a write of the state. -/
theorem state_held (d : Dev α) : state S d d.T ⊆ d.T := fun _ hx =>
  (Finset.mem_filter.1 (kept_subset _ hx)).1

end Device

end Tandem.Progress
