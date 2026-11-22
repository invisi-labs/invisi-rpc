package indexer

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

type advanceRPC struct {
	mu          sync.Mutex
	headers     map[string]*types.Header
	calls       int
	onFinalized func(int)
}

func (r *advanceRPC) CallContext(ctx context.Context, out any, method string, args ...any) error {
	tag := args[0].(string)
	r.mu.Lock()
	var hook func(int)
	var n int
	if tag == "finalized" {
		r.calls++
		n = r.calls
		hook = r.onFinalized
	}
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	r.mu.Lock()
	h := r.headers[tag]
	r.mu.Unlock()
	return (publicationFinalityRPC{h}).CallContext(ctx, out, method, args...)
}
func (r *advanceRPC) setHead(h *types.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers["finalized"], r.headers["safe"], r.headers["latest"] = h, h, h
	r.headers["0x"+h.Number.Text(16)] = h
}
func newAdvanceFixture(t *testing.T, through uint64) (*Indexer, *fakeChain, *storage.SQLiteStore, *advanceRPC, *finality.Reader) {
	t.Helper()
	ctx := context.Background()
	ix, chain, _, _ := fixture()
	chain.head = 5
	for _, h := range chain.headers {
		h.Difficulty = big.NewInt(0)
	}
	disk, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "advance.sqlite"), ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disk.Close)
	for n := uint64(2); n <= through; n++ {
		h := chain.headers[n]
		if err := disk.Apply(ctx, model.Batch{Block: block(h), Header: types.CopyHeader(h)}); err != nil {
			t.Fatal(err)
		}
	}
	rpc := &advanceRPC{headers: map[string]*types.Header{}}
	for n, h := range chain.headers {
		rpc.headers["0x"+new(big.Int).SetUint64(n).Text(16)] = h
	}
	rpc.setHead(chain.headers[through])
	reader := &finality.Reader{RPC: rpc, Store: disk}
	ix.Store, ix.Finality = disk, reader
	return ix, chain, disk, rpc, reader
}

func TestConcurrentNativeAdvanceCannotCommitOldBranch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ix, chain, disk, rpc, reader := newAdvanceFixture(t, 3)
	chain.head = 4
	if err := reader.Check(ctx); err != nil {
		t.Fatal(err)
	}
	rpc.calls = 0
	fork := types.CopyHeader(chain.headers[4])
	fork.Extra = []byte("native finalized replacement")
	done := make(chan finality.Snapshot, 1)
	rpc.onFinalized = func(n int) {
		if n != 2 {
			return
		}
		rpc.setHead(fork)
		chain.headers[4] = fork
		go func() { done <- reader.Read(ctx) }()
		select {
		case got := <-done:
			if got.Finalized.Status != "available" {
				t.Errorf("native head lost: %+v", got)
			}
		case <-ctx.Done():
			t.Error("concurrent native read blocked on writer")
		}
	}
	progress, err := ix.Step(ctx)
	if progress || err == nil {
		t.Fatalf("old branch committed: %v %v", progress, err)
	}
	cp, e := disk.Checkpoint(ctx)
	if e != nil || cp == nil || cp.Number != 3 {
		t.Fatalf("old branch escaped: %+v %v", cp, e)
	}
	fenced, e := disk.FinalityConflict(ctx)
	if e != nil || fenced {
		t.Fatalf("ordinary advance permanently fenced: %v %v", fenced, e)
	}
	rpc.onFinalized = nil
	if progress, err := ix.Step(ctx); err != nil || !progress {
		t.Fatalf("replacement could not progress: %v %v", progress, err)
	}
	if err := reader.Check(ctx); err != nil {
		t.Fatal(err)
	}
	anchor, e := disk.FinalizedAnchor(ctx)
	if e != nil || anchor == nil || anchor.Hash != fork.Hash() {
		t.Fatalf("replacement not accepted: %+v %v", anchor, e)
	}
}

func TestUnacceptedNativeRangeAdvanceDoesNotFence(t *testing.T) {
	ctx := context.Background()
	ix, chain, disk, rpc, reader := newAdvanceFixture(t, 2)
	if err := reader.Check(ctx); err != nil {
		t.Fatal(err)
	}
	rpc.setHead(chain.headers[5])
	rpc.calls = 0
	fork := types.CopyHeader(chain.headers[5])
	fork.Extra = []byte("new native head sharing indexed prefix")
	rpc.onFinalized = func(n int) {
		if n == 3 {
			rpc.setHead(fork)
			chain.headers[5] = fork
		}
	}
	ix.Chain = &rangeChain{fakeChain: chain}
	ix.BatchSize = 2
	progress, err := ix.Step(ctx)
	fenced, e := disk.FinalityConflict(ctx)
	if err != nil || !progress || e != nil || fenced {
		t.Fatalf("unaccepted native head change fenced range: progress=%v err=%v fenced=%v read=%v", progress, err, fenced, e)
	}
	cp, e := disk.Checkpoint(ctx)
	if e != nil || cp == nil || cp.Number != 4 {
		t.Fatalf("range failed to advance: %+v %v", cp, e)
	}
}

func TestPublicationValidatesAnchorAdvancedBySecondSQLiteReader(t *testing.T) {
	ctx := context.Background()
	ix, chain, _, _ := fixture()
	chain.head = 4
	for _, h := range chain.headers {
		h.Difficulty = big.NewInt(0)
	}
	path := filepath.Join(t.TempDir(), "two-readers.sqlite")
	disk, err := storage.OpenSQLite(ctx, path, ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	other, err := storage.OpenSQLite(ctx, path, ix.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for n := uint64(2); n <= 3; n++ {
		h := chain.headers[n]
		if err := disk.Apply(ctx, model.Batch{Block: block(h), Header: h}); err != nil {
			t.Fatal(err)
		}
	}
	rpc := &advanceRPC{headers: map[string]*types.Header{"0x2": chain.headers[2], "0x3": chain.headers[3], "0x4": chain.headers[4]}}
	rpc.setHead(chain.headers[2])
	reader := &finality.Reader{RPC: rpc, Store: disk}
	ix.Finality, ix.Store = reader, disk
	otherRPC := &advanceRPC{headers: map[string]*types.Header{"0x2": chain.headers[2], "0x3": chain.headers[3], "0x4": chain.headers[4]}}
	otherRPC.setHead(chain.headers[3])
	second := &finality.Reader{RPC: otherRPC, Store: other}
	chain.afterFilter = func() {
		if err := second.Check(ctx); err != nil {
			t.Fatal(err)
		}
		fork := types.CopyHeader(chain.headers[3])
		fork.Extra = []byte("true accepted-history conflict")
		rpc.setHead(fork)
	}
	progress, err := ix.Step(ctx)
	fenced, e := disk.FinalityConflict(ctx)
	cp, cpErr := disk.Checkpoint(ctx)
	if progress || !errors.Is(err, model.ErrFinalityConflict) || e != nil || !fenced || cpErr != nil || cp == nil || cp.Number != 3 {
		t.Fatalf("new durable anchor skipped: progress=%v err=%v fenced=%v read=%v cp=%+v cpErr=%v", progress, err, fenced, e, cp, cpErr)
	}
}
