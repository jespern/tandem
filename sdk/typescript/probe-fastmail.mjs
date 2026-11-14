#!/usr/bin/env node
/** Real Chromium CORS/SSE + optional IMAP IDLE probe. Credentials stay in memory;
 * every mutation is confined to a fresh Tandem-probe mailbox, removed on exit. */
import {parseArgs} from 'node:util';
import {readFile} from 'node:fs/promises';
import {randomUUID} from 'node:crypto';
import {spawn} from 'node:child_process';
import {createInterface} from 'node:readline';
import {once} from 'node:events';
import {chromium} from '@playwright/test';
const {values}=parseArgs({options:{'token-file':{type:'string'},'imap-password-file':{type:'string'},'imap-user':{type:'string'},'timeout-ms':{type:'string',default:'120000'}}});
if(!values['token-file'])throw new Error('--token-file is required');
const timeout=Number(values['timeout-ms']);if(!Number.isSafeInteger(timeout)||timeout<1)throw new Error('invalid timeout');
const token=(await readFile(values['token-file'],'utf8')).trim();if(!token||/\s/.test(token))throw new Error('invalid token file');
const folderName=`Tandem-probe-${randomUUID()}`,authorization=`Bearer ${token}`,core='urn:ietf:params:jmap:core',mail='urn:ietf:params:jmap:mail';
const session=await (await fetch('https://api.fastmail.com/jmap/session',{headers:{Authorization:authorization},signal:AbortSignal.timeout(timeout),redirect:'error'})).json();
const api=new URL(session.apiUrl);if(api.protocol!=='https:'||api.port||api.username||api.password||!(api.hostname==='api.fastmail.com'||api.hostname.endsWith('.api.fastmail.com')))throw new Error('untrusted Fastmail API URL');
const accountId=session.primaryAccounts[mail];
async function call(method,args){
  const r=await fetch(api,{method:'POST',headers:{Authorization:authorization,'Content-Type':'application/json'},signal:AbortSignal.timeout(timeout),redirect:'error',body:JSON.stringify({using:[core,mail],methodCalls:[[method,{accountId,...args},'0']]})});
  if(!r.ok)throw new Error(`JMAP HTTP ${r.status}`);const row=(await r.json()).methodResponses?.[0];
  if(row?.[0]!==method||['notCreated','notUpdated','notDestroyed'].some(k=>row[1][k]&&Object.keys(row[1][k]).length))throw new Error(`${method} refused`);return row[1];
}
let browser,imap;const report={provider:'Fastmail',date:new Date().toISOString(),polling:false};
try{
  browser=await chromium.launch({headless:true});const page=await browser.newPage();page.setDefaultTimeout(timeout);
  const bundle=await readFile(new URL('./dist/browser/tandem.js',import.meta.url));
  await page.route('https://tandem-probe.invalid/**',route=>route.fulfill({contentType:route.request().url().endsWith('sdk.js')?'application/javascript':'text/html',body:route.request().url().endsWith('sdk.js')?bundle:'<!doctype html><title>Tandem provider probe</title>'}));
  await page.goto('https://tandem-probe.invalid/');
  await page.evaluate(async({token,folderName,timeout})=>{
    const t=await import('/sdk.js'),signal=AbortSignal.timeout(timeout),cfg=t.fastmailConfig(token,{folderName,pageSize:2,commandTimeout:timeout});
    const reader=await t.JMAPStore.open(cfg,signal),writer=await t.JMAPStore.open(cfg,signal),events=(await reader.watch(signal))[Symbol.asyncIterator]();let cursor='';
    const scan=async()=>{const added=[],removed=[];let batch;do{batch=await reader.changes(cursor,signal);cursor=batch.cursor;added.push(...batch.added);removed.push(...batch.removed);}while(batch.more);return{added,removed};};await scan();
    window.probe={signal,reader,writer,make:()=>{const hub=t.randomBytes(32),key=t.randomBytes(32),channel=t.collectionTag(key,hub,t.randomBytes(32)),body=t.seal(key,hub,channel,new TextEncoder().encode('Tandem real provider probe'));
      return{channel,body:t.b64u(body),hash:t.b64u(t.sha(body)),wire:btoa(String.fromCharCode(...t.buildMessage(channel,body)))};},
      append:message=>writer.append(message.channel,t.unb64u(message.body),signal),
      wait:async(hash,removedID)=>{while(true){const event=await t.untilAbort(events.next(),signal);if(event.done)throw new Error('push ended');const batch=await scan();
        if(removedID?batch.removed.includes(removedID):batch.added.some(m=>t.b64u(t.sha(m.body))===hash))return true;}},
      replay:async()=>{const a=await reader.changes(cursor,signal),b=await reader.changes(cursor,signal);return a.cursor===b.cursor;},
      close:async()=>{await reader.close();await writer.close();}};
  },{token,folderName,timeout});report.cors=true;
  if(values['imap-password-file']){
    const password=(await readFile(values['imap-password-file'],'utf8')).trim();
    const child=spawn('python3',[new URL('./probe-imap.py',import.meta.url).pathname],{stdio:['pipe','pipe','inherit']});
    const waiting=new Map(),queued=new Map();
    const failure=error=>{for(const waiter of waiting.values())waiter.reject(error);waiting.clear();};
    createInterface({input:child.stdout}).on('line',line=>{const message=JSON.parse(line);if(message.kind==='error'){failure(new Error(`IMAP probe: ${message.error}`));return;}const waiter=waiting.get(message.kind);if(waiter){waiting.delete(message.kind);waiter.resolve(message);}else queued.set(message.kind,message);});
    child.on('error',failure);child.on('exit',code=>failure(new Error(`IMAP probe exited (${code})`)));
    const receive=kind=>{if(queued.has(kind)){const message=queued.get(kind);queued.delete(kind);return Promise.resolve(message);}return new Promise((resolve,reject)=>waiting.set(kind,{resolve,reject}));};
    imap={child,receive,send:message=>child.stdin.write(JSON.stringify(message)+'\n')};
    imap.send({user:values['imap-user']??session.username,password,folder:folderName,timeout:timeout/1000});await receive('ready');
  }
  const message=await page.evaluate(()=>window.probe.make());await page.evaluate(hash=>{window.pushed=window.probe.wait(hash);void window.pushed.catch(()=>{});},message.hash);
  if(imap){imap.send({op:'idle'});await imap.receive('idle-ready');}
  const start=performance.now(),id=await page.evaluate(message=>window.probe.append(message),message);await page.evaluate(()=>window.pushed);report.browserPushMs=Math.round(performance.now()-start);
  if(imap){const pushed=await imap.receive('imap-push');report.jmapToImapMs=Math.round(performance.now()-start);report.jmapToImapExact=pushed.hashes.includes(message.hash);if(!report.jmapToImapExact)throw new Error('JMAP→IMAP bytes differ');}
  report.cursorReplay=await page.evaluate(()=>window.probe.replay());
  if(imap){const message=await page.evaluate(()=>window.probe.make());await page.evaluate(hash=>{window.pushed=window.probe.wait(hash);void window.pushed.catch(()=>{});},message.hash);
    const start=performance.now();imap.send({op:'append',wire:message.wire});await imap.receive('appended');await page.evaluate(()=>window.pushed);report.imapToBrowserMs=Math.round(performance.now()-start);}
  await page.evaluate(id=>{window.removed=window.probe.wait('',id);void window.removed.catch(()=>{});},id);
  await page.evaluate(id=>window.probe.writer.destroy([id],window.probe.signal),id);await page.evaluate(()=>window.removed);report.pushRemoval=true;
  await page.evaluate(()=>window.probe.close());
}finally{
  if(imap){imap.send({op:'close'});imap.child.stdin.end();if(imap.child.exitCode===null)await once(imap.child,'exit');}
  await browser?.close();
  const ids=(await call('Mailbox/query',{filter:{name:folderName}})).ids;
  if(ids.length){const boxes=(await call('Mailbox/get',{ids})).list.filter(box=>box.name===folderName&&box.parentId===null);
    for(const box of boxes){const messages=(await call('Email/query',{filter:{inMailbox:box.id},limit:1000,calculateTotal:true}));if(messages.total>1000)throw new Error('unexpected probe mailbox size');if(messages.ids.length)await call('Email/set',{destroy:messages.ids});await call('Mailbox/set',{destroy:[box.id]});}}
  report.cleanedUp=true;
}
console.log(JSON.stringify(report,null,2));
