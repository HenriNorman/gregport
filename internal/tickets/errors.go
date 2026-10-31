package tickets

import "errors"

var (
	ErrTicketNotFound       = errors.New("ticket not found")
	ErrVersionMismatch      = errors.New("version mismatch")
	ErrInvalidTransition    = errors.New("invalid status transition")
	ErrPreconditionRequired = errors.New("if-match required")
	ErrInvalidInput         = errors.New("invalid input")
	ErrForbidden            = errors.New("forbidden")
	ErrTeamNotFound		= errors.New("team not found")
	ErrTeamNameTaken 	= errors.New("team name already taken")
	ErrMemberNotInOrg 	= errors.New("user is not a member of this organization")
	ErrAssigneeNotAgent 	= errors.New("assignee must be an org member with agent role or higher")
)
