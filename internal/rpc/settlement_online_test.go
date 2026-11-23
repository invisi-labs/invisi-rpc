package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

type nativeProfileTransport struct{ requests, logical atomic.Int64 }

func (r *nativeProfileTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	raw, e := io.ReadAll(q.Body)
	if e != nil {
		return nil, e
	}
	q.Body = io.NopCloser(bytes.NewReader(raw))
	r.requests.Add(1)
	var calls []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &calls) == nil {
		r.logical.Add(int64(len(calls)))
	} else {
		r.logical.Add(1)
	}
	return http.DefaultTransport.RoundTrip(q)
}

type profileFinality struct{ block *model.Block }

func (f profileFinality) Read(context.Context) finality.Snapshot {
	head := finality.Head{Status: "available", Block: f.block}
	return finality.Snapshot{Latest: head, Safe: head, Finalized: head}
}

// Explicit opt-in fixture server for actual-data comparisons. It reads public
// indexed batches and native chain data only, never an operator database/key.
// Two otherwise identical APIs expose legacy and batched native reads, letting
// Explorer benchmark the same fixed history with isolated local read models.
func TestOnlineSettlementProfileServers(t *testing.T) {
	nodeURL := os.Getenv("INVISI_SYNC_PROFILE_NODE")
	if file := os.Getenv("INVISI_SYNC_PROFILE_NODE_FILE"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal("cannot read native profile endpoint file")
		}
		nodeURL = strings.TrimSpace(string(raw))
	}
	if nodeURL == "" {
		t.Skip("opt-in native-node measurement")
	}
	protocolURL := os.Getenv("INVISI_SYNC_PROFILE_PROTOCOL")
	if protocolURL == "" {
		t.Fatal("INVISI_SYNC_PROFILE_PROTOCOL is required")
	}
	start, e := strconv.ParseUint(os.Getenv("INVISI_SYNC_PROFILE_FROM"), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	count, e := strconv.ParseUint(os.Getenv("INVISI_SYNC_PROFILE_BLOCKS"), 10, 64)
	if e != nil || count < 256 || count > 1024 {
		t.Fatal("profile blocks must be 256..1024")
	}
	ctx, stop := context.WithTimeout(context.Background(), 12*time.Minute)
	defer stop()
	source, e := gethrpc.DialContext(ctx, protocolURL)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	var dep struct {
		Deployment model.Deployment `json:"deployment"`
	}
	if e = source.CallContext(ctx, &dep, "invisi_getDeployment"); e != nil {
		t.Fatal(e)
	}
	var backend Backend
	var sampleHead *model.Block
	if file := os.Getenv("INVISI_SYNC_PROFILE_DATABASE"); file != "" {
		store, err := storage.OpenSQLite(ctx, file, dep.Deployment)
		if err != nil {
			t.Fatal("cannot open isolated profile snapshot")
		}
		defer store.Close()
		backend = store
		sampleHead, err = store.Block(ctx, start+count-1)
		if err != nil || sampleHead == nil {
			t.Fatal("sample absent from isolated profile snapshot")
		}
	} else {
		store := &replayStore{batches: map[uint64]*model.Batch{}}
		var mu sync.Mutex
		var group sync.WaitGroup
		errs := make(chan error, int(count))
		jobs := make(chan uint64)
		for range 4 {
			group.Add(1)
			go func() {
				defer group.Done()
				for n := range jobs {
					var batch *model.Batch
					if err := source.CallContext(ctx, &batch, "invisi_getBlock", n); err != nil {
						errs <- err
						continue
					}
					if batch == nil {
						errs <- io.ErrUnexpectedEOF
						continue
					}
					mu.Lock()
					store.batches[n] = batch
					mu.Unlock()
				}
			}()
		}
		for n := start; n < start+count; n++ {
			jobs <- n
		}
		close(jobs)
		group.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("profile setup failed (%T)", err)
		}
		store.head = &store.batches[start+count-1].Block
		backend = store
		sampleHead = store.head
	}
	for _, mode := range []string{"baseline", "candidate"} {
		transport := new(nativeProfileTransport)
		rpcClient, err := gethrpc.DialOptions(ctx, nodeURL, gethrpc.WithHTTPClient(&http.Client{Transport: transport, Timeout: 25 * time.Second}))
		if err != nil {
			t.Fatalf("profile setup failed (%T)", err)
		}
		client := ethclient.NewClient(rpcClient)
		t.Cleanup(client.Close)
		finalized, err := client.HeaderByNumber(ctx, big.NewInt(int64(gethrpc.FinalizedBlockNumber)))
		if err != nil || finalized.Number.Uint64() < sampleHead.Number {
			t.Fatalf("sample is not natively finalized (%T)", err)
		}
		// Independently verify both ends of the actual-data fixture.
		for _, n := range []uint64{start, start + count - 1} {
			h, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(n))
			if err != nil || h.Hash() != func() common.Hash {
				b, err := backend.Block(ctx, n)
				if err != nil || b == nil {
					t.Fatal("profile boundary missing")
				}
				return b.Hash
			}() {
				t.Fatalf("public index/native sample mismatch (%T)", err)
			}
		}
		api, err := New(backend, dep.Deployment, "1")
		if err != nil {
			t.Fatalf("profile setup failed (%T)", err)
		}
		api.Transactions = client
		if mode == "candidate" {
			api.Transactions = NewSettlementReader(client)
		}
		probeEnd := start + 15
		if _, probeErr := api.GetSettlementOperations(ctx, OperationsQuery{FromBlock: &start, ToBlock: &probeEnd, Limit: 100}); probeErr != nil {
			t.Fatalf("%s actual-data probe failed (%T)", mode, probeErr)
		}
		api.ChainState = client
		api.Finality = profileFinality{sampleHead}
		handler, err := HTTPHandler(api)
		if err != nil {
			t.Fatalf("profile setup failed (%T)", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/profile/stats", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"requests": transport.requests.Load(), "logicalCalls": transport.logical.Load()})
		})
		mux.Handle("/", handler)
		port := "18881"
		if mode == "candidate" {
			port = "18882"
		}
		listener, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Fatalf("profile setup failed (%T)", err)
		}
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		t.Cleanup(func() { server.Close() })
		go server.Serve(listener)
	}
	t.Logf("PROFILE_SERVERS_READY from=%d to=%d", start, start+count-1)
	<-ctx.Done()
}
