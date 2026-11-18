package indexer

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type rangeChain struct {
	*fakeChain
	headersErr error
	limit      uint64
	afterRange func()
	badHash    bool
	duplicate  bool
}

func (c *rangeChain) Headers(ctx context.Context, from, to uint64) ([]*types.Header, error) {
	if c.headersErr != nil {
		return nil, c.headersErr
	}
	out := []*types.Header{}
	for n := from; n <= to; n++ {
		h, e := c.HeaderByNumber(ctx, new(big.Int).SetUint64(n))
		if e != nil {
			return nil, e
		}
		copy := *h
		out = append(out, &copy)
	}
	if c.badHash {
		out[1].ParentHash[0] ^= 1
	}
	return out, nil
}
func (c *rangeChain) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	if q.BlockHash != nil {
		return c.fakeChain.FilterLogs(ctx, q)
	}
	if c.limit > 0 && q.ToBlock.Uint64()-q.FromBlock.Uint64()+1 > c.limit {
		return nil, errors.New("range limit")
	}
	c.queries = append(c.queries, q)
	out := []types.Log{}
	for _, l := range c.logs {
		if l.BlockNumber >= q.FromBlock.Uint64() && l.BlockNumber <= q.ToBlock.Uint64() {
			out = append(out, l)
		}
	}
	if c.duplicate && len(out) > 0 {
		out = append(out, out[0])
	}
	if c.afterRange != nil {
		c.afterRange()
	}
	return out, nil
}

type rangeStore struct {
	*fakeStore
	commits    int
	fail       bool
	afterApply func()
}

func (s *rangeStore) ApplyRange(ctx context.Context, blocks []model.Batch, guard func(context.Context) (func(), error)) error {
	if s.fail {
		return errors.New("range write failed")
	}
	if s.afterApply != nil {
		s.afterApply()
	}
	release, e := guard(ctx)
	if release != nil {
		defer release()
	}
	if e != nil {
		return e
	}
	for _, b := range blocks {
		if e := s.Apply(ctx, b); e != nil {
			return e
		}
	}
	s.commits++
	return nil
}

type rangeFinality struct {
	anchor    *model.Block
	checks    int
	conflict  bool
	fenced    bool
	available bool
}

func (f *rangeFinality) Check(context.Context) error {
	if f.fenced {
		return errors.New("fenced")
	}
	return nil
}
func (f *rangeFinality) Finalized(context.Context) (*model.Block, error) {
	f.checks++
	if !f.available {
		return nil, nil
	}
	return f.anchor, nil
}
func (f *rangeFinality) ValidateAnchor(context.Context, model.Block) error {
	if f.conflict {
		return finality.ErrFinalityConflict
	}
	return nil
}
func (f *rangeFinality) Fence(context.Context) error { f.fenced = true; return nil }
func rangeFixture() (*Indexer, *rangeChain, *rangeStore, *rangeFinality) {
	i, c, s, _ := fixture()
	chain := &rangeChain{fakeChain: c}
	store := &rangeStore{fakeStore: s}
	anchor := block(c.headers[c.head])
	fin := &rangeFinality{anchor: &anchor, available: true}
	i.Chain = chain
	i.Store = store
	i.Finality = fin
	i.BatchSize = 4
	return i, chain, store, fin
}
func TestFinalizedRangeAndSequentialFallback(t *testing.T) {
	i, _, s, f := rangeFixture()
	progress, e := i.Step(context.Background())
	if e != nil || !progress || len(s.applied) != 4 || s.commits != 1 || f.checks != 2 {
		t.Fatal("range did not commit", len(s.applied), s.commits, e)
	}
	i, c, s, f := rangeFixture()
	f.available = false
	progress, e = i.Step(context.Background())
	if e != nil || !progress || len(s.applied) != 1 || c.queries[0].BlockHash == nil {
		t.Fatal("unavailable finality changed head path", e)
	}
	i, c, s, _ = rangeFixture()
	c.headersErr = ErrBatchUnavailable
	progress, e = i.Step(context.Background())
	if e != nil || !progress || len(s.applied) != 1 {
		t.Fatal("unsupported batching did not fall back", e)
	}
	i, c, s, _ = rangeFixture()
	c.limit = 2
	progress, e = i.Step(context.Background())
	if e != nil || !progress || len(s.applied) != 2 || s.commits != 1 {
		t.Fatal("range limit was not adapted", e)
	}
}
func TestFinalizedRangeRejectsBadHeadersLogsAndCommitReorg(t *testing.T) {
	for _, kind := range []string{"parent", "duplicate_log", "wrong_hash", "removed", "wrong_emitter", "before_activation", "commit_reorg", "reorg_and_finality_conflict", "finality_conflict", "write_failure"} {
		t.Run(kind, func(t *testing.T) {
			i, c, s, f := rangeFixture()
			h := c.headers[2]
			c.logs = []types.Log{{Address: i.Deployment.Protocol, BlockNumber: 2, BlockHash: h.Hash(), Index: 0}}
			switch kind {
			case "parent":
				c.badHash = true
			case "duplicate_log":
				c.duplicate = true
			case "wrong_hash":
				c.logs[0].BlockHash[0] ^= 1
			case "removed":
				c.logs[0].Removed = true
			case "wrong_emitter":
				c.logs[0].Address[0] ^= 1
			case "before_activation":
				i.Deployment.FromBlock = 3
				i.Deployment.Registry = &model.ContractDeployment{Address: common.HexToAddress("999"), FromBlock: 2}
			case "commit_reorg", "reorg_and_finality_conflict":
				if kind == "reorg_and_finality_conflict" {
					f.conflict = true
				}
				s.afterApply = func() { copy := *c.headers[5]; copy.Extra = []byte{99}; c.headers[5] = &copy }
			case "finality_conflict":
				f.conflict = true
			case "write_failure":
				s.fail = true
			}
			if _, e := i.Step(context.Background()); e == nil {
				t.Fatal("invalid range accepted")
			}
			if len(s.applied) != 0 {
				t.Fatal("partial range published")
			}
			if (kind == "finality_conflict" || kind == "reorg_and_finality_conflict") && !f.fenced {
				t.Fatal("finality conflict not persisted")
			}
		})
	}
}

func (f *rangeFinality) AcquirePublication(context.Context) (func(), error) {
	if f.conflict {
		return nil, finality.ErrFinalityConflict
	}
	return func() {}, nil
}

func TestFinalizedRangeTerminalAnchorConflictIsFenced(t *testing.T) {
	i, c, s, f := rangeFixture()
	// ValidateAnchor subsequently reports success: the earlier terminal observation
	// alone must still permanently fence this accepted finalized height.
	s.afterApply = func() { h := *c.headers[5]; h.Extra = []byte{99}; c.headers[5] = &h }
	if _, e := i.Step(context.Background()); !errors.Is(e, finality.ErrFinalityConflict) || !f.fenced || len(s.applied) != 0 {
		t.Fatal("transient accepted anchor conflict was lost", e)
	}
}

func TestFinalizedRangeThenHeadReorgAndRestart(t *testing.T) {
	ctx := context.Background()
	i, c, s, f := rangeFixture()
	if _, e := i.Step(ctx); e != nil {
		t.Fatal(e)
	}
	for n := uint64(6); n <= 7; n++ {
		c.headers[n] = &types.Header{Number: new(big.Int).SetUint64(n), ParentHash: c.headers[n-1].Hash(), Extra: []byte{byte(n)}}
	}
	c.head = 7
	for n := 0; n < 2; n++ {
		if _, e := i.Step(ctx); e != nil {
			t.Fatal(e)
		}
	}
	// Restart uses the persisted checkpoint; finalized history remains untouched.
	restarted := *i
	i = &restarted
	for n := uint64(6); n <= 7; n++ {
		c.headers[n] = &types.Header{Number: new(big.Int).SetUint64(n), ParentHash: c.headers[n-1].Hash(), Extra: []byte{99, byte(n)}}
	}
	for n := 0; n < 4; n++ {
		if _, e := i.Step(ctx); e != nil {
			t.Fatal(e)
		}
		cp, _ := s.Checkpoint(ctx)
		if cp.Number == 7 && cp.Hash == c.headers[7].Hash() {
			break
		}
	}
	cp, _ := s.Checkpoint(ctx)
	if cp.Number != 7 || cp.Hash != c.headers[7].Hash() || f.fenced || len(s.rollbacks) == 0 || s.rollbacks[len(s.rollbacks)-1] != 5 {
		t.Fatal("head reorg did not recover above finalized batch", cp, s.rollbacks)
	}
}

type invalidRangeDecoder struct{ changeIdentity bool }

func (d invalidRangeDecoder) Decode(b model.Block, logs []types.Log) (model.Batch, error) {
	if b.Number == 3 {
		if !d.changeIdentity {
			return model.Batch{}, errors.New("invalid event")
		}
		b.Hash[0] ^= 1
	}
	return model.Batch{Block: b, Logs: logs}, nil
}
func TestFinalizedRangeDecoderFailureIsAtomic(t *testing.T) {
	for _, change := range []bool{false, true} {
		i, _, s, _ := rangeFixture()
		i.Decoder = invalidRangeDecoder{changeIdentity: change}
		if _, e := i.Step(context.Background()); e == nil || len(s.applied) != 0 {
			t.Fatal("decoder failure published history", e)
		}
	}
}
