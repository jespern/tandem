import Mathlib.Data.Finset.Card
import Mathlib.Data.Finset.Max
import Mathlib.Data.Finset.Union
import TandemProofs.Kept

/-!
# Collecting and healing file chunks (DESIGN.md §17.7, §17.8; open question B9)

What clean-up and healing of the files extension guarantee, for any interleaving of devices:

* **Protection.** A device destroys no chunk that a manifest it considers protected lists, by any
  rule (`collect_spares_protected`). So a chunk a live manifest needs is destroyed only by a device
  whose view lacked that manifest (`only_a_stale_view`): §17.7's three named histories, each of
  which healing repairs.
* **Copies.** Of several messages carrying one address, every device keeps the lowest verified
  one. Under any interleaving of destroys, each justified by a verified message with that address
  and a lower ID, which may itself have been destroyed since (stale views), the lowest verified copy
  is never destroyed (`lowest_verified_survives`), so an address that had a verified copy keeps one
  (`a_copy_survives`). A junk message with a lower ID justifies nothing: only verified ones do.
* **Healing.** Sealing is deterministic, so a device that holds a file's content restores every
  chunk its manifest lists when the manifest is consistent with the content (`heal_restores`), and
  never appends a chunk the manifest doesn't list (`heal_appends_only_listed`).

The cryptography is assumed, as elsewhere in `proofs/`: an address is the hash of an envelope,
and a message is verified when its body hashes to the address it claims.
-/

set_option linter.unusedSectionVars false

namespace Tandem.Files

open Classical

/-! ## Protection -/

section Protection

variable {A : Type*} [DecidableEq A]

/-- What one device's clean-up decides from, for one scope (§17.7): the addresses of manifests it
considers protected (live, held, pending, or still on the feed), those some write it holds has
listed, and whether anything stops clean-up there (an unknown manifest version, a held write, an
unresolved inventory entry for orphans). -/
structure View (A : Type*) where
  protect : Finset A
  listed : Finset A
  collect : Prop
  orphans : Prop

/-- §17.7's rules: a chunk some write listed and nothing protects, or an orphan past the grace. -/
def mayCollect (v : View A) (a : A) (aged : Prop) : Prop :=
  v.collect ∧ a ∉ v.protect ∧ (a ∈ v.listed ∨ (v.orphans ∧ a ∉ v.listed ∧ aged))

/-- **A device never destroys a chunk it considers protected.** -/
theorem collect_spares_protected {v : View A} {a : A} {aged : Prop} (h : mayCollect v a aged) :
    a ∉ v.protect := h.2.1

/-- **Only a stale view loses a needed chunk**: if a device's protected set covers every live
manifest's addresses, its clean-up destroys none of them. -/
theorem only_a_stale_view {v : View A} {live : Finset A} (hcover : live ⊆ v.protect) {a : A}
    {aged : Prop} (h : mayCollect v a aged) : a ∉ live :=
  fun ha => h.2.1 (hcover ha)

end Protection

/-! ## Copies -/

section Copies

variable {A C : Type*} [DecidableEq A] [DecidableEq C]
variable (addr : C → A) (id : C → ℕ) (verified : C → Prop)

/-- A destroy of `v` justified by `j` (§17.7, copies): `j` is verified, carries the same address,
and has a lower ID. `j` need only have existed: the device's view may be stale. -/
def justified (v j : C) : Prop := verified j ∧ addr j = addr v ∧ id j < id v

/-- The lowest verified message of an address, among the messages ever on the hub. -/
def lowest (ever : Finset C) (c : C) : Prop :=
  c ∈ ever ∧ verified c ∧ ∀ c' ∈ ever, verified c' → addr c' = addr c → id c ≤ id c'

/-- **The lowest verified copy is never destroyed**, by any device, whatever it saw. -/
theorem lowest_verified_survives {ever : Finset C} {c : C} (hc : lowest addr id verified ever c)
    {j : C} (hj : j ∈ ever) : ¬ justified addr id verified c j := by
  rintro ⟨hv, ha, hlt⟩
  exact absurd (hc.2.2 j hj hv ha) (not_le.2 hlt)

/-- **An address that had a verified copy keeps one**: some verified message with it was never
destroyed, under any set of justified destroys. -/
theorem a_copy_survives {ever : Finset C} {a : A} (h : ∃ c ∈ ever, verified c ∧ addr c = a)
    (destroyed : C → Prop) (hd : ∀ v, destroyed v → ∃ j ∈ ever, justified addr id verified v j) :
    ∃ c ∈ ever, verified c ∧ addr c = a ∧ ¬ destroyed c := by
  let cands := ever.filter (fun c => verified c ∧ addr c = a)
  have hne : cands.Nonempty := by
    obtain ⟨c, hc, hv, ha⟩ := h
    exact ⟨c, Finset.mem_filter.2 ⟨hc, hv, ha⟩⟩
  obtain ⟨c, hc, hmin⟩ := cands.exists_min_image id hne
  obtain ⟨hce, hcv, hca⟩ := Finset.mem_filter.1 hc
  refine ⟨c, hce, hcv, hca, fun hdc => ?_⟩
  obtain ⟨j, hj, hjv, hja, hjlt⟩ := hd c hdc
  have := hmin j (Finset.mem_filter.2 ⟨hj, hjv, hja.trans hca⟩)
  exact absurd this (not_le.2 hjlt)

/-- A junk message (not verified) justifies no destroy, whatever address it claims. -/
theorem junk_justifies_nothing {v j : C} (hj : ¬ verified j) : ¬ justified addr id verified v j :=
  fun h => hj h.1

end Copies

/-! ## Healing -/

section Healing

variable {A K P E : Type*} [DecidableEq A] [DecidableEq E]
variable (sealWith : K → P → E) (H : E → A)

/-- One entry of a manifest: a chunk's key and address (§17.5), with the plaintext the device
holds for it at the entry's offset. -/
structure Entry (K P A : Type*) where
  key : K
  plain : P
  addr : A

/-- What healing appends for a manifest whose content the device holds (§17.8): every entry's
chunk, sealed again, when it hashes to the listed address; nothing otherwise (the device then
checks its copy, and marks the manifest unreadable or drops the copy). -/
noncomputable def heal (es : List (Entry K P A)) : List E :=
  if es.all (fun e => decide (H (sealWith e.key e.plain) = e.addr)) then es.map (fun e => sealWith e.key e.plain) else []

/-- **Healing restores every listed chunk** of a manifest consistent with the content. -/
theorem heal_restores {es : List (Entry K P A)} (hc : ∀ e ∈ es, H (sealWith e.key e.plain) = e.addr) :
    ∀ e ∈ es, ∃ env ∈ heal sealWith H es, H env = e.addr := by
  intro e he
  have hall : es.all (fun e => decide (H (sealWith e.key e.plain) = e.addr)) = true :=
    List.all_eq_true.2 (fun x hx => decide_eq_true (hc x hx))
  refine ⟨sealWith e.key e.plain, ?_, hc e he⟩
  simp only [heal, hall, ite_true]
  exact List.mem_map.2 ⟨e, he, rfl⟩

/-- **Healing appends no junk**: every chunk it appends carries an address the manifest lists. -/
theorem heal_appends_only_listed {es : List (Entry K P A)} :
    ∀ env ∈ heal sealWith H es, ∃ e ∈ es, H env = e.addr := by
  intro env henv
  unfold heal at henv
  split_ifs at henv with hall
  · obtain ⟨e, he, rfl⟩ := List.mem_map.1 henv
    exact ⟨e, he, of_decide_eq_true (List.all_eq_true.1 hall e he)⟩
  · simp at henv

end Healing

/-! ## Pending writes and the feed rule (§17.6, §17.7)

Which manifests a device protects is computed from writes: the manifests kept among the writes of
its replica, and among those of them that are on the feed, together with its held and pending
writes. Supersession is any strict partial order (`Kept`). A pending write is never in the replica
(§17.6). -/

section Pending

variable {W A C : Type*} [DecidableEq W] [DecidableEq A] [DecidableEq C]
variable (r : W → W → Prop) [DecidableRel r] (addrs : W → Finset A)

/-- The addresses of the manifests a set of writes keeps (§17.7, "live"). -/
def liveAddrs (S : Finset W) : Finset A := (kept r S).biUnion addrs

/-- What a device protects (§17.7): what its replica `R` keeps live, what the writes of it that
are on the feed `F` keep live, and its held `H` and pending `P` writes. -/
def protectOf (R F P H : Finset W) : Finset A :=
  liveAddrs r addrs R ∪ liveAddrs r addrs F ∪ P.biUnion addrs ∪ H.biUnion addrs

variable {r addrs}

/-- **Only what is on the feed takes protection away**: a device protects everything a device
holding just the writes on the feed would show, whatever its replica holds beyond them. -/
theorem feed_rule (R F P H : Finset W) : liveAddrs r addrs F ⊆ protectOf r addrs R F P H :=
  fun _ ha => Finset.mem_union_left _ (Finset.mem_union_left _ (Finset.mem_union_right _ ha))

/-- **A pending write is protected**, and so is what the replica keeps while it is pending. -/
theorem pending_protected (R F P H : Finset W) {p : W} (hp : p ∈ P) :
    addrs p ⊆ protectOf r addrs R F P H ∧ liveAddrs r addrs R ⊆ protectOf r addrs R F P H := by
  refine ⟨fun a ha => ?_, fun a ha => ?_⟩
  · exact Finset.mem_union_left _ (Finset.mem_union_right _ (Finset.mem_biUnion.2 ⟨p, hp, ha⟩))
  · exact Finset.mem_union_left _ (Finset.mem_union_left _ (Finset.mem_union_left _ ha))

/-- Two writes: `1` replaces `0`; `0`'s manifest lists address `0`, `1`'s address `1`. -/
def two : ℕ → ℕ → Prop := fun y x => y = 1 ∧ x = 0

/-- **Why the feed rule is needed**: a device whose replica already holds an unpublished
replacement keeps the old version live in neither its replica nor anywhere else, while every other
device, which sees only the feed, still shows it. Without the feed clause its address would go
unprotected. -/
theorem without_feed_rule_loses :
    (0 : ℕ) ∈ liveAddrs two (fun w => ({w} : Finset ℕ)) ({0} : Finset ℕ) ∧
      (0 : ℕ) ∉ liveAddrs two (fun w => ({w} : Finset ℕ)) ({0, 1} : Finset ℕ) := by
  refine ⟨?_, ?_⟩
  · simp [liveAddrs, kept, two]
  · simp [liveAddrs, kept, two]

/-- **A committed manifest's chunks stay on the hub.** The device commits only once every address
it lists has a verified message (§17.6). From then on, if every destroy is either of a copy,
justified by a verified lower copy (stale views allowed), or a clean-up by a device that protects
this manifest, each of its addresses keeps a verified message. -/
theorem committed_chunks_stay (caddr : C → A) (cid : C → ℕ) (verified : C → Prop)
    {ever : Finset C} {p : W}
    (hcommit : ∀ a ∈ addrs p, ∃ c ∈ ever, verified c ∧ caddr c = a)
    (destroyed : C → Prop)
    (hd : ∀ v, destroyed v → (∃ j ∈ ever, justified caddr cid verified v j) ∨ caddr v ∉ addrs p) :
    ∀ a ∈ addrs p, ∃ c ∈ ever, verified c ∧ caddr c = a ∧ ¬ destroyed c := by
  intro a ha
  let cands := ever.filter (fun c => verified c ∧ caddr c = a)
  have hne : cands.Nonempty := by
    obtain ⟨c, hc, hv, hca⟩ := hcommit a ha
    exact ⟨c, Finset.mem_filter.2 ⟨hc, hv, hca⟩⟩
  obtain ⟨c, hc, hmin⟩ := cands.exists_min_image cid hne
  obtain ⟨hce, hcv, hca⟩ := Finset.mem_filter.1 hc
  refine ⟨c, hce, hcv, hca, fun hdc => ?_⟩
  rcases hd c hdc with ⟨j, hj, hjv, hja, hjlt⟩ | hout
  · exact absurd (hmin j (Finset.mem_filter.2 ⟨hj, hjv, hja.trans hca⟩)) (not_le.2 hjlt)
  · exact hout (hca ▸ ha)

end Pending

end Tandem.Files
