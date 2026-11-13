import TandemProofs.Kept

/-!
# Garbage collection never loses a write (§10.7)

A message holds some writes. `m` is **dominated** by `n` if every write in `m` is in `n` or is
superseded by a write in `n`. The frozen rule: a device may destroy `m` on account of `n` if
`n` dominates `m` and, should `m` also dominate `n`, `m` has the greater `H(envelope)`.
Byte-identical copies (same hash) are removed only by the device that made the envelope.

We prove, for any strict supersession order:

* the old rule (tie-break only for identical write sets) lets two devices destroy both of
  `{x, y}` and `{y}` and lose `y`;
* the frozen rule makes "may destroy on account of" a strict partial order;
* under **any** interleaving of destroys, each justified by a message that was on the hub
  when the device looked (stale views allowed), every write of every message stays held
  or superseded by a message still on the hub.

The one assumption, for byte-identical copies: the device that made an envelope never
removes a copy on account of a copy it has itself already removed (it sees its own destroys).
-/

set_option linter.unusedSectionVars false

namespace Tandem

variable {α : Type*} [DecidableEq α] {r : α → α → Prop}

/-- `n` holds `w` or a write that supersedes it. -/
def coversW (r : α → α → Prop) (n : Finset α) (w : α) : Prop := w ∈ n ∨ ∃ x ∈ n, r x w

def dominated (r : α → α → Prop) (m n : Finset α) : Prop := ∀ w ∈ m, coversW r n w

instance [DecidableRel r] (n : Finset α) (w : α) : Decidable (coversW r n w) := by
  unfold coversW; infer_instance

instance [DecidableRel r] (m n : Finset α) : Decidable (dominated r m n) := by
  unfold dominated; infer_instance

theorem dominated_refl (m : Finset α) : dominated r m m := fun _ h => Or.inl h

theorem coversW_trans (hr : IsStrict r) {n p : Finset α} {w : α} (hw : coversW r n w)
    (hnp : dominated r n p) : coversW r p w := by
  rcases hw with hw | ⟨x, hx, hxw⟩
  · exact hnp w hw
  · rcases hnp x hx with h | ⟨y, hy, hyx⟩
    · exact Or.inr ⟨x, h, hxw⟩
    · exact Or.inr ⟨y, hy, hr.trans hyx hxw⟩

theorem dominated_trans (hr : IsStrict r) {m n p : Finset α} (h1 : dominated r m n)
    (h2 : dominated r n p) : dominated r m p := fun w hw => coversW_trans hr (h1 w hw) h2

/-- The frozen rule: `m` (hash `hm`) may be destroyed on account of `n` (hash `hn`). -/
def mayDestroy (r : α → α → Prop) (m n : Finset α) (hm hn : ℕ) : Prop :=
  dominated r m n ∧ (¬ dominated r n m ∨ hn < hm)

instance [DecidableRel r] (m n : Finset α) (hm hn : ℕ) : Decidable (mayDestroy r m n hm hn) := by
  unfold mayDestroy; infer_instance

/-- The rule before the testbed finding: tie-break only for identical write sets. -/
def mayDestroyOld (r : α → α → Prop) (m n : Finset α) (hm hn : ℕ) : Prop :=
  dominated r m n ∧ (m ≠ n ∨ hn < hm)

/-! ## The old rule loses a write -/

/-- `1` supersedes `0`. -/
def r10 (a b : ℕ) : Prop := a = 1 ∧ b = 0

/-- With `m = {x, y}` and `n = {y}` (here `x = 0`, `y = 1`), each may be destroyed on account
of the other under the old rule, whatever the hashes; afterwards no message holds `y`. -/
theorem old_rule_loses_y (hm hn : ℕ) :
    mayDestroyOld r10 {0, 1} {1} hm hn ∧ mayDestroyOld r10 {1} {0, 1} hn hm := by
  refine ⟨⟨?_, Or.inl (by decide)⟩, ⟨?_, Or.inl (by decide)⟩⟩
  · intro w hw
    simp only [Finset.mem_insert, Finset.mem_singleton] at hw
    rcases hw with rfl | rfl
    · exact Or.inr ⟨1, by simp, rfl, rfl⟩
    · exact Or.inl (by simp)
  · intro w hw
    simp only [Finset.mem_singleton] at hw
    subst hw
    exact Or.inl (by simp)

/-- Under the frozen rule, at most one of the two may go (unless their hashes are equal,
in which case neither may: that is the byte-identical case). -/
theorem new_rule_keeps_one {m n : Finset α} {hm hn : ℕ}
    (h1 : mayDestroy r m n hm hn) (h2 : mayDestroy r n m hn hm) : False := by
  rcases h1 with ⟨d1, a1 | a1⟩ <;> rcases h2 with ⟨d2, a2 | a2⟩
  · exact a1 d2
  · exact a1 d2
  · exact a2 d1
  · exact lt_asymm a1 a2

/-! ## The frozen rule is a strict partial order -/

section Messages

variable {M : Type*} (ws : M → Finset α) (hash : M → ℕ)

/-- `md m n`: message `m` may be destroyed on account of message `n`. -/
def md (m n : M) : Prop := mayDestroy r (ws m) (ws n) (hash m) (hash n)

theorem md_strict (hr : IsStrict r) : IsStrict (md (r := r) ws hash) := by
  refine ⟨fun m h => ?_, fun {a b c} hab hbc => ?_⟩
  · rcases h.2 with h | h
    · exact h (dominated_refl _)
    · exact lt_irrefl _ h
  · refine ⟨dominated_trans hr hab.1 hbc.1, ?_⟩
    by_cases hca : dominated r (ws c) (ws a)
    · -- a, b, c all dominate each other: the hashes decide, and decrease strictly.
      have hcb : dominated r (ws c) (ws b) := dominated_trans hr hca hab.1
      have hba : dominated r (ws b) (ws a) := dominated_trans hr hbc.1 hca
      have h1 : hash b < hash a := hab.2.resolve_left (not_not.2 hba)
      have h2 : hash c < hash b := hbc.2.resolve_left (not_not.2 hcb)
      exact Or.inr (h2.trans h1)
    · exact Or.inl hca

end Messages

/-! ## Safety under any interleaving -/

/-- How a message was destroyed: when, on account of which message, and whether as a
byte-identical copy removed by its creator. -/
structure Destroy (M : Type*) where
  time : ℕ
  just : M
  copy : Bool

section Trace

variable {M : Type*} [DecidableEq M] (U : Finset M) (ws : M → Finset α) (hash : M → ℕ)
  (d : M → Option (Destroy M))

/-- Every destroy of a message in `U` is justified by a message of `U` (one that was on the hub
when the destroying device looked; it may be gone since):
* an ordinary destroy follows the frozen rule;
* a copy is removed only on account of another copy with the same envelope hash, which its
  creator hasn't removed before (it sees its own destroys). -/
def Justified : Prop :=
  ∀ m ∈ U, ∀ e : Destroy M, d m = some e → e.just ∈ U ∧
    (if e.copy then e.just ≠ m ∧ hash e.just = hash m ∧
        ∀ e' : Destroy M, d e.just = some e' → e'.copy = true → e.time < e'.time
     else md (r := r) ws hash m e.just)

/-- Still on the hub. -/
def live (q : M) : Prop := q ∈ U ∧ d q = none

open Classical in
/-- The messages `m` may be destroyed on account of. -/
noncomputable def above (m : M) : ℕ := (U.filter fun n => md (r := r) ws hash m n).card

open Classical in
/-- For a copy destroyed at time `t`: one plus the number of copies destroyed after `t`. -/
noncomputable def laterCopies (m : M) : ℕ :=
  match d m with
  | some e => if e.copy then 1 + (U.filter fun n => ∃ e', d n = some e' ∧ e'.copy = true ∧
      e.time < e'.time).card else 0
  | none => 0

theorem above_lt (hr : IsStrict r) {m n : M} (hn : n ∈ U) (h : md (r := r) ws hash m n) :
    above (r := r) U ws hash n < above (r := r) U ws hash m := by
  classical
  unfold above
  apply Finset.card_lt_card
  refine ⟨fun p hp => ?_, fun hsub => ?_⟩
  · simp only [Finset.mem_filter] at hp ⊢
    exact ⟨hp.1, (md_strict ws hash hr).trans h hp.2⟩
  · have := hsub (Finset.mem_filter.2 ⟨hn, h⟩)
    exact (md_strict ws hash hr).irrefl n (Finset.mem_filter.1 this).2

theorem above_eq_of_copy {m n : M} (hh : hash n = hash m) (hw : ws n = ws m) :
    above (r := r) U ws hash n = above (r := r) U ws hash m := by
  classical
  unfold above md
  rw [hh, hw]

/-- **GC safety.** Every message that was ever on the hub is dominated by a message that is
still there. Hence every write it held is still held, or superseded by a write still held. -/
theorem gc_safe (hr : IsStrict r) (hcopy : ∀ m n, hash m = hash n → ws m = ws n)
    (hj : Justified (r := r) U ws hash d) :
    ∀ m ∈ U, ∃ q, live U d q ∧ dominated r (ws m) (ws q) := by
  classical
  -- strong induction on (above m, laterCopies m), lexicographically
  suffices h : ∀ a k, ∀ m ∈ U, above (r := r) U ws hash m = a → laterCopies U d m = k →
      ∃ q, live U d q ∧ dominated r (ws m) (ws q) from
    fun m hm => h _ _ m hm rfl rfl
  intro a
  induction a using Nat.strong_induction_on with
  | _ a iha =>
  intro k
  induction k using Nat.strong_induction_on with
  | _ k ihk =>
  intro m hm ha hk
  cases hdm : d m with
  | none => exact ⟨m, ⟨hm, hdm⟩, dominated_refl _⟩
  | some e =>
    obtain ⟨hjU, hjr⟩ := hj m hm e hdm
    cases hc : e.copy with
    | false =>
      rw [hc] at hjr
      have hmd : md (r := r) ws hash m e.just := by simpa using hjr
      have hlt := above_lt U ws hash hr hjU hmd
      obtain ⟨q, hq, hdq⟩ := iha _ (ha ▸ hlt) _ e.just hjU rfl rfl
      exact ⟨q, hq, dominated_trans hr hmd.1 hdq⟩
    | true =>
      rw [hc] at hjr
      simp only [ite_true] at hjr
      obtain ⟨-, hh, hlater⟩ := hjr
      have hw : ws e.just = ws m := hcopy _ _ hh
      have habove := above_eq_of_copy (r := r) U ws hash hh hw
      -- the justifying copy: live, ordinarily destroyed, or a copy destroyed later
      have hkm : laterCopies U d m = 1 + (U.filter fun n => ∃ e', d n = some e' ∧
          e'.copy = true ∧ e.time < e'.time).card := by
        simp [laterCopies, hdm, hc]
      cases hdj : d e.just with
      | none => exact ⟨e.just, ⟨hjU, hdj⟩, hw ▸ dominated_refl _⟩
      | some e' =>
        have hk' : laterCopies U d e.just < k := by
          rw [← hk, hkm]
          cases hc' : e'.copy with
          | false => simp [laterCopies, hdj, hc']; omega
          | true =>
            have ht := hlater e' hdj hc'
            simp only [laterCopies, hdj, hc', ite_true]
            apply Nat.add_lt_add_left
            apply Finset.card_lt_card
            refine ⟨fun p hp => ?_, fun hsub => ?_⟩
            · simp only [Finset.mem_filter] at hp ⊢
              obtain ⟨e'', h1, h2, h3⟩ := hp.2
              exact ⟨hp.1, e'', h1, h2, ht.trans h3⟩
            · have := hsub (Finset.mem_filter.2 ⟨hjU, e', hdj, hc', ht⟩)
              obtain ⟨e'', h1, -, h3⟩ := (Finset.mem_filter.1 this).2
              rw [hdj] at h1
              cases h1
              exact lt_irrefl _ h3
        obtain ⟨q, hq, hdq⟩ := ihk _ hk' e.just hjU (habove.trans ha) rfl
        exact ⟨q, hq, hw ▸ hdq⟩

/-- The same, write by write. -/
theorem gc_no_write_lost (hr : IsStrict r) (hcopy : ∀ m n, hash m = hash n → ws m = ws n)
    (hj : Justified (r := r) U ws hash d) {m : M} (hm : m ∈ U) {w : α} (hw : w ∈ ws m) :
    ∃ q, live U d q ∧ coversW r (ws q) w := by
  obtain ⟨q, hq, hd⟩ := gc_safe U ws hash d hr hcopy hj m hm
  exact ⟨q, hq, hd w hw⟩

end Trace

/-- The old rule really loses `y`: a trace in which two devices each destroy one of
`{x, y}` and `{y}` on account of the other is justified under the old rule, and afterwards
nothing is live. -/
theorem old_rule_trace_loses :
    let ws : Bool → Finset ℕ := fun b => if b then {0, 1} else {1}
    let d : Bool → Option (Destroy Bool) := fun b => some ⟨0, !b, false⟩
    (∀ m, ∀ e, d m = some e → mayDestroyOld r10 (ws m) (ws e.just) 7 7) ∧
      ¬ ∃ q, live Finset.univ d q := by
  refine ⟨?_, ?_⟩
  · intro m e he
    simp only [Option.some.injEq] at he
    subst he
    cases m
    · exact (old_rule_loses_y 7 7).2
    · exact (old_rule_loses_y 7 7).1
  · rintro ⟨q, -, hq⟩
    simp at hq

end Tandem
