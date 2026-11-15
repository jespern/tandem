import {Buffer} from 'buffer';
import {b64u,bytes,randomBytes,managerKeys,publicKey,commit,sha,seal,sealInbox,directoryKey,directoryTag,homeTag,inboxTag} from './crypto.js';
import {type Mailbox,type ControlObject,type Fields,type Signatures,type Extras,controlFromJSON,controlToJSON,objectID,signControl,registrationID,sealHomeRecovery,How} from './control.js';
import {pinGenesis,type HubPin} from './registry.js';
import {inventoryBytes} from './core.js';
import type {PairingKeys} from './pairing.js';
import type {OutgoingMessage} from './operation.js';

export interface FoundingSecrets {managementSeed:Uint8Array;directoryKey:Uint8Array;enrollment:Uint8Array;homeKey:Uint8Array}
export interface Founding {
  pin:HubPin;participant:Uint8Array;genesis:ControlObject;certificate:ControlObject;snapshot:ControlObject;homeEpoch:ControlObject;
  directoryKey:Uint8Array;homeKey:Uint8Array;managementSeed:Uint8Array;
  messages:OutgoingMessage[];
}
const object=(kind:string,fields:Fields,signatures:Signatures={},extras:Extras={}):ControlObject=>controlFromJSON(controlToJSON(kind,fields,signatures,extras));
const json=(o:ControlObject)=>controlToJSON(o.kind,o.fields,o.signatures,o.extras);
const notice=(value:unknown)=>Buffer.from(JSON.stringify(value));

/** Native-compatible authority and home bootstrap. The mailbox descriptor carries
 * no credential and may describe either an HTTPS/JMAP or an IMAP endpoint.
 * The caller must seal the management seed in a capsule, then erase its copy. */
export function prepareFounding(mailbox:Mailbox,keys:PairingKeys,label:string,secrets?:FoundingSecrets):Founding {
  const s=secrets??{managementSeed:randomBytes(32),directoryKey:randomBytes(32),enrollment:randomBytes(16),homeKey:randomBytes(32)};
  const managementSeed=bytes(s.managementSeed,32),directory=bytes(s.directoryKey,32),homeKey=bytes(s.homeKey,32),enrollment=bytes(s.enrollment,16);
  const manager=managerKeys(managementSeed),hub=publicKey(manager.signing),sign=publicKey(keys.signing),agree=publicKey(keys.agreement);
  const participant=registrationID(hub,sign,agree,enrollment);
  const gf={hub,agree:publicKey(manager.agreement),hubkey:commit(directory),creator:participant,mailboxes:[mailbox]};
  const genesis=object('genesis',gf,{sig:signControl('genesis',gf,manager.signing)}),pin=pinGenesis(genesis);
  const rf={hub,genesis:pin.genesis,participant,sign,agree,label,how:How.first,authority:null,enrollment};
  const request=object('request',rf,{self:signControl('request',rf,keys.signing)});
  const cf={hub,genesis:pin.genesis,request:objectID('request',rf),issued:[]};
  const certificate=object('certificate',cf,{sig:signControl('certificate',cf,manager.signing)},{body:{fields:request.fields,signatures:request.signatures}}),certID=objectID(certificate.kind,certificate.fields);
  const snapshot=object('snapshot',{hub,genesis:pin.genesis,participant,certs:[certID],revocations:[]}),auth=objectID(snapshot.kind,snapshot.fields);
  const hf={hub,genesis:pin.genesis,participant,n:0,parents:[],key:commit(homeKey),recipients:[certID],snapshot:sha(inventoryBytes(new Map())),signer:certID,auth};
  const homeEpoch=object('home-epoch',hf,{sig:signControl('home-epoch',hf,keys.signing)}),epoch=objectID(homeEpoch.kind,homeEpoch.fields);
  const tag=directoryTag(directory,hub),key=directoryKey(directory,hub),home=homeTag(homeKey,hub,participant,epoch);
  const recovery=sealHomeRecovery(pin.agree,hub,epoch,homeKey);
  const messages:OutgoingMessage[]=[genesis,certificate,snapshot].map(o=>({channel:tag,body:seal(key,hub,tag,json(o),3),guard:{type:'control'}}));
  messages.push({channel:tag,body:seal(key,hub,tag,notice({type:'home-recovery',epoch:b64u(epoch),bytes:b64u(recovery)}),3),guard:{type:'control'}},
    {channel:home,body:seal(homeKey,hub,home,json(homeEpoch),3),guard:{type:'home',epoch:b64u(epoch)}},
    {channel:inboxTag(participant,hub),body:sealInbox(hub,participant,agree,notice({type:'home-key',epoch:b64u(epoch),k:b64u(homeKey)})),guard:{type:'recipient',purpose:'home-key',certificate:b64u(certID),epoch:b64u(epoch)}});
  return{pin,participant,genesis,certificate,snapshot,homeEpoch,directoryKey:directory,homeKey,managementSeed,messages};
}
/** Bind an already sealed capsule to the exact root and genesis. */
export function foundingCapsule(founding:Founding,sealed:Uint8Array):OutgoingMessage {
  const fields={hub:founding.pin.hub,genesis:founding.pin.genesis,sealed:sha(bytes(sealed,76)),supersedes:[]},manager=managerKeys(founding.managementSeed);
  const capsule=object('capsule',fields,{sig:signControl('capsule',fields,manager.signing)},{capsule:sealed});
  const channel=directoryTag(founding.directoryKey,founding.pin.hub);
  return{channel,body:seal(directoryKey(founding.directoryKey,founding.pin.hub),founding.pin.hub,channel,json(capsule),3),guard:{type:'control'}};
}
