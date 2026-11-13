import Mathlib.Data.List.Basic

/-!
# The device registry (§6.1, §6.3, §6.4, §6.8, §6.9)

The registry is a set of manager-authorized certificates and manager-signed revocations, with members' pairing
delegations. Objects are named by their content (IDs are collision-free), a signature is its
signer's key name, and a valid set is whatever has been received: there is no order to it.
`model/management_model.py` is the same model in Python.

* **The manager can't make itself a device of an existing member**: a certificate for keys
  other than a participant's first ones needs a source device of that participant, or a
  delegation it signed (`existing_member_needs_own_device`). A hub invitation is no way round
  it: it admits only the self-identifying registration of the claiming keys. Creator recovery is the only
  exception, and only for the creator (`recovery_only_for_the_creator`).
* **A delegation can't be retargeted** to another member, and the claim must be signed by the
  link's key (`pairing_bound_to_delegation`).
* **Revoked keys never return**, under any alias or issuance (`retired_keys_never_return`); a
  certificate neither retained at a cut nor issued after it is out (`omitted_needs_renewal`);
  one retained at the cut survives the revocation of the device that approved it
  (`retained_child_survives`). A revoked approver approves nothing new
  (`revoked_approver_approves_nothing`).
* **Removal wins** and the registry merges by union: more revocations only remove devices
  (`removal_wins`), and receiving more objects never invalidates a certificate already valid
  (`valid_mono`): concurrent enrollments don't compete, and a later revocation doesn't undo
  historical issuance.
* **Floors stick**: a policy snapshot that extends a floor keeps every revocation in it, so a
  device the floor cut off is out under every extension (`floor_cut_sticks`); "extends" is a
  preorder (`extends_refl`, `extends_trans`), which is all `Eligibility.lean` asks of it.
* **Only the manager revokes** (`only_manager_revokes`).
-/

namespace Tandem.Reg

inductive How where
  /-- A new participant's first admission: the participant ID is the registration of these
  very keys (§6.1, §13.1). -/
  | first (enrollment : String)
  /-- A new participant's first admission through a hub invitation (§13.1): the manager-signed
  capability, and the claim signed by its invite key. The participant ID is still the
  registration of these very keys. -/
  | invited (capability enrollment : String)
  /-- Approved by an admitted device of the same participant: the source certificate. -/
  | byDevice (source : String)
  /-- Through a pairing delegation (§6.3). -/
  | byPairing (delegation : String)
  /-- The hub password's one exception (§6.8). -/
  | creatorRecovery
deriving DecidableEq, Repr

structure Cert where
  id : String
  member : String
  keys : String
  how : How
  /-- Who signed the request: the new keys themselves, the source device's keys, or the
  pairing link's invite key. -/
  approver : String
  /-- The revocations in its issuance snapshot. -/
  issued : List String
  /-- The certificate signer: the invite key for a capability-backed first admission,
  otherwise the manager. -/
  signer : String
deriving DecidableEq, Repr

structure Delegation where
  id : String
  member : String
  source : String
  inviteKey : String
  signer : String
  expires : Nat
deriving DecidableEq, Repr

/-- A manager-signed new-member invitation capability (§13.1). -/
structure Capability where
  id : String
  /-- The hub it is for: `hubID`, which is `pub(MKs)`. -/
  hub : String
  inviteKey : String
  purpose : String
  expires : Nat
  signer : String
deriving DecidableEq, Repr

structure Rev where
  id : String
  target : String
  retained : List String
  signer : String
deriving DecidableEq, Repr

/-- What a device pins: the manager key (`hubID = pub(MKs)`), the creator the genesis fixes,
and the registration function (a hash, so injective). -/
structure Hub where
  manager : String
  creator : String
  reg : String → String → String

structure Registry where
  certs : List Cert
  revs : List Rev
  dels : List Delegation
  caps : List Capability := []

def Registry.Sub (R R' : Registry) : Prop :=
  R.certs ⊆ R'.certs ∧ R.revs ⊆ R'.revs ∧ R.dels ⊆ R'.dels ∧ R.caps ⊆ R'.caps

/-- IDs are content hashes: two objects with one ID are one object. -/
def Registry.Unique (R : Registry) : Prop :=
  (∀ a ∈ R.certs, ∀ b ∈ R.certs, a.id = b.id → a = b) ∧
  (∀ a ∈ R.revs, ∀ b ∈ R.revs, a.id = b.id → a = b) ∧
  (∀ a ∈ R.dels, ∀ b ∈ R.dels, a.id = b.id → a = b)

/-- §6.9: revocation `r`, whose target is `t`, retires `c`: same participant, and `c` has
the target's keys, or was neither retained at the cut nor issued after it. -/
def cuts (t : Cert) (r : Rev) (c : Cert) : Prop :=
  t.member = c.member ∧ (t.keys = c.keys ∨ (c.id ∉ r.retained ∧ r.id ∉ c.issued))

/-- `c` survives every revocation in `rs` whose target is known. -/
def survives (R : Registry) (rs : List Rev) (c : Cert) : Prop :=
  ∀ r ∈ rs, ∀ t ∈ R.certs, t.id = r.target → ¬ cuts t r c

/-- The revocations of `R` in `c`'s issuance snapshot. -/
def issuedRevs (R : Registry) (c : Cert) : List Rev := R.revs.filter (fun r => decide (r.id ∈ c.issued))

/-- §6.9: a valid certificate. -/
def authoritySigner (H : Hub) (c : Cert) : String :=
  match c.how with
  | .invited _ _ => c.approver
  | _ => H.manager

def certOK (H : Hub) (R : Registry) (c : Cert) : Prop :=
  c.signer = authoritySigner H c ∧
  -- its issuance snapshot is held, targets included
  (∀ rid ∈ c.issued, ∃ r ∈ R.revs, r.id = rid ∧ ∃ t ∈ R.certs, t.id = r.target) ∧
  match c.how with
  | .first e => c.member = H.reg c.keys e ∧ c.approver = c.keys
  | .invited k e => ∃ cap ∈ R.caps, cap.id = k ∧ cap.signer = H.manager ∧ cap.hub = H.manager ∧
      cap.purpose = "new-member" ∧ c.member = H.reg c.keys e ∧ c.approver = cap.inviteKey
  | .creatorRecovery => c.member = H.creator ∧ c.approver = c.keys
  | .byDevice s => ∃ src ∈ R.certs, src.id = s ∧ src.member = c.member ∧ c.approver = src.keys ∧
      survives R (issuedRevs R c) src
  | .byPairing d => ∃ dl ∈ R.dels, dl.id = d ∧ dl.member = c.member ∧ c.approver = dl.inviteKey ∧
      ∃ src ∈ R.certs, src.id = dl.source ∧ src.member = c.member ∧ dl.signer = src.keys ∧
        survives R (issuedRevs R c) src

def revOK (H : Hub) (R : Registry) (r : Rev) : Prop :=
  r.signer = H.manager ∧ ∃ t ∈ R.certs, t.id = r.target ∧
    ∀ cid ∈ r.retained, ∃ c ∈ R.certs, c.id = cid ∧ c.member = t.member

/-- Admitted under the revocations `rs` (a policy snapshot's, or the whole registry's). -/
def active (H : Hub) (R : Registry) (rs : List Rev) (c : Cert) : Prop :=
  c ∈ R.certs ∧ certOK H R c ∧ survives R rs c

/-! ## Who can admit whom -/

/-- **The manager can't make itself a device of an existing member.** A valid certificate for
keys that aren't the participant's own first registration, for anyone but the creator, needs
a device of that participant: as the approver, or as the signer of a pairing delegation. -/
theorem existing_member_needs_own_device {H : Hub} {R : Registry} {c : Cert} (h : certOK H R c)
    (hfirst : ∀ e, c.member ≠ H.reg c.keys e) (hcreator : c.member ≠ H.creator) :
    ∃ src ∈ R.certs, src.member = c.member ∧
      (c.approver = src.keys ∨ ∃ d ∈ R.dels, d.member = c.member ∧ d.signer = src.keys ∧
        c.approver = d.inviteKey) := by
  obtain ⟨-, -, h⟩ := h
  cases hh : c.how with
  | first e => rw [hh] at h; exact absurd h.1 (hfirst e)
  | invited k e =>
    rw [hh] at h
    obtain ⟨-, -, -, -, -, -, hm, -⟩ := h
    exact absurd hm (hfirst e)
  | creatorRecovery => rw [hh] at h; exact absurd h.1 hcreator
  | byDevice s =>
    rw [hh] at h
    obtain ⟨src, hs, -, hm, ha, -⟩ := h
    exact ⟨src, hs, hm, Or.inl ha⟩
  | byPairing d =>
    rw [hh] at h
    obtain ⟨dl, hd, -, hdm, ha, src, hs, -, hm, hsig, -⟩ := h
    exact ⟨src, hs, hm, Or.inr ⟨dl, hd, hdm, hsig, ha⟩⟩

/-- With injective registrations, a participant's first admission is for its first keys only. -/
theorem first_is_first_keys {H : Hub} (hinj : ∀ k e k' e', H.reg k e = H.reg k' e' → k = k')
    {R : Registry} {c : Cert} {e : String} (h : certOK H R c) (hh : c.how = .first e)
    {k₀ e₀ : String} (hm : c.member = H.reg k₀ e₀) : c.keys = k₀ := by
  obtain ⟨-, -, h⟩ := h
  rw [hh] at h
  exact hinj _ _ _ _ (h.1.symm.trans hm)

/-- **Creator recovery is for the creator only** (§6.8). -/
theorem recovery_only_for_the_creator {H : Hub} {R : Registry} {c : Cert} (h : certOK H R c)
    (hh : c.how = .creatorRecovery) : c.member = H.creator := by
  obtain ⟨-, -, h⟩ := h
  rw [hh] at h
  exact h.1

/-- **A pairing delegation can't be retargeted**: it names the member, it was signed by a device
of that member, and the claim was signed by the link's invite key. -/
theorem pairing_bound_to_delegation {H : Hub} {R : Registry} {c : Cert} {d : String}
    (h : certOK H R c) (hh : c.how = .byPairing d) :
    ∃ dl ∈ R.dels, dl.id = d ∧ dl.member = c.member ∧ c.approver = dl.inviteKey ∧
      ∃ src ∈ R.certs, src.id = dl.source ∧ src.member = dl.member ∧ dl.signer = src.keys := by
  obtain ⟨-, -, h⟩ := h
  rw [hh] at h
  obtain ⟨dl, hd, hid, hdm, ha, src, hs, hsid, hm, hsig, -⟩ := h
  exact ⟨dl, hd, hid, hdm, ha, src, hs, hsid, hm.trans hdm.symm, hsig⟩

/-- Every certificate carries the manager's authority (for an invitation, through the
manager-signed capability): a member alone can't admit anyone. -/
theorem needs_manager {H : Hub} {R : Registry} {c : Cert} (h : certOK H R c) :
    c.signer = H.manager ∨ ∃ cap ∈ R.caps, cap.signer = H.manager ∧
      cap.hub = H.manager ∧ cap.purpose = "new-member" ∧ c.signer = cap.inviteKey := by
  cases hh : c.how with
  | invited k e =>
    obtain ⟨hs, -, hc⟩ := h
    rw [hh] at hc
    obtain ⟨cap, hcap, -, hsig, hhub, hp, -, ha⟩ := hc
    have hsigner : c.signer = c.approver := by simpa [authoritySigner, hh] using hs
    exact Or.inr ⟨cap, hcap, hsig, hhub, hp, hsigner.trans ha⟩
  | first e => exact Or.inl (by simpa [authoritySigner, hh] using h.1)
  | byDevice s => exact Or.inl (by simpa [authoritySigner, hh] using h.1)
  | byPairing d => exact Or.inl (by simpa [authoritySigner, hh] using h.1)
  | creatorRecovery => exact Or.inl (by simpa [authoritySigner, hh] using h.1)

theorem only_manager_revokes {H : Hub} {R : Registry} {r : Rev} (h : revOK H R r) :
    r.signer = H.manager := h.1

/-! ## Revocation -/

/-- **Revoked keys never return**, under any certificate, alias or issuance snapshot. -/
theorem retired_keys_never_return {R : Registry} {rs : List Rev} {r : Rev} (hr : r ∈ rs)
    {t c : Cert} (ht : t ∈ R.certs) (htid : t.id = r.target) (hm : t.member = c.member)
    (hk : t.keys = c.keys) : ¬ survives R rs c :=
  fun h => h r hr t ht htid ⟨hm, Or.inl hk⟩

/-- **Omitted at a cut, out until renewed**: neither retained by the revocation nor issued
after it. -/
theorem omitted_needs_renewal {R : Registry} {rs : List Rev} {r : Rev} (hr : r ∈ rs)
    {t c : Cert} (ht : t ∈ R.certs) (htid : t.id = r.target) (hm : t.member = c.member)
    (hret : c.id ∉ r.retained) (hiss : r.id ∉ c.issued) : ¬ survives R [r] c ∧ ¬ survives R rs c :=
  ⟨fun h => h r (by simp) t ht htid ⟨hm, Or.inr ⟨hret, hiss⟩⟩,
   fun h => h r hr t ht htid ⟨hm, Or.inr ⟨hret, hiss⟩⟩⟩

/-- **A child retained at the cut survives** the revocation of its approving parent. -/
theorem retained_child_survives {R : Registry} {r : Rev} {c : Cert}
    (hkeys : ∀ t ∈ R.certs, t.id = r.target → t.keys ≠ c.keys) (hret : c.id ∈ r.retained) :
    survives R [r] c := by
  intro r' hr' t ht htid hcut
  simp at hr'; subst hr'
  rcases hcut.2 with h | h
  · exact hkeys t ht htid h
  · exact h.1 hret

/-- **A revoked approver approves nothing new**: a certificate whose issuance snapshot holds the
revocation of its source's keys is invalid. -/
theorem revoked_approver_approves_nothing {H : Hub} {R : Registry} {c : Cert} {s : String}
    (hh : c.how = .byDevice s) {src : Cert} (hs : src ∈ R.certs) (hsid : src.id = s)
    (huniq : ∀ a ∈ R.certs, a.id = s → a = src)
    {r : Rev} (hr : r ∈ R.revs) (hri : r.id ∈ c.issued) {t : Cert} (ht : t ∈ R.certs)
    (htid : t.id = r.target) (hm : t.member = src.member) (hk : t.keys = src.keys) :
    ¬ certOK H R c := by
  rintro ⟨-, -, h⟩
  rw [hh] at h
  obtain ⟨src', hs', hsid', -, -, hsurv⟩ := h
  have := huniq src' hs' hsid'; subst this
  exact hsurv r (List.mem_filter.2 ⟨hr, by simpa using hri⟩) t ht htid ⟨hm, Or.inl hk⟩

/-- **Removal wins**: more revocations only take devices away. -/
theorem removal_wins {R : Registry} {rs rs' : List Rev} (h : rs ⊆ rs') {c : Cert}
    (hs : survives R rs' c) : survives R rs c :=
  fun r hr => hs r (h hr)

/-! ## The registry merges by union -/

theorem survives_issued_mono {R R' : Registry} (hsub : R.Sub R') (hu : R'.Unique) {c src : Cert}
    (hdep : ∀ rid ∈ c.issued, ∃ r ∈ R.revs, r.id = rid ∧ ∃ t ∈ R.certs, t.id = r.target)
    (h : survives R (issuedRevs R c) src) : survives R' (issuedRevs R' c) src := by
  intro r hr t ht htid hcut
  obtain ⟨hr', hri⟩ := List.mem_filter.1 hr
  obtain ⟨r₀, hr₀, hr₀id, t₀, ht₀, ht₀id⟩ := hdep r.id (by simpa using hri)
  have er : r₀ = r := hu.2.1 r₀ (hsub.2.1 hr₀) r hr' hr₀id
  subst er
  have et : t₀ = t := hu.1 t₀ (hsub.1 ht₀) t ht (ht₀id.trans htid.symm)
  subst et
  exact h r₀ (List.mem_filter.2 ⟨hr₀, hri⟩) t₀ ht₀ ht₀id hcut

/-- **Receiving more never invalidates a valid certificate**: validity depends only on the
objects it names, which can't change. So concurrent enrollments merge without competing, and a
later revocation doesn't undo historical issuance (it only stops the device being active). -/
theorem valid_mono {H : Hub} {R R' : Registry} (hsub : R.Sub R') (hu : R'.Unique) {c : Cert}
    (h : certOK H R c) : certOK H R' c := by
  obtain ⟨hsig, hdep, h⟩ := h
  refine ⟨hsig, fun rid hrid => ?_, ?_⟩
  · obtain ⟨r, hr, hrid', t, ht, htid⟩ := hdep rid hrid
    exact ⟨r, hsub.2.1 hr, hrid', t, hsub.1 ht, htid⟩
  · cases hh : c.how with
    | first e => rw [hh] at h; exact h
    | invited k e =>
      rw [hh] at h
      obtain ⟨cap, hc, rest⟩ := h
      exact ⟨cap, hsub.2.2.2 hc, rest⟩
    | creatorRecovery => rw [hh] at h; exact h
    | byDevice s =>
      rw [hh] at h
      obtain ⟨src, hs, hsid, hm, ha, hsurv⟩ := h
      exact ⟨src, hsub.1 hs, hsid, hm, ha, survives_issued_mono hsub hu hdep hsurv⟩
    | byPairing d =>
      rw [hh] at h
      obtain ⟨dl, hd, hdid, hdm, ha, src, hs, hsid, hm, hsig', hsurv⟩ := h
      exact ⟨dl, hsub.2.2.1 hd, hdid, hdm, ha, src, hsub.1 hs, hsid, hm, hsig',
        survives_issued_mono hsub hu hdep hsurv⟩

/-! ## Policy snapshots and floors -/

/-- A policy snapshot: the revocations it holds (its certificates matter to `Eligibility` only
through which snapshot extends which). -/
structure Snap where
  certs : List String
  revs : List Rev

/-- §6.9: a source snapshot extends a floor when it includes every floor certificate and
revocation. -/
def Extends (src floor : Snap) : Prop := floor.certs ⊆ src.certs ∧ floor.revs ⊆ src.revs

theorem extends_refl (s : Snap) : Extends s s := ⟨fun _ h => h, fun _ h => h⟩

theorem extends_trans {a b c : Snap} (h1 : Extends a b) (h2 : Extends b c) : Extends a c :=
  ⟨fun _ h => h1.1 (h2.1 h), fun _ h => h1.2 (h2.2 h)⟩

/-- **Floors stick**: a device the floor's revocations cut off is cut off under every snapshot
that extends the floor, so it can't come back as a direct writer (§8.3). -/
theorem floor_cut_sticks {H : Hub} {R : Registry} {src floor : Snap} (hx : Extends src floor)
    {c : Cert} (hout : ¬ survives R floor.revs c) : ¬ active H R src.revs c :=
  fun h => hout (removal_wins hx.2 h.2.2)

/-! ## Examples: the definitions are satisfiable (`model/test_management_model.py`) -/

instance (R : Registry) (rs : List Rev) (c : Cert) : Decidable (survives R rs c) := by
  unfold survives cuts; infer_instance

instance (H : Hub) (R : Registry) (c : Cert) : Decidable (certOK H R c) := by
  unfold certOK
  rcases c with ⟨id, member, keys, how, approver, issued, signer⟩
  cases how <;> dsimp only <;> infer_instance

def H0 : Hub := { manager := "MKs", creator := "creator", reg := fun k e => k ++ "/" ++ e }
/-- Aimee joins with her phone; her laptop is approved by the phone; her iPad pairs. -/
def phone : Cert := ⟨"c-phone", "phone/invite", "phone", .first "invite", "phone", [], "MKs"⟩
def laptop : Cert := ⟨"c-laptop", "phone/invite", "laptop", .byDevice "c-phone", "phone", [], "MKs"⟩
def dlg : Delegation := ⟨"d1", "phone/invite", "c-phone", "invite-key", "phone", 100⟩
def ipad : Cert := ⟨"c-ipad", "phone/invite", "ipad", .byPairing "d1", "invite-key", [], "MKs"⟩
def R0 : Registry := ⟨[phone, laptop, ipad], [], [dlg], []⟩

example : certOK H0 R0 phone ∧ certOK H0 R0 laptop ∧ certOK H0 R0 ipad := by decide

-- test_manager_cannot_impersonate_an_existing_member: the manager signs a key of its own
-- as Aimee's, with no device of hers involved
example : ¬ certOK H0 R0 ⟨"c-x", "phone/invite", "mgr-device", .first "invite", "mgr-device", [], "MKs"⟩ ∧
    ¬ certOK H0 R0 ⟨"c-x", "phone/invite", "mgr-device", .creatorRecovery, "mgr-device", [], "MKs"⟩ := by
  decide

-- test_revoking_first_device_preserves_member_group_and_its_other_device; and
-- test_retired_key_never_returns_under_an_alias
def revPhone : Rev := ⟨"r1", "c-phone", ["c-laptop", "c-ipad"], "MKs"⟩
def R1 : Registry := ⟨[phone, laptop, ipad], [revPhone], [dlg], []⟩
def aliasCert : Cert := ⟨"c-aliasCert", "phone/invite", "phone", .byDevice "c-laptop", "laptop", ["r1"], "MKs"⟩

example : survives R1 [revPhone] laptop ∧ survives R1 [revPhone] ipad ∧ ¬ survives R1 [revPhone] phone ∧
    certOK H0 ⟨[phone, laptop, ipad, aliasCert], [revPhone], [dlg], []⟩ aliasCert ∧
    ¬ survives ⟨[phone, laptop, ipad, aliasCert], [revPhone], [dlg], []⟩ [revPhone] aliasCert := by decide

-- test_old_phone_alias_omitted_at_cut_cannot_arrive_later: certified before the cut but not
-- retained by it, and not issued after it
def late : Cert := ⟨"c-late", "phone/invite", "watch", .byDevice "c-phone", "phone", [], "MKs"⟩
example : certOK H0 ⟨[phone, laptop, ipad, late], [revPhone], [dlg], []⟩ late ∧
    ¬ survives ⟨[phone, laptop, ipad, late], [revPhone], [dlg], []⟩ [revPhone] late := by decide

end Tandem.Reg
