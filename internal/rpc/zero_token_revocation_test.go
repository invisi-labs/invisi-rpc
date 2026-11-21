package rpc

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/indexer"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
	sdkcontracts "github.com/invisi-labs/invisi-sdk-go/protocol/contracts"
)

// A legitimate fee-free pre-revoke must advance the canonical prefix and remain
// publicly queryable even though there can never have been a zero-token grant.
func TestZeroTokenPreRevokeIndexesAndRemainsTerminal(t *testing.T) {
	ctx := context.Background()
	chain, dep, _ := routerCoreBoundary(t, "direct-erc20", 10, common.Hash{})
	owner := common.HexToAddress("0x123")
	contract, err := sdkcontracts.InvisiCoreMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	event := contract.Events["CreditRevoked"]
	data, err := event.Inputs.NonIndexed().Pack(common.Address{}, new(big.Int))
	if err != nil {
		t.Fatal(err)
	}
	chain.logs = []types.Log{{Address: dep.Protocol, Topics: []common.Hash{event.ID, common.BytesToHash(owner.Bytes()), {}, common.BigToHash(big.NewInt(1))}, Data: data, BlockNumber: 10, BlockHash: chain.block.Hash(), TxHash: chain.tx.Hash()}}
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "zero-token.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	decoder, err := protocol.NewDeploymentDecoder(dep)
	if err != nil {
		t.Fatal(err)
	}
	ix := &indexer.Indexer{Chain: chain, Store: db, Decoder: decoder, Deployment: dep}
	if progress, err := ix.Step(ctx); err != nil || !progress {
		t.Fatalf("pre-revoke stalled indexing: %v %v", progress, err)
	}
	head, err := db.Checkpoint(ctx)
	if err != nil || head == nil || head.Number != 10 || head.Hash != chain.block.Hash() {
		t.Fatalf("checkpoint: %+v %v", head, err)
	}
	api := &API{Store: db, Deployment: dep}
	h, err := HTTPHandler(api)
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, h, "invisi_getCreditStatus", []any{owner, common.Address{}, "1", 10}, "")
	if response.Error != nil {
		t.Fatalf("zero-token status unavailable: %+v", response.Error)
	}
	status, err := api.GetCreditStatus(ctx, owner, common.Address{}, "1", 10)
	if err != nil || status.Status != 2 {
		t.Fatalf("pre-revoke status: %+v %v", status, err)
	}
	events, err := api.GetEvents(ctx, Range{FromBlock: 10, ToBlock: 10, BlockHash: chain.block.Hash()})
	if err != nil || len(events) != 1 || events[0].Name != "CreditRevoked" {
		t.Fatalf("revocation events: %+v %v", events, err)
	}
	if _, err := api.GetCreditStatus(ctx, common.Address{}, common.Address{}, "1", 10); err == nil {
		t.Fatal("accepted zero owner")
	}
	// The indexed disabled ID remains terminal, and failed replay is atomic.
	next := model.Batch{Block: model.Block{Number: 11, ParentHash: head.Hash, Hash: common.HexToHash("11")}, Events: events}
	if err := db.Apply(ctx, next); err == nil {
		t.Fatal("accepted repeated revocation")
	}
	after, err := db.Checkpoint(ctx)
	if err != nil || after.Number != 10 {
		t.Fatalf("invalid replay advanced checkpoint: %+v %v", after, err)
	}
}
