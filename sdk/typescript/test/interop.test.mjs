import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile, readdir} from 'node:fs/promises';
import * as t from '../dist/index.js';

test('cross SDK signatures and ciphertexts', {skip: process.env.TANDEM_VERIFY_INTEROP !== '1'}, async () => {
  const v = JSON.parse(await readFile(new URL('../../../vectors/v2/core.json', import.meta.url), 'utf8'));
  const hex = s => Buffer.from(s, 'hex'), m = v.material;
  const key = t.privateKey(hex(m.agreement_private), 'x25519');
  for (const file of (await readdir(process.env.TANDEM_INTEROP_DIR)).filter(name => name.endsWith('.json')).sort()) {
    const language = file.slice(0, -5);
    const artifact = JSON.parse(await readFile(`${process.env.TANDEM_INTEROP_DIR}/${language}.json`, 'utf8'));
    assert.equal(t.verifyStrict(hex(m.signing_public), hex(artifact.signature), hex(v.plaintext)), true, language);
    assert.deepEqual(t.openInbox(hex(m.hub), hex(m.uid), key, hex(artifact.inbox)), hex(v.plaintext), language);
  }
});
