import {Buffer} from 'buffer';
import {type PrivateKey,bytes,b64u,unb64u,sha,publicKey,openEnvelope,openInbox,seal,directoryTag,directoryKey,homeTag,homeScope,inboxTag,collectionTag,namedID} from './crypto.js';
import {type ControlObject,controlFromJSON,controlToJSON,parseControl,objectID} from './control.js';
import {parseJSON,validUnicode} from './json.js';
import {type Dot,type Write,type Op,Entry,RecordState,parseKind1,recordPayload,signWrite} from './core.js';
import {Registry,RegistryHoldingLimit,type Certificate} from './registry.js';
import {HomePolicy,HomeHoldingLimit} from './home-policy.js';
import {CollectionPolicy,CollectionHoldingLimit,ROSTER_RECORD,META_RECORD} from './roster.js';
import {InventoryAssembler,InventoryHoldingLimit,type InventoryFragment} from './inventory-assembly.js';
import {adoptGrant,grantValidity} from './grant.js';
import type {Message} from './session.js';

export interface DeviceStateOptions {maxMessages?:number;maxCachedBytes?:number;messageBytes?:number}
export interface OutgoingMessage {channel:string;body:Uint8Array}
export interface FieldEdit {field:string;op:Op;value?:Uint8Array;element?:Uint8Array}
export interface DeviceOffer {collection:Uint8Array;admission:Uint8Array;owner:Uint8Array;name:string|undefined}
interface Payload {epoch:Uint8Array;parsed:ReturnType<typeof parseKind1>;handled?:boolean}
interface Collection {policy:CollectionPolicy;payloads:Payload[];records:Map<string,RecordState>}
interface Identity {certificate:Certificate;auth:Uint8Array;messages:OutgoingMessage[]}
interface Offer {grant:ControlObject;key:Uint8Array;certificate:Uint8Array}
const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
const text=(value:Uint8Array):string=>new TextDecoder('utf-8',{fatal:true}).decode(value);
const display=(value:Uint8Array):string=>new TextDecoder().decode(value);
const raw=(value:unknown,n:number):Buffer=>{if(typeof value!=='string')throw new Error('byte string required');return bytes(unb64u(value),n);};
const obj=(value:unknown):Record<string,any>=>{if(!value||typeof value!=='object'||Array.isArray(value))throw new Error('object required');return value;};
const holding=(error:unknown):boolean=>error instanceof RegistryHoldingLimit||error instanceof HomeHoldingLimit||error instanceof CollectionHoldingLimit||error instanceof InventoryHoldingLimit||error instanceof DeviceHoldingLimit;
export class DeviceHoldingLimit extends Error {}

/** Verified participant view over durable envelopes. It retains source writes
 * and re-evaluates them when authority/epoch facts change; delivery order is not
 * authority. No I/O, timers or trusted application records enter this layer. */
export class DeviceState {
  readonly #registry:Registry;readonly #participant:Uint8Array;readonly #directory:Uint8Array;
  readonly #keys:{signing:PrivateKey;agreement:PrivateKey};readonly #home:HomePolicy;readonly #limits:Required<DeviceStateOptions>;
  readonly #messages=new Map<string,Map<string,Message>>();readonly #decoded=new Set<string>();readonly #offered=new Set<string>();
  readonly #homePayloads:Payload[]=[];readonly #collections=new Map<string,Collection>();readonly #offers=new Map<string,Offer>();
  readonly #writeValidity=new WeakMap<Write,{generation:string;valid:boolean}>();
  readonly #assemblers=new Map<string,InventoryAssembler>();readonly #complete=new Set<string>();readonly #controls=new Set<string>();
  #homeRecords=new Map<string,RecordState>();#count=0;#cachedBytes=0;#revision=0;
  constructor(registry:Registry,directory:Uint8Array,participant:Uint8Array,keys:{signing:PrivateKey;agreement:PrivateKey},options:DeviceStateOptions={}){
    this.#registry=registry;this.#directory=bytes(directory,32);this.#participant=bytes(participant,32);this.#keys=keys;this.#home=new HomePolicy(participant,registry);
    this.#limits={maxMessages:options.maxMessages??100000,maxCachedBytes:options.maxCachedBytes??64*1024*1024,messageBytes:options.messageBytes??64*1024};
    for(const n of Object.values(this.#limits))if(!Number.isSafeInteger(n)||n<1)throw new Error('invalid device holding limit');
    if(this.#limits.messageBytes>1024*1024)throw new Error('protocol messages must not exceed 1 MiB');
  }
  get revision():number{return this.#revision;}
  get participant():Uint8Array{return bytes(this.#participant);}
  get certificate():Certificate|undefined{return this.#registry.allCertificates.filter(c=>eq(c.participant,this.#participant)&&eq(c.sign,publicKey(this.#keys.signing))&&eq(c.agree,publicKey(this.#keys.agreement))&&this.#registry.activity(c.id).kind==='valid').sort((a,b)=>Buffer.compare(a.id,b.id))[0];}
  get homeReady():boolean{return this.#home.inventoriesReady();}
  offerHomeKey(epoch:Uint8Array,key:Uint8Array):void{if(this.#offerHome(epoch,key)){this.#drain();this.#revision++;}}
  #offerHome(epoch:Uint8Array,key:Uint8Array):boolean{const id=`home:${b64u(epoch)}:${b64u(sha(key))}`;if(this.#offered.has(id))return false;this.#home.offerKey(epoch,key);this.#offered.add(id);return true;}
  #collection(cid:Uint8Array):Collection{const id=b64u(cid);let c=this.#collections.get(id);if(!c){c={policy:new CollectionPolicy(cid,this.#registry),payloads:[],records:new Map()};this.#collections.set(id,c);}return c;}
  #offerCollection(cid:Uint8Array,epoch:Uint8Array,key:Uint8Array):boolean{const id=`collection:${b64u(cid)}:${b64u(epoch)}:${b64u(sha(key))}`;if(this.#offered.has(id))return false;this.#collection(cid).policy.offerKey(epoch,key);this.#offered.add(id);return true;}
  ingest(messages:readonly Message[]):void{
    let changed=false;
    for(const message of messages){if(message.body.length>1024*1024)continue;
      const id=b64u(sha(message.body)),channel=this.#messages.get(message.channel)??new Map<string,Message>();if(channel.has(id))continue;
      if(this.#count>=this.#limits.maxMessages||this.#cachedBytes+message.body.length>this.#limits.maxCachedBytes)throw new DeviceHoldingLimit('device envelope holding limit exceeded');
      channel.set(id,{id,channel:message.channel,body:bytes(message.body)});this.#messages.set(message.channel,channel);this.#count++;this.#cachedBytes+=message.body.length;changed=true;
    }
    if(changed){this.#drain();this.#revision++;}
  }
  #read(tag:string,route:string,consume:(message:Message)=>void):boolean{
    let changed=false;for(const [id,message]of this.#messages.get(tag)??[]){const key=JSON.stringify([route,id]);if(this.#decoded.has(key))continue;
      try{consume(message);}catch(error){if(holding(error))throw error;}
      this.#decoded.add(key);changed=true;
    }return changed;
  }
  #directoryObject(o:ControlObject):void{
    if(!['certificate','snapshot','revocation','invitation','delegation'].includes(o.kind))return;
    const encoded=controlToJSON(o.kind,o.fields,o.signatures,o.extras),key=b64u(sha(encoded));if(this.#controls.has(key))return;this.#registry.add(o);this.#controls.add(key);
  }
  #drain():void{
    const hub=this.#registry.pin.hub,dirTag=directoryTag(this.#directory,hub),dirKey=directoryKey(this.#directory,hub);
    let changed=true;
    while(changed){changed=false;
      changed=this.#read(dirTag,'directory',message=>{const opened=openEnvelope(dirKey,hub,dirTag,message.body);if(opened.kind===3)this.#directoryObject(controlFromJSON(opened.plaintext));})||changed;
      const inbox=inboxTag(this.#participant,hub);
      changed=this.#read(inbox,'inbox',message=>{const j=obj(parseJSON(openInbox(hub,this.#participant,this.#keys.agreement,message.body)));
        if(j.type==='home-key')this.#offerHome(raw(j.epoch,32),raw(j.k,32));
        else if(j.type==='grant'){
          const grant=parseControl(j.grant,'grant'),key=raw(j.k,32),certificate=raw(j.certificate,32);
          if(!eq(grant.fields.hub as Uint8Array,hub)||!eq(grant.fields.recipient as Uint8Array,this.#participant))return;
          const id=b64u(sha(controlToJSON(grant.kind,grant.fields,grant.signatures))),cid=grant.fields.collection as Uint8Array;
          this.#offers.set(id,{grant,key,certificate});this.#offerCollection(cid,grant.fields.epoch as Uint8Array,key);
        }
      })||changed;
      for(const {epoch,key}of this.#home.readableKeys()){
        const tag=homeTag(key,hub,this.#participant,epoch),route=`home:${b64u(epoch)}:${b64u(sha(key))}`;
        changed=this.#read(tag,route,message=>{const opened=openEnvelope(key,hub,tag,message.body);
          if(opened.kind===3){const o=controlFromJSON(opened.plaintext,'home-epoch');const witness=b64u(sha(controlToJSON(o.kind,o.fields,o.signatures,o.extras)));if(!this.#controls.has(witness)){this.#home.accept(o);this.#controls.add(witness);}}
          else if(opened.kind===1)this.#homePayloads.push({epoch,parsed:parseKind1(opened.plaintext,hub,homeScope(hub,this.#participant))});
        })||changed;
      }
      for(const payload of this.#homePayloads){if('writes'in payload.parsed)continue;const start=this.#home.object(payload.epoch);
        if(start&&this.#home.validity(payload.epoch).kind==='valid')changed=this.#assemble('home',payload,start.fields.snapshot as Uint8Array,data=>this.#home.addInventory(payload.epoch,data))||changed;
      }
      if(this.#home.inventoriesReady())this.#homeRecords=this.#records(this.#homePayloads,homeScope(hub,this.#participant),w=>this.#home.eligible(w),(w,e)=>this.#home.dataValidity(w,e).kind==='valid',w=>this.#home.entry(w),`home:${this.#registry.revision}:${this.#home.revision}`);
      changed=this.#memberships()||changed;
      for(const c of this.#collections.values()){
        for(const {epoch,key}of c.policy.readableKeys()){
          const tag=collectionTag(key,hub,epoch),route=`collection:${b64u(c.policy.id)}:${b64u(epoch)}:${b64u(sha(key))}`;
          changed=this.#read(tag,route,message=>{const opened=openEnvelope(key,hub,tag,message.body);if(opened.kind===1)c.payloads.push({epoch,parsed:parseKind1(opened.plaintext,hub,c.policy.id)});})||changed;
        }
        for(const payload of c.payloads){if(!('writes'in payload.parsed)||!eq(payload.parsed.record,ROSTER_RECORD)||payload.handled)continue;
          const before=c.policy.revision;let pending=false;for(const w of payload.parsed.writes){if(c.policy.accept(w,payload.epoch).kind==='pending')pending=true;}
          if(!pending){payload.handled=true;changed=true;}changed=c.policy.revision!==before||changed;
        }
        for(const payload of c.payloads){if('writes'in payload.parsed)continue;const start=c.policy.statement(payload.epoch);if(start?.snapshot)changed=this.#assemble(b64u(c.policy.id),payload,start.snapshot,data=>c.policy.addInventory(payload.epoch,data))||changed;}
      }
      for(const o of this.#offers.values()){
        const c=this.#collection(o.grant.fields.collection as Uint8Array),own=this.#registry.certificate(o.certificate);
        if(!own||!eq(own.participant,this.#participant)||!eq(own.sign,publicKey(this.#keys.signing))||!eq(own.agree,publicKey(this.#keys.agreement)))continue;
        if(adoptGrant(o.grant,o.key,o.certificate,c.policy,this.#registry).kind==='valid'){
          // Adoption may activate a previously held draft and thus its data.
          const marker=`grant:${b64u(sha(controlToJSON(o.grant.kind,o.grant.fields,o.grant.signatures)))}`;if(!this.#controls.has(marker)){this.#controls.add(marker);changed=true;}
        }
      }
    }
    for(const c of this.#collections.values())if(c.policy.inventoriesReady())c.records=this.#records(c.payloads,c.policy.id,w=>c.policy.eligible(w),(w,e)=>c.policy.dataValidity(w,e).kind==='valid',w=>c.policy.entry(w),`${b64u(c.policy.id)}:${this.#registry.revision}:${c.policy.revision}`);
  }
  #assemble(scope:string,payload:Payload,commitment:Uint8Array,accept:(data:Uint8Array)=>void):boolean{
    if(payload.handled||'writes'in payload.parsed)return false;const key=JSON.stringify([scope,b64u(payload.epoch)]);
    if(this.#complete.has(key)){payload.handled=true;return false;}
    if(!eq(payload.parsed.epoch,payload.epoch)){payload.handled=true;return false;}
    let assembler=this.#assemblers.get(key);if(!assembler){assembler=new InventoryAssembler(payload.epoch,commitment);this.#assemblers.set(key,assembler);}
    let data:Uint8Array|undefined;try{data=assembler.add(payload.parsed as InventoryFragment);}catch(error){if(holding(error))throw error;payload.handled=true;return false;}
    payload.handled=true;if(data){accept(data);this.#complete.add(key);this.#assemblers.delete(key);return true;}return false;
  }
  #records(payloads:readonly Payload[],scope:Uint8Array,eligible:(w:Write)=>boolean,valid:(w:Write,epoch:Uint8Array)=>boolean,entry:(w:Write)=>Entry,generation:string):Map<string,RecordState>{
    const records=new Map<string,RecordState>();for(const payload of payloads){if(!('writes'in payload.parsed))continue;
      for(const w of payload.parsed.writes){if(!eligible(w))continue;let checked=this.#writeValidity.get(w);if(checked?.generation!==generation){checked={generation,valid:valid(w,payload.epoch)};this.#writeValidity.set(w,checked);}if(!checked.valid)continue;const id=b64u(w.record),r=records.get(id)??new RecordState(this.#registry.pin.hub,scope,w.record);r.merge(entry(w));records.set(id,r);}
    }return records;
  }
  #memberships():boolean{
    let changed=false;for(const [id,record]of this.#homeRecords){const encoded=record.value('_name');if(!encoded)continue;
      try{const name=text(encoded);if(!name.startsWith('membership:')||b64u(namedID(name))!==id)continue;const cid=bytes(unb64u(name.slice(11)),16);
        for(const element of record.elements('keys')){try{const j=obj(parseJSON(element));changed=this.#offerCollection(cid,raw(j.epoch,32),raw(j.k,32))||changed;}catch(error){if(holding(error))throw error;}}
        const flight=record.value('flight');if(flight){try{const j=obj(parseJSON(flight));changed=this.#offerCollection(cid,raw(j.stmt,32),raw(j.k,32))||changed;}catch(error){if(holding(error))throw error;}}
      }catch(error){if(holding(error))throw error;}
    }return changed;
  }
  #copy(record:RecordState|undefined,scope:Uint8Array,id:Uint8Array):RecordState|undefined{if(!record)return;const copy=new RecordState(this.#registry.pin.hub,scope,id);copy.merge(...record.kept);return copy;}
  homeRecord(name:string):RecordState|undefined{const id=namedID(name);return this.#copy(this.#homeRecords.get(b64u(id)),homeScope(this.#registry.pin.hub,this.#participant),id);}
  record(cid:Uint8Array,id:Uint8Array):RecordState|undefined{return this.#copy(this.#collections.get(b64u(cid))?.records.get(b64u(id)),cid,id);}
  records(cid:Uint8Array):Uint8Array[]{return [...this.#collections.get(b64u(cid))?.records??[]].filter(([id,r])=>id!==b64u(META_RECORD)&&!r.deleted).map(([id])=>unb64u(id)).sort(Buffer.compare);}
  role(cid:Uint8Array):'owner'|'writer'|'reader'|undefined{const c=this.#collections.get(b64u(cid))?.policy,head=c?.head;return head?c!.statement(head)?.members.find(m=>eq(m.uid,this.#participant))?.role:undefined;}
  #answers(cid:Uint8Array,admission:Uint8Array):Set<number>{return new Set((this.homeRecord(`membership:${b64u(cid)}`)?.elements('answers')??[]).filter(value=>value.length===33&&eq(value.subarray(0,32),admission)).map(value=>value[32]!));}
  lists():{id:Uint8Array;name:string|undefined;role:'owner'|'writer'|'reader'|undefined;dead:boolean}[]{
    const out=[];for(const c of this.#collections.values()){const head=c.policy.head,s=head&&c.policy.statement(head),owner=c.policy.owner;if(!s||s.deleted||!owner||!s.members.some(m=>eq(m.uid,this.#participant)))continue;
      const membership=this.homeRecord(`membership:${b64u(c.policy.id)}`);if(!membership?.value('owner'))continue;
      const admission=c.policy.admission(this.#participant,head!),answers=admission?this.#answers(c.policy.id,admission):new Set<number>();
      if(!eq(owner,this.#participant)&&(!answers.has(97)||answers.has(110)||answers.has(108)))continue;
      const name=c.records.get(b64u(META_RECORD))?.value('name');out.push({id:c.policy.id,name:name?display(name):undefined,role:this.role(c.policy.id),dead:c.policy.dead});
    }return out.sort((a,b)=>Buffer.compare(a.id,b.id));
  }
  offers():DeviceOffer[]{
    const out=new Map<string,DeviceOffer>();for(const o of this.#offers.values()){const c=this.#collection(o.grant.fields.collection as Uint8Array),own=this.#registry.certificate(o.certificate);if(!own||!eq(own.sign,publicKey(this.#keys.signing))||!eq(own.agree,publicKey(this.#keys.agreement)))continue;if(grantValidity(o.grant,o.key,o.certificate,c.policy,this.#registry).kind!=='valid')continue;
      const admission=o.grant.fields.admission as Uint8Array;if(this.#answers(c.policy.id,admission).size)continue;const name=c.records.get(b64u(META_RECORD))?.value('name');
      out.set(b64u(c.policy.id),{collection:c.policy.id,admission:bytes(admission),owner:c.policy.owner!,name:name?display(name):undefined});
    }return [...out.values()].sort((a,b)=>Buffer.compare(a.collection,b.collection));
  }
  #identity():Identity{
    const certificate=this.certificate;if(!certificate)throw new Error('this device is not currently admitted');
    const snapshot=this.#registry.currentSnapshot(this.#participant),auth=objectID(snapshot.kind,snapshot.fields),hub=this.#registry.pin.hub;
    const messages=this.#registry.snapshot(auth)?[]:[{channel:directoryTag(this.#directory,hub),body:seal(directoryKey(this.#directory,hub),hub,directoryTag(this.#directory,hub),controlToJSON(snapshot.kind,snapshot.fields),3)}];
    return{certificate,auth,messages};
  }
  #sealRecord(scope:Uint8Array,record:Uint8Array,writes:readonly Write[],key:Uint8Array,tag:string):OutgoingMessage[]{
    const messages:OutgoingMessage[]=[];let part:Write[]=[];
    const flush=()=>{if(part.length){messages.push({channel:tag,body:seal(key,this.#registry.pin.hub,tag,recordPayload(record,part))});part=[];}};
    for(const write of writes){const single=recordPayload(record,[write]);if(single.length+54>1024*1024)throw new Error('single write exceeds the 1 MiB protocol limit');
      if(part.length&&recordPayload(record,[...part,write]).length+54>this.#limits.messageBytes)flush();part.push(write);
    }flush();return messages;
  }
  #edit(scope:Uint8Array,record:Uint8Array,ref:Uint8Array,epoch:Uint8Array,number:bigint,previous:RecordState|undefined,edits:readonly FieldEdit[],reserve:()=>Dot,identity:Identity):Write[]{
    const state=new RecordState(this.#registry.pin.hub,scope,record);if(previous)state.merge(...previous.kept);
    for(const edit of edits){const element=edit.op===2||edit.op===3?bytes(edit.element!):null,vectors=state.vectors(edit.op,edit.field,element,epoch,number);
      const write=signWrite({hub:this.#registry.pin.hub,scope,record,ref,author:this.#participant,certificate:identity.certificate.id,authorization:identity.auth,
        dot:reserve(),field:edit.field,op:edit.op,value:edit.op===1?bytes(edit.value!):new Uint8Array(),element,...vectors,signature:new Uint8Array(64)},this.#keys.signing);
      state.merge(new Entry(write,epoch,number));
    }return state.kept.map(entry=>entry.write);
  }
  prepareHome(name:string,edits:readonly FieldEdit[],reserve:()=>Dot):OutgoingMessage[]{
    validUnicode(name);const identity=this.#identity(),epoch=this.#home.heads()[0];if(!epoch||!this.#home.inventoriesReady())throw new Error('home keys or inventory are not ready');
    const key=this.#home.key(epoch)!,start=this.#home.object(epoch)!,scope=homeScope(this.#registry.pin.hub,this.#participant),record=namedID(name),previous=this.homeRecord(name);
    if(edits.some(e=>e.op===4||e.field==='_name'&&(e.op!==1||!e.value||!eq(e.value,Buffer.from(name)))))throw new Error('home records keep their name and cannot be deleted');
    const changes=[...(!previous?.value('_name')&&!edits.some(e=>e.field==='_name')?[{field:'_name',op:1 as const,value:Buffer.from(name)}]:[]),...edits];
    const writes=this.#edit(scope,record,epoch,epoch,BigInt(start.fields.n as number),previous,changes,reserve,identity);
    return [...identity.messages,...this.#sealRecord(scope,record,writes,key,homeTag(key,this.#registry.pin.hub,this.#participant,epoch))];
  }
  prepareField(cid:Uint8Array,record:Uint8Array,edits:readonly FieldEdit[],reserve:()=>Dot):OutgoingMessage[]{
    const identity=this.#identity(),c=this.#collections.get(b64u(cid)),head=c?.policy.head,s=head&&c?.policy.statement(head),epoch=head&&c?.policy.epoch(head);
    if(!c||!head||!s||!epoch||c.policy.dead||s.deleted||!c.policy.inventoriesReady())throw new Error('collection is not ready to write');
    const role=s.members.find(m=>eq(m.uid,this.#participant))?.role,key=c.policy.key(epoch);if(!key||!role||role==='reader'||eq(record,ROSTER_RECORD)||eq(record,META_RECORD)&&(role!=='owner'||edits.some(e=>e.op===4)))throw new Error('this participant may not write this record');
    if(s.members.some(member=>!this.#registry.snapshot(member.floor)))throw new Error('member policy proofs are missing');
    if(c.policy.exposed().length)throw new Error('waiting for the owner to replace an exposed collection key');
    const previous=this.record(cid,record),changes=[...(eq(record,META_RECORD)&&!previous?.value('_name')?[{field:'_name',op:1 as const,value:Buffer.from('_meta')}]:[]),...edits];
    const writes=this.#edit(cid,record,head,epoch,BigInt(s.keyN),previous,changes,reserve,identity);
    return [...identity.messages,...this.#sealRecord(cid,record,writes,key,collectionTag(key,this.#registry.pin.hub,epoch))];
  }
}
