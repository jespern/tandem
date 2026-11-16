import {parseJSON} from '../json.js';
import {type BlobFolder, type BlobChanges, type BlobRef, type ChunkForm, type ChunkMessage, type BlobQuota, BlobFolderFull, chunkMessage, readChunkMessage, parseBlobClaim} from '../blob-folder.js';
import {checkChannel} from './message.js';
import {type JMAPConfig, JMAPError, NetworkFailure, FASTMAIL_SESSION} from './jmap.js';

// The blob folder over JMAP (DESIGN.md §17.4.3 on RFC 8620/8621): a second root mailbox beside the feed's, read only
// when the engine has work. Changes come from Email/changes or a paged Email/query and carry the two header claims
// alone; chunks go up as uploaded MIME stored with one Email/import per batch, and come back as the raw message from
// the download URL, decoded by the reading rule of §17.4.2. No push subscription, no polling.

const CORE = 'urn:ietf:params:jmap:core', MAIL = 'urn:ietf:params:jmap:mail', QUOTA = 'urn:ietf:params:jmap:quota';
type ObjectValue = Record<string, any>;
interface Cursor {v: 1; account: string; folder: string; state: string; known: string[]; pending?: string[]}
const object = (v: unknown): ObjectValue => { if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('invalid JMAP object'); return v as ObjectValue; };
const strings = (v: unknown): string[] => { if (!Array.isArray(v) || !v.every(x => typeof x === 'string')) throw new Error('invalid JMAP ID list'); return v; };
const state = (v: unknown): string => { if (typeof v !== 'string' || !v) throw new Error('invalid JMAP state'); return v; };
const REF_PROPERTIES = ['id', 'mailboxIds', 'header:Tandem-Channel:asText:all', 'header:Tandem-Blob:asText:all'];

export interface JMAPBlobOptions {
  /** The blob folder: a root mailbox, by default the feed folder's name followed by `-Blobs` (`Tandem-Blobs`). */
  folderName?: string;
  /** How many uploads or downloads run at once, 1 to 8; 4 by default. */
  transfers?: number;
  /** The largest chunk message fetched, in bytes; 8 MiB by default (a 4 MiB chunk is 5.7 MB in base64). */
  maxChunkMessage?: number;
}

/** Runs `job` over `items` with at most `limit` at once; results in order. */
async function spread<T, R>(items: readonly T[], limit: number, job: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length); let next = 0;
  await Promise.all(Array.from({length: Math.min(limit, items.length)}, async () => { while (next < items.length) { const i = next++; out[i] = await job(items[i]!); } }));
  return out;
}

/** The blob folder of the JMAP account a `JMAPConfig` describes. Credentials go only to trusted origins, as for the feed. */
export class JMAPBlobFolder implements BlobFolder {
  readonly #cfg: JMAPConfig & {pageSize: number; maxResponseBytes: number; maxKnownMessages: number; commandTimeout: number};
  readonly #origins: Set<string>; readonly #abort = new AbortController();
  readonly #name: string; readonly #transfers: number; readonly #maxChunk: number;
  #api = ''; #upload = ''; #download = ''; #account = ''; #folder = ''; #maxUpload = Infinity; #setSize = 128; #quota = false;
  private constructor(cfg: JMAPConfig, options: JMAPBlobOptions) {
    this.#cfg = {...cfg, pageSize: cfg.pageSize ?? 128, maxResponseBytes: cfg.maxResponseBytes ?? 16 * 1024 * 1024, maxKnownMessages: cfg.maxKnownMessages ?? 1_000_000, commandTimeout: cfg.commandTimeout ?? 30_000};
    for (const n of [this.#cfg.pageSize, this.#cfg.maxResponseBytes, this.#cfg.maxKnownMessages, this.#cfg.commandTimeout]) if (!Number.isSafeInteger(n) || n < 1) throw new Error('invalid JMAP limit');
    if (!/^(?:Basic|Bearer) [^\r\n]+$/.test(cfg.authorization)) throw new Error('invalid JMAP configuration');
    if (cfg.provider !== undefined && (cfg.provider !== 'fastmail' || cfg.sessionUrl !== FASTMAIL_SESSION || !cfg.authorization.startsWith('Bearer '))) throw new Error('invalid Fastmail configuration');
    this.#name = options.folderName ?? `${cfg.folderName ?? 'Tandem'}-Blobs`;
    if (!this.#name || /[\r\n\0]/.test(this.#name) || this.#name === (cfg.folderName ?? 'Tandem')) throw new Error('invalid JMAP blob folder name');
    this.#transfers = options.transfers ?? 4; this.#maxChunk = options.maxChunkMessage ?? 8 * 1024 * 1024;
    if (!Number.isSafeInteger(this.#transfers) || this.#transfers < 1 || this.#transfers > 8 || !Number.isSafeInteger(this.#maxChunk) || this.#maxChunk < 1) throw new Error('invalid blob folder options');
    this.#origins = new Set([new URL(cfg.sessionUrl).origin, ...(cfg.trustedOrigins ?? []).map(x => new URL(x).origin)]);
    this.#url(cfg.sessionUrl);
  }
  /** Connects, and creates the folder if it is missing. */
  static async open(cfg: JMAPConfig, options: JMAPBlobOptions = {}, signal: AbortSignal = new AbortController().signal): Promise<JMAPBlobFolder> {
    const folder = new JMAPBlobFolder(cfg, options);
    try { await folder.#connect(signal); return folder; } catch (error) { await folder.close(); throw error; }
  }
  get folderName(): string { return this.#name; }
  get identity(): string { return `jmap:${JSON.stringify([new URL(this.#api).origin, this.#account, this.#folder])}`; }
  #url(value: string): string {
    const url = new URL(value);
    // Fastmail serves downloads from its content host (www.fastmailusercontent.com), not the API's.
    const fastmail = this.#cfg.provider === 'fastmail' && !url.port && (url.hostname === 'api.fastmail.com' || url.hostname.endsWith('.api.fastmail.com') || url.hostname.endsWith('.fastmailusercontent.com'));
    if (url.protocol !== 'https:' || url.username || url.password || url.hash || !(this.#origins.has(url.origin) || fastmail)) throw new Error('untrusted JMAP URL');
    return url.href;
  }
  async #fetch(url: string, init: RequestInit, signal: AbortSignal, discovery = false): Promise<Response> {
    // An untrusted URL is refused as such, not taken for a network failure that a retry could fix.
    const trusted = this.#url(url); let response: Response;
    try { response = await fetch(trusted, {...init, redirect: discovery ? 'follow' : 'error', credentials: 'omit', headers: {...init.headers, Authorization: this.#cfg.authorization},
      signal: AbortSignal.any([signal, this.#abort.signal, AbortSignal.timeout(this.#cfg.commandTimeout)])}); }
    catch (error) { signal.throwIfAborted(); this.#abort.signal.throwIfAborted(); throw new NetworkFailure(error); }
    if (discovery) { try { this.#url(response.url); } catch (error) { await response.body?.cancel(); throw error; } }
    if (!response.ok) { await response.body?.cancel(); if (response.status === 413) throw new BlobFolderFull('too-large'); throw new JMAPError(`HTTP ${response.status}`); }
    return response;
  }
  async #bytes(response: Response, limit: number): Promise<Uint8Array> {
    const reader = response.body?.getReader(); if (!reader) throw new Error('missing JMAP response');
    const parts: Uint8Array[] = []; let count = 0;
    try { while (true) {
      let next: ReadableStreamReadResult<Uint8Array>;
      try { next = await reader.read(); } catch (error) { this.#abort.signal.throwIfAborted(); throw new NetworkFailure(error); }
      if (next.done) break; count += next.value.length; if (count > limit) throw new Error('JMAP response exceeds configured limit'); parts.push(next.value);
    } } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
    const out = new Uint8Array(count); let at = 0; for (const part of parts) { out.set(part, at); at += part.length; } return out;
  }
  async #json(response: Response): Promise<ObjectValue> { return object(JSON.parse(new TextDecoder('utf-8', {fatal: true}).decode(await this.#bytes(response, this.#cfg.maxResponseBytes)))); }
  async #call(method: string, args: ObjectValue, signal: AbortSignal, using = [CORE, MAIL]): Promise<ObjectValue> {
    const response = await this.#json(await this.#fetch(this.#api, {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({using, methodCalls: [[method, {accountId: this.#account, ...args}, '0']]})}, signal));
    const rows = response.methodResponses;
    if (!Array.isArray(rows) || rows.length !== 1 || rows[0]?.[2] !== '0') throw new Error('invalid JMAP method response');
    const [name, body] = rows[0]; if (name === 'error') throw new JMAPError(state(object(body).type));
    if (name !== method) throw new Error('JMAP response method mismatch');
    return object(body);
  }
  async #connect(signal: AbortSignal): Promise<void> {
    const session = await this.#json(await this.#fetch(this.#cfg.sessionUrl, {}, signal, true));
    const capabilities = object(session.capabilities);
    if (!capabilities[MAIL]) throw new JMAPError('mail capability missing');
    this.#account = this.#cfg.accountId ?? state(object(session.primaryAccounts)[MAIL]);
    const account = object(object(session.accounts)[this.#account]);
    if (!account.accountCapabilities?.[MAIL]) throw new JMAPError('account cannot store mail');
    this.#quota = !!capabilities[QUOTA] && !!account.accountCapabilities?.[QUOTA];
    this.#api = this.#url(state(session.apiUrl)); this.#upload = state(session.uploadUrl); this.#download = state(session.downloadUrl);
    const core = object(capabilities[CORE]);
    if (Number.isSafeInteger(core.maxObjectsInGet) && core.maxObjectsInGet > 0) this.#cfg.pageSize = Math.min(this.#cfg.pageSize, core.maxObjectsInGet);
    this.#setSize = this.#cfg.pageSize;
    if (Number.isSafeInteger(core.maxObjectsInSet) && core.maxObjectsInSet > 0) this.#setSize = Math.min(this.#setSize, core.maxObjectsInSet);
    if (Number.isSafeInteger(core.maxSizeUpload) && core.maxSizeUpload > 0) this.#maxUpload = core.maxSizeUpload;
    const queried = await this.#call('Mailbox/query', {filter: {name: this.#name}}, signal);
    const boxes = strings(queried.ids).length ? (await this.#call('Mailbox/get', {ids: queried.ids}, signal)).list : [];
    const found = (boxes as ObjectValue[]).filter(x => x.name === this.#name && x.parentId === null);
    if (found.length > 1) throw new JMAPError('ambiguous blob folder');
    if (found.length) { this.#folder = state(found[0]!.id); return; }
    const created = await this.#call('Mailbox/set', {create: {blobs: {name: this.#name, parentId: null}}}, signal);
    if (created.notCreated && Object.keys(created.notCreated).length) throw new JMAPError('Mailbox/set refused');
    this.#folder = state(object(object(created.created).blobs).id);
  }
  #cursor(encoded: string): Cursor | undefined {
    if (!encoded) return undefined;
    const c = object(parseJSON(new TextEncoder().encode(encoded)));
    if (c.v !== 1 || c.account !== this.#account || c.folder !== this.#folder) throw new Error('cursor belongs to another JMAP blob folder');
    state(c.state); strings(c.known); if (new Set(c.known).size !== c.known.length || c.known.length > this.#cfg.maxKnownMessages) throw new Error('invalid cursor IDs');
    const known = new Set(c.known);
    if (c.pending !== undefined && (!strings(c.pending).every(id => known.has(id)) || new Set(c.pending).size !== c.pending.length)) throw new Error('invalid cursor continuation');
    return c as Cursor;
  }
  async #rows(ids: readonly string[], properties: string[], signal: AbortSignal): Promise<{rows: ObjectValue[]; missing: string[]}> {
    const rows: ObjectValue[] = [], missing: string[] = [];
    for (let at = 0; at < ids.length; at += this.#cfg.pageSize) {
      const result = await this.#call('Email/get', {ids: ids.slice(at, at + this.#cfg.pageSize), properties}, signal);
      if (!Array.isArray(result.list)) throw new Error('invalid Email/get');
      rows.push(...result.list.map(object)); missing.push(...strings(result.notFound ?? []));
    }
    return {rows, missing};
  }
  #inFolder(row: ObjectValue): boolean { return !!object(row.mailboxIds ?? {})[this.#folder]; }
  #ref(row: ObjectValue): BlobRef | undefined {
    const channels = row['header:Tandem-Channel:asText:all'], claims = row['header:Tandem-Blob:asText:all'];
    if (!Array.isArray(channels) || channels.length !== 1 || !Array.isArray(claims) || claims.length !== 1) return undefined;
    const channel = typeof channels[0] === 'string' ? channels[0].trim() : '', address = parseBlobClaim(typeof claims[0] === 'string' ? claims[0] : undefined);
    try { checkChannel(channel); } catch { return undefined; }
    return address && {id: state(row.id), channel, address};
  }
  async #full(old: Cursor | undefined, signal: AbortSignal): Promise<{cursor: Cursor; removed: string[]}> {
    const base = state((await this.#call('Email/get', {ids: [], properties: ['id']}, signal)).state);
    const ids: string[] = []; let queryState: string | undefined;
    while (true) {
      const result = await this.#call('Email/query', {filter: {inMailbox: this.#folder}, sort: [{property: 'receivedAt', isAscending: true}], position: ids.length, limit: this.#cfg.pageSize, calculateTotal: true}, signal);
      const current = state(result.queryState); if (queryState !== undefined && current !== queryState) throw new JMAPError('blob folder changed during a full scan; refresh again');
      queryState = current; const page = strings(result.ids); ids.push(...page);
      if (ids.length > this.#cfg.maxKnownMessages || new Set(ids).size !== ids.length) throw new Error('full scan exceeds limit or repeats IDs');
      if (typeof result.total !== 'number' || !Number.isSafeInteger(result.total) || result.total < ids.length) throw new Error('invalid query total');
      if (ids.length === result.total) break; if (!page.length) throw new Error('query made no progress');
    }
    const present = new Set(ids);
    return {cursor: {v: 1, account: this.#account, folder: this.#folder, state: base, known: ids, pending: [...ids]}, removed: old?.known.filter(id => !present.has(id)) ?? []};
  }
  async changes(encoded: string, signal: AbortSignal): Promise<BlobChanges> {
    let cursor = this.#cursor(encoded), removed: string[] = [];
    if (!cursor) { const full = await this.#full(undefined, signal); cursor = full.cursor; removed = full.removed; }
    if (cursor.pending !== undefined) {
      const page = cursor.pending.slice(0, this.#cfg.pageSize), {rows} = await this.#rows(page, REF_PROPERTIES, signal), next = {...cursor}, rest = cursor.pending.slice(page.length);
      if (rest.length) next.pending = rest; else delete next.pending;
      return {added: rows.filter(row => this.#inFolder(row)).flatMap(row => { const r = this.#ref(row); return r ? [r] : []; }), removed, cursor: JSON.stringify(next), more: true};
    }
    let changes: ObjectValue;
    try { changes = await this.#call('Email/changes', {sinceState: cursor.state, maxChanges: this.#cfg.pageSize}, signal); }
    catch (error) {
      if (!(error instanceof JMAPError) || error.type !== 'cannotCalculateChanges') throw error;
      const full = await this.#full(cursor, signal); const result = await this.changes(JSON.stringify(full.cursor), signal);
      return {...result, removed: [...full.removed, ...result.removed]};
    }
    const known = new Set(cursor.known), destroyed = strings(changes.destroyed), touched = [...new Set([...strings(changes.created), ...strings(changes.updated)])].filter(id => !destroyed.includes(id));
    const {rows, missing} = await this.#rows(touched, REF_PROPERTIES, signal), added: BlobRef[] = [];
    for (const id of [...destroyed, ...missing]) if (known.delete(id)) removed.push(id);
    for (const row of rows) {
      const id = state(row.id);
      if (this.#inFolder(row)) { if (!known.has(id)) { known.add(id); const r = this.#ref(row); if (r) added.push(r); } }
      else if (known.delete(id)) removed.push(id);
    }
    if (known.size > this.#cfg.maxKnownMessages) throw new Error('message limit exceeded');
    const next: Cursor = {v: 1, account: this.#account, folder: this.#folder, state: state(changes.newState), known: [...known].sort()};
    if (changes.hasMoreChanges && next.state === cursor.state) throw new Error('change page made no progress');
    return {added, removed: [...new Set(removed)], cursor: JSON.stringify(next), more: changes.hasMoreChanges === true};
  }
  async append(chunks: readonly {channel: string; envelope: Uint8Array}[], form: ChunkForm, signal: AbortSignal): Promise<string[]> {
    const wires = chunks.map(c => chunkMessage(c.channel, c.envelope, form));
    if (wires.some(wire => wire.length > this.#maxUpload || wire.length > this.#maxChunk)) throw new BlobFolderFull('too-large');
    const url = this.#upload.replace('{accountId}', encodeURIComponent(this.#account));
    const blobs = await spread(wires, this.#transfers, async wire => state((await this.#json(await this.#fetch(url, {method: 'POST', headers: {'Content-Type': 'message/rfc822'}, body: Uint8Array.from(wire)}, signal))).blobId));
    const ids: string[] = [];
    for (let at = 0; at < blobs.length; at += this.#setSize) {
      const batch = blobs.slice(at, at + this.#setSize), emails: ObjectValue = {};
      batch.forEach((blobId, i) => { emails[`c${i}`] = {blobId, mailboxIds: {[this.#folder]: true}, keywords: {$seen: true}}; });
      const imported = await this.#call('Email/import', {emails}, signal), refused = Object.values(object(imported.notCreated ?? {})) as ObjectValue[];
      if (refused.some(e => e?.type === 'overQuota')) throw new BlobFolderFull('over-quota');
      if (refused.some(e => e?.type === 'tooLarge')) throw new BlobFolderFull('too-large');
      if (refused.length) throw new JMAPError('Email/import refused');
      const created = object(imported.created ?? {});
      ids.push(...batch.map((_, i) => typeof created[`c${i}`]?.id === 'string' ? created[`c${i}`].id as string : ''));
    }
    return ids;
  }
  async fetch(ids: readonly string[], signal: AbortSignal): Promise<ChunkMessage[]> {
    const {rows} = await this.#rows([...new Set(ids)], ['id', 'blobId', 'mailboxIds', 'size'], signal);
    const wanted = rows.filter(row => this.#inFolder(row) && typeof row.blobId === 'string' && (typeof row.size !== 'number' || row.size <= this.#maxChunk));
    const got = await spread(wanted, this.#transfers, async row => {
      const url = this.#download.replace('{accountId}', encodeURIComponent(this.#account)).replace('{blobId}', encodeURIComponent(row.blobId))
        .replace('{type}', encodeURIComponent('application/octet-stream')).replace('{name}', 'chunk.eml');
      let raw: Uint8Array;
      try { raw = await this.#bytes(await this.#fetch(url, {}, signal), this.#maxChunk); } catch (error) { if (error instanceof JMAPError && error.type === 'HTTP 404') return undefined; throw error; }
      try { return {id: state(row.id), envelope: readChunkMessage(raw)}; } catch { return undefined; }
    });
    const byID = new Map(got.flatMap(c => c ? [[c.id, c] as const] : []));
    return ids.flatMap(id => { const c = byID.get(id); return c ? [c] : []; });
  }
  async destroy(ids: readonly string[], signal: AbortSignal): Promise<void> {
    const {rows} = await this.#rows([...new Set(ids)], ['id', 'mailboxIds'], signal);
    if (rows.some(row => !this.#inFolder(row))) throw new Error('refusing to destroy mail outside the blob folder');
    for (let at = 0; at < rows.length; at += this.#setSize) {
      const result = await this.#call('Email/set', {destroy: rows.slice(at, at + this.#setSize).map(row => state(row.id))}, signal);
      const refused = Object.entries(object(result.notDestroyed ?? {})).filter(([, e]) => (e as ObjectValue)?.type !== 'notFound');
      if (refused.length) throw new JMAPError('Email/set refused destruction');
    }
  }
  async quota(signal: AbortSignal): Promise<BlobQuota> {
    if (!this.#quota) return {kind: 'unknown'};
    const result = await this.#call('Quota/get', {ids: null}, signal, [CORE, QUOTA]);
    const rows = Array.isArray(result.list) ? result.list.map(object) : [];
    const octets = rows.find(q => q.resourceType === 'octets' && (q.scope === 'account' || (Array.isArray(q.types) && q.types.includes('Email'))));
    return octets && Number.isSafeInteger(octets.used) && Number.isSafeInteger(octets.hardLimit) && octets.hardLimit > 0 ? {kind: 'known', used: octets.used, limit: octets.hardLimit} : {kind: 'unknown'};
  }
  async close(): Promise<void> { this.#abort.abort(); }
}
