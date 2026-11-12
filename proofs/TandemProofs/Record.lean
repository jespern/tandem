import TandemProofs.Supersession
import TandemProofs.VectorLimit
import Mathlib.Data.List.MinMax
import Mathlib.Data.List.Lex
import Mathlib.Order.Prod.Lex.Basic
import Mathlib.Data.Char

/-!
# Sets, tombstones and display (§9.2)
-/

namespace Tandem

/-! ## Add-wins observed-remove set -/

/-- The element is present iff some kept write for it is an `add`. -/
def present (K : Finset Write) (rec field elem : String) : Prop :=
  ∃ x ∈ K, x.op = .add ∧ x.record = rec ∧ x.field = field ∧ x.elem = elem

/-- An add that nothing held supersedes keeps its element in, whatever removes are around:
a concurrent add and remove leave the element present (add wins). -/
theorem add_wins {S : Finset Write} {a : Write} (ha : a ∈ S) (hop : a.op = .add)
    (hn : ∀ y ∈ S, ¬ sup y a) : present (state S) a.record a.field a.elem :=
  ⟨a, mem_kept.2 ⟨ha, hn⟩, hop, rfl, rfl, rfl⟩

/-- A concurrent add and remove, concretely: neither supersedes the other, so the add stays. -/
theorem add_remove_concurrent {a rm : Write} (hop : a.op = .add)
    (h1 : ¬ sup rm a) : present (state {a, rm}) a.record a.field a.elem := by
  refine add_wins (by simp) hop ?_
  intro y hy
  simp only [Finset.mem_insert, Finset.mem_singleton] at hy
  rcases hy with rfl | rfl
  · exact sup_irrefl _
  · exact h1

/-- A remove that saw an add (its context covers the add, and the add didn't see it)
supersedes it. -/
theorem remove_supersedes_seen_add {a rm : Write} (hs : rm.slot = a.slot)
    (he : rm.epoch = a.epoch) (hseen : includes rm a) (hnot : ¬ includes a rm) : sup rm a :=
  ⟨Or.inl hs, Or.inr ⟨he, hseen, hnot⟩⟩

/-- If every add of an element is superseded by something held, the element is absent. -/
theorem absent_of_all_adds_superseded {S : Finset Write} {rec field elem : String}
    (h : ∀ a ∈ S, a.op = .add → a.record = rec → a.field = field → a.elem = elem →
      ∃ y ∈ S, sup y a) : ¬ present (state S) rec field elem := by
  rintro ⟨x, hx, hop, hr, hf, he⟩
  obtain ⟨hxS, hn⟩ := mem_kept.1 hx
  obtain ⟨y, hy, hyx⟩ := h x hxS hop hr hf he
  exact hn y hy hyx

/-- A replayed old add stays superseded. -/
theorem replayed_add_stays_out {S : Finset Write} {a y : Write} (hy : y ∈ S) (hya : sup y a) :
    state (insert a S) = state S := state_replay hy hya

/-! ## Sticky tombstones -/

def deleted (K : Finset Write) (rec : String) : Prop := ∃ x ∈ K, x.op = .delete ∧ x.record = rec

/-- Only a delete supersedes a delete. -/
theorem sup_delete {w x : Write} (h : sup w x) (hx : x.op = .delete) :
    w.op = .delete ∧ w.record = x.record := Write.governs_delete h.1 hx

/-- Once any valid delete is held, every merge that contains it is deleted (§9.2, §12.3). -/
theorem deleted_sticky {S T : Finset Write} {rec : String} (h : deleted S rec) :
    deleted (state (S ∪ T)) rec := by
  obtain ⟨d, hd, hop, hrec⟩ := h
  rcases kept_or_superseded sup_strict (S ∪ T) (Finset.mem_union_left T hd) with hk | ⟨k, hk, hkd⟩
  · exact ⟨d, hk, hop, hrec⟩
  · obtain ⟨hkop, hkrec⟩ := sup_delete hkd hop
    exact ⟨k, hk, hkop, hkrec.trans hrec⟩

/-- A kept delete stays kept until a delete that includes it arrives: whatever else is
merged in, it is kept or superseded by a kept delete of its record. -/
theorem kept_delete_stays {S : Finset Write} {d : Write} (hd : d ∈ S) (hop : d.op = .delete) :
    d ∈ state S ∨ ∃ k ∈ state S, k.op = .delete ∧ k.record = d.record ∧ sup k d := by
  rcases kept_or_superseded sup_strict S hd with hk | ⟨k, hk, hkd⟩
  · exact Or.inl hk
  · obtain ⟨h1, h2⟩ := sup_delete hkd hop
    exact Or.inr ⟨k, hk, h1, h2, hkd⟩

/-! ### A delete supersedes what it includes (decided 2026-11-12)

A delete supersedes any write to its record that it includes, in any field and of any op,
under the same vector test as everything else. So a delete built over the record's kept
writes shrinks the record to its tombstone: one small message per deleted record (§12.3). -/

/-- If every other write of the held set is superseded by some held delete, the state is
exactly the kept deletes: the kept set of the deletes alone. -/
theorem state_only_deletes {S : Finset Write}
    (h : ∀ x ∈ S, x.op ≠ .delete → ∃ d ∈ S, d.op = .delete ∧ sup d x) :
    state S = state (S.filter (·.op = .delete)) := by
  ext x
  simp only [state, mem_kept, Finset.mem_filter]
  constructor
  · rintro ⟨hx, hn⟩
    by_cases hop : x.op = .delete
    · exact ⟨⟨hx, hop⟩, fun y hy => hn y hy.1⟩
    · obtain ⟨d, hd, -, hdx⟩ := h x hx hop
      exact absurd hdx (hn d hd)
  · rintro ⟨⟨hx, hop⟩, hn⟩
    refine ⟨hx, fun y hy hyx => hn y ⟨hy, (sup_delete hyx hop).1⟩ hyx⟩

/-- **One tombstone per deleted record** (§12.3). `K` is a record's state. A delete `d` of the
current epoch `e`, its context and base built over all of `K` in every field (§9.2), with a dot
none of `K` has seen, supersedes all of it: after merging, `d` is all that is kept. -/
theorem delete_built_over_state {S : Finset Write} {d : Write} {e : Nat}
    (hop : d.op = .delete) (hrec : ∀ k ∈ state S, k.record = d.record)
    (he : d.epoch = e) (hle : ∀ k ∈ state S, k.epoch ≤ e)
    (hctx : d.ctx = buildCtx (state S).toList e) (hbase : d.base = buildBase (state S).toList e)
    (hfresh : ∀ k ∈ state S, k.epoch = e → ¬ includes k d) :
    state (insert d S) = {d} := by
  have hall : ∀ k ∈ (state S).toList, sup d k :=
    build_supersedes he (fun k hk => Or.inr ⟨hop, (hrec k (Finset.mem_toList.1 hk)).symm⟩)
      (fun k hk => hle k (Finset.mem_toList.1 hk)) hctx hbase
      (fun k hk => hfresh k (Finset.mem_toList.1 hk))
  have hnot : ∀ k ∈ state S, ¬ sup k d := by
    rintro k hk ⟨-, ⟨hlt, -⟩ | ⟨hke, hi, -⟩⟩
    · exact absurd (hle k hk) (by omega)
    · exact hfresh k hk (hke.trans he) hi
  have h1 : state {d} = {d} := by
    ext x
    simp only [state, mem_kept, Finset.mem_singleton]
    exact ⟨fun h => h.1, fun h => ⟨h, fun y hy => hy ▸ h ▸ sup_irrefl _⟩⟩
  rw [Finset.insert_eq, state_union, h1]
  ext x
  show x ∈ kept sup ({d} ∪ state S) ↔ _
  simp only [mem_kept, Finset.mem_union, Finset.mem_singleton]
  constructor
  · rintro ⟨hx | hx, hn⟩
    · exact hx
    · exact absurd (hall x (Finset.mem_toList.2 hx)) (hn d (Or.inl rfl))
  · rintro rfl
    refine ⟨Or.inl rfl, fun y hy hyx => ?_⟩
    rcases hy with rfl | hy
    · exact sup_irrefl _ hyx
    · exact hnot y hy hyx

/-! ### Records without deletes are unaffected

For records that hold no delete, `sup` is the register-only rule it was before the change, so
display, sets and add-wins read exactly as they did. -/

/-- Supersession keyed by `Write.slot` alone: the rule before 2026-11-12. -/
def supSlot (w x : Write) : Prop :=
  w.slot = x.slot ∧
    ((x.epoch < w.epoch ∧ vle x.reach w.base ∧ vle x.base w.base) ∨
     (w.epoch = x.epoch ∧ includes w x ∧ ¬ includes x w))

instance : DecidableRel supSlot := fun _ _ => by unfold supSlot; infer_instance

theorem sup_iff_supSlot {w x : Write} (hw : w.op ≠ .delete) : sup w x ↔ supSlot w x := by
  unfold sup supSlot
  rw [Write.governs_iff_slot hw]

/-- The state of a record that holds no delete is what the old rule gave, whatever deletes
other records hold. -/
theorem state_of_undeleted {S : Finset Write} {rec : String}
    (h : ∀ y ∈ S, y.record = rec → y.op ≠ .delete) :
    (state S).filter (·.record = rec) = (kept supSlot S).filter (·.record = rec) := by
  ext x
  simp only [state, mem_kept, Finset.mem_filter]
  constructor
  · rintro ⟨⟨hx, hn⟩, rfl⟩
    refine ⟨⟨hx, fun y hy hyx => ?_⟩, rfl⟩
    have hyr : y.record = x.record := by
      rw [← Write.slot_record y, ← Write.slot_record x, hyx.1]
    exact hn y hy ((sup_iff_supSlot (h y hy hyr)).2 hyx)
  · rintro ⟨⟨hx, hn⟩, rfl⟩
    refine ⟨⟨hx, fun y hy hyx => ?_⟩, rfl⟩
    have hyr := Write.governs_record hyx.1
    exact hn y hy ((sup_iff_supSlot (h y hy hyr)).1 hyx)

/-! ### The register of a delete, and why it needed saying

§9.2 said a record with any valid delete is deleted "whatever else happens to it", and §9.3
defined a record's state as the writes nothing supersedes, with supersession between writes
"to the same field". The design didn't say which field a delete names (`sigInput` carries a
field name for every op) or whether an `assign` can supersede a `delete` (finding F1). If
supersession looks only at (record, field), as `model/owner_model.py` does, a write that shares
the delete's field and covers it drops the tombstone from the state: -/

/-- Supersession keyed by (record, field) only, ignoring the op. -/
def supByField (w x : Write) : Prop :=
  w.record = x.record ∧ w.field = x.field ∧
    ((x.epoch < w.epoch ∧ vle x.reach w.base ∧ vle x.base w.base) ∨
     (w.epoch = x.epoch ∧ includes w x ∧ ¬ includes x w))

instance : DecidableRel supByField := fun _ _ => by unfold supByField; infer_instance

private def g0 : Stmt := []
private def del : Write where
  id := "d"
  author := "A"
  ref := g0
  record := "r"
  field := ""
  op := .delete
  elem := ""
  value := ""
  dev := "a"
  hlc := 1
  ctx := []
  base := []
private def over : Write where
  id := "o"
  author := "M"
  ref := g0
  record := "r"
  field := ""
  op := .assign
  elem := ""
  value := "x"
  dev := "m"
  hlc := 2
  ctx := [("a", 1)]
  base := []

/-- The counterexample: keyed by field, the assignment supersedes the delete. -/
theorem tombstone_dropped_if_keyed_by_field : supByField over del := by decide

/-- Under the decided rule, a write that covers a delete doesn't supersede it. -/
theorem tombstone_kept_with_slot : ¬ sup over del := by decide

/-- A note written before the delete was made, in another field. -/
private def seen : Write := { over with id := "s", field := "note", hlc := 1, dev := "m", ctx := [] }
/-- The delete, built over `seen`. -/
private def delSeen : Write := { del with id := "d2", hlc := 2, ctx := [("m", 1)] }
/-- An edit made offline, concurrently with the delete. -/
private def concurrent : Write := { over with id := "c", field := "title", ctx := [("m", 1)] }

/-- The delete supersedes the write it saw, though it is in another field. -/
theorem delete_supersedes_seen_other_field : sup delSeen seen ∧ seen.field ≠ delSeen.field := by
  decide

/-- **A concurrent write survives under the tombstone**: neither supersedes the other, the
record is deleted, and the edit is kept (the app doesn't show it). -/
theorem concurrent_write_survives_delete :
    state {delSeen, seen, concurrent} = {delSeen, concurrent} ∧
      deleted (state {delSeen, seen, concurrent}) "r" := by
  have hs : state {delSeen, seen, concurrent} = {delSeen, concurrent} := by decide
  refine ⟨hs, ?_⟩
  rw [hs]
  exact ⟨delSeen, by simp, rfl, rfl⟩

/-! ## Display (§9.2) -/

/-- Siblings are shown greatest first by `(epoch, hlc, author, deviceID, writeID)`. -/
def key (w : Write) : Lex (ℕ × Lex (ℕ × Lex (List Char × Lex (List Char × List Char)))) :=
  toLex (w.epoch, toLex (w.hlc, toLex (w.author.toList, toLex (w.dev.toList, w.id.toList))))

theorem key_inj {x y : Write} (hid : x.id = y.id → x = y) (h : key x = key y) : x = y := by
  apply hid
  simp only [key, toLex_inj, Prod.mk.injEq] at h
  exact String.toList_inj.1 h.2.2.2.2

/-- The displayed value: the greatest sibling. -/
def display (l : List Write) : Option Write := l.argmax key

/-- **Display is independent of arrival order**: any two enumerations of the same siblings
show the same value, given that a write ID names one write. -/
theorem display_perm {l l' : List Write} (h : l.Perm l')
    (hid : ∀ x ∈ l, ∀ y ∈ l, x.id = y.id → x = y) : display l = display l' := by
  unfold display
  cases hx : l.argmax key with
  | none =>
    rw [List.argmax_eq_none] at hx
    subst hx
    have := h.nil_eq
    subst this
    rfl
  | some x =>
    cases hy : l'.argmax key with
    | none =>
      rw [List.argmax_eq_none] at hy
      subst hy
      have := List.Perm.eq_nil h
      subst this
      simp at hx
    | some y =>
      have hxl : x ∈ l := List.argmax_mem hx
      have hyl' : y ∈ l' := List.argmax_mem hy
      have hyl : y ∈ l := h.symm.subset hyl'
      have h1 : key x ≤ key y := List.le_of_mem_argmax (h.subset hxl) hy
      have h2 : key y ≤ key x := List.le_of_mem_argmax hyl hx
      exact congrArg some (key_inj (hid x hxl y hyl) (le_antisymm h1 h2))

/-- The shown siblings: at most 16, greatest first. Presentation only; the state keeps all. -/
def shownLimit : Nat := 16

/-! ### Why the limit must not apply to the state

If a device kept only its top `k` siblings, its result would depend on arrival order.
With `k = 1`: `a` supersedes `x`, `b` is concurrent with both, and `key a < key b < key x`.
Receiving `a, b, x` keeps `b` then meets `x`, which nothing it still holds supersedes;
receiving `x, a, b` lets `a` remove `x` before `b` arrives. -/

def keptL (l : List Write) : List Write := l.filter fun x => decide (∀ y ∈ l, ¬ sup y x)

/-- A device that keeps only its top sibling. -/
def receiveTop1 (l : List Write) : Option Write :=
  l.foldl (fun st w => (keptL (st.toList ++ [w])).argmax key) none

private def wx : Write where
  id := "x"
  author := "U"
  ref := g0
  record := "r"
  field := "f"
  op := .assign
  elem := ""
  value := "x"
  dev := "d2"
  hlc := 9
  ctx := []
  base := []
private def wa : Write := { wx with id := "a", value := "a", dev := "d1", hlc := 1, ctx := [("d2", 9)] }
private def wb : Write := { wx with id := "b", value := "b", dev := "d3", hlc := 5 }

theorem truncated_state_depends_on_order :
    receiveTop1 [wa, wb, wx] = some wx ∧ receiveTop1 [wx, wa, wb] = some wb := by
  decide

end Tandem
