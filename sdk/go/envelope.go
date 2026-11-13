package tandem

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"errors"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	KindRecord  byte = 1
	KindInbox   byte = 2
	KindRequest byte = 3
)

var errEnvelope = errors.New("tandem: invalid envelope or authentication failed")

func envelopeContext(key, hub []byte, tag string) error {
	t, e := ParseB32(tag)
	if len(key) != 32 || len(hub) != 32 || e != nil || len(t) != 20 {
		return errEnvelope
	}
	return nil
}
func AAD(kind byte, hub []byte, tag string) []byte {
	return cat([]byte{'T', 'N', 'D', 'M', 2, kind}, hub, []byte(tag))
}

// Seal uses a fresh random salt. Persist and retry the returned bytes.
func Seal(key, hub []byte, kind byte, tag string, plaintext []byte) ([]byte, error) {
	if (kind != KindRecord && kind != KindRequest) || envelopeContext(key, hub, tag) != nil {
		return nil, errEnvelope
	}
	salt := make([]byte, 32)
	rand.Read(salt)
	a, err := chacha20poly1305.New(KDF(key, salt, "tandem/v2/msg", 32))
	if err != nil {
		return nil, err
	}
	return a.Seal(cat([]byte{'T', 'N', 'D', 'M', 2, kind}, salt), make([]byte, 12), plaintext, AAD(kind, hub, tag)), nil
}
func Open(key, hub []byte, tag string, env []byte) (byte, []byte, error) {
	if envelopeContext(key, hub, tag) != nil || len(env) < 54 || !bytes.Equal(env[:5], []byte{'T', 'N', 'D', 'M', 2}) || (env[5] != KindRecord && env[5] != KindRequest) {
		return 0, nil, errEnvelope
	}
	a, err := chacha20poly1305.New(KDF(key, env[6:38], "tandem/v2/msg", 32))
	if err != nil {
		return 0, nil, err
	}
	pt, err := a.Open(nil, make([]byte, 12), env[38:], AAD(env[5], hub, tag))
	if err != nil {
		return 0, nil, errEnvelope
	}
	return env[5], pt, nil
}

// SealHPKE uses RFC 9180 base mode, suite 0x0020/0x0001/0x0003, enc || ct.
func SealHPKE(public []byte, info string, aad, plaintext []byte) ([]byte, error) {
	pub, e := ecdh.X25519().NewPublicKey(public)
	if e != nil {
		return nil, e
	}
	pk, e := hpke.NewDHKEMPublicKey(pub)
	if e != nil {
		return nil, e
	}
	enc, s, e := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), []byte(info))
	if e != nil {
		return nil, e
	}
	ct, e := s.Seal(aad, plaintext)
	if e != nil {
		return nil, e
	}
	return cat(enc, ct), nil
}
func OpenHPKE(private *ecdh.PrivateKey, info string, aad, sealed []byte) ([]byte, error) {
	if private == nil || len(sealed) < 48 {
		return nil, errEnvelope
	}
	k, e := hpke.NewDHKEMPrivateKey(private)
	if e != nil {
		return nil, e
	}
	r, e := hpke.NewRecipient(sealed[:32], k, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), []byte(info))
	if e != nil {
		return nil, errEnvelope
	}
	pt, e := r.Open(aad, sealed[32:])
	if e != nil {
		return nil, errEnvelope
	}
	return pt, nil
}
func SealInbox(hub, uid, public, plaintext []byte) ([]byte, error) {
	tag := InboxTag(uid, hub)
	if len(uid) != 32 || envelopeContext(public, hub, tag) != nil {
		return nil, errEnvelope
	}
	sealed, e := SealHPKE(public, "tandem/v2/inbox", AAD(KindInbox, hub, tag), plaintext)
	if e != nil {
		return nil, e
	}
	return cat([]byte{'T', 'N', 'D', 'M', 2, KindInbox}, KeyID(hub, public), sealed), nil
}
func OpenInbox(hub, uid []byte, private *ecdh.PrivateKey, env []byte) ([]byte, error) {
	if private == nil || len(hub) != 32 || len(uid) != 32 || len(env) < 86 || !bytes.Equal(env[:6], []byte{'T', 'N', 'D', 'M', 2, KindInbox}) || !bytes.Equal(env[6:38], KeyID(hub, private.PublicKey().Bytes())) {
		return nil, errEnvelope
	}
	return OpenHPKE(private, "tandem/v2/inbox", AAD(KindInbox, hub, InboxTag(uid, hub)), env[38:])
}
