import {test} from 'node:test';
import assert from 'node:assert/strict';
import * as t from '../dist/index.js';
const keys={signing:t.privateKey(t.sha(Buffer.from('founding signing')),'ed25519'),agreement:t.privateKey(t.sha(Buffer.from('founding agreement')),'x25519')};
const mailbox={host:'api.fastmail.com',port:443,user:'creator@example.org'};
test('founding binds the first admitted device, home, recovery and mailbox without credentials',()=>{
  const f=t.prepareFounding(mailbox,keys,'Creator');
  try{
    const registry=new t.Registry(f.pin);registry.add(f.certificate);registry.add(f.snapshot);
    const cert=t.objectID(f.certificate.kind,f.certificate.fields),epoch=t.objectID(f.homeEpoch.kind,f.homeEpoch.fields);
    assert.equal(registry.activity(cert).kind,'valid');assert.equal(registry.authorizes(t.objectID(f.snapshot.kind,f.snapshot.fields),cert).kind,'valid');
    assert.deepEqual(f.genesis.fields.mailboxes,[mailbox]);assert.equal(f.homeEpoch.fields.n,0);
    const state=new t.DeviceState(registry,f.directoryKey,f.participant,keys);state.offerHomeKey(epoch,f.homeKey,true);state.ingest(f.messages.map((m,i)=>({...m,id:String(i)})).reverse());
    assert.equal(state.homeReady,true);assert.equal(t.b64u(state.certificate.id),t.b64u(cert));
    const message=f.messages.find(m=>m.channel===t.inboxTag(f.participant,f.pin.hub)),plain=JSON.parse(t.openInbox(f.pin.hub,f.participant,keys.agreement,message.body));
    assert.equal(plain.type,'home-key');assert.equal(plain.k,t.b64u(f.homeKey));
    const manager=t.managerKeys(f.managementSeed),tag=t.directoryTag(f.directoryKey,f.pin.hub),recovery=f.messages.filter(m=>m.channel===tag).map(m=>JSON.parse(t.openEnvelope(t.directoryKey(f.directoryKey,f.pin.hub),f.pin.hub,tag,m.body).plaintext)).find(o=>o.type==='home-recovery');
    assert(t.openHomeRecovery(manager.agreement,f.pin.hub,epoch,t.unb64u(recovery.bytes)).equals(f.homeKey));
    const capsule=t.foundingCapsule(f,Buffer.alloc(76)),o=t.controlFromJSON(t.openEnvelope(t.directoryKey(f.directoryKey,f.pin.hub),f.pin.hub,tag,capsule.body).plaintext);
    assert(t.verifyControl('capsule',o.fields,f.pin.hub,o.signatures.sig));assert.deepEqual(o.fields.supersedes,[]);
  }finally{f.managementSeed.fill(0);}
});
test('bad labels and mailbox descriptors fail before publishing anything',()=>{
  assert.throws(()=>t.prepareFounding(mailbox,keys,'x'.repeat(257)));
  assert.throws(()=>t.prepareFounding({...mailbox,host:'UPPER.EXAMPLE'},keys,'Creator'));
  assert.throws(()=>t.prepareFounding({...mailbox,port:0},keys,'Creator'));
});
