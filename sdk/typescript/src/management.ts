import {Buffer} from 'buffer';
import {b64u,unb64u,bytes,randomBytes as bytesRandom,sha,publicKey,managerKeys,seal,sealInbox,inboxTag,directoryTag,directoryKey,openEnvelope} from './crypto.js';
import {type ControlObject,type Fields,controlFromJSON,controlToJSON,parseControl,objectID,verifyControl,signControl,openGate,inviteKey,How,Outcome} from './control.js';
import {parseJSON} from './json.js';
import {Registry} from './registry.js';
import type {PreparedOperation,OutgoingMessage} from './operation.js';
import type {Message} from './session.js';
const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
const id=(o:ControlObject)=>objectID(o.kind,o.fields);
const clone=(o:ControlObject)=>controlFromJSON(controlToJSON(o.kind,o.fields,o.signatures,o.extras));
const encoded=(value:unknown)=>Buffer.from(JSON.stringify(value));
const controlJSON=(o:ControlObject)=>parseJSON(controlToJSON(o.kind,o.fields,o.signatures,o.extras));
interface Claim {request:ControlObject;delegation:ControlObject;gate:Uint8Array}
export interface DeviceClaim {id:Uint8Array;participant:Uint8Array;label:string;token:Uint8Array;expires:number}
/** Root-signed administration, separate from member data authority. It holds no
 * management seed: a caller unlocks one for an operation, then may erase it. */
export class DeviceManagement {
  readonly #registry:Registry;readonly #directory:Uint8Array;
  readonly #capsules=new Map<string,ControlObject>();readonly #receipts=new Map<string,ControlObject>();readonly #claims=new Map<string,Claim>();
  constructor(registry:Registry,directory:Uint8Array){this.#registry=registry;this.#directory=bytes(directory,32);}
  #bound(o:ControlObject):boolean{return eq(o.fields.hub as Uint8Array,this.#registry.pin.hub)&&eq(o.fields.genesis as Uint8Array,this.#registry.pin.genesis);}
  offerDirectory(o:ControlObject):void{
    if(o.kind!=='capsule'&&o.kind!=='receipt'||!this.#bound(o))return;
    if(o.kind==='capsule'){
      const box=o.extras.capsule;if(!(box instanceof Uint8Array)||box.length!==76||!eq(sha(box),o.fields.sealed as Uint8Array)||!o.signatures.sig||!verifyControl(o.kind,o.fields,this.#registry.pin.hub,o.signatures.sig))return;
      this.#capsules.set(b64u(id(o)),clone(o));
    }else this.#receipts.set(b64u(sha(controlToJSON(o.kind,o.fields,o.signatures))),clone(o));
  }
  offerClaim(value:Record<string,any>):void{
    const request=parseControl(value.request,'request'),delegation=parseControl(value.delegation,'delegation');if(typeof value.gate!=='string')return;const gate=unb64u(value.gate);
    if(!this.#bound(request)||!this.#bound(delegation))return;
    const witness=b64u(sha(encoded(value)));this.#claims.set(witness,{request,delegation,gate});
  }
  #receipt(outcome:number,subject:Uint8Array):boolean{
    return [...this.#receipts.values()].some(o=>{const f=o.fields;if(f.outcome!==outcome||!eq(f.subject as Uint8Array,subject)||!o.signatures.sig)return false;
      if(outcome===Outcome.rejected||outcome===Outcome.inviteCancelled)return f.signer===null&&verifyControl(o.kind,f,this.#registry.pin.hub,o.signatures.sig);
      const cert=f.signer&&this.#registry.certificate(f.signer as Uint8Array);if(!cert||!verifyControl(o.kind,f,cert.sign,o.signatures.sig))return false;
      const participant=outcome===Outcome.paired?this.#registry.certificate(subject)?.participant:(this.#registry.delegation(subject)?.fields.participant??[...this.#claims.values()].find(c=>eq(id(c.delegation),subject))?.delegation.fields.participant) as Uint8Array|undefined;
      return !!participant&&eq(participant,cert.participant)&&(outcome===Outcome.paired?this.#registry.certificateValidity(cert.id):this.#registry.activity(cert.id)).kind==='valid';
    });
  }
  #valid(c:Claim,allowPending=false):boolean{
    const r=c.request,d=c.delegation,f=r.fields,g=d.fields,source=this.#registry.certificate(g.source as Uint8Array);
    return f.how===How.byPairing&&eq(f.participant as Uint8Array,g.participant as Uint8Array)&&!!f.authority&&eq(f.authority as Uint8Array,id(d))&&(f.enrollment as Uint8Array).every(b=>b===0)&&eq(sha(c.gate),g.gate as Uint8Array)&&!!r.signatures.self&&verifyControl(r.kind,f,f.sign as Uint8Array,r.signatures.self)&&!!r.signatures.approver&&verifyControl(r.kind,f,g.key as Uint8Array,r.signatures.approver)&&!!source&&eq(source.participant,g.participant as Uint8Array)&&(allowPending?this.#registry.activity(source.id).kind!=='invalid':this.#registry.activity(source.id).kind==='valid')&&!!d.signatures.sig&&verifyControl(d.kind,g,source.sign,d.signatures.sig);
  }
  #find(subject:Uint8Array,now:number,allowPending=false):Claim|undefined{return [...this.#claims.values()].find(c=>eq(id(c.request),subject)&&this.#valid(c,allowPending)&&(c.delegation.fields.expires as number)>now&&!this.#receipt(Outcome.pairingCancelled,id(c.delegation))&&!this.#receipt(Outcome.rejected,id(c.request)));}
  claims(now=Date.now()):DeviceClaim[]{
    const certified=new Set(this.#registry.allCertificates.filter(c=>this.#registry.certificateValidity(c.id).kind==='valid').map(c=>b64u(c.requestID))),result=new Map<string,DeviceClaim>();
    for(const c of this.#claims.values()){const subject=id(c.request),key=b64u(subject);if(result.has(key)||certified.has(key)||!this.#valid(c)||(c.delegation.fields.expires as number)<=now||this.#receipt(Outcome.pairingCancelled,id(c.delegation))||this.#receipt(Outcome.rejected,subject))continue;
      result.set(key,{id:subject,participant:bytes(c.request.fields.participant as Uint8Array),label:c.request.fields.label as string,token:bytes(c.delegation.fields.token as Uint8Array),expires:c.delegation.fields.expires as number});
    }return [...result.values()].sort((a,b)=>Buffer.compare(Buffer.concat([a.token,a.id]),Buffer.concat([b.token,b.id])));
  }
  capsules():Uint8Array[]{const named=new Set([...this.#capsules.values()].flatMap(o=>(o.fields.supersedes as Uint8Array[]).map(b64u)));return [...this.#capsules].filter(([key])=>!named.has(key)).sort(([a],[b])=>Buffer.compare(unb64u(a),unb64u(b))).map(([,o])=>bytes(o.extras.capsule as Uint8Array));}
  #manager(seed:Uint8Array):ReturnType<typeof managerKeys>{const keys=managerKeys(seed);if(!eq(publicKey(keys.signing),this.#registry.pin.hub))throw new Error('management seed belongs to another hub');return keys;}
  #signed(kind:string,fields:Fields,key:ReturnType<typeof managerKeys>['signing'],extras:ControlObject['extras']={}):ControlObject{return controlFromJSON(controlToJSON(kind,fields,{sig:signControl(kind,fields,key)},extras));}
  #publish(objects:readonly ControlObject[],deferred=false):OutgoingMessage[]{const hub=this.#registry.pin.hub,channel=directoryTag(this.#directory,hub),key=directoryKey(this.#directory,hub);return objects.map(o=>({channel,body:seal(key,hub,channel,controlToJSON(o.kind,o.fields,o.signatures,o.extras),3),guard:{type:'control'},...(deferred?{deferred:true}:{})}));}
  approve(subject:Uint8Array,seed:Uint8Array,now=Date.now()):PreparedOperation{
    const manager=this.#manager(seed),claim=this.#find(subject,now);if(!claim)throw new Error('no valid unexpired claim with this ID');
    const registry=this.#registry.fork();registry.add(claim.delegation);
    const fields={hub:registry.pin.hub,genesis:registry.pin.genesis,request:subject,issued:registry.revocationIDs.sort(Buffer.compare)},certificate=this.#signed('certificate',fields,manager.signing,{body:claim.request}),certificateID=registry.add(certificate);
    if(registry.activity(certificateID).kind!=='valid')throw new Error('claim cannot produce an active certificate');
    const snapshot=registry.currentSnapshot(claim.request.fields.participant as Uint8Array),a=openGate(manager.agreement,registry.pin.hub,claim.delegation.fields.token as Uint8Array,claim.request.fields.participant as Uint8Array,claim.gate);
    const release=encoded({type:'pair-release',certificate:b64u(certificateID),delegation:b64u(id(claim.delegation)),package:b64u(claim.delegation.fields.package as Uint8Array),a:b64u(a)});a.fill(0);
    const participant=claim.request.fields.participant as Uint8Array,channel=(inboxTag(participant,registry.pin.hub));
    return{kind:'admin-approve',details:{claim:b64u(subject),certificate:b64u(certificateID)},phases:[{name:'publish-admission',messages:this.#publish([claim.delegation,certificate,snapshot],true).map(message=>({...message,guard:{type:'action',id:''}}))},{name:'release-pairing',requires:[0],messages:[{channel,body:sealInbox(registry.pin.hub,participant,claim.request.fields.agree as Uint8Array,release),guard:{type:'recipient',purpose:'pair-release',certificate:b64u(certificateID)}}]}]};
  }
  operationValid(kind:string,details:Record<string,string>):boolean{
    if(kind!=='admin-approve')return true;const certificate=unb64u(details.certificate!);
    if(this.#registry.certificate(certificate))return this.#registry.activity(certificate).kind!=='invalid';
    return !!this.#find(unb64u(details.claim!),Date.now(),true);
  }
  revoke(target:Uint8Array,seed:Uint8Array):PreparedOperation{
    const manager=this.#manager(seed),cert=this.#registry.certificate(target);if(!cert||this.#registry.certificateValidity(target).kind!=='valid')throw new Error('no valid certificate with this ID');
    const retained=this.#registry.allCertificates.filter(c=>eq(c.participant,cert.participant)&&!eq(c.id,target)&&!(eq(c.sign,cert.sign)&&eq(c.agree,cert.agree))&&this.#registry.activity(c.id).kind==='valid').map(c=>c.id).sort(Buffer.compare);
    const revocation=this.#signed('revocation',{hub:this.#registry.pin.hub,genesis:this.#registry.pin.genesis,target,retained},manager.signing),registry=this.#registry.fork();registry.add(revocation);
    return{kind:'admin-revoke',details:{subject:b64u(target)},phases:[{name:'publish-revocation',messages:this.#publish([revocation,registry.currentSnapshot(cert.participant)])}]};
  }
  receipt(outcome:typeof Outcome.rejected|typeof Outcome.inviteCancelled,subject:Uint8Array,seed:Uint8Array):PreparedOperation{
    const manager=this.#manager(seed);if(outcome===Outcome.rejected&&![...this.#claims.values()].some(c=>eq(id(c.request),subject)))throw new Error('no such claim');
    const receipt=this.#signed('receipt',{hub:this.#registry.pin.hub,genesis:this.#registry.pin.genesis,outcome,subject,signer:null},manager.signing);
    return{kind:'admin-receipt',details:{subject:b64u(subject)},phases:[{name:'publish-admin-answer',messages:this.#publish([receipt])}]};
  }
  invite(label:string,mail:{host:string;port:number;user:string;pass:string},seed:Uint8Array,validForMs=7*24*3600*1000,now=Date.now()):{link:Uint8Array;operation:PreparedOperation}{
    if(!Number.isSafeInteger(validForMs)||validForMs<1||!Number.isSafeInteger(now+validForMs))throw new Error('invalid invitation lifetime');
    const manager=this.#manager(seed),secret=bytesRandom(32),token=bytesRandom(16),key=inviteKey(secret,this.#registry.pin.hub,token),capability=this.#signed('invitation',{hub:this.#registry.pin.hub,genesis:this.#registry.pin.genesis,key:publicKey(key),invite:token,expires:now+validForMs,label},manager.signing);
    const link=encoded({v:2,hub:b64u(this.#registry.pin.hub),genesis:b64u(this.#registry.pin.genesis),hk:b64u(this.#directory),mail,s:b64u(secret),invitation:controlJSON(capability)}),values={name:`hubinvite:${b64u(token)}`,label,capability:b64u(id(capability)),made:String(now),hint:[...mail.pass].slice(-4).join('')};
    return{link,operation:{kind:'admin-invite',details:{link:b64u(link)},phases:[{name:'publish-invitation',messages:this.#publish([capability])},{name:'remember-invitation',requires:[0],messages:[],intent:{type:'invitation-home',values}}]}};
  }
  password(box:Uint8Array,seed:Uint8Array):PreparedOperation{
    const manager=this.#manager(seed);bytes(box,76);const named=new Map<string,Uint8Array>();for(const o of this.#capsules.values())for(const value of [id(o),...o.fields.supersedes as Uint8Array[]])named.set(b64u(value),value);
    const supersedes=[...named.values()].sort(Buffer.compare),capsule=this.#signed('capsule',{hub:this.#registry.pin.hub,genesis:this.#registry.pin.genesis,sealed:sha(box),supersedes},manager.signing,{capsule:Buffer.from(box)});
    return{kind:'admin-password',details:{},phases:[{name:'publish-password-capsule',messages:this.#publish([capsule],true)},{name:'remove-superseded-capsules',requires:[0],messages:[],intent:{type:'capsule-cleanup',values:{ids:JSON.stringify(supersedes.map(b64u))}}}]};
  }
  cleanup(ids:ReadonlySet<string>,deliveries:readonly Message[]):string[]{
    const hub=this.#registry.pin.hub,channel=directoryTag(this.#directory,hub),key=directoryKey(this.#directory,hub),destroy:string[]=[];
    for(const message of deliveries)if(message.channel===channel){try{const opened=openEnvelope(key,hub,channel,message.body);if(opened.kind!==3)continue;const o=controlFromJSON(opened.plaintext);if(o.kind==='capsule'&&this.#bound(o)&&ids.has(b64u(id(o))))destroy.push(message.id);}catch{}}
    return destroy;
  }
}
