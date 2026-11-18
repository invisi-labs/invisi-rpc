package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/merkle"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

func TestNoteConfirmationSelector(t *testing.T) {
	a, err := New(&fakeBackend{}, model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), FromBlock: 8}, "1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getNote", []any{common.HexToHash("1"), map[string]string{"finality": "finalized"}}, "")
	if r.Error != nil {
		t.Fatalf("confirmation selector rejected: %+v", r.Error)
	}
	var got struct{ Status, Reason string }
	if err := json.Unmarshal(r.Result, &got); err != nil || got.Status != "pending" || got.Reason != "finality_unavailable" {
		t.Fatalf("unexpected %s %v", r.Result, err)
	}
	for _, s := range []any{nil, map[string]string{}, map[string]string{"finality": "latest"}, map[string]any{"finality": "finalized", "atBlock": 10}, 10.5, -1, "10"} {
		if call(t, h, "invisi_getNote", []any{common.HexToHash("1"), s}, "").Error == nil {
			t.Fatalf("invalid selector accepted %v", s)
		}
	}
}
func TestNoteConfirmationMissingAndLag(t *testing.T) {
	header := &types.Header{Number: big.NewInt(10), Time: 123}
	b := model.Block{Number: 10, Hash: header.Hash(), Timestamp: 123, ParentHash: header.ParentHash}
	a, _, chain, _ := transactionFixture(t)
	chain.header = header
	a.Store = &fakeBackend{head: &b}
	a.Finality = fixedFinality{finality.Snapshot{Finalized: finality.Head{Status: "available", Block: &b}}}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getNote", []any{common.HexToHash("1"), map[string]string{"finality": "finalized"}}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var got struct {
		Status, Reason                   string
		Output, Membership, Confirmation any
	}
	if err = json.Unmarshal(r.Result, &got); err != nil || got.Status != "pending" || got.Reason != "not_observed" || got.Output != nil || got.Membership != nil || got.Confirmation != nil {
		t.Fatalf("unexpected %s %v", r.Result, err)
	}
	a.Store = &fakeBackend{}
	r = call(t, h, "invisi_getNote", []any{common.HexToHash("1"), map[string]string{"finality": "finalized"}}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	if err = json.Unmarshal(r.Result, &got); err != nil || got.Reason != "pending_index" {
		t.Fatalf("unexpected %s", r.Result)
	}
}

type confirmationStore struct {
	fakeBackend
	lookup *model.NoteLookup
}

func (s *confirmationStore) Note(_ context.Context, _ common.Hash, at uint64) (*model.NoteLookup, error) {
	if s.lookup != nil && s.lookup.Output.Block.Number > at {
		return nil, nil
	}
	return s.lookup, nil
}

type confirmationChain struct {
	transactionChain
	rootKnown bool
	rootErr   error
	rootBlock common.Hash
	onRoot    func()
}

func (c *confirmationChain) CallContractAtHash(_ context.Context, _ ethereum.CallMsg, block common.Hash) ([]byte, error) {
	c.rootBlock = block
	if c.onRoot != nil {
		c.onRoot()
	}
	if c.rootErr != nil {
		return nil, c.rootErr
	}
	out := make([]byte, 32)
	if c.rootKnown {
		out[31] = 1
	}
	return out, nil
}
func confirmedFixture(t *testing.T) (*API, *confirmationStore, *confirmationChain) {
	t.Helper()
	header := &types.Header{Number: big.NewInt(10), Time: 123}
	block := model.Block{Number: 10, Hash: header.Hash(), Timestamp: 123, ParentHash: header.ParentHash}
	leaf := common.HexToHash("1")
	root, _, err := merkle.Insert(0, leaf, nil)
	if err != nil {
		t.Fatal(err)
	}
	proof := &model.NoteLookup{Output: model.NoteOutput{Block: block, TreeID: 1, LeafIndex: 0, Commitment: leaf, Token: common.HexToAddress("2"), TransactionHash: common.HexToHash("3"), Kind: "shield"}, Membership: model.Membership{Root: model.Root{TreeID: 1, LeafCount: 1, Root: root, Block: block}, LeafIndex: 0, Commitment: leaf, Siblings: append([]common.Hash(nil), merkle.EmptyNodes[:merkle.Depth]...)}}
	store := &confirmationStore{fakeBackend: fakeBackend{head: &block}, lookup: proof}
	chain := &confirmationChain{transactionChain: transactionChain{header: header, receipt: &types.Receipt{TxHash: proof.Output.TransactionHash, BlockHash: block.Hash, BlockNumber: big.NewInt(10), Status: 1}}, rootKnown: true}
	a, err := New(store, model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), FromBlock: 8}, "1")
	if err != nil {
		t.Fatal(err)
	}
	a.Transactions = chain
	a.Finality = fixedFinality{finality.Snapshot{Finalized: finality.Head{Status: "available", Block: &block}, Safe: finality.Head{Status: "available", Block: &block}}}
	return a, store, chain
}
func TestNoteConfirmationEvidence(t *testing.T) {
	a, store, chain := confirmedFixture(t)
	for _, tag := range []string{"safe", "finalized"} {
		h, err := HTTPHandler(a)
		if err != nil {
			t.Fatal(err)
		}
		r := call(t, h, "invisi_getNote", []any{store.lookup.Output.Commitment, map[string]string{"finality": tag}}, "")
		if r.Error != nil {
			t.Fatal(r.Error)
		}
		var result confirmedNote
		if err = json.Unmarshal(r.Result, &result); err != nil || result.Status != "confirmed" || result.Reason != nil || result.Output == nil || result.Membership == nil || result.Confirmation == nil || result.Confirmation.Finality != tag || chain.rootBlock != store.head.Hash {
			t.Fatalf("bad evidence %s %v", r.Result, err)
		}
	}
}
func TestNoteConfirmationRejectsForgedEvidence(t *testing.T) {
	cases := map[string]func(*API, *confirmationStore, *confirmationChain){
		"proof": func(_ *API, s *confirmationStore, _ *confirmationChain) {
			s.lookup.Membership.Siblings[0] = common.HexToHash("99")
		},
		"commitment": func(_ *API, s *confirmationStore, _ *confirmationChain) {
			s.lookup.Output.Commitment = common.HexToHash("99")
		},
		"tree":          func(_ *API, s *confirmationStore, _ *confirmationChain) { s.lookup.Membership.Root.TreeID = 2 },
		"root":          func(_ *API, _ *confirmationStore, c *confirmationChain) { c.rootKnown = false },
		"receipt":       func(_ *API, _ *confirmationStore, c *confirmationChain) { c.receipt.Status = 0 },
		"receipt block": func(_ *API, _ *confirmationStore, c *confirmationChain) { c.receipt.BlockHash = common.HexToHash("99") },
		"receipt tx":    func(_ *API, _ *confirmationStore, c *confirmationChain) { c.receipt.TxHash = common.HexToHash("99") },
		"upstream":      func(_ *API, _ *confirmationStore, c *confirmationChain) { c.rootErr = errors.New("unavailable") },
		"canonical":     func(_ *API, _ *confirmationStore, c *confirmationChain) { c.reorg = true },
		"store reorg":   func(_ *API, s *confirmationStore, _ *confirmationChain) { s.reorgAfter = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			a, s, c := confirmedFixture(t)
			mutate(a, s, c)
			if _, err := a.confirmNote(context.Background(), s.lookup.Membership.Commitment, "finalized"); err == nil {
				t.Fatal("accepted forged or unavailable evidence")
			}
		})
	}
}

type changingFinality struct {
	head  model.Block
	calls int
}

func (f *changingFinality) Read(context.Context) finality.Snapshot {
	f.calls++
	if f.calls > 1 {
		return finality.Unavailable()
	}
	return finality.Snapshot{Finalized: finality.Head{Status: "available", Block: &f.head}}
}
func TestNoteConfirmationFinalityChanges(t *testing.T) {
	a, s, _ := confirmedFixture(t)
	a.Finality = &changingFinality{head: *s.head}
	result, err := a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized")
	if err != nil || result.Status != "pending" || result.Output != nil || *result.Reason != "finality_unavailable" {
		t.Fatalf("accepted changed finality: %+v %v", result, err)
	}
}

func TestNoteConfirmationWireFixture(t *testing.T) {
	a, s, _ := confirmedFixture(t)
	a.Deployment.RuntimeCodeHash = common.HexToHash("abcd")
	s.lookup.Output.Kind = "Shield"
	result, err := a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := "testdata/note-confirmation.json"
	if os.Getenv("UPDATE_NOTE_FIXTURE") == "1" {
		if err = os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, data) {
		t.Fatal("note confirmation wire fixture differs")
	}
}

type mappedConfirmationStore struct {
	*confirmationStore
	blocks map[uint64]model.Block
}

func (s *mappedConfirmationStore) Block(_ context.Context, n uint64) (*model.Block, error) {
	b, ok := s.blocks[n]
	if !ok {
		return nil, nil
	}
	return &b, nil
}

type mappedConfirmationChain struct {
	*confirmationChain
	headers map[uint64]*types.Header
}

func (c *mappedConfirmationChain) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	return c.headers[n.Uint64()], nil
}
func TestNoteConfirmationAnchorsAcrossIndexLag(t *testing.T) {
	a, s, c := confirmedFixture(t)
	h9 := &types.Header{Number: big.NewInt(9), Time: 122}
	h10 := &types.Header{Number: big.NewInt(10), Time: 123, ParentHash: h9.Hash()}
	h11 := &types.Header{Number: big.NewInt(11), Time: 124, ParentHash: h10.Hash()}
	block := func(h *types.Header) model.Block {
		return model.Block{Number: h.Number.Uint64(), Timestamp: h.Time, Hash: h.Hash(), ParentHash: h.ParentHash}
	}
	b9, b10, b11 := block(h9), block(h10), block(h11)
	s.lookup.Output.Block = b10
	s.lookup.Membership.Root.Block = b10
	c.receipt.BlockHash = b10.Hash
	m := &mappedConfirmationStore{s, map[uint64]model.Block{9: b9, 10: b10, 11: b11}}
	a.Store = m
	a.Transactions = &mappedConfirmationChain{c, map[uint64]*types.Header{9: h9, 10: h10, 11: h11}}
	s.head = &b11
	setHead := func(b model.Block) {
		a.Finality = fixedFinality{finality.Snapshot{Finalized: finality.Head{Status: "available", Block: &b}}}
	}
	setHead(b9)
	result, err := a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized")
	if err != nil || result.Status != "pending" || *result.Reason != "confirmation_pending" {
		t.Fatalf("not pending confirmation: %+v %v", result, err)
	}
	setHead(b10)
	result, err = a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized")
	if err != nil || result.Status != "confirmed" || result.Confirmation.VerifiedAt != b10 {
		t.Fatalf("bad finalized anchor: %+v %v", result, err)
	}
	// A reorg above native finality must not return an orphaned indexedHead.
	mapped := a.Transactions.(*mappedConfirmationChain)
	c.onRoot = func() { changed := *h11; changed.Time++; mapped.headers[11] = &changed }
	if _, err = a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized"); err == nil {
		t.Fatal("accepted orphaned indexed head")
	}
	c.onRoot = nil
	mapped.headers[11] = h11
	s.head = &b10
	setHead(b11)
	result, err = a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized")
	if err != nil || result.Status != "confirmed" || result.Confirmation.VerifiedAt != b10 {
		t.Fatalf("bad lagged anchor: %+v %v", result, err)
	}
}

func TestNoteConfirmationUnavailableFinality(t *testing.T) {
	for _, state := range []string{"unsupported", "unavailable", "inconsistent"} {
		t.Run(state, func(t *testing.T) {
			a, s, _ := confirmedFixture(t)
			a.Finality = fixedFinality{finality.Snapshot{Finalized: finality.Head{Status: state}}}
			result, err := a.confirmNote(context.Background(), s.lookup.Output.Commitment, "finalized")
			if err != nil || result.Status != "pending" || result.Reason == nil || *result.Reason != "finality_"+state || result.Output != nil || result.Confirmation != nil {
				t.Fatalf("unexpected: %+v %v", result, err)
			}
		})
	}
}
