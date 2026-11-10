package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type sourcedTokens struct {
	mu     sync.Mutex
	source string
	perms  []githubapp.Permissions
}

func (s *sourcedTokens) GetGithubToken(ctx context.Context, projectID string) (*githubapp.Token, error) {
	return s.GetGithubTokenFor(ctx, projectID, githubapp.PermsAgentTools)
}

func (s *sourcedTokens) GetGithubTokenFor(_ context.Context, projectID string, perms githubapp.Permissions) (*githubapp.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms = append(s.perms, perms)
	return &githubapp.Token{ProjectID: projectID, Token: "ghs_test", Source: s.source}, nil
}

func (s *sourcedTokens) last() githubapp.Permissions {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.perms) == 0 {
		return nil
	}
	return s.perms[len(s.perms)-1]
}

func (s *sourcedTokens) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms = nil
}

type delegatedGithubRig struct {
	*delegationRig
	tokens       *sourcedTokens
	own, foreign string
	key          string
	plainKey     string
}

func newDelegatedGithubRig(t *testing.T, chatStage ...string) *delegatedGithubRig {
	t.Helper()
	rig := &delegatedGithubRig{delegationRig: newDelegationRig(t), tokens: &sourcedTokens{source: githubapp.SourceApp}}
	rig.srv.gh.tokens = rig.tokens
	rig.srv.agentSource = staticSource{{Name: "admin", Type: "admin",
		MCPByStage: map[string][]string{"chat": chatStage}}}

	rig.own = createProject(t, rig.h, rig.token, "widgets")
	otherToken, _ := mintWorkspaceUser(t, rig.srv, "gh-other@test.com", "GHOther")
	rig.foreign = createProject(t, rig.h, otherToken, "secret")
	rig.setRepo(t, rig.own, "https://github.com/Acme/Widgets.git")
	rig.setRepo(t, rig.foreign, "git@github.com:acme/secret.git")

	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", w.Code, w.Body.String())
	}
	rig.key = rig.delegatedKey(t)
	rig.plainKey = rig.runKey(t, auth.RunScope{ChatID: rig.chatID, Agent: "admin", Stage: "chat"})
	return rig
}

func (rig *delegatedGithubRig) setRepo(t *testing.T, projectID, repoURL string) {
	t.Helper()
	if _, err := rig.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`, repoURL, projectID); err != nil {
		t.Fatalf("set repo_url: %v", err)
	}
}

func (rig *delegatedGithubRig) grants(t *testing.T) []audit.Entry {
	t.Helper()
	got, err := rig.srv.auditStore.List(audit.ListOptions{Kind: audit.KindChatGithubGrant})
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	return got
}

func TestDelegatedChatGithubToken_ReachesItsWorkspaceProjects(t *testing.T) {
	rig := newDelegatedGithubRig(t, "thulu-github")

	w := keyDo(t, rig.h, http.MethodGet, "/internal/projects/"+rig.own+"/github-token", rig.key, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delegated key, own project = %d: %s", w.Code, w.Body.String())
	}
	if got := rig.tokens.last(); !samePerms(got, githubapp.PermsDelegatedChat) {
		t.Errorf("minted %v, want %v (no contents:write)", got, githubapp.PermsDelegatedChat)
	}

	rig.tokens.reset()
	w = keyDo(t, rig.h, http.MethodGet, "/internal/projects/"+rig.foreign+"/github-token", rig.key, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("delegated key, another workspace's project = %d, want 404: %s", w.Code, w.Body.String())
	}
	if rig.tokens.last() != nil {
		t.Error("minted a token for another workspace's project")
	}

	w = keyDo(t, rig.h, http.MethodGet, "/internal/projects/"+rig.own+"/github-token", rig.plainKey, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("undelegated chat key = %d, want 403: %s", w.Code, w.Body.String())
	}
	w = keyDo(t, rig.h, http.MethodGet, "/internal/tasks/"+rig.a+"/github-token", rig.key, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("delegated key, task token = %d, want 403: %s", w.Code, w.Body.String())
	}

	entries := rig.grants(t)
	if len(entries) != 1 {
		t.Fatalf("audit has %d token grants, want 1", len(entries))
	}
	if e := entries[0]; e.ResourceID != rig.own || !strings.Contains(e.Summary, rig.ownerEmail) ||
		!strings.Contains(e.Summary, "acme/widgets") || !strings.Contains(e.ActorLabel, rig.ownerEmail) {
		t.Errorf("audit entry does not name the project and the person: %+v", e)
	}
}

func TestDelegatedChatGithubToken_FindsTheProjectByRepo(t *testing.T) {
	rig := newDelegatedGithubRig(t, "thulu-github")

	get := func(key, repo string) *http.Response {
		w := keyDo(t, rig.h, http.MethodGet, "/internal/github-token?repo="+url.QueryEscape(repo), key, nil)
		return w.Result()
	}
	for _, repo := range []string{
		"acme/widgets",
		"Acme/Widgets",
		"github.com/acme/widgets",
		"https://github.com/ACME/widgets",
		"https://github.com/acme/widgets.git",
		"https://github.com/acme/widgets/",
		"git@github.com:acme/widgets.git",
		"ssh://git@github.com/acme/widgets",
	} {
		res := get(rig.key, repo)
		if res.StatusCode != http.StatusOK {
			t.Errorf("repo=%q = %d, want 200", repo, res.StatusCode)
			continue
		}
		var body internalGithubTokenResp
		json.NewDecoder(res.Body).Decode(&body)
		if body.ProjectID != rig.own {
			t.Errorf("repo=%q answered project %q, want %q", repo, body.ProjectID, rig.own)
		}
	}

	cases := []struct {
		name, key, repo string
		want            int
	}{
		{"another workspace's repository", rig.key, "acme/secret", http.StatusNotFound},
		{"a repository no project has", rig.key, "acme/nothing", http.StatusNotFound},
		{"not a repository", rig.key, "not a repo", http.StatusBadRequest},
		{"no repo at all", rig.key, "", http.StatusBadRequest},
		{"a chat that acts for nobody", rig.plainKey, "acme/widgets", http.StatusForbidden},
		{"a task run off its projects", rig.keyA, "acme/widgets", http.StatusForbidden},
	}
	for _, c := range cases {
		if res := get(c.key, c.repo); res.StatusCode != c.want {
			t.Errorf("%s: repo=%q = %d, want %d", c.name, c.repo, res.StatusCode, c.want)
		}
	}
}

func TestRepoRef(t *testing.T) {
	cases := map[string]string{
		"acme/widgets":                          "acme/widgets",
		"Acme/Widgets":                          "acme/widgets",
		" acme/widgets.git ":                    "acme/widgets",
		"github.com/acme/widgets":               "acme/widgets",
		"https://github.com/Acme/Widgets.git":   "acme/widgets",
		"https://github.com/acme/widgets/":      "acme/widgets",
		"http://ghe.example.com/acme/widgets":   "acme/widgets",
		"git@github.com:Acme/widgets.git":       "acme/widgets",
		"ssh://git@github.com/acme/widgets.git": "acme/widgets",
		"":                                      "",
		"widgets":                               "",
		"/srv/repos/widgets.git":                "",
		"a/b/c/d":                               "",
		"file:///srv/repos/widgets.git":         "",
	}
	for in, want := range cases {
		got, ok := repoRef(in)
		if ok != (want != "") || got != want {
			t.Errorf("repoRef(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestDelegatedChatGithubToken_PermissionsFollowTheChatStage(t *testing.T) {
	path := func(rig *delegatedGithubRig, q string) string {
		return "/internal/projects/" + rig.own + "/github-token" + q
	}

	rig := newDelegatedGithubRig(t, "thulu-github@read+run_dispatch+deployment_approve")
	cases := []struct {
		query string
		want  int
		perms githubapp.Permissions
	}{
		{"?permissions=actions:write", http.StatusOK, githubapp.PermsDelegatedChat.With(githubapp.Permissions{"actions": "write"})},
		{"?permissions=deployments:write", http.StatusForbidden, nil},
		{"?permissions=workflows:write", http.StatusForbidden, nil},
		{"?permissions=administration:write", http.StatusBadRequest, nil},
		{"?permissions=checks:read", http.StatusOK, githubapp.PermsDelegatedChat.With(githubapp.PermChecksRead)},
		{"?permissions=actions:write,checks:read", http.StatusOK,
			githubapp.PermsDelegatedChat.With(githubapp.Permissions{"actions": "write"}).With(githubapp.PermChecksRead)},
	}
	for _, c := range cases {
		rig.tokens.reset()
		w := keyDo(t, rig.h, http.MethodGet, path(rig, c.query), rig.key, nil)
		if w.Code != c.want {
			t.Errorf("%s = %d, want %d: %s", c.query, w.Code, c.want, w.Body.String())
			continue
		}
		if got := rig.tokens.last(); c.want == http.StatusOK && !samePerms(got, c.perms) {
			t.Errorf("%s minted %v, want %v", c.query, got, c.perms)
		} else if c.want != http.StatusOK && got != nil {
			t.Errorf("%s minted a token on a refusal", c.query)
		}
	}

	readOnly := newDelegatedGithubRig(t, "thulu-github@read")
	if w := keyDo(t, readOnly.h, http.MethodGet, path(readOnly, "?permissions=actions:write"), readOnly.key, nil); w.Code != http.StatusForbidden {
		t.Errorf("read-only chat stage, actions:write = %d, want 403: %s", w.Code, w.Body.String())
	}
	listed := newDelegatedGithubRig(t, "thulu-github@pr_view+pr_diff")
	if w := keyDo(t, listed.h, http.MethodGet, path(listed, "?permissions=checks:read"), listed.key, nil); w.Code != http.StatusForbidden {
		t.Errorf("chat stage without pr_checks, checks:read = %d, want 403: %s", w.Code, w.Body.String())
	}
	readOnly.srv.agentSource = staticSource([]types.AgentType{})
	if w := keyDo(t, readOnly.h, http.MethodGet, path(readOnly, "?permissions=actions:write"), readOnly.key, nil); w.Code != http.StatusForbidden {
		t.Errorf("agent gone, actions:write = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestDelegatedChatGithubToken_RefusesAStoredCredential(t *testing.T) {
	rig := newDelegatedGithubRig(t, "thulu-github")
	rig.tokens.source = githubapp.SourceStored

	w := keyDo(t, rig.h, http.MethodGet, "/internal/projects/"+rig.own+"/github-token", rig.key, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("delegated key, stored credential = %d, want 403: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "ghs_test") {
		t.Error("the refusal carried the token")
	}
	if n := len(rig.grants(t)); n != 0 {
		t.Errorf("audit recorded %d grants for a refused token", n)
	}
}

type staticSource []types.AgentType

func (s staticSource) All() ([]types.AgentType, error) { return s, nil }
func (s staticSource) Invalidate()                     {}
