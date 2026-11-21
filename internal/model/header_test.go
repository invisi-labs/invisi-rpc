package model

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestValidateCompleteHeaderBeforePublication(t *testing.T) {
	zero := uint64(0)
	root := common.Hash{1}
	h := &types.Header{Number: big.NewInt(10), Difficulty: big.NewInt(1), BaseFee: big.NewInt(0), WithdrawalsHash: &root, BlobGasUsed: &zero, ExcessBlobGas: &zero, ParentBeaconRoot: &root, RequestsHash: &root, SlotNumber: &zero}
	batch := func(h *types.Header) Batch {
		return Batch{Block: Block{Number: 10, Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}, Header: h}
	}
	if e := batch(h).ValidateHeader(); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*types.Header){func(h *types.Header) { h.Difficulty = nil }, func(h *types.Header) { h.BaseFee = nil }, func(h *types.Header) { h.WithdrawalsHash = nil }, func(h *types.Header) { h.BlobGasUsed = nil }, func(h *types.Header) { h.ExcessBlobGas = nil }, func(h *types.Header) { h.ParentBeaconRoot = nil }, func(h *types.Header) { h.RequestsHash = nil }} {
		bad := types.CopyHeader(h)
		mutate(bad)
		if e := batch(bad).ValidateHeader(); e == nil {
			t.Fatal("published incomplete typed header")
		}
	}
	raw, e := json.Marshal(batch(h))
	if e != nil {
		t.Fatal(e)
	}
	var restored Batch
	if e = json.Unmarshal(raw, &restored); e != nil || restored.Header.Hash() != h.Hash() {
		t.Fatal("rich header changed", e)
	}
}
