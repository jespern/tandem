"""Shared fixtures for the binding probes.

On the testbed every probe class gets a fresh hub account, so probes can use the
real folder name `Tandem`. Against an external hub (`tb probe --hub-file`) they
work in a throwaway folder `TandemProbe-<hex>`, deleted afterwards, and never
touch an existing `Tandem` folder.
"""

import json
import os
import secrets
import unittest

from tandemtb import imap, stalwart
from tandemtb.jmap import Jmap

EXTERNAL = os.environ.get("TB_PROBE_HUB")


def report(test, **facts):
    """Facts a probe learned about the server, printed so runs double as a survey."""
    print(f"\n  [{test.id().rsplit('.', 1)[-1]}] " + ", ".join(f"{k}={v!r}" for k, v in facts.items()))


class HubCase(unittest.TestCase):
    """One hub account (or external hub) per test class."""

    @classmethod
    def setUpClass(cls):
        if EXTERNAL:
            with open(EXTERNAL) as f:
                cls.hub = json.load(f)
            cls.folder = "TandemProbe-" + secrets.token_hex(3)
            cls.created_hub = None
        else:
            cls.hub = stalwart.new_hub()
            cls.folder = "Tandem"
            cls.created_hub = cls.hub["name"]

    @classmethod
    def tearDownClass(cls):
        if cls.created_hub:
            stalwart.delete_hub(cls.created_hub)
        elif EXTERNAL:
            m = cls.imap_connect_cls()
            try:
                m.delete(cls.folder)
            finally:
                m.logout()

    # -- clients -----------------------------------------------------------

    @classmethod
    def jmap_cls(cls, desc=None):
        j = (desc or cls.hub)["jmap"]
        return Jmap(j["session"], j.get("user"), j.get("password"), j.get("token"))

    def jmap(self, desc=None):
        return self.jmap_cls(desc)

    @classmethod
    def imap_connect_cls(cls, desc=None):
        c = (desc or cls.hub)["imap"]
        return imap.connect(c["host"], c["port"], c["user"], c["password"], tls=c.get("tls", False))

    def imap(self, desc=None, select=True):
        m = self.imap_connect_cls(desc)
        self.addCleanup(_logout, m)
        imap.ensure_folder(m, self.folder)
        if select:
            imap.ok(m.select(self.folder))
        return m

    # -- JMAP folder handling (DESIGN §10.2 ensureFolder) --------------------

    def find_folder(self, client, name=None):
        """The design's lookup: filter by name (substring match), then take the
        mailbox named exactly `name` with no parent, else the one directly under
        the inbox."""
        name = name or self.folder
        ids = client.call("Mailbox/query", {"filter": {"name": name}})["ids"]
        boxes = client.call("Mailbox/get", {"ids": ids})["list"] if ids else []
        inbox = [b["id"] for b in client.call("Mailbox/get", {"ids": None})["list"] if b.get("role") == "inbox"]
        exact = [b for b in boxes if b["name"] == name and b.get("parentId") is None]
        under_inbox = [b for b in boxes if b["name"] == name and b.get("parentId") in inbox]
        chosen = exact or under_inbox
        return chosen[0]["id"] if chosen else None

    def ensure_folder_jmap(self, client, name=None):
        name = name or self.folder
        found = self.find_folder(client, name)
        if found:
            return found
        res = client.call("Mailbox/set", {"create": {"t": {"name": name, "parentId": None}}})
        if "t" in res.get("created", {}):
            return res["created"]["t"]["id"]
        found = self.find_folder(client, name)                # lost a race: query again
        self.assertIsNotNone(found, f"could not create or find {name}: {res}")
        return found


def _logout(m):
    try:
        m.logout()
    except Exception:
        pass


def testbed_only(cls):
    return unittest.skipIf(EXTERNAL, "testbed-only probe (needs provisioning or Toxiproxy)")(cls)
