# Python SDK

Native Tandem v2 core for Python 3.15+. See the [shared scope and real-time contract](../README.md).

```sh
python3 -m venv sdk/python/.venv
sdk/python/.venv/bin/python -m pip install -e sdk/python
sdk/python/.venv/bin/python -B -m unittest discover -s sdk/python/tests
```

The distribution is `tandem-protocol`; the import is `tandem`. Writes, dots and
entries are frozen dataclasses. Use `dataclasses.replace` to create a changed
write. Values are bytes, and missing values are distinct from empty byte strings.

Given an implementation of `PushStore`, receive changes without polling:

```python
import tandem

async with tandem.Session(store, cursor=saved_cursor, capacity=64) as session:
    async for batch in session.updates():
        # Decrypt, validate and apply envelopes; save batch.cursor atomically.
        await apply_batch(batch)
```

Another task can `await session.submit(channel_tag, persisted_envelope)`. The
context manager cancels and joins its worker/subscription tasks and closes the
store. Queue capacity applies backpressure, not dropped messages.

`cryptography` supplies secret-key operations. The strict Ed25519 public equation
check is portable Python and trades verification speed for consistent acceptance
across native libraries. The IMAP adapter and complete device engine remain to do.
