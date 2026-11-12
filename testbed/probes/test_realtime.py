"""Real-time probes: does the server tell watchers about every kind of change,
how quickly, and can a watcher catch up after a gap? (DESIGN §10.2 and §10.3
"watch"; scenarios/realtime.md.) Binding level only: no Tandem semantics.
"""

import json
import queue
import statistics
import threading
import time

from probes.common import HubCase, report, testbed_only
from tandemtb import imap, stalwart
from tandemtb.message import build_message, channel_tag, fake_envelope, jmap_create

WATCHERS = 5
BUDGET_S = 1.0          # realtime.md: p95 on the local testbed
SETTLE_S = 1.5          # gap between operations, so events can be attributed


def summary(latencies):
    xs = sorted(latencies)
    return {"n": len(xs), "p50_ms": round(statistics.median(xs) * 1000), "max_ms": round(xs[-1] * 1000)}


class JmapWatchers:
    """N EventSource connections, each pushing (watcher, monotonic time) on every Email StateChange."""

    def __init__(self, case, n):
        self.events, self.stop = queue.Queue(), threading.Event()
        self.clients = [case.jmap() for _ in range(n)]
        for i, client in enumerate(self.clients):
            threading.Thread(target=self._run, args=(i, client), daemon=True).start()
        time.sleep(1.0)                                   # let every stream open

    def _run(self, i, client):
        try:
            for event, data in client.event_source(types="Email", ping=30, read_timeout=90):
                if self.stop.is_set():
                    return
                if event == "state" and "Email" in data:
                    self.events.put((i, time.monotonic()))
        except Exception as e:
            self.events.put((i, e))

    def latencies(self, since, n, timeout=5.0):
        """First event per watcher after `since` (the start of the write); returns {watcher: seconds}."""
        got, deadline = {}, time.monotonic() + timeout
        while len(got) < n and time.monotonic() < deadline:
            try:
                i, t = self.events.get(timeout=0.2)
            except queue.Empty:
                continue
            if isinstance(t, Exception):
                raise t
            if t >= since and i not in got:
                got[i] = t - since
        return got

    def drain(self):
        while not self.events.empty():
            i, t = self.events.get_nowait()
            if isinstance(t, Exception):
                raise t

    def close(self):
        self.stop.set()


class ImapWatchers:
    """N IDLEing IMAP connections, each pushing (watcher, response type, time)."""

    def __init__(self, case, n, duration):
        self.events = queue.Queue()
        self.conns = []
        for _ in range(n):
            m = case.imap_connect_cls()
            imap.ensure_folder(m, case.folder)
            imap.ok(m.select(case.folder))
            self.conns.append(m)
        for i, m in enumerate(self.conns):
            threading.Thread(target=self._run, args=(i, m, duration), daemon=True).start()
        time.sleep(1.0)

    def _run(self, i, m, duration):
        try:
            with m.idle(duration=duration) as idler:
                for typ, _ in idler:
                    self.events.put((i, typ, time.monotonic()))
        except Exception as e:
            self.events.put((i, "exception", e))

    def latencies(self, since, kind, n, timeout=5.0):
        got, deadline = {}, time.monotonic() + timeout
        while len(got) < n and time.monotonic() < deadline:
            try:
                i, typ, t = self.events.get(timeout=0.2)
            except queue.Empty:
                continue
            if typ == "exception":
                raise t
            if typ in kind and t >= since and i not in got:
                got[i] = t - since
        return got


class RealtimeJmap(HubCase):

    def setUp(self):
        self.writer = self.jmap()
        self.box = self.ensure_folder_jmap(self.writer)
        self.inbox = [b["id"] for b in self.writer.call("Mailbox/get", {"ids": None})["list"] if b.get("role") == "inbox"][0]

    def test_every_change_reaches_every_watcher(self):
        """Create, flag, move out, move back and destroy each reach all watchers within budget."""
        watchers = JmapWatchers(self, WATCHERS)
        self.addCleanup(watchers.close)
        email_id = None
        ops = {
            "create": lambda: self.writer.call("Email/set", {"create": {"m": jmap_create(self.box, channel_tag(), fake_envelope())}}),
            "flag": lambda: self.writer.call("Email/set", {"update": {email_id: {"keywords/$flagged": True}}}),
            "move-out": lambda: self.writer.call("Email/set", {"update": {email_id: {"mailboxIds": {self.inbox: True}}}}),
            "move-back": lambda: self.writer.call("Email/set", {"update": {email_id: {"mailboxIds": {self.box: True}}}}),
            "destroy": lambda: self.writer.call("Email/set", {"destroy": [email_id]}),
        }
        all_latencies, per_op = [], {}
        for name, op in ops.items():
            watchers.drain()
            done = time.monotonic()                       # start of the write: push can beat its response
            result = op()
            if name == "create":
                email_id = result["created"]["m"]["id"]
            got = watchers.latencies(done, WATCHERS)
            self.assertEqual(len(got), WATCHERS, f"{name}: only {sorted(got)} of {WATCHERS} watchers notified")
            per_op[name] = summary(got.values())
            all_latencies += got.values()
            time.sleep(SETTLE_S)
        report(self, **{k: f"p50 {v['p50_ms']} ms, max {v['max_ms']} ms" for k, v in per_op.items()})
        self.assertLess(max(all_latencies), BUDGET_S)

    def test_imap_writer_reaches_jmap_watchers(self):
        watchers = JmapWatchers(self, WATCHERS)
        self.addCleanup(watchers.close)
        m = self.imap()
        latencies = []
        for _ in range(3):
            watchers.drain()
            done = time.monotonic()
            imap.append(m, build_message(channel_tag(), fake_envelope()), self.folder)
            got = watchers.latencies(done, WATCHERS)
            self.assertEqual(len(got), WATCHERS)
            latencies += got.values()
            time.sleep(SETTLE_S)
        report(self, **summary(latencies))
        self.assertLess(max(latencies), BUDGET_S)

    def test_catch_up_after_disconnect(self):
        """A watcher that was away resumes from its stored state with no gap."""
        watcher = self.jmap()
        since = watcher.call("Email/get", {"ids": []})["state"]
        made = []
        for _ in range(10):
            res = self.writer.call("Email/set", {"create": {"m": jmap_create(self.box, channel_tag(), fake_envelope())}})
            made.append(res["created"]["m"]["id"])
        self.writer.call("Email/set", {"destroy": made[:3]})
        created, destroyed = set(), set()
        while True:
            res = watcher.call("Email/changes", {"sinceState": since, "maxChanges": 4})
            created |= set(res["created"]); destroyed |= set(res["destroyed"])
            since = res["newState"]
            if not res["hasMoreChanges"]:
                break
        visible = (created - destroyed) | (created & destroyed)
        self.assertTrue(set(made[3:]) <= created, "missed creations")
        self.assertTrue(set(made[:3]) <= destroyed | (set(made[:3]) - created), "missed destructions")
        report(self, created=len(created), destroyed=len(destroyed), visible=len(visible))


class RealtimeImap(HubCase):

    def test_every_change_reaches_every_watcher(self):
        """Append, flag change and expunge each reach all IDLEing watchers within budget."""
        watchers = ImapWatchers(self, WATCHERS, duration=20)
        writer = self.imap()
        per_op, all_latencies = {}, []
        steps = [("append", ("EXISTS",)), ("flag", ("FETCH",)), ("expunge", ("EXPUNGE", "VANISHED"))]
        uid = None
        for name, kinds in steps:
            done = time.monotonic()
            if name == "append":
                uid = imap.append(writer, build_message(channel_tag(), fake_envelope()), self.folder)[1]
            elif name == "flag":
                imap.ok(writer.uid("STORE", str(uid), "+FLAGS", "(\\Flagged)"))
            else:
                imap.destroy(writer, [uid])
            got = watchers.latencies(done, kinds, WATCHERS)
            self.assertEqual(len(got), WATCHERS, f"{name}: only {sorted(got)} of {WATCHERS} watchers notified")
            per_op[name] = summary(got.values())
            all_latencies += got.values()
            time.sleep(SETTLE_S)
        report(self, **{k: f"p50 {v['p50_ms']} ms, max {v['max_ms']} ms" for k, v in per_op.items()})
        self.assertLess(max(all_latencies), BUDGET_S)

    def test_jmap_writer_reaches_imap_watchers(self):
        watchers = ImapWatchers(self, WATCHERS, duration=15)
        client = self.jmap()
        box = self.ensure_folder_jmap(client)
        latencies = []
        for _ in range(3):
            done = time.monotonic()
            client.call("Email/set", {"create": {"m": jmap_create(box, channel_tag(), fake_envelope())}})
            got = watchers.latencies(done, ("EXISTS",), WATCHERS)
            self.assertEqual(len(got), WATCHERS)
            latencies += got.values()
            time.sleep(SETTLE_S)
        report(self, **summary(latencies))
        self.assertLess(max(latencies), BUDGET_S)


@testbed_only
class RealtimeRestart(HubCase):
    """`hub: restart` (realtime.md RT-013): do change cursors survive a server restart?"""

    def test_cursors_survive_restart(self):
        client = self.jmap()
        box = self.ensure_folder_jmap(client)
        client.call("Email/set", {"create": {"m": jmap_create(box, channel_tag(), fake_envelope())}})
        state = client.call("Email/get", {"ids": []})["state"]
        m = self.imap(select=False)
        before = imap.status(m, self.folder)
        m.logout()

        stalwart.compose("restart", "stalwart")
        stalwart.wait_http(stalwart.jmap_base() + "/healthz/live")

        client = self.jmap()
        made = client.call("Email/set", {"create": {"m": jmap_create(box, channel_tag(), fake_envelope())}})["created"]["m"]["id"]
        res = client.call("Email/changes", {"sinceState": state})
        after = imap.status(self.imap(select=False), self.folder)
        report(self, jmap_state_ok=made in res["created"],
               uidvalidity_same=before["UIDVALIDITY"] == after["UIDVALIDITY"],
               modseq_grew=after["HIGHESTMODSEQ"] > before["HIGHESTMODSEQ"])
        self.assertIn(made, res["created"], "JMAP state from before the restart no longer works")
        self.assertEqual(before["UIDVALIDITY"], after["UIDVALIDITY"], "UIDVALIDITY changed: full resync")
