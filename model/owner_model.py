"""Symbolic model of DESIGN.md's collection protocol: one owner, fixed for the collection's life.

A user is one key, held by all their devices. The owner's key signs the roster.
Two signed successors of one statement kill the collection; nothing chooses
between them. The owner's devices keep out of each other's way with a note in
the owner's home channel.

Keys/signatures are symbols, not cryptography: a user ID stands for the user's
key, and "signed by" is a name. JSON encoding below makes model identities
deterministic; the protocol's binary encodings remain in DESIGN.md.
No mail binding, pairing or production storage. Of invitations, only what the roster decides.
"""
import base64
import copy
import hashlib
import json
import re
import struct

ROLES = {"reader": 0, "writer": 1, "owner": 2}
VECTOR_LIMIT = 1024  # entries in a write's context or base; more than this and the write is invalid (§9.2)


def digest(value):
    raw = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(raw).hexdigest()


def record(name):
    return hashlib.sha256(name.encode()).digest()[:16].hex()


META = record("_meta")  # the collection's name, icon and the like: an ordinary record, owner only


def strict_json(raw):
    """Protocol JSON as every client must read it (§10.5): nothing that two parsers read differently.

    A repeated property name, a number that isn't a plain integer within 2^53, bytes that aren't
    UTF-8, half a surrogate pair and anything around the one value are all invalid.
    """
    def pairs(items):
        if len({name for name, _ in items}) != len(items):
            raise ValueError("a property name twice")
        return dict(items)

    def integer(text):
        if abs(int(text)) > 2 ** 53:
            raise ValueError("number out of range")
        return int(text)

    def refuse(text):
        raise ValueError("not an integer: " + text)

    def strings(value):
        if isinstance(value, str):
            value.encode("utf-8")  # half a surrogate pair has no UTF-8 form
        elif isinstance(value, dict):
            for name, inner in value.items():
                strings(name), strings(inner)
        elif isinstance(value, list):
            for inner in value:
                strings(inner)

    value = json.loads(raw.decode("utf-8"), object_pairs_hook=pairs, parse_int=integer,
                       parse_float=refuse, parse_constant=refuse)
    strings(value)
    return value


def b64u(text):
    """Decode base64url as §5.2 requires: no padding, nothing outside the alphabet, unused bits zero.
    Lenient decoders read several texts as the same bytes; then two devices disagree on what is valid."""
    if re.fullmatch(r"[A-Za-z0-9_-]*", text) is None or len(text) % 4 == 1:
        raise ValueError("not base64url")
    raw = base64.urlsafe_b64decode(text + "=" * (-len(text) % 4))
    if base64.urlsafe_b64encode(raw).rstrip(b"=").decode() != text:
        raise ValueError("unused bits are not zero")
    return raw


def field_key(name):
    """A field's identity (§9.1): the UTF-8 bytes of its name, exactly. No Unicode normalization
    and no case folding, or devices whose string types compare differently keep different writes."""
    return name.encode("utf-8")


def inventory_bytes(inventory):
    out = bytearray(struct.pack(">I", len(inventory)))
    for rec, ids in sorted(inventory.items()):
        if len(bytes.fromhex(rec)) != 16 or len(set(ids)) != len(ids) or not ids:
            raise ValueError("noncanonical record, duplicate write or an entry with no writes")
        out += bytes.fromhex(rec) + struct.pack(">I", len(ids))
        for wid in sorted(ids):
            raw = bytes.fromhex(wid)
            if len(raw) != 32:
                raise ValueError("write ID must be 32 bytes")
            out += raw
    return bytes(out)


def inventory_hash(inventory):
    return hashlib.sha256(inventory_bytes(inventory)).hexdigest()


def statement(parent, signer, body, dot=0):
    s = {"parent": parent, "signer": signer, "body": copy.deepcopy(body), "dot": dot}
    s["id"] = digest(s)
    return s


def owner(body):
    """The one owner a roster state lists, or None."""
    found = [uid for uid, role in body["members"].items() if role == "owner"]
    return found[0] if len(found) == 1 else None


class OwnerLog:
    def __init__(self, genesis):
        self.genesis = genesis["id"]
        self.st, self.kids, self.held = {}, {}, {}
        self.authenticated = {}  # parent -> owner-signed successors, valid or not
        self.signed = {}         # their bytes: the evidence is kept and can be shown to others
        if not self.add(genesis):
            raise ValueError("invalid genesis")
        self.owner = genesis["signer"]  # fixed for the collection's life

    @property
    def frozen(self):
        return any(len(ids) > 1 for ids in self.authenticated.values())

    def conflict(self):
        """The statements that prove a conflict, for showing to devices that haven't seen them."""
        return [self.signed[i] for ids in self.authenticated.values() if len(ids) > 1 for i in sorted(ids)]

    def line(self, sid=None):
        sid = self.head() if sid is None else sid
        result = []
        while sid is not None:
            result.append(sid)
            sid = self.st[sid]["parent"]
        return result[::-1]

    def head(self):
        sid = self.genesis
        while len(self.authenticated.get(sid, ())) <= 1 and self.kids.get(sid):
            sid = self.kids[sid][0]
        return sid

    def epoch(self, sid):
        commit = self.st[sid]["body"]["commit"]
        while self.st[sid]["parent"] is not None:
            parent = self.st[sid]["parent"]
            if self.st[parent]["body"]["commit"] != commit:
                break
            sid = parent
        return sid

    def valid(self, parent, s):
        b = s["body"]
        members = b["members"]
        if owner(b) != s["signer"] or any(role not in ROLES for role in members.values()):
            return False  # one owner, and it is the key that signs: the owner never changes
        via = b.get("via", {})
        if parent is None:
            return (len(members) == 1 and b["n"] == b["epoch"] == 0
                    and b["snapshot"] is None and not b["deleted"] and not via)
        if parent["deleted"] or b["n"] != parent["n"] + 1:
            return False
        if set(via) - set(members) or any(via.get(uid) != parent.get("via", {}).get(uid)
                                          for uid in members if uid in parent["members"]):
            return False  # the invite a member came in through is fixed while they stay
        reducing = any(uid not in members or ROLES[members[uid]] < ROLES[role]
                       for uid, role in parent["members"].items())
        rotated = b["commit"] != parent["commit"]
        if rotated:
            if b["epoch"] != parent["epoch"] + 1 or b["snapshot"] is None:
                return False
            if set(members) - set(parent["members"]):
                return False
            if any(self.st[i]["body"]["commit"] == b["commit"] for i in self.line(s["parent"])):
                return False
        elif (reducing or b["epoch"] != parent["epoch"]
              or b["snapshot"] != parent["snapshot"]):
            return False
        return True

    def add(self, s):
        if s["id"] != digest({k: v for k, v in s.items() if k != "id"}):
            return False
        if s["id"] in self.st:
            return True
        parent_id = s["parent"]
        if parent_id is None:
            if s["id"] != self.genesis or not self.valid(None, s):
                return False
        else:
            if parent_id not in self.st:
                self.held.setdefault(parent_id, []).append(copy.deepcopy(s))
                return None
            if s["signer"] != self.owner:
                return False
            # In the real protocol this step verifies the complete signature by the owner's key.
            self.authenticated.setdefault(parent_id, set()).add(s["id"])
            self.signed[s["id"]] = copy.deepcopy(s)
            if not self.valid(self.st[parent_id]["body"], s):
                return False
        self.st[s["id"]] = copy.deepcopy(s)
        self.kids.setdefault(parent_id, []).append(s["id"])
        for child in self.held.pop(s["id"], []):
            self.add(child)
        return True


def data_write(uid, ref, rec, value, dev="device", hlc=1, ctx=None, base=None, field="v"):
    w = {"author": uid, "ref": ref, "rec": rec, "field": field, "value": value,
         "dev": dev, "hlc": hlc, "ctx": ctx or {}, "base": base or {}}
    w["id"] = digest(w)
    return w


def source_valid(log, w):
    if len(w["ctx"]) > VECTOR_LIMIT or len(w["base"]) > VECTOR_LIMIT:
        return False
    s = log.st.get(w["ref"])
    role = s["body"]["members"].get(w["author"]) if s else None
    return role == "owner" if w["rec"] == META else role in ("owner", "writer")


def write_valid(log, w, epoch_id, inventory=None):
    if log.frozen or epoch_id not in log.st or not source_valid(log, w):
        return False
    if log.epoch(w["ref"]) == epoch_id:
        return True
    if inventory is None or inventory_hash(inventory) != log.st[epoch_id]["body"]["snapshot"]:
        return False
    return w["ref"] in log.line(epoch_id) and w["id"] in inventory.get(w["rec"], ())


def covers(vector, other):
    return all(vector.get(dev, -1) >= time for dev, time in other.items())


def reach(w):
    r = dict(w["ctx"])
    r[w["dev"]] = max(r.get(w["dev"], -1), w["hlc"])
    return r


def supersedes(log, w, x):
    """§9.2, for two writes to the same field of one record."""
    if w["id"] == x["id"] or w["rec"] != x["rec"] or field_key(w["field"]) != field_key(x["field"]):
        return False
    we, xe = (log.st[i["ref"]]["body"]["epoch"] for i in (w, x))
    if we > xe:
        return covers(w["base"], reach(x)) and covers(w["base"], x["base"])
    if we == xe:
        includes = lambda a, b: covers(a["ctx"], reach(b)) and covers(a["base"], b["base"])
        return includes(w, x) and not includes(x, w)
    return False


def dominated(log, m, n):
    """§10.7: every write in message `m` is in message `n` or is superseded by a write in `n`."""
    ids = {w["id"] for w in n}
    return all(w["id"] in ids or any(supersedes(log, x, w) for x in n) for w in m)


def may_destroy(log, m, n, hm, hn):
    """Whether `m` may be destroyed because `n` is on the hub. `hm`, `hn`: their envelope hashes.
    Messages that dominate each other need not hold the same writes; the hash picks the one
    that stays, the same for every device. Equal hashes are byte-identical copies: not here."""
    return dominated(log, m, n) and (not dominated(log, n, m) or hm > hn)


def needed(log, kept, epoch):
    """The context and base an honest write in `epoch` needs to replace everything in `kept` (§9.2)."""
    ctx, base = {}, {}
    for k in kept:
        same = log.st[k["ref"]]["body"]["epoch"] == epoch
        for vector, source in ((ctx if same else base, reach(k)), (base, k["base"])):
            for dev, time in source.items():
                vector[dev] = max(vector.get(dev, -1), time)
    return ctx, base


def reissue(log, w, state=()):
    """Sign a write again in the head's epoch. None if the state there already holds or supersedes it."""
    if log.frozen or not source_valid(log, w):
        raise ValueError("untrusted authority or source")
    head = log.head()
    if log.st[head]["body"]["members"].get(w["author"]) not in ("owner", "writer"):
        raise ValueError("not a current writer")
    epoch = log.epoch(head)
    ref = next(i for i in log.line(head) if log.epoch(i) == epoch
               and log.st[i]["body"]["members"].get(w["author"]) in ("owner", "writer"))
    base = dict(w["base"])
    for dev, time in list(w["ctx"].items()) + [(w["dev"], w["hlc"])]:
        base[dev] = max(base.get(dev, -1), time)
    if len(base) > VECTOR_LIMIT:
        raise ValueError("too large to sign again: kept for the app")
    new = data_write(w["author"], ref, w["rec"], w["value"], w["dev"], w["hlc"], base=base, field=w["field"])
    if any(x["id"] in (w["id"], new["id"]) or supersedes(log, x, w) for x in state):
        return None
    return new


class Hub:
    def __init__(self):
        self.channels, self.inbox = {}, {}
        self.hidden = set()  # statements a withholding hub doesn't show

    def publish(self, epoch, key, history, writes, inventory):
        """Uploading again only adds what is missing: a hub holds messages, not a state to overwrite."""
        channel = self.channels.setdefault((epoch, key), {"history": [], "writes": [], "inventory": None})
        for name, items in (("history", history), ("writes", writes)):
            channel[name] += [copy.deepcopy(i) for i in items if i not in channel[name]]
        if channel["inventory"] is None:
            channel["inventory"] = copy.deepcopy(inventory)


def sync(log, hub, keys, announced=False):
    """Read the roster in every channel this device holds the key of.

    With `announced`, as the owner's devices read: a channel is read only once the statement
    that starts its epoch is known from the channel before it. They hold a new key before its
    change is announced, and a dropped change can leave its channel behind. Neither a key
    nor a missing note says a change was made; the announcement does.
    """
    read = set()
    while True:
        ready = [c for c in sorted(hub.channels) if c not in read and keys.get(c[0]) == c[1]
                 and (not announced or c[0] in log.st)]
        if not ready:
            return
        for c in ready:
            read.add(c)
            for s in hub.channels[c]["history"]:
                if s["id"] not in hub.hidden:
                    log.add(s)


def invite_open(log, invite):
    """§11.3. `invite` is the owner's record in home. Uses are counted on the roster, and an invite
    is over for good once anyone who came in through it has been removed, even if they are back
    by another invite. Both follow from the signed roster's history, so no second write has to
    land for a removal to close the door behind it."""
    users = set()
    for sid in log.line():
        came = {uid for uid, via in log.st[sid]["body"].get("via", {}).items() if via == invite["id"]}
        if users - came:
            return False
        users |= came
    return not invite["closed"] and len(users) < invite["maxUses"]


def report(log, hub, epoch, key):
    """A device that sees a conflict shows it where every member can see it: the current channel."""
    history = hub.channels[(epoch, key)]["history"]
    for s in log.conflict():
        if s not in history:
            history.append(copy.deepcopy(s))


def restore(log, old, new, inventory, epoch):
    """Before old data goes: anything the inventory lists that the new channel neither holds
    nor has replaced is copied across first, so the hub never loses its last copy. Only a
    write that is valid in the new epoch can have replaced anything: whoever holds the
    channel key can put signed junk there."""
    listed = {wid for ids in inventory.values() for wid in ids}
    valid = [x for x in new["writes"] if write_valid(log, x, epoch, inventory)]
    have = {x["id"] for x in valid}
    for w in old["writes"]:
        if w["id"] in listed and w["id"] not in have and not any(supersedes(log, x, w) for x in valid):
            new["writes"].append(copy.deepcopy(w))


def complete(channel, s, pending):
    return (channel is not None and any(x["id"] == s["id"] for x in channel["history"])
            and channel["inventory"] is not None
            and inventory_hash(channel["inventory"]) == s["body"]["snapshot"]
            and {w["id"] for w in pending["writes"]} <= {w["id"] for w in channel["writes"]})


class Device:
    """One device of the owner. Every one of them holds the owner's key and may change the roster.

    `home` is the owner's home channel, shared by all their devices and reachable with
    the backup key: every epoch key, and a note of the one key change that may be in
    flight. `store` is this device's own journal. Each step stands for an acknowledged
    upload; tests stop and restart between them, or continue on another device.
    """
    def __init__(self, uid, log, hub, home, store=None):
        self.uid, self.log, self.hub, self.home = uid, log, hub, home
        self.store = {"pending": None} if store is None else store
        if not {"keys", "flight"} <= home.keys():
            raise ValueError("no home: restore the identity first")

    def sync(self):
        sync(self.log, self.hub, self.home["keys"], announced=True)

    def ready(self):
        """Before anything is signed: the latest roster, no conflict, and nothing in flight anywhere."""
        self.sync()
        if self.store["pending"] is not None:
            raise ValueError("resume the journalled key change first")
        if self.log.frozen:
            raise ValueError("the collection is dead")
        if self.log.owner != self.uid:
            raise ValueError("not the owner")
        if self.home["flight"] is not None:
            raise ValueError("a key change is under way on another device: finish it here first")
        head = self.log.head()
        return head, self.log.st[head]["body"]

    def amend(self, edit):
        """A change inside the epoch: an admission or a promotion."""
        head, before = self.ready()
        body = copy.deepcopy(before)
        edit(body)
        body["n"] = before["n"] + 1
        s = statement(head, self.uid, body, body["n"])
        if not self.log.valid(before, s):
            raise ValueError("invalid owner decision")
        epoch = self.log.epoch(head)
        self.hub.channels[(epoch, self.home["keys"][epoch])]["history"].append(copy.deepcopy(s))
        self.log.add(s)
        return s

    def admit(self, uid, invite, role="reader", asked=False):
        """Answer a join request (§11.3). `asked`: the owner approved this very request by hand."""
        head, before = self.ready()
        if uid in before["members"]:
            self.grant(uid)  # the grant again: the device may have crashed between admitting and granting
            return "granted"
        was_member = any(uid in self.log.st[i]["body"]["members"] for i in self.log.line())
        if not invite_open(self.log, invite) or (was_member and not asked):
            return "refused"

        def join(body):
            body["members"][uid] = role
            body.setdefault("via", {})[uid] = invite["id"]
        self.amend(join)
        self.grant(uid)
        return "admitted"

    def prepare(self, edit, writes=(), fresh_key="new-key", resolved=()):
        """A change that starts an epoch. Nothing leaves the device before home and the journal have it.

        `writes` is what the device carries over. `resolved` are IDs of earlier snapshot
        writes it has itself seen replaced. Every other entry of the current inventory is
        carried forward unseen: the write may only be missing from the hub, and whoever
        holds it must be able to put it back.
        """
        head, before = self.ready()
        inventory = {}
        for w in writes:
            if not source_valid(self.log, w) or w["ref"] not in self.log.line(head):
                raise ValueError("snapshot source outside this history")
            inventory.setdefault(w["rec"], []).append(w["id"])
        epoch = self.log.epoch(head)
        if self.log.st[epoch]["body"]["snapshot"] is not None:
            current = self.hub.channels[(epoch, self.home["keys"][epoch])]["inventory"]
            if current is None or inventory_hash(current) != self.log.st[epoch]["body"]["snapshot"]:
                raise ValueError("this epoch's inventory is missing: restore it first")
            accounted = {w["id"] for w in writes} | set(resolved)
            for rec, ids in current.items():
                for wid in ids:
                    if wid not in accounted:
                        inventory.setdefault(rec, []).append(wid)
        body = copy.deepcopy(before)
        edit(body)
        body.update(n=before["n"] + 1, epoch=before["epoch"] + 1,
                    commit=fresh_key, snapshot=inventory_hash(inventory))
        s = statement(head, self.uid, body, body["n"])
        if not self.log.valid(before, s):
            raise ValueError("invalid owner decision")
        self.home["keys"][s["id"]] = fresh_key
        self.home["flight"] = {"parent": head, "id": s["id"], "key": fresh_key,
                               "members": copy.deepcopy(body["members"])}   # what the change is, for the owner's other devices
        self.store["pending"] = {"statement": s, "key": fresh_key,
                                 "inventory": inventory, "writes": copy.deepcopy(list(writes)),
                                 "phase": "prepared", "sent": []}
        return s

    def drop(self, sid, key):
        """An unannounced key change that lost its place: it goes, with its key and its channel."""
        self.home["keys"].pop(sid, None)
        if self.home["flight"] and self.home["flight"]["id"] == sid:
            self.home["flight"] = None
        self.hub.channels.pop((sid, key), None)

    def step(self):
        pending = self.store["pending"]
        if pending is None:
            return "idle"
        self.sync()
        s, hub = pending["statement"], self.hub
        parent = s["parent"]
        if self.log.frozen:
            raise ValueError("the collection is dead: keep the journal and the data")
        mine = self.home["flight"] is not None and self.home["flight"]["id"] == s["id"]
        if s["id"] not in self.log.st and (not mine or self.log.head() != parent):
            # Another device of the owner dropped this before it was announced.
            self.drop(s["id"], pending["key"])
            self.store["pending"] = None
            return "dropped"
        old_epoch = self.log.epoch(parent)
        old_key = self.home["keys"][old_epoch]
        new = (s["id"], pending["key"])
        publish = lambda: hub.publish(s["id"], pending["key"],
                                      [self.log.st[i] for i in self.log.line(parent)] + [s],
                                      pending["writes"], pending["inventory"])
        if pending["phase"] == "prepared":
            publish()
            pending["phase"] = "uploaded"
        elif pending["phase"] == "uploaded":
            if not complete(hub.channels.get(new), s, pending):
                publish()  # something was destroyed before the announcement: put it back first
                return "uploaded"
            old = hub.channels[(old_epoch, old_key)]
            if s not in old["history"]:
                old["history"].append(copy.deepcopy(s))  # the announcement
            self.log.add(s)
            if mine:
                self.home["flight"] = None
            pending["phase"] = "committed"
        else:
            if s["id"] not in self.log.line():
                raise ValueError("unannounced or conflicting key change")
            remaining = sorted(set(s["body"]["members"]) - set(pending["sent"]))
            if remaining:
                self.grant(remaining[0], s["id"])
                pending["sent"].append(remaining[0])
            else:
                channel = hub.channels.get(new)
                if (channel is None or channel["inventory"] is None
                        or inventory_hash(channel["inventory"]) != s["body"]["snapshot"]):
                    publish()  # the inventory must be there; data is healed like any other
                    return "committed"
                restore(self.log, hub.channels[(old_epoch, old_key)], channel, channel["inventory"], s["id"])
                hub.channels[(old_epoch, old_key)]["writes"] = []
                self.store["pending"] = None
                return "done"
        return pending["phase"]

    def grant(self, uid, at=None):
        """The current key, to a member the roster lists. Sent after an announcement, and again on request."""
        at = self.log.head() if at is None else at
        epoch = self.log.epoch(at)
        self.hub.inbox.setdefault(uid, []).append({"at": at, "epoch": epoch,
                                                   "key": self.home["keys"][epoch], "signer": self.uid})

    def finish_here(self):
        """The user says: deal on this device with what another one started. Finished if its
        statement and inventory are on the hub; otherwise it is dropped, and `self.abandoned`
        says what it was, so that the user can be told and can have it done again."""
        self.abandoned = None
        self.sync()
        if self.log.frozen:
            raise ValueError("the collection is dead")
        if self.store["pending"] is not None:
            raise ValueError("resume the journalled key change first")
        if self.log.owner != self.uid:
            raise ValueError("not the owner")
        flight = self.home["flight"]
        if flight is None:
            return "nothing"
        if flight["id"] in self.log.st:
            self.home["flight"] = None                        # announced already; only grants may be missing
            return "announced"
        channel = self.hub.channels.get((flight["id"], flight["key"]))
        s = next((x for x in channel["history"] if x["id"] == flight["id"]), None) if channel else None
        inventory = channel["inventory"] if channel else None
        if (s is not None and s["parent"] == self.log.head() and inventory is not None
                and inventory_hash(inventory) == s["body"]["snapshot"]):
            # The snapshot is judged against the inventory, not against whatever happens to be
            # in the new channel: a listed write missing there is fetched from the old channel.
            old_epoch = self.log.epoch(s["parent"])
            old = self.hub.channels[(old_epoch, self.home["keys"][old_epoch])]
            listed = {wid for ids in inventory.values() for wid in ids}
            found = {w["id"]: w for w in old["writes"] + channel["writes"] if w["id"] in listed}
            self.store["pending"] = {"statement": s, "key": flight["key"], "inventory": inventory,
                                     "writes": list(found.values()), "phase": "uploaded", "sent": []}
            while self.step() not in ("done", "idle"):
                pass
            return "finished"
        self.abandoned = copy.deepcopy(flight)
        self.drop(flight["id"], flight["key"])
        return "dropped"


def receive_grant(log, hub, uid, grant):
    """Verify accessible history; the hub is never asked what is current.

    `log` starts from the genesis the device expects: for a newcomer, the one whose
    owner is the inviter and whose ID gives the collection ID in the invite.
    """
    channel = hub.channels.get((grant["epoch"], grant["key"]))
    if channel is None:
        return None
    for s in channel["history"]:
        log.add(s)
    if log.frozen or grant["at"] not in log.line():
        return False
    at = log.st[grant["at"]]
    if (log.epoch(grant["at"]) != grant["epoch"] or log.epoch(log.head()) != grant["epoch"]
            or uid not in at["body"]["members"] or uid not in log.st[log.head()]["body"]["members"]):
        return False
    if grant["signer"] != log.owner or at["body"]["commit"] != grant["key"]:
        return False
    inventory = channel["inventory"]
    snapshot = at["body"]["snapshot"]
    if snapshot is None:
        inventory = {}  # epoch 0 has no imported earlier writes
    elif inventory is None or inventory_hash(inventory) != snapshot:
        return None     # held: without the inventory nothing old can be judged
    # A listed write that isn't here delays nothing: later edits supersede snapshot
    # writes and their messages are collected, and a destroyed one is healed by its holders.
    return [w for w in channel["writes"] if write_valid(log, w, grant["epoch"], inventory)]
