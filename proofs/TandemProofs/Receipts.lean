import TandemProofs.Registry
import TandemProofs.Answers
import TandemProofs.Symbolic

/-!
# Rejection and other receipts (§6.3, §6.10)

**Pairing outcomes.** An admin's rejection or a member's cancellation is a signed receipt,
not a registry object. The status of a claim is computed from the certificates and receipts
held (`status`):

* **A certificate beats a rejection**, whatever arrives when (`certificate_beats_rejection`):
  a rejection can't withdraw a completed enrollment, and it revokes nothing, since receipts
  aren't part of the registry that decides who is admitted.
* Receipts are a set: order and repeats don't matter (`status_of_same_set`).
* A rejection or cancellation signed by anyone else changes nothing
  (`forged_receipt_ignored`).
* **No secret without a certificate**: the admin releases the pairing secret `A` only to a
  certified claim, and only sealed to its exact keys (`release_needs_certificate`,
  `release_sealed_to_claim`); an uncertified claim gets nothing.

**Sharing receipts (§6.10).** A member's accept/decline receipt to the owner binds the member,
admission and answer, signed by an admitted device of the member. The owner's view of the
membership is the answer rule over the receipts it holds:

* receipts not signed by an admitted device of that member are ignored (`forged_answer_ignored`);
* holding receipts for the same answers as the member's home, the owner computes the member's
  own state (`owner_sees_member_state`); conflicting answers are both kept, and `n`/`l` wins
  (`conflicting_receipts_settle_on_no`).

This module abstracts a pairing claim to its participant and target keys. Exact release
bindings to the certificate, delegation and package hash, sharing-receipt collection context,
HPKE transport and event-ID deduplication are wire-validation obligations, not proved here.
The admitted-signer predicates are supplied by the caller.
-/

namespace Tandem.Receipts
open Reg

inductive Kind where
  | rejected | cancelled
deriving DecidableEq, Repr

structure Receipt where
  claim : String
  kind : Kind
  signer : String
deriving DecidableEq, Repr

inductive Status where
  | enrolled | rejected | cancelled | expired | pending
deriving DecidableEq, Repr

-- `isAdmin`, `isMember`: keys of the admin's admitted devices, and of the claiming member's.
variable (H : Hub) (R : Registry) (isAdmin isMember : String → Bool)

def certified (keys member : String) : Prop := ∃ c ∈ R.certs, c.keys = keys ∧ c.member = member ∧ certOK H R c

instance (keys member : String) : Decidable (certified H R keys member) := by
  unfold certified; infer_instance

/-- §6.3: a valid rejection is signed by an admin device; a valid cancellation by a device of
the member. -/
def valid (r : Receipt) : Bool :=
  match r.kind with
  | .rejected => isAdmin r.signer
  | .cancelled => isMember r.signer

def status (rs : List Receipt) (claim keys member : String) (expired : Bool) : Status :=
  if certified H R keys member then .enrolled
  else if rs.any (fun r => r.claim == claim && r.kind == .rejected && valid isAdmin isMember r) then .rejected
  else if rs.any (fun r => r.claim == claim && r.kind == .cancelled && valid isAdmin isMember r) then .cancelled
  else if expired then .expired
  else .pending

theorem certificate_beats_rejection {keys member : String} (h : certified H R keys member)
    (rs : List Receipt) (claim : String) (expired : Bool) :
    status H R isAdmin isMember rs claim keys member expired = .enrolled := by
  simp [status, h]

theorem status_of_same_set {rs rs' : List Receipt} (h : ∀ r, r ∈ rs ↔ r ∈ rs')
    (claim keys member : String) (expired : Bool) :
    status H R isAdmin isMember rs claim keys member expired =
      status H R isAdmin isMember rs' claim keys member expired := by
  have e : ∀ p : Receipt → Bool, rs.any p = rs'.any p := fun p => by
    apply Bool.eq_iff_iff.2; simp only [List.any_eq_true]
    exact ⟨fun ⟨r, hr, hp⟩ => ⟨r, (h r).1 hr, hp⟩, fun ⟨r, hr, hp⟩ => ⟨r, (h r).2 hr, hp⟩⟩
  simp only [status, e]

theorem forged_receipt_ignored {f : Receipt} (hf : valid isAdmin isMember f = false)
    (rs : List Receipt) (claim keys member : String) (expired : Bool) :
    status H R isAdmin isMember (f :: rs) claim keys member expired =
      status H R isAdmin isMember rs claim keys member expired := by
  simp [status, List.any_cons, hf]

/-! ## The release of `A` -/

open Sym in
/-- §6.3 step 3: `A`, sealed to the claim's own keys, for a certified claim only. -/
def release (keys member : String) : Option Msg :=
  if certified H R keys member then some (.hpke keys (.atom "A")) else none

theorem release_needs_certificate {keys member : String} (h : ¬ certified H R keys member) :
    release H R keys member = none := by
  simp [release, h]

theorem release_sealed_to_claim {keys member : String} {m : Sym.Msg}
    (h : release H R keys member = some m) : m = .hpke keys (.atom "A") := by
  unfold release at h
  split_ifs at h
  exact (Option.some.inj h).symm

/-! ## Sharing receipts (§6.10) -/

structure Answer where
  member : String
  admission : String
  kind : Tandem.Kind
  signer : String

/-- The owner's view of a member's answers: the receipts signed by a device of that member. -/
def answersOf (isDeviceOf : String → String → Bool) (rs : List Answer) (u : String) : Tandem.Answers :=
  (rs.filter fun r => r.member == u && isDeviceOf u r.signer).map fun r => (r.admission, r.kind)

theorem forged_answer_ignored {isDeviceOf : String → String → Bool} {f : Answer}
    (hf : isDeviceOf f.member f.signer = false) (rs : List Answer) (u : String) :
    answersOf isDeviceOf (f :: rs) u = answersOf isDeviceOf rs u := by
  unfold answersOf
  rw [List.filter_cons]
  by_cases hm : f.member = u
  · subst hm; simp [hf]
  · simp [hm]

theorem owner_sees_member_state {isDeviceOf : String → String → Bool} {rs : List Answer}
    {u : String} {home : Tandem.Answers} (h : ∀ p, p ∈ answersOf isDeviceOf rs u ↔ p ∈ home)
    (head : Stmt) (held : List Stmt) :
    mstate head u (answersOf isDeviceOf rs u) held = mstate head u home held :=
  mstate_of_same_set h

theorem conflicting_receipts_settle_on_no {isDeviceOf : String → String → Bool} {rs : List Answer}
    {u a : String} {head : Stmt} (ha : admission u head = some a) {r : Answer} (hr : r ∈ rs)
    (hm : r.member = u) (hd : isDeviceOf u r.signer = true) (hadm : r.admission = a)
    (hk : r.kind ≠ .add) (held : List Stmt) :
    mstate head u (answersOf isDeviceOf rs u) held = .left := by
  refine no_beats_add ha hk ?_
  refine List.mem_map.2 ⟨r, List.mem_filter.2 ⟨hr, by simp [hm, hd]⟩, ?_⟩
  rw [hadm]

/-! ## Examples: rejection and certification are both reachable -/

def adminKey (k : String) : Bool := k == "admin"
def memberKey (k : String) : Bool := k == "phone"
def rejected : Receipt := ⟨"claim1", .rejected, "admin"⟩

example : status Reg.H0 Reg.R0 adminKey memberKey [] "claim1" "watch" Reg.phone.member false = .pending ∧
    status Reg.H0 Reg.R0 adminKey memberKey [rejected] "claim1" "watch" Reg.phone.member false = .rejected ∧
    status Reg.H0 Reg.R0 adminKey memberKey [{ rejected with signer := "stranger" }]
      "claim1" "watch" Reg.phone.member false = .pending ∧
    status Reg.H0 Reg.R0 adminKey memberKey [⟨"claim1", .cancelled, "phone"⟩]
      "claim1" "watch" Reg.phone.member false = .cancelled ∧
    status Reg.H0 Reg.R0 adminKey memberKey [] "claim1" "watch" Reg.phone.member true = .expired ∧
    status Reg.H0 Reg.R0 adminKey memberKey [rejected] "claim1" "laptop" Reg.phone.member true = .enrolled := by
  decide

example : release Reg.H0 Reg.R0 "watch" Reg.phone.member = none ∧
    release Reg.H0 Reg.R0 "laptop" Reg.phone.member = some (.hpke "laptop" (.atom "A")) := by decide

end Tandem.Receipts
