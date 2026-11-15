import asyncio
from dataclasses import replace
import itertools
import json
import os
from pathlib import Path
import unittest

from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
import tandem as t

VECTORS = Path(__file__).resolve().parents[3] / "vectors/v2"


def read(name):
    return json.loads((VECTORS / name).read_text())


def write(row):
    w = dict(row["write"])
    def dot(d):
        return t.Dot(bytes.fromhex(d["device"]), d["millis"], d["counter"])
    for key in ("hub", "scope", "record", "author", "certificate", "authorization", "ref", "signature", "value"):
        w[key] = bytes.fromhex(w[key])
    w["element"] = bytes.fromhex(w["element"]) if w["element"] is not None else None
    w["op"] = t.Op(w["op"]); w["dot"] = dot(w["dot"])
    w["context"], w["base"] = tuple(map(dot, w["context"])), tuple(map(dot, w["base"]))
    return t.Write(**w)


class CoreTests(unittest.TestCase):
    def setUp(self):
        self.v = read("core.json")
        self.m = {k: bytes.fromhex(v) for k, v in self.v["material"].items()}

    def test_derivations_and_inventory(self):
        m, d = self.m, self.v["derived"]
        sign, agree = t.manager_keys(m["management_seed"])
        expected = dict(home_scope=t.home_scope(m["hub"], m["uid"]).hex(), named=t.named_id("é").hex(),
                        named_decomposed=t.named_id("e\u0301").hex(), commitment=t.commit(m["key"]).hex(),
                        key_id=t.key_id(m["hub"], m["agreement_public"]).hex(),
                        collection_tag=t.collection_tag(m["key"], m["hub"], m["epoch"]),
                        home_tag=t.home_tag(m["key"], m["hub"], m["uid"], m["epoch"]),
                        inbox_tag=t.inbox_tag(m["uid"], m["hub"]), directory_key=t.directory_key(m["key"], m["hub"]).hex(),
                        directory_tag=t.directory_tag(m["key"], m["hub"]), manager_signing_public=t.public(sign).hex(),
                        manager_agreement_public=t.public(agree).hex())
        self.assertEqual(expected, d)
        inv = bytes.fromhex(self.v["inventory"])
        self.assertEqual(t.inventory_bytes(t.parse_inventory(inv)), inv)
        for bad in (inv + b"x", inv[:-1], inv[:20] + bytes(4)):
            with self.assertRaises(ValueError):
                t.parse_inventory(bad)

    def test_packs(self):
        """Packs (§10.5): several records' states in one message; the vectors' bad forms are refused."""
        m, packs = self.m, self.v["packs"]
        for p in packs["valid"]:
            payload = bytes.fromhex(p["payload"])
            states = t.parse_kind1(payload, m["hub"], m["scope"])
            self.assertEqual([(r.hex(), [w.id.hex() for w in ws]) for r, ws in states],
                             [(r["record"], r["writes"]) for r in p["records"]])
            self.assertEqual(t.pack_payload([(r, list(ws)) for r, ws in states]), payload)
        for bad in packs["invalid"]:
            with self.assertRaises((ValueError, KeyError, TypeError)):
                t.parse_kind1(bytes.fromhex(bad), m["hub"], m["scope"])

    def test_writes_and_bindings(self):
        m = self.m
        for row in self.v["writes"]:
            with self.subTest(row["id"]):
                w = write(row)
                self.assertEqual(w.sig_input().hex(), row["sig_input"])
                self.assertEqual(w.id.hex(), row["id"])
                self.assertTrue(w.verify(m["signing_public"]))
                self.assertFalse(w.verify(w.author))
                rec, ws = t.parse_kind1(t.record_payload(w.record, [w]), w.hub, w.scope)
                self.assertEqual((rec, ws), (w.record, (w,)))
                signed = replace(w, signature=b"").sign(ed25519.Ed25519PrivateKey.from_private_bytes(m["signing_seed"]))
                self.assertTrue(signed.verify(m["signing_public"]))
                for binding in ("hub", "certificate", "authorization", "author"):
                    self.assertFalse(replace(w, **{binding: bytes(32)}).verify(m["signing_public"]))

    def test_envelopes(self):
        m, v = self.m, self.v
        key = x25519.X25519PrivateKey.from_private_bytes(m["agreement_private"])
        plain, tag = bytes.fromhex(v["plaintext"]), v["derived"]["collection_tag"]
        self.assertEqual(t.open_envelope(m["key"], m["hub"], tag, bytes.fromhex(v["symmetric"])), (1, plain))
        self.assertEqual(t.open_inbox(m["hub"], m["uid"], key, bytes.fromhex(v["inbox"])), plain)
        for kind in (1, 3):
            sealed = t.seal(m["key"], m["hub"], tag, plain, kind)
            self.assertEqual(t.open_envelope(m["key"], m["hub"], tag, sealed), (kind, plain))
        inbox = t.seal_inbox(m["hub"], m["uid"], m["agreement_public"], plain)
        self.assertEqual(t.open_inbox(m["hub"], m["uid"], key, inbox), plain)
        bad = bytearray(bytes.fromhex(v["symmetric"])); bad[4] = 1
        for hub, channel, sealed in ((bytes(32), tag, bytes.fromhex(v["symmetric"])), (m["hub"], tag.upper(), bytes.fromhex(v["symmetric"])), (m["hub"], tag, bytes(bad))):
            with self.assertRaises(Exception):
                t.open_envelope(m["key"], hub, channel, sealed)
        for length in range(len(bytes.fromhex(v["symmetric"]))):
            with self.assertRaises(Exception):
                t.open_envelope(m["key"], m["hub"], tag, bytes.fromhex(v["symmetric"])[:length])
        if directory := os.environ.get("TANDEM_INTEROP_DIR"):
            signature = ed25519.Ed25519PrivateKey.from_private_bytes(m["signing_seed"]).sign(plain)
            (Path(directory) / "python.json").write_text(json.dumps(dict(inbox=inbox.hex(), signature=signature.hex())))

    def test_strict_parsers_and_signature_policy(self):
        for row in read("json.json")["cases"]["invalid"]:
            with self.subTest(row), self.assertRaises((ValueError, UnicodeError)):
                t.parse_json(bytes.fromhex(row["hex"]))
        for row in read("json.json")["cases"]["valid"]:
            t.parse_json(bytes.fromhex(row["hex"]))
        for row in read("b64u.json")["cases"]["invalid"]:
            with self.assertRaises(ValueError):
                t.unb64u(row["text"])
        for row in read("b64u.json")["cases"]["valid"]:
            self.assertEqual(t.unb64u(row["text"]).hex(), row["hex"])
        for row in read("ed25519.json")["cases"]["signatures"]:
            self.assertEqual(t.verify_strict(bytes.fromhex(row["public"]), bytes.fromhex(row["signature"]), bytes.fromhex(row["message"])), row["valid"], row)

    def test_merge_arrival_order_replay_and_scope(self):
        m = self.m
        for case in self.v["merges"]:
            entries = [t.Entry(write(self.v["writes"][i]), m["epoch"], 0) for i in case["indexes"]]
            orders = itertools.permutations(entries) if len(entries) <= 4 else (entries, list(reversed(entries)))
            for order in orders:
                record = t.Record(m["hub"], m["scope"], m["record"])
                for e in order:
                    record.merge(e)
                record.merge(*entries)
                self.assertEqual([e.id.hex() for e in record.kept], case["kept"])
                self.assertEqual(record.deleted, case["deleted"])
        record = t.Record(m["hub"], m["scope"], m["record"])
        with self.assertRaises(ValueError):
            record.merge(t.Entry(replace(write(self.v["writes"][0]), hub=bytes(32)), m["epoch"], 0))

    def test_clock_rollover_and_limits(self):
        self.assertEqual(t.next_clock(9, 65535, 0), (10, 0))
        self.assertEqual(t.next_clock(9, 2, 4), (9, 3))
        self.assertEqual(t.next_clock(9, 2, 11), (11, 0))
        with self.assertRaises(ValueError):
            t.next_clock(2**48-1, 65535, 0)

    @unittest.skipUnless(os.environ.get("TANDEM_VERIFY_INTEROP") == "1", "run sdk/check.py for cross-SDK verification")
    def test_cross_sdk(self):
        m, v = self.m, self.v
        key = x25519.X25519PrivateKey.from_private_bytes(m["agreement_private"])
        for path in sorted(Path(os.environ["TANDEM_INTEROP_DIR"]).glob("*.json")):
            language = path.stem
            artifact = json.loads((Path(os.environ["TANDEM_INTEROP_DIR"]) / (language + ".json")).read_text())
            self.assertTrue(t.verify_strict(m["signing_public"], bytes.fromhex(artifact["signature"]), bytes.fromhex(v["plaintext"])), language)
            self.assertEqual(t.open_inbox(m["hub"], m["uid"], key, bytes.fromhex(artifact["inbox"])), bytes.fromhex(v["plaintext"]), language)


class FakeStore:
    def __init__(self):
        self.events = asyncio.Queue()
        self.calls, self.subscribed, self.closed = 0, False, False
        self.added = []
        self.observed = asyncio.Queue()

    async def watch(self):
        self.subscribed = True
        async def events():
            while True:
                item = await self.events.get()
                if item == "end":
                    return
                yield None
        return events()

    async def changes(self, cursor):
        assert self.subscribed
        self.calls += 1
        self.observed.put_nowait(self.calls)
        batch = t.ChangeBatch(tuple(self.added), (), str(self.calls))
        self.added = []
        return batch

    async def append(self, channel, body):
        self.added.append(t.Message("local", channel, body))
        return "local"

    async def close(self):
        self.closed = True


class SessionTests(unittest.IsolatedAsyncioTestCase):
    async def test_close_releases_blocked_submissions_and_preserves_buffered_batches(self):
        store = FakeStore()
        async with asyncio.timeout(5):
            session = await t.Session(store, capacity=1).__aenter__()
            self.assertEqual(await store.observed.get(), 1)
            await store.events.put(None)
            self.assertEqual(await store.observed.get(), 2)  # worker now blocks on the full updates queue
            pending = [asyncio.create_task(session.submit("channel", b"queued")) for _ in range(3)]
            barrier = asyncio.Event()
            asyncio.get_running_loop().call_soon(barrier.set)
            await barrier.wait()  # let the queued tasks reach their real queue waits
            await session.close()
            results = await asyncio.gather(*pending, return_exceptions=True)
            self.assertTrue(all(isinstance(result, BaseException) for result in results))
            updates = session.updates()
            self.assertEqual((await anext(updates)).cursor, "1")
            with self.assertRaises(StopAsyncIteration):
                await anext(updates)

    async def test_push_submit_reconnect_and_shutdown(self):
        store = FakeStore()
        async with asyncio.timeout(5):  # failure safety net, never a work trigger
            async with t.Session(store) as session:
                updates = session.updates()
                self.assertEqual((await anext(updates)).cursor, "1")
                await store.events.put(None)
                self.assertEqual((await anext(updates)).cursor, "2")
                self.assertEqual(await session.submit("channel", b"local edit"), "local")
                self.assertEqual((await anext(updates)).added[0].body, b"local edit")
                await store.events.put(None)  # reconnect notification
                self.assertEqual((await anext(updates)).cursor, "4")
            self.assertTrue(store.closed)
            self.assertEqual(store.calls, 4)
            with self.assertRaises(StopAsyncIteration):
                await anext(updates)

    async def test_subscription_end_surfaces_and_closes(self):
        store = FakeStore()
        async with asyncio.timeout(5):
            async with t.Session(store) as session:
                updates = session.updates(); await anext(updates)
                await store.events.put("end")
                with self.assertRaises(ConnectionError):
                    await anext(updates)
            self.assertTrue(store.closed)
