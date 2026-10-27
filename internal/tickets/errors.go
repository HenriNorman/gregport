package tickets

import "errors"

var (
	ErrTicketNotFound      = errors.New("ticket not found")
	ErrVersionMismatch     = errors.New("version mismatch")
	ErrInvalidTransition   = errors.New("invalid status transition")
	ErrPreconditionRequired = errors.New("if-match required")
)
