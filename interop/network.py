"""Real TLS connection failures, distinct from store-boundary error fixtures."""
import os
import sqlite3
from concurrent.futures import ThreadPoolExecutor
import signal

from files import CHUNK, attach, fetch, manifest, target, world
from harness import Failed, Rejected, check, unb64u
from wire_faults import WireProxy


def wire_scenario(kind, discard_partial=False):
    details = dict(fault=kind, discard_partial=discard_partial, evidence=[])

    def scenario(new, hub):
        with WireProxy(hub) as proxy:
            details['evidence'] = proxy.evidence
            bob, alice, _, lst = world(proxy.route(new, hub), hub)
            t, data = target(lst), os.urandom(5 * CHUNK + 19)
            if kind in ('upload', 'append_ack'):
                proxy.arm(kind)
                attach(bob, t, data)
                proxy.assert_fired()
                bob.restart(); bob('sync'); bob('settleBlobs')
            else:
                attach(bob, t, data); alice('sync')
                if kind == 'download':
                    # Both bindings retry an interrupted command once. Cut its
                    # retry too, after the first engine batch is durable.
                    proxy.arm(kind, skip=4, repeat=2)
                    try:
                        fetch(alice, t, data)
                    except Rejected:
                        pass  # The actual cut must also be evidenced below.
                    proxy.assert_fired()
                    with sqlite3.connect(alice.directory / 'device.sqlite') as db:
                        bitmaps = db.execute("SELECT value FROM values_ WHERE name LIKE 'blobs.partial.%'").fetchall()
                    details['partial_bitmaps'] = [bytes(row[0]).hex() for row in bitmaps]
                    check('both the command and its transport retry were cut', len(proxy.evidence), 2)
                    check('download cut preserves four completed chunks', any(bytes(row[0])[0] & 15 == 15 for row in bitmaps), True)
                    if discard_partial:
                        m = manifest(alice, t)
                        partial = alice.directory / ('device.sqlite.blobs/partial/' + unb64u(m['hash']).hex())
                        partial.unlink()  # Deliberately retain the durable bitmap.
                    alice.restart()
                else:
                    alice('run')
                    proxy.arm('idle'); proxy.assert_fired()
                    # Restart explicitly after a surfaced network failure; no
                    # assumption that the engine silently retries its Run loop.
                    alice.restart()
            alice('sync'); fetch(alice, t, data)
            for d in (bob, alice):
                d('sync'); d('settleBlobs')
            check('recovery has one physical message per chunk', bob('chunkCounts', **t)['counts'], [1] * 6)
            details['evidence'] = list(proxy.evidence)
            return details

    scenario.__name__ = 'file_wire_' + kind + ('_missing_partial' if discard_partial else '')
    scenario.details = details
    return scenario


WIRE_SCENARIOS = [wire_scenario(kind) for kind in ('upload', 'append_ack', 'download', 'idle')]
WIRE_SCENARIOS.append(wire_scenario('download', discard_partial=True))


def wire_crash(kind):
    details = dict(fault=kind, evidence=[])

    def scenario(new, hub):
        with WireProxy(hub) as proxy:
            details['evidence'] = proxy.evidence
            bob, alice, _, lst = world(proxy.route(new, hub), hub)
            t, data = target(lst), os.urandom(6 * CHUNK + 19)
            victim = bob
            if kind == 'download':
                attach(bob, t, data); alice('sync')
                victim = alice
            proxy.arm(kind, skip=4 if kind == 'download' else 0)
            with ThreadPoolExecutor(max_workers=1) as executor:
                work = executor.submit(fetch, victim, t, data) if kind == 'download' else executor.submit(attach, victim, t, data)
                if not proxy.fired.wait(victim.timeout):
                    victim.kill()
                    raise Failed('no real socket event reached the crash boundary')
                victim.kill()  # SIGKILL is triggered by the proxy event, never a sleep.
                check('actual abrupt process death', victim.process.returncode, -signal.SIGKILL)
                details['signal'] = 'SIGKILL'
                try:
                    work.result(timeout=10)
                except (Failed, OSError, ValueError):
                    pass  # The intentional death may interrupt the harness's pipe read too.
            victim.restart(); victim('sync'); victim('settleBlobs')
            alice('sync'); fetch(alice, t, data)
            bob('sync'); bob('settleBlobs')
            check('crash recovery has no duplicate chunks', bob('chunkCounts', **t)['counts'], [1] * 7)
            return details

    scenario.__name__ = 'file_wire_crash_' + kind
    scenario.details = details
    return scenario


WIRE_SCENARIOS += [wire_crash(kind) for kind in ('upload', 'append_ack', 'download')]
