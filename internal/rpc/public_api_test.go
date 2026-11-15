package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

const privateDiagnostic = "https://user:credential@rpc.invalid/private secret backend diagnostic"

type privateCheckpointBackend struct {
	*fakeBackend
	err error
}

func (f *privateCheckpointBackend) Checkpoint(context.Context) (*model.Block, error) {
	return nil, f.err
}

type privateCodeReader struct {
	stateFake
	err error
}

func (f *privateCodeReader) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return nil, f.err
}

func (f *privateCodeReader) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return nil, f.err
}

type privateNodeError struct{ code int }

func (e privateNodeError) Error() string  { return privateDiagnostic }
func (e privateNodeError) ErrorCode() int { return e.code }
func (e privateNodeError) ErrorData() any { return map[string]any{"debug": privateDiagnostic} }

func TestPublicAPISanitizesBackendAndNodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"ordinary", errors.New(privateDiagnostic), -32000},
		{"coded", privateNodeError{-32042}, -32042},
		{"wrapped coded", fmt.Errorf("wrapped %s: %w", privateDiagnostic, privateNodeError{-32602}), -32602},
	} {
		for _, source := range []string{"store", "node", "protocol call"} {
			t.Run(tc.name+"/"+source, func(t *testing.T) {
				a := &API{Store: &privateCheckpointBackend{&fakeBackend{}, tc.err}, ChainState: &privateCodeReader{err: tc.err}, Deployment: model.Deployment{Protocol: common.HexToAddress("1")}}
				method, params := "invisi_getStatus", `[]`
				if source == "node" {
					method = "invisi_readChainState"
					params = `[{"kind":"code","address":"0x0000000000000000000000000000000000000001"}]`
				}
				if source == "protocol call" {
					method = "invisi_readChainState"
					raw, err := json.Marshal([]any{ChainStateRequest{Kind: "call", Address: a.Deployment.Protocol, Data: crypto.Keccak256([]byte("VERSION()"))[:4]}})
					if err != nil {
						t.Fatal(err)
					}
					params = string(raw)
				}
				h, err := HTTPHandler(a)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"`+method+`","params":`+params+`}`))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				var out struct {
					Error struct {
						Code    int             `json:"code"`
						Message string          `json:"message"`
						Data    json.RawMessage `json:"data"`
					} `json:"error"`
				}
				if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || out.Error.Code != tc.code || out.Error.Message != "Request rejected" || len(out.Error.Data) != 0 || strings.Contains(w.Body.String(), "credential") {
					t.Fatalf("unsafe or changed error: %d %s", w.Code, w.Body)
				}
			})
		}
	}
}

func TestPublicAPINoteSuccessWireUnchanged(t *testing.T) {
	for _, selector := range []any{uint64(10), map[string]string{"finality": "safe"}, map[string]string{"finality": "finalized"}} {
		a, s, _ := confirmedFixture(t)
		encoded, err := json.Marshal(selector)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := (&noteAPI{a}).GetNote(context.Background(), s.lookup.Output.Commitment, encoded)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(direct)
		if err != nil {
			t.Fatal(err)
		}
		h, err := HTTPHandler(a)
		if err != nil {
			t.Fatal(err)
		}
		got := call(t, h, "invisi_getNote", []any{s.lookup.Output.Commitment, selector}, "")
		if got.Error != nil || string(got.Result) != string(want) {
			t.Fatalf("selector %s changed success wire: got %s want %s error=%+v", encoded, got.Result, want, got.Error)
		}
	}
	a := &API{Store: &fakeBackend{}}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getNote", []any{common.HexToHash("1"), map[string]string{"finality": "finalized"}}, "")
	if r.Error != nil || !strings.Contains(string(r.Result), `"reason":"finality_unavailable"`) {
		t.Fatalf("pending result changed: %s %+v", r.Result, r.Error)
	}
}

// Newly exported business methods must not bypass or be omitted by the public
// boundary. noteAPI supplies the intentional raw-selector GetNote override.
func TestPublicAPIInventoryAndParameterTypes(t *testing.T) {
	business := reflect.TypeOf((*API)(nil))
	selected := reflect.TypeOf((*noteAPI)(nil))
	public := reflect.TypeOf((*publicAPI)(nil))
	if business.NumMethod() != public.NumMethod() || selected.NumMethod() != public.NumMethod() {
		t.Fatalf("method inventory differs: API=%d selector=%d public=%d", business.NumMethod(), selected.NumMethod(), public.NumMethod())
	}
	for i := 0; i < public.Elem().NumField(); i++ {
		if public.Elem().Field(i).Anonymous {
			t.Fatal("public boundary must not embed an unguarded receiver")
		}
	}
	for i := 0; i < business.NumMethod(); i++ {
		method := business.Method(i)
		exposed, ok := public.MethodByName(method.Name)
		if !ok {
			t.Fatalf("unguarded or missing public method %s", method.Name)
		}
		wire, ok := selected.MethodByName(method.Name)
		if !ok || exposed.Type.NumIn() != wire.Type.NumIn() {
			t.Fatalf("wire argument count changed for %s", method.Name)
		}
		for j := 1; j < wire.Type.NumIn(); j++ {
			if exposed.Type.In(j) != wire.Type.In(j) {
				t.Fatalf("wire argument %d changed for %s", j, method.Name)
			}
		}
		if exposed.Type.NumOut() != 2 || exposed.Type.Out(0) != reflect.TypeOf((*any)(nil)).Elem() || exposed.Type.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
			t.Fatalf("public result guard missing for %s", method.Name)
		}
	}
}

func TestPublicAPIKeepsDispatcherErrorsAndNull(t *testing.T) {
	h := testServer(t, &fakeBackend{})
	for _, tc := range []struct {
		method string
		params []any
		code   int
	}{
		{"invisi_noSuchMethod", nil, -32601},
		{"invisi_getBlock", []any{"not a block"}, -32602},
		{"invisi_isNoteSpent", []any{common.Hash{}, uint64(10)}, -32000},
	} {
		r := call(t, h, tc.method, tc.params, "")
		if r.Error == nil || r.Error.Code != tc.code {
			t.Fatalf("changed error code: %+v", r.Error)
		}
		if (tc.code == -32000) != (r.Error.Message == "Request rejected") {
			t.Fatalf("dispatcher/API boundary changed: %+v", r.Error)
		}
	}
	r := call(t, h, "invisi_getBlock", []any{10}, "")
	if r.Error != nil || string(r.Result) != "null" {
		t.Fatalf("nil result changed: %s %+v", r.Result, r.Error)
	}
}

func TestPublicAPIKeepsGethParseErrors(t *testing.T) {
	h := testServer(t, &fakeBackend{})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out.Error.Code != -32700 || out.Error.Message == "Request rejected" {
		t.Fatalf("parse error changed: %d %s", w.Code, w.Body)
	}
}
