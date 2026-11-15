import {Buffer} from "buffer";
export interface Message {id: string; channel: string; body: Uint8Array}
export interface ChangeBatch {added: readonly Message[]; removed: readonly string[]; cursor: string; more?: boolean}

/** watch resolves once subscribed. Buffer invalidations until read, including reconnect.
 * An IMAP adapter serializes commands with IDLE/DONE, then resumes IDLE. */
export interface PushStore {
  watch(signal: AbortSignal): Promise<AsyncIterable<void>>;
  changes(cursor: string, signal: AbortSignal): Promise<ChangeBatch>;
  append(channel: string, body: Uint8Array, signal: AbortSignal): Promise<string>;
  close(): Promise<void>;
}

class Queue<T> {
  readonly #capacity: number; readonly #items: T[] = [];
  readonly #readers: {resolve: (r: IteratorResult<T>) => void; reject: (e: unknown) => void}[] = [];
  readonly #writers: {value: T; resolve: () => void; reject: (e: unknown) => void}[] = [];
  #closed = false; #error: unknown;
  constructor(capacity: number) { this.#capacity = capacity; }
  async push(value: T): Promise<void> {
    if (this.#closed) throw this.#error ?? new Error('queue closed');
    const reader = this.#readers.shift(); if (reader) { reader.resolve({value, done: false}); return; }
    if (this.#items.length < this.#capacity) { this.#items.push(value); return; }
    return new Promise((resolve, reject) => this.#writers.push({value, resolve, reject}));
  }
  async take(): Promise<IteratorResult<T>> {
    if (this.#items.length) {
      const value = this.#items.shift()!; const writer = this.#writers.shift();
      if (writer) { this.#items.push(writer.value); writer.resolve(); } return {value, done: false};
    }
    if (this.#closed) { if (this.#error !== undefined) throw this.#error; return {value: undefined, done: true}; }
    return new Promise((resolve, reject) => this.#readers.push({resolve, reject}));
  }
  end(error?: unknown): void {
    if (this.#closed) return; this.#closed = true; this.#error = error;
    for (const w of this.#writers.splice(0)) w.reject(error ?? new Error('queue closed'));
    for (const r of this.#readers.splice(0)) { if (error !== undefined) r.reject(error); else r.resolve({value: undefined, done: true}); }
  }
}
type Submission = {kind: 'submit'; channel: string; body: Buffer; resolve: (id: string) => void; reject: (e: unknown) => void; settled: boolean};
type Work = Submission | {kind: 'wake'} | {kind: 'error'; error: unknown};

/** Event-driven envelope delivery. The application owns durable exact-byte retry,
 * protocol validation and applying changes. There is no periodic reconciliation. */
export class Session {
  readonly #store: PushStore; readonly #work: Queue<Work>; readonly #updates: Queue<ChangeBatch>;
  readonly #abort = new AbortController(); readonly #pending = new Set<Submission>();
  #cursor: string; #started = false; #closed = false; #transportClosed = false;
  #running: Promise<void> | undefined;
  constructor(store: PushStore, {cursor = '', capacity = 64}: {cursor?: string; capacity?: number} = {}) {
    if (!Number.isInteger(capacity) || capacity < 1) throw new Error('session capacity must be positive');
    this.#store = store; this.#cursor = cursor; this.#work = new Queue(capacity); this.#updates = new Queue(capacity);
  }
  async start(): Promise<void> {
    if (this.#started || this.#closed) throw new Error('session already started or closed'); this.#started = true;
    let ready!: () => void, fail!: (e: unknown) => void;
    const started = new Promise<void>((resolve, reject) => { ready = resolve; fail = reject; });
    this.#running = this.#run(ready, fail);
    // Transport cleanup failures remain observable through close(), without an
    // unhandled promise rejection while the application is reading updates.
    void this.#running.catch(() => {});
    await started;
  }
  async *updates(): AsyncGenerator<ChangeBatch> {
    while (true) { const next = await this.#updates.take(); if (next.done) return; yield next.value; }
  }
  /** Cancellation cannot guarantee an APPEND was not received. Retry the same bytes. */
  async submit(channel: string, body: Uint8Array, signal?: AbortSignal): Promise<string> {
    if (!this.#started || this.#closed) throw new Error('session is not running'); signal?.throwIfAborted();
    let request!: Submission;
    const result = new Promise<string>((resolve, reject) => { request = {kind: 'submit', channel, body: Buffer.from(body), resolve, reject, settled: false}; });
    this.#pending.add(request);
    const abort = () => { if (!request.settled) { request.settled = true; request.reject(signal?.reason); } };
    signal?.addEventListener('abort', abort, {once: true});
    void this.#work.push(request).catch(e => { if (!request.settled) { request.settled = true; request.reject(e); } });
    try { return await result; } finally { this.#pending.delete(request); signal?.removeEventListener('abort', abort); }
  }
  async #reconcile(): Promise<void> {
    let more: boolean;
    do {
      const batch = await this.#store.changes(this.#cursor, this.#abort.signal);
      await this.#updates.push(batch); this.#cursor = batch.cursor; more = batch.more === true;
    } while (more);
  }
  async #closeTransport(): Promise<void> { if (!this.#transportClosed) { this.#transportClosed = true; await this.#store.close(); } }
  async #run(ready: () => void, failStart: (e: unknown) => void): Promise<void> {
    let watcher: Promise<void> | undefined, failure: unknown;
    try {
      const events = await this.#store.watch(this.#abort.signal);
      watcher = (async () => {
        try { for await (const _ of events) await this.#work.push({kind: 'wake'});
          if (!this.#closed) await this.#work.push({kind: 'error', error: new Error('push subscription ended')});
        } catch (error) { if (!this.#closed) await this.#work.push({kind: 'error', error}); }
      })();
      // Handle watcher errors even if the work queue closes during cancellation.
      void watcher.catch(() => {});
      ready(); await this.#reconcile();
      while (true) {
        const next = await this.#work.take(); if (next.done) break; const work = next.value;
        if (work.kind === 'error') throw work.error;
        if (work.kind === 'submit') {
          if (work.settled) continue;
          try { const id = await this.#store.append(work.channel, work.body, this.#abort.signal); if (!work.settled) { work.settled = true; work.resolve(id); } }
          catch (error) { if (!work.settled) { work.settled = true; work.reject(error); } continue; }
        }
        await this.#reconcile();
      }
    } catch (error) { if (!this.#closed) failure = error; failStart(error); }
    finally {
      this.#closed = true; this.#abort.abort(); this.#work.end(failure); this.#updates.end(failure);
      for (const p of this.#pending) if (!p.settled) { p.settled = true; p.reject(failure ?? new Error('session closed')); }
      try { await this.#closeTransport(); } finally { await watcher?.catch(() => {}); }
    }
  }
  async close(): Promise<void> {
    this.#closed = true; this.#abort.abort(); this.#work.end(); this.#updates.end();
    try { await this.#closeTransport(); } finally { await this.#running; }
  }
}
