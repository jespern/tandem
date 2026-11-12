"""IMAP binding probes: the assumptions of DESIGN §10.3 checked against a real server."""

import imaplib
import time

from probes.common import HubCase, report
from tandemtb import imap
from tandemtb.message import build_message, channel_tag, decode_message, fake_envelope


class ImapBinding(HubCase):

    def put(self, m, n=1):
        return [imap.append(m, build_message(channel_tag(), fake_envelope()), self.folder)[1] for _ in range(n)]

    def test_capabilities(self):
        """§10.3: UIDPLUS and IDLE are required; CONDSTORE and QRESYNC recommended."""
        m = self.imap(select=False)
        caps = set(m.capabilities)
        for required in ("UIDPLUS", "IDLE"):
            self.assertIn(required, caps)
        report(self, condstore="CONDSTORE" in caps, qresync="QRESYNC" in caps,
               namespace="NAMESPACE" in caps, move="MOVE" in caps, imap4rev2="IMAP4REV2" in caps)

    def test_namespace_prefix(self):
        """§10.3: the folder lives under the personal namespace prefix."""
        m = self.imap(select=False)
        data = imap.ok(m.namespace())
        report(self, namespace=data[0].decode())

    def test_create_existing_is_alreadyexists(self):
        """§10.3: CREATE of an existing folder answers ALREADYEXISTS, which counts as success."""
        m = self.imap(select=False)
        typ, data = m.create(self.folder)
        report(self, response=(typ, data[0].decode() if data and data[0] else None))
        self.assertTrue(typ == "OK" or b"ALREADYEXISTS" in (data[0] or b""))

    def test_append_returns_appenduid(self):
        m = self.imap()
        uidvalidity, uid = imap.append(m, build_message(channel_tag(), fake_envelope()), self.folder)
        self.assertGreater(uid, 0)
        self.assertGreater(uidvalidity, 0)

    def test_uid_fetch_star_returns_last(self):
        """§10.3: `UID FETCH n:*` returns the last message even when n is past it."""
        m = self.imap()
        uids = self.put(m, 3)
        returned = imap.fetch_uids_from(m, max(uids) + 1)
        report(self, asked_from=max(uids) + 1, returned=returned)
        self.assertEqual([u for u in returned if u > max(uids)], [], "filtering below the range start leaves nothing new")

    def test_uid_expunge_is_targeted(self):
        """§10.3: UID EXPUNGE removes only the named UIDs, even if others are \\Deleted."""
        m = self.imap()
        a, b = self.put(m, 2)
        imap.ok(m.uid("STORE", str(b), "+FLAGS.SILENT", "(\\Deleted)"))   # someone else's pending delete
        imap.destroy(m, [a])
        remaining = imap.uids(m)
        self.assertNotIn(a, remaining)
        self.assertIn(b, remaining)

    def test_qresync_reports_vanished(self):
        """§10.3: with QRESYNC, re-selecting reports expunged UIDs as VANISHED (EARLIER)."""
        m = self.imap(select=False)
        if "QRESYNC" not in m.capabilities:
            self.skipTest("server lacks QRESYNC")
        imap.ok(m.enable("QRESYNC"))
        imap.ok(m.select(self.folder))
        gone, = self.put(m, 1)
        st = imap.status(m, self.folder)
        other = self.imap()
        imap.destroy(other, [gone])
        imap.ok(m.unselect() if hasattr(m, "unselect") else m.close())
        typ, data = m._simple_command("SELECT", self.folder, f"(QRESYNC ({st['UIDVALIDITY']} {st['HIGHESTMODSEQ']}))")
        self.assertEqual(typ, "OK", data)
        m.state = "SELECTED"
        vanished = m.untagged_responses.get("VANISHED", [])
        report(self, vanished=[v.decode() for v in vanished])
        self.assertTrue(any(str(gone).encode() in v for v in vanished))

    def test_idle_sees_append_and_expunge(self):
        """§10.3 watch: an IDLEing connection hears EXISTS for appends and EXPUNGE for deletes."""
        watcher, writer = self.imap(), self.imap()
        uid, = self.put(writer, 1)
        heard = set()
        with watcher.idle(duration=10) as idler:
            time.sleep(0.5)
            self.put(writer, 1)
            imap.destroy(writer, [uid])
            for typ, _ in idler:
                heard.add(typ)
                if "EXISTS" in heard and heard & {"EXPUNGE", "VANISHED"}:
                    break
        report(self, heard=sorted(heard))
        self.assertIn("EXISTS", heard)
        self.assertTrue(heard & {"EXPUNGE", "VANISHED"})

    def test_uidvalidity_stable(self):
        first = imap.status(self.imap(select=False), self.folder)["UIDVALIDITY"]
        second = imap.status(self.imap(select=False), self.folder)["UIDVALIDITY"]
        self.assertEqual(first, second)

    def test_connection_cap(self):
        """§10.3: servers cap concurrent connections; every device holds one for IDLE."""
        opened = []
        cap = None
        try:
            for n in range(1, 41):
                try:
                    opened.append(self.imap_connect_cls())
                except (imaplib.IMAP4.error, OSError) as e:
                    cap = (n - 1, str(e)[:80])
                    break
        finally:
            for m in opened:
                try:
                    m.logout()
                except Exception:
                    pass
        report(self, accepted_before_refusal=cap[0] if cap else ">=40", error=cap[1] if cap else None)

    def test_roundtrip_bytes(self):
        """§10.4: what an IMAP append stores decodes back to the same envelope."""
        m = self.imap()
        channel, envelope = channel_tag(), fake_envelope(5000)
        _, uid = imap.append(m, build_message(channel, envelope), self.folder)
        self.assertEqual(decode_message(imap.fetch_raw(m, uid)), (channel, envelope))
