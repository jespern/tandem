"""Go/Swift end-to-end scenarios on disposable Stalwart or isolated Fastmail folders.

    python3 -B interop/interop.py --files
    python3 -B interop/interop.py swift go --files --provider fastmail

See interop/README.md for credential setup, coverage and fault boundaries.
"""
import argparse
from contextlib import ExitStack
import itertools
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from harness import ENGINES, ROOT, Device, Failed, b64u, unb64u, titles, check
from providers import Mailbox, fastmail, isolated

sys.path.insert(0, str(ROOT / 'conformance'))
from stalwart import Stalwart


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


def run(config, provider, first, second, scenarios, timeout):
    results = []
    for scenario in scenarios:
        devices, hub = [], isolated(config)
        started = time.monotonic()
        error = None
        with tempfile.TemporaryDirectory(prefix='tandem-interop-') as temporary:
            def new(side, name):
                directory = Path(temporary) / str(len(devices))
                directory.mkdir(mode=0o700)
                d = Device(first if side == 'a' else second, name, directory, timeout)
                devices.append(d)
                d('hub', **hub)
                return d
            try:
                scenario(new, hub)
            except Exception as exc:
                error = str(exc)
            finally:
                for d in devices:
                    try:
                        d.close()
                    except Exception as exc:
                        error = error or f'driver cleanup failed: {type(exc).__name__}'
                try:
                    Mailbox(hub).cleanup()
                except Exception as exc:
                    error = (error + '; ' if error else '') + f'mailbox cleanup failed: {type(exc).__name__}: {exc}; folder {hub["folder"]}'
        result = dict(provider=provider, sender=first, receiver=second, scenario=scenario.__name__,
                      seconds=round(time.monotonic() - started, 3), ok=error is None)
        if hasattr(scenario, 'details'):
            result['details'] = scenario.details
        if error:
            for secret in (hub['user'], hub['password']):
                error = error.replace(secret, '[redacted]')
            result['error'] = error
        results.append(result)
        print(f'{"FAIL" if error else "ok  "}  {provider} {first} → {second} {scenario.__name__} ({result["seconds"]:.1f}s)' +
              (f': {error}' if error else ''), flush=True)
    return results


def main():
    from files import FILE_SCENARIOS
    from network import WIRE_SCENARIOS
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('engines', nargs='*', choices=list(ENGINES))
    parser.add_argument('--provider', action='append', choices=['stalwart', 'fastmail'], help='repeat for both; default Stalwart')
    parser.add_argument('--race', action='store_true', help='build the Go driver with the race detector')
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument('--files', action='store_true', help='only file lifecycle scenarios')
    selection.add_argument('--wire', action='store_true', help='real TLS socket cuts and crashes; disposable Stalwart only')
    parser.add_argument('--scenario', action='append', choices=[s.__name__ for s in SCENARIOS + FILE_SCENARIOS + WIRE_SCENARIOS])
    parser.add_argument('--timeout', type=float, default=120, help='deadline per driver command, seconds')
    parser.add_argument('--report', type=Path, help='write a credential-free JSON report')
    args = parser.parse_args()
    if len(args.engines) > 2:
        parser.error('choose one engine or an ordered pair')
    if args.timeout <= 0:
        parser.error('--timeout must be positive')
    engines = list(dict.fromkeys(args.engines)) or list(ENGINES)
    pairs = [tuple(args.engines)] if len(args.engines) == 2 else list(itertools.product(engines, repeat=2))
    scenarios = ([blobs] + FILE_SCENARIOS) if args.files else SCENARIOS + FILE_SCENARIOS
    if args.wire:
        scenarios = WIRE_SCENARIOS
    if args.scenario:
        scenarios = [s for s in SCENARIOS + FILE_SCENARIOS + WIRE_SCENARIOS if s.__name__ in args.scenario]
    if 'fastmail' in (args.provider or []) and any(s in WIRE_SCENARIOS for s in scenarios):
        parser.error('wire faults are restricted to disposable Stalwart')
    (ROOT / '.build').mkdir(exist_ok=True)
    for engine in engines:
        command = list(ENGINES[engine]['build'])
        if engine == 'go' and args.race:
            binary = str(ROOT / '.build/tandem-device-go-race')
            command[command.index('-o') + 1] = binary
            command.insert(2, '-race')
            ENGINES[engine]['argv'] = [binary]
        subprocess.run(command, cwd=ENGINES[engine]['cwd'], check=True, stdout=subprocess.DEVNULL)
    results = []
    for provider in dict.fromkeys(args.provider or ['stalwart']):
        with ExitStack() as stack:
            if provider == 'stalwart':
                server = stack.enter_context(Stalwart())
                config = dict(host='127.0.0.1', port=server.ports[993], user=server.username, password=server.password, insecure=True)
            else:
                config = fastmail()
            for first, second in pairs:
                results += run(config, provider, first, second, scenarios, args.timeout)
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(dict(date=time.strftime('%Y-%m-%d'), results=results), indent=2) + '\n')
    failed = sum(not r['ok'] for r in results)
    print(f'{len(results) - failed}/{len(results)} scenarios passed; every test folder was checked during cleanup.', flush=True)
    sys.exit(bool(failed))


if __name__ == '__main__':
    main()
