package auth

import (
	"net/http"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/strx"
)

// Role and permissions are ANDed: a key must clear both.
type Permission string

const (
	PermTasksRead        Permission = "tasks:read"        //i18n:ignore permission identifier
	PermTasksWrite       Permission = "tasks:write"       //i18n:ignore permission identifier
	PermFlowsRead        Permission = "flows:read"        //i18n:ignore permission identifier
	PermFlowsWrite       Permission = "flows:write"       //i18n:ignore permission identifier
	PermProjectsRead     Permission = "projects:read"     //i18n:ignore permission identifier
	PermProjectsWrite    Permission = "projects:write"    //i18n:ignore permission identifier
	PermAgentsRead       Permission = "agents:read"       //i18n:ignore permission identifier
	PermAgentsWrite      Permission = "agents:write"      //i18n:ignore permission identifier
	PermRunsRead         Permission = "runs:read"         //i18n:ignore permission identifier
	PermRunsWrite        Permission = "runs:write"        //i18n:ignore permission identifier
	PermCredentialsRead  Permission = "credentials:read"  //i18n:ignore permission identifier
	PermCredentialsWrite Permission = "credentials:write" //i18n:ignore permission identifier
	PermSettingsRead     Permission = "settings:read"     //i18n:ignore permission identifier
	PermSettingsWrite    Permission = "settings:write"    //i18n:ignore permission identifier
	PermKeysWrite        Permission = "keys:write"        //i18n:ignore permission identifier
	PermInternalUse      Permission = "internal:use"      //i18n:ignore permission identifier

	// The /auth/workspaces/{id}/members routes are outside resourceForPath; handlers check these by
	// name via APIKeyGrants.
	PermMembersRead  Permission = "members:read"  //i18n:ignore permission identifier
	PermMembersWrite Permission = "members:write" //i18n:ignore permission identifier

	// A real wildcard entry, so an empty list keeps meaning deny, which is the safe reading for
	// rows written by older builds.
	PermAll Permission = "*"
)

// The first matching prefix wins, so list longer prefixes first.
// An unlisted prefix denies; TestPermissionCoverage fails until a new route group is added here.
var resourceForPath = []struct {
	prefix   string
	resource string
}{
	{"/internal", "internal"},
	{"/mcp-proxy", "internal"},

	{"/tasks", "tasks"},
	{"/task-attachments", "tasks"},
	{"/test-cases", "tasks"},
	{"/evidence/jobs", "runs"},
	{"/evidence/recording", "runs"},
	{"/evidence", "tasks"},
	{"/task-types", "flows"},
	{"/events", "tasks"},
	{"/inbox", "tasks"},

	{"/automations", "flows"},

	{"/boards", "flows"},
	{"/stages", "flows"},
	{"/types", "flows"},

	{"/projects", "projects"},
	{"/env-signals", "projects"},
	{"/ingest", "projects"},
	{"/chart-types", "projects"},

	{"/agents", "agents"},
	{"/skills", "agents"},
	{"/workshop", "agents"},
	{"/system/skills", "agents"},
	{"/engines", "agents"},
	{"/tools", "agents"},
	{"/mcps", "agents"},

	{"/usage", "runs"},
	{"/run", "runs"},
	{"/sandboxes", "runs"},
	{"/sandbox", "runs"},
	{"/chats", "runs"},

	{"/credentials", "credentials"},
	{"/credential-providers", "credentials"},
	{"/llm-key", "credentials"},
	{"/llm-profiles", "credentials"},
	{"/workload-identity", "credentials"},

	{"/settings", "settings"},
	{"/workspace-settings", "settings"},
	{"/onboarding", "settings"},
	{"/webhooks", "settings"},
	{"/workspaces", "settings"},
	{"/entitlements", "settings"},
	{"/billing", "settings"},
	{"/audit", "settings"},
	{"/chat-delegation", "settings"},
	{"/token-optimizer", "settings"},
	{"/github", "settings"},
	{"/updates", "settings"},
	{"/infra", "settings"},
	// /runner/v1 is deliberately absent: runners authenticate with their own credential, not an API
	// key.
	{"/runners", "settings"},
}

// "" means the path is in no known group; callers must deny.
func PermissionForRequest(method, path string) Permission {
	for _, e := range resourceForPath {
		if path == e.prefix || strings.HasPrefix(path, e.prefix+"/") || strings.HasPrefix(path, e.prefix+"?") {
			if e.resource == "internal" {
				return PermInternalUse
			}
			return Permission(e.resource + ":" + verbFor(method))
		}
	}
	return ""
}

func verbFor(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "read"
	default:
		return "write"
	}
}

func AllPermissions() []Permission {
	return []Permission{
		PermTasksRead, PermTasksWrite,
		PermFlowsRead, PermFlowsWrite,
		PermProjectsRead, PermProjectsWrite,
		PermAgentsRead, PermAgentsWrite,
		PermRunsRead, PermRunsWrite,
		PermCredentialsRead, PermCredentialsWrite,
		PermSettingsRead, PermSettingsWrite,
		PermKeysWrite, PermInternalUse,
		PermMembersRead, PermMembersWrite,
	}
}

// Presets are expanded at mint time and stored as explicit lists, so redefining a preset never
// widens keys already issued.
var presets = map[string][]Permission{
	"read-only": {
		PermTasksRead, PermFlowsRead, PermProjectsRead,
		PermAgentsRead, PermRunsRead, PermSettingsRead,
		PermMembersRead,
	},
	"ci": {
		PermTasksRead, PermTasksWrite, PermProjectsRead, PermRunsRead,
	},
	"agent": {
		PermTasksRead, PermTasksWrite, PermFlowsRead, PermProjectsRead,
		PermRunsRead, PermRunsWrite, PermInternalUse,
	},
	"full": {PermAll},
}

func Preset(name string) ([]Permission, bool) {
	p, ok := presets[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, false
	}
	out := make([]Permission, len(p))
	copy(out, p)
	return out, true
}

func PresetNames() []string {
	out := make([]string, 0, len(presets))
	for name := range presets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func ParsePermissions(in []string) ([]Permission, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) == 1 {
		if p, ok := Preset(in[0]); ok {
			return p, nil
		}
	}
	valid := map[Permission]bool{PermAll: true}
	for _, p := range AllPermissions() {
		valid[p] = true
	}
	seen := map[Permission]bool{}
	out := make([]Permission, 0, len(in))
	for _, raw := range in {
		p := Permission(strings.ToLower(strings.TrimSpace(string(raw))))
		if !valid[p] {
			return nil, apperr.Invalid("auth.errors.unknown_permission", "value", raw,
				"permissions", joinPermissions(AllPermissions()), "presets", strings.Join(PresetNames(), ", "))
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// The lowest route role for each permission; only entries above viewer are listed.
// Must match the routes; TestPermissionMinRoleMatchesRoutes checks it.
var minRoleForPermission = map[Permission]Role{
	PermProjectsWrite:    RoleDeveloper,
	PermAgentsWrite:      RoleDeveloper,
	PermRunsWrite:        RoleDeveloper,
	PermInternalUse:      RoleDeveloper,
	PermFlowsWrite:       RoleAdmin,
	PermCredentialsRead:  RoleAdmin,
	PermCredentialsWrite: RoleAdmin,
	PermSettingsWrite:    RoleAdmin,
}

func MinRoleForPermission(p Permission) Role {
	if r, ok := minRoleForPermission[p]; ok {
		return r
	}
	return RoleViewer
}

func PermissionsAboveRole(held []Permission, role Role) []Permission {
	var dead []Permission
	for _, p := range held {
		if p == PermAll {
			continue
		}
		if !roleAtLeast(role, MinRoleForPermission(p)) {
			dead = append(dead, p)
		}
	}
	return dead
}

func joinPermissions(ps []Permission) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = string(p)
	}
	return strings.Join(parts, ", ")
}

func EncodePermissions(ps []Permission) string {
	return joinPermissions(ps)
}

func DecodePermissions(s string) []Permission {
	fields := strx.SplitCSV(s)
	out := make([]Permission, 0, len(fields))
	for _, f := range fields {
		out = append(out, Permission(f))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func permitted(held []Permission, method, path string) bool {
	need := PermissionForRequest(method, path)
	if need == "" {
		return false
	}
	for _, h := range held {
		if h == PermAll || h == need {
			return true
		}
	}
	return false
}
