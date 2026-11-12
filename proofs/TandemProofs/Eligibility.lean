import TandemProofs.Roster

/-!
# Which writes count in an epoch (§8.3)

A data write names a roster statement and is signed by its author, whom that statement lists
as owner or writer (only the owner for `_meta`). In the channel of the epoch that starts with
statement `E`, it is eligible iff its source epoch is exactly `E`, or it is listed in `E`'s
committed snapshot inventory and the statement it names is an ancestor of `E`.

`inv` is the assembled inventory of `E` whose hash matched `E`'s commitment (§8.3), as a list
of (record ID, write ID); `none` while it is missing. The hash is collision-resistant and the
inventory bytes are canonical (`Encoding.lean`), so the commitment fixes it.

**Eligibility never changes**: given the statement it names and `E`, the verdict depends only
on signed statements and the fixed inventory, so more statements never change it (until the
collection is dead, when processing stops).
-/

namespace Tandem

/-- Named records the rules single out (§7.3). -/
structure Records where
  metaRec : String
  named : List String

def roleOk (R : Records) (w : Write) : Bool :=
  match w.ref.roleOf w.author with
  | some r => if w.record == R.metaRec then r == .owner else r.canWrite
  | none => false

/-- What can be judged from the write and the statement it names alone. -/
def sourceValid (R : Records) (w : Write) : Bool :=
  decide (size w.ctx ≤ vectorLimit) && decide (size w.base ≤ vectorLimit) && roleOk R w &&
  !(w.op == .delete && R.named.contains w.record)

/-- The part of the verdict that needs `E` and the inventory. -/
def epochOk (w : Write) (E : Stmt) (inv : Option (List (String × String))) : Bool :=
  epochStart w.ref == E ||
    match inv with
    | some l => decide (w.ref <:+ E) && l.contains (w.record, w.id)
    | none => false

def eligible (g : Node) (R : Records) (S : List Stmt) (w : Write) (E : Stmt)
    (inv : Option (List (String × String))) : Bool :=
  !dead g S && (accepted g S).contains E && (accepted g S).contains w.ref &&
    sourceValid R w && epochOk w E inv

variable {g : Node} {R : Records}

theorem eligible_iff {S : List Stmt} {w : Write} {E : Stmt} {inv} :
    eligible g R S w E inv = true ↔ dead g S = false ∧ E ∈ accepted g S ∧ w.ref ∈ accepted g S ∧
      sourceValid R w = true ∧ epochOk w E inv = true := by
  simp [eligible, and_assoc]

/-- More statements never take eligibility away, as long as the collection is alive. -/
theorem eligible_mono {S T : List Stmt} (hST : S ⊆ T) (halive : dead g T = false)
    {w : Write} {E : Stmt} {inv} (h : eligible g R S w E inv = true) :
    eligible g R T w E inv = true := by
  rw [eligible_iff] at h ⊢
  exact ⟨halive, accepted_mono hST h.2.1, accepted_mono hST h.2.2.1, h.2.2.2⟩

/-- **Eligibility never changes**: once a device follows the statement a write names and the
epoch start `E`, the verdict is the same for every larger set of statements it may come to
hold while the collection is alive. -/
theorem eligible_stable {S T : List Stmt} (hST : S ⊆ T) (halive : dead g T = false)
    {w : Write} {E : Stmt} {inv} (hE : E ∈ accepted g S) (hw : w.ref ∈ accepted g S) :
    eligible g R S w E inv = eligible g R T w E inv := by
  have haliveS : dead g S = false := by
    cases h : dead g S
    · rfl
    · rw [dead_mono hST h] at halive; exact halive
  have key : ∀ U : List Stmt, dead g U = false → E ∈ accepted g U → w.ref ∈ accepted g U →
      eligible g R U w E inv = (sourceValid R w && epochOk w E inv) := by
    intro U h1 h2 h3
    simp [eligible, h1, h2, h3]
  rw [key S haliveS hE hw, key T halive (accepted_mono hST hE) (accepted_mono hST hw)]

/-- Readers never write. -/
theorem reader_rejected {w : Write} (h : w.ref.roleOf w.author = some .reader) :
    sourceValid R w = false := by
  simp [sourceValid, roleOk, h, Role.canWrite]

/-- Only the owner writes the collection's name (`_meta`). -/
theorem meta_owner_only {w : Write} (hm : w.record = R.metaRec) (r : Role) (hr : r ≠ .owner)
    (h : w.ref.roleOf w.author = some r) : sourceValid R w = false := by
  simp [sourceValid, roleOk, h, hm, hr]

/-- A delete on a named record is invalid (§7.3). -/
theorem named_delete_rejected {w : Write} (hop : w.op = .delete) (hn : w.record ∈ R.named) :
    sourceValid R w = false := by
  simp [sourceValid, hop, hn]

/-- Matching a key or a counter is never enough: without the inventory, only writes whose
source epoch is exactly `E` count. -/
theorem without_inventory {w : Write} {E : Stmt} :
    epochOk w E none = true ↔ epochStart w.ref = E := by
  simp [epochOk]

end Tandem
