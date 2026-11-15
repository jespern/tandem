import {Buffer} from 'buffer';
import {sha256, sha512} from '@noble/hashes/sha2.js';
import {hmac} from '@noble/hashes/hmac.js';
import {hkdf as derive} from '@noble/hashes/hkdf.js';
import {ed25519, x25519} from '@noble/curves/ed25519.js';
import {chacha20poly1305} from '@noble/ciphers/chacha.js';
import type {PrivateKey} from '../key.js';

export function randomBytes(size: number): Buffer {
  if (!Number.isSafeInteger(size) || size < 0) throw new Error('invalid random byte count');
  const out = new Uint8Array(size);
  for (let at=0;at<size;at+=65536) globalThis.crypto.getRandomValues(out.subarray(at,Math.min(size,at+65536)));
  return Buffer.from(out);
}
export const hash256 = sha256, hash512 = sha512;
export const hmac256 = (key: Uint8Array, data: Uint8Array): Uint8Array => hmac(sha256, key, data);
export const hkdf = (key: Uint8Array, salt: Uint8Array, info: Uint8Array, size: number): Uint8Array => derive(sha256, key, salt, info, size);
export function privateKey(raw: Uint8Array, algorithm: PrivateKey['algorithm']): PrivateKey {
  if (algorithm !== 'ed25519' && algorithm !== 'x25519') throw new Error('unknown key algorithm');
  if (raw.length !== 32) throw new Error('private key must be 32 bytes');
  const seed = Uint8Array.from(raw);
  return Object.freeze({algorithm,
    publicBytes: () => (algorithm === 'ed25519' ? ed25519 : x25519).getPublicKey(seed),
    sign: (message: Uint8Array) => { if (algorithm !== 'ed25519') throw new Error('not a signing key'); return ed25519.sign(message, seed); },
    agree: (recipient: Uint8Array) => { if (algorithm !== 'x25519') throw new Error('not an agreement key'); return x25519.getSharedSecret(seed, recipient); }});
}
export const publicKey = (key: PrivateKey): Buffer => Buffer.from(key.publicBytes());
export const sign = (key: PrivateKey, message: Uint8Array): Buffer => Buffer.from(key.sign(message));
export const sharedSecret = (key: PrivateKey, recipient: Uint8Array): Uint8Array => key.agree(recipient);
export const encrypt = (key: Uint8Array, nonce: Uint8Array, ad: Uint8Array, plain: Uint8Array): Buffer => Buffer.from(chacha20poly1305(key, nonce, ad).encrypt(plain));
export const decrypt = (key: Uint8Array, nonce: Uint8Array, ad: Uint8Array, sealed: Uint8Array): Buffer => Buffer.from(chacha20poly1305(key, nonce, ad).decrypt(sealed));
