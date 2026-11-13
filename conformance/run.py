#!/usr/bin/env python3
"""Run isolated conformance tests; --mail opts into an ephemeral real server."""
import argparse
import importlib.util
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import unittest

sys.dont_write_bytecode = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mail', action='store_true', help='start and remove a disposable Stalwart Docker container')
    parser.add_argument('--require-cryptokit', action='store_true', help='fail instead of skipping when Apple CryptoKit is unavailable')
    parser.add_argument('--require-libsodium', action='store_true', help='fail instead of skipping when libsodium is unavailable')
    parser.add_argument('--verbose', action='store_true')
    args = parser.parse_args()
    if importlib.util.find_spec('cryptography') is None:
        parser.error('install conformance/requirements.txt in a virtual environment first')
    available = platform.system() == 'Darwin' and shutil.which('swiftc')
    if args.require_cryptokit and not available:
        parser.error('--require-cryptokit needs macOS and the Swift toolchain')
    os.environ['TANDEM_TEST_MAIL'] = '1' if args.mail else '0'
    os.environ['TANDEM_REQUIRE_LIBSODIUM'] = '1' if args.require_libsodium else '0'
    if args.mail:
        if not shutil.which('docker'):
            parser.error('--mail needs Docker')
        try:
            subprocess.run(['docker', 'info'], check=True, capture_output=True, timeout=10)
        except (subprocess.SubprocessError, OSError):
            parser.error('Docker daemon is unavailable; start it before using --mail')
    suite = unittest.defaultTestLoader.discover(str(Path(__file__).resolve().parent), pattern='test_*.py')
    result = unittest.TextTestRunner(verbosity=2 if args.verbose else 1).run(suite)
    return 0 if result.wasSuccessful() else 1


if __name__ == '__main__':
    raise SystemExit(main())
