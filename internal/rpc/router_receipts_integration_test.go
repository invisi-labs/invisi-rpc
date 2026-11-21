package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
)

// TestRouterExecutedCoreReceipts accepts the shared Periphery/Provenance harness
// export. The harness records whether Core is real or a boundary test double;
// this consumer verifies RPC projection, not proof validity or Router execution.
func TestRouterExecutedCoreReceipts(t *testing.T) {
	path := os.Getenv("INVISI_ROUTER_RECEIPTS")
	if path == "" {
		t.Skip("set INVISI_ROUTER_RECEIPTS to a shared executed-receipt fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name    string
			Context struct {
				ChainID         string
				ProtocolAddress common.Address
			}
			Header      *types.Header
			Transaction *types.Transaction
			Receipt     types.Receipt
			Expected    struct {
				RootSender, Player, Broadcaster, Router, FeeRecipient common.Address
				FundingAmount                                         string
			}
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("empty executed receipt fixture")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Transaction == nil || c.Header == nil || c.Header.Number == nil || c.Receipt.BlockNumber == nil {
				t.Fatal("transaction, canonical header and receipt block required")
			}
			if c.Receipt.TxHash != c.Transaction.Hash() || c.Receipt.BlockHash != c.Header.Hash() || c.Receipt.BlockNumber.Cmp(c.Header.Number) != 0 || c.Receipt.Status != 1 {
				t.Fatal("executed transaction/receipt/header mismatch")
			}
			chainID, ok := new(big.Int).SetString(c.Context.ChainID, 10)
			if !ok || chainID.Sign() <= 0 {
				t.Fatal("fixture chain ID required")
			}
			sender, err := types.Sender(types.LatestSignerForChainID(chainID), c.Transaction)
			if err != nil || sender != c.Expected.RootSender {
				t.Fatalf("outer sender mismatch: %s %v", sender, err)
			}
			block := model.Block{Number: c.Header.Number.Uint64(), Hash: c.Header.Hash(), ParentHash: c.Header.ParentHash, Timestamp: c.Header.Time}
			dep := model.Deployment{ChainID: c.Context.ChainID, Protocol: c.Context.ProtocolAddress, FromBlock: block.Number}
			decoder, err := protocol.NewDeploymentDecoder(dep)
			if err != nil {
				t.Fatal(err)
			}
			logs := []types.Log{}
			for _, log := range c.Receipt.Logs {
				if log != nil && log.Address == dep.Protocol {
					logs = append(logs, *log)
				}
			}
			batch, err := decoder.Decode(block, logs)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Outputs) == 0 {
				t.Fatal("executed receipt has no Core Note output")
			}
			backend := &transactionBackend{fakeBackend: fakeBackend{head: &block}, batch: &batch}
			api, err := New(backend, dep, "1")
			if err != nil {
				t.Fatal(err)
			}
			api.Transactions = &transactionChain{receipt: &c.Receipt, header: c.Header}
			notes, err := api.GetTransactionNoteUpdates(context.Background(), c.Transaction.Hash())
			if err != nil || notes.Status != "indexed" || !reflect.DeepEqual(notes.Notes, batch.Outputs) {
				t.Fatalf("public executed Note projection: %+v %v", notes, err)
			}
			deposit, fee := false, false
			fundingPayer := c.Expected.Router
			if c.Transaction.To() != nil && *c.Transaction.To() == dep.Protocol {
				fundingPayer = c.Expected.Player
			}
			feeRecipient := c.Expected.FeeRecipient
			if feeRecipient == (common.Address{}) {
				feeRecipient = c.Expected.Broadcaster
			}
			for _, event := range batch.Events {
				if event.TransactionHash != c.Transaction.Hash() {
					t.Fatal("Core event lost outer transaction hash")
				}
				switch event.Name {
				case "PublicDeposit":
					var v struct {
						Payer  common.Address
						Amount string
					}
					if err := json.Unmarshal(event.Data, &v); err != nil {
						t.Fatal(err)
					}
					if v.Payer != fundingPayer || v.Amount != c.Expected.FundingAmount {
						t.Fatalf("Core funding facts: %+v", v)
					}
					deposit = true
				case "BroadcasterFee":
					var v struct {
						Recipient common.Address
						Amount    string
					}
					if err := json.Unmarshal(event.Data, &v); err != nil {
						t.Fatal(err)
					}
					if v.Recipient != feeRecipient {
						t.Fatalf("Core fee recipient: %+v", v)
					}
					fee = true
				}
			}
			if !deposit || !fee {
				t.Fatal("expected Core funding and broadcaster fee facts")
			}
		})
	}
}
