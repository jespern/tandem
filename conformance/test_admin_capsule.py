"""The admin capsule of DESIGN.md §6.7, with real cryptography.

The hub's management seed MS lives on the hub, sealed under a key stretched from the hub
password with Argon2id. Reference test code with disposable keys, not an SDK. A fixed vector
in fixtures/tandem-v2-capsule.json is checked here against `cryptography` and libsodium.
"""
import ctypes
import ctypes.util
import json
import os
from pathlib import Path
import time
import unicodedata
import unittest

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from cryptography.hazmat.primitives.kdf.argon2 import Argon2id

from wire import kdf, pub

HERE = Path(__file__).resolve().parent
PURPOSE = b"TANDEM-ADMIN-CAPSULE-V2"
PROFILE = dict(memory_cost=1024 * 1024, iterations=3, lanes=1)   # §6.7: 1 GiB, 3 passes, 1 lane; the only one
FAST = dict(memory_cost=64, iterations=1, lanes=1)                 # for the other tests only; never on the wire


def manager(ms):
    return ed25519.Ed25519PrivateKey.from_private_bytes(kdf(ms, b"", b"tandem/v2/manager-sign"))


def password_bytes(password):
    return unicodedata.normalize("NFC", password).encode("utf-8")


def check_new_password(password):
    """§6.7: checked when a password is chosen, never when one is entered to unlock."""
    if len(unicodedata.normalize("NFC", password)) < 12:                       # code points
        raise ValueError("a hub password has at least 12 characters")


def stretch(password, salt, profile):
    return Argon2id(salt=salt, length=32, **profile).derive(password_bytes(password))


def seal(password, hub_id, ms, profile=FAST, salt=None, nonce=None):
    check_new_password(password)
    salt, nonce = salt or os.urandom(16), nonce or os.urandom(12)
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


def libsodium():
    for name in (ctypes.util.find_library("sodium"), "/opt/homebrew/lib/libsodium.dylib", "/usr/local/lib/libsodium.dylib"):
        try:
            so = ctypes.CDLL(name)
        except (OSError, TypeError):
            continue
        if so.sodium_init() < 0:
            continue
        so.crypto_pwhash.argtypes = [ctypes.c_char_p, ctypes.c_ulonglong, ctypes.c_char_p, ctypes.c_ulonglong,
                                     ctypes.c_char_p, ctypes.c_ulonglong, ctypes.c_size_t, ctypes.c_int]
        return so
    return None


def sodium_stretch(so, password, salt, profile):
    """libsodium's crypto_pwhash always uses one lane; the profile has one so that it can be used."""
    assert profile["lanes"] == 1
    out, raw = ctypes.create_string_buffer(32), password_bytes(password)
    if so.crypto_pwhash(out, 32, raw, len(raw), salt, profile["iterations"], profile["memory_cost"] * 1024,
                        so.crypto_pwhash_alg_argon2id13()) != 0:
        raise MemoryError("crypto_pwhash failed")
    return out.raw


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
        for wrong in ("correct horse battery stapl", "Correct horse battery staple", " correct horse battery staple", "short"):
            with self.assertRaises(InvalidTag):                                 # never a length error on unlock
                unlock(wrong, self.hub_id, self.capsule)

    def test_short_passwords_are_refused_when_chosen(self):
        with self.assertRaises(ValueError):
            seal("hunter2", self.hub_id, self.ms)

    def test_the_minimum_counts_code_points_after_nfc(self):
        # Sol, 2026-11-13: Swift's String.count gives 4 for this, Python's len 12. The unit is code points.
        coder = "\U0001F469‍\U0001F4BB" * 4                                # 4 grapheme clusters, 12 code points
        capsule = seal(coder, self.hub_id, self.ms)
        self.assertEqual(unlock(coder, self.hub_id, capsule), self.ms)
        with self.assertRaises(ValueError):
            seal("é" * 6, self.hub_id, self.ms)                           # 12 code points as typed, 6 after NFC

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
        self.assertGreater(time.perf_counter() - start, 0.3)                      # about 1.2 s on an Apple M5
        self.assertEqual(unlock(self.password, self.hub_id, capsule, profile=PROFILE), self.ms)


class Vector(unittest.TestCase):
    """The fixed vector another SDK reproduces, at the real profile."""

    @classmethod
    def setUpClass(cls):
        v = json.loads((HERE / "fixtures/tandem-v2-capsule.json").read_text())
        cls.v = {k: (bytes.fromhex(x) if k not in ("password", "profile") else x) for k, x in v.items()}
        p = cls.v["profile"]
        cls.profile = dict(memory_cost=p["m_kib"], iterations=p["t"], lanes=p["p"])

    def test_the_profile_is_the_one_in_the_design(self):
        self.assertEqual(self.profile, PROFILE)

    def test_cryptography_reproduces_the_vector(self):
        v = self.v
        self.assertEqual(stretch(v["password"], v["salt"], PROFILE), v["ck"])
        self.assertEqual(pub(manager(v["ms"])), v["hub_id"])
        self.assertEqual(seal(v["password"], v["hub_id"], v["ms"], PROFILE, v["salt"], v["nonce"]), v["capsule"])
        self.assertEqual(unlock(v["password"], v["hub_id"], v["capsule"], PROFILE), v["ms"])

    def test_libsodium_reproduces_the_vector(self):
        # Sol, 2026-11-13: with four lanes, libsodium's crypto_pwhash couldn't open anyone's capsule.
        so = libsodium()
        if so is None:
            if os.environ.get("TANDEM_REQUIRE_LIBSODIUM") == "1":
                self.fail("libsodium is required but was not found")
            self.skipTest("libsodium not installed")
        self.assertEqual(sodium_stretch(so, self.v["password"], self.v["salt"], PROFILE), self.v["ck"])


if __name__ == "__main__":
    unittest.main()
