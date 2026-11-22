package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

func TestSettlementCreditCanonicalUint128GrantRefs(t *testing.T) {
	for _, kind := range []string{"creditDraw", "updateCredit"} {
		t.Run(kind, func(t *testing.T) { testSettlementCreditCanonicalUint128GrantRefs(t, kind) })
	}
}

func testSettlementCreditCanonicalUint128GrantRefs(t *testing.T, kind string) {
	api, chain, store := settlementFixture(t)
	file := "testdata/credit-draw-two-owners.json"
	if kind == "updateCredit" {
		file = "testdata/credit-update.json"
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Transaction *types.Transaction
		Receipt     *types.Receipt
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	chain.tx, chain.receipt = fixture.Transaction, fixture.Receipt
	chain.block = chain.block.WithBody(types.Body{Transactions: []*types.Transaction{chain.tx}})
	api.Deployment.Protocol = *chain.tx.To()
	decoder, err := protocol.NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	rebuild := func() {
		t.Helper()
		receipt := chain.receipt
		receipt.TxHash, receipt.BlockHash, receipt.BlockNumber, receipt.TransactionIndex = chain.tx.Hash(), chain.block.Hash(), chain.block.Number(), 0
		logs := []types.Log{}
		for _, log := range receipt.Logs {
			log.TxHash, log.BlockHash, log.BlockNumber, log.TxIndex = receipt.TxHash, receipt.BlockHash, receipt.BlockNumber.Uint64(), 0
			if log.Address == api.Deployment.Protocol {
				logs = append(logs, *log)
			}
		}
		batch, err := decoder.Decode(store.batch.Block, logs)
		if err != nil {
			t.Fatal(err)
		}
		batch.BindBlockContext()
		store.batch = &batch
	}
	rebuild()
	page, err := api.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 1 || page.NextCursor != "" {
		t.Fatalf("unexpected page: %+v", page)
	}
	op := page.Operations[0]
	if op.Kind != kind || !op.Success || op.UnsupportedReason != "" {
		t.Fatalf("unsupported operation: %+v", op)
	}
	if kind == "creditDraw" {
		if len(op.NoteOutputs) != 1 || len(op.CreditOutputs) != 2 || len(op.CreditNullifiers) != 2 || len(op.GrantRefs) != 2 || len(op.Funding) != 2 {
			t.Fatalf("incomplete draw projection: %+v", op)
		}
		wantIDs := []string{"279396061989274430300683995475915352704", "199529509191770114066320263993201729974"}
		for i, ref := range op.GrantRefs {
			if ref.GrantID != wantIDs[i] || ref.PayerIndex != big.NewInt(int64(i)).String() {
				t.Fatalf("lost uint128 grant ref: %+v", ref)
			}
		}
	} else if len(op.NoteOutputs) != 0 || len(op.CreditOutputs) != 1 || len(op.CreditNullifiers) != 1 || len(op.Funding) != 0 {
		t.Fatalf("incomplete update projection: %+v", op)
	}
	txResult, err := api.GetTransactionOperations(context.Background(), chain.tx.Hash())
	if err != nil || txResult.Status != "indexed" || len(txResult.Operations) != 1 {
		t.Fatalf("transaction projection: %+v, %v", txResult, err)
	}

	// Receipt authentication must still reject a different grant in the calldata.
	request, err := settlement.Decode(chain.tx.Data())
	if err != nil {
		t.Fatal(err)
	}
	grantID := request.Update.GrantID
	if kind == "creditDraw" {
		grantID = request.Draw.GrantRefs[1].GrantID
	}
	grantID.Add(grantID, big.NewInt(1))
	data, err := settlement.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	chain.tx = types.NewTx(&types.LegacyTx{To: chain.tx.To(), Data: data})
	chain.block = chain.block.WithBody(types.Body{Transactions: []*types.Transaction{chain.tx}})
	rebuild()
	if _, err = api.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100}); err == nil {
		t.Fatal("accepted a grant ref that disagrees with canonical receipt")
	}
}
