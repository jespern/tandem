"""Credential loading and isolated IMAP test folders; only our exact folders are deleted."""
from contextlib import contextmanager
import email
from email import policy
import hashlib
import imaplib
import json
from pathlib import Path
import ssl
import urllib.request
import uuid

from harness import Failed


def quote(value):
    return '"' + value.replace('\\', '\\\\').replace('"', '\\"') + '"'


class Mailbox:
    def __init__(self, hub):
        self.hub = hub

    @contextmanager
    def connection(self):
        ctx = ssl._create_unverified_context() if self.hub.get('insecure') else ssl.create_default_context()
        m = imaplib.IMAP4_SSL(self.hub['host'], self.hub['port'], ssl_context=ctx, timeout=60)
        try:
            m.login(self.hub['user'], self.hub['password'])
            yield m
        finally:
            try:
                m.logout()
            except (OSError, imaplib.IMAP4.error):
                m.shutdown()

    @property
    def blob_folder(self):
        return self.hub['folder'] + '-blobs'

    def chunks(self):
        with self.connection() as m:
            if m.select(quote(self.blob_folder), readonly=True)[0] != 'OK':
                return []
            typ, data = m.uid('SEARCH', None, 'ALL')
            if typ != 'OK':
                raise Failed('could not list test chunks')
            rows = []
            for uid in data[0].split():
                typ, parts = m.uid('FETCH', uid, '(BODY.PEEK[])')
                if typ != 'OK':
                    raise Failed('could not fetch test chunk')
                raw = next((p[1] for p in parts if isinstance(p, tuple)), None)
                if raw is None:
                    raise Failed('test chunk disappeared during inspection')
                msg = email.message_from_bytes(raw, policy=policy.default)
                envelope = msg.get_payload(decode=True)
                if msg.get('Tandem-Body', '').lower() == 'escaped':
                    body, decoded, i = envelope.replace(b'\r', b'').replace(b'\n', b''), bytearray(), 0
                    while i < len(body):
                        b = body[i]; i += 1
                        if b == 61:
                            if i == len(body):
                                break
                            b = (body[i] - 64) % 256; i += 1
                        decoded.append(b)
                    envelope = bytes(decoded)
                import base64
                address = base64.b32encode(hashlib.sha256(envelope).digest()).decode().rstrip('=').lower()
                rows.append(dict(uid=uid.decode(), raw=raw, address=msg.get('Tandem-Blob'),
                                 channel=msg.get('Tandem-Channel'), verified=address == msg.get('Tandem-Blob'),
                                 form=msg.get('Tandem-Body', msg.get('Content-Transfer-Encoding'))))
            return rows

    def append(self, raw):
        with self.connection() as m:
            if m.append(quote(self.blob_folder), None, None, raw)[0] != 'OK':
                raise Failed('could not append a test chunk')

    def destroy(self, ids):
        with self.connection() as m:
            if m.select(quote(self.blob_folder))[0] != 'OK':
                raise Failed('could not select test chunks')
            if ids:
                if m.uid('STORE', ','.join(ids), '+FLAGS.SILENT', '(\\Deleted)')[0] != 'OK':
                    raise Failed('could not mark test chunks')
                if m.uid('EXPUNGE', ','.join(ids))[0] != 'OK':
                    raise Failed('could not destroy test chunks')

    def cleanup(self):
        root = self.hub['folder']
        if not root.startswith('Tandem-interop-') or not root[len('Tandem-interop-'):].isalnum():
            raise Failed('refusing cleanup outside an isolated test namespace')
        with self.connection() as m:
            for folder in (self.blob_folder, root):
                typ, found = m.list('""', quote(folder))
                if typ != 'OK':
                    raise Failed('could not inspect isolated test folder')
                if found == [None]:
                    continue  # A scenario may never have opened the blob folder.
                if m.select(quote(folder))[0] != 'OK':
                    raise Failed('could not select isolated test folder for cleanup')
                if m.uid('STORE', '1:*', '+FLAGS.SILENT', '(\\Deleted)')[0] != 'OK':
                    raise Failed('could not clear isolated test folder')
                if m.expunge()[0] != 'OK':
                    raise Failed('could not expunge isolated test folder')
                m.unselect()
                if m.delete(quote(folder))[0] != 'OK':
                    raise Failed('could not delete isolated test folder')
                if m.list('""', quote(folder))[1] != [None]:
                    raise Failed('isolated test folder survived cleanup')


def fastmail():
    directory = Path.home() / '.config/tandem'
    try:
        token = (directory / 'fastmail.json').read_text().strip()
        password = (directory / 'fastmail-imap.json').read_text().strip()
    except OSError:
        raise Failed('Fastmail needs ~/.config/tandem/fastmail.json and fastmail-imap.json (same credentials as the probes)') from None
    request = urllib.request.Request('https://api.fastmail.com/jmap/session', headers={'Authorization': 'Bearer ' + token})
    with urllib.request.urlopen(request, timeout=30) as response:
        user = json.load(response)['username']
    return dict(host='imap.fastmail.com', port=993, user=user, password=password, insecure=False)


def isolated(config):
    return dict(config, folder='Tandem-interop-' + uuid.uuid4().hex)
