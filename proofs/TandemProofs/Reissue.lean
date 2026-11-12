import TandemProofs.Eligibility
import TandemProofs.Supersession

/-!
# Signing again (§11.6)

A device that learns of a new epoch signs again any write of its own user that the new
inventory doesn't list and the new state doesn't supersede: same record, field, operation,
element, value and dot; an empty context; as base the join of the old base and `reach(old)`;
as `ref` the **first statement of the new epoch that lists the author as owner or writer**.

* Every device of the user gets the same write (`reissue_deterministic`), whichever later
  statement of that epoch its roster has reached.
* The re-signed write never supersedes a write of its own epoch (`reissued_not_over_new`),
  so it can't override what was written there.
* It keeps superseding what the original superseded (`reissued_keeps_replacing`), and
  supersedes an older write only if the original's vectors already covered it
  (`reissued_only_what_was_seen`).

Our plan said "a re-signed write never supersedes a write the snapshot kept". That is not the
design's rule and is false: a late write `w` that replaced a snapshot write `k` in the old
epoch should, and does, replace `k` again once signed in the new epoch (`late_write_replaces_
snapshot_again`). What the design does say holds: a write the new state supersedes isn't
signed again (`reissueIn`), and the order between two re-signed writes is lost
(`reissued_pair_conflicts`).
-/

namespace Tandem

/-- A statement's line, root first: the statement and all its ancestors. -/
def lineOf : Stmt → List Stmt
  | [] => []
  | x :: t => lineOf t ++ [x :: t]

theorem lineOf_suffix {s t : Stmt} (h : t <:+ s) : ∃ more, lineOf s = lineOf t ++ more := by
  induction s with
  | nil =>
    rw [List.suffix_nil] at h; subst h; exact ⟨[], by simp⟩
  | cons x s ih =>
    rcases List.suffix_cons_iff.1 h with rfl | h'
    · exact ⟨[], by simp⟩
    · obtain ⟨more, hm⟩ := ih h'
      exact ⟨more ++ [x :: s], by simp [lineOf, hm]⟩

def canWriteAt (s : Stmt) (u : String) : Bool := ((s.roleOf u).map Role.canWrite).getD false

/-- The first statement of `head`'s epoch that lets `u` write. -/
def firstAuthorizing (head : Stmt) (u : String) : Option Stmt :=
  (lineOf head).find? fun i => epochStart i == epochStart head && canWriteAt i u

/-- Sign `w` again under the roster whose head is `head`. `none`: refused (no statement lets
the author write, or the base would exceed the vector limit); the app keeps the work. -/
def reissue (head : Stmt) (w : Write) : Option Write :=
  if canWriteAt head w.author then
    match firstAuthorizing head w.author with
    | some ref =>
      let base := w.base ++ w.reach
      if size base ≤ vectorLimit then some { w with id := "", ref := ref, ctx := [], base := base }
      else none
    | none => none
  else none

/-- What a device does with its own late write: nothing if the new state already holds it
or supersedes it, else the re-signed write (if any). -/
def reissueIn (head : Stmt) (state : List Write) (w : Write) : Option Write :=
  match reissue head w with
  | none => none
  | some n => if state.any (fun x => x == w || { x with id := "" } == n || decide (sup x w))
      then none else some n

theorem reissue_spec {head : Stmt} {w n : Write} (h : reissue head w = some n) :
    ∃ ref, firstAuthorizing head w.author = some ref ∧
      n = { w with id := "", ref := ref, ctx := [], base := w.base ++ w.reach } ∧
      size (w.base ++ w.reach) ≤ vectorLimit := by
  unfold reissue at h
  split_ifs at h with h1
  cases hf : firstAuthorizing head w.author with
  | none => rw [hf] at h; simp at h
  | some ref =>
    rw [hf] at h
    simp only at h
    split_ifs at h with h2
    exact ⟨ref, rfl, (Option.some.inj h).symm, h2⟩

/-- **Every device of the user signs the same bytes.** Two devices whose rosters have reached
`h₁` and a later statement `h₂` of the same epoch produce the same write. -/
theorem reissue_deterministic {h₁ h₂ : Stmt} {w a b : Write} (hsuf : h₁ <:+ h₂)
    (he : epochStart h₁ = epochStart h₂) (ha : reissue h₁ w = some a)
    (hb : reissue h₂ w = some b) : a = b := by
  obtain ⟨r₁, hr₁, rfl, -⟩ := reissue_spec ha
  obtain ⟨r₂, hr₂, rfl, -⟩ := reissue_spec hb
  suffices r₁ = r₂ by subst this; rfl
  obtain ⟨more, hm⟩ := lineOf_suffix hsuf
  unfold firstAuthorizing at hr₁ hr₂
  rw [hm, List.find?_append, ← he, hr₁] at hr₂
  simpa using hr₂

theorem reissue_fields {head : Stmt} {w n : Write} (h : reissue head w = some n) :
    n.ctx = [] ∧ n.base = w.base ++ w.reach ∧ n.slot = w.slot ∧ n.dev = w.dev ∧
      n.hlc = w.hlc ∧ n.value = w.value ∧ n.author = w.author := by
  obtain ⟨ref, -, rfl, -⟩ := reissue_spec h
  exact ⟨rfl, rfl, rfl, rfl, rfl, rfl, rfl⟩

/-- A write with an empty context never supersedes a write of its own epoch. -/
theorem empty_ctx_not_over_same_epoch {n x : Write} (hc : n.ctx = []) (he : n.epoch = x.epoch) :
    ¬ sup n x := by
  rintro ⟨-, ⟨h, -⟩ | ⟨-, ⟨hi, -⟩, -⟩⟩
  · rw [he] at h; exact lt_irrefl _ h
  · rw [hc] at hi
    obtain ⟨q, hq, -⟩ := hi (x.dev, x.hlc) (List.mem_append_right _ (List.mem_singleton_self _))
    simp at hq

theorem reissued_not_over_new {head : Stmt} {w n x : Write} (h : reissue head w = some n)
    (he : n.epoch = x.epoch) : ¬ sup n x :=
  empty_ctx_not_over_same_epoch (reissue_fields h).1 he

/-- The re-signed write keeps replacing whatever the original replaced. -/
theorem reissued_keeps_replacing {head : Stmt} {w n x : Write} (h : reissue head w = some n)
    (hwx : sup w x) (hlater : w.epoch < n.epoch) : sup n x := by
  obtain ⟨-, hb, hs, -⟩ := reissue_fields h
  refine ⟨hs.trans hwx.1, Or.inl ⟨lt_of_le_of_lt (sup_epoch_le hwx) hlater, ?_, ?_⟩⟩
  · rw [hb]
    rcases hwx.2 with ⟨-, h1, -⟩ | ⟨-, ⟨h1, -⟩, -⟩
    · exact vle_trans h1 (vle_append_left _ _)
    · exact vle_trans h1 (vle_trans (vle_ctx_reach w) (vle_append_right _ _))
  · rw [hb]
    rcases hwx.2 with ⟨-, -, h2⟩ | ⟨-, ⟨-, h2⟩, -⟩
    · exact vle_trans h2 (vle_append_left _ _)
    · exact vle_trans h2 (vle_append_left _ _)

/-- The re-signed write supersedes an older write only if the original's base and reach
already covered it. -/
theorem reissued_only_what_was_seen {head : Stmt} {w n x : Write} (h : reissue head w = some n)
    (hnx : sup n x) : vle x.reach (w.base ++ w.reach) ∧ vle x.base (w.base ++ w.reach) := by
  obtain ⟨hc, hb, -⟩ := reissue_fields h
  rcases hnx.2 with ⟨-, h1, h2⟩ | ⟨he, -⟩
  · exact ⟨hb ▸ h1, hb ▸ h2⟩
  · exact absurd hnx (empty_ctx_not_over_same_epoch hc he)

/-- A late write `w` that replaced a snapshot write `k` replaces it again once re-signed:
the plan's "never supersedes a write the snapshot kept" was wrong, and the design never
claimed it. -/
theorem late_write_replaces_snapshot_again {head : Stmt} {w n k : Write}
    (h : reissue head w = some n) (hwk : sup w k) (hlater : w.epoch < n.epoch) : sup n k :=
  reissued_keeps_replacing h hwk hlater

/-- Two re-signed writes of one epoch never supersede each other: if one had replaced the
other, the replaced value comes back as a conflict until the next write (§11.6). -/
theorem reissued_pair_conflicts {h₁ h₂ : Stmt} {a b a' b' : Write} (ha : reissue h₁ a = some a')
    (hb : reissue h₂ b = some b') (he : a'.epoch = b'.epoch) : ¬ sup a' b' ∧ ¬ sup b' a' :=
  ⟨reissued_not_over_new ha he, reissued_not_over_new hb he.symm⟩

/-- Signing again never exceeds the vector limit. -/
theorem reissue_within_limit {head : Stmt} {w n : Write} (h : reissue head w = some n) :
    size n.ctx ≤ vectorLimit ∧ size n.base ≤ vectorLimit := by
  obtain ⟨ref, -, rfl, hs⟩ := reissue_spec h
  exact ⟨by simp [size, devices, vectorLimit], hs⟩

end Tandem
