package rpc

import (
	"encoding/json"
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
func TestDefaultIPFrequencyEnabledAndExplicitZeroOptOut(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h, err := LimitIPRequests(handler, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 121; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1", nil))
		want := 204
		if i == 120 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("request %d status %d want %d", i+1, w.Code, want)
		}
	}
	zero := 0
	h, err = LimitIPRequests(handler, &iplimit.Config{RequestsPerMinute: &zero})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1", nil))
		if w.Code != 204 {
			t.Fatal("explicit zero did not opt out")
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

func TestTrustedNginxSeparatesClientQuotas(t *testing.T) {
	rate := 1
	var cfg iplimit.Config
	if err := json.Unmarshal([]byte(`{"trustedProxyCIDRs":["10.1.0.21/32"],"exemptCIDRs":["100.65.0.0/16"]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.RequestsPerMinute = &rate
	h, err := LimitIPRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		peer, client string
		want         int
	}{
		{"10.1.0.21:1", "203.0.113.1", 204}, {"10.1.0.21:2", "203.0.113.1", 429},
		{"10.1.0.21:3", "203.0.113.2", 204}, {"10.1.0.22:1", "203.0.113.3", 204}, {"10.1.0.22:2", "203.0.113.4", 429},
		{"10.1.0.21:1", "100.65.1.1", 204}, {"10.1.0.21:2", "100.65.1.1", 429},
		{"100.65.1.1:1", "203.0.113.1", 204}, {"100.65.1.1:1", "203.0.113.1", 204},
	} {
		r := httptest.NewRequest("GET", "/v1", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Real-IP", tc.client)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s / %s = %d want %d", tc.peer, tc.client, w.Code, tc.want)
		}
	}
}

func TestTrustedProxyProbeBucketDoesNotShareVisitorQuota(t *testing.T) {
	rate := 1
	var cfg iplimit.Config
	json.Unmarshal([]byte(`{"trustedProxyCIDRs":["10.1.0.21/32"]}`), &cfg)
	cfg.RequestsPerMinute = &rate
	h, err := LimitIPRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		header string
		want   int
	}{{"10.1.0.21", 204}, {"10.1.0.21", 429}, {"", 204}, {"", 429}, {"203.0.113.9", 204}} {
		r := httptest.NewRequest("GET", "/v1", nil)
		r.RemoteAddr = "10.1.0.21:1"
		if tc.header != "" {
			r.Header.Set("X-Real-IP", tc.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("header %q status %d want %d", tc.header, w.Code, tc.want)
		}
	}
}
