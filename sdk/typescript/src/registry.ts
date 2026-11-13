import {Buffer} from 'buffer';
import {bytes, b64u} from './crypto.js';
import {type ControlObject, type Fields, controlFromJSON, controlToJSON, objectID, verifyControl, registrationID, How} from './control.js';

export type Validity = {kind: 'valid'} | {kind: 'invalid'; reason: string} | {kind: 'pending'; missing: Uint8Array[]};
export class RegistryHoldingLimit extends Error {
  constructor(readonly id: Uint8Array) { super('registry witness holding limit exceeded'); }
}
const valid: Validity = Object.freeze({kind: 'valid'});
const invalid = (reason: string): Validity => ({kind: 'invalid', reason});
const pending = (...missing: Uint8Array[]): Validity => ({kind: 'pending', missing: missing.map(x => Uint8Array.from(x))});
const equal = (a: Uint8Array, b: Uint8Array): boolean => Buffer.compare(a, b) === 0;
const has = (ids: readonly Uint8Array[], id: Uint8Array): boolean => ids.some(value => equal(value, id));
const all = (values: Validity[]): Validity => {
  const bad = values.find(v => v.kind === 'invalid'); if (bad) return bad;
  const missing = values.flatMap(v => v.kind === 'pending' ? v.missing : []); return missing.length ? pending(...missing) : valid;
};
const idOf = (o: ControlObject): Uint8Array => objectID(o.kind, o.fields);
const proof = (o: ControlObject, name: string, key: Uint8Array): boolean => !!o.signatures[name] && verifyControl(o.kind, o.fields, key, o.signatures[name]!);
const copyObject = (o: ControlObject): ControlObject => controlFromJSON(controlToJSON(o.kind, o.fields, o.signatures, o.extras));
export interface HubPin {hub: Uint8Array; agree: Uint8Array; genesis: Uint8Array; creator: Uint8Array}
export function pinGenesis(genesis: ControlObject): HubPin {
  const g = copyObject(genesis), hub = g.fields.hub as Uint8Array;
  if (g.kind !== 'genesis' || !proof(g, 'sig', hub)) throw new Error('genesis is not signed by its hub');
  return {hub: bytes(hub, 32), agree: bytes(g.fields.agree as Uint8Array, 32), genesis: idOf(g), creator: bytes(g.fields.creator as Uint8Array, 32)};
}
export interface Certificate {
  id: Uint8Array; requestID: Uint8Array; participant: Uint8Array; sign: Uint8Array; agree: Uint8Array; label: string;
  how: number; authority: Uint8Array | null; enrollment: Uint8Array; issued: Uint8Array[]; object: ControlObject; request: ControlObject;
}
interface Revocation {id: Uint8Array; target: Uint8Array; retained: Uint8Array[]}
interface Snapshot {id: Uint8Array; participant: Uint8Array; certs: Uint8Array[]; revocations: Uint8Array[]}
const certificate = (o: ControlObject): Certificate => {
  const request = o.extras.body as ControlObject;
  if (!request || request.kind !== 'request' || !equal(idOf(request), o.fields.request as Uint8Array)) throw new Error('certificate lacks its exact request');
  const f = request.fields;
  return {id: idOf(o), requestID: idOf(request), participant: f.participant as Uint8Array, sign: f.sign as Uint8Array,
    agree: f.agree as Uint8Array, label: f.label as string, how: f.how as number, authority: f.authority as Uint8Array | null,
    enrollment: f.enrollment as Uint8Array, issued: o.fields.issued as Uint8Array[], object: o, request};
};

/** The Swift/Lean registry rules, with explicit missing dependencies. No I/O or clocks. */
export class Registry {
  readonly #pin: HubPin; readonly #maxDepth: number; readonly #maxWitnesses: number;
  readonly #certs = new Map<string, Certificate>(); readonly #revs = new Map<string, Revocation>();
  readonly #certWitnesses = new Map<string, Certificate[]>();
  readonly #snapshots = new Map<string, Snapshot>(); readonly #delegations = new Map<string, ControlObject[]>();
  readonly #capabilities = new Map<string, ControlObject>();
  constructor(pin: HubPin, maxDepth = 64, maxWitnesses = 16) {
    if (!Number.isSafeInteger(maxDepth) || maxDepth < 1 || !Number.isSafeInteger(maxWitnesses) || maxWitnesses < 1) throw new Error('invalid registry limits'); this.#maxDepth = maxDepth; this.#maxWitnesses = maxWitnesses;
    this.#pin = {hub: bytes(pin.hub,32), agree: bytes(pin.agree,32), genesis: bytes(pin.genesis,32), creator: bytes(pin.creator,32)};
  }
  get pin(): HubPin { return Object.fromEntries(Object.entries(this.#pin).map(([name,value]) => [name, Uint8Array.from(value)])) as unknown as HubPin; }
  #bound(o: ControlObject): boolean {
    return (!o.fields.hub || equal(o.fields.hub as Uint8Array, this.#pin.hub)) && (!o.fields.genesis || equal(o.fields.genesis as Uint8Array, this.#pin.genesis));
  }
  add(object: ControlObject): Uint8Array {
    const o = copyObject(object); if (!this.#bound(o)) throw new Error('registry object belongs to another hub'); const id = idOf(o), key = b64u(id), f = o.fields;
    switch (o.kind) {
      case 'certificate': {
        const c = certificate(o); if (!this.#bound(c.request)) throw new Error('request belongs to another hub');
        const witnesses = this.#certWitnesses.get(key) ?? [];
        const encoded = controlToJSON(o.kind,o.fields,o.signatures,o.extras);
        if (!witnesses.some(w => equal(controlToJSON(w.object.kind,w.object.fields,w.object.signatures,w.object.extras),encoded))) {
          // Try a fully verifiable incoming witness even at the holding limit.
          // A flood of incomplete variants may exhaust a budget, but must never
          // overwrite an already valid witness or turn it into an invalid ID.
          if (witnesses.length >= this.#maxWitnesses) {
            if (this.#oneCertOK(c,0).kind !== 'valid') throw new RegistryHoldingLimit(Uint8Array.from(id));
            this.#certWitnesses.set(key,[c]);
          } else this.#certWitnesses.set(key,[...witnesses,c]);
        }
        if (!this.#certs.has(key)) this.#certs.set(key,c); break;
      }
      case 'revocation': if (!proof(o,'sig',this.#pin.hub)) throw new Error('revocation is not signed by the hub'); this.#revs.set(key,{id,target:f.target as Uint8Array,retained:f.retained as Uint8Array[]}); break;
      case 'invitation': if (!proof(o,'sig',this.#pin.hub)) throw new Error('invitation is not signed by the hub'); this.#capabilities.set(key,o); break;
      case 'delegation': {
        const witnesses=this.#delegations.get(key)??[], encoded=controlToJSON(o.kind,o.fields,o.signatures,o.extras);
        if (!witnesses.some(w=>equal(controlToJSON(w.kind,w.fields,w.signatures,w.extras),encoded))) {
          if (witnesses.length>=this.#maxWitnesses) {
            const source=this.#certs.get(b64u(f.source as Uint8Array));
            if (!source || !proof(o,'sig',source.sign)) throw new RegistryHoldingLimit(Uint8Array.from(id));
            this.#delegations.set(key,[o]);
          } else this.#delegations.set(key,[...witnesses,o]);
        }
        break;
      }
      case 'snapshot': this.#snapshots.set(key,{id,participant:f.participant as Uint8Array,certs:f.certs as Uint8Array[],revocations:f.revocations as Uint8Array[]}); break;
      default: throw new Error('not a registry object');
    }
    return Uint8Array.from(id);
  }
  certificate(id: Uint8Array): Certificate | undefined { const witnesses=this.#certWitnesses.get(b64u(id)); const c=witnesses?.find(w=>this.#oneCertOK(w,0).kind==='valid')??witnesses?.[0]; return c ? certificate(copyObject(c.object)) : undefined; }
  get allCertificates(): Certificate[] { return [...this.#certs.values()].map(c => this.certificate(c.id)!); }
  #survives(revs: Revocation[], candidate: Certificate): boolean {
    return revs.every(r => {
      const target = this.#certs.get(b64u(r.target)); if (!target || !equal(target.participant,candidate.participant)) return true;
      return !(equal(target.sign,candidate.sign) && equal(target.agree,candidate.agree)) && (has(r.retained,candidate.id) || has(candidate.issued,r.id));
    });
  }
  certificateValidity(id: Uint8Array): Validity { return this.#certOK(id,0); }
  #certOK(id: Uint8Array, depth: number): Validity {
    const witnesses=this.#certWitnesses.get(b64u(id)); if(!witnesses)return pending(id);
    let missing:Uint8Array[]=[], bad:Validity=invalid('no valid certificate proof');
    for(const c of witnesses){const outcome=this.#oneCertOK(c,depth);if(outcome.kind==='valid')return outcome;if(outcome.kind==='pending')missing.push(...outcome.missing);else bad=outcome;}
    return missing.length?pending(...missing):bad;
  }
  #oneCertOK(c: Certificate, depth: number): Validity {
    if (depth >= this.#maxDepth) return invalid('approval chain too deep');
    if (!proof(c.request,'self',c.sign)) return invalid('request not signed by its own keys');
    const missingRevs = c.issued.filter(id => !this.#revs.has(b64u(id))); if (missingRevs.length) return pending(...missingRevs);
    const issued = c.issued.map(id => this.#revs.get(b64u(id))!);
    const missingTargets = issued.map(r => r.target).filter(id => !this.#certs.has(b64u(id))); if (missingTargets.length) return pending(...missingTargets);
    const registration = registrationID(this.#pin.hub,c.sign,c.agree,c.enrollment);
    const managerProof = () => proof(c.object,'sig',this.#pin.hub) ? valid : invalid('not signed by the hub');
    switch (c.how) {
      case How.first: if (c.authority !== null || !equal(c.participant,registration)) return invalid('first admission of other keys'); return managerProof();
      case How.creatorRecovery: if (c.authority !== null || !equal(c.participant,this.#pin.creator)) return invalid('recovery for someone but the creator'); return managerProof();
      case How.invited: {
        if (!c.authority) return invalid('invited without a capability'); const cap = this.#capabilities.get(b64u(c.authority)); if (!cap) return pending(c.authority);
        if (!equal(c.participant,registration)) return invalid('invitation for other keys');
        if (!proof(c.request,'approver',cap.fields.key as Uint8Array)) return invalid('claim not signed by the invite key');
        return proof(c.object,'sig',cap.fields.key as Uint8Array) ? valid : invalid('not signed by the invite key');
      }
      case How.byDevice: {
        if (!c.authority) return invalid('no approving device'); const source = this.#certs.get(b64u(c.authority)); if (!source) return pending(c.authority);
        if (!equal(source.participant,c.participant)) return invalid("approved by another participant's device");
        if (!proof(c.request,'approver',source.sign)) return invalid('not approved by that device');
        if (!this.#survives(issued,source)) return invalid('approving device revoked at issuance');
        const ok = managerProof(); return ok.kind === 'valid' ? this.#certOK(source.id,depth+1) : ok;
      }
      case How.byPairing: {
        if (!c.authority) return invalid('no delegation'); const variants = this.#delegations.get(b64u(c.authority)); if (!variants) return pending(c.authority); const f=variants[0]!.fields;
        if (!equal(f.participant as Uint8Array,c.participant)) return invalid('delegation for another participant');
        if (!proof(c.request,'approver',f.key as Uint8Array)) return invalid('claim not signed by the link key');
        const source=this.#certs.get(b64u(f.source as Uint8Array)); if (!source) return pending(f.source as Uint8Array);
        if (!equal(source.participant,f.participant as Uint8Array) || !variants.some(dl=>proof(dl,'sig',source.sign))) return invalid('delegation not signed by a device of that participant');
        if (!this.#survives(issued,source)) return invalid('delegating device revoked at issuance');
        const ok=managerProof();return ok.kind==='valid'?this.#certOK(source.id,depth+1):ok;
      }
      default: return invalid('unknown admission');
    }
  }
  revocationValidity(id: Uint8Array): Validity {
    const rev=this.#revs.get(b64u(id));if(!rev)return pending(id);const target=this.#certs.get(b64u(rev.target));if(!target)return pending(rev.target);
    const missing:Uint8Array[]=[];for(const id of rev.retained){const c=this.#certs.get(b64u(id));if(!c)missing.push(id);else if(!equal(c.participant,target.participant))return invalid("retains another participant's device");}
    return missing.length?pending(...missing):valid;
  }
  activity(id:Uint8Array, revocationIDs?:readonly Uint8Array[]):Validity {
    const ok=this.certificateValidity(id);if(ok.kind!=='valid')return ok;const candidate=this.#certs.get(b64u(id))!;
    const ids=revocationIDs??[...this.#revs.values()].map(r=>r.id),missing=ids.filter(id=>!this.#revs.has(b64u(id)));if(missing.length)return pending(...missing);
    const checked=all(ids.map(id=>this.revocationValidity(id)));if(checked.kind!=='valid')return checked;
    return this.#survives(ids.map(id=>this.#revs.get(b64u(id))!),candidate)?valid:invalid('revoked');
  }
  currentSnapshot(participant:Uint8Array):ControlObject {
    const certs=[...this.#certs.values()].filter(c=>equal(c.participant,participant)&&this.certificateValidity(c.id).kind==='valid').map(c=>c.id).sort(Buffer.compare);
    const revocations=[...this.#revs.values()].filter(r=>{const c=this.#certs.get(b64u(r.target));return c&&equal(c.participant,participant)&&this.revocationValidity(r.id).kind==='valid';}).map(r=>r.id).sort(Buffer.compare);
    return controlFromJSON(controlToJSON('snapshot',{hub:this.#pin.hub,genesis:this.#pin.genesis,participant,certs,revocations}));
  }
  authorizes(snapshot:Uint8Array,certificate:Uint8Array):Validity {
    const s=this.#snapshots.get(b64u(snapshot));if(!s)return pending(snapshot);const c=this.#certs.get(b64u(certificate));if(!c)return pending(certificate);
    if(!equal(s.participant,c.participant)||!has(s.certs,certificate))return invalid('not in its snapshot');
    const named=all([...s.certs.map(id=>this.certificateValidity(id)),...s.revocations.map(id=>this.revocationValidity(id))]);return named.kind==='valid'?this.activity(certificate,s.revocations):named;
  }
  extendsFloor(source:Uint8Array,floor:Uint8Array):Validity {
    const s=this.#snapshots.get(b64u(source));if(!s)return pending(source);const f=this.#snapshots.get(b64u(floor));if(!f)return pending(floor);
    if(!equal(s.participant,f.participant))return invalid('snapshots of two participants');
    return f.certs.every(id=>has(s.certs,id))&&f.revocations.every(id=>has(s.revocations,id))?valid:invalid('does not extend the floor');
  }
}
