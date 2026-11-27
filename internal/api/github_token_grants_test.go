package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestGithubToolGranted(t *testing.T) {
	cases := []struct {
		entries []string
		tool    string
		want    bool
	}{
		{[]string{"thulu-github"}, "run_rerun", true},
		{[]string{"thulu-github@write"}, "run_cancel", true},
		{[]string{"thulu-github@admin"}, "run_rerun", true},
		{[]string{"thulu-github@read"}, "run_rerun", false},
		{[]string{"thulu-github@pr_view+pr_diff"}, "run_rerun", false},
		{[]string{"thulu-github@pr_view+run_cancel"}, "run_cancel", true},
		{[]string{"thulu-github@read+run_cancel"}, "run_cancel", true},
		{[]string{"thulu-github"}, "run_dispatch", false},
		{[]string{"thulu-github@admin"}, "deployment_approve", false},
		{[]string{"thulu-github@run_view+run_dispatch"}, "run_dispatch", true},
		{[]string{"thulu-github@deployment_approve"}, "deployment_approve", true},
		{[]string{"thulu-github@deploy"}, "run_dispatch", false},
		{[]string{"thulu-browser", "octopus-api@write"}, "run_rerun", false},
		{[]string{"thulu-githubx"}, "run_rerun", false},
		{nil, "run_rerun", false},
		{[]string{}, "run_rerun", false},
		{[]string{"thulu-github"}, "pr_checks", true},
		{[]string{"thulu-github@read"}, "pr_checks", true},
		{[]string{"thulu-github@read+pr_view"}, "pr_checks", true},
		{[]string{"thulu-github@write"}, "pr_checks", true},
		{[]string{"thulu-github@pr_view+pr_diff"}, "pr_checks", false},
		{[]string{"thulu-github@pr_view+pr_checks"}, "pr_checks", true},
		{[]string{"thulu-browser"}, "pr_checks", false},
	}
	for _, c := range cases {
		if got := githubToolGranted(c.entries, c.tool); got != c.want {
			t.Errorf("githubToolGranted(%v, %s) = %v, want %v", c.entries, c.tool, got, c.want)
		}
	}
	if githubPermissionGranted([]string{"thulu-github"}, "workflows:write") {
		t.Error("workflows:write was granted through a stage entry; only the daemon's push may carry it")
	}
}

func TestGithubToolScopesMatchToolYaml(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "thulu-github", "tool.yaml"))
	if err != nil {
		t.Fatalf("read tool.yaml: %v", err)
	}
	var y toolYaml
	if err := yaml.Unmarshal(raw, &y); err != nil {
		t.Fatalf("parse tool.yaml: %v", err)
	}
	if y.DefaultScope != githubDefaultScope {
		t.Errorf("default_scope = %q in tool.yaml, %q in the daemon", y.DefaultScope, githubDefaultScope)
	}
	declared := map[string]string{}
	for _, tool := range y.Tools {
		declared[tool.Name] = tool.Scope
	}
	if len(githubToolScopes) != len(declared) {
		t.Errorf("the daemon knows %d thulu-github tools, tool.yaml declares %d", len(githubToolScopes), len(declared))
	}
	for tool, scope := range declared {
		if githubToolScopes[tool] != scope {
			t.Errorf("%s: scope %q in tool.yaml, %q in the daemon", tool, scope, githubToolScopes[tool])
		}
	}
	// The tools that mint an extra permission keep these scopes: a change here widens or narrows
	// who may ask for it, so it has to be a deliberate edit of this test too.
	for tool, scope := range map[string]string{
		"run_rerun": "write", "run_cancel": "write", "pr_checks": "read",
		"run_dispatch": "deploy", "deployment_approve": "deploy", "pr_merge": "deploy",
	} {
		if githubToolScopes[tool] != scope {
			t.Errorf("%s: scope %q, want %q", tool, githubToolScopes[tool], scope)
		}
	}
	for perm, tools := range githubPermissionTools {
		for _, tool := range tools {
			if _, ok := declared[tool]; !ok {
				t.Errorf("%s names %s, which tool.yaml does not declare", perm, tool)
			}
		}
	}
}

type recordingTokens struct {
	mu    sync.Mutex
	perms []githubapp.Permissions
}

func (r *recordingTokens) GetGithubToken(ctx context.Context, projectID string) (*githubapp.Token, error) {
	return r.GetGithubTokenFor(ctx, projectID, githubapp.PermsAgentTools)
}

func (r *recordingTokens) GetGithubTokenFor(_ context.Context, projectID string, perms githubapp.Permissions) (*githubapp.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perms = append(r.perms, perms)
	return &githubapp.Token{ProjectID: projectID, Token: "ghs_test", Source: githubapp.SourceApp}, nil
}

func (r *recordingTokens) last() githubapp.Permissions {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.perms) == 0 {
		return nil
	}
	return r.perms[len(r.perms)-1]
}

func TestInternalGithubToken_ExtraPermissionsFollowTheStageGrant(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "ghgrant@test.com", "GHG", fullCaps())
	tokens := &recordingTokens{}
	f.srv.gh.tokens = tokens
	actionsWrite := githubapp.PermsRunTools.With(githubapp.Permissions{"actions": "write"})

	taskPath := "/internal/tasks/" + f.taskID + "/github-token"
	projPath := "/internal/projects/" + f.projID + "/github-token"
	stage := func(entries ...string) {
		f.srv.agentSource = staticSource{{Name: "developer", Type: "developer",
			MCPByStage: map[string][]string{"in_progress": entries}}}
	}

	cases := []struct {
		name    string
		entries []string
		query   string
		want    int
		perms   githubapp.Permissions
	}{
		{"defaults need no grant", []string{"thulu-github@read"}, "", http.StatusOK, githubapp.PermsRunTools},
		{"read-only stage asks for actions:write", []string{"thulu-github@read"}, "?permissions=actions:write", http.StatusForbidden, nil},
		{"no thulu-github at all", []string{"thulu-browser"}, "?permissions=actions:write", http.StatusForbidden, nil},
		{"write stage may re-run", []string{"thulu-github@write"}, "?permissions=actions:write", http.StatusOK, actionsWrite},
		{"write stage may not approve deploys", []string{"thulu-github@write"}, "?permissions=deployments:write", http.StatusForbidden, nil},
		{"named deployment_approve", []string{"thulu-github@read+deployment_approve"}, "?permissions=deployments:write", http.StatusOK,
			githubapp.PermsRunTools.With(githubapp.PermDeploymentsWrite)},
		{"one of two not granted", []string{"thulu-github@write"}, "?permissions=actions:write,deployments:write", http.StatusForbidden, nil},
		{"named run_dispatch waits for deploy_prod", []string{"thulu-github@run_view+run_dispatch"}, "?permissions=actions:write", http.StatusForbidden, nil},
		{"run_rerun beside run_dispatch waits too", []string{"thulu-github@write+run_dispatch"}, "?permissions=actions:write", http.StatusForbidden, nil},
		{"write stage may not merge", []string{"thulu-github@write"}, "?permissions=contents:write", http.StatusForbidden, nil},
		{"named pr_merge", []string{"thulu-github@read+pr_merge"}, "?permissions=contents:write", http.StatusOK,
			githubapp.PermsRunTools.With(githubapp.Permissions{"contents": "write"})},
		{"unknown name is still a 400", []string{"thulu-github@write"}, "?permissions=administration:write", http.StatusBadRequest, nil},
		{"bare entry reads checks", []string{"thulu-github"}, "?permissions=checks:read", http.StatusOK,
			githubapp.PermsRunTools.With(githubapp.PermChecksRead)},
		{"read stage reads checks", []string{"thulu-github@read+pr_view"}, "?permissions=checks:read", http.StatusOK,
			githubapp.PermsRunTools.With(githubapp.PermChecksRead)},
		{"named pr_checks", []string{"thulu-github@pr_view+pr_checks"}, "?permissions=checks:read", http.StatusOK,
			githubapp.PermsRunTools.With(githubapp.PermChecksRead)},
		{"tool list without pr_checks", []string{"thulu-github@pr_view+pr_diff"}, "?permissions=checks:read", http.StatusForbidden, nil},
		{"no thulu-github, no checks", []string{"thulu-browser"}, "?permissions=checks:read", http.StatusForbidden, nil},
		{"checks:read does not bring actions:write", []string{"thulu-github@read"}, "?permissions=checks:read,actions:write", http.StatusForbidden, nil},
	}
	for _, c := range cases {
		stage(c.entries...)
		for _, path := range []string{taskPath, projPath} {
			tokens.perms = nil
			w := f.getWithKey(t, path+c.query, f.taskKey)
			if w.Code != c.want {
				t.Errorf("%s: GET %s = %d, want %d: %s", c.name, path+c.query, w.Code, c.want, w.Body.String())
				continue
			}
			if c.want != http.StatusOK {
				if tokens.last() != nil {
					t.Errorf("%s: %s minted a token on a refusal", c.name, path)
				}
				continue
			}
			if got := tokens.last(); !samePerms(got, c.perms) {
				t.Errorf("%s: %s minted %v, want %v", c.name, path, got, c.perms)
			}
		}
	}

	f.srv.agentSource = staticSource([]types.AgentType{})
	if w := f.getWithKey(t, taskPath+"?permissions=actions:write", f.taskKey); w.Code != http.StatusForbidden {
		t.Errorf("agent gone: actions:write = %d, want 403", w.Code)
	}
}

// run_dispatch's Actions: write waits for a person to approve deploy_prod on the run's own task,
// on every route a run key can mint from.
func TestInternalGithubToken_RunDispatchWaitsForDeployProd(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "ghdeploy@test.com", "GHD", fullCaps())
	tokens := &recordingTokens{}
	f.srv.gh.tokens = tokens
	f.srv.agentSource = staticSource{{Name: "developer", Type: "developer",
		MCPByStage: map[string][]string{"in_progress": {"thulu-github@read+run_dispatch"}}}}
	ctx := t.Context()
	actionsWrite := githubapp.PermsRunTools.With(githubapp.Permissions{"actions": "write"})
	paths := []string{
		"/internal/tasks/" + f.taskID + "/github-token?permissions=actions:write",
		"/internal/projects/" + f.projID + "/github-token?permissions=actions:write",
	}
	expect := func(when string, want int) {
		t.Helper()
		for _, path := range paths {
			tokens.perms = nil
			w := f.getWithKey(t, path, f.taskKey)
			if w.Code != want {
				t.Errorf("%s: GET %s = %d, want %d: %s", when, path, w.Code, want, w.Body.String())
				continue
			}
			if want != http.StatusOK {
				if tokens.last() != nil {
					t.Errorf("%s: %s minted a token on a refusal", when, path)
				}
				if !strings.Contains(w.Body.String(), "deploy_prod") {
					t.Errorf("%s: the refusal does not name the approval: %s", when, w.Body.String())
				}
				continue
			}
			if got := tokens.last(); !samePerms(got, actionsWrite) {
				t.Errorf("%s: %s minted %v, want %v", when, path, got, actionsWrite)
			}
		}
	}

	expect("no approval", http.StatusForbidden)

	merge := &task.Approval{TaskID: f.taskID, Key: "merge", Label: "Merge", Status: task.ApprovalPending, CreatedBy: "ghdeploy@test.com"}
	if err := f.srv.taskStore.AddApproval(ctx, merge); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.taskStore.SetApprovalStatus(ctx, merge.ID, task.ApprovalApproved, "ghdeploy@test.com", "", ""); err != nil {
		t.Fatal(err)
	}
	expect("another key approved", http.StatusForbidden)

	deploy := &task.Approval{TaskID: f.taskID, Key: "deploy_prod", Label: "Deploy to production",
		Source: task.ApprovalFromAgent, Status: task.ApprovalPending, CreatedBy: "developer"}
	if err := f.srv.taskStore.AddApproval(ctx, deploy); err != nil {
		t.Fatal(err)
	}
	expect("deploy_prod pending", http.StatusForbidden)

	decide := httptest.NewRequest(http.MethodPost, "/tasks/"+f.taskID+"/approvals/"+deploy.ID+"/decide",
		strings.NewReader(`{"decision":"approved"}`))
	decide.Header.Set("X-API-Key", f.taskKey)
	decide.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, decide)
	if w.Code != http.StatusForbidden {
		t.Fatalf("the run approved its own deploy_prod: %d %s", w.Code, w.Body.String())
	}
	expect("the run tried to approve it", http.StatusForbidden)

	if _, err := f.srv.taskStore.SetApprovalStatus(ctx, deploy.ID, task.ApprovalChangesRequested, "ghdeploy@test.com", "not today", ""); err != nil {
		t.Fatal(err)
	}
	expect("deploy_prod sent back", http.StatusForbidden)

	if _, err := f.srv.taskStore.SetApprovalStatus(ctx, deploy.ID, task.ApprovalApproved, "ghdeploy@test.com", "", ""); err != nil {
		t.Fatal(err)
	}
	expect("deploy_prod approved", http.StatusOK)

	// The default token never carries what the gate holds back, nor what merges.
	tokens.perms = nil
	if w := f.getWithKey(t, "/internal/tasks/"+f.taskID+"/github-token", f.taskKey); w.Code != http.StatusOK {
		t.Fatalf("default token = %d: %s", w.Code, w.Body.String())
	}
	if got := tokens.last(); got["actions"] != "read" || got["contents"] != "read" {
		t.Errorf("default token minted %v, want actions:read and contents:read", got)
	}
}

func TestGithubToolApprovalsMatchToolYaml(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "thulu-github", "tool.yaml"))
	if err != nil {
		t.Fatalf("read tool.yaml: %v", err)
	}
	var y toolYaml
	if err := yaml.Unmarshal(raw, &y); err != nil {
		t.Fatalf("parse tool.yaml: %v", err)
	}
	declared := map[string]string{}
	for _, tool := range y.Tools {
		if tool.RequiresApproval != "" {
			declared[tool.Name] = tool.RequiresApproval
		}
	}
	if len(declared) != len(githubToolApprovals) {
		t.Errorf("requires_approval in tool.yaml = %v, githubToolApprovals = %v", declared, githubToolApprovals)
	}
	for tool, key := range githubToolApprovals {
		if declared[tool] != key {
			t.Errorf("%s: requires_approval %q in tool.yaml, %q in the daemon", tool, declared[tool], key)
		}
		if _, ok := githubToolScopes[tool]; !ok {
			t.Errorf("%s needs an approval but the daemon does not know which permission it mints", tool)
		}
	}
	if tool, key := githubPermissionApproval([]string{"thulu-github@write"}, "actions:write"); key != "" {
		t.Errorf("a write stage without run_dispatch waits for %s through %s", key, tool)
	}
	if _, key := githubPermissionApproval([]string{"thulu-github@read+run_dispatch"}, "actions:write"); key != "deploy_prod" {
		t.Errorf("run_dispatch's actions:write waits for %q, want deploy_prod", key)
	}
}

func samePerms(a, b githubapp.Permissions) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
