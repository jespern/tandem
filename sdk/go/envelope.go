package tandem

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// Envelopes (§5.4).

const (
	KindRecord  = 1 // record state or inventory fragment
	KindInbox   = 2 // HPKE-sealed inbox message
	KindRequest = 3 // request or notice
)

var magic = []byte("TNDM")

var errEnvelope = errors.New("tandem: envelope doesn't open")

// AAD is "TNDM" || u8(1) || u8(kind) || tag, with the tag as its b32 text.
func AAD(kind byte, tag string) []byte {
	return append([]byte{'T', 'N', 'D', 'M', 1, kind}, tag...)
}

// Seal makes a symmetric envelope (kinds 1 and 3) for the channel with key k
// and tag. salt is 32 random bytes; nil draws them.
func Seal(k []byte, kind byte, tag string, plaintext, salt []byte) ([]byte, error) {
	if kind != KindRecord && kind != KindRequest {
		return nil, errors.New("tandem: not a symmetric kind")
	}
	if salt == nil {
		salt = make([]byte, 32)
		rand.Read(salt)
	}
	if len(salt) != 32 {
		return nil, errors.New("tandem: salt must be 32 bytes")
	}
	aead, err := chacha20poly1305.New(KDF(k, salt, "tandem/v1/msg", 32))
	if err != nil {
		return nil, err
	}
	env := append([]byte{'T', 'N', 'D', 'M', 1, kind}, salt...)
	return aead.Seal(env, make([]byte, 12), plaintext, AAD(kind, tag)), nil
}

// Open opens a symmetric envelope, returning its kind and plaintext.
func Open(k []byte, tag string, env []byte) (byte, []byte, error) {
	if len(env) < 38+16 || !bytes.Equal(env[:4], magic) || env[4] != 1 {
		return 0, nil, errEnvelope
	}
	kind := env[5]
	if kind != KindRecord && kind != KindRequest {
		return 0, nil, errEnvelope
	}
	aead, err := chacha20poly1305.New(KDF(k, env[6:38], "tandem/v1/msg", 32))
	if err != nil {
		return 0, nil, err
	}
	pt, err := aead.Open(nil, make([]byte, 12), env[38:], AAD(kind, tag))
	if err != nil {
		return 0, nil, errEnvelope
	}
	return kind, pt, nil
}

// The HPKE suite of §5.1. Sealed bytes are enc || ct (§5.4).
func hpkeSeal(recipient []byte, info string, ad, plaintext []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(recipient)
	if err != nil {
		return nil, err
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return nil, err
	}
	enc, s, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), []byte(info))
	if err != nil {
		return nil, err
	}
	ct, err := s.Seal(ad, plaintext)
	if err != nil {
		return nil, err
	}
	return append(enc, ct...), nil
}

func hpkeOpen(priv *ecdh.PrivateKey, info string, ad, sealed []byte) ([]byte, error) {
	if len(sealed) < 32+16 {
		return nil, errEnvelope
	}
	k, err := hpke.NewDHKEMPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	r, err := hpke.NewRecipient(sealed[:32], k, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), []byte(info))
	if err != nil {
		return nil, errEnvelope
	}
	pt, err := r.Open(ad, sealed[32:])
	if err != nil {
		return nil, errEnvelope
	}
	return pt, nil
}

// SealInbox makes a kind-2 envelope for the user with this ID and agreement
// key: magic || version || kind || kid || enc || ct.
func SealInbox(uid, uka, plaintext []byte) ([]byte, error) {
	sealed, err := hpkeSeal(uka, "tandem/v1/inbox", AAD(KindInbox, InboxTag(uid)), plaintext)
	if err != nil {
		return nil, err
	}
	env := append([]byte{'T', 'N', 'D', 'M', 1, KindInbox}, KeyID(uka)...)
	return append(env, sealed...), nil
}

// OpenInbox opens a kind-2 envelope sealed to u.
func OpenInbox(u *User, env []byte) ([]byte, error) {
	if len(env) < 38 || !bytes.Equal(env[:4], magic) || env[4] != 1 || env[5] != KindInbox ||
		!bytes.Equal(env[6:38], KeyID(u.UKa)) {
		return nil, errEnvelope
	}
	return hpkeOpen(u.Agree, "tandem/v1/inbox", AAD(KindInbox, InboxTag(u.UID)), env[38:])
}

// SealPairing seals the user secret to a new device's one-time key (§6.3).
func SealPairing(uid, ek, secret []byte) ([]byte, error) {
	return hpkeSeal(ek, "tandem/v1/pair", PairAAD(uid, ek), secret)
}

// OpenPairing opens the pairing reply with the one-time private key.
func OpenPairing(eph *ecdh.PrivateKey, uid, sealed []byte) ([]byte, error) {
	return hpkeOpen(eph, "tandem/v1/pair", PairAAD(uid, eph.PublicKey().Bytes()), sealed)
}

// PairAAD is the HPKE aad of the pairing reply.
func PairAAD(uid, ek []byte) []byte {
	return append(append([]byte("TANDEM-PAIR-V1"), uid...), ek...)
}
