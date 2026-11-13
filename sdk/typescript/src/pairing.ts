import {Buffer} from 'buffer';
import {bytes,b64u,unb64u,sha,commit,publicKey,openEnvelope,openInbox,sealInbox,directoryKey,directoryTag,inboxTag,type PrivateKey} from './crypto.js';
import {parseJSON,type JSONValue} from './json.js';
import {type ControlObject,controlFromJSON,controlToJSON,parseControl,objectID,verifyControl,signControl,pairingInviteKey,pairingKey,pairingTag,openPackage,How} from './control.js';
import {Registry,RegistryHoldingLimit,pinGenesis,type Certificate} from './registry.js';
import type {Message} from './session.js';

export interface PairingLink {
  hub:Uint8Array;genesis:Uint8Array;directoryKey:Uint8Array;secret:Uint8Array;token:Uint8Array;participant:Uint8Array;
  /** Native connection metadata only. A browser must explicitly configure JMAP. */
  mail:{host:string;port:number;user:string;pass:string};
}
export interface PairingKeys {signing:PrivateKey;agreement:PrivateKey}
export interface PendingPairing {link:PairingLink;request:ControlObject;delegation:ControlObject;package:Uint8Array}
export interface PairingProposal {pending:PendingPairing;messages:Message[]}
export interface PairingRelease {certificate:Certificate;epoch:Uint8Array;key:Uint8Array}
const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
const raw=(value:unknown,n?:number):Buffer=>{if(typeof value!=='string')throw new Error('pairing byte string required');return bytes(unb64u(value),n);};
const object=(value:unknown):Record<string,JSONValue>=>{if(!value||typeof value!=='object'||Array.isArray(value))throw new Error('pairing object required');return value as Record<string,JSONValue>;};
const json=(value:unknown):Buffer=>Buffer.from(JSON.stringify(value),'utf8');
const controlJSON=(o:ControlObject):JSONValue=>parseJSON(controlToJSON(o.kind,o.fields,o.signatures,o.extras));

/** Accept raw JSON, base64url JSON or a native liszto://hub# link. Never fetch the
 * URL or forward its IMAP credentials to any browser endpoint. */
export function parsePairingLink(input:Uint8Array|string):PairingLink {
  let encoded:Uint8Array;
  if(typeof input==='string'){
    const value=input.trim();
    if(value.startsWith('liszto://')){const url=new URL(value);if(url.hostname!=='hub'||url.pathname||url.search||url.username||url.password)throw new Error('invalid Liszto pairing URL');encoded=unb64u(url.hash.slice(1));}
    else encoded=value.startsWith('{')?Buffer.from(value):unb64u(value);
  }else encoded=input;
  const j=object(parseJSON(encoded)),mail=object(j.mail);
  if(j.v!==2||typeof mail.host!=='string'||!mail.host||typeof mail.port!=='number'||!Number.isInteger(mail.port)||mail.port<1||mail.port>65535||typeof mail.user!=='string'||typeof mail.pass!=='string')throw new Error('invalid v2 pairing link');
  return{hub:raw(j.hub,32),genesis:raw(j.genesis,32),directoryKey:raw(j.hk,32),secret:raw(j.l,32),token:raw(j.t,16),participant:raw(j.participant,32),
    mail:{host:mail.host,port:mail.port,user:mail.user,pass:mail.pass}};
}
export function pairingLinkJSON(link:PairingLink):Buffer {
  const data=json({v:2,hub:b64u(link.hub),genesis:b64u(link.genesis),hk:b64u(link.directoryKey),mail:link.mail,l:b64u(link.secret),t:b64u(link.token),participant:b64u(link.participant)});
  parsePairingLink(data);return data;
}

/** Bootstrap only the genesis named by the link and signed by the pinned hub.
 * Directory objects are offered independently: a forged variant cannot hide a
 * legitimate object delivered earlier or later. Missing facts remain held. */
export function pairingRegistry(link:PairingLink,messages:readonly Message[]):Registry|undefined {
  const tag=directoryTag(link.directoryKey,link.hub),key=directoryKey(link.directoryKey,link.hub),objects:ControlObject[]=[];
  for(const message of messages){if(message.channel!==tag)continue;
    try{const opened=openEnvelope(key,link.hub,tag,message.body);if(opened.kind===3)objects.push(controlFromJSON(opened.plaintext));}catch{}
  }
  const genesis=objects.find(o=>o.kind==='genesis'&&eq(objectID(o.kind,o.fields),link.genesis)&&eq(o.fields.hub as Uint8Array,link.hub)&&eq(o.fields.hubkey as Uint8Array,commit(link.directoryKey))&&!!o.signatures.sig&&verifyControl(o.kind,o.fields,link.hub,o.signatures.sig));
  if(!genesis)return;const registry=new Registry(pinGenesis(genesis));
  for(const o of objects){if(!['certificate','revocation','invitation','delegation','snapshot'].includes(o.kind))continue;try{registry.add(o);}catch(error){if(error instanceof RegistryHoldingLimit)throw error;}}
  return registry;
}

/** Prepare exact claim envelopes; persist the proposal before sending any of
 * them. Undefined means staged bytes/proofs/admins have not all arrived yet. */
export function proposePairing(link:PairingLink,registry:Registry,messages:readonly Message[],keys:PairingKeys,label:string,now=Date.now()):PairingProposal|undefined {
  const pin=registry.pin;if(!eq(pin.hub,link.hub)||!eq(pin.genesis,link.genesis))throw new Error('pairing registry belongs to another hub');
  if(!Number.isSafeInteger(now)||now<0)throw new Error('invalid pairing time');
  const invite=pairingInviteKey(link.secret,link.hub,link.token),key=pairingKey(link.secret,link.hub,link.token),tag=pairingTag(link.secret,link.hub,link.token);
  const delegations:ControlObject[]=[],packages=new Map<string,Uint8Array>(),gates=new Map<string,Uint8Array>();
  for(const message of messages){if(message.channel!==tag)continue;
    try{const opened=openEnvelope(key,link.hub,tag,message.body);if(opened.kind!==3)continue;const j=object(parseJSON(opened.plaintext));
      if(j.type==='delegation')delegations.push(parseControl(j,'delegation'));
      else if(j.type==='package'||j.type==='gate'){const value=raw(j.bytes);(j.type==='package'?packages:gates).set(b64u(sha(value)),value);}
    }catch{}
  }
  for(const delegation of delegations){const f=delegation.fields,source=registry.certificate(f.source as Uint8Array);
    if(!eq(f.hub as Uint8Array,link.hub)||!eq(f.genesis as Uint8Array,link.genesis)||!eq(f.participant as Uint8Array,link.participant)||!eq(f.token as Uint8Array,link.token)||!eq(f.key as Uint8Array,publicKey(invite))||now>=(f.expires as number)||!source||!eq(source.participant,link.participant)||registry.activity(source.id).kind!=='valid'||!delegation.signatures.sig||!verifyControl(delegation.kind,f,source.sign,delegation.signatures.sig))continue;
    const packageBytes=packages.get(b64u(f.package as Uint8Array)),gate=gates.get(b64u(f.gate as Uint8Array));if(!packageBytes||!gate)continue;
    registry.add(delegation);
    const fields={hub:link.hub,genesis:link.genesis,participant:link.participant,sign:publicKey(keys.signing),agree:publicKey(keys.agreement),label,how:How.byPairing,authority:objectID(delegation.kind,f),enrollment:new Uint8Array(16)};
    const request=controlFromJSON(controlToJSON('request',fields,{self:signControl('request',fields,keys.signing),approver:signControl('request',fields,invite)}));
    const admins=registry.allCertificates.filter(c=>eq(c.participant,pin.creator)&&registry.activity(c.id).kind==='valid');if(!admins.length)return;
    const body=json({type:'pair-claim',request:controlJSON(request),delegation:controlJSON(delegation),gate:b64u(gate)}),channel=inboxTag(pin.creator,link.hub);
    return{pending:{link:parsePairingLink(pairingLinkJSON(link)),request,delegation,package:bytes(packageBytes)},messages:admins.map(admin=>{
      const bodyBytes=sealInbox(link.hub,pin.creator,admin.agree,body);return{id:b64u(sha(bodyBytes)),channel,body:bodyBytes};
    })};
  }
  return;
}

export function pendingPairingJSON(p:PendingPairing):Buffer {
  return json({link:parseJSON(pairingLinkJSON(p.link)),request:controlJSON(p.request),delegation:controlJSON(p.delegation),package:b64u(p.package)});
}
export function parsePendingPairing(data:Uint8Array):PendingPairing {
  const j=object(parseJSON(data)),link=parsePairingLink(json(j.link)),request=parseControl(j.request!,'request'),delegation=parseControl(j.delegation!,'delegation'),packageBytes=raw(j.package);
  const r=request.fields,d=delegation.fields;
  if(!eq(r.hub as Uint8Array,link.hub)||!eq(r.genesis as Uint8Array,link.genesis)||!eq(r.participant as Uint8Array,link.participant)||r.how!==How.byPairing||(r.enrollment as Uint8Array).some(byte=>byte!==0)||!eq(r.authority as Uint8Array,objectID(delegation.kind,d))||!eq(d.package as Uint8Array,sha(packageBytes))||!eq(d.token as Uint8Array,link.token)||!eq(d.hub as Uint8Array,link.hub)||!eq(d.genesis as Uint8Array,link.genesis)||!eq(d.participant as Uint8Array,link.participant)||!eq(d.key as Uint8Array,publicKey(pairingInviteKey(link.secret,link.hub,link.token)))||!request.signatures.self||!verifyControl(request.kind,r,r.sign as Uint8Array,request.signatures.self)||!request.signatures.approver||!verifyControl(request.kind,r,d.key as Uint8Array,request.signatures.approver))throw new Error('pending pairing does not match its link');
  return{link,request,delegation,package:packageBytes};
}

/** A certificate alone does not release home access. Require the exact request,
 * delegation, package hash and a decryptable gate release to these device keys.
 * The returned key is still a candidate until HomePolicy confirms its start. */
export function releasePairing(pending:PendingPairing,registry:Registry,messages:readonly Message[],keys:PairingKeys):PairingRelease|undefined {
  const p=parsePendingPairing(pendingPairingJSON(pending)),link=p.link,pin=registry.pin;
  if(!eq(pin.hub,link.hub)||!eq(pin.genesis,link.genesis))throw new Error('pairing registry belongs to another hub');
  registry.add(p.delegation);
  const requestID=objectID(p.request.kind,p.request.fields),delegationID=objectID(p.delegation.kind,p.delegation.fields);
  const certificates=registry.allCertificates.filter(c=>eq(c.requestID,requestID)&&eq(c.participant,link.participant)&&eq(c.sign,publicKey(keys.signing))&&eq(c.agree,publicKey(keys.agreement))&&registry.activity(c.id).kind==='valid');
  const tag=inboxTag(link.participant,link.hub);
  for(const message of messages){if(message.channel!==tag)continue;
    try{const j=object(parseJSON(openInbox(link.hub,link.participant,keys.agreement,message.body)));if(j.type!=='pair-release'||!eq(raw(j.delegation,32),delegationID)||!eq(raw(j.package,32),sha(p.package)))continue;
      const id=raw(j.certificate,32),certificate=certificates.find(c=>eq(c.id,id));if(!certificate)continue;
      const contents=object(parseJSON(openPackage(link.secret,raw(j.a,32),link.hub,link.token,p.package)));
      return{certificate,epoch:raw(contents.home,32),key:raw(contents.k,32)};
    }catch{}
  }
  return;
}
