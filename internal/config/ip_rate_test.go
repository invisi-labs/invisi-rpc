package config

import (
	"github.com/invisi-labs/invisi-rpc/internal/iplimit"
	"reflect"
	"testing"
)

func TestIPRatePolicySurvivesConfigurationRoundTrip(t *testing.T) {
	rate := 15
	c := Config{RateLimit: &iplimit.Config{RequestsPerMinute: &rate, ExemptCIDRs: []string{"100.65.0.0/16", "2001:db8::1"}}}
	dir := t.TempDir()
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDir(dir)
	if err != nil || !reflect.DeepEqual(loaded.RateLimit, c.RateLimit) {
		t.Fatalf("policy lost: %+v %v", loaded.RateLimit, err)
	}
	c.RateLimit.ExemptCIDRs = []string{"invalid"}
	if err = c.Validate(); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
