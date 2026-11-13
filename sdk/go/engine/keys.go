package engine

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"

	tandem "github.com/jespern/tandem/sdk/go"
)

// A keychain's keys are the app's to keep (§5.3): the engine never stores them. These give
// them one 64-byte form, the Ed25519 seed then the X25519 private key, for whatever secret
// store the platform has, and a plain file for when there is none.

// MarshalDeviceKeys is the 64-byte form of a keychain's keys. It is secret.
func MarshalDeviceKeys(k *tandem.DeviceKeys) []byte {
	return append(bytes.Clone(k.Signing.Seed()), k.Agreement.Bytes()...)
}

// ParseDeviceKeys reads MarshalDeviceKeys's form.
func ParseDeviceKeys(b []byte) (*tandem.DeviceKeys, error) {
	if len(b) != 64 {
		return nil, errors.New("tandem/engine: device keys are 64 bytes")
	}
	a, err := ecdh.X25519().NewPrivateKey(b[32:])
	if err != nil {
		return nil, err
	}
	return &tandem.DeviceKeys{Signing: ed25519.NewKeyFromSeed(b[:32]), Agreement: a}, nil
}

// LoadOrCreateDeviceKeys reads a keychain's keys from a file readable only by its owner,
// making them the first time. A copy of the file on another installation is the same
// keychain, as a synced keychain is (§6.3); keep it out of backups that leave the device
// unless that is what is wanted.
func LoadOrCreateDeviceKeys(path string) (*tandem.DeviceKeys, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return ParseDeviceKeys(b)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k, err := tandem.NewDeviceKeys()
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keys-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(MarshalDeviceKeys(k)); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// Link, not rename: of two installations racing, the first one's keys win and both use them.
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	b, err = os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseDeviceKeys(b)
}
