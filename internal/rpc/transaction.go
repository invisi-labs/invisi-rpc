package rpc

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

// TransactionReader locates mined transactions and checks their canonical block.
// It never submits transactions or substitutes upstream logs for indexed data.
type TransactionReader interface {
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
}

// TransactionUpdates separates availability from an indexed empty result.
// Outputs and CreditTransitions remain null until Status is indexed.
type TransactionUpdates struct {
	TransactionHash   common.Hash        `json:"transactionHash"`
	Status            string             `json:"status"`
	BlockNumber       *uint64            `json:"blockNumber,omitempty"`
	BlockHash         *common.Hash       `json:"blockHash,omitempty"`
	ReceiptStatus     *uint64            `json:"receiptStatus,omitempty"`
	IndexedHead       *model.Block       `json:"indexedHead"`
	Outputs           []model.NoteOutput `json:"notes"`
	CreditTransitions []model.Event      `json:"creditTransitions"`
}

// transactionUpdates reads one indexed block rather than scanning block ranges.
// not_found means the upstream has no mined receipt (unknown or still pending).
// pending_index also covers a stale indexed fork; callers must retry after catch-up.
func (a *API) transactionUpdates(ctx context.Context, hash common.Hash) (*TransactionUpdates, error) {
	if hash == (common.Hash{}) {
		return nil, errors.New("nonzero transaction hash required")
	}
	if a.Transactions == nil {
		return nil, errors.New("transaction lookup upstream is unavailable")
	}
	receipt, err := a.Transactions.TransactionReceipt(ctx, hash)
	if err != nil && !errors.Is(err, ethereum.NotFound) {
		return nil, err
	}
	result := &TransactionUpdates{TransactionHash: hash, Status: "not_found"}
	result.IndexedHead, err = a.Store.Checkpoint(ctx)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return result, nil
	}
	if receipt.TxHash != hash || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.BlockHash == (common.Hash{}) {
		return nil, errors.New("invalid upstream transaction receipt")
	}
	number := receipt.BlockNumber.Uint64()
	if number < a.Deployment.IndexFromBlock() {
		return nil, errors.New("transaction predates indexed deployment")
	}
	blockHash, receiptStatus := receipt.BlockHash, receipt.Status
	result.BlockNumber, result.BlockHash, result.ReceiptStatus = &number, &blockHash, &receiptStatus
	result.Status = "pending_index"
	canonical := func() error {
		header, err := a.Transactions.HeaderByNumber(ctx, receipt.BlockNumber)
		if err != nil {
			return err
		}
		if header == nil || header.Number == nil || header.Number.Cmp(receipt.BlockNumber) != 0 || header.Hash() != blockHash {
			return errors.New("transaction block is no longer canonical; retry lookup")
		}
		return nil
	}
	if err = canonical(); err != nil {
		return nil, err
	}
	if result.IndexedHead == nil || result.IndexedHead.Number < number {
		return result, nil
	}
	batch, err := a.Store.Batch(ctx, number)
	if err != nil {
		return nil, err
	}
	if batch == nil || batch.Block.Number != number || batch.Block.Hash != blockHash {
		return result, nil
	}
	// Check both before and after constructing the response, as in ranged queries.
	anchor := Range{FromBlock: number, ToBlock: number, BlockHash: blockHash}
	if err = a.checkRange(ctx, anchor); err != nil {
		return nil, err
	}
	result.Outputs = []model.NoteOutput{}
	result.CreditTransitions = []model.Event{}
	for _, output := range batch.Outputs {
		if output.TransactionHash == hash {
			if output.Block != batch.Block {
				return nil, errors.New("indexed output block mismatch")
			}
			result.Outputs = append(result.Outputs, output)
		}
	}
	for _, event := range batch.Events {
		if event.TransactionHash != hash {
			continue
		}
		switch event.Name {
		case "CreditCreated", "CreditDraw", "CreditUpdated", "CreditRevoked":
			if event.Block != batch.Block {
				return nil, errors.New("indexed Credit transition block mismatch")
			}
			result.CreditTransitions = append(result.CreditTransitions, event)
		}
	}
	result.Status = "indexed"
	if err = checkResponseSize(result); err != nil {
		return nil, errors.New("transaction updates exceed response limit; use invisi_getBlock")
	}
	if err = a.checkRange(ctx, anchor); err != nil {
		return nil, err
	}
	if err = canonical(); err != nil {
		return nil, err
	}
	return result, nil
}
