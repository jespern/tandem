import {privateKey, randomBytes, type PrivateKey} from '../crypto.js';

const request = <T>(req: IDBRequest<T>): Promise<T> => new Promise((resolve, reject) => { req.onsuccess = () => resolve(req.result); req.onerror = () => reject(req.error); });
/** This profile remembers enrollment, without inventing another identity password.
 * Nonextractable WebCrypto wraps the secret seeds. Same-origin code can use them;
 * this is browser-profile storage, not an OS keychain or XSS protection. */
export class BrowserKeychain {
  readonly #db: IDBDatabase;
  private constructor(db: IDBDatabase) { this.#db = db; db.onversionchange = () => this.close(); }
  static async open(name = 'tandem-keychain'): Promise<BrowserKeychain> {
    const opening = indexedDB.open(name, 1); opening.onupgradeneeded = () => opening.result.createObjectStore('keys').put('tandem-keychain-v2','application');
    const db = await request(opening);
    try { if (await request(db.transaction('keys').objectStore('keys').get('application')) !== 'tandem-keychain-v2') throw new Error('not a Tandem keychain'); }
    catch (error) { db.close(); throw error; }
    return new BrowserKeychain(db);
  }
  async #get<T>(name: string): Promise<T | undefined> { return await request(this.#db.transaction('keys').objectStore('keys').get(name)) as T | undefined; }
  #put(name: string, value: unknown): Promise<void> {
    const tx = this.#db.transaction('keys', 'readwrite', {durability: 'strict'}); tx.objectStore('keys').put(value, name);
    return new Promise((resolve, reject) => { tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error); });
  }
  async #wrap(): Promise<CryptoKey> {
    const saved = await this.#get<CryptoKey>('wrap'); if (saved) return saved;
    const key = await crypto.subtle.generateKey({name: 'AES-GCM', length: 256}, false, ['encrypt', 'decrypt']); await this.#put('wrap', key); return key;
  }
  async get(name: string): Promise<Uint8Array | undefined> {
    const saved = await this.#get<{iv: Uint8Array; ciphertext: ArrayBuffer}>(`secret:${name}`); if (!saved) return;
    const key = await this.#get<CryptoKey>('wrap'); if (!key) throw new Error('browser key wrapping key is missing; pair this browser again');
    return new Uint8Array(await crypto.subtle.decrypt({name: 'AES-GCM', iv: Uint8Array.from(saved.iv), additionalData: new TextEncoder().encode(name)}, key, saved.ciphertext));
  }
  async save(name: string, value: Uint8Array): Promise<void> {
    await navigator.locks.request(`tandem:keys:${this.#db.name}`, async () => {
      const key = await this.#wrap(), iv = Uint8Array.from(randomBytes(12));
      const ciphertext = await crypto.subtle.encrypt({name: 'AES-GCM', iv, additionalData: new TextEncoder().encode(name)}, key, Uint8Array.from(value));
      await this.#put(`secret:${name}`, {iv, ciphertext});
    });
  }
  async deviceKeys(): Promise<{signing: PrivateKey; agreement: PrivateKey}> {
    const seed = await navigator.locks.request(`tandem:keys:${this.#db.name}`, async () => {
      const saved = await this.get('device'); if (saved) { if (saved.length !== 64) throw new Error('invalid browser device keys'); return saved; }
      const value = randomBytes(64), key = await this.#wrap(), iv = Uint8Array.from(randomBytes(12));
      const ciphertext = await crypto.subtle.encrypt({name: 'AES-GCM', iv, additionalData: new TextEncoder().encode('device')}, key, Uint8Array.from(value));
      await this.#put('secret:device', {iv, ciphertext}); return value;
    });
    return {signing: privateKey(seed.subarray(0, 32), 'ed25519'), agreement: privateKey(seed.subarray(32), 'x25519')};
  }
  close(): void { this.#db.close(); }
}
