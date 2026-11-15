"""Regenerate public v2 core vectors with the Python implementation. No real secrets.

All four SDKs read the checked-in bytes; changes to these files are protocol changes.
"""
from dataclasses import asdict, replace
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk/python"))
import tandem as t
from tandem.crypto import _hpke_schedule, public
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305


def material(label, n=32):
    return t.sha(("Tandem v2 public test vector: " + label).encode())[:n]


def plain(value):
    if isinstance(value, bytes):
        return value.hex()
    if isinstance(value, (tuple, list)):
        return [plain(x) for x in value]
    if isinstance(value, dict):
        return {k: plain(v) for k, v in value.items()}
    return value


def generate():
    m = {key: material(key, n) for key, n in dict(hub=32, uid=32, key=32, epoch=32, scope=16, record=16,
         certificate=32, authorization=32, signing_seed=32, agreement_private=32, management_seed=32,
         device=8, device2=8).items()}
    signing = ed25519.Ed25519PrivateKey.from_private_bytes(m["signing_seed"])
    agreement = x25519.X25519PrivateKey.from_private_bytes(m["agreement_private"])
    m["signing_public"], m["agreement_public"] = public(signing), public(agreement)
    manager_sign, manager_agree = t.manager_keys(m["management_seed"])
    d = dict(home_scope=t.home_scope(m["hub"], m["uid"]), named=t.named_id("é"),
             named_decomposed=t.named_id("e\u0301"), commitment=t.commit(m["key"]),
             key_id=t.key_id(m["hub"], m["agreement_public"]),
             collection_tag=t.collection_tag(m["key"], m["hub"], m["epoch"]),
             home_tag=t.home_tag(m["key"], m["hub"], m["uid"], m["epoch"]),
             inbox_tag=t.inbox_tag(m["uid"], m["hub"]), directory_key=t.directory_key(m["key"], m["hub"]),
             directory_tag=t.directory_tag(m["key"], m["hub"]), manager_signing_public=public(manager_sign),
             manager_agreement_public=public(manager_agree))
    initial = t.Write(m["hub"], m["scope"], m["record"], "title", t.Op.ASSIGN, m["uid"],
                      m["certificate"], m["authorization"], t.Dot(m["device"], 123, 4), value=b"hello")
    later = replace(initial, value=b"updated", dot=t.Dot(m["device2"], 120, 0), context=(initial.dot,))
    concurrent = replace(initial, value=b"concurrent", dot=t.Dot(m["device2"], 119, 0))
    add = replace(initial, field="tags", op=t.Op.ADD, value=b"", element="é".encode())
    remove = replace(add, op=t.Op.REMOVE, dot=t.Dot(m["device2"], 200, 0))
    delete = replace(initial, field="", op=t.Op.DELETE, value=b"", context=t.join((initial.dot,), (later.dot,)))
    writes = [w.sign(signing) for w in [initial, later, concurrent, add, remove, delete,
              replace(initial, field="e\u0301", value=b""), replace(initial, field="é"),
              replace(initial, dot=t.Dot(m["device"], 2**48-1, 65535)),
              replace(initial, op=t.Op.ADD, value=b"", element=b"")]]
    rows = [dict(write=plain(asdict(w)), sig_input=w.sig_input().hex(), id=w.id.hex(),
                 payload=t.record_payload(w.record, [w]).hex()) for w in writes]
    plaintext = json.dumps(dict(test="public v2 fixture", unicode="é"), ensure_ascii=False, separators=(",", ":")).encode()
    salt = material("envelope-salt")
    symmetric = b"TNDM\x02\x01" + salt + ChaCha20Poly1305(t.kdf(m["key"], salt, "tandem/v2/msg")).encrypt(
        bytes(12), plaintext, t.aad(1, m["hub"], d["collection_tag"]))
    ephemeral = x25519.X25519PrivateKey.from_private_bytes(material("hpke-ephemeral"))
    enc = public(ephemeral)
    key, nonce = _hpke_schedule(ephemeral.exchange(agreement.public_key()), enc, public(agreement), b"tandem/v2/inbox")
    sealed = enc + ChaCha20Poly1305(key).encrypt(nonce, plaintext, t.aad(2, m["hub"], d["inbox_tag"]))
    inbox = b"TNDM\x02\x02" + t.key_id(m["hub"], public(agreement)) + sealed
    inv = {m["record"]: tuple(w.id for w in writes)}
    expected = []
    for indexes in ([0, 1], [0, 2], [3, 4], [0, 1, 2, 5], [6, 7], list(range(10))):
        record = t.Record(m["hub"], m["scope"], m["record"])
        record.merge(*(t.Entry(writes[i], m["epoch"], 0) for i in indexes))
        expected.append(dict(indexes=indexes, kept=[e.id.hex() for e in record.kept], deleted=record.deleted))
    # Packs (§10.5): two records' tombstones in one message, and payloads every reader rejects.
    other = replace(delete, record=material("record2", 16)).sign(signing)
    states = sorted([(m["record"], [writes[5]]), (other.record, [other])])
    entry = lambda rec, ws: dict(rec=t.b64u(rec), writes=json.loads(t.record_payload(rec, ws))["writes"])
    e1, e2 = (entry(r, ws) for r, ws in states)
    encode = lambda o: json.dumps(o, ensure_ascii=False, separators=(",", ":")).encode()
    single = json.loads(t.record_payload(states[0][0], states[0][1]))
    packs = dict(valid=[dict(payload=t.pack_payload(states).hex(),
                             records=[dict(record=r.hex(), writes=[w.id.hex() for w in ws]) for r, ws in states])],
                 invalid=[encode(o).hex() for o in (dict(recs=[e1]), dict(recs=[e2, e1]), dict(recs=[e1, e1]),
                                                     dict(recs=[e1, dict(e2, writes=[])]), dict(single, recs=[e1, e2]),
                                                     dict(recs={}), dict(recs=[]))])
    return dict(description="Public Tandem v2 core vectors; control formats awaiting Q28 are absent.",
                material=plain(m), derived=plain(d), writes=rows, plaintext=plaintext.hex(), symmetric=symmetric.hex(),
                inbox=inbox.hex(), inventory=t.inventory_bytes(inv).hex(), merges=expected, packs=packs,
                # A deletion mark (§8.3): an entry with no writes, beside an ordinary one.
                inventory_marked=t.inventory_bytes({m["record"]: (writes[0].id,), other.record: ()}).hex())


if __name__ == "__main__":
    data = json.dumps(generate(), ensure_ascii=False, indent=2) + "\n"
    path = Path(__file__).with_name("core.json")
    if "--check" in sys.argv:
        if path.read_text() != data:
            raise SystemExit("v2 vectors have drifted")
    else:
        path.write_text(data)
