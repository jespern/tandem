import {Buffer} from 'buffer';
import {type PrivateKey,bytes,b64u,unb64u,sha,publicKey,randomBytes,commit,sealInbox,openEnvelope,openInbox,seal,directoryTag,directoryKey,homeTag,homeScope,inboxTag,collectionTag,namedID} from './crypto.js';
import {type ControlObject,controlFromJSON,controlToJSON,parseControl,objectID,signControl,sealHomeRecovery} from './control.js';
import {parseJSON,validUnicode} from './json.js';
import {type Dot,type Write,type Op,Entry,RecordState,parseKind1,recordPayload,signWrite,writeID,inventoryBytes} from './core.js';
import {Registry,RegistryHoldingLimit,type Certificate} from './registry.js';
import {HomePolicy,HomeHoldingLimit} from './home-policy.js';
import {CollectionPolicy,CollectionHoldingLimit,ROSTER_RECORD,META_RECORD,statementJSON} from './roster.js';
import {InventoryAssembler,InventoryHoldingLimit,inventoryPayloads,type InventoryFragment} from './inventory-assembly.js';
import {adoptGrant,grantValidity} from './grant.js';
import type {Message} from './session.js';
import type {ExportGuard,OutgoingMessage,PreparedOperation} from './operation.js';
export type {OutgoingMessage} from './operation.js';

export interface DeviceStateOptions {maxMessages?:number;maxCachedBytes?:number;messageBytes?:number}
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
  get homeReady():boolean{return this.#home.inventoriesReady()&&this.#home.exposed().length===0;}
  offerHomeKey(epoch:Uint8Array,key:Uint8Array,anchor=false):void{const anchored=anchor&&this.#home.anchor(epoch),offered=this.#offerHome(epoch,key);if(anchored||offered){this.#drain();this.#revision++;}}
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
    const messages=this.#registry.snapshot(auth)?[]:[{channel:directoryTag(this.#directory,hub),body:seal(directoryKey(this.#directory,hub),hub,directoryTag(this.#directory,hub),controlToJSON(snapshot.kind,snapshot.fields),3),guard:{type:'control' as const}}];
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
    validUnicode(name);const identity=this.#identity(),epoch=this.#home.heads()[0];if(!epoch||!this.#home.inventoriesReady())throw new Error('home keys or inventory are not ready');if(this.#home.exposed().length)throw new Error('home key must rotate before writing after a device revocation');
    const key=this.#home.key(epoch)!,start=this.#home.object(epoch)!,scope=homeScope(this.#registry.pin.hub,this.#participant),record=namedID(name),previous=this.homeRecord(name);
    if(edits.some(e=>e.op===4||e.field==='_name'&&(e.op!==1||!e.value||!eq(e.value,Buffer.from(name)))))throw new Error('home records keep their name and cannot be deleted');
    const changes=[...(!previous?.value('_name')&&!edits.some(e=>e.field==='_name')?[{field:'_name',op:1 as const,value:Buffer.from(name)}]:[]),...edits];
    const writes=this.#edit(scope,record,epoch,epoch,BigInt(start.fields.n as number),previous,changes,reserve,identity);
    return [...identity.messages,...this.#sealRecord(scope,record,writes,key,homeTag(key,this.#registry.pin.hub,this.#participant,epoch)).map(message=>({...message,guard:{type:'home' as const,epoch:b64u(epoch)}}))];
  }
  collectionStatus(cid:Uint8Array):{epoch:Uint8Array|undefined;ready:boolean;dead:boolean}{
    const c=this.#collections.get(b64u(cid)),head=c?.policy.head,epoch=head&&c?.policy.epoch(head),statement=head&&c?.policy.statement(head);
    return{epoch,dead:c?.policy.dead??false,ready:!!c&&!!epoch&&!!c.policy.key(epoch)&&!c.policy.dead&&!!statement&&!statement.deleted&&c.policy.inventoriesReady()&&c.policy.exposed().length===0};
  }
  exportable(message:OutgoingMessage):boolean{
    let guard=message.guard;
    if(!guard){
      const hub=this.#registry.pin.hub;if(message.channel===directoryTag(this.#directory,hub))return true;
      for(const {epoch,key}of this.#home.readableKeys())if(message.channel===homeTag(key,hub,this.#participant,epoch)){
        try{const opened=openEnvelope(key,hub,message.channel,message.body);if(opened.kind===3)return true;guard={type:'home',epoch:b64u(epoch)};}catch{return false;}break;
      }
      if(!guard)for(const c of this.#collections.values())for(const {epoch,key}of c.policy.readableKeys())if(message.channel===collectionTag(key,hub,epoch)){
        try{const opened=openEnvelope(key,hub,message.channel,message.body);if(opened.kind===3)return true;const parsed=parseKind1(opened.plaintext,hub,c.policy.id);if('writes'in parsed&&eq(parsed.record,ROSTER_RECORD))return true;guard={type:'collection',collection:b64u(c.policy.id),epoch:b64u(epoch)};}catch{return false;}break;
      }
      if(!guard)return true; // Legacy HPKE claim/answer envelopes; new sensitive deliveries name their exact recipient guard.
    }
    if(guard.type==='control')return true;
    if(guard.type==='action')return true; // The controller also checks the durable operation's current guard.
    if(guard.type==='recipient'){
      const cert=this.#registry.certificate(unb64u(guard.certificate));if(!cert||this.#registry.activity(cert.id).kind!=='valid')return false;
      if(!guard.collection)return true;const c=this.#collections.get(guard.collection)?.policy,head=c?.head,s=head&&c?.statement(head);
      return !!s&&!s.deleted&&!c!.dead&&!!c!.owner&&eq(c!.owner!,this.#participant)&&s.members.some(member=>eq(member.uid,cert.participant))&&(!guard.epoch||b64u(c!.epoch(head!)!)===guard.epoch)&&c!.exposed().length===0;
    }
    if(!this.certificate)return false;
    if(guard.type==='home')return this.homeReady&&this.#home.heads().some(epoch=>b64u(epoch)===guard.epoch);
    const cid=unb64u(guard.collection),status=this.collectionStatus(cid);return status.ready&&!!status.epoch&&b64u(status.epoch)===guard.epoch&&this.role(cid)!==undefined;
  }
  deliveryRetired(message:OutgoingMessage):boolean{
    const guard=message.guard;if(guard?.type!=='recipient')return false;const active=this.#registry.activity(unb64u(guard.certificate));
    if(active.kind==='invalid'&&active.reason==='revoked')return true;
    if(guard.collection){const c=this.#collections.get(guard.collection)?.policy,head=c?.head,s=head&&c?.statement(head),cert=this.#registry.certificate(unb64u(guard.certificate));
      return !!s&&!!cert&&(s.deleted||!s.members.some(member=>eq(member.uid,cert.participant))||!!guard.epoch&&b64u(c!.epoch(head!)!)!==guard.epoch);
    }return false;
  }
  members(cid:Uint8Array):import('./roster.js').RosterMember[]{const c=this.#collections.get(b64u(cid))?.policy,head=c?.head;return head?c!.statement(head)?.members??[]:[];}
  operationValid(kind:string,details:Record<string,string>):boolean{
    if(!this.certificate)return false;
    if(kind==='home-rotation'){
      const planned=this.#registry.snapshot(unb64u(details.auth!));if(!planned)return false;
      return this.#registry.revocationIDs.every(id=>{const target=this.#registry.revocationTarget(id);return !target||!eq(target.participant,this.#participant)||this.#registry.revocationValidity(id).kind!=='valid'||planned.revocations.some(known=>eq(known,id));});
    }
    if(kind==='share'){
      const c=this.#collections.get(details.collection!)?.policy,head=c?.head;
      return !!c&&!!head&&!c.dead&&!!c.owner&&eq(c.owner,this.#participant)&&(b64u(head)===details.parent||c.chain(head).some(id=>b64u(id)===details.statement));
    }
    return true;
  }
  get canRotateHome():boolean{return !!this.certificate&&this.#home.inventoriesReady();}
  get homeNeedsRotation():boolean{return this.#home.exposed().length>0;}
  get homeEpochs():Uint8Array[]{return this.#home.heads();}
  prepareHomeRotation():PreparedOperation{
    const identity=this.#identity(),heads=this.#home.heads();if(!heads.length||!this.#home.inventoriesReady())throw new Error('home has missing heads, keys or inventory');
    const hub=this.#registry.pin.hub,key=randomBytes(32),recipients=this.#registry.allCertificates.filter(cert=>eq(cert.participant,this.#participant)&&this.#registry.activity(cert.id).kind==='valid').sort((a,b)=>Buffer.compare(a.id,b.id));
    const entries=new Map<string,Uint8Array[]>(),known=new Set<string>();
    for(const payload of this.#homePayloads)if('writes'in payload.parsed)for(const w of payload.parsed.writes)if(this.#home.dataValidity(w,payload.epoch).kind==='valid')known.add(b64u(writeID(w)));
    for(const record of this.#homeRecords.values())if(record.kept.length)entries.set(Buffer.from(record.kept[0]!.write.record).toString('hex'),record.kept.map(entry=>entry.id));
    for(const head of heads)for(const [record,ids]of this.#home.inventory(head)??[])for(const id of ids)if(!known.has(b64u(id))){const list=entries.get(record)??[];if(!list.some(value=>eq(value,id)))list.push(id);entries.set(record,list);}
    const inventory=inventoryBytes(entries),n=Math.max(...heads.map(id=>this.#home.object(id)!.fields.n as number))+1;
    const fields={hub,genesis:this.#registry.pin.genesis,participant:this.#participant,n,parents:[...heads].sort(Buffer.compare),key:commit(key),recipients:recipients.map(cert=>cert.id),snapshot:sha(inventory),signer:identity.certificate.id,auth:identity.auth};
    const start=controlToJSON('home-epoch',fields,{sig:signControl('home-epoch',fields,this.#keys.signing)}),epoch=objectID('home-epoch',fields),tag=homeTag(key,hub,this.#participant,epoch),scope=homeScope(hub,this.#participant);
    const stage=(plain:Uint8Array,kind=1):OutgoingMessage=>({channel:tag,body:seal(key,hub,tag,plain,kind),guard:{type:'action',id:''},deferred:true});
    const upload=[...identity.messages,...this.#home.acceptedObjects().map(o=>stage(controlToJSON(o.kind,o.fields,o.signatures,o.extras),3)),...inventoryPayloads(inventory,epoch).map(plain=>stage(plain))];
    for(const record of this.#homeRecords.values())if(record.kept.length)for(const message of this.#sealRecord(scope,record.kept[0]!.write.record,record.kept.map(entry=>entry.write),key,tag))upload.push({...message,guard:{type:'action',id:''},deferred:true});
    const announce:OutgoingMessage[]=[{channel:tag,body:seal(key,hub,tag,start,3),guard:{type:'control'},deferred:true}];
    for(const parent of heads){const old=this.#home.key(parent)!;const oldTag=homeTag(old,hub,this.#participant,parent);announce.push({channel:oldTag,body:seal(old,hub,oldTag,start,3),guard:{type:'control'},deferred:true});}
    const channel=inboxTag(this.#participant,hub),notice=Buffer.from(JSON.stringify({type:'home-key',epoch:b64u(epoch),k:b64u(key)}));
    const deliver:OutgoingMessage[]=recipients.map(cert=>({channel,body:sealInbox(hub,this.#participant,cert.agree,notice),guard:{type:'recipient',certificate:b64u(cert.id)}}));
    if(eq(this.#participant,this.#registry.pin.creator)){const recovery=sealHomeRecovery(this.#registry.pin.agree,hub,epoch,key);deliver.push({channel:directoryTag(this.#directory,hub),body:seal(directoryKey(this.#directory,hub),hub,directoryTag(this.#directory,hub),Buffer.from(JSON.stringify({type:'home-recovery',epoch:b64u(epoch),bytes:b64u(recovery)})),3),guard:{type:'control'}});}
    return{kind:'home-rotation',details:{epoch:b64u(epoch),key:b64u(key),auth:b64u(identity.auth)},phases:[{name:'upload-home-inventory',messages:upload},{name:'announce-home',messages:announce},{name:'deliver-home-keys',messages:deliver}]};
  }
  prepareShare(cid:Uint8Array,participant:Uint8Array,role:'writer'|'reader',name:string,reserve:()=>Dot):PreparedOperation{
    bytes(participant,32);validUnicode(name);if(role!=='writer'&&role!=='reader')throw new Error('share requires writer or reader role');
    const identity=this.#identity(),c=this.#collections.get(b64u(cid))?.policy,head=c?.head,current=head&&c?.statement(head),epoch=head&&c?.epoch(head);
    if(!c||!head||!current||!epoch||c.dead||current.deleted||!c.owner||!eq(c.owner,this.#participant)||c.exposed().length)throw new Error('only the current owner of a ready collection may share it');
    if(current.members.some(member=>eq(member.uid,participant)))throw new Error('participant is already a member');
    const key=c.key(epoch);if(!key)throw new Error('current collection key is missing');
    const targets=this.#registry.allCertificates.filter(cert=>eq(cert.participant,participant)&&this.#registry.activity(cert.id).kind==='valid');if(!targets.length)throw new Error('recipient has no currently admitted devices');
    const snapshot=this.#registry.currentSnapshot(participant),floor=objectID(snapshot.kind,snapshot.fields),hub=this.#registry.pin.hub,tag=collectionTag(key,hub,epoch);
    const {nonce,...rest}=current,next={...rest,n:current.n+1,members:[...current.members,{uid:participant,role,name,floor}]};
    const statement=signWrite({hub,scope:cid,record:ROSTER_RECORD,field:'s',op:1,author:this.#participant,certificate:identity.certificate.id,authorization:identity.auth,dot:reserve(),value:statementJSON(next),element:null,ref:head,context:[],base:[],signature:new Uint8Array(64)},this.#keys.signing);
    const at=writeID(statement),fields={hub,collection:cid,recipient:participant,admission:at,at,epoch,key:commit(key),signer:identity.certificate.id,auth:identity.auth};
    const grant=controlToJSON('grant',fields,{sig:signControl('grant',fields,this.#keys.signing)}),channel=inboxTag(participant,hub);
    const grants=targets.map(target=>({channel,body:sealInbox(hub,participant,target.agree,Buffer.from(JSON.stringify({type:'grant',grant:parseJSON(grant),certificate:b64u(target.id),k:b64u(key)}))),guard:{type:'recipient' as const,certificate:b64u(target.id),collection:b64u(cid),epoch:b64u(epoch)}}));
    const proofs=this.#registry.snapshot(floor)?[]:[{channel:directoryTag(this.#directory,hub),body:seal(directoryKey(this.#directory,hub),hub,directoryTag(this.#directory,hub),controlToJSON(snapshot.kind,snapshot.fields),3),guard:{type:'control' as const}}];
    return{kind:'share',details:{collection:b64u(cid),parent:b64u(head),statement:b64u(at)},phases:[{name:'publish-roster',messages:[...identity.messages,...proofs,{channel:tag,body:seal(key,hub,tag,recordPayload(ROSTER_RECORD,[statement])),deferred:true,guard:{type:'control'}}]},{name:'deliver-grants',messages:grants}]};
  }
  prepareCreateList(name:string,reserve:()=>Dot):{collection:Uint8Array;messages:OutgoingMessage[]}{
    validUnicode(name);const identity=this.#identity();if(!this.#home.inventoriesReady())throw new Error('home is not ready to create a list');
    const hub=this.#registry.pin.hub,key=randomBytes(32),statement={n:0,keyN:0,commit:commit(key),nonce:randomBytes(16),members:[{uid:this.#participant,role:'owner' as const,name:'',floor:identity.auth}],deleted:false};
    const genesis=signWrite({hub,scope:new Uint8Array(16),record:ROSTER_RECORD,field:'s',op:1,author:this.#participant,certificate:identity.certificate.id,authorization:identity.auth,
      dot:reserve(),value:statementJSON(statement),element:null,ref:new Uint8Array(32),context:[],base:[],signature:new Uint8Array(64)},this.#keys.signing);
    const epoch=writeID(genesis),collection=epoch.subarray(0,16),tag=collectionTag(key,hub,epoch);
    const home=this.prepareHome(`membership:${b64u(collection)}`,[{field:'owner',op:1,value:this.#participant},{field:'role',op:1,value:Buffer.from('owner')},
      {field:'keys',op:2,element:Buffer.from(JSON.stringify({epoch:b64u(epoch),k:b64u(key)}))}],reserve);
    const meta=this.#edit(collection,META_RECORD,epoch,epoch,0n,undefined,[{field:'_name',op:1,value:Buffer.from('_meta')},{field:'name',op:1,value:Buffer.from(name)}],reserve,identity);
    return{collection:bytes(collection),messages:[...home,{channel:tag,body:seal(key,hub,tag,recordPayload(ROSTER_RECORD,[genesis]))},...this.#sealRecord(collection,META_RECORD,meta,key,tag)]};
  }
  prepareAccept(collection:Uint8Array,reserve:()=>Dot):OutgoingMessage[]{
    const identity=this.#identity(),c=this.#collections.get(b64u(collection));if(!c)throw new Error('no offer for this collection');
    const candidates=[...this.#offers.values()].filter(o=>{
      if(!eq(o.grant.fields.collection as Uint8Array,collection)||this.#answers(collection,o.grant.fields.admission as Uint8Array).size)return false;
      const target=this.#registry.certificate(o.certificate);return target&&eq(target.sign,publicKey(this.#keys.signing))&&eq(target.agree,publicKey(this.#keys.agreement))&&grantValidity(o.grant,o.key,o.certificate,c.policy,this.#registry).kind==='valid';
    }).sort((a,b)=>(c.policy.statement(b.grant.fields.epoch as Uint8Array)?.keyN??0)-(c.policy.statement(a.grant.fields.epoch as Uint8Array)?.keyN??0));
    const offer=candidates[0];if(!offer)throw new Error('no unanswered valid offer for this collection');
    const f=offer.grant.fields,admission=f.admission as Uint8Array,epoch=f.epoch as Uint8Array,owner=c.policy.owner!,role=c.policy.statement(f.at as Uint8Array)!.members.find(m=>eq(m.uid,this.#participant))!.role;
    const messages=this.prepareHome(`membership:${b64u(collection)}`,[{field:'answers',op:2,element:Buffer.concat([admission,Buffer.from('a')])},{field:'owner',op:1,value:owner},
      {field:'role',op:1,value:Buffer.from(role)},{field:'keys',op:2,element:Buffer.from(JSON.stringify({epoch:b64u(epoch),k:b64u(offer.key)}))}],reserve);
    const fields={hub:this.#registry.pin.hub,collection,participant:this.#participant,admission,answer:'a',event:randomBytes(16),signer:identity.certificate.id,auth:identity.auth};
    const answer=controlToJSON('answer',fields,{sig:signControl('answer',fields,this.#keys.signing)}),channel=inboxTag(owner,this.#registry.pin.hub);
    for(const target of this.#registry.allCertificates)if(eq(target.participant,owner)&&this.#registry.activity(target.id).kind==='valid')messages.push({channel,body:sealInbox(this.#registry.pin.hub,owner,target.agree,answer)});
    return messages;
  }
  prepareField(cid:Uint8Array,record:Uint8Array,edits:readonly FieldEdit[],reserve:()=>Dot):OutgoingMessage[]{
    const identity=this.#identity(),c=this.#collections.get(b64u(cid)),head=c?.policy.head,s=head&&c?.policy.statement(head),epoch=head&&c?.policy.epoch(head);
    if(!c||!head||!s||!epoch||c.policy.dead||s.deleted||!c.policy.inventoriesReady())throw new Error('collection is not ready to write');
    const role=s.members.find(m=>eq(m.uid,this.#participant))?.role,key=c.policy.key(epoch);if(!key||!role||role==='reader'||eq(record,ROSTER_RECORD)||eq(record,META_RECORD)&&(role!=='owner'||edits.some(e=>e.op===4)))throw new Error('this participant may not write this record');
    if(s.members.some(member=>!this.#registry.snapshot(member.floor)))throw new Error('member policy proofs are missing');
    if(c.policy.exposed().length)throw new Error('waiting for the owner to replace an exposed collection key');
    const previous=this.record(cid,record),changes=[...(eq(record,META_RECORD)&&!previous?.value('_name')?[{field:'_name',op:1 as const,value:Buffer.from('_meta')}]:[]),...edits];
    const writes=this.#edit(cid,record,head,epoch,BigInt(s.keyN),previous,changes,reserve,identity);
    return [...identity.messages,...this.#sealRecord(cid,record,writes,key,collectionTag(key,this.#registry.pin.hub,epoch)).map(message=>({...message,guard:{type:'collection' as const,collection:b64u(cid),epoch:b64u(epoch)}}))];
  }
}
