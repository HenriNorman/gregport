package cache

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOrgKeysAlwaysContainOrgID(t *testing.T) {
	orgID := uuid.New()
	policyID := uuid.New()
	userID := uuid.New()
	keyID := uuid.New()

	tests := []struct {
		name string
		key  func() (string, error)
		want string
	}{
		{"settings", func() (string, error) { return OrgSettingsKey(orgID) }, "org:" + orgID.String() + ":settings"},
		{"sla list", func() (string, error) { return SLAPoliciesKey(orgID) }, "org:" + orgID.String() + ":sla_policies"},
		{"sla item", func() (string, error) { return SLAPolicyKey(orgID, policyID) }, "org:" + orgID.String() + ":sla_policy:" + policyID.String()},
		{"dashboard", func() (string, error) { return DashboardCountsKey(orgID) }, "org:" + orgID.String() + ":dashboard:counts"},
		{"user limit", func() (string, error) { return UserRateLimitKey(orgID, userID) }, "org:" + orgID.String() + ":ratelimit:user:" + userID.String()},
		{"api key limit", func() (string, error) { return APIKeyRateLimitKey(orgID, keyID) }, "org:" + orgID.String() + ":ratelimit:apikey:" + keyID.String()},
		{"api key last used", func() (string, error) { return APIKeyLastUsedKey(orgID, keyID) }, "org:" + orgID.String() + ":apikey:" + keyID.String() + ":last_used"},
	}

	seen := make(map[string]string, len(tests))
	for _, tt := range tests {
		key, err := tt.key()
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if key != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, key, tt.want)
		}
		if !strings.Contains(key, orgID.String()) {
			t.Errorf("%s: key %q is missing the org id", tt.name, key)
		}
		if previous, ok := seen[key]; ok {
			t.Errorf("%s collides with %s: %q", tt.name, previous, key)
		}
		seen[key] = tt.name
	}
}

func TestOrgKeyRejectsMissingTenant(t *testing.T) {
	if _, err := OrgKey(uuid.Nil, "settings"); !errors.Is(err, ErrMissingOrgID) {
		t.Fatalf("expected ErrMissingOrgID, got %v", err)
	}
	if _, err := OrgSettingsKey(uuid.Nil); !errors.Is(err, ErrMissingOrgID) {
		t.Fatalf("expected ErrMissingOrgID, got %v", err)
	}
}

func TestOrgKeyRejectsEmptyParts(t *testing.T) {
	orgID := uuid.New()

	if _, err := OrgKey(orgID); !errors.Is(err, ErrEmptyKeyPart) {
		t.Fatalf("expected ErrEmptyKeyPart, got %v", err)
	}
	if _, err := OrgKey(orgID, "settings", " "); !errors.Is(err, ErrEmptyKeyPart) {
		t.Fatalf("expected ErrEmptyKeyPart, got %v", err)
	}
	if _, err := SLAPolicyKey(orgID, uuid.Nil); !errors.Is(err, ErrEmptyKeyPart) {
		t.Fatalf("expected ErrEmptyKeyPart, got %v", err)
	}
	if _, err := UserRateLimitKey(orgID, uuid.Nil); !errors.Is(err, ErrEmptyKeyPart) {
		t.Fatalf("expected ErrEmptyKeyPart, got %v", err)
	}
	if _, err := APIKeyRateLimitKey(orgID, uuid.Nil); !errors.Is(err, ErrEmptyKeyPart) {
		t.Fatalf("expected ErrEmptyKeyPart, got %v", err)
	}
	if _, err := APIKeyLastUsedKey(orgID, uuid.Nil); !errors.Is(err, ErrEmptyKeyPart) {
		t.Fatalf("expected ErrEmptyKeyPart, got %v", err)
	}
}

func TestPreAuthenticationKeys(t *testing.T) {
	if got := PublicIPRateLimitKey("203.0.113.7"); got != "ratelimit:ip:203.0.113.7" {
		t.Fatalf("public ip key: %q", got)
	}
	if got := ScopedPublicIPRateLimitKey("ratings", "203.0.113.7"); got != "ratelimit:ip:ratings:203.0.113.7" {
		t.Fatalf("scoped public ip key: %q", got)
	}
	if got := LoginRateLimitKey("203.0.113.7", "hash"); got != "ratelimit:login:203.0.113.7:hash" {
		t.Fatalf("login key: %q", got)
	}
	if got := LoginIPRateLimitKey("203.0.113.7"); got != "ratelimit:login:203.0.113.7" {
		t.Fatalf("login ip key: %q", got)
	}
}
