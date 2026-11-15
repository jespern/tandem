import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile, writeFile} from 'node:fs/promises';
import {EventEmitter, on} from 'node:events';
import * as t from '../dist/index.js';

const vector = async name => JSON.parse(await readFile(new URL(`../../../vectors/v2/${name}`, import.meta.url), 'utf8'));
const v = await vector('core.json'), hex = s => Buffer.from(s, 'hex');
const m = Object.fromEntries(Object.entries(v.material).map(([k, value]) => [k, hex(value)]));
function write(row) {
  const w = {...row.write};
  for (const k of ['hub', 'scope', 'record', 'author', 'certificate', 'authorization', 'ref', 'signature', 'value']) w[k] = hex(w[k]);
  w.element = w.element === null ? null : hex(w.element);
  const dot = d => ({...d, device: hex(d.device)});
  w.dot = dot(w.dot); w.context = w.context.map(dot); w.base = w.base.map(dot); return w;
}

test('v2 key derivations and inventories', () => {
  const manager = t.managerKeys(m.management_seed);
  const derived = {home_scope: t.homeScope(m.hub, m.uid).toString('hex'), named: t.namedID('é').toString('hex'), named_decomposed: t.namedID('e\u0301').toString('hex'),
    commitment: t.commit(m.key).toString('hex'), key_id: t.keyID(m.hub, m.agreement_public).toString('hex'), collection_tag: t.collectionTag(m.key, m.hub, m.epoch),
    home_tag: t.homeTag(m.key, m.hub, m.uid, m.epoch), inbox_tag: t.inboxTag(m.uid, m.hub), directory_key: t.directoryKey(m.key, m.hub).toString('hex'),
    directory_tag: t.directoryTag(m.key, m.hub), manager_signing_public: t.publicKey(manager.signing).toString('hex'), manager_agreement_public: t.publicKey(manager.agreement).toString('hex')};
  assert.deepEqual(derived, v.derived);
  const inventory = hex(v.inventory); assert.deepEqual(t.inventoryBytes(t.parseInventory(inventory)), inventory);
  assert.throws(() => t.parseInventory(Buffer.concat([inventory, Buffer.from([0])])));
  assert.throws(() => t.parseInventory(inventory.subarray(0, -1)));
});

test('writes, public construction, signatures, payloads and all authority bindings', () => {
  for (const row of v.writes) {
    const w = write(row); assert.equal(t.sigInput(w).toString('hex'), row.sig_input); assert.equal(t.writeID(w).toString('hex'), row.id);
    assert.equal(t.verifyWrite(w, m.signing_public), true); assert.equal(t.verifyWrite(w, w.author), false);
    const parsed = t.parseKind1(t.recordPayload(w.record, [w]), w.hub, w.scope);
    assert.equal(t.verifyWrite(parsed.writes[0], m.signing_public), true);
    assert.equal(t.verifyWrite(t.signWrite(w, t.privateKey(m.signing_seed, 'ed25519')), m.signing_public), true);
    for (const binding of ['hub', 'certificate', 'authorization', 'author']) assert.equal(t.verifyWrite({...w, [binding]: Buffer.alloc(32)}, m.signing_public), false);
  }
});

test('native cryptography opens v2 fixtures and rejects changed contexts', async () => {
  const plain = hex(v.plaintext), tag = v.derived.collection_tag, key = t.privateKey(m.agreement_private, 'x25519');
  assert.deepEqual(t.openEnvelope(m.key, m.hub, tag, hex(v.symmetric)), {kind: 1, plaintext: plain});
  assert.deepEqual(t.openInbox(m.hub, m.uid, key, hex(v.inbox)), plain);
  for (const kind of [1, 3]) assert.deepEqual(t.openEnvelope(m.key, m.hub, tag, t.seal(m.key, m.hub, tag, plain, kind)), {kind, plaintext: plain});
  const inbox = t.sealInbox(m.hub, m.uid, m.agreement_public, plain); assert.deepEqual(t.openInbox(m.hub, m.uid, key, inbox), plain);
  assert.throws(() => t.openEnvelope(m.key, Buffer.alloc(32), tag, hex(v.symmetric)));
  assert.throws(() => t.openInbox(m.hub, Buffer.alloc(32), key, inbox));
  assert.throws(() => t.openEnvelope(m.key, m.hub, tag.toUpperCase(), hex(v.symmetric)));
  const old = hex(v.symmetric); old[4] = 1; assert.throws(() => t.openEnvelope(m.key, m.hub, tag, old));
  for (let i = 0; i < hex(v.symmetric).length; i++) assert.throws(() => t.openEnvelope(m.key, m.hub, tag, hex(v.symmetric).subarray(0, i)));
  if (process.env.TANDEM_INTEROP_DIR) await writeFile(`${process.env.TANDEM_INTEROP_DIR}/typescript.json`, JSON.stringify({inbox: inbox.toString('hex'), signature: t.sign(t.privateKey(m.signing_seed, 'ed25519'), plain).toString('hex')}));
});

test('strict JSON, base64url and Ed25519 acceptance policy', async () => {
  for (const row of (await vector('json.json')).cases.invalid) assert.throws(() => t.parseJSON(hex(row.hex)), row.why);
  for (const row of (await vector('json.json')).cases.valid) t.parseJSON(hex(row.hex));
  for (const row of (await vector('b64u.json')).cases.invalid) assert.throws(() => t.unb64u(row.text), row.why);
  for (const row of (await vector('b64u.json')).cases.valid) assert.equal(t.unb64u(row.text).toString('hex'), row.hex);
  for (const row of (await vector('ed25519.json')).cases.signatures) assert.equal(t.verifyStrict(hex(row.public), hex(row.signature), hex(row.message)), row.valid, `signature ${row.case}`);
});

test('record merge, replay, bytewise Unicode fields, scope exclusion and defensive copies', () => {
  for (const scenario of v.merges) {
    const entries = scenario.indexes.map(i => new t.Entry(write(v.writes[i]), m.epoch, 0n));
    for (const order of [entries, [...entries].reverse()]) {
      const record = new t.RecordState(m.hub, m.scope, m.record); for (const e of order) record.merge(e); record.merge(...entries);
      assert.deepEqual(record.kept.map(e => e.id.toString('hex')), scenario.kept); assert.equal(record.deleted, scenario.deleted);
    }
  }
  const w = write(v.writes[0]), entry = new t.Entry(w, m.epoch, 0n); w.value[0] ^= 1; assert.notDeepEqual(w.value, entry.write.value);
  const snapshot = entry.write; snapshot.value[0] ^= 1; assert.notDeepEqual(snapshot.value, entry.write.value);
  const record = new t.RecordState(m.hub, m.scope, m.record);
  assert.throws(() => record.merge(new t.Entry({...w, hub: Buffer.alloc(32)}, m.epoch, 0n)));
  assert.deepEqual(t.nextClock(9, 65535, 0), {millis: 10, counter: 0}); assert.throws(() => t.nextClock(2**48 - 1, 65535, 0));
});

class FakeStore {
  events = new EventEmitter(); calls = 0; subscribed = false; closed = false; added = [];
  async watch(signal) { this.subscribed = true; const iterator = on(this.events, 'wake', {signal}); return {[Symbol.asyncIterator]: () => iterator}; }
  async changes(cursor, signal) { signal.throwIfAborted(); assert.equal(this.subscribed, true); this.calls++; this.events.emit('changes'); const added = this.added; this.added = []; return {added, removed: [], cursor: String(this.calls)}; }
  async append(channel, body, signal) { signal.throwIfAborted(); this.added.push({id: 'local', channel, body: Buffer.from(body)}); return 'local'; }
  async close() { this.closed = true; }
  async untilChanges(n) { if (this.calls >= n) return; await new Promise(resolve => { const listener = () => { if (this.calls >= n) { this.events.off('changes', listener); resolve(); } }; this.events.on('changes', listener); }); }
}

test('closing a session releases blocked submissions and preserves buffered updates', {timeout: 5000}, async () => {
  const store = new FakeStore(), session = new t.Session(store, {capacity: 1}); await session.start();
  await store.untilChanges(1); store.events.emit('wake'); await store.untilChanges(2);
  const pending = [1, 2, 3].map(() => session.submit('channel', Buffer.from('queued')).then(() => null, error => error));
  await session.close(); const results = await Promise.all(pending); assert.equal(results.every(e => e instanceof Error), true);
  const updates = session.updates(); assert.equal((await updates.next()).value.cursor, '1'); assert.equal((await updates.next()).done, true);
});

test('live session only reconciles at initial sync, push, local submit and reconnect', {timeout: 5000}, async () => {
  const store = new FakeStore(), session = new t.Session(store, {capacity: 4}); await session.start(); const updates = session.updates();
  try {
    assert.equal((await updates.next()).value.cursor, '1');
    store.events.emit('wake'); assert.equal((await updates.next()).value.cursor, '2');
    assert.equal(await session.submit('channel', Buffer.from('local edit')), 'local');
    assert.equal(Buffer.from((await updates.next()).value.added[0].body).toString(), 'local edit');
    store.events.emit('wake'); assert.equal((await updates.next()).value.cursor, '4');
    assert.equal(store.calls, 4);
  } finally { await session.close(); }
  assert.equal(store.closed, true); assert.equal((await updates.next()).done, true);
});

test('packs: several records in one message; bad forms are refused (§10.5)', () => {
  for (const p of v.packs.valid) {
    const states = t.parseKind1States(hex(p.payload), m.hub, m.scope);
    assert.deepEqual(states.map(s => [s.record.toString('hex'), s.writes.map(w => t.writeID(w).toString('hex'))]),
      p.records.map(r => [r.record, r.writes]));
    assert.deepEqual(t.packPayload(states), hex(p.payload));
    assert.throws(() => t.parseKind1(hex(p.payload), m.hub, m.scope));
  }
  for (const bad of v.packs.invalid) assert.throws(() => t.parseKind1States(hex(bad), m.hub, m.scope));
});
