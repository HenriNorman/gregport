package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// ErrBlockedAddress is returned when a destination resolves to an address the
// webhook sender refuses to contact. It is deliberately opaque to the caller:
// the API only reports that the URL is invalid.
var ErrBlockedAddress = errors.New("webhooks: destination address is not allowed")

// Resolver resolves a hostname to addresses. net.DefaultResolver satisfies it;
// tests substitute a deterministic fake.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// forbiddenPrefixes covers loopback, private, link-local, CGNAT, documentation,
// benchmarking, multicast and reserved ranges. Cloud metadata services live in
// link-local (169.254.0.0/16, fe80::/10), so they are rejected too.
var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// IsForbiddenAddress reports whether addr may not be contacted. IPv4-mapped
// IPv6 addresses are unmapped first so ::ffff:127.0.0.1 cannot slip through.
func IsForbiddenAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ValidateDestination checks a webhook URL and every address its host resolves
// to. It is called when a subscription is created or updated, so a private
// address is rejected before anything is stored. The dialer repeats the check
// at connection time to defeat DNS rebinding.
func ValidateDestination(ctx context.Context, rawURL string, resolver Resolver) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || len(trimmed) > 2048 {
		return "", ErrInvalidURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", ErrInvalidURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidURL
	}
	if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", ErrInvalidURL
	}
	host := parsed.Hostname()
	if host == "" {
		return "", ErrInvalidURL
	}
	if port := parsed.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return "", ErrInvalidURL
		}
	}

	if literal, err := netip.ParseAddr(host); err == nil {
		if IsForbiddenAddress(literal) {
			return "", ErrInvalidURL
		}
		return parsed.String(), nil
	}

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return "", ErrInvalidURL
	}
	for _, addr := range addrs {
		if IsForbiddenAddress(addr) {
			return "", ErrInvalidURL
		}
	}
	return parsed.String(), nil
}

// blockedDialControl is installed on the webhook dialer. The address passed to
// Control is the resolved IP:port, so checking it here prevents a DNS answer
// from changing between URL validation and connection.
func blockedDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
	}
	if IsForbiddenAddress(addr) {
		return ErrBlockedAddress
	}
	return nil
}
