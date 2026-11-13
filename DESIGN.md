# Tandem: encrypted, shared, real-time collections over a mailbox

Status: **participant/device architecture draft**, 2026-11-13. This revision adopts one participant record per person per hub, keypairs kept in the system keychain, and hub management unlocked by a hub password. It incorporates the current working draft's IMAP-only transport, directory and grow-only admission answers. The implemented shared-secret v1 is a different protocol. This draft calls the incompatible successor **v2**. Its control-object encodings are fixed (§10.8). Nothing uses v1, so v2 replaces it outright: there is no migration. The model and crypto tests cover selected mechanisms, not a complete implementation or security sign-off.

This document is self-contained. It assumes no knowledge of the app that motivated it (Liszto, a to-do app; §17) and is written so that independent reviewers can check it for soundness. The words MUST, SHOULD and MAY are used in the sense of RFC 2119.

---

## 0. Summary

Tandem is a protocol for keeping **collections of records** in sync across many devices and many users, with **end-to-end encryption**, **per-collection sharing**, and **real-time updates**. It needs no server of its own. Its only infrastructure is a **mailbox**: one account on any IMAP server that takes a password or an app password (Fastmail, Gmail, iCloud Mail, Stalwart, Dovecot and most others).

- The mailbox account is the **hub**. Every participant's device holds a credential for it. A client can be on several hubs at once; each is a world of its own.
- Every piece of data is an **encrypted, signed message** in one folder of the hub. The hub sees opaque ciphertext, sizes and timing, nothing else.
- **Who can read a collection** is decided by who holds its key, not by the hub. **Who may change what** is decided by signatures checked on every device.
- Records merge **without coordination** (they are CRDTs: conflict-free replicated data types), so devices work offline and in any order, and every device converges on the same state.
- A **participant** is an automatically created record within one hub. Their keys live in the system keychain, so a new phone has them; the devices share one encrypted **home**. Lists created or accepted on one device appear on the others without another invitation (§6). There is no personal identity key or password.
- Every collection has **one owner**, fixed when it is created, who alone decides membership, roles and key changes. Other members are writers or readers.
- A member creates a pairing QR/link on an enrolled device and opens it on the new device. The **hub admin approves** the request with the hub password. Devices that share a keychain need no pairing at all. Approval releases access to the member's home, including future lists. No second member confirmation or comparison code is required (§6.3).
- Creating a hub asks its creator to choose a **hub password**. Joining through a **hub invite** creates the newcomer's participant record and first-device admission; members are asked for no password. Collections are shared with people from the hub directory. Removing a device rotates exposed home/list keys as well as retiring its certificate (§6.4). Content edits work offline; administration and sharing changes need the network.

The trade-off, accepted on purpose: anyone holding the hub credential can **delete or withhold** data (availability), even for collections they can't read. Honest devices hold full replicas and re-upload what was destroyed (§12). Tandem is meant for hubs shared by people who trust each other not to vandalise: a household, friends, a small team.

---

## 1. Goals and non-goals

### 1.1 Goals

1. **CRUD on records, real-time.** Create, read, update and delete records in collections; changes appear within seconds on other devices where the app is running (push, not polling).
2. **Offline content editing.** Every device has a local replica and can edit content without the network. Content merge is eventual and order-independent. Membership and keys are changed by the owner, one change at a time; they are not offline CRDT operations.
3. **Multi-user sharing.** A collection can be private (one user) or shared with other users, each with a role. A shared collection can become private again.
4. **End-to-end confidentiality and integrity.** The hub operator, and hub-credential holders who aren't members of a collection, can't read it or forge changes to it.
5. **Attribution.** Every change is signed by an admitted device key, bound to its participant record and immutable authorization snapshot. The UI can show “Alice added this”; details identify the particular device. A display name is not proof of a physical person.
6. **Device loss.** A new or replaced device that shares the keychain has the keys already. A stolen device's key is revoked; the user's other devices that share it make a new one (§6.4). The hub password can recover the creator's access, not another member's private data. A member who loses the keychain too rejoins as a new participant. Conflicting collection histories still require copying (§11.8).
7. **No infrastructure of our own.** Any standards-compliant mail account works as the hub. No relay, no push service, no key server.
8. **Portable.** Uses widely available primitives (X25519, Ed25519, HKDF-SHA-256, ChaCha20-Poly1305, HPKE) so that SDKs in other languages can interoperate, plus Argon2id for the hub password. Apple's CryptoKit covers all but Argon2id, which apps take from a library such as libsodium (decided 2026-11-13).
9. **Friendly UX.** Choose one hub password when creating a hub; nobody who joins needs one. A new phone that shares the keychain just works. Other devices pair by QR/link and one admin approval; regular list use needs no hub password (§13). The link is a sensitive bearer delegation (§6.3).

### 1.2 Non-goals

- **Collection administration by anyone but its owner.** The hub admin manages device admission and revocation, not other people's collection membership or data. Collection ownership does not move; handing it on requires a copy (§11.8).
- **Choosing between conflicting roster histories.** Two signed successors of one roster statement kill the collection (§9.4). No clock, chain length or later signature picks a winner.
- **Instant revocation or remote erasure.** Revocation retires device authority; confidentiality also needs home/list rekeys and provider credential revocation. Offline clients may remain stale. Downloaded data cannot be erased (§6.4).
- **Recovering a dead collection in place.** A proven roster fork, or loss of all owner authority without applicable creator recovery, requires a readable-data copy into a new collection (§11.8).
- **Availability against hub-credential holders.** They can delete messages. Mitigated by replicas and self-healing (§12), not prevented.
- **Hiding metadata from the hub.** It sees message counts, sizes, timing, which channel tags are active, and which connection reads and writes which of them (§14).
- **Forward secrecy / post-compromise security per message.** Keys rotate on membership changes, not per message. Whoever holds a collection key reads that collection's epoch. (MLS-style continuous key agreement is discussed in §16 and left for a later version.)
- **Protecting a collection from its own members.** Writers can overwrite and delete within a collection. Roles limit honest clients; they don't stop a malicious member from vandalising content they can write, including keeping a field in conflict (§9.3). Removing them ends it.
- **Huge hubs.** Every device downloads the change feed of the whole hub (bodies only for channels it follows). Designed for tens of users and thousands of collections per hub, not millions.
- **Large binary attachments.** Records are small (§10.6). Blobs are a possible later extension.
- **Collections that span hubs.** A client can be on several hubs (§6.5), but every collection, home and inbox lives on exactly one hub, and nothing crosses between hubs. Moving a collection to another hub is a copy (§11.8).
- **OAuth-only mail providers**, such as Outlook.com for personal accounts. The IMAP binding targets servers that accept a password or app password. Gmail works with an app password, at the costs listed in §10.3.

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **Hub** | One mail account that every participant's devices can log in to over IMAP, with one shared credential or with one each (§13.1). |
| **Binding** | How Tandem's abstract storage operations map onto a mail protocol. Version 2 has one binding, IMAP (§10.3). |
| **User / participant / member record** | A person's stable record within one hub. Its content hash is the participant ID (`uid`); it groups the approved keychain keypairs of their devices. It has no personal identity secret (§6.1). “Member” also means a participant listed in a collection roster. |
| **Hub admin** | Someone able to unlock the hub management seed using its password. Approves device enrollment/revocation; gains no access to other participants' private homes or list-owner powers. |
| **Hub password** | Chosen by whoever creates a hub. Entered on any device, it opens the admin capsule on the hub and with it the hub's management (§6.7). It can be changed, but a leaked one means a new hub (§6.8). |
| **Device** | One installation with its own random dot namespace and durable HLC. Its Ed25519/X25519 pair and admission certificate belong to its keychain and may be shared with other installations through platform keychain sync (§5.3). |
| **Home** | A participant's private, epoch-keyed channel on one hub, shared continuously by all their approved devices. Holds memberships, list keys, settings and private activity. |
| **Inbox** | A stable per-participant channel. Each HPKE envelope addresses one specific admitted device agreement key; there is no shared private inbox key. |
| **Collection** | A set of records shared by a group of members (Liszto: a list). Has a stable **collection ID** and a sequence of **epochs**. |
| **Owner** | The user who created a collection. Only the owner changes its sharing, and the owner never changes (§8.1). |
| **Role** | Owner, writer or reader. Writers change records; readers only read. |
| **Roster** | A collection's chain of statements signed by the owner: who is a member, in which role, and which key is current (§8). Everything else in a collection is a **data record**. |
| **Epoch** | A period during which a collection uses one symmetric **epoch key**. Removing a member or lowering a role starts a new epoch. |
| **Record** | An object in a collection (Liszto: an item), made of named **fields**. |
| **Write** | One signed change to a record: to one of its fields, or deleting it. The atomic unit of the protocol. |
| **Channel** | A stream of hub messages sharing a **channel tag** (a collection epoch, a home, an inbox, a hub directory, a pairing session). |
| **Hub invite** | Connection details, a credential, authority pins and a manager-signed capability for a new participant's first-device admission (§13.1). Grants no collection access and cannot enroll a device as an existing participant. |
| **Hub key** | A random key shared by every device on a hub, carried in hub invites. It keys the hub's directory (§6.6). |
| **Directory** | The hub's participant records, verified admission/revocation proofs and device-signed profile entries, readable by everyone holding HubK (§6.6). |
| **Hub credential** | What a device logs in with: the password or app password that goes with the hub's IMAP host and username. |

---

## 3. Threat model and security properties

### 3.1 Actors

| Actor | Has | Assumed behaviour |
|---|---|---|
| **Hub operator** (e.g. Fastmail) | All stored messages, connection metadata | Honest-but-curious; may be breached; may lose data. |
| **Hub participant** | The hub credential, and with a hub invite also the hub key | May be any person you gave hub access, including ex-members of your collections. Can read/write/delete raw messages, and read the hub's directory (§6.6). |
| **Collection member** | Hub credential + the collection's current epoch key | Can read, and as a writer change, the collection's records. May misbehave within their role. |
| **Removed member** | Old epoch keys, possibly still the hub credential | Wants to keep reading or writing. |
| **Network attacker** | The wire | Defeated by TLS to the hub; Tandem doesn't add a layer here. |
| **Device thief** | One unlocked or forensically imaged device | Gets its device keys and cached data keys. Cannot unlock hub management without the password, but can act as that participant until device-policy/key cutovers. Revocation cannot erase prior data (§6.4). |
| **Malicious or compromised hub admin** | Management seed, directory and mailbox access | Can admit new lookalike participants, revoke devices and deny service. Cannot forge an existing participant's pairing authorization or decrypt their home without additional secrets. Capturing a member's bearer pairing link defeats this separation for that package (§6.3). |

### 3.2 Properties

| Property | Holds against | Mechanism |
|---|---|---|
| Confidentiality of record contents, names and roles | Hub operator, non-member participants, and removed members for content written after they are cut off (§3.3) | AEAD under epoch keys (§5.4); a new key with every removal and demotion (§8.2, §11.6). |
| Integrity: no forged or altered writes | Everyone without the authoring device's private signing key | Device signature plus exact certificate and source authorization snapshot, inside ciphertext (§5.5, §8.3). |
| Authorization: only the owner changes the roster; readers cannot write | Other participants, including the hub admin without the owner's approval | Genesis fixes the owner participant. Admitted owner devices sign the roster; each data write names its roster, device certificate and authorization snapshot (§8). |
| Attribution | Participants impersonating an existing participant | Device possession proofs, member-authorized pairing and admin-signed certificates. Participant IDs are records, not signing keys. Display names and physical-person intent remain human judgments (§6). |
| Content convergence | Any delivery order, duplication and replay of the same accepted writes; malicious writers | The field merge of §9.2–9.3. |
| One roster history | Not guaranteed. | Two signed successors of one statement kill the collection instead of being merged or ranked (§9.4). |
| Replay of old messages | Hub operator, hub participants | Merge is idempotent. Inside an epoch superseded writes stay superseded and tombstones stay. Ciphertext from an old epoch doesn't decrypt in a new one, and an old write is accepted there only if the owner listed it (§8.3). Tombstones are permanent (§12.3). |
| Cutting off a stolen device | Removed device after the relevant cutovers | Permanent certificate/key revocation, new home/list keys, fixed inventories and device-policy floors (§6.4, §8.3). Stale/offline writes and historical roster-fork denial of service remain possible. |
| Rollback / withholding | **Not prevented.** Hub can hide recent messages from a device. | Detectable only when a device holds data the hub lacks; it re-uploads what is missing (§12.2). |
| Deletion | **Not prevented** for hub participants. | Replicas + self-healing (§12). |
| Forward secrecy | **Not provided** within an epoch. | Rotation on removal and demotion only. |
| Traffic analysis | **Not prevented.** | The hub sees which connection reads and writes which channel, and when (§14). |

### 3.3 What a removed member can still do

**Read** everything written under keys they hold: all content up to the rotation, and whatever other devices still publish to the old epoch in the moments before they see the new one announced (§11.6).

**Write** until the rotation. Afterwards the roster no longer lists them, they can't decrypt the new epoch, and a backdated write is absent from its fixed snapshot inventory (§8.3).

**Come back**, only if the owner adds them again (§11.3). They hold nothing that would let them in on their own.

**Vandalise**, if they still hold the hub credential: delete messages, including other people's key grants (retained and repaired, §11.4), and post junk that honest clients discard. Revoking the hub credential (a password per person or per device, §13.1) removes even that.

Removing one device while its participant remains a member is a different cut: the roster still names the participant, but the new device-policy floor excludes the removed certificate/keys and grants go only to surviving devices. Historical writes are preserved through fixed inventories; an old owner device can still cause the historical-roster denial of service described in §9.4.

---

## 4. Architecture

```
┌──────────────────────────── Application (e.g. Liszto) ──────────────────────────────┐
│ record types, field encodings, UI, conflict presentation                            │
└───────────────▲─────────────────────────────────────────────────────▲───────────────┘
                │ records, writes, conflicts                          │ sharing, members, devices
┌───────────────┴──────────────────── Tandem core ────────────────────┴───────────────┐
│ devices & users (§6) · collections, epochs, roster (§7–8) · writes & merge (§9)     │
│ envelope crypto (§5) · sharing (§11) · self-healing (§12) · local replica           │
└───────────────▲─────────────────────────────────────────────────────────────────────┘
                │ abstract store: append, list changes, fetch, destroy, watch (§10.1)
┌───────────────┴─────────────────────────────────────────────────────────────────────┐
│ IMAP binding (RFC 9051/3501 + UIDPLUS, IDLE, CONDSTORE/QRESYNC)                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
                         one folder named "Tandem" in one mail account
```

The core never depends on the binding: it sees the six operations of §10.1. Version 2 defines one binding, IMAP. A second one would be added below that line without touching anything above it.

---

## 5. Cryptography

### 5.1 Primitives (suite `v2`, fixed)

| Purpose | Primitive | Reference |
|---|---|---|
| Hash | SHA-256 | FIPS 180-4 |
| Key derivation | HKDF-SHA-256 | RFC 5869 |
| Symmetric AEAD | ChaCha20-Poly1305 (12-byte nonce, 16-byte tag) | RFC 8439 |
| Signatures | Ed25519 | RFC 8032 |
| Key agreement | X25519 | RFC 7748 |
| Public-key sealing | HPKE, base mode, DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305 | RFC 9180 |
| Hub-password hash | Argon2id, version 0x13, one fixed profile (§6.7) | RFC 9106 |
| Randomness | OS CSPRNG | |

There is exactly one suite per protocol version. A new suite means a new protocol version (§15); there is no negotiation.

### 5.2 Notation

- `||` concatenation. Integers are unsigned big-endian. `u8`, `u16`, `u32`, `u64` are their byte widths.
- `lp(x)` = `u32(len(x)) || x` (length-prefixed bytes).
- `H(x)` = SHA-256(x).
- `KDF(ikm, salt, info, L)` = HKDF-SHA-256, output `L` bytes. `info` strings are ASCII and begin with `tandem/v2/`.
- `b64u` = base64url without padding (RFC 4648 §5). Every value has one spelling: text with padding, with a character outside the alphabet, or with unused bits that aren't zero is invalid (§10.5).
- `b32` = RFC 4648 base32, lowercase, no padding.

### 5.3 Keys

| Material | Creation and storage |
|---|---|
| Hub password | Chosen by the creator at hub creation, at least 12 code points (§6.7); never stored on the hub. Stretched with Argon2id into the capsule key (§6.7) |
| `MS`, hub management seed | 32 random bytes at hub creation. Kept on the hub only inside the **admin capsule**, sealed under a key stretched from the hub password (§6.7) |
| `MKs`, `MKa` | `KDF(MS, "", "tandem/v2/manager-sign", 32)` and `…/manager-agree`: the management signing and agreement keys |
| `hubID` | `pub(MKs)`, 32 bytes, fixed by the hub's genesis and pinned at setup, invitation and pairing. Mail credentials and host names are not authority identifiers |
| `DKs`, `DKa` | Random signing and agreement private keys, one pair per **keychain**: kept in the system keychain as an item that syncs to the user's other devices where the platform does that (iCloud Keychain), so a new phone has them. Never sent anywhere else (§6.3) |
| `HK` | Random per participant home epoch; HPKE-sealed only to admitted device keys of that participant; for the creator also to `MKa` (§6.8) |
| `K(c,e)` | Random collection epoch key; member homes and device-specific grants |
| `L`, `A` | Independent random 32-byte pairing link and admin-gate secrets (§6.3) |
| Hub invite secret `s` | Random 32-byte bearer secret, signing key derived with a distinct new-member purpose label (§13.1) |
| `HubK` | Random 32-byte directory key from hub creation; carried in invites and pairing and kept in participant homes |

No home or collection key is derived from the hub password, MS or HubK. The management seed is not a universal data key: it can open the creator's home (§6.8) and nobody else's. There is no `US`, shared user signing key or shared private agreement key.

**Participant ID:** `uid` is the full hash of the immutable registration record binding hubID, first-device public keys and an enrollment reference. That reference must be available before computing the registration/certificate, avoiding a self-reference. The first device proves possession; its administrator admits it. Revoking it does not delete the participant or give that original key special authority. A new hub has a separate registration even for the same person (§6.5).

**Device ID:** 8 random bytes per **installation**, used only as the dot namespace (§9.1). It is not part of a certificate: several installations that share a keychain share one keypair and one certificate, and each has its own device ID. It is kept with the HLC, this-device-only and never synced, so a restored or new phone picks a fresh one. A write names its certificate explicitly; a device ID never selects a key. Same-dot writes remain distinct by write ID. If the durable HLC is lost, the installation picks a fresh device ID; its keys stay as they are.

**Hub identity and storage location:** the stable cryptographic hubID is independent of the IMAP descriptor. All signatures, envelopes, scopes, policies and boxes bind hubID. The genesis, signed with `MKs`, names the permitted descriptors without credentials; credentials merely connect to that mailbox. Copying metadata into a different mailbox cannot authorize relocation. Moving a hub to another mailbox is a management action, signed with `MKs` after entering the hub password.

All labels use `tandem/v2/`, which is not compatible with shared-secret v1. The management and control objects are encoded as §10.8 says.

### 5.4 Message encryption

Every hub message body (§10.4) is an **envelope**:

```
envelope = magic "TNDM" (4) || version u8 = 2 || kind u8 || salt (32) || ciphertext
```

`kind` is 1 = record state, 2 = sealed inbox message, 3 = request or notice (pairing, leave, key request). Others are reserved.

For symmetric kinds (1, 3), with channel key `K` (an epoch key, a home key, a directory key or a pairing key):

```
mk         = KDF(K, salt, "tandem/v2/msg", 32)     # fresh per message; salt random
nonce      = 12 zero bytes                          # safe: mk is never reused
aad        = "TNDM" || u8(2) || u8(kind) || hubID || tag     # tag: the channel's tag as its 32 characters of b32 text (§5.6),
                                                    # the same bytes as the Tandem-Channel header, not the 20 decoded bytes
ciphertext = ChaCha20-Poly1305-Seal(mk, nonce, plaintext, aad)
```

Deriving a fresh key from a 256-bit random salt makes key and nonce reuse negligibly likely, and there is no counter state to lose.

`plaintext` is UTF-8 JSON (§10.5). The AEAD makes any byte change detectable, and the tag in the AAD stops the hub from moving a message between channels.

For kind 2 (inbox), `salt` is replaced by a key ID, `kid = H("tandem/v2/kid" || hubID || pub(DKa))`, naming the exact admitted device agreement key. The ciphertext is HPKE (§5.1), sealed to that key with `info = "tandem/v2/inbox"` and the same `aad`. Routing a message through the participant's common inbox does not let its other devices decrypt that envelope; grants must be delivered or relayed to their individual keys (§11.4).

**HPKE wire format.** RFC 9180 leaves the encoding to the application. Everywhere Tandem uses HPKE (inbox messages, management recovery boxes and pairing gate/releases in §6.3), the sealed bytes are `enc || ct`: the 32-byte encapsulated key followed by the AEAD ciphertext with its tag.

### 5.5 Write signatures

Each write (§9.1) is signed over this exact byte string (independent of JSON formatting, so any SDK can reproduce it):

```
sigInput = "TANDEM-WRITE-V2"                 (15 ASCII bytes)
        || hubID (32)                        # pub(MKs), the hub's identity (§5.3)
        || scopeID (16)                      # collection ID, or the user's home scope (§6.2); zero in a genesis
        || recordID (16)
        || lp(fieldName UTF-8)               # empty for a delete
        || u8(op)                            # 1 assign, 2 add, 3 remove, 4 delete
        || lp(element)                       # set element for add/remove; else empty
        || H(value)                          # value bytes on an assign; H("") for add, remove and delete
        || authorUID (32)                    # participant registration ID
        || certificateID (32)                # exact device admission certificate
        || authorizationSnapshotID (32)      # immutable device policy snapshot (§6.9)
        || deviceID (8)                      # the installation that wrote it (§5.3)
        || u64(hlcMillis) || u16(hlcCounter)
        || ref (32)                          # the roster statement it was written under, or for a statement the one before it; zero if none
        || vv(context) || vv(base)           # data records (§9.2); both empty in a roster statement
writeID  = H(sigInput)

vv(x)    = u16(n) || n × ( deviceID (8) || u64(millis) || u16(counter) )   # sorted by deviceID; n ≤ 1024 (§9.2)
```

**Identity.** A write is identified by its write ID, and a roster statement by the write ID of its write. Ed25519 signing may be randomized (Apple's CryptoKit is), so one `sigInput` can carry different valid signatures from repeated signing of one device's exact input. Two writes with one write ID are one write, whichever signature arrived first. Devices deduplicate by write ID and never compare signatures to decide whether two writes are the same. A device that retries a publish reuses the signed message it stored. Hub messages are a different matter: they are identified by `H(envelope)` (§10.4).

**Who signs.** Data, home/profile writes, roster statements and grants carry a signature by the acting device's **DKs**. Look up the exact referenced certificate; verify the device public key, hub, participant and immutable source snapshot. The write signature binds the installation’s dot namespace; the certificate does not (§5.3). A signature alone proves device possession, not current permission. Missing proofs hold the object; arrival order never selects a key. Manager/root controls have their distinct authorities (§6.7–6.9).

The signature covers the statement a write was made under, not the epoch it is stored in, so a write can be re-encrypted into later epochs during rotation (§11.6) without losing its author's signature.

Signatures live **inside** the ciphertext, so message contents tell the hub nothing about who wrote what. Traffic patterns can (§14).

**Verification is strict and the same everywhere.** Whether a write is valid must not depend on which library checked it, or two honest clients could disagree and diverge. A verifier MUST reject a signature whose `S` is not below the group order, MUST reject a public key or `R` that isn't a canonical point encoding, MUST reject a small-order public key, and MUST check the plain equation `[S]B = R + [k]A`. RFC 8032 §5.1.7 permits either that or the cofactored form; Tandem fixes the choice. Libraries that verify in batches or with the cofactored equation accept signatures the others reject and MUST NOT be used.

Other signed objects (certificates, grants, key requests, leave notices and the rest of §10.8) use their own `TANDEM-…-V2` prefixes, so a signature can never be replayed as a different kind of object.

### 5.6 Channels and tags

The hub sees channel tags, never plaintext participant, device or collection IDs:

| Channel | Key | Tag |
|---|---|---|
| Collection epoch | Random `K(c,e)`, committed by epoch-start statement `E` | `b32(KDF(K(c,e), hubID || E, "tandem/v2/collection-tag", 20))` |
| Home epoch | Random `HK`, committed by its signed home-epoch start `E` | `b32(KDF(HK, hubID || uid || E, "tandem/v2/home-tag", 20))` |
| Participant inbox | HPKE to a particular admitted DKa | `b32(KDF(uid, hubID, "tandem/v2/inbox-tag", 20))` |
| Pairing token | `KDF(L, hubID ‖ token, "tandem/v2/pair-key", 32)` | `b32(KDF(L, hubID ‖ token, "tandem/v2/pair-tag", 20))` (§10.8) |
| Directory / registry discovery | `KDF(HubK, hubID, "tandem/v2/directory-key", 32)` | `b32(KDF(HubK, hubID, "tandem/v2/directory-tag", 20))` |

The inbox is stable per participant/hub so other participants can deliver to it, but it contains independently sealed device envelopes. Registry objects prove their own authority; learning HubK never permits forging a certificate. The initially pinned root and directory-discovery material travel in hub invites, pairing links or the creator's saved setup details.

AEAD AAD and signed inputs bind the cryptographic hubID. Home/collection epoch starts additionally bind their owner and channel purpose. Re-encrypting ciphertext under a different hub's envelope does not authorize its signed content there. Participant IDs and device keys are independently created per hub; profiles across hubs need not share an identifier. Traffic analysis can still link activity (§14).

### 5.7 Key commitment

`commit(K) = H("tandem/v2/commit" || K)`. A roster statement that starts an epoch carries the commitment of its key, so everyone who is given the key can check they got the same one.

---

## 6. Participants, devices and hub management

One participant record groups a person's approved keychains within a hub. Each keychain has its own keys, shared by the devices that sync it; they all share home and list access continuously. The participant record is bookkeeping, not another private key, global identity, personal password or recovery chain.

### 6.1 When the participant is created

A client works locally before sync. No participant identity or management key is created merely by installing the app or enabling local storage.

**Creating a hub:** connect the dedicated mailbox and explicitly create the hub; ask the creator for a hub password; generate MS, HubK and the first keychain's keys; suggest keeping the password with the mailbox details. Create the immutable creator participant registration and the genesis, publish the admin capsule (§6.7), then admit the first keychain and initialize the creator's private home. Journal exact signed setup objects before export. A mailbox that is unreachable or appears empty is not proof that a new authority may replace an existing one. Conflicting independent roots/directories must be shown as distinct authorities, not silently merged.

**Accepting a hub invitation:** the trusted app verifies the pinned hub authority and new-member invitation capability. It generates device keys and a durable enrollment reference, then creates a participant registration as part of the claim. The registration is pending until its complete first-admission proof verifies; restart/retry reuses that registration and exact claim. The manager-signed invitation preauthorizes this new-member admission (§13.1), so accepting it needs no second admin tap. That capability cannot name an existing participant. The participant then initializes their private home and directory profile. Nobody but the creator chooses a hub password.

**Pairing:** an existing member creates the link. The new device attaches to that member's existing registration (§6.3); it must not create a second person or clone another device's private keys. Revoking the first device leaves the participant and remaining devices intact. Joining another hub creates a separate participant there (§6.5).

### 6.2 Home

**Home** is one participant's continuing private stream, with a random key `HK` per home epoch. All its admitted devices follow the same stream. It holds named records (§7.3):

| Record | Fields |
|---|---|
| `membership:<collectionID>` | `keys` (a set of `{ "epoch": b64u(ID of the statement that started the epoch), "k": b64u(epoch key) }`, one for every epoch the user has been given), `owner`, `role` (cached), `answers` (the user's answers to each admission, §11.2), `flight` (collections the user owns: the one key change not yet announced, with its key and the roster it leads to, §8.2), app-defined display hints |
| `hubinvite:<id>` | A hub invite this user made (§13.1): `label`, `made`, `hint`. Never the credential itself. The `<id>` is the one in the invite, so the directory (§6.6) shows who arrived through it |
| `hub` | This hub's authority pins, directory key and authorized descriptor, without the credential (§6.6) |
| `pairing:<id>` | Pending/completed pairing, label, expiry, package references and outcomes; never the bearer link (§6.3) |
| `activity:<id>` | Immutable private event with actor, causal references and signed receipts (§6.10) |
| `settings:<app>` | App-defined (Liszto: list order, pinned lists) |

Home uses the write and merge rules of §9 within each home epoch, with `scopeID = H("tandem/v2/home" || hubID || uid)[0..16]`. Each write must authenticate an admitted device of this participant under its immutable source policy. A home epoch pins device floors and a fixed old-write inventory, with new random keys and retained old-channel announcements when access is cut (§6.4, §6.9). A revoked device cannot derive the next key from a shared user secret. Pairing joins this ongoing home; it does not import a static copy.

When the phone creates a new list, it adds its membership/key here. The laptop discovers it by normal home sync, with no new invitation or admin action. Accepted lists, admission answers, settings, pending invitations and private activity follow the same path. A device discovering a membership follows its collection channel after validating the grant/roster. Two rules persist across home epochs:

- `keys` only grows. Keys are indexed by the **full epoch-start statement ID**, never by their commitment alone. Which epoch is current follows the collection's roster, not a clock or arrival order.
- Home records are never deleted (§7.3): leaving a collection adds an answer (§11.2), so the user can be added again later.

**Field encodings.** Every SDK reads home, so the bytes of its protocol fields are fixed. IDs in record names are spelled in b64u. A value below is the value of an `assign` or the element of an `add`.

| Record | Field | Op | Bytes |
|---|---|---|---|
| `membership:…` | `_name` | assign | The record name in UTF-8: `membership:<b64u collection ID>` |
| | `owner` | assign | The owner's participant ID, 32 raw bytes, taken from the collection's genesis |
| | `role` | assign | ASCII `owner`, `writer` or `reader` |
| | `answers` | add | One element per answer: the 32-byte ID of the admission it answers (§11.2) followed by one ASCII byte, `a` (add), `n` (no) or `l` (leave). Elements are only ever added |
| | `keys` | add | One element per epoch key: the UTF-8 JSON `{"epoch":"<b64u 32-byte epoch-start ID>","k":"<b64u 32-byte key>"}`, with the two properties in that order and no whitespace. Readers index the elements by epoch ID, so the same key in another spelling does no harm |
| | `flight` | assign | A pending key change: the UTF-8 JSON `{"parent","stmt","k","members","what","device"}`. `parent` and `stmt` are statement IDs and `k` the new key, all in b64u; `members` is the member list the change leads to, in the roster's form (§8.1); `what` is a short description for the user; `device` is the name of the device that started it. **No pending change** is the JSON `null`. A reader also takes an empty value, or anything that is not a well-formed note naming a statement, as no pending change |
| `hubinvite:…` | `_name` | assign | `hubinvite:<b64u invite ID>`: the 16 random bytes in the invite's `i` |
| | `label` | assign | UTF-8 text |
| | `made` | assign | ASCII decimal digits: Unix milliseconds |
| | `hint` | assign | The last four characters of the credential, UTF-8 |
| `hub` | `_name` | assign | `hub` |
| | `hk` | add | This authority's 32-byte directory key. Conflicting independent hub setups are not silently merged (§6.6) |
| `settings:<app>` | | | App-defined |

The `hubinvite`, `pairing` and `activity` records have further fields, given in §10.8.

### 6.3 Adding a device

**Devices that share a keychain need nothing.** A device's keypair lives in the system keychain (§5.3). Where the keychain syncs between a user's devices, a new or replaced iPhone, or another Mac on the same Apple account, already holds the keypair: it is the same participant with the same certificate. It picks a fresh device ID, opens the home with its keys, and syncs. No pairing, no admin approval. Everything below is for a device that doesn't share the keychain: another platform, or another account on the same platform.


**The required experience:** Alice opens "Add a device" on her enrolled phone,
creates a QR/link and opens it on her laptop. The laptop obtains connection and
pairing information there, generates independent keys, and submits its claim.
The admin sees **"New device from Alice: Laptop — Approve / Reject"**. There is
no preliminary admin invitation, additional Alice approval or six-digit comparison.
The laptop says "Waiting for the hub admin" and shows private data after approval.
"Enrolled" is recorded only after that approval, so Reject remains meaningful.

Alice creates a pending pairing record in her own encrypted home. It contains a
random token ID, intended device label, creation time, expiry, staged-package
references and outcomes. The admin's private home records the arriving claim,
not a copy of her secret link. Both histories sync across their respective
devices. The default expiry is 24 hours, adjustable at creation; it bounds honest
pending claims, not leaked mail credentials. Already approved certificates do
not expire because the link expired.

#### What the phone prepares

The phone generates two independent random 32-byte secrets, `L` and `A`:

- `L` goes only in the QR/link. An invite signing key is derived from L using
  HKDF with a pairing-specific purpose label and token ID. Its public key goes
  in a **delegation** signed by the existing phone's device key.
- `A` is HPKE-sealed to the management agreement key `MKa`. Unlocking the admin
  capsule lets the admin open A, but not L. There is no home key in that gate box.
- The phone seals a small bootstrap bundle under
  `HKDF(L || A, tokenID, "tandem/v2/pair-package", 32)`, using
  ChaCha20-Poly1305 and a fresh random 12-byte nonce. It contains this member's
  current shared home-epoch key and the necessary grant authorizations/metadata
  for continuous sync, including lists created or shared after pairing. It never
  contains another device's signing private key.

All inputs bind hub authority ID, genesis ID, member ID, source device
certificate, token ID and expiry. The phone's signed delegation additionally
binds the derived invite public key, package ciphertext hash, gate-box hash and
permitted purpose **one existing member's device enrollment requests**. It cannot
change the member record, grant hub management authority or authorize another hub.
Use separate HPKE purpose labels for the gate and the eventual release.

The QR/link carries L, hub descriptor/credential, directory key, pinned member record
and hub authority, token ID, delegation and package references (or the compact
encrypted package). It never carries plaintext home keys, the hub password, MS or A. The phone
uploads/retains the signed delegation, encrypted package and gate box before
showing the link. This stages data delivery; it gives the laptop everything
needed for the ceremony without keeping the phone online for final approval.

#### Laptop claim and admin decision

1. Laptop opens the QR/link in the trusted native app, verifies its member/hub
   pins and delegation, generates its own signing/agreement keypair, and proves
   possession. The link's invite key signs the exact target keys, device label,
   token/delegation ID, genesis and issuance context. The laptop's signing
   key also signs the claim; an HPKE challenge proves agreement-key possession.
   This replaces a second signature from the phone over the now-known keys.
2. Send the claim and opaque package/gate references to the admin's admitted
   device inboxes, encrypted to their device keys. Admin devices can display
   "New device from Alice: Laptop" without the hub password. They verify the source
   member certificate, signed delegation and claim; they never receive L.
3. **Approve:** the admin unlocks the admin capsule (§6.7). Re-sync, check expiry,
   source device admission and current revocations, and prior token uses.
   Certify the exact target keys with MKs, including the delegation/claim hashes
   and member record ID. Open A with the management agreement key and HPKE-seal
   it to that exact laptop agreement key. The release binds the certificate ID,
   delegation, package ciphertext hash and target keys. No copy of A is released
   to a requester without a matching approved device certificate.
4. Persist the exact signed certificate and encrypted release before export.
   Publish the certificate before the release. Lost acknowledgements retry the
   same bytes. If interrupted after certification, a surviving admin device
   finishes the persisted release, or unlocks the capsule to reconstruct it.
5. Laptop verifies the certificate/release, opens A, combines it with L and opens
   the staged bundle. Its lists and personal metadata then sync. It retains its
   new device keys and current home/grant material and discards pairing secrets.
   Signed completion receipts update Alice's and the admin's private histories.

**Reject:** record and send the admitted admin device's signed rejection, keep A
unreleased, and stop that claim's retries. Rejection is a negative workflow
receipt, not a device revocation or management authority grant. A valid later
manager-signed certificate takes precedence over a concurrent rejection receipt.
Once certified, removal requires the normal revocation/rekey procedure (§6.4); a UI
rejection cannot withdraw a completed enrollment. Deduplicate claims/outcomes.

Alice can cancel a pending pairing from an admitted device. Honest admins refuse
cancelled/expired claims at final approval. The creator cancels their own link the
same way. Revoke a certificate separately if approval already happened. Mailbox
credentials leaked in any QR/link still require provider revocation.

**Link security trade-off.** The link is a bearer delegation: anyone who captures
it can request enrollment as Alice. Without the comparison ceremony we cannot
cryptographically distinguish her intended laptop from another link holder.
Admin approval is still required, and L alone cannot open the staged private
bundle. The admin, having A, must not receive or log L: capture of the link plus
management access supplies both factors and defeats that separation. Tell users
to treat the link as sensitive; never put it into the directory, admin log or
notification. Multiple claims on one token are shown together. Honest admins
approve one intended claim; concurrently approved claims are both real devices
and need explicit revocation if unintended, not an arbitrary mailbox-order winner.

If home keys rotate after the package was staged, old bootstrap keys cannot
promise current access. Refresh the package from an admitted member device or
deliver the new home key to the approved laptop through normal sync. This may
wait for a member device to reconnect, but asks for no second pairing ceremony.

An ordinary invitation for a **new member** remains separate. It may preauthorize
new-member admission through a manager-signed, purpose-limited invitation key;
it cannot reuse this path to claim an existing member ID. Its bounded delegation
and forwarding risks still require explicit wire validation.

### 6.4 Removing a device and losing every device

The member requests removal; the admin approves after unlocking the admin capsule.
For the creator's own phone this is one action on a surviving admin device.

**Removing a key removes every device that shares it.** A certificate is for a keychain's keypair, so revoking a stolen iPhone's key also cuts off the user's other devices that sync that keychain. They then make a new keypair and are admitted again: by pairing from a device outside the keychain, or, with none, by the admin as for a new keychain of that participant. **First the stolen device must be removed from the platform account** (on Apple, from the Apple account, for instance with Find My). Otherwise the keychain syncs the new keypair to the thief as well. The app says so before it makes the new keypair.

1. Sign and publish an irrevocable device revocation. Stop new deliveries to it.
2. Surviving devices of that member rotate their private home key, sealing it
   only to remaining admitted devices. The admin cannot do this without their
   participation. The creator may use the hub password if no device remains (§6.8).
3. Owners rotate every affected collection with fresh keys, inventories and
   device floors. Those owners need no hub password, only an admitted owner
   device. Devices that learn of an unreflected revocation queue new outgoing
   content rather than encrypting it with the exposed key.
4. Revoke that device's IMAP credential at the provider. With a shared credential,
   replace it and reconnect everyone. Without a supported provider API, show the
   exact manual step instead of claiming the app revoked it.

Track "Device revoked", "Waiting for private-home rekey", "Lists secured",
"Waiting for list owners" and "Mailbox credential still active" separately.
Offline/stale devices can publish under old keys until they learn the cutoff;
instant global exclusion is not promised. Previously downloaded data stays read.
Security revocation is not remote erasure or a directory-entry deletion.

For owner-controlled lists, the removal action can authorize a displayed batch of rekeys on the acting owner device; other owner devices follow its existing journal/home-note procedure (§11.6–11.7). Other collection owners must finish their own changes. Devices must not all independently sign automatic roster successors when one revocation arrives. Until a required rotation is complete, show “Waiting for list owner” and queue outgoing content once the exposed key is known.

**Losing every device.** Where the keychain syncs, a member who replaces every device keeps their keys: the new phone has them (§6.3). A member who loses the keychain too, for instance by moving to another platform with no device left, can't be given their old participant back by the admin, who can't reconstruct their home or impersonate them. They join as a new participant and have shared lists shared again; private data with no readable replica is lost. The creator alone can be recovered with the hub password (§6.8). A missing device with uncertain exposure should be revoked; a broken one whose keychain lives on elsewhere needs nothing.

### 6.5 Several hubs

Each hub has its own participant record, home, device keys and admission decisions. One app can present the lists together, with one live IMAP connection per hub. Pairing on one hub does not authorize admission on another. A client may discover a reference to another hub, but its admin still approves enrollment there; discovering mailbox credentials is insufficient. A keychain that syncs carries its keypair to every hub the user is on, and each hub keeps its own certificate for it. Removal/recovery is also tracked per hub. This trades one global identity and one-scan automatic admission everywhere for independent hub authority and revocable device keys.

### 6.6 Directory and admission authority

Every hub has a directory encrypted with HubK, readable by its participants. It contains immutable participant registrations, root/management metadata, admission certificates/revocations and device-signed profile records. A name is editable display text; it never selects a participant or key. The creator fixes HubK at authenticated setup. Independent simultaneous setups produce conflicting hub authorities rather than a union of self-selected directory keys.

The app shows **People**, with **Devices** underneath each person. Directory membership and device authority come from verified proofs under the pinned root/manager and member authorization (§6.9), not from profile timestamps, `joined`/`left` hints or possession of HubK. Only an admitted device of participant X may sign X's profile. Provenance, where available, comes from the signed invitation/pairing chain; copying someone else's inviter label cannot supply it. A display name/provenance proves neither a legal identity nor that the intended physical person held the invitation.

All participants can learn names, participant/device bindings and enrollment/revocation facts. They gain no home or collection keys from the directory. A former participant may retain HubK; excluding them from raw mailbox traffic also needs provider credential revocation. Signed registry objects and their full proof dependencies are retained/restored even if a profile says someone left. Absence of a directory entry is not evidence that a device was revoked or a hub is unused.

### 6.7 The hub password

The hub's management key lives **on the hub**, in an **admin capsule**: `MS` sealed with ChaCha20-Poly1305 under a key stretched from the hub password, signed with `MKs` and kept in the directory beside the genesis. Anyone on the hub can read the capsule and try passwords against it offline; the stretching is what makes each try expensive.

**Stretching.** `CK = Argon2id(password, salt, m = 1 GiB, t = 3, p = 1, 32 bytes)`, Argon2id version 0x13, with a random 16-byte salt stored in the capsule. That is one fixed profile, the only one a v2 client accepts, so a capsule can't be written with weaker settings. It has one lane because libsodium's `crypto_pwhash`, the usual Argon2id on phones, always uses one; the cost is that a device can't spread the work over its cores, so the time it spends buys less work than four lanes would. It takes about 1.2 s on an Apple M5 and a few seconds on a recent phone, and needs 1 GiB of memory while it runs. The capsule is `salt || nonce || ChaCha20-Poly1305(key, nonce, MS, aad)`, with `key = KDF(CK, hubID, "tandem/v2/admin-capsule", 32)`, a random 12-byte nonce and `aad = "TANDEM-ADMIN-CAPSULE-V2" || hubID || salt`. CryptoKit has no Argon2id: libsodium's `crypto_pwhash` with `ALG_ARGON2ID13`, an opslimit of 3 and a memlimit of 2^30 bytes computes `CK`, and the Swift SDK has its own implementation, about as fast (1.2 s on an Apple M5 in a release build). On iOS, the 1 GiB may need the increased-memory-limit entitlement. `conformance/fixtures/tandem-v2-capsule.json` is a vector at this profile, checked against both libsodium and Python's `cryptography`.

**The password.** Chosen by the creator. It is hashed as NFC-normalized UTF-8, with case and spaces kept and nothing trimmed. When a password is chosen, at hub creation or a change, it must have at least 12 Unicode code points and at most 1,024 bytes after normalization. That is code points, not the characters a person sees: Swift's `String.count` gives 4 for 👩‍💻👩‍💻👩‍💻👩‍💻, which has 12 code points and is long enough. Unlocking never checks the length; whatever is typed is normalized and tried. The app suggests a generated passphrase of six words. Stretching makes each guess cost a second or more of a strong machine and a gigabyte of memory, so an attacker with one graphics card tries tens per second, not billions; a six-word passphrase is out of reach. It doesn't save a password from a list of common ones: those fall in hours. The app says so when the password is chosen.

**Entering it.** Typing the hub password on any device opens the capsule, derives `MKs` and `MKa`, and checks that `pub(MKs)` is the `hubID` the device pinned. That device can then administer the hub. It MAY keep `MS` in the system keychain behind device authentication (Face ID, a passcode), on that device only and never synced, so that later approvals ask for that rather than the password. It keeps `MS`, not `CK`, because `CK` opens one capsule and stops working once the password is changed elsewhere. A device that keeps `MS` is as good as the password to whoever takes it over, whatever the password is changed to, so the app asks first, and the admin can forget it again on that device. A stolen device that keeps it is a leaked password (§6.8).

The hub password is asked for to create hub invitations, approve enrollments, revoke keys, move the hub and change the password. It is not asked for to create, edit or share lists, or to sync.

**Changing it.** A device that holds `MS`, from the password or from its keychain, writes a new capsule under the new password. The new capsule names, by ID, every capsule it supersedes: the current ones and every capsule they name. Once the new one is on the hub, the device destroys the old ones. Devices use any capsule that no other names. Because the names are carried forward, a copy of a destroyed capsule put back on the hub is still named and never becomes current again; the cost is one more ID in each capsule per change. Two changes at the same moment leave two, each opening with its own password, until the next change names both. Changing the password is not a reset: anyone who copied the old capsule and knows the old password still opens it (§6.8).

### 6.8 If the hub password leaks or is forgotten, and the creator's recovery

**Leaked password.** Whoever knows the hub password and has read the capsule can admit devices, revoke keys and invite people. They can recover the creator’s home and the list keys it contains (§6.8). They cannot read another member’s private home or act for that existing member: admitting keys to a non-creator participant needs that participant’s own authorization (§6.9). Collection ownership remains fixed. Still, the hub's management can't be trusted any more, and changing the password doesn't take the management key back. The creator makes a **new hub** and copies the lists across, and people join it, as with a dead list (§11.8).

**Forgotten password.** A device that keeps `MS` in its keychain can set a new password without the old one, even if the password was changed elsewhere since it stored it. With no such device, the hub's management is lost: lists keep syncing and members keep working, but nobody can admit, revoke or invite. The way back is a new hub and a copy.

**Recovering the creator.** The creator's home key is also sealed to `MKa` (§5.3). A creator who has lost every device and the keychain enters the hub password on a new device. It opens the capsule, admits a fresh keychain for the creator's participant, the one and only exception to member authorization (§6.9), and opens the creator's home. The hub password recovers nobody else: no member's home is sealed to `MKa`.

A management compromise can admit lookalike new members, revoke people and deny service; an owner fooled into sharing with a lookalike has made a real disclosure. Compromise of both an individual's device and the hub password is stronger: start the new hub from a clean device and review the devices that member had approved.

### 6.9 Genesis, device registry and policy snapshots

The hub's **genesis** binds `hubID` (that is, `pub(MKs)`), `pub(MKa)` (which a member's device seals a pairing gate box to, §6.3), the permitted mailbox descriptors, the commitment of `HubK` and the immutable creator registration, signed with `MKs`. It is the only root a hub ever has. A device pins it at setup, invitation or pairing, and refuses anything that claims to succeed it; a second genesis is a different hub. There are no recovery generations.

The device registry is a set of immutable manager-authorized certificates and
permanent manager-signed revocations. Existing-member pairing requires a direct
manager signature over the exact new device. New-member first admission may
instead carry the manager-signed new-member capability, an invite-key-signed
exact registration/target claim and both device-key possession proofs (§13.1).
That composite proof authorizes only the self-identifying new registration;
it cannot certify arbitrary keys as an existing participant. Its content ID
hashes canonical inputs, not randomized signature bytes. Union merges certificates and revocations;
removal wins. A revocation commits the exact certificate/public keys being
retired and the fixed set of member device certificates retained at that cut, so
issuing a new certificate for the same keys cannot resurrect them under an
alias. Concurrent device
additions and concurrent removals do not create competing device registries.

A certificate is not valid merely because MKs signed it. For a new participant,
verify its immutable registration, invitation/direct-admission authority and
proof of both device keys. For an existing participant, require authorization
by an admitted device of that same participant: either its signature over the
exact enrollment request or a valid signed pairing delegation and bearer-signed
target claim (§6.3). Bind the full target keys, participant, hub, genesis and issuance cut. Installation
dot namespaces are bound by individual write signatures, not by the shared certificate (§5.3). Validate the source against those immutable
issuance dependencies; a later source revocation does not invalidate an already
retained child. Creator recovery is the only exception, restricted to the
creator that the genesis fixes, and needs the hub password (§6.8). An admin
cannot omit these proofs and mint themselves a device as an existing member.

Each certificate has an **issuance snapshot**, the exact known certificate and
revocation IDs against which member authorization was checked. A valid new
device certificate does not make its own requester's claimed keys authoritative.
Proofs and required ancestors must be verified before it becomes usable.

An authorization snapshot hashes the hub's genesis ID and sorted,
unique certificate/revocation IDs for a member. A collection's fixed policy
floor pins such a snapshot. Missing signed objects are held; nobody can prove
that a snapshot includes every fact on the mailbox. Withholding is not solved.

For each revocation of that member, an admitted certificate must either be in
its fixed retained set or explicitly include that revocation in its issuance
snapshot. The target keys themselves are always retired. This rule is about
signed sets, not whether one receiver happened to see an enrollment first.
Concurrent enrollment and revocation can leave the enrollment needing renewal
against the current cut, with an admitted member device approving again. A
previously admitted child retained at the cut is independent of its approving
parent; removing the parent does not silently remove the child.

A source snapshot extending a collection floor includes every floor certificate
and revocation, and checks the retained-set/issuance rule above. Thus an old
certificate omitted at a cut cannot later arrive as a new-device alias. No
clock, claimed signature date or mailbox order supplies that authorization.
Implementation must verify these exact snapshot dependencies before pruning.

Management actions require connectivity, full sync of the hub, exact-byte
journals and a final pre-publication check. Registry objects merge; home and
collection key rotations still use their own serialized announcement procedure.
IMAP is not a signed compare-and-swap service. Cached clients may be stale, and
a fresh client shown only old metadata cannot prove freshness.

### 6.10 Private activity history

The originator's private home holds immutable activity events: invitation
created/cancelled, enrollment approved/completed, key revoked, sharing offered
and acceptance received. Member private events
remain private even from the hub admin; the admin sees their own administration
events and authorized receipts. Distinct records retain history instead of
attempting to reconstruct it from merged current state.

A member sends a signed, HPKE-sealed acceptance/decline receipt to the list-sharing
originator, binding member, admission, collection, answer, actor certificate and
unique event ID. The originator verifies/deduplicates it and records it privately.
Conflicting device answers are both logged; current status uses the admission
answer rule. A receipt proves a signed claim, not that all data was downloaded.
Hub enrollment comes from verified admission proofs. Logs carry causal references
and claimed/received times, not a fictitious globally trusted wall-clock order.

---

## 7. Collections

### 7.1 Identity and genesis

The creator writes the genesis roster: themselves as the owner, a commitment to a random `K(c,0)`, and 16 random bytes. In the statement's JSON (§8.1) that is `n: 0`, `key.n: 0`, no `key.prev`, no `snapshot`, and `nonce`: the 16 random bytes in b64u. A genesis that differs is invalid. The **collection ID** is the first 16 bytes of that statement's ID. The genesis uses zero `scopeID`, so its ID is computed before the collection ID is needed. The collection ID therefore fixes who the owner is, for the life of the collection.

The **epoch ID** `E` is the full 32-byte ID of its starting roster statement, including for epoch 0. Only after that ID exists is the channel tag derived (§5.6). Sharing one key commitment never makes two different starts the same epoch.

### 7.2 Epochs

An epoch is a segment of the roster history, identified by its starting statement, with a numeric counter for the data merge. Removing a member or lowering a role starts the next epoch with a fresh random key. Additions and promotions within an epoch only increase access.

Only the owner starts an epoch. Clients accept data directly under the **same epoch-start ID**, or through that epoch's committed snapshot inventory (§8.3).

### 7.3 Reserved and named records

| Record | Purpose | Who may write |
|---|---|---|
| `_roster` | The collection's chain of statements: members and roles, and each epoch's key commitment (§8) | The owner |
| `_meta` | The collection's name, icon and other app metadata. An ordinary data record | The owner (§8.3) |

Ordinary record IDs are 16 random bytes. **Named records** (`_roster`, `_meta`, and the records in home) use `id(name) = H("tandem/v2/named" || name)[0..16]` and, except `_roster`, carry their name in a `_name` field, written with their first write, so every client derives the same ID for the same name. `_roster` has no field but `s`; any other write to it is invalid. A random ID colliding with a named one is as likely as a 128-bit collision.

Reserved and named records are never removed: a record ID derived from a name can't be created afresh, so they change state through their fields instead. A `delete` write on a named record is invalid.

### 7.4 Private and shared are the same thing

A private collection is a collection whose roster has one member. Sharing adds members; making it private again removes all other members, which rotates the key. There is no mode switch.

---

## 8. Roster and authorization

### 8.1 Owner and members

A collection has exactly one **owner**: the participant registration named by its genesis, signed by an admitted device of that participant. All other members are **writers** or **readers**. Only the owner changes the roster: adds and removes members, changes a role, deletes the collection. There is no collection-admin role, and ownership doesn't move. The hub admin has no extra collection powers.

The roster is a chain of complete states. Each state carries its sequence number, epoch counter/commitment and prior commitment, fixed snapshot commitment at an epoch start, deletion flag, and sorted unique participant entries containing participant ID, role and display name. There is no shared participant agreement key. Each epoch additionally fixes a device-policy floor per listed participant: the root and exact certificate/revocation snapshot against which new direct writes are checked (§6.9).

New devices extend their participant's pinned policy without becoming separate collection members. Device revocation, management recovery or any policy change that excludes formerly authorized keys requires a new epoch/fixed inventory and fresh key. Adds/promotions may keep the epoch when they only increase access. A same-epoch member addition supplies that member's initial device floor. The statement's JSON is in §10.8.

The collection's name, icon and other app metadata are not in the roster. They are an ordinary record, `_meta`, that only the owner may write (§8.3), so renaming works offline from any of the owner's devices.

### 8.2 Valid statements, one change at a time

Every statement after the genesis is signed by a device certified for the **owner participant** fixed by genesis, using the statement's exact certificate/source snapshot. For a new epoch the actor must extend the previous owner floor and satisfy the new one; using an older certificate/snapshot cannot bypass a revocation cut. It must have the right collection scope, `_roster` record, `s` field, `assign` operation and the owner as author; empty contexts and an empty `element`; `n` exactly one higher; unique member IDs; exactly one owner, the same as in the genesis; only the three defined roles; and for every participant who stays, the same participant ID. Names are display hints and never substitute for verified device admission. A deleted statement has no successors.

The owner may add or remove members and change a member between writer and reader. Removing a member or lowering a role MUST advance the epoch counter by one, use a fresh random key whose commitment does not occur earlier in the roster, and commit a new snapshot inventory. A statement that starts an epoch adds no member. A same-epoch statement keeps existing device floors, the epoch commitment and snapshot commitment unchanged. A revocation/recovery cut cannot be smuggled into a same-key statement.

**One change at a time.** All admitted devices of the owner participant can sign, so independent keys do not prevent concurrent successors. The existing home-note/journal rules still apply: These rules keep them apart:

- A device changes a roster only when its user asks for it, on that device. Nothing changes a roster automatically.
- Before signing, a device syncs the roster and home. It never **announces** two different successors of one statement. It persists the exact signed statement in its journal before it writes the note or exports anything, and while the change is pending every retry reuses that statement. Between signing and publishing it syncs again and makes the check of §11.6 step 4. A statement inside an epoch, such as adding a member, has no note; for it the check is that its parent is still the head and that no note has appeared.
- Until it is announced, a statement that starts an epoch is a draft (§9.4). A draft may always be abandoned, by the device that signed it or by another device of the owner (§11.7): its channel is destroyed, its note removed, its journal entry dropped, and the user is told. An announced statement is never replaced or withdrawn. A device signs a second successor of one statement only after the first has been abandoned and its removal is on the hub.
- Before starting an epoch, a device writes the new epoch key and a note of the change (the parent, the statement's ID, and the members and roles it leads to) to the collection's `flight` field in home. While a note is there, no other device of the owner signs anything in that collection; it offers to finish the change instead (§11.7). The note is removed when the change is announced, and also when it is discarded, so a missing note doesn't say which of the two happened (§11.6).

The roster does not merge. Two different successors of one statement, both signed by the owner and both published (a draft is not), mean one of three things: two devices of the owner acted in the same moment, the hub hid one device's change from another, or someone else holds an owner device key. In each case the collection is dead (§9.4). A conflicting roster makes the collection read-only; no clock, chain length or ranking selects a winning history.

### 8.3 Valid data writes and snapshots

A data write names a valid roster statement in a valid chain from genesis (§8.2), and is signed by the exact device certificate/snapshot bound in its input. The certificate must bind its participant author, device public keys and hub; the write signature also binds the installation’s dot namespace. That roster must list the participant as owner or writer. Its immutable source authorization is checked against the certificate/root chain and revocation sets referenced by that snapshot (§6.9), not whatever registry facts happened to arrive first. A missing ancestor holds the write; an invalid one rejects it. A draft authorizes nothing (§9.4). Its source epoch is the first statement, going back from the one it names, that carries the same epoch commitment. In a channel identified by epoch-start ID `E`, it is eligible iff:

1. its source epoch ID is **exactly `E`**, and its source authorization snapshot extends that epoch's pinned floor for its participant (including the retained-certificate rule of §6.9); or
2. its write ID and record ID occur in the snapshot inventory committed by `E`, and the roster statement it names is an ancestor of `E`.

Matching a key or counter alone is never sufficient. Readers' writes are rejected, and a write to the `_meta` record is accepted only if the statement it names lists its author as the owner. Source authorization, epoch eligibility and inventory membership depend only on exact signed objects, none of which can change, so superseded data can be pruned, within the limit §9.3 sets. In a dead collection processing stops (§9.4); clients do not try to reconstruct a trusted state from what was already pruned.

**Snapshot inventory.** Before signing a statement that starts an epoch, the owner's device fixes every record/write ID it will carry over. The canonical inventory bytes are `u32(recordCount)`, then entries sorted by 16-byte record ID; each entry is `recordID || u32(writeCount) || writeIDs`, with the 32-byte write IDs sorted and unique. Record IDs are unique. Every entry lists at least one write: a record with nothing to carry over does not appear, and an entry with a zero count is invalid. The bytes are then a function of the set of (record, write) pairs. The empty inventory is exactly `u32(0)`. `snapshot = H(inventoryBytes)` in the roster statement commits the whole set before the epoch ID exists, avoiding a cycle.

An inventory is published as indexed encrypted fragments in the new channel (§10.5–10.6). A receiver checks the assembled bytes against the signed commitment before using any entry. A missing inventory holds the epoch: without it nothing old can be judged. A listed write that is absent holds nothing. The inventory says which old writes may enter, not which must be present: later edits supersede snapshot writes and their messages are collected (§10.7), and one that was destroyed is restored by the devices that hold it (§12.2). Only listed writes are imported, with their original signatures. Tombstones are included like any other write. The inventory cannot later grow: a late write is signed again by its own author (§11.6).

**Nothing listed is dropped unseen.** A device may enter an epoch while a listed write is missing from the hub, and may then start the next epoch itself. Its new inventory therefore lists, besides what it carries over, every entry of the current inventory that it neither holds nor has itself seen replaced. Such an entry costs 32 bytes and stays until a rotating device has seen the write. Without this rule a write that was only missing for a while, a tombstone included, could never be restored: nobody but its author may sign it again. A device needs the current epoch's inventory to start the next one.

A write with a missing roster, root, certificate, policy or referenced proof is held until it arrives. A later revocation does not retroactively invalidate a historical source write: it excludes that device from direct writing after a new-epoch floor cut. Old writes enter the new epoch only if the fixed inventory names them. Timestamps and mailbox arrival order never prove that a write preceded revocation. Bad signatures, malformed writes and an author not authorized by the named statement are rejected. Resource limits for holding incomplete objects are configurable (§9.4).

---

## 9. Records, writes and merge

### 9.1 Writes

A write is the tuple:

| Field | Meaning |
|---|---|
| `record` | 16-byte record ID |
| `field` | Field name (UTF-8; app-defined; names starting with `_` reserved). Two names are the same field iff their UTF-8 bytes are equal. Nothing is normalized or case-folded: `é` as one code point and as `e` with a combining accent are two fields. Empty for a `delete` |
| `op` | `assign` (register), `add` / `remove` (set element), `delete` (record tombstone) |
| `element` | Set element bytes (add/remove only) |
| `value` | Opaque bytes (assign only); the app chooses the encoding |
| `author` | Participant registration ID; plus the exact device certificate and authorization-snapshot IDs bound by the signature (§5.5) |
| `dot` | `(deviceID, hlc)`: this write's position in the authoring device's history |
| `ref` | In a collection: the ID of the roster statement the write was made under (§8.3). Its **epoch** is that statement's epoch. In home: its signed home-epoch policy/start reference; home also rotates on device removal. In a roster statement: the ID of the statement before it (§9.4). Epoch equality for collection data means the same epoch-start ID, not merely the same key or number. |
| `context` | A version vector covering the writes **of the same epoch** that this write supersedes |
| `base` | A version vector covering the writes **of earlier epochs** that it supersedes |
| `sig` | Ed25519 signature over `sigInput` (§5.5), by the acting device's DKs |

**HLC:** each device keeps a hybrid logical clock `(millis u48, counter u16)`. A new write's HLC is `(max(now, last.millis), counter)`, with `counter` incremented if `millis` didn't advance; if the counter would pass 65 535, `millis` advances by one and the counter restarts at 0. A device's HLCs are strictly increasing. Devices do **not** adopt other devices' clock values: causality is tracked by `context` and `base`, not by timestamps, so a device with a wrong clock can't make its writes win over writes that saw them.

A write whose `millis` is 2^48 or more is invalid, although the signed slot is wider. The HLC is stored durably with the device ID: a device reserves each new clock value, and stores the write, before it shows the edit as made or lets the write leave the device, an app extension included (§17). A restart must never issue a dot again. An installation that cannot recover its last clock value generates a fresh device ID and starts a new dot namespace (§5.3); its keychain keys and certificate stay usable. It does not guess from vectors other devices claim. Losing the keychain keys themselves still requires pairing or new-participant admission (§6.3–6.4).

The signature’s exact certificate binds the acting keychain keys and participant; the signature itself binds the installation’s dot namespace. A vector entry remains an untrusted causal claim, not a signature by the device it names. A namespace collision never selects a public key or changes deduplication by write ID.

### 9.2 Field semantics (data records)

Write `reach(x)` for `x.context` joined with `x`'s own dot, and say a vector **covers** another if it is at least as large in every entry.

**Registers.** Every write belongs to one register: `(assign, record, field)`, `(set, record, field, element)` for `add` and `remove`, or `(delete, record)`. Field names and elements are compared as bytes (§9.1). An `assign` and an `add` to one field name are in different registers and never supersede each other; what an app makes of a field that holds both is its own business.

**Supersession.** A write `x` is superseded by a write `w` iff `w` is in `x`'s register, or `w` is a `delete` of `x`'s record and `x` is not a delete, and either

- `w` is from a **later** epoch, and `w.base` covers both `reach(x)` and `x.base`; or
- `w` is from the **same** epoch, `w` includes `x`, and `x` doesn't include `w`, where "`w` includes `x`" means `w.context` covers `reach(x)` and `w.base` covers `x.base`.

A write never supersedes a write from a later epoch.

For writes by honest devices this is the familiar rule: a write supersedes exactly what its author had seen. The rest only matters when a context is malformed. Two writes that each claim to cover the other are both kept, as a conflict. A context that claims to cover the future binds only its own epoch, because a new epoch starts with empty contexts. Whatever the vectors say, supersession is a strict partial order, with no cycles (§9.3).

**Building a write.** The new write's `context` is the join of `reach(k)` over the kept writes `k` of **the register being written** that are of the current epoch, and its `base` is the join of `k.base` over all kept writes of that register plus `reach(k)` over those from earlier epochs. A `delete` builds both over every kept write of the record, in all its registers. The write therefore supersedes everything the device knew in that register. Taking a whole field's writes for a set element would be wrong: a dot from another element could then cover an `add` the device never saw, and add-wins would fail.

**Vector size.** A `context` or `base` with more than 1,024 entries makes a write invalid. Device IDs in a vector are claims nobody can check, so without a limit a writer could sign contexts full of made-up IDs, and every later write would have to copy them. Honest collections stay far below the limit: a vector has one entry per device that ever wrote the field. If the vectors a write would need don't fit, the device writes with what fits, keeping the most recent dots. Its write then doesn't replace everything, and no later write can settle the field. The way out doesn't depend on vectors, because a delete takes effect whatever it covers: once the member responsible has been removed, any writer deletes the record and creates it again. The app is told when a field is in that state.

**Register (op `assign`): a multi-value register.** For each field, a device keeps the writes not superseded by another kept write.

- If one write remains, that is the value.
- If several remain, they are **concurrent siblings**. The displayed value is the sibling with the greatest `(epoch, hlc, author, deviceID, writeID)`. The app is told about the others (Liszto keeps conflicting note text, §17). Any later assignment that covers all siblings resolves the conflict.
- Every sibling is kept. The app is shown at most 16, the greatest by the order above. The limit is on presentation only: dropping siblings from the state would make the result depend on the order writes arrived in.

**Set (ops `add` / `remove`): an add-wins observed-remove set.** Per element, the `add` and `remove` writes form a register of their own under the same supersession rule, and the element is present iff at least one kept write for it is an `add`. A concurrent add and remove therefore leave the element in (add wins); a remove that saw every add takes it out; a replayed old add stays superseded.

**Delete (op `delete`): sticky tombstone.** A delete write has an empty field name, no element and an empty value (`H("")` in `sigInput`), so every SDK signs it the same way. It supersedes every write of its record that it includes, in any register, and only another delete that includes it supersedes a delete. A deleted record therefore shrinks to its tombstone, plus whatever was written without having seen the delete. A record with any valid `delete` write is deleted, whatever else happens to it, and stays deleted (§12.3). Undeleting means creating a new record. (This is "delete wins", a deliberate product choice: an offline device editing a record someone deleted doesn't resurrect it.) `delete` is not used on the roster or named records (§7.3).

**Unknown fields** (written by a newer app version) are merged and carried like any other, so mixed versions don't lose data.

### 9.3 Record state and convergence

A data record's state is the set of its valid writes that no other valid write supersedes. Merging two states is set union followed by dropping superseded writes. Supersession is irreflexive and transitive for any `context` and `base`, honest or not, so the kept set is simply the set of maximal writes, and merging is a join on a semilattice: commutative, associative and idempotent. Devices that have seen the same writes hold the same state, regardless of order, duplicates or replays. For honest writers the rule coincides with the standard multi-value register and observed-remove set over dotted version vectors (Shapiro et al. 2011; Preguiça et al. 2012).

Whether a write is valid never changes (§8.3), so a device never has to judge a write twice. Discarding what is superseded has one limit. A write that is superseded only by writes the next epoch's inventory doesn't list is current again in that epoch: the usual case is a late write from a member who is being removed and so can't sign it again. A device therefore keeps the superseded writes of an epoch until the next epoch's inventory is fixed, and may then drop those the inventory doesn't list. Hub messages are still collected as before (§10.7); the cost is local storage that grows for as long as an epoch lasts (Q22).

**Malicious writers.** A member can sign anything: the same dot twice, contexts that cover each other, contexts that cover the future. None of it breaks convergence. Writes are identified by write ID, so two writes with one dot are simply two writes, and the ordering has no cycle to exploit. What such a member can do is keep a field in conflict, or pin a value of their own on top of it, for as long as they are a member (§1.2). The rotation that removes them ends it: old-epoch contexts don't bind writes of the new epoch, so the next honest write settles the field. The cost falls on late honest writes to that field from the poisoned epoch: they come back only when their author signs them again (§11.6). Where a member has left a field with more to cover than a write can carry, the record is deleted and created again (§9.2). (Kleppmann 2022 gets the same guarantee from hash-linked histories; Tandem keeps version vectors for data so that superseded writes can be discarded.)

### 9.4 The roster chain and dead collections

Statements are `assign` writes to field `s` of `_roster`, carrying the complete JSON state and the parent statement ID in `ref`. Their IDs are the write IDs. The first 16 bytes of a collection's genesis ID identify the collection. A statement's exact value bytes are signed without JSON reserialization.

**Following the roster.** Starting at the genesis, a device follows the one successor signed by the owner. Out-of-order statements are held until their parents arrive. A device keeps every statement the owner signed. An owner-signed successor that is invalid under §8.2 is kept and counts for the conflict test, but it has no successors: the chain ends at its parent, and no data write may name it. The device that signed it has a bug, and the remedy is a copy (§11.8).

**Conflict.** Receipt of any two `_roster` successors of the same statement with different statement IDs, both authenticated as historically authorized owner-device signatures, means the collection is **dead**, even if one of the two is invalid: the owner must never publish two decisions for one position. A statement that starts an epoch counts once it is announced; before that it is a draft that only the owner's devices can read (§11.6). The test needs the full signed statement bytes. Signatures without a valid owner-participant certificate/source proof do not count. A later-revoked owner device may still manufacture a competing old branch under historical authorization: this can kill the collection even after rekeying. The draft retains that denial-of-service boundary; a fixed new policy floor rejects new-epoch data from that device but is not a roster checkpoint. Preventing this requires a separate owner-control/checkpoint design, not a timestamp or ranked branch choice.

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

### 10.2 JMAP: set aside

This protocol uses IMAP. JMAP is outside this revision for two reasons:

- **One push stream per credential.** Fastmail, the main JMAP provider, keeps one EventSource connection per API token: a second connection with the same token closes the first within half a second. Every device then needs a token of its own, which only someone who can sign in to the mail account can make. IMAP has no such limit there.
- **Reach.** Nearly every mail provider speaks IMAP. JMAP is today mostly Fastmail, Stalwart and Cyrus.

The two can't be mixed on one hub either: on Fastmail a message appended over JMAP reaches an IMAP device only after about a minute. What JMAP did better was batching: hundreds of messages in one request. Live updates are about as fast either way, about half a second from one device to another. The abstract store (§10.1) would still admit a JMAP binding later.

### 10.3 IMAP binding (RFC 9051 / RFC 3501)

Required extensions: **UIDPLUS** (RFC 4315), **IDLE** (RFC 2177). Strongly recommended: **CONDSTORE/QRESYNC** (RFC 7162); a client that uses QRESYNC MUST issue `ENABLE QRESYNC` after authenticating (RFC 7162 §3.2.3). Authentication is `LOGIN` or `AUTHENTICATE PLAIN` over TLS with a password or app password; OAuth-only providers are out of scope (§1.2).

| Operation | IMAP |
|---|---|
| `ensureFolder` | `LIST "" Tandem`; `CREATE Tandem` if missing (`ALREADYEXISTS` is success). If the personal namespace has a prefix (`NAMESPACE`, RFC 2342), the folder is `<prefix>Tandem`. |
| `append` | `APPEND Tandem (\Seen) {literal}` with the full RFC 5322 message (§10.4); UIDPLUS returns the UID. |
| `changes` | **New messages**, in every case: `UID FETCH <last known UID + 1>:* (UID BODY.PEEK[HEADER.FIELDS (Tandem-Channel)])`, ignoring a returned UID below the start of the range (`n:*` always returns the last message). QRESYNC doesn't report new messages by itself (RFC 7162 §6). **Removed messages**: with QRESYNC, the `VANISHED (EARLIER)` response to `SELECT Tandem (QRESYNC (uidvalidity modseq))`; without it, compare `UID SEARCH ALL` with the local UID set (fine at Tandem's scale). A `UIDVALIDITY` change means a full resync. |
| `fetch` | `UID FETCH (BODY.PEEK[HEADER.FIELDS (Content-Type Content-Transfer-Encoding)] BODY.PEEK[TEXT])`, then undo the transfer encoding and the charset (§10.4). |
| `destroy` | `UID STORE +FLAGS.SILENT (\Deleted)` then `UID EXPUNGE` (UIDPLUS, so other messages aren't affected). |
| `watch` | `IDLE` on the selected `Tandem` folder; `EXISTS`/`EXPUNGE`/`VANISHED` responses trigger `changes`. Re-issue IDLE at least every 29 minutes (RFC 2177). Send a `NOOP` in the same write before each `IDLE`: some servers (Stalwart) report a change made between the last command and IDLE only on the next command. |

**Lost answers.** An append whose answer was lost may still have landed, and a device can be told of its own message by IDLE before the `APPEND` has answered. A device therefore knows its own messages by `H(envelope)` (§10.4), not by the UID in an answer, and looks for an envelope on the hub before appending it a second time.

**Why IMAP.** It reaches almost every provider that offers a password or an app password: Fastmail, iCloud Mail, self-hosted Dovecot and Stalwart, and most hosts. Measured on Fastmail (2026-11-12): every IDLE connection on one app password is told of changes, 40 connections at once were accepted, and an append reaches the other devices in about half a second (measured 2026-11-13 from a client in Europe; an earlier probe saw about two). **Costs:**

- the client needs an IMAP implementation. The Swift SDK has its own, `TandemIMAP`, on Apple's Network framework with no other dependency: one connection that leaves IDLE to run commands, QRESYNC where offered, Gmail's IDLE restart and its All Mail cleanup. Apple's `swift-nio-imap` was tried against Fastmail on 2026-11-13 and did everything this binding needs, but its `async` client was unreleased and it brought SwiftNIO along;
- push needs a long-lived TCP connection, which mobile OSes suspend;
- servers cap concurrent connections, per address or per account. Dovecot's default is 10 per user and IP, so a household behind one address can reach it. Stalwart's is 16 per account wherever they come from, so a hub there serves 16 devices in all, across every user. Every device holds one connection open for IDLE; a device uses a single connection and leaves IDLE to run commands;
- some servers lack QRESYNC;
- an `APPEND` carries one message, so a key change that uploads a whole collection, or a first sync, sends many commands where a batching protocol would send one. Measured on Fastmail: 100 messages took 51 s one at a time and 21 s in one `MULTIAPPEND` (RFC 3502), which Fastmail offers. Most of that is a round trip per message, which a client that sends literals without waiting (LITERAL+, also offered) would avoid;
- a whole hub has not yet been run over IMAP on Fastmail from end to end (Q26);
- on **Gmail** (measured 2026-11-13 with `probes/imap_probe.py`, from an app password) an append or an expunge reached an idling connection only after about 30 s each time (29.8–30.7 s), where Fastmail takes half a second. Appending to the inbox, watching `[Gmail]/All Mail` instead, or changing a flag made no difference: every kind of change arrives about 30 s late. Ending IDLE and starting it again does: a new IDLE reports changes that are about 6 s old or more, one to two and a half seconds after it starts. Restarting every 5 s brought changes in after about 12 s, every 2 s after about 8.5 s, so about 8 s is the floor. A client on Gmail therefore restarts IDLE every 3 s while the app is active, a per-hub setting; 3 s lies between the measured 2 s and 5 s and has not been measured itself. It is a timer, not an event, used only where the provider leaves no other way (Q30). Gmail has `UIDPLUS`, `IDLE`, `CONDSTORE` and `MOVE` but not `QRESYNC`, `MULTIAPPEND` or `LITERAL+`; 20 appends one at a time took 22 s. A folder is a Gmail label, and expunging a message from it leaves the message in `[Gmail]/All Mail`, so on Gmail `destroy` must then move it to `[Gmail]/Trash` and expunge it there; the probe did that and left nothing behind. Its connection limit was not measured.

### 10.4 Message format

A message is stored as:

```
From: Tandem <tandem@invalid>
Date: <time of append, UTC>
Message-ID: <random@tandem.invalid>
Subject: tandem
MIME-Version: 1.0
Tandem-Version: 2
Tandem-Channel: <channel tag>
Content-Type: text/plain; charset=us-ascii
Content-Transfer-Encoding: 7bit

<base64 of the envelope (§5.4), wrapped at 76 characters>
```

Nothing else is in the clear. The hub already knows the append time.

**Reading a message.** A server, or a mail client that touched the folder, may have stored the body in another transfer encoding or charset than it was appended in. A reader therefore takes three steps:

1. undo the body's transfer encoding (`7bit` and `8bit`: nothing to undo; `quoted-printable`; `base64`);
2. decode the resulting bytes to text with the `charset` of the `Content-Type` header. If the body was stored in a charset other than `us-ascii`, UTF-16 for instance, skipping this step yields garbage;
3. remove all whitespace and base64-decode what is left to get the envelope.

The channel tag is the `Tandem-Channel` value, unfolded and trimmed. It is matched byte for byte against the tags a client derives; the same letters in another case are another tag. A message whose body doesn't decode to an envelope is ignored. A message's identity across devices is `H(envelope)`, not its IMAP UID.

### 10.5 Plaintext payloads (inside the envelope)

**Kind 1, record state.** One message holds the state of one record, or part of it (§10.6):

```json
{
  "rec": "<b64u record id>",
  "writes": [
    {
      "f": "title",
      "op": 1,
      "v": "<b64u value>",
      "a": "<b64u participant id>",
      "d": [
        "<b64u device id>",
        1731312000123,
        0
      ],
      "r": "<b64u roster/home policy id>",
      "c": [
        [
          "<b64u device id>",
          1731311999000,
          2
        ]
      ],
      "b": [
        [
          "<b64u device id>",
          1731300000000,
          0
        ]
      ],
      "s": "<b64u signature>",
      "cert": "<b64u device certificate id>",
      "auth": "<b64u authorization snapshot id>"
    }
  ]
}
```

`cert` and `auth` name the device certificate and immutable policy snapshot; the hub is fixed by the verified channel context and signed input. `r` is the write's `ref`, `c` its context and `b` its base (§9.1). Missing authentication dependencies hold the whole affected write, including during garbage collection. A roster statement has `r` (none in a genesis), empty `c` and `b`, and no `e`. `v` is present exactly on an `assign` and `e` exactly on an `add` or `remove`; a `delete` has `"f": ""` and neither. An absent `v` signs as the empty value.

**Kind 1, inventory fragment.** Instead of `rec`/`writes`, a message may contain `{ "inventory": { "epoch": "<epoch-start id>", "part": 0, "parts": 1, "bytes": "<b64u fragment>" } }`. Exactly one of these two payload forms is allowed. Parts are numbered from zero; all parts must agree on `parts`. Assemble in index order, decode the canonical inventory and check its hash against the epoch statement. Duplicate identical fragments are harmless; inconsistent versions are held separately until a complete hash-matching assembly exists. A fragment is not an authorized inventory by itself.

Clients MUST use a real JSON serializer and MUST ignore unknown keys. A value (`v`) is opaque bytes: a client verifies and passes it on exactly as received and never re-serializes it.

**One reading.** Signatures are checked against parsed fields, so no two devices may read one message differently. JSON parsers do differ (RFC 8259 §4): given `"rec"` twice in one object, Apple's Foundation keeps the first and Python's `json` the last. A malicious writer could sign for the first and have every last-wins client reject what every first-wins client accepts. All JSON this document defines, in payloads, roster statements, QR codes and links, is therefore invalid, and rejected whole, if:

- any object has the same property name twice, at any depth. Names are compared as their decoded UTF-8 bytes: `é` as one code point and as `e` with a combining accent are two names, not one name twice;
- any number is not an integer in plain digits (no fraction, no exponent) of magnitude at most 2^53. HLC millis (≤ 2^48) and counters fit;
- it is not valid UTF-8, starts with a byte-order mark, holds an escaped surrogate that isn't half of a pair, or has anything but whitespace around the one value;
- a `b64u` value is not in its one spelling (§5.2). Lenient decoders read `QQ`, `QR` and `QQ==` as the same byte, and an ID would then have several texts;
- a `c` or `b` vector names a device twice or is not sorted by device ID, the order it is signed in (§5.5).

**Bytes, not strings.** Wherever this document compares text (field names, property names, record names, channel tags), it means the UTF-8 bytes, with no Unicode normalization and no case folding. Password input alone has the explicit NFC rule of §6.7; it is not a field-name comparison rule. Some string types compare otherwise: Swift's `==` says that `é` and `e` with a combining accent are equal, Python's says they differ. Two clients built on those would keep different writes for the same field and never agree.

A standard parser that silently picks a winner among repeated names, or folds equivalent names into one, is not enough: Apple's Foundation does both. A client on such a platform checks before parsing.

**Kind 2, inbox message** (HPKE-sealed): grants, key requests and roster replies (§11.4).

**Kind 3, request or notice**, each with a `type`: pairing claims, possession challenges, outcomes and releases (§6.3); management registry controls (§10.8), leave notices (collection channel, §11.5) and key-request notices in an earlier collection epoch (§11.4).

### 10.6 Sizes

A message SHOULD stay under 64 KiB and MUST stay under 1 MiB. A record whose state doesn't fit (a long chain, a field with many siblings) is published as several messages, each holding part of its writes; the record's state is the union of what its messages hold. The parts of a split record may overlap, and such a record is not promised to settle into a fixed set of messages on the hub. Snapshot inventories are split into indexed fragments under the same message limit; limits on assembly memory and incomplete fragments are configurable. A single write value must itself fit the message limit. Apps put large content elsewhere or split it.

### 10.7 Publishing and garbage collection

A device publishes a record by appending a kind-1 message with the record's current state (for a record in several messages, the part that changed). A message `m` is **dominated** by a message `n` of the same record if every write in `m` is in `n` or is superseded by a write in `n`. Writes are compared by write ID (§5.5). Held writes (§8.3) count as writes here, and inventory fragments are never dominated: an epoch's inventory stays on the hub for as long as the epoch is current, and devices that hold it restore a missing fragment (§12.2). The roster, and the two statements that prove a collection dead, are never pruned. The one exception is the channel of a key change that was discarded before it was announced: everything in it goes, its draft statement included (§11.7).

- A device MAY destroy a data message once it has merged it and another message on the hub dominates it. Two messages can dominate **each other**: when they hold the same writes, and also when they differ only in writes that are superseded anyway (`{x, y}` and `{y}`, where `y` supersedes `x`). Of two such messages, only the one with the greater `H(envelope)` may be destroyed on account of the other. Every device makes the same choice, so two devices never destroy each other's message and leave none.
- Byte-identical copies, which have the same `H(envelope)`, come from an append that was repeated after its answer was lost (§10.3). Only the device that made that envelope removes the extra copies, keeping one. Other devices leave them alone, since no hash can tell them which to keep.
- It MUST NOT destroy a message it hasn't merged, or a message of a version it doesn't understand (§15). Incomplete objects can be evicted locally under §9.4; that does not authorize destroying their hub messages. Statements the owner signed are kept.
- When a device sees messages for one record that none of them dominates (concurrent publishers), it waits a short random time, a few seconds at most, and publishes the merged state only if no dominating message has appeared in the meantime. Without the delay, every online device would publish the same merge at once. A record that fits one message converges to one message on the hub between bursts of activity. For a record split over several messages the test is the union: a device publishes only if the valid messages on the hub, taken together, don't dominate its own state. Parts that merely differ from each other are no reason to publish. Messages are still destroyed only one against one, by the rule above. Letting a set of messages dominate one would not be safe: of `{a, b}`, `{b, c}` and `{a, c}` each is covered by the other two, and three devices could destroy all three.
- Writes made in quick succession SHOULD be coalesced (debounce ~1 s) into one publish, so typing doesn't create a message per keystroke.

### 10.8 Control objects

Everything that is not a record write is a **control object**: the hub's genesis, admin capsules, registrations, enrollment requests, certificates, revocations, invitation capabilities, pairing delegations, receipts, authorization snapshots, home-epoch starts, grants, key requests, leave notices and sharing answers. Each has a fixed signed input built only from the types below, and its ID is the hash of that input, so signatures never enter an ID (§5.5). Each input starts with its own tag, `TANDEM-…-V2` for a signed object and a `tandem/v2/` label for the one hashed object, the authorization snapshot. No tag is a prefix of another, so a signature for one kind can't be read as another (`proofs/TandemProofs/Encoding.lean`).

| Type | Bytes |
|---|---|
| `id` | 32 bytes: an object ID, a hash or a public key |
| `id16` | 16 bytes: a collection ID, an enrollment reference, a token or invite ID |
| `text` | `lp(UTF-8)`; at most 256 bytes, compared as bytes (§10.5) |
| `set` | `u32(n)` followed by `n` sorted, unique `id`s; at most 65,536 |
| `none` | 32 zero bytes, where an `id` is optional; an optional `id` that is all zeros is invalid |
| `u8` … `u64` | Unsigned big-endian integers of that width; at most 2^53 in JSON |
| `answer` | One ASCII byte: `a`, `n` or `l` |
| `mailboxes` | `u32(n)`, then `n` entries `host` text, `port` u16, `user` text, sorted by their bytes and unique; at most 64. `host` is ASCII, lowercase (an IDNA A-label for a non-ASCII name) |

**Transport.** A control object travels as one strict JSON object (§10.5) with `"type"` set to the name in the table, one property per field, named as in the table and spelled as follows: `id` and `id16` in b64u, `text` as a JSON string, integers as JSON numbers (a `u64` time is Unix milliseconds), an `answer` as a one-letter JSON string, `mailboxes` as an array of `{"host","port","user"}` objects, a `set` as an array of b64u strings in its sorted order, `none` as JSON `null`, and each signature as b64u under the name the table gives. An optional signature that is absent is left out; a reader also takes `null` for it. A reader rebuilds the signed input from the parsed fields, checks the signatures, and computes the ID itself; it never trusts an ID that came in a message. Data an object carries beside its fields must match what they commit to: a capsule's `capsule` bytes must hash to `sealed`, and a certificate's `body` must be the request its `request` names. A reader rejects the object otherwise.

#### The hub

| Type | Signed input, after the tag | Signed by | ID |
|---|---|---|---|
| `genesis`<br>`TANDEM-GENESIS-V2` | `hub` id (`pub(MKs)`), `agree` id (`pub(MKa)`), `hubkey` id (`commit(HubK)`, §5.7), `creator` id (the creator's participant ID), `mailboxes` u32 count then, sorted by their bytes, each `host` text (lowercase), `port` u16, `user` text | `MKs`, as `sig` | `genesisID` |
| `capsule`<br>`TANDEM-CAPSULE-V2` | `hub` id, `genesis` id, `sealed` id (`H(salt ‖ nonce ‖ ciphertext)`, §6.7), `supersedes` set (capsule IDs) | `MKs`, as `sig`; the JSON also carries `capsule`, the sealed bytes, in b64u | capsule ID |
| `snapshot`<br>`tandem/v2/auth-snapshot` (hashed, not signed) | `hub` id, `genesis` id, `participant` id, `certs` set, `revocations` set | nobody | authorization snapshot ID (§6.9) |

`mailboxes` lists the permitted descriptors without credentials. A device that pins a hub pins `hubID` and `genesisID` together and refuses a second genesis (§6.9). The capsule ID is what a later capsule names in `supersedes` (§6.7). An authorization snapshot is a hash, not a signature: anyone can publish one, and it means only the exact sets it names.

#### Participants and devices

| Type | Signed input, after the tag | Signed by | ID |
|---|---|---|---|
| `registration`<br>`TANDEM-REGISTRATION-V2` (hashed, not signed) | `hub` id, `sign` id (`pub(DKs)`), `agree` id (`pub(DKa)`), `enrollment` id16 | nobody | the participant ID `uid` (§5.3) |
| `request`<br>`TANDEM-REQUEST-V2` | `hub` id, `genesis` id, `participant` id, `sign` id, `agree` id, `label` text, `how` u8, `authority` id or none, `enrollment` id16 | the new `DKs`, as `self`; and the approver, as `approver`, when `how` is 2, 3 or 4 | request ID |
| `certificate`<br>`TANDEM-CERT-V2` | `hub` id, `genesis` id, `request` id, `issued` set (revocation IDs) | `MKs`, as `sig`; for `how` 2, the invite key | certificate ID |
| `revocation`<br>`TANDEM-REVOKE-V2` | `hub` id, `genesis` id, `target` id (a certificate ID), `retained` set (certificate IDs of the same participant) | `MKs`, as `sig` | revocation ID |

`how` is the way the keys came in, as in the registry proofs: 1 a first admission (the creator's, or `uid` made directly by the admin), 2 through an invitation capability, 3 approved by a device of the same participant, 4 through a pairing delegation, 5 the creator's recovery (§6.8). `authority` is the capability ID for 2, the approving device's certificate ID for 3 and the delegation ID for 4, and none for 1 and 5. `enrollment` is the registration's reference for 1 and 2 and zero otherwise. For 1 and 2 the participant ID must be the registration of exactly these keys and this enrollment; for 5 it must be the creator the genesis names; for 3 and 4 it must be the participant of the approving certificate or delegation. The approver is the invite key for 2 and 4, and the approving certificate's `DKs` for 3. The JSON of a certificate carries its request whole, as a nested object under `body`, so a certificate can be checked alone.

A request is signed by the new `DKs` as proof that its holder asked for it. The agreement key is bound by that signature, not proved separately: X25519 has no way to sign, and a claimant who names an agreement key it doesn't hold only receives grants it can't open. Certificate validity is the registry rule of §6.9, checked on these exact fields.

#### Invitations and pairing

| Type | Signed input, after the tag | Signed by | ID |
|---|---|---|---|
| `invitation`<br>`TANDEM-INVITE-V2` | `hub` id, `genesis` id, `key` id (the invite public key), `invite` id16, `expires` u64, `label` text | `MKs`, as `sig` | capability ID |
| `delegation`<br>`TANDEM-DELEGATION-V2` | `hub` id, `genesis` id, `participant` id, `source` id (the phone's certificate ID), `key` id (the link's invite public key), `token` id16, `expires` u64, `package` id (`H` of the package bytes), `gate` id (`H` of the gate box) | the source `DKs`, as `sig` | delegation ID |
| `receipt`<br>`TANDEM-RECEIPT-V2` | `hub` id, `genesis` id, `outcome` u8, `subject` id, `signer` id (a certificate ID, or none) | see below, as `sig` | receipt ID |

**Invitation.** The admin picks a random 32-byte secret `s` and a 16-byte invite ID. The invite key is the Ed25519 key whose seed is `KDF(s, hubID ‖ invite, "tandem/v2/invite-key", 32)`. The link carries, in strict JSON, `{"v":2,"hub","genesis","hk","mail":{"host","port","user","pass"},"s","invitation":{…}}`, with `hk` the directory key `HubK`. The new device makes its keys and an enrollment reference, registers, and signs a request with `how` 2 both with its own `DKs` and with the invite key; then it signs the certificate itself with the invite key, with `issued` the revocation IDs it holds. That chain is the whole admission (§13.1). A capability is good until `expires`, checked by the accepting device. Cancelling one is a receipt with outcome 3; honest devices then refuse it, but admissions already made through it stand, and the admin revokes those that were not wanted. Two people using one link are two participants, both visible.

**Pairing.** The phone picks the token ID and `L` and `A` (§6.3). The link's invite key has seed `KDF(L, hubID ‖ token, "tandem/v2/pair-invite", 32)`. The package is `nonce ‖ ChaCha20-Poly1305(KDF(L ‖ A, token, "tandem/v2/pair-package", 32), nonce, plaintext, aad)`, with a random 12-byte nonce, `aad = "TANDEM-PAIR-PACKAGE-V2" ‖ hubID ‖ token` and plaintext the strict JSON `{"home":"<b64u home-epoch ID>","k":"<b64u HK>"}`. The gate box is `A` HPKE-sealed to `MKa` with `info = "tandem/v2/pair-gate"` and `aad = "TANDEM-PAIR-GATE-V2" ‖ hubID ‖ token ‖ participant`. The delegation, package and gate are kind-3 messages in the **pairing channel**: key `KDF(L, hubID ‖ token, "tandem/v2/pair-key", 32)`, tag `b32(KDF(L, hubID ‖ token, "tandem/v2/pair-tag", 20))`, with `type` `delegation`, `package` (`{"type":"package","bytes":…}`) and `gate` (`{"type":"gate","bytes":…}`). The link carries `{"v":2,"hub","genesis","hk","mail":{…},"l","t","participant"}`, with `l` = `L` and `t` the token.

The new device signs a request with `how` 4 and `authority` the delegation ID, with its `DKs` and the link's invite key, and sends it as `{"type":"pair-claim","request":{…},"delegation":{…},"gate":"<b64u>"}` to every admitted device of the creator, in the creator's inbox: the creator is the one participant a device can know to hold `MS`. The approving device unlocks `MS`, checks §6.3 step 3 and the registry rule, publishes the delegation, the certificate and a current authorization snapshot of the participant in the directory, in that order, and then seals `{"type":"pair-release","certificate":"<b64u ID>","delegation":"<b64u ID>","package":"<b64u hash>","a":"<b64u A>"}` to the new device's agreement key, in its participant's inbox. The new device opens the package only if all three named values are the ones it holds; it then publishes a receipt with outcome 4 and adds it to the `outcome` of the `pairing:` record in home. The certificate and the release are journalled together before either is sent, and sent again unchanged after a crash.

**Receipts.** `outcome` is 1 for a rejected claim (`subject` the request ID), 2 for a cancelled pairing (`subject` the delegation ID), 3 for a cancelled invitation (`subject` the capability ID) and 4 for a completed pairing (`subject` the certificate ID). A rejection and a cancelled invitation are signed with `MKs` and have `signer` none: rejecting, like approving, takes the admin's authority, so nobody else can mark someone's claim rejected. A cancelled pairing and a completed one are signed by a device of the pairing participant, with `signer` its certificate ID. A valid certificate outweighs any rejection (§6.3).

#### Homes

| Type | Signed input, after the tag | Signed by | ID |
|---|---|---|---|
| `home-epoch`<br>`TANDEM-HOME-EPOCH-V2` | `hub` id, `genesis` id, `participant` id, `n` u32, `parents` set (the home-epoch IDs it follows; empty for the first), `key` id (`commit(HK)`), `recipients` set (the certificate IDs `HK` is sealed to), `snapshot` id (inventory hash, §8.3), `signer` id (certificate ID), `auth` id (authorization snapshot) | the signer's `DKs`, as `sig` | home-epoch ID `E` (§5.6) |

A home epoch is announced by publishing its start as a kind-3 message in its own channel and in each parent's channel, and delivering `HK` to every recipient as `{"type":"home-key","epoch":"<b64u E>","k":"<b64u HK>"}` in the participant's inbox, sealed to that certificate's agreement key. The creator's home key is also sealed to `MKa`, with `info = "tandem/v2/home-recovery"` and `aad = "TANDEM-HOME-RECOVERY-V2" ‖ hubID ‖ E`, as `{"type":"home-recovery","epoch":…,"bytes":…}` in the directory (§6.8). `n` is one more than the greatest parent's. Every recipient must be admitted under `auth`, and `auth` must extend every parent's.

Two devices of one participant can start home epochs from the same parent at the same moment. Unlike a roster (§8.2), a home does not die of it: its records merge like any other (§9), and both successors exclude whatever their signers had revoked. A device follows every current head, that is every announced home epoch that no announced epoch names as a parent, and writes to the one with the smallest ID. The next home epoch names all current heads as `parents`, which joins them again. A device that holds a revocation that a current head's `auth` doesn't reflect starts a new home epoch at once. A device that sees an admitted keychain of its own participant that a current head's `recipients` leave out, such as one paired after its package was staged, delivers that head's `HK` to it as a `home-key` notice, once.

#### Collections

| Type | Signed input, after the tag | Signed by | ID |
|---|---|---|---|
| `grant`<br>`TANDEM-GRANT-V2` | `hub` id, `collection` id16, `recipient` id (participant), `admission` id, `at` id, `epoch` id, `key` id (`commit(K)`), `signer` id (owner certificate), `auth` id | the owner device's `DKs`, as `sig` | grant ID |
| `key-request`<br>`TANDEM-KEYREQ-V2` | `hub` id, `collection` id16, `participant` id, `have` id (a roster statement ID), `signer` id, `auth` id | the requesting device's `DKs`, as `sig` | request ID |
| `leave`<br>`TANDEM-LEAVE-V2` | `hub` id, `collection` id16, `participant` id, `at` id (the statement of the admission being left), `signer` id, `auth` id | the leaving device's `DKs`, as `sig` | notice ID |
| `answer`<br>`TANDEM-ANSWER-V2` | `hub` id, `collection` id16, `participant` id, `admission` id, `answer` u8 (`a`, `n` or `l` as its ASCII byte), `event` id16, `signer` id, `auth` id | the answering device's `DKs`, as `sig` | answer ID |

A grant is delivered as `{"type":"grant","grant":{…},"certificate":"<b64u recipient certificate ID>","k":"<b64u K>"}`, sealed to that certificate's agreement key in the recipient's inbox. A reply to a removed member is `{"type":"roster","c":"<b64u collection ID>","statements":[…]}`, the roster statements as kind-1 writes, which prove themselves. Leave notices and key-request notices are kind-3 messages in a collection channel; key requests also go to the owner's inbox. An answer is sealed to the devices of the participant who shared the collection, as the receipt of §6.10.

**Roster statements.** A statement is the value of the `s` field of `_roster`, the UTF-8 strict JSON

```json
{ "n": 12,
  "key": { "n": 3, "commit": "<b64u commit(K)>", "prev": "<b64u previous commit>" },
  "snapshot": "<b64u inventory hash>",
  "members": [ { "uid": "<b64u participant ID>", "role": "owner|writer|reader",
                 "name": "Alice", "floor": "<b64u authorization snapshot ID>" } ],
  "deleted": false }
```

with `members` sorted by the bytes of their `uid`. A genesis has `"n": 0`, `"key": {"n": 0, "commit": …}` without `prev`, no `snapshot`, and `"nonce"`: 16 random bytes in b64u (§7.1); no other statement has `nonce`. `floor` is the member's device-policy floor (§8.1). A statement that keeps the epoch keeps every listed member's `floor`; one that starts an epoch sets every floor afresh. `name` is a display hint, at most 256 bytes.

#### The directory

Genesis, capsules, registrations' certificates, revocations, invitation capabilities, pairing delegations once a claim through them is approved, receipts, authorization snapshots and home-recovery boxes are kind-3 messages in the directory channel (§5.6). Every receipt is there, so an admin device sees a cancelled pairing and every admin device sees a rejection. A **profile** is an ordinary record there: writes (§5.5) with `scopeID = H("tandem/v2/directory" ‖ hubID)[0..16]`, the named record `profile:<b64u uid>` with fields `_name` and `name` (UTF-8, at most 256 bytes), `ref` the genesis ID, and `cert` and `auth` those of an admitted device of exactly that participant. A profile write by any other participant's device is invalid. Names merge like any other field (§9.2).

#### Home records

The `hubinvite`, `pairing` and `activity` records of §6.2 have these fields, in addition to `_name`:

| Record | Field | Op | Bytes |
|---|---|---|---|
| `hubinvite:…` | `capability` | assign | The 32-byte capability ID |
| `pairing:<b64u token>` | `delegation` | assign | The 32-byte delegation ID |
| | `label` | assign | UTF-8 text |
| | `expires` | assign | ASCII decimal digits: Unix milliseconds |
| | `outcome` | add | One element per receipt: its 32-byte receipt ID |
| `activity:<b64u event>` | `event` | assign | The strict JSON `{"what","at","refs"}`: `what` one of `invited`, `invite-cancelled`, `paired`, `pairing-cancelled`, `revoked`, `shared`, `answered`; `at` the device's clock in Unix milliseconds, a claim, not an order; `refs` the IDs of the control objects it concerns, in b64u |

---

## 11. Sharing

### 11.1 Sharing

A collection is shared with people **on its hub**, chosen from the hub's directory (§6.6). There are no list invites, links or codes for a collection. Someone who isn't on the hub yet first gets a hub invite (§13.1); once their entry appears in the directory, they can be added.

The owner picks a person from the directory and a role, writer or reader. The owner's device:

1. reads that person's directory entry, verifies their registration/admitted-device proofs and device-signed profile (§6.6), and obtains the participant ID, device delivery bindings and name;
2. signs a same-epoch roster statement that adds the participant with that role, `uid`, initial device-policy floor and display name (§8.1), under the rules of §8.2;
3. once the statement is on the hub, sends the new member a grant (§11.4).

Only the owner shares, as only the owner changes the roster (§8.1). Adding someone is never automatic: it is always an owner's tap on one of their devices.

The directory holds whatever name a user gave themselves, so two entries can carry the same name. The owner's app shows, for each entry, who invited that person to the hub (`by`, §6.6) and, for people the owner invited, the note they wrote then, so a lookalike stands out.

### 11.2 Accepting

**Admissions.** Every time the owner adds a user to a collection, that is a new **admission**, named by the ID of the roster statement that adds them: the first statement that lists the user after one that doesn't, or after the genesis. A grant belongs to the admission in force at its `at`. Removing someone ends their admission; adding them again starts another.

**Asking.** A device that holds a valid grant (§11.4) for an admission its user hasn't answered doesn't follow the collection yet. It asks its user once, on every device of theirs: "Bob shared House with you. Add it?" It shows the owner's name as the directory gives it for the genesis's owner, and the collection's name, which it can read from `_meta` (§7.3) now that it holds the key. The offer itself is never written anywhere: it is the grant, kept in the inbox (§11.4), together with the absence of an answer. So no device can write an offer over an answer.

**Answering.** An answer is an element added to the membership's `answers` set in home (§6.2): the admission's ID and `a` for add, `n` for no, `l` for leaving later (§11.5). Elements are only ever added, so an answer can't be overwritten or undone by a write that hadn't seen it.

- **Add:** the user's devices follow the collection, and the key goes into `keys`.
- **No:** the device posts a leave notice (§11.5). The owner sees "Alice didn't add House" and can remove her. If the owner shares it with her again later, that is a new admission, and she is asked again.

**The state** of a membership is computed from the roster and the answers, never stored:

| Roster at the head | Answers to the current admission | State |
|---|---|---|
| Doesn't list the user | any | removed |
| Lists the user | none | offered, if a grant for it is held |
| Lists the user | `a` only | active |
| Lists the user | `n` or `l`, with or without `a` | left |

Two devices that answer at the same moment can disagree. Then `n` or `l` wins over `a`, whichever arrived first: it is the answer that has already posted a leave notice, and the owner will act on it. The user is told ("You said no to House on your iPad") and can ask the owner to share it again. Nothing appears in a user's lists without their saying so: someone on the hub who calls themselves "Bob" can share a collection, but not make it show up.

### 11.3 Adding someone again

A removed member, or one who left, is added again the same way, by the owner choosing them in the directory. That is a new admission, with its own ID, so their devices ask once more (§11.2). A grant from before the removal, posted again, belongs to the old admission, which already has an answer, and is ignored. Since sharing is always the owner's own act, nothing can bring a removed member back without the owner, and there is nothing that a removed member holds which would let them in again: no invite, no secret, no request to replay.

### 11.4 Grants and key requests

Inbox messages are kind-2 messages delivered to the participant's common inbox, each HPKE-sealed to a specific admitted device DKa. There is no participant `uka` private key to copy.

A logical **grant** is signed by an admitted owner device and binds hubID, recipient participant, that recipient's admission statement ID, collection ID, `at` roster ID, exact epoch-start ID, key commitment, acting owner certificate and source authorization snapshot. Delivery envelopes bind that same grant and the exact recipient certificate/key. Send to the recipient's verified admitted devices; once accepted, the membership and key also enter their shared home. A newly paired device receives the current home and therefore future memberships without a new owner invitation or roster entry. An existing admitted device of that recipient may re-seal the unchanged owner authorization to another admitted device of the **same** participant. It cannot change the grant's recipient/admission or promote a role.

It is sent only once `at` is on the hub and, for an epoch start, announced (§11.6). The new channel holds the complete roster through `at`, policy proofs and inventory so recipients can validate it. The grant's encoding is in §10.8; v1 grants are not accepted as v2 authorization.

**Grants are kept.** A recipient never destroys a grant; it stays in their inbox. The owner's devices keep, for each current member of each collection, the last grant they sent, heal it like their own data if it goes missing from the hub (§12.2), and destroy an earlier grant to the same member once a later one is on the hub. A grant to someone no longer listed is destroyed and not healed. So a grant that was deleted before it was read comes back, without the recipient needing to know that there was anything to ask for. The cost is one small delivery per relevant admitted device and collection that stays on the hub. Retain the owner authorization in home so an enrolled device can relay it to a later paired device; stop repairing envelopes to revoked device keys once the verified cutoff applies.

A receiver requires all of this:

- the grant belongs to the user's current admission (§11.2), or to a later one. A grant for an admission the user has answered with `n` or `l` is ignored; one for an admission without an answer is an offer (§11.2);
- the roster in the channel is one valid line from a genesis whose ID gives this collection ID, through `at`;
- the grant signature/certificate/source snapshot authorize an owner device for the participant genesis names; current deliveries/relays additionally check current root/admission and the recipient device cutoff (§6.9);
- `epoch` is the exact start of `at`'s epoch and carries `commit(k)`;
- `at` lists this user.

A device that already follows the collection checks the roster it is given against every statement it knows: a second successor anywhere means the collection is dead (§9.4). An older grant never moves it back. A grant for an earlier statement of the still-current epoch remains useful if the user is still listed. The device adds the key to home only after these checks. A grant that depends on something missing is held.

**Key requests** are device-signed objects binding purpose, hubID, collection ID, participant ID, exact requester certificate/source snapshot and `have`, the latest valid roster statement ID held. Leave notices and roster replies similarly bind their device certificate/source snapshot and hub/collection context (§10.8). These replace the v1 user-key signatures.

A device learning of an epoch whose key it lacks waits for normal grant delivery. After 10 minutes it may send one request to the owner's admitted device inboxes and post it as a kind-3 notice in the last epoch channel it can read. This timeout is a safety net behind grant/channel events, not a polling schedule. Owner devices keep watching earlier epoch tags through one hub change feed. Revoked devices receive no new key even if their participant remains a collection member; a stale certificate cannot be used as a key-request alias.

Any device of the owner answers. A currently admitted device of a current member gets the current key in an envelope bound to that exact recipient certificate. Someone since removed gets no key. They get `{ "type": "roster", "c", "statements": [ … ] }`: the signed roster statements from `have` up to the one that removed them, which prove themselves and are how a device that was away for several epochs learns of its removal. Answering is not a roster change, so two devices answering is harmless.

### 11.5 Leaving

A member adds the answer `l` for its current admission to home (§11.2), stops following, and posts a kind-3 device-signed leave notice in the epoch it knows, binding hub, collection, participant, exact admission/roster, actor certificate and source policy (§11.4). The owner's app shows "Alice left House" and removes her when the owner confirms, which starts a new epoch (§11.6). A notice counts only if `at` is a statement of the valid chain that lists the member, and every statement from `at` to the head lists them too. A key change in between does not spoil it; a removal does, so a notice from an earlier stay can't remove someone who has come back. The owner's devices read leave notices in every epoch whose key home holds. Until the owner confirms, the member stays listed.

The owner can't leave. They delete the collection (§11.9) or keep it.

### 11.6 Key rotation and crashes

Only an admitted owner device rotates: when the owner removes a member, lowers a role, asks for a new key, or authorizes a device-revocation/policy-cut batch (§6.4). It fixes current device floors for every retained participant and fresh keys, excluding revoked device keys from deliveries. Data edits work offline; a rotation needs the network.

The device makes one journalled change:

1. Sync the current epoch fully and choose the data state to preserve, including tombstones. Entries of the current inventory that the device hasn't seen are carried forward as they are (§8.3).
2. Generate the new key; fix per-member device floors and build the exact inventory and roster statement; persist the signed statement, inventory and progress in the device's journal; then write the key and the note to home (§8.2). Both come **before anything is exported**, and the journal comes before the note. A note that names a statement this device has no journal entry for is another device's note (§11.7). A note the device sees in home outweighs its own record of whether the note's append succeeded: an append whose answer was lost may have landed.
3. Upload the complete inventory fragments and the snapshot data to the new epoch's channel, then the full roster ending in the new statement. The statement is thus on the hub only once everything it commits to is. Verify it all, and restore anything destroyed meanwhile. No grant is sent yet.
4. **Announce:** sync. If the statement is already announced, by this device before a crash or by another device that finished the change, do the home update at the end of this step and go on to step 5. Otherwise check that the note in home still names this statement and that its parent is still the roster's head. If either fails, another device discarded or overtook the change (§11.7): destroy the new channel, drop the journal entry and tell the user that nothing changed. Otherwise publish the statement in the **old** epoch channel's `_roster`. This is the moment the change happens, and the only evidence that it did. Then update home: add the key to `keys`, and remove the note if it still names this statement. Both can be repeated without harm, and a device that finds the announcement made but home not yet updated does them first.
5. Send grants to the remaining members, the owner included. A grant can be sent again after a crash or on request.
6. Make sure the inventory is still on the hub. Then destroy the old epoch's **data** messages, each only once every write in it is in the new channel, has been replaced there by a valid write, or was not carried over; a listed write that is missing from the new channel is copied across first. Its roster stays for good, and home keeps its key: that is how a device that was away finds out where the collection went.

An interruption before the announcement leaves everyone on the old epoch. After a restart the device syncs first. If its statement is announced, it completes step 4's home update, which the crash may have cut off, and carries on from step 5. If its note is still in home naming its statement and the parent is still the head, it resumes the same change from the journalled step with the same statement. If there is no note at all, it resumes only a change it had not yet exported, and only while the parent is still the head. In every other case (another device's note, a new head) the change was discarded or overtaken: it drops the journal entry, destroys what it uploaded and tells the user. Unannounced bytes may always be dropped; announced ones never. An interruption after the announcement may leave members waiting for the key; they stop publishing to the old epoch, and the device resumes sending grants. If that device is gone, another device of the owner finishes or drops the change (§11.7). Nothing happens on a timer.

A device learns of the change from the announcement in the old channel, or from a verified grant and the roster in the new channel. The owner's other devices can read the new key in the note from step 2 on. Until they have the announcement they use it for one thing only: finishing that change when the user asks (§11.7). A missing note is not evidence, and neither is a key: a note goes when a change is announced and also when it is discarded (§11.7). Until it is announced, a statement that starts an epoch exists only in its own channel, under a key that only the owner's devices hold. There it is a draft: not part of the roster, not a successor, and not one half of a conflict (§9.4).

**Moving over.** A device that has learned of the change stops publishing to the old epoch and changes to the new snapshot. It keeps every old write the new inventory lists, whoever wrote it, and re-publishes one that the new channel lacks (§12.2). A missing inventory delays the move; a listed write that is absent does not (§8.3). A device coming back online reads the change feed before it sends the edits it had queued, so it never publishes into an epoch it is about to learn is over, where a removed member could still read. An edit made while a device waits for the new key is queued until the grant arrives. A removed user is told, stops publishing, and keeps their replica read-only for 30 days, configurable, so that unsent work can be rescued. They receive no new key.

**Late writes.** The same still-admitted authoring device may sign again its own late write: one that the inventory doesn't list and that no listed write supersedes under the old epoch's rules. A write that a listed write replaced is not signed again: that would bring the replaced value back on top. Vectors are claims, not proof that anything was delivered (§9.3), so two cases get special care:

- **A forged claim about this device.** If the new state supersedes one of the device's *own* writes only through a vector entry for this device's ID that is higher than any dot the device has issued, that entry is forged. The device signs the write again all the same, and it survives as a sibling. A later write by the same device always counts as having replaced its earlier one, whatever claims it copied, so this never brings back a value the user had already replaced.
- **Anything else the new state claims to cover.** A late write that is not signed again only because the new state supersedes it is kept, and reported to the app as work that may never have been shared, to recover or dismiss. The cost is that the app may be shown an edit that really was replaced.

**Signing again by the original device.** Keep the record, field, operation, element, value and original dot. Set the new context empty and base to the join of the old base and `reach(oldWrite)`. Use the first authorized statement of the new epoch listing the participant as owner/writer and the device's exact valid certificate/source snapshot extending its floor. The resulting input/write ID is deterministic for that device and those references; reuse persisted signed bytes on retry. If no authorization exists or the vector would exceed the limit, keep the work for the app.

**Recovery by another device of the same participant.** The laptop uses its own installation dot namespace, even if it shares the lost phone's keychain keys and certificate. It may preserve the old signed write through a fixed owner inventory; otherwise importing its content creates a **new write**, with the laptop's own dot and the certificate/source snapshot valid for its keys. Show it as recovered work, retain the old provenance privately, and do not silently claim to be the original authoring installation. Use new-epoch contexts/base under §9.2 rather than transporting the old installation's causal claims as current authority.

Signing again starts from an empty context, so the order between two writes that both missed the inventory is lost. If one had replaced the other, the replaced value returns as a conflict, and a removed set element returns, until the next write settles it. Keeping the old context instead would let a removed member's malformed context outlive its epoch (§9.3).

Only the owner's fixed inventory or the author's own signing again carries old writes into a new epoch. Another user's late write can be lost if its author never returns and it missed the inventory. Devices do not turn an incomplete snapshot into deletes.

### 11.7 Two devices of one owner

The owner's devices share collection access and home, and stay out of each other's way with the note in home (§8.2). A device that is asked to change a collection's sharing while another device's note is there doesn't sign. The note says what that change is, so the device can tell the user.

- **If the change's statement and inventory are on the hub** in the new channel, there is nothing to choose. The device finishes it and says so: "Finishing a change started on Bob's iPhone: making Alice read-only in House." It checks the new channel against the inventory, copies across from the old channel any listed write that is missing, announces the statement, sends the grants and cleans up, as the first device would have (§11.6). It finishes a change only while the note still names the statement the user was shown, and makes the check of step 4 before announcing.
- **Otherwise the change never happened**, and the device asks: "Bob's iPhone started making Alice read-only in House and didn't finish. Do it here, or discard it?" Either way it removes the note, and the key in it, from home and destroys what was uploaded. It removes the note only if it still names the statement the user was shown. For "do it here" it then makes the same change itself, with a new key, and signs only once the removal is on the hub.

A note that names a statement which is **already announced** is left over from a crash between the announcement and the home update. Any device of the owner that sees one adds the key to `keys` and removes that note, without asking: it is housekeeping, not a change to the roster, and two devices doing it do no harm. Otherwise the note would block every later change to sharing (§8.2).

It then makes the change it was asked for. A change is never dropped without the user being told which one it was. The first device, when it next syncs, looks for the announcement of its change in the old channel. If it is there, the change was made, by itself or by the device that finished it; it completes step 4's home update and carries on from step 5. If it isn't there and the note naming its statement is gone, the change was discarded: the announcement always precedes the note's removal and the hub has one change feed (§10.1), so a device that has seen the note go has seen the announcement if there was one. It then discards its journal entry and anything it had uploaded. While its note is still there and the parent is still the head, it resumes (§11.6).

Two things can still put two successors on one statement, and each kills the collection (§9.4): the hub hides one device's change from another; or two devices pass the check of step 4 in the same moment, each announcing before it has seen the other's announcement. The check narrows that window to one round trip. It can't close it (Q20). Members never end up following different rosters.

### 11.8 Dead collections and copying

A collection can't be administered any more in four cases:

- **It is dead** (§9.4): two successors of one roster statement exist. It is read-only for everyone.
- **No authorized owner device survives**, and creator recovery does not apply: Members can go on editing, but its sharing can never change again.
- **An owner device was compromised and its historical roster is forked** (§9.4). Revoking/rekeying an unambiguous collection can preserve its ID; a proven fork still requires a copy.
- **The owner published an invalid statement** (§9.4). The roster ends at the statement before it, and any other successor would kill the collection. Members can go on editing; its sharing can never change again.

There is no recovery under the same collection ID. Anyone who can read it can preserve a local copy. Publishing a **new collection** requires a currently admitted device of its new owner: they create it on the same hub or on another one they are on, of which they are the owner, write the current state of the records into it as writes of their own, and share it again. Tombstones and the old writes' signatures are not carried over; a copied item is the copier's. Apps do not silently redirect an old membership to the copy.

If no readable replica survives, the contents are lost.

### 11.9 Deleting a collection

The owner signs a statement with `deleted` set. No successor follows it. Devices stop showing the collection and keep local data read-only for 30 days, configurable; a device of the owner then destroys the data on the hub and keeps the roster, so that a device returning late learns the collection was deleted. There is no undo; a deleted collection can be copied into a new one while a replica remains (§11.8).

---

## 12. Availability and self-healing

### 12.1 Replicas

Every device keeps the full merged state of every collection it follows, its user's home, and the directory of each hub it is on. The hub is a meeting point, not the only copy.

### 12.2 Healing

After each full sync of a channel, a device compares its replica with the hub:

- **The owner's grants.** The last grant to each current member is restored if it is missing (§11.4).
- **The hub lacks something the device holds.** If the messages on the hub for a record, taken together, don't dominate (§10.7) the device's state of that record, the device re-publishes it. That covers a record with no message left at all, and one that was rolled back to an older message. The same goes for the roster, the current epoch's inventory fragments (§8.3) and the two statements that prove a collection dead (§9.4).
- **Messages the device can't decrypt** in a channel it follows indicate corruption or junk. The device logs and ignores them.
- **Pending pairing claims and releases.** Repair the exact persisted claim until expiry, cancellation or signed outcome. Admins finish a certified release after a crash; expiry does not cancel an already approved device. Never log or publish L while repairing (§6.3).
- **Authority dependencies.** Restore pinned root chains, registry facts and referenced proofs. Missing policy/certificate objects hold their dependents and never authorize pruning. A reset's fixed old-authority inventory limits what may affect the new generation (§6.8–6.9).

A device heals only where that is safe:

- **Only in a current channel:** the epoch of the roster's head, the latest verified home epoch, and the directory, where a device restores only its own user's entry. A channel whose `_roster` shows a later epoch is never refilled, except for that `_roster` record itself, which devices that have moved on keep complete (§11.6).
- **Not when the roster itself is missing.** If a collection's channel has no `_roster` record at all, the device first looks for a newer epoch in its home and its inbox and asks the owner for the current key (§11.4). It may restore the roster statements it holds, but an unanswered request is not proof that its old epoch is current. Data healing stays paused until it knows.

This makes vandalism by a hub participant a temporary nuisance, as long as a member device survives, while still honouring legitimate deletes (tombstones). A participant who keeps deleting can still keep a pairing from ever completing.

The hub can still withhold a newer roster statement. Signatures detect modification, not freshness, and silence proves nothing.

### 12.3 Deletions

A deleted record keeps its tombstone for good. Every rotation carries tombstones along like any other record, so a deleted record can never be brought back by replaying an old message, in this epoch or a later one, and a device may return after any length of time without special handling.

The cost is one small message per deleted record that never goes away. Dropping tombstones safely needs a point after which no device still holds the record, and v2 has no way to establish one (Q10).

### 12.4 Hub loss

Restoring a wiped hub at an already authorized descriptor is an explicit act of the user; moving it to a new mailbox additionally requires recovery-signed descriptor authorization (§5.3, §6.8), because silence can't establish that no newer roster exists. Devices then re-publish what they hold: home, and each collection with its roster. Restore home epoch metadata/device key boxes as well as encrypted home records. Paired devices retaining a home key can recover its contents; a new device still needs valid pairing/admission. Re-deliver missing current device grants, including offered lists not yet accepted. Everyone needs the new hub's credential, by hub invite (§13.1).

---

## 13. UX

The guiding rule: users never see keys, fingerprints or protocol terms unless they go looking.

### 13.1 Connecting a hub

Settings → **Sync → Add a hub** has two ordinary paths:

1. **Create a hub:** enter the dedicated IMAP account details, choose a hub name and a hub password, and keep the password with the setup details (§6.1). Existing local lists are uploaded under the creator's participant record. No management identity was required at installation.
2. **Join a hub:** open its invitation in the trusted native app. Verify the pinned authority/capability, choose a name/device label, and accept. The participant record and first-device admission are created by that flow (§6.1); the member chooses no password. Already a participant on this hub? Use a pairing link from an enrolled device to keep the same participant/lists. Opening a new-member invite again is not account recovery or device pairing.

The IMAP descriptor contains host, port, username and password/app-password credential. The stable hubID is `pub(MKs)`, not a hash of the mailbox address (§5.3). Root metadata authorizes descriptors; two credentials for the same authorized descriptor do not create different authorities. Manual credentials without pins/discovery material do not authorize taking over an existing hub.

**New-member invitations.** The admin unlocks MS and creates a manager-signed, purpose-limited invitation capability, intended label, token and expiry, carrying connection details/credential, HubK and root pins in a sensitive QR/link. The capability binds this hub/root, the invite verification key, new-participant purpose and limits. The first device proves both device keys and invitation possession over its exact registration/claim. The complete capability/claim/proof chain authorizes that first admission without a second admin tap; it never authorizes enrollment into an **existing** participant. The capability, its cancellation and what two claims on one link mean are in §10.8. Do not claim that a mutable “used” flag enforces one-time access against a hostile mailbox; duplicate uses are visible claims requiring admin resolution, not an arbitrary winning person.

The admin's private home records **Pending invite: Alice**, creation/expiry, credential hint and outcomes. Their other devices see the same pending invitation. Approval/completion receipts turn it into a joined participant; sharing two lists is a separate owner action. Invitation history and bearer secrets are different: do not put secrets into logs/directory/admin notifications.

**Pairing links.** The member creates them from an existing device (§6.3); the hub admin approves the resulting exact target device. They contain setup information and a bearer delegation, with private home access gated until approval. The app may use `liszto://` links or an HTTPS universal link with the sensitive payload in the fragment, never query/path parameters. Open only in the trusted app; fragments are not sent in HTTP requests, but page scripts, browser history and captured links can still expose them. No member-side comparison code is required; the captured-link trade-off is explicit in §6.3.

**Mail credentials are separate from the hub password.** A shared IMAP credential remains the simplest default and is passed on in invitations/pairing. Independent per-device credentials, where supported, allow provider-side removal of one device without reconnecting everyone. The app says which is being used; provider credential creation/revocation may require manual steps. Expiring/cancelling an invitation does not revoke a copied mail credential. Every device needs read, append and delete access; a read-only one is refused. Prefer credentials without send-mail permission where available, and a dedicated account rather than a personal inbox. The IMAP mapping remains §10.3.

### 13.2 Everyday flows

| Flow | What the user sees |
|---|---|
| Create, edit, delete | Nothing special. Instant locally; a subtle sync indicator when offline or behind. |
| Rename a list | Instant, from any of the owner's devices, offline too. |
| Add a device | On the enrolled phone: “Add device” → QR/link. Laptop opens it, names itself and shows “Waiting for hub approval”. Admin sees “New device from Alice: Laptop — Approve / Reject”; Approve asks for the hub password, or for Face ID where this device keeps it. Lists/settings then sync. No second Alice confirmation or comparison code. |
| Hub password | Chosen when the hub is created. Enter it on any device to administer the hub. Nobody who joins is asked for it. |
| Change the hub password | Enter the current one (or use Face ID on a device that keeps it), choose another. Devices and lists stay as they are. The app warns that an old copy of the capsule still opens with the old password. |
| A new phone | Sign in to the same Apple account; the keychain brings the keys, and everything syncs. No pairing and no approval. |
| A device broke or was replaced | Pair from a surviving enrolled device. Private device keys are not restored by cloning an app backup. |
| Lost every device | Creator: explicit hub recovery using the saved key. Ordinary member: rejoin as a new participant, ask owners to share lists again; private data without a readable replica is lost. |
| A device was stolen | People → person → Devices → Remove. Admin unlocks management; surviving member devices rekey home and owners rekey lists. Show remaining rekeys and mailbox credential steps separately. Previously downloaded data stays readable. |
| Share a list | "Share" → pick people from the hub's People, each as editor or read-only. Someone not on the hub yet first needs a hub invite. |
| Someone shared a list with you | "Bob shared House with you. Add it?", once, on all your devices. |
| Add someone to the hub | Admin: “Invite someone” → label/connection credential → hub password → QR/link. “Pending invite: Alice” appears on all admin devices; accepting creates her participant and first device. Then share selected lists. |
| On more than one hub | Lists appear together. Each hub has its own participant and device approval; pairing on one does not silently enroll on the others. |
| Who's on this hub | Hub → People → Alice → Phone, Laptop. Admission/revocation comes from certificates. Proven inviter/claimed names help recognition but do not prove a physical person. |
| Members | The list's sharing screen shows names and roles. The owner can add people from the hub's People, switch someone between editing and read-only, or remove them. It takes effect at once for the owner; the key change runs in the background and needs the network. |
| Sharing change started on another device | If it got far enough to finish: "Finishing a change started on Bob's iPhone: making Alice read-only in House." If not: "Bob's iPhone started making Alice read-only in House and didn't finish. Do it here, or discard it?" |
| Leave | "Leave list". Gone from all your devices. The owner sees "Alice left House" and confirms. |
| Removed by the owner | "House is no longer shared with you." Unsent edits are kept (app-specific: Liszto puts them in the Inbox). |
| A list can't sync any more | "House can't sync any more. Copy it to a new list?" The copy is yours; you share it again (§11.8). |
| Conflicts | App-specific. Tandem tells the app which fields have concurrent values. |
| An edit that may not have reached anyone | App-specific. After someone was removed, Tandem may hand the app an edit of yours from around that time, to keep or dismiss (§11.6). |
| Create a list on your phone | It appears on your approved laptop through the shared home, without invitations or further admin approval. The participant owns it; either device can act as owner. |
| Accept a shared list | Answer once on either device; the grow-only admission answer syncs through home. It appears on the other devices. Concurrent No/Leave still wins over Add (§11.2). |
| Private activity | “Activity” on any of your devices shows your invitations, enrollments and sharing actions, plus verified acceptance receipts. Hub admin status does not reveal another member's private history. |
| The hub password may have leaked | Make a new hub and copy the lists across; people join it. Changing the password doesn't undo a leak (§6.8). |
| Forgot the hub password | Set a new one from a device that keeps it behind Face ID. With none, management is lost; lists keep working. |

### 13.3 Errors

| Condition | Message |
|---|---|
| Hub unreachable | Offline indicator; everything keeps working locally. |
| Credential rejected | "Sync access was revoked or changed. Reconnect to keep syncing." Local data is untouched. |
| Sharing change without network | "Changing who a list is shared with needs a connection." |
| Collection dead | "House can't sync any more. Copy it to a new list?" Data is kept, read-only. |
| Expired pairing code | "This code has expired. Ask for a new one." |
| A credential that can only read | "This access can only read. Tandem also needs to write to the mailbox." |
| Hub data missing | Healing runs silently and is logged. |
| Device revoked | “This device was removed from this hub.” Keep local data/unsent work; send no current writes or key requests as that device. |
| Rekey incomplete | “Device removed. Waiting for list owners to secure 2 lists.” Show home/provider work separately; do not claim immediate erasure. |
| Conflicting root decisions | “Hub management cannot continue. Recover readable data into a new hub.” Freeze management; do not pick a branch. |
| Password changed concurrently | “Two password changes need finishing.” Unlock a current capsule and explicitly replace both (§6.7). |

---

## 14. What the hub learns

- That an account holds a folder of opaque messages, and their count, sizes and timing.
- Channel tags, and therefore how many collections, homes and inboxes are active and roughly how busy each is. The directory is one more tag; its contents are encrypted like everything else.
- **Who touches what.** Which connection appends, fetches and destroys which messages. With a credential per person or per device, that ties each credential to the channels it follows and the messages it writes, so the hub can tell who is in which channel and who wrote when, by credential rather than by name. With one shared credential it sees the same per IP address.
- **Patterns.** A participant's inbox tag stays stable on a hub; their home tag changes when its key/policy epoch changes. They differ between hubs (§6.5). A rotation shows as a burst of same-sized records under a new tag, followed by one inbox message per member: that links the new tag to the old one and gives away the size of the group.
- **Not** names, user or device IDs, collection names, record contents or roles.

Tandem doesn't defend against traffic analysis by the hub. Padding message sizes to fixed buckets would hide content length; v2 doesn't (Q9).

---

## 15. Versioning

This draft proposes incompatible **v2**: changed write inputs, participant/device authorization, random rotating home keys, registry controls, grants, directory and recovery. It must not be shipped under the implemented v1 version byte or labels. Envelope and mail header version fields are both 2; all authenticated contexts bind v2 and hubID. Control objects are encoded as §10.8 says.

A message in a v2 channel with a different or inconsistent version is junk to that client: ignored, never destroyed, and no reason to pause healing/GC. An unauthenticated header cannot change the version or disable the protocol. Unknown authenticated payload fields are preserved under §9.2; an incompatible crypto/authorization change requires another version. These rules do not make v1 devices interoperable with v2.

There is no migration from v1: nothing uses it, so a v2 client starts on a fresh hub.

## 16. Prior art and alternatives considered

| Work | What it does | What Tandem takes or leaves |
|---|---|---|
| **Kolab** groupware | Stores contacts, calendars and tasks as XML attachments in IMAP messages, with folders typed via IMAP METADATA (RFC 5464); "IMAP as a NoSQL store". | Proof that mail servers make a durable object store, and that ordinary mail clients will show such folders (harmless on a dedicated account). Tandem adds encryption, CRDT merge and per-collection keys, which Kolab leaves to server ACLs. |
| **Apple Notes over IMAP** (pre-iCloud Notes) | Notes kept as messages in a `Notes` IMAP folder. | Same storage idea, single user, no merge. |
| **Delta Chat / webxdc** | Apps exchange state updates as email messages between chat members; apps use CRDTs because updates arrive in different orders. | Confirms email works as a CRDT transport. Webxdc sends updates **between** mailboxes over SMTP; Tandem shares **one** mailbox, which avoids SMTP rate limits and gives a single change feed. |
| **secsync** (Serenity Notes) | Relays end-to-end encrypted CRDT snapshots and updates through a central service; the relay can't inject data; key exchange is out of scope. | Same trust model for the relay (here, the mailbox). Tandem specifies the key management and membership that secsync leaves out. |
| **Keybase** | Per-device keys in a user sigchain; paper keys for recovery; per-user keys rotated on device revocation; per-team keys rotated on removal, lazily by admins. | Tandem uses an independent hub recovery authority, password-unlocked manager and member-authorized device admissions merged as sets, with explicit revocation cuts (§6). It does not assume Keybase's server ordering. |
| **Matrix** | Room membership validated by authorization rules over an event DAG; Megolm group sessions. | Matrix merges concurrent authorization changes. Tandem has one owner participant per collection, several admitted signing devices, and freezes conflicting roster histories (§9.4). Megolm's ratchets give forward secrecy Tandem v2 lacks. |
| **MLS** (RFC 9420) | Standard group key agreement with forward secrecy and post-compromise security. | A candidate for a later version if forward secrecy matters. RFC 9420 §14 leaves it to the application to resolve competing commits; a single owner per collection would supply that order. Adoption and implementation cost are separate decisions. |
| **Keyhive / BeeKEM** (Ink & Switch) | Local-first capabilities and a decentralised TreeKEM variant for E2EE groups without a server. | A relevant research direction for a later group-key design; this draft does not depend on it. |
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
| Zone-wide CKShare | Roster, shared from the hub's directory. The list's creator is the owner; participants are writers or readers |
| Revoke share | Remove all other members (rotation) |
| "Alice added" | Device signature and participant certificate/source policy, then collection role; device visible in details |
| Field-level merge, `notesBase`/`notesSent` | Multi-value register: concurrent `notes` writes become siblings; Liszto resolves them with its existing conflict-text rule by writing a merged value covering both |
| Tags three-way merge, `tagsBase`/`tagsSent` | OR-set; no base bookkeeping needed |
| Delete wins | Sticky tombstone |
| Move between lists | Create in target + delete in source (unchanged) |
| `NSUbiquitousKeyValueStore` settings | `settings:liszto` in home |
| Extension direct-save | The extension appends one record message using the hub credential and this installation's admitted device keys and data keys from the shared Keychain group. It shares the app's device ID, so both draw their clock values from one HLC kept in the shared container, under a lock |
| FakeCloud tests | A FakeHub implementing §10.1, with the randomized convergence tests extended along the lines of the model in `model/` (Q14) |
| Push via CloudKit subscriptions | IMAP IDLE while running; on iOS, catch-up on launch and background refresh (no wake-up push without a push server) |

---

## 18. Open questions

| # | Question |
|---|---|
| Q3 | Folder size: how do Stalwart and Dovecot behave with ~100k messages in one folder (Tandem's expected upper bound per hub)? Fastmail held that many when measured over JMAP; over IMAP it is part of Q26. |
| Q5 | Should the app refuse hub passwords found in a list of common ones, rather than only warn? It would add a list to every SDK. And is 1 GiB of Argon2id memory safe on older phones with 4 GB of RAM, which may stop an app that takes that much? |
| Q9 | Padding message sizes to hide content length from the hub. |
| Q10 | Tombstones are kept for good (§12.3). Is that affordable at Liszto's deletion rate, or does v2 need a safe way to drop them? |
| Q14 | What checks the design, and what doesn't. A Go reference SDK and device engine, test vectors checked by a Swift SDK, Lean proofs of the merge, roster, clean-up and encoding rules, and a scenario harness live on the `testbed` branch; `conformance/` tests real cryptography, mail transport and a durable journal. `model/owner_model.py` is a small symbolic model of the roster rules and lags the draft in three places (its README says which). The testbed's `TESTING.md` says what is implemented and what isn't. |
| Q15 | Reintroduced keys per keychain, with a password-sealed management key and fixed certificate cutoffs (§6). Keychains that don't sync, such as Android's, lose the new-phone convenience; is a platform backup worth defining there? Selected model/crypto tests pass; the complete architecture still requires independent review. A revoked owner device can still kill an old roster by forking it (§9.4). |
| Q16 | A roster gains one complete statement per change and is copied at every rotation, and an inventory keeps entries for writes no rotating device has seen (§8.3). Is a signed checkpoint needed for long-lived collections? |
| Q20 | Two devices of one owner changing a collection's sharing in the same moment kill it (§11.7). The note in home makes that unlikely. Measure how often this occurs in practice; the note narrows the race but cannot eliminate it. |
| Q22 | A device keeps an epoch's superseded writes until the next epoch's inventory is fixed (§9.3). A collection that is never rotated keeps them for good. Is a narrower rule safe, for instance dropping at once what the owner's own writes superseded? |
| Q25 | A guest who can only look. A read-only credential (Fastmail can make an app password read-only) can't run today's protocol, because even a reader writes. If the owner did the writing (scanning the guest's code and admitting them), a guest could read a list without being able to destroy anything on the hub. |
| Q26 | A whole hub over IMAP on Fastmail. Measured so far: 40 connections on one app password all get live updates; an append reaches the others in about half a second; 100 appends take 21 s with `MULTIAPPEND`; `QRESYNC` reports removals correctly. Not yet run: every scenario with several devices over IMAP only, the time a key change takes when a whole collection is uploaded one `APPEND` at a time, and a full resync of 100,000 messages (64 s when it was measured over JMAP). |
| Q29 | Exercise the complete UX over real IMAP with durable storage: automatic future-list sync; original device removed while child keeps access; stale/forking owner; absent admin/member/owner; pending pairing across home rekey/root reset; kill/lost-ack at every export; concurrent password changes; recovery with late old-manager facts. Ensure no admin-accessible box decrypts another member's private home. |
| Q30 | Which mail providers work. Tested: Fastmail, Stalwart and Gmail (§10.3). Known not to: Outlook.com personal accounts, which accept only OAuth since September 2024. Not yet measured: iCloud Mail, Yahoo, GMX, mailbox.org and others, and Gmail's connection limit. On Gmail, live updates take about 30 s, or about 12 s and 8.5 s when IDLE is restarted every 5 s and 2 s: the default is 3 s; does Gmail tolerate it for hours on end? `probes/imap_probe.py` checks a provider: required extensions, live-update latency, bulk appends, whether a destroyed message survives elsewhere, and how many connections it allows; `--compare` tries other ways to get a change pushed. |

**Evidence for this revision.** `model/management_model.py` has 43 passing symbolic tests of selected registry/root/password/pairing rules, including randomized delivery. `conformance/test_pairing_gate.py` has 11 passing real-crypto tests of split-secret pairing, exact target/certificate binding, future-list sync through the shared home and home-key exclusion. They do not implement the full enrollment controller, home/collection policy lifecycle or v2 wire format, and do not establish Swift interoperability. The existing v1 model/SDK/Lean evidence must not be represented as proof of these new authority rules. Detailed rationale and test boundaries are in `DEVICE-MANAGEMENT.md`.
