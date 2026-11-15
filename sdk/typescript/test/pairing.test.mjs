import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';
const vector=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(vector.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
const material=Object.fromEntries(Object.entries(vector.material).map(([k,v])=>[k,Buffer.from(v,'hex')]));
const json=v=>Buffer.from(JSON.stringify(v)),asJSON=o=>JSON.parse(t.controlToJSON(o.kind,o.fields,o.signatures,o.extras));
function fixture(){
  const g=objects.get('genesis'),d=objects.get('delegation'),link={hub:g.fields.hub,genesis:t.objectID(g.kind,g.fields),directoryKey:material.directory_key,
    secret:material.link,token:material.token,participant:d.fields.participant,mail:{host:'mail.example.test',port:993,user:'a',pass:'secret'}};
  const messages=[],seal=(channel,key,body)=>{const env=t.seal(key,link.hub,channel,body,3);return{id:t.b64u(t.sha(env)),channel,body:env};};
  const tag=t.directoryTag(link.directoryKey,link.hub),key=t.directoryKey(link.directoryKey,link.hub);
  for(const name of ['genesis','creator certificate','invitation','alice phone certificate'])messages.push(seal(tag,key,t.controlToJSON(objects.get(name).kind,objects.get(name).fields,objects.get(name).signatures,objects.get(name).extras)));
  const pairingTag=t.pairingTag(link.secret,link.hub,link.token),pairingKey=t.pairingKey(link.secret,link.hub,link.token);
  for(const body of [t.controlToJSON(d.kind,d.fields,d.signatures),json({type:'package',bytes:t.b64u(Buffer.from(vector.derived.package,'hex'))}),json({type:'gate',bytes:t.b64u(Buffer.from(vector.derived.gate,'hex'))})])messages.push(seal(pairingTag,pairingKey,body));
  const registry=t.pairingRegistry(link,messages),keys=t.deviceKeys(),now=d.fields.expires-1;
  return{link,messages,registry,keys,now,seal,pairingTag,pairingKey};
}
function certify(f,p){
  const manager=t.managerKeys(material.management_seed),fields={hub:f.link.hub,genesis:f.link.genesis,request:t.objectID(p.pending.request.kind,p.pending.request.fields),issued:[]};
  const cert=t.controlFromJSON(t.controlToJSON('certificate',fields,{sig:t.signControl('certificate',fields,manager.signing)},{body:p.pending.request}));
  const id=f.registry.add(cert),channel=t.inboxTag(f.link.participant,f.link.hub),body=json({type:'pair-release',certificate:t.b64u(id),delegation:t.b64u(t.objectID(p.pending.delegation.kind,p.pending.delegation.fields)),package:t.b64u(t.sha(p.pending.package)),a:t.b64u(material.gate_secret)});
  const envelope=t.sealInbox(f.link.hub,f.link.participant,t.publicKey(f.keys.agreement),body);
  return{id,cert,message:{id:t.b64u(t.sha(envelope)),channel,body:envelope}};
}
test('native pairing link round-trips; bootstrap requires the exact signed genesis and directory key',()=>{
  const f=fixture(),encoded=t.pairingLinkJSON(f.link);assert.deepEqual(t.parsePairingLink(`liszto://hub#${t.b64u(encoded)}`),f.link);assert.ok(f.registry);
  assert.equal(t.pairingRegistry({...f.link,genesis:t.randomBytes(32)},f.messages),undefined);
  assert.equal(t.pairingRegistry({...f.link,directoryKey:t.randomBytes(32)},f.messages),undefined);
  assert.throws(()=>t.parsePairingLink('https://mail.example.test/#secret'));
});
test('claim binds the new keys to the delegated participant; bad later staged variants do not poison it',()=>{
  const f=fixture(),original=objects.get('delegation'),bad={...original,signatures:{sig:new Uint8Array(64)}};
  f.messages.push(f.seal(f.pairingTag,f.pairingKey,t.controlToJSON(bad.kind,bad.fields,bad.signatures)),f.seal(f.pairingTag,f.pairingKey,json({type:'package',bytes:t.b64u(t.randomBytes(32))})),f.seal(f.pairingTag,f.pairingKey,json({type:'gate',bytes:t.b64u(t.randomBytes(32))})));
  const proposal=t.proposePairing(f.link,f.registry,f.messages,f.keys,'Browser',f.now);assert.ok(proposal);assert.equal(proposal.messages.length,1);
  const creator=objects.get('creator certificate').extras.body.fields.participant,creatorKey=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: creator agreement')),'x25519');
  const claim=JSON.parse(t.openInbox(f.link.hub,creator,creatorKey,proposal.messages[0].body));
  assert.equal(claim.type,'pair-claim');assert.equal(claim.request.sign,t.b64u(t.publicKey(f.keys.signing)));assert.equal(claim.request.participant,t.b64u(f.link.participant));
  assert.equal(claim.request.authority,t.b64u(t.objectID(original.kind,original.fields)));assert.deepEqual(claim.delegation,asJSON(original));
  assert.equal(t.proposePairing(f.link,f.registry,f.messages,f.keys,'Browser',f.now+1),undefined);
  assert.equal(t.proposePairing({...f.link,participant:t.randomBytes(32)},f.registry,f.messages,f.keys,'Browser',f.now),undefined);
});
test('release requires a valid exact certificate and matching gate; returned home key remains a candidate',()=>{
  const f=fixture(),p=t.proposePairing(f.link,f.registry,f.messages,f.keys,'Browser',f.now),saved=t.parsePendingPairing(t.pendingPairingJSON(p.pending));
  assert.equal(t.releasePairing(saved,f.registry,f.messages,f.keys),undefined);
  const c=certify(f,p);assert.equal(f.registry.activity(c.id).kind,'valid');assert.equal(t.releasePairing(saved,f.registry,f.messages,f.keys),undefined);
  const opened=t.releasePairing(saved,f.registry,[...f.messages,c.message],f.keys);assert.ok(opened);assert.deepEqual(opened.certificate.id,Buffer.from(c.id));
  const expected=JSON.parse(Buffer.from(vector.derived.package_plaintext,'hex'));assert.equal(t.b64u(opened.key),expected.k);assert.equal(t.b64u(opened.epoch),expected.home);
  assert.equal(t.releasePairing(saved,f.registry,[c.message],t.deviceKeys()),undefined);
  const modified={...saved,package:t.randomBytes(saved.package.length)};assert.throws(()=>t.releasePairing(modified,f.registry,[c.message],f.keys));
  const home=new t.HomePolicy(f.link.participant,f.registry);home.offerKey(opened.epoch,opened.key);assert.equal(home.key(opened.epoch),undefined);
});
