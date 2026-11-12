"""Ephemeral real server: loopback ports, disposable account, no user mailbox."""
import base64
import json
import os
import re
import selectors
import subprocess
import time
import uuid

from mail_transport import JMAP

IMAGE = 'stalwartlabs/stalwart:v1.1.0@sha256:f772aa6ef8cd8018969fa491640f1436b0270eb14e2c219b5f37f1a77020837e'


def docker(*args):
    return subprocess.check_output(['docker', *args], text=True, stderr=subprocess.STDOUT, timeout=60).strip()


def listeners(name, wanted, timeout=45):
    """Read actual readiness events; deadline is a safety net, no polling."""
    p = subprocess.Popen(['docker', 'logs', '-f', name], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(p.stdout, selectors.EVENT_READ)
            pending, data, deadline = set(wanted), b'', time.monotonic() + timeout
            while pending:
                if not selector.select(max(0, deadline-time.monotonic())):
                    raise TimeoutError('Stalwart listener readiness: ' + data[-3000:].decode(errors='replace'))
                chunk = os.read(p.stdout.fileno(), 65536)
                if not chunk:
                    raise RuntimeError('Stalwart exited before readiness')
                data += chunk
                for line in data.splitlines():
                    if b'network.listen-start' in line:
                        match = re.search(rb'localPort[^=]*=\s*(\d+)', re.sub(rb'\x1b\[[0-9;]*m', b'', line))
                        if match:
                            pending.discard(int(match[1]))
    finally:
        p.terminate()
        p.wait(timeout=10)
        p.stdout.close()


class Stalwart:
    def __enter__(self):
        self.name = 'tandem-test-' + uuid.uuid4().hex[:12]
        try:
            docker('run', '-d', '--name', self.name, '--init', '-p', '127.0.0.1::8080', '-p', '127.0.0.1::443',
                   '-p', '127.0.0.1::993', '-e', 'STALWART_RECOVERY_ADMIN=admin:conformance-test-only', IMAGE)
            listeners(self.name, {8080})
            self.inspect()
            origin = 'http://127.0.0.1:' + str(self.ports[8080])
            basic = 'Basic ' + base64.b64encode(b'admin:conformance-test-only').decode()
            bootstrap = JMAP(origin + '/.well-known/jmap', basic, fixture_origin=origin)
            result = bootstrap.request(bootstrap.api, {'using': ['urn:ietf:params:jmap:core', 'urn:stalwart:jmap'],
                'methodCalls': [['x:Bootstrap/set', {'update': {'singleton': {
                    'defaultHostname': 'mail.example.org', 'defaultDomain': 'example.org',
                    'requestTlsCertificate': False, 'generateDkimKeys': False,
                    'tracer': {'@type': 'Stdout', 'ansi': False, 'buffered': False, 'level': 'info', 'enable': True}}}}, '0']]})
            account = result['methodResponses'][0][1]['updated']['singleton']
            self.username, self.password = account['username'], account['secret']
            docker('restart', self.name)
            listeners(self.name, {443, 993})
            self.inspect()  # Docker can assign different ephemeral ports on restart.
            self.origin = 'https://127.0.0.1:' + str(self.ports[443])
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def inspect(self):
        data = json.loads(docker('inspect', self.name))[0]
        self.ports = {int(k.split('/')[0]): int(v[0]['HostPort']) for k, v in data['NetworkSettings']['Ports'].items() if v}

    @property
    def authorization(self):
        return 'Basic ' + base64.b64encode((self.username + ':' + self.password).encode()).decode()

    def __exit__(self, *args):
        docker('rm', '-fv', self.name)
