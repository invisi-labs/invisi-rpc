package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
)

var ErrBatchUnavailable = errors.New("batch header transport unavailable")

// RPCChain retains all ethclient operations and adds bounded header batch reads.
type RPCChain struct{ *ethclient.Client }

func NewRPCChain(c *ethclient.Client) *RPCChain { return &RPCChain{c} }
func (c *RPCChain) Headers(ctx context.Context, from, to uint64) ([]*types.Header, error) {
	if to < from || to-from >= 128 {
		return nil, errors.New("invalid header range")
	}
	raw := make([]json.RawMessage, int(to-from+1))
	calls := make([]gethrpc.BatchElem, len(raw))
	for n := range calls {
		calls[n] = gethrpc.BatchElem{Method: "eth_getBlockByNumber", Args: []any{fmt.Sprintf("0x%x", from+uint64(n)), false}, Result: &raw[n]}
	}
	if e := c.Client.Client().BatchCallContext(ctx, calls); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrBatchUnavailable
	}
	out := make([]*types.Header, len(raw))
	for n, call := range calls {
		if call.Error != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrBatchUnavailable
		}
		var declared struct {
			Hash *common.Hash `json:"hash"`
		}
		var h types.Header
		if json.Unmarshal(raw[n], &declared) != nil || json.Unmarshal(raw[n], &h) != nil || declared.Hash == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Cmp(new(big.Int).SetUint64(from+uint64(n))) != 0 || h.Hash() != *declared.Hash {
			return nil, errors.New("invalid batched canonical header")
		}
		out[n] = &h
	}
	return out, nil
}
