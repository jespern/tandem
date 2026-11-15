"""Bounded, disposable Stalwart chunk-index measurements with real native devices.

Noise has envelope-shaped, content-addressed bodies, not decryptable files.
A genuine encrypted sentinel file checks correctness alongside index load.
"""
import argparse
import base64
import hashlib
from pathlib import Path
import sqlite3
import subprocess
import sys
import time

from campaign import save
from files import CHUNK, attach, fetch, target, world
from harness import ENGINES, ROOT, check
from interop import run, Stalwart
from providers import Mailbox

sys.path.append(str(ROOT / 'probes'))
from blob_probe import Raw, append_batch, astring


def index_rows(d):
    with sqlite3.connect(d.directory / 'device.sqlite') as db:
        return db.execute('SELECT COUNT(*) FROM chunks').fetchone()[0]


def rss(d):
    return int(subprocess.check_output(['ps', '-o', 'rss=', '-p', str(d.process.pid)], text=True).strip()) * 1024


def scale(messages, batch=250):
    details = dict(noise_messages=messages, batch=batch)

    def scenario(new, hub):
        bob, alice, _, lst = world(new, hub)
        t, data = target(lst), hashlib.shake_256(b'sentinel').digest(CHUNK + 1)
        attach(bob, t, data)
        rows = Mailbox(hub).chunks()
        channel = rows[0]['channel']
        folder = hub['folder'] + '-blobs'
        c = Raw(hub['host'], hub['port'], hub['user'], hub['password'], verify=False)
        started, uploaded = time.monotonic(), 0
        try:
            for start in range(0, messages, batch):
                bodies = []
                for i in range(start, min(messages, start + batch)):
                    envelope = b'TNDM\x02\x04' + hashlib.shake_256(str(i).encode()).digest(112)
                    address = base64.b32encode(hashlib.sha256(envelope).digest()).decode().rstrip('=').lower()
                    raw = (f'From: scale@invalid\r\nSubject: index fixture\r\nTandem-Channel: {channel}\r\nTandem-Blob: {address}\r\n'
                           'Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n').encode()
                    bodies.append(raw + base64.b64encode(envelope) + b'\r\n')
                ids = append_batch(c, b'APPEND ' + astring(folder) + b' (\\Seen) ', bodies, False)
                check('server acknowledged every index fixture', len(ids), len(bodies))
                uploaded += sum(map(len, bodies))
                if (start + len(bodies)) % 10000 == 0:
                    print(f'  scale {start + len(bodies)}/{messages} messages appended', flush=True)
        finally:
            c.f.close(); c.s.close()
        details.update(append_seconds=round(time.monotonic() - started, 3), message_bytes=uploaded)
        expected = messages + len(rows)
        started = time.monotonic()
        alice.restart(); alice('sync'); alice('settleBlobs')
        details['cold_recovery_seconds'] = round(time.monotonic() - started, 3)
        check('full durable chunk index', index_rows(alice), expected)
        details['index_rows'] = index_rows(alice)
        details['resident_bytes_after_scan'] = rss(alice)
        details['database_bytes'] = sum(p.stat().st_size for p in alice.directory.glob('device.sqlite*') if p.is_file())
        fetch(alice, t, data)
        started = time.monotonic()
        alice('checkBlobs')
        details['warm_check_seconds'] = round(time.monotonic() - started, 3)
        # Incremental discovery must remain correct with a large existing index.
        newer = hashlib.shake_256(b'incremental').digest(CHUNK + 13)
        started = time.monotonic()
        attach(bob, target(lst, record=t['record']), newer)
        alice('sync'); alice('settleBlobs'); fetch(alice, t, newer)
        details['replacement_seconds'] = round(time.monotonic() - started, 3)
        bob('delete', list=lst, record=t['record'])
        for d in (bob, alice, bob):
            d('sync'); d('settleBlobs')
        check('deleted sentinel stays absent', alice('blob', **t)['state'], None)
        # Orphans younger than the grace remain; only known obsolete files go.
        with Mailbox(hub).connection() as m:
            m.select('"' + folder + '"', readonly=True)
            remaining = len(m.uid('SEARCH', None, 'ALL')[1][0].split())
        check('cleanup retains young unrelated orphans', remaining, messages)
        return details

    scenario.__name__ = f'file_scale_{messages}'
    scenario.details = details
    return scenario


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--engines', nargs='+', choices=list(ENGINES), default=list(ENGINES))
    parser.add_argument('--messages', nargs='+', type=int, default=[1000, 10000, 100000])
    parser.add_argument('--batch', type=int, default=250)
    parser.add_argument('--timeout', type=float, default=300)
    parser.add_argument('--no-build', action='store_true')
    parser.add_argument('--swift-configuration', choices=['debug', 'release'], default='debug')
    parser.add_argument('--report', type=Path, default=ROOT / '.build/file-scale.json')
    args = parser.parse_args()
    if args.batch < 1 or min(args.messages) < 1 or args.timeout <= 0:
        parser.error('messages, batch and timeout must be positive')
    for engine in args.engines:
        command = list(ENGINES[engine]['build'])
        if engine == 'swift' and args.swift_configuration == 'release':
            command += ['-c', 'release']
            ENGINES[engine]['argv'] = [str(ROOT / '.build/out/Products/Release/tandem-device')]
        if not args.no_build:
            subprocess.run(command, cwd=ENGINES[engine]['cwd'], check=True, stdout=subprocess.DEVNULL)
    results = []
    with Stalwart() as server:
        hub = dict(host='127.0.0.1', port=server.ports[993], user=server.username, password=server.password, insecure=True)
        for engine in args.engines:
            for count in args.messages:
                results += run(hub, 'stalwart', engine, engine, [scale(count, args.batch)], args.timeout)
                save(args.report, results, dict(engines=args.engines, messages=args.messages, batch=args.batch,
                                               swift_configuration=args.swift_configuration))
    return any(not r['ok'] for r in results)


if __name__ == '__main__':
    raise SystemExit(main())
