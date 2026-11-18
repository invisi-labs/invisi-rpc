package finality

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

func TestRangeFinalityGuardAndDurableFence(t *testing.T) {
	ctx := context.Background()
	r, f, s := testReader()
	anchor, e := r.Finalized(ctx)
	if e != nil || anchor == nil || anchor.Number != 6 {
		t.Fatal("capture", e)
	}
	if e = r.ValidateAnchor(ctx, *anchor); e != nil {
		t.Fatal(e)
	}
	f.headers["finalized"] = f.headers["0x8"]
	if e = r.ValidateAnchor(ctx, *anchor); e != nil {
		t.Fatal("advancing finality rejected", e)
	}
	old := f.headers["0x6"]
	copy := *old
	copy.Extra = []byte{1}
	f.headers["0x6"] = &copy
	if e = r.ValidateAnchor(ctx, *anchor); !errors.Is(e, ErrFinalityConflict) {
		t.Fatal("changed anchor accepted", e)
	}
	if s.conflicted {
		t.Fatal("read-only guard mutated store")
	}
	if e = r.Fence(ctx); e != nil {
		t.Fatal(e)
	}
	if !s.conflicted {
		t.Fatal("conflict not durable")
	}
	f.headers["0x6"] = old
	if e = r.ValidateAnchor(ctx, *anchor); !errors.Is(e, ErrFinalityConflict) {
		t.Fatal("guard ignored in-memory conflict")
	}
}
func TestRangeFinalityUnavailableAndRegressed(t *testing.T) {
	ctx := context.Background()
	r, f, _ := testReader()
	anchor, e := r.Finalized(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.headers["finalized"] = f.headers["0x5"]
	if e = r.ValidateAnchor(ctx, *anchor); e == nil {
		t.Fatal("regression accepted")
	}
	delete(f.headers, "finalized")
	if e = r.ValidateAnchor(ctx, *anchor); e == nil {
		t.Fatal("missing tag accepted")
	}
	f.headers["finalized"] = &types.Header{Number: big.NewInt(6), Difficulty: big.NewInt(0), Time: 6, Extra: common.HexToHash("3").Bytes()}
	if e = r.ValidateAnchor(ctx, *anchor); !errors.Is(e, ErrFinalityConflict) {
		t.Fatal("conflicting finalized tag accepted", e)
	}
}

type blockedAnchor struct {
	memoryAnchor
	started chan struct{}
	release chan struct{}
	block   bool
}

func (s *blockedAnchor) SaveFinalizedAnchor(ctx context.Context, b model.Block) error {
	if s.block {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.memoryAnchor.SaveFinalizedAnchor(ctx, b)
}
func TestRangeGuardDoesNotWaitForReaderHoldingStoreWriter(t *testing.T) {
	r, f, _ := testReader()
	store := &blockedAnchor{started: make(chan struct{}), release: make(chan struct{})}
	r.Store = store
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	anchor, e := r.Finalized(ctx)
	if e != nil {
		t.Fatal(e)
	}
	store.block = true
	f.headers["finalized"] = f.headers["0x8"]
	done := make(chan error, 1)
	go func() { _, e := r.Finalized(ctx); done <- e }()
	<-store.started
	guarded := make(chan error, 1)
	go func() { guarded <- r.ValidateAnchor(ctx, *anchor) }()
	select {
	case e := <-guarded:
		if e != nil {
			t.Error(e)
		}
	case <-ctx.Done():
		t.Error("guard waited for Reader.mu while reader waited for DB")
	}
	close(store.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestRangePublicationPermitSerializesFences(t *testing.T) {
	r, _, store := testReader()
	ctx := context.Background()
	if _, e := r.Finalized(ctx); e != nil {
		t.Fatal(e)
	}
	release, e := r.AcquirePublication(ctx)
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- r.Fence(ctx) }()
	<-started
	select {
	case <-done:
		t.Fatal("fence crossed an active publication permit")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if !store.conflicted {
		t.Fatal("fence was not persisted after publisher finished")
	}
	if release, e = r.AcquirePublication(ctx); !errors.Is(e, ErrFinalityConflict) {
		if release != nil {
			release()
		}
		t.Fatal("publication allowed after fence", e)
	}
}
