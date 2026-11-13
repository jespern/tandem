import {test, expect} from '@playwright/test';
import {createServer} from 'node:http';
import {readFile, writeFile, readdir} from 'node:fs/promises';
import * as node from '../../dist/index.js';

let server, origin;
test.beforeAll(async () => {
  server = createServer(async (req, res) => {
    try {
      if (req.url === '/') { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><title>Tandem browser tests</title>'); return; }
      const path = req.url === '/sdk.js' ? '../../dist/browser/tandem.js' : req.url === '/argon-worker.js' ? '../../dist/browser/argon-worker.js' : req.url === '/capsule.json' ? '../../../../conformance/fixtures/tandem-v2-capsule.json' : `../../../../vectors/v2/${req.url.slice(1)}`;
      if (!['/sdk.js', '/argon-worker.js', '/capsule.json', '/core.json', '/control.json', '/json.json', '/b64u.json', '/ed25519.json'].includes(req.url)) { res.writeHead(404).end(); return; }
      res.setHeader('Content-Type', req.url.endsWith('.js') ? 'application/javascript' : 'application/json'); res.end(await readFile(new URL(path, import.meta.url)));
    } catch { res.writeHead(500).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve)); origin = `http://127.0.0.1:${server.address().port}`;
});
test.afterAll(async () => { await new Promise(resolve => server.close(resolve)); });
test.beforeEach(async ({page}) => { page.on('console', msg => { if (msg.type()==='error') console.error(msg.text()); }); page.on('requestfailed', req => { if(req.failure()?.errorText!=='net::ERR_ABORTED')console.error('request failed:',req.url(),req.failure()?.errorText); }); await page.goto(origin); await page.evaluate(async () => { window.t = await import('/sdk.js'); }); });

test('real browser: every control vector and strict Ed25519 case, and Node interoperability', async ({page}) => {
  const artifacts = await page.evaluate(async () => {
    const t = window.t, hex = s => Uint8Array.from(s.match(/../g) ?? [], x => parseInt(x, 16));
    const controls = await (await fetch('/control.json')).json();
    const objects=new Map(controls.objects.map(row=>[row.name,t.controlFromJSON(new TextEncoder().encode(row.json))]));
    const registry=new t.Registry(t.pinGenesis(objects.get('genesis')));
    for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'])registry.add(objects.get(name));
    const tablet=objects.get('alice tablet certificate'),tabletID=t.objectID(tablet.kind,tablet.fields);
    if(registry.certificateValidity(tabletID).kind!=='valid'||registry.activity(tabletID).kind!=='invalid')throw new Error('registry revocation');
    const bad=t.controlFromJSON(t.controlToJSON(tablet.kind,tablet.fields,tablet.signatures,tablet.extras));bad.extras.body.signatures.approver.fill(0);registry.add(bad);
    if(registry.certificateValidity(tabletID).kind!=='valid')throw new Error('proof variant poisoned a valid ID');
    for (const row of controls.objects) {
      const o = t.controlFromJSON(new TextEncoder().encode(row.json));
      if (t.signedInput(o.kind, o.fields).toString('hex') !== row.input || t.objectID(o.kind, o.fields).toString('hex') !== row.id) throw new Error(row.name);
      for (const [signature, key] of Object.entries(row.signers)) if (!t.verifyControl(o.kind, o.fields, hex(key), o.signatures[signature])) throw new Error(`signature ${row.name}`);
      if (t.controlToJSON(o.kind, o.fields, o.signatures, o.extras).toString('utf8') !== row.json) throw new Error(`JSON ${row.name}`);
    }
    const ed = await (await fetch('/ed25519.json')).json();
    for (const row of ed.cases.signatures) if (t.verifyStrict(hex(row.public), hex(row.signature), hex(row.message)) !== row.valid) throw new Error(`Ed25519 ${row.case}`);
    const vector = await (await fetch('/core.json')).json(), m = Object.fromEntries(Object.entries(vector.material).map(([k,v]) => [k,hex(v)]));
    const writes=vector.writes.map(row=>{
      const w={...row.write};for(const field of ['hub','scope','record','author','certificate','authorization','ref','signature','value'])w[field]=hex(w[field]);
      const dot=d=>({...d,device:hex(d.device)});w.element=w.element===null?null:hex(w.element);w.dot=dot(w.dot);w.context=w.context.map(dot);w.base=w.base.map(dot);
      if(t.sigInput(w).toString('hex')!==row.sig_input||t.writeID(w).toString('hex')!==row.id||!t.verifyWrite(w,m.signing_public))throw new Error(`write ${row.id}`);
      const parsed=t.parseKind1(t.recordPayload(w.record,[w]),w.hub,w.scope);if(!t.verifyWrite(parsed.writes[0],m.signing_public))throw new Error('write JSON');return w;
    });
    for(const scenario of vector.merges){const record=new t.RecordState(m.hub,m.scope,m.record);for(const i of [...scenario.indexes].reverse())record.merge(new t.Entry(writes[i],m.epoch,0n));if(JSON.stringify(record.kept.map(e=>e.id.toString('hex')))!==JSON.stringify(scenario.kept)||record.deleted!==scenario.deleted)throw new Error('merge');}
    const plain = hex(vector.plaintext), key = t.privateKey(m.agreement_private, 'x25519');
    if (t.openInbox(m.hub,m.uid,key,hex(vector.inbox)).toString('hex') !== vector.plaintext) throw new Error('HPKE fixture');
    return {signature: t.sign(t.privateKey(m.signing_seed,'ed25519'), plain).toString('hex'), inbox: t.sealInbox(m.hub,m.uid,m.agreement_public,plain).toString('hex'), material: vector.material, plaintext: vector.plaintext};
  });
  if(process.env.TANDEM_INTEROP_DIR)await writeFile(`${process.env.TANDEM_INTEROP_DIR}/browser.json`,JSON.stringify({signature:artifacts.signature,inbox:artifacts.inbox}));
  const m = Object.fromEntries(Object.entries(artifacts.material).map(([k,v]) => [k,Buffer.from(v,'hex')]));
  expect(node.verifyStrict(m.signing_public,Buffer.from(artifacts.signature,'hex'),Buffer.from(artifacts.plaintext,'hex'))).toBe(true);
  expect(node.openInbox(m.hub,m.uid,node.privateKey(m.agreement_private,'x25519'),Buffer.from(artifacts.inbox,'hex')).toString('hex')).toBe(artifacts.plaintext);
  if(process.env.TANDEM_INTEROP_DIR){
    const senders=await Promise.all((await readdir(process.env.TANDEM_INTEROP_DIR)).filter(name=>name.endsWith('.json')).map(async name=>JSON.parse(await readFile(`${process.env.TANDEM_INTEROP_DIR}/${name}`,'utf8'))));
    await page.evaluate(({senders,material,plaintext})=>{const t=window.t,hex=s=>Uint8Array.from(s.match(/../g),x=>parseInt(x,16)),m=Object.fromEntries(Object.entries(material).map(([k,v])=>[k,hex(v)]));
      for(const sender of senders)if(!t.verifyStrict(m.signing_public,hex(sender.signature),hex(plaintext))||t.openInbox(m.hub,m.uid,t.privateKey(m.agreement_private,'x25519'),hex(sender.inbox)).toString('hex')!==plaintext)throw new Error('cross-SDK browser verification');
    },{senders,material:artifacts.material,plaintext:artifacts.plaintext});
  }
});

test('real IndexedDB: failed preparation rolls back clock and bytes; inbox/cursor/outbox commit together', async ({page}) => {
  const result = await page.evaluate(async () => {
    const t = window.t, journal = await t.BrowserJournal.open('atomic'), installation = await journal.installation();
    const hub = new Uint8Array(32).fill(2), key = new Uint8Array(32).fill(1), epoch = new Uint8Array(32).fill(3);
    const tag = t.collectionTag(key,hub,epoch), body = t.seal(key,hub,tag,new TextEncoder().encode('local'));
    let failed;
    try { await journal.queue('hub',installation,12,dot => { failed=dot; throw new Error('signer failed'); }); } catch {}
    let committed;
    const id = await journal.queue('hub',installation,12,dot => { committed=dot; return {channel:tag,body}; });
    const pending = await journal.pending('hub');
    await journal.apply('hub',{added:[{id:'1',channel:tag,body}],removed:[],cursor:'saved'});
    try { await journal.apply('hub',{added:[{id:'1',channel:t.directoryTag(key,hub),body}],removed:[],cursor:'bad'}); } catch {}
    const result = {sameClock: failed.millis===committed.millis && failed.counter===committed.counter,
      exact: t.b64u(pending[0].body)===t.b64u(body), pending:(await journal.pending('hub')).length,
      cursor:await journal.cursor('hub'), kinds:(await journal.readEvents('hub')).map(e=>e.kind), id};
    journal.close();
    const reopened = await t.BrowserJournal.open('atomic'); result.reopened=await reopened.cursor('hub'); reopened.close(); return result;
  });
  expect(result.sameClock).toBe(true); expect(result.exact).toBe(true); expect(result.pending).toBe(0);
  expect(result.cursor).toBe('saved'); expect(result.reopened).toBe('saved'); expect(result.kinds).toEqual(['queued','received']);
});

test('remembered browser keys survive reload without an extra password', async ({page}) => {
  const keys = () => page.evaluate(async () => { const keychain=await window.t.BrowserKeychain.open('remembered'), keys=await keychain.deviceKeys();
    const pub=window.t.b64u(window.t.publicKey(keys.signing)); keychain.close(); return pub; });
  const first=await keys(); await page.reload(); await page.evaluate(async()=>{window.t=await import('/sdk.js');}); expect(await keys()).toBe(first);
});

test('losing a committed clock requires a new installation, while browser keys survive',async({page})=>{
  const result=await page.evaluate(async()=>{
    const t=window.t,keychain=await t.BrowserKeychain.open('clock-keys'),before=t.b64u(t.publicKey((await keychain.deviceKeys()).signing));
    const journal=await t.BrowserJournal.open('lost-clock'),old=await journal.installation();
    const hub=new Uint8Array(32).fill(2),key=new Uint8Array(32).fill(1),tag=t.collectionTag(key,hub,new Uint8Array(32).fill(3));
    const prepare=()=>({channel:tag,body:t.seal(key,hub,tag,new TextEncoder().encode('clock'))});await journal.queue('hub',old,0,prepare);journal.close();
    await new Promise((resolve,reject)=>{const opening=indexedDB.open('lost-clock');opening.onsuccess=()=>{const db=opening.result,tx=db.transaction('clocks','readwrite');tx.objectStore('clocks').clear();tx.oncomplete=()=>{db.close();resolve();};tx.onabort=()=>reject(tx.error);};});
    const reopened=await t.BrowserJournal.open('lost-clock');let refused=false;try{await reopened.queue('hub',old,0,prepare);}catch{refused=true;}
    const fresh=await reopened.installation(),after=t.b64u(t.publicKey((await keychain.deviceKeys()).signing));reopened.close();keychain.close();
    return{refused,changed:t.b64u(old)!==t.b64u(fresh),sameKeys:before===after};
  });expect(result).toEqual({refused:true,changed:true,sameKeys:true});
});

test('full 1 GiB Argon2id capsule opens in a browser worker', async ({page}) => {
  test.setTimeout(180_000);
  const result = await page.evaluate(async () => {
    const v=await (await fetch('/capsule.json')).json(), hex=s=>Uint8Array.from(s.match(/../g),x=>parseInt(x,16));
    const seed=await window.t.openCapsule(v.password,hex(v.hub_id),hex(v.capsule));
    return window.t.b64u(seed)===window.t.b64u(hex(v.ms));
  });
  expect(result).toBe(true);
});

test('two tabs reserve one installation clock without collisions and one runner hands over on close', async ({page,context}) => {
  const second=await context.newPage(); await second.goto(origin); await second.evaluate(async()=>{window.t=await import('/sdk.js');});
  const prepare=()=>{
    const t=window.t; window.prepareDot=async()=>{
      const journal=await t.BrowserJournal.open('shared'),installation=await journal.installation(); let dot;
      const hub=new Uint8Array(32).fill(2),key=new Uint8Array(32).fill(1),tag=t.collectionTag(key,hub,new Uint8Array(32).fill(3));
      await journal.queue('hub',installation,12,value=>{dot=value;return{channel:tag,body:t.seal(key,hub,tag,new TextEncoder().encode('change'))};});journal.close();return{installation:t.b64u(installation),counter:dot.counter};
    };
  };
  await page.evaluate(prepare);await second.evaluate(prepare);
  const dots=await Promise.all([page.evaluate(()=>window.prepareDot()),second.evaluate(()=>window.prepareDot())]);
  expect(dots[0].installation).toBe(dots[1].installation);expect(new Set(dots.map(x=>x.counter)).size).toBe(2);
  await page.evaluate(async()=>{
    window.ownerJournal=await window.t.BrowserJournal.open('owner');window.ownerAbort=new AbortController();
    window.ownerConnected=new Promise(resolve=>{window.ownerRunning=new window.t.BrowserReplica(window.ownerJournal,'hub').run(async()=>{
      resolve();return{watch:async()=>({async *[Symbol.asyncIterator](){await new Promise(()=>{});}}),changes:async()=>({added:[],removed:[],cursor:'ready'}),close:async()=>{}};
    },window.ownerAbort.signal).catch(()=>{});});await window.ownerConnected;
  });
  await second.evaluate(async()=>{
    window.nextJournal=await window.t.BrowserJournal.open('owner');window.nextAbort=new AbortController();
    window.nextConnected=new Promise(resolve=>{window.nextRunning=new window.t.BrowserReplica(window.nextJournal,'hub').run(async()=>{
      resolve();return{watch:async()=>({async *[Symbol.asyncIterator](){await new Promise(()=>{});}}),changes:async()=>({added:[],removed:[],cursor:'ready'}),close:async()=>{}};
    },window.nextAbort.signal).catch(()=>{});});
  });
  // Actual lock state is the barrier; no timer or repeated status checks.
  const locks=await second.evaluate(async()=>await navigator.locks.query());expect(locks.pending.length).toBe(1);
  await page.close();await second.evaluate(async()=>{await window.nextConnected;window.nextAbort.abort();await window.nextRunning;window.nextJournal.close();});await second.close();
});

test('real JMAP: browser CORS, persistent push, explicit pages, cursor replay and removal', async ({page}) => {
  test.skip(!process.env.TANDEM_JMAP_FIXTURE, 'run sdk/check_browser.py for disposable Stalwart');
  const cfg=JSON.parse(process.env.TANDEM_JMAP_FIXTURE);
  const result=await page.evaluate(async cfg=>{
    const t=window.t, abort=new AbortController();
    const reader=await t.JMAPStore.open({...cfg,pageSize:2}), writer=await t.JMAPStore.open({...cfg,pageSize:2});
    try {
      const stream=await reader.watch(abort.signal), events=stream[Symbol.asyncIterator]();
      let initial=await reader.changes('',abort.signal); while(initial.more) initial=await reader.changes(initial.cursor,abort.signal);
      const hub=new Uint8Array(32).fill(2),key=new Uint8Array(32).fill(1),epoch=new Uint8Array(32).fill(3),tag=t.collectionTag(key,hub,epoch);
      const ids=[]; for(let i=0;i<5;i++) ids.push(await writer.append(tag,t.seal(key,hub,tag,new TextEncoder().encode(String(i))),abort.signal));
      await events.next();
      let cursor='',pages=0,added=[]; do { const batch=await reader.changes(cursor,abort.signal);cursor=batch.cursor;pages++;added.push(...batch.added);if(!batch.more)break; }while(true);
      const a=await reader.changes(initial.cursor,abort.signal),b=await reader.changes(initial.cursor,abort.signal);
      await writer.destroy([ids[0]],abort.signal); const removed=[];
      let batch=await reader.changes(cursor,abort.signal); removed.push(...batch.removed);
      while(batch.more){batch=await reader.changes(batch.cursor,abort.signal);removed.push(...batch.removed);}
      return {count:added.length,pages,replay:a.cursor===b.cursor,removed:removed.includes(ids[0])};
    }finally{abort.abort();await reader.close();await writer.close();}
  },cfg);
  expect(result.count).toBe(5);expect(result.pages).toBeGreaterThanOrEqual(3);expect(result.replay).toBe(true);expect(result.removed).toBe(true);
});

test('real JMAP and IndexedDB: lost acknowledgement recovers without resending the envelope', async ({page}) => {
  test.skip(!process.env.TANDEM_JMAP_FIXTURE,'run sdk/check_browser.py');const cfg=JSON.parse(process.env.TANDEM_JMAP_FIXTURE);
  const result=await page.evaluate(async cfg=>{
    const t=window.t,abort=new AbortController(),journal=await t.BrowserJournal.open('lost-answer'),installation=await journal.installation();
    const hub=new Uint8Array(32).fill(2),key=new Uint8Array(32).fill(1),tag=t.collectionTag(key,hub,new Uint8Array(32).fill(3));
    const id=await journal.queue('hub',installation,10,()=>({channel:tag,body:t.seal(key,hub,tag,new TextEncoder().encode('durable change'))}));
    const sender=await t.JMAPStore.open(cfg);await sender.appendPrepared((await journal.pending('hub'))[0].wire,abort.signal);await sender.close();journal.close();
    const reopened=await t.BrowserJournal.open('lost-answer'),replica=new t.BrowserReplica(reopened,'hub');let appended=0;
    const running=replica.run(async()=>{const store=await t.JMAPStore.open(cfg);return{
      watch:signal=>store.watch(signal),changes:(cursor,signal)=>store.changes(cursor,signal),close:()=>store.close(),
      append:()=>{throw new Error('must use prepared bytes');},appendPrepared:(wire,signal)=>{appended++;return store.appendPrepared(wire,signal);}
    };},abort.signal);void running.catch(()=>{});
    try{for await(const event of replica.events(0,abort.signal)){if(event.kind==='received'&&event.id===id)break;}
      return {pending:(await reopened.pending('hub')).length,appended,cursor:!!await reopened.cursor('hub')};
    }finally{abort.abort();await running.catch(()=>{});reopened.close();}
  },cfg);expect(result).toEqual({pending:0,appended:0,cursor:true});
});
