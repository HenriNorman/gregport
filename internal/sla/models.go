package sla

import (
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Policy is one tenant's SLA target for a ticket priority. The unique
// (org_id, priority) constraint means an org has at most one policy per
// priority.
type Policy struct {
	ID                   uuid.UUID
	OrgID                uuid.UUID
	Name                 string
	Priority             string
	FirstResponseMinutes int
	ResolutionMinutes    int
	CreatedAt            time.Time
}

// CreatePolicyRequest is the payload for POST /v1/sla-policies.
type CreatePolicyRequest struct {
	Name                 string `json:"name"                   example:"Urgent default"`
	Priority             string `json:"priority"               example:"urgent" enums:"low,normal,high,urgent"`
	FirstResponseMinutes int    `json:"first_response_minutes" example:"15"`
	ResolutionMinutes    int    `json:"resolution_minutes"     example:"120"`
}

// UpdatePolicyRequest is a partial update. Omitted fields keep their value.
type UpdatePolicyRequest struct {
	Name                 *string `json:"name,omitempty"                    example:"Urgent default"`
	Priority             *string `json:"priority,omitempty"                example:"urgent" enums:"low,normal,high,urgent"`
	FirstResponseMinutes *int    `json:"first_response_minutes,omitempty"  example:"15"`
	ResolutionMinutes    *int    `json:"resolution_minutes,omitempty"      example:"120"`
}

// PolicyDTO is the API representation of a policy.
type PolicyDTO struct {
	ID                   uuid.UUID `json:"id"                     example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
	Name                 string    `json:"name"                   example:"Urgent default"`
	Priority             string    `json:"priority"               example:"urgent" enums:"low,normal,high,urgent"`
	FirstResponseMinutes int       `json:"first_response_minutes" example:"15"`
	ResolutionMinutes    int       `json:"resolution_minutes"     example:"120"`
	CreatedAt            time.Time `json:"created_at"             example:"2026-01-15T10:00:00Z"`
}

// PolicyListResponse is the response for GET /v1/sla-policies.
type PolicyListResponse struct {
	Policies []PolicyDTO `json:"policies"`
}

var (
	ErrNotFound     = httpx.NotFound("sla_policy_not_found", "SLA policy not found")
	ErrPolicyExists = httpx.Conflict("sla_policy_exists", "an SLA policy for this priority already exists")
	ErrInvalidInput = httpx.Unprocessable("invalid_sla_policy", "SLA policy is invalid")
	ErrPolicyInUse  = httpx.Conflict("sla_policy_in_use", "SLA policy is referenced by at least one ticket")
)

// ValidPriority reports whether priority is a supported ticket priority.
func ValidPriority(priority string) bool {
	switch priority {
	case "low", "normal", "high", "urgent":
		return true
	default:
		return false
	}
}
