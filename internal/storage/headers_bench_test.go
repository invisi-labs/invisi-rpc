package storage

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

func BenchmarkPersistedHeaderRange(b *testing.B) {
	ctx := context.Background()
	s, e := OpenSQLite(ctx, filepath.Join(b.TempDir(), "headers.sqlite"), model.Deployment{ChainID: "97", FromBlock: 10})
	if e != nil {
		b.Fatal(e)
	}
	defer s.Close()
	blocks := make([]model.Batch, 256)
	for i := range blocks {
		h := &types.Header{Number: big.NewInt(int64(10 + i)), Difficulty: big.NewInt(1), Extra: make([]byte, 700)}
		if i > 0 {
			h.ParentHash = blocks[i-1].Block.Hash
		}
		blocks[i] = model.Batch{Header: h, Block: model.Block{Number: uint64(10 + i), Hash: h.Hash(), ParentHash: h.ParentHash}}
	}
	for start := 0; start < len(blocks); start += 128 {
		if e = s.ApplyRange(ctx, blocks[start:start+128], nil); e != nil {
			b.Fatal(e)
		}
	}
	for _, single := range []bool{false, true} {
		name := "range256"
		if single {
			name = "single256"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if single {
					for n := uint64(10); n < 266; n++ {
						if _, e := s.Headers(ctx, n, n); e != nil {
							b.Fatal(e)
						}
					}
				} else {
					rows, e := s.Headers(ctx, 10, 265)
					if e != nil || len(rows) != 256 {
						b.Fatal(e)
					}
				}
			}
			calls := float64(1)
			if single {
				calls = 256
			}
			b.ReportMetric(calls, "sql_queries/op")
		})
	}
}
