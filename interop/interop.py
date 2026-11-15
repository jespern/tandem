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


def copying(new, hub):
    """The owner copies a list (the way out of a dead one, §11.8); the member is offered the copy and reads it."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice = new('b', 'Alice')
    alice('join', link=bob('invite', label='Alice')['link'], label='Alice’s phone')
    alice_id = alice('me')['participant']
    bob('sync')
    lst = bob('createList', name='House')['list']
    milk = b64u(os.urandom(16))
    bob('set', list=lst, record=milk, field='title', value=b64u(b'Milk'))
    bob('add', list=lst, record=milk, field='tags', element=b64u(b'dairy'))
    bob('share', list=lst, participant=alice_id, role='writer')
    alice('sync')
    alice('accept', list=lst)
    copy = bob('copy', list=lst)
    check('shared again', copy['shared'], [alice_id])
    check('undecided', copy['undecided'], [])
    alice('sync')
    check('offer', [o['list'] for o in alice('offers')['offers']], [copy['list']])
    check('copy of', alice('copyOf', list=copy['list'])['from'], lst)
    alice('accept', list=copy['list'])
    check('Alice reads the copy', titles(alice, copy['list']), ['Milk'])
    check('set field', [i['sets'].get('tags') for i in alice('items', list=copy['list'])['items']], [[b64u(b'dairy')]])
    alice('set', list=copy['list'], record=milk, field='done', value=b64u(b'1'))
    bob('sync')
    check('Bob reads Alice', [i['values'].get('done') for i in bob('items', list=copy['list'])['items']], [b64u(b'1')])


def blobs(new, hub):
    """Files both ways (DESIGN.md §17): an upload on one engine is read on the other, over the real blob folder; a chunk
    destroyed on the hub is restored by the other engine's device, which holds the file."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice = new('b', 'Alice')
    alice('join', link=bob('invite', label='Alice')['link'], label='Alice’s phone')
    bob('sync')
    lst = bob('createList', name='Photos')['list']
    bob('share', list=lst, participant=alice('me')['participant'], role='writer')
    alice('sync')
    alice('accept', list=lst)
    for d in (bob, alice):
        d('useBlobs', chunkSize=65536)
    photo, data = b64u(os.urandom(16)), os.urandom(200_000)
    bob('attach', list=lst, record=photo, name='photo', content=b64u(data))
    bob('settleBlobs')
    check('Bob has it', bob('blob', list=lst, record=photo, name='photo')['state'], 'here')
    check('four chunks, once each', bob('chunkCounts', list=lst, record=photo, name='photo')['counts'], [1, 1, 1, 1])
    alice('sync')
    check('Alice sees it remote', alice('blob', list=lst, record=photo, name='photo')['state'], 'remote')
    check('Alice reads it', unb64u(alice('fetchBlob', list=lst, record=photo, name='photo')['content']), data)
    doc, text = b64u(os.urandom(16)), os.urandom(70_000)
    alice('attach', list=lst, record=doc, name='file', content=b64u(text))
    alice('settleBlobs')
    bob('sync')
    check('Bob reads Alice\'s', unb64u(bob('fetchBlob', list=lst, record=doc, name='file')['content']), text)
    note = b64u(os.urandom(16))
    bob('attach', list=lst, record=note, name='note', content=b64u(b'inline, so no chunk'))
    alice('sync')
    check('inline', unb64u(alice('fetchBlob', list=lst, record=note, name='note')['content']), b'inline, so no chunk')
    # A credential holder destroys a chunk; Alice, who holds the file, puts it back.
    bob('dropChunk', list=lst, record=photo, name='photo', index=2)
    check('gone', bob('chunkCounts', list=lst, record=photo, name='photo')['counts'][2], 0)
    alice('checkBlobs')
    check('restored', alice('chunkCounts', list=lst, record=photo, name='photo')['counts'], [1, 1, 1, 1])
    # Replacing the file: the old chunks go once the new version is on the feed.
    newer = os.urandom(140_000)
    bob('attach', list=lst, record=photo, name='photo', content=b64u(newer))
    bob('settleBlobs')
    alice('sync')
    check('Alice reads the new version', unb64u(alice('fetchBlob', list=lst, record=photo, name='photo')['content']), newer)


def collecting(new, hub):
    """Garbage collection across engines (§9.3, §11.6, §11.9): a past epoch's writes dropped after the other engine's
    key change; a removed member's replica collected after the retention period; a deleted list's data destroyed on
    the hub by the owner's device, all but the roster, and collected on every device."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice, carol = new('b', 'Alice'), new('b', 'Carol')
    for person, name in ((alice, 'Alice'), (carol, 'Carol')):
        person('join', link=bob('invite', label=name)['link'], label=name)
    bob('sync')
    lst = bob('createList', name='House')['list']
    bob('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'Milk'))
    for person in (alice, carol):
        bob('share', list=lst, participant=person('me')['participant'], role='writer')
        person('sync')
        person('accept', list=lst)
    bread = b64u(os.urandom(16))
    for v in (b'Rye', b'Spelt', b'Sourdough'):
        alice('set', list=lst, record=bread, field='title', value=b64u(v))
    bob('sync')
    before = alice('writeCount', list=lst)['data']
    bob('removeMember', list=lst, participant=carol('me')['participant'])
    alice('sync')
    alice('set', list=lst, record=b64u(os.urandom(16)), field='title', value=b64u(b'Eggs'))
    bob('sync')
    alice('sync')
    if alice('writeCount', list=lst)['data'] >= before:
        raise Failed(f'Alice kept the replaced writes: {alice("writeCount", list=lst)["data"]} writes, {before} before')
    check('Alice reads', titles(alice, lst), ['Eggs', 'Milk', 'Sourdough'])
    check('Bob reads', titles(bob, lst), ['Eggs', 'Milk', 'Sourdough'])
    # Carol was removed: her replica stays for the retention period, then goes.
    carol('sync')
    check('Carol keeps it', carol('removedLists')['lists'], [lst])
    carol('setRetention', ms=1)
    carol('sync')
    carol('sync')
    check('Carol collected it', (carol('removedLists')['lists'], carol('goneLists')['lists'], carol('writeCount', list=lst)['data']), ([], [lst], 0))

    def tablet(name):
        t = new('b', name)
        t('pair', link=alice('startPairing', label=name)['link'], label=name)
        bob('sync')
        bob('approve', claim=bob('claims')['claims'][0]['id'])
        t('sync')
        return t
    # Bob deletes the list: it stays everywhere for the retention period.
    bob('deleteList', list=lst)
    alice('sync')
    check('deleted', alice('isDeleted', list=lst)['deleted'], True)
    if alice('writeCount', list=lst)['data'] == 0 or tablet('Alice’s tablet')('writeCount', list=lst)['data'] == 0:
        raise Failed('a deleted list is collected before the retention period')
    for d in (bob, alice):
        d('setRetention', ms=1)
    for d in (bob, bob, alice, alice):
        d('sync')
    check('Alice collected it', (alice('goneLists')['lists'], alice('writeCount', list=lst)['data']), ([lst], 0))
    late = tablet('Alice’s laptop')
    check('a late device learns of the deletion', late('isDeleted', list=lst)['deleted'], True)
    check('the hub holds only the roster', late('writeCount', list=lst)['data'], 0)


def packing(new, hub):
    """The owner's device packs tombstones (§10.7); a member and a new device on the other engine read the deleted
    records as deleted, from the packs, and a key change carries them."""
    bob = new('a', 'Bob')
    bob('found', label='Bob’s Mac')
    alice = new('b', 'Alice')
    alice('join', link=bob('invite', label='Alice')['link'], label='Alice')
    bob('sync')
    lst = bob('createList', name='House')['list']
    bob('share', list=lst, participant=alice('me')['participant'], role='writer')
    alice('sync')
    alice('accept', list=lst)
    bob('setPackThreshold', n=2)
    for i, name in enumerate((b'one', b'two', b'three', b'four')):
        record = b64u(os.urandom(16))
        alice('set', list=lst, record=record, field='title', value=b64u(name))
        if i < 3:
            alice('delete', list=lst, record=record)
    for d in (bob, alice, bob, alice, bob):
        d('sync')
    check('Alice reads', titles(alice, lst), ['four'])
    laptop = new('b', 'Alice’s laptop')
    laptop('pair', link=alice('startPairing', label='Laptop')['link'], label='Alice’s laptop')
    bob('sync')
    bob('approve', claim=bob('claims')['claims'][0]['id'])
    laptop('sync')
    check('a new device reads the packs', titles(laptop, lst), ['four'])
    bob('rekey', list=lst)
    for d in (alice, laptop, bob):
        d('sync')
    check('after a key change', (titles(alice, lst), titles(laptop, lst), titles(bob, lst)), (['four'], ['four'], ['four']))


SCENARIOS = [sharing, pairing, key_change, copying, blobs, collecting, packing]


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
