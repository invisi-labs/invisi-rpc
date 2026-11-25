package iplimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestTrustedProxyClientIdentityAndExemptionBoundary(t *testing.T) {
	raw := []byte(`{"exemptCIDRs":["100.65.0.0/16"],"trustedProxyCIDRs":["10.1.0.21/32","2001:db8:ffff::/64"]}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	p, err := New(&cfg, 120)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the compiled policy rather than a public transport wrapper.
	resolver, ok := any(p).(interface{ Key(*http.Request) string })
	if !ok {
		t.Fatal("missing trusted-proxy client identity resolution")
	}
	for _, tc := range []struct {
		peer    string
		headers []string
		want    string
		exempt  bool
	}{
		{"10.1.0.21:1", []string{"203.0.113.1"}, "203.0.113.1", false},
		{"10.1.0.21:1", []string{"203.0.113.2"}, "203.0.113.2", false},
		{"10.1.0.21:1", []string{"100.65.1.1"}, "100.65.1.1", false},
		{"10.1.0.11:1", []string{"100.65.1.1"}, "10.1.0.11", false},
		{"100.65.1.1:1", []string{"203.0.113.1"}, "100.65.1.1", true},
		{"10.1.0.21:1", nil, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{""}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"bad"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"203.0.113.1, 100.65.1.1"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"203.0.113.1", "203.0.113.2"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"fe80::1%eth0"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"0.0.0.0"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"::"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"::ffff:0.0.0.0"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"224.0.0.1"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"::ffff:224.0.0.1"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"::ffff:203.0.113.1%eth0"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"192.0.2.1:80"}, "proxy:10.1.0.21", false},
		{"10.1.0.21:1", []string{"2001:0db8::1"}, "2001:db8::1", false},
		{"10.1.0.21:1", []string{"::ffff:203.0.113.1"}, "203.0.113.1", false},
		{"[2001:db8:ffff::1]:1", []string{"203.0.113.1"}, "203.0.113.1", false},
		{"[::ffff:10.1.0.21]:1", []string{"203.0.113.1"}, "203.0.113.1", false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		for _, h := range tc.headers {
			r.Header.Add("X-Real-IP", h)
		}
		r.Header.Set("X-Forwarded-For", "100.65.2.2")
		r.Header.Set("Forwarded", "for=100.65.2.2")
		r.Header.Set("CF-Connecting-IP", "100.65.2.2")
		if got := resolver.Key(r); got != tc.want {
			t.Errorf("%s %v key %q want %q", tc.peer, tc.headers, got, tc.want)
		}
		if got := p.Exempt(r); got != tc.exempt {
			t.Errorf("%s %v exemption %v want %v", tc.peer, tc.headers, got, tc.exempt)
		}
	}
	// Proxy trust takes precedence if an operator explicitly overlaps the lists.
	json.Unmarshal([]byte(`{"exemptCIDRs":["100.65.0.0/16"],"trustedProxyCIDRs":["100.65.1.1/32"]}`), &cfg)
	p, err = New(&cfg, 120)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "100.65.1.1:1"
	r.Header.Set("X-Real-IP", "100.65.2.2")
	if p.Exempt(r) {
		t.Fatal("trusted proxy gained a direct-peer exemption")
	}
}
func TestProxyConfigurationValidationAndClone(t *testing.T) {
	for _, entry := range []string{"bad", "", "10.1.0.21:80", "fe80::1%eth0", "0.0.0.0/0", "::/0"} {
		var c Config
		raw, _ := json.Marshal(map[string]any{"trustedProxyCIDRs": []string{entry}})
		json.Unmarshal(raw, &c)
		if _, err := New(&c, 120); err == nil {
			t.Errorf("invalid/broad proxy %q accepted", entry)
		}
	}
	var c Config
	raw, _ := json.Marshal(map[string]any{"trustedProxyCIDRs": make([]string, 257)})
	json.Unmarshal(raw, &c)
	if _, err := New(&c, 120); err == nil {
		t.Fatal("unbounded proxy list accepted")
	}
	json.Unmarshal([]byte(`{"trustedProxyCIDRs":["10.1.0.21/32"]}`), &c)
	cloned := Clone(&c)
	field := reflect.ValueOf(&c).Elem().FieldByName("TrustedProxyCIDRs")
	if !field.IsValid() {
		t.Fatal("missing TrustedProxyCIDRs configuration")
	}
	field.Index(0).SetString("10.1.0.22/32")
	raw, _ = json.Marshal(cloned)
	if string(raw) != `{"trustedProxyCIDRs":["10.1.0.21/32"]}` {
		t.Fatalf("proxy trust lost/aliased during clone: %s", raw)
	}
}
