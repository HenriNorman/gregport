// Package clientip resolves the client address used for rate limiting and
// audit fields. Forwarded headers are only trusted when the immediate peer is
// an explicitly configured proxy.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Config controls forwarded-header handling.
type Config struct {
	TrustProxyHeaders bool
	TrustedProxies    []netip.Prefix
}

// FromRequest returns the best-known client IP for r.
func FromRequest(r *http.Request, cfg Config) string {
	remote := host(r.RemoteAddr)
	if !cfg.TrustProxyHeaders || !trusted(remote, cfg.TrustedProxies) {
		return remote
	}

	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	var leftmost string
	for i := len(forwarded) - 1; i >= 0; i-- {
		value := strings.TrimSpace(forwarded[i])
		if value == "" {
			continue
		}
		addr, err := netip.ParseAddr(value)
		if err != nil {
			continue
		}
		ip := addr.String()
		leftmost = ip
		if !trusted(ip, cfg.TrustedProxies) {
			return ip
		}
	}
	if leftmost != "" {
		return leftmost
	}
	return remote
}

func host(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func trusted(ip string, prefixes []netip.Prefix) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
