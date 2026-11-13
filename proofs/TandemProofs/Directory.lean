import TandemProofs.Registry
import TandemProofs.Symbolic

/-!
# The directory (§6.6)

The directory holds the registry (`Registry.lean`) and device-signed profiles, encrypted under
a key derived from `HubK`. What the app shows comes from the registry's proofs:

* **People are participants with an admitted device**, and nothing a profile says changes who
  they are (`people_ignore_profiles`): not a name, a `joined`/`left` hint, nor a missing entry.
* **A name shown for X was signed by an admitted device of X** (`name_from_own_device`); a
  profile signed by anyone else, another member included, changes nothing anywhere
  (`forged_profile_changes_nothing`).
* **`HubK` opens no home or list** (`hubk_opens_no_data`): the directory holds no data keys.
-/

namespace Tandem.Dir
open Reg

structure Profile where
  member : String
  signer : String
  name : String
deriving DecidableEq, Repr

-- What a device shows is its own judgement: `admitted` says which certificates it holds active
-- (`Reg.active` under its policy snapshot).
variable (admitted : Cert → Bool)

/-- The participants shown, with at least one admitted device. -/
def people (R : Registry) : List String :=
  ((R.certs.filter admitted).map (·.member)).dedup

/-- The name shown for `m`: the first profile signed by an admitted device of `m`. -/
def nameOf (R : Registry) (ps : List Profile) (m : String) : Option String :=
  (ps.find? fun p => p.member == m && R.certs.any fun c => admitted c && c.member == m && c.keys == p.signer).map
    (·.name)

/-- **Who is shown depends on the registry alone**: `people` doesn't read profiles at all, and
the same holds for any change to them. -/
theorem people_ignore_profiles (R : Registry) (ps ps' : List Profile) :
    (fun _ : List Profile => people admitted R) ps = (fun _ : List Profile => people admitted R) ps' := rfl

theorem name_from_own_device {R : Registry} {ps : List Profile} {m n : String}
    (h : nameOf admitted R ps m = some n) :
    ∃ p ∈ ps, p.name = n ∧ p.member = m ∧ ∃ c ∈ R.certs, admitted c ∧ c.member = m ∧ c.keys = p.signer := by
  unfold nameOf at h
  obtain ⟨p, hp, rfl⟩ := Option.map_eq_some_iff.1 h
  have hmem := List.mem_of_find?_eq_some hp
  have hsat := List.find?_some hp
  simp only [Bool.and_eq_true, beq_iff_eq, List.any_eq_true] at hsat
  obtain ⟨hpm, c, hc, ⟨⟨hadm, hcm⟩, hck⟩⟩ := hsat
  exact ⟨p, hmem, rfl, hpm, c, hc, hadm, hcm, hck⟩

/-- **A forged profile changes nothing**: one not signed by an admitted device of the member it
names is ignored, wherever it arrives. -/
theorem forged_profile_changes_nothing {R : Registry} {f : Profile}
    (hf : ∀ c ∈ R.certs, admitted c = true → c.member = f.member → c.keys ≠ f.signer)
    (ps qs : List Profile) (m : String) :
    nameOf admitted R (ps ++ f :: qs) m = nameOf admitted R (ps ++ qs) m := by
  unfold nameOf
  congr 1
  rw [List.find?_append, List.find?_append, List.find?_cons]
  have : (f.member == m && R.certs.any fun c => admitted c && c.member == m && c.keys == f.signer) = false := by
    cases hm : f.member == m
    · rfl
    · simp only [beq_iff_eq] at hm
      simp only [Bool.true_and, List.any_eq_false, Bool.and_eq_true, beq_iff_eq, not_and]
      intro c hc hac hck
      exact hf c hc hac.1 (hac.2.trans hm.symm) hck
  rw [this]

/-! ## The directory key opens no data -/

open Sym

def privOf (pk : String) : String := pk ++ "-priv"

/-- The directory as stored: registry and profiles (public facts and public keys) under the
directory key; the home and a list on the hub under their own keys. -/
def onHub : List Msg :=
  [.enc (.kdf (.atom "HubK") (.atom "directory-key"))
      (.pair (.atom "registry") (.pair (.atom "profiles") (.atom "MKa"))),
   .enc (.kdf (.atom "HK") (.atom "msg")) (.atom "home records"),
   .enc (.kdf (.atom "K") (.atom "msg")) (.atom "list records"),
   .hpke "laptop" (.atom "HK")]

theorem hubk_opens_no_data :
    ¬ Derives privOf (onHub ++ [.atom "HubK", .atom "credential"]) (.atom "HK") ∧
    ¬ Derives privOf (onHub ++ [.atom "HubK", .atom "credential"]) (.atom "K") ∧
    ¬ Derives privOf (onHub ++ [.atom "HubK", .atom "credential"]) (.atom "home records") := by
  have h : ∀ m ∈ onHub ++ [.atom "HubK", .atom "credential"],
      safe privOf ["HK", "K", "home records", "list records", "laptop-priv"] [] m = true := by decide
  exact ⟨atom_secret h (by decide), atom_secret h (by decide), atom_secret h (by decide)⟩

end Tandem.Dir
