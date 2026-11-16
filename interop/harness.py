"""Process drivers with durable test directories and event-based command deadlines."""
import base64
import json
import os
from pathlib import Path
import selectors
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
ENGINES = {
    'swift': dict(build=['swift', 'build', '--product', 'tandem-device'], cwd=ROOT,
                  argv=[str(ROOT / '.build/out/Products/Debug/tandem-device')]),
    'go': dict(build=['go', 'build', '-o', str(ROOT / '.build/tandem-device-go'), './engine/cmd/tandem-device'],
               cwd=ROOT / 'sdk/go', argv=[str(ROOT / '.build/tandem-device-go')]),
}


class Failed(Exception):
    pass


class Rejected(Failed):
    """A live driver explicitly refused the command; distinct from exit or timeout."""
    pass


# The driver's own deadline is this share of the harness's, so a hung command answers as a timeout
# well before the harness gives up on the process.
DRIVER_DEADLINE_SHARE = 0.8


def b64u(b):
    return base64.urlsafe_b64encode(b).rstrip(b'=').decode()


def unb64u(s):
    return base64.urlsafe_b64decode(s + '=' * (-len(s) % 4))


def check(what, got, want):
    if got != want:
        raise Failed(f'{what}: got {got!r}, want {want!r}')


def content(what, got, want):
    # Random test files can be large; never dump their contents on failure.
    import hashlib
    check(what, (len(got), hashlib.sha256(got).hexdigest()), (len(want), hashlib.sha256(want).hexdigest()))


def titles(device, lst):
    return sorted(unb64u(i['values']['title']).decode() for i in device('items', list=lst)['items'] if 'title' in i['values'])


class Device:
    def __init__(self, engine, name, directory=None, timeout=120):
        self.engine, self.name, self.directory, self.timeout = engine, name, directory, timeout
        self.hub, self.blob_options = None, None
        self.start()

    def start(self):
        self.process = subprocess.Popen(ENGINES[self.engine]['argv'], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        self.buffer = b''

    def __call__(self, op, **args):
        if op == 'hub':
            self.hub = dict(args)
            if self.directory is not None:
                args['directory'] = str(self.directory)
        if op == 'useBlobs':
            self.blob_options = dict(args)
        if self.process.poll() is not None or self.process.stdin.closed:
            raise Failed(f'{self.name} ({self.engine}) is not running for {op}')
        command = dict(op=op, deadlineSeconds=self.timeout * DRIVER_DEADLINE_SHARE, **args)
        try:
            self.process.stdin.write((json.dumps(command) + '\n').encode())
            self.process.stdin.flush()
        except BrokenPipeError:
            raise Failed(f'{self.name} ({self.engine}) exited during {op}') from None
        deadline = time.monotonic() + self.timeout
        with selectors.DefaultSelector() as selector:
            selector.register(self.process.stdout, selectors.EVENT_READ)
            while b'\n' not in self.buffer:
                if not selector.select(max(0, deadline - time.monotonic())):
                    # Stop the process: a late reply must never be read as the next command's.
                    self.kill()
                    raise Failed(f'{self.name} ({self.engine}) timed out during {op}')
                part = os.read(self.process.stdout.fileno(), 65536)
                if not part:
                    raise Failed(f'{self.name} ({self.engine}) exited during {op}')
                self.buffer += part
        line, self.buffer = self.buffer.split(b'\n', 1)
        reply = json.loads(line)
        if not reply.pop('ok'):
            if reply.get('timeout'):
                # The driver ran out of time: a hang, not a refusal. Its command may still be running.
                self.kill()
                raise Failed(f'{self.name} ({self.engine}) timed out during {op}: {reply["error"]}')
            raise Rejected(f'{self.name} ({self.engine}) {op}: {reply["error"]}')
        return reply

    def restart(self):
        """An ungraceful process loss; SQLite, staged content and keys survive."""
        if self.directory is None:
            raise Failed('restart needs durable test storage')
        self.kill()
        self.start()
        self('hub', **self.hub)
        self('reopen')
        if self.blob_options is not None:
            self('useBlobs', **self.blob_options)

    def die(self):
        """SIGKILL and reap, leaving the pipes open: a thread reading them sees end of file, not a closed descriptor."""
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=10)

    def kill(self):
        self.die()
        for pipe in (self.process.stdin, self.process.stdout):
            if not pipe.closed:
                pipe.close()

    def close(self):
        try:
            self.process.stdin.close()
            self.process.wait(timeout=30)
        finally:
            self.kill()
