import {Buffer} from 'buffer';
import {b64u,bytes,unb64u,commit,sha,homeScope} from './crypto.js';
import {type ControlObject,controlFromJSON,controlToJSON,objectID,verifyControl} from './control.js';
import {type Write,writeID,verifyWrite,parseInventory,Entry} from './core.js';
import {Registry,type Validity} from './registry.js';

const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
const valid:Validity=Object.freeze({kind:'valid'}),bad=(reason:string):Validity=>({kind:'invalid',reason}),hold=(...missing:Uint8Array[]):Validity=>({kind:'pending',missing:missing.map(x=>bytes(x))});
const all=(checks:Validity[]):Validity=>checks.find(c=>c.kind==='invalid')??(checks.some(c=>c.kind==='pending')?hold(...checks.flatMap(c=>c.kind==='pending'?c.missing:[])):valid);
export interface HomeLimits {maxEpochs?:number;maxWitnesses?:number;maxKeysPerEpoch?:number;maxDepth?:number}
export class HomeHoldingLimit extends Error {}

/** Participant-bound home history. Inbox/package keys remain candidates until
 * a valid participant-signed start commits to them. No manager privilege exists. */
export class HomePolicy {
  readonly #participant:Uint8Array;readonly #registry:Registry;readonly #limits:Required<HomeLimits>;
  readonly #starts=new Map<string,ControlObject[]>();readonly #keys=new Map<string,Uint8Array[]>();
  readonly #inventories=new Map<string,Map<string,Buffer[]>>();
  readonly #cache=new Map<string,Validity>();#registryRevision=-1;#revision=0;
  get revision():number{return this.#revision;}
  constructor(participant:Uint8Array,registry:Registry,limits:HomeLimits={}){
    this.#participant=bytes(participant,32);this.#registry=registry;this.#limits={maxEpochs:limits.maxEpochs??100000,maxWitnesses:limits.maxWitnesses??16,maxKeysPerEpoch:limits.maxKeysPerEpoch??8,maxDepth:limits.maxDepth??1024};
    for(const n of Object.values(this.#limits))if(!Number.isSafeInteger(n)||n<1)throw new Error('invalid home policy limit');
  }
  accept(object:ControlObject):Uint8Array{
    const o=controlFromJSON(controlToJSON(object.kind,object.fields,object.signatures,object.extras));
    if(o.kind!=='home-epoch'||!eq(o.fields.hub as Uint8Array,this.#registry.pin.hub)||!eq(o.fields.genesis as Uint8Array,this.#registry.pin.genesis)||!eq(o.fields.participant as Uint8Array,this.#participant))throw new Error('home start belongs to another participant or hub');
    const id=objectID(o.kind,o.fields),key=b64u(id),old=this.#starts.get(key)??[];
    if(!old.length&&this.#starts.size>=this.#limits.maxEpochs)throw new HomeHoldingLimit('home epoch holding limit exceeded');
    const encoded=controlToJSON(o.kind,o.fields,o.signatures,o.extras);
    if(!old.some(value=>eq(controlToJSON(value.kind,value.fields,value.signatures,value.extras),encoded))){
      if(old.length>=this.#limits.maxWitnesses){const signer=this.#registry.certificate(o.fields.signer as Uint8Array);
        if(!signer||!o.signatures.sig||!verifyControl(o.kind,o.fields,signer.sign,o.signatures.sig))throw new HomeHoldingLimit('home witness holding limit exceeded');this.#starts.set(key,[o]);
      }else this.#starts.set(key,[...old,o]);
    }
    this.#cache.clear();this.#revision++;return bytes(id);
  }
  #object(id:Uint8Array):ControlObject|undefined{return this.#starts.get(b64u(id))?.[0];}
  object(id:Uint8Array):ControlObject|undefined{const variants=this.#starts.get(b64u(id)),first=variants?.[0];if(!first)return;const signer=this.#registry.certificate(first.fields.signer as Uint8Array);
    const o=variants!.find(value=>signer&&value.signatures.sig&&verifyControl(value.kind,value.fields,signer.sign,value.signatures.sig))??first;return controlFromJSON(controlToJSON(o.kind,o.fields,o.signatures,o.extras));}
  validity(id:Uint8Array):Validity{const v=this.#validity(id,0);return v.kind==='pending'?hold(...v.missing):{...v};}
  #validity(id:Uint8Array,depth:number):Validity{
    if(this.#registryRevision!==this.#registry.revision){this.#cache.clear();this.#registryRevision=this.#registry.revision;}
    const key=b64u(id)+':'+depth,cached=this.#cache.get(key);if(cached)return cached;const result=this.#uncachedValidity(id,depth);this.#cache.set(key,result);return result;
  }
  #uncachedValidity(id:Uint8Array,depth:number):Validity{
    const variants=this.#starts.get(b64u(id));if(!variants)return hold(id);if(depth>=this.#limits.maxDepth)throw new HomeHoldingLimit('home ancestry holding limit exceeded');
    const f=variants[0]!.fields,signer=f.signer as Uint8Array,auth=f.auth as Uint8Array;
    const permitted=this.#registry.authorizes(auth,signer);if(permitted.kind!=='valid')return permitted;
    const cert=this.#registry.certificate(signer);
    if(!cert||!eq(cert.participant,this.#participant)||!variants.some(o=>o.signatures.sig&&verifyControl(o.kind,o.fields,cert.sign,o.signatures.sig)))return bad('home is not signed by its participant');
    const parents=f.parents as Uint8Array[],checks:Validity[]=[];let highest=-1;
    for(const parent of parents){const ok=this.#validity(parent,depth+1);checks.push(ok);if(ok.kind!=='valid')continue;const p=this.#object(parent)!;highest=Math.max(highest,p.fields.n as number);checks.push(this.#registry.extendsFloor(auth,p.fields.auth as Uint8Array));}
    const parentProof=all(checks);if(parentProof.kind!=='valid')return parentProof;
    if(f.n!==highest+1)return bad('invalid home epoch counter');
    return all((f.recipients as Uint8Array[]).map(recipient=>this.#registry.authorizes(auth,recipient)));
  }
  offerKey(epoch:Uint8Array,key:Uint8Array):void{
    bytes(epoch,32);bytes(key,32);const id=b64u(epoch),known=this.#keys.get(id)??[];
    if(known.some(value=>eq(value,key)))return;
    const start=this.#object(epoch);if(start&&this.validity(epoch).kind==='valid'&&eq(start.fields.key as Uint8Array,commit(key))){this.#keys.set(id,[bytes(key)]);this.#revision++;return;}
    if(known.length>=this.#limits.maxKeysPerEpoch||!known.length&&this.#keys.size>=this.#limits.maxEpochs)throw new HomeHoldingLimit('home key candidate holding limit exceeded');
    this.#keys.set(id,[...known,bytes(key)]);this.#revision++;
  }
  key(epoch:Uint8Array):Uint8Array|undefined{
    const start=this.#object(epoch);if(!start||this.validity(epoch).kind!=='valid')return;
    const key=this.#keys.get(b64u(epoch))?.find(key=>eq(commit(key),start.fields.key as Uint8Array));return key?bytes(key):undefined;
  }
  readableKeys():{epoch:Uint8Array;key:Uint8Array}[]{return [...this.#keys].flatMap(([epoch,keys])=>keys.map(key=>({epoch:unb64u(epoch),key:bytes(key)})));}
  historyHeads():Uint8Array[]{const accepted=[...this.#starts.keys()].filter(id=>this.validity(unb64u(id)).kind==='valid'),parents=new Set(accepted.flatMap(id=>(this.#object(unb64u(id))!.fields.parents as Uint8Array[]).map(b64u)));return accepted.filter(id=>!parents.has(id)).map(unb64u).sort(Buffer.compare);}
  #accepted():Set<string>{
    const accepted=new Set<string>(),take=(id:Uint8Array)=>{const key=b64u(id);if(accepted.has(key))return;accepted.add(key);for(const parent of this.#object(id)!.fields.parents as Uint8Array[])take(parent);};
    for(const key of this.#starts.keys()){const id=unb64u(key),o=this.#object(id)!;if(this.validity(id).kind==='valid'&&this.#registry.activity(o.fields.signer as Uint8Array).kind==='valid')take(id);}
    return accepted;
  }
  heads():Uint8Array[]{
    const accepted=this.#accepted(),parents=new Set([...accepted].flatMap(id=>(this.#object(unb64u(id))!.fields.parents as Uint8Array[]).map(b64u)));
    return [...accepted].filter(id=>!parents.has(id)&&!!this.key(unb64u(id))).map(unb64u).sort(Buffer.compare);
  }
  addInventory(epoch:Uint8Array,data:Uint8Array):void{const start=this.#object(epoch);if(!start||this.validity(epoch).kind!=='valid'||!eq(sha(data),start.fields.snapshot as Uint8Array))throw new Error('home inventory does not match a valid signed start');this.#inventories.set(b64u(epoch),parseInventory(data));this.#revision++;}
  listed(epoch:Uint8Array,w:Write):boolean{return this.#inventories.get(b64u(epoch))?.get(Buffer.from(w.record).toString('hex'))?.some(id=>eq(id,writeID(w)))??false;}
  #ancestor(source:Uint8Array,descendant:Uint8Array,seen=new Set<string>()):boolean{if(seen.has(b64u(descendant)))return false;seen.add(b64u(descendant));const start=this.#object(descendant);return !!start&&(start.fields.parents as Uint8Array[]).some(parent=>eq(parent,source)||this.#ancestor(source,parent,seen));}
  dataValidity(w:Write,channel:Uint8Array):Validity{
    if(!eq(w.hub,this.#registry.pin.hub)||!eq(w.author,this.#participant)||!eq(w.scope,homeScope(this.#registry.pin.hub,this.#participant)))return bad('not this participant home');
    if(w.op===4)return bad('home named records cannot be deleted');
    const start=this.#object(channel);if(!start)return hold(channel);const historical=this.validity(channel);if(historical.kind!=='valid')return historical;
    const checks=[this.#registry.authorizes(w.authorization,w.certificate)];
    if(eq(w.ref,channel)){
      checks.push(this.#registry.extendsFloor(w.authorization,start.fields.auth as Uint8Array));
      const children=[...this.#accepted()].map(unb64u).filter(id=>(this.#object(id)!.fields.parents as Uint8Array[]).some(parent=>eq(parent,channel)));
      if(children.length&&!children.some(child=>this.listed(child,w))){const missing=children.filter(child=>!this.#inventories.has(b64u(child)));checks.push(missing.length?hold(...missing):bad('late write in replaced home epoch'));}
    }else{if(!this.#ancestor(w.ref,channel))return bad('home write is not from an ancestor');if(!this.#inventories.has(b64u(channel)))return hold(channel);if(!this.listed(channel,w))return bad('home write not carried into this epoch');}
    const authorized=all(checks);if(authorized.kind!=='valid')return authorized;const cert=this.#registry.certificate(w.certificate);
    return cert&&eq(cert.participant,this.#participant)&&verifyWrite(w,cert.sign)?valid:bad('bad home signature');
  }
  eligible(w:Write):boolean{return this.heads().some(head=>eq(head,w.ref)||this.listed(head,w));}
  exposed():Uint8Array[]{
    const heads=this.heads();return this.#registry.revocationIDs.filter(id=>{
      const target=this.#registry.revocationTarget(id);if(!target||!eq(target.participant,this.#participant)||this.#registry.revocationValidity(id).kind!=='valid')return false;
      return heads.some(head=>{const auth=this.#registry.snapshot(this.#object(head)!.fields.auth as Uint8Array);return !auth||!auth.revocations.some(known=>eq(known,id));});
    });
  }
  inventoriesReady():boolean{const heads=this.heads();return heads.length>0&&heads.every(id=>(this.#object(id)!.fields.parents as Uint8Array[]).length===0||this.#inventories.has(b64u(id)));}
  entry(w:Write):Entry{const start=this.#object(w.ref);if(!start)throw new Error('missing home source');return new Entry(w,w.ref,BigInt(start.fields.n as number));}
}
