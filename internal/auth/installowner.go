package auth

import (
	"context"
	"os"
	"strings"
)

type MemberRoles interface {
	GetMemberRole(ctx context.Context, workspaceID, userID string) (Role, error)
}

func IsInstallationOwner(ctx context.Context, members MemberRoles) bool {
	op := strings.TrimSpace(os.Getenv("OCTOPUS_OPERATOR_WORKSPACE"))
	switch {
	case IsAPIKeyAuthed(ctx):
		return op != "" && !APIKeyRunScope(ctx).IsRun() &&
			APIKeyWorkspace(ctx) == op && APIKeyRole(ctx) == RoleOwner
	case ClaimsFromContext(ctx) != nil:
		c := ClaimsFromContext(ctx)
		if op == "" {
			return false
		}
		if c.WorkspaceID == op && c.Role == RoleOwner {
			return true
		}
		if members == nil || c.UserID == "" {
			return false
		}
		role, err := members.GetMemberRole(ctx, op, c.UserID)
		return err == nil && role == RoleOwner
	default:
		return DevSession(ctx)
	}
}
