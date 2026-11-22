package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

type publicationAnchorStore struct {
	anchor   *model.Block
	conflict bool
}

func (s *publicationAnchorStore) FinalizedAnchor(context.Context) (*model.Block, error) {
	return s.anchor, nil
}
func (s *publicationAnchorStore) SaveFinalizedAnchor(_ context.Context, b model.Block) error {
	s.anchor = &b
	return nil
}
func (s *publicationAnchorStore) FinalityConflict(context.Context) (bool, error) {
	return s.conflict, nil
}
func (s *publicationAnchorStore) MarkFinalityConflict(context.Context) error {
	s.conflict = true
	return nil
}

type publicationFinalityRPC struct{ header *types.Header }

func (r publicationFinalityRPC) CallContext(_ context.Context, out any, _ string, _ ...any) error {
	raw, err := json.Marshal(r.header)
	if err != nil {
		return err
	}
	var obj map[string]any
	if err = json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	obj["hash"] = r.header.Hash().Hex()
	raw, err = json.Marshal(obj)
	if err == nil {
		*out.(*json.RawMessage) = raw
	}
	return err
}

func TestPublicationSingleStepMustNotPublishAfterPermanentFence(t *testing.T) {
	ctx := context.Background()
	ix, chain, store, _ := fixture()
	chain.head = 2
	chain.headers[2].Difficulty = big.NewInt(1)
	anchorStore := &publicationAnchorStore{}
	reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[2]}, Store: anchorStore}
	ix.Finality = reader
	chain.afterFilter = func() {
		if err := reader.Fence(ctx); err != nil {
			t.Fatal(err)
		}
	}
	progress, err := ix.Step(ctx)
	if !anchorStore.conflict {
		t.Fatalf("fixture did not persist fence: err=%v", err)
	}
	if len(store.applied) != 0 || progress || err == nil {
		t.Fatalf("single block published after permanent finalized fence: progress=%v err=%v applied=%d", progress, err, len(store.applied))
	}
}

type publicationChain struct {
	*fakeChain
	afterFinality func()
}

func (c *publicationChain) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if n == nil && c.afterFinality != nil {
		f := c.afterFinality
		c.afterFinality = nil
		f()
	}
	return c.fakeChain.HeaderByNumber(ctx, n)
}

func TestPublicationSingleStepMustNotRollbackFinalizedHistoryAfterFence(t *testing.T) {
	ctx := context.Background()
	ix, chain, store, _ := fixture()
	chain.head = 4
	chain.headers[4].Difficulty = big.NewInt(1)
	for n := uint64(2); n <= 4; n++ {
		store.blocks[n] = block(chain.headers[n])
	}
	anchorStore := &publicationAnchorStore{}
	reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[4]}, Store: anchorStore}
	ix.Finality = reader
	ix.Chain = &publicationChain{fakeChain: chain, afterFinality: func() {
		// The finalized provider changes after Check, and an API read observes and persists the conflict.
		fork := types.CopyHeader(chain.headers[4])
		fork.Extra = []byte("conflicting finalized fork")
		reader.RPC = publicationFinalityRPC{fork}
		if got := reader.Read(ctx); got.Finalized.Status != "inconsistent" {
			t.Fatalf("no finality conflict: %+v", got)
		}
		chain.headers[4] = fork
	}}
	progress, err := ix.Step(ctx)
	if !anchorStore.conflict {
		t.Fatal("fixture did not persist fence")
	}
	if len(store.rollbacks) != 0 || len(store.applied) != 0 || progress || err == nil {
		cp, _ := store.Checkpoint(ctx)
		old := common.Hash{}
		if anchorStore.anchor != nil {
			old = anchorStore.anchor.Hash
		}
		t.Fatalf("finalized history changed after fence: progress=%v err=%v rollbacks=%v applied=%d checkpointHash=%s acceptedFinalizedHash=%s", progress, err, store.rollbacks, len(store.applied), cp.Hash, old)
	}
}

// An API read completes while Step is paused in network I/O. The persisted
// conflict must prevent every subsequent store mutation on all execution paths.
func TestPublicationAPIConflictDuringSync(t *testing.T) {
	for _, mode := range []string{"single", "range", "batch_fallback", "near_head"} {
		for _, write := range []string{"apply", "rollback_fork", "rollback_head_regression"} {
			t.Run(mode+"/"+write, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				ix, chain, store, _ := fixture()
				for _, h := range chain.headers {
					h.Difficulty = big.NewInt(0)
				}
				chain.head = 5
				anchorStore := &publicationAnchorStore{}
				reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[5]}, Store: anchorStore}
				ix.Finality = reader
				if mode != "single" {
					rc := &rangeChain{fakeChain: chain}
					ix.Chain = rc
					ix.Store = &rangeStore{fakeStore: store}
					ix.BatchSize = 4
					if mode == "batch_fallback" {
						rc.headersErr = ErrBatchUnavailable
					}
					if mode == "near_head" {
						store.blocks[4] = block(chain.headers[4])
					}
				}
				if write != "apply" {
					for n := uint64(2); n <= 5; n++ {
						store.blocks[n] = block(chain.headers[n])
					}
				}
				observe := func() {
					fork := types.CopyHeader(chain.headers[5])
					fork.Extra = []byte("conflicting finalized fork")
					reader.RPC = publicationFinalityRPC{fork}
					done := make(chan finality.Snapshot, 1)
					go func() { done <- reader.Read(ctx) }()
					select {
					case got := <-done:
						if got.Finalized.Status != "inconsistent" {
							t.Fatalf("API missed conflict: %+v", got)
						}
					case <-ctx.Done():
						t.Fatal("publication lock held during network I/O")
					}
					if write == "rollback_fork" {
						chain.headers[5] = fork
					}
					if write == "rollback_head_regression" {
						chain.head = 4
					}
				}
				if write == "apply" {
					chain.afterFilter = observe
					if rc, ok := ix.Chain.(*rangeChain); ok {
						rc.afterRange = observe
					}
				} else {
					if rc, ok := ix.Chain.(*rangeChain); ok {
						ix.Chain = &publicationRangeChain{rangeChain: rc, afterFinality: observe}
					} else {
						ix.Chain = &publicationChain{fakeChain: chain, afterFinality: observe}
					}
				}
				progress, err := ix.Step(ctx)
				if !anchorStore.conflict || !errors.Is(err, finality.ErrFinalityConflict) || progress || len(store.rollbacks) != 0 || len(store.applied) != 0 {
					t.Fatalf("publication escaped fence: persisted=%v progress=%v error=%v rollback=%v applied=%d", anchorStore.conflict, progress, err, store.rollbacks, len(store.applied))
				}
			})
		}
	}
}

func TestPublicationRollbackCannotCrossAcceptedAnchorWithoutAPIRead(t *testing.T) {
	for _, kind := range []string{"fork", "head_regression"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			ix, chain, store, _ := fixture()
			chain.head = 4
			chain.headers[4].Difficulty = big.NewInt(0)
			for n := uint64(2); n <= 4; n++ {
				store.blocks[n] = block(chain.headers[n])
			}
			anchors := &publicationAnchorStore{}
			reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[4]}, Store: anchors}
			ix.Finality = reader
			ix.Chain = &publicationChain{fakeChain: chain, afterFinality: func() {
				if kind == "fork" {
					fork := types.CopyHeader(chain.headers[4])
					fork.Extra = []byte("fork after Check")
					chain.headers[4] = fork
					reader.RPC = publicationFinalityRPC{fork}
				} else {
					chain.head = 3
				}
			}}
			progress, err := ix.Step(ctx)
			wantConflict := kind == "fork"
			if progress || err == nil || errors.Is(err, finality.ErrFinalityConflict) != wantConflict || anchors.conflict != wantConflict || len(store.rollbacks) != 0 || len(store.applied) != 0 {
				t.Fatalf("accepted anchor overwritten: progress=%v err=%v fenced=%v rollback=%v applied=%d", progress, err, anchors.conflict, store.rollbacks, len(store.applied))
			}

			if kind == "head_regression" {
				chain.head = 4
				progress, err = ix.Step(ctx)
				if err != nil || progress || anchors.conflict {
					t.Fatalf("temporary lag permanently fenced indexing: progress=%v err=%v fenced=%v", progress, err, anchors.conflict)
				}
			}
		})
	}
}

type publicationRangeChain struct {
	*rangeChain
	afterFinality func()
}

func (c *publicationRangeChain) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if n == nil && c.afterFinality != nil {
		f := c.afterFinality
		c.afterFinality = nil
		f()
	}
	return c.rangeChain.HeaderByNumber(ctx, n)
}

type creatingTreeDecoder struct{}

func (creatingTreeDecoder) Decode(b model.Block, logs []types.Log) (model.Batch, error) {
	return model.Batch{Block: b, Logs: logs, CreatedTrees: []uint64{1}}, nil
}
func TestPublicationFenceDuringSQLiteRootValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ix, chain, _, _ := fixture()
	chain.head = 2
	chain.headers[2].Difficulty = big.NewInt(0)
	disk, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "root-fence.sqlite"), ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	anchors := &publicationAnchorStore{}
	reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[2]}, Store: anchors}
	ix.Finality = reader
	ix.Store = disk
	ix.Decoder = creatingTreeDecoder{}
	fenced := make(chan error, 1)
	called := false
	blocked := false
	// Use an independent anchor store so this specifically detects a permit held
	// across the validator's network callback, rather than a database writer wait.
	disk.SetRootValidator(func(context.Context, model.Root) error {
		called = true
		go func() { fenced <- reader.Fence(ctx) }()
		select {
		case err := <-fenced:
			return err
		case <-time.After(200 * time.Millisecond):
			blocked = true
			return errors.New("publication permit held during root validation")
		}
	})
	progress, err := ix.Step(ctx)
	if blocked {
		<-fenced
		t.Fatal("root validation could not fence while indexing held the publication permit")
	}
	if !called || progress || !errors.Is(err, finality.ErrFinalityConflict) {
		t.Fatalf("root validator fence did not reject publication: called=%v progress=%v err=%v", called, progress, err)
	}
	cp, err := disk.Checkpoint(ctx)
	if err != nil || cp != nil {
		t.Fatalf("fenced block committed: checkpoint=%+v error=%v", cp, err)
	}
}

func TestPublicationSQLiteRootValidationFencesSameDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ix, chain, _, _ := fixture()
	chain.head = 2
	chain.headers[2].Difficulty = big.NewInt(0)
	disk, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "shared-fence.sqlite"), ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[2]}, Store: disk}
	ix.Finality = reader
	ix.Store = disk
	ix.Decoder = creatingTreeDecoder{}
	fenced := make(chan error, 1)
	observed := make(chan error, 1)
	disk.SetRootValidator(func(context.Context, model.Root) error {
		// Fence sets its memory bit before its persistent write waits for this
		// transaction. The validator can observe the bit and let precommit abort.
		go func() { fenced <- reader.Fence(ctx) }()
		go func() {
			for {
				release, err := reader.AcquirePublication(ctx)
				if err != nil {
					observed <- err
					return
				}
				release()
				runtime.Gosched()
			}
		}()
		select {
		case err := <-observed:
			if errors.Is(err, finality.ErrFinalityConflict) {
				return nil
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	progress, err := ix.Step(ctx)
	if errFence := <-fenced; errFence != nil {
		t.Fatal(errFence)
	}
	if progress || !errors.Is(err, finality.ErrFinalityConflict) {
		t.Fatalf("same-database fence failed: progress=%v err=%v", progress, err)
	}
	cp, err := disk.Checkpoint(ctx)
	if err != nil || cp != nil {
		t.Fatalf("fenced block committed: checkpoint=%+v error=%v", cp, err)
	}
	durable, err := disk.FinalityConflict(ctx)
	if err != nil || !durable {
		t.Fatalf("fence not persisted after writer rolled back: fenced=%v err=%v", durable, err)
	}
}

func TestPublicationSingleApplyCannotReplaceAcceptedFinalizedBlock(t *testing.T) {
	ctx := context.Background()
	ix, chain, store, _ := fixture()
	chain.head = 4
	chain.headers[4].Difficulty = big.NewInt(0)
	for n := uint64(2); n <= 3; n++ {
		store.blocks[n] = block(chain.headers[n])
	}
	anchors := &publicationAnchorStore{}
	reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[4]}, Store: anchors}
	ix.Finality = reader
	ix.Chain = &publicationChain{fakeChain: chain, afterFinality: func() {
		fork := types.CopyHeader(chain.headers[4])
		fork.Extra = []byte("fork at finalized after Check")
		chain.headers[4] = fork
		reader.RPC = publicationFinalityRPC{fork}
	}}
	progress, err := ix.Step(ctx)
	if progress || !errors.Is(err, finality.ErrFinalityConflict) || !anchors.conflict || len(store.applied) != 0 {
		t.Fatalf("conflicting finalized block published directly: progress=%v err=%v fenced=%v applied=%d", progress, err, anchors.conflict, len(store.applied))
	}
}

func TestPublicationSQLiteRollbackRechecksDurableFinalizedBound(t *testing.T) {
	for _, kind := range []string{"fork", "temporary_head_lag"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			ix, chain, _, _ := fixture()
			chain.head = 4
			for _, h := range chain.headers {
				h.Difficulty = big.NewInt(0)
			}
			disk, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "rollback-finality.sqlite"), ix.Deployment)
			if err != nil {
				t.Fatal(err)
			}
			defer disk.Close()
			for n := uint64(2); n <= 4; n++ {
				h := chain.headers[n]
				if err := disk.Apply(ctx, model.Batch{Block: block(h), Header: types.CopyHeader(h)}); err != nil {
					t.Fatal(err)
				}
			}
			reader := &finality.Reader{RPC: publicationFinalityRPC{chain.headers[4]}, Store: disk}
			ix.Finality = reader
			ix.Store = disk
			ix.Chain = &publicationChain{fakeChain: chain, afterFinality: func() {
				if kind == "fork" {
					fork := types.CopyHeader(chain.headers[4])
					fork.Extra = []byte("fork after initial Check")
					chain.headers[4] = fork
					reader.RPC = publicationFinalityRPC{fork}
				} else {
					chain.head = 3
				}
			}}
			progress, err := ix.Step(ctx)
			durable, readErr := disk.FinalityConflict(ctx)
			wantConflict := kind == "fork"
			if readErr != nil || err == nil || progress || durable != wantConflict || errors.Is(err, finality.ErrFinalityConflict) != wantConflict {
				t.Fatalf("durable-bound finality recheck lost: progress=%v err=%v durable=%v readError=%v", progress, err, durable, readErr)
			}
			cp, err := disk.Checkpoint(ctx)
			if err != nil || cp == nil || cp.Number != 4 {
				t.Fatalf("guarded rollback changed history: checkpoint=%+v error=%v", cp, err)
			}
			if kind == "temporary_head_lag" {
				chain.head = 4
				if progress, err := ix.Step(ctx); err != nil || progress {
					t.Fatalf("temporary lag could not recover: progress=%v err=%v", progress, err)
				}
			}
		})
	}
}
