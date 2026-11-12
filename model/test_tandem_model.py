"""Tests for the Tandem model: every defect found in review as a regression case,
plus randomized adversaries. Run:  python3 model/test_tandem_model.py
"""
# Historical ranked-authority tests; current collection tests: test_owner_model.py.
import copy
import itertools
import os
import random
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from tandem_model import (Chain, CollectionRules, UserRules, ROLE, author_write, epoch_statement,  # noqa: E402
                          merge, prove, prune, statement, write_valid)


# --- builders ---------------------------------------------------------------------------

def user_genesis(first="d0", recovery="rk"):
    g = statement(None, first, {"recovery": recovery, "devices": [first], "gone": [], "puk": 0, "uks": "uks0"})
    c = Chain(UserRules(), g["id"])
    assert c.add(g)
    return c, g["id"]


def user_next(chain, parent, signer, add=(), remove=(), recovery=None, rotate=None, salt="", cosign=()):
    P = chain.body(parent)
    B = copy.deepcopy(P)
    for r in remove:
        if r in B["devices"]:
            B["devices"].remove(r)
            B["gone"] = sorted(set(B["gone"]) | {r})
    B["devices"] += list(add)
    if recovery:
        B["recovery"] = recovery
    if rotate is None:
        rotate = recovery is not None or any(r != signer for r in remove)
    if rotate:
        B["puk"] += 1
        B["uks"] = "uks:%s:%s:%s" % (parent[:6], signer, salt)
    return statement(parent, signer, B, salt=salt, proofs=[prove(k, k, parent, B, salt) for k in cosign])


def coll_genesis(owner="O", key="O.k0", generation=lambda uid, key: 0, certified=lambda uid, key: True):
    g = statement(None, key, {"epoch": 0, "commit": "K0", "members": {owner: {"role": "owner", "uks": key}}, "deleted": False})
    c = Chain(CollectionRules(generation, certified), g["id"])
    assert c.add(g)
    return c, g["id"]


def coll_next(chain, parent, signer, edit, rotate=None, proofs=(), salt="", copied=(), commit=None):
    """`proofs` are (uid, key) pairs the actor holds the keys for; `copied` are proofs lifted from elsewhere."""
    P = chain.body(parent)
    B = copy.deepcopy(P)
    edit(B["members"])
    reducing = False
    for u in set(P["members"]) | set(B["members"]):
        o, n = P["members"].get(u), B["members"].get(u)
        if o != n and (n is None or (o and (o["uks"] != n["uks"] or ROLE[n["role"]] < ROLE[o["role"]]))):
            reducing = True
    if rotate is None:
        rotate = reducing
    if rotate:
        B["epoch"] += 1
        B["commit"] = commit or "K:%s:%s:%s" % (parent[:6], signer, salt)
    made = [prove(uid, key, parent, B, salt) for uid, key in proofs] + [list(x) for x in copied]
    return statement(parent, signer, B, proofs=made, salt=salt)


def setm(uid, role, key):
    return lambda m: m.__setitem__(uid, {"role": role, "uks": key})


def delm(uid):
    return lambda m: m.pop(uid, None)


def add_all(chain, *stmts):
    return [chain.add(s) for s in stmts]


def head_after_shuffles(make_chain, stmts, rng, rounds=12):
    heads = set()
    for _ in range(rounds):
        c = make_chain()
        order = stmts[:]
        rng.shuffle(order)
        for s in order:
            c.add(s)
        heads.add(c.head())
    return heads


# --- user chain -------------------------------------------------------------------------

class UserChain(unittest.TestCase):

    def test_newer_device_cannot_remove_older(self):
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        self.assertFalse(c.add(user_next(c, s1["id"], "d1", remove=["d0"])))
        self.assertTrue(c.add(user_next(c, s1["id"], "d0", remove=["d1"])))

    def test_removal_by_another_device_rotates_the_puk_in_the_same_statement(self):
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        self.assertFalse(c.add(user_next(c, s1["id"], "d0", remove=["d1"], rotate=False)))
        self.assertTrue(c.add(user_next(c, s1["id"], "d1", remove=["d1"], rotate=False)))   # signing out

    def test_only_the_recovery_key_replaces_itself_and_there_is_one_genesis(self):
        c, g = user_genesis()
        self.assertFalse(c.add(user_next(c, g, "d0", recovery="rk-evil")))
        self.assertFalse(c.add(user_next(c, g, "rk", recovery="rk2")))                # the recovery key alone: no
        self.assertTrue(c.add(user_next(c, g, "rk", recovery="rk2", cosign=["d0"])))  # with a device's co-signature
        other = statement(None, "d0", {"recovery": "rk-evil", "devices": ["d0"], "gone": [], "puk": 0, "uks": "uks0"})
        self.assertFalse(c.add(other))                       # a second genesis has another ID: another user

    def test_revoked_device_cannot_keep_or_add_a_device_by_forking(self):
        # First review round, Sol 3; my finding 1 of the last review.
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        s2 = user_next(c, s1["id"], "d0", remove=["d1"]); c.add(s2)          # d1 stolen, removed by the older d0
        fork = user_next(c, s1["id"], "d1", add=["alias"]); c.add(fork)       # thief leaves the removal out
        f2 = user_next(c, fork["id"], "alias", add=["alias2"]); c.add(f2)
        f3 = user_next(c, f2["id"], "alias2", add=["alias3"]); c.add(f3)      # and builds a longer line
        self.assertEqual(c.head(), s2["id"])
        self.assertNotIn("alias", c.body(c.head())["devices"])

    def test_stolen_oldest_device_is_settled_by_the_recovery_key(self):
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        thief = user_next(c, s1["id"], "d0", remove=["d1"]); c.add(thief)     # stolen d0 removes the owner's d1
        self.assertEqual(c.head(), thief["id"])                               # d1 can't answer that
        rec = user_next(c, s1["id"], "rk", remove=["d0", "d1"], add=["d2"]); c.add(rec)   # recovering needs no co-signature
        more = user_next(c, thief["id"], "d0", add=["x1"]); c.add(more)
        self.assertEqual(c.head(), rec["id"])
        self.assertEqual(c.body(c.head())["devices"], ["d2"])

    def test_the_old_recovery_secret_cannot_replace_or_outrank(self):
        # Second round, Sol 3. Fifth round, Sol 2: no device can be lured into backing the wrong replacement.
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        new = user_next(c, s1["id"], "rk", recovery="rk2", cosign=["d1"]); c.add(new)   # typed on d1, which co-signs
        evil = user_next(c, s1["id"], "rk", recovery="rk-evil", salt="x")
        self.assertFalse(c.add(evil))                                         # the old secret alone can't replace
        lifted = statement(s1["id"], "rk", evil["body"], proofs=new["proofs"], salt="x")
        self.assertFalse(c.add(lifted))                                       # nor with d1's co-signature copied over
        grab = user_next(c, s1["id"], "rk", remove=["d0", "d1"], add=["evil"], salt="y"); c.add(grab)
        g2 = user_next(c, grab["id"], "evil", add=["evil2"]); c.add(g2)       # it can still "recover" on a fork...
        self.assertEqual(c.head(), new["id"])                                 # ...which has no listed device behind it

    def test_signed_out_device_and_the_residual(self):
        # A device that signed out can't come back: signing out takes something away, a fork that adds doesn't.
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        out = user_next(c, s1["id"], "d0", remove=["d0"]); c.add(out)
        rot = user_next(c, out["id"], "d1", rotate=True); c.add(rot)
        back = user_next(c, s1["id"], "d0", add=["x"]); c.add(back)
        back2 = user_next(c, back["id"], "x", add=["y"]); c.add(back2)
        self.assertEqual(c.head(), rot["id"])
        # Nor by removing someone on its fork: the real line has another listed device behind it.
        evil = user_next(c, s1["id"], "d0", remove=["d1"], salt="e"); c.add(evil)
        self.assertEqual(c.head(), rot["id"])
        # Residual (Q15): from before any other device existed, two additions by the old key tie, and the ID decides.
        c2, g2 = user_genesis()
        t1 = user_next(c2, g2, "d0", add=["d1"]); c2.add(t1)
        t2 = user_next(c2, t1["id"], "d0", remove=["d0"]); c2.add(t2)
        t3 = user_next(c2, t2["id"], "d1", rotate=True); c2.add(t3)
        for n in range(1000):
            e1 = user_next(c2, g2, "d0", add=["x"], salt="e%d" % n)
            if e1["id"] < t1["id"]:
                break
        c2.add(e1)
        self.assertEqual(c2.head(), e1["id"])                                 # the old key wins here...
        seal = user_next(c2, t3["id"], "rk"); c2.add(seal)
        self.assertEqual(c2.head(), seal["id"])                               # ...until the recovery key signs

    def test_another_devices_signature_cannot_be_borrowed_to_tip_a_fork(self):
        # A stolen newer device signing on the wrong side changes nothing: the older counter-signer outranks it.
        c, g = user_genesis()
        s1 = user_next(c, g, "d0", add=["d1"]); c.add(s1)
        new = user_next(c, s1["id"], "rk", recovery="rk2", cosign=["d0"]); c.add(new)
        old = user_next(c, s1["id"], "rk", recovery="rk-evil", salt="x", cosign=["d1"]); c.add(old)   # the thief also holds d1
        self.assertEqual(c.head(), new["id"])

    def test_random_revoked_device_never_changes_the_head(self):
        rng = random.Random(1)
        checked = 0
        for _ in range(400):
            c, g = user_genesis()
            tip, n, removed_by_other, everything = g, 1, [], [c.st[g]]
            for _ in range(rng.randint(4, 10)):                               # an honest history
                devs = c.body(tip)["devices"]
                r = rng.random()
                if r < 0.5 or len(devs) < 2:
                    s = user_next(c, tip, rng.choice(devs + ["rk"]), add=["d%d" % n]); n += 1
                elif r < 0.85:
                    j = rng.randrange(1, len(devs)); i = rng.randrange(0, j)
                    s = user_next(c, tip, devs[i], remove=[devs[j]]); removed_by_other.append(devs[j])
                else:
                    victim = rng.choice(devs)
                    s = user_next(c, tip, "rk", remove=[victim], add=["d%d" % n]); n += 1
                    removed_by_other.append(victim)
                assert c.add(s)
                tip = s["id"]; everything.append(s)
            if not removed_by_other:
                continue
            honest_head = tip
            keys = [rng.choice(removed_by_other)]                             # the adversary holds a revoked key
            for m in range(rng.randint(3, 25)):
                spots = [i for i in c.st if any(k in c.body(i)["devices"] for k in keys)]
                if not spots:
                    break
                p = rng.choice(spots)
                k = rng.choice([k for k in keys if k in c.body(p)["devices"]])
                r = rng.random()
                if r < 0.6:
                    alias = "a%d" % len(keys); keys.append(alias)
                    s = user_next(c, p, k, add=[alias], salt=str(m))
                elif r < 0.9:
                    s = user_next(c, p, k, remove=[rng.choice(c.body(p)["devices"])], salt=str(m))
                else:
                    s = user_next(c, p, k, recovery="rk-evil", salt=str(m))
                c.add(s); everything.append(s)
            self.assertEqual(c.head(), honest_head)
            gid = c.genesis_id
            self.assertEqual(head_after_shuffles(lambda: Chain(UserRules(), gid), everything, rng), {honest_head})
            checked += 1
        self.assertGreater(checked, 200)


# --- collection chain -------------------------------------------------------------------

def family(generation=lambda uid, key: 0, certified=lambda uid, key: True):
    """Owner O, admins A and B, writer W, reader R."""
    c, g = coll_genesis(generation=generation, certified=certified)
    tip = g
    for uid, role in (("A", "admin"), ("B", "admin"), ("W", "writer"), ("R", "reader")):
        s = coll_next(c, tip, "O.k0", setm(uid, role, uid + ".k0")); assert c.add(s); tip = s["id"]
    return c, tip


class CollectionChain(unittest.TestCase):

    def test_who_may_change_what(self):
        c, tip = family()
        bad = [
            coll_next(c, tip, "W.k0", setm("Z", "reader", "Z.k0")),           # writers sign nothing
            coll_next(c, tip, "A.k0", delm("B")),                             # an admin can't remove an admin
            coll_next(c, tip, "A.k0", setm("O", "admin", "O.k0")),            # or touch the owner
            coll_next(c, tip, "A.k0", setm("A", "owner", "A.k0")),            # or promote themselves
            coll_next(c, tip, "A.k0", setm("Z", "admin", "Z.k0")),            # or appoint an admin
            coll_next(c, tip, "A.k0", delm("W"), rotate=False),               # a removal must change the key
            coll_next(c, tip, "A.k0", setm("W", "reader", "W.k0"), rotate=False),
            coll_next(c, tip, "A.k0", setm("A", "admin", "A.k1")),            # own new key needs a proof by it
            coll_next(c, tip, "A.k0", delm("A")),                             # nobody removes themselves
            coll_next(c, tip, "O.k0", setm("Z", "owner", "Z.k0")),            # two owners
        ]
        self.assertEqual([c.add(s) for s in bad], [False] * len(bad))
        good = [
            coll_next(c, tip, "A.k0", setm("Z", "writer", "Z.k0")),
            coll_next(c, tip, "A.k0", delm("W")),
            coll_next(c, tip, "A.k0", setm("A", "admin", "A.k1"), proofs=[("A", "A.k1")]),
            coll_next(c, tip, "O.k0", lambda m: (setm("O", "admin", "O.k0")(m), setm("A", "owner", "A.k0")(m))),
        ]
        self.assertEqual([c.add(s) for s in good], [True] * len(good))

    def test_removed_admin_gains_nothing_by_forking(self):
        # First round: my 2 and 3. Last review: my 1.
        c, tip = family()
        gone = coll_next(c, tip, "O.k0", delm("A")); c.add(gone)
        fork = coll_next(c, tip, "A.k0", setm("Z", "writer", "Z.k0"), salt="f"); c.add(fork)
        f2 = coll_next(c, fork["id"], "A.k0", delm("W")); c.add(f2)
        f3 = coll_next(c, f2["id"], "A.k0", setm("Y", "writer", "Y.k0")); c.add(f3)
        self.assertEqual(c.head(), gone["id"])
        commit = c.body(c.head())["commit"]
        for w in ({"id": "w1", "author": "Z", "key": "Z.k0", "stmt": f3["id"]},      # the admitted puppet
                  {"id": "w2", "author": "A", "key": "A.k0", "stmt": tip}):           # the removed admin, backdated
            self.assertFalse(write_valid(c, w, commit, {}))

    def test_demotion_takes_effect_at_the_rotation(self):
        # First round: Sol 4, my 5.
        c, tip = family()
        dem = coll_next(c, tip, "A.k0", setm("W", "reader", "W.k0")); c.add(dem)
        commit = c.body(dem["id"])["commit"]
        self.assertNotEqual(commit, c.body(tip)["commit"])
        late = {"id": "w", "author": "W", "key": "W.k0", "stmt": tip}                  # W pretends to be early
        self.assertFalse(write_valid(c, late, commit, {}))
        self.assertFalse(write_valid(c, dict(late, stmt=dem["id"]), commit, {}))

    def test_losing_ownership_branch_keeps_no_authority(self):
        # Second round: Sol 4.
        c, tip = family()
        to_a = coll_next(c, tip, "O.k0", lambda m: (setm("O", "admin", "O.k0")(m), setm("A", "owner", "A.k0")(m)), salt="a")
        to_b = coll_next(c, tip, "O.k0", lambda m: (setm("O", "admin", "O.k0")(m), setm("B", "owner", "B.k0")(m)), salt="b")
        add_all(c, to_a, to_b)
        b_removes_a = coll_next(c, to_b["id"], "B.k0", delm("A")); c.add(b_removes_a)
        a_acts = coll_next(c, to_a["id"], "A.k0", setm("Z", "writer", "Z.k0")); c.add(a_acts)
        head = c.head()
        roster = c.body(head)["members"]
        owner = [u for u, m in roster.items() if m["role"] == "owner"]
        self.assertEqual(len(owner), 1)                                               # one line, one owner
        self.assertIn(head, (b_removes_a["id"], a_acts["id"]))
        if owner == ["A"]:
            self.assertIn("B", roster)                                                # B's removal of A is gone
        else:
            self.assertNotIn("A", roster)

    def test_former_owner_can_fork_from_before_the_hand_over(self):
        # A stated gap (Q15): nothing in a roster outranks whoever was owner at the time.
        c, tip = family()
        hand = coll_next(c, tip, "O.k0", lambda m: (setm("O", "admin", "O.k0")(m), setm("A", "owner", "A.k0")(m))); c.add(hand)
        acts = coll_next(c, hand["id"], "A.k0", setm("Z", "writer", "Z.k0")); c.add(acts)
        for n in range(1000):
            back = coll_next(c, tip, "O.k0", delm("A"), salt="x%d" % n)
            if back["id"] < hand["id"]:
                break
        c.add(back)
        self.assertEqual(c.head(), back["id"])

    def test_stolen_owner_key_cannot_capture_the_collection(self):
        # Last self-review: my 1. This round: Sol 1 (a "replacement" key of the thief's own).
        gen = {"O.k0": 0, "O.k1": 1}                                                  # from O's user chain; anything else: -1
        c, tip = family(generation=lambda uid, key: gen.get(key, -1))
        t1 = coll_next(c, tip, "O.k0", setm("O", "owner", "O.evil"), proofs=[("O", "O.evil")], salt="t"); c.add(t1)
        t2 = coll_next(c, t1["id"], "O.evil", setm("Z", "admin", "Z.k0")); c.add(t2)
        t3 = coll_next(c, t2["id"], "O.evil", lambda m: (setm("O", "admin", "O.evil")(m), setm("Z", "owner", "Z.k0")(m))); c.add(t3)
        t4 = coll_next(c, t3["id"], "Z.k0", delm("A")); c.add(t4)
        self.assertEqual(c.head(), t4["id"])                                          # until the owner's device acts
        fix = coll_next(c, tip, "O.k0", setm("O", "owner", "O.k1"), proofs=[("O", "O.k1")]); c.add(fix)
        self.assertEqual(c.head(), fix["id"])                                         # one statement, however long the thief's line
        t5 = coll_next(c, t4["id"], "Z.k0", delm("B")); c.add(t5)
        t6 = coll_next(c, t5["id"], "Z.k0", setm("Y", "admin", "Y.k0")); c.add(t6)
        self.assertEqual(c.head(), fix["id"])
        self.assertEqual(c.body(c.head())["members"]["O"], {"role": "owner", "uks": "O.k1"})
        self.assertNotIn("Z", c.body(c.head())["members"])

    def test_a_copied_replacement_proof_is_worthless(self):
        # This round: Sol 1. The thief lifts the new key's proof off the genuine repair and puts it on a take-over.
        gen = {"O.k0": 0, "O.k1": 1}
        c, tip = family(generation=lambda uid, key: gen.get(key, -1))
        fix = coll_next(c, tip, "O.k0", setm("O", "owner", "O.k1"), proofs=[("O", "O.k1")]); c.add(fix)
        takeover = lambda m: (setm("O", "admin", "O.k1")(m), setm("R", "owner", "R.k0")(m))
        for n in range(200):                                                          # vary it until its ID would win
            evil = coll_next(c, tip, "O.k0", takeover, copied=fix["proofs"], salt="e%d" % n)
            if evil["id"] < fix["id"]:
                break
        self.assertFalse(c.add(evil))                                                 # the proof names another statement
        plain = coll_next(c, tip, "O.k0", lambda m: (setm("O", "admin", "O.k0")(m), setm("R", "owner", "R.k0")(m)), salt="p")
        self.assertTrue(c.add(plain))                                                 # valid without it, but only the old key signed
        more = coll_next(c, plain["id"], "R.k0", delm("O")); c.add(more)
        self.assertEqual(c.head(), fix["id"])

    def test_snapshot_lists_are_signed_with_the_key_the_epoch_starts_with(self):
        # Last review: my 1. A repair is signed with the old key, which the thief holds; a list must not be.
        c, tip = family()
        fix = coll_next(c, tip, "O.k0", setm("O", "owner", "O.k1"), proofs=[("O", "O.k1")]); c.add(fix)
        commit, R = c.body(fix["id"])["commit"], fix["id"]
        w = {"id": "x", "author": "O", "key": "O.k0", "stmt": tip, "rec": "r"}
        self.assertFalse(write_valid(c, w, commit, {("r", R): [({"x"}, "O.k0")]}))    # the thief's list
        self.assertTrue(write_valid(c, w, commit, {("r", R): [({"x"}, "O.k1")]}))     # the owner's

    def test_a_roster_key_must_come_from_the_users_own_chain(self):
        # Last review: my 3. An admin can't give a writer a key of the admin's making and then write as them.
        real = {("W", "W.k0"), ("W", "W.k1")}
        c, tip = family(certified=lambda uid, key: uid != "W" or (uid, key) in real)
        self.assertFalse(c.add(coll_next(c, tip, "A.k0", setm("W", "writer", "A.fake"))))
        self.assertTrue(c.add(coll_next(c, tip, "A.k0", setm("W", "writer", "W.k1"))))

    def test_generations_are_not_compared_between_users(self):
        # Last review: my 4. An admin who has advanced their key often doesn't win forks against other admins by that.
        gen = {"A.k0": 5, "B.k0": 0}
        for n in range(30):
            c, tip = family(generation=lambda uid, key: gen.get(key, 0))
            b = coll_next(c, tip, "B.k0", delm("W"), salt="b%d" % n); c.add(b)
            a = coll_next(c, tip, "A.k0", setm("N", "writer", "N.k"), salt="a%d" % n); c.add(a)
            self.assertEqual(c.head(), b["id"])                                       # taking away goes first

    def test_a_key_commitment_is_used_once(self):
        c, tip = family()
        r1 = coll_next(c, tip, "O.k0", delm("R"), commit="K1"); c.add(r1)
        self.assertFalse(c.add(coll_next(c, r1["id"], "O.k0", delm("W"), commit="K0")))   # back to the first key
        self.assertTrue(c.add(coll_next(c, r1["id"], "O.k0", delm("W"), commit="K2")))

    def test_a_proof_cannot_be_reused_under_other_metadata(self):
        # Fifth round, Sol 1: same body, same proof, different write metadata must not make a second statement.
        gen = {"O.k0": 0, "O.k1": 1}
        c, tip = family(generation=lambda uid, key: gen.get(key, -1))
        fix = coll_next(c, tip, "O.k0", setm("O", "owner", "O.k1"), proofs=[("O", "O.k1")]); c.add(fix)
        twin = statement(tip, "O.k0", fix["body"], proofs=fix["proofs"], salt="other-dot")
        self.assertNotEqual(twin["id"], fix["id"])
        self.assertFalse(c.add(twin))

    def test_two_starts_of_one_epoch_do_not_depend_on_arrival_order(self):
        # Fifth round, Sol 1 (second half). An admin signs two statements with one key; a list names the start it is for.
        def build(order):
            c, tip = family()
            a = coll_next(c, tip, "A.k0", delm("R"), commit="KX", salt="a")
            b = coll_next(c, tip, "A.k0", delm("W"), commit="KX", salt="b")
            for s in (a, b) if order else (b, a):
                c.add(s)
            return c, tip, a, b
        w = {"id": "w", "author": "V", "key": "V.k0", "stmt": None, "rec": "r"}
        verdicts = []
        for order in (True, False):
            c, tip, a, b = build(order)
            c.add(coll_next(c, tip, "O.k0", setm("V", "writer", "V.k0"), salt="v"))  # not on either line's past: ignore
            old = dict(w, author="W", key="W.k0", stmt=tip)
            verdicts.append((write_valid(c, old, "KX", {("r", a["id"]): [({"w"}, "A.k0")]}),
                             write_valid(c, old, "KX", {("r", b["id"]): [({"w"}, "A.k0")]}),
                             write_valid(c, old, "KX", {})))
        self.assertEqual(verdicts[0], verdicts[1])
        self.assertEqual(verdicts[0], (True, True, False))

    def test_nobody_joins_in_the_statement_that_starts_an_epoch(self):
        # Fifth round, Sol 4: a newcomer could not check that the epoch was ever announced.
        c, tip = family()
        both = lambda m: (delm("R")(m), setm("J", "writer", "J.k0")(m))
        self.assertFalse(c.add(coll_next(c, tip, "O.k0", both)))
        r = coll_next(c, tip, "O.k0", delm("R")); self.assertTrue(c.add(r))
        self.assertTrue(c.add(coll_next(c, r["id"], "O.k0", setm("J", "writer", "J.k0"))))

    def test_owners_two_devices_and_the_admin_being_removed(self):
        # This round: Opus 1. The owner's phone removes Mallory while the owner's Mac, unaware, adds someone.
        c, tip = family()
        c.add(coll_next(c, tip, "O.k0", setm("M", "admin", "M.k0"))); tip = c.head()
        phone = coll_next(c, tip, "O.k0", delm("M"), salt="phone"); c.add(phone)
        for n in range(1000):                                                         # give the Mac the luckier ID
            mac = coll_next(c, tip, "O.k0", setm("N", "writer", "N.k0"), salt="mac%d" % n)
            if mac["id"] < phone["id"]:
                break
        c.add(mac)
        self.assertEqual(c.head(), phone["id"])                                       # taking away beats adding
        x = coll_next(c, mac["id"], "M.k0", setm("P", "writer", "P.k0")); c.add(x)    # Mallory builds on the Mac's side
        x2 = coll_next(c, x["id"], "M.k0", delm("W")); c.add(x2)
        self.assertEqual(c.head(), phone["id"])                                       # and it changes nothing
        self.assertNotIn("M", c.body(c.head())["members"])

    def test_a_lower_rank_can_never_tip_a_fork_between_higher_ranks(self):
        rng = random.Random(5)
        for _ in range(200):
            c, tip = family()
            c.add(coll_next(c, tip, "O.k0", setm("M", "admin", "M.k0"))); tip = c.head()
            kinds = [lambda s: coll_next(c, tip, "O.k0", delm(rng.choice(["W", "R"])), salt=s),
                     lambda s: coll_next(c, tip, "O.k0", setm("N" + s, "writer", "N.k" + s), salt=s),
                     lambda s: coll_next(c, tip, "O.k0", delm("B"), salt=s)]
            sibs = [rng.choice(kinds)(str(n)) for n in range(rng.randint(2, 4))]
            for s in sibs:
                c.add(s)
            before = [s["id"] for s in sibs if s["id"] in c.line(c.head())]
            for m in range(rng.randint(1, 15)):                                       # admins M and A pile on anywhere
                key = rng.choice(["M.k0", "A.k0"])
                spots = [i for i in c.st if c.rules.who(c.body(i), key) and c.body(i)["members"][key[0]]["role"] == "admin"]
                c.add(coll_next(c, rng.choice(spots), key, setm("P%d" % m, "writer", "P%d.k0" % m), salt=str(m)))
            after = [s["id"] for s in sibs if s["id"] in c.line(c.head())]
            self.assertEqual(before, after)

    def test_a_lost_removal_made_again_sticks(self):
        # Two devices of the owner each take something away at once; the loser repeats its change on the head.
        rng = random.Random(6)
        for _ in range(200):
            c, tip = family()
            c.add(coll_next(c, tip, "O.k0", setm("M", "admin", "M.k0"))); tip = c.head()
            phone = coll_next(c, tip, "O.k0", delm("M"), salt="p%d" % rng.random()); c.add(phone)
            mac = coll_next(c, tip, "O.k0", delm("W"), salt="m%d" % rng.random()); c.add(mac)

            def mallory(n):
                for m in range(n):
                    spots = [i for i in c.st if c.body(i)["members"].get("M", {}).get("role") == "admin"]
                    r = rng.random()
                    edit = setm("P%d" % m, "writer", "P%d.k0" % m) if r < 0.6 else delm("R") if r < 0.8 else delm("W")
                    c.add(coll_next(c, rng.choice(spots), "M.k0", edit, salt=str(rng.random())))
            mallory(rng.randint(0, 10))
            for intent in ("M", "W"):                                                  # each device repeats what was lost
                if intent in c.body(c.head())["members"]:
                    s = coll_next(c, c.head(), "O.k0", delm(intent), salt="again"); assert c.add(s)
            settled = c.head()
            self.assertNotIn("M", c.body(settled)["members"])
            self.assertNotIn("W", c.body(settled)["members"])
            mallory(rng.randint(5, 20))
            self.assertEqual(c.head(), settled)

    def test_three_lines_have_one_winner(self):
        # This round: Sol 4, Opus 2. Equal-ranked admins A, B and C; the old pairwise rule ran in a circle here.
        c, tip = family()
        c.add(coll_next(c, tip, "O.k0", setm("C", "admin", "C.k0"))); tip = c.head()
        def run(start, keys, tag):
            at, out = start, []
            for n, k in enumerate(keys):
                s = coll_next(c, at, k, setm("%s%d" % (tag, n), "writer", "%s%d.k" % (tag, n)), salt=tag); out.append(s); at = s["id"]
                c.add(s)
            return out
        l1 = run(tip, ["A.k0"] * 4, "x"); l2 = run(tip, ["B.k0"] * 3, "y"); l3 = run(tip, ["A.k0", "A.k0", "C.k0"], "z")
        first = min(l[0]["id"] for l in (l1, l2, l3))                                 # same rank, all additions: lowest ID
        self.assertIn(first, c.line(c.head()))
        rng = random.Random(7)
        everything = list(c.st.values())
        gid = c.genesis_id
        self.assertEqual(head_after_shuffles(lambda: Chain(CollectionRules(), gid), everything, rng), {c.head()})

    def test_old_user_key_is_useless_after_the_rotation(self):
        # Second round: my 3 and Sol 2 (aliases), Sol 4 (dependence on a chain that changes).
        c, tip = family()
        rot = coll_next(c, tip, "A.k0", setm("W", "writer", "W.k1")); c.add(rot)      # W revoked a device: new user key
        commit = c.body(rot["id"])["commit"]
        old = {"id": "w-old", "author": "W", "key": "W.k0", "stmt": tip, "rec": "r"}
        self.assertFalse(write_valid(c, old, commit, {}))
        self.assertFalse(write_valid(c, dict(old, stmt=rot["id"]), commit, {}))
        self.assertTrue(write_valid(c, {"id": "w-new", "author": "W", "key": "W.k1", "stmt": rot["id"]}, commit, {}))

    def test_old_writes_enter_an_epoch_only_through_the_rotators_snapshot(self):
        # Second round: Sol 2 (reader replay). Third round: Sol 1 (approvals that vanish).
        c, tip = family()
        w = {"id": "w", "author": "W", "key": "W.k0", "stmt": tip, "rec": "r"}
        rot = coll_next(c, tip, "A.k0", delm("R")); c.add(rot)
        commit, R = c.body(rot["id"])["commit"], rot["id"]
        self.assertFalse(write_valid(c, w, commit, {}))                               # replayed by anyone: no
        self.assertFalse(write_valid(c, w, commit, {("r", R): [({"w"}, "W.k0")]}))    # vouched by a writer: no
        self.assertFalse(write_valid(c, w, commit, {("other", R): [({"w"}, "A.k0")]}))
        snap = {("r", R): [({"w"}, "A.k0")]}
        self.assertTrue(write_valid(c, w, commit, snap))
        snap[("r", R)].append(({"v"}, "A.k0"))                                        # lists only accumulate
        self.assertTrue(write_valid(c, w, commit, snap))
        # This round: Sol 3. Nothing from another line gets in, so epoch numbers only ever grow along what is carried.
        side = coll_next(c, tip, "B.k0", delm("W"), salt="side"); c.add(side)
        deeper = coll_next(c, side["id"], "B.k0", setm("V", "writer", "V.k0")); c.add(deeper)
        alien = {"id": "a", "author": "V", "key": "V.k0", "stmt": deeper["id"], "rec": "r"}
        self.assertTrue(write_valid(c, alien, c.body(deeper["id"])["commit"], {}))
        self.assertFalse(write_valid(c, alien, commit, {("r", R): [({"a"}, "A.k0")]}))
        forged = {"id": "w", "author": "W", "key": "R.k0", "stmt": tip, "rec": "r"}   # listed, but not W's signature
        self.assertFalse(write_valid(c, forged, commit, snap))

    def test_validity_of_a_write_never_changes(self):
        # Second round: Sol 1. Third round: Sol 4.
        c, tip = family()
        promote = coll_next(c, tip, "A.k0", setm("R", "writer", "R.k0"), salt="p"); c.add(promote)
        w = {"id": "w", "author": "R", "key": "R.k0", "stmt": promote["id"]}
        commit = c.body(tip)["commit"]
        self.assertTrue(write_valid(c, w, commit, {}))
        demote = coll_next(c, tip, "O.k0", setm("A", "writer", "A.k0")); c.add(demote)     # concurrent: A is demoted
        more = coll_next(c, demote["id"], "O.k0", setm("Q", "writer", "Q.k0")); c.add(more)
        self.assertEqual(c.head(), more["id"])                                        # A's promotion lost the fork...
        self.assertTrue(write_valid(c, w, commit, {}))                                # ...R's write in that epoch stands
        self.assertFalse(write_valid(c, w, c.body(more["id"])["commit"], {}))         # and doesn't enter the next one

    def test_a_late_fork_of_an_ancestor_has_a_defined_outcome(self):
        # Third round: Sol 3.
        c, tip = family()
        a1 = coll_next(c, tip, "A.k0", delm("W"), salt="1a"); c.add(a1)
        a2 = coll_next(c, a1["id"], "A.k0", delm("R")); c.add(a2)
        b1 = coll_next(c, tip, "B.k0", delm("R"), salt="1b"); c.add(b1)               # late, same rank
        self.assertEqual(c.head(), a2["id"] if a1["id"] < b1["id"] else b1["id"])     # equals: the lowest ID
        o1 = coll_next(c, tip, "O.k0", delm("R"), salt="1o"); c.add(o1)               # late, by the owner
        self.assertEqual(c.head(), o1["id"])                                          # the owner outranks both
        self.assertIsNotNone(epoch_statement(c, c.body(o1["id"])["commit"]))

    def test_random_cut_member_never_changes_the_head_or_writes(self):
        rng = random.Random(2)
        checked = 0
        for _ in range(300):
            c, tip = family()
            n, cut, everything = 0, [], list(c.st.values())
            for _ in range(rng.randint(3, 9)):
                body = c.body(tip)["members"]
                admins = [u for u, m in body.items() if m["role"] == "admin"]
                low = [u for u, m in body.items() if m["role"] in ("writer", "reader")]
                r = rng.random()
                if r < 0.35:
                    signer = rng.choice(["O"] + admins)
                    s = coll_next(c, tip, body[signer]["uks"], setm("M%d" % n, rng.choice(["writer", "reader"]), "M%d.k0" % n)); n += 1
                elif r < 0.55 and admins:
                    a = rng.choice(admins)
                    removed = rng.random() < 0.5
                    s = coll_next(c, tip, "O.k0", delm(a) if removed else setm(a, "writer", body[a]["uks"]))
                    cut.append((a, body[a]["uks"], "admin", removed))
                elif r < 0.8 and low:
                    v = rng.choice(low); signer = rng.choice(["O"] + admins)
                    s = coll_next(c, tip, body[signer]["uks"], delm(v))
                    if body[v]["role"] == "writer":
                        cut.append((v, body[v]["uks"], "writer", True))
                else:
                    s = coll_next(c, tip, "O.k0", setm("N%d" % n, "admin", "N%d.k0" % n)); n += 1
                assert c.add(s), s
                tip = s["id"]; everything.append(s)
            if not cut:
                continue
            honest_head, commit = tip, c.body(tip)["commit"]
            uid, key, was, removed = rng.choice(cut)
            writes = []
            for m in range(rng.randint(3, 20)):
                spots = [i for i in c.st if c.body(i)["members"].get(uid, {}).get("uks") == key]
                p = rng.choice(spots)
                writes.append({"id": "w%d" % m, "author": uid, "key": key, "stmt": p})
                if was != "admin" or c.body(p)["members"][uid]["role"] != "admin":
                    continue
                r = rng.random()
                if r < 0.5:
                    s = coll_next(c, p, key, setm("P%d" % m, "writer", "P%d.k0" % m), salt=str(m))
                elif r < 0.8:
                    others = [u for u in c.body(p)["members"] if u != uid]
                    s = coll_next(c, p, key, delm(rng.choice(others)), salt=str(m))
                else:
                    s = coll_next(c, p, key, setm(uid, "owner", key), salt=str(m))
                if c.add(s):
                    writes.append({"id": "p%d" % m, "author": "P%d" % m, "key": "P%d.k0" % m, "stmt": s["id"]})
                everything.append(s)
            self.assertEqual(c.head(), honest_head)
            for w in writes:
                if w["author"] != uid or removed:
                    self.assertFalse(write_valid(c, w, commit, {}), w)        # puppets, and removed members
                elif c.body(w["stmt"])["commit"] != commit:
                    self.assertFalse(write_valid(c, w, commit, {}), w)        # demoted: nothing backdated
                else:
                    self.assertEqual(c.body(w["stmt"])["members"][uid]["role"], "writer")
            gid = c.genesis_id
            self.assertEqual(head_after_shuffles(lambda: Chain(CollectionRules(), gid), everything, rng, rounds=8), {honest_head})
            checked += 1
        self.assertGreater(checked, 150)


# --- merge ------------------------------------------------------------------------------

def W(i, dev, hlc, epoch, ctx=None, base=None):
    return {"id": i, "dev": dev, "hlc": hlc, "epoch": epoch, "ctx": ctx or {}, "base": base or {}}


def ids(writes):
    return sorted(w["id"] for w in writes)


class Merge(unittest.TestCase):

    def test_any_contexts_any_order_same_result_never_empty(self):
        rng = random.Random(3)
        devs = ["a", "b", "c"]
        for _ in range(3000):
            ws = [W(i, rng.choice(devs), rng.randint(0, 5), rng.choice([0, 1, 2]),
                    {d: rng.randint(0, 5) for d in rng.sample(devs, rng.randint(0, 3))},
                    {d: rng.randint(0, 5) for d in rng.sample(devs, rng.randint(0, 3))})
                  for i in range(rng.randint(2, 6))]
            full = ids(prune(ws))
            self.assertTrue(full)
            for perm in itertools.permutations(ws):
                acc = []
                for w in perm:
                    acc = merge(acc, [w])
                self.assertEqual(ids(acc), full)

    def test_honest_devices_get_the_classic_result_across_epochs(self):
        rng = random.Random(4)
        for _ in range(3000):
            devs = ["a", "b", "c"]
            clock = dict.fromkeys(devs, 0); state = {d: [] for d in devs}; epoch = dict.fromkeys(devs, 0)
            allw = []
            for _ in range(rng.randint(3, 14)):
                d, r = rng.choice(devs), rng.random()
                if r < 0.45:
                    clock[d] += 1
                    w = author_write(state[d], len(allw), d, clock[d], epoch[d]); allw.append(w)
                    state[d] = merge(state[d], [w])
                elif r < 0.85:
                    o = rng.choice(devs)
                    state[d] = merge(state[d], state[o]); epoch[d] = max(epoch[d], epoch[o])
                else:
                    epoch[d] += 1
            if not allw:
                continue
            full = ids(prune(allw))
            order = allw[:]; rng.shuffle(order)
            acc = []
            for w in order:
                acc = merge(acc, [w])
            self.assertEqual(ids(acc), full)

            def seen(w, x):
                c = dict(w["ctx"])
                for dd, h in w["base"].items():
                    c[dd] = max(c.get(dd, -1), h)
                return w["id"] != x["id"] and c.get(x["dev"], -1) >= x["hlc"]
            self.assertEqual(sorted(x["id"] for x in allw if not any(seen(w, x) for w in allw)), full)

    def test_a_vandals_context_does_not_outlive_its_epoch(self):
        big = 10 ** 9
        vandal = W("v", "z", big, 0, {d: big for d in "abz"})
        honest = author_write([vandal], "h", "a", 1, 0)
        self.assertEqual(ids(merge([vandal], [honest])), ["h", "v"])          # stuck in conflict in epoch 0
        after = author_write(merge([vandal], [honest]), "n", "a", 2, 1)       # after removal and rotation
        self.assertEqual(ids(merge([vandal, honest], [after])), ["n"])
        later = author_write([after], "m", "b", 1, 1)
        self.assertEqual(ids(merge([after], [later])), ["m"])

    def test_first_round_counterexamples(self):
        sib = [W(i, "d%d" % i, 1, 0) for i in range(17)]                       # 17 siblings and a resolver
        res = W(99, "r", 1, 0, {"d%d" % i: 1 for i in range(1, 17)})
        one, two = [], []
        for w in sib + [res]:
            one = merge(one, [w])
        for w in [res] + sib:
            two = merge(two, [w])
        self.assertEqual(ids(one), ids(two))
        x, y = W(1, "p", 5, 0, {"q": 9}), W(2, "q", 5, 0, {"p": 9})            # each claims to cover the other
        self.assertEqual(ids(merge([x], [y])), [1, 2])


if __name__ == "__main__":
    unittest.main(verbosity=1)
