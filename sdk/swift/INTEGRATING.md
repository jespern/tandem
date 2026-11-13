# Integrating Tandem into an app

How an Apple app (Liszto, first) builds on the Swift SDK: what to call, in what order, and how Liszto's current CloudKit design maps onto it. This guide stands on its own. The protocol is `DESIGN.md` at the repository root; the engine's internals and state are in `ENGINE.md`.

## What the SDK gives you

Three products of one Swift package (the manifest is at the repository root; Swift 6.4, iOS and macOS 27):

| Product | What it is |
|---|---|
| `Tandem` | The protocol core: signed writes, merge, envelopes, control objects, Argon2id. No I/O. |
| `TandemIMAP` | `IMAPStore`, the mail binding: Network.framework, no dependencies, IDLE push, reconnects with backoff. |
| `TandemEngine` | `Device`: one installation on one hub. Keeps everything in a SQLite `Store`, talks to the hub through a `PushStore`, and gives the app lists, records, people, sharing, devices and events. Also `Keychain`, `MemoryHub` (an in-memory hub for your tests). |

The app links `TandemEngine` and `TandemIMAP`. Everything is `async`, every `Device` is an actor, and nothing runs on a timer: work happens on a push, a call from the app, or a start.

## Words

- **Hub**: one mailbox shared by a household or team. Everyone on it has the mailbox password; nobody can read anything without their keys.
- **Participant**: one person on a hub. **Keychain**: one device keypair, shared by every device on the same iCloud Keychain. A new iPhone on the same Apple account is the same keychain and needs nothing.
- **List** (a *collection* in the spec): records with one owner and members who are writers or readers. **Record**: fields of bytes, a set field (add/remove), or deleted.
- **Home**: a participant's private records, synced to all their devices: which lists they follow, their answers, app settings (`settings:liszto`).
- **Hub password**: chosen by whoever creates the hub. Needed to invite, approve devices, revoke and recover; never for everyday use.

## Starting up

```swift
import TandemEngine
import TandemIMAP

let keychain = Keychain(service: "org.noehr.liszto", accessGroup: "group.org.noehr.liszto")
let keys = try keychain.deviceKeys()          // made once, then synced by iCloud Keychain

// One store per hub, in the App Group container so extensions can share it.
let store = try Store(path: containerURL.appendingPathComponent("hub-\(B64u.encode(hubID)).sqlite").path)
let imap = IMAPStore(.init(host: entry.account.host, port: entry.account.port,
                           username: entry.account.user, password: entry.account.password))
try await imap.ensureFolder()

let device: Device
if let reopened = try Device.reopen(store: store, keys: keys, hub: imap) {
    device = reopened                         // the usual case
} else {
    // First launch on this device for a hub the keychain already knows (a new iPhone).
    let genesis = try ControlObject.parse(entry.genesis, expect: "genesis")
    device = try Device.open(genesis: genesis, directoryKey: entry.directoryKey, keys: keys, store: store, hub: imap)
}

let events = await device.events()
let live = Task { try await device.run() }   // resumes interrupted work, then syncs on every push
for await event in events { refreshUI(for: event) }
```

`keychain.hubs()` lists the hubs this keychain is on (mailbox account, pinned genesis, directory key); save one with `keychain.save(hub:_:)` when the device creates or joins a hub. A device on a synced keychain finds its own identity on the hub by itself (`device.me` is set after the first `sync()`).

`run()` returns when the push subscription ends for good; restart it then. A sync that fails is reported as `.syncFailed(message)` and retried on the next push.

### Events

| Event | Read again |
|---|---|
| `.lists` | `lists()`, `name(list:)`, `role(list:)` |
| `.items(list:records:)` | `item(list:id:)` for those records, or `items(list:)` |
| `.members(list:)` | `members(list:)`, `memberStatus(list:)` |
| `.offers` | `pendingOffers()` |
| `.claims` | `pendingClaims()` (admins) |
| `.people` | `people()`, `name(of:)` |
| `.home` | `homeValue(_:field:)`, `homeElements(_:field:)` |
| `.approved` | a paired device is in: `awaitingApproval` is false |
| `.revoked` | this device's keychain was revoked (`isRevoked`): show it, stop writing, offer to re-pair |
| `.syncFailed(String)` | log it |
| `.published` | this device put something on the hub: send a wake hint, if the app has a hint channel (below) |

Events are coarse refresh signals, sent after each sync and each local write. Several subscribers are fine.

## Lists and records

```swift
let list = try await device.createList(name: "Groceries")
let id = device.newRecordID()                                   // 16 random bytes
try await device.setField(list: list, record: id, field: "title", value: Bytes("Milk".utf8))
try await device.add(list: list, record: id, field: "tags", element: Bytes("dairy".utf8))
try await device.remove(list: list, record: id, field: "tags", element: Bytes("dairy".utf8))
try await device.deleteRecord(list: list, record: id)          // sticky: a deleted record stays deleted

for item in await device.items(list: list) {                   // not deleted, by ID
    item.text("title")                                         // the displayed value
    item.values("notes")                                       // more than one: a conflict, with authors
    item.elements("tags")
    item.createdBy; item.lastModifiedBy                        // participant IDs, for attribution
}
try await device.rename(list: list, to: "Food")                // owner only
```

- **Writes are local first.** A write is saved and merged before it goes out. If the hub can't be reached it is kept, reported as `.syncFailed`, and published by the next sync. There is nothing like CloudKit's dirty fields or system fields to track.
- **Merge is per field and needs no code in the app.** Concurrent edits of different fields both stay. Concurrent edits of one field leave every value (`item.values(field)`), the displayed one first; writing the field again settles it. That replaces Liszto's `notesBase`/`notesSent`: show a conflict when `values("notes").count > 1`, and the user's next edit resolves it. Set fields (tags) merge as add-wins sets; that replaces `tagsBase`/`tagsSent`.
- **Field values are bytes.** Pick one encoding per field and keep it: UTF-8 for text, a single byte for flags, the record ID's 16 bytes for a parent, your sort key's bytes for order. Unknown fields from newer app versions are kept and synced.
- **Moving an item between lists** is create in the target, delete in the source, as Liszto does today.
- Coalesce rapid edits (typing) before calling `setField`; every call is one message on the hub.

### Default lists (Inbox, Later)

Keep their IDs in home, which every device of the user shares: `setHome("settings:liszto", field: "inbox", value: listID)`. On first launch, read `homeValue("settings:liszto", field: "inbox")` after the first sync and create the list only if it's absent. Two devices that were offline may both create one: the home value then settles on one of the two, the same on every device. The device whose list lost moves its items into the winner and deletes its own.

## Sharing

Lists are shared with people already on the hub, picked from the directory; there are no list links. Someone new gets a hub invitation first.

```swift
try await device.setName("Alice")                               // the name others see
let people = await device.people()                              // names, devices, isMe, isCreator
try await device.share(list: list, with: bob.id, role: .writer) // owner only
try await device.memberStatus(list: list)                       // pending, accepted, declined, left
try await device.setRole(.reader, for: bob.id, in: list)        // lowering a role changes the key
try await device.remove(bob.id, from: list)                     // changes the key
try await device.delete(list: list)                             // final

// The other side:
for offer in await device.pendingOffers() { /* "Bob shared Groceries with you" */ }
try await device.accept(list: offerList)                       // every device of this person follows it
try await device.decline(list: offerList)
try await device.leave(list: list)                             // the owner sees .left and removes them
```

- "Stop sharing" is removing every member. Their devices keep a read-only replica; `role(list:)` is nil for them, the list leaves `lists()` and appears in `removedLists()`, for Liszto's rescue into the Inbox.
- A removed member's edits that never reached the list stay in their replica. For a member who is still in the list, edits that a key change may have left behind are listed by `unshared(list:)`; offer to recover or dismiss them (`dismissUnshared`). Liszto's "rescue into Inbox" fits both.
- Removing someone or lowering a role is a key change: it needs the network and takes a few round trips. It is journalled, so a crash resumes it (`resume()`, which `run()` calls). If another device of the same owner was in the middle of one, `pendingChange(list:)` describes it and `finish(list:seen:)` or `discard(list:seen:)` settles it; until then sharing changes on that list throw `ChangeError.pending`.
- `isDead(list:)`: two devices of the owner changed sharing at the same instant. The list is read-only for good; offer to copy it into a new list.

## Hubs, people and devices

### Creating a hub

```swift
let keys = try keychain.deviceKeys()
var founding = try Founder.found(account: account, keys: keys, label: "Alice’s iPhone")
founding.capsule = try Founder.capsule(password: password, pin: founding.pin, managementSeed: founding.managementSeed)
let device = try Device.open(genesis: founding.genesis, directoryKey: founding.directoryKey, keys: keys, store: store, hub: imap)
try await device.publish(founding)
try keychain.save(hub: founding.pin.hub, .init(account: account, genesis: try founding.genesis.json(),
                                               directoryKey: founding.directoryKey))
```

The password: at least 12 Unicode code points, at most 1,024 bytes. Suggest a six-word passphrase. Stretching takes about a second and 1 GiB on Apple silicon; on iOS add the increased-memory-limit entitlement. Unlocking is `try await device.unlock(password:)`, which returns the management seed `ms` the admin calls take. `keychain.keepManagementSeed(ms, hub:)` keeps it on this device only, behind Face ID, so later approvals don't ask for the password; ask the user first, since a device that keeps it is as good as the password.

### Inviting someone

```swift
let link = try await device.invite(label: "Bob", account: account, ms: ms)   // valid 7 days
let qr = try link.json()                   // show as a QR code or a link that opens the app; never log it
// On Bob's device:
let bob = try await Device.join(link: try InvitationLink.parse(scanned), keys: keys, label: "Bob’s iPhone",
                                store: store, hub: imap)
```

The link carries the mailbox password: treat it as a secret. `invitations()` lists the ones you made; `cancelInvitation(capability:ms:)` stops one from being used.

### Adding a device that doesn't share the keychain

An iPhone and a Mac on the same Apple account share the keychain and need nothing. For anything else (another Apple account, a future Android app):

```swift
let link = try await phone.startPairing(label: "Tablet", account: account)    // show link.json() as a QR code
let tablet = try await Device.pair(link: try PairingLink.parse(scanned), keys: tabletKeys, label: "Alice’s tablet",
                                   store: store, hub: imap)
// tablet.awaitingApproval: "Waiting for the hub admin"
// On the admin's device, on .claims:
for claim in await admin.pendingClaims() { /* "New device from \(name(of: claim.participant)): \(claim.label)" */ }
try await admin.approve(claim: claim.id, ms: ms)   // or reject(claim:ms:)
// The tablet gets .approved and then everything Alice's other devices have.
```

### Removing a device

`revoke(certificate:ms:)` (admin). The person's other devices change their home key by themselves. Lists whose members lost a device need a new key from their owner: `listsNeedingRekey()` on the owner's device, then `rekey(list:)`; until then writes to those lists throw, so nothing new goes out under the exposed key. Removing a key removes every device on that keychain: remove the stolen device from the Apple account first (Find My), then `keychain.forgetDeviceKeys()` and pair again.

### Other admin actions

`changePassword(to:ms:)`; `Device.recoverCreator(...)` brings back the hub's creator, with the hub password, after every device and the keychain are gone (nobody else can be recovered: a member who loses everything joins again as a new person and has lists shared again).

## App extensions

Share and widget extensions can't run the live sync, but they can write: open the same store from the App Group container with the same keychain access group, `Device.reopen(store:keys:hub:)`, call `setField`, and leave. The store is SQLite in WAL mode with a busy timeout, and each write reserves its clock value in a transaction, so the app and an extension never clash. If the extension can't reach the hub, the app sends the edit on its next sync.

## Mail providers

Any IMAP server with IDLE works. Measured: Fastmail (Cyrus) about 0.5 s from one device's append to another's push; Stalwart milliseconds; Gmail about 30 s by itself, 9–12 s with the SDK's IDLE restart every 3 s (automatic on Gmail). Outlook.com doesn't allow the app passwords this needs. Use an app password, never the account password.

## Running in the background

IMAP IDLE runs while the app runs; the Mac app, which stays running, gets every change live. A suspended iOS app keeps no connection. Decided for the first version: it syncs when it is opened, and whenever iOS grants it background time.

- **Background refresh.** Add the "Background fetch" mode and a task identifier (`BGTaskSchedulerPermittedIdentifiers`) to `Info.plist`. At launch, `BGTaskScheduler.shared.register(forTaskWithIdentifier:)` with a handler that calls `try await device.sync()`, schedules the next `BGAppRefreshTaskRequest`, and calls `setTaskCompleted`. iOS decides when it runs, from a few times an hour for an app used daily to a few times a day; each run has about 30 seconds.
- **On the way out.** When the app goes to the background, wrap the last sends in `beginBackgroundTask` so a just-made edit leaves the device.

**Wake hints, later, and never relied on.** Tandem exists so that the app depends on no cloud. A wake hint (`DESIGN.md` §10.3) only tells a device "something changed on this hub, sync now": it names `device.wakeTag`, an opaque tag every participant's devices compute the same, and carries no data, no key and no content. A lost, late or forged hint costs at most one sync or one delay, and with no hint channel at all everything still converges. So the channel is a plug-in the app may add and may drop:

- send one on `DeviceEvent.published` (coalesce a burst into one);
- on receiving one, call `device.sync()` (in the background, inside the time iOS gives a push);
- keep it outside the SDK: the SDK has no dependency on any channel.

For Liszto the first channel is CloudKit, which it already uses: a record type in the **public** database, `Wake`, with one field `tag`; a device that publishes saves (or touches) the `Wake` record for its tag, and every device subscribes with a `CKQuerySubscription` on `tag == device.wakeTag` and a silent notification (`shouldSendContentAvailable`). Apple then learns which devices share a tag and when a hub is busy, nothing more. Without an iCloud account, or if Apple throttles silent pushes, the app falls back to background refresh and opening. Another channel (a small relay, an Android push service) can replace it with no change to the SDK or the hub.

## Not like CloudKit

- **Account events.** There is no account to sign out of. A revoked device (`.revoked`) is the closest: keep the replica read-only and offer to pair again.
- **Quotas.** The hub is a mailbox: the SDK keeps about one message per record (old ones are collected), but a deleted record keeps a small tombstone for good.

## Testing your app

`MemoryHub` is an in-memory hub with live push: `let hub = MemoryHub(); Device.open(..., hub: await hub.connect())`, one connection per simulated device. `hub.drop(_:)` deletes messages as a hostile mailbox user would; the devices heal. The SDK's own tests (`sdk/swift/Tests/TandemEngineTests`) are worked examples of every flow above. Against a real server: `TANDEM_IMAP_STALWART=1 swift test` starts a disposable Stalwart in Docker. `Keychain` needs a signed host with a keychain entitlement; its test skips elsewhere.

## Not done yet

- A hub that was wiped completely (§12.4): the devices re-publish what they hold only through healing, record by record.
- Runs on Fastmail and Gmail with the full engine; the IMAP binding itself has been measured there.
