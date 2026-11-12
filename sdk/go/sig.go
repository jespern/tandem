package tandem

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
)

// Strict Ed25519 verification (§5.5): S below the group order, canonical A
// and R, A not of small order, and the plain equation [S]B = R + [k]A.
//
// Go's ed25519.Verify already rejects S >= L, uses the plain equation, and
// compares R by its bytes against a canonical encoding (so a non-canonical R
// never matches). It accepts non-canonical encodings of A and small-order A,
// which VerifyStrict rejects first.

// smallOrder holds the canonical encodings of the eight points of order
// dividing 8. Every point has one canonical encoding, so after the canonical
// check this list is complete (checked in sig_test.go).
var smallOrder = func() [][]byte {
	var out [][]byte
	for _, h := range []string{
		"0100000000000000000000000000000000000000000000000000000000000000", // order 1
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // order 2
		"0000000000000000000000000000000000000000000000000000000000000000", // order 4
		"0000000000000000000000000000000000000000000000000000000000000080", // order 4
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85", // order 8
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", // order 8
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa", // order 8
	} {
		b, _ := hex.DecodeString(h)
		out = append(out, b)
	}
	return out
}()

// p = 2^255 - 19, little-endian.
var fieldP = func() []byte {
	b := bytes.Repeat([]byte{0xff}, 32)
	b[0] = 0xed
	b[31] = 0x7f
	return b
}()

// canonicalPoint reports whether enc is the one encoding of whatever point it
// names: y < p, and no sign bit on x = 0 (y = 1 or y = p - 1).
func canonicalPoint(enc []byte) bool {
	y := bytes.Clone(enc)
	sign := y[31] >> 7
	y[31] &= 0x7f
	for i := 31; i >= 0; i-- { // y < p, compared little-endian from the top
		if y[i] != fieldP[i] {
			if y[i] > fieldP[i] {
				return false
			}
			break
		}
		if i == 0 {
			return false // y == p
		}
	}
	if sign == 1 {
		one := make([]byte, 32)
		one[0] = 1
		pm1 := bytes.Clone(fieldP)
		pm1[0] = 0xec
		if bytes.Equal(y, one) || bytes.Equal(y, pm1) {
			return false
		}
	}
	return true
}

// ValidPublicKey reports whether pub may be anyone's signing key.
func ValidPublicKey(pub []byte) bool {
	if len(pub) != ed25519.PublicKeySize || !canonicalPoint(pub) {
		return false
	}
	for _, s := range smallOrder {
		if bytes.Equal(pub, s) {
			return false
		}
	}
	return true
}

// VerifyStrict checks an Ed25519 signature by Tandem's rules.
func VerifyStrict(pub, msg, sig []byte) bool {
	return len(sig) == ed25519.SignatureSize && ValidPublicKey(pub) &&
		ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}
