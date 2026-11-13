import json
from pathlib import Path
import unittest

from cryptography.hazmat.primitives.asymmetric import x25519
import tandem as t
import tandem.control as c

VECTORS = json.loads((Path(__file__).resolve().parents[3] / "vectors/v2/control.json").read_text())


class ControlVectors(unittest.TestCase):
    def test_schema_matches_the_vectors(self):
        self.assertEqual(c.schema_table(), VECTORS["schema"])

    def test_objects(self):
        for row in VECTORS["objects"]:
            with self.subTest(row["name"]):
                kind, fields, sigs, extras = c.from_json(row["json"].encode())
                self.assertEqual(kind, row["kind"])
                self.assertEqual(c.signed_input(kind, fields).hex(), row["input"])
                self.assertEqual(c.object_id(kind, fields).hex(), row["id"])
                self.assertEqual(set(sigs), set(row["signers"]))
                for name, key in row["signers"].items():
                    self.assertTrue(c.verify(kind, fields, bytes.fromhex(key), sigs[name]))
                    tampered = bytearray(sigs[name]); tampered[0] ^= 1
                    self.assertFalse(c.verify(kind, fields, bytes.fromhex(key), bytes(tampered)))
                plain_extras = {k: (v if isinstance(v, bytes) else (v[1], v[2])) for k, v in extras.items()}
                self.assertEqual(c.to_json(kind, fields, sigs, plain_extras).decode(), row["json"])

    def test_rejects(self):
        for row in VECTORS["rejects"]:
            with self.subTest(row["why"]):
                with self.assertRaises(ValueError):
                    c.from_json(row["json"].encode())

    def test_derivations_and_boxes(self):
        m = {k: bytes.fromhex(v) for k, v in VECTORS["material"].items()}
        d = {k: (bytes.fromhex(v) if k not in ("pairing_tag", "wake_tag") else v) for k, v in VECTORS["derived"].items()}
        mks, mka = t.manager_keys(m["management_seed"])
        hub = t.public(mks)
        self.assertEqual(t.public(c.invite_key(m["invite_secret"], hub, m["invite"])), d["invite_key"])
        self.assertEqual(t.public(c.pairing_invite_key(m["link"], hub, m["token"])), d["pairing_invite_key"])
        self.assertEqual(c.pairing_key(m["link"], hub, m["token"]), d["pairing_key"])
        self.assertEqual(c.pairing_tag(m["link"], hub, m["token"]), d["pairing_tag"])
        self.assertEqual(c.directory_scope(hub), d["directory_scope"])
        self.assertEqual(c.wake_tag(m["directory_key"], hub), d["wake_tag"])
        self.assertEqual(t.public(mka), d["manager_agreement"])
        self.assertEqual(c.open_package(m["link"], m["gate_secret"], hub, m["token"], d["package"]),
                         d["package_plaintext"])
        alice = next(bytes.fromhex(o["id"]) for o in VECTORS["objects"] if o["name"] == "alice registration")
        self.assertEqual(c.open_gate(mka, hub, m["token"], alice, d["gate"]), m["gate_secret"])
        self.assertEqual(c.open_home_recovery(mka, hub, m["home_epoch"], d["home_recovery"]), m["creator_home_key"])
        with self.assertRaises(Exception):
            c.open_package(m["link"], bytes(32), hub, m["token"], d["package"])

    def test_fresh_boxes_round_trip(self):
        agree = x25519.X25519PrivateKey.generate()
        hub, token, participant = bytes(32), bytes(16), bytes(range(32))
        gate = c.seal_gate(t.public(agree), hub, token, participant, b"A" * 32)
        self.assertEqual(c.open_gate(agree, hub, token, participant, gate), b"A" * 32)
        package = c.seal_package(b"L" * 32, b"A" * 32, hub, token, b"{}")
        self.assertEqual(c.open_package(b"L" * 32, b"A" * 32, hub, token, package), b"{}")


if __name__ == "__main__":
    unittest.main()
