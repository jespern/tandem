import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import * as t from '../dist/index.js';

const v = JSON.parse(await readFile(new URL('../../../vectors/v2/control.json', import.meta.url), 'utf8'));
const hex = s => Buffer.from(s, 'hex');

test('control schema matches the vectors', () => assert.deepEqual(t.controlSchema(), v.schema));

test('control objects: input, ID, signatures and byte-identical JSON', () => {
  for (const row of v.objects) {
    const o = t.controlFromJSON(Buffer.from(row.json)), name = row.name;
    assert.equal(o.kind, row.kind, name);
    assert.equal(t.signedInput(o.kind, o.fields).toString('hex'), row.input, name);
    assert.equal(t.objectID(o.kind, o.fields).toString('hex'), row.id, name);
    assert.deepEqual(Object.keys(o.signatures).sort(), Object.keys(row.signers).sort(), name);
    for (const [sig, key] of Object.entries(row.signers)) {
      assert.equal(t.verifyControl(o.kind, o.fields, hex(key), o.signatures[sig]), true, name);
      const tampered = Buffer.from(o.signatures[sig]); tampered[0] ^= 1;
      assert.equal(t.verifyControl(o.kind, o.fields, hex(key), tampered), false, name);
    }
    assert.equal(t.controlToJSON(o.kind, o.fields, o.signatures, o.extras).toString('utf8'), row.json, name);
  }
});

test('control rejects', () => {
  for (const row of v.rejects) assert.throws(() => t.controlFromJSON(Buffer.from(row.json)), undefined, row.why);
});

test('a null optional signature is absent; mailboxes are capped at 64', () => {
  const row = v.objects.find(o => o.kind === 'request' && !('approver' in o.signers)), j = JSON.parse(row.json);
  const o = t.controlFromJSON(Buffer.from(JSON.stringify({...j, approver: null})));
  assert.deepEqual(Object.keys(o.signatures), ['self']); assert.equal(t.controlToJSON(o.kind, o.fields, o.signatures).toString('utf8'), row.json);
  const genesis = t.controlFromJSON(Buffer.from(v.objects.find(o => o.kind === 'genesis').json)).fields;
  const many = n => [...Array(n).keys()].map(i => ({host: `imap${String(i).padStart(3, '0')}.example.org`, port: 993, user: 'hub@example.org'}));
  assert.equal(t.signedInput('genesis', {...genesis, mailboxes: many(64)}).length > 0, true);
  assert.throws(() => t.signedInput('genesis', {...genesis, mailboxes: many(65)}));
  assert.throws(() => t.signedInput('genesis', {...genesis, mailboxes: [{host: 'imap.exämple.org', port: 993, user: 'u'}]}));
});

test('control derivations and boxes open to the vectors', () => {
  const m = Object.fromEntries(Object.entries(v.material).map(([k, value]) => [k, hex(value)]));
  const d = v.derived, manager = t.managerKeys(m.management_seed), hub = t.publicKey(manager.signing);
  assert.equal(t.publicKey(t.inviteKey(m.invite_secret, hub, m.invite)).toString('hex'), d.invite_key);
  assert.equal(t.publicKey(t.pairingInviteKey(m.link, hub, m.token)).toString('hex'), d.pairing_invite_key);
  assert.equal(t.pairingKey(m.link, hub, m.token).toString('hex'), d.pairing_key);
  assert.equal(t.pairingTag(m.link, hub, m.token), d.pairing_tag);
  assert.equal(t.directoryScope(hub).toString('hex'), d.directory_scope);
  assert.equal(t.publicKey(manager.agreement).toString('hex'), d.manager_agreement);
  assert.deepEqual(t.openPackage(m.link, m.gate_secret, hub, m.token, hex(d.package)), hex(d.package_plaintext));
  const alice = hex(v.objects.find(o => o.name === 'alice registration').id);
  const registration = t.controlFromJSON(Buffer.from(v.objects.find(o => o.name === 'alice registration').json)).fields;
  assert.deepEqual(t.registrationID(registration.hub, registration.sign, registration.agree, registration.enrollment), alice);
  assert.deepEqual(t.openGate(manager.agreement, hub, m.token, alice, hex(d.gate)), m.gate_secret);
  assert.deepEqual(t.openHomeRecovery(manager.agreement, hub, m.home_epoch, hex(d.home_recovery)), m.creator_home_key);
  assert.throws(() => t.openPackage(m.link, Buffer.alloc(32), hub, m.token, hex(d.package)));
  assert.throws(() => t.openGate(manager.agreement, hub, m.token, Buffer.alloc(32), hex(d.gate)));
  assert.throws(() => t.openHomeRecovery(manager.agreement, hub, Buffer.alloc(32), hex(d.home_recovery)));
});

test('fresh control boxes and signatures round-trip', () => {
  const {signing, agreement} = t.deviceKeys(), recipient = t.publicKey(agreement);
  const hub = Buffer.alloc(32), token = Buffer.alloc(16), participant = Buffer.from([...Array(32).keys()]), a = Buffer.alloc(32, 'A');
  assert.deepEqual(t.openGate(agreement, hub, token, participant, t.sealGate(recipient, hub, token, participant, a)), a);
  assert.deepEqual(t.openHomeRecovery(agreement, hub, participant, t.sealHomeRecovery(recipient, hub, participant, a)), a);
  const link = Buffer.alloc(32, 'L'), box = t.sealPackage(link, a, hub, token, Buffer.from('{}'));
  assert.deepEqual(t.openPackage(link, a, hub, token, box), Buffer.from('{}'));
  const fields = {hub, genesis: participant, outcome: t.Outcome.inviteCancelled, subject: participant, signer: null};
  const sig = t.signControl('receipt', fields, signing);
  assert.equal(t.verifyControl('receipt', fields, t.publicKey(signing), sig), true);
  const back = t.controlFromJSON(t.controlToJSON('receipt', fields, {sig}));
  assert.deepEqual(back.signatures.sig, sig); assert.equal(back.fields.signer, null);
  assert.throws(() => t.signedInput('receipt', {...fields, signer: Buffer.alloc(32)}));
  assert.throws(() => t.controlToJSON('receipt', fields));
});
