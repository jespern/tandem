# File reliability campaign

The native file campaign combines independently modeled histories, real socket
failures, durable process restart, malicious manifests, cache pressure and chunk
index load. It uses the actual Go/Swift engines, SQLite journals and IMAP bindings.
The [harness guide](README.md) covers providers, isolated folders and credentials.

## Reproduce a history

```sh
python3 -B interop/campaign.py --seeds 8 9 10 11 --steps 40 --race \
  --report .build/file-history.json
python3 -B interop/campaign.py go swift --seeds 16 --steps 48 --race \
  --report .build/replay-16.json
```

No engine arguments selects all four ordered Go/Swift pairings. A single engine
selects that engine on every device; two select founder and peer engines.
`--no-build` reuses existing drivers, with a separate Go race binary when
`--race` is selected. `--provider fastmail` explicitly selects a live account;
keep seeds and steps small there. Reports are saved atomically after each history,
including its seed, history version, operation trace and last successful checkpoint.
Replay with the same source revision, seed, step count and engine pair.

Three independent participants start with durable storage. At each checkpoint
the oracle computes expected sibling contents from observed-write ancestry and
sticky record deletion. It does not use engine clocks, version vectors or merge
code. Every device must have the exact expected siblings, and the public file API
must return the selected version byte for byte. Version 3 additionally checks a
physical header inventory for every sibling's chunks, including conflicts which
the public file API does not select, and verifies stored envelope hashes at the
end. A fourth, freshly admitted reader then recovers from provider state alone.

The seeded operations include:

- Empty, inline, chunk-boundary and multi-chunk contents, and Unicode names.
- Two- and three-writer offline conflicts and observing replacements.
- Partial uploads, lost acknowledgements, cancellation and replacement while pending.
- Several pending files, process loss and durable resume.
- Feed-offline commits, rotation across a concurrent pending attachment, and deletion during upload.
- Learning of removal while an upload is blocked, followed by re-admission.
- Deleting provider chunks and healing from a surviving content replica.

Seeds control operation choices, record IDs and file bytes. Protocol keys and
provider UIDs remain freshly generated. Reports hold no credentials, invitations
or plaintext file contents. They reproduce the schedule rather than retaining
private test databases or requiring the original mailbox.

The oracle itself has hand-worked regressions for unseen writes, causal conflict
resolution, record isolation and sticky tombstones. History versions 2 and 3 use
the same operation generator; version 3 adds physical inventory checks and counts.

## Real connection failures

```sh
python3 -B interop/interop.py --wire --race --report .build/file-wire.json
```

`wire_faults.py` is an event-driven TLS proxy restricted to disposable loopback
Stalwart. Both legs are real TLS connections. It observes IMAP framing to choose
the cut, forwards bytes unchanged and never logs authentication or bodies.
An ephemeral fixture certificate is generated with the existing `openssl` tool.
Fastmail cannot be routed through this fixture or have its certificate checks
disabled.

Each ordered pair exercises eight scenarios:

| Scenario suffix | Actual fault and recovery |
|---|---|
| `upload` | Abort both sockets inside an APPEND literal; reopen and resume. |
| `append_ack` | Observe the server's tagged successful APPEND result, discard it and abort; retry reuses accepted messages. |
| `download` | Finish four chunks, then cut inside a body literal and again on the transport's one retry; durable progress resumes after restart. |
| `idle` | Abort an active IDLE subscription, then reopen and reconcile. |
| `download_missing_partial` | As above, then remove the partial file while retaining its saved bitmap; retry must start safely. |
| `crash_upload` | The actual upload-cut event triggers SIGKILL during the operation. |
| `crash_append_ack` | The accepted-append event triggers SIGKILL before recovery. |
| `crash_download` | A body-cut event triggers SIGKILL while a download is active. |

Each cut records small evidence: literal size/forwarded byte count, successful
server acknowledgement or active subscription. A fault that never fires fails
the scenario. Crash cases require an actual SIGKILL exit status. Downloads check
the durable bitmap after a complete batch; all recovered transfers check exact
contents and one physical message per chunk. Deadlines are safety nets behind
socket, pipe and engine events, never polling or timed crash scheduling.

Proxy regressions cover FETCH bodies following header literals, CRLF on IDLE,
short headers versus skipped bodies, unchanged forwarded prefixes, and requiring
a real tagged success before discarding an acknowledgement.

## Confirmed failures and fixes

All four failures below were reproduced in every ordered Stalwart engine pairing
before their fixes. Both engines have native regressions; each also has a focused
provider scenario in `files.py`.

| Failure | Cause and resulting behavior |
|---|---|
| A bad manifest makes a good file unreadable after restart | Partial files and bitmaps were identified only by content hash. Reordered real chunks under the legitimate hash left progress reusable by a different layout. `blobs.layout.*` now binds a normalized manifest digest to resume state. A different layout or absent partial resets it, and a waiting request cannot inherit another layout's failure. |
| A valid file returns another file's bytes | Inline data was cached under an unchecked advertised hash. Inline hashes are now checked before reporting readability or accessing the content cache, including when another entry already exists there. |
| A removed uploader's old file reappears on re-admission | Pending uploads were rejected only at commit, which could never run without the new epoch's key. Invalid pending operations are now discarded when revocation is learned and before worker transfers. The regression also covers writer-to-reader downgrade and later promotion. |
| A successful fetch returns a missing path | Eviction removed a file larger than the cache budget immediately after downloading it. Eviction now retains the file being returned and can remove it on the next fetch. |

The manifest isolation fix also has a deterministic Go race regression: a good
request waits on an in-flight bad layout with the same content hash, then must
complete independently. Test synchronization uses a blocked fetch and a context
wait handshake rather than sleeps.

An additional lifecycle scenario deletes and recreates only its isolated blob
folder, restores actual encrypted messages, and requires a real UIDVALIDITY
change. It checks that old indexed message IDs disappear, first-seen clocks
restart, and a receiver with its content cache removed can still read the file.

Trade-offs are explicit: old partial checkpoints without a layout binding restart
once; complete verified content stays reusable by hash. The cache budget can be
exceeded by the just-fetched file. Its returned path is still a cache path, so a
later fetch/eviction may remove it. Defaults, dependencies and wire formats are
unchanged for the SDKs. Removal constrains a device after it learns the new roster; an offline
device cannot infer unseen revocation from silence.

## Chunk-index measurements

```sh
python3 -B interop/scale.py --messages 1000 10000 100000 \
  --report .build/file-scale.json
```

This runs only on disposable Stalwart, with bounded 250-message append batches.
The filler messages have unique content addresses and envelope-shaped bodies;
they are not decryptable application files. A genuine encrypted sentinel file
must remain byte-exact, be replaceable, and be collected after deletion while
young unrelated orphans survive. The receiver's SQLite row count must equal the
physical message count. The largest fixture is about 40 MB of message bytes.

Cold recovery includes reopening the receiver, IMAP reconciliation and persisting
its full chunk index. Warm checks include a real incremental refresh and hygiene.
Replacement includes uploading and publishing a new sentinel and reading it on
the peer. Reported memory is resident memory sampled after the scan, not peak
memory. Database bytes include any WAL files. Other local test jobs may be active;
these are characterizations, not isolated throughput benchmarks or Fastmail results.

The default Swift driver is a debug build; Go's normal build is optimized. Use
`--engines swift --swift-configuration release` to measure optimized Swift
separately; that selection is recorded in the report and never changes the default.

On this Apple Silicon Mac, Go 1.26.4, Swift 6.4.2 and disposable Stalwart 1.1.0
produced the following post-fix results (2026-11-15). Message counts exclude the
two real sentinel chunks; each asserted durable index contained those too.

| Engine/build | Filler messages | Cold recovery | Warm check | Replacement | Resident after scan |
|---|---:|---:|---:|---:|---:|
| Swift debug | 1,000 | 0.18s | 0.025s | 0.24s | 18 MiB |
| Swift debug | 10,000 | 0.97s | 0.073s | 0.87s | 30 MiB |
| Swift debug | 100,000 | 9.26s | 0.61s | 8.34s | 127 MiB |
| Swift release | 100,000 | 1.68s | 0.24s | 1.39s | 119 MiB |
| Go | 1,000 | 0.08s | 0.023s | 0.10s | 21 MiB |
| Go | 10,000 | 0.21s | 0.042s | 0.19s | 26 MiB |
| Go | 100,000 | 1.77s | 0.23s | 1.59s | 79 MiB |

At 100,002 indexed messages, SQLite plus WAL occupied about 17 MiB in Swift and
24 MiB in Go. Optimized Swift and Go are close in this run. The table does not
establish provider transfer speed or peak memory.

## Verified campaign (2026-11-15)

| Provider/workload | Scope | Passed |
|---|---|---:|
| Stalwart lifecycle | 22 scenarios in every ordered Go/Swift pairing | 88 |
| Stalwart real socket failures | 8 scenarios in every ordered pairing; Go race detector enabled | 32 |
| Stalwart seeded histories | Seeds 8–15 × 40 steps and 16–19 × 48 steps, in every ordered pairing | 48 |
| Fastmail lifecycle | 10 selected cases in each mixed direction | 20 |
| Fastmail seeded histories | Seed 8 × 8 steps in each mixed direction | 2 |
| Stalwart index scale | 1,000/10,000/100,000 in Swift debug and Go, plus 100,000 in Swift release | 7 |

All **197 provider runs** passed. The 50 expanded histories completed 2,064 seeded
steps and 2,114 checkpoints, including fresh-reader recovery. Seeds 16–19 and the
live histories include version 3's physical inventory checks. The Fastmail cases
are resume, lost acknowledgement, manifest guards, pending rotation conflict,
unpublished-source retention, the four new bug regressions and folder recreation.
This is a bounded selection, separate from the earlier full 17-case live matrix.

All exact test folders were emptied/deleted and checked absent. Fourteen harness
and oracle regressions passed, as did full Go race tests (with the 1 GiB Argon2
case run separately without race instrumentation), full local Swift tests, and
the subsequent focused native regressions. CI's socket and short-history commands
were also exercised locally; this is not a claim about an unobserved remote CI run.

The local checkout's ignored `.build/file-reliability-results.json` retains
credential-free outcomes, seeded traces, scale data, validation commands and the
16 expected failures which reproduced the four bugs before their fixes. Matrix
completeness and success were checked before that combined report was written.

## Verification boundaries

The campaign does not establish behavior under sustained provider quota
exhaustion, connection-cap fairness, power loss during disk writes, every possible
thread interleaving, 100,000 production-sized chunks, or a 100,000-message feed.
Socket tests explicitly restart the engine after a surfaced failure; they do not
claim an indefinitely self-healing `Run` session. Content already verified and
cached is trusted against arbitrary local disk tampering. Files still require a
surviving content replica to repair missing provider bytes. Attachment copying
remains refused; browser file transport remains unimplemented.

The Go/Stalwart lifecycle and socket matrices and two seeded histories run in
credential-free CI. Mixed Swift and live Fastmail runs remain explicit. A passing
history is evidence for its recorded schedule, not a proof of universal availability.
