import Lean.Data.Json
import TandemProofs

/-!
# `tandem-oracle`: the proved model, as a test oracle

Reads one JSON request per line on stdin and answers each with one JSON line on stdout.
Every verdict comes from the definitions the theorems in `TandemProofs/` are about; this
file only parses, resolves IDs and prints. The format is in `ORACLE.md`.
-/

open Lean (Json)
open Tandem

namespace Oracle

def str? (j : Json) (k : String) : Option String :=
  match j.getObjVal? k with
  | .ok (.str s) => some s
  | _ => none

def strD (j : Json) (k : String) (d : String := "") : String := (str? j k).getD d

def nat? (j : Json) (k : String) : Option Nat := (j.getObjValAs? Nat k).toOption

def boolD (j : Json) (k : String) (d : Bool) : Bool := ((j.getObjValAs? Bool k).toOption).getD d

def arr (j : Json) (k : String) : Array Json :=
  match j.getObjVal? k with
  | .ok (.arr a) => a
  | _ => #[]

def strs (j : Json) (k : String) : List String :=
  (arr j k).toList.filterMap fun x => match x with | .str s => some s | _ => none

/-- A version vector, as `[[device, hlc], …]` or `{device: hlc}`. -/
def vv (j : Json) (k : String) : VV :=
  match j.getObjVal? k with
  | .ok (.arr a) => a.toList.filterMap fun e => match e with
      | .arr #[.str d, n] => (n.getNat?.toOption).map (d, ·)
      | _ => none
  | .ok (.obj kvs) => kvs.toList.filterMap fun (d, n) => (n.getNat?.toOption).map (d, ·)
  | _ => []

/-- Normal form for printing: largest value per device, sorted by device. -/
def normVV (v : VV) : VV :=
  let ds := (devices v).mergeSort (fun a b => decide (a ≤ b))
  ds.map fun d => (d, (v.filter (·.1 == d)).foldl (fun m p => max m p.2) 0)

def vvJson (v : VV) : Json := Json.arr ((normVV v).map fun (d, n) => Json.arr #[Json.str d, Json.num n]).toArray

def member (j : Json) : Member :=
  { uid := strD j "uid", role := Role.parse (strD j "role"), name := strD j "name",
    floor := strD j "floor" }

def node (j : Json) : Node :=
  { id := strD j "id", parent := str? j "parent", signer := strD j "signer",
    body := { n := (nat? j "n").getD 0, epoch := (nat? j "epoch").getD 0,
              commit := strD j "commit", snapshot := str? j "snapshot",
              members := (arr j "members").toList.map member, deleted := boolD j "deleted" false },
    announced := boolD j "announced" true }

/-- A statement's chain, from the nodes held. It stops where a parent isn't held: such a
statement waits, exactly as the model treats an incomplete chain. -/
def chainOf (nodes : List Node) : Nat → String → Stmt
  | 0, _ => []
  | k + 1, id =>
    match nodes.find? (·.id == id) with
    | none => []
    | some x => x :: match x.parent with
      | none => []
      | some p => chainOf nodes k p

def ids (l : List Stmt) : Json := Json.arr (l.map fun s => Json.str s.id).toArray

def writeOf (resolve : String → Stmt) (j : Json) : Write :=
  { id := strD j "id", author := strD j "author", ref := resolve (strD j "ref"),
    record := strD j "record", field := strD j "field",
    op := (Op.parse (strD j "op" "assign")).getD .assign, elem := strD j "elem",
    value := strD j "value", dev := strD j "dev", hlc := (nat? j "hlc").getD 0,
    ctx := vv j "ctx", base := vv j "base", auth := strD j "auth" }

def opName : Op → String
  | .assign => "assign" | .add => "add" | .remove => "remove" | .delete => "delete"

def writeJson (w : Write) : Json :=
  Json.mkObj [("author", Json.str w.author), ("ref", Json.str w.ref.id),
    ("record", Json.str w.record), ("field", Json.str w.field), ("op", Json.str (opName w.op)),
    ("elem", Json.str w.elem), ("value", Json.str w.value), ("dev", Json.str w.dev),
    ("hlc", Json.num w.hlc), ("ctx", vvJson w.ctx), ("base", vvJson w.base)]

def pairs (l : List (String × String)) : Json :=
  Json.arr (l.map fun (a, b) => Json.arr #[Json.str a, Json.str b]).toArray

def hexDigit (c : Char) : Option Nat :=
  if '0' ≤ c ∧ c ≤ '9' then some (c.toNat - '0'.toNat)
  else if 'a' ≤ c ∧ c ≤ 'f' then some (c.toNat - 'a'.toNat + 10)
  else none

def unhex : List Char → Option (List Enc.Byte)
  | [] => some []
  | a :: b :: rest => do
    let x ← hexDigit a
    let y ← hexDigit b
    let r ← unhex rest
    pure (⟨(x * 16 + y) % 256, Nat.mod_lt _ (by norm_num)⟩ :: r)
  | _ => none

def hex (l : List Enc.Byte) : String :=
  String.join (l.map fun b =>
    let s := (Nat.toDigits 16 b.val).asString
    if s.length < 2 then "0" ++ s else s)

/-- `{record: [writeID, …]}` as (record, write) pairs. -/
def invPairs (j : Json) : List (String × String) :=
  match j with
  | .obj kvs => kvs.toList.flatMap fun (r, ws) => match ws with
      | .arr a => a.toList.filterMap fun w => match w with | .str s => some (r, s) | _ => none
      | _ => []
  | _ => []

def handle (req : Json) : Except String Json := do
  -- a statement is its ID: duplicates are one statement
  let nodes := ((arr req "statements").toList.map node).foldl
    (fun acc x => if acc.any (·.id == x.id) then acc else acc ++ [x]) []
  let fuel := nodes.length + 1
  let chain := chainOf nodes fuel
  let S : List Stmt := nodes.map fun x => chain x.id
  let gId := strD req "genesis"
  let g ← match nodes.find? (·.id == gId) with
    | some x => pure x
    | none => throw s!"genesis {gId} not among the statements"
  let acc := accepted g S
  let hd := head g S
  let isDead := dead g S
  let auth := authenticated g S
  let conflicts := (auth.flatMap fun a => auth.filterMap fun b =>
      if a != b && a.tail == b.tail && decide (a.id < b.id) then some (a.id, b.id) else none)
  let epochs := acc.map fun s => (s.id, (epochStart s).id)
  let resolve (id : String) : Stmt := (acc.find? (·.id == id)).getD (chain id)
  let writes := (arr req "writes").toList.map (writeOf resolve)
  -- a snapshot extends a floor if it is that floor or the request says so
  let extends_ := (arr req "extends").toList.map fun p => (strD p "snapshot", strD p "floor")
  let R : Records := { metaRec := strD req "meta", named := strs req "named",
                       ext := fun a f => a == f || extends_.contains (a, f) }
  let E : Stmt := match str? req "channel" with
    | some id => chain id
    | none => epochStart hd
  let inv : Option (List (String × String)) := match req.getObjVal? "inventory" with
    | .ok .null => none
    | .ok j => some (invPairs j)
    | .error _ => none
  let held := writes.filter fun w => !(acc.contains w.ref)
  let elig := writes.filter fun w => eligible g R S w E inv
  let following := writes.filter fun w => acc.contains w.ref
  let supPairs := following.flatMap fun w => following.filterMap fun x =>
    if decide (sup w x) then some (w.id, x.id) else none
  -- the channel's state: eligible writes nothing eligible supersedes
  let K := kept sup elig.toFinset
  let keptL := elig.filter (· ∈ K)
  let records := (keptL.map (·.record)).eraseDups
  let recJson := records.map fun r =>
    let mine := keptL.filter (·.record == r)
    let fields := ((mine.filter (·.op == .assign)).map (·.field)).eraseDups.map fun f =>
      let sibs := mine.filter fun w => w.op == .assign && w.field == f
      (f, Json.mkObj [("kept", Json.arr (sibs.map fun w => Json.str w.id).toArray),
        ("display", match display sibs with | some w => Json.str w.id | none => Json.null)])
    let setFields := ((mine.filter fun w => w.op == .add || w.op == .remove).map (·.field)).eraseDups
    let sets := setFields.map fun f =>
      let elems := ((mine.filter fun w => (w.op == .add || w.op == .remove) && w.field == f).map
        (·.elem)).eraseDups
      (f, Json.arr ((elems.filter fun e => mine.any fun w =>
        w.op == .add && w.field == f && w.elem == e).map Json.str).toArray)
    (r, Json.mkObj [("deleted", Json.bool (mine.any (·.op == .delete))),
      ("fields", Json.mkObj fields), ("sets", Json.mkObj sets)])
  -- clean-up
  let byId (id : String) : Option Write := following.find? (·.id == id)
  let msgs := (arr req "messages").toList.map fun m =>
    (strD m "id", (nat? m "hash").getD 0, ((strs m "writes").filterMap byId).toFinset)
  let domPairs := msgs.flatMap fun (a, _, wa) => msgs.filterMap fun (b, _, wb) =>
    if a != b && decide (dominated sup wa wb) then some (a, b) else none
  let mdPairs := msgs.flatMap fun (a, ha, wa) => msgs.filterMap fun (b, hb, wb) =>
    if a != b && decide (mayDestroy sup wa wb ha hb) then some (a, b) else none
  -- signing again
  let reissued := (arr req "reissue").toList.map fun q =>
    let wid := strD q "write"
    let st := (strs q "state").filterMap fun id => writes.find? (·.id == id)
    match writes.find? (·.id == wid) with
    | none => (wid, Json.mkObj [("status", Json.str "unknown")])
    | some w =>
      if isDead || !acc.contains w.ref || !sourceValid R w then (wid, Json.mkObj [("status", Json.str "refused")])
      else match reissue hd w with
        | none => (wid, Json.mkObj [("status", Json.str "refused")])
        | some n => match reissueIn hd st w with
          | none => (wid, Json.mkObj [("status", Json.str "skip")])
          | some _ => (wid, Json.mkObj [("status", Json.str "new"), ("write", writeJson n)])
  -- memberships: the state of each asked-about user (§11.2)
  let kindOf : String → Option Kind
    | "a" => some .add | "n" => some .no | "l" => some .leave | _ => none
  let stateName : MState → String
    | .removed => "removed" | .waiting => "none" | .offered => "offered" | .active => "active"
    | .left => "left"
  let memberships := (arr req "memberships").toList.map fun q =>
    let u := strD q "uid"
    let answers : Answers := (arr q "answers").toList.filterMap fun a => match a with
      | .arr #[.str adm, .str k] => (kindOf k).map (adm, ·)
      | _ => none
    let heldAt := (strs q "held").map chain
    (u, Json.mkObj [("state", Json.str (stateName (mstate hd u answers heldAt))),
      ("admission", match admission u hd with | some a => Json.str a | none => Json.null)])
  let invBytes : List (String × Json) := match req.getObjVal? "inventoryBytes" with
    | .ok j =>
      let ps := (invPairs j).filterMap fun (r, w) => do
        let r' ← unhex r.toList
        let w' ← unhex w.toList
        pure (r', w')
      [("inventoryBytes", Json.str (hex (Enc.inventoryBytes (Enc.canon ps.toFinset))))]
    | .error _ => []
  pure <| Json.mkObj ([
    ("accepted", ids acc),
    ("head", Json.str hd.id),
    ("dead", Json.bool isDead),
    ("conflicts", pairs conflicts),
    ("epochs", Json.mkObj (epochs.map fun (a, b) => (a, Json.str b))),
    ("channel", Json.str E.id),
    ("held", Json.arr (held.map fun w => Json.str w.id).toArray),
    ("eligible", Json.arr (elig.map fun w => Json.str w.id).toArray),
    ("supersedes", pairs supPairs),
    ("state", Json.mkObj recJson),
    ("dominated", pairs domPairs),
    ("mayDestroy", pairs mdPairs),
    ("reissue", Json.mkObj reissued),
    ("memberships", Json.mkObj memberships)] ++ invBytes)

partial def loop (stdin : IO.FS.Stream) (stdout : IO.FS.Stream) : IO Unit := do
  let line ← stdin.getLine
  if line.isEmpty then return
  let trimmed := line.trimAscii.toString
  unless trimmed.isEmpty do
    let out := match Json.parse trimmed with
      | .error e => Json.mkObj [("error", Json.str s!"bad JSON: {e}")]
      | .ok req => match handle req with
        | .ok j => j
        | .error e => Json.mkObj [("error", Json.str e)]
    stdout.putStrLn out.compress
    stdout.flush
  loop stdin stdout

end Oracle

def main : IO Unit := do
  Oracle.loop (← IO.getStdin) (← IO.getStdout)
