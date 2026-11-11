package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteDetectionMethodsAreUnavailable(t *testing.T) {
	for _, method := range []string{"invisi_detectNotes", "invisi_detectCreditStates"} {
		for _, authorization := range []string{"", "Bearer ignored"} {
			f, r := anchor()
			result := call(t, testServer(t, f), method, []any{r, "0x01"}, authorization)
			if result.Error == nil || result.Error.Code != -32601 {
				t.Fatalf("%s: expected method not found, got %+v", method, result)
			}
			if f.outputReads != 0 || f.eventReads != 0 {
				t.Fatal("removed method accessed public history")
			}
		}
	}
}

func TestPublicStatusOmitsDetectionMetadata(t *testing.T) {
	f, _ := anchor()
	result := call(t, testServer(t, f), "invisi_getStatus", []any{}, "")
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	var status map[string]json.RawMessage
	if err := json.Unmarshal(result.Result, &status); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"detectionEnabled", "checkedBits"} {
		if _, ok := status[field]; ok {
			t.Fatalf("status exposes %s", field)
		}
	}
}
func TestRemovedDetectionBatchPreservesPublicReads(t *testing.T) {
	f, _ := anchor()
	h := testServer(t, f)
	body := `[{"jsonrpc":"2.0","id":1,"method":"invisi_detectNotes","params":[]},{"jsonrpc":"2.0","id":2,"method":"invisi_detectCreditStates","params":[]},{"jsonrpc":"2.0","id":3,"method":"invisi_getStatus","params":[]}]`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var results []response
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("unexpected batch: %s", w.Body.String())
	}
	for _, result := range results {
		if result.ID == 3 {
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			continue
		}
		if result.Error == nil || result.Error.Code != -32601 {
			t.Fatalf("removed method result: %+v", result)
		}
	}
}
