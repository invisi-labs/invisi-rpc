package storage

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNoteLookup(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			dep := model.Deployment{ChainID: fmt.Sprint("note-", time.Now().UnixNano()), FromBlock: 10}
			var s Backend
			if backend == "sqlite" {
				v, e := OpenSQLite(ctx, filepath.Join(t.TempDir(), "notes.db"), dep)
				if e != nil {
					t.Fatal(e)
				}
				s = v
			} else {
				url := os.Getenv("TEST_DATABASE_URL")
				if url == "" {
					t.Skip("TEST_DATABASE_URL is not set")
				}
				v, e := Open(ctx, url, dep)
				if e != nil {
					t.Fatal(e)
				}
				s = v
				t.Cleanup(func() { v.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", v.key) })
			}
			defer s.Close()
			apply := func(b model.Batch) {
				t.Helper()
				if e := s.Apply(ctx, b); e != nil {
					t.Fatal(e)
				}
			}
			first := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}, CreatedTrees: []uint64{1}}
			apply(first)
			leaf := common.HexToHash("123")
			output := model.Output{TreeID: 1, LeafIndex: 0, Commitment: leaf, Token: common.HexToAddress("456"), TransactionHash: common.HexToHash("789"), Kind: "Shield"}
			second := model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: first.Block.Hash}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{leaf}}}, Outputs: []model.Output{output}}
			apply(second)
			if v, e := s.Note(ctx, leaf, 10); e != nil || v != nil {
				t.Fatalf("future note: %v %v", v, e)
			}
			v, e := s.Note(ctx, leaf, 11)
			if e != nil || v == nil {
				t.Fatalf("lookup: %v %v", v, e)
			}
			if v.Output.Commitment != leaf || v.Output.Token != output.Token || v.Membership.Commitment != leaf || v.Membership.Root.TreeID != 1 || v.Output.Block.Hash != second.Block.Hash || len(v.Membership.Siblings) != 16 {
				t.Fatalf("invalid note: %+v", v)
			}

			computed := leaf
			for _, sibling := range v.Membership.Siblings {
				computed, e = cryptography.HashPair(computed, sibling)
				if e != nil {
					t.Fatal(e)
				}
			}
			if computed != v.Membership.Root.Root {
				t.Fatal("invalid commitment membership path")
			}
			third := model.Batch{Block: model.Block{Number: 12, Hash: common.HexToHash("12"), ParentHash: second.Block.Hash}, Insertions: []model.Insertion{{TreeID: 1, Start: 1, Commitments: []common.Hash{common.HexToHash("124")}}}}
			if e = s.Apply(ctx, third); e != nil {
				t.Fatal(e)
			}
			historical, e := s.Note(ctx, leaf, 11)
			if e != nil || historical.Membership.Root.Root != v.Membership.Root.Root {
				t.Fatal("historical root changed")
			}
			latest, e := s.Note(ctx, leaf, 12)
			if e != nil || latest.Membership.Root.LeafCount != 2 || latest.Membership.Siblings[0] != common.HexToHash("124") {
				t.Fatal("latest membership not selected")
			}
			if v, e := s.Note(ctx, common.HexToHash("99"), 11); e != nil || v != nil {
				t.Fatalf("missing: %v %v", v, e)
			}
			if e = s.Rollback(ctx, 10); e != nil {
				t.Fatal(e)
			}
			if v, e := s.Note(ctx, leaf, 11); e != nil || v != nil {
				t.Fatalf("reorg retained note: %v %v", v, e)
			}
		})
	}
}
