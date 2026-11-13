import {Buffer} from "buffer";
import {randomBytes, hash256, hash512, hmac256, hkdf, privateKey, publicKey, sign, sharedSecret, encrypt, decrypt} from "#platform-crypto";
import type {PrivateKey} from "./key.js";
export {randomBytes, privateKey, publicKey, sign, encrypt, decrypt};
export type {PrivateKey} from "./key.js";

export const sha = (...parts: Uint8Array[]): Buffer => Buffer.from(hash256(Buffer.concat(parts)));
export const kdf = (key: Uint8Array, salt: Uint8Array, info: string, size = 32): Buffer =>
  Buffer.from(hkdf(key, salt, Buffer.from(info, 'ascii'), size));
export const b64u = (value: Uint8Array): string => Buffer.from(value).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
export function unb64u(text: string): Buffer {
  if (typeof text !== 'string' || !/^[A-Za-z0-9_-]*$/.test(text)) throw new Error('invalid base64url');
  const b = Buffer.from(text.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - text.length % 4) % 4), 'base64'); if (b64u(b) !== text) throw new Error('noncanonical base64url'); return b;
}
const alphabet = 'abcdefghijklmnopqrstuvwxyz234567';
export function b32(bytes: Uint8Array): string {
  let out = '', acc = 0, bits = 0;
  for (const b of bytes) { acc = (acc << 8) | b; bits += 8; while (bits >= 5) { bits -= 5; out += alphabet[(acc >>> bits) & 31]; } }
  if (bits) out += alphabet[(acc << (5 - bits)) & 31]; return out;
}
export function unb32(text: string): Buffer {
  let acc = 0, bits = 0; const out: number[] = [];
  for (const c of text) { const n = alphabet.indexOf(c); if (n < 0) throw new Error('invalid base32'); acc = (acc << 5) | n; bits += 5;
    if (bits >= 8) { bits -= 8; out.push((acc >>> bits) & 255); } }
  const b = Buffer.from(out); if (b32(b) !== text) throw new Error('noncanonical base32'); return b;
}
export function bytes(value: Uint8Array, n?: number): Buffer {
  if (!(value instanceof Uint8Array) || (n !== undefined && value.length !== n)) throw new Error('invalid byte length or type');
  return Buffer.from(value);
}
export function deviceKeys(): {signing: PrivateKey; agreement: PrivateKey} {
  return {signing: privateKey(randomBytes(32), 'ed25519'), agreement: privateKey(randomBytes(32), 'x25519')};
}
/** Keep this ID and its durable clock per installation; never sync them with keys. */
export const newInstallationID = (): Buffer => randomBytes(8);
export function managerKeys(seed: Uint8Array) {
  const s = bytes(seed, 32); return {signing: privateKey(kdf(s, Buffer.alloc(0), 'tandem/v2/manager-sign'), 'ed25519'),
    agreement: privateKey(kdf(s, Buffer.alloc(0), 'tandem/v2/manager-agree'), 'x25519')};
}
const smallOrder = new Set([
  '0100000000000000000000000000000000000000000000000000000000000000',
  'ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f',
  '0000000000000000000000000000000000000000000000000000000000000000',
  '0000000000000000000000000000000000000000000000000000000000000080',
  '26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05',
  '26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85',
  'c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a',
  'c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa',
]);
const little = (b: Uint8Array): bigint => BigInt('0x' + Buffer.from(b).reverse().toString('hex'));
const P = (1n << 255n) - 19n, L = (1n << 252n) + 27742317777372353535851937790883648493n;
// Public-input Edwards arithmetic only. Native OpenSSL verifiers reject some
// valid mixed-order signatures; the protocol fixes the plain equation instead.
// Extended coordinates avoid an inversion at every addition (RFC 8032 appendix A).
type Point = readonly [bigint, bigint, bigint, bigint];
const mod = (n: bigint): bigint => ((n % P) + P) % P;
function power(base: bigint, exponent: bigint): bigint {
  let out = 1n; base = mod(base);
  while (exponent) { if (exponent & 1n) out = mod(out * base); base = mod(base * base); exponent >>= 1n; } return out;
}
const D = mod(-121665n * power(121666n, P - 2n));
const identity: Point = [0n, 1n, 1n, 0n];
function point(raw: Uint8Array): Point {
  if (!canonical(raw)) throw new Error('noncanonical point');
  const n = little(raw), y = n & ((1n << 255n) - 1n), signBit = n >> 255n;
  const denominator = mod(D * y * y + 1n); if (!denominator) throw new Error('invalid point');
  const x2 = mod((y * y - 1n) * power(denominator, P - 2n)); let x = power(x2, (P + 3n) / 8n);
  if (mod(x * x) !== x2) x = mod(x * power(2n, (P - 1n) / 4n));
  if (mod(x * x) !== x2) throw new Error('point is off curve');
  if ((x & 1n) !== signBit) x = mod(-x); return [x, y, 1n, mod(x * y)];
}
function add(a: Point, b: Point): Point {
  const aa = mod((a[1] - a[0]) * (b[1] - b[0])), bb = mod((a[1] + a[0]) * (b[1] + b[0]));
  const cc = mod(2n * D * a[3] * b[3]), dd = mod(2n * a[2] * b[2]);
  const e = bb - aa, f = dd - cc, g = dd + cc, h = bb + aa;
  return [mod(e * f), mod(g * h), mod(f * g), mod(e * h)];
}
function multiply(p: Point, scalar: bigint): Point {
  let result = identity;
  while (scalar) { if (scalar & 1n) result = add(result, p); p = add(p, p); scalar >>= 1n; } return result;
}
const equalPoint = (a: Point, b: Point): boolean => mod(a[0] * b[2]) === mod(b[0] * a[2]) && mod(a[1] * b[2]) === mod(b[1] * a[2]);
const basePoint = point(Buffer.from('5866666666666666666666666666666666666666666666666666666666666666', 'hex'));
function canonical(b: Uint8Array): boolean {
  if (b.length !== 32) return false;
  const n = little(b), y = n & ((1n << 255n) - 1n), signBit = n >> 255n;
  return y < P && !(signBit === 1n && (y === 1n || y === P - 1n));
}
export function verifyStrict(publicBytes: Uint8Array, signature: Uint8Array, message: Uint8Array): boolean {
  if (signature.length !== 64 || !canonical(publicBytes) || smallOrder.has(Buffer.from(publicBytes).toString('hex')) ||
      !canonical(signature.subarray(0, 32)) || little(signature.subarray(32)) >= L) return false;
  try {
    const a = point(publicBytes), r = point(signature.subarray(0, 32));
    if (equalPoint(multiply(a, 8n), identity)) return false;
    const hash = hash512(Buffer.concat([signature.subarray(0, 32), publicBytes, message]));
    return equalPoint(multiply(basePoint, little(signature.subarray(32))), add(r, multiply(a, little(hash) % L)));
  } catch { return false; }
}
export const homeScope = (hub: Uint8Array, uid: Uint8Array): Buffer => sha(Buffer.from('tandem/v2/home'), hub, uid).subarray(0, 16);
export const namedID = (name: string): Buffer => sha(Buffer.from('tandem/v2/named'), Buffer.from(name)).subarray(0, 16);
export const keyID = (hub: Uint8Array, agreement: Uint8Array): Buffer => sha(Buffer.from('tandem/v2/kid'), hub, agreement);
export const commit = (key: Uint8Array): Buffer => sha(Buffer.from('tandem/v2/commit'), key);
export const collectionTag = (key: Uint8Array, hub: Uint8Array, epoch: Uint8Array): string => b32(kdf(key, Buffer.concat([hub, epoch]), 'tandem/v2/collection-tag', 20));
export const homeTag = (key: Uint8Array, hub: Uint8Array, uid: Uint8Array, epoch: Uint8Array): string => b32(kdf(key, Buffer.concat([hub, uid, epoch]), 'tandem/v2/home-tag', 20));
export const inboxTag = (uid: Uint8Array, hub: Uint8Array): string => b32(kdf(uid, hub, 'tandem/v2/inbox-tag', 20));
export const directoryKey = (key: Uint8Array, hub: Uint8Array): Buffer => kdf(key, hub, 'tandem/v2/directory-key');
export const directoryTag = (key: Uint8Array, hub: Uint8Array): string => b32(kdf(key, hub, 'tandem/v2/directory-tag', 20));
export const aad = (kind: number, hub: Uint8Array, tag: string): Buffer => Buffer.concat([Buffer.from([84, 78, 68, 77, 2, kind]), hub, Buffer.from(tag, 'ascii')]);
function context(key: Uint8Array, hub: Uint8Array, tag: string): void {
  bytes(key, 32); bytes(hub, 32); if (unb32(tag).length !== 20) throw new Error('invalid channel tag');
}
export function seal(key: Uint8Array, hub: Uint8Array, tag: string, plaintext: Uint8Array, kind = 1): Buffer {
  context(key, hub, tag); if (kind !== 1 && kind !== 3) throw new Error('not a symmetric kind');
  const salt = randomBytes(32); return Buffer.concat([Buffer.from([84, 78, 68, 77, 2, kind]), salt,
    encrypt(kdf(key, salt, 'tandem/v2/msg'), Buffer.alloc(12), aad(kind, hub, tag), plaintext)]);
}
export function openEnvelope(key: Uint8Array, hub: Uint8Array, tag: string, env: Uint8Array): {kind: number; plaintext: Buffer} {
  context(key, hub, tag); const kind = env[5];
  if (env.length < 54 || !Buffer.from(env.subarray(0, 5)).equals(Buffer.from('TNDM\x02')) || (kind !== 1 && kind !== 3)) throw new Error('invalid v2 envelope');
  return {kind, plaintext: decrypt(kdf(key, env.subarray(6, 38), 'tandem/v2/msg'), Buffer.alloc(12), aad(kind, hub, tag), env.subarray(38))};
}
const KEM = Buffer.from('4b454d0020', 'hex'), SUITE = Buffer.from('48504b45002000010003', 'hex');
function extract(salt: Uint8Array, suite: Uint8Array, label: string, ikm: Uint8Array): Buffer {
  return Buffer.from(hmac256(salt, Buffer.concat([Buffer.from('HPKE-v1'), suite, Buffer.from(label), ikm])));
}
function expand(prk: Uint8Array, suite: Uint8Array, label: string, info: Uint8Array, size: number): Buffer {
  // This suite asks only for 12 or 32 bytes: one HKDF-Expand block.
  const length = Buffer.alloc(2); length.writeUInt16BE(size);
  return Buffer.from(hmac256(prk, Buffer.concat([length, Buffer.from('HPKE-v1'), suite, Buffer.from(label), info, Buffer.from([1])]))).subarray(0, size);
}
function schedule(dh: Uint8Array, enc: Uint8Array, recipient: Uint8Array, info: string): [Buffer, Buffer] {
  const empty = Buffer.alloc(0), shared = expand(extract(empty, KEM, 'eae_prk', dh), KEM, 'shared_secret', Buffer.concat([enc, recipient]), 32);
  const ctx = Buffer.concat([Buffer.from([0]), extract(empty, SUITE, 'psk_id_hash', empty), extract(empty, SUITE, 'info_hash', Buffer.from(info, 'ascii'))]);
  const secret = extract(shared, SUITE, 'secret', empty);
  return [expand(secret, SUITE, 'key', ctx, 32), expand(secret, SUITE, 'base_nonce', ctx, 12)];
}
export function sealHPKE(recipient: Uint8Array, info: string, ad: Uint8Array, plaintext: Uint8Array): Buffer {
  const ephemeral = privateKey(randomBytes(32), 'x25519'), enc = publicKey(ephemeral);
  const [key, nonce] = schedule(sharedSecret(ephemeral, recipient), enc, recipient, info);
  return Buffer.concat([enc, encrypt(key, nonce, ad, plaintext)]);
}
export function openHPKE(privateValue: PrivateKey, info: string, ad: Uint8Array, sealed: Uint8Array): Buffer {
  if (sealed.length < 48) throw new Error('short HPKE input'); const enc = sealed.subarray(0, 32);
  const [key, nonce] = schedule(sharedSecret(privateValue, enc), enc, publicKey(privateValue), info);
  return decrypt(key, nonce, ad, sealed.subarray(32));
}
export function sealInbox(hub: Uint8Array, uid: Uint8Array, recipient: Uint8Array, plaintext: Uint8Array): Buffer {
  bytes(uid, 32); const tag = inboxTag(uid, hub); context(recipient, hub, tag);
  return Buffer.concat([Buffer.from('TNDM\x02\x02'), keyID(hub, recipient), sealHPKE(recipient, 'tandem/v2/inbox', aad(2, hub, tag), plaintext)]);
}
export function openInbox(hub: Uint8Array, uid: Uint8Array, privateValue: PrivateKey, env: Uint8Array): Buffer {
  bytes(hub, 32); bytes(uid, 32);
  if (env.length < 86 || !Buffer.from(env.subarray(0, 6)).equals(Buffer.from('TNDM\x02\x02')) || !Buffer.from(env.subarray(6, 38)).equals(keyID(hub, publicKey(privateValue)))) throw new Error('not an inbox envelope for this device');
  return openHPKE(privateValue, 'tandem/v2/inbox', aad(2, hub, inboxTag(uid, hub)), env.subarray(38));
}
