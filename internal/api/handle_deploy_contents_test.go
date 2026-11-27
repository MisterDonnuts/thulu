package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/deploycontents"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// pusher commits to a clone of the project's remote and pushes main, the way a merge on the forge
// would land.
type pusher struct {
	t   *testing.T
	dir string
}

func newPusher(t *testing.T, remote string) pusher {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	p := pusher{t: t, dir: filepath.Dir(dir)}
	p.git("clone", "-q", remote, dir)
	p.dir = dir
	p.git("config", "user.email", "test@example.com")
	p.git("config", "user.name", "test")
	return p
}

func (p pusher) git(args ...string) string {
	p.t.Helper()
	cmd := exec.CommandContext(p.t.Context(), "git", args...)
	cmd.Dir = p.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (p pusher) commit(file, msg string) string {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, file), []byte(msg+"\n"), 0o644); err != nil {
		p.t.Fatal(err)
	}
	p.git("add", ".")
	p.git("commit", "-q", "-m", msg)
	p.git("push", "-q", "origin", "HEAD:main")
	return p.git("rev-parse", "HEAD")
}

func (p pusher) revert(sha string) string {
	p.t.Helper()
	p.git("revert", "--no-edit", sha)
	p.git("push", "-q", "origin", "HEAD:main")
	return p.git("rev-parse", "HEAD")
}

type deployContentsView struct {
	deploycontents.Report
	ProjectID string `json:"project_id"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
}

func getDeployContents(t *testing.T, h http.Handler, token, path string) (int, deployContentsView) {
	t.Helper()
	w := getJSON(t, h, path, token)
	var v deployContentsView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
	}
	return w.Code, v
}

// A project on GitHub with a credential is answered by the compare API, never the clone.
func TestDeployContents_GitHub(t *testing.T) {
	const deployed = "9f2c1e7"
	var mu sync.Mutex
	var compares []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		basehead, ok := strings.CutPrefix(r.URL.Path, "/repos/acme/widgets/compare/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		compares = append(compares, basehead)
		mu.Unlock()
		status := "ahead"
		if strings.HasPrefix(basehead, "bbbbbbb") {
			status = "diverged"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "total_commits": 0, "commits": []any{}})
	}))
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	e := newMergeEnv(t)
	e.srv.gh.tokens = &sourcedTokens{source: githubapp.SourceStored}
	token, wsID := mintWorkspaceUser(t, e.srv, "deploy-gh@test.com", "DeployGH")
	projID := createProject(t, e.h, token, "widgets")
	if _, err := e.srv.projectStore.DB().Exec(`UPDATE projects SET repo_url = $1 WHERE id = $2`, "https://github.com/acme/widgets", projID); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		tk := &task.Task{Title: sha[:1], WorkspaceID: wsID, BoardID: mtBoard, Stage: mtRelease, ProjectIDs: []string{projID}}
		if err := e.srv.taskStore.Create(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.srv.taskStore.SetMergeSHA(t.Context(), tk.ID, projID, sha); err != nil {
			t.Fatal(err)
		}
		ids[sha[:1]] = tk.ID
	}
	code, rep := getDeployContents(t, e.h, token, "/projects/"+projID+"/deploy-contents?sha="+deployed)
	if code != http.StatusOK || rep.Source != "github" {
		t.Fatalf("GET = %d, source %q; want 200 from github", code, rep.Source)
	}
	got := map[string]deploycontents.Included{}
	for _, r := range rep.Tasks {
		got[r.TaskID] = r.Included
	}
	if got[ids["a"]] != deploycontents.IncludedYes || got[ids["b"]] != deploycontents.IncludedNo {
		t.Errorf("included = %v, want a yes and b no", got)
	}
	if len(compares) != 2 {
		t.Errorf("compare calls = %v, want one per task", compares)
	}
}

// From the daemon's clone (the project's remote is not on GitHub), with a two-repo task, a task
// that merged elsewhere, one with no merge, and one still in review.
func TestDeployContents_LocalGit(t *testing.T) {
	e := newMergeEnv(t)
	token, wsID := mintWorkspaceUser(t, e.srv, "deploy@test.com", "Deploy")
	otherToken, _ := mintWorkspaceUser(t, e.srv, "deploy-other@test.com", "Other")
	projID := createProject(t, e.h, token, "widgets")
	proj, err := e.srv.projectStore.Get(t.Context(), projID)
	if err != nil {
		t.Fatal(err)
	}
	push := newPusher(t, proj.RepoURL)
	shaA := push.commit("a.txt", "task a")
	shaB := push.commit("b.txt", "task b")

	ctx := t.Context()
	mk := func(title string, stage types.Stage, shas map[string]string) *task.Task {
		t.Helper()
		ids := []string{projID}
		if shas["other-project"] != "" {
			ids = append(ids, "other-project")
		}
		tk := &task.Task{Title: title, WorkspaceID: wsID, BoardID: mtBoard, Stage: stage, ProjectIDs: ids}
		if err := e.srv.taskStore.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		for pid, sha := range shas {
			if _, _, err := e.srv.taskStore.SetMergeSHA(ctx, tk.ID, pid, sha); err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	a := mk("a", mtRelease, map[string]string{projID: shaA, "other-project": strings.Repeat("c", 40)})
	b := mk("b", mtRelease, map[string]string{projID: shaB})
	unmerged := mk("unmerged here", mtRelease, map[string]string{"other-project": strings.Repeat("d", 40)})
	review := mk("in review", mtReview, map[string]string{projID: shaB})

	base := "/projects/" + projID + "/deploy-contents"
	code, rep := getDeployContents(t, e.h, token, base+"?sha="+shaA)
	if code != http.StatusOK || rep.Source != "git" || rep.ProjectID != projID {
		t.Fatalf("GET = %d source %q project %q", code, rep.Source, rep.ProjectID)
	}
	got := map[string]deploycontents.TaskResult{}
	for _, r := range rep.Tasks {
		got[r.TaskID] = r
	}
	if len(got) != 3 {
		t.Errorf("listed %d tasks, want the 3 in the merge_target stage", len(got))
	}
	if _, listed := got[review.ID]; listed {
		t.Error("a task still in review was listed")
	}
	if r := got[a.ID]; r.Included != deploycontents.IncludedYes || r.MergeSHA != shaA {
		t.Errorf("two-repo task a = %+v, want included with this project's merge sha", r)
	}
	if r := got[b.ID]; r.Included != deploycontents.IncludedNo {
		t.Errorf("task b (merged after the deploy) = %+v, want not included", r)
	}
	if r := got[unmerged.ID]; r.Included != deploycontents.IncludedUnknown || r.Reason != deploycontents.ReasonNoMergeSHA {
		t.Errorf("task merged only in another repo = %+v, want unknown/no_merge_sha", r)
	}

	revert := push.revert(shaA)
	_, rep = getDeployContents(t, e.h, token, base+"?sha="+revert+"&prev="+shaA)
	got = map[string]deploycontents.TaskResult{}
	for _, r := range rep.Tasks {
		got[r.TaskID] = r
	}
	if r := got[a.ID]; !r.Reverted || !r.AlreadyDeployed {
		t.Errorf("task a after its revert = %+v, want reverted and already deployed", r)
	}
	if r := got[b.ID]; r.Included != deploycontents.IncludedYes || r.AlreadyDeployed {
		t.Errorf("task b = %+v, want newly included", r)
	}
	if len(rep.Reverts) != 1 || rep.Reverts[0].TaskID != a.ID || rep.Reverts[0].SHA != revert {
		t.Errorf("reverts = %+v, want the revert of task a", rep.Reverts)
	}

	_, rep = getDeployContents(t, e.h, token, base+"?sha="+shaB+"&stage="+string(mtReview))
	if len(rep.Tasks) != 1 || rep.Tasks[0].TaskID != review.ID {
		t.Errorf("?stage=review listed %+v, want only the task in review", rep.Tasks)
	}

	for _, q := range []string{"", "?sha=main", "?sha=" + shaA + "&prev=--all"} {
		if code, _ := getDeployContents(t, e.h, token, base+q); code != http.StatusBadRequest {
			t.Errorf("GET %q = %d, want 400", q, code)
		}
	}
	if code, _ := getDeployContents(t, e.h, otherToken, base+"?sha="+shaA); code != http.StatusNotFound {
		t.Errorf("another workspace = %d, want 404", code)
	}
}

// The cap counts only the tasks that qualify: tasks in a stage of the same name on a board where it
// is not the merge_target must not use it up before the filter drops them.
func TestDeployCandidates_CapAppliesAfterTheBoardFilter(t *testing.T) {
	e := newMergeEnv(t)
	ctx := t.Context()
	const other = "mt_other"
	if err := e.srv.stageAdmin.CreateBoard(ctx, stagepkg.BoardInfo{ID: other, Label: "Other", WorkspaceID: mergeWS}, []types.StageInfo{
		{ID: "mto_backlog", Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: mtRelease, Label: "Release", Color: "#6fcf97"},
		{ID: "mto_done", Label: "Done", Color: "#3aa667", Terminal: true},
	}); err != nil {
		t.Fatal(err)
	}
	p := e.project(t, "app")
	mk := func(board string) *task.Task {
		t.Helper()
		tk := &task.Task{Title: board, WorkspaceID: mergeWS, BoardID: board, Stage: mtRelease, ProjectIDs: []string{p.ID}}
		if err := e.srv.taskStore.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	// Listed newest first: the one that counts is the oldest.
	want := mk(mtBoard)
	for range deployContentsMax + 1 {
		mk(other)
	}
	cands, truncated, err := e.srv.deployCandidates(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].TaskID != want.ID || truncated {
		t.Errorf("candidates = %d (truncated %v), want only %s", len(cands), truncated, want.ID)
	}
}
