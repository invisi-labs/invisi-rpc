package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestBatchedHeadersValidateResponses(t *testing.T) {
	for _, kind := range []string{"reversed", "hash", "height", "null", "missing", "element_error", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var calls []struct {
					ID json.RawMessage `json:"id"`
				}
				if e := json.NewDecoder(r.Body).Decode(&calls); e != nil {
					t.Error(e)
					return
				}
				if kind == "unsupported" {
					http.Error(w, "no batches", 400)
					return
				}
				replies := []map[string]any{}
				for n, call := range calls {
					h := &types.Header{Number: big.NewInt(int64(n + 10)), Difficulty: big.NewInt(0)}
					if kind == "height" {
						h.Number = big.NewInt(99)
					}
					raw, _ := json.Marshal(h)
					var result map[string]any
					_ = json.Unmarshal(raw, &result)
					if kind == "hash" {
						result["hash"] = "0x0000000000000000000000000000000000000000000000000000000000000001"
					}
					reply := map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result}
					if kind == "null" {
						reply["result"] = nil
					}
					if kind == "element_error" {
						delete(reply, "result")
						reply["error"] = map[string]any{"code": -32000, "message": "unsupported"}
					}
					if kind != "missing" || n != 0 {
						replies = append([]map[string]any{reply}, replies...)
					}
				}
				_ = json.NewEncoder(w).Encode(replies)
			}))
			defer server.Close()
			client, e := ethclient.Dial(server.URL)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			headers, e := NewRPCChain(client).Headers(context.Background(), 10, 11)
			if kind == "reversed" {
				if e != nil || len(headers) != 2 || headers[0].Number.Uint64() != 10 || headers[1].Number.Uint64() != 11 {
					t.Fatal("response ID ordering", e)
				}
			} else if e == nil {
				t.Fatal("invalid batch accepted")
			}
			if kind == "missing" || kind == "element_error" || kind == "unsupported" {
				if !errors.Is(e, ErrBatchUnavailable) {
					t.Fatal("missing capability did not request fallback", e)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, e = NewRPCChain(client).Headers(ctx, 10, 11); !errors.Is(e, context.Canceled) {
				t.Fatal("cancellation lost", e)
			}
		})
	}
}
