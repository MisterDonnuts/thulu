package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type prGitHub struct{ prReads atomic.Int32 }

func (g *prGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch p := r.URL.Path; {
	case p == "/repos/acme/widgets/pulls/7":
		g.prReads.Add(1)
		fmt.Fprint(w, `{"number":7,"title":"Fix login","state":"open","mergeable_state":"clean",
			"head":{"ref":"octopus/t1","sha":"abc123"},"base":{"ref":"main"},
			"changed_files":5,"additions":120,"deletions":30,"commits":3,"updated_at":"2026-10-06T10:00:00Z"}`)
	case p == "/repos/acme/widgets/pulls/7/reviews":
		fmt.Fprint(w, `[{"user":{"login":"ana"},"state":"APPROVED"}]`)
	case strings.HasSuffix(p, "/check-runs"):
		fmt.Fprint(w, `{"total_count":2,"check_runs":[{"status":"completed","conclusion":"success"},{"status":"in_progress"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}
}

type prRowView struct {
	ProjectID    string          `json:"project_id"`
	ProjectName  string          `json:"project_name"`
	URL          string          `json:"url"`
	Number       int             `json:"number"`
	Title        string          `json:"title"`
	State        string          `json:"state"`
	CI           github.CIStatus `json:"ci"`
	Review       string          `json:"review"`
	Mergeable    string          `json:"mergeable"`
	ChangedFiles int             `json:"changed_files"`
	Error        string          `json:"error"`
	ErrorMessage string          `json:"error_message"`
}

func getPullRequests(t *testing.T, h http.Handler, taskID, token string) (int, []prRowView) {
	t.Helper()
	w := getJSON(t, h, "/tasks/"+taskID+"/pull-requests", token)
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var out struct {
		PullRequests []prRowView `json:"pull_requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return w.Code, out.PullRequests
}

func TestTaskPullRequests(t *testing.T) {
	gh := &prGitHub{}
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	srv.gh.tokens = &sourcedTokens{source: githubapp.SourceStored}

	token, _ := mintWorkspaceUser(t, srv, "prs-a@test.com", "PRA")
	otherToken, _ := mintWorkspaceUser(t, srv, "prs-b@test.com", "PRB")
	proj := createProject(t, h, token, "widgets")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "fix login", "project_ids": []string{proj}})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", cw.Code, cw.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(cw.Body).Decode(&created)
	const prURL = "https://github.com/acme/widgets/pull/7"
	if _, err := srv.taskStore.Update(t.Context(), created.ID, task.Patch{PRURLs: map[string]string{
		proj:       prURL,
		"zz-other": "https://example.com/acme/widgets/pull/8",
	}}); err != nil {
		t.Fatal(err)
	}

	code, rows := getPullRequests(t, h, created.ID, token)
	if code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("GET = %d, %d rows, want 200 and 2", code, len(rows))
	}
	got := rows[0]
	if got.ProjectID != proj || got.ProjectName != "widgets" || got.URL != prURL || got.Number != 7 ||
		got.State != github.PRStateOpen || got.Review != github.ReviewApproved ||
		got.Mergeable != github.MergeableReady || got.ChangedFiles != 5 || got.Error != "" {
		t.Errorf("row = %+v", got)
	}
	wantCI := github.CIStatus{Source: github.CISourceChecks, Total: 2, Passed: 1, Running: 1, State: github.CIStateRunning}
	if got.CI != wantCI {
		t.Errorf("ci = %+v, want %+v", got.CI, wantCI)
	}
	bad := rows[1]
	if bad.URL != "https://example.com/acme/widgets/pull/8" || bad.Error != "api.pull_requests.errors.not_a_pull_request" ||
		bad.ErrorMessage == "" || bad.Number != 0 {
		t.Errorf("error row = %+v", bad)
	}

	if code, _ := getPullRequests(t, h, created.ID, otherToken); code != http.StatusNotFound {
		t.Errorf("other workspace = %d, want 404", code)
	}

	getPullRequests(t, h, created.ID, token)
	if n := gh.prReads.Load(); n != 1 {
		t.Fatalf("pull request read %d times, want 1 (cached)", n)
	}

	for _, repo := range []string{"acme/other", "acme/widgets"} {
		payload := map[string]any{
			"action":     "completed",
			"repository": map[string]any{"full_name": repo},
			"check_run":  map[string]any{"head_sha": "abc123", "status": "completed"},
		}
		if code := deliverWebhook(t, h, "check_run", payload); code != http.StatusNoContent {
			t.Fatalf("check_run delivery = %d, want 204", code)
		}
		getPullRequests(t, h, created.ID, token)
	}
	if n := gh.prReads.Load(); n != 2 {
		t.Errorf("pull request read %d times after the check_run deliveries, want 2", n)
	}
}

func TestPRStatusCache(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	c := &prStatusCache{now: func() time.Time { return now }}
	st := func(sha, ref string) *github.PRStatus { return &github.PRStatus{HeadSHA: sha, Head: ref} }
	put := func(url string, number int, sha, ref string) {
		c.put(c.generation(), url, "Acme", "Widgets", number, st(sha, ref))
	}
	has := func(url string) bool { _, ok := c.get(url); return ok }

	put("u1", 1, "s1", "b1")
	if !has("u1") {
		t.Fatal("fresh entry missed")
	}
	now = now.Add(prStatusTTL)
	if has("u1") {
		t.Fatal("expired entry served")
	}

	gen := c.generation()
	c.invalidate("acme/widgets", []int{1}, nil, nil)
	c.put(gen, "u1", "acme", "widgets", 1, st("s1", "b1"))
	if has("u1") {
		t.Fatal("a put from before an invalidation was stored")
	}

	put("u1", 1, "s1", "b1")
	put("u2", 2, "s2", "b2")
	put("u3", 3, "s3", "b3")
	put("u4", 4, "s4", "b4")
	c.invalidate("ACME/widgets", []int{1}, nil, nil)
	c.invalidate("acme/widgets", nil, []string{"s2"}, nil)
	c.invalidate("acme/widgets", nil, nil, []string{"b3"})
	if has("u1") || has("u2") || has("u3") || !has("u4") {
		t.Fatalf("targeted invalidation: u1=%v u2=%v u3=%v u4=%v", has("u1"), has("u2"), has("u3"), has("u4"))
	}
	c.invalidate("acme/other", nil, nil, nil)
	if !has("u4") {
		t.Fatal("another repository's delivery dropped the entry")
	}
	c.invalidate("acme/widgets", nil, nil, nil)
	if has("u4") {
		t.Fatal("a delivery naming no pull request left the repository's entry")
	}

	for i := range prStatusCacheMax + 1 {
		now = now.Add(time.Millisecond)
		put(fmt.Sprintf("bulk-%d", i), i, "", "")
	}
	if len(c.entries) != prStatusCacheMax || has("bulk-0") || !has(fmt.Sprintf("bulk-%d", prStatusCacheMax)) {
		t.Errorf("bounded cache: %d entries, bulk-0 kept=%v", len(c.entries), has("bulk-0"))
	}
}
