package api

import (
	"context"
	"io/fs"
	"net/http"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/tools"
)

// A run's key can call the token endpoints and the GitHub proxy without the MCP, so the daemon
// re-checks the stage grant.
// These tables copy tools/thulu-github/server.js (TOOL_PERMISSIONS, SCOPE_IMPLIES), not the
// editable catalog; TestGithubToolScopesMatchToolYaml keeps them in step.

const githubMCPServer = "thulu-github"

// workflows:write has no tool on purpose: only the daemon's own branch push (pushPermissions)
// carries it. contents:write is pr_merge's alone: GitHub files merging under it.
var githubPermissionTools = map[string][]string{
	"actions:write":     {"run_rerun", "run_cancel", "run_dispatch"}, //i18n:ignore GitHub permission token
	"contents:write":    {"pr_merge"},                                //i18n:ignore GitHub permission token
	"deployments:write": {"deployment_approve"},                      //i18n:ignore GitHub permission token
	"workflows:write":   nil,                                         //i18n:ignore GitHub permission token
	"checks:read":       {"pr_checks"},                               //i18n:ignore GitHub permission token
}

// Every thulu-github tool's scope, read from the tool.yaml built into the binary, never from a
// workspace's editable catalog: the GitHub proxy checks every tool, not only the six that mint an
// extra permission. No scope implies "deploy": those tools must be named. pr_merge's grant also
// gates held merges (describeHeldGithub).
var githubToolScopes = mustGithubToolScopes()

func mustGithubToolScopes() map[string]string {
	raw, err := fs.ReadFile(tools.Native(), githubMCPServer+"/tool.yaml")
	if err == nil {
		var y struct {
			Tools []struct {
				Name  string `yaml:"name"`
				Scope string `yaml:"scope"`
			} `yaml:"tools"`
		}
		if err = yaml.Unmarshal(raw, &y); err == nil {
			scopes := make(map[string]string, len(y.Tools))
			for _, t := range y.Tools {
				scopes[t.Name] = t.Scope
			}
			return scopes
		}
	}
	panic(apperr.Wrap(err, "api.github.errors.tool_yaml_unreadable", "server", githubMCPServer))
}

// requires_approval in tool.yaml: the approval a person must have approved on the run's task before
// the run gets the permission the tool needs. A token cannot tell one Actions call from another, so
// once a stage grants the gated tool the approval holds every tool sharing its permission: a stage
// with run_rerun and run_dispatch reruns only after the approval too.
var githubToolApprovals = map[string]string{
	"run_dispatch": "deploy_prod",
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
		if err := s.requireGithubApproval(r.Context(), t, entries, name); err != nil {
			return nil, err
		}
	}
	return perms, nil
}

// githubPermissionApproval names the first granted tool that needs permission behind an approval,
// and that approval's key.
func githubPermissionApproval(entries []string, permission string) (tool, key string) {
	for _, candidate := range githubPermissionTools[permission] {
		if k := githubToolApprovals[candidate]; k != "" && githubToolGranted(entries, candidate) {
			return candidate, k
		}
	}
	return "", ""
}

func (s *Server) requireGithubApproval(ctx context.Context, t *task.Task, entries []string, permission string) error {
	tool, key := githubPermissionApproval(entries, permission)
	if key == "" {
		return nil
	}
	approved, err := s.githubApprovalDecided(ctx, t, key)
	if err != nil || approved {
		return err
	}
	return apperr.Forbidden("api.github.errors.approval_required",
		"name", permission, "tool", tool, "approval", key)
}

// The proxy sees which call it forwards, so the approval holds back only the tool that needs it,
// not every tool sharing its permission.
func (s *Server) requireGithubToolApproval(ctx context.Context, t *task.Task, tool string) error {
	key := githubToolApprovals[tool]
	if key == "" {
		return nil
	}
	approved, err := s.githubApprovalDecided(ctx, t, key)
	if err != nil || approved {
		return err
	}
	return apperr.Forbidden("api.github.errors.proxy_approval_required", "tool", tool, "approval", key)
}

// Approved is enough to mean a person decided it: run keys cannot decide approvals
// (personOnlyWrite) and an agent only opens them pending.
func (s *Server) githubApprovalDecided(ctx context.Context, t *task.Task, key string) (bool, error) {
	if t == nil || s.taskStore == nil {
		return false, nil
	}
	list, err := s.taskStore.ListApprovals(ctx, t.ID)
	if err != nil {
		return false, err
	}
	for i := range list {
		a := &list[i]
		if a.Key == key && a.Status == task.ApprovalApproved && a.DecidedAt != nil && a.DecidedBy != "" {
			return true, nil
		}
	}
	return false, nil
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
