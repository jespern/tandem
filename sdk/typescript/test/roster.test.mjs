import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';
const vector=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(vector.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
function fixture(){
  const registry=new t.Registry(t.pinGenesis(objects.get('genesis')));for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'])registry.add(objects.get(name));
  const cert=t.objectID('certificate',objects.get('alice phone certificate').fields),auth=t.objectID('snapshot',objects.get('alice snapshot').fields),me=registry.certificate(cert);
  const key=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: alice phone signing')),'ed25519');assert.deepEqual(t.publicKey(key),me.sign);
  let counter=0;const write=(value,scope,ref=new Uint8Array(32),extra={})=>t.signWrite({hub:registry.pin.hub,scope,record:t.ROSTER_RECORD,field:'s',op:1,
    author:me.participant,certificate:cert,authorization:auth,dot:{device:new Uint8Array(8),millis:12,counter:counter++},value,element:null,ref,context:[],base:[],signature:new Uint8Array(64),...extra},key);
  const epochKey=t.randomBytes(32),statement={n:0,keyN:0,commit:t.commit(epochKey),nonce:t.randomBytes(16),members:[{uid:me.participant,role:'owner',name:'Alice',floor:auth}],deleted:false};
  const genesis=write(t.statementJSON(statement),new Uint8Array(16)),epoch=t.writeID(genesis),cid=epoch.subarray(0,16),policy=new t.CollectionPolicy(cid,registry);
  policy.offerKey(epoch,epochKey);assert.equal(policy.accept(genesis,epoch).kind,'valid');
  return{registry,write,statement,genesis,epoch,epochKey,cid,policy,auth,me};
}
test('genesis pins the owner and key; an untrusted key cannot displace it',()=>{
  const f=fixture();assert.deepEqual(f.policy.owner,f.me.participant);assert.deepEqual(f.policy.key(f.epoch),f.epochKey);
  f.policy.offerKey(f.epoch,t.randomBytes(32));assert.deepEqual(f.policy.key(f.epoch),f.epochKey);
  const round=t.parseStatement(t.statementJSON(f.statement));assert.equal(round.members[0].name,'Alice');
  assert.equal(f.policy.accept({...f.genesis,hub:new Uint8Array(32)},f.epoch).kind,'invalid');
});
test('an authenticated invalid owner successor still proves a fork',()=>{
  const f=fixture(),{nonce,...base}=f.statement;
  const good=f.write(t.statementJSON({...base,n:1}),f.cid,f.epoch);assert.equal(f.policy.accept(good,f.epoch).kind,'valid');
  const invalid=f.write(t.statementJSON({...base,n:3}),f.cid,f.epoch);assert.equal(f.policy.accept(invalid,f.epoch).kind,'invalid');
  assert.equal(f.policy.dead,true);assert.deepEqual(f.policy.head,f.epoch);
});
test('a new epoch stays a draft until the parent channel announces it; old late data is not imported',()=>{
  const f=fixture(),{nonce,...base}=f.statement,newKey=t.randomBytes(32),inventory=t.inventoryBytes(new Map());
  const next=f.write(t.statementJSON({...base,n:1,keyN:1,commit:t.commit(newKey),prev:f.statement.commit,snapshot:t.sha(inventory)}),f.cid,f.epoch),id=t.writeID(next);
  f.policy.offerKey(id,newKey);assert.equal(f.policy.accept(next,id).kind,'valid');assert.deepEqual(f.policy.head,f.epoch);
  assert.equal(f.policy.accept(next,f.epoch).kind,'valid');assert.deepEqual(f.policy.head,id);f.policy.addInventory(id,inventory);
  const late=f.write(Buffer.from('late'),f.cid,f.epoch,{record:t.randomBytes(16),field:'title'});assert.equal(f.policy.dataValidity(late,f.epoch).kind,'invalid');
  assert.equal(f.policy.dataValidity(late,id).kind,'invalid');
});
test('a reader cannot submit data, even with its own valid admitted key',()=>{
  const f=fixture(),{nonce,...base}=f.statement,creator=f.registry.certificate(t.objectID('certificate',objects.get('creator certificate').fields));
  const creatorSnapshot=f.registry.currentSnapshot(creator.participant),floor=f.registry.add(creatorSnapshot);
  const successor=f.write(t.statementJSON({...base,n:1,members:[...base.members,{uid:creator.participant,role:'reader',name:'Bob',floor}]}),f.cid,f.epoch);
  assert.equal(f.policy.accept(successor,f.epoch).kind,'valid');const ref=t.writeID(successor);
  // A valid signature by the wrong admitted role is insufficient for authority.
  const data=f.write(Buffer.from('allowed'),f.cid,ref,{record:t.randomBytes(16),field:'title'});assert.equal(f.policy.dataValidity(data,f.epoch).kind,'valid');
  const creatorKey=t.privateKey(t.sha(Buffer.from('Tandem v2 public control vector: creator signing')),'ed25519');
  const readerWrite=t.signWrite({...data,author:creator.participant,certificate:creator.id,authorization:floor},creatorKey);
  assert.equal(t.verifyWrite(readerWrite,creator.sign),true);assert.deepEqual(f.policy.dataValidity(readerWrite,f.epoch),{kind:'invalid',reason:'not a writer here'});
});

test('candidate budgets still admit a key confirmed by signed genesis',()=>{
  const f=fixture(),policy=new t.CollectionPolicy(f.cid,f.registry,{maxKeysPerEpoch:1,maxCandidateEpochs:1});
  policy.offerKey(f.epoch,t.randomBytes(32));assert.throws(()=>policy.offerKey(f.epoch,t.randomBytes(32)),t.CollectionHoldingLimit);
  assert.equal(policy.accept(f.genesis,f.epoch).kind,'valid');policy.offerKey(f.epoch,f.epochKey);assert.deepEqual(policy.key(f.epoch),f.epochKey);
});
