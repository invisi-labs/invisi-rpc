package indexer

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"math/big"
	"testing"
)

type fakeChain struct {
	headers     map[uint64]*types.Header
	head        uint64
	code        []byte
	id          *big.Int
	logs        []types.Log
	queries     []ethereum.FilterQuery
	afterFilter func()
}

func (c *fakeChain) ChainID(context.Context) (*big.Int, error) { return c.id, nil }
func (c *fakeChain) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return c.code, nil
}
func (c *fakeChain) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	i := c.head
	if n != nil {
		i = n.Uint64()
	}
	h := c.headers[i]
	if h == nil {
		return nil, fmt.Errorf("missing header %d", i)
	}
	return h, nil
}
func (c *fakeChain) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	c.queries = append(c.queries, q)
	if c.afterFilter != nil {
		c.afterFilter()
	}
	return c.logs, nil
}

type fakeStore struct {
	blocks    map[uint64]model.Block
	applied   []model.Batch
	rollbacks []uint64
}

func (s *fakeStore) Checkpoint(context.Context) (*model.Block, error) {
	var cp *model.Block
	for _, b := range s.blocks {
		if cp == nil || b.Number > cp.Number {
			v := b
			cp = &v
		}
	}
	return cp, nil
}
func (s *fakeStore) Block(_ context.Context, n uint64) (*model.Block, error) {
	b, ok := s.blocks[n]
	if !ok {
		return nil, nil
	}
	return &b, nil
}
func (s *fakeStore) Apply(_ context.Context, b model.Batch) error {
	s.applied = append(s.applied, b)
	s.blocks[b.Block.Number] = b.Block
	return nil
}
func (s *fakeStore) Rollback(_ context.Context, n uint64) error {
	s.rollbacks = append(s.rollbacks, n)
	for k := range s.blocks {
		if k > n {
			delete(s.blocks, k)
		}
	}
	return nil
}

type fakeDecoder struct{ calls int }

func (d *fakeDecoder) Decode(b model.Block, logs []types.Log) (model.Batch, error) {
	d.calls++
	return model.Batch{Block: b, Logs: logs}, nil
}
func fixture() (*Indexer, *fakeChain, *fakeStore, *fakeDecoder) {
	c := &fakeChain{headers: map[uint64]*types.Header{}, head: 5, code: []byte{1, 2, 3}, id: big.NewInt(31337)}
	var parent common.Hash
	for n := uint64(0); n <= 5; n++ {
		h := &types.Header{Number: new(big.Int).SetUint64(n), ParentHash: parent, Extra: []byte{byte(n)}}
		c.headers[n] = h
		parent = h.Hash()
	}
	s := &fakeStore{blocks: map[uint64]model.Block{}}
	d := &fakeDecoder{}
	return &Indexer{Chain: c, Store: s, Decoder: d, Deployment: model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("0x1234"), FromBlock: 2, RuntimeCodeHash: crypto.Keccak256Hash(c.code)}}, c, s, d
}
func block(h *types.Header) model.Block {
	return model.Block{Number: h.Number.Uint64(), Hash: h.Hash(), ParentHash: h.ParentHash}
}
func TestVerifyDeploymentPins(t *testing.T) {
	for _, kind := range []string{"valid", "chain", "runtime", "empty"} {
		t.Run(kind, func(t *testing.T) {
			i, c, _, _ := fixture()
			switch kind {
			case "chain":
				c.id = big.NewInt(1)
			case "runtime":
				c.code = []byte{4}
			case "empty":
				c.code = nil
			}
			err := i.Verify(context.Background())
			if (err == nil) != (kind == "valid") {
				t.Fatalf("verification: %v", err)
			}
		})
	}
}
func TestConfirmationBoundary(t *testing.T) {
	for _, confirm := range []uint64{0, 3, 4, 6} {
		t.Run(fmt.Sprint(confirm), func(t *testing.T) {
			i, _, s, d := fixture()
			i.Confirmations = confirm
			changed, err := i.Step(context.Background())
			want := confirm <= 3
			if err != nil || changed != want {
				t.Fatalf("step %v %v", changed, err)
			}
			if len(s.applied) != boolInt(want) || d.calls != boolInt(want) {
				t.Fatal("unconfirmed block published")
			}
		})
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func TestRejectInconsistentLogs(t *testing.T) {
	for _, kind := range []string{"hash", "number", "address", "removed"} {
		t.Run(kind, func(t *testing.T) {
			i, c, s, d := fixture()
			l := types.Log{BlockHash: c.headers[2].Hash(), BlockNumber: 2, Address: i.Deployment.Protocol}
			switch kind {
			case "hash":
				l.BlockHash = common.Hash{}
			case "number":
				l.BlockNumber = 3
			case "address":
				l.Address = common.Address{}
			case "removed":
				l.Removed = true
			}
			c.logs = []types.Log{l}
			changed, err := i.Step(context.Background())
			if err == nil || changed || len(s.applied) != 0 || d.calls != 0 {
				t.Fatal("inconsistent RPC log reached decoder or store")
			}
			q := c.queries[0]
			if q.BlockHash == nil || *q.BlockHash != c.headers[2].Hash() || len(q.Addresses) != 1 || q.Addresses[0] != i.Deployment.Protocol {
				t.Fatal("log query was not deployment and hash scoped")
			}
		})
	}
}
func TestRestartResumesCanonicalCheckpoint(t *testing.T) {
	i, c, s, _ := fixture()
	s.blocks[2] = block(c.headers[2])
	s.blocks[3] = block(c.headers[3])
	changed, err := i.Step(context.Background())
	if err != nil || !changed || len(s.applied) != 1 || s.applied[0].Block.Number != 4 || len(s.rollbacks) != 0 {
		t.Fatalf("restart step: %v %v %+v", changed, err, s)
	}
}
func TestReorgRollsBackToCanonicalAncestor(t *testing.T) {
	i, c, s, _ := fixture()
	for n := uint64(2); n <= 4; n++ {
		s.blocks[n] = block(c.headers[n])
	}
	c.headers[3] = &types.Header{Number: big.NewInt(3), ParentHash: c.headers[2].Hash(), Extra: []byte("fork")}
	c.headers[4] = &types.Header{Number: big.NewInt(4), ParentHash: c.headers[3].Hash()}
	changed, err := i.Step(context.Background())
	if err != nil || !changed || len(s.rollbacks) != 2 || s.rollbacks[0] != 3 || s.rollbacks[1] != 2 || len(s.applied) != 1 || s.applied[0].Block.Hash != c.headers[3].Hash() {
		t.Fatalf("reorg step: %v %v %+v", changed, err, s)
	}
}
func TestReorgReconcilesWithoutConfirmedNewBlock(t *testing.T) {
	i, c, s, _ := fixture()
	s.blocks[2] = block(c.headers[2])
	s.blocks[3] = block(c.headers[3])
	c.headers[3] = &types.Header{Number: big.NewInt(3), ParentHash: c.headers[2].Hash(), Extra: []byte("fork")}
	i.Confirmations = 3
	changed, err := i.Step(context.Background())
	if err != nil || changed || len(s.rollbacks) != 1 || s.rollbacks[0] != 2 || len(s.applied) != 0 {
		t.Fatalf("unconfirmed reconciliation %v %v", changed, err)
	}
}
func TestStartingBlockReorgFailsClosed(t *testing.T) {
	i, c, s, _ := fixture()
	s.blocks[2] = block(c.headers[2])
	c.headers[2] = &types.Header{Number: big.NewInt(2), Extra: []byte("fork")}
	changed, err := i.Step(context.Background())
	if err == nil || changed || len(s.rollbacks) != 0 || len(s.applied) != 0 {
		t.Fatal("deployment block reorg accepted")
	}
}
func TestConcurrentHeaderChangeDoesNotPublish(t *testing.T) {
	i, c, s, d := fixture()
	c.afterFilter = func() { c.headers[2] = &types.Header{Number: big.NewInt(2), Extra: []byte("concurrent fork")} }
	changed, err := i.Step(context.Background())
	if err == nil || changed || d.calls != 1 || len(s.applied) != 0 {
		t.Fatal("concurrent fork published")
	}
}
func TestChangedParentDoesNotFetchOrPublish(t *testing.T) {
	i, c, s, d := fixture()
	s.blocks[2] = block(c.headers[2])
	c.headers[3] = &types.Header{Number: big.NewInt(3), ParentHash: common.Hash{1}}
	changed, err := i.Step(context.Background())
	if err == nil || changed || d.calls != 0 || len(c.queries) != 0 || len(s.applied) != 0 {
		t.Fatal("inconsistent parent published")
	}
}

func TestReorgToShorterChain(t *testing.T) {
	i, c, s, _ := fixture()
	for n := uint64(2); n <= 5; n++ {
		s.blocks[n] = block(c.headers[n])
	}
	c.head = 3
	delete(c.headers, 4)
	delete(c.headers, 5)
	changed, err := i.Step(context.Background())
	if err != nil || changed || len(s.rollbacks) != 2 {
		t.Fatalf("short chain recovery: %v %v %+v", changed, err, s.rollbacks)
	}
	cp, _ := s.Checkpoint(context.Background())
	if cp.Number != 3 {
		t.Fatalf("head not rewound: %+v", cp)
	}
}
