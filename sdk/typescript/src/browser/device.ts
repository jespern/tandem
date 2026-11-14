import {Buffer} from 'buffer';
import {b64u,unb64u,bytes,sha} from '../crypto.js';
import {parseJSON} from '../json.js';
import {objectID} from '../control.js';
import {DeviceState,DeviceHoldingLimit,type DeviceStateOptions,type FieldEdit} from '../device-state.js';
import {type PairingLink,type PairingProposal,type PairingKeys,parsePairingLink,pairingLinkJSON,pairingRegistry,proposePairing,releasePairing,pendingPairingJSON,parsePendingPairing} from '../pairing.js';
import type {Registry} from '../registry.js';
import type {Message} from '../session.js';
import {BrowserJournal,type LocalPlan,type LocalTransaction} from './journal.js';
import type {Dot} from '../core.js';
import type {PreparedOperation} from '../operation.js';
import {newAction,sealAction,openAction,messageID,type DeviceAction,type ActionPhase} from './actions.js';
import {BrowserKeychain} from './keychain.js';
import {BrowserReplica,type PreparedStore,untilAbort} from './replica.js';
import {Invalidation} from './events.js';

interface Enrollment {link:PairingLink;label:string;proposal?:PairingProposal;complete?:{epoch:Uint8Array;key:Uint8Array;certificate:Uint8Array}}
export type BrowserDeviceStatus='material'|'approval'|'home'|'ready'|'revoked';
export interface BrowserDeviceUpdate {status:BrowserDeviceStatus;revision:number}
export class StaleDeviceState extends Error {constructor(){super('new durable events arrived; replay them before signing');}}
const encode=(value:unknown):Buffer=>Buffer.from(JSON.stringify(value));
const raw=(value:unknown,n?:number)=>{if(typeof value!=='string')throw new Error('invalid enrollment bytes');return bytes(unb64u(value),n);};
function saveEnrollment(record:Enrollment):Uint8Array {
  return encode({v:1,link:parseJSON(pairingLinkJSON(record.link)),label:record.label,
    ...(record.proposal?{proposal:{pending:parseJSON(pendingPairingJSON(record.proposal.pending)),messages:record.proposal.messages.map(m=>({channel:m.channel,body:b64u(m.body)}))}}:{}),
    ...(record.complete?{complete:{epoch:b64u(record.complete.epoch),key:b64u(record.complete.key),certificate:b64u(record.complete.certificate)}}:{})});
}
function loadEnrollment(data:Uint8Array):Enrollment {
  const j=parseJSON(data) as Record<string,any>;if(j.v!==1||typeof j.label!=='string')throw new Error('invalid browser enrollment');
  const record:Enrollment={link:parsePairingLink(encode(j.link)),label:j.label};
  if(j.proposal){const pending=parsePendingPairing(encode(j.proposal.pending));if(!Array.isArray(j.proposal.messages))throw new Error('invalid pending messages');
    record.proposal={pending,messages:j.proposal.messages.map((m:Record<string,unknown>)=>{if(typeof m.channel!=='string')throw new Error('invalid pending channel');const body=raw(m.body);return{id:b64u(sha(body)),channel:m.channel,body};})};}
  if(j.complete)record.complete={epoch:raw(j.complete.epoch,32),key:raw(j.complete.key,32),certificate:raw(j.complete.certificate,32)};
  return record;
}
interface DeviceLocalState {v:2;pairingRequest?:string;processed:string[];unshared:Record<string,string>}
function localState(data:Uint8Array|undefined):DeviceLocalState{
  if(!data)return{v:2,processed:[],unshared:{}};
  const text=Buffer.from(data).toString();if(!text.startsWith('{'))return{v:2,pairingRequest:text,processed:[],unshared:{}};
  const j=parseJSON(data) as Record<string,any>;if(j.v!==2||!Array.isArray(j.processed)||j.processed.some((id:unknown)=>typeof id!=='string')||!j.unshared||typeof j.unshared!=='object'||Object.values(j.unshared).some(value=>typeof value!=='string'))throw new Error('invalid device local state');
  return{v:2,...(typeof j.pairingRequest==='string'?{pairingRequest:j.pairingRequest}:{}),processed:j.processed,unshared:{...j.unshared}};
}
/** Browser device operations over the durable push pump. Enrollment secrets are
 * wrapped in the separate keychain; exact claims and edits commit before export.
 * This class owns the journal's opaque per-hub action state. */
export class BrowserDevice {
  readonly #journal:BrowserJournal;readonly #keychain:BrowserKeychain;readonly #keys:PairingKeys;readonly #options:DeviceStateOptions;
  readonly #actionKey:Uint8Array;readonly #activeActions=new Map<string,DeviceAction>();readonly #present=new Map<string,Set<string>>();readonly #remote=new Map<string,string>();
  readonly #hub:string;readonly #slot:string;readonly #lock:string;readonly #replica:BrowserReplica;readonly #changes=new Invalidation();
  readonly #messages=new Map<string,Message>();#local:DeviceLocalState={v:2,processed:[],unshared:{}};#enrollment:Enrollment;#registry:Registry|undefined;#view:DeviceState|undefined;
  #sequence=0;#messageBytes=0;#lastLate='';#closed=false;#running:Promise<void>|undefined;#abort:AbortController|undefined;#status:BrowserDeviceStatus='material';
  private constructor(journal:BrowserJournal,keychain:BrowserKeychain,keys:PairingKeys,enrollment:Enrollment,options:DeviceStateOptions,actionKey:Uint8Array){
    this.#actionKey=bytes(actionKey,32);
    this.#journal=journal;this.#keychain=keychain;this.#keys=keys;this.#enrollment=enrollment;this.#options=options;this.#hub=b64u(enrollment.link.hub);this.#slot=`device:${this.#hub}`;
    this.#lock=`tandem:device:${JSON.stringify([journal.name,this.#hub])}`;this.#replica=new BrowserReplica(journal,this.#hub);
  }
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
  get state():DeviceState|undefined{return this.#view;}
  get update():BrowserDeviceUpdate{return{status:this.#status,revision:this.#view?.revision??0};}
  async *updates(signal:AbortSignal):AsyncGenerator<BrowserDeviceUpdate>{
    while(!signal.aborted&&!this.#closed){const changed=this.#changes.snapshot();yield this.update;await untilAbort(changed,signal);}
  }
  async #lockedRefresh():Promise<void>{await navigator.locks.request(this.#lock,()=>this.#refresh());}
  async #refresh():Promise<void>{
    if(this.#closed)throw new Error('browser device closed');
    const saved=await this.#keychain.get(this.#slot);if(saved)this.#enrollment=loadEnrollment(saved);
    let rows;do{rows=await this.#journal.readEvents(this.#hub,this.#sequence);const messages=rows.filter(row=>row.kind!=='removed'&&!(row.kind==='queued'&&row.deferred));
      for(const row of rows){if(!row.remoteId)continue;const key=JSON.stringify([row.channel,row.id]);
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
      const missing=record.proposal.messages.filter(message=>!this.#present.has(JSON.stringify([message.channel,messageID(message)]))&&!queued.has(messageID(message)));
      try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{
        if(tx.sequence!==this.#sequence)throw new StaleDeviceState();
        const local=localState(tx.state);if(local.pairingRequest===request)return{messages:missing};local.pairingRequest=request;
        return{messages:record.proposal!.messages,state:encode(local)};
      });}catch(error){if(error instanceof StaleDeviceState)return this.#refresh();throw error;}
      const released=this.#registry&&releasePairing(record.proposal.pending,this.#registry,[...this.#messages.values()],this.#keys);
      if(released){record.complete={epoch:released.epoch,key:released.key,certificate:released.certificate.id};await this.#keychain.save(this.#slot,saveEnrollment(record));}
    }
    if(record.complete&&this.#view){this.#view.offerHomeKey(record.complete.epoch,record.complete.key,true);this.#messages.clear();this.#messageBytes=0;}
    if(this.#view&&await this.#progressActions())return this.#refresh();
    if(this.#view&&await this.#settleLateWrites())return this.#refresh();
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
    const transport=this.#replica.run(connect,merged,{beforeExport:()=>this.#lockedRefresh(),allowExport:entry=>{if(entry.guard?.type==='action'){const action=this.#activeActions.get(entry.guard.id);return !!action&&!action.details.failure&&!!this.#view?.operationValid(action.kind,action.details);}return this.#view?.exportable(entry)??false;}});void transport.then(()=>{if(!merged.aborted)abort.abort(new Error('replica ended'));},error=>abort.abort(error));
    this.#running=(async()=>{try{while(!merged.aborted){const changed=this.#journal.eventsChanged();await this.#lockedRefresh();await untilAbort(changed,merged);}}
      catch(error){if(!signal.aborted&&!this.#closed)throw error;}finally{abort.abort();await transport.catch(()=>{});this.#running=undefined;this.#abort=undefined;}})();
    return this.#running;
  }
  async #commitPlan(prepare:(state:DeviceState,reserve:()=>Dot,tx:LocalTransaction)=>LocalPlan):Promise<void>{
    if(this.#closed)throw new Error('browser device closed');
    await navigator.locks.request(this.#lock,async()=>{while(true){await this.#refresh();const state=this.#view;if(!state)throw new Error('browser device has no verified state yet');
      const installation=await this.#journal.installation();
      try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{if(tx.sequence!==this.#sequence)throw new StaleDeviceState();return prepare(state,()=>tx.nextDot(),tx);});break;}
      catch(error){if(!(error instanceof StaleDeviceState))throw error;}
    }await this.#refresh();});
  }
  async #edit(prepare:(state:DeviceState,reserve:()=>Dot)=>import('../device-state.js').OutgoingMessage[]):Promise<void>{await this.#commitPlan((state,reserve)=>({messages:prepare(state,reserve)}));}
  async #settleLateWrites():Promise<boolean>{
    if(!this.#view?.certificate)return false;const installation=await this.#journal.installation(),stamp=`${b64u(installation)}:${this.#view.revision}`;if(stamp===this.#lastLate)return false;const pending=await this.#journal.pending(this.#hub);let changed=false;
    try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{
      if(tx.sequence!==this.#sequence)throw new StaleDeviceState();const local=localState(tx.state),done=new Set(local.processed),prepared=this.#view!.prepareLateWrites(installation,tx.clock,done,pending);
      if(!prepared.messages.length&&!prepared.processed.length&&!prepared.cancel.length)return{messages:[]};
      for(const id of prepared.processed)done.add(id);local.processed=[...done];for(const held of prepared.unshared)local.unshared[`${held.scope}:${held.write}`]=held.record;
      changed=true;return{messages:prepared.messages,cancel:prepared.cancel,state:encode(local)};
    });}catch(error){if(error instanceof StaleDeviceState)return true;throw error;}this.#lastLate=stamp;return changed;
  }
  get unshared():readonly {scope:string;record:string;write:string}[]{return Object.entries(this.#local.unshared).map(([id,record])=>{const [scope,write]=id.split(':');return{scope:scope!,write:write!,record};});}
  async dismissUnshared(scope:Uint8Array,write:Uint8Array):Promise<void>{const key=`${b64u(scope)}:${b64u(write)}`;await this.#commitPlan((_state,_reserve,tx)=>{const local=localState(tx.state);delete local.unshared[key];return{messages:[],state:encode(local)};});}
  async #phasePlan(action:DeviceAction,phase:ActionPhase):Promise<(tx:LocalTransaction)=>LocalPlan>{
    const context=phase.intent?.type==='collection-cleanup'?{
      deliveries:(await this.#journal.deliveries(this.#hub)).filter(row=>row.present).map(row=>({id:row.remoteId,channel:row.channel,body:row.body})),
      outbox:await this.#journal.pending(this.#hub)
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
      if(action.details.failure)continue;
      const phase=action.phases[action.index]!;let build:((tx:LocalTransaction)=>LocalPlan)|undefined;
      if(!this.#view!.operationValid(action.kind,action.details)){
        action.details.failure='This operation was superseded by a newer policy or roster.';
        build=()=>({messages:[],cancel:phase.messages.map(messageID),actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}]});
      }else{
        const replaced=action.kind==='collection-rotation'&&action.index>=3&&b64u(this.#view!.collectionStatus(unb64u(action.details.collection!)).epoch??new Uint8Array())!==action.details.epoch;
        const required=replaced?[]:(phase.requires??[]).flatMap(index=>action.phases[index]!.messages);
        const absent=required.filter(message=>!this.#present.has(JSON.stringify([message.channel,messageID(message)])));
        if(absent.length){
          const queued=await this.#journal.queued(this.#hub,absent.map(messageID));
          const missing=absent.filter(message=>!queued.has(messageID(message))),currentQueued=await this.#journal.queued(this.#hub,phase.messages.map(messageID));
          if(missing.length||currentQueued.size)build=()=>({messages:missing,cancel:[...currentQueued]});
        }else{
          const retired=phase.messages.filter(message=>this.#view!.deliveryRetired(message));
          if(retired.length){phase.messages=phase.messages.filter(message=>!retired.includes(message));build=()=>({messages:[],cancel:retired.map(messageID),actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}]});}
          else if(phase.messages.every(message=>this.#present.has(JSON.stringify([message.channel,messageID(message)])))){
            if(phase.destroy?.length&&!phase.destroyQueued){phase.destroyQueued=true;build=()=>({messages:[],destroy:phase.destroy!,actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}]});}
            else if(!(await this.#journal.pendingDeletes(this.#hub)).some(row=>phase.destroy?.includes(row.id))){
              if(action.index+1===action.phases.length)build=()=>({messages:[],actions:[{id:action.id,sealed:null}]});
              else{const next=action.phases[action.index+1]!;if(!next.intent||this.#view!.intentReady(next.intent)){action.index++;build=await this.#phasePlan(action,next);}}
            }
          }else{
            const allowed=phase.messages.filter(message=>this.#view!.exportable(message)),queued=await this.#journal.queued(this.#hub,allowed.map(messageID));
            const missing=allowed.filter(message=>!this.#present.has(JSON.stringify([message.channel,messageID(message)]))&&!queued.has(messageID(message)));if(missing.length)build=()=>({messages:missing});
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
    for(const [existingID,sealed]of tx.actions){const existing=openAction(existingID,sealed,this.#actionKey,this.#enrollment.link.hub);if(!existing.details.failure&&(prepared.details.collection&&existing.details.collection===prepared.details.collection||prepared.kind==='home-rotation'&&existing.kind===prepared.kind))throw new Error('this scope already has a pending operation');}
    const action=newAction(prepared.kind,prepared.phases,prepared.details);
    for(const phase of action.phases)for(const message of phase.messages)if(message.guard?.type==='action'&&!message.guard.id)message.guard.id=action.id;
    const first=action.phases[0]!;return{messages:first.messages,actions:[{id:action.id,sealed:sealAction(action,this.#actionKey,this.#enrollment.link.hub)}],...(first.cancel?{cancel:first.cancel}:{}),...(first.destroy?{destroy:first.destroy}:{})};
  }
  async #startOperation(prepare:(state:DeviceState,reserve:()=>Dot)=>PreparedOperation):Promise<string>{
    let id='';await this.#commitPlan((state,reserve,tx)=>{const plan=this.#operationPlan(prepare(state,reserve),tx);id=plan.actions![0]!.id;return plan;});return id;
  }
  async rotateHome():Promise<string>{return this.#startOperation(state=>state.prepareHomeRotation());}
  async rekey(collection:Uint8Array):Promise<string>{const cid=bytes(collection,16);return this.#startOperation((state,reserve)=>state.prepareCollectionRotation(cid,reserve));}
  async removeMember(collection:Uint8Array,participant:Uint8Array):Promise<string>{const cid=bytes(collection,16),uid=bytes(participant,32);return this.#startOperation((state,reserve)=>state.prepareCollectionRotation(cid,reserve,{remove:uid}));}
  async setRole(collection:Uint8Array,participant:Uint8Array,role:'writer'|'reader'):Promise<string>{const cid=bytes(collection,16),uid=bytes(participant,32);return this.#startOperation((state,reserve)=>state.prepareSetRole(cid,uid,role,reserve));}
  async deleteList(collection:Uint8Array):Promise<string>{const cid=bytes(collection,16);return this.#startOperation((state,reserve)=>state.prepareDeleteList(cid,reserve));}
  async share(collection:Uint8Array,participant:Uint8Array,role:'writer'|'reader',name=''):Promise<string>{const cid=bytes(collection,16),uid=bytes(participant,32);return this.#startOperation((state,reserve)=>state.prepareShare(cid,uid,role,name,reserve));}
  async writeHome(name:string,edits:readonly FieldEdit[]):Promise<void>{const captured=structuredClone(edits);await this.#edit((state,reserve)=>state.prepareHome(name,captured,reserve));}
  async writeFields(collection:Uint8Array,record:Uint8Array,edits:readonly FieldEdit[]):Promise<void>{const cid=bytes(collection,16),id=bytes(record,16),captured=structuredClone(edits);await this.#edit((state,reserve)=>state.prepareField(cid,id,captured,reserve));}
  async createList(name:string):Promise<Uint8Array>{let collection:Uint8Array|undefined;await this.#edit((state,reserve)=>{const prepared=state.prepareCreateList(name,reserve);collection=prepared.collection;return prepared.messages;});return collection!;}
  async accept(collection:Uint8Array):Promise<void>{const cid=bytes(collection,16);await this.#edit((state,reserve)=>state.prepareAccept(cid,reserve));}
  async close():Promise<void>{if(this.#closed)return;this.#closed=true;this.#abort?.abort();this.#changes.end();await this.#running?.catch(()=>{});}
}
