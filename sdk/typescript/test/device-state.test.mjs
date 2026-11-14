import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';
const vector=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(vector.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
const seed=label=>t.sha(Buffer.from('Tandem v2 public control vector: '+label));
function fixture(){
  const pin=t.pinGenesis(objects.get('genesis')),registry=new t.Registry(pin),participant=objects.get('alice home epoch').fields.participant,directory=Buffer.from(vector.material.directory_key,'hex');
  const keys={signing:t.privateKey(seed('alice phone signing'),'ed25519'),agreement:t.privateKey(seed('alice phone agreement'),'x25519')};
  const messages=[],envelope=(key,channel,plain,kind=1)=>{const body=t.seal(key,pin.hub,channel,plain,kind),message={id:t.b64u(t.sha(body)),channel,body};messages.push(message);return message;};
  for(const name of ['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot']){const o=objects.get(name);registry.add(o);envelope(t.directoryKey(directory,pin.hub),t.directoryTag(directory,pin.hub),t.controlToJSON(o.kind,o.fields,o.signatures,o.extras),3);}
  const home=objects.get('alice home epoch'),homeID=t.objectID(home.kind,home.fields),homeKey=seed('alice home key'),state=new t.DeviceState(registry,directory,participant,keys);
  state.offerHomeKey(homeID,homeKey);envelope(homeKey,t.homeTag(homeKey,pin.hub,participant,homeID),t.controlToJSON(home.kind,home.fields,home.signatures),3);state.ingest(messages);
  let counter=0;const reserve=()=>({device:new Uint8Array(8),millis:12,counter:counter++});
  const apply=outgoing=>{const rows=outgoing.map(message=>({...message,id:t.b64u(t.sha(message.body))}));messages.push(...rows);state.ingest(rows);};
  const collectionKey=t.randomBytes(32),statement={n:0,keyN:0,commit:t.commit(collectionKey),nonce:t.randomBytes(16),members:[{uid:participant,role:'owner',name:'Alice',floor:home.fields.auth}],deleted:false};
  const write=(value,scope,ref=new Uint8Array(32),extra={})=>t.signWrite({hub:pin.hub,scope,record:t.ROSTER_RECORD,field:'s',op:1,author:participant,certificate:home.fields.signer,authorization:home.fields.auth,dot:reserve(),value,element:null,ref,context:[],base:[],signature:new Uint8Array(64),...extra},keys.signing);
  const genesis=write(t.statementJSON(statement),new Uint8Array(16)),epoch=t.writeID(genesis),cid=epoch.subarray(0,16),tag=t.collectionTag(collectionKey,pin.hub,epoch);
  apply(state.prepareHome('membership:'+t.b64u(cid),[{field:'owner',op:1,value:participant},{field:'role',op:1,value:Buffer.from('owner')},{field:'keys',op:2,element:Buffer.from(JSON.stringify({epoch:t.b64u(epoch),k:t.b64u(collectionKey)}))}],reserve));
  envelope(collectionKey,tag,t.recordPayload(t.ROSTER_RECORD,[genesis]));state.ingest(messages);
  apply(state.prepareField(cid,t.META_RECORD,[{field:'name',op:1,value:Buffer.from('Notes')}],reserve));
  return{state,pin,registry,participant,directory,keys,messages,apply,reserve,write,statement,epoch,cid,tag,collectionKey,homeID,homeKey,envelope};
}
test('a paired device reconstructs the same home and list from encrypted envelopes in reverse delivery order',()=>{
  const f=fixture();assert.equal(f.state.homeReady,true);assert.equal(f.state.lists()[0].name,'Notes');
  const restored=new t.DeviceState(new t.Registry(f.pin),f.directory,f.participant,f.keys);restored.offerHomeKey(f.homeID,f.homeKey);
  for(const message of [...f.messages].reverse())restored.ingest([message]);
  assert.equal(restored.homeReady,true);assert.deepEqual(restored.lists(),f.state.lists());
  const record=restored.record(f.cid,t.META_RECORD);record.value('name').fill(0);assert.equal(restored.lists()[0].name,'Notes');
});
test('write preparation preserves set semantics and signs real causal edits',()=>{
  const f=fixture(),record=t.randomBytes(16);
  f.apply(f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('One')},{field:'tags',op:2,element:Buffer.from('work')}],f.reserve));
  f.apply(f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Two')},{field:'tags',op:3,element:Buffer.from('work')}],f.reserve));
  assert.equal(f.state.record(f.cid,record).value('title').toString(),'Two');assert.deepEqual(f.state.record(f.cid,record).elements('tags'),[]);
  assert.deepEqual(f.state.records(f.cid),[record]);
  f.apply(f.state.prepareField(f.cid,record,[{field:'',op:4}],f.reserve));assert.deepEqual(f.state.records(f.cid),[]);
  assert.throws(()=>f.state.prepareField(f.cid,t.ROSTER_RECORD,[{field:'s',op:1,value:Buffer.from('invalid')}],f.reserve));
  assert.throws(()=>f.state.prepareHome('settings:liszto',[{field:'_name',op:1,value:Buffer.from('membership:fake')}],f.reserve));
});
test('a reverse-ordered roster chain in one envelope reaches its last statement',()=>{
  const f=fixture(),{nonce,...base}=f.statement,chain=[];let parent=f.epoch;
  for(let n=1;n<=5;n++){const w=f.write(t.statementJSON({...base,n}),f.cid,parent);chain.push(w);parent=t.writeID(w);}
  f.envelope(f.collectionKey,f.tag,t.recordPayload(t.ROSTER_RECORD,chain.reverse()));f.state.ingest(f.messages);
  const record=t.randomBytes(16),messages=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Current')}],f.reserve);
  const data=messages.find(m=>m.channel===f.tag),parsed=t.parseKind1(t.openEnvelope(f.collectionKey,f.pin.hub,f.tag,data.body).plaintext,f.pin.hub,f.cid);
  assert.deepEqual(parsed.writes[0].ref,parent);
});
test('a verified revocation prevents further writes without losing the cached list',()=>{
  const f=fixture(),target=f.state.certificate.id,manager=t.managerKeys(Buffer.from(vector.material.management_seed,'hex'));
  const fields={hub:f.pin.hub,genesis:f.pin.genesis,target,retained:[]},o=t.controlFromJSON(t.controlToJSON('revocation',fields,{sig:t.signControl('revocation',fields,manager.signing)}));
  f.envelope(t.directoryKey(f.directory,f.pin.hub),t.directoryTag(f.directory,f.pin.hub),t.controlToJSON(o.kind,o.fields,o.signatures),3);f.state.ingest(f.messages);
  assert.equal(f.state.certificate,undefined);assert.equal(f.state.lists()[0].name,'Notes');assert.throws(()=>f.state.prepareField(f.cid,t.randomBytes(16),[{field:'title',op:1,value:Buffer.from('Forbidden')}],f.reserve));
});

test('creating a list journals its membership, signed genesis and metadata as one prepared action',()=>{
  const f=fixture(),created=f.state.prepareCreateList('New list',f.reserve);assert.equal(f.state.lists().length,1);
  f.apply(created.messages);assert.equal(f.state.lists().find(row=>t.b64u(row.id)===t.b64u(created.collection)).name,'New list');
  const replay=new t.DeviceState(new t.Registry(f.pin),f.directory,f.participant,f.keys);replay.offerHomeKey(f.homeID,f.homeKey);replay.ingest([...f.messages].reverse());assert.deepEqual(replay.lists(),f.state.lists());
});

test('a cut to another device holds home writes even while this device is still admitted',()=>{
  const f=fixture(),tablet=t.objectID('certificate',objects.get('alice tablet certificate').fields),phone=f.state.certificate.id;
  const manager=t.managerKeys(Buffer.from(vector.material.management_seed,'hex')),fields={hub:f.pin.hub,genesis:f.pin.genesis,target:tablet,retained:[phone,tablet].sort(Buffer.compare)};
  const o=t.controlFromJSON(t.controlToJSON('revocation',fields,{sig:t.signControl('revocation',fields,manager.signing)}));
  f.envelope(t.directoryKey(f.directory,f.pin.hub),t.directoryTag(f.directory,f.pin.hub),t.controlToJSON(o.kind,o.fields,o.signatures),3);f.state.ingest(f.messages);
  assert.ok(f.state.certificate);assert.equal(f.state.homeReady,false);assert.throws(()=>f.state.prepareHome('settings:liszto',[{field:'theme',op:1,value:Buffer.from('new')}],f.reserve),/home key must rotate/);
});

test('a home rotation carries fixed state, ancestry and inventory before announcing or delivering keys',()=>{
  const f=fixture();f.apply(f.state.prepareHome('settings:liszto',[{field:'theme',op:1,value:Buffer.from('classic')}],f.reserve));
  const operation=f.state.prepareHomeRotation(),epoch=t.unb64u(operation.details.epoch),key=t.unb64u(operation.details.key);
  assert.equal(operation.kind,'home-rotation');assert.deepEqual(operation.phases.map(p=>p.name),['upload-home-inventory','announce-home','deliver-home-keys']);
  assert.deepEqual(f.state.homeEpochs,[f.homeID]);
  f.state.offerHomeKey(epoch,key);f.apply(operation.phases[0].messages);assert.deepEqual(f.state.homeEpochs,[f.homeID]);
  f.apply(operation.phases[1].messages);assert.deepEqual(f.state.homeEpochs,[epoch]);assert.equal(f.state.homeRecord('settings:liszto').value('theme').toString(),'classic');
  f.apply(operation.phases[2].messages);assert.equal(f.state.homeReady,true);
  const restored=new t.DeviceState(new t.Registry(f.pin),f.directory,f.participant,f.keys);restored.offerHomeKey(f.homeID,f.homeKey,true);restored.ingest([...f.messages].reverse());assert.deepEqual(restored.homeEpochs,[epoch]);assert.equal(restored.homeRecord('settings:liszto').value('theme').toString(),'classic');
});

test('collection rotation stages its note and complete snapshot before activating the epoch',()=>{
  const f=fixture(),record=t.randomBytes(16);f.apply(f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Carried')}],f.reserve));
  const op=f.state.prepareCollectionRotation(f.cid,f.reserve),epoch=t.unb64u(op.details.epoch);assert.equal(op.kind,'collection-rotation');
  f.apply(op.phases[0].messages);assert.equal(f.state.collectionStatus(f.cid).epoch.toString('hex'),f.epoch.toString('hex'));
  f.apply(op.phases[1].messages);assert.equal(f.state.collectionStatus(f.cid).epoch.toString('hex'),f.epoch.toString('hex'));
  f.apply(op.phases[2].messages);assert.equal(f.state.collectionStatus(f.cid).epoch.toString('hex'),epoch.toString('hex'));assert.equal(f.state.record(f.cid,record).value('title').toString(),'Carried');
  for(const phase of op.phases.slice(3)){if(phase.intent){const prepared=f.state.prepareIntent(phase.intent,f.reserve,{deliveries:f.messages});phase.messages=prepared.messages;}f.apply(phase.messages);}
  assert.equal(f.state.homeRecord('membership:'+t.b64u(f.cid)).value('flight').toString(),'null');
  assert.equal(f.state.collectionStatus(f.cid).ready,true);
});

test('this installation re-signs its own late edit with its original dot and causal base',()=>{
  const f=fixture(),record=t.randomBytes(16);f.apply(f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Before')}],f.reserve));
  const rotation=f.state.prepareCollectionRotation(f.cid,f.reserve);f.apply(rotation.phases[0].messages);f.apply(rotation.phases[1].messages);
  const late=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Late')}],f.reserve);f.apply(late);const original=f.state.record(f.cid,record).siblings('title')[0].write;
  f.apply(rotation.phases[2].messages);assert.equal(f.state.record(f.cid,record).value('title').toString(),'Before');
  const settled=f.state.prepareLateWrites(new Uint8Array(8),{millis:12,counter:65535},new Set(),late.map(message=>({...message,id:t.b64u(t.sha(message.body))})));
  assert.ok(settled.messages.length);f.apply(settled.messages);const current=f.state.record(f.cid,record).siblings('title')[0].write;
  assert.equal(current.value.toString(),'Late');assert.deepEqual(current.dot,original.dot);assert.notDeepEqual(current.ref,original.ref);assert.ok(current.base.some(dot=>t.b64u(dot.device)===t.b64u(original.dot.device)&&dot.counter>=original.dot.counter));
  const again=f.state.prepareLateWrites(new Uint8Array(8),{millis:12,counter:65535},new Set(settled.processed));assert.equal(again.messages.length,0);assert.equal(again.unshared.length,0);
  const other=f.state.prepareLateWrites(new Uint8Array(8).fill(9),{millis:12,counter:65535},new Set());assert.equal(other.messages.length,0);
});

test('a forged claim about this installation’s future clock cannot suppress its late edit',()=>{
  for(const forged of [true,false]){
    const f=fixture(),creator=f.registry.certificate(t.objectID('certificate',objects.get('creator certificate').fields));
    f.apply(f.state.prepareShare(f.cid,creator.participant,'writer','Bob',f.reserve).phases[0].messages);
    const record=t.randomBytes(16);f.apply(f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Before')}],f.reserve));
    const rotation=f.state.prepareCollectionRotation(f.cid,f.reserve);f.apply(rotation.phases[0].messages);f.apply(rotation.phases[1].messages);
    f.apply(f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Late own value')}],f.reserve));const original=f.state.record(f.cid,record).siblings('title')[0];
    f.apply(rotation.phases[2].messages);
    const epoch=t.unb64u(rotation.details.epoch),key=t.unb64u(rotation.details.key),auth=f.registry.add(f.registry.currentSnapshot(creator.participant));
    const write=t.signWrite({hub:f.pin.hub,scope:f.cid,record,field:'title',op:1,author:creator.participant,certificate:creator.id,authorization:auth,dot:{device:new Uint8Array(8).fill(7),millis:50,counter:0},value:Buffer.from('Other value'),element:null,ref:epoch,context:[],base:[{...original.write.dot,millis:forged?999:original.write.dot.millis}],signature:new Uint8Array(64)},t.privateKey(seed('creator signing'),'ed25519'));
    f.envelope(key,t.collectionTag(key,f.pin.hub,epoch),t.recordPayload(record,[write]));f.state.ingest(f.messages);
    const settled=f.state.prepareLateWrites(new Uint8Array(8),{millis:12,counter:65535},new Set());
    if(forged){f.apply(settled.messages);assert.ok(f.state.record(f.cid,record).siblings('title').some(entry=>entry.write.value.toString()==='Late own value'));assert.equal(settled.unshared.some(item=>item.write===t.b64u(original.id)),false);}
    else{assert.equal(settled.messages.length,0);assert.ok(settled.unshared.some(item=>item.write===t.b64u(original.id)));}
  }
});
