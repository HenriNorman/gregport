package tickets

import "github.com/HenriNorman/gregport/internal/platform/httpx"

// Domain sentinels are *httpx.APIError values. Each carries its own HTTP
// status, public code and message, so errors.Is still works and there is no
// separate mapping switch to drift out of sync.
var (
	ErrTicketNotFound       = httpx.NotFound("not_found", "ticket not found")
	ErrVersionMismatch      = httpx.PreconditionFailed("precondition_failed", "ticket was modified; re-fetch and retry with the new ETag")
	ErrInvalidTransition    = httpx.Unprocessable("invalid_transition", "that status change is not allowed")
	ErrPreconditionRequired = httpx.PreconditionRequired("precondition_required", "If-Match header is required")
	ErrInvalidInput         = httpx.Unprocessable("invalid_input", "invalid input")
	ErrForbidden            = httpx.Forbidden("forbidden", "insufficient permissions")

	ErrTeamNotFound     = httpx.NotFound("team_not_found", "team not found")
	ErrTeamNameTaken    = httpx.Conflict("team_name_taken", "a team with that name already exists")
	ErrMemberNotInOrg   = httpx.Unprocessable("member_not_in_org", "user is not a member of this organization")
	ErrAssigneeNotAgent = httpx.Unprocessable("assignee_not_agent", "assignee must be an org member with agent role or higher")
	ErrNoTeamMembers    = httpx.Unprocessable("no_team_members", "team has no assignable members")

	ErrTagNotFound  = httpx.NotFound("tag_not_found", "tag not found")
	ErrTagNameTaken = httpx.Conflict("tag_name_taken", "a tag with that name already exists")

	ErrTicketMerged       = httpx.Conflict("ticket_merged", "ticket is merged and read-only")
	ErrMergeSelf          = httpx.Unprocessable("merge_self", "cannot merge a ticket into itself")
	ErrMergeTargetInvalid = httpx.Unprocessable("merge_target_invalid", "merge target is closed or already merged")
	ErrMergeRequester     = httpx.Unprocessable("merge_requester_mismatch", "both tickets must have the same requester")
	ErrSourceMerged       = httpx.Conflict("source_merged", "source ticket is already merged")

	ErrMacroNotFound  = httpx.NotFound("macro_not_found", "macro not found")
	ErrMacroNameTaken = httpx.Conflict("macro_name_taken", "a macro with that name already exists")

	ErrInvitationNotFound       = httpx.NotFound("invitation_not_found", "invitation not found")
	ErrInvitationExpired        = httpx.Unprocessable("invitation_expired", "invitation has expired")
	ErrInvitationUsed           = httpx.Unprocessable("invitation_used", "invitation already accepted")
	ErrInvitationEmailMismatch  = httpx.Forbidden("invitation_email_mismatch", "invitation email does not match your account")
	ErrInvitationRoleNotAllowed = httpx.Forbidden("invitation_role_not_allowed", "not allowed to invite with that role")
	ErrDuplicateMembership      = httpx.Conflict("duplicate_membership", "user is already a member of this organization")

	ErrMemberNotFound = httpx.NotFound("member_not_found", "member not found")
	ErrLastOwner      = httpx.Unprocessable("last_owner", "cannot remove or demote the last owner")
	ErrAPIKeyNotFound = httpx.NotFound("api_key_not_found", "api key not found")
)
