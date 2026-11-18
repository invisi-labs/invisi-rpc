package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

// The fixture models transport latency once per HTTP request, including a JSON-RPC
// batch. It uses real HTTP, native finality validation and durable SQLite; it does
// not model a real node's execution cost or an event-heavy chain.
func benchmarkHTTPChain(b *testing.B, count int, latency time.Duration) (*ethclient.Client, *atomic.Int64, func()) {
	b.Helper()
	headers := make([]*types.Header, count)
	var parent common.Hash
	for n := range headers {
		h := &types.Header{Number: big.NewInt(int64(n)), Difficulty: big.NewInt(0), ParentHash: parent, Time: uint64(n + 1), GasLimit: 30000000}
		headers[n] = h
		parent = h.Hash()
	}
	requests := new(atomic.Int64)
	respond := func(raw json.RawMessage) any {
		var q struct {
			ID     json.RawMessage
			Method string
			Params []json.RawMessage
		}
		if e := json.Unmarshal(raw, &q); e != nil {
			b.Error(e)
			return nil
		}
		var result any
		switch q.Method {
		case "eth_getBlockByNumber":
			var tag string
			_ = json.Unmarshal(q.Params[0], &tag)
			n := count - 1
			if len(tag) > 2 && tag[:2] == "0x" {
				value, ok := new(big.Int).SetString(tag[2:], 16)
				if !ok || !value.IsInt64() || value.Int64() < 0 || value.Int64() >= int64(count) {
					b.Error("bad block selector")
					return nil
				}
				n = int(value.Int64())
			}
			result = headers[n]
		case "eth_getLogs":
			result = []types.Log{}
		default:
			b.Errorf("unexpected method %s", q.Method)
		}
		return map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if latency > 0 {
			time.Sleep(latency)
		}
		var raw json.RawMessage
		if e := json.NewDecoder(r.Body).Decode(&raw); e != nil {
			b.Error(e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(raw) > 0 && raw[0] == '[' {
			var calls []json.RawMessage
			_ = json.Unmarshal(raw, &calls)
			responses := make([]any, len(calls))
			for i, c := range calls {
				responses[len(calls)-1-i] = respond(c)
			}
			_ = json.NewEncoder(w).Encode(responses)
		} else {
			_ = json.NewEncoder(w).Encode(respond(raw))
		}
	}))
	client, e := ethclient.Dial(server.URL)
	if e != nil {
		b.Fatal(e)
	}
	return client, requests, func() { client.Close(); server.Close() }
}
func BenchmarkCatchupHTTP(b *testing.B) {
	for _, latency := range []time.Duration{0, 2 * time.Millisecond} {
		for _, size := range []int{1, 32, 64, 128} {
			b.Run(fmt.Sprintf("rtt_%s/batch_%d", latency, size), func(b *testing.B) { benchmarkCatchup(b, latency, size) })
		}
	}
}

func benchmarkCatchup(b *testing.B, latency time.Duration, batchSize int) {
	const blocks = 128
	ctx := context.Background()
	client, requests, closeChain := benchmarkHTTPChain(b, blocks, latency)
	defer closeChain()
	var duration time.Duration
	var commits int64
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		dep := model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1"), RuntimeCodeHash: common.HexToHash("2")}
		rawStore, e := storage.OpenSQLite(ctx, filepath.Join(b.TempDir(), "index.sqlite"), dep)
		if e != nil {
			b.Fatal(e)
		}
		store := &benchmarkStore{SQLiteStore: rawStore}
		reader := &finality.Reader{RPC: client.Client(), Store: store}
		ix := &Indexer{BatchSize: batchSize, Chain: NewRPCChain(client), Store: store, Decoder: &fakeDecoder{}, Deployment: dep, Finality: reader}
		b.StartTimer()
		start := time.Now()
		for {
			progress, e := ix.Step(ctx)
			if e != nil {
				b.Fatal(e)
			}
			if !progress {
				break
			}
		}
		duration += time.Since(start)
		b.StopTimer()
		cp, e := store.Checkpoint(ctx)
		if e != nil || cp == nil || cp.Number != blocks-1 {
			b.Fatal("incomplete replay", e)
		}
		commits += store.commits
		store.Close()
		b.StartTimer()
	}
	b.ReportMetric(float64(blocks*b.N)/duration.Seconds(), "blocks/s")
	b.ReportMetric(float64(requests.Load())/float64(blocks*b.N), "requests/block")
	b.ReportMetric(float64(commits)/float64(blocks*b.N), "index_commits/block")
}

type benchmarkStore struct {
	*storage.SQLiteStore
	commits int64
}

func (s *benchmarkStore) Apply(ctx context.Context, b model.Batch) error {
	e := s.SQLiteStore.Apply(ctx, b)
	if e == nil {
		s.commits++
	}
	return e
}
func (s *benchmarkStore) ApplyRange(ctx context.Context, blocks []model.Batch, guard func(context.Context) (func(), error)) error {
	e := s.SQLiteStore.ApplyRange(ctx, blocks, guard)
	if e == nil {
		s.commits++
	}
	return e
}
