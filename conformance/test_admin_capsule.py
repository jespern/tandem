"""The admin capsule of DESIGN.md §6.7, with real cryptography.

The hub's management seed MS lives on the hub, sealed under a key stretched from the hub
password with Argon2id. Reference test code with disposable keys, not an SDK.
"""
import os
import time
import unicodedata
import unittest

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.argon2 import Argon2id

from wire import kdf, pub

PURPOSE = b"TANDEM-ADMIN-CAPSULE-V2"
PROFILE = dict(memory_cost=1024 * 1024, iterations=8, lanes=4)   # §6.7: 1 GiB, 8 passes, 4 lanes; the only one
FAST = dict(memory_cost=64, iterations=1, lanes=1)                 # for the other tests only; never on the wire


def manager(ms):
    return ed25519.Ed25519PrivateKey.from_private_bytes(kdf(ms, b"", b"tandem/v2/manager-sign"))


def stretch(password, salt, profile):
    text = unicodedata.normalize("NFC", password)
    if len(text) < 12:
        raise ValueError("a hub password has at least 12 characters")
    return Argon2id(salt=salt, length=32, **profile).derive(text.encode("utf-8"))


def seal(password, hub_id, ms, profile=FAST):
    salt, nonce = os.urandom(16), os.urandom(12)
    key = kdf(stretch(password, salt, profile), hub_id, b"tandem/v2/admin-capsule")
    return salt + nonce + ChaCha20Poly1305(key).encrypt(nonce, ms, PURPOSE + hub_id + salt)


def unlock(password, hub_id, capsule, profile=FAST):
    """Open the capsule and check that it holds this hub's management key."""
    salt, nonce, body = capsule[:16], capsule[16:28], capsule[28:]
    key = kdf(stretch(password, salt, profile), hub_id, b"tandem/v2/admin-capsule")
    ms = ChaCha20Poly1305(key).decrypt(nonce, body, PURPOSE + hub_id + salt)
    if pub(manager(ms)) != hub_id:
        raise ValueError("the capsule holds another hub's key")
    return ms


class AdminCapsule(unittest.TestCase):
    def setUp(self):
        self.password = "correct horse battery staple"
        self.ms = os.urandom(32)
        self.hub_id = pub(manager(self.ms))
        self.capsule = seal(self.password, self.hub_id, self.ms)

    def test_the_password_opens_the_capsule_on_any_device(self):
        self.assertEqual(unlock(self.password, self.hub_id, self.capsule), self.ms)
        decomposed = unicodedata.normalize("NFD", "kålrabi og æbler")             # typed on another keyboard
        capsule = seal("kålrabi og æbler", self.hub_id, self.ms)
        self.assertEqual(unlock(decomposed, self.hub_id, capsule), self.ms)

    def test_a_wrong_password_fails(self):
        for wrong in ("correct horse battery stapl", "Correct horse battery staple", " correct horse battery staple"):
            with self.assertRaises(InvalidTag):
                unlock(wrong, self.hub_id, self.capsule)

    def test_short_passwords_are_refused(self):
        with self.assertRaises(ValueError):
            seal("hunter2", self.hub_id, self.ms)

    def test_a_capsule_is_bound_to_its_hub_and_its_salt(self):
        other_hub = pub(manager(os.urandom(32)))
        with self.assertRaises(InvalidTag):
            unlock(self.password, other_hub, self.capsule)
        foreign = seal(self.password, self.hub_id, os.urandom(32))
        with self.assertRaises(ValueError):
            unlock(self.password, self.hub_id, foreign)
        for i in (0, 15, 16, 27, 28, len(self.capsule) - 1):                       # salt, nonce, body
            bad = bytearray(self.capsule); bad[i] ^= 1
            with self.assertRaises(InvalidTag):
                unlock(self.password, self.hub_id, bytes(bad))

    def test_a_capsule_written_with_another_profile_does_not_open(self):
        weak = seal(self.password, self.hub_id, self.ms, profile=FAST)
        with self.assertRaises(InvalidTag):
            unlock(self.password, self.hub_id, weak, profile=PROFILE)

    def test_the_real_profile_round_trips_and_is_slow(self):
        start = time.perf_counter()
        capsule = seal(self.password, self.hub_id, self.ms, profile=PROFILE)
        self.assertGreater(time.perf_counter() - start, 0.3)                      # about 1 s on an Apple M5
        self.assertEqual(unlock(self.password, self.hub_id, capsule, profile=PROFILE), self.ms)


if __name__ == "__main__":
    unittest.main()
