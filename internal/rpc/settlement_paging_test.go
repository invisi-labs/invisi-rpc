package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

type replayStore struct {
	fakeBackend
	batches map[uint64]*model.Batch
}

func (s *replayStore) Block(_ context.Context, n uint64) (*model.Block, error) {
	if b := s.batches[n]; b != nil {
		return &b.Block, nil
	}
	return nil, nil
}
func (s *replayStore) Batch(_ context.Context, n uint64) (*model.Batch, error) {
	return s.batches[n], nil
}

type replayChain struct {
	settlementChain
	blocks   map[uint64]*types.Block
	receipts map[common.Hash]*types.Receipt
	reads    int
	created  uint64
}

func (c *replayChain) BlockByNumber(_ context.Context, n *big.Int) (*types.Block, error) {
	return c.blocks[n.Uint64()], nil
}
func (c *replayChain) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	return c.blocks[n.Uint64()].Header(), nil
}
func (c *replayChain) CodeAt(_ context.Context, _ common.Address, n *big.Int) ([]byte, error) {
	if n.Uint64() < c.created {
		return nil, nil
	}
	return c.code, nil
}
func (c *replayChain) TransactionReceipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	c.reads++
	return c.receipts[h], nil
}
func replayFixture(t *testing.T, counts ...int) (*API, *replayChain, []common.Hash) {
	t.Helper()
	address := common.HexToAddress("0x1111111111111111111111111111111111111111")
	chain := &replayChain{settlementChain: settlementChain{code: []byte{0}}, blocks: map[uint64]*types.Block{}, receipts: map[common.Hash]*types.Receipt{}, created: 10}
	store := &replayStore{batches: map[uint64]*model.Batch{}}
	hashes := []common.Hash{}
	var parent common.Hash
	for i, count := range counts {
		number := uint64(10 + i)
		txs := []*types.Transaction{}
		for j := 0; j < count; j++ {
			tx := types.NewTx(&types.LegacyTx{To: &address, Nonce: uint64(len(hashes)), Data: []byte{0xff}})
			txs = append(txs, tx)
			hashes = append(hashes, tx.Hash())
		}
		block := types.NewBlockWithHeader(&types.Header{Number: new(big.Int).SetUint64(number), ParentHash: parent, Time: number}).WithBody(types.Body{Transactions: txs})
		parent = block.Hash()
		chain.blocks[number] = block
		b := model.Block{Number: number, Hash: block.Hash(), ParentHash: block.ParentHash(), Timestamp: number}
		store.batches[number] = &model.Batch{Block: b}
		store.head = &b
		for j, tx := range txs {
			chain.receipts[tx.Hash()] = &types.Receipt{TxHash: tx.Hash(), BlockNumber: block.Number(), BlockHash: block.Hash(), TransactionIndex: uint(j), Status: 0}
		}
	}
	api, e := New(store, model.Deployment{ChainID: "56", Protocol: address, FromBlock: 10, RuntimeCodeHash: crypto.Keccak256Hash(chain.code)}, 24, false, "1")
	if e != nil {
		t.Fatal(e)
	}
	api.Transactions = chain
	return api, chain, hashes
}
func TestSettlementPagingMakesBoundedProgress(t *testing.T) {
	for _, counts := range [][]int{{90, 200}, {600}} {
		t.Run(big.NewInt(int64(counts[0])).String(), func(t *testing.T) {
			a, c, want := replayFixture(t, counts...)
			cursor := ""
			got := []common.Hash{}
			for page := 0; page < 20; page++ {
				c.reads = 0
				v, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100, Cursor: cursor})
				if e != nil {
					t.Fatal(e)
				}
				if c.reads > 256 {
					t.Fatalf("unbounded receipt reads: %d", c.reads)
				}
				for _, op := range v.Operations {
					got = append(got, op.TxHash)
				}
				if v.NextCursor == "" {
					break
				}
				if v.NextCursor == cursor {
					t.Fatal("cursor made no progress")
				}
				cursor = v.NextCursor
			}
			if len(got) != len(want) {
				t.Fatalf("got %d operations, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("operation order/duplication at %d", i)
				}
			}
		})
	}
}
func TestSettlementStartsAtProtocolDeployment(t *testing.T) {
	a, c, _ := replayFixture(t, 0, 1)
	a.Deployment.FromBlock = 11
	c.created = 11
	a.Deployment.Registry = &model.ContractDeployment{Address: common.HexToAddress("0x2222"), RuntimeCodeHash: common.HexToHash("0x1234"), FromBlock: 10}
	page, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Operations) != 1 {
		t.Fatalf("operations: %d", len(page.Operations))
	}
	if a.Deployment.IndexFromBlock() != 10 {
		t.Fatal("registry indexing coverage changed")
	}
}
func TestSettlementRejectsOutOfRangeReplayCursor(t *testing.T) {
	a, _, _ := replayFixture(t, 300)
	page, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		field string
		value int
	}{{"txOffset", 1000000}, {"txOffset", 300}, {"txOffset", -1}, {"offset", 1000000}, {"offset", 1}, {"offset", -1}} {
		var cursor map[string]any
		if e = json.Unmarshal(raw, &cursor); e != nil {
			t.Fatal(e)
		}
		field := test.field
		cursor[field] = test.value
		mutated, _ := json.Marshal(cursor)
		if _, e = a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100, Cursor: base64.RawURLEncoding.EncodeToString(mutated)}); e == nil {
			t.Fatalf("accepted invalid %s", field)
		}
	}
}
