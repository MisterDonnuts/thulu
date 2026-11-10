package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestPermissionCoverage(t *testing.T) {
	unreachableByKeys := map[string]string{
		"/auth/": "session lifecycle and workspace membership — an API key is not a person",
		"/me/":   "a person's own engine login — an API key is not a person, and a run must never sign one in",
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}

	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+) (/[^"]*)",\s*([^\n]+?)\)\s*(?://.*)?$`)

	var orphans []string
	for _, line := range strings.Split(string(src), "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		method, route, handler := m[1], m[2], m[3]
		if !strings.Contains(handler, "rr(") {
			continue
		}
		if skipped(route, unreachableByKeys) {
			continue
		}
		if auth.PermissionForRequest(method, concretePath(route)) == "" {
			orphans = append(orphans, method+" "+route)
		}
	}

	if len(orphans) > 0 {
		t.Errorf("these routes belong to no permission group, so no API key can reach them:\n  %s",
			strings.Join(orphans, "\n  "))
	}
}

func TestPermissionVerbs(t *testing.T) {
	cases := []struct {
		method, path string
		want         auth.Permission
	}{
		{"GET", "/tasks", auth.PermTasksRead},
		{"GET", "/tasks/abc123", auth.PermTasksRead},
		{"POST", "/tasks", auth.PermTasksWrite},
		{"PATCH", "/tasks/abc123", auth.PermTasksWrite},
		{"DELETE", "/tasks/abc123", auth.PermTasksWrite},
		{"POST", "/tasks/abc123/attachments", auth.PermTasksWrite},
		{"GET", "/test-cases", auth.PermTasksRead},
		{"GET", "/boards", auth.PermFlowsRead},
		{"GET", "/projects/p1/environments", auth.PermProjectsRead},
		{"GET", "/usage/sessions", auth.PermRunsRead},
		{"GET", "/credentials", auth.PermCredentialsRead},
		{"PUT", "/llm-key", auth.PermCredentialsWrite},
		{"GET", "/settings", auth.PermSettingsRead},
		{"GET", "/workspace-settings", auth.PermSettingsRead},
		{"PUT", "/workspace-settings/agent.max_run_minutes", auth.PermSettingsWrite},
		{"POST", "/internal/tasks/abc123/commit", auth.PermInternalUse},
		{"GET", "/internal/credentials/resolve", auth.PermInternalUse},
		{"GET", "/brand-new-thing", ""},
	}
	for _, c := range cases {
		if got := auth.PermissionForRequest(c.method, c.path); got != c.want {
			t.Errorf("%s %s → %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func skipped(route string, allow map[string]string) bool {
	for prefix := range allow {
		if strings.HasPrefix(route, prefix) {
			return true
		}
	}
	return false
}

func concretePath(pattern string) string {
	out := regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(pattern, "x")
	return strings.TrimSuffix(out, "/")
}
