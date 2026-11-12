package tandem

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// KDF is HKDF-SHA-256 (§5.2). info strings begin with "tandem/v1/".
func KDF(ikm, salt []byte, info string, n int) []byte {
	k, err := hkdf.Key(sha256.New, ikm, salt, info, n)
	if err != nil {
		panic(err) // only for n > 255*32, which Tandem never asks for
	}
	return k
}

// User holds the keys derived from a user secret (§5.3).
type User struct {
	Secret []byte             // US, 32 bytes
	Sign   ed25519.PrivateKey // UKs
	UID    []byte             // pub(UKs), 32 bytes: the user ID
	Agree  *ecdh.PrivateKey   // UKa
	UKa    []byte             // pub(UKa), 32 bytes
	Home   []byte             // HK
}

// NewUser derives a user's keys from their secret.
func NewUser(secret []byte) (*User, error) {
	if len(secret) != 32 {
		return nil, errors.New("tandem: user secret must be 32 bytes")
	}
	sign := ed25519.NewKeyFromSeed(KDF(secret, nil, "tandem/v1/user-sign", 32))
	agree, err := ecdh.X25519().NewPrivateKey(KDF(secret, nil, "tandem/v1/user-agree", 32))
	if err != nil {
		return nil, err
	}
	return &User{
		Secret: bytes.Clone(secret),
		Sign:   sign,
		UID:    []byte(sign.Public().(ed25519.PublicKey)),
		Agree:  agree,
		UKa:    agree.PublicKey().Bytes(),
		Home:   KDF(secret, nil, "tandem/v1/home", 32),
	}, nil
}

// DisplayUID is the user ID as shown: the first 20 bytes in b32, in groups of 4.
func DisplayUID(uid []byte) string { return groups(B32(uid[:20]), 4) }

// HomeScope is the scopeID of a user's home writes (§6.2).
func HomeScope(uid []byte) []byte { return H([]byte("tandem/v1/home"), uid)[:16] }

// NamedID is the record ID of a named record (§7.3).
func NamedID(name string) []byte { return H([]byte("tandem/v1/named"), []byte(name))[:16] }

// KeyID names the agreement key an inbox message is sealed to (§5.4).
func KeyID(uka []byte) []byte { return H([]byte("tandem/v1/kid"), uka) }

// Commit is a key commitment (§5.7).
func Commit(k []byte) []byte { return H([]byte("tandem/v1/commit"), k) }

// Channel tags (§5.6).

func CollectionTag(k, epoch []byte) string {
	return B32(KDF(k, epoch, "tandem/v1/collection-tag", 20))
}
func HomeTag(hk []byte) string   { return B32(KDF(hk, nil, "tandem/v1/tag", 20)) }
func InboxTag(uid []byte) string { return B32(KDF(uid, nil, "tandem/v1/inbox-tag", 20)) }

// InviteKey is KI, the invite channel's key.
func InviteKey(s []byte) []byte { return KDF(s, nil, "tandem/v1/invite-key", 32) }
func InviteTag(s []byte) string { return B32(KDF(InviteKey(s), nil, "tandem/v1/tag", 20)) }

// InviteSigner is VKs, derived from the invite secret.
func InviteSigner(s []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(KDF(s, nil, "tandem/v1/invite-sign", 32))
}

func PairKey(p []byte) []byte { return KDF(p, nil, "tandem/v1/pair-key", 32) }
func PairTag(p []byte) string { return B32(KDF(p, nil, "tandem/v1/tag", 20)) }

// PairCode is the six digits both devices show (§6.3).
func PairCode(p, ek, n []byte) string {
	h := H([]byte("tandem/v1/pair-code"), p, ek, n)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(h[:4])%1_000_000)
}

// BackupKey writes a user secret for humans (§6.3): b32(US || H(US)[0..3]),
// 56 characters in 14 groups of 4.
func BackupKey(secret []byte) string {
	return groups(B32(append(bytes.Clone(secret), H(secret)[:3]...)), 4)
}

// ParseBackupKey reads a backup key as a person typed it: case and spaces
// don't matter, the checksum does.
func ParseBackupKey(s string) ([]byte, error) {
	s = strings.ToLower(strings.Join(strings.Fields(s), ""))
	b, err := ParseB32(s)
	if err != nil || len(b) != 35 {
		return nil, errors.New("tandem: not a backup key")
	}
	secret := b[:32]
	if !bytes.Equal(H(secret)[:3], b[32:]) {
		return nil, errors.New("tandem: backup key has a typo")
	}
	return secret, nil
}
