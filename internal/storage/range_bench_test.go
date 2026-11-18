package storage

import (
	"context"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

// Isolates durable index transactions from HTTP/finality request latency.
func BenchmarkApplyHistory(b *testing.B) {
	for _, size := range []int{1, 32, 64, 128} {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			ctx := context.Background()
			blocks := make([]model.Batch, 128)
			for n := range blocks {
				blocks[n].Block = model.Block{Number: uint64(n), Hash: common.BigToHash(new(big.Int).SetUint64(uint64(n + 1))), ParentHash: common.BigToHash(new(big.Int).SetUint64(uint64(n)))}
			}
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				b.StopTimer()
				s, e := OpenSQLite(ctx, filepath.Join(b.TempDir(), "index.sqlite"), model.Deployment{ChainID: "31337"})
				if e != nil {
					b.Fatal(e)
				}
				b.StartTimer()
				for n := 0; n < len(blocks); n += size {
					if size == 1 {
						e = s.Apply(ctx, blocks[n])
					} else {
						e = s.ApplyRange(ctx, blocks[n:n+size], nil)
					}
					if e != nil {
						b.Fatal(e)
					}
				}
				b.StopTimer()
				s.Close()
				b.StartTimer()
			}
			b.ReportMetric(1/float64(size), "index_commits/block")
		})
	}
}
