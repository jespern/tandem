"""Regression checks for hangs, process cleanup, restart isolation and deletion boundaries."""
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

from harness import Device, ENGINES, Failed, Rejected
from providers import Mailbox


class HarnessTests(unittest.TestCase):
    def driver(self, program, **options):
        entry = dict(argv=[sys.executable, '-u', '-c', program])
        with patch.dict(ENGINES, fake=entry):
            device = Device('fake', 'fixture', **options)
        self.addCleanup(device.kill)
        return device

    def test_large_reply_and_another_command(self):
        device = self.driver('import sys,json\nfor line in sys.stdin:\n c=json.loads(line); print(json.dumps(dict(ok=True,value=c["value"])),flush=True)')
        for value in ('x' * 300_000, 'next'):
            self.assertEqual(device('echo', value=value), dict(value=value))

    def test_partial_line_has_a_deadline(self):
        # A real readable-byte event followed by no newline used to strand readline().
        device = self.driver('import sys\nsys.stdout.write("partial");sys.stdout.flush()\nsys.stdin.read()', timeout=0.1)
        with self.assertRaisesRegex(Failed, 'timed out during echo'):
            device('echo')
        device.kill()
        self.assertIsNotNone(device.process.returncode)

    def test_driver_exit_is_not_an_expected_rejection(self):
        device = self.driver('import sys\nsys.stdin.readline()\nsys.exit(1)')
        with self.assertRaisesRegex(Failed, 'exited during echo') as caught:
            device('echo')
        self.assertNotIsInstance(caught.exception, Rejected)

    def test_driver_deadline_is_a_timeout_not_a_rejection(self):
        # A hung command the driver gives up on answers a timeout; it must never pass as an expected refusal.
        device = self.driver('import sys,json\nc=json.loads(sys.stdin.readline())\nprint(json.dumps(dict(ok=False,error="context deadline exceeded",timeout=True,d=c["deadlineSeconds"])),flush=True)\nsys.stdin.read()', timeout=5)
        with self.assertRaisesRegex(Failed, 'timed out during awaitBlob') as caught:
            device('awaitBlob')
        self.assertNotIsInstance(caught.exception, Rejected)
        self.assertIsNotNone(device.process.returncode, 'the driver keeps running after a timeout')

    def test_driver_deadline_comes_before_the_harness_deadline(self):
        device = self.driver('import sys,json\nfor line in sys.stdin:\n c=json.loads(line); print(json.dumps(dict(ok=True,d=c["deadlineSeconds"])),flush=True)', timeout=10)
        self.assertLess(device('echo')['d'], 10)

    def test_a_late_reply_is_never_the_next_answer(self):
        # The harness gives up first: it stops the driver, so the stale reply can't answer the next command.
        device = self.driver('import sys,time,json\nsys.stdin.readline(); time.sleep(0.5)\nprint(json.dumps(dict(ok=True,value="late")),flush=True)\nsys.stdin.read()', timeout=0.1)
        with self.assertRaisesRegex(Failed, 'timed out during first'):
            device('first')
        with self.assertRaisesRegex(Failed, 'not running'):
            device('second')

    def test_rejection_is_an_explicit_driver_reply(self):
        device = self.driver('import sys,json\nsys.stdin.readline()\nprint(json.dumps(dict(ok=False,error="refused")),flush=True)')
        with self.assertRaisesRegex(Rejected, 'refused'):
            device('echo')

    def test_restart_reuses_only_its_own_directory(self):
        program = '''import sys,json
for line in sys.stdin:
 c=json.loads(line)
 print(json.dumps(dict(ok=True,directory=c.get("directory"))) if c["op"]=="hub" else json.dumps(dict(ok=True)),flush=True)
'''
        with tempfile.TemporaryDirectory() as directory:
            entry = dict(argv=[sys.executable, '-u', '-c', program])
            with patch.dict(ENGINES, fake=entry):
                device = Device('fake', 'fixture', Path(directory))
                try:
                    self.assertEqual(device('hub', host='test')['directory'], directory)
                    device.restart()
                    self.assertEqual(device('hub', host='test')['directory'], directory)
                finally:
                    device.close()

    def test_cleanup_refuses_unowned_names_before_connecting(self):
        for name in ('INBOX', 'Tandem', 'Tandem-interop-', 'Tandem-interop-a/*', 'Tandem-interop-a"'):
            with self.subTest(name=name), patch.object(Mailbox, 'connection', side_effect=AssertionError('connected')):
                with self.assertRaisesRegex(Failed, 'refusing cleanup'):
                    Mailbox(dict(folder=name)).cleanup()


if __name__ == '__main__':
    unittest.main()
