package tickets

import (
	"testing"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func principal(kind requestctx.Kind, role string) requestctx.Principal {
	return requestctx.Principal{
		Kind:    kind,
		ActorID: uuid.New(),
		OrgID:   uuid.New(),
		Role:    role,
	}
}

func TestCanInvite_GrantTable(t *testing.T) {
	cases := []struct {
		name       string
		kind       requestctx.Kind
		role       string
		targetRole string
		want       bool
	}{
		// owner invites
		{"owner->admin", requestctx.KindUser, "owner", "admin", true},
		{"owner->agent", requestctx.KindUser, "owner", "agent", true},
		{"owner->owner", requestctx.KindUser, "owner", "owner", false},

		// admin invites
		{"admin->agent", requestctx.KindUser, "admin", "agent", true},
		{"admin->admin", requestctx.KindUser, "admin", "admin", false},
		{"admin->owner", requestctx.KindUser, "admin", "owner", false},

		// agent invites
		{"agent->agent", requestctx.KindUser, "agent", "agent", false},
		{"agent->admin", requestctx.KindUser, "agent", "admin", false},
		{"agent->owner", requestctx.KindUser, "agent", "owner", false},

		// API keys never invite
		{"apikey->agent", requestctx.KindAPIKey, "agent", "agent", false},
		{"apikey->admin", requestctx.KindAPIKey, "agent", "admin", false},
		{"apikey->owner", requestctx.KindAPIKey, "agent", "owner", false},

		// unknown target role
		{"owner->bogus", requestctx.KindUser, "owner", "bogus", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CanInvite(principal(c.kind, c.role), c.targetRole)
			if got != c.want {
				t.Fatalf("CanInvite(%s, %q) = %v, want %v", c.role, c.targetRole, got, c.want)
			}
		})
	}
}

func TestManageGates_KindFirst(t *testing.T) {
	// API keys have role "agent"; none of the management gates may pass.
	apiKey := principal(requestctx.KindAPIKey, "agent")

	if CanDelete(apiKey) {
		t.Fatal("api_key should not CanDelete")
	}
	if CanManageTeams(apiKey) {
		t.Fatal("api_key should not CanManageTeams")
	}
	if CanManageMembers(apiKey) {
		t.Fatal("api_key should not CanManageMembers")
	}
	if CanManageAPIKeys(apiKey) {
		t.Fatal("api_key should not CanManageAPIKeys")
	}
	if CanManageWebhooks(apiKey) {
		t.Fatal("api_key should not CanManageWebhooks")
	}

	// Owner passes the ones that are owner-gated.
	owner := principal(requestctx.KindUser, "owner")
	if !CanDelete(owner) || !CanManageTeams(owner) || !CanManageMembers(owner) ||
		!CanManageAPIKeys(owner) || !CanManageWebhooks(owner) {
		t.Fatal("owner should pass all management gates")
	}

	// Agent passes none of the management gates.
	agent := principal(requestctx.KindUser, "agent")
	if CanDelete(agent) || CanManageTeams(agent) || CanManageMembers(agent) ||
		CanManageAPIKeys(agent) || CanManageWebhooks(agent) {
		t.Fatal("agent should not pass any management gate")
	}
}
