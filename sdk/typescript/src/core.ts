import {type KeyObject} from 'node:crypto';
import {bytes, sha, b64u, unb64u, sign, verifyStrict} from './crypto.js';
import {parseJSON, validUnicode} from './json.js';

export const MAX_VECTOR = 1024, MAX_MILLIS = 2**48;
export type Op = 1 | 2 | 3 | 4;
export interface Dot {device: Uint8Array; millis: number; counter: number}
export interface Write {hub: Uint8Array; scope: Uint8Array; record: Uint8Array; field: string; op: Op;
  author: Uint8Array; certificate: Uint8Array; authorization: Uint8Array; dot: Dot; value: Uint8Array;
  element: Uint8Array | null; ref: Uint8Array; context: readonly Dot[]; base: readonly Dot[]; signature: Uint8Array}
const eq = (a: Uint8Array, b: Uint8Array): boolean => Buffer.from(a).equals(b);
const cmp = (a: Uint8Array, b: Uint8Array): number => Buffer.compare(a, b);
export function integer(n: number, limit: number): void { if (!Number.isInteger(n) || n < 0 || n >= limit) throw new Error('integer out of range'); }
function u16(n: number): Buffer { integer(n, 2**16); const b = Buffer.alloc(2); b.writeUInt16BE(n); return b; }
function u32(n: number): Buffer { integer(n, 2**32); const b = Buffer.alloc(4); b.writeUInt32BE(n); return b; }
function u64(n: number): Buffer { integer(n, MAX_MILLIS); const b = Buffer.alloc(8); b.writeBigUInt64BE(BigInt(n)); return b; }
function lp(v: Uint8Array): Buffer { return Buffer.concat([u32(v.length), v]); }
function checkDot(d: Dot): void { bytes(d.device, 8); integer(d.millis, MAX_MILLIS); integer(d.counter, 2**16); }
export function vectorBytes(vector: readonly Dot[]): Buffer {
  if (vector.length > MAX_VECTOR) throw new Error('vector exceeds 1024 entries');
  const out = [u16(vector.length)]; let previous: Uint8Array | null = null;
  for (const d of vector) { checkDot(d); if (previous && cmp(previous, d.device) >= 0) throw new Error('vector not sorted and unique');
    out.push(bytes(d.device), u64(d.millis), u16(d.counter)); previous = d.device; }
  return Buffer.concat(out);
}
export function sigInput(w: Write): Buffer {
  for (const v of [w.hub, w.author, w.certificate, w.authorization, w.ref]) bytes(v, 32);
  bytes(w.scope, 16); bytes(w.record, 16); bytes(w.value); validUnicode(w.field); checkDot(w.dot);
  if (![1, 2, 3, 4].includes(w.op) || ((w.op === 2 || w.op === 3) !== (w.element !== null)) ||
    (w.op !== 1 && w.value.length !== 0) || (w.op === 4 && w.field !== '')) throw new Error('invalid operation payload');
  return Buffer.concat([Buffer.from('TANDEM-WRITE-V2'), w.hub, w.scope, w.record, lp(Buffer.from(w.field)), Buffer.from([w.op]),
    lp(w.element ?? Buffer.alloc(0)), sha(w.value), w.author, w.certificate, w.authorization, w.dot.device,
    u64(w.dot.millis), u16(w.dot.counter), w.ref, vectorBytes(w.context), vectorBytes(w.base)]);
}
export const writeID = (w: Write): Buffer => sha(sigInput(w));
export const signWrite = (w: Write, key: KeyObject): Write => ({...clone(w), signature: sign(key, sigInput(w))});
export const verifyWrite = (w: Write, publicKey: Uint8Array): boolean => verifyStrict(publicKey, w.signature, sigInput(w));
function clone(w: Write): Write {
  const dot = (d: Dot): Dot => ({...d, device: bytes(d.device)});
  return {...w, hub: bytes(w.hub), scope: bytes(w.scope), record: bytes(w.record), author: bytes(w.author), certificate: bytes(w.certificate),
    authorization: bytes(w.authorization), dot: dot(w.dot), value: bytes(w.value), element: w.element === null ? null : bytes(w.element),
    ref: bytes(w.ref), context: w.context.map(dot), base: w.base.map(dot), signature: bytes(w.signature)};
}
const dotJSON = (d: Dot) => [b64u(d.device), d.millis, d.counter];
export function recordPayload(record: Uint8Array, writes: readonly Write[]): Buffer {
  bytes(record, 16);
  return Buffer.from(JSON.stringify({rec: b64u(record), writes: writes.map(w => {
    sigInput(w); if (!eq(w.record, record) || w.signature.length !== 64) throw new Error('record or signature mismatch');
    return {f: w.field, op: w.op, ...(w.op === 1 ? {v: b64u(w.value)} : {}), ...(w.element !== null ? {e: b64u(w.element)} : {}),
      a: b64u(w.author), cert: b64u(w.certificate), auth: b64u(w.authorization), d: dotJSON(w.dot),
      ...(!eq(w.ref, Buffer.alloc(32)) ? {r: b64u(w.ref)} : {}), c: w.context.map(dotJSON), b: w.base.map(dotJSON), s: b64u(w.signature)};
  })}));
}
export function parseKind1(data: Uint8Array, hub: Uint8Array, scope: Uint8Array): {record: Buffer; writes: Write[]} | {epoch: Buffer; part: number; parts: number; bytes: Buffer} {
  bytes(hub, 32); bytes(scope, 16);
  const obj = (v: unknown): Record<string, any> => { if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('expected object'); return v as Record<string, any>; };
  const j = obj(parseJSON(data)); if (Object.hasOwn(j, 'rec') === Object.hasOwn(j, 'inventory')) throw new Error('exactly one of rec and inventory');
  const raw = (v: unknown, n?: number): Buffer => { if (typeof v !== 'string') throw new Error('expected base64url string'); return bytes(unb64u(v), n); };
  if (Object.hasOwn(j, 'inventory')) { const f = obj(j.inventory); integer(f.part, 2**53); integer(f.parts, 2**53 + 2);
    if (!(f.part < f.parts)) throw new Error('bad fragment index'); return {epoch: raw(f.epoch, 32), part: f.part, parts: f.parts, bytes: raw(f.bytes)}; }
  const rec = raw(j.rec, 16); if (!Array.isArray(j.writes)) throw new Error('writes missing');
  const dot = (d: unknown): Dot => { if (!Array.isArray(d) || d.length !== 3) throw new Error('bad dot'); const out = {device: raw(d[0], 8), millis: d[1], counter: d[2]}; checkDot(out); return out; };
  const vector = (v: unknown): Dot[] => { if (!Array.isArray(v)) throw new Error('vector missing'); return v.map(dot); };
  return {record: rec, writes: j.writes.map((row: unknown) => {
    const r = obj(row);
    if (typeof r.f !== 'string' || Object.hasOwn(r, 'v') !== (r.op === 1) || Object.hasOwn(r, 'e') !== (r.op === 2 || r.op === 3)) throw new Error('invalid write payload');
    const w: Write = {hub: bytes(hub), scope: bytes(scope), record: rec, field: r.f, op: r.op, author: raw(r.a, 32), certificate: raw(r.cert, 32), authorization: raw(r.auth, 32),
      dot: dot(r.d), value: Object.hasOwn(r, 'v') ? raw(r.v) : Buffer.alloc(0), element: Object.hasOwn(r, 'e') ? raw(r.e) : null,
      ref: Object.hasOwn(r, 'r') ? raw(r.r, 32) : Buffer.alloc(32), context: vector(r.c), base: vector(r.b), signature: raw(r.s, 64)};
    sigInput(w); return w;
  })};
}
export function inventoryBytes(entries: ReadonlyMap<string, readonly Uint8Array[]>): Buffer {
  const out: Uint8Array[] = [u32(entries.size)];
  for (const [hex, ws] of [...entries].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)) {
    if (!/^[0-9a-f]{32}$/.test(hex)) throw new Error('invalid inventory record ID');
    const sorted = ws.map(w => bytes(w, 32)).sort(cmp); if (!sorted.length || sorted.some((w, i) => i > 0 && eq(w, sorted[i - 1]!))) throw new Error('empty or duplicate inventory entry');
    out.push(Buffer.from(hex, 'hex'), u32(sorted.length), ...sorted);
  } return Buffer.concat(out);
}
export function parseInventory(data: Uint8Array): Map<string, Buffer[]> {
  const b = Buffer.from(data); let i = 0; const take = (n: number): Buffer => { if (n < 0 || n > b.length - i) throw new Error('short inventory'); const out = b.subarray(i, i + n); i += n; return Buffer.from(out); };
  const count = () => take(4).readUInt32BE(); const n = count(), out = new Map<string, Buffer[]>(); let previous: Buffer | null = null;
  for (let r = 0; r < n; r++) { const rec = take(16); if (previous && cmp(previous, rec) >= 0) throw new Error('records not sorted and unique'); previous = rec;
    const size = count(); if (!size || size > Math.floor((b.length - i) / 32)) throw new Error('invalid inventory write count'); const ws: Buffer[] = [];
    for (let k = 0; k < size; k++) { const w = take(32); if (ws.length && cmp(ws[ws.length - 1]!, w) >= 0) throw new Error('writes not sorted and unique'); ws.push(w); } out.set(rec.toString('hex'), ws); }
  if (i !== b.length) throw new Error('trailing inventory bytes'); return out;
}
const clockCmp = (a: Dot, b: Dot): number => a.millis - b.millis || a.counter - b.counter;
export function join(...vectors: readonly (readonly Dot[])[]): Dot[] {
  const out = new Map<string, Dot>(); for (const v of vectors) for (const d of v) { const key = Buffer.from(d.device).toString('hex'), old = out.get(key); if (!old || clockCmp(old, d) < 0) out.set(key, {...d, device: bytes(d.device)}); }
  return [...out.values()].sort((a, b) => cmp(a.device, b.device));
}
const covers = (a: readonly Dot[], b: readonly Dot[]): boolean => b.every(d => a.some(e => eq(d.device, e.device) && clockCmp(e, d) >= 0));
const reach = (w: Write): Dot[] => join(w.context, [w.dot]);
const includes = (a: Write, b: Write): boolean => covers(a.context, reach(b)) && covers(a.base, b.base);
function sameSlot(a: Write, b: Write): boolean {
  if (a.op === 4 || b.op === 4) return a.op === b.op;
  const sa = a.op === 2 || a.op === 3, sb = b.op === 2 || b.op === 3;
  return sa === sb && a.field === b.field && (!sa || eq(a.element!, b.element!));
}
export class Entry {
  readonly #write: Write; readonly #id: Buffer; readonly #epoch: Buffer; readonly #epochNumber: bigint;
  constructor(w: Write, epoch: Uint8Array, epochNumber: bigint) {
    if (epochNumber < 0n || epochNumber >= (1n << 64n)) throw new Error('invalid epoch counter');
    this.#write = clone(w); this.#id = writeID(w); this.#epoch = bytes(epoch, 32); this.#epochNumber = epochNumber;
  }
  get write(): Write { return clone(this.#write); } get id(): Buffer { return bytes(this.#id); } get epoch(): Buffer { return bytes(this.#epoch); }
  get epochNumber(): bigint { return this.#epochNumber; }
}
export function supersedes(w: Entry, x: Entry): boolean {
  const a = w.write, b = x.write;
  if (eq(w.id, x.id) || !eq(a.hub, b.hub) || !eq(a.scope, b.scope) || !eq(a.record, b.record) || (b.op === 4 && a.op !== 4) || (a.op !== 4 && !sameSlot(a, b))) return false;
  if (w.epochNumber > x.epochNumber) return covers(a.base, reach(b)) && covers(a.base, b.base);
  return w.epochNumber === x.epochNumber && eq(w.epoch, x.epoch) && includes(a, b) && !includes(b, a);
}
function displayCmp(a: Entry, b: Entry): number {
  if (a.epochNumber !== b.epochNumber) return a.epochNumber < b.epochNumber ? -1 : 1;
  const x = a.write, y = b.write; return clockCmp(x.dot, y.dot) || cmp(x.author, y.author) || cmp(x.dot.device, y.dot.device) || cmp(a.id, b.id);
}
export class RecordState {
  readonly #hub: Buffer; readonly #scope: Buffer; readonly #id: Buffer; #kept: Entry[] = [];
  constructor(hub: Uint8Array, scope: Uint8Array, id: Uint8Array) { this.#hub = bytes(hub, 32); this.#scope = bytes(scope, 16); this.#id = bytes(id, 16); }
  get kept(): readonly Entry[] { return [...this.#kept]; }
  merge(...entries: Entry[]): void {
    for (const e of entries) { const w = e.write; if (!eq(w.hub, this.#hub) || !eq(w.scope, this.#scope) || !eq(w.record, this.#id)) throw new Error('entry belongs to another record, scope or hub'); }
    const all = new Map([...this.#kept, ...entries].map(e => [e.id.toString('hex'), e]));
    this.#kept = [...all.values()].filter(x => ![...all.values()].some(w => supersedes(w, x))).sort((a, b) => cmp(a.id, b.id));
  }
  get deleted(): boolean { return this.#kept.some(e => e.write.op === 4); }
  siblings(field: string): readonly Entry[] { return this.#kept.filter(e => e.write.op === 1 && e.write.field === field).sort((a, b) => displayCmp(b, a)); }
  value(field: string): Buffer | undefined { const e = this.siblings(field)[0]; return e ? bytes(e.write.value) : undefined; }
  elements(field: string): Buffer[] { return [...new Map(this.#kept.filter(e => e.write.op === 2 && e.write.field === field).map(e => { const v = bytes(e.write.element!); return [v.toString('hex'), v] as const; })).values()].sort(cmp); }
  vectors(op: Op, field: string, element: Uint8Array | null, epoch: Uint8Array, epochNumber: bigint): {context: Dot[]; base: Dot[]} {
    const probe = {op, field, element} as Write, c: Dot[][] = [], b: Dot[][] = [];
    for (const e of this.#kept) { const w = e.write; if (op !== 4 && !sameSlot(probe, w)) continue; b.push([...w.base]);
      (eq(e.epoch, epoch) && e.epochNumber === epochNumber ? c : b).push(reach(w)); }
    const context = join(...c), base = join(...b); vectorBytes(context); vectorBytes(base); return {context, base};
  }
}
export function nextClock(millis: number, counter: number, now: number): {millis: number; counter: number} {
  integer(millis, MAX_MILLIS); integer(counter, 2**16); integer(now, MAX_MILLIS);
  const result = now > millis ? {millis: now, counter: 0} : counter === 65535 ? {millis: millis + 1, counter: 0} : {millis, counter: counter + 1};
  integer(result.millis, MAX_MILLIS); return result;
}
