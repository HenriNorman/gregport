package tickets

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

func TestDomainErrorsCarryHTTPMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    *httpx.APIError
		status int
		code   string
	}{
		{"ticket not found", ErrTicketNotFound, http.StatusNotFound, "not_found"},
		{"version mismatch", ErrVersionMismatch, http.StatusPreconditionFailed, "precondition_failed"},
		{"invalid transition", ErrInvalidTransition, http.StatusUnprocessableEntity, "invalid_transition"},
		{"precondition required", ErrPreconditionRequired, http.StatusPreconditionRequired, "precondition_required"},
		{"invalid input", ErrInvalidInput, http.StatusUnprocessableEntity, "invalid_input"},
		{"forbidden", ErrForbidden, http.StatusForbidden, "forbidden"},
		{"team not found", ErrTeamNotFound, http.StatusNotFound, "team_not_found"},
		{"team name taken", ErrTeamNameTaken, http.StatusConflict, "team_name_taken"},
		{"member not in org", ErrMemberNotInOrg, http.StatusUnprocessableEntity, "member_not_in_org"},
		{"assignee not agent", ErrAssigneeNotAgent, http.StatusUnprocessableEntity, "assignee_not_agent"},
		{"no team members", ErrNoTeamMembers, http.StatusUnprocessableEntity, "no_team_members"},
		{"tag not found", ErrTagNotFound, http.StatusNotFound, "tag_not_found"},
		{"tag name taken", ErrTagNameTaken, http.StatusConflict, "tag_name_taken"},
		{"ticket merged", ErrTicketMerged, http.StatusConflict, "ticket_merged"},
		{"merge self", ErrMergeSelf, http.StatusUnprocessableEntity, "merge_self"},
		{"merge target invalid", ErrMergeTargetInvalid, http.StatusUnprocessableEntity, "merge_target_invalid"},
		{"merge requester", ErrMergeRequester, http.StatusUnprocessableEntity, "merge_requester_mismatch"},
		{"source merged", ErrSourceMerged, http.StatusConflict, "source_merged"},
		{"macro not found", ErrMacroNotFound, http.StatusNotFound, "macro_not_found"},
		{"macro name taken", ErrMacroNameTaken, http.StatusConflict, "macro_name_taken"},
		{"invitation not found", ErrInvitationNotFound, http.StatusNotFound, "invitation_not_found"},
		{"invitation expired", ErrInvitationExpired, http.StatusUnprocessableEntity, "invitation_expired"},
		{"invitation used", ErrInvitationUsed, http.StatusUnprocessableEntity, "invitation_used"},
		{"invitation email mismatch", ErrInvitationEmailMismatch, http.StatusForbidden, "invitation_email_mismatch"},
		{"invitation role not allowed", ErrInvitationRoleNotAllowed, http.StatusForbidden, "invitation_role_not_allowed"},
		{"duplicate membership", ErrDuplicateMembership, http.StatusConflict, "duplicate_membership"},
		{"member not found", ErrMemberNotFound, http.StatusNotFound, "member_not_found"},
		{"last owner", ErrLastOwner, http.StatusUnprocessableEntity, "last_owner"},
		{"api key not found", ErrAPIKeyNotFound, http.StatusNotFound, "api_key_not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Status != tt.status {
				t.Errorf("status: got %d, want %d", tt.err.Status, tt.status)
			}
			if tt.err.Code != tt.code {
				t.Errorf("code: got %q, want %q", tt.err.Code, tt.code)
			}
			if tt.err.Message == "" {
				t.Error("message must not be empty")
			}

			wrapped := fmt.Errorf("repository: %w", tt.err)
			if !errors.Is(wrapped, tt.err) {
				t.Error("errors.Is must still match wrapped domain errors")
			}
		})
	}
}
