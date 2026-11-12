"""Bootstrap and provisioning of the Stalwart hub server.

Stalwart 1.x starts in a setup wizard unless a configuration exists. The
testbed skips it the documented way: write config.json, start once in recovery
mode with a temporary administrator, configure the server and create a
permanent provisioning administrator over the management API, then start
normally. Hub accounts are then created per test through that administrator.
"""

import json
import os
import pathlib
import re
import secrets
import subprocess
import time
import urllib.request

from .jmap import CORE, STALWART, Jmap

HERE = pathlib.Path(__file__).resolve().parent.parent          # testbed/
COMPOSE = HERE / "compose.yaml"
STATE_DIR = HERE / ".state"
STATE = STATE_DIR / "state.json"
VOLUMES = ("tandem-testbed-etc", "tandem-testbed-data")
DOMAIN = "tandem.test"
BOOTSTRAP_NAME = "tandem-testbed-bootstrap"


def image():
    """The pinned Stalwart image, read from compose.yaml so there is one source."""
    match = re.search(r"image:\s*(stalwartlabs/stalwart:\S+)", COMPOSE.read_text())
    return match.group(1)


def env(name, default):
    return os.environ.get(name, default)


def jmap_base():
    host = env("TB_HOST", "127.0.0.1")
    return f"http://{host}:{env('TB_JMAP_PORT', '8180')}"


def imap_endpoint():
    return env("TB_HOST", "127.0.0.1"), int(env("TB_IMAP_PORT", "1143"))


def run(*args, capture=True, check=True, **kw):
    return subprocess.run(args, check=check, text=True,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None, **kw)


def compose(*args, capture=True, check=True):
    return run("docker", "compose", "-f", str(COMPOSE), *args, capture=capture, check=check)


def load_state():
    return json.loads(STATE.read_text()) if STATE.exists() else None


def save_state(state):
    STATE_DIR.mkdir(exist_ok=True)
    STATE.write_text(json.dumps(state, indent=2))
    STATE.chmod(0o600)


def wait_http(url, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2):
                return
        except Exception:
            time.sleep(0.5)
    raise TimeoutError(f"{url} not reachable after {timeout}s")


def strong_password():
    return secrets.token_urlsafe(24)


def _mgmt(client, method, args):
    return client.call(method, args, using=(CORE, STALWART))


def _account(name, domain_id, password, role="User"):
    return {"@type": "User", "name": name, "domainId": domain_id,
            "credentials": {"0": {"@type": "Password", "secret": password}},
            "aliases": {}, "memberGroupIds": {}, "quotas": {},
            "roles": {"@type": role}, "permissions": {"@type": "Inherit"},
            "encryptionAtRest": {"@type": "Disabled"}}


def _created(result, key):
    if key not in result.get("created", {}):
        raise RuntimeError(f"not created: {json.dumps(result.get('notCreated', result))}")
    return result["created"][key]["id"]


def bootstrap(log=print):
    """One-time server setup. Idempotent: does nothing if already done."""
    if load_state():
        return load_state()
    img = image()
    compose("down", check=False)
    for vol in VOLUMES:
        run("docker", "volume", "rm", "-f", vol, check=False)
        run("docker", "volume", "create", vol)
    config = json.dumps({"@type": "RocksDb", "path": "/var/lib/stalwart/"})
    run("docker", "run", "--rm", "--entrypoint", "sh", "-e", f"CFG={config}",
        "-v", f"{VOLUMES[0]}:/etc/stalwart", img, "-c", 'printf "%s" "$CFG" > /etc/stalwart/config.json')

    recovery_password = strong_password()
    port = int(env("TB_BOOTSTRAP_PORT", "18999"))
    run("docker", "rm", "-f", BOOTSTRAP_NAME, check=False)
    run("docker", "run", "-d", "--name", BOOTSTRAP_NAME, "-p", f"127.0.0.1:{port}:8080",
        "-e", "STALWART_RECOVERY_MODE=1", "-e", f"STALWART_RECOVERY_ADMIN=admin:{recovery_password}",
        "-v", f"{VOLUMES[0]}:/etc/stalwart", "-v", f"{VOLUMES[1]}:/var/lib/stalwart", img)
    try:
        base = f"http://127.0.0.1:{port}"
        wait_http(base + "/healthz/live")
        admin = Jmap(base + "/jmap/session", "admin", recovery_password)
        domain_id = _created(_mgmt(admin, "x:Domain/set", {"create": {"d": {
            "name": DOMAIN, "aliases": {}, "certificateManagement": {"@type": "Manual"},
            "dnsManagement": {"@type": "Manual"}, "subAddressing": {"@type": "Enabled"}}}}), "d")
        # Local testbed only: plaintext IMAP LOGIN on 143 and JMAP over HTTP.
        _mgmt(admin, "x:Imap/set", {"update": {"singleton": {"allowPlainTextAuth": True}}})
        _mgmt(admin, "x:SystemSettings/set", {"update": {"singleton": {
            "defaultHostname": "hub." + DOMAIN, "defaultDomainId": domain_id,
            "services/jmap/cleartext": True, "services/imap/cleartext": True}}})
        _mgmt(admin, "x:NetworkListener/set", {"create": {
            "http": {"name": "http", "protocol": "http", "bind": {"[::]:8080": True}},
            "imap": {"name": "imap", "protocol": "imap", "bind": {"[::]:143": True}}}})
        provisioner = strong_password()
        _created(_mgmt(admin, "x:Account/set", {"create": {
            "p": _account("provisioner", domain_id, provisioner, role="Admin")}}), "p")
    finally:
        run("docker", "rm", "-f", BOOTSTRAP_NAME, check=False)
    state = {"image": img, "domain": DOMAIN, "domainId": domain_id,
             "provisioner": {"user": "provisioner", "password": provisioner}, "hubs": {}}
    save_state(state)
    log(f"bootstrapped Stalwart ({img.split('@')[0]})")
    return state


def up(log=print):
    bootstrap(log)
    compose("up", "-d", "--wait", capture=False)
    wait_http(jmap_base() + "/healthz/live")
    log(f"up: JMAP {jmap_base()}/.well-known/jmap, IMAP {':'.join(map(str, imap_endpoint()))}")


def down():
    compose("down", capture=False)


def reset():
    """Remove containers, volumes and state: the next `up` bootstraps from scratch."""
    compose("down", "-v", check=False, capture=False)
    for vol in VOLUMES:
        run("docker", "volume", "rm", "-f", vol, check=False)
    if STATE.exists():
        STATE.unlink()


def provisioner():
    state = load_state()
    if not state:
        raise RuntimeError("testbed not bootstrapped: run `tb up`")
    p = state["provisioner"]
    return Jmap(jmap_base() + "/.well-known/jmap", p["user"], p["password"]), state


def descriptor(name, password, jmap_base_url=None, imap_host_port=None):
    """A testbed hub descriptor: both bindings of one account (DESIGN §13.1,
    with Basic auth for JMAP, which Stalwart accepts)."""
    host, port = imap_host_port or imap_endpoint()
    return {"name": name,
            "jmap": {"kind": "jmap", "session": (jmap_base_url or jmap_base()) + "/.well-known/jmap",
                     "user": name, "password": password},
            "imap": {"kind": "imap", "host": host, "port": port, "tls": False,
                     "user": name, "password": password}}


def new_hub(name=None):
    """Create a fresh, empty hub account and return its descriptor."""
    admin, state = provisioner()
    name = name or "hub-" + secrets.token_hex(4)
    password = strong_password()
    account_id = _created(_mgmt(admin, "x:Account/set", {"create": {
        "a": _account(name, state["domainId"], password)}}), "a")
    state["hubs"][name] = {"id": account_id, "password": password}
    save_state(state)
    return descriptor(name, password)


def hub(name):
    state = load_state() or {}
    entry = state.get("hubs", {}).get(name)
    if not entry:
        raise KeyError(f"unknown hub {name!r}")
    return descriptor(name, entry["password"])


def delete_hub(name):
    admin, state = provisioner()
    entry = state["hubs"].pop(name)
    _mgmt(admin, "x:Account/set", {"destroy": [entry["id"]]})
    save_state(state)


def set_password(name, password=None):
    """Change a hub account's password: the `hub: reject auth` step."""
    admin, state = provisioner()
    entry = state["hubs"][name]
    password = password or strong_password()
    _mgmt(admin, "x:Account/set", {"update": {entry["id"]: {
        "credentials": {"0": {"@type": "Password", "secret": password}}}}})
    entry["password"] = password
    save_state(state)
    return descriptor(name, password)
