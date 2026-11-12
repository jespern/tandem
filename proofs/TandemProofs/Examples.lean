import TandemProofs.Invite

/-!
# The regression tests of `model/owner_model.py`, as Lean examples

Where `model/test_owner_model.py` checks a rule the Lean model covers, the same situation is
built here and checked by `decide`: the kernel evaluates the model's own definitions. Tests
of the device and hub simulation (journalled rotation, grants, finishing another device's
change) are about the protocol's procedures, which the Lean model doesn't cover; the
differential test (`difftest.py`) compares the two models on random histories instead.
-/

namespace Tandem.Examples

def m (u : String) (r : Role) (via : Option String := none) : Member := ⟨u, r, "uka-" ++ u, u, via⟩

def mk (id : String) (parent : Option Node) (signer : String) (n epoch : Nat) (commit : String)
    (snapshot : Option String) (members : List Member) (deleted : Bool := false)
    (announced : Bool := true) : Node :=
  ⟨id, parent.map (·.id), signer, ⟨n, epoch, commit, snapshot, members, deleted⟩, announced⟩

def g : Node := mk "g" none "O" 0 0 "K0" none [m "O" .owner]
/-- `family()`: the owner adds a writer W and a reader R. -/
def fam : Node := mk "fam" g "O" 1 0 "K0" none [m "O" .owner, m "W" .writer, m "R" .reader]
def famC : Stmt := [fam, g]
def base : List Stmt := [[g], famC]

/-- A successor of `fam` with these members, signer, key. -/
def next (id signer : String) (ms : List Member) (commit : String := "K0") (epoch : Nat := 0)
    (snap : Option String := none) (deleted : Bool := false) (announced : Bool := true) : Stmt :=
  [mk id fam signer 2 epoch commit snap ms deleted announced, fam, g]

def demoteW := [m "O" .owner, m "W" .reader, m "R" .reader]
def dropR := [m "O" .owner, m "W" .writer]

def follows (S : List Stmt) (s : Stmt) : Bool := (accepted g S).contains s

-- test_only_the_owners_key_signs_a_roster
example : follows (base ++ [next "x" "W" demoteW "K1" 1 "h"]) (next "x" "W" demoteW "K1" 1 "h") = false ∧
    dead g (base ++ [next "x" "W" demoteW "K1" 1 "h"]) = false := by decide
example : follows (base ++ [next "x" "O" demoteW "K1" 1 "h"]) (next "x" "O" demoteW "K1" 1 "h") = true := by
  decide

-- test_there_are_three_roles_and_the_owner_never_changes
example : ∀ ms ∈ [[m "O" .owner, m "W" (.other "admin"), m "R" .reader],
    [m "O" .owner, m "W" .owner, m "R" .reader], [m "O" .writer, m "W" .owner, m "R" .reader],
    [m "W" .writer, m "R" .reader]],
    follows (base ++ [next "x" "O" ms]) (next "x" "O" ms) = false ∧
    follows (base ++ [next "x" "O" ms "K1" 1 "h"]) (next "x" "O" ms "K1" 1 "h") = false := by decide

-- test_removal_and_demotion_require_a_new_key; additions and promotions don't
example : follows (base ++ [next "x" "O" dropR]) (next "x" "O" dropR) = false ∧
    follows (base ++ [next "x" "O" demoteW]) (next "x" "O" demoteW) = false ∧
    follows (base ++ [next "x" "O" dropR "K1" 1 "h"]) (next "x" "O" dropR "K1" 1 "h") = true ∧
    follows (base ++ [next "x" "O" [m "O" .owner, m "W" .writer, m "R" .writer, m "J" .reader]])
      (next "x" "O" [m "O" .owner, m "W" .writer, m "R" .writer, m "J" .reader]) = true := by decide

-- test_no_admission_in_an_epoch_start
example : let s := next "x" "O" [m "O" .owner, m "W" .writer, m "R" .reader, m "J" .writer] "K1" 1 "h"
    follows (base ++ [s]) s = false := by decide

-- test_a_key_is_never_used_for_two_epochs
def rot1 : Stmt := next "r1" "O" dropR "K1" 1 "h"
def back : Stmt := [mk "r2" (some (mk "r1" fam "O" 2 1 "K1" "h" dropR)) "O" 3 2 "K0" "h2"
  [m "O" .owner, m "W" .reader]] ++ rot1
example : follows (base ++ [rot1, back]) rot1 = true ∧ follows (base ++ [rot1, back]) back = false := by
  decide

-- test_two_successors_kill_the_collection_in_every_delivery_order
def first : Stmt := next "a" "O" demoteW "K1" 1 "h"
def second : Stmt := next "b" "O" dropR "K2" 1 "h"
example : dead g (base ++ [first, second]) = true ∧ dead g ([second] ++ base ++ [first]) = true ∧
    head g (base ++ [first, second]) = famC := by decide

-- test_an_invalid_statement_signed_by_the_owner_still_counts_as_a_successor
example : let bad := next "bad" "O" [m "O" .owner, m "W" .owner, m "R" .reader]
    follows (base ++ [first, bad]) bad = false ∧ dead g (base ++ [first, bad]) = true := by decide

-- a draft (an epoch start not yet announced) is not half of a conflict (§9.4)
example : dead g (base ++ [first, next "b" "O" dropR "K2" 1 "h" (announced := false)]) = false := by
  decide

-- test_how_a_member_came_in_cannot_be_rewritten
def joinG : Node := mk "j" fam "O" 2 0 "K0" none
  [m "O" .owner, m "W" .writer, m "R" .reader, m "G" .reader "party"]
def afterJoin (id : String) (ms : List Member) : Stmt := [mk id joinG "O" 3 0 "K0" none ms, joinG, fam, g]
example : ∀ via ∈ [some "other", none],
    let s := afterJoin "x" [m "O" .owner, m "W" .writer, m "R" .reader, m "G" .reader via]
    follows (base ++ [[joinG, fam, g], s]) s = false := by decide

-- test_a_deleted_collection_has_no_successors
def del : Node := mk "del" fam "O" 2 0 "K0" none [m "O" .owner, m "W" .writer, m "R" .reader] true
example : let s : Stmt := [mk "x" del "O" 3 0 "K0" none
      [m "O" .owner, m "W" .writer, m "R" .reader, m "J" .reader], del, fam, g]
    follows (base ++ [[del, fam, g], s]) [del, fam, g] = true ∧ follows (base ++ [[del, fam, g], s]) s = false := by
  decide

/-! ## Data -/

def R0 : Records := { metaRec := "meta", named := ["meta"] }

def wr (id author : String) (ref : Stmt) (rec : String := "item") (field : String := "v")
    (hlc : Nat := 1) (ctx base : VV := []) (op : Op := .assign) : Write :=
  ⟨id, author, ref, rec, field, op, "", "x", "device", hlc, ctx, base⟩

-- test_readers_and_strangers_cannot_write
example : eligible g R0 base (wr "1" "R" famC) [g] none = false ∧
    eligible g R0 base (wr "2" "X" famC) [g] none = false ∧
    eligible g R0 base (wr "3" "W" famC) [g] none = true := by decide

-- test_demotion_cannot_be_bypassed_by_copying_the_new_key
def demotion : Stmt := next "dem" "O" demoteW "K1" 1 "h"
def fake : Stmt := next "fake" "W" [m "O" .owner, m "W" .writer, m "R" .reader] "K1" 1 "h"
example : let S := base ++ [demotion, fake]
    follows S fake = false ∧ eligible g R0 S (wr "a" "W" fake) demotion none = false ∧
      eligible g R0 S (wr "b" "W" famC) demotion (some []) = false ∧ dead g S = false := by decide

-- test_exact_epoch_id_is_required_not_just_its_key
example : eligible g R0 base (wr "3" "W" famC) [g] none = true ∧
    eligible g R0 base (wr "3" "W" famC) famC none = false := by decide

-- test_snapshot_is_fixed_by_the_signed_inventory
def old : Write := wr "old" "W" famC
def removal : Stmt := next "rm" "O" [m "O" .owner, m "R" .reader] "K1" 1 "h"
example : let S := base ++ [removal]
    eligible g R0 S old removal (some [("item", "old")]) = true ∧
      eligible g R0 S (wr "late" "W" famC (hlc := 2)) removal (some [("item", "old")]) = false := by
  decide

-- test_reissue_uses_first_authorizing_statement_after_promotion; same bytes on every device
def promo : Stmt := [mk "pro" (some (mk "dem" fam "O" 2 1 "K1" "h" demoteW)) "O" 3 1 "K1" "h"
  [m "O" .owner, m "W" .writer, m "R" .reader]] ++ demotion
def promo2 : Stmt := [mk "pro2" (some (promo.head?.getD g)) "O" 4 1 "K1" "h"
  [m "O" .owner, m "W" .writer, m "R" .writer]] ++ promo
example : (reissue promo old).map (·.ref) = some promo ∧ reissue promo old = reissue promo2 old ∧
    ((reissue promo old).map (·.base)) = some [("device", 1)] := by decide

-- test_current_readers_cannot_reissue
example : reissue demotion old = none := by decide

-- test_a_write_the_new_state_holds_or_supersedes_is_not_signed_again
example : ∀ n ∈ reissue promo old,
    reissueIn promo [old] old = none ∧ reissueIn promo [{ n with id := "n" }] old = none ∧
      reissueIn promo [] old = some n := by decide

-- test_field_names_are_the_same_only_if_their_bytes_are
example : ¬ sup (wr "b" "W" famC (field := "é") (hlc := 2) (ctx := [("device", 1)]))
      (wr "a" "W" famC (field := "é")) ∧
    sup (wr "b" "W" famC (field := "é") (hlc := 2) (ctx := [("device", 1)]))
      (wr "a" "W" famC (field := "é")) := by decide

-- test_only_the_owner_writes_the_collections_name
example : eligible g R0 base (wr "n" "W" famC "meta") [g] none = false ∧
    eligible g R0 base (wr "n" "O" famC "meta") [g] none = true ∧
    eligible g R0 base (wr "d" "O" famC "meta" (op := .delete)) [g] none = false := by decide

-- test_oversized_vectors_are_invalid (evaluated, not decided: the kernel is slow on 1,025 entries)
#guard eligible g R0 base (wr "big" "W" famC (ctx := (List.range 1025).map fun i => (toString i, 1)))
    [g] none = false
#guard eligible g R0 base (wr "fits" "W" famC (ctx := (List.range 1024).map fun i => (toString i, 1)))
    [g] none = true

/-! ## Invites -/

def inv : InviteRec := { id := "party", closed := false, maxUses := 5 }
def joined : Stmt := [joinG, fam, g]
def gone : Stmt := [mk "gone" joinG "O" 3 1 "K1" "h" [m "O" .owner, m "W" .writer, m "R" .reader]] ++ joined
def back2 : Stmt := [mk "back" (gone.head?.getD g) "O" 4 1 "K1" "h"
  [m "O" .owner, m "W" .writer, m "R" .reader, m "G" .reader "other"]] ++ gone

-- test_removing_a_member_closes_their_invite_without_a_second_write
example : inviteOpen joined inv = true ∧ inviteOpen gone inv = false := by decide

-- test_an_invite_stays_closed_when_the_removed_member_comes_back_another_way
example : inviteOpen back2 inv = false ∧ admit gone inv "G" false = .refused := by decide

end Tandem.Examples
