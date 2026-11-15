# Proposal: blobs (files in the mailbox)

Status: **proposal**, 2026-11-15. Nothing here is implemented or measured. It extends `DESIGN.md`
(v2) and removes its non-goal "Large binary attachments" (§1.2). Section numbers with § refer to
`DESIGN.md`. Decisions Jesper made on 2026-11-15 are marked **Decided**; numbers that still need a
measurement are in §12.

## 1. Summary

A **blob** is an immutable sequence of bytes: a photo, a PDF, any file. It is cut into **chunks**,
each stored as one encrypted message in a second folder of the hub. What changes over time is not
the blob but a small **manifest** that points at it, and the manifest is the value of an ordinary
record field. Renaming, replacing, deleting, sharing, concurrent edits, conflicts and revocation are
therefore what records already do (§9); the only new protocol surface is storing and finding
immutable chunks.

- **Decided:** one generic layer. Attachments on records come first (Liszto), a shared-folder app
  later; both are records whose fields hold manifests.
- **Decided:** the wire format allows a chunk to be reused by a later version of a file (delta
  sync). The first implementation does not reuse chunks: an edited file is uploaded whole.

## 2. What it gives and what it costs

| Property | How |
|---|---|
| Works offline | The manifest write is made locally when the file is attached; chunks follow when the hub is reachable (§6). |
| Real time | A manifest reaches the feed only when its blob is complete on the hub, so its arrival is the event other devices act on. Nothing polls. |
| Resumable | Chunk encryption is deterministic. An interrupted upload continues by listing the folder and appending what is missing; no per-chunk state is kept. |
| One content for everyone | The signed manifest lists the hash of every encrypted chunk, so not even a member can show two devices different bytes. |
| No re-upload on a key change | Chunk keys are in the manifest, and the manifest moves to the new epoch with its record (§9 below). |
| Self-healing | Any device that still holds the file re-creates a destroyed chunk byte for byte (§8). |

Costs, accepted on purpose:

- **Two ways to carry a chunk.** Where the server offers `BINARY` (RFC 3516) a chunk is stored as
  raw bytes. Elsewhere, Gmail included, it is base64 text like every other message (§10.4), about
  37% larger: a 1 MiB chunk is a 1.4 MiB message and counts as that against quota and bandwidth.
  Readers handle both forms (§4.2).
- **No delta sync in the first implementation.** See the second decision above.
- **Weaker durability than records.** Every device holds every record (§12.1); not every device
  holds every file. A blob destroyed on the hub survives only if some device kept it.
- **A larger message limit in the blob folder.** §10.6's limit of 1 MiB stays for the feed folder.
- **The hub learns file sizes** and which channel a file belongs to, as it does for records (§14).
- **Provider ceilings.** Google documents 500 MB of IMAP upload and 2500 MB of download per day and
  account for Workspace; a hub is one account, so all its participants share that. This design
  suits documents and photos, not a media library.

## 3. Terms

| Term | Meaning |
|---|---|
| **Blob** | An immutable byte sequence, identified by its manifest. |
| **Chunk** | A piece of a blob, at most 4 MiB of plaintext, stored as one message. |
| **Address** | `H(envelope)` of a chunk's envelope: the identity every hub message already has (§10.4). |
| **Manifest** | The JSON value that describes a blob: its size, hash and chunks, or its bytes inline. |
| **Blob folder** | The hub folder `Tandem-Blobs`, holding chunks and nothing else. |
| **Scope** | The collection, home or directory whose record holds a manifest. |

## 4. Chunks

### 4.1 Encryption

```
ck       = the chunk's 32-byte key, from the manifest (§5)
aad      = "TNDM" || u8(2) || u8(4) || hubID
ct       = ChaCha20-Poly1305-Seal(ck, 12 zero bytes, plaintext, aad)
envelope = "TNDM" || u8(2) || u8(4) || ct          # kind 4 = blob chunk; no salt field
address  = H(envelope)
```

A chunk holds between 1 byte and 4 MiB (4,194,304 bytes) of plaintext. A reader rejects anything
else. The key is not derived from a channel key, so a chunk outlives the epoch it was written in.

**The writer chooses `ck` so that it can never seal two different plaintexts:**

```
ck = KDF(r, H(plaintext), "tandem/v2/blob-chunk", 32)     # KDF(ikm, salt, info, L) of §5.2
```

where `r` is 32 random bytes, fresh for every upload and kept in the upload journal (§6). The zero
nonce is safe because the key is bound to the content. A reader cannot check how a key was made and
does not need to: a writer who reuses a key harms only the secrecy of their own content.

Encryption is therefore deterministic: the same `r` and plaintext give the same envelope and the
same address. Three things rest on that. A retry after a lost answer finds its chunk instead of
storing a second one. A resumed upload needs no record of which chunks went out. And a device that
holds a file can restore a destroyed chunk from the plaintext and the manifest alone (§8), without
having kept any ciphertext.

### 4.2 The chunk message

The headers of §10.4 and one more, appended to the blob folder:

```
Tandem-Version: 2
Tandem-Channel: <tag of the scope's current channel when the chunk was appended>
Tandem-Blob: <b32 of the address, 52 characters>
```

Both are claims the hub or any participant could falsify. `Tandem-Blob` lets a device find a chunk
without fetching bodies; a body whose hash differs from the claim is junk and is ignored.
`Tandem-Channel` only limits clean-up (§7); it is not in the AAD and never authorizes anything.

The body carries the envelope in one of two forms. A chunk's identity is its address, so the form
is a property of the message, not of the chunk, and one hub may hold both.

**Binary**, where the server advertises `BINARY` (RFC 3516):

```
Content-Type: application/octet-stream
Content-Transfer-Encoding: binary

<the envelope, as it is>
```

It is appended as a binary literal (`APPEND Tandem-Blobs (\Seen) ~{n}`) and fetched with
`UID FETCH <uid> BINARY.PEEK[1]`, which returns the content with its transfer encoding undone.

**Text**, everywhere else: exactly §10.4, `text/plain` and `7bit` with the base64 of the envelope
wrapped at 76 characters, fetched as §10.3 says.

**Writing.** A writer uses the binary form if the server advertises `BINARY` and the hub's setting
allows it (on by default), otherwise the text form. Every device on a hub talks to the same server,
so they agree without negotiating.

**Reading.** A reader accepts both, whatever it would write. It undoes the transfer encoding
(§10.4 step 1). If the content type is `application/octet-stream`, the result is the envelope. If
it is `text/plain`, steps 2 and 3 of §10.4 follow. A server that hands back a binary body as base64
is covered by step 1. Whatever arrives must hash to the address, so a server that damages a binary
body produces a chunk that reads as missing, never wrong content; healing then restores it (§8). A
device that sees its own binary appends come back damaged switches that hub to the text form and
says so.

The binary form is conditional on B2: it has not been tried on any provider.

### 4.3 The blob folder

`Tandem-Blobs` sits beside `Tandem`, with the same namespace-prefix rule (§10.3). It is a second
instance of the abstract store (§10.1) with two additions and one omission:

- `append` takes the address as well as the channel tag;
- `changes` returns, for each added message, its channel tag, its claimed address and the time the
  server stored it (IMAP `INTERNALDATE`, JMAP `receivedAt`);
- `watch` is not used. No device idles on the blob folder.

A device keeps a **chunk index**: message ID, claimed address, tag and stored time for every message
in the folder, built by `changes` (headers only, paged like the feed). It refreshes the index only
when it has a reason: before fetching a chunk, before publishing a manifest, and in the hygiene pass
if it holds blob content or has chunks to collect. A device with no interest in files never opens
the folder. A `UIDVALIDITY` change rebuilds the index.

**Why a second folder.** The Swift and Go bindings fetch every body in the feed folder with
`changes`; a first sync must not download every file on the hub. Five hundred chunk appends in the
feed folder would wake every idling device five hundred times. And the feed's bound of about 100,000
messages (Q3) stays about records.

**Connections.** Chunks move on a second connection that exists only while there is something to
transfer, so the feed connection stays in IDLE and edits keep arriving during a long upload. Where
the server refuses a second connection (§10.3 lists the caps), the device uses its one connection
and transfers one chunk between returns to IDLE. That is a per-hub setting with the automatic
choice as default. `MULTIAPPEND` and `LITERAL+` are used where offered, as for the feed.

## 5. Manifests

A manifest is the value of an `assign` write to a field whose name starts with `_blob:` followed by
a name the app chooses (`_blob:file`, `_blob:photo`). It is JSON under the rules of §10.5 (one
reading, integers only, canonical `b64u`), in one of two forms:

```json
{ "blob": 1, "size": 3145728, "hash": "<b64u SHA-256 of the content>", "chunks": "<b64u>" }
{ "blob": 1, "size": 11,      "hash": "<b64u SHA-256 of the content>", "data":   "<b64u content>" }
```

- `blob` is the manifest version, 1.
- `size` is the content length in bytes, at most 2^53.
- `chunks` decodes to `n ≥ 1` entries of 68 bytes each: `address (32) || ck (32) || u32(length)`, in
  content order. Every length is between 1 and 4,194,304 and the lengths add up to `size`.
- `data` is the content itself, for small blobs. Its length is `size`. A writer uses it for content
  of at most 16 KiB by default (configurable); a reader accepts whatever fits a message. An empty
  file is `"size": 0` with empty `data`.
- Exactly one of `chunks` and `data` is present. Unknown keys are ignored.

**Validity.** The write is an ordinary write: whether it is valid never depends on its value
(§9.3), so clients of different versions cannot diverge over it. A value that is not a manifest by
the rules above, or has a `blob` version the client does not know, makes the field **unreadable** on
that client and nothing more. `add` and `remove` on a `_blob:` field have no meaning to this layer.

**What the manifest pins.** It is signed like any write (§5.5), and it names every chunk by the hash
of its ciphertext. A device accepts a chunk only if its envelope hashes to the listed address and
opens under the listed key, so every device sees the bytes the author signed or nothing. A device
that has assembled the whole content also checks `hash` and treats the blob as unreadable if it
differs. `hash` lets an app see that two versions are the same file without downloading either.

**What is not in it.** A file's name, media type and thumbnail are ordinary fields of the same
record. A rename then merges with a concurrent replacement of the content, and a thumbnail of a few
KiB arrives with the record, in real time, before anyone fetches a chunk.

**Sizes.** An entry is 68 bytes, so a manifest costs about 70 KB per GiB at 1 MiB chunks. A record
message stays under 64 KiB (§10.6, SHOULD) up to about 390 chunks and under 1 MiB (MUST) up to about
6,300. With 4 MiB chunks that is 24 GiB, far past what a mailbox should hold.

**Concurrent replacement** needs nothing new: two manifests written without seeing each other are
siblings of a multi-value register (§9.2). The app shows both versions and resolves by assigning one
of them again.

## 6. Writing a blob

1. **Stage.** The device copies the file into its blob store (a clone where the file system has
   them), so later changes to the original do not matter. It picks `r`.
2. **Describe.** One pass over the staged copy: cut it into chunks (1 MiB by default, a per-hub
   setting; the last may be shorter), compute each `ck`, envelope and address, and `hash`. That gives
   the manifest.
3. **Write.** The device makes the `assign` write with the manifest as its value, signs it and stores
   it with a journal entry (`r`, the staged copy, the write). The file now shows on this device. All
   of this works offline.
4. **Upload.** The device refreshes the chunk index and appends every chunk whose address is not on
   the hub, sealing it again from the staged copy. After a crash or a lost connection it starts this
   step over; determinism makes that a resume.
5. **Commit.** It refreshes the index once more, checks that every address is there, and only then
   publishes the write (§10.7). **A manifest on the feed means its blob was complete on the hub.**

Until step 5 the write is **withheld**: it is in the device's local state but left out of every
record message the device appends, and it does not count when the device asks whether the hub lacks
something it holds (§12.2). Other writes to the same record publish as usual.

If the user replaces or removes the file before step 5, or cancels, the device drops the write and
destroys the chunks it uploaded for it. Its dot is spent; that is harmless (§9.2).

If the scope's epoch changes while a write is withheld, the write is published by the rules for late
writes (§11.6). If its author was removed, it is never published, and its chunks are orphans (§7).

A full hub (`OVERQUOTA`, RFC 9208) or a chunk over the server's `APPENDLIMIT` stops the upload with
a clear state the app can show. The journal entry stays, and the upload resumes when asked.

## 7. Collecting chunks

Chunks are never dominated the way record messages are (§10.7). In the hygiene pass that follows a
full sync of a scope, a device MAY destroy a chunk message when all of this holds:

1. its `Tandem-Channel` is the tag of a channel of that scope which the device knows (any epoch);
2. the device holds a valid write in that scope whose manifest lists its address, so the device can
   account for the chunk;
3. no manifest lists its address among the scope's kept writes, its held writes (§8.3) and the
   device's own withheld writes.

Condition 1 keeps one scope's clean-up from touching another's chunks, whatever a manifest claims.
Condition 2 means a device never destroys what it cannot explain, as with messages it has not
merged.

**Copies.** Several messages with one address come from an append repeated after a lost answer. Only
the device that appended them removes the extras, keeping one (§10.7).

**Orphans.** A chunk that no write ever listed comes from an upload that never committed. Its
uploader destroys it (§6). Another device MAY destroy it when conditions 1 and 3 hold, no write the
device holds has ever listed it, and it was stored more than the **orphan grace** before the newest
message in the blob folder. Both times are the server's, so no device's clock is involved. The
default is 7 days, a per-hub setting. This is a safety net for a device that died mid-upload, not a
mechanism anything waits on. A device that holds a `_blob:` write it cannot read in a scope collects
no orphans there: the chunks may belong to a manifest version it does not know.

**What can go wrong, and what repairs it.** Clean-up decides from what one device has seen. Two
rare histories leave a kept manifest with a destroyed chunk: a device reuses or re-assigns a blob at
the moment another collects it, or an old write becomes current again in a new epoch (§9.3) after
its chunks were collected. In both the record is intact and the file reads as unavailable until a
device that holds it restores it (§8). The commit check of §6 covers the uploader's own case.

## 8. Reading, keeping and healing

**States.** For each manifest a device reports one of: **here** (inline, or downloaded and
verified), **remote** (not fetched), **downloading** with progress, **unavailable** (a listed chunk
was not on the hub at the last index refresh) and **unreadable** (§5). Unavailable is not an error
of the record and is distinct from remote.

**Fetching.** The device refreshes the chunk index, finds a message claiming each address, fetches
it, checks the hash, opens it and writes the plaintext at its offset. A wrong body is ignored and
another message claiming the same address is tried. Download resumes, and content can be read from
any offset, chunk by chunk. An unavailable blob is tried again at the next index refresh or when
the app asks.

**Keeping.** Whether a device fetches on demand or keeps every file of a scope is a setting per
scope and device, with an upper size for what is fetched unasked. The blob store has a size limit;
what is evicted becomes remote again. A staged copy is kept until its write is published.

**Healing** (§12.2) gains one rule. After an index refresh in the hygiene pass, for every kept
manifest in a current channel whose whole content the device holds: each listed address missing
from the hub is sealed again with the listed key and appended. The envelope must hash to the
address; if it does not, the local copy is damaged and is dropped. A device that holds no content
does nothing, which is the durability cost named in §2.

## 9. Keys, membership and removal

- **A key change uploads no chunk.** The manifest write is carried into the new epoch like any other
  write, with its chunk keys inside.
- **A removed member** can still open the chunks of every manifest they could read, including files
  they had not fetched, for as long as they hold the hub credential. That is §3.3's rule: they read
  what was written under keys they held. Files attached after the removal have new keys in a channel
  they cannot read.
- **A reader** can fetch and open blobs and cannot write manifests. Like anyone with the hub
  credential they can still append junk and destroy messages (§3.1).
- **Deleting a record or a collection** makes its chunks collectable by §7. A dead collection
  (§9.4) stops collecting and keeps its blobs readable.
- **Moving or copying to another scope** uploads the blob again under new keys. A blob belongs to
  one scope.
- **Quota.** Where the server offers `QUOTA` (RFC 9208), the binding reports used and total storage;
  otherwise it reports that the quota is unknown, never zero.

## 10. Reusing chunks (reserved; not in the first implementation)

Nothing in the wire format ties a chunk to a blob or to a position in it. A later writer may
therefore cut files where their content suggests (content-defined chunking), keep a local table
from plaintext hash to manifest entry, and list an existing entry in a new manifest instead of
uploading the chunk again. To leave that open, a reader MUST already accept from day one:

- chunks of any mix of lengths within the limits of §4.1;
- the same address more than once in a manifest;
- an address listed by several manifests of one scope (§7 condition 3 already counts them all).

A writer may reuse an entry only from a kept manifest of the same scope.

## 11. Prior art

| Work | What it does | What this takes or leaves |
|---|---|---|
| **IMAP Storage Filesystem** (2007), **GmailFS** (2004), **EMFS** (2016), **ePFS** (2007) | Mount or sync files as mail messages, with file metadata and state inside the messages. | They rewrite messages to change anything and leave concurrent edits, interrupted uploads and conflicts open. Here blobs never change and all mutable state is in CRDT records. |
| **Usenet binaries**: yEnc, NZB, PAR2 | Files posted as many articles below about 1 MB, an index file listing every segment's message ID and size, and parity files to repair lost segments. | The same shape after decades of use: segments, a manifest of segment IDs, a repair path. Healing from replicas stands in for parity. yEnc's 1–2% overhead comes from an encoding mail servers don't know; IMAP's `BINARY` gets to zero within the standards, with base64 where it is missing. |
| **Matrix** encrypted attachments | A single-use key and a SHA-256 of the ciphertext travel in the encrypted event; the ciphertext sits in a media store. | The manifest is the same idea per chunk. Matrix's AES-CTR is unauthenticated (MSC4016 proposes fixing that); chunks here are AEAD. |
| **restic**, **borg**, **Tahoe-LAFS**, **Perkeep** | Encrypted chunks addressed by hash; clean-up by reachability, guarded against uploads in progress. | Addresses are hashes of ciphertext; clean-up is conservative and repaired by healing instead of a repository lock. |
| **Kolab** (§16) | Files as objects in IMAP folders, in production. | Confirms mail servers hold such data durably. |

## 12. Open questions

| # | Question |
|---|---|
| B1 | **Chunk size.** 1 MiB is a guess. Measure appends and fetches of 256 KiB, 1 MiB and 4 MiB chunks on Fastmail, Gmail and Stalwart with `probes/imap_probe.py`, with and without `MULTIAPPEND`/`LITERAL+`, and on a second connection while the first idles. |
| B2 | **`BINARY`.** §4.2 uses it where offered, untested. Gmail does not advertise it (probed 2026-11-13). Do Fastmail and Stalwart, do they return a binary body byte for byte, with `MULTIAPPEND` and `LITERAL+` too, and do they count its raw size against quota? If a provider fails this, its hubs use the text form. |
| B3 | **Provider limits and terms.** Gmail's append limit was probed at about 35 MB and its daily IMAP volumes are documented for Workspace only. Fastmail's limits for large folders of large messages are unconfirmed, and no provider's terms were checked for use as file storage. |
| B4 | **Orphan grace.** Is 7 days right, and should the uploader's app show an upload that has waited that long? |
| B5 | **Copying a dead collection** (§11.8) uploads every blob again. Letting the copy list the old chunks would avoid that but nothing would ever collect them. |
| B6 | **Earlier versions.** Keeping the chunks of superseded manifests for a time would give version history. Not designed. |
| B7 | **Padding** the last chunk to hide exact sizes (Q9). |
| B8 | **Waiting for an unavailable blob.** A device could idle on the blob folder while it wants a missing chunk. Today it retries at the next refresh. |
| B9 | **Proofs.** The clean-up rule of §7 and the withheld write of §6 should get the treatment the mailbox clean-up proofs give §10.7. |
| B10 | **The browser binding** (§10.2): a second Mailbox and the same message format; JMAP could fetch byte ranges. Not designed. |
