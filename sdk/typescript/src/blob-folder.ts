import {Buffer} from 'buffer';
import {b32, unb32, randomBytes} from './crypto.js';
import {chunkAddress, escape, unescape} from './blob.js';

// The blob folder as an engine sees it (DESIGN.md §17.4.3), beside the feed's PushStore: list the chunks' claimed
// addresses from their headers alone, append chunks in the carrier forms of §17.4.2, fetch and decode them, destroy
// them and read the quota. Nothing here idles: a device asks.

/** How a chunk's body is carried (§17.4.2): escaped 8-bit content, or base64 for a server seen to damage it. */
export type ChunkForm = 'escaped' | 'base64';
/** A message in the blob folder as its headers claim it: the claims are unverified. */
export interface BlobRef {id: string; channel: string; address: Buffer}
/** One page of what changed in the blob folder; `more` says to call again at once with the new cursor. */
export interface BlobChanges {added: BlobRef[]; removed: string[]; cursor: string; more?: boolean}
/** A fetched chunk message, decoded but not verified against its address. */
export interface ChunkMessage {id: string; envelope: Buffer}
/** The folder's storage quota (RFC 9208, RFC 9425): unknown where the server reports none, never zero. */
export type BlobQuota = {kind: 'unknown'} | {kind: 'known'; used: number; limit: number};
/** A refusal the app can show: the hub is full, or a chunk is over the server's message limit (§17.6). */
export class BlobFolderFull extends Error { constructor(readonly reason: 'over-quota' | 'too-large') { super(reason === 'over-quota' ? 'the hub is full' : 'a chunk is over the server\'s message limit'); } }

export interface BlobFolder {
  /** Names the folder (account, mailbox, and its UIDVALIDITY where IMAP has one); a device whose index was built
   * from another rebuilds it (§17.4.3). */
  readonly identity?: string;
  /** Present only where message IDs have one order for every device of the hub, in which a later append is never
   * lower (IMAP UIDs). Clean-up of copies (§17.7) needs it; without it a device leaves copies alone. */
  readonly compare?: (a: string, b: string) => number;
  /** One page of changes since `cursor` ("" for the start), from headers alone. Messages without both claims are left out. */
  changes(cursor: string, signal: AbortSignal): Promise<BlobChanges>;
  /** Stores chunks; IDs in order, "" where the server named none. Not retried: an append whose answer was lost may have landed. */
  append(chunks: readonly {channel: string; envelope: Uint8Array}[], form: ChunkForm, signal: AbortSignal): Promise<string[]>;
  /** Chunks by ID, in the order of `ids`, leaving out what is gone or doesn't decode. */
  fetch(ids: readonly string[], signal: AbortSignal): Promise<ChunkMessage[]>;
  /** Removes chunk messages for good. */
  destroy(ids: readonly string[], signal: AbortSignal): Promise<void>;
  quota(signal: AbortSignal): Promise<BlobQuota>;
  close?(): Promise<void>;
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'], MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
/** `Fri, 13 Nov 2026 18:04:05 +0000`, the day without a leading zero (§10.4). */
export function rfc5322Date(date: Date): string {
  const two = (n: number) => String(n).padStart(2, '0');
  return `${DAYS[date.getUTCDay()]}, ${date.getUTCDate()} ${MONTHS[date.getUTCMonth()]} ${date.getUTCFullYear()} ${two(date.getUTCHours())}:${two(date.getUTCMinutes())}:${two(date.getUTCSeconds())} +0000`;
}
/** A message ID of 32 random lowercase hex digits (§10.4). */
export const randomMessageID = (): string => randomBytes(16).toString('hex');

/** The chunk message of §17.4.2: the headers of §10.4 with Tandem-Blob (and Tandem-Body for the escaped form),
 * application/octet-stream, CRLF line ends. */
export function chunkMessage(channel: string, envelope: Uint8Array, form: ChunkForm = 'escaped', date = new Date(), id = randomMessageID()): Buffer {
  if (!channel || !/^[\x21-\x7e]{1,900}$/.test(channel)) throw new Error('channel tag must be printable ASCII');
  if (!envelope.length) throw new Error('empty chunk');
  if (!/^[0-9a-f]{32}$/.test(id)) throw new Error('invalid message ID');
  const head = ['From: Tandem <tandem@tandem.airforce>', `Date: ${rfc5322Date(date)}`, `Message-ID: <${id}@tandem.airforce>`, 'Subject: tandem',
    'MIME-Version: 1.0', 'Tandem-Version: 2', `Tandem-Channel: ${channel}`, `Tandem-Blob: ${b32(chunkAddress(envelope))}`,
    ...(form === 'escaped' ? ['Tandem-Body: escaped', 'Content-Type: application/octet-stream', 'Content-Transfer-Encoding: 8bit']
      : ['Content-Type: application/octet-stream', 'Content-Transfer-Encoding: base64'])];
  const body = form === 'escaped' ? escape(envelope)
    : Buffer.from((Buffer.from(envelope).toString('base64').match(/.{1,76}/g) ?? []).map(line => line + '\r\n').join(''), 'ascii');
  return Buffer.concat([Buffer.from(head.map(line => line + '\r\n').join('') + '\r\n', 'ascii'), body]);
}

/** A header field's value, unfolded and trimmed; names compared without case. */
export function headerField(header: Uint8Array, name: string): string | undefined {
  const lines: string[] = [];
  for (const raw of Buffer.from(header).toString('latin1').split('\n')) {
    const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw;
    if ((line.startsWith(' ') || line.startsWith('\t')) && lines.length) lines[lines.length - 1] += line; else lines.push(line);
  }
  const want = name.toLowerCase();
  for (const line of lines) {
    const colon = line.indexOf(':'); if (colon < 0) continue;
    if (line.slice(0, colon).replace(/[ \t]/g, '').toLowerCase() === want) return line.slice(colon + 1).replace(/^[ \t\r\n]+|[ \t\r\n]+$/g, '');
  }
  return undefined;
}

function quotedPrintable(body: Uint8Array): Buffer {
  const out = Buffer.alloc(body.length); let n = 0;
  const hex = (b: number | undefined) => b === undefined ? -1 : b >= 0x30 && b <= 0x39 ? b - 0x30 : b >= 0x41 && b <= 0x46 ? b - 55 : b >= 0x61 && b <= 0x66 ? b - 87 : -1;
  for (let i = 0; i < body.length; i++) {
    const b = body[i]!;
    if (b === 0x3d) {
      if (body[i + 1] === 0x0d && body[i + 2] === 0x0a) { i += 2; continue; }
      if (body[i + 1] === 0x0a) { i += 1; continue; }
      const h = hex(body[i + 1]), l = hex(body[i + 2]);
      if (h >= 0 && l >= 0) { out[n++] = h * 16 + l; i += 2; continue; }
    }
    out[n++] = b;
  }
  return out.subarray(0, n);
}

/** Undoes the transfer encoding a header names (§10.4 step 1). */
export function undoTransferEncoding(name: string, body: Uint8Array): Buffer {
  switch (name.trim().toLowerCase()) {
    case '': case '7bit': case '8bit': case 'binary': return Buffer.from(body);
    case 'quoted-printable': return quotedPrintable(body);
    case 'base64': {
      const compact = Buffer.from(body).toString('latin1').replace(/[\t\n\v\f\r ]/g, '');
      if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(compact)) throw new Error('bad base64 transfer encoding');
      return Buffer.from(compact, 'base64');
    }
    default: throw new Error('unknown transfer encoding');
  }
}

/** A chunk's envelope from its header fields and body (§17.4.2, Reading): the transfer encoding the header names is
 * undone once, then the escaping if Tandem-Body says so. Only application/octet-stream is a chunk. The caller checks
 * the envelope against its address. */
export function readChunkParts(header: Uint8Array, body: Uint8Array): Buffer {
  const media = (headerField(header, 'Content-Type') ?? 'text/plain').split(';')[0]!.trim().toLowerCase();
  if (media !== 'application/octet-stream') throw new Error('a chunk is application/octet-stream');
  let content = undoTransferEncoding(headerField(header, 'Content-Transfer-Encoding') ?? '7bit', body);
  if ((headerField(header, 'Tandem-Body') ?? '').toLowerCase() === 'escaped') content = unescape(content);
  if (!content.length) throw new Error('empty chunk');
  return content;
}

/** The same from a whole message, as a server returns it. */
export function readChunkMessage(message: Uint8Array): Buffer {
  const m = Buffer.from(message);
  let end = -1, skip = 0;
  for (let i = 0; i < m.length; i++) {
    if (m[i] !== 0x0a) continue;
    if (m[i + 1] === 0x0a) { end = i + 1; skip = 1; break; }
    if (m[i + 1] === 0x0d && m[i + 2] === 0x0a) { end = i + 1; skip = 2; break; }
  }
  if (end < 0) throw new Error('a chunk message has a header and a body');
  return readChunkParts(m.subarray(0, end), m.subarray(end + skip));
}

/** A claimed address from a Tandem-Blob header: 52 base32 characters for 32 bytes. */
export function parseBlobClaim(value: string | undefined): Buffer | undefined {
  if (!value) return undefined;
  try { const address = unb32(value.trim()); return address.length === 32 ? address : undefined; } catch { return undefined; }
}

/** A blob folder in memory, shared by every device of a test (§17.4.3), as Swift's MemoryBlobHub. Like a credential
 * holder, a test can drop chunks, append junk under any claim, make the escaped form come back damaged, lose answers,
 * fail appends as a full hub does, or set a quota. IDs are ordered, as IMAP UIDs are. */
export class MemoryBlobFolder implements BlobFolder {
  readonly #events: ({kind: 'added'; ref: BlobRef} | {kind: 'removed'; id: string})[] = [];
  readonly #live = new Map<string, {ref: BlobRef; wire: Buffer}>();
  #serial = 0;
  appends = 0; fetches = 0;
  /** Every chunk appended in the escaped form comes back altered. */
  damageEscaped = false;
  /** What quota() returns. */
  quotaReport: BlobQuota = {kind: 'unknown'};
  /** Appends fail as an over-quota server's do. */
  failAppends = false;
  /** If set, only that many chunks of each append are stored, then it fails. */
  partialAppends: number | undefined;
  /** This many appends store their chunks and then lose the answer. */
  loseAnswers = 0;
  /** Destroy fails, as a lost connection does. */
  failDestroys = false;
  /** Fetches fail, as a lost connection does. */
  failFetches = false;
  readonly compare = (a: string, b: string): number => Number(a) - Number(b);
  readonly identity = `memory:${randomBytes(16).toString('hex')}`;

  async changes(cursor: string): Promise<BlobChanges> {
    const start = cursor === '' ? 0 : Number(cursor);
    if (!Number.isSafeInteger(start) || start < 0 || start > this.#events.length) throw new Error('memory blob folder: bad cursor');
    const added: BlobRef[] = [], removed: string[] = [];
    for (const e of this.#events.slice(start)) {
      if (e.kind === 'added') { if (this.#live.has(e.ref.id)) added.push({...e.ref, address: Buffer.from(e.ref.address)}); } else removed.push(e.id);
    }
    return {added, removed, cursor: String(this.#events.length)};
  }
  #add(channel: string, address: Uint8Array, wire: Buffer): string {
    const ref = {id: String(++this.#serial), channel, address: Buffer.from(address)};
    this.#live.set(ref.id, {ref, wire}); this.#events.push({kind: 'added', ref}); return ref.id;
  }
  async append(chunks: readonly {channel: string; envelope: Uint8Array}[], form: ChunkForm): Promise<string[]> {
    if (this.failAppends) throw new BlobFolderFull('over-quota');
    this.appends++; const ids: string[] = [];
    for (const [i, c] of chunks.entries()) {
      if (this.partialAppends !== undefined && i >= this.partialAppends) throw new Error('memory blob folder: connection lost');
      const envelope = Buffer.from(c.envelope);
      if (form === 'escaped' && this.damageEscaped) envelope[envelope.length - 1] = envelope[envelope.length - 1]! ^ 0x20;
      ids.push(this.#add(c.channel, chunkAddress(c.envelope), chunkMessage(c.channel, envelope, form)));
    }
    if (this.loseAnswers > 0) { this.loseAnswers--; throw new Error('memory blob folder: answer lost'); }
    return ids;
  }
  async fetch(ids: readonly string[]): Promise<ChunkMessage[]> {
    if (this.failFetches) throw new Error('memory blob folder: connection lost');
    this.fetches++;
    return ids.flatMap(id => { const m = this.#live.get(id); if (!m) return []; try { return [{id, envelope: readChunkMessage(m.wire)}]; } catch { return []; } });
  }
  async destroy(ids: readonly string[]): Promise<void> { if (this.failDestroys) throw new Error('memory blob folder: connection lost'); this.drop(ids); }
  async quota(): Promise<BlobQuota> { return this.quotaReport; }
  /** Deletes chunk messages as a credential holder could. */
  drop(ids: readonly string[]): void { for (const id of ids) if (this.#live.delete(id)) this.#events.push({kind: 'removed', id}); }
  /** Appends a message claiming any address with any envelope, as anyone with the credential can. */
  inject(channel: string, address: Uint8Array, envelope: Uint8Array): string {
    const wire = chunkMessage(channel, envelope, 'escaped');
    const claimed = Buffer.from(wire.toString('latin1').replace(/^Tandem-Blob: [a-z2-7]+\r$/m, `Tandem-Blob: ${b32(address)}\r`), 'latin1');
    return this.#add(channel, address, claimed);
  }
  /** Every chunk message in the folder, oldest first. */
  get refs(): BlobRef[] { return [...this.#live.values()].map(m => ({...m.ref, address: Buffer.from(m.ref.address)})).sort((a, b) => this.compare(a.id, b.id)); }
  /** The IDs of the messages claiming an address, oldest first. */
  withAddress(address: Uint8Array): string[] { return this.refs.filter(r => r.address.equals(address)).map(r => r.id); }
  /** The stored message, as a server would return it. */
  message(id: string): Buffer | undefined { const m = this.#live.get(id); return m && Buffer.from(m.wire); }
}
