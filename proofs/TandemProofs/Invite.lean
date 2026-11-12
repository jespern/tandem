import TandemProofs.Reissue

/-!
# Invites (§11.1, §11.3)

An invite is open iff it isn't closed in home, it has uses left, and **nobody it admitted has
ever been removed**. Uses and removals are read from the roster's history: the `via` field of
each member, fixed while they stay. The scan follows `owner_model.invite_open`.

* Once closed, closed for good, at every later roster head (`closed_stays_closed`).
* Removing someone who came in through an invite closes it, with no second write to home
  (`removal_closes`), even if they come back through another invite.
* A user the roster once listed and no longer lists is never admitted unasked
  (`former_member_needs_asking`).
-/

namespace Tandem

structure InviteRec where
  id : String
  closed : Bool
  maxUses : Nat
deriving DecidableEq, Repr

/-- Who a statement lists as having come in through invite `i`. -/
def came (i : String) (x : Node) : List String :=
  (x.body.members.filter (·.via == some i)).map (·.uid)

/-- Scanning the line root first: has anyone who came in through `i` stopped being listed so? -/
def removedScan (i : String) : List Node → List String → Bool
  | [], _ => false
  | x :: xs, users => users.any (fun u => !(came i x).contains u) || removedScan i xs (users ++ came i x)

def usesOf (i : String) (line : List Node) : Nat := (line.flatMap (came i)).dedup.length

/-- §11.3, for a roster whose head is `head`. -/
def inviteOpen (head : Stmt) (inv : InviteRec) : Bool :=
  !inv.closed && !removedScan inv.id head.reverse [] && decide (usesOf inv.id head.reverse < inv.maxUses)

theorem removedScan_mono_users {i : String} {L : List Node} {u v : List String} (huv : u ⊆ v)
    (h : removedScan i L u = true) : removedScan i L v = true := by
  induction L generalizing u v with
  | nil => simp [removedScan] at h
  | cons x xs ih =>
    simp only [removedScan, Bool.or_eq_true, List.any_eq_true] at h ⊢
    rcases h with ⟨a, ha, h⟩ | h
    · exact Or.inl ⟨a, huv ha, h⟩
    · refine Or.inr (ih (fun a ha => ?_) h)
      rcases List.mem_append.1 ha with ha | ha
      · exact List.mem_append_left _ (huv ha)
      · exact List.mem_append_right _ ha

theorem removedScan_append {i : String} (L M : List Node) (u : List String) :
    removedScan i (L ++ M) u = (removedScan i L u || removedScan i M (u ++ L.flatMap (came i))) := by
  induction L generalizing u with
  | nil => simp [removedScan]
  | cons x xs ih => simp [removedScan, ih, List.append_assoc, Bool.or_assoc]

theorem removedScan_of_mem {i : String} {L : List Node} {users : List String} {u : String}
    {y : Node} (hu : u ∈ users) (hy : y ∈ L) (hn : u ∉ came i y) :
    removedScan i L users = true := by
  induction L generalizing users with
  | nil => simp at hy
  | cons x xs ih =>
    simp only [removedScan, Bool.or_eq_true, List.any_eq_true]
    rcases List.mem_cons.1 hy with rfl | hy
    · exact Or.inl ⟨u, hu, by simpa using hn⟩
    · exact Or.inr (ih (List.mem_append_left _ hu) hy)

/-- **Removing someone closes the invite they came in through**: if `u` came in through `i`
at one statement and a later statement doesn't list them as having done so (removed, or back
through another invite), the scan finds it. -/
theorem removal_closes {i u : String} {A B C : List Node} {x y : Node}
    (hx : u ∈ came i x) (hy : u ∉ came i y) :
    removedScan i (A ++ x :: B ++ y :: C) [] = true := by
  have : A ++ x :: B ++ y :: C = (A ++ [x]) ++ (B ++ y :: C) := by simp
  rw [this, removedScan_append]
  simp only [Bool.or_eq_true]
  right
  refine removedScan_of_mem (u := u) ?_ (by simp) hy
  simp [hx]

theorem usesOf_mono {i : String} (L M : List Node) : usesOf i L ≤ usesOf i (L ++ M) := by
  unfold usesOf
  rw [← List.card_toFinset, ← List.card_toFinset]
  apply Finset.card_le_card
  intro a
  simp only [List.mem_toFinset, List.flatMap_append, List.mem_append]
  exact Or.inl

/-- **Closed stays closed.** If an invite isn't open at a roster head, it isn't open at any
later head of that roster, whatever home now says, as long as home never reopens it. -/
theorem closed_stays_closed {h₁ h₂ : Stmt} (hsuf : h₁ <:+ h₂) {inv inv' : InviteRec}
    (hid : inv'.id = inv.id) (hmax : inv'.maxUses = inv.maxUses)
    (hcl : inv.closed = true → inv'.closed = true) (h : inviteOpen h₁ inv = false) :
    inviteOpen h₂ inv' = false := by
  obtain ⟨pre, rfl⟩ := hsuf
  unfold inviteOpen at h ⊢
  rw [hid, hmax, List.reverse_append, removedScan_append]
  simp only [Bool.and_eq_false_iff, Bool.not_eq_eq_eq_not, decide_eq_false_iff_not,
    Nat.not_lt] at h ⊢
  rcases h with (h | h) | h
  · exact Or.inl (Or.inl (hcl h))
  · exact Or.inl (Or.inr (by simp [h]))
  · exact Or.inr (h.trans (usesOf_mono _ _))

/-- The result of answering a join request (§11.3). -/
inductive Admission where
  | granted | admitted | refused
deriving DecidableEq, Repr

/-- `asked`: the owner approved this very request by hand. -/
def admit (head : Stmt) (inv : InviteRec) (uid : String) (asked : Bool) : Admission :=
  if ((head.head?.map (fun x => x.body.uids.contains uid)).getD false) then .granted
  else if !inviteOpen head inv || (head.any (fun x => x.body.uids.contains uid) && !asked) then .refused
  else .admitted

/-- **A former member is never admitted without the owner being asked**, whatever the
invite's policy. -/
theorem former_member_needs_asking {head : Stmt} {inv : InviteRec} {uid : String}
    (hwas : ∃ x ∈ head, uid ∈ x.body.uids) :
    admit head inv uid false ≠ .admitted := by
  unfold admit
  split_ifs with h1 h2
  · simp
  · simp
  · simp only [Bool.or_eq_true, Bool.not_eq_eq_eq_not, Bool.not_true, not_or] at h2
    exact absurd hwas (by simpa using h2.2)

end Tandem
