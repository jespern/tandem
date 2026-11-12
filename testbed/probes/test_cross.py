"""Mixed-binding probes: one account, a JMAP client and an IMAP client (DESIGN §4, §10.4)."""

from probes.common import HubCase, report
from tandemtb import imap
from tandemtb.message import (build_message, channel_tag, decode_message, decode_text, envelope_id,
                              fake_envelope, jmap_create, reencode_message)


class CrossBinding(HubCase):

    def setUp(self):
        self.client = self.jmap()
        self.box = self.ensure_folder_jmap(self.client)
        self.m = self.imap()

    def jmap_read(self, email_id):
        got = self.client.call("Email/get", {"ids": [email_id], "fetchTextBodyValues": True,
                                             "properties": ["header:Tandem-Channel:asText", "bodyValues", "textBody", "mailboxIds"]})["list"][0]
        return got["header:Tandem-Channel:asText"].strip(), decode_text(got["bodyValues"][got["textBody"][0]["partId"]]["value"])

    def imap_uid_by_envelope(self, envelope):
        for uid in imap.uids(self.m):
            if decode_message(imap.fetch_raw(self.m, uid))[1] == envelope:
                return uid
        return None

    def test_jmap_append_reads_over_imap(self):
        channel, envelope = channel_tag(), fake_envelope(3000)
        self.client.call("Email/set", {"create": {"m": jmap_create(self.box, channel, envelope)}})
        self.m.noop()
        uid = self.imap_uid_by_envelope(envelope)
        self.assertIsNotNone(uid, "JMAP append not visible over IMAP")
        self.assertEqual(decode_message(imap.fetch_raw(self.m, uid))[0], channel)

    def test_imap_append_reads_over_jmap(self):
        since = self.client.call("Email/get", {"ids": []})["state"]
        channel, envelope = channel_tag(), fake_envelope(3000)
        imap.append(self.m, build_message(channel, envelope), self.folder)
        created = self.client.call("Email/changes", {"sinceState": since})["created"]
        self.assertEqual(len(created), 1)
        self.assertEqual(self.jmap_read(created[0]), (channel, envelope))

    def test_identity_is_envelope_hash(self):
        """§10.4: H(envelope) is the same whichever binding stored or reads the message."""
        envelope = fake_envelope()
        res = self.client.call("Email/set", {"create": {"m": jmap_create(self.box, channel_tag(), envelope)}})
        via_jmap = envelope_id(self.jmap_read(res["created"]["m"]["id"])[1])
        via_imap = envelope_id(decode_message(imap.fetch_raw(self.m, self.imap_uid_by_envelope(envelope)))[1])
        self.assertEqual(via_jmap, via_imap)

    def test_other_charsets_decode(self):
        """§10.4: bodies stored in UTF-16 or quoted-printable still decode in both bindings."""
        for charset, cte in (("UTF-16", "base64"), ("UTF-8", "quoted-printable"), ("UTF-16LE", "base64")):
            with self.subTest(charset=charset, cte=cte):
                channel, envelope = channel_tag(), fake_envelope(400)
                _, uid = imap.append(self.m, build_message(channel, envelope), self.folder)
                raw = imap.fetch_raw(self.m, uid)
                new_uid = imap.append(self.m, reencode_message(raw, charset, cte), self.folder)[1]
                imap.destroy(self.m, [uid])
                self.assertEqual(decode_message(imap.fetch_raw(self.m, new_uid)), (channel, envelope),
                                 "IMAP three-step decode")
                self.assertEqual(self.jmap_by_channel(channel), (channel, envelope), "JMAP bodyValues decode")

    def test_unrecognised_charset_label(self):
        """§10.4 gap: a charset label the server doesn't recognise (here `utf-16-le`,
        not the IANA `UTF-16LE`) comes back from JMAP undecoded, flagged
        `isEncodingProblem`. A JMAP reader must check the flag and fall back to
        the raw message and the three-step decode."""
        channel, envelope = channel_tag(), fake_envelope(300)
        raw = reencode_message(build_message(channel, envelope), "utf-16-le", "base64")
        imap.append(self.m, raw, self.folder)
        ids = self.client.call("Email/query", {"filter": {"inMailbox": self.box}})["ids"]
        got = self.client.call("Email/get", {"ids": ids, "fetchTextBodyValues": True,
                                             "properties": ["header:Tandem-Channel:asText", "bodyValues", "textBody", "blobId"]})["list"]
        mine, = [g for g in got if (g["header:Tandem-Channel:asText"] or "").strip() == channel]
        value = mine["bodyValues"][mine["textBody"][0]["partId"]]
        report(self, isEncodingProblem=value.get("isEncodingProblem"))
        if value.get("isEncodingProblem"):
            fallback = decode_message(self.client.download(mine["blobId"], "m.eml", "message/rfc822"))
            self.assertEqual(fallback, (channel, envelope), "raw-message fallback must decode")
        else:
            self.assertEqual(decode_text(value["value"]), envelope)

    def jmap_by_channel(self, channel):
        """Find a message by its channel tag without a header filter (Stalwart has none for custom headers)."""
        ids = self.client.call("Email/query", {"filter": {"inMailbox": self.box}})["ids"]
        got = self.client.call("Email/get", {"ids": ids, "properties": ["header:Tandem-Channel:asText"]})["list"]
        match = [g["id"] for g in got if (g["header:Tandem-Channel:asText"] or "").strip() == channel]
        self.assertEqual(len(match), 1, f"{len(match)} messages on channel")
        return self.jmap_read(match[0])

    def test_imap_move_out_is_jmap_update(self):
        """§10.1/§10.2: a message moved out of the folder by a mail client counts as removed."""
        since = self.client.call("Email/get", {"ids": []})["state"]
        _, uid = imap.append(self.m, build_message(channel_tag(), fake_envelope()), self.folder)
        created = self.client.call("Email/changes", {"sinceState": since})
        email_id, since = created["created"][0], created["newState"]
        imap.ok(self.m.uid("MOVE", str(uid), "INBOX"))
        res = self.client.call("Email/changes", {"sinceState": since})
        report(self, created=len(res["created"]), updated=len(res["updated"]), destroyed=len(res["destroyed"]))
        self.assertIn(email_id, res["updated"] + res["destroyed"])
        boxes = self.client.call("Email/get", {"ids": [email_id], "properties": ["mailboxIds"]})["list"]
        self.assertTrue(not boxes or self.box not in boxes[0]["mailboxIds"])
