import Tandem

/// One mailbox folder in memory, shared by any number of devices: what the
/// engine's tests run on. Each device gets its own connection (`connect()`),
/// which pushes on every change the way IDLE does (§10.3). The hub can also
/// misbehave on purpose, as §3.1 allows a hub to: hide messages from one
/// connection, or drop them for everyone.
public actor MemoryHub {
    struct Entry { let id: String; let channel: String; let body: Bytes }
    enum Event { case added(Entry); case removed(String) }

    private var events: [Event] = []
    private var live: [String: Entry] = [:]
    private var serial = 0
    private var watchers: [Int: AsyncThrowingStream<Void, any Error>.Continuation] = [:]
    private var watcherSerial = 0
    private var hidden: [Int: Set<String>] = [:]

    public init() {}

    /// A connection for one device.
    public func connect() -> Connection { watcherSerial += 1; return Connection(hub: self, number: watcherSerial) }

    /// Every message currently on the hub, oldest first.
    public var messages: [Message] {
        events.compactMap { e in
            guard case let .added(x) = e, live[x.id] != nil else { return nil }
            return Message(id: x.id, channel: x.channel, body: x.body)
        }
    }

    // MARK: Misbehaving (§3.1)

    /// Withhold existing and future messages of a channel from one connection.
    public func hide(channel: String, from connection: Connection) { hidden[connection.number, default: []].insert(channel) }

    /// Delete messages as a credential holder could, telling every connection.
    public func drop(_ ids: [String]) { remove(ids) }

    // MARK: The store, per connection

    func watch(_ number: Int) -> AsyncThrowingStream<Void, any Error> {
        let (stream, continuation) = AsyncThrowingStream<Void, any Error>.makeStream(bufferingPolicy: .bufferingNewest(1))
        watchers[number] = continuation
        continuation.onTermination = { _ in Task { await self.unwatch(number) } }
        return stream
    }

    private func unwatch(_ number: Int) { watchers[number] = nil }

    func changes(_ number: Int, since cursor: String) throws -> ChangeBatch {
        let start: Int
        if cursor.isEmpty { start = 0 } else {
            guard let n = Int(cursor), n >= 0, n <= events.count else { throw TandemError.invalid("hub: bad cursor") }
            start = n
        }
        var added: [Message] = [], removed: [String] = []
        let hide = hidden[number] ?? []
        for e in events[start...] {
            switch e {
            case let .added(x) where live[x.id] != nil && !hide.contains(x.channel):
                added.append(Message(id: x.id, channel: x.channel, body: x.body))
            case let .removed(id): removed.append(id)
            default: continue
            }
        }
        return ChangeBatch(added: added, removed: removed, cursor: String(events.count))
    }

    func append(channel: String, body: Bytes) -> String {
        serial += 1
        let entry = Entry(id: String(serial), channel: channel, body: body)
        live[entry.id] = entry
        events.append(.added(entry))
        notify()
        return entry.id
    }

    func fetch(_ number: Int, _ ids: [String]) -> [Message] {
        let hide = hidden[number] ?? []
        return ids.compactMap { live[$0] }.filter { !hide.contains($0.channel) }.map { Message(id: $0.id, channel: $0.channel, body: $0.body) }
    }

    func remove(_ ids: [String]) {
        var any = false
        for id in ids where live.removeValue(forKey: id) != nil { events.append(.removed(id)); any = true }
        if any { notify() }
    }

    private func notify() { for w in watchers.values { w.yield(()) } }

    func close(_ number: Int) { watchers.removeValue(forKey: number)?.finish() }

    public struct Connection: PushStore {
        let hub: MemoryHub
        let number: Int
        public func watch() async throws -> AsyncThrowingStream<Void, any Error> { await hub.watch(number) }
        public func changes(since cursor: String) async throws -> ChangeBatch { try await hub.changes(number, since: cursor) }
        public func append(channel: String, body: Bytes) async throws -> String { await hub.append(channel: channel, body: body) }
        public func fetch(ids: [String]) async throws -> [Message] { await hub.fetch(number, ids) }
        public func destroy(ids: [String]) async throws { await hub.remove(ids) }
        public func close() async { await hub.close(number) }
    }
}
