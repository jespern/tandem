"""The admin capsule of DESIGN.md §6.7, with real cryptography.

The hub's management seed MS lives on the hub, sealed under a key derived from the admin key
AK and the hub ID. Reference test code with disposable keys, not an SDK.
"""
import base64
import os
import unittest

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305

from wire import kdf, pub

PURPOSE = b"TANDEM-ADMIN-CAPSULE-V2"


def manager(ms):
    return ed25519.Ed25519PrivateKey.from_private_bytes(kdf(ms, b"", b"tandem/v2/manager-sign"))


def seal(ak, hub_id, ms, nonce=None):
    nonce = os.urandom(12) if nonce is None else nonce
    key = kdf(ak, hub_id, b"tandem/v2/admin-capsule")
    return nonce + ChaCha20Poly1305(key).encrypt(nonce, ms, PURPOSE + hub_id)


def unlock(ak, hub_id, capsule):
    """Open the capsule and check that it holds this hub's management key."""
    key = kdf(ak, hub_id, b"tandem/v2/admin-capsule")
    ms = ChaCha20Poly1305(key).decrypt(capsule[:12], capsule[12:], PURPOSE + hub_id)
    if pub(manager(ms)) != hub_id:
        raise ValueError("the capsule holds another hub's key")
    return ms


def show(ak):
    """§5.3: 20 bytes, 32 characters of lowercase base32 in 8 groups of 4."""
    text = base64.b32encode(ak).decode().lower()
    return " ".join(text[i:i + 4] for i in range(0, 32, 4))


class AdminCapsule(unittest.TestCase):
    def setUp(self):
        self.ak, self.ms = os.urandom(20), os.urandom(32)
        self.hub_id = pub(manager(self.ms))
        self.capsule = seal(self.ak, self.hub_id, self.ms)

    def test_the_admin_key_opens_the_capsule_on_any_device(self):
        self.assertEqual(unlock(self.ak, self.hub_id, self.capsule), self.ms)
        typed = bytes.fromhex(self.ak.hex())                                   # a device that only has what was typed
        self.assertEqual(unlock(typed, self.hub_id, self.capsule), self.ms)

    def test_a_wrong_or_mistyped_key_fails_without_a_checksum(self):
        for wrong in (os.urandom(20), self.ak[:-1] + bytes([self.ak[-1] ^ 1])):
            with self.assertRaises(InvalidTag):
                unlock(wrong, self.hub_id, self.capsule)

    def test_a_capsule_is_bound_to_its_hub(self):
        other_ms = os.urandom(32)
        other_hub = pub(manager(other_ms))
        with self.assertRaises(InvalidTag):
            unlock(self.ak, other_hub, self.capsule)                          # moved to another hub
        # The same admin key on a capsule that holds another hub's management key is refused.
        foreign = seal(self.ak, self.hub_id, other_ms)
        with self.assertRaises(ValueError):
            unlock(self.ak, self.hub_id, foreign)

    def test_tampering_is_detected(self):
        for i in (0, 11, 12, len(self.capsule) - 1):
            bad = bytearray(self.capsule); bad[i] ^= 1
            with self.assertRaises(InvalidTag):
                unlock(self.ak, self.hub_id, bytes(bad))

    def test_the_key_is_shown_as_eight_groups_of_four(self):
        shown = show(self.ak)
        self.assertEqual(len(shown.split()), 8)
        self.assertTrue(all(len(g) == 4 for g in shown.split()))
        self.assertEqual(base64.b32decode(shown.replace(" ", "").upper()), self.ak)
        self.assertEqual(len(self.ak) * 8, 160)


if __name__ == "__main__":
    unittest.main()
