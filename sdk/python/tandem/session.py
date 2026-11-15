"""Async envelope delivery. All work is triggered by startup, push, reconnect or submit."""
import asyncio
from dataclasses import dataclass
from typing import AsyncIterator, Protocol


@dataclass(frozen=True, slots=True)
class Message:
    id: str
    channel: str
    body: bytes


@dataclass(frozen=True, slots=True)
class ChangeBatch:
    added: tuple[Message, ...]
    removed: tuple[str, ...]
    cursor: str
    more: bool = False  # one page of what's new: call changes again with cursor at once, never a polling trigger


class PushStore(Protocol):
    async def watch(self) -> AsyncIterator[None]:
        """Return after subscription is active; buffer invalidations, including reconnect."""
        ...

    async def changes(self, cursor: str) -> ChangeBatch: ...
    async def append(self, channel: str, body: bytes) -> str: ...
    async def close(self) -> None: ...


class Session:
    """A serialized envelope pump; the application persists/retries and validates contents.

    Use `async with Session(store) as session`, `async for batch in session.updates()`,
    and `await session.submit(channel, envelope)`. No scheduled reconciliation exists.
    """
    def __init__(self, store: PushStore, cursor: str = "", capacity: int = 64):
        if capacity < 1:
            raise ValueError("session capacity must be positive")
        self.store, self.cursor = store, cursor
        self._work = asyncio.Queue(capacity)
        self._updates = asyncio.Queue(capacity)
        self._pending = set()
        self._task = None
        self._failure = None
        self._closed = False
        self._ready = asyncio.Event()

    async def __aenter__(self):
        if self._task is not None or self._closed:
            raise RuntimeError("session already started or closed")
        self._task = asyncio.create_task(self._run())
        await self._ready.wait()
        if self._failure is not None:
            raise self._failure
        return self

    async def __aexit__(self, *_):
        await self.close()

    async def submit(self, channel: str, body: bytes) -> str:
        if self._closed or self._task is None:
            raise RuntimeError("session is not running")
        result = asyncio.get_running_loop().create_future()
        self._pending.add(result)
        try:
            await self._work.put((channel, bytes(body), result))
            return await result
        except BaseException:
            if not result.done():
                result.cancel()
            elif not result.cancelled():
                result.exception()  # retrieve failure when shutdown interrupted queueing
            raise
        finally:
            self._pending.discard(result)

    async def updates(self) -> AsyncIterator[ChangeBatch]:
        while True:
            try:
                item = await self._updates.get()
            except asyncio.QueueShutDown:
                if self._failure is not None:
                    raise self._failure
                return
            yield item

    async def _reconcile(self):
        while True:
            batch = await self.store.changes(self.cursor)
            await self._updates.put(batch)
            self.cursor = batch.cursor
            if not batch.more:
                return

    async def _run(self):
        watcher = None
        try:
            events = await self.store.watch()
            async def watch():
                try:
                    async for _ in events:
                        await self._work.put(None)
                    await self._work.put(ConnectionError("push subscription ended"))
                except Exception as error:
                    await self._work.put(error)
            watcher = asyncio.create_task(watch())
            self._ready.set()
            await self._reconcile()
            while True:
                item = await self._work.get()
                if isinstance(item, Exception):
                    raise item
                if item is not None:
                    channel, body, result = item
                    if result.cancelled():
                        continue
                    try:
                        ident = await self.store.append(channel, body)
                    except Exception as error:
                        if not result.done():
                            result.set_exception(error)
                        continue
                    if not result.done():
                        result.set_result(ident)
                await self._reconcile()
        except asyncio.CancelledError:
            raise
        except Exception as error:
            self._failure = error
        finally:
            self._closed = True
            self._ready.set()
            if watcher is not None:
                watcher.cancel()
                await asyncio.gather(watcher, return_exceptions=True)
            for result in self._pending:
                if not result.done():
                    result.set_exception(self._failure or ConnectionError("session closed"))
            self._work.shutdown(immediate=True)
            self._updates.shutdown()  # preserve queued batches; wake blocked readers/writers
            await self.store.close()

    async def close(self):
        if self._task is not None:
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)
        elif not self._closed:
            self._closed = True
            await self.store.close()
