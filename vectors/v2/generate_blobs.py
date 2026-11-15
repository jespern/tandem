"""Regenerate public blob vectors (DESIGN.md §17) with the Python implementation. No real secrets.

All four SDKs read the checked-in bytes; changes to this file are protocol changes.
"""
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk/python"))
import tandem as t
from tandem import blob


def material(label, n=32):
    return t.sha(("Tandem v2 public blob vector: " + label).encode())[:n]


def stream(label, n):
    out = b""
    i = 0
    while len(out) < n:
        out += material(f"{label} {i}")
        i += 1
    return out[:n]


def manifest_text(**fields):
    return json.dumps(fields, separators=(",", ":"))


def generate():
    hub, r = material("hub"), material("r")
    chunk_plain = stream("chunk", 300) + b"\x00\r\n.\r\nFrom =\t "
    key = blob.chunk_key(r, chunk_plain)
    envelope = blob.seal_chunk(key, hub, chunk_plain)
    chunk = dict(r=r.hex(), hub=hub.hex(), plaintext=chunk_plain.hex(), key=key.hex(), envelope=envelope.hex(),
                 address=blob.chunk_address(envelope).hex(), escaped=blob.escape(envelope).hex())

    # A blob of three chunks cut at 100 bytes (readers accept any length), an inline one and an empty file.
    content = stream("content", 250)
    multi, envelopes = blob.describe(r, hub, content, chunk_size=100, inline=0)
    inline, _ = blob.describe(r, hub, b"hello, Alice", inline=blob.DEFAULT_INLINE)
    empty, _ = blob.describe(r, hub, b"")
    blobs = [dict(name="three chunks", content=content.hex(), chunk_size=100, inline=0,
                  manifest=blob.encode_manifest(multi).decode(), envelopes=[e.hex() for e in envelopes]),
             dict(name="inline", content=b"hello, Alice".hex(), chunk_size=blob.DEFAULT_CHUNK, inline=blob.DEFAULT_INLINE,
                  manifest=blob.encode_manifest(inline).decode(), envelopes=[]),
             dict(name="empty file", content="", chunk_size=blob.DEFAULT_CHUNK, inline=blob.DEFAULT_INLINE,
                  manifest=blob.encode_manifest(empty).decode(), envelopes=[])]

    # Manifests every reader must classify the same way: "manifest", "unknown" (blob is an integer
    # other than 1: §17.7) or "none" (no manifest at all).
    h = t.b64u(t.sha(content))
    entry = lambda address, k, length: address + k + length.to_bytes(4, "big")
    one = t.b64u(entry(material("a1"), material("k1"), 7))
    mixed = t.b64u(entry(material("a1"), material("k1"), 3) + entry(material("a1"), material("k1"), 3)
                   + entry(material("a2"), material("k2"), blob.MAX_CHUNK))
    cases = [
        ("one chunk", manifest_text(blob=1, size=7, hash=h, chunks=one), "manifest"),
        ("unknown keys are ignored", manifest_text(blob=1, size=7, hash=h, chunks=one, name="photo.jpg"), "manifest"),
        ("mixed lengths, a repeated address, a full-size chunk", manifest_text(blob=1, size=6 + blob.MAX_CHUNK, hash=h, chunks=mixed), "manifest"),
        ("keys in another order", '{"chunks":"%s","hash":"%s","size":7,"blob":1}' % (one, h), "manifest"),
        ("empty inline", manifest_text(blob=1, size=0, hash=h, data=""), "manifest"),
        ("version 2", manifest_text(blob=2, size=7, hash=h, chunks=one), "unknown"),
        ("version 2 with nothing else", '{"blob":2}', "unknown"),
        ("version 0", manifest_text(blob=0), "unknown"),
        ("negative version", '{"blob":-1}', "unknown"),
        ("version as text", manifest_text(blob="1", size=7, hash=h, chunks=one), "none"),
        ("version missing", manifest_text(size=7, hash=h, chunks=one), "none"),
        ("version true", '{"blob":true,"size":7,"hash":"%s","chunks":"%s"}' % (h, one), "none"),
        ("version 1.0", '{"blob":1.0,"size":7,"hash":"%s","chunks":"%s"}' % (h, one), "none"),
        ("top level is an array", "[1]", "none"),
        ("not JSON", "blob", "none"),
        ("both chunks and data", manifest_text(blob=1, size=7, hash=h, chunks=one, data=t.b64u(b"1234567")), "none"),
        ("neither chunks nor data", manifest_text(blob=1, size=7, hash=h), "none"),
        ("no chunk entries", manifest_text(blob=1, size=0, hash=h, chunks=""), "none"),
        ("a chunk of length 0", manifest_text(blob=1, size=0, hash=h, chunks=t.b64u(entry(material("a"), material("k"), 0))), "none"),
        ("a chunk over 4 MiB", manifest_text(blob=1, size=blob.MAX_CHUNK + 1, hash=h,
                                              chunks=t.b64u(entry(material("a"), material("k"), blob.MAX_CHUNK + 1))), "none"),
        ("lengths do not add up to size", manifest_text(blob=1, size=8, hash=h, chunks=one), "none"),
        ("entries not a multiple of 68 bytes", manifest_text(blob=1, size=7, hash=h, chunks=t.b64u(entry(material("a"), material("k"), 7) + b"\x00")), "none"),
        ("inline data not of size", manifest_text(blob=1, size=4, hash=h, data=t.b64u(b"abc")), "none"),
        ("hash of 31 bytes", manifest_text(blob=1, size=7, hash=t.b64u(bytes(31)), chunks=one), "none"),
        ("hash with padding", manifest_text(blob=1, size=7, hash=h + "=", chunks=one), "none"),
        ("noncanonical base64url", manifest_text(blob=1, size=1, hash=h, data="QR"), "none"),
        ("standard base64 alphabet", manifest_text(blob=1, size=3, hash=h, data="+/+/"), "none"),
        ("negative size", manifest_text(blob=1, size=-7, hash=h, chunks=one), "none"),
        ("size as text", manifest_text(blob=1, size="7", hash=h, chunks=one), "none"),
        ("size with a fraction", '{"blob":1,"size":7.0,"hash":"%s","chunks":"%s"}' % (h, one), "none"),
        ("size beyond 2**53", '{"blob":1,"size":9007199254740993,"hash":"%s","chunks":"%s"}' % (h, one), "none"),
        ("repeated key", '{"blob":1,"size":7,"size":7,"hash":"%s","chunks":"%s"}' % (h, one), "none"),
        ("chunks not a string", manifest_text(blob=1, size=7, hash=h, chunks=[one]), "none"),
    ]
    manifests = [dict(name=n, value=v, result=r) for n, v, r in cases]
    for n, v, r in cases:
        got = blob.parse_manifest(v.encode())
        kind = "manifest" if isinstance(got, blob.Manifest) else "unknown" if isinstance(got, blob.UnknownManifest) else "none"
        assert kind == r, (n, kind)

    # The escaped form (§17.4.2). Every byte value as the only byte, at the start, in the middle
    # and at the end of a line; the line boundaries; and input where every byte is escaped.
    escapes = []
    for b in range(256):
        for shape in ([b], [b, 0x41], [0x41, b, 0x41], [0x41, b]):
            data = bytes(shape)
            escapes.append(dict(input=data.hex(), escaped=blob.escape(data).hex()))
    for n in (489, 490, 491, 980, 981):
        data = bytearray(stream(f"line {n}", n))
        for i in (0, 488, 489, 490, n - 1):
            if i < n:
                data[i] = 0x20
        escapes.append(dict(input=bytes(data).hex(), escaped=blob.escape(bytes(data)).hex()))
    for data in (b"", bytes(980), b"=" * 491):
        escapes.append(dict(input=data.hex(), escaped=blob.escape(data).hex()))
    # What a reader must accept that no writer makes, and what it must refuse.
    lenient = [dict(body=b"=A=\x80==".hex(), envelope=bytes([0x01, 0x40, 0xFD]).hex()),
               dict(body=b"ab\r\ncd\ref\ngh".hex(), envelope=b"abcdefgh".hex()),
               dict(body=b"=\r\nA".hex(), envelope=bytes([0x01]).hex()),
               dict(body=(b"x" * 5000).hex(), envelope=(b"x" * 5000).hex())]
    refused = [b"abc=".hex(), b"=".hex(), b"abc=\r\n".hex()]

    # Opening must fail for these; the first opens with the right length.
    bad = [dict(name="length differs from the entry", envelope=envelope.hex(), length=len(chunk_plain) - 1),
           dict(name="one byte changed", envelope=(envelope[:-1] + bytes([envelope[-1] ^ 1])).hex(), length=len(chunk_plain)),
           dict(name="kind 1", envelope=(b"TNDM\x02\x01" + envelope[6:]).hex(), length=len(chunk_plain)),
           dict(name="version 1", envelope=(b"TNDM\x01\x04" + envelope[6:]).hex(), length=len(chunk_plain)),
           dict(name="another hub", envelope=blob.seal_chunk(key, material("other hub"), chunk_plain).hex(), length=len(chunk_plain))]
    return dict(description="Public Tandem v2 blob vectors (DESIGN.md §17.4 and §17.5).",
                max_chunk=blob.MAX_CHUNK, chunk=chunk, blobs=blobs, manifests=manifests, escapes=escapes,
                unescape_lenient=lenient, unescape_refused=refused, open_fails=bad)


if __name__ == "__main__":
    data = json.dumps(generate(), ensure_ascii=False, indent=1) + "\n"
    path = Path(__file__).with_name("blobs.json")
    if "--check" in sys.argv:
        if path.read_text() != data:
            raise SystemExit("blob vectors have drifted")
    else:
        path.write_text(data)
