package usage

import "strings"

type Scope struct {
	WorkspaceID  string
	Unrestricted bool
}

func Everything() Scope { return Scope{Unrestricted: true} }

func InWorkspace(wid string) Scope { return Scope{WorkspaceID: wid} }

// Empty workspace_id matches nothing, same rule as workspaceVisible in internal/api/scope.go:
// an unattributed run is more likely another tenant's, so fail closed.
func (sc Scope) clause(prefix string) (string, []any) {
	if sc.Unrestricted {
		return "", nil
	}
	col := qualify(prefix) + "workspace_id"
	return col + " != '' AND " + col + " = ?", []any{sc.WorkspaceID}
}

func (sc Scope) where() (string, []any) {
	cond, args := sc.clause("")
	if cond == "" {
		return "", nil
	}
	return "WHERE " + cond, args
}

func qualify(prefix string) string {
	if prefix == "" || strings.HasSuffix(prefix, ".") {
		return prefix
	}
	return prefix + "."
}
