# Hub scenarios

How Tandem uses the mail account: the abstract store and its JMAP and IMAP bindings on Stalwart, the message format, publishing and garbage collection, healing and hub loss, what the hub learns, unknown versions, and hostile or malformed messages on the hub.

Binding `jmap+imap` marks a mixed scenario: some devices use JMAP and others IMAP against the same Stalwart account. The Setup says which device uses which.

---

### HB-001 Mailbox lookup ignores substring matches
- **Kind:** edge
- **Design:** §10.1, §10.2
- **Binding:** jmap
- **Setup:** Fresh Stalwart account. Before any device connects, a mail client creates the mailboxes `Tandem Archive`, `Old Tandem`, and `Tandem` as a child of `Archive`. There is no `Tandem` at top level or under the inbox.
- **Steps:**
  1. `J.mac` connects the hub and finishes first run.
  2. `J.mac pairs J.phone`.
- **Expect:**
  - The account gains exactly one mailbox, named `Tandem`, with no parent.
  - Every message `J.mac` and `J.phone` append is in that mailbox; none is in the three pre-existing mailboxes.
  - The three pre-existing mailboxes keep their names, parents and contents.
- **Invariants:** INV-CONVERGE, INV-HUB-BLIND
- **Notes:** Creating mailboxes from a mail client is outside the step vocabulary (harness: raw `Mailbox/set`).

### HB-002 A `Tandem` under the inbox splits JMAP from IMAP
- **Kind:** fault
- **Design:** §4, §10.2, §10.3
- **Binding:** jmap+imap (`J.mac` JMAP; `A.mac` IMAP; Stalwart's personal namespace prefix is empty)
- **Setup:** Before anyone connects, a mail client creates `Tandem` as a child of the inbox. No top-level `Tandem` exists.
- **Steps:**
  1. `J.mac` connects the hub and creates collection House.
  2. `A.mac` connects to the same account over IMAP.
  3. `J.mac invites qr to House as writer`; `A.mac joins via` it.
  4. `J.mac quits`, then `J.mac launches`.
- **Expect:**
  - After step 4, every message either device has appended is in a single mailbox.
  - `A.mac` shows House with its contents within one sync of step 3.
  - `J.mac` and `A.mac` hold the same House.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** GAP (G-53): §10.2 makes a JMAP client fall back to `Tandem` directly under the inbox, to meet IMAP clients whose namespace prefix is `INBOX.`. An IMAP client whose prefix is empty runs `LIST "" Tandem`, finds nothing, and creates a top-level `Tandem` (§10.3). From then on the two bindings use different folders. If `J.mac` cached the mailbox id, it never sees `A.mac`'s messages. If it re-resolves on launch, it moves to the new top-level folder and leaves its earlier messages behind. Neither binding says how the two must agree. Neither says whether the mailbox is resolved once or on every sync (see HB-041). Possible fix: IMAP also looks under the inbox, or both bindings prefer whichever candidate already holds `Tandem-Channel` messages.

### HB-003 Two devices create the folder at the same moment
- **Kind:** edge
- **Design:** §10.2, §10.3
- **Binding:** jmap+imap (`J.mac`, `J.phone` JMAP; `A.mac` IMAP)
- **Setup:** Empty account. The harness holds each device's create request until all three have seen that no `Tandem` exists (`net: latency` on the create).
- **Steps:**
  1. `J.mac`, `J.phone` and `A.mac` each run `ensureFolder` at once.
  2. Each device appends one message.
- **Expect:**
  - The account holds exactly one mailbox named `Tandem`.
  - All three appends land in it.
  - `A.mac` treats `ALREADYEXISTS` as success. A JMAP device whose create fails queries again and uses the existing mailbox. No device shows an error.
- **Invariants:** INV-CONVERGE
- **Notes:** GAP (G-54, minor): §10.2 says "on a duplicate-name error, query again", but RFC 8621 defines no duplicate-name `SetError` for `Mailbox/set`. Servers return `invalidProperties` or a type of their own. The rule should be "query again after any failed create".

### HB-004 `Email/set` batches, limits and partial failure
- **Kind:** fault
- **Design:** §10.2, §10.6, §11.6
- **Binding:** jmap
- **Setup:** Stalwart configured with `maxObjectsInSet` = 5 and a low `maxSizeRequest`. House has 40 records, J is owner on `J.mac`, A is a writer, B is a writer. The account quota leaves room for 30 more messages.
- **Steps:**
  1. `J.mac removes B` (rotation; about 41 messages to append).
  2. After the quota is hit, the harness raises the quota.
  3. `J.mac syncs`; `A.mac syncs`.
- **Expect:**
  - No `Email/set` call from `J.mac` carries more than 5 creates. No call exceeds `maxSizeRequest`. No `Email/get` asks for more ids than `maxObjectsInGet`.
  - Creates reported in `notCreated` (`overQuota`) are retried after step 2. Creates that succeeded are not retried.
  - After step 3, the new epoch holds every record and `A.mac` shows all 40.
  - Once GC has run, each record has exactly one message in the new channel.
- **Invariants:** INV-NO-LOSS, INV-GC-SAFE, INV-QUIESCE
- **Notes:** GAP (G-55): §10.2 names only `maxObjectsInSet`. The session also limits `maxSizeRequest`, `maxCallsInRequest` and `maxObjectsInGet` (RFC 8620 §2). Five 1 MiB messages can exceed a server's request size, and `changes`/`fetch` must page `Email/get`. Nothing says how to handle `notCreated`: which errors are retried (`overQuota`, `tooLarge`, `rateLimit`), and what a rotation does when part of its snapshot fails (see FT-011, FT-029).

### HB-005 `Email/changes` with `hasMoreChanges`
- **Kind:** edge
- **Design:** §10.2
- **Binding:** jmap
- **Setup:** Stalwart caps `Email/changes` at 10 changes per response. `J.phone` is offline. Meanwhile `J.mac` makes 150 edits across 60 House records and garbage-collects as it goes.
- **Steps:**
  1. `J.phone online`; `J.phone syncs`.
- **Expect:**
  - `J.phone` repeats `Email/changes` until `hasMoreChanges` is false, each time starting from the previous `newState`.
  - The state it stores is the last `newState`.
  - `J.phone` holds the same 60 records as `J.mac`.
  - `J.phone` appends nothing: everything it holds is dominated on the hub.
- **Invariants:** INV-CONVERGE, INV-QUIESCE

### HB-006 A moved message is an update, not a destroy
- **Kind:** edge
- **Design:** §10.1, §10.2, §10.3, §12.2, §10.7
- **Binding:** any
- **Setup:** `J.mac` and `J.phone` follow House. Record Milk is in one message `m` on the hub.
- **Steps:**
  1. `hub: move m out of Tandem` (into `Archive`).
  2. `J.phone syncs`.
  3. `hub: move m into Tandem`.
  4. `J.phone syncs`; `J.mac syncs`.
- **Expect:**
  - Step 2 under JMAP: `m`'s id comes back in `updated`, not `destroyed`, and `J.phone` records `m` as removed. Under IMAP, `m`'s UID vanishes.
  - Step 2: `J.phone` republishes Milk as one new message `m'`.
  - Step 4 under JMAP: `m` returns with its old id and counts as added. Under IMAP it returns with a new UID.
  - After GC, exactly one of `m` and `m'` remains in Tandem: the one with the lower `H(envelope)`.
  - Milk is unchanged on both devices throughout.
  - No Tandem device destroys or changes anything in `Archive`.
- **Invariants:** INV-GC-SAFE, INV-QUIESCE

### HB-007 `cannotCalculateChanges` and a full resync during activity
- **Kind:** fault
- **Design:** §10.2
- **Binding:** jmap
- **Setup:** `J.phone` last synced at Email state `s0`. Stalwart's change log is then truncated, so `s0` can no longer be served. House holds 300 messages.
- **Steps:**
  1. `J.phone syncs` and gets `cannotCalculateChanges`.
  2. While `J.phone` pages through `Email/query`, `J.mac` appends 3 messages and destroys 2 that sort before `J.phone`'s current page.
  3. `J.phone syncs` again.
- **Expect:**
  - Step 1 uses `Email/query` with `inMailbox` and pages to the end.
  - Ids `J.phone` knew that are no longer in the mailbox count as removed.
  - After step 3, `J.phone` knows every message in Tandem, including the 3 appended in step 2, and none it doesn't hold is missed.
  - `J.phone` publishes nothing, because everything is dominated on the hub.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** GAP (G-56): the design doesn't say which state the client stores after a full resync. `Email/query` returns a `queryState`, not an Email state. The client must read the Email state (for example `Email/get` with no ids) before it starts paging. If it stores a state read after paging, the 3 appends from step 2 are never reported. Paging by position also skips messages when earlier ones are destroyed mid-query; this needs anchors or a final `Email/changes` pass from the pre-query state.

### HB-008 A device's own appends don't move its change cursor
- **Kind:** fault
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** `J.mac` and `A.mac` follow House and are idle. `J.mac`'s cursor is at UID 100 (IMAP) or state `s1` (JMAP).
- **Steps:**
  1. `net: latency 1500 ms` on `J.mac`.
  2. `A.mac adds Bread to House`, then 100 ms later `J.mac adds Eggs to House`. `A.mac`'s append lands first (UID 101); `J.mac`'s lands second (UID 102, `APPENDUID` 102, or `Email/set` `newState` `s3`).
  3. `J.mac syncs`.
- **Expect:**
  - `J.mac` shows Bread and Eggs after step 3.
  - Under IMAP, `J.mac`'s next `UID FETCH` range starts at 101 or lower.
  - Under JMAP, `J.mac`'s next `Email/changes` starts from `s1`, or from a state reported by an earlier `Email/changes`. It never starts from `s3`.
  - `J.mac` recognises UID 102 as its own by `H(envelope)`.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** GAP (G-46): §10.2 now has a device know its own messages by `H(envelope)`, "not by the id in an answer", which points the right way, but neither binding says what may move the stored cursor. A client that stores its own `APPENDUID` or `newState` skips `A.mac`'s message for good. The cursor should move only from `changes` results.

### HB-009 EventSource ping and a silent stream
- **Kind:** fault
- **Design:** §10.2
- **Binding:** jmap
- **Setup:** `J.phone` is in the foreground with an open EventSource. Stalwart reports its own ping interval `i` in every ping (testbed finding: it clamps the requested 60 s).
- **Steps:**
  1. `net: blackhole J.phone` (all bytes dropped, connection left open, no reset).
  2. `J.mac adds Milk to House`.
  3. `wait 4 × i`; the harness then removes the blackhole.
- **Expect:**
  - `J.phone`'s EventSource URL has `types=Email`, `closeafter=no` and `ping=60`.
  - Between `2 × i` and `2 × i + 10 s` after the last ping or event it received, `J.phone` abandons the stream and opens a new one: two of the intervals the server reports, not of the one requested. The proxy log shows the new connection.
  - Within 10 s of step 3, `J.phone` has a working stream, has run `Email/changes`, and shows Milk.
  - With no blackhole, `J.phone` opens no new stream for 10 minutes.
- **Invariants:** INV-CONVERGE
- **Notes:** `net: blackhole` is a Toxiproxy `timeout` toxic with timeout 0.

### HB-010 IMAP namespace prefix
- **Kind:** edge
- **Design:** §10.3, §10.2
- **Binding:** jmap+imap (`A.mac` IMAP, `A.phone` JMAP)
- **Setup:** The IMAP server reports personal namespace prefix `INBOX.` with delimiter `.`.
- **Steps:**
  1. `A.mac` connects the hub, finishes first run and creates collection Groceries.
  2. `A.mac pairs A.phone`.
- **Expect:**
  - `A.mac` sends `NAMESPACE` before `LIST`, then lists and creates `INBOX.Tandem`.
  - The account holds one Tandem mailbox, a child of the inbox. No top-level `Tandem` is created.
  - `A.phone` finds no top-level `Tandem`, uses the one under the inbox, and shows Groceries.
- **Invariants:** INV-CONVERGE
- **Notes:** Stalwart's personal namespace prefix is empty. This scenario needs a server or proxy that reports `INBOX.` (outside the vocabulary). The `LIST "" Tandem` shown in §10.3 should read `LIST "" <prefix>Tandem`; the sentence after it says so.

### HB-011 IMAP server without a required extension
- **Kind:** ux
- **Design:** §10.3, §13.1, §13.3
- **Binding:** imap
- **Setup:** An IMAP capability filter strips `UIDPLUS`. Variants strip `IDLE`, or strip `QRESYNC` and `CONDSTORE` together.
- **Steps:**
  1. `J.mac` enters the IMAP host, username and app password.
- **Expect:**
  - Without `UIDPLUS` or `IDLE`: `J.mac` creates no Tandem folder and appends nothing; it shows a named error that the server lacks a feature Tandem needs; local data is untouched.
  - Without `QRESYNC`/`CONDSTORE`: `J.mac` connects normally (they are only recommended) and uses `UID SEARCH ALL` to find removals.
- **Invariants:** INV-UX
- **Notes:** GAP (G-57): §13.3 has no message for a server that lacks a required IMAP extension, or a JMAP session without the mail capability or an EventSource URL. It also doesn't say what happens if a server loses `UIDPLUS` after setup, for example after a server move. The capability filter is a harness addition.

### HB-012 The `n:*` quirk after the newest message is destroyed
- **Kind:** edge
- **Design:** §10.3
- **Binding:** imap
- **Setup:** Tandem holds UIDs 1–50. UID 50 is a re-encrypted duplicate of UID 49 (same writes, higher `H(envelope)`). `A.mac` has synced, so its last known UID is 50.
- **Steps:**
  1. `J.mac` garbage-collects and destroys UID 50.
  2. `A.mac syncs`: `UID FETCH 51:*` returns UID 49.
  3. `A.mac syncs` again with nothing new: `UID FETCH 51:*` again returns UID 49.
- **Expect:**
  - `A.mac` ignores UID 49 in both responses. It fetches no body and doesn't count it as new.
  - `A.mac` records UID 50 as removed.
  - `A.mac`'s last known UID stays 50.
  - `A.mac` publishes nothing.
- **Invariants:** INV-QUIESCE

### HB-013 QRESYNC: removals while away and during IDLE
- **Kind:** edge
- **Design:** §10.3, §10.7
- **Binding:** imap
- **Setup:** QRESYNC is available. `A.mac` knows 30 messages.
- **Steps:**
  1. `A.mac offline`.
  2. `J.mac` publishes merges and garbage-collects 12 of the 30 messages.
  3. `A.mac online`; `A.mac syncs`.
  4. `A.mac` is idle; `J.mac` destroys one more dominated message.
- **Expect:**
  - `A.mac` sends `ENABLE QRESYNC` after authenticating and before `SELECT`.
  - In step 3, `SELECT Tandem (QRESYNC (…))` returns `VANISHED (EARLIER)` with exactly the 12 UIDs, and `A.mac` drops them from its known set.
  - `A.mac` fetches no body it already holds and publishes nothing.
  - In step 4, `A.mac` gets `VANISHED` (not `EXPUNGE`) during IDLE and runs `changes` within 5 s.
- **Invariants:** INV-CONVERGE, INV-QUIESCE

### HB-014 Without QRESYNC: CONDSTORE only, and neither
- **Kind:** edge
- **Design:** §10.3
- **Binding:** imap
- **Setup:** As HB-013, but the capability filter advertises `CONDSTORE` without `QRESYNC`. A variant advertises neither.
- **Steps:** Steps 1–4 of HB-013.
- **Expect:**
  - `A.mac` sends no `ENABLE QRESYNC`.
  - It finds the 12 removals by comparing `UID SEARCH ALL` with its local UID set, and new messages with `UID FETCH <last+1>:*`.
  - In step 4, an `EXPUNGE` during IDLE triggers `changes` within 5 s.
  - The end state equals that of HB-013, with no extra publishes.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** Needs the harness capability filter.

### HB-015 UIDVALIDITY changes
- **Kind:** fault
- **Design:** §10.3, §10.2, §10.4
- **Binding:** jmap+imap (`A.mac` IMAP, `J.mac` JMAP)
- **Setup:** House is fully synced on both devices. The hub holds `N` messages.
- **Steps:**
  1. `A.mac offline`; `J.mac offline`.
  2. The harness copies out every raw message, deletes `Tandem`, recreates it, and re-appends the same raw messages. This gives a new UIDVALIDITY, new UIDs and new JMAP ids.
  3. `A.mac online`; `A.mac syncs`.
  4. `J.mac online`; `J.mac syncs`.
- **Expect:**
  - `A.mac` sees the new UIDVALIDITY and resyncs fully.
  - Both devices match the re-appended messages to ones they already merged by `H(envelope)`. Neither replica changes.
  - Neither device publishes anything. The hub still holds `N` messages.
- **Invariants:** INV-QUIESCE, INV-GC-SAFE
- **Notes:** The folder rebuild is outside the vocabulary. A device that syncs while the folder is empty heals everything. That is harmless, so the scenario keeps devices offline during step 2.

### HB-016 IDLE renewal and events during IDLE
- **Kind:** edge
- **Design:** §10.3
- **Binding:** imap
- **Setup:** `A.mac` is in the foreground and idle on Tandem.
- **Steps:**
  1. `wait 65 min` in real time.
  2. During it, `J.mac adds` an item at minute 10 and at minute 45, and a dominated message is destroyed at minute 50.
- **Expect:**
  - The server log shows `A.mac` ending and re-issuing IDLE at intervals of 29 minutes or less.
  - `A.mac` shows each item within 5 s of its append, triggered by `EXISTS`.
  - The removal at minute 50 triggers `changes`.
  - `A.mac` holds at most one IMAP connection throughout. It leaves IDLE to run commands rather than opening a second connection.
- **Invariants:** INV-CONVERGE
- **Notes:** Must run in real time.

### HB-017 The IMAP connection cap
- **Kind:** fault
- **Design:** §10.3, §13.3, §17
- **Binding:** imap
- **Setup:** Stalwart's cap is 16 concurrent IMAP sessions per account, whatever the address. The harness lowers it to 4 for this run. `J.phone`, `J.mac`, `J.ipad`, `A.phone` and `A.mac` all use IMAP.
- **Steps:**
  1. All five devices launch and stay in the foreground.
  2. `A.phone` shares a URL into House through the share extension while its app is running.
  3. `J.ipad quits`.
- **Expect:**
  - Each running device holds one connection, leaving IDLE to run commands (§10.3).
  - No device shows "Sync access was revoked or changed" because of the cap. The credential is valid.
  - The refused device shows the offline indicator or a named message, and retries no more than once every 30 s.
  - The shared URL reaches the hub, at the latest once a slot is free.
  - Within the refused device's next retry after step 3, all four remaining devices converge.
- **Invariants:** INV-UX, INV-NO-LOSS
- **Notes:**
  - GAP (G-47): §10.3 says a device uses a single connection, but §17 has the share extension append on its own while the app keeps its IDLE connection. One device can therefore need two.
  - GAP (G-41): there is no handling for a refusal because of too many connections (`BYE`, or `NO` with a limit code). It must not be read as a rejected credential, and §13.3 has no message for it. Nothing says a suspended app closes its connection first; suspended iPhones hold slots until the server times them out.
  - The 30 s retry bound is the harness's choice; the design gives none.

### HB-018 Mixed bindings on one account
- **Kind:** real-world
- **Design:** §4, §10.2, §10.3, §10.4, §10.7
- **Binding:** jmap+imap (`J.phone`, `J.mac` JMAP; `A.phone`, `A.mac` IMAP)
- **Setup:** House is owned by J, with A as a writer. All four devices are in the foreground.
- **Steps:**
  1. `J.phone adds Milk to House`.
  2. `A.mac sets Milk.notes = "2 l"`.
  3. `A.mac deletes Bread`.
  4. All devices sync and garbage-collect.
- **Expect:**
  - `A.mac` shows Milk within 5 s of step 1, through an IDLE `EXISTS`.
  - `J.phone` shows the note within 5 s of step 2, through a `StateChange`.
  - For every message, the `H(envelope)` computed by `J.phone` (from `bodyValues`) equals the one computed by `A.mac` (from `BODY[TEXT]`).
  - JMAP devices destroy dominated messages that IMAP devices appended, and the IMAP devices see them vanish.
  - After quiescence, each record has one message on the hub.
- **Invariants:** INV-CONVERGE, INV-QUIESCE, INV-GC-SAFE, INV-HUB-BLIND

### HB-019 One account, two descriptors: what counts as a different hub
- **Kind:** edge
- **Design:** §4, §11.2, §13.1, §13.3, §1.2
- **Binding:** jmap+imap (J on JMAP with token `Tj`; `A.mac` on IMAP with an app password)
- **Setup:** Both point at the same Stalwart account. A is already a member of Groceries. J owns House.
- **Steps:**
  1. `J.mac invites link to House as writer`, choosing "Use a separate hub token for this invite" (JMAP token `Ta2`).
  2. `A.mac joins via` the link.
- **Expect:**
  - `A.mac` doesn't show "This invite is for a different sync hub".
  - `A.mac` shows House within one sync of J's approval.
  - Groceries is unaffected.
  - `A.mac` uses exactly one credential afterwards, and the harness records which one.
- **Invariants:** INV-CONVERGE, INV-UX
- **Notes:** GAP (G-13): §11.2 step 1 and §13.3 compare hubs, but nothing defines hub identity. The same account can appear as a JMAP descriptor and an IMAP descriptor (§4), or with different per-person tokens (§13.1). Comparing descriptors rejects this invite. Comparing hosts would accept an invite for a different account with the same provider (every Fastmail account shares one session URL). The design also doesn't say whether a device that receives a new token for its own hub keeps its old one or switches.

### HB-020 Bodies stored in other charsets and transfer encodings
- **Kind:** fault
- **Design:** §10.2, §10.3, §10.4
- **Binding:** any
- **Setup:** House is followed by `J.mac` and `A.mac`. Records Milk, Bread, Eggs, Jam and Tea each have one message.
- **Steps:**
  1. `hub: rewrite charset <Milk> as UTF-16` (with BOM, `base64` transfer encoding).
  2. `hub: rewrite charset <Bread> as UTF-16LE` (no BOM, `charset=utf-16le`).
  3. `hub: rewrite charset <Eggs> as quoted-printable` (`us-ascii`, soft breaks, `=` padding as `=3D`).
  4. `hub: rewrite charset <Jam> as base64` (transfer encoding over the base64 text).
  5. `hub: rewrite charset <Tea> as 8bit` (bare LF line ends, lines wrapped at 64, no final newline).
  6. `J.mac syncs`; `A.mac syncs`; `J.mac pairs J.ipad`; `J.ipad syncs`.
- **Expect:**
  - Every device merges all five messages.
  - Each message's `H(envelope)` equals its value before the rewrite.
  - No device logs any of them as undecryptable or republishes any of the five records.
  - `J.ipad` shows all five records.
- **Invariants:** INV-CONVERGE, INV-QUIESCE
- **Notes:** The harness rewrites by appending the re-encoded raw message first and destroying the original second, so no device sees the record missing in between.

### HB-021 A charset the reader can't decode
- **Kind:** fault
- **Design:** §10.2 (`isEncodingProblem`), §10.4, §12.2, §10.7
- **Binding:** jmap+imap (`J.mac` JMAP, `A.mac` IMAP)
- **Setup:** House has Milk and Bread, one message each.
- **Steps:**
  1. `hub: rewrite charset <Milk> as UTF-7`, which turns each `+` in the base64 text into `+-`.
  2. `hub: rewrite charset <Bread> as x-unknown`, with the bytes left as ASCII.
  3. Both devices sync. `wait 1 hour`.
- **Expect:**
  - Where Stalwart flags `isEncodingProblem`, `J.mac` downloads the raw message by `blobId` and decodes it as `A.mac` does.
  - `J.mac` and `A.mac` give the same verdict on each message: both merge it or both ignore it.
  - After quiescence, the hub holds at most two messages each for Milk and Bread, and no device keeps republishing them.
- **Invariants:** INV-QUIESCE, INV-CONVERGE
- **Notes:** GAP (G-48): step 2 of §10.4 still doesn't list the charsets a reader must decode, or say what to do with a body in a charset it can't decode. A device that can't read the message sees the record as missing and heals it; on a server that re-encodes every JMAP body, the healed message comes back in the same charset and the loop repeats. Possible fix: a fixed list of charsets (us-ascii, utf-8, utf-16 with LE/BE, iso-8859-1), with unreadable bodies blocking healing of that channel the way §15 does.

### HB-022 Two `Tandem-Channel` headers
- **Kind:** adversarial
- **Design:** §10.2, §10.3, §10.4, §5.4, §10.7, §12.2
- **Binding:** jmap+imap (`J.mac`, `J.phone` JMAP; `A.mac` IMAP)
- **Setup:** House (tag `T`). Milk is in one message `m`. E has the hub credential.
- **Steps:**
  1. E builds `m*`: `m`'s raw bytes plus a second `Tandem-Channel` header with a random tag, placed before the real one. `hub: inject m*`, then `hub: destroy record <Milk>`. This removes `m`.
  2. All devices sync. `wait 1 hour`.
- **Expect:**
  - All three devices give the same verdict on `m*`: either all read it under `T`, or all ignore it.
  - Milk is unchanged on all devices.
  - After quiescence, the hub holds at most two messages for Milk, and no device publishes Milk more than once.
- **Invariants:** INV-QUIESCE, INV-CONVERGE, INV-GC-SAFE
- **Notes:** GAP (G-58): §10.4 takes "the `Tandem-Channel` value" without saying what to do with several.
  - JMAP's `header:Tandem-Channel:asText` returns the last instance (RFC 8621 §4.1.3). An IMAP `HEADER.FIELDS` fetch returns both, and a reader that takes the first one fails AEAD under the wrong tag.
  - The IMAP device then sees Milk missing and heals it.
  - The JMAP devices see the healed message and `m*` holding the same writes, so they dominate each other. If `m*` has the lower `H(envelope)`, they destroy the healed one. The IMAP device heals again, and the loop has no end.
  - Possible fix: in both bindings, a message without exactly one `Tandem-Channel` header is not a Tandem message. A related question: tags are lowercase `b32`, but whether the header is compared case-sensitively is unstated.

### HB-023 Envelope wrapped in multipart or a non-text part
- **Kind:** adversarial
- **Design:** §10.2, §10.3, §10.4, §12.2
- **Binding:** jmap+imap (`J.mac` JMAP, `A.mac` IMAP)
- **Setup:** House; Milk and Bread have one message each.
- **Steps:**
  1. E re-wraps Milk's base64 envelope text as one `text/plain` part of a `multipart/alternative` message, `hub: inject`s it, and destroys the original.
  2. E re-wraps Bread's base64 envelope text in an `application/octet-stream` body with `base64` transfer encoding, `hub: inject`s it, and destroys the original.
  3. Both devices sync. `wait 1 hour`.
- **Expect:**
  - `J.mac` and `A.mac` give the same verdict on each injected message.
  - After quiescence, the hub holds at most two messages each for Milk and Bread.
- **Invariants:** INV-QUIESCE, INV-CONVERGE
- **Notes:** GAP (G-58): §10.4 doesn't require a single-part `text/plain` body.
  - In step 1, JMAP's `fetchTextBodyValues` returns the `text/plain` part, which decodes to an envelope. IMAP's `BODY[TEXT]` is the whole multipart body, boundaries included, and doesn't decode.
  - In step 2 it is the other way round: JMAP's `textBody` is empty, while the IMAP reader undoes `base64` and gets the envelope.
  - Either way the two bindings disagree, which leads to the heal and destroy loop of HB-022. Both readers should ignore any message that isn't single-part `text/plain`.

### HB-024 Byte-identical copies of one envelope
- **Kind:** edge
- **Design:** §10.2, §10.4, §10.7
- **Binding:** jmap+imap (`J.mac` JMAP, `A.mac` IMAP)
- **Setup:** House; Milk is in one message `m`, made by `A.mac`.
- **Steps:**
  1. `hub: replay m` twice, making copies `m2` and `m3` with identical raw bytes.
  2. `J.mac syncs`; `A.mac syncs` at the same moment. Both garbage-collect.
  3. Variant: `A.mac` is gone for good before step 2.
- **Expect:**
  - `J.mac` destroys none of the copies: no hash can tell it which to keep.
  - `A.mac`, which made the envelope, removes two copies and keeps one.
  - At every moment, at least one copy is on the hub (harness polls every 100 ms).
  - No device republishes Milk.
  - Variant: all three copies stay until a later write to Milk dominates them; they are then destroyed as ordinary dominated messages.
- **Invariants:** INV-GC-SAFE, INV-QUIESCE

### HB-025 Two devices publish the same merge
- **Kind:** real-world
- **Design:** §10.7
- **Binding:** any
- **Setup:** `J.mac` and `A.mac` follow House. Milk is in two messages that neither dominates: `m1` (J set the title) and `m2` (A set notes).
- **Steps:**
  1. `net: latency 6000 ms` on both devices' appends, so neither sees the other's merge within its random delay.
  2. Both devices see `m1` and `m2` and each publishes a merge: `n1` and `n2`.
  3. Latency is removed; both devices sync and garbage-collect.
- **Expect:**
  - `n1` and `n2` hold the same write IDs and dominate each other.
  - `m1` and `m2` are destroyed.
  - Of `n1` and `n2`, only the one with the greater `H(envelope)` is destroyed, by either device; the other remains.
  - At every moment the hub holds a set of messages that together dominate Milk (harness polls).
  - Neither device publishes Milk again.
- **Invariants:** INV-GC-SAFE, INV-QUIESCE

### HB-026 Concurrent publishers back off; typing is debounced
- **Kind:** real-world
- **Design:** §10.7
- **Binding:** any
- **Setup:** `J.phone`, `J.mac`, `J.ipad`, `A.phone` and `A.mac` all follow House and are in the foreground.
- **Steps:**
  1. `J.mac offline`; `A.mac offline`. Each `sets Milk.notes`. Both come `online`.
  2. All devices sync. Repeat 100 times.
  3. `J.mac` types 20 characters into Milk.notes over 3 s.
- **Expect:**
  - Each device that publishes a merge in step 2 does so between 0 and 5 s after it first saw the two messages, and only if no dominating message appeared in that time.
  - Over the 100 runs, the mean number of merge messages per run is below 2.
  - After GC each run, Milk has one message.
  - Step 3 produces at most 4 publishes of Milk.
- **Invariants:** INV-QUIESCE, INV-CONVERGE
- **Notes:** "A few seconds at most" is read as 5 s.

### HB-027 Garbage collection across epochs, and what is never pruned
- **Kind:** edge
- **Design:** §10.7, §11.6 (S6), §12.2
- **Binding:** any
- **Setup:** House is at `E3`. J is owner (`J.mac`), A and B are writers.
- **Steps:**
  1. `J.mac removes B`. House moves to `E4`; `A.mac` moves and holds both keys.
  2. Before `J.mac` reaches S6, `A.mac` garbage-collects.
  3. `J.mac` finishes S6; `wait 40 days`; all sync.
- **Expect:**
  - `A.mac` may destroy a channel-`E3` data message that a channel-`E4` message dominates: every write of it is in channel `E4` or superseded there, so nothing is lost.
  - Channel `E3`'s `_roster` is never destroyed, by `A.mac` or by S6.
  - Channel `E4`'s inventory fragments are never destroyed as dominated.
  - After step 3, channel `E3` holds only its `_roster`; it stays for good.
- **Invariants:** INV-GC-SAFE
- **Notes:** §10.7 compares messages "of the same record" in any channel. Because record and write IDs survive a key change, that is safe here; the scenario pins it down.

### HB-028 A record split across messages
- **Kind:** edge
- **Design:** §10.6, §10.7, §12.2, Q16
- **Binding:** any
- **Setup:** `J.mac` owns House with 40 members; after 200 changes the roster passes 64 KiB and is published in several messages. `A.mac` follows House.
- **Steps:**
  1. Both devices sync.
  2. `wait 10 min` with no edits.
  3. J admits one more member. Both sync. `wait 10 min`.
- **Expect:**
  - Within 1 minute of the last edit, neither device publishes `_roster` again.
  - The `_roster` messages on the hub together hold every statement.
  - The total size of the `_roster` messages is at most twice the size of the roster.
- **Invariants:** INV-QUIESCE, INV-GC-SAFE
- **Notes:** GAP (G-40, G-43):
  1. §10.6 doesn't say whether "doesn't fit" means 64 KiB (SHOULD) or 1 MiB (MUST), what the size counts, nor how a record is split. Two devices split the same record differently.
  2. §10.7's trigger, "messages for one record that none of them dominates", is always true of a record split into parts. Read literally, every online device republishes it after its random delay, again and again.
  3. GC compares one message with one message. A part is collected only when a single message holds all its writes, so copies split differently are never collected.
  - Domination should be by a set of messages, as §12.2 already uses ("taken together"), with a deterministic split.

### HB-029 A hub participant destroys a whole channel
- **Kind:** adversarial
- **Design:** §12.2, §10.7, §1.2
- **Binding:** any
- **Setup:** House is at `E2` with 200 records, 20 of them tombstoned. `J.phone`, `J.mac`, `J.ipad`, `A.phone` and `A.mac` are in the foreground.
- **Steps:**
  1. `hub: destroy channel <House E2>`.
  2. All devices sync. `wait 10 min`.
- **Expect:**
  - Within one sync, the hub again holds messages that together dominate every device's state of every House record, the roster and inventory fragments included.
  - Tombstoned records stay deleted.
  - Nothing is appended to any other channel.
  - After quiescence, each record has one message.
  - The harness records the total number of appends during healing; a design-conformant client may make up to 5 × 201.
- **Invariants:** INV-GC-SAFE, INV-QUIESCE, INV-NO-LOSS
- **Notes:** GAP (G-59): healing (§12.2) has no random delay and no "unless a dominating message has appeared" check, unlike concurrent publishing (§10.7). After a mass deletion, every online replica republishes every record at once, and GC then removes four copies out of five. On a server with rate limits or a nearly full quota (FT-029), that burst can itself fail. Applying §10.7's delay rule to healing would fix this.

### HB-030 The hub rolls a record back
- **Kind:** adversarial
- **Design:** §12.2, §3.2, §9.2
- **Binding:** any
- **Setup:** Milk's title was "Milk" in message `m1`, then "Oat milk" in `m2`. `m2` dominated `m1`, and `m1` was destroyed. E captured `m1`. `J.ipad` hasn't been paired yet.
- **Steps:**
  1. `hub: withhold <m2> from A.mac`.
  2. `hub: destroy record <Milk>`; `hub: replay m1`.
  3. All devices sync.
  4. `J.mac pairs J.ipad`; `J.ipad syncs`.
- **Expect:**
  - Devices that held "Oat milk" keep it; `m1` doesn't override it.
  - Within one sync, at least one device republishes Milk, because `m1` doesn't dominate its state.
  - After GC, `m1` is gone and one message shows "Oat milk".
  - `J.ipad` shows "Oat milk".
- **Invariants:** INV-CONVERGE, INV-GC-SAFE

### HB-031 An emptied old channel isn't refilled
- **Kind:** edge
- **Design:** §12.2, §11.6, §8.3
- **Binding:** any
- **Setup:** House moved from `E2` to `E3` when B was removed; `J.mac` finished S6, so channel `E2` holds only its `_roster`. `J.ipad` was offline from before the change and holds unsynced `E2` edits to Milk.
- **Steps:**
  1. A month later: `J.ipad online`; `J.ipad syncs`.
- **Expect:**
  - `J.ipad` reads the announcement in channel `E2`'s `_roster` and appends no data under the `E2` tag (a channel whose `_roster` shows a later epoch is never refilled).
  - It moves to `E3`, using the key in J's home, and signs its Milk edits again into `E3`.
  - Other devices show the edits.
- **Invariants:** INV-NO-LOSS, INV-MONOTONE-EPOCH, INV-CUT

### HB-032 The old roster destroyed under an offline member
- **Kind:** adversarial
- **Design:** §12.2, §11.6, §11.4, §3.3
- **Binding:** any
- **Setup:** House is at `E4`. J is owner (`J.mac`), A and B are writers. A's only device, `A.phone`, is offline and has unsynced edits.
- **Steps:**
  1. `J.mac removes B`. House moves to `E5`, and the grant for A sits in A's inbox.
  2. `hub: destroy` channel `E4`'s `_roster` messages; `hub: destroy grants for A`.
  3. Variant a: `J.mac` syncs before `A.phone` comes online. Variant b: `A.phone online` first, with `J.mac` offline for a day.
  4. All sync.
- **Expect:**
  - Variant a: `J.mac` restores channel `E4`'s `_roster` (devices that moved on keep it complete, §12.2). `A.phone` reads the announcement, waits 10 minutes, sends a key request, and gets `K(E5)`.
  - Variant b: `A.phone` finds no `_roster` in channel `E4`: it doesn't heal there, looks in home and inbox, and sends a key request. It holds its edits until `J.mac` answers.
  - In both, `A.phone` ends in `E5` with its edits signed again there, and nothing it writes after step 3 is readable with `K(E4)` alone.
- **Invariants:** INV-CUT, INV-MONOTONE-EPOCH, INV-NO-LOSS
- **Notes:** In variant b, `A.phone` publishing to `E4` before it learns of `E5` would expose its edits to B (§3.3); the "no `_roster`, no healing" rule prevents it.

### HB-033 Home, inbox and invite channel destroyed
- **Kind:** adversarial
- **Design:** §12.2, §11.2, §11.4, §3.3
- **Binding:** any
- **Setup:** J owns House. B has a pending link request on J's invite. All of J's devices have quit.
- **Steps:**
  1. `hub: destroy` J's home channel, J's inbox and the invite channel.
  2. `J.mac launches`; `B.phone` stays in the foreground, waiting.
- **Expect:**
  - `J.mac` re-publishes J's home within one sync.
  - Desired: B's request reaches J, or B learns it has to ask again.
- **Invariants:** INV-NO-LOSS, INV-UX
- **Notes:** GAP (G-49): healing (§12.2) covers current collection channels and home only. A destroyed join request is posted again by neither side: the joiner waits until the invite expires and the owner never sees it. §3.3 lists deleting grants as vandalism that key requests repair, but nothing repairs a deleted join request; the same holds for a pairing request or reply.

### HB-034 Moving to a new hub
- **Kind:** real-world
- **Design:** §12.4, §12.2, §13.1, §11.2, §11.4, §1.2
- **Binding:** any
- **Setup:** Hub H1 serves J (three devices), A (two devices) and B (`B.phone` only), with House shared by J, A and B, and Groceries shared by A and J. While `B.phone` was offline, House moved from `E1` to `E2` when C was removed. B's grant for `E2` is still unconsumed in B's inbox on H1. Hub H2 is a new Stalwart account.
- **Steps:**
  1. J restores onto H2 as an explicit act on `J.mac`.
  2. `J.phone`, `J.ipad`, `A.mac` and `A.phone` scan `J.mac`'s hub QR.
  3. `B.phone online` on H1, then it scans the H2 hub QR.
  4. All devices sync.
- **Expect:**
  - H2 holds every home and each collection's current channel with its roster.
  - Nothing is appended to H2 under a superseded epoch tag.
  - `B.phone` finds no `_roster` in channel `E1` on H2, pauses healing there, asks J for a key, and ends in House `E2`, converged with J and A.
  - No device sets up a new identity or a new collection.
- **Invariants:** INV-CONVERGE, INV-MONOTONE-EPOCH, INV-NO-LOSS, INV-CUT
- **Notes:**
  - GAP (G-17, G-13): there is no flow for replacing the hub on a device that already has one. "One hub per client" (§1.2) and §11.2's "different hub" error leave `B.phone` unable to accept anything that carries H2, and §13.1's connect sets up an identity and uploads local data as new collections. Nothing says what a device resets when its hub changes: cursors, UIDs, held grants.
  - The switch-hub step is outside the vocabulary.

### HB-035 A newer version in a channel
- **Kind:** edge
- **Design:** §15, §12.2, §10.7, §11.6, §11.9
- **Binding:** any
- **Setup:** House is at `E2`, and Groceries exists. `J.mac` and `A.mac` both run v1.
- **Steps:**
  1. `hub: inject` a message under House's tag with `Tandem-Version: 2` and envelope version byte 2. This stands in for a v2 client.
  2. `J.mac sets Milk.title = "Oat milk"`.
  3. `hub: destroy record <Bread>` in House, and the same in Groceries.
  4. Both devices sync.
  5. `J.mac` removes A from House and later deletes Groceries.
- **Expect:**
  - Neither device destroys the version-2 message.
  - Both devices tell the user an update is available.
  - Neither device destroys any House `E2` message, including Milk's dominated one, and neither republishes Bread in House.
  - The new Milk message is published.
  - Groceries heals Bread and garbage-collects normally.
  - After step 5, the harness records whether channel `E2`'s data is cleaned up in S6.
- **Invariants:** INV-GC-SAFE, INV-UX
- **Notes:** GAP (G-50): §15 forbids healing and GC in a channel holding such a message. It doesn't say whether that also covers S6's cleanup of an old epoch (§11.6), the cleanup after a collection is deleted (§11.9), or destroying handled requests in an invite channel. If it does, one v2 message keeps an old channel on the hub for ever.

### HB-036 A forged version freezes healing
- **Kind:** adversarial
- **Design:** §15, §12.2, §1.2, §5.4, §10.2, §10.3, §10.4
- **Binding:** any
- **Setup:** House has 50 records. `J.mac` and `A.mac` follow it. E holds the hub credential only.
- **Steps:**
  1. `hub: inject` under House's current tag a message with `Tandem-Version: 2` and a body of `TNDM`, `0x02` and random bytes.
  2. `hub: destroy channel <House>` except the injected message.
  3. Both devices sync; `J.mac pairs J.ipad`; `J.ipad syncs`.
  4. Variants: header 2 with envelope byte 1; header 1 with envelope byte 2.
- **Expect:**
  - Within one sync, House's records are back on the hub. §1.2 and §12.2 promise that vandalism is a passing nuisance while a member device survives.
  - `J.ipad` shows all 50 records.
  - In each variant of step 4, all devices give the same verdict on whether the message is version 1.
- **Invariants:** INV-GC-SAFE, INV-NO-LOSS
- **Notes:**
  - GAP (G-60): a v1 client can't authenticate a version-2 message, so §15 gives any hub participant a permanent off switch for healing and GC. One forged message per channel tag (tags are visible on the hub), plus destroying the rest, leaves the data only on replicas, and new devices see an empty House. Possible fixes: a v2 epoch must be announced in the v1 log before v2 messages under a v1 tag count, or v2 uses tags v1 clients never follow.
  - GAP (G-61): §15 names both the `Tandem-Version` header and the envelope byte, but neither binding fetches the header (§10.2 `changes`/`fetch`, §10.3 `fetch`). Which one decides when they disagree is unstated.

### HB-037 Malformed and junk messages
- **Kind:** adversarial
- **Design:** §5.4, §5.6, §10.4, §10.7, §12.2, §15
- **Binding:** any
- **Setup:** `J.mac` and `A.mac` follow House (tag `T1`) and Groceries (tag `T2`).
- **Steps:** E injects the following under `T1` with `hub: inject`:
  1. (a) A body that isn't base64.
  2. (b) Base64 of `XXXX` and 100 random bytes (wrong magic).
  3. (c) A `TNDM`, version-1 envelope truncated to 20 bytes.
  4. (d) Kind 1 with a random salt and ciphertext.
  5. (e) Version 1 with kind 9.
  6. (f) Kind 2 bytes.
  7. (g) A real Groceries message whose header has been changed to `T1`.
  8. (h) A message with no `Tandem-Channel` header.
  9. (i) An ordinary HTML email from a person, moved into Tandem.
  10. Then `hub: destroy record <Milk>`. Both devices sync.
- **Expect:**
  - No device crashes or applies anything from (a)–(i).
  - Each device logs (a)–(i) at `.error` with a message identity and no content.
  - (g) isn't merged into House; the AEAD check fails.
  - No device destroys any of (a)–(i).
  - House and Groceries are the same on both devices.
  - Milk heals within one sync.
- **Invariants:** INV-CONVERGE, INV-AUTHZ, INV-SIGNED
- **Notes:**
  - GAP (G-19): §10.7 lets a device destroy only messages it has merged. Junk a device can prove is junk stays for ever. A version-1 envelope under a collection tag that fails AEAD is certainly junk, since the tag derives from the key. That is not so in an inbox, where a message may be sealed to a user key the device hasn't received yet. E can fill the quota this way (FT-030).
  - GAP (G-62): §5.4 reserves the unused kinds but doesn't say how a version-1 envelope with one of them, (e), is treated: as junk (heal and GC go on) or as "a version it doesn't understand" (§15, both stop).

### HB-038 An oversized message
- **Kind:** adversarial
- **Design:** §10.6, §10.2, §10.3, §10.4
- **Binding:** any
- **Setup:** `J.phone` follows House. House holds normal messages.
- **Steps:**
  1. `hub: inject` a 50 MiB message under House's tag, with valid headers and a base64 garbage body.
  2. `hub: inject` a 1.6 MiB message under House's tag: a real 800 KiB message re-encoded as UTF-16.
  3. `J.phone syncs`.
- **Expect:**
  - `J.phone` doesn't download the 50 MiB body. The harness proxy counts at most 4 MiB transferred for it.
  - `J.phone` merges the UTF-16 message.
  - `J.phone` finishes the sync and stays responsive.
  - `J.phone` logs the skip at `.error`.
- **Invariants:** INV-CONVERGE
- **Notes:** GAP (G-63): §10.6 limits writers (a message MUST stay under 1 MiB) but sets no limit for readers. Read literally, `fetch` downloads and decodes any body in memory, so a hub participant can stall or crash every device following a channel. The limit can't simply be 1 MiB of stored message either: a server that re-encodes a legitimate body as UTF-16 roughly doubles it. The limit should apply to the decoded envelope, or allow for re-encoding, and be checked against JMAP `size` or IMAP `RFC822.SIZE` before fetching.

### HB-039 What the hub holds
- **Kind:** adversarial
- **Design:** §14, §10.4, §5.6, §3.2
- **Binding:** jmap+imap (J's devices JMAP, A's and B's IMAP)
- **Setup:** J, A and B use all their devices. The harness knows every plaintext title, note, collection name, display name, device name, user ID, device ID, collection ID, record ID, invite ID and epoch-start ID used.
- **Steps:**
  1. Run a scripted day: create House, add 50 items, share it with A and B, pair `J.ipad`, remove B, delete items.
  2. The harness dumps every raw message (IMAP `BODY.PEEK[]` and JMAP `Email/get` with all headers), every mailbox name and keyword, and the server's session logs (IMAP `ID`, HTTP `User-Agent`).
- **Expect:**
  - None of the known values appear in any header, body, mailbox name or keyword, as plaintext, hex, `b64u`, `b32` or base64. Any 8-byte window of an ID counts.
  - Every message has exactly the headers of §10.4, plus any the server adds.
  - Every `Subject` is `tandem`, and every `From` is `Tandem <tandem@invalid>`.
  - `Message-ID`s are unique and contain no ID.
  - The only flag or keyword set is `\Seen` / `$seen`.
  - No client sends a device name or user name in IMAP `ID` or `User-Agent`.
  - The harness records the distribution of `Date` minus server receive time for each device.
- **Invariants:** INV-HUB-BLIND
- **Notes:** GAP (G-51): §10.4 says "the hub already knows the append time", but `Date` is written from the device's clock, not the hub's. `Date` minus the server's receive time (`INTERNALDATE`/`receivedAt`) is a steady offset for each device. It tells apart devices that share a credential and an address, and it links one device's messages across networks. That is more than §14 admits. A device with a clock years off (FT-022) is obvious. Possible fix: write a fixed `Date`.

### HB-040 A mail client tidies the Tandem folder
- **Kind:** real-world
- **Design:** §10.1, §10.2, §10.3, §12.2, §10.7, §11.4, §13.1
- **Binding:** any
- **Setup:** J and A have devices on House. A grant for `A.phone` is unconsumed in A's inbox. A mail client is open on the account.
- **Steps:**
  1. In the mail client: mark all Tandem messages unread, flag 10, add a keyword or label, and open 5.
  2. All devices sync.
  3. Select all messages in Tandem and move them to `Archive` (`hub: move … out of Tandem`).
  4. All devices sync. `wait 7 days`.
  5. Move them all back into Tandem.
  6. All devices sync.
- **Expect:**
  - Step 2: no Tandem device fetches a body, publishes, heals or destroys anything.
  - Step 4: every record in a current channel, every home and every roster a device holds is republished into Tandem within one sync. Nothing in `Archive` is touched.
  - Step 4: `A.phone` gets a new grant through a key request, 10 minutes or more after the move.
  - Step 6: duplicates collapse through GC to one message per record.
  - Old-epoch messages that came back in step 5 aren't treated as current.
- **Invariants:** INV-QUIESCE, INV-GC-SAFE, INV-MONOTONE-EPOCH
- **Notes:** Flag changes from a mail client are outside the vocabulary.

### HB-041 A mail client renames or deletes the Tandem folder
- **Kind:** real-world
- **Design:** §10.1, §10.2, §10.3, §13.1
- **Binding:** jmap+imap (`J.mac` JMAP, `A.mac` IMAP)
- **Setup:** Both devices are synced on House.
- **Steps:**
  1. In a mail client, rename `Tandem` to `Old sync stuff`.
  2. `J.mac adds Eggs to House`; `A.mac adds Jam to House`.
  3. Both devices sync.
  4. Variant: in step 1, delete the `Tandem` folder (and its messages) instead.
- **Expect:**
  - After step 3, all Tandem messages either device appends are in one mailbox, and both show Eggs and Jam.
  - Variant: both devices heal into a single new `Tandem`, and both show Eggs and Jam.
- **Invariants:** INV-CONVERGE, INV-NO-LOSS
- **Notes:** GAP (G-52): nothing says whether the Tandem mailbox is resolved once and cached (a JMAP id, an IMAP name) or resolved again on each sync. Nothing covers a rename or deletion either.
  - A JMAP client that caches the id keeps appending to `Old sync stuff`. A rename changes no Email state, and the EventSource only watches `types=Email`.
  - An IMAP client fails `SELECT Tandem`, creates a new one, and heals into it. The hub is split in two, and neither side notices.
  - After a deletion, `Email/set` with a destroyed mailbox id fails. The design doesn't say to run `ensureFolder` again.
  - Renaming and deleting folders are outside the vocabulary.

### HB-042 Two messages that dominate each other with different contents
- **Kind:** edge
- **Design:** §10.7
- **Binding:** jmap+imap (`J.mac` JMAP, `A.mac` IMAP)
- **Setup:** Milk's title has writes `x` and `y`, where `y` supersedes `x`. The hub holds `m = {x, y}` and `n = {y}`. Both devices have merged both.
- **Steps:**
  1. `J.mac` and `A.mac` garbage-collect at the same moment.
- **Expect:**
  - `m` and `n` dominate each other. Both devices destroy only the one with the greater `H(envelope)`; the other remains.
  - At every moment, a message holding `y` is on the hub (harness polls every 100 ms).
  - Control run under the earlier rule (tie-break only for identical write sets): each device may destroy one, leaving none; the harness shows `y` lost.
- **Invariants:** INV-GC-SAFE
- **Notes:** The Lean `GC` theorem proves the general case: under the frozen rule, "may destroy `m` on account of `n`" is a strict order, so the top of every chain survives, even on stale views.

### HB-043 A push that beats the answer to an append
- **Kind:** fault
- **Design:** §10.2, §10.4
- **Binding:** jmap
- **Setup:** `J.mac` follows House with an open EventSource. `net: latency 2000 ms` on `J.mac`'s responses only.
- **Steps:**
  1. `J.mac adds Milk to House`.
- **Expect:**
  - The `StateChange` arrives before `Email/set` returns; `J.mac` runs `changes`, finds a message whose `H(envelope)` is its own, and treats Milk as published.
  - When the `Email/set` answer arrives, nothing is appended again or fetched twice.
- **Invariants:** INV-UNIQUE, INV-QUIESCE

### HB-044 `invalidArguments` from `Email/changes`
- **Kind:** fault
- **Design:** §10.2
- **Binding:** jmap
- **Setup:** `J.phone` last synced at Email state `s0`. The harness makes Stalwart answer `invalidArguments` to `Email/changes` from `s0` (as it does for a state it can no longer use).
- **Steps:**
  1. `J.phone syncs`.
- **Expect:**
  - `J.phone` treats the error like `cannotCalculateChanges` and does a full resync (HB-007).
- **Invariants:** INV-CONVERGE

### HB-045 Inventory fragments and the statements that prove a death are kept
- **Kind:** edge
- **Design:** §10.7, §12.2, §9.4
- **Binding:** any
- **Setup:** House moved to `E4` with a three-fragment inventory. Separately, Trip is dead and its two conflicting statements are in its current channel's `_roster`.
- **Steps:**
  1. `J.mac` writes 200 edits to House; all devices garbage-collect.
  2. `hub: destroy` one House fragment and one of Trip's two statements.
  3. All devices sync.
- **Expect:**
  - Step 1: no device destroys a fragment as dominated, however much later data there is.
  - Step 3: devices that hold them restore the fragment and the statement.
- **Invariants:** INV-GC-SAFE

### HB-046 A discarded draft's channel is the one roster copy destroyed
- **Kind:** edge
- **Design:** §10.7, §11.7
- **Binding:** any
- **Setup:** House is at `E3`. `J.phone`'s unfinished change left channel `E4'` with inventory fragments, some records and the roster ending in a draft.
- **Steps:**
  1. J discards the change on `J.mac`.
- **Expect:**
  - `J.mac` destroys every message in channel `E4'`, the roster included.
  - No other channel's `_roster` is touched; no device heals channel `E4'` afterwards.
- **Invariants:** INV-GC-SAFE

### HB-047 Channel tags are compared byte for byte
- **Kind:** adversarial
- **Design:** §10.4, §5.6
- **Binding:** any
- **Setup:** House's tag is a lower-case `b32` string `t`.
- **Steps:**
  1. E copies a real House message and changes its `Tandem-Channel` to `t` in upper case.
  2. E adds a folded header (`Tandem-Channel:` with `t` on a continuation line, with surrounding spaces).
- **Expect:**
  - Step 1's message is under another tag: no device follows it or merges it.
  - Step 2's message is unfolded and trimmed, matches `t`, and is merged (it is a real House envelope).
- **Invariants:** INV-CONVERGE
