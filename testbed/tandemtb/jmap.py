"""Minimal JMAP client (RFC 8620 / RFC 8621) on the standard library.

Enough for the testbed's provisioning, hostile-hub operations and binding probes.
Not the Tandem SDK: it knows nothing about envelopes or merging.
"""

import base64
import http.client
import json
import urllib.error
import urllib.parse
import urllib.request

CORE = "urn:ietf:params:jmap:core"
MAIL = "urn:ietf:params:jmap:mail"
STALWART = "urn:stalwart:jmap"


class JmapError(Exception):
    """A method-level or request-level JMAP error."""

    def __init__(self, kind, detail=None, status=None):
        super().__init__(f"{kind}: {detail}" if detail else kind)
        self.kind = kind
        self.detail = detail
        self.status = status                               # HTTP status for request-level errors


class Jmap:
    def __init__(self, session_url, user=None, password=None, token=None, timeout=30):
        self.session_url = session_url
        self.user, self.password, self.token = user, password, token
        self.timeout = timeout
        self._session = None

    # -- transport -------------------------------------------------------

    def _auth(self):
        if self.token:
            return "Bearer " + self.token
        raw = f"{self.user}:{self.password}".encode()
        return "Basic " + base64.b64encode(raw).decode()

    def _url(self, url):
        """Resolve a URL from the session against the session URL's origin.

        Servers may advertise absolute URLs with their own hostname; through a
        proxy (Toxiproxy) the client must keep talking to the proxy.
        """
        absolute = urllib.parse.urljoin(self.session_url, url)
        origin = urllib.parse.urlsplit(self.session_url)
        parts = urllib.parse.urlsplit(absolute)
        return urllib.parse.urlunsplit((origin.scheme, origin.netloc, parts.path, parts.query, parts.fragment))

    def _request(self, url, data=None, headers=None, method=None):
        hdrs = {"Authorization": self._auth()}
        hdrs.update(headers or {})
        req = urllib.request.Request(self._url(url), data=data, headers=hdrs, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                return resp.read()
        except urllib.error.HTTPError as e:
            body = e.read().decode(errors="replace")
            try:
                problem = json.loads(body)
            except ValueError:
                raise JmapError(f"http-{e.code}", body, e.code) from None
            raise JmapError(problem.get("type", f"http-{e.code}"), problem.get("detail") or body, e.code) from None

    # -- session ---------------------------------------------------------

    def session(self, refresh=False):
        if self._session is None or refresh:
            self._session = json.loads(self._request(self.session_url))
        return self._session

    @property
    def account_id(self):
        return self.session()["primaryAccounts"][MAIL]

    def core_capability(self, name):
        return self.session()["capabilities"][CORE].get(name)

    # -- method calls ----------------------------------------------------

    def request(self, method_calls, using=(CORE, MAIL)):
        body = json.dumps({"using": list(using), "methodCalls": method_calls}).encode()
        raw = self._request(self.session()["apiUrl"], data=body, headers={"Content-Type": "application/json"})
        return json.loads(raw)["methodResponses"]

    def call(self, method, args, using=(CORE, MAIL)):
        """One method call; returns its arguments or raises JmapError."""
        args = dict(args)
        args.setdefault("accountId", self.account_id)
        (name, result, _), = self.request([[method, args, "0"]], using)
        if name == "error":
            raise JmapError(result.get("type", "error"), result.get("description"))
        return result

    # -- blobs -----------------------------------------------------------

    def download(self, blob_id, name="blob", accept="application/octet-stream"):
        template = self.session()["downloadUrl"]
        url = (template.replace("{accountId}", urllib.parse.quote(self.account_id))
               .replace("{blobId}", urllib.parse.quote(blob_id))
               .replace("{name}", urllib.parse.quote(name))
               .replace("{type}", urllib.parse.quote(accept)))
        return self._request(url)

    def upload(self, data, content_type="message/rfc822"):
        url = self.session()["uploadUrl"].replace("{accountId}", urllib.parse.quote(self.account_id))
        return json.loads(self._request(url, data=data, headers={"Content-Type": content_type}))

    # -- push --------------------------------------------------------------

    def event_source(self, types="Email", closeafter="no", ping=60, read_timeout=None):
        """Yield (event, data) pairs from the EventSource endpoint (RFC 8620 §7.3).

        `read_timeout` bounds the wait for each line, so a silent connection
        raises TimeoutError instead of hanging (callers decide what that means).
        """
        template = self.session()["eventSourceUrl"]
        url = self._url(template.replace("{types}", types)
                        .replace("{closeafter}", closeafter)
                        .replace("{ping}", str(ping)))
        parts = urllib.parse.urlsplit(url)
        conn_cls = http.client.HTTPSConnection if parts.scheme == "https" else http.client.HTTPConnection
        conn = conn_cls(parts.hostname, parts.port, timeout=read_timeout or self.timeout)
        path = parts.path + ("?" + parts.query if parts.query else "")
        conn.request("GET", path, headers={"Authorization": self._auth(), "Accept": "text/event-stream"})
        resp = conn.getresponse()
        if resp.status != 200:
            raise JmapError(f"http-{resp.status}", resp.read().decode(errors="replace"))
        event, data = "message", []
        try:
            while True:
                line = resp.fp.readline()
                if not line:
                    return
                line = line.decode().rstrip("\r\n")
                if line == "":
                    if data:
                        yield event, "\n".join(data)
                    event, data = "message", []
                elif line.startswith(":"):
                    continue
                elif line.startswith("event:"):
                    event = line[6:].strip()
                elif line.startswith("data:"):
                    data.append(line[5:].lstrip())
        finally:
            conn.close()
