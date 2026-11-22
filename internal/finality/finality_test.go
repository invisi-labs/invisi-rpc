package finality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type fakeRPC struct {
	headers  map[string]*types.Header
	failures map[string]error
	corrupt  string
}

func (f *fakeRPC) CallContext(_ context.Context, out any, _ string, args ...any) error {
	tag := args[0].(string)
	if err := f.failures[tag]; err != nil {
		return err
	}
	h := f.headers[tag]
	if h == nil {
		return nil
	}
	data, _ := json.Marshal(h)
	if tag == f.corrupt {
		var obj map[string]any
		json.Unmarshal(data, &obj)
		obj["hash"] = "0x0000000000000000000000000000000000000000000000000000000000000001"
		data, _ = json.Marshal(obj)
	}
	return json.Unmarshal(data, out)
}

type rpcError struct{}

func (rpcError) Error() string  { return "invalid block tag" }
func (rpcError) ErrorCode() int { return -32602 }

type memoryAnchor struct {
	block      *model.Block
	conflicted bool
}

func (s *memoryAnchor) FinalizedAnchor(context.Context) (*model.Block, error) { return s.block, nil }
func (s *memoryAnchor) SaveFinalizedAnchor(_ context.Context, b model.Block) error {
	s.block = &b
	return nil
}
func testReader() (*Reader, *fakeRPC, *memoryAnchor) {
	f := &fakeRPC{headers: map[string]*types.Header{}, failures: map[string]error{}}
	for n := 0; n <= 10; n++ {
		h := &types.Header{Number: big.NewInt(int64(n)), Difficulty: big.NewInt(0), Time: uint64(n)}
		f.headers[fmt.Sprintf("0x%x", n)] = h
	}
	f.headers["latest"] = f.headers["0xa"]
	f.headers["safe"] = f.headers["0x8"]
	f.headers["finalized"] = f.headers["0x6"]
	s := &memoryAnchor{}
	return &Reader{RPC: f, Store: s}, f, s
}
func TestNativeAvailabilityAndIndexIndependence(t *testing.T) {
	r, f, _ := testReader()
	f.failures["safe"] = rpcError{}
	f.failures["finalized"] = errors.New("timeout")
	got := r.Read(context.Background())
	if got.Latest.Status != "available" || got.Safe.Status != "unsupported" || got.Finalized.Status != "unavailable" {
		t.Fatalf("%+v", got)
	}
	if err := r.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidAndOrderedHeads(t *testing.T) {
	for _, kind := range []string{"hash", "order"} {
		t.Run(kind, func(t *testing.T) {
			r, f, _ := testReader()
			if kind == "hash" {
				f.corrupt = "safe"
			} else {
				f.headers["safe"] = f.headers["0x2"]
			}
			got := r.Read(context.Background())
			if got.Safe.Status != "inconsistent" {
				t.Fatalf("%+v", got)
			}
		})
	}
}
func TestDurableFinalizedConflictAndStall(t *testing.T) {
	r, f, s := testReader()
	got := r.Read(context.Background())
	if got.Finalized.Status != "available" || s.block == nil {
		t.Fatal(got)
	}
	if got = r.Read(context.Background()); got.Finalized.Status != "available" {
		t.Fatal(got)
	}
	r = &Reader{RPC: f, Store: s}
	f.headers["0x6"] = &types.Header{Number: big.NewInt(6), Difficulty: big.NewInt(0), Extra: []byte{1}}
	if err := r.Check(context.Background()); err == nil {
		t.Fatal("accepted changed finalized history")
	}
	if got = r.Read(context.Background()); got.Finalized.Status != "inconsistent" || got.Finalized.Block != nil {
		t.Fatal(got)
	}
}
func TestNullIsUnavailable(t *testing.T) {
	r, f, _ := testReader()
	delete(f.headers, "safe")
	if got := r.Read(context.Background()); got.Safe.Status != "unavailable" {
		t.Fatal(got)
	}
}

func TestStalledFinalityAndAdvancingLatest(t *testing.T) {
	r, f, _ := testReader()
	if got := r.Read(context.Background()); got.Finalized.Block.Number != 6 {
		t.Fatal(got)
	}
	f.headers["latest"] = f.headers["0x9"]
	got := r.Read(context.Background())
	if got.Latest.Status != "available" || got.Latest.Block.Number != 9 || got.Finalized.Status != "available" || got.Finalized.Block.Number != 6 {
		t.Fatal(got)
	}
}
func TestMalformedNumberAndCanonicalDisagreement(t *testing.T) {
	for _, kind := range []string{"negative", "canonical", "sameHeight"} {
		t.Run(kind, func(t *testing.T) {
			r, f, _ := testReader()
			switch kind {
			case "negative":
				f.headers["safe"] = &types.Header{Number: big.NewInt(-1), Difficulty: big.NewInt(0)}
			case "canonical":
				f.headers["0x8"] = &types.Header{Number: big.NewInt(8), Difficulty: big.NewInt(0), Time: 999}
			case "sameHeight":
				f.headers["safe"] = &types.Header{Number: big.NewInt(10), Difficulty: big.NewInt(0), Time: 999}
			}
			if got := r.Read(context.Background()); got.Safe.Status != "inconsistent" || got.Safe.Block != nil {
				t.Fatal(got)
			}
		})
	}
}
func TestAnchorUnverifiableAndRegressingFinality(t *testing.T) {
	for _, kind := range []string{"transport", "regression"} {
		t.Run(kind, func(t *testing.T) {
			r, f, s := testReader()
			r.Read(context.Background())
			saved := *s.block
			if kind == "transport" {
				f.failures["0x6"] = errors.New("timeout")
			} else {
				f.headers["finalized"] = f.headers["0x5"]
			}
			if err := r.Check(context.Background()); err == nil {
				t.Fatal("unsafe index allowed")
			}
			if *s.block != saved {
				t.Fatal("anchor changed")
			}
		})
	}
}

func (s *memoryAnchor) FinalityConflict(context.Context) (bool, error) { return s.conflicted, nil }
func (s *memoryAnchor) MarkFinalityConflict(context.Context) error     { s.conflicted = true; return nil }
func TestConflictFenceSurvivesProviderRecoveryAndRestart(t *testing.T) {
	r, f, s := testReader()
	r.Read(context.Background())
	original := f.headers["0x6"]
	f.headers["0x6"] = &types.Header{Number: big.NewInt(6), Difficulty: big.NewInt(0), Extra: []byte{1}}
	if err := r.Check(context.Background()); err == nil {
		t.Fatal("expected fence")
	}
	f.headers["0x6"] = original
	r = &Reader{RPC: f, Store: s}
	if err := r.Check(context.Background()); err == nil {
		t.Fatal("fence silently cleared")
	}
	if got := r.Read(context.Background()); got.Finalized.Status != "inconsistent" {
		t.Fatal(got)
	}
}

func TestNativeFinalizedTagConflictingWithAcceptedAnchorFences(t *testing.T) {
	r, f, s := testReader()
	r.Read(context.Background())
	f.headers["finalized"] = &types.Header{Number: big.NewInt(6), Difficulty: big.NewInt(0), Extra: []byte{1}}
	if err := r.Check(context.Background()); !errors.Is(err, ErrFinalityConflict) {
		t.Fatalf("conflict not fenced: %v", err)
	}
	if !s.conflicted {
		t.Fatal("missing durable fence")
	}
}

func TestLaggingProviderDoesNotPermanentlyFence(t *testing.T) {
	for _, tag := range []string{"latest", "finalized"} {
		t.Run(tag, func(t *testing.T) {
			r, f, s := testReader()
			r.Read(context.Background())
			saved := f.headers[tag]
			f.headers[tag] = f.headers["0x5"]
			if err := r.Check(context.Background()); err == nil {
				t.Fatal("lag not paused")
			}
			if got := r.Read(context.Background()); got.Finalized.Status != "unavailable" || s.conflicted {
				t.Fatal(got, s.conflicted)
			}
			f.headers[tag] = saved
			if err := r.Check(context.Background()); err != nil {
				t.Fatal("lag caused sticky fence", err)
			}
		})
	}
}

func TestMalformedLatestPausesIndexingWithoutFence(t *testing.T) {
	r, f, s := testReader()
	f.corrupt = "latest"
	if err := r.Check(context.Background()); err == nil {
		t.Fatal("malformed latest allowed indexing")
	}
	if s.conflicted {
		t.Fatal("malformed latest created permanent fence")
	}
}

type failingFenceStore struct {
	*memoryAnchor
	fenceErr  error
	anchorErr error
}

func (s *failingFenceStore) FinalityConflict(ctx context.Context) (bool, error) {
	if s.fenceErr != nil {
		return false, s.fenceErr
	}
	return s.memoryAnchor.FinalityConflict(ctx)
}
func (s *failingFenceStore) FinalizedAnchor(ctx context.Context) (*model.Block, error) {
	if s.anchorErr != nil {
		return nil, s.anchorErr
	}
	return s.memoryAnchor.FinalizedAnchor(ctx)
}
func TestSafeHeadRespectsPermanentAndUnverifiableFence(t *testing.T) {
	for _, kind := range []string{"observed_conflict", "restart", "fence_store_error", "anchor_store_error", "memory_fence_store_error", "without_store"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r, f, s := testReader()
			r.Read(ctx)
			store := &failingFenceStore{memoryAnchor: s}
			r.Store = store
			want := "inconsistent"
			switch kind {
			case "observed_conflict", "restart":
				original := f.headers["0x6"]
				changed := types.CopyHeader(original)
				changed.Extra = []byte{9}
				f.headers["0x6"] = changed
				got := r.Read(ctx)
				if got.Safe.Status != "inconsistent" || got.Safe.Block != nil {
					t.Fatalf("safe escaped observed conflict: %+v", got)
				}
				f.headers["0x6"] = original
				if kind == "restart" {
					r = &Reader{RPC: f, Store: store}
				}
			case "fence_store_error":
				store.fenceErr = errors.New("cannot read fence")
				want = "unavailable"
			case "anchor_store_error":
				store.anchorErr = errors.New("cannot read anchor")
				want = "unavailable"
			case "memory_fence_store_error":
				if err := r.Fence(ctx); err != nil {
					t.Fatal(err)
				}
				store.fenceErr = errors.New("cannot read fence")
			case "without_store":
				r.Store = nil
				if err := r.Fence(ctx); err != nil {
					t.Fatal(err)
				}
			}
			got := r.Read(ctx)
			if got.Safe.Status != want || got.Safe.Block != nil || got.Latest.Status != "available" {
				t.Fatalf("unsafe finality snapshot: %+v want safe %s", got, want)
			}
		})
	}
}
func TestSafeOnlyNativeChainRemainsAvailable(t *testing.T) {
	r, f, _ := testReader()
	f.failures["finalized"] = rpcError{}
	got := r.Read(context.Background())
	if got.Safe.Status != "available" || got.Safe.Block == nil || got.Finalized.Status != "unsupported" {
		t.Fatal(got)
	}
}

type fenceDuringAnchorRPC struct {
	RPC
	beforeAnchor func()
	numericCalls int
}

func (f *fenceDuringAnchorRPC) CallContext(ctx context.Context, out any, method string, args ...any) error {
	if args[0] == "0x6" {
		f.numericCalls++
		if f.numericCalls == 2 {
			f.beforeAnchor()
		}
	}
	return f.RPC.CallContext(ctx, out, method, args...)
}
func TestSafeHeadObservesFenceDuringAnchorVerification(t *testing.T) {
	ctx := context.Background()
	r, f, _ := testReader()
	r.Read(ctx)
	r.RPC = &fenceDuringAnchorRPC{RPC: f, beforeAnchor: func() {
		if err := r.Fence(ctx); err != nil {
			t.Fatal(err)
		}
	}}
	got := r.Read(ctx)
	if got.Safe.Status != "inconsistent" || got.Safe.Block != nil || got.Finalized.Status != "inconsistent" {
		t.Fatalf("fence during anchor verification escaped: %+v", got)
	}
}

type afterSaveAnchor struct {
	memoryAnchor
	afterSave func()
}

func (s *afterSaveAnchor) SaveFinalizedAnchor(ctx context.Context, b model.Block) error {
	if err := s.memoryAnchor.SaveFinalizedAnchor(ctx, b); err != nil {
		return err
	}
	if s.afterSave != nil {
		s.afterSave()
	}
	return nil
}
func TestRollbackCannotEnterPersistedAnchorPublicationWindow(t *testing.T) {
	ctx := context.Background()
	r, _, _ := testReader()
	store := &afterSaveAnchor{}
	r.Store = store
	store.afterSave = func() {
		release, err := r.AcquireRollback(ctx, 5)
		if release != nil {
			release()
		}
		if !errors.Is(err, ErrFinalizedRollback) {
			t.Fatalf("rollback crossed newly persisted finalized anchor before cache publication: %v", err)
		}
	}
	if err := r.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
