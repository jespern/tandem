import {Buffer} from 'buffer';
import {bytes,b64u,unb64u,namedID,commit,sha} from './crypto.js';
import {parseJSON,validUnicode} from './json.js';
import {type Write,writeID,verifyWrite,Entry,recordPayload,parseKind1,parseInventory} from './core.js';
import {Registry,type Validity} from './registry.js';

export interface RosterMember {uid:Uint8Array;role:'owner'|'writer'|'reader';name:string;floor:Uint8Array}
export interface Statement {n:number;keyN:number;commit:Uint8Array;prev?:Uint8Array;snapshot?:Uint8Array;nonce?:Uint8Array;members:RosterMember[];deleted:boolean}
export const ROSTER_RECORD=namedID('_roster'),META_RECORD=namedID('_meta');
const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
const sameOptional=(a:Uint8Array|undefined,b:Uint8Array|undefined)=>a===undefined?b===undefined:b!==undefined&&eq(a,b);
const valid:Validity=Object.freeze({kind:'valid'}),bad=(reason:string):Validity=>({kind:'invalid',reason}),hold=(...missing:Uint8Array[]):Validity=>({kind:'pending',missing:missing.map(x=>bytes(x))});
const all=(checks:Validity[]):Validity=>checks.find(c=>c.kind==='invalid')??(checks.some(c=>c.kind==='pending')?hold(...checks.flatMap(c=>c.kind==='pending'?c.missing:[])):valid);
const object=(value:unknown):Record<string,any>=>{if(!value||typeof value!=='object'||Array.isArray(value))throw new Error('roster object required');return value;};
const raw=(value:unknown,size:number):Uint8Array=>{if(typeof value!=='string')throw new Error('roster bytes required');return bytes(unb64u(value),size);};
const optional=(value:Record<string,any>,name:string,size:number):Uint8Array|undefined=>Object.hasOwn(value,name)?raw(value[name],size):undefined;
const uint=(value:unknown):number=>{if(typeof value!=='number'||!Number.isInteger(value)||value<0||value>2**53)throw new Error('invalid roster counter');return value;};
export function parseStatement(data:Uint8Array):Statement {
  const j=object(parseJSON(data)),key=object(j.key);if(!Array.isArray(j.members)||typeof j.deleted!=='boolean')throw new Error('invalid roster members or deletion flag');
  const members:RosterMember[]=j.members.map(value=>{const m=object(value);if(!['owner','writer','reader'].includes(m.role)||typeof m.name!=='string'||new TextEncoder().encode(m.name).length>256)throw new Error('invalid roster member');
    return{uid:raw(m.uid,32),role:m.role,name:m.name,floor:raw(m.floor,32)};});
  if(members.filter(m=>m.role==='owner').length!==1||members.some((m,i)=>i>0&&Buffer.compare(members[i-1]!.uid,m.uid)>=0))throw new Error('roster requires one owner and sorted unique IDs');
  const result:Statement={n:uint(j.n),keyN:uint(key.n),commit:raw(key.commit,32),members,deleted:j.deleted};
  for(const [name,value]of Object.entries({prev:optional(key,'prev',32),snapshot:optional(j,'snapshot',32),nonce:optional(j,'nonce',16)}))if(value!==undefined)(result as unknown as Record<string,unknown>)[name]=value;
  return result;
}
export function statementJSON(value:Statement):Uint8Array {
  for(const member of value.members)validUnicode(member.name);
  const data=Buffer.from(JSON.stringify({n:value.n,key:{n:value.keyN,commit:b64u(value.commit),...(value.prev!==undefined?{prev:b64u(value.prev)}:{})},
    ...(value.snapshot!==undefined?{snapshot:b64u(value.snapshot)}:{}),...(value.nonce!==undefined?{nonce:b64u(value.nonce)}:{}),
    members:[...value.members].sort((a,b)=>Buffer.compare(a.uid,b.uid)).map(m=>({uid:b64u(m.uid),role:m.role,name:m.name,floor:b64u(m.floor)})),deleted:value.deleted}));
  parseStatement(data);return data;
}
export const isGenesis=(s:Statement):boolean=>s.n===0&&s.keyN===0&&s.prev===undefined&&s.snapshot===undefined&&s.nonce?.length===16&&s.members.length===1&&!s.deleted;
interface Row {id:Uint8Array;write:Write;statement:Statement}
export interface CollectionLimits{maxStatements?:number;maxCandidateEpochs?:number;maxKeysPerEpoch?:number}
export class CollectionHoldingLimit extends Error {}

/** One authenticated roster and its epoch inventories. No I/O or local clock. */
export class CollectionPolicy {
  readonly #id:Uint8Array;readonly #registry:Registry;
  readonly #limits:Required<CollectionLimits>;
  readonly #rows=new Map<string,Row>();readonly #decisions=new Map<string,Set<string>>();readonly #announced=new Set<string>();
  readonly #keys=new Map<string,Uint8Array>();readonly #candidates=new Map<string,Uint8Array[]>();readonly #inventories=new Map<string,Map<string,Buffer[]>>();
  #genesis:string|undefined;#revision=0;
  get revision():number{return this.#revision;}
  constructor(id:Uint8Array,registry:Registry,limits:CollectionLimits={}){this.#id=bytes(id,16);this.#registry=registry;this.#limits={maxStatements:limits.maxStatements??100000,maxCandidateEpochs:limits.maxCandidateEpochs??1024,maxKeysPerEpoch:limits.maxKeysPerEpoch??8};for(const n of Object.values(this.#limits))if(!Number.isSafeInteger(n)||n<1)throw new Error('invalid collection policy limit');}
  get id():Uint8Array{return bytes(this.#id);}
  get genesis():Uint8Array|undefined{return this.#genesis?bytes(this.#rows.get(this.#genesis)!.id):undefined;}
  get owner():Uint8Array|undefined{const row=this.#genesis?this.#rows.get(this.#genesis):undefined;return row?bytes(row.statement.members.find(m=>m.role==='owner')!.uid):undefined;}
  get dead():boolean{return [...this.#decisions.values()].some(ids=>ids.size>1);}
  #starts(row:Row):boolean{const parent=this.#rows.get(b64u(row.write.ref));return parent?!eq(parent.statement.commit,row.statement.commit):b64u(row.id)===this.#genesis;}
  #counts(row:Row):boolean{return b64u(row.id)===this.#genesis||!this.#starts(row)||this.#announced.has(b64u(row.id));}
  epoch(id:Uint8Array):Uint8Array|undefined{
    let row=this.#rows.get(b64u(id));const seen=new Set<string>();while(row){const key=b64u(row.id);if(seen.has(key))return;seen.add(key);const parent=this.#rows.get(b64u(row.write.ref));if(!parent||!eq(parent.statement.commit,row.statement.commit))return bytes(row.id);row=parent;}return;
  }
  get head():Uint8Array|undefined{
    if(!this.#genesis)return;let row=this.#rows.get(this.#genesis)!;const seen=new Set<string>();
    while(!seen.has(b64u(row.id))){if((this.#decisions.get(b64u(row.id))?.size??0)>1)break;seen.add(b64u(row.id));const next=[...this.#rows.values()].filter(child=>eq(child.write.ref,row.id)&&this.#counts(child));if(next.length!==1)break;row=next[0]!;}return bytes(row.id);
  }
  statement(id:Uint8Array):Statement|undefined{const row=this.#rows.get(b64u(id));return row?parseStatement(row.write.value):undefined;}
  key(epoch:Uint8Array):Uint8Array|undefined{const key=this.#keys.get(b64u(epoch));return key?bytes(key):undefined;}
  readableKeys():{epoch:Uint8Array;key:Uint8Array}[]{return [...this.#keys].map(([epoch,key])=>({epoch:unb64u(epoch),key:bytes(key)})).concat([...this.#candidates].flatMap(([epoch,keys])=>keys.map(key=>({epoch:unb64u(epoch),key:bytes(key)}))));}
  offerKey(epoch:Uint8Array,key:Uint8Array):void{
    bytes(epoch,32);bytes(key,32);const id=b64u(epoch);if(this.#keys.has(id))return;const candidates=this.#candidates.get(id)??[];
    const row=this.#rows.get(id);if(row&&this.#starts(row)&&eq(commit(key),row.statement.commit)){this.#keys.set(id,bytes(key));this.#candidates.delete(id);return;}
    if(candidates.some(old=>eq(old,key)))return;
    if(candidates.length>=this.#limits.maxKeysPerEpoch||!candidates.length&&this.#candidates.size>=this.#limits.maxCandidateEpochs)throw new CollectionHoldingLimit('collection key candidate holding limit exceeded');
    this.#candidates.set(id,[...candidates,bytes(key)]);this.#confirm(id);
  }
  #confirm(id:string):void{const row=this.#rows.get(id);if(!row||!this.#starts(row))return;const key=this.#candidates.get(id)?.find(key=>eq(commit(key),row.statement.commit));if(key){this.#keys.set(id,key);this.#candidates.delete(id);}}
  #decision(parent:Uint8Array,id:Uint8Array):void{const key=b64u(parent),ids=this.#decisions.get(key)??new Set<string>();if(!ids.has(b64u(id))){ids.add(b64u(id));this.#revision++;}this.#decisions.set(key,ids);}
  accept(input:Write,channel:Uint8Array):Validity {
    if(this.dead)return bad('collection has conflicting owner decisions');
    if(!eq(input.hub,this.#registry.pin.hub)||!eq(input.record,ROSTER_RECORD)||input.field!=='s'||input.op!==1)return bad('not a roster write');
    const genesis=input.ref.every(x=>x===0),w={...input,scope:genesis?new Uint8Array(16):input.scope};
    if(!genesis&&!eq(w.scope,this.#id))return bad('wrong collection scope');
    const authorization=this.#registry.authorizes(w.authorization,w.certificate);if(authorization.kind!=='valid')return authorization;
    const cert=this.#registry.certificate(w.certificate);if(!cert||!eq(cert.participant,w.author)||!verifyWrite(w,cert.sign))return bad('bad roster signature');
    const id=writeID(w),key=b64u(id),parent=this.#rows.get(b64u(w.ref));
    if(!genesis){if(!parent||!this.owner)return hold(w.ref);if(!eq(w.author,this.owner))return bad('only the fixed owner may decide the roster');
      // A historically authorized owner-signed conflicting decision counts even
      // when its roster value is invalid, once published in the parent epoch.
      const parentEpoch=this.epoch(parent.id);if(parentEpoch&&eq(channel,parentEpoch))this.#decision(parent.id,id);
    }
    const known=this.#rows.get(key);
    if(known){const parentEpoch=parent?this.epoch(parent.id):undefined;if(this.#starts(known)&&parentEpoch&&eq(parentEpoch,channel)){if(!this.#announced.has(key))this.#revision++;this.#announced.add(key);}return valid;}
    let s:Statement;try{s=parseStatement(w.value);}catch{return bad('invalid roster value');}
    if(w.element!==null||w.context.length||w.base.length)return bad('invalid roster vectors or element');
    const floors:Uint8Array[]=[];
    if(genesis){if(!isGenesis(s)||!eq(id.subarray(0,16),this.#id)||!eq(s.members[0]!.uid,w.author))return bad('invalid collection genesis');if(this.#genesis&&this.#genesis!==key)return bad('another collection genesis');}
    else{
      const p=parent!.statement,owner=s.members.find(m=>m.role==='owner')!;
      if(s.n!==p.n+1||p.deleted||!eq(owner.uid,this.owner!)||s.nonce!==undefined)return bad('invalid owner, sequence or deleted parent');
      floors.push(p.members.find(m=>m.role==='owner')!.floor);
      if(eq(s.commit,p.commit)){
        const rank=(role:RosterMember['role'])=>({reader:1,writer:2,owner:3})[role];
        if(s.keyN!==p.keyN||!sameOptional(s.prev,p.prev)||!sameOptional(s.snapshot,p.snapshot)||p.members.some(before=>{const after=s.members.find(m=>eq(m.uid,before.uid));return !after||!eq(after.floor,before.floor)||rank(after.role)<rank(before.role);}))return bad('access reduced without a new epoch');
      }else{
        if(s.keyN!==p.keyN+1||!s.prev||!eq(s.prev,p.commit)||!s.snapshot||[...this.#rows.values()].some(row=>eq(row.statement.commit,s.commit))||s.members.some(m=>!p.members.some(old=>eq(old.uid,m.uid))))return bad('invalid epoch start');
        floors.push(owner.floor);
      }
    }
    const extended=all(floors.map(floor=>this.#registry.extendsFloor(w.authorization,floor)));if(extended.kind!=='valid')return extended;
    const copied=parseKind1(recordPayload(w.record,[w]),w.hub,w.scope);if(!('writes'in copied))return bad('not a roster payload');
    if(this.#rows.size>=this.#limits.maxStatements)throw new CollectionHoldingLimit('roster statement holding limit exceeded');
    const row:Row={id:bytes(id),write:copied.writes[0]!,statement:s};this.#rows.set(key,row);this.#revision++;if(genesis)this.#genesis=key;
    const start=this.#starts(row),parentEpoch=parent?this.epoch(parent.id):undefined;
    if(!genesis&&(!start||(parentEpoch&&eq(parentEpoch,channel)))){this.#announced.add(key);this.#decision(w.ref,id);}
    this.#confirm(key);return valid;
  }
  announce(id:Uint8Array):void{const row=this.#rows.get(b64u(id));if(row){if(!this.#announced.has(b64u(id)))this.#revision++;this.#announced.add(b64u(id));if(this.#starts(row)&&b64u(id)!==this.#genesis)this.#decision(row.write.ref,id);}}
  addInventory(epoch:Uint8Array,data:Uint8Array):void{const row=this.#rows.get(b64u(epoch));if(!row?.statement.snapshot||!eq(sha(data),row.statement.snapshot))throw new Error('inventory does not match the signed commitment');this.#inventories.set(b64u(epoch),parseInventory(data));this.#revision++;}
  listed(epoch:Uint8Array,w:Write):boolean{return this.#inventories.get(b64u(epoch))?.get(Buffer.from(w.record).toString('hex'))?.some(id=>eq(id,writeID(w)))??false;}
  #precedes(ancestor:Uint8Array,descendant:Uint8Array):boolean{let row=this.#rows.get(b64u(descendant));const seen=new Set<string>();while(row){if(eq(row.write.ref,ancestor))return true;const key=b64u(row.id);if(seen.has(key))break;seen.add(key);row=this.#rows.get(b64u(row.write.ref));}return false;}
  dataValidity(w:Write,channel:Uint8Array):Validity{
    if(eq(w.record,ROSTER_RECORD))return bad('roster is not an ordinary data record');
    if(this.dead)return bad('collection has conflicting owner decisions');const row=this.#rows.get(b64u(w.ref));if(!row)return hold(w.ref);
    const source=this.epoch(row.id);if(!source||!eq(w.hub,this.#registry.pin.hub)||!eq(w.scope,this.#id))return bad('wrong hub or scope');
    if(!this.#counts(row)||!this.#counts(this.#rows.get(b64u(source))!))return hold(source);
    const member=row.statement.members.find(m=>eq(m.uid,w.author));if(!member||member.role==='reader'||eq(w.record,META_RECORD)&&member.role!=='owner')return bad('not a writer here');
    if(w.op===4&&(eq(w.record,META_RECORD)||eq(w.record,ROSTER_RECORD)))return bad('named records cannot be deleted');
    const checks=[this.#registry.authorizes(w.authorization,w.certificate)];
    if(eq(source,channel)){
      checks.push(this.#registry.extendsFloor(w.authorization,member.floor));
      const successors=[...this.#rows.values()].filter(child=>this.#starts(child)&&this.#announced.has(b64u(child.id))&&this.epoch(child.write.ref)&&eq(this.epoch(child.write.ref)!,source));
      if(successors.length&&!successors.some(child=>this.listed(child.id,w))){const missing=successors.filter(child=>!this.#inventories.has(b64u(child.id))).map(child=>child.id);checks.push(missing.length?hold(...missing):bad('late write in replaced epoch'));}
    }else{if(!this.#precedes(w.ref,channel))return bad('not an ancestor of this epoch');if(!this.#inventories.has(b64u(channel)))return hold(channel);if(!this.listed(channel,w))return bad('not carried into this epoch');}
    const authorized=all(checks);if(authorized.kind!=='valid')return authorized;const cert=this.#registry.certificate(w.certificate);
    return cert&&eq(cert.participant,w.author)&&verifyWrite(w,cert.sign)?valid:bad('bad data signature');
  }
  chain(at:Uint8Array):Uint8Array[]{
    const rows:Uint8Array[]=[],seen=new Set<string>();let row=this.#rows.get(b64u(at));
    while(row&&!seen.has(b64u(row.id))){seen.add(b64u(row.id));rows.push(bytes(row.id));row=this.#rows.get(b64u(row.write.ref));}
    return rows.reverse();
  }
  admission(participant:Uint8Array,at:Uint8Array):Uint8Array|undefined{
    let admission:Uint8Array|undefined,listed=false;
    for(const id of this.chain(at)){const now=this.#rows.get(b64u(id))!.statement.members.some(m=>eq(m.uid,participant));if(now&&!listed)admission=bytes(id);listed=now;}
    return listed?admission:undefined;
  }
  inventoriesReady():boolean{const head=this.head,epoch=head&&this.epoch(head);return !!epoch&&(eq(epoch,this.genesis!)||this.#inventories.has(b64u(epoch)));}
  eligible(w:Write):boolean{const head=this.head,current=head&&this.epoch(head),source=this.epoch(w.ref);return !!current&&!!source&&(eq(current,source)||this.listed(current,w));}
  exposed():Uint8Array[]{
    const head=this.head,s=head&&this.statement(head);if(!s)return [];
    return s.members.filter(member=>{const floor=this.#registry.snapshot(member.floor);return floor&&this.#registry.revocationIDs.some(id=>{
      const target=this.#registry.revocationTarget(id);return target&&eq(target.participant,member.uid)&&this.#registry.revocationValidity(id).kind==='valid'&&!floor.revocations.some(known=>eq(known,id));
    });}).map(member=>bytes(member.uid));
  }
  entry(w:Write):Entry{const epoch=this.epoch(w.ref),row=this.#rows.get(b64u(w.ref));if(!epoch||!row)throw new Error('missing source roster');return new Entry(w,epoch,BigInt(row.statement.keyN));}
}
