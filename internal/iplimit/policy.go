// Package iplimit defines portable direct-peer IP rate policies. It never trusts
// forwarding headers: an HTTP proxy is a client unless a separate trusted-proxy
// layer has explicitly authenticated and replaced the connection identity.
package iplimit

import (
	"fmt"
	"net/http"
	"net/netip"
)

// Nil RequestsPerMinute keeps the service default; explicit zero disables only
// that frequency bucket. ExemptCIDRs accepts IP literals as well as CIDRs.
type Config struct {
	RequestsPerMinute *int     `json:"requestsPerMinute,omitempty"`
	ExemptCIDRs       []string `json:"exemptCIDRs,omitempty"`
}
type Policy struct {
	limit    int
	prefixes []netip.Prefix
}

func New(c *Config, defaultRate int) (Policy, error) {
	p := Policy{limit: defaultRate}
	if c != nil && c.RequestsPerMinute != nil {
		p.limit = *c.RequestsPerMinute
	}
	if p.limit < 0 || p.limit > 1000000 {
		return Policy{}, fmt.Errorf("rateLimit.requestsPerMinute must be 0..1000000")
	}
	if c == nil {
		return p, nil
	}
	if len(c.ExemptCIDRs) > 256 {
		return Policy{}, fmt.Errorf("rateLimit.exemptCIDRs supports at most 256 entries")
	}
	for _, entry := range c.ExemptCIDRs {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, e := netip.ParseAddr(entry)
			if e != nil || addr.Zone() != "" {
				return Policy{}, fmt.Errorf("rateLimit.exemptCIDRs contains an invalid IP/CIDR")
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		} else if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return Policy{}, fmt.Errorf("rateLimit.exemptCIDRs contains an ambiguous mapped IPv4 prefix")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		p.prefixes = append(p.prefixes, prefix.Masked())
	}
	return p, nil
}
func (p Policy) Limit() int { return p.limit }
func peer(r *http.Request) (netip.Addr, bool) {
	addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
	var addr netip.Addr
	if err == nil {
		addr = addrPort.Addr()
	} else {
		addr, err = netip.ParseAddr(r.RemoteAddr)
	}
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
func (p Policy) Exempt(r *http.Request) bool {
	addr, ok := peer(r)
	if !ok {
		return false
	}
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Key canonicalizes equivalent IPv4/IPv6 spellings without inspecting headers.
func Key(r *http.Request) string {
	if addr, ok := peer(r); ok {
		return addr.String()
	}
	return "unknown"
}
func Clone(c *Config) *Config {
	if c == nil {
		return nil
	}
	out := *c
	out.ExemptCIDRs = append([]string(nil), c.ExemptCIDRs...)
	if c.RequestsPerMinute != nil {
		rate := *c.RequestsPerMinute
		out.RequestsPerMinute = &rate
	}
	return &out
}
