import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';
const vector=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(vector.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
const material=Object.fromEntries(Object.entries(vector.material).map(([k,v])=>[k,Buffer.from(v,'hex')]));
const asJSON=o=>JSON.parse(t.controlToJSON(o.kind,o.fields,o.signatures,o.extras));
function fixture(){
  const g=objects.get('genesis'),d=objects.get('delegation'),pin=t.pinGenesis(g),registry=new t.Registry(pin),directory=material.directory_key;
  const creatorKeys={signing:t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: creator signing')),'ed25519'),agreement:t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: creator agreement')),'x25519')};
  const state=new t.DeviceState(registry,directory,pin.creator,creatorKeys),messages=[];
  const apply=outgoing=>{const rows=outgoing.map(m=>({...m,id:t.b64u(t.sha(m.body))}));messages.push(...rows);state.ingest(rows);};
  const offer=o=>{const channel=t.directoryTag(directory,pin.hub);apply([{channel,body:t.seal(t.directoryKey(directory,pin.hub),pin.hub,channel,t.controlToJSON(o.kind,o.fields,o.signatures,o.extras),3)}]);};
  for(const name of ['genesis','creator certificate','invitation','alice phone certificate'])offer(objects.get(name));
  const link={hub:pin.hub,genesis:pin.genesis,directoryKey:directory,secret:material.link,token:material.token,participant:d.fields.participant,mail:{host:'mail.example.test',port:993,user:'fixture',pass:'fixture'}};
  const pairTag=t.pairingTag(link.secret,link.hub,link.token),pairKey=t.pairingKey(link.secret,link.hub,link.token),pairing=[];
  for(const plain of [t.controlToJSON(d.kind,d.fields,d.signatures),Buffer.from(JSON.stringify({type:'package',bytes:t.b64u(Buffer.from(vector.derived.package,'hex'))})),Buffer.from(JSON.stringify({type:'gate',bytes:t.b64u(Buffer.from(vector.derived.gate,'hex'))}))]){const body=t.seal(pairKey,pin.hub,pairTag,plain,3);pairing.push({id:t.b64u(t.sha(body)),channel:pairTag,body});}
  const keys=t.deviceKeys(),proposal=t.proposePairing(link,registry,pairing,keys,'New browser');assert.ok(proposal);apply(proposal.messages);
  return{state,registry,pin,directory,apply,offer,keys,proposal,link,messages};
}
test('browser admin certifies exact pairing keys before releasing their gate, without changing live admission during preparation',()=>{
  const f=fixture(),claim=f.state.claims()[0];assert.equal(claim.label,'New browser');
  const operation=f.state.prepareApprove(claim.id,material.management_seed),cert=t.unb64u(operation.details.certificate);assert.equal(f.registry.certificate(cert),undefined);
  assert.deepEqual(operation.phases.map(p=>p.name),['publish-admission','release-pairing']);assert.deepEqual(operation.phases[1].requires,[0]);
  assert.equal(f.state.exportable(operation.phases[1].messages[0]),false);
  f.apply(operation.phases[0].messages);assert.equal(f.registry.activity(cert).kind,'valid');assert.equal(f.state.claims().length,0);assert.equal(f.state.exportable(operation.phases[1].messages[0]),true);
  f.apply(operation.phases[1].messages);const release=t.releasePairing(f.proposal.pending,f.registry,f.messages,f.keys);assert.ok(release);assert.deepEqual(release.certificate.id,cert);
  assert.throws(()=>f.state.prepareApprove(claim.id,t.randomBytes(32)),/another hub/);
});
test('a root revocation immediately retires a queued pairing release while preserving the other keychains',()=>{
  const f=fixture(),claim=f.state.claims()[0],op=f.state.prepareApprove(claim.id,material.management_seed),cert=t.unb64u(op.details.certificate);f.apply(op.phases[0].messages);
  const revocation=f.state.prepareRevoke(cert,material.management_seed);assert.equal(f.registry.activity(cert).kind,'valid');f.apply(revocation.phases[0].messages);
  assert.equal(f.registry.activity(cert).kind,'invalid');assert.equal(f.state.exportable(op.phases[1].messages[0]),false);assert.equal(f.state.operationValid(op.kind,op.details),false);assert.equal(f.state.deliveryRetired(op.phases[1].messages[0]),true);
  const source=f.registry.certificate(objects.get('delegation').fields.source);assert.equal(f.registry.activity(source.id).kind,'valid');
});
test('a rejected or participant-cancelled pairing cannot be newly approved',()=>{
  for(const cancellation of [false,true]){const f=fixture(),claim=f.state.claims()[0];
    if(!cancellation){const op=f.state.prepareReject(claim.id,material.management_seed);f.apply(op.phases[0].messages);}
    else {const d=objects.get('delegation'),fields={hub:f.pin.hub,genesis:f.pin.genesis,outcome:t.Outcome.pairingCancelled,subject:t.objectID(d.kind,d.fields),signer:d.fields.source},key=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: alice phone signing')),'ed25519');f.offer(t.controlFromJSON(t.controlToJSON('receipt',fields,{sig:t.signControl('receipt',fields,key)})));}
    assert.equal(f.state.claims().length,0);assert.throws(()=>f.state.prepareApprove(claim.id,material.management_seed),/no valid/);
  }
});
test('password changes publish a signed replacement before deleting only named capsule deliveries',()=>{
  const f=fixture(),box=t.randomBytes(76),fields={hub:f.pin.hub,genesis:f.pin.genesis,sealed:t.sha(box),supersedes:[]},key=t.managerKeys(material.management_seed).signing,old=t.controlFromJSON(t.controlToJSON('capsule',fields,{sig:t.signControl('capsule',fields,key)},{capsule:box}));f.offer(old);assert.equal(f.state.adminCapsules().length,1);
  const replacement=f.state.preparePassword(t.randomBytes(76),material.management_seed);assert.equal(f.state.adminCapsules().length,1);f.apply(replacement.phases[0].messages);
  assert.equal(f.state.adminCapsules().length,1);const phase=replacement.phases[1],prepared=f.state.prepareIntent(phase.intent,()=>{throw new Error('cleanup must not reserve a clock');},{deliveries:f.messages});
  assert.equal(prepared.destroy.length,1);assert.equal(prepared.messages.length,0);
  const bad={...old,signatures:{sig:new Uint8Array(64)}};f.offer(bad);assert.equal(f.state.adminCapsules().length,1);
});

test('missing revocation dependencies hold a staged approval instead of retiring it',()=>{
  const f=fixture(),claim=f.state.claims()[0],op=f.state.prepareApprove(claim.id,material.management_seed),fields={hub:f.pin.hub,genesis:f.pin.genesis,target:t.randomBytes(32),retained:[]},key=t.managerKeys(material.management_seed).signing;
  f.offer(t.controlFromJSON(t.controlToJSON('revocation',fields,{sig:t.signControl('revocation',fields,key)})));
  assert.equal(f.state.authorizationPending,true);assert.equal(f.state.operationValid(op.kind,op.details),true);assert.equal(f.state.deliveryRetired(op.phases[1].messages[0]),false);assert.equal(f.state.exportable(op.phases[1].messages[0]),false);
});
