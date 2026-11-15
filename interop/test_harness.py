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
