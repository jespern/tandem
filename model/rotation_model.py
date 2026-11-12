"""HISTORICAL ranked-authority rotation model, superseded by owner_model.py.

Retained as research history, not validation of the current DESIGN.md.

Model of the previous key rotation procedure over a hub.

Builds on tandem_model: the same roster rules, fork choice and write validity, plus
what that model leaves out: channels on a hub, grants and key requests in inboxes,
devices that sync at their own pace, a rotation that can stop after any step, and
someone with the hub credential deleting things.

Still a model: no cryptography (a channel is readable by whoever holds its key
commitment), no merge of field values (a record is one write), no timing (a "round"
is every live device syncing once).

Run the tests:  python3 model/test_rotation_model.py
"""
import copy

from tandem_model import Chain, CollectionRules, ROLE, epoch_statement, statement, write_valid


class Hub:
    """One mail folder: channels of messages, and per-user inboxes."""

    def __init__(self):
        self.stmts = {}      # statement ID -> statement (what a roster message contains)
        self.writes = {}     # write ID -> write
        self.roster = {}     # channel -> {statement ID}
        self.data = {}       # channel -> {write ID}
        self.snaps = {}      # channel -> {(record, epoch statement): [(ids, key)]}
        self.notices = {}    # channel -> [(uid, statement ID)]   leave notices
        self.inbox = {}      # uid -> [message]
        self.shown = {}      # channel -> {(successor ID, uid, key)}: who signed behind a fork's successor
        self.home = {}       # uid -> epoch keys in that user's home channel, shared by their devices (§6.3)
        self.counter = 0

    def fresh(self, prefix):
        self.counter += 1
        return "%s%d" % (prefix, self.counter)

    def post(self, uid, message):
        self.inbox.setdefault(uid, []).append(message)

    def commit_of(self, sid):
        return self.stmts[sid]["body"]["commit"]

    def starts_epoch(self, s):
        return s["parent"] is None or self.commit_of(s["parent"]) != s["body"]["commit"]

    def earlier_epochs(self, s):
        """Key commitments of the epochs before this statement's own, nearest first."""
        out, p = [], s["parent"]
        while p is not None:
            c = self.commit_of(p)
            if c != s["body"]["commit"] and c not in out:
                out.append(c)
            p = self.stmts[p]["parent"]
        return out


class Device:
    def __init__(self, name, uid, key, hub, genesis):
        self.name, self.uid, self.key, self.hub = name, uid, key, hub
        self.chain = Chain(CollectionRules(), genesis["id"])
        self.keys = set()        # epoch keys held, this one and earlier ones (home keeps them, §6.3)
        self.current = None      # the epoch this device reads and writes
        self.replica = {}        # write ID -> write, valid in the current epoch
        self.queue = []          # logical writes made while it couldn't publish
        self.intents = {}        # member this device decided to remove -> the statement it made for that, if any
        self.waited = 0          # rounds spent knowing of an epoch without its key
        self.alive, self.removed = True, False

    # --- reading ---------------------------------------------------------------------

    def role(self, body=None):
        body = body or self.chain.body(self.chain.head())
        m = body["members"].get(self.uid)
        return ROLE[m["role"]] if m else -1

    def announced(self, s, channel):
        """May this device take statement `s`, found in `channel`, into its chain? (§11.6)

        A statement that starts an epoch counts only when found in the channel of an
        earlier epoch on its own line: that is its announcement. A copy in its own
        channel counts for nothing. The one who can't check is a device whose user
        wasn't a member before it.
        """
        if not self.hub.starts_epoch(s):
            return channel == s["body"]["commit"]
        if s["parent"] is None:
            return True
        if channel in self.hub.earlier_epochs(s):
            return True
        before = self.hub.stmts[s["parent"]]["body"]
        return self.uid not in before["members"]

    def read_rosters(self):
        hub, progress = self.hub, True
        while progress:
            progress = False
            for channel in sorted(self.keys):
                for sid in sorted(hub.roster.get(channel, ()), key=lambda i: len(hub.earlier_epochs(hub.stmts[i]))):
                    s = hub.stmts[sid]
                    if sid not in self.chain.st and self.announced(s, channel):
                        if self.chain.add(s):
                            progress = True

    def read_shown(self):
        """Signatures behind a fork's successor, shown in the channel of the forking statement."""
        for channel in self.keys:
            for sid, uid, key in self.hub.shown.get(channel, ()):
                s = self.chain.st.get(sid)
                if s is None or s["parent"] is None:
                    continue
                at = self.chain.body(s["parent"])
                if at["commit"] == channel and at["members"].get(uid, {}).get("uks") == key:
                    self.chain.shown.setdefault(sid, set()).add((uid, key))

    def show(self):
        """Wherever this device sees a fork, publish who of rank has signed behind each successor.

        Anyone who can see the signature can do this; it needn't be the signer. It lets
        a device that can't read the later channels (a removed member, or one whose
        grant was lost) reach the same verdict as everyone else.
        """
        hub, chain = self.hub, self.chain
        for pid, kids in chain.kids.items():
            if pid is None or len(kids) < 2 or hub.commit_of(pid) not in self.keys:
                continue
            at = chain.body(pid)
            for kid in kids:
                for i in chain.subtree(kid):
                    s = chain.st[i]
                    for uid, key in chain.rules.signers(chain.body(s["parent"]), s):
                        m = at["members"].get(uid)
                        if m and m["uks"] == key and ROLE[m["role"]] >= ROLE["admin"]:
                            hub.shown.setdefault(at["commit"], set()).add((kid, uid, key))

    def pull(self):
        """Merge the current channel's valid writes."""
        hub = self.hub
        snaps = hub.snaps.get(self.current, {})
        for wid in hub.data.get(self.current, ()):
            w = hub.writes[wid]
            if wid not in self.replica and write_valid(self.chain, w, self.current, snaps):
                self.replica[wid] = w

    # --- writing ---------------------------------------------------------------------

    def publish(self, w):
        self.hub.writes[w["id"]] = w
        self.hub.data.setdefault(self.current, set()).add(w["id"])
        self.replica[w["id"]] = w

    def sign(self, lid, stmt, wid=None):
        return {"id": wid or self.hub.fresh("w"), "lid": lid, "rec": lid, "author": self.uid, "key": self.key, "stmt": stmt}

    def write(self):
        """The user edits something. Returns the logical write's name."""
        self.sync()
        if not self.alive or self.removed:
            return None
        lid = self.hub.fresh("edit")
        head = self.chain.head()
        if self.current == self.chain.body(head)["commit"] and self.role() >= ROLE["writer"]:
            self.publish(self.sign(lid, head))
        else:
            self.queue.append(lid)       # never publish to an epoch that has been left behind
        return lid

    def move(self, commit):
        """Take up a new epoch: its state is the collection's state; sign again what is missing."""
        old, self.current, self.replica, self.waited = self.replica, commit, {}, 0
        self.pull()
        have = {w["lid"] for w in self.replica.values()}
        first = epoch_statement(self.chain, commit, line=set(self.chain.line(self.chain.head())))
        if self.role() >= ROLE["writer"]:
            for w in old.values():       # the user's own writes, whichever of their devices made them
                if w["author"] == self.uid and w["lid"] not in have:
                    # Same write, new statement: every device of the user produces the same ID.
                    self.publish(self.sign(w["lid"], first, wid="%s@%s" % (w["lid"], first[:8])))

    # --- one round -------------------------------------------------------------------

    def sync(self):
        if not self.alive:
            return
        hub = self.hub
        for m in list(hub.inbox.get(self.uid, ())):
            if m["type"] == "grant":
                self.keys |= set(m["keys"])                  # a key opens a channel; it doesn't make it current
        home = hub.home.setdefault(self.uid, set())          # home keeps every epoch key the user was given
        home |= self.keys
        self.keys |= home
        self.read_rosters()
        self.read_shown()
        self.show()
        head = self.chain.head()
        body = self.chain.body(head)
        if self.uid not in body["members"]:
            # Removed, as far as this device can tell. It stops, but keeps its replica for a
            # while: the line that removed it may yet lose, and then nothing must be missing.
            self.removed, self.current = True, None
            return
        self.removed = False
        want = body["commit"]
        if want != self.current:
            if want in self.keys:
                self.move(want)
            else:
                self.waited += 1
                if self.waited == 2:                         # the safety net behind the grant (§11.4)
                    for uid, m in body["members"].items():
                        if ROLE[m["role"]] >= ROLE["admin"] and uid != self.uid:
                            hub.post(uid, {"type": "keyreq", "by": self.uid, "have": self.current})
        if self.current == want:
            self.pull()
            for wid, w in self.replica.items():              # healing: the hub lacks something this device holds
                hub.data.setdefault(self.current, set()).add(wid)
                hub.writes[wid] = w
            if self.queue and self.role() >= ROLE["writer"]:
                for lid in self.queue:
                    self.publish(self.sign(lid, head))
                self.queue = []
        for sid in self.chain.line(head):                    # keep the announcements it relied on alive
            s = self.chain.st[sid]
            if s["parent"] is not None and hub.starts_epoch(s):
                where = [c for c in hub.earlier_epochs(s) if c in self.keys]
                if where:
                    hub.roster.setdefault(where[0], set()).add(sid)
        for pid, kids in self.chain.kids.items():            # competing announcements belong side by side:
            starts = [k for k in kids if pid is not None and hub.starts_epoch(self.chain.st[k])]
            if len(starts) > 1:                              # wherever one of them is, put the others too
                for c in hub.earlier_epochs(self.chain.st[starts[0]]):
                    if c in self.keys and any(k in hub.roster.get(c, ()) for k in starts):
                        hub.roster[c] |= set(starts)

    def serve(self):
        """An owner's or admin's device answers key requests, and acts on what is due."""
        if not self.alive or self.removed or self.role() < ROLE["admin"]:
            return
        hub, head = self.hub, self.chain.head()
        body = self.chain.body(head)
        on_head = self.current == body["commit"]
        if on_head:
            for m in list(hub.inbox.get(self.uid, ())):
                if m["type"] == "keyreq":
                    hub.inbox[self.uid].remove(m)
                    # Every epoch since theirs that they were a member of, so that they can follow the
                    # announcements, up to the one that removed them if one did.
                    line = self.chain.line(head)
                    commits = [hub.commit_of(i) for i in line]
                    start = commits.index(m["have"]) + 1 if m["have"] in commits else 0
                    theirs = [hub.commit_of(i) for i in line[start:] if m["by"] in self.chain.body(i)["members"]]
                    keys = [c for c in dict.fromkeys(theirs) if c in self.keys]
                    if keys:
                        hub.post(m["by"], {"type": "grant", "keys": keys})
            # A device remembers the changes it made. One whose statement is no longer on the
            # head's line, and isn't reflected there, is made again (§9.4).
            line = set(self.chain.line(head))
            due = {uid for uid, sid in self.intents.items() if sid not in line}
            due |= {uid for uid, at in hub.notices.get(self.current, ()) if hub.commit_of(at) == self.current}
            for uid in sorted(due):
                target = body["members"].get(uid)
                if target is not None and ROLE[target["role"]] < self.role():
                    s = self.rotate(lambda members, uid=uid: members.pop(uid, None))
                    if s and uid in self.intents:
                        self.intents[uid] = s["id"]
                    return
        elif self.waited >= 6 and self.current is not None:
            self.rotate(lambda members: None, takeover=True)  # announced, but nobody has its key (§11.6)

    # --- rotation --------------------------------------------------------------------

    def rotate(self, edit, stop_after=None, grants_sent=None, stale=False, takeover=False):
        """The six steps of §11.6. `stop_after` (3, 4 or 5) is where the device dies."""
        hub = self.hub
        if not stale:
            self.sync()                                                         # 1. sync fully
        if not self.alive or self.removed:
            return None
        head = self.chain.head()
        before = self.chain.body(head)
        if not takeover and self.current != before["commit"]:
            return None
        body = copy.deepcopy(before)
        edit(body["members"])
        if body == before and not takeover:
            return None                                                         # nothing left to do
        body["epoch"] += 1
        body["commit"] = new = hub.fresh("K")                                   # 2. a new key, and the statement
        s = statement(head, self.key, body)
        if not self.chain.rules.valid(before, s):
            return None
        hub.stmts[s["id"]] = s
        self.keys.add(new)
        hub.roster[new] = set(self.chain.line(head)) | {s["id"]}                # 3. the new channel
        hub.data[new] = set(self.replica)
        my_key = body["members"][self.uid]["uks"]
        on_line = set(self.chain.line(head))
        hub.snaps[new] = {(w["rec"], s["id"]): [({wid}, my_key)] for wid, w in self.replica.items() if w["stmt"] in on_line}
        if stop_after == 3:
            self.alive = False
            return s
        for n, uid in enumerate(sorted(body["members"])):                       # 4. grants
            if stop_after == 4 and n == grants_sent:
                self.alive = False
                return s
            hub.post(uid, {"type": "grant", "keys": [new]})
        where = [c for c in hub.earlier_epochs(s) if c in self.keys][0]
        hub.roster.setdefault(where, set()).add(s["id"])                        # 5. the announcement
        if stop_after == 5:
            self.alive = False
            return s
        hub.data[where] = set()                                                 # 6. the old data goes
        self.sync()
        return s


def new_collection(hub, members):
    """members: [(uid, role, [device names])]. The first is the owner. Returns {name: Device}."""
    owner = members[0][0]
    body = {"epoch": 0, "commit": hub.fresh("K"), "deleted": False,
            "members": {owner: {"role": "owner", "uks": owner + ".k"}}}
    genesis = statement(None, owner + ".k", body)
    hub.stmts[genesis["id"]] = genesis
    tip = genesis
    for uid, role, _ in members[1:]:
        body = copy.deepcopy(tip["body"])
        body["members"][uid] = {"role": role, "uks": uid + ".k"}
        tip = statement(tip["id"], owner + ".k", body)
        hub.stmts[tip["id"]] = tip
    channel = genesis["body"]["commit"]
    line, s = [], tip
    while s is not None:
        line.append(s["id"])
        s = hub.stmts[s["parent"]] if s["parent"] else None
    hub.roster[channel] = set(line)
    devices = {}
    for uid, _, names in members:
        for name in names:
            d = Device(name, uid, uid + ".k", hub, genesis)
            d.keys.add(channel)
            d.sync()
            devices[name] = d
    return devices


def settle(devices, rng=None, rounds=60):
    """Let every live device sync until nothing changes any more. Returns the rounds it took."""
    def picture():
        return [(d.name, d.current, d.removed, d.chain.head(), tuple(sorted(d.replica)), len(d.queue)) for d in devices.values()]
    quiet, last = 0, None
    for n in range(rounds):
        order = list(devices.values())
        if rng:
            rng.shuffle(order)
        for d in order:
            d.sync()
            d.serve()
        now = picture()
        quiet = quiet + 1 if now == last else 0
        last = now
        if quiet >= 8:
            return n
    return rounds
