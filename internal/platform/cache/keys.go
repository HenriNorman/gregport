package cache

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ErrMissingOrgID prevents a tenant-scoped key from being built without a
// tenant. Every cache key for tenant data must contain the org id.
var ErrMissingOrgID = errors.New("cache: org id is required")

// ErrEmptyKeyPart is returned when a key would contain an empty segment.
var ErrEmptyKeyPart = errors.New("cache: key part must not be empty")

// OrgKey builds a tenant-scoped key in the single scheme used by the service:
// org:{orgID}:{part}:{part}...
func OrgKey(orgID uuid.UUID, parts ...string) (string, error) {
	if orgID == uuid.Nil {
		return "", ErrMissingOrgID
	}
	if len(parts) == 0 {
		return "", ErrEmptyKeyPart
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", ErrEmptyKeyPart
		}
	}
	return "org:" + orgID.String() + ":" + strings.Join(parts, ":"), nil
}

// OrgSettingsKey is the cached organization settings document.
func OrgSettingsKey(orgID uuid.UUID) (string, error) {
	return OrgKey(orgID, "settings")
}

// SLAPoliciesKey is the cached SLA policy list for an organization.
func SLAPoliciesKey(orgID uuid.UUID) (string, error) {
	return OrgKey(orgID, "sla_policies")
}

// SLAPolicyKey is the cached single SLA policy.
func SLAPolicyKey(orgID, policyID uuid.UUID) (string, error) {
	if policyID == uuid.Nil {
		return "", ErrEmptyKeyPart
	}
	return OrgKey(orgID, "sla_policy", policyID.String())
}

// DashboardCountsKey is the short-lived dashboard ticket count cache.
func DashboardCountsKey(orgID uuid.UUID) (string, error) {
	return OrgKey(orgID, "dashboard", "counts")
}

// UserRateLimitKey is the per-user limiter bucket.
func UserRateLimitKey(orgID, userID uuid.UUID) (string, error) {
	if userID == uuid.Nil {
		return "", ErrEmptyKeyPart
	}
	return OrgKey(orgID, "ratelimit", "user", userID.String())
}

// APIKeyRateLimitKey is the per-API-key limiter bucket.
func APIKeyRateLimitKey(orgID, apiKeyID uuid.UUID) (string, error) {
	if apiKeyID == uuid.Nil {
		return "", ErrEmptyKeyPart
	}
	return OrgKey(orgID, "ratelimit", "apikey", apiKeyID.String())
}

// PublicIPRateLimitKey is the pre-authentication limiter bucket. It is the
// documented exception to tenant-scoped keys because no tenant exists yet.
func PublicIPRateLimitKey(ip string) string {
	return "ratelimit:ip:" + ip
}

// LoginRateLimitKey slows credential stuffing by combining the client IP with
// a hash of the submitted email. The raw email is never part of a key.
func LoginRateLimitKey(ip, emailHash string) string {
	return "ratelimit:login:" + ip + ":" + emailHash
}

// LoginIPRateLimitKey is the fallback login bucket when the body has no
// usable email, so malformed requests cannot bypass the limiter.
func LoginIPRateLimitKey(ip string) string {
	return "ratelimit:login:" + ip
}

// APIKeyLastUsedKey throttles api_keys.last_used_at writes.
func APIKeyLastUsedKey(orgID, apiKeyID uuid.UUID) (string, error) {
	if apiKeyID == uuid.Nil {
		return "", ErrEmptyKeyPart
	}
	return OrgKey(orgID, "apikey", apiKeyID.String(), "last_used")
}
