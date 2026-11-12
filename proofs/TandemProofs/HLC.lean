import Mathlib.Order.Basic
import Mathlib.Tactic.Linarith
import Mathlib.Tactic.Positivity

/-!
# Hybrid logical clocks (§9.1)

A new write's HLC is `(max(now, last.millis), counter)`, the counter incremented if the
millis didn't advance; if the counter would pass 65 535, millis advances by one and the
counter restarts at 0. A device's HLCs are strictly increasing, whatever its wall clock does.
-/

namespace Tandem

structure HLC where
  millis : Nat
  counter : Nat
deriving DecidableEq, Repr

def HLC.lt (a b : HLC) : Prop := a.millis < b.millis ∨ (a.millis = b.millis ∧ a.counter < b.counter)

def counterMax : Nat := 65535

def nextHLC (last : HLC) (now : Nat) : HLC :=
  if last.millis < now then ⟨now, 0⟩
  else if last.counter < counterMax then ⟨last.millis, last.counter + 1⟩
  else ⟨last.millis + 1, 0⟩

theorem nextHLC_gt (last : HLC) (now : Nat) : HLC.lt last (nextHLC last now) := by
  unfold nextHLC HLC.lt
  split_ifs with h1 h2
  · exact Or.inl h1
  · exact Or.inr ⟨rfl, Nat.lt_succ_self _⟩
  · exact Or.inl (Nat.lt_succ_self _)

theorem nextHLC_counter (last : HLC) (now : Nat) :
    (nextHLC last now).counter ≤ counterMax := by
  unfold nextHLC
  split_ifs with h1 h2
  · simp [counterMax]
  · exact h2
  · simp [counterMax]

/-- The millis never fall behind the wall clock or the last value. -/
theorem nextHLC_millis_ge (last : HLC) (now : Nat) :
    now ≤ (nextHLC last now).millis ∧ last.millis ≤ (nextHLC last now).millis := by
  unfold nextHLC
  split_ifs with h1 h2 <;> simp <;> omega

/-- The 64-bit wire order `millis · 2^16 + counter` (§5.5) agrees with `HLC.lt`. -/
def HLC.toNat (h : HLC) : Nat := h.millis * 65536 + h.counter

theorem HLC.toNat_lt {a b : HLC} (ha : a.counter ≤ counterMax)
    (h : HLC.lt a b) : a.toNat < b.toNat := by
  unfold HLC.toNat
  unfold counterMax at ha
  rcases h with h | ⟨h1, h2⟩
  · have : (a.millis + 1) * 65536 ≤ b.millis * 65536 := Nat.mul_le_mul_right _ h
    nlinarith
  · rw [h1]; omega

/-- The clock values of a run of writes, one per wall-clock reading. -/
def run (start : HLC) : List Nat → List HLC
  | [] => []
  | now :: rest => let h := nextHLC start now; h :: run h rest

/-- **Strictly increasing**: every value of a run is above the previous one, counter overflow
included, whatever the wall clock reads (backwards jumps too). -/
theorem run_chain (start : HLC) (nows : List Nat) :
    List.IsChain HLC.lt (start :: run start nows) := by
  induction nows generalizing start with
  | nil => simp [run]
  | cons now rest ih =>
    simp only [run]
    exact List.IsChain.cons_cons (nextHLC_gt start now) (ih _)

theorem run_counter (start : HLC) (nows : List Nat) :
    ∀ x ∈ run start nows, x.counter ≤ counterMax := by
  induction nows generalizing start with
  | nil => simp [run]
  | cons now rest ih =>
    intro x hx
    simp only [run, List.mem_cons] at hx
    rcases hx with rfl | hx
    · exact nextHLC_counter start now
    · exact ih _ x hx

end Tandem
