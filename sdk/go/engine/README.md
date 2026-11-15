# The Go device engine

`github.com/jespern/tandem/sdk/go/engine` is a Tandem v2 device for Go: one installation on one hub, a port of the Swift engine (`sdk/swift/Sources/TandemEngine`, described in `sdk/swift/ENGINE.md` and `INTEGRATING.md`). The protocol is `DESIGN.md` at the repository root. Every crypto and codec operation goes through the Go core (`github.com/jespern/tandem/sdk/go`), so the two engines read each other's messages.

```sh
cd sdk/go
go test ./engine                               # about 20 s; the three 1 GiB Argon2id tests take most of it
TANDEM_SKIP_ARGON2_GIB=1 go test -race ./...   # without them
```

It needs cgo: the store is SQLite through `github.com/mattn/go-sqlite3`.

## Using it

```go
import (
    tandem "github.com/jespern/tandem/sdk/go"
    "github.com/jespern/tandem/sdk/go/engine"
)

keys, err := engine.LoadOrCreateDeviceKeys(filepath.Join(dir, "keys")) // or MarshalDeviceKeys into the platform's secret store
store, err := engine.OpenStore(filepath.Join(dir, "hub-"+tandem.B64u(hubID)+".sqlite"))
var hub tandem.PushStore = …                   // the mail binding; engine.NewMemoryHub().Connect() in tests

device, err := engine.Reopen(store, keys, hub) // the usual case; nil if the store is new
if device == nil {
    device, err = engine.Open(genesis, directoryKey, keys, store, hub) // a hub this keychain already knows
}
defer device.Close()

events, stop := device.Events()
defer stop()
go func() { err := device.Run(ctx) }()         // resumes interrupted work, then syncs on every push
for e := range events { refresh(e) }
```

Every method that talks to the hub takes a `context.Context` and returns an error. Operations (syncs and the app's actions) run one at a time; getters (`Lists`, `Items`, `Members`, `People`, …) never wait for the network: they read between operations and while one waits on the hub. `Run` returns when its context ends or the push subscription does, and may be called again afterwards; a sync that fails is reported as `EventSyncFailed` and the next push tries again.

**Creating a hub.** `engine.Found(account, keys, label, nil)`, then `f.Capsule, err = engine.NewCapsule(password, f.Pin, f.ManagementSeed, nil)` (about a second and 1 GiB), `engine.Open(f.Genesis, f.DirectoryKey, …)` and `device.Publish(ctx, f)`. Keep `f.Genesis` (its JSON) and `f.DirectoryKey` with the account details: they are what `Open` needs on another installation of the same keychain. `device.Unlock(password)` returns the management seed the admin calls take.

**People.** `Invite(ctx, label, account, ms, validFor)` returns an `InvitationLink` (`JSON()`, `ParseInvitationLink`); the new member calls `engine.Join(ctx, link, keys, label, store, hub)`; a join that failed half way is finished by calling it again with the same store. `StartPairing` and `engine.Pair` add a device that doesn't share the keychain; the admin sees `PendingClaims()` and calls `Approve` or `Reject`. `Revoke(ctx, certificate, ms)`; the person's other devices rekey their home by themselves, and list owners see `ListsNeedingRekey()` and call `Rekey`. `SetName`, `People`, `NameOf`. `ChangePassword`, `Invitations`, `CancelInvitation`, `engine.RecoverCreator`.

**Lists.** `CreateList`, `SetField`, `Add`, `Remove`, `DeleteRecord`, `Rename`; `Write(ctx, list, record, changes...)` for several fields of one record in one message (`Set`, `Add`, `Remove`, `Delete`), and `MetaRecord()` for the app's list properties beside the name (owner only); `Lists`, `Items` (immutable `Item` snapshots: `Text`, `Values` for conflicts, `Elements`, `Fields`, `CreatedBy`, `LastModifiedBy`), `Item`, `Value`, `Records`, `Name`, `Role`, `Members`. Sharing: `Share(ctx, list, participant, role, name)`, `PendingOffers`, `Accept`, `Decline`, `Leave`, `MemberStatus`, `SetRole`, `RemoveMember`, `Delete`, `IsDeleted`, `IsDead`, `RemovedLists`. A key change in flight on another device of the owner shows as `PendingChange(list)` and blocks sharing changes with a `*PendingError` until `Finish` or `Discard`; `Resume` (which `Run` calls first) carries this device's own interrupted change on, or returns `ErrSuperseded` if another device discarded or overtook it; call one of them when the app starts: `Sync` alone doesn't resume a key change, and until it is resumed the removed member can still read what's written in the old epoch. `Unshared` and `DismissUnshared` report late writes a key change may have left behind. A dead list (`IsDead`) is read-only for good; `Copy` is the way out: a new list owned by whoever copies it, with the same name, `_meta` and record IDs, shared again with everyone every end of the old roster agrees on (`ListCopy.Shared`), the rest left for the app to ask about (`Undecided`). Calling it again, or on another device of the same person, finishes the same copy. `CopyOf` on an offer names the list it continues (§11.8).

**Home.** `SetHome`, `AddHome`, `HomeValue`, `HomeElements` for the app's own `settings:` records.

**Events.** `Event.Kind` is one of `EventLists`, `EventItems` (with `List` and `Records`), `EventMembers` (with `List`), `EventOffers`, `EventClaims`, `EventPeople`, `EventHome`, `EventApproved`, `EventRevoked`, `EventSyncFailed` (with `Message`), `EventPublished` (send a wake hint naming `WakeTag()`, if the app has a hint channel). They are coarse refresh signals, sent at the end of each operation; each subscriber has its own unbounded queue, so a slow reader never holds the device up.

**Keys.** The engine never stores the keychain's keys. `MarshalDeviceKeys`/`ParseDeviceKeys` give them a 64-byte form for a secret store; `LoadOrCreateDeviceKeys` keeps them in an owner-only file. A copy of the keys on another installation is the same keychain, as an iCloud-synced one is: it finds its identity on the hub by itself on the first sync.

**Files** (`DESIGN.md` §17). A slow link for a narrow purpose: files shared among the hub's devices that never leave its mailbox (no SMTP, no other service), measured at about 12 MiB/s up and 19 down on Fastmail and 0.8 up and 2.8 down on Gmail with the default connections (`sdk/swift/INTEGRATING.md`, Files, has the table). `device.UseBlobs(blobHub, engine.BlobSettings{})` with `imap.NewBlobStore(cfg, imap.BlobOptions{})` (or `engine.NewMemoryBlobHub()` in tests); `AttachFile`/`AttachBytes(ctx, list, record, name, …)`, `Blob(list, record, name)` (`BlobHere`, `BlobRemote`, `BlobDownloading`, `BlobUnavailable`, `BlobUnreadable`, `BlobUploading`), `FetchBlob` (a path), `CancelAttach`, `KeepFiles`, `SetBlobSettings`, `CheckBlobs`, `SettleBlobs`, `BlobQuota`, and `EventBlobs`. Up to 16 KiB is inline; anything larger is a pending write until its chunks are on the hub. After each sync a worker removes chunks of replaced and deleted files and restores missing chunks of files this device holds.

**Garbage collection** (`gc.go`). After each sync: writes of past epochs the current inventory doesn't list (§9.3); a deleted or removed list's replica after `SetRetention` (30 days by default, on this device's clock from when it first saw it gone), all but the roster, with `GoneLists()`; for the owner of a deleted list, its data and chunks on the hub, all but the roster; and downloaded files no record names. Also: replaced writes of the current epoch once their messages are gone; packs of tombstones and old channels' rosters cut to their epoch, on the owner's devices (`SetPackThreshold`); deletion marks at key changes. `sdk/swift/ENGINE.md` (Garbage collection) has the details, `GC.md` the design.

## What is done

Everything the Swift engine does except what is Apple-specific, with the Swift tests ported scenario by scenario (81 test functions, the crash points as subtests): Store, MemoryHub, Registry (against `vectors/v2/control.json`), Founding, Device, Home, Collection, Sharing, Pairing, Revocation, Rotation (every crash point of §11.6, finish and discard from another owner device, late writes signed again), ListLife, Live, Hygiene, KeyRequest, Admin, App, and ReviewFinding: each of the seven attacks of the independent review, plus one that a write under a not-yet-counted epoch is held, not dropped. SecondReview (`secondreview_test.go`) has the findings of the review of this engine that held in both: keys from home count only once their epoch's statement commits to them; a revoked keychain can't branch the home; revocations, admissions and pairing claims are journalled or kept and sent again; a discarded key change stays discarded after a lost answer; a same-epoch statement is on the hub before it is recorded; fragment healing checks each part; a home write rekeys an exposed home first; and two home heads settle. With the inventory search bound (`inventories_test.go`). The core gained the admin capsule (Argon2id at m = 1 GiB, t = 3, p = 1, NFC, checked against `conformance/fixtures/tandem-v2-capsule.json`) and `JSONObject`, the Python-compatible JSON writer the engine's notices, links and statements use.

## How it differs from the Swift engine, and why

- **Concurrency.** Swift's `Device` is an actor whose methods interleave at every `await`, with syncs chained by hand. Here an operation holds the device until it ends, so no two operations interleave at all; the state lock alone is released while an operation waits on the hub, so getters stay responsive. Simpler to reason about, and it covers what Swift's `changing` set and sync chain guard against.
- **`Run` subscribes first.** It calls `Watch` before resuming and syncing, so a change that lands during the first sync wakes the loop; Swift's `run()` resumes, then watches.
- **Left out:** the Keychain wrapper, `ItemWriter` and the multi-process `data_version` check (there are no app extensions in Go; a `Store` belongs to one `Device` in one process). In their place, `MarshalDeviceKeys` and `LoadOrCreateDeviceKeys`.
- **Records are copy-on-write**, so an `Item` the app holds never changes under it (Swift gets this from value types).
- **`ChangePassword`** stretches the new password before taking the device, so syncs go on during the second of Argon2id.
- **Names** follow Swift's where Go allows: `RemoveMember(participant, list)` because `Remove` is the set-element removal; `Name(list)` and `NameOf(participant)`; `Value`, `Item` and `HomeValue` return `(value, ok)`; `validFor` 0 means the default (7 days for invitations, a day for pairing links).
- **Errors.** `ErrSuperseded`, `ErrNoteChanged`, `ErrCantFinish` and `*PendingError` are Swift's `ChangeError`. A key change's step 5 skips a member only when they have no admitted device (Swift skips on any `invalid` error).
- Same-epoch roster changes also emit `EventMembers` and `EventLists` on the device that makes them.
- **Two home heads.** Hygiene keeps in each head's channel only the writes that count there; the Swift engine republishes a write made in one head into the other's, where other devices drop it, so it destroys and republishes on every sync once a record both heads carry changes after the fork (`TestSecondReviewTwoHomeHeadsSettle`).

## What is left

- Files: the one-batch transfer on the feed connection (`imap.Store.AppendChunks` exists, the engine doesn't use it yet); requested downloads ahead of unasked ones; pipelined `APPEND`s for a server with `LITERAL+` and no `MULTIAPPEND`.

- Nothing for IMAP: `sdk/go/imap` is the binding. `cmd/tandem-device` drives one device over stdin/stdout for `interop/interop.py`, which runs Swift and Go devices on one Stalwart hub in every pairing (all scenarios pass).
- What the Swift engine also lacks: restoring a wiped hub (§12.4) beyond record-by-record healing; another admin finishing a certified pairing whose release never went out; runs against Stalwart, Fastmail and Gmail.
- The core's `RecordPayload` writes with `encoding/json`, which escapes `<`, `>` and `&` in field names and so spells some record payloads differently from the Python and Swift writers. Readers accept both and nothing signs or compares those bytes across devices; worth aligning with `JSONObject` all the same.
