import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';
const v=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(v.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
function fixture(limits){
  const registry=new t.Registry(t.pinGenesis(objects.get('genesis')));for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'])registry.add(objects.get(name));
  const start=objects.get('alice home epoch'),participant=start.fields.participant,policy=new t.HomePolicy(participant,registry,limits);
  const key=t.sha(Buffer.from('Tandem v2 public control vector: alice home key')),signing=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: alice phone signing')),'ed25519');
  return{registry,start,participant,policy,key,signing,id:t.objectID(start.kind,start.fields)};
}
test('home keys are candidates until a participant-signed start confirms them',()=>{
  const f=fixture({maxKeysPerEpoch:1});f.policy.offerKey(f.id,t.randomBytes(32));assert.equal(f.policy.key(f.id),undefined);
  f.policy.accept(f.start);assert.equal(f.policy.validity(f.id).kind,'valid');f.policy.offerKey(f.id,f.key);assert.deepEqual(f.policy.key(f.id),f.key);assert.deepEqual(f.policy.heads(),[f.id]);
});
test('another participant and the manager cannot start Alice home',()=>{
  const f=fixture(),creator=f.registry.certificate(t.objectID('certificate',objects.get('creator certificate').fields)),snapshot=f.registry.currentSnapshot(creator.participant),auth=f.registry.add(snapshot);
  const fields={...f.start.fields,signer:creator.id,auth},key=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: creator signing')),'ed25519');
  const forged=t.controlFromJSON(t.controlToJSON('home-epoch',fields,{sig:t.signControl('home-epoch',fields,key)}));
  const id=f.policy.accept(forged);f.policy.offerKey(id,f.key);assert.equal(f.policy.validity(id).kind,'invalid');assert.equal(f.policy.key(id),undefined);assert.deepEqual(f.policy.heads(),[]);
});
test('home proof variants cannot replace a valid signature',()=>{
  const f=fixture(),bad=t.controlFromJSON(t.controlToJSON(f.start.kind,f.start.fields,{sig:new Uint8Array(64)}));
  for(const order of [[f.start,bad],[bad,f.start]]){const policy=new t.HomePolicy(f.participant,f.registry);for(const o of order)policy.accept(o);assert.equal(policy.validity(f.id).kind,'valid');assert.equal(t.verifyControl('home-epoch',policy.object(f.id).fields,t.publicKey(f.signing),policy.object(f.id).signatures.sig),true);}
});
test('a held home becomes usable when its registry proofs arrive; returned status is a copy',()=>{
  const f=fixture(),registry=new t.Registry(t.pinGenesis(objects.get('genesis'))),home=new t.HomePolicy(f.participant,registry);home.accept(f.start);home.offerKey(f.id,f.key);
  const before=home.validity(f.id);assert.equal(before.kind,'pending');before.missing[0].fill(0);
  for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'])registry.add(objects.get(name));
  const after=home.validity(f.id);assert.equal(after.kind,'valid');after.kind='invalid';assert.equal(home.validity(f.id).kind,'valid');assert.deepEqual(home.key(f.id),f.key);
});
test('concurrent home rekeys form two heads; old late writes require a fixed inventory',()=>{
  const f=fixture(),empty=t.inventoryBytes(new Map());f.policy.accept(f.start);f.policy.offerKey(f.id,f.key);
  const children=[];
  for(let i=0;i<2;i++){const key=t.randomBytes(32),fields={...f.start.fields,n:1,parents:[f.id],key:t.commit(key),snapshot:t.sha(empty)};
    const object=t.controlFromJSON(t.controlToJSON('home-epoch',fields,{sig:t.signControl('home-epoch',fields,f.signing)})),id=f.policy.accept(object);f.policy.offerKey(id,key);f.policy.addInventory(id,empty);children.push(id);}
  assert.equal(f.policy.heads().length,2);
  const w=t.signWrite({hub:f.registry.pin.hub,scope:t.homeScope(f.registry.pin.hub,f.participant),record:t.namedID('settings:liszto'),field:'theme',op:1,
    author:f.participant,certificate:f.start.fields.signer,authorization:f.start.fields.auth,dot:{device:new Uint8Array(8),millis:12,counter:0},value:Buffer.from('classic'),element:null,ref:f.id,context:[],base:[],signature:new Uint8Array(64)},f.signing);
  assert.equal(f.policy.dataValidity(w,f.id).kind,'invalid');assert.equal(f.policy.dataValidity(w,children[0]).kind,'invalid');
});

test('a home ancestry budget holds the proof instead of invalidating it',()=>{
  const f=fixture({maxDepth:1}),empty=t.inventoryBytes(new Map());f.policy.accept(f.start);
  const fields={...f.start.fields,n:1,parents:[f.id],snapshot:t.sha(empty)};
  const child=t.controlFromJSON(t.controlToJSON('home-epoch',fields,{sig:t.signControl('home-epoch',fields,f.signing)})),id=f.policy.accept(child);
  assert.throws(()=>f.policy.validity(id),t.HomeHoldingLimit);assert.equal(f.policy.validity(f.id).kind,'valid');
});
