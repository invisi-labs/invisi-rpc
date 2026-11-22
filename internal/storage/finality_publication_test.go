package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

func TestCheckedAnchorRequiresMatchingIndexedSnapshot(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend)
			checked := s.(interface {
				SaveFinalizedAnchorAt(context.Context, model.Block, *model.Block) error
			})
			b10 := model.Block{Number: 10, Hash: common.HexToHash("10")}
			b11 := model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: b10.Hash}
			if err := s.Apply(ctx, model.Batch{Block: b10}); err != nil {
				t.Fatal(err)
			}
			if err := checked.SaveFinalizedAnchorAt(ctx, b11, &b10); !errors.Is(err, model.ErrFinalityChanged) {
				t.Fatalf("future candidate accepted: %v", err)
			}
			if err := checked.SaveFinalizedAnchorAt(ctx, b10, &b10); err != nil {
				t.Fatal(err)
			}
			if err := s.Apply(ctx, model.Batch{Block: b11}); err != nil {
				t.Fatal(err)
			}
			if err := checked.SaveFinalizedAnchorAt(ctx, b11, &b10); !errors.Is(err, model.ErrFinalityChanged) {
				t.Fatalf("stale snapshot accepted: %v", err)
			}
			wrong := b11
			wrong.Hash = common.HexToHash("bad")
			if err := checked.SaveFinalizedAnchorAt(ctx, wrong, &b11); !errors.Is(err, model.ErrFinalityChanged) {
				t.Fatalf("other indexed branch accepted: %v", err)
			}
			anchor, err := s.FinalizedAnchor(ctx)
			if err != nil || anchor == nil || *anchor != b10 {
				t.Fatalf("deferred candidate changed anchor: %+v %v", anchor, err)
			}
			if err := checked.SaveFinalizedAnchorAt(ctx, b11, &b11); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRangeChecksDurableAnchorInsideBatch(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend)
			b10 := model.Block{Number: 10, Hash: common.HexToHash("10")}
			accepted := model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: b10.Hash}
			if err := s.Apply(ctx, model.Batch{Block: b10}); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveFinalizedAnchor(ctx, accepted); err != nil {
				t.Fatal(err)
			}
			fork := accepted
			fork.Hash = common.HexToHash("bad")
			b12 := model.Block{Number: 12, Hash: common.HexToHash("12"), ParentHash: fork.Hash}
			err := s.ApplyRange(ctx, []model.Batch{{Block: fork}, {Block: b12}}, func(context.Context) (func(), error) { return nil, nil })
			if !errors.Is(err, model.ErrFinalityChanged) {
				t.Fatalf("conflicting prefix published: %v", err)
			}
			cp, e := s.Checkpoint(ctx)
			if e != nil || cp == nil || *cp != b10 {
				t.Fatalf("partial range escaped: %+v %v", cp, e)
			}
			fenced, e := s.FinalityConflict(ctx)
			if e != nil || fenced {
				t.Fatalf("stale batch permanently fenced: %v %v", fenced, e)
			}
		})
	}
}

func TestFenceBeforeFirstAnchorSurvivesSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty-fence.sqlite")
	dep := model.Deployment{ChainID: "empty-fence", FromBlock: 10}
	s, err := OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFinalityConflict(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fenced, err := s.FinalityConflict(ctx)
	if err != nil || !fenced {
		t.Fatalf("lost sticky fence: %v %v", fenced, err)
	}
	anchor, err := s.FinalizedAnchor(ctx)
	if err != nil || anchor != nil {
		t.Fatalf("empty fence fabricated anchor: %+v %v", anchor, err)
	}
	if err := s.SaveFinalizedAnchorAt(ctx, model.Block{Number: 10}, nil); !errors.Is(err, model.ErrFinalityConflict) {
		t.Fatalf("fenced acceptance not rejected: %v", err)
	}
}
