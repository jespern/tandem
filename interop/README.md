# Native end-to-end tests

`interop.py` drives the real Go and Swift device engines in separate processes,
with separate SQLite databases, installation keys and blob stores. They exchange
messages through the SDK IMAP bindings. Stalwart is disposable; Fastmail
uses fresh, isolated folders in the configured account. No SMTP is used.

From the repository root, with Go 1.26.4+, Swift 6.4 and Docker available:

```sh
python3 -B -m unittest discover -s interop -q
python3 -B interop/interop.py --files --race --report .build/stalwart-files.json
```

With no engine arguments, every ordered pair runs: Swift/Swift, Swift/Go,
Go/Swift and Go/Go. Two arguments select an ordered pair, founder first; one
selects that engine alone. Omit `--files` to include sharing, pairing, key
changes, copying, record collection and tombstone packing. `--scenario NAME`
selects a case and can be repeated. `--timeout SECONDS` sets the safety deadline
for each driver command, default 120 seconds. Waiting uses pipe readiness and
engine events, with no polling or sleep-based assertions.

The Go/Stalwart file suite runs with the race detector in
[CI](../.github/workflows/files.yml). The four-pair matrix additionally needs a
Mac with Docker for the Swift engine. Fastmail is an explicit live run, separate
from the credential-free CI job.

## Fastmail

Use the same credential files as `probes/blob_probe.py --fastmail`:

- `~/.config/tandem/fastmail.json`: the API token, as raw text, used only to
  discover the account username from the JMAP session endpoint.
- `~/.config/tandem/fastmail-imap.json`: the IMAP app password, as raw text.

Tokens and passwords are never put on a command line or in reports. IMAP uses
certificate validation; trusting the fixture certificate is restricted to
Stalwart. The run creates `Tandem-interop-<random>` and its `-blobs` folder,
closes its drivers, clears only those exact folders, deletes them and checks they
are gone. A cleanup failure fails the case and prints the folder to remove.
Other folders and messages are not touched. Random test data and fixture-only
keys are removed with the temporary device directories, even after failures.

```sh
python3 -B interop/interop.py go swift --files --provider fastmail \
  --report .build/fastmail-go-swift.json
python3 -B interop/interop.py swift go --files --provider fastmail \
  --report .build/fastmail-swift-go.json
```

Repeat `--provider` to run the selected matrix against both providers. An absent
credential or unavailable provider is an error, never a skipped passing test.
Reports include provider, engine pair, scenario, elapsed time and outcome; elapsed
time includes setup and cleanup, so it is not a file-transfer benchmark.

## File coverage

| Scenario | What is checked |
|---|---|
| `blobs` | Two-way files, inline content, missing-chunk healing and replacement across engines. |
| `file_boundaries` | Empty files; 1-byte, inline and chunk boundaries; binary data and Unicode names; a file spanning several batches; byte-exact provider storage. |
| `file_resume` | A real partial upload followed by process kill; durable staged content and keys; existing messages reused without duplicates; downloaded content survives receiver restart. |
| `file_lost_ack` | The server accepts a chunk but its answer is discarded; process restart and retry preserve that message. |
| `file_pending_replacement` | Partial replacement, unrelated edits, key rotation and cancellation preserve the published file and release abandoned chunks. |
| `file_unpublished_replacement` | Chunk upload can finish while the feed is offline; peers' previous version stays protected until the replacement publishes after restart. |
| `file_conflicts` | Independent offline attachments become the same two siblings on both engines; both files' chunks stay protected; an observing replacement resolves the conflict. |
| `file_junk_and_healing` | A false address header cannot suppress repair; failed download resumes across restart; simultaneous duplicate cleanup retains genuine chunks. |
| `file_membership` | Readers can fetch and cannot attach; removal uploads no old chunks; old keys still read old files, while future manifests stay private. |
| `file_deletion` | Record deletion collects chunks; deleted-collection retention preserves them before purge; purge leaves another scope intact. |
| `file_manifest_guards` | Unknown manifest versions block cleanup; authenticated chunks with false lengths produce an unreadable file; a corrected manifest reads normally. |
| `file_reused_chunks` | Readers accept repeated addresses and mixed lengths; a chunk shared by two manifests survives deleting one reference and goes after the last. |
| `file_pending_rotation_conflict` | An upload signed before a peer edit crosses key rotation; the two attachments stay concurrent instead of silently overwriting one another. |
| `file_commit_failure` | Real SQLite triggers fail saving a manifest and consuming its journal; both roll back atomically, retain staged content and resume after process loss. |
| `file_copy_refuses_attachments` | Copying an attachment-bearing collection refuses before creating a partial list; existing file contents stay readable. |
| `file_unpublished_content_survives_eviction` | A committed upload with an offline feed survives cache eviction, remote chunk loss and process restart through its retained staged source. |
| `file_push` | Both directions discover a file through actual IDLE and engine events, with no explicit receiver sync after upload. |

## Fault boundaries and remaining work

The driver fixtures wrap the real IMAP stores. A blob fault interrupts after a
specified number of chunks have actually been stored; `lostAck` discards the
successful store's answer. An offline feed refuses engine calls while blob
connections remain usable. The harness then kills the process without a graceful
shutdown and reopens its durable state. This tests the engine's recovery at the
store boundary; it does not inject arbitrary TCP cuts inside TLS or IMAP literals.
SQLite triggers also inject durable-store failures before inserting a write and before deleting its upload journal entry. Cache eviction and provider chunk deletion are exercised independently. Fault controls are compiled into the test drivers, not the SDK libraries.

A small `chunkSize` (64 KiB) keeps lifecycle tests inexpensive on live accounts;
the boundary case uses the production 1 MiB default and a file over 7 MiB. Tests
request two transfer connections per device. Production defaults are unchanged.
Concurrent cases must share the provider's account connection budget.

The tests do not establish sustained quota behavior, connection-cap fairness,
large-folder scale, eventual repair without a surviving content replica, or
browser blob support. Gmail is not part of this suite. The live-provider matrix
and the Lean models complement one another; neither proves complete availability.

## Findings from the adversarial scenarios

These failures were reproduced in both engines and all four ordered pairings
against Stalwart before their fixes:

- **Pending upload across a key change:** commit created a fresh write from the
  newly seen state, silently superseding a peer's concurrent file. It now uses the
  ordinary late-write signing rule, retaining the original dot and causal reach.
- **Failure while committing locally:** the pending upload was removed and its
  source moved before saving its manifest. A failed write lost the resumable
  operation. Write persistence, completion bookkeeping and journal removal are
  now atomic; the staged source survives rollback.
- **Copying a list with attachments:** application-field enumeration omitted
  reserved blob fields, so a successful copy silently lost its files. Such copies
  now fail before creating a destination. Re-encrypting files into a new scope is
  still unimplemented; this guard deliberately reduces functionality to prevent
  silent loss.
- **Publication failure followed by eviction:** the staged source was discarded
  when the local manifest committed, before it reached the feed. Losing cached
  plaintext and remote chunks then made the file irreparable. A durable staging
  marker now retains the source until publication and can restore an evicted
  content file; cleanup releases it after confirmation.

The first, second and fourth cases also have native engine regressions. The copy
refusal is covered natively and end to end. These are implementation findings;
they do not change the cryptographic wire format or provider defaults.

## Verified runs (2026-11-15)

| Provider | Engine pairings | File cases | Outcome |
|---|---|---|---|
| Disposable Stalwart 1.1.0 | Swift/Swift, Swift/Go, Go/Swift, Go/Go | 17 per pair | 68 passed; Go driver built with the race detector. |
| Live Fastmail | Go/Swift, Swift/Go | 17 per pair | 34 passed; every isolated folder removed and checked. |

The Fastmail matrix combines the full 15-case run in each direction with the two
subsequent copy/source-retention regressions. The six existing control scenarios
also passed across all four Stalwart pairings. The harness's six regression tests,
full Go tests (including a separate non-race run of the 1 GiB Argon2 profile) and
full local Swift suite passed. Optional native live/keychain tests retain their
own opt-in/availability conditions; the explicit provider runs above are separate.

The staged source and cache share a hard link inside the device's blob directory,
so retaining an unpublished source does not duplicate its file bytes. This needs
a filesystem supporting hard links; a link failure leaves the upload pending.
The local completion marker contains only a write ID and content hash and is
removed with the source after publication confirmation.
