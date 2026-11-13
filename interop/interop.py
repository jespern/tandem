"""Two engines, one hub: Tandem devices built on different SDKs share a hub on a disposable Stalwart.

Each engine has a driver that reads one JSON command per line and answers one line (sdk/swift/Sources/tandem-device,
sdk/go/engine/cmd/tandem-device). This script starts Stalwart in Docker, then runs every scenario for every pair of
engines, the founder on one and the other people's devices on the other, each in its own IMAP folder.

    python3 interop/interop.py                 # every engine pair that is available
    python3 interop/interop.py swift go        # just this pair, founder first
"""
import base64
import itertools
import json
import os
import subprocess
import sys
import uuid

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, 'conformance'))
from stalwart import Stalwart  # noqa: E402


def b64u(b):
    return base64.urlsafe_b64encode(b).rstrip(b'=').decode()


def unb64u(s):
    return base64.urlsafe_b64decode(s + '=' * (-len(s) % 4))


ENGINES = {
    'swift': dict(build=['swift', 'build', '--product', 'tandem-device'], cwd=ROOT,
                  argv=[os.path.join(ROOT, '.build/out/Products/Debug/tandem-device')]),
    'go': dict(build=['go', 'build', '-o', os.path.join(ROOT, '.build/tandem-device-go'), './engine/cmd/tandem-device'],
               cwd=os.path.join(ROOT, 'sdk/go'), argv=[os.path.join(ROOT, '.build/tandem-device-go')]),
}


class Failed(Exception):
    pass


class Device:
    """One driver process: call it with an op and arguments, get the reply's fields."""

    def __init__(self, engine, name):
        self.engine, self.name = engine, name
        self.process = subprocess.Popen(ENGINES[engine]['argv'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)

    def __call__(self, op, **args):
        self.process.stdin.write(json.dumps(dict(op=op, **args)) + '\n')
        self.process.stdin.flush()
        line = self.process.stdout.readline()
        if not line:
            raise Failed(f'{self.name} ({self.engine}) exited during {op}')
        reply = json.loads(line)
        if not reply.pop('ok'):
            raise Failed(f'{self.name} ({self.engine}) {op}: {reply["error"]}')
        return reply

    def close(self):
        self.process.stdin.close()
        self.process.wait(timeout=30)


def titles(device, lst):
    return sorted(unb64u(i['values']['title']).decode() for i in device('items', list=lst)['items'] if 'title' in i['values'])


def check(what, got, want):
    if got != want:
        raise Failed(f'{what}: got {got!r}, want {want!r}')


# Scenarios: `a` is the founder and admin on the first engine, `others` start devices on the second.

def sharing(new, hub):
    """Invite, join, share, accept, and edit both ways, with a set field."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice = new('b', 'Alice')
    alice('join', link=bob('invite', label='Alice')['link'], label='Alice’s phone')
    alice('setName', name='Alice')
    alice_id = alice('me')['participant']
    bob('sync')
    lst = bob('createList', name='Groceries')['list']
    milk = b64u(os.urandom(16))
    bob('set', list=lst, record=milk, field='title', value=b64u(b'Milk'))
    bob('add', list=lst, record=milk, field='tags', element=b64u(b'dairy'))
    bob('share', list=lst, participant=alice_id, role='writer')
    alice('sync')
    check('offer', [o['name'] for o in alice('offers')['offers']], ['Groceries'])
    alice('accept', list=lst)
    check('Alice reads', titles(alice, lst), ['Milk'])
    tags = [i['sets'].get('tags') for i in alice('items', list=lst)['items']]
    check('set field', tags, [[b64u(b'dairy')]])
    alice('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'Bread'))
    bob('sync')
    check('Bob reads', titles(bob, lst), ['Bread', 'Milk'])
    names = sorted(p['name'] for p in bob('people')['people'] if p['name'])
    check('names', names, ['Alice'])


def pairing(new, hub):
    """A member pairs a device on the other engine; the admin approves; it reads the member's lists."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice = new('a', 'Alice')
    alice('join', link=bob('invite', label='Alice')['link'], label='Alice’s phone')
    lst = alice('createList', name='Notes')['list']
    alice('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'One'))
    tablet = new('b', 'Alice’s tablet')
    tablet('pair', link=alice('startPairing', label='Tablet')['link'], label='Alice’s tablet')
    check('waiting', tablet('me').get('awaitingApproval', True), True)
    bob('sync')
    claims = bob('claims')['claims']
    check('one claim', len(claims), 1)
    bob('approve', claim=claims[0]['id'])
    tablet('sync')
    check('same person', tablet('me')['participant'], alice('me')['participant'])
    check('tablet reads', titles(tablet, lst), ['One'])
    tablet('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'Two'))
    alice('sync')
    check('phone reads', titles(alice, lst), ['One', 'Two'])


def key_change(new, hub):
    """The owner removes a member and later changes the key again; the remaining member keeps writing."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice, carol = new('b', 'Alice'), new('b', 'Carol')
    for person, name in ((alice, 'Alice'), (carol, 'Carol')):
        person('join', link=bob('invite', label=name)['link'], label=name)
    bob('sync')
    lst = bob('createList', name='House')['list']
    bob('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'Before'))
    for person in (alice, carol):
        bob('share', list=lst, participant=person('me')['participant'], role='writer')
        person('sync')
        person('accept', list=lst)
    bob('removeMember', list=lst, participant=carol('me')['participant'])
    alice('sync')
    alice('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'After'))
    carol('sync')
    check('Carol removed', carol('role', list=lst)['role'], None)
    bob('rekey', list=lst)
    alice('sync')
    alice('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'Later'))
    bob('sync')
    check('Bob reads', titles(bob, lst), ['After', 'Before', 'Later'])
    check('Alice reads', titles(alice, lst), ['After', 'Before', 'Later'])


SCENARIOS = [sharing, pairing, key_change]


def run(server, first, second):
    failures = 0
    for scenario in SCENARIOS:
        devices = []
        hub = dict(host='127.0.0.1', port=server.ports[993], user=server.username, password=server.password,
                   folder='Tandem-interop-' + uuid.uuid4().hex[:8], insecure=True)

        def new(side, name):
            d = Device(first if side == 'a' else second, name)
            d('hub', **hub)
            devices.append(d)
            return d
        try:
            scenario(new, hub)
            print(f'ok    {first:>5} → {second:<5} {scenario.__name__}')
        except Failed as e:
            failures += 1
            print(f'FAIL  {first:>5} → {second:<5} {scenario.__name__}: {e}')
        finally:
            for d in devices:
                try:
                    d.close()
                except Exception:
                    d.process.kill()
    return failures


def main():
    wanted = sys.argv[1:]
    engines = [e for e in ENGINES if not wanted or e in wanted]
    for e in engines:
        subprocess.run(ENGINES[e]['build'], cwd=ENGINES[e]['cwd'], check=True, stdout=subprocess.DEVNULL)
    pairs = [tuple(wanted)] if len(wanted) == 2 else list(itertools.product(engines, repeat=2))
    with Stalwart() as server:
        failures = sum(run(server, a, b) for a, b in pairs)
    sys.exit(1 if failures else 0)


if __name__ == '__main__':
    main()
