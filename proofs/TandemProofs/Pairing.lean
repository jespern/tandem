import TandemProofs.HubPassword
import TandemProofs.Registry

/-!
# Pairing a device (§6.3)

Alice's phone makes two secrets: `L` goes only in the link, `A` only to the management key
(sealed to `MKa`). Her home key travels in a package sealed under `KDF(L || A, token)`. On
approval the admin seals `A` to the laptop's own key. In the symbolic model
(`Symbolic.lean`), with every message on the hub visible to anyone holding the mailbox
credential, which the link carries:

* **The link alone opens nothing**, the release included: someone who captures it, with keys of
  their own, gets neither `A` nor the home key (`link_alone_opens_nothing`).
* **Management alone opens nothing**: the admin, with the hub password, `MS` and `MKa`, gets `A` but not the home
  key (`admin_alone_opens_nothing`).
* Before approval, even the intended laptop holds nothing (`nothing_before_approval`); after it,
  the laptop opens the package (`approved_laptop_opens`).
* The trade-off §6.3 states, proved: the admin who also captures the link opens it
  (`admin_with_link_opens`).

And the approval itself (`approve`): it refuses expired or cancelled links, claims not signed
by the link's key and sources no longer active (`approve_refuses`), and an approval it makes
is a valid certificate of the delegating member (`approval_is_valid`).
-/

namespace Tandem.Pairing
open Sym

def privOf (pk : String) : String := pk ++ "-priv"

def L : Msg := .atom "L"
def A : Msg := .atom "A"
def homeKey : Msg := .atom "HK"
def token : Msg := .atom "token"
def MS : Msg := .atom "MS"

def package : Msg := .enc (.kdf (.pair L A) token) homeKey
def gate : Msg := .hpke "MKa" A
def release : Msg := .hpke "laptop" A
/-- The delegation and the claim: signed, public (the invite key is derived from `L`). -/
def delegation : Msg := .pair (.atom "delegation") (.atom "invite-public")
def claim : Msg := .pair (.atom "claim") (.atom "laptop")

/-- On the hub before approval: package, gate box, delegation, claim, public keys, and the
admin capsule with its salt (§6.7). -/
def staged : List Msg := [package, gate, delegation, claim, token, .atom "MKa", .atom "laptop",
  HubPassword.capsule, HubPassword.salt, HubPassword.hub]
/-- After approval, the release too. -/
def approved : List Msg := release :: staged
/-- The link: `L`, the token, the mailbox credential, the directory key, the delegation. -/
def link : List Msg := [L, token, .atom "credential", .atom "HubK", delegation]

theorem link_alone_opens_nothing :
    ¬ Derives privOf (approved ++ link ++ [.atom "thief-priv"]) homeKey ∧
    ¬ Derives privOf (approved ++ link ++ [.atom "thief-priv"]) A := by
  have h : ∀ m ∈ approved ++ link ++ [.atom "thief-priv"],
      safe privOf ["A", "HK", "MS", "MKa-priv", "laptop-priv"] [HubPassword.thePassword] m = true := by
    decide
  exact ⟨atom_secret h (by decide), atom_secret h (by decide)⟩

theorem admin_alone_opens_nothing :
    ¬ Derives privOf (approved ++ [.pw HubPassword.thePassword, MS, .atom "MKa-priv"]) homeKey ∧
    ¬ Derives privOf (approved ++ [.pw HubPassword.thePassword, MS, .atom "MKa-priv"]) L := by
  have h : ∀ m ∈ approved ++ [.pw HubPassword.thePassword, MS, .atom "MKa-priv"],
      safe privOf ["L", "HK", "laptop-priv"] [] m = true := by decide
  exact ⟨atom_secret h (by decide), atom_secret h (by decide)⟩

theorem nothing_before_approval :
    ¬ Derives privOf (staged ++ link ++ [.atom "laptop-priv"]) homeKey := by
  have h : ∀ m ∈ staged ++ link ++ [.atom "laptop-priv"],
      safe privOf ["A", "HK", "MS", "MKa-priv"] [HubPassword.thePassword] m = true := by decide
  exact atom_secret h (by decide)

theorem approved_laptop_opens :
    Derives privOf (approved ++ link ++ [.atom "laptop-priv"]) homeKey := by
  let K := approved ++ link ++ [.atom "laptop-priv"]
  have hp : package ∈ K := by simp [K, approved, staged]
  have hL : L ∈ K := by simp [K, link]
  have ht : token ∈ K := by simp [K, link]
  have hr : release ∈ K := by simp [K, approved]
  have hk : Msg.atom (privOf "laptop") ∈ K := by simp [K, privOf]
  exact .dec (.know hp) (.kdf (.pair (.know hL) (.unhpke (.know hr) (.know hk))) (.know ht))

theorem admin_with_link_opens :
    Derives privOf (staged ++ link ++ [.atom "MKa-priv"]) homeKey := by
  let K := staged ++ link ++ [.atom "MKa-priv"]
  have hp : package ∈ K := by simp [K, staged]
  have hL : L ∈ K := by simp [K, link]
  have ht : token ∈ K := by simp [K, link]
  have hg : gate ∈ K := by simp [K, staged]
  have hk : Msg.atom (privOf "MKa") ∈ K := by simp [K, privOf]
  exact .dec (.know hp) (.kdf (.pair (.know hL) (.unhpke (.know hg) (.know hk))) (.know ht))

/-! ## Approval -/

open Reg

structure Claim where
  delegation : String
  keys : String
  /-- The key that signed the claim: the link's invite key, for a genuine claim. -/
  signedBy : String
  certId : String

/-- §6.3 step 3: the admin approves a claim on a delegation, at `now`, against the registry,
issuing under every revocation it holds. `none`: refused. -/
def approve (H : Hub) (R : Registry) (dl : Delegation) (cl : Claim) (now : Nat) (cancelled : Bool)
    (sourceActive : Bool) : Option Cert :=
  if now < dl.expires ∧ cancelled = false ∧ cl.signedBy = dl.inviteKey ∧ cl.delegation = dl.id ∧
      sourceActive = true then
    some ⟨cl.certId, dl.member, cl.keys, .byPairing dl.id, cl.signedBy, R.revs.map (·.id), H.manager⟩
  else none

theorem approve_refuses {H : Hub} {R : Registry} {dl : Delegation} {cl : Claim} {now : Nat}
    {cancelled sourceActive : Bool}
    (h : dl.expires ≤ now ∨ cancelled = true ∨ cl.signedBy ≠ dl.inviteKey ∨ cl.delegation ≠ dl.id ∨
      sourceActive = false) : approve H R dl cl now cancelled sourceActive = none := by
  unfold approve
  split_ifs with hc
  · obtain ⟨h1, h2, h3, h4, h5⟩ := hc
    rcases h with h | h | h | h | h
    · omega
    · simp_all
    · exact absurd h3 h
    · exact absurd h4 h
    · simp_all
  · rfl

/-- **An approval is a valid certificate of the delegating member**, given a delegation signed by
an admitted device of that member that survives every revocation, and a registry whose
revocations name known certificates. -/
theorem approval_is_valid {H : Hub} {R : Registry} {dl : Delegation} {cl : Claim} {now : Nat}
    {c : Cert} (h : approve H R dl cl now false true = some c) (hd : dl ∈ R.dels)
    {src : Cert} (hs : src ∈ R.certs) (hsid : src.id = dl.source) (hm : src.member = dl.member)
    (hsig : dl.signer = src.keys) (hsurv : survives R R.revs src)
    (hrevs : ∀ r ∈ R.revs, ∃ t ∈ R.certs, t.id = r.target) : certOK H R c := by
  unfold approve at h
  split_ifs at h with hc
  obtain ⟨-, -, h3, -, -⟩ := hc
  simp only [Option.some.injEq] at h
  subst h
  refine ⟨rfl, ?_, ?_⟩
  · intro rid hrid
    obtain ⟨r, hr, rfl⟩ := List.mem_map.1 hrid
    exact ⟨r, hr, rfl, hrevs r hr⟩
  · exact ⟨dl, hd, rfl, rfl, h3, src, hs, hsid, hm, hsig,
      fun r hr => hsurv r (List.mem_filter.1 hr).1⟩

end Tandem.Pairing
