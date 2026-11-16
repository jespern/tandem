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
    else{assert.equal(settled.messages.length,0);assert.ok(settled.unshared.some(item=>item.write===t.b64u(original.id)));const rescued=f.state.historicalWrite(f.cid,original.id);assert.equal(Buffer.from(rescued.value).toString(),'Late own value');rescued.value.fill(0);assert.equal(Buffer.from(f.state.historicalWrite(f.cid,original.id).value).toString(),'Late own value');}
  }
});

const presence=messages=>messages.map((message,index)=>({id:`remote-${index}`,channel:message.channel,hash:t.b64u(t.sha(message.body))}));
test('healing restores missing kept writes and does not republish an in-flight repair',()=>{
  const f=fixture(),record=t.randomBytes(16),write=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Keep me')}],f.reserve);f.apply(write);
  const received=f.messages.filter(message=>!write.some(w=>t.b64u(t.sha(w.body))===message.id)),repair=f.state.prepareHygiene(presence(received),new Set());
  const data=repair.messages.filter(m=>m.channel===f.tag);assert.equal(data.length,1);assert.deepEqual(repair.destroy,[]);f.apply(repair.messages);
  assert.equal(f.state.prepareHygiene(presence(received),new Set(repair.messages.map(m=>t.b64u(t.sha(m.body))))).messages.length,0);
  assert.equal(f.state.prepareHygiene(presence([...received,...repair.messages]),new Set()).messages.length,0);
});
test('healing deletes superseded physical packets only after a replacement is present',()=>{
  const f=fixture(),record=t.randomBytes(16),first=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Old')}],f.reserve);f.apply(first);
  const second=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('New')}],f.reserve);f.apply(second);
  const oldHash=t.b64u(t.sha(first[0].body)),newHash=t.b64u(t.sha(second[0].body)),rows=presence(f.messages),old=rows.find(row=>row.hash===oldHash);
  const all=f.state.prepareHygiene(rows,new Set());assert.ok(all.destroy.includes(old.id));assert.equal(all.messages.length,0);
  const missing=f.state.prepareHygiene(rows.filter(row=>row.hash!==newHash),new Set([newHash]));assert.equal(missing.destroy.includes(old.id),false);assert.equal(missing.messages.length,0);
});
test('inventory healing requires the correct part index and count as well as slice bytes',()=>{
  const f=fixture(),rotation=f.state.prepareCollectionRotation(f.cid,f.reserve);f.apply(rotation.phases[0].messages);f.apply(rotation.phases[1].messages);f.apply(rotation.phases[2].messages);
  const epoch=t.unb64u(rotation.details.epoch),key=t.unb64u(rotation.details.key),tag=t.collectionTag(key,f.pin.hub,epoch);
  let fragment;const received=f.messages.filter(message=>{if(message.channel!==tag)return true;const opened=t.openEnvelope(key,f.pin.hub,tag,message.body);if(opened.kind!==1)return true;const p=t.parseKind1(opened.plaintext,f.pin.hub,f.cid);if('writes'in p)return true;fragment=p;return false;});
  assert.ok(fragment);assert.equal(fragment.parts,1);
  const malformed=f.envelope(key,tag,Buffer.from(JSON.stringify({inventory:{epoch:t.b64u(epoch),part:1,parts:2,bytes:t.b64u(fragment.bytes)}})));f.state.ingest([malformed]);received.push(malformed);
  const repair=f.state.prepareHygiene(presence(received),new Set()),fragments=repair.messages.filter(message=>message.channel===tag).map(message=>t.parseKind1(t.openEnvelope(key,f.pin.hub,tag,message.body).plaintext,f.pin.hub,f.cid)).filter(p=>!('writes'in p));
  assert.equal(fragments.length,1);assert.equal(fragments[0].part,0);assert.equal(fragments[0].parts,1);assert.deepEqual(fragments[0].bytes,fragment.bytes);
});

test('an operation barrier accepts a valid physical replacement of a compacted record',()=>{
  const f=fixture(),record=t.randomBytes(16),first=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('Old')}],f.reserve);f.apply(first);
  const second=f.state.prepareField(f.cid,record,[{field:'title',op:1,value:Buffer.from('New')}],f.reserve);f.apply(second);
  assert.equal(f.state.messageCovered(first[0],presence(second)),true);
  assert.equal(f.state.messageCovered(second[0],presence(first)),false);
  assert.equal(f.state.messageCovered(first[0],[]),false);
  const roster=f.messages.find(m=>{if(m.channel!==f.tag)return false;const p=t.parseKind1(t.openEnvelope(f.collectionKey,f.pin.hub,f.tag,m.body).plaintext,f.pin.hub,f.cid);return'writes'in p&&t.b64u(p.record)===t.b64u(t.ROSTER_RECORD);});
  assert.equal(f.state.messageCovered(roster,presence(f.messages)),false);
});
test('home key delivery slots suppress new HPKE generation and old epochs retire after rotation',()=>{
  const f=fixture(),messages=f.state.prepareKeyDeliveries(new Set()),home=messages.filter(message=>message.guard?.purpose==='home-key');assert.equal(home.length,1);
  const known=new Set(home.map(message=>JSON.stringify(['home-key',message.guard.certificate,null,message.guard.epoch])));assert.equal(f.state.prepareKeyDeliveries(known).length,0);
  assert.equal(f.state.exportable(home[0]),true);
  const rotation=f.state.prepareHomeRotation();f.state.offerHomeKey(t.unb64u(rotation.details.epoch),t.unb64u(rotation.details.key));f.apply(rotation.phases[0].messages);f.apply(rotation.phases[1].messages);
  assert.equal(f.state.deliveryRetired(home[0]),true);assert.equal(f.state.exportable(home[0]),false);
});

test('a browser stages an exact native pairing package and can cancel it in its private home',()=>{
  const f=fixture(),prepared=f.state.preparePairing('Another browser',{host:'mail.example.test',port:993,user:'fixture',pass:'app-password'}),stage=prepared.operation.phases[0].messages;
  assert.equal(stage.length,3);assert.equal(f.state.homeRecord('pairing:'+t.b64u(prepared.link.token)),undefined);
  const proposal=t.proposePairing(prepared.link,f.registry,stage.map(message=>({...message,id:t.b64u(t.sha(message.body))})),t.deviceKeys(),'Another browser');assert.ok(proposal);
  f.apply(f.state.prepareIntent(prepared.operation.phases[1].intent,f.reserve).messages);const name='pairing:'+t.b64u(prepared.link.token);
  assert.equal(f.state.homeRecord(name).value('label').toString(),'Another browser');assert.equal(f.state.homeRecord(name).elements('outcome').length,0);
  f.apply(f.state.prepareCancelPairing(prepared.link.token,f.reserve));assert.equal(f.state.homeRecord(name).elements('outcome').length,1);
  assert.throws(()=>f.state.preparePairing('Expired',prepared.link.mail,0),/lifetime/);
});
test('a pack of tombstones deletes its records on a device that never saw them one by one (§10.5)',()=>{
  const f=fixture(),a=t.randomBytes(16),b=t.randomBytes(16);
  for(const r of [a,b])f.apply(f.state.prepareField(f.cid,r,[{field:'title',op:1,value:Buffer.from('x')}],f.reserve));
  const before=f.messages.length;
  for(const r of [a,b])f.apply(f.state.prepareField(f.cid,r,[{field:'',op:4}],f.reserve));
  const deletes=f.messages.slice(before).filter(m=>m.channel===f.tag);
  const states=deletes.map(m=>t.parseKind1(t.openEnvelope(f.collectionKey,f.pin.hub,f.tag,m.body).plaintext,f.pin.hub,f.cid)).sort((x,y)=>Buffer.compare(x.record,y.record));
  const body=t.seal(f.collectionKey,f.pin.hub,f.tag,t.packPayload(states),1),pack={id:t.b64u(t.sha(body)),channel:f.tag,body};
  const fresh=()=>{const s=new t.DeviceState(new t.Registry(f.pin),f.directory,f.participant,f.keys);s.offerHomeKey(f.homeID,f.homeKey);return s;};
  const rest=f.messages.filter(m=>!deletes.includes(m));
  const without=fresh();without.ingest(rest);assert.equal(without.records(f.cid).length,2);
  const withPack=fresh();withPack.ingest([...rest,pack]);assert.deepEqual(withPack.records(f.cid),[]);
});
test('at a key change a deleted record becomes a mark: not carried, not writable, and carried forward (§8.3)',()=>{
  const f=fixture(),shelf=t.randomBytes(16),lamp=t.randomBytes(16);
  for(const r of [shelf,lamp])f.apply(f.state.prepareField(f.cid,r,[{field:'title',op:1,value:Buffer.from('x')}],f.reserve));
  f.apply(f.state.prepareField(f.cid,shelf,[{field:'',op:4}],f.reserve));
  const rotate=()=>{const rotation=f.state.prepareCollectionRotation(f.cid,f.reserve);for(const phase of rotation.phases.slice(0,3))f.apply(phase.messages);for(const phase of rotation.phases.slice(3)){if(phase.intent)phase.messages=f.state.prepareIntent(phase.intent,f.reserve,{deliveries:f.messages}).messages;f.apply(phase.messages);}return rotation;};
  const first=rotate(),epoch=t.unb64u(first.details.epoch),key=t.unb64u(first.details.key),tag=t.collectionTag(key,f.pin.hub,epoch);
  const inNew=f.messages.filter(m=>m.channel===tag).map(m=>t.openEnvelope(key,f.pin.hub,tag,m.body)).filter(o=>o.kind===1).map(o=>t.parseKind1States(o.plaintext,f.pin.hub,f.cid)).filter(Array.isArray).flat();
  assert.equal(inNew.some(s=>Buffer.from(s.record).equals(shelf)),false,'no tombstone carried');
  assert.ok(inNew.some(s=>Buffer.from(s.record).equals(lamp)));
  assert.deepEqual(f.state.records(f.cid).map(r=>Buffer.from(r).toString('hex')),[lamp.toString('hex')]);
  assert.throws(()=>f.state.prepareField(f.cid,shelf,[{field:'title',op:1,value:Buffer.from('again')}],f.reserve));
  const second=rotate(),epoch2=t.unb64u(second.details.epoch),key2=t.unb64u(second.details.key),tag2=t.collectionTag(key2,f.pin.hub,epoch2);
  const fragments=f.messages.filter(m=>m.channel===tag2).map(m=>t.openEnvelope(key2,f.pin.hub,tag2,m.body)).filter(o=>o.kind===1).map(o=>t.parseKind1States(o.plaintext,f.pin.hub,f.cid)).filter(p=>!Array.isArray(p));
  const assembled=Buffer.concat(fragments.sort((a,b)=>a.part-b.part).map(p=>p.bytes)),inventory=t.parseInventory(assembled);
  assert.deepEqual(inventory.get(shelf.toString('hex')),[],'the mark is carried forward');
});
test('a record only a deletion mark still names reads as deleted, not unknown, on this and a fresh device (§8.3)',()=>{
  const f=fixture(),shelf=t.randomBytes(16),lamp=t.randomBytes(16);
  for(const r of [shelf,lamp])f.apply(f.state.prepareField(f.cid,r,[{field:'title',op:1,value:Buffer.from('x')}],f.reserve));
  f.apply(f.state.prepareField(f.cid,shelf,[{field:'',op:4}],f.reserve));
  const rotation=f.state.prepareCollectionRotation(f.cid,f.reserve);for(const phase of rotation.phases.slice(0,3))f.apply(phase.messages);
  for(const phase of rotation.phases.slice(3)){if(phase.intent)phase.messages=f.state.prepareIntent(phase.intent,f.reserve,{deliveries:f.messages}).messages;f.apply(phase.messages);}
  const restored=new t.DeviceState(new t.Registry(f.pin),f.directory,f.participant,f.keys);restored.offerHomeKey(f.homeID,f.homeKey);restored.ingest(f.messages);
  for(const state of [f.state,restored]){
    const marked=state.record(f.cid,shelf);
    assert.equal(marked?.deleted,true,'a deletion mark must read as a deleted record, not an unknown one');
    assert.deepEqual(marked.kept,[],'a mark carries no writes, fields or authors');assert.equal(marked.value('title'),undefined);
    assert.equal(state.record(f.cid,lamp)?.deleted,false);
    assert.equal(state.record(f.cid,t.randomBytes(16)),undefined,'an unknown record stays unknown');
    assert.deepEqual(state.records(f.cid).map(r=>Buffer.from(r).toString('hex')),[lamp.toString('hex')],'live listings leave marks out');
  }
});
