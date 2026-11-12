"""Real signed/encrypted rotation bytes in a SQLite outbox, with crash barriers.

This exercises the single-device rotation path of DESIGN §11.6. It is not a
complete collection engine: roster decisions and the preserved snapshot are
inputs, and the user's secret is public test material instead of Keychain data.
"""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import sqlite3
import ssl
import struct
import sys
import urllib.parse

from cryptography.hazmat.primitives.asymmetric import ed25519
import wire as w


@contextmanager
def database(path):
    con = sqlite3.connect(path)
    try:
        con.execute('PRAGMA journal_mode=WAL')
        con.execute('PRAGMA synchronous=FULL')
        with con:
            yield con
    finally:
        con.close()


class DiskHub:
    """Separate durable transport, permitting duplicate physical messages."""
    def __init__(self, path):
        self.path = path
        with database(path) as db:
            db.execute('CREATE TABLE IF NOT EXISTS messages(id INTEGER PRIMARY KEY, channel TEXT, body BLOB)')
            db.execute('CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, op TEXT, channel TEXT, digest TEXT)')

    def append(self, channel, body):
        with database(self.path) as db:
            row = db.execute('INSERT INTO messages(channel,body) VALUES (?,?)', (channel, body))
            db.execute('INSERT INTO audit(op,channel,digest) VALUES (?,?,?)', ('append', channel, w.sha(body).hex()))
            return row.lastrowid

    def messages(self):
        with database(self.path) as db:
            return list(db.execute('SELECT id,channel,body FROM messages ORDER BY id'))

    def destroy(self, ids):
        with database(self.path) as db:
            for mid in ids:
                row = db.execute('SELECT channel,body FROM messages WHERE id=?', (mid,)).fetchone()
                if row:
                    db.execute('INSERT INTO audit(op,channel,digest) VALUES (?,?,?)', ('destroy', row[0], w.sha(row[1]).hex()))
                    db.execute('DELETE FROM messages WHERE id=?', (mid,))


def roster_member(who, role):
    return dict(uid=w.b64(who['uid']), uka=w.b64(who['uka']), name=role, role=role)


def fixture():
    owner, writer, removed = [w.identity(bytes(range(n, n+32))) for n in (0, 32, 64)]
    old_key = os.urandom(32)
    body = dict(n=0, key=dict(n=0, commit=w.b64(w.commitment(old_key))),
                members=[roster_member(owner, 'owner')], deleted=False, nonce=w.b64(os.urandom(16)))
    genesis, gid = w.signed_write(owner, bytes(16), w.named('_roster'), 's', w.dumps(body), bytes(32))
    scope = gid[:16]
    body = dict(body, n=1, members=body['members'] + [roster_member(writer, 'writer'), roster_member(removed, 'reader')])
    admission, sid = w.signed_write(owner, scope, w.named('_roster'), 's', w.dumps(body), gid, millis=2)
    rec, deleted = os.urandom(16), os.urandom(16)
    content, wid = w.signed_write(writer, scope, rec, 'title', b'persist across the crash', sid, millis=3)
    tombstone, tid = w.signed_write(owner, scope, deleted, '', b'', sid, millis=4, op=4)
    old_tag = w.tag(old_key, gid)
    records = [dict(rec=w.b64(rec), writes=[content]), dict(rec=w.b64(deleted), writes=[tombstone])]
    return dict(scope=scope.hex(), old_epoch=gid.hex(), old_key=old_key.hex(), old_tag=old_tag,
                roster=[genesis, admission], snapshot=records,
                initial=[dict(channel=old_tag, body=w.seal(old_key, old_tag, w.dumps(value)).hex())
                         for value in [dict(rec=w.b64(w.named('_roster')), writes=[genesis, admission]), *records]])


def plan(f):
    owner, writer = [w.identity(bytes(range(n, n+32))) for n in (0, 32)]
    scope, old_key = bytes.fromhex(f['scope']), bytes.fromhex(f['old_key'])
    inventory = []
    for rec in f['snapshot']:
        ids = [w.verify_write(scope, w.unb64(rec['rec']), wr) for wr in rec['writes']]
        inventory.append((w.unb64(rec['rec']), sorted(ids)))
    raw_inventory = struct.pack('>I', len(inventory))
    for rec, ids in sorted(inventory):
        raw_inventory += rec + struct.pack('>I', len(ids)) + b''.join(ids)
    key = os.urandom(32)
    previous = f['roster'][-1]
    previous_id = w.verify_write(scope, w.named('_roster'), previous)
    body = json.loads(w.unb64(previous['v']))
    body.update(n=2, key=dict(n=1, commit=w.b64(w.commitment(key)), prev=w.b64(w.commitment(old_key))),
                snapshot=w.b64(w.sha(raw_inventory)), members=[roster_member(owner, 'owner'), roster_member(writer, 'writer')])
    statement, epoch = w.signed_write(owner, scope, w.named('_roster'), 's', w.dumps(body), previous_id, millis=5)
    channel = w.tag(key, epoch)
    home = w.tag(owner['home'])
    home_scope = w.sha(b'tandem/v1/home' + owner['uid'])[:16]
    membership = w.named('membership:' + w.b64(scope))
    flight = dict(parent=w.b64(previous_id), id=w.b64(epoch), key=w.b64(key), members=body['members'])
    note, note_id = w.signed_write(owner, home_scope, membership, 'flight', w.dumps(flight), bytes(32), millis=6)
    actions = []

    def append(label, channel, key, payload):
        actions.append(dict(label=label, op='append', channel=channel, body=w.seal(key, channel, w.dumps(payload)).hex()))

    append('home-flight', home, owner['home'], dict(rec=w.b64(membership), writes=[note]))
    append('inventory', channel, key, dict(inventory=dict(epoch=w.b64(epoch), part=0, parts=1, bytes=w.b64(raw_inventory))))
    for i, rec in enumerate(f['snapshot']):
        append('snapshot-' + str(i), channel, key, rec)
    append('new-roster', channel, key, dict(rec=w.b64(w.named('_roster')), writes=f['roster'] + [statement]))
    append('announce', f['old_tag'], old_key, dict(rec=w.b64(w.named('_roster')), writes=[statement]))
    cleared, _ = w.signed_write(owner, home_scope, membership, 'flight', w.dumps(None), bytes(32),
                                millis=7, context=[(b'device01', 6, 0)])
    entry = w.dumps(dict(epoch=w.b64(epoch), k=w.b64(key)))
    added, _ = w.signed_write(owner, home_scope, membership, 'keys', b'', bytes(32), millis=8, op=2, element=entry)
    append('home-commit', home, owner['home'], dict(rec=w.b64(membership), writes=[cleared, added]))
    for who in (owner, writer):
        signed = b'TANDEM-GRANT-V1' + who['uid'] + scope + epoch + epoch + w.commitment(key)
        grant = dict(type='grant', c=w.b64(scope), at=w.b64(epoch), epoch=w.b64(epoch), k=w.b64(key),
                     sig=w.b64(ed25519.Ed25519PrivateKey.from_private_bytes(owner['sign']).sign(signed)))
        actions.append(dict(label='grant-' + w.b64(who['uid']), op='append', channel=w.tag(who['uid'], inbox=True),
                            body=w.inbox_seal(who['uid'], who['uka'], w.dumps(grant)).hex()))
    old_data = [w.sha(bytes.fromhex(m['body'])).hex() for m in f['initial'][1:]]
    actions.append(dict(label='cleanup', op='destroy', channel=f['old_tag'], digests=old_data))
    return dict(epoch=epoch.hex(), key=key.hex(), channel=channel, scope=scope.hex(),
                inventory=raw_inventory.hex(), statement=statement, actions=actions)


def prepare(path, f, checkpoint=lambda label: None):
    with database(path) as db:
        db.execute('CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY, value BLOB)')
        db.execute('CREATE TABLE IF NOT EXISTS outbox(seq INTEGER PRIMARY KEY, action BLOB, done INTEGER NOT NULL DEFAULT 0)')
        found = db.execute("SELECT value FROM metadata WHERE key='plan'").fetchone()
        if found:
            return json.loads(found[0])
        prepared = plan(f)
        db.execute('INSERT INTO metadata VALUES (?,?)', ('plan', w.dumps(prepared)))
        for seq, action in enumerate(prepared['actions']):
            db.execute('INSERT INTO outbox(seq,action) VALUES (?,?)', (seq, w.dumps(action)))
        checkpoint('prepare:before-commit')
    checkpoint('prepare:after-commit')
    return prepared


def restore_snapshot(prepared, hub):
    have = {w.sha(body).hex() for _, _, body in hub.messages()}
    for action in prepared['actions']:
        if action['channel'] == prepared['channel'] and action['op'] == 'append':
            body = bytes.fromhex(action['body'])
            if w.sha(body).hex() not in have:
                hub.append(action['channel'], body)


def resume(path, f, hub, checkpoint=lambda label: None):
    prepared = prepare(path, f, checkpoint)
    with database(path) as db:
        if db.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
            raise RuntimeError('corrupt journal')
        rows = list(db.execute('SELECT seq,action FROM outbox WHERE done=0 ORDER BY seq'))
        for seq, raw in rows:
            action = json.loads(raw)
            checkpoint(f'{seq}:before-export')
            if action['op'] == 'append':
                if action['label'] == 'announce':
                    restore_snapshot(prepared, hub)
                hub.append(action['channel'], bytes.fromhex(action['body']))
            else:
                restore_snapshot(prepared, hub)
                ids = [mid for mid, channel, body in hub.messages()
                       if channel == action['channel'] and w.sha(body).hex() in action['digests']]
                hub.destroy(ids)
            checkpoint(f'{seq}:after-export')
            db.execute('UPDATE outbox SET done=1 WHERE seq=?', (seq,))
            checkpoint(f'{seq}:before-ack-commit')
            db.commit()
            checkpoint(f'{seq}:after-ack-commit')
    return prepared


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--journal', required=True)
    transport = parser.add_mutually_exclusive_group(required=True)
    transport.add_argument('--hub')
    transport.add_argument('--jmap-fixture')
    parser.add_argument('--fixture', required=True)
    parser.add_argument('--pause')
    args = parser.parse_args()
    os.umask(0o077)

    def checkpoint(label):
        if label == args.pause:
            print(label, flush=True)
            sys.stdin.readline()  # parent kills the process at this barrier

    if args.hub:
        hub = DiskHub(args.hub)
    else:
        import base64
        from mail_transport import JMAP, JMAPHub
        config = json.loads(Path(args.jmap_fixture).read_text())
        origin = config['origin']
        parsed = urllib.parse.urlsplit(origin)
        if parsed.hostname != '127.0.0.1' or parsed.scheme != 'https':
            raise ValueError('this test transport is restricted to the ephemeral loopback fixture')
        auth = 'Basic ' + base64.b64encode((config['username'] + ':' + config['password']).encode()).decode()
        client = JMAP(origin+'/.well-known/jmap', auth, ssl._create_unverified_context(), fixture_origin=origin)
        hub = JMAPHub(client, config['folder'])
    resume(args.journal, json.loads(Path(args.fixture).read_text()), hub, checkpoint)


if __name__ == '__main__':
    main()
