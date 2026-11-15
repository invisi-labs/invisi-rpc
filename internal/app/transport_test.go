package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterTransportJSON(t *testing.T) {
	for name, h := range map[string]http.Handler{"static": chainRouter(nil), "live": &liveRouter{}} {
		for _, tc := range []struct {
			method, path string
			status       int
			allow        string
		}{{"POST", "/v1", 405, "GET"}, {"POST", "/v1/", 405, "GET"}, {"GET", "/v2", 404, ""}, {"POST", "/v1/chains/031337", 404, ""}, {"GET", "/v1", 200, ""}, {"GET", "/v1/", 200, ""}} {
			t.Run(name+tc.method+tc.path, func(t *testing.T) {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
				var data map[string]json.RawMessage
				if w.Code != tc.status || w.Header().Get("Allow") != tc.allow || w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &data) != nil {
					t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
				}
				if tc.status != 200 && (string(data["id"]) != "null" || data["error"] == nil) {
					t.Fatalf("missing error: %s", w.Body)
				}
			})
		}
	}
}
