import {Buffer} from 'buffer';
import {b64u,unb64u,bytes,sha,randomBytes,directoryTag,directoryKey,namedID,seal} from '../crypto.js';
import {parseJSON} from '../json.js';
import {objectID,directoryScope,type Mailbox} from '../control.js';
import {prepareFounding,foundingCapsule} from '../founding.js';
import {DeviceState,DeviceHoldingLimit,type DeviceStateOptions,type FieldEdit} from '../device-state.js';
import {type PairingLink,type PairingProposal,type PairingKeys,parsePairingLink,pairingLinkJSON,pairingRegistry,proposePairing,releasePairing,pendingPairingJSON,parsePendingPairing} from '../pairing.js';
import type {Registry} from '../registry.js';
import type {Message} from '../session.js';
import {BrowserJournal,type LocalPlan,type LocalTransaction} from './journal.js';
import {signWrite,recordPayload,type Dot} from '../core.js';
import {deliveryKey,type OutgoingMessage,type PreparedOperation,type MailboxPresence} from '../operation.js';
import {newAction,sealAction,openAction,messageID,type DeviceAction,type ActionPhase} from './actions.js';
import {BrowserKeychain} from './keychain.js';
import {BrowserReplica,type PreparedStore,untilAbort} from './replica.js';
import {Invalidation} from './events.js';
import {openCapsule,sealCapsule,type CapsuleOptions} from './capsule.js';

interface Enrollment {link:PairingLink;label:string;proposal?:PairingProposal;complete?:{epoch:Uint8Array;key:Uint8Array;certificate:Uint8Array};founding?:{auth:Uint8Array;messages:OutgoingMessage[]}}
export interface BrowserFoundingOptions extends CapsuleOptions {setupID?:string;device?:DeviceStateOptions}
export type BrowserDeviceStatus='material'|'approval'|'home'|'ready'|'revoked';
export interface BrowserDeviceUpdate {status:BrowserDeviceStatus;revision:number}
export class StaleDeviceState extends Error {constructor(){super('new durable events arrived; replay them before signing');}}
const encode=(value:unknown):Buffer=>Buffer.from(JSON.stringify(value));
const raw=(value:unknown,n?:number)=>{if(typeof value!=='string')throw new Error('invalid enrollment bytes');return bytes(unb64u(value),n);};
function saveEnrollment(record:Enrollment):Uint8Array {
  return encode({v:1,link:parseJSON(pairingLinkJSON(record.link)),label:record.label,
    ...(record.proposal?{proposal:{pending:parseJSON(pendingPairingJSON(record.proposal.pending)),messages:record.proposal.messages.map(m=>({channel:m.channel,body:b64u(m.body)}))}}:{}),
    ...(record.complete?{complete:{epoch:b64u(record.complete.epoch),key:b64u(record.complete.key),certificate:b64u(record.complete.certificate)}}:{}),
    ...(record.founding?{founding:{auth:b64u(record.founding.auth),messages:record.founding.messages.map(m=>({...m,body:b64u(m.body)}))}}:{})});
}
function loadEnrollment(data:Uint8Array):Enrollment {
  const j=parseJSON(data) as Record<string,any>;if(j.v!==1||typeof j.label!=='string')throw new Error('invalid browser enrollment');
  const record:Enrollment={link:parsePairingLink(encode(j.link)),label:j.label};
  if(j.proposal){const pending=parsePendingPairing(encode(j.proposal.pending));if(!Array.isArray(j.proposal.messages))throw new Error('invalid pending messages');
    record.proposal={pending,messages:j.proposal.messages.map((m:Record<string,unknown>)=>{if(typeof m.channel!=='string')throw new Error('invalid pending channel');const body=raw(m.body);return{id:b64u(sha(body)),channel:m.channel,body};})};}
  if(j.complete)record.complete={epoch:raw(j.complete.epoch,32),key:raw(j.complete.key,32),certificate:raw(j.complete.certificate,32)};
  if(j.founding){if(!record.complete||!Array.isArray(j.founding.messages)||!j.founding.messages.length)throw new Error('invalid founding enrollment');record.founding={auth:raw(j.founding.auth,32),messages:j.founding.messages.map((m:Record<string,any>)=>{if(typeof m.channel!=='string')throw new Error('invalid founding channel');return{channel:m.channel,body:raw(m.body),...(m.guard?{guard:m.guard}:{})};})};}
  return record;
}
interface DeviceLocalState {v:2;pairingRequest?:string;founding?:string;processed:string[];unshared:Record<string,string>}
function localState(data:Uint8Array|undefined):DeviceLocalState{
  if(!data)return{v:2,processed:[],unshared:{}};
  const text=Buffer.from(data).toString();if(!text.startsWith('{'))return{v:2,pairingRequest:text,processed:[],unshared:{}};
  const j=parseJSON(data) as Record<string,any>;if(j.v!==2||!Array.isArray(j.processed)||j.processed.some((id:unknown)=>typeof id!=='string')||!j.unshared||typeof j.unshared!=='object'||Object.values(j.unshared).some(value=>typeof value!=='string'))throw new Error('invalid device local state');
  return{v:2,...(typeof j.pairingRequest==='string'?{pairingRequest:j.pairingRequest}:{}),...(typeof j.founding==='string'?{founding:j.founding}:{}),processed:j.processed,unshared:{...j.unshared}};
}
function foundingSlot(mailbox:Mailbox,setupID='default'):string{
  // Never include a token/password. The selected folder belongs in setupID.
  return `founding:${b64u(sha(encode([mailbox.host,mailbox.port,mailbox.user,setupID])))}`;
}
/** Browser device operations over the durable push pump. Enrollment secrets are
 * wrapped in the separate keychain; exact claims and edits commit before export.
 * This class owns the journal's opaque per-hub action state. */
export class BrowserDevice {
  readonly #journal:BrowserJournal;readonly #keychain:BrowserKeychain;readonly #keys:PairingKeys;readonly #options:DeviceStateOptions;
  readonly #kept=new Map<string,OutgoingMessage>();readonly #actionKey:Uint8Array;readonly #activeActions=new Map<string,DeviceAction>();readonly #present=new Map<string,Set<string>>();readonly #remote=new Map<string,string>();
  readonly #hub:string;readonly #slot:string;readonly #lock:string;readonly #replica:BrowserReplica;readonly #changes=new Invalidation();
  readonly #messages=new Map<string,Message>();#local:DeviceLocalState={v:2,processed:[],unshared:{}};#enrollment:Enrollment;#registry:Registry|undefined;#view:DeviceState|undefined;
  #sequence=0;#messageBytes=0;#lastLate='';#lastHygiene='';#presenceSequence=-1;#presenceCache:MailboxPresence[]=[];#closed=false;#running:Promise<void>|undefined;#abort:AbortController|undefined;#status:BrowserDeviceStatus='material';
  private constructor(journal:BrowserJournal,keychain:BrowserKeychain,keys:PairingKeys,enrollment:Enrollment,options:DeviceStateOptions,actionKey:Uint8Array){
    this.#actionKey=bytes(actionKey,32);
    this.#journal=journal;this.#keychain=keychain;this.#keys=keys;this.#enrollment=enrollment;this.#options=options;this.#hub=b64u(enrollment.link.hub);this.#slot=`device:${this.#hub}`;
    this.#lock=`tandem:device:${JSON.stringify([journal.name,this.#hub])}`;this.#replica=new BrowserReplica(journal,this.#hub);
  }
  /** Explicitly create a new authority. The caller chooses/checks its mailbox;
   * an empty or unreachable mailbox is never an invitation to replace a root.
   * Durable preparation precedes all export. Retrying the same setup reuses it. */
  static async found(mailbox:Mailbox,label:string,password:string,journal:BrowserJournal,keychain:BrowserKeychain,options:BrowserFoundingOptions={}):Promise<BrowserDevice>{
    const slot=foundingSlot(mailbox,options.setupID),keys=await keychain.deviceKeys();
    const saved=await navigator.locks.request(`tandem:${slot}`,{...(options.signal?{signal:options.signal}:{})},async()=>{
      const existing=await keychain.get(slot);if(existing)return existing;
      options.signal?.throwIfAborted();const f=prepareFounding(mailbox,keys,label);
      try{
        const sealed=await sealCapsule(password,f.pin.hub,f.managementSeed,options);options.signal?.throwIfAborted();
        const record:Enrollment={label,link:{hub:f.pin.hub,genesis:f.pin.genesis,directoryKey:f.directoryKey,participant:f.participant,
          secret:randomBytes(32),token:randomBytes(16),mail:{...mailbox,pass:''}},
          complete:{epoch:objectID(f.homeEpoch.kind,f.homeEpoch.fields),key:f.homeKey,certificate:objectID(f.certificate.kind,f.certificate.fields)},
          founding:{auth:objectID(f.snapshot.kind,f.snapshot.fields),messages:[...f.messages,foundingCapsule(f,sealed)]}};
        const encoded=saveEnrollment(record);await keychain.save(slot,encoded);return encoded;
      }finally{f.managementSeed.fill(0);}
    });
    return this.#resumeFounding(saved,journal,keychain,options.device??{});
  }
  /** Recover after preparation, including a crash before the app saved the hub ID.
   * No hub password or management seed is retained in this local setup record. */
  static async resumeFounding(mailbox:Mailbox,journal:BrowserJournal,keychain:BrowserKeychain,options:Pick<BrowserFoundingOptions,'setupID'|'device'>={}):Promise<BrowserDevice|undefined>{
    const saved=await keychain.get(foundingSlot(mailbox,options.setupID));return saved?this.#resumeFounding(saved,journal,keychain,options.device??{}):undefined;
  }
  static async #resumeFounding(saved:Uint8Array,journal:BrowserJournal,keychain:BrowserKeychain,options:DeviceStateOptions):Promise<BrowserDevice>{
    const record=loadEnrollment(saved);if(!record.founding)throw new Error('not a founding enrollment');
    const hub=b64u(record.link.hub),slot=`device:${hub}`,lock=`tandem:device:${JSON.stringify([journal.name,hub])}`;
    await navigator.locks.request(lock,async()=>{if(!await keychain.get(slot))await keychain.save(slot,saved);});
    return this.resume(record.link.hub,journal,keychain,options);
  }
  get hub():Uint8Array{return bytes(this.#enrollment.link.hub);}
  static async pair(input:PairingLink|string|Uint8Array,label:string,journal:BrowserJournal,keychain:BrowserKeychain,options:DeviceStateOptions={}):Promise<BrowserDevice>{
    const link=typeof input==='string'||input instanceof Uint8Array?parsePairingLink(input):parsePairingLink(pairingLinkJSON(input));
    const keys=await keychain.deviceKeys(),hub=b64u(link.hub),slot=`device:${hub}`,lock=`tandem:device:${JSON.stringify([journal.name,hub])}`;
    const enrollment=await navigator.locks.request(lock,async()=>{const saved=await keychain.get(slot);if(saved){const record=loadEnrollment(saved);if(b64u(record.link.participant)!==b64u(link.participant)||b64u(record.link.genesis)!==b64u(link.genesis))throw new Error('this browser already has another enrollment on this hub');return record;}
      const record={link,label};await keychain.save(slot,saveEnrollment(record));return record;});
    const device=new BrowserDevice(journal,keychain,keys,enrollment,options,await keychain.localSecret(`actions:${hub}`));await device.#lockedRefresh();return device;
  }
  static async resume(hub:Uint8Array,journal:BrowserJournal,keychain:BrowserKeychain,options:DeviceStateOptions={}):Promise<BrowserDevice>{
    const saved=await keychain.get(`device:${b64u(bytes(hub,32))}`);if(!saved)throw new Error('pair this browser first');
    const device=new BrowserDevice(journal,keychain,await keychain.deviceKeys(),loadEnrollment(saved),options,await keychain.localSecret(`actions:${b64u(hub)}`));await device.#lockedRefresh();return device;
  }
  get state():DeviceState|undefined{return this.#enrollment.complete?this.#view:undefined;}
  get update():BrowserDeviceUpdate{return{status:this.#status,revision:this.#view?.revision??0};}
  async *updates(signal:AbortSignal):AsyncGenerator<BrowserDeviceUpdate>{
    while(!signal.aborted&&!this.#closed){const changed=this.#changes.snapshot();yield this.update;await untilAbort(changed,signal);}
  }
  async #lockedRefresh():Promise<void>{await navigator.locks.request(this.#lock,()=>this.#refresh());}
  async #refresh():Promise<void>{
    if(this.#closed)throw new Error('browser device closed');
    const saved=await this.#keychain.get(this.#slot);if(saved)this.#enrollment=loadEnrollment(saved);
    const founding=this.#enrollment.founding;
    if(founding){
      const installation=await this.#journal.installation();
      await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{
        const local=localState(tx.state);if(local.founding===this.#hub)return{messages:[]};
        const {link,label,complete}=this.#enrollment,scope=directoryScope(link.hub),name=`profile:${b64u(link.participant)}`,profile=namedID(name);
        const writes=[['_name',name],['name',label]].map(([field,value])=>signWrite({hub:link.hub,scope,record:profile,field:field!,op:1,author:link.participant,
          certificate:complete!.certificate,authorization:founding.auth,dot:tx.nextDot(),value:Buffer.from(value!),element:null,ref:link.genesis,context:[],base:[],signature:new Uint8Array(64)},this.#keys.signing));
        const tag=directoryTag(link.directoryKey,link.hub),messages=[...founding.messages,{channel:tag,body:seal(directoryKey(link.directoryKey,link.hub),link.hub,tag,recordPayload(profile,writes)),guard:{type:'control' as const}}];
        const action=newAction('founding',[{name:'publish-hub',messages}]);local.founding=this.#hub;
        return{messages,state:encode(local),actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,link.hub)}]};
      });
    }
    let rows;do{rows=await this.#journal.readEvents(this.#hub,this.#sequence);const messages=rows.filter(row=>row.kind!=='removed'&&!(row.kind==='queued'&&row.deferred));
      for(const row of rows){if(row.kind==='queued'&&row.guard?.type==='recipient')this.#kept.set(row.id,{channel:row.channel,body:row.body,guard:row.guard});if(!row.remoteId)continue;const key=JSON.stringify([row.channel,row.id]);
        if(row.kind==='received'){const ids=this.#present.get(key)??new Set<string>();ids.add(row.remoteId);this.#present.set(key,ids);this.#remote.set(row.remoteId,key);}
        if(row.kind==='removed'){const previous=this.#remote.get(row.remoteId);if(previous){const ids=this.#present.get(previous);ids?.delete(row.remoteId);if(!ids?.size)this.#present.delete(previous);this.#remote.delete(row.remoteId);}}
      }
      if(this.#view)this.#view.ingest(messages);
      for(const message of messages)if(!this.#enrollment.complete||!this.#view){const id=JSON.stringify([message.channel,message.id]);if(this.#messages.has(id))continue;
        if(this.#messages.size>=(this.#options.maxMessages??100000)||this.#messageBytes+message.body.length>(this.#options.maxCachedBytes??64*1024*1024))throw new DeviceHoldingLimit('enrollment envelope holding limit exceeded');
        this.#messages.set(id,message);this.#messageBytes+=message.body.length;
      }
      if(rows.length)this.#sequence=rows[rows.length-1]!.sequence;
    }while(rows.length===128);
    const record=this.#enrollment;
    if(!this.#registry)this.#registry=pairingRegistry(record.link,[...this.#messages.values()]);
    if(this.#registry&&!this.#view){this.#view=new DeviceState(this.#registry,record.link.directoryKey,record.link.participant,this.#keys,this.#options);this.#view.ingest([...this.#messages.values()]);}
    if(this.#registry&&!record.proposal&&!record.complete){const proposal=proposePairing(record.link,this.#registry,[...this.#messages.values()],this.#keys,record.label);
      if(proposal){record.proposal=proposal;await this.#keychain.save(this.#slot,saveEnrollment(record));}}
    if(record.proposal&&!record.complete){
      const request=b64u(objectID('request',record.proposal.pending.request.fields)),installation=await this.#journal.installation();
      const queued=await this.#journal.queued(this.#hub,record.proposal.messages.map(messageID));
      const missing=record.proposal.messages.filter(message=>!this.#confirmed(message)&&!queued.has(messageID(message)));
      try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{
        if(tx.sequence!==this.#sequence)throw new StaleDeviceState();
        const local=localState(tx.state);if(local.pairingRequest===request)return{messages:missing};local.pairingRequest=request;
        return{messages:record.proposal!.messages,state:encode(local)};
      });}catch(error){if(error instanceof StaleDeviceState)return this.#refresh();throw error;}
      const released=this.#registry&&releasePairing(record.proposal.pending,this.#registry,[...this.#messages.values()],this.#keys);
      if(released){record.complete={epoch:released.epoch,key:released.key,certificate:released.certificate.id};await this.#keychain.save(this.#slot,saveEnrollment(record));}
    }
    if(record.complete&&this.#view){this.#view.offerHomeKey(record.complete.epoch,record.complete.key,true);this.#messages.clear();this.#messageBytes=0;}
    if(record.complete&&this.#view&&await this.#progressActions())return this.#refresh();
    if(record.complete&&this.#view&&await this.#settleLateWrites())return this.#refresh();
    if(record.complete&&this.#view&&await this.#hygiene())return this.#refresh();
    this.#local=localState(await this.#journal.state(this.#hub));
    const activity=record.complete&&this.#registry?.activity(record.complete.certificate);
    this.#status=record.complete?(activity&&activity.kind==='invalid'&&activity.reason==='revoked'?'revoked':this.#view?.certificate?(this.#view.homeReady?'ready':'home'):'material'):record.proposal?'approval':'material';
    this.#changes.signal();
  }
  /** The caller supplies a JMAP connection factory. One tab owns the connection;
   * all tabs replay journal events and may commit local edits. */
  run(connect:()=>Promise<PreparedStore>,signal:AbortSignal):Promise<void>{
    if(this.#running||this.#closed)throw new Error('browser device already running or closed');
    const abort=new AbortController();this.#abort=abort;const merged=AbortSignal.any([signal,abort.signal]);
    const transport=this.#replica.run(connect,merged,{beforeExport:()=>this.#lockedRefresh(),allowExport:entry=>{if(!this.#enrollment.complete)return !!this.#enrollment.proposal?.messages.some(message=>messageID(message)===entry.id);if(entry.guard?.type==='action'){const action=this.#activeActions.get(entry.guard.id);return !!action&&!action.details.failure&&!this.#view?.authorizationPending&&!!this.#view?.operationValid(action.kind,action.details);}return this.#view?.exportable(entry)??false;}});void transport.then(()=>{if(!merged.aborted)abort.abort(new Error('replica ended'));},error=>abort.abort(error));
    this.#running=(async()=>{try{while(!merged.aborted){const changed=this.#journal.eventsChanged();await this.#lockedRefresh();await untilAbort(changed,merged);}}
      catch(error){if(!signal.aborted&&!this.#closed)throw error;}finally{abort.abort();await transport.catch(()=>{});this.#running=undefined;this.#abort=undefined;}})();
    return this.#running;
  }
  async #commitPlan(prepare:(state:DeviceState,reserve:()=>Dot,tx:LocalTransaction)=>LocalPlan):Promise<void>{
    if(this.#closed)throw new Error('browser device closed');
    await navigator.locks.request(this.#lock,async()=>{while(true){await this.#refresh();if(!this.#enrollment.complete)throw new Error('pairing requires its exact gated release before device operations');const state=this.#view;if(!state)throw new Error('browser device has no verified state yet');
      const installation=await this.#journal.installation();
      try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{if(tx.sequence!==this.#sequence)throw new StaleDeviceState();return prepare(state,()=>tx.nextDot(),tx);});break;}
      catch(error){if(!(error instanceof StaleDeviceState))throw error;}
    }await this.#refresh();});
  }
  async #edit(prepare:(state:DeviceState,reserve:()=>Dot)=>import('../device-state.js').OutgoingMessage[]):Promise<void>{await this.#commitPlan((state,reserve)=>({messages:prepare(state,reserve)}));}
  async #settleLateWrites():Promise<boolean>{
    if(!this.#view?.certificate)return false;const installation=await this.#journal.installation(),stamp=`${b64u(installation)}:${this.#view.revision}`;if(stamp===this.#lastLate)return false;const pending=await this.#journal.allPending(this.#hub);let changed=false;
    try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{
      if(tx.sequence!==this.#sequence)throw new StaleDeviceState();const local=localState(tx.state),done=new Set(local.processed),prepared=this.#view!.prepareLateWrites(installation,tx.clock,done,pending);
      if(!prepared.messages.length&&!prepared.processed.length&&!prepared.cancel.length)return{messages:[]};
      for(const id of prepared.processed)done.add(id);local.processed=[...done];for(const held of prepared.unshared)local.unshared[`${held.scope}:${held.write}`]=held.record;
      changed=true;return{messages:prepared.messages,cancel:prepared.cancel,state:encode(local)};
    });}catch(error){if(error instanceof StaleDeviceState)return true;throw error;}this.#lastLate=stamp;return changed;
  }
  #presence():MailboxPresence[]{if(this.#presenceSequence===this.#sequence)return this.#presenceCache;this.#presenceSequence=this.#sequence;return this.#presenceCache=[...this.#remote].map(([id,key])=>{const [channel,hash]=JSON.parse(key) as [string,string];return{id,channel,hash};});}
  #confirmed(message:OutgoingMessage):boolean{return this.#present.has(JSON.stringify([message.channel,messageID(message)]))||!!this.#view?.messageCovered(message,this.#presence());}
  async #hygiene():Promise<boolean>{
    const stamp=`${this.#sequence}:${this.#view!.revision}`;if(stamp===this.#lastHygiene)return false;
    const presence=this.#presence(),pending=await this.#journal.pendingIDs(this.#hub),prepared=this.#view!.prepareHygiene(presence,pending),cancel:string[]=[];
    const groups=new Map<string,{id:string;message:OutgoingMessage}[]>();
    for(const [id,message]of this.#kept){const slot=deliveryKey(message.guard)!;const rows=groups.get(slot)??[];rows.push({id,message});groups.set(slot,rows);}
    for(const rows of groups.values()){
      const latest=rows[rows.length-1]!,confirmed=this.#present.has(JSON.stringify([latest.message.channel,latest.id]));
      for(const row of rows){const retired=this.#view!.deliveryRetired(row.message),obsolete=row!==latest&&confirmed;
        if(retired||obsolete){if(pending.has(row.id))cancel.push(row.id);prepared.destroy.push(...(this.#present.get(JSON.stringify([row.message.channel,row.id]))??[]));}
        else if(row===latest&&!confirmed&&!pending.has(row.id)&&this.#view!.exportable(row.message))prepared.messages.push(row.message);
      }
    }
    const blocked=new Set([...this.#activeActions.values()].filter(action=>!action.details.failure).flatMap(action=>action.details.collection?[action.details.collection]:[]));
    prepared.messages.push(...this.#view!.prepareKeyDeliveries(new Set(groups.keys()),blocked,[...this.#activeActions.values()].some(action=>action.kind==='home-rotation'&&!action.details.failure)));
    const deleting=await this.#journal.pendingDeleteIDs(this.#hub);prepared.destroy=[...new Set(prepared.destroy)].filter(id=>!deleting.has(id));
    const plan={...prepared,cancel};
    if(plan.messages.length||plan.destroy.length||cancel.length){const installation=await this.#journal.installation();try{
      await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{if(tx.sequence!==this.#sequence)throw new StaleDeviceState();return plan;});
    }catch(error){if(error instanceof StaleDeviceState)return true;throw error;}
      if(plan.messages.length)return true;
    }
    this.#lastHygiene=stamp;return false;
  }
  get unshared():readonly {scope:string;record:string;write:string}[]{return Object.entries(this.#local.unshared).map(([id,record])=>{const [scope,write]=id.split(':');return{scope:scope!,write:write!,record};});}
  async dismissUnshared(scope:Uint8Array,write:Uint8Array):Promise<void>{const key=`${b64u(scope)}:${b64u(write)}`;await this.#commitPlan((_state,_reserve,tx)=>{const local=localState(tx.state);delete local.unshared[key];return{messages:[],state:encode(local)};});}
  async #phasePlan(action:DeviceAction,phase:ActionPhase):Promise<(tx:LocalTransaction)=>LocalPlan>{
    const context=phase.intent?.type.endsWith('-cleanup')?{
      deliveries:(await this.#journal.deliveries(this.#hub)).filter(row=>row.present).map(row=>({id:row.remoteId,channel:row.channel,body:row.body})),
      outbox:await this.#journal.allPending(this.#hub)
    }:{};
    return tx=>{
      if(phase.intent){const prepared=this.#view!.prepareIntent(phase.intent,()=>tx.nextDot(),context);phase.messages=prepared.messages;if(prepared.destroy)phase.destroy=prepared.destroy;if(prepared.cancel)phase.cancel=prepared.cancel;delete phase.intent;}
      const immediate=!!phase.destroy?.length&&!phase.messages.length;if(immediate)phase.destroyQueued=true;
      return{messages:phase.messages,actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}],...(phase.cancel?{cancel:phase.cancel}:{}),...(immediate?{destroy:phase.destroy!}:{})};
    };
  }
  async #progressActions():Promise<boolean>{
    const saved=await this.#journal.actions(this.#hub);this.#activeActions.clear();
    for(const row of saved){const action=openAction(row.id,row.sealed,this.#actionKey,this.#enrollment.link.hub);this.#activeActions.set(row.id,action);
      if(action.kind==='home-rotation')this.#view!.offerHomeKey(unb64u(action.details.epoch!),unb64u(action.details.key!));
    }
    for(const action of this.#activeActions.values()){
      if(action.details.failure||this.#view!.authorizationPending&&!['admin-revoke','admin-receipt','admin-password'].includes(action.kind))continue;
      const phase=action.phases[action.index]!,deleting=phase.destroyQueued?await this.#journal.pendingDeleteIDs(this.#hub):new Set<string>();let build:((tx:LocalTransaction)=>LocalPlan)|undefined;
      if(!this.#view!.operationValid(action.kind,action.details)){
        action.details.failure='This operation was superseded by a newer policy or roster.';
        build=()=>({messages:[],cancel:phase.messages.map(messageID),actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}]});
      }else{
        const replaced=action.kind==='collection-rotation'&&action.index>=3&&b64u(this.#view!.collectionStatus(unb64u(action.details.collection!)).epoch??new Uint8Array())!==action.details.epoch;
        const required=replaced?[]:(phase.requires??[]).flatMap(index=>action.phases[index]!.messages);
        const absent=required.filter(message=>!this.#confirmed(message));
        if(absent.length){
          const queued=await this.#journal.queued(this.#hub,absent.map(messageID));
          const missing=absent.filter(message=>!queued.has(messageID(message))),currentQueued=await this.#journal.queued(this.#hub,phase.messages.map(messageID));
          if(missing.length||currentQueued.size)build=()=>({messages:missing,cancel:[...currentQueued]});
        }else{
          const retired=phase.messages.filter(message=>this.#view!.deliveryRetired(message));
          if(retired.length){phase.messages=phase.messages.filter(message=>!retired.includes(message));build=()=>({messages:[],cancel:retired.map(messageID),actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}]});}
          else if(phase.messages.every(message=>this.#confirmed(message))){
            if(phase.destroy?.length&&!phase.destroyQueued){phase.destroyQueued=true;build=()=>({messages:[],destroy:phase.destroy!,actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}]});}
            else if(!(phase.destroy??[]).some(id=>deleting.has(id))){
              if(action.index+1===action.phases.length)build=()=>({messages:[],actions:[{id:action.id,sealed:null}]});
              else{const next=action.phases[action.index+1]!;if(!next.intent||this.#view!.intentReady(next.intent)){action.index++;build=await this.#phasePlan(action,next);}}
            }
          }else{
            const allowed=phase.messages.filter(message=>this.#view!.exportable(message)),queued=await this.#journal.queued(this.#hub,allowed.map(messageID));
            const missing=allowed.filter(message=>!this.#confirmed(message)&&!queued.has(messageID(message)));if(missing.length)build=()=>({messages:missing});
          }
        }
      }
      if(build){const installation=await this.#journal.installation();try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{if(tx.sequence!==this.#sequence)throw new StaleDeviceState();return build!(tx);});}catch(error){if(error instanceof StaleDeviceState)return true;throw error;}return true;}
    }
    if(this.#view!.homeNeedsRotation&&this.#view!.canRotateHome&&![...this.#activeActions.values()].some(action=>action.kind==='home-rotation'&&!action.details.failure)){
      const installation=await this.#journal.installation();try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{if(tx.sequence!==this.#sequence)throw new StaleDeviceState();return this.#operationPlan(this.#view!.prepareHomeRotation(),tx);});}catch(error){if(error instanceof StaleDeviceState)return true;throw error;}return true;
    }
    return false;
  }
  get operations():readonly {id:string;kind:string;phase:string;collection?:string;failure?:string}[]{return [...this.#activeActions.values()].map(action=>({id:action.id,kind:action.kind,phase:action.phases[action.index]!.name,...(action.details.collection?{collection:action.details.collection}:{}),...(action.details.failure?{failure:action.details.failure}:{})}));}
  #operationPlan(prepared:PreparedOperation,tx:LocalTransaction):LocalPlan{
    for(const [existingID,sealed]of tx.actions){const existing=openAction(existingID,sealed,this.#actionKey,this.#enrollment.link.hub);if(!existing.details.failure&&(prepared.details.claim&&existing.details.claim===prepared.details.claim||prepared.kind==='admin-password'&&existing.kind===prepared.kind||prepared.details.collection&&existing.details.collection===prepared.details.collection||prepared.kind==='home-rotation'&&existing.kind===prepared.kind))throw new Error('this scope already has a pending operation');}
    const action=newAction(prepared.kind,prepared.phases,prepared.details);
    for(const phase of action.phases)for(const message of phase.messages)if(message.guard?.type==='action'&&!message.guard.id)message.guard.id=action.id;
    const first=action.phases[0]!;return{messages:first.messages,actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}],...(first.cancel?{cancel:first.cancel}:{}),...(first.destroy?{destroy:first.destroy}:{})};
  }
  async #startOperation(prepare:(state:DeviceState,reserve:()=>Dot)=>PreparedOperation):Promise<string>{
    let id='';await this.#commitPlan((state,reserve,tx)=>{const plan=this.#operationPlan(prepare(state,reserve),tx);id=plan.actions![0]!.id;return plan;});return id;
  }
  async startPairing(label:string,validForMs=24*3600*1000,mail?:PairingLink['mail']):Promise<PairingLink>{
    const captured=mail?parsePairingLink(pairingLinkJSON({...this.#enrollment.link,mail})).mail:this.#enrollment.link.mail;
    let link:PairingLink|undefined;await this.#startOperation(state=>{const prepared=state.preparePairing(label,captured,validForMs);link=prepared.link;return prepared.operation;});return link!;
  }
  async invite(label:string,seed:Uint8Array,validForMs=7*24*3600*1000):Promise<string>{let link='';await this.#admin(seed,(state,key)=>{const prepared=state.prepareInvitation(label,this.#enrollment.link.mail,key,validForMs);link=b64u(prepared.link);return prepared.operation;});return link;}
  async cancelPairing(token:Uint8Array):Promise<void>{const captured=bytes(token,16);await this.#commitPlan((state,reserve,tx)=>{const actions:{id:string;sealed:null}[]=[],cancel:string[]=[];let delegation:Uint8Array|undefined;
    for(const [id,sealed]of tx.actions){const action=openAction(id,sealed,this.#actionKey,this.#enrollment.link.hub);if(action.kind==='pairing-start'&&action.details.token===b64u(captured)){delegation=unb64u(action.details.delegation!);actions.push({id,sealed:null});for(const phase of action.phases)cancel.push(...phase.messages.map(messageID));}}
    return{messages:state.prepareCancelPairing(captured,reserve,delegation),actions,cancel};});}
  async unlock(password:string,options:CapsuleOptions={}):Promise<Uint8Array>{await this.#lockedRefresh();let last:unknown;for(const capsule of this.#view?.adminCapsules()??[]){options.signal?.throwIfAborted();try{return await openCapsule(password,this.#enrollment.link.hub,capsule,options);}catch(error){last=error;}}throw last??new Error('no admin capsule on this hub');}
  async #admin(seed:Uint8Array,prepare:(state:DeviceState,seed:Uint8Array)=>PreparedOperation):Promise<string>{const captured=bytes(seed,32);try{return await this.#startOperation(state=>prepare(state,captured));}finally{captured.fill(0);}}
  async approve(claim:Uint8Array,seed:Uint8Array):Promise<string>{const id=bytes(claim,32);return this.#admin(seed,(state,key)=>state.prepareApprove(id,key));}
  async revoke(certificate:Uint8Array,seed:Uint8Array):Promise<string>{const id=bytes(certificate,32);return this.#admin(seed,(state,key)=>state.prepareRevoke(id,key));}
  async reject(claim:Uint8Array,seed:Uint8Array):Promise<string>{const id=bytes(claim,32);return this.#admin(seed,(state,key)=>state.prepareReject(id,key));}
  async cancelInvitation(capability:Uint8Array,seed:Uint8Array):Promise<string>{const id=bytes(capability,32);return this.#admin(seed,(state,key)=>state.prepareCancelInvitation(id,key));}
  async changePassword(password:string,seed:Uint8Array,options:CapsuleOptions={}):Promise<string>{const captured=bytes(seed,32);try{const box=await sealCapsule(password,this.#enrollment.link.hub,captured,options);return await this.#startOperation(state=>state.preparePassword(box,captured));}finally{captured.fill(0);}}
  async rotateHome():Promise<string>{return this.#startOperation(state=>state.prepareHomeRotation());}
  async rekey(collection:Uint8Array):Promise<string>{const cid=bytes(collection,16);return this.#startOperation((state,reserve)=>state.prepareCollectionRotation(cid,reserve));}
  async removeMember(collection:Uint8Array,participant:Uint8Array):Promise<string>{const cid=bytes(collection,16),uid=bytes(participant,32);return this.#startOperation((state,reserve)=>state.prepareCollectionRotation(cid,reserve,{remove:uid}));}
  async setRole(collection:Uint8Array,participant:Uint8Array,role:'writer'|'reader'):Promise<string>{const cid=bytes(collection,16),uid=bytes(participant,32);return this.#startOperation((state,reserve)=>state.prepareSetRole(cid,uid,role,reserve));}
  async deleteList(collection:Uint8Array):Promise<string>{const cid=bytes(collection,16);return this.#startOperation((state,reserve)=>state.prepareDeleteList(cid,reserve));}
  async share(collection:Uint8Array,participant:Uint8Array,role:'writer'|'reader',name=''):Promise<string>{const cid=bytes(collection,16),uid=bytes(participant,32);return this.#startOperation((state,reserve)=>state.prepareShare(cid,uid,role,name,reserve));}
  async writeHome(name:string,edits:readonly FieldEdit[]):Promise<void>{const captured=structuredClone(edits);await this.#edit((state,reserve)=>state.prepareHome(name,captured,reserve));}
  async writeFields(collection:Uint8Array,record:Uint8Array,edits:readonly FieldEdit[]):Promise<void>{const cid=bytes(collection,16),id=bytes(record,16),captured=structuredClone(edits);await this.#edit((state,reserve)=>state.prepareField(cid,id,captured,reserve));}
  async createList(name:string):Promise<Uint8Array>{let collection:Uint8Array|undefined;await this.#edit((state,reserve)=>{const prepared=state.prepareCreateList(name,reserve);collection=prepared.collection;return prepared.messages;});return collection!;}
  /** Create a collection and its private home reference in one durable commit.
   * Concurrent callers reuse the lowest existing reference instead of duplicating it. */
  async ensureList(name:string,home:{record:string;field:string}):Promise<Uint8Array>{let collection:Uint8Array|undefined;await this.#edit((state,reserve)=>{
    const existing=state.homeRecord(home.record)?.elements(home.field).filter(id=>id.length===16).sort(Buffer.compare)[0];if(existing){collection=existing;return[];}
    const prepared=state.prepareCreateList(name,reserve);collection=prepared.collection;return[...prepared.messages,...state.prepareHome(home.record,[{field:home.field,op:2,element:collection}],reserve)];
  });return collection!;}
  async accept(collection:Uint8Array):Promise<void>{const cid=bytes(collection,16);await this.#edit((state,reserve)=>state.prepareAccept(cid,reserve));}
  async close():Promise<void>{if(this.#closed)return;this.#closed=true;this.#abort?.abort();this.#changes.end();await this.#running?.catch(()=>{});}
}
