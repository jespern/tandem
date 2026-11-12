"""Small real JMAP/IMAP bindings used by the conformance tests."""
import base64
from email import policy
from email.message import EmailMessage
from email.parser import BytesParser
from email.utils import formatdate, make_msgid
import imaplib
import json
import re
import ssl
import urllib.parse
import urllib.request


def encoded_body(envelope):
    return '\r\n'.join(base64.b64encode(envelope).decode()[i:i+76] for i in range(0, len(base64.b64encode(envelope)), 76)) + '\r\n'


def decode_text(text):
    return base64.b64decode(''.join(text.split()), validate=True)


def message(channel, envelope, transfer='7bit', charset='us-ascii'):
    m = EmailMessage(policy=policy.SMTP)
    m['From'] = 'Tandem <tandem@invalid>'
    m['Date'] = formatdate(usegmt=True)
    m['Message-ID'] = make_msgid(domain='tandem.invalid')
    m['Subject'] = 'tandem'
    m['Tandem-Version'] = '1'
    m['Tandem-Channel'] = channel
    if transfer == 'base64':
        # email.set_content normalizes encoded newlines bytewise, corrupting
        # UTF-16. Encode the text first, then serialize the ASCII MIME payload.
        m.set_type('text/plain')
        m.set_param('charset', charset)
        m['Content-Transfer-Encoding'] = 'base64'
        m.set_payload(base64.encodebytes(encoded_body(envelope).encode(charset)).decode('ascii'))
    else:
        m.set_content(encoded_body(envelope), subtype='plain', charset=charset, cte=transfer)
    return m.as_bytes()


def decode_message(raw):
    m = BytesParser(policy=policy.default).parsebytes(raw)
    if m.is_multipart():
        raise ValueError('Tandem message must have a single text body')
    text = m.get_payload(decode=True).decode(m.get_content_charset() or 'us-ascii')
    return str(m['Tandem-Channel']).strip(), decode_text(text)


class JMAP:
    def __init__(self, session_url, authorization, context=None, fixture_origin=None):
        self.authorization, self.context = authorization, context
        self.fixture_origin = fixture_origin
        self.session = self.request(session_url)
        self.api = self.endpoint(self.session['apiUrl'])
        self.account = self.session['primaryAccounts']['urn:ietf:params:jmap:mail']

    def endpoint(self, url):
        # Only the disposable container advertises a hostname unavailable on the
        # host. Production endpoints retain the server's advertised origin.
        if self.fixture_origin:
            parsed = urllib.parse.urlsplit(url)
            return self.fixture_origin + parsed.path + ('?' + parsed.query if parsed.query else '')
        return url

    def request(self, url, body=None, accept=None):
        headers = {'Authorization': self.authorization}
        if accept:
            headers['Accept'] = accept
        if body is not None:
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(), headers=headers)
        response = urllib.request.urlopen(req, context=self.context, timeout=20)
        if accept == 'text/event-stream':
            return response
        with response:
            return json.load(response)

    def call(self, method, **args):
        args.setdefault('accountId', self.account)
        result = self.request(self.api, {'using': ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
                                         'methodCalls': [[method, args, '0']]})['methodResponses'][0]
        if result[0] == 'error' or any(result[1].get(k) for k in ('notCreated', 'notUpdated', 'notDestroyed')):
            raise RuntimeError(result)
        return result[1]

    def folder(self, name='Tandem'):
        found = self.call('Mailbox/query', filter={'name': name})['ids']
        if found:
            boxes = self.call('Mailbox/get', ids=found)['list']
            for box in boxes:
                if box['name'] == name and box.get('parentId') is None:
                    return box['id']
        return self.call('Mailbox/set', create={'folder': {'name': name}})['created']['folder']['id']

    def append(self, folder, channel, envelope):
        value = {'mailboxIds': {folder: True}, 'keywords': {'$seen': True},
                 'header:Tandem-Channel:asText': channel, 'header:Tandem-Version:asText': '1',
                 'header:From:asText': 'Tandem <tandem@invalid>', 'header:Subject:asText': 'tandem',
                 'textBody': [{'partId': 'body', 'type': 'text/plain'}],
                 'bodyValues': {'body': {'value': encoded_body(envelope)}}}
        return self.call('Email/set', create={'message': value})['created']['message']['id']

    def ids(self, folder):
        ids, position = [], 0
        while True:
            r = self.call('Email/query', filter={'inMailbox': folder}, position=position, limit=500)
            ids.extend(r['ids'])
            if not r['ids']:
                return ids
            position += len(r['ids'])

    def fetch(self, ids):
        if not ids:
            return {}
        r = self.call('Email/get', ids=ids, properties=['id', 'bodyValues', 'header:Tandem-Channel:asText'], fetchTextBodyValues=True)
        result = {}
        for value in r['list']:
            bodies = list(value['bodyValues'].values())
            if len(bodies) != 1 or bodies[0].get('isTruncated') or bodies[0].get('isEncodingProblem'):
                raise ValueError('missing, truncated or damaged body')
            result[value['id']] = (value['header:Tandem-Channel:asText'].strip(), decode_text(bodies[0]['value']))
        return result

    def state(self):
        return self.call('Email/get', ids=[], properties=['id'])['state']

    def changes(self, since, known, folder, max_changes=500):
        ids, destroyed = set(), set()
        while True:
            r = self.call('Email/changes', sinceState=since, maxChanges=max_changes)
            ids.update(r['created'] + r['updated'])
            destroyed.update(r['destroyed'])
            since = r['newState']
            if not r['hasMoreChanges']:
                break
        added, removed = {}, set(known) & destroyed
        if ids:
            objects = self.call('Email/get', ids=sorted(ids), properties=['id', 'mailboxIds', 'header:Tandem-Channel:asText'])['list']
            for obj in objects:
                if folder in obj['mailboxIds']:
                    if obj['id'] not in known:
                        added[obj['id']] = obj['header:Tandem-Channel:asText']
                elif obj['id'] in known:
                    removed.add(obj['id'])
        return added, removed, since

    def destroy(self, ids):
        if ids:
            self.call('Email/set', destroy=list(ids))

    def events(self):
        url = self.session['eventSourceUrl'].replace('{types}', 'Email').replace('{closeafter}', 'no').replace('{ping}', '60')
        return self.request(self.endpoint(url), accept='text/event-stream')


class IMAP:
    def __init__(self, host, port, username, password, context=None):
        self.client = imaplib.IMAP4_SSL(host, port, ssl_context=context or ssl.create_default_context(), timeout=20)
        self.client.login(username, password)

    def close(self):
        self.client.logout()

    def select(self, folder='Tandem'):
        typ, result = self.client.select(folder)
        if typ != 'OK':
            raise RuntimeError(result)
    def append(self, channel, envelope, folder='Tandem', **encoding):
        typ, result = self.client.append(folder, '\\Seen', None, message(channel, envelope, **encoding))
        if typ != 'OK':
            raise RuntimeError(result)
        code = self.client.response('APPENDUID')[1][0]
        return int(code.split()[-1])

    def fetch(self, uid):
        typ, result = self.client.uid('FETCH', str(uid), '(BODY.PEEK[])')
        if typ != 'OK':
            raise RuntimeError(result)
        return decode_message(next(x[1] for x in result if isinstance(x, tuple)))

    def uids(self):
        typ, result = self.client.uid('SEARCH', None, 'ALL')
        if typ != 'OK':
            raise RuntimeError(result)
        return {int(x) for x in result[0].split()}

    def new(self, cursor):
        typ, result = self.client.uid('FETCH', f'{cursor+1}:*', '(UID BODY.PEEK[HEADER.FIELDS (Tandem-Channel)])')
        if typ != 'OK':
            raise RuntimeError(result)
        found = {}
        for value in result:
            if isinstance(value, tuple):
                uid = int(re.search(rb'UID (\d+)', value[0])[1])
                if uid > cursor:
                    found[uid] = str(BytesParser(policy=policy.default).parsebytes(value[1])['Tandem-Channel']).strip()
        # Only a completed scan advances the cursor. APPENDUID is not a scan.
        return found, max([cursor, *found])

    def destroy(self, uids):
        if not uids:
            return
        sequence = ','.join(str(x) for x in sorted(uids))
        self.client.uid('STORE', sequence, '+FLAGS.SILENT', '(\\Deleted)')
        typ, result = self.client.uid('EXPUNGE', sequence)
        if typ != 'OK':
            raise RuntimeError(result)


class JMAPHub:
    """The durable outbox's transport interface, backed by a real mailbox."""
    def __init__(self, client, folder):
        self.client, self.folder = client, folder

    def append(self, channel, body):
        return self.client.append(self.folder, channel, body)

    def messages(self):
        return [(mid, channel, body) for mid, (channel, body) in self.client.fetch(self.client.ids(self.folder)).items()]

    def destroy(self, ids):
        self.client.destroy(ids)
