import asyncio
import io
import json
import os
import ssl
import unittest
import uuid

from tandem import JMAPConfig, JMAPStore, collection_tag, sha, seal


class JMAPTests(unittest.IsolatedAsyncioTestCase):
    async def test_origin_trust(self):
        store = JMAPStore(JMAPConfig.for_fastmail('example'))
        store._trusted('https://ams.api.fastmail.com/api')
        for value in ['https://api.fastmail.com.evil.test/api', 'http://ams.api.fastmail.com/api', 'https://ams.api.fastmail.com:8443/api', 'https://user:secret@ams.api.fastmail.com/api']:
            with self.assertRaises(ValueError):
                store._trusted(value)
        generic = JMAPStore(JMAPConfig('https://mail.example.test/session', 'Bearer example'))
        with self.assertRaises(ValueError):
            generic._trusted('https://api.example.test/api')
        with self.assertRaises(ValueError):
            JMAPConfig.for_fastmail('token\nheader')
        await store.close()
        await generic.close()

    async def test_sse_framing_and_limits(self):
        for separator in ['\n', '\r\n', '\r']:
            store = JMAPStore(JMAPConfig('https://mail.example.test/session', 'Bearer example'))
            store._account = 'account'
            def state(account, value):
                return 'data: ' + json.dumps({'changed': {account: {'Email': value}}, 'extension': 0.5})
            stream = separator.join(['\ufeff: heartbeat', '', 'event: state', state('other', 'a'), '', 'event: state', state('account', 'b'), '', '']).encode()
            with self.assertRaises(ConnectionError):
                await asyncio.to_thread(store._read_events, io.BytesIO(stream), asyncio.get_running_loop())
            await asyncio.wait_for(store._wakes.get(), 2)
            self.assertTrue(store._wakes.empty())
            await store.close()
        store = JMAPStore(JMAPConfig('https://mail.example.test/session', 'Bearer example', max_event_bytes=16))
        with self.assertRaises(ValueError):
            await asyncio.to_thread(store._read_events, io.BytesIO(b'x' * 100), asyncio.get_running_loop())
        await store.close()

    async def test_real_jmap(self):
        if 'TANDEM_JMAP_FIXTURE' not in os.environ:
            self.skipTest('run sdk/check_jmap.py for disposable Stalwart')
        fixture = json.loads(os.environ['TANDEM_JMAP_FIXTURE'])
        cfg = JMAPConfig(fixture['sessionUrl'], fixture['authorization'], folder_name='Tandem-python-' + uuid.uuid4().hex, page_size=2, tls_context=ssl._create_unverified_context())
        reader, writer = await JMAPStore.open(cfg), await JMAPStore.open(cfg)
        try:
            events = await reader.watch()
            pushed = asyncio.create_task(anext(events))
            initial = await reader.changes('')
            key, hub, epoch = (sha(value) for value in [b'key', b'hub', b'epoch'])
            tag, ids, bodies = collection_tag(key, hub, epoch), [], []
            for i in range(5):
                body = seal(key, hub, tag, bytes([i]))
                ids.append(await writer.append(tag, body))
                bodies.append(body)
            await asyncio.wait_for(pushed, 10)
            batch = await reader.changes(initial.cursor)
            self.assertEqual(len(batch.added), 5)
            self.assertEqual(await reader.changes(initial.cursor), batch)
            self.assertEqual(len((await reader.changes('')).added), 5)
            fetched = {message.id: message.body for message in await reader.fetch(ids)}
            self.assertEqual(fetched, dict(zip(ids, bodies)))
            other = await JMAPStore.open(JMAPConfig(fixture['sessionUrl'], fixture['authorization'], folder_name=cfg.folder_name + '-other', tls_context=cfg.tls_context))
            try:
                with self.assertRaises(ValueError):
                    await other.destroy(ids[:1])
            finally:
                await other.close()
            await writer.destroy(ids)
            self.assertEqual(set((await reader.changes(batch.cursor)).removed), set(ids))
            await events.aclose()
        finally:
            await reader.close()
            await writer.close()
