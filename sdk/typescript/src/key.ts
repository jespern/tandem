/** Opaque SDK key handle. Secret bytes stay inside the selected crypto backend. */
export interface PrivateKey {
  readonly algorithm: 'ed25519' | 'x25519';
  publicBytes(): Uint8Array;
  sign(message: Uint8Array): Uint8Array;
  agree(recipient: Uint8Array): Uint8Array;
}
