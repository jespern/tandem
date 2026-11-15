"""File lifecycle scenarios against the real Go/Swift engines and real IMAP stores."""
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import base64
import hashlib
import json
import struct
import sqlite3
import os

from harness import Failed, Rejected, b64u, unb64u, check, content
from providers import Mailbox, quote

CHUNK = 65536


def world(new, hub, role='writer', third=False, chunk=CHUNK, store_limit=None):
    bob, alice = new('a', 'Bob'), new('b', 'Alice')
    bob('found', label='Bob')
    alice('join', link=bob('invite', label='Alice')['link'], label='Alice')
    carol = None
    if third:
        carol = new('b', 'Carol')
        carol('join', link=bob('invite', label='Carol')['link'], label='Carol')
    bob('sync')
    lst = bob('createList', name='Files')['list']
    for d in (alice, carol):
        if d:
            bob('share', list=lst, participant=d('me')['participant'], role=role)
            d('sync'); d('accept', list=lst)
    for d in (bob, alice, carol):
        if d:
            options = dict(chunkSize=chunk, connections=2)
            if store_limit is not None:
                options['storeLimit'] = store_limit
            d('useBlobs', **options)
            d('settleBlobs')
    return bob, alice, carol, lst


def target(lst, record=None, name='file'):
    return dict(list=lst, record=record or b64u(os.urandom(16)), name=name)


def attach(d, t, data):
    d('attach', **t, content=b64u(data))
    d('settleBlobs')


def fetch(d, t, data):
    content('file content', unb64u(d('fetchBlob', **t)['content']), data)


def manifest(d, t):
    raw = d('manifest', **t)['value']
    return json.loads(unb64u(raw)) if raw else None


def rejected(d, op, **args):
    try:
        d(op, **args)
    except Rejected:
        return
    raise Failed(f'{op} unexpectedly succeeded')


def verified(hub):
    return [r for r in Mailbox(hub).chunks() if r['verified']]


def file_boundaries(new, hub):
    """Empty/inline/chunk boundaries, binary bytes, Unicode names and a multi-batch file."""
    bob, alice, _, lst = world(new, hub, chunk=1 << 20)
    for size in (0, 1, 16384, 16385, (1 << 20) - 1, 1 << 20, (1 << 20) + 1, (7 << 20) + 17):
        t = target(lst, name='résumé 📷')
        data = (bytes(range(256)) + os.urandom(size))[:size]
        attach(bob, t, data)
        alice('sync')
        fetch(alice, t, data)
        expected = [] if size <= 16384 else [1] * ((size + (1 << 20) - 1) // (1 << 20))
        check('one message per chunk', bob('chunkCounts', **t)['counts'], expected)
    rows = Mailbox(hub).chunks()
    check('every stored envelope hashes to its header', all(r['verified'] for r in rows), True)
    check('escaped form survives the real provider', {r['form'] for r in rows}, {'escaped'})


def file_resume(new, hub):
    """Real partial upload, abrupt process loss and resume; downloaded content survives another restart."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(5 * CHUNK + 17)
    bob('blobFault', after=2)
    attach(bob, t, data)
    check('two chunks really reached the provider', bob('blobFaultStatus')['stored'], 2)
    check('pending upload stays out of the published replica', bob('manifest', **t)['value'], None)
    check('partial upload messages', len(verified(hub)), 2)
    alice('sync')
    check('receiver sees no partial file', alice('blob', **t)['state'], None)
    first_ids = {r['uid'] for r in verified(hub)}
    bob.restart()
    bob('settleBlobs')
    alice('sync'); fetch(alice, t, data)
    check('resume reuses uploaded chunks', first_ids <= {r['uid'] for r in verified(hub)}, True)
    check('resume makes no duplicates', bob('chunkCounts', **t)['counts'], [1] * 6)
    alice.restart()
    check('download is durable', alice('blob', **t)['state'], 'here')
    fetch(alice, t, data)


def file_lost_ack(new, hub):
    """An accepted append loses its response, then a crash retries exact chunk identities."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(3 * CHUNK + 1)
    bob('blobFault', after=1, lostAck=True)
    attach(bob, t, data)
    check('server accepted the unacknowledged chunk', len(verified(hub)), 1)
    survivor = verified(hub)[0]['uid']
    bob.restart(); bob('settleBlobs')
    alice('sync'); fetch(alice, t, data)
    check('lost-answer resume is idempotent', bob('chunkCounts', **t)['counts'], [1] * 4)
    check('accepted message is reused', survivor in {r['uid'] for r in verified(hub)}, True)


def file_pending_replacement(new, hub):
    """A partial replacement, key rotation, cancellation and an unrelated edit preserve the published file."""
    bob, alice, _, lst = world(new, hub)
    t, old = target(lst), os.urandom(CHUNK + 13)
    attach(bob, t, old); alice('sync'); fetch(alice, t, old)
    old_ids = {r['uid'] for r in verified(hub)}
    bob('blobFault', after=1)
    attach(bob, t, os.urandom(3 * CHUNK + 1))
    check('replacement remains pending', bob('blob', **t)['state'], 'uploading')
    bob('set', list=lst, record=t['record'], field='title', value=b64u(b'edit during upload'))
    bob('rekey', list=lst)
    alice('sync'); fetch(alice, t, old)
    check('rotation keeps published chunks', old_ids <= {r['uid'] for r in verified(hub)}, True)
    bob('cancelAttach', **t); bob('clearBlobFault'); bob('sync'); bob('settleBlobs')
    alice('sync'); fetch(alice, t, old)
    check('cancelled replacement chunks are collected', {r['uid'] for r in verified(hub)}, old_ids)


def file_unpublished_replacement(new, hub):
    """A committed replacement whose feed append fails cannot collect the version peers still see."""
    bob, alice, _, lst = world(new, hub)
    t, old, newer = target(lst), os.urandom(CHUNK + 1), os.urandom(2 * CHUNK + 1)
    attach(bob, t, old); alice('sync')
    old_ids = {r['uid'] for r in verified(hub)}
    bob('offline', value=True)
    attach(bob, t, newer)
    # Blobs use their own real connections; the feed remains offline.
    check('published version remains stored', old_ids <= {r['uid'] for r in verified(hub)}, True)
    alice('sync'); fetch(alice, t, old)
    bob.restart(); bob('sync'); bob('settleBlobs')
    alice('sync'); fetch(alice, t, newer)
    check('old chunks collected after publication', old_ids.isdisjoint({r['uid'] for r in verified(hub)}), True)


def file_conflicts(new, hub):
    """Two offline replacements keep both siblings and their chunks, then a new assignment resolves them."""
    bob, alice, _, lst = world(new, hub)
    t, original = target(lst), os.urandom(CHUNK + 1)
    attach(bob, t, original); alice('sync')
    versions = [os.urandom(2 * CHUNK + 1), os.urandom(3 * CHUNK + 1)]
    for d, data in zip((bob, alice), versions):
        d('offline', value=True); d('blobFault', after=0); attach(d, t, data)
    for d in (bob, alice):
        d('offline', value=False); d('clearBlobFault'); d('settleBlobs')
    for d in (bob, alice, bob):
        d('sync'); d('settleBlobs')
    a = set(bob('fieldValues', list=lst, record=t['record'], field='_blob:file')['values'])
    b = set(alice('fieldValues', list=lst, record=t['record'], field='_blob:file')['values'])
    check('both devices keep the same conflicts', a, b)
    check('both offline files survive', len(a), 2)
    needed = set()
    for value in a:
        chunks = unb64u(json.loads(unb64u(value))['chunks'])
        needed.update(base64.b32encode(chunks[i:i+32]).decode().rstrip('=').lower() for i in range(0, len(chunks), 68))
    check('cleanup preserves every conflicting version', needed <= {r['address'] for r in verified(hub)}, True)
    # A fresh assigning write observes both siblings and resolves the conflict.
    final = os.urandom(CHUNK + 19)
    attach(alice, t, final)
    for d in (bob, alice, bob):
        d('sync'); d('settleBlobs')
    fetch(bob, t, final)
    check('resolution is one sibling', len(bob('fieldValues', list=lst, record=t['record'], field='_blob:file')['values']), 1)
    check('only resolved content remains', len(verified(hub)), 2)


def file_junk_and_healing(new, hub):
    """Junk with a correct-looking header cannot suppress healing; concurrent duplicate cleanup keeps a valid copy."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(3 * CHUNK + 7)
    attach(bob, t, data); alice('sync')
    mailbox = Mailbox(hub)
    rows = verified(hub)
    victim = rows[1]
    mailbox.destroy([victim['uid']])
    headers = victim['raw'].split(b'\r\n\r\n', 1)[0]
    mailbox.append(headers + b'\r\n\r\nnot-the-chunk\r\n')
    rejected(alice, 'fetchBlob', **t)
    check('missing bytes have an explicit state', alice('blob', **t)['state'], 'unavailable')
    # The failed download is durable too; resume after the writer repairs the chunk.
    alice.restart()
    bob('checkBlobs')
    fetch(alice, t, data)
    mailbox.append(verified(hub)[0]['raw'])
    check('duplicate really reached the provider', len(verified(hub)), 5)
    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = [pool.submit(d, 'checkBlobs') for d in (bob, alice)]
        for future in futures:
            future.result()
    bob('checkBlobs')
    good = verified(hub)
    check('one genuine message per address', len(good), len({r['address'] for r in good}))
    check('all genuine chunks survive cleanup', len(good), 4)
    fetch(alice, t, data)


def file_membership(new, hub):
    """Reader permissions, rotation without chunk uploads, and removal protecting future files."""
    bob, alice, carol, lst = world(new, hub, role='reader', third=True)
    t, old = target(lst), os.urandom(CHUNK + 3)
    attach(bob, t, old)
    for d in (alice, carol):
        d('sync')
    fetch(alice, t, old)
    rejected(alice, 'attach', **target(lst), content=b64u(os.urandom(CHUNK + 1)))
    before = {r['uid'] for r in verified(hub)}
    bob('removeMember', list=lst, participant=carol('me')['participant'])
    alice('sync'); carol('sync')
    check('removal uploads no file content', {r['uid'] for r in verified(hub)}, before)
    check('Carol is removed', carol('role', list=lst)['role'], None)
    future, data = target(lst), os.urandom(CHUNK + 19)
    attach(bob, future, data)
    alice('sync'); carol('sync'); fetch(alice, future, data)
    check('removed member learns no future manifest', carol('manifest', **future)['value'], None)
    fetch(carol, t, old)  # Old keys still open old data, as the protocol explicitly promises.


def file_deletion(new, hub):
    """Record deletion and expired collection retention release files without touching another scope."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(CHUNK + 1)
    attach(bob, t, data); alice('sync'); fetch(alice, t, data)
    bob('delete', list=lst, record=t['record'])
    for d in (bob, alice, bob):
        d('sync'); d('settleBlobs')
    check('record deletion collects its chunks', len(verified(hub)), 0)
    other = bob('createList', name='Keep this scope')['list']
    keep, kept_data = target(other), os.urandom(CHUNK + 17)
    attach(bob, keep, kept_data)
    keep_ids = {r['uid'] for r in verified(hub)}
    t = target(lst); attach(bob, t, data)
    bob('deleteList', list=lst); alice('sync')
    check('collection data survives the default retention', len(verified(hub)), 4)
    bob('setRetention', ms=1)
    for d in (bob, bob):
        d('sync'); d('settleBlobs')
    check('expired collection cleanup is scope-bound', {r['uid'] for r in verified(hub)}, keep_ids)
    fetch(bob, keep, kept_data)


def assign_manifest(d, t, m):
    d('set', list=t['list'], record=t['record'], field='_blob:' + t['name'], value=b64u(json.dumps(m).encode()))


def file_manifest_guards(new, hub):
    """An unknown version blocks collection; authenticated chunks with false lengths stay unreadable."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(CHUNK + 17)
    attach(bob, t, data)
    m, ids = manifest(bob, t), {r['uid'] for r in verified(hub)}
    assign_manifest(bob, t, dict(m, blob=2))
    for d in (bob, alice, bob):
        d('sync'); d('settleBlobs')
    check('unknown manifests are explicit', alice('blob', **t)['state'], 'unreadable')
    check('unknown version stops collection', {r['uid'] for r in verified(hub)}, ids)
    # Both entry lengths remain valid individually and still sum to the file size;
    # only checking the actual decrypted chunk lengths catches this.
    entries = bytearray(unb64u(m['chunks']))
    struct.pack_into('>I', entries, 64, CHUNK - 1)
    struct.pack_into('>I', entries, 68 + 64, 18)
    assign_manifest(bob, t, dict(m, chunks=b64u(entries)))
    alice('sync')
    rejected(alice, 'fetchBlob', **t)
    check('false lengths cannot produce a readable file', alice('blob', **t)['state'], 'unreadable')
    assign_manifest(bob, t, m)
    alice('sync'); fetch(alice, t, data)


def file_reused_chunks(new, hub):
    """Readers accept repeated addresses and mixed lengths; deleting one reference preserves the others."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(CHUNK + 17)
    attach(bob, t, data)
    m, before = manifest(bob, t), {r['uid'] for r in verified(hub)}
    entries = unb64u(m['chunks'])
    derived, composed = target(lst), data[:CHUNK] + data[:CHUNK] + data[CHUNK:]
    assign_manifest(bob, derived, dict(m, size=len(composed), hash=b64u(hashlib.sha256(composed).digest()),
                                     chunks=b64u(entries[:68] + entries)))
    alice('sync'); fetch(alice, derived, composed)
    bob('delete', list=lst, record=t['record'])
    for d in (bob, alice, bob):
        d('sync'); d('settleBlobs')
    check('a second reference retains the physical chunks', {r['uid'] for r in verified(hub)}, before)
    fetch(alice, derived, composed)
    bob('delete', list=lst, record=derived['record'])
    for d in (bob, alice, bob):
        d('sync'); d('settleBlobs')
    check('last reference releases the chunks', verified(hub), [])


def file_pending_rotation_conflict(new, hub):
    """Rotating during an upload cannot turn its old causal context into authority over unseen peer edits."""
    bob, alice, _, lst = world(new, hub)
    t, original = target(lst), os.urandom(CHUNK + 1)
    attach(bob, t, original); alice('sync')
    pending, peer = os.urandom(2 * CHUNK + 3), os.urandom(3 * CHUNK + 5)
    bob('blobFault', after=0); attach(bob, t, pending)
    attach(alice, t, peer)
    bob('sync'); bob('rekey', list=lst); alice('sync')
    bob('clearBlobFault'); bob('settleBlobs')
    for d in (alice, bob):
        d('sync'); d('settleBlobs')
    values = bob('fieldValues', list=lst, record=t['record'], field='_blob:file')['values']
    check('rotation preserves both concurrent files', len(values), 2)
    hashes = {json.loads(unb64u(v))['hash'] for v in values}
    check('neither attachment is silently overwritten', hashes,
          {b64u(hashlib.sha256(data).digest()) for data in (pending, peer)})


def file_commit_failure(new, hub):
    """A durable-store failure at publication cannot consume the upload journal or its staged content."""
    bob, alice, _, lst = world(new, hub)
    t, old = target(lst), os.urandom(CHUNK + 1)
    attach(bob, t, old); alice('sync')
    database = bob.directory / 'device.sqlite'
    for table, action in (('writes', 'INSERT'), ('uploads', 'DELETE')):
        previous = bob('manifest', **t)['value']
        newer = os.urandom(2 * CHUNK + 3)
        bob('blobFault', after=0); attach(bob, t, newer)
        # Exercise failure both before saving the write and after saving it but
        # before consuming the journal. SQLite must roll the whole commit back.
        with sqlite3.connect(database) as db:
            row = "NEW" if action == "INSERT" else "OLD"
            db.execute(f"CREATE TRIGGER fail_file_commit BEFORE {action} ON {table} WHEN {row}.record = X'" +
                       unb64u(t['record']).hex() + "' BEGIN SELECT RAISE(ABORT, 'injected durable commit failure'); END")
        bob('clearBlobFault'); bob('settleBlobs')
        with sqlite3.connect(database) as db:
            rows = db.execute('SELECT staged FROM uploads').fetchall()
            db.execute('DROP TRIGGER fail_file_commit')
        check('failed commit preserves a resumable upload', len(rows), 1)
        check('failed commit leaves the replica unchanged', bob('manifest', **t)['value'], previous)
        check('failed commit preserves its staged content', (Path(str(database) + '.blobs') / 'staged' / rows[0][0]).is_file(), True)
        bob.restart(); bob('settleBlobs'); alice('sync')
        fetch(alice, t, newer)


def file_copy_refuses_attachments(new, hub):
    """An unsupported cross-scope copy refuses before creating a list or silently dropping files."""
    bob, alice, _, lst = world(new, hub)
    for data in (b'inline attachment', os.urandom(CHUNK + 1)):
        t = target(lst)
        attach(bob, t, data); alice('sync')
        before = bob('lists')['lists']
        try:
            bob('copy', list=lst)
        except Rejected as error:
            if 'file attachments' not in str(error):
                raise
        else:
            raise Failed('copy silently accepted unsupported attachments')
        check('refused copy creates no partial list', bob('lists')['lists'], before)
        fetch(alice, t, data)
        bob('delete', list=lst, record=t['record']); bob('settleBlobs')


def file_unpublished_content_survives_eviction(new, hub):
    """Until the manifest reaches the feed, its staged source must survive cache eviction and lost hub chunks."""
    bob, alice, _, lst = world(new, hub)
    t, old, newer = target(lst), os.urandom(CHUNK + 1), os.urandom(2 * CHUNK + 3)
    attach(bob, t, old); alice('sync')
    old_ids = {r['uid'] for r in verified(hub)}
    bob('offline', value=True); attach(bob, t, newer)
    m = manifest(bob, t)
    cache = Path(str(bob.directory / 'device.sqlite') + '.blobs') / unb64u(m['hash']).hex()
    cache.unlink()  # The content cache is evictable; the staged source is not yet.
    Mailbox(hub).destroy([r['uid'] for r in verified(hub) if r['uid'] not in old_ids])
    bob.restart(); bob('sync'); bob('checkBlobs')
    alice('sync'); fetch(alice, t, newer)


def file_push(new, hub):
    """Both directions discover a published file through real IDLE and engine events, without explicit sync."""
    bob, alice, _, lst = world(new, hub)
    bob('run'); alice('run')  # Answers only after each real watch subscription is active.
    for sender, receiver in ((bob, alice), (alice, bob)):
        t, data = target(lst), os.urandom(2 * CHUNK + 17)
        attach(sender, t, data)
        receiver('awaitBlob', **t, state='remote')
        fetch(receiver, t, data)


def file_partial_manifest_isolation(new, hub):
    """A bad signed manifest cannot poison resume state for a good file with the same hash."""
    bob, alice, _, lst = world(new, hub)
    good, bad = target(lst), target(lst)
    data = os.urandom(5 * CHUNK + 17)
    attach(bob, good, data)
    m = manifest(bob, good)
    entries = unb64u(m['chunks'])
    # Real chunks and real keys, but a false order under the legitimate hash.
    reordered = entries[68:136] + entries[:68] + entries[136:]
    assign_manifest(bob, bad, dict(m, chunks=b64u(reordered)))
    alice('sync')
    rejected(alice, 'fetchBlob', **bad)
    check('only the bad manifest is unreadable', alice('blob', **bad)['state'], 'unreadable')
    alice.restart()
    fetch(alice, good, data)
    # Correcting the rejected record is also allowed to use the real content.
    assign_manifest(bob, bad, m)
    alice('sync'); fetch(alice, bad, data)


def file_inline_hash_isolation(new, hub):
    """Inline data must match its advertised hash before it can enter the shared content cache."""
    bob, alice, _, lst = world(new, hub)
    good, bad = target(lst), target(lst)
    data, poison = os.urandom(CHUNK + 17), b'false content under a real file hash'
    attach(bob, good, data)
    m = manifest(bob, good)
    assign_manifest(bob, bad, dict(blob=1, size=len(poison), hash=m['hash'], data=b64u(poison)))
    alice('sync')
    rejected(alice, 'fetchBlob', **bad)
    check('false inline hash is explicit', alice('blob', **bad)['state'], 'unreadable')
    fetch(alice, good, data)
    # A pre-existing verified cache entry must not conceal an invalid inline description.
    rejected(alice, 'fetchBlob', **bad)


def file_removed_upload_stays_cancelled(new, hub):
    """Learning of removal cancels a pending upload permanently, even if re-admitted later."""
    bob, alice, _, lst = world(new, hub)
    t, old = target(lst), os.urandom(CHUNK + 1)
    attach(bob, t, old); alice('sync')
    alice('blobFault', after=1)
    attach(alice, t, os.urandom(3 * CHUNK + 3))
    bob('removeMember', list=lst, participant=alice('me')['participant'])
    alice('sync'); alice('settleBlobs')
    with sqlite3.connect(alice.directory / 'device.sqlite') as db:
        count = db.execute('SELECT COUNT(*) FROM uploads').fetchone()[0]
    check('observed removal consumes the pending upload', count, 0)
    bob('share', list=lst, participant=alice('me')['participant'], role='writer')
    alice('sync')
    if any(o['list'] == lst for o in alice('offers')['offers']):
        alice('accept', list=lst)
    alice.restart(); alice('sync'); alice('settleBlobs'); bob('sync')
    fetch(bob, t, old); fetch(alice, t, old)


def file_larger_than_cache(new, hub):
    """A successful explicit fetch must not evict its own return value to meet the cache budget."""
    bob, alice, _, lst = world(new, hub, store_limit=CHUNK)
    first, second = target(lst), target(lst)
    old, newer = os.urandom(2 * CHUNK + 1), os.urandom(3 * CHUNK + 7)
    attach(bob, first, old); attach(bob, second, newer); alice('sync')
    fetch(alice, first, old)
    fetch(alice, second, newer)
    check('previous oversized file can be evicted', alice('blob', **first)['state'], 'remote')
    check('the just-returned file remains readable', alice('blob', **second)['state'], 'here')
    fetch(alice, second, newer)


def file_folder_recreation(new, hub):
    """Recreating the blob folder changes UIDVALIDITY; cached IDs and first-seen times must reset."""
    bob, alice, _, lst = world(new, hub)
    t, data = target(lst), os.urandom(2 * CHUNK + 17)
    attach(bob, t, data); alice('sync'); fetch(alice, t, data)
    m = manifest(alice, t)
    mailbox = Mailbox(hub)
    rows = mailbox.chunks()
    with sqlite3.connect(alice.directory / 'device.sqlite') as db:
        old_ids = {r[0] for r in db.execute('SELECT id FROM chunks')}
        db.execute('UPDATE chunks SET seen=0')
    # Only this scenario's random, exact folder is ever deleted/recreated.
    with mailbox.connection() as server:
        folder = quote(mailbox.blob_folder)
        check('select original folder', server.select(folder)[0], 'OK')
        old_validity = server.response('UIDVALIDITY')[1]
        ids = ','.join(row['uid'] for row in rows)
        check('clear owned chunks before deleting their folder', server.uid('STORE', ids, '+FLAGS.SILENT', '(\\Deleted)')[0], 'OK')
        check('expunge only the owned chunks', server.uid('EXPUNGE', ids)[0], 'OK')
        server.unselect()
        check('delete owned blob folder', server.delete(folder)[0], 'OK')
        check('recreate owned blob folder', server.create(folder)[0], 'OK')
        for row in rows:
            check('restore actual encrypted bytes', server.append(folder, None, None, row['raw'])[0], 'OK')
        check('select recreated folder', server.select(folder)[0], 'OK')
        new_validity = server.response('UIDVALIDITY')[1]
        if old_validity == new_validity:
            raise Failed('provider reused UIDVALIDITY: recreation was not exercised')
    cache = alice.directory / ('device.sqlite.blobs/' + unb64u(m['hash']).hex())
    cache.unlink()
    alice.restart(); alice('sync'); alice('settleBlobs'); fetch(alice, t, data)
    with sqlite3.connect(alice.directory / 'device.sqlite') as db:
        current = db.execute('SELECT id, seen FROM chunks').fetchall()
    check('no ID of the old folder survives', old_ids & {r[0] for r in current}, set())
    check('every new first-seen clock starts over', all(r[1] > 0 for r in current), True)
    check('recreation retains exactly the authentic chunks', alice('chunkCounts', **t)['counts'], [1] * 3)


FILE_SCENARIOS = [file_boundaries, file_resume, file_lost_ack, file_pending_replacement,
                  file_unpublished_replacement, file_conflicts, file_junk_and_healing,
                  file_membership, file_deletion, file_manifest_guards, file_reused_chunks, file_pending_rotation_conflict, file_commit_failure, file_copy_refuses_attachments, file_unpublished_content_survives_eviction, file_push, file_partial_manifest_isolation, file_inline_hash_isolation, file_removed_upload_stays_cancelled, file_larger_than_cache, file_folder_recreation]
