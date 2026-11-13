import Mathlib.Data.List.Basic

/-!
# A Dolev–Yao model (for §6.3, §6.6, §6.7)

Messages are terms; an attacker derives from what it holds only by pairing and splitting,
encrypting and decrypting with a key it can derive, sealing to a public key and opening with
the private key, and deriving keys (HKDF, Argon2id) from inputs it holds. Keys can't be
inverted, ciphertexts reveal nothing without the key, signatures can't be forged. This is
perfect cryptography: it says what the *composition* gives away, not how hard a primitive is to
break. In particular a password the attacker hasn't got is one it can't guess; Argon2id's cost
is what makes that assumption reasonable for a good password (§6.7), and nothing here proves it.

**Secrecy** (`secrecy`): pick the atoms that must stay secret. A term is *safe* when it shows
none of them: a ciphertext is safe if its key isn't (nobody can open it) or its content is.
Everything derivable from safe terms is safe. So to show that someone can't learn a secret, it
is enough to check, by computation, that every message they hold is safe.
-/

namespace Tandem.Sym

inductive Msg where
  /-- A value: a secret, a public key, an ID, a label. -/
  | atom (s : String)
  | pair (a b : Msg)
  /-- AEAD under key `k` (ChaCha20-Poly1305). -/
  | enc (k m : Msg)
  /-- HPKE to the public key named `pk`; opening it needs that key's private half. -/
  | hpke (pk : String) (m : Msg)
  /-- A key derived from two inputs (HKDF; Argon2id from a password and a salt). One-way. -/
  | kdf (a b : Msg)
  /-- A password, as the Unicode code points that are hashed (§6.7). -/
  | pw (cps : List Nat)
deriving DecidableEq, Repr

/-- What an attacker holding `K` can derive. `priv pk` names the private key of public key `pk`.
A private key derived from a seed (`MKa` from `MS`) is held by whoever is given it; the
scenarios give it to whoever holds the seed. -/
inductive Derives (priv : String → String) (K : List Msg) : Msg → Prop
  | know {m : Msg} : m ∈ K → Derives priv K m
  | pair {a b : Msg} : Derives priv K a → Derives priv K b → Derives priv K (.pair a b)
  | fst {a b : Msg} : Derives priv K (.pair a b) → Derives priv K a
  | snd {a b : Msg} : Derives priv K (.pair a b) → Derives priv K b
  | enc {k m : Msg} : Derives priv K k → Derives priv K m → Derives priv K (.enc k m)
  | dec {k m : Msg} : Derives priv K (.enc k m) → Derives priv K k → Derives priv K m
  | hpke {pk : String} {m : Msg} : Derives priv K m → Derives priv K (.hpke pk m)
  | unhpke {pk : String} {m : Msg} : Derives priv K (.hpke pk m) → Derives priv K (.atom (priv pk)) →
      Derives priv K m
  | kdf {a b : Msg} : Derives priv K a → Derives priv K b → Derives priv K (.kdf a b)

/-- Safe for secrets `S`: shows none of them. A password is in `S` by its code points. -/
def safe (priv : String → String) (S : List String) (P : List (List Nat)) : Msg → Bool
  | .atom s => !S.contains s
  | .pw c => !P.contains c
  | .pair a b => safe priv S P a && safe priv S P b
  | .enc k m => !safe priv S P k || safe priv S P m
  | .hpke pk m => S.contains (priv pk) || safe priv S P m
  | .kdf a b => safe priv S P a && safe priv S P b

/-- **Secrecy**: whatever is derived from safe messages is safe. -/
theorem secrecy {priv : String → String} {S : List String} {P : List (List Nat)} {K : List Msg}
    (hK : ∀ m ∈ K, safe priv S P m = true) {t : Msg} (h : Derives priv K t) :
    safe priv S P t = true := by
  induction h with
  | know hm => exact hK _ hm
  | pair _ _ ha hb => simp only [safe, ha, hb, Bool.and_self]
  | fst _ ih => simp only [safe, Bool.and_eq_true] at ih; exact ih.1
  | snd _ ih => simp only [safe, Bool.and_eq_true] at ih; exact ih.2
  | enc _ _ _ hm => simp only [safe, hm, Bool.or_true]
  | dec _ _ ih hk => simp only [safe, hk, Bool.not_true, Bool.false_or] at ih; exact ih
  | hpke _ ih => simp only [safe, ih, Bool.or_true]
  | unhpke _ _ ih hp =>
    simp only [safe, Bool.not_eq_true'] at hp
    simp only [safe, Bool.or_eq_true, hp] at ih
    simpa using ih
  | kdf _ _ ha hb => simp only [safe, ha, hb, Bool.and_self]

/-- So a secret atom can't be derived from safe messages. -/
theorem atom_secret {priv : String → String} {S : List String} {P : List (List Nat)} {K : List Msg}
    (hK : ∀ m ∈ K, safe priv S P m = true) {s : String} (hs : s ∈ S) :
    ¬ Derives priv K (.atom s) := fun h => by
  have := secrecy hK h
  simp [safe, hs] at this

theorem pw_secret {priv : String → String} {S : List String} {P : List (List Nat)} {K : List Msg}
    (hK : ∀ m ∈ K, safe priv S P m = true) {c : List Nat} (hc : c ∈ P) :
    ¬ Derives priv K (.pw c) := fun h => by
  have := secrecy hK h
  simp [safe, hc] at this

theorem Derives.mono {priv : String → String} {K K' : List Msg} (hK : K ⊆ K') {t : Msg}
    (h : Derives priv K t) : Derives priv K' t := by
  induction h with
  | know hm => exact .know (hK hm)
  | pair _ _ a b => exact .pair a b
  | fst _ a => exact .fst a
  | snd _ a => exact .snd a
  | enc _ _ a b => exact .enc a b
  | dec _ _ a b => exact .dec a b
  | hpke _ a => exact .hpke a
  | unhpke _ _ a b => exact .unhpke a b
  | kdf _ _ a b => exact .kdf a b

end Tandem.Sym
