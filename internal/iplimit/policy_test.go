package iplimit

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPeerExemptionsAndForwardedSpoofing(t *testing.T) {
	p, err := New(&Config{ExemptCIDRs: []string{"100.65.0.0/16", "192.0.2.8", "2001:db8::/64"}}, 120)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		peer string
		want bool
	}{
		{"100.65.0.1:123", true}, {"100.65.255.255:9", true}, {"100.64.255.255:9", false}, {"100.66.0.0:9", false},
		{"192.0.2.8:9", true}, {"192.0.2.9:9", false}, {"[2001:db8::1]:9", true}, {"[2001:db8:0:1::1]:9", false},
		{"[::ffff:100.65.1.1]:9", true}, {"10.1.0.21:9", false}, {"", false}, {"invalid", false}, {"[fe80::1%eth0]:9", false},
	} {
		r := httptest.NewRequest("GET", "http://example.com/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", "100.65.1.1")
		r.Header.Set("X-Real-IP", "100.65.1.1")
		r.Header.Set("Forwarded", "for=100.65.1.1")
		if got := p.Exempt(r); got != tc.want {
			t.Errorf("peer %q exemption=%v want %v", tc.peer, got, tc.want)
		}
	}
}
func TestRateConfigurationDefaultsAndValidation(t *testing.T) {
	for _, rate := range []int{0, 60, 120} {
		p, err := New(nil, rate)
		if err != nil || p.Limit() != rate {
			t.Fatalf("default %d: %v %v", rate, p, err)
		}
	}
	for _, rate := range []int{0, 1, 999, 1000000} {
		p, err := New(&Config{RequestsPerMinute: &rate}, 120)
		if err != nil || p.Limit() != rate {
			t.Fatalf("override %d: %v %v", rate, p, err)
		}
	}
	for _, rate := range []int{-1, 1000001} {
		if _, err := New(&Config{RequestsPerMinute: &rate}, 120); err == nil {
			t.Fatalf("invalid rate %d accepted", rate)
		}
	}
	for _, entry := range []string{"", "localhost", "100.65.1.1:80", "100.65.0.0/33", "2001:db8::/129", "10.0.0.999", "fe80::1%eth0"} {
		if _, err := New(&Config{ExemptCIDRs: []string{entry}}, 120); err == nil {
			t.Errorf("invalid IP/CIDR %q accepted", entry)
		}
	}
	if _, err := New(&Config{ExemptCIDRs: make([]string, 257)}, 120); err == nil {
		t.Fatal("unbounded allowlist accepted")
	}
}
func TestPolicyAndClonedConfigDoNotAliasInput(t *testing.T) {
	rate := 60
	c := &Config{RequestsPerMinute: &rate, ExemptCIDRs: []string{"100.65.0.0/16"}}
	cloned := Clone(c)
	p, err := New(c, 120)
	if err != nil {
		t.Fatal(err)
	}
	c.ExemptCIDRs[0] = "0.0.0.0/0"
	*c.RequestsPerMinute = 999
	if p.Limit() != 60 || *cloned.RequestsPerMinute != 60 || cloned.ExemptCIDRs[0] != "100.65.0.0/16" {
		t.Fatal("policy/config aliases caller data")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.1:1"
	if p.Exempt(r) {
		t.Fatal("caller mutation changed policy")
	}
	if Clone(nil) != nil || reflect.DeepEqual(cloned, c) {
		t.Fatal("invalid clone")
	}
}
func TestCanonicalPeerIdentity(t *testing.T) {
	for _, tc := range []struct{ peer, want string }{{"100.65.1.1:1", "100.65.1.1"}, {"[::ffff:100.65.1.1]:2", "100.65.1.1"}, {"[2001:0db8::1]:2", "2001:db8::1"}, {"invalid", "unknown"}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		if got := Key(r); got != tc.want {
			t.Fatalf("identity %q = %q want %q", tc.peer, got, tc.want)
		}
	}
}
