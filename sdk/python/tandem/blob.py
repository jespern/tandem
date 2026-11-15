"""Blobs (BLOBS.md): chunk encryption, manifests and the escaped body form. No I/O."""
from dataclasses import dataclass
import json
import re
import struct

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305

from .core import parse_json
from .crypto import b64u, kdf, sha, unb64u

KIND_CHUNK = 4
MAX_CHUNK = 4 * 1024 * 1024          # plaintext bytes in one chunk (BLOBS.md section 4.1)
DEFAULT_CHUNK = 1024 * 1024           # what a writer cuts files into
DEFAULT_INLINE = 16 * 1024            # content up to this size goes in the manifest itself
ENTRY = 68                            # address (32) || key (32) || u32(length)
LINE = 490                            # envelope bytes per line in the escaped form


def chunk_key(r: bytes, plaintext: bytes) -> bytes:
    """The writer's key for one chunk: never the same for two contents (section 4.1)."""
    if len(r) != 32:
        raise ValueError("r must be 32 bytes")
    return kdf(r, sha(plaintext), "tandem/v2/blob-chunk")


def _aad(hub: bytes) -> bytes:
    return b"TNDM\x02\x04" + hub


def seal_chunk(key: bytes, hub: bytes, plaintext: bytes) -> bytes:
    """The chunk's envelope. Deterministic: the same key and plaintext give the same bytes."""
    if len(key) != 32 or len(hub) != 32 or not 1 <= len(plaintext) <= MAX_CHUNK:
        raise ValueError("invalid chunk")
    return b"TNDM\x02\x04" + ChaCha20Poly1305(key).encrypt(bytes(12), plaintext, _aad(hub))


def open_chunk(key: bytes, hub: bytes, envelope: bytes, length: int) -> bytes:
    """The plaintext, which must be exactly `length` bytes (section 5)."""
    if len(key) != 32 or len(hub) != 32 or len(envelope) < 6 + 16 + 1 or envelope[:6] != b"TNDM\x02\x04":
        raise ValueError("not a chunk envelope")
    try:
        plaintext = ChaCha20Poly1305(key).decrypt(bytes(12), envelope[6:], _aad(hub))
    except InvalidTag:
        raise ValueError("chunk authentication failed") from None
    if len(plaintext) != length:
        raise ValueError("chunk length differs from its manifest entry")
    return plaintext


def chunk_address(envelope: bytes) -> bytes:
    return sha(envelope)


@dataclass(frozen=True)
class ChunkEntry:
    address: bytes
    key: bytes
    length: int


@dataclass(frozen=True)
class Manifest:
    size: int
    hash: bytes
    chunks: tuple[ChunkEntry, ...] | None = None
    data: bytes | None = None

    def offsets(self):
        """(offset, entry) for every chunk, in content order."""
        offset = 0
        for entry in self.chunks or ():
            yield offset, entry
            offset += entry.length


@dataclass(frozen=True)
class UnknownManifest:
    """A JSON object whose `blob` is an integer other than 1 (section 7)."""
    version: int


def encode_manifest(m: Manifest) -> bytes:
    """The canonical writer form: no spaces, keys in the order blob, size, hash, chunks or data."""
    if (m.chunks is None) == (m.data is None) or len(m.hash) != 32:
        raise ValueError("invalid manifest")
    body = dict(blob=1, size=m.size, hash=b64u(m.hash))
    if m.chunks is not None:
        body["chunks"] = b64u(b"".join(e.address + e.key + struct.pack(">I", e.length) for e in m.chunks))
    else:
        body["data"] = b64u(m.data)
    encoded = json.dumps(body, separators=(",", ":")).encode()
    if not isinstance(parse_manifest(encoded), Manifest):
        raise ValueError("invalid manifest")
    return encoded


def parse_manifest(value: bytes):
    """A Manifest, an UnknownManifest, or None for a value that is no manifest (section 5)."""
    try:
        doc = parse_json(value)
    except (ValueError, UnicodeDecodeError):
        return None
    if not isinstance(doc, dict) or type(doc.get("blob")) is not int:
        return None
    if doc["blob"] != 1:
        return UnknownManifest(doc["blob"])
    size, digest = doc.get("size"), doc.get("hash")
    if type(size) is not int or not 0 <= size <= 2**53 or not isinstance(digest, str):
        return None
    if ("chunks" in doc) == ("data" in doc):
        return None
    try:
        digest = unb64u(digest)
        raw = unb64u(doc["chunks"] if "chunks" in doc else doc["data"])
    except (ValueError, TypeError):
        return None
    if len(digest) != 32:
        return None
    if "data" in doc:
        return Manifest(size, digest, data=raw) if len(raw) == size else None
    if not raw or len(raw) % ENTRY:
        return None
    entries = []
    for i in range(0, len(raw), ENTRY):
        length = struct.unpack(">I", raw[i + 64:i + 68])[0]
        if not 1 <= length <= MAX_CHUNK:
            return None
        entries.append(ChunkEntry(raw[i:i + 32], raw[i + 32:i + 64], length))
    if sum(e.length for e in entries) != size:
        return None
    return Manifest(size, digest, chunks=tuple(entries))


def describe(r: bytes, hub: bytes, content: bytes, chunk_size: int = DEFAULT_CHUNK, inline: int = DEFAULT_INLINE):
    """The manifest of `content` and its envelopes in order (section 6, step 2), all in memory."""
    if not 1 <= chunk_size <= MAX_CHUNK:
        raise ValueError("invalid chunk size")
    if len(content) <= inline:
        return Manifest(len(content), sha(content), data=content), []
    entries, envelopes = [], []
    for i in range(0, len(content), chunk_size):
        piece = content[i:i + chunk_size]
        key = chunk_key(r, piece)
        envelope = seal_chunk(key, hub, piece)
        entries.append(ChunkEntry(chunk_address(envelope), key, len(piece)))
        envelopes.append(envelope)
    return Manifest(len(content), sha(content), chunks=tuple(entries)), envelopes


_ANYWHERE = re.compile(rb"[\x00\x0a\x0d=]")
_START = frozenset(b"\x00\x0a\x0d=\t .>F")
_END = frozenset(b"\x00\x0a\x0d=\t ")


def _escaped(b: int) -> bytes:
    return bytes((0x3D, (b + 64) & 255))


def escape(envelope: bytes) -> bytes:
    """The escaped body form of section 4.2: lines of at most 490 envelope bytes, CRLF after each."""
    out = []
    for i in range(0, len(envelope), LINE):
        line = envelope[i:i + LINE]
        first = _escaped(line[0]) if line[0] in _START else line[:1]
        if len(line) == 1:
            out.append(first)
            continue
        middle = _ANYWHERE.sub(lambda m: _escaped(m[0][0]), line[1:-1])
        last = _escaped(line[-1]) if line[-1] in _END else line[-1:]
        out.append(first + middle + last)
    return b"".join(x + b"\r\n" for x in out)


def unescape(body: bytes) -> bytes:
    """Remove every CR and LF, then each '=' and the byte after it become that byte minus 64."""
    flat = bytes(body).replace(b"\r", b"").replace(b"\n", b"")
    out, i = bytearray(), 0
    while True:
        j = flat.find(b"=", i)
        if j < 0:
            out += flat[i:]
            return bytes(out)
        if j + 1 >= len(flat):
            raise ValueError("escaped body ends in '='")
        out += flat[i:j]
        out.append((flat[j + 1] - 64) & 255)
        i = j + 2
