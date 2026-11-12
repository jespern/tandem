# What we assume about Tandem now

Written against the **frozen** `DESIGN.md` (`main` at `f4ce2e6`, 999 lines, merged into this branch), with `model/owner_model.py` as its model (50 tests, all passing). Earlier versions of this note described withdrawn designs and the 13:05 draft; git history has them.

The scenarios on this branch were written against the earliest of those designs and are **out of date**; the refresh waits for Jesper's go-ahead.

## 1. The design in one page

**Users.**
- A user is **one secret** (`US`), held by all of their devices and printable as a **backup key**. Their user ID is their public signing key.
- There are no per-device keys, no device chain, no recovery key, and no way to remove one device.
- A new device is paired by QR code plus a **six-digit code** compared on both screens, and receives the secret sealed to a one-time key. Restoring from the backup key needs nothing on the hub to change.
- A stolen device or a seen backup key means **starting over** with a new identity: the user's own lists are copied into new ones and other people re-invite them.

**Collections.**
- **One owner for life**: whoever signed the genesis, fixed by the collection ID.
- Members are **writers or readers**. There are no admins and no hand-over.
- Only the owner changes sharing. The owner can't leave, only delete.

**Roster.**
- A chain of complete statements, signed by the owner's key, each `n + 1`. Nothing merges and nothing ranks.
- **Two owner-signed successors of one statement kill the collection.** It goes read-only for everyone, with an offer to copy it into a new collection. The two statements are posted where every member will find them.
- The owner's devices avoid that through a **`flight` note in home**: no device signs while another device's change is pending, and a device can finish or drop another device's change and tells the user which (§11.7). Two devices acting in the same moment, or a hub hiding one device's change from another, still kill the collection (Q20).

**Epochs.**
- An epoch is identified by the ID of its **starting statement**. The channel tag derives from both the key and that ID.
- Removing a member or lowering a role starts a new epoch with a fresh key. Adding someone doesn't.
- Rotation is owner-only, needs the network, and runs as a **journalled six-step procedure** that resumes after a crash with the same signed bytes (§11.6). The announcement in the old channel is the moment the change happens, and the **only** evidence of it: a missing `flight` note says nothing.
- An epoch-starting statement that hasn't been announced is a **draft**: not part of the roster, not a successor, not half of a conflict. A discarded draft's channel is the one place a roster copy is destroyed.

**Data validity.**
- A write names the roster statement it was made under. It is eligible in epoch `E` iff its source epoch is exactly `E`, or it is listed in `E`'s **snapshot inventory**.
- The inventory is canonical bytes whose hash is committed in the statement, published as fragments. It also carries forward entries the rotating device hasn't seen.
- Late writes cross epochs only when their author **signs them again**, by fixed rules that produce identical bytes on every device of that user.
- Validity never changes, so pruning is safe.

**Merge.**
- §9.2–§9.3 are unchanged: multi-value registers and an add-wins set over `context`/`base` vectors, and sticky tombstones, kept for good.
- New: a vector may hold at most **1,024 entries**. A field that would need more is reset by deleting the record and creating it again.

**Sharing.**
- **Invites live in the owner's home**, not the roster. The invite channel is keyed from the invite secret, so everyone who holds the invite can read the join requests made on it (not only the owner's devices); members who never held it can't.
- An invite is **closed for good** once anyone it admitted has been removed, even if they came back another way. A user the roster once listed and no longer lists is **never admitted without the owner being asked**. Both are read from the roster's history (`via`), not from home.
- Grants are signed by the owner, and one grant catches a device up across any number of missed epochs.
- A removed member who asks for a key gets the signed statements that removed them.
- Leaving is a notice that the owner confirms. Declines are hints only.
- The collection's name and icon are an owner-only data record, `_meta`, so renaming works offline.

**One reading of every message (§5.2, §10.5).** Protocol JSON is rejected whole on a repeated property name (compared as bytes), a number that isn't a plain integer within 2^53, invalid UTF-8, a byte-order mark, an unpaired escaped surrogate, or anything but whitespace around the value. `b64u` has one spelling. Text is compared as UTF-8 bytes everywhere. The Swift SDK needs its own pre-parse checks (Foundation keeps the first duplicate) and byte comparison (Swift's `==` normalizes).

**Hub.** The bindings (§10) are unchanged apart from the testbed findings (below), except that inventory fragments, the roster and the two statements of a dead collection are never pruned. Restoring a wiped hub is an explicit user action.

## 2. Our findings, and how the frozen draft settled them

All three are fixed in the frozen draft, and each fix matches what we meant.

1. **Garbage-collection tie-break (§10.7).** Of two messages that dominate each other, whatever writes they hold, only the one with the greater `H(envelope)` may be destroyed on account of the other. Byte-identical copies are removed only by the device that made the envelope, keeping one. "May destroy `m` on account of `n`" is then a strict partial order, so the messages at the top of any chain are never destroyed and nothing is lost, even on stale views. The Lean `GC` module proves this and checks that the old rule's counterexample fails under the new one. The proof assumes a device sees its own destroys (read-your-writes), so that it never keeps different copies in two rounds.
2. **Testbed findings (§10.2–§10.4).**
   - `invalidArguments` on `Email/changes` means a full resync.
   - The push timeout is two of the intervals the server reports.
   - A JMAP reader checks `isEncodingProblem` and falls back to the raw message.
   - The IMAP connection cap can be per account (§10.3).
   - A device knows its own messages by `H(envelope)` and looks for an envelope on the hub before appending it again.
3. **Device ID after a restore (§5.3, §6.3).** A new device ID whenever the secret is installed, by pairing or from the backup key.

### New, from the test vectors (2026-11-12)

Writing the vectors meant choosing bytes where the frozen draft is silent; `vectors/README.md` lists the seven choices. Two findings need a sentence in the draft:

1. **Randomized signatures.** CryptoKit's Ed25519 adds randomness, so the same write signed twice in Swift has two signatures and one write ID. §11.6 ("the same bytes") should say "the same write ID", and §9.4's conflict should be between successors with different statement IDs, whatever their signatures.
2. **Unnamed pairing and request payloads.** Pairing requests, challenges and replies share one channel with no `type` field, and the reply's JSON isn't given; join requests have no `type` either. The vectors cover their signed inputs only.

Also open: whether named records (`_roster` included) really carry a `_name` write (§7.3), and in which scope a genesis's would be signed.

### New, from the Go engine and the proofs (2026-11-12)

These need a sentence in the draft. Ranked by impact.

1. **Resuming a key change can kill the collection (§8.2, §11.6; scenario gap G-08).** Found by `device_test.go`, crash point `2:journal`. The first device journals its signed statement and crashes before writing the note. The second device sees no note and makes the change itself. The first restarts and, as §11.6 says, "resumes with the same signed bytes": two successors, dead. **Fix (implemented):** signed bytes that were never exported may be dropped. On resume, a device that hadn't written its note uses its statement only if the parent is still the head and no other note is present. And just before announcing, a device checks that the note is still its own and the parent still the head; otherwise it drops the change and tells the user.
2. **Which register a delete belongs to (§9.2; Lean F1).** Not stated. Read by (record, field), as `owner_model.py` does, a covering assign drops a tombstone. Read as its own register, as Lean and the Go engine do, only a delete supersedes a delete. The SDKs must agree.
3. **Inventory entries with no writes (§8.3; Lean F3).** The format allows them, so the bytes aren't a function of the listed set. Require at least one write per entry.
4. **"Valid roster statement" (§8.3).** A statement can be valid relative to an invalid parent. The Go engine accepts data only under a statement whose whole chain back to the genesis is valid.
5. **Our plan, not the design (Lean F2).** A re-signed write *can* supersede a write the snapshot kept, rightly: it replaces it again. `Reissue` proves what holds instead.

The scenario refresh found 62 open gaps in all (`scenarios/README.md`, "Open gaps"); G-08 is item 1 above.

### Fastmail, §18 Q1–Q3 (2026-11-12)

Measured on a dedicated account (`liszto@noehr.org`) with API tokens; `sdk/go/store/fastmail_test.go`.

- **Q1, push: yes, but one EventSource connection per token.** Push reaches API-token clients, about 0.4–0.6 s from append to another client seeing it. A second connection with the **same token** makes the server send `event: close` to the older one within 0.4 s; another token's connection is unaffected. With §13.1's default of one shared credential, only one device has push at any moment and devices knock each other off. Per-device tokens fix it, but each needs the hub owner to make a token in Fastmail's settings. **This needs a design decision** (see the review request). The Go binding backs off up to 5 minutes when connections are closed quickly, so devices sharing a token don't fight every second.
- **Q1, limits:** `maxObjectsInSet` 4,096, `maxCallsInRequest` 50, `maxConcurrentRequests` 10, `maxSizeRequest` 10 MB. 500 appends in one `Email/set` take 2.7 s. Ten parallel small `Email/get` calls average about 0.9 s each, so batch reads instead. No rate-limit errors seen.
- **Q1, pings:** reported as `{"interval":60}`. With `ping=0` the server sends none.
- **Q2, headers: yes.** `Tandem-Version` and `Tandem-Channel` are stored and returned unchanged. The body is kept as `text/plain`, 7-bit, `us-ascii`. Fastmail writes its own `Date` (here with a −05:00 offset).
- **Q3, folder size: 100,000 messages work.** Appending them took 5:48. A full resync (`Email/query` paged, plus headers) took 64 s; an incremental change 0.27 s; fetching 100 bodies 0.6 s; destroying all 16 s. At 10,000 messages the full resync took 2.1 s.
- **IMAP, for the record** (app password; API tokens are refused over IMAP). Every IDLE connection on one credential is told of changes, unlike JMAP's one push connection per token, and 40 simultaneous connections were accepted. An IMAP append reaches other IDLE sessions in about 2 s, but a **JMAP append reaches IMAP IDLE only after about 58 s** (checked with Python's `imaplib` too, so it is Fastmail, not our client). Mixed bindings on Fastmail would leave IMAP devices a minute behind JMAP ones. Envelopes cross intact both ways.
- **Decision (Jesper, 2026-11-12):** one API token per device on Fastmail, over JMAP.
- **Found on the way:** the JMAP binding must follow the URLs the server advertises. Fastmail's download URL is on another host, so rewriting it to the session host (which the testbed's proxy needs) is now opt-in. A full resync must page until `total`, not until a short page.

### Applied from Fable's review (2026-11-12, Jesper: "make the corrections now")

In the Go engine, the vectors and the Swift SDK; the draft text waits for Sol's review and Jesper.
- Deletes: an empty field name; a delete supersedes every write of the record it includes, in any field; only a delete supersedes a delete. Lean is being re-proved for it.
- JSON vectors must arrive sorted by device ID, as signed.
- Every inventory entry lists at least one write.
- Key changes: a draft may always be dropped; a change resumes only while its note is in home and its parent is the head; finishing and discarding act only on the note the user was shown; admitting a member re-checks the head between signing and publishing.

Not yet in the engine, because the engine doesn't have the feature yet: leave notices (G-06), signing again (G-26), pairing payloads with `type`, declines (G-09), split records (G-40), version handling (G-60), HLC loss (G-20).

### Sol's review (2026-11-12) and where it disagrees with Fable's

`review/2026-11-12-sol.md`, written against `1c2631d`, before Fable's corrections were applied. Three code bugs, all fixed with regression tests: a field could alias the delete register (`"\x00delete"`); a lost answer to the note's append made `Resume` drop its journal and strand the note; a `flight` of JSON `null` (as `conformance/` writes it) read as a pending change. The Fastmail one-stream-per-token result is now a repeatable test (`TestFastmailOneStreamPerToken`).

Fable and Sol agree on findings 1, 3, 5 and most of 6, and on the gaps being real. Where they differ (decisions for Jesper):

| Topic | Fable | Sol | Implemented now |
|---|---|---|---|
| Deletes | supersede every write of the record they include, so a deleted record shrinks to one write (§12.3) | a register of their own; other fields' writes stay | Fable's (Go, Swift, Lean proved) |
| Vectors in JSON | must arrive sorted | any order, sorted for signing | Fable's |
| G-26 poisoned base | at signing again, a vector entry for one's own device ID above anything it issued is forged; sign again anyway | keep such writes as possibly unshared work and show them to the app | **both** (`device/resign.go`, `Unshared`) |
| G-40 split records | domination by a set of messages, with a hash tie-break | rejects that: `{a,b}`, `{b,c}`, `{a,c}` each dominated by the other two; publish only when the union doesn't dominate, keep pairwise GC | **Sol's**; for when healing and splitting are built |
| G-09 refused requests | new `declined` state | no new state; stop waiting, keep `requested` | **Sol's**; for the invite flow |
| Home field encodings | not raised | specify every protocol home field; `flight` schema and `null` for none, agreed with `conformance/` | **Sol's**; proposed schema in `review/home-encoding.md`; Go reads empty and `null` |

**Decided by Jesper, 2026-11-12: all of the "my recommendation" column** (deletes and sorted vectors as Fable; G-26 both; G-40, G-09 and home encodings as Sol).

## 3. What this means for our work

### Scenarios (refresh pending)

| File | State |
|---|---|
| `identity.md` | Almost entirely obsolete. Device chains, recovery keys, revocation and theft are gone. Replace with pairing and its code, the backup key, restoring, and starting over. |
| `rotation.md` | Almost entirely obsolete. Replace with:<br>• the owner-only journalled rotation, crashing at each of its six steps;<br>• the `flight` note and finishing or dropping another device's change;<br>• inventories, including entries carried forward unseen;<br>• signing again;<br>• dead collections, and the news reaching every member. |
| `sharing.md` | Largely obsolete: admins, ranks and hand-over are gone. Rewrite for invites in home, owner-confirmed leaving, declines as hints, and copying a dead or orphaned collection. |
| `permissions.md` | Rewrite the capability matrix for owner / writer / reader (plus removed, dead and outsider). The world checks and the bystander invariant still apply. |
| `faults.md` | About a third needs redoing: crash points move to the journal, staleness is gone, and a hub hiding a change from another device now kills the collection. |
| `records.md`, `scale.md` | Mostly hold. The vector limit settles SC-011. A `delete` on a named record is now invalid. Swap epoch numbers for epoch-start IDs. |
| `hub.md` | Mostly holds; add inventory fragments and the records that are never pruned. |
| `collab.md`, `realtime.md` | Hold. |

New scenario areas:
- two devices of one owner;
- a dead collection, seen by members who can't read the channel where it died;
- a removed member's key request answered with statements;
- the pairing code against a photographed QR code;
- a restored phone and its device ID;
- the vector limit and the reset by re-creation;
- a member removed, the device crashing before closing their invite in home, and the old join request posted again;
- a removed member let back in by a new invite, with the old invite staying closed to a new identity;
- a change discarded while another device still holds its key, which must not treat it as made;
- two JSON properties with the same name; a field name in two Unicode spellings; a `b64u` value in a non-canonical spelling;
- two hub messages that dominate each other with different contents, and byte-identical copies.

### Proofs

The cut makes the Lean work smaller and more tractable.

- **Unchanged:** `Supersession`, `Register`, `ORSet`, `Tombstone`, `Display`, `HLC`.
- **GC:** the counterexample now fails; prove the fixed rule (§2.1).
- **Update:** `Encoding`. `sigInput` now carries the author's signing key as the user ID, and `ref`; add the canonical inventory bytes (injective and deterministic).
- **Replace** the roles, log and chain theorems with `Roster`:
  - a statement's validity depends only on its parent;
  - "dead" is a function of the set of statements held, whatever order they arrived in;
  - once dead, always dead: dead in any superset;
  - every device that holds both conflicting statements agrees the collection is dead.
- **Add:**
  - `Eligibility`: a write's eligibility in an epoch never changes;
  - `Reissue`: every device of a user signing an old write again produces the same bytes, and a re-signed write never supersedes a write the snapshot kept;
  - `VectorLimit`: building a write never exceeds 1,024 entries unless it says so.
- **Cross-check:** the Lean model is the oracle (decided). It is differentially tested against `model/owner_model.py`, whose 50 tests become Lean examples. `tandem_model.py` and `rotation_model.py` model withdrawn designs and are ignored.

### Unaffected

The testbed, the binding and real-time probes, and the Go reference SDK plan, which is now smaller: no device chain, no fork choice. The Swift plan likewise.
