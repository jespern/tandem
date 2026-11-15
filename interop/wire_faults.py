"""Event-driven TLS IMAP fault proxy, exclusively for disposable Stalwart.

Both legs are real TLS sockets. IMAP framing is observed solely to choose cuts;
bytes are forwarded unchanged and credentials/bodies are never logged.
"""
import asyncio
from dataclasses import dataclass, field
from pathlib import Path
import re
import ssl
import subprocess
import tempfile
import threading

from harness import Failed

LITERAL = re.compile(rb'\{(\d+)\+?\}\r\n$')


@dataclass
class Connection:
    downstream: object
    upstream: object
    commands: dict = field(default_factory=dict)
    blobs: bool = False
    idle: bool = False
    append_blobs: bool = False

    def abort(self):
        self.downstream.transport.abort()
        self.upstream.transport.abort()


class WireProxy:
    def __init__(self, hub):
        if not hub.get('insecure') or hub['host'] not in ('127.0.0.1', 'localhost'):
            raise Failed('wire faults require a disposable loopback Stalwart fixture')
        self.hub = hub
        self.ready, self.fired = threading.Event(), threading.Event()
        self.connections, self.tasks = [], set()
        self.rule, self.evidence, self.error = None, [], None

    def __enter__(self):
        self.directory = tempfile.TemporaryDirectory(prefix='tandem-wire-')
        root = Path(self.directory.name)
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(root / 'key.pem'),
                        '-out', str(root / 'cert.pem'), '-days', '1', '-subj', '/CN=tandem-test-only'],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        self.tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        self.tls.load_cert_chain(root / 'cert.pem', root / 'key.pem')
        self.thread = threading.Thread(target=self.thread_main, name='tandem-wire')
        self.thread.start()
        if not self.ready.wait(10) or self.error:
            self.__exit__(None, None, None)
            raise Failed(f'wire proxy startup failed: {self.error}')
        return self

    def thread_main(self):
        try:
            asyncio.run(self.serve())
        except BaseException as exc:
            self.error = type(exc).__name__
            self.ready.set()

    async def serve(self):
        self.loop = asyncio.get_running_loop()
        self.stop = asyncio.Event()
        server = await asyncio.start_server(self.accept, '127.0.0.1', 0, ssl=self.tls, limit=1 << 20)
        self.port = server.sockets[0].getsockname()[1]
        self.ready.set()
        try:
            await self.stop.wait()
        finally:
            server.close()
            for c in self.connections:
                c.abort()
            for t in list(self.tasks):
                t.cancel()
            await asyncio.gather(*self.tasks, return_exceptions=True)
            await server.wait_closed()

    async def accept(self, reader, writer):
        task = asyncio.current_task()
        self.tasks.add(task)
        connection = None
        upstream = None
        relays = []
        try:
            remote, upstream = await asyncio.open_connection(self.hub['host'], self.hub['port'], ssl=ssl._create_unverified_context())
            connection = Connection(writer, upstream)
            self.connections.append(connection)
            relays = [asyncio.create_task(self.relay(reader, upstream, connection, True)),
                      asyncio.create_task(self.relay(remote, writer, connection, False))]
            await asyncio.wait(relays, return_when=asyncio.FIRST_COMPLETED)
        except (OSError, asyncio.IncompleteReadError):
            pass
        finally:
            for t in relays:
                t.cancel()
            await asyncio.gather(*relays, return_exceptions=True)
            if connection:
                connection.abort()
                self.connections.remove(connection)
            else:
                writer.close()
                if upstream:
                    upstream.close()
            self.tasks.discard(task)

    def cut(self, c, kind, **proof):
        self.evidence.append(dict(kind=kind, **proof))
        if self.rule.get('repeat', 1) > 1:
            self.rule['repeat'] -= 1
        else:
            self.rule = None
        c.abort()
        self.fired.set()

    async def relay(self, reader, writer, c, client):
        while True:
            line = await reader.readline()
            if not line:
                return
            parts = line.rstrip(b'\r\n').split(b' ', 2)
            if client and len(parts) >= 2:
                tag, verb = parts[0], parts[1].upper()
                if verb in (b'SELECT', b'EXAMINE'):
                    c.blobs = b'-blobs' in line
                if verb in (b'APPEND', b'FETCH', b'UID', b'IDLE', b'SELECT', b'EXAMINE'):
                    c.commands[tag] = verb
                if verb == b'APPEND':
                    c.append_blobs = b'-blobs' in line
                if verb == b'IDLE':
                    c.idle = True
            if client and line == b'DONE\r\n':
                c.idle = False
            if not client and parts[0] in c.commands:
                verb = c.commands.pop(parts[0])
                if self.rule and self.rule['kind'] == 'append_ack' and verb == b'APPEND' and c.append_blobs and parts[1].upper() == b'OK':
                    self.cut(c, 'append_ack', server_accepted=True)
                    return  # The tagged success never reaches the client.
            writer.write(line)
            await writer.drain()
            match = LITERAL.search(line)
            if not match:
                continue
            length, forwarded = int(match[1]), 0
            kind = 'upload' if client else 'download'
            # A FETCH response can contain a short header literal followed by
            # a body literal on a continuation line without the word FETCH.
            eligible = c.append_blobs if client else c.blobs and any(v in (b'UID', b'FETCH') for v in c.commands.values())
            if eligible and self.rule and self.rule['kind'] == kind and length >= self.rule['after'] and self.rule.get('skip', 0):
                self.rule['skip'] -= 1
                eligible = False
            while forwarded < length:
                remaining = length - forwarded
                armed = self.rule and self.rule['kind'] == kind and eligible
                n = min(65536, remaining)
                if armed:
                    n = min(n, max(1, self.rule['after'] - forwarded))
                body = await reader.readexactly(n)
                writer.write(body)
                await writer.drain()
                forwarded += len(body)
                if armed and forwarded >= self.rule['after']:
                    self.cut(c, kind, literal_bytes=length, forwarded_bytes=forwarded)
                    return

    def arm(self, kind, after=8192, skip=0, repeat=1):
        if kind not in ('upload', 'download', 'append_ack', 'idle') or after < 1 or skip < 0 or repeat < 1:
            raise ValueError('invalid wire fault')
        done = threading.Event()
        self.fired.clear()

        def set_rule():
            self.rule = dict(kind=kind, after=after, skip=skip, repeat=repeat)
            if kind == 'idle':
                for c in self.connections:
                    if c.idle:
                        self.cut(c, kind, active_subscription=True)
                        break
            done.set()
        self.loop.call_soon_threadsafe(set_rule)
        if not done.wait(10):
            raise Failed('wire proxy stopped while arming')

    def assert_fired(self):
        if not self.fired.is_set():
            raise Failed('wire fault was never exercised')

    def route(self, new, hub):
        def routed(side, name):
            d = new(side, name)
            d('hub', **dict(hub, host='127.0.0.1', port=self.port, insecure=True))
            return d
        return routed

    def __exit__(self, *args):
        if hasattr(self, 'loop') and self.thread.is_alive():
            self.loop.call_soon_threadsafe(self.stop.set)
        self.thread.join(timeout=15)
        self.directory.cleanup()
        if self.thread.is_alive():
            raise Failed('wire proxy failed to close')
