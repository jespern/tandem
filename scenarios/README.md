# Tandem scenarios

A catalogue of situations the protocol must handle, from everyday use to deliberate attacks. Each scenario is written so that a person can review it today and a test harness can execute it once a Tandem SDK exists: fixed actors, steps from a small vocabulary, and checkable expectations.

The protocol is specified in `DESIGN.md` at the repository root. Every scenario cites the sections it exercises. When the design changes, scenarios citing the changed sections are re-checked.

## Files

| File | Prefix | Area |
|---|---|---|
| `identity.md` | `ID` | First run, device chain, pairing, PUK and home, recovery, revocation, theft |
| `records.md` | `RC` | CRUD, field semantics, conflicts, sets, tombstones, unknown fields, clocks |
| `sharing.md` | `SH` | Invites, joins, approvals, declines, roles and ranks, ownership, leaving, removal |
| `rotation.md` | `RO` | Epochs, rotation triggers, carried writes, late writes, competing and interrupted rotations |
| `hub.md` | `HB` | JMAP and IMAP bindings, server quirks, message format, GC, healing, hostile hub participants |
| `faults.md` | `FT` | Network faults, offline devices, stale devices, crashes mid-operation, clock trouble |
| `journeys.md` | `JN` | Multi-step real-world stories that cross areas |
| `scale.md` | `SC` | Volume, many devices, large logs, long histories |

## Actors

Users and their devices are fixed across all scenarios, so steps stay short.

| Actor | Who | Devices |
|---|---|---|
| **J** | Jesper. Sets up the hub; owns most collections. | `J.phone`, `J.mac`, `J.ipad` |
| **A** | Aimee, Jesper's wife. | `A.phone`, `A.mac` |
| **B** | Bo, a friend. | `B.phone` |
| **C** | Carla, a friend. | `C.phone` |
| **M** | Mallory, a member who misbehaves within what her keys allow. Runs a modified client that can sign anything. | `M.phone` |
| **E** | Eve. Holds the hub credential (an ex-member, or someone who saw an invite) but no collection keys. | raw hub access only |
| **T** | A thief holding one of someone else's devices, e.g. `T(J.phone)`. | the stolen device |
| **Hub** | The mail server and its operator. | |

`J.rk` is Jesper's recovery key. A device written `J.new` is a device being added or recovering.

## Scenario format

````markdown
### ID-014 Pair a new iPad while the phone is offline
- **Kind:** real-world
- **Design:** §6.5, §6.3
- **Binding:** any
- **Setup:** J has `J.phone` and `J.mac` on hub H. `J.phone` is offline.
- **Steps:**
  1. `J.mac` shows a pairing code; `J.ipad` scans it.
  2. `J.mac` approves "Add 'Jesper's iPad'?".
  3. `J.phone` comes online and syncs.
- **Expect:**
  - `J.ipad` sees every collection J belongs to within one sync.
  - `J.phone` lists `J.ipad` under Devices.
  - No PUK rotation happens (adding a device doesn't rotate).
- **Invariants:** INV-CONVERGE, INV-HUB-BLIND
````

Field rules:

- **ID** is prefix + three digits, never reused. A retired scenario keeps its ID with `**Retired:** <why>`.
- **Kind** is one of:
  - `real-world`: what users actually do;
  - `edge`: legitimate but unusual;
  - `adversarial`: someone misbehaves on purpose (M, E, T or the hub);
  - `fault`: crashes, network failures, clocks, server quirks;
  - `ux`: what the user sees and does.
- **Binding** is `any`, `jmap` or `imap`. `any` scenarios run against both.
- **Steps** use the vocabulary below. Anything outside it is written plainly and flagged in **Notes** so the harness can grow to cover it.
- **Expect** lists observable outcomes: what devices show, what the hub holds, what the app is told. Each item must be checkable by a harness, not "works correctly".
- **Invariants** names the global properties (below) that must hold after the scenario. All scenarios implicitly check `INV-CONVERGE` once every device has synced.
- **Notes** (optional): design ambiguities the scenario exposes, open questions (`Q12`), or what makes it exotic.

A scenario that finds a gap or contradiction in the design says so in **Notes**, starting with `GAP:`. These are the most valuable ones.

## Step vocabulary

| Step | Meaning |
|---|---|
| `X.dev online` / `offline` | Network reachability of a device. Offline devices keep working locally. |
| `X.dev syncs` | Runs `changes`, fetches, merges, publishes queued writes, until quiescent. |
| `X.dev quits` / `launches` | App stops (no background work) or starts (catch-up sync on launch). |
| `X.dev crashes after <step>` | Process dies at a named point, e.g. after appending the genesis but before sending grants. |
| `X.dev clock = <offset>` | Shifts that device's wall clock, e.g. `+3 days`, `-1 year`. |
| `X.dev creates collection <name>` | New collection, X as owner. |
| `X.dev adds <item> to <coll>` | Creates a record. |
| `X.dev sets <item>.<field> = <value>` | Assigns a register field. |
| `X.dev tags <item> +t` / `-t` | Set add / remove. |
| `X.dev deletes <item>` | Writes a tombstone. |
| `X.dev moves <item> to <coll>` | Create in target, delete in source. |
| `X.dev invites (qr\|link) to <coll> as <role>` | Creates an invite. |
| `Y.dev joins via <invite>` | Scans or opens it; posts the join request. |
| `X.dev approves Y` / `declines Y` | Acts on a join request. |
| `X.dev sets role Y = <role>` / `removes Y` / `hands <coll> to Y` | Roster changes. |
| `Y.dev leaves <coll>` | Self-departure. |
| `X.dev pairs X.new` | Pairing flow, including approval. |
| `X.dev revokes X.other [lost since <time>]` | Device revocation. |
| `X.new recovers with X.rk` | Recovery flow. |
| `X.dev replaces recovery key` | New recovery key (needs the current one). |
| `X.dev rotates <coll>` | Explicit rotation by owner or admin. |
| `hub: destroy <selector>` | E or the hub deletes messages: `all`, `channel <tag>`, `record <id>`, `grants for Y`, `latest N`. |
| `hub: replay <message>` | Re-appends a previously captured message. |
| `hub: rollback <channel> to <time>` | Replaces a channel's messages with those it had at an earlier time. |
| `hub: withhold <selector> from X.dev` | The hub hides messages from one device's view. |
| `hub: move <message> out of / into Tandem` | Moves a message between folders. |
| `hub: inject <bytes>` | Appends arbitrary bytes or malformed messages. |
| `hub: rewrite charset <message> as <charset>` | Stores a body in another charset or transfer encoding, as a JMAP server may. |
| `hub: reject auth for X.dev` | Credential revoked or changed. |
| `net: latency <ms>` / `partition X.dev` / `drop after <n> bytes` / `reset` | Network faults between a device and the hub (Toxiproxy in the testbed). |
| `M.dev signs <write>` | Mallory's modified client signs an arbitrary write: malformed contexts, reused dots, backdated `prev`, forged roles. |
| `T(X.dev) …` | The thief uses the stolen device, with any of the above. |
| `wait <duration>` | Simulated time passes (the harness controls clocks; 30-day and 180-day periods are not waited out in real time). |

## Global invariants

Checked after every scenario unless the scenario says otherwise and why.

| Invariant | Holds when |
|---|---|
| `INV-CONVERGE` | Every device following a collection holds the same state of it after all have synced, regardless of order. |
| `INV-HUB-BLIND` | No plaintext record content, collection name, user ID, device ID or display name appears anywhere on the hub (headers or bodies). |
| `INV-SIGNED` | Every write any device accepted verifies under the strict Ed25519 rules (§5.5) against a key in its author's chain. |
| `INV-NO-LOSS` | No write accepted by an honest device is lost, except where the design says it is dropped (left behind at a rotation, superseded, past the horizon), and then the app was told when the design says it is. |
| `INV-AUTHZ` | No device applies a log or data write its author wasn't entitled to make (§8.2, §8.3). |
| `INV-CUT` | Removed members and revoked devices can't decrypt anything published after the rotation that followed their cut. |
| `INV-MONOTONE-EPOCH` | No device's epoch for a collection ever moves backwards. |
| `INV-NO-HOLD-FOREVER` | Every held write is eventually applied or dropped once all honest devices have synced. |
| `INV-ONE-OWNER` | Every collection's converged roster has exactly one owner, or the documented "owner gone" state. |
| `INV-GC-SAFE` | Garbage collection never leaves the hub with less than the union of what honest devices published, minus what the design lets it drop. |
| `INV-QUIESCE` | After all devices sync with no new edits, they stop publishing: no feedback loops between devices. |
| `INV-UX` | Every error the user can hit has a message from §13.3 or a named new one; nothing fails silently. |
