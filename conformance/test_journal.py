import json
from pathlib import Path
import selectors
import signal
import sqlite3
import subprocess
import sys
import tempfile
import unittest

from cryptography.exceptions import InvalidTag
from journal import DiskHub, database, fixture, prepare, resume
import wire as w

HERE = Path(__file__).resolve().parent


class DurableRotation(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tandem-journal-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.f = fixture()

    def instance(self, label):
        root = self.root / label
        root.mkdir()
        local, remote, fixture_path = root/'local.sqlite', root/'hub.sqlite', root/'fixture.json'
        fixture_path.write_bytes(w.dumps(self.f))
        hub = DiskHub(remote)
        for msg in self.f['initial']:
            hub.append(msg['channel'], bytes.fromhex(msg['body']))
        return local, remote, fixture_path, hub

    def process(self, local, remote, f, pause=None):
        args = [sys.executable, '-B', str(HERE/'journal.py'), '--journal', str(local), '--hub', str(remote), '--fixture', str(f)]
        if pause:
            args += ['--pause', pause]
        return subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def kill_at(self, local, remote, f, label):
        p = self.process(local, remote, f, label)
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(p.stdout, selectors.EVENT_READ)
                self.assertTrue(selector.select(20), 'worker did not reach ' + label)
                self.assertEqual(p.stdout.readline().decode().strip(), label)
            p.kill()  # actual SIGKILL, no Python cleanup or simulated deepcopy
            p.wait(timeout=10)
            self.assertEqual(p.returncode, -signal.SIGKILL)
        finally:
            if p.poll() is None:
                p.kill()
                p.wait(timeout=10)
            p.stdin.close(); p.stdout.close(); p.stderr.close()

    def finish(self, local, remote, f):
        p = self.process(local, remote, f)
        _, errors = p.communicate(timeout=30)
        self.assertEqual(p.returncode, 0, errors.decode())

    def verify(self, local, hub):
        with database(local) as db:
            self.assertEqual(db.execute('PRAGMA integrity_check').fetchone()[0], 'ok')
            self.assertEqual(db.execute('SELECT count(*) FROM outbox WHERE done=0').fetchone()[0], 0)
            prepared = json.loads(db.execute("SELECT value FROM metadata WHERE key='plan'").fetchone()[0])
        key, scope = bytes.fromhex(prepared['key']), bytes.fromhex(prepared['scope'])
        current = [body for _, channel, body in hub.messages() if channel == prepared['channel']]
        payloads = [json.loads(w.open_envelope(key, prepared['channel'], body)) for body in current]
        expected = {w.verify_write(scope, w.unb64(rec['rec']), wr) for rec in self.f['snapshot'] for wr in rec['writes']}
        have = {w.verify_write(scope, w.unb64(p['rec']), wr) for p in payloads if 'rec' in p and p['rec'] != w.b64(w.named('_roster')) for wr in p['writes']}
        self.assertEqual(have, expected)
        inventory = next(w.unb64(p['inventory']['bytes']) for p in payloads if 'inventory' in p)
        roster = json.loads(w.unb64(prepared['statement']['v']))
        self.assertEqual(w.sha(inventory), w.unb64(roster['snapshot']))
        self.assertEqual(w.verify_write(scope, w.named('_roster'), prepared['statement']).hex(), prepared['epoch'])
        self.assertEqual({m['role'] for m in roster['members']}, {'owner', 'writer'})
        old_data = {w.sha(bytes.fromhex(m['body'])) for m in self.f['initial'][1:]}
        self.assertFalse(any(w.sha(body) in old_data for _, _, body in hub.messages()))
        owner, writer, removed = [w.identity(bytes(range(n, n+32))) for n in (0, 32, 64)]
        self.assertFalse(any(channel == w.tag(removed['uid'], inbox=True) for _, channel, _ in hub.messages()))
        for who in (owner, writer):
            grants = [json.loads(w.inbox_open(who, body)) for _, channel, body in hub.messages() if channel == w.tag(who['uid'], inbox=True)]
            self.assertTrue(grants)
            for grant in grants:
                data = b'TANDEM-GRANT-V1' + who['uid'] + scope + w.unb64(grant['at']) + w.unb64(grant['epoch']) + w.commitment(w.unb64(grant['k']))
                self.assertTrue(w.strict_verify(owner['uid'], w.unb64(grant['sig']), data))
        with database(hub.path) as db:
            events = list(db.execute('SELECT op,channel,digest FROM audit ORDER BY id'))
        announce = next(i for i, event in enumerate(events) if event[2] == w.sha(bytes.fromhex(next(a['body'] for a in prepared['actions'] if a['label'] == 'announce'))).hex())
        grant_tags = {w.tag(x['uid'], inbox=True) for x in (owner, writer)}
        self.assertTrue(all(i > announce for i, event in enumerate(events) if event[1] in grant_tags or event[0] == 'destroy'))
        with self.assertRaises(InvalidTag):
            w.open_envelope(bytes.fromhex(self.f['old_key']), prepared['channel'], current[0])
        return prepared

    def test_sigkill_at_every_durable_and_export_boundary(self):
        # Ten outbox actions. Repeated physical messages after a lost ack are
        # permitted; their encrypted bytes and logical write IDs remain stable.
        phases = ['before-export', 'after-export', 'before-ack-commit', 'after-ack-commit']
        labels = ['prepare:before-commit', 'prepare:after-commit'] + [f'{i}:{phase}' for i in range(10) for phase in phases]
        for number, label in enumerate(labels):
            with self.subTest(checkpoint=label):
                local, remote, f, hub = self.instance(str(number))
                self.kill_at(local, remote, f, label)
                with database(local) as db:
                    row = db.execute("SELECT value FROM metadata WHERE key='plan'").fetchone()
                    before = row[0] if row else None
                self.finish(local, remote, f)
                self.verify(local, hub)
                if before is not None:
                    with database(local) as db:
                        self.assertEqual(db.execute("SELECT value FROM metadata WHERE key='plan'").fetchone()[0], before)

    def test_missing_snapshot_is_restored_before_old_data_is_destroyed(self):
        local, remote, f, hub = self.instance('missing')
        self.kill_at(local, remote, f, '9:before-export')
        prepared = prepare(local, self.f)
        hub.destroy([mid for mid, channel, _ in hub.messages() if channel == prepared['channel']])
        self.finish(local, remote, f)
        self.verify(local, hub)

    def test_lost_ack_reuses_exact_ciphertext(self):
        local, remote, f, hub = self.instance('lost-ack')
        self.kill_at(local, remote, f, '5:after-export')
        first = prepare(local, self.f)
        self.finish(local, remote, f)
        final = self.verify(local, hub)
        self.assertEqual(first, final)
        body = bytes.fromhex(first['actions'][5]['body'])
        self.assertEqual(sum(stored == body for _, _, stored in hub.messages()), 2)


if __name__ == '__main__':
    unittest.main()
