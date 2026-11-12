"""HISTORICAL ranked-authority tests; current tests: test_owner_model.py.

Tests for the earlier rotation model: a rotation stopped after each step, a vandal on the hub,
devices that were away, two rotations at once, and randomized mixes of all of these.
Run:  python3 model/test_rotation_model.py
"""
import os
import random
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from rotation_model import Device, Hub, new_collection, settle  # noqa: E402
from tandem_model import ROLE  # noqa: E402

FAMILY = [("O", "owner", ["O1", "O2"]), ("A", "admin", ["A1"]), ("B", "admin", ["B1"]),
          ("W", "writer", ["W1", "W2"]), ("V", "writer", ["V1"]), ("L", "writer", ["L1"]),
          ("M", "writer", ["M1"]), ("R", "reader", ["R1"])]


def remove(uid):
    return lambda members: members.pop(uid, None)


def drop_grants(hub):
    for uid in hub.inbox:
        hub.inbox[uid] = [m for m in hub.inbox[uid] if m["type"] != "grant"]


class Rotation(unittest.TestCase):

    def setUp(self):
        self.hub = Hub()
        self.d = new_collection(self.hub, FAMILY)
        self.edits = {}                                    # logical write -> device that made it

    def write(self, name):
        lid = self.d[name].write()
        if lid:
            self.edits[lid] = name
        return lid

    def agreed(self, gone=(), rng=None):
        """Settle, then check that everyone alive agrees. Returns the head's roster."""
        rounds = settle(self.d, rng)
        self.assertLess(rounds, 60, "did not settle")
        live = [d for d in self.d.values() if d.alive]
        members = [d for d in live if not d.removed]
        heads = {d.chain.head() for d in members}
        self.assertEqual(len(heads), 1, "devices disagree about the head")
        head = heads.pop()
        roster = members[0].chain.body(head)
        for d in members:
            self.assertEqual(d.current, roster["commit"], "%s is not on the head's epoch" % d.name)
            self.assertEqual(d.queue, [], "%s still has unpublished writes" % d.name)
        states = {d.name: frozenset(w["lid"] for w in d.replica.values()) for d in members}
        self.assertEqual(len(set(states.values())), 1, "replicas differ: %s" % states)
        state = next(iter(states.values()))
        for lid, name in self.edits.items():               # nothing by a live writer's device is lost
            d = self.d[name]
            if d.alive and not d.removed and roster["members"][d.uid]["role"] != "reader":
                self.assertIn(lid, state, "%s by %s was lost" % (lid, name))
        decided = set(gone) | {uid for d in live for uid in d.intents}
        for uid in decided:                                 # what a device that is still around decided has happened
            self.assertNotIn(uid, roster["members"])
        for d in live:                                      # whoever is out knows it, and holds no current key
            if d.uid not in roster["members"]:
                self.assertTrue(d.removed, "%s still thinks it is a member" % d.name)
                self.assertNotIn(roster["commit"], d.keys, "%s holds the new key" % d.name)
        return roster

    # --- the plain case ---------------------------------------------------------------

    def test_a_clean_rotation(self):
        for name in ("W1", "V1", "L1", "W2"):
            self.write(name)
        self.d["O1"].intents["L"] = None
        roster = self.agreed(gone={"L"})
        self.assertEqual(roster["epoch"], 1)

    def test_writes_that_miss_the_snapshot_are_signed_again(self):
        self.write("W1")
        self.d["O1"].sync()
        late1, late2 = self.write("W2"), self.write("V1")            # reach the old channel after the snapshot
        self.d["O1"].rotate(remove("L"), stale=True)
        self.agreed(gone={"L"})
        for d in self.d.values():
            if not d.removed:
                self.assertTrue({late1, late2} <= {w["lid"] for w in d.replica.values()})

    def test_a_removed_members_late_writes_stay_out(self):
        self.d["O1"].sync()
        late = self.write("L1")
        self.d["O1"].rotate(remove("L"), stale=True)
        self.agreed(gone={"L"})
        self.assertNotIn(late, {w["lid"] for w in self.d["W1"].replica.values()})

    # --- stopped after each step --------------------------------------------------------

    def test_stopped_before_the_announcement_changes_nothing(self):
        self.write("W1")
        for stop, sent in ((3, None), (4, 0), (4, 3), (4, 6)):
            before = self.d["W1"].chain.head()
            hub, d = self.hub, self.d
            rotator = d["A1"] if d["A1"].alive else d["B1"]
            rotator.rotate(remove("M"), stop_after=stop, grants_sent=sent)
            rotator.alive = True                                     # it comes back, having forgotten what it was doing
            roster = self.agreed()
            self.assertEqual(self.d["W1"].chain.head(), before)
            self.assertIn("M", roster["members"])

    def test_stopped_before_the_announcement_and_someone_else_finishes(self):
        self.write("W1")
        self.hub.notices.setdefault(self.d["L1"].current, []).append(("L", self.d["L1"].chain.head()))
        self.d["A1"].rotate(remove("L"), stop_after=4, grants_sent=4)   # A1 dies with some grants out
        roster = self.agreed(gone={"L"})                              # the notice is still due: B or O does it
        self.assertEqual(roster["epoch"], 1)

    def test_stopped_after_the_announcement(self):
        self.write("W1"); self.write("V1")
        self.d["A1"].rotate(remove("L"), stop_after=5)
        self.agreed(gone={"L"})

    def test_announced_and_then_nobody_has_the_key(self):
        self.write("W1"); self.write("V1")
        self.d["A1"].rotate(remove("L"), stop_after=5)               # announced, A1 dies
        drop_grants(self.hub)                                        # and a vandal deletes every grant
        roster = self.agreed(gone={"L"})
        self.assertEqual(roster["epoch"], 2)                         # someone took over, on top of it

    # --- a vandal on the hub ----------------------------------------------------------

    def test_deleted_grants_are_asked_for(self):
        self.write("W1")
        self.d["O1"].rotate(remove("L"))
        drop_grants(self.hub)
        self.agreed(gone={"L"})

    def test_a_deleted_announcement_is_put_back(self):
        old = self.d["O1"].current
        s = self.d["O1"].rotate(remove("L"))
        self.hub.roster[old].discard(s["id"])
        self.agreed(gone={"L"})

    def test_deleted_data_is_healed(self):
        for name in ("W1", "V1", "W2"):
            self.write(name)
        settle(self.d)
        self.hub.data[self.d["W1"].current] = set()
        self.agreed()
        self.assertEqual(len(self.hub.data[self.d["W1"].current]), 3)

    # --- away, and at once ------------------------------------------------------------

    def test_away_through_two_rotations(self):
        self.write("W2")
        self.d["W2"].alive = False
        self.d["O1"].rotate(remove("L")); settle(self.d)
        self.d["A1"].rotate(remove("M")); settle(self.d)
        self.d["W2"].alive = True
        self.agreed(gone={"L", "M"})

    def test_away_through_two_rotations_with_the_grants_gone(self):
        self.write("W2")
        self.d["W2"].alive = self.d["W1"].alive = False              # the user's every device is away
        self.d["O1"].rotate(remove("L")); settle(self.d)
        self.d["A1"].rotate(remove("M")); settle(self.d)
        drop_grants(self.hub)
        self.d["W2"].alive = self.d["W1"].alive = True
        self.agreed(gone={"L", "M"})

    def test_a_newly_paired_device_catches_up_from_home(self):
        # Fifth round, Sol 3: it holds no old channel keys of its own; home has them.
        self.write("W1")
        self.d["O1"].rotate(remove("L")); settle(self.d)
        self.d["A1"].rotate(remove("M")); settle(self.d)
        drop_grants(self.hub)
        genesis = self.hub.stmts[self.d["W1"].chain.genesis_id]
        self.d["W3"] = Device("W3", "W", "W.k", self.hub, genesis)
        self.agreed(gone={"L", "M"})
        self.assertEqual(self.d["W3"].current, self.d["W1"].current)

    def test_two_rotations_at_once(self):
        for name in ("W1", "V1", "L1", "M1"):
            self.write(name)
        for d in self.d.values():
            d.sync()
        self.d["A1"].intents["L"] = self.d["A1"].rotate(remove("L"), stale=True)["id"]
        self.d["B1"].intents["M"] = self.d["B1"].rotate(remove("M"), stale=True)["id"]   # B1 hasn't seen A1's
        self.agreed(gone={"L", "M"})

    # --- everything at once -----------------------------------------------------------

    def mix(self, rng):
        """A random run of edits, syncs, rotations that stop anywhere, and vandalism. Returns what must end up removed, and a log."""
        gone, resting, log = set(), [], []
        for _ in range(rng.randint(4, 16)):
            r = rng.random()
            live = [d for d in self.d.values() if d.alive and not d.removed]
            if r < 0.35:
                writers = [d for d in live if d.role() >= ROLE["writer"]]
                if writers:
                    log.append("write %s" % self.edits.get(self.write(rng.choice(writers).name)))
            elif r < 0.5:
                d = rng.choice(live); d.sync(); log.append("sync %s" % d.name)
            elif r < 0.75:
                admins = [d for d in live if d.role() >= ROLE["admin"]]
                if not admins:
                    continue
                who, uid = rng.choice(admins), rng.choice(["L", "M", "V"])
                stop, sent, stale = rng.choice([None, None, None, 3, 4, 5]), rng.randint(0, 8), rng.random() < 0.4
                s = who.rotate(remove(uid), stop_after=stop, grants_sent=sent, stale=stale)
                who.intents[uid] = s["id"] if s and stop is None else None    # a device that stopped forgot what it made
                back = not who.alive and rng.random() < 0.6
                if back:
                    resting.append(who)                               # a device that dies for good takes its decisions with it
                log.append("%s removes %s (stop=%s, grants=%s, stale=%s): %s%s" % (
                    who.name, uid, stop, sent, stale, s["id"][:6] if s else None, "" if who.alive else ", dies" + (", returns" if back else "")))
            elif r < 0.83:
                drop_grants(self.hub); log.append("vandal deletes grants")
            elif r < 0.9:
                for channel, ids in self.hub.roster.items():
                    for sid in list(ids):
                        if self.hub.commit_of(sid) != channel:
                            ids.discard(sid)
                log.append("vandal deletes announcements")
            elif r < 0.95:
                for channel in self.hub.data:
                    self.hub.data[channel] = {w for w in sorted(self.hub.data[channel]) if rng.random() < 0.5}
                log.append("vandal deletes data")
            else:
                d = rng.choice(live)
                if d.role() < ROLE["admin"]:
                    d.alive = False; resting.append(d); log.append("%s goes away" % d.name)
        for d in resting:
            d.alive = True
        ranked = [d for d in self.d.values() if d.uid in ("O", "A", "B")]
        if not any(d.alive for d in ranked):                # with nobody of rank left, nobody can re-key (§8.2)
            ranked[0].alive = True
            log.append("%s returns after all" % ranked[0].name)
        return gone, log

    def picture(self):
        out = []
        for d in self.d.values():
            b = d.chain.body(d.chain.head())
            out.append("%-3s alive=%s removed=%s current=%s head=%s (epoch %s, %s, members %s) keys=%s waited=%s intents=%s" % (
                d.name, d.alive, d.removed, d.current, d.chain.head()[:6], b["epoch"], b["commit"],
                "".join(sorted(b["members"])), sorted(d.keys), d.waited, sorted(d.intents)))
        return out

    def test_random_mixes(self):
        rng = random.Random(8)
        for trial in range(300):
            self.setUp()
            gone, log = self.mix(rng)
            try:
                self.agreed(gone=gone, rng=rng)
            except AssertionError as e:
                raise AssertionError("trial %d: %s\n%s\n%s" % (trial, e, "\n".join(log), "\n".join(self.picture())))


if __name__ == "__main__":
    unittest.main(verbosity=1)
