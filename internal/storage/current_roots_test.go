package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

func assertCurrentRoots(t *testing.T, s Backend) {
	t.Helper()
	ctx := context.Background()
	first := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x10")}, CreatedTrees: []uint64{7, 2}}
	second := model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("0x11"), ParentHash: first.Block.Hash}, Insertions: []model.Insertion{{TreeID: 7, Commitments: []common.Hash{common.HexToHash("0x01")}}}}
	third := model.Batch{CreatedTrees: []uint64{9}, Block: model.Block{Number: 12, Hash: common.HexToHash("0x12"), ParentHash: second.Block.Hash}, Insertions: []model.Insertion{{TreeID: 7, Start: 1, Commitments: []common.Hash{common.HexToHash("0x02")}}}}
	for _, b := range []model.Batch{first, second, third} {
		if e := s.Apply(ctx, b); e != nil {
			t.Fatal(e)
		}
	}
	roots, e := CurrentRoots(ctx, s)
	if e != nil || len(roots) != 3 {
		t.Fatalf("roots %+v %v", roots, e)
	}
	if roots[0].TreeID != 2 || roots[0].LeafCount != 0 || roots[0].Block != first.Block {
		t.Fatalf("empty tree anchor %+v", roots[0])
	}
	if roots[1].TreeID != 7 || roots[1].LeafCount != 2 || roots[1].Block != third.Block {
		t.Fatalf("latest tree anchor %+v", roots[1])
	}
	history, e := s.Roots(ctx, 7, 10)
	if e != nil || len(history) != 3 || roots[1].Root != history[0].Root {
		t.Fatalf("history mismatch %+v %v", history, e)
	}
	bounded, e := CurrentRootsAt(ctx, s, 11)
	if e != nil || len(bounded) != 2 || bounded[0].TreeID != 2 || bounded[1].TreeID != 7 || bounded[1].LeafCount != 1 || bounded[1].Root != history[1].Root || bounded[1].Block != second.Block {
		t.Fatalf("height bounded roots %+v %v", bounded, e)
	}
	before, e := CurrentRootsAt(ctx, s, 9)
	if e != nil || len(before) != 0 {
		t.Fatalf("future-created trees leaked %+v %v", before, e)
	}
	if _, e = CurrentRootsAt(ctx, s, ^uint64(0)); e == nil {
		t.Fatal("out-of-range height accepted")
	}
	if _, e = s.Membership(ctx, 7, 0, history[1].Root); e != nil {
		t.Fatalf("historical anchor unavailable: %v", e)
	}
	if e = s.Rollback(ctx, 11); e != nil {
		t.Fatal(e)
	}
	roots, e = CurrentRoots(ctx, s)
	if e != nil || len(roots) != 2 || roots[1].LeafCount != 1 || roots[1].Root != history[1].Root || roots[1].Block != second.Block {
		t.Fatalf("rollback latest %+v %v", roots, e)
	}
}
func TestSQLiteCurrentRoots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "roots.sqlite")
	dep := model.Deployment{ChainID: "1", FromBlock: 10}
	s, e := OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	roots, e := CurrentRoots(ctx, s)
	if e != nil || len(roots) != 0 {
		t.Fatalf("empty roots %+v %v", roots, e)
	}
	assertCurrentRoots(t, s)
	other, e := OpenSQLite(ctx, path, model.Deployment{ChainID: "2", FromBlock: 10})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	roots, e = CurrentRoots(ctx, other)
	if e != nil || len(roots) != 0 {
		t.Fatalf("deployment leaked %+v %v", roots, e)
	}
}
func TestPostgresCurrentRoots(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	s, e := Open(ctx, url, model.Deployment{ChainID: fmt.Sprintf("current-roots-%d", time.Now().UnixNano()), FromBlock: 10})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	defer s.pool.Exec(ctx, "DELETE FROM invisi_deployments WHERE deployment=$1", s.key)
	defer s.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", s.key)
	assertCurrentRoots(t, s)
}
