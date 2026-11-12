"""Fault-injection probes: the testbed's Toxiproxy layer behaves as scenarios assume.

These check the tools, not a protocol implementation: that each fault really
produces the failure a scenario names (§10.2 watch, §10.3, faults.md).
"""

import http.client
import imaplib
import secrets
import socket
import time

from probes.common import HubCase, report, testbed_only
from tandemtb import imap, stalwart, toxi
from tandemtb.jmap import Jmap, JmapError
from tandemtb.message import build_message, channel_tag, decode_message, fake_envelope, jmap_create


@testbed_only
class Faults(HubCase):

    def setUp(self):
        self.device = "probe-" + secrets.token_hex(3)
        self.via = toxi.device_descriptor(self.device, self.hub["name"])
        self.addCleanup(toxi.remove, self.device)

    def proxied_jmap(self, timeout=10):
        j = self.via["jmap"]
        return Jmap(j["session"], j["user"], j["password"], timeout=timeout)

    def proxied_imap(self, timeout=10):
        c = self.via["imap"]
        return imap.connect(c["host"], c["port"], c["user"], c["password"], timeout=timeout)

    def test_proxy_passes_traffic(self):
        self.assertIn("apiUrl", self.proxied_jmap().session())
        m = self.proxied_imap()
        m.logout()

    def test_latency(self):
        toxi.latency(self.device, 400)
        started = time.monotonic()
        self.proxied_jmap().session()
        elapsed = time.monotonic() - started
        report(self, seconds=round(elapsed, 2))
        self.assertGreater(elapsed, 0.4)

    def test_partition_and_heal(self):
        toxi.partition(self.device)
        with self.assertRaises((OSError, JmapError, imaplib.IMAP4.abort)):
            self.proxied_jmap(timeout=3).session()
        toxi.heal(self.device)
        self.assertIn("apiUrl", self.proxied_jmap().session())

    def test_append_lands_but_response_is_lost(self):
        """The `appended but never got the response` case (faults.md): the request
        reaches the server whole, the response is cut, the message is stored."""
        client = self.proxied_jmap()
        box = self.ensure_folder_jmap(client)
        channel, envelope = channel_tag(), fake_envelope(2000)
        toxi.cut_after(self.device, 0, stream="downstream", bindings=["jmap"])
        with self.assertRaises((OSError, JmapError, http.client.HTTPException)):
            client.call("Email/set", {"create": {"m": jmap_create(box, channel, envelope)}})
        toxi.heal(self.device)
        time.sleep(0.5)
        direct = self.imap()
        found = [decode_message(imap.fetch_raw(direct, u)) for u in imap.uids(direct)]
        report(self, stored=(channel, envelope) in found)
        self.assertIn((channel, envelope), found)

    def test_blackhole_silences_idle(self):
        """A dead NAT mapping: the connection stays open and IDLE hears nothing."""
        m = self.proxied_imap(timeout=3)
        imap.ensure_folder(m, self.folder)
        imap.ok(m.select(self.folder))
        toxi.blackhole(self.device, bindings=["imap"])
        writer = self.imap()
        heard = []
        try:
            with m.idle(duration=4) as idler:
                imap.append(writer, build_message(channel_tag(), fake_envelope()), self.folder)
                for typ, _ in idler:
                    heard.append(typ)
        except (socket.timeout, TimeoutError, imaplib.IMAP4.abort, OSError):
            pass
        report(self, heard=heard)
        self.assertNotIn("EXISTS", heard)

    def test_eventsource_ping_detects_blackhole(self):
        """§10.2 watch: with pings, a blackholed EventSource times out instead of hanging."""
        client = self.proxied_jmap()
        client.session()
        toxi.blackhole(self.device, bindings=["jmap"])
        started = time.monotonic()
        with self.assertRaises((TimeoutError, socket.timeout, OSError, JmapError)):
            for _ in client.event_source(ping=2, read_timeout=5):
                pass
        report(self, detected_after_s=round(time.monotonic() - started, 1))

    def test_password_change_rejects_old_credentials(self):
        """`hub: reject auth`: after a password change, old credentials fail on both bindings."""
        old = stalwart.hub(self.hub["name"])
        stalwart.set_password(self.hub["name"])
        with self.assertRaises(JmapError):
            self.jmap(old).session()
        with self.assertRaises(imaplib.IMAP4.error):
            c = old["imap"]
            imap.connect(c["host"], c["port"], c["user"], c["password"])
        type(self).hub = stalwart.hub(self.hub["name"])
