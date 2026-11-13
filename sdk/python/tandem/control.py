"""Control objects (§10.8): one schema, one encoder, one strict JSON reader.

Every object is a tag followed by typed fields; its ID is the hash of that input and its
signatures cover the same bytes. `SCHEMA` is the table of §10.8, and `vectors/v2/control.json`
carries a copy that every SDK checks its own against.
"""
import json
import os

from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305

from .core import parse_json
from .crypto import b32, b64u, kdf, open_hpke, public, seal_hpke, sha, unb64u
from .signature import verify_strict

MAX_TEXT = 256
MAX_SET = 65536
MAX_JSON_INTEGER = 2**53
MAX_MAILBOXES = 64


def _f(name, kind):
    return (name, kind)


# name: (tag, fields, signatures, extras). A signature name ending in "?" may be absent.
SCHEMA = {
    "genesis": ("TANDEM-GENESIS-V2", [_f("hub", "id"), _f("agree", "id"), _f("hubkey", "id"),
                                      _f("creator", "id"),
                                      _f("mailboxes", "mailboxes")], ["sig"], []),
    "capsule": ("TANDEM-CAPSULE-V2", [_f("hub", "id"), _f("genesis", "id"), _f("sealed", "id"),
                                      _f("supersedes", "set")], ["sig"], [_f("capsule", "bytes")]),
    "snapshot": ("tandem/v2/auth-snapshot", [_f("hub", "id"), _f("genesis", "id"), _f("participant", "id"),
                                             _f("certs", "set"), _f("revocations", "set")], [], []),
    "registration": ("TANDEM-REGISTRATION-V2", [_f("hub", "id"), _f("sign", "id"), _f("agree", "id"),
                                                _f("enrollment", "id16")], [], []),
    "request": ("TANDEM-REQUEST-V2", [_f("hub", "id"), _f("genesis", "id"), _f("participant", "id"),
                                      _f("sign", "id"), _f("agree", "id"), _f("label", "text"), _f("how", "u8"),
                                      _f("authority", "opt"), _f("enrollment", "id16")], ["self", "approver?"], []),
    "certificate": ("TANDEM-CERT-V2", [_f("hub", "id"), _f("genesis", "id"), _f("request", "id"),
                                       _f("issued", "set")], ["sig"], [_f("body", "request")]),
    "revocation": ("TANDEM-REVOKE-V2", [_f("hub", "id"), _f("genesis", "id"), _f("target", "id"),
                                        _f("retained", "set")], ["sig"], []),
    "invitation": ("TANDEM-INVITE-V2", [_f("hub", "id"), _f("genesis", "id"), _f("key", "id"),
                                        _f("invite", "id16"), _f("expires", "u64"), _f("label", "text")], ["sig"], []),
    "delegation": ("TANDEM-DELEGATION-V2", [_f("hub", "id"), _f("genesis", "id"), _f("participant", "id"),
                                            _f("source", "id"), _f("key", "id"), _f("token", "id16"),
                                            _f("expires", "u64"), _f("package", "id"), _f("gate", "id")], ["sig"], []),
    "receipt": ("TANDEM-RECEIPT-V2", [_f("hub", "id"), _f("genesis", "id"), _f("outcome", "u8"),
                                      _f("subject", "id"), _f("signer", "opt")], ["sig"], []),
    "home-epoch": ("TANDEM-HOME-EPOCH-V2", [_f("hub", "id"), _f("genesis", "id"), _f("participant", "id"),
                                            _f("n", "u32"), _f("parents", "set"), _f("key", "id"),
                                            _f("recipients", "set"), _f("snapshot", "id"), _f("signer", "id"),
                                            _f("auth", "id")], ["sig"], []),
    "grant": ("TANDEM-GRANT-V2", [_f("hub", "id"), _f("collection", "id16"), _f("recipient", "id"),
                                  _f("admission", "id"), _f("at", "id"), _f("epoch", "id"), _f("key", "id"),
                                  _f("signer", "id"), _f("auth", "id")], ["sig"], []),
    "key-request": ("TANDEM-KEYREQ-V2", [_f("hub", "id"), _f("collection", "id16"), _f("participant", "id"),
                                         _f("have", "id"), _f("signer", "id"), _f("auth", "id")], ["sig"], []),
    "leave": ("TANDEM-LEAVE-V2", [_f("hub", "id"), _f("collection", "id16"), _f("participant", "id"),
                                  _f("at", "id"), _f("signer", "id"), _f("auth", "id")], ["sig"], []),
    "answer": ("TANDEM-ANSWER-V2", [_f("hub", "id"), _f("collection", "id16"), _f("participant", "id"),
                                    _f("admission", "id"), _f("answer", "answer"), _f("event", "id16"),
                                    _f("signer", "id"), _f("auth", "id")], ["sig"], []),
}

# How a request's keys came in (§10.8).
FIRST, INVITED, BY_DEVICE, BY_PAIRING, CREATOR_RECOVERY = 1, 2, 3, 4, 5
# Receipt outcomes.
REJECTED, PAIRING_CANCELLED, INVITE_CANCELLED, PAIRED = 1, 2, 3, 4
ANSWERS = (b"a", b"n", b"l")


def schema_table():
    """The schema in the plain form `vectors/v2/control.json` carries."""
    return {name: {"tag": tag, "fields": [list(f) for f in fields], "signatures": sigs,
                   "extras": [list(f) for f in extras]}
            for name, (tag, fields, sigs, extras) in SCHEMA.items()}


class ControlError(ValueError):
    pass


def _need(cond, what):
    if not cond:
        raise ControlError(what)


def _mailbox_bytes(m):
    host, port, user = m["host"], m["port"], m["user"]
    _need(isinstance(host, str) and host.isascii() and host == host.lower(), "mailbox host must be lowercase ASCII")
    _need(isinstance(port, int) and not isinstance(port, bool) and 0 < port < 65536, "mailbox port")
    return _text(host) + port.to_bytes(2, "big") + _text(user)


def _text(value):
    _need(isinstance(value, str), "text")
    raw = value.encode("utf-8")
    _need(len(raw) <= MAX_TEXT, "text longer than 256 bytes")
    return len(raw).to_bytes(4, "big") + raw


def _field_bytes(kind, value):
    if kind == "id":
        _need(isinstance(value, bytes) and len(value) == 32, "id must be 32 bytes")
        return value
    if kind == "id16":
        _need(isinstance(value, bytes) and len(value) == 16, "id16 must be 16 bytes")
        return value
    if kind == "opt":
        if value is None:
            return bytes(32)
        _need(isinstance(value, bytes) and len(value) == 32 and value != bytes(32), "optional id")
        return value
    if kind == "text":
        return _text(value)
    if kind == "set":
        _need(isinstance(value, (list, tuple)) and len(value) <= MAX_SET, "set")
        _need(all(isinstance(x, bytes) and len(x) == 32 for x in value), "set members are 32-byte IDs")
        _need(all(a < b for a, b in zip(value, value[1:])), "set must be sorted and unique")
        return len(value).to_bytes(4, "big") + b"".join(value)
    if kind in ("u8", "u16", "u32", "u64"):
        width = {"u8": 1, "u16": 2, "u32": 4, "u64": 8}[kind]
        _need(isinstance(value, int) and not isinstance(value, bool), kind)
        _need(0 <= value < min(256 ** width, MAX_JSON_INTEGER + 1), kind + " out of range")
        return value.to_bytes(width, "big")
    if kind == "answer":
        _need(value in ANSWERS, "answer must be a, n or l")
        return value
    if kind == "mailboxes":
        _need(isinstance(value, (list, tuple)) and len(value) <= MAX_MAILBOXES, "mailboxes")
        encoded = [_mailbox_bytes(m) for m in value]
        _need(all(a < b for a, b in zip(encoded, encoded[1:])), "mailboxes must be sorted and unique")
        return len(encoded).to_bytes(4, "big") + b"".join(encoded)
    raise ControlError("unknown field type " + kind)


def signed_input(kind, fields):
    """The exact bytes an object's ID hashes and its signatures cover."""
    tag, layout, _, _ = SCHEMA[kind]
    _need(set(fields) >= {name for name, _ in layout}, "missing field")
    return tag.encode("ascii") + b"".join(_field_bytes(t, fields[name]) for name, t in layout)


def object_id(kind, fields):
    return sha(signed_input(kind, fields))


def sign(kind, fields, private: ed25519.Ed25519PrivateKey):
    return private.sign(signed_input(kind, fields))


def verify(kind, fields, signer_public: bytes, signature: bytes) -> bool:
    return verify_strict(signer_public, signature, signed_input(kind, fields))


def _field_json(kind, value):
    if kind in ("id", "id16"):
        return b64u(value)
    if kind == "opt":
        return None if value is None else b64u(value)
    if kind == "set":
        return [b64u(x) for x in value]
    if kind == "answer":
        return value.decode("ascii")
    if kind == "mailboxes":
        return [{"host": m["host"], "port": m["port"], "user": m["user"]} for m in value]
    return value


def to_json(kind, fields, signatures=None, extras=None) -> bytes:
    """Strict JSON (§10.5) for one object; signatures and extras are by name."""
    _, layout, sig_names, extra_layout = SCHEMA[kind]
    signed_input(kind, fields)  # validate before serializing
    out = {"type": kind}
    for name, t in layout:
        out[name] = _field_json(t, fields[name])
    for raw in sig_names:
        name = raw.rstrip("?")
        value = (signatures or {}).get(name)
        _need(value is not None or raw.endswith("?"), "missing signature " + name)
        if value is not None:
            out[name] = b64u(value)
    for name, t in extra_layout:
        value = (extras or {}).get(name)
        if value is None:
            continue
        out[name] = b64u(value) if t == "bytes" else json.loads(to_json(t, *value))
    return json.dumps(out, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def _from_json_value(kind, value):
    if kind in ("id", "id16"):
        _need(isinstance(value, str), kind)
        raw = unb64u(value)
        _need(len(raw) == (32 if kind == "id" else 16), kind + " length")
        return raw
    if kind == "opt":
        return None if value is None else _from_json_value("id", value)
    if kind == "set":
        _need(isinstance(value, list), "set")
        return [_from_json_value("id", x) for x in value]
    if kind == "answer":
        _need(isinstance(value, str), "answer")
        return value.encode("ascii")
    if kind == "mailboxes":
        _need(isinstance(value, list) and all(isinstance(m, dict) for m in value), "mailboxes")
        return [{"host": m.get("host"), "port": m.get("port"), "user": m.get("user")} for m in value]
    return value


def parse_object(value, expect=None):
    """Read one parsed JSON object. Returns (kind, fields, signatures, extras); validates every
    field and recomputes nothing it trusts from the message."""
    _need(isinstance(value, dict), "object")
    kind = value.get("type")
    _need(isinstance(kind, str) and kind in SCHEMA, "unknown control type")
    _need(expect is None or kind == expect, "unexpected control type")
    _, layout, sig_names, extra_layout = SCHEMA[kind]
    fields = {}
    for name, t in layout:
        _need(name in value, "missing " + name)
        fields[name] = _from_json_value(t, value[name])
    signed_input(kind, fields)
    signatures = {}
    for raw in sig_names:
        name = raw.rstrip("?")
        if name in value and value[name] is not None:
            sig = unb64u(value[name])
            _need(len(sig) == 64, "signature length")
            signatures[name] = sig
        else:
            _need(raw.endswith("?"), "missing signature " + name)
    extras = {}
    for name, t in extra_layout:
        if name in value:
            _need(t != "bytes" or isinstance(value[name], str), name)
            extras[name] = unb64u(value[name]) if t == "bytes" else parse_object(value[name], t)
    if kind == "capsule" and "capsule" in extras:
        _need(sha(extras["capsule"]) == fields["sealed"], "capsule bytes do not hash to sealed")
    if kind == "certificate" and "body" in extras:
        _need(object_id("request", extras["body"][1]) == fields["request"], "body is not the named request")
    return kind, fields, signatures, extras


def from_json(data: bytes, expect=None):
    return parse_object(parse_json(data), expect)


# ---- Derivations (§10.8) ----

def invite_key(secret: bytes, hub: bytes, invite: bytes) -> ed25519.Ed25519PrivateKey:
    return ed25519.Ed25519PrivateKey.from_private_bytes(kdf(secret, hub + invite, "tandem/v2/invite-key"))


def pairing_invite_key(link: bytes, hub: bytes, token: bytes) -> ed25519.Ed25519PrivateKey:
    return ed25519.Ed25519PrivateKey.from_private_bytes(kdf(link, hub + token, "tandem/v2/pair-invite"))


def pairing_key(link: bytes, hub: bytes, token: bytes) -> bytes:
    return kdf(link, hub + token, "tandem/v2/pair-key")


def pairing_tag(link: bytes, hub: bytes, token: bytes) -> str:
    return b32(kdf(link, hub + token, "tandem/v2/pair-tag", 20))


def directory_scope(hub: bytes) -> bytes:
    return sha(b"tandem/v2/directory", hub)[:16]


def registration_id(hub, sign_public, agree_public, enrollment) -> bytes:
    return object_id("registration", dict(hub=hub, sign=sign_public, agree=agree_public, enrollment=enrollment))


def _package_aead(link, gate_secret, token):
    return ChaCha20Poly1305(kdf(link + gate_secret, token, "tandem/v2/pair-package"))


def seal_package(link, gate_secret, hub, token, plaintext, nonce=None) -> bytes:
    nonce = nonce or os.urandom(12)
    aad = b"TANDEM-PAIR-PACKAGE-V2" + hub + token
    return nonce + _package_aead(link, gate_secret, token).encrypt(nonce, plaintext, aad)


def open_package(link, gate_secret, hub, token, package) -> bytes:
    _need(len(package) > 12, "package")
    aad = b"TANDEM-PAIR-PACKAGE-V2" + hub + token
    return _package_aead(link, gate_secret, token).decrypt(package[:12], package[12:], aad)


def seal_gate(manager_agree_public, hub, token, participant, gate_secret) -> bytes:
    return seal_hpke(manager_agree_public, "tandem/v2/pair-gate", b"TANDEM-PAIR-GATE-V2" + hub + token + participant,
                     gate_secret)


def open_gate(manager_agree: x25519.X25519PrivateKey, hub, token, participant, gate) -> bytes:
    return open_hpke(manager_agree, "tandem/v2/pair-gate", b"TANDEM-PAIR-GATE-V2" + hub + token + participant, gate)


def seal_home_recovery(manager_agree_public, hub, epoch, home_key) -> bytes:
    return seal_hpke(manager_agree_public, "tandem/v2/home-recovery", b"TANDEM-HOME-RECOVERY-V2" + hub + epoch, home_key)


def open_home_recovery(manager_agree: x25519.X25519PrivateKey, hub, epoch, box) -> bytes:
    return open_hpke(manager_agree, "tandem/v2/home-recovery", b"TANDEM-HOME-RECOVERY-V2" + hub + epoch, box)


def public_key(private) -> bytes:
    return public(private)
