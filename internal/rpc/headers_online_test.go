package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

// Opt-in, read-only online comparison. Production is not upgraded. Timings of
// remote live batches and local persisted ranges have different network costs;
// the local live control isolates serialization/storage/request-count effects.
type headerBenchRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}
type headerBenchResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func headerBenchPost(ctx context.Context, client *http.Client, url string, body any, out any) error {
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Invisi-Header-Range-Benchmark/1")
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("unexpected HTTP status %d", res.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if e != nil {
		return e
	}
	return json.Unmarshal(data, out)
}

type onlineHeaderBridge struct {
	stateFake
	client *http.Client
	url    string
	dep    model.Deployment
	calls  atomic.Uint64
}

func (b *onlineHeaderBridge) Headers(ctx context.Context, from, to uint64) ([]*types.Header, error) {
	out := make([]*types.Header, 0, to-from+1)
	for start := from; start <= to; {
		end := min(start+19, to)
		reqs := make([]headerBenchRequest, 0, end-start+1)
		for n := start; n <= end; n++ {
			reqs = append(reqs, headerBenchRequest{"2.0", n, "invisi_readChainState", []any{ChainStateRequest{Kind: "header", Block: fmt.Sprintf("0x%x", n)}}})
		}
		var rows []headerBenchResponse
		b.calls.Add(1)
		if e := headerBenchPost(ctx, b.client, b.url, reqs, &rows); e != nil {
			return nil, e
		}
		if len(rows) != len(reqs) {
			return nil, fmt.Errorf("incomplete online batch")
		}
		byID := make(map[uint64]headerBenchResponse, len(rows))
		for _, r := range rows {
			if _, ok := byID[r.ID]; ok {
				return nil, fmt.Errorf("duplicate response ID")
			}
			byID[r.ID] = r
		}
		for _, req := range reqs {
			r, ok := byID[req.ID]
			if !ok || len(r.Error) > 0 {
				return nil, fmt.Errorf("online header request failed")
			}
			var wire struct {
				VersionInfo
				Deployment model.Deployment `json:"deployment"`
				Header     json.RawMessage  `json:"header"`
				HeaderHash string           `json:"headerHash"`
			}
			if e := json.Unmarshal(r.Result, &wire); e != nil {
				return nil, e
			}
			if wire.APIVersion != "v1" || wire.ProtocolVersion != "1" || !reflect.DeepEqual(wire.Deployment, b.dep) {
				return nil, fmt.Errorf("online deployment/version changed")
			}
			h, e := model.DecodeCompleteHeader(wire.Header)
			if e != nil {
				return nil, e
			}
			if h.Number.Uint64() != req.ID || h.Hash().Hex() != wire.HeaderHash {
				return nil, fmt.Errorf("online header identity mismatch")
			}
			if len(out) > 0 && h.ParentHash != out[len(out)-1].Hash() {
				return nil, fmt.Errorf("online parent mismatch")
			}
			out = append(out, h)
		}
		start = end + 1
	}
	return out, nil
}
func (b *onlineHeaderBridge) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if n == nil || !n.IsUint64() {
		return nil, fmt.Errorf("numeric benchmark anchor required")
	}
	rows, e := b.Headers(ctx, n.Uint64(), n.Uint64())
	if e != nil {
		return nil, e
	}
	return rows[0], nil
}

type localHeaderFixture struct {
	stateFake
	headers map[uint64]*types.Header
}

func (f *localHeaderFixture) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	return f.headers[n.Uint64()], nil
}

func TestOnlineHistoricalHeaderComparison(t *testing.T) {
	url := os.Getenv("INVISI_HEADER_BENCH_RPC")
	if url == "" {
		t.Skip("set INVISI_HEADER_BENCH_RPC to a public protocol RPC endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	var depReply headerBenchResponse
	if e := headerBenchPost(ctx, client, url, headerBenchRequest{"2.0", 1, "invisi_getDeployment", []any{}}, &depReply); e != nil {
		t.Fatal(e)
	}
	var deployed struct {
		VersionInfo
		Deployment model.Deployment `json:"deployment"`
	}
	if e := json.Unmarshal(depReply.Result, &deployed); e != nil || len(depReply.Error) > 0 {
		t.Fatal("deployment", e)
	}
	dep := deployed.Deployment
	from := dep.IndexFromBlock()
	to := from + 255
	bridge := &onlineHeaderBridge{client: client, url: url, dep: dep}
	var original []*types.Header
	var remoteMS []float64
	for i := 0; i < 3; i++ {
		start := time.Now()
		rows, e := bridge.Headers(ctx, from, to)
		if e != nil {
			t.Fatal(e)
		}
		remoteMS = append(remoteMS, float64(time.Since(start).Microseconds())/1000)
		if i == 0 {
			original = rows
		} else {
			for n, h := range rows {
				if h.Hash() != original[n].Hash() {
					t.Fatal("online history changed")
				}
			}
		}
	}
	// This fixture contains real full headers and normalized blocks, not a copied
	// payment/event database. It does not claim end-to-end wallet timings.
	store, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "online-headers.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	for _, h := range original {
		if e = store.Apply(ctx, model.Batch{Block: model.Block{Number: h.Number.Uint64(), Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}}); e != nil {
			t.Fatal(e)
		}
	}
	api, e := New(store, dep, "1")
	if e != nil {
		t.Fatal(e)
	}
	api.ChainState = bridge
	api.HistoricalHeaders = bridge
	handler, e := HTTPHandler(api)
	if e != nil {
		t.Fatal(e)
	}
	local := httptest.NewServer(handler)
	defer local.Close()
	readRange := func() float64 {
		t.Helper()
		start := time.Now()
		var reply headerBenchResponse
		if e := headerBenchPost(ctx, client, local.URL, headerBenchRequest{"2.0", 7, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: from, ToBlock: to}}}, &reply); e != nil {
			t.Fatal(e)
		}
		var value HeaderRangeResponse
		if len(reply.Error) > 0 || json.Unmarshal(reply.Result, &value) != nil || len(value.Headers) != 256 {
			t.Fatal("range response", string(reply.Error))
		}
		if !reflect.DeepEqual(value.Deployment, dep) || value.Range.BlockHash != original[255].Hash() {
			t.Fatal("range pins/anchor changed")
		}
		for i, h := range value.Headers {
			if h.HeaderHash != original[i].Hash() || h.Header.Hash() != h.HeaderHash {
				t.Fatal("range changed original header")
			}
		}
		return float64(time.Since(start).Microseconds()) / 1000
	}
	coldCalls := bridge.calls.Load()
	coldMS := readRange()
	coldCalls = bridge.calls.Load() - coldCalls
	before := bridge.calls.Load()
	var warmMS []float64
	for i := 0; i < 5; i++ {
		warmMS = append(warmMS, readRange())
	}
	warmCalls := bridge.calls.Load() - before
	if warmCalls != 0 {
		t.Fatal("warm historical reads reached remote RPC")
	}
	fixture := &localHeaderFixture{headers: map[uint64]*types.Header{}}
	for _, h := range original {
		fixture.headers[h.Number.Uint64()] = h
	}
	api.ChainState = fixture
	localBridge := &onlineHeaderBridge{client: client, url: local.URL, dep: dep}
	var localLegacyMS []float64
	for i := 0; i < 5; i++ {
		start := time.Now()
		rows, e := localBridge.Headers(ctx, from, to)
		if e != nil || len(rows) != 256 {
			t.Fatal(e)
		}
		localLegacyMS = append(localLegacyMS, float64(time.Since(start).Microseconds())/1000)
	}
	live, e := bridge.HeaderByNumber(ctx, new(big.Int).SetUint64(to))
	if e != nil || live.Hash() != original[255].Hash() {
		t.Fatal("final live canonical fence", e)
	}
	report := map[string]any{"sourceRpc": url, "fromBlock": from, "toBlock": to, "headers": 256, "lastBlockHash": original[255].Hash().Hex(), "onlineLiveBatchRequestsPerSample": 13, "onlineLiveBatchMs": remoteMS, "localLegacyBatchMs": localLegacyMS, "localLegacyRequestsPerSample": 13, "localHistoricalRangeMs": warmMS, "localHistoricalRequestsPerSample": 1, "warmUpstreamRequests": warmCalls, "coldBackfillMs": coldMS, "coldBackfillProtocolRpcRequests": coldCalls, "originalHashesAndLiveFinalAnchorVerified": true, "scope": "Header acquisition only. New range runs locally; public network latency is not simulated. Production RPC and wallet runtime are unchanged."}
	raw, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	t.Log(string(raw))
	if path := os.Getenv("INVISI_HEADER_BENCH_REPORT"); path != "" {
		if e = os.WriteFile(path, append(raw, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
