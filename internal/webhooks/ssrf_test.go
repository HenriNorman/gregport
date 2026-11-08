package webhooks

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type fakeResolver struct {
	addrs map[string][]netip.Addr
	err   error
}

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.addrs[host], nil
}

func TestIsForbiddenAddress(t *testing.T) {
	forbidden := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1",
		"255.255.255.255", "::1", "fc00::1", "fe80::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1",
	}
	for _, raw := range forbidden {
		addr := netip.MustParseAddr(raw)
		if !IsForbiddenAddress(addr) {
			t.Fatalf("%s should be forbidden", raw)
		}
	}

	for _, raw := range []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111"} {
		addr := netip.MustParseAddr(raw)
		if IsForbiddenAddress(addr) {
			t.Fatalf("%s should be allowed", raw)
		}
	}
	if !IsForbiddenAddress(netip.Addr{}) {
		t.Fatal("invalid address should be forbidden")
	}
}

func TestValidateDestination(t *testing.T) {
	resolver := fakeResolver{addrs: map[string][]netip.Addr{
		"public.example":  {netip.MustParseAddr("93.184.216.34")},
		"private.example": {netip.MustParseAddr("10.0.0.9")},
		"mixed.example":   {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
	}}

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "public literal", url: "https://93.184.216.34/hooks"},
		{name: "public hostname", url: "https://public.example/hooks"},
		{name: "http allowed", url: "http://public.example:8080/hooks"},
		{name: "private hostname", url: "https://private.example/hooks", wantErr: true},
		{name: "mixed hostname", url: "https://mixed.example/hooks", wantErr: true},
		{name: "loopback", url: "http://127.0.0.1/hooks", wantErr: true},
		{name: "localhost", url: "http://localhost/hooks", wantErr: true},
		{name: "ipv6 loopback", url: "http://[::1]/hooks", wantErr: true},
		{name: "metadata", url: "http://169.254.169.254/latest/meta-data", wantErr: true},
		{name: "private", url: "http://192.168.0.10/hooks", wantErr: true},
		{name: "cgnat", url: "http://100.64.0.1/hooks", wantErr: true},
		{name: "relative", url: "/hooks", wantErr: true},
		{name: "ftp", url: "ftp://public.example/hooks", wantErr: true},
		{name: "empty", url: "", wantErr: true},
		{name: "userinfo", url: "https://user:pass@public.example/hooks", wantErr: true},
		{name: "fragment", url: "https://public.example/hooks#x", wantErr: true},
		{name: "bad port", url: "https://public.example:notaport/hooks", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateDestination(context.Background(), tt.url, resolver)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidURL) {
					t.Fatalf("err=%v, want ErrInvalidURL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if got == "" {
				t.Fatal("empty canonical URL")
			}
		})
	}
}

func TestValidateDestinationResolutionFailure(t *testing.T) {
	resolver := fakeResolver{err: errors.New("dns down")}
	if _, err := ValidateDestination(context.Background(), "https://public.example/hooks", resolver); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("err=%v, want ErrInvalidURL", err)
	}
	if _, err := ValidateDestination(context.Background(), "https://empty.example/hooks", fakeResolver{addrs: map[string][]netip.Addr{}}); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("empty answer err=%v, want ErrInvalidURL", err)
	}
}

func TestBlockedDialControl(t *testing.T) {
	if err := blockedDialControl("tcp", "127.0.0.1:443", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("loopback err=%v", err)
	}
	if err := blockedDialControl("tcp", "[::1]:443", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("ipv6 loopback err=%v", err)
	}
	if err := blockedDialControl("tcp", "169.254.169.254:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("metadata err=%v", err)
	}
	if err := blockedDialControl("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("public err=%v", err)
	}
	if err := blockedDialControl("tcp", "not-an-address", nil); err == nil {
		t.Fatal("expected an error for a malformed address")
	}
}
