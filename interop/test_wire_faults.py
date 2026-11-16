"""The proxy must prove its faults, preserve framing, and leave success observable."""
import asyncio
import unittest

from harness import Failed
from wire_faults import Connection, WireProxy


class Sink:
    def __init__(self):
        self.data, self.aborted = bytearray(), False
        self.transport = self

    def write(self, data):
        self.data.extend(data)

    async def drain(self):
        pass

    def abort(self):
        self.aborted = True


class WireFaultTests(unittest.TestCase):
    def relay(self, raw, kind, client=False, commands=None, skip=0):
        async def run():
            proxy = WireProxy(dict(host='127.0.0.1', insecure=True))
            proxy.rule = dict(kind=kind, after=8, skip=skip)
            source = asyncio.StreamReader()
            source.feed_data(raw); source.feed_eof()
            down, up = Sink(), Sink()
            c = Connection(down, up, commands=commands or {}, blobs=True, append_blobs=True)
            await proxy.relay(source, up if client else down, c, client)
            return proxy, c
        return asyncio.run(run())

    def test_body_literal_after_header_continuation_is_cut(self):
        raw = b'* 1 FETCH (UID 2 BODY[HEADER] {3}\r\nabc BODY[TEXT] {16}\r\n0123456789abcdef)\r\nx OK done\r\n'
        p, c = self.relay(raw, 'download', commands={b'x': b'UID'})
        p.assert_fired()
        self.assertEqual(p.evidence, [dict(kind='download', literal_bytes=16, forwarded_bytes=8)])
        self.assertTrue(c.downstream.data.endswith(b'01234567'))
        self.assertTrue(c.upstream.aborted)

    def test_lost_ack_requires_real_tagged_success(self):
        p, c = self.relay(b'x NO failed\r\n', 'append_ack', commands={b'x': b'APPEND'})
        with self.assertRaises(Failed):
            p.assert_fired()
        self.assertEqual(c.downstream.data, b'x NO failed\r\n')
        p, c = self.relay(b'* 1 EXISTS\r\nx OK [APPENDUID 1 2] stored\r\n', 'append_ack', commands={b'x': b'APPEND'})
        p.assert_fired()
        self.assertEqual(c.downstream.data, b'* 1 EXISTS\r\n')
        self.assertTrue(p.evidence[0]['server_accepted'])

    def test_idle_command_crlf_and_done(self):
        p, c = self.relay(b'x IDLE\r\n', 'idle', client=True)
        self.assertTrue(c.idle)
        self.assertEqual(c.commands, {b'x': b'IDLE'})
        _, c = self.relay(b'x IDLE\r\nDONE\r\n', 'idle', client=True)
        self.assertFalse(c.idle)

    def test_upload_cuts_inside_literal_and_forwards_unchanged_prefix(self):
        p, c = self.relay(b'x APPEND "test-blobs" {16+}\r\n0123456789abcdef\r\n', 'upload', client=True)
        p.assert_fired()
        self.assertEqual(c.upstream.data, b'x APPEND "test-blobs" {16+}\r\n01234567')

    def test_hold_stops_forwarding_without_closing(self):
        # A crash boundary: the engine must still be waiting there when it is killed, not told of a cut.
        async def run():
            proxy = WireProxy(dict(host='127.0.0.1', insecure=True))
            proxy.rule = dict(kind='download', after=8, skip=0, hold=True)
            source = asyncio.StreamReader()
            source.feed_data(b'* 1 FETCH (UID 2 BODY[TEXT] {16}\r\n0123456789abcdef)\r\nx OK done\r\n'); source.feed_eof()
            down, up = Sink(), Sink()
            c = Connection(down, up, commands={b'x': b'UID'}, blobs=True)
            task = asyncio.create_task(proxy.relay(source, down, c, False))
            for _ in range(1000):
                if c.held:
                    break
                await asyncio.sleep(0)
            self.assertTrue(c.held)
            self.assertFalse(task.done())
            self.assertFalse(down.aborted or up.aborted, 'a hold closed the connection')
            self.assertTrue(down.data.endswith(b'01234567'))
            self.assertEqual(proxy.evidence, [dict(kind='download', literal_bytes=16, forwarded_bytes=8, held=True)])
            c.release.set()
            await task
        asyncio.run(run())

    def test_a_rule_another_connection_used_is_not_applied_again(self):
        # Two connections race one rule: while this one waits mid-literal, the other uses it.
        async def run():
            proxy = WireProxy(dict(host='127.0.0.1', insecure=True))
            proxy.rule = dict(kind='download', after=8, skip=0)
            source = asyncio.StreamReader()
            source.feed_data(b'* 1 FETCH (UID 2 BODY[TEXT] {16}\r\n0123')
            down, up = Sink(), Sink()
            c = Connection(down, up, commands={b'x': b'UID'}, blobs=True)
            task = asyncio.create_task(proxy.relay(source, down, c, False))
            for _ in range(1000):
                if b'0123' in down.data or source._waiter is not None:
                    break
                await asyncio.sleep(0)
            proxy.rule = None  # the other connection's cut
            source.feed_data(b'456789abcdef)\r\nx OK done\r\n'); source.feed_eof()
            await task
            self.assertFalse(down.aborted)
            self.assertIn(b'0123456789abcdef', down.data)
        asyncio.run(run())

    def test_proxy_refuses_live_providers_and_remote_insecure_hosts(self):
        for hub in (dict(host='imap.fastmail.com', insecure=False), dict(host='remote', insecure=True)):
            with self.assertRaises(Failed):
                WireProxy(hub)

    def test_skip_counts_body_literals_and_not_short_headers(self):
        raw = (b'* 1 FETCH (BODY[HEADER] {3}\r\nabc BODY[TEXT] {16}\r\n0123456789abcdef)\r\n'
               b'* 2 FETCH (BODY[HEADER] {3}\r\nxyz BODY[TEXT] {16}\r\nfedcba9876543210)\r\n')
        p, c = self.relay(raw, 'download', commands={b'x': b'UID'}, skip=1)
        p.assert_fired()
        self.assertIn(b'0123456789abcdef', c.downstream.data)
        self.assertTrue(c.downstream.data.endswith(b'fedcba98'))
