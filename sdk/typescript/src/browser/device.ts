import {Buffer} from 'buffer';
import {b64u,unb64u,bytes,sha} from '../crypto.js';
import {parseJSON} from '../json.js';
import {objectID} from '../control.js';
import {DeviceState,DeviceHoldingLimit,type DeviceStateOptions,type FieldEdit} from '../device-state.js';
import {type PairingLink,type PairingProposal,type PairingKeys,parsePairingLink,pairingLinkJSON,pairingRegistry,proposePairing,releasePairing,pendingPairingJSON,parsePendingPairing} from '../pairing.js';
import type {Registry} from '../registry.js';
import type {Message} from '../session.js';
import {BrowserJournal} from './journal.js';
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
/** Browser device operations over the durable push pump. Enrollment secrets are
 * wrapped in the separate keychain; exact claims and edits commit before export.
 * This class owns the journal's opaque per-hub action state. */
export class BrowserDevice {
  readonly #journal:BrowserJournal;readonly #keychain:BrowserKeychain;readonly #keys:PairingKeys;readonly #options:DeviceStateOptions;
  readonly #hub:string;readonly #slot:string;readonly #lock:string;readonly #replica:BrowserReplica;readonly #changes=new Invalidation();
  readonly #messages=new Map<string,Message>();#enrollment:Enrollment;#registry:Registry|undefined;#view:DeviceState|undefined;
  #sequence=0;#messageBytes=0;#closed=false;#running:Promise<void>|undefined;#abort:AbortController|undefined;#status:BrowserDeviceStatus='material';
  private constructor(journal:BrowserJournal,keychain:BrowserKeychain,keys:PairingKeys,enrollment:Enrollment,options:DeviceStateOptions){
    this.#journal=journal;this.#keychain=keychain;this.#keys=keys;this.#enrollment=enrollment;this.#options=options;this.#hub=b64u(enrollment.link.hub);this.#slot=`device:${this.#hub}`;
    this.#lock=`tandem:device:${JSON.stringify([journal.name,this.#hub])}`;this.#replica=new BrowserReplica(journal,this.#hub);
  }
  static async pair(input:PairingLink|string|Uint8Array,label:string,journal:BrowserJournal,keychain:BrowserKeychain,options:DeviceStateOptions={}):Promise<BrowserDevice>{
    const link=typeof input==='string'||input instanceof Uint8Array?parsePairingLink(input):parsePairingLink(pairingLinkJSON(input));
    const keys=await keychain.deviceKeys(),hub=b64u(link.hub),slot=`device:${hub}`,lock=`tandem:device:${JSON.stringify([journal.name,hub])}`;
    const enrollment=await navigator.locks.request(lock,async()=>{const saved=await keychain.get(slot);if(saved){const record=loadEnrollment(saved);if(b64u(record.link.participant)!==b64u(link.participant)||b64u(record.link.genesis)!==b64u(link.genesis))throw new Error('this browser already has another enrollment on this hub');return record;}
      const record={link,label};await keychain.save(slot,saveEnrollment(record));return record;});
    const device=new BrowserDevice(journal,keychain,keys,enrollment,options);await device.#lockedRefresh();return device;
  }
  static async resume(hub:Uint8Array,journal:BrowserJournal,keychain:BrowserKeychain,options:DeviceStateOptions={}):Promise<BrowserDevice>{
    const saved=await keychain.get(`device:${b64u(bytes(hub,32))}`);if(!saved)throw new Error('pair this browser first');
    const device=new BrowserDevice(journal,keychain,await keychain.deviceKeys(),loadEnrollment(saved),options);await device.#lockedRefresh();return device;
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
    let rows;do{rows=await this.#journal.readEvents(this.#hub,this.#sequence);const messages=rows.filter(row=>row.kind!=='removed');
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
      try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{
        if(tx.sequence!==this.#sequence)throw new StaleDeviceState();
        if(tx.state&&Buffer.from(tx.state).toString()===request)return{messages:[]};
        return{messages:record.proposal!.messages,state:Buffer.from(request)};
      });}catch(error){if(error instanceof StaleDeviceState)return this.#refresh();throw error;}
      const released=this.#registry&&releasePairing(record.proposal.pending,this.#registry,[...this.#messages.values()],this.#keys);
      if(released){record.complete={epoch:released.epoch,key:released.key,certificate:released.certificate.id};await this.#keychain.save(this.#slot,saveEnrollment(record));}
    }
    if(record.complete&&this.#view){this.#view.offerHomeKey(record.complete.epoch,record.complete.key);this.#messages.clear();this.#messageBytes=0;}
    const activity=record.complete&&this.#registry?.activity(record.complete.certificate);
    this.#status=record.complete?(activity&&activity.kind==='invalid'&&activity.reason==='revoked'?'revoked':this.#view?.certificate?(this.#view.homeReady?'ready':'home'):'material'):record.proposal?'approval':'material';
    this.#changes.signal();
  }
  /** The caller supplies a JMAP connection factory. One tab owns the connection;
   * all tabs replay journal events and may commit local edits. */
  run(connect:()=>Promise<PreparedStore>,signal:AbortSignal):Promise<void>{
    if(this.#running||this.#closed)throw new Error('browser device already running or closed');
    const abort=new AbortController();this.#abort=abort;const merged=AbortSignal.any([signal,abort.signal]);
    const transport=this.#replica.run(connect,merged);void transport.then(()=>{if(!merged.aborted)abort.abort(new Error('replica ended'));},error=>abort.abort(error));
    this.#running=(async()=>{try{while(!merged.aborted){const changed=this.#journal.eventsChanged();await this.#lockedRefresh();await untilAbort(changed,merged);}}
      catch(error){if(!signal.aborted&&!this.#closed)throw error;}finally{abort.abort();await transport.catch(()=>{});}})();
    return this.#running;
  }
  async #edit(prepare:(state:DeviceState,reserve:()=>import('../core.js').Dot)=>Message[]|import('../device-state.js').OutgoingMessage[]):Promise<void>{
    if(this.#closed)throw new Error('browser device closed');
    await navigator.locks.request(this.#lock,async()=>{while(true){await this.#refresh();const state=this.#view;if(!state)throw new Error('browser device has no verified state yet');
      const installation=await this.#journal.installation();
      try{await this.#journal.commit(this.#hub,installation,Date.now(),tx=>{if(tx.sequence!==this.#sequence)throw new StaleDeviceState();return{messages:prepare(state,()=>tx.nextDot())};});break;}
      catch(error){if(!(error instanceof StaleDeviceState))throw error;}
    }await this.#refresh();});
  }
  async writeHome(name:string,edits:readonly FieldEdit[]):Promise<void>{const captured=structuredClone(edits);await this.#edit((state,reserve)=>state.prepareHome(name,captured,reserve));}
  async writeFields(collection:Uint8Array,record:Uint8Array,edits:readonly FieldEdit[]):Promise<void>{const cid=bytes(collection,16),id=bytes(record,16),captured=structuredClone(edits);await this.#edit((state,reserve)=>state.prepareField(cid,id,captured,reserve));}
  async close():Promise<void>{if(this.#closed)return;this.#closed=true;this.#abort?.abort();this.#changes.end();await this.#running?.catch(()=>{});}
}
