import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';

const v = JSON.parse(await readFile(new URL('../../../vectors/v2/blobs.json', import.meta.url), 'utf8'));
const hex = s => Buffer.from(s, 'hex'), c = Object.fromEntries(Object.entries(v.chunk).map(([k, x]) => [k, hex(x)]));

test('blob chunk: key, envelope, address, escaped form, opening', () => {
  assert.equal(t.MAX_CHUNK, v.max_chunk);
  const key = t.chunkKey(c.r, c.plaintext), envelope = t.sealChunk(key, c.hub, c.plaintext);
  assert.deepEqual(key, c.key); assert.deepEqual(envelope, c.envelope); assert.deepEqual(t.chunkAddress(envelope), c.address);
  assert.deepEqual(t.escape(envelope), c.escaped); assert.deepEqual(t.openChunk(key, c.hub, envelope, c.plaintext.length), c.plaintext);
  for (const f of v.open_fails) assert.throws(() => t.openChunk(key, c.hub, hex(f.envelope), f.length), undefined, f.name);
  assert.throws(() => t.sealChunk(key, c.hub, Buffer.alloc(0))); assert.throws(() => t.sealChunk(key, c.hub, Buffer.alloc(t.MAX_CHUNK + 1)));
});

test('blob descriptions and manifests', () => {
  for (const b of v.blobs) {
    const {manifest, envelopes} = t.describe(c.r, c.hub, hex(b.content), b.chunk_size, b.inline);
    assert.equal(t.encodeManifest(manifest).toString(), b.manifest, b.name);
    assert.deepEqual(envelopes.map(e => e.toString('hex')), b.envelopes, b.name);
    const parsed = t.parseManifest(Buffer.from(b.manifest)); assert.equal(parsed.kind, 'manifest');
    const m = parsed.manifest;
    const content = m.chunks ? Buffer.concat(m.chunks.map((e, i) => t.openChunk(e.key, c.hub, envelopes[i], e.length))) : m.data;
    assert.deepEqual(content, hex(b.content), b.name); assert.deepEqual(t.sha(content), m.hash);
  }
  for (const m of v.manifests) assert.equal(t.parseManifest(Buffer.from(m.value)).kind, m.result, m.name);
});

test('the escaped form', () => {
  for (const e of v.escapes) { assert.deepEqual(t.escape(hex(e.input)), hex(e.escaped)); assert.deepEqual(t.unescape(hex(e.escaped)), hex(e.input)); }
  for (const e of v.unescape_lenient) assert.deepEqual(t.unescape(hex(e.body)), hex(e.envelope));
  for (const body of v.unescape_refused) assert.throws(() => t.unescape(hex(body)));
});
