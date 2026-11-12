# Collaborating on one list

One list, from a single owner working alone to ten people editing it at once: adding, renaming, reordering, nesting, completing and deleting items, concurrently and not, with phones asleep and the internet coming and going. The concrete scenarios spell out exact outcomes for cases a person can follow; the families run the same behaviour across 1–10 players and thousands of interleavings against the reference model (README, "The reference model").

Rendering follows Liszto (DESIGN §17; Liszto's own design §6): items ordered by `sortKey` (a fractional index), ties broken by record ID; new items go to the top; one level of nesting; a parent can't be completed while a sub-item is open, and any device that sees a completed parent with an open sub-item reopens the parent (the repair rule).

## Schedules and op mixes (for the families)

| Schedule | Connectivity |
|---|---|
| `S-LIVE` | Every device online and in the foreground, syncing on push. |
| `S-SLEEP` | Phones sleep and wake at random (sleep 1 min–8 h); Macs stay online. Each phone wakes at least once at the end. |
| `S-OUTAGE` | Everyone online, then `net: outage` for all devices for 5 min–2 days while edits continue, then `net: restored`. |
| `S-PLANE` | One device offline for 1–48 h making many edits; the rest live. |
| `S-STAGGER` | Players join one by one, each after a random number of ops by the others (late joiners start from the full history). |
| `S-FLAP` | Random `net: reset` and `cut-after` faults on every device: dropped connections, lost responses, retried appends. |
| `S-CHAOS` | All of the above at random, plus app crashes and relaunches at random points. |

| Op mix | Weights |
|---|---|
| `M-LIST` | add 30, rename 15, reorder 20, complete/uncomplete 15, delete 5, notes 5, tag 5, indent/outdent 5 |
| `M-REORDER` | reorder 70, add 15, delete 5, rename 10, concentrated on the same 5 items |
| `M-TREE` | indent/outdent 40, complete/uncomplete 30, add sub-item 20, delete 10 |
| `M-HOT` | every op on the same 3 items, so nearly everything conflicts |

Every family checks, after the schedule ends and all devices have synced: `INV-CONVERGE`, `INV-ORACLE`, `INV-INTENT`, `INV-UNIQUE`, `INV-NO-LOSS`, `INV-QUIESCE`, and that the hub holds at most one message per record once garbage collection has settled (§10.7).

---

## The basic story

### CO-001 One person, one list
- **Kind:** real-world
- **Design:** §7.1, §9.5, §10.7
- **Binding:** any
- **Setup:** J has `J.phone` only, on hub H.
- **Steps:**
  1. `J.phone creates collection Groceries`.
  2. `J.phone adds` milk, eggs, bread, coffee, apples.
  3. `J.phone reorders` coffee `to top`; `completes` bread; `sets` eggs.title = "eggs (12)".
  4. `J.phone quits`, then `launches`.
- **Expect:**
  - `check state Groceries` = coffee, apples, eggs (12), milk; bread under Done. (New items go to the top, so the add order milk…apples renders apples first.)
  - The hub holds the collection's log messages and one record message per item once debouncing and GC have settled; no plaintext (INV-HUB-BLIND).
  - After the relaunch the state is identical and nothing is republished (INV-QUIESCE).
- **Invariants:** INV-CONVERGE, INV-HUB-BLIND, INV-QUIESCE

### CO-002 The same person on a second device
- **Kind:** real-world
- **Design:** §6.5, §6.3, §9.3
- **Binding:** any
- **Setup:** CO-001 done.
- **Steps:**
  1. `J.phone pairs J.mac`.
  2. `J.mac syncs`.
  3. `J.mac completes` milk; `J.phone adds` butter.
  4. Both sync.
- **Expect:**
  - After step 2, `J.mac` renders exactly what `J.phone` does, including order and the Done section.
  - After step 4, both show butter at the top and milk under Done.
- **Invariants:** INV-CONVERGE, INV-ORACLE

### CO-003 Sharing the list in the kitchen
- **Kind:** real-world
- **Design:** §11.1–§11.4, §11.6
- **Binding:** any
- **Setup:** CO-002 done. A has `A.phone` with an identity on H.
- **Steps:**
  1. `J.phone invites qr to Groceries as writer`; `A.phone joins via` it.
  2. `A.phone syncs`.
- **Expect:**
  - `A.phone` shows Groceries within one sync, identical to J's devices, including completed items and their order.
  - The roster shows J owner, A writer; the epoch is unchanged (adding a member doesn't rotate).
  - Every existing item is attributed to J.
- **Invariants:** INV-CONVERGE, INV-ORACLE, INV-AUTHZ

### CO-004 Taking turns
- **Kind:** real-world
- **Design:** §9.2
- **Binding:** any
- **Setup:** CO-003 done; all devices online.
- **Steps:**
  1. `A.phone adds` oat milk; everyone syncs.
  2. `J.phone sets` oat milk.title = "oat milk (2)"; everyone syncs.
  3. `A.phone completes` oat milk (2); everyone syncs.
  4. `J.mac uncompletes` it; everyone syncs.
- **Expect:**
  - After each step, every device shows the step's result, with no conflict reported at any point (each change was made after its author had seen the previous one).
  - oat milk (2) is open at the end, attributed "added by Aimee", last changed by Jesper.
- **Invariants:** INV-INTENT, INV-CONVERGE

### CO-005 Both add at the top at the same moment
- **Kind:** real-world
- **Design:** §9.2; Liszto §6.1
- **Binding:** any
- **Setup:** CO-003 done.
- **Steps:**
  1. Without syncing in between: `J.phone adds` tea; `A.phone adds` honey.
  2. Both sync.
- **Expect:**
  - Both items are present on every device, in the same order on every device.
  - If their `sortKey`s are equal (both generated "before the current first item"), the order is by record ID, and still the same everywhere.
- **Invariants:** INV-CONVERGE, INV-UNIQUE

### CO-006 One renames while the other ticks it off
- **Kind:** real-world
- **Design:** §9.2
- **Binding:** any
- **Setup:** CO-003 done; both offline.
- **Steps:**
  1. `J.phone sets` eggs (12).title = "eggs (6)"; `A.phone completes` eggs (12).
  2. Both go online and sync, J first.
  3. Repeat from the same start with A syncing first.
- **Expect:**
  - Both runs end with "eggs (6)", completed, on every device. Different fields don't conflict.
  - No conflict is reported.
- **Invariants:** INV-CONVERGE, INV-ORACLE

### CO-007 Both rename the same item
- **Kind:** edge
- **Design:** §9.2
- **Binding:** any
- **Setup:** CO-003 done; both offline.
- **Steps:**
  1. `J.phone sets` coffee.title = "coffee beans"; `A.phone sets` coffee.title = "ground coffee".
  2. Both sync.
  3. `A.phone sets` coffee.title = "coffee beans" after seeing the conflict.
- **Expect:**
  - After step 2: every device displays the same title (the greatest sibling by `(epoch, hlc, author, deviceID, writeID)`), and the app on every device is told about the other one.
  - After step 3: one title, no conflict, on every device.
- **Invariants:** INV-CONVERGE, INV-ORACLE, INV-INTENT

### CO-008 Both move the same item
- **Kind:** edge
- **Design:** §9.2; Liszto §6.1
- **Binding:** any
- **Setup:** CO-003 done; list order coffee, apples, eggs, milk; both offline.
- **Steps:**
  1. `J.phone reorders` milk `to top`; `A.phone reorders` milk after apples.
  2. Both sync.
- **Expect:**
  - milk appears exactly once, at one of the two positions, the same on every device.
  - No other item moves.
  - The app is told about the `sortKey` conflict; Liszto doesn't show it to the user (a reorder conflict has nothing to resolve by hand).
- **Invariants:** INV-UNIQUE, INV-CONVERGE
- **Notes:** `sortKey` is a register, so concurrent moves are siblings; whether Liszto should resolve them silently (write the displayed one back) is an app decision, not settled.

### CO-009 Both move different items into the same gap
- **Kind:** edge
- **Design:** Liszto §6.1
- **Binding:** any
- **Setup:** CO-003 done; order coffee, apples, eggs, milk; both offline.
- **Steps:**
  1. `J.phone reorders` milk after coffee; `A.phone reorders` eggs after coffee.
  2. Both sync.
- **Expect:**
  - Both moves are kept. milk and eggs sit between coffee and apples, in an order decided by their keys (and by record ID if the keys are equal), the same everywhere.
- **Invariants:** INV-CONVERGE, INV-UNIQUE

### CO-010 One deletes what the other is editing
- **Kind:** real-world
- **Design:** §9.2 (delete wins)
- **Binding:** any
- **Setup:** CO-003 done; both offline.
- **Steps:**
  1. `J.phone deletes` apples; `A.phone sets` apples.title = "green apples".
  2. Both sync.
- **Expect:**
  - apples is gone on every device.
  - `A.phone`'s app is told that its edit landed on a deleted record (see records.md RC-038, which finds the design gives no such signal).
- **Invariants:** INV-CONVERGE, INV-ORACLE
- **Notes:** GAP: as RC-038. Liszto should be able to tell Aimee "Jesper deleted this item"; the design has no event for it.

### CO-011 Phone asleep in a pocket
- **Kind:** real-world
- **Design:** §11.6 (sync before publishing), §9.3
- **Binding:** jmap
- **Setup:** CO-003 done.
- **Steps:**
  1. `A.phone adds` cheese; then `A.phone sleeps` before the debounce (§10.7) has published it.
  2. Over the next three hours, `J.phone` and `J.mac` make 20 changes to Groceries.
  3. `A.phone wakes`.
- **Expect:**
  - Within one sync of waking, `A.phone` shows all 20 changes and cheese.
  - cheese reaches `J.phone` and `J.mac` within the latency budget of A's wake-up sync (INV-LIVE for the Macs).
  - cheese was saved durably before the sleep: it survives even if iOS kills the app while it sleeps.
- **Invariants:** INV-NO-LOSS, INV-CONVERGE

### CO-012 The internet goes down for everyone
- **Kind:** real-world
- **Design:** §9.3, §10.7
- **Binding:** any
- **Setup:** CO-003 done; all devices online.
- **Steps:**
  1. `net: outage`.
  2. J and A each make ten changes, some to the same items.
  3. `net: restored`; devices reconnect in the order `A.phone`, `J.mac`, `J.phone`.
  4. Rerun with every other reconnection order.
- **Expect:**
  - Every reconnection order ends in the same state on every device, equal to the oracle.
  - Every device kept working during the outage, showing its own changes.
- **Invariants:** INV-CONVERGE, INV-ORACLE, INV-NO-LOSS

### CO-013 Six hours on a plane
- **Kind:** real-world
- **Design:** §9.2, §9.3
- **Binding:** any
- **Setup:** CO-003 done.
- **Steps:**
  1. `J.phone offline`. Over 6 h, `J.phone` adds 15 items, renames 10, completes 10, deletes 2.
  2. Meanwhile `A.phone` adds 5, completes 5 of J's original items, and renames one item J also renames.
  3. `J.phone online`; all sync.
- **Expect:**
  - Every addition from both is present once; deletions hold; completions from both hold.
  - The one item both renamed shows a conflict; every other rename applies without one.
  - `J.phone` publishes its offline changes after syncing the channel, not before (§11.6).
- **Invariants:** INV-ORACLE, INV-UNIQUE, INV-NO-LOSS

### CO-014 A sub-item added under a parent being completed
- **Kind:** edge
- **Design:** Liszto §6.3
- **Binding:** any
- **Setup:** CO-003 done; item "party" with sub-items cake, candles, both complete; both offline.
- **Steps:**
  1. `J.phone completes` party; `A.phone adds` balloons under party.
  2. Both sync.
- **Expect:**
  - On every device: party is open (repaired), balloons is open under it.
  - Concurrent repairs by J's and A's devices converge to the same value without a further conflict.
- **Invariants:** INV-CONVERGE, INV-ORACLE

### CO-015 Each indents the other's item under their own
- **Kind:** edge
- **Design:** Liszto §6.2
- **Binding:** any
- **Setup:** CO-003 done; top-level items X and Y; both offline.
- **Steps:**
  1. `J.phone indents` X under Y; `A.phone indents` Y under X.
  2. Both sync.
- **Expect:**
  - Every device renders the same tree, with each item once and nesting one level deep, by Liszto's rendering rules.
- **Invariants:** INV-CONVERGE, INV-UNIQUE
- **Notes:** GAP: as records.md RC-016. A `parentID` cycle has no rendering rule in Liszto §6.2: the rule "renders under its top-level ancestor" has no top-level ancestor in a cycle. The reference model needs a deterministic tie-break, e.g. the item with the lower record ID renders at top level.

### CO-016 Two people write in the same note
- **Kind:** edge
- **Design:** §9.2; §17
- **Binding:** any
- **Setup:** CO-003 done; item "trip" with notes "Pack passports"; both offline.
- **Steps:**
  1. `J.phone edits` trip.notes to add "Book taxi"; `A.phone edits` trip.notes to add "Feed the cat".
  2. Both sync.
- **Expect:**
  - Every device shows the same notes, containing both additions with Liszto's conflict separator.
  - The conflict is resolved once. It doesn't loop, and the text isn't nested in further separators (see records.md RC-039).
- **Invariants:** INV-CONVERGE, INV-QUIESCE

### CO-017 Tags added and removed at once
- **Kind:** edge
- **Design:** §9.2 (observed-remove set)
- **Binding:** any
- **Setup:** CO-003 done; item milk tagged `dairy`; both offline.
- **Steps:**
  1. `J.phone tags` milk `-dairy`; `A.phone tags` milk `+dairy` (re-adding it) and `+fridge`.
  2. Both sync.
- **Expect:**
  - milk is tagged `dairy` and `fridge` on every device: A's concurrent add wins over J's remove.
- **Invariants:** INV-CONVERGE, INV-ORACLE

### CO-018 One moves an item to another list while the other edits it
- **Kind:** edge
- **Design:** §9.5; Liszto move
- **Binding:** any
- **Setup:** CO-003 done; both are members of Groceries and of a second list, House; both offline.
- **Steps:**
  1. `J.phone moves` light bulbs from Groceries to House; `A.phone sets` light bulbs.title = "LED bulbs".
  2. Both sync.
- **Expect:**
  - light bulbs is in House only, under J's title (the move created a copy before A's edit), and gone from Groceries.
  - A's app is told its edit landed on a deleted record.
- **Invariants:** INV-UNIQUE, INV-CONVERGE
- **Notes:** A's rename is lost; that follows from move = create + delete (records.md RC-009 / RC-021).

### CO-019 Typing a long title
- **Kind:** real-world
- **Design:** §10.7 (debounce)
- **Binding:** any
- **Setup:** CO-003 done; both online.
- **Steps:**
  1. `J.mac` types a 40-character title into a new item, one keystroke every 150 ms.
- **Expect:**
  - `A.phone` sees the item appear and its title settle. It may show intermediate titles, but the final one arrives within the latency budget after the last keystroke.
  - The hub ends with one record message for the item, not forty.
- **Invariants:** INV-LIVE, INV-QUIESCE

### CO-020 One person on two devices at once
- **Kind:** edge
- **Design:** §9.1, §9.2
- **Binding:** any
- **Setup:** CO-003 done; `J.phone` and `J.mac` offline.
- **Steps:**
  1. `J.phone sets` milk.title = "whole milk"; `J.mac sets` milk.title = "skimmed milk".
  2. Both sync.
- **Expect:**
  - The same user's two devices conflict like two people: siblings on every device, one displayed.
- **Invariants:** INV-CONVERGE

### CO-021 Edits in a chain, each seeing the last
- **Kind:** real-world
- **Design:** §9.2
- **Binding:** any
- **Setup:** CO-003 done; both online.
- **Steps:**
  1. `J.phone sets` coffee.title = "a"; `A.phone syncs`, then `sets` it to "b"; `J.mac syncs`, then `sets` it to "c"; `A.phone syncs`, then `sets` it back to "a".
- **Expect:**
  - Every device ends with "a" and no conflict is reported at any point, although the final value equals an earlier one.
- **Invariants:** INV-INTENT

### CO-022 A wrong clock doesn't win over later edits
- **Kind:** fault
- **Design:** §9.1 (devices don't adopt clocks; causality by context)
- **Binding:** any
- **Setup:** CO-003 done; `A.phone clock = +3 hours`.
- **Steps:**
  1. `A.phone sets` coffee.title = "from A"; everyone syncs.
  2. `J.phone sets` coffee.title = "from J" (its clock is three hours behind A's write); everyone syncs.
- **Expect:**
  - "from J" everywhere, no conflict: J's write saw A's, so it supersedes it whatever the clocks say.
- **Invariants:** INV-INTENT, INV-CONVERGE

### CO-023 Undo after delete
- **Kind:** real-world
- **Design:** §9.2 (undelete = new record)
- **Binding:** any
- **Setup:** CO-003 done.
- **Steps:**
  1. `A.phone deletes` milk, then undoes within 5 s (before and after the debounce published the delete, in two runs).
- **Expect:**
  - milk is present once on every device in both runs. In the run where the delete was published, it is a new record (new ID) with the same fields.
  - Attribution of the restored item: see records.md RC-008 (unsettled).
- **Invariants:** INV-UNIQUE

### CO-024 A third person joins a busy list
- **Kind:** real-world
- **Design:** §11.2–§11.4, §9.3
- **Binding:** any
- **Setup:** CO-003 done; Groceries has 300 items and a year of history.
- **Steps:**
  1. `J.phone invites link to Groceries as writer`; `B.phone joins via` it; `J.mac approves` B.
  2. While B's device is fetching the list, J and A keep editing.
- **Expect:**
  - B sees the whole list, then the ongoing edits, without a restart, and converges with the others.
  - B's first edit shows on J's and A's devices within the latency budget.
- **Invariants:** INV-CONVERGE, INV-ORACLE, INV-LIVE

### CO-025 Shopping trip for three
- **Kind:** real-world
- **Design:** §9.2, §9.3
- **Binding:** any
- **Setup:** J, A, B members of Groceries with 30 open items; all online.
- **Steps:**
  1. J and B shop in different aisles, each ticking items. B goes to the basement car park, where `B.phone offline`, and ticks five more, two of which J also ticks.
  2. A, at home, adds three items and renames one J just ticked.
  3. `B.phone online`.
- **Expect:**
  - Every item ticked by anyone is completed once. A's three items are open at the top. The renamed item is completed with A's title, without a conflict, since rename and completion are different fields.
- **Invariants:** INV-CONVERGE, INV-ORACLE, INV-UNIQUE

### CO-026 Ten people plan a party, live
- **Kind:** real-world
- **Design:** §9.3, §10.7
- **Binding:** mixed (P1–P5 on JMAP, P6–P10 on IMAP)
- **Setup:** P1 owns "Party"; P2–P10 writers; all online.
- **Steps:**
  1. In the same minute, every player adds 10 items.
  2. Then every player completes 3 random items and moves 2 random items.
- **Expect:**
  - All 100 items present exactly once on all ten devices, in one order.
  - Completions and moves applied; items moved by several players at once each appear once.
  - Each player's additions reach the others within the latency budget, despite the burst.
  - Within the IMAP connection cap of the server (testbed: 16 per account; testbed/README finding 6).
- **Invariants:** INV-CONVERGE, INV-UNIQUE, INV-LIVE, INV-ORACLE

### CO-027 Someone leaves mid-collaboration
- **Kind:** real-world
- **Design:** §11.5, §11.6, §8.3
- **Binding:** any
- **Setup:** CO-025 done.
- **Steps:**
  1. `B.phone leaves Groceries`.
  2. J and A keep editing, including items B added.
- **Expect:**
  - Items B added stay, still attributed to B.
  - A rotation happens, and J's and A's edits continue in the new epoch without interruption.
  - `B.phone` no longer shows the list and can't decrypt anything published after the rotation (INV-CUT).
- **Invariants:** INV-CUT, INV-BYSTANDER, INV-CONVERGE

### CO-028 The busiest item
- **Kind:** edge
- **Design:** §9.2
- **Binding:** any
- **Setup:** P1–P10 members of a list with one item "rota"; all offline.
- **Steps:**
  1. Every player renames rota and edits its notes.
  2. All sync, in random order.
- **Expect:**
  - Every device shows the same displayed title. The app is told about all ten siblings and shows 16 at most (§9.2).
  - The notes contain all ten edits merged by Liszto's rule, once each.
  - One later rename by P3, after seeing all of them, resolves the title for everyone.
- **Invariants:** INV-CONVERGE, INV-INTENT

---

## Families

### CO-F01 Family: N players, live
- **Kind:** real-world
- **Design:** §9.2, §9.3, §10.7
- **Parameters:** players 1–10; devices per player 1–3; ops per player 20–200 from `M-LIST`; schedule `S-LIVE`; bindings all-jmap, all-imap, mixed; 100 seeds per combination
- **Oracle:** the reference model over every write made
- **Expect:**
  - All family checks (top of file).
  - Every op reaches every other foreground device within the latency budget (INV-LIVE), measured per op.

### CO-F02 Family: phones sleeping and waking
- **Kind:** real-world
- **Design:** §9.3, §11.6 (sync before publishing)
- **Parameters:** players 2–10; each player has a phone (`S-SLEEP`), half of them also a Mac (always on); ops 20–200 from `M-LIST`; 100 seeds per combination
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - A phone that wakes shows everything published while it slept within one sync.
  - Writes made just before sleeping were saved and published on waking (INV-NO-LOSS).

### CO-F03 Family: outage while everyone keeps editing
- **Kind:** fault
- **Design:** §9.2, §9.3, §10.7
- **Parameters:** players 2–10; devices per player 1–3; ops 20–200 from `M-LIST` and `M-HOT`; schedule `S-OUTAGE`; reconnection order random; 200 seeds per combination
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - For a fixed set of writes, every reconnection order gives the same final state (checked by replaying the seed with permuted orders).

### CO-F04 Family: one long-offline player
- **Kind:** real-world
- **Design:** §9.2, §9.3
- **Parameters:** players 2–10; schedule `S-PLANE` (offline 1–48 h, making 50–500 edits); ops from `M-LIST`; 100 seeds
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - The returning device publishes only after syncing the channel.

### CO-F05 Family: players joining over time
- **Kind:** real-world
- **Design:** §11.2–§11.4, §9.3
- **Parameters:** players 2–10 joining under `S-STAGGER`, each by QR or link (approved by P1 or an admin); ops from `M-LIST`; 100 seeds
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - Each joiner's first rendered state equals the oracle's state at the time of their first sync.
  - No joiner causes a rotation (adding members doesn't rotate).

### CO-F06 Family: flapping connections and lost responses
- **Kind:** fault
- **Design:** §10.2, §10.3, §10.7, §12.2
- **Parameters:** players 2–10; schedule `S-FLAP`; ops from `M-LIST`; bindings all three; 200 seeds
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - Appends retried after a lost response produce no duplicate items (INV-UNIQUE): duplicates on the hub are messages, not records.
  - Healing never re-uploads a record another device deleted (tombstones honoured).

### CO-F07 Family: reorder storm
- **Kind:** edge
- **Design:** Liszto §6.1, §9.2
- **Parameters:** players 2–10; ops from `M-REORDER`; schedules `S-LIVE` and `S-OUTAGE`; 200 seeds
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - Every device shows one total order with each item once.
  - Sort keys stay within the message size limit after 10,000 reorders (they grow; Liszto never rebalances).

### CO-F08 Family: nesting and completion
- **Kind:** edge
- **Design:** Liszto §6.2–§6.3
- **Parameters:** players 2–10; ops from `M-TREE`; schedules `S-LIVE`, `S-OUTAGE`, `S-SLEEP`; 200 seeds
- **Oracle:** the reference model, including the repair rule and the cycle tie-break of CO-015
- **Expect:**
  - All family checks.
  - After repair, no completed parent has an open sub-item on any device.
  - Repairs by several devices converge without further conflict.

### CO-F09 Family: everything at once
- **Kind:** fault
- **Design:** §9, §10, §11.6, §12
- **Parameters:** players 1–10; devices 1–3; all op mixes; schedule `S-CHAOS`; bindings all three; 500 seeds per night
- **Oracle:** the reference model
- **Expect:**
  - All family checks.
  - Every failure prints its seed and a minimal replay script.
