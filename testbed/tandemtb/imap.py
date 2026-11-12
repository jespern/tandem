"""IMAP helpers on imaplib (Python 3.14+ for IDLE).

Thin on purpose: tests should see the server's real responses, so these
helpers parse only what they must.
"""

import imaplib
import re

from .message import FOLDER


def connect(host, port, user, password, tls=False, timeout=30):
    cls = imaplib.IMAP4_SSL if tls else imaplib.IMAP4
    m = cls(host, port, timeout=timeout)
    m.login(user, password)
    return m


def ok(result):
    typ, data = result
    if typ != "OK":
        raise imaplib.IMAP4.error(f"{typ} {data!r}")
    return data


def ensure_folder(m, name=FOLDER):
    typ, data = m.create(name)
    if typ != "OK" and not any(b"ALREADYEXISTS" in (d or b"") for d in data):
        raise imaplib.IMAP4.error(f"CREATE {name}: {typ} {data!r}")


_APPENDUID = re.compile(rb"APPENDUID (\d+) (\d+)")


def append(m, raw, folder=FOLDER, flags="(\\Seen)"):
    """APPEND; returns (uidvalidity, uid) from the UIDPLUS response code."""
    data = ok(m.append(folder, flags, None, raw))
    match = _APPENDUID.search(b" ".join(d for d in data if isinstance(d, bytes)))
    if not match:
        raise imaplib.IMAP4.error(f"no APPENDUID in {data!r}")
    return int(match.group(1)), int(match.group(2))


def uids(m):
    """All UIDs in the selected folder."""
    data = ok(m.uid("SEARCH", "ALL"))
    return [int(x) for x in data[0].split()] if data and data[0] else []


def fetch_raw(m, uid):
    data = ok(m.uid("FETCH", str(uid), "(BODY.PEEK[])"))
    for part in data:
        if isinstance(part, tuple):
            return part[1]
    raise imaplib.IMAP4.error(f"no body for UID {uid}")


_UID_IN_FETCH = re.compile(rb"UID (\d+)")


def fetch_uids_from(m, start):
    """UIDs returned by `UID FETCH start:* (UID)`, unfiltered (to observe the n:* quirk)."""
    data = ok(m.uid("FETCH", f"{start}:*", "(UID)"))
    found = []
    for part in data:
        line = part[0] if isinstance(part, tuple) else part
        if isinstance(line, bytes):
            match = _UID_IN_FETCH.search(line)
            if match:
                found.append(int(match.group(1)))
    return found


def status(m, folder=FOLDER, items="(UIDVALIDITY UIDNEXT HIGHESTMODSEQ MESSAGES)"):
    data = ok(m.status(folder, items))
    text = data[0].decode()
    return {k: int(v) for k, v in re.findall(r"(\w+) (\d+)", text.split("(", 1)[1])}


def destroy(m, uid_list):
    """`UID STORE +FLAGS.SILENT (\\Deleted)` then `UID EXPUNGE` (UIDPLUS), as in §10.3."""
    if not uid_list:
        return
    seq = ",".join(str(u) for u in uid_list)
    ok(m.uid("STORE", seq, "+FLAGS.SILENT", "(\\Deleted)"))
    ok(m.uid("EXPUNGE", seq))
