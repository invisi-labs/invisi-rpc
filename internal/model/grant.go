package model

import (
	"errors"
	"math/big"
)

// ValidateGrantID requires the canonical wire representation of a positive uint128.
func ValidateGrantID(id string) error {
	n, ok := new(big.Int).SetString(id, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 128 || n.String() != id {
		return errors.New("canonical positive uint128 grantId required")
	}
	return nil
}
