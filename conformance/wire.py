"""Executable v1 wire reference. Test code, not a production SDK.

HPKE composes cryptography's X25519, HKDF and ChaCha20Poly1305 because its
single-shot HPKE API does not expose the AEAD AAD required by Tandem.
All secret inputs in the checked-in tests are public test material.
"""
import base64
import hashlib
import hmac
import json
import os
import struct

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.hkdf import HKDF, HKDFExpand


def sha(data):
    return hashlib.sha256(data).digest()


def kdf(key, salt, info, size=32):
    return HKDF(hashes.SHA256(), size, salt, info).derive(key)


def pub(key):
    return key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)


def b64(data):
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def unb64(text):
    raw = base64.b64decode(text + "=" * (-len(text) % 4), altchars=b"-_", validate=True)
    if b64(raw) != text:
        raise ValueError("noncanonical base64url")
    return raw


def dumps(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def identity(secret):
    if len(secret) != 32:
        raise ValueError("user secret length")
    sign = kdf(secret, b"", b"tandem/v1/user-sign")
    agree = kdf(secret, b"", b"tandem/v1/user-agree")
    return dict(sign=sign, agree=agree, uid=pub(ed25519.Ed25519PrivateKey.from_private_bytes(sign)),
                uka=pub(x25519.X25519PrivateKey.from_private_bytes(agree)),
                home=kdf(secret, b"", b"tandem/v1/home"))


def commitment(key):
    return sha(b"tandem/v1/commit" + key)


def tag(key, epoch=None, inbox=False):
    info = b"tandem/v1/inbox-tag" if inbox else b"tandem/v1/tag"
    if epoch is not None:
        info = b"tandem/v1/collection-tag"
    return base64.b32encode(kdf(key, epoch or b"", info, 20)).rstrip(b"=").lower().decode()


def aad(kind, channel):
    return b"TNDM" + bytes((1, kind)) + channel.encode("ascii")


def seal(key, channel, plaintext, kind=1, salt=None):
    if kind not in (1, 3):
        raise ValueError("symmetric kind")
    salt = os.urandom(32) if salt is None else salt
    if len(salt) != 32:
        raise ValueError("salt length")
    ct = ChaCha20Poly1305(kdf(key, salt, b"tandem/v1/msg")).encrypt(bytes(12), plaintext, aad(kind, channel))
    return b"TNDM" + bytes((1, kind)) + salt + ct


def open_envelope(key, channel, body):
    if len(body) < 54 or body[:5] != b"TNDM\x01" or body[5] not in (1, 3):
        raise ValueError("invalid envelope")
    return ChaCha20Poly1305(kdf(key, body[6:38], b"tandem/v1/msg")).decrypt(
        bytes(12), body[38:], aad(body[5], channel))


# RFC 9180, sections 4 and 5; suite 0x0020 / 0x0001 / 0x0003, base mode.
KEM_SUITE = b"KEM\x00\x20"
SUITE = b"HPKE\x00\x20\x00\x01\x00\x03"


def extract(salt, suite, label, ikm):
    return hmac.digest(salt, b"HPKE-v1" + suite + label + ikm, "sha256")


def expand(prk, suite, label, info, size):
    labeled = struct.pack(">H", size) + b"HPKE-v1" + suite + label + info
    return HKDFExpand(hashes.SHA256(), size, labeled).derive(prk)


def hpke_schedule(dh, enc, recipient, info):
    eae = extract(b"", KEM_SUITE, b"eae_prk", dh)
    shared = expand(eae, KEM_SUITE, b"shared_secret", enc + recipient, 32)
    context = b"\x00" + extract(b"", SUITE, b"psk_id_hash", b"") + extract(b"", SUITE, b"info_hash", info)
    secret = extract(shared, SUITE, b"secret", b"")
    return (expand(secret, SUITE, b"key", context, 32),
            expand(secret, SUITE, b"base_nonce", context, 12))


def hpke_seal(recipient, plaintext, info, authenticated, ephemeral=None):
    key = x25519.X25519PrivateKey.generate() if ephemeral is None else x25519.X25519PrivateKey.from_private_bytes(ephemeral)
    enc = pub(key)
    mk, nonce = hpke_schedule(key.exchange(x25519.X25519PublicKey.from_public_bytes(recipient)), enc, recipient, info)
    return enc + ChaCha20Poly1305(mk).encrypt(nonce, plaintext, authenticated)


def hpke_open(private, sealed, info, authenticated):
    key = x25519.X25519PrivateKey.from_private_bytes(private)
    enc = sealed[:32]
    mk, nonce = hpke_schedule(key.exchange(x25519.X25519PublicKey.from_public_bytes(enc)), enc, pub(key), info)
    return ChaCha20Poly1305(mk).decrypt(nonce, sealed[32:], authenticated)


def inbox_seal(recipient_uid, recipient_uka, plaintext):
    channel = tag(recipient_uid, inbox=True)
    kid = sha(b"tandem/v1/kid" + recipient_uka)
    return b"TNDM\x01\x02" + kid + hpke_seal(recipient_uka, plaintext, b"tandem/v1/inbox", aad(2, channel))


def inbox_open(who, body):
    if body[:6] != b"TNDM\x01\x02" or body[6:38] != sha(b"tandem/v1/kid" + who["uka"]):
        raise ValueError("wrong envelope or recipient")
    return hpke_open(who["agree"], body[38:], b"tandem/v1/inbox", aad(2, tag(who["uid"], inbox=True)))


def lp(data):
    return struct.pack(">I", len(data)) + data


def vector(entries):
    if len(entries) > 1024 or len({d for d, _, _ in entries}) != len(entries):
        raise ValueError("invalid vector")
    out = struct.pack(">H", len(entries))
    for dev, millis, counter in sorted(entries):
        if len(dev) != 8 or not 0 <= millis < 2**48 or not 0 <= counter < 2**16:
            raise ValueError("invalid vector entry")
        out += dev + struct.pack(">QH", millis, counter)
    return out


def write_input(scope, rec, field, op, value, author, dev, millis, counter, ref, context=(), base=(), element=b""):
    if (len(scope), len(rec), len(author), len(dev), len(ref)) != (16, 16, 32, 8, 32):
        raise ValueError("invalid identifier length")
    if op not in (1, 2, 3, 4) or (op != 1 and value) or (op not in (2, 3) and element):
        raise ValueError("invalid operation")
    if not 0 <= millis < 2**48 or not 0 <= counter < 2**16:
        raise ValueError("invalid clock")
    return (b"TANDEM-WRITE-V1" + scope + rec + lp(field.encode("utf-8")) + bytes((op,)) + lp(element)
            + sha(value) + author + dev + struct.pack(">QH", millis, counter) + ref + vector(context) + vector(base))


def named(name):
    return sha(b"tandem/v1/named" + name.encode("utf-8"))[:16]


def signed_write(who, scope, rec, field, value, ref, millis=1, op=1, element=b"", context=(), base=()):
    dev = b"device01"
    data = write_input(scope, rec, field, op, value, who["uid"], dev, millis, 0, ref, context, base, element)
    w = dict(f=field, op=op, a=b64(who["uid"]), d=[b64(dev), millis, 0], r=b64(ref),
             c=[[b64(d), m, c] for d, m, c in context], b=[[b64(d), m, c] for d, m, c in base],
             s=b64(ed25519.Ed25519PrivateKey.from_private_bytes(who["sign"]).sign(data)))
    if op == 1:
        w["v"] = b64(value)
    if op in (2, 3):
        w["e"] = b64(element)
    return w, sha(data)


def input_from_write(scope, rec, w):
    return write_input(scope, rec, w["f"], w["op"], unb64(w.get("v", "")), unb64(w["a"]),
                       unb64(w["d"][0]), *w["d"][1:], unb64(w["r"]),
                       [(unb64(d), m, c) for d, m, c in w["c"]],
                       [(unb64(d), m, c) for d, m, c in w["b"]], unb64(w.get("e", "")))


# Public-input arithmetic gives a strict verification oracle, independently of
# the native libraries' differing Ed25519 acceptance policies. Not signing code.
P = 2**255 - 19
L = 2**252 + 27742317777372353535851937790883648493
D = -121665 * pow(121666, -1, P) % P
IDENTITY = (0, 1)


def point(raw):
    if len(raw) != 32:
        raise ValueError("point length")
    n = int.from_bytes(raw, "little")
    y, sign = n & (2**255 - 1), n >> 255
    if y >= P:
        raise ValueError("noncanonical point")
    x2 = (y*y - 1) * pow(D*y*y + 1, -1, P) % P
    x = pow(x2, (P+3)//8, P)
    if x*x % P != x2:
        x = x * pow(2, (P-1)//4, P) % P
    if x*x % P != x2 or (x == 0 and sign):
        raise ValueError("invalid point")
    return (x if x & 1 == sign else P-x) % P, y


def add(a, b):
    x, y = a
    u, v = b
    q = D*x*u*y*v % P
    return ((x*v+y*u)*pow(1+q, -1, P) % P, (y*v+x*u)*pow(1-q, -1, P) % P)


def mul(a, n):
    result = IDENTITY
    while n:
        if n & 1:
            result = add(result, a)
        a, n = add(a, a), n >> 1
    return result


BASE = point(bytes.fromhex("5866666666666666666666666666666666666666666666666666666666666666"))


def strict_verify(public, signature, message):
    try:
        if len(signature) != 64 or int.from_bytes(signature[32:], "little") >= L:
            return False
        a, r = point(public), point(signature[:32])
        if mul(a, 8) == IDENTITY:
            return False
        k = int.from_bytes(hashlib.sha512(signature[:32] + public + message).digest(), "little") % L
        return mul(BASE, int.from_bytes(signature[32:], "little")) == add(r, mul(a, k))
    except (ValueError, ZeroDivisionError):
        return False


def verify_write(scope, rec, w):
    data = input_from_write(scope, rec, w)
    if not strict_verify(unb64(w["a"]), unb64(w["s"]), data):
        raise InvalidSignature
    return sha(data)
