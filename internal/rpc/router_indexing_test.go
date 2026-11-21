package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/indexer"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
	sdkcontracts "github.com/invisi-labs/invisi-sdk-go/protocol/contracts"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

// These are Core-boundary fixtures, not executions of Router contracts or proofs.
// Outer calldata is deliberately opaque: the indexer must never decode it.
type routerIndexChain struct {
	*settlementChain
	logs     []types.Log
	filters  []ethereum.FilterQuery
	previous *types.Header
}

func (c *routerIndexChain) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	if n != nil && c.previous != nil && n.Cmp(c.previous.Number) == 0 {
		return c.previous, nil
	}
	return c.block.Header(), nil
}

func (c *routerIndexChain) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	c.filters = append(c.filters, q)
	return c.logs, nil
}

func routerCoreBoundary(t *testing.T, name string, number uint64, parent common.Hash) (*routerIndexChain, model.Deployment, settlement.Request) {
	t.Helper()
	api, original, _ := settlementFixture(t)
	request, err := settlement.Decode(original.tx.Data())
	if err != nil {
		t.Fatal(err)
	}
	router := common.HexToAddress("0x7001")
	player := common.HexToAddress("0x7002")
	broadcaster := common.HexToAddress("0x7003")
	request.Params.Sources = nil
	request.Params.Nullifiers = nil
	request.Params.NoteCommitments = [][32]byte{common.BigToHash(new(big.Int).SetUint64(number * 2)), common.BigToHash(new(big.Int).SetUint64(number*2 + 1))}
	request.Deliveries = append(request.Deliveries, request.Deliveries[0])
	request.Params.FeeRecipient = broadcaster
	request.Params.FeeAmount = big.NewInt(1)
	request.Params.WithdrawalAmount = new(big.Int)
	request.Params.WithdrawalAddress = common.Address{}
	request.Funding.Payer = router
	request.Funding.Amount = big.NewInt(10)
	target := router
	value := new(big.Int)
	switch name {
	case "historical-direct-core":
		target = api.Deployment.Protocol
		request.Funding.Payer = player
	case "direct-native":
		value.SetInt64(10)
	case "sponsored-native-player":
		target = player
	}
	calldata := []byte{0xca, 0xfe, 0xba, 0xbe}
	if name == "historical-direct-core" {
		calldata, err = settlement.Encode(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Distinct signed outer senders model the user and broadcaster. Player/7702
	// authorization execution is covered by the shared integration fixture, not here.
	keyBytes := make([]byte, 32)
	keyBytes[31] = 1
	if name == "relayed-erc20" || name == "sponsored-native-player" {
		keyBytes[31] = 2
	}
	key, err := crypto.ToECDSA(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: number, To: &target, Value: value, Gas: 1000000, GasPrice: big.NewInt(1), Data: calldata}), types.LatestSignerForChainID(big.NewInt(56)), key)
	if err != nil {
		t.Fatal(err)
	}
	block := types.NewBlockWithHeader(&types.Header{Number: new(big.Int).SetUint64(number), ParentHash: parent, Time: 100 + number}).WithBody(types.Body{Transactions: []*types.Transaction{tx}})
	contract, err := sdkcontracts.InvisiCoreMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	logs := []types.Log{}
	emit := func(name string, topics []common.Hash, values ...any) {
		t.Helper()
		event := contract.Events[name]
		data, e := event.Inputs.NonIndexed().Pack(values...)
		if e != nil {
			t.Fatal(e)
		}
		if data == nil {
			data = []byte{}
		} // Match ethclient JSON decoding of empty event data.
		logs = append(logs, types.Log{Address: api.Deployment.Protocol, Topics: append([]common.Hash{event.ID}, topics...), Data: data, BlockNumber: number, BlockHash: block.Hash(), TxHash: tx.Hash(), Index: uint(len(logs))})
	}
	if number == 10 {
		emit("TreeCreated", []common.Hash{common.BigToHash(big.NewInt(1))})
	}
	emit("CommitmentsInserted", []common.Hash{common.BigToHash(big.NewInt(1))}, uint32((number-10)*2), request.Params.NoteCommitments)
	emitUnifiedNote(t, emit, *request, api.Deployment.Protocol, uint32((number-10)*2))
	emit("BroadcasterFee", []common.Hash{common.BytesToHash(request.Params.Token.Bytes()), common.BytesToHash(broadcaster.Bytes())}, big.NewInt(1))
	receipt := &types.Receipt{TxHash: tx.Hash(), BlockNumber: block.Number(), BlockHash: block.Hash(), Status: 1}
	for i := range logs {
		receipt.Logs = append(receipt.Logs, &logs[i])
	}
	// This is the transfer into Core, after any upstream Router funding hop.
	receipt.Logs = append(receipt.Logs, &types.Log{Address: request.Params.Token, Topics: []common.Hash{transferTopic, common.BytesToHash(request.Funding.Payer.Bytes()), common.BytesToHash(api.Deployment.Protocol.Bytes())}, Data: common.LeftPadBytes(request.Funding.Amount.Bytes(), 32), BlockNumber: number, BlockHash: block.Hash(), TxHash: tx.Hash(), Index: uint(len(logs))})
	dep := api.Deployment
	return &routerIndexChain{settlementChain: &settlementChain{block: block, tx: tx, receipt: receipt, code: original.code}, logs: logs}, dep, *request
}

func assertRouterCoreProjection(t *testing.T, api *API, chain *routerIndexChain, request settlement.Request) {
	t.Helper()
	ctx := context.Background()
	notes, err := api.GetTransactionNoteUpdates(ctx, chain.tx.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if notes.Status != "indexed" || len(notes.Notes) != 2 {
		t.Fatalf("notes: %+v", notes)
	}
	output := notes.Notes[0]
	if output.TransactionHash != chain.tx.Hash() || output.Commitment != common.Hash(request.Params.NoteCommitments[0]) || output.LeafIndex != uint32((chain.block.NumberU64()-10)*2) || output.Token != request.Params.Token || !bytes.Equal(output.Ciphertext, request.Deliveries[0].Ciphertext) || !bytes.Equal(output.Clue, request.Deliveries[0].Clue) {
		t.Fatalf("Core projection changed: %+v", output)
	}
	batch, err := api.GetBlock(ctx, chain.block.NumberU64())
	if err != nil {
		t.Fatal(err)
	}
	storedLogs, err := json.Marshal(batch.Logs)
	if err != nil {
		t.Fatal(err)
	}
	expectedLogs, err := json.Marshal(chain.logs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedLogs, expectedLogs) {
		t.Fatalf("Core logs changed: got %s want %s", storedLogs, expectedLogs)
	}
	events, err := api.GetEvents(ctx, Range{FromBlock: batch.Block.Number, ToBlock: batch.Block.Number, BlockHash: batch.Block.Hash})
	if err != nil {
		t.Fatal(err)
	}
	deposit, fee := false, false
	for _, event := range events {
		if event.TransactionHash != chain.tx.Hash() {
			t.Fatal("lost outer transaction hash")
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
			if v.Payer != request.Funding.Payer || v.Amount != "10" {
				t.Fatalf("payer attributed from outer transaction: %+v", v)
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
			if v.Recipient != request.Params.FeeRecipient || v.Amount != "1" {
				t.Fatalf("fee facts changed: %+v", v)
			}
			fee = true
		}
	}
	if !deposit || !fee {
		t.Fatal("missing deposit or broadcaster fee event")
	}
}

func TestRouterCoreEmitterIndexingAndHistoricalRestart(t *testing.T) {
	for _, name := range []string{"direct-native", "direct-erc20", "relayed-erc20", "sponsored-native-player"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			historical, dep, oldRequest := routerCoreBoundary(t, "historical-direct-core", 10, common.Hash{})
			routed, _, newRequest := routerCoreBoundary(t, name, 11, historical.block.Hash())
			path := filepath.Join(t.TempDir(), "index.sqlite")
			db, err := storage.OpenSQLite(ctx, path, dep)
			if err != nil {
				t.Fatal(err)
			}
			decoder, err := protocol.NewDeploymentDecoder(dep)
			if err != nil {
				t.Fatal(err)
			}
			ix := &indexer.Indexer{Chain: historical, Store: db, Decoder: decoder, Deployment: dep}
			if err := ix.Verify(ctx); err != nil {
				t.Fatal(err)
			}
			if progress, err := ix.Step(ctx); err != nil || !progress {
				t.Fatalf("historical step: %v %v", progress, err)
			}
			oldBlock, err := db.Block(ctx, 10)
			if err != nil || oldBlock == nil {
				t.Fatalf("historical block: %+v %v", oldBlock, err)
			}
			if err := db.SaveFinalizedAnchor(ctx, *oldBlock); err != nil {
				t.Fatal(err)
			}
			db.Close()
			// Reopen an existing deployment without changing its persisted profile.
			db, err = storage.OpenSQLite(ctx, path, dep)
			if err != nil {
				t.Fatal(err)
			}
			routed.previous = historical.block.Header()
			ix.Chain, ix.Store = routed, db
			if progress, err := ix.Step(ctx); err != nil || !progress {
				t.Fatalf("router step: %v %v", progress, err)
			}
			if len(routed.filters) != 1 || !reflect.DeepEqual(routed.filters[0].Addresses, []common.Address{dep.Protocol}) || routed.filters[0].BlockHash == nil || *routed.filters[0].BlockHash != routed.block.Hash() {
				t.Fatalf("filter used outer target: %+v", routed.filters)
			}
			db.Close()
			db, err = storage.OpenSQLite(ctx, path, dep)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			api, err := New(db, dep, "1")
			if err != nil {
				t.Fatal(err)
			}
			api.Transactions = routed
			assertRouterCoreProjection(t, api, routed, newRequest)
			result, err := api.GetTransactionOperations(ctx, routed.tx.Hash())
			if err != nil || result.Status != "indexed" || result.UnsupportedReason != "indirect_call_attribution_requires_traces" {
				t.Fatalf("indirect boundary: %+v %v", result, err)
			}
			page, err := api.GetSettlementOperations(ctx, OperationsQuery{FromBlock: ptrUint64(11), Limit: 1})
			if err != nil || len(page.Operations) != 1 || page.Operations[0].TxHash != routed.tx.Hash() {
				t.Fatalf("Core log discovery skipped Router target: %+v %v", page, err)
			}
			anchor, err := db.FinalizedAnchor(ctx)
			if err != nil || anchor == nil || *anchor != *oldBlock {
				t.Fatalf("Router changed finalized history: %+v %v", anchor, err)
			}
			api.Transactions = historical
			assertRouterCoreProjection(t, api, historical, oldRequest)
			oldOperations, err := api.GetTransactionOperations(ctx, historical.tx.Hash())
			if err != nil || len(oldOperations.Operations) != 1 || oldOperations.UnsupportedReason != "" || oldOperations.Operations[0].UnsupportedReason != "" || len(oldOperations.Operations[0].Funding) != 1 || oldOperations.Operations[0].Funding[0].Payer != oldRequest.Funding.Payer {
				t.Fatalf("historical direct Core operation changed: %+v %v", oldOperations, err)
			}
			if err := db.Rollback(ctx, 10); err != nil {
				t.Fatal(err)
			}
			api.Transactions = routed
			notes, err := api.GetTransactionNoteUpdates(ctx, routed.tx.Hash())
			if err != nil || notes.Status != "pending_index" || notes.Notes != nil {
				t.Fatalf("orphan route remains visible: %+v %v", notes, err)
			}
			if note, err := db.Note(ctx, common.Hash(newRequest.Params.NoteCommitments[0]), 11); err != nil || note != nil {
				t.Fatalf("orphan note survives: %+v %v", note, err)
			}
			api.Transactions = historical
			assertRouterCoreProjection(t, api, historical, oldRequest)
		})
	}
}

func ptrUint64(v uint64) *uint64 { return &v }

func TestRouterCannotReplaceConfiguredCoreEmitter(t *testing.T) {
	chain, dep, _ := routerCoreBoundary(t, "direct-erc20", 10, common.Hash{})
	decoder, err := protocol.NewDeploymentDecoder(dep)
	if err != nil {
		t.Fatal(err)
	}
	block := model.Block{Number: 10, Hash: chain.block.Hash(), Timestamp: chain.block.Time()}
	for i := range chain.logs {
		chain.logs[i].Address = *chain.tx.To()
	}
	if _, err := decoder.Decode(block, chain.logs); err == nil {
		t.Fatal("accepted Router-emitted copies of Core events")
	}
}
