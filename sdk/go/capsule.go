package tandem

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/text/unicode/norm"
)

// The admin capsule of §6.7: salt || nonce || ChaCha20-Poly1305(KDF(CK, hubID,
// "tandem/v2/admin-capsule"), nonce, MS, aad), with CK = Argon2id(password,
// salt, m = 1 GiB, t = 3, p = 1) and aad = "TANDEM-ADMIN-CAPSULE-V2" || hubID || salt.

// CapsuleLength is the length of a sealed capsule: salt, nonce, MS and the tag.
const CapsuleLength = 16 + 12 + 32 + 16

// argon2Profile is an Argon2id cost. The protocol accepts exactly capsuleProfile;
// tests use cheaper ones for everything but the vector.
type argon2Profile struct {
	memoryKiB, iterations uint32
	lanes                 uint8
}

var capsuleProfile = argon2Profile{memoryKiB: 1 << 20, iterations: 3, lanes: 1}

var (
	ErrCapsulePassword = errors.New("tandem/capsule: the hub password does not open this capsule")
	ErrCapsuleHub      = errors.New("tandem/capsule: the capsule holds another hub's key")
)

func capsuleErr(what string) error { return errors.New("tandem/capsule: " + what) }

// StretchPassword is CK at the capsule profile: about a second and 1 GiB.
func StretchPassword(password, salt []byte) []byte {
	return stretch(password, salt, capsuleProfile)
}

func stretch(password, salt []byte, p argon2Profile) []byte {
	return argon2.IDKey(password, salt, p.iterations, p.memoryKiB, p.lanes, 32)
}

// passwordBytes is §6.7's hashed form: NFC-normalized UTF-8, nothing trimmed.
func passwordBytes(password string) []byte { return norm.NFC.Bytes([]byte(password)) }

// checkChosenPassword applies §6.7's rule for a chosen password, after
// normalization: at least 12 code points and at most 1,024 bytes.
func checkChosenPassword(password []byte) error {
	if !utf8.Valid(password) {
		return capsuleErr("a hub password is UTF-8")
	}
	if len(password) > 1024 {
		return capsuleErr("a hub password has at most 1,024 bytes")
	}
	if utf8.RuneCount(password) < 12 {
		return capsuleErr("a hub password has at least 12 characters")
	}
	return nil
}

func capsuleAEAD(ck, hub []byte) ([]byte, error) {
	if len(ck) != 32 || len(hub) != 32 {
		return nil, capsuleErr("bad key or hub")
	}
	return KDF(ck, hub, "tandem/v2/admin-capsule", 32), nil
}

func capsuleAAD(hub, salt []byte) []byte { return cat([]byte("TANDEM-ADMIN-CAPSULE-V2"), hub, salt) }

func checkSeed(ms, hub []byte) error {
	k, err := ManagerKeys(ms)
	if err != nil {
		return err
	}
	if !bytes.Equal(k.Signing.Public().(ed25519.PublicKey), hub) {
		return ErrCapsuleHub
	}
	return nil
}

// OpenCapsule opens a capsule with a typed password and returns MS, checked to
// be this hub's seed. The password is NFC-normalized; unlocking checks no
// length (§6.7). Takes about a second and 1 GiB.
func OpenCapsule(password string, hub, capsule []byte) ([]byte, error) {
	return openCapsule(password, hub, capsule, capsuleProfile)
}

func openCapsule(password string, hub, capsule []byte, p argon2Profile) ([]byte, error) {
	if len(hub) != 32 || len(capsule) != CapsuleLength {
		return nil, capsuleErr("not an admin capsule")
	}
	return OpenCapsuleCK(stretch(passwordBytes(password), capsule[:16], p), hub, capsule)
}

// OpenCapsuleCK opens a capsule with an already stretched CK.
func OpenCapsuleCK(ck, hub, capsule []byte) ([]byte, error) {
	if len(capsule) != CapsuleLength {
		return nil, capsuleErr("not an admin capsule")
	}
	key, err := capsuleAEAD(ck, hub)
	if err != nil {
		return nil, err
	}
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	ms, err := a.Open(nil, capsule[16:28], capsule[28:], capsuleAAD(hub, capsule[:16]))
	if err != nil {
		return nil, ErrCapsulePassword
	}
	if err := checkSeed(ms, hub); err != nil {
		return nil, err
	}
	return ms, nil
}

// SealCapsule seals MS under a newly chosen password (after NFC, at least 12
// code points and at most 1,024 bytes), with a fresh salt and nonce.
func SealCapsule(password string, hub, ms []byte) ([]byte, error) {
	return sealCapsule(password, hub, ms, capsuleProfile)
}

func sealCapsule(typed string, hub, ms []byte, p argon2Profile) ([]byte, error) {
	password := passwordBytes(typed)
	if err := checkChosenPassword(password); err != nil {
		return nil, err
	}
	if err := checkSeed(ms, hub); err != nil {
		return nil, err
	}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	rand.Read(salt)
	rand.Read(nonce)
	return SealCapsuleCK(stretch(password, salt, p), hub, ms, salt, nonce)
}

// SealCapsuleCK seals MS under an already stretched CK with the given salt and nonce.
func SealCapsuleCK(ck, hub, ms, salt, nonce []byte) ([]byte, error) {
	if len(ms) != 32 || len(salt) != 16 || len(nonce) != 12 {
		return nil, capsuleErr("bad seed, salt or nonce")
	}
	key, err := capsuleAEAD(ck, hub)
	if err != nil {
		return nil, err
	}
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return a.Seal(cat(salt, nonce), nonce, ms, capsuleAAD(hub, salt)), nil
}
