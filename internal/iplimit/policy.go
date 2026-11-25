// Package iplimit defines explicit direct-peer exemption and trusted-proxy client
// identity policies. Forwarded client identities never grant exemptions.
package iplimit

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// Nil RequestsPerMinute keeps the service default; explicit zero disables only
// that frequency bucket. ExemptCIDRs accepts IP literals as well as CIDRs.
type Config struct {
	RequestsPerMinute *int     `json:"requestsPerMinute,omitempty"`
	ExemptCIDRs       []string `json:"exemptCIDRs,omitempty"`
	TrustedProxyCIDRs []string `json:"trustedProxyCIDRs,omitempty"`
}
type Policy struct {
	limit    int
	prefixes []netip.Prefix
	proxies  []netip.Prefix
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
	var err error
	p.prefixes, err = parsePrefixes(c.ExemptCIDRs, "exemptCIDRs", false)
	if err != nil {
		return Policy{}, err
	}
	p.proxies, err = parsePrefixes(c.TrustedProxyCIDRs, "trustedProxyCIDRs", true)
	if err != nil {
		return Policy{}, err
	}
	return p, nil
}
func parsePrefixes(entries []string, field string, proxy bool) ([]netip.Prefix, error) {
	if len(entries) > 256 {
		return nil, fmt.Errorf("rateLimit.%s supports at most 256 entries", field)
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, e := netip.ParseAddr(entry)
			if e != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("rateLimit.%s contains an invalid IP/CIDR", field)
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		} else if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("rateLimit.%s contains an ambiguous mapped IPv4 prefix", field)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		if proxy && prefix.Bits() == 0 {
			return nil, fmt.Errorf("rateLimit.%s cannot trust every address", field)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
func contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
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

// Exempt checks only direct non-proxy peers. A trusted proxy and every client
// it forwards remain subject to quotas, including clients in an exempt CIDR.
func (p Policy) Exempt(r *http.Request) bool {
	addr, ok := peer(r)
	return ok && !contains(p.proxies, addr) && contains(p.prefixes, addr)
}

// Key accepts exactly one bare X-Real-IP only from a configured direct proxy.
// Nginx must overwrite that header with its authenticated/restored $remote_addr.
// Missing or malformed metadata falls back to a dedicated nonexempt proxy bucket, which
// also keeps direct kubelet health probes usable without trusting any headers.
func (p Policy) Key(r *http.Request) string {
	addr, ok := peer(r)
	if !ok {
		return "unknown"
	}
	if contains(p.proxies, addr) {
		values := r.Header.Values("X-Real-IP")
		if len(values) == 1 {
			client, err := netip.ParseAddr(strings.TrimSpace(values[0]))
			if err == nil && client.Zone() == "" {
				client = client.Unmap()
				if !client.IsUnspecified() && !client.IsMulticast() {
					return client.String()
				}
			}
		}
		return "proxy:" + addr.String()
	}
	return addr.String()
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
	out.TrustedProxyCIDRs = append([]string(nil), c.TrustedProxyCIDRs...)
	if c.RequestsPerMinute != nil {
		rate := *c.RequestsPerMinute
		out.RequestsPerMinute = &rate
	}
	return &out
}
