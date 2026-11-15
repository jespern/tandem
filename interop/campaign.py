"""Replay seeded, multi-device file histories on isolated real IMAP providers."""
import argparse
from contextlib import ExitStack
import itertools
import json
from pathlib import Path
import subprocess
import time

from harness import ENGINES, ROOT
from history import history
from interop import run, Stalwart
from providers import fastmail


def save(path, results, config):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix('.tmp')
    temporary.write_text(json.dumps(dict(date=time.strftime('%Y-%m-%d'), configuration=config, results=results), indent=2) + '\n')
    temporary.replace(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('engines', nargs='*', choices=list(ENGINES))
    parser.add_argument('--provider', choices=['stalwart', 'fastmail'], default='stalwart')
    parser.add_argument('--seeds', type=int, nargs='+', default=list(range(8)))
    parser.add_argument('--steps', type=int, default=24)
    parser.add_argument('--timeout', type=float, default=90)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--no-build', action='store_true')
    parser.add_argument('--report', type=Path, default=ROOT / '.build/file-campaign.json')
    args = parser.parse_args()
    if len(args.engines) > 2 or args.steps < 1 or args.timeout <= 0:
        parser.error('choose up to two engines, positive steps and timeout')
    engines = list(dict.fromkeys(args.engines)) or list(ENGINES)
    pairs = [tuple(args.engines)] if len(args.engines) == 2 else list(itertools.product(engines, repeat=2))
    for engine in engines:
        command = list(ENGINES[engine]['build'])
        if engine == 'go' and args.race:
            binary = str(ROOT / '.build/tandem-device-go-race')
            command[command.index('-o') + 1] = binary
            command.insert(2, '-race')
            ENGINES[engine]['argv'] = [binary]
        if not args.no_build:
            subprocess.run(command, cwd=ENGINES[engine]['cwd'], check=True, stdout=subprocess.DEVNULL)
    results = []
    config = dict(provider=args.provider, pairs=pairs, seeds=args.seeds, steps=args.steps, race=args.race)
    with ExitStack() as stack:
        if args.provider == 'stalwart':
            server = stack.enter_context(Stalwart())
            hub = dict(host='127.0.0.1', port=server.ports[993], user=server.username, password=server.password, insecure=True)
        else:
            hub = fastmail()
        for first, second in pairs:
            for seed in args.seeds:
                results += run(hub, args.provider, first, second, [history(seed, args.steps)], args.timeout)
                save(args.report, results, config)
    failed = sum(not r['ok'] for r in results)
    print(f'{len(results) - failed}/{len(results)} histories passed; report: {args.report}', flush=True)
    return bool(failed)


if __name__ == '__main__':
    raise SystemExit(main())
