import {argon2id} from '@noble/hashes/argon2.js';

const scope = globalThis as unknown as {onmessage: (event: MessageEvent<{password: Uint8Array; salt: Uint8Array}>) => void;
  postMessage(message: unknown, transfer?: Transferable[]): void; close(): void};
scope.onmessage = ({data}) => {
  try {
    if (data.salt.length !== 16) throw new Error('invalid capsule salt');
    const key = argon2id(data.password, data.salt, {m: 1048576, t: 3, p: 1, version: 0x13, dkLen: 32, maxmem: 2**30});
    data.password.fill(0); scope.postMessage({key}, [key.buffer]);
  } catch { scope.postMessage({error: 'The browser could not complete the required 1 GiB Argon2id operation.'}); }
  finally { scope.close(); }
};
