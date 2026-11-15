package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTransportRejections(t *testing.T) {
	for _, tc := range []struct {
		name, method, media, body string
		length                    int64
		status                    int
		allow                     string
	}{
		{"method", "GET", "", "", 0, 405, "POST"},
		{"unsupported media", "POST", "text/plain; secret=private", `{}`, 2, 415, ""},
		{"malformed media", "POST", "application/json; broken", `{}`, 2, 415, ""},
		{"known overflow", "POST", "application/json", strings.Repeat(" ", (1<<20)+1), (1 << 20) + 1, 413, ""},
		{"streamed overflow", "POST", "application/json", `{"jsonrpc":"2.0","id":1,"method":"invisi_getEvents","params":[{"fromBlock":8,"toBlock":10,"blockHash":"0x0000000000000000000000000000000000000000000000000000000000001234"}]}` + strings.Repeat(" ", 1<<20), -1, 413, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, _ := anchor()
			h := testServer(t, backend)
			req := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
			req.ContentLength = tc.length
			req.Header.Set("Content-Type", tc.media)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status || w.Header().Get("Allow") != tc.allow {
				t.Fatalf("status=%d Allow=%q body=%s", w.Code, w.Header().Get("Allow"), w.Body)
			}
			var value map[string]json.RawMessage
			if w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &value) != nil || string(value["id"]) != "null" || value["error"] == nil {
				t.Fatalf("unsanitized JSON transport failure: %s", w.Body)
			}
			if strings.Contains(w.Body.String(), "private") || backend.eventReads != 0 {
				t.Fatalf("request leaked or dispatched: %s reads=%d", w.Body, backend.eventReads)
			}
		})
	}
}

func TestHTTPMediaCompatibilityAndNotifications(t *testing.T) {
	for _, media := range []string{"application/json", "application/json; charset=utf-8", "application/json-rpc", "application/jsonrequest"} {
		h := testServer(t, &fakeBackend{})
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`[{"jsonrpc":"2.0","method":"invisi_getVersion"},{"jsonrpc":"2.0","id":"test","method":"invisi_getVersion","params":[]}]`))
		req.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var results []struct {
			ID string `json:"id"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &results) != nil || len(results) != 1 || results[0].ID != "test" {
			t.Fatalf("%s: %d %s", media, w.Code, w.Body)
		}
	}
}
