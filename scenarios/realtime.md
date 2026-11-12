# Real-time updates

Whether a change made on one device shows up on the others while the app is open, and how quickly: over JMAP push (EventSource), over IMAP IDLE, and across both. Also what happens when a push connection breaks, quietly or loudly, and how a device catches up.

The binding level (does the server notify watchers at all, and how fast) is checked against the testbed today by `testbed/probes/test_realtime.py`. These scenarios cover the protocol level on top of it, once an SDK exists.

## Latency budget

`INV-LIVE` uses these budgets, from a write being published (its append acknowledged) to it rendering on a foreground device. The hub is the local testbed with no injected latency:

| Path | Budget (p95) |
|---|---|
| JMAP writer → JMAP watcher | 1 s |
| any writer → IMAP watcher | 1 s |
| with `net: latency 400` on the watcher | 2 s |
| after a dropped connection is noticed | one sync after reconnecting |

A device notices a dead connection within **two reported ping intervals** on JMAP (60 s on Stalwart, which raises any requested interval to 30 s; testbed/README finding 3). On IMAP it notices only when IDLE is reissued, or on its own liveness check if one is added (faults.md FT-025).

---

### RT-001 An addition appears live
- **Kind:** real-world
- **Design:** §10.2 (watch), §10.3 (watch)
- **Binding:** any
- **Setup:** J and A members of Groceries; `J.phone`, `A.phone` and `A.mac` in the foreground.
- **Steps:**
  1. `J.phone adds` oat milk.
  2. `measure latency` on `A.phone`, `A.mac`.
- **Expect:**
  - oat milk renders on both of A's devices within budget, without user action.
- **Invariants:** INV-LIVE

### RT-002 Edits, reorders, completions and deletions appear live
- **Kind:** real-world
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** RT-001 done.
- **Steps:**
  1. `J.phone` in turn: renames oat milk; reorders it to the top; completes it; uncompletes it; tags it; deletes it.
  2. `measure latency` for each, on A's devices.
- **Expect:**
  - Each change renders within budget, in the order made. A delete removes the item; it doesn't leave it behind until the next launch.
- **Invariants:** INV-LIVE, INV-CONVERGE

### RT-003 JMAP writer, IMAP watcher, and the reverse
- **Kind:** real-world
- **Design:** §4, §10.2, §10.3
- **Binding:** jmap+imap
- **Setup:** `J.mac` on IMAP, `A.phone` on JMAP, same hub account.
- **Steps:**
  1. `J.mac adds` an item; `A.phone adds` an item.
  2. `measure latency` both ways.
- **Expect:**
  - Both within budget.
- **Invariants:** INV-LIVE

### RT-004 Ten watchers
- **Kind:** real-world
- **Design:** §10.2, §10.3
- **Binding:** mixed
- **Setup:** P1–P10 members of one list. Each player's device is in the foreground; six on JMAP, four on IMAP.
- **Steps:**
  1. P1 adds an item; then P5 renames it; then P9 deletes it.
  2. `measure latency` on all other nine devices for each change.
- **Expect:**
  - Every change reaches all nine within budget.
  - The four IMAP devices use four of the account's IMAP connections; the cap (16 on the testbed) isn't reached.
- **Invariants:** INV-LIVE

### RT-005 Changes to people and keys appear live
- **Kind:** real-world
- **Design:** §11.3–§11.6
- **Binding:** any
- **Setup:** permissions.md standard setup, all in the foreground.
- **Steps:**
  1. `J.phone` makes C a writer.
  2. `J.phone removes D`; the key change runs.
- **Expect:**
  - `C.phone` offers editing within budget of step 1's statement appearing, without a relaunch.
  - `D.phone` shows "House is no longer shared with you" within budget of the announcement appearing.
  - Every remaining device moves to the new epoch, within budget of its grant, without a relaunch and keeps showing live edits made after it.
- **Invariants:** INV-LIVE, INV-CUT

### RT-006 Approval while the joiner watches
- **Kind:** ux
- **Design:** §11.2–§11.4
- **Binding:** any
- **Setup:** J sent B a link to House; `B.phone` shows "Waiting for Jesper to approve" in the foreground.
- **Steps:**
  1. `J.phone approves` B.
- **Expect:**
  - `B.phone`'s waiting screen turns into the list within budget of the grant being published. B's device watches its inbox live.
- **Invariants:** INV-LIVE

### RT-007 A device doesn't react to its own writes
- **Kind:** edge
- **Design:** §10.7, §12.2
- **Binding:** any
- **Setup:** RT-001 setup.
- **Steps:**
  1. `J.phone` adds 20 items one by one.
- **Expect:**
  - `J.phone` receives push notifications for its own appends, merges nothing new from them, and publishes nothing in response.
  - The UI doesn't flicker or re-sort. The hub holds 20 record messages once settled.
- **Invariants:** INV-QUIESCE

### RT-008 Garbage collection and healing are invisible
- **Kind:** edge
- **Design:** §10.7, §12.2
- **Binding:** any
- **Setup:** RT-001 setup; J and A edit the same item concurrently, so there are two messages for it on the hub.
- **Steps:**
  1. A device publishes the merged message and destroys the two older ones.
  2. Separately, `hub: destroy record <item>`; a device heals it.
- **Expect:**
  - Watchers see the destroys in their change feed, but the item never disappears from any screen, even briefly. Destroying a dominated message removes no state.
- **Invariants:** INV-QUIESCE, INV-UNIQUE

### RT-009 A burst of changes
- **Kind:** edge
- **Design:** §10.7 (debounce), §10.2
- **Binding:** any
- **Setup:** RT-001 setup.
- **Steps:**
  1. `J.mac` pastes 200 items at once, then renames 50 of them within 10 s.
- **Expect:**
  - `A.phone` shows all 200, then the renames. Its lag doesn't keep growing while the burst lasts, and it settles within budget after the last change.
  - The burst is batched into few requests (`Email/set` up to `maxObjectsInSet`).
- **Invariants:** INV-LIVE, INV-CONVERGE

### RT-010 A connection that dies quietly
- **Kind:** fault
- **Design:** §10.2 (ping and reconnect)
- **Binding:** jmap
- **Setup:** RT-001 setup.
- **Steps:**
  1. `net: blackhole A.phone` (the connection stays open; nothing flows).
  2. `J.phone` makes five changes.
  3. Wait.
- **Expect:**
  - `A.phone` notices within two reported ping intervals (≈60 s on Stalwart), reconnects through the restored path, and shows all five changes within one sync.
  - The indicator shows "reconnecting", not "up to date", once the first interval passes without a ping.
- **Invariants:** INV-NO-LOSS
- **Notes:** §10.2 now uses the server's reported interval (testbed/README finding 3).

### RT-011 An IDLE connection that dies quietly
- **Kind:** fault
- **Design:** §10.3 (watch)
- **Binding:** imap
- **Setup:** RT-001 setup with `A.mac` on IMAP.
- **Steps:**
  1. `net: blackhole A.mac`.
  2. `J.phone` makes five changes.
  3. Wait.
- **Expect:**
  - `A.mac` notices by the next IDLE reissue (≤ 29 min), or sooner if a liveness check is specified, then shows the changes.
- **Invariants:** INV-NO-LOSS
- **Notes:** GAP (G-21): as faults.md FT-025. Without a liveness check an IMAP device can be silently 29 minutes behind while showing itself online.

### RT-012 A connection reset
- **Kind:** fault
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** RT-001 setup.
- **Steps:**
  1. `net: reset A.phone` repeatedly for a minute while `J.phone` makes changes.
- **Expect:**
  - `A.phone` reconnects after each reset, and shows every change exactly once.
  - The change feed resumes from its cursor, with no gap and no full resync.
- **Invariants:** INV-NO-LOSS, INV-UNIQUE

### RT-013 The server restarts
- **Kind:** fault
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** RT-004 setup.
- **Steps:**
  1. `hub: restart`.
  2. During the restart and right after, P1 and P2 make changes.
- **Expect:**
  - Every watcher reconnects and shows every change. The change cursors kept from before the restart still work (no full resync), or, if the server rejects them, a full resync happens and the result is the same.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE

### RT-014 iPhone in the background
- **Kind:** real-world
- **Design:** §17 (no wake-up push); §10.2
- **Binding:** jmap
- **Setup:** RT-001 setup.
- **Steps:**
  1. `A.phone backgrounded`.
  2. `J.phone` makes ten changes.
  3. `A.phone` gets a background refresh (sometimes), then later comes to the foreground.
- **Expect:**
  - While backgrounded, `A.phone` holds no push connection and shows nothing live (the expected limitation).
  - If a background refresh ran, the changes are already there on foreground; otherwise they appear within one sync of it.
- **Invariants:** INV-NO-LOSS

### RT-015 A change arriving mid-sync
- **Kind:** edge
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** RT-001 setup; `A.phone` is processing a large sync (500 changes).
- **Steps:**
  1. While it processes, `J.phone` makes one more change.
- **Expect:**
  - The push that arrives during processing isn't lost: after the large sync, `A.phone` syncs again and shows the extra change, within budget of finishing.
- **Invariants:** INV-LIVE, INV-NO-LOSS

### RT-016 Too many IMAP devices
- **Kind:** fault
- **Design:** §10.3
- **Binding:** imap
- **Setup:** 16 IMAP devices already IDLE on the hub account (the testbed cap).
- **Steps:**
  1. A 17th IMAP device opens the app.
- **Expect:**
  - The 17th device can't hold an IDLE connection. It falls back to syncing on events it can observe (launch, foreground, background refresh) and tells the user that live updates are unavailable.
  - Nobody else loses their connection.
- **Invariants:** INV-UX
- **Notes:** GAP (G-41): as hub.md HB-017 / scale.md SC-012. There is no fallback or message in the design.

### RT-017 Live updates under a slow network
- **Kind:** fault
- **Design:** §10.2, §10.3
- **Binding:** any
- **Setup:** RT-001 setup; `net: latency 400` on `A.phone`.
- **Steps:**
  1. `J.phone` makes ten changes, 2 s apart.
- **Expect:**
  - Each renders on `A.phone` within the slow-network budget.
- **Invariants:** INV-LIVE

### RT-018 Editing a field while someone else changes it
- **Kind:** ux
- **Design:** §9.2
- **Binding:** any
- **Setup:** RT-001 setup; `A.phone` has milk's title open for editing.
- **Steps:**
  1. `J.phone` renames milk while A is still typing.
  2. A finishes typing and saves.
- **Expect:**
  - A's text field isn't overwritten under her while she types (the profile rule: never move content under someone who is reading). The incoming title shows when she leaves the field, or as a conflict if her edit was concurrent.
  - After both sync, every device agrees.
- **Invariants:** INV-CONVERGE
- **Notes:** an app rule, not a protocol rule. Tandem must give the app enough to tell "concurrent" from "after": siblings (§9.2) do that.

## Family

### RT-F01 Family: latency across players and bindings
- **Kind:** real-world
- **Design:** §10.2, §10.3
- **Parameters:**
  - watchers 1–10, bindings all-jmap, all-imap or mixed;
  - one writer making changes from `M-LIST` at 1–20 per second;
  - network latency 0, 100 or 400 ms;
  - 30 s runs, 20 seeds per combination.
- **Oracle:** the reference model for content; the budget table for timing
- **Expect:**
  - p95 latency per path is within budget.
  - Every watcher's final state equals the oracle; no change is delivered twice to the UI.
- **Invariants:** INV-LIVE, INV-ORACLE, INV-UNIQUE
