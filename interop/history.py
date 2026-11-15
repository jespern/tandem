"""Seeded file histories with a small, independent last-observed-write oracle.

The oracle tracks causal ancestry, not either engine's clocks, dots or merge code.
Each checkpoint checks all siblings and the bytes selected by the public file API.
"""
import hashlib
import json
import random

from files import CHUNK, attach, fetch, manifest, target, world, verified
from harness import b64u, check, unb64u
from providers import Mailbox


class FileModel:
    def __init__(self):
        self.writes = {}
        self.seen = {}
        self.deleted = set()

    def put(self, device, record, data):
        known = self.seen.setdefault(device, set())
        identity = len(self.writes)
        self.writes[identity] = (record, data, set(known))
        known.add(identity)
        return identity

    def sync(self, devices):
        all_seen = set().union(*(self.seen.get(d, set()) for d in devices))
        for d in devices:
            self.seen[d] = set(all_seen)

    def values(self, record):
        if record in self.deleted:
            return []
        candidates = {i for i, w in self.writes.items() if w[0] == record}
        covered = set().union(*(self.writes[i][2] for i in candidates))
        return [self.writes[i][1] for i in sorted(candidates - covered)]


def history(seed, steps=24):
    """Construct a replayable scenario; seed controls choices, record IDs and bytes."""
    details = dict(history_version=3, seed=seed, requested_steps=steps, trace=[], checkpoints=0)

    def scenario(new, hub):
        rng, model = random.Random(seed), FileModel()
        bob, alice, carol, lst = world(new, hub, third=True)
        devices = [bob, alice, carol]
        targets = []

        def note(action, **args):
            details['trace'].append(dict(action=action, **args))

        def data():
            return rng.randbytes(rng.choice([0, 1, 16384, 16385, CHUNK - 1, CHUNK, CHUNK + 1, 3 * CHUNK + 17]))

        def fresh():
            t = target(lst, record=b64u(rng.randbytes(16)), name=rng.choice(['file', 'résumé 📷', 'a/b']))
            targets.append(t)
            return t

        def put(d, t, value, remember=True):
            attach(d, t, value)
            if remember:
                model.put(d.name, t['record'], value)

        def checkpoint():
            # Two finite delivery passes: late-write reissues from the last
            # device in pass one are imported by earlier devices in pass two.
            for _ in range(2):
                for d in devices:
                    d('sync'); d('settleBlobs')
            model.sync([d.name for d in devices])
            required = set()
            for t in targets:
                expected = model.values(t['record'])
                hashes = {b64u(hashlib.sha256(v).digest()) for v in expected}
                for d in devices:
                    values = d('fieldValues', list=lst, record=t['record'], field='_blob:' + t['name'])['values']
                    manifests = [json.loads(unb64u(v)) for v in values]
                    for m in manifests:
                        entries = unb64u(m.get('chunks', ''))
                        required.update(base64_address(entries[i:i + 32]) for i in range(0, len(entries), 68))
                    check(f'{d.name} sibling hashes {t["record"]}', {m['hash'] for m in manifests}, hashes)
                    check(f'{d.name} sibling count', len(manifests), len(expected))
                    if expected:
                        selected = manifest(d, t)
                        value = next(v for v in expected if b64u(hashlib.sha256(v).digest()) == selected['hash'])
                        fetch(d, t, value)
                        counts = d('chunkCounts', **t)['counts']
                        check(f'{d.name} selected file has every chunk', all(n >= 1 for n in counts), True)
                    else:
                        check(f'{d.name} deleted file stays absent', d('blob', **t)['state'], None)
            # Include non-displayed conflict siblings: selected-file fetches
            # alone would miss a collector deleting their physical messages.
            check('every sibling retains its physical chunks', required - Mailbox(hub).claimed_addresses(), set())
            details['checkpoints'] += 1

        for step in range(steps):
            live = [t for t in targets if t['record'] not in model.deleted]
            action = rng.choice(['replace', 'conflict', 'pending', 'cancel', 'unpublished', 'rotate', 'delete', 'restart',
                                 'pending_delete', 'replace_pending', 'parallel_files', 'healing', 'removed_uploader']) if live else 'replace'
            d = rng.choice(devices)
            t = rng.choice(live) if live and rng.random() < .75 else fresh()
            note(action, step=step, device=d.name, record=t['record'], name=t['name'])
            if action == 'replace':
                put(d, t, data())
            elif action == 'conflict':
                writers = rng.sample(devices, rng.choice([2, 3]))
                for writer in writers:
                    writer('offline', value=True)
                for writer in writers:
                    put(writer, t, data())
                for writer in writers:
                    writer('offline', value=False)
            elif action in ('pending', 'cancel'):
                value = rng.randbytes((3 + rng.randrange(3)) * CHUNK + 1)
                d('blobFault', after=rng.randrange(3), lostAck=bool(rng.randrange(2)))
                put(d, t, value, remember=False)
                # An unrelated write must not consume or publish the pending file.
                d('set', list=lst, record=t['record'], field='title', value=b64u(b'during upload'))
                if action == 'cancel':
                    d('cancelAttach', **t); d('clearBlobFault')
                else:
                    model.put(d.name, t['record'], value)
                    d.restart(); d('settleBlobs')
            elif action == 'unpublished':
                d('offline', value=True)
                put(d, t, data())
                d.restart(); d('sync'); d('settleBlobs')
            elif action == 'rotate':
                # Keep a pending write's original ancestry while a peer publishes
                # unseen work and the owner starts a new epoch.
                value = rng.randbytes(2 * CHUNK + 7)
                d('blobFault', after=0)
                put(d, t, value, remember=False)
                model.put(d.name, t['record'], value)
                peer = rng.choice([p for p in devices if p != d])
                put(peer, t, data())
                bob('sync'); bob('rekey', list=lst)
                d.restart(); d('sync'); d('settleBlobs')
            elif action == 'delete':
                d('delete', list=lst, record=t['record'])
                model.deleted.add(t['record'])
            elif action == 'restart':
                d.restart()
            elif action == 'pending_delete':
                d('blobFault', after=1)
                put(d, t, rng.randbytes(3 * CHUNK + 1), remember=False)
                peer = rng.choice([p for p in devices if p != d])
                peer('delete', list=lst, record=t['record'])
                model.deleted.add(t['record'])
                d.restart(); d('sync'); d('settleBlobs')
            elif action == 'replace_pending':
                d('blobFault', after=1)
                put(d, t, rng.randbytes(4 * CHUNK + 1), remember=False)
                value = rng.randbytes(3 * CHUNK + 7)
                put(d, t, value, remember=False)
                model.put(d.name, t['record'], value)
                d.restart(); d('settleBlobs')
            elif action == 'parallel_files':
                d('blobFault', after=1)
                for _ in range(rng.randrange(2, 5)):
                    other, value = fresh(), rng.randbytes(2 * CHUNK + 3)
                    put(d, other, value, remember=False)
                    model.put(d.name, other['record'], value)
                d.restart(); d('settleBlobs')
            elif action == 'healing':
                values = model.values(t['record'])
                if len(values) == 1 and len(values[0]) > 16384:
                    m = manifest(d, t)
                    address = base64_address(unb64u(m['chunks'])[:32])
                    Mailbox(hub).destroy([r['uid'] for r in verified(hub) if r['address'] == address])
                    d.restart(); d('checkBlobs')
            elif action == 'removed_uploader':
                member = rng.choice([alice, carol])
                member('blobFault', after=1)
                put(member, t, rng.randbytes(3 * CHUNK + 1), remember=False)
                participant = member('me')['participant']
                bob('removeMember', list=lst, participant=participant)
                # A stale offline device cannot know it was removed. Deliver
                # revocation before clearing its blocked upload/restarting.
                member('sync'); member('settleBlobs'); member.restart()
                check('removed uploader loses its writing role', member('role', list=lst)['role'], None)
                bob('share', list=lst, participant=participant, role='writer')
                member('sync')
                if any(o['list'] == lst for o in member('offers')['offers']):
                    member('accept', list=lst)
            checkpoint()

        # A newly admitted installation must recover from provider state alone,
        # rather than benefiting from any participant's existing SQLite/cache.
        note('fresh_reader')
        fresh_device = new('b', 'Fresh reader')
        fresh_device('join', link=bob('invite', label='Fresh reader')['link'], label='Fresh reader')
        bob('sync')
        bob('share', list=lst, participant=fresh_device('me')['participant'], role='reader')
        fresh_device('sync'); fresh_device('accept', list=lst)
        fresh_device('useBlobs', chunkSize=CHUNK, connections=1)
        devices.append(fresh_device)
        checkpoint()
        rows = Mailbox(hub).chunks()
        check('final physical bodies hash to all claimed addresses', all(r['verified'] for r in rows), True)
        details.update(oracle_writes=len(model.writes), records=len(targets), final_chunk_messages=len(rows))
        return details

    scenario.__name__ = f'file_history_{seed}'
    scenario.details = details
    return scenario


def base64_address(address):
    import base64
    return base64.b32encode(address).decode().rstrip('=').lower()
