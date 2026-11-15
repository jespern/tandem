import TandemProofs.Kept

/-!
# Dropping replaced writes before the next inventory (GC.md §2, question G2)

§9.3 keeps a replaced write until the next epoch's inventory is fixed, because a key change can
make it current again. `GC.md` §2 proposes a narrower rule: a device may drop a write `x` of the
current epoch at once when a write `y` it holds, eligible in the current epoch, supersedes it; if
`x` is this installation's own write, `y` must be one of its own too.

This module shows that a device that drops under that rule, and under §9.3's own rule (writes no
longer eligible), holds the same state as one that drops nothing, after any history of receiving
writes, dropping and key changes (`same_state`), and makes the same decisions about signing its own
writes again at a key change (`resign_same`), provided §11.6 does not sign again an own write that a
later own write replaced (`resigns`).

The setting is abstract: supersession is any strict partial order that never points from an
earlier epoch to a later one (`Supersession`'s `sup_strict` and `sup_epoch_le` show Tandem's is
one). What the key change needs of the protocol is stated as the hypotheses of `Step.move`:

* the inventory lists only writes eligible in the epoch it replaces (§8.3, §11.6 step 1);
* every listed write of that epoch is in the new channel when the device moves over: the device
  that fixed the inventory held it and uploaded it before announcing (§11.6 step 3);
* a device holds no write of an epoch before it moves into that epoch (`Step.receive`): such a
  write waits until its epoch counts (§8.3, §9.4).
-/

set_option linter.unusedSectionVars false

namespace Tandem.Drop

open Classical

variable {α : Type*} [DecidableEq α]

/-- A kept set is unchanged by dropping writes that something received supersedes. -/
theorem kept_of_dropped {r : α → α → Prop} [DecidableRel r] (hr : IsStrict r) {U T : Finset α}
    (hTU : T ⊆ U) (hdrop : ∀ x ∈ U, x ∉ T → ∃ y ∈ U, r y x) : kept r T = kept r U := by
  have hk : kept r U ⊆ T := fun k hk => by
    by_contra hkT
    obtain ⟨y, hy, hyk⟩ := hdrop k (kept_subset _ hk) hkT
    exact (mem_kept.1 hk).2 y hy hyk
  ext z
  constructor
  · intro hz
    rw [mem_kept] at hz ⊢
    refine ⟨hTU hz.1, fun y hy hyz => ?_⟩
    obtain ⟨k, hk', hkz⟩ := superseded_by_kept hr hy hyz
    exact hz.2 k (hk hk') hkz
  · intro hz
    have hzT := hk hz
    rw [mem_kept] at hz ⊢
    exact ⟨hzT, fun y hy => hz.2 y (hTU hy)⟩

/-- What the model needs of writes: supersession `r w x` ("`w` supersedes `x`"), a strict partial
order that never points to a later epoch; the epoch each write was written in; and which writes
are this installation's own. -/
structure Setting (α : Type*) where
  r : α → α → Prop
  strict : IsStrict r
  src : α → ℕ
  epoch_le : ∀ {y x}, r y x → src x ≤ src y
  own : α → Prop

/-- A device: `U` is every write it has merged, `T` what it still stores; `cur` is its current
epoch and `L` that epoch's inventory. A device that never drops stores `U`. -/
structure Dev (α : Type*) where
  U : Finset α
  T : Finset α
  cur : ℕ
  L : Finset α

variable (S : Setting α)

/-- Eligible in the current epoch (§8.3): written in it, or listed by its inventory. -/
def elig (d : Dev α) (x : α) : Prop := S.src x = d.cur ∨ x ∈ d.L

/-- The state a device computes from a store: the kept eligible writes. -/
noncomputable def state (d : Dev α) (st : Finset α) : Finset α := kept S.r (st.filter (elig S d))

/-- What happens to a device. -/
inductive Step : Dev α → Dev α → Prop
  /-- Writes arrive; none of a later epoch than the current one (they wait for their epoch). -/
  | receive (d : Dev α) (B : Finset α) (hB : ∀ x ∈ B, S.src x ≤ d.cur) :
      Step d ⟨d.U ∪ B, d.T ∪ B, d.cur, d.L⟩
  /-- `GC.md` §2.2: a write of the current epoch that a held eligible write supersedes; an own
  write only when an own write supersedes it. -/
  | dropReplaced (d : Dev α) (x y : α) (hcur : S.src x = d.cur) (hy : y ∈ d.T)
      (hey : elig S d y) (hyx : S.r y x) (hown : S.own x → S.own y) :
      Step d ⟨d.U, d.T.erase x, d.cur, d.L⟩
  /-- §9.3: a write no longer eligible. -/
  | dropPast (d : Dev α) (x : α) (hne : ¬ elig S d x) :
      Step d ⟨d.U, d.T.erase x, d.cur, d.L⟩
  /-- A key change: the next epoch with inventory `L`, whose channel holds `C`. -/
  | move (d : Dev α) (L C : Finset α) (hL : ∀ x ∈ L, elig S d x)
      (hC : ∀ x ∈ C, S.src x ≤ d.cur + 1) (hup : ∀ x ∈ L, S.src x = d.cur → x ∈ C) :
      Step d ⟨d.U ∪ C, d.T ∪ C, d.cur + 1, L⟩

/-- What holds after any history. -/
structure Inv (d : Dev α) : Prop where
  sub : d.T ⊆ d.U
  past : ∀ x ∈ d.U, S.src x ≤ d.cur
  listed : ∀ x ∈ d.L, S.src x < d.cur
  dropped : ∀ x ∈ d.U, x ∉ d.T → ¬ elig S d x ∨
    (S.src x = d.cur ∧ ∃ y ∈ d.U, elig S d y ∧ S.r y x ∧ (S.own x → S.own y))

/-- A device before it has anything. -/
def start : Dev α := ⟨∅, ∅, 0, ∅⟩

theorem inv_start : Inv S (start : Dev α) :=
  ⟨by simp [start], by simp [start], by simp [start], by simp [start]⟩

theorem inv_step {d d' : Dev α} (h : Inv S d) (s : Step S d d') : Inv S d' := by
  cases s with
  | receive B hB =>
    refine ⟨Finset.union_subset_union h.sub (subset_refl _), fun x hx => ?_, h.listed,
      fun x hx hxT => ?_⟩
    · rcases Finset.mem_union.1 hx with hx | hx
      · exact h.past x hx
      · exact hB x hx
    · have hxB : x ∉ B := fun hb => hxT (Finset.mem_union_right _ hb)
      have hxU : x ∈ d.U := (Finset.mem_union.1 hx).resolve_right hxB
      rcases h.dropped x hxU (fun ht => hxT (Finset.mem_union_left _ ht)) with hn | ⟨hc, y, hy, he, hyx, ho⟩
      · exact Or.inl hn
      · exact Or.inr ⟨hc, y, Finset.mem_union_left _ hy, he, hyx, ho⟩
  | dropReplaced x y hcur hy hey hyx hown =>
    refine ⟨(Finset.erase_subset _ _).trans h.sub, h.past, h.listed, fun z hz hzT => ?_⟩
    by_cases hzx : z = x
    · subst hzx
      exact Or.inr ⟨hcur, y, h.sub hy, hey, hyx, hown⟩
    · exact h.dropped z hz (fun ht => hzT (Finset.mem_erase.2 ⟨hzx, ht⟩))
  | dropPast x hne =>
    refine ⟨(Finset.erase_subset _ _).trans h.sub, h.past, h.listed, fun z hz hzT => ?_⟩
    by_cases hzx : z = x
    · subst hzx
      exact Or.inl hne
    · exact h.dropped z hz (fun ht => hzT (Finset.mem_erase.2 ⟨hzx, ht⟩))
  | move L C hL hC hup =>
    refine ⟨Finset.union_subset_union h.sub (subset_refl _), fun x hx => ?_, fun x hx => ?_,
      fun z hz hzT => Or.inl ?_⟩
    · rcases Finset.mem_union.1 hx with hx | hx
      · exact Nat.le_succ_of_le (h.past x hx)
      · exact hC x hx
    · rcases hL x hx with hx' | hx'
      · show S.src x < d.cur + 1
        omega
      · exact Nat.lt_succ_of_lt (h.listed x hx')
    · have hzC : z ∉ C := fun hc => hzT (Finset.mem_union_right _ hc)
      have hzU : z ∈ d.U := (Finset.mem_union.1 hz).resolve_right hzC
      have hzT' : z ∉ d.T := fun ht => hzT (Finset.mem_union_left _ ht)
      have hle := h.past z hzU
      rintro (hs | hzL)
      · change S.src z = d.cur + 1 at hs
        omega
      · rcases h.dropped z hzU hzT' with hn | ⟨hc, _⟩
        · exact hn (hL z hzL)
        · exact hzC (hup z hzL hc)

/-- What holds of every device: the invariant, after any history from `start`. -/
theorem inv_reach {d : Dev α} (h : Relation.ReflTransGen (Step S) start d) : Inv S d := by
  induction h with
  | refl => exact inv_start S
  | tail _ s ih => exact inv_step S ih s

/-- **Dropping changes no state.** -/
theorem state_eq {d : Dev α} (h : Inv S d) : state S d d.T = state S d d.U := by
  unfold state
  apply kept_of_dropped S.strict (Finset.filter_subset_filter _ h.sub)
  intro x hx hxT
  rw [Finset.mem_filter] at hx
  have hxT' : x ∉ d.T := fun ht => hxT (Finset.mem_filter.2 ⟨ht, hx.2⟩)
  rcases h.dropped x hx.1 hxT' with hn | ⟨_, y, hy, he, hyx, _⟩
  · exact absurd hx.2 hn
  · exact ⟨y, Finset.mem_filter.2 ⟨hy, he⟩, hyx⟩

/-- **A device that drops under `GC.md` §2.2 and §9.3 holds, after any history of writes, drops
and key changes, the state of a device that drops nothing.** -/
theorem same_state {d : Dev α} (h : Relation.ReflTransGen (Step S) start d) :
    state S d d.T = state S d d.U :=
  state_eq S (inv_reach S h)

/-- An own write of the current epoch that the device no longer stores is replaced by an own write
it still stores. -/
theorem own_replaced_held {d : Dev α} (h : Inv S d) :
    ∀ x ∈ d.U, S.own x → S.src x = d.cur → x ∉ d.T → ∃ y ∈ d.T, S.own y ∧ S.r y x := by
  intro x
  induction hn : (d.U.filter (fun w => S.r w x)).card using Nat.strong_induction_on generalizing x with
  | _ n ih =>
    intro hxU hown hsrc hxT
    rcases h.dropped x hxU hxT with hne | ⟨_, y, hyU, _, hyx, hoy⟩
    · exact absurd (Or.inl hsrc) hne
    · have hoy := hoy hown
      by_cases hyT : y ∈ d.T
      · exact ⟨y, hyT, hoy, hyx⟩
      · have hsy : S.src y = d.cur :=
          le_antisymm (h.past y hyU) (hsrc ▸ S.epoch_le hyx)
        have hlt : (d.U.filter (fun w => S.r w y)).card < n := by
          rw [← hn]
          apply Finset.card_lt_card
          refine ⟨fun w hw => ?_, fun hsub => ?_⟩
          · rw [Finset.mem_filter] at hw ⊢
            exact ⟨hw.1, S.strict.trans hw.2 hyx⟩
          · have := hsub (Finset.mem_filter.2 ⟨hyU, hyx⟩)
            exact S.strict.irrefl y (Finset.mem_filter.1 this).2
        obtain ⟨z, hzT, hoz, hzy⟩ := ih _ hlt y rfl hyU hoy hsy hyT
        exact ⟨z, hzT, hoz, S.strict.trans hzy hyx⟩

/-- Signing again at a key change to inventory `L` (§11.6), from the writes a device stores: an own
write that the inventory doesn't list is signed again unless a listed write, or a later own write
the device stores, replaces it. -/
def resigns (L st : Finset α) (x : α) : Prop :=
  x ∈ st ∧ x ∉ L ∧ (¬ ∃ l ∈ L, S.r l x) ∧ ¬ ∃ y ∈ st, S.own y ∧ S.r y x

/-- **Dropping changes no decision to sign again**: for each own write of the epoch being
replaced, a device that drops decides as one that drops nothing. -/
theorem resign_same {d : Dev α} (h : Inv S d) (L : Finset α) {x : α} (hxU : x ∈ d.U)
    (hown : S.own x) (hsrc : S.src x = d.cur) : resigns S L d.T x ↔ resigns S L d.U x := by
  have held : ∀ y ∈ d.U, S.own y → S.r y x → ∃ z ∈ d.T, S.own z ∧ S.r z x := by
    intro y hyU hoy hyx
    by_cases hyT : y ∈ d.T
    · exact ⟨y, hyT, hoy, hyx⟩
    · have hsy : S.src y = d.cur := le_antisymm (h.past y hyU) (hsrc ▸ S.epoch_le hyx)
      obtain ⟨z, hzT, hoz, hzy⟩ := own_replaced_held S h y hyU hoy hsy hyT
      exact ⟨z, hzT, hoz, S.strict.trans hzy hyx⟩
  unfold resigns
  constructor
  · rintro ⟨_, hxL, hl, hy⟩
    refine ⟨hxU, hxL, hl, fun ⟨y, hyU, hoy, hyx⟩ => hy (held y hyU hoy hyx)⟩
  · rintro ⟨_, hxL, hl, hy⟩
    by_cases hxT : x ∈ d.T
    · exact ⟨hxT, hxL, hl, fun ⟨y, hyT, hoy, hyx⟩ => hy ⟨y, h.sub hyT, hoy, hyx⟩⟩
    · obtain ⟨z, hzT, hoz, hzx⟩ := own_replaced_held S h x hxU hown hsrc hxT
      exact absurd ⟨z, h.sub hzT, hoz, hzx⟩ hy

/-- The listed writes that can replace an own write of the epoch being replaced are in the new
channel, so a device that dropped them has them again when it decides. -/
theorem listed_replacers_arrive {d : Dev α} (h : Inv S d) {L C : Finset α}
    (hL : ∀ x ∈ L, elig S d x) (hup : ∀ x ∈ L, S.src x = d.cur → x ∈ C) {x l : α}
    (hsrc : S.src x = d.cur) (hl : l ∈ L) (hlx : S.r l x) : l ∈ C := by
  apply hup l hl
  have hge := hsrc ▸ S.epoch_le hlx
  rcases hL l hl with he | hlL
  · exact he
  · exact absurd hge (by have := h.listed l hlL; omega)

/-! ## Why both conditions are needed

Two writes of epoch 0: `1` supersedes `0`. -/

/-- Supersession on two writes: `1` supersedes `0`, both of epoch 0; `ownAll` says whose they are. -/
def two (ownAll : Fin 2 → Prop) : Setting (Fin 2) where
  r y x := y = 1 ∧ x = 0
  strict := ⟨fun a h => by rcases h with ⟨rfl, h⟩; exact absurd h (by decide),
    fun h1 h2 => by rcases h1 with ⟨_, rfl⟩; rcases h2 with ⟨h, _⟩; exact absurd h (by decide)⟩
  src _ := 0
  epoch_le _ := le_refl 0
  own := ownAll

/-- §11.6 as written today signs an own write again unless a *listed* write replaces it. -/
def resignsListed (L st : Finset α) (x : α) : Prop :=
  x ∈ st ∧ x ∉ L ∧ ¬ ∃ l ∈ L, S.r l x

/-- **Under §11.6 as written, dropping an own write changes what is signed again.** The device
drops its own `0`, which its own `1` replaced; neither is listed. A device that dropped nothing
signs `0` again (and `1`), and the two come back as a conflict (§11.6, last paragraph); the
device that dropped signs only `1`. `resigns` excludes `0` for both. -/
theorem listed_rule_differs :
    resignsListed (two (fun _ => True)) ∅ {0, 1} 0 ∧ ¬ resignsListed (two (fun _ => True)) ∅ {1} 0 ∧
    ¬ resigns (two (fun _ => True)) ∅ {0, 1} 0 := by
  refine ⟨⟨by simp, by simp, by simp⟩, fun h => by simpa using h.1, fun h => h.2.2.2 ?_⟩
  exact ⟨1, by simp, trivial, rfl, rfl⟩

/-- **Condition 3 is needed.** If the device dropped its own `0` because another author's `1`
replaced it, it would not sign `0` again, while a device that dropped nothing would: nothing listed
and no own write replaces `0`. -/
theorem own_condition_needed :
    resigns (two (fun x => x = 0)) ∅ {0, 1} 0 ∧ ¬ resigns (two (fun x => x = 0)) ∅ {1} 0 := by
  refine ⟨⟨by simp, by simp, by simp, ?_⟩, fun h => by simpa using h.1⟩
  rintro ⟨y, hy, hoy, hyx⟩
  rcases hyx with ⟨rfl, _⟩
  simp [two] at hoy

end Tandem.Drop
