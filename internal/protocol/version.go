package protocol

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// APIVersion identifies this service's HTTP namespace. Contract VERSION() is a
// separate on-chain value, read from each configured deployment at startup.
const SupportedVersion = "1"
const APIVersion = "v" + SupportedVersion
const APIPrefix = "/" + APIVersion

type VersionCaller interface {
	CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)
}

func ReadVersion(ctx context.Context, caller VersionCaller, address common.Address) (string, error) {
	result, err := caller.CallContract(ctx, ethereum.CallMsg{To: &address, Data: crypto.Keccak256([]byte("VERSION()"))[:4]}, nil)
	if err != nil {
		return "", fmt.Errorf("read Protocol VERSION(): %w", err)
	}
	if len(result) != 32 {
		return "", errors.New("Protocol VERSION() returned an invalid uint256")
	}
	n := new(big.Int).SetBytes(result)
	if n.Sign() == 0 {
		return "", errors.New("Protocol VERSION() must be nonzero")
	}
	return n.String(), nil
}
