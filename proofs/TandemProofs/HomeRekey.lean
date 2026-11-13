import TandemProofs.Symbolic

/-!
# Re-keying a home after a revocation (§6.2, §6.4)

A home epoch is a random key `HK`, sealed (HPKE) to each admitted device of the participant,
and to `MKa` for the creator only (§6.8), with the home's records under it. After a device is
revoked, the remaining devices start a new epoch with a fresh random key, sealed only to
themselves, and announce it in the old channel without the key. In the symbolic model, for
**any** old and remaining devices:

* **A revoked device can't read the new epoch**, whatever else it holds as long as that shows no
  secret: the old key, the old records, the announcement, every message on the hub
  (`revoked_cannot_read_next`).
* The remaining devices can (`remaining_read_next`).
* **The admin can't read a member's home**, old or new (`admin_cannot_read_member_home`); the
  creator's home is the one exception, through `MKa` and the hub password, which is the stated
  recovery (`creator_home_opens_with_management`).

And two counterexamples that are why the procedure is as it is:

* A next key derived from the old one leaks to the revoked device (`derived_next_key_leaks`):
  the new key must be fresh.
* Anything still written under the old key after the revocation is readable by it
  (`writing_under_old_key_leaks`): devices queue new content until the new epoch exists.
-/

namespace Tandem.Rekey
open Sym

def privOf (d : String) : String := d ++ "-priv"

/-- One home epoch on the hub: the key sealed to each device, and the records under it. -/
def epoch (devices : List String) (key records : Msg) : List Msg :=
  devices.map (fun d => .hpke d key) ++ [.enc key records]

def HK0 : Msg := .atom "HK0"
def HK1 : Msg := .atom "HK1"
def old : Msg := .atom "d0"
def new : Msg := .atom "d1"
/-- The announcement of the new epoch in the old channel: its start, not its key. -/
def announce : Msg := .enc HK0 (.pair (.atom "next") (.atom "E1"))

def secrets (remaining : List String) : List String := "HK1" :: "d1" :: remaining.map privOf

theorem not_priv {s : String} (hs : s.length < 5) (ds : List String) : s ∉ ds.map privOf := by
  intro h
  obtain ⟨d, -, hd⟩ := List.mem_map.1 h
  have := congrArg String.length hd
  simp only [privOf, String.length_append] at this
  have : "-priv".length = 5 := by decide
  omega

theorem privOf_inj {a b : String} (h : privOf a = privOf b) : a = b := by
  unfold privOf at h
  have := congrArg String.toList h
  simp only [String.toList_append, List.append_cancel_right_eq] at this
  exact String.toList_inj.1 this

theorem short_safe {s : String} (hs : s.length < 5) (h1 : s ≠ "HK1") (h2 : s ≠ "d1")
    (ds : List String) : safe privOf (secrets ds) [] (.atom s) = true := by
  simp only [safe, secrets, List.contains_cons, Bool.not_eq_true', Bool.or_eq_false_iff,
    beq_eq_false_iff_ne, ne_eq]
  refine ⟨h1, h2, ?_⟩
  simpa using not_priv hs ds

/-- **A revoked device can't read the new home epoch.** `others`: everything else the attacker
holds (its own private key, old records, any message on the hub), provided it shows no secret. -/
theorem revoked_cannot_read_next (before remaining : List String) {others : List Msg}
    (hK : ∀ m ∈ others, safe privOf (secrets remaining) [] m = true) :
    ¬ Derives privOf (epoch before HK0 old ++ [announce] ++ epoch remaining HK1 new ++ others) HK1 ∧
    ¬ Derives privOf (epoch before HK0 old ++ [announce] ++ epoch remaining HK1 new ++ others) new := by
  have hHK0 := short_safe (s := "HK0") (by decide) (by decide) (by decide) remaining
  have hold := short_safe (s := "d0") (by decide) (by decide) (by decide) remaining
  have hall : ∀ m ∈ epoch before HK0 old ++ [announce] ++ epoch remaining HK1 new ++ others,
      safe privOf (secrets remaining) [] m = true := by
    intro m hm
    simp only [List.mem_append, List.mem_singleton, epoch, List.mem_map] at hm
    rcases hm with (((⟨d, -, rfl⟩ | rfl) | rfl) | (⟨d, hd, rfl⟩ | rfl)) | hm
    · simp only [safe, Bool.or_eq_true]; exact Or.inr (by simpa [HK0] using hHK0)
    · simp only [safe, Bool.or_eq_true]; exact Or.inr (by simpa [old] using hold)
    · simp only [announce, safe, Bool.or_eq_true, Bool.and_eq_true]
      exact Or.inr ⟨short_safe (by decide) (by decide) (by decide) remaining,
        short_safe (by decide) (by decide) (by decide) remaining⟩
    · simp only [safe, Bool.or_eq_true, secrets, List.elem_eq_mem, decide_eq_true_eq]
      exact Or.inl (by simp [List.mem_map_of_mem hd])
    · simp [safe, HK1, secrets]
    · exact hK m hm
  exact ⟨atom_secret hall (by simp [secrets]), atom_secret hall (by simp [secrets])⟩

theorem remaining_read_next {before remaining : List String} {d : String} (hd : d ∈ remaining) :
    Derives privOf (epoch before HK0 old ++ [announce] ++ epoch remaining HK1 new ++ [.atom (privOf d)]) new := by
  let K := epoch before HK0 old ++ [announce] ++ epoch remaining HK1 new ++ [.atom (privOf d)]
  have hs : Msg.hpke d HK1 ∈ K := by simp [K, epoch, hd]
  have he : Msg.enc HK1 new ∈ K := by simp [K, epoch]
  have hp : Msg.atom (privOf d) ∈ K := by simp [K]
  exact .dec (.know he) (.unhpke (.know hs) (.know hp))

/-- **The admin can't read a member's home**: holding `MS` and `MKa`'s private key, it gets
neither epoch's key unless the home was sealed to `MKa`, which only the creator's is. -/
theorem admin_cannot_read_member_home {before remaining : List String} (hb : "MKa" ∉ before)
    (hr : "MKa" ∉ remaining) :
    ¬ Derives privOf (epoch before HK0 old ++ epoch remaining HK1 new ++ [.atom "MS", .atom (privOf "MKa")]) HK0 ∧
    ¬ Derives privOf (epoch before HK0 old ++ epoch remaining HK1 new ++ [.atom "MS", .atom (privOf "MKa")]) HK1 := by
  let S := "HK0" :: "HK1" :: "d0" :: "d1" :: (before ++ remaining).map privOf
  have hMKa : privOf "MKa" ∉ (before ++ remaining).map privOf := by
    intro h
    obtain ⟨d, hd, hdeq⟩ := List.mem_map.1 h
    rw [privOf_inj hdeq] at hd
    rcases List.mem_append.1 hd with h | h
    · exact hb h
    · exact hr h
  have hall : ∀ m ∈ epoch before HK0 old ++ epoch remaining HK1 new ++ [.atom "MS", .atom (privOf "MKa")],
      safe privOf S [] m = true := by
    intro m hm
    simp only [List.mem_append, epoch, List.mem_map, List.mem_cons, List.not_mem_nil, or_false] at hm
    rcases hm with ((⟨d, hd, rfl⟩ | rfl) | (⟨d, hd, rfl⟩ | rfl)) | rfl | rfl
    · have hc : privOf d ∈ S := by
        simp only [S, List.mem_cons]
        exact Or.inr (Or.inr (Or.inr (Or.inr (List.mem_map_of_mem (List.mem_append_left _ hd)))))
      simp only [safe, Bool.or_eq_true]; exact Or.inl (List.contains_iff_mem.2 hc)
    · simp [safe, S, HK0]
    · have hc : privOf d ∈ S := by
        simp only [S, List.mem_cons]
        exact Or.inr (Or.inr (Or.inr (Or.inr (List.mem_map_of_mem (List.mem_append_right _ hd)))))
      simp only [safe, Bool.or_eq_true]; exact Or.inl (List.contains_iff_mem.2 hc)
    · simp [safe, S, HK1]
    · simpa [safe, S] using not_priv (s := "MS") (by decide) (before ++ remaining)
    · have h5 : privOf "MKa" ≠ "HK0" ∧ privOf "MKa" ≠ "HK1" ∧ privOf "MKa" ≠ "d0" ∧
          privOf "MKa" ≠ "d1" := by decide
      simp only [safe, S]
      simp [h5]
      exact ⟨fun x hx h => hb (privOf_inj h ▸ hx), fun x hx h => hr (privOf_inj h ▸ hx)⟩
  exact ⟨atom_secret hall (by simp [S]), atom_secret hall (by simp [S])⟩

/-- The creator's home is also sealed to `MKa`: whoever holds the management key reads it.
That is the creator's recovery (§6.8), and why the hub password protects it. -/
theorem creator_home_opens_with_management {before remaining : List String} (hr : "MKa" ∈ remaining) :
    Derives privOf (epoch before HK0 old ++ [announce] ++ epoch remaining HK1 new ++
      [.atom (privOf "MKa")]) new :=
  remaining_read_next hr

/-! ## Why the procedure is as it is -/

/-- A next key derived from the old one leaks to the revoked device. -/
theorem derived_next_key_leaks :
    Derives privOf (epoch ["phone", "stolen"] HK0 old ++ epoch ["phone"] (.kdf HK0 (.atom "next")) new ++
      [.atom "next", .atom (privOf "stolen")]) new := by
  let K := epoch ["phone", "stolen"] HK0 old ++ epoch ["phone"] (.kdf HK0 (.atom "next")) new ++
      [.atom "next", .atom (privOf "stolen")]
  have hs : Msg.hpke "stolen" HK0 ∈ K := by simp [K, epoch]
  have hp : Msg.atom (privOf "stolen") ∈ K := by simp [K]
  have hn : Msg.atom "next" ∈ K := by simp [K]
  have he : Msg.enc (.kdf HK0 (.atom "next")) new ∈ K := by simp [K, epoch]
  exact .dec (.know he) (.kdf (.unhpke (.know hs) (.know hp)) (.know hn))

/-- Anything written under the old key after the revocation is readable by the revoked device. -/
theorem writing_under_old_key_leaks :
    Derives privOf (epoch ["phone", "stolen"] HK0 old ++ [.enc HK0 (.atom "late"), .atom (privOf "stolen")])
      (.atom "late") := by
  let K := epoch ["phone", "stolen"] HK0 old ++ [.enc HK0 (.atom "late"), .atom (privOf "stolen")]
  have hs : Msg.hpke "stolen" HK0 ∈ K := by simp [K, epoch]
  have hp : Msg.atom (privOf "stolen") ∈ K := by simp [K]
  have he : Msg.enc HK0 (.atom "late") ∈ K := by simp [K]
  exact .dec (.know he) (.unhpke (.know hs) (.know hp))

/-- The example: the stolen phone, with its own key, after Alice's phone and laptop re-keyed. -/
example : ¬ Derives privOf (epoch ["phone", "laptop", "stolen"] HK0 old ++ [announce] ++
    epoch ["phone", "laptop"] HK1 new ++ [.atom (privOf "stolen")]) new :=
  (revoked_cannot_read_next _ _ (by decide)).2

end Tandem.Rekey
