#!/usr/bin/env python3
"""Measures how an IMAP account carries blob chunks (BLOBS.md §4, questions B1 and B2).

    probes/blob_probe.py --stalwart
    probes/blob_probe.py --fastmail
    probes/blob_probe.py imap.fastmail.com alice@fastmail.com --keychain tandem-probe
    probes/blob_probe.py imap.gmail.com alice@gmail.com --keychain tandem-probe
    probes/blob_probe.py --fastmail --parallel

For chunks of 256 KiB, 1 MiB and 4 MiB it appends and fetches them back in the
text form (base64, DESIGN.md §10.4) and, where the server advertises BINARY
(RFC 3516), in the binary form: one append at a time, pipelined where LITERAL+
allows, and in one MULTIAPPEND where offered; each batch is fetched back one
chunk at a time and again with one command. Every chunk is compared byte for
byte with what was sent. It also reads the quota before and after, scans the
headers a chunk index needs, and times a small append reaching an idling
connection while chunks are being uploaded on another.

--parallel runs a different measurement instead: 1 MiB chunks spread over 1, 2,
4 and 8 connections at once (or the numbers given), appended one at a time and
in batches where the server can batch, then fetched one at a time and with one command a connection. It
uploads about 130 MB, or about 90 MB where the server cannot batch (Gmail).

--forms compares the ways of carrying a chunk on one connection: as text, as a
base64 attachment, as binary and as 8bit content with a few bytes escaped, each
with and without COMPRESS=DEFLATE (RFC 4978). It uploads about 55 MB on Gmail
and about 75 MB where BINARY is offered; name the forms to run fewer.

--stalwart starts the disposable Stalwart the tests use (needs Docker) and
removes it afterwards. --fastmail reads the app password from
~/.config/tandem/fastmail-imap.json and asks Fastmail's JMAP session, with the
token in ~/.config/tandem/fastmail.json, for the username. Otherwise the password comes from the macOS keychain or
is asked for, as in imap_probe.py; it is never printed or written down, and the
report holds no username. Everything happens in two new folders named
Tandem-probe-<run> and Tandem-probe-<run>-blobs, which are emptied and deleted
afterwards, with the copies Gmail keeps elsewhere. A full run uploads about
170 MB where every mode is offered and about 30 MB on Gmail.
"""
import argparse
import base64
import getpass
import hashlib
import imaplib
import json
import os
import re
import socket
import ssl
import subprocess
import sys
import threading
import time
import urllib.request
import zlib
from email.utils import formatdate

import imap_probe
from imap_probe import Watcher, cleanup, log, quote, special_folders

KIB = 1024
SIZES = [(256 * KIB, 12), (1024 * KIB, 6), (4096 * KIB, 3)]   # plaintext bytes, chunks per cell
# Bytes a mail store might mangle: NUL, bare CR and LF, a lone dot, an mbox "From ", a line over 998 bytes.
TRICKY = b'\x00' * 16 + b'\rx\ny\r\n.\r\n\nFrom \r\n' + b'A' * 2000 + b'\r\n\x00\xff\xfe\r\r\n\n'


class Refused(Exception):
    pass


class Lit(bytes):
    """A literal in a server response."""


class Deflated:
    """A connection after COMPRESS DEFLATE (RFC 4978): raw deflate both ways, flushed after every write."""

    def __init__(self, sock):
        self.sock, self.buf = sock, bytearray()
        self.c, self.d = zlib.compressobj(6, zlib.DEFLATED, -15), zlib.decompressobj(-15)
        self.sent = self.received = 0       # bytes on the wire, inside TLS

    def sendall(self, data):
        out = self.c.compress(data) + self.c.flush(zlib.Z_SYNC_FLUSH)
        self.sent += len(out)
        self.sock.sendall(out)

    def fill(self):
        raw = self.sock.recv(1 << 16)
        if not raw:
            raise EOFError('connection closed')
        self.received += len(raw)
        self.buf += self.d.decompress(raw)

    def readline(self):
        while True:
            i = self.buf.find(b'\n')
            if i >= 0:
                line = bytes(self.buf[:i + 1])
                del self.buf[:i + 1]
                return line
            self.fill()

    def read(self, n):
        while len(self.buf) < n:
            self.fill()
        out = bytes(self.buf[:n])
        del self.buf[:n]
        return out

    def close(self):
        self.sock.close()


class Raw:
    """Just enough IMAP to send literals of every kind and read literals back."""

    def __init__(self, host, port, user, password, verify=True, compress=False):
        ctx = ssl.create_default_context()
        if not verify:
            ctx.check_hostname, ctx.verify_mode = False, ssl.CERT_NONE
        self.s = ctx.wrap_socket(socket.create_connection((host, port), timeout=300), server_hostname=host)
        self.f = self.s.makefile('rb')
        self.n = 0
        self.response()
        self.cmd([b'LOGIN ' + astring(user) + b' ' + astring(password)])
        _, untagged = self.cmd([b'CAPABILITY'])
        self.caps = set()
        for r in untagged:
            if r[0].upper().startswith(b'* CAPABILITY'):
                self.caps = set(r[0].decode().upper().split()[2:])
        self.wire = None
        if compress:
            self.cmd([b'COMPRESS DEFLATE'])
            self.s = self.f = self.wire = Deflated(self.s)

    def response(self):
        """One response line with its literals, as a list of pieces."""
        parts = []
        while True:
            line = self.f.readline()
            if not line:
                raise EOFError('connection closed')
            found = re.search(rb'~?\{(\d+)\}\r\n$', line)
            if not found:
                parts.append(line.rstrip(b'\r\n'))
                return parts
            parts.append(line[:found.start()])
            parts.append(Lit(self.f.read(int(found.group(1)))))

    def write(self, tag, parts, nonsync):
        """parts: bytes, and (data, binary) for a literal. Returns responses that arrived meanwhile."""
        early, out = [], tag + b' '
        for p in parts:
            if isinstance(p, bytes):
                out += p
                continue
            data, binary = p
            out += (b'~' if binary else b'') + b'{%d%s}\r\n' % (len(data), b'+' if nonsync else b'')
            if nonsync:
                out += data
                continue
            self.s.sendall(out)
            while True:
                r = self.response()
                if r[0].startswith(b'+'):
                    break
                if r[0].startswith(tag + b' '):
                    raise Refused(r[0].decode(errors='replace')[:300])
                early.append(r)
            out = data
        self.s.sendall(out + b'\r\n')
        return early

    def result(self, tag, early=()):
        untagged = list(early)
        while True:
            r = self.response()
            if r[0].startswith(tag + b' '):
                status = r[0][len(tag) + 1:]
                if not status.upper().startswith(b'OK'):
                    raise Refused(status.decode(errors='replace')[:300])
                return status, untagged
            untagged.append(r)

    def tag(self):
        self.n += 1
        return b'p%d' % self.n

    def cmd(self, parts, nonsync=False):
        tag = self.tag()
        return self.result(tag, self.write(tag, parts, nonsync))

    def pipeline(self, commands):
        """Send every command before reading any answer; needs non-synchronizing literals."""
        tags = [self.tag() for _ in commands]
        for tag, parts in zip(tags, commands):
            self.write(tag, parts, True)
        return [self.result(tag) for tag in tags]

    def close(self):
        try:
            self.cmd([b'LOGOUT'])
        except Exception:
            pass
        try:
            self.s.close()
        except Exception:
            pass


def astring(text):
    return b'"' + text.encode().replace(b'\\', b'\\\\').replace(b'"', b'\\"') + b'"'


def chunk(size):
    """An envelope-shaped chunk (BLOBS.md §4.1): magic, version, kind 4, ciphertext, tag."""
    return b'TNDM\x02\x04' + TRICKY + os.urandom(size - len(TRICKY)) + os.urandom(16)


ESCAPED_ANYWHERE = (0x3D, 0x00, 0x0A, 0x0D)                  # '=' first, then NUL, LF, CR
ESCAPED_AT_LINE_START = b'\t .F>'                               # stripped, dot-stuffed or mbox-quoted by some stores
ESCAPED_AT_LINE_END = b'\t '


def escape8(data, width=490):
    """The envelope as legal 8bit mail content, the way yEnc does it: a few bytes written as
    '=' and the byte plus 64, lines of at most `width` bytes of input, so never over the 998 a
    line may have even if every byte is escaped. About 2% larger."""
    lines = []
    for i in range(0, len(data), width):
        line = data[i:i + width]
        first, last = line[:1], line[-1:]
        body = line[1:-1] if len(line) > 1 else b''
        for b in ESCAPED_ANYWHERE:
            body = body.replace(bytes([b]), b'=' + bytes([(b + 64) & 255]))

        def edge(byte, also):
            if byte and (byte[0] in ESCAPED_ANYWHERE or byte in [bytes([x]) for x in also]):
                return b'=' + bytes([(byte[0] + 64) & 255])
            return byte
        lines.append(edge(first, ESCAPED_AT_LINE_START) + body + (edge(last, ESCAPED_AT_LINE_END) if len(line) > 1 else b''))
    return b'\r\n'.join(lines) + b'\r\n'


def unescape8(body):
    flat = bytes(body).replace(b'\r', b'').replace(b'\n', b'')
    return re.sub(rb'=(.)', lambda m: bytes([(m.group(1)[0] - 64) & 255]), flat, flags=re.S)


def message(run, n, envelope, binary, attachment=False, eightbit=False):
    head = ('From: Tandem <tandem@tandem.airforce>\r\n'
            f'Date: {formatdate(usegmt=True)}\r\n'
            f'Message-ID: <{n}.{run}@tandem.airforce>\r\n'
            'Subject: tandem\r\nMIME-Version: 1.0\r\nTandem-Version: 2\r\n'
            f'Tandem-Channel: {base64.b32encode(os.urandom(20)).decode().lower()}\r\n'
            f'Tandem-Blob: {address(envelope)}\r\n').encode()
    if eightbit:
        return head + b'Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n\r\n' + escape8(envelope)
    if binary:
        return head + b'Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n' + envelope
    text = base64.encodebytes(envelope).replace(b'\n', b'\r\n')     # 76 characters a line
    if attachment:      # the same base64, declared as an encoding of binary content instead of as text
        return head + b'Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n' + text
    return head + b'Content-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: 7bit\r\n\r\n' + text


def address(envelope):
    return base64.b32encode(hashlib.sha256(envelope).digest()).decode().lower().rstrip('=')


def appended_uids(status):
    found = re.search(rb'APPENDUID \d+ ([0-9:,]+)', status)
    uids = []
    for part in found.group(1).split(b',') if found else []:
        lo, _, hi = part.partition(b':')
        uids += range(int(lo), int(hi or lo) + 1)
    return uids


def literals(untagged):
    """{uid: largest literal} from FETCH responses."""
    out = {}
    for r in untagged:
        text = b' '.join(p for p in r if not isinstance(p, Lit))
        uid = re.search(rb'UID (\d+)', text)
        lits = [p for p in r if isinstance(p, Lit)]
        if b' FETCH ' in r[0] and uid and lits:
            out[int(uid.group(1))] = max(lits, key=len)
    return out


def decoded(body, binary):
    if binary:
        return bytes(body)
    try:
        return base64.b64decode(b''.join(body.split()))
    except Exception:
        return None


def quota(c, folder):
    """(used, limit) of the STORAGE resource in bytes, or None where the server has no QUOTA."""
    if 'QUOTA' not in c.caps:
        return None
    try:
        _, untagged = c.cmd([b'GETQUOTAROOT ' + astring(folder)])
    except Refused:
        return None
    for r in untagged:
        found = re.search(rb'STORAGE (\d+) (\d+)', r[0])
        if r[0].upper().startswith(b'* QUOTA ') and found:
            return int(found.group(1)) * KIB, int(found.group(2)) * KIB
    return None


def cell(c, folder, run, counter, size, count, binary, mode):
    """Append `count` chunks of `size` in one mode, fetch them back, compare."""
    item = b'BINARY.PEEK[1]' if binary else b'BODY.PEEK[TEXT]'
    envelopes = [chunk(size) for _ in range(count)]
    messages = [message(run, next(counter), e, binary) for e in envelopes]
    out = {'form': 'binary' if binary else 'text', 'mode': mode, 'chunk': size, 'count': count,
           'envelope_bytes': sum(map(len, envelopes)), 'message_bytes': sum(map(len, messages))}
    prefix = b'APPEND ' + astring(folder) + b' (\\Seen) '
    start = time.monotonic()
    if mode == 'one at a time':
        uids = [u for m in messages for u in appended_uids(c.cmd([prefix, (m, binary)])[0])]
    elif mode == 'pipelined':
        uids = [u for status, _ in c.pipeline([[prefix, (m, binary)] for m in messages]) for u in appended_uids(status)]
    else:
        parts = [prefix]
        for i, m in enumerate(messages):
            parts += [b' (\\Seen) '] * (i > 0) + [(m, binary)]
        uids = appended_uids(c.cmd(parts, nonsync='LITERAL+' in c.caps)[0])
    out['append_seconds'] = round(time.monotonic() - start, 3)
    out['append_mib_per_second'] = round(out['envelope_bytes'] / 2**20 / max(out['append_seconds'], 1e-6), 2)
    if len(uids) != count:
        out['error'] = f'{len(uids)} UIDs for {count} appends'
        return out, uids

    # Fetching does not depend on how the chunks were appended: several UIDs in one command is
    # plain IMAP (RFC 9051 §6.4.9), so both ways are measured in every cell.
    c.cmd([b'SELECT ' + astring(folder)])
    mib = out['envelope_bytes'] / 2**20
    start = time.monotonic()
    single = {}
    for uid in uids:
        single.update(literals(c.cmd([b'UID FETCH %d (' % uid + item + b')'])[1]))
    out['fetch_single_seconds'] = round(time.monotonic() - start, 3)
    out['fetch_single_mib_per_second'] = round(mib / max(out['fetch_single_seconds'], 1e-6), 2)
    start = time.monotonic()
    batched = literals(c.cmd([b'UID FETCH ' + b','.join(b'%d' % u for u in uids) + b' (' + item + b')'])[1])
    out['fetch_batched_seconds'] = round(time.monotonic() - start, 3)
    out['fetch_batched_mib_per_second'] = round(mib / max(out['fetch_batched_seconds'], 1e-6), 2)
    out['fetched_bytes'] = sum(map(len, single.values()))
    out['intact'] = all(decoded(got.get(uid, b''), binary) == e for got in (single, batched) for uid, e in zip(uids, envelopes))
    return out, uids


def append_batch(c, prefix, messages, binary):
    """Append a batch the best way the server offers (BLOBS.md section 4.3). Returns the UIDs."""
    if 'MULTIAPPEND' in c.caps:
        parts = [prefix]
        for i, m in enumerate(messages):
            parts += [b' (\\Seen) '] * (i > 0) + [(m, binary)]
        return appended_uids(c.cmd(parts, nonsync='LITERAL+' in c.caps)[0])
    if 'LITERAL+' in c.caps:
        return [u for status, _ in c.pipeline([[prefix, (m, binary)] for m in messages]) for u in appended_uids(status)]
    return [u for m in messages for u in appended_uids(c.cmd([prefix, (m, binary)])[0])]


def parallel(connect_raw, folder, run, counter, form, counts, total=16, size=1024 * KIB, batch=4):
    """`total` chunks spread over k connections at once, for each k: appended one at a
    time and in batches of `batch`, then fetched one at a time and one command a connection."""
    binary = form == 'binary'
    undo = unescape8 if form == '8bit' else (lambda body: decoded(body, binary))
    item = b'BINARY.PEEK[1]' if binary else b'BODY.PEEK[TEXT]'
    prefix = b'APPEND ' + astring(folder) + b' (\\Seen) '
    out = []
    for k in counts:
        conns, errors = [], []
        try:
            for _ in range(k):
                conns.append(connect_raw())
                conns[-1].cmd([b'SELECT ' + astring(folder)])
        except Exception as e:
            out.append({'connections': k, 'error': f'{len(conns)} opened, then ' + type(e).__name__ + ': ' + str(e)[:160]})
            for c in conns:
                c.close()
            break
        shares = [list(range(i, total, k)) for i in range(k)]

        def run_all(work):
            """Start work(connection, share) on every connection at the same moment; seconds until all are done."""
            gate = threading.Barrier(k + 1)

            def one(c, share):
                gate.wait()
                try:
                    work(c, share)
                except Exception as e:
                    errors.append(type(e).__name__ + ': ' + str(e)[:160])
            threads = [threading.Thread(target=one, args=(c, share)) for c, share in zip(conns, shares)]
            for t in threads:
                t.start()
            gate.wait()
            start = time.monotonic()
            for t in threads:
                t.join()
            return time.monotonic() - start

        # Batching needs MULTIAPPEND or LITERAL+; without them it would repeat the first mode.
        for mode in ('one at a time',) + ('batched',) * bool({'MULTIAPPEND', 'LITERAL+'} & conns[0].caps):
            envelopes = [chunk(size) for _ in range(total)]
            messages = [message(run, next(counter), e, binary, form == 'attachment', form == '8bit') for e in envelopes]
            uids, got, again = [None] * total, {}, {}

            def up(c, share):
                if mode == 'one at a time':
                    for i in share:
                        uids[i] = appended_uids(c.cmd([prefix, (messages[i], binary)])[0])[0]
                else:
                    for j in range(0, len(share), batch):
                        group = share[j:j + batch]
                        for i, uid in zip(group, append_batch(c, prefix, [messages[i] for i in group], binary)):
                            uids[i] = uid

            def down_single(c, share):
                c.cmd([b'NOOP'])    # learn of the others' appends
                for i in share:
                    got.update(literals(c.cmd([b'UID FETCH %d (' % uids[i] + item + b')'])[1]))

            def down_batched(c, share):
                again.update(literals(c.cmd([b'UID FETCH ' + b','.join(b'%d' % uids[i] for i in share) + b' (' + item + b')'])[1]))

            mib = total * size / 2**20
            row = {'connections': k, 'mode': mode, 'form': form, 'chunk': size, 'count': total}
            log(f'{k} connections, {mode}: appending {total} chunks of {size // KIB} KiB, then fetching them ...')
            seconds = run_all(up)
            row['append_seconds'], row['append_mib_per_second'] = round(seconds, 3), round(mib / max(seconds, 1e-6), 2)
            if not errors and all(uids):
                # Both ways of fetching, whatever the append mode was.
                seconds = run_all(down_single)
                row['fetch_single_seconds'], row['fetch_single_mib_per_second'] = round(seconds, 3), round(mib / max(seconds, 1e-6), 2)
                seconds = run_all(down_batched)
                row['fetch_batched_seconds'], row['fetch_batched_mib_per_second'] = round(seconds, 3), round(mib / max(seconds, 1e-6), 2)
                row['intact'] = all(undo(g.get(uid, b'')) == e for g in (got, again) for uid, e in zip(uids, envelopes))
            if errors:
                row['errors'], errors = errors[:4], []
            out.append(row)
            log(f"{k} connections, {mode:13}: " + (str(row['errors']) if 'errors' in row else
                f"append {row['append_mib_per_second']:6.2f} MiB/s, fetch one at a time {row['fetch_single_mib_per_second']:6.2f}, "
                f"one command each {row['fetch_batched_mib_per_second']:6.2f}, intact {row['intact']}"))
        for c in conns:
            c.close()
    return out


def forms(connect_raw, caps, folder, run, counter, which, total=8, size=1024 * KIB):
    """One connection, `total` chunks a row: each way of carrying a chunk, with and without
    COMPRESS=DEFLATE, appended the best way the server offers and fetched with one command."""
    out, mib = [], total * size / 2**20
    prefix = b'APPEND ' + astring(folder) + b' (\\Seen) '
    for compress in (False, True):
        if compress and 'COMPRESS=DEFLATE' not in caps:
            continue
        c = connect_raw(compress)
        c.cmd([b'SELECT ' + astring(folder)])
        for form in which:
            if form == 'binary' and 'BINARY' not in caps:
                continue
            binary = form == 'binary'
            item = b'BINARY.PEEK[1]' if binary else b'BODY.PEEK[TEXT]'
            envelopes = [chunk(size) for _ in range(total)]
            messages = [message(run, next(counter), e, binary, form == 'attachment', form == '8bit') for e in envelopes]
            undo = unescape8 if form == '8bit' else (lambda body: decoded(body, binary))
            row = {'form': form, 'compress': compress, 'chunk': size, 'count': total, 'message_bytes': sum(map(len, messages))}
            log(f"{form}{' compressed' if compress else ''}: appending {total} chunks, then fetching them ...")
            try:
                before = c.wire.sent if c.wire else 0
                start = time.monotonic()
                uids = append_batch(c, prefix, messages, binary)
                seconds = time.monotonic() - start
                row['append_seconds'], row['append_mib_per_second'] = round(seconds, 3), round(mib / max(seconds, 1e-6), 2)
                row['wire_bytes_sent'] = c.wire.sent - before if c.wire else row['message_bytes']
                c.cmd([b'NOOP'])
                before = c.wire.received if c.wire else 0
                start = time.monotonic()
                got = literals(c.cmd([b'UID FETCH ' + b','.join(b'%d' % u for u in uids) + b' (' + item + b')'])[1])
                seconds = time.monotonic() - start
                row['fetch_seconds'], row['fetch_mib_per_second'] = round(seconds, 3), round(mib / max(seconds, 1e-6), 2)
                row['wire_bytes_received'] = c.wire.received - before if c.wire else sum(map(len, got.values()))
                row['intact'] = len(uids) == total and all(undo(got.get(u, b'')) == e for u, e in zip(uids, envelopes))
                if form == '8bit' and uids:     # what the server did to it
                    _, untagged = c.cmd([b'UID FETCH %d (BODY.PEEK[HEADER.FIELDS (Content-Type Content-Transfer-Encoding)] RFC822.SIZE)' % uids[0]])
                    head = literals(untagged).get(uids[0], b'').decode(errors='replace')
                    row['stored_headers'] = sorted(' '.join(x.split()) for x in head.splitlines() if x.strip())
                    row['identical_bytes'] = bytes(got.get(uids[0], b'')) == messages[0].split(b'\r\n\r\n', 1)[1]
            except (Refused, EOFError, OSError) as e:
                row['error'] = type(e).__name__ + ': ' + str(e)[:200]
                c.close()
                c = connect_raw(compress)
                c.cmd([b'SELECT ' + astring(folder)])
            out.append(row)
            log(f"{form:10} {'compressed' if compress else 'plain':10}: " + (row.get('error') or
                f"append {row['append_mib_per_second']:6.2f} MiB/s ({row['wire_bytes_sent'] / row['message_bytes']:.2f} of the bytes on the wire), "
                f"fetch {row['fetch_mib_per_second']:6.2f} MiB/s, intact {row['intact']}"))
        c.close()
    return out


def other_fetches(c, folder, uid, envelope, binary):
    """What the server hands back for one chunk through each way of asking."""
    c.cmd([b'SELECT ' + astring(folder)])
    out = {}
    for item in (b'BODY.PEEK[TEXT]', b'BODY.PEEK[1]', b'BINARY.PEEK[1]', b'BODY.PEEK[]'):
        name = item.decode()
        try:
            got = literals(c.cmd([b'UID FETCH %d (' % uid + item + b')'])[1]).get(uid)
        except Refused as e:
            out[name] = 'refused: ' + str(e)[:120]
            continue
        if got is None:
            out[name] = 'no literal'
            continue
        body = got.split(b'\r\n\r\n', 1)[-1] if item == b'BODY.PEEK[]' else got
        if bytes(body) == envelope:
            out[name] = 'the envelope, byte for byte'
        elif decoded(body, False) == envelope:
            out[name] = 'base64 of the envelope'
        else:
            out[name] = f'different: {len(body)} bytes for an envelope of {len(envelope)}'
    # The headers a reader decodes by, as the server now reports them.
    _, untagged = c.cmd([b'UID FETCH %d (BODY.PEEK[HEADER.FIELDS (Content-Type Content-Transfer-Encoding)] RFC822.SIZE)' % uid])
    head = literals(untagged).get(uid, b'').decode(errors='replace')
    out['stored_headers'] = sorted(' '.join(line.split()) for line in head.splitlines() if line.strip())
    size = re.search(rb'RFC822\.SIZE (\d+)', b' '.join(p for r in untagged for p in r if not isinstance(p, Lit)))
    out['stored_size'] = int(size.group(1)) if size else None
    return out


def index_scan(c, folder, expected):
    """The fetch a chunk index is built from (BLOBS.md §4.3): UID, stored time and two headers."""
    c.cmd([b'SELECT ' + astring(folder)])
    start = time.monotonic()
    _, untagged = c.cmd([b'UID FETCH 1:* (UID INTERNALDATE BODY.PEEK[HEADER.FIELDS (Tandem-Channel Tandem-Blob)])'])
    seconds = round(time.monotonic() - start, 3)
    rows = [r for r in untagged if b' FETCH ' in r[0]]
    whole = sum(1 for r in rows
                if b'INTERNALDATE' in b' '.join(p for p in r if not isinstance(p, Lit))
                and any(isinstance(p, Lit) and b'tandem-blob:' in p.lower() and b'tandem-channel:' in p.lower() for p in r))
    return {'messages': len(rows), 'expected': expected, 'with_date_and_both_headers': whole, 'seconds': seconds}


def push_during_upload(connect_raw, connect_lib, feed, blobs, run, counter, samples, wait):
    """How long a small append to the feed folder takes to reach an idling connection,
    with nothing else going on and while another connection uploads 1 MiB chunks."""
    a, b = connect_lib(), connect_lib()
    watcher = Watcher(b, feed, wait)

    def small():
        raw, _ = imap_probe.message(run, next(counter))
        a.append(quote(feed), '(\\Seen)', None, raw)

    out = {'idle': [watcher.wait_for({'EXISTS'}, small)['pushed'] for _ in range(samples)]}
    up, stop, sent = connect_raw(), threading.Event(), [0]

    def upload():
        while not stop.is_set():
            up.cmd([b'APPEND ' + astring(blobs) + b' (\\Seen) ', (message(run, next(counter), chunk(1024 * KIB), False), False)])
            sent[0] += 1
    t = threading.Thread(target=upload)
    t.start()
    try:
        time.sleep(1)
        out['uploading'] = [watcher.wait_for({'EXISTS'}, small)['pushed'] for _ in range(samples)]
    finally:
        stop.set()
        t.join(120)
        up.close()
    out['chunks_uploaded_meanwhile'] = sent[0]
    for m in (a, b):
        try:
            m.logout()
        except Exception:
            pass
    return out


def probe(host, port, user, password, verify=True, name=None, samples=3, wait=60, counts=None, carry=False, form=None):
    run = os.urandom(4).hex()
    feed, blobs = f'Tandem-probe-{run}', f'Tandem-probe-{run}-blobs'
    r = {'host': name or host, 'date': time.strftime('%Y-%m-%d'), 'run': run, 'probe': 'blobs'}

    def connect_raw(compress=False):
        return Raw(host, port, user, password, verify, compress)

    def connect_lib():
        ctx = ssl.create_default_context()
        if not verify:
            ctx.check_hostname, ctx.verify_mode = False, ssl.CERT_NONE
        m = imaplib.IMAP4_SSL(host, port, ssl_context=ctx, timeout=120)
        m.login(user, password)
        return m

    c = connect_raw()
    limit = next((int(x.split('=')[1]) for x in c.caps if x.startswith('APPENDLIMIT=')), None)
    r['capabilities'] = sorted(c.caps)
    r['has'] = {x: x in c.caps for x in ('BINARY', 'MULTIAPPEND', 'LITERAL+', 'LITERAL-', 'QUOTA')}
    r['appendlimit'] = limit
    for folder in (feed, blobs):
        c.cmd([b'CREATE ' + astring(folder)])
    log(f'created {feed} and {blobs}; BINARY {r["has"]["BINARY"]}, MULTIAPPEND {r["has"]["MULTIAPPEND"]}, '
        f'LITERAL+ {r["has"]["LITERAL+"]}, APPENDLIMIT {limit}')
    counter = iter(range(10**9))
    # What a transfer pays before its first chunk: TLS, LOGIN, CAPABILITY and SELECT on a new connection.
    r['connect_seconds'] = []
    for _ in range(3):
        start = time.monotonic()
        fresh = connect_raw()
        fresh.cmd([b'SELECT ' + astring(blobs)])
        r['connect_seconds'].append(round(time.monotonic() - start, 3))
        fresh.close()
    log('a new connection, ready to transfer, took', r['connect_seconds'], 'seconds')
    r['cells'], r['quota'] = [], {'start': quota(c, blobs)}
    modes = ['one at a time'] + ['pipelined'] * ('LITERAL+' in c.caps) + ['MULTIAPPEND'] * ('MULTIAPPEND' in c.caps)
    try:
        if carry:
            r['probe'] = 'blobs-forms'
            del r['cells'], r['quota']
            r['forms'] = forms(connect_raw, c.caps, blobs, run, counter, carry)
            return r
        if counts:
            r['probe'] = 'blobs-parallel'
            del r['cells'], r['quota']
            r['parallel'] = parallel(connect_raw, blobs, run, counter, form or ('binary' if 'BINARY' in c.caps else 'text'), counts)
            return r
        for binary in (False, True):
            if binary and 'BINARY' not in c.caps:
                continue
            form, stored, kept = 'binary' if binary else 'text', 0, None
            for mode in modes:
                for size, count in SIZES:
                    try:
                        out, uids = cell(c, blobs, run, counter, size, count, binary, mode)
                    except (Refused, EOFError, OSError) as e:
                        out, uids = {'form': form, 'mode': mode, 'chunk': size, 'count': count,
                                     'error': type(e).__name__ + ': ' + str(e)[:200]}, []
                        c.close()
                        c = connect_raw()
                    r['cells'].append(out)
                    stored += out.get('message_bytes', 0) if 'error' not in out else 0
                    log(f"{form:6} {mode:13} {size // KIB:5} KiB x{count:2}: "
                        + (out.get('error') or f"append {out['append_mib_per_second']:7.2f} MiB/s, "
                           f"fetch one at a time {out['fetch_single_mib_per_second']:7.2f}, "
                           f"in one command {out['fetch_batched_mib_per_second']:7.2f}, intact {out['intact']}"))
                    if uids and kept is None and size == 1024 * KIB:
                        kept = uids[0]
            if kept is not None:
                envelope = decoded(literals(c.cmd([b'UID FETCH %d (' % kept
                                                   + (b'BINARY.PEEK[1]' if binary else b'BODY.PEEK[TEXT]') + b')'])[1])[kept], binary)
                r.setdefault('other_fetches', {})[form] = other_fetches(c, blobs, kept, envelope, binary)
                log(form, 'fetched other ways:', r['other_fetches'][form])
            r['quota'][f'after_{form}'] = quota(c, blobs)
            r['quota'][f'{form}_message_bytes'] = stored
        r['index_scan'] = index_scan(c, blobs, sum(x['count'] for x in r['cells'] if 'error' not in x))
        log('index scan', r['index_scan'])
        r['push'] = push_during_upload(connect_raw, connect_lib, feed, blobs, run, counter, samples, wait)
        log('push', r['push'])
    finally:
        c.close()
        notes = []
        try:
            a = connect_lib()
            special = special_folders(a)
            for folder in (blobs, feed):
                a.select(quote(folder))
                a.uid('STORE', '1:*', '+FLAGS.SILENT', '(\\Deleted)')
                a.expunge()
                note = cleanup(a, folder, special, run)
                notes += [] if note == 'clean' else note
            a.logout()
        except Exception as e:
            notes.append(type(e).__name__ + ': ' + str(e)[:200])
        r['cleanup'] = notes or 'clean'
        log('cleanup', r['cleanup'])
    return r


def main():
    p = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    p.add_argument('host', nargs='?')
    p.add_argument('user', nargs='?')
    p.add_argument('--port', type=int, default=993)
    p.add_argument('--keychain', metavar='SERVICE', help='read the password from this macOS keychain item')
    p.add_argument('--forms', nargs='?', const='text,attachment,binary,8bit', metavar='FORMS', type=lambda v: v.split(','),
                   help='compare these ways of carrying a chunk, with and without COMPRESS=DEFLATE, instead '
                        '(default text,attachment,binary,8bit)')
    p.add_argument('--form', choices=['text', 'attachment', 'binary', '8bit'],
                   help='with --parallel, carry chunks this way (default binary where offered, else text)')
    p.add_argument('--stalwart', action='store_true', help='run against a disposable Stalwart in Docker')
    p.add_argument('--fastmail', action='store_true', help='run against Fastmail with ~/.config/tandem credentials')
    p.add_argument('--samples', type=int, default=3, help='appends timed for live updates (default 3)')
    p.add_argument('--parallel', nargs='?', const='1,2,4,8', metavar='COUNTS', type=lambda v: [int(x) for x in v.split(',')],
                   help='measure these numbers of connections at once instead (default 1,2,4,8)')
    p.add_argument('--out', default=os.path.join(os.path.dirname(os.path.abspath(__file__)), 'results'))
    args = p.parse_args()
    if not hasattr(imaplib.IMAP4, 'idle'):
        sys.exit('needs Python 3.14 or later for IMAP IDLE')
    if args.stalwart:
        sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'conformance'))
        from stalwart import IMAGE, Stalwart
        with Stalwart() as server:
            report = probe('127.0.0.1', server.ports[993], server.username, server.password, verify=False,
                           name='stalwart-' + IMAGE.split(':')[1].split('@')[0], counts=args.parallel, carry=args.forms, form=args.form)
    elif args.fastmail:
        config = os.path.expanduser('~/.config/tandem')
        token = open(os.path.join(config, 'fastmail.json')).read().strip()
        password = open(os.path.join(config, 'fastmail-imap.json')).read().strip()
        request = urllib.request.Request('https://api.fastmail.com/jmap/session', headers={'Authorization': 'Bearer ' + token})
        with urllib.request.urlopen(request, timeout=30) as answer:
            user = json.load(answer)['username']
        report = probe('imap.fastmail.com', 993, user, password, samples=args.samples, counts=args.parallel, carry=args.forms, form=args.form)
    else:
        if not (args.host and args.user):
            p.error('give a host and a user, --stalwart or --fastmail')
        if args.keychain:
            found = subprocess.run(['security', 'find-generic-password', '-s', args.keychain, '-a', args.user, '-w'],
                                   capture_output=True, text=True)
            if found.returncode:
                sys.exit(f'could not read keychain item {args.keychain}/{args.user}: {found.stderr.strip() or found.returncode}')
            password = found.stdout.rstrip('\n')
        else:
            password = getpass.getpass(f'Password for {args.user} at {args.host}: ')
        report = probe(args.host, args.port, args.user, password, samples=args.samples, counts=args.parallel, carry=args.forms, form=args.form)
    os.makedirs(args.out, exist_ok=True)
    path = os.path.join(args.out, f"{report['host']}-{report['probe']}-{report['date']}-{report['run']}.json")
    with open(path, 'w') as f:
        json.dump(report, f, indent=2, sort_keys=True)
        f.write('\n')
    print(json.dumps(report, indent=2, sort_keys=True))
    log('wrote', path)


if __name__ == '__main__':
    main()
