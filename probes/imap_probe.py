#!/usr/bin/env python3
"""Checks whether an IMAP account can host Tandem (DESIGN.md §10.3).

    probes/imap_probe.py imap.gmail.com alice@gmail.com
    probes/imap_probe.py imap.gmail.com alice@gmail.com --keychain tandem-probe
    probes/imap_probe.py imap.gmail.com alice@gmail.com --connections 20

The password is asked for, or read from the macOS keychain item SERVICE/USER
(add one with: security add-generic-password -s SERVICE -a USER -w). It is
never printed or written down; the report holds no username either.

Everything happens in a new folder named Tandem-probe-<run>. Afterwards the
probe deletes its messages, including copies a provider keeps elsewhere (Gmail
archives deleted messages to All Mail), and then the folder. Needs Python 3.14+
for IMAP IDLE. --connections opens that many logins at once to find the
provider's limit; other mail apps on the account may be refused while it runs.
"""
import argparse
import base64
import email
import getpass
import imaplib
import json
import os
import re
import ssl
import subprocess
import sys
import threading
import time
from email import policy
from email.message import EmailMessage
from email.utils import formatdate

BULK = 20        # appends timed back to back


def log(*a):
    print(*a, file=sys.stderr, flush=True)


def quote(name):
    return '"' + name.replace('\\', '\\\\').replace('"', '\\"') + '"'


def connect(host, port, user, password):
    m = imaplib.IMAP4_SSL(host, port, ssl_context=ssl.create_default_context(), timeout=60)
    m.login(user, password)
    return m


def message(run, n):
    """A message shaped like §10.4, with a random envelope-sized body."""
    body = os.urandom(300)
    m = EmailMessage(policy=policy.SMTP)
    m['From'] = 'Tandem <tandem@invalid>'
    m['Date'] = formatdate(usegmt=True)
    m['Message-ID'] = f'<{n}.{run}@tandem.invalid>'
    m['Subject'] = 'tandem'
    m['Tandem-Version'] = '2'
    m['Tandem-Channel'] = base64.b32encode(os.urandom(20)).decode().lower()
    text = base64.b64encode(body).decode()
    m.set_content('\r\n'.join(text[i:i + 76] for i in range(0, len(text), 76)) + '\r\n',
                  subtype='plain', charset='us-ascii', cte='7bit')
    return m.as_bytes(), body


def uid_of(response):
    found = re.search(rb'APPENDUID \d+ (\d+)', b' '.join(x for x in response if isinstance(x, bytes)))
    return found and found.group(1).decode()


def special_folders(m):
    """{'\\All': '[Gmail]/All Mail', '\\Trash': ...} from LIST (RFC 6154)."""
    out = {}
    typ, lines = m.list()
    for line in lines or []:
        if not isinstance(line, bytes):
            continue
        found = re.match(rb'\((.*?)\) (?:"(?:[^"\\]|\\.)*"|NIL) (.*)$', line)
        if not found:
            continue
        flags, name = found.group(1).decode(), found.group(2).decode()
        name = name[1:-1].replace('\\"', '"').replace('\\\\', '\\') if name.startswith('"') else name
        for use in ('\\All', '\\Trash'):
            if use in flags.split():
                out[use] = name
    return out


def search(m, folder, run):
    if m.select(quote(folder))[0] != 'OK':
        return []
    typ, data = m.uid('SEARCH', 'HEADER', 'Message-ID', quote(run))
    return data[0].split() if typ == 'OK' and data and data[0] else []


def purge(m, folder, uids):
    m.select(quote(folder))
    for uid in uids:
        m.uid('STORE', uid, '+FLAGS.SILENT', '(\\Deleted)')
    m.expunge()


UPDATES = ('EXISTS', 'EXPUNGE', 'VANISHED', 'RECENT', 'FETCH')


class Watcher:
    """A second connection idling on the folder, reporting pushed responses."""

    def __init__(self, m, folder, wait):
        self.m, self.folder, self.wait = m, folder, wait
        m.select(quote(folder))

    def drain(self):
        """Collect anything the server has queued, so it can't pass for the next change."""
        self.m.noop()
        return [k for k in UPDATES if self.m.response(k)[1] != [None]]

    def wait_for(self, kinds, action):
        """Start IDLE and run action. Returns how long the server took to push
        a response in kinds (None if it didn't within the wait) and, if it
        didn't, whether a NOOP afterwards showed the change anyway."""
        self.drain()
        ready, state = threading.Event(), {}

        def idle():
            with self.m.idle(duration=self.wait) as idler:
                ready.set()
                for typ, data in idler:
                    if typ in kinds and 'sent' in state:
                        state['at'] = time.monotonic()
                        break
        t = threading.Thread(target=idle)
        t.start()
        ready.wait(self.wait)
        time.sleep(0.2)  # let the server settle into IDLE
        state['sent'] = time.monotonic()
        action()
        t.join(self.wait + 5)
        if 'at' in state:
            return {'pushed': round(state['at'] - state['sent'], 3)}
        return {'pushed': None, 'seen_on_noop': bool(set(kinds) & set(self.drain()))}


def connections(host, port, user, password, limit):
    open_, error = [], None
    try:
        while len(open_) < limit:
            open_.append(connect(host, port, user, password))
    except Exception as e:
        error = type(e).__name__ + ': ' + str(e)[:200]
    for m in open_:
        try:
            m.logout()
        except Exception:
            pass
    return {'opened': len(open_), 'asked': limit, 'refusal': error}


def probe(host, port, user, password, want_connections, samples=3, wait=120):
    run = os.urandom(4).hex()
    folder = f'Tandem-probe-{run}'
    r = {'host': host, 'port': port, 'date': time.strftime('%Y-%m-%d'), 'run': run}

    a = connect(host, port, user, password)
    r['capabilities'] = sorted(a.capabilities)
    required = {'UIDPLUS', 'IDLE'}
    r['missing_required'] = sorted(required - set(a.capabilities))
    r['has'] = {c: c in a.capabilities for c in
                ('CONDSTORE', 'QRESYNC', 'MOVE', 'MULTIAPPEND', 'LITERAL+', 'LITERAL-', 'NAMESPACE', 'SPECIAL-USE')}
    special = special_folders(a)
    r['special_folders'] = special
    typ, data = a.create(quote(folder))
    if typ != 'OK':
        r['create'] = data[0].decode(errors='replace') if data else typ
        return r
    log(f'created {folder}')
    b = connect(host, port, user, password)
    watcher = Watcher(b, folder, wait)
    uids, bodies = [], {}

    def append(n):
        raw, body = message(run, n)
        typ, data = a.append(quote(folder), '(\\Seen)', None, raw)
        uid = uid_of(data)
        uids.append(uid)
        bodies[uid] = body
        return uid

    try:
        # Live updates: how long an append takes to reach an idling connection.
        r['push'] = []
        for n in range(samples):
            r['push'].append(watcher.wait_for({'EXISTS'}, lambda n=n: append(n)))
            log(f'append {n + 1}/{samples}:', r['push'][-1])
        r['appenduid'] = all(uids)

        # The body comes back byte for byte.
        a.select(quote(folder))
        ok = True
        for uid, body in bodies.items():
            typ, data = a.uid('FETCH', uid, '(BODY.PEEK[])')
            raw = next(x[1] for x in data if isinstance(x, tuple))
            m = email.message_from_bytes(raw, policy=policy.default)
            text = m.get_payload(decode=True).decode(m.get_content_charset() or 'us-ascii')
            ok = ok and base64.b64decode(''.join(text.split())) == body
        r['round_trip'] = ok

        # Bulk: BULK appends one at a time, as a key change would send them.
        start = time.monotonic()
        for n in range(samples, samples + BULK):
            append(n)
        r['bulk_seconds'] = {'messages': BULK, 'seconds': round(time.monotonic() - start, 2)}
        log('bulk', r['bulk_seconds'])

        # Destroy (§10.3): flag and UID EXPUNGE one message; is it pushed, and
        # does a copy survive anywhere else?
        victim = uids[0]
        a.select(quote(folder))

        def destroy():
            a.uid('STORE', victim, '+FLAGS.SILENT', '(\\Deleted)')
            a.uid('EXPUNGE', victim)
        r['expunge_push'] = watcher.wait_for({'EXPUNGE', 'VANISHED'}, destroy)
        log('expunge:', r['expunge_push'])
        time.sleep(3)
        lone = f'<0.{run}@tandem.invalid>'
        survivors = {}
        for use, name in special.items():
            if name and search(a, name, lone):
                survivors[use] = name
        r['destroyed_message_survives_in'] = survivors
        log('after destroy, copies in', survivors or 'nowhere')
    finally:
        try:
            b.logout()
        except Exception:
            pass
        r['cleanup'] = cleanup(a, folder, special, run)
        try:
            a.logout()
        except Exception:
            pass

    if want_connections:
        r['connections'] = connections(host, port, user, password, want_connections)
        log('connections', r['connections'])
    return r


def cleanup(a, folder, special, run):
    """Delete every probe message wherever it ended up, then the folder."""
    notes = []
    try:
        uids = search(a, folder, run)
        if uids:
            purge(a, folder, uids)
        a.close()
        if a.delete(quote(folder))[0] != 'OK':
            notes.append('folder not deleted')
        time.sleep(3)
        trash = special.get('\\Trash')
        everywhere = special.get('\\All')
        if everywhere:
            left = search(a, everywhere, run)
            if left and trash and 'MOVE' in a.capabilities:
                for uid in left:
                    a.uid('MOVE', uid, quote(trash))
            elif left:
                purge(a, everywhere, left)
        if trash:
            time.sleep(2)
            left = search(a, trash, run)
            if left:
                purge(a, trash, left)
        for name in filter(None, (everywhere, trash)):
            if search(a, name, run):
                notes.append(f'messages left in {name}')
    except Exception as e:
        notes.append(type(e).__name__ + ': ' + str(e)[:200])
    return notes or 'clean'


def main():
    p = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    p.add_argument('host')
    p.add_argument('user')
    p.add_argument('--port', type=int, default=993)
    p.add_argument('--keychain', metavar='SERVICE', help='read the password from this macOS keychain item')
    p.add_argument('--connections', type=int, default=0, metavar='N', help='also open up to N logins at once')
    p.add_argument('--samples', type=int, default=3, help='appends timed for live updates (default 3)')
    p.add_argument('--wait', type=int, default=120, metavar='SECONDS', help='how long to wait for each push (default 120)')
    p.add_argument('--out', default=os.path.join(os.path.dirname(os.path.abspath(__file__)), 'results'))
    args = p.parse_args()
    if not hasattr(imaplib.IMAP4, 'idle'):
        sys.exit('needs Python 3.14 or later for IMAP IDLE')
    if args.keychain:
        found = subprocess.run(['security', 'find-generic-password', '-s', args.keychain, '-a', args.user, '-w'],
                               capture_output=True, text=True)
        if found.returncode:
            sys.exit(f'could not read keychain item {args.keychain}/{args.user}: {found.stderr.strip() or found.returncode}'
                     + ('\n(36: the keychain is locked or needs a prompt this session cannot show,'
                        ' e.g. over SSH; leave out --keychain to be asked instead)' if found.returncode == 36 else ''))
        password = found.stdout.rstrip('\n')
    else:
        password = getpass.getpass(f'Password for {args.user} at {args.host}: ')
    report = probe(args.host, args.port, args.user, password, args.connections, args.samples, args.wait)
    os.makedirs(args.out, exist_ok=True)
    path = os.path.join(args.out, f"{args.host}-{report['date']}-{report['run']}.json")
    with open(path, 'w') as f:
        json.dump(report, f, indent=2, sort_keys=True)
        f.write('\n')
    print(json.dumps(report, indent=2, sort_keys=True))
    log('wrote', path)


if __name__ == '__main__':
    main()
