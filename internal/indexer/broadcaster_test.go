package indexer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type broadcasterChain struct {
	*fakeChain
	broadcaster  common.Address
	registryCode []byte
	codeErr      error
}

func (c *broadcasterChain) CodeAt(ctx context.Context, a common.Address, n *big.Int) ([]byte, error) {
	if a == c.broadcaster {
		return c.registryCode, c.codeErr
	}
	return c.fakeChain.CodeAt(ctx, a, n)
}
func TestBroadcasterRuntimePin(t *testing.T) {
	for _, kind := range []string{"valid", "wrong", "empty", "rpc-error"} {
		t.Run(kind, func(t *testing.T) {
			i, c, _, _ := fixture()
			addr := common.HexToAddress("0x5678")
			code := []byte{9, 8, 7}
			i.Deployment.BroadcasterRegistry = &model.ContractDeployment{Address: addr, FromBlock: 1, RuntimeCodeHash: crypto.Keccak256Hash(code)}
			chain := &broadcasterChain{fakeChain: c, broadcaster: addr, registryCode: code}
			i.Chain = chain
			switch kind {
			case "wrong":
				chain.registryCode = []byte{1}
			case "empty":
				chain.registryCode = nil
			case "rpc-error":
				chain.codeErr = errors.New("code unavailable")
			}
			e := i.Verify(context.Background())
			if (e == nil) != (kind == "valid") {
				t.Fatalf("unexpected pin check: %v", e)
			}
		})
	}
}
func TestBroadcasterEarlierStartAndAddressFilter(t *testing.T) {
	i, c, s, _ := fixture()
	addr := common.HexToAddress("0x5678")
	i.Deployment.BroadcasterRegistry = &model.ContractDeployment{Address: addr, FromBlock: 1, RuntimeCodeHash: crypto.Keccak256Hash(c.code)}
	for _, want := range []int{1, 2} {
		if ok, e := i.Step(context.Background()); e != nil || !ok {
			t.Fatalf("step %v %v", ok, e)
		}
		query := c.queries[len(c.queries)-1]
		if len(query.Addresses) != want || query.Addresses[want-1] != addr {
			t.Fatal("wrong source filter")
		}
	}
	if s.applied[0].Block.Number != 1 {
		t.Fatal("lost early history")
	}
	if allowedEmitter(i.Deployment, addr, 0) || !allowedEmitter(i.Deployment, addr, 1) || allowedEmitter(i.Deployment, common.HexToAddress("0x9999"), 2) {
		t.Fatal("incorrect emitter height boundary")
	}
}
func TestIndexerRejectsDuplicateBlockLogIndexes(t *testing.T) {
	i, c, s, d := fixture()
	h := c.headers[2]
	log := types.Log{Address: i.Deployment.Protocol, BlockNumber: 2, BlockHash: h.Hash(), Index: 7}
	c.logs = []types.Log{log, log}
	if _, e := i.Step(context.Background()); e == nil {
		t.Fatal("accepted duplicate log indexes")
	}
	if d.calls != 0 || len(s.applied) != 0 {
		t.Fatal("duplicate logs reached persistence")
	}
}
