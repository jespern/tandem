"""Regressions for the one-owner collection design; run with unittest discover."""
import copy
import itertools
import json
import random
import unicodedata
import unittest

from owner_model import (META, VECTOR_LIMIT, Device, Hub, OwnerLog, b64u, data_write, dominated, may_destroy, digest, inventory_bytes,
                         inventory_hash, needed, owner, receive_grant, record, reissue, report,
                         statement, strict_json, supersedes, sync, write_valid)


def genesis():
    body = {"n": 0, "epoch": 0, "commit": "K0", "snapshot": None, "deleted": False,
            "members": {"O": "owner"}}
    return statement(None, "O", body)


def next_statement(log, edit=lambda b: None, rotate=None, key="K1", inventory=None,
                   signer="O", dot=None, parent=None):
    parent = log.head() if parent is None else parent
    before = log.st[parent]["body"]
    body = copy.deepcopy(before)
    edit(body)
    body["n"] += 1
    if rotate:
        body.update(epoch=before["epoch"] + 1, commit=key,
                    snapshot=inventory_hash(inventory or {}))
    return statement(parent, signer, body, body["n"] if dot is None else dot)


def family():
    log = OwnerLog(genesis())
    assert log.add(next_statement(log, lambda b: b["members"].update(W="writer", R="reader")))
    return log


def fixture():
    log, hub = family(), Hub()
    epoch = log.epoch(log.head())
    hub.publish(epoch, "K0", [log.st[i] for i in log.line()], [], {})
    home = {"keys": {epoch: "K0"}, "flight": None}
    return log, hub, home


def demote(body):
    body["members"]["W"] = "reader"


def drop_r(body):
    body["members"].pop("R")


class Authority(unittest.TestCase):
    def test_only_the_owners_key_signs_a_roster(self):
        for key in ("W", "R", "X"):
            log = family()
            self.assertFalse(log.add(next_statement(log, demote, rotate=True, signer=key)), key)
            self.assertFalse(log.frozen)
        log = family()
        self.assertTrue(log.add(next_statement(log, demote, rotate=True)))

    def test_there_are_three_roles_and_the_owner_never_changes(self):
        def swap(b):
            b["members"].update(O="writer", W="owner")
        edits = [lambda b: b["members"].update(W="admin"),           # no such role
                 lambda b: b["members"].update(W="owner"),           # a second owner
                 swap,                                               # another owner
                 lambda b: b["members"].pop("O")]                    # no owner
        for edit in edits:
            for rotate in (None, True):
                log = family()
                self.assertFalse(log.add(next_statement(log, edit, rotate=rotate)))

    def test_removal_and_demotion_require_a_new_key(self):
        for edit in (drop_r, demote):
            log = family()
            self.assertFalse(log.add(next_statement(log, edit)))
            log = family()
            self.assertTrue(log.add(next_statement(log, edit, rotate=True)))
        log = family()                                               # additions and promotions don't
        self.assertTrue(log.add(next_statement(log, lambda b: b["members"].update(R="writer", J="reader"))))

    def test_no_admission_in_an_epoch_start(self):
        log = family()
        self.assertFalse(log.add(next_statement(log, lambda b: b["members"].update(J="writer"), rotate=True)))

    def test_a_key_is_never_used_for_two_epochs(self):
        log = family()
        self.assertTrue(log.add(next_statement(log, drop_r, rotate=True, key="K1")))
        self.assertFalse(log.add(next_statement(log, demote, rotate=True, key="K0")))

    def test_out_of_order_history_and_duplicates(self):
        log = family()
        start = log.head()
        s1 = next_statement(log, demote, rotate=True)
        self.assertTrue(log.add(s1))
        s2 = next_statement(log, lambda b: b["members"].update(W="writer"))
        for order in itertools.permutations([s1, s2]):
            replica = family()
            for s in list(order) + [s1, s2]:
                replica.add(s)
            self.assertEqual(replica.head(), s2["id"])
            self.assertFalse(replica.frozen)
            self.assertEqual(replica.line()[-3], start)

    def test_two_successors_kill_the_collection_in_every_delivery_order(self):
        for dot in range(12):
            origin = family()
            first = next_statement(origin, demote, rotate=True, dot=dot)
            second = next_statement(origin, drop_r, rotate=True, key="K2", dot=dot + 1)
            for order in ((first, second), (second, first)):
                log = family()
                prefix = log.head()
                for s in order:
                    log.add(s)
                self.assertTrue(log.frozen)
                self.assertEqual(log.head(), prefix)                 # nobody follows either line

    def test_an_invalid_statement_signed_by_the_owner_still_counts_as_a_successor(self):
        log = family()
        first = next_statement(log, demote, rotate=True)
        invalid = next_statement(log, lambda b: b["members"].update(W="owner"))
        self.assertTrue(log.add(first))
        self.assertFalse(log.add(invalid))
        self.assertTrue(log.frozen)

    def test_how_a_member_came_in_cannot_be_rewritten(self):
        log = family()
        join = lambda b: (b["members"].update(G="reader"), b.setdefault("via", {}).update(G="party"))
        self.assertTrue(log.add(next_statement(log, join)))
        self.assertFalse(log.add(next_statement(log, lambda b: b["via"].update(G="other"))))
        self.assertFalse(log.add(next_statement(log, lambda b: b["via"].pop("G"))))
        self.assertFalse(log.add(next_statement(log, lambda b: b["via"].update(X="party"))))   # not a member

    def test_a_deleted_collection_has_no_successors(self):
        log = family()
        self.assertTrue(log.add(next_statement(log, lambda b: b.update(deleted=True))))
        self.assertFalse(log.add(next_statement(log, lambda b: b["members"].update(J="reader"))))


class Data(unittest.TestCase):
    def setUp(self):
        self.log = family()
        self.old = data_write("W", self.log.head(), record("item"), "old")

    def test_readers_and_strangers_cannot_write(self):
        head = self.log.head()
        for uid in ("R", "X"):
            self.assertFalse(write_valid(self.log, data_write(uid, head, record("item"), "x"), self.log.genesis))
        self.assertTrue(write_valid(self.log, self.old, self.log.genesis))

    def test_demotion_cannot_be_bypassed_by_copying_the_new_key(self):
        old_head = self.log.head()
        demotion = next_statement(self.log, demote, rotate=True)
        self.log.add(demotion)
        fake_body = copy.deepcopy(self.log.st[old_head]["body"])
        fake_body.update(n=fake_body["n"] + 1, epoch=1, commit="K1", snapshot=inventory_hash({}))
        fake = statement(old_head, "W", fake_body)
        self.assertFalse(self.log.add(fake))
        attack = data_write("W", fake["id"], record("item"), "unauthorized")
        self.assertFalse(write_valid(self.log, attack, demotion["id"]))
        backdated = data_write("W", old_head, record("item"), "backdated")
        self.assertFalse(write_valid(self.log, backdated, demotion["id"], {}))
        self.assertFalse(self.log.frozen)

    def test_exact_epoch_id_is_required_not_just_its_key(self):
        # The admission and genesis share K0; only genesis is that epoch's ID.
        self.assertTrue(write_valid(self.log, self.old, self.log.genesis))
        self.assertFalse(write_valid(self.log, self.old, self.log.head()))

    def test_snapshot_is_fixed_by_the_signed_inventory(self):
        inventory = {self.old["rec"]: [self.old["id"]]}
        s = next_statement(self.log, lambda b: b["members"].pop("W"), rotate=True, inventory=inventory)
        self.log.add(s)
        self.assertTrue(write_valid(self.log, self.old, s["id"], inventory))   # a removed member's past stays
        late = data_write("W", self.old["ref"], self.old["rec"], "backdated", hlc=2)
        altered = {self.old["rec"]: [self.old["id"], late["id"]]}
        self.assertFalse(write_valid(self.log, late, s["id"], altered))
        self.assertFalse(write_valid(self.log, late, s["id"], inventory))

    def test_inventory_encoding_is_deterministic_and_rejects_duplicates(self):
        a, b = record("a"), record("b")
        x, y = digest("x"), digest("y")
        self.assertEqual(inventory_bytes({a: [x, y], b: [x]}),
                         inventory_bytes({b: [x], a: [y, x]}))
        with self.assertRaises(ValueError):
            inventory_bytes({a: [x, x]})
        with self.assertRaises(ValueError):                                    # an entry lists at least one write
            inventory_bytes({a: [x], b: []})
        self.assertEqual(inventory_bytes({}), bytes(4))                        # the empty inventory is u32(0)

    def test_reissue_uses_first_authorizing_statement_after_promotion(self):
        demotion = next_statement(self.log, demote, rotate=True)
        self.log.add(demotion)
        promotion = next_statement(self.log, lambda b: b["members"].update(W="writer"))
        self.log.add(promotion)
        new = reissue(self.log, self.old)
        self.assertEqual(new["ref"], promotion["id"])
        self.assertTrue(write_valid(self.log, new, demotion["id"]))
        self.assertEqual(new["base"][self.old["dev"]], self.old["hlc"])
        self.assertEqual(new, reissue(copy.deepcopy(self.log), copy.deepcopy(self.old)))

    def test_current_readers_cannot_reissue(self):
        self.log.add(next_statement(self.log, demote, rotate=True))
        with self.assertRaises(ValueError):
            reissue(self.log, self.old)

    def test_a_write_the_new_state_holds_or_supersedes_is_not_signed_again(self):
        # W's first device never saw the edit that replaced its write. The snapshot carries the edit.
        head = self.log.head()
        w1 = data_write("W", head, record("item"), "milk", dev="W1", hlc=1)
        w2 = data_write("O", head, record("item"), "oat milk", dev="O1", hlc=5, ctx={"W1": 1})
        self.assertTrue(supersedes(self.log, w2, w1))
        self.log.add(next_statement(self.log, drop_r, rotate=True, inventory={w2["rec"]: [w2["id"]]}))
        self.assertIsNone(reissue(self.log, w1, state=[w2]))                   # superseded: the old value stays gone
        self.assertIsNone(reissue(self.log, w2, state=[w2]))                   # carried: nothing to do
        other = data_write("W", head, record("other"), "eggs", dev="W1", hlc=2)
        again = reissue(self.log, other, state=[w2])
        self.assertIsNone(reissue(self.log, other, state=[w2, again]))         # a second device finds it done

    def test_two_reissued_writes_lose_their_order(self):
        # A stated limit (§11.6): both missed the snapshot; the replaced value returns as a conflict, not as the value.
        head = self.log.head()
        w1 = data_write("W", head, record("item"), "milk", dev="W1", hlc=1)
        w2 = data_write("O", head, record("item"), "oat milk", dev="O1", hlc=5, ctx={"W1": 1})
        self.log.add(next_statement(self.log, drop_r, rotate=True))
        a, b = reissue(self.log, w1), reissue(self.log, w2)
        self.assertFalse(supersedes(self.log, b, a) or supersedes(self.log, a, b))

    def test_oversized_vectors_are_invalid_and_signing_again_stays_within_the_limit(self):
        # Review (Sol): made-up device IDs in contexts. Each write is under the limit; what an
        # honest write would need to replace them all is not. Nothing resets such a field: the
        # way out is to delete the record and make it again (§9.2), which needs no vectors.
        head = self.log.head()
        big = lambda n, tag: {"%s%d" % (tag, i): 1 for i in range(n)}
        over = data_write("W", head, record("item"), "x", ctx=big(VECTOR_LIMIT + 1, "a"))
        self.assertFalse(write_valid(self.log, over, self.log.genesis))
        vandal = [data_write("W", head, record("item"), "v%d" % i, dev="V%d" % i, ctx=big(VECTOR_LIMIT // 2, "f%d-" % i))
                  for i in range(3)]
        for w in vandal:
            self.assertTrue(write_valid(self.log, w, self.log.genesis))
        ctx, base = needed(self.log, vandal, 0)
        self.assertGreater(len(ctx), VECTOR_LIMIT)                             # no valid write can replace them all
        fresh = data_write("O", head, record("item again"), "v0", dev="O1")
        self.assertTrue(write_valid(self.log, fresh, self.log.genesis))        # a new record owes them nothing
        full = data_write("W", head, record("other"), "y", dev="W9", ctx=big(VECTOR_LIMIT, "g"))
        self.assertTrue(write_valid(self.log, full, self.log.genesis))         # valid at the limit...
        self.log.add(next_statement(self.log, drop_r, rotate=True))
        with self.assertRaises(ValueError):                                    # ...but one entry too many once signed again
            reissue(self.log, full)
        small = data_write("W", head, record("other"), "z", dev="W9", ctx=big(VECTOR_LIMIT - 1, "g"))
        self.assertTrue(write_valid(self.log, reissue(self.log, small), self.log.epoch(self.log.head()), {}))

    def test_field_names_are_the_same_only_if_their_bytes_are(self):
        # Review (Sol): "é" as one code point and as "e" with a combining accent. A string type that
        # normalizes when comparing (Swift's does) calls them one field and drops the first write;
        # one that doesn't (Python's) keeps both. Bytes decide, for everyone.
        log = family()
        head, rec = log.head(), record("item")
        a = data_write("W", head, rec, "one", dev="W1", hlc=1, field="\u00e9")
        b = data_write("W", head, rec, "two", dev="W1", hlc=2, ctx={"W1": 1}, field="e\u0301")
        self.assertEqual(unicodedata.normalize("NFC", b["field"]), a["field"])
        self.assertFalse(supersedes(log, b, a))                                # two fields: both writes stay
        c = data_write("W", head, rec, "three", dev="W1", hlc=3, ctx={"W1": 1}, field="\u00e9")
        self.assertTrue(supersedes(log, c, a))
        again = reissue(log, a)
        self.assertEqual(again["field"], a["field"])                           # and signing again keeps the name

    def test_two_devices_never_destroy_each_others_message(self):
        # Testbed finding: {x, y} and {y}, where y supersedes x, each make the other look redundant.
        # With a tie-break for identical contents only, one device destroys the first, another the
        # second, and the hub has neither.
        log = family()
        head, rec = log.head(), record("item")
        x = data_write("W", head, rec, "one", dev="W1", hlc=1)
        y = data_write("W", head, rec, "two", dev="W1", hlc=2, ctx={"W1": 1})
        m, n = [x, y], [y]
        self.assertTrue(dominated(log, m, n) and dominated(log, n, m))
        for hm, hn in (("aa", "bb"), ("bb", "aa")):
            self.assertNotEqual(may_destroy(log, m, n, hm, hn), may_destroy(log, n, m, hn, hm))
        self.assertFalse(may_destroy(log, m, m, "aa", "aa"))                   # identical copies: the maker's job
        z = data_write("W", head, rec, "three", dev="W1", hlc=3, ctx={"W1": 2})
        self.assertTrue(may_destroy(log, n, [z], "aa", "bb"))                  # plainly redundant: goes, whatever the hashes
        self.assertFalse(may_destroy(log, [z], n, "aa", "bb"))

    def test_only_the_owner_writes_the_collections_name(self):
        head = self.log.head()
        self.assertTrue(write_valid(self.log, data_write("O", head, META, "House"), self.log.genesis))
        self.assertFalse(write_valid(self.log, data_write("W", head, META, "Mine"), self.log.genesis))


class Rotation(unittest.TestCase):
    def setUp(self):
        self.log, self.hub, self.home = fixture()
        self.phone = Device("O", self.log, self.hub, self.home)
        self.original = copy.deepcopy(self.log)

    def finish(self, device=None):
        device = device or self.phone
        for _ in range(12):
            if device.step() in ("done", "idle", "dropped"):
                return
        self.fail("journal did not finish")

    def mac(self):
        return Device("O", copy.deepcopy(self.original), self.hub, self.home)

    def test_grants_follow_the_announcement_and_old_data_survives_until_then(self):
        old = data_write("W", self.log.head(), record("item"), "old")
        epoch = self.log.epoch(self.log.head())
        self.hub.channels[(epoch, "K0")]["writes"] = [old]
        s = self.phone.prepare(demote, [old], fresh_key="K1")
        self.assertEqual(self.phone.step(), "uploaded")
        self.assertFalse(self.hub.inbox)
        self.assertNotIn(s, self.hub.channels[(epoch, "K0")]["history"])
        self.assertEqual(self.hub.channels[(epoch, "K0")]["writes"], [old])
        self.assertEqual(self.phone.step(), "committed")
        self.assertFalse(self.hub.inbox)
        self.finish()
        grant = self.hub.inbox["W"][0]
        self.assertEqual(receive_grant(self.original, self.hub, "W", grant), [old])

    def test_restart_at_each_step_resumes_identical_statement_and_key(self):
        for stop in range(7):
            log, hub, home = fixture()
            device = Device("O", log, hub, home)
            s = device.prepare(demote, fresh_key="K1")
            for _ in range(stop):
                device.step()
            device = Device("O", copy.deepcopy(log), hub, home, copy.deepcopy(device.store))
            self.finish(device)
            self.assertEqual(device.log.head(), s["id"])
            self.assertFalse(device.log.frozen)
            self.assertEqual(set(hub.channels) - {(log.genesis, "K0")}, {(s["id"], "K1")})
            self.assertIsNone(home["flight"])

    def test_lost_upload_announcement_and_grant_acknowledgements_are_idempotent(self):
        for stop in range(6):
            log, hub, home = fixture()
            device = Device("O", log, hub, home)
            s = device.prepare(demote, fresh_key="K1")
            for _ in range(stop):
                device.step()
            saved_log, saved_store = copy.deepcopy(log), copy.deepcopy(device.store)
            device.step()  # remote effect happens; crash loses the local progress update
            retry = Device("O", saved_log, hub, home, saved_store)
            self.finish(retry)
            self.assertFalse(retry.log.frozen)
            self.assertEqual(retry.log.head(), s["id"])

    def test_a_key_change_under_way_blocks_every_other_change(self):
        self.phone.prepare(demote, fresh_key="K1")
        self.phone.step()
        for device in (self.phone, self.mac()):
            for act in (lambda: device.prepare(drop_r, fresh_key="K2"),
                        lambda: device.amend(lambda b: b["members"].update(J="reader"))):
                with self.assertRaises(ValueError):
                    act()

    def test_nobody_but_the_owner_administers(self):
        for uid in ("W", "R", "X"):
            other = Device(uid, copy.deepcopy(self.original), self.hub, self.home)
            for act in (lambda: other.prepare(demote), lambda: other.amend(demote), other.finish_here):
                with self.assertRaises(ValueError):
                    act()
        with self.assertRaises(ValueError):
            Device("O", self.log, self.hub, {})

    def test_current_grant_catches_up_without_intermediate_keys(self):
        first = self.phone.prepare(demote, fresh_key="K1")
        self.finish()
        second = self.phone.prepare(drop_r, fresh_key="K2")
        self.finish()
        self.assertEqual(receive_grant(self.original, self.hub, "W", self.hub.inbox["W"][-1]), [])
        self.assertEqual(self.original.head(), second["id"])
        self.assertNotEqual(first["id"], second["id"])
        self.assertFalse(receive_grant(family(), self.hub, "W", self.hub.inbox["W"][0]))   # an old grant moves nobody back

    def test_epoch_zero_grant_and_an_older_grant_in_the_current_epoch(self):
        epoch = self.log.genesis
        grant = {"at": self.log.head(), "epoch": epoch, "key": "K0", "signer": "O"}
        self.assertEqual(receive_grant(self.original, self.hub, "W", grant), [])
        later = self.phone.amend(lambda b: b["members"].update(J="reader"))
        self.assertEqual(receive_grant(self.original, self.hub, "W", grant), [])
        self.assertEqual(self.original.head(), later["id"])
        self.assertFalse(receive_grant(family(), self.hub, "W", dict(grant, signer="W")))

    def test_a_conflict_stops_a_pending_journal(self):
        self.phone.prepare(demote, fresh_key="K1")
        self.phone.step()
        a = next_statement(self.log, drop_r, rotate=True, key="K2", dot=98)
        b = next_statement(self.log, drop_r, rotate=True, key="K3", dot=99)
        self.log.add(a)
        self.log.add(b)
        with self.assertRaises(ValueError):
            self.phone.step()
        self.assertTrue(self.log.frozen)
        self.assertFalse(self.hub.inbox)

    def test_cleanup_allows_additional_current_epoch_writes(self):
        self.phone.prepare(drop_r, fresh_key="K1")
        self.phone.step()
        self.phone.step()
        epoch = self.log.epoch(self.log.head())
        late = data_write("W", self.log.head(), record("late"), "new")
        self.hub.channels[(epoch, "K1")]["writes"].append(late)
        self.finish()
        self.assertIn(late, self.hub.channels[(epoch, "K1")]["writes"])

    def test_a_late_device_enters_an_epoch_whose_snapshot_was_edited_since(self):
        # A snapshot write that later edits superseded is collected; that must not shut anyone out.
        old = data_write("W", self.log.head(), record("item"), "buy milk", dev="W1", hlc=1)
        self.hub.channels[(self.log.genesis, "K0")]["writes"] = [old]
        s = self.phone.prepare(drop_r, [old], fresh_key="K1")
        self.finish()
        edit = data_write("O", s["id"], record("item"), "buy oat milk", dev="O1", hlc=2, base={"W1": 1})
        self.assertTrue(supersedes(self.log, edit, old))
        self.hub.channels[(s["id"], "K1")]["writes"] = [edit]
        self.assertEqual(receive_grant(self.original, self.hub, "W", self.hub.inbox["W"][-1]), [edit])

    def test_a_missing_inventory_holds_activation_and_destroyed_uploads_are_put_back(self):
        old = data_write("W", self.log.head(), record("item"), "old")
        s = self.phone.prepare(demote, [old], fresh_key="K1")
        self.phone.step()
        channel = self.hub.channels[(s["id"], "K1")]
        channel["writes"] = []                                                 # destroyed before the announcement
        self.assertEqual(self.phone.step(), "uploaded")                        # put back first; nothing announced yet
        self.assertNotIn(s["id"], self.log.st)
        self.finish()
        grant = self.hub.inbox["W"][-1]
        channel = self.hub.channels[(s["id"], "K1")]
        saved, channel["inventory"] = channel["inventory"], None
        self.assertIsNone(receive_grant(copy.deepcopy(self.original), self.hub, "W", grant))
        channel["inventory"] = saved
        self.assertEqual(receive_grant(self.original, self.hub, "W", grant), [old])

    def test_random_delivery_of_many_serialized_decisions(self):
        rng = random.Random(17)
        decisions = []
        for i in range(20):
            self.phone.prepare(lambda b, i=i: b["members"].update(W="reader" if i % 2 == 0 else "writer"),
                               fresh_key="K%d" % (i + 1))
            decisions.append(copy.deepcopy(self.phone.store["pending"]["statement"]))
            self.finish()
        for _ in range(30):
            replica = family()
            order = decisions * 2
            rng.shuffle(order)
            for s in order:
                replica.add(s)
            self.assertEqual(replica.head(), self.log.head())
            self.assertFalse(replica.frozen)

    def test_a_snapshot_write_missing_from_the_hub_survives_the_next_rotation(self):
        # Review (Sol 1): E1 lists X, X's message is gone for a while, and a device that never
        # saw X rotates to E2. Whoever still holds X must be able to put it back there.
        x = data_write("W", self.log.head(), record("item"), "milk", dev="W1", hlc=1)
        gone = data_write("W", self.log.head(), record("old"), "bread", dev="W1", hlc=2)
        self.hub.channels[(self.log.genesis, "K0")]["writes"] = [x, gone]
        e1 = self.phone.prepare(drop_r, [x, gone], fresh_key="K1")
        self.finish()
        self.hub.channels[(e1["id"], "K1")]["writes"] = []                     # destroyed on the hub
        mac = self.mac()
        e2 = mac.prepare(demote, [], fresh_key="K2", resolved=[gone["id"]])    # the Mac saw `gone` replaced, never saw x
        self.finish(mac)
        inventory = self.hub.channels[(e2["id"], "K2")]["inventory"]
        self.assertEqual(inventory, {x["rec"]: [x["id"]]})                     # carried forward unseen
        self.assertTrue(write_valid(mac.log, x, e2["id"], inventory))          # a replica that holds x restores it
        self.assertFalse(write_valid(mac.log, gone, e2["id"], inventory))
        self.hub.channels[(e2["id"], "K2")]["inventory"] = None
        with self.assertRaises(ValueError):
            mac.prepare(drop_r, [], fresh_key="K3")                            # no rotating without the inventory

    # --- two devices of one owner -----------------------------------------------------

    def test_another_device_finishes_or_drops_a_key_change_at_every_stage(self):
        # The phone is lost before, during and after a key change. The Mac holds the same key and home.
        for stop in range(8):
            log, hub, home = fixture()
            phone = Device("O", log, hub, home)
            s = phone.prepare(demote, fresh_key="K1")
            for _ in range(stop):
                phone.step()
            exported = stop >= 1
            mac = Device("O", family(), hub, home)
            mac.finish_here()
            self.assertFalse(mac.log.frozen)
            self.assertEqual(s["id"] in mac.log.line(), exported, stop)        # uploaded: finished. Not: never happened.
            self.assertIsNone(home["flight"])
            self.assertEqual((s["id"], "K1") in hub.channels, exported)
            if exported:
                member = family()
                if not hub.inbox.get("W"):
                    mac.grant("W")                                             # a grant the phone never sent, on request
                self.assertEqual(receive_grant(member, hub, "W", hub.inbox["W"][-1]), [])
                self.assertEqual(member.head(), s["id"])
            again = mac.prepare(drop_r, fresh_key="K2")
            self.finish(mac)
            self.assertEqual(mac.log.head(), again["id"])

    def test_finishing_elsewhere_never_destroys_the_last_copy_on_the_hub(self):
        # Review (Sol 2): the phone uploads the snapshot and is lost; one message in the new channel
        # is destroyed; the old channel still has it. The Mac must not announce and then delete it.
        a = data_write("W", self.log.head(), record("a"), "milk", dev="W1", hlc=1)
        b = data_write("W", self.log.head(), record("b"), "eggs", dev="W1", hlc=2)
        old = self.hub.channels[(self.log.genesis, "K0")]
        old["writes"] = [a, b]
        s = self.phone.prepare(demote, [a, b], fresh_key="K1")
        self.phone.step()                                                      # uploaded, not announced
        new = self.hub.channels[(s["id"], "K1")]
        new["writes"] = [w for w in new["writes"] if w["id"] != b["id"]]
        mac = self.mac()
        self.assertEqual(mac.finish_here(), "finished")
        self.assertEqual(old["writes"], [])
        self.assertEqual({w["id"] for w in new["writes"]}, {a["id"], b["id"]})

    def test_old_data_is_not_destroyed_while_a_snapshot_write_is_missing_from_the_new_channel(self):
        a = data_write("W", self.log.head(), record("a"), "milk", dev="W1", hlc=1)
        old = self.hub.channels[(self.log.genesis, "K0")]
        old["writes"] = [a]
        s = self.phone.prepare(drop_r, [a], fresh_key="K1")
        self.phone.step(); self.phone.step()                                   # announced
        new = self.hub.channels[(s["id"], "K1")]
        new["writes"] = []                                                     # destroyed between announcement and cleanup
        self.finish()
        self.assertEqual([w["id"] for w in new["writes"]], [a["id"]])
        edit = data_write("O", s["id"], record("a"), "oat milk", dev="O1", hlc=2, base={"W1": 1})
        old["writes"], new["writes"] = [a], [edit]                             # but a write that was replaced isn't brought back
        from owner_model import restore
        restore(self.log, old, new, new["inventory"], s["id"])
        self.assertEqual(new["writes"], [edit])
        # Review (Sol): a reader's write is signed but not allowed. It replaces nothing, so it must
        # not talk the cleanup out of restoring the real one.
        junk = data_write("R", s["id"], record("a"), "junk", dev="R1", hlc=9, base={"W1": 1})
        self.assertFalse(write_valid(self.log, junk, s["id"], new["inventory"]))
        old["writes"], new["writes"] = [a], [junk]
        restore(self.log, old, new, new["inventory"], s["id"])
        self.assertIn(a["id"], {w["id"] for w in new["writes"]})

    def test_a_dropped_change_is_named_so_it_can_be_done_again(self):
        # Review (Sol 4): the phone starts demoting W and dies before uploading. On the Mac the owner
        # admits someone else. The demotion must not vanish without a word.
        self.phone.prepare(demote, fresh_key="K1")
        mac = self.mac()
        self.assertEqual(mac.finish_here(), "dropped")
        was = mac.abandoned["members"]
        self.assertEqual(was["W"], "reader")                                   # the Mac can say what was abandoned
        redo = mac.prepare(lambda b: b.update(members=was), fresh_key="K2")    # "Do it here"
        self.finish(mac)
        mac.amend(lambda b: b["members"].update(J="reader"))                   # and then what the owner came for
        self.assertEqual(mac.log.st[mac.log.head()]["body"]["members"]["W"], "reader")
        self.assertEqual(self.phone.step(), "dropped")
        self.assertIn(redo["id"], mac.log.line())

    def test_the_first_device_returns_after_its_key_change_was_dropped(self):
        self.phone.prepare(demote, fresh_key="K1")                             # prepared on the phone, never uploaded
        mac = self.mac()
        self.assertEqual(mac.finish_here(), "dropped")
        other = mac.prepare(drop_r, fresh_key="K2")
        self.assertEqual(self.phone.step(), "dropped")                         # the phone finds its note gone
        self.finish(mac)
        self.phone.sync()
        self.assertEqual(self.log.head(), other["id"])
        self.assertFalse(self.log.frozen)
        self.phone.amend(lambda b: b["members"].update(J="reader"))            # and carries on as before
        self.assertFalse(self.log.frozen)

    def test_the_backup_key_is_enough_to_administer_again(self):
        # Every device is lost. The backup key is the user's key and opens home; home holds the epoch keys.
        s = self.phone.prepare(drop_r, fresh_key="K1")
        self.finish()
        new = Device("O", family(), self.hub, self.home)
        fresh = new.prepare(demote, fresh_key="K2")
        self.finish(new)
        self.assertEqual(fresh["parent"], s["id"])
        self.assertEqual(new.log.head(), fresh["id"])

    def test_a_note_that_is_gone_does_not_mean_the_change_was_made(self):
        # Review (Sol): a note goes when a change is announced and also when it is discarded. The Mac
        # discards the phone's uploaded change and is cut off before it has destroyed the channel; the
        # key is still within reach. Only the announcement says a change was made.
        s = self.phone.prepare(demote, fresh_key="K1")
        self.phone.step()                                                      # uploaded, not announced
        self.home["flight"] = None                                             # discarded: the note goes first
        ipad = self.mac()
        ipad.sync()
        self.assertNotIn(s["id"], ipad.log.st)
        self.assertFalse(ipad.log.frozen)
        redo = ipad.prepare(demote, fresh_key="K2")                            # "do it here", with a new key
        self.finish(ipad)
        other = self.mac()
        other.sync()                                                           # the leftover is no second successor
        self.assertFalse(other.log.frozen)
        self.assertEqual(other.log.head(), redo["id"])
        self.assertEqual(self.phone.step(), "dropped")                         # and the phone cleans up after itself
        self.assertNotIn((s["id"], "K1"), self.hub.channels)

    def test_removing_a_member_closes_their_invite_without_a_second_write(self):
        # Review (Sol): the phone removes G and is lost before it has closed G's invite in home. The
        # invite can be used again and again, and G can post the old request again.
        party = {"id": "party", "maxUses": 5, "closed": False}                 # as home holds it
        self.assertEqual(self.phone.admit("G", party), "admitted")
        self.assertEqual(self.phone.admit("G", party), "granted")              # already in: the grant again
        gone = lambda b: (b["members"].pop("G"), b["via"].pop("G"))
        self.phone.prepare(gone, fresh_key="K1")
        self.phone.step(); self.phone.step()                                   # announced; home still says the invite is open
        self.assertFalse(party["closed"])
        mac = self.mac()
        self.assertEqual(mac.admit("G", party), "refused")                     # the old request, posted again
        self.assertEqual(mac.admit("G2", party), "refused")                    # G under a new identity, same invite
        again = {"id": "again", "maxUses": 1, "closed": False}
        self.assertEqual(mac.admit("G", again), "refused")                     # never back in without the owner's word
        self.assertEqual(mac.admit("G", again, asked=True), "admitted")
        self.assertEqual(mac.admit("H", again), "refused")                     # uses are counted on the roster
        self.assertNotIn("G2", mac.log.st[mac.log.head()]["body"]["members"])

    def test_an_invite_stays_closed_when_the_removed_member_comes_back_another_way(self):
        # Review (Sol): G joins through a reusable writer invite and is removed; the home write that
        # would close the invite is lost. The owner lets G back in as a reader through a new invite.
        # The old invite must not open again: G still holds it and could bring in a new identity.
        party = {"id": "party", "maxUses": 5, "closed": False}
        self.assertEqual(self.phone.admit("G", party, role="writer"), "admitted")
        self.phone.prepare(lambda b: (b["members"].pop("G"), b["via"].pop("G")), fresh_key="K1")
        self.finish()
        again = {"id": "again", "maxUses": 1, "closed": False}
        self.assertEqual(self.phone.admit("G", again, role="reader", asked=True), "admitted")
        self.assertFalse(party["closed"])                                      # home never heard of it
        mac = self.mac()
        self.assertEqual(mac.admit("X", party, role="writer"), "refused")
        self.assertNotIn("X", mac.log.st[mac.log.head()]["body"]["members"])

    def test_a_crash_right_after_the_announcement_leaves_no_note_behind(self):
        # Review (Sol): the phone publishes the announcement and dies before it has updated home.
        # The note still names the change. Carrying on with the grants alone would leave the note
        # there for good, and every later change to sharing would be refused.
        s = self.phone.prepare(demote, fresh_key="K1")
        self.phone.step()                                                      # uploaded
        old = self.hub.channels[(self.log.genesis, "K0")]
        old["history"].append(copy.deepcopy(s))                                # announced; the home update is lost
        self.assertEqual(self.home["flight"]["id"], s["id"])
        phone = Device("O", copy.deepcopy(self.original), self.hub, self.home, self.phone.store)   # restart
        self.finish(phone)
        self.assertIsNone(self.home["flight"])
        self.assertEqual(self.home["keys"][s["id"]], "K1")
        self.assertTrue(self.hub.inbox["W"])
        phone.amend(lambda b: b["members"].update(J="reader"))                 # sharing can change again
        # The phone never comes back: the Mac sees the same thing and tidies up without being asked what to do.
        log, hub, home = fixture()
        gone = Device("O", log, hub, home)
        s = gone.prepare(demote, fresh_key="K1")
        gone.step()
        hub.channels[(log.genesis, "K0")]["history"].append(copy.deepcopy(s))
        mac = Device("O", family(), hub, home)
        self.assertEqual(mac.finish_here(), "announced")
        self.assertIsNone(home["flight"])
        mac.amend(lambda b: b["members"].update(J="reader"))

    def test_a_change_the_hub_hides_from_another_device_kills_the_collection(self):
        # Fails closed: a withholding hub can stop the collection, never hand it to anyone.
        mac = self.mac()
        hidden = mac.amend(lambda b: b["members"].update(J="reader"))
        self.hub.hidden.add(hidden["id"])
        self.phone.amend(lambda b: b["members"].update(Z="reader"))            # the phone, unaware, signs in the same place
        self.hub.hidden.clear()
        member = family()
        sync(member, self.hub, {member.genesis: "K0"})
        self.assertTrue(member.frozen)
        self.assertEqual(member.head(), self.original.head())

    def test_random_devices_crashes_and_decisions_never_conflict_on_an_honest_hub(self):
        rng = random.Random(23)
        for trial in range(400):
            log, hub, home = fixture()
            devices = [Device("O", family(), hub, home) for _ in range(3)]
            made, n = [], 0
            for _ in range(rng.randint(5, 30)):
                i, r, n = rng.randrange(3), rng.random(), n + 1
                d = devices[i]
                try:
                    if r < 0.25:
                        flip = "reader" if d.log.st[d.log.head()]["body"]["members"]["W"] == "writer" else "writer"
                        made.append(d.prepare(lambda b: b["members"].update(W=flip), fresh_key="K%d" % n))
                    elif r < 0.65:
                        d.step()
                    elif r < 0.8:
                        d.finish_here()
                    elif r < 0.9:
                        d.amend(lambda b: b["members"].update({"J%d" % n: "reader"}))
                    else:                                                      # the app is killed and started again
                        devices[i] = Device("O", copy.deepcopy(d.log), hub, home, copy.deepcopy(d.store))
                except ValueError:
                    pass
                self.assertFalse(devices[i].log.frozen, trial)
            for d in devices:                                                  # everyone comes back and finishes
                self.finish(d)
            devices[0].finish_here()
            member = family()
            sync(member, hub, home["keys"])
            self.assertFalse(member.frozen, trial)
            self.assertIsNone(home["flight"])
            for d in devices:
                d.sync()
                self.assertEqual(d.log.head(), member.head(), trial)
            for s in made:                                                     # announced or gone, never half
                self.assertEqual(s["id"] in member.st, (s["id"], s["body"]["commit"]) in hub.channels, trial)

    # --- conflicts reach everyone -----------------------------------------------------

    def test_a_conflict_reaches_members_who_cannot_read_where_it_was_posted(self):
        before = self.log.head()
        s = self.phone.prepare(drop_r, fresh_key="K1")
        self.finish()
        self.phone.amend(lambda b: b["members"].update(J="writer"))
        sibling = next_statement(self.log, demote, rotate=True, key="K9", parent=before, dot=77)
        self.hub.channels[(self.log.genesis, "K0")]["history"].append(sibling)
        j, w = family(), family()
        sync(j, self.hub, {s["id"]: "K1"})                                     # J joined later: no old key
        sync(w, self.hub, {self.log.genesis: "K0", s["id"]: "K1"})
        self.assertFalse(j.frozen)
        self.assertTrue(w.frozen)
        report(w, self.hub, s["id"], "K1")                                     # W shows it in the current channel
        sync(j, self.hub, {s["id"]: "K1"})
        self.assertTrue(j.frozen)


if __name__ == "__main__":
    unittest.main()


class Encoding(unittest.TestCase):
    def test_json_that_parsers_read_differently_is_invalid(self):
        # Review (Sol): two "rec" properties, signed for the first. A parser that keeps the first
        # accepts the signature, one that keeps the last rejects it: same bytes, two verdicts.
        raw = b'{"rec":"A","rec":"B","writes":[]}'
        first = json.loads(raw, object_pairs_hook=lambda items: dict(reversed(items)))["rec"]
        last = json.loads(raw)["rec"]
        self.assertNotEqual(first, last)
        bad = [raw, b'{"a":{"x":1,"x":1}}', b'{"n":1.0}', b'{"n":1e3}', b'{"n":9007199254740993}',
               b'{"n":NaN}', b'{"n":1} x', rb'{"s":"\ud800"}', rb'{"\udc00":1}', b'{"s":"\xff"}', b'\xef\xbb\xbf{"n":1}']
        for case in bad:
            with self.assertRaises(ValueError, msg=case):
                strict_json(case)
        self.assertEqual(strict_json(rb'{"n":9007199254740992,"more":{"n":-1},"s":"\u00f8"}')["n"], 2 ** 53)
        # Names are compared as they are, not normalized: these are two properties, not one twice.
        self.assertEqual(len(strict_json(rb'{"\u00e9":1,"e\u0301":2}')), 2)

    def test_base64url_has_one_spelling_per_value(self):
        # Lenient decoders read "QQ", "QR" and "QQ==" as the same byte; then one ID has several texts.
        self.assertEqual(b64u("QQ"), b"A")
        self.assertEqual(b64u(""), b"")
        for text in ("QR", "QQ==", "QQ=", "Q", "Q Q", "QQ\n", "+/8", "QUJDRA=="):
            with self.assertRaises(ValueError, msg=text):
                b64u(text)
