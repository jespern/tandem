import {test} from 'node:test';
import assert from 'node:assert/strict';
import * as t from '../dist/index.js';
import {buildMessage} from '../dist/browser/message.js';

const envelope = Buffer.concat([Buffer.from([84, 78, 68, 77, 2, 4]), Buffer.from('\0\r\n=. F>\t \0'), t.randomBytes(3000)]);
const channel = t.b32(t.randomBytes(20));

test('the canonical Date and Message-ID of DESIGN §10.4', () => {
  assert.equal(t.rfc5322Date(new Date(Date.UTC(2026, 10, 13, 18, 4, 5))), 'Fri, 13 Nov 2026 18:04:05 +0000');
  assert.equal(t.rfc5322Date(new Date(Date.UTC(2026, 10, 6, 8, 0, 9))), 'Fri, 6 Nov 2026 08:00:09 +0000');
  assert.match(t.randomMessageID(), /^[0-9a-f]{32}$/);
  const feed = Buffer.from(buildMessage(channel, Buffer.concat([Buffer.from([84, 78, 68, 77, 2, 1]), t.randomBytes(60)]))).toString('latin1');
  assert.match(feed, /\r\nDate: (Mon|Tue|Wed|Thu|Fri|Sat|Sun), [1-9]\d? [A-Z][a-z]{2} \d{4} \d\d:\d\d:\d\d \+0000\r\n/);
  assert.match(feed, /\r\nMessage-ID: <[0-9a-f]{32}@tandem\.airforce>\r\n/);
});

test('a chunk message in both forms reads back to its envelope (§17.4.2)', () => {
  const date = new Date(Date.UTC(2026, 10, 15, 9, 30, 0)), id = '0123456789abcdef0123456789abcdef';
  const escaped = t.chunkMessage(channel, envelope, 'escaped', date, id), text = escaped.toString('latin1');
  const [head] = text.split('\r\n\r\n');
  assert.deepEqual(head.split('\r\n'), ['From: Tandem <tandem@tandem.airforce>', 'Date: Sun, 15 Nov 2026 09:30:00 +0000', `Message-ID: <${id}@tandem.airforce>`,
    'Subject: tandem', 'MIME-Version: 1.0', 'Tandem-Version: 2', `Tandem-Channel: ${channel}`, `Tandem-Blob: ${t.b32(t.chunkAddress(envelope))}`,
    'Tandem-Body: escaped', 'Content-Type: application/octet-stream', 'Content-Transfer-Encoding: 8bit']);
  assert.ok(!escaped.includes(0), 'no NUL in the escaped form');
  assert.deepEqual(t.readChunkMessage(escaped), envelope);
  const base64 = t.chunkMessage(channel, envelope, 'base64', date, id);
  assert.ok(!base64.toString('latin1').includes('Tandem-Body'));
  assert.ok(base64.toString('latin1').split('\r\n\r\n')[1].split('\r\n').every(line => line.length <= 76));
  assert.deepEqual(t.readChunkMessage(base64), envelope);
  assert.deepEqual(t.parseBlobClaim(t.headerField(escaped.subarray(0, escaped.indexOf('\r\n\r\n') + 2), 'tandem-blob')), t.chunkAddress(envelope));
});

test('a reader undoes whatever transfer encoding a server stored, and nothing else is a chunk', () => {
  // A server that re-encoded the escaped body as base64 rewrites the MIME headers and keeps Tandem-Body (BLOBS.md).
  const escapedBody = t.escape(envelope);
  const reencoded = Buffer.from(`Tandem-Body: escaped\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n${escapedBody.toString('base64').match(/.{1,76}/g).join('\r\n')}\r\n`, 'latin1');
  assert.deepEqual(t.readChunkMessage(reencoded), envelope);
  const qp = Buffer.from(`Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n${[...envelope].map(b => '=' + b.toString(16).toUpperCase().padStart(2, '0')).join('').match(/.{1,72}/g).join('=\r\n')}`, 'latin1');
  assert.deepEqual(t.readChunkMessage(qp), envelope);
  const folded = Buffer.concat([Buffer.from('Content-Type:\r\n application/octet-stream; x=1\r\nTandem-Body: ESCAPED\r\n\r\n', 'latin1'), escapedBody]);
  assert.deepEqual(t.readChunkMessage(folded), envelope);
  const binary = Buffer.concat([Buffer.from('Content-Type: application/octet-stream\nContent-Transfer-Encoding: binary\n\n', 'latin1'), envelope]);
  assert.deepEqual(t.readChunkMessage(binary), envelope);
  assert.throws(() => t.readChunkMessage(Buffer.concat([Buffer.from('Content-Type: text/plain\r\nTandem-Body: escaped\r\n\r\n'), escapedBody])));
  assert.throws(() => t.readChunkMessage(Buffer.concat([Buffer.from('Tandem-Body: escaped\r\n\r\n'), escapedBody])), undefined, 'no content type is text/plain');
  assert.throws(() => t.readChunkMessage(Buffer.from('Content-Type: application/octet-stream\r\nTandem-Body: escaped\r\n\r\nabc=')));
  assert.throws(() => t.readChunkMessage(Buffer.from('Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!')));
  assert.equal(t.parseBlobClaim('not base32'), undefined); assert.equal(t.parseBlobClaim(t.b32(t.randomBytes(20))), undefined);
});

test('the memory blob folder: claims from headers, ordered IDs, removal, faults', async () => {
  const folder = new t.MemoryBlobFolder(), signal = new AbortController().signal, other = Buffer.concat([envelope.subarray(0, 6), t.randomBytes(100)]);
  const ids = await folder.append([{channel, envelope}, {channel, envelope: other}], 'escaped', signal);
  assert.deepEqual(ids, ['1', '2']);
  let page = await folder.changes('', signal);
  assert.deepEqual(page.added.map(r => [r.id, r.channel, t.b64u(r.address)]), [['1', channel, t.b64u(t.chunkAddress(envelope))], ['2', channel, t.b64u(t.chunkAddress(other))]]);
  assert.deepEqual((await folder.fetch(['2', '9', '1'], signal)).map(c => c.id), ['2', '1']);
  folder.drop(['1']);
  page = await folder.changes(page.cursor, signal); assert.deepEqual(page.removed, ['1']);
  const junk = folder.inject(channel, t.chunkAddress(envelope), other);
  assert.deepEqual(folder.withAddress(t.chunkAddress(envelope)), [junk]);
  assert.notDeepEqual(t.chunkAddress((await folder.fetch([junk], signal))[0].envelope), t.chunkAddress(envelope), 'the claim is not the content');
  folder.damageEscaped = true; const [damaged] = await folder.append([{channel, envelope}], 'escaped', signal);
  assert.notDeepEqual((await folder.fetch([damaged], signal))[0].envelope, envelope);
  const [clean] = await folder.append([{channel, envelope}], 'base64', signal);
  assert.deepEqual((await folder.fetch([clean], signal))[0].envelope, envelope);
  folder.loseAnswers = 1; await assert.rejects(folder.append([{channel, envelope: other}], 'base64', signal));
  assert.equal(folder.withAddress(t.chunkAddress(other)).length, 2, 'a lost answer may still have landed');
  folder.failAppends = true; await assert.rejects(folder.append([{channel, envelope}], 'base64', signal), t.BlobFolderFull);
  assert.deepEqual(await folder.quota(signal), {kind: 'unknown'});
  assert.ok(folder.compare('9', '10') < 0, 'IDs are ordered as numbers, as UIDs are');
});
