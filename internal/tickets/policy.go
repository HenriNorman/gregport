package tickets

import "github.com/HenriNorman/gregport/internal/platform/requestctx"

// CanDelete: only admin and owner can delete tickets.
func CanDelete(p requestctx.Principal) bool {
	return p.Role == "owner" || p.Role == "admin"
}

func CanManageTeams(p requestctx.Principal) bool {
	return p.Role == "owner" || p.Role == "admin"
}
