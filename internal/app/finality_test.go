package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	apirpc "github.com/invisi-labs/invisi-rpc/internal/rpc"
)

type finalityFixture struct {
	*chainRPCFixture
	fork atomic.Bool
}

func (f *finalityFixture) GetBlockByNumber(ctx context.Context, tag string, full bool) (*types.Header, error) {
	if tag == "safe" || tag == "finalized" {
		tag = "0xa"
	}
	h, err := f.chainRPCFixture.GetBlockByNumber(ctx, tag, full)
	if err == nil && h != nil && f.fork.Load() {
		h.Extra = []byte("conflicting-finalized-history")
	}
	return h, err
}
func TestFinalityConflictKeepsReadOnlyServiceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	f := &finalityFixture{chainRPCFixture: &chainRPCFixture{latest: 10}}
	server := gethrpc.NewServer()
	if err := server.RegisterName("eth", f); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(server)
	defer upstream.Close()
	cfg := serviceConfig(t)
	cfg.ChainRPC = upstream.URL
	runtime, err := initializeChain(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { runtime.client.Close(); runtime.store.Close() }()
	// A durable anchor protects an indexed prefix. Finish the initial catch-up
	// before testing a conflict in history that has actually been accepted.
	for n := 0; n < 12; n++ {
		progress, e := runtime.indexer.Step(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if !progress {
			break
		}
	}
	accepted, e := runtime.store.FinalizedAnchor(ctx)
	if e != nil || accepted == nil || accepted.Number != 10 {
		t.Fatalf("fixture did not accept indexed finality: %+v %v", accepted, e)
	}
	original, err := runtime.store.Checkpoint(ctx)
	if err != nil || original == nil {
		t.Fatal(original, err)
	}
	f.fork.Store(true)
	if _, err = runtime.indexer.Step(ctx); !errors.Is(err, finality.ErrFinalityConflict) {
		t.Fatalf("unsafe rollback: %v", err)
	}
	after, err := runtime.store.Checkpoint(ctx)
	if err != nil || after == nil || *after != *original {
		t.Fatal(after, err)
	}
	runtime.client.Close()
	runtime.store.Close()
	runtime, err = initializeChain(ctx, cfg)
	if err != nil {
		t.Fatalf("read-only route disappeared: %v", err)
	}
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"invisi_getStatus","params":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	runtime.handler.ServeHTTP(response, request)
	var wire struct {
		Result apirpc.Status `json:"result"`
		Error  any           `json:"error"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Error != nil || wire.Result.Finality.Finalized.Status != "inconsistent" || wire.Result.IndexedHead == nil || *wire.Result.IndexedHead != *original {
		t.Fatal(response.Body.String())
	}
	f.fork.Store(false)
	if _, err = runtime.indexer.Step(ctx); !errors.Is(err, finality.ErrFinalityConflict) {
		t.Fatal("recovery cleared fence", err)
	}
}
