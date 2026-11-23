package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

// SettlementReader augments receipt lookups with read-only bounded block discovery.
// Trace support is deliberately separate: an indirect reverted call has no discoverable log.
type SettlementReader interface {
	TransactionReader
	TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error)
	BlockByNumber(context.Context, *big.Int) (*types.Block, error)
	CodeAt(context.Context, common.Address, *big.Int) ([]byte, error)
	ChainID(context.Context) (*big.Int, error)
}
type OperationAnchor struct {
	BlockNumber string      `json:"blockNumber"`
	BlockHash   common.Hash `json:"blockHash"`
}
type OperationsQuery struct {
	FromBlock *uint64 `json:"fromBlock,omitempty"`
	ToBlock   *uint64 `json:"toBlock,omitempty"`
	Limit     int     `json:"limit"`
	Cursor    string  `json:"cursor,omitempty"`
}

func (q *OperationsQuery) UnmarshalJSON(raw []byte) error {
	type wire OperationsQuery
	var v wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid query JSON")
	}
	*q = OperationsQuery(v)
	return nil
}

type OperationsPage struct {
	Operations               []settlement.Operation `json:"operations"`
	Snapshot                 OperationAnchor        `json:"snapshot"`
	IndexedHead              OperationAnchor        `json:"indexedHead"`
	NextCursor               string                 `json:"nextCursor,omitempty"`
	IndirectFailureDiscovery string                 `json:"indirectFailureDiscovery"`
}
type settlementCursor struct {
	Chain    string         `json:"chain"`
	Protocol common.Address `json:"protocol"`
	Snapshot uint64         `json:"snapshot"`
	Hash     common.Hash    `json:"hash"`
	From     uint64         `json:"from"`
	Next     uint64         `json:"next"`
	Offset   int            `json:"offset"`
	TxOffset int            `json:"txOffset"`
}
type TransactionOperations struct {
	TransactionHash   common.Hash            `json:"transactionHash"`
	Status            string                 `json:"status"`
	ReceiptStatus     *uint64                `json:"receiptStatus,omitempty"`
	Operations        []settlement.Operation `json:"operations"`
	IndexedHead       *model.Block           `json:"indexedHead"`
	Block             *model.Block           `json:"block,omitempty"`
	UnsupportedReason string                 `json:"unsupportedReason,omitempty"`
}

func operationAnchor(b *model.Block) OperationAnchor {
	return OperationAnchor{strconv.FormatUint(b.Number, 10), b.Hash}
}
func (a *API) settlementReader() (SettlementReader, error) {
	r, ok := a.Transactions.(SettlementReader)
	if !ok {
		return nil, errors.New("canonical settlement upstream unavailable")
	}
	return r, nil
}
func (a *API) canonicalBlock(ctx context.Context, r SettlementReader, n uint64) (*model.Batch, *types.Block, error) {
	indexed, e := a.Store.Batch(ctx, n)
	if e != nil {
		return nil, nil, e
	}
	if indexed == nil {
		return nil, nil, errors.New("settlement range is not indexed")
	}
	block, e := r.BlockByNumber(ctx, new(big.Int).SetUint64(n))
	if e != nil {
		return nil, nil, e
	}
	if block == nil || block.NumberU64() != n || block.Hash() != indexed.Block.Hash || block.ParentHash() != indexed.Block.ParentHash || block.Time() != indexed.Block.Timestamp {
		return nil, nil, errors.New("settlement block is not canonical")
	}
	chain, e := r.ChainID(ctx)
	if e != nil || chain == nil || chain.String() != a.Deployment.ChainID {
		return nil, nil, errors.New("settlement chain identity mismatch")
	}
	code, e := r.CodeAt(ctx, a.Deployment.Protocol, block.Number())
	if e != nil {
		return nil, nil, e
	}
	if crypto.Keccak256Hash(code) != a.Deployment.RuntimeCodeHash {
		return nil, nil, errors.New("settlement deployment code mismatch")
	}
	return indexed, block, nil
}

// GetSettlementOperations returns ascending logical operations under a replayable canonical snapshot.
// Each request reads at most 16 blocks and 256 receipts; continuation is explicit even for empty pages.
func (a *API) GetSettlementOperations(ctx context.Context, q OperationsQuery) (*OperationsPage, error) {
	if q.Limit < 1 || q.Limit > 100 {
		return nil, errors.New("limit must be 1..100")
	}
	r, e := a.settlementReader()
	if e != nil {
		return nil, e
	}
	head, e := a.Store.Checkpoint(ctx)
	if e != nil {
		return nil, e
	}
	if head == nil {
		return nil, errors.New("settlement index unavailable")
	}
	c := settlementCursor{Chain: a.Deployment.ChainID, Protocol: a.Deployment.Protocol, Snapshot: head.Number, Hash: head.Hash, From: a.Deployment.FromBlock, Next: a.Deployment.FromBlock}
	if q.Cursor != "" {
		if len(q.Cursor) > 2048 {
			return nil, errors.New("invalid settlement cursor")
		}
		raw, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil {
			return nil, e
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Chain != a.Deployment.ChainID || c.Protocol != a.Deployment.Protocol || c.From < a.Deployment.FromBlock || c.Next < c.From || c.Next > c.Snapshot || c.Snapshot > head.Number || c.Offset < 0 || c.Offset > 5 || c.TxOffset < 0 {
			return nil, errors.New("invalid settlement cursor")
		}
		if q.FromBlock != nil || q.ToBlock != nil {
			return nil, errors.New("cursor cannot be combined with block bounds")
		}
	} else {
		if q.FromBlock != nil {
			c.From = *q.FromBlock
			c.Next = c.From
		}
		if q.ToBlock != nil {
			c.Snapshot = *q.ToBlock
		}
		if c.From < a.Deployment.FromBlock || c.From > c.Snapshot || c.Snapshot > head.Number {
			return nil, errors.New("settlement range unavailable")
		}
		b, e := a.Store.Block(ctx, c.Snapshot)
		if e != nil {
			return nil, e
		}
		if b == nil {
			return nil, errors.New("snapshot not indexed")
		}
		c.Hash = b.Hash
	}
	snap, e := a.at(ctx, c.Snapshot)
	if e != nil || snap.Hash != c.Hash {
		return nil, errors.New("settlement snapshot reorganized")
	}
	out := &OperationsPage{Operations: []settlement.Operation{}, Snapshot: operationAnchor(snap), IndexedHead: operationAnchor(head), IndirectFailureDiscovery: "unsupported_without_traces"}
	var native []canonicalBlockData
	nativeIndex := 0
	done := false
	receipts := 0
replay:
	for blocks := 0; blocks < 16; blocks++ {
		var batch *model.Batch
		var block *types.Block
		if ranges, ok := r.(settlementRangeReader); ok && nativeIndex == len(native) {
			// Fetch only the next bounded group. Dense continuations read one
			// block, and never depend on unrelated later blocks being available.
			last := min(c.Next+3, c.Snapshot)
			last = min(last, c.Next+uint64(15-blocks))
			if c.TxOffset != 0 || c.Offset != 0 {
				last = c.Next
			}
			native, e = ranges.canonicalRange(ctx, a.Deployment.Protocol, c.Next, last)
			if e != nil && !errors.Is(e, errSettlementBatchUnavailable) {
				return nil, e
			}
			if errors.Is(e, errSettlementBatchUnavailable) {
				native = nil
			}
			nativeIndex = 0
		}
		if native != nil {
			if nativeIndex >= len(native) {
				return nil, errors.New("incomplete native settlement range")
			}
			data := native[nativeIndex]
			nativeIndex++
			batch, e = a.Store.Batch(ctx, c.Next)
			block = data.Block
			if e == nil && (batch == nil || block == nil || block.NumberU64() != c.Next || block.Hash() != batch.Block.Hash || block.ParentHash() != batch.Block.ParentHash || block.Time() != batch.Block.Timestamp) {
				e = errors.New("settlement block is not canonical")
			}
			if e == nil && (data.ChainID == nil || data.ChainID.String() != a.Deployment.ChainID) {
				e = errors.New("settlement chain identity mismatch")
			}
			if e == nil && crypto.Keccak256Hash(data.Code) != a.Deployment.RuntimeCodeHash {
				e = errors.New("settlement deployment code mismatch")
			}
		} else {
			batch, block, e = a.canonicalBlock(ctx, r, c.Next)
		}
		if e != nil {
			return nil, e
		}
		transactions := block.Transactions()
		if c.TxOffset > len(transactions) || c.TxOffset == len(transactions) && (c.TxOffset != 0 || c.Offset != 0) {
			return nil, errors.New("invalid settlement cursor offset")
		}
		hashes := map[common.Hash]bool{}
		for _, l := range batch.Logs {
			if l.Address == a.Deployment.Protocol {
				hashes[l.TxHash] = true
			}
		}
		for c.TxOffset < len(transactions) {
			tx := transactions[c.TxOffset]
			relevant := hashes[tx.Hash()] || tx.To() != nil && *tx.To() == a.Deployment.Protocol
			if !relevant {
				if c.Offset != 0 {
					return nil, errors.New("invalid settlement cursor offset")
				}
				c.TxOffset++
				continue
			}
			if receipts == 256 || len(out.Operations) == q.Limit {
				break replay
			}
			receipts++
			receipt, e := r.TransactionReceipt(ctx, tx.Hash())
			if e != nil {
				return nil, e
			}
			ops, e := a.transactionOperationsInBlock(batch, tx, receipt)
			if e != nil {
				return nil, e
			}
			if c.Offset > len(ops) || c.Offset != 0 && c.Offset == len(ops) {
				return nil, errors.New("invalid settlement cursor offset")
			}
			for c.Offset < len(ops) && len(out.Operations) < q.Limit {
				out.Operations = append(out.Operations, ops[c.Offset])
				c.Offset++
			}
			if c.Offset < len(ops) {
				break replay
			}
			c.Offset = 0
			c.TxOffset++
		}
		c.TxOffset = 0
		if c.Next == c.Snapshot {
			done = true
			break
		}
		c.Next++
		if len(out.Operations) == q.Limit || receipts == 256 {
			break
		}
	}
	if !done {
		raw, _ := json.Marshal(c)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if e = a.unchanged(ctx, snap); e != nil {
		return nil, e
	}
	h, e := r.HeaderByNumber(ctx, new(big.Int).SetUint64(c.Snapshot))
	if e != nil || h == nil || h.Hash() != c.Hash {
		return nil, errors.New("settlement snapshot is no longer canonical")
	}
	return out, nil
}

// GetProtocolTransactions shares the operation cursor and includes direct reverted protocol calls.
func (a *API) GetProtocolTransactions(ctx context.Context, q OperationsQuery) (*OperationsPage, error) {
	return a.GetSettlementOperations(ctx, q)
}
func (a *API) GetTransactionOperations(ctx context.Context, hash common.Hash) (*TransactionOperations, error) {
	if hash == (common.Hash{}) {
		return nil, errors.New("nonzero transaction hash required")
	}
	r, e := a.settlementReader()
	if e != nil {
		return nil, e
	}
	head, e := a.Store.Checkpoint(ctx)
	if e != nil {
		return nil, e
	}
	out := &TransactionOperations{TransactionHash: hash, Status: "not_found", Operations: []settlement.Operation{}, IndexedHead: head}
	receipt, e := r.TransactionReceipt(ctx, hash)
	if errors.Is(e, ethereum.NotFound) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	if receipt == nil {
		return out, nil
	}
	if receipt.TxHash != hash || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() {
		return nil, errors.New("invalid settlement receipt")
	}
	out.ReceiptStatus = &receipt.Status
	header, err := r.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil || header == nil || header.Hash() != receipt.BlockHash {
		return nil, errors.New("transaction receipt is no longer canonical")
	}

	if head == nil || receipt.BlockNumber.Uint64() > head.Number {
		out.Status = "pending_index"
		return out, nil
	}
	batch, block, e := a.canonicalBlock(ctx, r, receipt.BlockNumber.Uint64())
	if e != nil {
		return nil, e
	}
	out.Block = &batch.Block
	if int(receipt.TransactionIndex) >= len(block.Transactions()) || block.Transactions()[receipt.TransactionIndex].Hash() != hash {
		return nil, errors.New("receipt transaction position mismatch")
	}
	tx, pending, e := r.TransactionByHash(ctx, hash)
	if e != nil {
		return nil, e
	}
	if pending || tx == nil || tx.Hash() != hash {
		return nil, errors.New("inconsistent settlement transaction")
	}
	out.Operations, e = a.transactionOperationsInBlock(batch, tx, receipt)
	if e != nil {
		return nil, e
	}
	out.Status = "indexed"
	if receipt.Status == types.ReceiptStatusFailed {
		out.Status = "failed"
	}
	if tx.To() == nil || *tx.To() != a.Deployment.Protocol {
		out.UnsupportedReason = "indirect_call_attribution_requires_traces"
	}
	if e = a.unchanged(ctx, &batch.Block); e != nil {
		return nil, e
	}
	return out, nil
}
