"""Pure v2 encodings and merge rules. Authorization is the caller's responsibility."""
from dataclasses import dataclass, replace
from enum import IntEnum
import json
import struct

from .crypto import b64u, unb64u, sha
from .signature import verify_strict

MAX_VECTOR = 1024
MAX_MILLIS = 1 << 48


def parse_json(data: bytes):
    """Reject duplicates, non-integers, invalid Unicode and integers beyond 2**53."""
    def pairs(items):
        out = {}
        for k, v in items:
            if k in out:
                raise ValueError("duplicate JSON name")
            out[k] = v
        return out
    def integer(s):
        n = int(s)
        if abs(n) > 2**53:
            raise ValueError("integer beyond 2**53")
        return n
    def forbidden(_):
        raise ValueError("not a plain integer")
    text = data.decode("utf-8")
    value = json.loads(text, object_pairs_hook=pairs, parse_int=integer, parse_float=forbidden, parse_constant=forbidden)
    def valid(v, depth=0):
        if depth > 64:
            raise ValueError("JSON nesting limit")
        if isinstance(v, str):
            v.encode("utf-8")
        elif isinstance(v, dict):
            for k, x in v.items():
                valid(k, depth + 1); valid(x, depth + 1)
        elif isinstance(v, list):
            for x in v:
                valid(x, depth + 1)
    valid(value)
    return value


def _bytes(value, size=None):
    if not isinstance(value, bytes) or (size is not None and len(value) != size):
        raise ValueError("invalid byte length or type")
    return value


def _integer(value, stop):
    if type(value) is not int or not 0 <= value < stop:
        raise ValueError("integer out of range")


def _lp(value):
    return struct.pack(">I", len(value)) + value


class Op(IntEnum):
    ASSIGN = 1
    ADD = 2
    REMOVE = 3
    DELETE = 4


@dataclass(frozen=True, slots=True)
class Dot:
    device: bytes
    millis: int
    counter: int = 0

    def check(self):
        _bytes(self.device, 8); _integer(self.millis, MAX_MILLIS); _integer(self.counter, 2**16)

    @property
    def clock(self):
        return self.millis, self.counter


def vector_bytes(vector: tuple[Dot, ...]) -> bytes:
    if type(vector) is not tuple or len(vector) > MAX_VECTOR:
        raise ValueError("invalid vector")
    previous = None
    out = struct.pack(">H", len(vector))
    for d in vector:
        d.check()
        if previous is not None and d.device <= previous:
            raise ValueError("vector not sorted and unique")
        out += d.device + struct.pack(">QH", d.millis, d.counter)
        previous = d.device
    return out


@dataclass(frozen=True, slots=True)
class Write:
    hub: bytes
    scope: bytes
    record: bytes
    field: str
    op: Op
    author: bytes
    certificate: bytes
    authorization: bytes
    dot: Dot
    value: bytes = b""
    element: bytes | None = None
    ref: bytes = bytes(32)
    context: tuple[Dot, ...] = ()
    base: tuple[Dot, ...] = ()
    signature: bytes = b""

    def sig_input(self) -> bytes:
        for value in (self.hub, self.author, self.certificate, self.authorization, self.ref):
            _bytes(value, 32)
        _bytes(self.scope, 16); _bytes(self.record, 16); _bytes(self.value)
        if type(self.field) is not str or type(self.op) is not Op:
            raise ValueError("invalid field or op")
        if (self.op in (Op.ADD, Op.REMOVE)) != (self.element is not None):
            raise ValueError("element only on add/remove")
        if self.element is not None:
            _bytes(self.element)
        if (self.op != Op.ASSIGN and self.value) or (self.op == Op.DELETE and self.field):
            raise ValueError("invalid operation payload")
        self.dot.check()
        return (b"TANDEM-WRITE-V2" + self.hub + self.scope + self.record + _lp(self.field.encode("utf-8"))
                + bytes((self.op,)) + _lp(self.element or b"") + sha(self.value) + self.author
                + self.certificate + self.authorization + self.dot.device
                + struct.pack(">QH", self.dot.millis, self.dot.counter) + self.ref
                + vector_bytes(self.context) + vector_bytes(self.base))

    @property
    def id(self):
        return sha(self.sig_input())

    def sign(self, private):
        return replace(self, signature=private.sign(self.sig_input()))

    def verify(self, public_key: bytes) -> bool:
        return verify_strict(public_key, self.signature, self.sig_input())


def _dot_json(d):
    return [b64u(d.device), d.millis, d.counter]


def record_payload(record: bytes, writes: list[Write]) -> bytes:
    _bytes(record, 16)
    rows = []
    for w in writes:
        w.sig_input()
        if w.record != record or len(w.signature) != 64:
            raise ValueError("record or signature mismatch")
        row = dict(f=w.field, op=int(w.op), a=b64u(w.author), cert=b64u(w.certificate), auth=b64u(w.authorization),
                   d=_dot_json(w.dot), c=list(map(_dot_json, w.context)), b=list(map(_dot_json, w.base)), s=b64u(w.signature))
        if w.ref != bytes(32):
            row["r"] = b64u(w.ref)
        if w.op == Op.ASSIGN:
            row["v"] = b64u(w.value)
        if w.element is not None:
            row["e"] = b64u(w.element)
        rows.append(row)
    return json.dumps(dict(rec=b64u(record), writes=rows), ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def parse_kind1(data: bytes, hub: bytes, scope: bytes):
    _bytes(hub, 32); _bytes(scope, 16)
    j = parse_json(data)
    if not isinstance(j, dict) or ("rec" in j) == ("inventory" in j):
        raise ValueError("exactly one of rec and inventory")
    def raw(text, n=None):
        return _bytes(unb64u(text), n)
    if "inventory" in j:
        f = j["inventory"]
        part, parts = f["part"], f["parts"]
        if type(part) is not int or type(parts) is not int or not 0 <= part < parts:
            raise ValueError("invalid inventory fragment index")
        return dict(epoch=raw(f["epoch"], 32), part=part, parts=parts, bytes=raw(f["bytes"]))
    rec = raw(j["rec"], 16)
    if not isinstance(j["writes"], list):
        raise ValueError("writes must be an array")
    def dot(d):
        if not isinstance(d, list) or len(d) != 3:
            raise ValueError("invalid dot")
        return Dot(raw(d[0], 8), d[1], d[2])
    out = []
    for row in j["writes"]:
        if type(row["op"]) is not int:
            raise ValueError("op must be an integer")
        op = Op(row["op"])
        if ("v" in row) != (op == Op.ASSIGN) or ("e" in row) != (op in (Op.ADD, Op.REMOVE)):
            raise ValueError("unexpected value/element")
        w = Write(hub, scope, rec, row["f"], op, raw(row["a"], 32), raw(row["cert"], 32), raw(row["auth"], 32), dot(row["d"]),
                  raw(row["v"]) if "v" in row else b"", raw(row["e"]) if "e" in row else None,
                  raw(row["r"], 32) if "r" in row else bytes(32), tuple(map(dot, row["c"])), tuple(map(dot, row["b"])), raw(row["s"], 64))
        w.sig_input(); out.append(w)
    return rec, tuple(out)


def inventory_bytes(entries: dict[bytes, tuple[bytes, ...]]) -> bytes:
    out = struct.pack(">I", len(entries))
    for rec, writes in sorted(entries.items()):
        _bytes(rec, 16)
        if not writes or len(set(writes)) != len(writes):
            raise ValueError("empty or duplicate inventory entry")
        out += rec + struct.pack(">I", len(writes))
        out += b"".join(_bytes(w, 32) for w in sorted(writes))
    return out


def parse_inventory(data: bytes) -> dict[bytes, tuple[bytes, ...]]:
    i = 0
    def take(n):
        nonlocal i
        if n < 0 or len(data) - i < n:
            raise ValueError("short inventory")
        out = data[i:i+n]; i += n
        return out
    def count():
        return int.from_bytes(take(4), "big")
    out = {}
    previous = b""
    for _ in range(count()):
        rec = take(16)
        if rec <= previous:
            raise ValueError("records not sorted and unique")
        n = count()
        if n == 0 or n > (len(data) - i) // 32:
            raise ValueError("invalid inventory write count")
        ws = tuple(take(32) for _ in range(n))
        if any(a >= b for a, b in zip(ws, ws[1:])):
            raise ValueError("writes not sorted and unique")
        out[rec] = ws; previous = rec
    if i != len(data):
        raise ValueError("trailing inventory bytes")
    return out


def join(*vectors: tuple[Dot, ...]) -> tuple[Dot, ...]:
    out = {}
    for vector in vectors:
        for d in vector:
            if d.device not in out or out[d.device].clock < d.clock:
                out[d.device] = d
    return tuple(out[d] for d in sorted(out))


def _covers(a, b):
    aa = {d.device: d.clock for d in a}
    return all(d.device in aa and aa[d.device] >= d.clock for d in b)


def _reach(w):
    return join(w.context, (w.dot,))


def _includes(w, x):
    return _covers(w.context, _reach(x)) and _covers(w.base, x.base)


def _slot(w):
    return (("delete",) if w.op == Op.DELETE else
            ("set", w.field, w.element) if w.op in (Op.ADD, Op.REMOVE) else ("assign", w.field))


@dataclass(frozen=True, slots=True)
class Entry:
    write: Write
    epoch: bytes
    epoch_number: int

    def __post_init__(self):
        _bytes(self.epoch, 32); _integer(self.epoch_number, 2**64); self.write.sig_input()

    @property
    def id(self):
        return self.write.id


def supersedes(w: Entry, x: Entry) -> bool:
    a, b = w.write, x.write
    if w.id == x.id or (a.hub, a.scope, a.record) != (b.hub, b.scope, b.record):
        return False
    if (b.op == Op.DELETE and a.op != Op.DELETE) or (a.op != Op.DELETE and _slot(a) != _slot(b)):
        return False
    if w.epoch_number > x.epoch_number:
        return _covers(a.base, _reach(b)) and _covers(a.base, b.base)
    return w.epoch_number == x.epoch_number and w.epoch == x.epoch and _includes(a, b) and not _includes(b, a)


class Record:
    def __init__(self, hub: bytes, scope: bytes, record: bytes):
        self.hub, self.scope, self.id = _bytes(hub, 32), _bytes(scope, 16), _bytes(record, 16)
        self.kept: tuple[Entry, ...] = ()

    def merge(self, *entries: Entry):
        if any((e.write.hub, e.write.scope, e.write.record) != (self.hub, self.scope, self.id) for e in entries):
            raise ValueError("entry belongs to another record, scope or hub")
        all_entries = {e.id: e for e in (*self.kept, *entries)}
        self.kept = tuple(e for _, e in sorted(all_entries.items()) if not any(supersedes(w, e) for w in all_entries.values()))

    @property
    def deleted(self):
        return any(e.write.op == Op.DELETE for e in self.kept)

    def siblings(self, field: str):
        return tuple(sorted((e for e in self.kept if e.write.op == Op.ASSIGN and e.write.field == field),
                            key=lambda e: (e.epoch_number, e.write.dot.clock, e.write.author, e.write.dot.device, e.id), reverse=True))

    def value(self, field: str) -> bytes | None:
        siblings = self.siblings(field)
        return siblings[0].write.value if siblings else None

    def elements(self, field: str):
        return tuple(sorted({e.write.element for e in self.kept if e.write.op == Op.ADD and e.write.field == field}))

    def vectors(self, op: Op, field: str, element: bytes | None, epoch: bytes, epoch_number: int):
        c, b = [], []
        slot = ("set", field, element) if op in (Op.ADD, Op.REMOVE) else ("assign", field)
        for e in self.kept:
            if op != Op.DELETE and _slot(e.write) != slot:
                continue
            b.append(e.write.base)
            (c if (e.epoch, e.epoch_number) == (epoch, epoch_number) else b).append(_reach(e.write))
        context, base = join(*c), join(*b)
        vector_bytes(context); vector_bytes(base)
        return context, base


def next_clock(millis: int, counter: int, now: int):
    """The caller durably reserves the returned clock with its write before export."""
    _integer(millis, MAX_MILLIS); _integer(counter, 2**16); _integer(now, MAX_MILLIS)
    m, c = (now, 0) if now > millis else (millis + 1, 0) if counter == 65535 else (millis, counter + 1)
    _integer(m, MAX_MILLIS)
    return m, c
