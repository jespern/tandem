import Mathlib.Data.List.Nodup

/-!
# Which admin capsule is current (§6.7)

The hub's management seed lives on the hub in an admin capsule, sealed under the hub password.
Changing the password writes a new capsule that names, by ID, every capsule it supersedes: the
current ones and every capsule they name. Old capsules are then destroyed. Devices use any
capsule on the hub that no other capsule there names.

The hub holds any subset of the capsules ever written: some destroyed, some put back as copies
(a replay). Capsules are signed with `MKs`, so nobody without the management key adds one; a
copy is the capsule itself (`id` is its hash, collision-free).

* Whatever was destroyed and put back, a capsule superseded through any chain of changes is
  never current while a capsule above it is on the hub (`superseded_never_current`).
* The hub always has a current capsule, whatever was destroyed or put back, as long as it has
  one at all (`current_nonempty`).
* Naming only the capsules a change replaces directly would not do: a copy of a destroyed
  grandparent becomes current again (`parent_only_replay`).
* Two changes at the same moment are both current until the next names both
  (`concurrent_changes`).
-/

namespace Tandem.Admin

structure Capsule where
  id : Nat
  names : List Nat
deriving DecidableEq, Repr

/-- The capsules on the hub that no capsule there names. -/
def current (P : List Capsule) : List Capsule :=
  P.filter fun c => P.all fun d => !d.names.contains c.id

/-- What a change names, given the capsules it found current: those and everything they name. -/
def supersedes (cur : List Capsule) : List Nat := cur.flatMap fun x => x.id :: x.names

/-- The capsules ever written, newest first. A change may have seen any of the earlier ones
(two changes at once each see only the older ones). -/
inductive Honest : List Capsule → Prop
  | genesis (id : Nat) : Honest [⟨id, []⟩]
  | change {W : List Capsule} (cur : List Capsule) (id : Nat) :
      Honest W → cur ⊆ W → id ∉ W.map (·.id) → Honest (⟨id, supersedes cur⟩ :: W)

theorem mem_supersedes {cur : List Capsule} {n : Nat} :
    n ∈ supersedes cur ↔ ∃ x ∈ cur, n = x.id ∨ n ∈ x.names := by
  simp [supersedes]

/-- Every capsule names only capsules written before it, and IDs are distinct. -/
theorem honest_wf {W : List Capsule} (hW : Honest W) :
    (W.map (·.id)).Nodup ∧ ∀ d ∈ W, ∀ n ∈ d.names, ∃ b ∈ W, b.id = n ∧ b ≠ d := by
  induction hW with
  | genesis id => simp
  | change cur id hW hcur hid ih =>
    refine ⟨List.nodup_cons.2 ⟨hid, ih.1⟩, ?_⟩
    intro d hd n hn
    rcases List.mem_cons.1 hd with rfl | hd
    · obtain ⟨x, hx, hxn⟩ := mem_supersedes.1 hn
      have hxW := hcur hx
      rcases hxn with rfl | hxn
      · refine ⟨x, List.mem_cons_of_mem _ hxW, rfl, ?_⟩
        rintro rfl; exact hid (List.mem_map_of_mem hxW)
      · obtain ⟨b, hb, rfl, -⟩ := ih.2 x hxW n hxn
        refine ⟨b, List.mem_cons_of_mem _ hb, rfl, ?_⟩
        rintro rfl; exact hid (List.mem_map_of_mem hb)
    · obtain ⟨b, hb, rfl, hbd⟩ := ih.2 d hd n hn
      exact ⟨b, List.mem_cons_of_mem _ hb, rfl, hbd⟩

theorem eq_of_id {W : List Capsule} (hnd : (W.map (·.id)).Nodup) {a b : Capsule} (ha : a ∈ W)
    (hb : b ∈ W) (h : a.id = b.id) : a = b :=
  List.inj_on_of_nodup_map hnd ha hb h

/-- **Names are closed**: whatever a named capsule names, the naming one names too. -/
theorem names_closed {W : List Capsule} (hW : Honest W) :
    ∀ d ∈ W, ∀ b ∈ W, b.id ∈ d.names → b.names ⊆ d.names := by
  induction hW with
  | genesis id => intro d hd b _ h; simp at hd; subst hd; simp at h
  | change cur id hW hcur hid ih =>
    have wf := honest_wf hW
    intro d hd b hb hbd
    rcases List.mem_cons.1 hd with rfl | hd
    · rcases List.mem_cons.1 hb with rfl | hb
      · obtain ⟨x, hx, hxn⟩ := mem_supersedes.1 hbd
        rcases hxn with h | h
        · exact (hid (by rw [show id = x.id from h]; exact List.mem_map_of_mem (hcur hx))).elim
        · obtain ⟨c, hc, hcx, -⟩ := wf.2 x (hcur hx) _ h
          exact (hid (by rw [show id = c.id from hcx.symm]; exact List.mem_map_of_mem hc)).elim
      · obtain ⟨x, hx, hxn⟩ := mem_supersedes.1 hbd
        intro n hn
        refine mem_supersedes.2 ⟨x, hx, Or.inr ?_⟩
        rcases hxn with h | h
        · rw [eq_of_id wf.1 hb (hcur hx) h] at hn; exact hn
        · exact ih x (hcur hx) b hb h hn
    · rcases List.mem_cons.1 hb with rfl | hb
      · obtain ⟨c, hc, hcx, -⟩ := wf.2 d hd _ hbd
        exact (hid (by rw [show id = c.id from hcx.symm]; exact List.mem_map_of_mem hc)).elim
      · exact ih d hd b hb hbd

/-- `d` is above `c`: a chain of capsules ever written, each naming the next. -/
inductive Above (W : List Capsule) : Capsule → Capsule → Prop
  | named {d c : Capsule} : d ∈ W → c ∈ W → c.id ∈ d.names → Above W d c
  | trans {d b c : Capsule} : Above W d b → Above W b c → Above W d c

theorem Above.mem {W : List Capsule} {d c : Capsule} (h : Above W d c) : d ∈ W ∧ c ∈ W := by
  induction h with
  | named hd hc _ => exact ⟨hd, hc⟩
  | trans _ _ ih1 ih2 => exact ⟨ih1.1, ih2.2⟩

/-- A capsule above another names it directly. -/
theorem Above.names {W : List Capsule} (hW : Honest W) {d c : Capsule} (h : Above W d c) :
    c.id ∈ d.names := by
  induction h with
  | named _ _ h => exact h
  | trans hdb hbc ih1 ih2 => exact names_closed hW _ hdb.mem.1 _ hbc.mem.1 ih1 ih2

/-- **A superseded capsule is never current while one above it is on the hub**, whatever has
been destroyed or put back since. -/
theorem superseded_never_current {W P : List Capsule} (hW : Honest W) {d c : Capsule}
    (hd : d ∈ P) (h : Above W d c) : c ∉ current P := by
  intro hc
  have := (List.mem_filter.1 hc).2
  rw [List.all_eq_true] at this
  simpa [h.names hW] using this d hd

/-- **There is always a current capsule** on a hub that holds any capsule at all. -/
theorem current_nonempty {W P : List Capsule} (hW : Honest W) (hP : P ⊆ W) (hne : P ≠ []) :
    current P ≠ [] := by
  induction hW generalizing P with
  | genesis id =>
    obtain ⟨c, hc⟩ := List.exists_mem_of_ne_nil P hne
    have hc' := hP hc; simp at hc'; subst hc'
    intro h
    have := List.filter_eq_nil_iff.1 h _ hc
    simp only [List.all_eq_true, Bool.not_eq_true', not_forall] at this
    obtain ⟨d, hd, hn⟩ := this
    have hd' := hP hd; simp at hd'; subst hd'; simp at hn
  | change cur id hW hcur hid ih =>
    by_cases hin : (⟨id, supersedes cur⟩ : Capsule) ∈ P
    · intro h
      have := List.filter_eq_nil_iff.1 h _ hin
      simp only [List.all_eq_true, Bool.not_eq_true', not_forall] at this
      obtain ⟨d, hd, hn⟩ := this
      have hn' : id ∈ d.names := by simpa using hn
      rcases List.mem_cons.1 (hP hd) with rfl | hd'
      · obtain ⟨x, hx, hxn⟩ := mem_supersedes.1 hn'
        rcases hxn with h | h
        · exact hid (by rw [h]; exact List.mem_map_of_mem (hcur hx))
        · obtain ⟨c, hc, hcx, -⟩ := (honest_wf hW).2 x (hcur hx) _ h
          exact hid (by rw [← hcx]; exact List.mem_map_of_mem hc)
      · obtain ⟨c, hc, hcx, -⟩ := (honest_wf hW).2 d hd' _ hn'
        exact hid (by rw [← hcx]; exact List.mem_map_of_mem hc)
    · exact ih (fun x hx => (List.mem_cons.1 (hP hx)).resolve_left (fun h => hin (h ▸ hx))) hne

/-! ## Examples -/

def c0 : Capsule := ⟨0, []⟩
def c1 : Capsule := ⟨1, supersedes [c0]⟩
def c2 : Capsule := ⟨2, supersedes [c1]⟩

/-- C0 → C1 → C2, the old ones destroyed, then a copy of C0 put back: C2 names it. -/
example : current [c0, c2] = [c2] := by decide

/-- **Naming only the parent would not do**: the copy of C0 is current again beside C2. -/
theorem parent_only_replay : current [c0, ⟨2, [1]⟩] = [c0, ⟨2, [1]⟩] := by decide

def c1b : Capsule := ⟨3, supersedes [c0]⟩

/-- **Two changes at once** are both current, until the next names both. -/
theorem concurrent_changes :
    current [c0, c1, c1b] = [c1, c1b] ∧
      current [c0, c1, c1b, ⟨4, supersedes [c1, c1b]⟩] = [⟨4, supersedes [c1, c1b]⟩] := by
  decide

end Tandem.Admin
