package tandem

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// KDF is HKDF-SHA-256. n must be in [0, 8160].
func KDF(ikm, salt []byte, info string, n int) []byte {
	k, err := hkdf.Key(sha256.New, ikm, salt, info, n)
	if err != nil {
		panic(err)
	}
	return k
}

// DeviceKeys are the signing/agreement pair for one keychain. Installations
// sharing a synced keychain reuse the pair and its admission certificate.
// Participant IDs come from registrations, never from these public keys.
type DeviceKeys struct {
	Signing   ed25519.PrivateKey
	Agreement *ecdh.PrivateKey
}

// NewInstallationID is this-installation-only state: never sync it with keys.
// If its durable clock is lost, replace this ID while retaining the keychain keys.
func NewInstallationID() [8]byte { var id [8]byte; rand.Read(id[:]); return id }

func NewDeviceKeys() (*DeviceKeys, error) {
	_, s, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	a, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &DeviceKeys{s, a}, nil
}

// ManagerKeys derives management keys only. It derives no participant data key.
func ManagerKeys(seed []byte) (*DeviceKeys, error) {
	if len(seed) != 32 {
		return nil, errors.New("tandem: management seed must be 32 bytes")
	}
	a, err := ecdh.X25519().NewPrivateKey(KDF(seed, nil, "tandem/v2/manager-agree", 32))
	if err != nil {
		return nil, err
	}
	return &DeviceKeys{ed25519.NewKeyFromSeed(KDF(seed, nil, "tandem/v2/manager-sign", 32)), a}, nil
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
func HomeScope(hub, uid []byte) []byte   { return H([]byte("tandem/v2/home"), hub, uid)[:16] }
func NamedID(name string) []byte         { return H([]byte("tandem/v2/named"), []byte(name))[:16] }
func KeyID(hub, agreement []byte) []byte { return H([]byte("tandem/v2/kid"), hub, agreement) }
func Commit(key []byte) []byte           { return H([]byte("tandem/v2/commit"), key) }
func CollectionTag(key, hub, epoch []byte) string {
	return B32(KDF(key, cat(hub, epoch), "tandem/v2/collection-tag", 20))
}
func HomeTag(key, hub, uid, epoch []byte) string {
	return B32(KDF(key, cat(hub, uid, epoch), "tandem/v2/home-tag", 20))
}
func InboxTag(uid, hub []byte) string     { return B32(KDF(uid, hub, "tandem/v2/inbox-tag", 20)) }
func DirectoryKey(key, hub []byte) []byte { return KDF(key, hub, "tandem/v2/directory-key", 32) }
func DirectoryTag(key, hub []byte) string { return B32(KDF(key, hub, "tandem/v2/directory-tag", 20)) }
