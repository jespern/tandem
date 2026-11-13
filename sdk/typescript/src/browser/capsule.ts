import {Buffer} from 'buffer';
import {bytes, randomBytes, kdf, encrypt, decrypt, managerKeys, publicKey} from '../crypto.js';
import {validUnicode} from '../json.js';

export const CAPSULE_LENGTH = 76;
export interface CapsuleOptions {signal?: AbortSignal; workerUrl?: URL}
function passwordBytes(password: string, choosing: boolean): Uint8Array {
  validUnicode(password); const text = password.normalize('NFC'), raw = new TextEncoder().encode(text);
  if (choosing && ([...text].length < 12 || raw.length > 1024)) throw new Error('choose a hub password of at least 12 code points and at most 1024 UTF-8 bytes');
  return raw;
}
function checkSeed(seed: Uint8Array, hub: Uint8Array): void {
  bytes(hub, 32); bytes(seed, 32);
  if (!publicKey(managerKeys(seed).signing).equals(Buffer.from(hub))) throw new Error('capsule belongs to another hub');
}
function stretch(password: Uint8Array, salt: Uint8Array, options: CapsuleOptions): Promise<Uint8Array> {
  options.signal?.throwIfAborted();
  const worker = new Worker(options.workerUrl ?? new URL('./argon-worker.js', import.meta.url), {type: 'module'});
  return new Promise((resolve, reject) => {
    const cleanup = () => { worker.terminate(); options.signal?.removeEventListener('abort', abort); };
    const abort = () => { cleanup(); reject(options.signal!.reason); };
    options.signal?.addEventListener('abort', abort, {once: true});
    worker.onerror = () => { cleanup(); reject(new Error('capsule worker failed')); };
    worker.onmessage = ({data}) => { cleanup(); if (data.error) reject(new Error(data.error)); else if (!(data.key instanceof Uint8Array) || data.key.length !== 32) reject(new Error('invalid capsule key')); else resolve(data.key); };
    worker.postMessage({password, salt: Uint8Array.from(salt)}, [password.buffer]);
  });
}
const aad = (hub: Uint8Array, salt: Uint8Array) => Buffer.concat([Buffer.from('TANDEM-ADMIN-CAPSULE-V2'), hub, salt]);
export async function openCapsule(password: string, hub: Uint8Array, capsule: Uint8Array, options: CapsuleOptions = {}): Promise<Uint8Array> {
  hub = bytes(hub,32); capsule = bytes(capsule,CAPSULE_LENGTH);
  const salt = capsule.subarray(0, 16), ck = await stretch(passwordBytes(password, false), salt, options);
  try { const seed = decrypt(kdf(ck, hub, 'tandem/v2/admin-capsule'), capsule.subarray(16, 28), aad(hub, salt), capsule.subarray(28)); checkSeed(seed, hub); return seed; }
  finally { ck.fill(0); }
}
export async function sealCapsule(password: string, hub: Uint8Array, seed: Uint8Array, options: CapsuleOptions = {}): Promise<Uint8Array> {
  hub = bytes(hub,32); seed = bytes(seed,32);
  checkSeed(seed, hub); const salt = randomBytes(16), nonce = randomBytes(12), ck = await stretch(passwordBytes(password, true), salt, options);
  try { return Buffer.concat([salt, nonce, encrypt(kdf(ck, hub, 'tandem/v2/admin-capsule'), nonce, aad(hub, salt), seed)]); }
  finally { ck.fill(0); }
}
