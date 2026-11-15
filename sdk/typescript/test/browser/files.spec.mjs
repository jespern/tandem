import {test, expect} from '@playwright/test';
import {createServer} from 'node:http';
import {readFile} from 'node:fs/promises';

// Files between browser devices (DESIGN.md §17) on an in-memory hub and blob folder, in a real browser: IndexedDB,
// the Origin Private File System, Web Locks and BroadcastChannel are the browser's own.

let server, origin;
test.beforeAll(async () => {
  server = createServer(async (req, res) => {
    const files = {'/sdk.js': '../../dist/browser/tandem.js', '/argon-worker.js': '../../dist/browser/argon-worker.js', '/blob-harness.js': './blob-harness.js'};
    try {
      if (req.url === '/') { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><title>Tandem files tests</title>'); return; }
      if (!files[req.url]) { res.writeHead(404).end(); return; }
      res.setHeader('Content-Type', 'application/javascript'); res.end(await readFile(new URL(files[req.url], import.meta.url)));
    } catch { res.writeHead(500).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve)); origin = `http://127.0.0.1:${server.address().port}`;
});
test.afterAll(async () => { await new Promise(resolve => server.close(resolve)); });
test.beforeEach(async ({page}) => {
  page.on('console', msg => { if (msg.type() === 'error') console.error(msg.text()); });
  await page.goto(origin); await page.evaluate(async () => { window.h = await import('/blob-harness.js'); window.t = window.h.t; });
});
test.setTimeout(120_000);

test('an inline file is in the record at once, and no chunk goes to the hub', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household(), {a, b, list} = house, record = t.randomBytes(16), content = h.random(1000);
    try {
      await a.device.attach(list, record, 'note', new Blob([content]));
      const here = a.device.blob(list, record, 'note');
      await a.sent(); await b.until(d => d.blob(list, record, 'note')?.state === 'here');
      const read = await h.bytesOf(await b.device.fetchBlob(list, record, 'note'));
      return {here: here.state, size: here.size, same: h.same(read, content), chunks: house.folder.refs.length, inline: !!h.manifest(b.device, list, record, 'note').data};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({here: 'here', size: 1000, same: true, chunks: 0, inline: true});
});

test('a file of several chunks goes up, commits, and is read by the other device, with events on the way', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list} = house, record = t.randomBytes(16), content = h.random(300_000);
    try {
      const events = [], watch = new AbortController();
      void (async () => { for await (const u of b.device.updates(watch.signal)) for (const e of u.blobs ?? []) if (t.b64u(e.collection) === t.b64u(list)) events.push(...e.records.map(r => t.b64u(r))); })().catch(() => {});
      await a.device.attach(list, record, 'photo', new Blob([content]));
      const pending = a.device.blob(list, record, 'photo'), seenEarly = b.device.blob(list, record, 'photo');
      await a.device.settleBlobs(); await a.sent();
      const m = h.manifest(a.device, list, record, 'photo');
      await b.until(d => d.blob(list, record, 'photo')?.state === 'remote');
      const read = await h.bytesOf(await b.device.fetchBlob(list, record, 'photo'));
      await b.until(() => events.length > 0);
      watch.abort();
      return {pending: [pending.state, pending.chunks, pending.done], seenEarly: seenEarly ?? null, after: a.device.blob(list, record, 'photo').state, counts: h.counts(house.folder, m),
        same: h.same(read, content), b: b.device.blob(list, record, 'photo').state, events: events.every(r => r === t.b64u(record)) && events.length > 0, fetched: b.folder.fetched.length};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({pending: ['uploading', 5, 0], seenEarly: null, after: 'here', counts: [1, 1, 1, 1, 1], same: true, b: 'here', events: true, fetched: 5});
});

test('a file attached offline stays pending across a reload, is not on the feed, and goes out once the hub is reachable', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {b, list} = house; let {a} = house;
    const record = t.randomBytes(16), content = h.random(200_000);
    try {
      await a.device.writeFields(list, record, [{field: 'title', op: 1, value: new TextEncoder().encode('Scan')}]);
      a.folder.offline = true;
      await a.device.attach(list, record, 'scan', new Blob([content]));
      await a.device.settleBlobs(); await a.sent();
      const offline = {state: a.device.blob(list, record, 'scan').state, problem: !!a.device.update.blobProblem, chunks: house.folder.refs.length};
      await b.until(d => !!d.state.record(list, record)?.value('title'));
      const other = {title: new TextDecoder().decode(b.device.state.record(list, record).value('title')), file: b.device.blob(list, record, 'scan') ?? null, mine: a.device.state.record(list, record).value('_blob:scan') ?? null};
      // A reload: the pending write, its staged copy and its r survive in IndexedDB and OPFS.
      const {journalName, keysName, hubID} = a; await a.stop();
      a = await h.start('A', house.hub, journalName, keysName, (journal, keys) => t.BrowserDevice.resume(t.unb64u(hubID), journal, keys));
      const folder = new h.DeviceFolder(house.folder); await a.device.useBlobs(folder, {chunkSize: 65536});
      const reloaded = a.device.blob(list, record, 'scan').state;
      await a.device.settleBlobs(); await a.sent();
      await b.until(d => d.blob(list, record, 'scan')?.state === 'remote');
      const read = await h.bytesOf(await b.device.fetchBlob(list, record, 'scan'));
      return {offline, other, reloaded, after: a.device.blob(list, record, 'scan').state, problem: a.device.update.blobProblem ?? null, same: h.same(read, content), counts: h.counts(house.folder, h.manifest(b.device, list, record, 'scan'))};
    } finally { await a.stop(); await b.stop(); }
  });
  expect(result).toEqual({offline: {state: 'uploading', problem: true, chunks: 0}, other: {title: 'Scan', file: null, mine: null}, reloaded: 'uploading', after: 'here', problem: null, same: true, counts: [1, 1, 1, 1]});
});

test('an interrupted download resumes where it stopped', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list} = house, record = t.randomBytes(16), content = h.random(600_000);
    try {
      await a.device.attach(list, record, 'video', new Blob([content])); await a.device.settleBlobs(); await a.sent();
      await b.until(d => d.blob(list, record, 'video')?.state === 'remote');
      const stop = new AbortController(); let progress;
      b.folder.onFetch = () => { stop.abort(); };
      let interrupted = false; try { await b.device.fetchBlob(list, record, 'video', stop.signal); } catch (error) { interrupted = error.name === 'AbortError'; }
      b.folder.onFetch = undefined; progress = b.device.blob(list, record, 'video'); const first = b.folder.fetched.length;
      const read = await h.bytesOf(await b.device.fetchBlob(list, record, 'video'));
      return {interrupted, between: progress.state, first, total: b.folder.fetched.length, distinct: new Set(b.folder.fetched).size, same: h.same(read, content)};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({interrupted: true, between: 'remote', first: 4, total: 10, distinct: 10, same: true});
});

test('a chunk missing from the hub makes the file unavailable; junk under its address is rejected; the device holding it puts it back', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list, folder} = house, record = t.randomBytes(16), content = h.random(200_000);
    try {
      await a.device.attach(list, record, 'photo', new Blob([content])); await a.device.settleBlobs(); await a.sent();
      const m = h.manifest(a.device, list, record, 'photo'), lost = m.chunks[2].address, tag = folder.refs[0].channel;
      folder.drop(folder.withAddress(lost));
      // Anyone with the credential can append junk claiming the address.
      folder.inject(tag, lost, Uint8Array.from([84, 78, 68, 77, 2, 4, ...h.random(200)]));
      await b.until(d => d.blob(list, record, 'photo')?.state === 'remote');
      let reason; try { await b.device.fetchBlob(list, record, 'photo'); } catch (error) { reason = error.reason; }
      const missing = b.device.blob(list, record, 'photo').state;
      a.device.checkBlobs(); await a.device.settleBlobs();
      const restored = folder.withAddress(lost).length, read = await h.bytesOf(await b.device.fetchBlob(list, record, 'photo'));
      return {reason, missing, restored, same: h.same(read, content), state: b.device.blob(list, record, 'photo').state};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({reason: 'unavailable', missing: 'unavailable', restored: 2, same: true, state: 'here'});
});

test('replaced and deleted files are collected from the hub, and downloaded copies no record names are dropped', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list, folder} = house, record = t.randomBytes(16);
    const settled = async (condition) => { for (let i = 0; i < 6 && !condition(); i++) { await a.device.settleBlobs(); await b.device.settleBlobs(); } return condition(); };
    try {
      await a.device.attach(list, record, 'doc', new Blob([h.random(200_000)])); await a.device.settleBlobs(); await a.sent();
      const v1 = h.manifest(a.device, list, record, 'doc');
      await b.until(d => d.blob(list, record, 'doc')?.state === 'remote'); await b.device.fetchBlob(list, record, 'doc');
      const held = (await h.contents(b)).includes(t.b64u(v1.hash)) || (await h.contents(b)).length;
      await a.device.attach(list, record, 'doc', new Blob([h.random(150_000)])); await a.device.settleBlobs(); await a.sent();
      const v2 = h.manifest(a.device, list, record, 'doc');
      await b.until(d => h.manifest(d, list, record, 'doc')?.hash.equals(v2.hash));
      const replaced = await settled(() => h.counts(folder, v1).every(n => n === 0));
      const kept = h.counts(folder, v2);
      const hex = x => Array.from(x, v => v.toString(16).padStart(2, '0')).join('');
      let dropped = false; for (let i = 0; i < 6 && !dropped; i++) { await b.device.settleBlobs(); dropped = !(await h.contents(b)).includes(hex(v1.hash)); }
      await a.device.writeFields(list, record, [{field: '', op: 4}]); await a.sent();
      await b.until(d => !d.state.record(list, record) || d.state.record(list, record).deleted);
      const deleted = await settled(() => folder.refs.length === 0);
      return {held: !!held, replaced, kept, dropped, deleted};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({held: true, replaced: true, kept: [1, 1, 1], dropped: true, deleted: true});
});

test('content that does not match its manifest is never shown: a wrong key, a wrong length or a wrong hash is unreadable', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household(), {a, b, list, folder} = house, record = t.randomBytes(16), content = h.random(100_000);
    try {
      const {manifest, envelopes} = t.describe(t.randomBytes(32), a.device.hub, content, 65536, 0), tag = a.device.state.currentTag(list);
      await folder.append(envelopes.map(envelope => ({channel: tag, envelope})), 'escaped', new AbortController().signal);
      const wrongKey = {...manifest, chunks: manifest.chunks.map((e, i) => i === 1 ? {...e, key: t.randomBytes(32)} : e)};
      const wrongLength = {...manifest, chunks: manifest.chunks.map((e, i) => ({...e, length: i === 0 ? e.length - 1 : e.length + 1}))};
      const wrongHash = {...manifest, hash: t.sha(t.randomBytes(8))};
      await a.device.writeFields(list, record, [['key', wrongKey], ['length', wrongLength], ['hash', wrongHash], ['good', manifest]].map(([name, m]) => ({field: `_blob:${name}`, op: 1, value: t.encodeManifest(m)})));
      await a.sent(); await b.until(d => d.blob(list, record, 'good')?.state === 'remote');
      const out = {};
      for (const name of ['key', 'length', 'hash']) { try { await b.device.fetchBlob(list, record, name); out[name] = 'shown'; } catch (error) { out[name] = error.reason; } out[name] += '/' + b.device.blob(list, record, name).state; }
      out.good = h.same(await h.bytesOf(await b.device.fetchBlob(list, record, 'good')), content);
      out.files = (await h.contents(b)).length;
      return out;
    } finally { await house.stop(); }
  });
  expect(result).toEqual({key: 'unreadable/unreadable', length: 'unreadable/unreadable', hash: 'unreadable/unreadable', good: true, files: 1});
});

test('cancelling a partly uploaded file drops its write, and the chunks it had up are collected', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list, folder} = house, record = t.randomBytes(16);
    try {
      folder.partialAppends = 1;
      await a.device.attach(list, record, 'draft', new Blob([h.random(300_000)])); await a.device.settleBlobs();
      const partly = {state: a.device.blob(list, record, 'draft').state, chunks: folder.refs.length > 0};
      folder.partialAppends = undefined;
      await a.device.cancelAttach(list, record, 'draft'); await a.device.settleBlobs(); await a.sent();
      return {partly, after: a.device.blob(list, record, 'draft') ?? null, chunks: folder.refs.length, other: b.device.blob(list, record, 'draft') ?? null};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({partly: {state: 'uploading', chunks: true}, after: null, chunks: 0, other: null});
});

test('a list kept on a device fetches its files unasked up to a size, and the store evicts what was opened longest ago', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list} = house, [one, two, three] = [t.randomBytes(16), t.randomBytes(16), t.randomBytes(16)];
    try {
      b.device.setBlobSettings({unasked: 200_000, storeLimit: 460_000}); await b.device.keepFiles(list, true);
      await a.device.attach(list, one, 'f', new Blob([h.random(150_000)])); await a.device.settleBlobs(); await a.sent();
      await b.until(d => d.blob(list, one, 'f')?.state === 'here');
      await a.device.attach(list, two, 'f', new Blob([h.random(300_000)])); await a.device.settleBlobs(); await a.sent();
      await b.until(d => d.blob(list, two, 'f')?.state === 'remote'); await b.device.settleBlobs();
      const large = b.device.blob(list, two, 'f').state;
      await b.device.fetchBlob(list, two, 'f');
      await a.device.attach(list, three, 'f', new Blob([h.random(100_000)])); await a.device.settleBlobs(); await a.sent();
      await b.until(d => d.blob(list, three, 'f')?.state === 'here'); await b.device.settleBlobs();
      return {large, states: [one, two, three].map(r => b.device.blob(list, r, 'f').state)};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({large: 'remote', states: ['remote', 'here', 'here']});
});

test('a server that damages the escaped form gets base64, and an append whose answer was lost is not repeated', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list, folder} = house, [one, two] = [t.randomBytes(16), t.randomBytes(16)], content = h.random(200_000);
    try {
      folder.loseAnswers = 1;
      await a.device.attach(list, one, 'f', new Blob([h.random(200_000)]));
      // The append lands and its answer is lost; the next round, at the next event, finds the chunks on the hub and commits.
      await a.device.settleBlobs(); if (a.device.blob(list, one, 'f').state !== 'here') await a.device.settleBlobs(); await a.sent();
      const lost = {answers: folder.loseAnswers, appends: folder.appends};
      const once = h.counts(folder, h.manifest(a.device, list, one, 'f'));
      // The memory folder's form check passed with the first upload; a second hub-device pair checks afresh.
      await a.journal.setBlobValues(a.hubID, [{key: 'formchecked', value: null}]);
      folder.damageEscaped = true;
      await a.device.attach(list, two, 'f', new Blob([content])); await a.device.settleBlobs(); await a.sent();
      const problem = a.device.update.blobProblem ?? '';
      await b.until(d => d.blob(list, two, 'f')?.state === 'remote');
      const read = await h.bytesOf(await b.device.fetchBlob(list, two, 'f'));
      return {lost, once, base64: problem.includes('base64'), form: await a.journal.blobValue(a.hubID, 'form'), same: h.same(read, content)};
    } finally { await house.stop(); }
  });
  expect(result).toEqual({lost: {answers: 0, appends: 1}, once: [1, 1, 1, 1], base64: true, form: 'base64', same: true});
});

test('another tab of the same device sees a download finish, and the quota is unknown until the server reports one', async ({page}) => {
  const result = await page.evaluate(async () => {
    const {h, t} = window, house = await h.household({chunkSize: 65536}), {a, b, list, folder} = house, record = t.randomBytes(16);
    let tab;
    try {
      tab = await h.start('B2', house.hub, b.journalName, b.keysName, (journal, keys) => t.BrowserDevice.resume(t.unb64u(b.hubID), journal, keys));
      await tab.device.useBlobs(new h.DeviceFolder(folder));
      await a.device.attach(list, record, 'f', new Blob([h.random(150_000)])); await a.device.settleBlobs(); await a.sent();
      await tab.until(d => d.blob(list, record, 'f')?.state === 'remote');
      const seen = [], watch = new AbortController();
      const told = (async () => { for await (const u of tab.device.updates(watch.signal)) if (u.blobs?.length) { seen.push(u.blobs); if (tab.device.blob(list, record, 'f').state === 'here') return true; } })();
      await b.until(d => d.blob(list, record, 'f')?.state === 'remote'); await b.device.fetchBlob(list, record, 'f');
      const ok = await told; watch.abort();
      const unknown = await a.device.blobQuota(); folder.quotaReport = {kind: 'known', used: 1200, limit: 10_000};
      return {ok, other: tab.folder ? 'x' : tab.device.blob(list, record, 'f').state, unknown, known: await a.device.blobQuota()};
    } finally { await tab?.stop(); await house.stop(); }
  });
  expect(result).toEqual({ok: true, other: 'here', unknown: {kind: 'unknown'}, known: {kind: 'known', used: 1200, limit: 10_000}});
});
