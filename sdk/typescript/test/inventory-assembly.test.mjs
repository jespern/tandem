import {test} from 'node:test';
import assert from 'node:assert/strict';
import * as t from '../dist/index.js';
const fixture=()=>{const epoch=t.randomBytes(32),data=t.inventoryBytes(new Map([[t.randomBytes(16).toString('hex'),[t.randomBytes(32)]]])),fragments=t.inventoryPayloads(data,epoch,23).map(data=>t.parseKind1(data,new Uint8Array(32),new Uint8Array(16)));return{epoch,data,fragments};};
test('inventory fragments assemble out of order with duplicate and competing versions',()=>{
  const f=fixture(),bad={...f.fragments[1],bytes:t.randomBytes(f.fragments[1].bytes.length)};
  for(const sequence of [[bad,...f.fragments].reverse(),[bad,...f.fragments]]){
    const a=new t.InventoryAssembler(f.epoch,t.sha(f.data));
    for(const fragment of sequence){a.add(fragment);a.add(fragment);}
    assert.deepEqual(Buffer.from(a.complete),f.data);const copy=a.complete;copy.fill(0);assert.deepEqual(Buffer.from(a.complete),f.data);
  }
});
test('a complete wrong assembly cannot displace the signed inventory',()=>{
  const f=fixture(),a=new t.InventoryAssembler(f.epoch,t.sha(f.data));
  for(const fragment of f.fragments)a.add({...fragment,bytes:t.randomBytes(fragment.bytes.length)});assert.equal(a.complete,undefined);
  for(const fragment of f.fragments)a.add(fragment);assert.deepEqual(Buffer.from(a.complete),f.data);
  assert.throws(()=>a.add({...f.fragments[0],epoch:t.randomBytes(32)}));
});
test('part-count variants are separate and resource limits hold rather than forge an inventory',()=>{
  const f=fixture(),a=new t.InventoryAssembler(f.epoch,t.sha(f.data),{maxParts:4,maxBufferedBytes:1000,maxInventoryBytes:1000,maxAssemblies:2});
  assert.throws(()=>a.add({...f.fragments[0],parts:5}),t.InventoryHoldingLimit);
  a.add({epoch:f.epoch,part:0,parts:2,bytes:f.data});assert.equal(a.complete,undefined);
  for(const fragment of f.fragments)a.add(fragment);assert.deepEqual(Buffer.from(a.complete),f.data);
  const small=new t.InventoryAssembler(f.epoch,t.sha(f.data),{maxBufferedBytes:1});assert.throws(()=>small.add(f.fragments[0]),t.InventoryHoldingLimit);assert.equal(small.complete,undefined);
});
test('a matching hash still requires canonical inventory bytes',()=>{
  const epoch=t.randomBytes(32),data=Buffer.from([0,0,0,0,1]),a=new t.InventoryAssembler(epoch,t.sha(data));assert.throws(()=>a.add({epoch,part:0,parts:1,bytes:data}));assert.equal(a.complete,undefined);
});
