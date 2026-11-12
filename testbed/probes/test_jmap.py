"""JMAP binding probes: the assumptions of DESIGN §10.2 checked against a real server."""

import json
import queue
import threading
import time

from probes.common import HubCase, report
from tandemtb.jmap import JmapError
from tandemtb.message import channel_tag, decode_text, fake_envelope, jmap_create, split_message


class JmapBinding(HubCase):

    def setUp(self):
        self.client = self.jmap()
        self.box = self.ensure_folder_jmap(self.client)

    def append(self, envelope=None, channel=None):
        envelope = envelope or fake_envelope()
        channel = channel or channel_tag()
        res = self.client.call("Email/set", {"create": {"m": jmap_create(self.box, channel, envelope)}})
        self.assertIn("m", res.get("created", {}), res)
        return res["created"]["m"]["id"], channel, envelope

    def state(self):
        return self.client.call("Email/get", {"ids": []})["state"]

    def changes(self, since, max_changes=None):
        """All changes since a state, following hasMoreChanges (§10.2)."""
        created, updated, destroyed = [], [], []
        while True:
            args = {"sinceState": since}
            if max_changes:
                args["maxChanges"] = max_changes
            res = self.client.call("Email/changes", args)
            created += res["created"]; updated += res["updated"]; destroyed += res["destroyed"]
            since = res["newState"]
            if not res["hasMoreChanges"]:
                return created, updated, destroyed, since

    # -- session -------------------------------------------------------------

    def test_session_limits(self):
        s = self.client.session()
        core = s["capabilities"]["urn:ietf:params:jmap:core"]
        for key in ("maxObjectsInSet", "maxSizeUpload", "maxCallsInRequest", "maxObjectsInGet"):
            self.assertIn(key, core)
        self.assertIn("eventSourceUrl", s, "§10.2 watch needs EventSource")
        report(self, maxObjectsInSet=core["maxObjectsInSet"], maxSizeUpload=core["maxSizeUpload"],
               maxCallsInRequest=core["maxCallsInRequest"], maxSizeRequest=core.get("maxSizeRequest"))

    def test_wrong_password_rejected(self):
        bad = dict(self.hub["jmap"], password="wrong-" + (self.hub["jmap"].get("password") or ""))
        if "token" in bad:
            bad["token"] = "invalid"
        with self.assertRaises(JmapError) as ctx:
            self.jmap({"jmap": bad}).session()
        self.assertEqual(ctx.exception.status, 401)

    # -- ensureFolder -----------------------------------------------------------

    def test_folder_filter_matches_substrings(self):
        """§10.2: `Mailbox/query` name filters match substrings, so lookup must pick the exact name."""
        decoys = [self.folder + "-old", "My" + self.folder]
        made = self.client.call("Mailbox/set", {"create": {f"d{i}": {"name": n, "parentId": None}
                                                           for i, n in enumerate(decoys)}})
        self.addCleanup(lambda: self.client.call("Mailbox/set", {"destroy": [v["id"] for v in made.get("created", {}).values()],
                                                                 "onDestroyRemoveEmails": True}))
        ids = self.client.call("Mailbox/query", {"filter": {"name": self.folder}})["ids"]
        report(self, matches=len(ids))
        self.assertGreaterEqual(len(ids), 1)
        self.assertEqual(self.find_folder(self.client), self.box)

    def test_duplicate_sibling_name_rejected(self):
        """§10.2: sibling names are unique, so a racing create fails and the client queries again."""
        res = self.client.call("Mailbox/set", {"create": {"x": {"name": self.folder, "parentId": None}}})
        self.assertNotIn("x", res.get("created", {}), "server allowed two sibling folders with one name")
        report(self, error=res["notCreated"]["x"]["type"])

    # -- append and read back -------------------------------------------------------

    def test_append_roundtrip(self):
        """§10.2 append + fetch, and Q2: custom headers come back unchanged."""
        email_id, channel, envelope = self.append()
        got = self.client.call("Email/get", {"ids": [email_id], "fetchTextBodyValues": True,
                                             "properties": ["mailboxIds", "keywords", "header:Tandem-Channel:asText",
                                                            "header:Tandem-Version:asText", "bodyValues", "textBody", "blobId"]})["list"][0]
        self.assertEqual(got["header:Tandem-Channel:asText"], channel)
        self.assertEqual(got["header:Tandem-Version:asText"], "1")
        self.assertTrue(got["keywords"].get("$seen"))
        part = got["textBody"][0]["partId"]
        self.assertEqual(decode_text(got["bodyValues"][part]["value"]), envelope)
        headers, _ = split_message(self.client.download(got["blobId"], "m.eml", "message/rfc822"))
        report(self, content_type=headers.get("content-type"), transfer_encoding=headers.get("content-transfer-encoding"))

    def test_append_batch(self):
        """§10.2: appends batch up to maxObjectsInSet."""
        limit = self.client.core_capability("maxObjectsInSet")
        n = min(limit, 200)
        creates = {f"m{i}": jmap_create(self.box, channel_tag(), fake_envelope()) for i in range(n)}
        started = time.monotonic()
        res = self.client.call("Email/set", {"create": creates})
        self.assertEqual(len(res.get("created", {})), n, res.get("notCreated"))
        report(self, batch=n, seconds=round(time.monotonic() - started, 2))

    def test_message_near_size_limit(self):
        """§10.6: a message MUST stay under 1 MiB; one just under it is accepted."""
        envelope = fake_envelope(700 * 1024)            # ~950 KiB once base64-wrapped
        self.append(envelope)

    # -- changes ------------------------------------------------------------------

    def test_changes_created_updated_destroyed(self):
        """§10.2: a message moved out of the folder is an update, not a destroy."""
        since = self.state()
        email_id, _, _ = self.append()
        created, _, _, since = self.changes(since)
        self.assertIn(email_id, created)

        inbox = [b["id"] for b in self.client.call("Mailbox/get", {"ids": None})["list"] if b.get("role") == "inbox"][0]
        self.client.call("Email/set", {"update": {email_id: {"mailboxIds": {inbox: True}}}})
        created, updated, destroyed, since = self.changes(since)
        self.assertIn(email_id, updated, "a move must show as an update")
        self.assertNotIn(email_id, destroyed)
        boxes = self.client.call("Email/get", {"ids": [email_id], "properties": ["mailboxIds"]})["list"][0]["mailboxIds"]
        self.assertNotIn(self.box, boxes)

        self.client.call("Email/set", {"destroy": [email_id]})
        _, _, destroyed, _ = self.changes(since)
        self.assertIn(email_id, destroyed)

    def test_changes_paging(self):
        """§10.2: repeat Email/changes while hasMoreChanges."""
        since = self.state()
        ids = [self.append()[0] for _ in range(25)]
        created, _, _, _ = self.changes(since, max_changes=10)
        self.assertEqual(sorted(set(created) & set(ids)), sorted(ids))

    def test_unusable_state(self):
        """§10.2 names only `cannotCalculateChanges` as the full-resync trigger.
        Servers also answer `invalidArguments`, or parse a prefix of a garbled
        state and answer as if it were valid. Records what this server does."""
        outcomes = {}
        for state in ("", "zzzzzzzz", "not-a-real-state"):
            try:
                res = self.client.call("Email/changes", {"sinceState": state})
                outcomes[state] = f"accepted (oldState={res['oldState']!r})"
            except JmapError as e:
                outcomes[state] = e.kind
        report(self, **{repr(k): v for k, v in outcomes.items()})
        errors = {v for v in outcomes.values() if not v.startswith("accepted")}
        self.assertTrue(errors <= {"cannotCalculateChanges", "invalidArguments"}, outcomes)

    # -- push -------------------------------------------------------------------------

    def _listen(self, events, ping, stop):
        try:
            for event, data in self.client.event_source(types="Email", closeafter="no", ping=ping, read_timeout=ping * 3):
                events.put((event, data))
                if stop.is_set():
                    return
        except Exception as e:                             # surfaced to the test thread
            events.put(("exception", repr(e)))

    def test_eventsource_state_change(self):
        """§10.2 watch: an append by another client arrives as a StateChange."""
        events, stop = queue.Queue(), threading.Event()
        threading.Thread(target=self._listen, args=(events, 30, stop), daemon=True).start()
        time.sleep(1)
        other = self.jmap()
        other.call("Email/set", {"create": {"m": jmap_create(self.box, channel_tag(), fake_envelope())}})
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                event, data = events.get(timeout=1)
            except queue.Empty:
                continue
            self.assertNotEqual(event, "exception", data)
            if event == "state" and "Email" in data:
                stop.set()
                report(self, latency_s=round(15 - (deadline - time.monotonic()), 2))
                return
        stop.set()
        self.fail("no StateChange for Email within 15 s")

    def test_eventsource_ping_interval(self):
        """§10.2 watch: pings arrive, and the server may raise the interval asked
        for (RFC 8620 §7.3). The reconnect rule must use the interval the ping
        reports. Slow: waits for the first ping (30 s on Stalwart)."""
        asked = 5
        started = time.monotonic()
        for event, data in self.client.event_source(ping=asked, read_timeout=120):
            if event == "ping":
                interval = json.loads(data).get("interval")
                report(self, asked=asked, reported=interval, first_ping_s=round(time.monotonic() - started, 1))
                self.assertIsNotNone(interval, "ping data should report the interval")
                return
        self.fail("EventSource closed without a ping")
