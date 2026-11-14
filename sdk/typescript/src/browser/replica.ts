import type {PushStore} from '../session.js';
import {BrowserJournal, type JournalEvent, type OutboxEntry} from './journal.js';
import {NetworkFailure, SnapshotChanged} from './jmap.js';

export interface PreparedStore extends PushStore {appendPrepared(wire: Uint8Array, signal: AbortSignal): Promise<string>;readonly deleteBatchSize?:number;destroy?(ids:readonly string[],signal:AbortSignal):Promise<void>}
export interface ReplicaHooks {beforeExport?:()=>Promise<void>;allowExport?:(entry:OutboxEntry)=>boolean|Promise<boolean>}
export function untilAbort<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const abort = () => reject(signal.reason); signal.addEventListener('abort', abort, {once: true});
    work.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
  });
}
/** Durable envelope pump. Decryption and device/roster authority are a separate layer. */
export class BrowserReplica {
  readonly #journal: BrowserJournal; readonly #hub: string; #started = false;
  constructor(journal: BrowserJournal, hub: string) { if (!hub) throw new Error('hub required'); this.#journal = journal; this.#hub = hub; }
  async run(connect: () => Promise<PreparedStore>, signal: AbortSignal,hooks:ReplicaHooks={}): Promise<void> {
    if (this.#started) throw new Error('replica already started'); this.#started = true;
    // One live owner per profile/database/hub. A closed tab releases the lock;
    // another tab takes over through the browser's lock event, never a timer.
    try { await navigator.locks.request(`tandem:runner:${JSON.stringify([this.#journal.name,this.#hub])}`, {signal}, async () => {
      const store = await connect();
      try {
        const changes = (await store.watch(signal))[Symbol.asyncIterator]();
        let pushed = changes.next();
        void pushed.catch(() => {});
        const reconcile = async () => {
          let more: boolean; const seen = new Set<string>();
          do {
            const previous = await this.#journal.cursor(this.#hub); seen.add(previous);
            const batch = await store.changes(previous, signal); more = batch.more === true;
            if (more && seen.has(batch.cursor)) throw new Error('unfinished change scan made no cursor progress');
            await this.#journal.apply(this.#hub, batch);
          } while (more);
        };
        let needsSync = true;
        while (!signal.aborted) {
          const committed = this.#journal.outboxChanged();
          try {
            if (needsSync) { await reconcile(); needsSync = false; }
            let after:string|undefined;
            exportQueued: while (true) {
              const pending = await this.#journal.pending(this.#hub,128,after);
              if (!pending.length) break;
              let sent=false;
              for (const entry of pending) {
                await hooks.beforeExport?.();
                if(hooks.allowExport&&!await hooks.allowExport(entry))continue;
                // A previous reconciliation may have confirmed later snapshot entries.
                if (!(await this.#journal.queued(this.#hub,[entry.id])).has(entry.id)) continue;
                sent=true;const id = await store.appendPrepared(entry.wire, signal);
                await this.#journal.markAppended(this.#hub, entry.id, id); await reconcile();
                if ((await this.#journal.queued(this.#hub,[entry.id])).has(entry.id)) break exportQueued;
              }
              if(sent){after=undefined;continue;}
              if(pending.length<128)break;
              after=pending[pending.length-1]!.id;
            }
            await hooks.beforeExport?.();
            while(true){
              const deletions=await this.#journal.pendingDeletes(this.#hub,store.deleteBatchSize??128);if(!deletions.length)break;
              if(!store.destroy)throw new Error('transport does not support durable deletion');
              await store.destroy(deletions.map(row=>row.id),signal);await reconcile();await this.#journal.confirmAbsent(this.#hub,deletions.map(row=>row.id));
              const still=await this.#journal.pendingDeletes(this.#hub,store.deleteBatchSize??128);if(still.some(row=>deletions.some(previous=>previous.id===row.id)))break;
            }
          } catch (error) { if (!(error instanceof NetworkFailure) && !(error instanceof SnapshotChanged)) throw error; needsSync = true; }
          const reason = await untilAbort(Promise.race([pushed.then(value => ({kind: 'push' as const, value})), committed.then(() => ({kind: 'commit' as const}))]), signal);
          if (reason.kind === 'push') { if (reason.value.done) throw new Error('push subscription ended'); pushed = changes.next(); void pushed.catch(() => {}); needsSync = true; }
        }
      } finally { await store.close(); }
    }); } finally {this.#started=false;}
  }
  async *events(after: number, signal: AbortSignal): AsyncGenerator<JournalEvent> {
    while (!signal.aborted) {
      const changed = this.#journal.eventsChanged();
      const rows = await this.#journal.readEvents(this.#hub, after);
      for (const row of rows) { signal.throwIfAborted(); yield row; after = row.sequence; }
      if (rows.length === 128) continue;
      await untilAbort(changed, signal);
    }
  }
}
