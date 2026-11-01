package tickets

import "github.com/HenriNorman/gregport/internal/platform/requestctx"

// CanDelete: only a human owner or admin can delete tickets.
func CanDelete(p requestctx.Principal) bool {
	if p.Kind != requestctx.KindUser {
		return false
	}
	return p.Role == "owner" || p.Role == "admin"
}

// CanManageTeams: only a human owner or admin can create, update, delete
// teams and change team membership.
func CanManageTeams(p requestctx.Principal) bool {
	if p.Kind != requestctx.KindUser {
		return false
	}
	return p.Role == "owner" || p.Role == "admin"
}

// CanInvite reports whether the principal may invite someone with targetRole.
// Owners invite admins and agents. Admins invite agents only. Nobody invites
// an owner. API keys cannot invite.
func CanInvite(p requestctx.Principal, targetRole string) bool {
	if p.Kind != requestctx.KindUser {
		return false
	}
	switch p.Role {
	case "owner":
		return targetRole == "admin" || targetRole == "agent"
	case "admin":
		return targetRole == "agent"
	}
	return false
}

// CanManageMembers: only a human owner or admin can list, change the role
// of, or remove members.
func CanManageMembers(p requestctx.Principal) bool {
	if p.Kind != requestctx.KindUser {
		return false
	}
	return p.Role == "owner" || p.Role == "admin"
}

// CanManageAPIKeys: only a human owner or admin can create or revoke API keys.
func CanManageAPIKeys(p requestctx.Principal) bool {
	if p.Kind != requestctx.KindUser {
		return false
	}
	return p.Role == "owner" || p.Role == "admin"
}

// CanManageWebhooks: only a human owner or admin. API keys cannot manage
// webhooks, per the slice's rule that keys are limited to tickets and comments.
func CanManageWebhooks(p requestctx.Principal) bool {
	if p.Kind != requestctx.KindUser {
		return false
	}
	return p.Role == "owner" || p.Role == "admin"
}
