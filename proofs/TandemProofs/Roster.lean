import TandemProofs.Model

/-!
# The roster chain and dead collections (§8.2, §9.4)

A device holds a set `S` of statements (each with its ancestry, which its ID fixes). It follows
the statements in a valid owner-signed line from the expected genesis `g`, and the collection
is dead once it holds two different owner-signed, counted successors of one followed statement.

* A statement's validity depends only on its parent (`chainOK_cons`): nothing a device learns
  later can change it.
* What a device follows, and whether the collection is dead, are functions of the **set** of
  statements held: arrival order never matters, and neither do duplicates.
* Both only grow with the set: once dead, dead in every superset (`dead_mono`).
* Every device that holds the two conflicting statements and their parent's line agrees the
  collection is dead (`dead_of_held`).
* An unannounced statement that starts an epoch is a draft: holding drafts changes nothing
  (`drafts_change_nothing`).
-/

namespace Tandem

variable (g : Node)

/-- Validity is a function of the statement and its parent's line, nothing else. -/
theorem chainOK_cons (x p : Node) (rest : List Node) :
    chainOK g (x :: p :: rest) = (chainOK g (p :: rest) && x.signer == g.signer &&
      stepOK p (commitsOf (p :: rest)) x && counted p x) := rfl

theorem chainOK_tail {x : Node} {t : Stmt} (h : chainOK g (x :: t) = true) (ht : t ≠ []) :
    chainOK g t = true := by
  match t, ht with
  | p :: rest, _ =>
    simp only [chainOK_cons, Bool.and_eq_true] at h
    exact h.1.1.1

theorem chainOK_suffix {s t : Stmt} (h : chainOK g s = true) (hst : t <:+ s) (ht : t ≠ []) :
    chainOK g t = true := by
  induction s with
  | nil => simp [chainOK] at h
  | cons x s ih =>
    rcases List.suffix_cons_iff.1 hst with rfl | h'
    · exact h
    · have hs : s ≠ [] := by rintro rfl; simp at h'; exact ht h'
      exact ih (chainOK_tail g h hs) h'

theorem chainOK_counted {x p : Node} {rest : List Node} (h : chainOK g (x :: p :: rest) = true) :
    counted p x = true := by
  simp only [chainOK_cons, Bool.and_eq_true] at h
  exact h.2

/-! ## Monotonicity: more statements never undo anything -/

variable {g}

theorem ancestorsIn_mono {S T : List Stmt} (hST : S ⊆ T) {s : Stmt} (h : ancestorsIn S s = true) :
    ancestorsIn T s = true := by
  induction s with
  | nil => rfl
  | cons x t ih =>
    match t, h, ih with
    | [], _, _ => rfl
    | y :: t', h, ih =>
      simp only [ancestorsIn, Bool.and_eq_true, List.contains_iff_mem] at h ⊢
      exact ⟨hST h.1, ih h.2⟩

theorem mem_accepted {S : List Stmt} {s : Stmt} :
    s ∈ accepted g S ↔ s ∈ S ∧ chainOK g s = true ∧ ancestorsIn S s = true := by
  simp [accepted]

theorem accepted_mono {S T : List Stmt} (hST : S ⊆ T) {s : Stmt} (h : s ∈ accepted g S) :
    s ∈ accepted g T := by
  rw [mem_accepted] at h ⊢
  exact ⟨hST h.1, h.2.1, ancestorsIn_mono hST h.2.2⟩

theorem isAuthenticated_mono {S T : List Stmt} (hST : S ⊆ T) {s : Stmt}
    (h : isAuthenticated g S s = true) : isAuthenticated g T s = true := by
  match s, h with
  | x :: p :: rest, h =>
    simp only [isAuthenticated, Bool.and_eq_true, List.contains_iff_mem] at h ⊢
    exact ⟨⟨⟨accepted_mono hST h.1.1.1, h.1.1.2⟩, h.1.2⟩, h.2⟩

theorem mem_authenticated {S : List Stmt} {s : Stmt} :
    s ∈ authenticated g S ↔ s ∈ S ∧ isAuthenticated g S s = true := by
  simp [authenticated]

theorem dead_iff {S : List Stmt} :
    dead g S = true ↔ ∃ a ∈ S, ∃ b ∈ S, isAuthenticated g S a = true ∧
      isAuthenticated g S b = true ∧ a ≠ b ∧ a.tail = b.tail := by
  simp only [dead, List.any_eq_true, mem_authenticated, Bool.and_eq_true, bne_iff_ne, ne_eq,
    beq_iff_eq]
  constructor
  · rintro ⟨a, ⟨ha, haa⟩, b, ⟨hb, hba⟩, hne, ht⟩
    exact ⟨a, ha, b, hb, haa, hba, hne, ht⟩
  · rintro ⟨a, ha, b, hb, haa, hba, hne, ht⟩
    exact ⟨a, ⟨ha, haa⟩, b, ⟨hb, hba⟩, hne, ht⟩

/-- **Once dead, always dead**: dead in every superset of what was held. -/
theorem dead_mono {S T : List Stmt} (hST : S ⊆ T) (h : dead g S = true) : dead g T = true := by
  rw [dead_iff] at h ⊢
  obtain ⟨a, ha, b, hb, haa, hba, hne, ht⟩ := h
  exact ⟨a, hST ha, b, hST hb, isAuthenticated_mono hST haa, isAuthenticated_mono hST hba, hne, ht⟩

/-- **A function of the set held.** Two devices holding the same statements, in whatever order
and with whatever duplicates, follow the same statements and agree on whether it is dead. -/
theorem same_set {S T : List Stmt} (h1 : S ⊆ T) (h2 : T ⊆ S) :
    (∀ s, s ∈ accepted g S ↔ s ∈ accepted g T) ∧ dead g S = dead g T := by
  refine ⟨fun s => ⟨accepted_mono h1, accepted_mono h2⟩, ?_⟩
  cases hS : dead g S <;> cases hT : dead g T
  · rfl
  · rw [dead_mono h2 hT] at hS; exact hS.symm
  · rw [dead_mono h1 hS] at hT; exact hT
  · rfl

theorem same_set_perm {S T : List Stmt} (h : S.Perm T) :
    (∀ s, s ∈ accepted g S ↔ s ∈ accepted g T) ∧ dead g S = dead g T :=
  same_set h.subset h.symm.subset

/-- **Every device that holds both conflicting statements agrees.** If a device holds two
different owner-signed, counted successors `a`, `b` of a statement `p`, and `p`'s line is
valid and held, the collection is dead for that device. -/
theorem dead_of_held {S : List Stmt} {a b : Stmt} {p : Node} {rest : List Node}
    (ha : a ∈ S) (hb : b ∈ S) (hne : a ≠ b)
    (hpa : isAuthenticated g S a = true) (hpb : isAuthenticated g S b = true)
    (hta : a.tail = p :: rest) (htb : b.tail = p :: rest) : dead g S = true :=
  dead_iff.2 ⟨a, ha, b, hb, hpa, hpb, hne, hta.trans htb.symm⟩

/-- What makes a successor authenticated depends only on the successor and its parent's line
being followed: so whoever holds the pair and the line agrees. -/
theorem isAuthenticated_iff {S : List Stmt} {x p : Node} {rest : List Node} :
    isAuthenticated g S (x :: p :: rest) = true ↔
      (p :: rest) ∈ accepted g S ∧ x.signer = g.signer ∧ x.parent = some p.id ∧
      counted p x = true := by
  simp [isAuthenticated, and_assoc]

/-- Two devices holding the same conflicting pair whose parent each follows: both see it dead. -/
theorem conflict_agreed {S₁ S₂ : List Stmt} {x y p : Node} {rest : List Node} (hne : x ≠ y)
    (h₁ : [x :: p :: rest, y :: p :: rest] ⊆ S₁) (h₂ : [x :: p :: rest, y :: p :: rest] ⊆ S₂)
    (hp₁ : (p :: rest) ∈ accepted g S₁) (hp₂ : (p :: rest) ∈ accepted g S₂)
    (hx : x.signer = g.signer ∧ x.parent = some p.id ∧ counted p x = true)
    (hy : y.signer = g.signer ∧ y.parent = some p.id ∧ counted p y = true) :
    dead g S₁ = true ∧ dead g S₂ = true := by
  have hne' : (x :: p :: rest) ≠ (y :: p :: rest) := by simpa using hne
  constructor
  · exact dead_of_held (h₁ (by simp)) (h₁ (by simp)) hne'
      (isAuthenticated_iff.2 ⟨hp₁, hx⟩) (isAuthenticated_iff.2 ⟨hp₁, hy⟩) rfl rfl
  · exact dead_of_held (h₂ (by simp)) (h₂ (by simp)) hne'
      (isAuthenticated_iff.2 ⟨hp₂, hx⟩) (isAuthenticated_iff.2 ⟨hp₂, hy⟩) rfl rfl

/-! ## Drafts (§11.6) -/

/-- A statement that starts an epoch and that this device hasn't seen announced. -/
def isDraft : Stmt → Bool
  | x :: p :: _ => !counted p x
  | _ => false

theorem not_isDraft_of_chainOK {s t : Stmt} (h : chainOK g s = true) (hst : t <:+ s) :
    isDraft t = false := by
  match t, hst with
  | [], _ => rfl
  | [_], _ => rfl
  | x :: p :: rest, hst =>
    have := chainOK_counted g (chainOK_suffix g h hst (by simp))
    simp [isDraft, this]

theorem ancestorsIn_suffix {S : List Stmt} {s t : Stmt} (h : ancestorsIn S s = true)
    (hst : t <:+ s) (hts : t ≠ s) (ht : t ≠ []) : t ∈ S := by
  induction s with
  | nil => simp at hst; exact absurd hst ht
  | cons x s ih =>
    rcases List.suffix_cons_iff.1 hst with rfl | h'
    · exact absurd rfl hts
    · match s, h, ih, h' with
      | [], _, _, h' => simp at h'; exact absurd h' ht
      | y :: s', h, ih, h' =>
        simp only [ancestorsIn, Bool.and_eq_true, List.contains_iff_mem] at h
        by_cases he : t = y :: s'
        · exact he ▸ h.1
        · exact ih h.2 h' he

theorem ancestorsIn_of_suffixes {S : List Stmt} {s : Stmt}
    (h : ∀ t, t <:+ s → t ≠ s → t ≠ [] → t ∈ S) : ancestorsIn S s = true := by
  induction s with
  | nil => rfl
  | cons x s ih =>
    match s, h, ih with
    | [], _, _ => rfl
    | y :: s', h, ih =>
      simp only [ancestorsIn, Bool.and_eq_true, List.contains_iff_mem]
      refine ⟨h _ (List.suffix_cons _ _) (by simp) (by simp), ih fun t h1 h2 h3 => ?_⟩
      refine h t (h1.trans (List.suffix_cons _ _)) ?_ h3
      rintro rfl
      have := h1.length_le
      simp at this
      omega

/-- **Holding drafts changes nothing**: neither what is followed nor whether it is dead. -/
theorem drafts_change_nothing {S D : List Stmt} (hD : ∀ d ∈ D, isDraft d = true) :
    (∀ s, s ∈ accepted g (S ++ D) ↔ s ∈ accepted g S) ∧ dead g (S ++ D) = dead g S := by
  have hacc : ∀ s, s ∈ accepted g (S ++ D) ↔ s ∈ accepted g S := by
    intro s
    refine ⟨fun h => ?_, accepted_mono (List.subset_append_left S D)⟩
    rw [mem_accepted] at h ⊢
    obtain ⟨hs, hc, ha⟩ := h
    have keep : ∀ t, t <:+ s → t ∈ S ++ D → t ∈ S := by
      intro t hts htm
      rcases List.mem_append.1 htm with h | h
      · exact h
      · have := hD t h
        rw [not_isDraft_of_chainOK hc hts] at this
        exact absurd this (by simp)
    refine ⟨keep s List.suffix_rfl hs, hc, ancestorsIn_of_suffixes fun t h1 h2 h3 => ?_⟩
    exact keep t h1 (ancestorsIn_suffix ha h1 h2 h3)
  have hauth : ∀ s, s ∈ S ++ D → isAuthenticated g (S ++ D) s = true →
      s ∈ S ∧ isAuthenticated g S s = true := by
    intro s hs h
    match s, h, hs with
    | x :: p :: rest, h, hs =>
      rw [isAuthenticated_iff] at h ⊢
      refine ⟨?_, (hacc _).1 h.1, h.2⟩
      rcases List.mem_append.1 hs with h' | h'
      · exact h'
      · have := hD _ h'
        simp [isDraft, h.2.2.2] at this
  refine ⟨hacc, ?_⟩
  cases hS : dead g S
  · cases hSD : dead g (S ++ D)
    · rfl
    · rw [dead_iff] at hSD
      obtain ⟨a, ha, b, hb, haa, hba, hne, ht⟩ := hSD
      obtain ⟨ha', haa'⟩ := hauth a ha haa
      obtain ⟨hb', hba'⟩ := hauth b hb hba
      rw [dead_iff.2 ⟨a, ha', b, hb', haa', hba', hne, ht⟩] at hS
      exact absurd hS (by simp)
  · exact dead_mono (List.subset_append_left S D) hS

end Tandem
