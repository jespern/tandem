"""The hub message format of DESIGN §10.4, without any cryptography.

The testbed treats envelopes as opaque bytes. Probes build fake envelopes (the
`TNDM` magic plus random bytes) to check that both bindings carry them intact.
"""

import base64
import email.utils
import hashlib
import os
import quopri
import re
import secrets
import time

FOLDER = "Tandem"
MAGIC = b"TNDM"


def fake_envelope(size=200, kind=1):
    """Bytes shaped like an envelope: magic, version 1, kind, then random."""
    return MAGIC + bytes([1, kind]) + os.urandom(max(0, size - 6))


def channel_tag():
    """A random 20-byte channel tag in lowercase base32, as in DESIGN §5.6."""
    return base64.b32encode(os.urandom(20)).decode().lower().rstrip("=")


def envelope_id(envelope):
    """A message's identity across bindings and devices: H(envelope) (§10.4)."""
    return hashlib.sha256(envelope).hexdigest()


def wrapped_base64(envelope):
    b64 = base64.b64encode(envelope).decode()
    return "\r\n".join(b64[i:i + 76] for i in range(0, len(b64), 76))


def build_message(channel, envelope, when=None):
    """The full RFC 5322 message an IMAP append stores (§10.4), CRLF line endings."""
    date = email.utils.formatdate(when if when is not None else time.time(), usegmt=True)
    headers = [
        "From: Tandem <tandem@invalid>",
        f"Date: {date}",
        f"Message-ID: <{secrets.token_hex(16)}@tandem.invalid>",
        "Subject: tandem",
        "MIME-Version: 1.0",
        "Tandem-Version: 1",
        f"Tandem-Channel: {channel}",
        "Content-Type: text/plain; charset=us-ascii",
        "Content-Transfer-Encoding: 7bit",
    ]
    return ("\r\n".join(headers) + "\r\n\r\n" + wrapped_base64(envelope) + "\r\n").encode("ascii")


def jmap_create(mailbox_id, channel, envelope):
    """The Email/set create object for the same message (§10.2)."""
    return {
        "mailboxIds": {mailbox_id: True},
        "keywords": {"$seen": True},
        "from": [{"name": "Tandem", "email": "tandem@invalid"}],
        "subject": "tandem",
        "header:Tandem-Version:asText": "1",
        "header:Tandem-Channel:asText": channel,
        "bodyStructure": {"type": "text/plain", "partId": "b"},
        "bodyValues": {"b": {"value": wrapped_base64(envelope).replace("\r\n", "\n") + "\n"}},
    }


_CHARSET = re.compile(r'charset\s*=\s*"?([^";\s]+)"?', re.I)


def charset_of(content_type):
    m = _CHARSET.search(content_type or "")
    return m.group(1) if m else "us-ascii"


def decode_text(text):
    """Step 3 of §10.4: strip all whitespace and base64-decode."""
    return base64.b64decode(re.sub(r"\s+", "", text), validate=True)


def decode_body(content_type, transfer_encoding, raw_body):
    """All three steps of §10.4 for a raw MIME body, as an IMAP reader does them."""
    cte = (transfer_encoding or "7bit").strip().lower()
    if cte == "base64":
        data = base64.b64decode(re.sub(rb"\s+", b"", raw_body))
    elif cte == "quoted-printable":
        data = quopri.decodestring(raw_body)
    elif cte in ("7bit", "8bit", "binary"):
        data = raw_body
    else:
        raise ValueError(f"unknown transfer encoding {cte!r}")
    return decode_text(data.decode(charset_of(content_type)))


def split_message(raw):
    """Split a raw RFC 5322 message into (headers dict, body bytes). Header names lowercased."""
    head, _, body = raw.replace(b"\r\n", b"\n").partition(b"\n\n")
    headers, last = {}, None
    for line in head.decode("utf-8", "replace").split("\n"):
        if line[:1] in (" ", "\t") and last:
            headers[last] += " " + line.strip()
        elif ":" in line:
            name, _, value = line.partition(":")
            last = name.strip().lower()
            headers[last] = value.strip()
    return headers, body


def decode_message(raw):
    """(channel tag, envelope) from a raw stored message."""
    headers, body = split_message(raw)
    envelope = decode_body(headers.get("content-type"), headers.get("content-transfer-encoding"), body)
    return headers.get("tandem-channel", "").strip(), envelope


def reencode_message(raw, charset, transfer_encoding):
    """Rebuild a stored message with its body in another charset and transfer
    encoding, as a JMAP server is allowed to (§10.2). Used for the
    `hub: rewrite charset` step."""
    headers, _ = split_message(raw)
    channel, envelope = decode_message(raw)
    text = wrapped_base64(envelope) + "\r\n"
    data = text.encode(charset)
    if transfer_encoding == "base64":
        body = base64.encodebytes(data).replace(b"\n", b"\r\n")
    elif transfer_encoding == "quoted-printable":
        body = quopri.encodestring(data).replace(b"\n", b"\r\n")
    elif transfer_encoding in ("7bit", "8bit"):
        body = data
    else:
        raise ValueError(transfer_encoding)
    keep = ["from", "date", "message-id", "subject", "mime-version", "tandem-version"]
    lines = [f"{k.title()}: {headers[k]}" for k in keep if k in headers]
    lines.append(f"Tandem-Channel: {channel}")
    lines.append(f"Content-Type: text/plain; charset={charset}")
    lines.append(f"Content-Transfer-Encoding: {transfer_encoding}")
    return ("\r\n".join(lines) + "\r\n\r\n").encode("ascii") + body
