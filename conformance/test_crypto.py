import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from cryptography.exceptions import InvalidSignature, InvalidTag
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
import wire as w

HERE = Path(__file__).resolve().parent


class Reference(unittest.TestCase):
    def test_exported_tandem_vectors(self):
        fixture = json.loads((HERE / 'fixtures/tandem-v1.json').read_text())
        self.assertEqual({k: v.hex() for k, v in w.identity(bytes.fromhex(fixture['secret'])).items()}, fixture['identity'])
        for case in fixture['writes']:
            args = case['inputs'].copy()
            for key in ('scope', 'rec', 'value', 'author', 'dev', 'ref'):
                args[key] = bytes.fromhex(args[key])
            for key in ('context', 'base'):
                args[key] = [(bytes.fromhex(d), m, c) for d, m, c in args[key]]
            data = w.write_input(**args)
            self.assertEqual(data.hex(), case['input'])
            self.assertEqual(w.sha(data).hex(), case['write_id'])
            self.assertTrue(w.strict_verify(args['author'], bytes.fromhex(case['signature']), data))
        s = fixture['symmetric']
        self.assertEqual(w.tag(bytes.fromhex(s['key']), bytes.fromhex(s['epoch'])), s['tag'])
        self.assertEqual(w.seal(bytes.fromhex(s['key']), s['tag'], bytes.fromhex(s['plaintext']),
                                salt=bytes.fromhex(s['salt'])).hex(), s['envelope'])

    def test_rfc9180_base_mode_vector(self):
        v = json.loads((HERE / 'fixtures/hpke-rfc9180.json').read_text())
        e = v['encryptions'][0]
        sealed = w.hpke_seal(bytes.fromhex(v['pkRm']), bytes.fromhex(e['pt']), bytes.fromhex(v['info']),
                             bytes.fromhex(e['aad']), bytes.fromhex(v['skEm']))
        self.assertEqual(sealed.hex(), v['enc'] + e['ct'])
        self.assertEqual(w.hpke_open(bytes.fromhex(v['skRm']), sealed, bytes.fromhex(v['info']), bytes.fromhex(e['aad'])).hex(), e['pt'])

    def test_rfc8032_ed25519_vector(self):
        seed = bytes.fromhex('9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60')
        key = ed25519.Ed25519PrivateKey.from_private_bytes(seed)
        public = bytes.fromhex('d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a')
        sig = bytes.fromhex('e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555f'
                            'b8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b')
        self.assertEqual(w.pub(key), public)
        self.assertEqual(key.sign(b''), sig)
        self.assertTrue(w.strict_verify(public, sig, b''))
        self.assertFalse(w.strict_verify(public, sig, b'changed'))
        self.assertFalse(w.strict_verify(public, sig[:32] + (int.from_bytes(sig[32:], 'little') + w.L).to_bytes(32, 'little'), b''))

    def test_strict_signature_policy(self):
        # Identity public key and R, S=0: a universal forgery accepted by some
        # native verifiers. Tandem explicitly forbids a small-order public key.
        identity = b'\x01' + bytes(31)
        self.assertFalse(w.strict_verify(identity, identity + bytes(32), b'any message'))
        self.assertFalse(w.strict_verify(bytes(32), bytes(64), b'message'))
        self.assertFalse(w.strict_verify((w.P + 1).to_bytes(32, 'little'), bytes(64), b'message'))

    def test_signature_policy_vectors_satisfy_the_plain_equation(self):
        vectors = json.loads((HERE / 'fixtures/ed25519-policy.json').read_text())
        for case in vectors['cases']:
            with self.subTest(case=case['name']):
                self.assertEqual(w.strict_verify(bytes.fromhex(case['public']), bytes.fromhex(case['signature']),
                                                bytes.fromhex(case['message'])), case['valid'])
        self.assertEqual(len(set(vectors['small_order'])), 8)
        for raw in vectors['small_order']:
            self.assertEqual(w.mul(w.point(bytes.fromhex(raw)), 8), w.IDENTITY)

    def test_signed_wire_and_tampering(self):
        who = w.identity(bytes(range(32)))
        scope, rec, ref = bytes(range(16)), w.named('_meta'), bytes(32)
        write, wid = w.signed_write(who, scope, rec, 'title', b'House', ref)
        self.assertEqual(w.verify_write(scope, rec, write), wid)
        for key, value in [('f', 'notes'), ('v', w.b64(b'changed')), ('r', w.b64(bytes([1])*32)), ('op', 4)]:
            bad = copy.deepcopy(write)
            bad[key] = value
            with self.assertRaises((InvalidSignature, ValueError)):
                w.verify_write(scope, rec, bad)

    def test_vectors_are_maps_with_bounded_clocks(self):
        d = bytes(8)
        for entries in ([(d, 1, 0), (d, 2, 0)], [(d, 2**48, 0)], [(d, 1, 2**16)], [(d, -1, 0)]):
            with self.assertRaises(ValueError):
                w.vector(entries)

    def test_inbox_binding_and_key_id(self):
        who = w.identity(bytes(range(32)))
        sealed = w.inbox_seal(who['uid'], who['uka'], w.dumps({'type': 'grant'}))
        self.assertEqual(w.inbox_open(who, sealed), w.dumps({'type': 'grant'}))
        with self.assertRaises(ValueError):
            w.inbox_open(w.identity(bytes([9])*32), sealed)
        with self.assertRaises(InvalidTag):
            w.inbox_open(who, sealed[:-1] + bytes([sealed[-1] ^ 1]))


@unittest.skipUnless(shutil.which('swiftc') and os.uname().sysname == 'Darwin', 'Apple CryptoKit needs macOS and swiftc')
class CryptoKit(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix='tandem-cryptokit-')
        cls.binary = Path(cls.temp.name) / 'cryptokit'
        subprocess.run(['swiftc', '-O', str(HERE / 'cryptokit.swift'), '-o', str(cls.binary)], check=True, capture_output=True, timeout=60)

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def apple(self, op, **request):
        result = subprocess.run([str(self.binary)], input=json.dumps(dict(op=op, **request)) + '\n',
                                text=True, capture_output=True, check=True, timeout=10)
        return json.loads(result.stdout)

    def test_key_derivation_matches_three_independent_secrets(self):
        for secret in (bytes(range(32)), bytes(32), bytes([255])*32):
            expected = {k: v.hex() for k, v in w.identity(secret).items()}
            self.assertEqual(self.apple('derive', secret=secret.hex()), expected)

    def test_canonical_write_encoding_and_signatures_match(self):
        who = w.identity(bytes(range(32)))
        for field in ('title', '\u00e9', 'e\u0301'):
            for operation in (1, 2, 3, 4):
                args = dict(scope=bytes(range(16)), rec=bytes(range(16, 32)), field=field, op=operation,
                            value=b'hello\x00\xff' if operation == 1 else b'', author=who['uid'],
                            dev=b'device01', millis=1731312000123, counter=65535, ref=bytes(range(32)),
                            context=[(b'device03', 5, 0), (b'device02', 3, 2)], base=[(b'device04', 1, 1)],
                            element=b'work' if operation in (2, 3) else b'')
                data = w.write_input(**args)
                native = self.apple('write', seed=who['sign'].hex(), scope=args['scope'].hex(), record=args['rec'].hex(),
                                    field=field, operation=operation, value=args['value'].hex(), device=args['dev'].hex(),
                                    millis=args['millis'], counter=args['counter'], ref=args['ref'].hex(), element=args['element'].hex(),
                                    context=[[d.hex(), m, c] for d, m, c in args['context']], base=[[d.hex(), m, c] for d, m, c in args['base']])
                self.assertEqual(native['input'], data.hex())
                self.assertEqual(native['id'], w.sha(data).hex())
                # Current CryptoKit uses randomized signing. The protocol ID
                # hashes sigInput, not the signature. Verify both directions.
                sig = bytes.fromhex(native['signature'])
                self.assertTrue(w.strict_verify(who['uid'], sig, data))
                ed25519.Ed25519PublicKey.from_public_bytes(who['uid']).verify(sig, data)
                python_sig = ed25519.Ed25519PrivateKey.from_private_bytes(who['sign']).sign(data)
                self.assertEqual(self.apple('verify', public=who['uid'].hex(), signature=python_sig.hex(), message=data.hex()), {'valid': True})

    def test_symmetric_envelopes_match_and_authenticate_every_header(self):
        key, salt = bytes(range(32)), bytes(range(32, 64))
        channel, plaintext = w.tag(key, bytes([8])*32), w.dumps({'hello': '\u00f8'})
        for kind in (1, 3):
            body = w.seal(key, channel, plaintext, kind, salt)
            self.assertEqual(self.apple('seal', key=key.hex(), salt=salt.hex(), tag=channel,
                                        plaintext=plaintext.hex(), kind=kind), {'envelope': body.hex()})
            self.assertEqual(self.apple('open', key=key.hex(), tag=channel, envelope=body.hex()), {'plaintext': plaintext.hex()})
            for index in (0, 4, 5, 6, 37, 38, len(body)-1):
                bad = bytearray(body); bad[index] ^= 1
                self.assertIn('error', self.apple('open', key=key.hex(), tag=channel, envelope=bad.hex()))
                with self.assertRaises((ValueError, InvalidTag)):
                    w.open_envelope(key, channel, bytes(bad))
            self.assertIn('error', self.apple('open', key=key.hex(), tag=channel.upper(), envelope=body.hex()))
            changed_kind = body[:5] + bytes([3 if kind == 1 else 1]) + body[6:]
            self.assertIn('error', self.apple('open', key=key.hex(), tag=channel, envelope=changed_kind.hex()))
            with self.assertRaises(InvalidTag):
                w.open_envelope(key, channel, changed_kind)

    def test_hpke_native_both_directions_for_pairing_and_inbox(self):
        who = w.identity(bytes(range(32)))
        for info, aad in ((b'tandem/v1/inbox', w.aad(2, w.tag(who['uid'], inbox=True))),
                          (b'tandem/v1/pair', b'TANDEM-PAIR-V1' + who['uid'] + who['uka'])):
            plain = b'actual HPKE\x00\xff'
            sealed = w.hpke_seal(who['uka'], plain, info, aad)
            self.assertEqual(self.apple('hpke-open', private=who['agree'].hex(), sealed=sealed.hex(),
                                         info=info.hex(), aad=aad.hex()), {'plaintext': plain.hex()})
            native = self.apple('hpke-seal', public=who['uka'].hex(), plaintext=plain.hex(), info=info.hex(), aad=aad.hex())
            self.assertEqual(w.hpke_open(who['agree'], bytes.fromhex(native['sealed']), info, aad), plain)
            for bad_info, bad_aad in ((info+b'x', aad), (info, aad+b'x')):
                self.assertIn('error', self.apple('hpke-open', private=who['agree'].hex(), sealed=sealed.hex(),
                                                  info=bad_info.hex(), aad=bad_aad.hex()))
                with self.assertRaises(InvalidTag):
                    w.hpke_open(who['agree'], bytes.fromhex(native['sealed']), bad_info, bad_aad)
            bad_key = w.identity(bytes([9])*32)['agree']
            self.assertIn('error', self.apple('hpke-open', private=bad_key.hex(), sealed=sealed.hex(), info=info.hex(), aad=aad.hex()))
            with self.assertRaises(InvalidTag):
                w.hpke_open(bad_key, sealed, info, aad)
            damaged = sealed[:-1] + bytes([sealed[-1] ^ 1])
            self.assertIn('error', self.apple('hpke-open', private=who['agree'].hex(), sealed=damaged.hex(), info=info.hex(), aad=aad.hex()))
            with self.assertRaises(InvalidTag):
                w.hpke_open(who['agree'], damaged, info, aad)

    def test_native_hpke_opens_the_rfc_vector(self):
        v = json.loads((HERE / 'fixtures/hpke-rfc9180.json').read_text())
        e = v['encryptions'][0]
        self.assertEqual(self.apple('hpke-open', private=v['skRm'], sealed=v['enc']+e['ct'],
                                     info=v['info'], aad=e['aad']), {'plaintext': e['pt']})

    def test_native_rejects_noncanonical_signature_scalar(self):
        who = w.identity(bytes(range(32)))
        message = b'policy test'
        sig = ed25519.Ed25519PrivateKey.from_private_bytes(who['sign']).sign(message)
        self.assertEqual(self.apple('verify', public=who['uid'].hex(), signature=sig.hex(), message=message.hex()), {'valid': True})
        bad = sig[:32] + (int.from_bytes(sig[32:], 'little') + w.L).to_bytes(32, 'little')
        self.assertEqual(self.apple('verify', public=who['uid'].hex(), signature=bad.hex(), message=message.hex()), {'valid': False})

    def test_strict_adapter_matches_all_policy_vectors(self):
        vectors = json.loads((HERE / 'fixtures/ed25519-policy.json').read_text())
        for case in vectors['cases']:
            with self.subTest(case=case['name']):
                self.assertEqual(self.apple('verify', public=case['public'], signature=case['signature'],
                                             message=case['message']), {'valid': case['valid']})
        # Check native behavior too, without assuming future OSes preserve its
        # present acceptance of the forbidden identity public key.
        case = next(x for x in vectors['cases'] if x['name'] == 'small-order-0')
        native = self.apple('verify-native', public=case['public'], signature=case['signature'], message=case['message'])
        self.assertIn(native.get('valid'), (True, False))


if __name__ == '__main__':
    unittest.main()
