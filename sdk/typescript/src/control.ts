import {randomBytes, type KeyObject} from 'node:crypto';
import {bytes, sha, kdf, b32, b64u, unb64u, sign, verifyStrict, privateKey, encrypt, decrypt, sealHPKE, openHPKE} from './crypto.js';
import {parseJSON, validUnicode, type JSONValue} from './json.js';

// Control objects (§10.8): one schema, one encoder, one strict JSON reader. Every object is a
// tag followed by typed fields; its ID hashes that input and its signatures cover the same bytes.
// vectors/v2/control.json carries a copy of the table that every SDK checks its own against.
export const MAX_TEXT = 256, MAX_SET = 65536, MAX_MAILBOXES = 64, MAX_JSON_INTEGER = 2**53;
export type FieldType = 'id' | 'id16' | 'opt' | 'text' | 'set' | 'u8' | 'u16' | 'u32' | 'u64' | 'answer' | 'mailboxes';
type Layout = readonly (readonly [string, FieldType])[];
interface Kind {tag: string; fields: Layout; signatures: readonly string[]; extras: readonly (readonly [string, 'bytes' | 'request'])[]}
const k = (tag: string, fields: Layout, signatures: string[], extras: Kind['extras'] = []): Kind => ({tag, fields, signatures, extras});
// A signature name ending in "?" may be absent.
const SCHEMA: Readonly<Record<string, Kind>> = {
  'genesis': k('TANDEM-GENESIS-V2', [['hub', 'id'], ['agree', 'id'], ['hubkey', 'id'], ['creator', 'id'], ['mailboxes', 'mailboxes']], ['sig']),
  'capsule': k('TANDEM-CAPSULE-V2', [['hub', 'id'], ['genesis', 'id'], ['sealed', 'id'], ['supersedes', 'set']], ['sig'], [['capsule', 'bytes']]),
  'snapshot': k('tandem/v2/auth-snapshot', [['hub', 'id'], ['genesis', 'id'], ['participant', 'id'], ['certs', 'set'], ['revocations', 'set']], []),
  'registration': k('TANDEM-REGISTRATION-V2', [['hub', 'id'], ['sign', 'id'], ['agree', 'id'], ['enrollment', 'id16']], []),
  'request': k('TANDEM-REQUEST-V2', [['hub', 'id'], ['genesis', 'id'], ['participant', 'id'], ['sign', 'id'], ['agree', 'id'], ['label', 'text'],
    ['how', 'u8'], ['authority', 'opt'], ['enrollment', 'id16']], ['self', 'approver?']),
  'certificate': k('TANDEM-CERT-V2', [['hub', 'id'], ['genesis', 'id'], ['request', 'id'], ['issued', 'set']], ['sig'], [['body', 'request']]),
  'revocation': k('TANDEM-REVOKE-V2', [['hub', 'id'], ['genesis', 'id'], ['target', 'id'], ['retained', 'set']], ['sig']),
  'invitation': k('TANDEM-INVITE-V2', [['hub', 'id'], ['genesis', 'id'], ['key', 'id'], ['invite', 'id16'], ['expires', 'u64'], ['label', 'text']], ['sig']),
  'delegation': k('TANDEM-DELEGATION-V2', [['hub', 'id'], ['genesis', 'id'], ['participant', 'id'], ['source', 'id'], ['key', 'id'], ['token', 'id16'],
    ['expires', 'u64'], ['package', 'id'], ['gate', 'id']], ['sig']),
  'receipt': k('TANDEM-RECEIPT-V2', [['hub', 'id'], ['genesis', 'id'], ['outcome', 'u8'], ['subject', 'id'], ['signer', 'opt']], ['sig']),
  'home-epoch': k('TANDEM-HOME-EPOCH-V2', [['hub', 'id'], ['genesis', 'id'], ['participant', 'id'], ['n', 'u32'], ['parents', 'set'], ['key', 'id'],
    ['recipients', 'set'], ['snapshot', 'id'], ['signer', 'id'], ['auth', 'id']], ['sig']),
  'grant': k('TANDEM-GRANT-V2', [['hub', 'id'], ['collection', 'id16'], ['recipient', 'id'], ['admission', 'id'], ['at', 'id'], ['epoch', 'id'], ['key', 'id'],
    ['signer', 'id'], ['auth', 'id']], ['sig']),
  'key-request': k('TANDEM-KEYREQ-V2', [['hub', 'id'], ['collection', 'id16'], ['participant', 'id'], ['have', 'id'], ['signer', 'id'], ['auth', 'id']], ['sig']),
  'leave': k('TANDEM-LEAVE-V2', [['hub', 'id'], ['collection', 'id16'], ['participant', 'id'], ['at', 'id'], ['signer', 'id'], ['auth', 'id']], ['sig']),
  'answer': k('TANDEM-ANSWER-V2', [['hub', 'id'], ['collection', 'id16'], ['participant', 'id'], ['admission', 'id'], ['answer', 'answer'], ['event', 'id16'],
    ['signer', 'id'], ['auth', 'id']], ['sig']),
};
/** How a request's keys came in (§10.8), and receipt outcomes. */
export const How = {first: 1, invited: 2, byDevice: 3, byPairing: 4, creatorRecovery: 5} as const;
export const Outcome = {rejected: 1, pairingCancelled: 2, inviteCancelled: 3, paired: 4} as const;

export interface Mailbox {host: string; port: number; user: string}
/** id, id16: bytes; opt: bytes or null; text: string; set: sorted bytes; uN: number; answer: 'a' | 'n' | 'l'. */
export type FieldValue = Uint8Array | null | string | number | readonly Uint8Array[] | readonly Mailbox[];
export type Fields = Readonly<Record<string, FieldValue>>;
export type Signatures = Readonly<Record<string, Uint8Array>>;
/** By name: a `bytes` extra is bytes, a nested object (a certificate's `body`) its fields and signatures. */
export type Extras = Readonly<Record<string, Uint8Array | {fields: Fields; signatures: Signatures}>>;
export interface ControlObject {kind: string; fields: Record<string, FieldValue>; signatures: Record<string, Buffer>; extras: Record<string, Buffer | ControlObject>}
export class ControlError extends Error {}

/** The schema in the plain form vectors/v2/control.json carries. */
export function controlSchema(): Record<string, {tag: string; fields: [string, string][]; signatures: string[]; extras: [string, string][]}> {
  return Object.fromEntries(Object.entries(SCHEMA).map(([name, s]) => [name, {tag: s.tag, fields: s.fields.map(([n, t]) => [n, t]),
    signatures: [...s.signatures], extras: s.extras.map(([n, t]) => [n, t])}]));
}
function need(cond: unknown, what: string): asserts cond { if (!cond) throw new ControlError(what); }
function schema(kind: string): Kind { need(typeof kind === 'string' && Object.hasOwn(SCHEMA, kind), 'unknown control type'); return SCHEMA[kind]!; }
const isBytes = (v: unknown, n: number): v is Uint8Array => v instanceof Uint8Array && v.length === n;
const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
function uint(v: unknown, width: number): Buffer {
  need(typeof v === 'number' && Number.isInteger(v) && v >= 0 && v < Math.min(256 ** width, MAX_JSON_INTEGER + 1), `u${width * 8} out of range`);
  const b = Buffer.alloc(width); if (width === 8) b.writeBigUInt64BE(BigInt(v)); else b.writeUIntBE(v, 0, width); return b;
}
function text(v: unknown): Buffer {
  need(typeof v === 'string', 'text'); validUnicode(v); const raw = Buffer.from(v, 'utf8');
  need(raw.length <= MAX_TEXT, 'text longer than 256 bytes'); return Buffer.concat([uint(raw.length, 4), raw]);
}
function mailbox(m: unknown): Buffer {
  need(isRecord(m), 'mailbox'); const {host, port, user} = m;
  // ASCII only, so no platform's Unicode case mapping decides what "lowercase" means.
  need(typeof host === 'string' && /^[\x00-\x40\x5b-\x7f]*$/.test(host), 'mailbox host must be lowercase ASCII');
  need(typeof port === 'number' && Number.isInteger(port) && port > 0 && port < 65536, 'mailbox port');
  return Buffer.concat([text(host), uint(port, 2), text(user)]);
}
const ascending = (items: readonly Uint8Array[]): boolean => items.every((x, i) => i === 0 || Buffer.compare(items[i - 1]!, x) < 0);
function fieldBytes(type: FieldType, v: unknown): Buffer {
  switch (type) {
    case 'id': need(isBytes(v, 32), 'id must be 32 bytes'); return Buffer.from(v);
    case 'id16': need(isBytes(v, 16), 'id16 must be 16 bytes'); return Buffer.from(v);
    case 'opt': if (v === null) return Buffer.alloc(32);
      need(isBytes(v, 32) && v.some(b => b !== 0), 'optional id'); return Buffer.from(v);
    case 'text': return text(v);
    case 'set': need(Array.isArray(v) && v.length <= MAX_SET, 'set');
      need(v.every(x => isBytes(x, 32)), 'set members are 32-byte IDs'); need(ascending(v), 'set must be sorted and unique');
      return Buffer.concat([uint(v.length, 4), ...v]);
    case 'u8': return uint(v, 1);
    case 'u16': return uint(v, 2);
    case 'u32': return uint(v, 4);
    case 'u64': return uint(v, 8);
    case 'answer': need(v === 'a' || v === 'n' || v === 'l', 'answer must be a, n or l'); return Buffer.from(v, 'ascii');
    case 'mailboxes': { need(Array.isArray(v) && v.length <= MAX_MAILBOXES, 'mailboxes'); const encoded = v.map(mailbox);
      need(ascending(encoded), 'mailboxes must be sorted and unique'); return Buffer.concat([uint(encoded.length, 4), ...encoded]); }
  }
  throw new ControlError(`unknown field type ${String(type)}`);
}
/** The exact bytes an object's ID hashes and its signatures cover. */
export function signedInput(kind: string, fields: Fields): Buffer {
  const s = schema(kind); need(isRecord(fields), 'fields');
  for (const [name] of s.fields) need(Object.hasOwn(fields, name), `missing field ${name}`);
  return Buffer.concat([Buffer.from(s.tag, 'ascii'), ...s.fields.map(([name, type]) => fieldBytes(type, fields[name]))]);
}
export const objectID = (kind: string, fields: Fields): Buffer => sha(signedInput(kind, fields));
export const signControl = (kind: string, fields: Fields, key: KeyObject): Buffer => sign(key, signedInput(kind, fields));
export const verifyControl = (kind: string, fields: Fields, signerPublic: Uint8Array, signature: Uint8Array): boolean =>
  verifyStrict(signerPublic, signature, signedInput(kind, fields));

function fieldJSON(type: FieldType, v: FieldValue): JSONValue {
  switch (type) {
    case 'id': case 'id16': return b64u(v as Uint8Array);
    case 'opt': return v === null ? null : b64u(v as Uint8Array);
    case 'set': return (v as readonly Uint8Array[]).map(b64u);
    case 'mailboxes': return (v as readonly Mailbox[]).map(m => ({host: m.host, port: m.port, user: m.user}));
    default: return v as string | number;
  }
}
function jsonObject(kind: string, fields: Fields, signatures: Signatures = {}, extras: Extras = {}): {[name: string]: JSONValue} {
  const s = schema(kind); signedInput(kind, fields); // validate before serializing
  const out: {[name: string]: JSONValue} = {type: kind};
  for (const [name, type] of s.fields) out[name] = fieldJSON(type, fields[name]!);
  for (const raw of s.signatures) {
    const name = raw.replace(/\?$/, ''), value = Object.hasOwn(signatures, name) ? signatures[name] : undefined;
    need(value != null || raw.endsWith('?'), `missing signature ${name}`);
    if (value != null) { need(isBytes(value, 64), 'signature length'); out[name] = b64u(value); }
  }
  for (const [name, type] of s.extras) {
    const value = Object.hasOwn(extras, name) ? extras[name] : undefined; if (value == null) continue;
    if (type === 'bytes') out[name] = b64u(bytes(value as Uint8Array));
    else { need(!(value instanceof Uint8Array), `${name} must be an object`); out[name] = jsonObject(type, value.fields, value.signatures); }
  }
  return out;
}
/** Strict JSON (§10.5) for one object: `type`, the fields in schema order, the signatures present, then extras.
 * Non-ASCII text stays raw UTF-8, as the reference writes it. */
export const controlToJSON = (kind: string, fields: Fields, signatures?: Signatures, extras?: Extras): Buffer =>
  Buffer.from(JSON.stringify(jsonObject(kind, fields, signatures, extras)), 'utf8');

function fieldValue(type: FieldType, v: JSONValue | undefined): FieldValue {
  switch (type) {
    case 'id': case 'id16': { need(typeof v === 'string', type); const raw = unb64u(v);
      need(raw.length === (type === 'id' ? 32 : 16), `${type} length`); return raw; }
    case 'opt': return v === null ? null : fieldValue('id', v);
    case 'set': need(Array.isArray(v), 'set'); return v.map(x => fieldValue('id', x) as Buffer);
    case 'answer': need(typeof v === 'string', 'answer'); return v;
    case 'mailboxes': need(Array.isArray(v), 'mailboxes');
      // Validated by mailbox() when the input is rebuilt; a missing property is null there.
      return v.map(m => { need(isRecord(m), 'mailboxes'); return {host: m.host ?? null, port: m.port ?? null, user: m.user ?? null} as unknown as Mailbox; });
    default: return v as FieldValue;
  }
}
/** Read one parsed JSON value. Validates every field; unknown properties are ignored and
 * nothing is trusted that can be recomputed: the caller derives the ID and checks signatures.
 * An optional signature given as null is absent (§10.8). */
export function parseControl(value: JSONValue, expect?: string): ControlObject {
  need(isRecord(value), 'object'); const kind = value.type as string, s = schema(kind);
  need(expect === undefined || kind === expect, 'unexpected control type');
  const fields: Record<string, FieldValue> = {};
  for (const [name, type] of s.fields) { need(Object.hasOwn(value, name), `missing ${name}`); fields[name] = fieldValue(type, value[name]); }
  signedInput(kind, fields);
  const signatures: Record<string, Buffer> = {};
  for (const raw of s.signatures) {
    const name = raw.replace(/\?$/, '');
    if (Object.hasOwn(value, name) && value[name] !== null) {
      const sig = unb64u(value[name] as string); need(sig.length === 64, 'signature length'); signatures[name] = sig;
    } else need(raw.endsWith('?'), `missing signature ${name}`);
  }
  const extras: ControlObject['extras'] = {};
  for (const [name, type] of s.extras) {
    if (Object.hasOwn(value, name)) extras[name] = type === 'bytes' ? unb64u(value[name] as string) : parseControl(value[name]!, type);
  }
  // A carried object must be the one the signed fields name.
  const capsule = extras.capsule, body = extras.body;
  if (kind === 'capsule' && capsule) need(sha(capsule as Buffer).equals(fields.sealed as Buffer), 'capsule bytes do not hash to sealed');
  if (kind === 'certificate' && body) need(objectID('request', (body as ControlObject).fields).equals(fields.request as Buffer), 'body is not the named request');
  return {kind, fields, signatures, extras};
}
/** The strict reader (§10.5), then the schema. */
export const controlFromJSON = (data: Uint8Array, expect?: string): ControlObject => parseControl(parseJSON(data), expect);

// ---- Derivations and boxes (§10.8) ----
export const inviteKey = (secret: Uint8Array, hub: Uint8Array, invite: Uint8Array): KeyObject =>
  privateKey(kdf(secret, Buffer.concat([bytes(hub, 32), bytes(invite, 16)]), 'tandem/v2/invite-key'), 'ed25519');
const pairing = (link: Uint8Array, hub: Uint8Array, token: Uint8Array, info: string, size = 32): Buffer =>
  kdf(bytes(link, 32), Buffer.concat([bytes(hub, 32), bytes(token, 16)]), info, size);
export const pairingInviteKey = (link: Uint8Array, hub: Uint8Array, token: Uint8Array): KeyObject =>
  privateKey(pairing(link, hub, token, 'tandem/v2/pair-invite'), 'ed25519');
export const pairingKey = (link: Uint8Array, hub: Uint8Array, token: Uint8Array): Buffer => pairing(link, hub, token, 'tandem/v2/pair-key');
export const pairingTag = (link: Uint8Array, hub: Uint8Array, token: Uint8Array): string => b32(pairing(link, hub, token, 'tandem/v2/pair-tag', 20));
/** The opaque tag a wake hint names (§10.3). */
export const wakeTag = (directoryKey: Uint8Array, hub: Uint8Array): string => b32(kdf(bytes(directoryKey, 32), bytes(hub, 32), 'tandem/v2/wake-tag', 20));
export const directoryScope = (hub: Uint8Array): Buffer => sha(Buffer.from('tandem/v2/directory'), bytes(hub, 32)).subarray(0, 16);
export const registrationID = (hub: Uint8Array, signPublic: Uint8Array, agreePublic: Uint8Array, enrollment: Uint8Array): Buffer =>
  objectID('registration', {hub, sign: signPublic, agree: agreePublic, enrollment});
function packageContext(link: Uint8Array, gateSecret: Uint8Array, hub: Uint8Array, token: Uint8Array): [Buffer, Buffer] {
  bytes(link, 32); bytes(gateSecret, 32); bytes(hub, 32); bytes(token, 16);
  return [kdf(Buffer.concat([link, gateSecret]), token, 'tandem/v2/pair-package'), Buffer.concat([Buffer.from('TANDEM-PAIR-PACKAGE-V2'), hub, token])];
}
/** nonce || ChaCha20-Poly1305 under KDF(L || A, token, "tandem/v2/pair-package"). */
export function sealPackage(link: Uint8Array, gateSecret: Uint8Array, hub: Uint8Array, token: Uint8Array, plaintext: Uint8Array, nonce: Uint8Array = randomBytes(12)): Buffer {
  const [key, ad] = packageContext(link, gateSecret, hub, token);
  return Buffer.concat([bytes(nonce, 12), encrypt(key, nonce, ad, plaintext)]);
}
export function openPackage(link: Uint8Array, gateSecret: Uint8Array, hub: Uint8Array, token: Uint8Array, box: Uint8Array): Buffer {
  const [key, ad] = packageContext(link, gateSecret, hub, token); need(box.length > 12, 'package');
  return decrypt(key, box.subarray(0, 12), ad, box.subarray(12));
}
const gateAAD = (hub: Uint8Array, token: Uint8Array, participant: Uint8Array): Buffer =>
  Buffer.concat([Buffer.from('TANDEM-PAIR-GATE-V2'), bytes(hub, 32), bytes(token, 16), bytes(participant, 32)]);
/** A, HPKE-sealed to MKa. */
export const sealGate = (managerAgreePublic: Uint8Array, hub: Uint8Array, token: Uint8Array, participant: Uint8Array, gateSecret: Uint8Array): Buffer =>
  sealHPKE(managerAgreePublic, 'tandem/v2/pair-gate', gateAAD(hub, token, participant), gateSecret);
export const openGate = (managerAgree: KeyObject, hub: Uint8Array, token: Uint8Array, participant: Uint8Array, gate: Uint8Array): Buffer =>
  openHPKE(managerAgree, 'tandem/v2/pair-gate', gateAAD(hub, token, participant), gate);
const recoveryAAD = (hub: Uint8Array, epoch: Uint8Array): Buffer => Buffer.concat([Buffer.from('TANDEM-HOME-RECOVERY-V2'), bytes(hub, 32), bytes(epoch, 32)]);
/** The creator's home key, HPKE-sealed to MKa (§6.8). */
export const sealHomeRecovery = (managerAgreePublic: Uint8Array, hub: Uint8Array, epoch: Uint8Array, homeKey: Uint8Array): Buffer =>
  sealHPKE(managerAgreePublic, 'tandem/v2/home-recovery', recoveryAAD(hub, epoch), homeKey);
export const openHomeRecovery = (managerAgree: KeyObject, hub: Uint8Array, epoch: Uint8Array, box: Uint8Array): Buffer =>
  openHPKE(managerAgree, 'tandem/v2/home-recovery', recoveryAAD(hub, epoch), box);
