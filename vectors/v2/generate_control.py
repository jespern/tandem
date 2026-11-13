"""Regenerate public v2 control-object vectors (§10.8) with the Python implementation.

One small hub, built the way the protocol builds it: genesis, the creator's first admission,
an invitation and the member it admits, a pairing with its package and gate, a rejection, a
revocation, a home epoch and the collection notices. All four SDKs read the checked-in file;
changes to it are protocol changes. No real secrets.
"""
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk/python"))
import tandem as t
import tandem.control as c
from tandem.crypto import _hpke_schedule, public
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305


def material(label, n=32):
    return t.sha(("Tandem v2 public control vector: " + label).encode())[:n]


def signing(label):
    return ed25519.Ed25519PrivateKey.from_private_bytes(material(label))


def agreement(label):
    return x25519.X25519PrivateKey.from_private_bytes(material(label))


def hpke_fixed(recipient_public, info, aad, plaintext, label):
    """HPKE as `seal_hpke` does it, with a fixed public ephemeral so the bytes are stable."""
    ephemeral = x25519.X25519PrivateKey.from_private_bytes(material(label))
    enc = public(ephemeral)
    key, nonce = _hpke_schedule(ephemeral.exchange(x25519.X25519PublicKey.from_public_bytes(recipient_public)),
                                enc, recipient_public, info.encode())
    return enc + ChaCha20Poly1305(key).encrypt(nonce, plaintext, aad)


def generate():
    objects, publics = [], {}

    def emit(name, kind, fields, signers=(), extras=None):
        sigs = {sig: c.sign(kind, fields, key) for sig, key in signers}
        for sig, key in signers:
            assert c.verify(kind, fields, public(key), sigs[sig])
        data = c.to_json(kind, fields, sigs, extras)
        objects.append(dict(name=name, kind=kind, json=data.decode("utf-8"),
                            input=c.signed_input(kind, fields).hex(), id=c.object_id(kind, fields).hex(),
                            signers={sig: public(key).hex() for sig, key in signers}))
        return c.object_id(kind, fields), sigs

    ms = material("management seed")
    mks, mka = t.manager_keys(ms)
    hub = public(mks)
    hubk = material("directory key")

    creator_s, creator_a = signing("creator signing"), agreement("creator agreement")
    creator_enrollment = material("creator enrollment", 16)
    creator = c.registration_id(hub, public(creator_s), public(creator_a), creator_enrollment)
    emit("creator registration", "registration",
         dict(hub=hub, sign=public(creator_s), agree=public(creator_a), enrollment=creator_enrollment))

    mailboxes = [dict(host="imap.example.org", port=993, user="hub@example.org"),
                 dict(host="imap.fastmail.com", port=993, user="hub@example.org")]
    mailboxes.sort(key=c._mailbox_bytes)
    genesis, _ = emit("genesis", "genesis", dict(hub=hub, agree=public(mka), hubkey=t.commit(hubk), creator=creator, mailboxes=mailboxes),
                      [("sig", mks)])

    sealed_capsule = material("capsule bytes (not a real capsule)", 76)
    capsule0, _ = emit("first capsule", "capsule",
                       dict(hub=hub, genesis=genesis, sealed=t.sha(sealed_capsule), supersedes=[]), [("sig", mks)],
                       {"capsule": sealed_capsule})
    sealed_capsule2 = material("second capsule bytes", 76)
    emit("capsule after a password change", "capsule",
         dict(hub=hub, genesis=genesis, sealed=t.sha(sealed_capsule2), supersedes=[capsule0]), [("sig", mks)],
         {"capsule": sealed_capsule2})

    def cert(name, request_fields, request_signers, issued, cert_signer):
        request_sigs = {sig: c.sign("request", request_fields, key) for sig, key in request_signers}
        request_id, _ = emit(name + " request", "request", request_fields, request_signers)
        cert_id, _ = emit(name + " certificate", "certificate",
                          dict(hub=hub, genesis=genesis, request=request_id, issued=issued), [("sig", cert_signer)],
                          {"body": (request_fields, request_sigs)})
        return cert_id

    creator_cert = cert("creator", dict(hub=hub, genesis=genesis, participant=creator, sign=public(creator_s),
                                        agree=public(creator_a), label="Mac", how=c.FIRST, authority=None,
                                        enrollment=creator_enrollment), [("self", creator_s)], [], mks)

    # An invitation, and Alice's phone admitted through it.
    invite_secret, invite = material("invite secret"), material("invite id", 16)
    invite_key = c.invite_key(invite_secret, hub, invite)
    capability, _ = emit("invitation", "invitation",
                         dict(hub=hub, genesis=genesis, key=public(invite_key), invite=invite,
                              expires=1_800_000_000_000, label="Alice"), [("sig", mks)])
    phone_s, phone_a = signing("alice phone signing"), agreement("alice phone agreement")
    alice_enrollment = material("alice enrollment", 16)
    alice = c.registration_id(hub, public(phone_s), public(phone_a), alice_enrollment)
    emit("alice registration", "registration",
         dict(hub=hub, sign=public(phone_s), agree=public(phone_a), enrollment=alice_enrollment))
    phone_cert = cert("alice phone", dict(hub=hub, genesis=genesis, participant=alice, sign=public(phone_s),
                                          agree=public(phone_a), label="Alice’s iPhone", how=c.INVITED,
                                          authority=capability, enrollment=alice_enrollment),
                      [("self", phone_s), ("approver", invite_key)], [], invite_key)
    emit("invitation cancelled", "receipt", dict(hub=hub, genesis=genesis, outcome=c.INVITE_CANCELLED,
                                                  subject=capability, signer=None), [("sig", mks)])

    # Pairing Alice's tablet from her phone.
    link, gate_secret, token = material("pairing L"), material("pairing A"), material("pairing token", 16)
    home0_placeholder = material("alice home epoch (placeholder)")
    home_key = material("alice home key")
    package_plain = json.dumps({"home": t.b64u(home0_placeholder), "k": t.b64u(home_key)},
                               separators=(",", ":")).encode()
    package = c.seal_package(link, gate_secret, hub, token, package_plain, nonce=material("package nonce", 12))
    gate = hpke_fixed(public(mka), "tandem/v2/pair-gate", b"TANDEM-PAIR-GATE-V2" + hub + token + alice, gate_secret,
                      "gate ephemeral")
    assert c.open_gate(mka, hub, token, alice, gate) == gate_secret
    assert c.open_package(link, gate_secret, hub, token, package) == package_plain
    pair_key = c.pairing_invite_key(link, hub, token)
    delegation, _ = emit("delegation", "delegation",
                         dict(hub=hub, genesis=genesis, participant=alice, source=phone_cert, key=public(pair_key),
                              token=token, expires=1_800_000_000_000, package=t.sha(package), gate=t.sha(gate)),
                         [("sig", phone_s)])
    tablet_s, tablet_a = signing("alice tablet signing"), agreement("alice tablet agreement")
    tablet_cert = cert("alice tablet", dict(hub=hub, genesis=genesis, participant=alice, sign=public(tablet_s),
                                            agree=public(tablet_a), label="Tablet", how=c.BY_PAIRING,
                                            authority=delegation, enrollment=bytes(16)),
                       [("self", tablet_s), ("approver", pair_key)], [], mks)
    emit("pairing completed", "receipt", dict(hub=hub, genesis=genesis, outcome=c.PAIRED, subject=tablet_cert,
                                               signer=tablet_cert), [("sig", tablet_s)])

    # A second claim on the same link, rejected by the admin.
    stranger_s, stranger_a = signing("stranger signing"), agreement("stranger agreement")
    rejected_request, _ = emit("rejected claim", "request",
                               dict(hub=hub, genesis=genesis, participant=alice, sign=public(stranger_s),
                                    agree=public(stranger_a), label="Laptop", how=c.BY_PAIRING,
                                    authority=delegation, enrollment=bytes(16)),
                               [("self", stranger_s), ("approver", pair_key)])
    emit("rejection", "receipt", dict(hub=hub, genesis=genesis, outcome=c.REJECTED, subject=rejected_request,
                                       signer=None), [("sig", mks)])

    # Revoking the tablet, keeping the phone.
    revocation, _ = emit("tablet revoked", "revocation",
                         dict(hub=hub, genesis=genesis, target=tablet_cert, retained=[phone_cert]), [("sig", mks)])

    snapshot, _ = emit("alice snapshot", "snapshot",
                       dict(hub=hub, genesis=genesis, participant=alice, certs=sorted([phone_cert, tablet_cert]),
                            revocations=[revocation]))
    empty_inventory = t.inventory_bytes({})
    home0, _ = emit("alice home epoch", "home-epoch",
                    dict(hub=hub, genesis=genesis, participant=alice, n=0, parents=[], key=t.commit(home_key),
                         recipients=sorted([phone_cert]), snapshot=t.sha(empty_inventory), signer=phone_cert,
                         auth=snapshot), [("sig", phone_s)])

    creator_home_key = material("creator home key")
    recovery = hpke_fixed(public(mka), "tandem/v2/home-recovery", b"TANDEM-HOME-RECOVERY-V2" + hub + home0,
                          creator_home_key, "recovery ephemeral")
    assert c.open_home_recovery(mka, hub, home0, recovery) == creator_home_key

    collection, statement, epoch = material("collection", 16), material("roster statement"), material("epoch start")
    epoch_key = material("epoch key")
    emit("grant", "grant", dict(hub=hub, collection=collection, recipient=alice, admission=statement, at=statement,
                                epoch=epoch, key=t.commit(epoch_key), signer=creator_cert, auth=snapshot),
         [("sig", creator_s)])
    emit("key request", "key-request", dict(hub=hub, collection=collection, participant=alice, have=statement,
                                            signer=phone_cert, auth=snapshot), [("sig", phone_s)])
    emit("leave", "leave", dict(hub=hub, collection=collection, participant=alice, at=statement, signer=phone_cert,
                                auth=snapshot), [("sig", phone_s)])
    emit("answer", "answer", dict(hub=hub, collection=collection, participant=alice, admission=statement,
                                  answer=b"a", event=material("event", 16), signer=phone_cert, auth=snapshot),
         [("sig", phone_s)])

    good = json.loads(next(o["json"] for o in objects if o["kind"] == "revocation"))
    genesis_json = json.loads(next(o["json"] for o in objects if o["kind"] == "genesis"))

    def bad(why, value):
        return dict(why=why, json=json.dumps(value, ensure_ascii=False, separators=(",", ":")))

    rejects = [
        bad("unknown type", dict(good, type="revocations")),
        bad("missing field", {k: v for k, v in good.items() if k != "target"}),
        bad("missing signature", {k: v for k, v in good.items() if k != "sig"}),
        bad("set not sorted", dict(good, retained=[good["retained"][0], t.b64u(bytes(32))])),
        bad("set with a duplicate", dict(good, retained=[good["retained"][0]] * 2)),
        bad("id of the wrong length", dict(good, target=t.b64u(bytes(31)))),
        bad("id in a second spelling", dict(good, target=good["target"] + "=")),
        bad("signature of the wrong length", dict(good, sig=t.b64u(bytes(63)))),
        bad("uppercase mailbox host", dict(genesis_json, mailboxes=[dict(genesis_json["mailboxes"][0],
                                                                          host="IMAP.EXAMPLE.ORG")])),
        bad("mailboxes not sorted", dict(genesis_json, mailboxes=list(reversed(genesis_json["mailboxes"])))),
        bad("text over 256 bytes", dict(json.loads(next(o["json"] for o in objects if o["kind"] == "invitation")),
                                        label="x" * 257)),
        bad("number out of range", dict(json.loads(next(o["json"] for o in objects if o["kind"] == "receipt")),
                                        outcome=256)),
        bad("answer other than a, n or l", dict(json.loads(next(o["json"] for o in objects if o["kind"] == "answer")),
                                                answer="y")),
        bad("mailbox host not ASCII", dict(genesis_json, mailboxes=[dict(genesis_json["mailboxes"][0], host="imap.exämple.org")])),
        bad("mailbox port a boolean", dict(genesis_json, mailboxes=[dict(genesis_json["mailboxes"][0], port=True)])),
        bad("type not a string", dict(good, type=["revocation"])),
        bad("capsule bytes that are not the sealed ones", dict(json.loads(next(o["json"] for o in objects if o["kind"] == "capsule")),
                                                             capsule=t.b64u(b"other bytes"))),
        bad("certificate body that is not its request", (lambda cj: dict(cj, body=dict(cj["body"], label="Someone else")))(
            json.loads(next(o["json"] for o in objects if o["kind"] == "certificate")))),
        bad("an all-zero optional id", dict(json.loads(next(o["json"] for o in objects if o["kind"] == "receipt")),
                                            signer=t.b64u(bytes(32)))),
    ]
    for r in rejects:
        try:
            c.from_json(r["json"].encode())
        except (c.ControlError, ValueError):
            continue
        raise SystemExit("accepted a reject vector: " + r["why"])
    # Duplicate property names are rejected by the strict reader, not the schema.
    rejects.append(dict(why="a property twice", json=objects[-1]["json"][:-1] + ',"sig":' +
                        json.dumps(json.loads(objects[-1]["json"])["sig"]) + "}"))

    derived = dict(invite_key=public(invite_key), pairing_invite_key=public(pair_key),
                   pairing_key=c.pairing_key(link, hub, token), pairing_tag=c.pairing_tag(link, hub, token),
                   directory_scope=c.directory_scope(hub), manager_agreement=public(mka),
                   package=package, package_plaintext=package_plain, gate=gate, home_recovery=recovery)
    secrets = dict(management_seed=ms, invite_secret=invite_secret, invite=invite, link=link, gate_secret=gate_secret,
                   token=token, home_epoch=home0, creator_home_key=creator_home_key)
    hexed = lambda d: {k: (v.hex() if isinstance(v, bytes) else v) for k, v in d.items()}
    return dict(description="Public Tandem v2 control-object vectors (§10.8). Test material only.",
                schema=c.schema_table(), material=hexed(secrets), derived=hexed(derived),
                objects=objects, rejects=rejects)


if __name__ == "__main__":
    data = json.dumps(generate(), ensure_ascii=False, indent=2) + "\n"
    path = Path(__file__).with_name("control.json")
    if "--check" in sys.argv:
        if path.read_text() != data:
            raise SystemExit("v2 control vectors have drifted")
    else:
        path.write_text(data)
