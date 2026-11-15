// A household of browser devices on an in-memory hub, for the files tests (DESIGN.md §17). The feed is a memory
// PreparedStore shared by every device of the page; the blob folder is the SDK's MemoryBlobFolder.
import * as t from '/sdk.js';

/** A feed folder in memory: append, change pages by cursor, destroy and push wakes, like a JMAP account. */
export class MemoryHub {
  messages = new Map(); log = []; serial = 0; #version = 0; #wake = () => {}; #changed = new Promise(resolve => { this.#wake = resolve; });
  #signal() { this.#version++; const wake = this.#wake; this.#changed = new Promise(resolve => { this.#wake = resolve; }); wake(); }
  append(channel, body) { const id = String(++this.serial); this.messages.set(id, {id, channel, body: Uint8Array.from(body)}); this.log.push({kind: 'added', id}); this.#signal(); return id; }
  destroy(ids) { for (const id of ids) if (this.messages.delete(id)) this.log.push({kind: 'removed', id}); this.#signal(); }
  store() { return new MemoryStore(this); }
  get version() { return this.#version; }
  get changed() { return this.#changed; }
}
class MemoryStore {
  #hub; #closed = false;
  constructor(hub) { this.#hub = hub; }
  deleteBatchSize = 128;
  async watch(signal) {
    const hub = this.#hub; let seen = hub.version;
    return {[Symbol.asyncIterator]: () => ({next: async () => {
      while (hub.version === seen && !signal.aborted && !this.#closed) await Promise.race([hub.changed, new Promise(resolve => signal.addEventListener('abort', resolve, {once: true}))]);
      seen = hub.version; return signal.aborted || this.#closed ? {done: true, value: undefined} : {done: false, value: undefined};
    }})};
  }
  async changes(cursor) {
    const start = cursor ? Number(cursor) : 0, added = [], removed = [];
    for (const e of this.#hub.log.slice(start)) { if (e.kind === 'added') { const m = this.#hub.messages.get(e.id); if (m) added.push({...m, body: Uint8Array.from(m.body)}); } else removed.push(e.id); }
    return {added, removed, cursor: String(this.#hub.log.length)};
  }
  async append(channel, body) { return this.#hub.append(channel, body); }
  async appendPrepared(wire) {
    const text = new TextDecoder().decode(wire), [head, ...rest] = text.split('\r\n\r\n'), channel = /^Tandem-Channel: (.+)$/m.exec(head)[1].trim();
    return this.#hub.append(channel, t.decodeBody(rest.join('\r\n\r\n')));
  }
  async destroy(ids) { this.#hub.destroy(ids); }
  async close() { this.#closed = true; }
}

/** A blob folder of one device over a shared one: counts what it fetched, and can be cut off as a network can. */
export class DeviceFolder {
  fetched = []; offline = false; onFetch = undefined;
  constructor(inner) { this.inner = inner; this.compare = inner.compare; }
  #check() { if (this.offline) throw new TypeError('Failed to fetch'); }
  async changes(cursor, signal) { this.#check(); return this.inner.changes(cursor, signal); }
  async append(chunks, form, signal) { this.#check(); return this.inner.append(chunks, form, signal); }
  async fetch(ids, signal) { this.#check(); this.fetched.push(...ids); const got = await this.inner.fetch(ids, signal); await this.onFetch?.(ids); return got; }
  async destroy(ids, signal) { this.#check(); return this.inner.destroy(ids, signal); }
  async quota(signal) { return this.inner.quota(signal); }
}

const password = 'A blob test household password';
let counter = 0;

/** One device, running on the hub. */
export async function start(name, hub, journalName, keysName, device) {
  const journal = await t.BrowserJournal.open(journalName), keys = await t.BrowserKeychain.open(keysName);
  const d = device ? await device(journal, keys) : undefined;
  const abort = new AbortController(), run = d.run(async () => hub.store(), abort.signal); void run.catch(() => {});
  const handle = {name, device: d, journal, keys, abort, run, hubID: t.b64u(d.hub), journalName, keysName,
    /** Waits for an update that satisfies `predicate`. */
    async until(predicate) { if (await predicate(d)) return; for await (const update of d.updates(abort.signal)) if (await predicate(d, update)) return; throw new Error(`${name}: stopped waiting`); },
    /** Waits until everything this device queued is on the hub. */
    async sent() { await handle.until(async () => !(await journal.allPending(handle.hubID)).length && !d.operations.length); },
    async stop() { abort.abort(); await run.catch(() => {}); await d.close(); journal.close(); keys.close(); },
  };
  return handle;
}

/** A founder and a paired second device of the same participant, with a list both write, and files on. */
export async function household(settings = {}) {
  const id = `${Date.now()}-${++counter}`, hub = new MemoryHub(), folder = new t.MemoryBlobFolder();
  const mailbox = {host: 'jmap.example.org', port: 443, user: `blobs-${id}@example.org`};
  const a = await start('A', hub, `blobs-a-${id}`, `blobs-a-keys-${id}`, (journal, keys) => t.BrowserDevice.found(mailbox, 'Laptop', password, journal, keys));
  await a.until(d => d.update.status === 'ready'); await a.sent();
  const link = await a.device.startPairing('Phone'); await a.sent();
  const b = await start('B', hub, `blobs-b-${id}`, `blobs-b-keys-${id}`, (journal, keys) => t.BrowserDevice.pair(link, 'Phone', journal, keys));
  await b.until(d => d.update.status === 'approval'); await b.sent();
  await a.until(d => d.state.claims().length > 0);
  const seed = await a.device.unlock(password);
  try { await a.device.approve(a.device.state.claims()[0].id, seed); } finally { seed.fill(0); }
  await a.sent(); await b.until(d => d.update.status === 'ready');
  const list = await a.device.createList('Files'); await a.sent();
  await b.until(d => d.state.collectionStatus(list).ready && d.state.lists().some(l => t.b64u(l.id) === t.b64u(list)));
  a.folder = new DeviceFolder(folder); b.folder = new DeviceFolder(folder);
  await a.device.useBlobs(a.folder, settings); await b.device.useBlobs(b.folder, settings);
  return {hub, folder, a, b, list, id, async stop() { await a.stop(); await b.stop(); }};
}

export const random = n => { const out = new Uint8Array(n); for (let at = 0; at < n; at += 65536) crypto.getRandomValues(out.subarray(at, Math.min(n, at + 65536))); return out; };
export const same = (x, y) => x.length === y.length && x.every((v, i) => v === y[i]);
export const bytesOf = async blob => new Uint8Array(await blob.arrayBuffer());
/** The manifest in a record's field, as the device's verified view holds it. */
export function manifest(device, list, record, name) { const v = device.state.record(list, record)?.value(`_blob:${name}`); const p = v && t.parseManifest(v); return p?.kind === 'manifest' ? p.manifest : undefined; }
/** How many messages of the folder carry each chunk of a manifest. */
export const counts = (folder, m) => m.chunks.map(e => folder.withAddress(e.address).length);
/** The hashes of the content files a device holds. */
export async function contents(h) {
  const root = await navigator.storage.getDirectory(), dir = await (await (await (await root.getDirectoryHandle('tandem-blobs')).getDirectoryHandle(encodeURIComponent(h.journal.name))).getDirectoryHandle(h.hubID)).getDirectoryHandle('content');
  const out = []; for await (const name of dir.keys()) out.push(name); return out.sort();
}
export {t};
