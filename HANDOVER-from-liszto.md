# Handover: what Liszto needs from the Swift engine

Written 2026-11-13 by the Liszto session (`~/Work/liszto`, PLAN Phase S), for the session that owns the Swift engine on `wire/v2`. It stands on its own. Read against `sdk/swift/INTEGRATING.md` at `df62d25`.

## How Liszto uses the engine

Liszto keeps its own GRDB store (current values plus UI state) and runs the engine next to it with the engine's own `Store`. A bridge in the app connects them:

- **Out:** a Liszto edit marks the row dirty. The bridge calls `setField`/`add`/`remove`/`deleteRecord` and clears the dirty mark once the call returns, since the write is saved by then. A crash in between resends; before writing, the bridge compares with `item(list:id:)` and skips a field whose only value already matches.
- **In:** on `.items(list:records:)` the bridge reads those records and applies them to Liszto's rows. At launch it compares every list once (`items(list:)` against Liszto's rows), so an event lost to a crash costs nothing.

Nothing polls: the bridge reacts to engine events and to Liszto's own store observation.

## Already covered, no change needed

1. **Liszto picks the database location** (App Group container), and the app and its extensions can open it together: `Store(path:)`, WAL and a busy timeout.
2. **A durable change feed with a cursor:** not needed. The launch-time comparison above replaces it; Liszto's lists are small. Only worth adding if that comparison ever shows up in profiles.
3. **Edits that are safe to repeat:** handled on Liszto's side by the comparison before writing.

## The gap: the app's running `Device` doesn't see an extension's writes

INTEGRATING.md, "App extensions", says an extension can `reopen`, `setField` and leave, and "if the extension can't reach the hub, the app sends the edit on its next sync". From the code, that doesn't seem to hold while the app is running:

- `writeRecord` merges into the extension's in-memory `collections` and saves the write with `store.putWrite`. The app's `Device` loaded `collections` once, in `reopen`/`open`, and never reads them again.
- **Offline extension write:** healing in the app compares the app's in-memory records with the hub, so it doesn't know the write exists and doesn't publish it. It goes out only after the app restarts and reloads the store.
- **The app writes the same record afterwards:** it publishes the record's whole state (`r.kept`) without the extension's write. The hub's newest message for that record then lacks it, until a later restart heals it.
- **Online extension write:** fine. The app gets it back from the hub through IDLE. Please confirm that a message carrying writes from this same installation is applied rather than skipped as "own".

**What would fix it**, your choice:

- **(a)** The `Device` notices that another process changed the store, by checking SQLite's `PRAGMA data_version` when the actor next does anything (a sync, a write, a read call). It then reloads the records that changed, merges them, and emits `.items` for them. This is cheap and needs no timer.
- **(b)** A public `refresh()` that does the same. Liszto calls it when an extension posts a Darwin notification, which reaches the app only while it runs, and that is exactly when this matters.

Liszto would use both: (b) for an immediate update, (a) as the safety net. A test with two `Device`s on one `Store` file (app and extension) and a `MemoryHub` that's offline for the extension's write would pin it down.

## Questions

- Is a `Device` per process on one store the intended model, or should extensions use a lighter writer (sign, `putWrite`, try one append) that never loads `collections`? Liszto only needs `setField`, `add` and `deleteRecord` from an extension.

## This file

Untracked, in your worktree. Fold what you take into ENGINE.md or INTEGRATING.md, then delete it.
