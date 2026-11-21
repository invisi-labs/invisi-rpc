package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type headerBackend interface {
	rangeBackend
	Headers(context.Context, uint64, uint64) ([]model.BlockHeader, error)
	CacheHeaders(context.Context, []*types.Header, func(context.Context) error) error
}

func headerBatches() []model.Batch {
	out := make([]model.Batch, 3)
	var parent common.Hash
	for i := range out {
		h := &types.Header{Number: big.NewInt(int64(10 + i)), Difficulty: big.NewInt(1), ParentHash: parent, Time: uint64(100 + i), Extra: []byte{byte(i)}, BaseFee: big.NewInt(5)}
		out[i] = model.Batch{Block: model.Block{Number: uint64(10 + i), Hash: h.Hash(), ParentHash: parent, Timestamp: h.Time}, Header: h}
		parent = h.Hash()
	}
	return out
}

func TestCompleteHeadersRoundTripRollbackAndRestart(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend).(headerBackend)
			blocks := headerBatches()
			if e := s.ApplyRange(ctx, blocks, nil); e != nil {
				t.Fatal(e)
			}
			rows, e := s.Headers(ctx, 10, 12)
			if e != nil || len(rows) != 3 {
				t.Fatal(rows, e)
			}
			for i, row := range rows {
				if row.HeaderHash != blocks[i].Block.Hash || !reflect.DeepEqual(row.Header, blocks[i].Header) {
					t.Fatal("full header changed")
				}
			}
			var exported []model.Batch
			if _, e = s.Export(ctx, func(b model.Batch) error { exported = append(exported, b); return nil }); e != nil {
				t.Fatal(e)
			}
			if e = s.Rollback(ctx, 10); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Headers(ctx, 10, 12); !errors.Is(e, ErrHeadersUnavailable) {
				t.Fatal("rolled back header survived", e)
			}
			// A fork at the same height must not reuse the removed full header.
			fork := headerBatches()[1]
			fork.Header.Extra = []byte("fork")
			fork.Block.Hash = fork.Header.Hash()
			if e = s.Apply(ctx, fork); e != nil {
				t.Fatal(e)
			}
			rows, e = s.Headers(ctx, 10, 11)
			if e != nil || rows[1].HeaderHash != fork.Block.Hash {
				t.Fatal("stale fork", e)
			}
			// Export/import carries the optional complete header without changing format.
			restored := openRangeBackend(t, backend).(headerBackend)
			i := 0
			e = restored.Import(ctx, func() (model.Batch, error) {
				if i == len(exported) {
					return model.Batch{}, io.EOF
				}
				b := exported[i]
				i++
				return b, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			rows, e = restored.Headers(ctx, 10, 12)
			if e != nil || len(rows) != 3 || rows[2].HeaderHash != blocks[2].Block.Hash {
				t.Fatal("restored headers", e)
			}
		})
	}
}

func TestCompleteHeadersFailClosed(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend).(headerBackend)
			blocks := headerBatches()
			bad := blocks[0]
			bad.Header = types.CopyHeader(bad.Header)
			bad.Header.Time++
			if e := s.Apply(ctx, bad); e == nil {
				t.Fatal("accepted header different from block")
			}
			cp, _ := s.Checkpoint(ctx)
			if cp != nil {
				t.Fatal("published invalid header")
			}
			// Legacy batches remain accepted, but are never reconstructed as full headers.
			legacy := blocks[0]
			legacy.Header = nil
			if e := s.Apply(ctx, legacy); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Headers(ctx, 10, 10); !errors.Is(e, ErrHeadersUnavailable) {
				t.Fatal("legacy incomplete header", e)
			}
			h := blocks[0].Header
			if e := s.CacheHeaders(ctx, []*types.Header{h}, func(context.Context) error { return errors.New("reorg") }); e == nil {
				t.Fatal("ignored commit fence")
			}
			if _, e := s.Headers(ctx, 10, 10); !errors.Is(e, ErrHeadersUnavailable) {
				t.Fatal("failed backfill published", e)
			}
			mismatch := types.CopyHeader(h)
			mismatch.Extra = []byte("wrong fork")
			if e := s.CacheHeaders(ctx, []*types.Header{mismatch}, nil); e == nil {
				t.Fatal("backfilled other fork")
			}
			if e := s.CacheHeaders(ctx, []*types.Header{h}, nil); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Headers(ctx, 10, 10); e != nil {
				t.Fatal(e)
			}
			// Header validation applies before idempotent Apply too.
			if e := s.Apply(ctx, bad); e == nil {
				t.Fatal("idempotent apply ignored corrupt header")
			}
			if e := s.SaveFinalizedAnchor(ctx, blocks[0].Block); e != nil {
				t.Fatal(e)
			}
			if e := s.MarkFinalityConflict(ctx); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Headers(ctx, 10, 10); e == nil || errors.Is(e, ErrHeadersUnavailable) {
				t.Fatal("fenced history eligible for fallback", e)
			}
			if e := s.CacheHeaders(ctx, []*types.Header{h}, nil); e == nil {
				t.Fatal("backfill crossed finality fence")
			}
		})
	}
}

func TestCompleteHeaderRangeBounds(t *testing.T) {
	s := openRangeBackend(t, "sqlite").(headerBackend)
	for _, r := range [][2]uint64{{11, 10}, {0, 256}, {math.MaxInt64, math.MaxUint64}} {
		if _, e := s.Headers(context.Background(), r[0], r[1]); e == nil || errors.Is(e, ErrHeadersUnavailable) {
			t.Fatal("invalid range accepted or fallback", r, e)
		}
	}
}

func TestHeaderBackfillRequiresMatchingStorageIdentity(t *testing.T) {
	ctx := context.Background()
	s := openRangeBackend(t, "sqlite").(*SQLiteStore)
	b := headerBatches()[0]
	h := b.Header
	b.Header = nil
	if e := s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	if _, e := s.pool.ExecContext(ctx, "UPDATE invisi_blocks SET hash=?1 WHERE number=10", common.HexToHash("bad").Bytes()); e != nil {
		t.Fatal(e)
	}
	if e := s.CacheHeaders(ctx, []*types.Header{h}, nil); e == nil {
		t.Fatal("reported successful cache update on a conflicting storage identity")
	}
}

func TestHeaderCacheReopenAndCorruption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "headers.sqlite")
	dep := model.Deployment{ChainID: fmt.Sprint(time.Now().UnixNano()), FromBlock: 10}
	s, e := OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	blocks := headerBatches()
	if e = s.ApplyRange(ctx, blocks, nil); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	rows, e := s.Headers(ctx, 10, 12)
	if e != nil || len(rows) != 3 {
		t.Fatal("reopen lost full headers", e)
	}
	// Corrupt a redundant zero seal field without changing the RLP hash.
	if _, e = s.pool.ExecContext(ctx, "UPDATE invisi_blocks SET batch=json_remove(batch,'$.header.nonce') WHERE number=11"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Headers(ctx, 10, 12); e == nil || errors.Is(e, ErrHeadersUnavailable) {
		t.Fatal("incomplete stored header allowed fallback", e)
	}
	for _, bad := range []string{"null", ""} {
		if _, e = s.pool.ExecContext(ctx, "UPDATE invisi_blocks SET batch=json_set(batch,'$.header',?1) WHERE number=11", bad); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Headers(ctx, 10, 12); e == nil || errors.Is(e, ErrHeadersUnavailable) {
			t.Fatal("scalar header mistaken for absent history", bad, e)
		}
	}
}

func TestFencedMissingHeaderRangeCannotFallback(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend).(headerBackend)
			b := headerBatches()[0]
			if e := s.Apply(ctx, b); e != nil {
				t.Fatal(e)
			}
			if e := s.SaveFinalizedAnchor(ctx, b.Block); e != nil {
				t.Fatal(e)
			}
			if e := s.MarkFinalityConflict(ctx); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Headers(ctx, 20, 21); e == nil || errors.Is(e, ErrHeadersUnavailable) {
				t.Fatal("fenced missing range eligible for fallback", e)
			}
		})
	}
}

func TestHeaderRangeConcurrentRollbackIsOneFork(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := openRangeBackend(t, backend).(headerBackend)
			blocks := headerBatches()
			if e := s.ApplyRange(ctx, blocks, nil); e != nil {
				t.Fatal(e)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 30; i++ {
					rows, e := s.Headers(ctx, 10, 12)
					if errors.Is(e, ErrHeadersUnavailable) {
						continue
					}
					if e != nil {
						errs <- e
						return
					}
					if len(rows) != 3 || rows[1].Header.ParentHash != rows[0].HeaderHash || rows[2].Header.ParentHash != rows[1].HeaderHash {
						errs <- errors.New("mixed fork snapshot")
						return
					}
				}
			}()
			for i := 0; i < 10; i++ {
				if e := s.Rollback(ctx, 10); e != nil {
					t.Fatal(e)
				}
				h := types.CopyHeader(blocks[1].Header)
				h.Extra = []byte{99, byte(i)}
				blocks[1].Header = h
				blocks[1].Block.Hash = h.Hash()
				h = types.CopyHeader(blocks[2].Header)
				h.ParentHash = blocks[1].Block.Hash
				blocks[2].Header = h
				blocks[2].Block.ParentHash = h.ParentHash
				blocks[2].Block.Hash = h.Hash()
				if e := s.ApplyRange(ctx, blocks[1:], nil); e != nil {
					t.Fatal(e)
				}
			}
			wg.Wait()
			select {
			case e := <-errs:
				t.Fatal(e)
			default:
			}
		})
	}
}
