import TandemProofs.Reissue

/-!
# The vector limit (§9.2)

A `context` or `base` with more than 1,024 device entries makes a write invalid. Building a
write (§9.2): its context is the join of `reach(k)` over the kept writes `k` of the current
epoch, its base the join of every kept write's base and of `reach(k)` over those from earlier
epochs.

* A write so built supersedes every kept write, provided its dot is new to them
  (`build_supersedes`).
* Its vectors name only devices already named by the kept writes (`build_devices`), and a
  re-signed write's base only devices of the original (`reissue_devices`). So if every write
  of a field names only devices that wrote the field, so does every new write: an honest
  field stays below the limit while fewer than 1,025 devices have written it
  (`honest_build_within_limit`).
-/

namespace Tandem

def buildCtx (K : List Write) (e : Nat) : VV := (K.filter (·.epoch == e)).flatMap Write.reach
def buildBase (K : List Write) (e : Nat) : VV :=
  K.flatMap Write.base ++ (K.filter (·.epoch != e)).flatMap Write.reach

theorem vle_flatMap {K : List Write} {f : Write → VV} {k : Write} (hk : k ∈ K) :
    vle (f k) (K.flatMap f) := by
  intro p hp
  exact ⟨p, List.mem_flatMap.2 ⟨k, hk, hp⟩, rfl, le_rfl⟩

/-- A write built from the kept set supersedes all of it. -/
theorem build_supersedes {K : List Write} {n : Write} {e : Nat} (he : n.epoch = e)
    (hslot : ∀ k ∈ K, k.slot = n.slot) (hle : ∀ k ∈ K, k.epoch ≤ e)
    (hctx : n.ctx = buildCtx K e) (hbase : n.base = buildBase K e)
    (hfresh : ∀ k ∈ K, k.epoch = e → ¬ includes k n) : ∀ k ∈ K, sup n k := by
  intro k hk
  refine ⟨(hslot k hk).symm, ?_⟩
  rcases (hle k hk).lt_or_eq with hlt | heq
  · refine Or.inl ⟨he ▸ hlt, ?_, ?_⟩
    · rw [hbase]
      exact vle_trans (vle_flatMap (List.mem_filter.2 ⟨hk, by simp; omega⟩))
        (vle_append_right _ _)
    · rw [hbase]; exact vle_trans (vle_flatMap hk) (vle_append_left _ _)
  · refine Or.inr ⟨he.trans heq.symm, ⟨?_, ?_⟩, hfresh k hk heq⟩
    · rw [hctx]; exact vle_flatMap (List.mem_filter.2 ⟨hk, by simp [heq]⟩)
    · rw [hbase]; exact vle_trans (vle_flatMap hk) (vle_append_left _ _)

theorem mem_devices {v : VV} {d : String} : d ∈ devices v ↔ ∃ t, (d, t) ∈ v := by
  simp [devices, List.mem_dedup]

/-- The devices a write names: its dot and its vectors. -/
def named (k : Write) : List String := k.dev :: (k.ctx.map Prod.fst ++ k.base.map Prod.fst)

theorem build_devices {K : List Write} {e : Nat} {d : String}
    (h : d ∈ devices (buildCtx K e) ∨ d ∈ devices (buildBase K e)) : ∃ k ∈ K, d ∈ named k := by
  simp only [mem_devices, buildCtx, buildBase, List.mem_append, List.mem_flatMap,
    List.mem_filter, Write.reach, List.mem_singleton] at h
  rcases h with ⟨t, k, ⟨hk, -⟩, h⟩ | ⟨t, (⟨k, hk, h⟩ | ⟨k, ⟨hk, -⟩, h⟩)⟩
  · refine ⟨k, hk, ?_⟩
    rcases h with h | h
    · exact List.mem_cons_of_mem _ (List.mem_append_left _ (List.mem_map.2 ⟨_, h, rfl⟩))
    · simp only [Prod.mk.injEq] at h; simp [named, h.1]
  · exact ⟨k, hk, List.mem_cons_of_mem _ (List.mem_append_right _ (List.mem_map.2 ⟨_, h, rfl⟩))⟩
  · refine ⟨k, hk, ?_⟩
    rcases h with h | h
    · exact List.mem_cons_of_mem _ (List.mem_append_left _ (List.mem_map.2 ⟨_, h, rfl⟩))
    · simp only [Prod.mk.injEq] at h; simp [named, h.1]

theorem size_le_of_subset {v : VV} {D : List String}
    (h : ∀ d ∈ devices v, d ∈ D) : size v ≤ D.length := by
  unfold size
  exact List.Subperm.length_le ((List.nodup_dedup _).subperm h)

/-- **Honest fields stay within the limit.** If every kept write names only devices of `D`
(the devices that ever wrote the field), the built write's vectors have at most `|D|`
entries each. -/
theorem honest_build_within_limit {K : List Write} {e : Nat} {D : List String}
    (hK : ∀ k ∈ K, ∀ d ∈ named k, d ∈ D) :
    size (buildCtx K e) ≤ D.length ∧ size (buildBase K e) ≤ D.length := by
  constructor
  · apply size_le_of_subset
    intro d hd
    obtain ⟨k, hk, hdk⟩ := build_devices (Or.inl hd)
    exact hK k hk d hdk
  · apply size_le_of_subset
    intro d hd
    obtain ⟨k, hk, hdk⟩ := build_devices (Or.inr hd)
    exact hK k hk d hdk

/-- A re-signed write names no device the original didn't. -/
theorem reissue_devices {head : Stmt} {w n : Write} (h : reissue head w = some n) :
    ∀ d ∈ named n, d ∈ named w := by
  obtain ⟨ref, -, rfl, -⟩ := reissue_spec h
  intro d hd
  simp only [named, List.map_nil, List.nil_append, List.mem_cons, List.map_append,
    List.mem_append, Write.reach, List.map_cons, List.mem_map] at hd ⊢
  rcases hd with h | h | h | h | h
  · exact Or.inl h
  · exact Or.inr (Or.inr h)
  · exact Or.inr (Or.inl h)
  · exact Or.inl h
  · simp at h

/-- Writes beyond the limit are invalid, whoever signed them. -/
theorem over_limit_invalid {R : Records} {w : Write} (h : vectorLimit < size w.ctx ∨ vectorLimit < size w.base) :
    sourceValid R w = false := by
  unfold sourceValid
  rcases h with h | h <;> simp [Nat.not_le.2 h]

end Tandem
