"""Differential test: the Lean oracle against model/owner_model.py on random histories.

Run from proofs/:  python3 -B difftest.py [runs] [seed]
Needs the oracle built (lake build tandem-oracle). Standard library only.

Each run builds a random roster history (valid and invalid statements, rotations with
inventories, conflicting successors, statements signed by others), random writes, messages,
invites and late writes; delivers a random subset in a random order to an OwnerLog; and asks
both models the same questions. Any disagreement is printed with the seed that produced it.
"""
import copy
import json
import pathlib
import random
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / "model"))

from owner_model import (META, OwnerLog, data_write, digest, dominated, inventory_bytes,  # noqa: E402
                         inventory_hash, invite_open, may_destroy, reissue, statement,
                         supersedes, write_valid)

USERS = ["O", "W", "R", "X"]
FIELDS = ["v", "é", "é"]          # one field in two Unicode spellings is two fields
RECORDS = [digest("r1")[:32], digest("r2")[:32], META]


def node(s):
    b = s["body"]
    via = b.get("via", {})
    return {"id": s["id"], "parent": s["parent"], "signer": s["signer"], "n": b["n"],
            "epoch": b["epoch"], "commit": b["commit"], "snapshot": b["snapshot"],
            "deleted": b["deleted"],
            "members": [{"uid": u, "role": r, "uka": "uka-" + u, "name": u,
                         **({"via": via[u]} if u in via else {})} for u, r in b["members"].items()]}


def wjson(w):
    return {"id": w["id"], "author": w["author"], "ref": w["ref"], "record": w["rec"],
            "field": w["field"], "value": w["value"], "dev": w["dev"], "hlc": w["hlc"],
            "ctx": w["ctx"], "base": w["base"]}


def vector(rng):
    if rng.random() < 0.02:
        return {"x%d" % i: 1 for i in range(1025)}       # over the limit
    return {rng.choice("abc"): rng.randint(0, 6) for _ in range(rng.randint(0, 3))}


def history(rng):
    """A random history: every statement any device might receive, and the writes made."""
    g = statement(None, "O", {"n": 0, "epoch": 0, "commit": "K0", "snapshot": None,
                              "deleted": False, "members": {"O": "owner"}})
    full = OwnerLog(g)
    sts, writes, inventories, keys = [g], [], {g["id"]: None}, 1
    for _ in range(rng.randint(1, 9)):
        parent = full.head() if rng.random() < 0.7 else rng.choice(sts)["id"]
        if parent not in full.st:
            continue
        pb = full.st[parent]["body"]
        body = copy.deepcopy(pb)
        body["n"] = pb["n"] + (1 if rng.random() < 0.95 else 2)
        members = body["members"]
        kind = rng.choice(["add", "add", "promote", "demote", "remove", "rotate", "rotate",
                           "rotate-add", "noop", "delete", "admin", "two-owners", "reuse-key",
                           "via-change"])
        others = [u for u in members if u != "O"]
        rotate = kind.startswith("rotate") or (kind in ("demote", "remove") and rng.random() < 0.8)
        if kind in ("add", "rotate-add"):
            u = rng.choice(["W", "R", "X", "Y"])
            members[u] = rng.choice(["writer", "reader"])
            if rng.random() < 0.6:
                body.setdefault("via", {})[u] = rng.choice(["i1", "i2"])
        elif kind == "promote" and others:
            members[rng.choice(others)] = "writer"
        elif kind == "demote" and others:
            members[rng.choice(others)] = "reader"
        elif kind == "remove" and others:
            u = rng.choice(others)
            members.pop(u)
            body.get("via", {}).pop(u, None)
        elif kind == "delete":
            body["deleted"] = True
        elif kind == "admin" and others:
            members[rng.choice(others)] = "admin"
        elif kind == "two-owners" and others:
            members[rng.choice(others)] = "owner"
        elif kind == "via-change" and body.get("via"):
            u = rng.choice(sorted(body["via"]))
            body["via"][u] = "i3"
        if rotate:
            line = set(full.line(parent))
            mine = [w for w in writes if w["ref"] in line]
            inv = {}
            for w in rng.sample(mine, rng.randint(0, len(mine))):
                inv.setdefault(w["rec"], []).append(w["id"])
            commit = "K%d" % (keys if kind != "reuse-key" else 0)
            keys += 1
            body.update(epoch=pb["epoch"] + 1, commit=commit, snapshot=inventory_hash(inv))
        signer = "O" if rng.random() < 0.9 else rng.choice(["W", "X"])
        s = statement(parent, signer, body, body["n"] + rng.randint(0, 1))
        sts.append(s)
        full.add(s)
        if rotate:
            inventories[s["id"]] = inv
        for _ in range(rng.randint(0, 4)):
            ref = rng.choice(sts)["id"] if rng.random() < 0.9 else "unknown"
            writes.append(data_write(rng.choice(USERS), ref, rng.choice(RECORDS),
                                     rng.choice("xyz"), rng.choice("abc"), rng.randint(0, 6),
                                     vector(rng), vector(rng) if rng.random() < 0.4 else {},
                                     field=rng.choice(FIELDS)))
    return g, sts, writes, inventories


def deliver(rng, g, sts):
    """What one device ends up holding, in the order it arrived."""
    got = [s for s in sts[1:] if rng.random() < 0.85]
    rng.shuffle(got)
    log = OwnerLog(g)
    for s in got:
        log.add(s)
    return log, [g] + got


class Oracle:
    def __init__(self):
        exe = HERE / ".lake" / "build" / "bin" / "tandem-oracle"
        self.p = subprocess.Popen([str(exe)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)

    def ask(self, req):
        self.p.stdin.write(json.dumps(req) + "\n")
        self.p.stdin.flush()
        out = json.loads(self.p.stdout.readline())
        if "error" in out:
            raise RuntimeError(out["error"])
        return out

    def close(self):
        self.p.stdin.close()
        self.p.wait()


STATS = {}


def seen(what, n=1):
    STATS[what] = STATS.get(what, 0) + n


def compare(rng, oracle, seed):
    g, sts, writes, inventories = history(rng)
    log, held = deliver(rng, g, sts)
    base = {"genesis": g["id"], "statements": [node(s) for s in held], "meta": META,
            "writes": [wjson(w) for w in writes]}
    bad = []

    def check(what, mine, theirs):
        if mine != theirs:
            bad.append("seed %d: %s: python %r, lean %r" % (seed, what, mine, theirs))

    following = [w for w in writes if w["ref"] in log.st]
    # messages for the clean-up rule; hashes collide on purpose sometimes
    msgs = []
    for i in range(rng.randint(0, 5)):
        ws = rng.sample(following, rng.randint(0, min(3, len(following))))
        msgs.append({"id": "m%d" % i, "hash": rng.randint(0, 3), "writes": [w["id"] for w in ws]})
    invites = [{"id": i, "closed": rng.random() < 0.2, "maxUses": rng.randint(1, 3)} for i in ("i1", "i2")]
    late = rng.sample(following, min(4, len(following)))
    states = {w["id"]: rng.sample(following, rng.randint(0, min(3, len(following)))) for w in late}
    req = dict(base, messages=msgs, invites=invites,
               reissue=[{"write": w["id"], "state": [x["id"] for x in states[w["id"]]]} for w in late])
    out = oracle.ask(req)

    seen("dead", log.frozen)
    seen("supersession pairs", len(out["supersedes"]))
    seen("may-destroy pairs", len(out["mayDestroy"]))
    seen("mutual domination", sum(1 for a, b in out["dominated"] if [b, a] in out["dominated"]))
    seen("invites open", sum(out["invites"].values()))
    check("accepted", sorted(log.st), sorted(out["accepted"]))
    check("dead", log.frozen, out["dead"])
    check("head", log.head(), out["head"])
    check("epochs", {s: log.epoch(s) for s in log.st}, out["epochs"])
    check("supersedes", sorted([w["id"], x["id"]] for w in following for x in following
                               if supersedes(log, w, x)), sorted(out["supersedes"]))
    by = {w["id"]: w for w in writes}
    mw = {m["id"]: [by[i] for i in m["writes"]] for m in msgs}
    check("dominated", sorted([a, b] for a in mw for b in mw if a != b and dominated(log, mw[a], mw[b])),
          sorted(out["dominated"]))
    hs = {m["id"]: m["hash"] for m in msgs}
    check("mayDestroy", sorted([a, b] for a in mw for b in mw
                               if a != b and may_destroy(log, mw[a], mw[b], hs[a], hs[b])),
          sorted(out["mayDestroy"]))
    for inv in invites:
        check("invite " + inv["id"], invite_open(log, inv), out["invites"][inv["id"]])
    for w in late:
        try:
            new = reissue(log, w, states[w["id"]])
            mine = "skip" if new is None else "new"
        except ValueError:
            new, mine = None, "refused"
        theirs = out["reissue"][w["id"]]
        check("reissue status " + w["id"][:8], mine, theirs["status"])
        seen("reissue " + mine)
        if new is not None and theirs["status"] == "new":
            t = theirs["write"]
            check("reissue write", (new["ref"], sorted(new["base"].items()), new["dev"], new["hlc"],
                                    new["field"], new["rec"], new["author"], new["value"]),
                  (t["ref"], sorted((d, n) for d, n in t["base"]), t["dev"], t["hlc"],
                   t["field"], t["record"], t["author"], t["value"]))
    # eligibility in every epoch the device follows
    for e in sorted({log.epoch(s) for s in log.st}):
        inv = inventories.get(e)
        r = oracle.ask(dict(base, channel=e, inventory=inv))
        mine = sorted(w["id"] for w in writes if write_valid(log, w, e, inv))
        seen("eligible through an inventory", sum(1 for w in writes if w["id"] in mine and log.epoch(w["ref"]) != e))
        check("eligible in " + e[:8], mine, sorted(r["eligible"]))
    # inventory bytes
    inv = {}
    for w in rng.sample(writes, min(len(writes), rng.randint(0, 6))):
        inv.setdefault(w["rec"], [])
        if w["id"] not in inv[w["rec"]]:
            inv[w["rec"]].append(w["id"])
    r = oracle.ask({"genesis": g["id"], "statements": [node(g)], "inventoryBytes": inv})
    check("inventory bytes", inventory_bytes(inv).hex(), r["inventoryBytes"])
    return bad


def main():
    runs = int(sys.argv[1]) if len(sys.argv) > 1 else 500
    seed0 = int(sys.argv[2]) if len(sys.argv) > 2 else 1
    oracle = Oracle()
    failures = []
    try:
        for seed in range(seed0, seed0 + runs):
            failures += compare(random.Random(seed), oracle, seed)
    finally:
        oracle.close()
    for f in failures[:20]:
        print(f)
    print("covered: " + ", ".join("%s %d" % (k, v) for k, v in sorted(STATS.items())))
    print("%d runs, %d disagreements" % (runs, len(failures)))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
