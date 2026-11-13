import TandemProofs.Symbolic

/-!
# The hub password (§6.7, §6.8)

The admin capsule is `MS` under `KDF(Argon2id(password, salt), hubID)`, on the hub for anyone
to read. In the symbolic model (`Symbolic.lean`), with Argon2id as a one-way function of the
password and the salt:

* **Without the password nobody gets `MS`**, whatever else they hold: the capsule, the
  pairing link, every message on the hub (`no_password_no_management`). Whether a password can
  be *guessed* is outside the model; Argon2id's cost is the assumption that it can't be cheaply.
* With the password and the capsule, anyone gets it (`password_opens_management`).

And the rules of §6.7 as functions of the typed text:

* The password is hashed as its NFC form, so any way of typing it opens the capsule
  (`any_typing_opens`), and unlocking checks no length: recovery can't be refused by a
  different way of counting characters (`unlocking_checks_no_length`).
* A capsule opens only with its password and only for its hub (`wrong_password_fails`,
  `other_hub_fails`); one that holds another hub's key is refused (`foreign_seed_refused`).
* The minimum counts code points of the NFC form, not what a person sees as characters
  (`emoji_example`).
-/

namespace Tandem.HubPassword
open Sym

/-- NFC normalization and UTF-8 length, given from outside: only their being functions of the
code points matters here (Unicode's tables don't). -/
structure Text where
  nfc : List Nat → List Nat
  utf8 : List Nat → Nat

/-- §6.7: checked when a password is chosen, at hub creation or a change. -/
def chooseOK (T : Text) (p : List Nat) : Bool :=
  decide (12 ≤ (T.nfc p).length) && decide (T.utf8 (T.nfc p) ≤ 1024)

def capsuleKey (T : Text) (p : List Nat) (salt hub : Msg) : Msg :=
  .kdf (.kdf (.pw (T.nfc p)) salt) hub

def sealCap (T : Text) (p : List Nat) (salt hub ms : Msg) : Msg := .enc (capsuleKey T p salt hub) ms

/-- Opening: the AEAD opens only under the exact key. -/
def unlock (T : Text) (p : List Nat) (salt hub : Msg) : Msg → Option Msg
  | .enc k m => if k = capsuleKey T p salt hub then some m else none
  | _ => none

theorem any_typing_opens {T : Text} {p q : List Nat} (h : T.nfc p = T.nfc q) (salt hub ms : Msg) :
    unlock T q salt hub (sealCap T p salt hub ms) = some ms := by
  simp [unlock, sealCap, capsuleKey, h]

/-- **Unlocking checks no length**: a capsule sealed under a password opens with it, whether or
not that password would pass `chooseOK` under some other way of counting. -/
theorem unlocking_checks_no_length (T : Text) (p : List Nat) (salt hub ms : Msg) :
    unlock T p salt hub (sealCap T p salt hub ms) = some ms :=
  any_typing_opens rfl salt hub ms

theorem wrong_password_fails {T : Text} {p q : List Nat} (h : T.nfc p ≠ T.nfc q) (salt hub ms : Msg) :
    unlock T q salt hub (sealCap T p salt hub ms) = none := by
  simp [unlock, sealCap, capsuleKey, h]

theorem other_hub_fails {T : Text} {p : List Nat} {salt hub hub' ms : Msg} (h : hub ≠ hub') :
    unlock T p salt hub' (sealCap T p salt hub ms) = none := by
  simp [unlock, sealCap, capsuleKey, h]

/-- The full check: the seed must be this hub's (`pub(MKs) = hubID`, §6.7). -/
def openFor (T : Text) (pubOf : Msg → Msg) (p : List Nat) (salt hub cap : Msg) : Option Msg :=
  (unlock T p salt hub cap).filter (fun ms => pubOf ms = hub)

theorem foreign_seed_refused {T : Text} {pubOf : Msg → Msg} {p : List Nat} {salt hub ms : Msg}
    (h : pubOf ms ≠ hub) : openFor T pubOf p salt hub (sealCap T p salt hub ms) = none := by
  simp [openFor, unlocking_checks_no_length, h]

/-- 👩‍💻 is three code points (U+1F469 U+200D U+1F4BB), one character to a reader. Four of them
are 12 code points: long enough (§6.7). -/
def coder : List Nat := [0x1F469, 0x200D, 0x1F4BB]

def asIs : Text := { nfc := id, utf8 := fun cps => cps.length * 4 }

example : chooseOK asIs (coder ++ coder ++ coder ++ coder) = true ∧
    chooseOK asIs (coder ++ coder ++ coder) = false := by decide

/-! ## Who gets the management seed -/

def salt : Msg := .atom "salt"
def hub : Msg := .atom "hubID"
def MS : Msg := .atom "MS"
/-- A good password, as code points. -/
def thePassword : List Nat := "kålrabi og æbler i skuret".toList.map Char.toNat

def capsule : Msg := sealCap asIs thePassword salt hub MS

def privOf (pk : String) : String := pk ++ "-priv"

/-- Everything a member or a mailbox holder sees of management: the capsule, its salt, the hub
ID and public keys. -/
def onHub : List Msg := [capsule, salt, hub, .atom "MKa", .atom "MKs"]

theorem onHub_safe : ∀ m ∈ onHub, safe privOf ["MS", "MKa-priv"] [thePassword] m = true := by
  decide

theorem no_password_no_management {K : List Msg} (hK : ∀ m ∈ K, safe privOf ["MS", "MKa-priv"]
    [thePassword] m = true) : ¬ Derives privOf (onHub ++ K) MS := by
  apply atom_secret (S := ["MS", "MKa-priv"]) (P := [thePassword]) _ (by decide)
  intro m hm
  rcases List.mem_append.1 hm with h | h
  · exact onHub_safe m h
  · exact hK m h

theorem password_opens_management : Derives privOf (onHub ++ [.pw thePassword]) MS := by
  have hc : capsule ∈ onHub ++ [.pw thePassword] := by simp [onHub]
  have hs : salt ∈ onHub ++ [.pw thePassword] := by simp [onHub]
  have hh : hub ∈ onHub ++ [.pw thePassword] := by simp [onHub]
  have hp : Msg.pw (asIs.nfc thePassword) ∈ onHub ++ [.pw thePassword] := by simp [asIs]
  exact .dec (k := capsuleKey asIs thePassword salt hub) (.know hc)
    (.kdf (.kdf (.know hp) (.know hs)) (.know hh))

end Tandem.HubPassword
