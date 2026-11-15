import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';

const vector=JSON.parse(await readFile(new URL('../../../vectors/v2/control.json',import.meta.url),'utf8'));
const objects=new Map(vector.objects.map(row=>[row.name,t.controlFromJSON(Buffer.from(row.json))]));
const copy=o=>t.controlFromJSON(t.controlToJSON(o.kind,o.fields,o.signatures,o.extras));
const id=name=>t.objectID(objects.get(name).kind,objects.get(name).fields);
const names=['creator certificate','invitation','alice phone certificate','delegation','alice tablet certificate','tablet revoked','alice snapshot'];
const registry=(ordered=names)=>{const r=new t.Registry(t.pinGenesis(objects.get('genesis')));for(const name of ordered)r.add(objects.get(name));return r;};

test('registry matches the Swift scenarios: historical validity, revocation, snapshot, dependency order',()=>{
  for(const order of [names,[...names].reverse()]){
    const r=registry(order);for(const name of ['creator certificate','alice phone certificate','alice tablet certificate'])assert.equal(r.certificateValidity(id(name)).kind,'valid');
    assert.deepEqual(r.activity(id('alice tablet certificate')),{kind:'invalid',reason:'revoked'});
    assert.equal(r.activity(id('alice phone certificate')).kind,'valid');
    assert.equal(r.authorizes(id('alice snapshot'),id('alice phone certificate')).kind,'valid');
    assert.equal(t.objectID('snapshot',r.currentSnapshot(r.certificate(id('alice phone certificate')).participant).fields).toString('hex'),Buffer.from(id('alice snapshot')).toString('hex'));
  }
  const r=registry([]);r.add(objects.get('alice tablet certificate'));assert.equal(r.certificateValidity(id('alice tablet certificate')).kind,'pending');
  for(const name of ['delegation','alice phone certificate','invitation'])r.add(objects.get(name));assert.equal(r.certificateValidity(id('alice tablet certificate')).kind,'valid');
  const returned=r.certificate(id('alice phone certificate'));returned.sign.fill(0);assert.equal(r.certificateValidity(id('alice phone certificate')).kind,'valid');
});

test('invalid proof variants cannot poison an already valid content ID in either delivery order',()=>{
  const bad=copy(objects.get('alice tablet certificate'));bad.extras.body.signatures.approver.fill(0);
  assert.equal(t.objectID(bad.kind,bad.fields).toString('hex'),Buffer.from(id('alice tablet certificate')).toString('hex'));
  for(const badFirst of [false,true]){
    const r=registry(names.filter(name=>name!=='alice tablet certificate'));
    for(const o of badFirst?[bad,objects.get('alice tablet certificate')]:[objects.get('alice tablet certificate'),bad])r.add(o);
    assert.equal(r.certificateValidity(id('alice tablet certificate')).kind,'valid');
  }
});

test('the manager cannot mint an existing participant under first admission or recovery',()=>{
  const r=registry(),phone=r.certificate(id('alice phone certificate')),keys=t.deviceKeys(),manager=t.managerKeys(Buffer.from(vector.material.management_seed,'hex'));
  for(const how of [t.How.first,t.How.byDevice,t.How.creatorRecovery]){
    const fields={hub:r.pin.hub,genesis:r.pin.genesis,participant:phone.participant,sign:t.publicKey(keys.signing),agree:t.publicKey(keys.agreement),label:'impostor',how,
      authority:how===t.How.byDevice?phone.id:null,enrollment:t.randomBytes(16)};
    const signatures={self:t.signControl('request',fields,keys.signing)};if(how===t.How.byDevice)signatures.approver=t.signControl('request',fields,manager.signing);
    const request=t.controlFromJSON(t.controlToJSON('request',fields,signatures));const certFields={hub:r.pin.hub,genesis:r.pin.genesis,request:t.objectID('request',fields),issued:[]};
    const cert=t.controlFromJSON(t.controlToJSON('certificate',certFields,{sig:t.signControl('certificate',certFields,manager.signing)},{body:request}));
    assert.equal(r.certificateValidity(r.add(cert)).kind,'invalid');
  }
});
