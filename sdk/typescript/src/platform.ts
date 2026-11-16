import {Buffer} from 'buffer';
import {createHash, createHmac, hkdfSync, randomBytes as nativeRandom, createPrivateKey, createPublicKey,
  sign as nativeSign, diffieHellman, createCipheriv, createDecipheriv} from 'node:crypto';
import type {PrivateKey} from './key.js';

export const randomBytes = (size: number): Buffer => nativeRandom(size);
export const hash256 = (data: Uint8Array): Uint8Array => createHash('sha256').update(data).digest();
/** Incremental SHA-256, for content too large to hold at once. */
export function hashStream(): {update(data: Uint8Array): void; digest(): Uint8Array} { const h = createHash('sha256'); return {update: data => { h.update(data); }, digest: () => h.digest()}; }
export const hash512 = (data: Uint8Array): Uint8Array => createHash('sha512').update(data).digest();
export const hmac256 = (key: Uint8Array, data: Uint8Array): Uint8Array => createHmac('sha256', key).update(data).digest();
export const hkdf = (key: Uint8Array, salt: Uint8Array, info: Uint8Array, size: number): Uint8Array => new Uint8Array(hkdfSync('sha256', key, salt, info, size));
export function privateKey(raw: Uint8Array, algorithm: PrivateKey['algorithm']): PrivateKey {
  if (algorithm !== 'ed25519' && algorithm !== 'x25519') throw new Error('unknown key algorithm');
  if (raw.length !== 32) throw new Error('private key must be 32 bytes');
  const key = createPrivateKey({key: Buffer.concat([Buffer.from(algorithm === 'ed25519' ? '302e020100300506032b657004220420' : '302e020100300506032b656e04220420', 'hex'), raw]), format: 'der', type: 'pkcs8'});
  return Object.freeze({algorithm,
    publicBytes: () => Buffer.from(createPublicKey(key).export({format: 'der', type: 'spki'})).subarray(-32),
    sign: (message: Uint8Array) => { if (algorithm !== 'ed25519') throw new Error('not a signing key'); return nativeSign(null, message, key); },
    agree: (recipient: Uint8Array) => {
      if (algorithm !== 'x25519' || recipient.length !== 32) throw new Error('invalid agreement key');
      const pub = createPublicKey({key: Buffer.concat([Buffer.from('302a300506032b656e032100', 'hex'), recipient]), format: 'der', type: 'spki'});
      return diffieHellman({privateKey: key, publicKey: pub});
    }});
}
export const publicKey = (key: PrivateKey): Buffer => Buffer.from(key.publicBytes());
export const sign = (key: PrivateKey, message: Uint8Array): Buffer => Buffer.from(key.sign(message));
export const sharedSecret = (key: PrivateKey, recipient: Uint8Array): Uint8Array => key.agree(recipient);
export function encrypt(key: Uint8Array, nonce: Uint8Array, ad: Uint8Array, plain: Uint8Array): Buffer {
  const c = createCipheriv('chacha20-poly1305', key, nonce, {authTagLength: 16}); c.setAAD(ad, {plaintextLength: plain.length});
  return Buffer.concat([c.update(plain), c.final(), c.getAuthTag()]);
}
export function decrypt(key: Uint8Array, nonce: Uint8Array, ad: Uint8Array, sealed: Uint8Array): Buffer {
  if (sealed.length < 16) throw new Error('short ciphertext');
  const d = createDecipheriv('chacha20-poly1305', key, nonce, {authTagLength: 16});
  d.setAuthTag(sealed.subarray(-16)); d.setAAD(ad, {plaintextLength: sealed.length - 16});
  return Buffer.concat([d.update(sealed.subarray(0, -16)), d.final()]);
}
