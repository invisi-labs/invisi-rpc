package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

type historyHeaderSource struct {
	headers []*types.Header
	calls   int
	corrupt bool
}

type failedHistorySource struct{ err error }

func (s failedHistorySource) Headers(context.Context, uint64, uint64) ([]*types.Header, error) {
	return nil, s.err
}

func (s *historyHeaderSource) Headers(_ context.Context, from, to uint64) ([]*types.Header, error) {
	s.calls++
	out := make([]*types.Header, to-from+1)
	for i := range out {
		out[i] = types.CopyHeader(s.headers[int(from)+i-10])
	}
	if s.corrupt {
		out[len(out)-1].Extra = []byte("fork")
	}
	return out, nil
}

func TestHistoricalHeadersPersistBackfillAndKeepLiveReads(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("1"), FromBlock: 10, RuntimeCodeHash: common.HexToHash("2")}
	store, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "index.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	source := &historyHeaderSource{}
	for n := uint64(10); n <= 12; n++ {
		h := &types.Header{Number: new(big.Int).SetUint64(n), Difficulty: big.NewInt(1), Time: n, Extra: []byte{byte(n)}}
		if len(source.headers) > 0 {
			h.ParentHash = source.headers[len(source.headers)-1].Hash()
		}
		source.headers = append(source.headers, h)
		if e = store.Apply(ctx, model.Batch{Block: model.Block{Number: n, Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}}); e != nil {
			t.Fatal(e)
		}
	}
	a, e := New(store, dep, "1")
	if e != nil {
		t.Fatal(e)
	}
	live := &stateFake{h: types.CopyHeader(source.headers[2])}
	a.ChainState = live
	handler, e := HTTPHandler(a)
	if e != nil {
		t.Fatal(e)
	}
	r := call(t, handler, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: 10, ToBlock: 12}}, "")
	if r.Error == nil || r.Error.Code != -32004 {
		t.Fatal("missing history must have explicit compatibility error", r.Error)
	}
	for _, code := range []int{-32004, -32601} {
		a.HistoricalHeaders = failedHistorySource{privateNodeError{code: code}}
		r = call(t, handler, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: 10, ToBlock: 12}}, "")
		if r.Error == nil || r.Error.Code != -32000 {
			t.Fatal("upstream error impersonated historical compatibility error", r.Error)
		}
	}
	a.HistoricalHeaders = source
	// Reorg during backfill must reject the publication.
	live.h.Extra = []byte("fork")
	r = call(t, handler, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: 10, ToBlock: 12}}, "")
	if r.Error == nil || r.Error.Code == -32004 {
		t.Fatal("backfill reorg became fallback", r.Error)
	}
	if _, e = store.Headers(ctx, 10, 12); e != storage.ErrHeadersUnavailable {
		t.Fatal("partial backfill published", e)
	}
	live.h = types.CopyHeader(source.headers[2])
	source.corrupt = true
	r = call(t, handler, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: 10, ToBlock: 12}}, "")
	if r.Error == nil || r.Error.Code == -32004 {
		t.Fatal("corrupt upstream header became fallback", r.Error)
	}
	source.corrupt = false
	r = call(t, handler, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: 10, ToBlock: 12}}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var result HeaderRangeResponse
	if e = json.Unmarshal(r.Result, &result); e != nil {
		t.Fatal(e)
	}
	if len(result.Headers) != 3 || result.Range.BlockHash != live.h.Hash() || result.Deployment != dep {
		t.Fatal("range not bound", result)
	}
	calls := source.calls
	if _, e = a.GetBlockHeaders(ctx, HeaderRangeRequest{FromBlock: 10, ToBlock: 12}); e != nil || source.calls != calls {
		t.Fatal("warm cache refetched upstream", e)
	}
	// The historical cache must not satisfy the independent live canonical fence.
	live.h.Extra = []byte("new live fork")
	got, e := a.ReadChainState(ctx, ChainStateRequest{Kind: "header", Block: "0xc"})
	if e != nil || got.HeaderHash == result.Range.BlockHash {
		t.Fatal("live observation used stale history", e)
	}
	if e = store.SaveFinalizedAnchor(ctx, model.Block{Number: 12, Hash: result.Range.BlockHash, Timestamp: 12}); e != nil {
		t.Fatal(e)
	}
	if e = store.MarkFinalityConflict(ctx); e != nil {
		t.Fatal(e)
	}
	r = call(t, handler, "invisi_getBlockHeaders", []any{HeaderRangeRequest{FromBlock: 10, ToBlock: 12}}, "")
	if r.Error == nil || r.Error.Code == -32004 {
		t.Fatal("fenced history fell back", r.Error)
	}
}

func TestHistoricalHeaderRangeInputBounds(t *testing.T) {
	a := &API{}
	h, e := HTTPHandler(a)
	if e != nil {
		t.Fatal(e)
	}
	for _, params := range []any{[]any{map[string]any{}}, []any{map[string]any{"toBlock": 10}}, []any{map[string]any{"fromBlock": 0, "toBlock": nil}}, []any{map[string]any{"fromBlock": 10, "toBlock": 9}}, []any{map[string]any{"fromBlock": 0, "toBlock": 256}}, []any{map[string]any{"fromBlock": -1, "toBlock": 2}}} {
		r := call(t, h, "invisi_getBlockHeaders", params.([]any), "")
		if r.Error == nil || r.Error.Code == -32004 {
			t.Fatal("invalid request became compatibility fallback", r.Error)
		}
	}
}
