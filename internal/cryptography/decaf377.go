package cryptography

import (
	"fmt"
	decafq "github.com/consensys/gnark-crypto/ecc/bls12-377/fr"
	"github.com/consensys/gnark-crypto/ecc/bls12-377/twistededwards"
)

func decafNegative(n *decafq.Element) bool { return n.Bits()[0]&1 != 0 }
func decafDecode(input []byte) (twistededwards.PointAffine, error) {
	var p twistededwards.PointAffine
	if len(input) != 32 || input[31]>>5 != 0 || input[0]&1 != 0 {
		return p, fmt.Errorf("invalid canonical Decaf377 encoding")
	}
	var canonical [32]byte
	for i, v := range input {
		canonical[31-i] = v
	}
	var s, ss, u1, u2, v, tmp, one, twoSU1 decafq.Element
	if err := s.SetBytesCanonical(canonical[:]); err != nil {
		return p, fmt.Errorf("invalid canonical Decaf377 field: %w", err)
	}
	one.SetOne()
	ss.Square(&s)
	u1.Sub(&one, &ss)
	u2.Square(&u1)
	tmp.SetUint64(12084)
	tmp.Mul(&tmp, &ss)
	u2.Sub(&u2, &tmp)
	tmp.Square(&u1)
	tmp.Mul(&tmp, &u2)
	if tmp.IsZero() {
		return p, fmt.Errorf("invalid Decaf377 square root")
	}
	tmp.Inverse(&tmp)
	if v.Sqrt(&tmp) == nil {
		return p, fmt.Errorf("invalid Decaf377 square root")
	}
	twoSU1.Double(&s)
	twoSU1.Mul(&twoSU1, &u1)
	tmp.Mul(&twoSU1, &v)
	if decafNegative(&tmp) {
		v.Neg(&v)
	}
	tmp.Square(&v)
	tmp.Mul(&tmp, &u2)
	p.X.Mul(&twoSU1, &tmp)
	tmp.Add(&one, &ss)
	tmp.Mul(&tmp, &v)
	p.Y.Mul(&tmp, &u1)
	if !p.IsOnCurve() {
		return twistededwards.PointAffine{}, fmt.Errorf("invalid Decaf377 point")
	}
	return p, nil
}
