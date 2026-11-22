package indexer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

func TestWrongAnchorHeightRetriesAndRecovers(t *testing.T) {
	for _, mode := range []string{"single", "range"} {
		for _, wrongHeight := range []uint64{2, 4} {
			t.Run(fmt.Sprintf("%s/height_%d", mode, wrongHeight), func(t *testing.T) {
				ctx := context.Background()
				ix, chain, disk, rpc, reader := newAdvanceFixture(t, 3)
				if err := reader.Check(ctx); err != nil {
					t.Fatal(err)
				}
				original := chain.headers[3]
				rpc.setHead(chain.headers[5])
				rpc.calls = 0
				guardCall, wantHead := 2, uint64(4)
				if mode == "range" {
					ix.BatchSize = 2
					ix.Chain = &rangeChain{fakeChain: chain}
					guardCall, wantHead = 3, 5
				}
				// Change the response after admission checks, when the commit guard
				// revalidates accepted anchor 3. The wrong header is otherwise valid.
				rpc.onFinalized = func(call int) {
					if call == guardCall {
						rpc.mu.Lock()
						rpc.headers["0x3"] = chain.headers[wrongHeight]
						rpc.mu.Unlock()
					}
				}
				progress, err := ix.Step(ctx)
				if progress || !errors.Is(err, model.ErrFinalityChanged) || errors.Is(err, model.ErrFinalityConflict) {
					t.Fatalf("wrong-height evidence was not retryable: progress=%v err=%v", progress, err)
				}
				assertAnchorHeightState(t, disk, 3, block(original))

				rpc.onFinalized = nil
				rpc.mu.Lock()
				rpc.headers["0x3"] = original
				rpc.mu.Unlock()
				progress, err = ix.Step(ctx)
				if err != nil || !progress {
					t.Fatalf("restored upstream did not resume indexing: progress=%v err=%v", progress, err)
				}
				assertAnchorHeightState(t, disk, wantHead, block(original))
			})
		}
	}
}

func assertAnchorHeightState(t *testing.T, store storage.Backend, height uint64, wantAnchor model.Block) {
	t.Helper()
	ctx := context.Background()
	head, err := store.Checkpoint(ctx)
	if err != nil || head == nil || head.Number != height {
		t.Fatalf("unexpected indexed head: %+v err=%v", head, err)
	}
	anchor, err := store.FinalizedAnchor(ctx)
	if err != nil || anchor == nil || *anchor != wantAnchor {
		t.Fatalf("accepted anchor changed: %+v err=%v", anchor, err)
	}
	fenced, err := store.FinalityConflict(ctx)
	if err != nil || fenced {
		t.Fatalf("temporary upstream failure permanently fenced indexing: fenced=%v err=%v", fenced, err)
	}
}
