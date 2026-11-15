import {Buffer} from 'buffer';
import {b64u, unb64u, bytes, sha, randomBytes, hashStream, encrypt, decrypt} from '../crypto.js';
import {parseJSON} from '../json.js';
import {type Manifest, type ChunkEntry, parseManifest, encodeManifest, chunkKey, sealChunk, openChunk, chunkAddress, DEFAULT_CHUNK, DEFAULT_INLINE, MAX_CHUNK} from '../blob.js';
import {type BlobFolder, type BlobQuota, BlobFolderFull} from '../blob-folder.js';
import {type Write, type Dot, parseKind1, recordPayload, writeID} from '../core.js';
import type {DeviceState} from '../device-state.js';
import type {MailboxPresence} from '../operation.js';
import type {BrowserJournal, ChunkRow, LocalPlan, LocalTransaction} from './journal.js';

// Files on a browser device (DESIGN.md §17), as the Swift engine has them. A file attached here is a pending write
// (§17.6): signed, kept in the journal's blob store and out of the verified view until every chunk is on the hub,
// then merged and published like any edit. Content lives in the Origin Private File System; the journal holds the
// upload records (sealed under a local key), the chunk index and small markers. One tab at a time runs the worker,
// under a Web Lock; it starts when there is work (an attach, a sync, `useBlobs`, `checkBlobs`, the browser coming
// online) and stops when there is none. Nothing waits on a timer.

export type BlobState = 'here' | 'remote' | 'downloading' | 'unavailable' | 'unreadable' | 'uploading';
/** A file attached to a record, as this device sees it (§17.8): `done` of `chunks` are here. */
export interface BlobInfo {name: string; state: BlobState; size: number; hash: Uint8Array | undefined; chunks: number; done: number}
/** The per-hub settings of §17, changeable at runtime. Sizes in bytes, the grace in milliseconds. */
export interface BlobSettings {
  /** How a file is cut, at most 4 MiB; 1 MiB by default. */
  chunkSize: number;
  /** The largest content kept in the manifest itself; 16 KiB by default. */
  inline: number;
  /** How long this device must have known an orphan chunk before it collects it; 7 days by default. */
  orphanGrace: number;
  /** Content kept on this device; what was opened longest ago goes first. 2 GiB by default. */
  storeLimit: number;
  /** The largest file fetched without being asked, in lists set to keep files; 25 MiB by default. */
  unasked: number;
}
export const defaultBlobSettings = (): BlobSettings => ({chunkSize: DEFAULT_CHUNK, inline: DEFAULT_INLINE, orphanGrace: 7 * 24 * 3600 * 1000, storeLimit: 2 * 1024 ** 3, unasked: 25 * 1024 ** 2});
function settled(s: Partial<BlobSettings>, base: BlobSettings): BlobSettings {
  const out = {...base, ...Object.fromEntries(Object.entries(s).filter(([, v]) => v !== undefined))} as BlobSettings;
  for (const v of Object.values(out)) if (!Number.isSafeInteger(v) || v < 0) throw new Error('invalid blob setting');
  if (out.chunkSize < 1 || out.chunkSize > MAX_CHUNK) throw new Error('invalid chunk size');
  return out;
}
export class BlobError extends Error {
  constructor(readonly reason: 'unavailable' | 'unreadable' | 'no-such-file' | 'no-blobs') {
    super({unavailable: 'a chunk of the file is not on the hub; a device that holds the file restores it', unreadable: 'the field holds no manifest this device reads, or its chunks do not match it',
      'no-such-file': 'no file in that field', 'no-blobs': 'useBlobs has not been called'}[reason]);
  }
}

/** What the device lends the blob runtime. */
export interface BlobHost {
  readonly hub: Uint8Array; readonly journal: BrowserJournal; readonly localKey: Uint8Array; readonly lockName: string;
  view(): DeviceState | undefined;
  presence(): MailboxPresence[];
  /** The device's locked, replayed journal commit. */
  commit(prepare: (state: DeviceState, reserve: () => Dot, tx: LocalTransaction) => LocalPlan): Promise<void>;
  /** An upload or download started, moved or ended in this tab or another. */
  changed(collection: Uint8Array, records: readonly Uint8Array[]): void;
  /** A problem the app can show, or undefined once the worker got through. */
  problem(message: string | undefined): void;
}

interface Upload {write: string; scope: Buffer; record: Buffer; field: string; epoch: Buffer; payload: Buffer; r: Buffer; staged: string; w: Write; m: Manifest}
interface Scope {cid: Buffer; tags: Set<string>; current?: string; collect: boolean; orphans: boolean; protected: Set<string>; listed: Set<string>; live: {id: Buffer; record: Buffer; m: Manifest}[]; named: Set<string>}
const hex = (b: Uint8Array) => Buffer.from(b).toString('hex');
const SEAL_LABEL = 'TANDEM-LOCAL-BLOB-V1';
const FIELD = '_blob:';
const isAbort = (error: unknown) => error instanceof DOMException && error.name === 'AbortError';

async function directory(parent: FileSystemDirectoryHandle, name: string): Promise<FileSystemDirectoryHandle> { return parent.getDirectoryHandle(name, {create: true}); }
async function maybeFile(dir: FileSystemDirectoryHandle, name: string): Promise<File | undefined> {
  try { return await (await dir.getFileHandle(name)).getFile(); } catch (error) { if (error instanceof DOMException && (error.name === 'NotFoundError' || error.name === 'TypeMismatchError')) return undefined; throw error; }
}
async function remove(dir: FileSystemDirectoryHandle, name: string): Promise<void> {
  try { await dir.removeEntry(name, {recursive: true}); } catch (error) { if (!(error instanceof DOMException && error.name === 'NotFoundError')) throw error; }
}
async function names(dir: FileSystemDirectoryHandle): Promise<string[]> {
  const out: string[] = []; for await (const name of (dir as unknown as {keys(): AsyncIterable<string>}).keys()) out.push(name); return out;
}
async function writeFile(dir: FileSystemDirectoryHandle, name: string, data: Blob | Uint8Array): Promise<void> {
  const writable = await (await dir.getFileHandle(name, {create: true})).createWritable();
  try { if (data instanceof Blob) await data.stream().pipeTo(writable); else { await writable.write(Uint8Array.from(data)); await writable.close(); } }
  catch (error) { await writable.abort().catch(() => {}); throw error; }
}
const slice = async (file: Blob, offset: number, length: number): Promise<Buffer> => Buffer.from(await file.slice(offset, offset + length).arrayBuffer());

/** The blob worker and content store of one device. Created by `BrowserDevice.useBlobs`. */
export class BlobRuntime {
  readonly #host: BlobHost; readonly #folder: BlobFolder; readonly #hub: string; readonly #abort = new AbortController();
  readonly #root: FileSystemDirectoryHandle; readonly #content: FileSystemDirectoryHandle; readonly #staged: FileSystemDirectoryHandle; readonly #partial: FileSystemDirectoryHandle;
  readonly #channel: BroadcastChannel; #settings: BlobSettings;
  // Mirrors of what is durable, so that `blob()` answers at once; reloaded when another tab reports a change.
  #uploads = new Map<string, Upload>(); #contents = new Map<string, number>(); #unreadable = new Set<string>(); #unavailable = new Set<string>(); #keep = new Set<string>();
  readonly #downloads = new Map<string, {chunks: number; done: number; running: Promise<void>}>();
  #kicked = false; #running: Promise<void> | undefined; #waiters: (() => void)[] = [];
  #hygiene = true; #healed = false; #view = ''; #closed = false; #failed = false;
  readonly #online = () => this.wake();

  private constructor(host: BlobHost, folder: BlobFolder, settings: BlobSettings, root: FileSystemDirectoryHandle, content: FileSystemDirectoryHandle, staged: FileSystemDirectoryHandle, partial: FileSystemDirectoryHandle) {
    this.#host = host; this.#folder = folder; this.#settings = settings; this.#hub = b64u(host.hub);
    this.#root = root; this.#content = content; this.#staged = staged; this.#partial = partial;
    this.#channel = new BroadcastChannel(`tandem:blobs:${JSON.stringify([host.journal.name, this.#hub])}`);
    this.#channel.onmessage = event => { void this.#load().then(() => { const d = event.data as {collection?: string; records?: string[]}; if (typeof d?.collection === 'string' && Array.isArray(d.records)) this.#host.changed(unb64u(d.collection), d.records.map(r => unb64u(r))); }, () => {}); };
    globalThis.addEventListener?.('online', this.#online);
  }
  static async open(host: BlobHost, folder: BlobFolder, settings: Partial<BlobSettings> = {}): Promise<BlobRuntime> {
    const opfs = await navigator.storage.getDirectory(), base = await directory(await directory(opfs, 'tandem-blobs'), encodeURIComponent(host.journal.name));
    const root = await directory(base, b64u(host.hub));
    const runtime = new BlobRuntime(host, folder, settled(settings, defaultBlobSettings()), root, await directory(root, 'content'), await directory(root, 'staged'), await directory(root, 'partial'));
    await runtime.#load(); runtime.wake(); return runtime;
  }
  get settings(): BlobSettings { return {...this.#settings}; }
  set settings(value: Partial<BlobSettings>) { this.#settings = settled(value, this.#settings); }
  /** The content store's directory in the Origin Private File System. */
  get directory(): FileSystemDirectoryHandle { return this.#root; }
  get #signal(): AbortSignal { return this.#abort.signal; }
  #lock(name: string): string { return `tandem:blobs:${name}:${JSON.stringify([this.#host.journal.name, this.#hub])}`; }

  // MARK: Durable state

  #aad(key: string): Buffer { return Buffer.concat([Buffer.from(SEAL_LABEL), bytes(this.#host.hub, 32), Buffer.from(key)]); }
  #seal(key: string, u: Omit<Upload, 'write' | 'w' | 'm'>): Uint8Array {
    const plain = Buffer.from(JSON.stringify({v: 1, scope: b64u(u.scope), record: b64u(u.record), field: u.field, epoch: b64u(u.epoch), payload: b64u(u.payload), r: b64u(u.r), staged: u.staged}));
    const nonce = randomBytes(12); return Uint8Array.from(Buffer.concat([nonce, encrypt(bytes(this.#host.localKey, 32), nonce, this.#aad(key), plain)]));
  }
  #open(key: string, sealed: unknown): Upload | undefined {
    if (!(sealed instanceof Uint8Array) || sealed.length < 28) return undefined;
    let j: Record<string, unknown>;
    try { j = parseJSON(decrypt(bytes(this.#host.localKey, 32), sealed.subarray(0, 12), this.#aad(key), sealed.subarray(12))) as Record<string, unknown>; } catch { return undefined; }
    if (j.v !== 1 || typeof j.field !== 'string' || typeof j.staged !== 'string' || !/^new-[0-9a-f]{32}$/.test(j.staged)) return undefined;
    try {
      const scope = bytes(unb64u(j.scope as string), 16), record = bytes(unb64u(j.record as string), 16), payload = unb64u(j.payload as string);
      const parsed = parseKind1(payload, this.#host.hub, scope), w = 'writes' in parsed ? parsed.writes[0] : undefined;
      const p = w && parseManifest(w.value);
      if (!w || !p || p.kind !== 'manifest' || !p.manifest.chunks || b64u(writeID(w)) !== key.slice('upload:'.length)) return undefined;
      return {write: key.slice('upload:'.length), scope, record, field: j.field, epoch: bytes(unb64u(j.epoch as string), 32), payload, r: bytes(unb64u(j.r as string), 32), staged: j.staged, w, m: p.manifest};
    } catch { return undefined; }
  }
  async #load(): Promise<void> {
    const j = this.#host.journal, hub = this.#hub, uploads = new Map<string, Upload>();
    for (const [key, sealed] of await j.blobValues(hub, 'upload:')) { const u = this.#open(key, sealed); if (u) uploads.set(u.write, u); }
    this.#uploads = uploads;
    this.#unreadable = new Set((await j.blobValues(hub, 'unreadable:')).map(([k]) => k.slice('unreadable:'.length)));
    this.#unavailable = new Set((await j.blobValues(hub, 'unavailable:')).map(([k]) => k.slice('unavailable:'.length)));
    this.#keep = new Set((await j.blobValues(hub, 'keep:')).map(([k]) => k.slice('keep:'.length)));
    const contents = new Map<string, number>();
    for (const name of await names(this.#content)) { if (!/^[0-9a-f]{64}$/.test(name)) continue; const file = await maybeFile(this.#content, name); if (file?.size) contents.set(name, file.size); }
    this.#contents = contents;
  }
  /** Tells the app in this tab and every other tab that files of a record changed. */
  #changed(collection: Uint8Array, records: readonly Uint8Array[]): void {
    this.#host.changed(collection, records);
    this.#channel.postMessage({collection: b64u(collection), records: records.map(r => b64u(r))});
  }
  async #set(entries: {key: string; value: unknown}[]): Promise<void> { await this.#host.journal.setBlobValues(this.#hub, entries); }
  async #touch(name: string): Promise<void> { await this.#set([{key: `opened:${name}`, value: Date.now()}]); }

  // MARK: The worker

  /** Asks the worker for a round; it runs in one tab at a time. */
  wake(): void {
    if (this.#closed) return;
    this.#kicked = true;
    this.#running ??= this.#rounds();
  }
  /** After a sync: a clean-up and healing pass (§17.7, §17.8). */
  afterSync(): void { this.#hygiene = true; this.wake(); }
  /** Check that chunks of files this device holds are still on the hub, as after a start (§17.8). */
  check(): void { this.#healed = false; this.#hygiene = true; this.wake(); }
  /** Wakes the worker and resolves once it has run a whole round after now. */
  async settle(): Promise<void> { if (this.#closed) return; const done = new Promise<void>(resolve => this.#waiters.push(resolve)); this.wake(); await done; }
  async #rounds(): Promise<void> {
    try {
      while (this.#kicked && !this.#closed) {
        this.#kicked = false;
        await navigator.locks.request(this.#lock('worker'), {signal: this.#signal}, async () => {
          await this.#load();
          try { while (!this.#closed && await this.#round()); if (this.#failed) { this.#failed = false; this.#host.problem(undefined); } }
          catch (error) { if (!this.#closed) { this.#failed = true; this.#host.problem(error instanceof Error ? error.message : String(error)); } }
        }).catch(error => { if (!isAbort(error) && !this.#closed) throw error; });
      }
    } finally {
      this.#running = undefined;
      const waiters = this.#waiters; this.#waiters = []; for (const w of waiters) w();
      if (this.#kicked && !this.#closed) this.wake();
    }
  }
  /** One round of work; whether there was any that calls for another. */
  async #round(): Promise<boolean> {
    const view = this.#host.view(); if (!view?.certificate) return false;
    if (this.#uploads.size) { const before = this.#uploads.size; await this.#uploadAll(view); await this.#load(); return this.#uploads.size < before; }
    if (!this.#hygiene && this.#healed) return false;
    this.#hygiene = false;
    await this.#cleanup(view);
    await this.#keepFiles(view);
    return false;
  }
  async close(): Promise<void> {
    if (this.#closed) return; this.#closed = true; this.#abort.abort();
    globalThis.removeEventListener?.('online', this.#online); this.#channel.close();
    await this.#running?.catch(() => {});
    await Promise.all([...this.#downloads.values()].map(d => d.running.catch(() => {})));
    await this.#folder.close?.();
  }

  // MARK: Attaching (§17.6)

  /** Removes the pending writes of a field in the same commit, keeping their addresses for clean-up. */
  #dropOps(tx: LocalTransaction, cid: Uint8Array, record: Uint8Array, field: string | undefined, staged: string[]): {key: string; value: unknown}[] {
    const out: {key: string; value: unknown}[] = [];
    for (const [key, sealed] of tx.uploads) {
      const u = this.#open(key, sealed); if (!u || !u.scope.equals(cid) || !u.record.equals(record) || (field !== undefined && u.field !== field)) continue;
      out.push({key, value: null}, {key: `dropped:${b64u(u.scope)}:${u.write}`, value: Uint8Array.from(Buffer.concat(u.m.chunks!.map(e => e.address)))});
      staged.push(u.staged);
    }
    return out;
  }
  /** Removes the staged copies of dropped writes; the next clean-up pass collects the chunks they had up. */
  async #afterDrop(staged: readonly string[]): Promise<void> { for (const name of staged) await remove(this.#staged, name); if (staged.length) this.#hygiene = true; }

  /** Attaches a copy of `file` to a record as `_blob:<name>`. Up to the inline size it is written at once; anything
   * larger is pending until its chunks are on the hub, and shows as uploading meanwhile. It works offline. */
  async attach(collection: Uint8Array, record: Uint8Array, name: string, file: Blob): Promise<void> {
    const cid = bytes(collection, 16), rid = bytes(record, 16), field = FIELD + name, settings = this.#settings;
    if (!name) throw new Error('blob: a name is needed');
    if (file.size <= settings.inline) {
      const content = Buffer.from(await file.arrayBuffer()), value = encodeManifest({size: content.length, hash: sha(content), data: content});
      let staged: string[] = [];
      await this.#host.commit((state, reserve, tx) => { staged = []; const blobs = this.#dropOps(tx, cid, rid, field, staged); return {messages: state.prepareField(cid, rid, [{field, op: 1, value}], reserve), blobs}; });
      await this.#afterDrop(staged); await this.#load(); this.#changed(cid, [rid]); return;
    }
    // Stage, holding the stage lock shared, so that clean-up does not take the copy for a leftover.
    await navigator.locks.request(this.#lock('stage'), {mode: 'shared'}, async () => {
      const stagedName = `new-${randomBytes(16).toString('hex')}`;
      let kept = false;
      try {
        await writeFile(this.#staged, stagedName, file);
        const copy = await maybeFile(this.#staged, stagedName);
        if (!copy) throw new Error('blob: the staged copy is gone');
        const r = randomBytes(32), manifest = await this.#describe(copy, r, settings.chunkSize), value = encodeManifest(manifest);
        let staged: string[] = [];
        await this.#host.commit((state, reserve, tx) => {
          staged = [];
          const prepared = state.prepareBlobWrite(cid, rid, field, value, reserve), key = `upload:${b64u(writeID(prepared.write))}`;
          const blobs = [...this.#dropOps(tx, cid, rid, field, staged), {key, value: this.#seal(key, {scope: cid, record: rid, field, epoch: Buffer.from(prepared.epoch), payload: recordPayload(rid, [prepared.write]), r, staged: stagedName})}];
          return {messages: prepared.messages, blobs};
        });
        kept = true;
        await this.#afterDrop(staged);
      } finally { if (!kept) await remove(this.#staged, stagedName).catch(() => {}); }
    });
    await this.#load(); this.#changed(cid, [rid]); this.wake();
  }
  /** Drops a pending attachment of the field: its write is never published, and its chunks are collected. */
  async cancelAttach(collection: Uint8Array, record: Uint8Array, name: string): Promise<void> {
    const cid = bytes(collection, 16), rid = bytes(record, 16); let staged: string[] = [];
    await this.#host.commit((_state, _reserve, tx) => { staged = []; return {messages: [], blobs: this.#dropOps(tx, cid, rid, FIELD + name, staged)}; });
    await this.#afterDrop(staged); await this.#load(); this.#changed(cid, [rid]); this.wake();
  }
  /** One pass over the content: cut it into chunks, seal each to learn its address, and hash the whole (§17.6, step 2). */
  async #describe(file: Blob, r: Uint8Array, chunkSize: number): Promise<Manifest> {
    const h = hashStream(), chunks: ChunkEntry[] = [];
    for (let offset = 0; offset < file.size; offset += chunkSize) {
      this.#signal.throwIfAborted();
      const piece = await slice(file, offset, chunkSize), key = chunkKey(r, piece);
      h.update(piece); chunks.push({address: chunkAddress(sealChunk(key, this.#host.hub, piece)), key, length: piece.length});
    }
    return {size: file.size, hash: Buffer.from(h.digest()), chunks};
  }

  // MARK: Uploading and committing (§17.6, steps 4 and 5)

  async #uploadFile(u: Upload): Promise<File | undefined> {
    return await maybeFile(this.#staged, u.staged) ?? (this.#contents.get(hex(u.m.hash)) === u.m.size ? await maybeFile(this.#content, hex(u.m.hash)) : undefined);
  }
  /** The operations that drop one pending write, if it is still pending. */
  #dropOne(tx: LocalTransaction, u: Upload, staged: string[]): {key: string; value: unknown}[] {
    const key = `upload:${u.write}`; if (!tx.uploads.has(key)) return [];
    staged.push(u.staged);
    return [{key, value: null}, {key: `dropped:${b64u(u.scope)}:${u.write}`, value: Uint8Array.from(Buffer.concat(u.m.chunks!.map(e => e.address)))}];
  }
  async #dropUpload(u: Upload): Promise<void> {
    let staged: string[] = [];
    await this.#host.commit((_state, _reserve, tx) => { staged = []; return {messages: [], blobs: this.#dropOne(tx, u, staged)}; });
    await this.#afterDrop(staged); this.#changed(u.scope, [u.record]);
  }
  async #uploadAll(view: DeviceState): Promise<void> {
    await this.#refreshIndex();
    const appended = new Map<string, string>();
    for (const u of this.#uploads.values()) {
      if (view.record(u.scope, u.record)?.deleted) { await this.#dropUpload(u); continue; }
      const tag = view.currentTag(u.scope), file = await this.#uploadFile(u);
      if (!file) { await this.#dropUpload(u); this.#host.problem('blob: the staged copy of an attached file is gone; it was dropped'); continue; }
      if (!tag) continue;
      const present = await this.#settleAddresses(u.m.chunks!.map(e => e.address));
      const {entries, offsets} = missing(u.m, present);
      await this.#appendChunks(file, tag, entries, offsets, appended);
    }
    await this.#refreshIndex();
    const verified: {id: string; state: 1}[] = [];
    for (const [id, address] of appended) if ((await this.#host.journal.chunks(this.#hub, address)).some(row => row.id === id)) verified.push({id, state: 1});
    await this.#host.journal.setChunkStates(this.#hub, verified);
    for (const u of this.#uploads.values()) {
      const present = await this.#settleAddresses(u.m.chunks!.map(e => e.address));
      if (u.m.chunks!.every(e => present.has(b64u(e.address)))) await this.#commit(u);
    }
  }
  /** Merges a pending write whose chunks are all on the hub into the view and publishes it (§17.6, step 5): from
   * here the content is a file this device holds. A record deleted meanwhile drops it. */
  async #commit(u: Upload): Promise<void> {
    const name = hex(u.m.hash);
    if (this.#contents.get(name) !== u.m.size) {
      const staged = await maybeFile(this.#staged, u.staged);
      if (staged) { await writeFile(this.#content, name, staged); this.#contents.set(name, u.m.size); }
    }
    let outcome = 'gone' as 'gone' | 'wait' | 'drop' | 'published', staged: string[] = [];
    await this.#host.commit((state, _reserve, tx) => {
      staged = []; const key = `upload:${u.write}`;
      if (!tx.uploads.has(key)) { outcome = 'gone'; return {messages: []}; }
      const prepared = state.prepareBlobCommit(u.scope, u.record, u.w);
      if (prepared.kind === 'wait') { outcome = 'wait'; return {messages: []}; }
      if (prepared.kind === 'drop') { outcome = 'drop'; return {messages: [], blobs: this.#dropOne(tx, u, staged)}; }
      outcome = 'published'; staged.push(u.staged);
      return {messages: prepared.messages, blobs: [{key, value: null}]};
    });
    if (outcome === 'wait') return;
    await this.#afterDrop(staged);
    if (outcome === 'published') await this.#touch(name);
    this.#changed(u.scope, [u.record]);
  }

  // MARK: The chunk index (§17.4.3)

  async #refreshIndex(): Promise<void> {
    await navigator.locks.request(this.#lock('index'), {signal: this.#signal}, async () => {
      const seen = new Set<string>();
      while (true) {
        const cursor = (await this.#host.journal.blobValue(this.#hub, 'cursor') as string | undefined) ?? '';
        if (seen.has(cursor)) throw new Error('blob folder: a page of changes made no progress'); seen.add(cursor);
        const page = await this.#folder.changes(cursor, this.#signal);
        await this.#host.journal.noteChunks(this.#hub, page.added.map(r => ({id: r.id, address: b64u(r.address), channel: r.channel})), page.removed, page.cursor, Date.now());
        if (!page.more) return;
      }
    });
  }
  #order(rows: ChunkRow[]): ChunkRow[] { const compare = this.#folder.compare; return compare ? [...rows].sort((a, b) => compare(a.id, b.id)) : rows; }
  /** For each address, a message this device verified as carrying it (the lowest, where IDs are ordered), fetching
   * unverified claims to verify or reject them (§17.4.3). Addresses without one are left out. */
  async #settleAddresses(addresses: readonly Uint8Array[]): Promise<Map<string, string>> {
    const out = new Map<string, string>(), done = new Set<string>(), wanted = [...new Set(addresses.map(a => b64u(a)))];
    while (true) {
      const fetch: string[] = [], claims = new Map<string, string>();
      for (const a of wanted) {
        if (done.has(a)) continue;
        const rows = this.#order(await this.#host.journal.chunks(this.#hub, a)), v = rows.find(r => r.state === 1), u = rows.find(r => r.state === 0);
        if (v) { out.set(a, v.id); done.add(a); } else if (u) { fetch.push(u.id); claims.set(u.id, a); } else done.add(a);
      }
      if (!fetch.length) return out;
      const got = new Map<string, Buffer>();
      for (let at = 0; at < fetch.length; at += 4) for (const c of await this.#folder.fetch(fetch.slice(at, at + 4), this.#signal)) got.set(c.id, c.envelope);
      await this.#host.journal.setChunkStates(this.#hub, fetch.map(id => { const e = got.get(id); return {id, state: e === undefined ? null : b64u(chunkAddress(e)) === claims.get(id) ? 1 : 2}; }));
    }
  }
  /** Seals entries again from a file that holds the content; each must seal to its address. */
  async #sealEntries(file: Blob, entries: readonly ChunkEntry[], offsets: readonly number[]): Promise<Buffer[]> {
    const out: Buffer[] = [];
    for (const [i, e] of entries.entries()) {
      const envelope = sealChunk(e.key, this.#host.hub, await slice(file, offsets[i]!, e.length));
      if (!chunkAddress(envelope).equals(e.address)) throw new Error('blob: the local copy does not seal to its manifest');
      out.push(envelope);
    }
    return out;
  }
  /** Seals chunks again from a file holding the content and appends them a few at a time, in the hub's form. The
   * first escaped chunk this device appends on the hub is fetched back and compared (§17.4.2). */
  async #appendChunks(file: Blob, tag: string, entries: readonly ChunkEntry[], offsets: readonly number[], appended: Map<string, string>): Promise<void> {
    for (let at = 0; at < entries.length; at += 4) {
      this.#signal.throwIfAborted();
      const envelopes = await this.#sealEntries(file, entries.slice(at, at + 4), offsets.slice(at, at + 4)), out = envelopes.map(envelope => ({channel: tag, envelope}));
      const form = await this.#host.journal.blobValue(this.#hub, 'form') === 'base64' ? 'base64' : 'escaped';
      let ids = await this.#folder.append(out, form, this.#signal);
      if (form === 'escaped' && ids[0] && await this.#checkForm(ids[0], envelopes[0]!)) {
        await this.#host.journal.setChunkStates(this.#hub, ids.filter(Boolean).map(id => ({id, state: 2 as const})));
        ids = await this.#folder.append(out, 'base64', this.#signal);
      }
      ids.forEach((id, k) => { if (id) appended.set(id, b64u(chunkAddress(envelopes[k]!))); });
    }
  }
  /** Fetches the first escaped chunk this device appends on the hub back, once; whether the server changed it. If so
   * the hub is written in base64 from then on. */
  async #checkForm(id: string, sent: Buffer): Promise<boolean> {
    if (await this.#host.journal.blobValue(this.#hub, 'formchecked') !== undefined) return false;
    const got = await this.#folder.fetch([id], this.#signal);
    if (!got.length) return false;   // gone already, or not readable yet: check the next one
    await this.#set([{key: 'formchecked', value: 1}]);
    if (got[0]!.envelope.equals(sent)) return false;
    await this.#set([{key: 'form', value: 'base64'}]);
    this.#host.problem('blob: the server changed the escaped form of a chunk; chunks go up in base64 on this hub from now on');
    return true;
  }

  // MARK: Reading (§17.8)

  /** What a device without `useBlobs` can say: inline files are here, others remote. */
  static peek(view: DeviceState | undefined, collection: Uint8Array, record: Uint8Array, name: string): BlobInfo | undefined {
    const r = view?.record(bytes(collection, 16), bytes(record, 16)), top = r && !r.deleted ? r.siblings(FIELD + name)[0] : undefined; if (!top) return undefined;
    const p = parseManifest(top.write.value); if (p.kind !== 'manifest') return {name, state: 'unreadable', size: 0, hash: undefined, chunks: 0, done: 0};
    return {name, state: p.manifest.chunks ? 'remote' : 'here', size: p.manifest.size, hash: Buffer.from(p.manifest.hash), chunks: p.manifest.chunks?.length ?? 0, done: 0};
  }
  /** The file in the field `_blob:<name>` of a record, as this device sees it now. */
  blob(view: DeviceState | undefined, collection: Uint8Array, record: Uint8Array, name: string): BlobInfo | undefined {
    const cid = bytes(collection, 16), rid = bytes(record, 16), field = FIELD + name;
    for (const u of this.#uploads.values()) if (u.scope.equals(cid) && u.record.equals(rid) && u.field === field) return {name, state: 'uploading', size: u.m.size, hash: Buffer.from(u.m.hash), chunks: u.m.chunks!.length, done: 0};
    const r = view?.record(cid, rid); if (!r || r.deleted) return undefined;
    const top = r.siblings(field)[0]; if (!top) return undefined;
    const p = parseManifest(top.write.value);
    if (p.kind !== 'manifest') return {name, state: 'unreadable', size: 0, hash: undefined, chunks: 0, done: 0};
    const m = p.manifest, chunks = m.chunks?.length ?? 0, info: BlobInfo = {name, state: 'remote', size: m.size, hash: Buffer.from(m.hash), chunks, done: 0};
    if (this.#unreadable.has(b64u(top.id))) return {...info, state: 'unreadable'};
    if (!m.chunks) return {...info, state: 'here'};
    const download = this.#downloads.get(hex(m.hash));
    if (this.#contents.get(hex(m.hash)) === m.size) return {...info, state: 'here', done: chunks};
    if (download) return {...info, state: 'downloading', done: download.done};
    if (this.#unavailable.has(b64u(top.id))) return {...info, state: 'unavailable'};
    return info;
  }
  /** The verified content of the field `_blob:<name>`, fetched if it is not here. A download that stops resumes. */
  async fetchBlob(view: DeviceState | undefined, collection: Uint8Array, record: Uint8Array, name: string, signal?: AbortSignal): Promise<Blob> {
    const cid = bytes(collection, 16), rid = bytes(record, 16), r = view?.record(cid, rid), top = r && !r.deleted ? r.siblings(FIELD + name)[0] : undefined;
    if (!top) throw new BlobError('no-such-file');
    const p = parseManifest(top.write.value);
    if (p.kind !== 'manifest' || this.#unreadable.has(b64u(top.id))) throw new BlobError('unreadable');
    return this.#fetchManifest(cid, rid, Buffer.from(top.id), p.manifest, signal);
  }
  async #contentFile(m: Manifest): Promise<File | undefined> {
    const file = await maybeFile(this.#content, hex(m.hash)); return file && file.size === m.size ? file : undefined;
  }
  async #fetchManifest(cid: Buffer, rid: Buffer, id: Buffer, m: Manifest, signal?: AbortSignal): Promise<Blob> {
    if (m.data) return new Blob([Uint8Array.from(m.data)]);
    const name = hex(m.hash), have = await this.#contentFile(m);
    if (have) { await this.#touch(name); return have; }
    let d = this.#downloads.get(name);
    if (!d) {
      const merged = signal ? AbortSignal.any([signal, this.#signal]) : this.#signal, entry = {chunks: m.chunks!.length, done: 0, running: Promise.resolve()};
      entry.running = navigator.locks.request(this.#lock(`download:${name}`), {signal: merged}, async () => {
        if (await this.#contentFile(m)) return;   // another tab fetched it meanwhile
        if (this.#unavailable.delete(b64u(id))) await this.#set([{key: `unavailable:${b64u(id)}`, value: null}]);
        this.#changed(cid, [rid]);
        try { await this.#download(cid, rid, id, m, entry, merged); }
        catch (error) {
          if (error instanceof BlobError && error.reason === 'unavailable') { this.#unavailable.add(b64u(id)); await this.#set([{key: `unavailable:${b64u(id)}`, value: 1}]); }
          if (error instanceof BlobError && error.reason === 'unreadable') { this.#unreadable.add(b64u(id)); await this.#set([{key: `unreadable:${b64u(id)}`, value: 1}]); }
          throw error;
        }
      }).finally(() => { this.#downloads.delete(name); this.#changed(cid, [rid]); }) as Promise<void>;
      this.#downloads.set(name, entry); d = entry;
    }
    await d.running;
    const file = await this.#contentFile(m); if (!file) throw new BlobError('unavailable');
    await this.#touch(name); await this.#evict(name);
    return file;
  }
  /** Fetches every chunk not yet in the partial directory, checks each against its address, its key and its length
   * before writing it, then writes the content and checks its hash before it becomes visible. */
  async #download(cid: Buffer, rid: Buffer, id: Buffer, m: Manifest, progress: {done: number}, signal: AbortSignal): Promise<void> {
    const name = hex(m.hash), partial = await directory(this.#partial, name), entries = m.chunks!;
    let todo: {e: ChunkEntry; i: number}[] = [];
    for (const [i, e] of entries.entries()) { const piece = await maybeFile(partial, String(i)); if (piece?.size === e.length) progress.done++; else todo.push({e, i}); }
    if (todo.length) await this.#refreshIndex();
    const tried = new Set<string>(), compare = this.#folder.compare;
    while (todo.length) {
      signal.throwIfAborted();
      // Up to four distinct addresses a round; an address repeated in the manifest is fetched once.
      const ids: string[] = [], wants = new Map<string, {e: ChunkEntry; i: number}[]>(), addressOf = new Map<string, string>(), byAddress = new Map<string, string>(), rest: typeof todo = [];
      for (const t of todo) {
        const a = b64u(t.e.address), same = byAddress.get(a);
        if (same) { wants.get(same)!.push(t); continue; }
        if (ids.length === 4) { rest.push(t); continue; }
        const rows = (await this.#host.journal.chunks(this.#hub, a)).sort((x, y) => (x.state === 1) !== (y.state === 1) ? (x.state === 1 ? -1 : 1) : compare ? compare(x.id, y.id) : 0);
        const candidate = rows.find(r => r.state !== 2 && !tried.has(r.id))?.id;
        if (!candidate) throw new BlobError('unavailable');
        tried.add(candidate); ids.push(candidate); wants.set(candidate, [t]); addressOf.set(candidate, a); byAddress.set(a, candidate);
      }
      const got = new Map((await this.#folder.fetch(ids, signal)).map(c => [c.id, c.envelope] as const)), states: {id: string; state: 1 | 2 | null}[] = [], next: typeof todo = [];
      for (const mid of ids) {
        const envelope = got.get(mid), good = !!envelope && b64u(chunkAddress(envelope)) === addressOf.get(mid);
        states.push({id: mid, state: envelope === undefined ? null : good ? 1 : 2});
        if (!good || !envelope) { next.push(...wants.get(mid)!); continue; }
        for (const t of wants.get(mid)!) {
          let plain: Buffer;
          try { plain = openChunk(t.e.key, this.#host.hub, envelope, t.e.length); } catch { await this.#host.journal.setChunkStates(this.#hub, states); throw new BlobError('unreadable'); }
          await writeFile(partial, String(t.i), plain); progress.done++;
        }
      }
      await this.#host.journal.setChunkStates(this.#hub, states);
      this.#changed(cid, [rid]);
      todo = [...next, ...rest];
    }
    const writable = await (await this.#content.getFileHandle(name, {create: true})).createWritable(), h = hashStream();
    try {
      for (const [i] of entries.entries()) { const piece = await maybeFile(partial, String(i)); if (!piece) throw new Error('blob: a downloaded chunk went missing'); const data = new Uint8Array(await piece.arrayBuffer()); h.update(data); await writable.write(data); }
      if (!Buffer.from(h.digest()).equals(m.hash)) { await writable.abort(); await remove(this.#content, name); await remove(this.#partial, name); throw new BlobError('unreadable'); }
      await writable.close();
    } catch (error) { await writable.abort().catch(() => {}); throw error; }
    await remove(this.#partial, name);
    this.#contents.set(name, m.size);
  }
  /** Removes content opened longest ago while the store is over its limit; it becomes remote again. The file just
   * opened stays, whatever its size. */
  async #evict(keep: string): Promise<void> {
    const opened = new Map((await this.#host.journal.blobValues(this.#hub, 'opened:')).map(([k, v]) => [k.slice('opened:'.length), typeof v === 'number' ? v : 0]));
    const files: {name: string; size: number; at: number}[] = []; let total = 0;
    for (const [name, size] of this.#contents) { files.push({name, size, at: opened.get(name) ?? 0}); total += size; }
    for (const f of files.sort((a, b) => a.at - b.at)) {
      if (total <= this.#settings.storeLimit) break;
      if (f.name === keep) continue;
      await remove(this.#content, f.name); this.#contents.delete(f.name); total -= f.size;
      await this.#set([{key: `opened:${f.name}`, value: null}]);
    }
  }

  // MARK: Keeping files (§17.8)

  /** Whether this device fetches every file of a list unasked, up to the `unasked` size. */
  async keepFiles(collection: Uint8Array, keep: boolean): Promise<void> {
    const key = `keep:${b64u(bytes(collection, 16))}`;
    await this.#set([{key, value: keep ? 1 : null}]);
    if (keep) { this.#keep.add(key.slice(5)); this.#hygiene = true; this.wake(); } else this.#keep.delete(key.slice(5));
  }
  async #keepFiles(view: DeviceState): Promise<void> {
    const jobs: {cid: Buffer; rid: Buffer; id: Buffer; m: Manifest}[] = [];
    for (const list of view.lists()) {
      if (!this.#keep.has(b64u(list.id))) continue;
      for (const rid of view.records(list.id)) {
        const r = view.record(list.id, rid); if (!r || r.deleted) continue;
        for (const e of r.kept) {
          if (e.write.op !== 1 || !e.write.field.startsWith(FIELD) || !r.siblings(e.write.field)[0]?.id.equals(e.id)) continue;
          const p = parseManifest(e.write.value);
          if (p.kind === 'manifest' && p.manifest.chunks && p.manifest.size <= this.#settings.unasked && this.#contents.get(hex(p.manifest.hash)) !== p.manifest.size && !this.#unreadable.has(b64u(e.id)))
            jobs.push({cid: Buffer.from(list.id), rid: Buffer.from(rid), id: Buffer.from(e.id), m: p.manifest});
        }
      }
    }
    for (const j of jobs) {
      try { await this.#fetchManifest(j.cid, j.rid, j.id, j.m); } catch (error) { if (!(error instanceof BlobError)) throw error; }
    }
  }

  /** The blob folder's storage quota. */
  async quota(): Promise<BlobQuota> { return this.#folder.quota(this.#signal); }

  // MARK: Collecting and healing (§17.7, §17.8)

  async #scopes(view: DeviceState): Promise<Scope[]> {
    const dropped = await this.#host.journal.blobValues(this.#hub, 'dropped:'), out: Scope[] = [];
    for (const v of view.blobScopes(this.#host.presence())) {
      const s: Scope = {cid: v.collection, tags: new Set(v.tags), ...(v.current ? {current: v.current} : {}), collect: v.collect, orphans: v.orphans, protected: new Set(), listed: new Set(), live: [], named: new Set()};
      const protect = (value: Uint8Array, live?: {id: Buffer; record: Buffer}) => {
        const p = parseManifest(value);
        if (p.kind === 'unknown') s.collect = false;   // it may list chunks this device can't tell (§17.7)
        if (p.kind !== 'manifest') return;
        for (const e of p.manifest.chunks ?? []) s.protected.add(b64u(e.address));
        if (live) { s.live.push({...live, m: p.manifest}); if (p.manifest.chunks) s.named.add(hex(p.manifest.hash)); }
      };
      for (const k of v.kept) protect(k.value, {id: k.id, record: k.record});
      for (const value of v.feed) protect(value);
      for (const u of this.#uploads.values()) if (u.scope.equals(s.cid)) { protect(u.w.value); s.named.add(hex(u.m.hash)); }
      for (const value of v.listed) { const p = parseManifest(value); if (p.kind === 'manifest') for (const e of p.manifest.chunks ?? []) s.listed.add(b64u(e.address)); }
      for (const [key, addresses] of dropped) if (key.startsWith(`dropped:${b64u(s.cid)}:`) && addresses instanceof Uint8Array) for (let i = 0; i + 32 <= addresses.length; i += 32) s.listed.add(b64u(addresses.subarray(i, i + 32)));
      s.orphans &&= s.collect;
      out.push(s);
    }
    return out;
  }
  /** What clean-up may destroy now by the rules of §17.7 that need no fetch. */
  #doomed(scopes: readonly Scope[], rows: readonly ChunkRow[], now: number): string[] {
    const byTag = new Map<string, Scope>(); for (const s of scopes) for (const t of s.tags) byTag.set(t, s);
    return rows.flatMap(r => {
      const s = byTag.get(r.channel); if (!s?.collect || r.state === 2 || s.protected.has(r.address)) return [];
      return s.listed.has(r.address) || (s.orphans && now - r.seen > this.#settings.orphanGrace) ? [r.id] : [];
    });
  }
  #scopeView(scopes: readonly Scope[]): string {
    return b64u(sha(Buffer.from(JSON.stringify(scopes.map(s => [b64u(s.cid), [...s.listed].sort(), [...s.protected].sort(), [...s.tags].sort(), s.collect, s.orphans])))));
  }
  /** The clean-up and healing pass, after a sync. It opens the blob folder only when there is something to collect,
   * when this device should check content it holds, or when what it lists or protects changed since it last looked. */
  async #cleanup(view: DeviceState): Promise<void> {
    const scopes = await this.#scopes(view);
    await this.#dropUnnamed(scopes);
    let rows = await this.#host.journal.chunks(this.#hub);
    const holds = scopes.some(s => s.live.some(l => l.m.chunks && this.#contents.get(hex(l.m.hash)) === l.m.size));
    const counts = new Map<string, number>(); for (const r of rows) if (r.state !== 2) counts.set(r.address, (counts.get(r.address) ?? 0) + 1);
    const copies = !!this.#folder.compare && [...counts.values()].some(n => n > 1), current = this.#scopeView(scopes), heal = !this.#healed;
    if (!this.#doomed(scopes, rows, Date.now()).length && !copies && !(heal && holds) && current === this.#view) { if (!holds) this.#healed = true; return; }
    await this.#refreshIndex();
    this.#view = current;
    rows = await this.#host.journal.chunks(this.#hub);
    const gone = new Set(this.#doomed(scopes, rows, Date.now()));
    // Copies (§17.7): every device keeps the lowest verified one. Only where IDs have one order for every device.
    const compare = this.#folder.compare;
    if (compare) {
      const byTag = new Map<string, Scope>(); for (const s of scopes) for (const t of s.tags) byTag.set(t, s);
      const groups = new Map<string, ChunkRow[]>();
      for (const r of rows) if (byTag.get(r.channel)?.collect && r.state !== 2 && !gone.has(r.id)) groups.set(r.address, [...groups.get(r.address) ?? [], r]);
      const several = [...groups].filter(([, g]) => g.length > 1).map(([a]) => a);
      if (several.length) {
        const winners = await this.#settleAddresses(several.map(a => unb64u(a)));
        for (const a of several) { const w = winners.get(a); if (w) for (const r of groups.get(a)!) if (r.id !== w && compare(w, r.id) < 0) gone.add(r.id); }
      }
    }
    if (gone.size) {
      const ids = [...gone].sort();
      for (let at = 0; at < ids.length; at += 64) await this.#folder.destroy(ids.slice(at, at + 64), this.#signal);
      await this.#host.journal.setChunkStates(this.#hub, ids.map(id => ({id, state: null})));
    }
    if (heal) { await this.#heal(scopes); this.#healed = true; }
    await this.#forgetDropped();
  }
  /** Restores the chunks of live manifests in current channels whose content this device holds (§17.8). A local copy
   * that seals to another address is checked against the manifest's hash: a good copy means the manifest is
   * inconsistent, which is marked unreadable; a bad one is dropped. */
  async #heal(scopes: readonly Scope[]): Promise<void> {
    for (const s of scopes) {
      if (!s.current) continue;
      for (const l of s.live) {
        if (!l.m.chunks || this.#contents.get(hex(l.m.hash)) !== l.m.size) continue;
        const file = await this.#contentFile(l.m); if (!file) continue;
        const present = await this.#settleAddresses(l.m.chunks.map(e => e.address)), {entries, offsets} = missing(l.m, present);
        if (!entries.length) continue;
        try { await this.#sealEntries(file, entries, offsets); } catch {
          const h = hashStream(); for (let at = 0; at < file.size; at += DEFAULT_CHUNK) h.update(await slice(file, at, DEFAULT_CHUNK));
          if (Buffer.from(h.digest()).equals(l.m.hash)) { this.#unreadable.add(b64u(l.id)); await this.#set([{key: `unreadable:${b64u(l.id)}`, value: 1}]); }   // keep the file
          else { await remove(this.#content, hex(l.m.hash)); this.#contents.delete(hex(l.m.hash)); }   // the local copy is damaged
          this.#changed(s.cid, [l.record]); continue;
        }
        await this.#appendChunks(file, s.current, entries, offsets, new Map());
      }
    }
  }
  /** Removes content no record names any more, downloaded or half downloaded: a deleted record's, a replaced
   * version's, and what was kept about it. A gone list's files stay as long as its records do. Staged copies no
   * pending write names go too, unless an attach is staging one now. */
  async #dropUnnamed(scopes: readonly Scope[]): Promise<void> {
    const named = new Set<string>(), writes = new Set<string>();
    for (const s of scopes) { for (const n of s.named) named.add(n); for (const l of s.live) writes.add(b64u(l.id)); }
    for (const u of this.#uploads.values()) named.add(hex(u.m.hash));
    for (const name of [...this.#contents.keys()]) if (!named.has(name)) { await remove(this.#content, name); this.#contents.delete(name); }
    for (const name of await names(this.#content)) if (/^[0-9a-f]{64}$/.test(name) && !named.has(name)) await remove(this.#content, name);
    for (const name of await names(this.#partial)) if (!named.has(name)) await remove(this.#partial, name);
    const stale: {key: string; value: null}[] = [];
    for (const [key] of await this.#host.journal.blobValues(this.#hub, 'opened:')) if (!named.has(key.slice('opened:'.length))) stale.push({key, value: null});
    for (const prefix of ['unreadable:', 'unavailable:']) for (const [key] of await this.#host.journal.blobValues(this.#hub, prefix)) if (!writes.has(key.slice(prefix.length))) stale.push({key, value: null});
    await this.#set(stale);
    const referenced = new Set([...this.#uploads.values()].map(u => u.staged));
    await navigator.locks.request(this.#lock('stage'), {ifAvailable: true}, async lock => {
      if (!lock) return;
      for (const name of await names(this.#staged)) if (!referenced.has(name)) await remove(this.#staged, name);
    });
  }
  /** Forgets a dropped upload once none of its chunks is in the index any more. */
  async #forgetDropped(): Promise<void> {
    const inIndex = new Set((await this.#host.journal.chunks(this.#hub)).map(r => r.address)), forget: {key: string; value: null}[] = [];
    for (const [key, addresses] of await this.#host.journal.blobValues(this.#hub, 'dropped:')) {
      let left = false;
      if (addresses instanceof Uint8Array) for (let i = 0; i + 32 <= addresses.length; i += 32) if (inIndex.has(b64u(addresses.subarray(i, i + 32)))) left = true;
      if (!left) forget.push({key, value: null});
    }
    await this.#set(forget);
  }
}

/** The entries whose addresses aren't on the hub, once each, with their offsets. */
function missing(m: Manifest, present: ReadonlyMap<string, string>): {entries: ChunkEntry[]; offsets: number[]} {
  const entries: ChunkEntry[] = [], offsets: number[] = [], seen = new Set<string>(); let offset = 0;
  for (const e of m.chunks ?? []) {
    const a = b64u(e.address);
    if (!present.has(a) && !seen.has(a)) { entries.push(e); offsets.push(offset); seen.add(a); }
    offset += e.length;
  }
  return {entries, offsets};
}

export {BlobFolderFull};
