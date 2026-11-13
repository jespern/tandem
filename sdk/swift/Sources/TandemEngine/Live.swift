import Foundation
import Tandem

/// What changed, for the app to refresh (§13). Coarse on purpose: each one says which getter to read again.
public enum DeviceEvent: Hashable, Sendable {
    /// `lists()`: lists added, removed, renamed, or their roles changed.
    case lists
    /// `items(list:)`: these records of a list changed.
    case items(list: Bytes, records: [Bytes])
    /// `members(list:)` and `memberStatus(list:)`.
    case members(list: Bytes)
    /// `pendingOffers()`.
    case offers
    /// `pendingClaims()`, on an admin's device.
    case claims
    /// `people()`: names, devices, admissions or revocations.
    case people
    /// Home records: `homeValue`, the app's `settings:` records.
    case home
    /// This device was waiting for the admin and is now on the hub (`awaitingApproval` is false).
    case approved
    /// This device's keychain was revoked: it reads what it has and writes nothing (`isRevoked`).
    case revoked
    /// A sync failed; the next push or `sync()` tries again. The message is for logs.
    case syncFailed(String)
}

extension Device {
    /// A stream of what changes, from now on. Each call is its own subscriber; ending the loop unsubscribes.
    public func events() -> AsyncStream<DeviceEvent> {
        let (stream, continuation) = AsyncStream<DeviceEvent>.makeStream()
        let id = UUID()
        subscribers[id] = continuation
        continuation.onTermination = { _ in Task { await self.unsubscribe(id) } }
        return stream
    }

    func unsubscribe(_ id: UUID) { subscribers[id] = nil }

    /// Note a change, to be sent when the current operation ends.
    func changed(_ e: DeviceEvent) { pendingEvents.append(e) }

    /// Send what changed, merging record lists per list.
    func flushEvents() {
        guard !pendingEvents.isEmpty else { return }
        var items: [Bytes: Set<Bytes>] = [:], others: [DeviceEvent] = []
        for e in pendingEvents {
            if case let .items(list, records) = e { items[list, default: []].formUnion(records) }
            else if !others.contains(e) { others.append(e) }
        }
        pendingEvents.removeAll()
        let merged = others + items.sorted { $0.key.lexicographicallyPrecedes($1.key) }.map {
            DeviceEvent.items(list: $0.key, records: $0.value.sorted { $0.lexicographicallyPrecedes($1) })
        }
        for s in subscribers.values { for e in merged { s.yield(e) } }
    }

    /// Run this device live: finish any journalled work, then sync on every push from the hub, never on a timer
    /// (§10.3). Returns when the push subscription ends; a sync that fails is reported as `.syncFailed` and the next
    /// push tries again. The IMAP binding reconnects underneath.
    public func run() async throws {
        do { try await resume() } catch { changed(.syncFailed("\(error)")); flushEvents() }
        let pushes = try await hub.watch()
        for try await _ in pushes {
            do { try await sync() } catch { changed(.syncFailed("\(error)")); flushEvents() }
        }
    }
}
