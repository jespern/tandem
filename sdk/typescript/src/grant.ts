import {Buffer} from 'buffer';
import {bytes,commit} from './crypto.js';
import {type ControlObject,controlFromJSON,controlToJSON,verifyControl} from './control.js';
import {CollectionPolicy} from './roster.js';
import {Registry,type Validity} from './registry.js';
const eq=(a:Uint8Array,b:Uint8Array)=>Buffer.compare(a,b)===0;
const bad=(reason:string):Validity=>({kind:'invalid',reason}),hold=(...missing:Uint8Array[]):Validity=>({kind:'pending',missing:missing.map(value=>bytes(value))});
/** A grant authenticates one admission and epoch, not just a readable key.
 * Historical owner authority is retained; current recipient cutoff is enforced. */
export function grantValidity(input:ControlObject,key:Uint8Array,recipientCertificate:Uint8Array,collection:CollectionPolicy,registry:Registry):Validity {
  const grant=controlFromJSON(controlToJSON(input.kind,input.fields,input.signatures,input.extras)),f=grant.fields;
  if(grant.kind!=='grant'||!eq(f.hub as Uint8Array,registry.pin.hub)||!eq(f.collection as Uint8Array,collection.id)||!eq(f.key as Uint8Array,commit(key)))return bad('grant belongs to another hub, collection or key');
  const recipient=registry.certificate(recipientCertificate);if(!recipient)return hold(recipientCertificate);
  if(!eq(f.recipient as Uint8Array,recipient.participant))return bad('grant is for another participant');
  const cutoff=registry.activity(recipientCertificate);if(cutoff.kind!=='valid')return cutoff;
  if(collection.dead)return bad('collection has conflicting owner decisions');
  const owner=collection.owner,at=f.at as Uint8Array,statement=collection.statement(at),epoch=collection.epoch(at);
  if(!owner||!statement||!epoch)return hold(at);
  if(!eq(epoch,f.epoch as Uint8Array)||!eq(statement.commit,commit(key))||!statement.members.some(m=>eq(m.uid,recipient.participant)))return bad('roster does not grant this epoch to this participant');
  const admission=collection.admission(recipient.participant,at);
  if(!admission||!eq(admission,f.admission as Uint8Array))return bad('grant names another admission');
  const head=collection.head;
  if(head&&collection.chain(head).some(id=>eq(id,at))){const current=collection.admission(recipient.participant,head);if(!current||!eq(current,admission))return bad('grant belongs to an earlier admission');}
  const authorized=registry.authorizes(f.auth as Uint8Array,f.signer as Uint8Array);if(authorized.kind!=='valid')return authorized;
  const signer=registry.certificate(f.signer as Uint8Array);
  if(!signer||!eq(signer.participant,owner)||!grant.signatures.sig||!verifyControl(grant.kind,f,signer.sign,grant.signatures.sig))return bad('grant is not signed by the fixed owner');
  return{kind:'valid'};
}
/** A fully checked grant announces each epoch in its roster line. Recheck after
 * adoption: an owner-granted conflicting branch proves the collection dead. */
export function adoptGrant(grant:ControlObject,key:Uint8Array,recipientCertificate:Uint8Array,collection:CollectionPolicy,registry:Registry):Validity {
  const checked=grantValidity(grant,key,recipientCertificate,collection,registry);if(checked.kind!=='valid')return checked;
  for(const id of collection.chain(grant.fields.at as Uint8Array))collection.announce(id);
  return grantValidity(grant,key,recipientCertificate,collection,registry);
}
