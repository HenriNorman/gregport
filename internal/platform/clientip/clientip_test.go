package clientip

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestFromRequestUsesRemoteByDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.10:4321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	if got := FromRequest(r, Config{}); got != "203.0.113.10" {
		t.Fatalf("got %q", got)
	}
}

func TestFromRequestIgnoresForwardedFromUntrustedPeer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.10:4321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")

	cfg := Config{
		TrustProxyHeaders: true,
		TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}
	if got := FromRequest(r, cfg); got != "203.0.113.10" {
		t.Fatalf("got %q", got)
	}
}

func TestFromRequestUsesForwardedChain(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:4321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.6")

	cfg := Config{
		TrustProxyHeaders: true,
		TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}
	if got := FromRequest(r, cfg); got != "198.51.100.9" {
		t.Fatalf("got %q", got)
	}
}

func TestFromRequestFallsBackWhenEveryHopIsTrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:4321"
	r.Header.Set("X-Forwarded-For", "10.0.0.6, 10.0.0.7")

	cfg := Config{
		TrustProxyHeaders: true,
		TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}
	if got := FromRequest(r, cfg); got != "10.0.0.6" {
		t.Fatalf("got %q", got)
	}
}

func TestFromRequestHandlesIPv6(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8::1]:4321"
	r.Header.Set("X-Forwarded-For", "2001:db8::2")

	cfg := Config{
		TrustProxyHeaders: true,
		TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")},
	}
	if got := FromRequest(r, cfg); got != "2001:db8::2" {
		t.Fatalf("got %q", got)
	}
}
