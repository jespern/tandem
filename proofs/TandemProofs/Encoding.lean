import Mathlib.Data.List.Lex
import Mathlib.Data.Finset.Sort
import Mathlib.Data.List.Infix
import Mathlib.Tactic.Ring

/-!
# Encodings (§5.2, §5.5, §5.6, §8.3)

* `sigInput` is injective: two different writes never produce the same signed bytes, so a
  signature and a write ID name exactly one write (`sigInput_inj`).
* The `TANDEM-…-V2` prefixes, and the `tandem/v2/…` labels hashed in front of data, are
  pairwise prefix-free, so input for one kind of object is never input for another
  (`signed_domains_separate`, `hash_domains_separate`); the HKDF `info` labels are distinct
  for each key they derive from (`kdf_labels_distinct`).
* The canonical inventory bytes are a function of the *set* of (record, write) pairs and
  determine it: its hash commits to exactly one set (`inventory_bytes_inj`).
-/

namespace Tandem.Enc

abbrev Byte := Fin 256

/-- `k` bytes, big-endian. -/
def be : Nat → Nat → List Byte
  | 0, _ => []
  | k + 1, n => be k (n / 256) ++ [⟨n % 256, Nat.mod_lt _ (by norm_num)⟩]

theorem be_length (k n : Nat) : (be k n).length = k := by
  induction k generalizing n with
  | zero => rfl
  | succ k ih => simp [be, ih]

theorem be_inj {k n m : Nat} (hn : n < 256 ^ k) (hm : m < 256 ^ k) (h : be k n = be k m) :
    n = m := by
  induction k generalizing n m with
  | zero => simp at hn hm; omega
  | succ k ih =>
    simp only [be] at h
    obtain ⟨h1, h2⟩ := List.append_inj h (by simp [be_length])
    simp only [List.cons.injEq, Fin.mk.injEq, and_true] at h2
    have hn' : n / 256 < 256 ^ k := by rw [Nat.div_lt_iff_lt_mul (by norm_num)]; rwa [pow_succ] at hn
    have hm' : m / 256 < 256 ^ k := by rw [Nat.div_lt_iff_lt_mul (by norm_num)]; rwa [pow_succ] at hm
    have := ih hn' hm' h1
    rw [← Nat.div_add_mod n 256, ← Nat.div_add_mod m 256, this, h2]

/-! ## Self-delimiting codecs -/

/-- `enc` is self-delimiting on well-formed values: an encoding followed by anything can be
taken apart in one way only. -/
def SelfDelim {α : Type*} (wf : α → Prop) (enc : α → List Byte) : Prop :=
  ∀ a b r s, wf a → wf b → enc a ++ r = enc b ++ s → a = b ∧ r = s

theorem selfDelim_fixed {α : Type*} {wf : α → Prop} {enc : α → List Byte} (n : Nat)
    (hlen : ∀ a, wf a → (enc a).length = n) (hinj : ∀ a b, wf a → wf b → enc a = enc b → a = b) :
    SelfDelim wf enc := by
  intro a b r s ha hb h
  obtain ⟨h1, h2⟩ := List.append_inj h (by rw [hlen a ha, hlen b hb])
  exact ⟨hinj a b ha hb h1, h2⟩

theorem selfDelim_flatMap {α : Type*} {wf : α → Prop} {enc : α → List Byte}
    (hd : SelfDelim wf enc) {l l' : List α} {r s : List Byte} (hl : ∀ a ∈ l, wf a)
    (hl' : ∀ a ∈ l', wf a) (hlen : l.length = l'.length)
    (h : l.flatMap enc ++ r = l'.flatMap enc ++ s) : l = l' ∧ r = s := by
  induction l generalizing l' with
  | nil =>
    match l', hlen with
    | [], _ => simpa using h
  | cons a l ih =>
    match l', hlen with
    | b :: l', hlen =>
      simp only [List.flatMap_cons, List.append_assoc] at h
      obtain ⟨rfl, h2⟩ := hd a b _ _ (hl a (by simp)) (hl' b (by simp)) h
      obtain ⟨rfl, rfl⟩ := ih (fun x hx => hl x (by simp [hx])) (fun x hx => hl' x (by simp [hx]))
        (by simpa using hlen) h2
      exact ⟨rfl, rfl⟩

/-- A list prefixed with its length in `k` bytes. -/
def counted {α : Type*} (k : Nat) (enc : α → List Byte) (l : List α) : List Byte :=
  be k l.length ++ l.flatMap enc

theorem selfDelim_counted {α : Type*} {wf : α → Prop} {enc : α → List Byte} (k : Nat)
    (hd : SelfDelim wf enc) :
    SelfDelim (fun l : List α => l.length < 256 ^ k ∧ ∀ a ∈ l, wf a) (counted k enc) := by
  intro l l' r s hl hl' h
  simp only [counted, List.append_assoc] at h
  obtain ⟨h1, h2⟩ := List.append_inj h (by simp [be_length])
  have hlen := be_inj hl.1 hl'.1 h1
  exact selfDelim_flatMap hd hl.2 hl'.2 hlen h2

/-- Bytes with their length in front: `lp(x) = u32(len(x)) || x` (§5.2). -/
def lp (x : List Byte) : List Byte := be 4 x.length ++ x

theorem selfDelim_lp : SelfDelim (fun x : List Byte => x.length < 256 ^ 4) lp := by
  intro x y r s hx hy h
  simp only [lp, List.append_assoc] at h
  obtain ⟨h1, h2⟩ := List.append_inj h (by simp [be_length])
  exact List.append_inj h2 (be_inj hx hy h1)

theorem selfDelim_bytes (n : Nat) : SelfDelim (fun x : List Byte => x.length = n) id :=
  selfDelim_fixed n (fun _ h => h) (fun _ _ _ _ h => h)

theorem selfDelim_be (k : Nat) : SelfDelim (fun n : Nat => n < 256 ^ k) (be k) :=
  selfDelim_fixed k (fun n _ => be_length k n) (fun _ _ ha hb h => be_inj ha hb h)

/-- Two self-delimiting codecs in a row. -/
theorem selfDelim_pair {α β : Type*} {wa : α → Prop} {wb : β → Prop} {ea : α → List Byte}
    {eb : β → List Byte} (ha : SelfDelim wa ea) (hb : SelfDelim wb eb) :
    SelfDelim (fun p : α × β => wa p.1 ∧ wb p.2) (fun p => ea p.1 ++ eb p.2) := by
  rintro ⟨a, b⟩ ⟨a', b'⟩ r s ⟨h1, h2⟩ ⟨h1', h2'⟩ h
  simp only [List.append_assoc] at h
  obtain ⟨rfl, h'⟩ := ha a a' _ _ h1 h1' h
  obtain ⟨rfl, rfl⟩ := hb b b' r s h2 h2' h'
  exact ⟨rfl, rfl⟩

/-! ## `sigInput` (§5.5) -/

/-- One version-vector entry: device ID (8), millis (u64), counter (u16). -/
structure Entry where
  dev : List Byte
  millis : Nat
  counter : Nat
deriving DecidableEq

def Entry.wf (e : Entry) : Prop := e.dev.length = 8 ∧ e.millis < 256 ^ 8 ∧ e.counter < 256 ^ 2
def Entry.bytes (e : Entry) : List Byte := e.dev ++ be 8 e.millis ++ be 2 e.counter

theorem selfDelim_entry : SelfDelim Entry.wf Entry.bytes := by
  rintro ⟨d, m, c⟩ ⟨d', m', c'⟩ r s ⟨h1, h2, h3⟩ ⟨h1', h2', h3'⟩ h
  simp only [Entry.bytes, List.append_assoc] at h
  obtain ⟨rfl, k1⟩ := selfDelim_bytes 8 d d' _ _ h1 h1' h
  obtain ⟨rfl, k2⟩ := selfDelim_be 8 m m' _ _ h2 h2' k1
  obtain ⟨rfl, rfl⟩ := selfDelim_be 2 c c' r s h3 h3' k2
  exact ⟨rfl, rfl⟩

/-- `vv(x) = u16(n) || n × entry`. -/
def vv (v : List Entry) : List Byte := counted 2 Entry.bytes v

/-- The fields a write signs. `vhash` is `H(value)`. -/
structure SigFields where
  hub : List Byte
  scope : List Byte
  record : List Byte
  field : List Byte
  op : Byte
  elem : List Byte
  vhash : List Byte
  author : List Byte
  cert : List Byte
  snap : List Byte
  dev : List Byte
  millis : Nat
  counter : Nat
  ref : List Byte
  ctx : List Entry
  base : List Entry
deriving DecidableEq

def SigFields.wf (f : SigFields) : Prop :=
  f.hub.length = 32 ∧ f.scope.length = 16 ∧ f.record.length = 16 ∧ f.field.length < 256 ^ 4 ∧
  f.elem.length < 256 ^ 4 ∧ f.vhash.length = 32 ∧ f.author.length = 32 ∧ f.cert.length = 32 ∧
  f.snap.length = 32 ∧ f.dev.length = 8 ∧
  f.millis < 256 ^ 8 ∧ f.counter < 256 ^ 2 ∧ f.ref.length = 32 ∧
  (f.ctx.length < 256 ^ 2 ∧ ∀ e ∈ f.ctx, e.wf) ∧ (f.base.length < 256 ^ 2 ∧ ∀ e ∈ f.base, e.wf)

def asciiBytes (s : String) : List Byte := s.toList.map fun c => ⟨c.toNat % 256, Nat.mod_lt _ (by norm_num)⟩

def writeTag : List Byte := asciiBytes "TANDEM-WRITE-V2"

/-- §5.5. -/
def sigInput (f : SigFields) : List Byte :=
  writeTag ++ f.hub ++ f.scope ++ f.record ++ lp f.field ++ [f.op] ++ lp f.elem ++ f.vhash ++
    f.author ++ f.cert ++ f.snap ++ f.dev ++ be 8 f.millis ++ be 2 f.counter ++ f.ref ++ vv f.ctx ++
    vv f.base

/-- **`sigInput` is injective** on well-formed fields: no two writes sign the same bytes. -/
theorem sigInput_inj {f g : SigFields} (hf : f.wf) (hg : g.wf) (h : sigInput f = sigInput g) :
    f = g := by
  obtain ⟨f0, f1, f2, f3, f4, f5, f6, f6a, f6b, f7, f8, f9, f10, f11, f12⟩ := hf
  obtain ⟨g0, g1, g2, g3, g4, g5, g6, g6a, g6b, g7, g8, g9, g10, g11, g12⟩ := hg
  simp only [sigInput, vv, List.append_assoc, List.append_cancel_left_eq] at h
  obtain ⟨e0, h⟩ := selfDelim_bytes 32 _ _ _ _ f0 g0 h
  obtain ⟨e1, h⟩ := selfDelim_bytes 16 _ _ _ _ f1 g1 h
  obtain ⟨e2, h⟩ := selfDelim_bytes 16 _ _ _ _ f2 g2 h
  obtain ⟨e3, h⟩ := selfDelim_lp _ _ _ _ f3 g3 h
  simp only [List.singleton_append, List.cons.injEq] at h
  obtain ⟨e4, h⟩ := h
  obtain ⟨e5, h⟩ := selfDelim_lp _ _ _ _ f4 g4 h
  obtain ⟨e6, h⟩ := selfDelim_bytes 32 _ _ _ _ f5 g5 h
  obtain ⟨e7, h⟩ := selfDelim_bytes 32 _ _ _ _ f6 g6 h
  obtain ⟨e7a, h⟩ := selfDelim_bytes 32 _ _ _ _ f6a g6a h
  obtain ⟨e7b, h⟩ := selfDelim_bytes 32 _ _ _ _ f6b g6b h
  obtain ⟨e8, h⟩ := selfDelim_bytes 8 _ _ _ _ f7 g7 h
  obtain ⟨e9, h⟩ := selfDelim_be 8 _ _ _ _ f8 g8 h
  obtain ⟨e10, h⟩ := selfDelim_be 2 _ _ _ _ f9 g9 h
  obtain ⟨e11, h⟩ := selfDelim_bytes 32 _ _ _ _ f10 g10 h
  obtain ⟨e12, h⟩ := selfDelim_counted 2 selfDelim_entry _ _ _ _ f11 g11 h
  obtain ⟨e13, -⟩ := selfDelim_counted 2 selfDelim_entry _ _ [] [] f12 g12 (by simpa using h)
  cases f; cases g
  simp_all

/-! ## Domain separation -/

/-- Prefixes of signed inputs and AEAD associated data (§5.5, §6.7, §10.8). -/
def signedTags : List String :=
  ["TANDEM-WRITE-V2", "TANDEM-ADMIN-CAPSULE-V2",
   "TANDEM-GENESIS-V2", "TANDEM-CAPSULE-V2", "TANDEM-REQUEST-V2", "TANDEM-CERT-V2",
   "TANDEM-REVOKE-V2", "TANDEM-INVITE-V2", "TANDEM-DELEGATION-V2", "TANDEM-RECEIPT-V2",
   "TANDEM-HOME-EPOCH-V2", "TANDEM-GRANT-V2", "TANDEM-KEYREQ-V2", "TANDEM-LEAVE-V2",
   "TANDEM-ANSWER-V2", "TANDEM-PAIR-PACKAGE-V2", "TANDEM-PAIR-GATE-V2", "TANDEM-HOME-RECOVERY-V2"]

/-- Labels hashed in front of data: key IDs, key commitments, named record IDs, home and
directory scopes, participant registrations and authorization snapshots (§5.4, §5.7, §7.3, §6.2,
§10.8). Every object ID hashes its signed input, so the signed tags join them. -/
def hashTags : List String :=
  ["TANDEM-WRITE-V2", "tandem/v2/kid", "tandem/v2/commit", "tandem/v2/named", "tandem/v2/home",
   "tandem/v2/directory", "tandem/v2/auth-snapshot", "TANDEM-REGISTRATION-V2",
   "TANDEM-GENESIS-V2", "TANDEM-CAPSULE-V2", "TANDEM-REQUEST-V2", "TANDEM-CERT-V2",
   "TANDEM-REVOKE-V2", "TANDEM-INVITE-V2", "TANDEM-DELEGATION-V2", "TANDEM-RECEIPT-V2",
   "TANDEM-HOME-EPOCH-V2", "TANDEM-GRANT-V2", "TANDEM-KEYREQ-V2", "TANDEM-LEAVE-V2",
   "TANDEM-ANSWER-V2"]

def PrefixFree (l : List String) : Prop :=
  ∀ a ∈ l, ∀ b ∈ l, a ≠ b → ¬ a.toList <+: b.toList

instance (l : List String) : Decidable (PrefixFree l) := by unfold PrefixFree; infer_instance

theorem signedTags_prefixFree : PrefixFree signedTags := by decide
theorem hashTags_prefixFree : PrefixFree hashTags := by decide

/-- Inputs that start with two labels neither of which is a prefix of the other differ. -/
theorem separate {a b x y : List Char} (hab : ¬ a <+: b) (hba : ¬ b <+: a) : a ++ x ≠ b ++ y := by
  intro h
  rcases le_total a.length b.length with hl | hl
  · exact hab (List.prefix_of_prefix_length_le (List.prefix_append a x) (h ▸ List.prefix_append b y) hl)
  · exact hba (List.prefix_of_prefix_length_le (h ▸ List.prefix_append b y) (List.prefix_append a x) hl)

/-- No signed input of one kind is a signed input of another kind. -/
theorem signed_domains_separate {a b : String} (ha : a ∈ signedTags) (hb : b ∈ signedTags)
    (hne : a ≠ b) (x y : List Char) : a.toList ++ x ≠ b.toList ++ y :=
  separate (signedTags_prefixFree a ha b hb hne) (signedTags_prefixFree b hb a ha (Ne.symm hne))

theorem hash_domains_separate {a b : String} (ha : a ∈ hashTags) (hb : b ∈ hashTags)
    (hne : a ≠ b) (x y : List Char) : a.toList ++ x ≠ b.toList ++ y :=
  separate (hashTags_prefixFree a ha b hb hne) (hashTags_prefixFree b hb a ha (Ne.symm hne))

/-- HKDF uses, as (input key, info label) (§5.3, §5.4, §5.6, §6.3, §6.7). The same `info` is
used twice only with different input keys. `DirK` is the directory key, `PairK` a pairing
session key, `L` a pairing link secret, `s` an invitation secret, `CK` the stretched hub
password. -/
def kdfUses : List (String × String) :=
  [("MS", "tandem/v2/manager-sign"), ("MS", "tandem/v2/manager-agree"),
   ("CK", "tandem/v2/admin-capsule"),
   ("K", "tandem/v2/msg"), ("K", "tandem/v2/collection-tag"),
   ("HK", "tandem/v2/msg"), ("HK", "tandem/v2/home-tag"),
   ("HubK", "tandem/v2/directory-key"), ("HubK", "tandem/v2/directory-tag"),
   ("DirK", "tandem/v2/msg"),
   ("PairK", "tandem/v2/msg"),
   ("L || A", "tandem/v2/pair-package"),
   ("L", "tandem/v2/pair-invite"), ("L", "tandem/v2/pair-key"), ("L", "tandem/v2/pair-tag"),
   ("s", "tandem/v2/invite-key"),
   ("uid", "tandem/v2/inbox-tag")]

/-- From any one input key, every derived key has its own label. -/
theorem kdf_labels_distinct : kdfUses.Nodup := by decide

/-! ## Snapshot inventory (§8.3) -/

/-- One record's entry: record ID (16) and its write IDs (32 each), counted. -/
def invEntry (e : List Byte × List (List Byte)) : List Byte := e.1 ++ counted 4 id e.2

def inventoryBytes (inv : List (List Byte × List (List Byte))) : List Byte := counted 4 invEntry inv

/-- The canonical inventory of a set of (record ID, write ID) pairs: records sorted and unique,
each with its write IDs sorted and unique. -/
def canon (P : Finset (List Byte × List Byte)) : List (List Byte × List (List Byte)) :=
  ((P.image Prod.fst).sort (· ≤ ·)).map fun r => (r, ((P.filter (·.1 = r)).image Prod.snd).sort (· ≤ ·))

def PairsWF (P : Finset (List Byte × List Byte)) : Prop :=
  P.card < 256 ^ 4 ∧ ∀ p ∈ P, p.1.length = 16 ∧ p.2.length = 32

theorem mem_of_canon {P : Finset (List Byte × List Byte)} {r w : List Byte} :
    (r, w) ∈ P ↔ ∃ e ∈ canon P, e.1 = r ∧ w ∈ e.2 := by
  constructor
  · intro h
    refine ⟨_, List.mem_map.2 ⟨r, Finset.mem_sort _ |>.2 (Finset.mem_image.2 ⟨(r, w), h, rfl⟩), rfl⟩,
      rfl, ?_⟩
    exact Finset.mem_sort _ |>.2 (Finset.mem_image.2 ⟨(r, w), Finset.mem_filter.2 ⟨h, rfl⟩, rfl⟩)
  · rintro ⟨e, he, rfl, hw⟩
    obtain ⟨r', -, rfl⟩ := List.mem_map.1 he
    obtain ⟨p, hp, hpw⟩ := Finset.mem_image.1 ((Finset.mem_sort _).1 hw)
    obtain ⟨hpP, hpr⟩ := Finset.mem_filter.1 hp
    obtain ⟨p1, p2⟩ := p
    simp only at hpr hpw
    subst hpr hpw
    simpa using hpP

theorem canon_inj {P Q : Finset (List Byte × List Byte)} (h : canon P = canon Q) : P = Q := by
  ext ⟨r, w⟩
  rw [mem_of_canon, mem_of_canon, h]

theorem card_le_of_sub {P : Finset (List Byte × List Byte)} {r : List Byte} :
    ((P.filter (·.1 = r)).image Prod.snd).card ≤ P.card :=
  (Finset.card_image_le).trans (Finset.card_filter_le _ _)

theorem canon_wf {P : Finset (List Byte × List Byte)} (hP : PairsWF P) :
    (canon P).length < 256 ^ 4 ∧ ∀ e ∈ canon P, e.1.length = 16 ∧
      (e.2.length < 256 ^ 4 ∧ ∀ w ∈ e.2, w.length = 32) := by
  refine ⟨?_, ?_⟩
  · simp only [canon, List.length_map, Finset.length_sort]
    exact lt_of_le_of_lt Finset.card_image_le hP.1
  · intro e he
    simp only [canon, List.mem_map, Finset.mem_sort, Finset.mem_image] at he
    obtain ⟨r, ⟨p, hp, rfl⟩, rfl⟩ := he
    refine ⟨(hP.2 p hp).1, ?_, ?_⟩
    · simp only [Finset.length_sort]
      exact lt_of_le_of_lt card_le_of_sub hP.1
    · intro w hw
      simp only [Finset.mem_sort, Finset.mem_image, Finset.mem_filter] at hw
      obtain ⟨q, ⟨hq, -⟩, rfl⟩ := hw
      exact (hP.2 q hq).2

theorem selfDelim_invEntry : SelfDelim (fun e : List Byte × List (List Byte) =>
    e.1.length = 16 ∧ (e.2.length < 256 ^ 4 ∧ ∀ w ∈ e.2, w.length = 32)) invEntry :=
  selfDelim_pair (selfDelim_bytes 16) (selfDelim_counted 4 (selfDelim_bytes 32))

/-- **The inventory bytes determine the set**, so their hash commits to exactly one set of
(record, write) pairs; and being a function of the set, they don't depend on any order. -/
theorem inventory_bytes_inj {P Q : Finset (List Byte × List Byte)} (hP : PairsWF P)
    (hQ : PairsWF Q) (h : inventoryBytes (canon P) = inventoryBytes (canon Q)) : P = Q := by
  have := selfDelim_counted 4 selfDelim_invEntry (canon P) (canon Q) [] [] (canon_wf hP)
    (canon_wf hQ) (by simpa [inventoryBytes] using h)
  exact canon_inj this.1

/-- The empty inventory is a zero count (§8.3). -/
theorem empty_inventory : inventoryBytes (canon ∅) = [0, 0, 0, 0] := by
  simp [inventoryBytes, canon, counted, be]

end Tandem.Enc
