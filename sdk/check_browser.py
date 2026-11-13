"""Foreground real-browser tests against a disposable, loopback-only JMAP account."""
import base64
import json
import os
from pathlib import Path
import subprocess
import ssl
import sys

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'conformance'))
from stalwart import Stalwart
from mail_transport import JMAP

with Stalwart(browser=True) as server:
    admin = JMAP(server.origin + '/.well-known/jmap', server.authorization, context=ssl._create_unverified_context(), fixture_origin=server.origin)
    configured = admin.request(admin.api, {'using': ['urn:ietf:params:jmap:core', 'urn:stalwart:jmap'],
        'methodCalls': [['x:Http/set', {'update': {'singleton': {'usePermissiveCors': True}}}, 'cors']]})
    if not configured['methodResponses'][0][1].get('updated'):
        raise RuntimeError('could not enable CORS on disposable browser fixture')
    authorization = 'Basic ' + base64.b64encode((server.username + ':' + server.password).encode()).decode()
    config = dict(sessionUrl=server.origin + '/.well-known/jmap', authorization=authorization)
    environment = dict(os.environ, TANDEM_JMAP_FIXTURE=json.dumps(config))
    subprocess.run(['npm', 'run', 'test:browser'], cwd=ROOT / 'sdk/typescript', env=environment, check=True)
