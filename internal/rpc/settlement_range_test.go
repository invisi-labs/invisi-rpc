package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

// Uses the real HTTP native client; assertions cover consumer-visible facts and
// native request amplification, rather than a mocked range-reader implementation.
func nativeSettlementFixture(t *testing.T, fault string, counts ...int) (*API, *atomic.Int64) {
	t.Helper()
	if len(counts) == 0 {
		counts = []int{0, 1, 0}
	}
	a, chain, _ := replayFixture(t, counts...)
	store := a.Store.(*replayStore)
	uncle := &types.Header{Number: big.NewInt(9), Difficulty: big.NewInt(0), Time: 9}
	parent := common.Hash{}
	for n := uint64(10); n < 10+uint64(len(counts)); n++ {
		old := chain.blocks[n]
		h := old.Header()
		h.ParentHash = parent
		h.UncleHash = types.EmptyUncleHash
		if fault == "uncle-body" && n == 11 {
			h.UncleHash = types.CalcUncleHash([]*types.Header{uncle})
		}
		h.TxHash = types.DeriveSha(old.Transactions(), trie.NewStackTrie(nil))
		b := types.NewBlockWithHeader(h).WithBody(types.Body{Transactions: old.Transactions()})
		chain.blocks[n] = b
		parent = b.Hash()
		stored := store.batches[n]
		stored.Block.Hash = b.Hash()
		stored.Block.ParentHash = b.ParentHash()
		for _, tx := range b.Transactions() {
			r := chain.receipts[tx.Hash()]
			r.BlockHash = b.Hash()
			r.Logs = []*types.Log{}
			r.GasUsed = 21000
			r.CumulativeGasUsed = 21000
		}
	}
	store.head = &store.batches[9+uint64(len(counts))].Block
	calls := new(atomic.Int64)
	type request struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	fullReads := new(atomic.Int64)
	response := func(q request) any {
		var result any
		switch q.Method {
		case "eth_chainId":
			result = "0x38"
			if fault == "chain" {
				result = "0x1"
			}
		case "eth_getCode":
			result = "0x00"
			if fault == "code" {
				result = "0x01"
			}
		case "eth_getBlockByNumber":
			var tag string
			json.Unmarshal(q.Params[0], &tag)
			n, _ := hexutil.DecodeUint64(tag)
			b := chain.blocks[n]
			if b == nil {
				break
			}
			raw, _ := json.Marshal(b.Header())
			var obj map[string]any
			json.Unmarshal(raw, &obj)
			var full bool
			json.Unmarshal(q.Params[1], &full)
			obj["hash"] = b.Hash()
			obj["uncles"] = []common.Hash{}
			if fault == "uncle-body" && n == 11 {
				obj["uncles"] = []common.Hash{uncle.Hash()}
			}
			if full {
				obj["transactions"] = b.Transactions()
				if fault == "later-block" && n >= 14 {
					return map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": nil}
				}
				if fault == "uncle-body" && n == 11 && fullReads.Add(1) > 1 {
					to := a.Deployment.Protocol
					obj["transactions"] = []*types.Transaction{types.NewTx(&types.LegacyTx{To: &to, Nonce: 999, Data: []byte{0xff}})}
				}
				if fault == "body" {
					obj["transactions"] = []any{}
				}
			} else {
				obj["transactions"] = []any{}
			}
			if fault == "height" && full {
				obj["number"] = "0xb"
			}
			if fault == "hash" && full {
				obj["hash"] = common.HexToHash("0xff")
			}
			result = obj
		case "eth_getUncleByBlockHashAndIndex":
			result = uncle
		case "eth_getTransactionReceipt":
			var hash common.Hash
			json.Unmarshal(q.Params[0], &hash)
			result = chain.receipts[hash]
		default:
			t.Errorf("unexpected native request %s", q.Method)
		}
		return map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var raw json.RawMessage
		json.NewDecoder(r.Body).Decode(&raw)
		w.Header().Set("Content-Type", "application/json")
		if len(raw) > 0 && raw[0] == '[' {
			if fault == "array-batch-limit" {
				var qs []request
				json.Unmarshal(raw, &qs)
				json.NewEncoder(w).Encode([]any{map[string]any{"jsonrpc": "2.0", "id": qs[0].ID, "error": map[string]any{"code": -32600, "message": "batch too large"}}})
				return
			}
			if fault == "no-batch" {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"batch unavailable"}}`)
				return
			}
			var qs []request
			json.Unmarshal(raw, &qs)
			out := []any{}
			for i := len(qs) - 1; i >= 0; i-- {
				if fault == "missing" && i == 0 {
					continue
				}
				out = append(out, response(qs[i]))
			}
			json.NewEncoder(w).Encode(out)
		} else {
			var q request
			json.Unmarshal(raw, &q)
			json.NewEncoder(w).Encode(response(q))
		}
	}))
	t.Cleanup(server.Close)
	transport, e := gethrpc.DialHTTP(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	client := ethclient.NewClient(transport)
	t.Cleanup(client.Close)
	a.Transactions = NewSettlementReader(client)
	return a, calls
}

func TestNativeSettlementRangeKeepsFailedTransactionsAndBatches(t *testing.T) {
	a, calls := nativeSettlementFixture(t, "")
	p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Operations) != 1 || p.Operations[0].Success || p.Operations[0].BlockNumber != "11" || p.NextCursor != "" {
		t.Fatalf("incomplete failed transaction replay: %+v", p)
	}
	if got := calls.Load(); got > 3 {
		t.Fatalf("per-block transport amplification: %d native requests, want batch + receipt + fresh anchor", got)
	}
}
func TestNativeSettlementRangeRejectsBadEvidence(t *testing.T) {
	for _, fault := range []string{"chain", "code", "height", "hash", "body", "missing"} {
		t.Run(fault, func(t *testing.T) {
			a, _ := nativeSettlementFixture(t, fault)
			if _, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100}); e == nil {
				t.Fatal("invalid native batch accepted")
			}
		})
	}
}
func TestNativeSettlementRangeFallsBackWithoutBatchTransport(t *testing.T) {
	a, _ := nativeSettlementFixture(t, "no-batch")
	p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Operations) != 1 || p.Operations[0].Success {
		t.Fatal("batch fallback lost failed direct transaction")
	}
	// Preserve compatibility of the ordinary transaction lookup as well.
	_, e = a.Transactions.(SettlementReader).BlockByNumber(context.Background(), big.NewInt(11))
	if e != nil {
		t.Fatal(e)
	}
}

func TestNativeSettlementRangeBoundsTransportForEmptyPage(t *testing.T) {
	a, calls := nativeSettlementFixture(t, "", make([]int, 16)...)
	p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Operations) != 0 || p.NextCursor != "" {
		t.Fatal("empty page incomplete")
	}
	if calls.Load() > 6 {
		t.Fatalf("too many transport requests for 16 empty blocks: %d", calls.Load())
	}
}
func TestNativeSettlementRangeRetainsDensePageContinuation(t *testing.T) {
	a, _ := nativeSettlementFixture(t, "", 600)
	cursor := ""
	seen := map[common.Hash]bool{}
	for page := 0; page < 7; page++ {
		p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100, Cursor: cursor})
		if e != nil {
			t.Fatal(e)
		}
		for _, op := range p.Operations {
			if op.Success || seen[op.TxHash] {
				t.Fatal("duplicate or missing failed transaction")
			}
			seen[op.TxHash] = true
		}
		cursor = p.NextCursor
		if cursor == "" {
			break
		}
	}
	if cursor != "" || len(seen) != 600 {
		t.Fatalf("dense paging lost transactions: %d", len(seen))
	}
}

func TestNativeSettlementHandlesArrayBatchLimit(t *testing.T) {
	a, _ := nativeSettlementFixture(t, "array-batch-limit")
	p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Operations) != 1 {
		t.Fatal("batch fallback lost transaction")
	}
}
func TestNativeSettlementUncleFallbackPreservesVerifiedTransactions(t *testing.T) {
	a, _ := nativeSettlementFixture(t, "uncle-body")
	p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Operations) != 1 || p.Operations[0].Success {
		t.Fatal("uncle body fallback lost original verified failed transaction")
	}
}
func TestNativeSettlementDoesNotFetchUnusedDensePageGroups(t *testing.T) {
	counts := make([]int, 16)
	counts[0] = 600
	a, _ := nativeSettlementFixture(t, "later-block", counts...)
	p, e := a.GetSettlementOperations(context.Background(), OperationsQuery{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Operations) != 100 || p.NextCursor == "" {
		t.Fatal("dense first page incomplete")
	}
}
