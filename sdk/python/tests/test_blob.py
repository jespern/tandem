import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from tandem import blob  # noqa: E402

VECTORS = json.loads((Path(__file__).resolve().parents[3] / "vectors/v2/blobs.json").read_text())


def kind(parsed):
    return "manifest" if isinstance(parsed, blob.Manifest) else "unknown" if isinstance(parsed, blob.UnknownManifest) else "none"


class BlobVectors(unittest.TestCase):
    def test_chunk(self):
        c = {k: bytes.fromhex(v) for k, v in VECTORS["chunk"].items()}
        self.assertEqual(blob.chunk_key(c["r"], c["plaintext"]), c["key"])
        self.assertEqual(blob.seal_chunk(c["key"], c["hub"], c["plaintext"]), c["envelope"])
        self.assertEqual(blob.chunk_address(c["envelope"]), c["address"])
        self.assertEqual(blob.escape(c["envelope"]), c["escaped"])
        self.assertEqual(blob.open_chunk(c["key"], c["hub"], c["envelope"], len(c["plaintext"])), c["plaintext"])

    def test_open_refuses(self):
        c = {k: bytes.fromhex(v) for k, v in VECTORS["chunk"].items()}
        for case in VECTORS["open_fails"]:
            with self.subTest(case["name"]), self.assertRaises(ValueError):
                blob.open_chunk(c["key"], c["hub"], bytes.fromhex(case["envelope"]), case["length"])

    def test_describe(self):
        c = {k: bytes.fromhex(v) for k, v in VECTORS["chunk"].items()}
        for case in VECTORS["blobs"]:
            with self.subTest(case["name"]):
                m, envelopes = blob.describe(c["r"], c["hub"], bytes.fromhex(case["content"]), case["chunk_size"], case["inline"])
                self.assertEqual(blob.encode_manifest(m).decode(), case["manifest"])
                self.assertEqual([e.hex() for e in envelopes], case["envelopes"])
                parsed = blob.parse_manifest(case["manifest"].encode())
                self.assertEqual(parsed, m)
                content = b"".join(blob.open_chunk(e.key, c["hub"], envelopes[i], e.length) for i, e in enumerate(parsed.chunks or ()))
                self.assertEqual(content if parsed.chunks else parsed.data, bytes.fromhex(case["content"]))

    def test_manifests(self):
        for case in VECTORS["manifests"]:
            with self.subTest(case["name"]):
                self.assertEqual(kind(blob.parse_manifest(case["value"].encode())), case["result"])

    def test_escapes(self):
        for case in VECTORS["escapes"]:
            data, escaped = bytes.fromhex(case["input"]), bytes.fromhex(case["escaped"])
            self.assertEqual(blob.escape(data), escaped)
            self.assertEqual(blob.unescape(escaped), data)
        for case in VECTORS["unescape_lenient"]:
            self.assertEqual(blob.unescape(bytes.fromhex(case["body"])), bytes.fromhex(case["envelope"]))
        for body in VECTORS["unescape_refused"]:
            with self.assertRaises(ValueError):
                blob.unescape(bytes.fromhex(body))

    def test_seal_limits(self):
        key, hub = bytes(32), bytes(32)
        self.assertEqual(len(blob.seal_chunk(key, hub, bytes(blob.MAX_CHUNK))), 6 + blob.MAX_CHUNK + 16)
        for bad in (b"", bytes(blob.MAX_CHUNK + 1)):
            with self.assertRaises(ValueError):
                blob.seal_chunk(key, hub, bad)


if __name__ == "__main__":
    unittest.main()
