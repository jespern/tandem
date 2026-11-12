import Mathlib.Data.List.Infix
import Mathlib.Data.List.Dedup
import Mathlib.Data.Finset.Card

/-!
# The executable model of DESIGN.md's collection rules

Everything here is computable: the oracle (`Oracle/Main.lean`) runs these very definitions,
and the theorems in the other modules are about them.

Cryptography is symbolic. A statement or write *is* its content; its ID stands for the
SHA-256 of that content, and we assume no collisions. A signature is the `signer`/`author`
name: whoever signed holds that key. A statement carries its whole ancestry (`Stmt` is a
list of nodes, nearest first), because its ID commits to its parent's ID, which commits to
the grandparent's, and so on: given an ID, the history behind it is fixed.
-/

set_option linter.unusedSectionVars false

namespace Tandem

/-! ## Roster statements (§8) -/

inductive Role where
  | owner | writer | reader
  /-- Anything else a statement might name. Never valid. -/
  | other (name : String)
deriving DecidableEq, Repr

namespace Role
def rank : Role → Nat
  | owner => 2 | writer => 1 | reader => 0 | other _ => 0
def known : Role → Bool
  | other _ => false | _ => true
def canWrite : Role → Bool
  | owner => true | writer => true | _ => false
def parse : String → Role
  | "owner" => owner | "writer" => writer | "reader" => reader | s => other s
end Role

structure Member where
  uid : String
  role : Role
  uka : String
  name : String
  via : Option String
deriving DecidableEq, Repr

/-- A statement's body (§8.1). `epoch` is the key counter `key.n`, `commit` the key commitment. -/
structure Body where
  n : Nat
  epoch : Nat
  commit : String
  snapshot : Option String
  members : List Member
  deleted : Bool
deriving DecidableEq, Repr

/-- One roster statement. `announced`: for a statement that starts an epoch, whether this
device has seen it in the previous epoch's channel (§11.6). Same-epoch statements ignore it. -/
structure Node where
  id : String
  parent : Option String
  signer : String
  body : Body
  announced : Bool
deriving DecidableEq, Repr

/-- A statement with its ancestors, nearest first. -/
abbrev Stmt := List Node

namespace Body
def roleOf (b : Body) (u : String) : Option Role := (b.members.find? (·.uid == u)).map (·.role)
def uids (b : Body) : List String := b.members.map (·.uid)
def owners (b : Body) : List String := (b.members.filter (·.role == .owner)).map (·.uid)
/-- One owner, the signer; only known roles; each member once (§8.2). -/
def wellFormed (b : Body) (signer : String) : Bool :=
  b.owners == [signer] && b.members.all (·.role.known) && b.uids.Nodup
end Body

def genesisOK (x : Node) : Bool :=
  x.parent.isNone && x.body.wellFormed x.signer && x.body.members.length == 1 &&
  x.body.n == 0 && x.body.epoch == 0 && x.body.snapshot.isNone && !x.body.deleted &&
  x.body.members.all (·.via.isNone)

/-- Whether `x` lowers or removes anyone `p` lists. -/
def reducing (p x : Node) : Bool :=
  p.body.members.any fun q => match x.body.roleOf q.uid with
    | none => true
    | some r => r.rank < q.role.rank

/-- §8.2: is `x` a valid successor of `p`? `commits`: the key commitments of `p`'s line.
This is the whole test. It looks at the parent and nothing else. -/
def stepOK (p : Node) (commits : List String) (x : Node) : Bool :=
  x.parent == some p.id && x.body.wellFormed x.signer && !p.body.deleted &&
  x.body.n == p.body.n + 1 &&
  (x.body.members.all fun m => p.body.members.all fun q =>
      m.uid != q.uid || (m.uka == q.uka && m.name == q.name && m.via == q.via)) &&
  (if x.body.commit != p.body.commit then
      x.body.epoch == p.body.epoch + 1 && x.body.snapshot.isSome &&
      x.body.members.all (fun m => p.body.uids.contains m.uid) && !commits.contains x.body.commit
    else
      !reducing p x && x.body.epoch == p.body.epoch && x.body.snapshot == p.body.snapshot)

/-- A statement that starts an epoch counts only once announced (§9.4, §11.6). -/
def counted (p x : Node) : Bool := x.body.commit == p.body.commit || x.announced

def commitsOf (s : Stmt) : List String := s.map (·.body.commit)

/-- The whole chain is valid, from the expected genesis `g`, every step signed by its owner. -/
def chainOK (g : Node) : Stmt → Bool
  | [] => false
  | [x] => x == g && genesisOK x
  | x :: p :: rest => chainOK g (p :: rest) && x.signer == g.signer &&
      stepOK p (commitsOf (p :: rest)) x && counted p x

/-- The device holds every ancestor. -/
def ancestorsIn (S : List Stmt) : Stmt → Bool
  | [] => true
  | [_] => true
  | _ :: t => S.contains t && ancestorsIn S t

/-- The statements a device holding `S` follows. -/
def accepted (g : Node) (S : List Stmt) : List Stmt :=
  S.filter fun s => chainOK g s && ancestorsIn S s

/-- Owner-signed, counted successors of accepted statements, valid or not (§9.4). -/
def isAuthenticated (g : Node) (S : List Stmt) : Stmt → Bool
  | x :: p :: rest => (accepted g S).contains (p :: rest) && x.signer == g.signer &&
      x.parent == some p.id && counted p x
  | _ => false

def authenticated (g : Node) (S : List Stmt) : List Stmt := S.filter (isAuthenticated g S)

/-- Two different owner-signed successors of one statement: the collection is dead. -/
def dead (g : Node) (S : List Stmt) : Bool :=
  (authenticated g S).any fun a => (authenticated g S).any fun b => a != b && a.tail == b.tail

def Stmt.node? (s : Stmt) : Option Node := s.head?
def Stmt.id (s : Stmt) : String := (s.head?.map (·.id)).getD ""
def Stmt.epochNum (s : Stmt) : Nat := (s.head?.map (·.body.epoch)).getD 0
def Stmt.roleOf (s : Stmt) (u : String) : Option Role := s.head? >>= (·.body.roleOf u)

/-- The statement that starts `s`'s epoch: back while the key commitment stays the same. -/
def epochStart : Stmt → Stmt
  | x :: p :: rest => if p.body.commit == x.body.commit then epochStart (p :: rest) else x :: p :: rest
  | s => s

/-- Accepted children of `s`. -/
def children (g : Node) (S : List Stmt) (s : Stmt) : List Stmt :=
  (accepted g S).filter fun c => c.tail == s

/-- Follow the one accepted successor while there is no conflict (as `OwnerLog.head`). -/
def headFrom (g : Node) (S : List Stmt) : Nat → Stmt → Stmt
  | 0, s => s
  | k + 1, s =>
    if (((authenticated g S).filter fun c => c.tail == s).eraseDups).length ≤ 1 then
      match children g S s with
      | c :: _ => headFrom g S k c
      | [] => s
    else s

def head (g : Node) (S : List Stmt) : Stmt := headFrom g S S.length [g]

/-! ## Version vectors and writes (§9) -/

/-- A version vector: device ↦ HLC. A device may appear more than once; the largest counts. -/
abbrev VV := List (String × Nat)

/-- `vle a b`: `b` covers `a`. -/
def vle (a b : VV) : Prop := ∀ p ∈ a, ∃ q ∈ b, q.1 = p.1 ∧ p.2 ≤ q.2

instance : DecidableRel vle := fun a b => by unfold vle; infer_instance

theorem vle_refl (a : VV) : vle a a := fun p hp => ⟨p, hp, rfl, le_rfl⟩

theorem vle_trans {a b c : VV} (h1 : vle a b) (h2 : vle b c) : vle a c := by
  intro p hp
  obtain ⟨q, hq, hq1, hq2⟩ := h1 p hp
  obtain ⟨t, ht, ht1, ht2⟩ := h2 q hq
  exact ⟨t, ht, ht1.trans hq1, hq2.trans ht2⟩

theorem vle_append_left (a b : VV) : vle a (a ++ b) :=
  fun p hp => ⟨p, List.mem_append_left _ hp, rfl, le_rfl⟩

theorem vle_append_right (a b : VV) : vle b (a ++ b) :=
  fun p hp => ⟨p, List.mem_append_right _ hp, rfl, le_rfl⟩

theorem vle_append {a b c : VV} (h1 : vle a c) (h2 : vle b c) : vle (a ++ b) c := by
  intro p hp
  rcases List.mem_append.1 hp with h | h
  · exact h1 p h
  · exact h2 p h

theorem vle_nil (a : VV) : vle [] a := fun _ h => absurd h List.not_mem_nil

/-- The devices a vector names. Its size on the wire (§5.5) is their number. -/
def devices (v : VV) : List String := (v.map Prod.fst).dedup
def size (v : VV) : Nat := (devices v).length

def vectorLimit : Nat := 1024

inductive Op where
  | assign | add | remove | delete
deriving DecidableEq, Repr

namespace Op
def parse : String → Option Op
  | "assign" => some assign | "add" => some add | "remove" => some remove
  | "delete" => some delete | _ => none
end Op

/-- A write (§9.1). `id` is the write ID: the oracle is given it, the proofs never need it
apart from ordering siblings for display (§9.2). -/
structure Write where
  id : String
  author : String
  ref : Stmt
  record : String
  field : String
  op : Op
  elem : String
  value : String
  dev : String
  hlc : Nat
  ctx : VV
  base : VV
deriving DecidableEq, Repr

namespace Write
def epoch (w : Write) : Nat := w.ref.epochNum
/-- `reach(w)`: its context joined with its own dot. -/
def reach (w : Write) : VV := w.ctx ++ [(w.dev, w.hlc)]

/-- The register a write belongs to. Field names are compared as their bytes (§9.1);
`String` equality is code-point equality, which is UTF-8 byte equality. Each set element is
its own register (§9.2). A delete is the record's own register: only a delete supersedes a
delete (see `Record.lean` for why this needs saying). -/
def slot (w : Write) : String × String × Nat × String :=
  match w.op with
  | .assign => (w.record, w.field, 0, "")
  | .add | .remove => (w.record, w.field, 1, w.elem)
  | .delete => (w.record, "", 2, "")
end Write

theorem vle_ctx_reach (w : Write) : vle w.ctx w.reach := vle_append_left _ _

/-- "`w` includes `x`": `w.context` covers `reach(x)` and `w.base` covers `x.base`. -/
def includes (w x : Write) : Prop := vle x.reach w.ctx ∧ vle x.base w.base

instance : DecidableRel includes := fun _ _ => by unfold includes; infer_instance

/-- §9.2 supersession: `sup w x` reads "`w` supersedes `x`". -/
def sup (w x : Write) : Prop :=
  w.slot = x.slot ∧
    ((x.epoch < w.epoch ∧ vle x.reach w.base ∧ vle x.base w.base) ∨
     (w.epoch = x.epoch ∧ includes w x ∧ ¬ includes x w))

instance : DecidableRel sup := fun _ _ => by unfold sup; infer_instance

end Tandem
