"""Run native SDK tests, then verify all 16 sender/receiver combinations.

Run with sdk/python/.venv/bin/python sdk/check.py [--node /path/to/node]
[--oracle /path/to/tandem-oracle]. Dependencies must already be installed.
All processes run in the foreground; test artifacts live in a temporary directory.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def run(command, cwd=ROOT, env=None):
    print("+", " ".join(map(str, command)), flush=True)
    subprocess.run(list(map(str, command)), cwd=cwd, env=env, check=True)


def check_event_sources():
    """No timers in SDK runtime code. A line marked "allowed-timer: <reason>" is exempt:
    the IMAP adapter's IDLE refresh, Gmail's IDLE restart and reconnect backoff (§10.3)."""
    forbidden = re.compile(r"\b(?:setInterval|setTimeout|NewTicker|NewTimer|sleep|usleep)\s*\(")
    sources = []
    for folder, pattern in (("sdk/go", "*.go"), ("sdk/python/tandem", "*.py"),
                            ("sdk/typescript/src", "*.ts"), ("sdk/swift/Sources", "*.swift")):
        sources.extend((ROOT / folder).rglob(pattern))
    for path in sources:
        if path.name.endswith("_test.go"):
            continue
        for line in path.read_text().splitlines():
            if forbidden.search(line) and "allowed-timer:" not in line:
                raise SystemExit(f"timer-triggered work in {path.relative_to(ROOT)}")


def check_lean(oracle):
    sys.path.insert(0, str(ROOT / "sdk/python"))
    import tandem as t
    v = json.loads((ROOT / "vectors/v2/core.json").read_text())
    m = {k: bytes.fromhex(value) for k, value in v["material"].items()}
    rows, entries = [], []
    def dot(d):
        return t.Dot(bytes.fromhex(d["device"]), d["millis"], d["counter"])
    def vector(ds):
        return [[d["device"], d["millis"] * 65536 + d["counter"]] for d in ds]
    for row in v["writes"]:
        w = dict(row["write"])
        rows.append(dict(id=row["id"], author=w["author"], ref=v["material"]["epoch"], record=w["record"], field=w["field"],
                         op={1: "assign", 2: "add", 3: "remove", 4: "delete"}[w["op"]], elem=w["element"] or "", value=w["value"],
                         dev=w["dot"]["device"], hlc=w["dot"]["millis"] * 65536 + w["dot"]["counter"],
                         ctx=vector(w["context"]), base=vector(w["base"]), auth=""))
        for key in ("hub", "scope", "record", "author", "certificate", "authorization", "ref", "signature", "value"):
            w[key] = bytes.fromhex(w[key])
        w["element"] = bytes.fromhex(w["element"]) if w["element"] is not None else None
        w["op"], w["dot"] = t.Op(w["op"]), dot(w["dot"])
        w["context"], w["base"] = tuple(map(dot, w["context"])), tuple(map(dot, w["base"]))
        entries.append(t.Entry(t.Write(**w), m["epoch"], 0))
    request = dict(genesis=v["material"]["epoch"], statements=[dict(id=v["material"]["epoch"], parent=None,
                   signer=v["material"]["uid"], n=0, epoch=0, commit="K0", snapshot=None, deleted=False,
                   members=[dict(uid=v["material"]["uid"], role="owner", name="", floor="")])], writes=rows)
    output = subprocess.run([str(oracle)], input=json.dumps(request) + "\n", text=True, capture_output=True, check=True)
    result = json.loads(output.stdout)
    if "error" in result:
        raise SystemExit(f"Lean oracle: {result['error']}")
    expected = {(a.id.hex(), b.id.hex()) for a in entries for b in entries if t.supersedes(a, b)}
    actual = {tuple(pair) for pair in result["supersedes"]}
    if actual != expected:
        raise SystemExit(f"Lean supersession disagreement: {actual ^ expected}")
    print(f"Lean agrees on all {len(entries)**2} ordered write pairs.", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--node", default="node")
    parser.add_argument("--oracle", type=Path)
    args = parser.parse_args()
    node = shutil.which(args.node)
    if node is None:
        raise SystemExit("Node.js 26 or later is required")
    version = subprocess.check_output([node, "-p", "process.versions.node"], text=True).strip()
    if int(version.split(".")[0]) < 26:
        raise SystemExit(f"Node.js {version} is installed; use Node.js 26 or later (--node PATH)")
    check_event_sources()
    run([sys.executable, "-B", "vectors/v2/generate.py", "--check"])
    run([sys.executable, "-B", "vectors/v2/generate_control.py", "--check"])
    if args.oracle:
        check_lean(args.oracle.resolve())
    with tempfile.TemporaryDirectory(prefix="tandem-sdk-interop-") as directory:
        env = dict(os.environ, TANDEM_INTEROP_DIR=directory)
        # The race detector multiplies memory; two packages stretching 1 GiB at once under it ran out the CI clock.
        # It looks for data races, which hashing doesn't have: the 1 GiB tests run in the second pass, without it.
        run(["go", "test", "-race", "-count=1", "./..."], ROOT / "sdk/go", dict(env, TANDEM_SKIP_ARGON2_GIB="1"))
        run(["go", "test", "-count=1", "./..."], ROOT / "sdk/go", env)
        run([sys.executable, "-B", "-W", "error::ResourceWarning", "-m", "unittest", "discover", "-s", "sdk/python/tests", "-q"], env=env)
        run([node, "node_modules/typescript/bin/tsc"], ROOT / "sdk/typescript", env)
        run([node, "--test", "test/core.test.mjs", "test/control.test.mjs"], ROOT / "sdk/typescript", env)
        # TandemArgon2 is built optimized in every configuration, so the 1 GiB tests take seconds in a debug build too.
        run(["swift", "test", "--quiet"], ROOT, env)
        env["TANDEM_VERIFY_INTEROP"] = "1"
        run(["go", "test", "-count=1", "-run", "TestCrossSDK", "./..."], ROOT / "sdk/go", env)
        run([sys.executable, "-B", "-m", "unittest", "discover", "-s", "sdk/python/tests", "-k", "cross_sdk", "-q"], env=env)
        run([node, "--test", "test/interop.test.mjs"], ROOT / "sdk/typescript", env)
        run(["swift", "test", "--quiet", "--filter", "CoreTests/testCrossSDK"], ROOT, env)
    print("All four SDKs verify every sender's signature and decrypt every sender's inbox envelope.")


if __name__ == "__main__":
    main()
