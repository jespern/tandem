package tandem

import (
	"bytes"
	"math/big"
	"testing"
)

// Edwards25519 in affine coordinates with math/big: slow, obviously correct,
// and used only to check the small-order list in sig.go.

var (
	bigP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	bigD = func() *big.Int { // d = -121665 / 121666
		n := new(big.Int).Neg(big.NewInt(121665))
		return n.Mod(n.Mul(n, new(big.Int).ModInverse(big.NewInt(121666), bigP)), bigP)
	}()
)

type pt struct{ x, y *big.Int }

func decode(t *testing.T, enc []byte) pt {
	t.Helper()
	le := bytes.Clone(enc)
	sign := le[31] >> 7
	le[31] &= 0x7f
	for i, j := 0, 31; i < j; i, j = i+1, j-1 {
		le[i], le[j] = le[j], le[i]
	}
	y := new(big.Int).SetBytes(le)
	y2 := new(big.Int).Mul(y, y)
	num := new(big.Int).Sub(y2, big.NewInt(1))
	den := new(big.Int).Add(new(big.Int).Mul(bigD, y2), big.NewInt(1))
	x2 := new(big.Int).Mul(num, new(big.Int).ModInverse(den.Mod(den, bigP), bigP))
	x := new(big.Int).ModSqrt(x2.Mod(x2, bigP), bigP)
	if x == nil {
		t.Fatalf("%x is not on the curve", enc)
	}
	if x.Bit(0) != uint(sign) {
		x.Sub(bigP, x)
	}
	return pt{x, y}
}

func add(a, b pt) pt {
	m := func(xs ...*big.Int) *big.Int {
		r := big.NewInt(1)
		for _, x := range xs {
			r.Mul(r, x).Mod(r, bigP)
		}
		return r
	}
	t := m(bigD, a.x, b.x, a.y, b.y)
	inv := func(v *big.Int) *big.Int { return new(big.Int).ModInverse(new(big.Int).Mod(v, bigP), bigP) }
	x := new(big.Int).Add(m(a.x, b.y), m(a.y, b.x))
	y := new(big.Int).Add(m(a.y, b.y), m(a.x, b.x)) // a = -1
	x.Mul(x, inv(new(big.Int).Add(big.NewInt(1), t))).Mod(x, bigP)
	y.Mul(y, inv(new(big.Int).Sub(big.NewInt(1), t))).Mod(y, bigP)
	return pt{x, y}
}

func TestSmallOrderListIsTheTorsionSubgroup(t *testing.T) {
	seen := map[string]bool{}
	for _, enc := range smallOrder {
		if !canonicalPoint(enc) {
			t.Errorf("%x is not canonical", enc)
		}
		p := decode(t, enc)
		q := add(add(add(p, p), add(p, p)), add(add(p, p), add(p, p))) // [8]P
		if q.x.Sign() != 0 || q.y.Cmp(big.NewInt(1)) != 0 {
			t.Errorf("%x: [8]P is not the identity", enc)
		}
		seen[p.x.String()+","+p.y.String()] = true
	}
	// The torsion subgroup of Curve25519's Edwards form has exactly 8 points.
	if len(seen) != 8 {
		t.Fatalf("%d distinct small-order points, want 8", len(seen))
	}
}

func TestCanonicalPoint(t *testing.T) {
	for _, enc := range smallOrder {
		if ValidPublicKey(enc) {
			t.Errorf("small-order key %x accepted", enc)
		}
	}
	u, _ := NewUser(bytes.Repeat([]byte{7}, 32))
	if !ValidPublicKey(u.UID) {
		t.Fatal("an ordinary key was rejected")
	}
}
