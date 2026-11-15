"""Oracle tests use hand-worked histories, independent of native engine internals."""
import unittest

from history import FileModel


class HistoryOracleTests(unittest.TestCase):
    def test_unseen_write_remains_concurrent(self):
        m = FileModel()
        m.put('a', 'file', b'initial'); m.sync(['a', 'b', 'c'])
        m.put('a', 'file', b'alice'); m.put('b', 'file', b'bob')
        self.assertEqual(set(m.values('file')), {b'alice', b'bob'})
        m.sync(['a', 'b', 'c']); m.put('c', 'file', b'resolved')
        self.assertEqual(m.values('file'), [b'resolved'])

    def test_ancestry_does_not_cross_records(self):
        m = FileModel()
        m.put('a', 'one', b'one'); m.put('a', 'two', b'two')
        self.assertEqual(m.values('one'), [b'one'])
        m.deleted.add('one'); m.put('b', 'one', b'offline')
        self.assertEqual(m.values('one'), [])
        self.assertEqual(m.values('two'), [b'two'])
