# Proposal: blobs (files in the mailbox)

Status: **proposal**, 2026-11-15. Nothing here is implemented. How chunks travel was measured on
Fastmail and Stalwart (section 12); Gmail was not. It extends `DESIGN.md` (v2) and removes its
non-goal "Large binary attachments" (§1.2). References with § are to `DESIGN.md`; this document's
own parts are called sections. Decisions Jesper made on 2026-11-15 are marked **Decided**; what is
still open is in section 13.

Revised the same day after a review found five faults: a pending replacement could destroy the
published file (section 6), a false header could suppress healing (section 4.3), two healers could
destroy each other's copy (section 7), binary reads were decoded twice (section 4.2) and the orphan
grace trusted a time a client can set (section 7). A second review found six more, fixed in sections
4.3, 5, 7, 8 and 9: clean-up and healing now share one definition of a live manifest, and a device
that cannot know what a chunk is needed for collects nothing. A third found three, fixed in sections
4.2, 7 and 8: a version stays protected until its replacement is on the feed, healing never discards
a good local file, and a refused binary append falls back to text.

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
| Works offline | The manifest write is signed locally when the file is attached; chunks follow when the hub is reachable (section 6). |
| Real time | A manifest reaches the feed only when its blob is complete on the hub, so its arrival is the event other devices act on. Nothing polls. |
| Resumable | Chunk encryption is deterministic. An interrupted upload continues by listing the folder and appending what is missing; no per-chunk state is kept. |
| One content for everyone | The signed manifest lists the hash of every encrypted chunk, so not even a member can show two devices different bytes. |
| No re-upload on a key change | Chunk keys are in the manifest, and the manifest moves to the new epoch with its record (section 9). |
| Self-healing | Any device that still holds the file re-creates a destroyed chunk byte for byte (section 8). |

Costs, accepted on purpose:

- **Two ways to carry a chunk.** Where the server offers `BINARY` (RFC 3516) a chunk travels as raw
  bytes. Elsewhere, Gmail included, it is base64 text like every other message (§10.4), about 37%
  larger: a 1 MiB chunk is a 1.4 MiB message. Readers handle both forms (section 4.2). `BINARY` saves
  transfer but not always storage: Fastmail keeps a binary chunk as base64 and charges quota for
  that; Stalwart keeps it raw (section 12).
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
ck       = the chunk's 32-byte key, from the manifest (section 5)
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

where `r` is 32 random bytes, fresh for every upload and kept in the upload journal (section 6). The zero
nonce is safe because the key is bound to the content. A reader cannot check how a key was made and
does not need to: a writer who reuses a key harms only the secrecy of their own content.

Encryption is therefore deterministic: the same `r` and plaintext give the same envelope and the
same address. Three things rest on that. A retry after a lost answer finds its chunk instead of
storing a second one. A resumed upload needs no record of which chunks went out. And a device that
holds a file can restore a destroyed chunk from the plaintext and the manifest alone (section 8), without
having kept any ciphertext.

### 4.2 The chunk message

The headers of §10.4 and one more, appended to the blob folder:

```
Tandem-Version: 2
Tandem-Channel: <tag of the scope's current channel when the chunk was appended>
Tandem-Blob: <b32 of the address, 52 characters>
```

Both are claims the hub or any participant could falsify. `Tandem-Blob` lets a device find a chunk
without fetching bodies; a body whose hash differs from the claim is junk and is ignored, and section 4.3
says what a claim may be relied on for.
`Tandem-Channel` only limits clean-up (section 7); it is not in the AAD and never authorizes anything.

The body carries the envelope in one of two forms. A chunk's identity is its address, so the form
is a property of the message, not of the chunk, and one hub may hold both.

**Binary**, where the server advertises `BINARY` (RFC 3516):

```
Content-Type: application/octet-stream
Content-Transfer-Encoding: binary

<the envelope, as it is>
```

It is appended as a binary literal (`APPEND Tandem-Blobs (\Seen) ~{n}`). The server may keep it in
another transfer encoding: Fastmail stores it as base64 and reports `Content-Transfer-Encoding:
base64` from then on (section 12).

**Text**, everywhere else: exactly §10.4, `text/plain` and `7bit` with the base64 of the envelope
wrapped at 76 characters, fetched as §10.3 says.

**Writing.** A writer uses the binary form if the server advertises `BINARY` and the hub's setting
allows it (on by default), otherwise the text form. Every device on a hub talks to the same server,
so they agree without negotiating.

A server that advertises `BINARY` may still refuse to store binary content in a mailbox, and
answers such an `APPEND` with `NO [UNKNOWN-CTE]` (RFC 3516 §4.4). The device then changes that hub
to the text form, says so, and goes on with the upload: the refused chunk, or the whole refused
`MULTIAPPEND` batch, is sent again as text. Chunks already stored in the binary form stay, since a
chunk's address does not depend on its form. The hub stays on the text form until the user turns
the binary form on again.

**Reading.** A reader accepts both forms, whatever it would write. There are two ways to fetch, and
the transfer encoding is undone exactly once, by the server or by the reader:

- Where the server advertises `BINARY`: `UID FETCH <uid> (BINARY.PEEK[1] BODY.PEEK[HEADER.FIELDS
  (Content-Type)])`. The server has undone the transfer encoding (RFC 3516 section 4.2), whatever it
  stored. The reader does not look at `Content-Transfer-Encoding` and skips step 1 of §10.4.
- Elsewhere, and where the server refuses a `BINARY.PEEK` with `UNKNOWN-CTE`: `BODY.PEEK[TEXT]` with
  the `Content-Type` and `Content-Transfer-Encoding` header fields, as §10.3 fetches. The reader
  undoes the transfer encoding that header names (step 1 of §10.4).

Either way the reader now has the body's content. If the content type is
`application/octet-stream`, that content is the envelope. If it is `text/plain`, steps 2 and 3 of
§10.4 turn it into the envelope. Anything else is not a chunk.

Whatever arrives must hash to the address, so a server that damages a binary body produces a chunk
that reads as missing, never wrong content; healing then restores it (section 8). A device that sees its
own binary appends come back damaged switches that hub to the text form and says so. On Fastmail
and Stalwart every binary chunk came back byte for byte (section 12).

### 4.3 The blob folder

`Tandem-Blobs` sits beside `Tandem`, with the same namespace-prefix rule (§10.3). It is a second
instance of the abstract store (§10.1) with three additions and one omission:

- `append` takes the address as well as the channel tag;
- `changes` returns, for each added message, its channel tag and its claimed address;
- message IDs have an order that is the same for every device, in which a later append is never
  lower (IMAP: the UID);
- `watch` is not used. No device idles on the blob folder.

A device keeps a **chunk index**: for every message in the folder its ID, claimed address and tag,
built by `changes` (headers only, paged like the feed), and two things only this device knows: when
it first saw the message, by its own clock, and whether it has verified it. It refreshes the index
only when it has a reason: before fetching a chunk, before publishing a manifest, and in the hygiene
pass if it holds blob content or has chunks to collect. A device with no interest in files never
opens the folder. A `UIDVALIDITY` change rebuilds the index; first-seen times and verification start
over with it.

**Claimed and verified.** A header is a claim. A device has **verified** a message once it has had
the body and seen it hash to the claimed address: it appended the message and the server answered
with its ID, or it fetched it. A message whose body hashes to something else is **rejected** on that
device for good, since a message never changes under one ID. A rejected message counts as absent
everywhere in this document. It is not destroyed on that account, like any message a device cannot
read (§12.2).

An address **is on the hub**, for a device, when the device has verified a message for it. An
address with nothing but unverified claims is unknown: before the device relies on it either way, to
skip an upload, to commit, to leave a chunk unhealed or to destroy a copy, it fetches a claim and
settles it. That costs one fetch of the chunk, once per message and device, and a device that
uploaded or downloaded the file has already paid it. Without this rule anyone with the hub
credential could destroy a chunk, append junk under its address, and stop every device from
restoring it.

**Why a second folder.** The Swift and Go bindings fetch every body in the feed folder with
`changes`; a first sync must not download every file on the hub. Five hundred chunk appends in the
feed folder would wake every idling device five hundred times. And the feed's bound of about 100,000
messages (Q3) stays about records.

**Connections.** Chunks move on a second connection that exists only while there is something to
transfer, so the feed connection stays in IDLE and edits keep arriving during a long upload. Where
the server refuses a second connection (§10.3 lists the caps), the device uses its one connection
and transfers one chunk between returns to IDLE. That is a per-hub setting with the automatic
choice as default.

**Batching.** An implementation MUST send chunks in batches of a few MiB, by the first of these the
server offers, in either form:

1. one `MULTIAPPEND` per batch (with `LITERAL+` its literals are sent without waiting);
2. with `LITERAL+` alone, the batch's `APPEND` commands sent without waiting for each answer;
3. otherwise one `APPEND` at a time.

Several chunks are fetched with one command. On Fastmail an append answered alone costs half a
second whatever its size, which matters more than the chunk size, and `MULTIAPPEND` was the steady
one: text chunks of 1 MiB went up at 3.3 to 5.7 MiB per second in a `MULTIAPPEND` and at 0.7 to 1.6
pipelined (section 12). A `MULTIAPPEND` stores all of its messages or none, so a lost connection
costs one batch, which the next attempt sends again.

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
- `data` is the content itself, for small blobs. Its length is `size`; a value where it is not is
  no manifest. A writer uses it for content
  of at most 16 KiB by default (configurable); a reader accepts whatever fits a message. An empty
  file is `"size": 0` with empty `data`.
- Exactly one of `chunks` and `data` is present. Unknown keys are ignored.

**Validity.** The write is an ordinary write: whether it is valid never depends on its value
(§9.3), so clients of different versions cannot diverge over it. A value that is not a manifest by
the rules above, or has a `blob` version the client does not know, makes the field **unreadable** on
that client. A manifest of an unknown version, a JSON object whose `blob` is an integer other than
1, has one more effect: it stops that client's clean-up in the scope (section 7). Every later
version of the manifest therefore stays a JSON object with an integer `blob`. `add` and `remove` on
a `_blob:` field have no meaning to this layer.

**What the manifest pins.** It is signed like any write (§5.5), and it names every chunk by the hash
of its ciphertext. A device accepts a chunk only if its envelope hashes to the listed address,
opens under the listed key and holds exactly as many bytes of plaintext as its entry says. The last
check is its own: a manifest whose lengths are wrong for its chunks can pass authentication, and
even the whole-file hash for a reader that writes chunks one after another, while a reader that
places each chunk at the offset the lengths give would produce other bytes. If a chunk's length
differs from its entry, the blob is unreadable, and no byte of that chunk is written or shown. So
every device sees the bytes the author signed or nothing. A device that has assembled the whole
content also checks `hash` and treats the blob as unreadable if it differs. `hash` lets an app see
that two versions are the same file without downloading either.

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
3. **Sign.** The device makes the `assign` write with the manifest as its value, with the context it
   would have if it were published now (§9.2), and signs it. It stores the write in its **upload
   journal** with `r` and the staged copy. The write is **pending**: it is not merged into the
   device's replica. The app is shown the pending file on top of the record's state. All of this
   works offline.
4. **Upload.** The device refreshes the chunk index and appends every chunk whose address is not on
   the hub (section 4.3), sealing it again from the staged copy. After a crash or a lost connection it
   starts this step over; determinism makes that a resume.
5. **Commit.** It refreshes the index once more and checks that every address is on the hub, each
   through a message it has verified. Only then does it merge the write into its replica, and from
   there the write is published like any other (§10.7). **A manifest on the feed means its blob was
   complete on the hub.** The version it replaces stays protected until the new write is on the
   feed (section 7).

**A pending write does not exist yet.** Until step 5 the device acts, for every purpose of the
protocol, as if the write had not been made. Its replica still holds what the field held before, and
that is what it publishes, heals (§12.2), counts as kept when it collects chunks (section 7), lists in the
inventory when it starts an epoch (§8.3) and builds the contexts of other writes from. If the pending
write were in the replica it would supersede the published version there, and the device could
destroy that version's chunks, or leave it out of a new epoch, while every other device still shows
it. A pending write has the standing of a write from another device that has not arrived: at commit
it is merged like one. If someone changed the field meanwhile, the two are siblings (§9.2), which is
the truth, since the user attached the file without having seen that change.

The device drops a pending write and destroys the chunks it uploaded for it when the user replaces
or removes the pending file, deletes the record or cancels, and when the record is deleted by the
time of the commit. Its dot is spent; that is harmless (§9.2).

If the scope's epoch changes while a write is pending, it is published at commit by the rules for
late writes (§11.6). If its author was removed, it is never published, and its chunks are orphans
(section 7).

A full hub (`OVERQUOTA`, RFC 9208) or a chunk over the server's `APPENDLIMIT` stops the upload with
a clear state the app can show. The journal entry stays, and the upload resumes when asked.

## 7. Collecting chunks

**Live and protected.** Clean-up and healing must agree on which manifests still need their chunks,
or one would restore what the other destroys. A manifest is **live** on a device when it is the
value of a kept write (§9.3) of a record that is not deleted, in a scope that is not deleted
(§11.9). A manifest that stayed kept under a record's tombstone, because it was written without
having seen the delete (§9.2), is not live. A manifest is **protected** when it is live, or the
value of a held write (§8.3), or of a pending write in the device's own upload journal, or when the
next paragraph says so. Healing restores the chunks of live manifests (section 8); clean-up leaves
alone the chunks of protected ones.

**Only what is on the feed takes protection away.** A device's replica can be ahead of the hub: it
may hold a write of its own that no message on the hub holds yet, because the publish failed, is
waiting for the network, or was cut short by a crash. That may be the replacement the device just
committed (section 6), any other assignment to the field, or a delete of the record. A write is **on
the feed** for a device when a message that holds it is in the feed folder: the server answered the
device's append of it, or the device has seen the message there in a sync. A manifest that would be
live if the replica held only writes that are on the feed is protected. So the device does not
collect a version's chunks while every other device still shows that version. The device already
records which message holds which write, in its store, so this holds across a restart.

Chunks are never dominated the way record messages are (§10.7). In the hygiene pass that follows a
full sync of a scope, a device MAY destroy a chunk message when all of this holds:

1. its `Tandem-Channel` is the tag of a channel of that scope which the device knows (any epoch);
2. the device holds a valid write in that scope whose manifest lists its address, so the device can
   account for the chunk;
3. no protected manifest of the scope lists its address.

Condition 1 keeps one scope's clean-up from touching another's chunks, whatever a manifest claims.
Condition 2 means a device never destroys what it cannot explain, as with messages it has not
merged. A pending replacement changes nothing here: the version it will replace is still kept
(section 6).

**When a device cannot know.** Every rule of this section decides from the manifests a device can
read, so a device that knows it cannot read them all holds back:

- It collects nothing in a scope, by any rule here, while a kept or held `_blob:` write of that
  scope has a manifest of a version it does not know: a JSON object whose `blob` is an integer other
  than 1. Such a manifest may list chunks, among them chunks an older, superseded manifest also
  lists, and the device cannot tell which. A value that is no manifest at all lists nothing and
  blocks nothing.
- It collects no orphans in a scope while the current epoch's inventory is not assembled, or lists a
  write the device neither holds nor has seen replaced (§8.3). Such a write may be a manifest that
  devices holding it will publish again (§12.2), and until then its chunks look like orphans.

The second rule stops at orphans on purpose. A device that joined an epoch late can never resolve
the entries of writes that were replaced before it came; if they blocked every rule, it would never
collect at all. What that leaves open is in "What can go wrong" below.

**Deleted collections.** When a collection's roster ends in a statement with `deleted` set (§11.9),
its manifests stop being live, so nothing heals its chunks, but its records carry no tombstones and
the rules above would never release them. They don't apply: no device collects in a deleted
collection. Its chunks are part of its data on the hub and go the way §11.9 sends that data: after
the retention period, a device of the owner destroys every chunk message whose `Tandem-Channel` is
the tag of one of the collection's channels, when it destroys the collection's other messages.
Until then the chunks stay, so that the collection can still be copied (§11.8).

**Copies.** Several messages can carry one address: an append repeated after a lost answer, or two
devices healing the same chunk, which gives identical envelopes. Nobody owns such a message, so the
rule cannot depend on who appended it. After an index refresh, a device MAY destroy a message for
which condition 1 holds when it has verified another message with the same address and a lower ID
(section 4.3). Every device thus keeps the same survivor, the lowest verified copy, and since a lower ID
cannot appear later, no device comes to prefer a copy another has destroyed. The survivor must be
verified: a junk message with a low ID claiming the address would otherwise get every real copy
destroyed.

**Orphans.** A chunk that no write ever listed comes from an upload that never committed. Its
uploader destroys it (section 6). Another device MAY destroy it when conditions 1 and 3 hold, no write the
device holds has ever listed it, and the device itself first saw the message more than the **orphan
grace** ago, by its own clock and its own chunk index. The default is 7 days, a per-hub setting. No
time stored on the hub is used: a client may set a message's `INTERNALDATE` when it appends
(RFC 9051 §6.3.12), so one future-dated message could make every unfinished upload look old. The
guarantee is therefore local: an honest device collects no orphan it has known for less than the
grace, and nothing a participant writes to the hub shortens that. A device whose own clock jumps
forward may collect early; that costs an upload in progress some chunks, which its commit check
appends again (section 6). This is a safety net for a device that died mid-upload, not a mechanism anything
waits on. The limits of "When a device cannot know" apply.

**What can go wrong, and what repairs it.** Clean-up decides from what one device has seen. Three
rare histories leave a live manifest with a destroyed chunk: a device reuses or re-assigns a blob at
the moment another collects it; an old write becomes current again in a new epoch (§9.3) after its
chunks were collected; or a write the inventory lists, which the collecting device never held, lists
a chunk again that a superseded manifest listed. In each the record is intact and the file reads as
unavailable until a device that holds it restores it (section 8). The commit check of section 6
covers the uploader's own case.

## 8. Reading, keeping and healing

**States.** For each manifest a device reports one of: **here** (inline, or downloaded and
verified), **remote** (not fetched), **downloading** with progress, **unavailable** (at the last
index refresh a listed address had no message, or only rejected ones) and **unreadable** (section
5). Unavailable is not an error of the record and is distinct from remote.

**Fetching.** The device refreshes the chunk index, finds a message claiming each address, fetches
it and checks the hash. That check verifies or rejects the message (section 4.3); after a rejection
another message claiming the same address is tried. The device then opens the chunk and compares
the plaintext's length with the entry's (section 5) before it writes the plaintext at the entry's
offset or shows any of it. Download resumes, and content can be read from any offset, chunk by
chunk. An unavailable blob is tried again at the next index refresh or when
the app asks.

**Keeping.** Whether a device fetches on demand or keeps every file of a scope is a setting per
scope and device, with an upper size for what is fetched unasked. The blob store has a size limit;
what is evicted becomes remote again. A staged copy is kept until its write is published.

**Healing** (§12.2) gains one rule. After an index refresh in the hygiene pass, for every live
manifest (section 7) in a current channel whose whole content the device holds: each listed address
that is not on the hub (section 4.3: an unverified claim is fetched and settled first) is sealed
again with the listed key and appended. The envelope must hash to the address. If it does not, the
device does not yet know which of the two is wrong, and hashes its local copy. If that hash differs
from the manifest's `hash`, the copy is damaged and is dropped. If it matches, the copy is good and
the manifest is inconsistent: an entry's key or address does not belong to the content its hash
names. The device keeps the file, which another manifest with the same hash may need, marks this
manifest unreadable (section 5) and appends nothing for it. Only live manifests are healed: a manifest under a tombstone
or in a deleted or dead collection is left alone, so healing never restores what clean-up may
destroy. A device that holds no content does nothing, which is the durability cost named in section
2.

## 9. Keys, membership and removal

- **A key change uploads no chunk.** The manifest write is carried into the new epoch like any other
  write, with its chunk keys inside.
- **A removed member** can still open the chunks of every manifest they could read, including files
  they had not fetched, for as long as they hold the hub credential. That is §3.3's rule: they read
  what was written under keys they held. Files attached after the removal have new keys in a channel
  they cannot read.
- **A reader** can fetch and open blobs and cannot write manifests. Like anyone with the hub
  credential they can still append junk and destroy messages (§3.1).
- **Deleting a record** makes its chunks collectable by section 7, by any device of the scope.
- **Deleting a collection** leaves its chunks on the hub for the retention period of §11.9; a
  device of the owner then destroys them with the collection's other data (section 7).
- **A dead collection** (§9.4) stops collecting and healing and keeps its blobs readable.
- **Moving or copying to another scope** uploads the blob again under new keys. A blob belongs to
  one scope.
- **Quota.** Where the server offers `QUOTA` (RFC 9208), the binding reports used and total storage;
  otherwise it reports that the quota is unknown, never zero.

## 10. Reusing chunks (reserved; not in the first implementation)

Nothing in the wire format ties a chunk to a blob or to a position in it. A later writer may
therefore cut files where their content suggests (content-defined chunking), keep a local table
from plaintext hash to manifest entry, and list an existing entry in a new manifest instead of
uploading the chunk again. To leave that open, a reader MUST already accept from day one:

- chunks of any mix of lengths within the limits of section 4.1;
- the same address more than once in a manifest;
- an address listed by several manifests of one scope (section 7 condition 3 already counts them
  all).

A writer may reuse an entry only from a kept manifest of the same scope.

## 11. Prior art

| Work | What it does | What this takes or leaves |
|---|---|---|
| **IMAP Storage Filesystem** (2007), **GmailFS** (2004), **EMFS** (2016), **ePFS** (2007) | Mount or sync files as mail messages, with file metadata and state inside the messages. | They rewrite messages to change anything and leave concurrent edits, interrupted uploads and conflicts open. Here blobs never change and all mutable state is in CRDT records. |
| **Usenet binaries**: yEnc, NZB, PAR2 | Files posted as many articles below about 1 MB, an index file listing every segment's message ID and size, and parity files to repair lost segments. | The same shape after decades of use: segments, a manifest of segment IDs, a repair path. Healing from replicas stands in for parity. yEnc's 1–2% overhead comes from an encoding mail servers don't know; IMAP's `BINARY` gets to zero within the standards, with base64 where it is missing. |
| **Matrix** encrypted attachments | A single-use key and a SHA-256 of the ciphertext travel in the encrypted event; the ciphertext sits in a media store. | The manifest is the same idea per chunk. Matrix's AES-CTR is unauthenticated (MSC4016 proposes fixing that); chunks here are AEAD. |
| **restic**, **borg**, **Tahoe-LAFS**, **Perkeep** | Encrypted chunks addressed by hash; clean-up by reachability, guarded against uploads in progress. | Addresses are hashes of ciphertext; clean-up is conservative and repaired by healing instead of a repository lock. |
| **Kolab** (§16) | Files as objects in IMAP folders, in production. | Confirms mail servers hold such data durably. |

## 12. Measurements (2026-11-15)

`probes/blob_probe.py` appends chunks of 256 KiB, 1 MiB and 4 MiB in both forms, fetches them back
and compares every one byte for byte. Each chunk begins with NUL bytes, bare CR and LF, a lone dot
and a line of 2,000 bytes. Reports are in `probes/results/*-blobs-*.json`. Fastmail was run twice.
Stalwart is the disposable test server on the same machine, so its speeds mean nothing and are left
out. Gmail was not run: the keychain item for it could not be read from the session that ran these.

| | Fastmail | Stalwart 1.1.0 | Gmail |
|---|---|---|---|
| `BINARY` | yes | yes | no (probed 2026-11-13) |
| Binary chunks intact through `BINARY.PEEK[1]` | 126 of 126, both runs | 126 of 126 | |
| How a binary chunk is stored | as base64, header rewritten to `Content-Transfer-Encoding: base64`, quota charged at 1.37 times the bytes sent | raw | |
| `BODY.PEEK[TEXT]` of a binary chunk | base64 | the raw bytes | |
| `LITERAL+` and `MULTIAPPEND` | both | both | neither |
| `APPENDLIMIT` | 200 MB | not advertised | 35 MB |
| Quota (RFC 9208) | reported | advertised, none set | not read |

Fastmail, MiB of chunk content per second, first run / second run:

| Form | Chunk | Append, one at a time | Append, pipelined | Append, `MULTIAPPEND` | Fetch, one at a time | Fetch, one command |
|---|---|---|---|---|---|---|
| binary | 256 KiB | 0.4 / 0.4 | 5.2 / 3.7 | 6.8 / 6.9 | 0.7 / 0.7 | 8.6 / 9.3 |
| binary | 1 MiB | 1.6 / 1.6 | 9.6 / 7.9 | 9.3 / 9.4 | 2.6 / 2.5 | 12.4 / 10.2 |
| binary | 4 MiB | 5.7 / 4.8 | 12.4 / 11.9 | 11.1 / 11.0 | 7.1 / 6.2 | 15.1 / 13.8 |
| text | 256 KiB | 0.5 / 0.5 | 4.6 / 1.7 | 5.4 / 5.0 | 0.7 / 0.7 | 3.2 / 3.2 |
| text | 1 MiB | 1.0 / 0.9 | 0.7 / 1.6 | 3.3 / 5.7 | 2.5 / 2.5 | 5.6 / 5.5 |
| text | 4 MiB | 1.6 / 1.5 | 1.5 / 1.5 | 8.3 / 9.0 | 6.5 / 6.4 | 8.9 / 8.6 |

What follows from it:

- **Batching matters more than chunk size.** An append answered alone costs about half a second and
  a fetch about a third, whatever the size. One at a time, 1 MiB chunks go up at 1.6 MiB per second;
  batched, at 8 to 10.
- **1 MiB stays the default where appends can be batched.** Batched, it is within about a quarter of
  4 MiB, and 256 KiB loses up to half. Where they cannot be batched the question is open (B1).
- **The binary form is worth having on Fastmail for speed only**: about twice as fast both ways when
  batched, and no saving in storage (B2).
- **Text appends sent pipelined were slow and uneven** (0.7 to 4.6); in one `MULTIAPPEND` they were
  steady, and binary ones were as fast as pipelined. Not explained. Section 4.3 therefore puts
  `MULTIAPPEND` first for both forms.
- **The feed stays live during an upload, at half speed.** A small append to the feed folder reached
  an idling connection in 0.53 s with nothing else going on, and in 1.0 to 1.2 s while another
  connection uploaded 1 MiB chunks.
- **The chunk index scan works as specified**: one command returned the ID and both headers of all
  126 messages in 0.32 s. Scale is unmeasured (B11).

## 13. Open questions

| # | Question |
|---|---|
| B1 | **Chunk size where appends cannot be batched.** 1 MiB holds on Fastmail (section 12). Gmail offers neither `LITERAL+` nor `MULTIAPPEND` and is unmeasured; if an append there costs what one costs on Fastmail, 4 MiB chunks would move about three times as fast. Should the default depend on the server? |
| B2 | **Storage of binary chunks.** `BINARY` works on Fastmail and Stalwart (section 12), but Fastmail stores the chunk as base64, so it saves transfer and no quota there. No way to store raw bytes on Fastmail is known. |
| B3 | **Provider limits and terms.** Gmail's append limit was probed at about 35 MB and its daily IMAP volumes are documented for Workspace only. Fastmail advertises an append limit of 200 MB; its behaviour with large folders of large messages is unmeasured, and no provider's terms were checked for use as file storage. |
| B4 | **Orphan grace.** Is 7 days right, and should the uploader's app show an upload that has waited that long? The grace runs on each device's own first sight of a chunk (section 7), so a device that opens the blob folder rarely collects late. |
| B5 | **Copying a dead collection** (§11.8) uploads every blob again. Letting the copy list the old chunks would avoid that but nothing would ever collect them. |
| B6 | **Earlier versions.** Keeping the chunks of superseded manifests for a time would give version history. Not designed. |
| B7 | **Padding** the last chunk to hide exact sizes (Q9). |
| B8 | **Waiting for an unavailable blob.** A device could idle on the blob folder while it wants a missing chunk. Today it retries at the next refresh. |
| B9 | **Proofs.** The clean-up rule of section 7 and the pending write of section 6 should get the treatment the mailbox clean-up proofs give §10.7. |
| B10 | **The browser binding** (§10.2): a second Mailbox and the same message format; JMAP could fetch byte ranges. Not designed. It needs an order of message IDs that every device shares (section 4.3), which JMAP's IDs do not give by themselves. |
| B11 | **Scale.** The chunk index was scanned for 126 messages only. How long does a folder of tens of thousands of chunks take to scan and to select? |
