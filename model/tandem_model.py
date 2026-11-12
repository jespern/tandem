"""HISTORICAL ranked-authority model, superseded by owner_model.py.

Retained for earlier identity/merge experiments, not current collection validation.

Executable model of the previous authorization and merge rules.

Mirrors DESIGN.md: the user chain (§6.2), the collection chain and data-write
validity (§8), fork choice (§9.4) and the merge rule for data (§9.2).

It is a model, not an implementation: no cryptography and no storage. A key is
a string, "signed by K" is the name K, and an ID is a hash of the content.

Run the tests:  python3 model/test_tandem_model.py
"""
import hashlib
import json

ROLE = {"reader": 0, "writer": 1, "admin": 2, "owner": 3}


def digest(parent, body, salt=""):
    return hashlib.sha256(json.dumps([parent, body, salt], sort_keys=True).encode()).hexdigest()


def prove(uid, key, parent, body, salt=""):
    """A co-signature, by `key`, over this exact statement: its parent, its whole body, and
    the write metadata (`salt` stands in for the dot) that goes into its ID.

    Only the holder of `key` can make one. Anyone can copy one, which is why it covers
    everything that identifies the statement: on any other statement it is worthless,
    and nobody can turn it into a second statement with the same content.
    """
    return [uid, key, digest(parent, body, salt)]


def statement(parent, signer, body, proofs=(), salt=""):
    """A signed statement: the complete state after a change, pointing at its parent.

    `proofs` are extra signatures made with `prove`: a member who changes their own
    key also signs the statement with the new one.
    """
    s = {"parent": parent, "signer": signer, "body": body, "proofs": sorted(map(list, proofs)), "salt": salt}
    raw = json.dumps([parent, signer, body, s["proofs"], salt], sort_keys=True)
    s["id"] = hashlib.sha256(raw.encode()).hexdigest()
    return s


class Chain:
    """Statements of one scope (a user, or a collection), with fork choice.

    A statement is judged against its parent only, so its validity never changes.
    Which line is current is a separate question, answered by `head()`.
    """

    def __init__(self, rules, genesis_id):
        self.rules = rules
        self.genesis_id = genesis_id      # the scope's ID is the ID of its genesis
        self.st = {}                      # accepted statements, by ID
        self.kids = {}
        self.held = {}                    # statements waiting for their parent
        self.shown = {}                   # successor ID -> signers shown to be behind it (§9.4), for
                                          # devices that can't read the statements themselves

    def add(self, s):
        """True: accepted. False: invalid, dropped. None: held until its parent arrives."""
        if s["id"] in self.st:
            return True
        p = s["parent"]
        if p is None:
            ok = s["id"] == self.genesis_id and self.rules.valid(None, s)
        elif p not in self.st:
            self.held.setdefault(p, []).append(s)
            return None
        else:
            ok = self.rules.valid(self.st[p]["body"], s) and self.rules.line_ok(self, s)
        if not ok:
            return False
        self.st[s["id"]] = s
        self.kids.setdefault(p, []).append(s["id"])
        for h in self.held.pop(s["id"], []):
            self.add(h)
        return True

    def body(self, i):
        return self.st[i]["body"]

    def line(self, i):
        out = []
        while i is not None:
            out.append(i)
            i = self.st[i]["parent"]
        return out[::-1]

    def subtree(self, i):
        out, todo = [], [i]
        while todo:
            n = todo.pop()
            out.append(n)
            todo += self.kids.get(n, [])
        return out

    def score(self, child, base):
        """What stands behind one successor of a statement: itself and everything built on it.

        The score depends only on that successor's own subtree and on the state it forks
        from, never on the sibling it is compared with. Choosing is therefore a plain
        maximum over a total order: no cycles, and nothing to gain from picking IDs.
        """
        signers = set()
        for i in self.subtree(child):
            s = self.st[i]
            signers |= set(self.rules.signers(self.st[s["parent"]]["body"], s))
        signers |= self.shown.get(child, set())
        ranks = {p: self.rules.rank(p, base) for p, _ in signers}
        top = max(ranks.values())
        newest = 0
        if top == self.rules.GENERATION_RANK:                # only where one user holds the rank
            newest = max(self.rules.generation(p, key) for p, key in signers if ranks[p] == top)
        takes = self.rules.takes_away(base, self.st[child]["body"])
        others = sorted((r for r in ranks.values() if r > self.rules.NO_RANK), reverse=True)
        return (top, newest, takes, others if self.rules.OTHERS_COUNT else [])

    def head(self):
        """The current statement (DESIGN.md §9.4): walk down from the genesis, and where a
        statement has several successors follow the one with the best score:

        1. the highest rank, as of the forking statement, of anyone who signed behind it;
        2. in a roster, if that rank is the owner's: the newest generation of the owner's key that signed;
        3. a successor that takes something away, over one that only adds;
        4. in a user chain: the ranks of everyone else who signed behind it;
        5. the lowest ID.
        """
        i = self.genesis_id
        while self.kids.get(i):
            base = self.st[i]["body"]
            i = max(sorted(self.kids[i]), key=lambda c: self.score(c, base))   # ties: lowest ID
        return i


class UserRules:
    """A user's device chain (§6.2).

    body = {"recovery": key, "devices": [key, oldest first], "gone": [key], "puk": n, "uks": key}
    """
    NO_RANK = (0, 0)
    OTHERS_COUNT = True          # every device has its own key, so who else signed is telling
    GENERATION_RANK = None

    def valid(self, P, s):
        B, signer = s["body"], s["signer"]
        if P is None:                                        # genesis: one device, which signs
            return B["devices"] == [signer] and B["gone"] == [] and B["puk"] == 0
        if signer != P["recovery"] and signer not in P["devices"]:
            return False
        for _, key, made_for in s["proofs"]:                 # co-signatures: by a listed device, for this statement
            if key not in P["devices"] or made_for != digest(s["parent"], B, s["salt"]):
                return False
        old, new = P["devices"], B["devices"]
        kept = [d for d in old if d in new]
        if new[:len(kept)] != kept or len(set(new)) != len(new):
            return False                                     # order is seniority; additions go last
        added, removed = new[len(kept):], [d for d in old if d not in new]
        if any(d in P["gone"] or d == B["recovery"] for d in added):
            return False                                     # a removed device never returns
        if B["gone"] != sorted(set(P["gone"]) | set(removed)):
            return False
        for r in removed:                                    # who may remove whom
            senior = signer in old and old.index(signer) < old.index(r)
            if not (signer == P["recovery"] or signer == r or senior):
                return False
        new_recovery = B["recovery"] != P["recovery"]
        if new_recovery and (signer != P["recovery"] or not s["proofs"]):
            return False                                     # the recovery key replaces itself, and a device co-signs
        rotated = B["puk"] == P["puk"] + 1 and B["uks"] != P["uks"]
        unchanged = B["puk"] == P["puk"] and B["uks"] == P["uks"]
        must_rotate = new_recovery or any(r != signer for r in removed)
        return rotated if must_rotate else (rotated or unchanged)

    def signers(self, P, s):
        return [(s["signer"], s["signer"])] + [(key, key) for _, key, _ in s["proofs"]]

    def rank(self, key, base):
        if key == base["recovery"]:
            return (2, 0)
        if key in base["devices"]:
            return (1, -base["devices"].index(key))          # older outranks newer
        return (0, 0)

    def generation(self, key, _):
        return 0

    def line_ok(self, chain, s):
        return True

    def takes_away(self, P, B):
        return B["recovery"] != P["recovery"] or any(d not in B["devices"] for d in P["devices"])


class CollectionRules:
    """A collection's roster chain (§8).

    body = {"epoch": n, "commit": key commitment, "members": {uid: {"role", "uks"}}, "deleted": bool}
    A member is the user key the roster lists. Validity takes one thing from a user's chain,
    and it can't change: `certified(uid, key)`, that the chain has named that key. Which line
    of a user chain is current matters only when choosing between competing roster lines:
    `generation(uid, key)` says how new a key of the owner is.
    """
    NO_RANK = (0,)
    OTHERS_COUNT = False         # a user's devices share one key; a lower rank must not tip a fork
    GENERATION_RANK = (1 + ROLE["owner"],)

    def __init__(self, generation=lambda uid, key: 0, certified=lambda uid, key: True):
        self.generation = generation
        self.certified = certified   # "some valid statement of uid's chain names this key": once true, always true

    @staticmethod
    def who(body, key):
        for uid, m in body["members"].items():
            if m["uks"] == key:
                return uid
        return None

    def valid(self, P, s):
        B, signer = s["body"], s["signer"]
        members = B["members"]
        owners = [u for u, m in members.items() if m["role"] == "owner"]
        keys = [m["uks"] for m in members.values()]
        if len(owners) != 1 or len(set(keys)) != len(keys):
            return False
        for uid, key, made_for in s["proofs"]:               # by the key now listed, and for this statement
            if uid not in members or members[uid]["uks"] != key or made_for != digest(s["parent"], B, s["salt"]):
                return False
        proved = [[uid, key] for uid, key, _ in s["proofs"]]
        for u, m in members.items():                         # every key that is new here needs its certificate
            old = P["members"].get(u) if P else None
            if (old is None or old["uks"] != m["uks"]) and not self.certified(u, m["uks"]):
                return False
        if P is None:
            return list(members) == owners and members[owners[0]]["uks"] == signer and B["epoch"] == 0
        if P["deleted"]:
            return False
        A = self.who(P, signer)
        role = P["members"][A]["role"] if A else None
        if role not in ("owner", "admin"):
            return False                                     # writers and readers sign nothing
        reducing = False
        for u in set(P["members"]) | set(members):
            o, n = P["members"].get(u), members.get(u)
            if o == n:
                continue
            rekeyed = o is not None and n is not None and o["uks"] != n["uks"]
            if n is None or rekeyed or (o is not None and ROLE[n["role"]] < ROLE[o["role"]]):
                reducing = True
            if u == A:
                if n is None:
                    return False                             # nobody removes themselves: ask to leave
                if rekeyed and [u, n["uks"]] not in proved:
                    return False
                if role == "admin" and ROLE[n["role"]] > ROLE["admin"]:
                    return False
            elif role == "owner":
                if n is not None and n["role"] == "owner" and o is None:
                    return False                             # ownership goes to an existing member
            else:                                            # an admin, acting on someone else
                if o is not None and ROLE[o["role"]] >= ROLE["admin"]:
                    return False
                if n is not None and ROLE[n["role"]] >= ROLE["admin"]:
                    return False
        rotated = B["commit"] != P["commit"] and B["epoch"] == P["epoch"] + 1
        same_key = B["commit"] == P["commit"] and B["epoch"] == P["epoch"]
        if rotated and any(u not in P["members"] for u in members):
            return False                                     # nobody joins in the statement that starts an epoch
        if B["deleted"] and role != "owner":
            return False
        return rotated if reducing else (rotated or same_key)

    def signers(self, P, s):
        out = [(self.who(P if P is not None else s["body"], s["signer"]), s["signer"])]
        out += [(uid, key) for uid, key, _ in s["proofs"]]
        return out

    def rank(self, uid, base):
        m = base["members"].get(uid)
        return (1 + ROLE[m["role"]],) if m else (0,)

    def line_ok(self, chain, s):
        """A new key commitment must be new: no earlier statement on this line may carry it."""
        parent = chain.st[s["parent"]]["body"]
        if s["body"]["commit"] == parent["commit"]:
            return True
        return all(chain.st[i]["body"]["commit"] != s["body"]["commit"] for i in chain.line(s["parent"]))

    def takes_away(self, P, B):
        for u, o in P["members"].items():
            n = B["members"].get(u)
            if n is None or n["uks"] != o["uks"] or ROLE[n["role"]] < ROLE[o["role"]]:
                return True
        return False


def epoch_statements(chain, commit):
    """Every statement that starts an epoch with this key.

    There is normally one. A member who signs two statements with one key makes two,
    on two lines; nothing here may depend on which of them a device happened to see first.
    """
    out = []
    for i, s in chain.st.items():
        p = s["parent"]
        if s["body"]["commit"] == commit and (p is None or chain.st[p]["body"]["commit"] != commit):
            out.append(i)
    return sorted(out)


def epoch_statement(chain, commit, line=None):
    """The start of an epoch: on the given line if one is given, else the only one."""
    found = epoch_statements(chain, commit)
    if line is not None:
        found = [i for i in found if i in line]
    return found[0] if found else None


def write_valid(chain, w, channel_commit, snapshots):
    """Is data write `w` valid in the channel of the epoch whose key commits to `channel_commit`? (§8.3)

    w = {"id", "author": uid, "key": signing key, "stmt": ID of the statement it was written under}
    snapshots = {(record, epoch statement ID): [(set of write IDs, signer key), ...]}

    The verdict uses only the statement the write names, the statement that started the
    epoch, and that epoch's snapshot lists. None of these can change, so neither can the verdict.
    Returns None while the statement the write names hasn't arrived (the write is held).
    """
    S = chain.st.get(w["stmt"])
    if S is None:
        return None
    m = S["body"]["members"].get(w["author"])
    if m is None or m["uks"] != w["key"] or ROLE[m["role"]] < ROLE["writer"]:
        return False                                         # not written by someone who could write
    if S["body"]["commit"] == channel_commit:
        return True                                          # written in this epoch
    starts = epoch_statements(chain, channel_commit)
    if not starts:
        return None
    for R in starts:                                         # a list names the start it was made for
        if w["stmt"] not in chain.line(R):
            continue                                         # only that line's own past can be carried over
        first = chain.st[R]
        before = chain.st[first["parent"]]["body"] if first["parent"] else first["body"]
        rotator = first["body"]["members"][CollectionRules.who(before, first["signer"])]["uks"]
        if any(w["id"] in ids and key == rotator for ids, key in snapshots.get((w.get("rec"), R), [])):
            return True
    return False


# --- Merge rule for data records (§9.2) ------------------------------------------------
# write = {"id", "dev", "hlc", "epoch", "ctx": {dev: hlc}, "base": {dev: hlc}}

def covers(vector, other):
    return all(vector.get(d, -1) >= h for d, h in other.items())


def reach(w):
    r = dict(w["ctx"])
    r[w["dev"]] = max(r.get(w["dev"], -1), w["hlc"])
    return r


def supersedes(w, x):
    if w["id"] == x["id"]:
        return False
    if w["epoch"] > x["epoch"]:
        return covers(w["base"], reach(x)) and covers(w["base"], x["base"])
    if w["epoch"] == x["epoch"]:
        def includes(a, b):
            return covers(a["ctx"], reach(b)) and covers(a["base"], b["base"])
        return includes(w, x) and not includes(x, w)
    return False


def prune(writes):
    return [x for x in writes if not any(supersedes(w, x) for w in writes)]


def merge(a, b):
    seen = {}
    for w in list(a) + list(b):
        seen[w["id"]] = w
    return sorted(prune(list(seen.values())), key=lambda w: w["id"])


def author_write(kept, wid, dev, hlc, epoch):
    """An honest device writes a field: it covers everything it has kept."""
    ctx, base = {}, {}
    for k in kept:
        target = ctx if k["epoch"] == epoch else base
        for d, h in reach(k).items():
            target[d] = max(target.get(d, -1), h)
        for d, h in k["base"].items():
            base[d] = max(base.get(d, -1), h)
    return {"id": wid, "dev": dev, "hlc": hlc, "epoch": epoch, "ctx": ctx, "base": base}
