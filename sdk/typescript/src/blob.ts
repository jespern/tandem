import {Buffer} from "buffer";
import {bytes, sha, kdf, b64u, unb64u, encrypt, decrypt} from './crypto.js';
import {parseJSON} from './json.js';

// Blobs (DESIGN.md §17): chunk encryption, manifests and the escaped body form. No I/O.

export const KIND_CHUNK = 4;
export const MAX_CHUNK = 4 * 1024 * 1024;       // plaintext bytes in one chunk (DESIGN.md §17.4.1)
export const DEFAULT_CHUNK = 1024 * 1024;       // what a writer cuts files into
export const DEFAULT_INLINE = 16 * 1024;        // content up to this size goes in the manifest itself
const ENTRY = 68, LINE = 490, MAGIC = Buffer.from([84, 78, 68, 77, 2, KIND_CHUNK]);

export interface ChunkEntry {address: Buffer; key: Buffer; length: number}
/** A blob's description: its chunks, or its bytes inline in `data`. */
export interface Manifest {size: number; hash: Buffer; chunks?: ChunkEntry[]; data?: Buffer}
/** How a _blob: field's value reads (§17.5). 'unknown' is a JSON object whose blob is an integer other than 1: it stops clean-up (§17.7). */
export type ParsedManifest = {kind: 'manifest'; manifest: Manifest} | {kind: 'unknown'; version: number} | {kind: 'none'};

/** The writer's key for one chunk: never the same for two contents (§17.4.1). */
export const chunkKey = (r: Uint8Array, plaintext: Uint8Array): Buffer => kdf(bytes(r, 32), sha(plaintext), 'tandem/v2/blob-chunk');
const chunkAAD = (hub: Uint8Array): Buffer => Buffer.concat([MAGIC, bytes(hub, 32)]);

/** The chunk's envelope. Deterministic: the same key and plaintext give the same bytes. */
export function sealChunk(key: Uint8Array, hub: Uint8Array, plaintext: Uint8Array): Buffer {
  bytes(key, 32); if (plaintext.length < 1 || plaintext.length > MAX_CHUNK) throw new Error('invalid chunk');
  return Buffer.concat([MAGIC, encrypt(key, Buffer.alloc(12), chunkAAD(hub), plaintext)]);
}

/** The plaintext, which must be exactly `length` bytes (§17.5). */
export function openChunk(key: Uint8Array, hub: Uint8Array, envelope: Uint8Array, length: number): Buffer {
  bytes(key, 32);
  if (envelope.length < 6 + 16 + 1 || !Buffer.from(envelope.subarray(0, 6)).equals(MAGIC)) throw new Error('not a chunk envelope');
  let plaintext: Buffer;
  try { plaintext = Buffer.from(decrypt(key, Buffer.alloc(12), chunkAAD(hub), envelope.subarray(6))); }
  catch { throw new Error('chunk authentication failed'); }
  if (plaintext.length !== length) throw new Error('chunk length differs from its manifest entry');
  return plaintext;
}

export const chunkAddress = (envelope: Uint8Array): Buffer => sha(envelope);

/** The canonical writer form: no spaces, keys blob, size, hash, then chunks or data. */
export function encodeManifest(m: Manifest): Buffer {
  if ((m.chunks === undefined) === (m.data === undefined)) throw new Error('invalid manifest');
  const tail = m.chunks !== undefined
    ? {chunks: b64u(Buffer.concat(m.chunks.flatMap(e => { const n = Buffer.alloc(4); n.writeUInt32BE(e.length); return [e.address, e.key, n]; })))}
    : {data: b64u(m.data!)};
  const out = Buffer.from(JSON.stringify({blob: 1, size: m.size, hash: b64u(m.hash), ...tail}));
  if (parseManifest(out).kind !== 'manifest') throw new Error('invalid manifest');
  return out;
}

export function parseManifest(value: Uint8Array): ParsedManifest {
  let doc: unknown;
  try { doc = parseJSON(value); } catch { return {kind: 'none'}; }
  if (!doc || typeof doc !== 'object' || Array.isArray(doc)) return {kind: 'none'};
  const j = doc as Record<string, unknown>;
  if (typeof j.blob !== 'number' || !Number.isInteger(j.blob)) return {kind: 'none'};
  if (j.blob !== 1) return {kind: 'unknown', version: j.blob};
  const {size} = j;
  if (typeof size !== 'number' || !Number.isInteger(size) || size < 0 || size > 2 ** 53 || typeof j.hash !== 'string') return {kind: 'none'};
  if (Object.hasOwn(j, 'chunks') === Object.hasOwn(j, 'data')) return {kind: 'none'};
  const raw = Object.hasOwn(j, 'chunks') ? j.chunks : j.data;
  let hash: Buffer, content: Buffer;
  try { if (typeof raw !== 'string') return {kind: 'none'}; hash = unb64u(j.hash); content = unb64u(raw); } catch { return {kind: 'none'}; }
  if (hash.length !== 32) return {kind: 'none'};
  if (Object.hasOwn(j, 'data')) return content.length === size ? {kind: 'manifest', manifest: {size, hash, data: content}} : {kind: 'none'};
  if (content.length === 0 || content.length % ENTRY !== 0) return {kind: 'none'};
  const chunks: ChunkEntry[] = []; let total = 0;
  for (let i = 0; i < content.length; i += ENTRY) {
    const length = content.readUInt32BE(i + 64);
    if (length < 1 || length > MAX_CHUNK) return {kind: 'none'};
    total += length; chunks.push({address: Buffer.from(content.subarray(i, i + 32)), key: Buffer.from(content.subarray(i + 32, i + 64)), length});
  }
  return total === size ? {kind: 'manifest', manifest: {size, hash, chunks}} : {kind: 'none'};
}

/** The manifest of `content` and its envelopes in order (§17.6, step 2), all in memory. */
export function describe(r: Uint8Array, hub: Uint8Array, content: Uint8Array, chunkSize = DEFAULT_CHUNK, inline = DEFAULT_INLINE): {manifest: Manifest; envelopes: Buffer[]} {
  if (!Number.isInteger(chunkSize) || chunkSize < 1 || chunkSize > MAX_CHUNK) throw new Error('invalid chunk size');
  if (content.length <= inline) return {manifest: {size: content.length, hash: sha(content), data: Buffer.from(content)}, envelopes: []};
  const chunks: ChunkEntry[] = [], envelopes: Buffer[] = [];
  for (let i = 0; i < content.length; i += chunkSize) {
    const piece = content.subarray(i, i + chunkSize), key = chunkKey(r, piece), envelope = sealChunk(key, hub, piece);
    chunks.push({address: chunkAddress(envelope), key, length: piece.length}); envelopes.push(envelope);
  }
  return {manifest: {size: content.length, hash: sha(content), chunks}, envelopes};
}

const anywhere = (b: number) => b === 0x00 || b === 0x0A || b === 0x0D || b === 0x3D;
const atStart = (b: number) => anywhere(b) || b === 0x09 || b === 0x20 || b === 0x2E || b === 0x3E || b === 0x46;
const atEnd = (b: number) => anywhere(b) || b === 0x09 || b === 0x20;

/** The escaped body form of §17.4.2: lines of at most 490 envelope bytes, CRLF after each. */
export function escape(envelope: Uint8Array): Buffer {
  const out = Buffer.alloc(envelope.length * 2 + 2 * Math.ceil(envelope.length / LINE)); let n = 0;
  for (let i = 0; i < envelope.length; i += LINE) {
    const end = Math.min(i + LINE, envelope.length);
    for (let j = i; j < end; j++) {
      const b = envelope[j]!;
      if (j === i ? atStart(b) : j === end - 1 ? atEnd(b) : anywhere(b)) { out[n++] = 0x3D; out[n++] = (b + 64) & 255; } else out[n++] = b;
    }
    out[n++] = 0x0D; out[n++] = 0x0A;
  }
  return out.subarray(0, n);
}

/** Remove every CR and LF, then each '=' and the byte after it become that byte minus 64. */
export function unescape(body: Uint8Array): Buffer {
  const out = Buffer.alloc(body.length); let n = 0, pending = false;
  for (const b of body) {
    if (b === 0x0D || b === 0x0A) continue;
    if (pending) { out[n++] = (b - 64) & 255; pending = false; } else if (b === 0x3D) pending = true; else out[n++] = b;
  }
  if (pending) throw new Error("escaped body ends in '='");
  return out.subarray(0, n);
}
