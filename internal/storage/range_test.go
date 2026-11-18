package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type rangeBackend interface {
	Backend
	ApplyRange(context.Context, []model.Batch, func(context.Context) (func(), error)) error
}

func openRangeBackend(t *testing.T, backend string) rangeBackend {
	t.Helper()
	dep := model.Deployment{ChainID: fmt.Sprintf("range-%d", time.Now().UnixNano()), FromBlock: 10}
	if backend == "postgres" {
		url := os.Getenv("TEST_DATABASE_URL")
		if url == "" {
			t.Skip("TEST_DATABASE_URL is not set")
		}
		s, e := OpenPostgres(context.Background(), url, dep)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Close)
		return s
	}
	s, e := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "range.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func TestApplyRangeAtomicGuards(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { testApplyRangeAtomicGuards(t, backend) })
	}
}
func testApplyRangeAtomicGuards(t *testing.T, backend string) {
	for _, failure := range []string{"none", "middle_gap", "root", "commit_guard", "cancel", "fence"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend)
			var e error
			blocks := []model.Batch{{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}}, {Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: common.HexToHash("10")}}, {Block: model.Block{Number: 12, Hash: common.HexToHash("12"), ParentHash: common.HexToHash("11")}}}
			guard := func(context.Context) (func(), error) { return nil, nil }
			switch failure {
			case "middle_gap":
				blocks[1].Block.Number = 15
			case "root":
				blocks[1].CreatedTrees = []uint64{1}
				s.SetRootValidator(func(context.Context, model.Root) error { return errors.New("invalid root") })
			case "commit_guard":
				guard = func(context.Context) (func(), error) { return nil, errors.New("chain changed") }
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "fence":
				if e = s.SaveFinalizedAnchor(ctx, model.Block{Number: 20}); e != nil {
					t.Fatal(e)
				}
				if e = s.MarkFinalityConflict(ctx); e != nil {
					t.Fatal(e)
				}
			}
			e = s.ApplyRange(ctx, blocks, guard)
			if (e == nil) != (failure == "none") {
				t.Fatal("unexpected range result", e)
			}
			cp, e := s.Checkpoint(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if failure == "none" {
				if cp == nil || cp.Number != 12 {
					t.Fatal("range not committed")
				}
			} else {
				if cp != nil {
					t.Fatal("partial range published", cp)
				}
				for _, n := range []uint64{10, 11, 12} {
					b, e := s.Batch(context.Background(), n)
					if e != nil || b != nil {
						t.Fatal("rolled back block retained", n, e)
					}
				}
			}
		})
	}
}

func TestApplyRangeMatchesSequentialHistory(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			sequential, batched := openRangeBackend(t, backend), openRangeBackend(t, backend)
			leaf := common.HexToHash("1")
			nullifier := common.HexToHash("2")
			blocks := []model.Batch{
				{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}, CreatedTrees: []uint64{1}},
				{Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: common.HexToHash("10")}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{leaf}}}, Outputs: []model.NoteOutput{{TreeID: 1, Commitment: leaf}}, Nullifiers: []common.Hash{nullifier}},
				{Block: model.Block{Number: 12, Hash: common.HexToHash("12"), ParentHash: common.HexToHash("11")}},
			}
			for _, b := range blocks {
				if e := sequential.Apply(ctx, b); e != nil {
					t.Fatal(e)
				}
			}
			if e := batched.ApplyRange(ctx, blocks, nil); e != nil {
				t.Fatal(e)
			}
			export := func(s Backend) []model.Batch {
				var out []model.Batch
				_, e := s.Export(ctx, func(b model.Batch) error { out = append(out, b); return nil })
				if e != nil {
					t.Fatal(e)
				}
				return out
			}
			if !reflect.DeepEqual(export(sequential), export(batched)) {
				t.Fatal("normalized histories differ")
			}
			roots, e := sequential.Roots(ctx, 1, 10)
			if e != nil || len(roots) != 2 {
				t.Fatal("roots", e)
			}
			other, e := batched.Roots(ctx, 1, 10)
			if e != nil || !reflect.DeepEqual(roots, other) {
				t.Fatal("roots differ", e)
			}
			m, e := sequential.Membership(ctx, 1, 0, roots[0].Root)
			if e != nil {
				t.Fatal(e)
			}
			bm, e := batched.Membership(ctx, 1, 0, roots[0].Root)
			if e != nil || !reflect.DeepEqual(m, bm) {
				t.Fatal("membership differs", e)
			}
			if spent, e := batched.IsSpent(ctx, nullifier); e != nil || !spent {
				t.Fatal("nullifier missing", e)
			}
			if e := batched.Rollback(ctx, 10); e != nil {
				t.Fatal(e)
			}
			if spent, e := batched.IsSpent(ctx, nullifier); e != nil || spent {
				t.Fatal("rollback retained nullifier", e)
			}
		})
	}
}
