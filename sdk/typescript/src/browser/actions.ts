import {Buffer} from 'buffer';
import {b64u,unb64u,sha,bytes,randomBytes,encrypt,decrypt} from '../crypto.js';
import {parseJSON} from '../json.js';
import type {LocalMessage} from './journal.js';
export interface ActionPhase {name:string;messages:LocalMessage[];destroy?:string[];cancel?:string[]}
export interface DeviceAction {v:1;id:string;kind:string;index:number;details:Record<string,string>;phases:ActionPhase[]}
export const messageID=(message:{body:Uint8Array}):string=>b64u(sha(message.body));
export function newAction(kind:string,phases:ActionPhase[],details:Record<string,string>={}):DeviceAction{
  if(!kind||!phases.length)throw new Error('action needs a kind and phases');return{v:1,id:b64u(randomBytes(16)),kind,index:0,details:{...details},phases};
}
const aad=(hub:Uint8Array,id:string)=>Buffer.concat([Buffer.from('TANDEM-LOCAL-ACTION-V1'),bytes(hub,32),Buffer.from(id)]);
/** Action secrets never enter the mail store. Their local journal box uses an
 * independent key kept in BrowserKeychain; clock reservation stays synchronous. */
export function sealAction(action:DeviceAction,key:Uint8Array,hub:Uint8Array):Uint8Array{
  const plain=Buffer.from(JSON.stringify({...action,phases:action.phases.map(phase=>({...phase,messages:phase.messages.map(message=>({...message,body:b64u(message.body)}))}))}));
  const nonce=randomBytes(12);return Buffer.concat([nonce,encrypt(bytes(key,32),nonce,aad(hub,action.id),plain)]);
}
export function openAction(id:string,sealed:Uint8Array,key:Uint8Array,hub:Uint8Array,maxBytes=64*1024*1024):DeviceAction{
  if(sealed.length>maxBytes||sealed.length<28)throw new Error('local action exceeds limit or is truncated');
  const j=parseJSON(decrypt(bytes(key,32),sealed.subarray(0,12),aad(hub,id),sealed.subarray(12))) as Record<string,any>;
  if(j.v!==1||j.id!==id||typeof j.kind!=='string'||!Number.isSafeInteger(j.index)||j.index<0||!Array.isArray(j.phases)||j.index>=j.phases.length||!j.details||typeof j.details!=='object'||Array.isArray(j.details)||Object.values(j.details).some(value=>typeof value!=='string'))throw new Error('invalid local action');
  const phases:ActionPhase[]=j.phases.map((phase:Record<string,any>)=>{
    if(typeof phase.name!=='string'||!Array.isArray(phase.messages))throw new Error('invalid action phase');
    for(const field of ['destroy','cancel'])if(phase[field]!==undefined&&(!Array.isArray(phase[field])||phase[field].some((id:unknown)=>typeof id!=='string')))throw new Error('invalid action removals');
    return{name:phase.name,...(phase.destroy?{destroy:phase.destroy}:{}),...(phase.cancel?{cancel:phase.cancel}:{}),messages:phase.messages.map((message:Record<string,any>)=>{if(typeof message.channel!=='string'||typeof message.body!=='string')throw new Error('invalid action message');return{channel:message.channel,body:unb64u(message.body),...(message.guard?{guard:{...message.guard}}:{}),...(message.deferred?{deferred:true}:{})};})} as ActionPhase;
  });return{v:1,id,kind:j.kind,index:j.index,details:{...j.details},phases};
}
