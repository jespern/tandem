"""Tandem v2 primitives. Keys/signing/AEAD use cryptography; HPKE follows RFC 9180."""
import base64
import hashlib
import hmac
import os
import struct

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.hkdf import HKDF, HKDFExpand


def sha(*parts: bytes) -> bytes:
    return hashlib.sha256(b"".join(parts)).digest()


def kdf(key: bytes, salt: bytes, info: str, size: int = 32) -> bytes:
    return HKDF(hashes.SHA256(), size, salt, info.encode("ascii")).derive(key)


def public(key) -> bytes:
    return key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)


def b64u(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def unb64u(text: str) -> bytes:
    if not isinstance(text, str) or any(c not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" for c in text):
        raise ValueError("invalid base64url alphabet")
    raw = base64.b64decode(text + "=" * (-len(text) % 4), altchars=b"-_", validate=True)
    if b64u(raw) != text:
        raise ValueError("noncanonical base64url")
    return raw


def b32(value: bytes) -> str:
    return base64.b32encode(value).rstrip(b"=").lower().decode("ascii")


def unb32(text: str) -> bytes:
    raw = base64.b32decode(text.upper() + "=" * (-len(text) % 8))
    if b32(raw) != text:
        raise ValueError("noncanonical base32")
    return raw


def manager_keys(seed: bytes):
    if len(seed) != 32:
        raise ValueError("management seed must be 32 bytes")
    return (ed25519.Ed25519PrivateKey.from_private_bytes(kdf(seed, b"", "tandem/v2/manager-sign")),
            x25519.X25519PrivateKey.from_private_bytes(kdf(seed, b"", "tandem/v2/manager-agree")))


def device_keys():
    """Create once per keychain; reuse existing keys when the platform syncs them."""
    return ed25519.Ed25519PrivateKey.generate(), x25519.X25519PrivateKey.generate()


def new_installation_id() -> bytes:
    """Keep with this installation's durable clock; never sync it with the keys."""
    return os.urandom(8)


def home_scope(hub: bytes, uid: bytes) -> bytes:
    return sha(b"tandem/v2/home", hub, uid)[:16]


def named_id(name: str) -> bytes:
    return sha(b"tandem/v2/named", name.encode("utf-8"))[:16]


def key_id(hub: bytes, agreement: bytes) -> bytes:
    return sha(b"tandem/v2/kid", hub, agreement)


def commit(key: bytes) -> bytes:
    return sha(b"tandem/v2/commit", key)


def collection_tag(key: bytes, hub: bytes, epoch: bytes) -> str:
    return b32(kdf(key, hub + epoch, "tandem/v2/collection-tag", 20))


def home_tag(key: bytes, hub: bytes, uid: bytes, epoch: bytes) -> str:
    return b32(kdf(key, hub + uid + epoch, "tandem/v2/home-tag", 20))


def inbox_tag(uid: bytes, hub: bytes) -> str:
    return b32(kdf(uid, hub, "tandem/v2/inbox-tag", 20))


def directory_key(key: bytes, hub: bytes) -> bytes:
    return kdf(key, hub, "tandem/v2/directory-key")


def directory_tag(key: bytes, hub: bytes) -> str:
    return b32(kdf(key, hub, "tandem/v2/directory-tag", 20))


def aad(kind: int, hub: bytes, tag: str) -> bytes:
    return b"TNDM" + bytes((2, kind)) + hub + tag.encode("ascii")


def _context(key: bytes, hub: bytes, tag: str):
    if len(key) != 32 or len(hub) != 32 or len(unb32(tag)) != 20:
        raise ValueError("invalid envelope context")


def seal(key: bytes, hub: bytes, tag: str, plaintext: bytes, kind: int = 1) -> bytes:
    """Draw a fresh salt; retries must use the persisted returned envelope."""
    _context(key, hub, tag)
    if kind not in (1, 3):
        raise ValueError("not a symmetric kind")
    salt = os.urandom(32)
    ct = ChaCha20Poly1305(kdf(key, salt, "tandem/v2/msg")).encrypt(bytes(12), plaintext, aad(kind, hub, tag))
    return b"TNDM" + bytes((2, kind)) + salt + ct


def open_envelope(key: bytes, hub: bytes, tag: str, envelope: bytes) -> tuple[int, bytes]:
    _context(key, hub, tag)
    if len(envelope) < 54 or envelope[:5] != b"TNDM\x02" or envelope[5] not in (1, 3):
        raise ValueError("invalid v2 envelope")
    kind = envelope[5]
    return kind, ChaCha20Poly1305(kdf(key, envelope[6:38], "tandem/v2/msg")).decrypt(
        bytes(12), envelope[38:], aad(kind, hub, tag))


_KEM_SUITE = b"KEM\x00\x20"
_SUITE = b"HPKE\x00\x20\x00\x01\x00\x03"


def _extract(salt, suite, label, ikm):
    return hmac.digest(salt, b"HPKE-v1" + suite + label + ikm, "sha256")


def _expand(prk, suite, label, info, size):
    return HKDFExpand(hashes.SHA256(), size, struct.pack(">H", size) + b"HPKE-v1" + suite + label + info).derive(prk)


def _hpke_schedule(dh, enc, recipient, info):
    shared = _expand(_extract(b"", _KEM_SUITE, b"eae_prk", dh), _KEM_SUITE, b"shared_secret", enc + recipient, 32)
    ctx = b"\x00" + _extract(b"", _SUITE, b"psk_id_hash", b"") + _extract(b"", _SUITE, b"info_hash", info)
    secret = _extract(shared, _SUITE, b"secret", b"")
    return _expand(secret, _SUITE, b"key", ctx, 32), _expand(secret, _SUITE, b"base_nonce", ctx, 12)


def seal_hpke(recipient: bytes, info: str, authenticated: bytes, plaintext: bytes) -> bytes:
    ephemeral = x25519.X25519PrivateKey.generate()
    enc = public(ephemeral)
    key, nonce = _hpke_schedule(ephemeral.exchange(x25519.X25519PublicKey.from_public_bytes(recipient)), enc, recipient, info.encode("ascii"))
    return enc + ChaCha20Poly1305(key).encrypt(nonce, plaintext, authenticated)


def open_hpke(private: x25519.X25519PrivateKey, info: str, authenticated: bytes, sealed: bytes) -> bytes:
    if len(sealed) < 48:
        raise ValueError("short HPKE input")
    key, nonce = _hpke_schedule(private.exchange(x25519.X25519PublicKey.from_public_bytes(sealed[:32])), sealed[:32], public(private), info.encode("ascii"))
    return ChaCha20Poly1305(key).decrypt(nonce, sealed[32:], authenticated)


def seal_inbox(hub: bytes, uid: bytes, recipient: bytes, plaintext: bytes) -> bytes:
    tag = inbox_tag(uid, hub)
    _context(recipient, hub, tag)
    if len(uid) != 32:
        raise ValueError("participant ID must be 32 bytes")
    return b"TNDM\x02\x02" + key_id(hub, recipient) + seal_hpke(recipient, "tandem/v2/inbox", aad(2, hub, tag), plaintext)


def open_inbox(hub: bytes, uid: bytes, private: x25519.X25519PrivateKey, envelope: bytes) -> bytes:
    if (len(hub) != 32 or len(uid) != 32 or len(envelope) < 86 or envelope[:6] != b"TNDM\x02\x02"
            or envelope[6:38] != key_id(hub, public(private))):
        raise ValueError("not an inbox envelope for this device")
    return open_hpke(private, "tandem/v2/inbox", aad(2, hub, inbox_tag(uid, hub)), envelope[38:])
