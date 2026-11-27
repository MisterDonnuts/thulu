package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const patToken = "ghp_stored_pat_that_must_stay_in_the_daemon"

type fakeGithubCall struct {
	method, path, query, auth, apiKey, cookie, body string
}

type fakeGithub struct {
	*httptest.Server
	mu    sync.Mutex
	calls []fakeGithubCall
}

func (g *fakeGithub) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = nil
}

func (g *fakeGithub) seen() []fakeGithubCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

// Answers what the proxy tests ask for; the node lookup knows two pull requests, one per repo.
func newFakeGithub(t *testing.T) *fakeGithub {
	t.Helper()
	g := &fakeGithub{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.calls = append(g.calls, fakeGithubCall{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-API-Key"), cookie: r.Header.Get("Cookie"),
			body: string(body),
		})
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			w.Header().Set("X-OAuth-Scopes", "repo, workflow")
			w.Write([]byte(`{"login":"octocat"}`))
		case r.URL.Path == "/repos/Acme/Widgets/issues/7/comments" && r.Method == http.MethodGet:
			w.Header().Set("Link", `<https://api.github.com/repositories/123/issues/7/comments?page=2&per_page=100>; rel="next", `+
				`<https://api.github.com/repositories/123/issues/7/comments?page=5&per_page=100>; rel="last"`)
			w.Header().Set("Set-Cookie", "gh=1")
			w.Write([]byte(`[]`))
		case r.URL.Path == "/repos/Acme/Widgets/actions/jobs/5/logs":
			http.Redirect(w, r, g.URL+"/blob/log?sig=abc", http.StatusFound)
		case r.URL.Path == "/blob/log":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("step 1 ok\nstep 2 failed\n"))
		case r.URL.Path == "/repos/Acme/Widgets/pulls/8":
			http.Redirect(w, r, "https://elsewhere.example/x", http.StatusMovedPermanently)
		case r.URL.Path == "/graphql":
			var req struct {
				Query     string            `json:"query"`
				Variables map[string]string `json:"variables"`
			}
			json.Unmarshal(body, &req)
			if req.Query == githubNodeRepoQuery {
				repo := map[string]string{"PR_own": "Acme/Widgets", "PR_other": "Acme/Secret"}[req.Variables["id"]]
				if repo == "" {
					w.Write([]byte(`{"data":{"node":null}}`))
					return
				}
				w.Write([]byte(`{"data":{"node":{"repository":{"nameWithOwner":"` + repo + `"}}}}`))
				return
			}
			w.Write([]byte(`{"data":{"ok":true}}`))
		default:
			w.Write([]byte(`{"number":7}`))
		}
	}))
	t.Cleanup(g.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", g.URL)
	return g
}

type ghProxyRig struct {
	*credFixture
	gh     *fakeGithub
	tokens *sourcedTokens
}

// Not parallel: the fake GitHub is reached through OCTOPUS_GITHUB_API_URL.
func newGhProxyRig(t *testing.T, name string) *ghProxyRig {
	t.Helper()
	gh := newFakeGithub(t)
	f := newCredFixture(t, name+"@test.com", name, fullCaps())
	tokens := &sourcedTokens{source: githubapp.SourceStored}
	f.srv.gh.tokens = &patTokens{sourcedTokens: tokens}
	if _, err := f.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`,
		"https://github.com/Acme/Widgets.git", f.projID); err != nil {
		t.Fatalf("set repo_url: %v", err)
	}
	return &ghProxyRig{credFixture: f, gh: gh, tokens: tokens}
}

// patTokens answers with the stored PAT the way the resolver does for a project without the App.
type patTokens struct{ *sourcedTokens }

func (p *patTokens) GetGithubToken(ctx context.Context, projectID string) (*githubapp.Token, error) {
	return p.GetGithubTokenFor(ctx, projectID, githubapp.PermsAgentTools)
}

func (p *patTokens) GetGithubTokenFor(ctx context.Context, projectID string, perms githubapp.Permissions) (*githubapp.Token, error) {
	rec, err := p.sourcedTokens.GetGithubTokenFor(ctx, projectID, perms)
	if err == nil && rec.Source != githubapp.SourceApp {
		rec.Token = patToken
	}
	return rec, err
}

func (rig *ghProxyRig) stage(entries ...string) {
	rig.srv.agentSource = staticSource{{Name: "developer", Type: "developer",
		MCPByStage: map[string][]string{"in_progress": entries}}}
}

func (rig *ghProxyRig) do(t *testing.T, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, githubProxyPrefix+path, rd)
	req.Header.Set("X-API-Key", rig.taskKey)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, req)
	return w
}

func (rig *ghProxyRig) approve(t *testing.T, key string) {
	t.Helper()
	ctx := t.Context()
	a := &task.Approval{TaskID: rig.taskID, Key: key, Label: key, Source: task.ApprovalFromAgent,
		Status: task.ApprovalPending, CreatedBy: "developer"}
	if err := rig.srv.taskStore.AddApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.srv.taskStore.SetApprovalStatus(ctx, a.ID, task.ApprovalApproved, "person@test.com", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGithubToken_RunKeyNeverGetsAStoredCredential(t *testing.T) {
	rig := newGhProxyRig(t, "ghpat")
	rig.stage("thulu-github@write")
	paths := []string{
		"/internal/tasks/" + rig.taskID + "/github-token",
		"/internal/projects/" + rig.projID + "/github-token",
		"/internal/github-token?repo=acme/widgets",
	}
	for _, path := range paths {
		w := rig.getWithKey(t, path, rig.taskKey)
		if w.Code != http.StatusConflict {
			t.Errorf("GET %s with a stored credential = %d, want 409: %s", path, w.Code, w.Body.String())
		}
		var body struct{ Code string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != "api.github.errors.use_proxy" {
			t.Errorf("GET %s: code %q, want the use_proxy signal: %s", path, body.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), patToken) {
			t.Errorf("GET %s handed the PAT to a run key", path)
		}
	}

	// The App path is unchanged: an installation token still reaches the run.
	rig.tokens.source = githubapp.SourceApp
	for _, path := range paths {
		w := rig.getWithKey(t, path, rig.taskKey)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"token":"ghs_test"`) {
			t.Errorf("GET %s with an App token = %d, want 200 with the token: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestGithubProxy_ForwardsWithThePATAndNothingOfTheCaller(t *testing.T) {
	rig := newGhProxyRig(t, "ghfwd")
	rig.stage("thulu-github@read")

	w := rig.do(t, http.MethodGet, "/repos/Acme/Widgets/pulls/7?per_page=5", "",
		map[string]string{"Cookie": "session=caller", "Accept": "application/vnd.github.diff"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"number":7`) {
		t.Fatalf("pr_view through the proxy = %d: %s", w.Code, w.Body.String())
	}
	calls := rig.gh.seen()
	if len(calls) != 1 {
		t.Fatalf("GitHub saw %d calls, want 1", len(calls))
	}
	c := calls[0]
	if c.path != "/repos/Acme/Widgets/pulls/7" || c.query != "per_page=5" {
		t.Errorf("forwarded %s?%s", c.path, c.query)
	}
	if c.auth != "token "+patToken {
		t.Errorf("GitHub got Authorization %q, want the project's PAT", c.auth)
	}
	if c.apiKey != "" || c.cookie != "" {
		t.Errorf("the caller's key or cookie reached GitHub: %q %q", c.apiKey, c.cookie)
	}
	if strings.Contains(w.Body.String(), patToken) || strings.Contains(w.Header().Get("Authorization"), patToken) {
		t.Error("the PAT came back to the caller")
	}

	rig.gh.reset()
	w = rig.do(t, http.MethodGet, "/repos/Acme/Widgets/issues/7/comments", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("pr_comments_list = %d: %s", w.Code, w.Body.String())
	}
	wantLink := `</repos/Acme/Widgets/issues/7/comments?page=2&per_page=100>; rel="next", ` +
		`</repos/Acme/Widgets/issues/7/comments?page=5&per_page=100>; rel="last"`
	if got := w.Header().Get("Link"); got != wantLink {
		t.Errorf("Link = %q, want the pages back through the proxy: %q", got, wantLink)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("GitHub's cookie reached the caller")
	}
}

func TestGithubProxy_FollowsTheStageGrant(t *testing.T) {
	rig := newGhProxyRig(t, "ghgrantp")
	comment := func() int {
		return rig.do(t, http.MethodPost, "/repos/Acme/Widgets/issues/7/comments", `{"body":"hi"}`, nil).Code
	}
	merge := func() int {
		return rig.do(t, http.MethodPut, "/repos/Acme/Widgets/pulls/7/merge", `{"merge_method":"squash"}`, nil).Code
	}

	cases := []struct {
		name           string
		entries        []string
		comment, merge int
	}{
		{"read stage", []string{"thulu-github@read"}, http.StatusForbidden, http.StatusForbidden},
		{"write stage", []string{"thulu-github@write"}, http.StatusOK, http.StatusForbidden},
		{"named pr_comment", []string{"thulu-github@pr_view+pr_comment"}, http.StatusOK, http.StatusForbidden},
		{"named pr_merge", []string{"thulu-github@read+pr_merge"}, http.StatusForbidden, http.StatusOK},
		{"no thulu-github", []string{"thulu-browser"}, http.StatusForbidden, http.StatusForbidden},
		{"no stage entry", nil, http.StatusForbidden, http.StatusForbidden},
	}
	for _, c := range cases {
		rig.stage(c.entries...)
		rig.gh.reset()
		if got := comment(); got != c.comment {
			t.Errorf("%s: pr_comment = %d, want %d", c.name, got, c.comment)
		}
		if got := merge(); got != c.merge {
			t.Errorf("%s: pr_merge = %d, want %d", c.name, got, c.merge)
		}
		allowed := 0
		for _, code := range []int{c.comment, c.merge} {
			if code == http.StatusOK {
				allowed++
			}
		}
		if n := len(rig.gh.seen()); n != allowed {
			t.Errorf("%s: GitHub saw %d calls, want %d (a refusal must not reach it)", c.name, n, allowed)
		}
	}

	rig.srv.agentSource = staticSource([]types.AgentType{})
	if got := comment(); got != http.StatusForbidden {
		t.Errorf("agent gone: pr_comment = %d, want 403", got)
	}
}

// run_dispatch waits for deploy_prod; pr_merge waits for nothing but its own name in the stage.
// The proxy holds back only the dispatch: a rerun beside it does not wait.
func TestGithubProxy_ApprovalGate(t *testing.T) {
	rig := newGhProxyRig(t, "ghappr")
	rig.stage("thulu-github@write+run_dispatch+pr_merge")
	dispatch := func() *httptest.ResponseRecorder {
		return rig.do(t, http.MethodPost, "/repos/Acme/Widgets/actions/workflows/deploy.yml/dispatches", `{"ref":"main"}`, nil)
	}

	w := dispatch()
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "deploy_prod") {
		t.Errorf("run_dispatch without deploy_prod = %d, want 403 naming the approval: %s", w.Code, w.Body.String())
	}
	if n := len(rig.gh.seen()); n != 0 {
		t.Errorf("a held dispatch reached GitHub (%d calls)", n)
	}
	if got := rig.do(t, http.MethodPost, "/repos/Acme/Widgets/actions/runs/5/rerun", "", nil).Code; got != http.StatusOK {
		t.Errorf("run_rerun beside an unapproved run_dispatch = %d, want 200", got)
	}
	if got := rig.do(t, http.MethodGet, "/repos/Acme/Widgets/actions/workflows/deploy.yml/runs", "", nil).Code; got != http.StatusOK {
		t.Errorf("run_list (shares a route with run_dispatch) = %d, want 200", got)
	}
	if got := rig.do(t, http.MethodPut, "/repos/Acme/Widgets/pulls/7/merge", `{}`, nil).Code; got != http.StatusOK {
		t.Errorf("named pr_merge = %d, want 200", got)
	}

	rig.approve(t, "merge")
	if got := dispatch().Code; got != http.StatusForbidden {
		t.Errorf("another approval approved: run_dispatch = %d, want 403", got)
	}
	rig.approve(t, "deploy_prod")
	if w := dispatch(); w.Code != http.StatusOK {
		t.Errorf("run_dispatch after deploy_prod = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Only run_dispatch in the stage: reading the runs waits on the same approval as dispatching.
	rig2 := newGhProxyRig(t, "ghappr2")
	rig2.stage("thulu-github@run_dispatch")
	if got := rig2.do(t, http.MethodGet, "/repos/Acme/Widgets/actions/workflows/deploy.yml/runs", "", nil).Code; got != http.StatusForbidden {
		t.Errorf("run_dispatch alone, unapproved, listing runs = %d, want 403", got)
	}
}

func TestGithubProxy_RefusesARepoOutsideTheTask(t *testing.T) {
	rig := newGhProxyRig(t, "ghrepo")
	rig.stage("thulu-github@write")
	other := createProject(t, rig.h, rig.token, "other-repo-ghrepo")
	if _, err := rig.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`,
		"git@github.com:Acme/Secret.git", other); err != nil {
		t.Fatal(err)
	}

	if w := rig.do(t, http.MethodGet, "/repos/Acme/Secret/pulls/1", "", nil); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "acme/secret") {
		t.Errorf("a workspace repo outside the task = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := rig.do(t, http.MethodGet, "/repos/Someone/Else/pulls/1", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("a repo no project has = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := rig.do(t, http.MethodGet, "/repos/acme/WIDGETS/pulls/1", "", nil); w.Code != http.StatusOK {
		t.Errorf("the task's repo in other case = %d, want 200: %s", w.Code, w.Body.String())
	}
	for _, c := range rig.gh.seen() {
		if !strings.EqualFold(c.path, "/repos/acme/widgets/pulls/1") {
			t.Errorf("GitHub saw %s %s", c.method, c.path)
		}
	}
}

// A project on another git host can share owner/name with a GitHub repository. The workspace's
// GitHub credential resolves for it too, so matching by owner/name alone let the run act on a
// GitHub repository no project has.
func TestGithubProxy_RefusesAProjectOnAnotherGitHost(t *testing.T) {
	rig := newGhProxyRig(t, "ghhost")
	rig.stage("thulu-github@write")
	if _, err := rig.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`,
		"https://gitlab.com/Acme/Widgets.git", rig.projID); err != nil {
		t.Fatal(err)
	}
	if w := rig.do(t, http.MethodPost, "/repos/Acme/Widgets/issues/7/comments", `{"body":"hi"}`, nil); w.Code != http.StatusNotFound {
		t.Errorf("proxy, a GitLab project with a GitHub repo's name = %d, want 404: %s", w.Code, w.Body.String())
	}
	if n := len(rig.gh.seen()); n != 0 {
		t.Errorf("GitHub saw %d calls for a GitLab project", n)
	}
	rig.tokens.source = githubapp.SourceApp
	if w := rig.getWithKey(t, "/internal/github-token?repo=acme/widgets", rig.taskKey); w.Code != http.StatusNotFound {
		t.Errorf("token for a GitLab project with a GitHub repo's name = %d, want 404: %s", w.Code, w.Body.String())
	}

	// The SSH form may use a host alias, so it still matches by owner/name.
	if _, err := rig.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`,
		"git@github-work:Acme/Widgets.git", rig.projID); err != nil {
		t.Fatal(err)
	}
	rig.tokens.source = githubapp.SourceStored
	if w := rig.do(t, http.MethodGet, "/repos/Acme/Widgets/pulls/7", "", nil); w.Code != http.StatusOK {
		t.Errorf("proxy, SSH remote with a host alias = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestGithubProxy_RefusesUnknownRoutes(t *testing.T) {
	rig := newGhProxyRig(t, "ghroute")
	rig.stage("thulu-github@write+pr_merge+run_dispatch+deployment_approve")
	rig.approve(t, "deploy_prod")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/user"},
		{http.MethodGet, "/repos/Acme/Widgets"},
		{http.MethodDelete, "/repos/Acme/Widgets"},
		{http.MethodGet, "/repos/Acme/Widgets/collaborators"},
		{http.MethodPut, "/repos/Acme/Widgets/collaborators/mallory"},
		{http.MethodGet, "/repos/Acme/Widgets/pulls/comments"},
		{http.MethodDelete, "/repos/Acme/Widgets/pulls/7"},
		{http.MethodGet, "/repos/Acme/Widgets/pulls/7/../../../user"},
		{http.MethodGet, "/repos/Acme/Widgets/contents/..%2F..%2Fuser"},
		{http.MethodGet, "/repos/Acme/Widgets/actions/runs/1%2F..%2F2"},
		{http.MethodPost, "/repos/Acme/Widgets/git/refs"},
		{http.MethodPatch, "/repos/Acme/Widgets/actions/secrets/X"},
	} {
		w := rig.do(t, c.method, c.path, "", nil)
		if w.Code == http.StatusOK {
			t.Errorf("%s %s = 200, want a refusal", c.method, c.path)
		}
	}
	if n := len(rig.gh.seen()); n != 0 {
		t.Errorf("GitHub saw %d calls from unknown routes: %+v", n, rig.gh.seen())
	}
}

func TestGithubProxy_GraphQL(t *testing.T) {
	rig := newGhProxyRig(t, "ghgql")
	rig.stage("thulu-github@write")
	threads := githubProxy.GraphQL["review_threads"].Query
	ready := githubProxy.GraphQL["mark_ready"].Query
	post := func(query string, vars map[string]any, header map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
		return rig.do(t, http.MethodPost, "/graphql", string(b), header)
	}

	w := post("query { viewer { login } }", nil, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "proxy_document_unknown") {
		t.Errorf("an unknown document = %d, want 403: %s", w.Code, w.Body.String())
	}
	w = post(threads+" query Other { viewer { login } }", map[string]any{"owner": "Acme", "repo": "Widgets", "number": 7}, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("a known document with more appended = %d, want 403", w.Code)
	}
	if n := len(rig.gh.seen()); n != 0 {
		t.Fatalf("unknown documents reached GitHub (%d calls)", n)
	}

	reindented := strings.ReplaceAll(threads, " { ", "\n  {\n    ")
	w = post(reindented, map[string]any{"owner": "Acme", "repo": "Widgets", "number": 7, "after": nil}, nil)
	if w.Code != http.StatusOK {
		t.Errorf("review_threads, reindented = %d, want 200: %s", w.Code, w.Body.String())
	}
	w = post(threads, map[string]any{"owner": "Acme", "repo": "Secret", "number": 7}, nil)
	if w.Code == http.StatusOK {
		t.Errorf("review_threads on a repo outside the task = %d", w.Code)
	}
	w = post(threads, map[string]any{"owner": "Acme", "repo": "Widgets", "number": 7, "first": 100}, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("an extra variable = %d, want 403", w.Code)
	}

	w = post(ready, map[string]any{"id": "PR_own"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a node document without X-Thulu-Repo = %d, want 400: %s", w.Code, w.Body.String())
	}
	w = post(ready, map[string]any{"id": "PR_other"}, map[string]string{githubProxyRepoHeader: "Acme/Widgets"})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "PR_other") {
		t.Errorf("a pull request of another repo = %d, want 403: %s", w.Code, w.Body.String())
	}
	rig.gh.reset()
	w = post(ready, map[string]any{"id": "PR_own"}, map[string]string{githubProxyRepoHeader: "Acme/Widgets"})
	if w.Code != http.StatusOK {
		t.Errorf("mark_ready on the task's pull request = %d, want 200: %s", w.Code, w.Body.String())
	}
	calls := rig.gh.seen()
	if len(calls) != 2 || calls[1].auth != "token "+patToken {
		t.Errorf("want the node lookup and the mutation, both with the PAT; got %+v", calls)
	}

	rig.stage("thulu-github@read")
	w = post(ready, map[string]any{"id": "PR_own"}, map[string]string{githubProxyRepoHeader: "Acme/Widgets"})
	if w.Code != http.StatusForbidden {
		t.Errorf("mark_ready from a read stage = %d, want 403", w.Code)
	}
}

func TestGithubProxy_Redirects(t *testing.T) {
	rig := newGhProxyRig(t, "ghredir")
	rig.stage("thulu-github@read")
	w := rig.do(t, http.MethodGet, "/repos/Acme/Widgets/actions/jobs/5/logs", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "step 2 failed") {
		t.Fatalf("run_logs through the signed URL = %d: %s", w.Code, w.Body.String())
	}
	for _, c := range rig.gh.seen() {
		if c.path == "/blob/log" && c.auth != "" {
			t.Errorf("the signed log URL got Authorization %q", c.auth)
		}
	}
	if w := rig.do(t, http.MethodGet, "/repos/Acme/Widgets/pulls/8", "", nil); w.Code != http.StatusBadGateway {
		t.Errorf("a redirect off a plain route = %d, want 502: %s", w.Code, w.Body.String())
	}
}

func TestGithubProxy_OnlyRunKeys(t *testing.T) {
	rig := newGhProxyRig(t, "ghsess")
	rig.stage("thulu-github@read")
	req := httptest.NewRequest(http.MethodGet, githubProxyPrefix+"/repos/Acme/Widgets/pulls/7", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+rig.token)
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("a browser session used the proxy: %d", w.Code)
	}
	if n := len(rig.gh.seen()); n != 0 {
		t.Errorf("a session reached GitHub (%d calls)", n)
	}
}

// A delegated chat on a PAT project reads and comments through the proxy; what waits for the
// person's confirmation (pr_merge, deployment_approve) never runs there.
func TestGithubProxy_DelegatedChat(t *testing.T) {
	gh := newFakeGithub(t)
	rig := newDelegatedGithubRig(t, "thulu-github@write+pr_merge")
	rig.tokens.source = githubapp.SourceStored
	do := func(method, path, key string) *httptest.ResponseRecorder {
		return keyDo(t, rig.h, method, githubProxyPrefix+path, key, nil)
	}

	if w := keyDo(t, rig.h, http.MethodGet, "/internal/github-token?repo=acme/widgets", rig.key, nil); w.Code != http.StatusConflict {
		t.Errorf("delegated key, token for a PAT project = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, "/repos/Acme/Widgets/pulls/7", rig.key); w.Code != http.StatusOK {
		t.Errorf("delegated pr_view = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodPut, "/repos/Acme/Widgets/pulls/7/merge", rig.key); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "proxy_held_in_chat") {
		t.Errorf("delegated pr_merge through the proxy = %d, want 403 held: %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, "/repos/acme/secret/pulls/7", rig.key); w.Code != http.StatusNotFound {
		t.Errorf("delegated key, another workspace's repo = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, "/repos/Acme/Widgets/pulls/7", rig.plainKey); w.Code != http.StatusForbidden {
		t.Errorf("undelegated chat key = %d, want 403: %s", w.Code, w.Body.String())
	}
	for _, c := range gh.seen() {
		if c.method != http.MethodGet || c.path != "/repos/Acme/Widgets/pulls/7" {
			t.Errorf("GitHub saw %s %s", c.method, c.path)
		}
	}
}

// /internal/credentials/resolve would otherwise hand a run the project's PAT as a thulu-github
// credential.
func TestInternalResolveCredential_NeverHandsARunAGithubPAT(t *testing.T) {
	rig := newGhProxyRig(t, "ghresolve")
	rig.stage("thulu-github@read")
	cw := postJSONAuth(t, rig.h, "/credentials", rig.token, map[string]any{
		"name": "gh-pat", "provider": "github", "kind": "github_pat",
		"payload": map[string]string{"token": patToken},
		"grants":  []map[string]string{{"subject_type": "project", "subject_id": rig.projID}},
	})
	if cw.Code != http.StatusOK {
		t.Fatalf("create github credential = %d: %s", cw.Code, cw.Body.String())
	}
	q := url.Values{"server": {"thulu-github"}, "tool": {"pr_view"}, "project_id": {rig.projID}}
	w := rig.getWithKey(t, "/internal/credentials/resolve?"+q.Encode(), rig.taskKey)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "provider_unsupported") {
		t.Errorf("resolve a github credential with a run key = %d, want 403: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), patToken) {
		t.Fatal("the run key got the PAT")
	}
}

// Every tool the proxy table names is a thulu-github tool, and every thulu-github tool that calls
// GitHub has at least one route or document. tools/thulu-github/server.test.mjs checks the other
// side: each call server.js makes is in the table under the tool that makes it.
func TestGithubProxyTableMatchesToolYaml(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "thulu-github", "tool.yaml"))
	if err != nil {
		t.Fatalf("read tool.yaml: %v", err)
	}
	var y toolYaml
	if err := yaml.Unmarshal(raw, &y); err != nil {
		t.Fatalf("parse tool.yaml: %v", err)
	}
	declared := map[string]bool{}
	for _, tool := range y.Tools {
		declared[tool.Name] = true
	}
	covered := map[string]bool{}
	check := func(where string, tools []string) {
		for _, tool := range tools {
			if !declared[tool] {
				t.Errorf("%s names %s, which tool.yaml does not declare", where, tool)
			}
			covered[tool] = true
		}
	}
	for _, r := range githubProxy.REST {
		check(r.Method+" "+r.Path, r.Tools)
	}
	for name, d := range githubProxy.GraphQL {
		check("graphql "+name, d.Tools)
	}
	// These ask the daemon, which runs git itself; they never call GitHub.
	daemonOnly := map[string]bool{"commit": true, "branch_push": true, "branch_status": true}
	for tool := range declared {
		if !covered[tool] && !daemonOnly[tool] {
			t.Errorf("%s calls GitHub but the proxy table has no route for it", tool)
		}
		if covered[tool] && daemonOnly[tool] {
			t.Errorf("%s only asks the daemon, yet the proxy table names it", tool)
		}
	}
	for tool := range githubToolScopes {
		if !declared[tool] {
			t.Errorf("githubToolScopes has %s, which tool.yaml does not declare", tool)
		}
	}
}

func TestValidGithubParam(t *testing.T) {
	for _, c := range []struct {
		name, value string
		want        bool
	}{
		{"pull_number", "7", true},
		{"pull_number", "comments", false},
		{"run_id", "12a", false},
		{"comment_id", "", false},
		{"owner", "Acme", true},
		{"owner", "a/b", false},
		{"repo", "..", false},
		{"workflow_id", "deploy.yml", true},
		{"workflow_id", "123", true},
		{"workflow_id", "../x", false},
		{"path", ".github/workflows/ci.yml", true},
		{"path", "../../etc", false},
		{"path", "a//b", false},
		{"ref", "feature/x", true},
		{"name", "needs review", true},
		{"name", "bad\nlabel", false},
	} {
		if got := validGithubParam(c.name, c.value); got != c.want {
			t.Errorf("validGithubParam(%s, %q) = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}

// encoding/json matches keys without regard to case and keeps the last duplicate, while GitHub
// reads the exact keys "query" and "variables". The body GitHub runs must be the one the proxy
// checked: a "QUERY" key carrying a listed document must not carry a "query" GitHub runs instead.
func TestGithubProxy_GraphQLForwardsOnlyTheCheckedDocument(t *testing.T) {
	rig := newGhProxyRig(t, "ghsmuggle")
	rig.stage("thulu-github@read")
	other := createProject(t, rig.h, rig.token, "other-repo-ghsmuggle")
	if _, err := rig.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`,
		"git@github.com:Acme/Secret.git", other); err != nil {
		t.Fatal(err)
	}
	listed, _ := json.Marshal(githubProxy.GraphQL["review_threads"].Query)
	evil := `"mutation { deleteRepository(input: {repositoryId: \"R_x\"}) { clientMutationId } }"`
	own := `{"owner":"Acme","repo":"Widgets","number":7}`
	secret := `{"owner":"Acme","repo":"Secret","number":7}`
	bodies := []string{
		`{"query":` + evil + `,"QUERY":` + string(listed) + `,"variables":` + own + `}`,
		`{"query":` + evil + `,"Query":` + string(listed) + `,"variables":` + own + `}`,
		`{"query":` + string(listed) + `,"variables":` + secret + `,"Variables":` + own + `}`,
		`{"query":` + string(listed) + `,"variables":{"owner":"Acme","repo":"Secret","repo":"Widgets","number":7}}`,
	}
	for i, body := range bodies {
		rig.gh.reset()
		w := rig.do(t, http.MethodPost, "/graphql", body, nil)
		for _, c := range rig.gh.seen() {
			var sent map[string]json.RawMessage
			if err := json.Unmarshal([]byte(c.body), &sent); err != nil {
				t.Errorf("body %d: GitHub got a body it cannot read: %s", i, c.body)
				continue
			}
			if strings.Contains(c.body, "deleteRepository") || strings.Contains(c.body, "Secret") {
				t.Errorf("body %d (%d): GitHub got a document or a repo the proxy did not check: %s", i, w.Code, c.body)
			}
			for k := range sent {
				if k != "query" && k != "variables" {
					t.Errorf("body %d: GitHub got key %q", i, k)
				}
			}
		}
	}

	rig.gh.reset()
	w := rig.do(t, http.MethodPost, "/graphql",
		`{"query":`+string(listed)+`,"variables":{"owner":"Acme","repo":"Widgets","number":7,"after":{"a":1,"a":2}}}`, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "proxy_variable_not_scalar") || len(rig.gh.seen()) != 0 {
		t.Errorf("an object variable = %d with %d GitHub calls, want 403 and none: %s", w.Code, len(rig.gh.seen()), w.Body.String())
	}
}
