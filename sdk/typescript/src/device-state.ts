import {Buffer} from 'buffer';
import {type PrivateKey,bytes,b64u,unb64u,sha,publicKey,randomBytes,commit,sealInbox,openEnvelope,openInbox,seal,directoryTag,directoryKey,homeTag,homeScope,inboxTag,collectionTag,namedID} from './crypto.js';
import {type ControlObject,controlFromJSON,controlToJSON,parseControl,objectID,signControl,sealHomeRecovery,verifyControl,pairingInviteKey,pairingKey,pairingTag,sealPackage,sealGate} from './control.js';
import {parseJSON,validUnicode} from './json.js';
import {type Dot,type Write,type Op,Entry,RecordState,parseKind1,recordPayload,signWrite,writeID,inventoryBytes,supersedes,join,MAX_VECTOR,verifyWrite} from './core.js';
import {Registry,RegistryHoldingLimit,type Certificate,type Validity} from './registry.js';
import {HomePolicy,HomeHoldingLimit} from './home-policy.js';
import {CollectionPolicy,CollectionHoldingLimit,ROSTER_RECORD,META_RECORD,statementJSON} from './roster.js';
import {InventoryAssembler,InventoryHoldingLimit,inventoryPayloads,type InventoryFragment} from './inventory-assembly.js';
import {adoptGrant,grantValidity} from './grant.js';
import type {Message} from './session.js';
import {pairingLinkJSON,type PairingLink} from './pairing.js';
import {DeviceManagement,type DeviceClaim} from './management.js';
export type {DeviceClaim} from './management.js';
import {deliveryKey,type ExportGuard,type OutgoingMessage,type PreparedOperation,type OperationIntent,type MailboxPresence} from './operation.js';
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
  readonly #management:DeviceManagement;readonly #registry:Registry;readonly #participant:Uint8Array;readonly #directory:Uint8Array;
  readonly #keys:{signing:PrivateKey;agreement:PrivateKey};readonly #home:HomePolicy;readonly #limits:Required<DeviceStateOptions>;
  readonly #messages=new Map<string,Map<string,Message>>();readonly #decoded=new Set<string>();readonly #offered=new Set<string>();
  readonly #homePayloads:Payload[]=[];readonly #collections=new Map<string,Collection>();readonly #offers=new Map<string,Offer>();
  readonly #packets=new Map<string,Payload>();readonly #homeStarts=new Map<string,ControlObject>();readonly #rosterVerified=new WeakSet<Write>();
  readonly #sourceVerified=new WeakSet<Write>();
  readonly #writeValidity=new WeakMap<Write,{generation:string;valid:boolean;pending:boolean}>();
  readonly #assemblers=new Map<string,InventoryAssembler>();readonly #complete=new Set<string>();readonly #controls=new Set<string>();
  #homeRecords=new Map<string,RecordState>();#count=0;#cachedBytes=0;#revision=0;
  constructor(registry:Registry,directory:Uint8Array,participant:Uint8Array,keys:{signing:PrivateKey;agreement:PrivateKey},options:DeviceStateOptions={}){
    this.#management=new DeviceManagement(registry,directory);this.#registry=registry;this.#directory=bytes(directory,32);this.#participant=bytes(participant,32);this.#keys=keys;this.#home=new HomePolicy(participant,registry);
    this.#limits={maxMessages:options.maxMessages??100000,maxCachedBytes:options.maxCachedBytes??64*1024*1024,messageBytes:options.messageBytes??64*1024};
    for(const n of Object.values(this.#limits))if(!Number.isSafeInteger(n)||n<1)throw new Error('invalid device holding limit');
    if(this.#limits.messageBytes>1024*1024)throw new Error('protocol messages must not exceed 1 MiB');
  }
  get revision():number{return this.#revision;}
  get participant():Uint8Array{return bytes(this.#participant);}
  get certificate():Certificate|undefined{return this.#registry.allCertificates.filter(c=>eq(c.participant,this.#participant)&&eq(c.sign,publicKey(this.#keys.signing))&&eq(c.agree,publicKey(this.#keys.agreement))&&this.#registry.activity(c.id).kind==='valid').sort((a,b)=>Buffer.compare(a.id,b.id))[0];}
  get authorizationPending():boolean{return this.#registry.allCertificates.some(c=>eq(c.participant,this.#participant)&&eq(c.sign,publicKey(this.#keys.signing))&&eq(c.agree,publicKey(this.#keys.agreement))&&this.#registry.activity(c.id).kind==='pending');}
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
    this.#management.offerDirectory(o);
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
        if(j.type==='pair-claim')this.#management.offerClaim(j);
        else if(j.type==='home-key')this.#offerHome(raw(j.epoch,32),raw(j.k,32));
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
          if(opened.kind===3){const o=controlFromJSON(opened.plaintext,'home-epoch');const witness=b64u(sha(controlToJSON(o.kind,o.fields,o.signatures,o.extras)));if(!this.#controls.has(witness)){this.#home.accept(o);this.#controls.add(witness);}this.#homeStarts.set(JSON.stringify([tag,message.id]),o);}
          else if(opened.kind===1){const payload={epoch,parsed:parseKind1(opened.plaintext,hub,homeScope(hub,this.#participant))};this.#homePayloads.push(payload);this.#packets.set(JSON.stringify([tag,message.id]),payload);}
        })||changed;
      }
      for(const payload of this.#homePayloads){if('writes'in payload.parsed)continue;const start=this.#home.object(payload.epoch);
        if(start&&this.#home.validity(payload.epoch).kind==='valid')changed=this.#assemble('home',payload,start.fields.snapshot as Uint8Array,data=>this.#home.addInventory(payload.epoch,data))||changed;
      }
      if(this.#home.inventoriesReady())this.#homeRecords=this.#records(this.#homePayloads,homeScope(hub,this.#participant),w=>this.#home.eligible(w),(w,e)=>this.#home.dataValidity(w,e),w=>this.#home.entry(w),`home:${this.#registry.revision}:${this.#home.revision}`);
      changed=this.#memberships()||changed;
      for(const c of this.#collections.values()){
        for(const {epoch,key}of c.policy.readableKeys()){
          const tag=collectionTag(key,hub,epoch),route=`collection:${b64u(c.policy.id)}:${b64u(epoch)}:${b64u(sha(key))}`;
          changed=this.#read(tag,route,message=>{const opened=openEnvelope(key,hub,tag,message.body);if(opened.kind===1){const payload={epoch,parsed:parseKind1(opened.plaintext,hub,c.policy.id)};c.payloads.push(payload);this.#packets.set(JSON.stringify([tag,message.id]),payload);}})||changed;
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
    for(const c of this.#collections.values())if(c.policy.inventoriesReady())c.records=this.#records(c.payloads,c.policy.id,w=>c.policy.eligible(w),(w,e)=>c.policy.dataValidity(w,e),w=>c.policy.entry(w),`${b64u(c.policy.id)}:${this.#registry.revision}:${c.policy.revision}`);
  }
  #assemble(scope:string,payload:Payload,commitment:Uint8Array,accept:(data:Uint8Array)=>void):boolean{
    if(payload.handled||'writes'in payload.parsed)return false;const key=JSON.stringify([scope,b64u(payload.epoch)]);
    if(this.#complete.has(key)){payload.handled=true;return false;}
    if(!eq(payload.parsed.epoch,payload.epoch)){payload.handled=true;return false;}
    let assembler=this.#assemblers.get(key);if(!assembler){assembler=new InventoryAssembler(payload.epoch,commitment);this.#assemblers.set(key,assembler);}
    let data:Uint8Array|undefined;try{data=assembler.add(payload.parsed as InventoryFragment);}catch(error){if(holding(error))throw error;payload.handled=true;return false;}
    payload.handled=true;if(data){accept(data);this.#complete.add(key);this.#assemblers.delete(key);return true;}return false;
  }
  #records(payloads:readonly Payload[],scope:Uint8Array,eligible:(w:Write)=>boolean,valid:(w:Write,epoch:Uint8Array)=>Validity,entry:(w:Write)=>Entry,generation:string):Map<string,RecordState>{
    const records=new Map<string,RecordState>();for(const payload of payloads){if(!('writes'in payload.parsed))continue;
      for(const w of payload.parsed.writes){if(!eligible(w))continue;let checked=this.#writeValidity.get(w);if(checked?.generation!==generation){const result=valid(w,payload.epoch);checked={generation,valid:result.kind==='valid',pending:result.kind==='pending'};this.#writeValidity.set(w,checked);}if(!checked.valid)continue;this.#sourceVerified.add(w);const id=b64u(w.record),r=records.get(id)??new RecordState(this.#registry.pin.hub,scope,w.record);r.merge(entry(w));records.set(id,r);}
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
  /** A verified historical value for rescue UI; never re-signs it or changes
   * membership. Missing proof/value stays undefined. Returned bytes are copies. */
  historicalWrite(scope:Uint8Array,id:Uint8Array):Write|undefined{
    const home=eq(scope,homeScope(this.#registry.pin.hub,this.#participant)),collection=this.#collections.get(b64u(scope)),policy=home?this.#home:collection?.policy;if(!policy)return;
    for(const payload of home?this.#homePayloads:collection!.payloads)if('writes'in payload.parsed)for(const write of payload.parsed.writes)if(eq(writeID(write),id)&&policy.sourceValidity(write).kind==='valid')return structuredClone(write);
    return;
  }
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
      if(guard.purpose==='home-key')return eq(cert.participant,this.#participant)&&this.homeReady&&this.#home.heads().some(epoch=>b64u(epoch)===guard.epoch);
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
    if(guard.purpose==='home-key'&&this.#home.heads().length&&!this.#home.heads().some(epoch=>b64u(epoch)===guard.epoch))return true;
    if(guard.collection){const c=this.#collections.get(guard.collection)?.policy,head=c?.head,s=head&&c?.statement(head),cert=this.#registry.certificate(unb64u(guard.certificate));
      return !!s&&!!cert&&(s.deleted||c!.exposed().length>0||!s.members.some(member=>eq(member.uid,cert.participant))||!!guard.epoch&&b64u(c!.epoch(head!)!)!==guard.epoch);
    }return false;
  }
  members(cid:Uint8Array):import('./roster.js').RosterMember[]{const c=this.#collections.get(b64u(cid))?.policy,head=c?.head;return head?c!.statement(head)?.members??[]:[];}
  operationValid(kind:string,details:Record<string,string>):boolean{
    if(kind.startsWith('admin-'))return this.#management.operationValid(kind,details);
    if(!this.certificate)return this.authorizationPending;
    if(kind==='pairing-start')return Date.now()<Number(details.expires)&&this.#registry.activity(unb64u(details.source!)).kind!=='invalid';
    if(kind==='home-rotation'){
      const planned=this.#registry.snapshot(unb64u(details.auth!));if(!planned)return false;
      return this.#registry.revocationIDs.every(id=>{const target=this.#registry.revocationTarget(id);return !target||!eq(target.participant,this.#participant)||this.#registry.revocationValidity(id).kind!=='valid'||planned.revocations.some(known=>eq(known,id));});
    }
    if(kind==='collection-rotation'){
      const c=this.#collections.get(details.collection!)?.policy,head=c?.head;if(!c||!head||c.dead||!c.owner||!eq(c.owner,this.#participant))return false;
      if(c.isAnnounced(unb64u(details.epoch!)))return c.chain(head).some(id=>b64u(id)===details.epoch);
      if(b64u(head)!==details.parent)return false;
      const value=this.homeRecord(`membership:${details.collection}`)?.value('flight');if(!value)return false;
      try{const note=obj(parseJSON(value));if(note.stmt!==details.epoch||note.k!==details.key)return false;}catch{return false;}
      const floors=parseJSON(Buffer.from(details.floors!)) as {participant:string;floor:string}[];
      return floors.every(member=>{const floor=this.#registry.snapshot(unb64u(member.floor));return !!floor&&this.#registry.revocationIDs.every(id=>{const target=this.#registry.revocationTarget(id);return !target||b64u(target.participant)!==member.participant||this.#registry.revocationValidity(id).kind!=='valid'||floor.revocations.some(known=>eq(known,id));});});
    }
    if(kind==='share'||kind==='roster-change'){
      const c=this.#collections.get(details.collection!)?.policy,head=c?.head;
      return !!c&&!!head&&!c.dead&&!!c.owner&&eq(c.owner,this.#participant)&&(b64u(head)===details.parent||c.chain(head).some(id=>b64u(id)===details.statement));
    }
    return true;
  }
  claims(now=Date.now()):DeviceClaim[]{return this.#management.claims(now);}
  adminCapsules():Uint8Array[]{return this.#management.capsules();}
  prepareApprove(claim:Uint8Array,seed:Uint8Array):PreparedOperation{return this.#management.approve(bytes(claim,32),seed);}
  prepareRevoke(certificate:Uint8Array,seed:Uint8Array):PreparedOperation{return this.#management.revoke(bytes(certificate,32),seed);}
  prepareReject(claim:Uint8Array,seed:Uint8Array):PreparedOperation{return this.#management.receipt(1,bytes(claim,32),seed);}
  prepareCancelInvitation(capability:Uint8Array,seed:Uint8Array):PreparedOperation{return this.#management.receipt(3,bytes(capability,32),seed);}
  preparePassword(box:Uint8Array,seed:Uint8Array):PreparedOperation{return this.#management.password(box,seed);}
  prepareInvitation(label:string,mail:PairingLink['mail'],seed:Uint8Array,validForMs=7*24*3600*1000):{link:Uint8Array;operation:PreparedOperation}{if(!this.certificate||!this.homeReady)throw new Error('invitation requires an admitted device with a private home');return this.#management.invite(label,mail,seed,validForMs);}
  preparePairing(label:string,mail:PairingLink['mail'],validForMs=24*3600*1000,now=Date.now()):{link:PairingLink;operation:PreparedOperation}{
    const identity=this.#identity(),epoch=this.#home.heads()[0];if(!epoch||!this.homeReady)throw new Error('pairing requires a private home');if(!Number.isSafeInteger(validForMs)||validForMs<1||!Number.isSafeInteger(now+validForMs))throw new Error('invalid pairing lifetime');
    const hub=this.#registry.pin.hub,token=randomBytes(16),secret=randomBytes(32),a=randomBytes(32),invite=pairingInviteKey(secret,hub,token),key=this.#home.key(epoch)!;
    const packageBytes=sealPackage(secret,a,hub,token,Buffer.from(JSON.stringify({home:b64u(epoch),k:b64u(key)}))),gate=sealGate(this.#registry.pin.agree,hub,token,this.#participant,a);a.fill(0);
    const fields={hub,genesis:this.#registry.pin.genesis,participant:this.#participant,source:identity.certificate.id,key:publicKey(invite),token,expires:now+validForMs,package:sha(packageBytes),gate:sha(gate)},delegation=controlToJSON('delegation',fields,{sig:signControl('delegation',fields,this.#keys.signing)}),delegationID=objectID('delegation',fields),channel=pairingTag(secret,hub,token),channelKey=pairingKey(secret,hub,token);
    const link:PairingLink={hub,genesis:this.#registry.pin.genesis,directoryKey:bytes(this.#directory),secret,token,participant:bytes(this.#participant),mail:{...mail}};
    const messages=[delegation,Buffer.from(JSON.stringify({type:'package',bytes:b64u(packageBytes)})),Buffer.from(JSON.stringify({type:'gate',bytes:b64u(gate)}))].map(plain=>({channel,body:seal(channelKey,hub,channel,plain,3),guard:{type:'action' as const,id:''}}));
    return{link,operation:{kind:'pairing-start',details:{source:b64u(identity.certificate.id),expires:String(fields.expires),token:b64u(token),delegation:b64u(delegationID),link:b64u(pairingLinkJSON(link))},phases:[{name:'stage-pairing',messages},{name:'remember-pairing',requires:[0],messages:[],intent:{type:'pairing-home',values:{token:b64u(token),delegation:b64u(delegationID),label,expires:String(fields.expires)}}}]}};
  }
  prepareCancelPairing(token:Uint8Array,reserve:()=>Dot,fallback?:Uint8Array):OutgoingMessage[]{
    bytes(token,16);const identity=this.#identity(),name=`pairing:${b64u(token)}`,delegation=this.homeRecord(name)?.value('delegation')??fallback;if(!delegation)throw new Error('no such pairing');
    const fields={hub:this.#registry.pin.hub,genesis:this.#registry.pin.genesis,outcome:2,subject:bytes(delegation,32),signer:identity.certificate.id},plain=controlToJSON('receipt',fields,{sig:signControl('receipt',fields,this.#keys.signing)}),channel=directoryTag(this.#directory,this.#registry.pin.hub);
    return[{channel,body:seal(directoryKey(this.#directory,this.#registry.pin.hub),this.#registry.pin.hub,channel,plain,3),guard:{type:'control'}},...this.prepareHome(name,[{field:'delegation',op:1,value:delegation},{field:'outcome',op:2,element:objectID('receipt',fields)}],reserve)];
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
    const deliver:OutgoingMessage[]=recipients.map(cert=>({channel,body:sealInbox(hub,this.#participant,cert.agree,notice),guard:{type:'recipient',purpose:'home-key',epoch:b64u(epoch),certificate:b64u(cert.id)}}));
    if(eq(this.#participant,this.#registry.pin.creator)){const recovery=sealHomeRecovery(this.#registry.pin.agree,hub,epoch,key);deliver.push({channel:directoryTag(this.#directory,hub),body:seal(directoryKey(this.#directory,hub),hub,directoryTag(this.#directory,hub),Buffer.from(JSON.stringify({type:'home-recovery',epoch:b64u(epoch),bytes:b64u(recovery)})),3),guard:{type:'control'}});}
    return{kind:'home-rotation',details:{epoch:b64u(epoch),key:b64u(key),auth:b64u(identity.auth)},phases:[{name:'upload-home-inventory',messages:upload},{name:'announce-home',messages:announce},{name:'deliver-home-keys',messages:deliver}]};
  }
  prepareKeyDeliveries(known:ReadonlySet<string>,blockedCollections:ReadonlySet<string>=new Set(),blockedHome=false):OutgoingMessage[]{
    const messages:OutgoingMessage[]=[],hub=this.#registry.pin.hub;if(!this.certificate)return messages;
    if(this.homeReady&&!blockedHome)for(const epoch of this.#home.heads()){
      const key=this.#home.key(epoch)!,channel=inboxTag(this.#participant,hub);
      for(const cert of this.#registry.allCertificates)if(eq(cert.participant,this.#participant)&&this.#registry.activity(cert.id).kind==='valid'){
        const guard={type:'recipient' as const,purpose:'home-key' as const,epoch:b64u(epoch),certificate:b64u(cert.id)};
        if(!known.has(deliveryKey(guard)!))messages.push({channel,guard,body:sealInbox(hub,this.#participant,cert.agree,Buffer.from(JSON.stringify({type:'home-key',epoch:b64u(epoch),k:b64u(key)})))});
      }
    }
    for(const row of this.lists())if(!blockedCollections.has(b64u(row.id))){const c=this.#collections.get(b64u(row.id))!.policy,head=c.head,epoch=head&&c.epoch(head),key=epoch&&c.key(epoch);
      if(head&&epoch&&key&&c.owner&&eq(c.owner,this.#participant)&&this.collectionStatus(row.id).ready){const grants=this.#grants(row.id,head,key,known);if(grants.some(message=>message.guard?.type==='recipient'))messages.push(...grants);}
    }return messages;
  }
  /** A staged record remains confirmed if physical, valid data now dominates it.
   * Exact ciphertext presence is still required for inventories and control proofs. */
  messageCovered(message:OutgoingMessage,presence:readonly MailboxPresence[]):boolean{
    const packet=this.#packets.get(JSON.stringify([message.channel,b64u(sha(message.body))]));if(!packet||!('writes'in packet.parsed)||eq(packet.parsed.record,ROSTER_RECORD))return false;
    const parsed=packet.parsed,hub=this.#registry.pin.hub;
    let policy:HomePolicy|CollectionPolicy|undefined;
    for(const row of this.#home.readableKeys())if(homeTag(row.key,hub,this.#participant,row.epoch)===message.channel)policy=this.#home;
    if(!policy)for(const c of this.#collections.values())for(const row of c.policy.readableKeys())if(collectionTag(row.key,hub,row.epoch)===message.channel)policy=c.policy;
    if(!policy)return false;const entries=new Map<string,Entry>();
    for(const row of presence)if(row.channel===message.channel){const p=this.#packets.get(JSON.stringify([row.channel,row.hash]));if(p&&'writes'in p.parsed&&eq(p.parsed.record,parsed.record))for(const w of p.parsed.writes)if(policy.dataValidity(w,p.epoch).kind==='valid')entries.set(b64u(writeID(w)),policy.entry(w));}
    return parsed.writes.every(w=>entries.has(b64u(writeID(w)))||[...entries.values()].some(candidate=>supersedes(candidate,policy!.entry(w))));
  }
  prepareHygiene(presence:readonly MailboxPresence[],pending:ReadonlySet<string>):{messages:OutgoingMessage[];destroy:string[]}{
    const messages:OutgoingMessage[]=[],destroy:string[]=[];if(!this.certificate)return{messages,destroy};
    type Route={tag:string;key:Uint8Array;epoch:Uint8Array;scope:Uint8Array;records:Map<string,RecordState>;generation:string;valid:(w:Write)=>Validity;entry:(w:Write)=>Entry;belongs:(entry:Entry)=>boolean;guard:ExportGuard;roster?:Write[];starts?:ControlObject[];inventory?:Map<string,Buffer[]>};
    const hub=this.#registry.pin.hub,routes:Route[]=[];
    for(const row of this.lists()){
      const c=this.#collections.get(b64u(row.id))!,head=c.policy.head,epoch=head&&c.policy.epoch(head);if(!head||!epoch||c.policy.dead||!c.policy.inventoriesReady()||c.policy.exposed().length)continue;const key=c.policy.key(epoch);if(!key)continue;
      routes.push({tag:collectionTag(key,hub,epoch),key,epoch,scope:c.policy.id,records:c.records,generation:`${b64u(c.policy.id)}:${this.#registry.revision}:${c.policy.revision}`,valid:w=>c.policy.dataValidity(w,epoch),entry:w=>c.policy.entry(w),belongs:()=>true,guard:{type:'collection',collection:b64u(c.policy.id),epoch:b64u(epoch)},roster:c.policy.writes(head),...(c.policy.inventory(epoch)?{inventory:c.policy.inventory(epoch)!}:{})});
    }
    if(this.homeReady)for(const epoch of this.#home.heads()){
      const key=this.#home.key(epoch)!;routes.push({tag:homeTag(key,hub,this.#participant,epoch),key,epoch,scope:homeScope(hub,this.#participant),records:this.#homeRecords,generation:`home:${this.#registry.revision}:${this.#home.revision}`,valid:w=>this.#home.dataValidity(w,epoch),entry:w=>this.#home.entry(w),belongs:e=>eq(e.write.ref,epoch)||this.#home.listed(epoch,e.write),guard:{type:'home',epoch:b64u(epoch)},starts:this.#home.pathObjects(epoch),...(this.#home.inventory(epoch)?{inventory:this.#home.inventory(epoch)!}:{})});
    }
    for(const route of routes){
      type Row={id:string;hash:string;writes:Map<string,Entry>;held:boolean;pending:boolean};
      const records=new Map<string,Row[]>(),roster=new Set<string>(),starts=new Set<string>(),fragments=new Set<string>();
      const add=(hash:string,id:string,isPending:boolean)=>{
        const cacheKey=JSON.stringify([route.tag,hash]),packet=this.#packets.get(cacheKey),start=this.#homeStarts.get(cacheKey);
        if(start){const expected=route.starts?.find(o=>eq(objectID(o.kind,o.fields),objectID(start.kind,start.fields))),cert=this.#registry.certificate(start.fields.signer as Uint8Array);if(expected&&cert&&start.signatures.sig&&verifyControl(start.kind,start.fields,cert.sign,start.signatures.sig))starts.add(b64u(objectID(start.kind,start.fields)));return;}
        if(!packet)return;const p=packet.parsed;
        if(!('writes'in p)){if(eq(p.epoch,route.epoch))fragments.add(JSON.stringify([p.part,p.parts,b64u(sha(p.bytes))]));return;}
        if(eq(p.record,ROSTER_RECORD)){
          for(const raw of p.writes){const w=raw.ref.every(b=>b===0)?{...raw,scope:new Uint8Array(16)}:raw,expected=route.roster?.find(candidate=>eq(writeID(candidate),writeID(w)));if(!expected)continue;
            if(!this.#rosterVerified.has(raw)){const cert=this.#registry.certificate(w.certificate);if(!cert||!eq(cert.participant,w.author)||this.#registry.authorizes(w.authorization,w.certificate).kind!=='valid'||!verifyWrite(w,cert.sign))continue;this.#rosterVerified.add(raw);}roster.add(b64u(writeID(w)));
          }return;
        }
        const entries=new Map<string,Entry>();let held=false;
        for(const w of p.writes){let checked=this.#writeValidity.get(w);if(checked?.generation!==route.generation){const result=route.valid(w);checked={generation:route.generation,valid:result.kind==='valid',pending:result.kind==='pending'};this.#writeValidity.set(w,checked);}if(checked.pending)held=true;if(checked.valid)entries.set(b64u(writeID(w)),route.entry(w));}
        if(!entries.size&&!held)return;const record=b64u(p.record),rows=records.get(record)??[];rows.push({id,hash,writes:entries,held,pending:isPending});records.set(record,rows);
      };
      for(const row of presence)if(row.channel===route.tag)add(row.hash,row.id,false);
      for(const hash of pending)if(this.#messages.get(route.tag)?.has(hash))add(hash,hash,true);
      if(route.roster&&route.roster.some(w=>!roster.has(b64u(writeID(w)))))messages.push(...this.#sealRecord(route.scope,ROSTER_RECORD,route.roster,route.key,route.tag).map(message=>({...message,guard:{type:'control' as const}})));
      for(const start of route.starts??[])if(!starts.has(b64u(objectID(start.kind,start.fields))))messages.push({channel:route.tag,body:seal(route.key,hub,route.tag,controlToJSON(start.kind,start.fields,start.signatures),3),guard:route.guard});
      if(route.inventory){const payloads=inventoryPayloads(inventoryBytes(route.inventory),route.epoch);for(const plain of payloads){const p=parseKind1(plain,hub,route.scope);if('writes'in p)throw new Error('inventory payload became a record');if(!fragments.has(JSON.stringify([p.part,p.parts,b64u(sha(p.bytes))])))messages.push({channel:route.tag,body:seal(route.key,hub,route.tag,plain),guard:route.guard});}}
      const dominates=(a:ReadonlyMap<string,Entry>,b:ReadonlyMap<string,Entry>)=>[...b].every(([id,x])=>a.has(id)||[...a.values()].some(w=>supersedes(w,x)));
      for(const [record,state]of route.records){const kept=new Map(state.kept.filter(route.belongs).map(entry=>[b64u(entry.id),entry]));if(!kept.size)continue;
        const rows=records.get(record)??[],physical=rows.filter(row=>!row.pending&&!row.held),queued=rows.filter(row=>row.pending&&!row.held),gone=new Set<string>();
        for(const m of physical)for(const n of physical){if(m.id===n.id||gone.has(n.id)||!dominates(n.writes,m.writes))continue;const mutual=dominates(m.writes,n.writes);if(mutual&&Buffer.compare(unb64u(n.hash),unb64u(m.hash))>=0)continue;gone.add(m.id);break;}
        destroy.push(...gone);
        const left=physical.filter(row=>!gone.has(row.id)),coverage=new Map(left.flatMap(row=>[...row.writes])),all=new Map([...coverage,...queued.flatMap(row=>[...row.writes])]);
        const complete=dominates(coverage,kept),single=left.some(row=>dominates(row.writes,kept)),inFlight=dominates(all,kept)&&queued.length>0;
        const writes=[...kept.values()].map(entry=>entry.write),fits=recordPayload(unb64u(record),writes).length+54<=this.#limits.messageBytes;
        if(!inFlight&&(!complete||fits&&!single&&left.length>1)){
          const selected=complete?writes:[...kept.values()].filter(entry=>!dominates(all,new Map([[b64u(entry.id),entry]]))).map(entry=>entry.write);
          messages.push(...this.#sealRecord(route.scope,unb64u(record),selected,route.key,route.tag).map(message=>({...message,guard:route.guard})));
        }
      }
    }
    return{messages,destroy:[...new Set(destroy)]};
  }
  prepareLateWrites(installation:Uint8Array,clock:{millis:number;counter:number}|undefined,done:ReadonlySet<string>,outbox:readonly Message[]=[]):{messages:OutgoingMessage[];processed:string[];unshared:{scope:string;record:string;write:string}[];cancel:string[]}{
    bytes(installation,8);const messages:OutgoingMessage[]=[],processed:string[]=[],unshared:{scope:string;record:string;write:string}[]=[],cancel:string[]=[];
    const identity=this.certificate?this.#identity():undefined;if(!identity||!clock)return{messages,processed,unshared,cancel};
    const scopes:{scope:Uint8Array;epoch:Uint8Array;active:Uint8Array[];number:bigint;records:Map<string,RecordState>;entries:Entry[];listed:(w:Write)=>boolean;ref:(w:Write)=>Uint8Array|undefined;tag:string;key:Uint8Array;guard:ExportGuard}[]=[];
    for(const c of this.#collections.values()){
      const head=c.policy.head,epoch=head&&c.policy.epoch(head);if(!head||!epoch||c.policy.dead||!c.policy.inventoriesReady()||c.policy.exposed().length||!c.policy.key(epoch))continue;
      const entries=new Map<string,Entry>();for(const payload of c.payloads)if('writes'in payload.parsed&&!eq(payload.parsed.record,ROSTER_RECORD))for(const w of payload.parsed.writes){if(!this.#sourceVerified.has(w)){if(c.policy.sourceValidity(w).kind!=='valid')continue;this.#sourceVerified.add(w);}entries.set(b64u(writeID(w)),c.policy.entry(w));}
      const ref=c.policy.chain(head).find(id=>{const statement=c.policy.statement(id)!;return eq(c.policy.epoch(id)!,epoch)&&statement.members.some(m=>eq(m.uid,this.#participant)&&m.role!=='reader');});
      scopes.push({scope:c.policy.id,epoch,active:[epoch],number:BigInt(c.policy.statement(head)!.keyN),records:c.records,entries:[...entries.values()],listed:w=>c.policy.listed(epoch,w),ref:()=>ref,tag:collectionTag(c.policy.key(epoch)!,this.#registry.pin.hub,epoch),key:c.policy.key(epoch)!,guard:{type:'collection',collection:b64u(c.policy.id),epoch:b64u(epoch)}});
    }
    const homeHeads=this.#home.heads(),home=homeHeads[0];
    if(home&&this.homeReady){const entries=new Map<string,Entry>();for(const payload of this.#homePayloads)if('writes'in payload.parsed)for(const w of payload.parsed.writes){if(!this.#sourceVerified.has(w)){if(this.#home.sourceValidity(w).kind!=='valid')continue;this.#sourceVerified.add(w);}entries.set(b64u(writeID(w)),this.#home.entry(w));}
      scopes.push({scope:homeScope(this.#registry.pin.hub,this.#participant),epoch:home,active:homeHeads,number:BigInt(this.#home.object(home)!.fields.n as number),records:this.#homeRecords,entries:[...entries.values()],listed:w=>homeHeads.some(head=>this.#home.listed(head,w)),ref:()=>home,tag:homeTag(this.#home.key(home)!,this.#registry.pin.hub,this.#participant,home),key:this.#home.key(home)!,guard:{type:'home',epoch:b64u(home)}});
    }
    const own=(w:Write):boolean=>{const cert=this.#registry.certificate(w.certificate);return eq(w.author,this.#participant)&&eq(w.dot.device,installation)&&!!cert&&eq(cert.participant,this.#participant)&&eq(cert.sign,publicKey(this.#keys.signing))&&eq(cert.agree,publicKey(this.#keys.agreement))&&this.#registry.activity(cert.id).kind==='valid';};
    const high=(d:Dot)=>d.millis>clock.millis||d.millis===clock.millis&&d.counter>clock.counter;
    for(const s of scopes){const touched=new Map<string,Write[]>();
      for(const x of s.entries){const w=x.write,id=b64u(x.id);if(s.active.some(epoch=>eq(x.epoch,epoch))||!own(w)||done.has(id))continue;
        processed.push(id);if(s.listed(w))continue;
        const genuine=s.entries.some(o=>{if(!eq(o.write.record,w.record)||!supersedes(o,x))return false;const candidate=o.write,ownLater=eq(candidate.dot.device,installation)&&(candidate.dot.millis>w.dot.millis||candidate.dot.millis===w.dot.millis&&candidate.dot.counter>w.dot.counter);return ownLater||![...candidate.context,...candidate.base].some(dot=>eq(dot.device,installation)&&high(dot));});
        const record=b64u(w.record),base=join(w.base,w.context,[w.dot]),ref=s.ref(w);
        if(genuine||base.length>MAX_VECTOR||!ref){let kept=false;const old=new RecordState(this.#registry.pin.hub,s.scope,w.record);old.merge(...s.entries.filter(e=>eq(e.write.record,w.record)&&eq(e.epoch,x.epoch)));kept=old.kept.some(e=>eq(e.id,x.id));if(kept)unshared.push({scope:b64u(s.scope),record,write:id});continue;}
        const resigned=signWrite({...w,certificate:identity.certificate.id,authorization:identity.auth,ref,context:[],base,signature:new Uint8Array(64)},this.#keys.signing);
        const writes=touched.get(record)??[];writes.push(resigned);touched.set(record,writes);
      }
      for(const [record,writes]of touched)messages.push(...this.#sealRecord(s.scope,unb64u(record),writes,s.key,s.tag).map(message=>({...message,guard:s.guard})));
    }
    const settled=new Set([...done,...processed]);
    for(const message of outbox){for(const s of scopes){let source:{epoch:Uint8Array;key:Uint8Array;tag:string}|undefined;
        if(s.guard.type==='collection'){const c=this.#collections.get(s.guard.collection)!.policy;source=c.readableKeys().map(({epoch,key})=>({epoch,key,tag:collectionTag(key,this.#registry.pin.hub,epoch)})).find(candidate=>candidate.tag===message.channel&&!eq(candidate.epoch,s.epoch));}
        else source=this.#home.readableKeys().map(({epoch,key})=>({epoch,key,tag:homeTag(key,this.#registry.pin.hub,this.#participant,epoch)})).find(candidate=>candidate.tag===message.channel&&!homeHeads.some(head=>eq(head,candidate.epoch)));
        if(!source)continue;try{const opened=openEnvelope(source.key,this.#registry.pin.hub,source.tag,message.body);if(opened.kind!==1)continue;const p=parseKind1(opened.plaintext,this.#registry.pin.hub,s.scope);if(!('writes'in p)||eq(p.record,ROSTER_RECORD))continue;if(p.writes.every(w=>!own(w)||settled.has(b64u(writeID(w)))))cancel.push(message.id);}catch{}
      }}
    return{messages:messages.length?[...identity.messages,...messages]:[],processed,unshared,cancel:[...new Set(cancel)]};
  }
  intentReady(intent:OperationIntent):boolean{
    if(intent.type==='capsule-cleanup')return true;
    if(['settle-collection','invitation-home','pairing-home'].includes(intent.type))return this.homeReady;
    return !!this.certificate;
  }
  prepareIntent(intent:OperationIntent,reserve:()=>Dot,context:{deliveries?:readonly Message[];outbox?:readonly Message[]}={}):{messages:OutgoingMessage[];destroy?:string[];cancel?:string[]}{
    if(intent.type==='capsule-cleanup')return{messages:[],destroy:this.#management.cleanup(new Set(parseJSON(Buffer.from(intent.values.ids!)) as string[]),context.deliveries??[])};
    const v=intent.values,cid=v.collection?unb64u(v.collection):undefined;
    if(intent.type==='pairing-home')return{messages:this.prepareHome(`pairing:${v.token}`,[{field:'delegation',op:1,value:unb64u(v.delegation!)},{field:'label',op:1,value:Buffer.from(v.label!)},{field:'expires',op:1,value:Buffer.from(v.expires!)}],reserve)};
    if(intent.type==='invitation-home')return{messages:this.prepareHome(v.name!,[{field:'label',op:1,value:Buffer.from(v.label!)},{field:'capability',op:1,value:unb64u(v.capability!)},{field:'made',op:1,value:Buffer.from(v.made!)},{field:'hint',op:1,value:Buffer.from(v.hint!)}],reserve)};
    if(intent.type==='settle-collection'){
      const name=`membership:${v.collection}`,epoch=unb64u(v.epoch!),key=unb64u(v.key!),edits:FieldEdit[]=[];
      const element=Buffer.from(JSON.stringify({epoch:b64u(epoch),k:b64u(key)}));
      if(!this.homeRecord(name)?.elements('keys').some(value=>eq(value,element)))edits.push({field:'keys',op:2,element});
      const flight=this.homeRecord(name)?.value('flight');if(flight){try{if(obj(parseJSON(flight)).stmt===v.epoch)edits.push({field:'flight',op:1,value:Buffer.from('null')});}catch{}}
      return{messages:edits.length?this.prepareHome(name,edits,reserve):[]};
    }
    if(intent.type==='collection-grants')return{messages:this.#grants(cid!,unb64u(v.at!),unb64u(v.key!))};
    if(intent.type==='collection-cleanup'){
      const c=this.#collections.get(v.collection!)!,epoch=unb64u(v.epoch!),head=c.policy.head,current=head&&c.policy.epoch(head),oldEpoch=unb64u(v.old!),oldKey=c.policy.key(oldEpoch),key=c.policy.key(epoch);
      if(!current||!eq(current,epoch)||!oldKey||!key)return{messages:[]};
      const oldTag=collectionTag(oldKey,this.#registry.pin.hub,oldEpoch),tag=collectionTag(key,this.#registry.pin.hub,epoch),copies=new Map<string,Write[]>(),destroy:string[]=[],cancel:string[]=[];
      const inNew=new Set<string>();for(const message of context.deliveries??[])if(message.channel===tag){try{const o=openEnvelope(key,this.#registry.pin.hub,tag,message.body);if(o.kind===1){const p=parseKind1(o.plaintext,this.#registry.pin.hub,cid!);if('writes'in p)for(const w of p.writes)if(c.policy.dataValidity(w,epoch).kind==='valid')inNew.add(b64u(writeID(w)));}}catch{}}
      const scan=(message:Message,local:boolean)=>{
        if(message.channel!==oldTag)return;try{const o=openEnvelope(oldKey,this.#registry.pin.hub,oldTag,message.body);if(o.kind!==1)return;const p=parseKind1(o.plaintext,this.#registry.pin.hub,cid!);if(!('writes'in p)||eq(p.record,ROSTER_RECORD)||!p.writes.length)return;
          const checks=p.writes.map(w=>c.policy.sourceValidity(w));if(checks.some(check=>check.kind==='pending')||!checks.some(check=>check.kind==='valid'))return;
          if(local&&!p.writes.every(w=>c.policy.listed(epoch,w)||c.records.get(b64u(w.record))?.kept.some(e=>{try{return supersedes(e,c.policy.entry(w));}catch{return false;}})))return;
          for(const w of p.writes)if(c.policy.listed(epoch,w)&&!inNew.has(b64u(writeID(w)))&&c.policy.sourceValidity(w).kind==='valid'){const writes=copies.get(b64u(w.record))??[];writes.push(w);copies.set(b64u(w.record),writes);inNew.add(b64u(writeID(w)));}
          (local?cancel:destroy).push(message.id);
        }catch{}
      };
      for(const message of context.deliveries??[])scan(message,false);for(const message of context.outbox??[])scan(message,true);
      const messages=[...copies].flatMap(([record,writes])=>this.#sealRecord(cid!,unb64u(record),writes,key,tag).map(message=>({...message,guard:{type:'collection' as const,collection:v.collection!,epoch:v.epoch!}})));
      return{messages,destroy,cancel};
    }
    throw new Error('unknown operation intent');
  }
  #grants(cid:Uint8Array,at:Uint8Array,key:Uint8Array,known:ReadonlySet<string>=new Set()):OutgoingMessage[]{
    const identity=this.#identity(),c=this.#collections.get(b64u(cid))?.policy,s=c?.statement(at),epoch=c?.epoch(at);if(!c||!s||!epoch||!c.owner||!eq(c.owner,this.#participant)||!eq(s.commit,commit(key)))throw new Error('grant requires the fixed owner and exact epoch key');
    const hub=this.#registry.pin.hub,messages=[...identity.messages];
    for(const member of s.members)if(!eq(member.uid,this.#participant)){
      const admission=c.admission(member.uid,at);if(!admission)continue;
      const fields={hub,collection:cid,recipient:member.uid,admission,at,epoch,key:commit(key),signer:identity.certificate.id,auth:identity.auth},grant=controlToJSON('grant',fields,{sig:signControl('grant',fields,this.#keys.signing)}),channel=inboxTag(member.uid,hub);
      for(const target of this.#registry.allCertificates)if(eq(target.participant,member.uid)&&this.#registry.activity(target.id).kind==='valid'&&!known.has(deliveryKey({type:'recipient',certificate:b64u(target.id),collection:b64u(cid),epoch:b64u(epoch)})!))messages.push({channel,body:sealInbox(hub,member.uid,target.agree,Buffer.from(JSON.stringify({type:'grant',grant:parseJSON(grant),certificate:b64u(target.id),k:b64u(key)}))),guard:{type:'recipient',certificate:b64u(target.id),collection:b64u(cid),epoch:b64u(epoch)}});
    }return messages;
  }
  prepareCollectionRotation(cid:Uint8Array,reserve:()=>Dot,edit:{remove?:Uint8Array;role?:{participant:Uint8Array;role:'writer'|'reader'}}={}):PreparedOperation{
    const identity=this.#identity(),state=this.#collections.get(b64u(cid)),c=state?.policy,head=c?.head,current=head&&c?.statement(head),old=head&&c?.epoch(head);
    if(!state||!c||!head||!current||!old||!c.owner||!eq(c.owner,this.#participant)||c.dead||current.deleted||!c.inventoriesReady()||!this.homeReady)throw new Error('rotation requires the owner, complete inventory and a private home');
    const existing=this.homeRecord(`membership:${b64u(cid)}`)?.value('flight');if(existing){try{if(obj(parseJSON(existing)).stmt)throw new Error('another key change is pending');}catch(error){if(error instanceof Error&&error.message==='another key change is pending')throw error;}}
    if(edit.remove&&eq(edit.remove,this.#participant)||edit.role&&eq(edit.role.participant,this.#participant))throw new Error('the fixed owner cannot be removed or demoted');
    const oldKey=c.key(old)!;let members=current.members.filter(member=>!edit.remove||!eq(member.uid,edit.remove));
    if(edit.role){if(!members.some(member=>eq(member.uid,edit.role!.participant)))throw new Error('participant is not a member');members=members.map(member=>eq(member.uid,edit.role!.participant)?{...member,role:edit.role!.role}:member);}
    const proofs:OutgoingMessage[]=[],hub=this.#registry.pin.hub;
    members=members.map(member=>{const snapshot=this.#registry.currentSnapshot(member.uid),floor=objectID(snapshot.kind,snapshot.fields);if(!this.#registry.snapshot(floor))proofs.push({channel:directoryTag(this.#directory,hub),body:seal(directoryKey(this.#directory,hub),hub,directoryTag(this.#directory,hub),controlToJSON(snapshot.kind,snapshot.fields),3),guard:{type:'control'}});return{...member,floor};});
    const entries=new Map<string,Uint8Array[]>(),known=new Set<string>();for(const payload of state.payloads)if('writes'in payload.parsed)for(const w of payload.parsed.writes)if(!eq(w.record,ROSTER_RECORD)&&c.dataValidity(w,payload.epoch).kind==='valid')known.add(b64u(writeID(w)));
    for(const record of state.records.values())if(record.kept.length)entries.set(Buffer.from(record.kept[0]!.write.record).toString('hex'),record.kept.map(entry=>entry.id));
    for(const [record,ids]of c.inventory(old)??[])for(const id of ids)if(!known.has(b64u(id))){const list=entries.get(record)??[];if(!list.some(value=>eq(value,id)))list.push(id);entries.set(record,list);}
    const inventory=inventoryBytes(entries),key=randomBytes(32),{nonce,...base}=current,next={...base,n:current.n+1,keyN:current.keyN+1,commit:commit(key),prev:current.commit,snapshot:sha(inventory),members};
    const statement=signWrite({hub,scope:cid,record:ROSTER_RECORD,field:'s',op:1,author:this.#participant,certificate:identity.certificate.id,authorization:identity.auth,dot:reserve(),value:statementJSON(next),element:null,ref:head,context:[],base:[],signature:new Uint8Array(64)},this.#keys.signing),epoch=writeID(statement),tag=collectionTag(key,hub,epoch);
    const note=Buffer.from(JSON.stringify({parent:b64u(head),stmt:b64u(epoch),k:b64u(key),members:next.members.sort((a,b)=>Buffer.compare(a.uid,b.uid)).map(m=>({uid:b64u(m.uid),role:m.role,name:m.name,floor:b64u(m.floor)})),what:edit.remove?'Remove member':edit.role?'Change role':'Change key',device:identity.certificate.label}));
    const noteMessages=this.prepareHome(`membership:${b64u(cid)}`,[{field:'flight',op:1,value:note}],reserve);
    const stage=(plain:Uint8Array):OutgoingMessage=>({channel:tag,body:seal(key,hub,tag,plain),guard:{type:'action',id:''},deferred:true});
    const upload=[...proofs,...inventoryPayloads(inventory,epoch).map(stage)];for(const record of state.records.values())if(record.kept.length)upload.push(...this.#sealRecord(cid,record.kept[0]!.write.record,record.kept.map(entry=>entry.write),key,tag).map(message=>({...message,guard:{type:'action' as const,id:''},deferred:true})));
    upload.push(stage(recordPayload(ROSTER_RECORD,[...c.writes(head),statement])));
    const values={collection:b64u(cid),epoch:b64u(epoch),key:b64u(key),old:b64u(old),at:b64u(epoch)};
    return{kind:'collection-rotation',details:{...values,parent:b64u(head),floors:JSON.stringify(members.map(member=>({participant:b64u(member.uid),floor:b64u(member.floor)})))},phases:[
      {name:'publish-change-note',messages:noteMessages},{name:'upload-collection-inventory',messages:upload},
      {name:'announce-collection',messages:[{channel:collectionTag(oldKey,hub,old),body:seal(oldKey,hub,collectionTag(oldKey,hub,old),recordPayload(ROSTER_RECORD,[statement])),guard:{type:'control'},deferred:true}],requires:[1]},
      {name:'settle-home',messages:[],intent:{type:'settle-collection',values}},{name:'deliver-collection-grants',messages:[],intent:{type:'collection-grants',values},requires:[1]},
      {name:'clean-old-channel',messages:[],intent:{type:'collection-cleanup',values},requires:[1]}
    ]};
  }
  prepareSetRole(cid:Uint8Array,participant:Uint8Array,role:'writer'|'reader',reserve:()=>Dot):PreparedOperation{
    const state=this.#collections.get(b64u(cid)),head=state?.policy.head,s=head&&state?.policy.statement(head),before=s?.members.find(member=>eq(member.uid,participant));
    if(!before||before.role==='owner'||role!=='writer'&&role!=='reader')throw new Error('role change requires a non-owner member and writer or reader role');
    if(before.role==='writer'&&role==='reader'||state!.policy.exposed().length)return this.prepareCollectionRotation(cid,reserve,{role:{participant,role}});
    return this.#sameEpoch(cid,reserve,statement=>({...statement,members:statement.members.map(member=>eq(member.uid,participant)?{...member,role}:member)}));
  }
  prepareDeleteList(cid:Uint8Array,reserve:()=>Dot):PreparedOperation{return this.#sameEpoch(cid,reserve,statement=>({...statement,deleted:true}));}
  #sameEpoch(cid:Uint8Array,reserve:()=>Dot,edit:(statement:import('./roster.js').Statement)=>import('./roster.js').Statement):PreparedOperation{
    const identity=this.#identity(),c=this.#collections.get(b64u(cid))?.policy,head=c?.head,s=head&&c?.statement(head),epoch=head&&c?.epoch(head);
    if(!c||!head||!s||!epoch||c.dead||s.deleted||!c.owner||!eq(c.owner,this.#participant)||c.exposed().length)throw new Error('roster changes require the current owner and an unexposed key');
    const key=c.key(epoch);if(!key)throw new Error('current collection key missing');const {nonce,...base}=s,next=edit({...base,n:s.n+1});
    const w=signWrite({hub:this.#registry.pin.hub,scope:cid,record:ROSTER_RECORD,field:'s',op:1,author:this.#participant,certificate:identity.certificate.id,authorization:identity.auth,dot:reserve(),value:statementJSON(next),element:null,ref:head,context:[],base:[],signature:new Uint8Array(64)},this.#keys.signing);
    const tag=collectionTag(key,this.#registry.pin.hub,epoch);return{kind:'roster-change',details:{collection:b64u(cid),parent:b64u(head),statement:b64u(writeID(w))},phases:[{name:'publish-roster',messages:[...identity.messages,{channel:tag,body:seal(key,this.#registry.pin.hub,tag,recordPayload(ROSTER_RECORD,[w])),guard:{type:'control'},deferred:true}]}]};
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
