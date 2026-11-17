package indexer

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

type RootCaller interface {
	CallContractAtHash(context.Context, ethereum.CallMsg, common.Hash) ([]byte, error)
}

// RootValidator checks the contract's recorded batch root at the exact indexed block.
func RootValidator(client RootCaller, address common.Address) func(context.Context, model.Root) error {
	return func(ctx context.Context, r model.Root) error {
		data := make([]byte, 68)
		copy(data[:4], crypto.Keccak256([]byte("rootHistory(uint256,bytes32)"))[:4])
		binary.BigEndian.PutUint64(data[28:36], r.TreeID)
		copy(data[36:], r.Root[:])
		result, err := client.CallContractAtHash(ctx, ethereum.CallMsg{To: &address, Data: data}, r.Block.Hash)
		if err != nil {
			return err
		}
		if len(result) != 32 || new(big.Int).SetBytes(result).Cmp(big.NewInt(1)) != 0 {
			return fmt.Errorf("tree %d root %s not recorded by contract at %s", r.TreeID, r.Root, r.Block.Hash)
		}
		return nil
	}
}
