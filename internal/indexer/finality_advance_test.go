package indexer

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

func TestFinalityAdvanceFromAnotherReaderCannotPublishConflictingBlock(t *testing.T) {
	ctx := context.Background()
	ix, chain, _, _ := fixture()
	chain.head = 4
	for _, h := range chain.headers {
		h.Difficulty = big.NewInt(0)
	}
	disk, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "other-reader.sqlite"), ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for n := uint64(2); n <= 3; n++ {
		h := chain.headers[n]
		if err := disk.Apply(ctx, model.Batch{Block: block(h), Header: types.CopyHeader(h)}); err != nil {
			t.Fatal(err)
		}
	}
	rpc := &advanceHeadersRPC{headers: map[string]*types.Header{"finalized": chain.headers[3], "safe": chain.headers[3], "latest": chain.headers[4], "0x3": chain.headers[3], "0x4": chain.headers[4]}}
	reader := &finality.Reader{RPC: rpc, Store: disk}
	ix.Finality, ix.Store = reader, disk
	original := block(chain.headers[4])
	ix.Chain = &publicationChain{fakeChain: chain, afterFinality: func() {
		// Another reader sharing the database accepts the next finalized header.
		if err := disk.SaveFinalizedAnchor(ctx, original); err != nil {
			t.Fatal(err)
		}
		rpc.headers["finalized"], rpc.headers["safe"], rpc.headers["latest"] = chain.headers[4], chain.headers[4], chain.headers[4]
		fork := types.CopyHeader(chain.headers[4])
		fork.Extra = []byte("alternate block after another reader finalized")
		chain.headers[4] = fork
	}}
	progress, err := ix.Step(ctx)
	cp, cpErr := disk.Checkpoint(ctx)
	anchor, anchorErr := disk.FinalizedAnchor(ctx)
	if cpErr != nil || anchorErr != nil {
		t.Fatalf("read failed: %v %v", cpErr, anchorErr)
	}
	if progress || !errors.Is(err, model.ErrFinalityChanged) || cp == nil || cp.Number != 3 || anchor == nil || *anchor != original {
		t.Fatalf("stale Reader published against durable anchor: progress=%v err=%v checkpoint=%+v anchor=%+v", progress, err, cp, anchor)
	}
	fenced, e := disk.FinalityConflict(ctx)
	if e != nil || fenced {
		t.Fatalf("stale candidate permanently fenced: %v %v", fenced, e)
	}
}

type advanceHeadersRPC struct{ headers map[string]*types.Header }

func (r *advanceHeadersRPC) CallContext(ctx context.Context, out any, method string, args ...any) error {
	return (publicationFinalityRPC{r.headers[args[0].(string)]}).CallContext(ctx, out, method, args...)
}

func TestFinalityAdvanceAllowsOrdinaryUnfinalizedReorg(t *testing.T) {
	for _, mode := range []string{"single", "range-configured", "restart"} {
		t.Run(mode, func(t *testing.T) { testFinalityReorg(t, mode) })
	}
}
func testFinalityReorg(t *testing.T, mode string) {
	ctx := context.Background()
	ix, chain, _, _ := fixture()
	chain.head = 4
	for _, h := range chain.headers {
		h.Difficulty = big.NewInt(0)
	}
	path := filepath.Join(t.TempDir(), "normal-reorg.sqlite")
	disk, err := storage.OpenSQLite(ctx, path, ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	for n := uint64(2); n <= 4; n++ {
		h := chain.headers[n]
		if err := disk.Apply(ctx, model.Batch{Block: block(h), Header: types.CopyHeader(h)}); err != nil {
			t.Fatal(err)
		}
	}
	rpc := &advanceHeadersRPC{headers: map[string]*types.Header{"finalized": chain.headers[3], "safe": chain.headers[3], "latest": chain.headers[4], "0x3": chain.headers[3], "0x4": chain.headers[4]}}
	reader := &finality.Reader{RPC: rpc, Store: disk}
	if err := reader.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if mode == "restart" {
		disk.Close()
		disk, err = storage.OpenSQLite(ctx, path, ix.Deployment)
		if err != nil {
			t.Fatal(err)
		}
		reader = &finality.Reader{RPC: rpc, Store: disk}
	}
	if mode == "range-configured" {
		ix.BatchSize = 2
		ix.Chain = &rangeChain{fakeChain: chain}
	}
	// Only block 3 was accepted as finalized. Replace optimistic 4, then finalize
	// that replacement, keeping all previously accepted finalized history intact.
	fork := types.CopyHeader(chain.headers[4])
	fork.Extra = []byte("legitimate unfinalized reorg")
	chain.headers[4] = fork
	rpc.headers["finalized"], rpc.headers["safe"], rpc.headers["latest"], rpc.headers["0x4"] = fork, fork, fork, fork
	ix.Finality, ix.Store = reader, disk
	for attempt := 1; attempt <= 2; attempt++ {
		progress, stepErr := ix.Step(ctx)
		cp, cpErr := disk.Checkpoint(ctx)
		anchor, anchorErr := disk.FinalizedAnchor(ctx)
		fenced, fenceErr := disk.FinalityConflict(ctx)
		if cpErr != nil || anchorErr != nil || fenceErr != nil {
			t.Fatalf("read error: %v %v %v", cpErr, anchorErr, fenceErr)
		}
		if stepErr != nil || fenced || cp == nil || cp.Hash != fork.Hash() {
			t.Errorf("attempt %d cannot reconcile normal reorg: progress=%v err=%v indexed=%+v finalized=%+v fenced=%v", attempt, progress, stepErr, cp, anchor, fenced)
		}
	}
	anchor, e := disk.FinalizedAnchor(ctx)
	if e != nil || anchor == nil || anchor.Hash != fork.Hash() {
		t.Fatalf("replacement anchor not accepted: %+v %v", anchor, e)
	}
}
