# Files in the mailbox: rationale and measurements

The specification of files is `DESIGN.md` §17, an optional extension of the protocol; its open
questions are in `DESIGN.md` §19 (B1 to B14). It was designed in this document and folded into
`DESIGN.md` on 2026-11-15, once both engines exchanged files on Stalwart. What stays here is what
the specification does not need: the decisions and why, the reviews, and every measurement behind
the figures in §17. References with § are to `DESIGN.md`.

## Decisions (2026-11-15)

- **One generic layer.** Attachments on records come first (Liszto), a shared-folder app later;
  both are records whose fields hold manifests.
- **Chunks are written as 8-bit content with a few bytes escaped, with base64 as the fallback.**
  IMAP's `BINARY` extension is not used. It was the first choice, until measurement showed the
  escaped form as fast on Fastmail, faster on Gmail, and a quarter smaller in storage on both
  (below).
- **The wire format allows a chunk to be reused** by a later version of a file (delta sync). The
  first implementation does not reuse chunks: an edited file is uploaded whole (§17.10).
- **An extension, not part of the core.** A client without it treats a `_blob:` field as an
  ordinary field and never opens the blob folder.
- **Stated plainly wherever files are described:** a slow link, for files that should be shared
  among a hub's devices without ever leaving its mailbox (no SMTP, no other service). Not a file
  service. Figures are published with their date; new measurements are added, not substituted.

## Reviews

The first review found five faults: a pending replacement could destroy the
published file (§17.6), a false header could suppress healing (§17.4.3), two healers could
destroy each other's copy (§17.7), binary reads were decoded twice (§17.4.2) and the orphan
grace trusted a time a client can set (§17.7). A second review found six more, fixed in
§17.4.3, §17.5, §17.7, §17.8 and §17.9: clean-up and healing now share one definition of a live manifest, and a device
that cannot know what a chunk is needed for collects nothing. A third found three, fixed in
§17.4.2, §17.7 and §17.8: a version stays protected until its replacement is on the feed, healing never discards
a good local file, and a refused binary append falls back to text. A fourth found that the guard for
unknown manifest versions missed manifests protected only by the feed rule (§17.7). Two of
those findings concern a binary form of the chunk message that was dropped afterwards (§17.4.2).

## End-to-end validation

The [native harness](interop/README.md) exercises the real Go/Swift engines and
IMAP bindings, including upload interruption, process loss, publication failures,
malformed manifests, healing and cleanup. The [reliability campaign](interop/CAMPAIGN.md)
adds seeded histories, actual TLS socket cuts and crashes, manifest/cache isolation
regressions and a 100,000-message chunk index. These lifecycle runs
are separate from the transfer measurements below; their elapsed times include
setup and cleanup.

## Measurements (2026-11-15)

`probes/blob_probe.py` appends chunks of 256 KiB, 1 MiB and 4 MiB in both forms, fetches them back
and compares every one byte for byte. Each chunk begins with NUL bytes, bare CR and LF, a lone dot
and a line of 2,000 bytes. Reports are in `probes/results/*-blobs-*.json`. Fastmail was run twice
and Gmail twice, from the same machine. Stalwart is the disposable test server on that machine, so
its speeds mean nothing and are left out.

| | Fastmail | Stalwart 1.1.0 | Gmail |
|---|---|---|---|
| `BINARY` | yes | yes | no; `BINARY.PEEK` is refused as unparsable |
| Text chunks intact | 126 of 126, both runs | 126 of 126 | 21 of 21, both runs |
| Binary chunks intact through `BINARY.PEEK[1]` | 126 of 126, both runs | 126 of 126 | |
| How a binary chunk is stored | as base64, header rewritten to `Content-Transfer-Encoding: base64`, quota charged at 1.37 times the bytes sent | raw | |
| `BODY.PEEK[TEXT]` of a binary chunk | base64 | the raw bytes | |
| `LITERAL+` and `MULTIAPPEND` | both | both | neither |
| `APPENDLIMIT` | 200 MB | not advertised | 35 MB |
| Quota (RFC 9208) | reported | advertised, none set | reported; the figure did not move after 30 MB |
| A feed append reaching an idling connection | 0.53 s | | 29.8 s, as §10.3 found |

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
  4 MiB, and 256 KiB loses up to half. Where they cannot be batched the question is open (B1, §19).
- **In these runs the binary form looked about twice as fast as text when batched.** A later
  comparison on a warmed connection, with the order varied, did not bear that out ("Going faster"
  below), and the binary form was dropped.
- **Text appends sent pipelined were slow and uneven** (0.7 to 4.6); in one `MULTIAPPEND` they were
  steady, and binary ones were as fast as pipelined. Not explained. Section 4.3 therefore puts
  `MULTIAPPEND` first for both forms.
- **The feed stays live during an upload, at half speed.** A small append to the feed folder reached
  an idling connection in 0.53 s with nothing else going on, and in 1.0 to 1.2 s while another
  connection uploaded 1 MiB chunks.
- **The chunk index scan works as specified**: one command returned the ID and both headers of all
  126 messages in 0.32 s on Fastmail, and of 21 in 0.70 s on Gmail. Scale is unmeasured (B11, §19).

Gmail, text form, one connection, first run / second run. Gmail offers no way to batch appends.
Fetching several messages with one command is plain IMAP (RFC 9051 §6.4.9) and was measured in the
second run only, because the probe at first tied the way of fetching to the way of appending.

| Chunk | Append, MiB per second | Seconds per append | Fetch, one at a time | Fetch, one command |
|---|---|---|---|---|
| 256 KiB | 0.09 / 0.10 | 2.7 | 0.24 / 0.24 | 0.56 |
| 1 MiB | 0.17 / 0.17 | 5.7 | 0.47 / 0.47 | 0.59 |
| 4 MiB | 0.22 / 0.21 | 18.3 | 0.61 / 0.53 | 0.66 |

- **Gmail limits the rate, not the count.** An append costs about 1.5 s plus about 4 s per MiB, so a
  larger chunk gains little: 4 MiB is 1.3 times as fast as 1 MiB, not the three times that a fixed
  cost alone would give. A 4 MiB chunk takes 18 s, and a lost connection costs that much.
- **Downloads level off at about 0.6 MiB per second a connection.** One command for several chunks
  more than doubles the speed of small chunks and adds a quarter at 1 MiB.
- **1 MiB is the default everywhere.** That closes the chunk-size question.

Gmail, 16 text chunks of 1 MiB spread over several connections at once, MiB per second, first run /
second run. Appends are one at a time on each connection.

| Connections | Append | Fetch, one at a time | Fetch, one command each |
|---|---|---|---|
| 1 | 0.16 / 0.16 | 0.43 / 0.42 | 0.65 |
| 2 | 0.24 / 0.33 | 0.59 / 0.83 | 1.30 |
| 4 | 0.54 / 0.39 | 0.85 / 0.90 | 1.61 |
| 8 | 0.81 / 0.91 | 1.88 / 2.28 | 3.88 |

- **On Gmail connections are the lever.** The rate is mostly per connection: eight moved five to six
  times as much up, and six times as much down, as one. All chunks came back intact and no
  connection was refused. The runs differ by up to 40% at two and four connections.
- **Four is the default there** (§17.4.3): 2.4 to 3.4 times the upload of one connection, and it
  leaves room under Gmail's cap for the other devices of the hub (B1, §19).

Going faster. Four ways of carrying a chunk were compared on one connection with 8 chunks of 1 MiB
a row, each row one `MULTIAPPEND` where offered and one fetch command. "Text" is a `text/plain` body
of base64 as §10.4 has it; "base64" is the same bytes declared `application/octet-stream`; "escaped"
is §17.4.2's; "binary" is a literal of RFC 3516.

**A first comparison was wrong, and the mistake is worth keeping.** It measured each form on a new
connection in a fixed order, and whichever form came first ran at half speed: a connection is slow
until its window has opened. That made the text form, and then the escaped form, look half as fast
as binary on Fastmail, and a trade between speed and storage was written into this document on the
strength of it. The comparison below throws one transfer away on each connection first and was run
three times in different orders. Those first reports are still in `probes/results/` (the
`blobs-forms` files made before the probe warmed its connections).

Fastmail, MiB per second, append / fetch, the three runs:

| Form | First run | Second run | Third run |
|---|---|---|---|
| escaped | 11.3 / 11.9 | 12.8 / 12.9 | 12.9 / 5.7 |
| binary | 10.5 / 12.5 | 10.5 / 12.2 | 10.6 / 5.7 |
| base64 | 10.4 / 12.1 | 10.4 / 12.8 | 9.6 / 4.8 |
| text | 10.1 / 7.5 | 11.2 / 6.5 | 10.1 / 3.8 |

The third run's fetches were all slow, for a reason outside the comparison. The same hour, on
several connections with 16 chunks (append batched / fetch with one command each):

| Connections | Escaped | Binary |
|---|---|---|
| 1 | 8.4 / 14.3 | 7.5 / 17.5 |
| 2 | 12.0 / 19.5 | 10.8 / 18.6 |
| 4 | 13.9 / 15.2 | 12.8 / 9.5 |

- **The escaped form costs no speed on Fastmail.** Its appends were the fastest in every run, by a
  tenth to a fifth, and its fetches level with binary. Fastmail handed it back byte for byte under
  the header it was sent with, 8,541,323 bytes for 8 MiB, where a binary chunk comes back as base64
  and is charged at 1.37. The quota charge for the escaped form was not read, here or on Gmail.
- **A text body is slower to fetch**, by a fifth to a half, and no slower to append. A chunk is
  therefore `application/octet-stream`.
- **Nothing speaks for `BINARY`**: no faster, a third more storage on Fastmail, absent on Gmail.

Gmail, one connection, where a new connection's window matters little at these rates; append /
fetch, plain and with `COMPRESS=DEFLATE` (RFC 4978) on the connection:

| Form | Plain | Compressed |
|---|---|---|
| text | 0.16 / 0.60 | 0.17 / 0.75 |
| base64 | 0.17 / 0.60 and 0.15 / 0.56 | 0.20 / 0.77 and 0.19 / 0.75 |
| escaped | 0.19 / 0.72 | 0.20 / 0.76 |

- **On Gmail the escaped form is the fastest and the smallest.** Gmail limits each connection by the
  bytes of the message, and the escaped form has a quarter fewer than base64: about a fifth more
  file per second up and a quarter more down. Gmail stored it byte for byte.
- **Compression is not worth a dependency.** Base64 shrinks to 0.76 on the wire. On Gmail that
  added a quarter to downloads, which the escaped form gets without it, and ciphertext does not
  shrink at all. On Fastmail it was measured only in the flawed first comparison.
- **The line was not the limit**: 21.6 MiB per second up and 26.4 down to another service, measured
  with `curl` against `speed.cloudflare.com` in the same hour.

Gmail again on several connections, 16 chunks of 1 MiB in the escaped form, one run:

| Connections | Append | Fetch, one at a time | Fetch, one command each |
|---|---|---|---|
| 4 | 0.77 | 1.65 | 2.78 |
| 8 | 1.26 | 2.82 | 4.54 |

That is 1.4 to 2 times the text form on four connections and 1.4 to 1.5 times on eight, more than
the fewer bytes alone explain; the runs are too few to say why.

Opening a connection, ready to transfer (TLS, `LOGIN`, `CAPABILITY`, `SELECT`), three times each:
Fastmail 1.73, 1.60 and 1.51 s; Gmail 2.40, 2.76 and 2.42 s. The same short run fetched 16 chunks
from Fastmail at 18.3 MiB per second on one connection, against 10 to 12 earlier, and appended them
in batches at 4.4, against 7 to 10: the line or the server varies that much.

Fastmail, 16 binary chunks of 1 MiB spread over several connections at once, MiB per second:

| Connections | Append, one at a time | Append, batched | Fetch, one at a time | Fetch, one command each |
|---|---|---|---|---|
| 1 | 1.5 | 7.4 | 2.5 | 10.9 |
| 2 | 2.6 | 10.5 | 4.4 | 13.6 |
| 4 | 4.7 | 12.7 | 6.6 | 8.7 |
| 8 | 7.3 | 11.7 | 8.9 | 13.5 |

- **Connections and batching buy the same thing.** Unbatched, speed grows with every connection,
  five times from one to eight. Batched, a second connection adds about 40% to uploads and a quarter
  to downloads, and a ceiling of 11 to 13 MiB per second follows, on this line at least.
- **Two transfer connections is the default** (§17.4.3). Eight unbatched connections are no
  faster than one batched one, so connections do not replace batching where batching exists.
- One run; the fetch at four connections (8.7) is out of line and unexplained.
