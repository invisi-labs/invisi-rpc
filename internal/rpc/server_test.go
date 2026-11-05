package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

type fakeBackend struct {
	head        *model.Block
	outputs     []model.Output
	events      []model.Event
	blockReads  int
	reorgAfter  int
	outputReads int
	eventReads  int
}

func (f *fakeBackend) Checkpoint(context.Context) (*model.Block, error) { return f.head, nil }
func (f *fakeBackend) Block(_ context.Context, n uint64) (*model.Block, error) {
	f.blockReads++
	if f.head == nil || n != f.head.Number {
		return nil, nil
	}
	b := *f.head
	if f.reorgAfter > 0 && f.blockReads > f.reorgAfter {
		b.Hash = common.HexToHash("dead")
	}
	return &b, nil
}
func (f *fakeBackend) Batch(context.Context, uint64) (*model.Batch, error)      { return nil, nil }
func (f *fakeBackend) Roots(context.Context, uint64, int) ([]model.Root, error) { return nil, nil }
func (f *fakeBackend) Membership(context.Context, uint64, uint32, common.Hash) (*model.Membership, error) {
	return nil, nil
}
func (f *fakeBackend) Events(context.Context, uint64, uint64, int) ([]model.Event, error) {
	f.eventReads++
	return f.events, nil
}
func (f *fakeBackend) Outputs(context.Context, uint64, uint64, int) ([]model.Output, error) {
	f.outputReads++
	return f.outputs, nil
}
func (f *fakeBackend) IsSpent(context.Context, common.Hash) (bool, error) { return false, nil }
func testServer(t *testing.T, f *fakeBackend, enabled bool) http.Handler {
	t.Helper()
	a, err := New(f, model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), FromBlock: 8}, 24, enabled, "1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func call(t *testing.T, h http.Handler, method string, params []any, token string) response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var out response
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid RPC JSON %s: %v", w.Body.String(), err)
	}
	if out.JSONRPC != "2.0" || out.ID != 7 {
		t.Fatalf("invalid RPC envelope: %s", w.Body.String())
	}
	return out
}
func anchor() (*fakeBackend, Range) {
	b := &model.Block{Number: 10, Hash: common.HexToHash("1234"), ParentHash: common.HexToHash("1233")}
	return &fakeBackend{head: b}, Range{FromBlock: 8, ToBlock: 10, BlockHash: b.Hash}
}
func TestHTTPStatusAndUnknownMethod(t *testing.T) {
	f, _ := anchor()
	h := testServer(t, f, false)
	r := call(t, h, "invisi_getStatus", []any{}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Result, &raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"apiVersion", "protocolVersion", "deployment", "indexedHead", "detectionEnabled", "checkedBits"} {
		if _, ok := raw[name]; !ok {
			t.Errorf("missing status wire field %q", name)
		}
	}
	var s Status
	if err := json.Unmarshal(r.Result, &s); err != nil {
		t.Fatal(err)
	}
	if s.APIVersion != "v1" || s.ProtocolVersion != "1" || s.Deployment.ChainID != "31337" || s.IndexedHead.Number != 10 || s.DetectionEnabled || s.CheckedBits != 24 {
		t.Fatalf("unexpected status %s", r.Result)
	}
	versionResult := call(t, h, "invisi_getVersion", []any{}, "")
	var version VersionInfo
	if err := json.Unmarshal(versionResult.Result, &version); err != nil || version.APIVersion != "v1" || version.ProtocolVersion != "1" {
		t.Fatalf("unexpected version: %s %v", versionResult.Result, err)
	}
	r = call(t, h, "invisi_nonexistent", []any{}, "")
	if r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("unknown method result %+v", r)
	}
}
func TestHTTPStrictRangeAnchors(t *testing.T) {
	_, good := anchor()
	cases := []struct {
		name string
		r    Range
	}{{"no anchor", Range{FromBlock: 8, ToBlock: 10}}, {"reverse", Range{FromBlock: 11, ToBlock: 10, BlockHash: good.BlockHash}}, {"10001 blocks", Range{FromBlock: 0, ToBlock: 10000, BlockHash: good.BlockHash}}, {"wrong hash", Range{FromBlock: 8, ToBlock: 10, BlockHash: common.HexToHash("ff")}}, {"unknown block", Range{FromBlock: 8, ToBlock: 11, BlockHash: good.BlockHash}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := anchor()
			r := call(t, testServer(t, f, false), "invisi_getEvents", []any{tc.r}, "")
			if r.Error == nil {
				t.Fatalf("accepted invalid range: %s", r.Result)
			}
			if f.eventReads != 0 {
				t.Fatal("queried event data before validating anchor")
			}
		})
	}
	f, _ := anchor()
	f.events = []model.Event{{Name: "Shield"}}
	r := call(t, testServer(t, f, false), "invisi_getEvents", []any{good}, "")
	if r.Error != nil || f.blockReads != 2 {
		t.Fatalf("valid range failed: %+v", r)
	}
	f, _ = anchor()
	f.reorgAfter = 1
	r = call(t, testServer(t, f, false), "invisi_getEvents", []any{good}, "")
	if r.Error == nil || !strings.Contains(r.Error.Message, "reorganized") {
		t.Fatalf("did not reject reorg during query: %+v", r)
	}
}
func TestDisabledDetectionCannotBeEnabledByRequestHeaders(t *testing.T) {
	for _, header := range []string{"", "Bearer arbitrary", "wrong-scheme"} {
		f, rng := anchor()
		result := call(t, testServer(t, f, false), "invisi_detectOutputs", []any{rng, "0x" + strings.Repeat("01", 32)}, header)
		if result.Error == nil || !strings.Contains(result.Error.Message, "disabled") {
			t.Fatalf("disabled detection accepted: %+v", result)
		}
		if f.outputReads != 0 {
			t.Fatal("disabled detection read output data")
		}
	}
}
func TestHTTPDetectionRustVectorAndTransitions(t *testing.T) {
	fixture, err := os.ReadFile("../cryptography/testdata/fmd-rust-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Vectors []struct {
			DetectionKey      string
			OtherDetectionKey string
			Clue              string
			OtherMatches      []bool
		}
	}
	if err = json.Unmarshal(fixture, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Vectors) == 0 {
		t.Fatal("no Rust vectors")
	}
	v := vectors.Vectors[0]
	if v.OtherMatches[23] {
		t.Fatal("fixture must reject wrong key at 24 bits")
	}
	clue, err := hex.DecodeString(v.Clue)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key string
		wantValid bool
	}{{"correct key", v.DetectionKey, true}, {"wrong key", v.OtherDetectionKey, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, rng := anchor()
			f.head.Timestamp = 2678400
			f.outputs = []model.Output{{Block: *f.head, Commitment: common.HexToHash("01"), Clue: clue}, {Block: *f.head, Commitment: common.HexToHash("02"), Clue: []byte{0xff}}}
			f.outputs = append(f.outputs, model.Output{Block: *f.head, Kind: "BatchOutputs", Commitment: common.HexToHash("03")})
			for _, kind := range []string{"Shield", "Transact", "CrossTreeTransact", "CreditCreated", "CreditToppedUp"} {
				f.outputs = append(f.outputs, model.Output{Block: *f.head, Kind: kind, Commitment: common.HexToHash("04")})
			}
			f.events = []model.Event{{Name: "Shield"}, {Block: *f.head, Name: "CreditDraw", Data: json.RawMessage(`{"params":{"remainderCommitments":[]}}`)}, {Name: "CreditCancelled"}}
			transport := call(t, testServer(t, f, true), "invisi_getUpdates", []any{rng}, "")
			if transport.Error != nil {
				t.Fatal(transport.Error)
			}
			var updates Updates
			if err := json.Unmarshal(transport.Result, &updates); err != nil || len(updates.Outputs) != len(f.outputs) {
				t.Fatalf("no-envelope outputs missing from transport: %s (%v)", transport.Result, err)
			}
			r := call(t, testServer(t, f, true), "invisi_detectOutputs", []any{rng, "0x" + tc.key}, "")
			if r.Error != nil {
				t.Fatal(r.Error)
			}
			var result DetectionResult
			if err = json.Unmarshal(r.Result, &result); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.wantValid {
				want = 2
			}
			if result.CheckedBits != 24 || result.BlockHash != rng.BlockHash || len(result.Candidates) != want {
				t.Fatalf("unexpected detection %s", r.Result)
			}
			fallback := result.Candidates[len(result.Candidates)-1]
			if fallback.Output.Block != *f.head {
				t.Fatal("detection lost canonical block context")
			}
			if result.CreditTransitions[0].Block != *f.head {
				t.Fatal("transition lost block context")
			}
			if !fallback.Fallback || fallback.Output.Commitment != f.outputs[1].Commitment {
				t.Fatal("malformed clue was not preserved as fallback")
			}
			if tc.wantValid && (result.Candidates[0].Fallback || result.Candidates[0].Output.Commitment != f.outputs[0].Commitment) {
				t.Fatal("correct Rust vector not matched")
			}
			if len(result.CreditTransitions) != 2 || result.CreditTransitions[0].Name != "CreditDraw" {
				t.Fatalf("output-less CreditDraw transition missing: %s", r.Result)
			}
		})
	}
}
func TestHTTPMethodAndBodyLimits(t *testing.T) {
	f, _ := anchor()
	h := testServer(t, f, false)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, "/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s HTTP %d", method, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat(" ", 1<<20)+`{"jsonrpc":"2.0","id":7,"method":"invisi_getStatus","params":[]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestHTTPExactLengthMalformedCluesFallback(t *testing.T) {
	data, err := os.ReadFile("../cryptography/testdata/fmd-rust-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []struct{ DetectionKey, Clue string }
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	vector := fixture.Vectors[0]
	valid, err := hex.DecodeString(vector.Clue)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"precision23", "invalid point", "noncanonical scalar", "identity point"} {
		t.Run(kind, func(t *testing.T) {
			clue := append([]byte(nil), valid...)
			switch kind {
			case "precision23":
				clue[64] = 23
			case "invalid point":
				clue[0] |= 1
			case "noncanonical scalar":
				for i := 32; i < 64; i++ {
					clue[i] = 255
				}
			case "identity point":
				clear(clue[:32])
			}
			if len(clue) != 68 {
				t.Fatal("fixture is not an exact-length clue")
			}
			f, rng := anchor()
			f.outputs = []model.Output{{Clue: clue, Commitment: common.HexToHash("123")}}
			r := call(t, testServer(t, f, true), "invisi_detectOutputs", []any{rng, "0x" + vector.DetectionKey}, "")
			if r.Error != nil {
				t.Fatal(r.Error)
			}
			var result DetectionResult
			if err = json.Unmarshal(r.Result, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Candidates) != 1 || !result.Candidates[0].Fallback || !bytes.Equal(result.Candidates[0].Output.Clue, clue) {
				t.Fatalf("malformed 68-byte clue lost fallback: %s", r.Result)
			}
		})
	}
}
func TestHTTPInvalidDetectionKeysFailOnEmptyRange(t *testing.T) {
	for _, tc := range []struct{ name, key string }{{"zero", strings.Repeat("00", 32)}, {"noncanonical", strings.Repeat("ff", 32)}} {
		t.Run(tc.name, func(t *testing.T) {
			f, rng := anchor()
			r := call(t, testServer(t, f, true), "invisi_detectOutputs", []any{rng, "0x" + tc.key}, "")
			if r.Error == nil {
				t.Fatalf("invalid 32-byte key accepted on empty range: %s", r.Result)
			}
			if f.outputReads != 0 || f.blockReads != 0 {
				t.Fatal("invalid key read backend before rejection")
			}
		})
	}
}

func TestRejectUnsupportedProtocolVersion(t *testing.T) {
	f, _ := anchor()
	for _, v := range []string{"", "0", "2", "3"} {
		if _, err := New(f, model.Deployment{}, 8, false, v); err == nil {
			t.Fatalf("accepted incompatible Protocol VERSION %q", v)
		}
	}
}

func TestDeploymentWithoutIndexedHead(t *testing.T) {
	d := model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), RuntimeCodeHash: common.HexToHash("4567"), FromBlock: 8, Registry: &model.ContractDeployment{Address: common.HexToAddress("5678"), RuntimeCodeHash: common.HexToHash("6789"), FromBlock: 9}}
	a, err := New(&fakeBackend{}, d, 24, false, "1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	result := call(t, h, "invisi_getDeployment", []any{}, "")
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	var decoded struct {
		VersionInfo
		Deployment model.Deployment `json:"deployment"`
	}
	if err := json.Unmarshal(result.Result, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.APIVersion != "v1" || decoded.ProtocolVersion != "1" || decoded.Deployment.Registry == nil || *decoded.Deployment.Registry != *d.Registry || decoded.Deployment.Protocol != d.Protocol {
		t.Fatalf("wrong deployment: %+v", decoded)
	}
}

func TestPublicDetectionNeedsNoAPIKey(t *testing.T) {
	f, rng := anchor()
	result := call(t, testServer(t, f, true), "invisi_detectOutputs", []any{rng, "0x" + strings.Repeat("01", 32)}, "")
	if result.Error != nil {
		t.Fatalf("enabled public detection rejected: %+v", result.Error)
	}
	if f.outputReads != 1 {
		t.Fatal("enabled detection did not read candidates")
	}
}

func TestDetectionCapacityReturnsRetryableRPCError(t *testing.T) {
	f, rng := anchor()
	api, err := New(f, model.Deployment{ChainID: "31337"}, 24, true, "1")
	if err != nil {
		t.Fatal(err)
	}
	for range cap(api.detectionSlots) {
		api.detectionSlots <- struct{}{}
	}
	handler, err := HTTPHandler(api)
	if err != nil {
		t.Fatal(err)
	}
	result := call(t, handler, "invisi_detectOutputs", []any{rng, "0x" + strings.Repeat("01", 32)}, "")
	if result.Error == nil || result.Error.Code != -32005 {
		t.Fatalf("wrong overload error: %+v", result.Error)
	}
	if f.outputReads != 0 {
		t.Fatal("overloaded detection read backend")
	}
	if status := call(t, handler, "invisi_getStatus", []any{}, ""); status.Error != nil {
		t.Fatal("detection overload blocked ordinary queries")
	}
}

func TestCreditEndpointsPreserveConfirmedBlockContext(t *testing.T) {
	f, rng := anchor()
	b := *f.head
	b.Timestamp = 2678400
	f.events = []model.Event{{Block: b, Name: "CreditDraw", Data: json.RawMessage(`{"params":{"remainderCommitments":[]}}`)}}
	f.outputs = []model.Output{{Block: b, Kind: "CreditDraw", Ciphertext: bytes.Repeat([]byte{5}, 754), Clue: []byte{255}}}
	h := testServer(t, f, true)
	for _, method := range []string{"invisi_getCreditTransitions", "invisi_getUpdates"} {
		params := []any{rng}
		r := call(t, h, method, params, "")
		if r.Error != nil {
			t.Fatalf("%s: %v", method, r.Error)
		}
		var transitions []model.Event
		if method == "invisi_getUpdates" {
			var u Updates
			if err := json.Unmarshal(r.Result, &u); err != nil {
				t.Fatal(err)
			}
			transitions = u.CreditTransitions
			if len(u.Outputs) != 1 || u.Outputs[0].Block != b {
				t.Fatalf("output block missing: %s", r.Result)
			}
		} else if err := json.Unmarshal(r.Result, &transitions); err != nil {
			t.Fatal(err)
		}
		if len(transitions) != 1 || transitions[0].Block != b {
			t.Fatalf("transition block missing: %s", r.Result)
		}
	}
}

func (f *fakeBackend) ReceivingAddress(context.Context, common.Address) (*model.RegisteredReceivingAddress, error) {
	return nil, nil
}

func (f *fakeBackend) Broadcasters(context.Context, model.BroadcasterQuery) (*model.BroadcasterPage, error) {
	return &model.BroadcasterPage{}, nil
}

func TestHTTPRangeBoundaryAndAnchorReads(t *testing.T) {
	for _, method := range []string{"invisi_getEvents", "invisi_getCreditTransitions", "invisi_getUpdates"} {
		t.Run(method, func(t *testing.T) {
			for _, tc := range []struct {
				name                string
				from, to            uint64
				zeroHash, wantError bool
			}{
				{"10000 blocks", 1, 10000, false, false},
				{"10001 blocks", 0, 10000, false, true},
				{"reversed", 10001, 10000, false, true},
				{"zero hash", 1, 10000, true, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f, rng := anchor()
					f.head.Number = tc.to
					rng.FromBlock, rng.ToBlock = tc.from, tc.to
					if tc.zeroHash {
						rng.BlockHash = common.Hash{}
					}
					r := call(t, testServer(t, f, false), method, []any{rng}, "")
					if (r.Error != nil) != tc.wantError {
						t.Fatalf("unexpected result: %+v", r)
					}
					if tc.wantError {
						if f.blockReads != 0 || f.eventReads != 0 || f.outputReads != 0 {
							t.Fatal("invalid range read backend")
						}
					} else if f.blockReads != 2 || f.eventReads != 1 {
						t.Fatalf("anchor reads=%d event reads=%d; want 2 and 1", f.blockReads, f.eventReads)
					}
				})
			}
			f, rng := anchor()
			f.reorgAfter = 1
			r := call(t, testServer(t, f, false), method, []any{rng}, "")
			if r.Error == nil || !strings.Contains(r.Error.Message, "reorganized") {
				t.Fatalf("accepted reorg: %+v", r)
			}
		})
	}
}

func TestHTTPRangeEventCapsBeforeTransitionFilter(t *testing.T) {
	for _, method := range []string{"invisi_getEvents", "invisi_getCreditTransitions", "invisi_getUpdates"} {
		for _, kind := range []string{"count", "bytes"} {
			t.Run(method+"/"+kind, func(t *testing.T) {
				f, rng := anchor()
				if kind == "count" {
					f.events = make([]model.Event, 10000)
				} else {
					// Even non-credit events retain their original response-size cap.
					f.events = []model.Event{{Name: "Shield", Data: json.RawMessage(`"` + strings.Repeat("x", maxRangeResponseBytes) + `"`)}}
				}
				r := call(t, testServer(t, f, false), method, []any{rng}, "")
				if r.Error == nil || !strings.Contains(r.Error.Message, "smaller ranges") {
					t.Fatalf("accepted oversized event data: %+v", r)
				}
			})
		}
	}
}

func (f *fakeBackend) Note(context.Context, common.Hash, uint64) (*model.NoteLookup, error) {
	return nil, nil
}

func TestNoteLookupRPC(t *testing.T) {
	f := &fakeBackend{head: &model.Block{Number: 10, Hash: common.HexToHash("10")}}
	h := testServer(t, f, false)
	if r := call(t, h, "invisi_getNote", []any{common.HexToHash("1"), 10}, ""); r.Error != nil || string(r.Result) != "null" {
		t.Fatalf("missing note: %+v", r)
	}
	if r := call(t, h, "invisi_getNote", []any{common.Hash{}, 10}, ""); r.Error == nil {
		t.Fatal("zero commitment accepted")
	}
	if r := call(t, h, "invisi_getNote", []any{common.HexToHash("1"), 9}, ""); r.Error == nil {
		t.Fatal("unindexed anchor accepted")
	}
	if r := call(t, h, "invisi_getNote", []any{common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"), 10}, ""); r.Error == nil {
		t.Fatal("noncanonical commitment accepted")
	}
	f.reorgAfter = f.blockReads + 1
	if r := call(t, h, "invisi_getNote", []any{common.HexToHash("1"), 10}, ""); r.Error == nil {
		t.Fatal("reorganized anchor accepted")
	}
}
