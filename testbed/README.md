# Tandem testbed

A real mail server for testing Tandem against: **Stalwart** (JMAP and IMAP on one account) behind **Toxiproxy** (per-device network faults), driven by `tb`, a command-line tool on the Python standard library.

Needs Docker with Compose v2 and Python 3.14 or later. Both images are pinned by digest in `compose.yaml`.

## Use

```sh
./tb up                      # first run bootstraps Stalwart unattended, then starts everything
./tb hub new                 # a fresh, empty hub account; prints its descriptor (JSON)
./tb probe                   # binding conformance probes (~45 s)
./tb down                    # stop, keep data
./tb reset                   # delete containers, volumes and state
```

Everything listens on 127.0.0.1 by default. To reach the testbed from another machine, set `TB_BIND=0.0.0.0` and `TB_HOST=<this host's name>`, or forward the ports over SSH. Other variables: `TB_JMAP_PORT` (8180), `TB_IMAP_PORT` (1143), `TB_TOXI_PORT` (8474), `TB_PROXY_RANGE` (21000-21099).

### Hubs

Each test should use its own hub account: `tb hub new` takes a few milliseconds, so there's no need to reset the server between tests. A descriptor holds both bindings of one account:

```json
{ "name": "hub-1a2b3c4d",
  "jmap": { "kind": "jmap", "session": "http://127.0.0.1:8180/.well-known/jmap", "user": "...", "password": "..." },
  "imap": { "kind": "imap", "host": "127.0.0.1", "port": 1143, "tls": false, "user": "...", "password": "..." } }
```

JMAP uses Basic auth here (Stalwart accepts it); Fastmail uses a bearer token (`"token"`), which the client also supports. IMAP is plaintext on 143, enabled for the testbed only.

### Simulated devices and faults

Each simulated device reaches the hub through its own proxies, so one device can fail while others don't:

```sh
./tb fault proxy J.phone hub-1a2b3c4d   # descriptor for J.phone, routed through its proxies
./tb fault latency J.phone 800          # add latency
./tb fault partition J.phone            # cut it off
./tb fault blackhole J.phone            # connection stays open, nothing flows (dead NAT)
./tb fault cut-after J.phone 0          # drop the response: "appended but never got the answer"
./tb fault reset-peer J.phone           # TCP reset
./tb fault heal J.phone                 # undo all of it
```

### Hostile hub operations

The `hub:` steps of the scenario vocabulary (`scenarios/README.md`), on raw messages in the Tandem folder:

```sh
./tb hub ls HUB                                  # every message: UID, channel, size, H(envelope)
./tb hub destroy HUB --channel TAG | --uid N | --latest N | --all
./tb hub snapshot HUB DIR [--channel TAG]        # capture raw messages
./tb hub replay HUB DIR/*.eml                    # append them again
./tb hub rollback HUB DIR [--channel TAG]        # make a channel look as it did at the snapshot
./tb hub move HUB UID --to INBOX                 # a mail client tidying the folder
./tb hub inject HUB FILE [--channel TAG]         # arbitrary bytes, or a well-formed message around them
./tb hub rewrite-charset HUB UID --charset UTF-16 --encoding base64
./tb hub rotate-password HUB                     # credential revoked
```

Not built yet: `hub: withhold … from X.dev`, which needs a proxy that understands JMAP and IMAP and filters one device's view.

## Probes

`tb probe` checks the assumptions DESIGN.md §10 makes about mail servers, against the real server. Each probe class gets its own hub account. The probes also run against a real provider:

```sh
./tb probe --hub-file fastmail.json     # works in a throwaway folder, deleted afterwards
```

That answers open questions Q1–Q3 for Fastmail once someone supplies a token. Probes that need provisioning or Toxiproxy are skipped there.

### What the probes found on Stalwart 1.1.0

All 36 probes pass. The findings, with the ones that bear on the design marked **design**:

| # | Finding | Bearing |
|---|---|---|
| 1 | `Mailbox/query` name filters match substrings: a filter for `Tandem` returns `Tandem-old` and `MyTandem` too. | Confirms §10.2's exact-name selection is needed. |
| 2 | An unusable `sinceState` gets `invalidArguments`, not `cannotCalculateChanges`. A garbled state is sometimes parsed by prefix and answered as if valid. | **design**: §10.2 names only `cannotCalculateChanges` as the full-resync trigger; `invalidArguments` must trigger it too. A prefix-parsed state can't be detected by the client at all. |
| 3 | EventSource pings: asking for `ping=5` gets pings every 30 s; each ping reports `{"interval": 30}`. | **design**: §10.2's "two ping intervals" must use the interval the server reports, not the one requested. A dead connection takes about a minute to notice. |
| 4 | JMAP-created bodies are stored as `charset="utf-8"`, `7bit`. | Fine; §10.4's decoding covers it. |
| 5 | A charset label the server doesn't recognise (`utf-16-le` rather than IANA `UTF-16LE`) comes back from `Email/get` undecoded, with `isEncodingProblem: true`. | **design**: §10.4 says JMAP's `bodyValues` has done the decoding. A JMAP reader must check `isEncodingProblem` and fall back to the raw message. |
| 6 | IMAP allows 16 concurrent connections per account, then answers `[LIMIT] Too many concurrent requests`. | **design**: §10.3 mentions Dovecot's 10 per user and IP. On Stalwart the cap is per account, so one hub serves at most 16 IMAP devices holding IDLE, across all its users. |
| 7 | `UID FETCH n:*` past the last UID returns the last message. | Confirms §10.3. |
| 8 | QRESYNC reports expunged UIDs as `VANISHED (EARLIER)`; `CREATE` of an existing folder says `[ALREADYEXISTS]`; UIDPLUS returns `APPENDUID`; the personal namespace prefix is empty. | Confirms §10.3. |
| 9 | A message moved out of the folder shows in `Email/changes` as **updated**, not destroyed. | Confirms §10.2. |
| 10 | `Email/query` can't filter by a custom header. | Clients can't select a channel's messages server-side; they read tags from headers, as §10.2 already does. |
| 11 | A JMAP append whose response is cut still lands. | The "appended but never got the response" fault is reproducible. |
| 12 | Limits: `maxObjectsInSet` 500, `maxCallsInRequest` 16, `maxSizeRequest` 10 MB. 200 appends in one call take about 30 ms. | |

## Layout

```
compose.yaml       Stalwart + Toxiproxy, pinned
tb                 entry point
tandemtb/          jmap, imap and message-format helpers; stalwart (bootstrap,
                   accounts); toxi (faults); hubops (hostile operations); cli
probes/            binding conformance probes (unittest)
.state/            bootstrap credentials and hub accounts (git-ignored)
```

### How the bootstrap works

Stalwart 1.x starts a setup wizard when it has no configuration. `tb up` skips it the documented way:

1. writes `config.json` (a RocksDB store) into the config volume;
2. starts Stalwart once in recovery mode with a random temporary administrator;
3. over the management API (`x:Domain/set`, `x:NetworkListener/set`, `x:Imap/set`, `x:Account/set`), creates the domain and the HTTP and IMAP listeners, allows plaintext IMAP login, and creates a permanent `provisioner` administrator;
4. restarts normally.

The temporary administrator stops working at the restart.
