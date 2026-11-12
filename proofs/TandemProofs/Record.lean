import TandemProofs.Supersession
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
  ⟨hs, Or.inr ⟨he, hseen, hnot⟩⟩

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

theorem slot_delete {w x : Write} (h : w.slot = x.slot) (hx : x.op = .delete) :
    w.op = .delete ∧ w.record = x.record := by
  unfold Write.slot at h
  rw [hx] at h
  cases hw : w.op <;> rw [hw] at h <;> simp_all

/-- Once any valid delete is held, every merge that contains it is deleted (§9.2, §12.3). -/
theorem deleted_sticky {S T : Finset Write} {rec : String} (h : deleted S rec) :
    deleted (state (S ∪ T)) rec := by
  obtain ⟨d, hd, hop, hrec⟩ := h
  rcases kept_or_superseded sup_strict (S ∪ T) (Finset.mem_union_left T hd) with hk | ⟨k, hk, hkd⟩
  · exact ⟨d, hk, hop, hrec⟩
  · obtain ⟨hkop, hkrec⟩ := slot_delete hkd.1 hop
    exact ⟨k, hk, hkop, hkrec.trans hrec⟩

/-! ### Why the register of a delete needs saying

§9.2 says a record with any valid delete is deleted "whatever else happens to it", and §9.3
defines a record's state as the writes nothing supersedes, with supersession between writes
"to the same field". The design doesn't say which field a delete names (`sigInput` carries a
field name for every op) or whether an `assign` can supersede a `delete`. If supersession
looks only at (record, field), as `model/owner_model.py` does, a write that shares the
delete's field and covers it drops the tombstone from the state: -/

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

/-- Keyed by `Write.slot` (a delete is its own register), it doesn't. -/
theorem tombstone_kept_with_slot : ¬ sup over del := by decide

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
