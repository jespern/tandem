import {Buffer} from 'buffer';
import {bytes,b64u,sha} from './crypto.js';
import {parseInventory} from './core.js';
export interface InventoryFragment {epoch:Uint8Array;part:number;parts:number;bytes:Uint8Array}
export interface InventoryLimits {maxParts?:number;maxBufferedBytes?:number;maxInventoryBytes?:number;maxAssemblies?:number}
export class InventoryHoldingLimit extends Error {}
const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
/** Competing fragment versions are retained independently. Only a complete,
 * canonical, hash-matching assembly may authorize old writes. No timer or I/O. */
export class InventoryAssembler {
  readonly #epoch:Uint8Array;readonly #commitment:Uint8Array;readonly #limits:Required<InventoryLimits>;
  readonly #groups=new Map<number,Map<number,Uint8Array[]>>();#buffered=0;#complete:Uint8Array|undefined;
  constructor(epoch:Uint8Array,commitment:Uint8Array,limits:InventoryLimits={}){
    this.#epoch=bytes(epoch,32);this.#commitment=bytes(commitment,32);
    this.#limits={maxParts:limits.maxParts??4096,maxBufferedBytes:limits.maxBufferedBytes??16*1024*1024,maxInventoryBytes:limits.maxInventoryBytes??16*1024*1024,maxAssemblies:limits.maxAssemblies??4096};
    for(const value of Object.values(this.#limits))if(!Number.isSafeInteger(value)||value<1)throw new Error('invalid inventory holding limit');
  }
  get complete():Uint8Array|undefined{return this.#complete?bytes(this.#complete):undefined;}
  add(fragment:InventoryFragment):Uint8Array|undefined{
    if(!eq(fragment.epoch,this.#epoch)||!Number.isSafeInteger(fragment.parts)||fragment.parts<1||!Number.isSafeInteger(fragment.part)||fragment.part<0||fragment.part>=fragment.parts)throw new Error('inventory fragment belongs to another epoch or has an invalid index');
    if(this.#complete)return bytes(this.#complete);
    if(fragment.parts>this.#limits.maxParts)throw new InventoryHoldingLimit('inventory part holding limit exceeded');
    const group=this.#groups.get(fragment.parts)??new Map<number,Uint8Array[]>(),versions=group.get(fragment.part)??[];
    if(!versions.some(value=>eq(value,fragment.bytes))){
      // Count even empty alternatives, so hostile empty fragments cost space.
      const cost=fragment.bytes.length+32;
      if(this.#buffered+cost>this.#limits.maxBufferedBytes||fragment.bytes.length>this.#limits.maxInventoryBytes)throw new InventoryHoldingLimit('inventory byte holding limit exceeded');
      versions.unshift(bytes(fragment.bytes));group.set(fragment.part,versions);this.#groups.set(fragment.parts,group);this.#buffered+=cost;
    }
    if(group.size!==fragment.parts)return;
    const candidates=Array.from({length:fragment.parts},(_,index)=>group.get(index)!);
    // Iterative Cartesian search avoids recursion at the 4,096-part boundary.
    const indices=candidates.map(()=>0);let attempted=0;
    while(true){
      const chunks=candidates.map((versions,index)=>versions[indices[index]!]!);
      if(chunks.reduce((size,value)=>size+value.length,0)<=this.#limits.maxInventoryBytes){
        const data=Buffer.concat(chunks);
        if(eq(sha(data),this.#commitment)){parseInventory(data);this.#complete=data;this.#groups.clear();this.#buffered=0;return bytes(data);}
      }
      let index=indices.length-1;
      while(index>=0){const next=indices[index]!+1;if(next<candidates[index]!.length){indices[index]=next;break;}indices[index]=0;index--;}
      if(index<0)return;
      if(++attempted>=this.#limits.maxAssemblies)throw new InventoryHoldingLimit('inventory alternative search holding limit exceeded');
    }
  }
}
/** The native engine uses 48 KiB fragments, keeping normal envelopes below the
 * protocol's recommended 64 KiB message size. The fragment size is configurable. */
export function inventoryPayloads(data:Uint8Array,epoch:Uint8Array,fragmentBytes=48*1024):Uint8Array[]{
  bytes(epoch,32);parseInventory(data);if(!Number.isSafeInteger(fragmentBytes)||fragmentBytes<1)throw new Error('invalid inventory fragment size');
  const parts=Math.max(1,Math.ceil(data.length/fragmentBytes));
  return Array.from({length:parts},(_,part)=>Buffer.from(JSON.stringify({inventory:{epoch:b64u(epoch),part,parts,bytes:b64u(data.subarray(part*fragmentBytes,(part+1)*fragmentBytes))}})));
}
