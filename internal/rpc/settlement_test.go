package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/invisi-lab/invisi-rpc/internal/protocol"
	sdkcontracts "github.com/invisi-lab/invisi-sdk-go/protocol/contracts"
	"github.com/invisi-lab/invisi-sdk-go/protocol/settlement"
)

type settlementChain struct {
	block      *types.Block
	tx         *types.Transaction
	receipt    *types.Receipt
	code       []byte
	wrongChain bool
}

func (c *settlementChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return c.receipt, nil
}
func (c *settlementChain) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return c.block.Header(), nil
}
func (c *settlementChain) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return c.tx, false, nil
}
func (c *settlementChain) BlockByNumber(context.Context, *big.Int) (*types.Block, error) {
	return c.block, nil
}
func (c *settlementChain) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return c.code, nil
}
func (c *settlementChain) ChainID(context.Context) (*big.Int, error) {
	if c.wrongChain {
		return big.NewInt(1), nil
	}
	return big.NewInt(56), nil
}
func settlementFixture(t *testing.T) (*API, *settlementChain, *transactionBackend) {
	t.Helper()
	raw, e := os.ReadFile("testdata/settlement-statements.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Vectors []struct {
			Data hexutil.Bytes `json:"data"`
		} `json:"vectors"`
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture")
	}
	data := fixture.Vectors[0].Data
	r, e := settlement.Decode(data)
	if e != nil {
		t.Fatal(e)
	}
	address := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := types.NewTx(&types.LegacyTx{To: &address, Data: data})
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(10), Time: 100}).WithBody(types.Body{Transactions: []*types.Transaction{tx}})
	b := model.Block{Number: 10, Hash: block.Hash(), ParentHash: block.ParentHash(), Timestamp: 100}
	aRaw := []byte(sdkcontracts.InvisiCoreMetaData.ABI)
	contract, e := abi.JSON(strings.NewReader(string(aRaw)))
	if e != nil {
		t.Fatal(e)
	}
	logs := []types.Log{}
	emit := func(name string, topics []common.Hash, values ...any) {
		t.Helper()
		event := contract.Events[name]
		encoded, e := event.Inputs.NonIndexed().Pack(values...)
		if e != nil {
			t.Fatal(e)
		}
		logs = append(logs, types.Log{Address: address, Topics: append([]common.Hash{event.ID}, topics...), Data: encoded, BlockNumber: 10, BlockHash: block.Hash(), TxHash: tx.Hash(), Index: uint(len(logs))})
	}
	emit("CommitmentsInserted", []common.Hash{common.BigToHash(big.NewInt(1))}, uint32(0), r.Params.NoteCommitments)
	emit("PublicDeposit", []common.Hash{common.BytesToHash(r.Params.Token[:]), common.BytesToHash(r.Funding.Payer[:])}, r.Funding.Amount, crypto.Keccak256Hash(r.Proof))
	emit("BroadcasterFee", []common.Hash{common.BytesToHash(r.Params.Token[:]), common.BytesToHash(r.Params.FeeRecipient[:])}, r.Params.FeeAmount)
	emit("CrossTreeTransact", []common.Hash{common.BigToHash(big.NewInt(1))}, r.Params.Token, r.Params.Sources, r.Params.Nullifiers, r.Params.NoteCommitments, []uint32{0}, r.Deliveries, r.Params.WithdrawalAddress, r.Params.WithdrawalAmount, r.Params.Deadline)
	decoder, e := protocol.NewDecoder()
	if e != nil {
		t.Fatal(e)
	}
	batch, e := decoder.Decode(b, logs)
	if e != nil {
		t.Fatal(e)
	}
	batch.BindBlockContext()
	store := &transactionBackend{fakeBackend: fakeBackend{head: &b}, batch: &batch}
	code := []byte{0}
	api, e := New(store, model.Deployment{ChainID: "56", Protocol: address, FromBlock: 10, RuntimeCodeHash: crypto.Keccak256Hash(code)}, "1")
	if e != nil {
		t.Fatal(e)
	}
	receipt := &types.Receipt{TxHash: tx.Hash(), BlockNumber: big.NewInt(10), BlockHash: block.Hash(), Status: 1}
	for i := range logs {
		receipt.Logs = append(receipt.Logs, &logs[i])
	}
	receipt.Logs = append(receipt.Logs, &types.Log{Address: r.Params.Token, Topics: []common.Hash{transferTopic, common.BytesToHash(r.Funding.Payer[:]), common.BytesToHash(address[:])}, Data: common.LeftPadBytes(r.Funding.Amount.Bytes(), 32), BlockNumber: 10, BlockHash: b.Hash, TxHash: tx.Hash(), Index: 4})
	chain := &settlementChain{block: block, tx: tx, receipt: receipt, code: code}
	api.Transactions = chain
	return api, chain, store
}
func TestSettlementCanonicalFactsAndFunding(t *testing.T) {
	a, chain, _ := settlementFixture(t)
	ctx := context.Background()
	page, e := a.GetSettlementOperations(ctx, OperationsQuery{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Operations) != 1 || page.NextCursor != "" {
		t.Fatalf("page %+v", page)
	}
	o := page.Operations[0]
	if o.UnsupportedReason != "" || o.MainCircuitCapacity != "3" || len(o.PublicInputs) != 26 || o.Funding[0].Payer == (common.Address{}) || len(o.NoteOutputs) != 1 || o.OperationDigest == (common.Hash{}) {
		t.Fatalf("facts %+v", o)
	}
	h, e := HTTPHandler(a)
	if e != nil {
		t.Fatal(e)
	}
	if result := call(t, h, "invisi_getSettlementOperations", []any{map[string]any{"limit": 1}}, ""); result.Error != nil {
		t.Fatal(result.Error)
	}
	chain.receipt.Logs = chain.receipt.Logs[:4]
	result, e := a.GetTransactionOperations(ctx, chain.tx.Hash())
	if e != nil {
		t.Fatal(e)
	}
	if result.Operations[0].UnsupportedReason != "funding_receipts_cannot_be_authenticated" {
		t.Fatal("missing actual payer receipt trusted")
	}
	chain.wrongChain = true
	if _, e = a.GetSettlementOperations(ctx, OperationsQuery{Limit: 1}); e == nil {
		t.Fatal("wrong chain accepted")
	}
}
func TestSettlementRejectsTamperedSnapshotAndLogs(t *testing.T) {
	a, chain, store := settlementFixture(t)
	ctx := context.Background()
	chain.receipt.Logs[0].Data = []byte{1}
	if _, e := a.GetTransactionOperations(ctx, chain.tx.Hash()); e == nil {
		t.Fatal("receipt/index mismatch accepted")
	}
	a, chain, store = settlementFixture(t)
	store.reorgAfter = 1
	if _, e := a.GetSettlementOperations(ctx, OperationsQuery{Limit: 1}); e == nil {
		t.Fatal("reorg accepted")
	}
	for _, q := range []OperationsQuery{{Limit: 0}, {Limit: 101}, {Limit: 1, Cursor: "bad"}} {
		if _, e := a.GetSettlementOperations(ctx, q); e == nil {
			t.Fatal("bad bound/cursor accepted")
		}
	}
	var q OperationsQuery
	if json.Unmarshal([]byte(`{"limit":1,"approved":true}`), &q) == nil {
		t.Fatal("unknown query field accepted")
	}
}
func TestSettlementFailedDirectAndUnsupportedIndirect(t *testing.T) {
	a, chain, store := settlementFixture(t)
	chain.receipt.Status = 0
	chain.receipt.Logs = nil
	store.batch.Logs = nil
	store.batch.Events = nil
	store.batch.Outputs = nil
	result, e := a.GetTransactionOperations(context.Background(), chain.tx.Hash())
	if e != nil {
		t.Fatal(e)
	}
	if result.Status != "failed" || result.Operations[0].Success {
		t.Fatal("revert misclassified")
	}
	other := common.HexToAddress("1234")
	chain.tx = types.NewTx(&types.LegacyTx{To: &other, Data: chain.tx.Data()})
	chain.receipt.TxHash = chain.tx.Hash()
	chain.block = chain.block.WithBody(types.Body{Transactions: []*types.Transaction{chain.tx}})
	result, e = a.GetTransactionOperations(context.Background(), chain.tx.Hash())
	if e != nil {
		t.Fatal(e)
	}
	if result.UnsupportedReason != "indirect_call_attribution_requires_traces" {
		t.Fatal("indirect failure guessed")
	}
}

func TestSettlementAggregateNativeMembersAndPooledPull(t *testing.T) {
	a, chain, store := settlementFixture(t)
	raw, e := os.ReadFile("testdata/settlement-statements.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Vectors []struct {
			Data hexutil.Bytes `json:"data"`
		} `json:"vectors"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	data := fixture.Vectors[2].Data
	members, e := settlement.DecodeCalls(data)
	if e != nil {
		t.Fatal(e)
	}
	address := a.Deployment.Protocol
	chain.tx = types.NewTx(&types.LegacyTx{To: &address, Data: data})
	chain.block = chain.block.WithBody(types.Body{Transactions: []*types.Transaction{chain.tx}})
	raw = []byte(sdkcontracts.InvisiCoreMetaData.ABI)
	contract, e := abi.JSON(strings.NewReader(string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	logs := []types.Log{}
	emit := func(name string, topics []common.Hash, values ...any) {
		t.Helper()
		event := contract.Events[name]
		encoded, e := event.Inputs.NonIndexed().Pack(values...)
		if e != nil {
			t.Fatal(e)
		}
		logs = append(logs, types.Log{Address: address, Topics: append([]common.Hash{event.ID}, topics...), Data: encoded, BlockNumber: 10, BlockHash: chain.block.Hash(), TxHash: chain.tx.Hash(), Index: uint(len(logs))})
	}
	commitments := [][32]byte{}
	tokens := []common.Address{}
	packed := []byte{}
	total := new(big.Int)
	for i, r := range members {
		c, _, _, e := settlement.TransactStatement(big.NewInt(56), address, &r)
		if e != nil {
			t.Fatal(e)
		}
		h, e := settlement.AggregateMemberHash(big.NewInt(56), address, &r)
		if e != nil {
			t.Fatal(e)
		}
		emit("BatchRequest", []common.Hash{common.BigToHash(big.NewInt(int64(i)))}, r.Params.Token, big.NewInt(int64(c)), r.Params.Sources, r.Params.Nullifiers, big.NewInt(int64(len(r.Params.NoteCommitments))), r.Funding.Payer, r.Funding.Amount, r.Params.WithdrawalAddress, r.Params.WithdrawalAmount, r.Params.Deadline, h)
		emit("BroadcasterFee", []common.Hash{common.BytesToHash(r.Params.Token[:]), common.BytesToHash(r.Params.FeeRecipient[:])}, r.Params.FeeAmount)
		commitments = append(commitments, r.Params.NoteCommitments...)
		for _, d := range r.Deliveries {
			tokens = append(tokens, r.Params.Token)
			packed = append(packed, byte(len(d.Ciphertext)>>8), byte(len(d.Ciphertext)))
			packed = append(packed, d.Ciphertext...)
			packed = append(packed, d.Clue...)
		}
		total.Add(total, r.Funding.Amount)
	}
	emit("BatchOutputs", []common.Hash{common.BigToHash(big.NewInt(1))}, uint32(0), big.NewInt(0), tokens, commitments, packed)
	emit("TransactAggregated", []common.Hash{crypto.Keccak256Hash([]byte{0xde, 0xad, 0xbe, 0xef})}, big.NewInt(2))
	decoder, e := protocol.NewDecoder()
	if e != nil {
		t.Fatal(e)
	}
	batch, e := decoder.Decode(store.batch.Block, logs)
	if e != nil {
		t.Fatal(e)
	}
	batch.BindBlockContext()
	store.batch = &batch
	receipt := &types.Receipt{TxHash: chain.tx.Hash(), BlockNumber: big.NewInt(10), BlockHash: chain.block.Hash(), Status: 1}
	for i := range logs {
		receipt.Logs = append(receipt.Logs, &logs[i])
	}
	r := members[0]
	receipt.Logs = append(receipt.Logs, &types.Log{Address: r.Params.Token, Topics: []common.Hash{transferTopic, common.BytesToHash(r.Funding.Payer[:]), common.BytesToHash(address[:])}, Data: common.LeftPadBytes(total.Bytes(), 32), BlockNumber: 10, BlockHash: chain.block.Hash(), TxHash: chain.tx.Hash(), Index: uint(len(logs))})
	chain.receipt = receipt
	page, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if page.NextCursor == "" || len(page.Operations) != 1 || page.Operations[0].MainCircuitCapacity != "3" || page.Operations[0].UnsupportedReason != "" {
		t.Fatalf("first native member %+v", page)
	}
	second, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 1, Cursor: page.NextCursor})
	if e != nil {
		t.Fatal(e)
	}
	if second.NextCursor != "" || len(second.Operations) != 1 || second.Operations[0].MainCircuitCapacity != "2" || second.Operations[0].UnsupportedReason != "" || second.Operations[0].Funding[0].Amount != "67" || len(second.Operations[0].NoteOutputs) != 2 {
		t.Fatalf("second native member %+v", second)
	}
	if second.Operations[0].MemberIndex == nil || *second.Operations[0].MemberIndex != "1" || second.Snapshot != page.Snapshot {
		t.Fatal("member order/snapshot lost")
	}
}
