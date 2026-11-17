package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

type transactionChain struct {
	receipt     *types.Receipt
	err         error
	header      *types.Header
	headerReads int
	reorg       bool
}

func (c *transactionChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return c.receipt, c.err
}
func (c *transactionChain) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	c.headerReads++
	if c.reorg && c.headerReads > 1 {
		h := *c.header
		h.Time++
		return &h, nil
	}
	return c.header, nil
}

type transactionBackend struct {
	fakeBackend
	batch      *model.Batch
	batchReads int
}

func (b *transactionBackend) Batch(context.Context, uint64) (*model.Batch, error) {
	b.batchReads++
	return b.batch, nil
}
func transactionFixture(t *testing.T) (*API, *transactionBackend, *transactionChain, common.Hash) {
	t.Helper()
	header := &types.Header{Number: big.NewInt(10), Time: 123}
	block := model.Block{Number: 10, Hash: header.Hash(), Timestamp: 123}
	tx := common.HexToHash("0x1234")
	store := &transactionBackend{fakeBackend: fakeBackend{head: &block}, batch: &model.Batch{Block: block}}
	chain := &transactionChain{receipt: &types.Receipt{TxHash: tx, BlockNumber: big.NewInt(10), BlockHash: block.Hash, Status: 1}, header: header}
	api, err := New(store, model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("0x1234"), FromBlock: 8}, "1")
	if err != nil {
		t.Fatal(err)
	}
	api.Transactions = chain
	return api, store, chain, tx
}
func TestTransactionUpdatesFilterAndHTTP(t *testing.T) {
	api, store, _, tx := transactionFixture(t)
	other := common.HexToHash("0x99")
	store.batch.Outputs = []model.NoteOutput{{TransactionHash: other}, {TransactionHash: tx, LogIndex: 2, Block: store.batch.Block}}
	store.batch.Events = []model.Event{{TransactionHash: tx, Name: "Unrelated"}, {TransactionHash: other, Name: "CreditDraw"}, {TransactionHash: tx, Name: "CreditDraw", LogIndex: 3, Block: store.batch.Block}}
	h, err := HTTPHandler(api)
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getTransactionNoteUpdates", []any{tx.Hex()}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var decoded TransactionUpdates
	if err := json.Unmarshal(r.Result, &decoded); err != nil || decoded.Status != "indexed" || len(decoded.Outputs) != 1 {
		t.Fatalf("invalid wire response: %s (%v)", r.Result, err)
	}
	result, err := api.transactionUpdates(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "indexed" || len(result.Outputs) != 1 || len(result.CreditTransitions) != 1 || result.TransactionHash != tx || result.BlockNumber == nil || *result.BlockNumber != 10 {
		t.Fatalf("unexpected updates: %+v", result)
	}
	if store.outputReads != 0 || store.eventReads != 0 {
		t.Fatal("performed range queries")
	}
	for _, bad := range []any{"0x1234", "not-a-hash", common.Hash{}.Hex()} {
		if call(t, h, "invisi_getTransactionNoteUpdates", []any{bad}, "").Error == nil {
			t.Fatal("accepted invalid transaction hash")
		}
	}
}
func TestTransactionUpdatesStates(t *testing.T) {
	for _, state := range []string{"not_found", "pending_index", "indexed", "reorg_index", "no_checkpoint", "missing_block", "reverted"} {
		t.Run(state, func(t *testing.T) {
			api, store, chain, tx := transactionFixture(t)
			expected := state
			switch state {
			case "not_found":
				chain.receipt = nil
				chain.err = ethereum.NotFound
			case "pending_index":
				store.head.Number = 9
				store.batch = nil
			case "reorg_index":
				store.batch.Block.Hash = common.HexToHash("0xff")
				expected = "pending_index"
			case "missing_block":
				store.batch = nil
				expected = "pending_index"
			case "reverted":
				chain.receipt.Status = 0
				expected = "indexed"
			case "no_checkpoint":
				store.head = nil
				expected = "pending_index"
			}
			result, err := api.transactionUpdates(context.Background(), tx)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != expected {
				t.Fatalf("status %s", result.Status)
			}
			if expected == "indexed" && (result.Outputs == nil || result.CreditTransitions == nil) {
				t.Fatal("indexed empty arrays must be non-null")
			}
			if expected != "indexed" && (result.Outputs != nil || result.CreditTransitions != nil) {
				t.Fatal("unindexed result claims empty complete data")
			}
		})
	}
}
func TestTransactionUpdatesRejectUntrustedOrFailedReads(t *testing.T) {
	for _, failure := range []string{"upstream", "wrong_hash", "invalid_height", "before_deployment", "canonical", "reorg_during_read", "index_reorg", "missing_reader"} {
		t.Run(failure, func(t *testing.T) {
			api, store, chain, tx := transactionFixture(t)
			switch failure {
			case "upstream":
				chain.err = errors.New("upstream unavailable")
			case "wrong_hash":
				chain.receipt.TxHash = common.Hash{}
			case "invalid_height":
				chain.receipt.BlockNumber = new(big.Int).Lsh(big.NewInt(1), 80)
			case "before_deployment":
				chain.receipt.BlockNumber = big.NewInt(7)
			case "canonical":
				chain.header.Time++
			case "reorg_during_read":
				chain.reorg = true
			case "index_reorg":
				store.reorgAfter = 1
			case "missing_reader":
				api.Transactions = nil
			}
			if _, err := api.transactionUpdates(context.Background(), tx); err == nil {
				t.Fatal("accepted inconsistent transaction query")
			}
		})
	}
}

func TestTransactionUpdatesRejectOversizedResponse(t *testing.T) {
	api, store, _, hash := transactionFixture(t)
	store.batch.Outputs = make([]model.NoteOutput, 1100)
	for i := range store.batch.Outputs {
		store.batch.Outputs[i] = model.NoteOutput{TransactionHash: hash, Block: store.batch.Block, Ciphertext: bytes.Repeat([]byte{1}, 4096)}
	}
	if result, err := api.transactionUpdates(context.Background(), hash); err == nil || result != nil {
		t.Fatal("oversized transaction response was accepted")
	}
}
