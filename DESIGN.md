# Tandem: encrypted, shared, real-time collections over a mailbox

Status: **draft for review**, 2026-11-12. Author: Jesper Nøhr, with Claude. Cut down to the requirements: one secret per user, one owner per collection for its whole life, signed writes, and no automatic recovery from lost keys or conflicting histories (§19). This is an architecture revision, not a security sign-off.

This document is self-contained. It assumes no knowledge of the app that motivated it (Liszto, a to-do app; §17) and is written so that independent reviewers can check it for soundness. The words MUST, SHOULD and MAY are used in the sense of RFC 2119.

---

## 0. Summary

Tandem is a protocol for keeping **collections of records** in sync across many devices and many users, with **end-to-end encryption**, **per-collection sharing**, and **real-time updates**. It needs no server of its own. Its only infrastructure is a **mailbox**: one account on any JMAP server (Fastmail, Stalwart, Cyrus) or, through a second binding, an IMAP server.

- The mailbox account is the **hub**. Every participant's device holds a credential for it.
- Every piece of data is an **encrypted, signed message** in one folder of the hub. The hub sees opaque ciphertext, sizes and timing, nothing else.
- **Who can read a collection** is decided by who holds its key, not by the hub. **Who may change what** is decided by signatures checked on every device.
- Records merge **without coordination** (they are CRDTs: conflict-free replicated data types), so devices work offline and in any order, and every device converges on the same state.
- A **user is one secret**, held by all their devices and printable as a backup. There are no per-device keys: a device or backup in the wrong hands means starting over with a new identity (§6.4).
- Every collection has **one owner**, fixed when it is created, who alone decides membership, roles and key changes. Other members are writers or readers.
- A user's devices learn about all their collections from an encrypted **home** channel. Adding a device takes one QR scan and a six-digit code to compare.
- Sharing uses **invite links or QR codes**. Removing or demoting a member **rotates the collection key**. Content edits work offline; changes to sharing need the network.

The trade-off, accepted on purpose: anyone holding the hub credential can **delete or withhold** data (availability), even for collections they can't read. Honest devices hold full replicas and re-upload what was destroyed (§12). Tandem is meant for hubs shared by people who trust each other not to vandalise: a household, friends, a small team.

---

## 1. Goals and non-goals

### 1.1 Goals

1. **CRUD on records, real-time.** Create, read, update and delete records in collections; changes appear within seconds on other devices where the app is running (push, not polling).
2. **Offline content editing.** Every device has a local replica and can edit content without the network. Content merge is eventual and order-independent. Membership and keys are changed by the owner, one change at a time; they are not offline CRDT operations.
3. **Multi-user sharing.** A collection can be private (one user) or shared with other users, each with a role. A shared collection can become private again.
4. **End-to-end confidentiality and integrity.** The hub operator, and hub-credential holders who aren't members of a collection, can't read it or forge changes to it.
5. **Attribution.** Every change is signed with its author's key; "Aimee added this" is verifiable, not asserted. Attribution is to the user, not to one of their devices.
6. **Loss is handled by starting again.** A lost device costs nothing while another device or the backup key remains. A secret in the wrong hands, an owner who has lost theirs, and a collection with conflicting histories are all handled the same way: a new identity or a new collection, with the items copied (§6.4, §11.8).
7. **No infrastructure of our own.** Any standards-compliant mail account works as the hub. No relay, no push service, no key server.
8. **Portable.** Uses only widely available primitives (X25519, Ed25519, HKDF-SHA-256, ChaCha20-Poly1305, HPKE) so that SDKs in other languages can interoperate. Apple's CryptoKit covers all of them.
9. **Friendly UX.** One-time hub setup; QR or link for everything after that; no passwords to invent; no key fingerprints. The one thing a user ever compares is a six-digit code when adding a device (§6.3).

### 1.2 Non-goals

- **Administration by anyone but the owner.** There are no admins, and ownership doesn't move. An owner who wants to hand a list on has the other person copy it into a new one (§11.8).
- **Choosing between conflicting roster histories.** Two signed successors of one roster statement kill the collection (§9.4). No clock, chain length or later signature picks a winner.
- **Removing one device, or replacing a user's secret.** Every device of a user holds the same secret. If one is stolen, or the backup key is found, the user starts over with a new identity (§6.4).
- **Recovering a collection in place.** A dead collection, or one whose owner's secret is lost, is copied into a new one by whoever can read it (§11.8).
- **Availability against hub-credential holders.** They can delete messages. Mitigated by replicas and self-healing (§12), not prevented.
- **Hiding metadata from the hub.** It sees message counts, sizes, timing, which channel tags are active, and which connection reads and writes which of them (§14).
- **Forward secrecy / post-compromise security per message.** Keys rotate on membership changes, not per message. Whoever holds a collection key reads that collection's epoch. (MLS-style continuous key agreement is discussed in §16 and left for a later version.)
- **Protecting a collection from its own members.** Writers can overwrite and delete within a collection. Roles limit honest clients; they don't stop a malicious member from vandalising content they can write, including keeping a field in conflict (§9.3). Removing them ends it.
- **Huge hubs.** Every device downloads the change feed of the whole hub (bodies only for channels it follows). Designed for tens of users and thousands of collections per hub, not millions.
- **Large binary attachments.** Records are small (§10.6). Blobs are a possible later extension.
- **Several hubs per client** (decided 2026-11-11, §19). A client talks to one hub.
- **OAuth-only mail providers** (Gmail, Outlook) (decided 2026-11-11, §19). The IMAP binding targets servers that accept a password or app password.

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **Hub** | One mail account (JMAP or IMAP) whose credential is shared by all participants. |
| **Binding** | How Tandem's abstract storage operations map onto JMAP (§10.2) or IMAP (§10.3). |
| **User** | A person. Identified by their **user ID**, which is their public signing key. |
| **User secret** | 32 random bytes from which all of a user's keys derive. Every device of the user holds it (§6). |
| **Backup key** | The user secret, written out for the user to keep. Whoever holds it is the user (§6.3). |
| **Device** | One installation of a Tandem client. It holds its user's secret and has a random device ID. |
| **Home** | A user's private channel holding their memberships (collections and their keys) and per-user settings. |
| **Inbox** | A per-user channel where others post sealed messages to that user (key grants and requests). |
| **Collection** | A set of records shared by a group of members (Liszto: a list). Has a stable **collection ID** and a sequence of **epochs**. |
| **Owner** | The user who created a collection. Only the owner changes its sharing, and the owner never changes (§8.1). |
| **Role** | Owner, writer or reader. Writers change records; readers only read. |
| **Roster** | A collection's chain of statements signed by the owner: who is a member, in which role, and which key is current (§8). Everything else in a collection is a **data record**. |
| **Epoch** | A period during which a collection uses one symmetric **epoch key**. Removing a member or lowering a role starts a new epoch. |
| **Record** | An object in a collection (Liszto: an item), made of named **fields**. |
| **Write** | One signed change to one field of one record. The atomic unit of the protocol. |
| **Channel** | A stream of hub messages sharing a **channel tag** (a collection epoch, a home, an inbox, an invite, a pairing session). |
| **Hub credential** | Whatever the binding needs to log in: JMAP session URL + bearer token, or IMAP host + username + password. |

---

## 3. Threat model and security properties

### 3.1 Actors

| Actor | Has | Assumed behaviour |
|---|---|---|
| **Hub operator** (e.g. Fastmail) | All stored messages, connection metadata | Honest-but-curious; may be breached; may lose data. |
| **Hub participant** | The hub credential | May be any person you gave hub access, including ex-members of your collections. Can read/write/delete raw messages. |
| **Collection member** | Hub credential + the collection's current epoch key | Can read, and as a writer change, the collection's records. May misbehave within their role. |
| **Removed member** | Old epoch keys, possibly still the hub credential | Wants to keep reading or writing. |
| **Network attacker** | The wire | Defeated by TLS to the hub; Tandem doesn't add a layer here. |
| **Device thief** | One unlocked or forensically imaged device, or a found backup key | Gets the user secret, and is that user everywhere from then on. Nothing cuts them off short of the user starting over (§6.4). |

### 3.2 Properties

| Property | Holds against | Mechanism |
|---|---|---|
| Confidentiality of record contents, names and roles | Hub operator, non-member participants, and removed members for content written after they are cut off (§3.3) | AEAD under epoch keys (§5.4); a new key with every removal and demotion (§8.2, §11.6). |
| Integrity: no forged or altered writes | Everyone without the author's secret | Ed25519 signature on every write, inside the ciphertext (§5.5). |
| Authorization: only the owner changes the roster; readers cannot write | Everyone without the owner's secret | The roster is signed with the owner's key, which the collection ID fixes for good (§7.1). Every write names the roster statement that allows it (§8.3). |
| Attribution | Members impersonating each other | Per-write signatures. A user ID is the signing key itself, so nobody, the owner included, can assign someone a key (§5.3). |
| Content convergence | Any delivery order, duplication and replay of the same accepted writes; malicious writers | The field merge of §9.2–9.3. |
| One roster history | Not guaranteed. | Two signed successors of one statement kill the collection instead of being merged or ranked (§9.4). |
| Replay of old messages | Hub operator, hub participants | Merge is idempotent. Inside an epoch superseded writes stay superseded and tombstones stay. Ciphertext from an old epoch doesn't decrypt in a new one, and an old write is accepted there only if the owner listed it (§8.3). Tombstones are permanent (§12.3). |
| Cutting off a stolen device | **Not provided.** | Every device holds the user secret. The user starts over (§6.4). |
| Rollback / withholding | **Not prevented.** Hub can hide recent messages from a device. | Detectable only when a device holds data the hub lacks; it re-uploads what is missing (§12.2). |
| Deletion | **Not prevented** for hub participants. | Replicas + self-healing (§12). |
| Forward secrecy | **Not provided** within an epoch. | Rotation on removal and demotion only. |
| Traffic analysis | **Not prevented.** | The hub sees which connection reads and writes which channel, and when (§14). |

### 3.3 What a removed member can still do

**Read** everything written under keys they hold: all content up to the rotation, and whatever other devices still publish to the old epoch in the moments before they see the new one announced (§11.6).

**Write** until the rotation. Afterwards the roster no longer lists them, they can't decrypt the new epoch, and a backdated write is absent from its fixed snapshot inventory (§8.3).

**Come back**, only if the owner lets them. The invite that admitted them is closed for good, and their old identity is never admitted again without the owner being asked (§11.3). Under a new identity they are a stranger holding whatever open invite they can get: the owner judges a link request by its name, and shows a QR code only to people in the room.

**Vandalise**, if they still hold the hub credential: delete messages, including other people's key grants (replaced on request, §11.4), and post junk that honest clients discard. Revoking the hub credential (per-person or per-device tokens, §13.1) removes even that.

---

## 4. Architecture

```
┌──────────────────────────── Application (e.g. Liszto) ──────────────────────────────┐
│ record types, field encodings, UI, conflict presentation                            │
└───────────────▲─────────────────────────────────────────────────────▲───────────────┘
                │ records, writes, conflicts                          │ invites, members, devices
┌───────────────┴──────────────────── Tandem core ────────────────────┴───────────────┐
│ devices & users (§6) · collections, epochs, roster (§7–8) · writes & merge (§9)     │
│ envelope crypto (§5) · sharing (§11) · self-healing (§12) · local replica           │
└───────────────▲─────────────────────────────────────────────────────────────────────┘
                │ abstract store: append, list changes, fetch, destroy, watch (§10.1)
┌───────────────┴───────────────┐       ┌───────────────────────────────┐
│ JMAP binding (RFC 8620/8621)  │  or   │ IMAP binding (RFC 9051/3501 + │
│ Email/set, Email/changes,     │       │ UIDPLUS, CONDSTORE/QRESYNC,   │
│ EventSource push              │       │ IDLE)                         │
└───────────────────────────────┘       └───────────────────────────────┘
                         one folder named "Tandem" in one mail account
```

The core never depends on which binding is in use. All devices of all participants of one hub use the same hub; they may reach it through different bindings if the server offers both.

---

## 5. Cryptography

### 5.1 Primitives (suite `v1`, fixed)

| Purpose | Primitive | Reference |
|---|---|---|
| Hash | SHA-256 | FIPS 180-4 |
| Key derivation | HKDF-SHA-256 | RFC 5869 |
| Symmetric AEAD | ChaCha20-Poly1305 (12-byte nonce, 16-byte tag) | RFC 8439 |
| Signatures | Ed25519 | RFC 8032 |
| Key agreement | X25519 | RFC 7748 |
| Public-key sealing | HPKE, base mode, DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305 | RFC 9180 |
| Randomness | OS CSPRNG | |

There is exactly one suite per protocol version. A new suite means a new protocol version (§15); there is no negotiation.

### 5.2 Notation

- `||` concatenation. Integers are unsigned big-endian. `u8`, `u16`, `u32`, `u64` are their byte widths.
- `lp(x)` = `u32(len(x)) || x` (length-prefixed bytes).
- `H(x)` = SHA-256(x).
- `KDF(ikm, salt, info, L)` = HKDF-SHA-256, output `L` bytes. `info` strings are ASCII and begin with `tandem/v1/`.
- `b64u` = base64url without padding (RFC 4648 §5). Every value has one spelling: text with padding, with a character outside the alphabet, or with unused bits that aren't zero is invalid (§10.5).
- `b32` = RFC 4648 base32, lowercase, no padding.

### 5.3 Keys

| Key | Type | Created | Lives |
|---|---|---|---|
| User secret `US` | 32 B | Random, once per user | Every device of the user, in the Keychain with this-device-only accessibility; and the backup key (§6.3) |
| User signing key `UKs` | Ed25519 | `KDF(US, "", "tandem/v1/user-sign", 32)` | Derived. Its public half is the user ID |
| User agreement key `UKa` | X25519 | `KDF(US, "", "tandem/v1/user-agree", 32)` | Derived. Its public half is in the rosters that list the user (§8.1) |
| Home key `HK` | 32 B | `KDF(US, "", "tandem/v1/home", 32)` | Derived |
| Epoch key `K(c,e)` | 32 B | Random, per collection epoch | Members' home channels; grants |
| Invite secret `S` | 32 B | Random, per invite | The invite link or QR code; the owner's home (§11.1) |
| Invite signing key `VKs` | Ed25519 | `KDF(S, "", "tandem/v1/invite-sign", 32)` | Derived by whoever holds the invite secret: the joiner and the owner's devices |
| Pairing secret `P` | 32 B | Random, per pairing session | Pairing QR only |

**User ID:** `uid = pub(UKs)`, 32 bytes. A user is their signing key, so nothing has to certify which key is whose. Displayed, when shown at all, as the first 20 bytes in `b32`, in groups of 4.

**Device ID:** 8 random bytes. It only tells one device's writes from another's (§9.1). A device picks a new one whenever the secret is installed on it, by pairing or from the backup key, and keeps it with the same this-device-only protection as `US`. A device ID that a phone migration copied along with the app's data would otherwise be in use on two devices at once; their dots would collide, and a write covering one device's dot would silently replace the other device's write.

**Key storage.** `US` lives in the Keychain with this-device-only accessibility: not synced, and not restored onto another device from a backup. A new or restored device gets it by pairing, or from the backup key (§6.3).

### 5.4 Message encryption

Every hub message body (§10.4) is an **envelope**:

```
envelope = magic "TNDM" (4) || version u8 = 1 || kind u8 || salt (32) || ciphertext
```

`kind` is 1 = record state, 2 = sealed inbox message, 3 = request or notice (join, pairing, leave, key request). Others are reserved.

For symmetric kinds (1, 3), with channel key `K` (an epoch key, a home key, an invite key or a pairing key):

```
mk         = KDF(K, salt, "tandem/v1/msg", 32)     # fresh per message; salt random
nonce      = 12 zero bytes                          # safe: mk is never reused
aad        = "TNDM" || u8(1) || u8(kind) || tag     # tag: the channel's tag (§5.6)
ciphertext = ChaCha20-Poly1305-Seal(mk, nonce, plaintext, aad)
```

Deriving a fresh key from a 256-bit random salt makes key and nonce reuse negligibly likely, and there is no counter state to lose.

`plaintext` is UTF-8 JSON (§10.5). The AEAD makes any byte change detectable, and the tag in the AAD stops the hub from moving a message between channels.

For kind 2 (inbox), `salt` is replaced by a key ID, `kid = H("tandem/v1/kid" || pub(UKa))`, naming the exact user agreement key the message is sealed to. The ciphertext is HPKE (§5.1), sealed to that key with `info = "tandem/v1/inbox"` and the same `aad`.

**HPKE wire format.** RFC 9180 leaves the encoding to the application. Everywhere Tandem uses HPKE (inbox messages, and the pairing reply in §6.3), the sealed bytes are `enc || ct`: the 32-byte encapsulated key followed by the AEAD ciphertext with its tag.

### 5.5 Write signatures

Each write (§9.1) is signed over this exact byte string (independent of JSON formatting, so any SDK can reproduce it):

```
sigInput = "TANDEM-WRITE-V1"                 (15 ASCII bytes)
        || scopeID (16)                      # collection ID, or the user's home scope (§6.2); zero in a genesis
        || recordID (16)
        || lp(fieldName UTF-8)
        || u8(op)                            # 1 assign, 2 add, 3 remove, 4 delete
        || lp(element)                       # set element for add/remove; else empty
        || H(value)                          # value bytes; H("") for remove/delete
        || authorUID (32)                    # the author's user ID: their public signing key (§5.3)
        || deviceID (8)                      # the installation that wrote it (§5.3)
        || u64(hlcMillis) || u16(hlcCounter)
        || ref (32)                          # the roster statement it was written under, or for a statement the one before it; zero if none
        || vv(context) || vv(base)           # data records (§9.2); both empty in a roster statement
writeID  = H(sigInput)

vv(x)    = u16(n) || n × ( deviceID (8) || u64(millis) || u16(counter) )   # sorted by deviceID; n ≤ 1024 (§9.2)
```

**Who signs.** Everything is signed with its author's **user signing key** `UKs`: data, home writes, and for the owner the roster statements and grants. The author's user ID in `sigInput` is that key, so a signature can be checked with nothing but the write. Whether the write is allowed is a separate question, answered by the roster (§8).

The signature covers the statement a write was made under, not the epoch it is stored in, so a write can be re-encrypted into later epochs during rotation (§11.6) without losing its author's signature.

Signatures live **inside** the ciphertext, so message contents tell the hub nothing about who wrote what. Traffic patterns can (§14).

**Verification is strict and the same everywhere.** Whether a write is valid must not depend on which library checked it, or two honest clients could disagree and diverge. A verifier MUST reject a signature whose `S` is not below the group order, MUST reject a public key or `R` that isn't a canonical point encoding, MUST reject a small-order public key, and MUST check the plain equation `[S]B = R + [k]A`. RFC 8032 §5.1.7 permits either that or the cofactored form; Tandem fixes the choice. Libraries that verify in batches or with the cofactored equation accept signatures the others reject and MUST NOT be used.

Other signed objects (join requests, grants, key requests, leave notices and pairing challenges) use their own `TANDEM-…-V1` prefixes, given where they're defined, so a signature can never be replayed as a different kind of object.

### 5.6 Channels and tags

The hub sees only channel tags, never user, device or collection IDs:

| Channel | Key | Tag |
|---|---|---|
| Collection epoch | Random `K(c,e)`, committed by its starting statement `E` | `b32(KDF(K(c,e), E, "tandem/v1/collection-tag", 20))` |
| Home | `HK` | `b32(KDF(HK, "", "tandem/v1/tag", 20))` |
| Inbox | HPKE to `UKa` | `b32(KDF(uid, "", "tandem/v1/inbox-tag", 20))` |
| Invite | `KI = KDF(S, "", "tandem/v1/invite-key", 32)` | `b32(KDF(KI, "", "tandem/v1/tag", 20))` |
| Pairing | `KDF(P, "", "tandem/v1/pair-key", 32)` | `b32(KDF(P, "", "tandem/v1/tag", 20))` |

The inbox is tagged from the user ID on purpose: anyone you share with knows your user ID and needs to reach your inbox. The hub never sees a user ID, so it can't link an inbox to a person.

The invite channel is keyed from the invite secret, so a join request can be read only by those who hold that invite: the owner's devices and everyone it was given to (§11.2).

A tag reveals nothing about the key or the identifiers behind it, and the tags of successive epochs aren't related cryptographically. Traffic patterns can still link them (§14).

### 5.7 Key commitment

`commit(K) = H("tandem/v1/commit" || K)`. A roster statement that starts an epoch carries the commitment of its key, so everyone who is given the key can check they got the same one.

---

## 6. Users and devices

A user is one secret. There are no per-device keys, no chain of devices and no way to remove one device (decided 2026-11-12, §19). That keeps identity out of everything else: a user's keys never change, so nothing in a collection ever has to follow a key change.

### 6.1 First run

A Tandem client works locally with no hub. When the user connects a hub (§13.1) and has no identity yet, the client:

1. generates the user secret `US` and derives the user's keys from it (§5.3);
2. picks a random device ID;
3. creates home (§6.2);
4. shows the backup key (§6.3) and asks the user to save it.

### 6.2 Home

**Home** is one channel, keyed by `HK`. It holds named records (§7.3):

| Record | Fields |
|---|---|
| `membership:<collectionID>` | `keys` (a set of `{ "epoch": b64u(ID of the statement that started the epoch), "k": b64u(epoch key) }`, one for every epoch the user has been given), `role` (cached), `state` (`requested`, `active`, `left`, `removed`), `invite` (while requested), `flight` (collections the user owns: the one key change not yet announced, with its key and the roster it leads to, §8.2), app-defined display hints |
| `invite:<inviteID>` | An invite the user made for a collection they own (§11.1): `c` (collection ID), `s` (the invite secret), `role`, `expires`, `autoApprove`, `maxUses`, `closed` |
| `settings:<app>` | App-defined (Liszto: list order, pinned lists) |

Home uses the write and merge rules of §9, as a single epoch, with `scopeID = H("tandem/v1/home" || uid)[0..16]`. A home write is valid iff it is signed with the user's own key; everyone who holds `HK` is one of the user's own devices. Two rules are specific to home:

- `keys` only grows. Keys are indexed by the **full epoch-start statement ID**, never by their commitment alone. Which epoch is current follows the collection's roster, not a clock or arrival order.
- Home records are never deleted (§7.3): leaving a collection sets `state`, so the user can join it again later.

### 6.3 Adding a device, and the backup key

**Pairing.** On an existing device, "Add a device" generates `P` and shows a QR code:

```json
{ "t": "pair", "v": 1, "hub": <hub descriptor §13.1>, "u": "<b64u uid>", "p": "<b64u P>", "exp": <unix ms, now + 120 s> }
```

1. The new device scans it, stores the hub credential, makes a one-time X25519 key pair, and posts a kind-3 **pairing request** to the pairing channel: `{ "name", "ek": "<b64u one-time public key>" }`.
2. The showing device, watching the pairing channel while the QR is on screen, answers each request it sees, at most three per QR code, with a kind-3 **challenge**: `{ "ek", "n": "<b64u 16 random bytes>", "sig" }`, signed with the user's key over `"TANDEM-PAIR-CODE-V1" || P || ek || n`.
3. Both devices compute a **code**: the first four bytes of `H("tandem/v1/pair-code" || P || ek || n)` as an unsigned integer, modulo 1,000,000, shown as six digits. The new device shows it only after checking the challenge's signature against the `uid` in the QR code. The showing device asks "Add 'Aimee's iPad'? Code 483 920", and the user approves only if the new device shows the same code.
4. On approval the showing device posts a kind-3 reply to the same channel: `US`, HPKE-sealed (§5.4) to `ek` with `info = "tandem/v1/pair"` and `aad = "TANDEM-PAIR-V1" || uid || ek`. It stops listening when the QR code expires or after one approval.
5. The new device opens the reply, checks that the secret gives the `uid` in the QR code, picks its own device ID, opens home, and starts following every active membership.

The QR code holds no secret of the identity, but it contains the hub credential, and anyone who captures it within its 120 seconds can read the pairing channel and post requests of their own, under any name. Approving such a request would hand over the identity for good. The code is what stops that. It depends on a number the showing device picks only after it has seen the request's key, and the new device accepts that number only with the user's signature. A request the new device didn't make shows a different code, and one put in the real request's place leaves the new device showing none. With three answers per QR code, a blind guess succeeds three times in a million (decided 2026-11-12, §19).

**Backup key.** `US` encoded for humans: `b32(US || H(US)[0..3])`, 35 bytes = 56 characters, shown as 14 groups of 4. Shown at setup with "Write this down. It's the only way back in if you lose every device", and again on request behind device authentication.

**Restoring.** On a new device, enter the hub credential and the backup key. The client derives the keys, picks a new device ID (§5.3), opens home, and is the user. Nothing else is needed, and nothing on the hub has to change.

The hub credential isn't part of the backup key. Anyone on the hub can show the hub QR (§13.1), and the app SHOULD suggest keeping the hub details next to the key.

### 6.4 Lost devices, and starting over

**A device that is merely gone** (broken, wiped, replaced) needs nothing. The other devices carry on. With none left, the backup key restores the user on a new one.

**A device or a backup key in someone else's hands** can't be cut off: whoever holds the secret is the user, in every collection, and the secret can't be changed. The user **starts over** with a new identity:

- For collections they own, they create new ones, copy the items across and invite people again (§11.8). The old collections stay as they are, in the thief's hands as much as the user's, and members should leave them.
- For collections others own, the owner removes the old identity and invites the new one.

The app offers this as one flow ("Start over on this device"). Nothing in the protocol does it automatically, and nothing tells other members that an identity was abandoned except the user. This is the price of having one secret instead of per-device keys; Q15 records what the alternative cost.

---

## 7. Collections

### 7.1 Identity and genesis

The creator writes the genesis roster: themselves as the owner, a commitment to a random `K(c,0)`, and 16 random bytes. The **collection ID** is the first 16 bytes of that statement's ID. The genesis uses zero `scopeID`, so its ID is computed before the collection ID is needed. The collection ID therefore fixes who the owner is, for the life of the collection.

The **epoch ID** `E` is the full 32-byte ID of its starting roster statement, including for epoch 0. Only after that ID exists is the channel tag derived (§5.6). Sharing one key commitment never makes two different starts the same epoch.

### 7.2 Epochs

An epoch is a segment of the roster history, identified by its starting statement, with a numeric counter for the data merge. Removing a member or lowering a role starts the next epoch with a fresh random key. Additions and promotions within an epoch only increase access.

Only the owner starts an epoch. Clients accept data directly under the **same epoch-start ID**, or through that epoch's committed snapshot inventory (§8.3).

### 7.3 Reserved and named records

| Record | Purpose | Who may write |
|---|---|---|
| `_roster` | The collection's chain of statements: members and roles, and each epoch's key commitment (§8) | The owner |
| `_meta` | The collection's name, icon and other app metadata. An ordinary data record | The owner (§8.3) |

Ordinary record IDs are 16 random bytes. **Named records** (`_roster`, `_meta`, and the records in home) use `id(name) = H("tandem/v1/named" || name)[0..16]` and carry their name in a `_name` field, so every client derives the same ID for the same name. A random ID colliding with a named one is as likely as a 128-bit collision.

Reserved and named records are never removed: a record ID derived from a name can't be created afresh, so they change state through their fields instead. A `delete` write on a named record is invalid.

### 7.4 Private and shared are the same thing

A private collection is a collection whose roster has one member. Sharing adds members; making it private again removes all other members, which rotates the key. There is no mode switch.

---

## 8. Roster and authorization

### 8.1 Owner and members

A collection has exactly one **owner**: the user whose key signed its genesis. All other members are **writers** or **readers**. Only the owner changes the roster: adds and removes members, changes a role, deletes the collection. There is no admin role, and ownership doesn't move.

The roster is a chain of complete states:

```json
{ "n": 12,
  "key": { "n": 3, "commit": "<commit of epoch key>", "prev": "<previous commit>" },
  "snapshot": "<hash of this epoch's snapshot inventory; absent at genesis>",
  "members": [ { "uid": "<user ID>", "role": "owner|writer|reader", "uka": "<agreement key>",
                 "name": "Aimee", "via": "<optional invite id>" } ],
  "deleted": false }
```

A member is their user ID, which is their signing key. `uka` is the key that grants are sealed to, and `name` is what the member called themselves; both come from the member's signed join request (§11.2). None of the three ever changes. Invites are not in the roster; they are the owner's private records (§11.1).

The collection's name, icon and other app metadata are not in the roster. They are an ordinary record, `_meta`, that only the owner may write (§8.3), so renaming works offline from any of the owner's devices.

### 8.2 Valid statements, one change at a time

Every statement after the genesis is signed with the **owner's key**: the user ID that the genesis names as owner. It must have the right collection scope, `_roster` record, `s` field, `assign` operation and the owner as author; empty contexts and an empty `element`; `n` exactly one higher; unique member IDs; exactly one owner, the same as in the genesis; only the three defined roles; and for every member who stays, the same `uka`, `name` and `via` as before. A deleted statement has no successors.

The owner may add or remove members and change a member between writer and reader. Removing a member or lowering a role MUST advance the epoch counter by one, use a fresh random key whose commitment does not occur earlier in the roster, and commit a new snapshot inventory. A statement that starts an epoch adds no member. A same-epoch statement keeps the epoch commitment and snapshot commitment unchanged.

**One change at a time.** Every device of the owner holds the key, so nothing cryptographic stops two of them from signing at once. Three rules keep them apart:

- A device changes a roster only when its user asks for it, on that device. The one automatic change is admitting someone through an auto-approve invite, made by the device that is showing its QR code (§11.3).
- Before signing, a device syncs the roster and home. It never signs two different successors of one statement: it persists the exact signed bytes before exporting them, and after a crash it resumes with those bytes.
- Before starting an epoch, a device writes the new epoch key and a note of the change (the parent, the statement's ID, and the members and roles it leads to) to the collection's `flight` field in home. While a note is there, no other device of the owner signs anything in that collection; it offers to finish the change instead (§11.7). The note is removed when the change is announced, and also when it is discarded, so a missing note doesn't say which of the two happened (§11.6).

The roster does not merge. Two different successors of one statement, both signed by the owner, mean one of three things: two devices of the owner acted in the same moment, the hub hid one device's change from another, or someone else holds the owner's secret. In each case the collection is dead (§9.4). This deliberately gives up the collection in a rare accident in exchange for having no rule that chooses a winner; the rules that were tried are listed in §19.

### 8.3 Valid data writes and snapshots

A data write names a valid roster statement and is signed by its author, whom that statement lists as owner or writer. Its source epoch is the first statement, going back from the one it names, that carries the same epoch commitment. In a channel identified by epoch-start ID `E`, it is eligible iff:

1. its source epoch ID is **exactly `E`**; or
2. its write ID and record ID occur in the snapshot inventory committed by `E`, and the roster statement it names is an ancestor of `E`.

Matching a key or counter alone is never sufficient. Readers' writes are rejected, and a write to the `_meta` record is accepted only if the statement it names lists its author as the owner. These checks depend only on signed statements and a fixed inventory, none of which can change, so superseded data can be pruned. In a dead collection processing stops (§9.4); clients do not try to reconstruct a trusted state from what was already pruned.

**Snapshot inventory.** Before signing a statement that starts an epoch, the owner's device fixes every record/write ID it will carry over. The canonical inventory bytes are `u32(recordCount)`, then entries sorted by 16-byte record ID; each entry is `recordID || u32(writeCount) || writeIDs`, with the 32-byte write IDs sorted and unique. Record IDs are unique. Empty inventories encode a zero count. `snapshot = H(inventoryBytes)` in the roster statement commits the whole set before the epoch ID exists, avoiding a cycle.

An inventory is published as indexed encrypted fragments in the new channel (§10.5–10.6). A receiver checks the assembled bytes against the signed commitment before using any entry. A missing inventory holds the epoch: without it nothing old can be judged. A listed write that is absent holds nothing. The inventory says which old writes may enter, not which must be present: later edits supersede snapshot writes and their messages are collected (§10.7), and one that was destroyed is restored by the devices that hold it (§12.2). Only listed writes are imported, with their original signatures. Tombstones are included like any other write. The inventory cannot later grow: a late write is signed again by its own author (§11.6).

**Nothing listed is dropped unseen.** A device may enter an epoch while a listed write is missing from the hub, and may then start the next epoch itself. Its new inventory therefore lists, besides what it carries over, every entry of the current inventory that it neither holds nor has itself seen replaced. Such an entry costs 32 bytes and stays until a rotating device has seen the write. Without this rule a write that was only missing for a while, a tombstone included, could never be restored: nobody but its author may sign it again. A device needs the current epoch's inventory to start the next one.

A write that names a roster statement the device doesn't have yet is held until it arrives. Bad signatures, malformed writes and an author not authorized by the named statement are rejected. Resource limits for holding incomplete objects are configurable (§9.4).

---

## 9. Records, writes and merge

### 9.1 Writes

A write is the tuple:

| Field | Meaning |
|---|---|
| `record` | 16-byte record ID |
| `field` | Field name (UTF-8; app-defined; names starting with `_` reserved). Two names are the same field iff their UTF-8 bytes are equal. Nothing is normalized or case-folded: `é` as one code point and as `e` with a combining accent are two fields |
| `op` | `assign` (register), `add` / `remove` (set element), `delete` (record tombstone) |
| `element` | Set element bytes (add/remove only) |
| `value` | Opaque bytes (assign only); the app chooses the encoding |
| `author` | Author's user ID, which is their public signing key |
| `dot` | `(deviceID, hlc)`: this write's position in the authoring device's history |
| `ref` | In a collection: the ID of the roster statement the write was made under (§8.3). Its **epoch** is that statement's epoch. In home: none; home is one epoch. In a roster statement: the ID of the statement before it (§9.4). Epoch equality for collection data means the same epoch-start ID, not merely the same key or number. |
| `context` | A version vector covering the writes **of the same epoch** that this write supersedes |
| `base` | A version vector covering the writes **of earlier epochs** that it supersedes |
| `sig` | Ed25519 signature over `sigInput` (§5.5), by the author's user key |

**HLC:** each device keeps a hybrid logical clock `(millis u48, counter u16)`. A new write's HLC is `(max(now, last.millis), counter)`, with `counter` incremented if `millis` didn't advance; if the counter would pass 65 535, `millis` advances by one and the counter restarts at 0. A device's HLCs are strictly increasing. Devices do **not** adopt other devices' clock values: causality is tracked by `context` and `base`, not by timestamps, so a device with a wrong clock can't make its writes win over writes that saw them.

A device ID in a dot only tells one device's writes from another's. Writes are signed with the user's key, so a dot proves nothing about which device wrote.

### 9.2 Field semantics (data records)

Write `reach(x)` for `x.context` joined with `x`'s own dot, and say a vector **covers** another if it is at least as large in every entry.

**Supersession.** A write `x` is superseded by a write `w` to the same field (the same record, and field names equal byte for byte, §9.1) iff either

- `w` is from a **later** epoch, and `w.base` covers both `reach(x)` and `x.base`; or
- `w` is from the **same** epoch, `w` includes `x`, and `x` doesn't include `w`, where "`w` includes `x`" means `w.context` covers `reach(x)` and `w.base` covers `x.base`.

A write never supersedes a write from a later epoch.

For writes by honest devices this is the familiar rule: a write supersedes exactly what its author had seen. The rest only matters when a context is malformed. Two writes that each claim to cover the other are both kept, as a conflict. A context that claims to cover the future binds only its own epoch, because a new epoch starts with empty contexts. Whatever the vectors say, supersession is a strict partial order, with no cycles (§9.3).

**Building a write.** When a device writes a field, the new write's `context` is the join of `reach(k)` over the kept writes `k` of the current epoch, and its `base` is the join of `k.base` over all kept writes plus `reach(k)` over those from earlier epochs. It therefore supersedes everything the device knew.

**Vector size.** A `context` or `base` with more than 1,024 entries makes a write invalid. Device IDs in a vector are claims nobody can check, so without a limit a writer could sign contexts full of made-up IDs, and every later write would have to copy them. Honest collections stay far below the limit: a vector has one entry per device that ever wrote the field. If the vectors a write would need don't fit, the device writes with what fits. Its write then doesn't replace everything, and no later write can settle the field. The way out needs no vectors: once the member responsible has been removed, any writer deletes the record and creates it again. The app is told when a field is in that state.

**Register (op `assign`): a multi-value register.** For each field, a device keeps the writes not superseded by another kept write.

- If one write remains, that is the value.
- If several remain, they are **concurrent siblings**. The displayed value is the sibling with the greatest `(epoch, hlc, author, deviceID, writeID)`. The app is told about the others (Liszto keeps conflicting note text, §17). Any later assignment that covers all siblings resolves the conflict.
- Every sibling is kept. The app is shown at most 16, the greatest by the order above. The limit is on presentation only: dropping siblings from the state would make the result depend on the order writes arrived in.

**Set (ops `add` / `remove`): an add-wins observed-remove set.** Per element, the `add` and `remove` writes form a register of their own under the same supersession rule, and the element is present iff at least one kept write for it is an `add`. A concurrent add and remove therefore leave the element in (add wins); a remove that saw every add takes it out; a replayed old add stays superseded.

**Delete (op `delete`): sticky tombstone.** A record with any valid `delete` write is deleted, whatever else happens to it, and stays deleted (§12.3). Undeleting means creating a new record. (This is "delete wins", a deliberate product choice: an offline device editing a record someone deleted doesn't resurrect it.) `delete` is not used on the roster or named records (§7.3).

**Unknown fields** (written by a newer app version) are merged and carried like any other, so mixed versions don't lose data.

### 9.3 Record state and convergence

A data record's state is the set of its valid writes that no other valid write supersedes. Merging two states is set union followed by dropping superseded writes. Supersession is irreflexive and transitive for any `context` and `base`, honest or not, so the kept set is simply the set of maximal writes, and merging is a join on a semilattice: commutative, associative and idempotent. Devices that have seen the same writes hold the same state, regardless of order, duplicates or replays. For honest writers the rule coincides with the standard multi-value register and observed-remove set over dotted version vectors (Shapiro et al. 2011; Preguiça et al. 2012).

Whether a write is valid never changes (§8.3), so a device can discard what is superseded without ever having to judge it again.

**Malicious writers.** A member can sign anything: the same dot twice, contexts that cover each other, contexts that cover the future. None of it breaks convergence. Writes are identified by write ID, so two writes with one dot are simply two writes, and the ordering has no cycle to exploit. What such a member can do is keep a field in conflict, or pin a value of their own on top of it, for as long as they are a member (§1.2). The rotation that removes them ends it: old-epoch contexts don't bind writes of the new epoch, so the next honest write settles the field. Where a member has left a field with more to cover than a write can carry, the record is deleted and created again (§9.2). (Kleppmann 2022 gets the same guarantee from hash-linked histories; Tandem keeps version vectors for data so that superseded writes can be discarded.)

### 9.4 The roster chain and dead collections

Statements are `assign` writes to field `s` of `_roster`, carrying the complete JSON state and the parent statement ID in `ref`. Their IDs are the write IDs. The first 16 bytes of a collection's genesis ID identify the collection. A statement's exact value bytes are signed without JSON reserialization.

**Following the roster.** Starting at the genesis, a device follows the one successor signed by the owner. Out-of-order statements are held until their parents arrive. A device keeps every statement the owner signed.

**Conflict.** Receipt of any two different `_roster` successors of the same statement, both signed by the owner, means the collection is **dead**, even if one of the two is invalid: the owner's key must never sign two decisions for one position. A statement that starts an epoch counts once it is announced; before that it is a draft that only the owner's devices can read (§11.6). The test needs the full signed statement bytes. Signatures with any other key do not count.

A device that finds a collection dead:

- stops publishing data, accepting grants and merging for that collection;
- puts the two conflicting statements into the `_roster` of the current epoch's channel, and restores them there if they are destroyed. They prove themselves to anyone who holds the roster, so members who cannot read the channel where the conflict first appeared learn of it too;
- preserves its local replica, the conflicting statements and unsent edits;
- shows "House can't sync any more" and keeps the data, read-only;
- offers to copy it into a new collection (§11.8).

Different devices may hold slightly different contents at that moment. They agree that the collection is dead once they have the two statements. Clocks, chain length and later signatures never select one history over another, and nothing is rolled back.

**Holding limits.** Incomplete items can be held for 30 days by default, configurable per client. After that they may be evicted locally and re-fetched. A missing parent or inventory fragment is not evidence of forgery, and an incomplete item's hub message is not destroyed merely because this timer expired.

### 9.5 CRUD in terms of writes

| Operation | Writes |
|---|---|
| **Create** record | New random record ID; `assign` writes for its initial fields (including the app's type field, e.g. `type`). |
| **Read** | Local: the merged state. Nothing to send. |
| **Update** field | One `assign` (or `add`/`remove`) write with full `context` and `base` for that field. |
| **Delete** record | One `delete` write. The tombstone stays (§12.3). |
| **Move** between collections | App-level: create in the target, delete in the source. Collections are separate key domains. |

---

## 10. Storage on the hub

### 10.1 Abstract store

The core needs six operations:

| Operation | Meaning |
|---|---|
| `ensureFolder()` | Make sure the single folder `Tandem` exists. |
| `append(channelTag, body) → id` | Store a new message. |
| `changes(since cursor) → (added ids with channel tags, removed ids, cursor)` | Everything that changed in the folder. A message moved into the folder counts as added, and one moved out of it as removed. |
| `fetch(ids) → bodies` | Download message bodies. |
| `destroy(ids)` | Remove messages permanently. |
| `watch(onChange)` | Push notification while connected. |

**All channels share one folder.** Each message carries its channel tag in a header (§10.4), so a client can skip bodies of channels it doesn't follow. One folder means one IDLE connection on IMAP, one change cursor, no folder-creation races and no folder-count limits. More folders would hide nothing: the hub sees the tags either way.

### 10.2 JMAP binding (RFC 8620, RFC 8621)

| Operation | JMAP |
|---|---|
| `ensureFolder` | `Mailbox/query` with filter `name: "Tandem"`, then `Mailbox/get`. The filter matches substrings (RFC 8621 §2.3), so take the mailbox named exactly `Tandem` with no parent, or failing that the one directly under the inbox (where some servers keep IMAP folders, §10.3). `Mailbox/set create` at top level if there is none. On a duplicate-name error, query again (sibling names are unique, RFC 8621 §2). |
| `append` | `Email/set create` with `mailboxIds: {<Tandem>: true}`, `keywords: {"$seen": true}`, `header:Tandem-Channel:asText`, `header:Tandem-Version:asText`, and a `text/plain` body via `bodyValues` (§10.4). Batch up to the session's `maxObjectsInSet`. The server chooses the body's charset and transfer encoding (RFC 8621 §4.6), so the stored bytes may differ from an IMAP append; readers allow for that (§10.4). |
| `changes` | `Email/changes` since the stored state, repeated while `hasMoreChanges` is true. Then `Email/get` of the **created and updated** ids with properties `mailboxIds`, `header:Tandem-Channel:asText`: an id that is in the Tandem mailbox and not yet known is added; a known id that is no longer in it is removed. (Moving a message is an update, not a destroy.) Known ids in `destroyed` are removed. On `cannotCalculateChanges`, and on `invalidArguments` (which some servers answer to a state they can no longer use), full resync: `Email/query` with `inMailbox`, paged. |
| `fetch` | `Email/get` with `bodyValues`, `fetchTextBodyValues: true`. The value arrives decoded, with CRLF turned into LF (RFC 8621 §4.1.4). If it is flagged `isEncodingProblem`, the server couldn't decode it: download the raw message by its `blobId` and decode it as an IMAP reader would (§10.4). |
| `destroy` | `Email/set destroy`. |
| `watch` | EventSource push (RFC 8620 §7.3) with `types=Email`, `closeafter=no`, `ping=60`. On a `StateChange`, run `changes`. A server may ping at an interval of its own choosing and says which in every ping (`interval`). If neither an event nor a ping arrives for two of **those** intervals, reconnect and run `changes`. |

Writes don't use `ifInState`: merging is coordination-free, so concurrent writers never conflict at the JMAP layer.

A push can arrive before the answer to the device's own `Email/set`, and an append whose answer was lost may still have landed. In both bindings a device therefore knows its own messages by `H(envelope)` (§10.4), not by the id in an answer, and looks for an envelope on the hub before appending it a second time.

### 10.3 IMAP binding (RFC 9051 / RFC 3501)

Required extensions: **UIDPLUS** (RFC 4315), **IDLE** (RFC 2177). Strongly recommended: **CONDSTORE/QRESYNC** (RFC 7162); a client that uses QRESYNC MUST issue `ENABLE QRESYNC` after authenticating (RFC 7162 §3.2.3). Authentication is `LOGIN` or `AUTHENTICATE PLAIN` over TLS with a password or app password; OAuth-only providers are out of scope (§1.2).

| Operation | IMAP |
|---|---|
| `ensureFolder` | `LIST "" Tandem`; `CREATE Tandem` if missing (`ALREADYEXISTS` is success). If the personal namespace has a prefix (`NAMESPACE`, RFC 2342), the folder is `<prefix>Tandem`. |
| `append` | `APPEND Tandem (\Seen) {literal}` with the full RFC 5322 message (§10.4); UIDPLUS returns the UID. |
| `changes` | **New messages**, in every case: `UID FETCH <last known UID + 1>:* (UID BODY.PEEK[HEADER.FIELDS (Tandem-Channel)])`, ignoring a returned UID below the start of the range (`n:*` always returns the last message). QRESYNC doesn't report new messages by itself (RFC 7162 §6). **Removed messages**: with QRESYNC, the `VANISHED (EARLIER)` response to `SELECT Tandem (QRESYNC (uidvalidity modseq))`; without it, compare `UID SEARCH ALL` with the local UID set (fine at Tandem's scale). A `UIDVALIDITY` change means a full resync. |
| `fetch` | `UID FETCH (BODY.PEEK[HEADER.FIELDS (Content-Type Content-Transfer-Encoding)] BODY.PEEK[TEXT])`, then undo the transfer encoding and the charset (§10.4). |
| `destroy` | `UID STORE +FLAGS.SILENT (\Deleted)` then `UID EXPUNGE` (UIDPLUS, so other messages aren't affected). |
| `watch` | `IDLE` on the selected `Tandem` folder; `EXISTS`/`EXPUNGE`/`VANISHED` responses trigger `changes`. Re-issue IDLE at least every 29 minutes (RFC 2177). |

**Why IMAP too:** it reaches providers that offer app passwords but not JMAP (iCloud Mail, self-hosted Dovecot, many hosts), where JMAP is today mostly Fastmail, Stalwart and Cyrus. **Costs:**

- the client needs an IMAP implementation (Apple's `swift-nio-imap` is a protocol codec, not a full client);
- push needs a long-lived TCP connection, which mobile OSes suspend;
- servers cap concurrent connections, per address or per account. Dovecot's default is 10 per user and IP, so a household behind one address can reach it. Stalwart's is 16 per account wherever they come from, so a shared hub there serves 16 IMAP devices in all, across every user. Every device holds one connection open for IDLE; a device uses a single connection and leaves IDLE to run commands;
- some servers lack QRESYNC.

The JMAP binding is the reference; the IMAP binding is second.

### 10.4 Message format

Both bindings store the same logical message:

```
From: Tandem <tandem@invalid>
Date: <time of append, UTC>
Message-ID: <random@tandem.invalid>
Subject: tandem
MIME-Version: 1.0
Tandem-Version: 1
Tandem-Channel: <channel tag>
Content-Type: text/plain; charset=us-ascii
Content-Transfer-Encoding: 7bit          (IMAP append; under JMAP the server chooses charset and encoding)

<base64 of the envelope (§5.4), wrapped at 76 characters>
```

Nothing else is in the clear. The hub already knows the append time.

**Reading a message, in either binding.** Three steps, of which JMAP's `bodyValues` has already done the first two (RFC 8621 §4.1.4), unless it says `isEncodingProblem` (§10.2), and an IMAP reader does all three:

1. undo the body's transfer encoding (`7bit` and `8bit`: nothing to undo; `quoted-printable`; `base64`);
2. decode the resulting bytes to text with the `charset` of the `Content-Type` header. A JMAP server may have stored the body in a charset other than `us-ascii`, UTF-16 for instance, and skipping this step then yields garbage;
3. remove all whitespace and base64-decode what is left to get the envelope.

The channel tag is the `Tandem-Channel` value, unfolded and trimmed. It is matched byte for byte against the tags a client derives; the same letters in another case are another tag. A message whose body doesn't decode to an envelope is ignored. A message's identity across bindings and devices is `H(envelope)`, not its JMAP id or IMAP UID.

### 10.5 Plaintext payloads (inside the envelope)

**Kind 1, record state.** One message holds the state of one record, or part of it (§10.6):

```json
{
  "rec": "<b64u record id>",
  "writes": [
    { "f": "title", "op": 1, "v": "<b64u value>", "a": "<b64u author uid>",
      "d": ["<b64u device id>", 1731312000123, 0], "r": "<b64u statement id>",
      "c": [["<b64u device id>", 1731311999000, 2]],
      "b": [["<b64u device id>", 1731300000000, 0]],
      "s": "<b64u signature>" },
    { "f": "tags", "op": 2, "e": "<b64u element>", "a": "...", "d": [...], "r": "...", "c": [...], "b": [...], "s": "..." }
  ]
}
```

`r` is the write's `ref`, `c` its context and `b` its base (§9.1). A roster statement has `r` (none in a genesis), empty `c` and `b`, and no `e`.

**Kind 1, inventory fragment.** Instead of `rec`/`writes`, a message may contain `{ "inventory": { "epoch": "<epoch-start id>", "part": 0, "parts": 1, "bytes": "<b64u fragment>" } }`. Exactly one of these two payload forms is allowed. Parts are numbered from zero; all parts must agree on `parts`. Assemble in index order, decode the canonical inventory and check its hash against the epoch statement. Duplicate identical fragments are harmless; inconsistent versions are held separately until a complete hash-matching assembly exists. A fragment is not an authorized inventory by itself.

Clients MUST use a real JSON serializer and MUST ignore unknown keys. A value (`v`) is opaque bytes: a client verifies and passes it on exactly as received and never re-serializes it.

**One reading.** Signatures are checked against parsed fields, so no two devices may read one message differently. JSON parsers do differ (RFC 8259 §4): given `"rec"` twice in one object, Apple's Foundation keeps the first and Python's `json` the last. A malicious writer could sign for the first and have every last-wins client reject what every first-wins client accepts. All JSON this document defines, in payloads, roster statements, QR codes and links, is therefore invalid, and rejected whole, if:

- any object has the same property name twice, at any depth. Names are compared as their decoded UTF-8 bytes: `é` as one code point and as `e` with a combining accent are two names, not one name twice;
- any number is not an integer in plain digits (no fraction, no exponent) of magnitude at most 2^53. HLC millis (≤ 2^48) and counters fit;
- it is not valid UTF-8, starts with a byte-order mark, holds an escaped surrogate that isn't half of a pair, or has anything but whitespace around the one value;
- a `b64u` value is not in its one spelling (§5.2). Lenient decoders read `QQ`, `QR` and `QQ==` as the same byte, and an ID would then have several texts.

**Bytes, not strings.** Wherever this document compares text (field names, property names, record names, channel tags), it means the UTF-8 bytes, with no Unicode normalization and no case folding. Some string types compare otherwise: Swift's `==` says that `é` and `e` with a combining accent are equal, Python's says they differ. Two clients built on those would keep different writes for the same field and never agree.

A standard parser that silently picks a winner among repeated names, or folds equivalent names into one, is not enough: Apple's Foundation does both. A client on such a platform checks before parsing.

**Kind 2, inbox message** (HPKE-sealed): grants, key requests, roster replies and declines (§11.4).

**Kind 3, request or notice**: join requests (invite channel, §11.2), pairing requests, challenges and replies (pairing channel, §6.3), leave notices (collection channel, §11.5) and key-request notices in an earlier collection epoch (§11.4).

### 10.6 Sizes

A message SHOULD stay under 64 KiB and MUST stay under 1 MiB. A record whose state doesn't fit (a long chain, a field with many siblings) is published as several messages, each holding part of its writes; the record's state is the union of what its messages hold. Snapshot inventories are split into indexed fragments under the same message limit; limits on assembly memory and incomplete fragments are configurable. A single write value must itself fit the message limit. Apps put large content elsewhere or split it.

### 10.7 Publishing and garbage collection

A device publishes a record by appending a kind-1 message with the record's current state (for a record in several messages, the part that changed). A message `m` is **dominated** by a message `n` of the same record if every write in `m` is in `n` or is superseded by a write in `n`. Held writes (§8.3) count as writes here, and inventory fragments are never dominated: an epoch's inventory stays on the hub for as long as the epoch is current, and devices that hold it restore a missing fragment (§12.2). The roster, and the two statements that prove a collection dead, are never pruned. The one exception is the channel of a key change that was discarded before it was announced: everything in it goes, its draft statement included (§11.7).

- A device MAY destroy a data message once it has merged it and another message on the hub dominates it. Two messages can dominate **each other**: when they hold the same writes, and also when they differ only in writes that are superseded anyway (`{x, y}` and `{y}`, where `y` supersedes `x`). Of two such messages, only the one with the greater `H(envelope)` may be destroyed on account of the other. Every device makes the same choice, so two devices never destroy each other's message and leave none.
- Byte-identical copies, which have the same `H(envelope)`, come from an append that was repeated after its answer was lost (§10.2). Only the device that made that envelope removes the extra copies, keeping one. Other devices leave them alone, since no hash can tell them which to keep.
- It MUST NOT destroy a message it hasn't merged, or a message of a version it doesn't understand (§15). Incomplete objects can be evicted locally under §9.4; that does not authorize destroying their hub messages. Statements the owner signed are kept.
- When a device sees messages for one record that none of them dominates (concurrent publishers), it waits a short random time, a few seconds at most, and publishes the merged state only if no dominating message has appeared in the meantime. Without the delay, every online device would publish the same merge at once. A record converges to one message on the hub between bursts of activity.
- Writes made in quick succession SHOULD be coalesced (debounce ~1 s) into one publish, so typing doesn't create a message per keystroke.

---

## 11. Sharing

### 11.1 Invites

The owner creates an invite: a random `inviteID` (16 B) and secret `S` (32 B), from which the invite's signing key pair and channel key derive (§5.3, §5.6). An invite is the owner's private business. It is a record in the owner's home (§6.2), not in the roster: creating one changes nothing that members see, and only the owner's devices and whoever holds the invite can read its channel. The invite payload is:

```json
{ "t": "join", "v": 1, "hub": <hub descriptor §13.1, optional>,
  "c": "<b64u collection id>", "i": "<b64u invite id>", "s": "<b64u S>",
  "by": { "uid": "<b64u owner's user ID>", "name": "Jesper" },
  "hint": "House", "exp": <unix ms> }
```

`by.uid` is the owner. The joiner later checks that the collection it is given was created by that key (§11.4). `hint` (the collection name) is optional and visible to anyone who sees the link; clients SHOULD let the inviter turn it off.

| Form | Default policy | Use |
|---|---|---|
| **QR code** shown on screen | `autoApprove: true`, expires in 10 minutes, 1 use | In person: scan and you're in. |
| **Link** `https://<app domain>/join#<b64u(payload)>` | `autoApprove: false`, expires in 7 days | Sent over a messenger; the owner approves the request. |

An invite admits a writer or a reader. `maxUses` is counted on the roster: everyone it has ever listed with a `via` that names the invite, removed members included. An invite is also over for good once anyone it admitted has been removed, whether or not they have since come back another way (§11.3). Someone who photographs a still-valid auto-approve QR can race the intended joiner for a remaining use. Every join shows in the member list, and an unexpected one can be removed.

The payload sits in the URL **fragment**, which browsers never send to a server. The app domain needs only a static page and universal-link association.

The hub credential is in the payload so a newcomer can join with one scan. That is the shared-hub model: whoever obtains the link obtains hub access (§3.3), and keeps it after the invite has expired, because an invite's expiry can't expire a mail credential. On an IMAP hub that credential can usually send mail as the account, too. A hub owner who wants to be able to cut one person off puts a separate token into the invite (§13.1).

### 11.2 Join request

The joiner's client:

1. If it has no hub, configures the one in the payload. If it has a **different** hub, it stops with a clear error (one hub per client, §1.2).
2. Ensures it has an identity (§6.1).
3. Records the pending request in home: `membership:<c>` with `state: requested`, the invite ID and the owner's user ID from `by`. A device accepts a key only for a collection it has asked to join or already belongs to (§11.4).
4. Appends a kind-3 message to the invite channel (keyed from the invite secret, §5.6):

```json
{ "uid": "<b64u>", "uka": "<b64u agreement key>", "name": "Aimee",
  "sig": "<by the joiner's key over 'TANDEM-JOIN-V1' || collectionID || inviteID || uid || uka || lp(name)>",
  "isig": "<by the invite key over 'TANDEM-INVITE-V1' || collectionID || inviteID || uid>" }
```

`sig` ties the agreement key and the name to the joiner. `isig` proves the joiner holds the invite itself. The request can be read by the owner's devices and by everyone else who holds this invite: whoever the link or code reached, including someone who joined through it and was later removed. Members who never held the invite can't read it, and neither can the hub.

5. Shows "Waiting for Jesper to approve" (or nothing, for auto-approve) and watches its inbox until the invite expires.

### 11.3 Approval

A device of the owner that sees a request checks `sig` against the `uid` in it, `isig` against the invite's key, and that the invite is **open**: it is in home and not closed there, it hasn't expired, it has uses left, and nobody it admitted has ever been removed. That last test is about the roster's whole history, not its current state: a member who was removed and later let back in by another invite does not reopen the first one. Uses and removals are read from the roster, which the device syncs before it signs (§8.2), never from home. Then:

- For an **auto-approve** invite, the device that is showing its QR code admits the joiner at once. The owner's other devices leave the request alone, so that only one of them signs (§8.2).
- For any other invite, the owner's devices show "Aimee wants to join House" with Approve / Decline. The name is whatever the request claims; the owner should know who they sent the link to. The device where the owner taps acts.

Admitting is a same-epoch roster statement that adds the member with the invite's role, their `uid`, `uka`, `name` and `via`. Once that statement is on the hub, the device sends the grant (§11.4) and destroys the request. Declining sends a hint instead.

A join request carries nothing that ties it to a moment, so anyone on the hub can post an old one again, and a removed member still holds the invite they came in through. Two rules keep a removal final. Both are read from the signed roster, so neither depends on a second write reaching home after the removal:

- **An invite is closed, for good, once anyone it admitted has been removed.** The roster's history records who came in through which invite (`via`) and who was removed afterwards. Coming back through another invite changes nothing for the old one: the removed member still holds it and could use it to bring in a new identity. The removing device also sets `closed` in home, so that the owner's devices stop watching the invite's channel; nothing relies on that write.
- **A user the roster once listed and no longer lists is never admitted without the owner being asked**, whatever the invite's policy: "Aimee was removed from House. Add her again?" That needs an invite that is still open, so in practice a new one.

A request from someone who is already a member is answered by sending the grant again, which also covers a device that crashed between admitting someone and sending their grant.

### 11.4 Grants and key requests

Inbox messages are kind-2 messages, HPKE-sealed to the recipient's `uka` as the roster or the join request gives it. A **grant** is signed by the owner:

```json
{ "type": "grant", "c": "<collection id>", "at": "<roster statement id>",
  "epoch": "<epoch-start id>", "k": "<epoch key>",
  "sig": "<by the owner over 'TANDEM-GRANT-V1' || recipientUID || collectionID || at || epoch || commit(k)>" }
```

It is sent only once `at` is on the hub and, for a statement that starts an epoch, announced (§11.6). The epoch's channel holds the complete roster through `at`, so one grant is enough to catch up, however many epochs a device missed.

A receiver requires all of this:

- its home has a `membership` for that collection in state `requested` or `active`;
- the roster in the channel is one valid line from a genesis whose ID gives this collection ID, through `at`;
- on a first grant, that genesis is signed by the owner named in the invite (`by.uid`), and the statement that admits this user names the invite it used;
- the grant is signed by that owner;
- `epoch` is the exact start of `at`'s epoch and carries `commit(k)`;
- `at` lists this user.

A device that already follows the collection checks the roster it is given against every statement it knows: a second successor anywhere means the collection is dead (§9.4). An older grant never moves it back. A grant for an earlier statement of the still-current epoch remains useful if the user is still listed. The device adds the key to home only after these checks. A grant that depends on something missing is held.

**Key requests** are `{ "type": "keyreq", "c", "have", "by", "sig" }`, signed by the member over `"TANDEM-KEYREQ-V1" || collectionID || have || uid`. A device that has learned of an epoch whose key it lacks, and has waited 10 minutes for the grant, sends one to the owner's inbox. It also posts it as a kind-3 notice in the last epoch channel it can read; the owner's devices keep watching earlier epochs' tags, whose keys are in home. This is one hub change feed, not a polling loop or a connection per epoch. The 10 minutes are a safety net behind the grant, not a schedule.

Any device of the owner answers. A current member gets the current key. Someone since removed gets no key. They get `{ "type": "roster", "c", "statements": [ … ] }`: the signed roster statements from `have` up to the one that removed them, which prove themselves and are how a device that was away for several epochs learns of its removal. Answering is not a roster change, so two devices answering is harmless.

A decline is `{ "type": "declined", "c", "i": "<invite id>" }`. A first-time joiner can't check who it is from, so it is a hint only: the client tells the user and keeps the request until the invite expires. A valid grant that arrives later still counts.

### 11.5 Leaving

A member sets its home membership to `left`, stops following, and posts a kind-3 leave notice in the epoch it knows: `{ "type": "leave", "uid", "at", "sig" }`, signed over `"TANDEM-LEAVE-V1" || collectionID || uid || at`. The owner's app shows "Aimee left House" and removes her when the owner confirms, which starts a new epoch (§11.6). A notice counts only if `at` is a statement of the current epoch, so one from an earlier stay can't remove someone who has come back. Until the owner confirms, the member stays listed.

The owner can't leave. They delete the collection (§11.9) or keep it.

### 11.6 Key rotation and crashes

Only the owner rotates: when they remove a member, lower a role, or ask for a new key. Data edits work offline; a rotation needs the network.

The device makes one journalled change:

1. Sync the current epoch fully and choose the data state to preserve, including tombstones. Entries of the current inventory that the device hasn't seen are carried forward as they are (§8.3).
2. Generate the new key; build the exact inventory and roster statement; write the key and the note to home (§8.2), and persist the signed statement, inventory and progress in the device's journal, **before exporting any of them**.
3. Upload the complete inventory fragments and the snapshot data to the new epoch's channel, then the full roster ending in the new statement. The statement is thus on the hub only once everything it commits to is. Verify it all, and restore anything destroyed meanwhile. No grant is sent yet.
4. **Announce:** publish the statement in the **old** epoch channel's `_roster`. This is the moment the change happens, and the only evidence that it did. Then move the key from the note into `keys` in home and remove the note.
5. Send grants to the remaining members, the owner included. A grant can be sent again after a crash or on request.
6. Make sure the inventory is still on the hub. Then destroy the old epoch's **data** messages, each only once every write in it is in the new channel, has been replaced there by a valid write, or was not carried over; a listed write that is missing from the new channel is copied across first. Its roster stays for good, and home keeps its key: that is how a device that was away finds out where the collection went.

An interruption before the announcement leaves everyone on the old epoch. The device resumes the **same** change after a restart; it never abandons signed bytes it has exported to sign something else. An interruption after the announcement may leave members waiting for the key; they stop publishing to the old epoch, and the device resumes sending grants. If that device is gone, another device of the owner finishes or drops the change (§11.7). Nothing happens on a timer.

A device learns of the change from the announcement in the old channel, or from a verified grant and the roster in the new channel. The owner's other devices can read the new key in the note from step 2 on. Until they have the announcement they use it for one thing only: finishing that change when the user asks (§11.7). A missing note is not evidence, and neither is a key: a note goes when a change is announced and also when it is discarded (§11.7). Until it is announced, a statement that starts an epoch exists only in its own channel, under a key that only the owner's devices hold. There it is a draft: not part of the roster, not a successor, and not one half of a conflict (§9.4). A device that has learned of the change stops publishing to the old epoch and changes to the new snapshot. It keeps every old write the new inventory lists, whoever wrote it, and re-publishes one that the new channel lacks (§12.2). It signs again any write of its own user that the inventory doesn't list and the new state doesn't supersede (§9.2). A write the snapshot replaced is not signed again: that would bring the replaced value back on top. A missing inventory delays the move; a listed write that is absent does not (§8.3). A removed user is told, stops publishing, and keeps their replica read-only for 30 days, configurable, so that unsent work can be rescued. They receive no new key.

**Signing again.** Use the same record, field, operation, element, value and dot. Set the new context empty and the base to the join of the old base and `reach(oldWrite)`. As `ref`, use the **first statement of the new epoch that lists this user as owner or writer**, which is not necessarily the statement that starts it. Every device of the user that signs the same write again gets the same bytes and the same write ID. If no statement allows it, or the base would exceed the vector limit (§9.2), the work is kept for the app instead of being published as an invalid write.

Signing again starts from an empty context, so the order between two writes that both missed the inventory is lost. If one had replaced the other, the replaced value returns as a conflict, and a removed set element returns, until the next write settles it. Keeping the old context instead would let a removed member's malformed context outlive its epoch (§9.3).

Only the owner's fixed inventory or the author's own signing again carries old writes into a new epoch. Another user's late write can be lost if its author never returns and it missed the inventory. Devices do not turn an incomplete snapshot into deletes.

### 11.7 Two devices of one owner

The owner's devices share the key and home, and stay out of each other's way with the note in home (§8.2). A device that is asked to change a collection's sharing while another device's note is there doesn't sign. The note says what that change is, so the device can tell the user.

- **If the change's statement and inventory are on the hub** in the new channel, there is nothing to choose. The device finishes it and says so: "Finishing a change started on Jesper's iPhone: making Aimee read-only in House." It checks the new channel against the inventory, copies across from the old channel any listed write that is missing, announces the statement, sends the grants and cleans up, as the first device would have (§11.6).
- **Otherwise the change never happened**, and the device asks: "Jesper's iPhone started making Aimee read-only in House and didn't finish. Do it here, or discard it?" Either way it removes the note, and the key in it, from home and destroys what was uploaded. For "do it here" it then makes the same change itself, with a new key.

It then makes the change it was asked for. A change is never dropped without the user being told which one it was. The first device, when it next syncs, looks for the announcement of its change in the old channel. If it is there, the change was made, by itself or by the device that finished it, and it carries on from step 5. If it isn't there and the note naming its statement is gone, the change was discarded: the announcement always precedes the note's removal and the hub has one change feed (§10.1), so a device that has seen the note go has seen the announcement if there was one. It then discards its journal entry and anything it had uploaded. While its note is still there, it resumes.

Three things can still put two successors on one statement, and each kills the collection (§9.4): the user has the second device deal with a change while the first device is in fact still working on it; the hub hides one device's change from another; or two devices are asked for a change in the same moment. Members never end up following different rosters.

### 11.8 Dead collections and copying

A collection can't be administered any more in three cases:

- **It is dead** (§9.4): two successors of one roster statement exist. It is read-only for everyone.
- **The owner's secret is lost**: every device and the backup key. Members can go on editing, but its sharing can never change again.
- **The owner's secret is in someone else's hands** (§6.4). The owner abandons it.

There is no recovery under the same collection ID. Anyone who can read the collection can copy it: they create a **new collection**, of which they are the owner, write the current state of the records into it as writes of their own, and invite people again. Tombstones and the old writes' signatures are not carried over; a copied item is the copier's. Apps do not silently redirect an old membership to the copy.

If no readable replica survives, the contents are lost.

### 11.9 Deleting a collection

The owner signs a statement with `deleted` set. No successor follows it. Devices stop showing the collection and keep local data read-only for 30 days, configurable; a device of the owner then destroys the data on the hub and keeps the roster, so that a device returning late learns the collection was deleted. There is no undo; a deleted collection can be copied into a new one while a replica remains (§11.8).

---

## 12. Availability and self-healing

### 12.1 Replicas

Every device keeps the full merged state of every collection it follows, and its user's home. The hub is a meeting point, not the only copy.

### 12.2 Healing

After each full sync of a channel, a device compares its replica with the hub:

- **The hub lacks something the device holds.** If the messages on the hub for a record, taken together, don't dominate (§10.7) the device's state of that record, the device re-publishes it. That covers a record with no message left at all, and one that was rolled back to an older message. The same goes for the roster, the current epoch's inventory fragments (§8.3) and the two statements that prove a collection dead (§9.4).
- **Messages the device can't decrypt** in a channel it follows indicate corruption or junk. The device logs and ignores them.

A device heals only where that is safe:

- **Only in a current channel:** the epoch of the roster's head, and home. A channel whose `_roster` shows a later epoch is never refilled, except for that `_roster` record itself, which devices that have moved on keep complete (§11.6).
- **Not when the roster itself is missing.** If a collection's channel has no `_roster` record at all, the device first looks for a newer epoch in its home and its inbox and asks the owner for the current key (§11.4). It may restore the roster statements it holds, but an unanswered request is not proof that its old epoch is current. Data healing stays paused until it knows.
- **Not next to messages it can't read:** if the channel holds messages of a version it doesn't understand (§15), a newer client may have replaced what looks missing.

This makes vandalism by a hub participant a temporary nuisance, as long as a member device survives, while still honouring legitimate deletes (tombstones).

The hub can still withhold a newer roster statement. Signatures detect modification, not freshness, and silence proves nothing.

### 12.3 Deletions

A deleted record keeps its tombstone for good (decided 2026-11-12, §19). Every rotation carries tombstones along like any other record, so a deleted record can never be brought back by replaying an old message, in this epoch or a later one, and a device may return after any length of time without special handling.

The cost is one small message per deleted record that never goes away. Dropping tombstones safely needs a point after which no device still holds the record, and v1 has no way to establish one (Q10).

### 12.4 Hub loss

Restoring a wiped or replaced hub is an explicit act of the user, because silence can't establish that no newer roster exists. Devices then re-publish what they hold: home, and each collection with its roster. Grants already consumed don't need re-sending, because keys live in each member's home and each member's devices re-publish their own home. Pending join requests are lost and are made again. Everyone needs the new hub's credential, by hub QR (§13.1).

---

## 13. UX

The guiding rule: users never see keys, fingerprints or protocol terms unless they go looking.

### 13.1 Connecting a hub

Settings → **Sync** → **Connect**. Three ways:

1. **Scan** a hub QR code from someone who already has one. It contains the hub descriptor.
2. **Paste** a JMAP session URL and token (e.g. Fastmail: `https://api.fastmail.com/jmap/session` plus an API token made in Fastmail's settings).
3. **Enter** IMAP host, username and app password.

The hub descriptor:

```json
{ "kind": "jmap", "session": "https://api.fastmail.com/jmap/session", "token": "..." }
{ "kind": "imap", "host": "imap.example.com", "port": 993, "user": "...", "password": "..." }
```

**Shared vs per-person credentials.** Whoever sets up the hub chooses. One credential for everyone is simplest, and it is what invites and pairing codes pass on by default: the newcomer gets a copy of the credential the inviting device uses. Cutting one person off then means changing the credential and handing the new one to everyone else. A hub owner who wants to cut off a single person or a stolen device creates a token for each at the provider (Fastmail: one API token each) and enters it when inviting or pairing ("Use a separate hub token for this invite"). The protocol doesn't care which credential a device uses. The app SHOULD tell the hub owner which they're doing ("Everyone you invite gets access to this hub's mailbox").

The app shows the hub QR on request, behind device authentication. It is how a new or restored device gets hub access.

A **dedicated mail account** is strongly recommended. Tandem's change feed covers the whole account, an ordinary mail client would show the folder and might tidy it up, and the credential often allows sending mail as the account.

After connecting, the client sets up the identity (§6.1) and shows the **backup key**. It then uploads existing local data: collections that existed locally become collections on the hub with the user as owner and only member.

### 13.2 Everyday flows

| Flow | What the user sees |
|---|---|
| Create, edit, delete | Nothing special. Instant locally; a subtle sync indicator when offline or behind. |
| Rename a list | Instant, from any of the owner's devices, offline too. |
| Add a device | Device A: "Add a device" shows a QR code with a 2-minute countdown. Device B: scan; it shows a six-digit code. Device A: "Add 'Aimee's iPad'? Code 483 920" → Add, if the codes match. Everything appears on B. |
| Backup key | Settings → Sync → "Show backup key", behind device authentication. |
| A device broke or was replaced | Nothing to do. Pair the new one from another device, or type the backup key. |
| Lost every device | On a new device: connect the hub (scan the hub QR from anyone on it, or enter its details), choose "I have a backup key", type it. |
| A device was stolen, or the backup key was seen | "Start over on this device": a new identity. Your own lists are copied into new ones and you invite people again; for other people's lists you ask to be invited again (§6.4). |
| Share in person | "Share" → QR code. The other person scans it; the list appears on their phone within seconds. |
| Share remotely | "Share" → "Copy invite link" → send it over a messenger. They tap it, then see "Waiting for Jesper to approve". Jesper sees "Aimee wants to join House" next time he opens the app, and taps Approve. If he declines, Aimee sees that he did. |
| Members | The list's sharing screen shows names and roles, and to the owner the open invites and pending requests. The owner can switch someone between editing and read-only, or remove them. It takes effect at once for the owner; the key change runs in the background and needs the network. |
| Sharing change started on another device | If it got far enough to finish: "Finishing a change started on Jesper's iPhone: making Aimee read-only in House." If not: "Jesper's iPhone started making Aimee read-only in House and didn't finish. Do it here, or discard it?" |
| Someone you removed asks to join again | "Aimee was removed from House. Add her again?" Always asked, even when she scans a QR code, and it takes a new invite (§11.3). |
| Leave | "Leave list". Gone from all your devices. The owner sees "Aimee left House" and confirms. |
| Removed by the owner | "House is no longer shared with you." Unsent edits are kept (app-specific: Liszto puts them in the Inbox). |
| A list can't sync any more | "House can't sync any more. Copy it to a new list?" The copy is yours; you invite people again (§11.8). |
| Conflicts | App-specific. Tandem tells the app which fields have concurrent values. |

### 13.3 Errors

| Condition | Message |
|---|---|
| Hub unreachable | Offline indicator; everything keeps working locally. |
| Credential rejected | "Sync access was revoked or changed. Reconnect to keep syncing." Local data is untouched. |
| Sharing change without network | "Changing who a list is shared with needs a connection." |
| Collection dead | "House can't sync any more. Copy it to a new list?" Data is kept, read-only. |
| Different hub in an invite | "This invite is for a different sync hub. Liszto can only use one hub at a time." |
| Expired invite or pairing code | "This code has expired. Ask for a new one." |
| Join request declined | "Jesper declined your request to join House." |
| Hub data missing | Healing runs silently and is logged. |

---

## 14. What the hub learns

- That an account holds a folder of opaque messages, and their count, sizes and timing.
- Channel tags, and therefore how many collections, homes and inboxes are active and roughly how busy each is.
- **Who touches what.** Which connection appends, fetches and destroys which messages. With per-person or per-device credentials that ties each credential to the channels it follows and the messages it writes, so the hub can tell who is in which channel and who wrote when, by credential rather than by name. With one shared credential it sees the same per IP address.
- **Patterns.** A user's home and inbox tags never change. A rotation shows as a burst of same-sized records under a new tag, followed by one inbox message per member: that links the new tag to the old one and gives away the size of the group.
- **Not** names, user or device IDs, collection names, record contents or roles.

Tandem doesn't defend against traffic analysis by the hub. Padding message sizes to fixed buckets would hide content length; v1 doesn't (Q9).

---

## 15. Versioning

- `Tandem-Version` header and envelope version byte: both `1`.
- A client MUST ignore (not delete) messages with a version it doesn't understand and SHOULD tell the user an update is available. It MUST NOT heal (§12.2) or garbage-collect (§10.7) in a channel that holds such messages: a newer client may have replaced what looks missing.
- Within v1, payload JSON may gain keys. Clients ignore unknown keys and preserve unknown fields (§9.2).
- A new crypto suite or incompatible change is v2. Collections migrate by rotation into a v2 epoch once all members' clients support v2.

---

## 16. Prior art and alternatives considered

| Work | What it does | What Tandem takes or leaves |
|---|---|---|
| **Kolab** groupware | Stores contacts, calendars and tasks as XML attachments in IMAP messages, with folders typed via IMAP METADATA (RFC 5464); "IMAP as a NoSQL store". | Proof that mail servers make a durable object store, and that ordinary mail clients will show such folders (harmless on a dedicated account). Tandem adds encryption, CRDT merge and per-collection keys, which Kolab leaves to server ACLs. |
| **Apple Notes over IMAP** (pre-iCloud Notes) | Notes kept as messages in a `Notes` IMAP folder. | Same storage idea, single user, no merge. |
| **Delta Chat / webxdc** | Apps exchange state updates as email messages between chat members; apps use CRDTs because updates arrive in different orders. | Confirms email works as a CRDT transport. Webxdc sends updates **between** mailboxes over SMTP; Tandem shares **one** mailbox, which avoids SMTP rate limits and gives a single change feed. |
| **secsync** (Serenity Notes) | Relays end-to-end encrypted CRDT snapshots and updates through a central service; the relay can't inject data; key exchange is out of scope. | Same trust model for the relay (here, the mailbox). Tandem specifies the key management and membership that secsync leaves out. |
| **Keybase** | Per-device keys in a user sigchain; paper keys for recovery; per-user keys rotated on device revocation; per-team keys rotated on removal, lazily by admins. | Earlier drafts followed this closely: device chains, paper keys, per-user keys. Without a server to order events a device chain can fork, and settling that safely was never finished (§19). v1 has one secret per user, and per-device keys are the first candidate for a later version (Q15). |
| **Matrix** | Room membership validated by authorization rules over an event DAG; Megolm group sessions. | Matrix merges concurrent changes to who-may-do-what. An earlier draft of Tandem did the same and it proved too easy to get wrong (§19); a collection now has one signer and no rule for resolving two histories (§9.4). Megolm's ratchets give forward secrecy Tandem v1 lacks. |
| **MLS** (RFC 9420) | Standard group key agreement with forward secrecy and post-compromise security. | A candidate for a later version if forward secrecy matters. RFC 9420 §14 leaves it to the application to resolve competing commits; a single owner per collection would supply that order. Adoption and implementation cost are separate decisions. |
| **Keyhive / BeeKEM** (Ink & Switch) | Local-first capabilities and a decentralised TreeKEM variant for E2EE groups without a server. | The most relevant research direction; the formal paper is from 2026. Too new to depend on; worth tracking for v2. |
| **JMAP Sharing** (RFC 9670) and the JMAP Mail Sharing draft | Server-side sharing of data between accounts on the same server. | Would let each user have their own account, but it's server-specific, not end-to-end encrypted, and mailbox sharing is still a draft. Tandem keeps access control in cryptography so any mail account works. |
| **Nostr** | Signed events on public relays. | Considered as the transport. The relay pool is public infrastructure that shrinks and grows on others' terms; a mailbox you pay for is more predictable. The abstract store (§10.1) would admit a Nostr binding later. |
| **CloudKit** | Apple's sync with zone-wide shares. | The status quo being replaced: Apple-only, Apple-ID-bound sharing, opaque failure modes. |

References for the CRDT parts: Shapiro, Preguiça, Baquero, Zawirski, "Conflict-free Replicated Data Types" (2011); Preguiça et al., "Dotted Version Vectors" (2012); Kulkarni et al., "Logical Physical Clocks" (2014); Kleppmann, "Making CRDTs Byzantine Fault Tolerant" (PaPoC 2022).

---

## 17. Appendix: Liszto on Tandem

| Liszto (CloudKit design) | Tandem |
|---|---|
| List (one CloudKit zone) | Collection |
| Item (record) | Record; fields `title`, `notes`, `parentID`, `sortKey`, `completedAt`, `url`, … as registers; `tags` as a set |
| Inbox, Later (fixed zones) | Collections created at hub connection; their IDs are recorded in `settings:liszto` in home |
| List name, icon, tags-enabled | The `_meta` record, written only by the owner (§8.3); works offline |
| Zone-wide CKShare | Roster + invites. The list's creator is the owner; participants are writers or readers |
| Revoke share | Remove all other members (rotation) |
| "Aimee added" | Write author, verified against her user key in the roster |
| Field-level merge, `notesBase`/`notesSent` | Multi-value register: concurrent `notes` writes become siblings; Liszto resolves them with its existing conflict-text rule by writing a merged value covering both |
| Tags three-way merge, `tagsBase`/`tagsSent` | OR-set; no base bookkeeping needed |
| Delete wins | Sticky tombstone |
| Move between lists | Create in target + delete in source (unchanged) |
| `NSUbiquitousKeyValueStore` settings | `settings:liszto` in home |
| Extension direct-save | The extension appends one record message using the hub credential and the user's keys from the shared Keychain group. It shares the app's device ID, so both draw their clock values from one HLC kept in the shared container, under a lock |
| FakeCloud tests | A FakeHub implementing §10.1, with the randomized convergence tests extended along the lines of the model in `model/` (Q14) |
| Push via CloudKit subscriptions | EventSource/IDLE while running; on iOS, catch-up on launch and background refresh (no wake-up push without a push server) |

---

## 18. Open questions

| # | Question |
|---|---|
| Q1 | Does Fastmail serve JMAP EventSource push to API-token clients, and what are its JMAP rate limits and `maxObjectsInSet`? |
| Q2 | Does Fastmail accept arbitrary `Tandem-*` headers via `Email/set`, and return them unchanged on `Email/get`? |
| Q3 | Folder size: how do Fastmail and Dovecot behave with ~100k messages in one folder (Tandem's expected upper bound per hub)? |
| Q5 | Backup key format: base32 groups (§6.3) or a word list (easier to write down, adds a list to every SDK)? |
| Q9 | Padding message sizes to hide content length from the hub. |
| Q10 | Tombstones are kept for good (§12.3). Is that affordable at Liszto's deletion rate, or does v1 need a safe way to drop them? |
| Q14 | `model/owner_model.py` and `model/test_owner_model.py` model the collection rules: the owner's key as the only authority, epoch identity, fixed inventories, dead collections and how the news spreads, signing again, crash-resumable rotation, and the owner's devices coordinating through home, with a randomized run of three devices, crashes and decisions. Keys and signatures are symbols, not cryptography. Not modelled: the mail bindings, pairing and its code, invitations and UX. `tandem_model.py` and `rotation_model.py` are models of withdrawn designs (§19) and say nothing about this one. |
| Q15 | A stolen device or a found backup key can't be cut off; the user starts over (§6.4). Per-device keys would fix that. They were designed three times in this draft's history and never came out sound (§19), so they are left for a later version. |
| Q16 | A roster gains one complete statement per change and is copied at every rotation, and an inventory keeps entries for writes no rotating device has seen (§8.3). Is a signed checkpoint needed for long-lived collections? It would also have to carry who came in through which invite and has since gone, which devices now read from the full chain (§11.3). |
| Q20 | Two devices of one owner changing a collection's sharing in the same moment kill it (§11.7). The note in home makes that unlikely. If it happens in practice, a designated device per collection is the known fix (§19). |

---

## 19. Decision log

| Date | Decision |
|---|---|
| 2026-11-11 | Name: **Tandem**. |
| 2026-11-11 | One hub per client; collections don't span hubs. |
| 2026-11-11 | No OAuth-only providers: the IMAP binding uses passwords or app passwords, so Gmail and Outlook are out of scope. |
| 2026-11-11 | Data records keep version vectors, split into `context` and `base` so that a malformed context can't outlive its epoch (§9.2). |
| 2026-11-11 | Removals and demotions take effect at a key rotation. Inside an epoch nothing changes a data write's validity (§8.3). The cost: a demotion needs a rotation. |
| 2026-11-11 | Pairing always asks, showing the name the new device gives. In-person collection invites stay auto-approved. (The original decision had no code to compare; that was replaced on 2026-11-12, once pairing handed over the whole identity.) |
| 2026-11-11 | JMAP appends stay on `Email/set`, which batches; readers undo whatever transfer encoding the server chose (§10.4). The alternative, upload plus `Email/import`, would fix the stored bytes but cost a request per message. |
| 2026-11-12 | Tombstones are kept for good in v1 (§12.3). |
| 2026-11-12 | An old write enters a new epoch only through the owner's snapshot inventory, or by its author signing it again (§8.3, §11.6). The inventory says what may enter, not what must be present. A write the new state supersedes isn't signed again. |
| 2026-11-12 | An epoch is identified by the statement that starts it, and the channel tag derives from the key and that statement (§5.6, §7.1). |
| 2026-11-12 | **Withdrawn designs.** Over these two days the draft went through: per-device keys with a signed device chain, a recovery key and per-user key generations; ranked roles with admins; mergeable authorization logs; statement chains with a scored fork rule and "stubs"; a pinned controller device per collection; and an owner key with a designated device and take-over. Seven review rounds (Sol, Opus and self-reviews) kept finding defects, nearly all of them in rules for choosing between competing histories and for following a user's key changes. Only the first is in git (commit 98114c3); the others were never committed. `model/tandem_model.py` and `model/rotation_model.py` are models of two of them and are what remains. |
| 2026-11-12 | **Cut to the requirements.** A user is one secret, shared by their devices and printable as a backup; a user ID is the public signing key (§6). A collection has one owner for its whole life; members are writers or readers; only the owner changes sharing (§8). Writes stay signed, so authorship is provable and readers can't write. Two signed successors of one roster statement kill the collection, and nothing chooses between them (§9.4). A lost owner secret, a stolen device and a dead collection are all handled the same way: start again and copy the items (§6.4, §11.8). The owner's devices coordinate through a note in home, not through a designated device (§8.2, Q20). The collection's name is an owner-only record (§8.3). What this gives up: cutting off one stolen device (Q15), handing a collection to someone else, and recovering a collection in place. |
| 2026-11-12 | Review of the cut-down design (Sol): four defects in the details, none in who may do what. A new inventory carries forward entries the rotating device hasn't seen, so a write that was only missing from the hub can still be restored (§8.3). Old data is destroyed only once the new channel holds or has replaced it, and a device finishing another's change checks the snapshot against the inventory (§11.6, §11.7). A vector has at most 1,024 entries (§9.2). The note in home says what a pending change is, so it is never dropped without the user being told (§11.7). |
| 2026-11-12 | Final review pass. Pairing shows a six-digit code on both devices, bound to the new device's key and to a signed number from the showing device, so a photographed pairing QR can't be used to take the identity (§6.3). Invites moved from the roster to the owner's home, and the invite channel is keyed from the invite secret: members who don't hold an invite no longer see who asks to join through it, and creating an invite is no longer a roster change (§11.1). A join request signs the name; a removed member who asks for a key gets the signed statements that removed them; a member's `uka`, `name` and `via` can't change; a `delete` on a named record is invalid. A further review (Sol) of the vector limit: a field that needs more entries than a write may carry is not reset in place, because honest devices would sign their old writes again and undo it; the record is deleted and created again, which needs no new mechanism (§9.2). A write is not signed again if that would exceed the limit (§11.6). Only a valid write counts as having replaced another when old data is cleaned up (§11.6). |
| 2026-11-12 | Review (Sol), fourth pass: one removal race and three narrower points, none in the cryptography. An invite is closed for good once anyone it admitted has been removed, even if they later come back by another invite, and a former member is never admitted without the owner being asked; both are read from the signed roster, so a crash between removing someone and closing their invite in home no longer lets an old request back in (§11.3). Only the announcement says a key change was made: a missing note doesn't, because a note also goes when a change is discarded, and an unannounced statement is a draft, not a successor (§11.6, §11.7, §9.4). JSON that parsers read differently is invalid: repeated property names, numbers that aren't plain integers, bad UTF-8 (§10.5). The cost is that a client whose standard parser hides repeated names (Apple's Foundation does) needs its own check. Join requests can be read by everyone who holds the invite, not only by the owner; the claim was narrowed and requests are not sealed to the owner (§11.2). |
| 2026-11-12 | Review (Sol), fifth pass: no protocol finding, one model defect. The model let an invite reopen when a removed member was let back in through another invite; it now reads the roster's history, as §11.3 already said, and §11.1 and §11.3 say so in as many words. A last read of the whole draft added three clarifications and no rule: what a removed member can do about coming back (§3.3), that a discarded draft's channel is the one place a roster copy is destroyed (§10.7), and the prompt for re-adding someone who was removed (§13.2). |
| 2026-11-12 | Review (Sol), sixth pass: one gap, in how text is compared. Field names, property names, record names and channel tags are equal iff their UTF-8 bytes are; nothing is normalized or case-folded (§9.1, §10.5). Swift's string comparison treats equivalent Unicode spellings as equal and Python's doesn't, so two honest clients could keep different writes. For the same reason base64url has one spelling per value (§5.2). The cost: two field names that look alike can be different fields, which is the app's to avoid, and a Swift client compares bytes, not `String`s. |
| 2026-11-12 | Findings from the testbed work, folded in before the draft was frozen. Clean-up: two messages that dominate each other need not hold the same writes, so the tie-break by envelope hash now covers every such pair, and byte-identical copies are removed only by the device that made them (§10.7). Bindings, from probing a real server (Stalwart 1.1.0): `invalidArguments` also means a full resync; the push timeout uses the interval the server reports; a JMAP reader checks `isEncodingProblem`; an IMAP connection cap can be per account; a device knows its own messages by envelope hash, because a push can beat the answer to its own append (§10.2–§10.4). A device picks a new device ID whenever the secret is installed on it, so a phone migration can't put one ID on two devices (§5.3, §6.3). |
