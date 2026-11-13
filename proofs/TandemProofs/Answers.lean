import TandemProofs.Roster

/-!
# Admissions and answers (§11.2, §11.3)

Sharing is the owner adding someone to the roster from the hub's directory. There are no list
invites. Each time a user is added is an **admission**, named by the statement that adds them:
the first that lists them after one that doesn't, or the genesis. The user's answers are an
add-only set in home of (admission, `a` | `n` | `l`), and the state of a membership is computed
from the roster and that set, never stored (`mstate`, as `owner_model.state`).

* The state depends only on the set of answers, not their order or repeats
  (`mstate_of_same_set`).
* `n` or `l` beats `a`, whichever arrived first, and stays (`no_beats_add`, `left_sticky`).
* Nothing is active unless the user added it (`active_needs_add`); a user is anything but
  removed only while the head lists them (`removed_iff_unlisted`).
* An admission doesn't change while the user stays listed (`admission_stable`), so their
  answers keep applying.
* Adding someone again is a new admission (`readmission_is_new`): answers to the old one don't
  count (`readmitted_is_asked_again`), and a grant from before the removal offers nothing
  (`stale_grant_offers_nothing`).
-/

namespace Tandem

def Node.lists (x : Node) (u : String) : Bool := x.body.uids.contains u

/-- The admission of `u` in force at a statement (nearest first), or `none` if it doesn't list
them. -/
def admission (u : String) : Stmt → Option String
  | [] => none
  | x :: rest =>
    if x.lists u then
      match rest with
      | p :: _ => if p.lists u then admission u rest else some x.id
      | [] => some x.id
    else none

inductive Kind where
  | add | no | leave
deriving DecidableEq, Repr

/-- The add-only `answers` set in home. -/
abbrev Answers := List (String × Kind)

inductive MState where
  | removed | waiting | offered | active | left
deriving DecidableEq, Repr

/-- §11.2. `held`: the `at` statements of the grants the user's devices hold; a grant belongs
to the admission in force at its `at`. -/
def mstate (h : Stmt) (u : String) (A : Answers) (held : List Stmt) : MState :=
  match admission u h with
  | none => .removed
  | some a =>
    if A.any (fun p => p.1 == a && p.2 != .add) then .left
    else if A.contains (a, .add) then .active
    else if held.any (fun s => admission u s == some a) then .offered
    else .waiting

theorem admission_mem {u : String} : ∀ {L : Stmt} {a : String}, admission u L = some a →
    ∃ x ∈ L, x.id = a ∧ x.lists u = true
  | [], _, h => by simp [admission] at h
  | x :: rest, a, h => by
    unfold admission at h
    split_ifs at h with hx
    rcases rest with _ | ⟨p, ps⟩
    · simp at h; exact ⟨x, by simp, h, hx⟩
    · simp only at h
      split_ifs at h with hp
      · obtain ⟨y, hy, rfl, hl⟩ := admission_mem h
        exact ⟨y, List.mem_cons_of_mem _ hy, rfl, hl⟩
      · simp at h; exact ⟨x, by simp, h, hx⟩

theorem admission_isSome {u : String} : ∀ {L : Stmt} {x : Node}, L.head? = some x →
    x.lists u = true → (admission u L).isSome
  | [], _, h, _ => by simp at h
  | [y], x, h, hx => by simp at h; subst h; simp [admission, hx]
  | y :: p :: ps, x, h, hx => by
    simp at h; subst h
    simp only [admission, hx, ite_true]
    split_ifs with hp
    · have h := admission_isSome (L := p :: ps) rfl hp
      simpa only [admission, hp, ite_true] using h
    · rfl

theorem admission_eq_none_iff {u : String} {L : Stmt} :
    admission u L = none ↔ ∀ x ∈ L.head?, x.lists u = false := by
  constructor
  · intro h x hx
    cases hl : x.lists u
    · rfl
    · have := admission_isSome hx hl; simp [h] at this
  · intro h
    rcases L with _ | ⟨x, rest⟩
    · rfl
    · simp [admission, h x (by simp)]

/-- A user is anything but removed exactly while the head lists them. -/
theorem removed_iff_unlisted {h : Stmt} {u : String} {A : Answers} {held : List Stmt} :
    mstate h u A held = .removed ↔ ∀ x ∈ h.head?, x.lists u = false := by
  rw [← admission_eq_none_iff]
  unfold mstate
  rcases admission u h with _ | a
  · simp
  · simp only [reduceCtorEq, iff_false]
    split_ifs <;> simp

/-- **The state depends only on the set of answers.** -/
theorem mstate_of_same_set {h : Stmt} {u : String} {A B : Answers} {held : List Stmt}
    (hAB : ∀ p, p ∈ A ↔ p ∈ B) : mstate h u A held = mstate h u B held := by
  unfold mstate
  have e1 : ∀ a : String, A.any (fun p => p.1 == a && p.2 != .add) =
      B.any (fun p => p.1 == a && p.2 != .add) := by
    intro a; apply Bool.eq_iff_iff.2; simp only [List.any_eq_true]
    exact ⟨fun ⟨p, hp, h⟩ => ⟨p, (hAB p).1 hp, h⟩, fun ⟨p, hp, h⟩ => ⟨p, (hAB p).2 hp, h⟩⟩
  have e2 : ∀ a : String, A.contains (a, .add) = B.contains (a, .add) := by
    intro a; apply Bool.eq_iff_iff.2; simp [hAB]
  simp only [e1, e2]

/-- **`n` or `l` beats `a`**, whichever arrived first. -/
theorem no_beats_add {h : Stmt} {u a : String} {A : Answers} {held : List Stmt} {k : Kind}
    (ha : admission u h = some a) (hk : k ≠ .add) (hA : (a, k) ∈ A) :
    mstate h u A held = .left := by
  have h1 : A.any (fun p => p.1 == a && p.2 != .add) = true :=
    List.any_eq_true.2 ⟨(a, k), hA, by simp [hk]⟩
  simp [mstate, ha, h1]

/-- **Left stays left** as answers are added: the set only grows. -/
theorem left_sticky {h : Stmt} {u : String} {A B : Answers} {held held' : List Stmt}
    (hAB : A ⊆ B) (hl : mstate h u A held = .left) : mstate h u B held' = .left := by
  unfold mstate at hl ⊢
  rcases ha : admission u h with _ | a
  · simp [ha] at hl
  · simp only [ha] at hl ⊢
    split_ifs at hl with h1
    · obtain ⟨p, hp, hp'⟩ := List.any_eq_true.1 h1
      have h2 : B.any (fun p => p.1 == a && p.2 != .add) = true := List.any_eq_true.2 ⟨p, hAB hp, hp'⟩
      simp [h2]

/-- **Nothing is active unless the user added it**, for the admission in force. -/
theorem active_needs_add {h : Stmt} {u : String} {A : Answers} {held : List Stmt}
    (hs : mstate h u A held = .active) : ∃ a, admission u h = some a ∧ (a, .add) ∈ A := by
  unfold mstate at hs
  rcases ha : admission u h with _ | a
  · simp [ha] at hs
  · simp only [ha] at hs
    split_ifs at hs with h1 h2
    exact ⟨a, rfl, by simpa using h2⟩

/-- **An admission stays as long as the user stays listed**: extending the roster with
statements that all list them doesn't change it. -/
theorem admission_stable {u : String} {y : Node} {ys : Stmt} (hy : y.lists u = true) :
    ∀ {pre : Stmt}, (∀ x ∈ pre, x.lists u = true) →
      admission u (pre ++ y :: ys) = admission u (y :: ys)
  | [], _ => rfl
  | [x], hpre => by
    have hx := hpre x (by simp)
    simp [admission, hx, hy]
  | x :: p :: ps, hpre => by
    have hx := hpre x (by simp)
    have hp := hpre p (by simp)
    have ih := admission_stable hy (ys := ys) (pre := p :: ps) (fun y hy => hpre y (List.mem_cons_of_mem _ hy))
    simp only [List.cons_append, admission, hx, ite_true, hp] at ih ⊢
    exact ih

/-- Once the roster has stopped listing the user somewhere in `pre`, any admission at the head
is a statement of `pre`. -/
theorem admission_in_prefix {u a : String} {L : Stmt} :
    ∀ {pre : Stmt}, (∃ y ∈ pre, y.lists u = false) → admission u (pre ++ L) = some a →
      ∃ x ∈ pre, x.id = a
  | [], ⟨_, hy, _⟩, _ => by simp at hy
  | x :: pre, ⟨y, hy, hyl⟩, h => by
    simp only [List.cons_append, admission] at h
    split_ifs at h with hx
    rcases List.mem_cons.1 hy with rfl | hy'
    · simp [hx] at hyl
    rcases pre with _ | ⟨p, ps⟩
    · simp at hy'
    · simp only [List.cons_append] at h
      split_ifs at h with hp
      · obtain ⟨z, hz, hza⟩ := admission_in_prefix ⟨y, hy', hyl⟩ (by simpa using h)
        exact ⟨z, List.mem_cons_of_mem _ hz, hza⟩
      · simp at h; exact ⟨x, by simp, h⟩

/-- **Adding someone again is a new admission.** Statement IDs on one line are distinct, as
their `n` differ and IDs are collision-free. -/
theorem readmission_is_new {u : String} {pre L : Stmt} (hnd : ((pre ++ L).map (·.id)).Nodup)
    (hgone : ∃ y ∈ pre, y.lists u = false) {a b : String} (hb : admission u L = some b)
    (ha : admission u (pre ++ L) = some a) : a ≠ b := by
  obtain ⟨x, hx, rfl⟩ := admission_in_prefix hgone ha
  obtain ⟨y, hy, rfl, -⟩ := admission_mem hb
  rw [List.map_append, List.nodup_append] at hnd
  exact hnd.2.2 _ (List.mem_map_of_mem hx) _ (List.mem_map_of_mem hy)

/-- Answers to an earlier admission don't count: someone added again is asked again. -/
theorem readmitted_is_asked_again {u : String} {pre L : Stmt}
    (hnd : ((pre ++ L).map (·.id)).Nodup) (hgone : ∃ y ∈ pre, y.lists u = false)
    {b : String} (hb : admission u L = some b) {A : Answers} (hA : ∀ p ∈ A, p.1 = b)
    {held : List Stmt} (hlisted : ∀ x ∈ (pre ++ L).head?, x.lists u = true) :
    mstate (pre ++ L) u A held = .waiting ∨ mstate (pre ++ L) u A held = .offered := by
  obtain ⟨a, ha⟩ : ∃ a, admission u (pre ++ L) = some a := by
    cases h : admission u (pre ++ L) with
    | some a => exact ⟨a, rfl⟩
    | none =>
      obtain ⟨x, hx⟩ : ∃ x, (pre ++ L).head? = some x := by
        obtain ⟨y, hy, -⟩ := hgone
        cases hh : pre with
        | nil => simp [hh] at hy
        | cons z zs => exact ⟨z, by simp⟩
      have := admission_eq_none_iff.1 h x hx
      simp [hlisted x hx] at this
  have hne := readmission_is_new hnd hgone hb ha
  have h1 : A.any (fun p => p.1 == a && p.2 != .add) = false := by
    simp only [List.any_eq_false, Bool.and_eq_true, beq_iff_eq, not_and]
    intro p hp hpa
    exact (hne (hpa ▸ hA p hp)).elim
  have h2 : A.contains (a, .add) = false := by
    cases hc : A.contains (a, .add)
    · rfl
    · exact (hne (hA _ (by simpa using hc))).elim
  simp only [mstate, ha, h1, h2]
  split_ifs <;> simp_all

/-- **A grant from before the removal offers nothing**: it belongs to the old admission. -/
theorem stale_grant_offers_nothing {u : String} {pre L : Stmt}
    (hnd : ((pre ++ L).map (·.id)).Nodup) (hgone : ∃ y ∈ pre, y.lists u = false)
    {b : String} (hb : admission u L = some b) {a : String} (ha : admission u (pre ++ L) = some a) :
    mstate (pre ++ L) u [] [L] = .waiting := by
  have hne := readmission_is_new hnd hgone hb ha
  simp [mstate, ha, hb, Ne.symm hne]

end Tandem
