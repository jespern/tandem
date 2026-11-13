import {b64u, sha, randomBytes} from '../crypto.js';
import {nextClock, type Dot} from '../core.js';
import type {ChangeBatch} from '../session.js';
import {buildMessage, checkChannel, validEnvelope} from './message.js';
import {Invalidation} from './events.js';

export interface OutboxEntry {id: string; hub: string; channel: string; body: Uint8Array; wire: Uint8Array; remoteId?: string}
export interface JournalEvent {sequence: number; hub: string; kind: 'queued' | 'received' | 'removed'; id: string; channel: string; body: Uint8Array; remoteId?: string}
interface Delivery {hub: string; remoteId: string; id: string; channel: string; body: Uint8Array; present: boolean}
type EventRow = Omit<JournalEvent, 'sequence'>;
const SCHEMA = ['meta', 'clocks', 'outbox', 'received', 'cursors', 'events'] as const;
const request = <T>(req: IDBRequest<T>): Promise<T> => new Promise((resolve, reject) => { req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error); });
const finished = (tx: IDBTransaction): Promise<void> => new Promise((resolve, reject) => { tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error ?? new Error('transaction aborted')); tx.onerror = () => {}; });

/** Separate SDK-owned IndexedDB. All preparation inside a write transaction is synchronous. */
export class BrowserJournal {
  readonly #db: IDBDatabase; readonly #channel: BroadcastChannel;
  #outbox = new Invalidation(); #events = new Invalidation(); #closed = false;
  private constructor(db: IDBDatabase) {
    this.#db = db; this.#channel = new BroadcastChannel(`tandem:${db.name}`);
    this.#channel.onmessage = () => { this.#outbox.signal(); this.#events.signal(); };
    db.onversionchange = () => this.close();
  }
  static async open(name = 'tandem'): Promise<BrowserJournal> {
    if (!name) throw new Error('database name is required');
    const opening = indexedDB.open(name, 1);
    opening.onupgradeneeded = () => {
      const db = opening.result;
      for (const name of SCHEMA) {
        const store = db.createObjectStore(name, name === 'events' ? {keyPath: 'sequence', autoIncrement: true} : undefined);
        if (name === 'events' || name === 'outbox') store.createIndex('hub', 'hub');
        if (name === 'events') store.createIndex('hubSequence',['hub','sequence']);
      }
      opening.transaction!.objectStore('meta').put('tandem-v2', 'application');
    };
    const db = await request(opening);
    try { const tx = db.transaction('meta', 'readonly');
      if (await request(tx.objectStore('meta').get('application')) !== 'tandem-v2') throw new Error('not a Tandem database');
    } catch (error) { db.close(); throw error; }
    return new BrowserJournal(db);
  }
  #notify(outbox: boolean): void { if (outbox) this.#outbox.signal(); this.#events.signal(); this.#channel.postMessage('commit'); }
  close(): void { if (this.#closed) return; this.#closed = true; this.#channel.close(); this.#db.close(); this.#outbox.end(); this.#events.end(); }
  async installation(): Promise<Uint8Array> {
    const tx = this.#db.transaction(['meta','clocks'], 'readwrite', {durability: 'strict'}), done = finished(tx), store = tx.objectStore('meta');
    const saved = await request(store.get('installation')) as Uint8Array | undefined;
    if (saved && (!(saved instanceof Uint8Array) || saved.length !== 8)) { tx.abort(); await done.catch(()=>{}); throw new Error('invalid stored installation'); }
    const committed = await request(store.get('installation.committed'));
    const clock = saved ? await request(tx.objectStore('clocks').get(b64u(saved))) : undefined;
    const lostClock = saved && committed === true && !clock;
    const id = !saved || lostClock ? randomBytes(8) : saved;
    if (!saved || lostClock) { store.put(Uint8Array.from(id), 'installation'); store.put(false,'installation.committed'); }
    await done; return Uint8Array.from(id);
  }
  async queue(hub: string, installation: Uint8Array, now: number, prepare: (dot: Dot) => {channel: string; body: Uint8Array}): Promise<string> {
    if (installation.length !== 8 || !Number.isInteger(now) || now < 0 || now >= 2**48 || !hub) throw new Error('invalid installation, clock or hub');
    const localID = Uint8Array.from(installation);
    const tx = this.#db.transaction(['meta', 'clocks', 'outbox', 'events'], 'readwrite', {durability: 'strict'}), done = finished(tx);
    // Attach immediately, even if synchronous preparation later throws.
    void done.catch(() => {});
    try {
      const clockKey = b64u(localID), clock = await request(tx.objectStore('clocks').get(clockKey)) as {millis: number; counter: number} | undefined;
      const meta = tx.objectStore('meta'), active = await request(meta.get('installation')) as Uint8Array | undefined;
      if (active && b64u(active) !== clockKey) throw new Error('stale installation; reload its ID before queueing');
      if (!clock && await request(meta.get('installation.committed')) === true) throw new Error('installation clock was lost; obtain a fresh installation ID');
      if (!active) meta.put(localID,'installation');
      const next = clock ? nextClock(clock.millis, clock.counter, now) : {millis: now, counter: 0};
      const prepared = prepare({...next, device: Uint8Array.from(localID)});
      if (prepared instanceof Promise) throw new Error('queue preparation must be synchronous');
      checkChannel(prepared.channel); if (!validEnvelope(prepared.body)) throw new Error('invalid envelope');
      const body = Uint8Array.from(prepared.body), id = b64u(sha(body));
      const previous = await request(tx.objectStore('outbox').get([hub, id])) as OutboxEntry | undefined;
      if (previous && previous.channel !== prepared.channel) throw new Error('queued channel changed');
      tx.objectStore('clocks').put(next, clockKey);
      tx.objectStore('meta').put(true,'installation.committed');
      if (!previous) {
        const entry: OutboxEntry = {id, hub, channel: prepared.channel, body, wire: buildMessage(prepared.channel, body)};
        tx.objectStore('outbox').put(entry, [hub, id]);
        tx.objectStore('events').add({hub, kind: 'queued', id, channel: entry.channel, body} satisfies EventRow);
      }
      await done; this.#notify(!previous); return id;
    } catch (error) { try { tx.abort(); } catch {} await done.catch(() => {}); throw error; }
  }
  async cursor(hub: string): Promise<string> { return (await request(this.#db.transaction('cursors').objectStore('cursors').get(hub)) as string | undefined) ?? ''; }
  async pending(hub: string, limit = 128): Promise<OutboxEntry[]> {
    if (!Number.isSafeInteger(limit) || limit < 1) throw new Error('invalid pending page limit');
    return await request(this.#db.transaction('outbox').objectStore('outbox').index('hub').getAll(hub, limit)) as OutboxEntry[];
  }
  async markAppended(hub: string, id: string, remoteId: string): Promise<void> {
    const tx = this.#db.transaction('outbox', 'readwrite', {durability: 'strict'}), done = finished(tx), store = tx.objectStore('outbox');
    const entry = await request(store.get([hub, id])) as OutboxEntry | undefined;
    if (entry) store.put({...entry, remoteId}, [hub, id]); await done;
  }
  async apply(hub: string, batch: ChangeBatch): Promise<void> {
    batch = structuredClone(batch);
    const tx = this.#db.transaction(['received', 'outbox', 'cursors', 'events'], 'readwrite', {durability: 'strict'}), done = finished(tx);
    void done.catch(() => {});
    try {
      const deliveries = tx.objectStore('received'), outbox = tx.objectStore('outbox'), events = tx.objectStore('events');
      for (const message of batch.added) {
        checkChannel(message.channel); if (!message.id || !validEnvelope(message.body)) throw new Error('invalid received envelope');
        const id = b64u(sha(message.body)), old = await request(deliveries.get([hub, message.id])) as Delivery | undefined;
        if (old && (old.id !== id || old.channel !== message.channel)) throw new Error('physical delivery changed content or channel');
        const body = Uint8Array.from(message.body);
        if (!old?.present) {
          deliveries.put({hub, remoteId: message.id, id, channel: message.channel, body, present: true} satisfies Delivery, [hub, message.id]);
          events.add({hub, kind: 'received', id, channel: message.channel, body, remoteId: message.id} satisfies EventRow);
        }
        const queued = await request(outbox.get([hub, id])) as OutboxEntry | undefined;
        if (queued?.channel === message.channel) outbox.delete([hub, id]);
      }
      for (const remoteId of batch.removed) {
        const old = await request(deliveries.get([hub, remoteId])) as Delivery | undefined;
        if (old?.present) { deliveries.put({...old, present: false}, [hub, remoteId]); events.add({hub, kind: 'removed', id: old.id, channel: old.channel, body: old.body, remoteId} satisfies EventRow); }
      }
      tx.objectStore('cursors').put(batch.cursor, hub); await done; this.#notify(false);
    } catch (error) { try { tx.abort(); } catch {} await done.catch(() => {}); throw error; }
  }
  async readEvents(hub: string, after = 0, limit = 128): Promise<JournalEvent[]> {
    if (!Number.isSafeInteger(after) || after < 0 || !Number.isSafeInteger(limit) || limit < 1) throw new Error('invalid event range');
    const store = this.#db.transaction('events').objectStore('events').index('hubSequence'), out: JournalEvent[] = [];
    return new Promise((resolve, reject) => {
      const cursor = store.openCursor(IDBKeyRange.bound([hub,after],[hub,Number.MAX_SAFE_INTEGER],true,false)); cursor.onerror = () => reject(cursor.error);
      cursor.onsuccess = () => { const row = cursor.result; if (!row || out.length === limit) { resolve(out); return; }
        const event = row.value as JournalEvent; if (event.hub === hub) out.push(event); row.continue(); };
    });
  }
  /** Event notifications include commits made by another tab, through BroadcastChannel. */
  eventsChanged(): Promise<void> { return this.#events.snapshot(); }
  outboxChanged(): Promise<void> { return this.#outbox.snapshot(); }
  get name(): string { return this.#db.name; }
}
