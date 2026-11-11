# Tandem: encrypted, shared, real-time collections over a mailbox

Status: **draft for review**, 2026-11-11. Author: Jesper Nøhr, with Claude. "Tandem" is a working name.

This document is self-contained. It assumes no knowledge of the app that motivated it (Liszto, a to-do app; §17) and is written so that independent reviewers can check it for soundness. The words MUST, SHOULD and MAY are used in the sense of RFC 2119.

---

## 0. Summary

Tandem is a protocol for keeping **collections of records** in sync across many devices and many users, with **end-to-end encryption**, **per-collection sharing**, and **real-time updates**. It needs no server of its own. Its only infrastructure is a **mailbox**: one account on any JMAP server (Fastmail, Stalwart, Cyrus) or, through a second binding, any IMAP server.

- The mailbox account is the **hub**. Every participant's device holds a credential for it.
- Every piece of data is an **encrypted, signed message** in one folder of the hub. The hub sees opaque ciphertext, sizes and timing, nothing else.
- **Who can read a collection** is decided by who holds its key, not by the hub. **Who may change what** is decided by signatures checked on every device.
- Records merge **without coordination** (they are CRDTs: conflict-free replicated data types), so devices can work offline and in any order, and every device converges on the same state.
- A user's devices learn about all their collections from an encrypted **keyring** stored on the hub. A new device needs one QR code.
- Sharing uses **invite links or QR codes**. Removing a member **rotates the collection key**.

The trade-off, accepted on purpose: anyone holding the hub credential can **delete or withhold** data (availability), even for collections they can't read. Honest devices hold full replicas and automatically re-upload what was destroyed (§12). Tandem is meant for hubs shared by people who trust each other not to vandalise: a household, friends, a small team.

---

## 1. Goals and non-goals

### 1.1 Goals

1. **CRUD on records, real-time.** Create, read, update and delete records in collections; changes appear on other online devices within seconds (push, not polling).
2. **Offline first.** Every device has a full local replica of the collections it follows and works without the network. Sync is eventual and order-independent.
3. **Multi-user sharing.** A collection can be private (one user) or shared with other users, each with a role. A shared collection can become private again.
4. **End-to-end confidentiality and integrity.** The hub operator, and hub-credential holders who aren't members of a collection, can't read it or forge changes to it.
5. **Attribution.** Every change is signed by its author; "Aimee added this" is verifiable, not asserted.
6. **No infrastructure of our own.** Any standards-compliant mail account works as the hub. No relay, no push service, no key server.
7. **Portable.** Uses only widely available primitives (X25519, Ed25519, HKDF-SHA-256, ChaCha20-Poly1305, HPKE) so that SDKs in other languages can interoperate. Apple's CryptoKit covers all of them.
8. **Friendly UX.** One-time hub setup; QR or link for everything after that; no passwords to invent; no key fingerprints the user must compare by default.

### 1.2 Non-goals

- **Availability against hub-credential holders.** They can delete messages. Mitigated by replicas and self-healing (§12), not prevented.
- **Hiding metadata from the hub.** It sees message counts, sizes, timing, and which channel tags are active (§14).
- **Forward secrecy / post-compromise security per message.** Keys rotate on membership change, not per message. A thief who copies a collection key reads that collection's epoch. (MLS-style continuous key agreement is discussed in §16 and left for a later version.)
- **Protecting a collection from its own members.** Writers can overwrite and delete within a collection. Roles limit honest clients; they don't stop a malicious member from vandalising content they can write.
- **Huge hubs.** Every device downloads the change feed of the whole hub (bodies only for channels it follows). Designed for tens of users and thousands of collections per hub, not millions.
- **Large binary attachments.** Records are small (§10.6). Blobs are a possible later extension.

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **Hub** | One mail account (JMAP or IMAP) whose credential is shared by all participants. |
| **Binding** | How Tandem's abstract storage operations map onto JMAP (§10.2) or IMAP (§10.3). |
| **User** | A person, identified by a long-term identity derived from a 32-byte **root secret**. |
| **Device** | One installation of a Tandem client acting for a user. Has a random **device ID**. |
| **Collection** | A set of records shared by a fixed group of members (Liszto: a list). Has a stable **collection ID** and a sequence of **epochs**. |
| **Epoch** | A period during which a collection uses one symmetric **epoch key**. Membership removal starts a new epoch. |
| **Record** | An object in a collection (Liszto: an item), made of named **fields**. |
| **Write** | One signed change to one field of one record. The atomic unit of the protocol. |
| **Channel** | A stream of hub messages sharing a **channel tag** (a collection epoch, a user's home, a user's inbox, an invite). |
| **Home** | A user's private channel holding their **keyring** (the collections they belong to), their device list and per-user settings. |
| **Inbox** | A per-user channel where others post sealed messages to that user (key grants). |
| **Hub credential** | Whatever the binding needs to log in: JMAP session URL + bearer token, or IMAP host + username + password. |

---

## 3. Threat model and security properties

### 3.1 Actors

| Actor | Has | Assumed behaviour |
|---|---|---|
| **Hub operator** (e.g. Fastmail) | All stored messages, connection metadata | Honest-but-curious; may be breached; may lose data. |
| **Hub participant** | The hub credential | May be any person you gave hub access, including ex-members of your collections. Can read/write/delete raw messages. |
| **Collection member** | Hub credential + the collection's current epoch key | Can read and write the collection within their role. May misbehave within their role. |
| **Removed member** | Old epoch keys, possibly still the hub credential | Wants to keep reading or writing. |
| **Network attacker** | The wire | Defeated by TLS to the hub; Tandem doesn't add a layer here. |
| **Device thief** | One unlocked or forensically imaged device | Gets that user's root secret and every key the device holds. |

### 3.2 Properties

| Property | Holds against | Mechanism |
|---|---|---|
| Confidentiality of record contents, collection names, membership | Hub operator, non-member participants, removed members (for content after their removal) | AEAD encryption under epoch keys (§5.4); key rotation on removal (§11.6). |
| Integrity: no forged or altered writes | Everyone without the author's signing key | Ed25519 signature on every write, inside the ciphertext (§5.5). |
| Authorization: only admins change membership/metadata; readers can't write | Honest clients; any member's misbehaviour is ignored by honest clients | Roles in a signed roster; every client validates every write (§8). |
| Attribution | Members impersonating each other | Per-write signatures carried through merges. |
| Convergence | Any delivery order, duplication, loss-then-retry, replay | State-based CRDT merge (§9); deterministic tie-breaks. |
| Replay of old messages by the hub | Hub operator | Replays are harmless: merge is idempotent, and old writes are superseded by causal context (§9). |
| Rollback / withholding | **Not prevented.** Hub can hide recent messages from a device. | Detectable only when a device holds data the hub lacks; it re-uploads (§12). |
| Deletion | **Not prevented** for hub participants. | Replicas + self-healing (§12). |
| Forward secrecy | **Not provided** within an epoch. | Rotation on membership change only. |

### 3.3 What a removed member can still do

Read everything up to the moment of rotation (they had the key). If they still hold the hub credential: delete messages, and post junk that honest clients discard. Revoking their hub credential (per-person tokens, §13.1) removes even that.

---

## 4. Architecture

```
┌──────────────────────────── Application (e.g. Liszto) ────────────────────────────┐
│ record types, field encodings, UI, conflict presentation                           │
└───────────────▲───────────────────────────────────────────────────▲──────────────┘
                │ records, writes, conflicts                          │ invites, members
┌───────────────┴──────────────────── Tandem core ────────────────────┴──────────────┐
│ identity & keys (§6) · collections, epochs, roster (§7–8) · writes & merge (§9)     │
│ envelope crypto (§5) · sharing (§11) · self-healing (§12) · local replica           │
└───────────────▲───────────────────────────────────────────────────────────────────┘
                │ abstract store: append, list changes, fetch, destroy, watch (§10.1)
┌───────────────┴───────────────┐       ┌──────────────────────────────┐
│ JMAP binding (RFC 8620/8621)  │  or   │ IMAP binding (RFC 9051/3501 + │
│ Email/set, Email/changes,     │       │ UIDPLUS, CONDSTORE/QRESYNC,  │
│ EventSource push              │       │ IDLE)                        │
└───────────────────────────────┘       └──────────────────────────────┘
                         one folder named "Tandem" in one mail account
```

The core never depends on which binding is in use. A hub is either JMAP or IMAP; all devices of all participants of one hub use the same hub, though they may use different bindings to reach it if the server offers both.

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
- `b64u` = base64url without padding (RFC 4648 §5).
- `b32` = RFC 4648 base32, lowercase, no padding.

### 5.3 Keys

| Key | Size | Created | Lives |
|---|---|---|---|
| Root secret `R` | 32 B | Random, once per user | Keychain of each of the user's devices; recovery key (§6.4) |
| Identity signing key `IKs` | Ed25519 | `seed = KDF(R, "", "tandem/v1/identity-sign", 32)` | Derived on demand |
| Identity agreement key `IKa` | X25519 | `KDF(R, "", "tandem/v1/identity-agree", 32)` | Derived on demand |
| Home key `KH` | 32 B | `KDF(R, "", "tandem/v1/home", 32)` | Derived on demand |
| Epoch key `K(c,e)` | 32 B | Random, per collection epoch | User keyrings; grants |
| Invite secret `S` | 32 B | Random, per invite | Invite link/QR; roster |

**User ID:** `uid = H("tandem/v1/uid" || pub(IKs) || pub(IKa))`, 32 bytes. Displayed (when shown at all) as the first 20 bytes in `b32`, in groups of 4.

**Device ID:** 8 random bytes, generated per installation, never reused.

Deriving all identity keys from one root secret makes pairing and recovery a single 32-byte value. The cost: rotating any identity key means a new identity (§6.5).

### 5.4 Message encryption

Every hub message body (§10.4) is an **envelope**:

```
envelope = magic "TNDM" (4) || version u8 = 1 || kind u8 || salt (32) || ciphertext
```

`kind` is 1 = record state, 2 = sealed inbox message, 3 = join request (others reserved).

For symmetric kinds (1, 3), with channel key `K` (an epoch key, the home key, or an invite key):

```
mk         = KDF(K, salt, "tandem/v1/msg", 32)          # fresh per message; salt random
nonce      = 12 zero bytes                               # safe: mk is never reused
aad        = "TNDM" || u8(1) || u8(kind) || tag           # tag: the channel's tag (§5.6)
ciphertext = ChaCha20-Poly1305-Seal(mk, nonce, plaintext, aad)
```

Deriving a fresh key from a 256-bit random salt removes nonce-reuse risk entirely; there is no counter state to lose.

`plaintext` is UTF-8 JSON (§10.5). The AEAD makes any byte change detectable, and the channel tag in the AAD stops the hub from moving a message between channels.

For kind 2 (inbox), the body after `salt` is an HPKE ciphertext sealed to the recipient's `pub(IKa)` with `info = "tandem/v1/inbox"` and the same `aad`. `salt` is unused random padding there and MUST be ignored.

### 5.5 Write signatures

Each write (§9.1) is signed by its author's `IKs` over this exact byte string (independent of JSON formatting, so any SDK can reproduce it):

```
sigInput = "TANDEM-WRITE-V1"                 (15 ASCII bytes)
        || collectionID (16)
        || recordID (16)
        || lp(fieldName UTF-8)
        || u8(op)                            # 1 assign, 2 add, 3 remove, 4 delete
        || lp(element)                       # set element for add/remove; empty otherwise
        || H(value)                          # value bytes; H("") for remove/delete
        || authorUID (32)
        || deviceID (8)
        || u64(hlcMillis) || u16(hlcCounter)
        || u16(n) || n × ( deviceID (8) || u64(millis) || u16(counter) )   # context, sorted by deviceID
writeID  = H(sigInput)
```

The signature does **not** cover the epoch. A write can therefore be re-encrypted into a new epoch during key rotation (§11.6) without losing its original author's signature.

Signatures live **inside** the ciphertext, so the hub learns nothing about who wrote what.

### 5.6 Channel tags

The hub sees only channel tags, never collection IDs or user IDs:

| Channel | Tag |
|---|---|
| Collection epoch | `b32(KDF(K(c,e), "", "tandem/v1/tag", 20))` |
| Home | `b32(KDF(KH, "", "tandem/v1/tag", 20))` |
| Inbox | `b32(KDF(pub(IKa), "", "tandem/v1/inbox-tag", 20))` |
| Invite | `b32(KDF(S, "", "tandem/v1/tag", 20))` |

Tags are unlinkable across epochs of the same collection. The inbox tag is computable by anyone who knows the user's public agreement key; that is required so others can post grants. Public keys circulate only inside ciphertext and invites, so the hub doesn't learn them.

### 5.7 Key commitment

`commit(K) = H("tandem/v1/commit" || K)`. Used wherever one party announces a key that others receive separately (grants, epoch announcements), so the receiver can check they got the same key everyone else did.

---

## 6. Identity and devices

### 6.1 First run

A Tandem client works locally with no hub. When the user connects a hub (§13.1), the client generates `R`, derives the identity, creates the home channel, and writes a device record for itself into home.

### 6.2 Home channel contents

Home is a collection-like channel encrypted under `KH`, holding records of these types (all single-writer: only this user's devices write here):

| Record | Fields |
|---|---|
| `membership:<collectionID>` | `epoch`, `epochKey`, `role` (cached), `state` (`active`, `left`, `removed`), app-defined display hints |
| `device:<deviceID>` | `name`, `platform`, `addedAt`, `lastSeen` (coarse) |
| `settings:<app>` | App-defined (Liszto: list order, pinned lists) |

These are named records (§7.3). Home uses the same write and merge rules as collections (§9), with one validity rule instead of a roster: every write MUST be signed by the user's own `IKs`; anything else in the home channel is dropped. Two of the user's devices joining collections concurrently simply add different records.

### 6.3 Pairing a new device

On an existing device, "Add a device" shows a QR code containing:

```
{ "t": "pair", "v": 1, "hub": <hub descriptor §13.1>, "r": b64u(R), "exp": <unix ms, now + 120 s> }
```

The new device scans it, stores `R` and the hub credential in its Keychain, derives the identity, reads home, and starts following every active membership. The QR code is the user's entire identity, so it:

- is shown only on explicit request, full-screen, with a visible countdown;
- expires after 120 seconds (the receiving client refuses an expired code);
- is never offered as a link or for sharing.

### 6.4 Recovery key

`R` encoded for humans: `b32(R || H(R)[0..2])` = 56 characters, shown as 14 groups of 4. Offered once at hub connection ("Save this somewhere safe; it's the only way back in if you lose every device") and viewable later behind device authentication. Restoring = entering the recovery key and the hub credential.

### 6.5 Lost or stolen device

Because every device holds `R`, a stolen device is a compromised identity. The remedy is **identity rotation**:

1. On a remaining device, the user chooses "Replace my identity". The client generates `R'`, creates a new home, and copies memberships into it.
2. For each collection, it asks an admin to re-admit the new identity (an automatic join request using the existing membership as justification, approved like any other, §11.3) and to remove the old one, which rotates the key (§11.6). Collections where the user is the only admin are rotated by the client itself.
3. If hub credentials are per-device (§13.1), the stolen device's credential is revoked at the hub.

This is deliberately heavy. Per-device signing keys certified by the identity (Keybase-style) would allow cheaper device revocation; see §16 and open question Q4.

---

## 7. Collections

### 7.1 Identity and genesis

```
nonce        = 16 random bytes
collectionID = H("tandem/v1/collection" || creatorUID || nonce)[0..16]
```

The creator then writes the genesis roster entry (§8.1) containing `creatorUID` and `nonce`. Any client can recompute the collection ID from it. This anchors the roster: someone holding the key can't invent an alternative creator.

### 7.2 Epochs

A collection starts at epoch 0 with a random `K(c,0)`. Each removal (or explicit rotation) creates epoch `e+1` with a fresh key (§11.6). Members follow only the current epoch, plus the previous epoch during a grace period.

### 7.3 Reserved records

| Record ID | Purpose | Who may write |
|---|---|---|
| `_roster` | Members, roles, invites, genesis (§8) | Admins; members for their own `left` status |
| `_collection` | Collection metadata the app wants admin-controlled (Liszto: name, icon, tags-enabled) | Current admins |
| `_epoch` | Epoch announcements: `next` (in the old epoch), `genesis` (in the new) | Admins |

Record IDs for ordinary records are 16 random bytes. **Named records** (the reserved records above, and home's records in §6.2) use `id(name) = H("tandem/v1/named" || name)[0..16]` and carry their name in a `_name` field, so every client derives the same ID for the same name. A random ID colliding with a named one is as likely as a 128-bit collision.

### 7.4 Private and shared are the same thing

A private collection is a collection whose roster has one member. Sharing adds members; making it private again removes all other members, which rotates the key. There is no mode switch.

---

## 8. Roster and authorization

### 8.1 Roster fields

The `_roster` record holds:

| Field | Value (JSON) |
|---|---|
| `genesis` | `{ "creator": b64u(uid), "nonce": b64u(nonce) }` |
| `member:<b32 uid>` | `{ "role": "admin"\|"writer"\|"reader", "status": "active"\|"left"\|"removed", "sign": b64u(pub IKs), "agree": b64u(pub IKa), "name": "<display name>" }` |
| `invite:<b32 inviteID>` | `{ "secret": b64u(S), "expires": ms, "autoApprove": bool, "maxUses": n, "by": b64u(uid) }` |

Unlike other records, `_roster` is **history-retaining**: no roster write is ever garbage-collected, because validating a roster write requires the roster state it was based on.

`_roster` writes use a **record-wide context** (their context covers every roster write the author had seen, across all roster fields). Ordinary records use per-field context (§9.2).

### 8.2 Validating roster writes

A roster write `w` is valid iff, evaluating the roster formed by the valid roster writes covered by `w`'s context (its causal past):

- `genesis`: `w` has an empty context, its author equals `genesis.creator`, and the recomputed collection ID (§7.1) matches. Exactly one genesis can be valid per collection; if a key holder forges others, they fail the ID check. **Bootstrap rule:** in any roster that contains the genesis, the creator counts as an `active` `admin` until a valid `member:<creator>` write says otherwise. The creator's first act is writing their own `member:<creator>` entry (with their public keys and name), which is valid under this rule;
- `member:X` or `invite:Y` written by an **admin** of that causal-past roster; or
- `member:X` written by **X itself**, changing only `status` to `left`.

Because validity depends only on the write's causal past, every device reaches the same verdict regardless of arrival order. (This is the same idea as Matrix's authorization rules over the event graph and Keybase's sigchains, applied to a CRDT context.)

Concurrent valid writes to the same `member:X` field are resolved by **least privilege**: `removed` > `left` > `reader` > `writer` > `admin`. Two admins removing each other concurrently therefore both end up removed; a collection with no active admins is frozen (no roster or metadata changes) but stays readable and writable by remaining writers, who can duplicate it into a new collection. Clients SHOULD refuse to let the last admin leave or demote themselves.

### 8.3 Validating other writes

| Record | A write by author A is valid iff |
|---|---|
| `_collection` | A is an `active` `admin` in the **current** converged roster. A demoted admin's metadata edits stop counting. |
| `_epoch` | A was an `admin` in the roster at the write's context (record-wide context, like `_roster`). |
| Ordinary records | A has ever held role `writer` or `admin` in the roster. Writes by removed members made before removal remain (they're part of the data); after rotation a removed member can't encrypt for the new epoch at all (§11.6). |

Every write must also carry a valid Ed25519 signature by the `sign` key listed for its author in the roster. Invalid writes are dropped before merging and SHOULD be logged.

---

## 9. Records, writes and merge

### 9.1 Writes

A write is the tuple:

| Field | Meaning |
|---|---|
| `record` | 16-byte record ID |
| `field` | Field name (UTF-8; app-defined; names starting with `_` reserved) |
| `op` | `assign` (register), `add` / `remove` (set element), `delete` (record tombstone) |
| `element` | Set element bytes (add/remove only) |
| `value` | Opaque bytes (assign only); the app chooses the encoding |
| `author` | Author's user ID |
| `dot` | `(deviceID, hlc)`: unique identifier of this write's position in the author device's history |
| `context` | Version vector: for each device ID, the highest HLC of that device's writes this write supersedes |
| `sig` | Ed25519 signature over `sigInput` (§5.5) |

**HLC:** each device keeps a hybrid logical clock `(millis u48, counter u16)`. A new write's HLC is `(max(now, last.millis), counter)`, with `counter` incremented if `millis` didn't advance. A device's HLCs are strictly increasing. Devices do **not** adopt other devices' clock values: causality is tracked by `context`, not by timestamps, so a device with a wrong clock can't make its writes win over writes that saw them.

### 9.2 Field semantics

**Register (op `assign`): a multi-value register.** For each field, a device keeps the set of writes not superseded by any other kept write. Write `x` is superseded by `w` iff `x.dot.hlc ≤ w.context[x.dot.device]`.

- When the app assigns a field, the new write's context is the join (pointwise max) of the dots and contexts of all writes currently kept for that field. It therefore supersedes everything the device knew.
- If one write remains, that is the value.
- If several remain, they are **concurrent siblings**. The displayed value is the sibling with the greatest `(hlc, author, deviceID, writeID)`. The app is told about the others (Liszto keeps conflicting note text, §17). Any later assignment that covers all siblings resolves the conflict.
- At most 16 siblings are kept per field: the greatest 16 by the order above, a deterministic rule that preserves convergence.

**Set (ops `add` / `remove`): an add-wins observed-remove set.** Per element, keep add-writes not superseded by a remove-write's context. An element is present iff at least one add survives. A remove's context covers the dots of the adds the device observed for that element.

**Delete (op `delete`): sticky tombstone.** A record with any valid `delete` write is deleted, whatever else happens to it. Undeleting means creating a new record. (This is "delete wins", a deliberate product choice: an offline device editing a record someone deleted doesn't resurrect it.)

**Unknown fields** (written by a newer app version) are merged and carried like any other, so mixed versions don't lose data.

### 9.3 Record state and convergence

A record's state is the set of its valid, non-superseded writes (plus, for `_roster` and `_epoch`, all writes). Merging two states is set union followed by dropping superseded writes. This is a join on a semilattice: commutative, associative, idempotent. Any devices that have seen the same set of writes compute the same state, regardless of order, duplicates or replays. The merge rules are the standard multi-value register and OR-set with dotted version vectors (Shapiro et al. 2011; Preguiça et al. 2012).

**Byzantine dots.** A malicious member could sign two different writes with the same dot. Both are kept and treated as distinct (they have different write IDs). Coverage still uses the dot, so both are superseded together. Convergence holds; the author just gains nothing. (Cf. Kleppmann 2022 on Byzantine-tolerant CRDTs.)

### 9.4 CRUD in terms of writes

| Operation | Writes |
|---|---|
| **Create** record | New random record ID; `assign` writes for its initial fields (including the app's type field, e.g. `type`). |
| **Read** | Local: the merged state. Nothing to send. |
| **Update** field | One `assign` (or `add`/`remove`) write with full context for that field. |
| **Delete** record | One `delete` write. The tombstone is kept until the horizon (§12.3). |
| **Move** between collections | App-level: create in the target, delete in the source. Collections are separate key domains. |

---

## 10. Storage on the hub

### 10.1 Abstract store

The core needs six operations:

| Operation | Meaning |
|---|---|
| `ensureFolder()` | Make sure the single folder `Tandem` exists. |
| `append(channelTag, body) → id` | Store a new message. |
| `changes(since cursor) → (added ids with channel tags, removed ids, cursor)` | Everything that changed in the folder. |
| `fetch(ids) → bodies` | Download message bodies. |
| `destroy(ids)` | Remove messages permanently. |
| `watch(onChange)` | Push notification while connected. |

**All channels share one folder.** Each message carries its channel tag in a header (§10.4), so a client can skip bodies of channels it doesn't follow. One folder means one IDLE connection on IMAP, one change cursor, no folder-creation races and no folder-count limits.

### 10.2 JMAP binding (RFC 8620, RFC 8621)

| Operation | JMAP |
|---|---|
| `ensureFolder` | `Mailbox/query` by name `Tandem` at top level; `Mailbox/set create` if missing. On a duplicate-name error, query again (sibling names are unique, RFC 8621 §2). |
| `append` | `Email/set create` with `mailboxIds: {<Tandem>: true}`, `keywords: {"$seen": true}`, `header:Tandem-Channel:asText`, `header:Tandem-Version:asText`, and a `text/plain` body via `bodyValues` (§10.4). Batch up to the session's `maxObjectsInSet`. |
| `changes` | `Email/changes` since the stored state, then `Email/get` of created ids with properties `mailboxIds`, `header:Tandem-Channel:asText`. Ignore emails not in the Tandem mailbox. On `cannotCalculateChanges`, full resync: `Email/query` with `inMailbox`. |
| `fetch` | `Email/get` with `bodyValues`, `fetchTextBodyValues: true`. |
| `destroy` | `Email/set destroy`. |
| `watch` | EventSource push (RFC 8620 §7.3), `types=Email`. On a `StateChange`, run `changes`. |

Writes don't use `ifInState`: merging is coordination-free, so concurrent writers never conflict at the JMAP layer.

### 10.3 IMAP binding (RFC 9051 / RFC 3501)

Required extensions: **UIDPLUS** (RFC 4315), **IDLE** (RFC 2177). Strongly recommended: **CONDSTORE/QRESYNC** (RFC 7162).

| Operation | IMAP |
|---|---|
| `ensureFolder` | `LIST "" Tandem`; `CREATE Tandem` if missing (`ALREADYEXISTS` is success). |
| `append` | `APPEND Tandem (\Seen) {literal}` with the full RFC 5322 message (§10.4); UIDPLUS returns the UID. |
| `changes` | With QRESYNC: `SELECT Tandem (QRESYNC (uidvalidity modseq))` gives new/changed UIDs and `VANISHED` UIDs. With CONDSTORE only: `UID FETCH 1:* (UID) (CHANGEDSINCE modseq)` for additions; deletions by comparing `UID SEARCH ALL` with the local UID set. Neither: compare UID sets (fine at Tandem's scale). Channel tags via `UID FETCH (BODY.PEEK[HEADER.FIELDS (Tandem-Channel)])`. A `UIDVALIDITY` change means a full resync. |
| `fetch` | `UID FETCH (BODY.PEEK[TEXT])`. |
| `destroy` | `UID STORE +FLAGS.SILENT (\Deleted)` then `UID EXPUNGE` (UIDPLUS, so other messages aren't affected). |
| `watch` | `IDLE` on the selected `Tandem` folder; `EXISTS`/`EXPUNGE`/`VANISHED` responses trigger `changes`. Re-issue IDLE at least every 29 minutes (RFC 2177). |

**Why IMAP too:** it works with essentially every mail provider (iCloud Mail, Gmail, Outlook, self-hosted Dovecot), where JMAP is today mostly Fastmail, Stalwart and Cyrus. **Costs:**

- the client needs an IMAP implementation (Apple's `swift-nio-imap` is a protocol codec, not a full client);
- push needs a long-lived TCP connection, which mobile OSes suspend;
- providers vary: Gmail requires OAuth for third-party apps and treats folders as labels, and some servers lack QRESYNC.

The JMAP binding is the reference; the IMAP binding is second.

### 10.4 Message format

Both bindings store the same logical message:

```
From: Tandem <tandem@invalid>
Date: <time of append>
Message-ID: <random@tandem.invalid>
Subject: tandem
MIME-Version: 1.0
Tandem-Version: 1
Tandem-Channel: <channel tag>
Content-Type: text/plain; charset=us-ascii
Content-Transfer-Encoding: 7bit

<base64 of the envelope (§5.4), wrapped at 76 characters>
```

Nothing else is in the clear. The hub already knows the append time.

### 10.5 Plaintext payloads (inside the envelope)

**Kind 1, record state.** One message holds the current state of one record:

```json
{
  "rec": "<b64u record id>",
  "writes": [
    { "f": "title", "op": 1, "v": "<b64u value>", "a": "<b64u author uid>",
      "d": ["<b64u device id>", 1731312000123, 0],
      "c": [["<b64u device id>", 1731311999000, 2]],
      "s": "<b64u signature>" },
    { "f": "tags", "op": 2, "e": "<b64u element>", "a": "...", "d": [...], "c": [...], "s": "..." }
  ]
}
```

HLC millis (≤ 2^48) and counters are JSON numbers, which every JSON parser represents exactly. Clients MUST use a real JSON serializer and MUST ignore unknown keys.

**Kind 2, inbox message** (HPKE-sealed): grants (§11.4) and epoch notices.

**Kind 3, join request** (invite channel, §11.3).

### 10.6 Sizes

A record message SHOULD stay under 64 KiB and MUST stay under 1 MiB. Apps put large content elsewhere or split it.

### 10.7 Publishing and garbage collection

A device publishes a record by appending a kind-1 message with the record's **full current state**. A message `m` is **dominated** by a later message `n` of the same record if every write in `m` is either in `n` or superseded by a write in `n`.

- After an append succeeds, the device MAY destroy messages for that record that its new message dominates. It MUST NOT destroy a message it hasn't merged.
- Concurrent publishers each leave one message; the next device to merge them publishes a dominating message and destroys both. A record converges to one message on the hub between bursts of activity.
- Writes made in quick succession SHOULD be coalesced (debounce ~1 s) into one publish, so typing doesn't create a message per keystroke.

---

## 11. Sharing

### 11.1 Invites

An admin creates an invite: random `inviteID` (16 B) and `S` (32 B), recorded as `invite:<id>` in the roster. The invite payload is:

```json
{ "t": "join", "v": 1, "hub": <hub descriptor §13.1, optional>,
  "c": "<b64u collection id>", "i": "<b64u invite id>", "s": "<b64u S>",
  "by": { "uid": "...", "sign": "...", "agree": "...", "name": "Jesper" },
  "hint": "House", "exp": <unix ms> }
```

`hint` (the collection name) is optional and is visible to anyone who sees the link. Clients SHOULD let the inviter turn it off.

Two delivery forms:

| Form | Default policy | Use |
|---|---|---|
| **QR code** shown on screen | `autoApprove: true`, expires in 10 minutes, 1 use | In person: scan and you're in. |
| **Link** `https://<app domain>/join#<b64u(payload)>` | `autoApprove: false`, expires in 7 days | Sent over a messenger; an admin approves the request. |

The payload sits in the URL **fragment**, which browsers never send to a server. The app domain needs only a static page and universal-link association.

The hub credential is in the payload so a newcomer can join with one scan. That is the shared-hub model: whoever obtains the link obtains hub access (§3.3).

### 11.2 Join request

The joiner's client:

1. If it has no hub, configures the one in the payload. If it has a **different** hub, it stops with a clear error (one hub per client in v1; Q6).
2. Ensures it has an identity (§6.1).
3. Appends a kind-3 message to the invite channel, encrypted with `Kinv = KDF(S, "", "tandem/v1/invite-key", 32)`:

```json
{ "uid": "...", "sign": "...", "agree": "...", "name": "Aimee", "device": "Aimee's iPhone",
  "inv": "<b64u invite id>", "sig": "<Ed25519 over 'TANDEM-JOIN-V1' || collectionID || inviteID || uid || sign || agree>" }
```

4. Shows "Waiting for Jesper to approve" (or nothing, for auto-approve) and watches its inbox.

### 11.3 Approval

Admins' devices follow the invite channels of their collections' open invites. On a valid request (correct signature, invite open, not expired, uses left):

- **Auto-approve** invites are approved by the first admin device that sees the request.
- Others show "Aimee wants to join House" with Approve / Decline.

Approval is: (a) a roster write `member:<uid>` with role `writer` (or the role chosen in the invite UI), and (b) a **grant** to the joiner's inbox (§11.4). The admin then marks the request handled by destroying it, and decrements uses (or closes the invite) with a roster write.

Concurrent approval by two admin devices produces two identical-in-effect roster writes (merged) and two grants (harmless duplicates).

### 11.4 Grants

A grant is a kind-2 message to the recipient's inbox:

```json
{ "type": "grant", "c": "<collection id>", "e": <epoch>, "k": "<b64u epoch key>",
  "by": "<granter uid>", "sig": "<Ed25519 over 'TANDEM-GRANT-V1' || recipientUID || collectionID || u32(epoch) || commit(k) || granterUID>" }
```

The recipient's device decrypts it, then fetches the collection's channel with `k`. It verifies that the granter was an admin and that the roster lists the recipient as an active member. Only then does it add a `membership` record to home and destroy the grant. A grant that fails verification is destroyed and logged.

### 11.5 Leaving

A member writes `member:<self>` with `status: left`, removes the membership from home, and stops following. Admin devices that see a `left` status SHOULD rotate the key (§11.6), because the leaver still has it. Clients MAY batch this.

### 11.6 Removal and key rotation

To remove member X (or rotate for any reason), an admin device:

1. Writes `member:X` with `status: removed` in the current epoch `e`.
2. Generates `K(c,e+1)`, and in the **new** channel writes `_epoch.genesis = { "epoch": e+1, "prev": e, "prevCommit": commit(K(c,e)) }`.
3. Re-encrypts the current state of every record into the new channel. Writes keep their original signatures (§5.5); `_roster` and `_epoch` histories are copied in full.
4. Sends a grant for `e+1` to every remaining active member's inbox, including the admin's own home (via its membership record).
5. Writes `_epoch.next = { "epoch": e+1, "commit": commit(K(c,e+1)) }` in the **old** channel.

Members' devices that see `_epoch.next` in the old channel and hold a matching grant switch to `e+1`. A device that sees `_epoch.next` but finds its user `removed` in the roster knows it was removed: it surfaces any unsent local edits to the app (Liszto moves them to the Inbox) and deletes its replica. A device that sees `_epoch.next` but has no grant yet keeps waiting.

**Late writes.** For 30 days after rotation, devices following both epochs forward writes that appear in the old channel into the new one. They forward only writes whose author is an active member of the new epoch's roster; anything by the removed member is not forwarded, however it is timestamped. After the grace period, an admin device destroys every message in the old channel.

**Concurrent rotations.** If two admins create epoch `e+1` at once, the winner is the genesis with the lowest write ID. The losing admin's device merges its epoch's content into the winner and destroys its own channel. Members holding grants for both follow the winner.

### 11.7 Deleting a collection

An admin writes a `delete` on `_collection`. Members' devices remove the collection locally and from home. After 30 days an admin device destroys all its messages. Any hub participant could destroy them sooner; that's the availability trade-off (§1.2).

---

## 12. Availability and self-healing

### 12.1 Replicas

Every device keeps the full merged state of every collection it follows, plus its home. The hub is a meeting point, not the only copy.

### 12.2 Healing

After each full sync of a channel, a device compares its replica with the hub:

- A record the device holds (not past the horizon) that has **no message at all** on the hub was destroyed by someone other than the GC rule, which never removes a record's last message. The device re-publishes it.
- A device that sees records on the hub it can't decrypt in a channel it follows logs it (corruption or junk) and ignores them.

This makes vandalism by a hub participant a temporary nuisance as long as any member device survives, while honouring legitimate deletes (tombstones).

### 12.3 Tombstone horizon

Tombstoned records are kept for **180 days** (a protocol constant in v1), then any device MAY destroy their message. A device whose last successful full sync of a channel is older than the horizon is **stale**. It MUST NOT heal: it may be holding records whose tombstones have since been collected. Instead it resyncs from scratch, and hands the app any local writes the hub doesn't reflect, as rescued data rather than silently re-uploading.

### 12.4 Hub loss

If the hub is wiped or replaced, any device can re-publish everything it holds (home, collections, rosters with full history). Grants already consumed don't need re-sending: keys live in each member's home, and each member's devices re-publish their own home.

---

## 13. UX

The guiding rule: users never see keys, fingerprints or protocol terms unless they go looking.

### 13.1 Connecting a hub

Settings → **Sync** → **Connect**. Three ways:

1. **Scan** a hub QR code from someone who already has one (the person who set up the hub, or any device). It contains the hub descriptor.
2. **Paste** a JMAP session URL and token (e.g. Fastmail: `https://api.fastmail.com/jmap/session` plus an API token made in Fastmail's settings).
3. **Enter** IMAP host, username and app password.

The hub descriptor:

```json
{ "kind": "jmap", "session": "https://api.fastmail.com/jmap/session", "token": "..." }
{ "kind": "imap", "host": "imap.example.com", "port": 993, "user": "...", "password": "..." }
```

**Shared vs per-person credentials.** Whoever sets up the hub chooses. One credential for everyone is simplest. One per person (Fastmail: one API token each) lets them cut off a single person at the hub. The protocol doesn't care. The app SHOULD tell the hub owner which they're doing ("Everyone you invite gets access to this hub's mailbox").

After connecting, the client shows the **recovery key** once (§6.4), then uploads existing local data. Collections that existed locally before the hub was connected become collections on the hub with the user as sole admin.

### 13.2 Everyday flows

| Flow | What the user sees |
|---|---|
| Create, edit, delete | Nothing special. Instant locally; a subtle sync indicator when offline or behind. |
| Add a device | Device A: "Add a device" shows a QR code with a 2-minute countdown. Device B: scan, and everything appears. |
| Share in person | "Share" → QR code. The other person scans it; the list appears on their phone within seconds. |
| Share remotely | "Share" → "Copy invite link" → send it over iMessage. They tap it, then see "Waiting for Jesper to approve". Jesper sees "Aimee wants to join House" next time he opens the app, and taps Approve. |
| Members | The list's sharing screen shows names, roles, pending requests, and "Remove". Removing is instant for the user; key rotation happens in the background. |
| Leave | "Leave list". Gone from all your devices. |
| Removed by someone else | "House is no longer shared with you." Unsent edits are kept (app-specific: Liszto puts them in the Inbox). |
| Conflicts | App-specific. Tandem tells the app which fields have concurrent values. |
| Lost a device | Settings → Sync → "Replace my identity" (§6.5), with a plain explanation. |

### 13.3 Errors

| Condition | Message |
|---|---|
| Hub unreachable | Offline indicator; everything keeps working locally. |
| Credential rejected | "Sync access was revoked or changed. Reconnect to keep syncing." Local data is untouched. |
| Different hub in an invite | "This invite is for a different sync hub. Liszto can only use one hub at a time." |
| Expired invite or pairing code | "This code has expired. Ask for a new one." |
| Stale device | "This device was offline for a long time. Reloading from the hub; nothing you wrote here is lost." |
| Hub data missing | Healing runs silently and is logged. |

---

## 14. What the hub learns

- That an account holds a folder of opaque messages, and their count, sizes and timing.
- Channel tags, and therefore how many collections (or epochs) are active and roughly how busy each is.
- Which IP addresses connect (and, with per-person credentials, which credential), and when.
- **Not** user identities, names, collection names, record contents, membership, roles, or who wrote what.

A user wanting to hide even sizes could pad messages to fixed buckets; v1 doesn't (Q9).

---

## 15. Versioning

- `Tandem-Version` header and envelope version byte: both `1`.
- A client MUST ignore (not delete) messages with a version it doesn't understand and SHOULD tell the user an update is available.
- Within v1, payload JSON may gain keys. Clients ignore unknown keys and preserve unknown fields (§9.2).
- A new crypto suite or incompatible change is v2. Collections migrate by rotation into a v2 epoch once all members' clients support v2.

---

## 16. Prior art and alternatives considered

| Work | What it does | What Tandem takes or leaves |
|---|---|---|
| **Kolab** groupware | Stores contacts, calendars and tasks as XML attachments in IMAP messages, with folders typed via IMAP METADATA (RFC 5464); "IMAP as a NoSQL store". | Proof that mail servers make a durable object store, and that ordinary mail clients will show such folders (harmless on a dedicated account). Tandem adds encryption, CRDT merge and per-collection keys, which Kolab leaves to server ACLs. |
| **Apple Notes over IMAP** (pre-iCloud Notes) | Notes kept as messages in a `Notes` IMAP folder. | Same storage idea, single user, no merge. |
| **Delta Chat / webxdc** | Apps exchange state updates as email messages between chat members; apps use CRDTs because updates arrive in different orders. | Confirms email works as a CRDT transport. Webxdc sends updates **between** mailboxes over SMTP; Tandem shares **one** mailbox, which avoids SMTP rate limits and gives a single change feed. A future multi-hub version could federate the webxdc way (Q6). |
| **secsync** (Serenity Notes) | Relays end-to-end encrypted CRDT snapshots and updates through a central service; the relay can't inject data; key exchange is out of scope. | Same trust model for the relay (here: the mailbox). Tandem specifies key management and membership, which secsync leaves out. |
| **Keybase teams** | Per-team keys in a signed chain; removal rotates the key and re-encrypts it for remaining members; servers orchestrate lazy rotation. | The roster-plus-rotation design (§8, §11.6) follows it. Keybase's per-device keys (cheap device revocation) are a candidate for v2 (Q4). |
| **Matrix** | Room membership validated by authorization rules over an event DAG; Megolm group sessions. | Causal-past authorization (§8.2) is the same idea on a CRDT context. Megolm's ratchets give forward secrecy Tandem v1 lacks. |
| **MLS** (RFC 9420) | Standard group key agreement with forward secrecy and post-compromise security. | Rejected for v1: it needs a delivery service that orders handshake messages, plus a sizeable implementation (no CryptoKit-native one). Candidate for v2 if forward secrecy matters. |
| **Keyhive / BeeKEM** (Ink & Switch) | Local-first capabilities and a decentralised TreeKEM variant for E2EE groups without a server. | The most relevant research direction; formal paper is 2026. Too new to depend on; worth tracking for v2. |
| **JMAP Sharing** (RFC 9670) and the JMAP Mail Sharing draft | Server-side sharing of data between accounts on the same server. | Would let each user have their own account, but it's server-specific, not end-to-end encrypted, and mailbox sharing is still a draft. Tandem keeps access control in cryptography so any mail account works. |
| **Nostr** | Signed events on public relays. | Considered as the transport. The relay pool is public infrastructure that shrinks and grows on others' terms; a mailbox you pay for is more predictable. The core could gain a Nostr binding later; the abstract store (§10.1) fits. |
| **CloudKit** | Apple's sync with zone-wide shares. | The status quo being replaced: Apple-only, Apple-ID-bound sharing, opaque failure modes. |

References for the CRDT parts: Shapiro, Preguiça, Baquero, Zawirski, "Conflict-free Replicated Data Types" (2011); Preguiça et al., "Dotted Version Vectors" (2012); Kulkarni et al., "Logical Physical Clocks" (2014); Kleppmann, "Making CRDTs Byzantine Fault Tolerant" (PaPoC 2022).

---

## 17. Appendix: Liszto on Tandem

| Liszto (CloudKit design) | Tandem |
|---|---|
| List (one CloudKit zone) | Collection |
| Item (record) | Record; fields `title`, `notes`, `parentID`, `sortKey`, `completedAt`, `url`, … as registers; `tags` as a set |
| Inbox, Later (fixed zones) | Collections created at hub connection; their IDs are recorded in `settings:liszto` in home |
| List name, icon, tags-enabled | `_collection` (admin-only, matching "only the owner renames") |
| Zone-wide CKShare | Roster + invites |
| Revoke share | Remove all other members (rotation) |
| "Aimee added" | Write author, verified by signature |
| Field-level merge, `notesBase`/`notesSent` | Multi-value register: concurrent `notes` writes become siblings; Liszto resolves them with its existing conflict-text rule by writing a merged value covering both |
| Tags three-way merge, `tagsBase`/`tagsSent` | OR-set; no base bookkeeping needed |
| Delete wins | Sticky tombstone |
| Move between lists | Create in target + delete in source (unchanged) |
| `NSUbiquitousKeyValueStore` settings | `settings:liszto` in home |
| Extension direct-save | The extension appends one record message using the hub credential from the shared Keychain group |
| FakeCloud tests | A FakeHub implementing §10.1 (both bindings' semantics), with the randomized convergence tests unchanged in spirit |
| Push via CloudKit subscriptions | EventSource/IDLE while running; on iOS, catch-up on launch and background refresh (no wake-up push without a push server) |

---

## 18. Open questions

| # | Question |
|---|---|
| Q1 | Does Fastmail serve JMAP EventSource push to API-token clients, and what are its JMAP rate limits and `maxObjectsInSet`? |
| Q2 | Does Fastmail accept arbitrary `Tandem-*` headers via `Email/set`, and preserve them byte-for-byte on `Email/get`? |
| Q3 | Folder size: how do Fastmail and Dovecot behave with ~100k messages in one folder (Tandem's expected upper bound per hub)? |
| Q4 | Per-device signing keys certified by the identity key, so a lost device can be revoked without replacing the identity. Worth the complexity in v1? |
| Q5 | Recovery key format: base32 groups (§6.4) or a word list (easier to write down, adds a list to every SDK)? |
| Q6 | One hub per client in v1. Should a client support several hubs (e.g. a household hub and a work hub), and could collections span hubs by mailing updates between them, webxdc-style? |
| Q7 | Join-request verification: should in-person QR joins also show a short code on both screens to confirm (protects against someone photographing the QR)? |
| Q8 | IMAP binding with Gmail: OAuth is mandatory for third-party apps there. In scope, or IMAP limited to app-password providers? |
| Q9 | Padding message sizes to hide content length from the hub. |
| Q10 | The 30-day old-epoch grace period and 180-day tombstone horizon: right defaults? Per-collection setting? |
| Q11 | Name. |
