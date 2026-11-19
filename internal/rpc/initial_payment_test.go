package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
	sdkcontracts "github.com/invisi-labs/invisi-sdk-go/protocol/contracts"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestInitialPaymentCanonicalReceipt(t *testing.T) {
	raw, err := os.ReadFile("../protocol/testdata/unified-receipts.json")
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
			Receipt types.Receipt
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		if c.Name != "credit-initial-payment" {
			continue
		}
		contract, err := sdkcontracts.InvisiCoreMetaData.GetAbi()
		if err != nil {
			t.Fatal(err)
		}
		var creation map[string]any
		var draw map[string]any
		for _, log := range c.Receipt.Logs {
			if log.Address != c.Context.ProtocolAddress || len(log.Topics) == 0 {
				continue
			}
			event, e := contract.EventByID(log.Topics[0])
			if e != nil {
				continue
			}
			m := map[string]any{}
			if e = event.Inputs.NonIndexed().UnpackIntoMap(m, log.Data); e != nil {
				t.Fatal(e)
			}
			indexed := abi.Arguments{}
			for _, v := range event.Inputs {
				if v.Indexed {
					indexed = append(indexed, v)
				}
			}
			if e = abi.ParseTopicsIntoMap(m, indexed, log.Topics[1:]); e != nil {
				t.Fatal(e)
			}
			switch event.Name {
			case "CreditCreated":
				creation = m
			case "CreditDraw":
				draw = m
			}
		}
		if creation == nil || draw == nil {
			t.Fatal("missing creation/payment fixture")
		}
		r := &settlement.Request{Method: "createCredit", Proof: []byte{1}, Creation: settlement.CreateCreditParams{Token: creation["token"].(common.Address), Owner: creation["owner"].(common.Address), RevocationShard: creation["revocationShard"].(uint64), RevocationSlot: creation["revocationSlot"].(uint16), CreditCommitment: creation["creditCommitment"].([32]byte), Deadline: creation["deadline"].(*big.Int), FeeRecipient: draw["feeRecipient"].(common.Address), FeeAmount: draw["feeAmount"].(*big.Int), HasInitialPayment: true, TotalAmount: draw["totalAmount"].(*big.Int), NoteCommitment: draw["noteCommitment"].([32]byte)}}
		r.Deliveries = *abi.ConvertType(creation["deliveries"], new([]settlement.Delivery)).(*[]settlement.Delivery)
		r.Deliveries = append(r.Deliveries, *abi.ConvertType(draw["delivery"], new(settlement.Delivery)).(*settlement.Delivery))
		data, err := settlement.Encode(r)
		if err != nil {
			t.Fatal(err)
		}
		tx := types.NewTx(&types.LegacyTx{To: &c.Context.ProtocolAddress, Data: data})
		receipt := c.Receipt
		receipt.TxHash = tx.Hash()
		for _, log := range receipt.Logs {
			log.TxHash = tx.Hash()
		}
		block := model.Block{Number: receipt.BlockNumber.Uint64(), Timestamp: 1, Hash: receipt.BlockHash}
		decoder, _ := protocol.NewDecoder()
		logs := []types.Log{}
		for _, log := range receipt.Logs {
			if log.Address == c.Context.ProtocolAddress {
				logs = append(logs, *log)
			}
		}
		batch, err := decoder.Decode(block, logs)
		if err != nil {
			t.Fatal(err)
		}
		a := &API{}
		a.Deployment.ChainID = c.Context.ChainID
		a.Deployment.Protocol = c.Context.ProtocolAddress
		ops, err := a.transactionOperationsInBlock(&batch, tx, &receipt)
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) != 1 {
			t.Fatalf("operations=%d", len(ops))
		}
		op := ops[0]
		if op.Kind != "createCredit" || op.UnsupportedReason != "" || len(op.NoteOutputs) != 1 || len(op.CreditOutputs) != 1 || len(op.Funding) != 1 || op.Funding[0].Amount != "7" || op.NoteOutputs[0].Amount != "6" || op.FeeAmount != "1" || len(op.CreditNullifiers) != 0 {
			t.Fatalf("initial payment: %+v", op)
		}
		// An ordinary merchant Note must keep its independent delivery.
		if len(batch.Outputs) != 1 || !bytes.Equal(batch.Outputs[0].Ciphertext, r.Deliveries[2].Ciphertext) || len(batch.CreditStates) != 1 || !bytes.Equal(batch.CreditStates[0].Deliveries[0].Ciphertext, r.Deliveries[0].Ciphertext) {
			t.Fatal("delivery projection mismatch")
		}
		// The new Note and Credit survive restart and disappear atomically on reorg.
		ctx := context.Background()
		dbPath := filepath.Join(t.TempDir(), "initial.sqlite")
		dep := model.Deployment{ChainID: c.Context.ChainID, Protocol: c.Context.ProtocolAddress, FromBlock: block.Number}
		db, e := storage.OpenSQLite(ctx, dbPath, dep)
		if e != nil {
			t.Fatal(e)
		}
		if len(batch.CreatedTrees) == 0 {
			batch.CreatedTrees = []uint64{1}
		}
		if e = db.Apply(ctx, batch); e != nil {
			db.Close()
			t.Fatal(e)
		}
		db.Close()
		db, e = storage.OpenSQLite(ctx, dbPath, dep)
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		head, e := db.Checkpoint(ctx)
		if e != nil || head == nil || head.Hash != block.Hash {
			t.Fatalf("restart checkpoint: %+v %v", head, e)
		}
		note, e := db.Note(ctx, batch.Outputs[0].Commitment, block.Number)
		if e != nil || note == nil || note.Output.LeafIndex != 1 || !bytes.Equal(note.Output.Ciphertext, r.Deliveries[2].Ciphertext) {
			t.Fatalf("restart Note: %+v %v", note, e)
		}
		credit, e := db.CreditState(ctx, batch.CreditStates[0].Commitment, block.Number)
		if e != nil || credit == nil || credit.State.LeafIndex != 0 {
			t.Fatalf("restart Credit: %+v %v", credit, e)
		}
		if e = db.Rollback(ctx, block.Number-1); e != nil {
			t.Fatal(e)
		}
		if note, e = db.Note(ctx, batch.Outputs[0].Commitment, block.Number); e != nil || note != nil {
			t.Fatal("orphan Note survived", e)
		}
		if credit, e = db.CreditState(ctx, batch.CreditStates[0].Commitment, block.Number); e != nil || credit != nil {
			t.Fatal("orphan Credit survived", e)
		}
		if head, e = db.Checkpoint(ctx); e != nil || head != nil {
			t.Fatal("orphan checkpoint survived", e)
		}
		// A different gross debit cannot be attributed to this receipt.
		r.Creation.TotalAmount = big.NewInt(8)
		data, err = settlement.Encode(r)
		if err != nil {
			t.Fatal(err)
		}
		badTx := types.NewTx(&types.LegacyTx{To: &c.Context.ProtocolAddress, Data: data})
		badReceipt := receipt
		badReceipt.TxHash = badTx.Hash()
		for _, log := range badReceipt.Logs {
			log.TxHash = badTx.Hash()
		}
		for i := range batch.Logs {
			batch.Logs[i].TxHash = badTx.Hash()
		}
		for i := range batch.Events {
			batch.Events[i].TransactionHash = badTx.Hash()
		}
		for i := range batch.Outputs {
			batch.Outputs[i].TransactionHash = badTx.Hash()
		}
		for i := range batch.CreditStates {
			batch.CreditStates[i].TransactionHash = badTx.Hash()
		}
		if _, err = a.transactionOperationsInBlock(&batch, badTx, &badReceipt); err == nil {
			t.Fatal("accepted different initial debit")
		}
		return
	}
	t.Fatal("initial payment fixture absent")
}
