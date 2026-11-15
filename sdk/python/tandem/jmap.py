"""HTTPS JMAP transport with authenticated SSE; no polling or new dependencies.

Use JMAPStore.open(config), then Session(store). IMAP defaults are unaffected.
"""
import asyncio
import base64
from dataclasses import dataclass
from email import policy
from email.message import EmailMessage
from email.utils import formatdate, make_msgid
import http.client
import json
import math
import socket
import ssl
import threading
import urllib.parse

from .core import parse_json
from .crypto import unb32
from .session import ChangeBatch, Message

CORE = 'urn:ietf:params:jmap:core'
MAIL = 'urn:ietf:params:jmap:mail'
FASTMAIL_SESSION = 'https://api.fastmail.com/jmap/session'


class JMAPError(Exception):
    def __init__(self, kind):
        self.kind = kind
        super().__init__('JMAP: ' + kind)


@dataclass(frozen=True, slots=True)
class JMAPConfig:
    session_url: str
    authorization: str
    account_id: str | None = None
    folder_name: str = 'Tandem'
    trusted_origins: tuple[str, ...] = ()
    fastmail: bool = False
    page_size: int = 128
    max_response_bytes: int = 16 * 1024 * 1024
    max_known_messages: int = 1_000_000
    max_event_bytes: int = 128 * 1024
    max_batch_bytes: int = 64 * 1024 * 1024
    command_timeout: float = 30
    retry_min: float = 1
    retry_max: float = 30
    tls_context: ssl.SSLContext | None = None

    @classmethod
    def for_fastmail(cls, token: str, **options):
        if not token or any(c.isspace() for c in token):
            raise ValueError('a bare Fastmail JMAP API token is required')
        return cls(FASTMAIL_SESSION, 'Bearer ' + token, fastmail=True, **options)


def _api_json(data):
    # JMAP extensions may contain fractional numbers. The signed Tandem payload
    # inside bodyValues still goes through the protocol's stricter JSON reader.
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate JMAP JSON name')
            result[key] = value
        return result
    def constant(value):
        raise ValueError('invalid JMAP number')
    result = json.loads(data.decode('utf-8'), object_pairs_hook=pairs, parse_constant=constant)
    def valid(value, depth=0):
        if depth > 64:
            raise ValueError('JMAP JSON nesting limit')
        if isinstance(value, str):
            value.encode('utf-8')
        elif isinstance(value, float) and not math.isfinite(value):
            raise ValueError('JMAP JSON number overflow')
        elif isinstance(value, dict):
            for key, item in value.items():
                key.encode('utf-8')
                valid(item, depth + 1)
        elif isinstance(value, list):
            for item in value:
                valid(item, depth + 1)
    valid(result)
    return result


def _origin(value):
    url = urllib.parse.urlsplit(value)
    if url.scheme != 'https' or not url.hostname or url.username is not None or url.password is not None or url.fragment:
        raise ValueError('untrusted JMAP URL')
    return url.hostname.lower(), url.port or 443


def _valid_envelope(body):
    return len(body) >= 54 and body[:5] == b'TNDM\x02' and (body[5] in (1, 3) or body[5] == 2 and len(body) >= 86)


class JMAPStore:
    def __init__(self, config: JMAPConfig):
        self.config = config
        for value in (config.page_size, config.max_response_bytes, config.max_known_messages, config.max_event_bytes, config.max_batch_bytes):
            if type(value) is not int or value < 1:
                raise ValueError('invalid JMAP limit')
        if not all(math.isfinite(v) for v in (config.command_timeout, config.retry_min, config.retry_max)) or config.command_timeout <= 0 or config.retry_min <= 0 or config.retry_max < config.retry_min or not config.folder_name or any(c in config.folder_name for c in '\r\n\0'):
            raise ValueError('invalid JMAP configuration')
        if not config.authorization.startswith(('Bearer ', 'Basic ')) or any(c in config.authorization for c in '\r\n'):
            raise ValueError('invalid JMAP authorization')
        if config.fastmail and (config.session_url != FASTMAIL_SESSION or not config.authorization.startswith('Bearer ') or not config.authorization[7:] or any(c.isspace() for c in config.authorization[7:])):
            raise ValueError('invalid Fastmail configuration')
        self._origins = {_origin(value) for value in (config.session_url, *config.trusted_origins)}
        self._active, self._lock = set(), threading.Lock()
        self._closed = self._watching = False
        self._task = None
        self._wakes = asyncio.Queue(1)
        self._page = config.page_size
        self._delete_size = config.page_size

    @classmethod
    async def open(cls, config: JMAPConfig):
        store = cls(config)
        try:
            await store._connect()
            return store
        except BaseException:
            await store.close()
            raise

    def _trusted(self, value):
        origin = _origin(value)
        fastmail = self.config.fastmail and origin[1] == 443 and (origin[0] == 'api.fastmail.com' or origin[0].endswith('.api.fastmail.com'))
        if origin not in self._origins and not fastmail:
            raise ValueError('untrusted JMAP origin')
        return urllib.parse.urlsplit(value)

    def _finish(self, connection):
        with self._lock:
            self._active.discard(connection)
        connection.close()

    def _blocking(self, url, method='GET', data=None, content_type=None, stream=False):
        discovery = url == self.config.session_url and method == 'GET'
        for _ in range(6):
            target = self._trusted(url)
            connection = http.client.HTTPSConnection(target.hostname, target.port or 443, context=self.config.tls_context or ssl.create_default_context(), timeout=self.config.command_timeout)
            with self._lock:
                if self._closed:
                    raise ConnectionError('JMAP store closed')
                self._active.add(connection)
            keep = False
            try:
                headers = {'Authorization': self.config.authorization}
                if content_type:
                    headers['Content-Type'] = content_type
                if stream:
                    headers['Accept'] = 'text/event-stream'
                connection.request(method, urllib.parse.urlunsplit(('', '', target.path or '/', target.query, '')), data, headers)
                response = connection.getresponse()
                if response.status in (301, 302, 303, 307, 308) and discovery:
                    location = response.getheader('Location')
                    if not location:
                        raise JMAPError('redirect has no location')
                    url = urllib.parse.urljoin(url, location)
                    self._trusted(url)
                    continue
                if not 200 <= response.status < 300:
                    raise JMAPError('HTTP ' + str(response.status))
                if stream:
                    if not response.getheader('Content-Type', '').lower().startswith('text/event-stream'):
                        raise JMAPError('not an event stream')
                    if connection.sock:
                        connection.sock.settimeout(None)
                    keep = True
                    return connection, response
                body = response.read(self.config.max_response_bytes + 1)
                if len(body) > self.config.max_response_bytes:
                    raise ValueError('JMAP response limit exceeded')
                result = _api_json(body)
                if not isinstance(result, dict):
                    raise ValueError('invalid JMAP response')
                return result
            finally:
                if not keep:
                    self._finish(connection)
        raise JMAPError('too many discovery redirects')

    async def _json(self, url, method='GET', data=None, content_type=None):
        return await asyncio.to_thread(self._blocking, url, method, data, content_type)

    async def _call(self, method, **args):
        request = {'using': [CORE, MAIL], 'methodCalls': [[method, {'accountId': self._account, **args}, '0']]}
        response = await self._json(self._api, 'POST', json.dumps(request, ensure_ascii=False).encode(), 'application/json')
        rows = response.get('methodResponses')
        if not isinstance(rows, list) or len(rows) != 1 or len(rows[0]) != 3 or rows[0][2] != '0':
            raise ValueError('JMAP response mismatch')
        name, result, _ = rows[0]
        if name == 'error':
            raise JMAPError(result['type'])
        if name != method or not isinstance(result, dict):
            raise ValueError('JMAP response method mismatch')
        if any(result.get(key) for key in ('notCreated', 'notUpdated', 'notDestroyed')):
            raise JMAPError(method + ' refused')
        return result

    async def _connect(self):
        session = await self._json(self.config.session_url)
        if MAIL not in session['capabilities']:
            raise JMAPError('mail capability missing')
        self._account = self.config.account_id or session['primaryAccounts'][MAIL]
        if MAIL not in session['accounts'][self._account]['accountCapabilities']:
            raise JMAPError('mail account missing')
        self._api, self._upload, self._events = session['apiUrl'], session['uploadUrl'], session['eventSourceUrl']
        self._trusted(self._api)
        capability = session['capabilities'][CORE]
        for key in ('maxObjectsInGet', 'maxObjectsInSet'):
            if type(capability.get(key)) is not int or capability[key] < 1:
                raise ValueError('invalid JMAP capability limit')
        self._page = min(self._page, capability['maxObjectsInGet'])
        self._delete_size = min(self._page, capability['maxObjectsInSet'])
        ids = (await self._call('Mailbox/query', filter={'name': self.config.folder_name}))['ids']
        boxes = (await self._call('Mailbox/get', ids=ids))['list'] if ids else []
        found = [box for box in boxes if box['name'] == self.config.folder_name and box.get('parentId') is None]
        if len(found) > 1:
            raise ValueError('ambiguous JMAP root mailbox')
        self._folder = found[0]['id'] if found else (await self._call('Mailbox/set', create={'tandem': {'name': self.config.folder_name, 'parentId': None}}))['created']['tandem']['id']

    async def _subscribe(self):
        url = self._events
        for key, value in (('types', 'Email'), ('closeafter', 'no'), ('ping', '60')):
            url = url.replace('{' + key + '}', value)
        if '{' in url or '}' in url:
            raise ValueError('unsupported JMAP event URL template')
        return await asyncio.to_thread(self._blocking, url, 'GET', None, None, True)

    def _wake(self, value=None):
        if self._closed:
            return
        if isinstance(value, Exception) and self._wakes.full():
            self._wakes.get_nowait()
        if not self._wakes.full():
            self._wakes.put_nowait(value)

    def _read_events(self, response, loop):
        line, data, event, size, cr, first = bytearray(), [], '', 0, False, True

        def consume():
            nonlocal event, data, size, first
            text = line.decode('utf-8-sig' if first else 'utf-8')
            first = False
            size += len(line) + 1
            line.clear()
            if size > self.config.max_event_bytes:
                raise ValueError('JMAP event limit exceeded')
            if not text:
                if event == 'state' and data:
                    payload = _api_json('\n'.join(data).encode())
                    if 'Email' in payload.get('changed', {}).get(self._account, {}):
                        loop.call_soon_threadsafe(self._wake)
                event, data, size = '', [], 0
            elif text.startswith('event:'):
                event = text[6:].removeprefix(' ')
            elif text.startswith('data:'):
                data.append(text[5:].removeprefix(' '))

        while not self._closed:
            chunk = response.read1(4096)
            if not chunk:
                raise ConnectionError('JMAP push ended')
            for byte in chunk:
                if cr:
                    cr = False
                    if byte == 10:
                        continue
                if byte in (10, 13):
                    consume()
                    cr = byte == 13
                else:
                    if len(line) >= self.config.max_event_bytes:
                        raise ValueError('JMAP push line limit exceeded')
                    line.append(byte)

    async def _pump(self, stream):
        delay, loop = self.config.retry_min, asyncio.get_running_loop()
        try:
            while not self._closed:
                connection, response = stream
                try:
                    await asyncio.to_thread(self._read_events, response, loop)
                except (OSError, http.client.HTTPException):
                    if self._closed:
                        break
                finally:
                    self._finish(connection)
                while not self._closed:
                    await asyncio.sleep(delay)  # allowed-timer: failed-connection backoff only
                    try:
                        stream = await self._subscribe()
                        delay = self.config.retry_min
                        self._wake()
                        break
                    except (OSError, http.client.HTTPException):
                        delay = min(delay * 2, self.config.retry_max)
        except asyncio.CancelledError:
            raise
        except Exception as error:
            self._wake(error)

    async def watch(self):
        if self._watching or self._closed:
            raise RuntimeError('JMAP watch already started or closed')
        self._watching = True
        stream = await self._subscribe()
        self._task = asyncio.create_task(self._pump(stream))

        async def events():
            while not self._closed:
                try:
                    item = await self._wakes.get()
                except asyncio.QueueShutDown:
                    return
                if isinstance(item, Exception):
                    raise item
                yield None
        return events()

    async def _objects(self, ids, bodies=True):
        if not ids:
            return [], []
        properties = ['id', 'mailboxIds'] + (['bodyValues', 'textBody', 'header:Tandem-Channel:asText:all', 'header:Tandem-Version:asText:all'] if bodies else [])
        result = await self._call('Email/get', ids=list(ids), properties=properties, fetchTextBodyValues=bodies, maxBodyValueBytes=self.config.max_response_bytes)
        return result['list'], result.get('notFound', [])

    def _message(self, row):
        if not row['mailboxIds'].get(self._folder) or row.get('header:Tandem-Version:asText:all') != ['2']:
            return None
        tags = row.get('header:Tandem-Channel:asText:all', [])
        if len(tags) != 1:
            return None
        tag = tags[0].strip()
        try:
            if len(unb32(tag)) != 20:
                return None
        except ValueError:
            return None
        values = list(row['bodyValues'].values())
        if any(value.get('isTruncated') for value in values):
            raise ValueError('JMAP body truncated; cursor unchanged')
        if len(values) != 1 or values[0].get('isEncodingProblem'):
            return None
        compact = values[0]['value'].translate(str.maketrans('', '', '\t\n\v\f\r '))
        try:
            body = base64.b64decode(compact, validate=True)
        except ValueError:
            return None
        if base64.b64encode(body).decode() != compact or not _valid_envelope(body):
            return None
        return Message(row['id'], tag, body)

    async def changes(self, encoded):
        cursor = dict(v=1, account=self._account, folder=self._folder, state='', known=[])
        if encoded:
            cursor = parse_json(encoded.encode())
            if type(cursor.get('v')) is not int or cursor.get('v') != 1 or cursor.get('account') != self._account or cursor.get('folder') != self._folder or not isinstance(cursor.get('state'), str) or not cursor['state']:
                raise ValueError('JMAP cursor belongs to another store')
            if not isinstance(cursor.get('known'), list) or any(not isinstance(key, str) or not key for key in cursor['known']) or len(cursor['known']) != len(set(cursor['known'])) or len(cursor['known']) > self.config.max_known_messages:
                raise ValueError('invalid JMAP cursor IDs')
        known, added, removed, batch_bytes = set(cursor['known']), {}, set(), 0

        def collect(row):
            nonlocal batch_bytes
            key = row['id']
            if not row['mailboxIds'].get(self._folder):
                known.discard(key)
                added.pop(key, None)
                removed.add(key)
                return
            known.add(key)
            value = self._message(row)
            if value:
                batch_bytes += len(value.body) - (len(added[key].body) if key in added else 0)
                if batch_bytes > self.config.max_batch_bytes:
                    raise ValueError('JMAP batch byte limit exceeded')
                added[key] = value

        async def full_scan():
            nonlocal known, added, batch_bytes
            cursor['state'] = (await self._call('Email/get', ids=[], properties=['id']))['state']
            ids, query_state, seen = [], None, set()
            while True:
                result = await self._call('Email/query', filter={'inMailbox': self._folder}, sort=[{'property': 'receivedAt', 'isAscending': True}], position=len(ids), limit=self._page, calculateTotal=True)
                if query_state is not None and result['queryState'] != query_state:
                    raise JMAPError('query changed during full scan; resume after push')
                query_state = result['queryState']
                for key in result['ids']:
                    if key in seen:
                        raise ValueError('JMAP repeated query ID')
                    seen.add(key)
                    ids.append(key)
                if len(ids) > self.config.max_known_messages or type(result.get('total')) is not int or result['total'] < len(ids):
                    raise ValueError('invalid JMAP query total or message limit exceeded')
                if len(ids) == result['total']:
                    break
                if not result['ids']:
                    raise ValueError('JMAP query made no progress')
            removed.update(known - seen)
            known, added, batch_bytes = seen, {}, 0
            for at in range(0, len(ids), self._page):
                rows, missing = await self._objects(ids[at:at + self._page])
                known.difference_update(missing)
                removed.update(missing)
                for row in rows:
                    collect(row)

        if not encoded:
            await full_scan()
        reset = False
        while True:
            try:
                result = await self._call('Email/changes', sinceState=cursor['state'], maxChanges=self._page)
            except JMAPError as error:
                if error.kind != 'cannotCalculateChanges' or reset:
                    raise
                reset = True
                await full_scan()
                continue
            if not result.get('newState') or result['hasMoreChanges'] and result['newState'] == cursor['state']:
                raise ValueError('JMAP changes made no progress')
            destroyed = set(result['destroyed'])
            removed.update(known & destroyed)
            known.difference_update(destroyed)
            for key in destroyed:
                added.pop(key, None)
            ids = sorted(set(result['created'] + result['updated']))
            for at in range(0, len(ids), self._page):
                rows, missing = await self._objects(ids[at:at + self._page], False)
                for key in missing:
                    known.discard(key)
                    added.pop(key, None)
                    removed.add(key)
                bodies = []
                for row in rows:
                    key = row['id']
                    if key in destroyed:
                        continue
                    if row['mailboxIds'].get(self._folder):
                        known.add(key)
                        bodies.append(key)
                    elif key in known:
                        known.remove(key)
                        added.pop(key, None)
                        removed.add(key)
                rows, missing = await self._objects(bodies)
                for key in missing:
                    known.discard(key)
                    added.pop(key, None)
                    removed.add(key)
                for row in rows:
                    collect(row)
            if len(known) > self.config.max_known_messages:
                raise ValueError('JMAP message limit exceeded')
            cursor['state'] = result['newState']
            if not result['hasMoreChanges']:
                break
        cursor['known'] = sorted(known)
        return ChangeBatch(tuple(added[key] for key in sorted(added)), tuple(sorted(removed - known)), json.dumps(cursor, separators=(',', ':')))

    async def append(self, channel, body):
        if len(unb32(channel)) != 20 or not _valid_envelope(body):
            raise ValueError('invalid Tandem channel or envelope')
        message = EmailMessage(policy=policy.SMTP)
        for key, value in [('From', 'Tandem <tandem@tandem.airforce>'), ('Date', formatdate(usegmt=True)), ('Message-ID', make_msgid(domain='tandem.invalid')), ('Subject', 'tandem'), ('Tandem-Version', '2'), ('Tandem-Channel', channel)]:
            message[key] = value
        encoded = base64.b64encode(body).decode()
        message.set_content('\r\n'.join(encoded[at:at + 76] for at in range(0, len(encoded), 76)) + '\r\n', subtype='plain', charset='us-ascii', cte='7bit')
        return await self.append_prepared(message.as_bytes())

    async def append_prepared(self, wire):
        if len(wire) > self.config.max_response_bytes:
            raise ValueError('JMAP message limit exceeded')
        url = self._upload.replace('{accountId}', urllib.parse.quote(self._account, safe=''))
        if '{' in url or '}' in url:
            raise ValueError('unsupported JMAP upload template')
        uploaded = await self._json(url, 'POST', bytes(wire), 'message/rfc822')
        return (await self._call('Email/import', emails={'tandem': {'blobId': uploaded['blobId'], 'mailboxIds': {self._folder: True}, 'keywords': {'$seen': True}}}))['created']['tandem']['id']

    async def fetch(self, ids):
        result = []
        for at in range(0, len(ids), self._page):
            rows, _ = await self._objects(ids[at:at + self._page])
            result.extend(value for row in rows if (value := self._message(row)))
        return result

    async def destroy(self, ids):
        for at in range(0, len(ids), self._delete_size):
            rows, _ = await self._objects(ids[at:at + self._delete_size], False)
            if any(not row['mailboxIds'].get(self._folder) for row in rows):
                raise ValueError('refusing destruction outside JMAP replica mailbox')
            if rows:
                await self._call('Email/set', destroy=[row['id'] for row in rows])

    async def close(self):
        if self._closed:
            return
        self._closed = True
        with self._lock:
            active = list(self._active)
        for connection in active:
            try:
                if connection.sock:
                    connection.sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        if self._task:
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)
        for connection in active:
            self._finish(connection)
        self._wakes.shutdown(immediate=True)
