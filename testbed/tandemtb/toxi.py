"""Network faults through Toxiproxy (https://github.com/Shopify/toxiproxy).

Each simulated device gets its own pair of proxies (JMAP and IMAP) so one
device can be partitioned, slowed or cut mid-stream while others are not.
"""

import json
import os
import urllib.error
import urllib.request

from . import stalwart

PORT_RANGE = os.environ.get("TB_PROXY_RANGE", "21000-21099")
UPSTREAM = {"jmap": "stalwart:8080", "imap": "stalwart:143"}


def api():
    return f"http://{os.environ.get('TB_HOST', '127.0.0.1')}:{os.environ.get('TB_TOXI_PORT', '8474')}"


def _call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(api() + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"toxiproxy {method} {path}: {e.code} {e.read().decode()}") from None


def proxies():
    return _call("GET", "/proxies")


def _free_ports(count):
    lo, hi = (int(x) for x in PORT_RANGE.split("-"))
    used = {int(p["listen"].rsplit(":", 1)[1]) for p in proxies().values()}
    free = [p for p in range(lo, hi + 1) if p not in used]
    if len(free) < count:
        raise RuntimeError(f"no free proxy ports left in {PORT_RANGE}")
    return free[:count]


def device(name):
    """Create (or reuse) the JMAP and IMAP proxies for a device; returns their host ports."""
    existing = proxies()
    ports = {}
    wanted = [b for b in UPSTREAM if f"{name}-{b}" not in existing]
    free = iter(_free_ports(len(wanted)))
    for binding, upstream in UPSTREAM.items():
        pname = f"{name}-{binding}"
        if pname in existing:
            ports[binding] = int(existing[pname]["listen"].rsplit(":", 1)[1])
            continue
        port = next(free)
        _call("POST", "/proxies", {"name": pname, "listen": f"0.0.0.0:{port}", "upstream": upstream, "enabled": True})
        ports[binding] = port
    return ports


def device_descriptor(device_name, hub_name):
    """The hub descriptor a device uses: the hub's account, reached through this device's proxies."""
    ports = device(device_name)
    entry = stalwart.hub(hub_name)
    host = os.environ.get("TB_HOST", "127.0.0.1")
    d = stalwart.descriptor(hub_name, entry["jmap"]["password"],
                            jmap_base_url=f"http://{host}:{ports['jmap']}",
                            imap_host_port=(host, ports["imap"]))
    d["device"] = device_name
    return d


def _each(device_name, bindings):
    return [f"{device_name}-{b}" for b in (bindings or UPSTREAM)]


def partition(device_name, bindings=None):
    """The device can't reach the hub: new connections are refused, open ones dropped."""
    for p in _each(device_name, bindings):
        _call("POST", f"/proxies/{p}", {"enabled": False})


def heal(device_name, bindings=None):
    """Undo partition and remove every toxic."""
    for p in _each(device_name, bindings):
        _call("POST", f"/proxies/{p}", {"enabled": True})
        for t in _call("GET", f"/proxies/{p}/toxics") or []:
            _call("DELETE", f"/proxies/{p}/toxics/{t['name']}")


def toxic(device_name, kind, attributes, stream="downstream", bindings=None, name=None):
    for p in _each(device_name, bindings):
        _call("POST", f"/proxies/{p}/toxics", {"name": name or f"{kind}-{stream}", "type": kind,
                                               "stream": stream, "toxicity": 1.0, "attributes": attributes})


def latency(device_name, ms, jitter=0, bindings=None):
    toxic(device_name, "latency", {"latency": ms, "jitter": jitter}, bindings=bindings)


def cut_after(device_name, nbytes, stream="downstream", bindings=None):
    """Close the connection after `nbytes` have passed: a response lost mid-flight."""
    toxic(device_name, "limit_data", {"bytes": nbytes}, stream=stream, bindings=bindings)


def reset_peer(device_name, timeout_ms=0, bindings=None):
    """TCP RST after `timeout_ms`: an abrupt drop, as a NAT or a radio switch causes."""
    toxic(device_name, "reset_peer", {"timeout": timeout_ms}, bindings=bindings)


def blackhole(device_name, bindings=None):
    """Data stops flowing but the connection stays open: a silently dead NAT mapping."""
    toxic(device_name, "timeout", {"timeout": 0}, bindings=bindings)


def remove(device_name):
    for p in _each(device_name, None):
        try:
            _call("DELETE", f"/proxies/{p}")
        except RuntimeError:
            pass


def reset_all():
    """Re-enable every proxy and remove every toxic."""
    _call("POST", "/reset")
