package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A run's key can call the token endpoints without the MCP, so the daemon re-checks the stage
// grant.
// These tables copy tools/thulu-github/server.js (TOOL_PERMISSIONS, SCOPE_IMPLIES, tool scopes),
// not the editable catalog; TestGithubToolScopesMatchToolYaml keeps them in step.

const githubMCPServer = "thulu-github"

// workflows:write has no tool on purpose: only the daemon's own branch push (pushPermissions)
// carries it.
var githubPermissionTools = map[string][]string{
	"actions:write":     {"run_rerun", "run_cancel", "run_dispatch"}, //i18n:ignore GitHub permission token
	"deployments:write": {"deployment_approve"},                      //i18n:ignore GitHub permission token
	"workflows:write":   nil,                                         //i18n:ignore GitHub permission token
	"checks:read":       {"pr_checks"},                               //i18n:ignore GitHub permission token
}

// pr_merge needs no token permission but its grant gates held merges (describeHeldGithub).
// No scope implies "deploy": those tools must be named.
var githubToolScopes = map[string]string{
	"run_rerun":          "write",
	"run_cancel":         "write",
	"run_dispatch":       "deploy",
	"deployment_approve": "deploy",
	"pr_merge":           "deploy",
	"pr_checks":          "read",
}

var githubScopeImplies = map[string][]string{
	"read":  {"read"},
	"write": {"read", "write"},
	"admin": {"read", "write", "admin"},
}

const githubDefaultScope = "write"

// Must parse entries the same way images/snippets/mcp-config.sh and the thulu-github server do.
func githubToolGranted(entries []string, tool string) bool {
	for _, raw := range entries {
		server, suffix, _ := strings.Cut(strings.TrimSpace(raw), "@")
		if strings.TrimSpace(server) != githubMCPServer {
			continue
		}
		segments := []string{githubDefaultScope}
		if strings.TrimSpace(suffix) != "" {
			segments = strings.Split(suffix, "+")
		}
		for _, seg := range segments {
			seg = strings.TrimSpace(seg)
			if seg == tool {
				return true
			}
			if implied, ok := githubScopeImplies[seg]; ok && slices.Contains(implied, githubToolScopes[tool]) {
				return true
			}
		}
	}
	return false
}

func githubPermissionGranted(entries []string, permission string) bool {
	for _, tool := range githubPermissionTools[permission] {
		if githubToolGranted(entries, tool) {
			return true
		}
	}
	return false
}

// An ungranted permission is refused (403), not silently dropped, so the failure names the grant
// instead of surfacing as a GitHub 403.
func (s *Server) tokenPermissionsFor(r *http.Request, t *task.Task) (githubapp.Permissions, error) {
	scope := auth.APIKeyRunScope(r.Context())
	if scope.Delegated() {
		return s.delegatedTokenPermissions(r, scope)
	}
	perms, err := tokenPermissionsParam(r)
	if err != nil {
		return nil, err
	}
	raw := r.URL.Query().Get("permissions")
	if raw == "" || !scope.IsRun() {
		return perms, nil
	}
	var entries []string
	if t != nil {
		entries = s.credentialStageTools(scope, t)
	}
	for _, name := range strings.Split(raw, ",") {
		if !githubPermissionGranted(entries, name) {
			return nil, apperr.Forbidden("api.github.errors.permission_not_granted",
				"name", name, "tools", strings.Join(githubPermissionTools[name], ", "), "server", githubMCPServer)
		}
	}
	return perms, nil
}

func (s *Server) runTask(r *http.Request) *task.Task {
	scope := auth.APIKeyRunScope(r.Context())
	if !scope.IsRun() || scope.TaskID == "" {
		return nil
	}
	t, err := s.taskStore.Get(r.Context(), scope.TaskID)
	if err != nil {
		return nil
	}
	return t
}
