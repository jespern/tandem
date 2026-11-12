import json
import os
from pathlib import Path
import selectors
import ssl
import subprocess
import sys
import tempfile
import unittest

from mail_transport import IMAP, JMAP, JMAPHub
from journal import database, fixture, prepare
from stalwart import Stalwart
import wire as w


@unittest.skipUnless(os.environ.get('TANDEM_TEST_MAIL') == '1', 'run with --mail for the disposable Stalwart integration tests')
class Mail(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = Stalwart()
        cls.server = cls.fixture.__enter__()
        # Deliberately restricted to this loopback-only, disposable server. No
        # live-account mode uses this context or accepts an untrusted certificate.
        cls.context = ssl._create_unverified_context()
        cls.jmap = JMAP(cls.server.origin + '/.well-known/jmap', cls.server.authorization,
                        cls.context, fixture_origin=cls.server.origin)
        cls.folder = cls.jmap.folder()

    @classmethod
    def tearDownClass(cls):
        cls.fixture.__exit__(None, None, None)

    def setUp(self):
        self.imap = self.connection()
        self.addCleanup(self.imap.close)
        self.imap.select()
        self.key = bytes(range(32))
        self.channel = w.tag(self.key, bytes([5])*32)
        self.body = w.seal(self.key, self.channel, w.dumps({'rec': w.b64(bytes(16)), 'writes': []}))
        self.addCleanup(lambda: self.jmap.destroy(self.jmap.ids(self.folder)))

    def connection(self):
        return IMAP('127.0.0.1', self.server.ports[993], self.server.username, self.server.password, self.context)

    def test_jmap_append_is_readable_over_imap(self):
        eid = self.jmap.append(self.folder, self.channel, self.body)
        found, cursor = self.imap.new(0)
        self.assertEqual(len(found), 1)
        uid = next(iter(found))
        self.assertEqual(found[uid], self.channel)
        self.assertEqual(self.imap.fetch(uid), (self.channel, self.body))
        self.assertEqual(self.jmap.fetch([eid])[eid], (self.channel, self.body))
        self.assertEqual(self.imap.new(cursor)[0], {})  # n:* may return the last UID again.

    def test_imap_encodings_are_readable_over_jmap(self):
        cases = [('7bit', 'us-ascii'), ('quoted-printable', 'utf-8'), ('base64', 'utf-16')]
        expected = []
        for i, (transfer, charset) in enumerate(cases):
            body = w.seal(self.key, self.channel, w.dumps({'encoding': i}))
            uid = self.imap.append(self.channel, body, transfer=transfer, charset=charset)
            self.assertEqual(self.imap.fetch(uid), (self.channel, body))
            expected.append(body)
        actual = self.jmap.fetch(self.jmap.ids(self.folder))
        self.assertEqual({body for channel, body in actual.values()}, set(expected))
        self.assertTrue(all(channel == self.channel for channel, _ in actual.values()))

    def test_large_body_is_not_truncated(self):
        body = w.seal(self.key, self.channel, w.dumps({'value': w.b64(bytes(range(256))*1024)}))
        eid = self.jmap.append(self.folder, self.channel, body)
        self.assertEqual(self.jmap.fetch([eid])[eid][1], body)
        self.assertEqual(self.imap.fetch(next(iter(self.imap.uids())))[1], body)

    def test_jmap_changes_include_moves_and_all_pages(self):
        other = self.jmap.folder('Other')
        initial = self.jmap.state()
        ids = [self.jmap.append(self.folder, self.channel, self.body) for _ in range(4)]
        added, removed, cursor = self.jmap.changes(initial, {}, self.folder, max_changes=1)
        self.assertEqual(set(added), set(ids))
        self.assertEqual(removed, set())
        self.jmap.call('Email/set', update={ids[0]: {'mailboxIds': {other: True}}})
        _, removed, cursor = self.jmap.changes(cursor, added, self.folder)
        self.assertEqual(removed, {ids[0]})
        self.jmap.call('Email/set', update={ids[0]: {'mailboxIds': {self.folder: True}}})
        returned, _, _ = self.jmap.changes(cursor, {i: self.channel for i in ids[1:]}, self.folder)
        self.assertEqual(set(returned), {ids[0]})

    def test_appenduid_does_not_skip_an_unseen_concurrent_message(self):
        _, scanned = self.imap.new(0)
        self.jmap.append(self.folder, self.channel, self.body)
        own = self.imap.append(self.channel, self.body)
        found, cursor = self.imap.new(scanned)
        self.assertIn(own, found)
        self.assertEqual(len(found), 2)
        self.assertEqual(cursor, own)

    def test_uid_expunge_preserves_unrelated_deleted_message(self):
        a = self.imap.append(self.channel, self.body)
        b = self.imap.append(self.channel, self.body)
        self.imap.client.uid('STORE', str(b), '+FLAGS.SILENT', '(\\Deleted)')
        self.imap.destroy({a})
        self.assertNotIn(a, self.imap.uids())
        self.assertIn(b, self.imap.uids())

    def test_idle_delivers_an_event_without_polling(self):
        with self.imap.client.idle(duration=10) as events:
            self.jmap.append(self.folder, self.channel, self.body)
            for kind, data in events:
                if kind == 'EXISTS':
                    break
            else:
                self.fail('no EXISTS push event')
        self.assertEqual(len(self.imap.new(0)[0]), 1)

    def test_eventsource_delivers_email_state_without_polling(self):
        with self.jmap.events() as events:
            self.jmap.append(self.folder, self.channel, self.body)
            expected = self.jmap.state()
            for line in events:
                if line.startswith(b'data:'):
                    event = json.loads(line[5:])
                    changed = event.get('changed', {}).get(self.jmap.account, {})
                    if changed.get('Email') == expected:
                        break
            else:
                self.fail('no Email StateChange push event')

    def test_qresync_reports_the_expunged_uid(self):
        reader = self.connection()
        self.addCleanup(reader.close)
        self.assertEqual(reader.client.enable('QRESYNC')[0], 'OK')
        reader.select()
        uid = self.imap.append(self.channel, self.body)
        reader.new(0)
        # SELECT refreshes HIGHESTMODSEQ before capturing the reconnect cursor.
        reader.select()
        validity = reader.client.response('UIDVALIDITY')[1][0].decode()
        modseq = reader.client.response('HIGHESTMODSEQ')[1][0].decode()
        self.imap.destroy({uid})
        typ, _ = reader.client._simple_command('SELECT', 'Tandem', f'(QRESYNC ({validity} {modseq}))')
        self.assertEqual(typ, 'OK')
        vanished = reader.client.response('VANISHED')[1]
        self.assertTrue(any(str(uid).encode() in line.split()[-1].split(b',') for line in vanished if line))

    def test_rotation_resumes_after_sigkill_against_the_real_mailbox(self):
        f = fixture()
        hub = JMAPHub(self.jmap, self.folder)
        for msg in f['initial']:
            hub.append(msg['channel'], bytes.fromhex(msg['body']))
        with tempfile.TemporaryDirectory(prefix='tandem-mail-journal-') as directory:
            root = Path(directory)
            config = dict(origin=self.server.origin, username=self.server.username, password=self.server.password, folder=self.folder)
            config_path, fixture_path, local = root/'mail.json', root/'fixture.json', root/'journal.sqlite'
            config_path.touch(mode=0o600)
            config_path.write_bytes(w.dumps(config))
            fixture_path.write_bytes(w.dumps(f))
            args = [sys.executable, '-B', str(Path(__file__).with_name('journal.py')), '--journal', str(local),
                    '--jmap-fixture', str(config_path), '--fixture', str(fixture_path)]
            p = subprocess.Popen(args + ['--pause', '5:after-export'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                with selectors.DefaultSelector() as selector:
                    selector.register(p.stdout, selectors.EVENT_READ)
                    self.assertTrue(selector.select(30), 'rotation did not reach the announcement')
                    self.assertEqual(p.stdout.readline(), b'5:after-export\n')
                p.kill()
                p.wait(timeout=10)
            finally:
                if p.poll() is None:
                    p.kill(); p.wait(timeout=10)
                p.stdin.close(); p.stdout.close(); p.stderr.close()
            prepared = prepare(local, f)
            before = hub.messages()
            writer = w.identity(bytes(range(32, 64)))
            self.assertFalse(any(channel == w.tag(writer['uid'], inbox=True) for _, channel, _ in before))
            subprocess.run(args, capture_output=True, check=True, timeout=30)
            after = hub.messages()
            with database(local) as db:
                self.assertEqual(db.execute('SELECT count(*) FROM outbox WHERE done=0').fetchone()[0], 0)
            grants = [body for _, channel, body in after if channel == w.tag(writer['uid'], inbox=True)]
            self.assertEqual(len(grants), 1)
            self.assertEqual(json.loads(w.inbox_open(writer, grants[0]))['epoch'], w.b64(bytes.fromhex(prepared['epoch'])))
            old_data = {bytes.fromhex(m['body']) for m in f['initial'][1:]}
            self.assertFalse(any(body in old_data for _, _, body in after))
            # Independently read every surviving encrypted byte through IMAP.
            imap_messages = [self.imap.fetch(uid) for uid in self.imap.uids()]
            self.assertEqual({w.sha(body) for _, _, body in after}, {w.sha(body) for _, body in imap_messages})
            self.assertEqual(prepare(local, f), prepared)


if __name__ == '__main__':
    unittest.main()
