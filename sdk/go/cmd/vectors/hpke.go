package main

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"

	"golang.org/x/crypto/chacha20poly1305"
)

// A deterministic HPKE sealer for vectors only: RFC 9180 base mode,
// DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305, with the
// ephemeral key given instead of drawn. The SDK seals with crypto/hpke; the
// generator checks that crypto/hpke opens what this produces.

func labeledExtract(suite []byte, salt []byte, label string, ikm []byte) []byte {
	in := append(append(append([]byte("HPKE-v1"), suite...), label...), ikm...)
	prk, _ := hkdf.Extract(sha256.New, in, salt)
	return prk
}

func labeledExpand(suite, prk []byte, label string, info []byte, n int) []byte {
	in := binary.BigEndian.AppendUint16(nil, uint16(n))
	in = append(append(append(append(in, "HPKE-v1"...), suite...), label...), info...)
	out, _ := hkdf.Expand(sha256.New, prk, string(in), n)
	return out
}

func hpkeSealDet(recipient, ephSecret []byte, info string, aad, pt []byte) []byte {
	eph, err := ecdh.X25519().NewPrivateKey(ephSecret)
	must(err)
	pkR, err := ecdh.X25519().NewPublicKey(recipient)
	must(err)
	dh, err := eph.ECDH(pkR)
	must(err)
	enc := eph.PublicKey().Bytes()

	kem := []byte{'K', 'E', 'M', 0x00, 0x20}
	shared := labeledExpand(kem, labeledExtract(kem, nil, "eae_prk", dh), "shared_secret",
		append(append([]byte{}, enc...), recipient...), 32)

	suite := []byte{'H', 'P', 'K', 'E', 0x00, 0x20, 0x00, 0x01, 0x00, 0x03}
	ctx := append([]byte{0x00}, labeledExtract(suite, nil, "psk_id_hash", nil)...)
	ctx = append(ctx, labeledExtract(suite, nil, "info_hash", []byte(info))...)
	secret := labeledExtract(suite, shared, "secret", nil)
	key := labeledExpand(suite, secret, "key", ctx, 32)
	nonce := labeledExpand(suite, secret, "base_nonce", ctx, 12)

	a, err := chacha20poly1305.New(key)
	must(err)
	return a.Seal(append([]byte{}, enc...), nonce, pt, aad)
}
