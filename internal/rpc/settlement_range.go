package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

type canonicalBlockData struct {
	Block   *types.Block
	ChainID *big.Int
	Code    []byte
}
type settlementRangeReader interface {
	canonicalRange(context.Context, common.Address, uint64, uint64) ([]canonicalBlockData, error)
}

// SettlementRPCReader preserves ethclient's ordinary methods and bounds native
// full-block batches to four blocks (12 calls), avoiding unbounded batch bodies.
type SettlementRPCReader struct {
	*ethclient.Client
	retryAfter atomic.Int64
}

func NewSettlementReader(client *ethclient.Client) *SettlementRPCReader {
	return &SettlementRPCReader{Client: client}
}

func (r *SettlementRPCReader) canonicalRange(ctx context.Context, address common.Address, from, to uint64) ([]canonicalBlockData, error) {
	if to < from || to-from >= 16 {
		return nil, errors.New("invalid settlement native range")
	}
	if time.Now().UnixNano() < r.retryAfter.Load() {
		return nil, errSettlementBatchUnavailable
	}
	out := make([]canonicalBlockData, 0, to-from+1)
	for start := from; start <= to; {
		end := min(start+3, to)
		n := int(end - start + 1)
		blocks := make([]json.RawMessage, n)
		codes := make([]hexutil.Bytes, n)
		chains := make([]hexutil.Big, n)
		calls := make([]gethrpc.BatchElem, 0, n*3)
		for i := 0; i < n; i++ {
			tag := hexutil.EncodeUint64(start + uint64(i))
			calls = append(calls,
				gethrpc.BatchElem{Method: "eth_getBlockByNumber", Args: []any{tag, true}, Result: &blocks[i]},
				gethrpc.BatchElem{Method: "eth_chainId", Result: &chains[i]},
				gethrpc.BatchElem{Method: "eth_getCode", Args: []any{address, tag}, Result: &codes[i]})
		}
		if e := r.Client.Client().BatchCallContext(ctx, calls); e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Match the existing indexer's compatibility behavior for unavailable
			// batch transports. Malformed/missing individual results never fall back.
			r.retryAfter.Store(time.Now().Add(time.Minute).UnixNano())
			return nil, errSettlementBatchUnavailable
		}
		// A standards-compliant batch-limit rejection is an array carrying
		// -32600 on one member and missing responses for the rest. It is a
		// compatibility failure, unlike an unexplained missing/bad result.
		for _, call := range calls {
			var rejected gethrpc.Error
			if errors.As(call.Error, &rejected) && rejected.ErrorCode() == -32600 {
				r.retryAfter.Store(time.Now().Add(time.Minute).UnixNano())
				return nil, errSettlementBatchUnavailable
			}
		}
		for _, call := range calls {
			if call.Error != nil {
				return nil, call.Error
			}
		}
		for i := 0; i < n; i++ {
			block, e := decodeSettlementBlock(blocks[i])
			if e != nil {
				return nil, e
			}
			if block.NumberU64() != start+uint64(i) {
				return nil, errors.New("wrong native settlement block height")
			}
			// Uncle bodies require additional native requests; retain ethclient's
			// original decoding for those blocks, then check the same declared hash.
			var body struct {
				Uncles []common.Hash `json:"uncles"`
			}
			json.Unmarshal(blocks[i], &body)
			if len(body.Uncles) > 0 {
				verified := block
				replacement, err := r.BlockByNumber(ctx, verified.Number())
				if err != nil {
					return nil, err
				}
				if replacement == nil || replacement.Hash() != verified.Hash() || types.CalcUncleHash(replacement.Uncles()) != verified.UncleHash() {
					return nil, errors.New("native settlement uncle body mismatch")
				}
				// Only use the fetched uncle headers. Keep the transaction and
				// withdrawal bodies whose roots we already verified above.
				block = types.NewBlockWithHeader(verified.Header()).WithBody(types.Body{Transactions: verified.Transactions(), Uncles: replacement.Uncles(), Withdrawals: verified.Withdrawals()})
			}
			out = append(out, canonicalBlockData{block, new(big.Int).Set((*big.Int)(&chains[i])), append([]byte(nil), codes[i]...)})
		}
		start = end + 1
	}
	return out, nil
}

var errSettlementBatchUnavailable = errors.New("native settlement batch unavailable")

func decodeSettlementBlock(raw json.RawMessage) (*types.Block, error) {
	var head *types.Header
	if e := json.Unmarshal(raw, &head); e != nil {
		return nil, e
	}
	if head == nil || head.Number == nil || !head.Number.IsUint64() {
		return nil, errors.New("missing native settlement block")
	}
	var body struct {
		Hash         *common.Hash         `json:"hash"`
		Transactions []*types.Transaction `json:"transactions"`
		Uncles       []common.Hash        `json:"uncles"`
		Withdrawals  []*types.Withdrawal  `json:"withdrawals"`
	}
	if e := json.Unmarshal(raw, &body); e != nil {
		return nil, e
	}
	if body.Hash == nil || *body.Hash != head.Hash() {
		return nil, errors.New("native settlement header hash mismatch")
	}
	if (head.UncleHash == types.EmptyUncleHash) != (len(body.Uncles) == 0) {
		return nil, errors.New("native settlement uncle body mismatch")
	}
	for _, tx := range body.Transactions {
		if tx == nil {
			return nil, errors.New("missing native settlement transaction")
		}
	}
	if types.DeriveSha(types.Transactions(body.Transactions), trie.NewStackTrie(nil)) != head.TxHash {
		return nil, errors.New("native settlement transaction root mismatch")
	}
	if head.WithdrawalsHash != nil {
		for _, w := range body.Withdrawals {
			if w == nil {
				return nil, errors.New("missing native withdrawal")
			}
		}
		if types.DeriveSha(types.Withdrawals(body.Withdrawals), trie.NewStackTrie(nil)) != *head.WithdrawalsHash {
			return nil, errors.New("native withdrawal root mismatch")
		}
	} else if len(body.Withdrawals) > 0 {
		return nil, fmt.Errorf("unexpected native withdrawals")
	}
	return types.NewBlockWithHeader(head).WithBody(types.Body{Transactions: body.Transactions, Withdrawals: body.Withdrawals}), nil
}
