import {spawn} from 'node:child_process';
import {createInterface} from 'node:readline';
import {once} from 'node:events';
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

test('browser home, collection and inventory policies verify commitments and owner forks', async ({page}) => {
  const result=await page.evaluate(async()=>{
    const t=window.t,rows=(await(await fetch('/control.json')).json()).objects;
    const objects=new Map(rows.map(row=>[row.name,t.controlFromJSON(new TextEncoder().encode(row.json))]));
    const registry=new t.Registry(t.pinGenesis(objects.get('genesis')));
    for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'])registry.add(objects.get(name));
    const start=objects.get('alice home epoch'),epoch=t.objectID(start.kind,start.fields),home=new t.HomePolicy(start.fields.participant,registry);
    const signing=t.privateKey(t.sha(new TextEncoder().encode('Tandem v2 public control vector: alice phone signing')),'ed25519');
    home.offerKey(epoch,t.sha(new TextEncoder().encode('Tandem v2 public control vector: alice home key')));const before=home.key(epoch)===undefined;
    home.accept(start);const after=home.key(epoch)!==undefined;
    const key=t.randomBytes(32),statement={n:0,keyN:0,commit:t.commit(key),nonce:t.randomBytes(16),members:[{uid:start.fields.participant,role:'owner',name:'Alice',floor:start.fields.auth}],deleted:false};
    const w=t.signWrite({hub:registry.pin.hub,scope:new Uint8Array(16),record:t.ROSTER_RECORD,field:'s',op:1,author:start.fields.participant,certificate:start.fields.signer,authorization:start.fields.auth,
      dot:{device:new Uint8Array(8),millis:12,counter:0},value:t.statementJSON(statement),element:null,ref:new Uint8Array(32),context:[],base:[],signature:new Uint8Array(64)},signing);
    const id=t.writeID(w),collection=new t.CollectionPolicy(id.subarray(0,16),registry);collection.offerKey(id,key);
    const accepted=collection.accept(w,id).kind;const {nonce,...base}=statement;
    const next=n=>t.signWrite({...w,scope:id.subarray(0,16),ref:id,dot:{...w.dot,counter:n},value:t.statementJSON({...base,n})},signing);
    collection.accept(next(1),id);collection.accept(next(3),id);
    const data=t.inventoryBytes(new Map()),assembler=new t.InventoryAssembler(epoch,t.sha(data));
    const fragments=t.inventoryPayloads(data,epoch,2).map(data=>t.parseKind1(data,registry.pin.hub,new Uint8Array(16)));
    assembler.add({...fragments[0],bytes:t.randomBytes(2)});for(const fragment of fragments.reverse())assembler.add(fragment);
    return{before,after,accepted,fork:collection.dead,inventory:t.b64u(assembler.complete)===t.b64u(data)};
  });expect(result).toEqual({before:true,after:true,accepted:'valid',fork:true,inventory:true});
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

test('multi-message device actions commit state, clocks and exact bytes atomically', async ({page,context}) => {
  const second=await context.newPage();await second.goto(origin);await second.evaluate(async()=>{window.t=await import('/sdk.js');});
  const setup=async()=>{
    const t=window.t;window.actions=await t.BrowserJournal.open('actions');window.installation=await window.actions.installation();
    window.hub=new Uint8Array(32).fill(2);window.key=new Uint8Array(32).fill(1);window.tag=t.directoryTag(window.key,window.hub);
  };
  await page.evaluate(setup);await second.evaluate(setup);
  const rollback=await page.evaluate(async()=>{
    const t=window.t;let failed;try{await window.actions.commit('hub',window.installation,12,tx=>{
      failed=[tx.nextDot(),tx.nextDot()];throw new Error('preparation failed');
    });}catch{}
    const before={pending:(await window.actions.pending('hub')).length,state:await window.actions.state('hub')};
    let committed,late;await window.actions.commit('hub',window.installation,12,tx=>{
      committed=[tx.nextDot(),tx.nextDot()];late=tx.nextDot;
      return{state:new Uint8Array([0]),messages:committed.map(dot=>({channel:window.tag,body:t.seal(window.key,window.hub,window.tag,new TextEncoder().encode(JSON.stringify(dot)),3)}))};
    });
    let refused=false;try{late();}catch{refused=true;}
    const body=t.seal(window.key,window.hub,window.tag,new Uint8Array([9]),3);
    try{await window.actions.commit('hub',window.installation,12,tx=>{tx.nextDot();return{state:new Uint8Array([99]),messages:[{channel:window.tag,body},{channel:t.collectionTag(window.key,window.hub,new Uint8Array(32)),body}]};});}catch{}
    const atomic=(await window.actions.state('hub'))[0]===0&&(await window.actions.pending('hub')).length===2;
    return{empty:before.pending===0&&before.state===undefined,same:failed.every((dot,i)=>dot.counter===committed[i].counter),refused,atomic};
  });expect(rollback).toEqual({empty:true,same:true,refused:true,atomic:true});
  const increment=()=>window.actions.commit('hub',window.installation,12,tx=>({state:new Uint8Array([tx.state[0]+1]),messages:[]}));
  await Promise.all([page.evaluate(increment),second.evaluate(increment)]);
  expect(await page.evaluate(async()=>{const n=(await window.actions.state('hub'))[0];window.actions.close();return n;})).toBe(2);
  await second.evaluate(()=>window.actions.close());await second.close();
  await page.reload();await page.evaluate(async()=>{window.t=await import('/sdk.js');});
  expect(await page.evaluate(async()=>{const journal=await window.t.BrowserJournal.open('actions');try{return{state:(await journal.state('hub'))[0],pending:(await journal.pending('hub')).length,events:(await journal.readEvents('hub')).length};}finally{journal.close();}})).toEqual({state:2,pending:2,events:2});
});

test('a no-op journal transaction does not wake the device loop',async({page})=>{
  expect(await page.evaluate(async()=>{
    const journal=await window.t.BrowserJournal.open('no-op'),installation=await journal.installation();let woke=false;
    const changed=journal.eventsChanged();void changed.then(()=>{woke=true;});
    await journal.commit('hub',installation,12,()=>({messages:[]}));await Promise.resolve();
    const result=woke;journal.close();return result;
  })).toBe(false);
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


function nativeDriver(config){
  const driver=spawn(config.binary,[],{stdio:['pipe','pipe','inherit']}),lines=createInterface({input:driver.stdout})[Symbol.asyncIterator]();
  return{
    command:async(op,args={})=>{driver.stdin.write(JSON.stringify({op,...args})+'\n');const line=await lines.next();if(line.done)throw new Error('native driver ended');const result=JSON.parse(line.value);if(!result.ok)throw new Error(`native ${op}: ${result.error}`);return result;},
    close:async()=>{driver.stdin.end();if(driver.exitCode===null)await once(driver,'exit');}
  };
}

test('native Swift approves a browser claim over IMAP; browser reload receives the exact gated release through JMAP push',async({page})=>{
  test.skip(!process.env.TANDEM_NATIVE_FIXTURE,'run sdk/check_browser.py --native');
  test.setTimeout(90_000);
  const native=JSON.parse(process.env.TANDEM_NATIVE_FIXTURE),cfg=JSON.parse(process.env.TANDEM_JMAP_FIXTURE);
  const driver=nativeDriver(native),command=driver.command;
  try{
    await command('hub',native);await command('found',{label:'Native Mac'});
    const cid=(await command('createList',{name:'Native notes'})).list,record=node.b64u(node.randomBytes(16));
    await command('set',{list:cid,record,field:'title',value:node.b64u(Buffer.from('Created on Mac'))});
    const {link}=await command('startPairing',{label:'Browser'});
    const staged=await page.evaluate(async({cfg,link})=>{
      const t=window.t,parsed=t.parsePairingLink(link),keychain=await t.BrowserKeychain.open('native-pair-keys'),keys=await keychain.deviceKeys();
      const store=await t.JMAPStore.open(cfg),abort=new AbortController(),messages=[];let cursor='';
      try{let batch;do{batch=await store.changes(cursor,abort.signal);cursor=batch.cursor;messages.push(...batch.added);}while(batch.more);}finally{await store.close();}
      const registry=t.pairingRegistry(parsed,messages),proposal=t.proposePairing(parsed,registry,messages,keys,'Browser');if(!proposal)throw new Error('native pairing not readable');
      // Secrets are wrapped before any exact claim bytes can leave the journal.
      await keychain.save('native-pending',t.pendingPairingJSON(proposal.pending));keychain.close();
      const journal=await t.BrowserJournal.open('native-pair'),installation=await journal.installation(),hub=t.b64u(parsed.hub);
      await journal.commit(hub,installation,Date.now(),()=>({state:t.objectID('request',proposal.pending.request.fields),messages:proposal.messages}));
      window.nativeAbort=new AbortController();window.nativeReplica=new t.BrowserReplica(journal,hub);window.nativeJournal=journal;
      window.nativeRun=window.nativeReplica.run(()=>t.JMAPStore.open(cfg),window.nativeAbort.signal);void window.nativeRun.catch(()=>{});
      const pending=new Set(proposal.messages.map(message=>message.id));
      for await(const event of window.nativeReplica.events(0,window.nativeAbort.signal)){if(event.kind==='received')pending.delete(event.id);if(!pending.size)break;}
      return{confirmed:(await journal.pending(hub)).length===0,request:t.b64u(t.objectID('request',proposal.pending.request.fields))};
    },{cfg,link});expect(staged.confirmed).toBe(true);
    // Page destruction releases the old Web Lock/SSE; no explicit graceful shutdown.
    await page.reload();await page.evaluate(async()=>{window.t=await import('/sdk.js');});
    await page.evaluate(async cfg=>{
      const t=window.t,keychain=await t.BrowserKeychain.open('native-pair-keys'),keys=await keychain.deviceKeys(),pending=t.parsePendingPairing(await keychain.get('native-pending'));keychain.close();
      const journal=await t.BrowserJournal.open('native-pair'),hub=t.b64u(pending.link.hub),abort=new AbortController(),replica=new t.BrowserReplica(journal,hub);
      window.nativeJournal=journal;window.nativeAbort=abort;window.nativeReplica=replica;window.nativeRun=replica.run(()=>t.JMAPStore.open(cfg),abort.signal);void window.nativeRun.catch(()=>{});
      window.nativeReleased=(async()=>{
        const messages=[];for await(const event of replica.events(0,abort.signal)){
          if(event.kind==='removed')continue;messages.push(event);
          const registry=t.pairingRegistry(pending.link,messages);if(!registry)continue;
          const released=t.releasePairing(pending,registry,messages,keys);if(!released)continue;
          const home=new t.HomePolicy(pending.link.participant,registry);home.offerKey(released.epoch,released.key);
          const tag=t.homeTag(released.key,pending.link.hub,pending.link.participant,released.epoch);
          for(const message of messages)if(message.channel===tag){try{const opened=t.openEnvelope(released.key,pending.link.hub,tag,message.body);if(opened.kind===3)home.accept(t.controlFromJSON(opened.plaintext,'home-epoch'));}catch{}}
          if(!home.key(released.epoch))continue;
          const state=new t.DeviceState(registry,pending.link.directoryKey,pending.link.participant,keys);state.ingest(messages);state.offerHomeKey(released.epoch,released.key);window.nativeState=state;window.nativeHub=hub;
          return{lists:state.lists().map(row=>row.name),certificate:t.b64u(released.certificate.id),request:t.b64u(released.certificate.requestID),participant:t.b64u(released.certificate.participant),confirmedHome:home.heads().length>0};
        }
        throw new Error('browser stopped before release');
      })();void window.nativeReleased.catch(()=>{});
    },cfg);
    await command('sync');const claims=(await command('claims')).claims;expect(claims).toHaveLength(1);expect(claims[0].id).toBe(staged.request);expect(claims[0].label).toBe('Browser');
    await command('approve',{claim:claims[0].id});
    const release=await page.evaluate(()=>window.nativeReleased);expect(release.request).toBe(staged.request);expect(release.confirmedHome).toBe(true);expect(release.participant).toBe((await command('me')).participant);expect(release.lists).toEqual(['Native notes']);
    const edit=await page.evaluate(async({cid,record})=>{
      const t=window.t,journal=window.nativeJournal,hub=window.nativeHub,state=window.nativeState;
      const installation=await journal.installation(),ids=await journal.commit(hub,installation,Date.now(),tx=>({messages:state.prepareField(t.unb64u(cid),t.unb64u(record),[{field:'title',op:1,value:new TextEncoder().encode('Edited in browser')}],()=>tx.nextDot())}));
      const pending=new Set(ids);for await(const event of window.nativeReplica.events(0,window.nativeAbort.signal)){if(event.kind!=='removed')state.ingest([event]);if(event.kind==='received')pending.delete(event.id);if(!pending.size)break;}
      return new TextDecoder().decode(state.record(t.unb64u(cid),t.unb64u(record)).value('title'));
    },{cid,record});expect(edit).toBe('Edited in browser');
    await command('sync');expect((await command('items',{list:cid})).items.find(item=>item.record===record).values.title).toBe(node.b64u(Buffer.from('Edited in browser')));
    await command('set',{list:cid,record,field:'title',value:node.b64u(Buffer.from('Pushed from native'))});
    const pushed=await page.evaluate(async({cid,record})=>{
      const t=window.t;for await(const event of window.nativeReplica.events(0,window.nativeAbort.signal)){
        if(event.kind==='removed')continue;window.nativeState.ingest([event]);const title=window.nativeState.record(t.unb64u(cid),t.unb64u(record))?.value('title');if(title&&new TextDecoder().decode(title)==='Pushed from native')return true;
      }return false;
    },{cid,record});expect(pushed).toBe(true);
  }finally{
    await page.evaluate(async()=>{window.nativeAbort?.abort();await window.nativeRun?.catch(()=>{});window.nativeJournal?.close();});
    await driver.close();
  }
});


test('BrowserDevice pairs, restores offline edits, and refuses a revocation arriving between replay and commit',async({page})=>{
  test.skip(!process.env.TANDEM_NATIVE_FIXTURE,'run sdk/check_browser.py --native');test.setTimeout(90_000);
  const native=JSON.parse(process.env.TANDEM_NATIVE_FIXTURE),cfg=JSON.parse(process.env.TANDEM_JMAP_FIXTURE),driver=nativeDriver(native),command=driver.command;
  try{
    await command('hub',native);await command('found',{label:'Mac controller fixture'});
    const cid=(await command('createList',{name:'Controller notes'})).list,record=node.b64u(node.randomBytes(16));await command('set',{list:cid,record,field:'title',value:node.b64u(Buffer.from('Native original'))});
    const {link}=await command('startPairing',{label:'Browser controller'}),hub=node.parsePairingLink(link).hub;
    const install=async({cfg,link,hub,resume})=>{
      const t=window.t;window.controllerJournal=await t.BrowserJournal.open('controller');window.controllerKeys=await t.BrowserKeychain.open('controller-keys');
      window.controllerDevice=resume?await t.BrowserDevice.resume(t.unb64u(hub),window.controllerJournal,window.controllerKeys):await t.BrowserDevice.pair(link,'Browser controller',window.controllerJournal,window.controllerKeys);
      window.controllerHub=hub;window.controllerAbort=new AbortController();window.controllerRun=window.controllerDevice.run(()=>t.JMAPStore.open(cfg),window.controllerAbort.signal);void window.controllerRun.catch(()=>{});
      window.controllerReady=(async()=>{for await(const update of window.controllerDevice.updates(window.controllerAbort.signal)){if(update.status==='ready')return true;}return false;})();void window.controllerReady.catch(()=>{});
      window.waitControllerSends=async()=>{while(true){const changed=window.controllerJournal.eventsChanged();if(!(await window.controllerJournal.pending(hub)).length)return;await changed;}};
    };
    await page.evaluate(install,{cfg,link,hub:node.b64u(hub),resume:false});
    await page.evaluate(async()=>{for await(const update of window.controllerDevice.updates(window.controllerAbort.signal)){if(update.status==='approval')break;}await window.waitControllerSends();});
    await page.reload();await page.evaluate(async()=>{window.t=await import('/sdk.js');});await page.evaluate(install,{cfg,hub:node.b64u(hub),resume:true});
    await command('sync');const claims=(await command('claims')).claims;expect(claims).toHaveLength(1);await command('approve',{claim:claims[0].id});
    expect(await page.evaluate(()=>window.controllerReady)).toBe(true);
    const certificate=await page.evaluate(()=>window.t.b64u(window.controllerDevice.state.certificate.id));
    const created=await page.evaluate(async()=>{const id=await window.controllerDevice.createList('Browser made');await window.waitControllerSends();return window.t.b64u(id);});await command('sync');
    expect((await command('lists')).lists.find(list=>list.id===created).name).toBe('Browser made');

    const offline=await page.evaluate(async({cid,record})=>{
      const t=window.t;window.controllerAbort.abort();await window.controllerRun;
      await window.controllerDevice.writeFields(t.unb64u(cid),t.unb64u(record),[{field:'title',op:1,value:new TextEncoder().encode('Saved offline')}]);
      return{value:new TextDecoder().decode(window.controllerDevice.state.record(t.unb64u(cid),t.unb64u(record)).value('title')),pending:(await window.controllerJournal.pending(window.controllerHub)).length};
    },{cid,record});expect(offline.value).toBe('Saved offline');expect(offline.pending).toBeGreaterThan(0);
    await page.reload();await page.evaluate(async()=>{window.t=await import('/sdk.js');});await page.evaluate(install,{cfg,hub:node.b64u(hub),resume:true});
    await page.evaluate(async()=>{await window.controllerReady;await window.waitControllerSends();});await command('sync');
    expect((await command('items',{list:cid})).items.find(item=>item.record===record).values.title).toBe(node.b64u(Buffer.from('Saved offline')));
    await page.evaluate(async()=>{window.controllerAbort.abort();await window.controllerRun;});
    await command('revoke',{certificate});
    const refusal=await page.evaluate(async({cfg,link,cid,record,certificate})=>{
      const t=window.t,parsed=t.parsePairingLink(link),store=await t.JMAPStore.open(cfg),abort=new AbortController(),messages=[];let cursor='';
      try{let batch;do{batch=await store.changes(cursor,abort.signal);cursor=batch.cursor;messages.push(...batch.added);}while(batch.more);}finally{await store.close();}
      const tag=t.directoryTag(parsed.directoryKey,parsed.hub),key=t.directoryKey(parsed.directoryKey,parsed.hub);
      const cut=messages.find(message=>{if(message.channel!==tag)return false;try{const o=t.controlFromJSON(t.openEnvelope(key,parsed.hub,tag,message.body).plaintext);return o.kind==='revocation'&&t.b64u(o.fields.target)===certificate;}catch{return false;}});if(!cut)throw new Error('native revocation not found');
      const journal=window.controllerJournal,original=journal.commit.bind(journal);let injected=false;
      journal.commit=async(...args)=>{if(!injected){injected=true;await journal.apply(window.controllerHub,{added:[cut],removed:[],cursor:await journal.cursor(window.controllerHub)});}return original(...args);};
      let error;try{await window.controllerDevice.writeFields(t.unb64u(cid),t.unb64u(record),[{field:'title',op:1,value:new TextEncoder().encode('Must not leave')}]);}catch(failure){error=failure.message;}
      journal.commit=original;
      return{injected,refused:!!error,pending:(await journal.pending(window.controllerHub)).length,status:window.controllerDevice.update.status,value:new TextDecoder().decode(window.controllerDevice.state.record(t.unb64u(cid),t.unb64u(record)).value('title'))};
    },{cfg,link,cid,record,certificate});expect(refusal).toEqual({injected:true,refused:true,pending:0,status:'revoked',value:'Saved offline'});
  }finally{
    await page.evaluate(async()=>{window.controllerAbort?.abort();await window.controllerDevice?.close();window.controllerJournal?.close();window.controllerKeys?.close();});await driver.close();
  }
});


test('BrowserDevice accepts native grants and enforces reader permissions',async({page})=>{
  test.skip(!process.env.TANDEM_NATIVE_FIXTURE,'run sdk/check_browser.py --native');test.setTimeout(90_000);
  const native=JSON.parse(process.env.TANDEM_NATIVE_FIXTURE),cfg=JSON.parse(process.env.TANDEM_JMAP_FIXTURE),owner=nativeDriver(native),phone=nativeDriver(native);
  try{
    await owner.command('hub',native);await owner.command('found',{label:'Owner Mac'});await phone.command('hub',native);
    await phone.command('join',{link:(await owner.command('invite',{label:'Alice'})).link,label:'Alice phone'});
    const participant=(await phone.command('me')).participant,link=(await phone.command('startPairing',{label:'Alice browser'})).link,hub=node.b64u(node.parsePairingLink(link).hub);
    await page.evaluate(async({cfg,link,hub})=>{
      const t=window.t;window.sharingJournal=await t.BrowserJournal.open('sharing');window.sharingKeys=await t.BrowserKeychain.open('sharing-keys');window.sharingDevice=await t.BrowserDevice.pair(link,'Alice browser',window.sharingJournal,window.sharingKeys);
      window.sharingAbort=new AbortController();window.sharingRun=window.sharingDevice.run(()=>t.JMAPStore.open(cfg),window.sharingAbort.signal);void window.sharingRun.catch(()=>{});
      window.waitSharing=async predicate=>{for await(const _ of window.sharingDevice.updates(window.sharingAbort.signal)){if(predicate(window.sharingDevice.state))return;}};
      window.waitSharingSends=async()=>{while(true){const changed=window.sharingJournal.eventsChanged();if(!(await window.sharingJournal.pending(hub)).length)return;await changed;}};
      for await(const update of window.sharingDevice.updates(window.sharingAbort.signal)){if(update.status==='approval')break;}await window.waitSharingSends();
    },{cfg,link,hub});
    await owner.command('sync');const claims=(await owner.command('claims')).claims;expect(claims).toHaveLength(1);await owner.command('approve',{claim:claims[0].id});
    await page.evaluate(async()=>{for await(const update of window.sharingDevice.updates(window.sharingAbort.signal)){if(update.status==='ready')return;}});
    for(const role of ['reader','writer']){
      const cid=(await owner.command('createList',{name:`Shared ${role}`})).list,record=node.b64u(node.randomBytes(16));
      await owner.command('set',{list:cid,record,field:'title',value:node.b64u(Buffer.from('From owner'))});await owner.command('share',{list:cid,participant,role});
      const answer=await page.evaluate(async({cid,record})=>{
        const t=window.t,id=t.unb64u(cid);await window.waitSharing(state=>state?.offers().some(offer=>t.b64u(offer.collection)===cid));
        const before=window.sharingDevice.state.lists().some(list=>t.b64u(list.id)===cid);await window.sharingDevice.accept(id);await window.waitSharingSends();
        let refused=false;try{await window.sharingDevice.writeFields(id,t.unb64u(record),[{field:'title',op:1,value:new TextEncoder().encode('From browser')}]);await window.waitSharingSends();}catch{refused=true;}
        return{before,following:window.sharingDevice.state.lists().some(list=>t.b64u(list.id)===cid),role:window.sharingDevice.state.role(id),refused};
      },{cid,record});expect(answer).toEqual({before:false,following:true,role,refused:role==='reader'});
      await phone.command('sync');expect((await phone.command('lists')).lists.some(list=>list.id===cid)).toBe(true);
      await owner.command('sync');expect((await owner.command('items',{list:cid})).items.find(item=>item.record===record).values.title).toBe(node.b64u(Buffer.from(role==='reader'?'From owner':'From browser')));
      if(role==='writer'){
        const previous=await page.evaluate(cid=>window.t.b64u(window.sharingDevice.state.collectionStatus(window.t.unb64u(cid)).epoch),cid);await owner.command('rekey',{list:cid});
        await page.evaluate(async({cid,record,previous})=>{
          const t=window.t,id=t.unb64u(cid);await window.waitSharing(state=>{const status=state?.collectionStatus(id);return status?.ready&&t.b64u(status.epoch)!==previous;});
          await window.sharingDevice.writeFields(id,t.unb64u(record),[{field:'title',op:1,value:new TextEncoder().encode('After native rekey')}]);await window.waitSharingSends();
        },{cid,record,previous});
        await owner.command('sync');expect((await owner.command('items',{list:cid})).items.find(item=>item.record===record).values.title).toBe(node.b64u(Buffer.from('After native rekey')));
      }

    }
  }finally{await page.evaluate(async()=>{window.sharingAbort?.abort();await window.sharingDevice?.close();window.sharingJournal?.close();window.sharingKeys?.close();});await phone.close();await owner.close();}
});
