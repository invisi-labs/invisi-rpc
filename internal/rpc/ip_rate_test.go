package rpc

import (
	"fmt"
	"github.com/invisi-labs/invisi-rpc/internal/iplimit"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPRateLimitsAndDirectPeerExemption(t *testing.T) {
	rate := 1
	h, err := LimitIPRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), &iplimit.Config{RequestsPerMinute: &rate, ExemptCIDRs: []string{"100.65.0.0/16"}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(peer string) int {
		r := httptest.NewRequest("GET", "/v1", nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-For", "100.65.1.1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 429 && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry delay")
		}
		return w.Code
	}
	for _, tc := range []struct {
		peer string
		want int
	}{{"203.0.113.1:1", 204}, {"203.0.113.1:2", 429}, {"203.0.113.2:1", 204}, {"100.65.1.1:1", 204}, {"100.65.1.1:2", 204}, {"10.1.0.21:1", 204}, {"10.1.0.21:2", 429}} {
		if got := call(tc.peer); got != tc.want {
			t.Fatalf("%s=%d want %d", tc.peer, got, tc.want)
		}
	}
}
func TestIPExemptionDoesNotBypassConcurrency(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	rate := 1
	h, err := LimitIPRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(204) }), &iplimit.Config{RequestsPerMinute: &rate, ExemptCIDRs: []string{"100.65.0.0/16"}})
	if err != nil {
		t.Fatal(err)
	}
	h = LimitConcurrentRequests(h, 1)
	request := func() *http.Request {
		r := httptest.NewRequest("GET", "/v1", nil)
		r.RemoteAddr = "100.65.1.1:1"
		return r
	}
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), request()) }()
	<-entered
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request())
	close(release)
	<-done
	if w.Code != 429 {
		t.Fatalf("exemption bypassed global limit: %d", w.Code)
	}
}
func TestDefaultIPFrequencyRemainsDisabled(t *testing.T) {
	h, err := LimitIPRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1", nil))
		if w.Code != 204 {
			t.Fatal("legacy default changed")
		}
	}
	if _, err = LimitIPRequests(h, &iplimit.Config{ExemptCIDRs: []string{"invalid"}}); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

func TestIPRateWindowExpiryAndTrackingBound(t *testing.T) {
	now := time.Unix(100000, 0)
	rate := 1
	h, err := limitIPRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), &iplimit.Config{RequestsPerMinute: &rate}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	call := func(peer string) (int, string) {
		r := httptest.NewRequest("GET", "/v1", nil)
		r.RemoteAddr = peer
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Header().Get("Retry-After")
	}
	for i := 0; i < 4096; i++ {
		peer := fmt.Sprintf("198.18.%d.%d:1", i/256, i%256)
		if status, _ := call(peer); status != 204 {
			t.Fatal("quota map filled prematurely", i, status)
		}
	}
	if status, retry := call("203.0.113.5:1"); status != 429 || retry != "60" {
		t.Fatal("tracking bound ignored", status, retry)
	}
	now = now.Add(61 * time.Second)
	if status, _ := call("203.0.113.5:1"); status != 204 {
		t.Fatal("expired clients not reclaimed", status)
	}
	if status, _ := call("203.0.113.5:2"); status != 429 {
		t.Fatal("same peer bypassed quota by changing port", status)
	}
	now = now.Add(30 * time.Second)
	if status, retry := call("203.0.113.5:3"); status != 429 || retry != "30" {
		t.Fatal("incorrect window backoff", status, retry)
	}
	now = now.Add(30 * time.Second)
	if status, _ := call("203.0.113.5:4"); status != 204 {
		t.Fatal("fixed window did not reset", status)
	}
}
