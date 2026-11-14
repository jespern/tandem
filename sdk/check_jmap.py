"""Real native JMAP transports against a disposable, loopback-only Stalwart."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'conformance'))
from stalwart import Stalwart

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--sdk', choices=['go', 'swift', 'python', 'all'], default='all')
args = parser.parse_args()
with Stalwart(browser=True) as server:
    config = dict(sessionUrl=server.origin + '/.well-known/jmap', authorization='Basic ' + base64.b64encode((server.username + ':' + server.password).encode()).decode())
    environment = dict(os.environ, TANDEM_JMAP_FIXTURE=json.dumps(config))
    if args.sdk in ('go', 'all'):
        subprocess.run(['go', 'test', '-race', '-count=1', './jmap'], cwd=ROOT / 'sdk/go', env=environment, check=True)
    if args.sdk in ('swift', 'all'):
        subprocess.run(['swift', 'test', '--filter', 'JMAPTests'], cwd=ROOT, env=environment, check=True)
    if args.sdk in ('python', 'all'):
        environment['PYTHONPATH'] = str(ROOT / 'sdk/python')
        subprocess.run([sys.executable, '-B', '-m', 'unittest', 'discover', '-s', 'sdk/python/tests', '-p', 'test_jmap.py', '-q'], cwd=ROOT, env=environment, check=True)
