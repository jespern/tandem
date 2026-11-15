"""Private JSON-lines helper for probe-fastmail.mjs; no credentials in argv/logs."""
import base64
from email import policy
from email.parser import BytesParser
import hashlib
import imaplib
import json
import ssl
import sys
import threading


def emit(kind, **fields):
    print(json.dumps(dict(kind=kind, **fields)), flush=True)


config = json.loads(sys.stdin.readline())
connections = []
watcher = None
try:
    for _ in range(2):
        client = imaplib.IMAP4_SSL('imap.fastmail.com', 993, ssl_context=ssl.create_default_context(), timeout=config['timeout'])
        connections.append(client)
        client.login(config['user'], config['password'])
    reader, writer = connections
    if reader.select(config['folder'])[0] != 'OK':
        raise RuntimeError('cannot select probe mailbox')
    emit('ready')

    def watch():
        try:
            with reader.idle(duration=config['timeout']) as events:
                emit('idle-ready')
                for kind, data in events:
                    if kind in ('EXISTS', 'RECENT', 'FETCH'):
                        break
                else:
                    raise TimeoutError('no IMAP push before deadline')
            kind, data = reader.uid('SEARCH', None, 'ALL')
            if kind != 'OK':
                raise RuntimeError('IMAP scan failed')
            hashes = []
            for uid in data[0].split():
                kind, values = reader.uid('FETCH', uid, '(BODY.PEEK[])')
                if kind != 'OK':
                    raise RuntimeError('IMAP fetch failed')
                raw = next(value[1] for value in values if isinstance(value, tuple))
                message = BytesParser(policy=policy.default).parsebytes(raw)
                text = message.get_payload(decode=True).decode(message.get_content_charset() or 'us-ascii')
                body = base64.b64decode(''.join(text.split()), validate=True)
                hashes.append(base64.urlsafe_b64encode(hashlib.sha256(body).digest()).rstrip(b'=').decode())
            emit('imap-push', hashes=hashes)
        except Exception as error:
            emit('error', error=type(error).__name__)

    for line in sys.stdin:
        command = json.loads(line)
        if command['op'] == 'idle':
            watcher = threading.Thread(target=watch)
            watcher.start()
        elif command['op'] == 'append':
            if writer.append(config['folder'], '\\Seen', None, base64.b64decode(command['wire'], validate=True))[0] != 'OK':
                raise RuntimeError('IMAP append failed')
            emit('appended')
        elif command['op'] == 'close':
            break
finally:
    # Closing the socket interrupts a pending IDLE on errors. On a successful
    # run the watcher has already returned from its one push-driven scan.
    if watcher and watcher.is_alive():
        connections[0].shutdown()
        watcher.join()
    for client in connections:
        try:
            client.logout()
        except Exception:
            pass
