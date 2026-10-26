package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConcurrentRequestOverloadAndRelease(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := LimitConcurrentRequests(next, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/hold", nil))
	}()
	<-entered
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/v1", nil))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("wrong overload response: %d %v", response.Code, response.Header())
	}
	close(release)
	<-done
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/v1", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("capacity not released: %d", response.Code)
	}
}
