import {b64u, sha, randomBytes} from '../crypto.js';
import {nextClock, type Dot} from '../core.js';
import type {ChangeBatch} from '../session.js';
import {buildMessage, checkChannel, validEnvelope} from './message.js';
import {Invalidation} from './events.js';

import type {ExportGuard} from '../operation.js';
export type {ExportGuard} from '../operation.js';
export interface JournalAction {id:string;hub:string;sealed:Uint8Array}
export interface PendingDelete {hub:string;id:string}
export interface OutboxEntry {guard?:ExportGuard;deferred?:boolean;id: string; hub: string; channel: string; body: Uint8Array; wire: Uint8Array; remoteId?: string}
export interface JournalEvent {deferred?:boolean;sequence: number; hub: string; kind: 'queued' | 'received' | 'removed'; id: string; channel: string; body: Uint8Array; remoteId?: string}
export interface LocalMessage {guard?:ExportGuard;deferred?:boolean;channel: string; body: Uint8Array}
export interface LocalTransaction {
  /** Opaque device state, read under the same transaction lock as the outbox. */
  readonly state: Uint8Array | undefined;
  /** Last durable envelope event in this hub, read under the write lock. */
  readonly sequence: number;
  readonly clock: {millis:number;counter:number}|undefined;
  readonly actions: ReadonlyMap<string, Uint8Array>;
  nextDot(): Dot;
}
export interface LocalPlan {
  messages: readonly LocalMessage[];
  actions?: readonly {id:string;sealed:Uint8Array|null}[];
  cancel?: readonly string[];
  destroy?: readonly string[];
  /** Omit to preserve state; null removes it. Secrets must be wrapped before use. */
  state?: Uint8Array | null;
}
export interface Delivery {hub: string; remoteId: string; id: string; channel: string; body: Uint8Array; present: boolean}
type EventRow = Omit<JournalEvent, 'sequence'>;
const SCHEMA = ['meta', 'clocks', 'outbox', 'received', 'cursors', 'events', 'actions', 'deletions'] as const;
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
    const opening = indexedDB.open(name, 2);
    opening.onupgradeneeded = event => {
      const db = opening.result;
      const upgrade = () => {
        for (const name of SCHEMA) {
          const store = db.objectStoreNames.contains(name) ? opening.transaction!.objectStore(name) : db.createObjectStore(name, name === 'events' ? {keyPath:'sequence',autoIncrement:true} : undefined);
          if (['events','outbox','received','actions','deletions'].includes(name) && !store.indexNames.contains('hub')) store.createIndex('hub','hub');
          if (name === 'events' && !store.indexNames.contains('hubSequence')) store.createIndex('hubSequence',['hub','sequence']);
        }
        if(event.oldVersion===0)opening.transaction!.objectStore('meta').put('tandem-v2','application');
      };
      if(event.oldVersion===0)upgrade();
      else if(!db.objectStoreNames.contains('meta'))opening.transaction!.abort();
      else {const marker=opening.transaction!.objectStore('meta').get('application');marker.onsuccess=()=>{if(marker.result!=='tandem-v2')opening.transaction!.abort();else upgrade();};}
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
  async state(hub: string): Promise<Uint8Array | undefined> {
    const value = await request(this.#db.transaction('meta').objectStore('meta').get(['device', hub])) as Uint8Array | undefined;
    return value === undefined ? undefined : Uint8Array.from(value);
  }
  async queue(hub: string, installation: Uint8Array, now: number, prepare: (dot: Dot) => LocalMessage): Promise<string> {
    const ids = await this.commit(hub, installation, now, tx => ({messages: [prepare(tx.nextDot())]}));
    return ids[0]!;
  }
  /** Synchronous preparation under an IndexedDB write lock. All dots, exact MIME
   * bytes, action state and replay events commit together, or all roll back. */
  async commit(hub: string, installation: Uint8Array, now: number, prepare: (tx: LocalTransaction) => LocalPlan): Promise<string[]> {
    if (installation.length !== 8 || !Number.isInteger(now) || now < 0 || now >= 2**48 || !hub) throw new Error('invalid installation, clock or hub');
    const localID = Uint8Array.from(installation);
    const tx = this.#db.transaction(['meta', 'clocks', 'outbox', 'events', 'actions', 'deletions'], 'readwrite', {durability: 'strict'}), done = finished(tx);
    void done.catch(() => {});
    try {
      const clockKey = b64u(localID), clocks = tx.objectStore('clocks'), meta = tx.objectStore('meta');
      let clock = await request(clocks.get(clockKey)) as {millis: number; counter: number} | undefined;
      const active = await request(meta.get('installation')) as Uint8Array | undefined;
      if (active && b64u(active) !== clockKey) throw new Error('stale installation; reload its ID before queueing');
      if (!clock && await request(meta.get('installation.committed')) === true) throw new Error('installation clock was lost; obtain a fresh installation ID');
      const state = await request(meta.get(['device', hub])) as Uint8Array | undefined;
      const latest = await request(tx.objectStore('events').index('hubSequence').openCursor(IDBKeyRange.bound([hub, 0], [hub, Number.MAX_SAFE_INTEGER]), 'prev'));
      const sequence = latest ? (latest.value as JournalEvent).sequence : 0;
      const savedActions = await request(tx.objectStore('actions').index('hub').getAll(hub)) as JournalAction[];
      const actions = new Map(savedActions.map(action => [action.id, Uint8Array.from(action.sealed)]));
      let reserved = false, preparing = true;
      const plan = (() => {
        try { return prepare({sequence, clock:clock?{...clock}:undefined, actions, state: state === undefined ? undefined : Uint8Array.from(state), nextDot: () => {
          if (!preparing) throw new Error('clock reservation outside preparation');
          clock = clock ? nextClock(clock.millis, clock.counter, now) : {millis: now, counter: 0}; reserved = true;
          return {...clock, device: Uint8Array.from(localID)};
        }}); } finally { preparing = false; }
      })();
      if (!plan || typeof plan !== 'object' || 'then' in plan || !Array.isArray(plan.messages)) throw new Error('commit preparation must return a synchronous plan');
      // Snapshot before the first await: caller mutation cannot change a commit.
      const messages = plan.messages.map(message => {
        checkChannel(message.channel); if (!validEnvelope(message.body)) throw new Error('invalid envelope');
        const body = Uint8Array.from(message.body); return {channel: message.channel, body, id: b64u(sha(body)), ...(message.guard?{guard:structuredClone(message.guard)}:{}), ...(message.deferred?{deferred:true}:{})};
      });
      const updated = plan.state === undefined ? undefined : plan.state === null ? null : Uint8Array.from(plan.state);
      if (!active) meta.put(localID, 'installation');
      if (reserved) { clocks.put(clock, clockKey); meta.put(true, 'installation.committed'); }
      if (updated === null) meta.delete(['device', hub]); else if (updated !== undefined) meta.put(updated, ['device', hub]);
      const actionUpdates = (plan.actions ?? []).map(action => {if(!action.id)throw new Error('action ID required');return {id:action.id,sealed:action.sealed===null?null:Uint8Array.from(action.sealed)};});
      for(const action of actionUpdates){if(action.sealed===null)tx.objectStore('actions').delete([hub,action.id]);else tx.objectStore('actions').put({id:action.id,hub,sealed:action.sealed} satisfies JournalAction,[hub,action.id]);}
      const outbox = tx.objectStore('outbox'), events = tx.objectStore('events'); let queued = false;
      for(const id of plan.cancel??[])outbox.delete([hub,id]);
      for(const id of plan.destroy??[]){if(!id)throw new Error('delivery ID required');tx.objectStore('deletions').put({hub,id} satisfies PendingDelete,[hub,id]);}
      for (const message of messages) {
        const previous = await request(outbox.get([hub, message.id])) as OutboxEntry | undefined;
        if (previous && previous.channel !== message.channel) throw new Error('queued channel changed');
        if (!previous) {
          const entry: OutboxEntry = {...message, hub, wire: buildMessage(message.channel, message.body)};
          outbox.put(entry, [hub, entry.id]); events.add({hub, kind: 'queued', id: entry.id, channel: entry.channel, body: entry.body, ...(entry.deferred?{deferred:true}:{})} satisfies EventRow); queued = true;
        }
      }
      await done; if (queued || reserved || updated !== undefined || !active || actionUpdates.length || plan.cancel?.length || plan.destroy?.length) this.#notify(queued || !!plan.destroy?.length); return messages.map(message => message.id);
    } catch (error) { try { tx.abort(); } catch {} await done.catch(() => {}); throw error; }
  }
  async cursor(hub: string): Promise<string> { return (await request(this.#db.transaction('cursors').objectStore('cursors').get(hub)) as string | undefined) ?? ''; }
  async pending(hub: string, limit = 128): Promise<OutboxEntry[]> {
    if (!Number.isSafeInteger(limit) || limit < 1) throw new Error('invalid pending page limit');
    return await request(this.#db.transaction('outbox').objectStore('outbox').index('hub').getAll(hub, limit)) as OutboxEntry[];
  }
  async pendingIDs(hub:string):Promise<Set<string>>{const keys=await request(this.#db.transaction('outbox').objectStore('outbox').index('hub').getAllKeys(hub));return new Set(keys.map(key=>(key as IDBValidKey[])[1] as string));}
  async queued(hub:string,ids:readonly string[]):Promise<Set<string>>{
    const store=this.#db.transaction('outbox').objectStore('outbox');
    const keys=await Promise.all(ids.map(id=>request(store.getKey([hub,id]))));return new Set(ids.filter((_,index)=>keys[index]!==undefined));
  }
  async markAppended(hub: string, id: string, remoteId: string): Promise<void> {
    const tx = this.#db.transaction('outbox', 'readwrite', {durability: 'strict'}), done = finished(tx), store = tx.objectStore('outbox');
    const entry = await request(store.get([hub, id])) as OutboxEntry | undefined;
    if (entry) store.put({...entry, remoteId}, [hub, id]); await done;
  }
  async apply(hub: string, batch: ChangeBatch): Promise<void> {
    batch = structuredClone(batch);
    const tx = this.#db.transaction(['received', 'outbox', 'cursors', 'events', 'deletions'], 'readwrite', {durability: 'strict'}), done = finished(tx);
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
        tx.objectStore('deletions').delete([hub,remoteId]);
        const old = await request(deliveries.get([hub, remoteId])) as Delivery | undefined;
        if (old?.present) { deliveries.put({...old, present: false}, [hub, remoteId]); events.add({hub, kind: 'removed', id: old.id, channel: old.channel, body: old.body, remoteId} satisfies EventRow); }
      }
      tx.objectStore('cursors').put(batch.cursor, hub); await done; this.#notify(false);
    } catch (error) { try { tx.abort(); } catch {} await done.catch(() => {}); throw error; }
  }
  async actions(hub:string):Promise<JournalAction[]>{return await request(this.#db.transaction('actions').objectStore('actions').index('hub').getAll(hub)) as JournalAction[];}
  async deliveries(hub:string):Promise<Delivery[]>{return await request(this.#db.transaction('received').objectStore('received').index('hub').getAll(hub)) as Delivery[];}
  async pendingDeletes(hub:string,limit=128):Promise<PendingDelete[]>{return await request(this.#db.transaction('deletions').objectStore('deletions').index('hub').getAll(hub,limit)) as PendingDelete[];}
  async pendingDeleteIDs(hub:string):Promise<Set<string>>{const keys=await request(this.#db.transaction('deletions').objectStore('deletions').index('hub').getAllKeys(hub));return new Set(keys.map(key=>(key as IDBValidKey[])[1] as string));}
  async confirmAbsent(hub:string,ids:readonly string[]):Promise<void>{
    const tx=this.#db.transaction(['received','deletions'],'readwrite',{durability:'strict'}),done=finished(tx);let changed=false;
    for(const id of ids){const delivery=await request(tx.objectStore('received').get([hub,id])) as Delivery|undefined;if(!delivery?.present){tx.objectStore('deletions').delete([hub,id]);changed=true;}}
    await done;if(changed)this.#notify(false);
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
