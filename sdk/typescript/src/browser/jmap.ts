import {parseJSON} from '../json.js';
import type {PushStore, ChangeBatch, Message} from '../session.js';
import {buildMessage, checkChannel, decodeBody} from './message.js';
import {Invalidation} from './events.js';
import {reconnectDelay} from './reconnect.js';

const CORE = 'urn:ietf:params:jmap:core', MAIL = 'urn:ietf:params:jmap:mail';
type ObjectValue = Record<string, any>;
interface Cursor {v: 1; account: string; folder: string; state: string; known: string[]; pending?: string[]}
export const FASTMAIL_SESSION='https://api.fastmail.com/jmap/session';
export function fastmailConfig(token:string,options:Omit<JMAPConfig,'sessionUrl'|'authorization'|'provider'>={}):JMAPConfig{
  if(!token||/\s/.test(token))throw new Error('a bare Fastmail JMAP API token is required');
  return{...options,provider:'fastmail',sessionUrl:FASTMAIL_SESSION,authorization:`Bearer ${token}`};
}
export interface JMAPConfig {
  /** Fastmail explicitly trusts HTTPS api.fastmail.com and its regional API subdomains. */
  provider?:'fastmail';
  /** A root mailbox dedicated to this replica; defaults to Tandem. */
  folderName?:string;
  sessionUrl: string; authorization: string; accountId?: string;
  /** Additional provider origins explicitly trusted with this credential. */
  trustedOrigins?: readonly string[];
  pageSize?: number; maxResponseBytes?: number; maxKnownMessages?: number; maxEventBytes?: number;
  commandTimeout?: number; retryMin?: number; retryMax?: number;
  onStatus?: (status: {connected: boolean; error?: unknown}) => void;
}
export class JMAPError extends Error { constructor(readonly type: string) { super(`JMAP: ${type}`); } }
export class NetworkFailure extends Error { constructor(cause: unknown) { super('JMAP connection failed; retain queued bytes', {cause}); } }
export class SnapshotChanged extends Error { constructor() { super('JMAP query changed during full scan; await its push before restarting'); } }
const object = (v: unknown): ObjectValue => { if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('invalid JMAP object'); return v; };
const strings = (v: unknown): string[] => { if (!Array.isArray(v) || !v.every(x => typeof x === 'string')) throw new Error('invalid JMAP ID list'); return v; };
const state = (v: unknown): string => { if (typeof v !== 'string' || !v) throw new Error('invalid JMAP state'); return v; };

/** HTTPS JMAP + one authenticated streaming-fetch SSE subscription. Never polls. */
export class JMAPStore implements PushStore {
  readonly #cfg: Required<Pick<JMAPConfig, 'pageSize' | 'maxResponseBytes' | 'maxKnownMessages' | 'maxEventBytes' | 'commandTimeout' | 'retryMin' | 'retryMax'>> & JMAPConfig;
  readonly #origins: Set<string>; readonly #abort = new AbortController(); readonly #wakes = new Invalidation();
  #deleteSize=128;
  #api = ''; #upload = ''; #events = ''; #account = ''; #folder = ''; #username=''; #watching = false; #running: Promise<void> | undefined;
  #streamAbort: AbortController | undefined;
  private constructor(cfg: JMAPConfig) {
    this.#cfg = {...cfg, pageSize: cfg.pageSize ?? 128, maxResponseBytes: cfg.maxResponseBytes ?? 16 * 1024 * 1024,
      maxKnownMessages: cfg.maxKnownMessages ?? 1_000_000, maxEventBytes: cfg.maxEventBytes ?? 131072, commandTimeout: cfg.commandTimeout ?? 30_000, retryMin: cfg.retryMin ?? 1000, retryMax: cfg.retryMax ?? 30_000};
    this.#origins = new Set([new URL(cfg.sessionUrl).origin, ...(cfg.trustedOrigins ?? []).map(x => new URL(x).origin)]);
    for (const n of [this.#cfg.pageSize, this.#cfg.maxResponseBytes, this.#cfg.maxKnownMessages, this.#cfg.maxEventBytes, this.#cfg.commandTimeout, this.#cfg.retryMin, this.#cfg.retryMax]) if (!Number.isSafeInteger(n) || n < 1) throw new Error('invalid JMAP limit');
    if (this.#cfg.retryMax < this.#cfg.retryMin || !/^(?:Basic|Bearer) [^\r\n]+$/.test(cfg.authorization)) throw new Error('invalid JMAP configuration');
    if(cfg.provider!==undefined&&(cfg.provider!=='fastmail'||cfg.sessionUrl!==FASTMAIL_SESSION||!cfg.authorization.startsWith('Bearer ')))throw new Error('invalid Fastmail configuration');
    if(cfg.folderName!==undefined&&(!cfg.folderName||/[\r\n\0]/.test(cfg.folderName)))throw new Error('invalid JMAP mailbox name');
    this.#url(cfg.sessionUrl);
  }
  static async open(cfg: JMAPConfig, signal: AbortSignal = new AbortController().signal): Promise<JMAPStore> {
    const store = new JMAPStore(cfg);
    try { await store.#connect(signal); return store; } catch (error) { await store.close(); throw error; }
  }
  /** Credential-free identity of the selected JMAP mailbox for a hub's genesis. */
  get mailboxDescriptor():import('../control.js').Mailbox {
    if(!this.#username)throw new JMAPError('session has no username');
    const url=new URL(this.#api);return{host:url.hostname.toLowerCase(),port:Number(url.port||443),user:this.#username};
  }
  /** One explicit setup check; never called by the change-feed loop. */
  async hasMessages(signal:AbortSignal=new AbortController().signal):Promise<boolean>{
    const result=await this.#call('Email/query',{filter:{inMailbox:this.#folder},limit:1},signal);return strings(result.ids).length>0;
  }
  #url(value: string): string {
    const url = new URL(value);
    const fastmail=this.#cfg.provider==='fastmail'&&!url.port&&(url.hostname==='api.fastmail.com'||url.hostname.endsWith('.api.fastmail.com'));
    if (url.protocol !== 'https:' || url.username || url.password || url.hash || !(this.#origins.has(url.origin)||fastmail)) throw new Error('untrusted JMAP URL');
    return url.href;
  }
  async #fetch(url: string, init: RequestInit, signal: AbortSignal, stream = false, discovery = false): Promise<Response> {
    let response: Response;
    try { response = await fetch(this.#url(url), {...init, redirect: discovery ? 'follow' : 'error', credentials: 'omit',
      headers: {...init.headers, Authorization: this.#cfg.authorization},
      signal: AbortSignal.any([signal, this.#abort.signal, ...(stream ? [] : [AbortSignal.timeout(this.#cfg.commandTimeout)])])}); }
    catch (error) {
      signal.throwIfAborted(); this.#abort.signal.throwIfAborted();
      const failure = new NetworkFailure(error); if (!stream) this.#streamAbort?.abort(failure); throw failure;
    }
    if (discovery) { try { this.#url(response.url); } catch (error) { await response.body?.cancel(); throw error; } }
    if (!response.ok) { await response.body?.cancel(); throw new JMAPError(`HTTP ${response.status}`); }
    return response;
  }
  async #json(response: Response): Promise<ObjectValue> {
    const reader = response.body?.getReader(); if (!reader) throw new Error('missing JMAP response');
    const parts: Uint8Array[] = []; let count = 0;
    try { while (true) {
      let next: ReadableStreamReadResult<Uint8Array>;
      try { next = await reader.read(); } catch (error) {
        this.#abort.signal.throwIfAborted(); const failure = new NetworkFailure(error); this.#streamAbort?.abort(failure); throw failure;
      }
      if (next.done) break; count += next.value.length;
      if (count > this.#cfg.maxResponseBytes) throw new Error('JMAP response exceeds configured limit'); parts.push(next.value); }
    } finally { await reader.cancel().catch(()=>{}); reader.releaseLock(); }
    const body = new Uint8Array(count); let offset = 0; for (const part of parts) { body.set(part, offset); offset += part.length; }
    return object(JSON.parse(new TextDecoder('utf-8', {fatal: true}).decode(body)));
  }
  async #connect(signal: AbortSignal): Promise<void> {
    const session = await this.#json(await this.#fetch(this.#cfg.sessionUrl, {}, signal, false, true));
    this.#username=typeof session.username==='string'?session.username:'';
    if (!object(session.capabilities)[MAIL]) throw new JMAPError('mail capability missing');
    this.#account = this.#cfg.accountId ?? state(object(session.primaryAccounts)[MAIL]);
    if (!object(object(session.accounts)[this.#account]).accountCapabilities?.[MAIL]) throw new JMAPError('account cannot store mail');
    this.#api = this.#url(state(session.apiUrl)); this.#upload = state(session.uploadUrl); this.#events = state(session.eventSourceUrl);
    const capability=object(session.capabilities[CORE]),maxGet=capability.maxObjectsInGet;
    if (typeof maxGet === 'number' && maxGet > 0) this.#cfg.pageSize = Math.min(this.#cfg.pageSize, maxGet);
    this.#deleteSize=this.#cfg.pageSize;
    if(Number.isSafeInteger(capability.maxObjectsInSet)&&capability.maxObjectsInSet>0)this.#deleteSize=Math.min(this.#deleteSize,capability.maxObjectsInSet);
    const folderName=this.#cfg.folderName??'Tandem';
    const queried = await this.#call('Mailbox/query', {filter: {name: folderName}}, signal);
    const boxes = strings(queried.ids).length ? (await this.#call('Mailbox/get', {ids: queried.ids}, signal)).list : [];
    const found = (boxes as ObjectValue[]).filter(x => x.name === folderName && x.parentId === null);
    if (found.length > 1) throw new JMAPError('ambiguous Tandem folder');
    if (found.length) this.#folder = state(found[0]!.id);
    else { const created = await this.#call('Mailbox/set', {create: {tandem: {name: folderName, parentId: null}}}, signal); this.#folder = state(object(object(created.created).tandem).id); }
  }
  async #call(method: string, args: ObjectValue, signal: AbortSignal): Promise<ObjectValue> {
    const response = await this.#json(await this.#fetch(this.#api, {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({using: [CORE, MAIL], methodCalls: [[method, {accountId: this.#account, ...args}, '0']]})}, signal));
    const rows = response.methodResponses;
    if (!Array.isArray(rows) || rows.length !== 1 || rows[0]?.[2] !== '0') throw new Error('invalid JMAP method response');
    const [name, body] = rows[0]; if (name === 'error') throw new JMAPError(state(object(body).type));
    if (name !== method) throw new Error('JMAP response method mismatch');
    const result = object(body);
    for (const key of ['notCreated', 'notUpdated', 'notDestroyed']) if (result[key] && Object.keys(result[key]).length) throw new JMAPError(`${method} refused`);
    return result;
  }
  async watch(signal: AbortSignal): Promise<AsyncIterable<void>> {
    if (this.#watching) throw new Error('watch already started'); this.#watching = true;
    const lifetime = AbortSignal.any([signal, this.#abort.signal]);
    const response = await this.#subscribe(lifetime); // active before the initial scan
    this.#cfg.onStatus?.({connected: true});
    this.#running = this.#pump(response, lifetime);
    void this.#running.catch(error => this.#wakes.end(error));
    return this.#wakes;
  }
  #subscribe(signal: AbortSignal): Promise<Response> {
    const url = this.#events.replace('{types}', 'Email').replace('{closeafter}', 'no').replace('{ping}', '60');
    if (/[{}]/.test(url)) throw new Error('unsupported event-source URL template');
    this.#streamAbort = new AbortController();
    return this.#fetch(url, {headers: {Accept: 'text/event-stream'}}, AbortSignal.any([signal, this.#streamAbort.signal]), true).then(async response => {
      if (!response.headers.get('content-type')?.toLowerCase().startsWith('text/event-stream')) { await response.body?.cancel(); throw new JMAPError('not an event stream'); } return response;
    });
  }
  async #pump(initial: Response, signal: AbortSignal): Promise<void> {
    let response = initial, delay = this.#cfg.retryMin;
    try { while (!signal.aborted) {
      try { await this.#readStream(response, signal); throw new NetworkFailure('JMAP push connection ended'); }
      catch (error) {
        if (signal.aborted) break;
        if (!(error instanceof NetworkFailure)) throw error; // malformed/protocol/credential errors are terminal
        this.#cfg.onStatus?.({connected: false, error});
        while (!signal.aborted) {
          await reconnectDelay(delay, signal);
          try { response = await this.#subscribe(signal); delay = this.#cfg.retryMin; this.#cfg.onStatus?.({connected: true}); this.#wakes.signal(); break; }
          catch (error) { if (!(error instanceof NetworkFailure)) throw error; delay = Math.min(delay * 2, this.#cfg.retryMax); this.#cfg.onStatus?.({connected: false, error}); }
        }
      }
    }} catch (error) { if (!signal.aborted) { this.#cfg.onStatus?.({connected: false, error}); this.#wakes.end(error); } }
    finally { this.#wakes.end(); }
  }
  async #readStream(response: Response, signal: AbortSignal): Promise<void> {
    const reader = response.body!.getReader(), decoder = new TextDecoder('utf-8', {fatal: true});
    let pending = '', event = '', data: string[] = [], eventBytes = 0;
    try { while (!signal.aborted) {
      let next: ReadableStreamReadResult<Uint8Array>;
      try { next = await reader.read(); } catch (error) { signal.throwIfAborted(); throw new NetworkFailure(error); }
      if (next.done) return;
      pending += decoder.decode(next.value, {stream: true});
      if (new TextEncoder().encode(pending).length > this.#cfg.maxEventBytes) throw new Error('push line exceeds configured stream limit');
      while (true) {
        const end = pending.search(/[\r\n]/); if (end < 0) break;
        if (pending[end] === '\r' && end === pending.length - 1) break;
        const line = pending.slice(0, end); pending = pending.slice(end + (pending[end] === '\r' && pending[end + 1] === '\n' ? 2 : 1));
        eventBytes += new TextEncoder().encode(line).length + 1; if (eventBytes > this.#cfg.maxEventBytes) throw new Error('push event exceeds stream limit');
        if (!line) {
          if (event === 'state' && data.length) { const payload = object(JSON.parse(data.join('\n')));
            const changed = object(payload.changed ?? {}); if (changed[this.#account]?.Email !== undefined) this.#wakes.signal(); }
          data = []; event = ''; eventBytes = 0;
        } else if (line.startsWith('event:')) event = line.slice(6).replace(/^ /, '');
        else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
      }
    }} finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
  }
  #cursor(encoded: string): Cursor | undefined {
    if (!encoded) return undefined;
    const c = object(parseJSON(new TextEncoder().encode(encoded)));
    if (c.v !== 1 || c.account !== this.#account || c.folder !== this.#folder) throw new Error('cursor belongs to another JMAP store');
    state(c.state); strings(c.known); if (new Set(c.known).size !== c.known.length || c.known.length > this.#cfg.maxKnownMessages) throw new Error('invalid cursor IDs');
    const known = new Set(c.known);
    if (c.pending !== undefined && (!strings(c.pending).every(id => known.has(id)) || new Set(c.pending).size !== c.pending.length)) throw new Error('invalid cursor continuation');
    return c as Cursor;
  }
  async #objects(ids: string[], signal: AbortSignal, withBodies = true): Promise<{rows: ObjectValue[]; missing: string[]}> {
    if (!ids.length) return {rows: [], missing: []};
    const result = await this.#call('Email/get', {ids, properties: ['id', 'mailboxIds', ...(withBodies ? ['bodyValues', 'textBody', 'header:Tandem-Channel:asText:all', 'header:Tandem-Version:asText:all'] : [])],
      fetchTextBodyValues: true, maxBodyValueBytes: this.#cfg.maxResponseBytes}, signal);
    if (!Array.isArray(result.list)) throw new Error('invalid Email/get');
    return {rows: result.list.map(object), missing: strings(result.notFound ?? [])};
  }
  #message(row: ObjectValue): Message | undefined {
    const versions = row['header:Tandem-Version:asText:all'], channels = row['header:Tandem-Channel:asText:all'];
    if (!object(row.mailboxIds)[this.#folder] || !Array.isArray(versions) || versions.length !== 1 || versions[0]?.trim() !== '2' || !Array.isArray(channels) || channels.length !== 1) return;
    const channel = channels[0]?.trim(); if (typeof channel !== 'string') return;
    const values = Object.values(object(row.bodyValues)) as ObjectValue[];
    if (values.some(x => x.isTruncated)) throw new Error('JMAP body truncated; cursor not advanced');
    if (values.length !== 1 || values[0]!.isEncodingProblem || typeof values[0]!.value !== 'string') return;
    try { checkChannel(channel); return {id: state(row.id), channel, body: decodeBody(values[0]!.value)}; } catch { return undefined; }
  }
  async #full(old: Cursor | undefined, signal: AbortSignal): Promise<{cursor: Cursor; removed: string[]}> {
    const base = state((await this.#call('Email/get', {ids: [], properties: ['id']}, signal)).state);
    const ids: string[] = []; let queryState: string | undefined;
    while (true) {
      const result = await this.#call('Email/query', {filter: {inMailbox: this.#folder}, sort: [{property: 'receivedAt', isAscending: true}], position: ids.length, limit: this.#cfg.pageSize, calculateTotal: true}, signal);
      const current = state(result.queryState); if (queryState !== undefined && current !== queryState) throw new SnapshotChanged(); queryState = current;
      const page = strings(result.ids); ids.push(...page);
      if (ids.length > this.#cfg.maxKnownMessages || new Set(ids).size !== ids.length) throw new Error('full scan exceeds limit or repeats IDs');
      if (typeof result.total !== 'number' || !Number.isSafeInteger(result.total) || result.total < ids.length) throw new Error('invalid query total');
      if (ids.length === result.total) break; if (!page.length) throw new Error('query made no progress');
    }
    const present = new Set(ids);
    return {cursor: {v: 1, account: this.#account, folder: this.#folder, state: base, known: ids, pending: [...ids]}, removed: old?.known.filter(id => !present.has(id)) ?? []};
  }
  async changes(encoded: string, signal: AbortSignal): Promise<ChangeBatch> {
    let cursor = this.#cursor(encoded), removed: string[] = [];
    if (!cursor) { const full = await this.#full(undefined, signal); cursor = full.cursor; removed = full.removed; }
    if (cursor.pending !== undefined) {
      const page = cursor.pending.slice(0, this.#cfg.pageSize), fetched = await this.#objects(page, signal);
      const remaining = cursor.pending.slice(page.length), next = {...cursor};
      if (remaining.length) next.pending = remaining; else delete next.pending;
      return {added: fetched.rows.flatMap(row => { const m = this.#message(row); return m ? [m] : []; }), removed, cursor: JSON.stringify(next), more: true};
    }
    let changes: ObjectValue;
    try { changes = await this.#call('Email/changes', {sinceState: cursor.state, maxChanges: this.#cfg.pageSize}, signal); }
    catch (error) { if (!(error instanceof JMAPError) || error.type !== 'cannotCalculateChanges') throw error;
      const full = await this.#full(cursor, signal); const result = await this.changes(JSON.stringify(full.cursor), signal); return {...result, removed: [...full.removed, ...result.removed]}; }
    const known = new Set(cursor.known), destroyed = strings(changes.destroyed), changed = [...new Set([...strings(changes.created), ...strings(changes.updated)])];
    const rows: ObjectValue[] = [], missing: string[] = [];
    for (let at = 0; at < changed.length; at += this.#cfg.pageSize) { const batch = await this.#objects(changed.slice(at, at + this.#cfg.pageSize), signal, false); rows.push(...batch.rows); missing.push(...batch.missing); }
    const inFolder = rows.filter(row => object(row.mailboxIds)[this.#folder]).map(row => state(row.id)), bodies = new Map<string, ObjectValue>();
    for (let at = 0; at < inFolder.length; at += this.#cfg.pageSize) { for (const row of (await this.#objects(inFolder.slice(at, at + this.#cfg.pageSize), signal)).rows) bodies.set(state(row.id), row); }
    const added: Message[] = [];
    for (const id of [...destroyed, ...missing]) if (known.delete(id)) removed.push(id);
    for (const row of rows) {
      const id = state(row.id);
      if (destroyed.includes(id)) continue;
      if (object(row.mailboxIds)[this.#folder]) { known.add(id); const body = bodies.get(id); if (body) { const m = this.#message(body); if (m) added.push(m); } }
      else if (known.delete(id)) removed.push(id);
    }
    if (known.size > this.#cfg.maxKnownMessages) throw new Error('message limit exceeded');
    const next: Cursor = {v: 1, account: this.#account, folder: this.#folder, state: state(changes.newState), known: [...known].sort()};
    if (changes.hasMoreChanges && next.state === cursor.state) throw new Error('change page made no progress');
    return {added, removed: [...new Set(removed)], cursor: JSON.stringify(next), more: changes.hasMoreChanges === true};
  }
  async append(channel: string, body: Uint8Array, signal: AbortSignal): Promise<string> { return this.appendPrepared(buildMessage(channel, body), signal); }
  async appendPrepared(wire: Uint8Array, signal: AbortSignal): Promise<string> {
    if (wire.length > this.#cfg.maxResponseBytes) throw new Error('message exceeds configured limit');
    const uploadUrl = this.#upload.replace('{accountId}', encodeURIComponent(this.#account));
    const uploaded = await this.#json(await this.#fetch(uploadUrl, {method: 'POST', headers: {'Content-Type': 'message/rfc822'}, body: Uint8Array.from(wire)}, signal));
    const imported = await this.#call('Email/import', {emails: {tandem: {blobId: state(uploaded.blobId), mailboxIds: {[this.#folder]: true}, keywords: {$seen: true}}}}, signal);
    return state(object(object(imported.created).tandem).id);
  }
  async fetch(ids: readonly string[], signal: AbortSignal): Promise<Message[]> {
    if (ids.length > this.#cfg.pageSize) throw new Error('fetch exceeds page limit');
    return (await this.#objects([...ids], signal)).rows.flatMap(row => { const m = this.#message(row); return m ? [m] : []; });
  }
  get deleteBatchSize():number{return this.#deleteSize;}
  async destroy(ids: readonly string[], signal: AbortSignal): Promise<void> {
    if (ids.length > this.#deleteSize) throw new Error('destroy exceeds page limit'); if (!ids.length) return;
    const checked = await this.#objects([...ids], signal, false);
    if (checked.rows.some(row => !object(row.mailboxIds)[this.#folder])) throw new Error('refusing to destroy mail outside Tandem');
    if(!checked.rows.length)return;const result=await this.#call('Email/set',{destroy:checked.rows.map(row=>row.id)},signal);if(result.notDestroyed&&Object.keys(object(result.notDestroyed)).length)throw new JMAPError('Email/set refused destruction');
  }
  async close(): Promise<void> { this.#abort.abort(); this.#wakes.end(); await this.#running?.catch(() => {}); }
}
