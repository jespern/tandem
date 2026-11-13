import Foundation

public struct Message: Sendable, Equatable {
    public let id: String, channel: String
    public let body: Bytes
    public init(id: String, channel: String, body: Bytes) { self.id = id; self.channel = channel; self.body = body }
}
public struct ChangeBatch: Sendable, Equatable {
    public let added: [Message], removed: [String], cursor: String
    public init(added: [Message], removed: [String], cursor: String) { self.added = added; self.removed = removed; self.cursor = cursor }
}

/// The abstract store of §10.1. watch returns after subscription is active; buffer
/// invalidations, including reconnection. An IMAP adapter serializes commands with
/// IDLE/DONE, then resumes IDLE. fetch leaves out messages that don't decode (§10.4).
public protocol PushStore: Sendable {
    func watch() async throws -> AsyncThrowingStream<Void, any Error>
    func changes(since cursor: String) async throws -> ChangeBatch
    func append(channel: String, body: Bytes) async throws -> String
    func fetch(ids: [String]) async throws -> [Message]
    func destroy(ids: [String]) async throws
    func close() async
}

fileprivate actor SessionChannel<Element: Sendable> {
    private struct Sender { let id: UInt64; let value: Element; let continuation: CheckedContinuation<Void, any Error> }
    private struct Receiver { let id: UInt64; let continuation: CheckedContinuation<Element?, any Error> }
    private var buffer: [Element] = [], senders: [Sender] = [], receivers: [Receiver] = []
    private let capacity: Int
    private var serial: UInt64 = 0
    private var ended = false
    private var error: (any Error)?
    init(capacity: Int) { self.capacity = capacity }
    func send(_ value: Element) async throws {
        try Task.checkCancellation()
        guard !ended else { throw error ?? CancellationError() }
        if !receivers.isEmpty { receivers.removeFirst().continuation.resume(returning: value); return }
        if buffer.count < capacity { buffer.append(value); return }
        serial += 1; let id = serial
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { senders.append(Sender(id: id, value: value, continuation: $0)) }
        } onCancel: { Task { await self.cancelSend(id) } }
    }
    func next() async throws -> Element? {
        try Task.checkCancellation()
        if !buffer.isEmpty {
            let value = buffer.removeFirst()
            if !senders.isEmpty { let sender = senders.removeFirst(); buffer.append(sender.value); sender.continuation.resume() }
            return value
        }
        if ended { if let error { throw error }; return nil }
        serial += 1; let id = serial
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { receivers.append(Receiver(id: id, continuation: $0)) }
        } onCancel: { Task { await self.cancelReceive(id) } }
    }
    private func cancelSend(_ id: UInt64) {
        if let i = senders.firstIndex(where: { $0.id == id }) { senders.remove(at: i).continuation.resume(throwing: CancellationError()) }
    }
    private func cancelReceive(_ id: UInt64) {
        if let i = receivers.firstIndex(where: { $0.id == id }) { receivers.remove(at: i).continuation.resume(throwing: CancellationError()) }
    }
    func finish(_ error: (any Error)?) {
        guard !ended else { return }; ended = true; self.error = error
        for sender in senders { sender.continuation.resume(throwing: error ?? CancellationError()) }; senders.removeAll()
        for receiver in receivers {
            if let error { receiver.continuation.resume(throwing: error) } else { receiver.continuation.resume(returning: nil) }
        }
        receivers.removeAll()
    }
}

/// A bounded async sequence: producers suspend while the consumer catches up.
public struct Changes: AsyncSequence, Sendable {
    public typealias Element = ChangeBatch
    fileprivate let channel: SessionChannel<ChangeBatch>
    public struct AsyncIterator: AsyncIteratorProtocol {
        fileprivate let channel: SessionChannel<ChangeBatch>
        public mutating func next() async throws -> ChangeBatch? { try await channel.next() }
    }
    public func makeAsyncIterator() -> AsyncIterator { AsyncIterator(channel: channel) }
}

/// A serialized envelope pump. The application owns durable exact-byte retry,
/// validation and applying changes. There is no scheduled reconciliation.
public actor Session {
    public nonisolated let updates: Changes
    private let output: SessionChannel<ChangeBatch>
    private let work: AsyncStream<Work>
    private let input: AsyncStream<Work>.Continuation
    private let store: any PushStore
    private var cursor: String
    private let capacity: Int
    private var pending: [UInt64: CheckedContinuation<String, any Error>] = [:]
    private var serial: UInt64 = 0
    private var worker: Task<Void, Never>?, watcher: Task<Void, Never>?
    private var started = false, closed = false
    private var wakeQueued = false

    private enum Work: Sendable { case wake; case submit(UInt64, String, Bytes); case failed(any Error) }

    public init(store: any PushStore, cursor: String = "", capacity: Int = 64) throws {
        guard capacity > 0 else { throw TandemError.invalid("session capacity must be positive") }
        self.store = store; self.cursor = cursor; self.capacity = capacity
        let channel = SessionChannel<ChangeBatch>(capacity: capacity)
        updates = Changes(channel: channel); output = channel
        let (work, input) = AsyncStream<Work>.makeStream()
        self.work = work; self.input = input
    }

    public func start() async throws {
        guard !started, !closed else { throw TandemError.invalid("session already started or closed") }
        started = true
        do {
            let events = try await store.watch()
            guard !closed else { throw CancellationError() }
            let input = self.input
            watcher = Task {
                do {
                    for try await _ in events { self.enqueueWake() }
                    if !Task.isCancelled { input.yield(.failed(TandemError.invalid("push subscription ended"))) }
                } catch { if !Task.isCancelled { input.yield(.failed(error)) } }
            }
            worker = Task { await self.run() }
        } catch { await finish(error); throw error }
    }
    private func enqueueWake() {
        guard !wakeQueued, !closed else { return }
        wakeQueued = true; input.yield(.wake)
    }

    /// An APPEND may have reached the mailbox when cancellation arrives. Retry exact bytes.
    public func submit(channel: String, body: Bytes) async throws -> String {
        guard started, !closed else { throw TandemError.invalid("session is not running") }
        guard pending.count < capacity else { throw TandemError.invalid("session submission queue is full") }
        try Task.checkCancellation()
        serial += 1
        let id = serial
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                pending[id] = continuation
                input.yield(.submit(id, channel, body))
            }
        } onCancel: { Task { await self.cancel(id) } }
    }
    private func cancel(_ id: UInt64) { pending.removeValue(forKey: id)?.resume(throwing: CancellationError()) }

    private func reconcile() async throws {
        let batch = try await store.changes(since: cursor)
        try await output.send(batch)
        cursor = batch.cursor
    }
    private func run() async {
        do {
            try await reconcile()
            for await item in work {
                try Task.checkCancellation()
                switch item {
                case .failed(let error): throw error
                case .wake: wakeQueued = false
                case .submit(let id, let channel, let body):
                    guard pending[id] != nil else { continue }
                    do {
                        let result = try await store.append(channel: channel, body: body)
                        pending.removeValue(forKey: id)?.resume(returning: result)
                    } catch { pending.removeValue(forKey: id)?.resume(throwing: error); continue }
                }
                try await reconcile()
            }
            await finish(nil)
        } catch { await finish(error) }
    }
    private func finish(_ error: (any Error)?) async {
        guard !closed else { return }; closed = true
        watcher?.cancel(); input.finish(); await output.finish(error)
        let waiting = pending.values; pending.removeAll()
        for result in waiting { result.resume(throwing: error ?? CancellationError()) }
        await store.close()
    }
    public func close() async {
        worker?.cancel(); await finish(nil)
        await worker?.value; await watcher?.value
    }
}
