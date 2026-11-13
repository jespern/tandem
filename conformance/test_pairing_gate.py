"""Primitive-composition probes for DEVICE-MANAGEMENT.md §5.

Reference test code, not a complete enrollment implementation. Certificate,
current-registry, expiry and revocation validation are supplied by the caller
and checked separately in model/test_management_model.py. Uses disposable keys.
"""
from dataclasses import dataclass, replace
import os
import unittest

from cryptography.exceptions import InvalidSignature, InvalidTag
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305

from wire import hpke_open, hpke_seal, kdf, lp, pub, sha, strict_verify


@dataclass(frozen=True)
class Package:
    token: bytes
    context: bytes
    payload: bytes  # nonce || AEAD ciphertext
    gate: bytes     # HPKE to manager agreement key, containing only share A
    invite_public: bytes
    signature: bytes

    def signature_input(self):
        return (b"TANDEM-PAIR-DELEGATION-HUBMGMT1" + self.context + self.token
                + self.invite_public + sha(self.payload) + sha(self.gate))

    @property
    def id(self):
        return sha(self.signature_input())


@dataclass(frozen=True)
class Claim:
    signing: bytes
    agreement: bytes
    label: str
    invite_signature: bytes
    possession_signature: bytes

    def signature_input(self, package):
        return (b"TANDEM-PAIR-CLAIM-HUBMGMT1" + package.id + self.signing
                + self.agreement + lp(self.label.encode("utf-8")))


def _pair_key(link_share, approval_share, token):
    return kdf(link_share + approval_share, token, b"tandem/hubmgmt1/pair-package")


def _stage(phone, manager_public, context, bootstrap):
    link_share, approval_share, token, nonce = (os.urandom(n) for n in (32, 32, 16, 12))
    invite = ed25519.Ed25519PrivateKey.from_private_bytes(
        kdf(link_share, token, b"tandem/hubmgmt1/pair-sign"))
    payload = nonce + ChaCha20Poly1305(_pair_key(link_share, approval_share, token)).encrypt(
        nonce, bootstrap, context)
    gate = hpke_seal(manager_public, approval_share, b"tandem/hubmgmt1/pair-gate", context + sha(payload))
    package = Package(token, context, payload, gate, pub(invite), b"")
    return link_share, replace(package, signature=phone.sign(package.signature_input()))


def _claim(package, link_share, signing, agreement, label="Laptop"):
    invite = ed25519.Ed25519PrivateKey.from_private_bytes(
        kdf(link_share, package.token, b"tandem/hubmgmt1/pair-sign"))
    claim = Claim(pub(signing), pub(agreement), label, b"", b"")
    data = claim.signature_input(package)
    return replace(claim, invite_signature=invite.sign(data),
                   possession_signature=signing.sign(b"TANDEM-PAIR-POP-HUBMGMT1" + data))


def _approve(package, claim, manager_private, expected_phone, expected_context, certificate_id):
    if (package.context != expected_context
            or not strict_verify(expected_phone, package.signature, package.signature_input())):
        raise InvalidSignature
    data = claim.signature_input(package)
    if (not strict_verify(package.invite_public, claim.invite_signature, data)
            or not strict_verify(claim.signing, claim.possession_signature,
                                 b"TANDEM-PAIR-POP-HUBMGMT1" + data)):
        raise InvalidSignature
    share = hpke_open(manager_private, package.gate, b"tandem/hubmgmt1/pair-gate",
                      package.context + sha(package.payload))
    binding = package.context + package.id + certificate_id + claim.signing + claim.agreement
    return hpke_seal(claim.agreement, share, b"tandem/hubmgmt1/pair-release", binding)


def _finish(package, link_share, release, target_private, signing_public, certificate_id):
    target = pub(x25519.X25519PrivateKey.from_private_bytes(target_private))
    binding = package.context + package.id + certificate_id + signing_public + target
    share = hpke_open(target_private, release, b"tandem/hubmgmt1/pair-release", binding)
    return ChaCha20Poly1305(_pair_key(link_share, share, package.token)).decrypt(
        package.payload[:12], package.payload[12:], package.context)


class PairingGate(unittest.TestCase):
    def setUp(self):
        self.phone = ed25519.Ed25519PrivateKey.generate()
        self.manager = os.urandom(32)
        manager_public = pub(x25519.X25519PrivateKey.from_private_bytes(self.manager))
        # Fixed-width hub/root/member/source/expiry/epoch fixture, not a wire schema.
        self.context = os.urandom(32 * 5) + (100).to_bytes(8, "big")
        self.bootstrap = os.urandom(32)
        self.link, self.package = _stage(self.phone, manager_public, self.context, self.bootstrap)
        self.signing = ed25519.Ed25519PrivateKey.generate()
        self.agreement = x25519.X25519PrivateKey.generate()
        self.private = self.agreement.private_bytes_raw()
        self.claim = _claim(self.package, self.link, self.signing, self.agreement)
        self.certificate = os.urandom(32)

    def approve(self, package=None, claim=None, context=None):
        return _approve(package or self.package, claim or self.claim, self.manager,
                        pub(self.phone), self.context if context is None else context,
                        self.certificate)

    def test_link_alone_does_not_open_private_bootstrap(self):
        key = _pair_key(self.link, bytes(32), self.package.token)
        with self.assertRaises(InvalidTag):
            ChaCha20Poly1305(key).decrypt(self.package.payload[:12], self.package.payload[12:], self.context)

    def test_admin_share_without_link_does_not_open_private_bootstrap(self):
        share = hpke_open(self.manager, self.package.gate, b"tandem/hubmgmt1/pair-gate",
                          self.context + sha(self.package.payload))
        with self.assertRaises(InvalidTag):
            ChaCha20Poly1305(_pair_key(bytes(32), share, self.package.token)).decrypt(
                self.package.payload[:12], self.package.payload[12:], self.context)

    def test_approved_laptop_opens_staged_package_without_phone_online(self):
        release = self.approve()
        self.assertEqual(_finish(self.package, self.link, release, self.private,
                                 pub(self.signing), self.certificate), self.bootstrap)

    def test_list_created_after_pairing_syncs_through_shared_home(self):
        laptop_home = _finish(self.package, self.link, self.approve(), self.private,
                              pub(self.signing), self.certificate)
        # A later list is created, after the enrollment ceremony has finished.
        new_list_key, nonce = os.urandom(32), os.urandom(12)
        aad = self.context + b"home-membership-update"
        update = ChaCha20Poly1305(self.bootstrap).encrypt(nonce, new_list_key, aad)
        self.assertEqual(ChaCha20Poly1305(laptop_home).decrypt(nonce, update, aad), new_list_key)

    def test_home_rekey_keeps_laptop_access_without_old_phone_key(self):
        next_home, next_epoch = os.urandom(32), os.urandom(32)
        # Current device membership/policy is checked in the symbolic model.
        # Rekey boxes are sent only to the surviving device's agreement key.
        binding = self.context + next_epoch + self.certificate
        box = hpke_seal(pub(self.agreement), next_home, b"tandem/hubmgmt1/home-box", binding)
        laptop_home = hpke_open(self.private, box, b"tandem/hubmgmt1/home-box", binding)
        new_list_key, nonce = os.urandom(32), os.urandom(12)
        update = ChaCha20Poly1305(next_home).encrypt(nonce, new_list_key, binding)
        self.assertEqual(ChaCha20Poly1305(laptop_home).decrypt(nonce, update, binding), new_list_key)
        with self.assertRaises(InvalidTag):
            ChaCha20Poly1305(self.bootstrap).decrypt(nonce, update, binding)

    def test_claim_target_cannot_be_substituted_without_invite_signature(self):
        bad = replace(self.claim, agreement=pub(x25519.X25519PrivateKey.generate()))
        with self.assertRaises(InvalidSignature):
            self.approve(claim=bad)

    def test_claim_requires_target_signing_key_possession(self):
        with self.assertRaises(InvalidSignature):
            self.approve(claim=replace(self.claim, possession_signature=bytes(64)))

    def test_wrong_link_secret_cannot_create_a_valid_claim(self):
        claim = _claim(self.package, os.urandom(32), self.signing, self.agreement)
        with self.assertRaises(InvalidSignature):
            self.approve(claim=claim)

    def test_release_is_bound_to_exact_laptop_and_certificate(self):
        release = self.approve()
        for key, certificate in ((os.urandom(32), self.certificate), (self.private, os.urandom(32))):
            with self.assertRaises((InvalidTag, ValueError)):
                _finish(self.package, self.link, release, key, pub(self.signing), certificate)

    def test_modified_package_or_wrong_pinned_context_fails(self):
        changed = replace(self.package, payload=self.package.payload[:-1] + bytes([self.package.payload[-1] ^ 1]))
        with self.assertRaises(InvalidSignature):
            self.approve(package=changed)
        with self.assertRaises(InvalidSignature):
            self.approve(context=self.context + b"other-hub")

    def test_bearer_can_make_a_different_claim_but_each_requires_admin_release(self):
        other_signing = ed25519.Ed25519PrivateKey.generate()
        other_agreement = x25519.X25519PrivateKey.generate()
        other_claim = _claim(self.package, self.link, other_signing, other_agreement)
        release = self.approve(claim=other_claim)
        with self.assertRaises(InvalidTag):
            _finish(self.package, self.link, release, self.private, pub(self.signing), self.certificate)
        self.assertEqual(_finish(self.package, self.link, release, other_agreement.private_bytes_raw(),
                                 pub(other_signing), self.certificate), self.bootstrap)


if __name__ == "__main__":
    unittest.main()
