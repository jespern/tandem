import TandemProofs.Roster

/-!
# Which writes count in an epoch (§8.3)

A data write names a roster statement and is signed by a device of its author, whom that
statement lists as owner or writer (only the owner for `_meta`). In the channel of the epoch that
starts with statement `E`, it is eligible iff its source epoch is exactly `E` and the policy
snapshot it was signed under extends the floor the statement pins for its author (§6.9), or it
is listed in `E`'s committed snapshot inventory and the statement it names is an ancestor of `E`.

"Extends" is the registry's relation between snapshot IDs (`Records.ext`). Both are immutable
signed objects, so it is a fixed function here; the registry itself is not modelled.

`inv` is the assembled inventory of `E` whose hash matched `E`'s commitment (§8.3), as a list
of (record ID, write ID); `none` while it is missing. The hash is collision-resistant and the
inventory bytes are canonical (`Encoding.lean`), so the commitment fixes it.

**Eligibility never changes**: given the statement it names and `E`, the verdict depends only
on signed statements, signed policy snapshots and the fixed inventory, so more statements never
change it (until the collection is dead, when processing stops).
-/

namespace Tandem

/-- Named records the rules single out (§7.3). -/
structure Records where
  metaRec : String
  named : List String
  /-- `ext a f`: policy snapshot `a` extends floor `f` (§6.9). -/
  ext : String → String → Bool

def roleOk (R : Records) (w : Write) : Bool :=
  match w.ref.roleOf w.author with
  | some r => if w.record == R.metaRec then r == .owner else r.canWrite
  | none => false

/-- What can be judged from the write and the statement it names alone. -/
def sourceValid (R : Records) (w : Write) : Bool :=
  decide (size w.ctx ≤ vectorLimit) && decide (size w.base ≤ vectorLimit) && roleOk R w &&
  decide (w.hlc < 2 ^ 64) &&
  !(w.op == .delete && R.named.contains w.record) && w.shapeOK

/-- The floor the statement a write names pins for its author. -/
def floorOf (w : Write) : Option String :=
  (w.ref.head? >>= fun x => x.body.members.find? (·.uid == w.author)).map (·.floor)

/-- Its policy snapshot extends that floor. -/
def floorOk (R : Records) (w : Write) : Bool :=
  match floorOf w with
  | some f => R.ext w.auth f
  | none => false

/-- The part of the verdict that needs `E` and the inventory. -/
def epochOk (R : Records) (w : Write) (E : Stmt) (inv : Option (List (String × String))) : Bool :=
  (epochStart w.ref == E && floorOk R w) ||
    match inv with
    | some l => decide (w.ref <:+ E) && l.contains (w.record, w.id)
    | none => false

def eligible (g : Node) (R : Records) (S : List Stmt) (w : Write) (E : Stmt)
    (inv : Option (List (String × String))) : Bool :=
  !dead g S && (accepted g S).contains E && (accepted g S).contains w.ref &&
    sourceValid R w && epochOk R w E inv

variable {g : Node} {R : Records}

theorem eligible_iff {S : List Stmt} {w : Write} {E : Stmt} {inv} :
    eligible g R S w E inv = true ↔ dead g S = false ∧ E ∈ accepted g S ∧ w.ref ∈ accepted g S ∧
      sourceValid R w = true ∧ epochOk R w E inv = true := by
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
      eligible g R U w E inv = (sourceValid R w && epochOk R w E inv) := by
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

/-- A delete with a field name, an element or a value is invalid (§9.2): every SDK signs a
delete the same way. -/
theorem malformed_delete_rejected {w : Write} (hop : w.op = .delete)
    (h : w.field ≠ "" ∨ w.elem ≠ "" ∨ w.value ≠ "") : sourceValid R w = false := by
  simp only [sourceValid, Write.shapeOK, hop]
  rcases h with h | h | h <;> simp [h]

/-- Matching a key or a counter is never enough: without the inventory, only writes whose
source epoch is exactly `E`, signed under a snapshot that extends the author's floor, count. -/
theorem without_inventory {w : Write} {E : Stmt} :
    epochOk R w E none = true ↔ epochStart w.ref = E ∧ floorOk R w = true := by
  simp [epochOk]

/-- **A device cut off by the floor writes nothing new** (§6.9, §8.3): a write signed under a
snapshot that doesn't extend the floor is not eligible directly. It can come into the epoch
only as a write the fixed inventory lists, from before the cut. -/
theorem below_floor_only_by_inventory {w : Write} {E : Stmt} {inv}
    (hf : floorOk R w = false) (h : epochOk R w E inv = true) :
    ∃ l, inv = some l ∧ w.ref <:+ E ∧ (w.record, w.id) ∈ l := by
  cases inv with
  | none => simp [epochOk, hf] at h
  | some l => simp [epochOk, hf] at h; exact ⟨l, rfl, h⟩

/-- A clock whose milliseconds reach 2^48 makes a write invalid (§9.1): the packed value is
then at least 2^64. -/
theorem clock_overflow_rejected {w : Write} (h : 2 ^ 64 ≤ w.hlc) : sourceValid R w = false := by
  simp [sourceValid, Nat.not_lt.2 h]

end Tandem
