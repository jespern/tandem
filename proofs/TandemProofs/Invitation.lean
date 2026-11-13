import TandemProofs.Registry

/-!
# Hub invitations for new members (§13.1, §6.1, §6.9)

The admin makes a manager-signed capability for one new participant: this hub, an invite key,
the new-member purpose, an expiry. The QR/link carries the invite key's secret. The new
device registers itself and signs its claim with the invite key; the capability and the claim
together admit it, with no second admin tap (`Reg.How.invited`).

* **An invitation never adds a device to an existing participant**: what it admits is the
  registration of the claiming keys themselves (`invite_never_joins_existing`). With
  `Reg.existing_member_needs_own_device`, nothing but a device of that participant can.
* It needs a capability the manager signed **for this hub**, for new members, and a claim
  signed by its invite key (`invite_needs_capability`, `other_hub_refused`).
* **Two uses of one link are two people**: different keys give different participants, both
  admitted, neither replacing the other (`two_uses_two_people`); the registry is a union and
  receiving one never invalidates the other (`Reg.valid_mono`), so nothing picks a winner. A retry of the same claim is the same participant (`retry_same_participant`).
* Accepting refuses an expired or cancelled invitation (`accept_refuses`), and what it accepts
  is a valid admission (`accepted_is_valid`).

Consumption, cancellation and the capability's encoding are Q28 in the design; only what is
specified is proved. A mutable "used" flag can't enforce one use against a hostile mailbox,
which is why duplicate uses are visible people for the admin to resolve.
-/

namespace Tandem.Inv
open Reg

/-- Registrations are hashes of (keys, enrollment): injective. -/
def RegInjective (H : Hub) : Prop := ∀ k e k' e', H.reg k e = H.reg k' e' → k = k' ∧ e = e'

theorem invite_needs_capability {H : Hub} {R : Registry} {c : Cert} {k e : String}
    (h : certOK H R c) (hh : c.how = .invited k e) :
    ∃ cap ∈ R.caps, cap.id = k ∧ cap.signer = H.manager ∧ cap.hub = H.manager ∧
      cap.purpose = "new-member" ∧ c.approver = cap.inviteKey := by
  obtain ⟨-, -, h⟩ := h
  rw [hh] at h
  obtain ⟨cap, hc, hid, hs, hhub, hp, -, ha⟩ := h
  exact ⟨cap, hc, hid, hs, hhub, hp, ha⟩

/-- A capability made for another hub, or signed by anyone but this hub's manager, admits
nobody here. -/
theorem other_hub_refused {H : Hub} {R : Registry} {c : Cert} {k e : String}
    (hh : c.how = .invited k e)
    (hcaps : ∀ cap ∈ R.caps, cap.id = k → cap.hub ≠ H.manager ∨ cap.signer ≠ H.manager) :
    ¬ certOK H R c := by
  intro h
  obtain ⟨cap, hc, hid, hs, hhub, -, -⟩ := invite_needs_capability h hh
  rcases hcaps cap hc hid with h1 | h1
  · exact h1 hhub
  · exact h1 hs

/-- **An invitation never joins an existing participant**: if it admits keys as participant
`m`, then `m` is the registration of exactly those keys and that enrollment, so it is a new
participant (or the same claim again), never another device of someone already there. -/
theorem invite_never_joins_existing {H : Hub} (hinj : RegInjective H) {R : Registry} {c : Cert}
    {k e : String} (h : certOK H R c) (hh : c.how = .invited k e) {k₀ e₀ : String}
    (hm : c.member = H.reg k₀ e₀) : c.keys = k₀ ∧ e = e₀ := by
  obtain ⟨-, -, h⟩ := h
  rw [hh] at h
  obtain ⟨-, -, -, -, -, -, hreg, -⟩ := h
  exact hinj _ _ _ _ (hreg.symm.trans hm)

/-- **Two uses of one link are two people**: different keys, different participants. -/
theorem two_uses_two_people {H : Hub} (hinj : RegInjective H) {R : Registry} {c d : Cert}
    {k e e' : String} (hc : certOK H R c) (hd : certOK H R d) (hhc : c.how = .invited k e)
    (hhd : d.how = .invited k e') (hkeys : c.keys ≠ d.keys) : c.member ≠ d.member := by
  intro heq
  obtain ⟨-, -, hc⟩ := hc
  obtain ⟨-, -, hd⟩ := hd
  rw [hhc] at hc; rw [hhd] at hd
  obtain ⟨-, -, -, -, -, -, hcm, -⟩ := hc
  obtain ⟨-, -, -, -, -, -, hdm, -⟩ := hd
  exact hkeys (hinj _ _ _ _ (hcm.symm.trans (heq.trans hdm))).1

/-- A retry reuses the registration and exact claim: the same participant. -/
theorem retry_same_participant {H : Hub} {R : Registry} {c d : Cert} {k e : String}
    (hc : certOK H R c) (hd : certOK H R d) (hhc : c.how = .invited k e) (hhd : d.how = .invited k e)
    (hkeys : c.keys = d.keys) : c.member = d.member := by
  obtain ⟨-, -, hc⟩ := hc
  obtain ⟨-, -, hd⟩ := hd
  rw [hhc] at hc; rw [hhd] at hd
  obtain ⟨-, -, -, -, -, -, hcm, -⟩ := hc
  obtain ⟨-, -, -, -, -, -, hdm, -⟩ := hd
  rw [hcm, hdm, hkeys]

/-! ## Accepting -/

structure Claim where
  capability : String
  certId : String
  keys : String
  enrollment : String
  /-- The key that signed the claim: the link's invite key, for a genuine one. -/
  signedBy : String

/-- The new device's acceptance (§13.1): refused once the capability has expired or been
cancelled, or if the claim isn't signed with its invite key. -/
def accept (H : Hub) (R : Registry) (cap : Capability) (cl : Claim) (now : Nat) (cancelled : Bool) :
    Option Cert :=
  if now < cap.expires ∧ cancelled = false ∧ cl.signedBy = cap.inviteKey ∧
      cl.capability = cap.id ∧ cap ∈ R.caps ∧ cap.signer = H.manager ∧
      cap.hub = H.manager ∧ cap.purpose = "new-member" then
    some ⟨cl.certId, H.reg cl.keys cl.enrollment, cl.keys, .invited cap.id cl.enrollment, cl.signedBy,
      R.revs.map (·.id), cl.signedBy⟩
  else none

theorem accept_refuses {H : Hub} {R : Registry} {cap : Capability} {cl : Claim} {now : Nat}
    {cancelled : Bool} (h : cap.expires ≤ now ∨ cancelled = true ∨ cl.signedBy ≠ cap.inviteKey ∨
      cl.capability ≠ cap.id ∨ cap ∉ R.caps ∨ cap.signer ≠ H.manager ∨
      cap.hub ≠ H.manager ∨ cap.purpose ≠ "new-member") :
    accept H R cap cl now cancelled = none := by
  unfold accept
  split_ifs with hc
  · obtain ⟨h1, h2, h3, h4, h5, h6, h7, h8⟩ := hc
    rcases h with h | h | h | h | h | h | h | h
    · omega
    · simp_all
    · exact absurd h3 h
    · exact h h4
    · exact h h5
    · exact h h6
    · exact h h7
    · exact h h8
  · rfl

theorem accepted_is_valid {H : Hub} {R : Registry} {cap : Capability} {cl : Claim} {now : Nat}
    {c : Cert} (h : accept H R cap cl now false = some c)
    (hrevs : ∀ r ∈ R.revs, ∃ t ∈ R.certs, t.id = r.target) : certOK H R c := by
  unfold accept at h
  split_ifs at h with hcond
  obtain ⟨-, -, h3, -, hc, hs, hhub, hp⟩ := hcond
  simp only [Option.some.injEq] at h
  subst h
  refine ⟨rfl, ?_, ⟨cap, hc, rfl, hs, hhub, hp, rfl, h3⟩⟩
  intro rid hrid
  obtain ⟨r, hr, rfl⟩ := List.mem_map.1 hrid
  exact ⟨r, hr, rfl, hrevs r hr⟩

/-! ## Example: one link used twice -/

def H0 : Hub := { manager := "MKs", creator := "creator", reg := fun k e => k ++ "/" ++ e }
def cap0 : Capability := ⟨"cap1", "MKs", "invite-key", "new-member", 100, "MKs"⟩
def alice : Cert := ⟨"c-a", "alice-phone/e1", "alice-phone", .invited "cap1" "e1", "invite-key", [], "invite-key"⟩
def thief : Cert := ⟨"c-t", "thief-phone/e2", "thief-phone", .invited "cap1" "e2", "invite-key", [], "invite-key"⟩
def R0 : Registry := ⟨[alice, thief], [], [], [cap0]⟩

/-- Both are admitted as two people; neither can become the other, nor Alice's second device. -/
example : certOK H0 R0 alice ∧ certOK H0 R0 thief ∧ alice.member ≠ thief.member ∧
    ¬ certOK H0 R0 { thief with member := alice.member } ∧
    ¬ certOK H0 ⟨[alice], [], [], [{ cap0 with hub := "other-hub" }]⟩ alice := by decide

def claim0 : Claim := ⟨"cap1", "c-a", "alice-phone", "e1", "invite-key"⟩

/-- Acceptance needs only the pre-signed capability and the invite-key claim; it does not
invent a manager signature or ask the admin to sign again. -/
example : accept H0 R0 cap0 claim0 99 false = some alice ∧
    alice.signer ≠ H0.manager ∧ certOK H0 R0 alice := by decide

/-- Boundary cases use the actual acceptance function, including capability context. -/
example : accept H0 R0 cap0 claim0 100 false = none ∧
    accept H0 R0 cap0 claim0 99 true = none ∧
    accept H0 R0 cap0 { claim0 with signedBy := "thief" } 99 false = none ∧
    accept H0 R0 cap0 { claim0 with capability := "other-cap" } 99 false = none ∧
    accept H0 { R0 with caps := [] } cap0 claim0 99 false = none ∧
    accept H0 R0 { cap0 with hub := "other-hub" } claim0 99 false = none ∧
    accept H0 R0 { cap0 with purpose := "pairing" } claim0 99 false = none := by decide

end Tandem.Inv
