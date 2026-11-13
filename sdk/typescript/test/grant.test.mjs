import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';
const vector=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(vector.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
function fixture(){
  const registry=new t.Registry(t.pinGenesis(objects.get('genesis')));for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'])registry.add(objects.get(name));
  const owner=registry.certificate(t.objectID('certificate',objects.get('alice phone certificate').fields)),recipient=registry.certificate(t.objectID('certificate',objects.get('creator certificate').fields));
  const signing=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: alice phone signing')),'ed25519'),auth=registry.add(registry.currentSnapshot(owner.participant)),floor=registry.add(registry.currentSnapshot(recipient.participant));
  const key=t.randomBytes(32),base={n:0,keyN:0,commit:t.commit(key),nonce:t.randomBytes(16),members:[{uid:owner.participant,role:'owner',name:'Alice',floor:auth}],deleted:false};let counter=0;
  const write=(statement,scope,ref=new Uint8Array(32))=>t.signWrite({hub:registry.pin.hub,scope,record:t.ROSTER_RECORD,field:'s',op:1,author:owner.participant,certificate:owner.id,authorization:auth,dot:{device:new Uint8Array(8),millis:12,counter:counter++},value:t.statementJSON(statement),element:null,ref,context:[],base:[],signature:new Uint8Array(64)},signing);
  const genesis=write(base,new Uint8Array(16)),epoch=t.writeID(genesis),cid=epoch.subarray(0,16),policy=new t.CollectionPolicy(cid,registry);policy.offerKey(epoch,key);policy.accept(genesis,epoch);
  const {nonce,...rest}=base,shared={...rest,n:1,members:[...rest.members,{uid:recipient.participant,role:'reader',name:'Bob',floor}]},admissionWrite=write(shared,cid,epoch),admission=t.writeID(admissionWrite);policy.accept(admissionWrite,epoch);
  const grant=(extra={})=>{const fields={hub:registry.pin.hub,collection:cid,recipient:recipient.participant,admission,at:admission,epoch,key:t.commit(key),signer:owner.id,auth,...extra};return t.controlFromJSON(t.controlToJSON('grant',fields,{sig:t.signControl('grant',fields,signing)}));};
  return{registry,owner,recipient,policy,grant,key,epoch,cid,admission,write,shared};
}
test('a grant binds the fixed owner, exact admission, recipient and epoch key',()=>{
  const f=fixture();assert.equal(t.grantValidity(f.grant(),f.key,f.recipient.id,f.policy,f.registry).kind,'valid');
  for(const extra of [{admission:t.randomBytes(32)},{epoch:t.randomBytes(32)},{recipient:f.owner.participant},{signer:f.recipient.id}])assert.equal(t.grantValidity(f.grant(extra),f.key,f.recipient.id,f.policy,f.registry).kind,'invalid');
  assert.equal(t.grantValidity(f.grant(),t.randomBytes(32),f.recipient.id,f.policy,f.registry).kind,'invalid');
});
test('only a verified grant can activate a new-epoch draft',()=>{
  const f=fixture(),key=t.randomBytes(32),next={...f.shared,n:2,keyN:1,commit:t.commit(key),prev:t.commit(f.key),snapshot:t.sha(t.inventoryBytes(new Map()))};
  const write=f.write(next,f.cid,f.admission),epoch=t.writeID(write);f.policy.offerKey(epoch,key);f.policy.accept(write,epoch);assert.deepEqual(f.policy.head,f.admission);
  const grant=f.grant({at:epoch,epoch,key:t.commit(key)});assert.equal(t.adoptGrant(grant,t.randomBytes(32),f.recipient.id,f.policy,f.registry).kind,'invalid');assert.deepEqual(f.policy.head,f.admission);
  assert.equal(t.adoptGrant(grant,key,f.recipient.id,f.policy,f.registry).kind,'valid');assert.deepEqual(f.policy.head,epoch);
});
test('an older admission cannot be replayed after removal and re-admission',()=>{
  const f=fixture(),old=f.grant(),key=t.randomBytes(32),next={...f.shared,n:2,keyN:1,commit:t.commit(key),prev:t.commit(f.key),snapshot:t.sha(t.inventoryBytes(new Map())),members:f.shared.members.filter(m=>m.role==='owner')};
  const removed=f.write(next,f.cid,f.admission),epoch=t.writeID(removed);f.policy.offerKey(epoch,key);f.policy.accept(removed,f.epoch);
  assert.equal(t.grantValidity(old,f.key,f.recipient.id,f.policy,f.registry).kind,'invalid');
  const added=f.write({...next,n:3,members:f.shared.members},f.cid,epoch),at=t.writeID(added);f.policy.accept(added,epoch);
  assert.equal(t.grantValidity(old,f.key,f.recipient.id,f.policy,f.registry).kind,'invalid');
  assert.equal(t.grantValidity(f.grant({admission:at,at,epoch,key:t.commit(key)}),key,f.recipient.id,f.policy,f.registry).kind,'valid');
});
