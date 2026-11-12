"""What a hub participant or the hub operator can do (the `hub:` steps of the
scenario vocabulary): look at, destroy, capture, replay, roll back, move,
inject and re-encode raw messages in the Tandem folder.

All operations go through IMAP, which hands back raw message bytes; the
results are visible to JMAP clients of the same account as well.
"""

import json
import pathlib

from . import imap
from .message import FOLDER, build_message, decode_message, envelope_id, reencode_message


def _open(desc):
    c = desc["imap"]
    m = imap.connect(c["host"], c["port"], c["user"], c["password"], tls=c.get("tls", False))
    imap.ensure_folder(m)
    imap.ok(m.select(FOLDER))
    return m


def listing(desc):
    """Every message in the folder: UID, channel tag, size, H(envelope)."""
    m = _open(desc)
    try:
        out = []
        for uid in imap.uids(m):
            raw = imap.fetch_raw(m, uid)
            entry = {"uid": uid, "size": len(raw)}
            try:
                channel, envelope = decode_message(raw)
                entry.update(channel=channel, envelope=envelope_id(envelope))
            except Exception as e:                       # junk is part of the picture
                entry.update(channel=None, error=str(e))
            out.append(entry)
        return out
    finally:
        m.logout()


def select(entries, channel=None, uids=None, latest=None, everything=False):
    chosen = entries
    if channel:
        chosen = [e for e in chosen if e.get("channel") == channel]
    if uids:
        wanted = set(uids)
        chosen = [e for e in chosen if e["uid"] in wanted]
    if latest:
        chosen = sorted(chosen, key=lambda e: e["uid"])[-latest:]
    if not (channel or uids or latest or everything):
        raise ValueError("refusing to select everything without --all")
    return chosen


def destroy(desc, **selector):
    chosen = select(listing(desc), **selector)
    m = _open(desc)
    try:
        imap.destroy(m, [e["uid"] for e in chosen])
    finally:
        m.logout()
    return chosen


def snapshot(desc, directory, channel=None):
    """Capture raw messages (all, or one channel) to a directory, for replay or rollback."""
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    m = _open(desc)
    saved = []
    try:
        for uid in imap.uids(m):
            raw = imap.fetch_raw(m, uid)
            try:
                tag, envelope = decode_message(raw)
            except Exception:
                tag, envelope = None, raw
            if channel and tag != channel:
                continue
            name = envelope_id(envelope)[:16] + ".eml"
            (directory / name).write_bytes(raw)
            saved.append({"uid": uid, "file": name, "channel": tag})
    finally:
        m.logout()
    (directory / "index.json").write_text(json.dumps(saved, indent=2))
    return saved


def replay(desc, files):
    """Append captured messages again, byte for byte."""
    m = _open(desc)
    try:
        return [imap.append(m, pathlib.Path(f).read_bytes())[1] for f in files]
    finally:
        m.logout()


def rollback(desc, directory, channel=None):
    """Make a channel (or the folder) look as it did at snapshot time: destroy what
    is there now, then put the snapshot back. The `hub: rollback` step."""
    directory = pathlib.Path(directory)
    files = sorted(p for p in directory.glob("*.eml"))
    if channel:
        files = [f for f in files if decode_message(f.read_bytes())[0] == channel]
        destroy(desc, channel=channel)
    else:
        destroy(desc, everything=True)
    return replay(desc, files)


def move(desc, uid, to):
    """Move a message out of (or within) the account: the `hub: move` step."""
    m = _open(desc)
    try:
        imap.ensure_folder(m, to)
        imap.ok(m.uid("MOVE", str(uid), to))
    finally:
        m.logout()


def move_back(desc, source, uid):
    """Move a message from another folder into Tandem."""
    c = desc["imap"]
    m = imap.connect(c["host"], c["port"], c["user"], c["password"], tls=c.get("tls", False))
    try:
        imap.ok(m.select(source))
        imap.ok(m.uid("MOVE", str(uid), FOLDER))
    finally:
        m.logout()


def inject(desc, raw=None, channel=None, envelope=None):
    """Append arbitrary bytes, or a well-formed message carrying any envelope bytes."""
    if raw is None:
        raw = build_message(channel, envelope)
    m = _open(desc)
    try:
        return imap.append(m, raw)[1]
    finally:
        m.logout()


def rewrite_charset(desc, uid, charset, transfer_encoding):
    """Store a message's body in another charset and transfer encoding, then
    remove the original: what a JMAP server may legitimately do (§10.2)."""
    m = _open(desc)
    try:
        raw = imap.fetch_raw(m, uid)
        new_uid = imap.append(m, reencode_message(raw, charset, transfer_encoding))[1]
        imap.destroy(m, [uid])
        return new_uid
    finally:
        m.logout()
